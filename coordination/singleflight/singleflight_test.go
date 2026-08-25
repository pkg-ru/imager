package singleflight

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg-ru/imager/domain/object"
)

func TestDoDedupSameKey(t *testing.T) {
	g := New(Options{})
	key := object.ObjectKey("k1")

	var calls atomic.Int32
	fn := func() (any, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return "result", nil
	}

	const n = 10
	var wg sync.WaitGroup
	vals := make([]any, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vals[i], errs[i] = g.Do(context.Background(), key, fn)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Do[%d]: %v", i, errs[i])
		}
		if vals[i] != "result" {
			t.Fatalf("Do[%d] = %v, want result", i, vals[i])
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("fn calls = %d, want 1 (dedup)", calls.Load())
	}
}

func TestDoDifferentKeys(t *testing.T) {
	g := New(Options{})
	var calls atomic.Int32
	fn := func() (any, error) {
		calls.Add(1)
		return "ok", nil
	}
	if _, err := g.Do(context.Background(), "a", fn); err != nil {
		t.Fatalf("Do a: %v", err)
	}
	if _, err := g.Do(context.Background(), "b", fn); err != nil {
		t.Fatalf("Do b: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("fn calls = %d, want 2", calls.Load())
	}
}

func TestDoPropagatesError(t *testing.T) {
	g := New(Options{})
	wantErr := errors.New("boom")
	_, err := g.Do(context.Background(), "k", func() (any, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestDoCancelWaiter(t *testing.T) {
	g := New(Options{})
	block := make(chan struct{})
	started := make(chan struct{})

	go func() {
		_, _ = g.Do(context.Background(), "k", func() (any, error) {
			close(started)
			<-block
			return "ok", nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Do(ctx, "k", func() (any, error) { return "x", nil })
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(block)
}

func TestDoKeyTooLong(t *testing.T) {
	g := New(Options{MaxKeyLen: 4})
	long := object.ObjectKey("12345")
	_, err := g.Do(context.Background(), long, func() (any, error) { return nil, nil })
	if !errors.Is(err, ErrKeyTooLong) {
		t.Fatalf("err = %v, want ErrKeyTooLong", err)
	}
}

func TestDoTooManyKeys(t *testing.T) {
	g := New(Options{MaxKeys: 2})
	block := make(chan struct{})
	started := make(chan struct{}, 2)

	fn := func() (any, error) {
		started <- struct{}{}
		<-block
		return "ok", nil
	}

	var wg sync.WaitGroup
	for _, k := range []string{"k1", "k2"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_, _ = g.Do(context.Background(), object.ObjectKey(k), fn)
		}(k)
	}
	<-started
	<-started

	_, err := g.Do(context.Background(), "k3", func() (any, error) { return nil, nil })
	if !errors.Is(err, ErrTooManyKeys) {
		t.Fatalf("err = %v, want ErrTooManyKeys", err)
	}
	close(block)
	wg.Wait()
}

func TestAcquireSerializes(t *testing.T) {
	g := New(Options{})
	var active atomic.Int32
	var maxActive atomic.Int32

	fn := func() {
		unlock, err := g.Acquire(context.Background(), "k")
		if err != nil {
			t.Errorf("Acquire: %v", err)
			return
		}
		defer unlock()
		cur := active.Add(1)
		for {
			m := maxActive.Load()
			if cur <= m || maxActive.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	wg.Wait()
	if maxActive.Load() != 1 {
		t.Fatalf("max concurrent = %d, want 1", maxActive.Load())
	}
}

func TestAcquireCancelWaiter(t *testing.T) {
	g := New(Options{})
	block := make(chan struct{})
	started := make(chan struct{})

	go func() {
		unlock, err := g.Acquire(context.Background(), "k")
		if err != nil {
			t.Errorf("Acquire: %v", err)
			return
		}
		close(started)
		<-block
		unlock()
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Acquire(ctx, "k")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(block)
}

// TestDoPanicDoesNotBlockKey проверяет C4: паника в fn не должна навсегда
// блокировать ключ — последующие вызовы с тем же ключом должны выполняться.
func TestDoPanicDoesNotBlockKey(t *testing.T) {
	g := New(Options{})
	key := object.ObjectKey("k")

	_, err := g.Do(context.Background(), key, func() (any, error) {
		panic("boom")
	})
	if err == nil {
		t.Fatal("expected error from panic")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v, want panic mention", err)
	}

	// Ключ не заблокирован: следующий вызов выполняется.
	var calls atomic.Int32
	v, err := g.Do(context.Background(), key, func() (any, error) {
		calls.Add(1)
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Do after panic: %v", err)
	}
	if v != "ok" {
		t.Fatalf("v = %v, want ok", v)
	}
	if calls.Load() != 1 {
		t.Fatalf("fn calls = %d, want 1", calls.Load())
	}
}

// TestDoPanicWaitersGetError проверяет C4: ожидающие вызовы получают ошибку
// паники, а не зависают навсегда.
func TestDoPanicWaitersGetError(t *testing.T) {
	g := New(Options{})
	key := object.ObjectKey("k")

	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = g.Do(context.Background(), key, func() (any, error) {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started

	// Ожидающий вызов должен получить ошибку после паники.
	errCh := make(chan error, 1)
	go func() {
		_, err := g.Do(context.Background(), key, func() (any, error) { return "x", nil })
		errCh <- err
	}()
	close(release)
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from panic")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not return after panic")
	}
}

// TestDoWaitTimeout проверяет, что ожидание завершения владельца ограничено
// WaitTimeout: если владелец "завис" и не завершился, ожидающий получает
// ErrWaitTimeout, а не блокируется навсегда.
func TestDoWaitTimeout(t *testing.T) {
	g := New(Options{WaitTimeout: 50 * time.Millisecond})
	key := object.ObjectKey("k")

	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = g.Do(context.Background(), key, func() (any, error) {
			close(started)
			<-release // владелец "завис" и не завершается
			return "never", nil
		})
	}()
	<-started

	start := time.Now()
	_, err := g.Do(context.Background(), key, func() (any, error) { return "x", nil })
	elapsed := time.Since(start)

	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want ErrWaitTimeout", err)
	}
	// Ожидание должно завершиться примерно через WaitTimeout, а не ждать
	// освобождения владельца.
	if elapsed < 30*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("wait elapsed = %v, want ~50ms", elapsed)
	}
	close(release)
}
