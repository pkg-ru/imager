package imagemagick

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBoundedWriter_UnderLimit(t *testing.T) {
	var buf bytes.Buffer
	bw := &boundedWriter{w: &buf, max: 100}
	n, err := bw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
	if bw.exceeded {
		t.Error("should not be exceeded")
	}
	if buf.String() != "hello" {
		t.Errorf("buf = %q", buf.String())
	}
}

func TestBoundedWriter_ExceedsLimit(t *testing.T) {
	var buf bytes.Buffer
	canceled := false
	bw := &boundedWriter{w: &buf, max: 5, cancel: func() { canceled = true }}
	_, err := bw.Write([]byte("hello world"))
	if err == nil {
		t.Fatal("expected error on exceeding limit")
	}
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected LimitError, got %T", err)
	}
	if le.Kind != LimitOutput {
		t.Errorf("kind = %q, want output", le.Kind)
	}
	exceeded, _ := bw.exceededN()
	if !exceeded {
		t.Error("should be marked exceeded")
	}
	if !canceled {
		t.Error("cancel should be called")
	}
}

func TestBoundedWriter_ZeroLimitUnlimited(t *testing.T) {
	var buf bytes.Buffer
	bw := &boundedWriter{w: &buf, max: 0}
	if _, err := bw.Write([]byte(strings.Repeat("x", 10000))); err != nil {
		t.Fatalf("zero limit should be unlimited, got %v", err)
	}
}

func TestLimitedBuffer_UnderLimit(t *testing.T) {
	lb := &limitedBuffer{max: 100}
	if _, err := lb.Write([]byte("short")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if lb.truncated {
		t.Error("should not be truncated")
	}
	if lb.String() != "short" {
		t.Errorf("got %q", lb.String())
	}
}

func TestLimitedBuffer_Truncates(t *testing.T) {
	lb := &limitedBuffer{max: 10}
	if _, err := lb.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !lb.truncated {
		t.Error("should be truncated")
	}
	s := lb.String()
	if !strings.Contains(s, "... (stderr truncated)") {
		t.Errorf("missing truncation marker, got %q", s)
	}
	if len(s) <= 10 {
		t.Errorf("expected more than 10 chars, got %d", len(s))
	}
}

func TestLimitedBuffer_ZeroMaxUnlimited(t *testing.T) {
	lb := &limitedBuffer{max: 0}
	if _, err := lb.Write([]byte(strings.Repeat("x", 10000))); err != nil {
		t.Fatalf("zero max should be unlimited, got %v", err)
	}
	if lb.truncated {
		t.Error("should not be truncated")
	}
}

func TestLimitError_Unwrap(t *testing.T) {
	base := context.DeadlineExceeded
	le := &LimitError{Kind: LimitTime, Limit: 30, Err: base}
	if !errors.Is(le, base) {
		t.Error("LimitError should unwrap to base error")
	}
	if !strings.Contains(le.Error(), "time") {
		t.Errorf("error should mention limit kind, got %q", le.Error())
	}
}
