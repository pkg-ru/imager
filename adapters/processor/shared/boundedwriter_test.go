package shared

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestBoundedWriterUnderLimit(t *testing.T) {
	var out bytes.Buffer
	bw := NewBoundedWriter(&out, 100, nil)
	n, err := bw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
	exceeded, actual := bw.ExceededN()
	if exceeded {
		t.Error("should not be exceeded")
	}
	if actual != 5 {
		t.Errorf("actual = %d, want 5", actual)
	}
	if out.String() != "hello" {
		t.Errorf("buf = %q", out.String())
	}
}

func TestBoundedWriterExceedsLimit(t *testing.T) {
	var out bytes.Buffer
	canceled := false
	cancel := func() { canceled = true }
	bw := NewBoundedWriter(&out, 5, cancel)
	n, err := bw.Write([]byte("hello world"))
	if !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("err = %v, want ErrOutputLimitExceeded", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0 (nothing written on exceed)", n)
	}
	exceeded, _ := bw.ExceededN()
	if !exceeded {
		t.Error("should be marked exceeded")
	}
	if !canceled {
		t.Error("cancel should be called")
	}
	// Данные не должны были попасть в writer.
	if out.Len() != 0 {
		t.Errorf("out = %q, want empty", out.String())
	}
}

func TestBoundedWriterZeroLimitUnlimited(t *testing.T) {
	var out bytes.Buffer
	bw := NewBoundedWriter(&out, 0, nil)
	data := bytes.Repeat([]byte("x"), 10000)
	if _, err := bw.Write(data); err != nil {
		t.Fatalf("zero limit should be unlimited, got %v", err)
	}
	exceeded, actual := bw.ExceededN()
	if exceeded {
		t.Error("zero limit should never be exceeded")
	}
	if actual != int64(len(data)) {
		t.Errorf("actual = %d, want %d", actual, len(data))
	}
}

func TestBoundedWriterExactBoundary(t *testing.T) {
	var out bytes.Buffer
	bw := NewBoundedWriter(&out, 5, nil)
	// Ровно max — допустимо.
	if n, err := bw.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("boundary write: n=%d err=%v", n, err)
	}
	// Один байт сверх — превышение.
	if _, err := bw.Write([]byte("!")); !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("err = %v, want ErrOutputLimitExceeded", err)
	}
}

func TestBoundedWriterNilCancelOK(t *testing.T) {
	var out bytes.Buffer
	bw := NewBoundedWriter(&out, 1, nil)
	if _, err := bw.Write([]byte("ab")); !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("err = %v, want ErrOutputLimitExceeded", err)
	}
}

func TestBoundedWriterCancelReceivesContext(t *testing.T) {
	// Проверяем интеграцию с context: cancel из writer отменяет контекст.
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	bw := NewBoundedWriter(&out, 1, cancel)
	_, _ = bw.Write([]byte("ab"))
	select {
	case <-ctx.Done():
	default:
		t.Fatal("context should be canceled after exceeding limit")
	}
}
