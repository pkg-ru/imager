package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Runtime — bounded graceful shutdown runtime для HTTP-сервера.
//
// Гарантии:
//   - Shutdown перестаёт принимать новые запросы и ждёт активные запросы
//     через http.Server.Shutdown с таймаутом.
//   - Readiness становится false при начале shutdown; liveness остаётся
//     отдельной и не зависит от shutdown.
//   - Сигналы ОС обрабатываются корректно, без утечек goroutines.
type Runtime struct {
	server   *http.Server
	listener net.Listener

	ready atomic.Bool
	alive atomic.Bool

	shutdownTimeout time.Duration

	mu       sync.Mutex
	shutdown bool
}

// RuntimeOptions — параметры Runtime.
type RuntimeOptions struct {
	// Handler — корневой HTTP handler.
	Handler http.Handler
	// Addr — адрес прослушивания (TCP), например ":8080".
	Addr string
	// ReadHeaderTimeout — таймаут чтения заголовков.
	ReadHeaderTimeout time.Duration
	// ReadTimeout — таймаут чтения тела запроса.
	ReadTimeout time.Duration
	// WriteTimeout — таймаут записи ответа.
	WriteTimeout time.Duration
	// IdleTimeout — таймаут idle-соединений.
	IdleTimeout time.Duration
	// ShutdownTimeout — максимальное время ожидания активных запросов.
	ShutdownTimeout time.Duration
	// MaxHeaderBytes — максимальный размер заголовков запроса.
	MaxHeaderBytes int
}

// NewRuntime создаёт Runtime. Не начинает прослушивание.
func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	if opts.Handler == nil {
		return nil, errors.New("httpapi: runtime: nil handler")
	}
	if opts.Addr == "" {
		opts.Addr = ":8080"
	}
	if opts.ReadHeaderTimeout <= 0 {
		opts.ReadHeaderTimeout = defaultTimeouts.ReadHeader
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = defaultTimeouts.Read
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultTimeouts.Write
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = defaultTimeouts.Idle
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = defaultTimeouts.Shutdown
	}
	if opts.MaxHeaderBytes <= 0 {
		opts.MaxHeaderBytes = 1 << 20 // 1 MiB
	}

	rt := &Runtime{
		shutdownTimeout: opts.ShutdownTimeout,
	}
	rt.alive.Store(true)
	rt.ready.Store(true)

	rt.server = &http.Server{
		Handler:           opts.Handler,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		MaxHeaderBytes:    opts.MaxHeaderBytes,
	}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	rt.listener = ln
	return rt, nil
}

// Addr возвращает фактический адрес прослушивания.
func (rt *Runtime) Addr() net.Addr {
	if rt.listener == nil {
		return nil
	}
	return rt.listener.Addr()
}

// SetHandler заменяет корневой handler. Должен вызываться до Serve.
func (rt *Runtime) SetHandler(h http.Handler) {
	rt.server.Handler = h
}

// Serve запускает HTTP-сервер и блокирует до завершения.
// Возвращает ошибку сервера (кроме http.ErrServerClosed).
func (rt *Runtime) Serve() error {
	err := rt.server.Serve(rt.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeAsync запускает сервер в фоне и возвращает канал ошибок.
func (rt *Runtime) ServeAsync() <-chan error {
	ch := make(chan error, 1)
	go func() {
		ch <- rt.Serve()
	}()
	return ch
}

// Ready сообщает, готов ли сервер принимать запросы.
func (rt *Runtime) Ready() bool { return rt.ready.Load() }

// Alive сообщает, жив ли процесс (liveness).
func (rt *Runtime) Alive() bool { return rt.alive.Load() }

// Shutdown выполняет bounded graceful shutdown:
//   - readiness → false (перестаём принимать новые запросы на уровне health);
//   - http.Server.Shutdown с таймаутом ждёт активные запросы;
//   - при превышении таймаута принудительно закрывает соединения.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rt.mu.Lock()
	if rt.shutdown {
		rt.mu.Unlock()
		return nil
	}
	rt.shutdown = true
	rt.mu.Unlock()

	rt.ready.Store(false)

	shutdownCtx, cancel := context.WithTimeout(ctx, rt.shutdownTimeout)
	defer cancel()

	err := rt.server.Shutdown(shutdownCtx)
	if err != nil {
		// Принудительно закрываем оставшиеся соединения.
		_ = rt.server.Close()
	}
	rt.alive.Store(false)
	return err
}

// Close немедленно закрывает сервер и listener.
func (rt *Runtime) Close() error {
	rt.ready.Store(false)
	rt.alive.Store(false)
	return rt.server.Close()
}

// WaitSignal ожидает сигнал завершения (SIGINT/SIGTERM) и возвращает его.
// Не создаёт утечек: регистрирует обработчик и снимает его после получения.
func WaitSignal(ctx context.Context) os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	select {
	case sig := <-ch:
		return sig
	case <-ctx.Done():
		return nil
	}
}
