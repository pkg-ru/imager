package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitverse.ru/pkg-ru/imager/app/generatev2"
)

// newTestHandler создаёт handler с fake generator и базовой конфигурацией.
func newTestHandler(t *testing.T, gen Generator, cfg Config) *Handler {
	t.Helper()
	h, err := New(gen, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func baseConfig() Config {
	return Config{
		AllowedOrigins: []string{"https://example.com"},
		CacheControl:   "public, max-age=31536000, immutable",
	}
}

func TestBuildFormatMap_JXL(t *testing.T) {
	m := buildFormatMap()
	if ct := m["jxl"]; ct != "image/jxl" {
		t.Errorf("jxl content-type = %q, want image/jxl", ct)
	}
	// Неизвестный формат не должен попадать в маппинг.
	if _, ok := m["bogus"]; ok {
		t.Error("bogus should not be in format map")
	}
}

func TestHandlerGetSuccess(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)

	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "7" {
		t.Errorf("Content-Length = %q, want 7", cl)
	}
	if etag := rec.Header().Get("ETag"); etag == "" {
		t.Error("ETag missing")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if body := rec.Body.String(); body != "PNGDATA" {
		t.Errorf("body = %q, want PNGDATA", body)
	}
}

func TestHandlerHeadNoBody(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)

	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodHead, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body length = %d, want 0", rec.Body.Len())
	}
	// HEAD должен иметь те же headers, что и GET.
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("HEAD Content-Type = %q, want image/png", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "7" {
		t.Errorf("HEAD Content-Length = %q, want 7", cl)
	}
	if etag := rec.Header().Get("ETag"); etag == "" {
		t.Error("HEAD ETag missing")
	}
}

func TestHandlerOptions(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodOptions, "/img-png/thumb.png", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD, OPTIONS" {
		t.Errorf("Allow = %q", allow)
	}
	if acam := rec.Header().Get("Access-Control-Allow-Methods"); acam != "GET, HEAD, OPTIONS" {
		t.Errorf("Access-Control-Allow-Methods = %q", acam)
	}
	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q", acao)
	}
}

func TestHandlerOptionsVaryOrigin(t *testing.T) {
	// Регрессия: preflight-ответ должен содержать Vary: Origin, чтобы
	// прокси-кэши не смешивали ответы разных origins (в т.ч. denied).
	gen := newFakeGenerator()

	for _, origin := range []string{"https://example.com", "https://evil.example"} {
		h := newTestHandler(t, gen, baseConfig())
		req := httptest.NewRequest(http.MethodOptions, "/img-png/thumb.png", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		vary := rec.Header().Values("Vary")
		found := false
		for _, v := range vary {
			if strings.Contains(v, "Origin") {
				found = true
			}
		}
		if !found {
			t.Errorf("Origin %q: Vary: Origin missing in OPTIONS response", origin)
		}
	}
}

func TestHandlerOptionsDeniedOrigin(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodOptions, "/x.png", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty (deny)", acao)
	}
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/x.png", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != "GET, HEAD, OPTIONS" {
			t.Errorf("%s Allow = %q", method, allow)
		}
	}
}

func TestHandlerInvalidPath(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())

	cases := []string{
		"/not-an-asset",  // нет структуры
		"/../etc/passwd", // traversal
		"/img-png/thumb", // нет output format
	}
	for _, p := range cases {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("path %q status = %d, want 400", p, rec.Code)
		}
	}
}

func TestHandlerForbidden(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeForbidden, Reason: "denied"})
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	assertErrorCode(t, rec, "forbidden")
}

func TestHandlerNotFoundNoFallback(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/missing-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "not_found")
}

func TestHandlerNotFoundPixelFallback(t *testing.T) {
	gen := newFakeGenerator()
	cfg := baseConfig()
	cfg.NotFound = NotFoundConfig{Pixel: true}
	cfg.Pixel = &fakePixel{}
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/missing-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); body != "PIXEL:png" {
		t.Errorf("pixel body = %q, want PIXEL:png", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
}

func TestHandlerNotFoundPageFallback(t *testing.T) {
	gen := newFakeGenerator()
	cfg := baseConfig()
	cfg.NotFound = NotFoundConfig{Page: "testdata/not-found.html"}
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/missing-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "not found") {
		t.Errorf("fallback body = %q, want contains 'not found'", body)
	}
}

func TestHandlerETagNotModified(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())

	// Первый GET получаем ETag.
	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag in first response")
	}

	// Второй GET с If-None-Match → 304 без body.
	req2 := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 body length = %d, want 0", rec2.Body.Len())
	}
}

func TestHandlerSecurityHeaders(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)
	cfg := baseConfig()
	cfg.ReferrerPolicy = "no-referrer"
	cfg.CSP = "default-src 'none'"
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", xcto)
	}
	if rp := rec.Header().Get("Referrer-Policy"); rp != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", rp)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "default-src 'none'" {
		t.Errorf("CSP = %q", csp)
	}
}

func TestHandlerCORSAllowedOrigin(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q", acao)
	}
}

func TestHandlerCORSDeniedOrigin(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", acao)
	}
}

func TestHandlerQuota(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeQuota, Reason: "quota"})
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", rec.Code)
	}
	assertErrorCode(t, rec, "quota")
}

func TestHandlerUnavailable(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeUnavailable, Reason: "unavail"})
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	assertErrorCode(t, rec, "unavailable")
}

func TestHandlerOverloaded(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeOverloaded, Reason: "overloaded"})
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Fatalf("Retry-After = %q, want 1", ra)
	}
	assertErrorCode(t, rec, "overloaded")
}

func TestHandlerProcessing(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeProcessing, Reason: "proc"})
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	assertErrorCode(t, rec, "processing")
}

func TestHandlerCanceled(t *testing.T) {
	gen := newFakeGenerator()
	gen.setFallback(&generatev2.OutcomeError{Kind: generatev2.OutcomeCanceled, Reason: "canceled"})
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	assertErrorCode(t, rec, "canceled")
}

func TestHandlerETagWildcardAndList(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag in first response")
	}

	// Wildcard "*" → 304.
	reqW := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	reqW.Header.Set("If-None-Match", "*")
	recW := httptest.NewRecorder()
	h.ServeHTTP(recW, reqW)
	if recW.Code != http.StatusNotModified {
		t.Errorf("wildcard status = %d, want 304", recW.Code)
	}

	// Список ETag (включая текущий) → 304.
	reqL := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	reqL.Header.Set("If-None-Match", `"other", `+etag)
	recL := httptest.NewRecorder()
	h.ServeHTTP(recL, reqL)
	if recL.Code != http.StatusNotModified {
		t.Errorf("list status = %d, want 304", recL.Code)
	}

	// Несовпадающий ETag → 200.
	reqM := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	reqM.Header.Set("If-None-Match", `"other"`)
	recM := httptest.NewRecorder()
	h.ServeHTTP(recM, reqM)
	if recM.Code != http.StatusOK {
		t.Errorf("mismatch status = %d, want 200", recM.Code)
	}
}

func TestHandlerRedirectFallbackHeadNoBody(t *testing.T) {
	gen := newFakeGenerator()
	cfg := baseConfig()
	cfg.NotFound = NotFoundConfig{Redirect: "https://example.com/missing"}
	h := newTestHandler(t, gen, cfg)

	// HEAD → 301 без body.
	req := httptest.NewRequest(http.MethodHead, "/missing-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("HEAD status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/missing" {
		t.Errorf("Location = %q", loc)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD redirect body length = %d, want 0", rec.Body.Len())
	}

	// GET → 301 с body.
	reqG := httptest.NewRequest(http.MethodGet, "/missing-png/thumb.png", nil)
	recG := httptest.NewRecorder()
	h.ServeHTTP(recG, reqG)
	if recG.Code != http.StatusMovedPermanently {
		t.Fatalf("GET status = %d, want 301", recG.Code)
	}
	if recG.Body.Len() == 0 {
		t.Error("GET redirect body empty, want non-empty")
	}
}

func TestHandlerCORSDeniedVaryOrigin(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", acao)
	}
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Origin") {
		t.Errorf("Vary = %q, want contains Origin", vary)
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("error body not JSON: %v (body=%q)", err, rec.Body.String())
	}
	if env.Error.Code != want {
		t.Errorf("error code = %q, want %q", env.Error.Code, want)
	}
}

// TestEtagCacheLRUEviction проверяет, что etagCache ограничен по числу ключей:
// при превышении max вытесняется наименее недавно использованный ключ.
func TestEtagCacheLRUEviction(t *testing.T) {
	c := newEtagCache(3)

	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")
	// Touch "a", затем добавляем "d" — должен вытесниться "b" (LRU).
	_, _ = c.Get("a")
	c.Set("d", "4")

	if _, ok := c.Get("b"); ok {
		t.Fatal("expected b evicted (LRU)")
	}
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("a = %q ok=%v, want 1", v, ok)
	}
	if v, ok := c.Get("d"); !ok || v != "4" {
		t.Fatalf("d = %q ok=%v, want 4", v, ok)
	}
	if n := c.Len(); n > 3 {
		t.Fatalf("cache size %d exceeds max 3", n)
	}
}

// TestGzipJSONResponse проверяет, что JSON-ответ (error envelope) сжимается
// gzip при Accept-Encoding: gzip.
func TestGzipJSONResponse(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())
	gh := gzipHandler(h)

	// Ошибка → JSON error envelope.
	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	gh.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", ce)
	}
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Errorf("Vary = %q, want contains Accept-Encoding", vary)
	}
	// Content-Length должен быть удалён (длина после сжатия неизвестна).
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length = %q, want empty for gzip", cl)
	}

	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer zr.Close()
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decompressed body not JSON: %v (body=%q)", err, body)
	}
	if env.Error.Code != "not_found" {
		t.Errorf("error code = %q, want not_found", env.Error.Code)
	}
}

// TestGzipNotAppliedToImages проверяет, что изображения не сжимаются
// (gzip только для JSON).
func TestGzipNotAppliedToImages(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())
	gh := gzipHandler(h)

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	gh.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding = %q, want empty for image", ce)
	}
	if body := rec.Body.String(); body != "PNGDATA" {
		t.Errorf("body = %q, want PNGDATA", body)
	}
}

// TestGzipNotAppliedWithoutAcceptEncoding проверяет, что без Accept-Encoding
// ответ не сжимается.
func TestGzipNotAppliedWithoutAcceptEncoding(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())
	gh := gzipHandler(h)

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	gh.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding = %q, want empty", ce)
	}
	if cl := rec.Header().Get("Content-Length"); cl == "" {
		t.Error("Content-Length should be present without gzip")
	}
}
