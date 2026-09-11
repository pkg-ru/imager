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
//   - имена файлов для каждой ОС (.so / .dll / .dylib) и версионированные
//     варианты;
//   - выбор первого СУЩЕСТВУЮЩЕГО кандидата (firstExisting);
//   - автодетект при пустом detection.onnx-runtime-lib (autodetectORTLib);
//   - приоритет пути из конфига над автодетектом (выбор в initORT).
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
		assertHasPath(t, cands, "libonnxruntime."+ortLibVersion+".dylib")
		for _, c := range cands {
			if strings.HasSuffix(strings.ToLower(c), ".so") || strings.HasSuffix(strings.ToLower(c), ".dll") {
				t.Errorf("darwin candidate has non-.dylib extension: %q", c)
			}
		}
	default: // linux и прочие unix
		assertHasPath(t, cands, join("/usr/lib", "libonnxruntime.so"))
		assertHasPath(t, cands, join("/usr/lib", "libonnxruntime.so."+ortLibVersion))
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
	assertHasPath(t, cands, join("/usr/lib", "libonnxruntime.so."+ortLibVersion))
	assertHasPath(t, cands, join("/usr/lib", "libonnxruntime.so"))
	assertHasPath(t, cands, join("/usr/local/lib", "libonnxruntime.so"))
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
	assertHasPath(t, cands, join("/usr/local/lib", "libonnxruntime."+ortLibVersion+".dylib"))
	assertHasPath(t, cands, join("/usr/local/lib", "libonnxruntime.dylib"))
	assertHasPath(t, cands, join("/opt/homebrew/lib", "libonnxruntime.dylib"))
	assertHasPath(t, cands, join("/opt/onnxruntime", "lib", "libonnxruntime.dylib"))
	assertHasPath(t, cands, "libonnxruntime.dylib")
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
// кандидат (на текущей машине) либо "" . Сам по себе вызов не должен
// паниковать/падать независимо от наличия библиотеки.
func TestAutodetectORTLib(t *testing.T) {
	got := autodetectORTLib()
	if got == "" {
		return // библиотека не установлена — автодетект корректно пуст.
	}
	if !fileExists(got) {
		t.Errorf("autodetectORTLib = %q, but file does not exist", got)
	}
	// Возвращённый путь обязан быть одним из кандидатов текущей ОС.
	for _, c := range ortLibraryCandidates() {
		if got == c {
			return
		}
	}
	t.Errorf("autodetectORTLib = %q is not in ortLibraryCandidates() for %s", got, runtime.GOOS)
}

// TestOrtLibPathForInit проверяет приоритет выбора пути библиотеки:
// непустой путь из конфига (detection.onnx-runtime-lib) всегда имеет
// приоритет над автодетектом, пустой — приводит к автодетекции.
func TestOrtLibPathForInit(t *testing.T) {
	// Непустой путь из конфига: возвращается как есть, даже если файла нет
	// (валидация существования — забота initORT/биндинга, а не выбора пути).
	custom := filepath.Join("C:", "custom", "lib", "onnxruntime.dll")
	if got := ortLibPathForInit(custom); got != custom {
		t.Errorf("ortLibPathForInit(%q) = %q, want %q (config path wins)", custom, got, custom)
	}
	// Пустой путь = автодетекция: результат совпадает с autodetectORTLib.
	if got := ortLibPathForInit(""); got != autodetectORTLib() {
		t.Errorf("ortLibPathForInit(\"\") = %q, want autodetectORTLib() = %q", got, autodetectORTLib())
	}
}

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
