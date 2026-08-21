// Package contract определяет контрактные тесты для storage-адаптеров.
// Выделен в отдельный пакет, чтобы избежать циклических зависимостей между
// тестами разных адаптеров.
package contract

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// SourceStoreContract — набор тестов, которым должен удовлетворять любой
// SourceStore. Реализации адаптеров вызывают эти функции из своих _test.go.
type SourceStoreContract struct {
	// NewSource создаёт свежий SourceStore для теста.
	NewSource func(t *testing.T) storage.SourceStore
	// Seed наполняет хранилище тестовыми данными.
	Seed func(t *testing.T, s storage.SourceStore, key object.ObjectKey, data []byte)
}

// ResultStoreContract — набор тестов, которым должен удовлетворять любой
// ResultStore.
type ResultStoreContract struct {
	// NewResult создаёт свежий ResultStore для теста.
	NewResult func(t *testing.T) storage.ResultStore
}

// Run запускает контрактные тесты для ResultStore.
func Run(t *testing.T, c ResultStoreContract) {
	t.Helper()
	t.Run("PublishAndOpen", func(t *testing.T) {
		s := c.NewResult(t)
		ctx := context.Background()
		key := object.ObjectKey("test.bin")
		data := []byte("hello")
		if err := s.Publish(ctx, key, bytes.NewReader(data), object.PublishOptions{}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		art, err := s.Open(ctx, key)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer art.Close()
		got, err := io.ReadAll(art)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(got) != string(data) {
			t.Fatalf("got %q, want %q", got, data)
		}
	})
	t.Run("DeleteIdempotent", func(t *testing.T) {
		s := c.NewResult(t)
		ctx := context.Background()
		key := object.ObjectKey("del.bin")
		if err := s.Publish(ctx, key, bytes.NewReader([]byte("x")), object.PublishOptions{}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatalf("Delete idempotent: %v", err)
		}
	})
}

// RunSource запускает контрактные тесты для SourceStore.
func RunSource(t *testing.T, c SourceStoreContract) {
	t.Helper()
	t.Run("OpenNotFound", func(t *testing.T) {
		s := c.NewSource(t)
		ctx := context.Background()
		if _, err := s.Open(ctx, object.ObjectKey("nonexistent")); !object.IsNotFound(err) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
	t.Run("LookupNotFound", func(t *testing.T) {
		s := c.NewSource(t)
		ctx := context.Background()
		if _, err := s.Lookup(ctx, object.ObjectKey("nonexistent")); !object.IsNotFound(err) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
	t.Run("OpenSeeded", func(t *testing.T) {
		s := c.NewSource(t)
		ctx := context.Background()
		key := object.ObjectKey("seeded.bin")
		data := []byte("seed-data")
		c.Seed(t, s, key, data)
		art, err := s.Open(ctx, key)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer art.Close()
		got, err := io.ReadAll(art)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(got) != string(data) {
			t.Fatalf("got %q, want %q", got, data)
		}
	})
}
