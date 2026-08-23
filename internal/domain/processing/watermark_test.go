package processing

import (
	"reflect"
	"testing"
)

func TestParseWatermarkSize(t *testing.T) {
	cases := []struct {
		in   string
		kind WatermarkSizeKind
		w, h int
		ok   bool
	}{
		{"contain", WatermarkSizeContain, 0, 0, true},
		{"  cover ", WatermarkSizeCover, 0, 0, true},
		{"", WatermarkSizeContain, 0, 0, true}, // пусто = contain
		{"100px 50px", WatermarkSizePixels, 100, 50, true},
		{"100 50", 0, 0, 0, false},           // без px
		{"100px", 0, 0, 0, false},            // одно значение
		{"0px 50px", 0, 0, 0, false},         // нулевая ширина
		{"abcpx 50px", 0, 0, 0, false},       // не число
		{"100px 50px extra", 0, 0, 0, false}, // лишнее поле
	}
	for _, tc := range cases {
		kind, w, h, err := ParseWatermarkSize(tc.in)
		if tc.ok {
			if err != nil {
				t.Errorf("ParseWatermarkSize(%q) unexpected error: %v", tc.in, err)
				continue
			}
			if kind != tc.kind || w != tc.w || h != tc.h {
				t.Errorf("ParseWatermarkSize(%q) = (%v,%d,%d), want (%v,%d,%d)", tc.in, kind, w, h, tc.kind, tc.w, tc.h)
			}
		} else if err == nil {
			t.Errorf("ParseWatermarkSize(%q) expected error", tc.in)
		}
	}
}

func TestNewWatermarkSpecValidation(t *testing.T) {
	// Валидная спецификация.
	wm, err := NewWatermarkSpec("logo", "/w/logo.png", "bottom-right-nope", "", "")
	if err == nil {
		t.Fatal("expected error for invalid position")
	}
	_ = wm
	wm, err = NewWatermarkSpec("logo", "/w/logo.png", WatermarkPositionBottom, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wm.Position != WatermarkPositionBottom {
		t.Errorf("position = %q, want bottom", wm.Position)
	}
	if wm.Repeat != WatermarkRepeatNoRepeat {
		t.Errorf("repeat default = %q, want no-repeat", wm.Repeat)
	}
	if wm.SizeKind != WatermarkSizeContain {
		t.Errorf("size default = %v, want contain", wm.SizeKind)
	}
	if _, err := NewWatermarkSpec("", "/w.png", "center", "no-repeat", "contain"); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := NewWatermarkSpec("a", "", "center", "no-repeat", "contain"); err == nil {
		t.Error("expected error for empty path")
	}
	if _, err := NewWatermarkSpec("a", "/w.png", "center", "sometimes", "contain"); err == nil {
		t.Error("expected error for invalid repeat")
	}
	if _, err := NewWatermarkSpec("a", "/w.png", "center", "no-repeat", "huge"); err == nil {
		t.Error("expected error for invalid size")
	}
}

func TestWatermarkTargetSize(t *testing.T) {
	contain, _ := NewWatermarkSpec("a", "/a.png", "center", "no-repeat", "contain")
	cover, _ := NewWatermarkSpec("a", "/a.png", "center", "no-repeat", "cover")
	px, _ := NewWatermarkSpec("a", "/a.png", "center", "no-repeat", "200px 100px")
	// Холст 1000x500, ватермарка 500x250.
	if w, h := contain.TargetSize(1000, 500, 500, 250); w != 1000 || h != 500 {
		t.Errorf("contain = %dx%d, want 1000x500", w, h)
	}
	// Contain: вписать в квадрат холст 400x400.
	if w, h := contain.TargetSize(400, 400, 500, 250); w != 400 || h != 200 {
		t.Errorf("contain = %dx%d, want 400x200", w, h)
	}
	// Cover: покрыть квадрат холст.
	if w, h := cover.TargetSize(400, 400, 500, 250); w != 800 || h != 400 {
		t.Errorf("cover = %dx%d, want 800x400", w, h)
	}
	// Pixels: точный размер независимо от натурального.
	if w, h := px.TargetSize(400, 400, 500, 250); w != 200 || h != 100 {
		t.Errorf("pixels = %dx%d, want 200x100", w, h)
	}
}

func TestWatermarkLayoutNoRepeat(t *testing.T) {
	top, _ := NewWatermarkSpec("a", "/a.png", "top", "no-repeat", "contain")
	pts := top.Layout(1000, 800, 300, 150)
	want := []Point{{X: 350, Y: 0}} // центр по X, верх по Y
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("layout = %v, want %v", pts, want)
	}
	left, _ := NewWatermarkSpec("a", "/a.png", "left", "no-repeat", "contain")
	pts = left.Layout(1000, 800, 300, 150)
	want = []Point{{X: 0, Y: 325}}
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("layout = %v, want %v", pts, want)
	}
	center, _ := NewWatermarkSpec("a", "/a.png", "center", "no-repeat", "contain")
	pts = center.Layout(1001, 801, 301, 151)
	want = []Point{{X: 350, Y: 325}}
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("layout = %v, want %v", pts, want)
	}
}

func TestWatermarkLayoutRepeat(t *testing.T) {
	rep, _ := NewWatermarkSpec("a", "/a.png", "center", "repeat", "contain")
	pts := rep.Layout(700, 500, 300, 200)
	want := []Point{{0, 0}, {300, 0}, {600, 0}, {0, 200}, {300, 200}, {600, 200}, {0, 400}, {300, 400}, {600, 400}}
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("layout = %v, want %v", pts, want)
	}
}

func TestWatermarkLayoutRepeatX(t *testing.T) {
	rep, _ := NewWatermarkSpec("a", "/a.png", "bottom", "repeat-x", "contain")
	pts := rep.Layout(700, 500, 300, 200)
	// Один ряд на Y = 500-200 = 300; X от 0 с шагом 300.
	want := []Point{{X: 0, Y: 300}, {X: 300, Y: 300}, {X: 600, Y: 300}}
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("layout = %v, want %v", pts, want)
	}
}

func TestWatermarkLayoutRepeatY(t *testing.T) {
	rep, _ := NewWatermarkSpec("a", "/a.png", "right", "repeat-y", "contain")
	pts := rep.Layout(700, 500, 300, 200)
	want := []Point{{X: 400, Y: 0}, {X: 400, Y: 200}, {X: 400, Y: 400}}
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("layout = %v, want %v", pts, want)
	}
}

func TestWatermarkLayoutSpace(t *testing.T) {
	rep, _ := NewWatermarkSpec("a", "/a.png", "center", "space", "contain")
	// Холст 1000x200, копия 200x100: cols=5, gapX=(1000-1000)/4=0,
	// rows=2, gapY=(200-200)/1=0 → плотная сетка без промежутков.
	pts := rep.Layout(1000, 200, 200, 100)
	if len(pts) != 10 {
		t.Fatalf("layout len = %d, want 10", len(pts))
	}
	// С промежутком: холст 1100x100, копия 200x50: cols=5, gapX=(1100-1000)/4=25.
	pts = rep.Layout(1100, 100, 200, 50)
	want := []Point{
		{X: 0, Y: 0}, {X: 225, Y: 0}, {X: 450, Y: 0}, {X: 675, Y: 0}, {X: 900, Y: 0},
		{X: 0, Y: 50}, {X: 225, Y: 50}, {X: 450, Y: 50}, {X: 675, Y: 50}, {X: 900, Y: 50},
	}
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("layout = %v, want %v", pts, want)
	}
}

func TestWatermarkLayoutRound(t *testing.T) {
	rep, _ := NewWatermarkSpec("a", "/a.png", "center", "round", "contain")
	// Холст 1000x500, базовая копия 300x200:
	// cols = ceil(1000/300)=4 → sw=ceil(1000/4)=250;
	// rows = ceil(500/200)=3 → sh=ceil(500/3)=167.
	pts := rep.Layout(1000, 500, 300, 200)
	sw, sh := rep.RoundStep(1000, 500, 300, 200)
	if sw != 250 || sh != 167 {
		t.Errorf("round step = %dx%d, want 250x167", sw, sh)
	}
	cols := (1000 + sw - 1) / sw // ceil
	rows := (500 + sh - 1) / sh  // ceil
	if len(pts) != cols*rows {
		t.Errorf("layout len = %d, want %d (%dx%d)", len(pts), cols*rows, cols, rows)
	}
	// Последняя копия должна доходить до края холста.
	last := pts[len(pts)-1]
	if last.X+sw < 1000 || last.Y+sh < 500 {
		t.Errorf("round layout does not cover canvas: last=%v, step=%dx%d", last, sw, sh)
	}
}
