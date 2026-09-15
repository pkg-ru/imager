//go:build libvips

// Интеграционный тест пропорционального resize с ОДНОЙ заданной осью
// (размер-грамматика "x200" — только высота, "200x" — только ширина) через
// реальный govips-движок. Компилируется ТОЛЬКО с тэком "libvips".
//
// Регрессии: vips_thumbnail_image требует ЯВНЫЕ ОБА измерения.
//   - "x200" (план Size{Width: 0, Height: 200}): передача width=0 вызывала
//     GLib critical ("value "0" of type 'gint' is invalid or out of range
//     for property 'width'") и ошибку "parameter width not set";
//   - "200x" (план Size{Width: 200, Height: 0}): передача height=0 давала
//     GLib critical ("value "0" ... property 'height'"), свойство оставалось
//     на дефолте (200), и изображение вписывалось в бокс (width × 200)
//     вместо пропорционального ресайза: из 400x600 получалось 133x200
//     вместо 200x300 (на квадратном боксе 400x200 баг был незаметен —
//     fit в 200x200 совпадал с пропорциональным результатом 200x100).
//
// Ожидаемое поведение — пропорциональное сжатие по заданной оси
// (resolveResizeSize дополняет недостающую ось из пропорций кадра).
package libvips

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"bytes"

	"github.com/davidbyttow/govips/v2/vips"
	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// makeSolidPng генерирует сплошной PNG WxH заданного цвета.
func makeSolidPng(t *testing.T, W, H int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return out.Bytes()
}

// TestOpResizeHeightOnly проверяет "x200": исходник 400x200 → высота 200
// даёт ширину 400 (пропорция 2:1 сохраняется); ранее запрос падал с
// "parameter width not set" (width=0 в vips_thumbnail_image).
func TestOpResizeHeightOnly(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 0, Height: 200}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeSolidPng(t, 400, 200, color.RGBA{255, 0, 0, 255}), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	w, h := decodePngSize(t, res.data)
	if w != 400 || h != 200 {
		t.Errorf("output size = %dx%d, want 400x200", w, h)
	}
}

// TestOpResizeWidthOnly проверяет симметричный случай "200x": исходник
// 400x200 → ширина 200 даёт высоту 100 (высота вычисляется
// resolveResizeSize из пропорций кадра).
func TestOpResizeWidthOnly(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 200, Height: 0}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeSolidPng(t, 400, 200, color.RGBA{0, 255, 0, 255}), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	w, h := decodePngSize(t, res.data)
	if w != 200 || h != 100 {
		t.Errorf("output size = %dx%d, want 200x100", w, h)
	}
}

// TestOpResizeWidthOnlyTallSource — регрессия width-only на
// НЕКВАДРАТНОМ боксе: исходник 400x600, план {Width: 200, Height: 0}.
// Раньше height=0 в vips_thumbnail_image давал GLib critical + fallback
// на дефолт свойства 'height' (200): изображение вписывалось в бокс
// 200x200 → 133x200 вместо пропорциональных 200x300.
func TestOpResizeWidthOnlyTallSource(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 200, Height: 0}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeSolidPng(t, 400, 600, color.RGBA{0, 0, 255, 255}), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	w, h := decodePngSize(t, res.data)
	if w != 200 || h != 300 {
		t.Errorf("output size = %dx%d, want 200x300", w, h)
	}
}

// TestOpResizeBothDimensions проверяет контрольный случай "200x200"
// (оба измерения заданы): thumbnail вписывает изображение пропорционально
// в бокс (fit, без кропа), затем Embed добавляет прозрачные поля
// (letterbox/pillarbox) до ТОЧНОГО 200x200. Из 400x200 получается
// 200x100 + прозрачные полосы сверху/снизу по 50px.
func TestOpResizeBothDimensions(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 200, Height: 200}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeSolidPng(t, 400, 200, color.RGBA{0, 0, 255, 255}), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	w, h := decodePngSize(t, res.data)
	if w != 200 || h != 200 {
		t.Errorf("output size = %dx%d, want 200x200", w, h)
	}
	// Верхняя полоса (letterbox) — прозрачная.
	_, _, _, aTop := pngPixelAt(t, res.data, 100, 0)
	if aTop != 0 {
		t.Errorf("top letterbox alpha = %d, want 0 (transparent)", aTop)
	}
	// Центр — синий исходник (400x200 → 200x100, top = 50).
	r, g, bl, a := pngPixelAt(t, res.data, 100, 50)
	if r != 0 || g != 0 || bl != 255 || a != 255 {
		t.Errorf("center pixel = (%d,%d,%d,%d), want (0,0,255,255)", r, g, bl, a)
	}
}

// pngPixelAt возвращает RGBA пикселя декодированного PNG.
func pngPixelAt(t *testing.T, data []byte, x, y int) (uint8, uint8, uint8, uint8) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	r, g, b, a := img.At(x, y).RGBA()
	return uint8(r / 257), uint8(g / 257), uint8(b / 257), uint8(a / 257)
}

// TestOpResizeLetterboxTransparent проверяет letterbox/pillarbox для PNG:
// исходник 100x100, план 200x100 → thumbnail вписывает в 100x100, Embed
// добавляет прозрачные поля слева/справа (pillarbox) до ТОЧНОГО 200x100.
func TestOpResizeLetterboxTransparent(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 200, Height: 100}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeSolidPng(t, 100, 100, color.RGBA{255, 0, 0, 255}), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	w, h := decodePngSize(t, res.data)
	if w != 200 || h != 100 {
		t.Errorf("output size = %dx%d, want 200x100", w, h)
	}
	// Поле слева (x=10) — прозрачное (alpha=0); контент в центре (x=100) —
	// красный непрозрачный.
	_, _, _, a := pngPixelAt(t, res.data, 10, 50)
	if a != 0 {
		t.Errorf("left margin alpha = %d, want 0 (transparent)", a)
	}
	r, g, bl, a := pngPixelAt(t, res.data, 100, 50)
	if !(r > 200 && g < 50 && bl < 50 && a > 200) {
		t.Errorf("center pixel = (%d,%d,%d,%d), want red opaque", r, g, bl, a)
	}
}

// TestOpResizeLetterboxJpegBackground проверяет letterbox для JPEG-выхода:
// исходник 100x100, план 200x100, Background "#ff0000" → поля красные
// (JPEG альфу не поддерживает, используется цвет из конфига).
func TestOpResizeLetterboxJpegBackground(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatJPEG,
		processing.Size{Width: 200, Height: 100}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Background = "#ff0000"

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeSolidPng(t, 100, 100, color.RGBA{0, 0, 255, 255}), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	// JPEG-выход: декодируем через libvips (image/png не читает JPEG).
	img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
	if err != nil {
		t.Fatalf("jpeg load: %v", err)
	}
	defer img.Close()
	if img.Width() != 200 || img.Height() != 100 {
		t.Errorf("output size = %dx%d, want 200x100", img.Width(), img.Height())
	}
	// Поле слева (x=10) — красное (Background); контент в центре (x=100) —
	// синий.
	r, g, bl, _ := vipsPixelAt(t, img, 0, 10, 50)
	if !(r > 200 && g < 50 && bl < 50) {
		t.Errorf("left margin = (%d,%d,%d), want red", r, g, bl)
	}
	r, g, bl, _ = vipsPixelAt(t, img, 0, 100, 50)
	if !(bl > 200 && r < 50 && g < 50) {
		t.Errorf("center pixel = (%d,%d,%d), want blue", r, g, bl)
	}
}

// TestOpResizeLetterboxJpegDefaultWhite проверяет дефолт для JPEG-выхода:
// Background пуст → белые поля (#ffffff).
func TestOpResizeLetterboxJpegDefaultWhite(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatJPEG,
		processing.Size{Width: 200, Height: 100}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeSolidPng(t, 100, 100, color.RGBA{0, 0, 255, 255}), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
	if err != nil {
		t.Fatalf("jpeg load: %v", err)
	}
	defer img.Close()
	r, g, bl, _ := vipsPixelAt(t, img, 0, 10, 50)
	if !(r > 200 && g > 200 && bl > 200) {
		t.Errorf("left margin = (%d,%d,%d), want white", r, g, bl)
	}
}
