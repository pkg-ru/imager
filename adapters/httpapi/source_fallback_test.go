package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/observability"
)

// recordingMetrics — fake Metrics, записывающий IncAssetError.
type recordingMetrics struct {
	mu    sync.Mutex
	asset map[observability.AssetErrorKind]int
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{asset: map[observability.AssetErrorKind]int{}}
}

func (r *recordingMetrics) IncRequests(observability.StatusClass)                               {}
func (r *recordingMetrics) ObserveRequestDuration(observability.StatusClass, time.Duration)     {}
func (r *recordingMetrics) IncCacheHit()                                                        {}
func (r *recordingMetrics) IncCacheMiss()                                                       {}
func (r *recordingMetrics) IncProcessorSuccess()                                                {}
func (r *recordingMetrics) IncProcessorError()                                                  {}
func (r *recordingMetrics) ObserveProcessorDuration(time.Duration)                              {}
func (r *recordingMetrics) IncStorageOp(observability.StorageOp, bool)                          {}
func (r *recordingMetrics) ObserveStorageDuration(observability.StorageOp, bool, time.Duration) {}
func (r *recordingMetrics) IncAssetError(k observability.AssetErrorKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asset[k]++
}

func (r *recordingMetrics) count(k observability.AssetErrorKind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asset[k]
}

// fallbackConfig возвращает конфиг с включённым source fallback.
func fallbackConfig(sources *memSourceStore, status int) Config {
	cfg := baseConfig()
	cfg.SourceFallback = SourceFallbackConfig{
		Enabled:      true,
		Status:       status,
		CacheControl: "no-store",
	}
	cfg.Sources = sources
	return cfg
}

func TestSourceFallbackParseError(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, fallbackConfig(src, http.StatusNotFound))

	// Неканонический URL (недопустимый dpr): Parse вернёт ошибку, но
	// исходник существует.
	req := httptest.NewRequest(http.MethodGet, "/img-png/c-120x80@5.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); body != "SOURCE" {
		t.Errorf("body = %q, want SOURCE", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Error("Content-Type missing")
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Error("Content-Disposition missing")
	}
}

func TestHandlerSourceFallbackDisabled(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	cfg := baseConfig()
	cfg.SourceFallback.Enabled = false
	cfg.Sources = src
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/img-png/c-120x80@5.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Fallback выключен → обычная ошибка 400 (Parse error).
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlerSourceFallbackStatus200(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, fallbackConfig(src, http.StatusOK))

	req := httptest.NewRequest(http.MethodGet, "/img-png/c-120x80@5.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlerSourceFallbackHead(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, fallbackConfig(src, http.StatusNotFound))

	req := httptest.NewRequest(http.MethodHead, "/img-png/c-120x80@5.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body length = %d, want 0", rec.Body.Len())
	}
}

func TestHandlerSourceFallbackSourceNotFound(t *testing.T) {
	src := newMemSourceStore() // пустое хранилище
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, fallbackConfig(src, http.StatusNotFound))

	req := httptest.NewRequest(http.MethodGet, "/img-png/c-120x80@5.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Исходник не найден -> обычная ошибка 400.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlerSourceFallbackPresetNotFound(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	// Несуществующий пресет -> OutcomeInvalid с ResolveError.
	gen.setFallback(&generatev2.OutcomeError{
		Kind:   generatev2.OutcomeInvalid,
		Reason: "resolve preset",
		Cause:  &asset.ResolveError{PresetName: "nope", Reason: "preset not found"},
	})

	h := newTestHandler(t, gen, fallbackConfig(src, http.StatusNotFound))

	req := httptest.NewRequest(http.MethodGet, "/img-png/nope.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); body != "SOURCE" {
		t.Errorf("body = %q, want SOURCE", body)
	}
}

func TestHandlerAssetErrorMetrics(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	metrics := newRecordingMetrics()
	cfg := baseConfig()
	cfg.Metrics = metrics
	cfg.AssetErrors = AssetErrorConfig{Enabled: true, LogLevel: "warn"}
	h := newTestHandler(t, gen, cfg)

	// Parse error.
	req := httptest.NewRequest(http.MethodGet, "/img-png/c-120x80@5.png", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := metrics.count(observability.AssetErrParse); got != 1 {
		t.Errorf("parse errors = %d, want 1", got)
	}

	// Policy denied.
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeForbidden, Reason: "policy: denied"})
	req = httptest.NewRequest(http.MethodGet, "/img-png/c-120x80.png", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := metrics.count(observability.AssetErrPolicyDenied); got != 1 {
		t.Errorf("policy denied = %d, want 1", got)
	}
}

func TestHandlerAssetErrorMetricsDisabled(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	metrics := newRecordingMetrics()
	cfg := baseConfig()
	cfg.Metrics = metrics
	cfg.AssetErrors = AssetErrorConfig{Enabled: false}
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/img-png/c-120x80@5.png", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := metrics.count(observability.AssetErrParse); got != 0 {
		t.Errorf("parse errors = %d, want 0 (disabled)", got)
	}
}
