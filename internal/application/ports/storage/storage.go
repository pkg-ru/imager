// Package storage определяет platform-independent контракты (порты) для
// хранилищ исходников и результатов. Контракты не зависят от os.File и
// конкретной файловой системы: они оперируют только ObjectKey,
// ObjectMetadata, Artifact и типизированными ошибками из domain/object.
//
// Адаптеры (filesystem, S3, external disk, FTP, HTTP) реализуют эти
// интерфейсы. FTP и FTPS реализуют и SourceStore, и ResultStore; HTTP —
// только SourceStore (read-only).
package storage

import (
	"context"
	"io"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// SourceStore открывает исходные объекты по ключу. Реализации могут быть
// локальными (filesystem, external disk) или удалёнными (S3, FTP, HTTP).
//
// Capability differences:
//   - HTTP является read-only источником: он реализует только Open/Lookup
//     (HEAD/GET) и не реализует ResultStore.
//   - S3 поддерживает conditional reads (If-Match/If-None-Match) и strong
//     ETag; filesystem — только слабый идентификатор по modtime+size.
type SourceStore interface {
	// Lookup возвращает метаданные исходного объекта по ключу.
	// Если объект отсутствует — возвращается object.ErrNotFound.
	Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error)

	// Open открывает поток исходного объекта по ключу.
	// Если объект отсутствует — возвращается object.ErrNotFound.
	Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error)
}

// ResultStore хранит сгенерированные ассеты (кэш). В отличие от SourceStore,
// ResultStore поддерживает атомарную публикацию, удаление и статистику.
//
// Capability differences:
//   - filesystem реализует атомарность через temp-файл + rename в том же
//     каталоге; S3 — через conditional PUT (If-None-Match) и multipart.
//   - external disk может не поддерживать атомарный rename между каталогами;
//     адаптер должен документировать это ограничение.
//   - HTTP не реализует ResultStore: он является read-only источником.
type ResultStore interface {
	// Lookup возвращает метаданные результата по ключу.
	// Если объект отсутствует — возвращается object.ErrNotFound.
	Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error)

	// Open открывает перематываемый поток результата по ключу.
	// Если объект отсутствует — возвращается object.ErrNotFound.
	Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error)

	// ReadStream открывает одноразовый поток результата по ключу для
	// последовательной отдачи (например, клиенту) без материализации.
	// В отличие от Open, возвращаемый object.Stream не перематываем и
	// читается от начала до конца. Для remote-хранилищ стримит напрямую
	// из хранилища, не удерживая данные в памяти/на диске.
	// Если объект отсутствует — возвращается object.ErrNotFound.
	ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error)

	// Publish атомарно публикует содержимое r под ключом key.
	// Реализация обязана гарантировать, что читатели либо видят полностью
	// записанный объект, либо не видят его вовсе (temp + rename / conditional
	// PUT). При NoOverwrite и существующем объекте возвращается
	// object.ErrConflict.
	Publish(ctx context.Context, key object.ObjectKey, r io.Reader, opts object.PublishOptions) error

	// Delete удаляет объект по ключу. Удаление отсутствующего объекта не
	// является ошибкой (идемпотентно). Возвращает ErrNotFound только если
	// адаптер явно документирует строгий режим.
	Delete(ctx context.Context, key object.ObjectKey) error

	// Stats возвращает агрегированную статистику хранилища (число объектов,
	// суммарный размер). Может быть дорогой операцией для удалённых хранилищ.
	Stats(ctx context.Context) (object.StoreStats, error)
}

// Publisher — узкий интерфейс атомарной публикации, выделенный отдельно,
// чтобы адаптеры, которые не поддерживают удаление/статистику (например,
// write-only sink), могли реализовать только публикацию.
type Publisher interface {
	// Publish атомарно публикует содержимое r под ключом key.
	Publish(ctx context.Context, key object.ObjectKey, r io.Reader, opts object.PublishOptions) error
}

// Lister — ОПЦИОНАЛЬНЫЙ интерфейс перечисления объектов result-хранилища по
// префиксу ключа (например, для admin DELETE /admin/assets/delete по
// исходнику). Не является частью обязательного контракта ResultStore:
// адаптеры без List не поддерживают режим удаления «по исходнику» (501).
//
// Реализации обязаны возвращать ключи, начинающиеся с prefix (без учёта
// зарезервированных внутренних объектов, если адаптер их хранит).
type Lister interface {
	// List возвращает ключи объектов хранилища, начинающиеся с prefix.
	List(ctx context.Context, prefix object.ObjectKey) ([]object.ObjectKey, error)
}

// ArtifactFactory — фабрика открытых объектов, используемая адаптерами для
// создания Artifact из своих ресурсов. Позволяет переиспользовать логику
// метаданных между адаптерами.
type ArtifactFactory interface {
	// NewArtifact создаёт Artifact поверх открытого ресурса.
	NewArtifact(meta object.ObjectMetadata, closer io.Closer, seeker io.Seeker) object.Artifact
}
