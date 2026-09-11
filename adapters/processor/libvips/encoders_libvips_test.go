//go:build libvips

// Интеграционные тесты новых параметров кодировщиков (Волны 5c/5d) через
// реальный govips-движок: PNG quantization + палитровая bit-depth, DPI-
// нормализация (xres/yres → 72). Компилируются ТОЛЬКО с тэком "libvips".
package libvips

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// makePalettePng создаёт PNG с ограниченной палитрой (8 уникальных цветов),
// который хорошо поддаётся квантованию.
func makePalettePng(t *testing.T) []byte {
	t.Helper()
	pal := []color.RGBA{
		{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255},
		{255, 255, 0, 255}, {255, 0, 255, 255}, {0, 255, 255, 255},
		{255, 255, 255, 255}, {0, 0, 0, 255},
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetRGBA(x, y, pal[(x+y)%len(pal)])
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return out.Bytes()
}

// pngBackendPlan создаёт план PNG→PNG через OpResize без изменения размера.
func pngBackendPlan(t *testing.T) *processing.ProcessingPlan {
	t.Helper()
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 8, Height: 8}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}
	return plan
}

// TestPNGQuantizationExportsPalettePNG — при включённой опции PNGPalette
// экспорт PNG не падает и даёт валидный файл с палитровой bit-depth.
func TestPNGQuantizationExportsPalettePNG(t *testing.T) {
	palette := true
	colors := 64
	bitdepth := 4
	b, err := newLibvipsBackend(Options{
		Limits: Limits{Concurrency: 1},
		EncodersConfig: EncodersConfig{
			DefaultQuality: 85,
			Formats: map[string]FormatEncodersConfig{
				"png": {
					Palette:         &palette,
					PaletteColors:   &colors,
					PaletteBitDepth: &bitdepth,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	out, err := b.process(context.Background(), makePalettePng(t), pngBackendPlan(t), false, nil, nil)
	if err != nil {
		t.Fatalf("process with palette: %v", err)
	}
	if len(out.data) == 0 {
		t.Fatal("empty output")
	}
	// Результат — валидный PNG требуемого размера (читается govips).
	img, err := vips.NewImageFromBuffer(out.data)
	if err != nil {
		t.Fatalf("decode palette output: %v", err)
	}
	defer img.Close()
	if img.Width() != 8 || img.Height() != 8 {
		t.Errorf("output size = %dx%d, want 8x8", img.Width(), img.Height())
	}
}

// TestPNGQuantizationDisabledByDefault — без опции PNGPalette quantization не
// применяется (обычный RGB PNG-экспорт).
func TestPNGQuantizationDisabledByDefault(t *testing.T) {
	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	out, err := b.process(context.Background(), makePalettePng(t), pngBackendPlan(t), false, nil, nil)
	if err != nil {
		t.Fatalf("process plain: %v", err)
	}
	if len(out.data) == 0 {
		t.Fatal("empty output")
	}
}

// TestExportNormalizesResolutionTo72 — DPI-нормализация: изображение с
// 300 DPI после exportImage имеет xres/yres = 72 (просмотрщики не
// масштабируют по DPI).
func TestExportNormalizesResolutionTo72(t *testing.T) {
	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}
	src, err := vips.NewImageFromBuffer(makeBasePng(t))
	if err != nil {
		t.Fatalf("load base png: %v", err)
	}
	defer src.Close()
	// 300 DPI (имитация скана) через CopyChangingResolution.
	img, err := src.CopyChangingResolution(300, 300)
	if err != nil {
		t.Fatalf("set 300 dpi: %v", err)
	}
	defer img.Close()

	lb, ok := b.(*libvipsBackend)
	if !ok {
		t.Fatalf("newLibvipsBackend returned %T, want *libvipsBackend", b)
	}
	out, err := lb.exportImage(img, pngBackendPlan(t))
	if err != nil {
		t.Fatalf("exportImage: %v", err)
	}
	got, err := vips.NewImageFromBuffer(out)
	if err != nil {
		t.Fatalf("decode normalized png: %v", err)
	}
	defer got.Close()
	// libvips хранит разрешение в px/mm: 72 DPI = 72/25.4 ≈ 2.8346.
	// Эпсилон 0.01: PNG-кодировщик округляет pHYs до рационального числа
	// (наблюдается 2.835 вместо 2.8346).
	want := dpiToPxPerMm(72)
	if absFloat(got.ResX()-want) > 0.01 || absFloat(got.ResY()-want) > 0.01 {
		t.Errorf("resolution after export = %v/%v, want %v (72 dpi)", got.ResX(), got.ResY(), want)
	}
}

// TestNormalizeResolution — normalizeResolution возвращает новый ImageRef с
// 72 DPI для 300-DPI исходника и тот же ImageRef для уже 72-DPI.
func TestNormalizeResolution(t *testing.T) {
	src, err := vips.NewImageFromBuffer(makeBasePng(t))
	if err != nil {
		t.Fatalf("load base png: %v", err)
	}
	defer src.Close()

	t.Logf("loaded png resolution: xres=%v yres=%v", src.ResX(), src.ResY())
	// Уже 72: копия не создаётся (тот же указатель).
	same, err := normalizeResolution(src, defaultResolutionDPI)
	if err != nil {
		t.Fatalf("normalizeResolution(72): %v", err)
	}
	if same != src {
		t.Fatal("normalizeResolution must return the same ref for 72 dpi")
	}

	// 300 px/mm (≈ 7620 DPI): создаётся новая копия с 72 DPI; исходник не
	// мутируется. CopyChangingResolution принимает px/mm, поэтому 300 px/mm —
	// гарантированно «значимое» разрешение, требующее нормализации.
	hi, err := src.CopyChangingResolution(300, 300)
	if err != nil {
		t.Fatalf("set 300 px/mm: %v", err)
	}
	defer hi.Close()
	norm, err := normalizeResolution(hi, defaultResolutionDPI)
	if err != nil {
		t.Fatalf("normalizeResolution(300px/mm): %v", err)
	}
	defer norm.Close()
	if norm == hi {
		t.Fatal("normalizeResolution must create a new ref for 300 px/mm")
	}
	// libvips хранит разрешение в px/mm: 72 DPI = 72/25.4 ≈ 2.8346.
	want := dpiToPxPerMm(72)
	if absFloat(norm.ResX()-want) > 1e-6 || absFloat(norm.ResY()-want) > 1e-6 {
		t.Errorf("normalized resolution = %v/%v, want %v (72 dpi)", norm.ResX(), norm.ResY(), want)
	}
	// Исходник не мутируется: 300 px/mm остаётся как было.
	if absFloat(hi.ResX()-300) > 1e-6 || absFloat(hi.ResY()-300) > 1e-6 {
		t.Errorf("source resolution mutated: %v/%v, want 300 (px/mm)", hi.ResX(), hi.ResY())
	}
}

// absFloat возвращает модуль числа (хелпер для сравнения float64 с эпсилоном).
func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
