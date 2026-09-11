package encoding

import (
	"strings"
	"testing"
)

// --- Реестр: структура и дефолты ----------------------------------------

func TestFormatsCoverage(t *testing.T) {
	want := []Format{FormatJPEG, FormatWebP, FormatAVIF, FormatHEIF, FormatJXL, FormatPNG, FormatAPNG, FormatGIF}
	got := Formats()
	if len(got) != len(want) {
		t.Fatalf("Formats() = %d entries, want %d", len(got), len(want))
	}
	for i, f := range want {
		if got[i].Name != f {
			t.Errorf("Formats()[%d] = %s, want %s", i, got[i].Name, f)
		}
	}
}

func TestRegistryDefaults(t *testing.T) {
	cases := []struct {
		format string
		param  string
		want   any
	}{
		{"jpeg", "quality", 80},
		{"jpeg", "progressive", false}, // server.yaml:358 jpeg-progressive=false
		{"webp", "quality", 75},
		{"webp", "reduction-effort", 4},
		{"webp", "lossless", false},
		{"webp", "near-lossless", false},
		{"avif", "quality", 80},
		{"avif", "speed", 6}, // конфиг avif-speed=6
		{"avif", "lossless", false},
		{"heif", "quality", 80},
		{"jxl", "quality", 75},
		{"jxl", "effort", 7},
		{"jxl", "lossless", false},
		{"png", "quality", 80},
		{"png", "compression-level", 6},
		{"png", "interlace", false}, // server.yaml:362 png-interlace=false
		{"png", "palette", false},   // server.yaml:370 png-palette=false
		{"png", "palette-colors", 256},
		{"png", "palette-bit-depth", 8},
		{"png", "dither", 1.0},
		{"apng", "compression-level", 6},
		{"apng", "interlace", false},
		{"gif", "effort", 7},
		{"gif", "bit-depth", 8},
		{"gif", "dither", 1.0},
	}
	for _, c := range cases {
		def, ok := LookupFormat(c.format)
		if !ok {
			t.Fatalf("format %q not in registry", c.format)
		}
		meta, ok := def.Param(c.param)
		if !ok {
			t.Errorf("%s: parameter %q missing from registry", c.format, c.param)
			continue
		}
		if meta.Kind == KindBool {
			if got := meta.Default != 0; got != c.want {
				t.Errorf("%s.%s default = %v, want %v", c.format, c.param, got, c.want)
			}
		} else if meta.Default != float64(toFloat(c.want)) {
			t.Errorf("%s.%s default = %v, want %v", c.format, c.param, meta.Default, c.want)
		}
	}
}

func toFloat(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	default:
		return 0
	}
}

func TestLosslessFormats(t *testing.T) {
	for _, name := range []string{"png", "apng", "gif"} {
		def, _ := LookupFormat(name)
		if !def.AlwaysLossless {
			t.Errorf("%s: AlwaysLossless = false, want true", name)
		}
		if def.DirectQuality {
			t.Errorf("%s: DirectQuality = true, want false (quality не передаётся кодеру)", name)
		}
	}
	for _, name := range []string{"jpeg", "webp", "avif", "heif", "jxl"} {
		def, _ := LookupFormat(name)
		if !def.DirectQuality {
			t.Errorf("%s: DirectQuality = false, want true", name)
		}
	}
}

// --- Якорные точки ---------------------------------------------------------

func TestAnchorPoints(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"webp effort@75", WebPReductionEffort(75), 4},
		{"webp effort@100", WebPReductionEffort(100), 6},
		{"webp effort@0", WebPReductionEffort(0), 0},
		{"jxl effort@75", JXLEffort(75), 7},
		{"jxl effort@100", JXLEffort(100), 9},
		{"jxl effort@0", JXLEffort(0), 3},
		{"gif effort@75", GIFEffort(75), 7},
		{"gif effort@100", GIFEffort(100), 10},
		{"gif effort@0", GIFEffort(0), 1},
		{"avif speed@80", AVIFSpeed(80), 6},
		{"avif speed@100", AVIFSpeed(100), 0},
		{"avif speed@0", AVIFSpeed(0), 9},
		{"png cl@85", PNGCompressionLevel(85), 6},
		{"png cl@100", PNGCompressionLevel(100), 9},
		{"png cl@0", PNGCompressionLevel(0), 1},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestPaletteAnchor(t *testing.T) {
	// Якорь: q=85 → palette=true, colors=256, bit-depth=8 (текущий дефолт).
	if got := PaletteAuto(85); !got {
		t.Errorf("PaletteAuto(85) = false, want true")
	}
	if got := PaletteColorsFromQuality(85); got != 256 {
		t.Errorf("PaletteColorsFromQuality(85) = %d, want 256", got)
	}
	if got := PaletteBitDepthFromQuality(85); got != 8 {
		t.Errorf("PaletteBitDepthFromQuality(85) = %d, want 8", got)
	}
	// q>=90 → палитра OFF (truecolor).
	if got := PaletteAuto(90); got {
		t.Errorf("PaletteAuto(90) = true, want false (truecolor)")
	}
	if got := PaletteAuto(100); got {
		t.Errorf("PaletteAuto(100) = true, want false")
	}
}

// --- Монотонность ----------------------------------------------------------

func TestMonotonicity(t *testing.T) {
	check := func(name string, fn func(int) int, n int) {
		t.Helper()
		for q := 0; q < 100; q++ {
			if fn(q) > fn(q+1) {
				t.Errorf("%s: not monotonically non-decreasing at q=%d: %d > %d", name, q, fn(q), fn(q+1))
			}
		}
	}
	check("WebPReductionEffort", WebPReductionEffort, 101)
	check("JXLEffort", JXLEffort, 101)
	check("GIFEffort", GIFEffort, 101)
	check("PNGCompressionLevel", PNGCompressionLevel, 101)
	check("PaletteColorsFromQuality", PaletteColorsFromQuality, 101)

	// avif speed — инверсия: монотонно НЕвозрастающая.
	for q := 0; q < 100; q++ {
		if AVIFSpeed(q) < AVIFSpeed(q+1) {
			t.Errorf("AVIFSpeed: not monotonically non-increasing at q=%d: %d < %d", q, AVIFSpeed(q), AVIFSpeed(q+1))
		}
	}
}

// --- Клампы ----------------------------------------------------------------

func TestClamps(t *testing.T) {
	// Выход за диапазоны по всей шкале quality невозможен (уже по формуле),
	// но дополнительно проверяем крайние значения и жёсткую границу качества.
	clampCases := []struct {
		name string
		got  int
		lo   int
		hi   int
	}{
		{"WebPReductionEffort(0)", WebPReductionEffort(0), 0, 6},
		{"WebPReductionEffort(100)", WebPReductionEffort(100), 0, 6},
		{"JXLEffort(100)", JXLEffort(100), 3, 9},
		{"GIFEffort(100)", GIFEffort(100), 1, 10},
		{"PNGCompressionLevel(100)", PNGCompressionLevel(100), 1, 9},
		{"PaletteColorsFromQuality(0)", PaletteColorsFromQuality(0), 2, 256},
		{"PaletteColorsFromQuality(100)", PaletteColorsFromQuality(100), 2, 256},
	}
	for _, c := range clampCases {
		if c.got < c.lo || c.got > c.hi {
			t.Errorf("%s = %d, outside [%d,%d]", c.name, c.got, c.lo, c.hi)
		}
	}
}

func TestPaletteBitDepthSnap(t *testing.T) {
	cases := []struct {
		colors int
		want   int
	}{
		{2, 1},
		{3, 2},
		{4, 2},
		{5, 4},
		{16, 4},
		{17, 8},
		{128, 8},
		{256, 8},
	}
	for _, c := range cases {
		if got := PaletteBitDepthFromColors(c.colors); got != c.want {
			t.Errorf("PaletteBitDepthFromColors(%d) = %d, want %d", c.colors, got, c.want)
		}
	}
}

func TestNormalizeGIFBitDepth(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		// gifBitDepth: 0→8, clamp <1→1, >8→8.
		{"NormalizeGIFBitDepth(0)", NormalizeGIFBitDepth(0), 8},
		{"NormalizeGIFBitDepth(4)", NormalizeGIFBitDepth(4), 4},
		{"NormalizeGIFBitDepth(-2)", NormalizeGIFBitDepth(-2), 1},
		{"NormalizeGIFBitDepth(12)", NormalizeGIFBitDepth(12), 8},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// --- Resolve: якори через эффективные параметры --------------------------

func TestResolveAnchors(t *testing.T) {
	cases := []struct {
		format  string
		quality uint8
		key     string
		want    any
	}{
		{"webp", 75, "reduction-effort", 4},
		{"webp", 75, "lossless", false},
		{"webp", 100, "reduction-effort", 6},
		{"avif", 80, "speed", 6},
		{"avif", 100, "speed", 0},
		{"jxl", 75, "effort", 7},
		{"gif", 75, "effort", 7},
		{"gif", 75, "bit-depth", 8},
		{"gif", 75, "dither", 1.0},
		{"png", 85, "compression-level", 6},
		{"png", 85, "palette", true},
		{"png", 85, "palette-colors", 256},
		{"png", 85, "palette-bit-depth", 8},
		{"png", 90, "palette", false},
		{"png", 100, "palette", false},
		{"jpeg", 80, "quality", 80},
		{"jpeg", 80, "progressive", false},
		{"apng", 85, "compression-level", 6},
	}
	for _, c := range cases {
		got, err := Resolve(c.format, c.quality, nil)
		if err != nil {
			t.Errorf("Resolve(%s, %d, nil) error: %v", c.format, c.quality, err)
			continue
		}
		v, ok := got.Value(c.key)
		if !ok {
			t.Errorf("Resolve(%s): key %q missing", c.format, c.key)
			continue
		}
		if v != c.want {
			t.Errorf("Resolve(%s, %d).%s = %v, want %v", c.format, c.quality, c.key, v, c.want)
		}
	}
}

// --- Resolve: overrides ----------------------------------------------------

func TestResolveOverrideWins(t *testing.T) {
	got, err := Resolve("webp", 75, map[string]any{"reduction-effort": 2})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got.ReductionEffort != 2 {
		t.Errorf("ReductionEffort = %d, want 2 (override wins over anchor 4)", got.ReductionEffort)
	}
	if got.Effort != 0 { // не применимо к webp → нулевое значение
		t.Errorf("Effort = %d, want 0 (not applicable)", got.Effort)
	}
}

func TestResolvePNGPaletteOverride(t *testing.T) {
	// Явная palette=false побеждает автомаппинг (при q=85 должна быть true).
	got, err := Resolve("png", 85, map[string]any{"palette": false})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got.Palette {
		t.Errorf("Palette = true, want false (explicit override)")
	}
	// Явные colors/bitdepth сохраняются без автомаппинга.
	got, err = Resolve("png", 85, map[string]any{"palette": true, "palette-colors": 32, "palette-bit-depth": 4})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got.PaletteColors != 32 || got.PaletteBitDepth != 4 {
		t.Errorf("colors=%d bitdepth=%d, want 32/4", got.PaletteColors, got.PaletteBitDepth)
	}
}

func TestResolveGIFBitDepthOverride(t *testing.T) {
	got, err := Resolve("gif", 75, map[string]any{"bit-depth": 4})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got.BitDepth != 4 {
		t.Errorf("BitDepth = %d, want 4 (explicit override)", got.BitDepth)
	}
}

func TestResolveFloatOverride(t *testing.T) {
	got, err := Resolve("gif", 75, map[string]any{"dither": 0.25})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got.Dither != 0.25 {
		t.Errorf("Dither = %v, want 0.25", got.Dither)
	}
	// Вне диапазона — ошибка.
	if _, err := Resolve("gif", 75, map[string]any{"dither": 1.5}); err == nil {
		t.Errorf("dither=1.5: want error, got nil")
	}
}

// --- Resolve: ошибки -------------------------------------------------------

func TestResolveErrors(t *testing.T) {
	unknownFormat := func() error {
		_, err := Resolve("bmp", 80, nil)
		return err
	}
	if err := unknownFormat(); err == nil {
		t.Errorf("unknown format: want error, got nil")
	}

	unknownParam := func() error {
		_, err := Resolve("jpeg", 80, map[string]any{"speed": 5})
		return err
	}
	if err := unknownParam(); err == nil {
		t.Errorf("unknown param for format: want error, got nil")
	}

	foreignParam := func() error {
		_, err := Resolve("webp", 75, map[string]any{"speed": 5})
		return err
	}
	if err := foreignParam(); err == nil {
		t.Errorf("foreign param (avif speed on webp): want error, got nil")
	}

	if _, err := Resolve("webp", 75, map[string]any{"quality": 90}); err == nil {
		t.Errorf("quality in overrides: want error, got nil")
	}

	if _, err := Resolve("webp", 75, map[string]any{"reduction-effort": 99}); err == nil {
		t.Errorf("out-of-range override: want error, got nil")
	}

	if _, err := Resolve("webp", 75, map[string]any{"reduction-effort": "abc"}); err == nil {
		t.Errorf("bad type override: want error, got nil")
	}

	if _, err := Resolve("webp", 75, map[string]any{"lossless": "yes"}); err == nil {
		t.Errorf("bad bool type override: want error, got nil")
	}
}

func TestResolveCaseInsensitiveFormat(t *testing.T) {
	if _, err := Resolve("WEBP", 75, nil); err != nil {
		t.Errorf("Resolve(WEBP) error: %v", err)
	}
}

// --- Дополнительно: физика палитры ----------------------------------------

func TestPaletteDoesNotDestroyGradientsAtHighQuality(t *testing.T) {
	// Физика: палитра портит градиенты; высокое качество → truecolor.
	for q := uint8(90); q <= 100; q++ {
		got, err := Resolve("png", q, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Palette {
			t.Errorf("png q=%d: Palette = true, want false (truecolor at high quality)", q)
		}
	}
	// Низкое качество: палитра ON и число цветов не превосходит порога качества.
	for q := uint8(0); q < 90; q++ {
		got, err := Resolve("png", q, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Palette {
			t.Errorf("png q=%d: Palette = false, want true", q)
		}
		if got.PaletteColors < 2 || got.PaletteColors > 256 {
			t.Errorf("png q=%d: colors = %d, outside [2,256]", q, got.PaletteColors)
		}
	}
}

func TestStringKeysAndResolvedValue(t *testing.T) {
	// Пресет может прийти из YAML со строковыми числами.
	got, err := Resolve("png", 85, map[string]any{"compression-level": "8"})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got.CompressionLevel != 8 {
		t.Errorf("CompressionLevel = %d, want 8", got.CompressionLevel)
	}
	// Новое значение (из YAML float) — тоже валидно.
	got, err = Resolve("gif", 75, map[string]any{"dither": 0.5})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got.Dither != 0.5 {
		t.Errorf("Dither = %v, want 0.5", got.Dither)
	}
}

func TestParamNamesKebabCase(t *testing.T) {
	for _, def := range Formats() {
		for _, p := range def.Params {
			if p.Name != strings.ToLower(p.Name) || !strings.Contains(p.Name, "-") {
				// quality и известные параметры не обязаны содержать дефис,
				// но обязаны быть kebab-case (нижний регистр, только буквы/дефис).
				for _, r := range p.Name {
					if !(r >= 'a' && r <= 'z' || r == '-') {
						t.Errorf("%s: param %q not kebab-case", def.Name, p.Name)
					}
				}
			}
		}
	}
}
