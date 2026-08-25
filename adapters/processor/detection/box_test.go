package detection

import (
	"math"
	"testing"
)

func TestIou(t *testing.T) {
	a := Box{X: 0, Y: 0, W: 10, H: 10}
	// Идентичные боксы — IoU 1.
	if got := iou(a, a); got != 1 {
		t.Errorf("iou(same) = %v, want 1", got)
	}
	// Непересекающиеся — 0.
	b := Box{X: 100, Y: 100, W: 10, H: 10}
	if got := iou(a, b); got != 0 {
		t.Errorf("iou(disjoint) = %v, want 0", got)
	}
	// Пересечение 5x5 из 10x10 + 10x10: inter=25, union=175, iou=25/175.
	c := Box{X: 5, Y: 5, W: 10, H: 10}
	want := 25.0 / 175.0
	if got := iou(a, c); math.Abs(got-want) > 1e-9 {
		t.Errorf("iou(overlap) = %v, want %v", got, want)
	}
	// Нулевые размеры — 0.
	d := Box{X: 0, Y: 0, W: 0, H: 0}
	if got := iou(a, d); got != 0 {
		t.Errorf("iou(zero-size) = %v, want 0", got)
	}
}

func TestNMS(t *testing.T) {
	boxes := []Box{
		{X: 0, Y: 0, W: 100, H: 100, Confidence: 0.9},
		{X: 10, Y: 10, W: 100, H: 100, Confidence: 0.8}, // дубль (IoU высокий)
		{X: 300, Y: 300, W: 50, H: 50, Confidence: 0.7}, // отдельный объект
		{X: 0, Y: 0, W: 100, H: 100, Confidence: 0.4},   // дубль с низкой уверенностью
	}
	out := NMS(boxes, 0.45)
	if len(out) != 2 {
		t.Fatalf("NMS len = %d, want 2 (got %+v)", len(out), out)
	}
	// Результат отсортирован по confidence убыванию.
	if !(out[0].Confidence >= out[1].Confidence) {
		t.Errorf("NMS result not sorted by confidence: %v", out)
	}
	if out[0].Confidence != 0.9 || out[1].Confidence != 0.7 {
		t.Errorf("NMS result = %+v, want top two 0.9/0.7", out)
	}
	// Входной слайс не модифицируется.
	if len(boxes) != 4 {
		t.Errorf("input mutated: len = %d, want 4", len(boxes))
	}
}

func TestNMSNilEmpty(t *testing.T) {
	if out := NMS(nil, 0.45); out != nil {
		t.Errorf("NMS(nil) = %v, want nil", out)
	}
	if out := NMS([]Box{}, 0.45); out != nil {
		t.Errorf("NMS(empty) = %v, want nil", out)
	}
}

func TestNMSThresholdClamp(t *testing.T) {
	// Отрицательный и >1 порог не паникуют и не модифицируют вход.
	boxes := []Box{{X: 0, Y: 0, W: 10, H: 10, Confidence: 1.0}}
	if out := NMS(boxes, -1); len(out) != 1 {
		t.Errorf("NMS(threshold=-1) len = %d, want 1", len(out))
	}
	if out := NMS(boxes, 5); len(out) != 1 {
		t.Errorf("NMS(threshold=5) len = %d, want 1", len(out))
	}
}

// rectEq проверяет равенство двух Rect.
func rectEq(a, b Rect) bool {
	return a.X == b.X && a.Y == b.Y && a.W == b.W && a.H == b.H
}

func TestSelectCropFallbackCenter(t *testing.T) {
	// Пустой список боксов → центральный кроп с целевым aspect ratio.
	r := SelectCrop(nil, 200, 100, 100, 100, 0.1)
	// target 100x100 в кадре 200x100: центр по X, вся высота.
	if !rectEq(r, Rect{X: 50, Y: 0, W: 100, H: 100}) {
		t.Errorf("fallback center = %+v, want {50 0 100 100}", r)
	}

	// Пустой список + целевой размер не задан → весь кадр.
	r = SelectCrop(nil, 200, 100, 0, 0, 0.1)
	if !rectEq(r, Rect{X: 0, Y: 0, W: 200, H: 100}) {
		t.Errorf("fallback full = %+v, want full frame", r)
	}
}

func TestSelectCropUnion(t *testing.T) {
	boxes := []Box{
		{X: 100, Y: 40, W: 50, H: 50, Confidence: 0.9},
		{X: 160, Y: 60, W: 40, H: 40, Confidence: 0.8},
	}
	imgW, imgH := 300, 200
	// Без margin и без целевого ratio кроп = bounding box двух боксов.
	r := SelectCrop(boxes, imgW, imgH, 0, 0, 0)
	// bbox: x=[100,200], y=[40,100] → {100,40,100,60}
	want := Rect{X: 100, Y: 40, W: 100, H: 60}
	if !rectEq(r, want) {
		t.Errorf("union crop = %+v, want %+v", r, want)
	}
}

func TestSelectCropMarginAndAspect(t *testing.T) {
	boxes := []Box{{X: 100, Y: 100, W: 100, H: 100, Confidence: 0.9}}
	imgW, imgH := 1000, 1000

	// margin 0.2: добавляет по 10px с каждой стороны (20% от 100 / 2).
	r := SelectCrop(boxes, imgW, imgH, 0, 0, 0.2)
	want := Rect{X: 90, Y: 90, W: 120, H: 120}
	if !rectEq(r, want) {
		t.Errorf("margin crop = %+v, want %+v", r, want)
	}

	// Целевой aspect ratio 16:9 при квадратном кадре: расширение по ширине.
	r = SelectCrop(boxes, imgW, imgH, 160, 90, 0)
	// bbox 100x100 → расширяем до 160x90-ratio: w = h * (160/90) = 177.7 → 178.
	w := int(math.Round(100.0 * 160.0 / 90.0))
	if r.W != w || r.H != 100 {
		t.Errorf("aspect crop = %+v, want w=%d h=100", r, w)
	}
}

func TestSelectCropClampToFrame(t *testing.T) {
	// Бокс у края: margin выводит левую/верхнюю границу за кадр — clamp
	// сдвигает их в 0, правая/нижняя расширяется (отступ 13 = round(50*0.5/2)).
	boxes := []Box{{X: 0, Y: 0, W: 50, H: 50, Confidence: 0.9}}
	r := SelectCrop(boxes, 100, 100, 0, 0, 0.5)
	// Площадь: [0,0]-[63,63].
	want := Rect{X: 0, Y: 0, W: 63, H: 63}
	if !rectEq(r, want) {
		t.Errorf("clamp crop = %+v, want %+v", r, want)
	}

	// Сам бокс больше кадра (после clamp) — ужатие до кадра.
	big := []Box{{X: -100, Y: -100, W: 500, H: 500, Confidence: 0.9}}
	r = SelectCrop(big, 100, 100, 0, 0, 0)
	if r.X < 0 || r.Y < 0 || r.X+r.W > 100 || r.Y+r.H > 100 {
		t.Errorf("big box crop = %+v, want fully inside frame", r)
	}
	if r.W <= 0 || r.H <= 0 {
		t.Errorf("big box crop dims = %dx%d, want positive", r.W, r.H)
	}
}

func TestSelectCropEmptyImg(t *testing.T) {
	// Защита: некорректный кадр → пустой Rect.
	if r := SelectCrop(nil, 0, 100, 100, 100, 0.1); r != (Rect{}) {
		t.Errorf("invalid img = %+v, want zero Rect", r)
	}
}
