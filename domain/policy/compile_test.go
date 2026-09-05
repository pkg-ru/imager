package policy

import (
	"testing"

	"github.com/pkg-ru/dynamic"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gopkg.in/yaml.v3"
)

// presetCfg — хелпер построения PresetConfig (имя пресета задаётся КЛЮЧОМ
// map в Config.Presets, поэтому в саму структуру не входит).
func presetCfg(name string, width, height uint32, outFmts ...string) PresetConfig {
	formats := make(dynamic.StringSlice, 0, len(outFmts))
	for _, f := range outFmts {
		formats = append(formats, dynamic.String(f))
	}
	return PresetConfig{
		Width:         dynamic.Uint32(width),
		Height:        dynamic.Uint32(height),
		OutputFormats: formats,
	}
}

// presetCfgWithDPR — пресет с явным dpr (для @N-суффиксов и фиксированного
// множителя; @N требует dpr == N).
func presetCfgWithDPR(name string, width, height uint32, dpr uint32, outFmts ...string) PresetConfig {
	c := presetCfg(name, width, height, outFmts...)
	c.DPR = dynamic.NewNullable(dynamic.Uint32(dpr))
	return c
}

// presetsMap — хелпер построения map пресетов: имя = ключ.
func presetsMap(pairs ...[2]any) map[string]PresetConfig {
	m := make(map[string]PresetConfig, len(pairs))
	for _, p := range pairs {
		name, _ := p[0].(string)
		cfg, _ := p[1].(PresetConfig)
		m[name] = cfg
	}
	return m
}

func TestValidateConfig(t *testing.T) {
	valid := &Config{
		Presets: map[string]PresetConfig{
			"thumb":  presetCfg("thumb", 120, 80, "webp"),
			"smart":  presetCfg("smart", 120, 80, "webp"),
			"face":   presetCfg("face", 120, 80, "webp"),
			"object": presetCfg("object", 120, 80, "webp"),
			// Пустой crop — валиден (кроп не используется).
			"resize": presetCfg("resize", 120, 80, "webp"),
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
		// Пресеты: пустое имя (пустой ключ map).
		{Presets: map[string]PresetConfig{"": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{dynamic.String("")}}}},
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{}}}},
		// Недопустимые значения crop.
		{Presets: map[string]PresetConfig{"a": {Crop: dynamic.String("bogus"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: map[string]PresetConfig{"a": {Crop: dynamic.String("true"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: map[string]PresetConfig{"a": {Crop: dynamic.String("Center"), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		// dpr вне [0,3].
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, DPR: dynamic.NewNullable(dynamic.Uint32(4))}}},
		// quality вне [0,100].
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, Quality: dynamic.Uint32(101)}}},
		// width/height > MaxDimension.
		{Presets: map[string]PresetConfig{"a": {Width: dynamic.Uint32(asset.MaxDimension + 1), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		// Имя пресета с @0/@1.
		{Presets: map[string]PresetConfig{"a@0": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		{Presets: map[string]PresetConfig{"a@1": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		// Конфликт dpr в имени vs настройки.
		{Presets: map[string]PresetConfig{"a@2": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, DPR: dynamic.NewNullable(dynamic.Uint32(3))}}},
		// Имя с @2 без dpr в настройках — ошибка (dpr обязателен и == 2).
		{Presets: map[string]PresetConfig{"a@2": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}},
		// Имя без @N с dpr: 2 — ошибка (без суффикса допустимо только dpr: 1).
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, DPR: dynamic.NewNullable(dynamic.Uint32(2))}}},
		// Имя без @N с dpr: 3 — ошибка (без суффикса допустимо только dpr: 1).
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, DPR: dynamic.NewNullable(dynamic.Uint32(3))}}},
		// Custom: невалидное имя.
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"bogus": presetCfg("", 0, 0, "webp")}}}},
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200@0": presetCfg("", 0, 0, "webp")}}}},
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200@1": presetCfg("", 0, 0, "webp")}}}},
		// Custom: имя с @2 без dpr в настройках — ошибка (dpr обязателен и == 2).
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200@2": presetCfg("", 0, 0, "webp")}}}},
		// Custom: имя с @2 с dpr: 3 в настройках — ошибка (конфликт, должен быть 2).
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200@2": {DPR: dynamic.NewNullable(dynamic.Uint32(3)), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}}}},
		// Custom: имя без @N с dpr: 2 — ошибка (без суффикса допустимо только dpr: 1).
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200": {DPR: dynamic.NewNullable(dynamic.Uint32(2)), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}}}},
		// Custom: имя без @N с dpr: 3 — ошибка (без суффикса допустимо только dpr: 1).
		{PathPolicies: map[string]PathPolicyConfig{"/": {Customs: map[string]PresetConfig{"200x200": {DPR: dynamic.NewNullable(dynamic.Uint32(3)), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}}}}}},
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
		Presets: map[string]PresetConfig{
			// Пресет без @N с dpr: 1 (фиксированный множитель 1) — валиден.
			"thumb": {
				Crop:  dynamic.String("center"),
				Width: dynamic.Uint32(120), Height: dynamic.Uint32(80),
				OutputFormats: dynamic.StringSlice{dynamic.String("webp"), dynamic.String("avif")},
				DPR:           dynamic.NewNullable(dynamic.Uint32(1)),
				Quality:       dynamic.Uint32(80), Frames: dynamic.Uint32(10), Duration: dynamic.Uint32(5000),
				Loop: dynamic.NewNullable(dynamic.Bool(loop)),
			},
			"trim":   {Trim: dynamic.Bool(true), Height: dynamic.Uint32(50), OutputFormats: dynamic.StringSlice{dynamic.String("png")}},
			"both":   {Crop: dynamic.String("center"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(800), Height: dynamic.Uint32(200), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"resize": {Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
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
					// Имя с @2 требует dpr: 2.
					"200x100@2": presetCfgWithDPR("", 0, 0, 2, "webp", "avif"),
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
		Presets: map[string]PresetConfig{
			"thumb": presetCfg("thumb", 120, 80, "webp"),
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config with path-policies, got %v", err)
	}
}

// TestValidateConfigValidCustomNameDPR — позитивные кейсы правил @N для
// customs: имя без @N без dpr — ок (wildcard); имя без @N с
// dpr: 1 — ок; имя с @N с dpr == N — ок.
func TestValidateConfigValidCustomNameDPR(t *testing.T) {
	valid := &Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/": {
				Customs: map[string]PresetConfig{
					// Без @N, dpr не задан — wildcard: допустимы x.webp/x@2.webp/x@3.webp.
					"x": presetCfg("", 0, 0, "webp"),
					// Без @N, dpr: 1 — ок (фиксированный множитель 1).
					"200x200": {DPR: dynamic.NewNullable(dynamic.Uint32(1)), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
					// Имя с @2, dpr: 2 — ок.
					"200x100@2": presetCfgWithDPR("", 0, 0, 2, "webp"),
				},
			},
		},
	}
	if err := ValidateConfig(valid); err != nil {
		t.Errorf("expected valid config, got %v", err)
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
		Presets: map[string]PresetConfig{
			"thumb": presetCfg("thumb", 120, 80, "webp"),
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
		Presets: map[string]PresetConfig{
			"crop":        {Crop: dynamic.String("center"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"trim":        {Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"both":        {Crop: dynamic.String("center"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"resize":      presetCfg("resize", 120, 80, "webp"),
			"smart":       {Crop: dynamic.String("smart"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"face":        {Crop: dynamic.String("face"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"object":      {Crop: dynamic.String("object"), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"smart-trim":  {Crop: dynamic.String("smart"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"face-trim":   {Crop: dynamic.String("face"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
			"object-trim": {Crop: dynamic.String("object"), Trim: dynamic.Bool(true), Width: dynamic.Uint32(120), Height: dynamic.Uint32(80), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}},
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
		Presets: map[string]PresetConfig{
			"thumb@2": {
				Crop:  dynamic.String("center"),
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
	// Дубликаты имён в map невозможны по построению; проверяем, что
	// одинаковые пресеты с разными ключами компилируются без ошибок.
	cfg := Config{
		Presets: map[string]PresetConfig{
			"thumb":   presetCfg("thumb", 120, 80, "webp"),
			"thumb@2": presetCfgWithDPR("thumb@2", 240, 160, 2, "webp"),
		},
	}
	if _, err := Compile(cfg, nil, nil); err != nil {
		t.Errorf("expected valid config, got %v", err)
	}
}

func TestCompileDuplicatePresetWithDPRSuffix(t *testing.T) {
	// "thumb" и "thumb@2" — разные имена (не дубликаты).
	cfg := Config{
		Presets: map[string]PresetConfig{
			"thumb":   presetCfg("thumb", 120, 80, "webp"),
			"thumb@2": presetCfgWithDPR("thumb@2", 240, 160, 2, "webp"),
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
					"200x200": presetCfg("", 0, 0, "webp"),
					"200x":    presetCfg("", 0, 0, "webp"),
					"x200":    presetCfg("", 0, 0, "webp"),
					"x":       presetCfg("", 0, 0, "webp"),
					// Имя с @2 требует dpr: 2.
					"200x100@2": presetCfgWithDPR("", 0, 0, 2, "webp"),
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
		Presets: map[string]PresetConfig{
			"thumb": presetCfg("thumb", 120, 80, "webp"),
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
		Presets: map[string]PresetConfig{
			"thumb": {
				Crop:  dynamic.String("center"),
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
		Presets: map[string]PresetConfig{
			"thumb": {
				Crop:  dynamic.String("center"),
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
		Presets: map[string]PresetConfig{
			"thumb": presetCfg("thumb", 120, 80, "webp"),
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

func intPtr2(v int) *int           { return &v }
func boolPtr2(v bool) *bool        { return &v }
func floatPtr2(v float64) *float64 { return &v }

// TestCompileEncodingOverrides проверяет, что плоские native-ключи пресета
// собираются в overrides (формат → нативные параметры реестра без префикса)
// и прокидываются в asset.Preset. per-format quality (webp-quality) ложится
// под ключом "quality".
func TestCompileEncodingOverrides(t *testing.T) {
	cfg := Config{
		Presets: map[string]PresetConfig{
			"thumb": {
				Width:               dynamic.Uint32(120),
				Height:              dynamic.Uint32(80),
				OutputFormats:       dynamic.StringSlice{dynamic.String("webp"), dynamic.String("png")},
				Quality:             dynamic.Uint32(85),
				WebPQuality:         intPtr2(90),
				WebPReductionEffort: intPtr2(6),
				PNGCompressionLevel: intPtr2(9),
				JXLEffort:           intPtr2(9),
			},
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
	over := p.EncodingOverrides()
	if over == nil {
		t.Fatal("expected encoding overrides")
	}
	if over["webp"]["quality"] != 90 {
		t.Errorf("webp quality = %v, want 90", over["webp"]["quality"])
	}
	if over["webp"]["reduction-effort"] != 6 {
		t.Errorf("webp reduction-effort = %v, want 6", over["webp"]["reduction-effort"])
	}
	if over["png"]["compression-level"] != 9 {
		t.Errorf("png compression-level = %v, want 9", over["png"]["compression-level"])
	}
	if over["jxl"]["effort"] != 9 {
		t.Errorf("jxl effort = %v, want 9", over["jxl"]["effort"])
	}
}

// TestCompileEncodingOverridesYAML проверяет разбор плоских native-ключей из
// YAML-представления Config (тот же путь, что в composition: yaml.v3) в
// PresetConfig и проброс в asset.Preset через Compile.
func TestCompileEncodingOverridesYAML(t *testing.T) {
	doc := `
presets:
  thumb:
    width: 320
    height: 200
    output-formats: [webp, png]
    quality: 85
    png-compression-level: 9
    webp-reduction-effort: 6
    webp-quality: 90
    jxl-effort: 9
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal error: %v", err)
	}
	pcfg, ok := cfg.Presets["thumb"]
	if !ok {
		t.Fatal("expected preset thumb")
	}
	if pcfg.WebPQuality == nil || *pcfg.WebPQuality != 90 {
		t.Errorf("WebPQuality = %v, want 90", pcfg.WebPQuality)
	}
	if pcfg.WebPReductionEffort == nil || *pcfg.WebPReductionEffort != 6 {
		t.Errorf("WebPReductionEffort = %v, want 6", pcfg.WebPReductionEffort)
	}
	if pcfg.PNGCompressionLevel == nil || *pcfg.PNGCompressionLevel != 9 {
		t.Errorf("PNGCompressionLevel = %v, want 9", pcfg.PNGCompressionLevel)
	}
	compiled, err := Compile(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	p, ok := compiled.Presets.Get("thumb")
	if !ok {
		t.Fatal("expected preset thumb")
	}
	over := p.EncodingOverrides()
	if over["webp"]["quality"] != 90 {
		t.Errorf("webp quality = %v, want 90", over["webp"]["quality"])
	}
	if over["webp"]["reduction-effort"] != 6 {
		t.Errorf("webp reduction-effort = %v, want 6", over["webp"]["reduction-effort"])
	}
	if over["png"]["compression-level"] != 9 {
		t.Errorf("png compression-level = %v, want 9", over["png"]["compression-level"])
	}
}

// TestValidateConfigInvalidEncodingOverrides проверяет fail-fast валидацию
// native-параметров: выход за диапазон реестра — ошибка конфигурации.
func TestValidateConfigInvalidEncodingOverrides(t *testing.T) {
	invalid := []*Config{
		// png-compression-level вне [1,9].
		{Presets: map[string]PresetConfig{"a": {Width: dynamic.Uint32(1), OutputFormats: dynamic.StringSlice{dynamic.String("png")}, PNGCompressionLevel: intPtr2(0)}}},
		// webp-reduction-effort вне [0,6].
		{Presets: map[string]PresetConfig{"a": {Width: dynamic.Uint32(1), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, WebPReductionEffort: intPtr2(7)}}},
		// gif-bit-depth вне [1,8].
		{Presets: map[string]PresetConfig{"a": {Width: dynamic.Uint32(1), OutputFormats: dynamic.StringSlice{dynamic.String("gif")}, GIFBitDepth: intPtr2(9)}}},
		// webp-quality: 0 (вне [1,100]).
		{Presets: map[string]PresetConfig{"a": {Width: dynamic.Uint32(1), OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, WebPQuality: intPtr2(0)}}},
	}
	for _, c := range invalid {
		if err := ValidateConfig(c); err == nil {
			t.Errorf("ValidateConfig(+%v) expected error", c)
		}
	}
}

func TestValidateConfigInvalidPresetOrientation(t *testing.T) {
	invalid := []*Config{
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, Rotate: dynamic.String("45")}}},
		{Presets: map[string]PresetConfig{"a": {OutputFormats: dynamic.StringSlice{dynamic.String("webp")}, Flip: dynamic.String("diagonal")}}},
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

// TestConfigLearningModeYAML — парсинг yaml-поля learning-mode в Config.
// Config — DTO с yaml-тегами; поле LearningMode (dynamic.Bool) должно
// корректно разбираться из YAML (true/false) и не ломать ValidateConfig.
// Декодирование выполняется тем же путём, что и в composition
// (yaml.v2 re-encode секции policy в typed Config).
func TestConfigLearningModeYAML(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "learning-mode true",
			yaml: "learning-mode: true\n",
			want: true,
		},
		{
			name: "learning-mode false",
			yaml: "learning-mode: false\n",
			want: false,
		},
		{
			name: "learning-mode отсутствует",
			yaml: "presets: {}\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(tt.yaml), &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := cfg.LearningMode.Unwrap(); got != tt.want {
				t.Errorf("LearningMode = %v, want %v", got, tt.want)
			}
			// Валидация не ломается наличием флага.
			if err := ValidateConfig(&cfg); err != nil {
				t.Errorf("ValidateConfig: %v", err)
			}
		})
	}
}
