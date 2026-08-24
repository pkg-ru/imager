package ftp

import (
	"context"

	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
)

// store — общая часть SourceStore и ResultStore: опции и доступ к пулу.
type store struct {
	opts Options
	pool *connPool
}

// newStore валидирует опции и создаёт пул (если не задан тестовый Dialer).
func newStore(opts Options) (store, error) {
	if err := opts.validate(); err != nil {
		return store{}, err
	}
	var pool *connPool
	if opts.Dialer == nil {
		pool = newConnPool(opts)
	}
	return store{opts: opts, pool: pool}, nil
}

// getConn возвращает соединение из пула или opts.Dialer для тестов.
func (s *store) getConn(ctx context.Context) (*pooledConn, error) {
	if s.pool != nil {
		return s.pool.acquire(ctx)
	}
	// Для тестов с Dialer используем прямой вызов.
	c, err := s.opts.dial(ctx)
	if err != nil {
		return nil, err
	}
	return &pooledConn{conn: c}, nil
}

// withRetry — retry-каркас операций: acquire -> needDiscard defer -> op ->
// при ошибке соединения discard и повторная попытка (до MaxAttempts).
//
// Контракт op: возвращает результат, сырую ошибку raw (по ней выполняется
// классификация isConnErr) и замапленную ошибку mapped (возвращается
// вызывающему; при raw == nil не используется). Бизнес-ошибки и исчерпанный
// ctx завершают цикл сразу; после исчерпания попыток возвращается последняя
// mapped-ошибка.
func withRetry[T any](ctx context.Context, s *store, op func(c *pooledConn) (T, error, error)) (T, error) {
	ctx, cancel := s.opts.withTimeout(ctx)
	defer cancel()
	attempts := s.opts.attempts()
	var lastErr error
	for range attempts {
		c, err := s.getConn(ctx)
		if err != nil {
			var zero T
			return zero, remote.MapError("ftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				c.discard()
			}
		}()
		v, raw, mapped := op(c)
		if raw == nil && mapped == nil {
			needDiscard = false
			return v, nil
		}
		lastErr = mapped
		if !isConnErr(raw) {
			var zero T
			return zero, lastErr
		}
		c.discard()
		if ctx.Err() != nil {
			var zero T
			return zero, lastErr
		}
	}
	var zero T
	return zero, lastErr
}
