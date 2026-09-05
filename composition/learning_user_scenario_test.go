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
