package generatev2

import (
	"io"
	"testing"
)

// Регрессия: повторный Close одного reader'а не должен декрементировать refs
// другого активного reader'а (иначе память освобождается преждевременно).
func TestMemBufferReaderCloseIdempotent(t *testing.T) {
	b := &memBuffer{}
	if _, err := b.Write([]byte("hello world")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	r1, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader() error: %v", err)
	}
	r2, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader() error: %v", err)
	}

	// Закрываем r1 дважды и буфер (как это делает bufferStream.Close).
	if err := r1.Close(); err != nil {
		t.Fatalf("r1.Close() error: %v", err)
	}
	if err := r1.Close(); err != nil {
		t.Fatalf("double r1.Close() error: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("buf.Close() error: %v", err)
	}

	// r2 всё ещё активен: данные должны читаться корректно.
	got, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("ReadAll(r2) error: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("r2 read %q, want %q", string(got), "hello world")
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("r2.Close() error: %v", err)
	}
}
