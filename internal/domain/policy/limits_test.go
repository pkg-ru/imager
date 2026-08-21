package policy

import (
	"math"
	"testing"
)

func TestNewLimitsValid(t *testing.T) {
	l, err := NewLimits(Limits{SourceBytes: 100, Width: 1000, Height: 1000, Pixels: 1_000_000, DPR: 2, Frames: 10, OutputBytes: 500, Duration: 1000, Concurrency: 4})
	if err != nil {
		t.Fatalf("NewLimits error: %v", err)
	}
	if l.SourceBytes != 100 {
		t.Errorf("SourceBytes = %d, want 100", l.SourceBytes)
	}
}

func TestNewLimitsInvalid(t *testing.T) {
	bad := []Limits{
		{SourceBytes: -1},
		{Width: -1},
		{Height: -1},
		{Pixels: -1},
		{DPR: -1},
		{Frames: -1},
		{OutputBytes: -1},
		{Duration: -1},
		{Concurrency: -1},
	}
	for _, l := range bad {
		if _, err := NewLimits(l); err == nil {
			t.Errorf("NewLimits(%+v) expected error", l)
		}
	}
}

func TestLimitsCheck(t *testing.T) {
	// Всё в пределах.
	l, _ := NewLimits(Limits{SourceBytes: 1000, Width: 100, Height: 100, Pixels: 10_000, DPR: 2, Frames: 5, OutputBytes: 500, Duration: 1000})
	if r := l.Check(500, 50, 50, 1, 3, 200, 500); r.Exceeded() {
		t.Errorf("expected no exceed, got %+v", r)
	}

	cases := []struct {
		name string
		lim  Limits
		args []int64
		want string
	}{
		{"source_bytes", Limits{SourceBytes: 1000}, []int64{2000, 50, 50, 1, 3, 200, 500}, "source_bytes"},
		{"width", Limits{Width: 100}, []int64{500, 200, 50, 1, 3, 200, 500}, "width"},
		{"height", Limits{Height: 100}, []int64{500, 50, 200, 1, 3, 200, 500}, "height"},
		// width/height в пределах 1000, но произведение 200x200=40000 > 10000.
		{"pixels", Limits{Width: 1000, Height: 1000, Pixels: 10_000}, []int64{500, 200, 200, 1, 3, 200, 500}, "pixels"},
		{"dpr", Limits{DPR: 2}, []int64{500, 50, 50, 3, 3, 200, 500}, "dpr"},
		{"frames", Limits{Frames: 5}, []int64{500, 50, 50, 1, 10, 200, 500}, "frames"},
		{"output_bytes", Limits{OutputBytes: 500}, []int64{500, 50, 50, 1, 3, 600, 500}, "output_bytes"},
		{"duration", Limits{Duration: 1000}, []int64{500, 50, 50, 1, 3, 200, 2000}, "duration"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			lim, _ := NewLimits(tt.lim)
			r := lim.Check(tt.args[0], int(tt.args[1]), int(tt.args[2]), int(tt.args[3]), int(tt.args[4]), tt.args[5], tt.args[6])
			if !r.Exceeded() || r.ExceededLimit != tt.want {
				t.Errorf("expected %s exceed, got %+v", tt.want, r)
			}
		})
	}
}

func TestLimitsCheckPixelsOverflow(t *testing.T) {
	// Огромные размеры, произведение которых переполняет int64.
	l, _ := NewLimits(Limits{Pixels: 100})
	r := l.Check(0, math.MaxInt64, math.MaxInt64, 1, 1, 0, 0)
	if !r.Exceeded() || r.ExceededLimit != "pixels" {
		t.Errorf("expected pixels overflow exceed, got %+v", r)
	}
	if r.Actual != math.MaxInt64 {
		t.Errorf("Actual = %d, want MaxInt64", r.Actual)
	}
}

func TestLimitsUnlimited(t *testing.T) {
	l := Unlimited()
	if r := l.Check(math.MaxInt64, math.MaxInt32, math.MaxInt32, 100, 100, math.MaxInt64, math.MaxInt64); r.Exceeded() {
		t.Errorf("unlimited limits should never exceed, got %+v", r)
	}
}

func TestLimitsZeroMeansUnlimited(t *testing.T) {
	// 0 для лимита означает "не ограничено".
	l, _ := NewLimits(Limits{})
	if r := l.Check(1_000_000, 100_000, 100_000, 100, 100, 1_000_000, 1_000_000); r.Exceeded() {
		t.Errorf("zero limits should be unlimited, got %+v", r)
	}
}
