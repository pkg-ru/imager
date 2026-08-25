package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg-ru/imager/coordination/singleflight"
	"github.com/pkg-ru/imager/ports/metadata"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/object"
)

// Ограничения sidecar-хранилища:
// максимум элементов на срез проверяется в filemeta.Validate, здесь —
// лимит размера файла при чтении (защита от аномальных/подменённых файлов).
const (
	// maxMetaFileBytes — максимальный размер sidecar-файла при чтении (256 KiB).
	maxMetaFileBytes = 256 << 10
	// metaTempPrefix — префикс temp-файлов записи; попадает под уборку
	// существующего janitor (префикс ".tmp-").
	metaTempPrefix = ".tmp-meta-"
	// metaFileExt — суффикс sidecar-файла.
	metaFileExt = ".json"
	// metaFlightPrefix — префикс ключей keyed singleflight для Update.
	metaFlightPrefix = "meta:"
)

// MetadataStore — filesystem-реализация metadata.Store: локальное
// sidecar-хранилище метаданных родительских файлов
// (`<metaRoot>/<srcKey>.json`.
//
// metaRoot — корень sidecar-хранилища, задаётся ЯВНО (metadata.dir из
// конфигурации, либо по умолчанию `<локальный result-каталог>/.meta`).
// Метаданные ВСЕГДА хранятся локально по этому пути, независимо от того,
// какие хранилища используются для source/result (fs/S3/SFTP/FTP/HTTP).
//
// Безопасность пути: ключ валидируется тем же механизмом cleanRelAbs, что и
// публичные ключи хранилища (запрещены "..", обратный слеш, NUL,
// зарезервированный сегмент ".meta", префикс ".tmp-" и т.д.), поэтому ни один
// публичный ключ не может адресовать файлы метаданных, а сам store не может
// выйти за пределы metaRoot.
//
// Атомарность: запись через temp-файл (.tmp-meta-*) в том же каталоге →
// Sync → Chmod 0644 → renameReplace → fsyncDir; чтение через secureOpenFile
// без следования symlink. Читатель видит либо старую, либо новую версию файла.
//
// Конкурентность: Update выполняется под внутренним keyed singleflight по
// ключу "meta:<srcKey>" (read-modify-write); Load/Save вне групп допустимы —
// атомарный rename гарантирует целостность прочитанного.
type MetadataStore struct {
	metaRoot string
	flights  *singleflight.Group
}

var _ metadata.Store = (*MetadataStore)(nil)

// NewMetadataStore создаёт MetadataStore с корнем метаданных metaRoot.
// Каталог создаётся лениво при первой записи.
func NewMetadataStore(metaRoot string) (*MetadataStore, error) {
	if metaRoot == "" {
		return nil, fmt.Errorf("fs: metadata store: empty meta root")
	}
	abs, err := filepath.Abs(metaRoot)
	if err != nil {
		return nil, fmt.Errorf("fs: metadata store: %w", err)
	}
	return &MetadataStore{
		metaRoot: abs,
		// MaxKeyLen с запасом покрывает maxPathLen, чтобы длинные (но
		// валидные) ключи не отклонялись singleflight'ом раньше проверки пути.
		flights: singleflight.New(singleflight.Options{MaxKeyLen: maxPathLen + len(metaFlightPrefix)}),
	}, nil
}

// MetaRoot возвращает абсолютный корень sidecar-хранилища.
func (s *MetadataStore) MetaRoot() string { return s.metaRoot }

// metaPath преобразует srcKey в полный путь sidecar-файла
// <metaRoot>/<srcKey>.json с полной валидацией ключа (containment).
func (s *MetadataStore) metaPath(srcKey string) (string, error) {
	rel, err := cleanRelAbs(s.metaRoot, objectKeyFromString(srcKey))
	if err != nil {
		return "", err
	}
	// Суффикс .json не может породить ".." или завершающую точку
	// (последний символ имени — всегда 'n').
	full := filepath.Join(s.metaRoot, rel+metaFileExt)
	if !within(s.metaRoot, full) {
		return "", errUnsafeContainment()
	}
	return full, nil
}

// Load возвращает метаданные родителя или (nil, nil), если файла нет.
// Никогда не создаёт файл.
func (s *MetadataStore) Load(_ context.Context, srcKey string) (*filemeta.FileMetadata, error) {
	full, err := s.metaPath(srcKey)
	if err != nil {
		return nil, err
	}
	if err := walkComponentsNotSymlink(s.metaRoot, full); err != nil {
		return nil, fmt.Errorf("fs: metadata load %q: %w", srcKey, err)
	}
	rel, err := filepath.Rel(s.metaRoot, full)
	if err != nil {
		return nil, errUnsafeContainment()
	}
	f, err := secureOpenFile(s.metaRoot, rel, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			// Ленивое создание: отсутствие sidecar — норма.
			return nil, nil
		}
		if isSymlinkErr(err) {
			return nil, fmt.Errorf("fs: metadata load %q: %w", srcKey, err)
		}
		return nil, fmt.Errorf("fs: metadata open %q: %w", srcKey, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("fs: metadata stat %q: %w", srcKey, err)
	}
	if info.IsDir() {
		return nil, nil // каталог вместо файла трактуем как отсутствие данных
	}
	if info.Size() > maxMetaFileBytes {
		return nil, fmt.Errorf("%w: metadata file %q is %d bytes, limit is %d",
			filemeta.ErrCorrupt, srcKey, info.Size(), maxMetaFileBytes)
	}

	// Читаем с лимитом +1 байт, чтобы поймать файл, доросший до лимита
	// между Stat и чтением (гонка с параллельной записью).
	data, err := io.ReadAll(io.LimitReader(f, maxMetaFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fs: metadata read %q: %w", srcKey, err)
	}
	if len(data) > maxMetaFileBytes {
		return nil, fmt.Errorf("%w: metadata file %q exceeds %d bytes",
			filemeta.ErrCorrupt, srcKey, maxMetaFileBytes)
	}

	m, err := decodeMetadata(data)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata %q: %v", filemeta.ErrCorrupt, srcKey, err)
	}
	if m.SchemaVersion > filemeta.CurrentSchemaVersion {
		// Чужие данные более новой версии сервиса: не читаем и НЕ перезаписываем.
		return nil, fmt.Errorf("%w: schema_version %d > supported %d",
			filemeta.ErrSchemaTooNew, m.SchemaVersion, filemeta.CurrentSchemaVersion)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("fs: metadata %q: %w", srcKey, err)
	}
	return m, nil
}

// Save атомарно записывает метаданные (создаёт каталоги и файл при
// необходимости). CreatedAt/UpdatedAt проставляются, если нулевые;
// SchemaVersion нормализуется к текущей.
func (s *MetadataStore) Save(_ context.Context, srcKey string, m *filemeta.FileMetadata) (err error) {
	if m == nil {
		return fmt.Errorf("fs: metadata save %q: nil metadata", srcKey)
	}
	full, err := s.metaPath(srcKey)
	if err != nil {
		return err
	}
	// Нормализация перед записью.
	m = normalizeForSave(m)
	if err := m.Validate(); err != nil {
		return fmt.Errorf("fs: metadata save %q: %w", srcKey, err)
	}

	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fs: metadata mkdir: %w", err)
	}
	// Проверка после создания (защита от symlink-подмены в окне Mkdir),
	// как в Publish.
	if err := walkComponentsNotSymlink(s.metaRoot, full); err != nil {
		return fmt.Errorf("fs: metadata save %q: %w", srcKey, err)
	}

	tmp, err := os.CreateTemp(dir, metaTempPrefix+"*")
	if err != nil {
		return fmt.Errorf("fs: metadata create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("fs: metadata marshal: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("fs: metadata write temp: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("fs: metadata sync temp: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("fs: metadata close temp: %w", err)
	}
	if err = os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("fs: metadata chmod temp: %w", err)
	}
	if err = renameReplace(tmpName, full); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("fs: metadata rename temp: %w", err)
	}
	if err = fsyncDir(dir); err != nil {
		// fsync каталога не удался — запись может быть не durable.
		return fmt.Errorf("fs: metadata fsync dir: %w", err)
	}
	return nil
}

// Update выполняет атомарный read-modify-write под keyed-блокировкой
// "meta:<srcKey>" (singleflight.Acquire: последовательный доступ, а не
// дедупликация — каждый конкурентный вызов обязан применить свою fn,
// иначе обновления терялись бы). При changed=false файл не создаётся
// (ленивое создание).
func (s *MetadataStore) Update(ctx context.Context, srcKey string, fn metadata.UpdateFn) error {
	key := objectKeyFromString(metaFlightPrefix + srcKey)
	unlock, err := s.flights.Acquire(ctx, key)
	if err != nil {
		return fmt.Errorf("fs: metadata update %q: acquire: %w", srcKey, err)
	}
	defer unlock()
	return s.updateLocked(ctx, srcKey, fn)
}

// updateLocked — тело Update: вызывается только под keyed-блокировкой.
func (s *MetadataStore) updateLocked(ctx context.Context, srcKey string, fn metadata.UpdateFn) error {
	current, err := s.Load(ctx, srcKey)
	if err != nil {
		// ErrSchemaTooNew не трогаем: чужие данные более новой версии
		// перезаписывать запрещено.
		return err
	}
	if current == nil {
		current = filemeta.NewFileMetadata()
	} else {
		// Работаем на копии: fn может мутировать вход свободно.
		current = current.Clone()
	}
	// C4: паника в UpdateFn (например, ошибка реализации вызывающего fn)
	// не должна ронять сервис — перехватываем и возвращаем как ошибку.
	changed, err := callUpdateFn(fn, current)
	if err != nil {
		return fmt.Errorf("fs: metadata update %q: %w", srcKey, err)
	}
	if !changed {
		// Ленивое создание: писать нечего — файл не создаётся.
		return nil
	}
	// Семантика updated_at: момент последней успешной записи;
	// CreatedAt сохраняется из загруженного файла.
	current.UpdatedAt = time.Now().UTC()
	return s.Save(ctx, srcKey, current)
}

// decodeMetadata разбирает JSON sidecar. Повреждённый JSON даёт ошибку,
// которую вызывающий код оборачивает в filemeta.ErrCorrupt.
func decodeMetadata(data []byte) (*filemeta.FileMetadata, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var m filemeta.FileMetadata
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// normalizeForSave приводит метаданные к канонической форме для записи:
// текущая версия схемы, UTC-время, заполнение нулевых временных полей.
func normalizeForSave(m *filemeta.FileMetadata) *filemeta.FileMetadata {
	out := m.Clone()
	out.SchemaVersion = filemeta.CurrentSchemaVersion
	now := time.Now().UTC()
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.CreatedAt = out.CreatedAt.UTC()
	if out.UpdatedAt.IsZero() {
		out.UpdatedAt = now
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out
}

// objectKeyFromString адаптирует строковый ключ к object.ObjectKey для
// переиспользования утилит пакета (cleanRelAbs, singleflight).
func objectKeyFromString(k string) object.ObjectKey { return object.ObjectKey(k) }

// callUpdateFn вызывает UpdateFn, перехватывая панику (C4): паника в fn
// (например, ошибка реализации вызывающего) не должна ронять сервис или
// оставлять keyed-блокировку навсегда — unlock выполняется в defer Update.
func callUpdateFn(fn metadata.UpdateFn, m *filemeta.FileMetadata) (changed bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			changed = false
			err = fmt.Errorf("panic in metadata update fn: %v", r)
		}
	}()
	return fn(m)
}
