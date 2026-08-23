package policy

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

// Config — конфигурация политики (DTO, не связан с YAML).
type Config struct {
	Global       GlobalConfig       `yaml:"global"`
	PathPolicies []PathPolicyConfig `yaml:"path-policies"`
	Presets      []PresetConfig     `yaml:"presets"`
}

// GlobalConfig — глобальная конфигурация политики.
type GlobalConfig struct {
	Authorization  string   `yaml:"authorization"`
	SizeRules      []string `yaml:"size-rules"`
	AllowedPresets []string `yaml:"allowed-presets"`
	Limits         Limits   `yaml:"limits"`
}

// PathPolicyConfig — конфигурация path-policy (политики пути).
//
// Path — префикс пути. Нормализуется при компиляции (см. normalizePath):
// добавляется ведущий "/", убирается завершающий "/" (кроме "/").
//
// Path-policy применяется только к каноническим URL (не preset) и является
// дополнительным ограничением поверх глобальной политики.
type PathPolicyConfig struct {
	Path string `yaml:"path"`
	// DPR — строка-диапазон допустимого DPR (nil/пусто = без ограничения).
	// Например "0-1" (только dpr=1) или "2-3" (dpr 2 или 3).
	DPR string `yaml:"dpr"`
	// Crop — требование к crop (nil = не задано/неважно). true = crop
	// обязан присутствовать в transform, false = crop запрещён.
	Crop *bool `yaml:"crop"`
	// Trim — требование к trim (nil = не задано/неважно). true = trim
	// обязан присутствовать в transform, false = trim запрещён.
	Trim *bool `yaml:"trim"`
	// Watermark — имя ватермарки (ссылка на элемент секции watermarks;
	// пусто = не задана). Разрешается в спецификацию при компиляции:
	// неизвестное имя — ошибка старта.
	Watermark string `yaml:"watermark"`
}

// PresetConfig — конфигурация пресета.
//
// Preset не содержит source-format: исходный формат определяется URL
// ({source_name}-{source_format}/{preset_name}.{output_format}).
//
// Имя пресета может содержать фиксированный @dpr-суффикс (например
// "thumb@2"). Поле dpr (если задано) имеет приоритет над @dpr в имени.
//
// crop/trim — булевы флаги, маппящиеся в операции:
//   - crop=true, trim=false → crop
//   - crop=false, trim=true → trim
//   - crop=true, trim=true → crop-trim
//   - crop=false, trim=false → resize (пустой transform)
type PresetConfig struct {
	Name         string `yaml:"name"`
	Crop         bool   `yaml:"crop"`
	Trim         bool   `yaml:"trim"`
	Size         string `yaml:"size"`
	OutputFormat string `yaml:"output-format"`
	// DPR — фиксированный DPR пресета (0 = не задан). Допустимы 1, 2, 3
	// (1 эквивалентен отсутствию).
	DPR int `yaml:"dpr"`
	// Quality — качество сжатия (0-100; 0 = default-quality из processing).
	Quality int `yaml:"quality"`
	// Frames — максимальное число кадров анимации (0 = без ограничения).
	Frames int `yaml:"frames"`
	// Duration — максимальная длительность анимации в мс (0 = без
	// ограничения).
	Duration int `yaml:"duration"`
	// Loop — зацикливание анимации (nil = default-loop из processing).
	Loop *bool `yaml:"loop"`
	// Watermark — имя ватермарки (ссылка на элемент секции watermarks;
	// пусто = не задана). Разрешается в спецификацию при компиляции:
	// неизвестное имя — ошибка старта. Приоритет выше path-policy и
	// processing.default-watermark.
	Watermark string `yaml:"watermark"`
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

	// Path-policies.
	seen := map[string]bool{}
	for i, pp := range cfg.PathPolicies {
		base := fmt.Sprintf("path-policies[%d]", i)
		norm := normalizePath(pp.Path)
		if norm == "" {
			errs = append(errs, &ValidationError{Path: base + ".path", Reason: "path is empty"})
		} else if seen[norm] {
			errs = append(errs, &ValidationError{Path: base + ".path", Reason: fmt.Sprintf("duplicate path %q (after normalization)", norm)})
		}
		seen[norm] = true
		if pp.DPR != "" {
			r, err := ParseDPRRange(pp.DPR)
			if err != nil {
				errs = append(errs, &ValidationError{Path: base + ".dpr", Reason: err.Error()})
			} else if err := validateDPRRange(r); err != nil {
				errs = append(errs, &ValidationError{Path: base + ".dpr", Reason: err.Error()})
			}
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
		if _, err := asset.ParseSize(p.Size); err != nil {
			errs = append(errs, &ValidationError{Path: base + ".size", Reason: err.Error()})
		}
		if p.OutputFormat == "" {
			errs = append(errs, &ValidationError{Path: base + ".output-format", Reason: "output format is empty"})
		}
		if p.DPR != 0 {
			if p.DPR < asset.DefaultDPR || p.DPR > asset.MaxDPR {
				errs = append(errs, &ValidationError{
					Path:   base + ".dpr",
					Reason: fmt.Sprintf("dpr must be in [%d,%d], got %d", asset.DefaultDPR, asset.MaxDPR, p.DPR),
				})
			}
		}
		if p.Quality < 0 || p.Quality > 100 {
			errs = append(errs, &ValidationError{
				Path:   base + ".quality",
				Reason: fmt.Sprintf("quality must be in [0,100], got %d", p.Quality),
			})
		}
		if p.Frames < 0 {
			errs = append(errs, &ValidationError{
				Path:   base + ".frames",
				Reason: fmt.Sprintf("frames must be non-negative, got %d", p.Frames),
			})
		}
		if p.Duration < 0 {
			errs = append(errs, &ValidationError{
				Path:   base + ".duration",
				Reason: fmt.Sprintf("duration must be non-negative, got %d", p.Duration),
			})
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
//
// watermarks — реестр скомпилированных спецификаций ватермарок по имени
// (строится из секции watermarks конфигурации). Имена watermark пресетов
// и path-policies разрешаются здесь; неизвестное имя — ошибка.
func Compile(cfg *Config, watermarks map[string]*processing.WatermarkSpec) (*Compiled, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	resolveWM := func(name, ctx string) (*processing.WatermarkSpec, error) {
		if name == "" {
			return nil, nil
		}
		wm, ok := watermarks[name]
		if !ok || wm == nil {
			return nil, fmt.Errorf("policy: %s: unknown watermark %q", ctx, name)
		}
		return wm, nil
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

	for i, pp := range cfg.PathPolicies {
		compiled := PathPolicy{
			Path: normalizePath(pp.Path),
			Crop: pp.Crop,
			Trim: pp.Trim,
		}
		if pp.DPR != "" {
			r, _ := ParseDPRRange(pp.DPR)
			compiled.DPR = &r
		}
		wm, err := resolveWM(pp.Watermark, fmt.Sprintf("path-policies[%d] (%s)", i, pp.Path))
		if err != nil {
			return nil, err
		}
		compiled.Watermark = wm
		policy.PathPolicies = append(policy.PathPolicies, compiled)
	}

	presets := make([]*asset.Preset, 0, len(cfg.Presets))
	for i, p := range cfg.Presets {
		size, _ := asset.ParseSize(p.Size)
		preset, err := asset.NewPreset(
			p.Name,
			transformFromCropTrim(p.Crop, p.Trim),
			size,
			asset.Format(p.OutputFormat),
			asset.DPR(p.DPR),
			p.Quality,
			p.Frames,
			p.Duration,
			p.Loop,
		)
		if err != nil {
			return nil, err
		}
		wm, err := resolveWM(p.Watermark, fmt.Sprintf("presets[%d] (%s)", i, p.Name))
		if err != nil {
			return nil, err
		}
		if wm != nil {
			preset = preset.WithWatermark(wm)
		}
		presets = append(presets, preset)
	}
	presetSet, err := asset.NewPresetSet(presets)
	if err != nil {
		return nil, err
	}

	return &Compiled{Policy: policy, Presets: presetSet}, nil
}

// transformFromCropTrim маппит булевы флаги crop/trim в Transform:
//
//	crop=true, trim=false → crop
//	crop=false, trim=true → trim
//	crop=true, trim=true → crop-trim
//	crop=false, trim=false → resize (пустой transform)
func transformFromCropTrim(crop, trim bool) asset.Transform {
	switch {
	case crop && trim:
		return asset.TransformCropTrim
	case crop:
		return asset.TransformCrop
	case trim:
		return asset.TransformTrim
	default:
		return ""
	}
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

// ParseDPRRange разбирает строку-диапазон DPR (например "0-1" или "2-3").
// Поддерживает одиночное значение ("2" → [2,2]) и диапазон "min-max".
func ParseDPRRange(s string) (Range, error) {
	if s == "" {
		return Range{}, fmt.Errorf("empty dpr range")
	}
	return parseRange(s)
}

// validateDPRRange проверяет, что диапазон DPR корректен: min>=0, max>=min,
// значения в [0, MaxDPR] (dpr не может быть больше 3).
func validateDPRRange(r Range) error {
	if r.Min < 0 {
		return fmt.Errorf("dpr range min must be non-negative, got %d", r.Min)
	}
	if r.Max < r.Min {
		return fmt.Errorf("dpr range min %d greater than max %d", r.Min, r.Max)
	}
	if r.Max > asset.MaxDPR {
		return fmt.Errorf("dpr range max %d exceeds maximum %d", r.Max, asset.MaxDPR)
	}
	return nil
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
