package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/ports/storage"
)

// SourceStore — filesystem-реализация storage.SourceStore. Читает исходные
// объекты из root. Это read-only адаптер: не реализует Publish/Delete.
type SourceStore struct {
	root string
}

// NewSourceStore создаёт SourceStore в root.
func NewSourceStore(root string) (*SourceStore, error) {
	if root == "" {
		return nil, fmt.Errorf("fs: source store: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fs: source store: %w", err)
	}
	return &SourceStore{root: abs}, nil
}

// Root возвращает абсолютный корень хранилища.
func (s *SourceStore) Root() string { return s.root }

func (s *SourceStore) path(key object.ObjectKey) (string, error) {
	rel, err := cleanRel(s.root, key)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, rel), nil
}

// Lookup возвращает метаданные исходного объекта.
func (s *SourceStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	full, err := s.path(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	if err := walkComponentsNotSymlink(s.root, full); err != nil {
		return object.ObjectMetadata{}, unsafeErr(key, err)
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
		}
		return object.ObjectMetadata{}, err
	}
	if info.IsDir() {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return metaFromInfo(key, info), nil
}

// Open открывает поток исходного объекта.
func (s *SourceStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	rel, err := cleanRelAbs(s.root, key)
	if err != nil {
		return nil, unsafe(key, err)
	}
	full := filepath.Join(s.root, rel)
	if err := walkComponentsNotSymlink(s.root, full); err != nil {
		return nil, unsafe(key, err)
	}
	f, err := secureOpenFile(s.root, rel, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &object.NotFoundError{Key: key}
		}
		if isSymlinkErr(err) {
			return nil, unsafe(key, err)
		}
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, &object.NotFoundError{Key: key}
	}
	return &fileArtifact{file: f, meta: metaFromInfo(key, info)}, nil
}

var _ storage.SourceStore = (*SourceStore)(nil)

// ResultStore — filesystem-реализация storage.ResultStore.
//
// Атомарная публикация: temp-файл в том же каталоге + fsync + rename.
// NoOverwrite реализуется атомарно через link(2) (hard link temp → target),
// что исключает race проверки существования и записи.
//
// Permissions: temp создаётся с 0o600 (безопасно для промежуточного
// содержимого); перед rename Chmod 0o644, чтобы финальный файл был
// читаем. (umask применяется только к CreateTemp; Chmod явный.)
//
// Durability: fsync temp перед rename и fsync каталога после rename
// (где поддерживается платформой).
type ResultStore struct {
	root  string
	cache *cacheManager
}

// ResultStoreOptions — опции ResultStore (квота/eviction).
type ResultStoreOptions struct {
	// Cache — параметры квоты (0 по умолчанию = без лимитов).
	Cache CacheOptions
}

// NewResultStore создаёт ResultStore в root без лимитов.
func NewResultStore(root string) (*ResultStore, error) {
	return NewResultStoreWithOptions(root, ResultStoreOptions{})
}

// NewResultStoreWithOptions создаёт ResultStore с квотой/eviction.
func NewResultStoreWithOptions(root string, opts ResultStoreOptions) (*ResultStore, error) {
	if root == "" {
		return nil, fmt.Errorf("fs: result store: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fs: result store: %w", err)
	}
	r := &ResultStore{root: abs}
	cm, err := newCacheManager(opts.Cache, r.deleteFile)
	if err != nil {
		return nil, err
	}
	r.cache = cm
	r.warmCache()
	return r, nil
}

func (r *ResultStore) warmCache() {
	files := make([]storedFile, 0, 64)
	_ = filepath.Walk(r.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		// Каталог метаданных sidecar-файлов пропускается ЦЕЛИКОМ
		// (filepath.SkipDir): meta-файлы не должны попадать в LRU-таблицу
		// квоты и становиться целями eviction.
		if info.IsDir() {
			if info.Name() == reservedSegment {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(r.root, path)
		if relErr != nil {
			return nil
		}
		files = append(files, storedFile{
			key:     object.ObjectKey(filepath.ToSlash(rel)),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		return nil
	})
	r.cache.restore(files)
	// Применяем лимиты сразу после старта: warm-заполненная таблица может
	// превышать MaxBytes/MaxObjects (например, после рестарта с меньшим
	// лимитом). evictIfNeeded приводит кэш к лимитам до первой публикации.
	_, _ = r.cache.evictIfNeeded()
}

// Root возвращает абсолютный корень хранилища.
func (r *ResultStore) Root() string { return r.root }

func (r *ResultStore) path(key object.ObjectKey) (string, error) {
	rel, err := cleanRel(r.root, key)
	if err != nil {
		return "", err
	}
	return filepath.Join(r.root, rel), nil
}

// Lookup возвращает метаданные результата.
func (r *ResultStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	full, err := r.path(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return object.ObjectMetadata{}, unsafe(key, err)
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
		}
		return object.ObjectMetadata{}, err
	}
	if info.IsDir() {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	r.cache.touch(key)
	return metaFromInfo(key, info), nil
}

// Open открывает поток результата.
func (r *ResultStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	rel, err := cleanRelAbs(r.root, key)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(r.root, rel)
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return nil, unsafe(key, err)
	}
	f, err := secureOpenFile(r.root, rel, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &object.NotFoundError{Key: key}
		}
		if isSymlinkErr(err) {
			return nil, unsafe(key, err)
		}
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, &object.NotFoundError{Key: key}
	}
	r.cache.touch(key)
	return &fileArtifact{file: f, meta: metaFromInfo(key, info)}, nil
}

// ReadStream открывает одноразовый поток результата для последовательной
// отдачи. Для FS это тот же файл, что и Open, но без требования Seek.
func (r *ResultStore) ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error) {
	rel, err := cleanRelAbs(r.root, key)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(r.root, rel)
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return nil, unsafe(key, err)
	}
	f, err := secureOpenFile(r.root, rel, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &object.NotFoundError{Key: key}
		}
		if isSymlinkErr(err) {
			return nil, unsafe(key, err)
		}
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, &object.NotFoundError{Key: key}
	}
	r.cache.touch(key)
	return &fileStream{file: f, meta: metaFromInfo(key, info)}, nil
}

// Publish атомарно публикует содержимое r под ключом key.
func (r *ResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) (err error) {
	if ctx != nil {
		if cErr := ctx.Err(); cErr != nil {
			return cErr
		}
	}
	full, err := r.path(key)
	if err != nil {
		return err
	}
	// Проверка промежуточных каталогов до создания (best-effort).
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return unsafe(key, err)
	}

	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fs: mkdir result dir: %w", err)
	}
	// Проверка после создания (защита от symlink-подмены в окне Mkdir).
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return unsafe(key, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(full)+"-*")
	if err != nil {
		return fmt.Errorf("fs: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	written, err := writeTemp(tmp, src, ctx)
	if err != nil {
		if qErr := quotaErr(err); qErr != nil {
			return qErr
		}
		// Отмена контекста во время копирования — возвращаем как есть.
		if ctx != nil {
			if cErr := ctx.Err(); cErr != nil {
				return cErr
			}
		}
		return fmt.Errorf("fs: write temp: %w", err)
	}
	if ctx != nil {
		if cErr := ctx.Err(); cErr != nil {
			return cErr
		}
	}
	if err = tmp.Sync(); err != nil {
		if qErr := quotaErr(err); qErr != nil {
			return qErr
		}
		return fmt.Errorf("fs: sync temp: %w", err)
	}
	if err = tmp.Close(); err != nil {
		if qErr := quotaErr(err); qErr != nil {
			return qErr
		}
		return fmt.Errorf("fs: close temp: %w", err)
	}
	if err = os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("fs: chmod temp: %w", err)
	}

	// Жёсткая квота: резервируем записываемые байты до commit.
	if rErr := r.cache.reserveBytes(written); rErr != nil {
		return rErr
	}

	if opts.NoOverwrite {
		// Атомарный link: не перезаписывает существующий файл.
		lnErr := os.Link(tmpName, full)
		if lnErr != nil {
			r.cache.releaseBytes(written)
			_ = os.Remove(tmpName)
			if isExist(lnErr) {
				return &object.ConflictError{Key: key}
			}
			return fmt.Errorf("fs: link temp: %w", lnErr)
		}
		_ = os.Remove(tmpName)
		if err := fsyncDir(dir); err != nil {
			// fsync каталога не удался — запись может быть не durable.
			// Возвращаем ошибку, чтобы вызывающий знал о риске потери.
			r.cache.releaseBytes(written)
			return fmt.Errorf("fs: fsync dir: %w", err)
		}
		r.cache.releaseBytes(written)
		r.cache.recordPublish(key, written)
		_, _ = r.cache.evictIfNeeded()
		return nil
	}

	// Атомарный replace через rename (платформо-специфичный).
	if err = renameReplace(tmpName, full); err != nil {
		r.cache.releaseBytes(written)
		_ = os.Remove(tmpName)
		return fmt.Errorf("fs: rename temp: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		// fsync каталога не удался — запись может быть не durable.
		r.cache.releaseBytes(written)
		return fmt.Errorf("fs: fsync dir: %w", err)
	}
	r.cache.releaseBytes(written)
	r.cache.recordPublish(key, written)
	_, _ = r.cache.evictIfNeeded()
	return nil
}

// quotaErr маппит ошибку записи в типизированную object.ErrQuota, если она
// вызвана физическим лимитом диска (ENOSPC) или превышением квоты (EDQUOT).
// Цели сравнения платформенно-специфичны (см. quota_unix.go,
// quota_windows.go, quota_other.go): на платформах с этими errno
// распознаются реальные ошибки ОС, на остальных (например plan9)
// используется нейтральные заглушки. Возвращает nil, если ошибка не
// относится к квоте.
func quotaErr(err error) error {
	if errors.Is(err, errNoSpace) {
		return &object.QuotaError{Err: err, Reason: "no space left on device"}
	}
	if errors.Is(err, errQuotaExceeded) {
		return &object.QuotaError{Err: err, Reason: "disk quota exceeded"}
	}
	return nil
}

// deleteFile удаляет файл результата по ключу (используется в eviction).
func (r *ResultStore) deleteFile(key object.ObjectKey) error {
	full, err := r.path(key)
	if err != nil {
		return err
	}
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return err
	}
	err = os.Remove(full)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Delete удаляет объект по ключу. Идемпотентно.
func (r *ResultStore) Delete(ctx context.Context, key object.ObjectKey) error {
	full, err := r.path(key)
	if err != nil {
		return err
	}
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return unsafe(key, err)
	}
	err = os.Remove(full)
	if err != nil {
		if os.IsNotExist(err) {
			r.cache.remove(key)
			return nil
		}
		return err
	}
	// Файл уже удалён: учёт квоты не должен расходиться с диском, поэтому
	// запись из LRU-таблицы снимаем независимо от результата fsync.
	r.cache.remove(key)
	if err := fsyncDir(filepath.Dir(full)); err != nil {
		// fsync каталога не удался — удаление может быть не durable.
		return fmt.Errorf("fs: fsync dir: %w", err)
	}
	return nil
}

// DeleteByPrefix удаляет все ключи, начинающиеся с prefix (с границей '/'),
// одним каталогом/пакетом. Реализует опциональный storage.PrefixDeleter
// (используется admin DELETE по исходнику).
//
// Каталог ассетов исходника содержит только производные ассеты, поэтому
// удаляется целиком через os.RemoveAll. Перед удалением число файлов
// подсчитывается через filepath.Walk (для возврата). После удаления
// синхронизируется LRU-кэш/квота (removePrefix) и выполняется fsync
// родительского каталога по аналогии с Delete. Идемпотентно: если каталога
// нет — возвращает (0, nil).
//
// Sidecar-метаданные (<metaRoot>/<каталог ассета>/.meta.json) при удалении
// каталога ассетов НЕ чистятся здесь: metadata.Store не связан с этим
// хранилищем (sidecar может лежать в отдельном metaRoot). Удаление sidecar
// выполняется на уровне adminsvc.DeleteBySource через metadata.Store.Delete.
// При явном metadata.dir (вне result-каталога) sidecar лежат вне удаляемого
// каталога ассетов, а префикс не может указывать на зарезервированный
// сегмент .meta (см. cleanRel), поэтому удаление не затрагивает метаданные
// чужих ассетов.
func (r *ResultStore) DeleteByPrefix(ctx context.Context, prefix object.ObjectKey) (int64, error) {
	pre := string(prefix)
	pre = strings.Trim(pre, "/")
	if pre == "" {
		return 0, fmt.Errorf("fs: delete by prefix: empty prefix")
	}
	// Префикс трактуется как каталог: гарантируем завершающий '/', чтобы
	// граница совпадала с каталогом ассетов (см. hasPrefixBoundary).
	dirPrefix := pre
	if !strings.HasSuffix(dirPrefix, "/") {
		dirPrefix += "/"
	}
	rel, err := cleanRel(r.root, object.ObjectKey(dirPrefix))
	if err != nil {
		return 0, unsafe(object.ObjectKey(dirPrefix), err)
	}
	full := filepath.Join(r.root, rel)
	if err := walkComponentsNotSymlink(r.root, full); err != nil {
		return 0, unsafe(object.ObjectKey(dirPrefix), err)
	}

	// Подсчитываем число файлов до удаления (для возврата).
	var count int64
	err = filepath.Walk(full, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if ctx != nil {
			if cErr := ctx.Err(); cErr != nil {
				return cErr
			}
		}
		if info.IsDir() {
			return nil
		}
		count++
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("fs: delete by prefix: count: %w", err)
	}
	if count == 0 {
		// Каталога нет или он пуст — идемпотентно.
		return 0, nil
	}

	if err := os.RemoveAll(full); err != nil {
		return 0, fmt.Errorf("fs: delete by prefix: remove: %w", err)
	}
	// Синхронизируем учёт квоты: снимаем все записи с данным префиксом.
	r.cache.removePrefix(dirPrefix)
	if err := fsyncDir(filepath.Dir(full)); err != nil {
		return 0, fmt.Errorf("fs: delete by prefix: fsync dir: %w", err)
	}
	return count, nil
}

// Stats возвращает статистику из in-memory учёта кэша. Не выполняет
// filepath.Walk по root (дорого на каждый вызов). temp/.meta файлы не
// публикуются через recordPublish, поэтому не учитываются — это корректно.
func (r *ResultStore) Stats(ctx context.Context) (object.StoreStats, error) {
	bytes, objects := r.cache.snapshot()
	return object.StoreStats{Objects: objects, TotalBytes: bytes}, nil
}

// CacheStats возвращает статистику кэша (с числом evictions).
func (r *ResultStore) CacheStats(ctx context.Context) (CacheStats, error) {
	bytes, objects := r.cache.snapshot()
	return CacheStats{
		Objects:    objects,
		TotalBytes: bytes,
		Evicted:    r.cache.evictedCount(),
	}, nil
}

// List возвращает ключи результатов, начинающиеся с prefix. Реализует
// опциональный storage.Lister (используется admin DELETE по исходнику).
//
// Каталог результатов обходится рекурсивно; файлы метаданных (>/.meta) и
// временные файлы публикации исключаются, ключи нормализуются в canonical
// форму (filepath.ToSlash). Пустой prefix означает «все объекты».
func (r *ResultStore) List(ctx context.Context, prefix object.ObjectKey) ([]object.ObjectKey, error) {
	pre := string(prefix)
	// Нормализуем префикс: без ведущих "/", "//" не допускаем.
	pre = strings.Trim(pre, "/")
	var keys []object.ObjectKey
	err := filepath.Walk(r.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if ctx != nil {
			if cErr := ctx.Err(); cErr != nil {
				return cErr
			}
		}
		if info.IsDir() {
			if info.Name() == reservedSegment {
				return filepath.SkipDir
			}
			return nil
		}
		// Временные файлы публикации пропускаем.
		if strings.HasPrefix(info.Name(), reservedSegmentPrefix) {
			return nil
		}
		rel, relErr := filepath.Rel(r.root, path)
		if relErr != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if pre != "" && !hasPrefixBoundary(key, pre) {
			return nil
		}
		keys = append(keys, object.ObjectKey(key))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fs: list: %w", err)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys, nil
}

var _ storage.ResultStore = (*ResultStore)(nil)
var _ storage.PrefixDeleter = (*ResultStore)(nil)

// metaFromInfo строит ObjectMetadata из os.FileInfo.
func metaFromInfo(key object.ObjectKey, info os.FileInfo) object.ObjectMetadata {
	return object.ObjectMetadata{
		Key:     key,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
}
