// Package admin определяет platform-independent контракт (порт)
// административных операций (фоновая генерация и удаление ассетов) для
// транспортных адаптеров (например HTTP).
//
// Порт изолирует адаптеры от конкретной реализации application-слоя:
// адаптер зависит только от интерфейса Service и типов этого пакета,
// а production-реализация (app/adminsvc) использует их через алиасы.
package admin

import (
	"context"
	"errors"
)

// FailedAsset — описание неудавшегося ассета.
type FailedAsset struct {
	URL     string `json:"url"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JobResult — результат выполнения задачи.
type JobResult struct {
	JobID     string        `json:"job_id"`
	Status    string        `json:"status"` // "accepted" | "completed"
	Queued    int           `json:"queued"`
	Generated int           `json:"generated,omitempty"`
	Skipped   int           `json:"skipped,omitempty"`
	Failed    []FailedAsset `json:"failed,omitempty"`
	Deleted   int           `json:"deleted,omitempty"`
}

// Типизированные ошибки административных операций (маппятся транспортом
// в HTTP-статусы).
var (
	// ErrQueueFull — очередь задач переполнена (→ HTTP 503).
	ErrQueueFull = errors.New("adminsvc: queue is full")
	// ErrStopped — сервис остановлен (Stop вызван), новые задачи не принимаются.
	ErrStopped = errors.New("adminsvc: service is stopped")
	// ErrInvalidRequest — невалидный запрос: ровно одно из source/assets
	// (→ HTTP 400).
	ErrInvalidRequest = errors.New("adminsvc: invalid request: exactly one of source or assets is required")
	// ErrSourceNotFound — исходник не существует (→ HTTP 404).
	ErrSourceNotFound = errors.New("adminsvc: source not found")
	// ErrWaitTimeout — превышен таймаут режима wait=true.
	ErrWaitTimeout = errors.New("adminsvc: wait timeout")
	// ErrNotImplemented — result store не поддерживает listing/prefix deletion.
	ErrNotImplemented = errors.New("adminsvc: result store does not support listing or prefix deletion")
)

// Service — точка входа административных операций для транспорта.
//
// Реализуется app/adminsvc.Service (который дополнительно предоставляет
// lifecycle-методы Start/Stop/Close, управляемые composition root).
type Service interface {
	// EnqueueGenerate ставит задачу генерации: ровно одно из source|assets.
	// wait=true блокирует до завершения всех ассетов (или таймаута сервиса).
	EnqueueGenerate(source string, assets []string, wait bool) (*JobResult, error)
	// DeleteBySource удаляет все ассеты, производные от исходника.
	DeleteBySource(ctx context.Context, source string) (int, error)
	// DeleteAssets удаляет ассеты по списку canonical URL.
	DeleteAssets(ctx context.Context, assets []string) (int, error)
}
