package composition

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pkg-ru/dynamic"
	"github.com/pkg-ru/imager/config"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/internal/testutil"
)

// testConfigYAML — валидная конфигурация с path-policy "/" (разрешает пресет
// thumb на всех путях).
const testConfigYAML = `
version: "1"
policy:
  path-policies:
    "/":
      presets: [thumb]
  presets:
    - name: thumb
      crop: center
      width: 120
      height: 80
      output-formats: [webp]
processing:
  default-quality: 80
http:
  allowed-origins:
    - https://example.com
  cache-control: "public, max-age=31536000, immutable"
`

// memSourceStore — in-memory storage.SourceStore (алиас testutil).
type memSourceStore = testutil.MemSourceStore

func newMemSourceStore() *memSourceStore { return testutil.NewMemSourceStore() }

// memResultStore — in-memory storage.ResultStore (алиас testutil).
type memResultStore = testutil.MemResultStore

func newMemResultStore() *memResultStore { return testutil.NewMemResultStore() }

func TestBuildFailFastInvalidConfig(t *testing.T) {
	// Невалидная версия.
	cfg := &config.Config{Version: dynamic.String("999")}
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
	sources.Add(object.ObjectKey("img.png"), []byte("RAWIMAGE"))

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

	// Полный запрос через handler. Пресет thumb (crop center, 120x80,
	// output webp) разрешается path-policy "/".
	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "RAWIMAGE" {
		t.Errorf("body = %q, want RAWIMAGE", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type = %q, want image/webp", ct)
	}
}

// TestBuildWithMetadataEnabled проверяет сборку pipeline с включённым
// sidecar-кэшем метаданных (MetadataEnabled + Detector).
func TestBuildWithMetadataEnabled(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(testConfigYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	cfg, httpCfg := rc.Pipeline, rc.HTTP

	sources := newMemSourceStore()
	sources.Add(object.ObjectKey("img.png"), []byte("RAWIMAGE"))
	results := newMemResultStore()

	app, err := Build(context.Background(), AppOptions{
		Config:          cfg,
		HTTP:            httpCfg,
		Processor:       fakeProcessor{},
		Sources:         sources,
		Results:         results,
		MetadataEnabled: true,
		Detector:        fakeDetector{},
	})
	if err != nil {
		t.Fatalf("Build with metadata enabled: %v", err)
	}
	if app.Handler == nil || app.Service == nil {
		t.Fatal("Build returned nil handler/service")
	}
}

// TestBuildMetadataDisabledNoDetector проверяет, что без детектора
// metadata-кэш не создаётся (best-effort, сборка не падает).
func TestBuildMetadataDisabledNoDetector(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(testConfigYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	cfg, httpCfg := rc.Pipeline, rc.HTTP

	app, err := Build(context.Background(), AppOptions{
		Config:          cfg,
		HTTP:            httpCfg,
		Processor:       fakeProcessor{},
		Sources:         newMemSourceStore(),
		Results:         newMemResultStore(),
		MetadataEnabled: true,
		Detector:        nil,
	})
	if err != nil {
		t.Fatalf("Build with metadata enabled but no detector: %v", err)
	}
	if app.Handler == nil || app.Service == nil {
		t.Fatal("Build returned nil handler/service")
	}
}
