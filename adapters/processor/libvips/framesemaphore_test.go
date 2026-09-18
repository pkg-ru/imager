// Тесты кадрового семафора и обобщённого worker pool покадровой обработки
// (runFramesParallel): порядок кадров, ограничение параллелизма семафором,
// проброс ошибки, последовательный путь для n<=1, конфиг-валидация
// FrameSemaphoreOpts.
//
// Файл без build-tag: runFramesParallel и FrameSemaphoreOpts не зависят от
// govips (см. framesemaphore.go), поэтому тестируются в любой сборке.
package libvips

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFrameSemaphoreOptsValidate — fail-fast валидация настроек.
func TestFrameSemaphoreOptsValidate(t *testing.T) {
	if err := (FrameSemaphoreOpts{Workers: -1}).Validate(); err == nil {
		t.Fatal("negative workers must fail validation")
	}
	if err := (FrameSemaphoreOpts{}).Validate(); err != nil {
		t.Fatalf("zero opts are valid defaults: %v", err)
	}
	if err := (FrameSemaphoreOpts{Workers: 4}).Validate(); err != nil {
		t.Fatalf("explicit workers are valid: %v", err)
	}
}

// TestFrameSemaphoreOptsNormalized — подстановка дефолтов и клэмпинг:
// 0 → min(GOMAXPROCS, 4); значения выше MaxFrameWorkers клэмпятся до 8.
func TestFrameSemaphoreOptsNormalized(t *testing.T) {
	n := FrameSemaphoreOpts{}.Normalized()
	want := defaultFrameWorkers()
	if want != min(runtime.GOMAXPROCS(0), 4) {
		t.Fatalf("defaultFrameWorkers = %d, want min(GOMAXPROCS,4) = %d", want, min(runtime.GOMAXPROCS(0), 4))
	}
	if n.Workers != want {
		t.Errorf("normalized workers = %d, want %d", n.Workers, want)
	}
	if n.Workers < 1 || n.Workers > MaxFrameWorkers {
		t.Errorf("normalized workers %d out of range [1,%d]", n.Workers, MaxFrameWorkers)
	}
	// Явные значения сохраняются.
	e := FrameSemaphoreOpts{Workers: 3}.Normalized()
	if e.Workers != 3 {
		t.Errorf("explicit workers = %d, want 3", e.Workers)
	}
	// Клэмпинг верхнего лимита.
	c := FrameSemaphoreOpts{Workers: 100}.Normalized()
	if c.Workers != MaxFrameWorkers {
		t.Errorf("clamped workers = %d, want %d", c.Workers, MaxFrameWorkers)
	}
}

// TestRunFramesParallelOrder — порядок кадров сохраняется: колбэк маркирует
// кадры по индексу, результаты собираются в slice по индексу.
func TestRunFramesParallelOrder(t *testing.T) {
	const n = 16
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: 4})
	frames, err := runFramesParallel[int](nil, n, 4, sem,
		func(i int) (int, error) { return i, nil },
		func(f int, i int) error {
			if f != i {
				t.Errorf("frame value %d != index %d", f, i)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("runFramesParallel: %v", err)
	}
	if len(frames) != n {
		t.Fatalf("len(frames) = %d, want %d", len(frames), n)
	}
	for i, f := range frames {
		if f != i {
			t.Fatalf("frames[%d] = %d, want %d (order broken)", i, f, i)
		}
	}
}

// TestRunFramesParallelSemaphoreLimit — семафор ограничивает параллелизм:
// максимальное число одновременных fn <= лимита.
func TestRunFramesParallelSemaphoreLimit(t *testing.T) {
	const (
		n       = 32
		workers = 3
	)
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: workers})
	var cur, maxCur atomic.Int64
	_, err := runFramesParallel[int](nil, n, workers, sem,
		func(i int) (int, error) { return i, nil },
		func(f int, i int) error {
			c := cur.Add(1)
			for {
				m := maxCur.Load()
				if c <= m || maxCur.CompareAndSwap(m, c) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			cur.Add(-1)
			return nil
		})
	if err != nil {
		t.Fatalf("runFramesParallel: %v", err)
	}
	if got := maxCur.Load(); got > workers {
		t.Errorf("max concurrent fn = %d, want <= %d", got, workers)
	}
	if got := maxCur.Load(); got < 2 {
		t.Errorf("max concurrent fn = %d, want >= 2 (no parallelism at all)", got)
	}
}

// TestRunFramesParallelError — ошибка fn на одном кадре корректно
// пробрасывается; запуск новых кадров прекращается (не все кадры обработаны).
func TestRunFramesParallelError(t *testing.T) {
	const (
		n       = 64
		workers = 2
	)
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: workers})
	wantErr := errors.New("boom frame 5")
	var processed atomic.Int64
	_, err := runFramesParallel[int](nil, n, workers, sem,
		func(i int) (int, error) { return i, nil },
		func(f int, i int) error {
			processed.Add(1)
			if i == 5 {
				return wantErr
			}
			return nil
		})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	// С 2 воркерами и ошибкой на кадре 5 обработка прекращается рано:
	// все 64 кадра обработаться не могли.
	if p := processed.Load(); p >= n {
		t.Errorf("processed = %d, want < %d (no early stop)", p, n)
	}
}

// TestRunFramesParallelSequentialPath — последовательный путь для n<=1
// и workers<=1: без горутин, порядок и результат корректны.
func TestRunFramesParallelSequentialPath(t *testing.T) {
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: 1})
	// n = 1.
	frames, err := runFramesParallel[int](nil, 1, 4, sem,
		func(i int) (int, error) { return i * 10, nil },
		func(f int, i int) error { return nil })
	if err != nil {
		t.Fatalf("n=1: %v", err)
	}
	if len(frames) != 1 || frames[0] != 0 {
		t.Fatalf("n=1: frames = %v, want [0]", frames)
	}
	// workers = 1: последовательный путь даже при n > 1.
	var order []int
	frames, err = runFramesParallel[int](nil, 8, 1, sem,
		func(i int) (int, error) { return i, nil },
		func(f int, i int) error {
			order = append(order, i)
			return nil
		})
	if err != nil {
		t.Fatalf("workers=1: %v", err)
	}
	if len(frames) != 8 {
		t.Fatalf("workers=1: len(frames) = %d, want 8", len(frames))
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("workers=1: order broken: %v", order)
		}
	}
}

// TestRunFramesParallelMakeFrameError — ошибка makeFrame пробрасывается,
// частичный slice возвращается владельцу для закрытия ресурсов.
func TestRunFramesParallelMakeFrameError(t *testing.T) {
	const n = 8
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: 4})
	wantErr := errors.New("copy frame 3")
	frames, err := runFramesParallel[int](nil, n, 4, sem,
		func(i int) (int, error) {
			if i == 3 {
				return 0, wantErr
			}
			return i, nil
		},
		func(f int, i int) error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	// Частичный slice: кадры 0..2 созданы, владелец закроет их.
	if len(frames) != 3 {
		t.Errorf("partial frames = %d, want 3", len(frames))
	}
}

// TestRunFramesParallelCtxCancel — отмена ctx прекращает запуск новых fn.
func TestRunFramesParallelCtxCancel(t *testing.T) {
	const n = 64
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: 2})
	ctx, cancel := context.WithCancel(context.Background())
	var processed atomic.Int64
	_, _ = runFramesParallel(ctx, n, 2, sem,
		func(i int) (int, error) { return i, nil },
		func(f int, i int) error {
			processed.Add(1)
			if i == 2 {
				cancel()
			}
			time.Sleep(time.Millisecond)
			return nil
		})
	// После отмены новые кадры не запускаются: обработано меньше n.
	if p := processed.Load(); p >= n {
		t.Errorf("processed = %d, want < %d (ctx cancel ignored)", p, n)
	}
}

// TestNewFrameSemaphore — семафор создаётся с нормализованным лимитом.
func TestNewFrameSemaphore(t *testing.T) {
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: 2})
	// Два слота захватываются мгновенно, третий — нет (ctx с таймаутом).
	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if err := sem.Acquire(ctx1); err == nil {
		t.Fatal("acquire 3 must block (limit 2)")
	}
	sem.Release()
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

// TestRunFramesParallelConcurrentRuns — несколько параллельных withFrames
// (разные запросы) делят один семафор: суммарный параллелизм не превышает
// лимит. Гарантирует, что семафор общий на backend, а не per-call.
func TestRunFramesParallelConcurrentRuns(t *testing.T) {
	const (
		runs    = 4
		n       = 8
		workers = 2
	)
	sem := newFrameSemaphore(FrameSemaphoreOpts{Workers: workers})
	var cur, maxCur atomic.Int64
	var wg sync.WaitGroup
	wg.Add(runs)
	for r := 0; r < runs; r++ {
		go func() {
			defer wg.Done()
			_, err := runFramesParallel[int](nil, n, workers, sem,
				func(i int) (int, error) { return i, nil },
				func(f int, i int) error {
					c := cur.Add(1)
					for {
						m := maxCur.Load()
						if c <= m || maxCur.CompareAndSwap(m, c) {
							break
						}
					}
					time.Sleep(time.Millisecond)
					cur.Add(-1)
					return nil
				})
			if err != nil {
				t.Errorf("run %d: %v", r, err)
			}
		}()
	}
	wg.Wait()
	if got := maxCur.Load(); got > workers {
		t.Errorf("max concurrent fn across runs = %d, want <= %d", got, workers)
	}
}

// min для Go < 1.21 (в проекте может отсутствовать builtin).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
