//go:build libvips

// Интеграционный тест защитного rewind источника в Process (processor.go):
// источник, уже потреблённый до EOF (например, PrepareRGB для app-level
// детекции в ensureDetections), должен быть перемотан перед чтением, иначе
// загрузка получает пустой буфер → "libvips: load: unsupported image format"
// (регрессия "первый запрос на новый ассет").
package libvips

import (
	"bytes"
	"context"
	"image/color"
	"io"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// TestProcessRewindsConsumedSource проверяет, что Process перематывает
// источник, уже прочитанный до EOF, и успешно обрабатывает изображение.
func TestProcessRewindsConsumedSource(t *testing.T) {
	data := makeSolidPng(t, 64, 32, color.RGBA{0, 255, 0, 255})

	// Имитация первого прохода (PrepareRGB): источник прочитан полностью
	// и оставлен на EOF — ровно то, что случалось в ensureDetections до
	// фикса rewind в PrepareRGB.
	src := bytes.NewReader(data)
	if _, err := io.Copy(io.Discard, src); err != nil {
		t.Fatalf("consume source: %v", err)
	}
	if src.Len() != 0 {
		t.Fatalf("source not consumed: remaining = %d", src.Len())
	}

	p, err := New(Options{Limits: Limits{Concurrency: 1}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Не вызываем p.Close(): в libvips-сборке он делает vips.Shutdown(),
	// после которого govips нельзя перезапустить — это сломало бы остальные
	// тесты пакета (см. "govips cannot be stopped and restarted").

	var out bytes.Buffer
	res, err := p.Process(context.Background(), processor.Input{
		Source: src,
		Plan:   mustPlan(t, processing.FormatPNG, processing.FormatPNG),
	}, &out)
	if err != nil {
		t.Fatalf("Process on EOF-consumed source: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("empty output")
	}
	// Резолв плана — OpResize 100x100, из 64x32 должен получиться валидный
	// PNG (не пустой буфер).
	w, h := decodePngSize(t, out.Bytes())
	if w == 0 || h == 0 {
		t.Fatalf("decoded output size = %dx%d, want non-zero", w, h)
	}
	if res.Size != int64(out.Len()) {
		t.Errorf("res.Size = %d, want %d", res.Size, out.Len())
	}
}
