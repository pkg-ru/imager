package policy

import (
	"testing"
)

func TestValidateConfig(t *testing.T) {
	valid := &Config{
		Global: GlobalConfig{Authorization: "safe", SizeRules: []string{"120x80"}},
		Presets: []PresetConfig{
			{Name: "thumb", Crop: true, Size: "120x80", OutputFormat: "webp"},
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
		{PathPolicies: []PathPolicyConfig{{Path: ""}}},
		{PathPolicies: []PathPolicyConfig{{Path: "a"}, {Path: "a"}}},
		// Дубликаты после нормализации: "a" и "/a/" — один и тот же путь.
		{PathPolicies: []PathPolicyConfig{{Path: "a"}, {Path: "/a/"}}},
		// dpr: невалидные диапазоны.
		{PathPolicies: []PathPolicyConfig{{Path: "/", DPR: "bogus"}}},
		{PathPolicies: []PathPolicyConfig{{Path: "/", DPR: "3-1"}}},
		{PathPolicies: []PathPolicyConfig{{Path: "/", DPR: "-1-2"}}},
		{PathPolicies: []PathPolicyConfig{{Path: "/", DPR: "0-4"}}},
		{Presets: []PresetConfig{{Name: "", Crop: true, Size: "120x80", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "bogus", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "120x80", OutputFormat: ""}}},
		// dpr вне [1,3] (0 = не задан, валиден).
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "120x80", OutputFormat: "webp", DPR: 4}}},
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "120x80", OutputFormat: "webp", DPR: -1}}},
		// quality вне [0,100].
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "120x80", OutputFormat: "webp", Quality: -1}}},
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "120x80", OutputFormat: "webp", Quality: 101}}},
		// frames/duration отрицательные.
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "120x80", OutputFormat: "webp", Frames: -1}}},
		{Presets: []PresetConfig{{Name: "a", Crop: true, Size: "120x80", OutputFormat: "webp", Duration: -1}}},
	}
	for _, c := range invalid {
		if err := ValidateConfig(c); err == nil {
			t.Errorf("ValidateConfig(%+v) expected error", c)
		}
	}
}

func TestValidateConfigValidNewFields(t *testing.T) {
	loop := true
	valid := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: true, Size: "120x80", OutputFormat: "webp", DPR: 2, Quality: 80, Frames: 10, Duration: 5000, Loop: &loop},
			{Name: "trim", Trim: true, Size: "x50", OutputFormat: "png"},
			{Name: "both", Crop: true, Trim: true, Size: "800x200", OutputFormat: "webp"},
			{Name: "resize", Size: "120x80", OutputFormat: "webp"},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config with new fields, got %v", err)
	}
}

func TestValidateConfigValidPathPolicies(t *testing.T) {
	cropFalse := false
	trimFalse := false
	valid := &Config{
		PathPolicies: []PathPolicyConfig{
			{Path: "/", DPR: "0-1", Crop: &cropFalse},
			{Path: "/users", DPR: "2-3", Crop: &cropFalse, Trim: &trimFalse},
			{Path: "/users/gift", DPR: "0-1"},
			{Path: "/basket/users", DPR: "2-3"},
			// Без ведущего "/" и с завершающим "/" — нормализуются.
			{Path: "basket/products", DPR: "0-1", Crop: &cropFalse, Trim: &trimFalse},
			{Path: "/basket/users/extra/", DPR: "2-3"},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config with path-policies, got %v", err)
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/", "/"},
		{"/users", "/users"},
		{"/basket/users/", "/basket/users"},
		{"basket/products", "/basket/products"},
		{"/users/gift", "/users/gift"},
		{"users", "/users"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizePath(tt.in); got != tt.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseDPRRange(t *testing.T) {
	tests := []struct {
		in   string
		want Range
	}{
		{"0-1", Range{Min: 0, Max: 1}},
		{"2-3", Range{Min: 2, Max: 3}},
		{"2", Range{Min: 2, Max: 2}},
		{"0-3", Range{Min: 0, Max: 3}},
	}
	for _, tt := range tests {
		got, err := ParseDPRRange(tt.in)
		if err != nil {
			t.Fatalf("ParseDPRRange(%q) error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("ParseDPRRange(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}

	invalid := []string{"", "bogus", "3-1", "-1-2", "1-2-3"}
	for _, s := range invalid {
		if _, err := ParseDPRRange(s); err == nil {
			t.Errorf("ParseDPRRange(%q) expected error", s)
		}
	}
}

func TestValidateDPRRange(t *testing.T) {
	valid := []Range{
		{Min: 0, Max: 1},
		{Min: 2, Max: 3},
		{Min: 0, Max: 3},
		{Min: 3, Max: 3},
	}
	for _, r := range valid {
		if err := validateDPRRange(r); err != nil {
			t.Errorf("validateDPRRange(%+v) unexpected error: %v", r, err)
		}
	}

	invalid := []Range{
		{Min: -1, Max: 1},
		{Min: 3, Max: 2},
		{Min: 0, Max: 4},
		{Min: 4, Max: 4},
	}
	for _, r := range invalid {
		if err := validateDPRRange(r); err == nil {
			t.Errorf("validateDPRRange(%+v) expected error", r)
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
		PathPolicies: []PathPolicyConfig{
			{Path: "/private", DPR: "0-1"},
		},
		Presets: []PresetConfig{
			{Name: "thumb", Crop: true, Size: "120x80", OutputFormat: "webp"},
		},
	}
	compiled, err := Compile(cfg, nil)
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
	if len(compiled.Policy.PathPolicies) != 1 {
		t.Errorf("PathPolicies = %d, want 1", len(compiled.Policy.PathPolicies))
	}
	if _, ok := compiled.Presets.Get("thumb"); !ok {
		t.Error("expected preset thumb")
	}
}

func TestCompilePathPolicyNormalization(t *testing.T) {
	cropFalse := false
	cfg := &Config{
		PathPolicies: []PathPolicyConfig{
			{Path: "/", DPR: "0-1", Crop: &cropFalse},
			{Path: "basket/products", DPR: "2-3"},
			{Path: "/basket/users/", DPR: "0-1"},
		},
	}
	compiled, err := Compile(cfg, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	got := compiled.Policy.PathNames()
	want := []string{"/", "/basket/products", "/basket/users"}
	if len(got) != len(want) {
		t.Fatalf("PathNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PathNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Проверяем скомпилированные поля.
	for _, pp := range compiled.Policy.PathPolicies {
		switch pp.Path {
		case "/":
			if pp.DPR == nil || *pp.DPR != (Range{Min: 0, Max: 1}) {
				t.Errorf("path / dpr = %+v, want 0-1", pp.DPR)
			}
			if pp.Crop == nil || *pp.Crop != false {
				t.Errorf("path / crop = %v, want false", pp.Crop)
			}
		case "/basket/products":
			if pp.DPR == nil || *pp.DPR != (Range{Min: 2, Max: 3}) {
				t.Errorf("path /basket/products dpr = %+v, want 2-3", pp.DPR)
			}
		case "/basket/users":
			if pp.DPR == nil || *pp.DPR != (Range{Min: 0, Max: 1}) {
				t.Errorf("path /basket/users dpr = %+v, want 0-1", pp.DPR)
			}
		}
	}
}

func TestCompileCropTrimMapping(t *testing.T) {
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "crop", Crop: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "trim", Trim: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "both", Crop: true, Trim: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "resize", Size: "120x80", OutputFormat: "webp"},
		},
	}
	compiled, err := Compile(cfg, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	tests := []struct {
		name string
		want string
	}{
		{"crop", "c"},
		{"trim", "t"},
		{"both", "ct"},
		{"resize", ""},
	}
	for _, tt := range tests {
		p, ok := compiled.Presets.Get(tt.name)
		if !ok {
			t.Fatalf("preset %q not found", tt.name)
		}
		if got := string(p.Transform()); got != tt.want {
			t.Errorf("preset %q transform = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCompilePresetOptions(t *testing.T) {
	loop := true
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb@2", Crop: true, Size: "240x160", OutputFormat: "webp", DPR: 2, Quality: 80, Frames: 10, Duration: 5000, Loop: &loop},
		},
	}
	compiled, err := Compile(cfg, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	p, ok := compiled.Presets.Get("thumb@2")
	if !ok {
		t.Fatal("expected preset thumb@2")
	}
	if p.DPR().Int() != 2 {
		t.Errorf("DPR = %d, want 2", p.DPR().Int())
	}
	if p.Quality() != 80 {
		t.Errorf("Quality = %d, want 80", p.Quality())
	}
	if p.Frames() != 10 {
		t.Errorf("Frames = %d, want 10", p.Frames())
	}
	if p.Duration() != 5000 {
		t.Errorf("Duration = %d, want 5000", p.Duration())
	}
	if p.Loop() == nil || !*p.Loop() {
		t.Errorf("Loop = %v, want true", p.Loop())
	}
}

func TestCompileDuplicatePreset(t *testing.T) {
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "thumb", Crop: true, Size: "120x80", OutputFormat: "webp"},
		},
	}
	if _, err := Compile(cfg, nil); err == nil {
		t.Error("expected duplicate preset error")
	}
}

func TestCompileDuplicatePresetWithDPRSuffix(t *testing.T) {
	// "thumb" и "thumb@2" — разные имена (не дубликаты).
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "thumb@2", Crop: true, Size: "240x160", OutputFormat: "webp"},
		},
	}
	if _, err := Compile(cfg, nil); err != nil {
		t.Errorf("expected valid config with distinct names, got %v", err)
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
