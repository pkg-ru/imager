// Command imager — composition root нового production pipeline.
//
// Собирает новый HTTP adapter/runtime (internal/adapters/httpapi) поверх
// application/generatev2, domain/asset, policy/config и storage-адаптеров.
// НЕ подключает legacy server, legacy handler или legacy config.
//
// Конфигурация: полностью из YAML-файлов в каталоге, заданном единственной
// env-переменной IMAGER_CONFIG_DIR (${dir}/setting.yaml + опциональный
// ${dir}/setting-local.yaml, который глубоко переопределяет базовый).
// Прикладных env-переменных и флагов нет — всё в YAML. Fail-fast на invalid
// config.
//
// Observability: структурированные JSON-логи (log/slog), request ID
// (X-Request-Id), bounded-cardinality метрики (request/cache/processor/
// storage) через /metrics и /debug/vars. URL/query/raw user input и секреты
// не логируются и не попадают в метрики.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/httpapi"
	"github.com/pkg-ru/imager/internal/adapters/processor/imagemagick"
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
	rc.HTTP.Pixel = newPixelGenerator(rc.ImageMagick.Binary)

	// 3) ImageMagick processor с resource limits и deny-by-default policy.
	proc, err := imagemagick.New(imagemagick.Options{
		Binary: rc.ImageMagick.Binary,
		Limits: rc.ImageMagick.Limits,
		Policy: rc.ImageMagick.Policy,
	})
	if err != nil {
		logger.Errorf("imager: imagemagick: %v", err)
		os.Exit(1)
	}

	// 4) Composition root: собирает pipeline (fail-fast на invalid config).
	// Хранилища, лимиты и адрес — из единого YAML-конфига.
	app, err := httpapi.Build(context.Background(), httpapi.AppOptions{
		Config:         rc.Pipeline,
		HTTP:           rc.HTTP,
		SourceDir:      rc.SourceDir,
		ResultDir:      rc.ResultDir,
		SourceStorage:  rc.Source,
		ResultStorage:  rc.Result,
		Processor:      proc,
		OutputLimit:    rc.OutputLimit,
		BufferMaxBytes: rc.BufferMaxBytes,
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

	// П.6: регистрируем ресурсы для закрытия при Shutdown (хранилища,
	// процессор, пул буферов).
	rt.AddCloser(app.Sources)
	rt.AddCloser(app.Results)
	rt.AddCloser(proc)
	rt.AddCloser(app.Pool)

	// 6) Health (readiness/liveness) + metrics привязаны к runtime.
	health := httpapi.NewHealth(rt)
	rt.SetHandler(httpapi.NewMux(app.Handler, health, metrics))

	logger.Infof("imager: listening on %s", rt.Addr())

	// 7) Запуск сервера в фоне. П.2: воркер защищён от паники.
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

// pixelGenerator — минимальный генератор прозрачного 1x1 пикселя через
// ImageMagick binary. Реализует httpapi.PixelGenerator без изменения
// ImageMagick adapter.
type pixelGenerator struct {
	binary string
}

func newPixelGenerator(binary string) *pixelGenerator {
	if binary == "" {
		binary = "magick"
	}
	return &pixelGenerator{binary: binary}
}

func (p *pixelGenerator) GeneratePixel(ctx context.Context, format string) ([]byte, error) {
	return generatePixel(ctx, p.binary, format)
}
