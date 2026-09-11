package processing

import (
	"strings"
	"testing"
)

func TestParseRotation(t *testing.T) {
	cases := []struct {
		in   string
		want Rotation
		ok   bool
	}{
		{"", RotationNone, true},
		{"none", RotationNone, true},
		{"NONE", RotationNone, true},
		{"0", RotationNone, true},
		{"90", Rotation90, true},
		{"180", Rotation180, true},
		{"270", Rotation270, true},
		{" 90 ", Rotation90, true},
		{"45", RotationNone, false},
		{"360", RotationNone, false},
		{"abc", RotationNone, false},
	}
	for _, c := range cases {
		got, err := ParseRotation(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseRotation(%q) unexpected error: %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseRotation(%q) expected error, got %v", c.in, got)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("ParseRotation(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseFlip(t *testing.T) {
	cases := []struct {
		in   string
		want FlipMode
		ok   bool
	}{
		{"", FlipNone, true},
		{"none", FlipNone, true},
		{"horizontal", FlipHorizontal, true},
		{"HORIZONTAL", FlipHorizontal, true},
		{"vertical", FlipVertical, true},
		{"diagonal", FlipNone, false},
	}
	for _, c := range cases {
		got, err := ParseFlip(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseFlip(%q) unexpected error: %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseFlip(%q) expected error, got %v", c.in, got)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("ParseFlip(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNewOrientationSpec(t *testing.T) {
	spec, err := NewOrientationSpec(true, Rotation90, FlipHorizontal)
	if err != nil {
		t.Fatalf("NewOrientationSpec unexpected error: %v", err)
	}
	if !spec.AutoOrient || spec.Rotate != Rotation90 || spec.Flip != FlipHorizontal {
		t.Errorf("unexpected spec: %+v", spec)
	}

	if _, err := NewOrientationSpec(true, Rotation(45), FlipNone); err == nil {
		t.Error("expected error for invalid rotation 45")
	}
	if _, err := NewOrientationSpec(true, RotationNone, FlipMode("diagonal")); err == nil {
		t.Error("expected error for invalid flip")
	}
}

func TestOrientationSpecIsZero(t *testing.T) {
	if !(*OrientationSpec)(nil).IsZero() {
		t.Error("nil spec should be zero")
	}
	zero, _ := NewOrientationSpec(false, RotationNone, FlipNone)
	if !zero.IsZero() {
		t.Error("spec with all disabled should be zero")
	}
	on, _ := NewOrientationSpec(true, RotationNone, FlipNone)
	if on.IsZero() {
		t.Error("spec with auto-orient should not be zero")
	}
	rot, _ := NewOrientationSpec(false, Rotation180, FlipNone)
	if rot.IsZero() {
		t.Error("spec with rotation should not be zero")
	}
}

func TestDefaultOrientation(t *testing.T) {
	d := DefaultOrientation()
	if d == nil || !d.AutoOrient || d.Rotate != RotationNone || d.Flip != FlipNone {
		t.Errorf("unexpected default orientation: %+v", d)
	}
}

func TestOrientationSpecString(t *testing.T) {
	spec, _ := NewOrientationSpec(true, Rotation90, FlipVertical)
	s := spec.String()
	if !strings.Contains(s, "auto-orient=true") || !strings.Contains(s, "rotate=90") || !strings.Contains(s, "flip=vertical") {
		t.Errorf("unexpected String(): %q", s)
	}
}

func TestOrientationSpecValidate(t *testing.T) {
	if err := (*OrientationSpec)(nil).Validate(); err != nil {
		t.Errorf("nil spec should validate: %v", err)
	}
	bad := &OrientationSpec{AutoOrient: true, Rotate: Rotation(45)}
	if err := bad.Validate(); err == nil {
		t.Error("expected error for invalid rotation")
	}
	badFlip := &OrientationSpec{AutoOrient: true, Flip: FlipMode("diagonal")}
	if err := badFlip.Validate(); err == nil {
		t.Error("expected error for invalid flip")
	}
}

func TestProcessingPlanOrientationValidation(t *testing.T) {
	plan, err := NewProcessingPlan(OpResize, FormatJPEG, FormatWebP, Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	// nil Orientation валиден (поведение по умолчанию).
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan with nil orientation should validate: %v", err)
	}
	// Валидная спецификация.
	plan.Orientation, _ = NewOrientationSpec(true, Rotation270, FlipHorizontal)
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan with valid orientation should validate: %v", err)
	}
	// Невалидная спецификация.
	plan.Orientation = &OrientationSpec{AutoOrient: true, Rotate: Rotation(45)}
	if err := plan.Validate(); err == nil {
		t.Error("expected error for invalid orientation in plan")
	}
}
