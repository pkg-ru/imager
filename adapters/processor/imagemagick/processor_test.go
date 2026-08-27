package imagemagick

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/ports/processor"
)

// fakeRunner — тестовый runner, не требующий установленного ImageMagick.
type fakeRunner struct {
	mu        sync.Mutex
	ctx       context.Context
	args      []string
	env       []string
	in        io.Reader
	out       io.Writer
	err       io.Writer
	runFn     func(ctx context.Context, in io.Reader, out, err io.Writer) error
	started   chan struct{}
	startOnce sync.Once
}

func (f *fakeRunner) run(ctx context.Context, binary string, args []string, env []string, in io.Reader, out io.Writer, err io.Writer) error {
	f.mu.Lock()
	f.ctx = ctx
	f.args = args
	f.env = env
	f.in = in
	f.out = out
	f.err = err
	f.mu.Unlock()
	f.startOnce.Do(func() {
		if f.started != nil {
			close(f.started)
		}
	})
	if f.runFn != nil {
		return f.runFn(ctx, in, out, err)
	}
	return nil
}

// fakeSource — тестовый object.Artifact.
type fakeSource struct {
	data []byte
}

func (f *fakeSource) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}
func (f *fakeSource) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (f *fakeSource) Close() error                                 { return nil }
func (f *fakeSource) Metadata() object.ObjectMetadata              { return object.ObjectMetadata{} }

func testPlan(t *testing.T) *processing.ProcessingPlan {
	t.Helper()
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

func TestProcess_StreamsOutput(t *testing.T) {
	fr := &fakeRunner{
		runFn: func(ctx context.Context, in io.Reader, out, err io.Writer) error {
			_, werr := out.Write([]byte("image-data"))
			return werr
		},
	}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	res, err := p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte("src")},
		Plan:   testPlan(t),
	}, &buf)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Size != int64(len("image-data")) {
		t.Errorf("size = %d, want %d", res.Size, len("image-data"))
	}
	if buf.String() != "image-data" {
		t.Errorf("output = %q", buf.String())
	}
	// Проверяем, что argv передан в runner без shell.
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.args) == 0 || fr.args[0] != "-quiet" {
		t.Errorf("unexpected args: %v", fr.args)
	}
}

func TestProcess_OutputLimit(t *testing.T) {
	fr := &fakeRunner{
		runFn: func(ctx context.Context, in io.Reader, out, err io.Writer) error {
			// Пишем больше лимита.
			_, werr := out.Write([]byte(strings.Repeat("x", 100)))
			return werr
		},
	}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		Limits:             Limits{OutputBytes: 10},
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte("src")},
		Plan:   testPlan(t),
	}, &buf)
	if err == nil {
		t.Fatal("expected output limit error")
	}
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected LimitError, got %T", err)
	}
	if le.Kind != LimitOutput {
		t.Errorf("kind = %q, want output", le.Kind)
	}
}

func TestProcess_Cancellation(t *testing.T) {
	fr := &fakeRunner{
		started: make(chan struct{}),
		runFn: func(ctx context.Context, in io.Reader, out, err io.Writer) error {
			// Блокируемся до отмены контекста.
			<-ctx.Done()
			return ctx.Err()
		},
	}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		_, err := p.Process(ctx, processor.Input{
			Source: &fakeSource{data: []byte("src")},
			Plan:   testPlan(t),
		}, &buf)
		errCh <- err
	}()
	// Ждём запуска runner, затем отменяем.
	<-fr.started
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Process did not return after cancellation")
	}
}

func TestProcess_TimeoutLimit(t *testing.T) {
	fr := &fakeRunner{
		started: make(chan struct{}),
		runFn: func(ctx context.Context, in io.Reader, out, err io.Writer) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		Limits:             Limits{Timeout: 50 * time.Millisecond},
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte("src")},
		Plan:   testPlan(t),
	}, &buf)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected LimitError, got %T", err)
	}
	if le.Kind != LimitTime {
		t.Errorf("kind = %q, want time", le.Kind)
	}
}

func TestProcess_ProcessFailure(t *testing.T) {
	fr := &fakeRunner{
		runFn: func(ctx context.Context, in io.Reader, out, err io.Writer) error {
			err.Write([]byte("magick: unable to read image"))
			return errors.New("exit status 1")
		},
	}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte("src")},
		Plan:   testPlan(t),
	}, &buf)
	if err == nil {
		t.Fatal("expected process failure error")
	}
	if !strings.Contains(err.Error(), "unable to read image") {
		t.Errorf("error should include stderr, got %q", err.Error())
	}
}

func TestProcess_StderrTruncation(t *testing.T) {
	fr := &fakeRunner{
		runFn: func(ctx context.Context, in io.Reader, out, err io.Writer) error {
			err.Write([]byte(strings.Repeat("e", 1000)))
			return errors.New("exit status 1")
		},
	}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		StderrLimit:        64,
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte("src")},
		Plan:   testPlan(t),
	}, &buf)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stderr truncated") {
		t.Errorf("error should mention truncation, got %q", err.Error())
	}
}

func TestProcess_PolicyEnv(t *testing.T) {
	fr := &fakeRunner{}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		Policy:             PolicyConfig{Enabled: true, Dir: t.TempDir()},
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	if _, err := p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte("src")},
		Plan:   testPlan(t),
	}, &buf); err != nil {
		t.Fatalf("Process: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	found := false
	for _, e := range fr.env {
		if strings.HasPrefix(e, "MAGICK_CONFIGURE_PATH=") {
			found = true
		}
	}
	if !found {
		t.Error("expected MAGICK_CONFIGURE_PATH in env")
	}
}

// TestProcess_EmptySource проверяет, что пустой источник обрабатывается
// корректно: runner получает пустой вход и может вернуть ошибку (malformed
// media), которую процессор пробрасывает без паники.
func TestProcess_EmptySource(t *testing.T) {
	fr := &fakeRunner{
		runFn: func(ctx context.Context, in io.Reader, out, err io.Writer) error {
			// Читаем весь вход: пустой источник → EOF.
			_, _ = io.ReadAll(in)
			err.Write([]byte("magick: no decode delegate for this image format"))
			return errors.New("exit status 1")
		},
	}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte{}}, // пустой источник
		Plan:   testPlan(t),
	}, &buf)
	if err == nil {
		t.Fatal("expected error for empty/malformed source")
	}
	if !strings.Contains(err.Error(), "no decode delegate") {
		t.Errorf("error should include stderr, got %q", err.Error())
	}
}

func TestProcess_NilPlan(t *testing.T) {
	p, err := New(Options{Capabilities: &Capabilities{}, DetectCapabilities: false, runner: &fakeRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{Source: &fakeSource{}, Plan: nil}, &buf)
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
}

func TestProcess_NilSource(t *testing.T) {
	p, err := New(Options{Capabilities: &Capabilities{}, DetectCapabilities: false, runner: &fakeRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{Source: nil, Plan: testPlan(t)}, &buf)
	if err == nil {
		t.Fatal("expected error for nil source")
	}
}

// TestProcess_ConcurrencyLimit проверяет, что глобальный семафор ограничивает
// число одновременно работающих ImageMagick subprocess.
func TestProcess_ConcurrencyLimit(t *testing.T) {
	const workers = 3
	const maxConcurrent = 2

	fr := &fakeRunner{}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		Limits:             Limits{Concurrency: maxConcurrent},
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var mu sync.Mutex
	active := 0
	maxActive := 0
	release := make(chan struct{})
	fr.runFn = func(ctx context.Context, in io.Reader, out, errW io.Writer) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()

		// Блокируемся до сигнала, чтобы держать слот занятым.
		<-release

		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			_, _ = p.Process(context.Background(), processor.Input{
				Source: &fakeSource{data: []byte("x")},
				Plan:   testPlan(t),
			}, &buf)
		}()
	}

	// Даём конкурентным запускам стартовать и блокироваться.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if maxActive > maxConcurrent {
		t.Errorf("max active processes = %d, want <= %d", maxActive, maxConcurrent)
	}
	if maxActive == 0 {
		t.Error("expected at least one active process")
	}
}

// TestProcess_ConcurrencyCancel проверяет, что ожидание слота семафора
// прерывается отменой контекста.
func TestProcess_ConcurrencyCancel(t *testing.T) {
	fr := &fakeRunner{}
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		Limits:             Limits{Concurrency: 1},
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	release := make(chan struct{})
	fr.runFn = func(ctx context.Context, in io.Reader, out, errW io.Writer) error {
		<-release // первый процесс держит слот
		return nil
	}

	// Занимаем единственный слот.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var buf bytes.Buffer
		_, _ = p.Process(context.Background(), processor.Input{
			Source: &fakeSource{data: []byte("x")},
			Plan:   testPlan(t),
		}, &buf)
	}()
	time.Sleep(50 * time.Millisecond)

	// Второй вызов должен ждать слот — отменяем ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	_, err = p.Process(ctx, processor.Input{
		Source: &fakeSource{data: []byte("x")},
		Plan:   testPlan(t),
	}, &buf)
	if err == nil {
		t.Fatal("expected error for cancelled context while waiting for slot")
	}
	close(release)
	wg.Wait()
}

// TestProcess_ConcurrencyQueueOverflow проверяет, что при переполнении
// bounded очереди ожидания слота возвращается быстрый отказ, а не бесконечное
// ожидание.
func TestProcess_ConcurrencyQueueOverflow(t *testing.T) {
	fr := &fakeRunner{}
	// concurrency=1, maxWait=1: один активный + один ожидающий.
	p, err := New(Options{
		Capabilities:       &Capabilities{},
		DetectCapabilities: false,
		Limits:             Limits{Concurrency: 1},
		runner:             fr,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	release := make(chan struct{})
	fr.runFn = func(ctx context.Context, in io.Reader, out, errW io.Writer) error {
		<-release
		return nil
	}

	// Занимаем единственный слот.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var buf bytes.Buffer
		_, _ = p.Process(context.Background(), processor.Input{
			Source: &fakeSource{data: []byte("x")},
			Plan:   testPlan(t),
		}, &buf)
	}()
	time.Sleep(50 * time.Millisecond)

	// Второй вызов занимает очередь ожидания.
	var wg2 sync.WaitGroup
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		var buf bytes.Buffer
		_, _ = p.Process(context.Background(), processor.Input{
			Source: &fakeSource{data: []byte("x")},
			Plan:   testPlan(t),
		}, &buf)
	}()
	time.Sleep(50 * time.Millisecond)

	// Третий вызов — очередь переполнена → быстрый отказ.
	var buf bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: []byte("x")},
		Plan:   testPlan(t),
	}, &buf)
	if !errors.Is(err, ErrTooManyConcurrency) {
		t.Fatalf("expected ErrTooManyConcurrency, got %v", err)
	}
	close(release)
	wg.Wait()
	wg2.Wait()
}
