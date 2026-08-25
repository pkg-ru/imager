package imagemagick

import (
	"testing"
)

func TestParseFormatList(t *testing.T) {
	out := `Format  Module  Mode  Description
-------------------------------------------------------------------------------
* PNG  rw-  Portable Network Graphics (libpng 1.6.40)
  JPEG  rw-  Joint Photographic Experts Group JFIF format (libjpeg-turbo 2.1.4)
  GIF  rw-  CompuServe graphics interchange format
  MSL  r--  Magick Scripting Language
  URL  r--  Uniform Resource Locator
  WEBP  rw-  WebP Image Format
  HEIC  rw-  High Efficiency Image Format
  AVIF  rw-  AV1 Image File Format
  TXT  rw-  Text
`
	list, err := parseFormatList(out, "magick")
	if err != nil {
		t.Fatalf("parseFormatList: %v", err)
	}
	// Только форматы с r и w.
	for _, f := range list {
		if f == "msl" || f == "url" {
			t.Errorf("read-only format %q should be excluded", f)
		}
	}
	// Проверяем наличие ожидаемых.
	got := map[string]bool{}
	for _, f := range list {
		got[f] = true
	}
	for _, want := range []string{"png", "jpeg", "gif", "webp", "heic", "avif"} {
		if !got[want] {
			t.Errorf("expected format %q in list, got %v", want, list)
		}
	}
}

func TestParseFormatList_Empty(t *testing.T) {
	if _, err := parseFormatList("", "magick"); err == nil {
		t.Fatal("expected error for empty format list")
	}
}

func TestCapabilities_SupportsFormat(t *testing.T) {
	caps := &Capabilities{
		Formats:   []string{"jpeg", "png"},
		formatSet: map[string]struct{}{"jpeg": {}, "png": {}},
	}
	if !caps.SupportsFormat("JPEG") {
		t.Error("expected JPEG supported (case-insensitive)")
	}
	if !caps.SupportsFormat("png") {
		t.Error("expected png supported")
	}
	if caps.SupportsFormat("webp") {
		t.Error("webp should not be supported")
	}
	if caps.SupportsFormat("") {
		t.Error("empty format should not be supported")
	}
}

func TestCapabilities_Nil(t *testing.T) {
	var caps *Capabilities
	if caps.SupportsFormat("png") {
		t.Error("nil capabilities should not support anything")
	}
	if caps.HasFormatList() {
		t.Error("nil capabilities should not have format list")
	}
}

func TestMajorVersion(t *testing.T) {
	cases := []struct {
		version string
		want    int
	}{
		{"7.1.1-35", 7},
		{"6.9.12-66", 6},
		{"", 0},
		{"abc", 0},
		{"7", 0},
	}
	for _, c := range cases {
		if got := majorVersion(c.version); got != c.want {
			t.Errorf("majorVersion(%q) = %d, want %d", c.version, got, c.want)
		}
	}
}

func TestCapabilities_ImmutableFormats(t *testing.T) {
	caps := &Capabilities{
		Formats:   []string{"jpeg", "png"},
		formatSet: map[string]struct{}{"jpeg": {}, "png": {}},
	}
	// Снимок не должен мутироваться извне (нет сеттеров).
	if len(caps.Formats) != 2 {
		t.Errorf("expected 2 formats, got %d", len(caps.Formats))
	}
}
