// Passthrough fast-path: возврат исходных байтов без decode/encode.
//
// Решение принимается ЧИСТОЙ функцией passthroughEligible над планом
// обработки и лёгкими метаданными исходника (только заголовок файла;
// пиксели libvips декодирует лениво). Политика — отказоустойчивость:
// при ЛЮБЫХ сомнениях (неизвестные размеры, лишние метаданные, ручная
// ориентация, ограничение кадров и т.п.) passthrough отклоняется и
// выполняется полная обработка.
//
// Файл без build-tag: логика не зависит от govips и тестируется в любой
// сборке (см. passthrough_test.go). Заголовок читается в process_libvips.go
// (tryPassthrough).
package libvips

import (
	"github.com/pkg-ru/imager/domain/processing"
)

// sourceInfo — лёгкие метаданные исходника, достаточные для решения о
// passthrough. Заполняется из заголовка изображения (без декодирования
// пикселей).
type sourceInfo struct {
	// Width — ширина кадра (px). 0 = неизвестна (отклоняет passthrough).
	Width int
	// Height — высота ОДНОГО кадра (для анимации — page-height, а не высота
	// всего стека). 0 = неизвестна (отклоняет passthrough).
	Height int
	// Pages — число страниц/кадров (1 для статичных изображений).
	Pages int
	// Orientation — EXIF Orientation исходника (0/1 = поворот не требуется).
	Orientation int
	// MetaFields — имена пользовательских полей метаданных (EXIF/XMP/IPTC/
	// комментарии кодеков и т.п.), видимых libvips в заголовке.
	MetaFields []string
	// HasICC — наличие ICC-профиля в исходнике.
	HasICC bool
	// SRGBProfile — true, если embedded-профиль sRGB-совместим (проверка по
	// сигнатуре/имени БЕЗ lcms-конверсии; см. isSRGBProfile). Значимо только
	// при HasICC=true. false при отсутствии/битости профиля.
	SRGBProfile bool
}

// passthroughMetaAllowlist — технические поля метаданных, которые полный
// конвейер и так сохраняет/пересоздаёт при экспорте (см. stripAllMetadata:
// RemoveMetadata сохраняет orientation/n-pages/page-height/delay/loop).
// Любое ДРУГОЕ поле (exif-data, xmp-data, iptc-data, комментарии кодеков,
// имя ICC-профиля и т.п.) требует полной обработки с зачисткой метаданных —
// passthrough не должен «просачивать» EXIF/GPS и прочее в выход.
var passthroughMetaAllowlist = map[string]bool{
	"orientation": true,
	"n-pages":     true,
	"page-height": true,
	"delay":       true,
	"loop":        true,
	"background":  true,
}

// passthroughEligible сообщает, можно ли вернуть исходные байты как есть:
//   - целевой формат совпадает с исходным;
//   - размер не меняется (size=x либо размеры уже совпадают);
//   - нет watermark/trim/детекции/ручной ориентации;
//   - EXIF auto-orient ничего не изменит (orientation 0/1);
//   - нет ограничения кадров/длительности и переопределения loop;
//   - DPR нейтрален (0/1);
//   - в исходнике нет метаданных вне allowlist (полный конвейер всегда
//     делает strip — passthrough обязан давать тот же результат по
//     приватности);
//   - ICC-профиль: в режиме strip отклоняется (конвейер удаляет профиль);
//     в режиме transform допускается sRGB-совместимый профиль (конверсия
//     была бы no-op); в режиме keep допускается любой профиль (профиль
//     сохраняется в выходе как есть).
//
// colorMode — политика color management (Фаза 5a; ColorStrip/ColorTransform/
// ColorKeep). Функция чистая и консервативная: любое сомнение → false.
func passthroughEligible(plan *processing.ProcessingPlan, src sourceInfo, colorMode ColorMode) bool {
	if plan == nil {
		return false
	}
	// Перекодирование формата — не passthrough.
	if plan.SourceFormat != plan.OutputFormat {
		return false
	}
	// Независимые фильтры и ватермарка меняют пиксели.
	if plan.Trim || plan.Watermark != nil {
		return false
	}
	// Детекторные операции (face-crop/object-crop) всегда перекраивают кадр.
	switch plan.Operation {
	case processing.OpResize, processing.OpCrop, processing.OpSmartCrop:
	default:
		return false
	}
	// Ограничение состава кадров анимации меняет выход.
	if plan.Frames > 0 || plan.Duration > 0 {
		return false
	}
	// Явное переопределение loop меняет метаданные анимации.
	if plan.Loop != nil {
		return false
	}
	// DPR отличный от единицы масштабирует целевой размер.
	if plan.DPR != 0 && plan.DPR != 1 {
		return false
	}
	// Ориентация: ручные rotate/flip запрещены; auto-orient безопасен,
	// только если EXIF Orientation отсутствует или нейтрален (<= 1).
	if or := plan.Orientation; or != nil {
		if or.Rotate != processing.RotationNone || or.Flip != processing.FlipNone {
			return false
		}
		if or.AutoOrient && src.Orientation > 1 {
			return false
		}
	}
	// Strip-необходимость: любые пользовательские метаданные или ICC-профиль
	// требуют полного конвейера (экспорт зачищает их, passthrough — нет).
	for _, f := range src.MetaFields {
		if !passthroughMetaAllowlist[f] {
			return false
		}
	}
	// ICC-профиль: политика color management (Фаза 5a).
	switch colorMode {
	case ColorTransform:
		// sRGB-совместимый профиль не требует конверсии (fast-path) —
		// passthrough допустим: профиль остаётся в выходе как есть.
		if src.HasICC && !src.SRGBProfile {
			return false
		}
	case ColorKeep:
		// Профиль сохраняется в выходе — passthrough допустим.
	default: // ColorStrip и неизвестные режимы (консервативно)
		if src.HasICC {
			return false
		}
	}
	// Размер: уже совпадает с целевым?
	return sizeMatches(plan.Size, src.Width, src.Height)
}

// sizeMatches проверяет, что целевой размер плана совпадает с фактическим
// размером исходника (для анимации передаётся размер одного кадра).
// size=x не зависит от размеров исходника (ресайза нет вовсе), поэтому
// passthrough применим даже при неизвестных размерах. Для явных размеров
// неизвестные значения (<= 0) отклоняются — отказоустойчивость.
func sizeMatches(s processing.Size, w, h int) bool {
	if s.Original {
		return true
	}
	if w <= 0 || h <= 0 {
		return false
	}
	switch {
	case s.Width > 0 && s.Height > 0:
		return s.Width == w && s.Height == h
	case s.Width > 0:
		// Пропорциональный resize по ширине: высота не изменится,
		// только если ширина уже целевая.
		return s.Width == w
	case s.Height > 0:
		return s.Height == h
	default:
		return false
	}
}
