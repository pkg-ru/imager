package remote

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestSpoolWriteReadSeek(t *testing.T) {
	s, err := NewSpool(SpoolOptions{})
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}
	defer s.Close()

	payload := []byte("hello spool world")
	n, err := s.WriteFrom(bytes.NewReader(payload), 0)
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("written = %d, want %d", n, len(payload))
	}
	if s.Size() != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", s.Size(), len(payload))
	}

	if _, err := s.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read mismatch: got %q, want %q", got, payload)
	}
}

func TestSpoolLimit(t *testing.T) {
	s, err := NewSpool(SpoolOptions{})
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}
	defer s.Close()

	payload := []byte("0123456789")
	_, err = s.WriteFrom(bytes.NewReader(payload), 5)
	if !errors.Is(err, ErrSpoolLimit) {
		t.Fatalf("expected ErrSpoolLimit, got %v", err)
	}
}

func TestSpoolCloseRemovesFile(t *testing.T) {
	s, err := NewSpool(SpoolOptions{})
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}
	name := s.File().Name()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := io.ReadAll(s); err == nil {
		t.Fatal("expected read after close to fail")
	}
	_ = name
}
