package encoding

import (
	"math"
	"strconv"
)

// Пакет mapping.go: детерминированные чистые функции якорного маппинга
// quality → нативные параметры и методы EffectiveX (явное значение из
// пресета побеждает автомаппинг).
//
// Принцип «эффективный параметр»: EffectiveX(quality, explicit *T) —
// explicit != nil → значение из пресета (уже провалидировано в Resolve),
// иначе — результат чистой функции автомаппинга от quality.

// Эффективные простые (bool) параметры: явное значение или registry-дефолт.
//
// quality не влияет на них: это физические переключатели кодера (прогрессив,
// interlace, lossless-режим), а не усилие сжатия.

// EffectiveProgressive — JPEG progressive (дефолт false, server.yaml:358).
func (r ResolvedParams) EffectiveProgressive(_ uint8, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return false
}

// EffectiveLossless — lossless-режим lossy-формата (webp/avif/jxl).
func (r ResolvedParams) EffectiveLossless(_ uint8, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return false
}

// EffectiveNearLossless — near-lossless WebP.
func (r ResolvedParams) EffectiveNearLossless(_ uint8, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return false
}

// EffectiveInterlace — чересстрочный PNG/APNG (дефолт false, server.yaml:362).
func (r ResolvedParams) EffectiveInterlace(_ uint8, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return false
}

// --- Автомаппинг усилия сжатия от quality --------------------------------

// Effort: webp reduction-effort [0..6].
// Якорь: q=75→4 (текущий дефолт конфига/движка), q=100→6 (максимум усилия),
// q=0→0 (минимум). Линейно, монотонно:
//
//	effort(q) = clamp(round(4 + (q-75)*(2/25)), 0, 6)
func WebPReductionEffort(q int) int {
	return clampInt(int(math.Round(4+float64(q-75)*(2.0/25.0))), 0, 6)
}

// EffectiveReductionEffort — webp reduction-effort: явный → он, иначе автомаппинг.
func (r ResolvedParams) EffectiveReductionEffort(q uint8, explicit *int) int {
	if explicit != nil {
		return *explicit
	}
	return WebPReductionEffort(int(q))
}

// Effort: avif speed [0..9] (ИНВЕРСИЯ: меньше = медленнее, лучше сжатие).
// 0 ВАЛИДЕН как скорость. Якорь: q=80→6 (конфиг avif-speed=6), q=100→0 (медленно,
// максимальное сжатие), q=0→9 (быстро). Линейно, монотонно:
//
//	speed(q) = clamp(round(6 + (q-80)*(-9/20)), 0, 9)
func AVIFSpeed(q int) int {
	return clampInt(int(math.Round(6+float64(q-80)*(-9.0/20.0))), 0, 9)
}

// EffectiveSpeed — avif speed: явный → он, иначе автомаппинг.
func (r ResolvedParams) EffectiveSpeed(q uint8, explicit *int) int {
	if explicit != nil {
		return *explicit
	}
	return AVIFSpeed(int(q))
}

// Effort: jxl effort [3..9].
// Якорь: q=75→7 (умолчание govips), q=100→9 (максимум усилия), q=0→3 (минимум
// диапазона). Больше effort = лучше сжатие при том же качестве:
//
//	effort(q) = clamp(round(7 + (q-75)*(2/25)), 3, 9)
func JXLEffort(q int) int {
	return clampInt(int(math.Round(7+float64(q-75)*(2.0/25.0))), 3, 9)
}

// EffectiveJXLEffort — jxl effort: явный → он, иначе автомаппинг.
func (r ResolvedParams) EffectiveJXLEffort(q uint8, explicit *int) int {
	if explicit != nil {
		return *explicit
	}
	return JXLEffort(int(q))
}

// Effort: gif effort [1..10] (дефолт libvips 7).
// Якорь: q=75→7, q=100→10 (максимум), q=0→1 (минимум):
//
//	effort(q) = clamp(round(7 + (q-75)*(3/25)), 1, 10)
func GIFEffort(q int) int {
	return clampInt(int(math.Round(7+float64(q-75)*(3.0/25.0))), 1, 10)
}

// EffectiveGIFEffort — gif effort: явный → он, иначе автомаппинг.
func (r ResolvedParams) EffectiveGIFEffort(q uint8, explicit *int) int {
	if explicit != nil {
		return *explicit
	}
	return GIFEffort(int(q))
}

// CompressionLevel: png/apng compression-level [1..9].
// Физика: compression-level не влияет на размер пикселей, только на усилие
// упаковки (на lossless-качество не влияет). Якорь: q=85→6 (конфиг
// png-compression-level=6), q=100→9 (максимальная упаковка), q=0→1 (быстрое
// кодирование). Кусочно-линейная монотонная функция:
//
//	cl(q) = 1 + round(5*q/85)   для q <= 85   (1 → 6)
//	cl(q) = 6 + round((q-85)/5) для q >  85   (6 → 9)
//
// Якорь: cl(85)=6, cl(100)=9, cl(0)=1. Монотонность проверена тестами.
func PNGCompressionLevel(q int) int {
	if q <= 85 {
		return 1 + int(math.Round(5.0*float64(q)/85.0))
	}
	return 6 + int(math.Round(float64(q-85)/5.0))
}

// EffectiveCompressionLevel — png/apng compression-level: явный → он, иначе
// автомаппинг от quality (lossless-формат: потерь не вводит).
func (r ResolvedParams) EffectiveCompressionLevel(q uint8, explicit *int) int {
	if explicit != nil {
		return *explicit
	}
	return PNGCompressionLevel(int(q))
}

// --- PNG палитровая автоматика от quality --------------------------------

// PaletteThreshold — quality-порог: при q >= порога палитра OFF (truecolor).
// Физика: палитра портит градиенты; высокий quality требует качества
// пикселей, поэтому при q>=90 автоматическая палитра выключается.
const PaletteThreshold = 90

// PaletteMaxColors — максимум палитры PNG.
const PaletteMaxColors = 256

// PaletteAuto — решение об автоматической палитре от quality (когда palette
// не задана явно): q < 90 → ON, q >= 90 → OFF (truecolor).
func PaletteAuto(q int) bool {
	return q < PaletteThreshold
}

// PaletteColorsFromQuality — число цветов автоматической палитры [2..256].
// Якорь: q=85 → 256 (текущий дефолт png-palette-colors). Монотонно возрастает
// от 2 (q=0) до 256 (q>=85):
//
//	colors(q) = clamp(round(2 + (q/85)*254), 2, 256)
func PaletteColorsFromQuality(q int) int {
	v := int(math.Round(2 + float64(q)*254.0/85.0))
	return clampInt(v, 2, PaletteMaxColors)
}

// PaletteBitDepthFromColors — битность палитры из числа цветов.
// base = ceil(log2(colors)), снап к допустимым битностям PNG {1,2,4,8};
// при colors >= 128 → 8 (snap через 7 к 8). Якорь: colors=256 → 8.
func PaletteBitDepthFromColors(colors int) int {
	switch {
	case colors <= 2:
		return 1
	case colors <= 4:
		return 2
	case colors <= 16:
		return 4
	default:
		return 8 // colors в [17..256]: битности 5..8 снапятся к 8
	}
}

// PaletteBitDepthFromQuality — битность автоматической палитры из quality:
// bit-depth = PaletteBitDepthFromColors(PaletteColorsFromQuality(q)).
func PaletteBitDepthFromQuality(q int) int {
	return PaletteBitDepthFromColors(PaletteColorsFromQuality(q))
}

// EffectivePalette — png palette: явная → она, иначе автоматика от quality
// (q < 90 → true, q >= 90 → false): качество управляет палитрой.
func (r ResolvedParams) EffectivePalette(q uint8, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return PaletteAuto(int(q))
}

// EffectivePaletteColors — png palette-colors: явное → оно, иначе автомаппинг
// от quality. Значимо только при эффективной palette=true.
func (r ResolvedParams) EffectivePaletteColors(q uint8, explicit *int) int {
	if explicit != nil {
		return *explicit
	}
	return PaletteColorsFromQuality(int(q))
}

// EffectivePaletteBitDepth — png palette-bit-depth: явное → оно, иначе
// автомаппинг из числа цветов.
func (r ResolvedParams) EffectivePaletteBitDepth(q uint8, explicit *int) int {
	if explicit != nil {
		return *explicit
	}
	return PaletteBitDepthFromQuality(int(q))
}

// --- Dither и эффективный bit-depth --------------------------------------

// DefaultDither — дефолт дизеринга для палитрового экспорта (PNG palette /
// GIF): 1.0. Текущий код задаёт dither=1.0 для gifsave; для палитрового PNG
// дизеринг улучшает визуальный переход градиентов. Явное значение из пресета
// [0,1] побеждает.
const DefaultDither = 1.0

// EffectiveDither — dither [0,1]: явный → он, иначе DefaultDither.
// Применим png (палитровый экспорт) и gif.
func (r ResolvedParams) EffectiveDither(_ uint8, explicit *float64) float64 {
	if explicit != nil {
		return *explicit
	}
	return DefaultDither
}

// NormalizeGIFBitDepth — «эффективный bit-depth» GIF [1..8]:
// 0 = «не задано» → 8 (умолчание govips), < 1 → 1, > 8 → 8.
func NormalizeGIFBitDepth(v int) int {
	switch {
	case v == 0:
		return 8
	case v < 1:
		return 1
	case v > 8:
		return 8
	}
	return v
}

// EffectiveBitDepth — gif bit-depth: явное → оно (нормализовано в диапазон
// [1,8] через NormalizeGIFBitDepth), иначе 8.
//
// ОСОБЕННОСТЬ: значения bit-depth > 4 требуют dither/квантование — движок
// всегда квантует до запрошенной битности; Dither=0 лишь отключает дизеринг,
// но квантование выполняет gifsave. Граница 4 — «безопасная» битность, где
// визуальные артефакты малозаметны даже без дизеринга; при bit-depth > 4 и
// dither=0 возможна полосатость (banding) — это документированное поведение
// движка, а не ошибка маппинга.
func (r ResolvedParams) EffectiveBitDepth(_ uint8, explicit *int) int {
	if explicit != nil {
		return NormalizeGIFBitDepth(*explicit)
	}
	return 8
}

// --- утилиты --------------------------------------------------------------

// clampInt зажимает v в [lo, hi] (lo <= hi).
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// parseFloat разбирает строковое числовое значение (для пресетов, заданных
// YAML-строками).
func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}
