package generatev2

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// wantOutcome проверяет, что err является *OutcomeError с указанной
// категорией (в том числе обёрнутой) — тот же путь, что использует прод
// (errors.As + switch по Kind).
func wantOutcome(t *testing.T, err error, kind OutcomeKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected OutcomeError %v, got nil", kind)
	}
	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v (%T), want *OutcomeError", err, err)
	}
	if oe.Kind != kind {
		t.Fatalf("kind = %v, want %v", oe.Kind, kind)
	}
}

// fakeProcessor — processor.Processor с детерминированным выводом и
// возможностью эмулировать ошибки/блокировку.
type fakeProcessor struct {
	mu        sync.Mutex
	payload   []byte
	procErr   error
	block     chan struct{}
	calls     int
	lastInput []byte
}

func newFakeProcessor(payload []byte) *fakeProcessor {
	return &fakeProcessor{payload: payload}
}

func (f *fakeProcessor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	f.mu.Lock()
	f.calls++
	block := f.block
	procErr := f.procErr
	payload := f.payload
	if in.Source != nil {
		data, _ := io.ReadAll(in.Source)
		f.lastInput = data
	}
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if procErr != nil {
		return nil, procErr
	}
	if _, err := out.Write(payload); err != nil {
		return nil, err
	}
	return &processor.Result{Size: int64(len(payload))}, nil
}

func (f *fakeProcessor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProcessor) lastInputData() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastInput
}

func (f *fakeProcessor) setBlock(ch chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block = ch
}

func (f *fakeProcessor) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procErr = err
}
