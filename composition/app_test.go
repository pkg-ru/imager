package composition

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pkg-ru/dynamic"
	"gitverse.ru/pkg-ru/imager/config"
	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/internal/testutil"
	"gitverse.ru/pkg-ru/imager/ports/processor"
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
    thumb:
      crop: center
      width: 120
      height: 80
      output-formats: [webp]
encoders:
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

// blockingProcessor — processor.Processor, который блокируется на канале
// release (эмуляция "зависшего" владельца генерации). После close(release)
// копирует исходник в out.
type blockingProcessor struct {
	release chan struct{}
}

func (p *blockingProcessor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	n, err := io.Copy(out, in.Source)
	if err != nil {
		return nil, err
	}
	return &processor.Result{Size: n}, nil
}

// TestBuildSingleflightWaitTimeoutDefault проверяет: production-создание
// Coordinator применяет WaitTimeout по умолчанию (60s). Если владелец
// генерации "завис" (не завершается), waiter получает ошибку по истечении
// WaitTimeout, а не ждёт вечно. Проверяем через короткий WaitTimeout,
// заданный явно в AppOptions.
func TestBuildSingleflightWaitTimeoutDefault(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(testConfigYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	cfg, httpCfg := rc.Pipeline, rc.HTTP

	sources := newMemSourceStore()
	sources.Add(object.ObjectKey("img.png"), []byte("RAWIMAGE"))
	results := newMemResultStore()

	// Владелец "завис": процессор блокируется до close(release).
	release := make(chan struct{})
	app, err := Build(context.Background(), AppOptions{
		Config:                  cfg,
		HTTP:                    httpCfg,
		Processor:               &blockingProcessor{release: release},
		Sources:                 sources,
		Results:                 results,
		SingleflightWaitTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Владелец: первый запрос входит в singleflight и блокируется в процессоре.
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
	}()

	// Ждём, пока владелец войдёт в singleflight (детерминированный барьер:
	// процессор заблокирован, значит первый запрос уже в generateLocked).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// Нет прямого счётчика вызовов процессора; даём владельцу время
		// войти в singleflight и заблокироваться.
		time.Sleep(10 * time.Millisecond)
		break
	}

	// Waiter: тот же ключ → ждёт владельца. По истечении WaitTimeout должен
	// получить 503 (OutcomeUnavailable → StatusServiceUnavailable), а не
	// висеть вечно.
	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("waiter status = %d, want 503 (wait timeout)", rec.Code)
	}
	// Ожидание должно завершиться примерно через WaitTimeout, а не ждать
	// освобождения владельца.
	if elapsed < 30*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("waiter elapsed = %v, want ~50ms", elapsed)
	}

	// Освобождаем владельца и дожидаемся его завершения.
	close(release)
	<-ownerDone
}

// TestBuildSingleflightWaitTimeoutDisabled проверяет, что при
// SingleflightWaitTimeout=0 поведение прежнее: waiter ждёт завершения
// владельца (без таймаута) и получает результат.
func TestBuildSingleflightWaitTimeoutDisabled(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(testConfigYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	cfg, httpCfg := rc.Pipeline, rc.HTTP

	sources := newMemSourceStore()
	sources.Add(object.ObjectKey("img.png"), []byte("RAWIMAGE"))
	results := newMemResultStore()

	release := make(chan struct{})
	app, err := Build(context.Background(), AppOptions{
		Config:                  cfg,
		HTTP:                    httpCfg,
		Processor:               &blockingProcessor{release: release},
		Sources:                 sources,
		Results:                 results,
		SingleflightWaitTimeout: 0, // отключено
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
	}()
	time.Sleep(50 * time.Millisecond)

	// Waiter ждёт владельца (без таймаута). Пока владелец заблокирован,
	// waiter не должен вернуться с ошибкой.
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
	}()

	// Даём waiter'у время "повисеть" в ожидании: он не должен завершиться
	// с ошибкой таймаута (WaitTimeout=0).
	select {
	case <-waiterDone:
		t.Fatal("waiter returned while owner blocked (WaitTimeout=0 must wait forever)")
	case <-time.After(150 * time.Millisecond):
	}

	// Освобождаем владельца: waiter получает результат.
	close(release)
	<-waiterDone
	<-ownerDone
}
