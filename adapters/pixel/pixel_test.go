package pixel

import (
	"context"
	"testing"
)

// TestGenerator_AllFormats проверяет, что для всех поддерживаемых
// форматов есть встроенный пиксель и он непустой.
func TestGenerator_AllFormats(t *testing.T) {
	gen := New()
	formats := []string{"png", "apng", "webp", "gif", "avif", "heif", "jpeg", "jpg"}
	for _, f := range formats {
		b, err := gen.GeneratePixel(context.Background(), f)
		if err != nil {
			t.Errorf("GeneratePixel(%q): %v", f, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("GeneratePixel(%q): empty bytes", f)
		}
	}
}

// TestGenerator_UnknownFormat проверяет, что неизвестный формат
// возвращает ошибку (вызывающий перейдёт к следующему not-found fallback).
func TestGenerator_UnknownFormat(t *testing.T) {
	gen := New()
	if _, err := gen.GeneratePixel(context.Background(), "bmp"); err == nil {
		t.Fatal("GeneratePixel(bmp): want error")
	}
	if _, err := gen.GeneratePixel(context.Background(), ""); err == nil {
		t.Fatal("GeneratePixel(empty): want error")
	}
}

// TestGenerator_CaseInsensitive проверяет регистронезависимость.
func TestGenerator_CaseInsensitive(t *testing.T) {
	gen := New()
	lower, err := gen.GeneratePixel(context.Background(), "png")
	if err != nil {
		t.Fatalf("GeneratePixel(png): %v", err)
	}
	upper, err := gen.GeneratePixel(context.Background(), "PNG")
	if err != nil {
		t.Fatalf("GeneratePixel(PNG): %v", err)
	}
	if string(lower) != string(upper) {
		t.Fatal("case-insensitive mismatch")
	}
}
