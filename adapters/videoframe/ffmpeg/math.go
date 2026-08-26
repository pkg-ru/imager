package ffmpeg

// targetSecond вычисляет целевую секунду из процента от длительности.
// framePercent ограничивается диапазоном [0, 100], длительность — не
// отрицательна. Чистая функция, тестируется без реального ffmpeg.
func targetSecond(duration float64, framePercent int64) float64 {
	if duration < 0 {
		duration = 0
	}
	if framePercent < 0 {
		framePercent = 0
	}
	if framePercent > 100 {
		framePercent = 100
	}
	return duration * float64(framePercent) / 100
}

// nextSecond вычисляет секунду следующего кадра при шаге вперёд на
// frameStep кадров с частотой fps. fps <= 0 трактуется как дефолт 25.
// Чистая функция, тестируется без реального ffmpeg.
func nextSecond(t float64, frameStep int64, fps float64) float64 {
	if fps <= 0 {
		fps = 25
	}
	if frameStep < 0 {
		frameStep = 0
	}
	return t + float64(frameStep)/fps
}
