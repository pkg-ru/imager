// Package coordinator определяет абстрактный порт координации (keyed
// singleflight / distributed lock) для дедупликации конкурентных запросов
// генерации ассетов.
package coordinator

import (
	"context"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// Keyed — интерфейс keyed координации. Гарантирует, что для одного и того же
// ключа одновременно выполняется не более одной операции. Остальные
// запросы блокируются до завершения первой и получают её результат.
type Keyed interface {
	// Do выполняет fn под защитой keyed singleflight. Если другой запрос
	// с тем же key уже выполняется, Do блокируется и возвращает результат
	// первого запроса.
	Do(ctx context.Context, key object.ObjectKey, fn func() (any, error)) (any, error)
}

// Unlock — функция освобождения блокировки (для Acquire-style API).
type Unlock func()

// KeyedLocker — альтернативный интерфейс keyed блокировки с явным
// Acquire/Release (для случаев, когда нужно удерживать блокировку между
// несколькими операциями).
type KeyedLocker interface {
	// Acquire блокирует ключ. Блокируется до получения блокировки или
	// отмены контекста.
	Acquire(ctx context.Context, key object.ObjectKey) (Unlock, error)
}
