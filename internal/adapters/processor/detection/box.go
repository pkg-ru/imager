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

// SelectCrop выбирает область кропа по найденным боксам.
//
// Параметры:
//   - boxes — обнаруженные объекты (уже после NMS);
//   - imgW, imgH — размеры исходного изображения;
//   - targetW, targetH — целевые размеры кропа (0 = не задано);
//   - margin — отступ к найденной области как доля от её размера (0-1).
//
// Алгоритм:
//  1. Пустой список боксов — fallback: центральный кроп с целевым aspect
//     ratio (или весь кадр, если целевой размер не задан);
//  2. Область — объединение всех валидных боксов (bounding box);
//  3. К области добавляется отступ margin (доля размера, половина с каждой
//     стороны);
//  4. Область подгоняется под целевой aspect ratio (расширение по
//     недостающей оси, чтобы вместить всю найденную область);
//  5. Итоговый прямоугольник (x, y, w, h) подгоняется к границам кадра
//     (clamp): сдвиг внутрь при выходе за границы, ужатие, если больше
//     кадра.
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

	// Объединение всех валидных боксов (bounding box).
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
		// Нет валидных боксов — fallback на центр.
		return centerCrop(imgW, imgH, targetW, targetH)
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
	cw := nr - nx
	ch := nb - ny

	// Подгонка под целевой aspect ratio (расширение, не ужимка).
	cw, ch = fitAspect(cw, ch, targetW, targetH)
	return fitRect(nx, ny, cw, ch, imgW, imgH)
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

// min и max для двух int.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
