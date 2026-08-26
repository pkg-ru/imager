// Shrink-on-load: предварительное уменьшение при декодировании JPEG/WebP/
// GIF/HEIF/AVIF. Кодеки libvips умеют декодировать сразу в уменьшенном
// масштабе (JPEG — shrink степени двойки 1/2/1/4/1/8; WebP/HEIF/AVIF/GIF —
// произвольный scale), что на порядки дешевле полного декодирования с
// последующим resize для больших исходников.
//
// Решение принимается ЧИСТОЙ функцией shrinkOnLoadFactor над планом и
// лёгкими метаданными заголовка (размеры/страницы уже прочитаны для
// passthrough-проверки). Политика — отказоустойчивость: коэффициент берётся
// С ЗАПАСОМ ×2 относительно целевого размера (после shrink размер гарантированно
// >= цели, точный resize всё равно выполняется далее через Thumbnail), а при
// ЛЮБЫХ сомнениях (точная геометрия до ресайза, анимационные тонкости,
// неизвестные размеры) shrink-on-load НЕ применяется.
//
// Файл без build-tag: логика не зависит от govips и тестируется в любой
// сборке (см. shrinkonload_test.go). Применение параметров к ImportParams —
// в load (process_libvips.go).
package libvips

import (
	"github.com/pkg-ru/imager/domain/processing"
)

// shrinkOnLoadInfo — лёгкие метаданные исходника, достаточные для решения о
// shrink-on-load. Заполняется из заголовка изображения (тот же head, что и
// для passthrough-проверки).
type shrinkOnLoadInfo struct {
	// Width — ширина кадра (px). 0 = неизвестна (отклоняет shrink).
	Width int
	// Height — высота ОДНОГО кадра (для анимации — page-height). 0 =
	// неизвестна (отклоняет shrink).
	Height int
	// Pages — число страниц/кадров (1 для статичных изображений).
	Pages int
}

// jpegShrinkFactors — допустимые значения shrink-on-load JPEG (степени
// двойки: 1 = без уменьшения, 2 = 1/2, 4 = 1/4, 8 = 1/8).
var jpegShrinkFactors = [4]int{1, 2, 4, 8}

// shrinkSupportedFormat сообщает, поддерживает ли формат источника
// shrink/scale-on-load в libvips.
func shrinkSupportedFormat(f processing.Format) bool {
	switch f {
	case processing.FormatJPEG, processing.FormatWebP, processing.FormatGIF,
		processing.FormatHEIF, processing.FormatAVIF:
		return true
	default:
		return false
	}
}

// shrinkGeometrySafe сообщает, не требует ли план ТОЧНОЙ геометрии до
// ресайза:
//   - trim сканирует края исходника — уменьшение исказило бы результат;
//   - smart-crop ищет attention-область — её положение сместится;
//   - детекторные операции (face/object-crop) работают по координатам
//     боксов в координатах ОРИГИНАЛА;
//   - watermark с позиционированием от краёв применяется ПОСЛЕ ресайза
//     (геометрия холста уже целевая), поэтому сам по себе watermark НЕ
//     блокирует shrink; но для консервативности он также отключает
//     shrink-on-load: раскладка repeat/round зависит от целевого размера,
//     который после shrink достигается тем же Thumbnail — влияние нулевое,
//     однако при сомнении отказоустойчивость важнее микровыигрыша.
func shrinkGeometrySafe(plan *processing.ProcessingPlan) bool {
	if plan.Trim {
		return false
	}
	// Watermark: накладывается после ресайза, но для консистентности
	// раскладки (repeat/round зависят от целевого холста) отключаем
	// shrink-on-load консервативно.
	if plan.Watermark != nil {
		return false
	}
	switch plan.Operation {
	case processing.OpResize, processing.OpCrop:
		return true
	case processing.OpSmartCrop, processing.OpFaceCrop, processing.OpObjectCrop:
		return false
	default:
		return false
	}
}

// shrinkOrientationNeutral сообщает, нейтральна ли ориентация для
// shrink-on-load. Ручные rotate/flip применяются до ресайза и меняют оси
// геометрии: при повороте на 90/270 оси меняются местами, поэтому фактор
// пришлось бы считать по «повёрнутым» размерам — при сомнении отключаем.
// EXIF auto-orient безопасен только при нейтральной ориентации исходника
// (0/1): иначе после авторазворота оси могут поменяться.
func shrinkOrientationNeutral(plan *processing.ProcessingPlan, srcExifOrientation int) bool {
	if or := plan.Orientation; or != nil && !or.IsZero() {
		if or.Rotate != processing.RotationNone || or.Flip != processing.FlipNone {
			return false
		}
		if or.AutoOrient && srcExifOrientation > 1 {
			return false
		}
	}
	return true
}

// targetBounds возвращает верхнюю границу целевых размеров плана с учётом
// DPR. Возвращает false, если план не задаёт явного размера (size=x) или
// размеры некорректны.
func targetBounds(plan *processing.ProcessingPlan) (w, h int, ok bool) {
	s := plan.Size
	if s.Original || (s.Width <= 0 && s.Height <= 0) {
		return 0, 0, false
	}
	dpr := plan.DPR
	if dpr <= 0 {
		dpr = 1
	}
	w = s.Width * dpr
	h = s.Height * dpr
	return w, h, true
}

// shrinkFactorForSource вычисляет общий коэффициент уменьшения (>= 1) так,
// чтобы после применения ОБА измерения остались >= целевых (запас ×2
// относительно цели: цель*2 <= исходник). Если исходник уже близок к цели
// (меньше, чем 2× цели) — shrink не даёт выигрыша и возвращается 1.
//
// Для пропорционального resize (задана одна ось) вторая ось вычисляется из
// пропорций исходника; для консервативности фактор ограничивается по ОБЕИМ
// осям (берётся минимум коэффициентов).
func shrinkFactorForSource(plan *processing.ProcessingPlan, src shrinkOnLoadInfo) float64 {
	tw, th, ok := targetBounds(plan)
	if !ok || src.Width <= 0 || src.Height <= 0 {
		return 1
	}
	// Пропорциональный resize: недостающая ось выводится из пропорций.
	if tw == 0 {
		tw = scaleByRatio(th, src.Width, src.Height)
	}
	if th == 0 {
		th = scaleByRatio(tw, src.Height, src.Width)
	}
	if tw <= 0 || th <= 0 {
		return 1
	}
	// Запас ×2: после shrink размер должен остаться >= цели. Коэффициент
	// по каждой оси = floor(исходник / (цель*2)); берём минимум по осям.
	fx := float64(src.Width) / float64(tw*2)
	fy := float64(src.Height) / float64(th*2)
	f := fx
	if fy < f {
		f = fy
	}
	if f < 1 {
		return 1
	}
	return f
}

// scaleByRatio вычисляет пропорциональный размер: side * (num/den),
// округление вверх (чтобы не занизить цель).
func scaleByRatio(side, num, den int) int {
	if den <= 0 {
		return 0
	}
	return (side*num + den - 1) / den
}

// jpegShrinkOnLoad переводит вещественный коэффициент в допустимое значение
// shrink-on-load JPEG (1/2/4/8): берётся НАИБОЛЬШЕЕ допустимое значение,
// при котором уменьшение всё ещё оставляет размер >= цели (т.е. наибольшая
// степень двойки <= коэффициента с запасом).
func jpegShrinkOnLoad(factor float64) int {
	result := 1
	for _, s := range jpegShrinkFactors {
		if float64(s) <= factor {
			result = s
		}
	}
	return result
}

// scaleShrinkOnLoad переводит вещественный коэффициент в scale-on-load
// WebP/HEIF/AVIF/GIF: обратная величина коэффициента, округлённая ВНИЗ до
// двух знаков (консервативно: чуть меньший scale → больший результат ≥ цели).
func scaleShrinkOnLoad(factor float64) float64 {
	if factor < 1 {
		return 1
	}
	scale := 1 / factor
	// Округление вниз до сотых: 0.1873 → 0.18 (запас в пользу качества).
	return float64(int(scale*100)) / 100
}

// shrinkOnLoadDecision — решённые параметры shrink-on-load для конкретного
// формата источника.
type shrinkOnLoadDecision struct {
	// JpegShrink — значение shrink-on-load JPEG (1 = не применять).
	JpegShrink int
	// Scale — значение scale-on-load WebP/HEIF/AVIF/GIF (1 = не применять).
	Scale float64
}

// applied сообщает, применяются ли какие-либо параметры shrink-on-load.
func (d shrinkOnLoadDecision) applied() bool { return d.JpegShrink > 1 || d.Scale < 1 }

// resolveShrinkOnLoad — чистая функция принятия решения о shrink-on-load.
//
// Правила отказаоустойчивости (любое сомнение → решение «не применять»):
//   - конфигурационный выключатель (enabled=false);
//   - формат источника не поддерживает shrink/scale-on-load;
//   - план требует точной геометрии до ресайза (trim/smart-crop/детекция);
//   - ручная ориентация или EXIF auto-orient с ненейтральной ориентацией
//     исходника (оси могут поменяться);
//   - size=x (ресайза нет вовсе);
//   - неизвестные размеры исходника;
//   - анимации: shrink-on-load для multi-page входов НЕ применяется, если
//     это GIF (scale-on-load GIF в некоторых сборках libvips работает
//     покадрово непредсказуемо); для остальных форматов анимация допустима,
//     т.к. scale применяется равномерно ко всем кадрам стека и сохраняет
//     page-height пропорционально. При plan.Frames > 0 лимит кадров
//     применяется на этапе загрузки независимо от scale — совместимо.
func resolveShrinkOnLoad(plan *processing.ProcessingPlan, src shrinkOnLoadInfo, exifOrientation int, enabled bool) shrinkOnLoadDecision {
	none := shrinkOnLoadDecision{JpegShrink: 1, Scale: 1}
	if !enabled || plan == nil {
		return none
	}
	if !shrinkSupportedFormat(plan.SourceFormat) {
		return none
	}
	if !shrinkGeometrySafe(plan) {
		return none
	}
	if !shrinkOrientationNeutral(plan, exifOrientation) {
		return none
	}
	if plan.Size.Original {
		return none
	}
	// Анимации: GIF — не рискуем (см. выше); остальные форматы — scale
	// равномерно по кадрам, безопасно.
	if src.Pages > 1 && plan.SourceFormat == processing.FormatGIF {
		return none
	}
	factor := shrinkFactorForSource(plan, src)
	if factor <= 1 {
		return none
	}
	switch plan.SourceFormat {
	case processing.FormatJPEG:
		d := shrinkOnLoadDecision{JpegShrink: jpegShrinkOnLoad(factor), Scale: 1}
		if d.JpegShrink <= 1 {
			return none
		}
		return d
	default:
		scale := scaleShrinkOnLoad(factor)
		if scale >= 1 {
			return none
		}
		return shrinkOnLoadDecision{JpegShrink: 1, Scale: scale}
	}
}
