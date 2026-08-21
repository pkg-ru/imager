package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pkg-ru/imager/internal/application/generatev2"
	"github.com/pkg-ru/imager/internal/domain/asset"
)

// TestHealthHeadNoBody проверяет п.16: для HEAD health-эндпоинты пишут только
// заголовки (Content-Length), без тела.
func TestHealthHeadNoBody(t *testing.T) {
	rt, err := NewRuntime(RuntimeOptions{Handler: http.NotFoundHandler(), Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	defer rt.Close()

	health := NewHealth(rt)

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodHead, path, nil)
		rec := httptest.NewRecorder()
		var h http.Handler
		if path == "/healthz" {
			h = health.LivenessHandler()
		} else {
			h = health.ReadinessHandler()
		}
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s HEAD status = %d, want 200", path, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s HEAD body length = %d, want 0", path, rec.Body.Len())
		}
		if rec.Header().Get("Content-Length") == "" {
			t.Errorf("%s HEAD missing Content-Length", path)
		}
	}
}

// panicGenerator — генератор, который паникует в Generate.
type panicGenerator struct{}

func (panicGenerator) Generate(context.Context, *asset.Request) (*generatev2.Result, error) {
	panic("boom")
}

// TestHandlerPanicReturns500 проверяет п.2: паника в генераторе не роняет
// процесс, а возвращает 500.
func TestHandlerPanicReturns500(t *testing.T) {
	h := newTestHandler(t, panicGenerator{}, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestHandlerGenerateTimeout проверяет п.18: превышение GenerateTimeout
// маппится в 504 (OutcomeCanceled).
func TestHandlerGenerateTimeout(t *testing.T) {
	cfg := baseConfig()
	cfg.GenerateTimeout = 1 // 1ns — мгновенный таймаут

	// Генератор, который блокируется навсегда (до отмены ctx).
	gen := newFakeGenerator()
	gen.block = make(chan struct{})
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
}

// TestETagCached проверяет п.15: ETag стабилен и кэшируется (одинаков для
// повторных запросов).
func TestETagCached(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/c-120x80@2.png", []byte("PNGDATA"), 7)

	h := newTestHandler(t, gen, baseConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag1 := rec.Header().Get("ETag")
	if etag1 == "" {
		t.Fatal("ETag missing")
	}

	// Повторный запрос — тот же ETag (кэш).
	req2 := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	etag2 := rec2.Header().Get("ETag")
	if etag2 != etag1 {
		t.Errorf("ETag changed across requests: %q vs %q", etag1, etag2)
	}
}

// TestMetricsAuthToken проверяет п.17: /metrics защищён токеном.
func TestMetricsAuthToken(t *testing.T) {
	rt, err := NewRuntime(RuntimeOptions{Handler: http.NotFoundHandler(), Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	defer rt.Close()

	health := NewHealth(rt)
	mux := NewMuxWithAuth(newTestHandler(t, newFakeGenerator(), baseConfig()), health, nil,
		MetricsAuthConfig{Token: "secret"})

	// Без токена — 403.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status without token = %d, want 403", rec.Code)
	}

	// С токеном — 200.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Metrics-Token", "secret")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status with token = %d, want 200", rec.Code)
	}
}

// TestReadinessCheckDependency проверяет п.5: readiness учитывает проверку
// зависимостей.
func TestReadinessCheckDependency(t *testing.T) {
	rt, err := NewRuntime(RuntimeOptions{Handler: http.NotFoundHandler(), Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	defer rt.Close()

	health := NewHealth(rt)
	health.SetReadinessCheck(func() error { return errors.New("storage down") })

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	health.ReadinessHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness with failing dep = %d, want 503", rec.Code)
	}

	// После восстановления зависимости — 200.
	health.SetReadinessCheck(func() error { return nil })
	rec = httptest.NewRecorder()
	health.ReadinessHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness with ok dep = %d, want 200", rec.Code)
	}
}

// TestRuntimeMaxBodyBytes проверяет п.7: лимит тела запроса.
func TestRuntimeMaxBodyBytes(t *testing.T) {
	rt, err := NewRuntime(RuntimeOptions{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Читаем тело, чтобы MaxBytesHandler применил лимит. Если чтение
			// вернуло ошибку (MaxBytesError), возвращаем 413.
			_, err := io.Copy(io.Discard, r.Body)
			if err != nil {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
		Addr:         "127.0.0.1:0",
		MaxBodyBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	defer rt.Close()

	serveErr := make(chan error, 1)
	go func() { serveErr <- rt.Serve() }()
	defer func() { _ = rt.Shutdown(context.Background()) }()

	// Даём серверу применить MaxBytesHandler.
	time.Sleep(50 * time.Millisecond)

	// Тело больше лимита → 413.
	client := &http.Client{}
	body := strings.Repeat("x", 100)
	req, err := http.NewRequest(http.MethodPost, "http://"+rt.Addr().String()+"/", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.ContentLength = int64(len(body))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}
