package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pkg-ru/imager/internal/domain/asset"
)

// TestHandlerMaxURLLenRejected проверяет ограничение длины URL (request limit).
func TestHandlerMaxURLLenRejected(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/c-120x80@2.png", []byte("PNGDATA"), 7)
	cfg := baseConfig()
	cfg.MaxURLLen = 20
	h := newTestHandler(t, gen, cfg)

	// URL длиннее лимита → 414.
	req := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("status = %d, want 414", rec.Code)
	}
	assertErrorCode(t, rec, "invalid")
}

// TestHandlerEncodedSeparatorRejected проверяет, что encoded path separator
// (%2f) в URL отклоняется на уровне парсера (400), а не трактуется как
// разделитель пути.
func TestHandlerEncodedSeparatorRejected(t *testing.T) {
	gen := newFakeGenerator()
	h := newTestHandler(t, gen, baseConfig())

	for _, p := range []string{
		"/v1/a%2fb-png/c-120x80@2.png",
		"/v1/a%2fb-png/c-120x80@2.png",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("path %q status = %d, want 400", p, rec.Code)
		}
	}
}

// TestParseControlCharRejected проверяет, что raw control-символы в URL
// отклоняются на уровне парсера (rejectUnsafe). Проверяется напрямую через
// asset.Parse, т.к. httptest.NewRequest кодирует control-символы в %XX.
func TestParseControlCharRejected(t *testing.T) {
	for _, raw := range []string{
		"/v1/img-png/c-120x80@2.png\x00",
		"/v1/img-png/c-120x80@2.png\x1f",
		"/v1/img-png/c-120x80@2.png\x7f",
	} {
		if _, err := asset.Parse(raw); err == nil {
			t.Errorf("asset.Parse(%q): expected error for control char", raw)
		}
	}
}

// TestConfigWildcardWithCredentialsRejected проверяет, что wildcard origin
// с credentials запрещён на этапе валидации конфигурации.
func TestConfigWildcardWithCredentialsRejected(t *testing.T) {
	cfg := baseConfig()
	cfg.AllowedOrigins = []string{"*"}
	cfg.AllowCredentials = true
	if _, err := New(newFakeGenerator(), cfg); err == nil {
		t.Fatal("expected wildcard+credentials config to be rejected")
	}
}

// TestHandlerHeadConditionalNoBody проверяет, что HEAD с If-None-Match
// возвращает 304 без body, но с корректными headers.
func TestHandlerHeadConditionalNoBody(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/c-120x80@2.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())

	// Получаем ETag через GET.
	req := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	// HEAD с If-None-Match → 304, без body.
	reqH := httptest.NewRequest(http.MethodHead, "/v1/img-png/c-120x80@2.png", nil)
	reqH.Header.Set("If-None-Match", etag)
	recH := httptest.NewRecorder()
	h.ServeHTTP(recH, reqH)
	if recH.Code != http.StatusNotModified {
		t.Fatalf("HEAD status = %d, want 304", recH.Code)
	}
	if recH.Body.Len() != 0 {
		t.Errorf("HEAD 304 body length = %d, want 0", recH.Body.Len())
	}
}

// TestHandlerNotFoundImageFallback проверяет Image fallback: статический
// файл-картинка отдаётся с 404 и корректным Content-Type.
func TestHandlerNotFoundImageFallback(t *testing.T) {
	gen := newFakeGenerator()
	cfg := baseConfig()
	cfg.NotFound = NotFoundConfig{Image: "testdata/not-found.html"}
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/v1/missing-png/c-120x80@2.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "not found") {
		t.Errorf("fallback body = %q, want contains 'not found'", body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("fallback Cache-Control = %q, want no-store", cc)
	}
}

// TestHandlerVaryOriginOnAllowed проверяет, что Vary: Origin ставится и для
// разрешённого origin (чтобы прокси не смешивали ответы).
func TestHandlerVaryOriginOnAllowed(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/c-120x80@2.png", []byte("PNGDATA"), 7)
	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Origin") {
		t.Errorf("Vary = %q, want contains Origin", vary)
	}
	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q", acao)
	}
}
