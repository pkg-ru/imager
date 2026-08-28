// Тесты shrink-on-load (Фаза 2): вычисление коэффициента предварительного
// уменьшения при декодировании. Файл без build-tag: тестируемая логика
// платформенно-независима (shrinkonload.go).
package libvips

import (
	"testing"

	"github.com/pkg-ru/imager/domain/processing"
)

func mustPlanShrink(t *testing.T, mutate func(p *processing.ProcessingPlan)) *processing.ProcessingPlan {
	t.Helper()
	p, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatWebP,
		processing.Size{Width: 100}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

// TestResolveShrinkOnLoadJPEG — таблица кейсов для JPEG (shrink степени
// двойки). Исходник 4000x3000.
func TestResolveShrinkOnLoadJPEG(t *testing.T) {
	src := shrinkOnLoadInfo{Width: 4000, Height: 3000, Pages: 1}
	cases := []struct {
		name      string
		mutate    func(p *processing.ProcessingPlan)
		wantJpeg  int
		wantScale float64
		wantApply bool
	}{
		{
			name:      "target 100px wide: shrink 8",
			mutate:    nil,
			wantJpeg:  8,
			wantScale: 1,
			wantApply: true,
		},
		{
			name: "target 1000px wide: shrink 2",
			mutate: func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 1000}
			},
			wantJpeg:  2,
			wantScale: 1,
			wantApply: true,
		},
		{
			name: "target 2000px wide: shrink 1 (no gain)",
			mutate: func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 2000}
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "target larger than source: no shrink",
			mutate: func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: 8000}
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "size=original: disabled",
			mutate: func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Original: true}
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "trim plan: disabled",
			mutate: func(p *processing.ProcessingPlan) {
				p.Trim = true
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "smart-crop plan: disabled",
			mutate: func(p *processing.ProcessingPlan) {
				p.Operation = processing.OpSmartCrop
				p.Size = processing.Size{Width: 100, Height: 100}
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "face-crop plan: disabled",
			mutate: func(p *processing.ProcessingPlan) {
				p.Operation = processing.OpFaceCrop
				p.Size = processing.Size{Width: 100, Height: 100}
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "object-crop plan: disabled",
			mutate: func(p *processing.ProcessingPlan) {
				p.Operation = processing.OpObjectCrop
				p.Size = processing.Size{Width: 100, Height: 100}
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "manual rotate: disabled",
			mutate: func(p *processing.ProcessingPlan) {
				or, err := processing.NewOrientationSpec(true, processing.Rotation90, processing.FlipNone)
				if err != nil {
					t.Fatalf("NewOrientationSpec: %v", err)
				}
				p.Orientation = or
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "manual flip: disabled",
			mutate: func(p *processing.ProcessingPlan) {
				or, err := processing.NewOrientationSpec(true, processing.RotationNone, processing.FlipVertical)
				if err != nil {
					t.Fatalf("NewOrientationSpec: %v", err)
				}
				p.Orientation = or
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name:      "neutral orientation: allowed",
			mutate:    nil,
			wantJpeg:  8,
			wantScale: 1,
			wantApply: true,
		},
		{
			name: "watermark: conservatively disabled",
			mutate: func(p *processing.ProcessingPlan) {
				p.Watermark = &processing.WatermarkSpec{Name: "wm", Path: "/tmp/wm.png"}
			},
			wantJpeg:  1,
			wantScale: 1,
			wantApply: false,
		},
		{
			name: "DPR 2 doubles target bounds",
			mutate: func(p *processing.ProcessingPlan) {
				p.DPR = 2 // цель 200px → фактор по ширине 4000/400=10 → shrink 8
			},
			wantJpeg:  8,
			wantScale: 1,
			wantApply: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := mustPlanShrink(t, tc.mutate)
			got := resolveShrinkOnLoad(plan, src, 1, true)
			if got.JpegShrink != tc.wantJpeg || got.Scale != tc.wantScale || got.applied() != tc.wantApply {
				t.Errorf("resolveShrinkOnLoad = %+v (applied=%v), want jpeg=%d scale=%v applied=%v",
					got, got.applied(), tc.wantJpeg, tc.wantScale, tc.wantApply)
			}
		})
	}
}

// TestResolveShrinkOnLoadFormats — поддержка форматов и scale-on-load.
func TestResolveShrinkOnLoadFormats(t *testing.T) {
	src := shrinkOnLoadInfo{Width: 4000, Height: 3000, Pages: 1}
	cases := []struct {
		name      string
		format    processing.Format
		wantApply bool
		check     func(t *testing.T, d shrinkOnLoadDecision)
	}{
		{
			name:      "webp uses scale",
			format:    processing.FormatWebP,
			wantApply: true,
			check: func(t *testing.T, d shrinkOnLoadDecision) {
				if d.Scale >= 1 || d.Scale <= 0 {
					t.Fatalf("Scale = %v, want in (0,1)", d.Scale)
				}
				// После scale размер должен остаться >= цели (100px).
				if int(float64(src.Width)*d.Scale) < 100 {
					t.Errorf("scaled width %d < target 100", int(float64(src.Width)*d.Scale))
				}
			},
		},
		{name: "heif supported", format: processing.FormatHEIF, wantApply: true},
		{name: "avif supported", format: processing.FormatAVIF, wantApply: true},
		{name: "gif static supported", format: processing.FormatGIF, wantApply: true},
		{name: "png not supported", format: processing.FormatPNG, wantApply: false},
		{name: "apng not supported", format: processing.FormatAPNG, wantApply: false},
		{name: "jxl not supported", format: processing.FormatJPEGXL, wantApply: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := mustPlanShrink(t, func(p *processing.ProcessingPlan) {
				p.SourceFormat = tc.format
			})
			got := resolveShrinkOnLoad(plan, src, 1, true)
			if got.applied() != tc.wantApply {
				t.Fatalf("applied = %v (%+v), want %v", got.applied(), got, tc.wantApply)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

// TestResolveShrinkOnLoadAnimations — анимации: GIF отключён консервативно,
// остальные форматы допустимы (scale равномерен по кадрам).
func TestResolveShrinkOnLoadAnimations(t *testing.T) {
	animSrc := shrinkOnLoadInfo{Width: 4000, Height: 500, Pages: 20} // page-height 500
	cases := []struct {
		name      string
		format    processing.Format
		frames    int
		wantApply bool
	}{
		{name: "animated gif: disabled", format: processing.FormatGIF, wantApply: false},
		{name: "animated webp: allowed", format: processing.FormatWebP, wantApply: true},
		{name: "animated webp with frames limit: allowed", format: processing.FormatWebP, frames: 10, wantApply: true},
		{name: "animated heif: allowed", format: processing.FormatHEIF, wantApply: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := mustPlanShrink(t, func(p *processing.ProcessingPlan) {
				p.SourceFormat = tc.format
				p.OutputFormats = tc.format
				p.Frames = tc.frames
			})
			got := resolveShrinkOnLoad(plan, animSrc, 1, true)
			if got.applied() != tc.wantApply {
				t.Errorf("applied = %v (%+v), want %v", got.applied(), got, tc.wantApply)
			}
		})
	}
}

// TestResolveShrinkOnLoadDisabled — конфигурационный выключатель и
// неизвестные размеры исходника.
func TestResolveShrinkOnLoadDisabled(t *testing.T) {
	plan := mustPlanShrink(t, nil)
	src := shrinkOnLoadInfo{Width: 4000, Height: 3000, Pages: 1}
	if got := resolveShrinkOnLoad(plan, src, 1, false); got.applied() {
		t.Errorf("enabled=false: applied = %+v, want none", got)
	}
	noSize := shrinkOnLoadInfo{Width: 0, Height: 0, Pages: 1}
	if got := resolveShrinkOnLoad(plan, noSize, 1, true); got.applied() {
		t.Errorf("unknown source size: applied = %+v, want none", got)
	}
	// Ненейтральный EXIF-поворот при auto-orient.
	or, err := processing.NewOrientationSpec(true, processing.RotationNone, processing.FlipNone)
	if err != nil {
		t.Fatalf("NewOrientationSpec: %v", err)
	}
	plan.Orientation = or
	if got := resolveShrinkOnLoad(plan, src, 6, true); got.applied() {
		t.Errorf("exif orientation 6 with auto-orient: applied = %+v, want none", got)
	}
}

// TestShrinkFactorGuarantee — инвариант запаса ×2: после применения
// вычисленного фактора оба измерения остаются >= целевых.
func TestShrinkFactorGuarantee(t *testing.T) {
	cases := []struct {
		name string
		srcW int
		srcH int
		tw   int
		th   int
		dpr  int
	}{
		{"proportional width", 4000, 3000, 100, 0, 0},
		{"proportional height", 4000, 3000, 0, 100, 0},
		{"exact both", 4000, 3000, 100, 75, 0},
		{"dpr 2", 4000, 3000, 100, 0, 2},
		{"portrait source", 1000, 6000, 50, 0, 0},
		{"tiny target", 8000, 6000, 16, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := mustPlanShrink(t, func(p *processing.ProcessingPlan) {
				p.Size = processing.Size{Width: tc.tw, Height: tc.th}
				p.DPR = tc.dpr
			})
			src := shrinkOnLoadInfo{Width: tc.srcW, Height: tc.srcH, Pages: 1}
			factor := shrinkFactorForSource(plan, src)
			if factor <= 1 {
				return // shrink не применяется — инвариант тривиален
			}
			dpr := tc.dpr
			if dpr <= 0 {
				dpr = 1
			}
			targetW, targetH := tc.tw*dpr, tc.th*dpr
			if targetW == 0 {
				targetW = scaleByRatio(targetH, tc.srcW, tc.srcH)
			}
			if targetH == 0 {
				targetH = scaleByRatio(targetW, tc.srcH, tc.srcW)
			}
			gotW := int(float64(tc.srcW) / factor)
			gotH := int(float64(tc.srcH) / factor)
			if gotW < targetW || gotH < targetH {
				t.Errorf("factor %v gives %dx%d, target %dx%d (must be >=)",
					factor, gotW, gotH, targetW, targetH)
			}
		})
	}
}

// TestJpegShrinkOnLoad — маппинг вещественного коэффициента в степени
// двойки JPEG.
func TestJpegShrinkOnLoad(t *testing.T) {
	cases := []struct {
		factor float64
		want   int
	}{
		{1.0, 1},
		{1.9, 1},
		{2.0, 2},
		{3.9, 2},
		{4.0, 4},
		{7.9, 4},
		{8.0, 8},
		{100.0, 8}, // максимум JPEG
	}
	for _, tc := range cases {
		if got := jpegShrinkOnLoad(tc.factor); got != tc.want {
			t.Errorf("jpegShrinkOnLoad(%v) = %d, want %d", tc.factor, got, tc.want)
		}
	}
}

// TestScaleShrinkOnLoad — округление scale вниз до сотых (запас в пользу
// качества).
func TestScaleShrinkOnLoad(t *testing.T) {
	cases := []struct {
		factor float64
		want   float64
	}{
		{1.0, 1.0},
		{0.5, 1.0},
		{2.0, 0.5},
		{5.3763, 0.18},
		{10.0, 0.1},
	}
	for _, tc := range cases {
		if got := scaleShrinkOnLoad(tc.factor); got != tc.want {
			t.Errorf("scaleShrinkOnLoad(%v) = %v, want %v", tc.factor, got, tc.want)
		}
	}
}

// TestShrinkOnLoadOptsDefault — умолчание включено; явное выключение
// работает.
func TestShrinkOnLoadOptsDefault(t *testing.T) {
	var zero ShrinkOnLoadOpts
	if !zero.Enabled() {
		t.Error("zero value must be enabled (default)")
	}
	if !NewShrinkOnLoadOpts(false, false).Enabled() {
		t.Error("non-explicit value must default to enabled")
	}
	if NewShrinkOnLoadOpts(false, true).Enabled() {
		t.Error("explicit enabled=false must disable")
	}
	if !NewShrinkOnLoadOpts(true, true).Enabled() {
		t.Error("explicit enabled=true must stay enabled")
	}
}
