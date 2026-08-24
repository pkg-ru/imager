package sftp

import (
	"context"

	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
)

// pooledClient — клиент, выданный пулом. Close() — no-op: жизненным циклом
// клиента управляет пул (discard закрывает его через remote.Pool).
type pooledClient struct {
	client
	entry *remote.Entry[client]
}

func (p *pooledClient) Close() error {
	return nil // пул управляет закрытием
}

// discard закрывает клиента и освобождает слот пула. Идемпотентно.
func (p *pooledClient) discard() {
	if p == nil {
		return
	}
	if p.entry != nil {
		p.entry.Discard()
		return
	}
	_ = p.client.Close()
}

// connPool — тонкая обёртка над generic-пулом remote.Pool, ограничивающая
// число одновременных SFTP-клиентов значением MaxConns (минимум 2). Клиенты
// не переиспользуются: после каждой операции они закрываются через discard,
// а пул лишь следит за лимитом одновременных клиентов. MaxIdleConns сохранён
// в Options для совместимости конфигурации и больше не применяется напрямую.
type connPool struct {
	pool *remote.Pool[client]
}

func newConnPool(opts Options) *connPool {
	dial := func(ctx context.Context) (client, error) { return opts.dial() }
	return &connPool{pool: remote.NewPool(dial, func(c client) error { return c.Close() }, opts.MaxConns)}
}

// acquire создаёт нового клиента, захватив слот лимита пула.
func (p *connPool) acquire(ctx context.Context) (*pooledClient, error) {
	e, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &pooledClient{client: e.Value, entry: e}, nil
}
