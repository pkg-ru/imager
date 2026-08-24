package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
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

	// maxBodyBytes — жёсткий лимит тела запроса (п.7).
	maxBodyBytes int64

	// closers — ресурсы (хранилища/процессор/координатор), закрываемые при
	// Shutdown (п.6). Закрываются только те, что реализуют io.Closer.
	closers []io.Closer

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
	// MaxBodyBytes — максимальный размер тела запроса (0 = без лимита).
	MaxBodyBytes int64
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
		opts.MaxHeaderBytes = 32 << 10 // 32 KiB (п.7: уменьшен с 1 MiB)
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
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
	// П.7: жёсткий лимит тела запроса (сервис не принимает тела). Оборачиваем
	// handler в MaxBytesHandler; SetHandler переустанавливает handler, поэтому
	// обёртка применяется в Serve.
	rt.maxBodyBytes = opts.MaxBodyBytes

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

// AddCloser регистрирует ресурс, закрываемый при Shutdown (п.6).
// Ресурс закрывается только если реализует io.Closer.
func (rt *Runtime) AddCloser(c any) {
	if c == nil {
		return
	}
	if cl, ok := c.(io.Closer); ok {
		rt.mu.Lock()
		rt.closers = append(rt.closers, cl)
		rt.mu.Unlock()
	}
}

// Serve запускает HTTP-сервер и блокирует до завершения.
// Возвращает ошибку сервера (кроме http.ErrServerClosed).
func (rt *Runtime) Serve() error {
	// П.7: применяем лимит тела запроса к текущему handler (SetHandler мог
	// заменить handler после NewRuntime).
	rt.server.Handler = http.MaxBytesHandler(rt.server.Handler, rt.maxBodyBytes)
	err := rt.server.Serve(rt.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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

	// П.6: закрываем зарегистрированные ресурсы (хранилища/процессор/
	// координатор). Закрытие bounded по shutdownCtx.
	rt.mu.Lock()
	closers := append([]io.Closer(nil), rt.closers...)
	rt.mu.Unlock()
	for _, c := range closers {
		select {
		case <-shutdownCtx.Done():
			// Таймаут shutdown — прекращаем закрывать ресурсы.
			if err == nil {
				err = shutdownCtx.Err()
			}
			rt.alive.Store(false)
			return err
		default:
		}
		_ = c.Close()
	}

	rt.alive.Store(false)
	return err
}

// WaitSignal ожидает сигнал завершения (SIGINT/SIGTERM) и возвращает его.
// Не создаёт утечек: регистрирует обработчик и снимает его после получения.
//
// П.20: SIGTERM регистрируется только на Unix-платформах (на Windows
// syscall.SIGTERM не поддерживается).
func WaitSignal(ctx context.Context) os.Signal {
	ch := make(chan os.Signal, 1)
	if runtime.GOOS == "windows" {
		signal.Notify(ch, os.Interrupt)
	} else {
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	}
	defer signal.Stop(ch)
	select {
	case sig := <-ch:
		return sig
	case <-ctx.Done():
		return nil
	}
}
