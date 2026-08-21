package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/imager/internal/application/ports/contract"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// TestResultStoreContractFS — контрактные тесты, общие для всех будущих
// адаптеров (S3, external disk).
func TestResultStoreContractFS(t *testing.T) {
	contract.Run(t, contract.ResultStoreContract{
		NewResult: func(t *testing.T) storage.ResultStore {
			s, err := NewResultStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewResultStore: %v", err)
			}
			return s
		},
	})
}

// TestSourceStoreContractFS — контрактные тесты SourceStore, общие для всех
// будущих read-only адаптеров (S3, external disk, FTP).
func TestSourceStoreContractFS(t *testing.T) {
	contract.RunSource(t, contract.SourceStoreContract{
		NewSource: func(t *testing.T) storage.SourceStore {
			s, err := NewSourceStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewSourceStore: %v", err)
			}
			return s
		},
		Seed: func(t *testing.T, s storage.SourceStore, key object.ObjectKey, data []byte) {
			t.Helper()
			fs, ok := s.(*SourceStore)
			if !ok {
				t.Fatalf("expected *SourceStore, got %T", s)
			}
			full := filepath.Join(fs.Root(), filepath.FromSlash(key.String()))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(full, data, 0o644); err != nil {
				t.Fatalf("write seed: %v", err)
			}
		},
	})
}

// TestSymlinkTraversalRejected проверяет, что операции не следуют symlink
// внутри root, указывающим наружу. На Windows создание symlink требует
// привилегий — поэтому тест пропускается, если не удалось создать.
func TestSymlinkTraversalRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows; covered by junction test if available")
	}
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// symlink внутри root указывает на outside.
	link := filepath.Join(root, "sub", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	store, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}

	// Open через symlink должен вернуть ErrUnsafePath (или не выдать содержимое).
	art, err := store.Open(ctx, object.ObjectKey("sub/link"))
	if err == nil {
		art.Close()
		t.Fatalf("expected Open through symlink to fail")
	}
	if !object.IsUnsafePath(err) {
		t.Fatalf("expected unsafe path error, got %v", err)
	}

	// Delete через symlink тоже запрещён.
	if err := store.Delete(ctx, object.ObjectKey("sub/link")); !object.IsUnsafePath(err) {
		t.Fatalf("expected unsafe path error on Delete, got %v", err)
	}
}

// TestQuotaEviction проверяет soft-лимит (MaxBytes) и eviction LRU.
func TestQuotaEviction(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStoreWithOptions(t.TempDir(), ResultStoreOptions{
		Cache: CacheOptions{MaxBytes: 30, MaxObjects: 0},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Публикуем три объекта: a(10), b(10), c(10) => 30 <= limit.
	for _, k := range []string{"a", "b", "c"} {
		if err := store.Publish(ctx, object.ObjectKey(k), strings.NewReader(strings.Repeat("x", 10)), object.PublishOptions{}); err != nil {
			t.Fatalf("publish %s: %v", k, err)
		}
	}
	stats, err := store.CacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stats

	// Публикация d(10) превысит 30: eviction удалит самый старый (a — первым
	// опубликован). Ожидаем, что a исчезнет после publish d.
	if err := store.Publish(ctx, object.ObjectKey("d"), strings.NewReader(strings.Repeat("y", 10)), object.PublishOptions{}); err != nil {
		t.Fatalf("publish d: %v", err)
	}
	if _, err := store.Lookup(ctx, "a"); !object.IsNotFound(err) {
		t.Fatalf("expected 'a' evicted, got %v", err)
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, err := store.Lookup(ctx, object.ObjectKey(k)); err != nil {
			t.Fatalf("expected %s present, got %v", k, err)
		}
	}
}

// TestMaxObjectsEviction проверяет soft-лимит MaxObjects: при превышении
// числа объектов eviction удаляет самые старые (LRU).
func TestMaxObjectsEviction(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStoreWithOptions(t.TempDir(), ResultStoreOptions{
		Cache: CacheOptions{MaxObjects: 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Публикуем a, b (2 объекта = лимит).
	for _, k := range []string{"a", "b"} {
		if err := store.Publish(ctx, object.ObjectKey(k), strings.NewReader("x"), object.PublishOptions{}); err != nil {
			t.Fatalf("publish %s: %v", k, err)
		}
	}
	// Публикация c превысит MaxObjects=2: eviction удалит самый старый (a).
	if err := store.Publish(ctx, object.ObjectKey("c"), strings.NewReader("y"), object.PublishOptions{}); err != nil {
		t.Fatalf("publish c: %v", err)
	}
	if _, err := store.Lookup(ctx, "a"); !object.IsNotFound(err) {
		t.Fatalf("expected 'a' evicted (MaxObjects), got %v", err)
	}
	for _, k := range []string{"b", "c"} {
		if _, err := store.Lookup(ctx, object.ObjectKey(k)); err != nil {
			t.Fatalf("expected %s present, got %v", k, err)
		}
	}
}

// TestHardQuotaRejectsPublish проверяет жёсткую квоту QuotaBytes до записи.
func TestHardQuotaRejectsPublish(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStoreWithOptions(t.TempDir(), ResultStoreOptions{
		Cache: CacheOptions{MaxBytes: 100, QuotaBytes: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 40 байт — успешно.
	if err := store.Publish(ctx, "a", strings.NewReader(strings.Repeat("x", 40)), object.PublishOptions{}); err != nil {
		t.Fatalf("publish 40B: %v", err)
	}
	// Ещё 40 байт => current=80 > quota=50 — превышение.
	err = store.Publish(ctx, "b", strings.NewReader(strings.Repeat("y", 40)), object.PublishOptions{})
	if !errors.Is(err, object.ErrQuota) {
		t.Fatalf("expected ErrQuota, got %v", err)
	}
}

// TestJanitorTempFiles проверяет поиск и удаление старых temp-файлов.
func TestJanitorTempFiles(t *testing.T) {
	root := t.TempDir()
	// Создаём "брошенный" temp-файл с old modtime.
	old := filepath.Join(root, ".tmp-abandoned")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	// Молодой temp.
	young := filepath.Join(root, ".tmp-fresh")
	if err := os.WriteFile(young, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// обычный файл.
	if err := os.WriteFile(filepath.Join(root, "real.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	j, err := NewJanitor(root, JanitorOptions{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	n, err := j.CleanTemps()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 temp removed, got %d", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old temp should be removed")
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("young temp should remain: %v", err)
	}
}

// TestConcurrentQuotaPublish проверяет race-safe поведение жёсткой квоты при
// конкурентных публикациях: суммарный размер не должен превышать квоту, а
// ошибки ErrQuota допустимы (но не data race).
func TestConcurrentQuotaPublish(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStoreWithOptions(t.TempDir(), ResultStoreOptions{
		Cache: CacheOptions{QuotaBytes: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 8
	var wg sync.WaitGroup
	quotaErrs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := object.ObjectKey(fmt.Sprintf("q-%d", i))
			payload := bytes.Repeat([]byte{byte('a' + i)}, 30)
			err := store.Publish(ctx, key, bytes.NewReader(payload), object.PublishOptions{})
			if err != nil && !errors.Is(err, object.ErrQuota) {
				quotaErrs <- err
			}
		}(i)
	}
	wg.Wait()
	close(quotaErrs)
	for err := range quotaErrs {
		t.Fatalf("unexpected error: %v", err)
	}
	// Суммарный размер не должен превышать квоту.
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalBytes > 100 {
		t.Fatalf("total bytes %d exceeds quota 100", stats.TotalBytes)
	}
}

// TestConcurrentPublishSameKey проверяет, что одновременные publish одного и
// того же ключа не ломают кэш (последний победитель, чтение целостно).
//
// На Windows os.Rename не перезаписывает существующий файл атомарно при
// конкурентной записи (возвращает Access denied), поэтому тест пропускается.
func TestConcurrentPublishSameKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Rename on Windows is not atomic for concurrent overwrite")
	}
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 8
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte('a' + i)}, 4096)
			if err := store.Publish(ctx, "shared", bytes.NewReader(payload), object.PublishOptions{}); err != nil {
				t.Errorf("publish: %v", err)
			}
		}(i)
	}
	wg.Wait()

	art, err := store.Open(ctx, "shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer art.Close()
	data, err := io.ReadAll(art)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 4096 {
		t.Fatalf("unexpected length %d", len(data))
	}
	// Содержимое должно быть одним из валидных payload.
	ok := false
	for i := 0; i < goroutines; i++ {
		if bytes.Equal(data, bytes.Repeat([]byte{byte('a' + i)}, 4096)) {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("corrupted data after concurrent publish")
	}
}

// TestContainmentEncodedTraversal проверяет, что обратный слеш (Windows-
// разделитель) в ключе отклоняется. Encoded separators (%2f) НЕ декодируются
// адаптером: это ответственность доменного слоя (object.ObjectKey). Адаптер
// трактует "%2f" как обычный символ имени файла, что безопасно (не является
// разделителем на ФС).
func TestContainmentEncodedTraversal(t *testing.T) {
	ctx := context.Background()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys := []object.ObjectKey{
		"a\\b.jpg", // обратный слеш — запрещён
		"a\\..\\b.jpg",
	}
	for _, k := range keys {
		if err := store.Publish(ctx, k, strings.NewReader("x"), object.PublishOptions{}); err == nil {
			t.Fatalf("expected publish %q to fail", k)
		}
	}
}

// TestEncodedSeparatorInSegmentRejected проверяет, что даже внутри чистого
// имени обратный слеш запрещён.
func TestEncodedSeparatorInSegmentRejected(t *testing.T) {
	if _, err := cleanRel(t.TempDir(), "a\\b.jpg"); err == nil {
		t.Fatalf("expected backslash segment rejection")
	}
}

func TestWalkComponentsRejectsSymlinkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "out")
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.WriteFile(outside, []byte("s"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "a", "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	err := walkComponentsNotSymlink(root, filepath.Join(root, "a", "link", "x.jpg"))
	if err == nil || !errors.Is(err, errSymlinkEscape) {
		t.Fatalf("expected errSymlinkEscape, got %v", err)
	}
}

// TestUnsafeErrorTyped проверяет, что traversal-ключ даёт типизированную
// ошибку object.ErrUnsafePath.
func TestUnsafeErrorTyped(t *testing.T) {
	base := t.TempDir()
	store, _ := NewResultStore(base)
	_, err := store.Lookup(context.Background(), object.ObjectKey("../x"))
	if !object.IsUnsafePath(err) {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
}
