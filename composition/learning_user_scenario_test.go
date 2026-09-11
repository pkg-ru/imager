package composition

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testConfigYAMLLearningUserScenario — конфиг, воспроизводящий production
// сценарий пользователя: learning-mode включён в generate-local.yaml (глубокий
// merge поверх generate.yaml с learning-mode: false), path-policy "/"
// разрешает пресет test и custom "x" (оригинальный размер). Запрос
// /test/my-png/x.webp ПОКРЫТ политикой, /test/my-png/50x50.webp — НЕ покрыт
// (custom 50x50 отсутствует). Путь Path() в обоих случаях = "test" (каталог
// исходника, грамматика /{path}/{source}-{fmt}/{segment}.{out}).
// Оба запроса при включённом learning-mode должны:
//   - генерироваться и отдаваться клиенту (bypass admission для непокрытых);
//   - наблюдаться Recorder'ом: путь /test/my-png + размеры x и 50x50 должны
//     попасть в generate-local.yaml после Learning.Stop().
func TestIntegrationLearningModeUserScenario(t *testing.T) {
	cfgDir := t.TempDir()
	// Эмуляция docker-compose: generate-local.yaml поверх generate.yaml
	// (deep merge; learning-mode: true из local-файла побеждает false из base).
	generateYAML := strings.Replace(testConfigYAMLLearningUser,
		"learning-mode: false", "learning-mode: true", 1)
	rc, err := ParseRuntimeConfig([]byte(generateYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(merged): %v", err)
	}
	app, srcDir, _ := buildFSApp(t, func(o *AppOptions) {
		o.ConfigDir = cfgDir
		o.Config = rc.Pipeline
	})
	seedSource(t, srcDir, "test/my-png.png", []byte("RAWIMAGE"))
	if app.Learning == nil {
		t.Fatal("app.Learning == nil: expected learning service")
	}
	if !app.Learning.Enabled() {
		t.Fatal("learning-mode should be enabled from config")
	}

	// Запрос 1: покрытый политикой custom "x" (оригинальный размер).
	req1 := httptest.NewRequest(http.MethodGet, "/test/my-png-png/x.webp", nil)
	rec1 := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("x.webp: status = %d, want 200 (body=%q)", rec1.Code, rec1.Body.String())
	}

	// Запрос 2: НЕ покрытый политикой размер-грамматик 50x50 (bypass
	// admission при learning-mode).
	req2 := httptest.NewRequest(http.MethodGet, "/test/my-png-png/50x50.webp", nil)
	rec2 := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("50x50.webp: status = %d, want 200 (body=%q)", rec2.Code, rec2.Body.String())
	}

	// Stop() → drain + финальная запись наблюдений в generate-local.yaml.
	app.Learning.Stop()
	localFile := filepath.Join(cfgDir, "generate-local.yaml")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read %s: %v", localFile, err)
	}
	doc := string(data)
	// Path() = "test" (каталог исходника /test/my-png-png/x.webp).
	if !strings.Contains(doc, "/test:") {
		t.Errorf("generate-local.yaml missing observed path /test:\n%s", doc)
	}
	if !strings.Contains(doc, "50x50") {
		t.Errorf("generate-local.yaml missing observed size 50x50:\n%s", doc)
	}
}

// TestIntegrationLearningModeExtendsOutputFormats — воспроизведение бага
// пользователя: запросы одного размера с разными форматами (+ @2-запрос)
// должны пополнять output-formats существующего custom, а не теряться.
// Custom 220x200 в политике отсутствует → создаётся learning-mode'ом.
func TestIntegrationLearningModeExtendsOutputFormats(t *testing.T) {
	cfgDir := t.TempDir()
	rc, err := ParseRuntimeConfig([]byte(testConfigYAMLLearning))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(learning): %v", err)
	}
	app, srcDir, _ := buildFSApp(t, func(o *AppOptions) {
		o.ConfigDir = cfgDir
		o.Config = rc.Pipeline
	})
	seedSource(t, srcDir, "test/my-png.png", []byte("RAWIMAGE"))

	// Последовательные запросы одного размера с разными форматами,
	// включая @2-запрос (суффикс отделяется парсером, сегмент — 220x200).
	urls := []string{
		"/test/my-png-png/220x200.gif",
		"/test/my-png-png/220x200.jpg",
		"/test/my-png-png/220x200.webp",
		"/test/my-png-png/220x200@2.gif",
	}
	for _, u := range urls {
		req := httptest.NewRequest(http.MethodGet, u, nil)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body=%q)", u, rec.Code, rec.Body.String())
		}
	}

	app.Learning.Stop()
	localFile := filepath.Join(cfgDir, "generate-local.yaml")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read %s: %v", localFile, err)
	}
	doc := string(data)
	if !strings.Contains(doc, "220x200:") {
		t.Errorf("generate-local.yaml missing custom 220x200:\n%s", doc)
	}
	// Flow-style: форматы выводятся в одну строку "output-formats: [...]"
	// (yaml.v3 Style: FlowStyle), а не block-списком "- webp".
	for _, f := range []string{"gif", "jpg", "webp"} {
		if n := strings.Count(doc, f); n != 1 {
			t.Errorf("format %q occurs %d times, want 1:\n%s", f, n, doc)
		}
	}
	if !strings.Contains(doc, "output-formats: [gif, jpg, webp]") {
		t.Errorf("expected flow-style output-formats [gif, jpg, webp]:\n%s", doc)
	}
}

// TestIntegrationLearningModePresetObservation — пресет с дефисом в имени
// (face-fix), НЕ покрытый path-policies: при learning-mode запрос
// /test/my-png/face-fix.png должен (1) генерироваться и отдаваться клиенту,
// (2) после Learning.Stop() попадать в generate-local.yaml как presets
// записи пути /test (flow-style).
func TestIntegrationLearningModePresetObservation(t *testing.T) {
	cfgDir := t.TempDir()
	rc, err := ParseRuntimeConfig([]byte(testConfigYAMLLearningPreset))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(preset): %v", err)
	}
	app, srcDir, _ := buildFSApp(t, func(o *AppOptions) {
		o.ConfigDir = cfgDir
		o.Config = rc.Pipeline
	})
	seedSource(t, srcDir, "test/my-png.png", []byte("RAWIMAGE"))
	if !app.Learning.Enabled() {
		t.Fatal("learning-mode should be enabled from config")
	}

	// URL пользователя: сегмент face-fix, source my-png (source name
	// "my-png" + format png — разделение по последнему дефису), выходной
	// формат png.
	req := httptest.NewRequest(http.MethodGet, "/test/my-png-png/face-fix.png", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("face-fix.png: status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}

	app.Learning.Stop()
	localFile := filepath.Join(cfgDir, "generate-local.yaml")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read %s: %v", localFile, err)
	}
	doc := string(data)
	if !strings.Contains(doc, "/test:") {
		t.Errorf("generate-local.yaml missing observed path /test:\n%s", doc)
	}
	// Пресет face-fix попадает в presets path-policy (flow-style список).
	if !strings.Contains(doc, "presets: [face-fix]") {
		t.Errorf("generate-local.yaml missing presets: [face-fix]:\n%s", doc)
	}
}

// testConfigYAMLLearningPreset — конфиг с пресетом face-fix, не покрытым
// path-policies: learning-mode должен наблюдать его и разрешать генерацию.
const testConfigYAMLLearningPreset = `
version: "1"
policy:
  learning-mode: true
  presets:
    face-fix:
      crop: face-fix
      width: 200
      height: 200
      output-formats: [webp, avif, jpg, png, gif]
      quality: 85
      dpr: 1
encoders:
  default-quality: 80
`

// testConfigYAMLLearningUser — базовый конфиг, поверх которого логика
// LoadConfigDir теста накатывает generate-local.yaml (как в docker-compose).
const testConfigYAMLLearningUser = `
version: "1"
policy:
  learning-mode: false
  presets:
    test:
      width: 200
      height: 200
      output-formats: [webp, avif, png, gif]
      quality: 85
      dpr: 1
  path-policies:
    "/":
      presets: [test]
      customs:
        x:
          output-formats: [webp, avif, png, gif]
encoders:
  default-quality: 80
http:
  cache-control: "public, max-age=31536000, immutable"
`
