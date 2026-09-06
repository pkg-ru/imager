// Package bootstrap содержит переиспользуемую composition-root логику,
// общую для cmd/imager и публичного фасада package imager.
//
// Здесь вынесены:
//   - BuildProcessor — сборка процессора (libvips) с детектором;
//   - SlogLevel — разбор уровня логов из строки;
//   - capabilities libvips для routing;
//   - Fatal — корректная печать фатальной ошибки до создания логгера.
//
// Пакет живёт на верхнем уровне репозитория и переиспользуется тонким
// cmd/imager (thor wrapper) и послойным публичным фасадом.
package bootstrap

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"gitverse.ru/pkg-ru/imager/adapters/processor/detection"
	"gitverse.ru/pkg-ru/imager/adapters/processor/libvips"
	"gitverse.ru/pkg-ru/imager/adapters/processor/routing"
	"gitverse.ru/pkg-ru/imager/composition"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/observability"
	"gitverse.ru/pkg-ru/imager/ports/detector"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// Logger — узкий интерфейс логирования composition root (реализуется
// *observability.SlogLogger, любой Logger из observability).
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// ProcessorBuild — результат сборки процессоров: маршрутизатор + детектор
// (для sidecar-кэша моделей на уровне приложения).
type ProcessorBuild struct {
	// Processor — маршрутизатор, реализующий processor.Processor и Close.
	Processor processor.Processor
	// Detector — порт ИИ-детекции на уровне приложения (nil = детекция
	// остаётся в процессоре).
	Detector detector.Detector
}

// BuildProcessor собирает процессор:
//
//   - primary: libvips (govips, in-process). libvips покрывает все форматы,
//     включая APNG (≥ 8.13). Требует сборки с тэком "libvips".
//
// Возвращает процессор, реализующий processor.Processor и Close, закрывающий
// созданный движок, а также детектор для sidecar-кэша моделей.
func BuildProcessor(logger Logger, rc *composition.RuntimeConfig) (*ProcessorBuild, error) {
	var closers []io.Closer

	// Детектор лиц/объектов (face-crop/object-crop). Создаётся всегда из
	// секции detection.*; при пустых путях к моделям — неактивная заглушка,
	// и libvips вернёт понятную ошибку при запросе fc/oc без моделей.
	det := detection.NewDetector(detection.Options{
		FaceModel:           rc.Detection.FaceModel,
		ObjectModel:         rc.Detection.ObjectModel,
		OnnxRuntimeLib:      rc.Detection.OnnxRuntimeLib,
		ConfidenceThreshold: rc.Detection.ConfidenceThreshold,
		MaxObjects:          rc.Detection.MaxObjects,
	})
	// Порт детекции на уровне приложения (для sidecar-кэша моделей).
	portDet := detection.NewPortDetector(det)

	// libvips доступен только если скомпилирован с тэком "libvips".
	lvProc, lvErr := libvips.New(libvips.Options{
		Limits: libvips.Limits{
			SourceBytes:   rc.Libvips.Limits.SourceBytes,
			OutputBytes:   rc.Libvips.Limits.OutputBytes,
			Timeout:       rc.Libvips.Limits.Timeout,
			Concurrency:   rc.Libvips.Limits.Concurrency,
			Threads:       rc.Libvips.Limits.Threads,
			MaxCacheMem:   rc.Libvips.Limits.MaxCacheMem,
			MaxCacheFiles: rc.Libvips.Limits.MaxCacheFiles,
			MaxCacheSize:  rc.Libvips.Limits.MaxCacheSize,
		},
		EncodersConfig:      rc.Libvips.EncodersConfig,
		ShrinkOnLoad:        rc.Libvips.ShrinkOnLoad,
		Color:               rc.Libvips.Color,
		OperationCache:      rc.Libvips.OperationCache,
		WatermarkCache:      rc.Libvips.WatermarkCache,
		DetectionSem:        rc.Libvips.DetectionSem,
		VipsMetricsInterval: rc.Libvips.VipsMetricsInterval,
		// Фильтрация логов libvips/govips по configured observability.log-level:
		// без этого govips пишет info-сообщения ([govips.info]/[VIPS.info])
		// в stderr даже при log-level=warn (см. vipslog.go).
		VipsLogLevel:   rc.LogLevel,
		VipsLogger:     logger,
		Detector:       det,
		DetectorMargin: rc.Detection.Margin,
	})
	if lvErr != nil {
		return nil, fmt.Errorf("no processor available: libvips: %w", lvErr)
	}
	closers = append(closers, lvProc)
	logger.Infof("imager: processor: primary=libvips (all formats, incl. APNG)")
	r, err := routing.New(routing.Options{
		Primary:     lvProc,
		PrimaryCaps: LibvipsCaps(),
	})
	if err != nil {
		return nil, fmt.Errorf("libvips routing: %w", err)
	}
	return &ProcessorBuild{Processor: &closedProcessor{Processor: r, closers: closers}, Detector: portDet}, nil
}

// closedProcessor — обёртка над processor.Processor, закрывающая созданный
// движок (libvips) при Close. Реализует io.Closer.
type closedProcessor struct {
	processor.Processor
	closers []io.Closer
}

// Compile-time assertion: closedProcessor обязан сохранять опциональный
// processor.RGBPreparer (иначе app-level детекция ensureDetections всегда
// деградирует к self-detection — см. generatev2).
var _ processor.RGBPreparer = (*closedProcessor)(nil)

// PrepareRGB пробрасывает подготовку RGB-кадра на нижележащий процессор,
// если тот реализует processor.RGBPreparer (иначе — деградация к
// self-detection на уровне приложения).
func (c *closedProcessor) PrepareRGB(ctx context.Context, src io.ReadSeeker) (*processor.RGBFrame, error) {
	prep, ok := c.Processor.(processor.RGBPreparer)
	if !ok {
		return nil, fmt.Errorf("%T does not implement processor.RGBPreparer", c.Processor)
	}
	return prep.PrepareRGB(ctx, src)
}

func (c *closedProcessor) Close() error {
	var first error
	for _, cl := range c.closers {
		if err := cl.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// LibvipsCaps — покрытие форматов libvips (primary). Включает все форматы,
// в том числе APNG (libvips ≥ 8.13 поддерживает чтение и запись APNG как
// multi-page PNG).
func LibvipsCaps() routing.Capability {
	return routing.Capability{
		Name: "libvips",
		Formats: map[processing.Format]bool{
			processing.FormatJPEG:   true,
			processing.FormatPNG:    true,
			processing.FormatWebP:   true,
			processing.FormatGIF:    true,
			processing.FormatAVIF:   true,
			processing.FormatHEIF:   true,
			processing.FormatAPNG:   true,
			processing.FormatJPEGXL: true,
		},
	}
}

// SlogLevel сопоставляет строковый уровень логов из конфигурации
// (debug/info/warn/error) с сигнатурой slog.Level. Неизвестные значения
// отображаются в slog.LevelInfo (fail-safe направление).
func SlogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Fatal печатает ошибку в stderr через JSON-логгер и завершает процесс.
// Используется до создания основного логгера (например, при ошибке загрузки
// конфигурации). Завершает процесс с кодом 1 через os.Exit.
func Fatal(format string, args ...any) {
	logger := observability.NewSlogLogger(slog.LevelInfo)
	logger.Errorf(format, args...)
	os.Exit(1)
}
