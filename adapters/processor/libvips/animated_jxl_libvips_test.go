//go:build libvips

// Регрессионные тесты анимированного JPEG XL:
//
//   - animated GIF → JXL: jxlsave пишет multi-page изображение как
//     анимацию (кадры + frame timing). Проверка: выход содержит 2 кадра
//     (page-height < высоты стека) и delay первого кадра 100 мс.
//   - static JPEG → JXL: статичный JXL (1 страница), без регрессии.
//   - animated JXL → повторная обработка (JXL → WebP): jxlload с
//     page/n загружает все кадры (NumPages=-1), анимация сохраняется.
//
// Компилируется ТОЛЬКО с тэгом "libvips" (прогон — через docker-test).
package libvips

import (
	"context"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// TestAnimatedGifToJxlKeepsAnimation — главный регрессионный тест:
// 2-кадровый GIF (32×32, delay 100 мс) → JXL должен содержать анимацию:
// 2 кадра (page-height 32, высота стека 64) и delay первого кадра 100 мс.
func TestAnimatedGifToJxlKeepsAnimation(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatJPEGXL,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b := makeJpegBackend(t)
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	img, err := vips.LoadImageFromBuffer(res.data, func() *vips.ImportParams {
		p := vips.NewImportParams()
		p.NumPages.Set(-1)
		return p
	}())
	if err != nil {
		t.Fatalf("jxl load: %v", err)
	}
	defer img.Close()

	if img.Width() != 32 || img.Height() != 64 {
		t.Fatalf("output size = %dx%d, want 32x64 (2 frames stacked)", img.Width(), img.Height())
	}
	if ph := img.PageHeight(); ph != 32 {
		t.Errorf("page-height = %d, want 32", ph)
	}
	if n := img.Pages(); n != 2 {
		t.Errorf("pages = %d, want 2 (animation preserved)", n)
	}
	delay, err := img.PageDelay()
	if err != nil {
		t.Fatalf("page delay: %v", err)
	}
	if len(delay) != 2 || delay[0] != 100 {
		t.Errorf("page delay = %v, want [100 100] (frame timing preserved)", delay)
	}
}

// TestStaticJpegToJxlStaticPath — статичный вход → JXL: валидный статичный
// JXL (1 страница), без регрессии.
func TestStaticJpegToJxlStaticPath(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatJPEGXL,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b := makeJpegBackend(t)
	res, err := b.process(context.Background(), makeJpeg(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
	if err != nil {
		t.Fatalf("jxl load: %v", err)
	}
	defer img.Close()

	// makeJpeg — сплошной красный JPEG 8×8 (см. animated_avif_libvips_test.go).
	if img.Width() != 8 || img.Height() != 8 {
		t.Fatalf("output size = %dx%d, want 8x8 (single frame)", img.Width(), img.Height())
	}
	if n := img.Pages(); n != 1 {
		t.Errorf("pages = %d, want 1 (static)", n)
	}
}

// TestAnimatedJxlToWebpKeepsAnimation — повторная обработка анимированного
// JXL (JXL → WebP): jxlload с page/n (NumPages=-1) загружает все кадры,
// webpsave пишет анимацию.
func TestAnimatedJxlToWebpKeepsAnimation(t *testing.T) {
	// Сначала GIF → JXL.
	planJxl, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatJPEGXL,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan(jxl): %v", err)
	}
	b := makeJpegBackend(t)
	resJxl, err := b.process(context.Background(), makeGif(t), planJxl, false, nil, nil)
	if err != nil {
		t.Fatalf("process (gif→jxl): %v", err)
	}

	// Затем JXL → WebP.
	planWebp, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEGXL, processing.FormatWebP,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan(webp): %v", err)
	}
	resWebp, err := b.process(context.Background(), resJxl.data, planWebp, false, nil, nil)
	if err != nil {
		t.Fatalf("process (jxl→webp): %v", err)
	}

	img, err := vips.LoadImageFromBuffer(resWebp.data, func() *vips.ImportParams {
		p := vips.NewImportParams()
		p.NumPages.Set(-1)
		return p
	}())
	if err != nil {
		t.Fatalf("webp load: %v", err)
	}
	defer img.Close()

	if img.Width() != 32 || img.Height() != 64 {
		t.Fatalf("output size = %dx%d, want 32x64 (2 frames stacked)", img.Width(), img.Height())
	}
	if n := img.Pages(); n != 2 {
		t.Errorf("pages = %d, want 2 (animation preserved through jxl roundtrip)", n)
	}
}
