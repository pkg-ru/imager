// Package detection реализует чистую (без cgo/ONNX) логику детекции
// лиц/объектов для операций face-crop и object-crop.
//
// Файл box.go содержит только типы и алгоритмы (NMS, selectCrop) без
// внешних зависимостей: они полностью тестируемы и не требуют ни govips,
// ни ONNX Runtime. Реальная инференс-часть живёт в onnx.go (build tag
// "onnx"), заглушка — в onnx_stub.go (build tag "!onnx").
package detection

import (
	"math"
	"sort"
)

// Box — прямоугольник объекта, найденный детектором.
//
// Координаты (X, Y) — верхний левый угол, (W, H) — размеры. Координаты
// задаются в пикселях исходного изображения. Confidence — уверенность
// детектора в интервале [0,1]. Label — имя класса (для object crop;
// для лиц может быть пустой).
type Box struct {
	// X — координата левого верхнего угла по горизонтали (px).
	X int
	// Y — координата левого верхнего угла по вертикали (px).
	Y int
	// W — ширина бокса (px).
	W int
	// H — высота бокса (px).
	H int
	// Confidence — уверенность детектора в интервале [0,1].
	Confidence float64
	// Label — имя класса (необязательно).
	Label string
}

// iou вычисляет Intersection over Union двух боксов в интервале [0,1].
// Возвращает 0, если боксы не пересекаются или имеют нулевую площадь.
func iou(a, b Box) float64 {
	interW := min(a.X+a.W, b.X+b.W) - max(a.X, b.X)
	interH := min(a.Y+a.H, b.Y+b.H) - max(a.Y, b.Y)
	if interW <= 0 || interH <= 0 {
		return 0
	}
	inter := float64(interW) * float64(interH)
	union := float64(a.W*a.H) + float64(b.W*b.H) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// NMS выполняет подавление немаксимумов (Non-Maximum Suppression).
//
// Алгоритм:
//  1. Боксы сортируются по Confidence по убыванию;
//  2. Самый уверенный бокс добавляется в результат;
//  3. Остальные боксы, пересекающиеся с ним с IoU > iouThreshold,
//     отбрасываются;
//  4. Процесс повторяется для оставшихся.
//
// iouThreshold — порог перекрытия: боксы с IoU выше порога считаются
// дублирующими и подавляются. Обычные значения 0.4-0.5 для лиц,
// 0.4-0.6 для объектов. Входящий слайс не модифицируется. Результат
// отсортирован по confidence убыванию.
func NMS(boxes []Box, iouThreshold float64) []Box {
	if len(boxes) == 0 {
		return nil
	}
	if iouThreshold < 0 {
		iouThreshold = 0
	}
	if iouThreshold > 1 {
		iouThreshold = 1
	}

	sorted := make([]Box, len(boxes))
	copy(sorted, boxes)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Confidence > sorted[j].Confidence
	})

	out := make([]Box, 0, len(sorted))
	for len(sorted) > 0 {
		best := sorted[0]
		out = append(out, best)
		// Переиспользуем память sorted: оставшиеся элементы (без best)
		// фильтруются на месте, чтобы не копировать слайс на каждой
		// итерации.
		kept := sorted[:0]
		for _, b := range sorted[1:] {
			if iou(best, b) <= iouThreshold {
				kept = append(kept, b)
			}
		}
		sorted = kept
	}
	return out
}

// Rect — область кропа изображения (прямоугольник в пикселях).
type Rect struct {
	// X — левый край области.
	X int
	// Y — верхний край области.
	Y int
	// W — ширина области (px).
	W int
	// H — высота области (px).
	H int
}

// SelectCrop выбирает область кропа по найденным боксам (object-crop).
//
// Семантика идентична SelectFaceCrop (общее ядро detectionCropWindow):
//  1. Пустой список боксов — fallback: центральный кроп с целевым aspect
//     ratio (или весь кадр, если целевой размер не задан);
//  2. Область объекта = bounding box всех боксов + margin (симметричный);
//  3. Размер окна подгоняется под целевой aspect ratio (fitAspect —
//     расширение по недостающей оси, не ужимка);
//  4. Окно ЦЕНТРИРУЕТСЯ на центре области объекта и зажимается к границам
//     кадра (centeredClampedRect): объект остаётся в центре окна, окно у
//     края упирается в край, окно больше кадра — ужинается до кадра.
//
// Историческая семантика «от угла» (окно росло вправо/вниз от левого
// верхнего угла области через fitRect) заменена на центрированную: она
// асимметрично смещала объект к левому/верхнему краю окна при расширении
// fitAspect по недостающей оси — та же систематика, что и в face-crop.
//
// Параметры:
//   - boxes — обнаруженные объекты (уже после NMS);
//   - imgW, imgH — размеры исходного изображения;
//   - targetW, targetH — целевые размеры кропа (0 = не задано);
//   - margin — отступ к найденной области как доля от её размера (0-1).
//
// Возвращаемая область всегда имеет w,h >= 1 и полностью внутри кадра.
func SelectCrop(boxes []Box, imgW, imgH, targetW, targetH int, margin float64) Rect {
	if imgW <= 0 || imgH <= 0 {
		return Rect{} // защита от некорректного кадра
	}
	if targetW < 0 {
		targetW = 0
	}
	if targetH < 0 {
		targetH = 0
	}
	if margin < 0 {
		margin = 0
	}

	if len(boxes) == 0 {
		return centerCrop(imgW, imgH, targetW, targetH)
	}

	// Объединение всех валидных боксов (bounding box) + отступ margin.
	region, ok := unionWithMargin(boxes, imgW, imgH, margin)
	if !ok {
		// Нет валидных боксов — fallback на центр.
		return centerCrop(imgW, imgH, targetW, targetH)
	}

	return detectionCropWindow(region, imgW, imgH, targetW, targetH, false)
}

// detectionCropWindow — общее ядро детекторных кропов (face-crop и
// object-crop): строит окно кропа из области интереса region (bbox+margin):
// размер окна = fitAspect(region) под целевой аспект, позиция = центр region,
// зажатый к границам кадра (centeredClampedRect). fitCrop=true (face-crop)
// достраивает отсутствующую сторону целевого размера по аспекту кадра;
// fitCrop=false (object-crop) использует target как есть.
func detectionCropWindow(region Rect, imgW, imgH, targetW, targetH int, fitCrop bool) Rect {
	if fitCrop {
		if targetW <= 0 {
			targetW = int(math.Round(float64(targetH) * float64(imgW) / float64(imgH)))
			if targetW < 1 {
				targetW = 1
			}
		}
		if targetH <= 0 {
			targetH = int(math.Round(float64(targetW) * float64(imgH) / float64(imgW)))
			if targetH < 1 {
				targetH = 1
			}
		}
	}

	// Размер окна в исходных пикселях: расширяем область интереса под аспект
	// целевого ассета (после ресайза кропа объект сохраняет стабильную долю
	// кадра). Целевой аспект не задан (одна из сторон <= 0) — окно = область.
	cw, ch := fitAspect(region.W, region.H, targetW, targetH)
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}

	// Центр окна = центр области интереса (+margin), с clamp к границам.
	cx := region.X + region.W/2
	cy := region.Y + region.H/2
	return centeredClampedRect(cx, cy, cw, ch, imgW, imgH)
}

// SelectFaceCrop выбирает область кропа для face-crop: окно масштабируется
// так, чтобы область лица (bounding box + margin) занимала разумную долю
// кадра, и центрируется на центре области лица, с clamp к границам кадра.
//
// Параметры:
//   - boxes — обнаруженные лица (уже после NMS);
//   - imgW, imgH — размеры исходного изображения;
//   - targetW, targetH — размер итогового ассета (после ресайза кропа;
//     0 = сторона выводится по аспекту кадра, оба нуля = окно равно
//     области лица с отступом margin);
//   - margin — отступ к найденной области как доля от её размера (0-1).
//
// Алгоритм:
//  1. Область лица = bounding box всех боксов + margin (unionWithMargin);
//  2. Размер окна в пикселях ОРИГИНАЛА вычисляется подгонкой области лица
//     под целевой aspect ratio (fitAspect — расширение по недостающей оси),
//     т.е. окно масштабируется вместе с лицом: после ресайза кропа до
//     целевого размера ассета лицо занимает стабильную долю кадра;
//  3. Окно центрируется на центре области лица (cx, cy);
//  4. Clamp к границам кадра (centeredClampedRect): окно у края упирается
//     в край, окно больше кадра по оси — ужинается до кадра.
//
// Возвращаемая область всегда имеет w,h >= 1 и полностью внутри кадра.
func SelectFaceCrop(boxes []Box, imgW, imgH, targetW, targetH int, margin float64) Rect {
	if imgW <= 0 || imgH <= 0 {
		return Rect{} // защита от некорректного кадра
	}
	if targetW < 0 {
		targetW = 0
	}
	if targetH < 0 {
		targetH = 0
	}
	if margin < 0 {
		margin = 0
	}

	fallback := func() Rect {
		if targetW <= 0 && targetH <= 0 {
			return Rect{X: 0, Y: 0, W: imgW, H: imgH}
		}
		return centerCrop(imgW, imgH, targetW, targetH)
	}

	if len(boxes) == 0 {
		return fallback()
	}

	// Область лица (bounding box + margin) — из неё берётся и размер окна,
	// и точка центрирования.
	region, ok := unionWithMargin(boxes, imgW, imgH, margin)
	if !ok {
		return fallback()
	}

	if targetW <= 0 && targetH <= 0 {
		// Целевой размер не задан: окно = область лица (+margin).
		return fitRect(region.X, region.Y, region.W, region.H, imgW, imgH)
	}

	return detectionCropWindow(region, imgW, imgH, targetW, targetH, true)
}

// SelectFaceFixCrop выбирает cover-окно кропа для face-fix: изображение
// масштабируется ровно до целевого размера (cover, БЕЗ зума в лицо), и
// обрезается ТОЛЬКО по оси, пропорционально избыточной в оригинале.
//
// Отличие от SelectFaceCrop: в face-crop окно зумится в область лица
// (face-area расширяется до аспекта цели через fitAspect — лицо занимает
// стабильную долю кадра). В face-fix зума НЕТ: полная (непропорционально-
// избыточная) сторона сохраняется целиком, окно имеет точный целевой аспект,
// а по избыточной оси центрируется на центре face-area (bbox всех лиц +
// margin) с clamp к границам кадра (лицо у края — окно упирается в край,
// лицо остаётся смещённым, но не обрезается).
//
// Параметры идентичны SelectFaceCrop. Возвращаемая область всегда имеет
// w,h >= 1 и полностью внутри кадра; её аспект совпадает с целевым (с
// точностью округления), поэтому последующий ThumbnailWithSize SizeBoth не
// докручивает геометрию.
func SelectFaceFixCrop(boxes []Box, imgW, imgH, targetW, targetH int, margin float64) Rect {
	return detectionFixCropWindow(boxes, imgW, imgH, targetW, targetH, margin)
}

// SelectObjectFixCrop — cover-окно кропа для object-fix; семантика
// идентична SelectFaceFixCrop, но область интереса строится по боксам
// объектов (детектор объектов), а не лиц.
func SelectObjectFixCrop(boxes []Box, imgW, imgH, targetW, targetH int, margin float64) Rect {
	return detectionFixCropWindow(boxes, imgW, imgH, targetW, targetH, margin)
}

// detectionFixCropWindow — общее ядро fix-кропов (face-fix и object-fix):
// cover-масштаб без зума в область интереса.
//
// Алгоритм:
//  1. Сравнение аспектов оригинала (imgW/imgH) и цели (targetW/targetH)
//     кросс-умножением (без деления с плавающей точкой);
//  2. Оригинал «шире» по пропорции (imgW*targetH > targetW*imgH) — масштаб
//     по высоте (scale = targetH/imgH): окно в исходных координатах =
//     (targetW/scale) × imgH — полная высота, вертикаль НЕ режется вовсе;
//     кроп только по X;
//  3. Иначе (оригинал «выше» или аспекты равны) — масштаб по ширине: окно =
//     imgW × (targetH/scale), горизонталь не режется; кроп только по Y.
//     При равных аспектах окно вырождается в весь кадр;
//  4. Позиция по избыточной оси: центр окна = центр области интереса
//     (bbox + margin, unionWithMargin), затем clamp к границам оригинала
//     по этой оси (clampInt);
//  5. Нет боксов / нет валидной области — fallback: центральное
//     позиционирование по избыточной оси (эквивалент центрального кропа);
//  6. Цель задана не полностью (одна из сторон <= 0) — cover не определён,
//     fallback: центральный кроп с целевым аспектом (centerCrop), цель
//     0x0 — весь кадр.
func detectionFixCropWindow(boxes []Box, imgW, imgH, targetW, targetH int, margin float64) Rect {
	if imgW <= 0 || imgH <= 0 {
		return Rect{} // защита от некорректного кадра
	}
	if targetW < 0 {
		targetW = 0
	}
	if targetH < 0 {
		targetH = 0
	}
	if margin < 0 {
		margin = 0
	}
	if targetW <= 0 || targetH <= 0 {
		// Cover требует обоих измерений цели; вырожденные случаи —
		// fallback: центральный кроп (0x0 → весь кадр).
		return centerCrop(imgW, imgH, targetW, targetH)
	}

	// Центр области интереса по избыточной оси; без боксов — центр кадра
	// (fallback: центральное позиционирование, эквивалент центрального кропа).
	// Возвращает координату окна по оси: центр области интереса (или центр
	// кадра по этой оси при отсутствии боксов — fallback в центр) минус
	// half (пол-окна), зажатый в диапазон [0, maxOffset], где maxOffset =
	// размер кадра - размер окна по этой оси: лицо у края — окно упирается
	// в край.
	centerOf := func(axis func(Rect) int, defCenter, half, maxOffset int) int {
		c := defCenter
		if region, ok := unionWithMargin(boxes, imgW, imgH, margin); ok {
			c = axis(region)
		}
		return clampInt(c-half, 0, maxOffset)
	}

	if float64(imgW)*float64(targetH) > float64(targetW)*float64(imgH) {
		// Оригинал пропорционально ШИРЕ цели: масштаб по высоте
		// (scale = targetH/imgH), избыточна только ШИРИНА → кроп только
		// по X; полная высота сохраняется.
		w := int(math.Round(float64(targetW) / float64(targetH) * float64(imgH)))
		w = clampInt(w, 1, imgW)
		x := centerOf(func(r Rect) int { return r.X + r.W/2 }, imgW/2, w/2, imgW-w)
		return Rect{X: x, Y: 0, W: w, H: imgH}
	}

	// Оригинал пропорционально ВЫШЕ цели (или аспекты равны): масштаб по
	// ширине (scale = targetW/imgW), избыточна только ВЫСОТА → кроп только
	// по Y; полная ширина сохраняется. Равные аспекты → окно = весь кадр.
	h := int(math.Round(float64(targetH) / float64(targetW) * float64(imgW)))
	h = clampInt(h, 1, imgH)
	y := centerOf(func(r Rect) int { return r.Y + r.H/2 }, imgH/2, h/2, imgH-h)
	return Rect{X: 0, Y: y, W: imgW, H: h}
}

// boxFromEdges строит Box из вещественных координат краёв (в пикселях кадра),
// СИММЕТРИЧНО округляя каждый край через math.Round (не truncate):
// int(x1) (floor для положительных) + усечение ширины int(x2-x1) давали
// систематическое смещение центра бокса ВЛЕВО/ВВЕРХ до ~1px, которое
// наследовал центрированный face/object-crop. ok=false при вырожденном
// боксе (неположительные размеры после округления).
func boxFromEdges(x1, y1, x2, y2 float64) (Box, bool) {
	X := int(math.Round(x1))
	Y := int(math.Round(y1))
	W := int(math.Round(x2)) - X
	H := int(math.Round(y2)) - Y
	if W <= 0 || H <= 0 {
		return Box{}, false
	}
	return Box{X: X, Y: Y, W: W, H: H}, true
}

// unionWithMargin объединяет все валидные боксы в bounding box, зажимает его
// в кадр [0,imgW)x[0,imgH) и добавляет отступ margin (доля от размеров
// области, половина с каждой стороны), зажимая результат в кадр.
// ok=false, если валидных боксов нет (или область вырождена).
func unionWithMargin(boxes []Box, imgW, imgH int, margin float64) (Rect, bool) {
	minX, minY := math.MaxInt32, math.MaxInt32
	maxX, maxY := 0, 0
	has := false
	for _, b := range boxes {
		if b.W <= 0 || b.H <= 0 {
			continue
		}
		l := clampInt(b.X, 0, imgW-1)
		t := clampInt(b.Y, 0, imgH-1)
		r := clampInt(b.X+b.W, 1, imgW)
		bo := clampInt(b.Y+b.H, 1, imgH)
		if l < minX {
			minX = l
		}
		if t < minY {
			minY = t
		}
		if r > maxX {
			maxX = r
		}
		if bo > maxY {
			maxY = bo
		}
		has = true
	}
	if !has || maxX <= minX || maxY <= minY {
		return Rect{}, false
	}

	w := maxX - minX
	h := maxY - minY

	// Отступ: margin — доля от размеров области; половина с каждой стороны.
	padX := int(math.Round(float64(w) * margin / 2))
	padY := int(math.Round(float64(h) * margin / 2))
	nx := clampInt(minX-padX, 0, imgW-1)
	ny := clampInt(minY-padY, 0, imgH-1)
	nr := clampInt(maxX+padX, 1, imgW)
	nb := clampInt(maxY+padY, 1, imgH)

	// Гарантируем положительные размеры области даже при нулевом отступе.
	if nr <= nx {
		nr = nx + 1
		if nr > imgW {
			nr = imgW
			nx = nr - 1
		}
	}
	if nb <= ny {
		nb = ny + 1
		if nb > imgH {
			nb = imgH
			ny = nb - 1
		}
	}
	return Rect{X: nx, Y: ny, W: nr - nx, H: nb - ny}, true
}

// centeredClampedRect возвращает окно w×h, центрированное в точке (cx, cy)
// и зажатое в границы кадра [0,imgW)x[0,imgH): при выходе за границу окно
// сдвигается к краю (объект остаётся смещённым, но кадр не выходит за
// пределы изображения); если окно больше кадра по оси — сторона ужинается
// до кадра (позиция 0 = по центру кадра).
//
// Целочисленная симметрия: x = cx - w/2 (целочисленное деление) задаёт окно
// как полуинтервал [cx-w/2, cx+w/2) — он СИММЕТРИЧЕН относительно cx и для
// чётных, и для нечётных w (нечётное окно 2k+1 накрывает [cx-k, cx+k] —
// центральный пиксель стоит точно в cx). Альтернатива cx-(w-1)/2 при чётном
// окне давала бы окно [cx-w/2+1, ...) с центром cx+0.5 — систематический
// сдвиг объекта влево на полпикселя.
func centeredClampedRect(cx, cy, w, h, imgW, imgH int) Rect {
	if w > imgW {
		w = imgW
	}
	if h > imgH {
		h = imgH
	}
	x := cx - w/2
	y := cy - h/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x+w > imgW {
		x = imgW - w
	}
	if y+h > imgH {
		y = imgH - h
	}
	// Повторная проверка после сдвига (страховка от негативных координат).
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return Rect{X: x, Y: y, W: w, H: h}
}

// centerCrop возвращает центральный кроп кадра с целевым aspect ratio.
// Используется как fallback при пустом списке боксов. Если целевой размер
// не задан — возвращается весь кадр.
func centerCrop(imgW, imgH, targetW, targetH int) Rect {
	if targetW <= 0 && targetH <= 0 {
		return Rect{X: 0, Y: 0, W: imgW, H: imgH}
	}
	if targetW <= 0 {
		targetW = int(math.Round(float64(imgW) * float64(targetH) / float64(imgH)))
	}
	if targetH <= 0 {
		targetH = int(math.Round(float64(imgH) * float64(targetW) / float64(imgW)))
	}
	x := (imgW - targetW) / 2
	y := (imgH - targetH) / 2
	return fitRect(x, y, targetW, targetH, imgW, imgH)
}

// fitAspect расширяет (w, h) под целевой aspect ratio (targetW/targetH),
// не уменьшая размеры. Если целевой ratio не задан — возвращает как есть.
func fitAspect(w, h, targetW, targetH int) (int, int) {
	if w <= 0 || h <= 0 {
		return w, h
	}
	if targetW <= 0 || targetH <= 0 {
		return w, h
	}
	cur := float64(w) / float64(h)
	tgt := float64(targetW) / float64(targetH)
	if cur < tgt {
		// Расширяем по ширине: w' = h * target.
		w = int(math.Round(float64(h) * tgt))
	} else if cur > tgt {
		// Расширяем по высоте: h' = w / target.
		h = int(math.Round(float64(w) / tgt))
	}
	return w, h
}

// fitRect подгоняет прямоугольник к границам кадра [0,imgW)x[0,imgH):
// сдвигает внутрь при выходе за границы и ужимает, если больше кадра.
func fitRect(x, y, w, h, imgW, imgH int) Rect {
	if w <= 0 || h <= 0 || imgW <= 0 || imgH <= 0 {
		return Rect{}
	}
	if w > imgW {
		w = imgW
	}
	if h > imgH {
		h = imgH
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x+w > imgW {
		x = imgW - w
	}
	if y+h > imgH {
		y = imgH - h
	}
	// Повторная проверка после сдвига (страховка от негативных координат).
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return Rect{X: x, Y: y, W: w, H: h}
}

// clampInt ограничивает v диапазоном [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
