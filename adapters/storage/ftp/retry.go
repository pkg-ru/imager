package ftp

import (
	"context"

	"github.com/pkg-ru/imager/adapters/storage/remote"
)

// store — общая часть SourceStore и ResultStore: опции и доступ к пулу.
type store struct {
	opts Options
	pool *remote.Pool[conn]
}

// newStore валидирует опции и создаёт пул (если не задан тестовый Dialer).
func newStore(opts Options) (store, error) {
	if err := opts.validate(); err != nil {
		return store{}, err
	}
	var pool *remote.Pool[conn]
	if opts.Dialer == nil {
		pool = remote.NewPool(opts.dial, func(c conn) error { return c.Quit() }, opts.MaxConns)
	}
	return store{opts: opts, pool: pool}, nil
}

// acquire возвращает соединение из пула либо через тестовый Dialer
// (внепуловая Session, закрывающая соединение при Discard).
func (s *store) acquire(ctx context.Context) (*remote.Session[conn], error) {
	if s.pool != nil {
		e, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		return remote.NewSession(e), nil
	}
	c, err := s.opts.dial(ctx)
	if err != nil {
		return nil, err
	}
	return remote.NewDirectSession(c, func(c conn) error { return c.Quit() }), nil
}

// withRetry — retry-каркас FTP-операций: тонкая обёртка над общим
// remote.Retry (прежде цикл дублировался здесь построчно). Жизненный цикл:
// acquire -> op -> при ошибке соединения discard и повторная попытка
// (до MaxAttempts).
//
// Контракт op: возвращает результат, сырую ошибку raw (по ней выполняется
// классификация remote.IsConnErr) и замапленную ошибку mapped (возвращается
// вызывающему; при raw == nil не используется). Бизнес-ошибки и исчерпанный
// ctx завершают цикл сразу; после исчерпания попыток возвращается последняя
// mapped-ошибка.
//
// Владение соединением: каркас закрывает соединение после каждой попытки;
// операция стриминга (ReadStream) передаёт владение потоку через
// pooledConn.keepAlive.
func withRetry[T any](ctx context.Context, s *store, op func(c *pooledConn) (T, error, error)) (T, error) {
	var owned bool
	spec := remote.RetrySpec[*remote.Session[conn], T]{
		Acquire:        s.acquire,
		Discard:        func(c *remote.Session[conn]) { c.Discard() },
		Policy:         remote.IsConnErr,
		MapDialErr:     func(err error) error { return remote.MapError("ftp dial", err) },
		TakesOwnership: func(T) bool { return owned },
	}
	return remote.Retry(ctx, s.opts.ConnOptions, spec, func(c *remote.Session[conn]) (T, error, error) {
		owned = false
		return op(&pooledConn{conn: c.Value, session: c, keep: func() { owned = true }})
	})
}

// pooledConn — FTP-соединение внутри одной попытки: интерфейс conn (методы
// .promotируются) плюс ссылка на общую сессию пула.
type pooledConn struct {
	conn
	session *remote.Session[conn]
	keep    func()
}

// discard закрывает соединение и освобождает слот пула. Идемпотентно.
func (p *pooledConn) discard() {
	p.session.Discard()
}

// keepAlive передаёт владение соединением результату операции (стриминг):
// каркас не закроет соединение после успешного возврата — это сделает
// владелец потока через Session.Discard.
func (p *pooledConn) keepAlive() {
	p.keep()
}
