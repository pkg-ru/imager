package policy

import (
	"math"
	"testing"

	"github.com/pkg-ru/dynamic"
)

func TestNewLimitsValid(t *testing.T) {
	l, err := NewLimits(Limits{SourceBytes: dynamic.Int64(100), Width: dynamic.Int64(1000), Height: dynamic.Int64(1000), Pixels: dynamic.Int64(1_000_000), DPR: dynamic.Int64(2), Frames: dynamic.Int64(10), OutputBytes: dynamic.Int64(500), Duration: dynamic.Int64(1000), Concurrency: dynamic.Int64(4)})
	if err != nil {
		t.Fatalf("NewLimits error: %v", err)
	}
	if l.SourceBytes.Unwrap() != 100 {
		t.Errorf("SourceBytes = %d, want 100", l.SourceBytes.Unwrap())
	}
}

func TestNewLimitsInvalid(t *testing.T) {
	bad := []Limits{
		{SourceBytes: dynamic.Int64(-1)},
		{Width: dynamic.Int64(-1)},
		{Height: dynamic.Int64(-1)},
		{Pixels: dynamic.Int64(-1)},
		{DPR: dynamic.Int64(-1)},
		{Frames: dynamic.Int64(-1)},
		{OutputBytes: dynamic.Int64(-1)},
		{Duration: dynamic.Int64(-1)},
		{Concurrency: dynamic.Int64(-1)},
	}
	for _, l := range bad {
		if _, err := NewLimits(l); err == nil {
			t.Errorf("NewLimits(%+v) expected error", l)
		}
	}
}

func TestLimitsCheck(t *testing.T) {
	// Всё в пределах.
	l, _ := NewLimits(Limits{SourceBytes: dynamic.Int64(1000), Width: dynamic.Int64(100), Height: dynamic.Int64(100), Pixels: dynamic.Int64(10_000), DPR: dynamic.Int64(2), Frames: dynamic.Int64(5), OutputBytes: dynamic.Int64(500), Duration: dynamic.Int64(1000)})
	if r := l.Check(500, 50, 50, 1, 3, 200, 500); r.Exceeded() {
		t.Errorf("expected no exceed, got %+v", r)
	}

	cases := []struct {
		name string
		lim  Limits
		args []int64
		want string
	}{
		{"source_bytes", Limits{SourceBytes: dynamic.Int64(1000)}, []int64{2000, 50, 50, 1, 3, 200, 500}, "source_bytes"},
		{"width", Limits{Width: dynamic.Int64(100)}, []int64{500, 200, 50, 1, 3, 200, 500}, "width"},
		{"height", Limits{Height: dynamic.Int64(100)}, []int64{500, 50, 200, 1, 3, 200, 500}, "height"},
		// width/height в пределах 1000, но произведение 200x200=40000 > 10000.
		{"pixels", Limits{Width: dynamic.Int64(1000), Height: dynamic.Int64(1000), Pixels: dynamic.Int64(10_000)}, []int64{500, 200, 200, 1, 3, 200, 500}, "pixels"},
		{"dpr", Limits{DPR: dynamic.Int64(2)}, []int64{500, 50, 50, 3, 3, 200, 500}, "dpr"},
		{"frames", Limits{Frames: dynamic.Int64(5)}, []int64{500, 50, 50, 1, 10, 200, 500}, "frames"},
		{"output_bytes", Limits{OutputBytes: dynamic.Int64(500)}, []int64{500, 50, 50, 1, 3, 600, 500}, "output_bytes"},
		{"duration", Limits{Duration: dynamic.Int64(1000)}, []int64{500, 50, 50, 1, 3, 200, 2000}, "duration"},
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
	l, _ := NewLimits(Limits{Pixels: dynamic.Int64(100)})
	r := l.Check(0, math.MaxInt64, math.MaxInt64, 1, 1, 0, 0)
	if !r.Exceeded() || r.ExceededLimit != "pixels" {
		t.Errorf("expected pixels overflow exceed, got %+v", r)
	}
	if r.Actual != math.MaxInt64 {
		t.Errorf("Actual = %d, want MaxInt64", r.Actual)
	}
}

func TestLimitsUnlimited(t *testing.T) {
	// Нулевые лимиты (эквивалент бывшего Unlimited()) ничего не ограничивают.
	l, _ := NewLimits(Limits{})
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
