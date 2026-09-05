// Параметры кодировщиков (per-format) в адаптере libvips.
//
// S4: глобальные статические EncoderParams заменены на разрешение параметров
// через domain/encoding на КАЖДЫЙ экспорт: глобальные значения из секции
// encoders (EncodersConfig) + overrides из ProcessingPlan + якорный
// автомаппинг от plan.Quality. Единственная точка вычисления — resolveEffective,
// используемая из exportImage (process_libvips.go).
//
// Файл без build-tag: resolveEffective и структуры EncodersConfig не зависят
// от govips и тестируются в любой сборке.
package libvips

import (
	"gitverse.ru/pkg-ru/imager/domain/encoding"
)

// defaultQuality — дефолт encoders.default-quality при 0 (совпадает с
// историческим дефолтом кода 80; см. composition.buildEncoders).
const defaultQuality = 80

// EncodersConfig — полное per-format представление единой секции encoders
// из setting/server.yaml (проброшенной через composition.RuntimeConfig).
//
// nil-поле = «не задано глобально»: для этого параметра применяется якорный
// автомаппинг от quality (если параметр Auto в реестре domain/encoding) либо
// registry-дефолт.
type EncodersConfig struct {
	// DefaultQuality — encoders.default-quality [1,100] (0 = дефолт кода 80).
	DefaultQuality int
	// Formats — per-format параметры (ключ — каноническое имя формата
	// domain/encoding: jpeg/webp/avif/heif/jxl/png/apng/gif).
	Formats map[string]FormatEncodersConfig
}

// FormatEncodersConfig — параметры группы одного формата в секции encoders.
// nil-поле = «не задано» для этого формата (применяются registry-дефолты
// domain/encoding и/или автомаппинг от quality). Значения уже проверены
// по реестру domain/encoding при загрузке конфигурации.
type FormatEncodersConfig struct {
	Quality          *int     // quality [1,100]; nil = из запроса / encoders.default-quality
	Progressive      *bool    // jpeg
	ReductionEffort  *int     // webp [0,6]
	Lossless         *bool    // webp/avif/jxl
	NearLossless     *bool    // webp
	Speed            *int     // avif [0,9] (0 валиден)
	Effort           *int     // jxl [3,9] / gif [1,10]
	CompressionLevel *int     // png/apng [1,9]
	Interlace        *bool    // png/apng
	Palette          *bool    // png
	PaletteColors    *int     // png [2,256]
	PaletteBitDepth  *int     // png [1,8]
	Dither           *float64 // png/gif [0,1]
	BitDepth         *int     // gif [1,8]
}

// resolveEffective вычисляет эффективные параметры кодирования формата для
// ОДНОГО экспорта через domain/encoding.Resolve.
//
// Приоритеты параметров (строго, от высшего к низшему):
//  1. explicit override из пресета (plan.EncodingOverrides[format]);
//  2. глобальный параметр секции encoders (EncodersConfig / setting/server.yaml);
//  3. якорный автомаппинг от quality (Auto-параметры реестра);
//  4. registry-дефолт domain/encoding.
//
// Качество (для DirectQuality-форматов и как усилие для AlwaysLossless):
//  1. per-format quality override из пресета (plan.EncodingOverrides[format]
//     ["quality"], только lossy-форматы);
//  2. encoders.<fmt>.quality (если задан глобально);
//  3. plan.Quality (скаляр из пресета/запроса; 0 заменён на
//     encoders.default-quality в generatev2, здесь — defense-in-depth).
//
// Реализация: merged-overrides (yaml-глобальные + preset) подаются в
// encoding.Resolve как ЯВНЫЕ значения (каждое уже провалидировано при
// загрузке конфигурации/компиляции плана), незаданные покрываются
// автомаппингом от quality или registry-дефолтами — это даёт ровно
// приведённую матрицу приоритетов.
func resolveEffective(cfg EncodersConfig, format string, quality int, overrides map[string]any) (encoding.ResolvedParams, error) {
	fcfg := cfg.Formats[format]

	// Качество: план → глобальный per-format → override пресета.
	q := quality
	if q <= 0 {
		q = cfg.DefaultQuality
	}
	if q <= 0 {
		q = defaultQuality
	}
	if fcfg.Quality != nil {
		q = *fcfg.Quality
	}

	// merged-overrides: сначала глобальные значения YAML, затем preset-ы
	// (на один и тот же реестровый ключ preset побеждает).
	merged := make(map[string]any, 14)
	putGlobals(merged, fcfg)
	for k, v := range overrides {
		if k == "quality" {
			// per-format quality из пресета — качество формата (побеждает
			// всё остальное); в overrides Resolve он не попадает (строго
			// запрещён реестром).
			if f, ok := toFloat(v); ok {
				q = int(f)
			}
			continue
		}
		merged[k] = v
	}

	q = clampQuality(q)
	return encoding.Resolve(format, uint8(q), merged)
}

// putGlobals копирует заданные глобальные параметры формата из
// EncodersConfig в merged (реестровые имена без префикса формата).
// качества нет — оно разрешается отдельно (см. resolveEffective).
func putGlobals(m map[string]any, f FormatEncodersConfig) {
	if f.Progressive != nil {
		m["progressive"] = *f.Progressive
	}
	if f.ReductionEffort != nil {
		m["reduction-effort"] = *f.ReductionEffort
	}
	if f.Lossless != nil {
		m["lossless"] = *f.Lossless
	}
	if f.NearLossless != nil {
		m["near-lossless"] = *f.NearLossless
	}
	if f.Speed != nil {
		m["speed"] = *f.Speed
	}
	if f.Effort != nil {
		m["effort"] = *f.Effort
	}
	if f.CompressionLevel != nil {
		m["compression-level"] = *f.CompressionLevel
	}
	if f.Interlace != nil {
		m["interlace"] = *f.Interlace
	}
	if f.Palette != nil {
		m["palette"] = *f.Palette
	}
	if f.PaletteColors != nil {
		m["palette-colors"] = *f.PaletteColors
	}
	if f.PaletteBitDepth != nil {
		m["palette-bit-depth"] = *f.PaletteBitDepth
	}
	if f.Dither != nil {
		m["dither"] = *f.Dither
	}
	if f.BitDepth != nil {
		m["bit-depth"] = *f.BitDepth
	}
}

// clampQuality зажимает качество в допустимый диапазон реестра [1,100]
// (все источники уже валидированы; clamp — defense-in-depth).
func clampQuality(q int) int {
	if q < 1 {
		return 1
	}
	if q > 100 {
		return 100
	}
	return q
}

// toFloat приводит числовое значение override (int/int64/float64) к float64.
// Типы — строгие из пресетов/реестра (ValidateOverrides), поэтому строки не
// разбираются.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

// pngQuantizeDecision — решение о применении PNG-квантования (палитровый
// экспорт) в exportImage. Вычисляется чистой функцией resolvePNGQuantize из
// resolved-параметров domain/encoding.
type pngQuantizeDecision struct {
	// Palette — применять ли палитровый (quantized) PNG-экспорт.
	Palette bool
	// Colors — максимальное число цветов палитры [2..256]. govips не
	// поддерживает передачу числа цветов (нет поля в PngExportParams) —
	// значение резолвится для картографии/диагностики.
	Colors int
	// Bitdepth — битность палитры [1..8]. Позволяет сохранить
	// (воспроизвести) палитровую битность исходника.
	Bitdepth int
}

// resolvePNGQuantize формирует решение о PNG-квантовании из resolved-параметров.
//
// Отказоустойчивость: квантование применяется только при эффективной
// palette=true (явный override из пресета/YAML или автомаппинг при q<90), в
// противном случае — обычный truecolor-экспорт, поэтому градиентные
// изображения не квантуются случайно. Значения уже нормализованы реестром
// (PaletteColors/PaletteBitDepth в допустимых диапазонах).
func resolvePNGQuantize(r encoding.ResolvedParams) pngQuantizeDecision {
	if !r.Palette {
		return pngQuantizeDecision{}
	}
	return pngQuantizeDecision{
		Palette:  true,
		Colors:   r.PaletteColors,
		Bitdepth: r.PaletteBitDepth,
	}
}
