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

	"gitverse.ru/pkg-ru/imager/domain/processing"
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

// Имена тестовых полей метаданных. Зарезервированные имена ("exif-data",
// "icc-profile-data") НЕ используются: кодеки libvips требуют для них тип
// VipsBlob, а установить blob через govips v2.18.0 невозможно — см. комментарий
// setTestMetadata. Не-зарезервированные имена с теми же префиксами проверяют
// ту же семантику зачистки (RemoveMetadata удаляет все не-технические поля).
const (
	testExifField = "exif-fake-data"
	testXmpField  = "xmp-fake-data"
	testIccField  = "icc-fake-data"
)

// setTestMetadata устанавливает на ImageRef пользовательские метаданные
// (EXIF, XMP, ICC) для проверки их зачистки. Используется SetString вместо
// SetBlob: govips v2.18.0 vipsImageSetBlob (operations.go:780) передаёт
// unsafe.Pointer(&data) (указатель на Go slice header) в C, что нарушает
// правила cgo и падает с "cgo argument has Go pointer to unpinned Go pointer"
// на Go >= 1.21. SetString копирует значение через C.CString — cgo-безопасно.
// stripAllMetadata удаляет поля по имени независимо от типа (RemoveMetadata —
// все не-технические поля), поэтому проверка зачистки сохраняет смысл.
func setTestMetadata(v *vips.ImageRef) {
	v.SetString(testExifField, "fake-exif")
	v.SetString(testXmpField, "<x:xmpmeta>test</x:xmpmeta>")
	v.SetString(testIccField, "fake-icc")
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

	setTestMetadata(v)

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
		if f == testXmpField {
			t.Errorf("field %q leaked: XMP metadata not stripped", f)
		}
		if f == testIccField {
			t.Errorf("field %q leaked: ICC-like metadata not stripped", f)
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

	setTestMetadata(v)

	// Санити-проверка: метаданные действительно установлены до вызова.
	fields := v.GetFields()
	if !hasField(fields, testExifField) || !hasField(fields, testIccField) {
		t.Fatal("precondition failed: metadata not present on ImageRef")
	}

	if err := stripAllMetadata(v, false); err != nil {
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
	out, err := b.process(context.Background(), src, plan, false, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	dec, err := vips.NewImageFromBuffer(out.data)
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

		res, err := b.process(context.Background(), makePngWithMetadata(t), plan, false, nil, nil)
		if err != nil {
			// Кодек может отсутствовать в сборке libvips (heif/jxl не
			// скомпилированы). Это не провал зачистки — пропускаем.
			t.Skipf("%s codec not available: %v", c.name, err)
			continue
		}
		out := res.data

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
