package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMiddlewareGeneratesRequestID(t *testing.T) {
	var gotID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = RequestIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	m := NewMiddleware(NopMetrics(), next)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)

	if gotID == "" {
		t.Fatal("expected generated request ID in context")
	}
	if rec.Header().Get(RequestIDHeader) != gotID {
		t.Errorf("response header %s = %q, want %q", RequestIDHeader, rec.Header().Get(RequestIDHeader), gotID)
	}
}

// TestStatusWriterUnwrap проверяет, что statusWriter.Unwrap() возвращает
// внутренний writer (совместимость с обёртками и sendfile-оптимизацией).
func TestStatusWriterUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	if got := sw.Unwrap(); got != rec {
		t.Fatalf("Unwrap() = %v, want inner recorder", got)
	}
	// Обёртка продолжает работать после Unwrap.
	sw.WriteHeader(http.StatusOK)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestNewRequestIDUnique проверяет, что сгенерированные request ID уникальны
// в рамках генерации 10k ID (fallback-путь: атомарный счётчик + случайная
// база исключают коллизии в пределах процесса).
func TestNewRequestIDUnique(t *testing.T) {
	const n = 10000
	seen := make(map[string]bool)
	for i := 0; i < n; i++ {
		id := newRequestID()
		if id == "" {
			t.Fatal("newRequestID returned empty")
		}
		if seen[id] {
			t.Fatalf("duplicate request ID %q at iteration %d", id, i)
		}
		seen[id] = true
	}
}

func TestMiddlewarePropagatesIncomingRequestID(t *testing.T) {
	var gotID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = RequestIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	m := NewMiddleware(NopMetrics(), next)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(RequestIDHeader, "incoming-id-123")
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)

	if gotID != "incoming-id-123" {
		t.Errorf("request ID = %q, want incoming-id-123", gotID)
	}
}

func TestMiddlewareRecordsStatusClass(t *testing.T) {
	rec := newRecordingMetrics()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	m := NewMiddleware(rec, next)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	m.ServeHTTP(httptest.NewRecorder(), req)

	if rec.requests[Status4xx] != 1 {
		t.Errorf("4xx requests = %d, want 1", rec.requests[Status4xx])
	}
	if _, ok := rec.durations[Status4xx]; !ok {
		t.Error("expected recorded duration for 4xx")
	}
}

// recordingMetrics — тестовая реализация Metrics.
type recordingMetrics struct {
	requests  map[StatusClass]int
	durations map[StatusClass]float64
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{
		requests:  map[StatusClass]int{},
		durations: map[StatusClass]float64{},
	}
}

func (r *recordingMetrics) IncRequests(c StatusClass) { r.requests[c]++ }
func (r *recordingMetrics) ObserveRequestDuration(c StatusClass, d time.Duration) {
	r.durations[c] += d.Seconds()
}
func (r *recordingMetrics) IncCacheHit()                                          {}
func (r *recordingMetrics) IncCacheMiss()                                         {}
func (r *recordingMetrics) IncProcessorSuccess()                                  {}
func (r *recordingMetrics) IncProcessorError()                                    {}
func (r *recordingMetrics) ObserveProcessorDuration(time.Duration)                {}
func (r *recordingMetrics) IncDetectionDegraded()                                 {}
func (r *recordingMetrics) IncVipsOrphanOp()                                      {}
func (r *recordingMetrics) SetVipsOrphanInflight(int64)                           {}
func (r *recordingMetrics) IncPanics()                                            {}
func (r *recordingMetrics) IncStorageOp(StorageOp, bool)                          {}
func (r *recordingMetrics) ObserveStorageDuration(StorageOp, bool, time.Duration) {}
func (r *recordingMetrics) IncAssetError(AssetErrorKind)                          {}

func TestMetricsHandlerOutputsCounters(t *testing.T) {
	sm := NewStdMetrics()
	sm.IncRequests(Status2xx)
	sm.IncCacheHit()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, req)

	body := rec.Body.String()
	// Проверяем наличие метрик, а не абсолютные значения: expvar-реестр
	// глобален на процесс, и другие тесты могут инкрементить те же счётчики.
	if !strings.Contains(body, "imager_requests_2xx ") {
		t.Errorf("metrics output missing imager_requests_2xx: %q", body)
	}
	if !strings.Contains(body, "imager_cache_hits ") {
		t.Errorf("metrics output missing imager_cache_hits: %q", body)
	}
}
