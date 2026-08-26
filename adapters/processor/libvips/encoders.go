// Параметры кодировщиков (per-format), прокидываемые из конфигурации в
// exportImage. Файл без build-tag: значения и умолчания не зависят от
// govips и тестируются в любой сборке.
//
// Валидация диапазонов выполняется при загрузке конфигурации
// (adapters/httpapi/runtimeconfig.go): невалидное значение — ошибка старта,
// а не runtime-ошибка обработки.
package libvips

// Нормализация значений кодировщиков: 0 = «не задано» → встроенное
// умолчание адаптера (историческое поведение) либо govips. Диапазоны уже
// проверены при загрузке конфигурации; здесь только подстановка умолчаний
// и защитный clamp (defense-in-depth для прямых вызовов вне конфигурации).

// webpReductionEffort: 0 → 4 (умолчание govips и историческое значение).
func webpReductionEffort(v int) int {
	if v <= 0 {
		return 4
	}
	return v
}

// avifSpeed: 0 → 0 («использовать умолчание govips»).
func avifSpeed(v int) int { return v }

// pngCompression: 0 → 6 (умолчание govips и историческое значение).
func pngCompression(v int) int {
	if v <= 0 {
		return 6
	}
	return v
}

// jxlEffort: 0 → 0 («использовать умолчание govips», Effort=7).
func jxlEffort(v int) int { return v }

// pngPaletteColors: 0 → 256 (максимум палитры PNG, «не задано»);
// отрицательные значения — защитный clamp к 2 (палитра менее 2 цветов
// бессмысленна), значения > 256 зажимаются (максимум палитры PNG).
func pngPaletteColors(v int) int {
	if v == 0 {
		return 256
	}
	if v < 2 {
		return 2
	}
	if v > 256 {
		return 256
	}
	return v
}

// pngPaletteBitdepth: 0 → 8 (стандартная битность палитры, «не задано»);
// отрицательные значения — защитный clamp к 1, значения > 8 зажимаются
// (допустимые битности палитры PNG: 1, 2, 4, 8).
func pngPaletteBitdepth(v int) int {
	if v == 0 {
		return 8
	}
	if v < 1 {
		return 1
	}
	if v > 8 {
		return 8
	}
	return v
}

// gifBitDepth: 0 → 8 (умолчание govips для gifsave, «не задано»);
// отрицательные значения — защитный clamp к 1, значения > 8 зажимаются
// (битность палитры GIF).
func gifBitDepth(v int) int {
	if v == 0 {
		return 8
	}
	if v < 1 {
		return 1
	}
	if v > 8 {
		return 8
	}
	return v
}

// pngQuantizeDecision — решение о применении PNG-квантования (палитровый
// экспорт) в exportImage. Вычисляется чистой функцией resolvePNGQuantize.
type pngQuantizeDecision struct {
	// Palette — применять ли палитровый (quantized) PNG-экспорт.
	Palette bool
	// Colors — максимальное число цветов палитры [2..256].
	Colors int
	// Bitdepth — битность палитры [1..8]. Позволяет сохранить
	// (воспроизвести) палитровую битность исходника.
	Bitdepth int
}

// resolvePNGQuantize формирует решение о PNG-квантовании из параметров
// кодировщиков.
//
// Отказоустойчивость: квантование применяется ТОЛЬКО при явном включении
// (PNGPalette=true) — по умолчанию выключено, поэтому градиентные
// изображения (где палитра даёт артефакты) никогда не квантуются случайно.
// Значения 0 в конфиге заменяются безопасными умолчаниями; некорректные
// значения зажимаются в допустимые диапазоны.
func resolvePNGQuantize(enc EncoderParams) pngQuantizeDecision {
	if !enc.PNGPalette {
		return pngQuantizeDecision{}
	}
	return pngQuantizeDecision{
		Palette:  true,
		Colors:   pngPaletteColors(enc.PNGPaletteColors),
		Bitdepth: pngPaletteBitdepth(enc.PNGPaletteBitDepth),
	}
}

// EncoderParams — per-format параметры сжатия кодировщиков libvips.
// Нулевое значение поля = использовать встроенное умолчание движка.
type EncoderParams struct {
	// WebPReductionEffort — reduction effort WebP [0..6] (больше = лучше
	// сжатие, медленнее). 0 = умолчание govips (4).
	WebPReductionEffort int
	// AVIFSpeed — speed/effort AVIF [0..9] (больше = быстрее, хуже сжатие).
	// 0 = умолчание govips (5).
	AVIFSpeed int
	// PNGCompression — уровень сжатия PNG [0..9]. 0 = умолчание govips (6).
	PNGCompression int
	// JXLEffort — effort JPEG XL [0..9] (больше = лучше сжатие, медленнее).
	// 0 = умолчание govips (7).
	JXLEffort int
	// JPEGProgressive — прогрессивный (interlaced) JPEG. false = обычный
	// (baseline) JPEG.
	JPEGProgressive bool
	// PNGInterlace — чересстрочный (interlaced/Adam7) PNG. false = обычный
	// (не-интерлейсный) PNG.
	PNGInterlace bool
	// PNGPalette — включить PNG-квантование (палитровый экспорт). По
	// умолчанию выключено: применяется ТОЛЬКО при явном включении, чтобы
	// не вносить артефакты в градиенты. При ошибке квантования в
	// exportImage выполняется fallback на обычный PNG-экспорт.
	PNGPalette bool
	// PNGPaletteColors — максимальное число цветов палитры [2..256] при
	// PNGPalette=true. 0 = 256 (максимум палитры PNG).
	PNGPaletteColors int
	// PNGPaletteBitDepth — битность палитры [1..8] при PNGPalette=true.
	// Позволяет сохранить (воспроизвести) палитровую битность исходника
	// (например 4-битную палитру градаций серого). 0 = 8.
	PNGPaletteBitDepth int
	// GIFBitDepth — битность палитры GIF [1..8] (меньше = компактнее,
	// до 256 цветов). 0 = умолчание govips (8).
	GIFBitDepth int
}

// DefaultEncoderParams — параметры по умолчанию (совпадают с историческим
// поведением адаптера: webp effort=4, png compression=6, jpeg progressive
// off, png interlace off, png palette off; avif/jxl — умолчания govips).
func DefaultEncoderParams() EncoderParams {
	return EncoderParams{
		WebPReductionEffort: 4,
		AVIFSpeed:           0, // govips default (5)
		PNGCompression:      6,
		JXLEffort:           0, // govips default (7)
		GIFBitDepth:         0, // govips default (8)
	}
}
