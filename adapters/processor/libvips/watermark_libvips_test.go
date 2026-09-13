//go:build libvips

// Интеграционный тест покадровой ватермарки на анимированных выходах
// (GIF/WebP/HEIF) через реальный govips-движок. Компилируется ТОЛЬКО с
// тэком "libvips" (требует libvips + cgo-окружение, см. docs/INSTALLATION.md).
//
// Проверяет, что ватермарка накладывается на КАЖДЫЙ кадр анимации, а не
// только на первый (регрессия: композит на сшитый холст попадал бы лишь в
// область первого кадра).
package libvips

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// makeGif собирает 2-кадровый GIF: кадр 0 — сплошной красный, кадр 1 —
// сплошной синий. Кадры одинакового размера (32×32, без смещений), чтобы
// после загрузки libvips представил их как вертикальный стек с page-height.
func makeGif(t *testing.T) []byte {
	t.Helper()
	const w, h = 32, 32
	pal := color.Palette{
		color.RGBA{255, 0, 0, 255},
		color.RGBA{0, 0, 255, 255},
	}
	f0 := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	f1 := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f0.SetColorIndex(x, y, 0)
			f1.SetColorIndex(x, y, 1)
		}
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, &gif.GIF{
		Image: []*image.Paletted{f0, f1},
		Delay: []int{10, 10},
	}); err != nil {
		t.Fatalf("gif encode: %v", err)
	}
	return out.Bytes()
}

// makePng собирает сплошную зелёную PNG-ватермарку 8×8.
func makePng(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetRGBA(x, y, color.RGBA{0, 255, 0, 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return out.Bytes()
}

// countFrames возвращает число кадров в GIF-буфере (для проверки, что
// анимация сохранена из 2 кадров после обработки).
func countFrames(t *testing.T, data []byte) int {
	t.Helper()
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gif decode: %v", err)
	}
	return len(g.Image)
}

// pixelAt возвращает RGBA-цвет пикселя кадра frame (0..1) из GIF-буфера.
// Кадры GIF — отдельные изображения (не вертикальный стек), поэтому
// декодируем через gif.DecodeAll и читаем пиксель из нужного кадра.
func pixelAt(t *testing.T, data []byte, frame, x, y int) (uint8, uint8, uint8, uint8) {
	t.Helper()
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gif decode: %v", err)
	}
	if frame >= len(g.Image) {
		t.Fatalf("gif has %d frames, want frame %d", len(g.Image), frame)
	}
	px := g.Image[frame].At(x, y)
	r, gr, b, a := px.RGBA()
	return uint8(r >> 8), uint8(gr >> 8), uint8(b >> 8), uint8(a >> 8)
}

func TestWatermarkAppliedToEveryFrame(t *testing.T) {
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatNoRepeat, "8px 8px")
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Watermark = wm

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data

	// Анимация сохранена: 2 кадра в выходе.
	if n := countFrames(t, out); n != 2 {
		t.Fatalf("output has %d frames, want 2", n)
	}

	// Центр каждого кадра — зелёная ватермарка.
	for _, frame := range []int{0, 1} {
		r, g, bl, _ := pixelAt(t, out, frame, 16, 16)
		if !(g > 200 && r < 50 && bl < 50) {
			t.Errorf("frame %d center = (%d,%d,%d), want green", frame, r, g, bl)
		}
	}
	// Углы: кадр 0 красный, кадр 1 синий (ватермарка не покрыла).
	r0, g0, b0, _ := pixelAt(t, out, 0, 2, 2)
	if !(r0 > 200 && g0 < 50 && b0 < 50) {
		t.Errorf("frame 0 corner = (%d,%d,%d), want red", r0, g0, b0)
	}
	r1, g1, b1, _ := pixelAt(t, out, 1, 2, 2)
	if !(b1 > 200 && r1 < 50 && g1 < 50) {
		t.Errorf("frame 1 corner = (%d,%d,%d), want blue", r1, g1, b1)
	}
}

// TestWatermarkOpacity проверяет применение прозрачности ватермарки:
//   - opacity 0 — знак полностью прозрачен (пиксель центра = фон кадра);
//   - opacity 50 — знак полупрозрачен (смешение зелёного с фоном);
//   - opacity 100 (дефолт) — знак непрозрачен (как раньше).
func TestWatermarkOpacity(t *testing.T) {
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	cases := []struct {
		name    string
		opacity int
		check   func(t *testing.T, r, g, bl uint8)
	}{
		{
			name:    "opacity 0 invisible",
			opacity: 0,
			check: func(t *testing.T, r, g, bl uint8) {
				if !(r > 200 && g < 50 && bl < 50) {
					t.Errorf("center = (%d,%d,%d), want red background (wm invisible)", r, g, bl)
				}
			},
		},
		{
			name:    "opacity 50 blend",
			opacity: 50,
			check: func(t *testing.T, r, g, bl uint8) {
				// Полусмешение зелёного (0,255,0) с красным (255,0,0):
				// ~ (128,128,0); допускаем погрешность квантования.
				if !(g > 80 && g < 200 && r > 80 && r < 200 && bl < 50) {
					t.Errorf("center = (%d,%d,%d), want red/green blend", r, g, bl)
				}
			},
		},
		{
			name:    "opacity 100 opaque",
			opacity: 100,
			check: func(t *testing.T, r, g, bl uint8) {
				if !(g > 200 && r < 50 && bl < 50) {
					t.Errorf("center = (%d,%d,%d), want green (opaque wm)", r, g, bl)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatNoRepeat, "8px 8px")
			if err != nil {
				t.Fatalf("NewWatermarkSpec: %v", err)
			}
			wm.Opacity = tc.opacity
			p := *plan
			p.Watermark = wm
			res, err := b.process(context.Background(), makeGif(t), &p, false, nil, nil)
			if err != nil {
				t.Fatalf("process: %v", err)
			}
			r, g, bl, _ := pixelAt(t, res.data, 0, 16, 16)
			tc.check(t, r, g, bl)
		})
	}
}

func TestWatermarkNoWatermark(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data
	// Анимация сохранена: 2 кадра в выходе.
	if n := countFrames(t, out); n != 2 {
		t.Fatalf("output has %d frames, want 2", n)
	}
	// Кадры не тронуты: центр кадра 0 красный, кадра 1 — синий.
	r0, g0, b0, _ := pixelAt(t, out, 0, 16, 16)
	if !(r0 > 200 && g0 < 50 && b0 < 50) {
		t.Errorf("frame 0 center = (%d,%d,%d), want red", r0, g0, b0)
	}
	r1, g1, b1, _ := pixelAt(t, out, 1, 16, 16)
	if !(b1 > 200 && r1 < 50 && g1 < 50) {
		t.Errorf("frame 1 center = (%d,%d,%d), want blue", r1, g1, b1)
	}
}

// loadAnimated декодирует выходной буфер через govips (NumPages=-1) и
// возвращает ImageRef анимации (вертикальный стек кадров с page-height).
// Вызывающий обязан закрыть результат. Используется для форматов, которые
// stdlib не декодирует покадрово (WebP, APNG): libvips читает их нативно.
func loadAnimated(t *testing.T, data []byte) *vips.ImageRef {
	t.Helper()
	params := vips.NewImportParams()
	params.NumPages.Set(-1)
	img, err := vips.LoadImageFromBuffer(data, params)
	if err != nil {
		t.Fatalf("load animated output: %v", err)
	}
	return img
}

// vipsPixelAt возвращает RGBA-пиксель кадра frame (0..n-1) в координатах
// (x,y) кадра из вертикального стека img. Кадры лежат друг под другом
// (page-height), поэтому абсолютная координата Y = frame*ph + y.
func vipsPixelAt(t *testing.T, img *vips.ImageRef, frame, x, y int) (uint8, uint8, uint8, uint8) {
	t.Helper()
	ph := img.PageHeight()
	if ph <= 0 {
		ph = img.Height()
	}
	px, err := img.GetPoint(x, frame*ph+y)
	if err != nil {
		t.Fatalf("get point (%d,%d) frame %d: %v", x, y, frame, err)
	}
	// 16-битные изображения возвращают значения 0..65535; приводим к 0..255.
	scale := 1.0
	switch img.Interpretation() {
	case vips.InterpretationRGB16, vips.InterpretationGrey16:
		scale = 257.0
	}
	// Альфа-канал есть не у всех выходов (lossy WebP может быть 3-канальным).
	var a uint8 = 255
	if len(px) > 3 {
		a = uint8(px[3] / scale)
	}
	return uint8(px[0] / scale), uint8(px[1] / scale), uint8(px[2] / scale), a
}

// apngWriteSupported определяет, умеет ли собранный libvips ПИСАТЬ APNG.
// pngsave пишет APNG-чанки (acTL/fcTL/fdAT) только если libvips собран с
// libspng; Alpine-пакет vips собран только с libpng → pngsave пишет
// статичный PNG (кадры склеиваются в один высокий холст). Проверка:
// экспортируем 2-кадровый стек в PNG и смотрим, читается ли он как
// multi-page. Если нет — тесты APNG-выхода пропускаются (ограничение
// окружения, а не регрессия кода).
func apngWriteSupported(t *testing.T) bool {
	t.Helper()
	params := vips.NewImportParams()
	params.NumPages.Set(-1)
	img, err := vips.LoadImageFromBuffer(makeGif(t), params)
	if err != nil {
		t.Fatalf("load gif: %v", err)
	}
	defer img.Close()
	data, _, err := img.ExportPng(vips.NewPngExportParams())
	if err != nil {
		t.Fatalf("export png: %v", err)
	}
	back, err := vips.LoadImageFromBuffer(data, params)
	if err != nil {
		t.Fatalf("reload png: %v", err)
	}
	defer back.Close()
	return back.Pages() > 1
}

// TestWatermarkAnimatedOutputWebP проверяет покадровую ватермарку для
// анимированного WebP-выхода (GIF→WebP): центр каждого кадра — зелёная
// ватермарка, углы — фон кадров (красный/синий). Пороги цветов смягчены:
// lossy-кодирование WebP вносит квантование.
func TestWatermarkAnimatedOutputWebP(t *testing.T) {
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatNoRepeat, "8px 8px")
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatWebP,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Watermark = wm

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	out := loadAnimated(t, res.data)
	defer out.Close()

	if n := out.Pages(); n != 2 {
		t.Fatalf("webp output has %d pages, want 2", n)
	}
	if ph := out.PageHeight(); ph != 32 {
		t.Fatalf("webp output page-height = %d, want 32", ph)
	}
	for _, frame := range []int{0, 1} {
		r, g, bl, _ := vipsPixelAt(t, out, frame, 16, 16)
		if !(g > 180 && r < 80 && bl < 80) {
			t.Errorf("webp frame %d center = (%d,%d,%d), want green", frame, r, g, bl)
		}
	}
	r0, g0, b0, _ := vipsPixelAt(t, out, 0, 2, 2)
	if !(r0 > 180 && g0 < 80 && b0 < 80) {
		t.Errorf("webp frame 0 corner = (%d,%d,%d), want red", r0, g0, b0)
	}
	r1, g1, b1, _ := vipsPixelAt(t, out, 1, 2, 2)
	if !(b1 > 180 && r1 < 80 && g1 < 80) {
		t.Errorf("webp frame 1 corner = (%d,%d,%d), want blue", r1, g1, b1)
	}
}

// TestWatermarkAnimatedOutputAPNG проверяет покадровую ватермарку для
// анимированного APNG-выхода (GIF→APNG). APNG — lossless, цвета точные.
// Пропускается, если собранный libvips не умеет писать APNG (нет libspng:
// pngsave пишет статичный PNG) — см. apngWriteSupported.
func TestWatermarkAnimatedOutputAPNG(t *testing.T) {
	if !apngWriteSupported(t) {
		t.Skip("libvips built without libspng: pngsave cannot write APNG")
	}
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatNoRepeat, "8px 8px")
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatAPNG,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Watermark = wm

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	out := loadAnimated(t, res.data)
	defer out.Close()

	if n := out.Pages(); n != 2 {
		t.Fatalf("apng output has %d pages, want 2", n)
	}
	if ph := out.PageHeight(); ph != 32 {
		t.Fatalf("apng output page-height = %d, want 32", ph)
	}
	for _, frame := range []int{0, 1} {
		r, g, bl, _ := vipsPixelAt(t, out, frame, 16, 16)
		if !(g > 200 && r < 50 && bl < 50) {
			t.Errorf("apng frame %d center = (%d,%d,%d), want green", frame, r, g, bl)
		}
	}
	r0, g0, b0, _ := vipsPixelAt(t, out, 0, 2, 2)
	if !(r0 > 200 && g0 < 50 && b0 < 50) {
		t.Errorf("apng frame 0 corner = (%d,%d,%d), want red", r0, g0, b0)
	}
	r1, g1, b1, _ := vipsPixelAt(t, out, 1, 2, 2)
	if !(b1 > 200 && r1 < 50 && g1 < 50) {
		t.Errorf("apng frame 1 corner = (%d,%d,%d), want blue", r1, g1, b1)
	}
}

// TestWatermarkAnimatedCrop проверяет покадровую ватермарку при операции
// OpCrop (центрированный кроп 16x16 из 32x32): центр каждого кадра —
// зелёная ватермарка, углы — фон кадров.
func TestWatermarkAnimatedCrop(t *testing.T) {
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatNoRepeat, "8px 8px")
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpCrop, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Watermark = wm

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := res.data

	if n := countFrames(t, out); n != 2 {
		t.Fatalf("output has %d frames, want 2", n)
	}
	for _, frame := range []int{0, 1} {
		r, g, bl, _ := pixelAt(t, out, frame, 8, 8)
		if !(g > 200 && r < 50 && bl < 50) {
			t.Errorf("crop frame %d center = (%d,%d,%d), want green", frame, r, g, bl)
		}
	}
	r0, g0, b0, _ := pixelAt(t, out, 0, 2, 2)
	if !(r0 > 200 && g0 < 50 && b0 < 50) {
		t.Errorf("crop frame 0 corner = (%d,%d,%d), want red", r0, g0, b0)
	}
	r1, g1, b1, _ := pixelAt(t, out, 1, 2, 2)
	if !(b1 > 200 && r1 < 50 && g1 < 50) {
		t.Errorf("crop frame 1 corner = (%d,%d,%d), want blue", r1, g1, b1)
	}
}

// TestWatermarkRepeatAnimated проверяет repeat-режим ватермарки на
// анимированном выходе: копии 8x8 тайлят холст 32x32 (сетка 4x4), поэтому
// ЛЮБОЙ пиксель каждого кадра покрыт ватермаркой (зелёный).
//
// Выход — WEBP с ПОЛУПРОЗРАЧНОЙ ватермаркой (opacity=50): сплошная
// непрозрачная repeat-ватермарка делает оба кадра ИДЕНТИЧНО зелёными, и
// энкодеры (gifsave/webpsave) схлопывают идентичные кадры в один — это
// свойство энкодеров, а не потеря анимации пайплайном. opacity=50
// сохраняет различие кадров (красный+зелёный vs синий+зелёный), поэтому
// WebP сохраняет 2 страницы, и проверяется, что repeat-композит наложен
// на ОБА кадра (иначе второй остался бы синим).
func TestWatermarkRepeatAnimated(t *testing.T) {
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatRepeat, "8px 8px")
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	wm.Opacity = 50
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatWebP,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Watermark = wm

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	out := loadAnimated(t, res.data)
	defer out.Close()

	if n := out.Pages(); n != 2 {
		t.Fatalf("webp output has %d pages, want 2", n)
	}
	// Пробные точки: углы тайлов и центр холста. С opacity=50 каждый кадр —
	// смесь фона кадра с 50% зелёной ватермарки:
	//   кадр 0 (красный фон): r≈127, g≈127, b≈0;
	//   кадр 1 (синий фон):   r≈0,   g≈127, b≈127.
	// Если бы repeat-композит попал только в первый кадр, второй остался бы
	// чистым синим (b≈255, g≈0).
	for _, frame := range []int{0, 1} {
		for _, pt := range [][2]int{{2, 2}, {10, 10}, {16, 16}, {28, 28}} {
			r, g, bl, _ := vipsPixelAt(t, out, frame, pt[0], pt[1])
			if frame == 0 {
				if !(r > 100 && g > 100 && bl < 50) {
					t.Errorf("repeat frame 0 at (%d,%d) = (%d,%d,%d), want red+green mix", pt[0], pt[1], r, g, bl)
				}
			} else if !(r < 50 && g > 100 && bl > 100) {
				t.Errorf("repeat frame 1 at (%d,%d) = (%d,%d,%d), want blue+green mix", pt[0], pt[1], r, g, bl)
			}
		}
	}
}

// --- Диагностические тесты БЕЗ ватермарки: изолируют, теряется ли анимация
// из-за ватермарки или это общий баг пайплайна (операция/экспорт). ---

// TestDiagNoWatermarkAPNG: GIF→APNG без ватермарки — анимация сохраняется?
// Пропускается, если libvips не умеет писать APNG (нет libspng).
func TestDiagNoWatermarkAPNG(t *testing.T) {
	if !apngWriteSupported(t) {
		t.Skip("libvips built without libspng: pngsave cannot write APNG")
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatAPNG,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := loadAnimated(t, res.data)
	defer out.Close()
	if n := out.Pages(); n != 2 {
		t.Fatalf("apng (no wm) has %d pages, want 2", n)
	}
}

// TestDiagNoWatermarkCrop: GIF→GIF OpCrop без ватермарки — анимация сохраняется?
func TestDiagNoWatermarkCrop(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpCrop, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	out := loadAnimated(t, res.data)
	defer out.Close()
	t.Logf("crop (no wm): pages=%d pageHeight=%d W=%d H=%d",
		out.Pages(), out.PageHeight(), out.Width(), out.Height())
	if n := countFrames(t, res.data); n != 2 {
		t.Fatalf("crop (no wm) has %d frames, want 2", n)
	}
}

// TestDiagWatermarkCropResize: GIF→GIF OpResize 16x16 с ватермаркой —
// изолирует влияние размера цели (16x16 как в OpCrop-тесте).
func TestDiagWatermarkCropResize(t *testing.T) {
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatNoRepeat, "8px 8px")
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Watermark = wm
	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if n := countFrames(t, res.data); n != 2 {
		t.Fatalf("resize16 (wm) has %d frames, want 2", n)
	}
}
