package sftp

import (
	"context"

	"github.com/pkg-ru/imager/adapters/storage/remote"
)

// store — общая часть SourceStore и ResultStore: опции и доступ к пулу.
type store struct {
	opts Options
	pool *remote.Pool[client]
}

// newStore валидирует опции и создаёт пул (если не задан тестовый Client).
func newStore(opts Options) (store, error) {
	if err := opts.validate(); err != nil {
		return store{}, err
	}
	var pool *remote.Pool[client]
	if opts.Client == nil {
		pool = remote.NewPool(
			func(context.Context) (client, error) { return opts.dial() },
			func(c client) error { return c.Close() },
			opts.MaxConns,
		)
	}
	return store{opts: opts, pool: pool}, nil
}

// acquire возвращает клиента из пула либо тестовый/прямой клиент
// (внепуловая Session, закрывающая клиента при Discard).
func (s *store) acquire(ctx context.Context) (*remote.Session[client], error) {
	closeFn := func(c client) error { return c.Close() }
	if s.pool != nil {
		e, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		return remote.NewSession(e), nil
	}
	if s.opts.Client != nil {
		return remote.NewDirectSession(s.opts.Client, closeFn), nil
	}
	c, err := s.opts.dial()
	if err != nil {
		return nil, err
	}
	return remote.NewDirectSession(c, closeFn), nil
}

// withRetryPolicy — общий каркас retry-операций: тонкая обёртка над общим
// remote.Retry (прежде цикл дублировался здесь построчно). acquire -> op ->
// классификация сырой ошибки политикой policy -> повторная попытка с новым
// соединением (до MaxAttempts) или немедленный возврат.
//
// Контракт op: возвращает результат, сырую ошибку raw (по ней policy
// решает, ретраить ли) и замапленную ошибку mapped (возвращается
// вызывающему; при успехе обе равны nil). Ошибки, не прошедшие policy, и
// исчерпанный ctx завершают цикл сразу; после исчерпания попыток
// возвращается последняя mapped-ошибка.
//
// Владение соединением: каркас закрывает соединение после каждой попытки;
// операция стриминга (ReadStream) передаёт владение потоку через
// pooledClient.keepAlive.
func withRetryPolicy[T any](ctx context.Context, s *store, policy func(error) bool, op func(cl *pooledClient) (T, error, error)) (T, error) {
	var owned bool
	spec := remote.RetrySpec[*remote.Session[client], T]{
		Acquire:        s.acquire,
		Discard:        func(c *remote.Session[client]) { c.Discard() },
		Policy:         policy,
		MapDialErr:     func(err error) error { return remote.MapError("sftp dial", err) },
		TakesOwnership: func(T) bool { return owned },
	}
	return remote.Retry(ctx, s.opts.ConnOptions, spec, func(c *remote.Session[client]) (T, error, error) {
		owned = false
		return op(&pooledClient{client: c.Value, session: c, keep: func() { owned = true }})
	})
}

// withRetry — retry-каркас операций: acquire -> op -> при ошибке соединения
// discard и повторная попытка (до MaxAttempts).
func withRetry[T any](ctx context.Context, s *store, op func(cl *pooledClient) (T, error, error)) (T, error) {
	return withRetryPolicy(ctx, s, remote.IsConnErr, op)
}

// pooledClient — SFTP-клиент внутри одной попытки: интерфейс client (методы
// промотируются) плюс ссылка на общую сессию пула.
type pooledClient struct {
	client
	session *remote.Session[client]
	keep    func()
}

// discard закрывает клиента и освобождает слот пула. Идемпотентно.
func (p *pooledClient) discard() {
	p.session.Discard()
}

// keepAlive передаёт владение соединением результату операции (стриминг):
// каркас не закроет соединение после успешного возврата — это сделает
// владелец потока через Session.Discard.
func (p *pooledClient) keepAlive() {
	p.keep()
}
