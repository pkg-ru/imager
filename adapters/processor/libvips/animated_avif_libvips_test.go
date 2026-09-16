//go:build libvips

// Регрессионные тесты анимированного AVIF/HEIF:
//
//   - animated GIF → AVIF: настоящий animation track через libheif
//     sequence encoder (brand avis, sequence track с N кадрами и
//     per-frame duration). Проверка через libheif track API, т.к.
//     libvips heifload НЕ читает avis-файлы (sequence файлы).
//   - animated GIF → HEIF: статичный HEIF (первый кадр), декодируемый
//     libvips heifload; Pages()==1, содержимое — первый кадр GIF.
//   - static JPEG → AVIF/HEIF/HEIC: static path без изменений.
//   - multipage без delay (Pages()>1 && delay==0) — НЕ анимация: уходит
//     в static path.
//
// Компилируется ТОЛЬКО с тэгом "libvips" (прогон — через docker-test).
package libvips

import (
	"context"
	"testing"
	"time"

	"github.com/davidbyttow/govips/v2/vips"

	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// TestAnimatedGifToAvifSequenceTrack — главный регрессионный тест: 2-кадровый
// GIF (32×32, delay 100 мс) → AVIF должен содержать НАСТОЯЩУЮ анимацию:
// sequence track с 2 кадрами и длительностью первого кадра 100 мс.
func TestAnimatedGifToAvifSequenceTrack(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatAVIF,
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

	// libvips heifload не читает avis — верифицируем через libheif track API.
	if !vips.HeifHasSequence(res.data) {
		t.Fatal("animated GIF → AVIF: output has no sequence track (not animated)")
	}
	if n := vips.HeifSequenceFrameCount(res.data); n != 2 {
		t.Fatalf("sequence frame count = %d, want 2", n)
	}
	if d := vips.HeifSequenceFirstDuration(res.data); d != 100 {
		t.Fatalf("first frame duration = %d, want 100 (ms @ timescale 1000)", d)
	}
}

// TestAnimatedGifToHeifStaticFirstFrame — анимированный GIF → HEIF: успешный
// статичный HEIF, ровно 1 страница (первый кадр), декодируется libvips.
func TestAnimatedGifToHeifStaticFirstFrame(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatHEIF,
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

	img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
	if err != nil {
		t.Fatalf("heif load: %v", err)
	}
	defer img.Close()

	if img.Width() != 32 || img.Height() != 32 {
		t.Fatalf("output size = %dx%d, want 32x32 (single frame)", img.Width(), img.Height())
	}
	if n := img.Pages(); n != 1 {
		t.Errorf("heif pages = %d, want 1 (static, first frame only)", n)
	}
	// Содержимое — первый кадр GIF (сплошной красный).
	r, g, bl, _ := vipsPixelAt(t, img, 0, 16, 16)
	if !(r > 200 && g < 80 && bl < 80) {
		t.Errorf("center pixel = (%d,%d,%d), want red (first frame)", r, g, bl)
	}
	// Выход НЕ содержит sequence track (валидный статичный HEIF).
	if vips.HeifHasSequence(res.data) {
		t.Error("heif output unexpectedly contains sequence track")
	}
}

// TestStaticJpegToAvifAndHeifStaticPath — статичные входы идут через
// static path: JPEG → AVIF/HEIF дают валидные статичные файлы без
// sequence track.
func TestStaticJpegToAvifAndHeifStaticPath(t *testing.T) {
	src := makeJpeg(t)

	cases := []struct {
		name   string
		format processing.Format
	}{
		{"jpeg-to-avif", processing.FormatAVIF},
		{"jpeg-to-heif", processing.FormatHEIF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := processing.NewProcessingPlan(
				processing.OpResize, processing.FormatJPEG, tc.format,
				processing.Size{Original: true}, 1, 0, nil, 0, 0,
			)
			if err != nil {
				t.Fatalf("NewProcessingPlan: %v", err)
			}

			b := makeJpegBackend(t)
			res, err := b.process(context.Background(), src, plan, false, nil, nil)
			if err != nil {
				t.Fatalf("process: %v", err)
			}
			if vips.HeifHasSequence(res.data) {
				t.Errorf("%s: static source produced sequence track", tc.name)
			}
			img, err := vips.LoadImageFromBuffer(res.data, vips.NewImportParams())
			if err != nil {
				t.Fatalf("%s: load: %v", tc.name, err)
			}
			defer img.Close()
			if img.Pages() != 1 {
				t.Errorf("%s: pages = %d, want 1", tc.name, img.Pages())
			}
		})
	}
}

// TestMultipageWithoutDelayNotAnimated — Pages()>1 при отсутствии delay
// НЕ считается анимацией: isAnimatedImage возвращает false, изображение
// уходит в static path.
func TestMultipageWithoutDelayNotAnimated(t *testing.T) {
	// Собираем 2-страничный вертикальный стек из статичных JPEG
	// (источник без delay-поля): 32x64, page-height 32, pages 2.
	img, err := vips.LoadImageFromBuffer(makeJpeg(t), vips.NewImportParams())
	if err != nil {
		t.Fatalf("jpeg load: %v", err)
	}
	defer img.Close()

	second, err := img.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	defer second.Close()
	if err := img.ArrayJoin([]*vips.ImageRef{second}, 1); err != nil {
		t.Fatalf("arrayjoin: %v", err)
	}
	if err := img.SetPageHeight(32); err != nil {
		t.Fatalf("set page height: %v", err)
	}
	if err := img.SetPages(2); err != nil {
		t.Fatalf("set pages: %v", err)
	}

	if img.Pages() != 2 {
		t.Fatalf("precondition: pages = %d, want 2", img.Pages())
	}
	if isAnimatedImage(img) {
		t.Error("isAnimatedImage: multipage without delay treated as animated")
	}

	// Контроль: GIF с delay распознаётся как анимация.
	anim, err := vips.LoadImageFromBuffer(makeGif(t), func() *vips.ImportParams {
		p := vips.NewImportParams()
		p.NumPages.Set(-1)
		return p
	}())
	if err != nil {
		t.Fatalf("gif load: %v", err)
	}
	defer anim.Close()
	if !isAnimatedImage(anim) {
		t.Error("isAnimatedImage: animated GIF with delay not detected")
	}
}

// makeJpeg собирает сплошной красный JPEG 32×32.
func makeJpeg(t *testing.T) []byte {
	t.Helper()
	img, err := vips.LoadImageFromBuffer(makePng(t), vips.NewImportParams())
	if err != nil {
		t.Fatalf("png load: %v", err)
	}
	defer img.Close()
	data, _, err := img.ExportJpeg(vips.NewJpegExportParams())
	if err != nil {
		t.Fatalf("jpeg export: %v", err)
	}
	return data
}

// TestAvifBenchmarkStaticVsAnimated — sanity-benchmark: static AVIF идёт через
// libvips heifsave (без sequence track), animated AVIF — через libheif
// sequence encoder. Тест не ставит жёстких порогов по времени (CI-машины
// разные), но проверяет, что:
//   - static path НЕ создаёт sequence track (animated-ветка не задевает hot path);
//   - animated path создаёт настоящий sequence track;
//   - оба пути завершаются без ошибок.
func TestAvifBenchmarkStaticVsAnimated(t *testing.T) {
	b := makeJpegBackend(t)

	// Static: JPEG → AVIF (libvips heifsave).
	planS, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatJPEG, processing.FormatAVIF,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan(static): %v", err)
	}
	startS := time.Now()
	resS, err := b.process(context.Background(), makeJpeg(t), planS, false, nil, nil)
	elapsedS := time.Now().Sub(startS)
	if err != nil {
		t.Fatalf("static avif process: %v", err)
	}
	if vips.HeifHasSequence(resS.data) {
		t.Error("static AVIF produced sequence track (animated branch leaked into static path)")
	}

	// Animated: GIF → AVIF (libheif sequence encoder).
	planA, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatGIF, processing.FormatAVIF,
		processing.Size{Original: true}, 1, 0, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan(animated): %v", err)
	}
	startA := time.Now()
	resA, err := b.process(context.Background(), makeGif(t), planA, false, nil, nil)
	elapsedA := time.Now().Sub(startA)
	if err != nil {
		t.Fatalf("animated avif process: %v", err)
	}
	if !vips.HeifHasSequence(resA.data) {
		t.Error("animated AVIF produced no sequence track")
	}

	t.Logf("bench: static avif (heifsave) = %v ms, animated avif (sequence) = %v ms, sizes %d/%d bytes",
		elapsedS.Milliseconds(), elapsedA.Milliseconds(), len(resS.data), len(resA.data))
}
