//go:build libvips

// Интеграционные тесты trim-вариантов детекторных кропов (sct/fct/oct) через
// реальный govips-движок. Компилируются ТОЛЬКО с тэком "libvips" (требует
// libvips + cgo-окружение, см. docs/PRODUCTION.md).
//
// Проверяют семантику "сначала trim, затем crop". Trim — независимый булев
// фильтр (plan.Trim=true), а не отдельная операция: операция плана — только
// режим кропа (smart-crop/face-crop/object-crop), trim применяется первым.
//   - smart-crop + trim: trim убирает однотонные края, затем smart-crop
//     (attention) применяется к подрезанному изображению;
//   - face-crop/object-crop + trim: детекция выполняется на УЖЕ подрезанном
//     изображении — детектор получает размеры trim-области, а не исходного
//     холста (координаты боксов относятся к подрезанному изображению).
package libvips

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/pkg-ru/imager/adapters/processor/detection"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/processing"
)

// makeTrimPng генерирует PNG WxH: белый фон и сплошной красный прямоугольник
// [x0,x1)×[y0,y1) в центре. После find_trim (threshold 0.0) область трима
// должна точно совпасть с красным прямоугольником (без сглаживания).
func makeTrimPng(t *testing.T, W, H, x0, y0, x1, y1 int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	white := color.RGBA{255, 255, 255, 255}
	red := color.RGBA{255, 0, 0, 255}
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if x >= x0 && x < x1 && y >= y0 && y < y1 {
				img.SetRGBA(x, y, red)
			} else {
				img.SetRGBA(x, y, white)
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return out.Bytes()
}

// decodePngSize возвращает размеры декодированного PNG.
func decodePngSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	return img.Bounds().Dx(), img.Bounds().Dy()
}

// hasRedPixel проверяет, что в декодированном изображении есть заметный
// красный пиксель (контент не был обрезан полностью).
func hasRedPixel(t *testing.T, data []byte) bool {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r > 50000 && g < 30000 && bl < 30000 {
				return true
			}
		}
	}
	return false
}

// fakeDetector — тестовый детектор: всегда available, возвращает один бокс на
// всю переданную область и запоминает размеры кадра, переданного детектору.
type fakeDetector struct {
	lastW, lastH int
	calls        int
}

func (f *fakeDetector) Available() bool { return true }

func (f *fakeDetector) DetectFaces(_ context.Context, _ []byte, width, height int) ([]detection.Box, error) {
	f.calls++
	f.lastW, f.lastH = width, height
	return []detection.Box{{X: 0, Y: 0, W: width, H: height, Confidence: 1.0}}, nil
}

func (f *fakeDetector) DetectObjects(_ context.Context, _ []byte, width, height int) ([]detection.Box, error) {
	f.calls++
	f.lastW, f.lastH = width, height
	return []detection.Box{{X: 0, Y: 0, W: width, H: height, Confidence: 1.0}}, nil
}

// TestOpSmartCropTrim проверяет "сначала trim, потом smart-crop": трим убирает
// белые края 120x80 -> 60x40, затем attention-crop масштабирует до 100x50.
func TestOpSmartCropTrim(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpSmartCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 100, Height: 50}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Trim = true

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, false, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	w, h := decodePngSize(t, out)
	if w != 100 || h != 50 {
		t.Errorf("output size = %dx%d, want 100x50", w, h)
	}
	if !hasRedPixel(t, out) {
		t.Error("output lost the red content after trim+smart-crop")
	}
}

// TestOpFaceCropTrimUsesTrimmedDimensions проверяет, что детектор получает
// размеры кадра ПОСЛЕ трима (60x40), а не полного холста (120x80), т.е.
// координаты боксов относятся к подрезанному изображению.
func TestOpFaceCropTrimUsesTrimmedDimensions(t *testing.T) {
	det := &fakeDetector{}
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 60, Height: 40}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Trim = true

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}, Detector: det, DetectorMargin: 0})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, false, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	if det.lastW != 60 || det.lastH != 40 {
		t.Errorf("detector got frame %dx%d, want 60x40 (trimmed), not 120x80", det.lastW, det.lastH)
	}
	w, h := decodePngSize(t, out)
	if w != 60 || h != 40 {
		t.Errorf("output size = %dx%d, want 60x40", w, h)
	}
	if !hasRedPixel(t, out) {
		t.Error("output lost the red content after trim+face-crop")
	}
}

// TestOpObjectCropTrimUsesTrimmedDimensions аналогичен face-crop-тесту для
// object-crop: детектор получает trim-область.
func TestOpObjectCropTrimUsesTrimmedDimensions(t *testing.T) {
	det := &fakeDetector{}
	plan, err := processing.NewProcessingPlan(
		processing.OpObjectCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 60, Height: 40}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Trim = true

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}, Detector: det, DetectorMargin: 0})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, false, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	if det.lastW != 60 || det.lastH != 40 {
		t.Errorf("detector got frame %dx%d, want 60x40 (trimmed), not 120x80", det.lastW, det.lastH)
	}
	w, h := decodePngSize(t, out)
	if w != 60 || h != 40 {
		t.Errorf("output size = %dx%d, want 60x40", w, h)
	}
	if !hasRedPixel(t, out) {
		t.Error("output lost the red content after trim+object-crop")
	}
}

// TestOpFaceCropWithReadyBoxes проверяет, что при DetectionsReady=true
// процессор НЕ вызывает ИИ-модель, а использует переданные боксы
// (координаты оригинала; fc — без trim, боксы как есть).
func TestOpFaceCropWithReadyBoxes(t *testing.T) {
	det := &fakeDetector{}
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 60, Height: 40}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}, Detector: det, DetectorMargin: 0})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	// Красный прямоугольник [20,20)x[80,60) на холсте 120x80. Бокс в
	// координатах оригинала совпадает с ним.
	boxes := []filemeta.PixelBox{{X: 20, Y: 20, Width: 60, Height: 40}}
	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, true, boxes)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	if det.calls != 0 {
		t.Errorf("detector calls = %d, want 0 (DetectionsReady skips model)", det.calls)
	}
	w, h := decodePngSize(t, out)
	if w != 60 || h != 40 {
		t.Errorf("output size = %dx%d, want 60x40", w, h)
	}
	if !hasRedPixel(t, out) {
		t.Error("output lost the red content after face-crop with ready boxes")
	}
}

// TestOpFaceCropTrimReadyBoxesTranslation проверяет трансляцию предзаданных
// боксов на trim-offset: бокс задан в координатах ОРИГИНАЛА (120x80), а
// кадр после trim — 60x40 (красный прямоугольник [20,20)x[80,60)).
func TestOpFaceCropTrimReadyBoxesTranslation(t *testing.T) {
	det := &fakeDetector{}
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 60, Height: 40}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Trim = true

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}, Detector: det, DetectorMargin: 0})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	// Бокс в координатах оригинала: весь красный прямоугольник.
	boxes := []filemeta.PixelBox{{X: 20, Y: 20, Width: 60, Height: 40}}
	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, true, boxes)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	if det.calls != 0 {
		t.Errorf("detector calls = %d, want 0 (DetectionsReady skips model)", det.calls)
	}
	w, h := decodePngSize(t, out)
	if w != 60 || h != 40 {
		t.Errorf("output size = %dx%d, want 60x40", w, h)
	}
	if !hasRedPixel(t, out) {
		t.Error("output lost the red content after trim+face-crop with ready boxes")
	}
}
