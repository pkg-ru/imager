//go:build libvips

// Интеграционные тесты trim-вариантов детекторных кропов (sct/fct/oct) через
// реальный govips-движок. Компилируются ТОЛЬКО с тэком "libvips" (требует
// libvips + cgo-окружение, см. docs/INSTALLATION.md).
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
	"image/gif"
	"image/png"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/adapters/processor/detection"
	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/ports/processor"
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

// makeTrimPngTwoColor генерирует PNG WxH: белый фон, красный прямоугольник
// [rx0,ry0)x[rx1,ry1) и синий прямоугольник [bx0,by0)x[bx1,by1). После
// find_trim (threshold 0.0) область трима = bounding box обоих цветных
// прямоугольников (контент), белая рамка обрезается.
func makeTrimPngTwoColor(t *testing.T, W, H, rx0, ry0, rx1, ry1, bx0, by0, bx1, by1 int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	white := color.RGBA{255, 255, 255, 255}
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if x >= rx0 && x < rx1 && y >= ry0 && y < ry1 {
				img.SetRGBA(x, y, red)
			} else if x >= bx0 && x < bx1 && y >= by0 && y < by1 {
				img.SetRGBA(x, y, blue)
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

func (f *fakeDetector) Describe() detection.DetectorInfo { return detection.DetectorInfo{} }

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

	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, false, nil, nil)
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

	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, false, nil, nil)
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

	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, false, nil, nil)
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
	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, true, boxes, nil)
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

// TestFaceCropDegradesToCenterCropOnDetectionOverload: при
// ПЕРЕГРУЗКЕ detection-семафора (ErrTooManyDetectionConcurrency) запрос с
// face-crop НЕ завершается ошибкой 503, а деградирует к center-crop:
//   - обработка завершается УСПЕХОМ (валидный PNG целевого размера);
//   - модель НЕ вызывается (детектор не получает кадр);
//   - vips-слот корректно освобождён (следующий Process проходит).
func TestFaceCropDegradesToCenterCropOnDetectionOverload(t *testing.T) {
	det := &fakeDetector{}
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 60, Height: 40}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	// Detection-семафор с concurrency=1 и коротким maxWait: перегрузка
	// детерминированно воспроизводится занятием единственного слота.
	p, err := New(Options{
		Limits:         Limits{Concurrency: 1},
		Detector:       det,
		DetectorMargin: 0,
		DetectionSem:   DetectionSemaphoreOpts{Concurrency: 1, MaxWait: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Не вызываем p.Close(): vips.Shutdown() нельзя перезапустить (см.
	// rewind_libvips_test.go).

	// Занимаем единственный detection-слот «чужим» запросом (эмуляция
	// инференса другого запроса) — handoff гарантированно откажется.
	if err := p.gate.det.Acquire(context.Background()); err != nil {
		t.Fatalf("blocker det.Acquire: %v", err)
	}

	var out bytes.Buffer
	_, err = p.Process(context.Background(), processor.Input{
		Source: bytes.NewReader(makeTrimPng(t, 120, 80, 20, 20, 80, 60)),
		Plan:   plan,
	}, &out)
	p.gate.det.Release()
	if err != nil {
		t.Fatalf("Process with overloaded detection semaphore must degrade, got error: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("empty output on degraded processing")
	}
	// Модель не вызывалась — деградация, а не self-detection.
	if det.calls != 0 {
		t.Errorf("detector calls = %d, want 0 (degraded to center-crop)", det.calls)
	}
	w, h := decodePngSize(t, out.Bytes())
	if w != 60 || h != 40 {
		t.Errorf("output size = %dx%d, want 60x40 (center-crop)", w, h)
	}
	if !hasRedPixel(t, out.Bytes()) {
		t.Error("output lost the red content after degraded center-crop")
	}

	// vips-слот освобождён корректно: следующий Process (без перегрузки
	// detection) проходит успешно.
	var out2 bytes.Buffer
	res2, err := p.Process(context.Background(), processor.Input{
		Source: bytes.NewReader(makeTrimPng(t, 120, 80, 20, 20, 80, 60)),
		Plan:   plan,
	}, &out2)
	if err != nil {
		t.Fatalf("second Process after degraded one: %v", err)
	}
	if res2 == nil || out2.Len() == 0 {
		t.Fatal("second Process returned empty result")
	}
}

// makeTrimGif собирает 2-кадровый GIF 32x32: белый фон (однотонная рамка) и
// сплошной красный прямоугольник [8,8)x[24,24) в центре КАЖДОГО кадра.
// Кадры одинакового размера и без смещений, чтобы libvips представил их как
// вертикальный стек с page-height = 32.
func makeTrimGif(t *testing.T) []byte {
	t.Helper()
	const w, h = 32, 32
	pal := color.Palette{
		color.RGBA{255, 255, 255, 255}, // фон-рамка
		color.RGBA{255, 0, 0, 255},     // контент кадра 0
		color.RGBA{0, 0, 255, 255},     // контент кадра 1
	}
	frames := make([]*image.Paletted, 2)
	for i := range frames {
		f := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if x >= 8 && x < 24 && y >= 8 && y < 24 {
					f.SetColorIndex(x, y, uint8(i+1))
				} else {
					f.SetColorIndex(x, y, 0)
				}
			}
		}
		frames[i] = f
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, &gif.GIF{
		Image: frames,
		Delay: []int{10, 10},
	}); err != nil {
		t.Fatalf("gif encode: %v", err)
	}
	return out.Bytes()
}

// decodeGifFrameSize возвращает размеры кадра frame декодированного GIF.
func decodeGifFrameSize(t *testing.T, data []byte, frame int) (int, int) {
	t.Helper()
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gif decode: %v", err)
	}
	if frame >= len(g.Image) {
		t.Fatalf("gif has %d frames, want frame %d", len(g.Image), frame)
	}
	b := g.Image[frame].Bounds()
	return b.Dx(), b.Dy()
}

// TestTrimAnimatedGifColorBorder проверяет trim на анимации с НЕ прозрачной,
// а цветной (белой) рамкой: trim-region вычисляется по первому кадру
// мультистраничного стека (FindTrim по всему стеку даёт top за пределами
// кадра) и применяется покадрово. Ожидания:
//   - анимация сохранена (2 кадра в выходе);
//   - каждый кадр обрезан с 32x32 до trim-области 16x16;
//   - контент обоих кадров сохранён.
func TestTrimAnimatedGifColorBorder(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Trim = true

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeTrimGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data

	// Анимация сохранена: 2 кадра.
	if n := countFrames(t, out); n != 2 {
		t.Fatalf("output has %d frames, want 2", n)
	}

	// Каждый кадр обрезан до trim-области 16x16.
	for frame := 0; frame < 2; frame++ {
		w, h := decodeGifFrameSize(t, out, frame)
		if w != 16 || h != 16 {
			t.Errorf("frame %d size = %dx%d, want 16x16", frame, w, h)
		}
	}

	// Контент сохранён: центр кадра 0 красный, кадра 1 — синий (кадры
	// отличаются, иначе gifsave схлопывает идентичные кадры в один).
	r, g, bl, _ := pixelAt(t, out, 0, 8, 8)
	if !(r > 200 && g < 50 && bl < 50) {
		t.Errorf("frame 0 center = (%d,%d,%d), want red", r, g, bl)
	}
	r, g, bl, _ = pixelAt(t, out, 1, 8, 8)
	if !(r < 50 && g < 50 && bl > 200) {
		t.Errorf("frame 1 center = (%d,%d,%d), want blue", r, g, bl)
	}
}

// makeTrimPngAlpha генерирует PNG 60x40 с альфа-каналом: НЕ прозрачная
// (непрозрачная белая) рамка и непрозрачный красный прямоугольник
// [20,10)x[40,30) в центре. Изображение загружается в libvips с 4 бандами
// (RGBA); раньше find_trim сравнивал все 4 канала и цветная рамка не
// всегда распознавалась как фон — теперь FindTrim выполняется на RGB-копии
// (ExtractBand(0,3)), а ExtractArea на оригинале.
func makeTrimPngAlpha(t *testing.T, W, H, x0, y0, x1, y1 int) []byte {
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

// TestTrimPngWithAlphaColorBorder проверяет trim PNG с альфа-каналом и
// цветной (белой) непрозрачной рамкой: рамка должна обрезаться, контент
// (красный прямоугольник 20x20) — сохраниться.
func TestTrimPngWithAlphaColorBorder(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Trim = true

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeTrimPngAlpha(t, 60, 40, 20, 10, 40, 30), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	w, h := decodePngSize(t, out)
	if w != 20 || h != 20 {
		t.Errorf("output size = %dx%d, want 20x20 (white border trimmed)", w, h)
	}
	if !hasRedPixel(t, out) {
		t.Error("output lost the red content after trim")
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
	res, err := b.process(context.Background(), makeTrimPng(t, 120, 80, 20, 20, 80, 60), plan, true, boxes, nil)
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

// TestOpObjectCropTrimReadyBoxesPosition проверяет, что при trim + готовых
// боксах (DetectionsReady=true) кроп-окно позиционируется ПРАВИЛЬНО, т.е.
// боксы из sidecar (координаты ОРИГИНАЛА) транслируются на trim-offset.
//
// Сценарий: холст 120x80, белая рамка. Контент — ДВА цветных прямоугольника:
// красный (объект/лицо) [20,20)x[50,40) и синий (фон) [50,20)x[100,60).
// Trim (auto, белый фон) обрезает рамку до bounding box контента
// [20,20)x[100,60) → кадр 80x40, где красный теперь [0,0)x[30,20), а синий
// [30,0)x[80,40). Бокс из sidecar задан в координатах ОРИГИНАЛА:
// {X:20, Y:20, W:30, H:20} (красный объект). Цель кропа = ровно размер
// объекта (30x20).
//
// Регрессия: translateBoxes НЕ вычитал trim-offset (20,20), бокс оставался
// [20,20)x[50,40), и SelectCrop вырезал регион [20,20)x[50,40) — это СИНИЙ
// фон (объект вне кропа). Итог 30x20 синий, красного нет вовсе.
// С фиксом бокс транслируется в [0,0)x[30,20) — кроп попадает ровно на
// красный объект, итог 30x20 красный.
func TestOpObjectCropTrimReadyBoxesPosition(t *testing.T) {
	det := &fakeDetector{}
	plan, err := processing.NewProcessingPlan(
		processing.OpObjectCrop, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 30, Height: 20}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Trim = true

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}, Detector: det, DetectorMargin: 0})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	// Бокс в координатах ОРИГИНАЛА: красный объект [20,20)x[50,40).
	boxes := []filemeta.PixelBox{{X: 20, Y: 20, Width: 30, Height: 20}}
	res, err := b.process(context.Background(),
		makeTrimPngTwoColor(t, 120, 80, 20, 20, 50, 40, 50, 20, 100, 60),
		plan, true, boxes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	if det.calls != 0 {
		t.Errorf("detector calls = %d, want 0 (DetectionsReady skips model)", det.calls)
	}
	w, h := decodePngSize(t, out)
	if w != 30 || h != 20 {
		t.Errorf("output size = %dx%d, want 30x20", w, h)
	}
	// Итог обязан содержать красный объект. При регрессии (offset не вычтен)
	// кроп вырезает синий фон — красного нет вовсе.
	if !hasRedPixel(t, out) {
		t.Error("output lost the red object after trim+object-crop with ready boxes; trim-offset not applied to ready boxes")
	}
}
