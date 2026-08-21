package sftp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// pooledClient оборачивает client и делает Close() no-op,
// так как пул управляет жизненным циклом соединения.
type pooledClient struct {
	client
	pool *connPool
}

func (p *pooledClient) Close() error {
	return nil // пул управляет закрытием
}

// connPool — lazy singleton SFTP-соединение с автоматическим
// восстановлением при ошибках. Все операции в рамках одного
// экземпляра Store переиспользуют одно TCP+SSH+SFTP-соединение,
// что устраняет накладные расходы handshake на каждую операцию.
//
// Параметры пула:
//   - MaxIdleConns == 0 — не кэшировать соединение между операциями
//     (каждый acquire создаёт новое, release закрывает).
//   - IdleConnTimeout > 0 — закрывать простаивающее соединение, если
//     оно не использовалось дольше таймаута.
type connPool struct {
	opts   Options
	mu     sync.Mutex
	client client
	// lastUsed — время последнего возврата соединения в пул.
	lastUsed time.Time
	closed   atomic.Bool
}

func newConnPool(opts Options) *connPool {
	return &connPool{opts: opts}
}

// acquire возвращает клиента из пула. При первом вызове создаёт
// соединение через opts.dial(). При ошибке соединение сбрасывается.
func (p *connPool) acquire(ctx context.Context) (client, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("sftp: connection pool closed")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		// Если соединение простаивало дольше IdleConnTimeout — закрываем.
		if p.opts.IdleConnTimeout > 0 && time.Since(p.lastUsed) > p.opts.IdleConnTimeout {
			_ = p.client.Close()
			p.client = nil
		} else {
			return &pooledClient{client: p.client, pool: p}, nil
		}
	}
	c, err := p.opts.dial()
	if err != nil {
		return nil, err
	}
	p.client = c
	p.lastUsed = time.Now()
	return &pooledClient{client: c, pool: p}, nil
}

// release возвращает соединение в пул. Если MaxIdleConns == 0, соединение
// закрывается сразу (пул не хранит idle-соединения).
func (p *connPool) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opts.MaxIdleConns == 0 && p.client != nil {
		_ = p.client.Close()
		p.client = nil
		return
	}
	p.lastUsed = time.Now()
}

// discard сбрасывает соединение при ошибке, чтобы следующий
// acquire создал новое.
func (p *connPool) discard() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		_ = p.client.Close()
		p.client = nil
	}
}

// close закрывает соединение и помечает пул как закрытый.
func (p *connPool) close() {
	p.closed.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		_ = p.client.Close()
		p.client = nil
	}
}
