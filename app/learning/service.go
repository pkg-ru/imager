package learning

import "gitverse.ru/pkg-ru/imager/domain/asset"

// service.go — точка сборки фундамента learning-mode: связывает
// Controller (runtime-флаг) и Recorder (сборщик наблюдений).
//
// Интеграция с generatev2/httpapi/composition — отдельная подзадача:
// композиция создаёт Service из конфига (learning-mode: true в
// policy.Config) и передаёт его в use case'ы.

// Service — фасад фундамента learning-mode.
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

// Stop останавливает Recorder (drain + финальная запись).
func (s *Service) Stop() {
	if s.recorder != nil {
		s.recorder.Stop()
	}
}
