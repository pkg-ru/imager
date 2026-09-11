package config

import (
	"strings"
	"testing"

	"github.com/pkg-ru/dynamic"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/policy"
	"gitverse.ru/pkg-ru/imager/domain/processing"
)

func wmDecls(names ...string) map[string]WatermarkConfig {
	out := make(map[string]WatermarkConfig, len(names))
	for _, n := range names {
		out[n] = WatermarkConfig{
			Path:     dynamic.String("/w/" + n + ".png"),
			Position: dynamic.String("center"),
			Repeat:   dynamic.String("no-repeat"),
			Size:     dynamic.String("contain"),
		}
	}
	return out
}

func TestCompileWatermarks(t *testing.T) {
	cfg := &Config{
		Version: dynamic.String(SupportedVersion),
		Watermarks: map[string]WatermarkConfig{
			"logo": {
				Path:     dynamic.String("/w/logo.png"),
				Position: dynamic.String("bottom"),
				Repeat:   dynamic.String("repeat-x"),
				Size:     dynamic.String("200px 50px"),
			},
		},
		Policy: policyConfigForTest(),
		Processing: ProcessingConfig{
			DefaultWatermark: dynamic.String("logo"),
		},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	wm, ok := compiled.Watermarks["logo"]
	if !ok {
		t.Fatal("watermark logo not in registry")
	}
	if wm.Path != "/w/logo.png" || wm.Position != "bottom" || wm.Repeat != "repeat-x" || wm.WidthPx != 200 || wm.HeightPx != 50 {
		t.Errorf("spec mismatch: %+v", wm)
	}
	preset, err := compiled.Presets.Resolve(mustPresetReq(t))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if preset.Watermark() == nil || preset.Watermark().Name != "logo" {
		t.Errorf("preset watermark not resolved: %+v", preset.Watermark())
	}
	pp := compiled.Policy.MatchPath("any/path")
	if pp == nil {
		t.Errorf("path-policy not found for any/path")
	}
	if compiled.DefaultWatermark == nil || compiled.DefaultWatermark.Name != "logo" {
		t.Errorf("default watermark not resolved")
	}
}

func TestValidateWatermarkErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "invalid position",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				w := c.Watermarks["logo"]
				w.Position = dynamic.String("middle")
				c.Watermarks["logo"] = w
			},
		},
		{
			name: "invalid size",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				w := c.Watermarks["logo"]
				w.Size = dynamic.String("huge")
				c.Watermarks["logo"] = w
			},
		},
		{
			name: "empty path",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				w := c.Watermarks["logo"]
				w.Path = dynamic.String("")
				c.Watermarks["logo"] = w
			},
		},
		{
			name: "unknown preset reference",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("other")
				p := c.Policy.Presets["thumb"]
				p.Watermark = dynamic.String("missing")
				c.Policy.Presets["thumb"] = p
			},
		},
		{
			name: "unknown default",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Processing.DefaultWatermark = dynamic.String("ghost")
			},
		},
		{
			name: "unknown custom reference",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				pp := c.Policy.PathPolicies["/"]
				cc := pp.Customs["200x200"]
				cc.Watermark = dynamic.String("ghost")
				pp.Customs["200x200"] = cc
				c.Policy.PathPolicies["/"] = pp
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Version:    dynamic.String(SupportedVersion),
				Watermarks: wmDecls("logo"),
				Policy:     policyConfigForTest(),
				Processing: ProcessingConfig{},
			}
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), "watermark") {
				t.Fatalf("error should mention watermark, got: %v", err)
			}
		})
	}
}

// policyConfigForTest — минимальная политика с пресетом, custom и path-policy.
func policyConfigForTest() policy.Config {
	return policy.Config{
		Presets: map[string]policy.PresetConfig{
			"thumb": {
				Width:         dynamic.Uint32(200),
				Height:        dynamic.Uint32(200),
				OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
				Quality:       dynamic.Uint32(85),
				Watermark:     dynamic.String("logo"),
			},
		},
		PathPolicies: map[string]policy.PathPolicyConfig{
			"/": {
				Presets: dynamic.StringSlice{dynamic.String("thumb")},
				Customs: map[string]policy.PresetConfig{
					"200x200": {
						Width:         dynamic.Uint32(200),
						Height:        dynamic.Uint32(200),
						OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
						Watermark:     dynamic.String("logo"),
					},
				},
			},
		},
	}
}

// mustPresetReq создаёт валидный preset-запрос для резолва.
func mustPresetReq(t *testing.T) *asset.Request {
	t.Helper()
	req, err := asset.Parse("/photos/photo-1-jpg/thumb.webp")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return req
}

func TestCompileDefaultOrientation(t *testing.T) {
	cfg := &Config{
		Version:    dynamic.String(SupportedVersion),
		Watermarks: wmDecls("logo"),
		Policy:     policyConfigForTest(),
		Processing: ProcessingConfig{
			DefaultAutoOrient: dynamic.NewNullable(dynamic.Bool(false)),
			DefaultRotate:     dynamic.String("90"),
			DefaultFlip:       dynamic.String("horizontal"),
		},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	or := compiled.DefaultOrientation
	if or == nil {
		t.Fatal("DefaultOrientation is nil")
	}
	if or.AutoOrient || or.Rotate != 90 || or.Flip != "horizontal" {
		t.Errorf("unexpected default orientation: %+v", or)
	}
}

func TestCompileDefaultOrientationDefaults(t *testing.T) {
	cfg := &Config{
		Version:    dynamic.String(SupportedVersion),
		Watermarks: wmDecls("logo"),
		Policy:     policyConfigForTest(),
		Processing: ProcessingConfig{},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	or := compiled.DefaultOrientation
	if or == nil || !or.AutoOrient || or.Rotate != 0 || or.Flip != "" {
		t.Errorf("expected default {AutoOrient:true}, got %+v", or)
	}
}

func TestValidateDefaultOrientationErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProcessingConfig)
	}{
		{"invalid rotate", func(p *ProcessingConfig) { p.DefaultRotate = dynamic.String("45") }},
		{"invalid flip", func(p *ProcessingConfig) { p.DefaultFlip = dynamic.String("diagonal") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Version:    dynamic.String(SupportedVersion),
				Policy:     policyConfigForTest(),
				Processing: ProcessingConfig{},
			}
			tc.mutate(&cfg.Processing)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCompileDefaultTrim(t *testing.T) {
	cfg := &Config{
		Version:    dynamic.String(SupportedVersion),
		Watermarks: wmDecls("logo"),
		Policy:     policyConfigForTest(),
		Processing: ProcessingConfig{
			DefaultTrimMode:      dynamic.String("color"),
			DefaultTrimColor:     dynamic.String("#ffffff"),
			DefaultTrimTolerance: dynamic.Float64(0.25),
		},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ts := compiled.DefaultTrim
	if ts == nil {
		t.Fatal("DefaultTrim is nil")
	}
	if ts.Mode != processing.TrimModeColor || ts.Color != "#ffffff" || ts.Tolerance != 0.25 {
		t.Errorf("unexpected default trim spec: %+v", ts)
	}
}

func TestCompileDefaultTrimDefaults(t *testing.T) {
	cfg := &Config{
		Version:    dynamic.String(SupportedVersion),
		Watermarks: wmDecls("logo"),
		Policy:     policyConfigForTest(),
		Processing: ProcessingConfig{},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ts := compiled.DefaultTrim
	if ts == nil {
		t.Fatal("DefaultTrim is nil")
	}
	if ts.Mode != processing.TrimModeAuto || ts.Color != "" || ts.Tolerance != 0 {
		t.Errorf("expected default {Mode:auto, Tolerance:0}, got %+v", ts)
	}
}

func TestValidateDefaultTrimErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProcessingConfig)
	}{
		{"invalid mode", func(p *ProcessingConfig) { p.DefaultTrimMode = dynamic.String("edge") }},
		{"color mode without color", func(p *ProcessingConfig) { p.DefaultTrimMode = dynamic.String("color") }},
		{"invalid color", func(p *ProcessingConfig) {
			p.DefaultTrimMode = dynamic.String("color")
			p.DefaultTrimColor = dynamic.String("white")
		}},
		{"tolerance too high", func(p *ProcessingConfig) { p.DefaultTrimTolerance = dynamic.Float64(1.5) }},
		{"tolerance negative", func(p *ProcessingConfig) { p.DefaultTrimTolerance = dynamic.Float64(-0.1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Version:    dynamic.String(SupportedVersion),
				Policy:     policyConfigForTest(),
				Processing: ProcessingConfig{},
			}
			tc.mutate(&cfg.Processing)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDefaultLoopVar(t *testing.T) {
	loop := dynamic.NewNullable(dynamic.Bool(true))
	cfg := &Config{
		Version:    dynamic.String(SupportedVersion),
		Watermarks: wmDecls("logo"),
		Policy:     policyConfigForTest(),
		Processing: ProcessingConfig{
			DefaultLoop: loop,
		},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if compiled.DefaultLoop == nil || !*compiled.DefaultLoop {
		t.Errorf("DefaultLoop = %v, want true", compiled.DefaultLoop)
	}
}

func TestCompileDefaultVideo(t *testing.T) {
	cfg := &Config{
		Version:    dynamic.String(SupportedVersion),
		Watermarks: wmDecls("logo"),
		Policy:     policyConfigForTest(),
		Processing: ProcessingConfig{
			DefaultVideoFramePercent: 25,
			DefaultVideoMinContrast:  0.3,
			DefaultVideoFrameStep:    5,
			DefaultVideoAttempts:     10,
		},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if compiled.DefaultVideoFramePercent != 25 {
		t.Errorf("DefaultVideoFramePercent = %d, want 25", compiled.DefaultVideoFramePercent)
	}
	if compiled.DefaultVideoMinContrast != 0.3 {
		t.Errorf("DefaultVideoMinContrast = %v, want 0.3", compiled.DefaultVideoMinContrast)
	}
	if compiled.DefaultVideoFrameStep != 5 {
		t.Errorf("DefaultVideoFrameStep = %d, want 5", compiled.DefaultVideoFrameStep)
	}
	if compiled.DefaultVideoAttempts != 10 {
		t.Errorf("DefaultVideoAttempts = %d, want 10", compiled.DefaultVideoAttempts)
	}
}

func TestCompileDefaultVideoDefaults(t *testing.T) {
	cfg := &Config{
		Version:    dynamic.String(SupportedVersion),
		Watermarks: wmDecls("logo"),
		Policy:     policyConfigForTest(),
		Processing: ProcessingConfig{},
	}
	compiled, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if compiled.DefaultVideoFramePercent != 0 || compiled.DefaultVideoMinContrast != 0 ||
		compiled.DefaultVideoFrameStep != 0 || compiled.DefaultVideoAttempts != 0 {
		t.Errorf("expected zero video defaults, got %+v", compiled)
	}
}

func TestValidateDefaultVideoErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProcessingConfig)
	}{
		{"frame percent too high", func(p *ProcessingConfig) { p.DefaultVideoFramePercent = 101 }},
		{"frame percent negative", func(p *ProcessingConfig) { p.DefaultVideoFramePercent = -1 }},
		{"min contrast too high", func(p *ProcessingConfig) { p.DefaultVideoMinContrast = 1.5 }},
		{"min contrast negative", func(p *ProcessingConfig) { p.DefaultVideoMinContrast = -0.1 }},
		{"frame step zero", func(p *ProcessingConfig) { p.DefaultVideoFrameStep = 0 }},
		{"frame step negative", func(p *ProcessingConfig) { p.DefaultVideoFrameStep = -3 }},
		{"attempts zero", func(p *ProcessingConfig) { p.DefaultVideoAttempts = 0 }},
		{"attempts negative", func(p *ProcessingConfig) { p.DefaultVideoAttempts = -2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Version:    dynamic.String(SupportedVersion),
				Policy:     policyConfigForTest(),
				Processing: ProcessingConfig{},
			}
			tc.mutate(&cfg.Processing)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
