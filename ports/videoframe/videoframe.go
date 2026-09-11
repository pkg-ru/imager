// Package videoframe defines an abstract port of the video frame extractor.
// Implementations (ffmpeg, mock) do not depend on HTTP, the file system, or a
// specific engine. The port is used to produce a single preview frame (JPEG)
// from a video source.
package videoframe

import (
	"context"
	"io"
)

// Options — параметры извлечения кадра.
type Options struct {
	// FramePercent — процент от длительности видео (0-100), на котором
	// берётся первый кадр.
	FramePercent int64
	// MinContrast — минимальная контрастность кадра (0-1). Кадр с
	// контрастностью ниже порога считается неудачным (например, чёрный
	// экран) и пропускается.
	MinContrast float64
	// FrameStep — шаг вперёд в кадрах при неудачной проверке контрастности.
	FrameStep int64
	// Attempts — число попыток извлечения кадра (включая первую).
	Attempts int64
}

// Result — результат извлечения кадра.
type Result struct {
	// Frame — JPEG-данные кадра.
	Frame []byte
	// Width — ширина кадра в пикселях.
	Width int
	// Height — высота кадра в пикселях.
	Height int
	// Timestamp — секунды, на которых взят кадр.
	Timestamp float64
}

// Extractor извлекает кадр из видео.
type Extractor interface {
	// Extract извлекает кадр из видео-источника.
	// source — читаемый источник видео (io.ReadSeeker).
	// Возвращает кадр, прошедший проверку контрастности, либо последний
	// проверенный кадр, если ни один не прошёл.
	Extract(ctx context.Context, source io.ReadSeeker, opts Options) (*Result, error)
}
