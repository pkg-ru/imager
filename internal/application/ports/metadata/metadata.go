// Package metadata определяет platform-independent контракт (порт)
// ЛОКАЛЬНОГО sidecar-хранилища метаданных родительских файлов
// (docs/METADATA_STORE.md, раздел 6.2).
//
// Реализация обязана быть безопасной для конкурентного использования в одном
// процессе. Ошибки порта — доменные sentinel'ы из filemeta (ErrNotFound,
// ErrCorrupt, ErrSchemaTooNew) плюс ошибки ввода-вывода реализации.
package metadata

import (
	"context"

	"github.com/pkg-ru/imager/internal/domain/filemeta"
)

// UpdateFn — функция модификации метаданных для Store.Update.
// Возвращает changed=true, если метаданные изменены и их нужно записать;
// changed=false означает «писать не нужно» — файл не создаётся
// (механизм ленивого создания, T4 дизайн-дока).
type UpdateFn func(*filemeta.FileMetadata) (changed bool, err error)

// Store — локальное sidecar-хранилище метаданных родительских файлов.
//
// Расположение и формат файла скрыты за интерфейсом: реализация знает про
// `<resultRoot>/.meta/<srcKey>.json`, вызывающий код оперирует только ключом
// родителя srcKey.
type Store interface {
	// Load возвращает метаданные родителя по ключу srcKey.
	//
	// Семантика:
	//   - файл отсутствует            → (nil, nil) — ленивое создание:
	//     отсутствие sidecar является нормой и никогда не создаёт файл;
	//   - schema_version > текущей    → filemeta.ErrSchemaTooNew;
	//   - битый JSON / IO при чтении  → filemeta.ErrCorrupt.
	Load(ctx context.Context, srcKey string) (*filemeta.FileMetadata, error)

	// Save атомарно записывает метаданные (создаёт каталоги и файл при
	// необходимости). CreatedAt/UpdatedAt проставляются реализацией, если
	// нулевые; SchemaVersion нормализуется к текущей.
	Save(ctx context.Context, srcKey string, m *filemeta.FileMetadata) error

	// Update выполняет атомарный read-modify-write под внутренним per-key
	// lock (keyed singleflight). fn получает текущие метаданные (или свежий
	// пустой объект с текущей версией схемы, если файла нет) и возвращает
	// changed=false, если писать не нужно — тогда файл не создаётся.
	Update(ctx context.Context, srcKey string, fn UpdateFn) error
}
