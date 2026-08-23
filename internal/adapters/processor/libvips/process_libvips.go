//go:build libvips

// Реализация libvips-движка (govips). Файл компилируется ТОЛЬКО с тэком
// "libvips" (см. также process_stub.go для сборки без тэка).
//
// Здесь сосредоточена вся cgo-зависимая логика: govips.Startup (один раз
// на процесс), загрузка изображений, применение плана и экспорт.
package libvips

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"

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

// load загружает изображение из памяти. AutoRotate и FailOnError всегда
// включены; для анимированных входов/выходов загружаются все кадры
// (NumPages=-1).
func (b *libvipsBackend) load(ctx context.Context, data []byte, plan *processing.ProcessingPlan) (*vips.ImageRef, error) {
	params := vips.NewImportParams()
	params.AutoRotate.Set(true)
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
	case processing.OpTrim:
		left, top, tw, th, err := img.FindTrim(0.0, nil)
		if err != nil {
			return fmt.Errorf("libvips: find-trim: %w", err)
		}
		if tw <= 0 || th <= 0 {
			return fmt.Errorf("libvips: trim: empty trim area (%dx%d)", tw, th)
		}
		if err := img.ExtractArea(left, top, tw, th); err != nil {
			return fmt.Errorf("libvips: trim: %w", err)
		}
	case processing.OpCropTrim:
		// Сначала trim, затем crop.
		left, top, tw, th, err := img.FindTrim(0.0, nil)
		if err != nil {
			return fmt.Errorf("libvips: find-trim: %w", err)
		}
		if tw <= 0 || th <= 0 {
			return fmt.Errorf("libvips: crop-trim: empty image dimensions (%dx%d)", tw, th)
		}
		if err := img.ExtractArea(left, top, tw, th); err != nil {
			return fmt.Errorf("libvips: crop-trim: trim: %w", err)
		}
		if err := img.ThumbnailWithSize(w, h, vips.InterestingCentre, vips.SizeForce); err != nil {
			return fmt.Errorf("libvips: crop-trim: crop: %w", err)
		}
	default:
		return fmt.Errorf("libvips: unsupported operation %q", plan.Operation)
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

// exportImage экспортирует изображение в целевой формат.
func (b *libvipsBackend) exportImage(img *vips.ImageRef, plan *processing.ProcessingPlan) ([]byte, error) {
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
		// APNG не поддерживается libvips. Ошибка перехватывается роутингом
		// (routing), который переключается на ImageMagick fallback.
		return nil, errors.New("libvips: APNG is not supported by libvips; use ImageMagick (fallback)")
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
