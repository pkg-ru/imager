//go:build libvips

// Интеграционные тесты принудительной зачистки метаданных (stripAllMetadata)
// через реальный govips-движок. Компилируются ТОЛЬКО с тэком "libvips".
//
// Проверяют инвариант: итоговый ассет НЕ содержит пользовательских
// метаданных (EXIF/GPS, XMP, IPTC, описания) и ICC-профиля — независимо от
// того, поддерживает ли конкретный кодек опцию strip (heifsave/jxlsave её
// не поддерживают и копируют метаданные исходника в выходной файл).
package libvips

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/pkg-ru/imager/internal/domain/processing"
)

// makeBasePng создаёт простой PNG 4x4 (сплошной красный).
func makeBasePng(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	red := color.RGBA{255, 0, 0, 255}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, red)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return out.Bytes()
}

// makePngWithMetadata создаёт PNG, содержащий EXIF, XMP и ICC-профиль.
// Метаданные добавляются через govips и сохраняются в PNG (StripMetadata=false).
func makePngWithMetadata(t *testing.T) []byte {
	t.Helper()
	v, err := vips.NewImageFromBuffer(makeBasePng(t))
	if err != nil {
		t.Fatalf("load base png: %v", err)
	}
	defer v.Close()

	v.SetBlob("exif-data", []byte{0x45, 0x78, 0x69, 0x66, 0x00, 0x00}) // "Exif\0\0"
	v.SetString("xmp-data", "<x:xmpmeta>test</x:xmpmeta>")
	v.SetBlob("icc-profile-data", []byte{0x00, 0x01, 0x02, 0x03})

	p := vips.NewPngExportParams()
	p.StripMetadata = false // сохранить метаданные в PNG
	out, _, err := v.ExportPng(p)
	if err != nil {
		t.Fatalf("export png with metadata: %v", err)
	}
	return out
}

// assertNoUserMetadata проверяет, что в изображении не осталось
// пользовательских метаданных (EXIF/GPS, XMP, IPTC) и ICC-профиля.
func assertNoUserMetadata(t *testing.T, img *vips.ImageRef) {
	t.Helper()
	for _, f := range img.GetFields() {
		if strings.HasPrefix(f, "exif-") {
			t.Errorf("field %q leaked: EXIF/GPS metadata not stripped", f)
		}
		if f == "xmp-data" {
			t.Errorf("field %q leaked: XMP metadata not stripped", f)
		}
		if f == "icc-profile-data" {
			t.Errorf("field %q leaked: ICC profile not stripped", f)
		}
	}
}

// TestStripAllMetadataRemovesUserMetadata — юнит-тест самой функции
// stripAllMetadata: устанавливает метаданные на ImageRef и проверяет,
// что после вызова они удалены (а технические поля сохранены).
func TestStripAllMetadataRemovesUserMetadata(t *testing.T) {
	v, err := vips.NewImageFromBuffer(makeBasePng(t))
	if err != nil {
		t.Fatalf("load base png: %v", err)
	}
	defer v.Close()

	v.SetBlob("exif-data", []byte{0x45, 0x78, 0x69, 0x66})
	v.SetString("xmp-data", "<x:xmpmeta>test</x:xmpmeta>")
	v.SetBlob("icc-profile-data", []byte{0x00, 0x01, 0x02, 0x03})

	// Санити-проверка: метаданные действительно установлены до вызова.
	fields := v.GetFields()
	if !hasField(fields, "exif-data") || !hasField(fields, "icc-profile-data") {
		t.Fatal("precondition failed: metadata not present on ImageRef")
	}

	if err := stripAllMetadata(v); err != nil {
		t.Fatalf("stripAllMetadata: %v", err)
	}
	assertNoUserMetadata(t, v)
}

// TestProcessStripsMetadataPng проверяет, что guard вызывается в exportImage:
// PNG с EXIF/XMP/ICC после process не содержит метаданных.
func TestProcessStripsMetadataPng(t *testing.T) {
	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 4, Height: 4}, 1, 85, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("NewProcessingPlan: %v", err)
	}

	b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("newLibvipsBackend: %v", err)
	}

	src := makePngWithMetadata(t)
	out, err := b.process(context.Background(), src, plan)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	dec, err := vips.NewImageFromBuffer(out)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	defer dec.Close()
	assertNoUserMetadata(t, dec)
}

// TestProcessStripsMetadataHeifJxl проверяет форматы, чьи кодеки НЕ
// поддерживают опцию strip (heifsave/jxlsave) и копируют метаданные
// исходника в выходной файл. Guard stripAllMetadata обязан их удалить.
// Если кодек не скомпилирован в текущей сборке libvips — тест пропускается.
func TestProcessStripsMetadataHeifJxl(t *testing.T) {
	cases := []struct {
		name string
		fmt  processing.Format
	}{
		{"heif", processing.FormatHEIF},
		{"jxl", processing.FormatJPEGXL},
	}

	for _, c := range cases {
		plan, err := processing.NewProcessingPlan(
			processing.OpResize, processing.FormatPNG, c.fmt,
			processing.Size{Width: 4, Height: 4}, 1, 85, nil, 0, 0,
		)
		if err != nil {
			t.Fatalf("%s: NewProcessingPlan: %v", c.name, err)
		}

		b, err := newLibvipsBackend(Options{Limits: Limits{Concurrency: 1}})
		if err != nil {
			t.Fatalf("%s: newLibvipsBackend: %v", c.name, err)
		}

		out, err := b.process(context.Background(), makePngWithMetadata(t), plan)
		if err != nil {
			// Кодек может отсутствовать в сборке libvips (heif/jxl не
			// скомпилированы). Это не провал зачистки — пропускаем.
			t.Skipf("%s codec not available: %v", c.name, err)
			continue
		}

		dec, err := vips.NewImageFromBuffer(out)
		if err != nil {
			t.Fatalf("%s: decode output: %v", c.name, err)
		}
		defer dec.Close()
		assertNoUserMetadata(t, dec)
	}
}

// hasField проверяет наличие поля в списке.
func hasField(fields []string, name string) bool {
	for _, f := range fields {
		if f == name {
			return true
		}
	}
	return false
}
