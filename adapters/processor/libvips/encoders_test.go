// Тесты чистой логики параметров кодировщиков (без build-tag): разрешение
// effective-параметров через domain/encoding (resolveEffective), матрица
// приоритетов (preset override > encoders yaml > автомаппинг > дефолт
// реестра), якорные точки, PNG-квантование.
package libvips

import (
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/encoding"
)

// testCfg возвращает EncodersConfig с заданными глобальными параметрами
// (аналог секции encoders из setting/server.yaml).
func testCfg() EncodersConfig {
	p := func(v int) *int { return &v }
	b := func(v bool) *bool { return &v }
	return EncodersConfig{
		DefaultQuality: 85,
		Formats: map[string]FormatEncodersConfig{
			"jpeg": {Progressive: b(true)},
			"webp": {
				ReductionEffort: p(2),
			},
			"avif": {Speed: p(3), Lossless: b(true)},
			"jxl":  {Effort: p(5)},
			"png": {
				CompressionLevel: p(9),
				Interlace:        b(true),
				Palette:          b(true),
				PaletteColors:    p(128),
				PaletteBitDepth:  p(4),
			},
			"gif": {Effort: p(3), BitDepth: p(4)},
			"apng": {
				CompressionLevel: p(7),
			},
		},
	}
}

// TestResolveEffectiveYAMLGlobals — глобальные значения секции encoders
// применяются как effective-параметры, когда пресет-оверрайдов нет.
func TestResolveEffectiveYAMLGlobals(t *testing.T) {
	r, err := resolveEffective(testCfg(), "webp", 75, nil)
	if err != nil {
		t.Fatalf("resolveEffective(webp): %v", err)
	}
	if r.ReductionEffort != 2 {
		t.Errorf("webp reduction-effort = %d, want 2 (encoders yaml)", r.ReductionEffort)
	}
	// Quality: yaml-глобальный не задан → план (75).
	if r.Quality != 75 {
		t.Errorf("webp quality = %d, want 75 (plan)", r.Quality)
	}
	// Незаданные глобально параметры — автомаппинг от quality.
	if r.Quality != 75 {
		t.Fatalf("unexpected quality %d", r.Quality)
	}

	r2, err := resolveEffective(testCfg(), "png", 85, nil)
	if err != nil {
		t.Fatalf("resolveEffective(png): %v", err)
	}
	if r2.CompressionLevel != 9 {
		t.Errorf("png compression-level = %d, want 9 (encoders yaml)", r2.CompressionLevel)
	}
	if !r2.Interlace {
		t.Error("png interlace want true (encoders yaml)")
	}
	if !r2.Palette || r2.PaletteColors != 128 || r2.PaletteBitDepth != 4 {
		t.Errorf("png palette = %v/%d/%d, want true/128/4 (encoders yaml)", r2.Palette, r2.PaletteColors, r2.PaletteBitDepth)
	}

	r3, err := resolveEffective(testCfg(), "gif", 75, nil)
	if err != nil {
		t.Fatalf("resolveEffective(gif): %v", err)
	}
	if r3.Effort != 3 || r3.BitDepth != 4 {
		t.Errorf("gif effort/bitdepth = %d/%d, want 3/4 (encoders yaml)", r3.Effort, r3.BitDepth)
	}
}

// TestResolveEffectivePresetOverrides — override пресета побеждает глобал
// YAML (на одном реестровом ключе).
func TestResolveEffectivePresetOverrides(t *testing.T) {
	// webp: yaml reduction-effort=2, preset reduction-effort=6.
	r, err := resolveEffective(testCfg(), "webp", 75, map[string]any{"reduction-effort": 6})
	if err != nil {
		t.Fatalf("resolveEffective: %v", err)
	}
	if r.ReductionEffort != 6 {
		t.Errorf("webp reduction-effort = %d, want 6 (preset override > yaml)", r.ReductionEffort)
	}

	// png: yaml compression-level=9, preset compression-level=1.
	r2, err := resolveEffective(testCfg(), "png", 85, map[string]any{"compression-level": 1, "palette": false})
	if err != nil {
		t.Fatalf("resolveEffective(png): %v", err)
	}
	if r2.CompressionLevel != 1 {
		t.Errorf("png compression-level = %d, want 1 (preset override)", r2.CompressionLevel)
	}
	if r2.Palette {
		t.Error("png palette must be false (preset override wins over yaml true)")
	}
}

// TestResolveEffectiveAutoMapping — параметры без override и без yaml-
// глобала берутся из якорного автомаппинга от quality.
func TestResolveEffectiveAutoMapping(t *testing.T) {
	cfg := EncodersConfig{DefaultQuality: 85, Formats: map[string]FormatEncodersConfig{}}
	// webp q=75 → effort 4 (якорь), q=100 → 6.
	r, err := resolveEffective(cfg, "webp", 75, nil)
	if err != nil {
		t.Fatalf("resolveEffective(webp 75): %v", err)
	}
	if r.ReductionEffort != 4 {
		t.Errorf("webp q75 reduction-effort = %d, want 4 (anchor)", r.ReductionEffort)
	}
	r2, err := resolveEffective(cfg, "webp", 100, nil)
	if err != nil {
		t.Fatalf("resolveEffective(webp 100): %v", err)
	}
	if r2.ReductionEffort != 6 {
		t.Errorf("webp q100 reduction-effort = %d, want 6", r2.ReductionEffort)
	}

	// avif q=80 → speed 6 (якорь); 0 ВАЛИДЕН (q=100 → 0).
	r3, err := resolveEffective(cfg, "avif", 80, nil)
	if err != nil {
		t.Fatalf("resolveEffective(avif 80): %v", err)
	}
	if r3.Speed != 6 {
		t.Errorf("avif q80 speed = %d, want 6 (anchor)", r3.Speed)
	}
	r4, err := resolveEffective(cfg, "avif", 100, nil)
	if err != nil {
		t.Fatalf("resolveEffective(avif 100): %v", err)
	}
	if r4.Speed != 0 {
		t.Errorf("avif q100 speed = %d, want 0 (valid speed, not 'unset')", r4.Speed)
	}

	// gif q=75 → effort 7 (якорь libvips), dither=1.0.
	r5, err := resolveEffective(cfg, "gif", 75, nil)
	if err != nil {
		t.Fatalf("resolveEffective(gif): %v", err)
	}
	if r5.Effort != 7 {
		t.Errorf("gif q75 effort = %d, want 7 (anchor)", r5.Effort)
	}
	if r5.Dither != 1.0 {
		t.Errorf("gif dither = %v, want 1.0 (registry default)", r5.Dither)
	}

	// png q=85 → compression 6 (якорь), q=100 → 9.
	r6, err := resolveEffective(cfg, "png", 85, nil)
	if err != nil {
		t.Fatalf("resolveEffective(png 85): %v", err)
	}
	if r6.CompressionLevel != 6 {
		t.Errorf("png q85 compression = %d, want 6 (anchor)", r6.CompressionLevel)
	}
}

// TestResolveEffectiveQualityPriority — приоритет качества:
// preset per-format quality > encoders.<fmt>.quality > plan.Quality.
func TestResolveEffectiveQualityPriority(t *testing.T) {
	type p = *int
	q85 := p(intp(85))
	q90 := p(intp(90))
	cfg := EncodersConfig{
		DefaultQuality: 80,
		Formats: map[string]FormatEncodersConfig{
			"webp": {Quality: q85},
			"jpeg": {Quality: q90},
		},
	}
	// webp: yaml quality=85 > plan 50.
	r, err := resolveEffective(cfg, "webp", 50, nil)
	if err != nil {
		t.Fatalf("resolveEffective: %v", err)
	}
	if r.Quality != 85 {
		t.Errorf("webp quality = %d, want 85 (encoders yaml > plan)", r.Quality)
	}
	// jpeg: yaml quality=90; preset "quality" (per-format) побеждает.
	r2, err := resolveEffective(cfg, "jpeg", 50, map[string]any{"quality": 95})
	if err != nil {
		t.Fatalf("resolveEffective(jpeg override): %v", err)
	}
	if r2.Quality != 95 {
		t.Errorf("jpeg quality = %d, want 95 (preset per-format quality > yaml)", r2.Quality)
	}
	// webp без уyl и override → plan.
	r3, err := resolveEffective(EncodersConfig{DefaultQuality: 80, Formats: map[string]FormatEncodersConfig{}}, "webp", 60, nil)
	if err != nil {
		t.Fatalf("resolveEffective(plan): %v", err)
	}
	if r3.Quality != 60 {
		t.Errorf("webp quality = %d, want 60 (plan)", r3.Quality)
	}
	// plan.Quality=0 → encoders.default-quality.
	r4, err := resolveEffective(EncodersConfig{DefaultQuality: 80, Formats: map[string]FormatEncodersConfig{}}, "webp", 0, nil)
	if err != nil {
		t.Fatalf("resolveEffective(q0): %v", err)
	}
	if r4.Quality != 80 {
		t.Errorf("webp quality = %d, want 80 (default-quality)", r4.Quality)
	}
}

// TestResolveEffectiveRegistryDefault — параметр без Auto и без override —
// registry-дефолт (например bit-depth GIF: 8; lossless false).
func TestResolveEffectiveRegistryDefault(t *testing.T) {
	cfg := EncodersConfig{DefaultQuality: 85, Formats: map[string]FormatEncodersConfig{}}
	r, err := resolveEffective(cfg, "gif", 75, nil)
	if err != nil {
		t.Fatalf("resolveEffective(gif): %v", err)
	}
	if r.BitDepth != 8 {
		t.Errorf("gif bit-depth = %d, want 8 (registry default)", r.BitDepth)
	}
	r2, err := resolveEffective(cfg, "webp", 75, nil)
	if err != nil {
		t.Fatalf("resolveEffective(webp): %v", err)
	}
	if r2.Lossless || r2.NearLossless {
		t.Error("webp lossless/near-lossless must default to false (registry default)")
	}
}

// TestResolvePNGQuantizeFromResolved — решение о PNG-квантовании из
// resolved-параметров: выключено по умолчанию, параметры из реестра.
func TestResolvePNGQuantizeFromResolved(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		d := resolvePNGQuantize(encoding.ResolvedParams{})
		if d.Palette {
			t.Fatal("quantize must be disabled by default")
		}
		if d.Colors != 0 || d.Bitdepth != 0 {
			t.Fatalf("off decision must carry zero values, got %+v", d)
		}
	})

	t.Run("enabled from resolved palette params", func(t *testing.T) {
		d := resolvePNGQuantize(encoding.ResolvedParams{
			Palette:         true,
			PaletteColors:   64,
			PaletteBitDepth: 4,
		})
		if !d.Palette || d.Colors != 64 || d.Bitdepth != 4 {
			t.Fatalf("decision = %+v, want {true 64 4}", d)
		}
	})
}

func intp(v int) *int { return &v }
