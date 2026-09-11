// Package shared содержит общие примитивы ограничения ресурсов для
// процессорных адаптеров (libvips): bounded-семафор конкурентности и
// bounded writer для application-level лимита размера
// выхода. Пакет не зависит от конкретных адаптеров: sentinel-ошибки
// перегрузки передаются извне (каждый адаптер сохраняет свой текст ошибки).
package shared

import (
	"context"
	"sync"
	"time"
)

// Semaphore — bounded FIFO-очередь слотов конкурентности. Ограничивает и число
// активных слотов (пул свободных токенов), и число ожидающих (очередь
// ожидания ограничена числом слотов). При переполнении очереди ожидания —
// быстрый отказ с ошибкой tooManyErr (например, ErrTooManyConcurrency
// адаптера), а не бесконечное ожидание.
//
// FIFO-честность: свободный токен назначается самому старшему ожидающему.
// Ожидающие хранятся как очередь личных каналов под мьютексом (ticket-based);
// освободитель передаёт токен первому элементу очереди (send в его буферизованный
// канал) либо, если очередь пуста, возвращает токен в пул свободных. Быстрый
// путь (свободный токен уже в пуле) захватывает токен без постановки в очередь,
// поэтому при отсутствии конкуренции очередь ожидания пуста.
//
// Отмена ctx прерывает ожидание через select, maxWait > 0 дополнительно
// ограничивает бюджет ожидания (по истечении возвращается tooManyErr — сигнал
// перегрузки). При уходе из ожидания по ctx/таймауту есть гонка с освободителем,
// уже «передавшим» токен в личный канал waiter'а: waiter сам возвращает токен
// следующему ожидающему (или в пул), поэтому токен никогда не теряется.
type Semaphore struct {
	mu         sync.Mutex
	tokens     int             // свободные слоты (инвариант: если > 0, очередь пуста)
	max        int             // максимум слотов (для клэмпинга избыточных Release)
	waits      []chan struct{} // FIFO-очередь ожидающих (личные каналы, buf=1)
	maxWaiting int             // максимум записей в очереди ожидания
	maxWait    time.Duration   // бюджет ожидания; <= 0 — без временного лимита
	tooManyErr error
}

// NewSemaphore создаёт Semaphore с max слотами. Очередь ожидания ограничена
// max записями; при переполнении Acquire немедленно возвращает tooManyErr.
// maxWait > 0 дополнительно ограничивает время ожидания слота (по истечении —
// tooManyErr). tooManyErr используется как есть (без оборачивания), поэтому
// обязан содержать распознаваемый текст (например, "too many concurrent").
func NewSemaphore(max int, maxWait time.Duration, tooManyErr error) *Semaphore {
	if max <= 0 {
		max = 1
	}
	return &Semaphore{
		tokens:     max,
		max:        max,
		maxWaiting: max,
		maxWait:    maxWait,
		tooManyErr: tooManyErr,
	}
}

// Acquire занимает слот. Свободный токен захватывается мгновенно; иначе
// waiter встаёт в FIFO-очередь и ожидает назначения токена. Блокируется до
// освобождения слота, отмены ctx или (при maxWait > 0) истечения бюджета
// ожидания. Возвращает tooManyErr, если очередь ожидания переполнена (быстрый
// отказ) или истёк maxWait.
func (s *Semaphore) Acquire(ctx context.Context) error {
	// Быстрый путь: свободный токен уже есть.
	s.mu.Lock()
	if s.tokens > 0 {
		s.tokens--
		s.mu.Unlock()
		return nil
	}
	if len(s.waits) >= s.maxWaiting {
		s.mu.Unlock()
		return s.tooManyErr
	}
	ch := make(chan struct{}, 1)
	s.waits = append(s.waits, ch)
	s.mu.Unlock()

	var timeout <-chan time.Time
	var timer *time.Timer
	if s.maxWait > 0 {
		timer = time.NewTimer(s.maxWait)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		s.abandon(ch)
		return ctx.Err()
	case <-timeout:
		s.abandon(ch)
		return s.tooManyErr
	}
}

// abandon вызывается waiter'ом, решившим уйти по ctx/таймауту. Под мьютексом:
//   - если канал ещё в очереди — токен ещё не назначался, waiter просто
//     удаляется из очереди;
//   - если канала в очереди уже нет — освободитель успел передать токен в
//     личный канал (гонка в select): waiter забирает токен и передаёт его
//     дальше (следующему по FIFO или в пул), не теряя слот.
//
// Вызов должен происходить с уже снятым мьютексом s.mu.
func (s *Semaphore) abandon(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.waits {
		if w == ch {
			s.waits = append(s.waits[:i], s.waits[i+1:]...)
			return
		}
	}
	// В очереди нас нет — токен был передан в наш личный канал (удаление из
	// очереди и send происходят в одном критическом разделе освободителя,
	// поэтому непустой буфер гарантирован). Забираем и передаём дальше.
	<-ch
	s.forwardLocked()
}

// Release освобождает слот: передаёт его первому ожидающему по FIFO либо,
// если очередь пуста, возвращает в пул свободных. Лишние вызовы (без парного
// Acquire) не ломают состояние: пул не вырастает выше исходного максимума.
func (s *Semaphore) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forwardLocked()
}

// forwardLocked передаёт один свободный токен: первому ожидающему по FIFO
// (send в его буферизованный канал — неблокирующий), либо в пул свободных
// (с клэмпингом по s.max). Должен вызываться при удержанном s.mu.
func (s *Semaphore) forwardLocked() {
	if len(s.waits) > 0 {
		ch := s.waits[0]
		s.waits = s.waits[1:]
		ch <- struct{}{}
		return
	}
	if s.tokens < s.max {
		s.tokens++
	}
}

// Waiting возвращает текущее число ожидающих Acquire (для тестов и
// диагностики; потокобезопасно).
func (s *Semaphore) Waiting() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waits)
}
