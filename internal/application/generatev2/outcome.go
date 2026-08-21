// Package generatev2 реализует production application use case "GenerateAsset"
// поверх новых domain packages (asset, policy, processing, object) и узких
// портов (storage.SourceStore/ResultStore, coordinator.Keyed, processor).
//
// В отличие от legacy generate, сервис:
//   - принимает уже parsed/validated asset.Request и скомпилированную policy;
//   - выполняет cache lookup без гонки Exists+Open (только Lookup/Open);
//   - выполняет policy decision, source lookup, keyed singleflight/coordinator
//     recheck, обработку через абстрактный processor.Processor, bounded output
//     через writer/limit и атомарный publish через ResultStore;
//   - гарантирует cancellation-safe lifecycle (дочерний context, ожидание всех
//     goroutine, CloseWithError, cleanup).
package generatev2

import (
	"errors"
	"fmt"
)

// OutcomeKind — категория исхода генерации для маппинга в HTTP-статусы.
type OutcomeKind string

// Категории исходов.
const (
	// OutcomeInvalid — запрос невалиден (неразрешимый preset, недопустимый
	// план и т.п.).
	OutcomeInvalid OutcomeKind = "invalid"
	// OutcomeForbidden — запрос запрещён политикой.
	OutcomeForbidden OutcomeKind = "forbidden"
	// OutcomeNotFound — исходный объект не найден.
	OutcomeNotFound OutcomeKind = "not-found"
	// OutcomeQuota — превышена квота хранилища.
	OutcomeQuota OutcomeKind = "quota"
	// OutcomeUnavailable — хранилище/координатор временно недоступны.
	OutcomeUnavailable OutcomeKind = "unavailable"
	// OutcomeProcessing — ошибка обработки изображения.
	OutcomeProcessing OutcomeKind = "processing"
	// OutcomeCanceled — операция отменена.
	OutcomeCanceled OutcomeKind = "canceled"
)

// OutcomeError — типизированная ошибка use case с категорией исхода.
type OutcomeError struct {
	Kind   OutcomeKind
	Reason string
	Cause  error
}

// Error реализует error.
func (e *OutcomeError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("generatev2: %s: %s: %v", e.Kind, e.Reason, e.Cause)
	}
	return fmt.Sprintf("generatev2: %s: %s", e.Kind, e.Reason)
}

// Unwrap возвращает причину (cause).
func (e *OutcomeError) Unwrap() error { return e.Cause }

// outcome создаёт OutcomeError.
func outcome(kind OutcomeKind, reason string, cause error) *OutcomeError {
	return &OutcomeError{Kind: kind, Reason: reason, Cause: cause}
}

// IsOutcome сообщает, является ли err типизированной ошибкой OutcomeError
// с указанной категорией (в том числе обёрнутой).
func IsOutcome(err error, kind OutcomeKind) bool {
	var oe *OutcomeError
	if !errors.As(err, &oe) {
		return false
	}
	return oe.Kind == kind
}
