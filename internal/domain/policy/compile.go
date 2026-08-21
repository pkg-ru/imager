package policy

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/asset"
)

// Config — конфигурация политики (DTO, не связан с YAML).
type Config struct {
	Global  GlobalConfig   `yaml:"global"`
	Buckets []BucketConfig `yaml:"buckets"`
	Presets []PresetConfig `yaml:"presets"`
}

// GlobalConfig — глобальная конфигурация политики.
type GlobalConfig struct {
	Authorization  string   `yaml:"authorization"`
	SizeRules      []string `yaml:"size-rules"`
	AllowedPresets []string `yaml:"allowed-presets"`
	Limits         Limits   `yaml:"limits"`
}

// BucketConfig — конфигурация политики для bucket.
type BucketConfig struct {
	Bucket         string   `yaml:"bucket"`
	Authorization  string   `yaml:"authorization"`
	SizeRules      []string `yaml:"size-rules"`
	AllowedPresets []string `yaml:"allowed-presets"`
	Limits         Limits   `yaml:"limits"`
}

// PresetConfig — конфигурация пресета.
//
// Preset не содержит source-format: исходный формат определяется URL
// ({source_name}-{source_format}/{preset_name}.{output_format}). DPR в
// пресете не задаётся: он передаётся в URL.
type PresetConfig struct {
	Name         string `yaml:"name"`
	Transform    string `yaml:"transform"`
	Size         string `yaml:"size"`
	OutputFormat string `yaml:"output-format"`
}

// ValidationError описывает ошибку валидации конфигурации с путём к полю.
type ValidationError struct {
	Path   string
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("config validation failed at %q: %s", e.Path, e.Reason)
	}
	return "config validation failed: " + e.Reason
}

// ValidationErrors — набор ошибок валидации.
type ValidationErrors []*ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "no validation errors"
	}
	var sb strings.Builder
	sb.WriteString("config validation failed:")
	for _, err := range e {
		sb.WriteString("\n  - ")
		sb.WriteString(err.Error())
	}
	return sb.String()
}

// ValidateConfig проверяет конфигурацию и возвращает список ошибок с путями.
// Если ошибок нет, возвращается nil.
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return &ValidationError{Path: "", Reason: "config is nil"}
	}
	var errs ValidationErrors

	// Global.
	if cfg.Global.Authorization != "" && !ValidAuthorization(Authorization(cfg.Global.Authorization)) {
		errs = append(errs, &ValidationError{
			Path:   "global.authorization",
			Reason: fmt.Sprintf("invalid value %q, must be safe or unsafe", cfg.Global.Authorization),
		})
	}
	for i, rule := range cfg.Global.SizeRules {
		if _, err := ParseSizeRule(rule); err != nil {
			errs = append(errs, &ValidationError{
				Path:   fmt.Sprintf("global.size-rules[%d]", i),
				Reason: err.Error(),
			})
		}
	}
	if _, err := NewLimits(cfg.Global.Limits); err != nil {
		errs = append(errs, &ValidationError{Path: "global.limits", Reason: err.Error()})
	}

	// Buckets.
	seen := map[string]bool{}
	for i, b := range cfg.Buckets {
		base := fmt.Sprintf("buckets[%d]", i)
		if b.Bucket == "" {
			errs = append(errs, &ValidationError{Path: base + ".bucket", Reason: "bucket name is empty"})
		} else if seen[b.Bucket] {
			errs = append(errs, &ValidationError{Path: base + ".bucket", Reason: fmt.Sprintf("duplicate bucket %q", b.Bucket)})
		}
		seen[b.Bucket] = true
		if b.Authorization != "" && !ValidAuthorization(Authorization(b.Authorization)) {
			errs = append(errs, &ValidationError{
				Path:   base + ".authorization",
				Reason: fmt.Sprintf("invalid value %q, must be safe or unsafe", b.Authorization),
			})
		}
		for j, rule := range b.SizeRules {
			if _, err := ParseSizeRule(rule); err != nil {
				errs = append(errs, &ValidationError{
					Path:   fmt.Sprintf("%s.size-rules[%d]", base, j),
					Reason: err.Error(),
				})
			}
		}
		if _, err := NewLimits(b.Limits); err != nil {
			errs = append(errs, &ValidationError{Path: base + ".limits", Reason: err.Error()})
		}
	}

	// Presets.
	presetNames := map[string]bool{}
	for i, p := range cfg.Presets {
		base := fmt.Sprintf("presets[%d]", i)
		if p.Name == "" {
			errs = append(errs, &ValidationError{Path: base + ".name", Reason: "preset name is empty"})
		} else if presetNames[p.Name] {
			errs = append(errs, &ValidationError{Path: base + ".name", Reason: fmt.Sprintf("duplicate preset %q", p.Name)})
		}
		presetNames[p.Name] = true
		if !asset.ValidTransform(asset.Transform(p.Transform)) {
			errs = append(errs, &ValidationError{
				Path:   base + ".transform",
				Reason: fmt.Sprintf("invalid value %q, must be c, t or ct", p.Transform),
			})
		}
		if _, err := asset.ParseSize(p.Size); err != nil {
			errs = append(errs, &ValidationError{Path: base + ".size", Reason: err.Error()})
		}
		if p.OutputFormat == "" {
			errs = append(errs, &ValidationError{Path: base + ".output-format", Reason: "output format is empty"})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Compiled — результат компиляции политики.
type Compiled struct {
	Policy  *Policy
	Presets *asset.PresetSet
}

// Compile собирает Policy и PresetSet из валидированной конфигурации.
func Compile(cfg *Config) (*Compiled, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}

	policy := &Policy{
		Global: GlobalPolicy{
			Authorization:  Authorization(cfg.Global.Authorization),
			SizeRules:      make([]SizeRule, 0, len(cfg.Global.SizeRules)),
			AllowedPresets: cfg.Global.AllowedPresets,
			Limits:         cfg.Global.Limits,
		},
	}
	for _, r := range cfg.Global.SizeRules {
		rule, _ := ParseSizeRule(r)
		policy.Global.SizeRules = append(policy.Global.SizeRules, rule)
	}

	for _, b := range cfg.Buckets {
		bp := BucketPolicy{
			Bucket:         b.Bucket,
			Authorization:  Authorization(b.Authorization),
			AllowedPresets: b.AllowedPresets,
			Limits:         b.Limits,
		}
		if b.SizeRules != nil {
			bp.SizeRules = make([]SizeRule, 0, len(b.SizeRules))
			for _, r := range b.SizeRules {
				rule, _ := ParseSizeRule(r)
				bp.SizeRules = append(bp.SizeRules, rule)
			}
		}
		policy.Buckets = append(policy.Buckets, bp)
	}

	presets := make([]*asset.Preset, 0, len(cfg.Presets))
	for _, p := range cfg.Presets {
		size, _ := asset.ParseSize(p.Size)
		preset, err := asset.NewPreset(
			p.Name,
			asset.Transform(p.Transform),
			size,
			asset.Format(p.OutputFormat),
		)
		if err != nil {
			return nil, err
		}
		presets = append(presets, preset)
	}
	presetSet, err := asset.NewPresetSet(presets)
	if err != nil {
		return nil, err
	}

	return &Compiled{Policy: policy, Presets: presetSet}, nil
}

// ParseSizeRule разбирает строку правила размера.
func ParseSizeRule(s string) (SizeRule, error) {
	if s == "" {
		return SizeRule{}, fmt.Errorf("empty size rule")
	}
	x := strings.Index(s, "x")
	if x < 0 {
		return SizeRule{}, fmt.Errorf("missing 'x' separator")
	}
	wStr := s[:x]
	hStr := s[x+1:]

	var rule SizeRule
	if wStr != "" {
		r, err := parseRange(wStr)
		if err != nil {
			return SizeRule{}, fmt.Errorf("invalid width: %w", err)
		}
		rule.Width = &r
	}
	if hStr != "" {
		r, err := parseRange(hStr)
		if err != nil {
			return SizeRule{}, fmt.Errorf("invalid height: %w", err)
		}
		rule.Height = &r
	}
	if rule.Width == nil && rule.Height == nil {
		return SizeRule{}, fmt.Errorf("size rule must specify width or height")
	}
	return rule, nil
}

func parseRange(s string) (Range, error) {
	if strings.Contains(s, "-") {
		parts := strings.SplitN(s, "-", 2)
		if len(parts) != 2 {
			return Range{}, fmt.Errorf("invalid range %q", s)
		}
		min, err := strconv.Atoi(parts[0])
		if err != nil {
			return Range{}, fmt.Errorf("invalid range min %q", parts[0])
		}
		max, err := strconv.Atoi(parts[1])
		if err != nil {
			return Range{}, fmt.Errorf("invalid range max %q", parts[1])
		}
		if min < 0 || max < 0 {
			return Range{}, fmt.Errorf("range values must be non-negative")
		}
		if min > max {
			return Range{}, fmt.Errorf("range min %d greater than max %d", min, max)
		}
		return Range{Min: min, Max: max}, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return Range{}, fmt.Errorf("invalid value %q", s)
	}
	if v < 0 {
		return Range{}, fmt.Errorf("value must be non-negative")
	}
	return Range{Min: v, Max: v}, nil
}
