package generatev2

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Регрессия: вывод ровно max байт должен читаться до EOF, а не отклоняться
// как превышение лимита (off-by-one на границе OutputLimit).
func TestBoundedReaderExactLimit(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 64)
	br := &boundedReader{r: bytes.NewReader(data), max: 64}

	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("read %d bytes, want 64", len(got))
	}
}

// Данные сверх лимита отвергаются, лишний байт не передаётся дальше.
func TestBoundedReaderOverLimit(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 65)
	br := &boundedReader{r: bytes.NewReader(data), max: 64}

	got, err := io.ReadAll(br)
	if err == nil || !strings.Contains(err.Error(), errOutputLimit.Error()) {
		t.Fatalf("ReadAll() error = %v, want %v", err, errOutputLimit)
	}
	if len(got) > 64 {
		t.Fatalf("read %d bytes, want <= 64 (limit must not be exceeded)", len(got))
	}
}

// Меньше лимита — читается полностью без ошибок.
func TestBoundedReaderUnderLimit(t *testing.T) {
	data := bytes.Repeat([]byte("b"), 10)
	br := &boundedReader{r: bytes.NewReader(data), max: 64}

	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("data mismatch: got %d bytes", len(got))
	}
}

// Регрессия: reset() сбрасывает счётчик перед повторной попыткой публикации,
// иначе retry публикует усечённые данные.
func TestBoundedReaderReset(t *testing.T) {
	data := bytes.Repeat([]byte("c"), 32)
	src := bytes.NewReader(data)
	br := &boundedReader{r: src, max: 64}

	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("first ReadAll() error: %v", err)
	}

	// Имитация retry: перемотка источника + reset счётчика.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek() error: %v", err)
	}
	br.reset()

	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("second ReadAll() error: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("after reset read %d bytes, want 32 (full data)", len(got))
	}
}
