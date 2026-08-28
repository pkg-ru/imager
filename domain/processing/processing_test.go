package processing

import "testing"

func TestValidOperation(t *testing.T) {
	// Полный список допустимых операций зафиксирован явно. Trim — НЕ
	// операция enum: это независимый булев фильтр (ProcessingPlan.Trim),
	// поэтому комбинированных операций (crop-trim и т.п.) не существует.
	ops := []Operation{
		OpResize, OpCrop, OpSmartCrop, OpFaceCrop, OpObjectCrop,
	}
	for _, op := range ops {
		if !ValidOperation(op) {
			t.Errorf("ValidOperation(%q) = false, want true", op)
		}
	}
	if ValidOperation("bogus") {
		t.Error("ValidOperation(bogus) = true, want false")
	}
}

func TestValidFormat(t *testing.T) {
	// Полный список допустимых форматов зафиксирован явно.
	formats := []Format{FormatJPEG, FormatPNG, FormatWebP, FormatGIF, FormatAVIF, FormatHEIF, FormatAPNG, FormatJPEGXL}
	for _, f := range formats {
		if !ValidFormat(f) {
			t.Errorf("ValidFormat(%q) = false, want true", f)
		}
	}
	if ValidFormat("bogus") {
		t.Error("ValidFormat(bogus) = true, want false")
	}
}

func TestParseFormat(t *testing.T) {
	tests := []struct {
		in   string
		want Format
	}{
		{"jpeg", FormatJPEG},
		{"JPEG", FormatJPEG},
		{"jpg", FormatJPEG}, // алиас расширения
		{"png", FormatPNG},
		{"webp", FormatWebP},
		{"gif", FormatGIF},
		{"avif", FormatAVIF},
		{"heif", FormatHEIF},
		{"heic", FormatHEIF}, // алиас расширения
		{"apng", FormatAPNG},
		{"jxl", FormatJPEGXL},
		{"jpegxl", FormatJPEGXL}, // алиас расширения
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseFormat(tt.in)
			if err != nil {
				t.Fatalf("ParseFormat(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseFormat(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	if _, err := ParseFormat("bogus"); err == nil {
		t.Error("ParseFormat(bogus) expected error")
	}
}

func TestFormatAnimated(t *testing.T) {
	animated := []Format{FormatGIF, FormatWebP, FormatAPNG, FormatHEIF}
	still := []Format{FormatJPEG, FormatPNG, FormatAVIF, FormatJPEGXL}
	for _, f := range animated {
		if !f.Animated() {
			t.Errorf("%q should be animated", f)
		}
	}
	for _, f := range still {
		if f.Animated() {
			t.Errorf("%q should not be animated", f)
		}
	}
}

func TestNewProcessingPlan(t *testing.T) {
	loop := true
	plan, err := NewProcessingPlan(OpCrop, FormatJPEG, FormatWebP, Size{Width: 120, Height: 80}, 2, 80, &loop, 10, 5000)
	if err != nil {
		t.Fatalf("NewProcessingPlan error: %v", err)
	}
	if plan.Operation != OpCrop {
		t.Errorf("Operation = %q, want crop", plan.Operation)
	}
	if plan.SourceFormat != FormatJPEG || plan.OutputFormats != FormatWebP {
		t.Errorf("formats = %q/%q, want jpeg/webp", plan.SourceFormat, plan.OutputFormats)
	}
	if plan.Frames != 10 {
		t.Errorf("Frames = %d, want 10", plan.Frames)
	}
	if plan.Duration != 5000 {
		t.Errorf("Duration = %d, want 5000", plan.Duration)
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("Validate error: %v", err)
	}
}

func TestNewProcessingPlanInvalid(t *testing.T) {
	invalid := []struct {
		name     string
		op       Operation
		sf       Format
		of       Format
		size     Size
		dpr      int
		q        int
		frames   int
		duration int
	}{
		{"invalid op", "bogus", FormatJPEG, FormatWebP, Size{Width: 120, Height: 80}, 1, 80, 0, 0},
		{"invalid source format", OpCrop, "bogus", FormatWebP, Size{Width: 120, Height: 80}, 1, 80, 0, 0},
		{"invalid output format", OpCrop, FormatJPEG, "bogus", Size{Width: 120, Height: 80}, 1, 80, 0, 0},
		{"empty size", OpCrop, FormatJPEG, FormatWebP, Size{}, 1, 80, 0, 0},
		{"negative dpr", OpCrop, FormatJPEG, FormatWebP, Size{Width: 120, Height: 80}, -1, 80, 0, 0},
		{"quality too high", OpCrop, FormatJPEG, FormatWebP, Size{Width: 120, Height: 80}, 1, 101, 0, 0},
		{"quality too low", OpCrop, FormatJPEG, FormatWebP, Size{Width: 120, Height: 80}, 1, -1, 0, 0},
		{"negative frames", OpCrop, FormatJPEG, FormatWebP, Size{Width: 120, Height: 80}, 1, 80, -1, 0},
		{"negative duration", OpCrop, FormatJPEG, FormatWebP, Size{Width: 120, Height: 80}, 1, 80, 0, -1},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewProcessingPlan(tt.op, tt.sf, tt.of, tt.size, tt.dpr, tt.q, nil, tt.frames, tt.duration); err == nil {
				t.Errorf("NewProcessingPlan(%s) expected error", tt.name)
			}
		})
	}
}

func TestSizeValid(t *testing.T) {
	if err := (Size{Width: 120, Height: 80}).Valid(); err != nil {
		t.Errorf("expected valid size, got %v", err)
	}
	if err := (Size{Width: 120}).Valid(); err != nil {
		t.Errorf("expected valid width-only size, got %v", err)
	}
	if err := (Size{}).Valid(); err == nil {
		t.Error("expected error for empty size")
	}
	if err := (Size{Width: -1}).Valid(); err == nil {
		t.Error("expected error for negative width")
	}
}
