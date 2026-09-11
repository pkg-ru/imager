package ftp

import (
	"context"

	"gitverse.ru/pkg-ru/imager/adapters/storage/remote"
)

// store — общая часть SourceStore и ResultStore: опции и доступ к пулу.
// Встраивает общий remote.Store (см. remote/poolutil.go); локальная
// структура нужна, чтобы определять методы (openBuffered).
type store struct {
	remote.Store[conn, Options]
}

// newStore валидирует опции и создаёт пул (если не задан тестовый Dialer).
func newStore(opts Options) (store, error) {
	s, err := remote.NewStore(
		opts,
		func(o Options) error { return o.validate() },
		opts.Dialer == nil,
		opts.dial,
		func(c conn) error { return c.Quit() },
		opts.MaxConns,
	)
	return store{Store: s}, err
}

// withRetry — retry-каркас FTP-операций: тонкая обёртка над общим
// remote.WithRetry (прежде цикл дублировался здесь построчно). Жизненный цикл:
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
// pooledConn.KeepAlive.
func withRetry[T any](ctx context.Context, s *store, op func(c *pooledConn) (T, error, error)) (T, error) {
	return remote.WithRetry(
		ctx,
		s.Opts.ConnOptions,
		s.Acquire,
		func(err error) error { return remote.MapError("ftp dial", err) },
		remote.IsConnErr,
		func(c *remote.Pooled[conn]) (T, error, error) {
			return op(&pooledConn{conn: c.Value, Pooled: c})
		},
	)
}

// pooledConn — FTP-соединение внутри одной попытки: интерфейс conn (методы
// промотируются) плюс общий remote.Pooled (сессия пула, discard/keepAlive).
type pooledConn struct {
	conn
	*remote.Pooled[conn]
}
