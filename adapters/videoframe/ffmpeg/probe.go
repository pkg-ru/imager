package ffmpeg

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// probeInfo — результат парсинга вывода ffprobe (JSON).
type probeInfo struct {
	// Duration — длительность видео в секундах.
	Duration float64
	// FPS — частота кадров видео.
	FPS float64
	// Width — ширина видео в пикселях.
	Width int
	// Height — высота видео в пикселях.
	Height int
}

// ffprobeJSON — структура вывода ffprobe `-of json`.
type ffprobeJSON struct {
	Streams []struct {
		Duration   string `json:"duration"`
		RFrameRate string `json:"r_frame_rate"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// parseProbeJSON разбирает JSON-вывод ffprobe и возвращает параметры видео.
// Чистая функция, тестируется без реального ffmpeg.
// Если длительность недоступна в потоке, используется длительность формата.
// Если fps недоступен, возвращается дефолт 25.
func parseProbeJSON(data []byte) (probeInfo, error) {
	var out ffprobeJSON
	if err := json.Unmarshal(data, &out); err != nil {
		return probeInfo{}, fmt.Errorf("parse ffprobe json: %w", err)
	}

	info := probeInfo{Width: out.Streams[0].Width, Height: out.Streams[0].Height}

	// Длительность: сначала из потока, затем из формата.
	if len(out.Streams) > 0 && out.Streams[0].Duration != "" {
		if d, err := strconv.ParseFloat(out.Streams[0].Duration, 64); err == nil {
			info.Duration = d
		}
	}
	if info.Duration == 0 && out.Format.Duration != "" {
		if d, err := strconv.ParseFloat(out.Format.Duration, 64); err == nil {
			info.Duration = d
		}
	}

	// FPS: r_frame_rate вида "num/den".
	if len(out.Streams) > 0 && out.Streams[0].RFrameRate != "" {
		if fps, ok := parseFrameRate(out.Streams[0].RFrameRate); ok {
			info.FPS = fps
		}
	}
	if info.FPS == 0 {
		info.FPS = 25
	}

	return info, nil
}

// parseFrameRate разбирает строку частоты кадров вида "30000/1001" или "25".
// Возвращает fps и признак успеха.
func parseFrameRate(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	parts := strings.SplitN(s, "/", 2)
	num, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, false
	}
	if len(parts) == 1 {
		return num, num > 0
	}
	den, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || den == 0 {
		return 0, false
	}
	return num / den, num/den > 0
}
