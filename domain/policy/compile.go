package policy

import (
	"fmt"
	"strings"

	"github.com/pkg-ru/dynamic"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/encoding"
	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// Config — конфигурация политики (DTO, не связан с YAML).
//
// PathPolicies — map: ключ = путь (префикс), значение = настройки пути.
// "/" — fallback, применяется ко всем путям, если нет более специфичного
// совпадения (longest-prefix match).
//
// Presets — map: ключ = имя пресета (уникальность обеспечивается самим
// map), значение = настройки пресета. Поиск пресета по имени — O(1).
type Config struct {
	PathPolicies map[string]PathPolicyConfig `yaml:"path-policies"`
	Presets      map[string]PresetConfig     `yaml:"presets"`
	// LearningMode — флаг режима обучения (learning-mode): при включении
	// сервис генерирует ассеты, не подходящие по правилам, но не сохраняет
	// их в storage, и пополняет path-policies на основе наблюдаемых URL.
	LearningMode dynamic.Bool `yaml:"learning-mode"`
}

// PathPolicyConfig — конфигурация path-policy (политики пути).
//
// Presets — имена глобальных пресетов, доступных на этом пути.
// Customs — custom-настройки: ключ = имя custom (размер-грамматика
// "x"/"x200"/"200x"/"200x200", опционально с @dpr-суффиксом, например
// "200x100@2"), значение = настройки как у пресетов.
type PathPolicyConfig struct {
	Presets dynamic.StringSlice     `yaml:"presets"`
	Customs map[string]PresetConfig `yaml:"customs"`
}

// PresetConfig — конфигурация пресета/custom.
//
// Preset не содержит source-format: исходный формат определяется URL
// ({source_name}-{source_format}/{segment}.{output_format}).
//
// Имя пресета задаётся КЛЮЧОМ map в policy.presets (поля name нет).
// Имя может содержать фиксированный @dpr-суффикс (например "banner@2").
// Поле dpr (если задано) имеет приоритет над @dpr в имени.
//
// DPR — dynamic.Nullable[dynamic.Uint32]: Set=true означает «ключ dpr
// присутствует» (даже 0/1), Set=false — «ключ отсутствует». Это различает
// «dpr не задан» (разрешены URL с @dpr-суффиксом) от «dpr задан» (суффикс
// в URL запрещён, кроме случая, когда имя пресета содержит тот же @dpr).
//
// crop — ТОЛЬКО строковый параметр, дефолт "" (кроп не используется):
//   - ""        — resize (только изменение размера)
//   - "center"  — центрированный кроп (transform c)
//   - "smart"   — умный кроп (sc)
//   - "face"    — кроп по лицу (fc)
//   - "object"  — кроп по объекту (oc)
//
// trim — булев флаг независимого фильтра обрезки однотонных полей (false =
// не применять). Комбинация crop+trim кодируется в transform код:
// при trim=true — "t"/"ct"/"sct"/"fct"/"oct", иначе — ""/"c"/"sc"/"fc"/"oc".
// Фактическое применение — сначала trim, затем кроп.
//
// Native-параметры форматов задаются ПЛОСКО, на одном уровне с quality:
// ключ = "{формат}-{параметр}" (kebab-case), где {параметр} — ключ реестра
// domain/encoding без префикса. Имена нативных параметров уникальны между
// форматами, поэтому вложенные группы не требуются. Пример:
//
//	presets:
//	  thumb:
//	    width: 200
//	    quality: 85
//	    png-compression-level: 9   # native override PNG
//	    webp-reduction-effort: 6   # native override WebP
//	    webp-quality: 90           # per-format quality override WebP
//
// Per-format quality (jpeg-quality/webp-quality/avif-quality/heif-quality/
// jxl-quality) допустим только для lossy-форматов и переопределяет скалярный
// quality для этого формата. Lossless-форматам (png/apng/gif) quality-ключи
// формата НЕ задаются (png-quality и т.п. отсутствуют в структуре — их
// задание даёт неизвестное поле при строгом парсинге). Указатели отличают
// «не задано» от нулевого значения.
type PresetConfig struct {
	Width         dynamic.Uint32                   `yaml:"width"`
	Height        dynamic.Uint32                   `yaml:"height"`
	OutputFormats dynamic.StringSlice              `yaml:"output-formats"`
	DPR           dynamic.Nullable[dynamic.Uint32] `yaml:"dpr"`
	Crop          dynamic.String                   `yaml:"crop"`
	Trim          dynamic.Bool                     `yaml:"trim"`
	Quality       dynamic.Uint32                   `yaml:"quality"`
	Frames        dynamic.Uint32                   `yaml:"frames"`
	Duration      dynamic.Uint32                   `yaml:"duration"`
	Loop          dynamic.Nullable[dynamic.Bool]   `yaml:"loop"`
	Watermark     dynamic.String                   `yaml:"watermark"`
	AutoOrient    dynamic.Nullable[dynamic.Bool]   `yaml:"auto-orient"`
	Rotate        dynamic.String                   `yaml:"rotate"`
	Flip          dynamic.String                   `yaml:"flip"`

	// Native-параметры форматов (плоские, kebab-case; nil = не задан).
	// JPEG.
	JPEGQuality     *int  `yaml:"jpeg-quality"`
	JPEGProgressive *bool `yaml:"jpeg-progressive"`
	// WebP.
	WebPQuality         *int  `yaml:"webp-quality"`
	WebPReductionEffort *int  `yaml:"webp-reduction-effort"`
	WebPLossless        *bool `yaml:"webp-lossless"`
	WebPNearLossless    *bool `yaml:"webp-near-lossless"`
	// AVIF.
	AVIFQuality  *int  `yaml:"avif-quality"`
	AVIFSpeed    *int  `yaml:"avif-speed"`
	AVIFLossless *bool `yaml:"avif-lossless"`
	// HEIF.
	HEIFQuality *int `yaml:"heif-quality"`
	// JPEG XL.
	JXLQuality  *int  `yaml:"jxl-quality"`
	JXLEffort   *int  `yaml:"jxl-effort"`
	JXLLossless *bool `yaml:"jxl-lossless"`
	// PNG (lossless: quality только из скаляра).
	PNGCompressionLevel *int     `yaml:"png-compression-level"`
	PNGInterlace        *bool    `yaml:"png-interlace"`
	PNGPalette          *bool    `yaml:"png-palette"`
	PNGPaletteColors    *int     `yaml:"png-palette-colors"`
	PNGPaletteBitDepth  *int     `yaml:"png-palette-bit-depth"`
	PNGDither           *float64 `yaml:"png-dither"`
	// APNG (lossless: quality только из скаляра).
	APNGCompressionLevel *int  `yaml:"apng-compression-level"`
	APNGInterlace        *bool `yaml:"apng-interlace"`
	// GIF (lossless: quality только из скаляра).
	GIFEffort   *int     `yaml:"gif-effort"`
	GIFBitDepth *int     `yaml:"gif-bit-depth"`
	GIFDither   *float64 `yaml:"gif-dither"`
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
//
// Проверяются:
//   - непустые имена пресетов (уникальность обеспечивается map);
//   - валидность путей (нормализация "/prefix", "/" допустим, без дубликатов);
//   - валидность custom-имён (размер-грамматика x/x200/200x/200x200,
//     опционально с @2/@3);
//   - конфликт dpr в имени vs настройки (banner@2 + dpr: 3 — ошибка);
//   - суффиксы @0/@1 в имени — ошибка;
//   - output-formats непустой и допустимые форматы;
//   - размеры width/height ≤ MaxDimension;
//   - quality в [0,100].
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return &ValidationError{Path: "", Reason: "config is nil"}
	}
	var errs ValidationErrors

	// Path-policies: валидность путей (нормализация, дубликаты).
	seen := map[string]bool{}
	for path := range cfg.PathPolicies {
		norm := normalizePath(path)
		if norm == "" {
			errs = append(errs, &ValidationError{Path: "path-policies." + path, Reason: "path is empty"})
			continue
		}
		if seen[norm] {
			errs = append(errs, &ValidationError{
				Path:   "path-policies." + path,
				Reason: fmt.Sprintf("duplicate path %q (after normalization)", norm),
			})
		}
		seen[norm] = true
	}

	// Presets: имя = ключ map. Обход в отсортированном порядке —
	// детерминированный список ошибок валидации. Непустой map имён
	// включает проверки имени (пустое имя, @0/@1, конфликт dpr).
	presetKeys := make([]string, 0, len(cfg.Presets))
	for name := range cfg.Presets {
		presetKeys = append(presetKeys, name)
	}
	sortStrings(presetKeys)
	for _, name := range presetKeys {
		base := "presets." + name
		validatePresetConfig(&errs, base, name, cfg.Presets[name], map[string]bool{})
	}

	// Customs (в path-policies): имя = ключ, размер-грамматика.
	for path, pp := range cfg.PathPolicies {
		base := fmt.Sprintf("path-policies.%s.customs", path)
		for cname, cc := range pp.Customs {
			cb := base + "." + cname
			// Имя custom: размер-грамматика + опциональный @dpr.
			baseName, nameDPR, err := asset.SplitPresetNameDPR(cname)
			if err != nil {
				errs = append(errs, &ValidationError{Path: cb, Reason: err.Error()})
				continue
			}
			if nameDPR.Int() == asset.DefaultDPR {
				errs = append(errs, &ValidationError{
					Path:   cb,
					Reason: fmt.Sprintf("custom name %q: dpr suffix @1 is not allowed", cname),
				})
			}
			if _, err := asset.ParseSize(baseName); err != nil {
				errs = append(errs, &ValidationError{
					Path:   cb,
					Reason: fmt.Sprintf("custom name %q is not a valid size: %v", cname, err),
				})
			}
			// Соответствие @N-суффикса имени и поля dpr (те же правила, что
			// для пресетов): имя с @N (2/3) требует dpr == N (отсутствие или
			// другое значение — ошибка конфигурации); имя без @N — wildcard:
			// dpr не фиксирован, допустим любой @dpr в URL, а если поле dpr
			// задано — только dpr: 1.
			validateNameDPR(&errs, cb, "custom", cname, cc)
			validatePresetConfig(&errs, cb, cname, cc, nil)
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// validatePresetConfig валидирует общие поля пресета/custom.
// name — имя пресета/custom (ключ map). presetNames — карта уже
// встреченных имён пресетов (nil для customs).
func validatePresetConfig(errs *ValidationErrors, base, name string, p PresetConfig, presetNames map[string]bool) {
	crop := p.Crop.Unwrap()
	rotate := p.Rotate.Unwrap()
	flip := p.Flip.Unwrap()

	if presetNames != nil {
		if name == "" {
			*errs = append(*errs, &ValidationError{Path: base + ".name", Reason: "preset name is empty"})
		} else if presetNames[name] {
			*errs = append(*errs, &ValidationError{Path: base + ".name", Reason: fmt.Sprintf("duplicate preset %q", name)})
		}
		presetNames[name] = true
		// Имя пресета: @0/@1 запрещены, @2/@3 допустимы.
		_, nameDPR, err := asset.SplitPresetNameDPR(name)
		if err != nil {
			*errs = append(*errs, &ValidationError{Path: base + ".name", Reason: err.Error()})
		} else if nameDPR.Int() == asset.DefaultDPR {
			*errs = append(*errs, &ValidationError{
				Path:   base + ".name",
				Reason: fmt.Sprintf("preset name %q: dpr suffix @1 is not allowed", name),
			})
		} else {
			// Соответствие @N-суффикса имени и поля dpr: имя с @N (2/3)
			// требует dpr == N; без @N допустимо только dpr: 1.
			validateNameDPR(errs, base, "preset", name, p)
		}
	}

	switch crop {
	case "", "center", "smart", "face", "object":
	default:
		*errs = append(*errs, &ValidationError{
			Path:   base + ".crop",
			Reason: fmt.Sprintf("invalid value %q, must be one of: center, smart, face, object (empty = no crop)", crop),
		})
	}

	// Размеры: width/height ≤ MaxDimension.
	if w := p.Width.Unwrap(); w > asset.MaxDimension {
		*errs = append(*errs, &ValidationError{
			Path:   base + ".width",
			Reason: fmt.Sprintf("width %d exceeds maximum %d", w, asset.MaxDimension),
		})
	}
	if h := p.Height.Unwrap(); h > asset.MaxDimension {
		*errs = append(*errs, &ValidationError{
			Path:   base + ".height",
			Reason: fmt.Sprintf("height %d exceeds maximum %d", h, asset.MaxDimension),
		})
	}

	// Output-format: непустой список допустимых форматов.
	if len(p.OutputFormats) == 0 {
		*errs = append(*errs, &ValidationError{Path: base + ".output-formats", Reason: "output format list is empty"})
	}
	for i, f := range p.OutputFormats {
		if _, err := asset.NewFormat(f.Unwrap()); err != nil {
			*errs = append(*errs, &ValidationError{
				Path:   fmt.Sprintf("%s.output-formats[%d]", base, i),
				Reason: err.Error(),
			})
		}
	}

	// DPR: если задан, значение в [0,3] (0/1 = без умножения).
	if p.DPR.Set {
		dpr := p.DPR.Value.Unwrap()
		if dpr > asset.MaxDPR {
			*errs = append(*errs, &ValidationError{
				Path:   base + ".dpr",
				Reason: fmt.Sprintf("dpr must be in [0,%d], got %d", asset.MaxDPR, dpr),
			})
		}
	}

	if q := p.Quality.Unwrap(); q > 100 {
		*errs = append(*errs, &ValidationError{
			Path:   base + ".quality",
			Reason: fmt.Sprintf("quality must be in [0,100], got %d", q),
		})
	}

	if _, err := processing.ParseRotation(rotate); err != nil {
		*errs = append(*errs, &ValidationError{Path: base + ".rotate", Reason: err.Error()})
	}
	if _, err := processing.ParseFlip(flip); err != nil {
		*errs = append(*errs, &ValidationError{Path: base + ".flip", Reason: err.Error()})
	}

	// Native-параметры кодирования (плоские ключи "<формат>-<параметр>"):
	// собираются в overrides и валидируются по реестру domain/encoding
	// (известный формат/параметр, диапазон, per-format quality только для
	// lossy-форматов). Ошибки — fail-fast на этапе ValidateConfig.
	if _, err := encodingOverridesFromConfig(p); err != nil {
		*errs = append(*errs, &ValidationError{Path: base, Reason: err.Error()})
	}
}

// validateNameDPR проверяет соответствие фиксированного @N-суффикса имени
// пресета/custom и поля dpr в настройках. Правила одинаковы для пресетов
// и customs:
//
//   - имя с суффиксом @N (N = 2/3) жёстко фиксирует dpr: поле dpr ОБЯЗАНО
//     присутствовать и быть РАВНО N. Отсутствие dpr или другое значение —
//     ошибка конфигурации;
//   - имя без суффикса @N — wildcard-dpr: dpr не закреплён в имени, в URL
//     допустим любой @N. Если поле dpr задано, единственное допустимое
//     значение — 1 (фиксированный множитель 1; @dpr в URL запрещён).
//
// kind используется в сообщениях об ошибках ("preset"/"custom"). Суффиксы
// @0 (ошибка парсинга) и @1 обрабатываются вызывающим кодом.
func validateNameDPR(errs *ValidationErrors, base, kind, name string, p PresetConfig) {
	_, nameDPR, err := asset.SplitPresetNameDPR(name)
	if err != nil || nameDPR.Int() == asset.DefaultDPR {
		return // ошибки суффикса обрабатываются вызывающим кодом
	}
	if nameDPR != 0 {
		// Имя с суффиксом @N: поле dpr обязательно и должно быть РАВНО N.
		if !p.DPR.Set {
			*errs = append(*errs, &ValidationError{
				Path:   base + ".dpr",
				Reason: fmt.Sprintf("%s name %q: dpr is required for @%d suffix (set dpr: %d)", kind, name, nameDPR.Int(), nameDPR.Int()),
			})
			return
		}
		if asset.DPR(p.DPR.Value.Unwrap()) != nameDPR {
			*errs = append(*errs, &ValidationError{
				Path:   base + ".dpr",
				Reason: fmt.Sprintf("%s name %q: dpr %d conflicts with dpr %d in name (must be equal)", kind, name, p.DPR.Value.Unwrap(), nameDPR.Int()),
			})
		}
		return
	}
	// Имя без суффикса @N: допустимо только dpr: 1.
	if p.DPR.Set {
		if v := p.DPR.Value.Unwrap(); v != uint32(asset.DefaultDPR) {
			*errs = append(*errs, &ValidationError{
				Path:   base + ".dpr",
				Reason: fmt.Sprintf("%s name %q: dpr %d is not allowed without @dpr suffix (only dpr: %d)", kind, name, v, asset.DefaultDPR),
			})
		}
	}
}

// Compiled — результат компиляции политики.
type Compiled struct {
	Policy  *Policy
	Presets *asset.PresetSet
}

// Compile собирает Policy и глобальный PresetSet из валидированной
// конфигурации.
//
// watermarks — реестр скомпилированных спецификаций ватермарок по имени
// (строится из секции watermarks конфигурации). Имена watermark пресетов
// и customs разрешаются здесь; неизвестное имя — ошибка.
//
// defaultOrientation — глобальная ориентация по умолчанию
// (processing.default-auto-orient/rotate/flip). Пресеты наследуют её
// по-полево: явные значения пресета (auto-orient/rotate/flip) перекрывают
// глобальные; пустые строки rotate/flip и nil auto-orient означают
// «наследовать», значение "none" — «явно отключить». nil defaultOrientation
// эквивалентен {AutoOrient: true}.
func Compile(cfg Config, watermarks map[string]*processing.WatermarkSpec, defaultOrientation *processing.OrientationSpec) (*Compiled, error) {
	if err := ValidateConfig(&cfg); err != nil {
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

	// Глобальные пресеты: имя = ключ map (поиск по имени — O(1) в
	// PresetSet). Обход в отсортированном порядке — детерминированные
	// ошибки компиляции.
	presetKeys := make([]string, 0, len(cfg.Presets))
	for name := range cfg.Presets {
		presetKeys = append(presetKeys, name)
	}
	sortStrings(presetKeys)
	presets := make([]*asset.Preset, 0, len(cfg.Presets))
	for _, name := range presetKeys {
		preset, err := compilePreset(name, cfg.Presets[name], resolveWM, defaultOrientation)
		if err != nil {
			return nil, fmt.Errorf("policy: presets.%s: %w", name, err)
		}
		presets = append(presets, preset)
	}
	presetSet, err := asset.NewPresetSet(presets)
	if err != nil {
		return nil, err
	}

	// Path-policies.
	paths := make(map[string]*PathPolicy, len(cfg.PathPolicies))
	for path, pp := range cfg.PathPolicies {
		norm := normalizePath(path)
		compiled := &PathPolicy{Path: norm, Customs: make(map[string]*asset.Preset, len(pp.Customs))}

		// Пресеты пути — подмножество глобальных по именам.
		pathPresets := make([]*asset.Preset, 0, len(pp.Presets))
		for _, name := range pp.Presets {
			n := name.Unwrap()
			pr, ok := presetSet.Get(n)
			if !ok {
				return nil, fmt.Errorf("policy: path %q: unknown preset %q", norm, n)
			}
			pathPresets = append(pathPresets, pr)
		}
		ps, err := asset.NewPresetSet(pathPresets)
		if err != nil {
			return nil, fmt.Errorf("policy: path %q: %w", norm, err)
		}
		compiled.Presets = ps

		// Customs: имя = ключ (размер-грамматика, опционально с @dpr).
		for cname, cc := range pp.Customs {
			preset, err := compileCustom(cname, cc, resolveWM, defaultOrientation)
			if err != nil {
				return nil, fmt.Errorf("policy: path %q: custom %q: %w", norm, cname, err)
			}
			compiled.Customs[cname] = preset
		}
		paths[norm] = compiled
	}

	ordered := make([]string, 0, len(paths))
	for k := range paths {
		ordered = append(ordered, k)
	}
	sortStrings(ordered)

	return &Compiled{
		Policy:  &Policy{paths: paths, orderedPaths: ordered, presets: presetSet},
		Presets: presetSet,
	}, nil
}

// compilePreset собирает asset.Preset из конфигурации пресета.
func compilePreset(name string, cfg PresetConfig, resolveWM func(string, string) (*processing.WatermarkSpec, error), defaultOrientation *processing.OrientationSpec) (*asset.Preset, error) {
	size, err := sizeFromPreset(cfg)
	if err != nil {
		return nil, err
	}
	formats, err := formatsFromConfig(cfg.OutputFormats)
	if err != nil {
		return nil, err
	}
	dpr, dprSet := dprFromConfig(cfg.DPR)
	encOverrides, err := encodingOverridesFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("preset %q: %w", name, err)
	}
	preset, err := asset.NewPreset(
		name,
		transformFromCropTrim(cfg.Crop.Unwrap(), cfg.Trim.Unwrap()),
		size,
		formats,
		dpr,
		dprSet,
		int(cfg.Quality.Unwrap()),
		int(cfg.Frames.Unwrap()),
		int(cfg.Duration.Unwrap()),
		loopFromConfig(cfg.Loop),
		encOverrides,
	)
	if err != nil {
		return nil, err
	}
	wm, err := resolveWM(cfg.Watermark.Unwrap(), fmt.Sprintf("preset %q", name))
	if err != nil {
		return nil, err
	}
	if wm != nil {
		preset = preset.WithWatermark(wm)
	}
	preset = preset.WithOrientation(mergePresetOrientation(cfg, defaultOrientation))
	return preset, nil
}

// encodingOverridesFromConfig собирает map[формат]map[нативный ключ]значение
// из плоских нативных полей PresetConfig. Формат в карте — каноническое имя
// реестра (jpeg/webp/avif/heif/jxl/png/apng/gif). Возвращает nil, если ни
// одного нативного поля не задано.
//
// Ключи внутри формата — реестровые имена БЕЗ префикса (compression-level,
// reduction-effort, ...): они совпадают с ожиданиями encoding.Resolve.
// per-format quality (jpeg-quality и т.п.) ложится под ключом "quality".
func encodingOverridesFromConfig(cfg PresetConfig) (map[string]map[string]any, error) {
	type setup struct {
		format string
		params map[string]any
	}
	var out map[string]map[string]any
	put := func(s setup) {
		if len(s.params) == 0 {
			return
		}
		if out == nil {
			out = make(map[string]map[string]any)
		}
		out[s.format] = s.params
	}
	intPtr := func(p *int) any {
		if p == nil {
			return nil
		}
		return *p
	}
	boolPtr := func(p *bool) any {
		if p == nil {
			return nil
		}
		return *p
	}
	floatPtr := func(p *float64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	fill := func(m map[string]any, k string, v any) {
		if v != nil {
			m[k] = v
		}
	}

	// Канонические имена реестра без префикса формата (см. domain/encoding).
	jpeg := map[string]any{}
	fill(jpeg, "quality", intPtr(cfg.JPEGQuality))
	fill(jpeg, "progressive", boolPtr(cfg.JPEGProgressive))
	put(setup{"jpeg", jpeg})

	webp := map[string]any{}
	fill(webp, "quality", intPtr(cfg.WebPQuality))
	fill(webp, "reduction-effort", intPtr(cfg.WebPReductionEffort))
	fill(webp, "lossless", boolPtr(cfg.WebPLossless))
	fill(webp, "near-lossless", boolPtr(cfg.WebPNearLossless))
	put(setup{"webp", webp})

	avif := map[string]any{}
	fill(avif, "quality", intPtr(cfg.AVIFQuality))
	fill(avif, "speed", intPtr(cfg.AVIFSpeed))
	fill(avif, "lossless", boolPtr(cfg.AVIFLossless))
	put(setup{"avif", avif})

	heif := map[string]any{}
	fill(heif, "quality", intPtr(cfg.HEIFQuality))
	put(setup{"heif", heif})

	jxl := map[string]any{}
	fill(jxl, "quality", intPtr(cfg.JXLQuality))
	fill(jxl, "effort", intPtr(cfg.JXLEffort))
	fill(jxl, "lossless", boolPtr(cfg.JXLLossless))
	put(setup{"jxl", jxl})

	png := map[string]any{}
	fill(png, "compression-level", intPtr(cfg.PNGCompressionLevel))
	fill(png, "interlace", boolPtr(cfg.PNGInterlace))
	fill(png, "palette", boolPtr(cfg.PNGPalette))
	fill(png, "palette-colors", intPtr(cfg.PNGPaletteColors))
	fill(png, "palette-bit-depth", intPtr(cfg.PNGPaletteBitDepth))
	fill(png, "dither", floatPtr(cfg.PNGDither))
	put(setup{"png", png})

	apng := map[string]any{}
	fill(apng, "compression-level", intPtr(cfg.APNGCompressionLevel))
	fill(apng, "interlace", boolPtr(cfg.APNGInterlace))
	put(setup{"apng", apng})

	gif := map[string]any{}
	fill(gif, "effort", intPtr(cfg.GIFEffort))
	fill(gif, "bit-depth", intPtr(cfg.GIFBitDepth))
	fill(gif, "dither", floatPtr(cfg.GIFDither))
	put(setup{"gif", gif})

	for format, params := range out {
		if err := encoding.ValidateOverrides(format, params); err != nil {
			return nil, fmt.Errorf("%s: %w", format, err)
		}
	}
	return out, nil
}

// compileCustom собирает asset.Preset из custom-настройки. Имя custom = ключ
// (размер-грамматика, опционально с @dpr). Размер из имени, если width/height
// не заданы в настройках.
func compileCustom(name string, cfg PresetConfig, resolveWM func(string, string) (*processing.WatermarkSpec, error), defaultOrientation *processing.OrientationSpec) (*asset.Preset, error) {
	size, err := sizeFromCustom(name, cfg)
	if err != nil {
		return nil, err
	}
	formats, err := formatsFromConfig(cfg.OutputFormats)
	if err != nil {
		return nil, err
	}
	dpr, dprSet := dprFromConfig(cfg.DPR)
	encOverrides, err := encodingOverridesFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("custom %q: %w", name, err)
	}
	preset, err := asset.NewPreset(
		name,
		transformFromCropTrim(cfg.Crop.Unwrap(), cfg.Trim.Unwrap()),
		size,
		formats,
		dpr,
		dprSet,
		int(cfg.Quality.Unwrap()),
		int(cfg.Frames.Unwrap()),
		int(cfg.Duration.Unwrap()),
		loopFromConfig(cfg.Loop),
		encOverrides,
	)
	if err != nil {
		return nil, err
	}
	wm, err := resolveWM(cfg.Watermark.Unwrap(), fmt.Sprintf("custom %q", name))
	if err != nil {
		return nil, err
	}
	if wm != nil {
		preset = preset.WithWatermark(wm)
	}
	preset = preset.WithOrientation(mergePresetOrientation(cfg, defaultOrientation))
	return preset, nil
}

// sizeFromPreset строит размер из width/height настроек пресета.
// 0 = не задано; оба 0 → оригинал ("x").
func sizeFromPreset(cfg PresetConfig) (asset.Size, error) {
	w := cfg.Width.Unwrap()
	h := cfg.Height.Unwrap()
	if w == 0 && h == 0 {
		return asset.NewOriginalSize(), nil
	}
	var dw, dh *asset.Dimension
	if w > 0 {
		d := asset.Dimension(w)
		dw = &d
	}
	if h > 0 {
		d := asset.Dimension(h)
		dh = &d
	}
	return asset.NewSize(dw, dh)
}

// sizeFromCustom строит размер custom из имени (размер-грамматика) с
// приоритетом настроек width/height.
func sizeFromCustom(name string, cfg PresetConfig) (asset.Size, error) {
	base, _, err := asset.SplitPresetNameDPR(name)
	if err != nil {
		return asset.Size{}, err
	}
	sz, err := asset.ParseSize(base)
	if err != nil {
		return asset.Size{}, err
	}
	w := cfg.Width.Unwrap()
	h := cfg.Height.Unwrap()
	if w == 0 && h == 0 {
		return sz, nil
	}
	var dw, dh *asset.Dimension
	if w > 0 {
		d := asset.Dimension(w)
		dw = &d
	}
	if h > 0 {
		d := asset.Dimension(h)
		dh = &d
	}
	if dw == nil {
		dw = sz.Width()
	}
	if dh == nil {
		dh = sz.Height()
	}
	if dw == nil && dh == nil {
		return asset.NewOriginalSize(), nil
	}
	return asset.NewSize(dw, dh)
}

// formatsFromConfig собирает список допустимых выходных форматов.
func formatsFromConfig(list dynamic.StringSlice) ([]asset.Format, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("output format list is empty")
	}
	out := make([]asset.Format, 0, len(list))
	for _, s := range list {
		f, err := asset.NewFormat(s.Unwrap())
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// dprFromConfig извлекает dpr из Nullable: Set=false → (0, false) — «ключ
// отсутствует»; Set=true → (значение, true) — «ключ присутствует» (даже 0/1).
func dprFromConfig(d dynamic.Nullable[dynamic.Uint32]) (asset.DPR, bool) {
	if !d.Set {
		return 0, false
	}
	return asset.DPR(d.Value.Unwrap()), true
}

// loopFromConfig извлекает loop из Nullable (nil = default-loop).
func loopFromConfig(l dynamic.Nullable[dynamic.Bool]) *bool {
	if !l.Set {
		return nil
	}
	v := l.Value.Unwrap()
	return &v
}

// transformFromCropTrim маппит строковый режим crop и булев флаг trim
// (независимые фильтры) в трансформационный код:
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

// sortStrings сортирует строки (обёртка для детерминированного порядка).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
