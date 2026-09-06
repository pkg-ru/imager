// Package ffmpeg implements the videoframe.Extractor port using the external
// ffmpeg and ffprobe binaries. Frames are extracted to stdout as JPEG via a
// pipe, so no temporary files are created and the video is never loaded into
// memory as a whole.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"gitverse.ru/pkg-ru/imager/ports/videoframe"
)

// defaultFPS — частота кадров по умолчанию, если ffprobe не смог её
// определить.
const defaultFPS = 25

// pathProvider — опциональный интерфейс источника, позволяющий получить
// путь к файлу на диске. Если источник реализует его, ffmpeg читает файл
// напрямую по пути (быстрее, без копирования в stdin). Иначе источник
// передаётся в ffmpeg через stdin (pipe:0).
type pathProvider interface {
	Path() string
}

// Extractor — реализация videoframe.Extractor через внешние бинарники
// ffmpeg и ffprobe.
type Extractor struct {
	ffmpegPath  string
	ffprobePath string
}

// New создаёт Extractor с указанными путями к бинарникам ffmpeg и ffprobe.
func New(ffmpegPath, ffprobePath string) *Extractor {
	return &Extractor{ffmpegPath: ffmpegPath, ffprobePath: ffprobePath}
}

// NewDefault создаёт Extractor с путями по умолчанию ("ffmpeg"/"ffprobe"),
// которые ищутся в PATH.
func NewDefault() *Extractor {
	return New("ffmpeg", "ffprobe")
}

// Extract извлекает кадр из видео-источника. См. videoframe.Extractor.
func (e *Extractor) Extract(ctx context.Context, source io.ReadSeeker, opts videoframe.Options) (*videoframe.Result, error) {
	if source == nil {
		return nil, errors.New("videoframe: source is nil")
	}

	// Определяем длительность и fps через ffprobe.
	info, err := e.probe(ctx, source)
	if err != nil {
		return nil, err
	}

	// Целевая секунда первого кадра.
	t := targetSecond(info.Duration, opts.FramePercent)

	// Перебор кадров вперёд при неудачной проверке контрастности.
	var last *videoframe.Result
	attempts := opts.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	for i := int64(0); i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		res, err := e.extractFrame(ctx, source, t)
		if err != nil {
			return nil, err
		}
		res.Width = info.Width
		res.Height = info.Height
		last = res

		// Проверка контрастности.
		contrast, cerr := contrastOf(res.Frame)
		if cerr != nil {
			// Не удалось декодировать кадр — считаем неудачным и идём дальше.
			contrast = 0
		}
		if contrast >= opts.MinContrast {
			return res, nil
		}

		// Следующий кадр вперёд.
		t = nextSecond(t, opts.FrameStep, info.FPS)
	}

	// Ни один кадр не прошёл проверку — возвращаем последний извлечённый.
	return last, nil
}

// rewindToStart перематывает источник в начало перед передачей во внешний
// процесс через stdin. Это необходимо, потому что ffprobe читает начало
// потока (заголовок контейнера), и если после него тот же reader без
// перемотки передать в ffmpeg, ffmpeg получит данные без заголовка и
// упадёт с "Invalid data found when processing input" на pipe:0.
// Для источников-файлов (pathProvider) перемотка не требуется, но и
// безвредна.
func rewindToStart(source io.ReadSeeker) error {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("videoframe: seek source to start: %w", err)
	}
	return nil
}

// probe определяет длительность, fps и размеры видео через ffprobe.
func (e *Extractor) probe(ctx context.Context, source io.ReadSeeker) (probeInfo, error) {
	// Гарантируем чтение с начала: вызывающий мог частично прочитать поток.
	if err := rewindToStart(source); err != nil {
		return probeInfo{}, err
	}
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=duration,r_frame_rate,width,height",
		"-show_entries", "format=duration",
		"-of", "json",
	}

	cmd := exec.CommandContext(ctx, e.ffprobePath, args...)
	cmd.Stderr = &bytes.Buffer{}

	// Передача источника: по пути, если доступен, иначе через stdin.
	if p, ok := source.(pathProvider); ok && p.Path() != "" {
		cmd.Args = append(cmd.Args, p.Path())
	} else {
		cmd.Args = append(cmd.Args, "pipe:0")
		cmd.Stdin = source
	}

	out, err := cmd.Output()
	if err != nil {
		return probeInfo{}, fmt.Errorf("ffprobe failed: %w: %s", err, cmd.Stderr)
	}
	return parseProbeJSON(out)
}

// extractFrame извлекает один кадр (JPEG) на секунде t через ffmpeg.
func (e *Extractor) extractFrame(ctx context.Context, source io.ReadSeeker, t float64) (*videoframe.Result, error) {
	// Если источник — файл на диске, используем его путь напрямую.
	// Иначе пишем источник в stdin ffmpeg через pipe:0.
	input := "pipe:0"
	if p, ok := source.(pathProvider); ok && p.Path() != "" {
		input = p.Path()
	} else if err := rewindToStart(source); err != nil {
		// Корневая причина бага: ffprobe уже прочитал начало потока, и без
		// перемотки ffmpeg получает данные без заголовка контейнера —
		// "Error opening input file pipe:0: Invalid data found".
		return nil, err
	}

	// -threads 2 ограничивает число декодер/энкодер-потоков ffmpeg: при
	// извлечении кадра из 4K HEVC 10-bit `-threads auto` порождает ~16
	// frame-threads с большим DPB, что вместе с cgroup-лимитом памяти
	// приводит к OOM-kill контейнера.
	args := []string{
		"-ss", formatSeconds(t),
		"-i", input,
		"-threads", "2",
		"-frames:v", "1",
		"-q:v", "2",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-", // вывод JPEG в stdout (pipe)
	}

	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	cmd.Stderr = &bytes.Buffer{}
	if input == "pipe:0" {
		cmd.Stdin = source
	}

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg extract failed: %w: %s", err, cmd.Stderr)
	}
	if len(out) == 0 {
		return nil, errors.New("ffmpeg produced no frame")
	}

	return &videoframe.Result{Frame: out, Timestamp: t}, nil
}

// formatSeconds форматирует секунды для аргумента -ss.
func formatSeconds(t float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", t), "0"), ".")
}
