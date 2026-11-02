package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pkg-ru/imager/composition"
	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/ports/processor"
)

// fakeRGBProcessor — processor.Processor, реализующий RGBPreparer, для
// проверки проброса PrepareRGB через closedProcessor.
type fakeRGBProcessor struct {
	prepCalled bool
	prepErr    error
}

func (f *fakeRGBProcessor) Process(_ context.Context, _ processor.Input, _ io.Writer) (*processor.Result, error) {
	return &processor.Result{}, nil
}

func (f *fakeRGBProcessor) PrepareRGB(_ context.Context, _ io.ReadSeeker) (*processor.RGBFrame, error) {
	f.prepCalled = true
	if f.prepErr != nil {
		return nil, f.prepErr
	}
	return &processor.RGBFrame{Pixels: []byte{1, 2, 3}, Width: 1, Height: 1}, nil
}

var _ processor.Processor = (*fakeRGBProcessor)(nil)
var _ processor.RGBPreparer = (*fakeRGBProcessor)(nil)

// TestClosedProcessorImplementsRGBPreparer — compile-time assertion:
// closedProcessor обязан реализовывать processor.RGBPreparer, иначе
// app-level детекция ensureDetections всегда деградирует к self-detection.
func TestClosedProcessorImplementsRGBPreparer(t *testing.T) {
	var _ processor.RGBPreparer = (*closedProcessor)(nil)
}

// TestClosedProcessorPrepareRGBForwards — PrepareRGB пробрасывает вызов
// во внутренний процессор (type assertion на processor.RGBPreparer).
func TestClosedProcessorPrepareRGBForwards(t *testing.T) {
	inner := &fakeRGBProcessor{}
	c := &closedProcessor{Processor: inner}

	frame, err := c.PrepareRGB(context.Background(), nil)
	if err != nil {
		t.Fatalf("PrepareRGB err = %v, want nil", err)
	}
	if !inner.prepCalled {
		t.Fatal("PrepareRGB was not forwarded to inner processor")
	}
	if frame == nil || frame.Width != 1 || frame.Height != 1 || len(frame.Pixels) != 3 {
		t.Fatalf("frame = %+v, want 1x1 RGB", frame)
	}
}

// TestClosedProcessorPrepareRGBError — ошибка внутреннего процессора
// пробрасывается как есть.
func TestClosedProcessorPrepareRGBError(t *testing.T) {
	want := errors.New("prep failed")
	inner := &fakeRGBProcessor{prepErr: want}
	c := &closedProcessor{Processor: inner}

	_, err := c.PrepareRGB(context.Background(), nil)
	if !errors.Is(err, want) {
		t.Fatalf("PrepareRGB err = %v, want %v", err, want)
	}
}

// TestClosedProcessorPrepareRGBNotSupported — внутренний процессор без
// RGBPreparer → понятная ошибка (деградация к self-detection).
func TestClosedProcessorPrepareRGBNotSupported(t *testing.T) {
	c := &closedProcessor{Processor: plainProcessor{}}

	_, err := c.PrepareRGB(context.Background(), nil)
	if err == nil {
		t.Fatal("PrepareRGB err = nil, want error for processor without RGBPreparer")
	}
}

// plainProcessor — минимальный processor.Processor БЕЗ RGBPreparer.
type plainProcessor struct{}

func (plainProcessor) Process(_ context.Context, _ processor.Input, _ io.Writer) (*processor.Result, error) {
	return &processor.Result{}, nil
}

var _ processor.Processor = plainProcessor{}

// fakeCloser — io.Closer, подсчитывающий вызовы и (опционально) ошибающийся.
type fakeCloser struct {
	called int
	err    error
}

func (f *fakeCloser) Close() error {
	f.called++
	return f.err
}

// TestClosedProcessorCloseAllFirstError — Close закрывает все closer'ы
// по порядку и возвращает ПЕРВУЮ ошибку (не прерывая остальные).
func TestClosedProcessorCloseAllFirstError(t *testing.T) {
	first := errors.New("first")
	c1 := &fakeCloser{err: first}
	c2 := &fakeCloser{err: errors.New("second")}
	c3 := &fakeCloser{}
	c := &closedProcessor{Processor: plainProcessor{}, closers: []io.Closer{c1, c2, c3}}

	for range 2 {
		if err := c.Close(); !errors.Is(err, first) {
			t.Fatalf("Close() err = %v, want first error %v", err, first)
		}
	}
	for _, f := range []*fakeCloser{c1, c2, c3} {
		if f.called != 2 {
			t.Errorf("closer called %d times, want 2", f.called)
		}
	}
}

// TestClosedProcessorCloseNoClosers — Close без closer'ов возвращает nil.
func TestClosedProcessorCloseNoClosers(t *testing.T) {
	c := &closedProcessor{Processor: plainProcessor{}}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
}

// TestLibvipsCaps — покрытие форматов libvips: все 8 форматов присутствуют
// и на входе, и на выходе. APNG-выход разрешён (в стандартной сборке это
// статичный PNG, анимированный — только с libspng); APNG-вход читается как
// анимированный PNG.
func TestLibvipsCaps(t *testing.T) {
	caps := LibvipsCaps()
	if caps.Name != "libvips" {
		t.Errorf("Name = %q, want libvips", caps.Name)
	}
	want := []processing.Format{
		processing.FormatJPEG, processing.FormatPNG, processing.FormatWebP,
		processing.FormatGIF, processing.FormatAVIF, processing.FormatHEIF,
		processing.FormatAPNG, processing.FormatJPEGXL,
	}
	for _, f := range want {
		if !caps.Formats[f] {
			t.Errorf("Formats[%s] = false, want true", f)
		}
		if !caps.SourceFormats[f] {
			t.Errorf("SourceFormats[%s] = false, want true", f)
		}
	}
	if len(caps.Formats) != len(want) || len(caps.SourceFormats) != len(want) {
		t.Errorf("Formats=%d SourceFormats=%d, want %d/%d",
			len(caps.Formats), len(caps.SourceFormats), len(want), len(want))
	}
}

// TestSlogLevel — строковые уровни конфигурации → slog.Level; неизвестные
// значения → info (fail-safe).
func TestSlogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"fatal", slog.LevelInfo},
	}
	for _, tt := range tests {
		if got := SlogLevel(tt.in); got != tt.want {
			t.Errorf("SlogLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// fakeLogger — bootstrap.Logger, подсчитывающий INFO-вызовы.
type fakeLogger struct {
	infos  int
	errors int
}

func (l *fakeLogger) Debugf(string, ...any) {}
func (l *fakeLogger) Infof(string, ...any)  { l.infos++ }
func (l *fakeLogger) Warnf(string, ...any)  {}
func (l *fakeLogger) Errorf(string, ...any) { l.errors++ }

// TestBuildProcessorSmoke — сборка процессора из нулевого конфига:
// возвращается routes + детектор, обёртка закрывается без ошибок.
//
// Без тега libvips движок — заглушка (ErrNotCompiled на Process),
// с тегом govips стартует реальный libvips; в обеих сборках New не падает.
func TestBuildProcessorSmoke(t *testing.T) {
	log := &fakeLogger{}
	build, err := BuildProcessor(log, &composition.RuntimeConfig{})
	if err != nil {
		t.Fatalf("BuildProcessor: %v", err)
	}
	if build.Processor == nil {
		t.Fatal("Processor = nil")
	}
	if build.Detector == nil {
		t.Fatal("Detector = nil")
	}
	closer, ok := build.Processor.(io.Closer)
	if !ok {
		t.Fatalf("Processor %T does not implement io.Closer", build.Processor)
	}
	if err := closer.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
	// Повторное Close — идемпотентно.
	if err := closer.Close(); err != nil {
		t.Errorf("Close() second = %v, want nil", err)
	}
}

// TestFatalExits — Fatal логирует ошибку в stderr (JSON) и завершает
// процесс с кодом 1. os.Exit нельзя проверить в рамках процесса теста,
// поэтому проверка — перезапуск теста в дочернем процессе.
func TestFatalExits(t *testing.T) {
	if os.Getenv("IMAGER_TEST_FATAL") == "1" {
		Fatal("bootstrap boom: %d", 42)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestFatalExits")
	cmd.Env = append(os.Environ(), "IMAGER_TEST_FATAL=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("Fatal(): child exited with status 0, want 1")
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child error = %v, want *exec.ExitError", err)
	}
	if ee.ExitCode() != 1 {
		t.Fatalf("child exit code = %d, want 1", ee.ExitCode())
	}
	if !strings.Contains(string(out), "bootstrap boom: 42") {
		t.Fatalf("output = %q, want mention of 'bootstrap boom: 42'", out)
	}
}

