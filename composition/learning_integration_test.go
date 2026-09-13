package composition

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testConfigYAMLLearning — конфиг с включённым learning-mode.
const testConfigYAMLLearning = `
version: "1"
policy:
  learning-mode: true
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

// TestIntegrationLearningModeFS — end-to-end проверка learning-mode:
//
//  1. learning-mode: true в конфиге → контроллер включён;
//  2. запрос к пути "forbidden" (не покрыт path-policy "/", которая разрешает
//     только пресет thumb), сегмент — размер-грамматика "120x60" →
//     генерируется (bypass admission) и отдаётся клиенту;
//  3. результат НЕ сохраняется в ResultStore (learning-mode);
//  4. после Learning.Stop() наблюдение (path "/forbidden" + размер 120x60)
//     записывается в generate-local.yaml внутри ConfigDir.
func TestIntegrationLearningModeFS(t *testing.T) {
	cfgDir := t.TempDir()
	app, srcDir, _ := buildFSApp(t, func(o *AppOptions) {
		o.ConfigDir = cfgDir
		rc, err := ParseRuntimeConfig([]byte(testConfigYAMLLearning))
		if err != nil {
			t.Fatalf("ParseRuntimeConfig(learning): %v", err)
		}
		o.Config = rc.Pipeline
	})
	seedSource(t, srcDir, "forbidden/zone/img.png", []byte("RAWIMAGE"))
	if app.Learning == nil {
		t.Fatal("app.Learning == nil: expected learning service")
	}
	if !app.Learning.Enabled() {
		t.Fatal("learning-mode should be enabled from config")
	}

	req := httptest.NewRequest(http.MethodGet, "/forbidden/zone/img-png/120x60.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "RAWIMAGE" {
		t.Errorf("body = %q, want RAWIMAGE", body)
	}

	// Результат не должен быть сохранён в ResultStore.
	stats, err := app.Results.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 0 {
		t.Fatalf("result objects = %d, want 0 (learning-mode must not publish)", stats.Objects)
	}

	// Stop() → drain + финальная запись наблюдений в generate-local.yaml
	// + персистентный сброс learning-mode: false (graceful shutdown).
	app.Learning.Stop()
	localFile := filepath.Join(cfgDir, "generate-local.yaml")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read %s: %v", localFile, err)
	}
	doc := string(data)
	if !strings.Contains(doc, "learning-mode: false") {
		t.Errorf("generate-local.yaml missing learning-mode: false (reset on shutdown):\n%s", doc)
	}
	if strings.Contains(doc, "learning-mode: true") {
		t.Errorf("generate-local.yaml still has learning-mode: true after Stop:\n%s", doc)
	}
	if !strings.Contains(doc, "/forbidden") {
		t.Errorf("generate-local.yaml missing observed path /forbidden:\n%s", doc)
	}
	if !strings.Contains(doc, "120x60") {
		t.Errorf("generate-local.yaml missing observed size 120x60:\n%s", doc)
	}
}

// TestIntegrationLearningModeResetOnStop — graceful shutdown (Service.Stop)
// персистентно сбрасывает learning-mode: даже если флаг был включён
// (learning-mode: true в конфиге и generate-local.yaml), после Stop()
// в generate-local.yaml записывается learning-mode: false, чтобы после
// перезапуска сервер не продолжал работу в learning-режиме.
func TestIntegrationLearningModeResetOnStop(t *testing.T) {
	cfgDir := t.TempDir()
	// Пред-существующий generate-local.yaml с learning-mode: true (как при
	// реальном включении режима).
	initial := "policy:\n  learning-mode: true\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "generate-local.yaml"), []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	rc, err := ParseRuntimeConfig([]byte(testConfigYAMLLearning))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(learning): %v", err)
	}
	app, _, _ := buildFSApp(t, func(o *AppOptions) {
		o.ConfigDir = cfgDir
		o.Config = rc.Pipeline
	})
	if app.Learning == nil {
		t.Fatal("app.Learning == nil: expected learning service")
	}
	if !app.Learning.Enabled() {
		t.Fatal("learning-mode should be enabled from config")
	}

	// Stop() — то, что выполняется при graceful shutdown (learningCloser).
	app.Learning.Stop()
	if app.Learning.Enabled() {
		t.Error("learning-mode must be disabled after Stop")
	}

	localFile := filepath.Join(cfgDir, "generate-local.yaml")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read %s: %v", localFile, err)
	}
	doc := string(data)
	if !strings.Contains(doc, "learning-mode: false") {
		t.Errorf("generate-local.yaml missing learning-mode: false after shutdown:\n%s", doc)
	}
	if strings.Contains(doc, "learning-mode: true") {
		t.Errorf("generate-local.yaml still has learning-mode: true after shutdown:\n%s", doc)
	}
}

// TestIntegrationLearningModeOffNoLocalFileWrites — при learning-mode: false
// и заданном ConfigDir вся цепочка обучения отключена:
//   - Recorder не создаётся → generate-local.yaml НЕ читается и НЕ пишется:
//     пред-существующий файл остаётся байт-в-байт неизменным, отсутствующий
//     файл не создаётся (даже после Learning.Stop() при shutdown);
//   - отслеживание запросов не подключено (запросы не порождают наблюдений,
//     запрещённый путь остаётся 403 — bypass admission не активен).
func TestIntegrationLearningModeOffNoLocalFileWrites(t *testing.T) {
	cfgDir := t.TempDir()
	localFile := filepath.Join(cfgDir, "generate-local.yaml")
	// Пред-существующий generate-local.yaml (как после прошлого запуска в
	// learning-режиме): при learning-mode: false он должен остаться нетронутым.
	initial := "policy:\n  learning-mode: false\n  path-policies:\n    \"/\":\n      presets: [thumb]\n"
	if err := os.WriteFile(localFile, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	app, srcDir, _ := buildFSApp(t, func(o *AppOptions) {
		o.ConfigDir = cfgDir
	})
	seedSource(t, srcDir, "forbidden/img.png", []byte("RAWIMAGE"))
	if app.Learning == nil {
		t.Fatal("app.Learning == nil: expected learning service (controller only)")
	}
	if app.Learning.Enabled() {
		t.Fatal("learning-mode should be disabled (policy.learning-mode: false)")
	}

	// Запросы к запрещённому пути: отслеживание отключено, наблюдений нет,
	// bypass admission не активен → 403.
	for _, u := range []string{"/forbidden/img-png/120x60.webp", "/forbidden/img-png/x.webp"} {
		req := httptest.NewRequest(http.MethodGet, u, nil)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403 (learning disabled)", u, rec.Code)
		}
	}

	// Stop() — то, что выполняется при graceful shutdown (learningCloser):
	// не должен ни читать, ни перезаписывать generate-local.yaml.
	app.Learning.Stop()
	if app.Learning.Enabled() {
		t.Error("learning-mode must be disabled after Stop")
	}

	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read %s: %v", localFile, err)
	}
	if string(data) != initial {
		t.Errorf("generate-local.yaml was modified with learning-mode: false:\n--- want ---\n%s\n--- got ---\n%s", initial, string(data))
	}
}

// TestIntegrationLearningModeOffNoLocalFileCreated — при learning-mode: false
// отсутствующий generate-local.yaml не должен создаваться ни при запросах,
// ни при Learning.Stop() (shutdown).
func TestIntegrationLearningModeOffNoLocalFileCreated(t *testing.T) {
	cfgDir := t.TempDir()
	app, srcDir, _ := buildFSApp(t, func(o *AppOptions) {
		o.ConfigDir = cfgDir
	})
	seedSource(t, srcDir, "forbidden/img.png", []byte("RAWIMAGE"))
	if app.Learning.Enabled() {
		t.Fatal("learning-mode should be disabled (policy.learning-mode: false)")
	}

	req := httptest.NewRequest(http.MethodGet, "/forbidden/img-png/120x60.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (learning disabled)", rec.Code)
	}

	app.Learning.Stop()
	localFile := filepath.Join(cfgDir, "generate-local.yaml")
	if _, err := os.Stat(localFile); !os.IsNotExist(err) {
		t.Errorf("generate-local.yaml must not be created with learning-mode: false (stat err: %v)", err)
	}
}

// TestIntegrationLearningModeOffStillForbidden — regression: без learning-mode
// (флаг выключен) запрещённый путь остаётся 403.
func TestIntegrationLearningModeOffStillForbidden(t *testing.T) {
	app, srcDir, _ := buildFSApp(t) // ConfigDir = "" → Recorder не создаётся, флаг выключен
	seedSource(t, srcDir, "forbidden/img.png", []byte("RAWIMAGE"))
	if app.Learning.Enabled() {
		t.Fatal("learning-mode should be disabled by default config")
	}

	req := httptest.NewRequest(http.MethodGet, "/forbidden/img-png/120x60.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (learning disabled)", rec.Code)
	}
	assertErrorCode(t, rec, "forbidden")
}
