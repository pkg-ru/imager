package imagemagick

import (
	"strings"
	"testing"

	"github.com/pkg-ru/imager/internal/domain/processing"
)

func boolPtr(b bool) *bool { return &b }

func TestBuildArgv_ValidResize(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 800, Height: 600}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	if len(args) == 0 {
		t.Fatal("empty args")
	}
	// Первый аргумент — -quiet.
	if args[0] != "-quiet" {
		t.Errorf("first arg = %q, want -quiet", args[0])
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-thumbnail 800x600") {
		t.Errorf("missing thumbnail, got: %s", joined)
	}
	// -extent применяется только для crop-операций (не для OpResize).
	if strings.Contains(joined, "-extent") {
		t.Errorf("resize should not use -extent (letterbox), got: %s", joined)
	}
	if !strings.Contains(joined, "-quality 85") {
		t.Errorf("missing quality, got: %s", joined)
	}
	if !strings.Contains(joined, "PNG:-") {
		t.Errorf("missing output coder, got: %s", joined)
	}
	// Неанимированный выход -> первый кадр.
	if !strings.Contains(joined, "-[0]") {
		t.Errorf("missing first-frame marker, got: %s", joined)
	}
	// C5: -auto-orient ДО -strip.
	if !strings.Contains(joined, "-auto-orient") {
		t.Errorf("missing -auto-orient, got: %s", joined)
	}
	if strings.Index(joined, "-auto-orient") > strings.Index(joined, "-strip") {
		t.Errorf("-auto-orient must come before -strip, got: %s", joined)
	}
	// sampling-factor применяется только для JPEG-выхода.
	if strings.Contains(joined, "-sampling-factor") {
		t.Errorf("sampling-factor should only be added for JPEG output, got: %s", joined)
	}
}

func TestBuildArgv_ValidCrop(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpCrop, processing.FormatPNG, processing.FormatJPEG,
		processing.Size{Width: 400, Height: 300}, 2, 90, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-thumbnail 400x300^") {
		t.Errorf("crop should use ^, got: %s", joined)
	}
}

func TestBuildArgv_ValidTrim(t *testing.T) {
	// Trim — независимый фильтр: trim-only выражается как OpResize + Trim=true.
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Trim = true
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-trim") {
		t.Errorf("trim should include -trim, got: %s", joined)
	}
	// trim-bounds — IM7-only. Без снимка capabilities (major=0) считаем
	// IM7-совместимым.
	if !strings.Contains(joined, "trim-bounds") {
		t.Errorf("trim should include trim-bounds (IM7), got: %s", joined)
	}
}

func TestBuildArgv_TrimIM6NoTrimBounds(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Trim = true
	caps := &Capabilities{Major: 6}
	args, err := buildArgv(plan, caps, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "trim-bounds") {
		t.Errorf("IM6 should not include trim-bounds, got: %s", joined)
	}
	if !strings.Contains(joined, "-trim") {
		t.Errorf("trim should include -trim, got: %s", joined)
	}
}

func TestBuildArgv_AnimatedOutputNoFrameIndex(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 100, Height: 100}, 1, 80, boolPtr(true), 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-[0]") {
		t.Errorf("animated output should not use first-frame marker, got: %s", joined)
	}
	if !strings.Contains(joined, "-loop 0") {
		t.Errorf("loop=true should add -loop 0, got: %s", joined)
	}
}

func TestBuildArgv_JXLOutput(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatJPEGXL,
		processing.Size{Width: 800, Height: 600}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "JXL:-") {
		t.Errorf("missing JXL output coder, got: %s", joined)
	}
}

func TestBuildArgv_RejectsInvalidOperation(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Operation = processing.Operation("evil")
	if _, err := buildArgv(plan, nil, Limits{}); err == nil {
		t.Fatal("expected error for invalid operation")
	}
}

func TestBuildArgv_RejectsUnsafeOutputFormat(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.OutputFormat = processing.Format("msl")
	if _, err := buildArgv(plan, nil, Limits{}); err == nil {
		t.Fatal("expected error for unsafe output format")
	}
}

func TestBuildArgv_RejectsUnsafeSourceFormat(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.SourceFormat = processing.Format("https")
	if _, err := buildArgv(plan, nil, Limits{}); err == nil {
		t.Fatal("expected error for unsafe source format")
	}
}

func TestBuildArgv_NoUserStringsInArgv(t *testing.T) {
	// План с экстремальными значениями не должен протаскивать строки в argv.
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	for _, a := range args {
		if strings.ContainsAny(a, ";&|`$<>") {
			t.Errorf("unsafe character in argv element %q", a)
		}
	}
}

func TestBuildArgv_CapabilityValidation(t *testing.T) {
	caps := &Capabilities{
		Binary:    "magick",
		Version:   "7.1.1",
		Major:     7,
		Formats:   []string{"jpeg", "png"},
		formatSet: map[string]struct{}{"jpeg": {}, "png": {}},
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := buildArgv(plan, caps, Limits{}); err != nil {
		t.Fatalf("buildArgv with supported caps: %v", err)
	}

	// Формат, не поддерживаемый binary.
	plan.OutputFormat = processing.FormatWebP
	if _, err := buildArgv(plan, caps, Limits{}); err == nil {
		t.Fatal("expected error for unsupported format by binary")
	}
}

func TestBuildArgv_LimitArgs(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	limits := Limits{
		MemoryBytes: 1 << 30,
		MapBytes:    2 << 30,
		DiskBytes:   1 << 30,
		Threads:     4,
		TimeSeconds: 30,
		Width:       1000000,
		Height:      1000000,
		Pixels:      1000000,
		Frames:      10,
	}
	args, err := buildArgv(plan, nil, limits)
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-limit memory 1073741824",
		"-limit map 2147483648",
		"-limit disk 1073741824",
		"-limit threads 4",
		"-limit time 30",
		"-limit width 1000",
		"-limit height 1000",
		"-limit area 1000000",
		"-limit list-length 10",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing limit arg %q, got: %s", want, joined)
		}
	}
}

func TestBuildArgv_CropUsesExtent(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 400, Height: 300}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-extent 400x300") {
		t.Errorf("crop should use -extent, got: %s", joined)
	}
	// Явный фон для extent.
	if !strings.Contains(joined, "-background none") {
		t.Errorf("crop PNG should set -background none, got: %s", joined)
	}
}

func TestBuildArgv_CropTrimOrder(t *testing.T) {
	// Trim — независимый фильтр: plan.Trim=true + OpCrop. Порядок операций:
	// сначала trim, затем crop. В ImageMagick операции применяются слева
	// направо, поэтому -trim должен предшествовать -thumbnail/-extent.
	plan, err := processing.NewProcessingPlan(
		processing.OpCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 400, Height: 300}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Trim = true
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-trim") {
		t.Fatalf("crop+trim should include -trim, got: %s", joined)
	}
	if !strings.Contains(joined, "-thumbnail 400x300^") {
		t.Fatalf("crop+trim should include crop thumbnail, got: %s", joined)
	}
	// -trim должен идти ДО -thumbnail (trim выполняется первым).
	if strings.Index(joined, "-trim") > strings.Index(joined, "-thumbnail") {
		t.Errorf("-trim must come before -thumbnail (trim then crop), got: %s", joined)
	}
}

func TestBuildArgv_FramesLimit(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 10, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-limit list-length 10") {
		t.Errorf("frames limit should add -limit list-length 10, got: %s", joined)
	}
}

func TestBuildArgv_DurationLimit(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 5000,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	// 5000 мс → ceil(5000/1000) = 5 секунд.
	if !strings.Contains(joined, "-limit time 5") {
		t.Errorf("duration limit should add -limit time 5, got: %s", joined)
	}
}

func TestBuildArgv_AutoOrientDisabled(t *testing.T) {
	// AutoOrient=false → -auto-orient не добавляется.
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 800, Height: 600}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Orientation = &processing.OrientationSpec{AutoOrient: false}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-auto-orient") {
		t.Errorf("-auto-orient should be omitted when AutoOrient=false, got: %s", joined)
	}
}

func TestBuildArgv_RotateFlipOrder(t *testing.T) {
	// rotate/flip вставляются ДО -trim/-thumbnail. Trim — независимый фильтр
	// (plan.Trim=true + OpCrop).
	plan, err := processing.NewProcessingPlan(
		processing.OpCrop, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 400, Height: 300}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Trim = true
	plan.Orientation = &processing.OrientationSpec{
		AutoOrient: true,
		Rotate:     processing.Rotation90,
		Flip:       processing.FlipHorizontal,
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-rotate 90") {
		t.Errorf("missing -rotate 90, got: %s", joined)
	}
	if !strings.Contains(joined, "-flop") {
		t.Errorf("missing -flop, got: %s", joined)
	}
	// rotate/flip должны идти ДО -trim и -thumbnail.
	if strings.Index(joined, "-rotate") > strings.Index(joined, "-trim") {
		t.Errorf("-rotate must come before -trim, got: %s", joined)
	}
	if strings.Index(joined, "-flop") > strings.Index(joined, "-thumbnail") {
		t.Errorf("-flop must come before -thumbnail, got: %s", joined)
	}
}

func TestBuildArgv_FlipVertical(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 800, Height: 600}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Orientation = &processing.OrientationSpec{
		AutoOrient: true,
		Flip:       processing.FlipVertical,
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-flip") {
		t.Errorf("missing -flip, got: %s", joined)
	}
}

func TestBuildArgv_JpegSizeSwap90(t *testing.T) {
	// При повороте 90/270 стороны в jpeg:size свапаются.
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 400, Height: 300}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Orientation = &processing.OrientationSpec{
		AutoOrient: true,
		Rotate:     processing.Rotation90,
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "jpeg:size=300x400") {
		t.Errorf("jpeg:size should swap to 300x400 for rotate 90, got: %s", joined)
	}
}

func TestBuildArgv_JpegSizeNoSwap(t *testing.T) {
	// Без поворота 90/270 стороны не свапаются.
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatPNG,
		processing.Size{Width: 400, Height: 300}, 1, 2, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.Orientation = &processing.OrientationSpec{
		AutoOrient: true,
		Rotate:     processing.Rotation180,
	}
	args, err := buildArgv(plan, nil, Limits{})
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "jpeg:size=400x300") {
		t.Errorf("jpeg:size should stay 400x300 for rotate 180, got: %s", joined)
	}
}

func TestResizeString(t *testing.T) {
	cases := []struct {
		w, h int
		crop bool
		want string
	}{
		{800, 600, false, "800x600"},
		{800, 0, false, "800x"},
		{0, 600, false, "x600"},
		{800, 600, true, "800x600^"},
	}
	for _, c := range cases {
		if got := resizeString(c.w, c.h, c.crop); got != c.want {
			t.Errorf("resizeString(%d,%d,%v) = %q, want %q", c.w, c.h, c.crop, got, c.want)
		}
	}
}
