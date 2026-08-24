package remote

import (
	"context"
	"sync/atomic"
)

// Pool — обобщённый пул соединений удалённого хранилища, работающий в режиме
// limiter: он ограничивает число одновременных соединений значением max
// (минимум 2), но не переиспользует их — каждое соединение закрывается через
// Entry.Discard после операции, а освободившийся слот разрешает dial нового.
//
// Поведение перенесено из пулов адаптеров ftp и sftp после удаления цепочек
// переиспользования (release/put): dial выполняется после захвата слота, без
// блокировок вокруг самого соединения, поэтому медленный/упавший dial не
// мешает другим операциям, а конкурентные операции работают параллельно в
// пределах лимита.
type Pool[T any] struct {
	dial  func(ctx context.Context) (T, error)
	close func(T) error
	max   int
	slots chan struct{}
}

// Entry — соединение, выданное пулом. Discard идемпотентен: повторный вызов
// и вызов для nil-Entry безопасны.
type Entry[T any] struct {
	Value    T
	pool     *Pool[T]
	released atomic.Bool
}

// NewPool создаёт пул с лимитом max одновременных соединений (минимум 2).
// dial создаёт новое соединение, closeFn закрывает его.
func NewPool[T any](
	dial func(ctx context.Context) (T, error),
	closeFn func(T) error,
	max int,
) *Pool[T] {
	if max < 2 {
		max = 2
	}
	return &Pool[T]{
		dial:  dial,
		close: closeFn,
		max:   max,
		slots: make(chan struct{}, max),
	}
}

// Acquire захватывает слот лимита и создаёт новое соединение через dial.
// При достижении предела max ждёт освобождения слота до закрытия ctx.
func (p *Pool[T]) Acquire(ctx context.Context) (*Entry[T], error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	v, err := p.dial(ctx)
	if err != nil {
		<-p.slots
		return nil, err
	}
	return &Entry[T]{Value: v, pool: p}, nil
}

// Discard закрывает соединение через close-колбэк и освобождает слот пула,
// разрешая Acquire создать новое. Идемпотентно.
func (e *Entry[T]) Discard() {
	if e == nil || e.released.Swap(true) {
		return
	}
	if e.pool == nil {
		return
	}
	if e.pool.close != nil {
		_ = e.pool.close(e.Value)
	}
	<-e.pool.slots
}
