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

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)

	if gotID == "" {
		t.Fatal("expected generated request ID in context")
	}
	if rec.Header().Get(RequestIDHeader) != gotID {
		t.Errorf("response header %s = %q, want %q", RequestIDHeader, rec.Header().Get(RequestIDHeader), gotID)
	}
}

func TestMiddlewarePropagatesIncomingRequestID(t *testing.T) {
	var gotID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = RequestIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	m := NewMiddleware(NopMetrics(), next)

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
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
func (r *recordingMetrics) IncStorageOp(StorageOp, bool)                          {}
func (r *recordingMetrics) ObserveStorageDuration(StorageOp, bool, time.Duration) {}

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
