// Package bootstrap содержит переиспользуемую composition-root логику,
// общую для cmd/imager и публичного фасада package imager.
//
// Здесь вынесены:
//   - BuildProcessor — сборка маршрутизатора процессоров (libvips primary,
//     ImageMagick fallback) с детектором;
//   - SlogLevel — разбор уровня логов из строки;
//   - capabilities libvips/ImageMagick для routing;
//   - Fatal — корректная печать фатальной ошибки до создания логгера.
//
// Пакет живёт на верхнем уровне репозитория и переиспользуется тонким
// cmd/imager (thor wrapper) и послойным публичным фасадом.
package bootstrap

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/pkg-ru/imager/adapters/httpapi"
	"github.com/pkg-ru/imager/adapters/processor/detection"
	"github.com/pkg-ru/imager/adapters/processor/imagemagick"
	"github.com/pkg-ru/imager/adapters/processor/libvips"
	"github.com/pkg-ru/imager/adapters/processor/routing"
	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/observability"
	"github.com/pkg-ru/imager/ports/detector"
	"github.com/pkg-ru/imager/ports/processor"
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

// BuildProcessor собирает маршрутизатор процессоров:
//
//   - primary: libvips (если скомпилирован с тэком "libvips"). libvips
//     покрывает все форматы, включая APNG (≥ 8.13).
//   - fallback: ImageMagick — создаётся ЛЕНИВО, только если libvips
//     недоступен (не скомпилирован или Startup завершился ошибкой). В
//     обычном сценарии (libvips работает) ImageMagick не создаётся и не
//     запускается вовсе.
//
// Возвращает процессор, реализующий processor.Processor и Close, закрывающий
// все созданные движки, а также детектор для sidecar-кэша моделей.
func BuildProcessor(logger Logger, rc *httpapi.RuntimeConfig) (*ProcessorBuild, error) {
	var closers []io.Closer

	// Детектор лиц/объектов (face-crop/object-crop). Создаётся всегда из
	// секции detection.*; при пустых путях к моделям — неактивная заглушка,
	// и libvips вернёт понятную ошибку при запросе fc/oc без моделей.
	det := detection.NewDetector(detection.Options{
		FaceModel:           rc.Detection.FaceModel,
		ObjectModel:         rc.Detection.ObjectModel,
		ConfidenceThreshold: rc.Detection.ConfidenceThreshold,
		MaxObjects:          rc.Detection.MaxObjects,
	})
	// Порт детекции на уровне приложения (для sidecar-кэша моделей).
	portDet := detection.NewPortDetector(det)

	// libvips доступен только если скомпилирован с тэком "libvips".
	lvProc, lvErr := libvips.New(libvips.Options{
		Limits: libvips.Limits{
			OutputBytes:   rc.Libvips.Limits.OutputBytes,
			Timeout:       rc.Libvips.Limits.Timeout,
			Concurrency:   rc.Libvips.Limits.Concurrency,
			Threads:       rc.Libvips.Limits.Threads,
			MaxCacheMem:   rc.Libvips.Limits.MaxCacheMem,
			MaxCacheFiles: rc.Libvips.Limits.MaxCacheFiles,
			MaxCacheSize:  rc.Libvips.Limits.MaxCacheSize,
		},
		Detector:       det,
		DetectorMargin: rc.Detection.Margin,
	})

	if libvips.Compiled() && lvErr == nil {
		// Основной сценарий: libvips — primary, ImageMagick не нужен.
		if lvProc != nil {
			closers = append(closers, lvProc)
		}
		logger.Infof("imager: processor: primary=libvips (all formats, incl. APNG)")
		r, err := routing.New(routing.Options{
			Primary:      lvProc,
			PrimaryCaps:  LibvipsCaps(),
			Fallback:     nil,
			FallbackCaps: routing.Capability{Name: "imagemagick"},
		})
		if err != nil {
			return nil, fmt.Errorf("libvips routing: %w", err)
		}
		return &ProcessorBuild{Processor: &closedProcessor{Processor: r, closers: closers}, Detector: portDet}, nil
	}

	// libvips недоступен: warning и fallback на ImageMagick как primary.
	if lvErr != nil {
		logger.Warnf("imager: libvips unavailable: %v; using ImageMagick as primary", lvErr)
	} else {
		logger.Warnf("imager: libvips not compiled in (build with -tags libvips); using ImageMagick as primary")
	}
	imProc, imErr := imagemagick.New(imagemagick.Options{
		Binary: rc.ImageMagick.Binary,
		Limits: rc.ImageMagick.Limits,
		Policy: rc.ImageMagick.Policy,
	})
	if imErr != nil {
		return nil, fmt.Errorf("no processor available: libvips: %v; imagemagick: %v", lvErr, imErr)
	}
	closers = append(closers, imProc)
	r, err := routing.New(routing.Options{
		Primary:      imProc,
		PrimaryCaps:  ImageMagickCaps(),
		Fallback:     nil,
		FallbackCaps: routing.Capability{Name: "imagemagick"},
	})
	if err != nil {
		return nil, fmt.Errorf("imagemagick routing: %w", err)
	}
	return &ProcessorBuild{Processor: &closedProcessor{Processor: r, closers: closers}, Detector: portDet}, nil
}

// closedProcessor — обёртка над processor.Processor, закрывающая все
// созданные движки (libvips, imagemagick) при Close. Реализует io.Closer.
type closedProcessor struct {
	processor.Processor
	closers []io.Closer
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
// multi-page PNG). ImageMagick остаётся опциональным fallback-ом для сборок
// без тега "libvips".
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

// ImageMagickCaps — покрытие форматов ImageMagick (fallback): все текущие
// форматы, включая APNG.
func ImageMagickCaps() routing.Capability {
	return routing.Capability{
		Name: "imagemagick",
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
