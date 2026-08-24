package ftp

import (
	"context"

	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
)

// pooledConn — соединение, выданное пулом. Жизненным циклом соединения
// управляет пул: Quit() обёртке не нужен, закрытие выполняется через
// discard -> remote.Entry.Discard -> close-колбэк пула.
type pooledConn struct {
	conn
	entry *remote.Entry[conn]
}

// discard закрывает соединение и освобождает слот пула. Идемпотентно.
// Для соединений вне пула (тестовый Dialer) закрывает напрямую.
func (p *pooledConn) discard() {
	if p == nil {
		return
	}
	if p.entry != nil {
		p.entry.Discard()
		return
	}
	_ = p.conn.Quit()
}

// connPool — тонкая обёртка над generic-пулом remote.Pool, ограничивающая
// число одновременных FTP/FTPS-соединений значением MaxConns (минимум 2).
// Соединения не переиспользуются: после каждой операции они закрываются через
// discard, а пул лишь следит за лимитом одновременных соединений.
// MaxIdleConns сохранён в Options для совместимости конфигурации и больше не
// применяется напрямую.
type connPool struct {
	pool *remote.Pool[conn]
}

func newConnPool(opts Options) *connPool {
	dial := func(ctx context.Context) (conn, error) { return opts.dial(ctx) }
	return &connPool{pool: remote.NewPool(dial, func(c conn) error { return c.Quit() }, opts.MaxConns)}
}

// acquire создаёт новое соединение, захватив слот лимита пула.
func (p *connPool) acquire(ctx context.Context) (*pooledConn, error) {
	e, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &pooledConn{conn: e.Value, entry: e}, nil
}
