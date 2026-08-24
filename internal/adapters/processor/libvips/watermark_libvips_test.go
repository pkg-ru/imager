//go:build libvips

// Интеграционный тест покадровой ватермарки на анимированных выходах
// (GIF/WebP/HEIF) через реальный govips-движок. Компилируется ТОЛЬКО с
// тэком "libvips" (требует libvips + cgo-окружение, см. docs/PRODUCTION.md).
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

	"github.com/pkg-ru/imager/internal/domain/processing"
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

	out, err := b.process(context.Background(), makeGif(t), plan)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

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

	out, err := b.process(context.Background(), makeGif(t), plan)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
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
