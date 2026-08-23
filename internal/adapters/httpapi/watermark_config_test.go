package httpapi

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
  - name: logo
    path: ` + wmFile + `
    position: bottom
    repeat: repeat-x
    size: 200px 50px
policy:
  global:
    authorization: safe
    allowed-presets: [thumb]
  presets:
    - name: thumb
      size: 200x200
      output-format: webp
      watermark: logo
`
	if _, err := ParseRuntimeConfig([]byte(yaml)); err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}

	bad := strings.Replace(yaml, wmFile, filepath.Join(t.TempDir(), "missing.png"), 1)
	if _, err := ParseRuntimeConfig([]byte(bad)); err == nil {
		t.Fatal("expected error for missing watermark file")
	}
}
