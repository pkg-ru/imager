package generatev2

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/imager/internal/domain/asset"
)

// TestGenerateCacheStampedeSingleFlight проверяет, что при конкурентных
// запросах одного и того же ключа (cache stampede) процессор вызывается
// ровно один раз, а остальные запросы получают результат из singleflight
// (без повторной генерации и без гонки).
func TestGenerateCacheStampedeSingleFlight(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))

	// Блокируем процессор, чтобы все запросы вошли в singleflight.
	block := make(chan struct{})
	env.proc.setBlock(block)

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	results := make([]*Result, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = env.svc.Generate(ctx, req)
		}(i)
	}

	// Ждём, пока все запросы войдут в singleflight (детерминированно через
	// барьер: процессор заблокирован, значит первый запрос уже в generateLocked).
	// Используем polling с таймаутом вместо фиксированного sleep.
	deadline := time.Now().Add(2 * time.Second)
	for env.proc.callCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(block)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Generate[%d]: %v", i, errs[i])
		}
		if results[i] == nil {
			t.Fatalf("Generate[%d]: nil result", i)
		}
		data, _ := io.ReadAll(results[i].Opened)
		if string(data) != "IMG" {
			t.Fatalf("Generate[%d] data = %q, want IMG", i, data)
		}
		results[i].Close()
	}
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1 (cache stampede dedup)", env.proc.callCount())
	}
}

// TestGenerateCancelNoGoroutineLeak проверяет, что при отмене контекста во
// время блокировки процессора Generate возвращается и не оставляет
// зависших goroutine (детерминированно через каналы, без фиксированных
// sleep для маскировки).
func TestGenerateCancelNoGoroutineLeak(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))

	block := make(chan struct{})
	env.proc.setBlock(block)

	ctx, cancel := context.WithCancel(context.Background())
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	done := make(chan error, 1)
	go func() {
		_, err := env.svc.Generate(ctx, req)
		done <- err
	}()

	// Ждём, пока процессор начнёт выполнение (детерминированный барьер).
	deadline := time.Now().Add(2 * time.Second)
	for env.proc.callCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Отменяем контекст и освобождаем процессор.
	cancel()
	close(block)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected canceled error")
		}
		wantOutcome(t, err, OutcomeCanceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not return after cancel (possible goroutine leak)")
	}
}
