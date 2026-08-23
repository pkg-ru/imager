package config

import (
	"strings"
	"testing"

	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/policy"
)

func wmDecls(names ...string) []WatermarkConfig {
	out := make([]WatermarkConfig, 0, len(names))
	for _, n := range names {
		out = append(out, WatermarkConfig{
			Name:     n,
			Path:     "/w/" + n + ".png",
			Position: "center",
			Repeat:   "no-repeat",
			Size:     "contain",
		})
	}
	return out
}

func TestCompileWatermarks(t *testing.T) {
	cfg := &Config{
		Version: SupportedVersion,
		Watermarks: []WatermarkConfig{{
			Name:     "logo",
			Path:     "/w/logo.png",
			Position: "bottom",
			Repeat:   "repeat-x",
			Size:     "200px 50px",
		}},
		Policy: policyConfigForTest(),
		Processing: ProcessingConfig{
			DefaultQuality:   80,
			DefaultWatermark: "logo",
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
				c.Watermarks[0].Position = "middle"
			},
		},
		{
			name: "invalid size",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Watermarks[0].Size = "huge"
			},
		},
		{
			name: "empty path",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Watermarks[0].Path = ""
			},
		},
		{
			name: "unknown preset reference",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("other")
				c.Policy.Presets[0].Watermark = "missing"
			},
		},
		{
			name: "unknown default",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Processing.DefaultWatermark = "ghost"
			},
		},
		{
			name: "unknown path-policy reference",
			mutate: func(c *Config) {
				c.Watermarks = wmDecls("logo")
				c.Policy.PathPolicies[0].Watermark = "ghost"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Version:    SupportedVersion,
				Watermarks: wmDecls("logo"),
				Policy:     policyConfigForTest(),
				Processing: ProcessingConfig{DefaultQuality: 80},
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
			Authorization:  "safe",
			AllowedPresets: []string{"thumb"},
		},
		Presets: []policy.PresetConfig{{
			Name:         "thumb",
			Size:         "200x200",
			OutputFormat: "webp",
			Quality:      85,
			Watermark:    "logo",
		}},
		PathPolicies: []policy.PathPolicyConfig{{
			Path:      "/",
			Watermark: "logo",
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
