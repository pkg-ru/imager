package ffmpeg

import (
	"math"
	"testing"
)

func TestTargetSecond(t *testing.T) {
	tests := []struct {
		name         string
		duration     float64
		framePercent int64
		want         float64
	}{
		{name: "half", duration: 100, framePercent: 50, want: 50},
		{name: "zero percent", duration: 100, framePercent: 0, want: 0},
		{name: "full", duration: 100, framePercent: 100, want: 100},
		{name: "fractional", duration: 10.5, framePercent: 10, want: 1.05},
		{name: "clamp above 100", duration: 100, framePercent: 150, want: 100},
		{name: "clamp below 0", duration: 100, framePercent: -5, want: 0},
		{name: "negative duration", duration: -10, framePercent: 50, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := targetSecond(tt.duration, tt.framePercent)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("targetSecond(%v, %d) = %v, want %v", tt.duration, tt.framePercent, got, tt.want)
			}
		})
	}
}

func TestNextSecond(t *testing.T) {
	tests := []struct {
		name      string
		t         float64
		frameStep int64
		fps       float64
		want      float64
	}{
		{name: "basic", t: 10, frameStep: 25, fps: 25, want: 11},
		{name: "fractional fps", t: 0, frameStep: 30, fps: 30, want: 1},
		{name: "zero step", t: 5, frameStep: 0, fps: 25, want: 5},
		{name: "default fps when zero", t: 0, frameStep: 25, fps: 0, want: 1},
		{name: "default fps when negative", t: 0, frameStep: 25, fps: -1, want: 1},
		{name: "negative step clamped", t: 5, frameStep: -10, fps: 25, want: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextSecond(tt.t, tt.frameStep, tt.fps)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("nextSecond(%v, %d, %v) = %v, want %v", tt.t, tt.frameStep, tt.fps, got, tt.want)
			}
		})
	}
}
