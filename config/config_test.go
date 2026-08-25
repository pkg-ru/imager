package config

import (
	"strings"
	"testing"

	"github.com/pkg-ru/dynamic"
	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/policy"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

func wmDecls(names ...string) []WatermarkConfig {
	out := make([]WatermarkConfig, 0, len(names))
	for _, n := range names {
		out = append(out, WatermarkConfig{
			Name:     dynamic.String(n),
			Path:     dynamic.String("/w/" + n + ".png"),
			Position: dynamic.String("center"),
			Repeat:   dynamic.String("no-repeat"),
			Size:     dynamic.String("contain"),
		})
	}
	return out
}

func TestCompileWatermarks(t *testing.T) {
	cfg := &Config{
		Version: dynamic.String(SupportedVersion),
		Watermarks: []WatermarkConfig{{
			Name:     dynamic.String("logo"),
			Path:     dynamic.String("/w/logo.png"),
			Position: dynamic.String("bottom"),
			Repeat:   dynamic.String("repeat-x"),
			Size:     dynamic.String("200px 50px"),
		}},
		Policy: policyConfigForTest(),
		Processing: ProcessingConfig{
			DefaultQuality:   dynamic.Int64(80),
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
	if pp == nil || pp.Watermark == nil || pp.Watermark.Name != "logo" {
		t.Errorf("path-policy watermark not resolved")
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
			name:   "duplicate names",
			mutate: func(c *Config) { c.Watermarks = append(wmDecls("logo"), wmDecls("logo")...) },
		},
		{
			name: "invalid position",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Watermarks[0].Position = dynamic.String("middle")
			},
		},
		{
			name: "invalid size",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Watermarks[0].Size = dynamic.String("huge")
			},
		},
		{
			name: "empty path",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Watermarks[0].Path = dynamic.String("")
			},
		},
		{
			name: "unknown preset reference",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("other")
				c.Policy.Presets[0].Watermark = dynamic.String("missing")
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
			name: "unknown path-policy reference",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Policy.PathPolicies[0].Watermark = dynamic.String("ghost")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Version:    dynamic.String(SupportedVersion),
				Watermarks: wmDecls("logo"),
				Policy:     policyConfigForTest(),
				Processing: ProcessingConfig{DefaultQuality: dynamic.Int64(80)},
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

// policyConfigForTest — минимальная политика с пресетом и path-policy.
func policyConfigForTest() policy.Config {
	return policy.Config{
		Global: policy.GlobalConfig{
			Authorization:  dynamic.String("safe"),
			AllowedPresets: []dynamic.String{dynamic.String("thumb")},
		},
		Presets: []policy.PresetConfig{{
			Name:         dynamic.String("thumb"),
			Size:         dynamic.String("200x200"),
			OutputFormat: dynamic.String("webp"),
			Quality:      dynamic.Int64(85),
			Watermark:    dynamic.String("logo"),
		}},
		PathPolicies: []policy.PathPolicyConfig{{
			Path:      dynamic.String("/"),
			Watermark: dynamic.String("logo"),
		}},
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
			DefaultQuality:    dynamic.Int64(80),
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
		Processing: ProcessingConfig{DefaultQuality: dynamic.Int64(80)},
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
				Processing: ProcessingConfig{DefaultQuality: dynamic.Int64(80)},
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
			DefaultQuality:       dynamic.Int64(80),
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
		Processing: ProcessingConfig{DefaultQuality: dynamic.Int64(80)},
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
				Processing: ProcessingConfig{DefaultQuality: dynamic.Int64(80)},
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
			DefaultQuality: dynamic.Int64(80),
			DefaultLoop:    loop,
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
