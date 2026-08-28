package routing

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/ports/processor"
)

// recProcessor — записывающий процессор: помечает имя движка и копирует
// источник в out.
type recProcessor struct {
	name   string
	calls  int
	result *processor.Result
	err    error
}

func (p *recProcessor) Process(_ context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	n, _ := io.Copy(out, in.Source)
	if p.result != nil {
		return p.result, nil
	}
	return &processor.Result{Size: n}, nil
}

// plan строит план с указанными форматами.
func plan(t *testing.T, src, out processing.Format, op processing.Operation) *processing.ProcessingPlan {
	t.Helper()
	p, err := processing.NewProcessingPlan(
		op, src, out,
		processing.Size{Width: 100, Height: 100},
		1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	return p
}

// caps строит Capability из списка форматов.
func caps(name string, formats ...processing.Format) Capability {
	m := make(map[processing.Format]bool, len(formats))
	for _, f := range formats {
		m[f] = true
	}
	return Capability{Name: name, Formats: m}
}

func TestRoutingUsesPrimary(t *testing.T) {
	primary := &recProcessor{name: "primary"}

	r, err := New(Options{
		Primary:     primary,
		PrimaryCaps: caps("libvips", processing.FormatJPEG, processing.FormatPNG),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var out strings.Builder
	_, err = r.Process(context.Background(), processor.Input{
		Source: strings.NewReader("DATA"),
		Plan:   plan(t, processing.FormatJPEG, processing.FormatPNG, processing.OpResize),
	}, &out)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if primary.calls != 1 {
		t.Fatalf("primary.calls=%d, want 1", primary.calls)
	}
	if out.String() != "DATA" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestRoutingAPNGUsesPrimary(t *testing.T) {
	primary := &recProcessor{name: "primary"}

	r, err := New(Options{
		Primary:     primary,
		PrimaryCaps: caps("libvips", processing.FormatJPEG, processing.FormatPNG, processing.FormatAPNG),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var out strings.Builder
	_, err = r.Process(context.Background(), processor.Input{
		Source: strings.NewReader("DATA"),
		Plan:   plan(t, processing.FormatPNG, processing.FormatAPNG, processing.OpResize),
	}, &out)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// APNG покрывается primary (libvips ≥ 8.13).
	if primary.calls != 1 {
		t.Fatalf("primary.calls=%d, want 1", primary.calls)
	}
}

func TestRoutingEngineUnavailable(t *testing.T) {
	primary := &recProcessor{name: "primary"}

	r, err := New(Options{
		Primary:     primary,
		PrimaryCaps: caps("libvips", processing.FormatJPEG, processing.FormatPNG),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Формат вне покрытия primary → engine-unavailable.
	_, err = r.Process(context.Background(), processor.Input{
		Source: strings.NewReader("DATA"),
		Plan:   plan(t, processing.FormatGIF, processing.FormatAPNG, processing.OpResize),
	}, io.Discard)
	if err == nil {
		t.Fatal("Process: want engine-unavailable error")
	}
	if !IsEngineUnavailable(err) {
		t.Fatalf("err = %v, want IsEngineUnavailable", err)
	}
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnsupportedError", err)
	}
	if ue.Missing == "" {
		t.Fatal("UnsupportedError.Missing must be set")
	}
	if primary.calls != 0 {
		t.Fatalf("primary.calls=%d, want 0", primary.calls)
	}
}

func TestRoutingNilPlan(t *testing.T) {
	primary := &recProcessor{name: "primary"}
	r, err := New(Options{
		Primary:     primary,
		PrimaryCaps: caps("libvips", processing.FormatJPEG),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = r.Process(context.Background(), processor.Input{
		Source: strings.NewReader("DATA"),
		Plan:   nil,
	}, io.Discard)
	if err == nil {
		t.Fatal("Process: want nil-plan error")
	}
}

func TestRoutingNewValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New without primary: want error")
	}
	if _, err := New(Options{Primary: &recProcessor{}}); err == nil {
		t.Fatal("New without primary caps: want error")
	}
}

func TestIsEngineUnavailable(t *testing.T) {
	if IsEngineUnavailable(nil) {
		t.Fatal("nil must not be engine-unavailable")
	}
	if IsEngineUnavailable(errors.New("other")) {
		t.Fatal("plain error must not be engine-unavailable")
	}
	if !IsEngineUnavailable(ErrEngineUnavailable) {
		t.Fatal("ErrEngineUnavailable must be detected")
	}
	if !IsEngineUnavailable(&UnsupportedError{Format: processing.FormatAPNG, Missing: "libvips"}) {
		t.Fatal("*UnsupportedError must be detected")
	}
}
