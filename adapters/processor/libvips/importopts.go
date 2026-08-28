// Построение import-параметров из плана обработки. Файл без build-tag:
// логика не зависит от cgo и тестируется в любой сборке. Применение
// значений к vips.ImportParams выполняется в process_libvips.go.
//
// Семантика:
//   - NumPages: для анимированных входов/выходов загружаются ВСЕ кадры
//     (-1); при заданном plan.Frames (> 0) — не более N кадров (лимит
//     применяется на этапе загрузки, что дешевле пост-обрезки стека).
//     Для статичных изображений параметр НЕ выставляется (умолчание
//     libvips: n=1), как и раньше.
//   - Access: sequential mode выставляется только когда он безопасен —
//     операция выполняет ровно один линейный проход по пикселям
//     (thumbnail/extract + экспорт без повторных чтений). Для операций с
//     детекцией (face-crop/object-crop) пиксели читаются ДВАЖДЫ (RGB-
//     извлечение для модели + кроп), поэтому остаётся random access.
package libvips

import (
	"github.com/pkg-ru/imager/domain/processing"
)

// importPlan — решённые import-параметры (платформенно-независимые).
type importPlan struct {
	// NumPages — значение для NumPages (-1 = все кадры). setPages=false
	// означает «не выставлять» (статичные изображения).
	NumPages int
	SetPages bool
	// Sequential — выставить AccessSequential в import params.
	Sequential bool
}

// resolveImportPlan вычисляет import-параметры для плана.
func resolveImportPlan(plan *processing.ProcessingPlan) importPlan {
	p := importPlan{}
	if plan.OutputFormats.Animated() || plan.SourceFormat.Animated() {
		n := -1 // все кадры
		if plan.Frames > 0 {
			n = plan.Frames
		}
		p.NumPages = n
		p.SetPages = true
	}
	p.Sequential = sequentialSafe(plan)
	return p
}

// sequentialSafe сообщает, безопасен ли sequential access mode для плана:
// один линейный проход по пикселям (resize/crop/smart-crop через thumbnail,
// затем экспорт). Детекторные операции читают пиксели дважды; trim требует
// find-trim (полное сканирование) до extract — тоже не один проход.
func sequentialSafe(plan *processing.ProcessingPlan) bool {
	if plan.Trim {
		return false
	}
	switch plan.Operation {
	case processing.OpResize, processing.OpCrop, processing.OpSmartCrop:
		return true
	default:
		return false
	}
}
