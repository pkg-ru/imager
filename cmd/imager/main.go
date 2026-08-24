// Command imager — composition root production pipeline.
//
// Собирает HTTP adapter/runtime (internal/adapters/httpapi) поверх
// application/generatev2, domain/asset, policy/config и storage-адаптеров.
//
// Конфигурация: полностью из YAML-файлов в каталоге, заданном единственной
// env-переменной IMAGER_CONFIG_DIR (${dir}/setting.yaml + опциональный
// ${dir}/setting-local.yaml, который глубоко переопределяет базовый).
// Прикладных env-переменных и флагов нет — всё в YAML. Fail-fast на invalid
// config.
//
// Обработка изображений: основной движок — libvips (govips, in-process,
// собирается с тэком "-tags libvips"). libvips покрывает все форматы,
// включая APNG (≥ 8.13). ImageMagick остаётся опциональным fallback-ом
// только для сборок без тега "libvips". Маршрутизация между движками —
// internal/adapters/processor/routing.
//
// Observability: структурированные JSON-логи (log/slog), request ID
// (X-Request-Id), bounded-cardinality метрики (request/cache/processor/
// storage) через /metrics и /debug/vars. URL/query/raw user input и секреты
// не логируются и не попадают в метрики.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/httpapi"
	"github.com/pkg-ru/imager/internal/adapters/processor/detection"
	"github.com/pkg-ru/imager/internal/adapters/processor/imagemagick"
	"github.com/pkg-ru/imager/internal/adapters/processor/libvips"
	"github.com/pkg-ru/imager/internal/adapters/processor/routing"
	"github.com/pkg-ru/imager/internal/adapters/storage/fs"
	"github.com/pkg-ru/imager/internal/application/ports/detector"
	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/domain/processing"
	"github.com/pkg-ru/imager/internal/observability"
)

// ConfigDirEnv — единственная env-переменная: путь к каталогу с настройками.
// Внутри каталога читаются setting.yaml (обязательный) и setting-local.yaml
// (опциональный, глубоко переопределяет базовый).
const ConfigDirEnv = "IMAGER_CONFIG_DIR"

// DefaultConfigDir — каталог конфигурации по умолчанию, если IMAGER_CONFIG_DIR
// не задан. Используется при локальном запуске из корня репозитория.
const DefaultConfigDir = "."

func main() {
	// 0) Единственная env-переменная: каталог с настройками.
	configDir := os.Getenv(ConfigDirEnv)
	if configDir == "" {
		configDir = DefaultConfigDir
	}

	// 1) Единый typed runtime config из YAML (fail-fast). Базовый
	// setting.yaml обязателен; setting-local.yaml опционально глубоко
	// переопределяет его.
	rc, err := httpapi.LoadConfigDir(configDir)
	if err != nil {
		// Логгер ещё не создан — используем stderr напрямую.
		fatal("imager: load config: %v", err)
	}

	// 2) Observability: структурированный JSON-логгер и метрики.
	logger := observability.NewSlogLogger(slogLevel(rc.LogLevel))
	metrics := observability.NewStdMetrics()
	rc.HTTP.Logger = logger
	rc.HTTP.Metrics = metrics
	rc.HTTP.Pixel = newEmbedPixelGenerator()

	// 3) Процессоры: libvips (primary, если скомпилирован) + ImageMagick
	// (опциональный fallback для сборок без тега "libvips").
	proc, err := buildProcessor(logger, rc)
	if err != nil {
		logger.Errorf("imager: processor: %v", err)
		os.Exit(1)
	}

	// 4) Composition root: собирает pipeline (fail-fast на invalid config).
	// Хранилища, лимиты и адрес — из единого YAML-конфига.
	app, err := httpapi.Build(context.Background(), httpapi.AppOptions{
		Config:          rc.Pipeline,
		HTTP:            rc.HTTP,
		SourceDir:       rc.SourceDir,
		ResultDir:       rc.ResultDir,
		SourceStorage:   rc.Source,
		ResultStorage:   rc.Result,
		Processor:       proc.Processor,
		OutputLimit:     rc.OutputLimit,
		BufferMaxBytes:  rc.BufferMaxBytes,
		MetadataEnabled: rc.MetadataEnabled,
		MetadataDir:     rc.MetadataDir,
		Detector:        proc.Detector,
	})
	if err != nil {
		logger.Errorf("imager: build: %v", err)
		os.Exit(1)
	}

	// 5) Runtime: listener + graceful shutdown + signal handling.
	rt, err := httpapi.NewRuntime(httpapi.RuntimeOptions{
		Handler:           app.Handler,
		Addr:              rc.Server.Addr,
		ReadHeaderTimeout: rc.Server.ReadHeaderTimeout,
		ReadTimeout:       rc.Server.ReadTimeout,
		WriteTimeout:      rc.Server.WriteTimeout,
		IdleTimeout:       rc.Server.IdleTimeout,
		ShutdownTimeout:   rc.Server.ShutdownTimeout,
		MaxHeaderBytes:    rc.Server.MaxHeaderBytes,
		MaxBodyBytes:      int64(rc.Server.MaxBodyBytes),
	})
	if err != nil {
		logger.Errorf("imager: runtime: %v", err)
		os.Exit(1)
	}

	// Регистрируем ресурсы для закрытия при Shutdown (хранилища, процессор,
	// пул буферов).
	rt.AddCloser(app.Sources)
	rt.AddCloser(app.Results)
	rt.AddCloser(proc.Processor)
	rt.AddCloser(app.Pool)

	// Admin-сервис: запускаем пул воркеров (если admin включён) и
	// регистрируем graceful drain очереди при shutdown.
	if app.AdminSvc != nil {
		app.AdminSvc.Start(context.Background())
		rt.AddCloser(app.AdminSvc)
	}

	// Периодическая уборка осиротевших temp-файлов публикации.
	// Запускаем janitor для каталога результатов (каждые 5 минут, файлы
	// старше 1 часа). Останавливается при shutdown через rt.AddCloser.
	janitor, jErr := fs.NewJanitor(rc.ResultDir, fs.JanitorOptions{
		Interval: 5 * time.Minute,
		MaxAge:   1 * time.Hour,
	})
	if jErr != nil {
		logger.Errorf("imager: janitor: %v", jErr)
		os.Exit(1)
	}
	if err := janitor.Start(); err != nil {
		logger.Errorf("imager: janitor start: %v", err)
		os.Exit(1)
	}
	rt.AddCloser(janitorCloser{j: janitor})

	// 6) Health (readiness + liveness) + metrics привязаны к runtime.
	health := httpapi.NewHealth(rt)
	// Admission control: ограничиваем число одновременно обрабатываемых
	// asset-запросов лимитом из конфигурации (http.max-concurrent-requests).
	rt.SetHandler(httpapi.NewMuxWithAdmission(app.Handler, health, metrics,
		httpapi.MetricsAuthConfig{}, rc.HTTP.MaxConcurrentRequests, app.AdminHandler))

	logger.Infof("imager: listening on %s", rt.Addr())

	// 7) Запуск сервера в фоне; воркер защищён от паники.
	serveErr := make(chan error, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Errorf("imager: panic in server worker: %v", rec)
				serveErr <- fmt.Errorf("imager: panic in server worker: %v", rec)
			}
		}()
		serveErr <- rt.Serve()
	}()

	// 8) Ожидание сигнала завершения ИЛИ раннего отказа сервера.
	// Если Serve упадёт до сигнала (например, listener error), процесс
	// завершается немедленно, не дожидаясь сигнала.
	sigCh := make(chan os.Signal, 1)
	go func() {
		sigCh <- httpapi.WaitSignal(context.Background())
	}()
	select {
	case sig := <-sigCh:
		if sig != nil {
			logger.Infof("imager: received signal %v, shutting down", sig)
		}
	case err := <-serveErr:
		logger.Errorf("imager: server failed before signal: %v", err)
		os.Exit(1)
	}

	// 9) Bounded graceful shutdown (таймаут из конфигурации сервера).
	shutdownTimeout := rc.Server.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 15 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := rt.Shutdown(shutdownCtx); err != nil {
		logger.Errorf("imager: shutdown: %v", err)
	}

	// 10) Ожидание завершения сервера.
	if err := <-serveErr; err != nil {
		logger.Errorf("imager: server: %v", err)
	}
	logger.Infof("imager: stopped")
}

// appLogger — узкий интерфейс логирования composition root (реализуется
// *observability.SlogLogger).
type appLogger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// processorBuild — результат сборки процессоров: маршрутизатор + детектор
// (для sidecar-кэша моделей на уровне приложения).
type processorBuild struct {
	// Processor — маршрутизатор, реализующий processor.Processor и Close.
	Processor processor.Processor
	// Detector — порт ИИ-детекции на уровне приложения (nil = детекция
	// остаётся в процессоре).
	Detector detector.Detector
}

// buildProcessor собирает маршрутизатор процессоров:
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
func buildProcessor(logger appLogger, rc *httpapi.RuntimeConfig) (*processorBuild, error) {
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
			PrimaryCaps:  libvipsCaps(),
			Fallback:     nil,
			FallbackCaps: routing.Capability{Name: "imagemagick"},
		})
		if err != nil {
			return nil, fmt.Errorf("libvips routing: %w", err)
		}
		return &processorBuild{Processor: &closedProcessor{Processor: r, closers: closers}, Detector: portDet}, nil
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
		PrimaryCaps:  imagemagickCaps(),
		Fallback:     nil,
		FallbackCaps: routing.Capability{Name: "imagemagick"},
	})
	if err != nil {
		return nil, fmt.Errorf("imagemagick routing: %w", err)
	}
	return &processorBuild{Processor: &closedProcessor{Processor: r, closers: closers}, Detector: portDet}, nil
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

// libvipsCaps — покрытие форматов libvips (primary). Включает все форматы,
// в том числе APNG (libvips ≥ 8.13 поддерживает чтение и запись APNG как
// multi-page PNG). ImageMagick остаётся опциональным fallback-ом для сборок
// без тега "libvips".
func libvipsCaps() routing.Capability {
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

// imagemagickCaps — покрытие форматов ImageMagick (fallback): все текущие
// форматы, включая APNG.
func imagemagickCaps() routing.Capability {
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

// fatal печатает ошибку в stderr и завершает процесс (используется до
// создания логгера).
func fatal(format string, args ...any) {
	logger := observability.NewSlogLogger(slog.LevelInfo)
	logger.Errorf(format, args...)
	os.Exit(1)
}

func slogLevel(s string) slog.Level {
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

// janitorCloser адаптирует *fs.Janitor к io.Closer для rt.AddCloser:
// при shutdown останавливает периодическую уборку.
type janitorCloser struct {
	j *fs.Janitor
}

func (c janitorCloser) Close() error {
	c.j.Stop()
	return nil
}
