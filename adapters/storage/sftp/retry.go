package sftp

import (
	"context"

	"gitverse.ru/pkg-ru/imager/adapters/storage/remote"
)

// store — общая часть SourceStore и ResultStore: опции и доступ к пулу.
// Встраивает общий remote.Store (см. remote/poolutil.go); локальная
// структура нужна, чтобы определять методы (openBuffered).
type store struct {
	remote.Store[client, Options]
}

// newStore валидирует опции и создаёт пул (если не задан тестовый Client).
func newStore(opts Options) (store, error) {
	s, err := remote.NewStore(
		opts,
		func(o Options) error { return o.validate() },
		opts.Client == nil,
		func(ctx context.Context) (client, error) { return opts.dial() },
		func(c client) error { return c.Close() },
		opts.MaxConns,
	)
	return store{Store: s}, err
}

// withRetryPolicy — общий каркас retry-операций: тонкая обёртка над общим
// remote.WithRetry (прежде цикл дублировался здесь построчно). acquire -> op ->
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
// pooledClient.KeepAlive.
func withRetryPolicy[T any](ctx context.Context, s *store, policy func(error) bool, op func(cl *pooledClient) (T, error, error)) (T, error) {
	return remote.WithRetry(
		ctx,
		s.Opts.ConnOptions,
		s.Acquire,
		func(err error) error { return remote.MapError("sftp dial", err) },
		policy,
		func(c *remote.Pooled[client]) (T, error, error) {
			return op(&pooledClient{client: c.Value, Pooled: c})
		},
	)
}

// withRetry — retry-каркас операций: acquire -> op -> при ошибке соединения
// discard и повторная попытка (до MaxAttempts).
func withRetry[T any](ctx context.Context, s *store, op func(cl *pooledClient) (T, error, error)) (T, error) {
	return withRetryPolicy(ctx, s, remote.IsConnErr, op)
}

// pooledClient — SFTP-клиент внутри одной попытки: интерфейс client (методы
// промотируются) плюс общий remote.Pooled (сессия пула, discard/keepAlive).
type pooledClient struct {
	client
	*remote.Pooled[client]
}
