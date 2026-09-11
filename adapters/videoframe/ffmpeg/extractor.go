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

// inputPath возвращает файловый путь источника для ffmpeg/ffprobe либо ""
// (stdin pipe:0), если источник не реализует pathProvider или файловый путь
// недоступен (in-RAM буфер).
func inputPath(source io.ReadSeeker) string {
	if p, ok := source.(pathProvider); ok {
		return p.Path()
	}
	return ""
}

// probe определяет длительность, fps и размеры видео через ffprobe.
func (e *Extractor) probe(ctx context.Context, source io.ReadSeeker) (probeInfo, error) {
	// Передача источника: по пути, если доступен, иначе через stdin.
	// Для stdin-ветки гарантируем чтение с начала: ffprobe читает заголовок
	// контейнера, и без перемотки получит поток без него.
	input := inputPath(source)
	if input == "" {
		if err := rewindToStart(source); err != nil {
			return probeInfo{}, err
		}
		input = "pipe:0"
	}

	cmd := exec.CommandContext(ctx, e.ffprobePath, probeArgs(input)...)
	cmd.Stderr = &bytes.Buffer{}
	if input == "pipe:0" {
		cmd.Stdin = source
	}

	out, err := cmd.Output()
	if err != nil {
		return probeInfo{}, fmt.Errorf("ffprobe failed: %w: %s", err, cmd.Stderr)
	}
	return parseProbeJSON(out)
}

// probeArgs формирует аргументы ffprobe.
//
// -probesize/-analyzeduration ограничивают анализ контейнера/потоков: по
// умолчанию ffmpeg может анализировать до 5 секунд (analyzeduration) и до
// первого ключевого кадра в глубину, что на длинных видео заметно замедляет
// запуск. 5M достаточно для надёжного определения duration/fps/размеров,
// при этом анализ заканчивается раньше. Применяется к обеим веткам (path и
// pipe), потому что ограничивает именно работу демуксера.
func probeArgs(input string) []string {
	args := []string{
		"-v", "error",
		"-probesize", "5M",
		"-analyzeduration", "5M",
		"-select_streams", "v:0",
		"-show_entries", "stream=duration,r_frame_rate,width,height",
		"-show_entries", "format=duration",
		"-of", "json",
	}
	if input != "pipe:0" {
		args = append(args, input)
	} else {
		args = append(args, "pipe:0")
	}
	return args
}

// extractFrame извлекает один кадр (JPEG) на секунде t через ffmpeg.
// Две ветки:
//   - path: источник — файл на диске (pathProvider). ffmpeg открывает файл
//     сам; input seek `-ss <t>` перед `-i <path>` перематывает по контейнеру
//     (без декодирования до точки seek) — основной выигрыш против pipe.
//     Перемотка rewindToStart не нужна: каждый запуск ffmpeg открывает файл
//     заново с начала.
//   - pipe: источник — stdin (pipe:0). Перед КАЖДОЙ попыткой выполняется
//     rewindToStart (см. rewindToStart), т.к. ffprobe уже прочитал начало
//     потока.
func (e *Extractor) extractFrame(ctx context.Context, source io.ReadSeeker, t float64) (*videoframe.Result, error) {
	input := inputPath(source)
	if input == "" {
		if err := rewindToStart(source); err != nil {
			// Корневая причина бага: ffprobe уже прочитал начало потока, и
			// без перемотки ffmpeg получает данные без заголовка
			// контейнера — "Error opening input file pipe:0: Invalid data
			// found". Для path-ветки перемотка не выполняется.
			return nil, err
		}
		input = "pipe:0"
	}

	// Аргументы идентичны для обеих веток: input seek `-ss <t>` перед `-i`
	// быстр по контейнеру для файла и по максимальному байтовому смещению
	// для pipe; см. frameArgs.
	args := frameArgs(input, t)

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

// frameArgs формирует аргументы ffmpeg для извлечения одного кадра (JPEG).
// Используется для обеих веток (path и pipe) — отличается только значение
// -i (путь файла либо pipe:0).
//
//   - `-ss <t>` перед `-i` — input seek: для файла демуксер перематывает по
//     контейнеру (индекс/ключевые кадры) без декодирования всей прокрутки;
//     точность кадра сохраняется (ffmpeg декодирует до целевого PTS).
//     Значение достаточно точное, т.к. ffmpeg после input seek делает
//     аккуратный доводчик до требуемого кадра.
//   - `-threads 2` ограничивает число декодер/энкодер-потоков: при
//     извлечении кадра из 4K HEVC 10-bit `-threads auto` порождает ~16
//     frame-threads с большим DPB, что вместе с cgroup-лимитом памяти
//     приводит к OOM-kill контейнера.
//   - `-vf scale='min(1920,iw)':-2` уменьшает кадр до 1920 по ширине (шире —
//     ужимается, уже/равно — не масштабируется вверх), высота считается
//     пропорционально с выравниванием на чётность (-2). Аргументы передаются
//     через exec.Command напрямую (без shell): запятая в значении фильтра
//     не требует экранирования, фильтр — один аргумент argv.
//   - `-noaccurate_seek` и `-skip_frame nokey` намеренно НЕ добавляются:
//     они ускоряют seek, но жертвуют точностью кадра.
func frameArgs(input string, t float64) []string {
	return []string{
		"-ss", formatSeconds(t),
		"-i", input,
		"-threads", "2",
		"-vf", "scale='min(1920,iw)':-2",
		"-frames:v", "1",
		"-q:v", "2",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-", // вывод JPEG в stdout (pipe)
	}
}

// formatSeconds форматирует секунды для аргумента -ss.
func formatSeconds(t float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", t), "0"), ".")
}
