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
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/pkg-ru/imager/adapters/processor/detection"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/observability"
	"github.com/pkg-ru/imager/ports/processor"
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
	// wmCache — in-memory кэш байтов файлов ватермарок этого движка
	// (LRU + TTL + singleflight; см. watermarkcache.go). Привязан к
	// экземпляру backend, поэтому несколько Processor с разными
	// WatermarkCacheOpts не влияют друг на друга.
	wmCache *watermarkCache
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
	b := &libvipsBackend{
		opts:    opts,
		wmCache: newWatermarkCache(opts.WatermarkCache),
	}
	// Vips-метрики (Фаза 4): регистрируем провайдер снимков libvips +
	// кэша ватермарок этого движка в observability (периодический сборщик,
	// отказоустойчивый). Повторное создание движка заменяет провайдер.
	registerVipsStatsProvider(opts.VipsMetricsInterval, b.wmCache)
	startupOnce.Do(func() {
		// Лимиты кэша (Фаза 5b): при отключённом operation cache передаются
		// НУЛЕВЫЕ значения. В govips 0 означает ПОЛНОЕ ОТКЛЮЧЕНИЕ кэша
		// (vips_cache_set_max_mem(0) / vips_cache_set_max(0) /
		// vips_cache_set_max_files(0)); значение < 0 = default govips.
		cacheMem := opts.Limits.MaxCacheMem
		cacheFiles := opts.Limits.MaxCacheFiles
		cacheSize := opts.Limits.MaxCacheSize
		if !opts.OperationCache.Enabled() {
			cacheMem, cacheFiles, cacheSize = 0, 0, 0
		}
		cfg := &vips.Config{
			ConcurrencyLevel: opts.Limits.Threads,
			MaxCacheMem:      cacheMem,
			MaxCacheFiles:    cacheFiles,
			MaxCacheSize:     cacheSize,
			// CollectStats (Фаза 4): включает счётчик операций govips
			// (ReadRuntimeStats) для метрики imager_vips_operations_total.
			CollectStats: true,
		}
		startupErr = vips.Startup(cfg)
	})
	if startupErr != nil {
		return nil, fmt.Errorf("libvips: startup: %w", startupErr)
	}
	return b, nil
}

func (b *libvipsBackend) close() error {
	// Останавливаем периодический сборщик vips-метрик ДО Shutdown: после
	// vips.Shutdown() cgo-вызовы ReadVipsMemStats/ReadRuntimeStats из
	// провайдера недопустимы, а горутина-collector иначе остаётся жить
	// (утечка + риск падения процесса). Идемпотентно.
	observability.StopVipsMetrics()
	// vips.Shutdown() безопасен только когда все ImageRef закрыты. Адаптер
	// не держит глобальных изображений между вызовами Process, поэтому
	// завершаем libvips идемпотентно.
	shutdownOnce.Do(func() {
		vips.Shutdown()
	})
	return nil
}

// process загружает изображение из данных, применяет план и экспортирует в
// требуемый формат. detectionsReady/boxes — готовые боксы детекции из
// sidecar-кэша: при true процессор НЕ
// вызывает ИИ-модель, а использует переданные боксы (в координатах
// оригинала; для fct/oct транслируются на trim-offset).
func (b *libvipsBackend) process(ctx context.Context, data []byte, plan *processing.ProcessingPlan, detectionsReady bool, boxes []filemeta.PixelBox, slot *gateSlot) (*backendResult, error) {
	// Отмена контекста проверяется перед затратной работой.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("libvips: plan: %w", err)
	}

	// Passthrough fast-path: если план ничего не меняет (формат совпадает,
	// размер тот же, нет watermark/trim/ориентации/детекции/strip-работы),
	// возвращаем исходные байты без decode/encode. При любых сомнениях —
	// полная обработка (см. passthroughEligible).
	if res, ok, err := b.tryPassthrough(ctx, data, plan); ok || err != nil {
		return res, err
	}

	img, err := b.load(ctx, data, plan)
	if err != nil {
		return nil, err
	}
	defer img.Close()

	// ICC color management (Фаза 5a): политика transform конвертирует
	// embedded-профиль в sRGB ПЕРЕД пиксельной обработкой. Fast-path:
	// sRGB-совместимые профили и изображения уже в sRGB без профиля не
	// конвертируются (нулевой оверхед). Ошибки lcms не роняют запрос —
	// fallback на strip-поведение с warning-логом.
	if err := b.applyColorManagement(img); err != nil {
		return nil, err
	}

	// Размеры входа (из заголовка) для Result; для анимации — высота кадра.
	srcW := img.Width()
	srcH := img.Height()
	if ph := img.PageHeight(); img.Pages() > 1 && ph > 0 && srcH > ph {
		srcH = ph
	}

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

	if err := b.applyOperation(ctx, img, plan, detectionsReady, boxes, slot); err != nil {
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
	return &backendResult{
		data:         out,
		width:        img.Width(),
		height:       img.Height(),
		sourceWidth:  srcW,
		sourceHeight: srcH,
	}, nil
}

// prepareRGB извлекает RGB-пиксели источника в размерах ОРИГИНАЛА (без
// trim) для детекции на уровне приложения (ensureDetections). Реализует
// backend.prepareRGB.
func (b *libvipsBackend) prepareRGB(ctx context.Context, data []byte) (*processor.RGBFrame, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Загружаем без изменений: только для извлечения RGB в исходных размерах.
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatJPEG,
		processing.Size{Original: true}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		return nil, fmt.Errorf("libvips: prepareRGB: plan: %w", err)
	}
	img, err := b.load(ctx, data, plan)
	if err != nil {
		return nil, err
	}
	defer img.Close()

	W := img.Width()
	H := img.Height()
	if ph := img.PageHeight(); img.Pages() > 1 && ph > 0 && H > ph {
		H = ph
	}

	tmp, err := img.Copy()
	if err != nil {
		return nil, fmt.Errorf("libvips: prepareRGB: copy: %w", err)
	}
	defer tmp.Close()
	if err := tmp.ToColorSpace(vips.InterpretationSRGB); err != nil {
		return nil, fmt.Errorf("libvips: prepareRGB: to-srgb: %w", err)
	}
	if err := tmp.Cast(vips.BandFormatUchar); err != nil {
		return nil, fmt.Errorf("libvips: prepareRGB: cast: %w", err)
	}
	if H < img.Height() {
		if err := tmp.ExtractArea(0, 0, W, H); err != nil {
			return nil, fmt.Errorf("libvips: prepareRGB: extract first frame: %w", err)
		}
	}
	if tmp.Bands() > 3 {
		if err := tmp.ExtractBand(0, 3); err != nil {
			return nil, fmt.Errorf("libvips: prepareRGB: extract rgb: %w", err)
		}
	}
	pixels, err := tmp.ToBytes()
	if err != nil {
		return nil, fmt.Errorf("libvips: prepareRGB: to-bytes: %w", err)
	}
	return &processor.RGBFrame{Pixels: pixels, Width: W, Height: H}, nil
}

// load загружает изображение из памяти. FailOnError всегда включён;
// AutoRotate (EXIF orientation) управляется планом: nil-спецификация =
// включён. Для анимированных входов/выходов (включая APNG) загружаются
// все кадры (NumPages=-1), либо не более plan.Frames кадров, если лимит
// задан. Sequential access mode выставляется там, где операция выполняет
// ровно один линейный проход по пикселям (см. resolveImportPlan).
func (b *libvipsBackend) load(ctx context.Context, data []byte, plan *processing.ProcessingPlan) (*vips.ImageRef, error) {
	params := vips.NewImportParams()
	params.AutoRotate.Set(plan.Orientation == nil || plan.Orientation.AutoOrient)
	params.FailOnError.Set(true)
	ip := resolveImportPlan(plan)
	if ip.SetPages {
		params.NumPages.Set(ip.NumPages)
	}
	if ip.Sequential {
		params.Access.Set(vips.AccessSequential)
	}
	// Shrink-on-load (Фаза 2): предварительное уменьшение при декодировании.
	// Заголовок читается лёгкой загрузкой libvips (пиксели декодируются
	// лениво — это дёшево); решение принимает чистая функция
	// resolveShrinkOnLoad. При любой ошибке чтения заголовка shrink просто
	// не применяется (отказоустойчивость): полная загрузка вернёт понятную
	// ошибку декодирования.
	if so := b.resolveShrinkForLoad(data, plan); so.JpegShrink > 1 {
		params.JpegShrinkFactor.Set(so.JpegShrink)
	} else if so.Scale < 1 {
		params.WebpScaleFactor.Set(so.Scale)
	}
	img, err := vips.LoadImageFromBuffer(data, params)
	if err != nil {
		return nil, fmt.Errorf("libvips: load: %w", err)
	}
	return img, nil
}

// resolveShrinkForLoad вычисляет параметры shrink-on-load для загрузки:
// читает лёгкий заголовок исходника и вызывает чистую функцию
// resolveShrinkOnLoad. Выключатель берётся из конфигурации движка
// (b.opts.ShrinkOnLoad; nil = включён по умолчанию).
func (b *libvipsBackend) resolveShrinkForLoad(data []byte, plan *processing.ProcessingPlan) shrinkOnLoadDecision {
	none := shrinkOnLoadDecision{JpegShrink: 1, Scale: 1}
	head, err := vips.LoadImageFromBuffer(data, vips.NewImportParams())
	if err != nil {
		return none
	}
	defer head.Close()
	src := shrinkOnLoadInfo{
		Width:  head.Width(),
		Height: frameHeight(head),
		Pages:  head.Pages(),
	}
	return resolveShrinkOnLoad(plan, src, head.Orientation(), b.opts.ShrinkOnLoad.Enabled())
}

// applyColorManagement применяет политику ICC color management (Фаза 5a) к
// загруженному изображению ПЕРЕД пиксельной обработкой.
//
// Режимы:
//   - strip (дефолт): профиль удаляется при экспорте (stripAllMetadata);
//     здесь ничего не делается;
//   - transform: embedded-профиль конвертируется в стандартный sRGB через
//     PCS (govips TransformICCProfile → vips_icc_transform с профилем
//     SRGBIEC61966-2.1); CMYK без профиля обрабатывается fallback-профилем;
//   - keep: профиль сохраняется в выходе (stripAllMetadata не удаляет его);
//     здесь ничего не делается.
//
// Fast-path (нулевой оверхед) в режиме transform:
//   - изображение уже в sRGB colorspace и без профиля;
//   - embedded-профиль sRGB-совместим (проверка по сигнатуре/имени без
//     lcms-конверсии — isSRGBProfile).
//
// Отказоустойчивость: битый/отсутствующий профиль или ошибка lcms НЕ
// роняют запрос — выполняется fallback на strip-поведение (профиль
// удалится при экспорте) с warning-логом.
func (b *libvipsBackend) applyColorManagement(img *vips.ImageRef) error {
	// Fast-path (нулевой оверхед): режим не transform, изображение уже в
	// sRGB без профиля либо embedded-профиль sRGB-совместим — конверсия
	// не требуется (решение — чистая функция colorNeedsTransform).
	srgbProfile := img.HasICCProfile() && isSRGBProfile(img.GetICCProfile())
	if !colorNeedsTransform(b.opts.Color, img.HasICCProfile(), srgbProfile, img.Interpretation() == vips.InterpretationSRGB) {
		return nil
	}
	// Трансформация embedded-профиля (или CMYK/иного без профиля) в sRGB.
	// TransformICCProfile использует fallback-профиль SRGBIEC61966-2.1 для
	// изображений без embedded-профиля и преобразует через PCS.
	if err := img.TransformICCProfile(vips.SRGBIEC6196621ICCProfilePath); err != nil {
		// Fallback на strip-поведение: профиль удалится при экспорте,
		// запрос продолжит обработку как раньше (без transform).
		slog.Default().Warn("libvips: icc transform failed, falling back to strip",
			"error", err.Error())
		return nil
	}
	return nil
}

// premultiplyResize выполняет изменение размера изображения с альфа-каналом
// без тёмных ореолов на полупрозрачных краях: Premultiply → resize →
// Unpremultiply. Для изображений без альфы выполняет resize напрямую.
// Premultiply/Unpremultiply govips работают корректно на многостраничных
// изображениях (операция применяется ко всему вертикальному стеку кадров,
// метаданные анимации page-height/delay/loop сохраняются), поэтому отдельная
// покадровая обработка не требуется.
func premultiplyResize(img *vips.ImageRef, fn func() error) error {
	hasAlpha := img.HasAlpha()
	if hasAlpha {
		if err := img.PremultiplyAlpha(); err != nil {
			return fmt.Errorf("libvips: premultiply: %w", err)
		}
	}
	if err := fn(); err != nil {
		return err
	}
	if hasAlpha {
		if err := img.UnpremultiplyAlpha(); err != nil {
			return fmt.Errorf("libvips: unpremultiply: %w", err)
		}
	}
	return nil
}

// tryPassthrough проверяет применимость fast-path и возвращает исходные
// байты как есть (без decode/encode). Заголовок читается лёгкой загрузкой
// libvips (пиксели декодируются лениво, поэтому это дёшево).
//
// Возвращаемые значения:
//   - ok=true  — passthrough применён, res содержит исходные данные;
//   - ok=false, err=nil — passthrough неприменим, нужна полная обработка;
//   - err!=nil — ошибка чтения заголовка; для отказоустойчивости она НЕ
//     прерывает запрос: вызывающий выполняет полную обработку.
func (b *libvipsBackend) tryPassthrough(ctx context.Context, data []byte, plan *processing.ProcessingPlan) (*backendResult, bool, error) {
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	head, err := vips.LoadImageFromBuffer(data, vips.NewImportParams())
	if err != nil {
		// Не удалось прочитать заголовок — полная обработка вернёт
		// понятную ошибку загрузки.
		return nil, false, nil
	}
	defer head.Close()

	src := sourceInfo{
		Width:       head.Width(),
		Height:      frameHeight(head),
		Pages:       head.Pages(),
		Orientation: head.Orientation(),
		MetaFields:  head.GetFields(),
		HasICC:      head.HasICCProfile(),
	}
	// sRGB-совместимость embedded-профиля (Фаза 5a): проверка по
	// сигнатуре/имени БЕЗ lcms-конверсии; false при битом/отсутствующем
	// профиле. Значимо только для режима transform.
	if src.HasICC {
		src.SRGBProfile = isSRGBProfile(head.GetICCProfile())
	}
	if !passthroughEligible(plan, src, b.opts.Color) {
		return nil, false, nil
	}
	return &backendResult{
		data:         data,
		width:        src.Width,
		height:       src.Height,
		sourceWidth:  src.Width,
		sourceHeight: src.Height,
	}, true, nil
}

// frameHeight возвращает высоту ОДНОГО кадра (для анимации — page-height,
// а не высота всего вертикального стека страниц).
func frameHeight(img *vips.ImageRef) int {
	h := img.Height()
	if ph := img.PageHeight(); img.Pages() > 1 && ph > 0 && h > ph {
		h = ph
	}
	return h
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
	H := img.Height()
	if ph <= 0 || H <= ph {
		if err := img.Flip(vips.DirectionVertical); err != nil {
			return nil, err
		}
		return img, nil
	}
	return withFrames(img, func(f *vips.ImageRef, i int) error {
		if err := f.Flip(vips.DirectionVertical); err != nil {
			return fmt.Errorf("flip frame %d/%d: %w", i+1, n, err)
		}
		return nil
	})
}

// withFrames инкапсулирует механику покадровой обработки анимации
// (вертикальный стек страниц):
//
//  1. До разборки захватываются метаданные анимации (delay, loop) —
//     arrayjoin их не переносит;
//  2. каждый кадр вырезается из лёгкой копии (Copy + SetPageHeight(H) +
//     ExtractArea(0, i*ph, W, ph)); временная установка высоты страницы
//     равной высоте всего стека переключает govips на ОБЫЧНЫЙ extract
//     региона (иначе он сам делает мультистраничный extract по всем
//     кадрам сразу);
//  3. к каждому кадру применяется колбэк fn;
//  4. кадры склеиваются ArrayJoin(..., across=1) и восстанавливаются
//     page-height / delay / loop.
//
// Семантика освобождения cgo-ресурсов: при любой ошибке текущий кадр и все
// ранее собранные (кроме base при успешном join) закрываются; после
// успешной сборки промежуточные кадры закрываются, остаётся только base.
func withFrames(img *vips.ImageRef, fn func(f *vips.ImageRef, i int) error) (*vips.ImageRef, error) {
	n := img.Pages()
	ph := img.PageHeight()
	W := img.Width()
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
		if err := fn(f, i); err != nil {
			f.Close()
			closeFrames(false)
			return nil, err
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

// applyOperation применяет операцию из плана к изображению.
//
// Trim — независимый фильтр: если plan.Trim установлен, он применяется
// СТРОГО первым (сначала trim, затем основная операция кропа/ресайза).
// detectionsReady/boxes — готовые боксы из sidecar-кэша (координаты
// оригинала); при trim они транслируются на trim-offset внутри
// applyDetectionCrop.
func (b *libvipsBackend) applyOperation(ctx context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan, detectionsReady bool, boxes []filemeta.PixelBox, slot *gateSlot) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Trim-first: независимый фильтр обрезки однотонных полей применяется
	// до основной операции (кропа/ресайза).
	if plan.Trim {
		if err := applyTrim(img, plan.TrimSpec); err != nil {
			return err
		}
	}

	// Size.Original (size=x): размер не меняем (после trim).
	if plan.Size.Original {
		return nil
	}

	w, h := plan.Size.Width, plan.Size.Height
	switch plan.Operation {
	case processing.OpResize:
		// Пропорциональное изменение размера (без обрезки). Для изображений
		// с альфой — Premultiply → resize → Unpremultiply (без тёмных
		// ореолов на полупрозрачных краях).
		err := premultiplyResize(img, func() error {
			return img.ThumbnailWithSize(w, h, vips.InterestingNone, vips.SizeBoth)
		})
		if err != nil {
			return fmt.Errorf("libvips: resize: %w", err)
		}
	case processing.OpCrop:
		// Центрированная обрезка до точного размера (с premultiply для
		// альфы — см. OpResize).
		err := premultiplyResize(img, func() error {
			return img.ThumbnailWithSize(w, h, vips.InterestingCentre, vips.SizeForce)
		})
		if err != nil {
			return fmt.Errorf("libvips: crop: %w", err)
		}
	case processing.OpSmartCrop:
		// Умная обрезка: внимание (attention) libvips — центр тяжести
		// изображения; масштаб и кроп до точного размера одним проходом
		// (с premultiply для альфы — см. OpResize).
		err := premultiplyResize(img, func() error {
			return img.ThumbnailWithSize(w, h, vips.InterestingAttention, vips.SizeForce)
		})
		if err != nil {
			return fmt.Errorf("libvips: smart-crop: %w", err)
		}
	case processing.OpFaceCrop:
		fallthrough
	case processing.OpObjectCrop:
		// Детекторная обрезка (лица/объекты): находится область интереса
		// (детектор + selectCrop), вырезается и подгоняется до целевого
		// размера.
		if err := b.applyDetectionCrop(ctx, img, plan, detectionsReady, boxes, slot); err != nil {
			return err
		}
	default:
		return fmt.Errorf("libvips: unsupported operation %q", plan.Operation)
	}
	return nil
}

// applyTrim выполняет обрезку однотонных/пустых краёв изображения по контенту
// (vips_find_trim + ExtractArea). spec — настройки trim (режим auto/color +
// tolerance); nil = по умолчанию ({auto, 0}). Возвращает ошибку, если область
// трима пуста.
func applyTrim(img *vips.ImageRef, spec *processing.TrimSpec) error {
	if spec == nil {
		spec = processing.DefaultTrimSpec()
	}
	// Режим color: фиксированный цвет фона. Режим auto: цвет фона берётся из
	// углового пикселя (0,0) — govips v2.18.0 не поддерживает nil-фон (см.
	// edgeBackgroundColor), поэтому явно передаём не-nil *vips.Color.
	var bg *vips.Color
	switch spec.Mode {
	case processing.TrimModeColor:
		bg = hexToColor(spec.Color)
	default:
		var err error
		bg, err = edgeBackgroundColor(img)
		if err != nil {
			return fmt.Errorf("libvips: trim: edge background: %w", err)
		}
	}
	left, top, tw, th, err := img.FindTrim(spec.Tolerance, bg)
	if err != nil {
		return fmt.Errorf("libvips: trim: find-trim: %w", err)
	}
	if tw <= 0 || th <= 0 {
		return fmt.Errorf("libvips: trim: empty trim area (%dx%d)", tw, th)
	}
	if err := img.ExtractArea(left, top, tw, th); err != nil {
		return fmt.Errorf("libvips: trim: %w", err)
	}
	return nil
}

// edgeBackgroundColor возвращает цвет фона для авто-trim, считывая угловой
// пиксель (0,0). Нужен, потому что vips.FindTrim у govips v2.18.0 не
// поддерживает nil-фон: vipsFindTrim (operations.go:21) безусловно
// разыменовывает backgroundColor.R/G/B, из-за чего авто-trim
// (spec.Mode == TrimModeAuto) падает с nil-pointer dereference. Обходим,
// передавая явный цвет края.
func edgeBackgroundColor(img *vips.ImageRef) (*vips.Color, error) {
	p, err := img.GetPoint(0, 0)
	if err != nil {
		return nil, err
	}
	if len(p) < 3 {
		return nil, fmt.Errorf("unexpected pixel bands %d", len(p))
	}
	// 16-битные изображения возвращают значения 0..65535; приводим к 0..255.
	scale := 1.0
	switch img.Interpretation() {
	case vips.InterpretationRGB16, vips.InterpretationGrey16:
		scale = 257.0
	}
	return &vips.Color{
		R: uint8(p[0] / scale),
		G: uint8(p[1] / scale),
		B: uint8(p[2] / scale),
	}, nil
}

// hexToColor преобразует hex-цвет "#RRGGBB" в vips.Color.
func hexToColor(hex string) *vips.Color {
	if len(hex) != 7 || hex[0] != '#' {
		return nil
	}
	parse := func(s string) uint8 {
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			return 0
		}
		return uint8(v)
	}
	return &vips.Color{
		R: parse(hex[1:3]),
		G: parse(hex[3:5]),
		B: parse(hex[5:7]),
	}
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
// applyDetectionCrop выполняет детекторную обрезку (face-crop/object-crop).
//
// Двухуровневые семафоры (Фаза 4): при self-detection (модель вызывается
// здесь) тяжёлый CPU-bound ONNX-инференс выполняется ВНЕ libvips-слота —
// слот перекладывается на detection-семофор (handoffToDetection) и
// возвращается обратно (reacquireVips) после инференса. Лёгкие cgo-операции
// (подготовка RGB до инференса, кроп/ресайз после) выполняются в обычном
// libvips-слоте.
//
// Отказоустойчивость: при неудаче любого перекладывания слот(ы) остаются в
// консистентном состоянии (владение не теряется), а slot.Release() в defer
// Process освобождает всё удерживаемое; ошибка перегрузки detection-семафора
// пробрасывается вызывающему как есть.
func (b *libvipsBackend) applyDetectionCrop(ctx context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan, detectionsReady bool, boxes []filemeta.PixelBox, slot *gateSlot) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Размеры кадра: для анимации используем высоту одного кадра.
	W := img.Width()
	H := img.Height()
	if ph := img.PageHeight(); img.Pages() > 1 && ph > 0 && H > ph {
		H = ph
	}

	// Готовые боксы из sidecar-кэша (координаты ОРИГИНАЛА). Если trim включён
	// (plan.Trim), изображение уже подрезано (applyTrim выполнен до вызова),
	// поэтому боксы транслируются на trim-offset. Без trim кадр совпадает с
	// оригиналом — боксы используются напрямую.
	var detBoxes []detection.Box
	if detectionsReady {
		detBoxes = translateBoxes(boxes, W, H)
	} else {
		// Self-detection: модель вызывается здесь.
		det := b.opts.Detector
		if det == nil || !det.Available() {
			return fmt.Errorf("libvips: %s: detection is not configured; set detection.face-model / detection.object-model and rebuild with -tags onnx", plan.Operation)
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

		// Handoff (Фаза 4): захватываем detection-слот и освобождаем
		// libvips-слот НА ВРЕМЯ ИНФЕРЕНСА. Порядок строго детерминирован
		// (см. detectionsemaphore.go): Acquire detection при удержании
		// libvips-слота → Release libvips. При ошибке ожидания libvips-слот
		// остаётся у нас — обработка завершится ошибкой без утечки слотов.
		//
		// slot может быть nil только в тестах движка вне Process; в этом
		// случае инференс выполняется в текущем контексте конкурентности
		// (деградация к прежнему поведению, не ошибка).
		if slot != nil {
			if err := slot.handoffToDetection(ctx); err != nil {
				return fmt.Errorf("libvips: %s: detection semaphore: %w", plan.Operation, err)
			}
		}

		// Детекция. Trim — независимый фильтр и не влияет на тип детекции:
		// face-crop всегда ищет лица, object-crop — объекты.
		var err2 error
		switch plan.Operation {
		case processing.OpFaceCrop:
			detBoxes, err2 = det.DetectFaces(ctx, rgb, W, H)
		case processing.OpObjectCrop:
			detBoxes, err2 = det.DetectObjects(ctx, rgb, W, H)
		}
		if err2 != nil {
			return fmt.Errorf("libvips: %s: detect: %w", plan.Operation, err2)
		}
	}

	// Возврат libvips-слота после инференса (фаза кропа/ресайза/экспорта).
	// Detection-слот удерживается до успешного возврата libvips-слота —
	// суммарная конкурентность не превышает лимиты ни на мгновение.
	if slot != nil && !detectionsReady {
		if err := slot.reacquireVips(ctx); err != nil {
			return fmt.Errorf("libvips: %s: reacquire vips slot: %w", plan.Operation, err)
		}
	}

	// Выбор области кропа и применение.
	rect := detection.SelectCrop(detBoxes, W, H, plan.Size.Width, plan.Size.Height, b.opts.DetectorMargin)
	if err := img.ExtractArea(rect.X, rect.Y, rect.W, rect.H); err != nil {
		return fmt.Errorf("libvips: %s: extract area (%d,%d %dx%d): %w", plan.Operation, rect.X, rect.Y, rect.W, rect.H, err)
	}
	// Финальный ресайз после кропа — тоже с premultiply для альфы
	// (консистентно с applyOperation).
	err := premultiplyResize(img, func() error {
		return img.ThumbnailWithSize(plan.Size.Width, plan.Size.Height, vips.InterestingCentre, vips.SizeForce)
	})
	if err != nil {
		return fmt.Errorf("libvips: %s: resize to %dx%d: %w", plan.Operation, plan.Size.Width, plan.Size.Height, err)
	}
	return nil
}

// translateBoxes транслирует боксы из координат ОРИГИНАЛА в координаты
// текущего кадра (после trim). Без trim кадр совпадает с оригиналом —
// боксы используются как есть. С trim кадр уже подрезан (applyTrim
// выполнен), поэтому боксы сдвигаются на trim-offset и зажимаются в кадр
// (clamp идентичен fitRect из detection.box.go).
func translateBoxes(boxes []filemeta.PixelBox, W, H int) []detection.Box {
	out := make([]detection.Box, 0, len(boxes))
	for _, b := range boxes {
		x := b.X
		y := b.Y
		w := b.Width
		h := b.Height
		// Clamp в кадр [0,W)x[0,H).
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x+w > W {
			w = W - x
		}
		if y+h > H {
			h = H - y
		}
		if w <= 0 || h <= 0 {
			continue
		}
		out = append(out, detection.Box{X: x, Y: y, W: w, H: h, Confidence: 1.0})
	}
	return out
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
// (EXIF/GPS, XMP, IPTC, описания) и (по умолчанию) ICC-профиль перед
// экспортом.
//
// Вызывается для ВСЕХ форматов как defense-in-depth: часть кодеков libvips
// (heifsave, jxlsave) не поддерживает опцию strip и копирует метаданные
// исходника в выходной файл. govips RemoveMetadata сохраняет технические
// поля (orientation, n-pages/page-height/delay/loop), необходимые для
// корректного отображения; orientation к этому моменту уже применён при
// загрузке (AutoRotate). RemoveICCProfile удаляет цветовой профиль —
// консистентно с ImageMagick -strip.
//
// keepICC (Фаза 5a, режим ColorKeep) сохраняет embedded-профиль в выходе:
// профиль описывает цвет пикселей, и при совпадении формата/без конверсии
// он остаётся валидным. В режиме transform конвертированные пиксели уже в
// sRGB, профиль (sRGB) удаляется как лишний. В режиме strip (дефолт)
// профиль всегда удаляется.
func stripAllMetadata(img *vips.ImageRef, keepICC bool) error {
	if err := img.RemoveMetadata(); err != nil {
		return fmt.Errorf("libvips: remove metadata: %w", err)
	}
	if keepICC {
		return nil
	}
	if err := img.RemoveICCProfile(); err != nil {
		return fmt.Errorf("libvips: remove icc profile: %w", err)
	}
	return nil
}

// exportImage экспортирует изображение в целевой формат. Per-format
// параметры сжатия кодировщиков берутся из конфигурации (b.opts.Encoders;
// нулевые значения = встроенные умолчания).
func (b *libvipsBackend) exportImage(img *vips.ImageRef, plan *processing.ProcessingPlan) ([]byte, error) {
	// Единая принудительная зачистка метаданных на готовом ассете — до
	// экспорта, независимо от поддержки strip конкретным кодеком. Режим
	// keep (Фаза 5a) сохраняет embedded-профиль в выходе (keepICC=true).
	if err := stripAllMetadata(img, b.opts.Color == ColorKeep); err != nil {
		return nil, err
	}
	// DPI-нормализация (Волна 5d): после strip сбрасываем xres/yres к 72 DPI,
	// чтобы просмотрщики не масштабировали изображение по DPI-метаданным
	// исходника. Решение (нужна ли копия) — чистая функция
	// needsResolutionNormalization; при необходимости создаётся новый ImageRef.
	//
	// ВАЖНО: новый ImageRef живёт ТОЛЬКО до конца экспорта (defer Close) —
	// исходный img остаётся у вызывающего (process) и читается им после
	// exportImage (Width/Height для Result). Копия размеров не меняет, поэтому
	// закрытие здесь безопасно и не приводит к двойному освобождению.
	norm, err := normalizeResolution(img, defaultResolutionDPI)
	if err != nil {
		return nil, err
	}
	if norm != img {
		defer norm.Close()
		img = norm
	}
	enc := b.opts.Encoders
	switch plan.OutputFormat {
	case processing.FormatJPEG:
		p := vips.NewJpegExportParams()
		p.Quality = plan.Quality
		p.StripMetadata = true
		p.SubsampleMode = vips.VipsForeignSubsampleOn
		// JPEG progressive (Волна 5d): false = baseline (обычный) JPEG.
		p.Interlace = enc.JPEGProgressive
		out, _, err := img.ExportJpeg(p)
		return out, err
	case processing.FormatPNG:
		p := vips.NewPngExportParams()
		p.StripMetadata = true
		p.Compression = pngCompression(enc.PNGCompression)
		// PNG interlace (Волна 5d): false = обычный (не-интерлейсный) PNG.
		p.Interlace = enc.PNGInterlace
		// PNG quantization (Волна 5c): палитровый экспорт. Применяется
		// ТОЛЬКО при явном включении (PNGPalette); при ошибке квантования —
		// fallback на обычный PNG-экспорт без падения запроса.
		if q := resolvePNGQuantize(enc); q.Palette {
			p.Palette = true
			p.Bitdepth = q.Bitdepth
			out, _, err := img.ExportPng(p)
			if err == nil {
				return out, nil
			}
			slog.Default().Warn("libvips: png quantization failed, falling back to plain png",
				"error", err.Error())
			// Fallback: обычный PNG-экспорт (без палитры).
			p.Palette = false
			p.Bitdepth = 0
		}
		out, _, err := img.ExportPng(p)
		return out, err
	case processing.FormatWebP:
		p := vips.NewWebpExportParams()
		p.Quality = plan.Quality
		p.StripMetadata = true
		p.ReductionEffort = webpReductionEffort(enc.WebPReductionEffort)
		out, _, err := img.ExportWebp(p)
		return out, err
	case processing.FormatGIF:
		p := vips.NewGifExportParams()
		p.Dither = 1.0
		// GIF bit-depth (Волна 5c): govips поддерживает Bitdepth для gifsave
		// (native gifsave vips ≥ 8.12). 0 = умолчание govips (8).
		p.Bitdepth = gifBitDepth(enc.GIFBitDepth)
		out, _, err := img.ExportGIF(p)
		return out, err
	case processing.FormatAVIF:
		p := vips.NewAvifExportParams()
		p.Quality = plan.Quality
		p.StripMetadata = true
		if s := avifSpeed(enc.AVIFSpeed); s > 0 {
			p.Effort = s
		}
		out, _, err := img.ExportAvif(p)
		return out, err
	case processing.FormatHEIF:
		p := vips.NewHeifExportParams()
		p.Quality = plan.Quality
		// Форк govips (third_party/govips) поддерживает для heifsave/jxlsave
		// общий аргумент VipsForeignSave "strip": при strip=true libvips не
		// синтезирует технический EXIF-блок из заголовка (vips__exif_update),
		// который иначе просачивается в выходной файл.
		p.StripMetadata = true
		out, _, err := img.ExportHeif(p)
		return out, err
	case processing.FormatJPEGXL:
		p := vips.NewJxlExportParams()
		p.Quality = plan.Quality
		p.StripMetadata = true
		// JXL effort (Волна 5d): 0 = умолчание govips (7).
		if e := jxlEffort(enc.JXLEffort); e > 0 {
			p.Effort = e
		}
		out, _, err := img.ExportJxl(p)
		return out, err
	case processing.FormatAPNG:
		// APNG — надмножество PNG: pngsave автоматически пишет анимацию
		// (acTL/fcTL/fdAT чанки) для multi-page изображений (кадры загружены
		// с NumPages=-1, page-height < высоты стека). Для одиночного
		// изображения результат — статичный PNG, который также является
		// валидным APNG (без анимации). Метаданные анимации (delay/loop)
		// сохраняются из исходника (см. applyAnimation).
		//
		// PNG quantization НЕ применяется к APNG: палитровый экспорт
		// анимации не поддерживается pngsave (палитра на каждый кадр
		// несовместима с APNG-чанками) — обычный PNG-экспорт с interlace.
		p := vips.NewPngExportParams()
		p.StripMetadata = true
		p.Compression = pngCompression(enc.PNGCompression)
		p.Interlace = enc.PNGInterlace
		out, _, err := img.ExportPng(p)
		return out, err
	default:
		return nil, fmt.Errorf("libvips: unsupported output format %q", plan.OutputFormat)
	}
}

// normalizeResolution сбрасывает xres/yres изображения к целевому DPI
// (по умолчанию 72), чтобы просмотрщики не масштабировали изображение по
// DPI-метаданным исходника. Решение о необходимости копии — чистая функция
// needsResolutionNormalization.
//
// Возвращает новый ImageRef, если разрешение отличалось (вызывающий обязан
// закрыть старый), либо тот же img, если нормализация не требуется.
// Отказоустойчивость: ошибка копирования — понятная ошибка экспорта.
func normalizeResolution(img *vips.ImageRef, targetDPI float64) (*vips.ImageRef, error) {
	if !needsResolutionNormalization(img.ResX(), img.ResY(), targetDPI) {
		return img, nil
	}
	// libvips хранит разрешение в px/mm, поэтому целевой DPI переводится
	// в px/mm (72 DPI = 72/25.4 ≈ 2.8346 px/mm).
	targetPxPerMm := dpiToPxPerMm(targetDPI)
	out, err := img.CopyChangingResolution(targetPxPerMm, targetPxPerMm)
	if err != nil {
		return nil, fmt.Errorf("libvips: normalize resolution to %.0f dpi: %w", targetDPI, err)
	}
	return out, nil
}

// maxWatermarkTiles — защитный лимит числа копий ватермарки на холсте
// (защита от патологических конфигураций тайлинга: крошечный файл при
// repeat покроет холст миллионами копий).
const maxWatermarkTiles = 4096

// registerVipsStatsProvider публикует провайдер vips-метрик в observability:
// tracked memory/allocs, open files, mem highwater, operation cache hits/
// misses (govips ReadVipsMemStats + счётчики операций) и метрики кэша
// ватермарок Фазы 3. Вызывается при создании движка; повторные вызовы
// заменяют провайдер без перезапуска сборщика.
//
// Отказоустойчивость: сам провайдер не паникует (cgo-вызовы обёрнуты
// recover'ом на стороне collector'а); до Startup значения нулевые — это
// корректное состояние.
func registerVipsStatsProvider(interval time.Duration, wmCache *watermarkCache) {
	observability.SetVipsStatsProvider(func() (observability.VipsSnapshot, error) {
		var snap observability.VipsSnapshot
		var ms vips.MemoryStats
		vips.ReadVipsMemStats(&ms)
		snap.TrackedMemory = ms.Mem
		snap.MemHighwater = ms.MemHigh
		snap.OpenFiles = ms.Files
		snap.TrackedAllocs = ms.Allocs
		var stats vips.RuntimeStats
		vips.ReadRuntimeStats(&stats)
		var total int64
		for _, n := range stats.OperationCounts {
			total += n
		}
		snap.OperationsTotal = total
		if wmCache != nil {
			entries, bytes, hits, misses := wmCache.stats()
			snap.WatermarkCacheEntries = int64(entries)
			snap.WatermarkCacheBytes = bytes
			snap.WatermarkCacheHits = hits
			snap.WatermarkCacheMisses = misses
		}
		return snap, nil
	}, interval)
}

// loadWatermark читает файл ватермарки через кэш байтов этого движка:
// stat файла выполняется на каждый вызов (дёшево) для инвалидации по
// mtime/размеру; сами байты берутся из памяти при попадании. При любой
// ошибке кэша/чтения возвращается ошибка — вызывающий не должен «ломаться»
// молча (ватермарка обязательна для запроса), но сам кэш ошибок не
// генерирует: промах просто означает чтение с диска.
func (b *libvipsBackend) loadWatermark(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat watermark file: %w", err)
	}
	loader := func() ([]byte, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read watermark file: %w", err)
		}
		return data, nil
	}
	return b.wmCache.getOrLoad(path, st.ModTime(), st.Size(), loader)
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
	data, err := b.loadWatermark(wm.Path)
	if err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: %w", wm.Name, err)
	}
	// Декодирование из кэшированных байтов: libvips кэширует операции
	// декодирования, поэтому повторный decode одного файла быстрый.
	// ImageRef создаётся НА КАЖДЫЙ запрос и мутируется локально — cgo-безопасно.
	wmImg, err := vips.NewImageFromBuffer(data)
	if err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: decode: %w", wm.Name, err)
	}
	defer wmImg.Close()

	W, H := img.Width(), img.Height()
	// Для анимации ватермарка накладывается на КАЖДЫЙ кадр (высота ph), а не
	// на весь вертикально сшитый холст (высота H). Поэтому целевой размер и
	// раскладку ватермарки нужно вычислять относительно размеров ОДНОГО кадра
	// (W×ph); иначе координаты по Y (например, center при H=2*ph) окажутся
	// смещены вниз на сшитом холсте и не попадут в центр кадра.
	ph := img.PageHeight()
	animated := img.Pages() > 1 && ph > 0 && H > ph
	canvasH := H
	if animated {
		canvasH = ph
	}
	tw, th := wm.TargetSize(W, canvasH, wmImg.Width(), wmImg.Height())
	// Режим round: копия масштабируется до шага сетки, чтобы целое число
	// копий точно укладывалось по осям холста.
	if wm.RoundScale() {
		tw, th = wm.RoundStep(W, canvasH, tw, th)
	}
	if err := wmImg.ThumbnailWithSize(tw, th, vips.InterestingNone, vips.SizeForce); err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: resize to %dx%d: %w", wm.Name, tw, th, err)
	}

	pts := wm.Layout(W, canvasH, tw, th)
	if len(pts) > maxWatermarkTiles {
		return nil, fmt.Errorf("libvips: watermark %q: too many tiles (%d > %d); increase watermark size or change repeat", wm.Name, len(pts), maxWatermarkTiles)
	}

	// Анимация (кадры = вертикальный стек страниц): покадровый композит.
	// Композит на весь сшитый холст попал бы только в область первого кадра.
	if animated {
		out, err := compositeWatermarkPerFrame(img, wmImg, pts, W, ph)
		if err != nil {
			return nil, fmt.Errorf("libvips: watermark %q: animated output %q: %w", wm.Name, plan.OutputFormat, err)
		}
		return out, nil
	}

	if err := compositeWatermarkOnce(img, wmImg, pts); err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: composite: %w", wm.Name, err)
	}
	return img, nil
}

// compositeWatermarkOnce накладывает все копии ватермарки ОДНИМ вызовом
// CompositeMulti (единый vips_composite со всеми слоями) вместо N
// последовательных Composite. Для одиночной позиции это тот же один композит,
// что и раньше; для repeat/tile — N слоёв одной операции: libvips выполняет
// один проход композиции вместо N промежуточных изображений.
//
// Требование vips_composite: все входы должны иметь одинаковое число каналов;
// если у цели и копии оно различается — недостающая альфа добавляется
// AddAlpha. BlendModeOver консистентен с premultiply-семантикой Фазы 2
// (composite выполняет смешивание в premultiplied пространстве внутри
// операции).
func compositeWatermarkOnce(target *vips.ImageRef, tile *vips.ImageRef, pts []processing.Point) error {
	if len(pts) == 0 {
		return nil
	}
	// Выравниваем число каналов: composite требует одинаковую структуру
	// входов. Обе картинки мутируются локально (владелец — текущий запрос).
	targetBands, tileBands := target.Bands(), tile.Bands()
	if targetBands != tileBands {
		if targetBands < tileBands {
			if err := target.AddAlpha(); err != nil {
				return fmt.Errorf("add alpha to target: %w", err)
			}
		} else if err := tile.AddAlpha(); err != nil {
			return fmt.Errorf("add alpha to watermark: %w", err)
		}
	}
	layers := make([]*vips.ImageComposite, len(pts))
	for i, pt := range pts {
		layers[i] = &vips.ImageComposite{Image: tile, BlendMode: vips.BlendModeOver, X: pt.X, Y: pt.Y}
	}
	return target.CompositeMulti(layers)
}

// compositeWatermarkPerFrame накладывает ватермарку на каждый кадр
// многокадрового изображения и собирает кадры обратно в вертикальный стек.
// Механика разборки/сборки анимации инкапсулирована в withFrames; каждый кадр
// получает ЕДИНЫЙ композит всех копий (см. compositeWatermarkOnce).
func compositeWatermarkPerFrame(img *vips.ImageRef, wmImg *vips.ImageRef, pts []processing.Point, W, ph int) (*vips.ImageRef, error) {
	n := img.Pages()
	return withFrames(img, func(f *vips.ImageRef, i int) error {
		if err := compositeWatermarkOnce(f, wmImg, pts); err != nil {
			return fmt.Errorf("frame %d/%d: %w", i+1, n, err)
		}
		return nil
	})
}
