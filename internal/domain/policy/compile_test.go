package policy

import (
	"testing"

	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

func TestValidateConfig(t *testing.T) {
	valid := &Config{
		Global: GlobalConfig{Authorization: "safe", SizeRules: []string{"120x80"}},
		Presets: []PresetConfig{
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp"},
			{Name: "smart", Crop: "smart", Size: "120x80", OutputFormat: "webp"},
			{Name: "face", Crop: "face", Size: "120x80", OutputFormat: "webp"},
			{Name: "object", Crop: "object", Size: "120x80", OutputFormat: "webp"},
			// Пустой crop — валиден (кроп не используется).
			{Name: "resize", Size: "120x80", OutputFormat: "webp"},
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
		{Presets: []PresetConfig{{Name: "", Crop: "center", Size: "120x80", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "bogus", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: ""}}},
		// Недопустимые значения crop (пусто = валидно).
		{Presets: []PresetConfig{{Name: "a", Crop: "bogus", Size: "120x80", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "true", Size: "120x80", OutputFormat: "webp"}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "Center", Size: "120x80", OutputFormat: "webp"}}},
		// dpr вне [1,3] (0 = не задан, валиден).
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", DPR: 4}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", DPR: -1}}},
		// quality вне [0,100].
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", Quality: -1}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", Quality: 101}}},
		// frames/duration отрицательные.
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", Frames: -1}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", Duration: -1}}},
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
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp", DPR: 2, Quality: 80, Frames: 10, Duration: 5000, Loop: &loop},
			{Name: "trim", Trim: true, Size: "x50", OutputFormat: "png"},
			{Name: "both", Crop: "center", Trim: true, Size: "800x200", OutputFormat: "webp"},
			{Name: "resize", Size: "120x80", OutputFormat: "webp"},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config with new fields, got %v", err)
	}
}

func TestValidateConfigValidPathPolicies(t *testing.T) {
	trimFalse := false
	valid := &Config{
		PathPolicies: []PathPolicyConfig{
			{Path: "/", DPR: "0-1", Crop: strCropRule("none")},
			{Path: "/users", DPR: "2-3", Crop: strCropRule("smart"), Trim: &trimFalse},
			{Path: "/users/gift", DPR: "0-1"},
			{Path: "/basket/users", DPR: "2-3"},
			// Без ведущего "/" и с завершающим "/" — нормализуются.
			{Path: "basket/products", DPR: "0-1", Crop: listCropRule("center", "face"), Trim: &trimFalse},
			{Path: "/basket/users/extra/", DPR: "2-3"},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config with path-policies, got %v", err)
	}
}

func TestValidateConfigInvalidPathPolicyCrop(t *testing.T) {
	invalid := []*Config{
		// Неизвестный режим в строковой форме.
		{PathPolicies: []PathPolicyConfig{{Path: "/", Crop: strCropRule("bogus")}}},
		{PathPolicies: []PathPolicyConfig{{Path: "/", Crop: strCropRule("Center")}}},
		{PathPolicies: []PathPolicyConfig{{Path: "/", Crop: strCropRule("true")}}},
		// Неизвестный режим в списке.
		{PathPolicies: []PathPolicyConfig{{Path: "/", Crop: listCropRule("smart", "bogus")}}},
		// Пустой список.
		{PathPolicies: []PathPolicyConfig{{Path: "/", Crop: listCropRule()}}},
	}
	for _, c := range invalid {
		if err := ValidateConfig(c); err == nil {
			t.Errorf("ValidateConfig(%+v) expected error", c)
		}
	}
}

// strCropRule / listCropRule — хелперы построения CropRuleConfig для тестов
// (имитируют результат UnmarshalYAML: crop — только строковые формы).
func strCropRule(s string) *CropRuleConfig {
	c := CropRuleConfig{s}
	return &c
}

func listCropRule(modes ...string) *CropRuleConfig {
	// Явно ненулевой слайс: пустой список должен считаться заданным
	// (и невалидным), а не «поле отсутствует».
	c := CropRuleConfig{}
	c = append(c, modes...)
	return &c
}

func TestCompileCropRule(t *testing.T) {
	tests := []struct {
		name    string
		cfg     CropRuleConfig
		check   func(*testing.T, *CropRule)
		wantErr bool
	}{
		{
			name: "nil = no restriction",
			cfg:  nil,
			check: func(t *testing.T, r *CropRule) {
				if r != nil {
					t.Errorf("rule = %+v, want nil", r)
				}
			},
		},
		{
			name: "center allows c/ct",
			cfg:  []string{"center"},
			check: func(t *testing.T, r *CropRule) {
				if !r.Allows(asset.TransformCrop) || !r.Allows(asset.TransformCropTrim) {
					t.Error("c/ct must be allowed")
				}
				if r.Allows("") || r.Allows(asset.TransformSmartCrop) || r.Allows(asset.TransformTrim) {
					t.Error("non-crop transforms must be denied")
				}
			},
		},
		{
			name: "none denies all crop modes",
			cfg:  []string{"none"},
			check: func(t *testing.T, r *CropRule) {
				for _, tr := range []asset.Transform{
					asset.TransformCrop, asset.TransformCropTrim,
					asset.TransformSmartCrop, asset.TransformSmartCropTrim,
					asset.TransformFaceCrop, asset.TransformFaceCropTrim,
					asset.TransformObjectCrop, asset.TransformObjectCropTrim,
				} {
					if r.Allows(tr) {
						t.Errorf("transform %q must be denied", tr)
					}
				}
				if !r.Allows("") || !r.Allows(asset.TransformTrim) {
					t.Error("resize and trim-only must stay allowed")
				}
			},
		},
		{
			name: "smart allows sc/sct",
			cfg:  []string{"smart"},
			check: func(t *testing.T, r *CropRule) {
				if !r.Allows(asset.TransformSmartCrop) || !r.Allows(asset.TransformSmartCropTrim) {
					t.Error("sc/sct must be allowed")
				}
				if r.Allows(asset.TransformCrop) || r.Allows(asset.TransformFaceCrop) {
					t.Error("c/fc must be denied")
				}
			},
		},
		{
			name: "list [smart, face]",
			cfg:  []string{"smart", "face"},
			check: func(t *testing.T, r *CropRule) {
				if !r.Allows(asset.TransformSmartCrop) || !r.Allows(asset.TransformSmartCropTrim) ||
					!r.Allows(asset.TransformFaceCrop) || !r.Allows(asset.TransformFaceCropTrim) {
					t.Error("sc/sct/fc/fct must be allowed")
				}
				if r.Allows(asset.TransformCrop) || r.Allows(asset.TransformObjectCrop) {
					t.Error("c/oc must be denied")
				}
			},
		},
		{
			name:    "unknown mode",
			cfg:     []string{"bogus"},
			wantErr: true,
		},
		{
			name:    "empty list",
			cfg:     []string{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, err := compileCropRule(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("compileCropRule(%v) expected error", tt.cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("compileCropRule(%v) error: %v", tt.cfg, err)
			}
			tt.check(t, rule)
		})
	}
}

func TestCompilePathPolicyCropModes(t *testing.T) {
	cfg := &Config{
		PathPolicies: []PathPolicyConfig{
			{Path: "/avatars", Crop: strCropRule("center")},
			{Path: "/news", Crop: strCropRule("smart")},
			{Path: "/portraits", Crop: strCropRule("face"), Trim: nil},
			{Path: "/catalog", Crop: listCropRule("object")},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	byPath := map[string]*PathPolicy{}
	for i := range compiled.Policy.PathPolicies {
		pp := &compiled.Policy.PathPolicies[i]
		byPath[pp.Path] = pp
	}
	cases := []struct {
		path      string
		transform asset.Transform
		allowed   bool
	}{
		{"/avatars", asset.TransformCrop, true},
		{"/avatars", asset.TransformCropTrim, true},
		{"/avatars", asset.TransformSmartCrop, false},
		{"/news", asset.TransformSmartCrop, true},
		{"/news", asset.TransformSmartCropTrim, true},
		{"/news", asset.TransformCrop, false},
		{"/portraits", asset.TransformFaceCrop, true},
		{"/portraits", asset.TransformFaceCropTrim, true},
		{"/portraits", asset.TransformObjectCrop, false},
		{"/catalog", asset.TransformObjectCrop, true},
		{"/catalog", asset.TransformObjectCropTrim, true},
		{"/catalog", asset.TransformCrop, false},
	}
	for _, tc := range cases {
		pp := byPath[tc.path]
		if pp == nil {
			t.Fatalf("path policy %q not found", tc.path)
		}
		if got := pp.Crop.Allows(tc.transform); got != tc.allowed {
			t.Errorf("%s: Allows(%q) = %v, want %v", tc.path, tc.transform, got, tc.allowed)
		}
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
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp"},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
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
	cfg := &Config{
		PathPolicies: []PathPolicyConfig{
			{Path: "/", DPR: "0-1", Crop: strCropRule("none")},
			{Path: "basket/products", DPR: "2-3"},
			{Path: "/basket/users/", DPR: "0-1"},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
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
			if pp.Crop == nil || pp.Crop.Allows(asset.TransformCrop) {
				t.Errorf("path / crop must deny TransformCrop")
			}
			if !pp.Crop.Allows("") {
				t.Errorf("path / crop must allow resize")
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
			{Name: "crop", Crop: "center", Size: "120x80", OutputFormat: "webp"},
			{Name: "trim", Trim: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "both", Crop: "center", Trim: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "resize", Size: "120x80", OutputFormat: "webp"},
			{Name: "smart", Crop: "smart", Size: "120x80", OutputFormat: "webp"},
			{Name: "face", Crop: "face", Size: "120x80", OutputFormat: "webp"},
			{Name: "object", Crop: "object", Size: "120x80", OutputFormat: "webp"},
			{Name: "smart-trim", Crop: "smart", Trim: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "face-trim", Crop: "face", Trim: true, Size: "120x80", OutputFormat: "webp"},
			{Name: "object-trim", Crop: "object", Trim: true, Size: "120x80", OutputFormat: "webp"},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
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
		{"smart", "sc"},
		{"face", "fc"},
		{"object", "oc"},
		{"smart-trim", "sct"},
		{"face-trim", "fct"},
		{"object-trim", "oct"},
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

func TestTransformFromCropTrim(t *testing.T) {
	tests := []struct {
		crop string
		trim bool
		want string
	}{
		{"", false, ""},
		{"", true, "t"},
		{"center", false, "c"},
		{"center", true, "ct"},
		{"smart", false, "sc"},
		{"smart", true, "sct"},
		{"face", false, "fc"},
		{"face", true, "fct"},
		{"object", false, "oc"},
		{"object", true, "oct"},
	}
	for _, tt := range tests {
		got := string(transformFromCropTrim(tt.crop, tt.trim))
		if got != tt.want {
			t.Errorf("transformFromCropTrim(%q, %v) = %q, want %q", tt.crop, tt.trim, got, tt.want)
		}
	}
}

func TestCompilePresetOptions(t *testing.T) {
	loop := true
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb@2", Crop: "center", Size: "240x160", OutputFormat: "webp", DPR: 2, Quality: 80, Frames: 10, Duration: 5000, Loop: &loop},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
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
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp"},
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp"},
		},
	}
	if _, err := Compile(cfg, nil, nil); err == nil {
		t.Error("expected duplicate preset error")
	}
}

func TestCompileDuplicatePresetWithDPRSuffix(t *testing.T) {
	// "thumb" и "thumb@2" — разные имена (не дубликаты).
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp"},
			{Name: "thumb@2", Crop: "center", Size: "240x160", OutputFormat: "webp"},
		},
	}
	if _, err := Compile(cfg, nil, nil); err != nil {
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

func TestCompilePresetOrientationInheritsGlobal(t *testing.T) {
	// Пресет без явных ориентационных полей наследует глобальный дефолт.
	def := &processing.OrientationSpec{AutoOrient: true, Rotate: processing.Rotation90, Flip: processing.FlipHorizontal}
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp"},
		},
	}
	compiled, err := Compile(cfg, nil, def)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	p, ok := compiled.Presets.Get("thumb")
	if !ok {
		t.Fatal("expected preset thumb")
	}
	or := p.Orientation()
	if or == nil {
		t.Fatal("expected orientation spec")
	}
	if !or.AutoOrient || or.Rotate != processing.Rotation90 || or.Flip != processing.FlipHorizontal {
		t.Errorf("orientation = %s, want auto-orient + rotate 90 + flip horizontal", or.String())
	}
}

func TestCompilePresetOrientationOverridesGlobal(t *testing.T) {
	def := &processing.OrientationSpec{AutoOrient: true, Rotate: processing.Rotation90, Flip: processing.FlipHorizontal}
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp", AutoOrient: boolPtr(false), Rotate: "270", Flip: "vertical"},
		},
	}
	compiled, err := Compile(cfg, nil, def)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	p, ok := compiled.Presets.Get("thumb")
	if !ok {
		t.Fatal("expected preset thumb")
	}
	or := p.Orientation()
	if or == nil {
		t.Fatal("expected orientation spec")
	}
	if or.AutoOrient || or.Rotate != processing.Rotation270 || or.Flip != processing.FlipVertical {
		t.Errorf("orientation = %s, want auto-orient off, rotate 270, flip vertical", or.String())
	}
}

func TestCompilePresetOrientationNoneDisables(t *testing.T) {
	// "none" в пресете ЯВНО отключает унаследованный глобальный поворот/отражение.
	def := &processing.OrientationSpec{AutoOrient: true, Rotate: processing.Rotation90, Flip: processing.FlipHorizontal}
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp", Rotate: "none", Flip: "none"},
		},
	}
	compiled, err := Compile(cfg, nil, def)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	p, ok := compiled.Presets.Get("thumb")
	if !ok {
		t.Fatal("expected preset thumb")
	}
	or := p.Orientation()
	if or == nil {
		t.Fatal("expected orientation spec")
	}
	if !or.AutoOrient || or.Rotate != processing.RotationNone || or.Flip != processing.FlipNone {
		t.Errorf("orientation = %s, want auto-orient on, no rotate, no flip", or.String())
	}
}

func TestCompilePresetOrientationNilDefault(t *testing.T) {
	// nil defaultOrientation эквивалентен {AutoOrient: true}.
	cfg := &Config{
		Presets: []PresetConfig{
			{Name: "thumb", Crop: "center", Size: "120x80", OutputFormat: "webp"},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	p, ok := compiled.Presets.Get("thumb")
	if !ok {
		t.Fatal("expected preset thumb")
	}
	or := p.Orientation()
	if or == nil {
		t.Fatal("expected orientation spec")
	}
	if !or.AutoOrient || or.Rotate != processing.RotationNone || or.Flip != processing.FlipNone {
		t.Errorf("orientation = %s, want auto-orient on, no rotate, no flip", or.String())
	}
}

func TestValidateConfigInvalidPresetOrientation(t *testing.T) {
	invalid := []*Config{
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", Rotate: "45"}}},
		{Presets: []PresetConfig{{Name: "a", Crop: "center", Size: "120x80", OutputFormat: "webp", Flip: "diagonal"}}},
	}
	for _, c := range invalid {
		if err := ValidateConfig(c); err == nil {
			t.Errorf("ValidateConfig(%+v) expected error", c)
		}
	}
}

// boolPtr — хелпер построения *bool для тестов.
func boolPtr(b bool) *bool {
	v := b
	return &v
}
