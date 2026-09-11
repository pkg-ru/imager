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
// Dial выполняется после захвата слота, без блокировок вокруг самого
// соединения: медленный/упавший dial не мешает другим операциям, а
// конкурентные операции работают параллельно в пределах лимита.
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

// NewSession оборачивает пуловую запись в Session.
func NewSession[C any](e *Entry[C]) *Session[C] {
	return &Session[C]{Value: e.Value, entry: e}
}

// NewDirectSession создаёт Session для внепулового соединения (тестовый
// путь адаптеров): closeFn вызывается при Discard.
func NewDirectSession[C any](v C, closeFn func(C) error) *Session[C] {
	return &Session[C]{Value: v, closeFn: closeFn}
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

// Session — соединение, выданное для одной операции: из пула (через Entry)
// либо напрямую (dial вне пула — тестовый путь адаптеров). Единая замена
// прежних обёрток pooledConn/pooledClient адаптеров ftp и sftp.
type Session[C any] struct {
	// Value — само соединение (FTP-conn, SFTP-client и т.п.).
	Value C
	entry *Entry[C]
	// closeFn закрывает прямое (внепуловое) соединение; для пуловых
	// соединений закрытие выполняет Entry.Discard.
	closeFn func(C) error
	done    bool
}

// Discard закрывает соединение и освобождает слот пула. Идемпотентен и
// безопасен для nil-Session: повторный вызов и вызов после передачи
// владения — no-op.
func (s *Session[C]) Discard() {
	if s == nil || s.done {
		return
	}
	s.done = true
	if s.entry != nil {
		s.entry.Discard()
		return
	}
	if s.closeFn != nil {
		_ = s.closeFn(s.Value)
	}
}
