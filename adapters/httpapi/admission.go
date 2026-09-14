package httpapi

import (
	"net/http"
	"strconv"
)

// admissionControl — middleware глобального ограничения числа одновременных
// запросов. При переполнении семафора отвечает 503 + Retry-After (динамический
// — зависит от текущей загрузки, см. retryAfter), не отправляя запрос в
// процессоры/сеть. Предотвращает массовые 500 при шкале запросов и
// переполнение bounded-очередей процессоров.
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

// retryAfter возвращает динамическое значение Retry-After (в секундах) для
// HTTP 503 при переполнении семафора.
//
// Обоснование: фиксированный Retry-After=1 не учитывает
// реальную нагрузку — при большом числе одновременно обрабатываемых запросов
// слот освободится не раньше, чем через время обработки самого короткого из
// них, что при тесной ёмкости может быть существенно больше секунды. Чем
// больше запросов уже занимают слоты (len(sem) == ёмкости в ветке
// «переполнено»), тем дольше освобождение — поэтому честнее сообщить клиенту
// о соответствующей задержке. Минимум 1s (отказоустойчивый быстрый retry для
// маленьких семафоров).
//
// Поведение задокументировано в docs/CONFIGURATION.md (раздел
// «Лимиты и admission control»).
func (a *admissionControl) retryAfter() int {
	if a.sem == nil {
		return 1
	}
	// В ветке «переполнено» семафор плотно заполнен (len == cap), поэтому
	// занятость эквивалентна ёмкости. Берём минимум 1.
	if n := len(a.sem); n > 1 {
		return n
	}
	return 1
}

// Wrap оборачивает next, ограничивая число одновременных запросов.
// bypassCheck — опциональный предикат (nil = выключен): вызывается ТОЛЬКО
// при переполнении семафора. Если возвращает true, запрос пропускается в
// обход семафора (без занятия слота) — используется для запросов, которые
// могут присоединиться к уже идущей singleflight-генерации того же ассета
// (см. generatev2.Service.InFlightByPath). Bypass не должен занимать слот:
// join к идущей генерации не создаёт новой работы.
func (a *admissionControl) Wrap(next http.Handler, bypassCheck func(*http.Request) bool) http.Handler {
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
			// Семафор переполнен. Если запрос может присоединиться к уже
			// идущей singleflight-генерации того же ассета — пропускаем его
			// в обход семафора (вместо 503).
			if bypassCheck != nil && bypassCheck(r) {
				next.ServeHTTP(w, r)
				return
			}
			// Иначе — отклоняем с 503 + динамический Retry-After.
			w.Header().Set("Retry-After", strconv.Itoa(a.retryAfter()))
			http.Error(w, "too many requests", http.StatusServiceUnavailable)
		}
	})
}
