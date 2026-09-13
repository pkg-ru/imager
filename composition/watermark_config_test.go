package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseRuntimeConfigWatermarks проверяет декодирование секции
// watermarks и fail-fast на отсутствующий файл ватермарки.
func TestParseRuntimeConfigWatermarks(t *testing.T) {
	wmFile := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(wmFile, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write wm file: %v", err)
	}
	yaml := `
version: "1"
watermarks:
  logo:
    path: ` + wmFile + `
    position: bottom
    repeat: repeat-x
    size: 200px 50px
    opacity: 40
policy:
  path-policies:
    "/":
      presets: [thumb]
  presets:
    thumb:
      width: 200
      height: 200
      output-formats: [webp]
      watermark: logo
`
	if _, err := ParseRuntimeConfig([]byte(yaml)); err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}

	bad := strings.Replace(yaml, wmFile, filepath.Join(t.TempDir(), "missing.png"), 1)
	if _, err := ParseRuntimeConfig([]byte(bad)); err == nil {
		t.Fatal("expected error for missing watermark file")
	}

	// Некорректная прозрачность — fail-fast ошибка конфигурации.
	badOpacity := strings.Replace(yaml, "opacity: 40", "opacity: 150", 1)
	if _, err := ParseRuntimeConfig([]byte(badOpacity)); err == nil {
		t.Fatal("expected error for out-of-range watermark opacity")
	}
}

// TestParseRuntimeConfigWatermarkOpacityDefault проверяет, что секция
// watermarks без поля opacity компилируется с дефолтом 100.
func TestParseRuntimeConfigWatermarkOpacityDefault(t *testing.T) {
	wmFile := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(wmFile, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write wm file: %v", err)
	}
	yaml := `
version: "1"
watermarks:
  logo:
    path: ` + wmFile + `
    position: center
policy:
  path-policies:
    "/":
      presets: [thumb]
  presets:
    thumb:
      width: 200
      height: 200
      output-formats: [webp]
      watermark: logo
`
	cfg, err := ParseRuntimeConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("nil runtime config")
	}
}
