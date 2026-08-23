package sftp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// pooledClient оборачивает клиента и делает Close() no-op, потому что
// жизненным циклом клиента управляет пул (release/discard).
type pooledClient struct {
	client
	pool     *connPool
	released atomic.Bool
	// lastUsed — время последнего возврата в пул (UnixNano) для проверки
	// IdleConnTimeout при повторном acquire.
	lastUsed int64
}

func (p *pooledClient) Close() error {
	return nil // пул управляет закрытием
}

// release возвращает клиента в пул. Идемпотентно.
func (p *pooledClient) release() {
	if p == nil || p.released.Swap(true) {
		return
	}
	if p.pool == nil {
		_ = p.client.Close()
		return
	}
	p.pool.put(p)
}

// discard закрывает клиента, позволяя пулу dial-ить нового. Идемпотентно.
func (p *pooledClient) discard() {
	if p == nil || p.released.Swap(true) {
		return
	}
	if p.pool == nil {
		_ = p.client.Close()
		return
	}
	p.pool.discard(p)
}

// connPool — пул SFTP-клиентов с dial() вне блокировки.
//
// Держит до MaxConns одновременных клиентов (минимум 2). Idle-клиенты
// хранятся в буферизованном канале; конкурентные операции могут выполняться
// параллельно, а медленный/упавший dial не блокирует другие ключи.
//
// Параметры пула:
//   - MaxConns — максимальное число одновременных клиентов (0 = 2).
//   - MaxIdleConns — сохранён для совместимости конфигурации; управлял
//     числом idle-клиентов в старой реализации и больше не применяется
//     напрямую (число idle ограничено MaxConns).
//   - IdleConnTimeout > 0 — закрывать простаивающий клиент, если он
//     не использовался дольше таймаута.
type connPool struct {
	opts   Options
	max    int
	idle   chan *pooledClient
	cur    atomic.Int32
	closed atomic.Bool
	// closeMu сериализует put/close, устраняя гонку: без него клиент,
	// прошедший проверку closed в put до close(), мог быть добавлен в
	// idle после осушения канала и утечь (не закрыт, cur не уменьшен).
	closeMu sync.Mutex
}

func newConnPool(opts Options) *connPool {
	max := opts.MaxConns
	if max < 2 {
		max = 2
	}
	return &connPool{
		opts: opts,
		max:  max,
		idle: make(chan *pooledClient, max),
	}
}

// acquire возвращает клиента из пула.
//
// Сначала пробует взять idle-клиента. Если подходящих нет и число созданных
// клиентов меньше max — создаёт нового через p.opts.dial() БЕЗ удержания
// блокировки: параллельные acquire могут диалить одновременно, а единственное
// общее состояние (счётчик cur) защищено атомарно. Если достигнут предел
// max — ждёт idle-клиента до закрытия ctx.
func (p *connPool) acquire(ctx context.Context) (*pooledClient, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("sftp: connection pool closed")
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
			c, err := p.opts.dial()
			if err != nil {
				p.cur.Add(-1)
				return nil, err
			}
			return &pooledClient{client: c, pool: p}, nil
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

// fresh сообщает, не истёк ли idle-таймаут для клиента.
func (p *connPool) fresh(pc *pooledClient) bool {
	if p.opts.IdleConnTimeout <= 0 || pc.lastUsed == 0 {
		return true
	}
	return time.Since(time.Unix(0, pc.lastUsed)) < p.opts.IdleConnTimeout
}

// closeStale закрывает простаревшего idle-клиента и разрешает dial.
func (p *connPool) closeStale(pc *pooledClient) {
	_ = pc.client.Close()
	p.cur.Add(-1)
}

// put возвращает клиента в пул. Если канал полон или пул закрыт — клиент
// закрывается (пул не накапливает сверхлимитные).
func (p *connPool) put(pc *pooledClient) {
	pc.lastUsed = time.Now().UnixNano()
	// closeMu устраняет гонку с close(): проверка closed и запись в idle
	// атомарны относительно осушения канала в close().
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed.Load() {
		_ = pc.client.Close()
		p.cur.Add(-1)
		return
	}
	select {
	case p.idle <- pc:
	default:
		_ = pc.client.Close()
		p.cur.Add(-1)
	}
}

// discard закрывает конкретного клиента, разрешая пулу создать нового.
func (p *connPool) discard(pc *pooledClient) {
	_ = pc.client.Close()
	p.cur.Add(-1)
}

// close закрывает пул и всех idle-клиентов.
func (p *connPool) close() {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	p.closed.Store(true)
	for {
		select {
		case pc := <-p.idle:
			_ = pc.client.Close()
			p.cur.Add(-1)
		default:
			return
		}
	}
}
