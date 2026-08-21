package policy

import (
	"testing"
)

func TestValidateConfig(t *testing.T) {
	valid := &Config{
		Global: GlobalConfig{Authorization: "safe", SizeRules: []string{"120x80"}},
		Presets: []PresetConfig{
			{Name: "thumb", Transform: "c", Size: "120x80", OutputFormat: "webp"},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config, got %v", err)
	}
}

func TestValidateConfigInvalid(t *testing.T) {
	invalid := []*Config{
		{Global: GlobalConfig{Authorization: "bogus"}},
		{Global: GlobalConfig{SizeRules: []string{"bogus"}}},
		{Global: GlobalConfig{Limits: Limits{Width: -1}}},
		{Buckets: []BucketConfig{{Bucket: ""}}},
		{Buckets: []BucketConfig{{Bucket: "a"}, {Bucket: "a"}}},
		{Presets: []PresetConfig{{Name: "", Transform: "c", Size: "120x80", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Transform: "bogus", Size: "120x80", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Transform: "crop", Size: "120x80", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Transform: "c", Size: "bogus", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Transform: "c", Size: "120x80", OutputFormat: ""}}},
	}
	for _, c := range invalid {
		if err := ValidateConfig(c); err == nil {
			t.Errorf("ValidateConfig(%+v) expected error", c)
		}
	}
}

func TestCompile(t *testing.T) {
	cfg := &Config{
		Global: GlobalConfig{
			Authorization:  "safe",
			SizeRules:      []string{"120x80"},
			AllowedPresets: []string{"thumb"},
			Limits:         Limits{Width: 1000, Height: 1000},
		},
		Buckets: []BucketConfig{
			{Bucket: "private", Authorization: "unsafe"},
		},
		Presets: []PresetConfig{
			{Name: "thumb", Transform: "c", Size: "120x80", OutputFormat: "webp"},
		},
	}
	compiled, err := Compile(cfg)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	if compiled.Policy == nil {
		t.Fatal("expected non-nil policy")
	}
	if compiled.Presets == nil {
		t.Fatal("expected non-nil presets")
	}
	if compiled.Policy.Global.Limits.Width != 1000 {
		t.Errorf("Global.Limits.Width = %d, want 1000", compiled.Policy.Global.Limits.Width)
	}
	if len(compiled.Policy.Buckets) != 1 {
		t.Errorf("Buckets = %d, want 1", len(compiled.Policy.Buckets))
	}
	if _, ok := compiled.Presets.Get("thumb"); !ok {
		t.Error("expected preset thumb")
	}
}

func TestCompileDuplicatePreset(t *testing.T) {
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Transform: "c", Size: "120x80", OutputFormat: "webp"},
			{Name: "thumb", Transform: "c", Size: "120x80", OutputFormat: "webp"},
		},
	}
	if _, err := Compile(cfg); err == nil {
		t.Error("expected duplicate preset error")
	}
}

func TestParseSizeRule(t *testing.T) {
	tests := []struct {
		in   string
		want SizeRule
	}{
		{"120x80", SizeRule{Width: &Range{120, 120}, Height: &Range{80, 80}}},
		{"x50", SizeRule{Height: &Range{50, 50}}},
		{"180x", SizeRule{Width: &Range{180, 180}}},
		{"120-300x", SizeRule{Width: &Range{120, 300}}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSizeRule(tt.in)
			if err != nil {
				t.Fatalf("ParseSizeRule(%q) error: %v", tt.in, err)
			}
			if got.Width == nil && tt.want.Width != nil {
				t.Errorf("width = nil, want %+v", *tt.want.Width)
			}
			if got.Width != nil && tt.want.Width != nil && *got.Width != *tt.want.Width {
				t.Errorf("width = %+v, want %+v", *got.Width, *tt.want.Width)
			}
			if got.Height == nil && tt.want.Height != nil {
				t.Errorf("height = nil, want %+v", *tt.want.Height)
			}
			if got.Height != nil && tt.want.Height != nil && *got.Height != *tt.want.Height {
				t.Errorf("height = %+v, want %+v", *got.Height, *tt.want.Height)
			}
		})
	}
}
