//go:build onnx && cgo

// Реальный ONNX Runtime инференс (github.com/yalue/onnxruntime_go, dlopen).
//
// Модели (эмпирически проверены в Docker на официальных файлах):
//
//	Лица — YuNet face_detection_yunet_2023mar.onnx
//	  вход:  "input"  [1,3,640,640] float32 (ФИКСИРОВАННЫЙ размер 640×640)
//	  выходы: cls_{8,16,32} [1,N,1], obj_{8,16,32} [1,N,1],
//	          bbox_{8,16,32} [1,N,4], N = 6400/1600/400;
//	  препроцессинг: letterbox в 640×640 с паддингом 127.5, СЫРЫЕ пиксели
//	    0..255 БЕЗ нормализации (нормализация (v-127.5)/128 + mean=127.5
//	    ОБНУЛЯЕТ obj-выходы — модель перестаёт что-либо детектировать);
//	  score = cls*obj (RAW, без sigmoid: sigmoid(cls)~0.7 почти на всём
//	    входе и "съедает" дискриминацию obj);
//	  декодирование (как в OpenCV face_detect.cpp):
//	    cx = (gridX+0.5+b0)*stride; cy = (gridY+0.5+b1)*stride
//	    w  = exp(b2)*stride;        h  = exp(b3)*stride
//
//	Объекты — SSD MobileNet v1 (tf2onnx) ssd_mobilenet_v1_12.onnx
//	  вход:  "inputs" [-1,-1,-1,3] uint8 NHWC
//	  выходы: detection_boxes [-1,-1,4], detection_classes [-1,-1],
//	          detection_scores [-1,-1], num_detections [-1];
//	  препроцессинг: stretch-resize 300×300 (биллинейно), uint8 RGB NHWC;
//	  декодирование: normalized [ymin,xmin,ymax,xmax] -> пиксели;
//	  классы COCO 1..90; label = "COCO_<id>".
package detection

import (
	"context"
	"fmt"
	"math"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ortenvOnce — однократная инициализация глобального ORT-окружения.
// Биндинг грузит libonnxruntime через dlopen (Linux/macOS) или LoadLibrary
// (Windows) при первом обращении. По умолчанию он ищет "onnxruntime.so" на
// Linux/macOS и "onnxruntime.dll" на Windows, но пакеты (Alpine edge,
// Homebrew) ставят версионированные файлы без голого симлинка, поэтому
// путь к библиотеке подбираем сами — кроссплатформенно (ort_library.go).
var (
	ortenvOnce sync.Once
	ortenvErr  error
)

// initORT инициализирует окружение ONNX Runtime (лениво, один раз):
// подбирает путь к библиотеке и вызывает InitializeEnvironment.
//
// Путь к библиотеке берётся из конфига (detection.onnx-runtime-lib,
// передаётся через Options.OnnxRuntimeLib), а НЕ из env-переменных
// ONNXRUNTIME_SHARED_LIBRARY_PATH / ORT_DYLIB_PATH (соглашение проекта:
// все настройки — через конфиг-файл). Если путь из конфига пуст —
// выполняется автодетекция по платформе (ort_library.go): Linux (.so),
// Windows (.dll), macOS (.dylib). Если автодетект не нашёл библиотеку,
// путь остаётся пустым и биндинг пробует свой дефолт
// ("onnxruntime.so" / "onnxruntime.dll" в системных путях).
func initORT(libPath string) error {
	ortenvOnce.Do(func() {
		path := ortLibPathForInit(libPath)
		if path != "" {
			ort.SetSharedLibraryPath(path)
		}
		ortenvErr = ort.InitializeEnvironment()
	})
	return ortenvErr
}

// ensureInit — обёртка для ленивой инициализации при загрузке модели.
// libPath — путь к libonnxruntime из конфига (пусто = автодетекция).
func ensureInit(libPath string) error {
	if err := initORT(libPath); err != nil {
		return fmt.Errorf("detection: init onnxruntime: %w", err)
	}
	return nil
}

// buildModel создаёт реальный инференс-бэкенд по пути модели.
// Тип бэкенда (YuNet/SSD) выбирается по виду детектора (face/object).
func (d *OnnxDetector) buildModel(path, kind string) (modelBackend, error) {
	if err := modelExists(path, kind); err != nil {
		return nil, err
	}
	if err := ensureInit(d.opts.OnnxRuntimeLib); err != nil {
		return nil, err
	}
	if kind == "face" {
		return newYuNetBackend(path)
	}
	return newSSDBackend(path)
}

// ─────────────────────────── YuNet ───────────────────────────

const (
	yunetInSize = 640 // фиксированный размер входа модели (растёт с разрешением)
)

// yunetBackend — YuNet face detector поверх ort-сессии.
type yunetBackend struct {
	sess  *ort.DynamicAdvancedSession
	mu    sync.Mutex // ort-сессии не потокобезопасны
	inW   int        // ширина канвы (letterbox)
	padX  float64
	padY  float64
	scale float64 // масштаб letterbox (original -> канва)
	winW  int     // ширина исходного кадра при создании бэкенда
	winH  int
}

// newYuNetBackend загружает YuNet и вычисляет параметры letterbox для
// исходного кадра (используются при run; сами кадры могут быть другого
// размера, но параметры пересчитываются в run под фактический кадр).
func newYuNetBackend(path string) (*yunetBackend, error) {
	names := []string{"cls_8", "cls_16", "cls_32", "obj_8", "obj_16", "obj_32", "bbox_8", "bbox_16", "bbox_32"}
	sess, err := ort.NewDynamicAdvancedSession(path, []string{"input"}, names, nil)
	if err != nil {
		return nil, fmt.Errorf("detection: load yunet model %s: %w", path, err)
	}
	return &yunetBackend{sess: sess}, nil
}

// run выполняет YuNet-инференс. rgb — RGB (Go) пиксели исходного кадра.
// Результат — боксы в координатах исходного кадра (до NMS), score = cls*obj.
func (b *yunetBackend) run(_ context.Context, rgb []byte, width, height int) ([]Box, error) {
	if width <= 0 || height <= 0 || len(rgb) < width*height*3 {
		return nil, fmt.Errorf("detection: yunet: bad frame %dx%d len=%d", width, height, len(rgb))
	}
	// Letterbox: масштаб и паддинги под размеры ФАКТИЧЕСКОГО кадра.
	scale := math.Min(float64(yunetInSize)/float64(width), float64(yunetInSize)/float64(height))
	newW := int(math.Round(float64(width) * scale))
	newH := int(math.Round(float64(height) * scale))
	padX := (float64(yunetInSize) - float64(newW)) / 2
	padY := (float64(yunetInSize) - float64(newH)) / 2

	blob := make([]float32, 3*yunetInSize*yunetInSize)
	fillValue := float32(127.5)
	for i := range blob {
		blob[i] = fillValue // паддинг = 127.5 (нейтральный для raw-пикселей)
	}
	for y := 0; y < newH; y++ {
		fy := (float64(y) + 0.5) / float64(newH)
		for x := 0; x < newW; x++ {
			fx := (float64(x) + 0.5) / float64(newW)
			r, g, bl := bilinearSample(rgb, width, height, fx, fy)
			ox := int(padX) + x
			oy := int(padY) + y
			o := oy*yunetInSize + ox
			// NCHW, плоскость 0 = R (Go-кадр уже RGB).
			blob[o] = r
			blob[yunetInSize*yunetInSize+o] = g
			blob[2*yunetInSize*yunetInSize+o] = bl
		}
	}

	input, err := ort.NewTensor(ort.NewShape(1, 3, yunetInSize, yunetInSize), blob)
	if err != nil {
		return nil, fmt.Errorf("detection: yunet tensor: %w", err)
	}
	defer input.Destroy()

	b.mu.Lock()
	defer b.mu.Unlock()
	outputs := make([]ort.Value, 9)
	if err := b.sess.Run([]ort.Value{input}, outputs); err != nil {
		for _, v := range outputs {
			if v != nil {
				v.Destroy()
			}
		}
		return nil, fmt.Errorf("detection: yunet run: %w", err)
	}
	defer func() {
		for _, v := range outputs {
			if v != nil {
				v.Destroy()
			}
		}
	}()

	type group struct {
		cls, obj, bbox *ort.Tensor[float32]
		px             int
	}
	byStride := map[int]*group{}
	for _, si := range []int{0, 1, 2} { // cls_8/16/32
		t, ok := outputs[si].(*ort.Tensor[float32])
		if !ok {
			return nil, fmt.Errorf("detection: yunet: cls_%d not float32", 8<<si)
		}
		stride := 8 << si
		g := byStride[stride]
		if g == nil {
			g = &group{}
			byStride[stride] = g
		}
		g.cls = t
		g.px = int(t.GetShape()[1])
	}
	for _, si := range []int{3, 4, 5} { // obj_8/16/32
		t, ok := outputs[si].(*ort.Tensor[float32])
		if !ok {
			return nil, fmt.Errorf("detection: yunet: obj_%d not float32", 8<<(si-3))
		}
		stride := 8 << (si - 3)
		byStride[stride].obj = t
	}
	for _, si := range []int{6, 7, 8} { // bbox_8/16/32
		t, ok := outputs[si].(*ort.Tensor[float32])
		if !ok {
			return nil, fmt.Errorf("detection: yunet: bbox_%d not float32", 8<<(si-6))
		}
		stride := 8 << (si - 6)
		byStride[stride].bbox = t
	}

	out := make([]Box, 0, 64)
	for stride := 8; stride <= 32; stride *= 2 {
		g := byStride[stride]
		if g == nil {
			continue
		}
		cls := g.cls.GetData()
		obj := g.obj.GetData()
		box := g.bbox.GetData()
		gridW := yunetInSize / stride
		sf := float32(stride)
		for a := 0; a < g.px; a++ {
			score := float64(cls[a]) * float64(obj[a])
			if score < 0.3 {
				continue
			}
			bx := box[a*4 : a*4+4]
			gx := a % gridW
			gy := a / gridW
			cx := (float32(gx) + 0.5 + bx[0]) * sf
			cy := (float32(gy) + 0.5 + bx[1]) * sf
			w := float32(math.Exp(float64(bx[2]))) * sf
			h := float32(math.Exp(float64(bx[3]))) * sf
			x1 := cx - w/2
			y1 := cy - h/2
			x2 := cx + w/2
			y2 := cy + h/2
			// Обрезаем канву (лица в паддинг не выходят); небольшой допуск.
			if x1 < -8 || y1 < -8 || x2 > yunetInSize+8 || y2 > yunetInSize+8 {
				continue
			}
			// В координаты исходного кадра.
			ox1 := (float64(x1) - padX) / scale
			oy1 := (float64(y1) - padY) / scale
			ox2 := (float64(x2) - padX) / scale
			oy2 := (float64(y2) - padY) / scale
			// Клампим к границам кадра (бокс у края может слегка выходить
			// из-за округления при маппинге из канва-координат).
			if ox1 < 0 {
				ox1 = 0
			}
			if oy1 < 0 {
				oy1 = 0
			}
			if ox2 > float64(width) {
				ox2 = float64(width)
			}
			if oy2 > float64(height) {
				oy2 = float64(height)
			}
			if ox2 <= ox1 || oy2 <= oy1 {
				continue
			}
			out = append(out, Box{
				X:          int(ox1),
				Y:          int(oy1),
				W:          int(ox2 - ox1),
				H:          int(oy2 - oy1),
				Confidence: score,
			})
		}
	}
	return out, nil
}

// ─────────────────────────── SSD ───────────────────────────

const (
	ssdInSize = 300
)

// ssdBackend — SSD MobileNet v1 (uint8 NHWC) поверх ort-сессии.
type ssdBackend struct {
	sess *ort.DynamicAdvancedSession
	mu   sync.Mutex
}

// newSSDBackend загружает SSD модель.
func newSSDBackend(path string) (*ssdBackend, error) {
	names := []string{"detection_boxes", "detection_classes", "detection_scores", "num_detections"}
	sess, err := ort.NewDynamicAdvancedSession(path, []string{"inputs"}, names, nil)
	if err != nil {
		return nil, fmt.Errorf("detection: load ssd model %s: %w", path, err)
	}
	return &ssdBackend{sess: sess}, nil
}

// run выполняет SSD-инференс: stretch-resize 300×300, uint8 RGB NHWC,
// декодирование normalized [ymin,xmin,ymax,xmax] -> пиксели, label COCO.
func (b *ssdBackend) run(_ context.Context, rgb []byte, width, height int) ([]Box, error) {
	if width <= 0 || height <= 0 || len(rgb) < width*height*3 {
		return nil, fmt.Errorf("detection: ssd: bad frame %dx%d len=%d", width, height, len(rgb))
	}
	blob := make([]uint8, 3*ssdInSize*ssdInSize)
	for y := 0; y < ssdInSize; y++ {
		fy := (float64(y) + 0.5) / float64(ssdInSize)
		for x := 0; x < ssdInSize; x++ {
			fx := (float64(x) + 0.5) / float64(ssdInSize)
			r, g, bl := bilinearSample(rgb, width, height, fx, fy)
			o := (y*ssdInSize + x) * 3
			blob[o] = uint8(r + 0.5)
			blob[o+1] = uint8(g + 0.5)
			blob[o+2] = uint8(bl + 0.5)
		}
	}
	input, err := ort.NewTensor(ort.NewShape(1, ssdInSize, ssdInSize, 3), blob)
	if err != nil {
		return nil, fmt.Errorf("detection: ssd tensor: %w", err)
	}
	defer input.Destroy()

	b.mu.Lock()
	defer b.mu.Unlock()
	outputs := make([]ort.Value, 4)
	if err := b.sess.Run([]ort.Value{input}, outputs); err != nil {
		for _, v := range outputs {
			if v != nil {
				v.Destroy()
			}
		}
		return nil, fmt.Errorf("detection: ssd run: %w", err)
	}
	defer func() {
		for _, v := range outputs {
			if v != nil {
				v.Destroy()
			}
		}
	}()

	tBox, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("detection: ssd: detection_boxes not float32")
	}
	tCls, ok := outputs[1].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("detection: ssd: detection_classes not float32")
	}
	tScore, ok := outputs[2].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("detection: ssd: detection_scores not float32")
	}
	tNum, ok := outputs[3].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("detection: ssd: num_detections not float32")
	}
	boxes := tBox.GetData()
	classes := tCls.GetData()
	scores := tScore.GetData()
	numData := tNum.GetData()
	n := 0
	if len(numData) > 0 {
		n = int(numData[0] + 0.5)
	}
	if n > len(scores) {
		n = len(scores)
	}
	out := make([]Box, 0, n)
	for i := 0; i < n; i++ {
		ymin, xmin, ymax, xmax := boxes[i*4], boxes[i*4+1], boxes[i*4+2], boxes[i*4+3]
		x1 := xmin * float32(width)
		y1 := ymin * float32(height)
		x2 := xmax * float32(width)
		y2 := ymax * float32(height)
		if x2 <= x1 || y2 <= y1 {
			continue
		}
		cls := int(classes[i])
		out = append(out, Box{
			X:          int(x1),
			Y:          int(y1),
			W:          int(x2 - x1),
			H:          int(y2 - y1),
			Confidence: float64(scores[i]),
			Label:      cocoLabel(cls),
		})
	}
	return out, nil
}

// cocoLabel возвращает имя класса COCO (SSD MobileNet) или "class-N".
func cocoLabel(id int) string {
	if id >= 1 && id <= len(cocoNames) {
		return "COCO_" + cocoNames[id-1]
	}
	return fmt.Sprintf("class-%d", id)
}

// cocoNames — подмножество классов COCO для SSD MobileNet v1 (id 1..90).
var cocoNames = []string{
	"person", "bicycle", "car", "motorcycle", "airplane", "bus", "train",
	"truck", "boat", "traffic_light", "fire_hydrant", "street_sign",
	"stop_sign", "parking_meter", "bench", "bird", "cat", "dog", "horse",
	"sheep", "cow", "elephant", "bear", "zebra", "giraffe", "hat",
	"backpack", "umbrella", "shoe", "eye_glasses", "handbag", "tie",
	"suitcase", "frisbee", "skis", "snowboard", "sports_ball", "kite",
	"baseball_bat", "baseball_glove", "skateboard", "surfboard",
	"tennis_racket", "bottle", "plate", "wine_glass", "cup", "fork",
	"knife", "spoon", "bowl", "banana", "apple", "sandwich", "orange",
	"broccoli", "carrot", "hot_dog", "pizza", "donut", "cake", "chair",
	"couch", "potted_plant", "bed", "mirror", "dining_table", "window",
	"desk", "toilet", "door", "tv", "laptop", "mouse", "remote",
	"keyboard", "cell_phone", "microwave", "oven", "toaster", "sink",
	"refrigerator", "blender", "book", "clock", "vase", "scissors",
	"teddy_bear", "hair_drier", "toothbrush",
}

// bilinearSample извлекает биллинейно интерполированный пиксель (floats 0..255)
// из RGB-массива по нормализованным координатам центра пикселя (fx,fy) в [0,1].
func bilinearSample(rgb []byte, w, h int, fx, fy float64) (float32, float32, float32) {
	px := fx*float64(w) - 0.5
	py := fy*float64(h) - 0.5
	x0 := int(math.Floor(px))
	y0 := int(math.Floor(py))
	dx := px - float64(x0)
	dy := py - float64(y0)
	clamp := func(v, max int) int {
		if v < 0 {
			return 0
		}
		if v > max {
			return max
		}
		return v
	}
	x1 := clamp(x0+1, w-1)
	y1 := clamp(y0+1, h-1)
	x0 = clamp(x0, w-1)
	y0 = clamp(y0, h-1)
	idx := func(x, y, c int) byte { return rgb[(y*w+x)*3+c] }
	lerp := func(a, b, t float64) float64 { return a + (b-a)*t }
	top := func(c int) float64 { return lerp(float64(idx(x0, y0, c)), float64(idx(x1, y0, c)), dx) }
	bot := func(c int) float64 { return lerp(float64(idx(x0, y1, c)), float64(idx(x1, y1, c)), dx) }
	return float32(lerp(top(0), bot(0), dy)), float32(lerp(top(1), bot(1), dy)), float32(lerp(top(2), bot(2), dy))
}
