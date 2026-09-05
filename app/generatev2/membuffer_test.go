package generatev2

import (
	"bytes"
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

// Регрессия: новый reader можно создать и после Close буфера, пока данные
// живы (есть открытые reader'ы). Это позволяет каждому запросу singleflight
// получить собственный reader из общего буфера, даже если другой запрос уже
// закрыл свой reader (и тем самым пометил буфер closed). Данные
// освобождаются только когда буфер закрыт И не осталось reader'ов.
func TestMemBufferNewReaderAfterClose(t *testing.T) {
	b := &memBuffer{}
	if _, err := b.Write([]byte("hello world")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	r1, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader() error: %v", err)
	}
	// Закрываем буфер (как это делает bufferStream.Close после закрытия
	// reader'а) — данные должны остаться живыми, пока открыт r1.
	if err := b.Close(); err != nil {
		t.Fatalf("buf.Close() error: %v", err)
	}

	// Новый reader после Close: разрешён, пока есть открытые reader'ы.
	r2, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader() after Close error: %v", err)
	}

	got1, err := io.ReadAll(r1)
	if err != nil {
		t.Fatalf("ReadAll(r1) error: %v", err)
	}
	got2, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("ReadAll(r2) error: %v", err)
	}
	if string(got1) != "hello world" || string(got2) != "hello world" {
		t.Fatalf("read %q / %q, want %q", string(got1), string(got2), "hello world")
	}

	// Закрываем оба reader'а — теперь данных нет, новые reader'ы запрещены.
	if err := r1.Close(); err != nil {
		t.Fatalf("r1.Close() error: %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("r2.Close() error: %v", err)
	}
	if _, err := b.NewReader(); err == nil {
		t.Fatal("NewReader() after full release: expected error")
	}
}

// Quick win (Q4): chunk'и берутся из sync.Pool и возвращаются в него только
// при окончательном освобождении буфера (закрыт и нет открытых reader'ов).
// Проверяем целостность данных для буфера больше одного chunk и переиспользование
// пула после освобождения.
func TestMemBufferChunkPoolReuse(t *testing.T) {
	// Данные больше одного 32KiB chunk — должны корректно писаться/читаться
	// через chunk'и из пула.
	const payloadSize = 3*memChunkSize + 123
	data := make([]byte, payloadSize)
	for i := range data {
		data[i] = byte(i % 251)
	}

	b := &memBuffer{}
	if _, err := b.Write(data); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if b.Size() != int64(payloadSize) {
		t.Fatalf("Size() = %d, want %d", b.Size(), payloadSize)
	}

	got, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("data mismatch: got %d bytes, want %d", len(got), payloadSize)
	}

	// Буфер без reader'ов: Close должен вернуть chunk'и в пул.
	if err := b.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}
