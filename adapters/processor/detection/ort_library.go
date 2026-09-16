//go:build onnx

// Кроссплатформенный автодетект библиотеки ONNX Runtime.
//
// Список кандидатов зависит от ОС (runtime.GOOS):
//   - Linux:   libonnxruntime.so, версионированные libonnxruntime.so.*
//     (glob), onnxruntime.so — в /usr/lib, /usr/local/lib, /opt/onnxruntime/lib
//     (+ путь Debian/Ubuntu multiarch /usr/lib/x86_64-linux-gnu);
//   - Windows: onnxruntime.dll рядом с exe, в %WINDIR%\System32 и в каталоге
//     установки ONNX Runtime (ProgramFiles);
//   - macOS:   libonnxruntime.dylib, версионированные
//     libonnxruntime.*.dylib (glob) — в /usr/local/lib, /opt/homebrew/lib,
//     /opt/onnxruntime/lib.
//
// Список НЕ привязан к конкретной минорной версии: версионированные имена
// ищутся через glob-паттерны (libonnxruntime.so.* / libonnxruntime.*.dylib),
// поэтому обновление ONNX Runtime (например, 1.20 -> 1.29) не требует
// изменений в коде или конфигах.
//
// Автодетект выполняется при пустом detection.onnx-runtime-lib, а также как
// fallback, если указанный в конфиге путь не существует/не загружается
// (см. initORT в onnx_cgo.go).
//
// Файл определён с тегом "onnx" (без cgo), чтобы список кандидатов был
// доступен в тестах и в сборках с CGO_ENABLED=0.
package detection

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ortLibraryCandidates возвращает возможные пути к библиотеке ONNX Runtime
// в порядке приоритета для текущей ОС. Элементы, содержащие glob-метасимволы
// (* ? [), раскрываются в конкретные существующие файлы функцией
// expandORTCandidates (версионированные варианты — по убыванию версии).
//
// Сохраняется приоритет: путь из конфига (detection.onnx-runtime-lib) →
// первый СУЩЕСТВУЮЩИЙ кандидат из этого списка → дефолт биндинга
// (InitializeEnvironment с пустым путём сам пробует "onnxruntime.so" на
// Linux/macOS и "onnxruntime.dll" на Windows).
func ortLibraryCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		return ortWindowsCandidates()
	case "darwin":
		return ortDarwinCandidates()
	default:
		// Linux и остальные UNIX-подобные: библиотеки .so.
		return ortLinuxCandidates()
	}
}

// autodetectORTLib возвращает первый СУЩЕСТВУЮЩИЙ кандидат (с раскрытием
// glob-паттернов) или "", если ни один не найден. Вызывается из initORT
// (onnx_cgo.go) при пустом пути из конфига и как fallback при неудачной
// загрузке конфиг-пути. Выделена в отдельную функцию для тестируемости.
func autodetectORTLib() string {
	return firstExisting(expandORTCandidates(ortLibraryCandidates()))
}

// ortLibPathForInit выбирает путь к библиотеке ONNX Runtime для initORT:
// непустой путь из конфига (detection.onnx-runtime-lib) имеет приоритет над
// автодетектом; при пустом — автодетекция по платформе; если автодетект не
// нашёл ни одного файла — возвращается "" (тогда биндинг пробует свой
// дефолт "onnxruntime.so" / "onnxruntime.dll").
//
// Если конфиг-путь не существует как файл, а автодетект нашёл библиотеку —
// возвращается автодетектированный путь (конфиг-путь при этом логируется
// как сбойный в initORT).
func ortLibPathForInit(libPath string) string {
	if libPath != "" && fileExists(libPath) {
		return libPath
	}
	if alt := autodetectORTLib(); alt != "" {
		if libPath != "" && alt != libPath {
			// Конфиг-путь отсутствует на диске — используем автодетект;
			// предупреждение пишет initORT через логгер.
			return alt
		}
		if libPath == "" {
			return alt
		}
	}
	if libPath != "" {
		return libPath
	}
	return ""
}

// firstExisting возвращает первый путь из paths, являющийся обычным файлом,
// или "" , если таких нет. Вынесена отдельно, чтобы тестировать выбор
// кандидата на временных файлах без обращения к системным путям.
func firstExisting(paths []string) string {
	for _, c := range paths {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// expandORTCandidates раскрывает glob-паттерны (содержащие * ? [) в список
// существующих файлов (совпадения сортируются по убыванию, чтобы более
// высокие версии шли первыми: libonnxruntime.so.1.29.0 раньше
// libonnxruntime.so.1) и возвращает единый список путей без дубликатов.
// Не-паттерны переносятся как есть.
func expandORTCandidates(cands []string) []string {
	var out []string
	seen := make(map[string]bool, len(cands))
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, c := range cands {
		if !strings.ContainsAny(c, "*?[") {
			add(c)
			continue
		}
		matches, err := filepath.Glob(c)
		if err != nil {
			continue // некорректный паттерн — пропускаем
		}
		// По убыванию: "1.29.0" лексикографически больше "1.20.0".
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		for _, m := range matches {
			if fileExists(m) {
				add(m)
			}
		}
	}
	return out
}

// fileExists проверяет существование обычного файла по пути path.
// Определена здесь (а не в onnx_cgo.go), чтобы оставаться доступной в
// сборках "onnx" без cgo (тесты автодетекта).
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// ortLinuxCandidates — кандидаты для Linux (разделяемые библиотеки .so).
func ortLinuxCandidates() []string {
	return []string{
		// Дефолт большинства дистрибутивов (симлинк на версионный файл).
		filepath.Join("/usr/lib", "libonnxruntime.so"),
		// Версионированные файлы без симлинка (Alpine edge onnxruntime/musl
		// и ручные установки): glob, НЕ привязанный к минорной версии.
		filepath.Join("/usr/lib", "libonnxruntime.so.*"),
		filepath.Join("/usr/local/lib", "libonnxruntime.so"),
		filepath.Join("/usr/local/lib", "libonnxruntime.so.*"),
		// Debian/Ubuntu multiarch.
		filepath.Join("/usr/lib", "x86_64-linux-gnu", "libonnxruntime.so"),
		filepath.Join("/usr/lib", "x86_64-linux-gnu", "libonnxruntime.so.*"),
		// Кастомная установка (например, из официального tar.gz).
		filepath.Join("/opt/onnxruntime", "lib", "libonnxruntime.so"),
		filepath.Join("/opt/onnxruntime", "lib", "libonnxruntime.so.*"),
		// Дефолт биндинга yalue/onnxruntime_go (dlopen ищет в ld.so / PATH).
		"onnxruntime.so",
		"libonnxruntime.so",
	}
}

// ortWindowsCandidates — кандидаты для Windows (.dll).
func ortWindowsCandidates() []string {
	// Дефолт биндинга: LoadLibrary ищет onnxruntime.dll в каталоге
	// приложения, системных каталогах и PATH. Оставляем первым.
	cands := []string{"onnxruntime.dll"}
	// Явный абсолютный путь рядом с исполняемым файлом.
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), "onnxruntime.dll"))
	}
	// %WINDIR%\System32 — системная DLL.
	if windir := os.Getenv("WINDIR"); windir != "" {
		cands = append(cands, filepath.Join(windir, "System32", "onnxruntime.dll"))
	}
	// Каталог установки ONNX Runtime (официальный дистрибутив / NuGet).
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		cands = append(cands, filepath.Join(pf, "onnxruntime", "lib", "onnxruntime.dll"))
	}
	return cands
}

// ortDarwinCandidates — кандидаты для macOS (.dylib).
func ortDarwinCandidates() []string {
	return []string{
		// Обычные имена (после symlink-сборки или Homebrew).
		filepath.Join("/usr/local/lib", "libonnxruntime.dylib"),
		filepath.Join("/opt/homebrew/lib", "libonnxruntime.dylib"),
		// Версионированные имена без симлинка (Homebrew / ручная установка):
		// glob, НЕ привязанный к минорной версии.
		filepath.Join("/usr/local/lib", "libonnxruntime.*.dylib"),
		filepath.Join("/opt/homebrew/lib", "libonnxruntime.*.dylib"),
		// Кастомная установка (официальный .pkg / tar.gz).
		filepath.Join("/opt/onnxruntime", "lib", "libonnxruntime.dylib"),
		// Голое имя: dyld ищет по DYLD_LIBRARY_PATH и стандартным путям.
		"libonnxruntime.dylib",
	}
}
