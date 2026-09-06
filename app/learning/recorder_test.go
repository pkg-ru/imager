package learning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/policy"
	"gitverse.ru/pkg-ru/imager/observability"
)

// newTestRequest — segment-запрос для тестов.
func newTestRequest(t *testing.T, path, sourceName, sourceFormat, segment, format string) *asset.Request {
	t.Helper()
	req, err := asset.NewSegmentRequest(
		path,
		asset.SourceName(sourceName),
		asset.Format(sourceFormat),
		asset.SegmentName(segment),
		0,
		asset.Format(format),
	)
	if err != nil {
		t.Fatalf("NewSegmentRequest: %v", err)
	}
	return req
}

func TestRecorderObserveExtractsPathSizeFormat(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// req.Path() — каталог исходника БЕЗ source-файла и сегмента
	// (грамматика: /{path}/{source_name}-{source_format}/{segment}.{out};
	// домен хранит только {path}). Префикс path-policy = нормализованный
	// Path() с ведущим "/". Regression: раньше pathPrefix срезал последний
	// сегмент, и наблюдения для одно-сегментных путей (/test/...) молча
	// отбрасывались — generate-local.yaml не пополнялся.
	req := newTestRequest(t, "test", "my-png", "png", "120x60", "webp")
	rec.Observe(req)
	rec.Stop()

	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read generate-local.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "/test:") {
		t.Errorf("path prefix missing:\n%s", got)
	}
	if !strings.Contains(got, "120x60:") {
		t.Errorf("size custom missing:\n%s", got)
	}
	if !strings.Contains(got, "webp") {
		t.Errorf("format missing:\n%s", got)
	}
}

func TestRecorderObserveNestedPath(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Вложенный путь: полный {path} сохраняется целиком (без среза
	// последнего сегмента, как раньше).
	req := newTestRequest(t, "a/b/c", "img", "jpg", "120x60", "webp")
	rec.Observe(req)
	rec.Stop()

	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read generate-local.yaml: %v", err)
	}
	if got := string(data); !strings.Contains(got, "/a/b/c:") {
		t.Errorf("nested path prefix missing:\n%s", got)
	}
}

func TestRecorderObserveEmptyPathIgnored(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Путь пуст (исходник в корне): наблюдение игнорируется — path-policy
	// "/" это fallback, не правило.
	req := newTestRequest(t, "", "img", "jpg", "120x60", "webp")
	rec.Observe(req)
	rec.Stop()

	if _, err := os.Stat(filepath.Join(dir, localFileName)); !os.IsNotExist(err) {
		t.Errorf("expected no generate-local.yaml for empty path, err = %v", err)
	}
}

func TestRecorderObservePresetObservation(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{
		ConfigDir: dir,
		PresetNames: map[string]struct{}{
			"face-fix": {}, "face": {}, "object": {}, "smart": {},
		},
		Logger: observability.NopLogger(),
	})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Сегмент — известный пресет (не размер-грамматика): наблюдение
	// пополняет presets path-policy.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "face-fix", "webp"))
	// Дубликат — no-op.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "face-fix", "jpg"))
	// Другой пресет того же пути — дополняет список.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "face", "webp"))
	rec.Stop()

	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read generate-local.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "/test:") {
		t.Errorf("path prefix missing:\n%s", got)
	}
	// Flow-style список пресетов, отсортированный, без дубликатов.
	if !strings.Contains(got, "presets: [face, face-fix]") {
		t.Errorf("expected presets: [face, face-fix]:\n%s", got)
	}
}

func TestRecorderIgnoresUnknownPreset(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{
		ConfigDir:   dir,
		PresetNames: map[string]struct{}{"face-fix": {}},
		Logger:      observability.NopLogger(),
	})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Имя не из PresetNames и не размер — наблюдение отбрасывается.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "unknown-preset", "webp"))
	rec.Stop()

	if _, err := os.Stat(filepath.Join(dir, localFileName)); !os.IsNotExist(err) {
		t.Errorf("expected no generate-local.yaml for unknown preset, err = %v", err)
	}
}

func TestRecorderIgnoresInvalidSize(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Имя пресета (не размер-грамматика) — наблюдение игнорируется.
	req := newTestRequest(t, "a/b/img-jpg", "img", "jpg", "banner", "webp")
	rec.Observe(req)
	rec.Stop()

	if _, err := os.Stat(filepath.Join(dir, localFileName)); !os.IsNotExist(err) {
		t.Errorf("expected no generate-local.yaml for invalid size, err = %v", err)
	}
}

func TestRecorderStopFinalWrite(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Дебаунс: запись не выполняется сразу после Observe (файл появляется
	// не раньше writeInterval).
	rec.Observe(newTestRequest(t, "a/b", "img", "jpg", "120x60", "webp"))
	time.Sleep(100 * time.Millisecond)
	if info, err := os.Stat(filepath.Join(dir, localFileName)); err == nil {
		if time.Since(info.ModTime()) < writeInterval-500*time.Millisecond {
			t.Errorf("file written before debounce interval: modTime %v", info.ModTime())
		}
	}
	// Stop() делает финальную запись.
	rec.Stop()
	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read after Stop: %v", err)
	}
	if !strings.Contains(string(data), "/a/b:") {
		t.Errorf("path missing after Stop:\n%s", string(data))
	}
}

func TestRecorderDebounce(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Поток наблюдений: несколько подряд — запись не чаще 1 раза в 2с.
	for i := 0; i < 5; i++ {
		rec.Observe(newTestRequest(t, "a/b/img-jpg", "img", "jpg", "120x60", "webp"))
		time.Sleep(20 * time.Millisecond)
	}
	// До истечения дебаунса файла может не быть; после Stop — должен быть.
	rec.Stop()
	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read after Stop: %v", err)
	}
	if !strings.Contains(string(data), "120x60:") {
		t.Errorf("custom missing:\n%s", string(data))
	}
}

func TestRecorderInitialPathPolicies(t *testing.T) {
	dir := t.TempDir()
	initial := policy.Config{
		PathPolicies: map[string]policy.PathPolicyConfig{
			"/existing": {Customs: customs([2]any{"100x100", sizeCustom("avif")})},
		},
	}
	rec, err := NewRecorder(Deps{ConfigDir: dir, Initial: initial, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	rec.Stop()
	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "/existing:") || !strings.Contains(got, "avif") {
		t.Errorf("initial path-policies not written:\n%s", got)
	}
}

func TestRecorderWriteErrorRetries(t *testing.T) {
	dir := t.TempDir()
	// Файл-каталог: запись в generate-local.yaml упадёт.
	blocked := filepath.Join(dir, localFileName)
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	rec.Observe(newTestRequest(t, "a/b/img-jpg", "img", "jpg", "120x60", "webp"))
	rec.Stop() // финальная запись упадёт (ERROR лог), состояние в памяти сохраняется

	// Убираем блокировку — новое наблюдение ретраит запись.
	if err := os.Remove(blocked); err != nil {
		t.Fatalf("remove: %v", err)
	}
	rec2, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder 2: %v", err)
	}
	rec2.Observe(newTestRequest(t, "a/b/img-jpg", "img", "jpg", "120x60", "webp"))
	rec2.Stop()
	data, err := os.ReadFile(blocked)
	if err != nil {
		t.Fatalf("read after retry: %v", err)
	}
	if !strings.Contains(string(data), "120x60:") {
		t.Errorf("retry write missing custom:\n%s", string(data))
	}
}

// TestRecorderExtendsExistingCustomFormats — воспроизведение бага: первый
// запрос создаёт custom с одним форматом, последующие запросы того же размера
// с ДРУГИМИ форматами должны ДОПОЛНЯТЬ output-formats (а не теряться).
func TestRecorderExtendsExistingCustomFormats(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Запрос 1: создаёт custom 220x200 с gif.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "220x200", "gif"))
	// Запрос 2 и 3: тот же размер, другие форматы.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "220x200", "jpg"))
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "220x200", "webp"))
	// Идемпотентность: повтор того же формата не дублирует.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "220x200", "gif"))
	rec.Stop()

	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read generate-local.yaml: %v", err)
	}
	got := string(data)
	for _, want := range []string{"/test:", "220x200:", "gif", "jpg", "webp"} {
		if !strings.Contains(got, want) {
			t.Errorf("generate-local.yaml missing %q:\n%s", want, got)
		}
	}
	// Flow-style: все форматы в одной строке "output-formats: [...]",
	// ровно по одному вхождению каждого формата.
	for _, f := range []string{"gif", "jpg", "webp"} {
		if n := strings.Count(got, f); n != 1 {
			t.Errorf("format %q occurs %d times, want 1:\n%s", f, n, got)
		}
	}
	if !strings.Contains(got, "output-formats: [gif, jpg, webp]") {
		t.Errorf("expected flow-style output-formats [gif, jpg, webp]:\n%s", got)
	}
}

// TestRecorderObserveDPRSuffixMapsToBaseCustom — запрос с @dpr-суффиксом
// (например /test/my-png/220x200@2.gif) по дизайну проекта маппится на
// wildcard-custom "220x200" (custom без dpr покрывает @2/@3): формат gif
// должен попасть в output-formats записи 220x200.
func TestRecorderObserveDPRSuffixMapsToBaseCustom(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Сначала запрос без @2 — создаёт custom 220x200 с gif.
	rec.Observe(newTestRequest(t, "test", "my-png", "png", "220x200", "gif"))
	// Затем запрос с @2 — парсер отделяет @2 в DPR, сегмент остаётся 220x200.
	req, err := asset.Parse("/test/my-png-png/220x200@2.webp")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rec.Observe(req)
	rec.Stop()

	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read generate-local.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "220x200:") {
		t.Errorf("base custom 220x200 missing:\n%s", got)
	}
	if strings.Contains(got, "220x200@2") {
		t.Errorf("unexpected @2 custom key (design: wildcard custom 220x200):\n%s", got)
	}
	if !strings.Contains(got, "webp") {
		t.Errorf("webp format from @2 request missing:\n%s", got)
	}
}

func TestController(t *testing.T) {
	c := NewController()
	if c.Enabled() {
		t.Error("new controller must be disabled")
	}
	if !c.Enable() {
		t.Error("Enable must return true")
	}
	if !c.Enabled() {
		t.Error("controller must be enabled after Enable")
	}
	c.Disable()
	if c.Enabled() {
		t.Error("controller must be disabled after Disable")
	}
}

func TestServiceObserveGatedByController(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	svc := NewService(NewController(), rec)
	// Выключен: наблюдение не принимается.
	svc.Observe(newTestRequest(t, "a/b/img-jpg", "img", "jpg", "120x60", "webp"))
	svc.Stop()
	if _, err := os.Stat(filepath.Join(dir, localFileName)); !os.IsNotExist(err) {
		t.Errorf("expected no file while disabled, err = %v", err)
	}

	// Включён: наблюдение принимается.
	rec2, err := NewRecorder(Deps{ConfigDir: dir, Logger: observability.NopLogger()})
	if err != nil {
		t.Fatalf("NewRecorder 2: %v", err)
	}
	svc2 := NewService(NewController(), rec2)
	svc2.controller.Enable()
	svc2.Observe(newTestRequest(t, "a/b/img-jpg", "img", "jpg", "120x60", "webp"))
	svc2.Stop()
	data, err := os.ReadFile(filepath.Join(dir, localFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "120x60:") {
		t.Errorf("custom missing:\n%s", string(data))
	}
}
