//go:build libvips

// Реализация libvips-движка (govips). Файл компилируется ТОЛЬКО с тэком
// "libvips" (см. также process_stub.go для сборки без тэка).
//
// Здесь сосредоточена вся cgo-зависимая логика: govips.Startup (один раз
// на процесс), загрузка изображений, применение плана и экспорт.
package libvips

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/pkg-ru/imager/internal/adapters/processor/detection"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

// startupOnce гарантирует однократный Startup govips на процесс.
var (
	startupOnce sync.Once
	startupErr  error
	// shutdownOnce защищает от повторных вызовов vips.Shutdown.
	shutdownOnce sync.Once
)

// libvipsBackend — реальный движок обработки через govips.
type libvipsBackend struct {
	opts Options
}

var _ backend = (*libvipsBackend)(nil)

func init() {
	// Связываем фабрику движков общего кода (processor.go) с реальной
	// реализацией govips.
	newBackend = newLibvipsBackend
}

// Compiled сообщает, скомпилирована ли реальная поддержка libvips (govips).
// Возвращает true в сборках с тэком "libvips".
func Compiled() bool { return true }

// newLibvipsBackend создаёт движок и выполняет однократный Startup govips с
// конфигурацией из Limits (ConcurrencyLevel, MaxCacheMem/Files/Size).
func newLibvipsBackend(opts Options) (backend, error) {
	startupOnce.Do(func() {
		cfg := &vips.Config{
			ConcurrencyLevel: opts.Limits.Threads,
			MaxCacheMem:      opts.Limits.MaxCacheMem,
			MaxCacheFiles:    opts.Limits.MaxCacheFiles,
			MaxCacheSize:     opts.Limits.MaxCacheSize,
		}
		startupErr = vips.Startup(cfg)
	})
	if startupErr != nil {
		return nil, fmt.Errorf("libvips: startup: %w", startupErr)
	}
	return &libvipsBackend{opts: opts}, nil
}

func (b *libvipsBackend) close() error {
	// vips.Shutdown() безопасен только когда все ImageRef закрыты. Адаптер
	// не держит глобальных изображений между вызовами Process, поэтому
	// завершаем libvips идемпотентно.
	shutdownOnce.Do(func() {
		vips.Shutdown()
	})
	return nil
}

// process загружает изображение из данных, применяет план и экспортирует в
// требуемый формат.
func (b *libvipsBackend) process(ctx context.Context, data []byte, plan *processing.ProcessingPlan) ([]byte, error) {
	// Отмена контекста проверяется перед затратной работой.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("libvips: plan: %w", err)
	}

	img, err := b.load(ctx, data, plan)
	if err != nil {
		return nil, err
	}
	defer img.Close()

	// К2: проверка отмены контекста между стадиями.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Ориентация (EXIF auto-orient уже применён при загрузке; здесь —
	// ручной rotate/flip) применяется СТРОГО до resize/crop/trim, чтобы
	// поворот/отражение не искажали геометрию последующих операций.
	// Для вертикального flip анимации создаётся новый ImageRef (старый
	// закрывается внутри applyOrientation).
	img, err = b.applyOrientation(ctx, img, plan)
	if err != nil {
		return nil, err
	}

	if err := b.applyOperation(ctx, img, plan); err != nil {
		return nil, err
	}

	// Ватермарка (nil = не применяется): накладывается ПОСЛЕ операции
	// (resize/crop/trim) — размер холста уже целевой, ДО экспорта.
	// Для анимации ватермарка накладывается на КАЖДЫЙ кадр, результатом
	// может быть новое изображение (см. applyWatermark).
	if plan.Watermark != nil {
		wmImg, err := b.applyWatermark(img, plan)
		if err != nil {
			return nil, err
		}
		if wmImg != img {
			img.Close()
			img = wmImg
		}
	}

	// К2: проверка отмены контекста перед экспортом.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Анимация: loop/delay применяются только для анимированных выходов.
	if plan.OutputFormat.Animated() {
		if err := b.applyAnimation(ctx, img, plan); err != nil {
			return nil, err
		}
	}

	out, err := b.exportImage(img, plan)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// load загружает изображение из памяти. FailOnError всегда включён;
// AutoRotate (EXIF orientation) управляется планом: nil-спецификация =
// включён (историческое поведение). Для анимированных входов/выходов
// (включая APNG) загружаются все кадры (NumPages=-1).
func (b *libvipsBackend) load(ctx context.Context, data []byte, plan *processing.ProcessingPlan) (*vips.ImageRef, error) {
	params := vips.NewImportParams()
	params.AutoRotate.Set(plan.Orientation == nil || plan.Orientation.AutoOrient)
	params.FailOnError.Set(true)
	if plan.OutputFormat.Animated() || plan.SourceFormat.Animated() {
		params.NumPages.Set(-1)
	}
	img, err := vips.LoadImageFromBuffer(data, params)
	if err != nil {
		return nil, fmt.Errorf("libvips: load: %w", err)
	}
	return img, nil
}

// applyOrientation применяет ручные rotate/flip из плана. EXIF auto-orient
// уже применён при загрузке (см. load). Операции выполняются СТРОГО до
// resize/crop/trim (вызывается из process до applyOperation).
//
// Порядок: rotate → flip. govips Rotate корректно обрабатывает
// многостраничные изображения (Grid для 90/270, поворот всего стека для
// 180). Горизонтальный flip корректен на вертикальном стеке кадров (каждый
// кадр зеркалится независимо); вертикальный flip требует покадровой
// обработки — иначе перевернётся весь стек и порядок кадров сломается.
//
// Возвращает актуальный ImageRef: для вертикального flip многостраничного
// изображения создаётся новый (старый закрывается здесь же), в остальных
// случаях — тот же img.
func (b *libvipsBackend) applyOrientation(ctx context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan) (*vips.ImageRef, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	or := plan.Orientation
	if or == nil || or.IsZero() {
		return img, nil
	}

	if or.Rotate != processing.RotationNone {
		var angle vips.Angle
		switch or.Rotate {
		case processing.Rotation90:
			angle = vips.Angle90
		case processing.Rotation180:
			angle = vips.Angle180
		case processing.Rotation270:
			angle = vips.Angle270
		default:
			return nil, fmt.Errorf("libvips: unsupported rotation %d", int(or.Rotate))
		}
		if err := img.Rotate(angle); err != nil {
			return nil, fmt.Errorf("libvips: rotate %s: %w", or.Rotate.String(), err)
		}
	}

	switch or.Flip {
	case processing.FlipHorizontal:
		if err := img.Flip(vips.DirectionHorizontal); err != nil {
			return nil, fmt.Errorf("libvips: flip horizontal: %w", err)
		}
	case processing.FlipVertical:
		newImg, err := flipVertical(img)
		if err != nil {
			return nil, fmt.Errorf("libvips: flip vertical: %w", err)
		}
		if newImg != img {
			img.Close()
			img = newImg
		}
	}
	return img, nil
}

// flipVertical отражает изображение сверху-вниз. Для многостраничных
// изображений (анимации) применяется покадрово: вертикальный flip всего
// стека перевернул бы порядок кадров. Для одиночных изображений — прямой
// vips_flip.
func flipVertical(img *vips.ImageRef) (*vips.ImageRef, error) {
	n := img.Pages()
	if n <= 1 {
		if err := img.Flip(vips.DirectionVertical); err != nil {
			return nil, err
		}
		return img, nil
	}
	ph := img.PageHeight()
	W := img.Width()
	H := img.Height()
	if ph <= 0 || H <= ph {
		if err := img.Flip(vips.DirectionVertical); err != nil {
			return nil, err
		}
		return img, nil
	}

	delay, _ := img.PageDelay()
	loop := img.Loop()

	frames := make([]*vips.ImageRef, 0, n)
	closeFrames := func(keepFirst bool) {
		for i, f := range frames {
			if i == 0 && keepFirst {
				continue
			}
			f.Close()
		}
	}
	for i := 0; i < n; i++ {
		f, err := img.Copy()
		if err != nil {
			closeFrames(false)
			return nil, fmt.Errorf("copy frame %d/%d: %w", i+1, n, err)
		}
		// Высота страницы = высота всего стека: ExtractArea вырезает ровно
		// один кадр по смещению i*ph (как в compositeWatermarkPerFrame).
		if err := f.SetPageHeight(H); err != nil {
			f.Close()
			closeFrames(false)
			return nil, fmt.Errorf("set page height of frame %d/%d: %w", i+1, n, err)
		}
		if err := f.ExtractArea(0, i*ph, W, ph); err != nil {
			f.Close()
			closeFrames(false)
			return nil, fmt.Errorf("extract frame %d/%d: %w", i+1, n, err)
		}
		if err := f.Flip(vips.DirectionVertical); err != nil {
			f.Close()
			closeFrames(false)
			return nil, fmt.Errorf("flip frame %d/%d: %w", i+1, n, err)
		}
		frames = append(frames, f)
	}

	base := frames[0]
	if len(frames) > 1 {
		if err := base.ArrayJoin(frames[1:], 1); err != nil {
			closeFrames(true)
			return nil, fmt.Errorf("join %d frames: %w", len(frames), err)
		}
	}
	if err := base.SetPageHeight(ph); err != nil {
		base.Close()
		closeFrames(false)
		return nil, fmt.Errorf("restore page height: %w", err)
	}
	if len(delay) > 0 {
		if err := base.SetPageDelay(delay); err != nil {
			base.Close()
			closeFrames(false)
			return nil, fmt.Errorf("restore page delay: %w", err)
		}
	}
	if err := base.SetLoop(loop); err != nil {
		base.Close()
		closeFrames(false)
		return nil, fmt.Errorf("restore loop: %w", err)
	}
	closeFrames(true)
	return base, nil
}

// applyOperation применяет операцию из плана к изображению.
func (b *libvipsBackend) applyOperation(ctx context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Size.Original (size=x): размер не меняем.
	if plan.Size.Original {
		return nil
	}

	w, h := plan.Size.Width, plan.Size.Height
	switch plan.Operation {
	case processing.OpResize:
		// Пропорциональное изменение размера (без обрезки).
		if err := img.ThumbnailWithSize(w, h, vips.InterestingNone, vips.SizeBoth); err != nil {
			return fmt.Errorf("libvips: resize: %w", err)
		}
	case processing.OpCrop:
		// Центрированная обрезка до точного размера.
		if err := img.ThumbnailWithSize(w, h, vips.InterestingCentre, vips.SizeForce); err != nil {
			return fmt.Errorf("libvips: crop: %w", err)
		}
	case processing.OpSmartCrop:
		// Умная обрезка: внимание (attention) libvips — центр тяжести
		// изображения; масштаб и кроп до точного размера одним проходом.
		if err := img.ThumbnailWithSize(w, h, vips.InterestingAttention, vips.SizeForce); err != nil {
			return fmt.Errorf("libvips: smart-crop: %w", err)
		}
	case processing.OpFaceCrop:
		fallthrough
	case processing.OpObjectCrop:
		// Детекторная обрезка (лица/объекты): находится область интереса
		// (детектор + selectCrop), вырезается и подгоняется до целевого
		// размера.
		if err := b.applyDetectionCrop(ctx, img, plan); err != nil {
			return err
		}
	case processing.OpTrim:
		if err := applyTrim(img, "trim"); err != nil {
			return err
		}
	case processing.OpCropTrim:
		// Сначала trim, затем crop.
		if err := applyTrim(img, "crop-trim"); err != nil {
			return err
		}
		if err := img.ThumbnailWithSize(w, h, vips.InterestingCentre, vips.SizeForce); err != nil {
			return fmt.Errorf("libvips: crop-trim: crop: %w", err)
		}
	case processing.OpSmartCropTrim:
		// Сначала trim, затем smart-crop: внимание (attention) применяется
		// уже к подрезанному изображению.
		if err := applyTrim(img, "smart-crop-trim"); err != nil {
			return err
		}
		if err := img.ThumbnailWithSize(w, h, vips.InterestingAttention, vips.SizeForce); err != nil {
			return fmt.Errorf("libvips: smart-crop-trim: smart-crop: %w", err)
		}
	case processing.OpFaceCropTrim:
		fallthrough
	case processing.OpObjectCropTrim:
		// Сначала trim, затем детекторная обрезка. Детекция выполняется на
		// УЖЕ подрезанном изображении, поэтому координаты боксов детектора
		// относятся к подрезанному изображению.
		if err := applyTrim(img, string(plan.Operation)); err != nil {
			return err
		}
		if err := b.applyDetectionCrop(ctx, img, plan); err != nil {
			return err
		}
	default:
		return fmt.Errorf("libvips: unsupported operation %q", plan.Operation)
	}
	return nil
}

// applyTrim выполняет обрезку однотонных/пустых краёв изображения по контенту
// (vips_find_trim + ExtractArea). Возвращает ошибку, если область трима пуста.
func applyTrim(img *vips.ImageRef, op string) error {
	left, top, tw, th, err := img.FindTrim(0.0, nil)
	if err != nil {
		return fmt.Errorf("libvips: %s: find-trim: %w", op, err)
	}
	if tw <= 0 || th <= 0 {
		return fmt.Errorf("libvips: %s: empty trim area (%dx%d)", op, tw, th)
	}
	if err := img.ExtractArea(left, top, tw, th); err != nil {
		return fmt.Errorf("libvips: %s: trim: %w", op, err)
	}
	return nil
}

// applyDetectionCrop выполняет детекторную обрезку (face-crop/object-crop).
//
// Алгоритм:
//  1. Проверяется доступность детектора (b.opts.Detector). Если детектор
//     не сконфигурирован (nil) или не готов (Available() false) — понятная
//     ошибка: операция требует настроенной модели в секции detection.*.
//  2. Изображение приводится к sRGB/uchar и извлекаются RGB-пиксели
//     (3 байта на пиксель, порядок R,G,B) для передачи в детектор.
//  3. Детектор находит боксы (лица или объекты); selectCrop выбирает
//     область кропа с учётом целевого aspect ratio и отступа margin.
//  4. Область вырезается (ExtractArea) и подгоняется до целевого размера
//     (ThumbnailWithSize, SizeForce).
//
// Для анимированных изображений детекция выполняется по первому кадру
// (PageHeight), а область применяется ко всему стеку кадров — это
// согласовано с поведением trim/crop для анимации.
func (b *libvipsBackend) applyDetectionCrop(ctx context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	det := b.opts.Detector
	if det == nil || !det.Available() {
		return fmt.Errorf("libvips: %s: detection is not configured; set detection.face-model / detection.object-model and rebuild with -tags onnx", plan.Operation)
	}

	// Размеры кадра: для анимации используем высоту одного кадра.
	W := img.Width()
	H := img.Height()
	if ph := img.PageHeight(); img.Pages() > 1 && ph > 0 && H > ph {
		H = ph
	}

	// Извлечение RGB-пикселей: работаем на копии, чтобы не менять исходник.
	tmp, err := img.Copy()
	if err != nil {
		return fmt.Errorf("libvips: %s: copy: %w", plan.Operation, err)
	}
	defer tmp.Close()
	if err := tmp.ToColorSpace(vips.InterpretationSRGB); err != nil {
		return fmt.Errorf("libvips: %s: to-srgb: %w", plan.Operation, err)
	}
	if err := tmp.Cast(vips.BandFormatUchar); err != nil {
		return fmt.Errorf("libvips: %s: cast: %w", plan.Operation, err)
	}
	// Для анимации берём только первый кадр (высота H).
	if H < img.Height() {
		if err := tmp.ExtractArea(0, 0, W, H); err != nil {
			return fmt.Errorf("libvips: %s: extract first frame: %w", plan.Operation, err)
		}
	}
	// Приводим к 3 каналам (RGB), если есть альфа.
	if tmp.Bands() > 3 {
		if err := tmp.ExtractBand(0, 3); err != nil {
			return fmt.Errorf("libvips: %s: extract rgb: %w", plan.Operation, err)
		}
	}
	rgb, err := tmp.ToBytes()
	if err != nil {
		return fmt.Errorf("libvips: %s: to-bytes: %w", plan.Operation, err)
	}

	// Детекция.
	var boxes []detection.Box
	switch plan.Operation {
	case processing.OpFaceCrop, processing.OpFaceCropTrim:
		boxes, err = det.DetectFaces(ctx, rgb, W, H)
	case processing.OpObjectCrop, processing.OpObjectCropTrim:
		boxes, err = det.DetectObjects(ctx, rgb, W, H)
	}
	if err != nil {
		return fmt.Errorf("libvips: %s: detect: %w", plan.Operation, err)
	}

	// Выбор области кропа и применение.
	rect := detection.SelectCrop(boxes, W, H, plan.Size.Width, plan.Size.Height, b.opts.DetectorMargin)
	if err := img.ExtractArea(rect.X, rect.Y, rect.W, rect.H); err != nil {
		return fmt.Errorf("libvips: %s: extract area (%d,%d %dx%d): %w", plan.Operation, rect.X, rect.Y, rect.W, rect.H, err)
	}
	if err := img.ThumbnailWithSize(plan.Size.Width, plan.Size.Height, vips.InterestingCentre, vips.SizeForce); err != nil {
		return fmt.Errorf("libvips: %s: resize to %dx%d: %w", plan.Operation, plan.Size.Width, plan.Size.Height, err)
	}
	return nil
}

// applyAnimation применяет настройки анимации (loop) для анимированных
// выходных форматов (GIF, WebP).
//
// TODO(libvips-animation): ограничение plan.Frames (максимальное число
// кадров) и plan.Duration (максимальная длительность) требует обрезки
// массива кадров после загрузки (Pages/PageDelay) и переноса задержек.
// В текущей версии govips-биндинга безопасное ограничение кадров на этапе
// загрузки (Page/NumPages) не покрывает диапазон "первые N кадров" —
// реализация оставлена на будущее. Loop применяется ниже.
func (b *libvipsBackend) applyAnimation(_ context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan) error {
	_ = plan.Frames
	_ = plan.Duration

	// Loop: nil = оставить как есть (по умолчанию исходника).
	if plan.Loop != nil {
		loop := 0 // 0 = бесконечно
		if !*plan.Loop {
			loop = 1
		}
		if err := img.SetLoop(loop); err != nil {
			return fmt.Errorf("libvips: set-loop: %w", err)
		}
	}
	// PageDelay сохраняется из исходника автоматически при загрузке всех
	// кадров (NumPages=-1); явная перезапись не требуется.
	return nil
}

// stripAllMetadata принудительно удаляет все пользовательские метаданные
// (EXIF/GPS, XMP, IPTC, описания) и ICC-профиль перед экспортом.
//
// Вызывается для ВСЕХ форматов как defense-in-depth: часть кодеков libvips
// (heifsave, jxlsave) не поддерживает опцию strip и копирует метаданные
// исходника в выходной файл. govips RemoveMetadata сохраняет технические
// поля (orientation, n-pages/page-height/delay/loop), необходимые для
// корректного отображения; orientation к этому моменту уже применён при
// загрузке (AutoRotate). RemoveICCProfile удаляет цветовой профиль —
// консистентно с ImageMagick -strip.
func stripAllMetadata(img *vips.ImageRef) error {
	if err := img.RemoveMetadata(); err != nil {
		return fmt.Errorf("libvips: remove metadata: %w", err)
	}
	if err := img.RemoveICCProfile(); err != nil {
		return fmt.Errorf("libvips: remove icc profile: %w", err)
	}
	return nil
}

// exportImage экспортирует изображение в целевой формат.
func (b *libvipsBackend) exportImage(img *vips.ImageRef, plan *processing.ProcessingPlan) ([]byte, error) {
	// Единая принудительная зачистка метаданных на готовом ассете — до
	// экспорта, независимо от поддержки strip конкретным кодеком.
	if err := stripAllMetadata(img); err != nil {
		return nil, err
	}
	switch plan.OutputFormat {
	case processing.FormatJPEG:
		p := vips.NewJpegExportParams()
		p.Quality = plan.Quality
		p.StripMetadata = true
		p.SubsampleMode = vips.VipsForeignSubsampleOn
		out, _, err := img.ExportJpeg(p)
		return out, err
	case processing.FormatPNG:
		p := vips.NewPngExportParams()
		p.StripMetadata = true
		p.Compression = 6
		out, _, err := img.ExportPng(p)
		return out, err
	case processing.FormatWebP:
		p := vips.NewWebpExportParams()
		p.Quality = plan.Quality
		p.StripMetadata = true
		p.ReductionEffort = 4
		out, _, err := img.ExportWebp(p)
		return out, err
	case processing.FormatGIF:
		p := vips.NewGifExportParams()
		p.Dither = 1.0
		out, _, err := img.ExportGIF(p)
		return out, err
	case processing.FormatAVIF:
		p := vips.NewAvifExportParams()
		p.Quality = plan.Quality
		p.StripMetadata = true
		out, _, err := img.ExportAvif(p)
		return out, err
	case processing.FormatHEIF:
		p := vips.NewHeifExportParams()
		p.Quality = plan.Quality
		out, _, err := img.ExportHeif(p)
		return out, err
	case processing.FormatJPEGXL:
		p := vips.NewJxlExportParams()
		p.Quality = plan.Quality
		out, _, err := img.ExportJxl(p)
		return out, err
	case processing.FormatAPNG:
		// APNG — надмножество PNG: pngsave автоматически пишет анимацию
		// (acTL/fcTL/fdAT чанки) для multi-page изображений (кадры загружены
		// с NumPages=-1, page-height < высоты стека). Для одиночного
		// изображения результат — статичный PNG, который также является
		// валидным APNG (без анимации). Метаданные анимации (delay/loop)
		// сохраняются из исходника (см. applyAnimation).
		p := vips.NewPngExportParams()
		p.StripMetadata = true
		p.Compression = 6
		out, _, err := img.ExportPng(p)
		return out, err
	default:
		return nil, fmt.Errorf("libvips: unsupported output format %q", plan.OutputFormat)
	}
}

// maxWatermarkTiles — защитный лимит числа копий ватермарки на холсте
// (защита от патологических конфигураций тайлинга: крошечный файл при
// repeat покроет холст миллионами копий).
const maxWatermarkTiles = 4096

// wmCacheMu / wmCache — кэш содержимого файлов ватермарок по пути.
// Файлы задаются администратором в конфиге и не меняются в рантайме;
// кэш избавляет от чтения с диска на каждый запрос.
var (
	wmCacheMu sync.RWMutex
	wmCache   = map[string][]byte{}
)

// loadWatermark читает файл ватермарки (с кэшем по пути).
func loadWatermark(path string) ([]byte, error) {
	wmCacheMu.RLock()
	data, ok := wmCache[path]
	wmCacheMu.RUnlock()
	if ok {
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read watermark file: %w", err)
	}
	wmCacheMu.Lock()
	wmCache[path] = data
	wmCacheMu.Unlock()
	return data, nil
}

// applyWatermark накладывает ватермарку из плана на изображение.
//
// Семантика CSS:
//   - size contain/cover/{w}px {h}px — масштабирование одной копии
//     относительно ЦЕЛЕВОГО холста;
//   - position top/bottom/left/right/center — якорь одиночной копии
//     (вторая ось — центр);
//   - repeat no-repeat/repeat/repeat-x/repeat-y/round/space — раскладка
//     копий (см. WatermarkSpec.Layout); round дополнительно масштабирует
//     копию до шага сетки (RoundStep), чтобы копии точно укладывались.
//
// Для анимированных выходов (GIF/WebP/HEIF; кадры хранятся libvips как один
// вертикально сшитый холст с page-height) ватермарка накладывается на КАЖДЫЙ
// кадр: изображение разбирается на кадры, композит применяется к каждому,
// кадры собираются обратно через arrayjoin с восстановлением метаданных
// анимации (page-height, delay, loop). Возвращает изображение-результат:
// для одиночного кадра это тот же img, для анимации — новое (вызывающий
// код обязан закрыть старое).
func (b *libvipsBackend) applyWatermark(img *vips.ImageRef, plan *processing.ProcessingPlan) (*vips.ImageRef, error) {
	wm := plan.Watermark
	data, err := loadWatermark(wm.Path)
	if err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: %w", wm.Name, err)
	}
	wmImg, err := vips.NewImageFromBuffer(data)
	if err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: decode: %w", wm.Name, err)
	}
	defer wmImg.Close()

	W, H := img.Width(), img.Height()
	tw, th := wm.TargetSize(W, H, wmImg.Width(), wmImg.Height())
	// Режим round: копия масштабируется до шага сетки, чтобы целое число
	// копий точно укладывалось по осям холста.
	if wm.RoundScale() {
		tw, th = wm.RoundStep(W, H, tw, th)
	}
	if err := wmImg.ThumbnailWithSize(tw, th, vips.InterestingNone, vips.SizeForce); err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: resize to %dx%d: %w", wm.Name, tw, th, err)
	}

	pts := wm.Layout(W, H, tw, th)
	if len(pts) > maxWatermarkTiles {
		return nil, fmt.Errorf("libvips: watermark %q: too many tiles (%d > %d); increase watermark size or change repeat", wm.Name, len(pts), maxWatermarkTiles)
	}

	// Анимация (кадры = вертикальный стек страниц): покадровый композит.
	// Композит на весь сшитый холст попал бы только в область первого кадра.
	if ph := img.PageHeight(); img.Pages() > 1 && ph > 0 && H > ph {
		out, err := compositeWatermarkPerFrame(img, wmImg, pts, W, ph)
		if err != nil {
			return nil, fmt.Errorf("libvips: watermark %q: animated output %q: %w", wm.Name, plan.OutputFormat, err)
		}
		return out, nil
	}

	for _, pt := range pts {
		if err := img.Composite(wmImg, vips.BlendModeOver, pt.X, pt.Y); err != nil {
			return nil, fmt.Errorf("libvips: watermark %q: composite at (%d,%d): %w", wm.Name, pt.X, pt.Y, err)
		}
	}
	return img, nil
}

// compositeWatermarkPerFrame накладывает ватермарку на каждый кадр
// многокадрового изображения и собирает кадры обратно в вертикальный стек.
//
// Алгоритм:
//  1. До разборки захватываются метаданные анимации (delay, loop) —
//     arrayjoin их не переносит;
//  2. каждый кадр вырезается из лёгкой копии (Copy + ExtractArea);
//     перед ExtractArea высота страницы копии временно устанавливается
//     равной высоте всего стека, чтобы govips выполнил ОБЫЧНЫЙ extract
//     региона (иначе он сам делает мультистраничный extract по всем
//     кадрам сразу);
//  3. к каждому кадру применяется Composite во всех точках раскладки;
//  4. кадры склеиваются ArrayJoin(..., across=1) и восстанавливаются
//     page-height / delay / loop.
func compositeWatermarkPerFrame(img *vips.ImageRef, wmImg *vips.ImageRef, pts []processing.Point, W, ph int) (*vips.ImageRef, error) {
	n := img.Pages()
	H := img.Height()

	delay, _ := img.PageDelay()
	loop := img.Loop()

	frames := make([]*vips.ImageRef, 0, n)
	closeFrames := func(keepFirst bool) {
		for i, f := range frames {
			if i == 0 && keepFirst {
				continue
			}
			f.Close()
		}
	}
	for i := 0; i < n; i++ {
		f, err := img.Copy()
		if err != nil {
			closeFrames(false)
			return nil, fmt.Errorf("copy frame %d/%d: %w", i+1, n, err)
		}
		// Высота страницы = высота всего стека: Height()==PageHeight()
		// переключает ExtractArea на обычный (не мультистраничный) путь,
		// позволяя вырезать ровно один кадр по смещению i*ph.
		if err := f.SetPageHeight(H); err != nil {
			f.Close()
			closeFrames(false)
			return nil, fmt.Errorf("set page height of frame %d/%d: %w", i+1, n, err)
		}
		if err := f.ExtractArea(0, i*ph, W, ph); err != nil {
			f.Close()
			closeFrames(false)
			return nil, fmt.Errorf("extract frame %d/%d: %w", i+1, n, err)
		}
		for _, pt := range pts {
			if err := f.Composite(wmImg, vips.BlendModeOver, pt.X, pt.Y); err != nil {
				f.Close()
				closeFrames(false)
				return nil, fmt.Errorf("composite frame %d/%d at (%d,%d): %w", i+1, n, pt.X, pt.Y, err)
			}
		}
		frames = append(frames, f)
	}

	base := frames[0]
	if len(frames) > 1 {
		if err := base.ArrayJoin(frames[1:], 1); err != nil {
			closeFrames(true)
			return nil, fmt.Errorf("join %d frames: %w", len(frames), err)
		}
	}
	// Метаданные анимации: page-height обязателен (иначе стек читается как
	// один высокий кадр), delay/loop переносятся вручную.
	if err := base.SetPageHeight(ph); err != nil {
		base.Close()
		closeFrames(false)
		return nil, fmt.Errorf("restore page height: %w", err)
	}
	if len(delay) > 0 {
		if err := base.SetPageDelay(delay); err != nil {
			base.Close()
			closeFrames(false)
			return nil, fmt.Errorf("restore page delay: %w", err)
		}
	}
	if err := base.SetLoop(loop); err != nil {
		base.Close()
		closeFrames(false)
		return nil, fmt.Errorf("restore loop: %w", err)
	}
	// Промежуточные кадры больше не нужны (base держит свои ссылки).
	closeFrames(true)
	return base, nil
}
