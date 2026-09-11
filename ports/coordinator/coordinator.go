// Package coordinator определяет абстрактный порт координации (keyed
// singleflight / distributed lock) для дедупликации конкурентных запросов
// генерации ассетов.
package coordinator

import (
	"context"

	"gitverse.ru/pkg-ru/imager/domain/object"
)

// Keyed — интерфейс keyed координации. Гарантирует, что для одного и того же
// ключа одновременно выполняется не более одной операции. Остальные
// запросы блокируются до завершения первой и получают её результат.
type Keyed interface {
	// Do выполняет fn под защитой keyed singleflight. Если другой запрос
	// с тем же key уже выполняется, Do блокируется и возвращает результат
	// первого запроса.
	Do(ctx context.Context, key object.ObjectKey, fn func() (any, error)) (any, error)

	// InFlight сообщает, выполняется ли в данный момент операция для key.
	// Используется admission control для bypass: запрос, который может
	// присоединиться к уже идущей singleflight-генерации того же ассета,
	// пропускается в обход семафора (вместо 503). Должен быть дешёвым:
	// lookup в map под блокировкой, без аллокаций.
	InFlight(key object.ObjectKey) bool
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
