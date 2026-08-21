// Package imagemagick: вспомогательные функции для окружения subprocess.
//
// На Windows portable-сборка ImageMagick ищет coder-модули и конфигурацию
// по умолчанию в %LOCALAPPDATA%\ImageMagick\ и не открывает модули из
// каталога рядом с binary. Чтобы ImageMagick находил coders/, filters/ и
// config/ рядом с запускаемым binary, мы формируем окружение из
// os.Environ() и дополняем его соответствующими переменными на основе
// каталога binary — без потери PATH/SystemRoot.
package imagemagick

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// envCoderModules — каталог coder-модулей.
	envCoderModules = "MAGICK_CODER_MODULE_PATH"
	// envFilterModules — каталог filter-модулей.
	envFilterModules = "MAGICK_FILTER_MODULE_PATH"
	// envConfigurePath — конфигурационные каталоги ImageMagick.
	envConfigurePath = "MAGICK_CONFIGURE_PATH"
)

// binaryDir возвращает каталог binary (если binary — это путь, а не просто
// имя из PATH), иначе пустую строку.
func binaryDir(binary string) string {
	if binary == "" || !strings.ContainsAny(binary, `/\`) {
		return ""
	}
	if d := filepath.Dir(binary); d != "" && d != "." {
		return d
	}
	return ""
}

// envForBinary строит окружение для запуска ImageMagick.
//
//   - наследует os.Environ() (PATH, SYSTEMROOT и т.п.);
//   - если рядом с binary есть каталоги coders/, filters/, config/ —
//     добавляет их в соответствующие MAGICK_*;
//   - к MAGICK_CONFIGURE_PATH добавляет configurePaths (например, каталог
//     с сгенерированной policy.xml) с приоритетом перед config/.
//
// Возвращаемый slice можно передавать как cmd.Env.
func envForBinary(binary string, configurePaths []string) []string {
	env := os.Environ()
	dir := binaryDir(binary)
	if dir == "" {
		var nonEmpty []string
		for _, p := range configurePaths {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		if len(nonEmpty) > 0 {
			env = upsertEnv(env, envConfigurePath, strings.Join(nonEmpty, string(os.PathListSeparator)))
		}
		return env
	}
	for _, sub := range []struct{ env, dir string }{
		{envCoderModules, "coders"},
		{envFilterModules, "filters"},
	} {
		p := filepath.Join(dir, sub.dir)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			env = upsertEnv(env, sub.env, p)
		}
	}
	// Зависимые DLL (CORE_RL_*.dll) лежат в каталоге binary. Windows ищет
	// их по PATH процесса, поэтому добавляем каталог binary в начало PATH.
	env = upsertPath(env, dir)

	cfg := filepath.Join(dir, "config")
	if st, err := os.Stat(cfg); err == nil && st.IsDir() {
		configurePaths = append(configurePaths, cfg)
	}
	var nonEmpty []string
	for _, p := range configurePaths {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) > 0 {
		env = upsertEnv(env, envConfigurePath, strings.Join(nonEmpty, string(os.PathListSeparator)))
	}
	return env
}

// MagickEnv возвращает окружение для заданного binary: наследует
// окружение процесса и добавляет модульные пути из каталога binary.
// Используется cmd/imager (генератор пикселя) и в тестах.
func MagickEnv(binary string) []string {
	return envForBinary(binary, nil)
}

// upsertEnv заменяет переменную с ключом key на value; если переменной
// не было — добавляет в конец. Результат может использоваться как cmd.Env.
func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// upsertPath добавляет dir в начало PATH (если dir ещё не в PATH).
func upsertPath(env []string, dir string) []string {
	prefix := "PATH="
	for i, e := range env {
		if !strings.HasPrefix(e, prefix) {
			continue
		}
		cur := strings.TrimPrefix(e, prefix)
		if cur == "" {
			env[i] = prefix + dir
			return env
		}
		// Если dir уже присутствует (как отдельный элемент) — не дублируем.
		for _, p := range strings.Split(cur, string(os.PathListSeparator)) {
			if strings.EqualFold(p, dir) {
				return env
			}
		}
		env[i] = prefix + dir + string(os.PathListSeparator) + cur
		return env
	}
	return append(env, prefix+dir)
}
