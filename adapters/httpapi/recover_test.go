package httpapi

import (
	"expvar"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitverse.ru/pkg-ru/imager/observability"
)

// captureLogger — тестовый логгер, накапливающий сообщения уровня error.
type captureLogger struct {
	mu  sync.Mutex
	msg string
}

func (c *captureLogger) Errorf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msg = c.msg + format + "\n"
}

func (c *captureLogger) Debugf(string, ...any) {}
func (c *captureLogger) Infof(string, ...any)  {}
func (c *captureLogger) Warnf(string, ...any)  {}

func (c *captureLogger) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.msg
}

var _ observability.Logger = (*captureLogger)(nil)

// TestRecoverWriterUnwrap проверяет, что recoverWriter.Unwrap() возвращает
// внутренний writer (совместимость с обёртками и sendfile-оптимизацией).
func TestRecoverWriterUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &recoverWriter{ResponseWriter: rec}
	if got := rw.Unwrap(); got != rec {
		t.Fatalf("Unwrap() = %v, want inner recorder", got)
	}
	// Обёртка продолжает работать после Unwrap.
	rw.WriteHeader(http.StatusOK)
	if !rw.started {
		t.Fatal("WriteHeader should mark started")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// panicHandler — handler, который паникует до записи ответа.
type panicHandler struct{}

func (panicHandler) ServeHTTP(http.ResponseWriter, *http.Request) {
	panic("boom")
}

// TestRecoverAdminPanicReturns500 проверяет, что паника в admin-ветке
// (handler без собственного recover) возвращает 500 JSON, а не обрыв.
func TestRecoverAdminPanicReturns500(t *testing.T) {
	log := &captureLogger{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/generate", nil)

	NewRecover(log, panicHandler{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Errorf("body = %q, want error envelope with code=internal", rec.Body.String())
	}
	if log.get() == "" {
		t.Error("panic was not logged")
	}
	if !strings.Contains(log.get(), "panic in mux handler") {
		t.Errorf("log message = %q, want panic in mux handler", log.get())
	}
}

// TestRecoverHealthPanicReturns500 проверяет, что паника в health-ветке
// возвращает 500.
func TestRecoverHealthPanicReturns500(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	NewRecover(nil, panicHandler{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestRecoverPanicAfterWriteNoSecondaryPanic проверяет, что паника после
// начала записи ответа НЕ вызывает вторичную панику (попытку дописать 500) —
// ответ уже начат, просто логируем. Паника залогирована.
func TestRecoverPanicAfterWriteNoSecondaryPanic(t *testing.T) {
	log := &captureLogger{}
	// handler пишет заголовки/тело и затем паникует.
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom after write")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	// Не должно упасть (вторичной паники нет) — иначе тест сам упадёт.
	NewRecover(log, h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (ответ уже начат, 500 не пишем)", rec.Code)
	}
	if log.get() == "" {
		t.Error("panic was not logged")
	}
}

// TestRecoverHeadPanicNoBody проверяет, что HEAD-запрос с паникой возвращает
// 500 без тела (HEAD-parity).
func TestRecoverHeadPanicNoBody(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/admin/assets/generate", nil)

	NewRecover(nil, panicHandler{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body length = %d, want 0", rec.Body.Len())
	}
	if cl := rec.Header().Get("Content-Length"); cl == "" {
		t.Error("HEAD missing Content-Length")
	}
}

// TestMuxAdminPanicReturns500 — интеграционный тест: паника в admin-handler,
// переданном в NewMuxWithAdmission, возвращает 500 через полный стек
// (Recover → observability → gzip → mux → admin).
func TestMuxAdminPanicReturns500(t *testing.T) {
	mux := NewMuxWithAdmission(
		panicHandler{}, // asset handler (не будет вызван)
		NewHealth(nil),
		nil,
		MetricsAuthConfig{},
		0,
		panicHandler{}, // admin handler — паникует
		nil,
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/generate", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Errorf("body = %q, want error envelope", rec.Body.String())
	}
}

// TestMuxPanicIncrementsPanicsCounter проверяет, что паника в mux-ветке
// инкрементирует глобальный счётчик imager_panics_total (B2). Счётчик
// глобален (expvar-реестр), поэтому проверяем приращение от снапшота.
func TestMuxPanicIncrementsPanicsCounter(t *testing.T) {
	before := panicMetricValue("imager_panics_total")

	mux := NewMuxWithAdmission(
		panicHandler{}, // asset handler (не будет вызван)
		NewHealth(nil),
		nil,
		MetricsAuthConfig{},
		0,
		panicHandler{}, // admin handler — паникует
		nil,
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/generate", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := panicMetricValue("imager_panics_total"); got != before+1 {
		t.Errorf("imager_panics_total = %d, want %d (before=%d)", got, before+1, before)
	}
}

// panicMetricValue возвращает текущее значение expvar-счётчика (0, если
// переменная ещё не зарегистрирована).
func panicMetricValue(name string) int64 {
	v := expvar.Get(name)
	if v == nil {
		return 0
	}
	iv, ok := v.(*expvar.Int)
	if !ok {
		return 0
	}
	return iv.Value()
}
