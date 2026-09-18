// Тесты конвейера покадровой подготовки AVIF-кадров (runAvifFramePipeline,
// см. avifpipeline.go): строго упорядоченное потребление (AddFrame), лимит
// кадров «в полёте» (ограничение памяти), проброс ошибок (детерминизм),
// освобождение необработанных кадров (discard), последовательный путь для
// n<=1/workers<=1, отмена ctx.
//
// Файл без build-tag: runAvifFramePipeline не зависит от govips, поэтому
// тестируется в любой сборке (реальный libvips на Windows недоступен —
// прогон через Docker CI).
package libvips

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// avifPipelineHarness — фейки колбэков конвейера со счётчиками для проверок
// порядка, параллелизма и освобождения кадров.
type avifPipelineHarness struct {
	// makeFrame: возвращает кадр = индекс.
	makeFrame func(i int) (int, error)
	// drain: имитация RawRGBAPixels (произвольная задержка), возвращает
	// результат = индекс.
	drain func(f, i int) (int, error)
	// consume: имитация enc.AddFrame (произвольная задержка).
	consume func(r, i int) error
	// discard: освобождение необработанного кадра.
	discard func(f int)

	// Наблюдения (защищены mu или атомарные).
	mu           sync.Mutex
	consumeOrder []int
	discarded    []int
	curDrain     atomic.Int64
	maxDrain     atomic.Int64
	curConsume   atomic.Int64
	maxConsume   atomic.Int64
}

func newAvifPipelineHarness() *avifPipelineHarness {
	h := &avifPipelineHarness{}
	h.makeFrame = func(i int) (int, error) { return i, nil }
	h.drain = func(f, i int) (int, error) { return i, nil }
	h.consume = func(r, i int) error { return nil }
	h.discard = func(f int) {}
	return h
}

// observeDrain фиксирует текущий/максимальный параллелизм drain-фазы.
func (h *avifPipelineHarness) observeDrain() {
	cur := h.curDrain.Add(1)
	for {
		max := h.maxDrain.Load()
		if cur <= max || h.maxDrain.CompareAndSwap(max, cur) {
			break
		}
	}
}

// observeConsume фиксирует порядок и параллелизм consume-фазы.
func (h *avifPipelineHarness) observeConsume(i int) {
	h.mu.Lock()
	h.consumeOrder = append(h.consumeOrder, i)
	h.mu.Unlock()
	cur := h.curConsume.Add(1)
	for {
		max := h.maxConsume.Load()
		if cur <= max || h.maxConsume.CompareAndSwap(max, cur) {
			break
		}
	}
}

// TestAvifPipelineOrder — consume вызывается строго в порядке кадров
// (имитация требования AddFrame: кадры обязаны идти в порядке
// последовательности), независимо от случайных задержек drain.
func TestAvifPipelineOrder(t *testing.T) {
	const n = 32
	h := newAvifPipelineHarness()
	h.drain = func(f, i int) (int, error) {
		h.observeDrain()
		// Случайная задержка: более медленные ранние кадры не должны
		// ломать порядок потребления.
		time.Sleep(time.Duration((i*37)%5) * time.Millisecond)
		return i, nil
	}
	h.consume = func(r, i int) error {
		if r != i {
			t.Errorf("consume: result %d != index %d", r, i)
		}
		h.observeConsume(i)
		return nil
	}

	if err := runAvifFramePipeline[int, int](nil, n, 4,
		h.makeFrame, h.drain, h.consume, h.discard); err != nil {
		t.Fatalf("runAvifFramePipeline: %v", err)
	}

	if len(h.consumeOrder) != n {
		t.Fatalf("consume called %d times, want %d", len(h.consumeOrder), n)
	}
	for i, got := range h.consumeOrder {
		if got != i {
			t.Fatalf("consume order broken: index %d got frame %d", i, got)
		}
	}
}

// TestAvifPipelineInFlightLimit — «в полёте» не более workers кадров:
// сумма кадров в drain + в очереди результатов + в consume не превышает
// workers (ограничение памяти). Проверяется через слоты: consume блокируется,
// пока drain не заполнит окно; если бы конвейер буферизовал все кадры,
// drain прошёл бы все n кадров до первого consume.
func TestAvifPipelineInFlightLimit(t *testing.T) {
	const n = 24
	const workers = 3
	h := newAvifPipelineHarness()

	// inFlight — кадры, созданные, но ещё не потреблённые (слот удерживается
	// от makeFrame до конца consume).
	var inFlight, maxInFlight atomic.Int64
	enteredDrain := make(chan struct{}, n)
	releaseDrain := make(chan struct{})

	h.drain = func(f, i int) (int, error) {
		cur := inFlight.Add(1)
		for {
			max := maxInFlight.Load()
			if cur <= max || maxInFlight.CompareAndSwap(max, cur) {
				break
			}
		}
		enteredDrain <- struct{}{}
		<-releaseDrain // держим кадр в drain до сигнала
		inFlight.Add(-1)
		return i, nil
	}
	h.consume = func(r, i int) error {
		cur := inFlight.Add(1)
		for {
			max := maxInFlight.Load()
			if cur <= max || maxInFlight.CompareAndSwap(max, cur) {
				break
			}
		}
		inFlight.Add(-1)
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- runAvifFramePipeline[int, int](nil, n, workers,
			h.makeFrame, h.drain, h.consume, h.discard)
	}()

	// Ждём, пока окно заполнится (workers кадров войдут в drain).
	for i := 0; i < workers; i++ {
		select {
		case <-enteredDrain:
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for drain %d", i)
		}
	}
	// Даём конвейеру шанс нарушить лимит (буферизовать больше кадров):
	// если бы лимит не работал, dispatcher создал бы все n кадров.
	time.Sleep(100 * time.Millisecond)
	if got := maxInFlight.Load(); got > workers {
		t.Fatalf("in-flight frames %d > workers %d", got, workers)
	}
	close(releaseDrain)
	if err := <-done; err != nil {
		t.Fatalf("runAvifFramePipeline: %v", err)
	}
	if got := maxInFlight.Load(); got > workers {
		t.Fatalf("in-flight frames %d > workers %d (final)", got, workers)
	}
}

// TestAvifPipelineDrainParallel — drain выполняется параллельно (при
// блокирующем consume несколько drain'ов идут одновременно), параллелизм
// drain ограничен workers.
func TestAvifPipelineDrainParallel(t *testing.T) {
	const n = 12
	const workers = 4
	h := newAvifPipelineHarness()

	block := make(chan struct{})
	h.drain = func(f, i int) (int, error) {
		h.observeDrain()
		defer h.curDrain.Add(-1) // слот освобождается после выхода
		<-block                  // держим все drain'ы одновременно
		return i, nil
	}
	h.consume = func(r, i int) error {
		h.observeConsume(i)
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- runAvifFramePipeline[int, int](nil, n, workers,
			h.makeFrame, h.drain, h.consume, h.discard)
	}()

	// Ждём, пока все workers воркеров войдут в drain (consume ещё не
	// начинался — первый результат в очереди, но consume блокирует
	// продвижение только после получения; drain'ы при этом параллельны).
	deadline := time.After(5 * time.Second)
	for h.maxDrain.Load() < workers {
		select {
		case <-deadline:
			t.Fatalf("drain parallelism %d < workers %d", h.maxDrain.Load(), workers)
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("runAvifFramePipeline: %v", err)
	}
	if h.maxDrain.Load() > workers {
		t.Fatalf("drain parallelism %d > workers %d", h.maxDrain.Load(), workers)
	}
}

// TestAvifPipelineConsumeSingle — consume выполняется из одной горутины
// (требование потокобезопасности libheif AddFrame).
func TestAvifPipelineConsumeSingle(t *testing.T) {
	const n = 32
	h := newAvifPipelineHarness()
	h.drain = func(f, i int) (int, error) {
		h.observeDrain()
		defer h.curDrain.Add(-1)
		time.Sleep(time.Millisecond)
		return i, nil
	}
	h.consume = func(r, i int) error {
		h.observeConsume(i)
		defer h.curConsume.Add(-1)
		time.Sleep(time.Millisecond)
		return nil
	}

	if err := runAvifFramePipeline[int, int](nil, n, 4,
		h.makeFrame, h.drain, h.consume, h.discard); err != nil {
		t.Fatalf("runAvifFramePipeline: %v", err)
	}
	if h.maxConsume.Load() != 1 {
		t.Fatalf("consume parallelism %d, want 1 (AddFrame не потокобезопасен)", h.maxConsume.Load())
	}
}

// TestAvifPipelineMakeFrameError — ошибка создания кадра прекращает запуск
// новых кадров, необработанные кадры не создаются, ошибка возвращается.
func TestAvifPipelineMakeFrameError(t *testing.T) {
	const n = 16
	wantErr := errors.New("copy frame 3")
	h := newAvifPipelineHarness()
	h.makeFrame = func(i int) (int, error) {
		if i == 3 {
			return 0, wantErr
		}
		return i, nil
	}
	var consumed atomic.Int64
	h.consume = func(r, i int) error {
		consumed.Add(1)
		return nil
	}

	err := runAvifFramePipeline[int, int](nil, n, 4,
		h.makeFrame, h.drain, h.consume, h.discard)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if got := consumed.Load(); got > 3 {
		t.Fatalf("consumed %d frames after makeFrame error at index 3", got)
	}
}

// TestAvifPipelineDrainError — ошибка drain прекращает запуск новых кадров,
// ошибка возвращается; кадры, не дошедшие до drain, освобождаются через
// discard (утечки cgo-ресурсов исключены).
func TestAvifPipelineDrainError(t *testing.T) {
	const n = 16
	wantErr := errors.New("pixels frame 2")
	h := newAvifPipelineHarness()
	h.drain = func(f, i int) (int, error) {
		if i == 2 {
			return 0, wantErr
		}
		time.Sleep(time.Millisecond)
		return i, nil
	}
	var consumed atomic.Int64
	h.consume = func(r, i int) error {
		consumed.Add(1)
		return nil
	}

	err := runAvifFramePipeline[int, int](nil, n, 4,
		h.makeFrame, h.drain, h.consume, h.discard)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if got := consumed.Load(); got > 3 {
		t.Fatalf("consumed %d frames after drain error at index 2", got)
	}
}

// TestAvifPipelineConsumeError — ошибка consume (AddFrame) прекращает
// конвейер, ошибка возвращается, необработанные кадры освобождаются.
func TestAvifPipelineConsumeError(t *testing.T) {
	const n = 16
	wantErr := errors.New("encode frame 1")
	h := newAvifPipelineHarness()
	h.drain = func(f, i int) (int, error) {
		time.Sleep(time.Millisecond)
		return i, nil
	}
	h.consume = func(r, i int) error {
		if i == 1 {
			return wantErr
		}
		return nil
	}

	err := runAvifFramePipeline[int, int](nil, n, 4,
		h.makeFrame, h.drain, h.consume, h.discard)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// TestAvifPipelineDiscardOnStop — при остановке конвейера (ошибка consume)
// кадры, не дошедшие до drain, освобождаются через discard; суммарно каждый
// кадр либо потреблён, либо освобождён ровно один раз (нет утечек).
func TestAvifPipelineDiscardOnStop(t *testing.T) {
	const n = 32
	h := newAvifPipelineHarness()
	h.drain = func(f, i int) (int, error) {
		time.Sleep(time.Millisecond)
		return i, nil
	}
	h.consume = func(r, i int) error {
		if i == 0 {
			return errors.New("encode frame 1")
		}
		return nil
	}
	var mu sync.Mutex
	touched := make(map[int]int) // индекс → число событий (drain или discard)
	h.discard = func(f int) {
		mu.Lock()
		touched[f]++
		mu.Unlock()
	}

	if err := runAvifFramePipeline[int, int](nil, n, 4,
		h.makeFrame, h.drain, h.consume, h.discard); err == nil {
		t.Fatal("expected consume error")
	}

	mu.Lock()
	defer mu.Unlock()
	for f, cnt := range touched {
		if cnt != 1 {
			t.Fatalf("frame %d discarded %d times, want 1", f, cnt)
		}
	}
}

// TestAvifPipelineSequentialPath — последовательный путь (n<=1 или
// workers<=1): без горутин, порядок и результаты идентичны прежнему циклу.
func TestAvifPipelineSequentialPath(t *testing.T) {
	// n = 1 при workers = 4.
	h := newAvifPipelineHarness()
	var order []int
	h.consume = func(r, i int) error {
		order = append(order, i)
		return nil
	}
	if err := runAvifFramePipeline[int, int](nil, 1, 4,
		h.makeFrame, h.drain, h.consume, h.discard); err != nil {
		t.Fatalf("n=1: %v", err)
	}
	if len(order) != 1 || order[0] != 0 {
		t.Fatalf("n=1: order = %v, want [0]", order)
	}

	// workers = 1 при n > 1: строго последовательное выполнение.
	h2 := newAvifPipelineHarness()
	var seq []int
	h2.drain = func(f, i int) (int, error) {
		seq = append(seq, i)
		return i, nil
	}
	h2.consume = func(r, i int) error {
		seq = append(seq, i)
		return nil
	}
	if err := runAvifFramePipeline[int, int](nil, 8, 1,
		h2.makeFrame, h2.drain, h2.consume, h2.discard); err != nil {
		t.Fatalf("workers=1: %v", err)
	}
	want := []int{0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7}
	if len(seq) != len(want) {
		t.Fatalf("workers=1: seq len %d, want %d", len(seq), len(want))
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("workers=1: seq[%d] = %d, want %d (полный seq: %v)", i, seq[i], want[i], seq)
		}
	}
}

// TestAvifPipelineSequentialPathError — на последовательном пути ошибки
// makeFrame/drain/consume возвращаются немедленно (как в прежнем цикле).
func TestAvifPipelineSequentialPathError(t *testing.T) {
	mkErr := errors.New("make")
	drErr := errors.New("drain")
	cnErr := errors.New("consume")

	h := newAvifPipelineHarness()
	h.makeFrame = func(i int) (int, error) { return 0, mkErr }
	if err := runAvifFramePipeline[int, int](nil, 1, 4,
		h.makeFrame, h.drain, h.consume, h.discard); !errors.Is(err, mkErr) {
		t.Fatalf("makeFrame err = %v, want %v", err, mkErr)
	}

	h2 := newAvifPipelineHarness()
	h2.drain = func(f, i int) (int, error) { return 0, drErr }
	if err := runAvifFramePipeline[int, int](nil, 1, 4,
		h2.makeFrame, h2.drain, h2.consume, h2.discard); !errors.Is(err, drErr) {
		t.Fatalf("drain err = %v, want %v", err, drErr)
	}

	h3 := newAvifPipelineHarness()
	h3.consume = func(r, i int) error { return cnErr }
	if err := runAvifFramePipeline[int, int](nil, 1, 4,
		h3.makeFrame, h3.drain, h3.consume, h3.discard); !errors.Is(err, cnErr) {
		t.Fatalf("consume err = %v, want %v", err, cnErr)
	}
}

// TestAvifPipelineCtxCancel — отмена внешнего ctx прекращает конвейер:
// возвращается ошибка ctx, новые кадры не запускаются.
func TestAvifPipelineCtxCancel(t *testing.T) {
	const n = 32
	h := newAvifPipelineHarness()
	block := make(chan struct{})
	h.drain = func(f, i int) (int, error) {
		h.observeDrain()
		defer h.curDrain.Add(-1)
		<-block
		return i, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runAvifFramePipeline[int, int](ctx, n, 2,
			h.makeFrame, h.drain, h.consume, h.discard)
	}()

	// Ждём, пока воркеры войдут в drain, затем отменяем.
	deadline := time.After(5 * time.Second)
	for h.curDrain.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for drains")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	close(block) // разблокируем запущенные drain (они не прерываются)

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not finish after ctx cancel")
	}
}

// TestAvifPipelineWorkersClampedToN — workers > n: конвейер работает
// корректно (воркеров фактически min(workers, n)), порядок сохранён.
func TestAvifPipelineWorkersClampedToN(t *testing.T) {
	const n = 3
	h := newAvifPipelineHarness()
	h.drain = func(f, i int) (int, error) {
		h.observeDrain()
		time.Sleep(time.Millisecond)
		return i, nil
	}
	var order []int
	h.consume = func(r, i int) error {
		order = append(order, i)
		return nil
	}

	if err := runAvifFramePipeline[int, int](nil, n, 8,
		h.makeFrame, h.drain, h.consume, h.discard); err != nil {
		t.Fatalf("runAvifFramePipeline: %v", err)
	}
	for i, got := range order {
		if got != i {
			t.Fatalf("order broken: index %d got %d", i, got)
		}
	}
}

// TestAvifPipelineSlowConsumer — медленный consume (AddFrame) не ломает
// порядок и не приводит к росту памяти сверх окна: consume с задержкой,
// порядок строго по индексам.
func TestAvifPipelineSlowConsumer(t *testing.T) {
	const n = 24
	h := newAvifPipelineHarness()
	h.drain = func(f, i int) (int, error) {
		return i, nil
	}
	h.consume = func(r, i int) error {
		if r != i {
			t.Errorf("consume: result %d != index %d", r, i)
		}
		time.Sleep(2 * time.Millisecond) // имитация AV1-кодирования
		return nil
	}

	if err := runAvifFramePipeline[int, int](nil, n, 4,
		h.makeFrame, h.drain, h.consume, h.discard); err != nil {
		t.Fatalf("runAvifFramePipeline: %v", err)
	}
}
