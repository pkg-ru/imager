package remote

import (
	"bytes"
	"io"
	"testing"
)

// Регрессия: reader in-memory буфера должен видеть данные, записанные ПОСЛЕ
// его создания (контракт параллельного чтения во время записи), а не снимок
// слайса на момент создания.
func TestBufferReaderSeesWritesAfterCreation(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer() error: %v", err)
	}
	defer func() { _ = b.Close() }()

	if _, err := b.Write([]byte("hello ")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	r, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader() error: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Дописываем данные после создания reader'а.
	if _, err := b.Write([]byte("world")); err != nil {
		t.Fatalf("Write() after NewReader error: %v", err)
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if !bytes.Equal(got, []byte("hello world")) {
		t.Fatalf("reader read %q, want %q", string(got), "hello world")
	}
}

// Seek от конца должен использовать актуальный размер буфера.
func TestBufferReaderSeekEndLiveSize(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer() error: %v", err)
	}
	defer func() { _ = b.Close() }()

	if _, err := b.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	r, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader() error: %v", err)
	}
	defer func() { _ = r.Close() }()

	if _, err := b.Write([]byte("gh")); err != nil {
		t.Fatalf("Write() after NewReader error: %v", err)
	}

	pos, err := r.Seek(-2, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek() error: %v", err)
	}
	if pos != 6 {
		t.Fatalf("Seek(-2, End) = %d, want 6", pos)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if string(got) != "gh" {
		t.Fatalf("tail read %q, want %q", string(got), "gh")
	}
}
