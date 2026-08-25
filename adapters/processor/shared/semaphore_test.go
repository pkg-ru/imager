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
