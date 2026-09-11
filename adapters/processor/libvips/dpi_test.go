// Тесты чистой логики DPI-нормализации (без build-tag): решение о
// необходимости сброса xres/yres к стандартному DPI.
//
// ВАЖНО: libvips хранит разрешение в пикселях на миллиметр (px/mm), а не в
// DPI. Поэтому xres/yres в тестах задаются в px/mm: 72 DPI = 72/25.4 ≈ 2.8346,
// 300 DPI ≈ 11.811, 96 DPI ≈ 3.7795, 144 DPI ≈ 5.6693, 1200 DPI ≈ 47.244.
package libvips

import "testing"

// TestNeedsResolutionNormalization — решение о нормализации resolution:
//   - уже 72 DPI (2.8346 px/mm) — НЕ требует (быстрый путь);
//   - xres/yres ≈ 0/1 (конвенция libvips «нет DPI-метаданных») — НЕ требует
//     (просмотрщики показывают 1:1, сбрасывать нечего);
//   - 300 DPI (сканы/принт-исходники) — требует (вьюеры иначе масштабируют);
//   - 96/144 DPI — требует (экранные/печатные экспорты с явным DPI);
//   - разнобой x/y — требует;
//   - отклонение в пределах эпсилона — не требует;
//   - нецелевой targetDPI — работает относительно цели.
func TestNeedsResolutionNormalization(t *testing.T) {
	const dpi = 72.0
	// Значения в px/mm.
	const (
		px72   = 72.0 / 25.4   // 2.8346
		px96   = 96.0 / 25.4   // 3.7795
		px144  = 144.0 / 25.4  // 5.6693
		px300  = 300.0 / 25.4  // 11.811
		px1200 = 1200.0 / 25.4 // 47.244
	)
	cases := []struct {
		name string
		x    float64
		y    float64
		want bool
	}{
		{"72/72 not needed", px72, px72, false},
		{"72.1/72 rounded within epsilon", px72 + 0.1, px72, false},
		{"71.9/72 rounded within epsilon", px72 - 0.1, px72, false},
		// «Нет DPI-метаданных»: xres/yres = 1.0 (libvips), 0.0 (некоторые
		// конвейеры) — не требует нормализации.
		{"absent 1/1 not needed", 1, 1, false},
		{"absent 0/0 not needed", 0, 0, false},
		{"absent 0/1 not needed", 0, 1, false},
		// Значимые разрешения — требуют нормализации.
		{"300/300 scan needs normalization", px300, px300, true},
		{"299/300 scan needs normalization", px300 - 0.1, px300, true},
		{"x=300 y=72 mismatched", px300, px72, true},
		{"x=96 y=96 web dpi", px96, px96, true},
		{"x=144 y=144 print dpi", px144, px144, true},
		{"x=1200 y=1200 scanner", px1200, px1200, true},
		// Смешанный случай: одна ось «без DPI», другая значимая.
		{"x=1 y=300 one axis significant", 1, px300, true},
	}
	for _, tc := range cases {
		if got := needsResolutionNormalization(tc.x, tc.y, dpi); got != tc.want {
			t.Errorf("%s: needsResolutionNormalization(%v,%v) = %v, want %v",
				tc.name, tc.x, tc.y, got, tc.want)
		}
	}

	// targetDPI <= 0 → используется стандарт 72.
	if needsResolutionNormalization(px300, px300, 0) != true {
		t.Error("targetDPI=0 must fall back to default 72 and flag 300dpi")
	}
	if needsResolutionNormalization(px72, px72, 0) != false {
		t.Error("targetDPI=0 must fall back to default 72 and pass 72dpi")
	}
	// Нестандартная цель: 150 DPI (150/25.4 ≈ 5.9055 px/mm).
	px150 := 150.0 / 25.4
	if !needsResolutionNormalization(px72, px72, 150) {
		t.Error("72dpi must be normalized when target is 150")
	}
	if needsResolutionNormalization(px150, px150, 150) {
		t.Error("150dpi must not be normalized when target is 150")
	}
}
