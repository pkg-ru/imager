package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkg-ru/imager/internal/domain/object"
)

func TestResultStoreNotFound(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	if _, err := store.Lookup(ctx, "missing.jpg"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("Lookup missing: expected ErrNotFound, got %v", err)
	}
	if _, err := store.Open(ctx, "missing.jpg"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("Open missing: expected ErrNotFound, got %v", err)
	}
}

func TestResultStoreAtomicPublishAndRead(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	key := object.ObjectKey("a/b/c.jpg")
	payload := []byte("hello world")
	if err := store.Publish(ctx, key, bytes.NewReader(payload), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	meta, err := store.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("Lookup size = %d, want %d", meta.Size, len(payload))
	}

	art, err := store.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read = %q, want %q", got, payload)
	}
	if art.Metadata().Size != int64(len(payload)) {
		t.Fatalf("artifact metadata size = %d, want %d", art.Metadata().Size, len(payload))
	}
}

func TestResultStoreNoOverwriteConflict(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	key := object.ObjectKey("dup.jpg")
	if err := store.Publish(ctx, key, strings.NewReader("first"), object.PublishOptions{}); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	err = store.Publish(ctx, key, strings.NewReader("second"), object.PublishOptions{NoOverwrite: true})
	if !errors.Is(err, object.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// Без NoOverwrite перезапись разрешена.
	if err := store.Publish(ctx, key, strings.NewReader("second"), object.PublishOptions{}); err != nil {
		t.Fatalf("overwrite Publish: %v", err)
	}
}

func TestResultStoreConcurrentReads(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	key := object.ObjectKey("shared.jpg")
	payload := bytes.Repeat([]byte("x"), 64*1024)
	if err := store.Publish(ctx, key, bytes.NewReader(payload), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	const readers = 16
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			art, err := store.Open(ctx, key)
			if err != nil {
				errs <- err
				return
			}
			defer art.Close()
			got, err := io.ReadAll(art)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, payload) {
				errs <- errors.New("concurrent read mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent read error: %v", err)
	}
}

func TestResultStoreTraversalRejected(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewResultStore(root)
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	// Создаём файл вне root, чтобы убедиться, что traversal не пишет туда.
	outside := filepath.Join(filepath.Dir(root), "outside.jpg")
	os.Remove(outside)
	defer os.Remove(outside)

	// Попытка publish с traversal-ключом должна завершиться ошибкой.
	err = store.Publish(ctx, object.ObjectKey("../outside.jpg"), strings.NewReader("x"), object.PublishOptions{})
	if err == nil {
		t.Fatalf("expected traversal publish to fail")
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("traversal wrote outside root: %v", statErr)
	}

	// Lookup/Open с traversal-ключом тоже должны отклоняться.
	if _, err := store.Lookup(ctx, object.ObjectKey("../outside.jpg")); err == nil {
		t.Fatalf("expected traversal lookup to fail")
	}
	if _, err := store.Open(ctx, object.ObjectKey("../outside.jpg")); err == nil {
		t.Fatalf("expected traversal open to fail")
	}
}

func TestResultStoreDeleteAndStats(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	key := object.ObjectKey("del.jpg")
	if err := store.Publish(ctx, key, strings.NewReader("data"), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 1 || stats.TotalBytes != 4 {
		t.Fatalf("stats = %+v, want 1 object / 4 bytes", stats)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Идемпотентность: повторное удаление не ошибка.
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if _, err := store.Lookup(ctx, key); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestResultStoreMetaSegmentIsolated — публичные операции ResultStore
// отклоняют ключи с зарезервированным сегментом ".meta" (изоляция sidecar-
// метаданных от пространства ассетов).
func TestResultStoreMetaSegmentIsolated(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	keys := []object.ObjectKey{
		".meta/about.png.json", // чтение sidecar как ассета
		"x/.meta/y",            // вложенный сегмент
		"meta/../.meta/x",      // обход через ".." (отклоняется отдельно)
	}

	for _, key := range keys {
		if _, err := store.Open(ctx, key); !object.IsUnsafePath(err) {
			t.Errorf("Open(%q): expected ErrUnsafePath, got %v", key, err)
		}
		if _, err := store.Lookup(ctx, key); !object.IsUnsafePath(err) {
			t.Errorf("Lookup(%q): expected ErrUnsafePath, got %v", key, err)
		}
		if _, err := store.ReadStream(ctx, key); !object.IsUnsafePath(err) {
			t.Errorf("ReadStream(%q): expected ErrUnsafePath, got %v", key, err)
		}
		if err := store.Delete(ctx, key); !object.IsUnsafePath(err) {
			t.Errorf("Delete(%q): expected ErrUnsafePath, got %v", key, err)
		}
		err := store.Publish(ctx, key, strings.NewReader("x"), object.PublishOptions{})
		if !object.IsUnsafePath(err) {
			t.Errorf("Publish(%q): expected ErrUnsafePath, got %v", key, err)
		}
	}
}

func TestSourceStoreNotFoundAndOpen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewSourceStore(root)
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}

	if _, err := store.Lookup(ctx, "missing.jpg"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("Lookup missing: expected ErrNotFound, got %v", err)
	}

	// Создаём исходный файл и проверяем чтение.
	sub := filepath.Join(root, "bucket")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := []byte("source-data")
	if err := os.WriteFile(filepath.Join(sub, "img.jpg"), payload, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	art, err := store.Open(ctx, object.ObjectKey("bucket/img.jpg"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read = %q, want %q", got, payload)
	}
}

// TestResultStoreList проверяет List: возвращает ключи с заданным префиксом,
// пропуская временные файлы публикации.
func TestResultStoreList(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	// Публикуем ассеты исходника "thumbs/photo.jpg".
	keys := []object.ObjectKey{
		"thumbs/photo-jpg/thumb.webp",
		"thumbs/photo-jpg/c-120x80@2.webp",
		"thumbs/other-jpg/thumb.webp",
	}
	for _, k := range keys {
		if err := store.Publish(ctx, k, strings.NewReader("x"), object.PublishOptions{}); err != nil {
			t.Fatalf("Publish(%q): %v", k, err)
		}
	}

	// List по префиксу исходника возвращает только его ассеты.
	got, err := store.List(ctx, object.ObjectKey("thumbs/photo-jpg/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2 (got %v)", len(got), got)
	}
	for _, k := range got {
		if !strings.HasPrefix(k.String(), "thumbs/photo-jpg/") {
			t.Errorf("key %q outside prefix", k)
		}
	}

	// List с пустым префиксом возвращает все.
	all, err := store.List(ctx, object.ObjectKey(""))
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List(empty) len = %d, want 3", len(all))
	}
}
