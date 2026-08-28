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

// Semaphore — bounded очередь слотов конкурентности. Ограничивает и число
// активных слотов (capacity канала), и число ожидающих (очередь ожидания
// ограничена числом слотов). При переполнении очереди ожидания — быстрый
// отказ с ошибкой tooManyErr (например, ErrTooManyConcurrency адаптера),
// а не бесконечное ожидание.
//
// Реализация на канале с capacity = max: канал ограничивает число активных
// слотов, а счётчик waiting под мьютексом — число ожидающих. Отмена ctx
// прерывает ожидание через select. Если maxWait > 0, ожидающий даётся
// максимум maxWait на получение слота; по истечении возвращается tooManyErr
// (сигнал перегрузки). maxWait <= 0 означает ожидание без временного лимита
// (до освобождения слота или отмены ctx).
type Semaphore struct {
	mu         sync.Mutex
	slots      chan struct{}
	waiting    int
	maxWaiting int
	maxWait    time.Duration
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
		slots:      make(chan struct{}, max),
		maxWaiting: max,
		maxWait:    maxWait,
		tooManyErr: tooManyErr,
	}
}

// Acquire занимает слот. Блокируется до освобождения слота, отмены ctx или
// (при maxWait > 0) истечения бюджета ожидания. Возвращает tooManyErr, если
// очередь ожидания переполнена (быстрый отказ) или истёк maxWait.
func (s *Semaphore) Acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.waiting >= s.maxWaiting {
		s.mu.Unlock()
		return s.tooManyErr
	}
	s.waiting++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}()

	var timeout <-chan time.Time
	if s.maxWait > 0 {
		timer := time.NewTimer(s.maxWait)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout:
		return s.tooManyErr
	}
}

// Release освобождает слот. Лишние вызовы (без парного Acquire) игнорируются.
func (s *Semaphore) Release() {
	select {
	case <-s.slots:
	default:
	}
}

// Waiting возвращает текущее число ожидающих Acquire (для тестов и
// диагностики; потокобезопасно).
func (s *Semaphore) Waiting() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting
}
