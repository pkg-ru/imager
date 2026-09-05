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

// Stop останавливает Recorder (drain + финальная запись).
func (s *Service) Stop() {
	if s.recorder != nil {
		s.recorder.Stop()
	}
}
