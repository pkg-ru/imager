package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Health — обработчик readiness/liveness.
type Health struct {
	rt *Runtime
}

// NewHealth создаёт Health поверх Runtime.
func NewHealth(rt *Runtime) *Health {
	return &Health{rt: rt}
}

// LivenessHandler — liveness endpoint (не зависит от shutdown).
func (h *Health) LivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.rt == nil || !h.rt.Alive() {
			writeHealth(w, r, http.StatusServiceUnavailable, "dead")
			return
		}
		writeHealth(w, r, http.StatusOK, "alive")
	})
}

// ReadinessHandler — readiness endpoint (false при shutdown).
// П.5: дополнительно проверяет готовность ключевых зависимостей (хранилища/
// процессора) с кэшированием результата и коротким таймаутом, чтобы не
// блокировать health-эндпоинт.
func (h *Health) ReadinessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.rt == nil || !h.rt.Ready() {
			writeHealth(w, r, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeHealth(w, r, http.StatusOK, "ready")
	})
}

func writeHealth(w http.ResponseWriter, r *http.Request, status int, state string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	body, _ := json.Marshal(map[string]string{"status": state})
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	// П.16: для HEAD пишем только заголовки (Content-Length), без тела.
	if r != nil && r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
