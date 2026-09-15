//go:build libvips

// Интеграционный тест: анимированный вход (GIF) → НЕ-анимационный выход
// (JPEG) должен давать ровно ОДИН кадр (первый), а не «простыню» всех
// кадров друг под другом. Все операции (resize/crop) применяются к этому
// единственному кадру.
//
// Регрессия: resolveImportPlan грузил ВСЕ кадры (n=-1) при анимированном
// источнике независимо от выходного формата; jpegsave не умеет выбирать
// кадр и писал вертикальный стек (W × n*page-height) как один JPEG.
//
// Компилируется ТОЛЬКО с тэгом "libvips" (требует libvips + cgo-окружение,
// прогон — через docker-test, см. make.ps1).
package libvips

import (
	"context"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// makeJpegBackend создаёт реальный govips-движок для интеграционных тестов.
func makeJpegBackend(t *testing.T) backend {
	t.Helper()
	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	return b
}

// TestAnimatedGifToJpegSingleFrame — регрессия «простыни»: 2-кадровый GIF
// (32×32) → JPEG должен дать изображение 32×32 (только первый кадр), а не
// 32×64 (все кадры столбиком).
func TestAnimatedGifToJpegSingleFrame(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatJPEG,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b := makeJpegBackend(t)
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	// JPEG декодируем через libvips (image/jpeg stdlib не нужен).
	img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
	if err != nil {
		t.Fatalf("jpeg load: %v", err)
	}
	defer img.Close()

	// Высота = высота ОДНОГО кадра (32), не 2*32=64.
	if img.Width() != 32 || img.Height() != 32 {
		t.Fatalf("output size = %dx%d, want 32x32 (single frame, no sheet)", img.Width(), img.Height())
	}
	// JPEG одностраничный: Pages()==1.
	if n := img.Pages(); n != 1 {
		t.Errorf("jpeg pages = %d, want 1", n)
	}
	// Содержимое — первый кадр GIF (сплошной красный).
	r, g, bl, _ := vipsPixelAt(t, img, 0, 16, 16)
	if !(r > 200 && g < 80 && bl < 80) {
		t.Errorf("center pixel = (%d,%d,%d), want red (first frame)", r, g, bl)
	}
}

// TestAnimatedGifToJpegResizeFirstFrame — resize применяется к первому
// кадру, а не ко всему стеку: 2-кадровый GIF 32×32 → JPEG resize 16×16.
func TestAnimatedGifToJpegResizeFirstFrame(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatJPEG,
		processing.Size{Width: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b := makeJpegBackend(t)
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
	if err != nil {
		t.Fatalf("jpeg load: %v", err)
	}
	defer img.Close()

	// Квадратный кадр 32×32, resize по ширине до 16 → 16×16.
	if img.Width() != 16 || img.Height() != 16 {
		t.Fatalf("output size = %dx%d, want 16x16 (resize of single frame)", img.Width(), img.Height())
	}
	r, g, bl, _ := vipsPixelAt(t, img, 0, 8, 8)
	if !(r > 200 && g < 80 && bl < 80) {
		t.Errorf("center pixel = (%d,%d,%d), want red (first frame)", r, g, bl)
	}
}

// TestAnimatedGifToJpegCropFirstFrame — crop применяется к первому кадру:
// 2-кадровый GIF 32×32 → JPEG crop 16×16 (центр) даёт ровно 16×16.
func TestAnimatedGifToJpegCropFirstFrame(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpCrop, processing.FormatGIF, processing.FormatJPEG,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b := makeJpegBackend(t)
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
	if err != nil {
		t.Fatalf("jpeg load: %v", err)
	}
	defer img.Close()

	if img.Width() != 16 || img.Height() != 16 {
		t.Fatalf("output size = %dx%d, want 16x16 (crop of single frame)", img.Width(), img.Height())
	}
	r, g, bl, _ := vipsPixelAt(t, img, 0, 8, 8)
	if !(r > 200 && g < 80 && bl < 80) {
		t.Errorf("center pixel = (%d,%d,%d), want red (first frame)", r, g, bl)
	}
}

// TestAnimatedGifToGifKeepsAnimation — контроль: АНИМАЦИОННЫЙ выход
// (GIF→GIF) по-прежнему сохраняет ВСЕ кадры (поведение не менялось).
func TestAnimatedGifToGifKeepsAnimation(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 32, Height: 32}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b := makeJpegBackend(t)
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	if n := countFrames(t, res.data); n != 2 {
		t.Fatalf("output has %d frames, want 2 (animation preserved)", n)
	}
	// Кадр 0 — красный, кадр 1 — синий.
	r, g, bl, _ := pixelAt(t, res.data, 0, 16, 16)
	if !(r > 200 && g < 50 && bl < 50) {
		t.Errorf("frame 0 center = (%d,%d,%d), want red", r, g, bl)
	}
	r, g, bl, _ = pixelAt(t, res.data, 1, 16, 16)
	if !(bl > 200 && r < 50 && g < 50) {
		t.Errorf("frame 1 center = (%d,%d,%d), want blue", r, g, bl)
	}
}
