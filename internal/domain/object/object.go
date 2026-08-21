// Package object определяет platform-independent типы и типизированные
// ошибки, общие для всех storage-адаптеров (filesystem, S3, external disk,
// FTP). Пакет не зависит от os.File и конкретной файловой системы.
package object

import (
	"errors"
	"fmt"
	"time"
)

// Typed-ошибки storage-слоя. Адаптеры обязаны возвращать их (оборачивая
// внутренние ошибки через %w), чтобы application-слой мог принимать решения
// без знания конкретной реализации хранилища.
var (
	// ErrNotFound — объект (исходник или результат) не найден.
	ErrNotFound = errors.New("object not found")
	// ErrConflict — объект уже существует и не может быть перезаписан
	// (например, при publish с флагом NoOverwrite).
	ErrConflict = errors.New("object already exists")
	// ErrQuota — превышена квота хранилища (размер, число объектов) или
	// физический лимит диска (ENOSPC).
	ErrQuota = errors.New("storage quota exceeded")
	// ErrUnsafePath — ключ/путь пытается выйти за границы хранилища через
	// символьную ссылку, junction/reparse point или недопустимый сегмент.
	// Возвращается адаптерами при нарушении root containment.
	ErrUnsafePath = errors.New("unsafe object path")
	// ErrUnavailable — хранилище временно недоступно (сеть, таймаут,
	// отключённый внешний диск). Не является ошибкой отсутствия объекта.
	ErrUnavailable = errors.New("storage unavailable")
	// ErrForbidden — доступ к объекту/хранилищу запрещён (например, S3
	// AccessDenied / HTTP 403). Отличается от ErrUnavailable: это постоянная
	// ошибка авторизации, а не временная недоступность, и ретраи не помогут.
	ErrForbidden = errors.New("storage access forbidden")
)

// IsNotFound сообщает, является ли err типизированной ошибкой ErrNotFound
// (в том числе обёрнутой).
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsConflict сообщает, является ли err типизированной ошибкой ErrConflict.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }

// IsQuota сообщает, является ли err типизированной ошибкой ErrQuota.
func IsQuota(err error) bool { return errors.Is(err, ErrQuota) }

// IsUnavailable сообщает, является ли err типизированной ошибкой ErrUnavailable.
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }

// IsForbidden сообщает, является ли err типизированной ошибкой ErrForbidden.
func IsForbidden(err error) bool { return errors.Is(err, ErrForbidden) }

// IsUnsafePath сообщает, является ли err типизированной ошибкой ErrUnsafePath
// (в том числе обёрнутой).
func IsUnsafePath(err error) bool { return errors.Is(err, ErrUnsafePath) }

// ObjectKey — канонический ключ объекта в хранилище. Ключ всегда
// нормализован: использует "/" как разделитель, не начинается с "/",
// не содержит "." и ".." сегментов. Адаптеры отвечают за безопасное
// преобразование ключа в свой формат адресации (путь, bucket+key и т.п.).
type ObjectKey string

// String возвращает строковое представление ключа.
func (k ObjectKey) String() string { return string(k) }

// ObjectMetadata — метаданные объекта, возвращаемые при lookup/stat.
type ObjectMetadata struct {
	// Key — канонический ключ объекта.
	Key ObjectKey
	// Size — размер объекта в байтах.
	Size int64
	// ModTime — время последней модификации объекта.
	ModTime time.Time
	// ContentType — MIME-тип, если известен (может быть пустым).
	ContentType string
	// ETag — слабый/сильный идентификатор версии объекта, если доступен.
	ETag string
}

// PublishOptions — параметры атомарной публикации результата.
type PublishOptions struct {
	// NoOverwrite — если true, публикация завершится ошибкой ErrConflict,
	// когда объект уже существует.
	NoOverwrite bool
	// ContentType — MIME-тип публикуемого объекта (может быть пустым).
	ContentType string
	// CacheControl — значение заголовка Cache-Control (может быть пустым).
	CacheControl string
}

// Artifact — открытый поток объекта с метаданными. Реализация обязана
// закрывать ресурс через Close. Интерфейс не зависит от os.File.
type Artifact interface {
	// Read реализует io.Reader.
	Read(p []byte) (int, error)
	// Seek реализует io.Seeker (для перемотки при повторной обработке).
	Seek(offset int64, whence int) (int64, error)
	// Close освобождает ресурс.
	Close() error
	// Metadata возвращает метаданные открытого объекта.
	Metadata() ObjectMetadata
}

// Stream — открытый поток объекта с метаданными, предназначенный для
// последовательной отдачи (например, клиенту). В отличие от Artifact,
// Stream не требует перематываемости (Seek): это одноразовый поток,
// который читается от начала до конца. Реализация обязана закрывать
// ресурс через Close.
type Stream interface {
	// Read реализует io.Reader.
	Read(p []byte) (int, error)
	// Close освобождает ресурс.
	Close() error
	// Metadata возвращает метаданные открытого объекта.
	Metadata() ObjectMetadata
}

// NotFoundError — обёртка, позволяющая адаптерам добавлять контекст
// (например, ключ) к ErrNotFound, сохраняя errors.Is-совместимость.
type NotFoundError struct {
	Key ObjectKey
}

// Error реализует error.
func (e *NotFoundError) Error() string {
	return fmt.Sprintf("object %q not found", e.Key)
}

// Unwrap возвращает каноническую ошибку ErrNotFound.
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// ConflictError — обёртка, добавляющая контекст (например, ключ) к
// ErrConflict, сохраняя errors.Is-совместимость.
type ConflictError struct {
	Key ObjectKey
}

// Error реализует error.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("object %q already exists", e.Key)
}

// Unwrap возвращает каноническую ошибку ErrConflict.
func (e *ConflictError) Unwrap() error { return ErrConflict }

// StoreStats — агрегированная статистика хранилища.
type StoreStats struct {
	// Objects — число объектов в хранилище.
	Objects int64
	// TotalBytes — суммарный размер объектов в байтах.
	TotalBytes int64
}

// QuotaError — обёртка, добавляющая детали превышения квоты (объект/байты)
// к ErrQuota, сохраняя errors.Is-совместимость. Поля Limit/Current имеют
// значение 0, если адаптер не ведёт точного учёта (best-effort).
type QuotaError struct {
	// Key — ключ объекта, который не удалось записать (может быть пустым).
	Key ObjectKey
	// Limit — значение лимита (0, если лимит не задан/неизвестен).
	Limit int64
	// Current — фактическое значение квоты, из-за которого запись отклонена.
	Current int64
	// Reason — человекочитаемое описание (например, "max bytes", "objects").
	Reason string
	// Err — исходная ошибка (например, syscall.ENOSPC); может быть nil.
	Err error
}

// Error реализует error.
func (e *QuotaError) Error() string {
	msg := "storage quota exceeded"
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if e.Key != "" {
		msg += " (key " + string(e.Key) + ")"
	}
	if e.Limit > 0 || e.Current > 0 {
		msg += fmt.Sprintf(" (limit=%d current=%d)", e.Limit, e.Current)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap возвращает каноническую ошибку ErrQuota; если задана внутренняя
// причина, она возвращается первой для сохранения errors.Is-совместимости.
func (e *QuotaError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrQuota
}
