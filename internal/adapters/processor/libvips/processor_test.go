package libvips

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

// fakeBackend — тестовый движок без cgo: возвращает данные или ошибку.
type fakeBackend struct {
	mu        sync.Mutex
	block     chan struct{}
	err       error
	processed int32
}

func (f *fakeBackend) process(ctx context.Context, data []byte, _ *processing.ProcessingPlan) ([]byte, error) {
	atomic.AddInt32(&f.processed, 1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return bytes.ToUpper(data), nil
}

func (f *fakeBackend) close() error { return nil }

// overrideBackend временно подменяет фабрику движков.
func overrideBackend(b backend) func() {
	orig := newBackend
	newBackend = func(Options) (backend, error) { return b, nil }
	return func() { newBackend = orig }
}

func mustPlan(t *testing.T, src, out processing.Format) *processing.ProcessingPlan {
	t.Helper()
	p, err := processing.NewProcessingPlan(
		processing.OpResize, src, out,
		processing.Size{Width: 100, Height: 100},
		1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	return p
}

func TestProcessCopiesData(t *testing.T) {
	bk := &fakeBackend{}
	restore := overrideBackend(bk)
	defer restore()

	p, err := New(Options{Limits: Limits{Concurrency: 4}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	var out bytes.Buffer
	res, err := p.Process(context.Background(), processor.Input{
		Source: strings.NewReader("hello"),
		Plan:   mustPlan(t, processing.FormatPNG, processing.FormatPNG),
	}, &out)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got := out.String(); got != "HELLO" {
		t.Errorf("output = %q, want %q", got, "HELLO")
	}
	if res.Size != int64(len("HELLO")) {
		t.Errorf("size = %d, want %d", res.Size, len("HELLO"))
	}
}

func TestProcessNilArgs(t *testing.T) {
	bk := &fakeBackend{}
	restore := overrideBackend(bk)
	defer restore()

	p, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	if _, err := p.Process(nil, processor.Input{Source: strings.NewReader("x"), Plan: mustPlan(t, processing.FormatPNG, processing.FormatPNG)}, io.Discard); err == nil {
		t.Error("nil ctx: want error")
	}
	if _, err := p.Process(context.Background(), processor.Input{Source: nil, Plan: mustPlan(t, processing.FormatPNG, processing.FormatPNG)}, io.Discard); err == nil {
		t.Error("nil source: want error")
	}
	if _, err := p.Process(context.Background(), processor.Input{Source: strings.NewReader("x"), Plan: nil}, io.Discard); err == nil {
		t.Error("nil plan: want error")
	}
}

// notCompiledBackend — тестовая заглушка, всегда возвращающая ErrNotCompiled.
// Не зависит от build tags (в отличие от stubBackend из process_stub.go).
type notCompiledBackend struct{}

func (n *notCompiledBackend) process(_ context.Context, _ []byte, _ *processing.ProcessingPlan) ([]byte, error) {
	return nil, ErrNotCompiled
}

func (n *notCompiledBackend) close() error { return nil }

func TestProcessNotCompiled(t *testing.T) {
	// Если фабрика не подменена (stub-сборка), процесс возвращает
	// ErrNotCompiled. Тест принудительно подменяет фабрику заглушкой,
	// чтобы он работал и в libvips-сборке.
	restore := overrideBackend(&notCompiledBackend{})
	defer restore()

	p, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	_, err = p.Process(context.Background(), processor.Input{
		Source: strings.NewReader("x"),
		Plan:   mustPlan(t, processing.FormatPNG, processing.FormatPNG),
	}, io.Discard)
	if err == nil || !errors.Is(err, ErrNotCompiled) {
		t.Fatalf("err = %v, want ErrNotCompiled", err)
	}
}

func TestProcessOutputLimit(t *testing.T) {
	bk := &fakeBackend{}
	restore := overrideBackend(bk)
	defer restore()

	p, err := New(Options{Limits: Limits{Concurrency: 2, OutputBytes: 4}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	var out bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: strings.NewReader("hello world"), // -> "HELLO WORLD" (11 байт)
		Plan:   mustPlan(t, processing.FormatPNG, processing.FormatPNG),
	}, &out)
	if err == nil {
		t.Fatal("Process: want LimitError")
	}
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != LimitOutput {
		t.Fatalf("err = %v, want LimitError{Kind: LimitOutput}", err)
	}
}

func TestProcessTimeout(t *testing.T) {
	bk := &fakeBackend{block: make(chan struct{})}
	restore := overrideBackend(bk)
	defer restore()

	p, err := New(Options{Limits: Limits{Concurrency: 2, Timeout: 20 * time.Millisecond}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	_, err = p.Process(context.Background(), processor.Input{
		Source: strings.NewReader("x"),
		Plan:   mustPlan(t, processing.FormatPNG, processing.FormatPNG),
	}, io.Discard)
	if err == nil {
		t.Fatal("Process: want timeout error")
	}
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != LimitTime {
		t.Fatalf("err = %v, want LimitError{Kind: LimitTime}", err)
	}
	close(bk.block)
}

func TestProcessContextCancel(t *testing.T) {
	bk := &fakeBackend{block: make(chan struct{})}
	restore := overrideBackend(bk)
	defer restore()

	p, err := New(Options{Limits: Limits{Concurrency: 2}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Process(ctx, processor.Input{
		Source: strings.NewReader("x"),
		Plan:   mustPlan(t, processing.FormatPNG, processing.FormatPNG),
	}, io.Discard)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(bk.block)
}

// --- semaphore ---

func TestSemaphoreAllowsConcurrent(t *testing.T) {
	s := newSemaphore(2, 2)
	ctx := context.Background()
	if err := s.acquire(ctx); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if err := s.acquire(ctx); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	s.release()
	s.release()
}

func TestSemaphoreTooManyWaiting(t *testing.T) {
	// maxWait=1: слот занят + один ожидающий → третий запрос получает
	// быстрый отказ ErrTooManyConcurrency вместо блокировки.
	s := newSemaphore(1, 1)
	if err := s.acquire(context.Background()); err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	queued := make(chan struct{})
	go func() {
		defer close(queued)
		_ = s.acquire(context.Background())
	}()
	// Детерминированно ждём, пока второй запрос войдёт в очередь.
	waitForWaiting(t, s, 1)

	// Третий запрос: waiting >= maxWait → ErrTooManyConcurrency.
	if err := s.acquire(context.Background()); err != ErrTooManyConcurrency {
		t.Fatalf("err = %v, want ErrTooManyConcurrency", err)
	}
	s.release()
	select {
	case <-queued:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not unblock after release")
	}
}

// waitForWaiting ждёт, пока s.waiting станет >= want (вход в очередь).
func waitForWaiting(t *testing.T, s *semaphore, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		w := s.waiting
		s.mu.Unlock()
		if w >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("semaphore: waiter did not enter the queue in time")
}

func TestSemaphoreCancel(t *testing.T) {
	s := newSemaphore(1, 10)
	if err := s.acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer s.release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// --- boundedWriter ---

func TestBoundedWriterUnderLimit(t *testing.T) {
	var out bytes.Buffer
	bw := &boundedWriter{w: &out, max: 10}
	n, err := bw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 || out.String() != "hello" {
		t.Fatalf("n=%d out=%q", n, out.String())
	}
	ex, actual := bw.exceededN()
	if ex || actual != 5 {
		t.Fatalf("exceeded=%v actual=%d", ex, actual)
	}
}

func TestBoundedWriterOverLimit(t *testing.T) {
	var out bytes.Buffer
	bw := &boundedWriter{w: &out, max: 5}
	_, err := bw.Write([]byte("hello world"))
	if err == nil {
		t.Fatal("Write: want error")
	}
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != LimitOutput || le.Limit != 5 {
		t.Fatalf("err = %v, want LimitError{Limit:5}", err)
	}
	ex, _ := bw.exceededN()
	if !ex {
		t.Fatal("exceededN: want true")
	}
}

func TestBoundedWriterNoLimit(t *testing.T) {
	var out bytes.Buffer
	bw := &boundedWriter{w: &out, max: 0}
	if _, err := bw.Write([]byte("anything")); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// --- Close идемпотентен ---

func TestCloseIdempotent(t *testing.T) {
	bk := &fakeBackend{}
	restore := overrideBackend(bk)
	defer restore()

	p, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close 2 (idempotent): %v", err)
	}
}
