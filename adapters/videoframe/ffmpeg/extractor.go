// Package ffmpeg implements the videoframe.Extractor port using the external
// ffmpeg and ffprobe binaries. Frames are extracted to stdout as JPEG via a
// pipe, so no temporary files are created and the video is never loaded into
// memory as a whole.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
//
// Все попытки (перебор кадров вперёд при неудачной проверке контрастности)
// выполняются ОДНИМ процессом ffmpeg: поток декодируется один раз, кадры на
// целевых позициях выбираются фильтром select по номеру кадра. Это устраняет
// N-1 повторных запусков ffmpeg и повторных seek/decode (раньше каждая
// попытка была отдельным процессом, а для pipe-источников — ещё и повторным
// чтением потока с начала). Как только очередной кадр проходит проверку
// контрастности, процесс останавливается досрочно — ранний выход и порядок
// попыток сохранены.
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

	// Перебор кадров вперёд при неудачной проверке контрастности: позиции
	// попыток известны заранее (t, t+step/fps, ...), поэтому все кадры
	// выбираются одним процессом.
	attempts := opts.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	times := make([]float64, attempts)
	times[0] = t
	for i := 1; i < len(times); i++ {
		times[i] = nextSecond(times[i-1], opts.FrameStep, info.FPS)
	}

	var last *videoframe.Result
	err = e.extractFrames(ctx, source, times, opts.FrameStep, func(res *videoframe.Result) bool {
		res.Width = info.Width
		res.Height = info.Height
		last = res

		// Проверка контрастности; true — кадр подошёл, остановить извлечение.
		contrast, cerr := contrastOf(res.Frame)
		if cerr != nil {
			// Не удалось декодировать кадр — считаем неудачным и идём дальше.
			contrast = 0
		}
		return contrast >= opts.MinContrast
	})
	if err != nil {
		return nil, err
	}
	if last == nil {
		return nil, errors.New("ffmpeg produced no frame")
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

// extractFrames извлекает до len(times) кадров (JPEG) ОДНИМ процессом
// ffmpeg: первый — на секунде times[0], далее с шагом step кадров
// (times[i] вычислены заранее через nextSecond). Для каждого извлечённого
// кадра вызывается fn; если fn вернула true, извлечение останавливается —
// процесс завершается досрочно (кадры после подошедшего не декодируются).
//
// Две ветки ввода:
//   - path: источник — файл на диске (pathProvider). ffmpeg открывает файл
//     сам; input seek `-ss <times[0]>` перед `-i <path>` перематывает по
//     контейнеру (без декодирования до точки seek) — основной выигрыш против
//     pipe. Перемотка rewindToStart не нужна: ffmpeg открывает файл заново
//     с начала.
//   - pipe: источник — stdin (pipe:0). Перемотка rewindToStart выполняется
//     ОДИН раз перед единственным запуском ffmpeg (см. rewindToStart):
//     процесс читает поток последовательно от точки seek вперёд, seek назад
//     невозможен и не нужен — именно поэтому пакетная обработка попыток
//     одним процессом для pipe-источников не только возможна, но и выгодна.
//
// Кадры читаются из stdout по одному (см. nextJPEG) и не буферизуются все
// сразу: память ограничена одним кадром независимо от числа попыток.
func (e *Extractor) extractFrames(ctx context.Context, source io.ReadSeeker, times []float64, step int64, fn func(*videoframe.Result) bool) error {
	input := inputPath(source)
	if input == "" {
		if err := rewindToStart(source); err != nil {
			// Корневая причина бага: ffprobe уже прочитал начало потока, и
			// без перемотки ffmpeg получает данные без заголовка
			// контейнера — "Error opening input file pipe:0: Invalid data
			// found". Для path-ветки перемотка не выполняется.
			return err
		}
		input = "pipe:0"
	}

	// Аргументы идентичны для обеих веток: input seek `-ss <t0>` перед `-i`
	// быстр по контейнеру для файла и по максимальному байтовому смещению
	// для pipe; см. batchFrameArgs.
	args := batchFrameArgs(input, times[0], int64(len(times)), step)

	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	cmd.Stderr = &bytes.Buffer{}
	if input == "pipe:0" {
		cmd.Stdin = source
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start failed: %w", err)
	}

	// stop завершает процесс досрочно (кадр подошёл или контекст отменён).
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	// Чтение кадров из stdout: в image2pipe JPEG-кадры идут подряд,
	// разбираем их по маркерам SOI/EOI по одному.
	br := bufio.NewReader(stdout)
	delivered := 0
	for {
		if err := ctx.Err(); err != nil {
			stop()
			return err
		}

		frame, rerr := nextJPEG(br)
		if rerr != nil {
			// EOF (поток закончился) или ошибка чтения — обработка ниже,
			// после Wait.
			break
		}

		res := &videoframe.Result{Frame: frame, Timestamp: times[delivered]}
		delivered++
		if fn(res) {
			// Кадр подошёл — досрочно останавливаем ffmpeg.
			stop()
			return nil
		}
	}

	waitErr := cmd.Wait()
	if delivered == 0 {
		if waitErr != nil {
			return fmt.Errorf("ffmpeg extract failed: %w: %s", waitErr, cmd.Stderr)
		}
		return errors.New("ffmpeg produced no frame")
	}
	// Кадры были доставлены; ошибка завершения процесса после них (обрыв
	// потока, досрочный kill) на результат не влияет.
	return nil
}

// batchFrameArgs формирует аргументы ffmpeg для извлечения нескольких
// кадров (JPEG) одним процессом: первый кадр — на секунде t0, далее каждый
// step-й кадр, всего до attempts штук. Используется для обеих веток (path и
// pipe) — отличается только значение -i (путь файла либо pipe:0).
//
//   - `-ss <t0>` перед `-i` — input seek: для файла демуксер перематывает по
//     контейнеру (индекс/ключевые кадры) без декодирования всей прокрутки;
//     точность кадра сохраняется (ffmpeg декодирует до целевого PTS).
//     Значение достаточно точное, т.к. ffmpeg после input seek делает
//     аккуратный доводчик до требуемого кадра.
//   - `-threads 2` ограничивает число декодер/энкодер-потоков: при
//     извлечении кадра из 4K HEVC 10-bit `-threads auto` порождает ~16
//     frame-threads с большим DPB, что вместе с cgroup-лимитом памяти
//     приводит к OOM-kill контейнера. Процесс теперь один, поэтому пик
//     памяти декодера не выше, чем у одного прежнего одиночного процесса
//     (параллельных процессов нет).
//   - `-vf select='...'` выбирает кадры с номерами 0, step, 2*step, ...
//     (номер n считается фильтром от первого кадра после seek). Выбор по
//     номеру кадра вместо времени не зависит от точности таймстампов и даёт
//     те же позиции, что прежние последовательные попытки с шагом
//     step/fps секунд. select стоит перед scale, чтобы не масштабировать
//     отброшенные кадры.
//   - `-vf scale='min(1920,iw)':-2` уменьшает кадр до 1920 по ширине (шире —
//     ужимается, уже/равно — не масштабируется вверх), высота считается
//     пропорционально с выравниванием на чётность (-2). Аргументы передаются
//     через exec.Command напрямую (без shell): запятая в значении фильтра
//     экранируется одинарными кавычками фильтрграфа, фильтр — один аргумент
//     argv.
//   - `-noaccurate_seek` и `-skip_frame nokey` намеренно НЕ добавляются:
//     они ускоряют seek, но жертвуют точностью кадра.
func batchFrameArgs(input string, t0 float64, attempts, step int64) []string {
	return []string{
		"-ss", formatSeconds(t0),
		"-i", input,
		"-threads", "2",
		"-vf", "select='" + selectExpr(attempts, step) + "',scale='min(1920,iw)':-2",
		"-frames:v", strconv.FormatInt(attempts, 10),
		"-q:v", "2",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-", // вывод JPEG в stdout (pipe)
	}
}

// selectExpr формирует выражение фильтра select для выбора кадров с
// номерами 0, step, 2*step, ... (всего attempts штук). Номер кадра n
// считается от первого декодированного после seek кадра (с нуля): попытка i
// прежней последовательной версии извлекала кадр на t0 + i*step/fps секунд —
// это тот же (i*step)-й кадр после t0. Целочисленное выражение не зависит
// от накопления ошибок плавающей точки в таймстампах.
func selectExpr(attempts, step int64) string {
	if attempts < 1 {
		attempts = 1
	}
	if step < 0 {
		step = 0
	}
	parts := make([]string, 0, attempts)
	for i := int64(0); i < attempts; i++ {
		parts = append(parts, fmt.Sprintf("eq(n,%d)", i*step))
	}
	return strings.Join(parts, "+")
}

// nextJPEG читает из br следующий JPEG-кадр из потока image2pipe (кадры
// идут подряд: SOI ... EOI SOI ... EOI). Поиск ведётся по маркерам:
// SOI (0xFF 0xD8) — начало кадра, EOI (0xFF 0xD9) — конец. В энтропийно
// кодированных данных JPEG байт 0xFF всегда сопровождается вставкой 0x00
// (byte stuffing), поэтому пара 0xFF 0xD9 внутри сжатых данных кадра не
// встречается. Байты до первого SOI пропускаются. Возвращает ошибку
// (в т.ч. io.EOF), если кадр не найден до конца потока.
func nextJPEG(br *bufio.Reader) ([]byte, error) {
	for {
		// Поиск SOI.
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 0xFF {
			continue
		}
		b, err = br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 0xD8 {
			continue
		}

		// SOI найден — читаем до EOI.
		frame := []byte{0xFF, 0xD8}
		prev := byte(0)
		for {
			b, err := br.ReadByte()
			if err != nil {
				return nil, err
			}
			frame = append(frame, b)
			if prev == 0xFF && b == 0xD9 {
				return frame, nil
			}
			prev = b
		}
	}
}

// formatSeconds форматирует секунды для аргумента -ss.
func formatSeconds(t float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", t), "0"), ".")
}
