// Package encoding — domain-ядро конфигурации кодирования.
//
// Единый реестр нативных параметров форматов (yaml-ключи в kebab-case,
// они же ключи пресетов) и якорный маппинг quality → нативные параметры.
//
// Маппинг — это НЕ голая математика, а физическое соответствие результата
// ожиданиям quality: при quality, равном якорному значению формата
// (webp 75, jpeg 80, avif/heif 80, jxl 75), нативные параметры дают текущее
// дефолтное поведение движка. При отклонении от якоря параметры масштабируются
// линейно между якорной точкой и граничными точками диапазона: quality 100 →
// максимальное усилие сжатия (лучшая упаковка), quality → 0 → минимальное.
//
// Все вычисления — детерминированные чистые функции (см. mapping.go). Явные
// значения из пресета (overrides) применяются как есть с валидацией диапазона
// и всегда побеждают автомаппинг.
package encoding

import (
	"fmt"
	"math"
	"strings"
)

// Format — каноническое имя формата (yaml-ключ, он же ключ пресета).
type Format string

// Канонические имена форматов реестра.
const (
	FormatJPEG Format = "jpeg"
	FormatWebP Format = "webp"
	FormatAVIF Format = "avif"
	FormatHEIF Format = "heif"
	FormatJXL  Format = "jxl"
	FormatPNG  Format = "png"
	FormatAPNG Format = "apng"
	FormatGIF  Format = "gif"
)

func (f Format) String() string { return string(f) }

// knownOrder — канонический порядок перечисления форматов (для Formats()).
var knownOrder = []Format{
	FormatJPEG, FormatWebP, FormatAVIF, FormatHEIF,
	FormatJXL, FormatPNG, FormatAPNG, FormatGIF,
}

// ParamKind — тип нативного параметра (для валидации и приведения overrides).
type ParamKind int

// Типы нативных параметров.
const (
	KindInt ParamKind = iota
	KindBool
	KindFloat
)

// ParamMeta — описание нативного параметра формата.
//
// Name — yaml-ключ в kebab-case (он же ключ в пресете). Min/Max — допустимый
// диапазон (для KindBool не используются), Default — дефолтное значение
// (для KindBool: 0/1). Auto — параметр участвует в якорном автомаппинге от
// quality, когда явное значение (из пресета) не задано.
type ParamMeta struct {
	Name    string
	Kind    ParamKind
	Min     float64
	Max     float64
	Default float64
	Auto    bool
	Help    string
}

// FormatDef — реестр формата: якорное quality, признак прямого quality
// (передаётся кодеку как качество потери) и список нативных параметров.
type FormatDef struct {
	// Name — каноническое имя формата.
	Name Format
	// AnchorQuality — якорное значение quality: при нём нативные параметры
	// дают текущее дефолтное поведение движка.
	AnchorQuality uint8
	// DirectQuality — true для форматов с прямым quality (jpeg/webp/avif/
	// heif/jxl): quality запроса передаётся кодеру как качество и управляет
	// Lossless=false форматами. Для AlwaysLossless-форматов (png/apng/gif)
	// quality никогда не вводит потерю: оно влияет только на усилие
	// упаковки и палитровую автоматику.
	DirectQuality bool
	// AlwaysLossless — формат принципиально lossless (png/apng/gif):
	// quality влияет только на усилие упаковки/палитру, но не на качество
	// пикселей.
	AlwaysLossless bool
	// Params — нативные параметры формата (ключ kebab-case → мета).
	Params []ParamMeta
}

// Param возвращает мету нативного параметра формата.
func (d FormatDef) Param(name string) (ParamMeta, bool) {
	for _, p := range d.Params {
		if p.Name == name {
			return p, true
		}
	}
	return ParamMeta{}, false
}

// registry — единый реестр нативных параметров форматов. Неизменяем после
// инициализации пакета; LookupFormat возвращает копии, чтение конкурентно-
// безопасно.
//
// Дефолты соответствуют setting.yaml: jpeg-progressive=false,
// png-interlace=false, png-palette=false; «не задано» в конфиге трактуется
// как «эффективный» дефолт движка: webp effort 4, png compression 6,
// jxl effort 7, palette colors 256, palette bit-depth 8, gif bit-depth 8.
var registry = map[Format]FormatDef{
	FormatJPEG: {
		Name:          FormatJPEG,
		AnchorQuality: 80,
		DirectQuality: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 80,
				Help: "Качество JPEG [1,100]; дефолт 80. Не допускается в пресетах — задаётся из запроса."},
			{Name: "progressive", Kind: KindBool, Default: 0,
				Help: "Прогрессивный JPEG; дефолт false (baseline, server.yaml:358)."},
		},
	},
	FormatWebP: {
		Name:          FormatWebP,
		AnchorQuality: 75,
		DirectQuality: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 75,
				Help: "Качество WebP [1,100]; дефолт 75. Не допускается в пресетах."},
			{Name: "reduction-effort", Kind: KindInt, Min: 0, Max: 6, Default: 4, Auto: true,
				Help: "Reduction effort WebP [0,6]: больше = лучше сжатие, медленнее. Якорь: q=75→4, q=100→6, q=0→0."},
			{Name: "lossless", Kind: KindBool, Default: 0,
				Help: "Без потерь (lossless) WebP; дефолт false."},
			{Name: "near-lossless", Kind: KindBool, Default: 0,
				Help: "Near-lossless WebP; дефолт false."},
		},
	},
	FormatAVIF: {
		Name:          FormatAVIF,
		AnchorQuality: 80,
		DirectQuality: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 80,
				Help: "Качество AVIF [1,100]; дефолт 80. Не допускается в пресетах."},
			{Name: "speed", Kind: KindInt, Min: 0, Max: 9, Default: 6, Auto: true,
				Help: "Speed AVIF [0,9]: меньше = медленнее, лучше сжатие. 0 ВАЛИДЕН (это скорость, а не «не задано»). Якорь (инверсия): q=80→6, q=100→0, q=0→9."},
			{Name: "lossless", Kind: KindBool, Default: 0,
				Help: "Без потерь (lossless) AVIF; дефолт false."},
		},
	},
	FormatHEIF: {
		Name:          FormatHEIF,
		AnchorQuality: 80,
		DirectQuality: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 80,
				Help: "Качество HEIF [1,100]; дефолт 80. Не допускается в пресетах."},
		},
	},
	FormatJXL: {
		Name:          FormatJXL,
		AnchorQuality: 75,
		DirectQuality: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 75,
				Help: "Качество JPEG XL [1,100]; дефолт 75. Не допускается в пресетах."},
			{Name: "effort", Kind: KindInt, Min: 3, Max: 9, Default: 7, Auto: true,
				Help: "Effort JPEG XL [3,9]: больше = лучше сжатие при том же качестве. Якорь: q=75→7, q=100→9, q=0→3."},
			{Name: "lossless", Kind: KindBool, Default: 0,
				Help: "Без потерь (lossless) JPEG XL; дефолт false."},
		},
	},
	FormatPNG: {
		Name:           FormatPNG,
		AnchorQuality:  85, // якорь усилия упаковки и палитровой автоматики
		DirectQuality:  false,
		AlwaysLossless: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 80,
				Help: "Якорное качество запроса для PNG: влияет только на усилие упаковки и палитру, потерь не вводит. Не допускается в пресетах."},
			{Name: "compression-level", Kind: KindInt, Min: 1, Max: 9, Default: 6, Auto: true,
				Help: "Уровень сжатия PNG/APNG [1,9] (больше = меньше размер, медленнее). Якорь: q=85→6, q=100→9, q=0→1."},
			{Name: "interlace", Kind: KindBool, Default: 0,
				Help: "Чересстрочный (Adam7) PNG; дефолт false (server.yaml:362). Применяется и к APNG."},
			{Name: "palette", Kind: KindBool, Default: 0, Auto: true,
				Help: "Палитровый (quantized) экспорт PNG. Явное значение из пресета побеждает. Автоматика: если не задана явно — палитра ON при q<90, OFF (truecolor) при q>=90 (палитра портит градиенты, а высокий quality требует качества). Дефолт конфига false (server.yaml:370)."},
			{Name: "palette-colors", Kind: KindInt, Min: 2, Max: 256, Default: 256, Auto: true,
				Help: "Максимум цветов палитры [2,256]. Автоматика от quality (legacy-0 → 256). Якорь: q=85 → 256."},
			{Name: "palette-bit-depth", Kind: KindInt, Min: 1, Max: 8, Default: 8, Auto: true,
				Help: "Битность палитры [1,8] (snap к допустимым 1/2/4/8). Автоматика из числа цветов; при colors >= 128 → 8."},
			{Name: "dither", Kind: KindFloat, Min: 0, Max: 1, Default: 1,
				Help: "Дизеринг палитрового PNG [0,1]; дефолт 1.0. Значим при palette=true."},
		},
	},
	FormatAPNG: {
		Name:           FormatAPNG,
		AnchorQuality:  85,
		DirectQuality:  false,
		AlwaysLossless: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 80,
				Help: "Качество запроса для APNG (см. PNG). Не допускается в пресетах."},
			{Name: "compression-level", Kind: KindInt, Min: 1, Max: 9, Default: 6, Auto: true,
				Help: "Уровень сжатия APNG [1,9]. Якорь: q=85→6, q=100→9, q=0→1. Палитра к APNG не применяется (несовместима с чанками анимации)."},
			{Name: "interlace", Kind: KindBool, Default: 0,
				Help: "Чересстрочный APNG; дефолт false."},
		},
	},
	FormatGIF: {
		Name:           FormatGIF,
		AnchorQuality:  75,
		DirectQuality:  false,
		AlwaysLossless: true,
		Params: []ParamMeta{
			{Name: "quality", Kind: KindInt, Min: 1, Max: 100, Default: 80,
				Help: "Качество запроса для GIF (см. PNG). Не допускается в пресетах."},
			{Name: "effort", Kind: KindInt, Min: 1, Max: 10, Default: 7, Auto: true,
				Help: "Effort GIF [1,10] (дефолт libvips 7, ранее не конфигурировался). Якорь: q=75→7, q=100→10, q=0→1."},
			{Name: "bit-depth", Kind: KindInt, Min: 1, Max: 8, Default: 8,
				Help: "Битность палитры GIF [1,8] (меньше = компактнее, до 256 цветов). Дефолт 8 (legacy-0 → 8). ОСОБЕННОСТЬ: значения >4 требуют dither/квантование — правило «эффективного bit-depth» см. NormalizeGIFBitDepth и EffectiveDither."},
			{Name: "dither", Kind: KindFloat, Min: 0, Max: 1, Default: 1,
				Help: "Дизеринг палитры GIF [0,1]; дефолт 1.0 (текущий код задаёт dither=1.0 для gifsave)."},
		},
	},
}

// Formats возвращает реестр форматов в каноническом порядке.
func Formats() []FormatDef {
	out := make([]FormatDef, 0, len(knownOrder))
	for _, f := range knownOrder {
		out = append(out, registry[f])
	}
	return out
}

// LookupFormat находит формат по имени (регистронезависимо).
func LookupFormat(name string) (FormatDef, bool) {
	d, ok := registry[Format(strings.ToLower(name))]
	return d, ok
}

// Params — типобезопасный контейнер ЯВНЫХ значений параметров из пресета.
// nil-указатель = параметр не задан → действует якорный автомаппинг от
// quality (для параметров с Auto=true) либо registry-дефолт. Все значения,
//
//	попавшие сюда, уже прошли валидацию диапазона в Resolve.
type Params struct {
	Progressive      *bool
	Lossless         *bool
	NearLossless     *bool
	Interlace        *bool
	ReductionEffort  *int // webp
	Speed            *int // avif
	Effort           *int // jxl / gif
	CompressionLevel *int // png / apng
	Palette          *bool
	PaletteColors    *int
	PaletteBitDepth  *int
	Dither           *float64
	BitDepth         *int // gif
}

// ResolvedParams — эффективные параметры формата после Resolve: явные
// значения из пресета применены с валидацией, незаданные — из якорного
// маппинга (или registry-дефолтов). Поля, неприменимые к формату, содержат
// нулевые/дефолтные значения.
type ResolvedParams struct {
	Format  Format
	Quality int // качество запроса; для AlwaysLossless-форматов не передаётся кодеру

	Progressive  bool
	Lossless     bool
	NearLossless bool
	Interlace    bool

	ReductionEffort  int // webp
	Speed            int // avif
	Effort           int // jxl / gif
	CompressionLevel int // png / apng

	Palette         bool // png
	PaletteColors   int  // png
	PaletteBitDepth int  // png

	Dither   float64 // png / gif
	BitDepth int     // gif (эффективный)
}

// Resolve вычисляет эффективные параметры формата.
//
// format — каноническое имя формата; quality — целевое качество запроса
// (uint8, [0,100]); overrides — нативные ключи из пресета (kebab-case).
// Overrides применяются как есть с валидацией диапазона и побеждают
// автомаппинг; незаданные параметры берутся из якорного маппинга от quality
// либо из registry-дефолтов.
//
// Ошибки: неизвестный формат, неизвестный/чужой для формата параметр,
// значение вне диапазона, неверный тип значения, ключ "quality" в overrides
// (строго запрещён — управляется из запроса), quality > 100.
func Resolve(format string, quality uint8, overrides map[string]any) (ResolvedParams, error) {
	def, ok := LookupFormat(format)
	if !ok {
		return ResolvedParams{}, fmt.Errorf("encoding: unknown format %q", format)
	}
	if quality > 100 {
		return ResolvedParams{}, fmt.Errorf("encoding: %s: quality %d out of range [0,100]", def.Name, quality)
	}
	if _, ok := overrides["quality"]; ok {
		return ResolvedParams{}, fmt.Errorf("encoding: %s: parameter %q is not allowed in presets (derived from request quality)", def.Name, "quality")
	}
	explicit, err := applyOverrides(def, overrides)
	if err != nil {
		return ResolvedParams{}, err
	}

	r := ResolvedParams{Format: def.Name, Quality: int(quality)}

	// Простые (не Auto) параметры: явное значение или registry-дефолт.
	r.Progressive = r.EffectiveProgressive(quality, explicit.Progressive)
	r.Lossless = r.EffectiveLossless(quality, explicit.Lossless)
	r.NearLossless = r.EffectiveNearLossless(quality, explicit.NearLossless)
	r.Interlace = r.EffectiveInterlace(quality, explicit.Interlace)

	// Auto-параметры усилия сжатия (по формату, т.к. формулы разные).
	switch def.Name {
	case FormatWebP:
		r.ReductionEffort = r.EffectiveReductionEffort(quality, explicit.ReductionEffort)
	case FormatAVIF:
		r.Speed = r.EffectiveSpeed(quality, explicit.Speed)
	case FormatJXL:
		r.Effort = r.EffectiveJXLEffort(quality, explicit.Effort)
	case FormatGIF:
		r.Effort = r.EffectiveGIFEffort(quality, explicit.Effort)
		r.BitDepth = r.EffectiveBitDepth(quality, explicit.BitDepth)
	case FormatPNG, FormatAPNG:
		r.CompressionLevel = r.EffectiveCompressionLevel(quality, explicit.CompressionLevel)
	}
	if def.Name == FormatPNG {
		r.Palette = r.EffectivePalette(quality, explicit.Palette)
		r.PaletteColors = r.EffectivePaletteColors(quality, explicit.PaletteColors)
		r.PaletteBitDepth = r.EffectivePaletteBitDepth(quality, explicit.PaletteBitDepth)
	}
	if def.Name == FormatGIF || def.Name == FormatPNG {
		r.Dither = r.EffectiveDither(quality, explicit.Dither)
	}
	return r, nil
}

// ValidateOverrides проверяет набор нативных параметров формата (формат →
// ключи пресета в kebab-case) на валидность при компиляции и построении
// ProcessingPlan (fail-fast): известный формат, параметры принадлежат
// формату, значения в допустимых диапазонах.
//
// Ключ "quality" обрабатывается ОСОБО: это per-format quality из пресета
// (jpeg-quality/webp-quality/avif-quality/heif-quality/jxl-quality). Он
// допустим ТОЛЬКО для lossy-форматов (DirectQuality) и ложится в overrides
// под реестровым именем "quality"; после валидации downstream извлекает
// его из overrides и передаёт в Resolve как качество формата —
// сам Resolve по-прежнему запрещает "quality" внутри overrides. Для
// lossless-форматов (png/apng/gif) задание quality-ключа формата — ошибка
// (их качество задаётся только скалярным quality пресета).
//
// Остальные параметры прогоняются через applyOverrides (та же проверка, что
// в Resolve), поэтому валидные overrides гарантированно примутся Resolve
// после удаления ключа "quality".
func ValidateOverrides(format string, overrides map[string]any) error {
	if len(overrides) == 0 {
		return nil
	}
	def, ok := LookupFormat(format)
	if !ok {
		return fmt.Errorf("encoding: unknown format %q", format)
	}
	// per-format quality валидируется отдельно (lossy-форматы) и не проходит
	// через applyOverrides: это качество формата, а не нативный параметр
	// кодера. Для lossless-форматов quality-ключ формата — ошибка.
	params := make(map[string]any, len(overrides))
	for k, v := range overrides {
		if k == "quality" {
			if !def.DirectQuality {
				return fmt.Errorf("encoding: %s: per-format quality is not allowed for lossless format (use scalar quality)", def.Name)
			}
			if _, err := coerceParam(ParamMeta{Name: "quality", Kind: KindInt, Min: 1, Max: 100}, v); err != nil {
				return fmt.Errorf("encoding: %s: parameter %q: %w", def.Name, "quality", err)
			}
			continue
		}
		params[k] = v
	}
	_, err := applyOverrides(def, params)
	return err
}

// applyOverrides разбирает map[нативный ключ]значение в типизированный Params
// с валидацией диапазона и типа. Чужие для формата и неизвестные ключи —
// ошибка.
func applyOverrides(def FormatDef, overrides map[string]any) (Params, error) {
	var p Params
	for key, raw := range overrides {
		meta, ok := def.Param(key)
		if !ok {
			return Params{}, fmt.Errorf("encoding: %s: unknown parameter %q", def.Name, key)
		}
		val, err := coerceParam(meta, raw)
		if err != nil {
			return Params{}, fmt.Errorf("encoding: %s: parameter %q: %w", def.Name, key, err)
		}
		switch key {
		case "progressive":
			b := val == 1
			p.Progressive = &b
		case "lossless":
			b := val == 1
			p.Lossless = &b
		case "near-lossless":
			b := val == 1
			p.NearLossless = &b
		case "interlace":
			b := val == 1
			p.Interlace = &b
		case "reduction-effort":
			v := int(val)
			p.ReductionEffort = &v
		case "speed":
			v := int(val)
			p.Speed = &v
		case "effort":
			v := int(val)
			p.Effort = &v
		case "compression-level":
			v := int(val)
			p.CompressionLevel = &v
		case "palette":
			b := val == 1
			p.Palette = &b
		case "palette-colors":
			v := int(val)
			p.PaletteColors = &v
		case "palette-bit-depth":
			v := int(val)
			p.PaletteBitDepth = &v
		case "dither":
			v := val
			p.Dither = &v
		case "bit-depth":
			v := int(val)
			p.BitDepth = &v
		default:
			return Params{}, fmt.Errorf("encoding: %s: parameter %q is not applicable in domain registry", def.Name, key)
		}
	}
	return p, nil
}

// coerceParam приводит сырое значение из пресета к типу параметра и
// проверяет диапазон.
func coerceParam(meta ParamMeta, raw any) (float64, error) {
	if meta.Kind == KindBool {
		b, ok := raw.(bool)
		if !ok {
			return 0, fmt.Errorf("want bool, got %T", raw)
		}
		if b {
			return 1, nil
		}
		return 0, nil
	}
	var v float64
	switch t := raw.(type) {
	case int:
		v = float64(t)
	case int64:
		v = float64(t)
	case float64:
		v = t
	case string:
		f, err := parseFloat(t)
		if err != nil {
			return 0, err
		}
		v = f
	default:
		return 0, fmt.Errorf("want number, got %T", raw)
	}
	if meta.Kind == KindInt && v != math.Trunc(v) {
		return 0, fmt.Errorf("want integer, got %v", v)
	}
	if v < meta.Min || v > meta.Max {
		return 0, fmt.Errorf("value %v out of range [%v,%v]", v, meta.Min, meta.Max)
	}
	return v, nil
}

// Value возвращает эффективное значение нативного параметра (для
// потребителей и тестов). ok=false для ключей, не принадлежащих формату.
func (r ResolvedParams) Value(name string) (any, bool) {
	switch name {
	case "quality":
		return r.Quality, true
	case "progressive":
		return r.Progressive, true
	case "lossless":
		return r.Lossless, true
	case "near-lossless":
		return r.NearLossless, true
	case "interlace":
		return r.Interlace, true
	case "reduction-effort":
		return r.ReductionEffort, true
	case "speed":
		return r.Speed, true
	case "effort":
		return r.Effort, true
	case "compression-level":
		return r.CompressionLevel, true
	case "palette":
		return r.Palette, true
	case "palette-colors":
		return r.PaletteColors, true
	case "palette-bit-depth":
		return r.PaletteBitDepth, true
	case "dither":
		return r.Dither, true
	case "bit-depth":
		return r.BitDepth, true
	}
	return nil, false
}
