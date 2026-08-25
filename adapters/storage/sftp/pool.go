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
	if p == nil || p.client == nil {
		return
	}
	if p.entry != nil {
		p.entry.Discard()
		return
	}
	_ = p.client.Close()
}

// detach отключает клиента от владения каркаса: последующий discard
// становится no-op (соединение и слот пула не затрагиваются). Используется
// Publish при ErrConflict, чтобы бизнес-отказ не приводил к закрытию
// соединения каркасом.
func (p *pooledClient) detach() {
	if p == nil {
		return
	}
	p.entry = nil
	p.client = nil
}

// connPool — тонкая обёртка над generic-пулом remote.Pool, ограничивающая
// число одновременных SFTP-клиентов значением MaxConns (минимум 2). Клиенты
// не переиспользуются: после каждой операции они закрываются через discard,
// а пул лишь следит за лимитом одновременных клиентов. MaxIdleConns в Options
// игнорируется (не применяется напрямую).
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
