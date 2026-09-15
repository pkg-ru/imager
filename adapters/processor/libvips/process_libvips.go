//go:build libvips

// Реализация libvips-движка (govips). Файл компилируется ТОЛЬКО с тэгом
// "libvips" (см. также process_stub.go для сборки без тэга).
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

	"gitverse.ru/pkg-ru/imager/adapters/processor/detection"
	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/observability"
	"gitverse.ru/pkg-ru/imager/ports/processor"
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
// Возвращает true в сборках с тэгом "libvips".
func Compiled() bool { return true }

// vipsLoggingSetup подключает фильтруемый хендлер логов govips: передаёт
// observability.Logger (или nil → дефолтный stderr-хендлер govips) и verbosity
// в vips.LoggingSettings. Функция-хендлер мостит vips.LogLevel (биты glib)
// на RouteVipsLog.
func vipsLoggingSetup(log observability.Logger, verbosity int) {
	if log == nil {
		// Хендлер не задан: остаёмся на дефолтном хендлере govips (stderr),
		// но verbosity всё равно фильтруется по configured уровню.
		vips.LoggingSettings(nil, vips.LogLevel(verbosity))
		return
	}
	handler := func(messageDomain string, messageLevel vips.LogLevel, message string) {
		RouteVipsLog(log, messageDomain, int(messageLevel), message)
	}
	vips.LoggingSettings(handler, vips.LogLevel(verbosity))
}

// newLibvipsBackend создаёт движок и выполняет однократный Startup govips с
// конфигурацией из Limits (ConcurrencyLevel, MaxCacheMem/Files/Size).
func newLibvipsBackend(opts Options) (backend, error) {
	b := &libvipsBackend{
		opts:    opts,
		wmCache: newWatermarkCache(opts.WatermarkCache),
	}
	// Vips-метрики: регистрируем провайдер снимков libvips +
	// кэша ватермарок этого движка в observability (периодический сборщик,
	// отказоустойчивый). Повторное создание движка заменяет провайдер.
	registerVipsStatsProvider(opts.VipsMetricsInterval, b.wmCache)
	startupOnce.Do(func() {
		// Фильтрация логов libvips/govips по configured observability.log-level:
		// устанавливаем хендлер и verbosity ДО vips.Startup (иначе govips
		// ставит свой дефолт — verbosity info + stderr-хендлер, из-за чего
		// [govips.info]/[VIPS.info] проходили мимо фильтра). Маппинг:
		// err/critical→error, warn→warn, message/info→info, debug→debug.
		verbosity := VipsVerbosityFor(opts.VipsLogLevel)
		vipsLoggingSetup(opts.VipsLogger, verbosity)
		// Лимиты кэша: при отключённом operation cache передаются
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
			// CollectStats: включает счётчик операций govips
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

	// ICC color management: политика transform конвертирует
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

	opImg, detections, detail, err := b.applyOperation(ctx, img, plan, detectionsReady, boxes, slot)
	if err != nil {
		return nil, err
	}
	if opImg != img {
		img.Close()
		img = opImg
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
	if plan.OutputFormats.Animated() {
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
		detections:   detections,
		detail:       detail,
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
	// Shrink-on-load: предварительное уменьшение при декодировании.
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

// applyColorManagement применяет политику ICC color management к
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

// thumbnailCropFrames выполняет thumbnail с кропом (SizeBoth + crop) с
// корректной обработкой анимаций.
//
// Регрессия 1: vips_thumbnail_image с crop != none схлопывает page-height
// вертикального стека кадров до высоты ОДНОГО кадра результата (n-pages
// формально сохраняется, но высота стека становится равной page-height),
// из-за чего экспортёры (gifsave и др.) записывают только первый кадр —
// анимация молча теряется.
//
// Регрессия 2 (съезжание «как прокрутка плёнки»): на кадре-копии из
// withFrames метаданные анимации (n-pages/page-height) наследуются от
// стека; premultiplyResize (Premultiply → thumbnail → Unpremultiply) внутри
// колбэка мутирует f и vips_thumbnail_image на изображении с
// нестандартной парой (n-pages, page-height) даёт непредсказуемую
// геометрию страницы (GLib critical / молча неверный размер кадра).
// Дополнительно vips_thumbnail_image наследует (не пересчитывает)
// page-height/n-pages входа, поэтому при округлении масштаба кадры могли
// бы отличаться по высоте — withFrames не проверял это и собирал
// рассинхронизированный стек.
//
// Исправление: для многостраничных изображений каждый кадр обрабатывается
// ИЗОЛИРОВАННО как одиночное изображение (механика withFrames: Copy +
// SetPageHeight(H) + ExtractArea), затем на кадре сбрасываются
// анимационные метаданные (page-height = высота кадра, n-pages = 1), и
// только после этого выполняется thumbnail. Гарантия SizeBoth + crop=не
// none: результат ТОЧНО w x h для одиночного изображения. После сборки
// проверяется целостность стека: Height % n == 0 и высота кадра == h;
// при рассинхронизации возвращается понятная ошибка вместо молча битого
// выхода. Для одиночных изображений — напрямую (прежнее поведение).
// Возвращает ImageRef-результат: для анимации это НОВОЕ изображение
// (вызывающий закрывает старое).
func thumbnailCropFrames(img *vips.ImageRef, w, h int, crop vips.Interesting) (*vips.ImageRef, error) {
	ph := img.PageHeight()
	if !(img.Pages() > 1 && ph > 0 && img.Height() > ph) {
		if err := premultiplyResize(img, func() error {
			return img.ThumbnailWithSize(w, h, crop, vips.SizeBoth)
		}); err != nil {
			return nil, err
		}
		return img, nil
	}
	n := img.Pages()
	base, err := withFrames(img, func(f *vips.ImageRef, i int) error {
		// Кадр из withFrames — одиночное изображение высотой ph, но с
		// унаследованными анимационными метаданными стека (n-pages > 1,
		// page-height == ph). Выравниваем метаданные под одиночное
		// изображение ДО thumbnail: vips_thumbnail_image наследует
		// n-pages/page-height, и их несоответствие реальной геометрии
		// фрейма даёт непредсказуемый результат (регрессия 2).
		if f.Pages() != 1 {
			if err := f.SetPages(1); err != nil {
				return fmt.Errorf("frame %d/%d: reset n-pages: %w", i+1, n, err)
			}
		}
		if f.PageHeight() != f.Height() {
			if err := f.SetPageHeight(f.Height()); err != nil {
				return fmt.Errorf("frame %d/%d: reset page-height: %w", i+1, n, err)
			}
		}
		// premultiplyResize на одиночном кадре безопасен: Premultiply/
		// Unpremultiply не меняют геометрию, а thumbnail с crop != none
		// и SizeBoth гарантирует точный w x h.
		if err := premultiplyResize(f, func() error {
			return f.ThumbnailWithSize(w, h, crop, vips.SizeBoth)
		}); err != nil {
			return fmt.Errorf("frame %d/%d: %w", i+1, n, err)
		}
		// Защита на каждый кадр: thumbnail с crop != none обязан дать
		// ТОЧНО w x h; отклонение (например, изменение реализации
		// vips_thumbnail_image или конфликт метаданных) фиксируем сразу —
		// иначе стек рассинхронизируется молча.
		if fw, fh := f.Width(), f.Height(); fw != w || fh != h {
			return fmt.Errorf("frame %d/%d: thumbnail with crop produced %dx%d, want %dx%d", i+1, n, fw, fh, w, h)
		}
		// Нормализация метаданных кадра: thumbnail мог унаследовать
		// отличающийся page-height (регрессия 2) — приводим к одиночному
		// изображению, чтобы ArrayJoin собрал стек с корректной геометрией.
		if f.PageHeight() != h {
			if err := f.SetPageHeight(h); err != nil {
				return fmt.Errorf("frame %d/%d: normalize page-height: %w", i+1, n, err)
			}
		}
		if f.Pages() != 1 {
			if err := f.SetPages(1); err != nil {
				return fmt.Errorf("frame %d/%d: normalize n-pages: %w", i+1, n, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Финальная защита целостности стека: withFrames вычисляет page-height
	// как Height(stack)/n; если кадры после обработки имеют разную высоту
	// (округление/наследование метаданных), стек читался бы как биты
	// разных кадров — вместо молча битого выхода возвращаем ошибку.
	if H := base.Height(); H%n != 0 {
		base.Close()
		return nil, fmt.Errorf("libvips: crop: inconsistent frame stack: height %d is not divisible by %d frames", H, n)
	}
	if got := base.Height() / n; got != h {
		base.Close()
		return nil, fmt.Errorf("libvips: crop: inconsistent frame stack: frame height %d, want %d", got, h)
	}
	return base, nil
}

// resizeEmbedFrames выполняет resize с letterbox/pillarbox (ОБА измерения
// заданы): thumbnail вписывает изображение пропорционально в бокс w x h
// (InterestingNone + SizeBoth), затем Embed заполняет недостающие края до
// ТОЧНОГО w x h. Для анимаций — покадрово (механика withFrames), для
// одиночных изображений — напрямую.
//
// Фон заполнения:
//   - если альфа возможна (выходной формат поддерживает альфу ИЛИ исходник
//     имеет альфа-канал) — прозрачный (EmbedBackgroundRGBA с RGBA{0,0,0,0});
//   - иначе (формат без альфы, например JPEG) — цвет из plan.Background
//     (hex "#RRGGBB"); при пустом значении — белый "#ffffff".
//
// Возвращает ImageRef-результат: для анимации это НОВОЕ изображение
// (вызывающий закрывает старое).
func resizeEmbedFrames(img *vips.ImageRef, w, h int, plan *processing.ProcessingPlan) (*vips.ImageRef, error) {
	ph := img.PageHeight()
	if !(img.Pages() > 1 && ph > 0 && img.Height() > ph) {
		if err := resizeEmbedFrame(img, w, h, plan); err != nil {
			return nil, err
		}
		return img, nil
	}
	n := img.Pages()
	base, err := withFrames(img, func(f *vips.ImageRef, i int) error {
		// Кадр из withFrames — одиночное изображение высотой ph, но с
		// унаследованными анимационными метаданными стека (n-pages > 1,
		// page-height == ph). Выравниваем метаданные под одиночное
		// изображение ДО thumbnail (см. thumbnailCropFrames: регрессия 2).
		if f.Pages() != 1 {
			if err := f.SetPages(1); err != nil {
				return fmt.Errorf("frame %d/%d: reset n-pages: %w", i+1, n, err)
			}
		}
		if f.PageHeight() != f.Height() {
			if err := f.SetPageHeight(f.Height()); err != nil {
				return fmt.Errorf("frame %d/%d: reset page-height: %w", i+1, n, err)
			}
		}
		if err := resizeEmbedFrame(f, w, h, plan); err != nil {
			return fmt.Errorf("frame %d/%d: %w", i+1, n, err)
		}
		// Защита на каждый кадр: thumbnail + embed обязаны дать ТОЧНО w x h.
		if fw, fh := f.Width(), f.Height(); fw != w || fh != h {
			return fmt.Errorf("frame %d/%d: resize+embed produced %dx%d, want %dx%d", i+1, n, fw, fh, w, h)
		}
		// Нормализация метаданных кадра: приводим к одиночному изображению,
		// чтобы ArrayJoin собрал стек с корректной геометрией.
		if f.PageHeight() != h {
			if err := f.SetPageHeight(h); err != nil {
				return fmt.Errorf("frame %d/%d: normalize page-height: %w", i+1, n, err)
			}
		}
		if f.Pages() != 1 {
			if err := f.SetPages(1); err != nil {
				return fmt.Errorf("frame %d/%d: normalize n-pages: %w", i+1, n, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Финальная защита целостности стека (см. thumbnailCropFrames).
	if H := base.Height(); H%n != 0 {
		base.Close()
		return nil, fmt.Errorf("libvips: resize: inconsistent frame stack: height %d is not divisible by %d frames", H, n)
	}
	if got := base.Height() / n; got != h {
		base.Close()
		return nil, fmt.Errorf("libvips: resize: inconsistent frame stack: frame height %d, want %d", got, h)
	}
	return base, nil
}

// resizeEmbedFrame выполняет resize + letterbox/pillarbox для ОДНОГО кадра
// (или одиночного изображения): thumbnail вписывает пропорционально в бокс
// w x h, затем Embed заполняет края до ТОЧНОГО w x h. Фон — прозрачный, если
// альфа возможна, иначе цвет из plan.Background (пусто = белый).
func resizeEmbedFrame(img *vips.ImageRef, w, h int, plan *processing.ProcessingPlan) error {
	if err := premultiplyResize(img, func() error {
		return img.ThumbnailWithSize(w, h, vips.InterestingNone, vips.SizeBoth)
	}); err != nil {
		return err
	}
	// Embed до ТОЧНОГО w x h. Центрирование: left/top = (цель - факт)/2.
	// ThumbnailWithSize с InterestingNone + SizeBoth гарантирует, что
	// результат ≤ w x h (одна ось точно w или h), поэтому left/top ≥ 0.
	left := (w - img.Width()) / 2
	top := (h - img.Height()) / 2
	if left < 0 || top < 0 {
		return fmt.Errorf("resize+embed: thumbnail produced %dx%d, larger than target %dx%d", img.Width(), img.Height(), w, h)
	}
	// Альфа возможна, если выходной формат поддерживает альфу ИЛИ исходник
	// имеет альфа-канал (тогда EmbedBackgroundRGBA добавит/сохранит альфу).
	if plan.OutputFormats.SupportsAlpha() || img.HasAlpha() {
		// Если у изображения нет альфа-канала (например, непрозрачный PNG
		// после thumbnail оптимизирован до 3-канального RGB), а фон должен
		// быть прозрачным, добавляем альфу явно: embed_image_background
		// использует 4-канальный фон {r,g,b,a} только при Bands > 3.
		if !img.HasAlpha() {
			if err := img.AddAlpha(); err != nil {
				return fmt.Errorf("add alpha: %w", err)
			}
		}
		if err := img.EmbedBackgroundRGBA(left, top, w, h, &vips.ColorRGBA{R: 0, G: 0, B: 0, A: 0}); err != nil {
			return fmt.Errorf("embed (transparent): %w", err)
		}
		return nil
	}
	// Формат без альфы (JPEG): цвет из конфига, пусто = белый.
	bg := plan.Background
	if bg == "" {
		bg = "#ffffff"
	}
	if err := img.EmbedBackground(left, top, w, h, hexToColor(bg)); err != nil {
		return fmt.Errorf("embed (background %s): %w", bg, err)
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
	// sRGB-совместимость embedded-профиля: проверка по
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
	// ExtractArea (vips_crop) сохраняет Xoffset/Yoffset на кадрах, а ArrayJoin
	// протаскивает их в собранный стек. Экспортёры многостраничных форматов
	// (gifsave/pngsave) при ненулевом offset пишут только первый кадр —
	// сбрасываем offset в 0,0. Делаем это ДО восстановления метаданных:
	// SetPageHeight/SetPages создают копию через vips_copy, которая сохраняет
	// уже сброшенный offset.
	if base.OffsetX() != 0 || base.OffsetY() != 0 {
		nb, err := base.CopyChangingOffset(0, 0)
		if err != nil {
			// base == frames[0]: closeFrames(false) закроет его вместе с
			// остальными кадрами, отдельный Close не нужен (двойное закрытие).
			closeFrames(false)
			return nil, fmt.Errorf("reset frame offset: %w", err)
		}
		base.Close()
		base = nb
		frames[0] = nb
	}
	// Метаданные анимации: page-height обязателен (иначе стек читается как
	// один высокий кадр), n-pages/delay/loop переносятся вручную. Регрессия:
	// arrayjoin не переносит n-pages — без восстановления экспортёры
	// (gifsave/pngsave) пишут только первый кадр. Порядок ВАЖЕН: SetPages/
	// SetPageDelay/ SetLoop создают копию через vips_copy; финальный
	// SetPageHeight должен идти ПЕРВЫМ (n-pages восстанавливается в конце,
	// т.к. vips_copy не переносит n-pages при page-height < высоты стека).
	//
	// page-height результата НЕ копируется из исходного изображения: колбэк
	// может изменить высоту кадров (например OpCrop — покадровый thumbnail
	// 32→16). page-height = высота одного кадра = Height(stack)/n. Если
	// оставить старый ph, стек 16×32 с ph=32 даёт n_pages = 32/32 = 1 —
	// экспортёр (gifsave/pngsave) молча запишет только первый кадр.
	newPH := base.Height() / n
	if newPH <= 0 || newPH > base.Height() {
		newPH = ph
	}
	if err := base.SetPageHeight(newPH); err != nil {
		base.Close()
		closeFrames(false)
		return nil, fmt.Errorf("restore page height: %w", err)
	}
	if err := base.SetPages(n); err != nil {
		base.Close()
		closeFrames(false)
		return nil, fmt.Errorf("restore n-pages: %w", err)
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
// Возвращает боксы детекции (в координатах ОРИГИНАЛА) для детекторных
// операций (fc/oc/fct/oct); для прочих операций — nil. detail —
// детализированные результаты self-detection (nil для недетекторных
// операций и для DetectionsReady=true).
// Возвращает актуальный ImageRef: для кроп-операций на анимации это НОВОЕ
// изображение (старый закрывает вызывающий в process), в остальных случаях —
// тот же img.
func (b *libvipsBackend) applyOperation(ctx context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan, detectionsReady bool, boxes []filemeta.PixelBox, slot *gateSlot) (*vips.ImageRef, []filemeta.PixelBox, *processor.DetectionsDetail, error) {
	if ctx.Err() != nil {
		return img, nil, nil, ctx.Err()
	}

	// Trim-first: независимый фильтр обрезки однотонных полей применяется
	// до основной операции (кропа/ресайза). Для анимаций trim пересобирает
	// стек кадров (withFrames) и возвращает НОВОЕ изображение — старое
	// закрываем здесь (вызывающий process закрывает актуальный img).
	// trimOffset — смещение кадра после trim (left, top) в координатах
	// оригинала: нужно для трансляции готовых боксов детекции (sidecar)
	// из координат оригинала в координаты подрезанного кадра.
	var trimOffsetX, trimOffsetY int
	if plan.Trim {
		trimmed, ox, oy, err := applyTrim(img, plan.TrimSpec)
		if err != nil {
			return img, nil, nil, err
		}
		trimOffsetX, trimOffsetY = ox, oy
		if trimmed != img {
			img.Close()
			img = trimmed
		}
	}

	// Size.Original (size=x): размер не меняем (после trim).
	if plan.Size.Original {
		return img, nil, nil, nil
	}

	// Размеры кадра: для анимации — высота ОДНОГО кадра (page-height),
	// чтобы пропорция недостающей оси считалась по кадру, а не по высоте
	// всего вертикального стека страниц.
	frameW := img.Width()
	frameH := frameHeight(img)

	w, h := plan.Size.Width, plan.Size.Height
	switch plan.Operation {
	case processing.OpResize:
		// Пропорциональное изменение размера (без обрезки). Для изображений
		// с альфой — Premultiply → resize → Unpremultiply (без тёмных
		// ореолов на полупрозрачных краях).
		//
		// ОБА измерения заданы (w > 0 && h > 0): letterbox/pillarbox —
		// thumbnail вписывает изображение пропорционально в бокс w x h
		// (InterestingNone + SizeBoth: одна ось точно w или h, вторая ≤),
		// затем Embed заполняет недостающие края до ТОЧНОГО w x h
		// (прозрачным фоном, где альфа возможна, иначе — цветом из
		// plan.Background; см. resizeEmbedFrames). Для анимаций — покадрово.
		//
		// Размер-грамматика с ОДНОЙ осью даёт план с нулём в другой оси:
		// vips_thumbnail_image требует ЯВНЫЕ ОБА измерения — width=0 →
		// ошибка "parameter width not set"; height=0 → GLib critical
		// "property 'height'" + fallback на дефолт свойства (молча неверный
		// box-fit). Поэтому недостающая ось вычисляется из пропорций кадра
		// (см. resolveResizeSize): "x200" → ширина, "200x" → высота. Здесь
		// letterbox не нужен: холст = результат thumbnail (пропорциональный).
		if w > 0 && h > 0 {
			out, err := resizeEmbedFrames(img, w, h, plan)
			if err != nil {
				return img, nil, nil, fmt.Errorf("libvips: resize: %w", err)
			}
			img = out
		} else {
			w, h = resolveResizeSize(frameW, frameH, w, h)
			err := premultiplyResize(img, func() error {
				return img.ThumbnailWithSize(w, h, vips.InterestingNone, vips.SizeBoth)
			})
			if err != nil {
				return img, nil, nil, fmt.Errorf("libvips: resize: %w", err)
			}
		}
	case processing.OpCrop:
		// Центрированная обрезка до точного размера (с premultiply для
		// альфы — см. OpResize). SizeBoth + crop=centre: пропорциональное
		// уменьшение/увеличение до заполнения целевого размера с
		// последующей обрезкой до ТОЧНОГО WxH. SizeForce здесь нельзя:
		// он растягивает изображение до WxH, игнорируя пропорции
		// (сплющивание). Для анимации — покадрово (см. thumbnailCropFrames:
		// thumbnail с кропом на стеке кадров схлопывает page-height).
		out, err := thumbnailCropFrames(img, w, h, vips.InterestingCentre)
		if err != nil {
			return img, nil, nil, fmt.Errorf("libvips: crop: %w", err)
		}
		img = out
	case processing.OpSmartCrop:
		// Умная обрезка: внимание (attention) libvips — центр тяжести
		// изображения; масштаб и кроп до точного размера одним проходом
		// (с premultiply для альфы — см. OpResize). SizeBoth (не Force):
		// см. комментарий OpCrop. Для анимации — покадрово.
		out, err := thumbnailCropFrames(img, w, h, vips.InterestingAttention)
		if err != nil {
			return img, nil, nil, fmt.Errorf("libvips: smart-crop: %w", err)
		}
		img = out
	case processing.OpFaceCrop:
		fallthrough
	case processing.OpObjectCrop:
		fallthrough
	case processing.OpFaceFixCrop:
		fallthrough
	case processing.OpObjectFixCrop:
		// Детекторная обрезка (лица/объекты): находится область интереса
		// (детектор + selectCrop), вырезается и подгоняется до целевого
		// размера. trimOffset передаётся для трансляции готовых боксов
		// (sidecar, координаты оригинала) в координаты подрезанного кадра.
		boxes, detail, err := b.applyDetectionCrop(ctx, img, plan, detectionsReady, boxes, slot, trimOffsetX, trimOffsetY)
		return img, boxes, detail, err
	default:
		return img, nil, nil, fmt.Errorf("libvips: unsupported operation %q", plan.Operation)
	}
	return img, nil, nil, nil
}

// applyTrim выполняет обрезку однотонных/пустых краёв изображения по контенту
// (vips_find_trim + ExtractArea). spec — настройки trim (режим auto/color +
// tolerance); nil = по умолчанию ({auto, 0}). Возвращает ошибку, если область
// трима пуста.
//
// Возвращает также trim-offset (left, top) — смещение подрезанного кадра
// относительно оригинала. Оно необходимо вызывающему для трансляции готовых
// боксов детекции (sidecar, координаты оригинала) в координаты подрезанного
// кадра (см. translateBoxes).
//
// Для анимированных изображений (многостраничный вертикальный стек кадров)
// trim выполняется ПОКАДРОВО: bounding box вычисляется по первому кадру
// (vips_find_trim на всём стеке считает область по высоте n*page-height,
// из-за чего top улетает за пределы одного кадра, а ExtractArea на стеке
// вырезает область из КАЖДОГО кадра по отдельности с ошибочным/съехавшим
// результатом). Найденная область применяется к каждому кадру через
// withFrames (page-height/delay/loop восстанавливаются там же). В этом
// случае возвращается НОВОЕ изображение (старое остаётся на совести
// вызывающего); для одиночных изображений правится img на месте и
// возвращается img.
//
// Для изображений с альфа-каналом FindTrim выполняется на RGB-копии
// (ExtractBand(0,3)): vips_find_trim сравнивает ВСЕ каналы, включая альфу,
// поэтому непрозрачная цветная рамка (отличающаяся по альфе от контента или
// шумящая в альфе) не распознавалась как фон. Координаты RGB-копии совпадают
// с оригиналом, ExtractArea выполняется на оригинале.
func applyTrim(img *vips.ImageRef, spec *processing.TrimSpec) (*vips.ImageRef, int, int, error) {
	if spec == nil {
		spec = processing.DefaultTrimSpec()
	}

	left, top, tw, th, err := trimRegion(img, spec)
	if err != nil {
		return img, 0, 0, err
	}
	if tw <= 0 || th <= 0 {
		return img, 0, 0, fmt.Errorf("libvips: trim: empty trim area (%dx%d)", tw, th)
	}

	// Многостраничное изображение: применяем trim-область к каждому кадру.
	// Краевой случай "область = весь кадр" не обрабатываем отдельно: extract
	// кадра без изменений корректен, а withFrames гарантированно
	// восстанавливает метаданные анимации.
	if img.Pages() > 1 && img.Height() > img.PageHeight() && img.PageHeight() > 0 {
		out, err := withFrames(img, func(f *vips.ImageRef, i int) error {
			if err := f.ExtractArea(left, top, tw, th); err != nil {
				return fmt.Errorf("libvips: trim: frame extract: %w", err)
			}
			return nil
		})
		if err != nil {
			return img, 0, 0, err
		}
		return out, left, top, nil
	}

	// Одиночное изображение: вырезаем область на месте (прежнее поведение).
	if err := img.ExtractArea(left, top, tw, th); err != nil {
		return img, 0, 0, fmt.Errorf("libvips: trim: %w", err)
	}
	return img, left, top, nil
}

// trimRegion вычисляет trim-область (left, top, width, height) для одного
// кадра. Для многостраничных изображений область считается по ПЕРВОМУ кадру:
// копия стека с page-height = высоте всего стека становится одиночным
// изображением, первый кадр — строки [0, ph) — вырезается ExtractArea, и
// FindTrim на нём возвращает координаты валидные и для остальных кадров.
func trimRegion(img *vips.ImageRef, spec *processing.TrimSpec) (int, int, int, int, error) {
	probe, closeProbe, err := trimProbe(img)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("libvips: trim: probe: %w", err)
	}
	defer closeProbe()

	// Режим color: фиксированный цвет фона. Режим auto: цвет фона берётся из
	// углового пикселя (0,0) — govips v2.18.0 не поддерживает nil-фон (см.
	// edgeBackgroundColor), поэтому явно передаём не-nil *vips.Color.
	var bg *vips.Color
	switch spec.Mode {
	case processing.TrimModeColor:
		bg = hexToColor(spec.Color)
	default:
		bg, err = edgeBackgroundColor(probe)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("libvips: trim: edge background: %w", err)
		}
	}
	left, top, tw, th, err := probe.FindTrim(spec.Tolerance, bg)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("libvips: trim: find-trim: %w", err)
	}
	return left, top, tw, th, nil
}

// trimProbe готовит изображение для FindTrim:
//   - многостраничное (анимация) — копия ПЕРВОГО кадра как одиночное
//     изображение (механика withFrames: Copy + SetPageHeight(H) +
//     ExtractArea(0,0,W,ph)); без этого vips_find_trim считает bounding box
//     по высоте всего стека (n*ph) и top улетает за пределы кадра;
//   - с альфа-каналом — RGB-копия (ExtractBand(0,3)): find_trim сравнивает
//     все каналы, включая альфу, из-за чего непрозрачная цветная рамка не
//     распознавалась как фон.
//
// Возвращает probe-изображение и функцию его освобождения (nil, no-op, если
// модификация не потребовалась — тогда FindTrim можно звать прямо на img).
func trimProbe(img *vips.ImageRef) (*vips.ImageRef, func(), error) {
	noop := func() {}

	multipage := img.Pages() > 1 && img.Height() > img.PageHeight() && img.PageHeight() > 0
	if !multipage && !img.HasAlpha() {
		return img, noop, nil
	}

	probe, err := img.Copy()
	if err != nil {
		return nil, nil, err
	}
	closeProbe := func() { probe.Close() }

	if multipage {
		W, H, ph := img.Width(), img.Height(), img.PageHeight()
		if err := probe.SetPageHeight(H); err != nil {
			probe.Close()
			return nil, nil, fmt.Errorf("set page height: %w", err)
		}
		if err := probe.ExtractArea(0, 0, W, ph); err != nil {
			probe.Close()
			return nil, nil, fmt.Errorf("extract first frame: %w", err)
		}
	}
	if probe.Bands() > 3 {
		if err := probe.ExtractBand(0, 3); err != nil {
			probe.Close()
			return nil, nil, fmt.Errorf("extract rgb bands: %w", err)
		}
	}
	return probe, closeProbe, nil
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
// Двухуровневые семафоры: при self-detection (модель вызывается
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
// Возвращает итоговые боксы детекции в координатах ОРИГИНАЛА (для
// self-detection — найденные моделью; для DetectionsReady — переданные
// app-боксы), чтобы app-слой мог сохранить их в sidecar при деградации
// ensureDetections.
// applyDetectionCrop применяет детекторную обрезку (fc/oc/fct/oct) и
// возвращает боксы в координатах ОРИГИНАЛА + детализированные результаты
// self-detection (faces/objects с реальной уверенностью и label).
// detail заполняется ТОЛЬКО в режиме self-detection (модель вызывалась
// внутри процессора); при DetectionsReady=true — nil.
func (b *libvipsBackend) applyDetectionCrop(ctx context.Context, img *vips.ImageRef, plan *processing.ProcessingPlan, detectionsReady bool, boxes []filemeta.PixelBox, slot *gateSlot, trimOffsetX, trimOffsetY int) ([]filemeta.PixelBox, *processor.DetectionsDetail, error) {
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
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
	// degraded — флаг graceful degradation: при перегрузке
	// detection-семафора (ErrTooManyDetectionConcurrency) обработка
	// продолжается с fallback-фокусировкой (center-crop) вместо ошибки 503.
	// Объявлен на уровне функции, т.к. используется и в self-detection
	// ветке, и ниже (reacquireVips).
	var degraded bool
	if detectionsReady {
		detBoxes = translateBoxes(boxes, W, H, trimOffsetX, trimOffsetY)
	} else {
		// Self-detection: модель вызывается здесь.
		det := b.opts.Detector
		if det == nil || !det.Available() {
			return nil, nil, fmt.Errorf("libvips: %s: detection is not configured; set detection.face-model / detection.object-model and rebuild with -tags onnx", plan.Operation)
		}

		// Извлечение RGB-пикселей: работаем на копии, чтобы не менять исходник.
		tmp, err := img.Copy()
		if err != nil {
			return nil, nil, fmt.Errorf("libvips: %s: copy: %w", plan.Operation, err)
		}
		defer tmp.Close()
		if err := tmp.ToColorSpace(vips.InterpretationSRGB); err != nil {
			return nil, nil, fmt.Errorf("libvips: %s: to-srgb: %w", plan.Operation, err)
		}
		if err := tmp.Cast(vips.BandFormatUchar); err != nil {
			return nil, nil, fmt.Errorf("libvips: %s: cast: %w", plan.Operation, err)
		}
		// Для анимации берём только первый кадр (высота H).
		if H < img.Height() {
			if err := tmp.ExtractArea(0, 0, W, H); err != nil {
				return nil, nil, fmt.Errorf("libvips: %s: extract first frame: %w", plan.Operation, err)
			}
		}
		// Приводим к 3 каналам (RGB), если есть альфа.
		if tmp.Bands() > 3 {
			if err := tmp.ExtractBand(0, 3); err != nil {
				return nil, nil, fmt.Errorf("libvips: %s: extract rgb: %w", plan.Operation, err)
			}
		}
		rgb, err := tmp.ToBytes()
		if err != nil {
			return nil, nil, fmt.Errorf("libvips: %s: to-bytes: %w", plan.Operation, err)
		}

		// Handoff: захватываем detection-слот и освобождаем
		// libvips-слот НА ВРЕМЯ ИНФЕРЕНСА. Порядок строго детерминирован
		// (см. detectionsemaphore.go): Acquire detection при удержании
		// libvips-слота → Release libvips. При ошибке ожидания libvips-слот
		// остаётся у нас — обработка может продолжиться или корректно
		// завершиться ошибкой без утечки слотов.
		//
		// Graceful degradation: при ПЕРЕГРУЗКЕ detection-семафора
		// (переполнение очереди ожидания ИЛИ истечение maxWait — оба случая
		// возвращают ErrTooManyDetectionConcurrency) запрос НЕ завершается
		// ошибкой 503, а продолжает обработку с fallback-фокусировкой
		// (center-crop). Ошибки самой модели (DetectFaces/DetectObjects)
		// этим путём НЕ распознаются — они остаются жёсткими ошибками.
		// vips-слот остаётся у нас (handoff не удался), Release в defer
		// Process освободит его ровно один раз (идемпотентно).
		//
		// slot может быть nil только в тестах движка вне Process; в этом
		// случае инференс выполняется в текущем контексте конкурентности
		// (деградация к прежнему поведению, не ошибка).
		if slot != nil {
			if err := slot.handoffToDetection(ctx); err != nil {
				if isDetectionOverload(err) {
					degraded = true
					slog.Default().Warn("libvips: detection semaphore overloaded; degraded to center-crop",
						"operation", plan.Operation)
					observability.IncDetectionDegradedGlobal()
				} else {
					return nil, nil, fmt.Errorf("libvips: %s: detection semaphore: %w", plan.Operation, err)
				}
			}
		}

		// Детекция. Trim — независимый фильтр и не влияет на тип детекции:
		// face-crop/face-fix-crop ищут лица, object-crop/object-fix-crop —
		// объекты. При деградации (перегрузка detection-семафора) модель
		// НЕ вызывается — detBoxes остаётся пустым, ниже применяется
		// center-crop.
		var err2 error
		if !degraded {
			switch plan.Operation {
			case processing.OpFaceCrop, processing.OpFaceFixCrop:
				detBoxes, err2 = det.DetectFaces(ctx, rgb, W, H)
			case processing.OpObjectCrop, processing.OpObjectFixCrop:
				detBoxes, err2 = det.DetectObjects(ctx, rgb, W, H)
			}
			if err2 != nil {
				return nil, nil, fmt.Errorf("libvips: %s: detect: %w", plan.Operation, err2)
			}
		}
	}

	// Возврат libvips-слота после инференса (фаза кропа/ресайза/экспорта).
	// Detection-слот удерживается до успешного возврата libvips-слота —
	// суммарная конкурентность не превышает лимиты ни на мгновение.
	// При деградации detection-слот НЕ захватывался — reacquire не нужен
	// (vips-слот всё время у нас).
	if slot != nil && !detectionsReady && !degraded {
		if err := slot.reacquireVips(ctx); err != nil {
			return nil, nil, fmt.Errorf("libvips: %s: reacquire vips slot: %w", plan.Operation, err)
		}
	}

	// Выбор области кропа и применение. Для face-crop — окно, масштабируемое
	// под область лица (bbox + margin, аспект целевого ассета) и
	// центрированное по лицу (с clamp к границам кадра); для object-crop —
	// окно, вмещающее всю найденную область. Для fix-режимов (face-fix/
	// object-fix) — cover-окно БЕЗ зума в область интереса: полная сторона
	// сохраняется, кроп только по пропорционально избыточной оси, позиция
	// по центру области интереса (bbox + margin) с clamp к границам.
	// Масштаб согласован: окно вычисляется в пикселях ОРИГИНАЛА так, что
	// после ресайза до plan.Size (ниже) окно совпадает с cover-масштабом.
	var rect detection.Rect
	switch plan.Operation {
	case processing.OpFaceCrop:
		rect = detection.SelectFaceCrop(detBoxes, W, H, plan.Size.Width, plan.Size.Height, b.opts.DetectorMargin)
	case processing.OpObjectCrop:
		rect = detection.SelectCrop(detBoxes, W, H, plan.Size.Width, plan.Size.Height, b.opts.DetectorMargin)
	case processing.OpFaceFixCrop:
		rect = detection.SelectFaceFixCrop(detBoxes, W, H, plan.Size.Width, plan.Size.Height, b.opts.DetectorMargin)
	default: // processing.OpObjectFixCrop
		rect = detection.SelectObjectFixCrop(detBoxes, W, H, plan.Size.Width, plan.Size.Height, b.opts.DetectorMargin)
	}
	if err := img.ExtractArea(rect.X, rect.Y, rect.W, rect.H); err != nil {
		return nil, nil, fmt.Errorf("libvips: %s: extract area (%d,%d %dx%d): %w", plan.Operation, rect.X, rect.Y, rect.W, rect.H, err)
	}
	// Финальный ресайз после кропа — тоже с premultiply для альфы
	// (консистентно с applyOperation). SizeBoth (не Force): область кропа
	// может иметь пропорции, отличные от целевых, — нужен пропорциональный
	// масштаб до заполнения + центрированная обрезка до точного WxH.
	err := premultiplyResize(img, func() error {
		return img.ThumbnailWithSize(plan.Size.Width, plan.Size.Height, vips.InterestingCentre, vips.SizeBoth)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("libvips: %s: resize to %dx%d: %w", plan.Operation, plan.Size.Width, plan.Size.Height, err)
	}
	// Итоговые боксы: в координатах ОРИГИНАЛА (до trim). При self-detection
	// модель уже вернула боксы в координатах текущего кадра; если trim был
	// применён ДО детекции (applyTrim выше по стеку в applyOperation), кадр
	// сдвинут — обратная трансляция на trim-offset невозможна здесь, поэтому
	// в Result возвращаются боксы как есть (координаты кадра, в котором
	// выполнялась детекция). Без trim кадр == оригинал — точное совпадение.
	out := make([]filemeta.PixelBox, 0, len(detBoxes))
	var detail *processor.DetectionsDetail
	if !detectionsReady {
		// Self-detection: сохраняем детализированные результаты (реальная
		// уверенность модели + label для объектов), чтобы app-слой смог
		// записать их в sidecar без деградации confidence к 1.0.
		detail = &processor.DetectionsDetail{}
		switch plan.Operation {
		case processing.OpFaceCrop, processing.OpFaceFixCrop:
			for _, b2 := range detBoxes {
				detail.Faces = append(detail.Faces, processor.DetectedFace{
					Box:        filemeta.PixelBox{X: b2.X, Y: b2.Y, Width: b2.W, Height: b2.H},
					Confidence: detection.ClampConfidence(b2.Confidence),
				})
			}
		case processing.OpObjectCrop, processing.OpObjectFixCrop:
			for _, b2 := range detBoxes {
				detail.Objects = append(detail.Objects, processor.DetectedObject{
					Box:        filemeta.PixelBox{X: b2.X, Y: b2.Y, Width: b2.W, Height: b2.H},
					Confidence: detection.ClampConfidence(b2.Confidence),
					Label:      b2.Label,
				})
			}
		}
	}
	for _, b2 := range detBoxes {
		out = append(out, filemeta.PixelBox{X: b2.X, Y: b2.Y, Width: b2.W, Height: b2.H})
	}
	return out, detail, nil
}

// translateBoxes транслирует боксы из координат ОРИГИНАЛА в координаты
// текущего кадра (после trim). Без trim (offset = 0,0) кадр совпадает с
// оригиналом — боксы используются как есть. С trim кадр уже подрезан
// (applyTrim выполнен), поэтому из координат боксов вычитается trim-offset
// (left, top) и результат зажимается в кадр [0,W)x[0,H) (clamp идентичен
// fitRect из detection.box.go).
func translateBoxes(boxes []filemeta.PixelBox, W, H, offsetX, offsetY int) []detection.Box {
	out := make([]detection.Box, 0, len(boxes))
	for _, b := range boxes {
		x := b.X - offsetX
		y := b.Y - offsetY
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
// выходных форматов (GIF, WebP, HEIF, APNG, AVIF).
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
// загрузке (AutoRotate). RemoveICCProfile удаляет цветовой профиль.
//
// keepICC (режим ColorKeep) сохраняет embedded-профиль в выходе:
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

// exportImage экспортирует изображение в целевой формат. Эффективные
// параметры кодирования разрешаются per-export через domain/encoding
// (resolveEffective в encoders.go): preset override > encoders yaml (globals)
// > якорный автомаппинг от plan.Quality > registry-дефолт.
func (b *libvipsBackend) exportImage(img *vips.ImageRef, plan *processing.ProcessingPlan) ([]byte, error) {
	// Единая принудительная зачистка метаданных на готовом ассете — до
	// экспорта, независимо от поддержки strip конкретным кодеком. Режим
	// keep сохраняет embedded-профиль в выходе (keepICC=true).
	if err := stripAllMetadata(img, b.opts.Color == ColorKeep); err != nil {
		return nil, err
	}
	// DPI-нормализация: после strip сбрасываем xres/yres к 72 DPI,
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
	resolved, err := resolveEffective(b.opts.EncodersConfig, string(plan.OutputFormats), plan.Quality, plan.EncodingOverrides[string(plan.OutputFormats)])
	if err != nil {
		return nil, fmt.Errorf("libvips: encode %s: %w", plan.OutputFormats, err)
	}
	switch plan.OutputFormats {
	case processing.FormatJPEG:
		p := vips.NewJpegExportParams()
		p.Quality = resolved.Quality
		p.StripMetadata = true
		p.SubsampleMode = vips.VipsForeignSubsampleOn
		// JPEG progressive: false = baseline (обычный) JPEG.
		p.Interlace = resolved.Progressive
		out, _, err := img.ExportJpeg(p)
		return out, err
	case processing.FormatPNG:
		p := vips.NewPngExportParams()
		p.StripMetadata = true
		p.Compression = resolved.CompressionLevel
		// PNG interlace: false = обычный (не-интерлейсный) PNG.
		p.Interlace = resolved.Interlace
		// PNG quantization: палитровый экспорт. Применяется при
		// эффективной palette=true (явный override или автомаппинг от
		// quality); при ошибке квантования — fallback на обычный PNG-экспорт
		// без падения запроса.
		if q := resolvePNGQuantize(resolved); q.Palette {
			p.Palette = true
			p.Dither = resolved.Dither
			p.Bitdepth = q.Bitdepth
			out, _, err := img.ExportPng(p)
			if err == nil {
				return out, nil
			}
			slog.Default().Warn("libvips: png quantization failed, falling back to plain png",
				"error", err.Error())
			// Fallback: обычный PNG-экспорт (без палитры).
			p.Palette = false
			p.Dither = 0
			p.Bitdepth = 0
		}
		out, _, err := img.ExportPng(p)
		return out, err
	case processing.FormatWebP:
		p := vips.NewWebpExportParams()
		p.Quality = resolved.Quality
		p.StripMetadata = true
		p.ReductionEffort = resolved.ReductionEffort
		p.Lossless = resolved.Lossless
		p.NearLossless = resolved.NearLossless
		out, _, err := img.ExportWebp(p)
		return out, err
	case processing.FormatGIF:
		// GIF — палитровый формат: gifsave пишет анимацию для multi-page
		// изображений (кадры загружены с NumPages=-1, page-height < высоты
		// стека). Регрессия: как и heifsave/pngsave при strip=true (см. ветки
		// FormatAVIF/FormatAPNG), gifsave при strip не переносит метаданные
		// анимации в выходной файл — GIF читается как статичный кадр. Обход:
		// перед экспортом пересчитываем n-pages из геометрии стека
		// (H / page-height) и восстанавливаем рассинхронизированные значения;
		// gifsave пишет последовательность кадров по page-height/n-pages
		// входного изображения. Для одиночного изображения результат —
		// статичный GIF (валидный).
		ph := img.PageHeight()
		H := img.Height()
		if ph > 0 && H > ph {
			n := H / ph
			if img.Pages() != n {
				if err := img.SetPages(n); err != nil {
					return nil, fmt.Errorf("libvips: gif restore n-pages: %w", err)
				}
			}
		}
		p := vips.NewGifExportParams()
		// GIF effort/dither (S4): раньше dither=1.0 хардкодился, effort не
		// управлялся — теперь оба из resolved (дефолты registry 7/1.0
		// сохраняют текущее поведение при отсутствии override).
		p.Effort = resolved.Effort
		p.Dither = resolved.Dither
		p.Bitdepth = resolved.BitDepth
		out, _, err := img.ExportGIF(p)
		return out, err
	case processing.FormatAVIF:
		// AVIF — HEIF-контейнер с AV1-компрессией: heifsave (foreign.c,
		// AVIF идёт через heifsave_buffer) пишет анимацию для multi-page
		// изображений (libvips 8.12+). Кадры загружены с NumPages=-1,
		// page-height < высоты стека.
		//
		// Регрессия: как и pngsave при strip=true (см. ветку FormatAPNG),
		// heifsave при strip не переносит метаданные анимации в выходной
		// файл — AVIF читается как статичный кадр. Обход: перед экспортом
		// пересчитываем n-pages из геометрии стека (H / page-height) и
		// восстанавливаем рассинхронизированные значения; heifsave пишет
		// последовательность кадров по page-height/n-pages входного
		// изображения. Для одиночного изображения результат — статичный
		// AVIF (валидный).
		ph := img.PageHeight()
		H := img.Height()
		if ph > 0 && H > ph {
			n := H / ph
			if img.Pages() != n {
				if err := img.SetPages(n); err != nil {
					return nil, fmt.Errorf("libvips: avif restore n-pages: %w", err)
				}
			}
		}
		p := vips.NewAvifExportParams()
		p.Quality = resolved.Quality
		p.StripMetadata = true
		// avif speed 0 ВАЛИДЕН (это скорость, а не «не задано») — ставим
		// всегда из resolved (govips: Speed=0 → Effort из params,
		// поэтому пишем в Effort).
		p.Effort = resolved.Speed
		p.Lossless = resolved.Lossless
		out, _, err := img.ExportAvif(p)
		return out, err
	case processing.FormatHEIF:
		p := vips.NewHeifExportParams()
		p.Quality = resolved.Quality
		// Форк govips (govips) поддерживает для heifsave/jxlsave
		// общий аргумент VipsForeignSave "strip": при strip=true libvips не
		// синтезирует технический EXIF-блок из заголовка (vips__exif_update),
		// который иначе просачивается в выходной файл.
		p.StripMetadata = true
		// HEIF effort — libvips-дефолт (не конфигурируется).
		out, _, err := img.ExportHeif(p)
		return out, err
	case processing.FormatJPEGXL:
		p := vips.NewJxlExportParams()
		p.Quality = resolved.Quality
		p.StripMetadata = true
		p.Effort = resolved.Effort
		p.Lossless = resolved.Lossless
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
		//
		// Регрессия: stripAllMetadata (RemoveMetadata) сохраняет n-pages/
		// page-height/delay/loop, но pngsave при strip=true НЕ переносит
		// метаданные анимации в выходной файл — APNG читается как статичный
		// PNG (1 страница). Обход: перед экспортом пересчитываем n-pages из
		// геометрии стека (H / page-height) и восстанавливаем page-height,
		// если они рассинхронизированы; pngsave пишет acTL/fcTL/fdAT по
		// page-height/n-pages входного изображения.
		ph := img.PageHeight()
		H := img.Height()
		if ph > 0 && H > ph {
			n := H / ph
			if img.Pages() != n {
				if err := img.SetPages(n); err != nil {
					return nil, fmt.Errorf("libvips: apng restore n-pages: %w", err)
				}
			}
		}
		p := vips.NewPngExportParams()
		p.StripMetadata = true
		p.Compression = resolved.CompressionLevel
		p.Interlace = resolved.Interlace
		out, _, err := img.ExportPng(p)
		return out, err
	default:
		return nil, fmt.Errorf("libvips: unsupported output format %q", plan.OutputFormats)
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
//   - size contain/cover/natural/{w}px {h}px/{w}px/{n}%/{n} —
//     масштабирование одной копии относительно ЦЕЛЕВОГО холста
//     (natural = исходный размер; {n}% = n% от обоих измерений холста);
//   - position top/bottom/left/right/center — якорь одиночной копии
//     (вторая ось — центр); для cover-размера копия может быть БОЛЬШЕ
//     холста, и тогда видимый фрагмент вырезается по позиции (излишек
//     обрезается с нужной стороны, а не всегда с правого/нижнего края);
//   - repeat no-repeat/repeat/repeat-x/repeat-y/round/space — раскладка
//     копий (см. WatermarkSpec.Layout); round дополнительно масштабирует
//     копию до шага сетки (RoundStep), чтобы копии точно укладывались.
//   - opacity 0-100 — прозрачность знака (100 = непрозрачный, дефолт;
//     0 = полностью прозрачный/невидимый). Реализуется умножением
//     альфа-канала копии на множитель opacity/100 (vips_linear по
//     альфа-каналу) ДО композиции: при 100 множитель = 1 и изображение
//     ватермарки не изменяется (нулевые накладные расходы).
//
// DPR: холст уже dpr-кратный (buildPlan умножает целевой размер на dpr).
// Чтобы ватермарка выглядела одинаково при любом dpr, фиксированные
// (px) и натуральные размеры копии масштабируются на dpr; contain/cover/
// percent зависят от холста и масштабируются автоматически.
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
	// DPR: холст уже dpr-кратный (buildPlan умножает целевой размер на dpr).
	// Фиксированные (px) и натуральные размеры копии масштабируются на dpr,
	// чтобы ватермарка выглядела одинаково при любом dpr; contain/cover/
	// percent зависят от холста и масштабируются автоматически.
	dpr := plan.DPR
	if dpr <= 0 {
		dpr = 1
	}
	switch wm.SizeKind {
	case processing.WatermarkSizePixels, processing.WatermarkSizeNatural:
		tw, th = tw*dpr, th*dpr
	}
	// Режим round: копия масштабируется до шага сетки, чтобы целое число
	// копий точно укладывалось по осям холста.
	if wm.RoundScale() {
		tw, th = wm.RoundStep(W, canvasH, tw, th)
	}
	if err := wmImg.ThumbnailWithSize(tw, th, vips.InterestingNone, vips.SizeForce); err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: resize to %dx%d: %w", wm.Name, tw, th, err)
	}

	// Прозрачность: умножаем альфа-канал копии на opacity/100 до композита.
	// При 100 (дефолт) операция пропускается — поведение идентично прежнему.
	if err := applyWatermarkOpacity(wmImg, wm.Opacity); err != nil {
		return nil, fmt.Errorf("libvips: watermark %q: opacity %d: %w", wm.Name, wm.Opacity, err)
	}

	// Cover + no-repeat: копия может быть БОЛЬШЕ холста. CompositeMulti не
	// умеет отрицательные координаты, поэтому видимый фрагмент вырезается
	// из копии по позиции (CoverOffset) и композитится в (max(dx,0), max(dy,0)).
	// Для repeat-режимов при oversized-копии остаётся прежний clamp-путь
	// (repeat при cover — экзотика; поведение не ломается).
	var pts []processing.Point
	if wm.SizeKind == processing.WatermarkSizeCover && wm.Repeat == processing.WatermarkRepeatNoRepeat {
		dx, dy := wm.CoverOffset(W, canvasH, tw, th)
		if dx < 0 || dy < 0 {
			sx, sy := 0, 0
			if dx < 0 {
				sx = -dx
			}
			if dy < 0 {
				sy = -dy
			}
			sw, sh := tw-sx, th-sy
			if sw > W {
				sw = W
			}
			if sh > canvasH {
				sh = canvasH
			}
			if sw > 0 && sh > 0 {
				if err := wmImg.ExtractArea(sx, sy, sw, sh); err != nil {
					return nil, fmt.Errorf("libvips: watermark %q: cover extract %d,%d %dx%d: %w", wm.Name, sx, sy, sw, sh, err)
				}
				px, py := 0, 0
				if dx > 0 {
					px = dx
				}
				if dy > 0 {
					py = dy
				}
				pts = []processing.Point{{X: px, Y: py}}
			}
		}
	}
	if pts == nil {
		// Проверяем число тайлов ДО материализации среза точек: Layout строит
		// срез всех позиций, что при патологическом тайлинге (крошечный файл +
		// repeat на большом холсте) аллоцирует до ~1.6 ГБ. LayoutCount — чистая
		// арифметика без аллокаций.
		if n := wm.LayoutCount(W, canvasH, tw, th); n > maxWatermarkTiles {
			return nil, fmt.Errorf("libvips: watermark %q: too many tiles (%d > %d); increase watermark size or change repeat", wm.Name, n, maxWatermarkTiles)
		}
		pts = wm.Layout(W, canvasH, tw, th)
	}

	// Анимация (кадры = вертикальный стек страниц): покадровый композит.
	// Композит на весь сшитый холст попал бы только в область первого кадра.
	if animated {
		out, err := compositeWatermarkPerFrame(img, wmImg, pts, W, ph)
		if err != nil {
			return nil, fmt.Errorf("libvips: watermark %q: animated output %q: %w", wm.Name, plan.OutputFormats, err)
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
// applyWatermarkOpacity умножает альфа-канал ватермарки на множитель
// opacity/100 (vips_linear только по альфа-полосе): 100 = без изменений,
// 0 = знак полностью прозрачен (не влияет на результат композита).
// Значения вне [0,100] нормализуются к дефолту (100) — та же семантика,
// что и при компиляции конфига.
func applyWatermarkOpacity(wmImg *vips.ImageRef, opacity int) error {
	opacity = processing.NormalizeWatermarkOpacity(opacity)
	if opacity >= processing.DefaultWatermarkOpacity {
		return nil
	}
	if !wmImg.HasAlpha() {
		// Знак без альфы: добавляем полностью непрозрачный альфа-канал,
		// затем масштабируем его — цветовые полосы не затрагиваются.
		if err := wmImg.AddAlpha(); err != nil {
			return fmt.Errorf("add alpha: %w", err)
		}
	}
	bands := wmImg.Bands()
	a := make([]float64, bands)
	b := make([]float64, bands)
	for i := 0; i < bands; i++ {
		a[i] = 1
		b[i] = 0
	}
	a[bands-1] = float64(opacity) / float64(processing.DefaultWatermarkOpacity)
	return wmImg.Linear(a, b)
}

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
