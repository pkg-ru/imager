// Package learning реализует фундамент learning-mode: runtime-флаг,
// слияние наблюдений в path-policies и comment-preserving запись
// в generate-local.yaml.
//
// Цель режима (реализуется интеграцией отдельно): при включённой опции
// сервис генерирует ассеты, не подходящие по правилам, отдаёт их клиенту,
// но ничего не сохраняет в storage, и автоматически пополняет
// policy.path-policies в setting/generate-local.yaml на основе наблюдаемых
// URL (путь + размер + формат).
//
// Этот пакет НЕ интегрирован с generatev2/httpapi/composition — только
// фундамент: Controller (runtime-флаг), чистые функции слияния (merge.go),
// writer (writer.go) и Recorder (recorder.go/service.go).
package learning

import "sync/atomic"

// Controller — потокобезопасный runtime-флаг learning-mode.
//
// Отдельный от конфига: конфиг задаёт начальное состояние, runtime-флаг
// позволяет включать/выключать режим на лету (например, через admin API).
type Controller struct {
	enabled atomic.Bool
}

// NewController создаёт Controller в выключенном состоянии.
func NewController() *Controller {
	return &Controller{}
}

// Enable включает learning-mode и возвращает итоговое состояние (true).
func (c *Controller) Enable() bool {
	c.enabled.Store(true)
	return c.enabled.Load()
}

// Disable выключает learning-mode.
func (c *Controller) Disable() {
	c.enabled.Store(false)
}

// Enabled возвращает текущее состояние флага.
func (c *Controller) Enabled() bool {
	return c.enabled.Load()
}
