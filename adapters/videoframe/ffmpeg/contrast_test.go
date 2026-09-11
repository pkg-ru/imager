package ffmpeg

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// encodeJPEG кодирует изображение в JPEG для тестов.
func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

// solidImage создаёт однотонное изображение заданного цвета.
func solidImage(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// contrastImage создаёт изображение с чёрными и белыми пикселями
// (максимальная контрастность).
func contrastImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x+y)%2 == 0 {
				img.Set(x, y, color.White)
			} else {
				img.Set(x, y, color.Black)
			}
		}
	}
	return img
}

func TestContrastOf(t *testing.T) {
	t.Run("solid image has near-zero contrast", func(t *testing.T) {
		data := encodeJPEG(t, solidImage(64, 64, color.Gray{Y: 128}))
		c, err := contrastOf(data)
		if err != nil {
			t.Fatalf("contrastOf: %v", err)
		}
		if c > 0.05 {
			t.Fatalf("solid image contrast = %v, want near 0", c)
		}
	})

	t.Run("checkerboard has high contrast", func(t *testing.T) {
		data := encodeJPEG(t, contrastImage(64, 64))
		c, err := contrastOf(data)
		if err != nil {
			t.Fatalf("contrastOf: %v", err)
		}
		if c < 0.4 {
			t.Fatalf("checkerboard contrast = %v, want >= 0.4", c)
		}
	})

	t.Run("invalid data returns error", func(t *testing.T) {
		if _, err := contrastOf([]byte("not a jpeg")); err == nil {
			t.Fatal("expected error for invalid jpeg data")
		}
	})
}
