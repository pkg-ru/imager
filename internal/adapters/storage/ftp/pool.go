package ftp

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// pooledConn оборачивает соединение и делает Quit() no-op, потому что
// жизненным циклом соединения управляет пул (release/discard).
type pooledConn struct {
	conn
	pool     *connPool
	released atomic.Bool
	// lastUsed — время последнего возврата в пул (UnixNano) для проверки
	// IdleConnTimeout при повторном acquire.
	lastUsed int64
}

func (p *pooledConn) Quit() error {
	return nil // пул управляет закрытием
}

// release возвращает соединение в пул. Идемпотентно.
func (p *pooledConn) release() {
	if p == nil || p.released.Swap(true) {
		return
	}
	if p.pool == nil {
		_ = p.conn.Quit()
		return
	}
	p.pool.put(p)
}

// discard закрывает соединение, позволяя пулу dial-ить новое. Идемпотентно.
func (p *pooledConn) discard() {
	if p == nil || p.released.Swap(true) {
		return
	}
	if p.pool == nil {
		_ = p.conn.Quit()
		return
	}
	p.pool.discard(p)
}

// connPool — пул FTP/FTPS-соединений с dial() вне блокировки.
//
// Держит до MaxConns одновременных соединений (минимум 2). Idle-соединения
// хранятся в буферизованном канале; конкурентные операции могут выполняться
// параллельно, а медленный/упавший dial не блокирует другие ключи.
//
// Параметры пула:
//   - MaxConns — максимальное число одновременных соединений (0 = 2).
//   - MaxIdleConns — сохранён для совместимости конфигурации; управлял
//     числом idle-соединений в старой реализации и больше не применяется
//     напрямую (число idle ограничено MaxConns).
//   - IdleConnTimeout > 0 — закрывать простаивающее соединение, если оно
//     не использовалось дольше таймаута.
type connPool struct {
	opts   Options
	max    int
	idle   chan *pooledConn
	cur    atomic.Int32
	closed atomic.Bool
}

func newConnPool(opts Options) *connPool {
	max := opts.MaxConns
	if max < 2 {
		max = 2
	}
	return &connPool{
		opts: opts,
		max:  max,
		idle: make(chan *pooledConn, max),
	}
}

// acquire возвращает соединение из пула.
//
// Сначала пробует взять idle-соединение. Если подходящих нет и число
// созданных соединений меньше max — создаёт новое через p.opts.dial() БЕЗ
// удержания блокировки: параллельные acquire могут диалить одновременно,
// а единственное общее состояние (счётчик cur) защищено атомарно.
// Если достигнут предел max — ждёт idle-соединение до закрытия ctx.
func (p *connPool) acquire(ctx context.Context) (*pooledConn, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("ftp: connection pool closed")
	}
	for {
		select {
		case pc := <-p.idle:
			if !p.fresh(pc) {
				p.closeStale(pc)
				continue
			}
			return pc, nil
		default:
		}
		cur := p.cur.Load()
		if cur < int32(p.max) && p.cur.CompareAndSwap(cur, cur+1) {
			c, err := p.opts.dial(ctx)
			if err != nil {
				p.cur.Add(-1)
				return nil, err
			}
			return &pooledConn{conn: c, pool: p}, nil
		}
		select {
		case pc := <-p.idle:
			if !p.fresh(pc) {
				p.closeStale(pc)
				continue
			}
			return pc, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// fresh сообщает, не истёк ли idle-таймаут для соединения.
func (p *connPool) fresh(pc *pooledConn) bool {
	if p.opts.IdleConnTimeout <= 0 || pc.lastUsed == 0 {
		return true
	}
	return time.Since(time.Unix(0, pc.lastUsed)) < p.opts.IdleConnTimeout
}

// closeStale закрывает простаревшее idle-соединение и разрешает dial.
func (p *connPool) closeStale(pc *pooledConn) {
	_ = pc.conn.Quit()
	p.cur.Add(-1)
}

// put возвращает соединение в пул. Если канал полон или пул закрыт —
// соединение закрывается (пул не накапливает сверхлимитные).
func (p *connPool) put(pc *pooledConn) {
	pc.lastUsed = time.Now().UnixNano()
	if p.closed.Load() {
		_ = pc.conn.Quit()
		p.cur.Add(-1)
		return
	}
	select {
	case p.idle <- pc:
	default:
		_ = pc.conn.Quit()
		p.cur.Add(-1)
	}
}

// discard закрывает конкретное соединение, разрешая пулу создать новое.
func (p *connPool) discard(pc *pooledConn) {
	_ = pc.conn.Quit()
	p.cur.Add(-1)
}

// close закрывает пул и все idle-соединения.
func (p *connPool) close() {
	p.closed.Store(true)
	for {
		select {
		case pc := <-p.idle:
			_ = pc.conn.Quit()
			p.cur.Add(-1)
		default:
			return
		}
	}
}
