// Тесты чистой логики параметров кодировщиков (без build-tag): нормализаторы
// значений, решение о PNG-квантовании, умолчания.
package libvips

import "testing"

// TestDefaultEncoderParams — умолчания совпадают с историческим поведением
// и дефолтами govips.
func TestDefaultEncoderParams(t *testing.T) {
	d := DefaultEncoderParams()
	if d.WebPReductionEffort != 4 {
		t.Errorf("WebPReductionEffort = %d, want 4", d.WebPReductionEffort)
	}
	if d.PNGCompression != 6 {
		t.Errorf("PNGCompression = %d, want 6", d.PNGCompression)
	}
	if d.AVIFSpeed != 0 {
		t.Errorf("AVIFSpeed = %d, want 0 (= govips default)", d.AVIFSpeed)
	}
	if d.JXLEffort != 0 {
		t.Errorf("JXLEffort = %d, want 0 (= govips default)", d.JXLEffort)
	}
	if d.GIFBitDepth != 0 {
		t.Errorf("GIFBitDepth = %d, want 0 (= govips default)", d.GIFBitDepth)
	}
	if d.JPEGProgressive {
		t.Error("JPEGProgressive must default to false")
	}
	if d.PNGInterlace {
		t.Error("PNGInterlace must default to false")
	}
	if d.PNGPalette {
		t.Error("PNGPalette must default to false")
	}
}

// TestEncoderNormalizers — нормализаторы «0 = умолчание» для новых параметров.
func TestEncoderNormalizers(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"webpReductionEffort(0)", webpReductionEffort(0), 4},
		{"webpReductionEffort(2)", webpReductionEffort(2), 2},
		{"pngCompression(0)", pngCompression(0), 6},
		{"pngCompression(9)", pngCompression(9), 9},
		{"avifSpeed(0)", avifSpeed(0), 0},
		{"avifSpeed(8)", avifSpeed(8), 8},
		{"jxlEffort(0)", jxlEffort(0), 0},
		{"jxlEffort(3)", jxlEffort(3), 3},
		{"pngPaletteColors(0)", pngPaletteColors(0), 256},
		{"pngPaletteColors(2)", pngPaletteColors(2), 2},
		{"pngPaletteColors(128)", pngPaletteColors(128), 128},
		{"pngPaletteColors(1) clamp to 2", pngPaletteColors(1), 2},
		{"pngPaletteColors(300) clamp to 256", pngPaletteColors(300), 256},
		{"pngPaletteBitdepth(0)", pngPaletteBitdepth(0), 8},
		{"pngPaletteBitdepth(4)", pngPaletteBitdepth(4), 4},
		{"pngPaletteBitdepth(0) low clamp", pngPaletteBitdepth(0), 8},
		{"pngPaletteBitdepth(-1) clamp to 1", pngPaletteBitdepth(-1), 1},
		{"pngPaletteBitdepth(9) clamp to 8", pngPaletteBitdepth(9), 8},
		{"gifBitDepth(0)", gifBitDepth(0), 8},
		{"gifBitDepth(4)", gifBitDepth(4), 4},
		{"gifBitDepth(-2) clamp to 1", gifBitDepth(-2), 1},
		{"gifBitDepth(12) clamp to 8", gifBitDepth(12), 8},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestResolvePNGQuantize — решение о PNG-квантовании:
//   - выключено по умолчанию (квантование НЕ применяется к градиентам/фото
//     без явной опции);
//   - при включении — умолчания 256 цветов и 8 бит;
//   - защитный clamp некорректных значений.
func TestResolvePNGQuantize(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		d := resolvePNGQuantize(EncoderParams{})
		if d.Palette {
			t.Fatal("quantize must be disabled by default (option off)")
		}
		if d.Colors != 0 || d.Bitdepth != 0 {
			t.Fatalf("off decision must carry zero values, got %+v", d)
		}
	})

	t.Run("enabled defaults to 256 colors / 8 bit", func(t *testing.T) {
		d := resolvePNGQuantize(EncoderParams{PNGPalette: true})
		if !d.Palette {
			t.Fatal("quantize must be enabled when PNGPalette=true")
		}
		if d.Colors != 256 {
			t.Errorf("Colors = %d, want 256", d.Colors)
		}
		if d.Bitdepth != 8 {
			t.Errorf("Bitdepth = %d, want 8", d.Bitdepth)
		}
	})

	t.Run("explicit colors and bitdepth preserved", func(t *testing.T) {
		d := resolvePNGQuantize(EncoderParams{PNGPalette: true, PNGPaletteColors: 64, PNGPaletteBitDepth: 4})
		if !d.Palette || d.Colors != 64 || d.Bitdepth != 4 {
			t.Fatalf("decision = %+v, want {true 64 4}", d)
		}
	})

	t.Run("invalid values clamped", func(t *testing.T) {
		d := resolvePNGQuantize(EncoderParams{PNGPalette: true, PNGPaletteColors: 1, PNGPaletteBitDepth: 300})
		if d.Colors != 2 {
			t.Errorf("Colors = %d, want 2 (clamp min)", d.Colors)
		}
		if d.Bitdepth != 8 {
			t.Errorf("Bitdepth = %d, want 8 (clamp max)", d.Bitdepth)
		}
	})
}
