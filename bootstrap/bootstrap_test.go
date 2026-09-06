package bootstrap

import (
	"context"
	"errors"
	"io"
	"testing"

	"gitverse.ru/pkg-ru/imager/ports/processor"
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
