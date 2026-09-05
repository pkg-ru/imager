package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/app/generatev2"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/observability"
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

// serveOriginalConfig возвращает конфиг с включённой отдельной фичей
// serve-original (отдача исходников по «простым» URL /path/name.ext со
// статусом 200) и опционально включённым каноническим source fallback.
func serveOriginalConfig(sources *memSourceStore, sfEnabled bool, sfStatus int) Config {
	cfg := baseConfig()
	cfg.SourceFallback = SourceFallbackConfig{
		Enabled:      sfEnabled,
		Status:       sfStatus,
		CacheControl: "no-store",
	}
	cfg.ServeOriginal = ServeOriginalConfig{
		Enabled:      true,
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
	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
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

	req := httptest.NewRequest(http.MethodHead, "/img-png/200x200@5.png", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := metrics.count(observability.AssetErrParse); got != 1 {
		t.Errorf("parse errors = %d, want 1", got)
	}

	// Policy denied: имя сегмента thumb — валидный URL, генератор вернёт 403.
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeForbidden, Reason: "policy: denied"})
	req = httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := metrics.count(observability.AssetErrParse); got != 0 {
		t.Errorf("parse errors = %d, want 0 (disabled)", got)
	}
}

// TestHandlerServeOriginal проверяет отдачу исходника по «простому» URL
// /test/my.png при serve-original.enabled: true — со СТАТУСОМ 200.
func TestHandlerServeOriginal(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("test/my.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, serveOriginalConfig(src, false, http.StatusNotFound))

	req := httptest.NewRequest(http.MethodGet, "/test/my.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
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

// TestHandlerServeOriginalDisabled проверяет, что при
// serve-original.enabled: false (дефолт) «простой» URL по-прежнему даёт
// ошибку 400.
func TestHandlerServeOriginalDisabled(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("test/my.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	cfg := baseConfig()
	cfg.SourceFallback = SourceFallbackConfig{Enabled: true, Status: http.StatusOK, CacheControl: "no-store"}
	cfg.Sources = src
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/test/my.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandlerServeOriginalNotFound проверяет, что при
// serve-original.enabled: true и отсутствии исходника в хранилище фича не
// срабатывает (обычная ошибка 400).
func TestHandlerServeOriginalNotFound(t *testing.T) {
	src := newMemSourceStore() // пустое хранилище
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, serveOriginalConfig(src, false, http.StatusNotFound))

	req := httptest.NewRequest(http.MethodGet, "/test/my.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandlerServeOriginalUnsafe проверяет, что небезопасные «простые» URL
// (traversal) не отдаются даже при serve-original.enabled: true.
func TestHandlerServeOriginalUnsafe(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("test/my.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, serveOriginalConfig(src, false, http.StatusNotFound))

	req := httptest.NewRequest(http.MethodGet, "/test/../test/my.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandlerServeOriginalCanonicalURLStillFallback — канонический
// source-fallback (URL вида name-format.ext) при source-fallback.enabled: true
// отдаётся со статусом sf.Status (существующее поведение, не 200), даже если
// serve-original включён.
func TestHandlerServeOriginalCanonicalURLStillFallback(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, serveOriginalConfig(src, true, http.StatusNotFound))

	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (sf.Status)", rec.Code)
	}
	if body := rec.Body.String(); body != "SOURCE" {
		t.Errorf("body = %q, want SOURCE", body)
	}
}

// TestHandlerServeOriginalWithoutSourceFallback — ключевой сценарий:
// serve-original.enabled: true при source-fallback.enabled: false —
// «простой» URL отдаётся (200), канонический URL даёт 400.
func TestHandlerServeOriginalWithoutSourceFallback(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("test/my.png"), []byte("SOURCE"))
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	h := newTestHandler(t, gen, serveOriginalConfig(src, false, http.StatusNotFound))

	// «Простой» URL отдаётся со статусом 200.
	req := httptest.NewRequest(http.MethodGet, "/test/my.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("simple url: status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "SOURCE" {
		t.Errorf("simple url: body = %q, want SOURCE", body)
	}

	// Канонический URL — 400 (source-fallback выключен).
	req = httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("canonical url: status = %d, want 400", rec.Code)
	}
}

// TestHandlerSourceFallbackCanonicalDisabled проверяет, что канонический
// source-fallback (URL вида name-format.ext) при enabled: false НЕ
// срабатывает (400), т.е. enabled по-прежнему управляет канонической веткой.
func TestHandlerSourceFallbackCanonicalDisabled(t *testing.T) {
	src := newMemSourceStore()
	src.Add(object.ObjectKey("img.png"), []byte("SOURCE"))
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "nf"})

	cfg := baseConfig()
	cfg.SourceFallback = SourceFallbackConfig{
		Enabled:      false,
		Status:       http.StatusNotFound,
		CacheControl: "no-store",
	}
	cfg.Sources = src
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/img-png/200x200@5.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
