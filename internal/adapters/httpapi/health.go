package httpapi

import (
	"encoding/json"
	"net/http"
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
			writeHealth(w, http.StatusServiceUnavailable, "dead")
			return
		}
		writeHealth(w, http.StatusOK, "alive")
	})
}

// ReadinessHandler — readiness endpoint (false при shutdown).
func (h *Health) ReadinessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.rt == nil || !h.rt.Ready() {
			writeHealth(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeHealth(w, http.StatusOK, "ready")
	})
}

func writeHealth(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	body, _ := json.Marshal(map[string]string{"status": state})
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
