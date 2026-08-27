// Package generatev2 реализует production application use case "GenerateAsset"
// поверх domain packages (asset, policy, processing, object) и узких портов
// (storage.SourceStore/ResultStore, coordinator.Keyed, processor).
//
// Сервис:
//   - принимает уже parsed/validated asset.Request и скомпилированную policy;
//   - выполняет cache lookup без гонки Exists+Open (только Lookup/Open);
//   - выполняет policy decision, source lookup, keyed singleflight/coordinator
//     recheck, обработку через абстрактный processor.Processor, bounded output
//     через writer/limit и атомарный publish через ResultStore;
//   - гарантирует cancellation-safe lifecycle (дочерний context, ожидание всех
//     goroutine, CloseWithError, cleanup).
//
// Публичные типы результата (Result, OutcomeError, OutcomeKind) определены
// в порту ports/generation и переиспользуются здесь через алиасы: транспортные
// адаптеры зависят только от порта, а не от этого пакета.
package generatev2

import (
	"github.com/pkg-ru/imager/ports/generation"
)

// OutcomeKind — категория исхода генерации для маппинга в HTTP-статусы.
type OutcomeKind = generation.OutcomeKind

// Категории исходов.
const (
	// OutcomeInvalid — запрос невалиден (неразрешимый preset, недопустимый
	// план и т.п.).
	OutcomeInvalid = generation.OutcomeInvalid
	// OutcomeForbidden — запрос запрещён политикой.
	OutcomeForbidden = generation.OutcomeForbidden
	// OutcomeNotFound — исходный объект не найден.
	OutcomeNotFound = generation.OutcomeNotFound
	// OutcomeQuota — превышена квота хранилища.
	OutcomeQuota = generation.OutcomeQuota
	// OutcomeUnavailable — хранилище/координатор временно недоступны.
	OutcomeUnavailable = generation.OutcomeUnavailable
	// OutcomeOverloaded — перегрузка процессоров (bounded очередь переполнена);
	// клиенту следует повторить позже (503 + Retry-After).
	OutcomeOverloaded = generation.OutcomeOverloaded
	// OutcomeProcessing — ошибка обработки изображения.
	OutcomeProcessing = generation.OutcomeProcessing
	// OutcomeCanceled — операция отменена.
	OutcomeCanceled = generation.OutcomeCanceled
)

// OutcomeError — типизированная ошибка use case с категорией исхода.
type OutcomeError = generation.OutcomeError

// outcome создаёт OutcomeError.
func outcome(kind OutcomeKind, reason string, cause error) *OutcomeError {
	return &OutcomeError{Kind: kind, Reason: reason, Cause: cause}
}
