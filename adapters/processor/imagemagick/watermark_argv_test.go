package imagemagick

import (
	"strings"
	"testing"

	"github.com/pkg-ru/imager/internal/domain/processing"
)

func mustWM(t *testing.T, position, repeat, size string) *processing.WatermarkSpec {
	t.Helper()
	wm, err := processing.NewWatermarkSpec("logo", "/w/logo.png",
		processing.WatermarkPosition(position), processing.WatermarkRepeat(repeat), size)
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	return wm
}

func buildArgvWithWM(t *testing.T, wm *processing.WatermarkSpec) []string {
	t.Helper()
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatWebP,
		processing.Size{Width: 800, Height: 600}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Watermark = wm
	args, err := buildArgv(plan, &Capabilities{}, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	return args
}

func TestBuildArgv_WatermarkNoRepeat(t *testing.T) {
	args := buildArgvWithWM(t, mustWM(t, "top", "no-repeat", "contain"))
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-gravity North") {
		t.Errorf("expected -gravity North for top position, argv: %v", args)
	}
	// Ватермарка идёт как отдельный argv-элемент перед выходным кодером.
	i := indexOf(args, "/w/logo.png")
	if i < 0 {
		t.Fatalf("watermark path not found in argv: %v", args)
	}
	if !strings.HasSuffix(args[len(args)-1], ":-") || i > len(args)-2 {
		t.Errorf("watermark must precede output coder, argv: %v", args)
	}
	// Композит поверх холста.
	for _, tok := range []string{"(", "-compose", "over", "-composite"} {
		if !contains(args, tok) {
			t.Errorf("expected token %q in argv: %v", tok, args)
		}
	}
}

func TestBuildArgv_WatermarkPixelSize(t *testing.T) {
	args := buildArgvWithWM(t, mustWM(t, "center", "no-repeat", "120px 40px"))
	i := indexOf(args, "120x40!")
	if i < 0 {
		t.Errorf("expected exact pixel resize geometry, argv: %v", args)
	}
	if !contains(args, "Center") {
		t.Errorf("expected Center gravity, argv: %v", args)
	}
}

func TestBuildArgv_WatermarkTile(t *testing.T) {
	args := buildArgvWithWM(t, mustWM(t, "center", "repeat", "contain"))
	joined := strings.Join(args, " ")
	for _, tok := range []string{"mpr:wm", "-tile mpr:wm", "color 0,0 reset", "-composite"} {
		if !strings.Contains(joined, tok) {
			t.Errorf("expected tiled watermark token %q in argv: %v", tok, args)
		}
	}
}

func contains(args []string, tok string) bool {
	return indexOf(args, tok) >= 0
}

func indexOf(args []string, tok string) int {
	for i, a := range args {
		if a == tok {
			return i
		}
	}
	return -1
}
