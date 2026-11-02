// Package generation определяет platform-independent контракт (порт)
// use case "GenerateAsset" для транспортных адаптеров (например HTTP).
//
// Порт изолирует адаптеры от конкретной реализации application-слоя:
// адаптер зависит только от типов этого пакета (Result, OutcomeError,
// Generator), а production-реализация (app/generatev2) использует их
// через алиасы типов.
package generation

import (
	"context"
	"fmt"

	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/object"
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
	// OutcomeOverloaded — перегрузка процессоров (bounded очередь переполнена);
	// клиенту следует повторить позже (503 + Retry-After).
	OutcomeOverloaded OutcomeKind = "overloaded"
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

// Result — типизированный результат генерации ассета.
type Result struct {
	// Key — канонический cache key (canonical URL), под которым ассет
	// опубликован.
	Key object.ObjectKey
	// URL — каноническая форма URL (без ведущего "/").
	URL string
	// Request — конечный канонический запрос (уже с разрешённым preset).
	Request *asset.Request
	// Opened — готовый к чтению поток ассета (для отдачи клиенту).
	Opened object.Stream
	// FromCache — true, если ассет уже существовал и генерация не выполнялась.
	FromCache bool
}

// Close закрывает Opened, если он есть.
func (r *Result) Close() error {
	if r != nil && r.Opened != nil {
		return r.Opened.Close()
	}
	return nil
}

// Generator — узкий порт генерации ассета, реализуемый application-сервисом
// (generatev2.Service).
type Generator interface {
	Generate(ctx context.Context, req *asset.Request) (*Result, error)
}
