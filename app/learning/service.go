package learning

import "gitverse.ru/pkg-ru/imager/domain/asset"

// Service связывает Controller (runtime-флаг) и Recorder (сборщик
// наблюдений) learning-mode.
type Service struct {
	controller *Controller
	recorder   *Recorder
}

// NewService собирает Service из Controller и Recorder.
func NewService(c *Controller, r *Recorder) *Service {
	return &Service{controller: c, recorder: r}
}

// Enabled возвращает текущее состояние learning-mode.
func (s *Service) Enabled() bool {
	return s.controller != nil && s.controller.Enabled()
}

// Observe регистрирует наблюдение (неблокирующе). Наблюдения принимаются
// только при включённом learning-mode.
func (s *Service) Observe(req *asset.Request) {
	if !s.Enabled() || s.recorder == nil {
		return
	}
	s.recorder.Observe(req)
}

// Stop выключает learning-mode (runtime-флаг) и останавливает Recorder
// (drain + финальная запись наблюдений). Если режим был включён —
// дополнительно выполняется персистентный сброс: policy.learning-mode: false
// записывается в generate-local.yaml, чтобы после перезапуска сервер не
// продолжал работу в learning-режиме (конфиг при старте читается с диска).
// Вызывается при graceful shutdown.
func (s *Service) Stop() {
	wasEnabled := s.Enabled()
	if s.controller != nil {
		s.controller.Disable()
	}
	if s.recorder != nil {
		if wasEnabled {
			// Персистентный сброс ДО Stop(): Recorder ещё жив, ошибка записи
			// логируется его логгером. Порядок с финальной записью наблюдений
			// не важен: SetLearningMode меняет только scalar learning-mode,
			// UpdatePathPolicies — только секцию path-policies.
			s.recorder.ResetLearningMode()
		}
		s.recorder.Stop()
	}
}
