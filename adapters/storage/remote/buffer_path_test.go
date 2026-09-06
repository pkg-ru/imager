package remote

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/object"
)

// TestBufferPathInMemory — in-RAM буфер возвращает пустой путь (fallback на
// stdin-pipe для videoframe/ffmpeg).
func TestBufferPathInMemory(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer() error: %v", err)
	}
	defer func() { _ = b.Close() }()

	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if !b.InMemory() {
		t.Fatal("buffer should be in memory")
	}
	if got := b.Path(); got != "" {
		t.Fatalf("Path() = %q, want empty (in-memory)", got)
	}
}

// TestBufferPathSpilled — буфер, спилленный на диск, возвращает путь
// spill-файла; файл существует и содержит записанные данные.
func TestBufferPathSpilled(t *testing.T) {
	dir := t.TempDir()
	// Пул с бюджетом 1 байт форсирует spill первого же чанка
	// (tryReserve вернёт false, т.к. need > 1).
	pool := NewBufferPool(1)
	b, err := NewBuffer(BufferOptions{Pool: pool, Dir: dir})
	if err != nil {
		t.Fatalf("NewBuffer() error: %v", err)
	}
	defer func() { _ = b.Close() }()

	payload := []byte("video-bytes")
	if _, err := b.Write(payload); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if b.InMemory() {
		t.Fatal("buffer should have spilled to disk")
	}

	path := b.Path()
	if path == "" {
		t.Fatal("Path() = empty, want spill file path")
	}
	if fp := b.File(); fp == nil || fp.Name() != path {
		t.Fatalf("Path() = %q, File().Name() = %q", path, func() string {
			if fp == nil {
				return "<nil>"
			}
			return fp.Name()
		}())
	}
	// Файл по пути существует.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat spill file: %v", err)
	}
	// Данные доступны из буфера.
	if _, err := b.Seek(0, 0); err != nil {
		t.Fatalf("Seek() error: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := b.Read(got); err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read %q, want %q", string(got), string(payload))
	}
	_ = filepath.Base(path) // путь внутри dir
}

// TestBufferPathAfterRelease — после полного освобождения ресурсов путь
// больше не валиден (возвращает "" или файл удалён). Контракт: Path()
// валиден до Close/release.
func TestBufferPathAfterRelease(t *testing.T) {
	dir := t.TempDir()
	pool := NewBufferPool(1)
	b, err := NewBuffer(BufferOptions{Pool: pool, Dir: dir})
	if err != nil {
		t.Fatalf("NewBuffer() error: %v", err)
	}
	if _, err := b.Write([]byte("data")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	path := b.Path()
	if path == "" {
		t.Fatal("Path() = empty, want spill file path")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	// Ресурсы освобождены — spill-файл удалён.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("spill file should be removed after release, stat err = %v", err)
	}
}

// TestBufferArtifactPath — bufferArtifact делегирует Path() буферу.
func TestBufferArtifactPath(t *testing.T) {
	dir := t.TempDir()
	pool := NewBufferPool(1)
	b, err := NewBuffer(BufferOptions{Pool: pool, Dir: dir})
	if err != nil {
		t.Fatalf("NewBuffer() error: %v", err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Write([]byte("payload-over-budget")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	art := NewBufferArtifact(b, object.ObjectMetadata{})
	p, ok := art.(interface{ Path() string })
	if !ok {
		t.Fatal("bufferArtifact does not implement Path()")
	}
	if got := p.Path(); got == "" {
		t.Fatal("bufferArtifact.Path() = empty, want spill file path")
	} else if got != b.Path() {
		t.Fatalf("bufferArtifact.Path() = %q, want %q", got, b.Path())
	}

	// In-RAM буфер → пустой путь.
	mb, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer() error: %v", err)
	}
	defer func() { _ = mb.Close() }()
	mart := NewBufferArtifact(mb, object.ObjectMetadata{})
	if got := mart.(interface{ Path() string }).Path(); got != "" {
		t.Fatalf("in-memory bufferArtifact.Path() = %q, want empty", got)
	}
}
