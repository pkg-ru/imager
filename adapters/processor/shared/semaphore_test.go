package shared

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTooMany = errors.New("too many concurrent requests waiting for a slot")

func TestSemaphoreAllowsConcurrent(t *testing.T) {
	s := NewSemaphore(2, 0, errTooMany)
	ctx := context.Background()
	if err := s.Acquire(ctx); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if err := s.Acquire(ctx); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if got := s.Waiting(); got != 0 {
		t.Fatalf("waiting = %d, want 0", got)
	}
	s.Release()
	s.Release()
}

func TestSemaphoreFastFailOnQueueOverflow(t *testing.T) {
	// max=1: слот занят + один ожидающий → третий запрос получает быстрый
	// отказ errTooMany вместо блокировки.
	s := NewSemaphore(1, 0, errTooMany)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	queued := make(chan struct{})
	go func() {
		defer close(queued)
		_ = s.Acquire(context.Background())
	}()
	waitForWaiting(t, s, 1)

	if err := s.Acquire(context.Background()); !errors.Is(err, errTooMany) {
		t.Fatalf("err = %v, want errTooMany", err)
	}
	s.Release()
	select {
	case <-queued:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not unblock after release")
	}
}

func TestSemaphoreCancel(t *testing.T) {
	s := NewSemaphore(1, 0, errTooMany)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer s.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSemaphoreMaxWaitExpiry(t *testing.T) {
	// maxWait > 0: ожидающий получает tooManyErr по истечении бюджета
	// ожидания, если слот не освободился.
	s := NewSemaphore(1, 30*time.Millisecond, errTooMany)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	defer s.Release()

	start := time.Now()
	if err := s.Acquire(context.Background()); !errors.Is(err, errTooMany) {
		t.Fatalf("err = %v, want errTooMany after maxWait", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("returned too early: %v", elapsed)
	}
}

func TestSemaphoreMaxWaitZeroWaitsIndefinitely(t *testing.T) {
	// maxWait <= 0: ожидание без временного лимита — до освобождения слота.
	s := NewSemaphore(1, 0, errTooMany)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.Release()
	}()
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestSemaphoreReleaseWithoutAcquireIsNoop(t *testing.T) {
	s := NewSemaphore(1, 0, errTooMany)
	s.Release() // лишний вызов не должен паниковать/ломать состояние
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
}

// waitForWaiting ждёт, пока число ожидающих в семафоре станет >= want.
func waitForWaiting(t *testing.T, s *Semaphore, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Waiting() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("semaphore: waiter did not enter the queue in time")
}

// TestSemaphoreFIFOGrantOrder проверяет FIFO-честность: токены выдаются строго
// в порядке постановки в очередь ожидания. Тест полностью детерминированный:
// очередь строится явно (канал i ставится раньше канала i+1) без горутин;
// каждый Release обязан наполнИТЬ ровно один канал — текущий первый в очереди —
// и не затронуть остальные. Так проверяется именно FIFO-семантика передачи
// токенов (планировщик горутин в проверке не участвует).
func TestSemaphoreFIFOGrantOrder(t *testing.T) {
	const n = 5
	s := NewSemaphore(n, 0, errTooMany)
	for i := 0; i < n; i++ {
		if err := s.Acquire(context.Background()); err != nil {
			t.Fatalf("acquire holder %d: %v", i, err)
		}
	}
	// Явно формируем FIFO-очередь в порядке 0..n-1 (эквивалент того, что
	// делает Acquire, но с детерминированным порядком).
	queues := make([]chan struct{}, n)
	for i := range queues {
		queues[i] = make(chan struct{}, 1)
	}
	s.mu.Lock()
	s.waits = append(s.waits, queues...)
	s.mu.Unlock()
	if got := s.Waiting(); got != n {
		t.Fatalf("waiting = %d, want %d", got, n)
	}

	// Каждый Release передаёт токен текущему первому каналу в очереди.
	// После i-го Release заполнены РОВНО каналы 0..i (буфер канала сохраняет
	// переданный токен), остальные — пусты.
	for i := 0; i < n; i++ {
		s.Release()
		for j := 0; j < n; j++ {
			filled := len(queues[j]) > 0
			wantFilled := j <= i
			if filled != wantFilled {
				t.Fatalf("after release %d: channel %d filled = %v, want %v", i, j, filled, wantFilled)
			}
		}
	}
}

// TestSemaphoreAbandonAfterTokenForwarded детерминированно проверяет гонку
// «waiter ушёл по ctx/таймауту, но токен уже передан в его личный канал».
// Токен не должен быть потерян: abandon забирает его из канала и передаёт
// дальше (в пул/следующему), поэтому последующий Acquire успешен.
func TestSemaphoreAbandonAfterTokenForwarded(t *testing.T) {
	s := NewSemaphore(1, 0, errTooMany)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	// Вручную ставим waiter'а в очередь (эквивалент того, что делает Acquire).
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.waits = append(s.waits, ch)
	s.mu.Unlock()
	if got := s.Waiting(); got != 1 {
		t.Fatalf("waiting = %d, want 1", got)
	}

	// Release передаёт токен в канал waiter'а ещё до того, как waiter выбирает
	// в select ветку c ctx/таймаутом (гонка).
	s.Release()
	// Waiter покидает ожидание: abandon находит, что его канал уже вне очереди,
	// забирает переданный токен и возвращает его дальше.
	s.abandon(ch)

	if got := s.Waiting(); got != 0 {
		t.Fatalf("waiting after abandon = %d, want 0", got)
	}
	// Токен не потерян: следующий Acquire получает слот мгновенно.
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after abandon: %v", err)
	}
	s.Release()
}

// TestSemaphoreAbandonRaceNoTokenLoss стресс-тест реальной гонки: waiter
// отменяется по ctx ровно в момент, когда Release передаёт токен. При любом
// исходе (waiter успел взять токен либо ушёл по ctx) токен не теряется —
// после цикла Waiting() == 0 и свежий Acquire мгновенно получает слот.
func TestSemaphoreAbandonRaceNoTokenLoss(t *testing.T) {
	s := NewSemaphore(1, 0, errTooMany)
	for i := 0; i < 1000; i++ {
		if err := s.Acquire(context.Background()); err != nil {
			t.Fatalf("hold slot %d: %v", i, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		res := make(chan error, 1)
		go func() { res <- s.Acquire(ctx) }()
		waitForWaiting(t, s, 1)
		cancel()
		s.Release() // гонка: токен уходит в канал waiter'а или в пул
		switch err := <-res; {
		case err == nil:
			s.Release() // waiter успел взять токен; возвращаем его в систему
		case errors.Is(err, context.Canceled):
			// abandon вернул токен — ничего дополнительно делать не надо
		default:
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
	}
	if got := s.Waiting(); got != 0 {
		t.Fatalf("waiting after race = %d, want 0", got)
	}
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("final acquire: %v", err)
	}
	s.Release()
}
