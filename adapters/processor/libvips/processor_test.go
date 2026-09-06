package libvips

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// fakeBackend — тестовый движок без cgo: возвращает данные или ошибку.
type fakeBackend struct {
	block     chan struct{}
	err       error
	processed int32
}

func (f *fakeBackend) process(ctx context.Context, data []byte, _ *processing.ProcessingPlan, _ bool, _ []filemeta.PixelBox, _ *gateSlot) (*backendResult, error) {
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
	return &backendResult{data: bytes.ToUpper(data)}, nil
}

func (f *fakeBackend) prepareRGB(_ context.Context, _ []byte) (*processor.RGBFrame, error) {
	return nil, nil // не поддерживается тестовым движком
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

func (n *notCompiledBackend) process(_ context.Context, _ []byte, _ *processing.ProcessingPlan, _ bool, _ []filemeta.PixelBox, _ *gateSlot) (*backendResult, error) {
	return nil, ErrNotCompiled
}

func (n *notCompiledBackend) prepareRGB(_ context.Context, _ []byte) (*processor.RGBFrame, error) {
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

// TestPrepareRGBRewindsSource — регрессия "первый запрос на новый ассет":
// PrepareRGB читает источник в пределах лимита и НЕ должен оставлять reader
// на EOF, иначе последующая Process в том же запросе получает пустой буфер
// ("libvips: load: unsupported image format"). После вызова позиция reader
// должна быть восстановлена в 0.
func TestPrepareRGBRewindsSource(t *testing.T) {
	bk := &fakeBackend{}
	restore := overrideBackend(bk)
	defer restore()

	p, err := New(Options{Limits: Limits{Concurrency: 2}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	src := strings.NewReader("hello world")
	// Стартовая позиция не обязана быть 0: источник перематываемый по
	// контракту, Reader может быть уже частично прочитан.
	if _, err := src.Seek(3, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	if _, err := p.PrepareRGB(context.Background(), src); err != nil {
		t.Fatalf("PrepareRGB: %v", err)
	}
	pos, err := src.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek current: %v", err)
	}
	if pos != 0 {
		t.Fatalf("source position after PrepareRGB = %d, want 0", pos)
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
