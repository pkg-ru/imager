package fs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/filemeta"
)

func newTestMetaStore(t *testing.T) (*MetadataStore, string) {
	t.Helper()
	root := t.TempDir()
	// Новая семантика: конструктор принимает КОРЕНЬ МЕТАДАННЫХ напрямую
	// (metadata.dir явно, либо дефолт <resultRoot> на уровне DI).
	metaRoot := filepath.Join(root, reservedSegment)
	s, err := NewMetadataStore(metaRoot)
	if err != nil {
		t.Fatalf("NewMetadataStore: %v", err)
	}
	return s, root
}

// TestNewMetadataStoreUsesExplicitRoot — NewMetadataStore принимает корень
// метаданных НАПРЯМУЮ (никакого добавления .meta внутри): sidecar пишется
// строго <metaRoot>/<каталог ассета>/.meta.json, каталог — именно заданный.
func TestNewMetadataStoreUsesExplicitRoot(t *testing.T) {
	ctx := context.Background()
	// Произвольный НЕ-дефолтный локальный путь (metadata.dir из конфигурации):
	// не внутри result-каталога и не .meta-вложенный.
	metaRoot := t.TempDir()
	s, err := NewMetadataStore(metaRoot)
	if err != nil {
		t.Fatalf("NewMetadataStore: %v", err)
	}
	if got := s.MetaRoot(); got != metaRoot {
		t.Fatalf("MetaRoot() = %q, want %q (no nested .meta added)", got, metaRoot)
	}
	m := filemeta.NewFileMetadata()
	m.Faces = []filemeta.FaceInfo{}
	// Ключ ассета "photos/cat.jpg" → каталог "photos" → файл .meta.json.
	if err := s.Save(ctx, "photos/cat.jpg", m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	full := filepath.Join(metaRoot, "photos", metaFileName)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("sidecar not created at explicit metaRoot (%q): %v", full, err)
	}
	// Никакого двойного вложения <metaRoot>/.meta не существует.
	nested := filepath.Join(metaRoot, reservedSegment)
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatalf("unexpected nested %q created; metadata root is used as-is", nested)
	}
}

// TestMetadataLoadLazyCreation — Load отсутствующего файла возвращает
// (nil, nil) и НЕ создаёт ни каталог .meta, ни файл (ленивое создание).
func TestMetadataLoadLazyCreation(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	m, err := s.Load(ctx, "about.png")
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil metadata for missing file, got %+v", m)
	}
	if _, err := os.Stat(filepath.Join(resultRoot, reservedSegment)); !os.IsNotExist(err) {
		t.Fatalf(".meta directory must not be created by Load: %v", err)
	}
}

// TestMetadataExists — Exists проверяет НАЛИЧИЕ файла без чтения содержимого
// и не создаёт файл/каталог.
func TestMetadataExists(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	// Файла нет → false, каталог не создаётся.
	ok, err := s.Exists(ctx, "photos/cat.jpg")
	if err != nil {
		t.Fatalf("Exists missing: %v", err)
	}
	if ok {
		t.Fatalf("Exists = true for missing file, want false")
	}
	if _, err := os.Stat(filepath.Join(resultRoot, reservedSegment)); !os.IsNotExist(err) {
		t.Fatalf(".meta directory must not be created by Exists: %v", err)
	}

	// После Save → true.
	m := filemeta.NewFileMetadata()
	if err := s.Save(ctx, "photos/cat.jpg", m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ok, err = s.Exists(ctx, "photos/cat.jpg")
	if err != nil {
		t.Fatalf("Exists after save: %v", err)
	}
	if !ok {
		t.Fatalf("Exists = false after Save, want true")
	}
}

// TestMetadataSaveCreatesDirsAndFile — Save создаёт вложенные каталоги и файл
// .meta.json РЯДОМ с ассетом (в каталоге ассета).
func TestMetadataSaveCreatesDirsAndFile(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	m := filemeta.NewFileMetadata()
	m.Faces = []filemeta.FaceInfo{}
	// Ключ ассета "photos/cat.jpg" → каталог "photos" → файл .meta.json.
	if err := s.Save(ctx, "photos/cat.jpg", m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	full := filepath.Join(resultRoot, reservedSegment, "photos", metaFileName)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("sidecar file not created: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("written JSON invalid: %v", err)
	}
	if raw["schema_version"].(float64) != 1 {
		t.Fatalf("schema_version = %v, want 1", raw["schema_version"])
	}
}

// TestMetadataRoundTrip — Save → Load возвращает эквивалентные данные,
// включая различение «нет данных» (nil) и «проверено, пусто» (len==0).
func TestMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestMetaStore(t)

	src := &filemeta.FileMetadata{
		SchemaVersion: filemeta.CurrentSchemaVersion,
		Faces:         []filemeta.FaceInfo{{PixelBox: filemeta.PixelBox{X: 10, Y: 20, Width: 30, Height: 40}, Confidence: 0.97}},
		Objects:       []filemeta.ObjectInfo{{PixelBox: filemeta.PixelBox{X: 1, Y: 2, Width: 3, Height: 4}, Confidence: 0.5, Label: "person"}},
		LargestAIAsset: &filemeta.AIAssetInfo{
			Width: 4000, Height: 3000, Format: "webp", Key: "photos/cat-jpg/x4000@2.webp",
		},
		CreatedAt: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 24, 13, 5, 0, 0, time.UTC),
	}
	if err := s.Save(ctx, "cat.jpg", src); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load(ctx, "cat.jpg")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SchemaVersion != src.SchemaVersion ||
		len(got.Faces) != 1 || got.Faces[0] != src.Faces[0] ||
		len(got.Objects) != 1 || got.Objects[0].Label != "person" ||
		got.LargestAIAsset == nil || *got.LargestAIAsset != *src.LargestAIAsset ||
		!got.CreatedAt.Equal(src.CreatedAt) || !got.UpdatedAt.Equal(src.UpdatedAt) {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", got, src)
	}

	// Пустой непустой-nil срез сохраняет семантику «проверено, пусто».
	empty := filemeta.NewFileMetadata()
	empty.Faces = []filemeta.FaceInfo{}
	if err := s.Save(ctx, "empty.jpg", empty); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	gotEmpty, err := s.Load(ctx, "empty.jpg")
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if gotEmpty.Faces == nil || len(gotEmpty.Faces) != 0 {
		t.Fatalf("empty faces semantics lost: %#v", gotEmpty.Faces)
	}
}

// TestUpdateNoChangeDoesNotCreateFile — Update с changed=false не создаёт файл.
func TestUpdateNoChangeDoesNotCreateFile(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	err := s.Update(ctx, "lazy.png", func(m *filemeta.FileMetadata) (bool, error) {
		if m.SchemaVersion != filemeta.CurrentSchemaVersion {
			t.Errorf("fresh metadata schema = %d, want %d", m.SchemaVersion, filemeta.CurrentSchemaVersion)
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(resultRoot, reservedSegment)); !os.IsNotExist(statErr) {
		t.Fatalf(".meta must not exist after changed=false: %v", statErr)
	}
}

// TestUpdateChangedUpdatesTimestamps — Update с changed=true записывает файл;
// CreatedAt сохраняется из загруженного файла, UpdatedAt обновляется.
func TestUpdateChangedUpdatesTimestamps(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestMetaStore(t)

	first := filemeta.NewFileMetadata()
	first.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first.UpdatedAt = first.CreatedAt
	if err := s.Save(ctx, "ts.png", first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // гарантируем видимый сдвиг времени
	err := s.Update(ctx, "ts.png", func(m *filemeta.FileMetadata) (bool, error) {
		m.Faces = []filemeta.FaceInfo{}
		return true, nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Load(ctx, "ts.png")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed: got %v, want %v", got.CreatedAt, first.CreatedAt)
	}
	if !got.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdatedAt not advanced: got %v, want > %v", got.UpdatedAt, first.UpdatedAt)
	}
	if got.Faces == nil || len(got.Faces) != 0 {
		t.Fatalf("faces not persisted by Update: %#v", got.Faces)
	}
}

// TestUpdateOnMissingFileCreatesIt — Update с changed=true на отсутствующем
// файле лениво создаёт его со свежими метаданными.
func TestUpdateOnMissingFileCreatesIt(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestMetaStore(t)

	err := s.Update(ctx, "new.png", func(m *filemeta.FileMetadata) (bool, error) {
		m.LargestAIAsset = &filemeta.AIAssetInfo{Width: 100, Height: 50, Format: "png", Key: "k"}
		return true, nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Load(ctx, "new.png")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LargestAIAsset == nil || got.LargestAIAsset.Key != "k" {
		t.Fatalf("largest_ai_asset not persisted: %+v", got.LargestAIAsset)
	}
}

// TestConcurrentUpdateRace — конкурентный Update под -race: каждый инкремент
// счётчика доходит до файла, итоговый JSON целен и валиден (нет потерь).
func TestConcurrentUpdateRace(t *testing.T) {
	if testing.Short() {
		t.Skip("race test skipped in short mode")
	}
	ctx := context.Background()
	s, _ := newTestMetaStore(t)

	const workers = 16
	const perWorker = 5

	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				err := s.Update(ctx, "shared.jpg", func(m *filemeta.FileMetadata) (bool, error) {
					m.Objects = append(m.Objects, filemeta.ObjectInfo{
						PixelBox:   filemeta.PixelBox{X: 1, Y: 1, Width: 2, Height: 2},
						Confidence: 0.9,
					})
					return true, nil
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Update error: %v", err)
	}

	got, err := s.Load(ctx, "shared.jpg")
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if len(got.Objects) != workers*perWorker {
		t.Fatalf("lost updates: objects = %d, want %d", len(got.Objects), workers*perWorker)
	}
	// Целостность: файл валидный JSON (Load уже распарсил), версия схемы корректна.
	if got.SchemaVersion != filemeta.CurrentSchemaVersion {
		t.Fatalf("schema_version = %d", got.SchemaVersion)
	}
}

// TestMetadataPathTraversalRejected — traversal-ключи отклоняются:
// "..", абсолютный путь, зарезервированный сегмент ".meta" внутри ключа.
func TestMetadataPathTraversalRejected(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	badKeys := []string{
		"../x",
		"..\\x",
		"a/../b",
		".meta/x",        // сегмент ".meta" первым компонентом
		"photos/.meta/x", // сегмент ".meta" внутри ключа
		".tmp-meta-x",    // зарезервированный префикс temp-файлов
		"a/.tmp-x",       // префикс внутри пути
		"\x00bad",        // NUL-байт
		"",               // пустой ключ
	}
	for _, key := range badKeys {
		if _, err := s.Load(ctx, key); err == nil {
			t.Errorf("Load(%q): expected error, got none", key)
		}
		if err := s.Save(ctx, key, filemeta.NewFileMetadata()); err == nil {
			t.Errorf("Save(%q): expected error, got none", key)
		}
		if err := s.Update(ctx, key, func(*filemeta.FileMetadata) (bool, error) { return true, nil }); err == nil {
			t.Errorf("Update(%q): expected error, got none", key)
		}
	}

	// Ничего не записано за пределами metaRoot.
	metaRoot := filepath.Join(resultRoot, reservedSegment)
	if _, err := os.Stat(metaRoot); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(metaRoot)
		if len(entries) > 0 {
			t.Fatalf("unexpected files in .meta after rejected keys: %v", entries)
		}
	}
	outside := filepath.Join(filepath.Dir(resultRoot), "escaped.json")
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("traversal escaped result root: %v", err)
	}
}

// TestSchemaTooNewNotOverwritten — файл с schema_version=2 даёт
// ErrSchemaTooNew при Load и НЕ перезаписывается через Update.
func TestSchemaTooNewNotOverwritten(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	// Ключ "future.png" без каталога → файл <metaRoot>/.meta.json.
	full := filepath.Join(resultRoot, reservedSegment, metaFileName)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	future := `{"schema_version":2,"created_at":"2026-08-24T13:00:00Z","updated_at":"2026-08-24T13:00:00Z"}`
	if err := os.WriteFile(full, []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := s.Load(ctx, "future.png")
	if !errors.Is(err, filemeta.ErrSchemaTooNew) {
		t.Fatalf("Load: expected ErrSchemaTooNew, got %v", err)
	}

	// Update обязан отказаться от перезаписи чужих данных.
	err = s.Update(ctx, "future.png", func(m *filemeta.FileMetadata) (bool, error) {
		t.Error("fn must not run on ErrSchemaTooNew file")
		return true, nil
	})
	if !errors.Is(err, filemeta.ErrSchemaTooNew) {
		t.Fatalf("Update: expected ErrSchemaTooNew, got %v", err)
	}
	data, readErr := os.ReadFile(full)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(data) != future {
		t.Fatalf("file was overwritten:\n%s", data)
	}
}

// TestCorruptJSONRejected — повреждённый JSON даёт filemeta.ErrCorrupt.
func TestCorruptJSONRejected(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	full := filepath.Join(resultRoot, reservedSegment, metaFileName)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(`{"schema_version":1,"faces":[`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load(ctx, "broken.jpg")
	if !errors.Is(err, filemeta.ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
}

// TestOversizedFileRejected — файл больше 256 KiB отклоняется как ErrCorrupt.
func TestOversizedFileRejected(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	full := filepath.Join(resultRoot, reservedSegment, metaFileName)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	// Валидный JSON размером больше лимита: массив объектов с длинными label.
	objs := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		objs = append(objs, `{"x":1,"y":1,"w":1,"h":1,"confidence":0.5,"label":"`+strings.Repeat("a", 5000)+`"}`)
	}
	big := `{"schema_version":1,"objects":[` + strings.Join(objs, ",") + `]}`
	if len(big) <= maxMetaFileBytes {
		t.Fatalf("test payload too small: %d bytes", len(big))
	}
	if err := os.WriteFile(full, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load(ctx, "big.jpg")
	if !errors.Is(err, filemeta.ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for oversized file, got %v", err)
	}
}

// TestInvalidDomainRejectedOnSave — Save отклоняет метаданные, нарушающие
// доменные инварианты (confidence вне [0,1], отрицательные координаты).
func TestInvalidDomainRejectedOnSave(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestMetaStore(t)

	bad := filemeta.NewFileMetadata()
	bad.Faces = []filemeta.FaceInfo{{PixelBox: filemeta.PixelBox{X: -1, Y: 0, Width: 1, Height: 1}, Confidence: 0.5}}
	if err := s.Save(ctx, "bad.png", bad); !errors.Is(err, filemeta.ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on save, got %v", err)
	}

	badConf := filemeta.NewFileMetadata()
	badConf.Objects = []filemeta.ObjectInfo{{PixelBox: filemeta.PixelBox{Width: 1, Height: 1}, Confidence: 1.5}}
	if err := s.Save(ctx, "bad2.png", badConf); !errors.Is(err, filemeta.ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on confidence, got %v", err)
	}
}

// TestJanitorKeepsSidecarsAndCleansMetaTemps — janitor не удаляет
// .meta/**/.meta.json, но удаляет оставшиеся .tmp-meta-* внутри .meta.
func TestJanitorKeepsSidecarsAndCleansMetaTemps(t *testing.T) {
	resultRoot := t.TempDir()

	sidecarDir := filepath.Join(resultRoot, reservedSegment, "photos")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(sidecarDir, metaFileName)
	if err := os.WriteFile(sidecar, []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	staleTemp := filepath.Join(sidecarDir, ".tmp-meta-123456")
	if err := os.WriteFile(staleTemp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(staleTemp, old, old); err != nil {
		t.Fatal(err)
	}

	j, err := NewJanitor(resultRoot, JanitorOptions{Interval: 0, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := j.CleanTemps()
	if err != nil {
		t.Fatalf("CleanTemps: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only stale temp)", removed)
	}
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("janitor deleted sidecar: %v", err)
	}
	if _, err := os.Stat(staleTemp); !os.IsNotExist(err) {
		t.Fatalf("stale temp survived: %v", err)
	}
}

// TestWarmCacheSkipsMetaDirectory — warmCache не индексирует содержимое
// .meta: sidecar не попадают в LRU-таблицу квоты/eviction.
func TestWarmCacheSkipsMetaDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Готовим результат и sidecar ДО создания store (warmCache идёт в конструкторе).
	resDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(resDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resDir, "asset.webp"), []byte("result-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	metaDir := filepath.Join(root, reservedSegment, "deep")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "parent.png.json"), []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewResultStoreWithOptions(root, ResultStoreOptions{
		Cache: CacheOptions{MaxBytes: 1000, MaxObjects: 100},
	})
	if err != nil {
		t.Fatalf("NewResultStoreWithOptions: %v", err)
	}

	stats, err := store.CacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Objects != 1 || stats.TotalBytes != int64(len("result-data")) {
		t.Fatalf("warm cache counted meta files: %+v", stats)
	}
	// Sidecar-ключ недоступен и через публичные операции.
	if _, err := store.Open(ctx, objectKeyFromString(".meta/deep/parent.png.json")); err == nil {
		t.Fatal("Open on .meta key must fail")
	}
}

// TestMetadataPathFromAssetKey — sidecar лежит РЯДОМ с ассетом в его каталоге:
// <metaRoot>/<каталог ассета>/.meta.json. Ключ ассета (с именем файла) не
// влияет на имя sidecar — всегда .meta.json.
func TestMetadataPathFromAssetKey(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	// Ассет "user/basket/product-1-jpg/thumb.webp" → каталог
	// "user/basket/product-1-jpg" → файл .meta.json.
	assetKey := "user/basket/product-1-jpg/thumb.webp"
	m := filemeta.NewFileMetadata()
	m.Faces = []filemeta.FaceInfo{}
	if err := s.Save(ctx, assetKey, m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	full := filepath.Join(resultRoot, reservedSegment, "user", "basket", "product-1-jpg", metaFileName)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("sidecar not next to asset (%q): %v", full, err)
	}
	// Старый формат <каталог>/<имя ассета>.json не должен существовать.
	old := filepath.Join(resultRoot, reservedSegment, "user", "basket", "product-1-jpg", "thumb.webp.json")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("legacy sidecar path %q must not exist", old)
	}

	// Load по тому же ключу ассета находит данные.
	got, err := s.Load(ctx, assetKey)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || got.Faces == nil || len(got.Faces) != 0 {
		t.Fatalf("Load after Save mismatch: %+v", got)
	}
}

// TestCreatedUnixRoundTrip — created_unix сохраняется и читается через
// Save/Load, а Update не перезаписывает уже записанное время.
func TestCreatedUnixRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestMetaStore(t)

	// Первый Update на отсутствующем файле записывает created_unix.
	err := s.Update(ctx, "asset.webp", func(m *filemeta.FileMetadata) (bool, error) {
		if m.CreatedUnix != 0 {
			t.Fatalf("fresh metadata created_unix = %d, want 0", m.CreatedUnix)
		}
		m.CreatedUnix = 1234567890
		return true, nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Load(ctx, "asset.webp")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CreatedUnix != 1234567890 {
		t.Fatalf("created_unix = %d, want 1234567890", got.CreatedUnix)
	}

	// Повторный Update с changed=false не трогает created_unix.
	err = s.Update(ctx, "asset.webp", func(m *filemeta.FileMetadata) (bool, error) {
		if m.CreatedUnix != 1234567890 {
			t.Fatalf("created_unix changed to %d, want 1234567890", m.CreatedUnix)
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Update no-change: %v", err)
	}
	got2, err := s.Load(ctx, "asset.webp")
	if err != nil {
		t.Fatalf("Load after no-change: %v", err)
	}
	if got2.CreatedUnix != 1234567890 {
		t.Fatalf("created_unix after no-change = %d, want 1234567890", got2.CreatedUnix)
	}
}

// TestMetadataDelete — Delete удаляет sidecar-файл и идемпотентен.
func TestMetadataDelete(t *testing.T) {
	ctx := context.Background()
	s, resultRoot := newTestMetaStore(t)

	// Сохраняем sidecar для ассета в каталоге.
	assetKey := "photos/cat.jpg"
	m := filemeta.NewFileMetadata()
	m.Faces = []filemeta.FaceInfo{}
	if err := s.Save(ctx, assetKey, m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	full := filepath.Join(resultRoot, reservedSegment, "photos", metaFileName)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("sidecar not created (%q): %v", full, err)
	}

	// Delete удаляет файл.
	if err := s.Delete(ctx, assetKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("sidecar should be removed (%q)", full)
	}
	// Load после Delete → (nil, nil).
	got, err := s.Load(ctx, assetKey)
	if err != nil {
		t.Fatalf("Load after Delete: %v", err)
	}
	if got != nil {
		t.Fatalf("Load after Delete = %+v, want nil", got)
	}

	// Повторный Delete идемпотентен.
	if err := s.Delete(ctx, assetKey); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
}
