// DPI-нормализация (Волна 5d): сброс xres/yres к стандартному разрешению.
//
// Файл без build-tag: решение «нужна ли нормализация» платформенно-независимо
// и тестируется в любой сборке (см. dpi_test.go). Фактическое применение к
// vips.ImageRef — в process_libvips.go (normalizeResolution в exportImage).
package libvips

// defaultResolutionDPI — стандартное разрешение (DPI), к которому
// нормализуются xres/yres при экспорте, чтобы просмотрщики не масштабировали
// изображение по DPI-метаданным исходника (например, 300 DPI из сканера).
const defaultResolutionDPI = 72.0

// pixelsPerInch — число миллиметров в одном дюйме. libvips хранит разрешение
// в пикселях на миллиметр (px/mm), а не в DPI: 72 DPI = 72/25.4 ≈ 2.8346 px/mm.
// Все сравнения и запись разрешения выполняются в единицах px/mm.
const pixelsPerInch = 25.4

// dpiToPxPerMM переводит разрешение из DPI в пиксели на миллиметр — единицы,
// в которых libvips хранит xres/yres (см. ResX/ResY и CopyChangingResolution).
func dpiToPxPerMm(dpi float64) float64 {
	return dpi / pixelsPerInch
}

// resolutionEpsilon — допустимое отклонение от целевого разрешения при
// сравнении в px/mm (float64; изображения уже нормализованные не
// перекопируются лишний раз). 0.5 px/mm ≈ 12.7 DPI.
const resolutionEpsilon = 0.5

// dpiAbsentEpsilon — порог (в px/mm), ниже которого xres/yres считаются «без
// DPI-метаданных». libvips по умолчанию держит xres=yres=1.0 для
// изображений без разрешения (0 в некоторых конвейерах): такие выходы
// просмотрщики показывают 1:1 (PNG без pHYs, JPEG без JFIF density) —
// масштабирование по DPI им не грозит, нормализация не требуется.
const dpiAbsentEpsilon = 1.5

// needsResolutionNormalization сообщает, отличается ли разрешение
// изображения (xres/yres, в px/mm) от стандартного DPI настолько, что
// требуется нормализация (создание копии в cgo-слое).
//
// Решение дешёвое — сравнение нескольких float64 с эпсилонами. Два быстрых
// пути без дополнительной копии:
//   - xres/yres уже равны целевому разрешению (72 DPI = 2.8346 px/mm) —
//     большинство содержимого, грузившегося с явными 72;
//   - xres/yres ≈ 0 или 1 (конвенция libvips «нет DPI-метаданных») —
//     просмотрщики показывают 1:1, сбрасывать нечего.
//
// Нормализуются только значимые отличные разрешения (96/144/300/1200 DPI —
// сканы, принт, экранные экспорты), которые реально масштабируют вьюеры.
func needsResolutionNormalization(xres, yres float64, targetDPI float64) bool {
	if targetDPI <= 0 {
		targetDPI = defaultResolutionDPI
	}
	targetPxPerMm := dpiToPxPerMm(targetDPI)
	abs := func(v float64) float64 {
		if v < 0 {
			return -v
		}
		return v
	}
	// «Нет DPI-метаданных»: отсутствует значимое разрешение.
	if abs(xres) <= dpiAbsentEpsilon && abs(yres) <= dpiAbsentEpsilon {
		return false
	}
	return abs(xres-targetPxPerMm) > resolutionEpsilon ||
		abs(yres-targetPxPerMm) > resolutionEpsilon
}
