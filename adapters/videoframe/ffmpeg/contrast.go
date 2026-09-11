package ffmpeg

import (
	"bytes"
	"image"
	_ "image/jpeg" // регистрация декодера JPEG
	"math"
)

// contrastOf декодирует JPEG-данные и вычисляет нормализованную
// контрастность (0..1) как стандартное отклонение яркости (luma),
// делённое на 255. Чистая функция, тестируется без реального ffmpeg.
// Возвращает ошибку, если данные не являются декодируемым изображением.
func contrastOf(jpegData []byte) (float64, error) {
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return 0, err
	}
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= 0 || h <= 0 {
		return 0, nil
	}

	// Сумма и сумма квадратов яркости для вычисления stddev за один проход.
	var sum, sumSq float64
	var n float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA() возвращает 16-битные значения; приводим к 0..255.
			luma := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
			sum += luma
			sumSq += luma * luma
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	mean := sum / n
	variance := sumSq/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	stddev := math.Sqrt(variance)
	return stddev / 255, nil
}
