//go:build libvips

// Фиксация поведения APNG-выхода в стандартной сборке: без libspng pngsave
// пишет статичный PNG (кадры склеены в вертикальный холст). Компилируется
// ТОЛЬКО с тэком "libvips" (требует libvips + cgo-окружение).
package libvips

import (
	"bytes"
	"context"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/pkg-ru/imager/domain/processing"
)

// TestAPNGOutputStaticWithoutSpng фиксирует контракт APNG-выхода:
//
//   - стандартная сборка (libvips без libspng) → статичный PNG: Pages()==1,
//     кадры анимации склеены в один вертикальный холст (высота = n × кадр),
//     APNG-чанка acTL в файле нет;
//   - сборка с libspng → APNG анимированный, тест пропускается
//     (поведение покрыто TestWatermarkAnimatedOutputAPNG).
func TestAPNGOutputStaticWithoutSpng(t *testing.T) {
	if apngWriteSupported(t) {
		t.Skip("libvips built with libspng: APNG output is animated")
	}

	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatAPNG,
		processing.Size{Width: 32, Height: 32}, 1, 85, nil, 0, 0,
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

	params := vips.NewImportParams()
	params.NumPages.Set(-1)
	img, err := vips.LoadImageFromBuffer(res.data, params)
	if err != nil {
		t.Fatalf("reload APNG output: %v", err)
	}
	defer img.Close()

	if n := img.Pages(); n != 1 {
		t.Fatalf("APNG output pages = %d, want 1 (static PNG without libspng)", n)
	}
	// Два кадра 32×32 склеены вертикально.
	if w, h := img.Width(), img.Height(); w != 32 || h != 64 {
		t.Errorf("APNG output size = %dx%d, want 32x64 (stacked frames)", w, h)
	}
	// acTL — обязательный чанк анимированного APNG; в статичном PNG его нет.
	if bytes.Contains(res.data, []byte("acTL")) {
		t.Error("APNG output contains acTL chunk, want static PNG without animation")
	}
}

