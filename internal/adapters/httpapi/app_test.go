package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pkg-ru/imager/internal/config"
)

// testConfigYAML — валидная конфигурация с unsafe policy (разрешает всё).
const testConfigYAML = `
version: "1"
policy:
  global:
    authorization: unsafe
  presets:
    - name: thumb
      transform: c
      size: 120x80
      output-format: webp
processing:
  default-quality: 80
http:
  allowed-origins:
    - https://example.com
  cache-control: "public, max-age=31536000, immutable"
`

func TestBuildFailFastInvalidConfig(t *testing.T) {
	// Невалидная версия.
	cfg := &config.Config{Version: "999"}
	_, err := Build(context.Background(), AppOptions{
		Config:    cfg,
		Processor: fakeProcessor{},
	})
	if err == nil {
		t.Fatal("Build with invalid config should fail")
	}
}

func TestBuildFailFastNilConfig(t *testing.T) {
	_, err := Build(context.Background(), AppOptions{})
	if err == nil {
		t.Fatal("Build with nil config should fail")
	}
}

func TestBuildFailFastNilProcessor(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(testConfigYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	_, err = Build(context.Background(), AppOptions{
		Config:    rc.Pipeline,
		Sources:   newMemSourceStore(),
		Results:   newMemResultStore(),
		Processor: nil,
	})
	if err == nil {
		t.Fatal("Build with nil processor should fail")
	}
}

func TestBuildFullPipeline(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(testConfigYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	cfg, httpCfg := rc.Pipeline, rc.HTTP

	sources := newMemSourceStore()
	// Исходник: img.png (source key = "img.png").
	sources.data["img.png"] = []byte("RAWIMAGE")

	results := newMemResultStore()

	app, err := Build(context.Background(), AppOptions{
		Config:    cfg,
		HTTP:      httpCfg,
		Processor: fakeProcessor{},
		Sources:   sources,
		Results:   results,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if app.Handler == nil || app.Service == nil {
		t.Fatal("Build returned nil handler/service")
	}

	// Полный запрос через handler.
	req := httptest.NewRequest(http.MethodGet, "/v1/img-png/c-120x80@2.png", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "RAWIMAGE" {
		t.Errorf("body = %q, want RAWIMAGE", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
}
