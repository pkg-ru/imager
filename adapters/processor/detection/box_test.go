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
	// Семантика центрированная (общее ядро с face-crop): окно 178x100
	// центрируется на центре bbox (150,150): x=150-89=61, y=150-50=100.
	r = SelectCrop(boxes, imgW, imgH, 160, 90, 0)
	// bbox 100x100 → расширяем до 160x90-ratio: w = h * (160/90) = 177.7 → 178.
	w := int(math.Round(100.0 * 160.0 / 90.0))
	if r.W != w || r.H != 100 {
		t.Errorf("aspect crop = %+v, want w=%d h=100", r, w)
	}
	// Объект остаётся в центре окна (историческая семантика «от угла»
	// смещала объект к левому/верхнему краю при расширении fitAspect).
	if cx, cy := r.X+r.W/2, r.Y+r.H/2; cx != 150 || cy != 150 {
		t.Errorf("aspect crop center = (%d,%d), want (150,150)", cx, cy)
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

func TestSelectFaceCropCentered(t *testing.T) {
	// Лицо точно по центру кадра → окно центрируется на центре лица.
	// Кадр 300x200, лицо {125,75,50,50} (центр 150,100), target 100x50.
	// Область лица 50x50 расширяется под аспект 2:1 → окно 100x50
	// (лицо+margin заполняет кадр после ресайза).
	boxes := []Box{{X: 125, Y: 75, W: 50, H: 50, Confidence: 0.9}}
	r := SelectFaceCrop(boxes, 300, 200, 100, 50, 0)
	// Окно 100x50 в точке (150,100): x=100, y=75.
	want := Rect{X: 100, Y: 75, W: 100, H: 50}
	if !rectEq(r, want) {
		t.Errorf("centered face crop = %+v, want %+v", r, want)
	}

	// Лицо шире целевого аспекта: окно масштабируется под лицо —
	// область лица 200x100 уже имеет аспект 2:1 → окно = область лица.
	big := []Box{{X: 50, Y: 50, W: 200, H: 100, Confidence: 0.9}} // центр 150,100
	r = SelectFaceCrop(big, 300, 200, 100, 50, 0)
	if !rectEq(r, Rect{X: 50, Y: 50, W: 200, H: 100}) {
		t.Errorf("big face crop = %+v, want {50 50 200 100}", r)
	}
}

func TestSelectFaceCropScalesWindowToFace(t *testing.T) {
	// Маленькое лицо в большом кадре (сценарий бага "виден только нос"):
	// оригинал 3000x2000, ассет 200x200, лицо 200x200 исходных пикселей.
	// Окно НЕ может быть 200x200 в исходных пикселях — оно должно
	// масштабироваться под область лица, чтобы после ресайза лицо
	// занимало разумную долю кадра.
	boxes := []Box{{X: 1400, Y: 900, W: 200, H: 200, Confidence: 0.9}} // центр (1500,1000)
	r := SelectFaceCrop(boxes, 3000, 2000, 200, 200, 0.2)
	// margin 0.2: по 20px с каждой стороны → область {1380,880,240,240};
	// аспект квадратный → окно = область 240x240, центрировано на лице.
	if !rectEq(r, Rect{X: 1380, Y: 880, W: 240, H: 240}) {
		t.Errorf("scaled face window = %+v, want {1380 880 240 240}", r)
	}
	if r.W <= 200 || r.H <= 200 {
		t.Errorf("window %dx%d must exceed asset size 200x200 in source px", r.W, r.H)
	}
	// Центр области лица остаётся в центре окна.
	if cx, cy := r.X+r.W/2, r.Y+r.H/2; cx != 1500 || cy != 1000 {
		t.Errorf("window center = (%d,%d), want (1500,1000)", cx, cy)
	}

	// Неквадратный ассет 200x100 (аспект 2:1): область 240x240 расширяется
	// по ширине до 480x240.
	r = SelectFaceCrop(boxes, 3000, 2000, 200, 100, 0.2)
	// x = 1500-240 = 1260, y = 1000-120 = 880.
	if !rectEq(r, Rect{X: 1260, Y: 880, W: 480, H: 240}) {
		t.Errorf("aspect-scaled face window = %+v, want {1260 880 480 240}", r)
	}
}

func TestSelectFaceCropClampLeftEdge(t *testing.T) {
	// Лицо у левого края: окно 100x50 упирается в левую границу (x=0),
	// лицо остаётся смещённым влево, но кадр не выходит за границы.
	boxes := []Box{{X: 0, Y: 75, W: 50, H: 50, Confidence: 0.9}} // центр (25,100)
	r := SelectFaceCrop(boxes, 300, 200, 100, 50, 0)
	// Без clamp было бы x=-25 → clamp до 0.
	want := Rect{X: 0, Y: 75, W: 100, H: 50}
	if !rectEq(r, want) {
		t.Errorf("left edge face crop = %+v, want %+v", r, want)
	}
}

func TestSelectFaceCropClampRightEdge(t *testing.T) {
	// Лицо у правого края: окно упирается в правую границу.
	boxes := []Box{{X: 250, Y: 75, W: 50, H: 50, Confidence: 0.9}} // центр (275,100)
	r := SelectFaceCrop(boxes, 300, 200, 100, 50, 0)
	// Без clamp было бы x=225, x+100=325 > 300 → x=200.
	want := Rect{X: 200, Y: 75, W: 100, H: 50}
	if !rectEq(r, want) {
		t.Errorf("right edge face crop = %+v, want %+v", r, want)
	}
}

func TestSelectFaceCropClampTopBottomEdges(t *testing.T) {
	// Вертикальное поведение: лицо у верхнего края — окно прижимается к y=0;
	// у нижнего — к нижней границе. Вертикаль тоже центрируется + clamp.
	top := []Box{{X: 125, Y: 0, W: 50, H: 50, Confidence: 0.9}} // центр (150,25)
	r := SelectFaceCrop(top, 300, 200, 100, 50, 0)
	// Без clamp было бы y=0 (25-25=0) — уже на границе.
	if !rectEq(r, Rect{X: 100, Y: 0, W: 100, H: 50}) {
		t.Errorf("top edge face crop = %+v", r)
	}

	bottom := []Box{{X: 125, Y: 150, W: 50, H: 50, Confidence: 0.9}} // центр (150,175)
	r = SelectFaceCrop(bottom, 300, 200, 100, 50, 0)
	// Без clamp было бы y=150, y+50=200 → в границе.
	if !rectEq(r, Rect{X: 100, Y: 150, W: 100, H: 50}) {
		t.Errorf("bottom edge face crop = %+v", r)
	}
}

func TestSelectFaceCropWindowLargerThanImage(t *testing.T) {
	// Желаемое окно шире кадра (большая face-area, расширение fitAspect
	// выводит ширину за границу): ширина ужинается до кадра, остальное
	// центрируется. Кадр 300x200, область лица {50,0,200,200} (квадрат),
	// target 500x300 (аспект 5:3 ≈ 1.667): окно = round(200*1.667) = 333x200
	// → ширина clamp до 300; центр (150,100) → {0,0,300,200}.
	boxes := []Box{{X: 50, Y: 0, W: 200, H: 200, Confidence: 0.9}}
	r := SelectFaceCrop(boxes, 300, 200, 500, 300, 0)
	if !rectEq(r, Rect{X: 0, Y: 0, W: 300, H: 200}) {
		t.Errorf("window wider than image = %+v, want full frame", r)
	}

	// Окно больше кадра только по ширине, по высоте — меньше: ширина
	// ужинается, высота центрируется на лице. Область {50,10,200,180}
	// (центр (150,100)), target 500x200 (аспект 2.5): окно =
	// round(180*2.5) = 450x180 → ширина clamp до 300, высота 180:
	// x=0, y=100-90=10.
	boxes = []Box{{X: 50, Y: 10, W: 200, H: 180, Confidence: 0.9}}
	r = SelectFaceCrop(boxes, 300, 200, 500, 200, 0)
	if !rectEq(r, Rect{X: 0, Y: 10, W: 300, H: 180}) {
		t.Errorf("window wider only = %+v, want {0 10 300 180}", r)
	}

	// Желаемое окно выше кадра: высота ужинается, ширина центрируется.
	// Область {0,0,300,200} (весь кадр, аспект 1.5), target 200x200
	// (аспект 1): окно 300x300 → высота clamp до 200, ширина 300:
	// x=0, y=0.
	boxes = []Box{{X: 0, Y: 0, W: 300, H: 200, Confidence: 0.9}}
	r = SelectFaceCrop(boxes, 300, 200, 200, 200, 0)
	if !rectEq(r, Rect{X: 0, Y: 0, W: 300, H: 200}) {
		t.Errorf("window taller than image = %+v, want full frame", r)
	}
}

func TestSelectFaceCropFallbacks(t *testing.T) {
	// Пустой список боксов + целевой размер задан → центральный кроп.
	r := SelectFaceCrop(nil, 300, 200, 100, 50, 0)
	if !rectEq(r, Rect{X: 100, Y: 75, W: 100, H: 50}) {
		t.Errorf("fallback center = %+v, want {100 75 100 50}", r)
	}

	// Пустой список + целевой размер не задан → весь кадр.
	r = SelectFaceCrop(nil, 300, 200, 0, 0, 0)
	if !rectEq(r, Rect{X: 0, Y: 0, W: 300, H: 200}) {
		t.Errorf("fallback full = %+v, want full frame", r)
	}

	// Целевой размер не задан, но есть лицо → окно = область лица (+margin).
	boxes := []Box{{X: 100, Y: 100, W: 50, H: 50, Confidence: 0.9}}
	r = SelectFaceCrop(boxes, 300, 200, 0, 0, 0.2)
	// margin 0.2: по 5px с каждой стороны → {95,95,60,60}.
	if !rectEq(r, Rect{X: 95, Y: 95, W: 60, H: 60}) {
		t.Errorf("face area as window = %+v, want {95 95 60 60}", r)
	}

	// Авто-сторона окна по аспекту кадра: задана только высота 50 →
	// ширина = 50 * (300/200) = 75.
	boxes = []Box{{X: 125, Y: 75, W: 50, H: 50, Confidence: 0.9}} // центр (150,100)
	r = SelectFaceCrop(boxes, 300, 200, 0, 50, 0)
	if !rectEq(r, Rect{X: 113, Y: 75, W: 75, H: 50}) {
		t.Errorf("auto width window = %+v, want {113 75 75 50}", r)
	}
}

func TestSelectFaceCropInvalidImg(t *testing.T) {
	// Защита: некорректный кадр → пустой Rect.
	if r := SelectFaceCrop(nil, 0, 100, 100, 100, 0.1); r != (Rect{}) {
		t.Errorf("invalid img = %+v, want zero Rect", r)
	}
}

// TestSelectFaceCropMarginSymmetry проверяет, что margin применяется
// СИММЕТРИЧНО: центр лица остаётся точно в центре окна при отсутствии
// clamp (лицо в середине кадра). Версия «margin расширяет только
// вправо/вниз» смещала бы центр окна от центра лица.
func TestSelectFaceCropMarginSymmetry(t *testing.T) {
	imgW, imgH := 1000, 800
	// Лицо в центре кадра, нечётные размеры bbox (101x77) — ловит
	// асимметрию округления паддинга.
	boxes := []Box{{X: 449, Y: 361, W: 101, H: 77, Confidence: 0.9}} // центр (499,399) ≈ центр кадра
	for _, margin := range []float64{0, 0.05, 0.1, 0.2, 0.5, 1.0} {
		r := SelectFaceCrop(boxes, imgW, imgH, 200, 200, margin)
		if r.X < 0 || r.Y < 0 || r.X+r.W > imgW || r.Y+r.H > imgH {
			t.Fatalf("margin %v: rect %+v outside frame", margin, r)
		}
		// Центр лица должен совпадать с центром окна.
		fcx, fcy := 449+101/2, 361+77/2
		if cx, cy := r.X+r.W/2, r.Y+r.H/2; cx != fcx || cy != fcy {
			t.Errorf("margin %v: window center (%d,%d), want face center (%d,%d)", margin, cx, cy, fcx, fcy)
		}
	}
}

// TestCenteredClampedRectEvenOddSymmetry проверяет целочисленную симметрию
// центрирования: для чётного И нечётного размера окна объект в точке (cx,cy)
// остаётся в центре окна (полуинтервальная семантика: окно [cx-w/2, cx+w/2)).
// Формула x=cx-(w-1)/2 давала бы смещение центра на +0.5px (влево визуально)
// при чётном окне.
func TestCenteredClampedRectEvenOddSymmetry(t *testing.T) {
	cx, cy := 500, 400
	for _, w := range []int{10, 11, 99, 100, 101, 199, 200, 201} {
		for _, h := range []int{10, 11, 99, 100, 101} {
			r := centeredClampedRect(cx, cy, w, h, 1000, 800)
			// Левые и правые поля до объекта симметричны (разница <= 1px,
			// inevitable для нечётного окна относительно целочисленной
			// полуинтервальной модели: [cx-k, cx+k+1) — ровно k слева).
			left := cx - r.X
			right := r.X + r.W - cx
			if left != w/2 || right != w-w/2 {
				t.Errorf("w=%d: left=%d right=%d, want %d/%d", w, left, right, w/2, w-w/2)
			}
			top := cy - r.Y
			bottom := r.Y + r.H - cy
			if top != h/2 || bottom != h-h/2 {
				t.Errorf("h=%d: top=%d bottom=%d, want %d/%d", h, top, bottom, h/2, h-h/2)
			}
		}
	}
}

// TestSelectCropCenteredOnObject проверяет новую центрированную семантику
// object-crop (общее ядро с face-crop): центр окна на центре объекта.
func TestSelectCropCenteredOnObject(t *testing.T) {
	// Объект в центре кадра 1000x800, bbox 100x100 (центр 500,400),
	// целевой аспект 2:1 → окно 200x100, центр объекта в центре окна.
	boxes := []Box{{X: 450, Y: 350, W: 100, H: 100, Confidence: 0.9}}
	r := SelectCrop(boxes, 1000, 800, 200, 100, 0)
	if !rectEq(r, Rect{X: 400, Y: 350, W: 200, H: 100}) {
		t.Fatalf("centered object crop = %+v, want {400 350 200 100}", r)
	}
	if cx, cy := r.X+r.W/2, r.Y+r.H/2; cx != 500 || cy != 400 {
		t.Errorf("object crop center = (%d,%d), want (500,400)", cx, cy)
	}

	// С margin: объект (центр 500,400) остаётся в центре окна.
	r = SelectCrop(boxes, 1000, 800, 200, 100, 0.2)
	// margin 0.2 → по 10px с каждой стороны: область {440,340,120,120};
	// аспект 2:1 → окно 240x120, центр (500,400) → {380,340,240,120}.
	if !rectEq(r, Rect{X: 380, Y: 340, W: 240, H: 120}) {
		t.Errorf("margin object crop = %+v, want {380 340 240 120}", r)
	}
	if cx, cy := r.X+r.W/2, r.Y+r.H/2; cx != 500 || cy != 400 {
		t.Errorf("margin object crop center = (%d,%d), want (500,400)", cx, cy)
	}
}

// TestSelectCropClampEdges проверяет clamp окна к краям кадра для
// object-crop: окно упирается в край, объект остаётся внутри.
func TestSelectCropClampEdges(t *testing.T) {
	imgW, imgH := 300, 200
	// Объект у левого края: окно 100x50 не может центрироваться на (25,100)
	// — clamp к x=0.
	boxes := []Box{{X: 0, Y: 75, W: 50, H: 50, Confidence: 0.9}}
	r := SelectCrop(boxes, imgW, imgH, 100, 50, 0)
	if !rectEq(r, Rect{X: 0, Y: 75, W: 100, H: 50}) {
		t.Errorf("left edge crop = %+v, want {0 75 100 50}", r)
	}

	// Объект у правого края: clamp к x=imgW-w.
	boxes = []Box{{X: 250, Y: 75, W: 50, H: 50, Confidence: 0.9}}
	r = SelectCrop(boxes, imgW, imgH, 100, 50, 0)
	if !rectEq(r, Rect{X: 200, Y: 75, W: 100, H: 50}) {
		t.Errorf("right edge crop = %+v, want {200 75 100 50}", r)
	}

	// Объект у верхнего края: clamp к y=0.
	boxes = []Box{{X: 125, Y: 0, W: 50, H: 50, Confidence: 0.9}}
	r = SelectCrop(boxes, imgW, imgH, 100, 50, 0)
	if !rectEq(r, Rect{X: 100, Y: 0, W: 100, H: 50}) {
		t.Errorf("top edge crop = %+v, want {100 0 100 50}", r)
	}

	// Окно больше кадра по ширине: ужинается до кадра.
	boxes = []Box{{X: 100, Y: 80, W: 100, H: 40, Confidence: 0.9}}
	r = SelectCrop(boxes, imgW, imgH, 500, 60, 0)
	if r.W != imgW || r.X != 0 {
		t.Errorf("wide window crop = %+v, want width clamped to %d", r, imgW)
	}
}

// TestBoxFromEdgesSymmetry проверяет симметричное округление краёв бокса
// детектора: центр вещественного бокса сохраняется (round, не truncate).
// Прежние int(x1)/int(x2-x1) смещали центр систематически ВЛЕВО до ~1px.
func TestBoxFromEdgesSymmetry(t *testing.T) {
	cases := []struct {
		x1, y1, x2, y2 float64
		want           Box
	}{
		// 100.7..200.7: trunc давал X=100,W=100 (центр 150); round даёт
		// X=101,W=100 (центр 151 = вещественный центр 150.7 → 151).
		{100.7, 50.2, 200.7, 120.9, Box{X: 101, Y: 50, W: 100, H: 71}},
		// Точные границы — без изменений.
		{10, 20, 30, 40, Box{X: 10, Y: 20, W: 20, H: 20}},
		// Вещественный центр 99.5: округление до 100 (banker-free: .5 → up).
		{49.5, 0, 149.5, 10, Box{X: 50, Y: 0, W: 100, H: 10}},
	}
	for i, c := range cases {
		got, ok := boxFromEdges(c.x1, c.y1, c.x2, c.y2)
		if !ok {
			t.Fatalf("case %d: boxFromEdges(%v) = !ok", i, c)
		}
		if got.X != c.want.X || got.Y != c.want.Y || got.W != c.want.W || got.H != c.want.H {
			t.Errorf("case %d: boxFromEdges = %+v, want %+v", i, got, c.want)
		}
		// Инвариант: вещественный центр бокса = центр вещественных краёв
		// (±0.5 из-за целочисленной модели полуинтервалов).
		fcx := (c.x1 + c.x2) / 2
		fcy := (c.y1 + c.y2) / 2
		if d := math.Abs(float64(got.X) + float64(got.W)/2 - fcx); d > 0.5 {
			t.Errorf("case %d: center X drift = %v (> 0.5)", i, d)
		}
		if d := math.Abs(float64(got.Y) + float64(got.H)/2 - fcy); d > 0.5 {
			t.Errorf("case %d: center Y drift = %v (> 0.5)", i, d)
		}
	}
	// Вырожденный бокс → !ok.
	if _, ok := boxFromEdges(10, 10, 10.4, 20); ok {
		t.Error("degenerate box should return ok=false")
	}
}

// ===== fix-режимы (face-fix / object-fix): cover-масштаб без зума =====

// TestSelectFaceFixCropWiderThanTarget проверяет: оригинал пропорционально
// ШИРЕ цели → кроп только по X, ПОЛНАЯ высота сохраняется; окно центрируется
// на центре face-area.
func TestSelectFaceFixCropWiderThanTarget(t *testing.T) {
	// Кадр 400x200 (aspect 2.0), цель 100x100 (aspect 1.0): оригинал шире.
	// scale = 100/200 = 0.5 → окно = 200x200 (полная высота).
	// Лицо в центре (центр 200,100) → x = 200-100 = 100.
	boxes := []Box{{X: 175, Y: 75, W: 50, H: 50, Confidence: 0.9}} // центр (200,100)
	r := SelectFaceFixCrop(boxes, 400, 200, 100, 100, 0)
	if !rectEq(r, Rect{X: 100, Y: 0, W: 200, H: 200}) {
		t.Errorf("wider-than-target = %+v, want {100 0 200 200} (full height)", r)
	}
}

// TestSelectFaceFixCropTallerThanTarget проверяет: оригинал пропорционально
// ВЫШЕ цели → кроп только по Y, ПОЛНАЯ ширина сохраняется.
func TestSelectFaceFixCropTallerThanTarget(t *testing.T) {
	// Кадр 200x400 (aspect 0.5), цель 100x100 (aspect 1.0): оригинал выше.
	// scale = 100/200 = 0.5 → окно = 200x200 (полная ширина).
	// Лицо в центре (центр 100,200) → y = 200-100 = 100.
	boxes := []Box{{X: 75, Y: 175, W: 50, H: 50, Confidence: 0.9}} // центр (100,200)
	r := SelectFaceFixCrop(boxes, 200, 400, 100, 100, 0)
	if !rectEq(r, Rect{X: 0, Y: 100, W: 200, H: 200}) {
		t.Errorf("taller-than-target = %+v, want {0 100 200 200} (full width)", r)
	}
}

// TestSelectFaceFixCropClampLeftEdge: лицо у левого края — окно упирается
// в левую границу (x=0), лицо остаётся смещённым, но не обрезается.
func TestSelectFaceFixCropClampLeftEdge(t *testing.T) {
	// Кадр 400x200, цель 100x100 → окно 200x200 по X.
	// Лицо у левого края (центр X=25): без clamp x=-75 → clamp до 0.
	boxes := []Box{{X: 0, Y: 75, W: 50, H: 50, Confidence: 0.9}} // центр (25,100)
	r := SelectFaceFixCrop(boxes, 400, 200, 100, 100, 0)
	if !rectEq(r, Rect{X: 0, Y: 0, W: 200, H: 200}) {
		t.Errorf("left edge = %+v, want {0 0 200 200}", r)
	}
}

// TestSelectFaceFixCropClampRightEdge: лицо у правого края — окно упирается
// в правую границу (x=imgW-w).
func TestSelectFaceFixCropClampRightEdge(t *testing.T) {
	// Лицо у правого края (центр X=375): без clamp x=275, x+200=475 > 400
	// → clamp до x=200.
	boxes := []Box{{X: 350, Y: 75, W: 50, H: 50, Confidence: 0.9}} // центр (375,100)
	r := SelectFaceFixCrop(boxes, 400, 200, 100, 100, 0)
	if !rectEq(r, Rect{X: 200, Y: 0, W: 200, H: 200}) {
		t.Errorf("right edge = %+v, want {200 0 200 200}", r)
	}
}

// TestSelectFaceFixCropEqualAspect: аспект оригинала равен аспекту цели →
// окно = весь кадр (независимо от позиции лица).
func TestSelectFaceFixCropEqualAspect(t *testing.T) {
	// Кадр 400x200 (aspect 2), цель 200x100 (aspect 2): окно = весь кадр.
	boxes := []Box{{X: 0, Y: 0, W: 50, H: 50, Confidence: 0.9}}
	r := SelectFaceFixCrop(boxes, 400, 200, 200, 100, 0)
	if !rectEq(r, Rect{X: 0, Y: 0, W: 400, H: 200}) {
		t.Errorf("equal aspect = %+v, want full frame {0 0 400 200}", r)
	}
}

// TestSelectFaceFixCropNoDetectionFallback: нет детекции → fallback в центр
// по избыточной оси (эквивалент центрального кропа).
func TestSelectFaceFixCropNoDetectionFallback(t *testing.T) {
	// Кадр 400x200, цель 100x100 → окно 200x200 по центру: x = 100.
	r := SelectFaceFixCrop(nil, 400, 200, 100, 100, 0)
	if !rectEq(r, Rect{X: 100, Y: 0, W: 200, H: 200}) {
		t.Errorf("no detection fallback = %+v, want {100 0 200 200}", r)
	}

	// Вертикальный случай: кадр 200x400, цель 100x100 → окно 200x200
	// по центру: y = 100.
	r = SelectFaceFixCrop(nil, 200, 400, 100, 100, 0)
	if !rectEq(r, Rect{X: 0, Y: 100, W: 200, H: 200}) {
		t.Errorf("no detection fallback (tall) = %+v, want {0 100 200 200}", r)
	}
}

// TestSelectFaceFixCropMarginShiftsCenter: margin смещает центр окна
// (центр face-area с отступом), clamp удерживает окно в кадре.
func TestSelectFaceFixCropMarginShiftsCenter(t *testing.T) {
	// Кадр 400x200, цель 100x100 → окно 200x200. Лицо 50x50 в (175,75),
	// margin 0.2: pad = round(50*0.2/2) = 5 → область {170,70,60,60},
	// центр (200,100) → x = 100.
	boxes := []Box{{X: 175, Y: 75, W: 50, H: 50, Confidence: 0.9}}
	r := SelectFaceFixCrop(boxes, 400, 200, 100, 100, 0.2)
	if !rectEq(r, Rect{X: 100, Y: 0, W: 200, H: 200}) {
		t.Errorf("margin center = %+v, want {100 0 200 200}", r)
	}
}

// TestSelectFaceFixCropInvalidImg: защита от некорректного кадра.
func TestSelectFaceFixCropInvalidImg(t *testing.T) {
	if r := SelectFaceFixCrop(nil, 0, 100, 100, 100, 0); r != (Rect{}) {
		t.Errorf("invalid img = %+v, want zero Rect", r)
	}
}

// TestSelectObjectFixCropWiderThanTarget: object-fix аналогичен face-fix
// (общее ядро): кроп только по X, полная высота.
func TestSelectObjectFixCropWiderThanTarget(t *testing.T) {
	// Кадр 400x200, цель 100x100 → окно 200x200. Объект в центре.
	boxes := []Box{{X: 175, Y: 75, W: 50, H: 50, Confidence: 0.9}}
	r := SelectObjectFixCrop(boxes, 400, 200, 100, 100, 0)
	if !rectEq(r, Rect{X: 100, Y: 0, W: 200, H: 200}) {
		t.Errorf("object fix wider = %+v, want {100 0 200 200}", r)
	}
}

// TestSelectObjectFixCropTallerThanTarget: object-fix, кроп только по Y,
// полная ширина; объект у нижнего края → clamp к y=imgH-h.
func TestSelectObjectFixCropTallerThanTarget(t *testing.T) {
	// Кадр 200x400, цель 100x100 → окно 200x200 по Y. Объект у нижнего
	// края (центр Y=375): без clamp y=275, y+200=475 > 400 → y=200.
	boxes := []Box{{X: 75, Y: 350, W: 50, H: 50, Confidence: 0.9}} // центр (100,375)
	r := SelectObjectFixCrop(boxes, 200, 400, 100, 100, 0)
	if !rectEq(r, Rect{X: 0, Y: 200, W: 200, H: 200}) {
		t.Errorf("object fix taller clamp = %+v, want {0 200 200 200}", r)
	}
}

// TestSelectObjectFixCropNoDetectionFallback: object-fix без детекции →
// центрирование по избыточной оси.
func TestSelectObjectFixCropNoDetectionFallback(t *testing.T) {
	r := SelectObjectFixCrop(nil, 400, 200, 100, 100, 0)
	if !rectEq(r, Rect{X: 100, Y: 0, W: 200, H: 200}) {
		t.Errorf("object fix fallback = %+v, want {100 0 200 200}", r)
	}
}
