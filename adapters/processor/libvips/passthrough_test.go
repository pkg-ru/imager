// Тесты passthrough fast-path, import-параметров (лимит кадров анимации +
// sequential access) и параметров кодировщиков. Файл без build-tag: вся
// тестируемая логика платформенно-независима.
package libvips

import (
	"testing"

	"github.com/pkg-ru/imager/domain/processing"
)

func mustPlanFull(t *testing.T, mutate func(p *processing.ProcessingPlan)) *processing.ProcessingPlan {
	t.Helper()
	p, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatWebP, processing.FormatWebP,
		processing.Size{Original: true}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

// staticSource — типичный статичный WebP без метаданных.
func staticSource() sourceInfo {
	return sourceInfo{Width: 200, Height: 100, Pages: 1, Orientation: 1}
}

// TestPassthroughEligiblePositive — базовые случаи, где fast-path применим.
func TestPassthroughEligiblePositive(t *testing.T) {
	cases := []struct {
		name string
		plan *processing.ProcessingPlan
		src  sourceInfo
	}{
		{
			name: "size=original",
			plan: mustPlanFull(t, nil),
			src:  staticSource(),
		},
		{
			name: "dimensions already match exact",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 200, Height: 100}
			}),
			src: staticSource(),
		},
		{
			name: "width already matches proportional",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 200}
			}),
			src: staticSource(),
		},
		{
			name: "height already matches proportional",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Height: 100}
			}),
			src: staticSource(),
		},
		{
			name: "no orientation spec",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Orientation = nil
			}),
			src: staticSource(),
		},
		{
			name: "auto-orient with neutral exif orientation 0",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Orientation = &processing.OrientationSpec{AutoOrient: true}
			}),
			src: sourceInfo{Width: 200, Height: 100, Pages: 1, Orientation: 0},
		},
		{
			name: "explicit zero orientation spec",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Orientation = &processing.OrientationSpec{}
			}),
			src: staticSource(),
		},
		{
			name: "technical metadata fields only",
			plan: mustPlanFull(t, nil),
			src: sourceInfo{
				Width: 200, Height: 100, Pages: 3,
				MetaFields: []string{"n-pages", "page-height", "delay", "loop", "background"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !passthroughEligible(tc.plan, tc.src, ColorStrip) {
				t.Errorf("passthroughEligible = false, want true")
			}
		})
	}
}

// TestPassthroughEligibleNegative — отказоустойчивость: любые сомнения
// отклоняют fast-path и приводят к полной обработке.
func TestPassthroughEligibleNegative(t *testing.T) {
	cases := []struct {
		name string
		plan *processing.ProcessingPlan
		src  sourceInfo
	}{
		{
			name: "nil plan",
			plan: nil,
			src:  staticSource(),
		},
		{
			name: "format differs",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.OutputFormats = processing.FormatJPEG
			}),
			src: staticSource(),
		},
		{
			name: "trim enabled",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Trim = true
			}),
			src: staticSource(),
		},
		{
			name: "watermark set",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Watermark = &processing.WatermarkSpec{Name: "wm", Path: "/x.png"}
			}),
			src: staticSource(),
		},
		{
			name: "face-crop operation",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Operation = processing.OpFaceCrop
			}),
			src: staticSource(),
		},
		{
			name: "object-crop operation",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Operation = processing.OpObjectCrop
			}),
			src: staticSource(),
		},
		{
			name: "frames limit set",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Frames = 5
			}),
			src: staticSource(),
		},
		{
			name: "duration limit set",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Duration = 500
			}),
			src: staticSource(),
		},
		{
			name: "loop override",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				f := false
				p.Loop = &f
			}),
			src: staticSource(),
		},
		{
			name: "dpr=2",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.DPR = 2
			}),
			src: staticSource(),
		},
		{
			name: "manual rotate",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Orientation = &processing.OrientationSpec{Rotate: processing.Rotation90}
			}),
			src: staticSource(),
		},
		{
			name: "manual flip",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Orientation = &processing.OrientationSpec{Flip: processing.FlipHorizontal}
			}),
			src: staticSource(),
		},
		{
			name: "auto-orient with exif orientation 6",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Orientation = &processing.OrientationSpec{AutoOrient: true}
			}),
			src: sourceInfo{Width: 200, Height: 100, Pages: 1, Orientation: 6},
		},
		{
			name: "exif metadata present",
			plan: mustPlanFull(t, nil),
			src: sourceInfo{
				Width: 200, Height: 100,
				MetaFields: []string{"exif-data"},
			},
		},
		{
			name: "icc profile present",
			plan: mustPlanFull(t, nil),
			src: sourceInfo{
				Width: 200, Height: 100,
				HasICC: true,
			},
		},
		{
			name: "unknown width",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 200}
			}),
			src: sourceInfo{Width: 0, Height: 100},
		},
		{
			name: "unknown height",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Height: 100}
			}),
			src: sourceInfo{Width: 200, Height: 0},
		},
		{
			name: "size mismatch",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 300, Height: 150}
			}),
			src: staticSource(),
		},
		{
			name: "proportional width mismatch",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 100}
			}),
			src: staticSource(),
		},
		{
			name: "empty size",
			plan: mustPlanFull(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{}
			}),
			src: staticSource(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if passthroughEligible(tc.plan, tc.src, ColorStrip) {
				t.Errorf("passthroughEligible = true, want false")
			}
		})
	}
}

// TestPassthroughEligibleColorMode — взаимодействие ICC-профиля и политики
// color management (Фаза 5a): passthrough допускается для sRGB-совместимого
// профиля в режиме transform и для любого профиля в режиме keep; в режиме
// strip (и для не-sRGB профиля в transform) — отклоняется.
func TestPassthroughEligibleColorMode(t *testing.T) {
	base := mustPlanFull(t, nil)
	withICC := func(srgb bool) sourceInfo {
		s := staticSource()
		s.HasICC = true
		s.SRGBProfile = srgb
		return s
	}

	// transform + sRGB-совместимый профиль → passthrough допустим.
	if !passthroughEligible(base, withICC(true), ColorTransform) {
		t.Error("transform + sRGB profile: want passthrough")
	}
	// transform + не-sRGB профиль → passthrough отклонён (нужна конверсия).
	if passthroughEligible(base, withICC(false), ColorTransform) {
		t.Error("transform + non-sRGB profile: want no passthrough")
	}
	// keep + любой профиль → passthrough допустим (профиль сохраняется).
	if !passthroughEligible(base, withICC(false), ColorKeep) {
		t.Error("keep + non-sRGB profile: want passthrough")
	}
	// strip + sRGB профиль → passthrough отклонён (конвейер удаляет профиль).
	if passthroughEligible(base, withICC(true), ColorStrip) {
		t.Error("strip + sRGB profile: want no passthrough")
	}
	// strip + не-sRGB профиль → passthrough отклонён.
	if passthroughEligible(base, withICC(false), ColorStrip) {
		t.Error("strip + non-sRGB profile: want no passthrough")
	}
	// transform + sRGB профиль, но есть EXIF-метаданные → всё равно
	// отклоняется (passthrough не должен просачивать EXIF).
	src := withICC(true)
	src.MetaFields = []string{"exif-data"}
	if passthroughEligible(base, src, ColorTransform) {
		t.Error("transform + sRGB profile + exif: want no passthrough")
	}
}

// TestSizeMatchesUnknownDimensions проверяет консервативность sizeMatches:
// неизвестные размеры никогда не дают положительного решения.
func TestSizeMatchesUnknownDimensions(t *testing.T) {
	s := processing.Size{Original: true}
	if !sizeMatches(s, 0, 0) {
		t.Error("original size with unknown dims: want true (original не зависит от размеров)")
	}
	exact := processing.Size{Width: 100, Height: 50}
	if sizeMatches(exact, 0, 50) || sizeMatches(exact, 100, 0) || sizeMatches(exact, 0, 0) {
		t.Error("exact size with unknown dims: want false")
	}
}

// TestResolveImportPlanFrameLimit — лимит кадров анимации и NumPages.
func TestResolveImportPlanFrameLimit(t *testing.T) {
	cases := []struct {
		name         string
		src, out     processing.Format
		frames       int
		wantSetPages bool
		wantNumPages int
	}{
		{
			name:         "static to static: no pages param",
			src:          processing.FormatJPEG,
			out:          processing.FormatJPEG,
			frames:       0,
			wantSetPages: false,
		},
		{
			name:         "animated output no limit: all pages",
			src:          processing.FormatGIF,
			out:          processing.FormatWebP,
			frames:       0,
			wantSetPages: true,
			wantNumPages: -1,
		},
		{
			name:         "animated input only: all pages",
			src:          processing.FormatAPNG,
			out:          processing.FormatJPEG,
			frames:       0,
			wantSetPages: true,
			wantNumPages: -1,
		},
		{
			name:         "animated with frames limit",
			src:          processing.FormatGIF,
			out:          processing.FormatWebP,
			frames:       10,
			wantSetPages: true,
			wantNumPages: 10,
		},
		{
			name:         "frames limit ignored for static pipeline",
			src:          processing.FormatJPEG,
			out:          processing.FormatPNG,
			frames:       5,
			wantSetPages: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := processing.NewProcessingPlan(
				processing.OpResize, tc.src, tc.out,
				processing.Size{Width: 100}, 1, 85, nil, tc.frames, 0,
			)
			if err != nil {
				t.Fatalf("NewProcessingPlan: %v", err)
			}
			got := resolveImportPlan(p)
			if got.SetPages != tc.wantSetPages {
				t.Fatalf("SetPages = %v, want %v", got.SetPages, tc.wantSetPages)
			}
			if got.SetPages && got.NumPages != tc.wantNumPages {
				t.Errorf("NumPages = %d, want %d", got.NumPages, tc.wantNumPages)
			}
		})
	}
}

// TestResolveImportPlanSequentialAccess — sequential access только там,
// где это безопасно (один линейный проход по пикселям).
func TestResolveImportPlanSequentialAccess(t *testing.T) {
	cases := []struct {
		name    string
		op      processing.Operation
		trim    bool
		wantSeq bool
	}{
		{name: "resize", op: processing.OpResize, wantSeq: true},
		{name: "crop", op: processing.OpCrop, wantSeq: true},
		{name: "smart-crop", op: processing.OpSmartCrop, wantSeq: true},
		{name: "face-crop reads pixels twice", op: processing.OpFaceCrop, wantSeq: false},
		{name: "object-crop reads pixels twice", op: processing.OpObjectCrop, wantSeq: false},
		{name: "trim needs full scan first", op: processing.OpResize, trim: true, wantSeq: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := processing.NewProcessingPlan(
				tc.op, processing.FormatJPEG, processing.FormatWebP,
				processing.Size{Width: 100}, 1, 85, nil, 0, 0,
			)
			if err != nil {
				t.Fatalf("NewProcessingPlan: %v", err)
			}
			p.Trim = tc.trim
			if got := resolveImportPlan(p).Sequential; got != tc.wantSeq {
				t.Errorf("Sequential = %v, want %v", got, tc.wantSeq)
			}
		})
	}
}
