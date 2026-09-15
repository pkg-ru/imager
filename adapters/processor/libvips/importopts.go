// Построение import-параметров из плана обработки. Файл без build-tag:
// логика не зависит от cgo и тестируется в любой сборке. Применение
// значений к vips.ImportParams выполняется в process_libvips.go.
//
// Семантика:
//   - NumPages: для анимированных ВЫХОДОВ (или анимированного входа при
//     анимированном выходе) загружаются ВСЕ кадры (-1); при заданном
//     plan.Frames (> 0) — не более N кадров (лимит применяется на этапе
//     загрузки, что дешевле пост-обрезки стека).
//   - НЕ-анимационный выход (например, JPEG) из анимированного источника
//     загружает ТОЛЬКО ПЕРВЫЙ кадр (n=1): формат не поддерживает анимацию,
//     а экспортёр (jpegsave и др.) не умеет сам выбирать кадр из
//     вертикального стека — без этого ограничения на выходе получилась бы
//     «простыня» (все кадры друг под другом) вместо одного кадра. Все
//     последующие фильтры применяются уже к единственному кадру.
//   - Для статичных изображений параметр НЕ выставляется (умолчание
//     libvips: n=1), как и раньше.
//   - Access: sequential mode выставляется только когда он безопасен —
//     операция выполняет ровно один линейный проход по пикселям
//     (thumbnail/extract + экспорт без повторных чтений). Для операций с
//     детекцией (face-crop/object-crop) пиксели читаются ДВАЖДЫ (RGB-
//     извлечение для модели + кроп), поэтому остаётся random access.
package libvips

import (
	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// importPlan — решённые import-параметры (платформенно-независимые).
type importPlan struct {
	// NumPages — значение для NumPages (-1 = все кадры, 1 = только первый
	// кадр). setPages=false означает «не выставлять» (статичные
	// изображения).
	NumPages int
	SetPages bool
	// Sequential — выставить AccessSequential в import params.
	Sequential bool
}

// resolveImportPlan вычисляет import-параметры для плана.
func resolveImportPlan(plan *processing.ProcessingPlan) importPlan {
	p := importPlan{}
	// Анимационный ВЫХОД: нужны все кадры (или лимит). Также случай
	// анимированный-вход/анимированный-выход (например WebP→GIF) — тот же
	// путь: все кадры.
	if plan.OutputFormats.Animated() {
		n := -1 // все кадры
		if plan.Frames > 0 {
			n = plan.Frames
		}
		p.NumPages = n
		p.SetPages = true
	} else if plan.SourceFormat.Animated() {
		// НЕ-анимационный выход из анимированного источника: анимация на
		// выходе невозможна — нужен ОДИН (первый) кадр. Загружаем n=1
		// (page=0 по умолчанию), чтобы все последующие фильтры
		// (ориентация, trim, resize, crop, watermark, детекции) применялись
		// только к выбранному кадру, а экспортёр не писал «простыню» всех
		// кадров столбиком.
		p.NumPages = 1
		p.SetPages = true
	}
	// Статичный вход → статичный выход: параметр не выставляется
	// (умолчание libvips: n=1).
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
