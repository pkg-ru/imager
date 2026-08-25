package imagemagick

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnvForBinary_AddsModulePaths проверяет, что для binary по абсолютному
// пути окружение наследует os.Environ() и дополняется путями coders/,
// filters/ и config/ из каталога binary (portable Windows-сборки).
func TestEnvForBinary_AddsModulePaths(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "coders"))
	mustMkdir(t, filepath.Join(dir, "filters"))
	mustMkdir(t, filepath.Join(dir, "config"))

	binary := filepath.Join(dir, "magick.exe")
	env := envForBinary(binary, nil)

	// Наследуется PATH из окружения процесса.
	if !containsEnvPrefix(env, "PATH=") {
		t.Error("expected inherited PATH in env")
	}
	// Каталог binary добавлен в PATH (для CORE_RL_*.dll).
	if !strings.HasPrefix(envValue(env, "PATH"), dir) {
		t.Errorf("PATH should start with binary dir %q, got %q", dir, envValue(env, "PATH"))
	}
	// Пути модулей добавлены.
	checkEnvValue(t, env, envCoderModules, filepath.Join(dir, "coders"))
	checkEnvValue(t, env, envFilterModules, filepath.Join(dir, "filters"))
	checkEnvValue(t, env, envConfigurePath, filepath.Join(dir, "config"))
}

// TestEnvForBinary_PolicyDirPriority проверяет, что каталог policy.xml
// идёт первым в MAGICK_CONFIGURE_PATH.
func TestEnvForBinary_PolicyDirPriority(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "config"))

	policyDir := t.TempDir()
	env := envForBinary(filepath.Join(dir, "magick"), []string{policyDir})

	val := envValue(env, envConfigurePath)
	if !strings.HasPrefix(val, policyDir) {
		t.Errorf("MAGICK_CONFIGURE_PATH = %q, want leading policy dir %q", val, policyDir)
	}
}

// TestEnvForBinary_BareName проверяет, что для имени из PATH модульные пути
// не добавляются (нет каталога binary), но MAGICK_CONFIGURE_PATH может быть
// установлен.
func TestEnvForBinary_BareName(t *testing.T) {
	policyDir := t.TempDir()
	env := envForBinary("magick", []string{policyDir})
	if containsEnvPrefix(env, envCoderModules+"=") {
		t.Error("expected no MAGICK_CODER_MODULE_PATH for bare name")
	}
	checkEnvValue(t, env, envConfigurePath, policyDir)
}

// TestEnvForBinary_Upsert не создаёт дублей при повторном вызове.
func TestEnvForBinary_Upsert(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "coders"))
	mustMkdir(t, filepath.Join(dir, "config"))

	binary := filepath.Join(dir, "magick.exe")
	env := envForBinary(binary, nil)
	env = envForBinary(binary, nil)

	if n := countEnvPrefix(env, envCoderModules+"="); n != 1 {
		t.Errorf("MAGICK_CODER_MODULE_PATH appears %d times, want 1", n)
	}
	// Каталог binary добавлен в PATH ровно один раз.
	if n := countEnvPrefix(env, "PATH="); n != 1 {
		t.Errorf("PATH appears %d times, want 1", n)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func countEnvPrefix(env []string, prefix string) int {
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

func checkEnvValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	if got := envValue(env, key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}
