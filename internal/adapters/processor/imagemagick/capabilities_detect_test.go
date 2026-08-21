package imagemagick

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFakeBinary создаёт исполняемый fake binary (shell script на unix,
// .bat на windows), который эмулирует `-version` и `-list format`.
func writeFakeBinary(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "magick")
	var script string
	if runtime.GOOS == "windows" {
		path += ".bat"
		script = `@echo off
if "%1"=="-version" (
  echo Version: ImageMagick 7.1.1-35 Q16-HDRI x86_64
  exit /b 0
)
if "%1"=="-list" (
  echo Format  Module  Mode  Description
  echo -------------------------------------------------------------------------------
  echo * PNG  rw-  Portable Network Graphics
  echo   JPEG  rw-  Joint Photographic Experts Group
  echo   WEBP  rw-  WebP Image Format
  echo   MSL  r--  Magick Scripting Language
  exit /b 0
)
exit /b 1
`
	} else {
		script = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "Version: ImageMagick 7.1.1-35 Q16-HDRI x86_64"
  exit 0
fi
if [ "$1" = "-list" ]; then
  echo "Format  Module  Mode  Description"
  echo "-------------------------------------------------------------------------------"
  echo "* PNG  rw-  Portable Network Graphics"
  echo "  JPEG  rw-  Joint Photographic Experts Group"
  echo "  WEBP  rw-  WebP Image Format"
  echo "  MSL  r--  Magick Scripting Language"
  exit 0
fi
exit 1
`
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func TestDetectCapabilities_FakeBinary(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeBinary(t, dir)

	caps, err := detectCapabilities(context.Background(), binary)
	if err != nil {
		t.Fatalf("detectCapabilities: %v", err)
	}
	if caps.Version != "7.1.1-35" {
		t.Errorf("version = %q, want 7.1.1-35", caps.Version)
	}
	if caps.Major != 7 {
		t.Errorf("major = %d, want 7", caps.Major)
	}
	if !caps.SupportsFormat("png") {
		t.Error("expected png support")
	}
	if !caps.SupportsFormat("jpeg") {
		t.Error("expected jpeg support")
	}
	if caps.SupportsFormat("msl") {
		t.Error("msl is read-only and should be excluded")
	}
}

func TestNew_DetectCapabilitiesFakeBinary(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeBinary(t, dir)

	p, err := New(Options{Binary: binary, DetectCapabilities: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Capabilities() == nil || !p.Capabilities().HasFormatList() {
		t.Fatal("expected capabilities")
	}
	if p.Capabilities().Major != 7 {
		t.Errorf("major = %d, want 7", p.Capabilities().Major)
	}
}

func TestNew_DetectionErrorNotCachedGlobally(t *testing.T) {
	// Первый экземпляр с несуществующим binary — ошибка.
	if _, err := New(Options{Binary: "definitely-not-a-binary-xyz", DetectCapabilities: true}); err == nil {
		t.Fatal("expected error for missing binary")
	}
	// Второй экземпляр с валидным fake binary — успех. Ошибка первого не
	// должна кэшироваться глобально.
	dir := t.TempDir()
	binary := writeFakeBinary(t, dir)
	p, err := New(Options{Binary: binary, DetectCapabilities: true})
	if err != nil {
		t.Fatalf("second instance should succeed, got %v", err)
	}
	if p.Capabilities() == nil {
		t.Fatal("expected capabilities")
	}
}
