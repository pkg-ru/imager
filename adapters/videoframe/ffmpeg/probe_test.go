package ffmpeg

import (
	"math"
	"testing"
)

func TestParseProbeJSON(t *testing.T) {
	t.Run("full stream info", func(t *testing.T) {
		data := []byte(`{
			"streams": [{
				"duration": "10.5",
				"r_frame_rate": "30000/1001",
				"width": 1920,
				"height": 1080
			}],
			"format": {"duration": "10.5"}
		}`)
		info, err := parseProbeJSON(data)
		if err != nil {
			t.Fatalf("parseProbeJSON: %v", err)
		}
		if math.Abs(info.Duration-10.5) > 1e-9 {
			t.Fatalf("duration = %v, want 10.5", info.Duration)
		}
		if math.Abs(info.FPS-30000.0/1001.0) > 1e-6 {
			t.Fatalf("fps = %v, want ~29.97", info.FPS)
		}
		if info.Width != 1920 || info.Height != 1080 {
			t.Fatalf("size = %dx%d, want 1920x1080", info.Width, info.Height)
		}
	})

	t.Run("duration from format when stream missing", func(t *testing.T) {
		data := []byte(`{
			"streams": [{"r_frame_rate": "25", "width": 640, "height": 480}],
			"format": {"duration": "7.25"}
		}`)
		info, err := parseProbeJSON(data)
		if err != nil {
			t.Fatalf("parseProbeJSON: %v", err)
		}
		if math.Abs(info.Duration-7.25) > 1e-9 {
			t.Fatalf("duration = %v, want 7.25", info.Duration)
		}
		if math.Abs(info.FPS-25) > 1e-9 {
			t.Fatalf("fps = %v, want 25", info.FPS)
		}
	})

	t.Run("default fps when unavailable", func(t *testing.T) {
		data := []byte(`{
			"streams": [{"width": 320, "height": 240}],
			"format": {"duration": "3"}
		}`)
		info, err := parseProbeJSON(data)
		if err != nil {
			t.Fatalf("parseProbeJSON: %v", err)
		}
		if info.FPS != defaultFPS {
			t.Fatalf("fps = %v, want default %v", info.FPS, defaultFPS)
		}
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		if _, err := parseProbeJSON([]byte("{not json")); err == nil {
			t.Fatal("expected error for invalid json")
		}
	})
}

func TestParseFrameRate(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{in: "25", want: 25, ok: true},
		{in: "30000/1001", want: 30000.0 / 1001.0, ok: true},
		{in: "0/0", want: 0, ok: false},
		{in: "abc", want: 0, ok: false},
		{in: "", want: 0, ok: false},
	}
	for _, tt := range tests {
		got, ok := parseFrameRate(tt.in)
		if ok != tt.ok {
			t.Fatalf("parseFrameRate(%q) ok = %v, want %v", tt.in, ok, tt.ok)
		}
		if ok && math.Abs(got-tt.want) > 1e-9 {
			t.Fatalf("parseFrameRate(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
