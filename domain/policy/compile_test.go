package policy

import (
	"testing"

	"github.com/pkg-ru/dynamic"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/processing"
)

// presetCfg — хелпер построения PresetConfig.
func presetCfg(name string, width, height uint32, outFmts ...string) PresetConfig {
	formats := make(dynamic.StringSlice, 0, len(outFmts))
	for _, f := range outFmts {
		formats = append(formats, dynamic.String(f))
	}
	return PresetConfig{
		Name:          dynamic.String(name),
		Width:         dynamic.Uint32(width),
		Height:        dynamic.Uint32(height),
		OutputFormats: formats,
	}
}

func TestValidateConfig(t *testing.T) {
	valid := &Config{
		Presets: []PresetConfig{
			presetCfg("thumb", 120, 80, "webp"),
			presetCfg("smart", 120, 80, "webp"),
			presetCfg("face", 120, 80, "webp"),
			presetCfg("object", 120, 80, "webp"),
			// Пустой crop — валиден (кроп не используется).
			presetCfg("resize", 120, 80, "webp"),
		},
		PathPolicies: map[string]PathPolicyConfig{
			"/": {
				Presets: dynamic.StringSlice{dynamic.String("thumb")},
				Customs: map[string]PresetConfig{
					"200x200": presetCfg("", 0, 0, "webp"),
					"x":       presetCfg("", 0, 0, "webp"),
				},
			},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config, got %v", err)
	}
}

func TestValidateConfigInvalid(t *testing.T) {
	invalid := []*Config{
		// Пути.
		{PathPolicies: map[string]PathPolicyConfig{"": {}}},
		{PathPolicies: map[string]PathPolicyConfig{"a": {}, "/a/": {}}},
		// Пресеты.
		{Presets: []PresetConfig{{Name: dynamic.String(""), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: []PresetConfig{{Name: dynamic.String("a"), OutputFormats: dynamic.StringSlice{dynamic.String("")}}}},
		{Presets: []PresetConfig{{Name: dynamic.String("a"), OutputFormats: dynamic.StringSlice{}}}},
		// Недопустимые значения crop.
		{Presets: []PresetConfig{{Name: dynamic.String("a"), Crop: dynamic.String("bogus"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: []PresetConfig{{Name: dynamic.String("a"), Crop: dynamic.String("true"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: []PresetConfig{{Name: dynamic.String("a"), Crop: dynamic.String("Center"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		// dpr вне [0,3].
		{Presets: []PresetConfig{{Name: dynamic.String("a"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, DPR: dynamic.NewNullable(dynamic.Uint32(4))}}},
		// quality вне [0,100].
		{Presets: []PresetConfig{{Name: dynamic.String("a"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, Quality: dynamic.Uint32(101)}}},
		// width/height > MaxDimension.
		{Presets: []PresetConfig{{Name: dynamic.String("a"), Width: dynamic.Uint32(asset.MaxDimension + 1), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		// Имя пресета с @0/@1.
		{Presets: []PresetConfig{{Name: dynamic.String("a@0"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: []PresetConfig{{Name: dynamic.String("a@1"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		// Конфликт dpr в имени vs настройки.
		{Presets: []PresetConfig{{Name: dynamic.String("a@2"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, DPR: dynamic.NewNullable(dynamic.Uint32(3))}}},
		// Custom: невалидное имя.
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"bogus": presetCfg("", 0, 0, "webp")}}}},
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200@0": presetCfg("", 0, 0, "webp")}}}},
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200@1": presetCfg("", 0, 0, "webp")}}}},
		// Custom: конфликт dpr в имени vs настройки.
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200@2": {DPR: dynamic.NewNullable(dynamic.Uint32(3)), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}}}},
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
			{
				Name: dynamic.String("thumb"), Crop: dynamic.String("center"),
				Width: dynamic.Uint32(120), Height: dynamic.Uint32(80),
				OutputFormats: dynamic.StringSlice{dynamic.String("webp"), dynamic.String("avif")},
				DPR:           dynamic.NewNullable(dynamic.Uint32(2)),
				Quality:       dynamic.Uint32(80), Frames: dynamic.Uint32(10), Duration: dynamic.Uint32(5000),
				Loop: dynamic.NewNullable(dynamic.Bool(loop)),
			},
			{Name: dynamic.String("trim"), Trim: dynamic.Bool(true), Height: dynamic.Uint32(50), OutputFormats: dynamic.StringSlice{dynamic.String("png")}},
			{Name: dynamic.String("both"), Crop: dynamic.String("center"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(800), Height: dynamic.Uint32(200), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("resize"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config with new fields, got %v", err)
	}
}

func TestValidateConfigValidPathPolicies(t *testing.T) {
	valid := &Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/": {
				Presets: dynamic.StringSlice{dynamic.String("thumb")},
				Customs: map[string]PresetConfig{
					"200x200": presetCfg("", 0, 0, "webp"),
					"x":       presetCfg("", 0, 0, "webp"),
				},
			},
			"/users": {
				Presets: dynamic.StringSlice{dynamic.String("thumb")},
				Customs: map[string]PresetConfig{
					"200x100@2": presetCfg("", 0, 0, "webp", "avif"),
				},
			},
			// Без ведущего "/" и с завершающим "/" — нормализуются.
			"basket/products": {
				Customs: map[string]PresetConfig{"x200": presetCfg("", 0, 0, "webp")},
			},
			"/basket/users/": {
				Customs: map[string]PresetConfig{"200x": presetCfg("", 0, 0, "webp")},
			},
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

func TestCompile(t *testing.T) {
	cfg := Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/private": {
				Presets: dynamic.StringSlice{dynamic.String("thumb")},
				Customs: map[string]PresetConfig{
					"200x200": presetCfg("", 0, 0, "webp"),
				},
			},
		},
		Presets: []PresetConfig{
			presetCfg("thumb", 120, 80, "webp"),
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
	if len(compiled.Policy.PathNames()) != 1 {
		t.Errorf("PathNames = %v, want 1", compiled.Policy.PathNames())
	}
	if _, ok := compiled.Presets.Get("thumb"); !ok {
		t.Error("expected preset thumb")
	}
	pp := compiled.Policy.MatchPath("private")
	if pp == nil {
		t.Fatal("expected path-policy /private")
	}
	if _, ok := pp.Customs["200x200"]; !ok {
		t.Error("expected custom 200x200")
	}
	if _, ok := pp.Presets.Get("thumb"); !ok {
		t.Error("expected preset thumb in path-policy")
	}
}

func TestCompilePathPolicyNormalization(t *testing.T) {
	cfg := Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/":               {},
			"basket/products": {},
			"/basket/users/":  {},
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
}

func TestCompileCropTrimMapping(t *testing.T) {
	cfg := Config{
		Presets: []PresetConfig{
			{Name: dynamic.String("crop"), Crop: dynamic.String("center"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("trim"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("both"), Crop: dynamic.String("center"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			presetCfg("resize", 120, 80, "webp"),
			{Name: dynamic.String("smart"), Crop: dynamic.String("smart"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("face"), Crop: dynamic.String("face"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("object"), Crop: dynamic.String("object"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("smart-trim"), Crop: dynamic.String("smart"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("face-trim"), Crop: dynamic.String("face"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			{Name: dynamic.String("object-trim"), Crop: dynamic.String("object"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
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
	cfg := Config{
		Presets: []PresetConfig{
			{
				Name: dynamic.String("thumb@2"), Crop: dynamic.String("center"),
				Width: dynamic.Uint32(240), Height: dynamic.Uint32(160),
				OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
				DPR:           dynamic.NewNullable(dynamic.Uint32(2)),
				Quality:       dynamic.Uint32(80), Frames: dynamic.Uint32(10), Duration: dynamic.Uint32(5000),
				Loop: dynamic.NewNullable(dynamic.Bool(loop)),
			},
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
	cfg := Config{
		Presets: []PresetConfig{
			presetCfg("thumb", 120, 80, "webp"),
			presetCfg("thumb", 120, 80, "webp"),
		},
	}
	if _, err := Compile(cfg, nil, nil); err == nil {
		t.Error("expected duplicate preset error")
	}
}

func TestCompileDuplicatePresetWithDPRSuffix(t *testing.T) {
	// "thumb" и "thumb@2" — разные имена (не дубликаты).
	cfg := Config{
		Presets: []PresetConfig{
			presetCfg("thumb", 120, 80, "webp"),
			presetCfg("thumb@2", 240, 160, "webp"),
		},
	}
	if _, err := Compile(cfg, nil, nil); err != nil {
		t.Errorf("expected valid config with distinct names, got %v", err)
	}
}

func TestCompileCustomSizeFromName(t *testing.T) {
	cfg := Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/": {
				Customs: map[string]PresetConfig{
					"200x200":   presetCfg("", 0, 0, "webp"),
					"200x":      presetCfg("", 0, 0, "webp"),
					"x200":      presetCfg("", 0, 0, "webp"),
					"x":         presetCfg("", 0, 0, "webp"),
					"200x100@2": presetCfg("", 0, 0, "webp"),
				},
			},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	pp := compiled.Policy.MatchPath("anything")
	if pp == nil {
		t.Fatal("expected path-policy /")
	}
	tests := []struct {
		name string
		want string
	}{
		{"200x200", "200x200"},
		{"200x", "200x"},
		{"x200", "x200"},
		{"x", "x"},
		{"200x100@2", "200x100"},
	}
	for _, tt := range tests {
		c, ok := pp.Customs[tt.name]
		if !ok {
			t.Fatalf("custom %q not found", tt.name)
		}
		if got := c.Size().String(); got != tt.want {
			t.Errorf("custom %q size = %q, want %q", tt.name, got, tt.want)
		}
	}
	// dpr из имени custom.
	c, ok := pp.Customs["200x100@2"]
	if !ok {
		t.Fatal("custom 200x100@2 not found")
	}
	if c.DPR().Int() != 2 {
		t.Errorf("custom 200x100@2 DPR = %d, want 2", c.DPR().Int())
	}
}

func TestCompileCustomSizeOverride(t *testing.T) {
	// width/height в настройках имеют приоритет над именем.
	cfg := Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/": {
				Customs: map[string]PresetConfig{
					"200x200": presetCfg("", 300, 100, "webp"),
				},
			},
		},
	}
	compiled, err := Compile(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	pp := compiled.Policy.MatchPath("anything")
	c := pp.Customs["200x200"]
	if got := c.Size().String(); got != "300x100" {
		t.Errorf("custom size = %q, want 300x100 (settings override name)", got)
	}
}

func TestCompilePresetOrientationInheritsGlobal(t *testing.T) {
	// Пресет без явных ориентационных полей наследует глобальный дефолт.
	def := &processing.OrientationSpec{AutoOrient: true, Rotate: processing.Rotation90, Flip: processing.FlipHorizontal}
	cfg := Config{
		Presets: []PresetConfig{
			presetCfg("thumb", 120, 80, "webp"),
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
	cfg := Config{
		Presets: []PresetConfig{
			{
				Name: dynamic.String("thumb"), Crop: dynamic.String("center"),
				Width: dynamic.Uint32(120), Height: dynamic.Uint32(80),
				OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
				AutoOrient:    dynamic.NewNullable(dynamic.Bool(false)),
				Rotate:        dynamic.String("270"),
				Flip:          dynamic.String("vertical"),
			},
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
	cfg := Config{
		Presets: []PresetConfig{
			{
				Name: dynamic.String("thumb"), Crop: dynamic.String("center"),
				Width: dynamic.Uint32(120), Height: dynamic.Uint32(80),
				OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
				Rotate:        dynamic.String("none"),
				Flip:          dynamic.String("none"),
			},
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
	cfg := Config{
		Presets: []PresetConfig{
			presetCfg("thumb", 120, 80, "webp"),
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
		{Presets: []PresetConfig{{Name: dynamic.String("a"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, Rotate: dynamic.String("45")}}},
		{Presets: []PresetConfig{{Name: dynamic.String("a"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, Flip: dynamic.String("diagonal")}}},
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
