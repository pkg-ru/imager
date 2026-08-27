//go:build onnx

// Кроссплатформенный автодетект библиотеки ONNX Runtime.
//
// Список кандидатов зависит от ОС (runtime.GOOS):
//   - Linux:   libonnxruntime.so, libonnxruntime.so.<version>, onnxruntime.so
//     в /usr/lib, /usr/local/lib, /opt/onnxruntime/lib
//     (+ путь Debian/Ubuntu multiarch /usr/lib/x86_64-linux-gnu);
//   - Windows: onnxruntime.dll рядом с exe, в %WINDIR%\System32 и в каталоге
//     установки ONNX Runtime (ProgramFiles);
//   - macOS:   libonnxruntime.dylib, libonnxruntime.<version>.dylib
//     в /usr/local/lib, /opt/homebrew/lib, /opt/onnxruntime/lib.
//
// Автодетект выполняется ТОЛЬКО при пустом detection.onnx-runtime-lib:
// путь из конфиг-файла всегда имеет приоритет (см. initORT в onnx_cgo.go).
// Файл определён с тегом "onnx" (без cgo), чтобы список кандидатов был
// доступен в тестах и в сборках с CGO_ENABLED=0.
package detection

import (
	"os"
	"path/filepath"
	"runtime"
)

// ortLibVersion — версия ONNX Runtime, чьи версионированные имена файлов
// ищем в автодетекте. Alpine edge и Homebrew ставят версионированные файлы
// без голого симлинка (.so / .dylib), поэтому такие имена нужны явно.
const ortLibVersion = "1.29.0"

// ortLibraryCandidates возвращает возможные пути к библиотеке ONNX Runtime
// в порядке приоритета для текущей ОС.
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

// autodetectORTLib возвращает первый СУЩЕСТВУЮЩИЙ кандидат в
// ortLibraryCandidates() или "" , если ни один не найден. Вызывается из
// initORT (onnx_cgo.go) при пустом пути из конфига. Выделена в отдельную
// функцию для тестируемости.
func autodetectORTLib() string {
	return firstExisting(ortLibraryCandidates())
}

// ortLibPathForInit выбирает путь к библиотеке ONNX Runtime для initORT:
// непустой путь из конфига (detection.onnx-runtime-lib) имеет приоритет над
// автодетектом; при пустом — автодетекция по платформе; если автодетект не
// нашёл ни одного файла — возвращается "" (тогда биндинг пробует свой
// дефолт "onnxruntime.so" / "onnxruntime.dll").
func ortLibPathForInit(libPath string) string {
	if libPath != "" {
		return libPath
	}
	return autodetectORTLib()
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
		// Alpine edge onnxruntime (musl): версионированный файл без симлинка.
		filepath.Join("/usr/lib", "libonnxruntime.so."+ortLibVersion),
		// Дефолт большинства дистрибутивов.
		filepath.Join("/usr/lib", "libonnxruntime.so"),
		filepath.Join("/usr/local/lib", "libonnxruntime.so"),
		// Debian/Ubuntu multiarch.
		filepath.Join("/usr/lib", "x86_64-linux-gnu", "libonnxruntime.so"),
		// Кастомная установка (например, из официального tar.gz).
		filepath.Join("/opt/onnxruntime", "lib", "libonnxruntime.so"),
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
		// Версионированные имена (Homebrew / ручная установка): файл без
		// голого симлинка .dylib.
		filepath.Join("/usr/local/lib", "libonnxruntime."+ortLibVersion+".dylib"),
		filepath.Join("/opt/homebrew/lib", "libonnxruntime."+ortLibVersion+".dylib"),
		// Обычные имена (после symlink-сборки или Homebrew).
		filepath.Join("/usr/local/lib", "libonnxruntime.dylib"),
		filepath.Join("/opt/homebrew/lib", "libonnxruntime.dylib"),
		// Кастомная установка (официальный .pkg / tar.gz).
		filepath.Join("/opt/onnxruntime", "lib", "libonnxruntime.dylib"),
		// Голое имя: dyld ищет по DYLD_LIBRARY_PATH и стандартным путям.
		"libonnxruntime.dylib",
	}
}
