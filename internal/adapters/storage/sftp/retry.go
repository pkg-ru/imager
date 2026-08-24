package sftp

import (
	"context"

	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
)

// store — общая часть SourceStore и ResultStore: опции и доступ к пулу.
type store struct {
	opts Options
	pool *connPool
}

// newStore валидирует опции и создаёт пул (если не задан тестовый Client).
func newStore(opts Options) (store, error) {
	if err := opts.validate(); err != nil {
		return store{}, err
	}
	var pool *connPool
	if opts.Client == nil {
		pool = newConnPool(opts)
	}
	return store{opts: opts, pool: pool}, nil
}

// getClient возвращает клиента из пула или opts.Client для тестов.
func (s *store) getClient(ctx context.Context) (*pooledClient, error) {
	if s.pool != nil {
		return s.pool.acquire(ctx)
	}
	// Для тестов с fake client используем прямой вызов.
	if s.opts.Client != nil {
		return &pooledClient{client: s.opts.Client}, nil
	}
	c, err := s.opts.dial()
	if err != nil {
		return nil, err
	}
	return &pooledClient{client: c}, nil
}

// withRetryPolicy — общий каркас retry-операций: acquire -> needDiscard
// defer -> op -> классификация сырой ошибки политикой policy -> повторная
// попытка с новым соединением (до MaxAttempts) или немедленный возврат.
//
// Контракт op: возвращает результат, сырую ошибку raw (по ней policy
// решает, ретраить ли) и замапленную ошибку mapped (возвращается
// вызывающему; при успехе обе равны nil). Ошибки, не прошедшие policy, и
// исчерпанный ctx завершают цикл сразу; после исчерпания попыток
// возвращается последняя mapped-ошибка.
func withRetryPolicy[T any](ctx context.Context, s *store, policy func(error) bool, op func(cl *pooledClient) (T, error, error)) (T, error) {
	ctx, cancel := s.opts.withTimeout(ctx)
	defer cancel()
	attempts := s.opts.attempts()
	var lastErr error
	for range attempts {
		cl, err := s.getClient(ctx)
		if err != nil {
			var zero T
			return zero, remote.MapError("sftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				cl.discard()
			}
		}()
		v, raw, mapped := op(cl)
		if raw == nil && mapped == nil {
			needDiscard = false
			return v, nil
		}
		lastErr = mapped
		if !policy(raw) {
			var zero T
			return zero, lastErr
		}
		cl.discard()
		if ctx.Err() != nil {
			var zero T
			return zero, lastErr
		}
	}
	var zero T
	return zero, lastErr
}

// neverRetry — политика повтора Publish: ни одна ошибка не ретраится,
// операция выполняется ровно одной попыткой (историческое поведение
// метода: внутренние ошибки возвращаются сразу).
func neverRetry(error) bool { return false }

// withRetry — retry-каркас операций: acquire -> needDiscard defer -> op ->
// при ошибке соединения discard и повторная попытка (до MaxAttempts).
//
// Контракт op: возвращает результат, сырую ошибку raw (по ней выполняется
// классификация isClientErr) и замапленную ошибку mapped (возвращается
// вызывающему; при успехе обе равны nil). Бизнес-ошибки и исчерпанный ctx
// завершают цикл сразу; после исчерпания попыток возвращается последняя
// mapped-ошибка.
func withRetry[T any](ctx context.Context, s *store, op func(cl *pooledClient) (T, error, error)) (T, error) {
	return withRetryPolicy(ctx, s, isClientErr, op)
}
