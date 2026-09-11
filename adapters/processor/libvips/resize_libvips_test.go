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
// (оба измерения заданы): SizeBoth при resize ВПИСЫВАЕТ изображение в
// целевой размер с сохранением пропорций (fit, без кропа) — из 400x200
// получается 200x100. Изменения поведения на эту ветку фикс не вносил.
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
	if w != 200 || h != 100 {
		t.Errorf("output size = %dx%d, want 200x100", w, h)
	}
}
