package ftp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// pooledConn оборачивает conn и делает Quit() no-op,
// так как пул управляет жизненным циклом соединения.
type pooledConn struct {
	conn
	pool *connPool
}

func (p *pooledConn) Quit() error {
	return nil // пул управляет закрытием
}

// connPool — lazy singleton FTP/FTPS-соединение с автоматическим
// восстановлением при ошибках. Все операции в рамках одного
// экземпляра Store переиспользуют одно TCP+TLS-соединение,
// что устраняет накладные расходы handshake + login на каждую операцию.
//
// Параметры пула:
//   - MaxIdleConns == 0 — не кэшировать соединение между операциями
//     (каждый acquire создаёт новое, release закрывает).
//   - IdleConnTimeout > 0 — закрывать простаивающее соединение, если
//     оно не использовалось дольше таймаута.
type connPool struct {
	opts Options
	mu   sync.Mutex
	conn conn
	// lastUsed — время последнего возврата соединения в пул.
	lastUsed time.Time
	closed   atomic.Bool
}

func newConnPool(opts Options) *connPool {
	return &connPool{opts: opts}
}

// acquire возвращает соединение из пула. При первом вызове создаёт
// соединение через opts.dial(). При ошибке соединение сбрасывается.
func (p *connPool) acquire(ctx context.Context) (conn, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("ftp: connection pool closed")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		// Если соединение простаивало дольше IdleConnTimeout — закрываем.
		if p.opts.IdleConnTimeout > 0 && time.Since(p.lastUsed) > p.opts.IdleConnTimeout {
			_ = p.conn.Quit()
			p.conn = nil
		} else {
			return &pooledConn{conn: p.conn, pool: p}, nil
		}
	}
	c, err := p.opts.dial(ctx)
	if err != nil {
		return nil, err
	}
	p.conn = c
	p.lastUsed = time.Now()
	return &pooledConn{conn: c, pool: p}, nil
}

// release возвращает соединение в пул. Если MaxIdleConns == 0, соединение
// закрывается сразу (пул не хранит idle-соединения).
func (p *connPool) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opts.MaxIdleConns == 0 && p.conn != nil {
		_ = p.conn.Quit()
		p.conn = nil
		return
	}
	p.lastUsed = time.Now()
}

// discard сбрасывает соединение при ошибке, чтобы следующий
// acquire создал новое.
func (p *connPool) discard() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Quit()
		p.conn = nil
	}
}

// close закрывает соединение и помечает пул как закрытый.
func (p *connPool) close() {
	p.closed.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Quit()
		p.conn = nil
	}
}
