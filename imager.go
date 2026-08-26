// Package imager — публичный фасад библиотеки github.com/pkg-ru/imager.
//
// Предоставляет высокоуровневый API для внешних приложений:
//
//   - NewServer — собрать и запустить полный HTTP-сервер из YAML-конфига
//     (сценарий "как cmd/imager");
//   - New — программная сборка pipeline без YAML: пользователь сам
//     подставляет порты (processor, storage, coordinator, detector, buffer,
//     metadata);
//   - Server — обёртка над httpapi.Runtime с graceful shutdown.
//
// Фасад знает все слои (adapters, app, ports, domain, config, observability),
// но никто не импортирует фасад — это композиционный корень.
package imager

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/pkg-ru/imager/adapters/httpapi"
	"github.com/pkg-ru/imager/adapters/pixel"
	"github.com/pkg-ru/imager/adapters/storage/fs"
	"github.com/pkg-ru/imager/bootstrap"
	"github.com/pkg-ru/imager/observability"
)

// ConfigDirEnv — единственная env-переменная: путь к каталогу с настройками.
// Внутри каталога читаются три слоя конфигурации:
//
//	setting.yaml + setting-local.yaml   — фундамент (обязателен setting.yaml);
//	generate.yaml + generate-local.yaml — генерация ассетов (опционально);
//	failback.yaml + failback-local.yaml — fallback-механизмы (опционально).
const ConfigDirEnv = "IMAGER_CONFIG_DIR"

// DefaultConfigDir — каталог конфигурации по умолчанию, если IMAGER_CONFIG_DIR
// не задан. Используется при локальном запуске из корня репозитория.
const DefaultConfigDir = "."

// Fatal печатает ошибку в stderr через JSON-логгер и завершает процесс с
// кодом 1. Используется до создания основного логгера (например, при ошибке
// загрузки конфигурации).
func Fatal(format string, args ...any) {
	bootstrap.Fatal(format, args...)
}

// Option — функциональная опция для NewServer.
type Option func(*serverOptions)

// serverOptions — внутренние настройки NewServer.
type serverOptions struct {
	// logger — кастомный логгер (nil = JSON-логгер в stderr).
	logger observability.Logger
	// metrics — кастомные метрики (nil = NewStdMetrics).
	metrics observability.Metrics
	// pixelGen — генератор пикселей (nil = встроенный).
	pixelGen httpapi.PixelGenerator
	// janitorInterval — период уборки temp-файлов (0 = 5m).
	janitorInterval time.Duration
	// janitorMaxAge — возраст temp-файла (0 = 1h).
	janitorMaxAge time.Duration
}

// WithLogger задаёт кастомный логгер для сервера.
func WithLogger(l observability.Logger) Option {
	return func(o *serverOptions) { o.logger = l }
}

// WithMetrics задаёт кастомные метрики.
func WithMetrics(m observability.Metrics) Option {
	return func(o *serverOptions) { o.metrics = m }
}

// WithPixelGenerator задаёт кастомный генератор пикселей для not-found
// fallback. По умолчанию используется встроенный генератор (adapters/pixel).
func WithPixelGenerator(p httpapi.PixelGenerator) Option {
	return func(o *serverOptions) { o.pixelGen = p }
}

// WithJanitorInterval задаёт период уборки temp-файлов публикации.
func WithJanitorInterval(d time.Duration) Option {
	return func(o *serverOptions) { o.janitorInterval = d }
}

// WithJanitorMaxAge задаёт возраст temp-файла, после которого он считается
// брошенным.
func WithJanitorMaxAge(d time.Duration) Option {
	return func(o *serverOptions) { o.janitorMaxAge = d }
}

// Server — собранный HTTP-сервер imager. Обёртка над httpapi.Runtime.
type Server struct {
	rt              *httpapi.Runtime
	logger          observability.Logger
	handler         http.Handler
	shutdownTimeout time.Duration
}

// NewServer собирает и запускает полный HTTP-сервер из YAML-конфига в
// каталоге cfgDir (сценарий "как cmd/imager").
//
// Читает три слоя: setting (setting.yaml + setting-local.yaml, обязателен
// только setting.yaml), generate (generate.yaml + generate-local.yaml) и
// failback (failback.yaml + failback-local.yaml); собирает pipeline
// (хранилища, процессоры, детектор, admin, janitor) и создаёт runtime.
// Сервер ещё не слушает — для запуска вызовите Run(ctx).
//
// Опции: WithLogger, WithMetrics, WithPixelGenerator, WithJanitorInterval,
// WithJanitorMaxAge.
func NewServer(cfgDir string, opts ...Option) (*Server, error) {
	o := &serverOptions{
		janitorInterval: 5 * time.Minute,
		janitorMaxAge:   1 * time.Hour,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	// 1) Единый typed runtime config из YAML (fail-fast).
	rc, err := httpapi.LoadConfigDir(cfgDir)
	if err != nil {
		return nil, fmt.Errorf("imager: load config: %w", err)
	}

	// 2) Observability: структурированный JSON-логгер и метрики.
	logger := o.logger
	if logger == nil {
		logger = observability.NewSlogLogger(bootstrap.SlogLevel(rc.LogLevel))
	}
	metrics := o.metrics
	if metrics == nil {
		metrics = observability.NewStdMetrics()
	}
	rc.HTTP.Logger = logger
	rc.HTTP.Metrics = metrics
	if o.pixelGen != nil {
		rc.HTTP.Pixel = o.pixelGen
	} else {
		rc.HTTP.Pixel = pixel.New()
	}

	// 3) Процессоры: libvips (primary, если скомпилирован) + ImageMagick
	// (опциональный fallback для сборок без тега "libvips").
	proc, err := bootstrap.BuildProcessor(logger, rc)
	if err != nil {
		return nil, fmt.Errorf("imager: processor: %w", err)
	}

	// 4) Composition root: собирает pipeline (fail-fast на invalid config).
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
		return nil, fmt.Errorf("imager: build: %w", err)
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
		return nil, fmt.Errorf("imager: runtime: %v", err)
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
	janitor, jErr := fs.NewJanitor(rc.ResultDir, fs.JanitorOptions{
		Interval: o.janitorInterval,
		MaxAge:   o.janitorMaxAge,
	})
	if jErr != nil {
		return nil, fmt.Errorf("imager: janitor: %w", jErr)
	}
	if err := janitor.Start(); err != nil {
		return nil, fmt.Errorf("imager: janitor start: %w", err)
	}
	rt.AddCloser(janitorCloser{j: janitor})

	// 6) Health (readiness + liveness) + metrics привязаны к runtime.
	health := httpapi.NewHealth(rt)
	// Admission control: ограничиваем число одновременно обрабатываемых
	// asset-запросов лимитом из конфигурации (http.max-concurrent-requests).
	handler := httpapi.NewMuxWithAdmission(app.Handler, health, metrics,
		httpapi.MetricsAuthConfig{}, rc.HTTP.MaxConcurrentRequests, app.AdminHandler)
	rt.SetHandler(handler)

	shutdownTimeout := rc.Server.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 15 * time.Second
	}

	return &Server{
		rt:              rt,
		logger:          logger,
		handler:         handler,
		shutdownTimeout: shutdownTimeout,
	}, nil
}

// Addr возвращает фактический адрес прослушивания.
func (s *Server) Addr() string {
	if s == nil || s.rt == nil {
		return ""
	}
	return s.rt.Addr().String()
}

// Handler возвращает корневой HTTP handler (для встраивания в собственный
// mux/сервер внешнего приложения).
func (s *Server) Handler() http.Handler {
	if s == nil {
		return nil
	}
	return s.handler
}

// Run запускает HTTP-сервер и блокирует до завершения (graceful shutdown по
// сигналу ОС или отмене ctx). Возвращает ошибку сервера, если он упал до
// сигнала.
func (s *Server) Run(ctx context.Context) error {
	if s == nil || s.rt == nil {
		return fmt.Errorf("imager: server is nil")
	}
	s.logger.Infof("imager: listening on %s", s.rt.Addr())

	// Запуск сервера в фоне; воркер защищён от паники.
	serveErr := make(chan error, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Errorf("imager: panic in server worker: %v", rec)
				serveErr <- fmt.Errorf("imager: panic in server worker: %v", rec)
			}
		}()
		serveErr <- s.rt.Serve()
	}()

	// Ожидание сигнала завершения ИЛИ раннего отказа сервера.
	sigCh := make(chan os.Signal, 1)
	go func() {
		sigCh <- httpapi.WaitSignal(ctx)
	}()
	select {
	case sig := <-sigCh:
		if sig != nil {
			s.logger.Infof("imager: received signal %v, shutting down", sig)
		}
	case err := <-serveErr:
		s.logger.Errorf("imager: server failed before signal: %v", err)
		return err
	}

	// Bounded graceful shutdown (таймаут из конфигурации сервера).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	if err := s.rt.Shutdown(shutdownCtx); err != nil {
		s.logger.Errorf("imager: shutdown: %v", err)
	}

	// Ожидание завершения сервера.
	if err := <-serveErr; err != nil {
		s.logger.Errorf("imager: server: %v", err)
		return err
	}
	s.logger.Infof("imager: stopped")
	return nil
}

// Shutdown выполняет bounded graceful shutdown с указанным таймаутом.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.rt == nil {
		return nil
	}
	return s.rt.Shutdown(ctx)
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
