//go:build libvips

// Интеграционные тесты детекторного кропа (face-crop/object-crop/-fix) на
// анимированных источниках (GIF→GIF/WEBP/AVIF) через реальный govips-движок.
// Компилируется ТОЛЬКО с тэгом "libvips" (требует libvips + cgo-окружение,
// прогон — через docker-test, см. make.ps1).
//
// Регрессия: applyDetectionCrop работал со всем вертикальным стеком кадров
// как с одним изображением — ExtractArea по стеку + ThumbnailWithSize
// (InterestingCentre, SizeBoth) без покадровой обработки. vips_thumbnail_image
// не поддерживает multi-page: он «склеивал» стек кадров по совокупной высоте
// и схлопывал page-height, в итоге анимация терялась (статичная «склейка
// кадров»). Исправление — покадровая обработка через withFrames (по образцу
// thumbnailCropFrames).
//
// Диагностические тесты TestDiag* удалены: они проверяли старое (неверное)
// поведение heifsave — multi-page HEIF/AVIF без animation track. Новая
// архитектура: animated AVIF → libheif sequence encoder (см.
// animated_avif_libvips.go и animated_avif_libvips_test.go); animated
// HEIF/HEIC → статичный первый кадр (документированное поведение).
package libvips

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"
	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// detBackend создаёт движок с fakeDetector (бокс на весь кадр) для
// детекторных операций.
func detBackend(t *testing.T) (*libvipsBackend, *fakeDetector) {
	t.Helper()
	det := &fakeDetector{}
	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}, Detector: det, DetectorMargin: 0})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	lb, ok := b.(*libvipsBackend)
	if !ok {
		t.Fatalf("backend type = %T, want *libvipsBackend", b)
	}
	return lb, det
}

// checkAnimatedOutput проверяет, что выходной буфер — анимация с n кадрами,
// page-height = ph, размер кадра w x h (Height() — высота ВСЕГО стека,
// т.е. n*ph).
func checkAnimatedOutput(t *testing.T, data []byte, n, ph, w, h int) {
	t.Helper()
	out := loadAnimated(t, data)
	defer out.Close()
	if pages := out.Pages(); pages != n {
		t.Fatalf("output pages = %d, want %d", pages, n)
	}
	if got := out.PageHeight(); got != ph {
		t.Fatalf("output page-height = %d, want %d", got, ph)
	}
	if out.Width() != w || out.Height() != n*ph {
		t.Fatalf("output stack = %dx%d, want %dx%d (%d frames)", out.Width(), out.Height(), w, n*ph, n)
	}
}

// TestDetectionCropAnimatedGifToGifReadyBoxes — GIF(2 кадра)→GIF face-crop
// с готовыми боксами (DetectionsReady=true): n-pages сохранён, page-height
// корректен, размер кадра = plan.Size.
func TestDetectionCropAnimatedGifToGifReadyBoxes(t *testing.T) {
	b, det := detBackend(t)
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	boxes := []filemeta.PixelBox{{X: 0, Y: 0, Width: 32, Height: 32}}
	res, err := b.process(context.Background(), makeGif(t), plan, true, boxes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if det.calls != 0 {
		t.Errorf("detector calls = %d, want 0 (DetectionsReady skips model)", det.calls)
	}
	checkAnimatedOutput(t, res.data, 2, 16, 16, 16)
}

// TestDetectionCropAnimatedGifToWebpReadyBoxes — GIF(2 кадра)→WEBP face-crop
// с готовыми боксами: анимация сохранена (pages=2, page-height=16, 16x16).
func TestDetectionCropAnimatedGifToWebpReadyBoxes(t *testing.T) {
	b, det := detBackend(t)
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatGIF, processing.FormatWebP,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	boxes := []filemeta.PixelBox{{X: 0, Y: 0, Width: 32, Height: 32}}
	res, err := b.process(context.Background(), makeGif(t), plan, true, boxes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if det.calls != 0 {
		t.Errorf("detector calls = %d, want 0 (DetectionsReady skips model)", det.calls)
	}
	checkAnimatedOutput(t, res.data, 2, 16, 16, 16)
}

// TestDetectionCropAnimatedGifToAvifReadyBoxes — GIF(2 кадра)→AVIF face-crop
// с готовыми боксами: анимация сохранена (sequence track с 2 кадрами).
// Верификация через libheif track API: libvips heifload НЕ читает avis-файлы
// (sequence), поэтому loadAnimated/checkAnimatedOutput для AVIF неприменимы.
func TestDetectionCropAnimatedGifToAvifReadyBoxes(t *testing.T) {
	b, det := detBackend(t)
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatGIF, processing.FormatAVIF,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	boxes := []filemeta.PixelBox{{X: 0, Y: 0, Width: 32, Height: 32}}
	res, err := b.process(context.Background(), makeGif(t), plan, true, boxes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if det.calls != 0 {
		t.Errorf("detector calls = %d, want 0 (DetectionsReady skips model)", det.calls)
	}
	if !vips.HeifHasSequence(res.data) {
		t.Fatal("animated GIF → AVIF (face-crop): output has no sequence track")
	}
	if n := vips.HeifSequenceFrameCount(res.data); n != 2 {
		t.Fatalf("sequence frame count = %d, want 2", n)
	}
}

// TestDetectionCropAnimatedSelfDetect — self-detection (модель вызывается
// внутри процессора) на анимации: детектор получает ПЕРВЫЙ кадр (32x32),
// а не весь стек (32x64); анимация выхода сохранена.
func TestDetectionCropAnimatedSelfDetect(t *testing.T) {
	b, det := detBackend(t)
	plan, err := processing.NewProcessingPlan(
		processing.OpObjectCrop, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	res, err := b.process(context.Background(), makeGif(t), plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if det.calls != 1 {
		t.Fatalf("detector calls = %d, want 1", det.calls)
	}
	if det.lastW != 32 || det.lastH != 32 {
		t.Errorf("detector got frame %dx%d, want 32x32 (first frame, not stack 32x64)", det.lastW, det.lastH)
	}
	checkAnimatedOutput(t, res.data, 2, 16, 16, 16)
}

// TestDetectionCropAnimatedFixModes — fix-режимы (face-fix-crop,
// object-fix-crop) на анимации: анимация сохранена, размер = plan.Size.
func TestDetectionCropAnimatedFixModes(t *testing.T) {
	cases := []struct {
		name string
		op   processing.Operation
	}{
		{"face-fix-crop", processing.OpFaceFixCrop},
		{"object-fix-crop", processing.OpObjectFixCrop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := detBackend(t)
			plan, err := processing.NewProcessingPlan(
				tc.op, processing.FormatGIF, processing.FormatGIF,
				processing.Size{Width: 32, Height: 16}, 1, 0, nil, 0, 0,
			)
			if err != nil {
				t.Fatalf("NewProcessingPlan: %v", err)
			}
			boxes := []filemeta.PixelBox{{X: 0, Y: 0, Width: 32, Height: 32}}
			res, err := b.process(context.Background(), makeGif(t), plan, true, boxes, nil)
			if err != nil {
				t.Fatalf("process: %v", err)
			}
			checkAnimatedOutput(t, res.data, 2, 16, 32, 16)
		})
	}
}

// TestDetectionCropAnimatedWithWatermark — детекторный кроп на анимации
// С ватермаркой: полный путь (покадровый кроп → покадровая ватермарка →
// экспорт). Анимация сохранена, ватермарка в центре каждого кадра.
func TestDetectionCropAnimatedWithWatermark(t *testing.T) {
	wmPath := filepath.Join(t.TempDir(), "wm.png")
	if err := os.WriteFile(wmPath, makePng(t), 0o644); err != nil {
		t.Fatalf("write wm: %v", err)
	}
	wm, err := processing.NewWatermarkSpec("wm", wmPath, processing.WatermarkPositionCenter, processing.WatermarkRepeatNoRepeat, "8px 8px")
	if err != nil {
		t.Fatalf("NewWatermarkSpec: %v", err)
	}
	b, _ := detBackend(t)
	plan, err := processing.NewProcessingPlan(
		processing.OpFaceCrop, processing.FormatGIF, processing.FormatGIF,
		processing.Size{Width: 16, Height: 16}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	plan.Watermark = wm
	boxes := []filemeta.PixelBox{{X: 0, Y: 0, Width: 32, Height: 32}}
	res, err := b.process(context.Background(), makeGif(t), plan, true, boxes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	checkAnimatedOutput(t, res.data, 2, 16, 16, 16)
	// Центр каждого кадра — зелёная ватермарка.
	for _, frame := range []int{0, 1} {
		r, g, bl, _ := pixelAt(t, res.data, frame, 8, 8)
		if !(g > 200 && r < 50 && bl < 50) {
			t.Errorf("frame %d center = (%d,%d,%d), want green watermark", frame, r, g, bl)
		}
	}
}
