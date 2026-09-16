//go:build onnx

package detection

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Тесты кроссплатформенного списка кандидатов библиотеки ONNX Runtime
// (ort_library.go). Проверяют:
//   - имена файлов для каждой ОС (.so / .dll / .dylib), включая
//     версионированные glob-паттерны (НЕ привязанные к минорной версии);
//   - раскрытие glob-паттернов (expandORTCandidates) и выбор первого
//     СУЩЕСТВУЮЩЕГО кандидата (firstExisting);
//   - автодетект при пустом detection.onnx-runtime-lib (autodetectORTLib);
//   - fallback с несуществующего конфиг-пути (ortLibPathForInit);
//   - приоритет существующего пути из конфига над автодетектом.
//
// TestRequireORTLibraryFound при отсутствии библиотеки на машине ПАДАЕТ
// с t.Fatalf и списком проверенных путей — по требованию: сборка/тесты
// с тегом onnx обязаны явно сигнализировать о недоступной библиотеке.
//
// Файл собран с тегом "onnx", но не требует cgo, поэтому запускается и в
// сборках с CGO_ENABLED=0.

func TestORTLibraryCandidatesForCurrentOS(t *testing.T) {
	cands := ortLibraryCandidates()
	if len(cands) == 0 {
		t.Fatal("ortLibraryCandidates() returned empty list")
	}
	switch runtime.GOOS {
	case "windows":
		assertHasPath(t, cands, "onnxruntime.dll")
		for _, c := range cands {
			if strings.HasSuffix(strings.ToLower(c), ".so") || strings.HasSuffix(c, ".dylib") {
				t.Errorf("windows candidate has non-.dll extension: %q", c)
			}
		}
	case "darwin":
		assertHasPath(t, cands, "libonnxruntime.dylib")
		assertHasGlob(t, cands, "libonnxruntime.*.dylib")
		for _, c := range cands {
			if strings.HasSuffix(strings.ToLower(c), ".so") || strings.HasSuffix(strings.ToLower(c), ".dll") {
				t.Errorf("darwin candidate has non-.dylib extension: %q", c)
			}
		}
	default: // linux и прочие unix
		assertHasPath(t, cands, join("/usr/lib", "libonnxruntime.so"))
		assertHasGlob(t, cands, join("/usr/lib", "libonnxruntime.so.*"))
		assertHasPath(t, cands, join("/usr/local/lib", "libonnxruntime.so"))
		assertHasPath(t, cands, join("/opt/onnxruntime", "lib", "libonnxruntime.so"))
		assertHasPath(t, cands, "onnxruntime.so")
		assertHasPath(t, cands, "libonnxruntime.so")
		for _, c := range cands {
			if strings.HasSuffix(strings.ToLower(c), ".dll") || strings.HasSuffix(c, ".dylib") {
				t.Errorf("linux candidate has non-.so extension: %q", c)
			}
		}
	}
}

// join — локальная обёртка filepath.Join для компактности ожиданий.
// Пути строятся тем же способом, что и кандидаты в ort_library.go, поэтому
// тесты проходят на любой ОС-хосте (на Windows filepath.Join даёт "\").
func join(elems ...string) string {
	return filepath.Join(elems...)
}

func TestORTLinuxCandidates(t *testing.T) {
	cands := ortLinuxCandidates()
	assertHasPath(t, cands, join("/usr/lib", "libonnxruntime.so"))
	assertHasGlob(t, cands, join("/usr/lib", "libonnxruntime.so.*"))
	assertHasPath(t, cands, join("/usr/local/lib", "libonnxruntime.so"))
	assertHasGlob(t, cands, join("/usr/local/lib", "libonnxruntime.so.*"))
	assertHasPath(t, cands, join("/usr/lib", "x86_64-linux-gnu", "libonnxruntime.so"))
	assertHasPath(t, cands, join("/opt/onnxruntime", "lib", "libonnxruntime.so"))
	assertHasPath(t, cands, "onnxruntime.so")
	assertHasPath(t, cands, "libonnxruntime.so")
}

func TestORTWindowsCandidates(t *testing.T) {
	cands := ortWindowsCandidates()
	assertHasPath(t, cands, "onnxruntime.dll")
	// Каталог рядом с exe: должен быть абсолютным и содержать onnxruntime.dll.
	if exe, err := os.Executable(); err == nil {
		assertHasPath(t, cands, filepath.Join(filepath.Dir(exe), "onnxruntime.dll"))
	}
	// %WINDIR%\System32 — проверяем только если env задан (это Windows-тест;
	// на Linux контейнере WINDIR не установлен, поэтому кандидат не строится).
	if windir := os.Getenv("WINDIR"); windir != "" {
		assertHasPath(t, cands, filepath.Join(windir, "System32", "onnxruntime.dll"))
	}
}

func TestORTDarwinCandidates(t *testing.T) {
	cands := ortDarwinCandidates()
	assertHasPath(t, cands, "libonnxruntime.dylib")
	assertHasPath(t, cands, join("/usr/local/lib", "libonnxruntime.dylib"))
	assertHasGlob(t, cands, join("/usr/local/lib", "libonnxruntime.*.dylib"))
	assertHasPath(t, cands, join("/opt/homebrew/lib", "libonnxruntime.dylib"))
	assertHasGlob(t, cands, join("/opt/homebrew/lib", "libonnxruntime.*.dylib"))
	assertHasPath(t, cands, join("/opt/onnxruntime", "lib", "libonnxruntime.dylib"))
}

// TestExpandORTCandidates проверяет раскрытие glob-паттернов: версионные
// файлы (libonnxruntime.so.1.29.0, libonnxruntime.so.1) раскрываются из
// паттерна libonnxruntime.so.* по убыванию версии, не-паттерны переносятся
// как есть, дубликаты удаляются.
func TestExpandORTCandidates(t *testing.T) {
	dir := t.TempDir()
	v129 := filepath.Join(dir, "libonnxruntime.so.1.29.0")
	v1 := filepath.Join(dir, "libonnxruntime.so.1")
	plain := filepath.Join(dir, "libonnxruntime.so")
	for _, f := range []string{v1, v129, plain} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	// Не-glob кандидаты переносятся как есть (включая несуществующие):
	// фильтрацию по существованию выполняет firstExisting, чтобы голые
	// имена ("onnxruntime.so") искались и через ld.so/PATH.
	got := expandORTCandidates([]string{
		filepath.Join(dir, "libonnxruntime.so.*"),
		plain,
		plain, // дубликат
		filepath.Join(dir, "missing.so"),
	})
	want := []string{v129, v1, plain, filepath.Join(dir, "missing.so")} // версии по убыванию
	if len(got) != len(want) {
		t.Fatalf("expandORTCandidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("expandORTCandidates[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Автодетект по временной директории: паттерн без совпадений не даёт
	// элементов, некорректный паттерн ([) безопасно пропускается.
	if got := expandORTCandidates([]string{"[bad"}); len(got) != 0 {
		t.Errorf("expandORTCandidates with bad pattern = %v, want empty", got)
	}
}

// TestFirstExisting проверяет выбор первого СУЩЕСТВУЮЩЕГО файла из списка
// кандидатов на временных файлах (без обращения к системным путям).
func TestFirstExisting(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.so")
	b := filepath.Join(dir, "b.dll")
	c := filepath.Join(dir, "c.dylib")
	// Первый существующий — b (a не существует).
	if err := os.WriteFile(b, []byte("x"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	if err := os.WriteFile(c, []byte("x"), 0o644); err != nil {
		t.Fatalf("write c: %v", err)
	}
	if got := firstExisting([]string{a, b, c}); got != b {
		t.Errorf("firstExisting = %q, want %q", got, b)
	}
	// Каталог не считается файлом.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := firstExisting([]string{filepath.Join(dir, "sub"), c}); got != c {
		t.Errorf("firstExisting skipped dir, got %q, want %q", got, c)
	}
	// Ничего нет — пусто.
	if got := firstExisting([]string{a, filepath.Join(dir, "missing.so")}); got != "" {
		t.Errorf("firstExisting = %q, want empty", got)
	}
}

// TestAutodetectORTLib — автодетект при пустом detection.onnx-runtime-lib:
// autodetectORTLib возвращает первый реально существующий стандартный
// кандидат (на текущей машине) либо "". Сам по себе вызов не должен
// паниковать/падать независимо от наличия библиотеки.
func TestAutodetectORTLib(t *testing.T) {
	got := autodetectORTLib()
	if got == "" {
		return // библиотека не установлена — автодетект корректно пуст.
	}
	if !fileExists(got) {
		t.Errorf("autodetectORTLib = %q, but file does not exist", got)
	}
	// Возвращённый путь обязан быть одним из раскрытых кандидатов текущей ОС.
	for _, c := range expandORTCandidates(ortLibraryCandidates()) {
		if got == c {
			return
		}
	}
	t.Errorf("autodetectORTLib = %q is not in expanded candidates for %s", got, runtime.GOOS)
}

// TestOrtLibPathForInit проверяет приоритет выбора пути библиотеки:
//   - существующий путь из конфига всегда имеет приоритет над автодетектом;
//   - НЕсуществующий конфиг-путь заменяется автодетектом (fallback),
//     если автодетект нашёл библиотеку (на временных файлах через
//     поведение ortLibPathForInit: конфиг-путь в несуществующем каталоге
//     не возвращается, если найден автодетект-кандидат).
func TestOrtLibPathForInit(t *testing.T) {
	// Существующий путь из конфига: возвращается как есть.
	dir := t.TempDir()
	existing := filepath.Join(dir, "onnxruntime.so")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ortLibPathForInit(existing); got != existing {
		t.Errorf("ortLibPathForInit(%q) = %q, want %q (existing config path wins)", existing, got, existing)
	}
	// Пустой путь = автодетекция: результат совпадает с autodetectORTLib.
	if got := ortLibPathForInit(""); got != autodetectORTLib() {
		t.Errorf("ortLibPathForInit(\"\") = %q, want autodetectORTLib() = %q", got, autodetectORTLib())
	}
	// Несуществующий конфиг-путь без библиотеки на машине: возвращается как
	// есть (биндинг вернёт понятную ошибку именно по нему). Если библиотека
	// реально установлена — ortLibPathForInit вернёт автодетект-кандидат;
	// проверяем это отдельно, без завязки на окружение.
	missing := filepath.Join(dir, "missing", "libonnxruntime.so")
	got := ortLibPathForInit(missing)
	if got != missing && !fileExists(got) {
		t.Errorf("ortLibPathForInit(%q) = %q, which is neither the config path nor an existing file", missing, got)
	}
}

// TestRequireORTLibraryFound — ГЛАВНЫЙ тест по требованию: при сборке/тестах
// с тегом onnx библиотека ONNX Runtime обязана быть найдена автодетектом.
// Если не найдена — тест ПАДАЕТ с t.Fatalf и ПОЛНЫМ списком проверенных
// путей (раскрытых кандидатов), чтобы было ясно, где искать библиотеку.
// На машине без установленной библиотеки это осознанное падение, а не skip:
// тег onnx означает намерение работать с реальным ONNX Runtime.
func TestRequireORTLibraryFound(t *testing.T) {
	got := autodetectORTLib()
	if got == "" {
		cands := expandORTCandidates(ortLibraryCandidates())
		t.Fatalf("ONNX Runtime library NOT found (build tag onnx). "+
			"Install libonnxruntime (Linux: .so, Windows: onnxruntime.dll, macOS: .dylib) "+
			"or set detection.onnx-runtime-lib in config. Paths checked (%s):\n  - %s",
			runtime.GOOS, strings.Join(cands, "\n  - "))
	}
	if !fileExists(got) {
		t.Fatalf("autodetectORTLib = %q, but the file does not exist", got)
	}
	t.Logf("ONNX Runtime library found: %s", got)
}

// TestAutodetectLogHelpers проверяет, что лог-обёртки не паникуют при
// nil-логгере (логирование опционально, SetLogger может не вызываться).
func TestAutodetectLogHelpers(t *testing.T) {
	defer SetLogger(nil) // восстановить глобальное состояние
	SetLogger(nil)
	logORTInfo("autodetect info: %s", "probe")
	logORTWarn("autodetect warn: %s", "probe")
	// С непустым логгером — заглушка фиксирует вызовы.
	fake := &fakeDetectionLogger{}
	SetLogger(fake)
	logORTInfo("info %d", 1)
	logORTWarn("warn %d", 2)
	if fake.infos != 1 || fake.warns != 1 {
		t.Errorf("logger calls: infos=%d warns=%d, want 1/1", fake.infos, fake.warns)
	}
}

// fakeDetectionLogger — заглушка Logger для тестов.
type fakeDetectionLogger struct {
	infos, warns int
}

func (f *fakeDetectionLogger) Infof(string, ...any) { f.infos++ }
func (f *fakeDetectionLogger) Warnf(string, ...any) { f.warns++ }

// assertHasPath проверяет наличие элемента want в списке paths.
func assertHasPath(t *testing.T, paths []string, want string) {
	t.Helper()
	for _, p := range paths {
		if p == want {
			return
		}
	}
	t.Errorf("candidates %v missing %q", paths, want)
}

// assertHasGlob проверяет наличие кандидата-паттерна, у которого базовое
// имя совпадает с wantGlob (сравнение по filepath.Base, без учёта каталога).
func assertHasGlob(t *testing.T, paths []string, wantGlob string) {
	t.Helper()
	want := filepath.Base(wantGlob)
	for _, p := range paths {
		if filepath.Base(p) == want {
			return
		}
	}
	t.Errorf("candidates %v missing glob pattern %q", paths, want)
}
