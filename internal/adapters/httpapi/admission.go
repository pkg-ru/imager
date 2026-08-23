package httpapi

import (
	"net/http"
)

// admissionControl — middleware глобального ограничения числа одновременных
// запросов (В11). При переполнении семафора отвечает 503 + Retry-After,
// не отправляя запрос в процессоры/сеть. Предотвращает массовые 500 при
// шкале запросов и переполнение bounded-очередей процессоров.
type admissionControl struct {
	// sem — семафор: канал фиксированной ёмкости. Запись в канал успешна,
	// пока есть свободные слоты; иначе запрос отклоняется.
	sem chan struct{}
}

// NewAdmissionControl создаёт admission control с лимитом maxConcurrent
// одновременных запросов (0 = без ограничения).
func NewAdmissionControl(maxConcurrent int) *admissionControl {
	if maxConcurrent <= 0 {
		maxConcurrent = 0
	}
	return &admissionControl{sem: make(chan struct{}, maxConcurrent)}
}

// Wrap оборачивает next, ограничивая число одновременных запросов.
func (a *admissionControl) Wrap(next http.Handler) http.Handler {
	if a.sem == nil || cap(a.sem) == 0 {
		// Без ограничения (maxConcurrent == 0): пропускаем все запросы.
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case a.sem <- struct{}{}:
			defer func() { _ = <-a.sem }()
			next.ServeHTTP(w, r)
		default:
			// Семафор переполнен — отклоняем с 503 + Retry-After.
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusServiceUnavailable)
		}
	})
}
