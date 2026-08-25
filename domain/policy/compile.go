package policy

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg-ru/dynamic"
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
	Authorization  dynamic.String   `yaml:"authorization"`
	SizeRules      []dynamic.String `yaml:"size-rules"`
	AllowedPresets []dynamic.String `yaml:"allowed-presets"`
	Limits         Limits           `yaml:"limits"`
}

// PathPolicyConfig — конфигурация path-policy (политики пути).
//
// Path — префикс пути. Нормализуется при компиляции (см. normalizePath):
// добавляется ведущий "/", убирается завершающий "/" (кроме "/").
//
// Path-policy применяется только к каноническим URL (не preset) и является
// дополнительным ограничением поверх глобальной политики.
type PathPolicyConfig struct {
	Path dynamic.String `yaml:"path"`
	// DPR — строка-диапазон допустимого DPR (nil/пусто = без ограничения).
	// Например "0-1" (только dpr=1) или "2-3" (dpr 2 или 3).
	DPR dynamic.String `yaml:"dpr"`
	// Crop — правило допустимых crop-режимов (nil = не задано/неважно).
	// ТОЛЬКО строковые формы (см. CropRuleConfig): имя режима
	// ("center"/"smart"/"face"/"object"), "none" (запрет любого кропа) или
	// список имён режимов. БОЛЕВЫЕ значения запрещены.
	Crop *CropRuleConfig `yaml:"crop"`
	// Trim — требование к trim (nil = не задано/неважно). true = trim обязан
	// присутствовать (код с trim), false = trim запрещён.
	Trim dynamic.Nullable[dynamic.Bool] `yaml:"trim"`
	// Watermark — имя ватермарки (ссылка на элемент секции watermarks;
	// пусто = не задана). Разрешается в спецификацию при компиляции:
	// неизвестное имя — ошибка старта.
	Watermark dynamic.String `yaml:"watermark"`
}

// PresetConfig — конфигурация пресета.
//
// Preset не содержит source-format: исходный формат определяется URL
// ({source_name}-{source_format}/{preset_name}.{output_format}).
//
// Имя пресета может содержать фиксированный @dpr-суффикс (например
// "thumb@2"). Поле dpr (если задано) имеет приоритет над @dpr в имени.
//
// crop — ТОЛЬКО строковый параметр, дефолт "" (кроп не используется):
//   - ""        — resize (только изменение размера)
//   - "center"  — центрированный кроп (transform c) — дефолт
//   - "smart"   — умный кроп (sc)
//   - "face"    — кроп по лицу (fc)
//   - "object"  — кроп по объекту (oc)
//
// trim — булев флаг независимого фильтра обрезки однотонных полей (false =
// не применять). Комбинация crop+trim кодируется в transform код URL:
// при trim=true — "t"/"ct"/"sct"/"fct"/"oct" (trim последним в коде), иначе —
// ""/"c"/"sc"/"fc"/"oc". Фактическое применение — сначала trim, затем кроп.
// Настройки trim (режим auto/color + tolerance) — глобальные
// (processing.default-trim-*), не per-preset.
type PresetConfig struct {
	Name         dynamic.String `yaml:"name"`
	Crop         dynamic.String `yaml:"crop"`
	Trim         dynamic.Bool   `yaml:"trim"`
	Size         dynamic.String `yaml:"size"`
	OutputFormat dynamic.String `yaml:"output-format"`
	// DPR — фиксированный DPR пресета (0 = не задан). Допустимы 1, 2, 3
	// (1 эквивалентен отсутствию).
	DPR dynamic.Int64 `yaml:"dpr"`
	// Quality — качество сжатия (0-100; 0 = default-quality из processing).
	Quality dynamic.Int64 `yaml:"quality"`
	// Frames — максимальное число кадров анимации (0 = без ограничения).
	Frames dynamic.Int64 `yaml:"frames"`
	// Duration — максимальная длительность анимации в мс (0 = без
	// ограничения).
	Duration dynamic.Int64 `yaml:"duration"`
	// Loop — зацикливание анимации (nil = default-loop из processing).
	Loop dynamic.Nullable[dynamic.Bool] `yaml:"loop"`
	// Watermark — имя ватермарки (ссылка на элемент секции watermarks;
	// пусто = не задана). Разрешается в спецификацию при компиляции:
	// неизвестное имя — ошибка старта. Приоритет выше path-policy и
	// processing.default-watermark.
	Watermark dynamic.String `yaml:"watermark"`
	// AutoOrient — EXIF auto-orient пресета (nil = наследовать глобальный
	// дефолт processing.default-auto-orient).
	AutoOrient dynamic.Nullable[dynamic.Bool] `yaml:"auto-orient"`
	// Rotate — фиксированный поворот пресета: ""/"90"/"180"/"270"/"none".
	// "" = наследовать глобальный дефолт processing.default-rotate;
	// "none" = ЯВНО отключить поворот (перекрыть глобальный).
	Rotate dynamic.String `yaml:"rotate"`
	// Flip — отражение пресета: ""/"horizontal"/"vertical"/"none".
	// "" = наследовать глобальный дефолт processing.default-flip;
	// "none" = ЯВНО отключить отражение (перекрыть глобальный).
	Flip dynamic.String `yaml:"flip"`
}

// CropRuleConfig — YAML-представление правила crop для path-policy.
//
// crop — ТОЛЬКО строковый параметр (или список строк); булевы значения
// (true/false) ЗАПРЕЩЕНЫ. Допустимые формы значения поля crop:
//
//	crop: center            # разрешён ТОЛЬКО центрированный кроп (c)
//	crop: smart             # разрешён ТОЛЬКО умный кроп (sc)
//	crop: face              # разрешён ТОЛЬКО кроп по лицу (fc)
//	crop: object            # разрешён ТОЛЬКО кроп по объекту (oc)
//	crop: none              # любой crop-режим запрещён (эквивалент "")
//	crop: [smart, face]     # разрешены только перечисленные режимы
//	crop: ""                # не задано (nil) — неважно
//
// Режим маппится в код URL "c"/"sc"/"fc"/"oc". Trim — отдельный булев
// параметр path-policy (Trim), не являющийся частью CropRuleConfig.
// Пустой список невалиден (используйте none для запрета). Неизвестное
// значение — ошибка компиляции конфигурации.
type CropRuleConfig []string

// UnmarshalYAML реализует гибкое декодирование поля crop: скалярная строка
// или список строк сводятся к единому представлению — списку имён режимов.
// БУЛЕВЫЕ значения НЕ ПРИНИМАЮТСЯ: crop — только строка. Отсутствие значения
// (null) не вызывает unmarshaler: поле остаётся nil («не ограничено»).
func (c *CropRuleConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		if s == "" {
			*c = nil
			return nil
		}
		*c = []string{s}
		return nil
	}
	var list []string
	if err := unmarshal(&list); err != nil {
		return fmt.Errorf("crop must be a mode name or a list of mode names (boolean values are not supported)")
	}
	*c = list
	return nil
}

// compileCropRule компилирует CropRuleConfig в доменное *CropRule.
//
// Формы:
//   - nil (поле не задано / пустая строка) → nil (без ограничения);
//   - ["none"] — явный запрет любого crop-режима: чёрный список всех
//     crop-кодов (включая trim-варианты);
//   - список режимов → белый список кодов. Режим КРОПА автоматически
//     покрывает свой trim-вариант (например "center" → c и ct): trim —
//     независимый фильтр, а не отдельный режим кропа, поэтому отдельно
//     указывать "center+t" не нужно.
func compileCropRule(cfg CropRuleConfig) (*CropRule, error) {
	if cfg == nil {
		return nil, nil
	}
	if len(cfg) == 0 {
		return nil, fmt.Errorf("policy: crop rule is empty")
	}
	codes := make([]asset.Transform, 0, len(cfg)*2)
	for _, name := range cfg {
		switch name {
		case "center":
			codes = append(codes, asset.TransformCrop, asset.TransformCropTrim)
		case "smart":
			codes = append(codes, asset.TransformSmartCrop, asset.TransformSmartCropTrim)
		case "face":
			codes = append(codes, asset.TransformFaceCrop, asset.TransformFaceCropTrim)
		case "object":
			codes = append(codes, asset.TransformObjectCrop, asset.TransformObjectCropTrim)
		case "none":
			// Явный запрет любого crop-режима (и его trim-вариантов).
			return NewCropDenyList(
				asset.TransformCrop, asset.TransformCropTrim,
				asset.TransformSmartCrop, asset.TransformSmartCropTrim,
				asset.TransformFaceCrop, asset.TransformFaceCropTrim,
				asset.TransformObjectCrop, asset.TransformObjectCropTrim,
			), nil
		default:
			return nil, fmt.Errorf("policy: invalid crop mode %q, must be one of: center, smart, face, object", name)
		}
	}
	return NewCropAllowList(codes...), nil
}

// derefCropRuleConfig безопасно разыменовывает указатель на CropRuleConfig
// (nil → nil-правило «без ограничения»).
func derefCropRuleConfig(cfg *CropRuleConfig) CropRuleConfig {
	if cfg == nil {
		return nil
	}
	return *cfg
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
	if cfg.Global.Authorization.Unwrap() != "" && !ValidAuthorization(Authorization(cfg.Global.Authorization.Unwrap())) {
		errs = append(errs, &ValidationError{
			Path:   "global.authorization",
			Reason: fmt.Sprintf("invalid value %q, must be safe or unsafe", cfg.Global.Authorization.Unwrap()),
		})
	}
	for i, rule := range cfg.Global.SizeRules {
		if _, err := ParseSizeRule(rule.Unwrap()); err != nil {
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
		norm := normalizePath(pp.Path.Unwrap())
		if norm == "" {
			errs = append(errs, &ValidationError{Path: base + ".path", Reason: "path is empty"})
		} else if seen[norm] {
			errs = append(errs, &ValidationError{Path: base + ".path", Reason: fmt.Sprintf("duplicate path %q (after normalization)", norm)})
		}
		seen[norm] = true
		if pp.DPR.Unwrap() != "" {
			r, err := ParseDPRRange(pp.DPR.Unwrap())
			if err != nil {
				errs = append(errs, &ValidationError{Path: base + ".dpr", Reason: err.Error()})
			} else if err := validateDPRRange(r); err != nil {
				errs = append(errs, &ValidationError{Path: base + ".dpr", Reason: err.Error()})
			}
		}
		if pp.Crop != nil {
			if _, err := compileCropRule(*pp.Crop); err != nil {
				errs = append(errs, &ValidationError{Path: base + ".crop", Reason: err.Error()})
			}
		}
	}

	// Presets.
	presetNames := map[string]bool{}
	for i, p := range cfg.Presets {
		base := fmt.Sprintf("presets[%d]", i)
		name := p.Name.Unwrap()
		crop := p.Crop.Unwrap()
		outputFormat := p.OutputFormat.Unwrap()
		rotate := p.Rotate.Unwrap()
		flip := p.Flip.Unwrap()
		if name == "" {
			errs = append(errs, &ValidationError{Path: base + ".name", Reason: "preset name is empty"})
		} else if presetNames[name] {
			errs = append(errs, &ValidationError{Path: base + ".name", Reason: fmt.Sprintf("duplicate preset %q", name)})
		}
		presetNames[name] = true
		switch crop {
		case "", "center", "smart", "face", "object":
		default:
			errs = append(errs, &ValidationError{
				Path:   base + ".crop",
				Reason: fmt.Sprintf("invalid value %q, must be one of: center, smart, face, object (empty = no crop)", crop),
			})
		}
		if _, err := asset.ParseSize(p.Size.Unwrap()); err != nil {
			errs = append(errs, &ValidationError{Path: base + ".size", Reason: err.Error()})
		}
		if outputFormat == "" {
			errs = append(errs, &ValidationError{Path: base + ".output-format", Reason: "output format is empty"})
		}
		dpr := p.DPR.Unwrap()
		if dpr != 0 {
			if dpr < int64(asset.DefaultDPR) || dpr > int64(asset.MaxDPR) {
				errs = append(errs, &ValidationError{
					Path:   base + ".dpr",
					Reason: fmt.Sprintf("dpr must be in [%d,%d], got %d", asset.DefaultDPR, asset.MaxDPR, dpr),
				})
			}
		}
		quality := p.Quality.Unwrap()
		if quality < 0 || quality > 100 {
			errs = append(errs, &ValidationError{
				Path:   base + ".quality",
				Reason: fmt.Sprintf("quality must be in [0,100], got %d", quality),
			})
		}
		frames := p.Frames.Unwrap()
		if frames < 0 {
			errs = append(errs, &ValidationError{
				Path:   base + ".frames",
				Reason: fmt.Sprintf("frames must be non-negative, got %d", frames),
			})
		}
		duration := p.Duration.Unwrap()
		if duration < 0 {
			errs = append(errs, &ValidationError{
				Path:   base + ".duration",
				Reason: fmt.Sprintf("duration must be non-negative, got %d", duration),
			})
		}
		if _, err := processing.ParseRotation(rotate); err != nil {
			errs = append(errs, &ValidationError{Path: base + ".rotate", Reason: err.Error()})
		}
		if _, err := processing.ParseFlip(flip); err != nil {
			errs = append(errs, &ValidationError{Path: base + ".flip", Reason: err.Error()})
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
//
// defaultOrientation — глобальная ориентация по умолчанию
// (processing.default-auto-orient/rotate/flip). Пресеты наследуют её
// по-полево: явные значения пресета (auto-orient/rotate/flip) перекрывают
// глобальные; пустые строки rotate/flip и nil auto-orient означают
// «наследовать», значение "none" — «явно отключить». nil defaultOrientation
// эквивалентен {AutoOrient: true}.
func Compile(cfg *Config, watermarks map[string]*processing.WatermarkSpec, defaultOrientation *processing.OrientationSpec) (*Compiled, error) {
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

	allowedPresets := make([]string, 0, len(cfg.Global.AllowedPresets))
	for _, ap := range cfg.Global.AllowedPresets {
		allowedPresets = append(allowedPresets, ap.Unwrap())
	}
	policy := &Policy{
		Global: GlobalPolicy{
			Authorization:  Authorization(cfg.Global.Authorization.Unwrap()),
			SizeRules:      make([]SizeRule, 0, len(cfg.Global.SizeRules)),
			AllowedPresets: allowedPresets,
			Limits:         cfg.Global.Limits,
		},
	}
	for _, r := range cfg.Global.SizeRules {
		rule, _ := ParseSizeRule(r.Unwrap())
		policy.Global.SizeRules = append(policy.Global.SizeRules, rule)
	}

	for i, pp := range cfg.PathPolicies {
		cropRule, err := compileCropRule(derefCropRuleConfig(pp.Crop))
		if err != nil {
			return nil, fmt.Errorf("policy: path-policies[%d] (%s): %w", i, pp.Path.Unwrap(), err)
		}
		var trim *bool
		if pp.Trim.Set {
			v := pp.Trim.Value.Unwrap()
			trim = &v
		}
		compiled := PathPolicy{
			Path: normalizePath(pp.Path.Unwrap()),
			Crop: cropRule,
			Trim: trim,
		}
		if pp.DPR.Unwrap() != "" {
			r, _ := ParseDPRRange(pp.DPR.Unwrap())
			compiled.DPR = &r
		}
		wm, err := resolveWM(pp.Watermark.Unwrap(), fmt.Sprintf("path-policies[%d] (%s)", i, pp.Path.Unwrap()))
		if err != nil {
			return nil, err
		}
		compiled.Watermark = wm
		policy.PathPolicies = append(policy.PathPolicies, compiled)
	}

	presets := make([]*asset.Preset, 0, len(cfg.Presets))
	for i, p := range cfg.Presets {
		size, _ := asset.ParseSize(p.Size.Unwrap())
		var loop *bool
		if p.Loop.Set {
			v := p.Loop.Value.Unwrap()
			loop = &v
		}
		preset, err := asset.NewPreset(
			p.Name.Unwrap(),
			transformFromCropTrim(p.Crop.Unwrap(), p.Trim.Unwrap()),
			size,
			asset.Format(p.OutputFormat.Unwrap()),
			asset.DPR(int(p.DPR.Unwrap())),
			int(p.Quality.Unwrap()),
			int(p.Frames.Unwrap()),
			int(p.Duration.Unwrap()),
			loop,
		)
		if err != nil {
			return nil, err
		}
		wm, err := resolveWM(p.Watermark.Unwrap(), fmt.Sprintf("presets[%d] (%s)", i, p.Name.Unwrap()))
		if err != nil {
			return nil, err
		}
		if wm != nil {
			preset = preset.WithWatermark(wm)
		}
		preset = preset.WithOrientation(mergePresetOrientation(p, defaultOrientation))
		presets = append(presets, preset)
	}
	presetSet, err := asset.NewPresetSet(presets)
	if err != nil {
		return nil, err
	}

	return &Compiled{Policy: policy, Presets: presetSet}, nil
}

// transformFromCropTrim маппит строковый режим crop и булев флаг trim
// (независимые фильтры) в трансформационный код URL:
//
//	crop="",        trim=false → ""            (resize, без кропа)
//	crop="center",  trim=false → "c"           (центрированный кроп)
//	crop="smart",   trim=false → "sc" (smart-crop)
//	crop="face",    trim=false → "fc" (face-crop)
//	crop="object",  trim=false → "oc" (object-crop)
//	crop="",        trim=true  → "t" (только trim)
//	crop="center",  trim=true  → "ct" (c + trim; применяется trim, затем кроп)
//	crop="smart",   trim=true  → "sct"
//	crop="face",    trim=true  → "fct"
//	crop="object",  trim=true  → "oct"
//
// Trim в коде всегда стоит ПОСЛЕДНИМ; фактическое применение — сначала trim,
// затем кроп.
func transformFromCropTrim(crop string, trim bool) asset.Transform {
	switch crop {
	case "center":
		if trim {
			return asset.TransformCropTrim
		}
		return asset.TransformCrop
	case "smart":
		if trim {
			return asset.TransformSmartCropTrim
		}
		return asset.TransformSmartCrop
	case "face":
		if trim {
			return asset.TransformFaceCropTrim
		}
		return asset.TransformFaceCrop
	case "object":
		if trim {
			return asset.TransformObjectCropTrim
		}
		return asset.TransformObjectCrop
	default: // "" — кроп не используется
		if trim {
			return asset.TransformTrim
		}
		return ""
	}
}

// mergePresetOrientation мержит ориентационные поля пресета с глобальным
// дефолтом. Семантика по-полевая:
//   - AutoOrient: nil пресета → значение дефолта; явное значение перекрывает;
//   - Rotate: "" пресета → значение дефолта; "none" → явно отключено
//     (RotationNone даже при заданном глобальном); иначе — значение пресета;
//   - Flip: аналогично Rotate.
//
// defaultOrientation nil эквивалентен {AutoOrient: true}. Значения пресета
// валидированы в ValidateConfig, поэтому ошибки здесь невозможны; при
// некорректной комбинации возвращается ошибка компиляции.
func mergePresetOrientation(p PresetConfig, def *processing.OrientationSpec) *processing.OrientationSpec {
	if def == nil {
		def = processing.DefaultOrientation()
	}
	autoOrient := def.AutoOrient
	if p.AutoOrient.Set {
		autoOrient = p.AutoOrient.Value.Unwrap()
	}
	rotate := def.Rotate
	pr := p.Rotate.Unwrap()
	if r, err := processing.ParseRotation(pr); err == nil && pr != "" {
		rotate = r
	}
	flip := def.Flip
	pf := p.Flip.Unwrap()
	if f, err := processing.ParseFlip(pf); err == nil && pf != "" {
		flip = f
	}
	spec, err := processing.NewOrientationSpec(autoOrient, rotate, flip)
	if err != nil {
		// Не должно случиться после ValidateConfig: значения уже проверены.
		return def
	}
	return spec
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
