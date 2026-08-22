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

	if err := b.applyOperation(ctx, img, plan); err != nil {
		return nil, err
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
