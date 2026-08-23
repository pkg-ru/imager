package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ReadinessCheck — опциональная проверка готовности ключевых зависимостей
// (хранилища/процессора) для readiness endpoint (п.5). Возвращает nil, если
// зависимость готова, иначе — описание проблемы.
type ReadinessCheck func() error

// Health — обработчик readiness/liveness.
type Health struct {
	rt *Runtime

	// check — опциональная проверка зависимостей (п.5).
	check ReadinessCheck
	// checkInterval — интервал кэширования результата проверки.
	checkInterval time.Duration
	// checkTimeout — таймаут выполнения проверки.
	checkTimeout time.Duration

	mu        sync.Mutex
	lastCheck time.Time
	lastOK    error
	// checkInProgress — выполняется ли проверка зависимостей (В9: ограничение
	// конкурентных проверок при частых healthcheck-запросах).
	checkInProgress bool
}

// NewHealth создаёт Health поверх Runtime.
func NewHealth(rt *Runtime) *Health {
	return &Health{rt: rt, checkInterval: 5 * time.Second, checkTimeout: 2 * time.Second}
}

// SetReadinessCheck регистрирует проверку зависимостей для readiness (п.5).
func (h *Health) SetReadinessCheck(check ReadinessCheck) {
	h.mu.Lock()
	h.check = check
	h.lastCheck = time.Time{}
	h.mu.Unlock()
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
		if err := h.dependenciesReady(); err != nil {
			writeHealth(w, r, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeHealth(w, r, http.StatusOK, "ready")
	})
}

// dependenciesReady выполняет проверку зависимостей с кэшированием и
// таймаутом. Возвращает nil, если зависимости готовы.
//
// В9: вместо time.After (аллокация таймера на каждый вызов) используем
// time.NewTimer с defer timer.Stop() — таймер не утекает при раннем выходе.
// Результат передаётся через канал (без гонки на общей переменной).
// Конкурентные проверки ограничены флагом checkInFlight: при частых
// healthcheck-запросах не плодим горутины.
func (h *Health) dependenciesReady() error {
	h.mu.Lock()
	check := h.check
	interval := h.checkInterval
	timeout := h.checkTimeout
	now := time.Now()
	if check == nil {
		h.mu.Unlock()
		return nil
	}
	if !h.lastCheck.IsZero() && now.Sub(h.lastCheck) < interval {
		err := h.lastOK
		h.mu.Unlock()
		return err
	}
	// Не запускаем параллельные проверки: если одна уже выполняется,
	// возвращаем её результат (или nil, если ещё не завершилась).
	if h.checkInProgress {
		h.mu.Unlock()
		return h.lastOK
	}
	h.checkInProgress = true
	h.mu.Unlock()

	// Выполняем проверку вне блокировки, с таймаутом.
	done := make(chan error, 1)
	go func() {
		done <- check()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case err = <-done:
	case <-timer.C:
		err = errors.New("readiness check timeout")
	}

	h.mu.Lock()
	h.checkInProgress = false
	h.lastCheck = time.Now()
	h.lastOK = err
	h.mu.Unlock()
	return err
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
