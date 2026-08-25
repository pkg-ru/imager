// Package libvips реализует in-process обработку изображений через libvips
// (cgo-биндинг github.com/davidbyttow/govips/v2).
//
// Пакет изолирован от cgo: общий код (semaphore, boundedWriter, Limits,
// LimitError, Processor, Process) не импортирует govips и собирается без
// установленного libvips. Реальная обработка живёт в process_libvips.go
// (build tag "libvips"); без тэга используется заглушка process_stub.go,
// возвращающая понятную ошибку об отсутствии поддержки.
//
// Адаптер реализует порт processor.Processor (internal/application/ports/
// processor). Используется как primary-движок в routing.Processor. libvips
// (≥ 8.13) поддерживает все форматы, включая APNG (чтение и запись как
// multi-page PNG).
package libvips

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/processor/detection"
	"github.com/pkg-ru/imager/internal/adapters/processor/shared"
	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/domain/filemeta"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

// ErrTooManyConcurrency — сигнал переполнения очереди ожидания слота (bounded
// семофор). Возвращается при превышении лимита ожидающих запросов.
var ErrTooManyConcurrency = errors.New("libvips: too many concurrent requests waiting for a slot")

// LimitKind — тип ресурсного лимита.
type LimitKind string

const (
	// LimitTime — лимит времени (таймаут) обработки.
	LimitTime LimitKind = "time"
	// LimitOutput — лимит размера выходных данных (bytes).
	LimitOutput LimitKind = "output"
)

// LimiterError — типизированная ошибка превышения лимита.
type LimitError struct {
	// Kind — тип превышенного лимита.
	Kind LimitKind
	// Limit — значение лимита.
	Limit int64
	// Actual — фактическое значение.
	Actual int64
	// Err — исходная ошибка (может быть nil).
	Err error
}

// Error реализует error.
func (e *LimitError) Error() string {
	msg := fmt.Sprintf("libvips: %s limit exceeded (limit=%d actual=%d)", e.Kind, e.Limit, e.Actual)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap возвращает исходную ошибку.
func (e *LimitError) Unwrap() error { return e.Err }

// Limits — resource limits для libvips-обработчика.
//
// Значение 0 поля означает "без лимита"/"умолчание govips" (см. поля ниже).
// Лимиты применяются двумя слоями:
//  1. глобальная конфигурация libvips (ConcurrencyLevel, MaxCacheMem/
//     MaxCacheFiles/MaxCacheSize) — задаётся ОДИН раз на процесс при Startup;
//  2. application-level ограничения: bounded writer (OutputBytes) на выход
//     и context deadline (Timeout) на каждый запрос Process.
type Limits struct {
	// SourceBytes — application-level лимит размера ВХОДНЫХ данных в байтах.
	// Вход читается с ограничением; при превышении возвращается
	// LimitError{LimitOutput} (kind переиспользуется для единообразия
	// маппинга). 0 = дефолт DefaultSourceBytes.
	SourceBytes int64
	// OutputBytes — application-level лимит размера выходных данных в байтах.
	// При превышении запись в out прекращается и возвращается
	// LimitError{LimitOutput}.
	OutputBytes int64
	// Timeout — application-level context deadline на одну операцию Process.
	// При превышении возвращается LimitError{LimitTime}.
	Timeout time.Duration
	// Concurrency — максимальное число одновременно выполняющихся операций
	// обработки (bounded очередь слотов). 0 = default (16).
	Concurrency int
	// Threads — число потоков libvips (vips_concurrency_set). 0 = default
	// govps (1) — для многопроцессных движений лучше задать число CPU.
	Threads int
	// MaxCacheMem — максимум памяти кэша libvips в байтах.
	MaxCacheMem int
	// MaxCacheFiles — максимум файлов кэша libvips.
	MaxCacheFiles int
	// MaxCacheSize — максимум операций в кэше libvips.
	MaxCacheSize int
}

// Options — параметры создания Processor.
type Options struct {
	// Limits — resource limits обработчика.
	Limits Limits
	// Detector — детектор лиц/объектов для операций face-crop/object-crop.
	// nil = детекция недоступна: запросы с fc/oc вернут понятную ошибку.
	// Детектор создаётся в composition root (cmd/imager/main.go) из секции
	// конфигурации detection.* и может быть nil при пустых путях к моделям.
	// Для smart-crop НЕ требуется: он использует встроенное "attention"
	// libvips (InterestingAttention).
	Detector detection.Detector
	// DetectorMargin — отступ к найденной детектором области face/object-crop
	// как доля от её размера (интервал [0,1]); применяется на обе стороны
	// по каждой оси. 0 = кроп строго по bounding box обнаруженного объекта.
	DetectorMargin float64
}

// backendResult — результат обработки движка: байты + размеры выхода и
// входа (для заполнения processor.Result; 0 = неизвестно).
type backendResult struct {
	data         []byte
	width        int
	height       int
	sourceWidth  int
	sourceHeight int
}

// backend — реализация обработки изображения (build-tag specific):
//   - libvips (process_libvips.go, tag "libvips") — реальный govips;
//   - stubBackend (process_stub.go, tag "!libvips") — заглушка с ошибкой.
//
// Интерфейс держит "живой" код адаптера свободным от cgo.
type backend interface {
	// process выполняет загрузку, обработку по плану и экспорт. Возвращает
	// байты результата и размеры. Ошибки контекста/лимитов — через ctx и
	// LimitError. detectionsReady/boxes — готовые боксы детекции из
	// sidecar-кэша: при true процессор
	// НЕ вызывает ИИ-модель, а использует переданные боксы (в координатах
	// оригинала).
	process(ctx context.Context, data []byte, plan *processing.ProcessingPlan, detectionsReady bool, boxes []filemeta.PixelBox) (*backendResult, error)
	// prepareRGB извлекает RGB-пиксели источника в размерах ОРИГИНАЛА
	// (без trim) для детекции на уровне приложения (ensureDetections).
	// Возвращает nil, если движок не поддерживает подготовку RGB.
	prepareRGB(ctx context.Context, data []byte) (*processor.RGBFrame, error)
	// close освобождает ресурсы движка (например, vips.Shutdown).
	// Идемпотентен.
	close() error
}

// newBackend создаёт движок обработки для данных build-этов (см.
// process_libvips.go / process_stub.go).
var newBackend func(opts Options) (backend, error)

// Processor — in-process libvips-процессор, реализующий processor.Processor.
//
// Экземпляр владеет bounded-семофором конкурентности и применяет
// application-level лимиты (OutputBytes/Timeout). Startup libvips выполняется
// ОДИН раз на процесс (sync.Once в process_libvips.go).
type Processor struct {
	limits    Limits
	sem       *shared.Semaphore
	backend   backend
	closeOnce sync.Once
}

var _ processor.Processor = (*Processor)(nil)
var _ processor.RGBPreparer = (*Processor)(nil)

// DefaultSourceBytes — дефолтный лимит размера ВХОДНЫХ данных (10 MiB).
// Применяется, если SourceBytes == 0. Защита от OOM при чтении
// неограниченного входа (К3): для remote-источников размер может быть
// неизвестен (Metadata.Size == 0), поэтому io.ReadAll без лимита недопустим.
const DefaultSourceBytes int64 = 10 * 1024 * 1024

// ErrNotCompiled — ошибка, которую возвращает stub-движок, если пакет
// собран без тэка "libvips".
var ErrNotCompiled = errors.New("libvips support not compiled in (build with -tags libvips)")

// New создаёт Processor. Запускает libvips (startup) при наличии движка.
// Возвращает ошибку только при некорректной конфигурации; отсутствие
// скомпилированной поддержки libvps не является ошибкой здесь — ошибка
// возвращается при первом Process.
func New(opts Options) (*Processor, error) {
	conc := opts.Limits.Concurrency
	if conc <= 0 {
		conc = 16
	}
	bk, err := newBackend(opts)
	if err != nil {
		return nil, fmt.Errorf("libvips: new backend: %w", err)
	}
	return &Processor{
		limits:  opts.Limits,
		sem:     shared.NewSemaphore(conc, 0, ErrTooManyConcurrency),
		backend: bk,
	}, nil
}

// Close освобождает ресурсы движка. Идемпотентен.
func (p *Processor) Close() error {
	p.closeOnce.Do(func() {
		_ = p.backend.close()
	})
	return nil
}

// Process обрабатывает изображение согласно плану и записывает результат в
// out.
//
// Гарантии:
//   - bounded очередь слотов конкурентности: при переполнении очереди
//     ожидания — быстрый отказ ErrTooManyConcurrency;
//   - context deadline (Timeout) применяется к libvps-обработке и маппится в
//     LimitError{LimitTime};
//   - OutputBytes применяется через boundedWriter при записи в out и маппится
//     в LimitError{LimitOutput};
//   - исходник читается полностью в память (io.ReadAll в in.Source).
func (p *Processor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	if ctx == nil {
		return nil, fmt.Errorf("libvips: nil context")
	}
	if in.Source == nil {
		return nil, fmt.Errorf("libvips: nil source")
	}
	if in.Plan == nil {
		return nil, fmt.Errorf("libvips: nil plan")
	}

	// Ожидание слота конкурентности с bounded очередью. При переполнении
	// очереди — быстрый отказ, а не бесконечное ожидание.
	if err := p.sem.Acquire(ctx); err != nil {
		return nil, err
	}
	defer p.sem.Release()

	// Application-level context deadline (не полагаемся только на libvips).
	runCtx := ctx
	cancel := func() {}
	if p.limits.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.limits.Timeout)
	}
	defer cancel()

	// Чтение исходника в буфер с ограничением размера (К3): вход не должен
	// материализоваться без границ даже для remote-источников с неизвестным
	// размером. Лимит — SourceBytes или дефолт DefaultSourceBytes.
	sourceLimit := p.limits.SourceBytes
	if sourceLimit <= 0 {
		sourceLimit = DefaultSourceBytes
	}
	data, err := io.ReadAll(io.LimitReader(in.Source, sourceLimit))
	if err != nil {
		return nil, fmt.Errorf("libvips: read source: %w", err)
	}
	if int64(len(data)) >= sourceLimit {
		return nil, &LimitError{Kind: LimitOutput, Limit: sourceLimit, Actual: int64(len(data))}
	}

	// Обработка (govips или заглушка). К2: watchdog-обёртка — зависшая
	// cgo-операция не прерывается по ctx, но сервис не блокируется:
	// по истечении контекста возвращается ошибка, а слот семафора
	// освобождается (defer p.sem.release() выше).
	br, err := runWatchdog(runCtx, func() (*backendResult, error) {
		return p.backend.process(runCtx, data, in.Plan, in.DetectionsReady, in.Boxes)
	})
	if err != nil {
		if runCtx.Err() != nil {
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return nil, &LimitError{
					Kind:  LimitTime,
					Limit: int64(p.limits.Timeout.Seconds()),
					Err:   runCtx.Err(),
				}
			}
			return nil, runCtx.Err()
		}
		return nil, err
	}

	// Запись результата через bounded writer: OutputBytes → LimitOutput.
	bw := shared.NewBoundedWriter(out, p.limits.OutputBytes, cancel)
	_, _ = bw.Write(br.data)
	exceeded, actual := bw.ExceededN()
	if exceeded {
		return nil, &LimitError{Kind: LimitOutput, Limit: p.limits.OutputBytes, Actual: actual}
	}
	if err != nil && !errors.Is(err, shared.ErrOutputLimitExceeded) {
		return nil, err
	}
	return &processor.Result{
		Size:         actual,
		Width:        br.width,
		Height:       br.height,
		SourceWidth:  br.sourceWidth,
		SourceHeight: br.sourceHeight,
	}, nil
}

// PrepareRGB извлекает RGB-пиксели источника в размерах ОРИГИНАЛА (без
// trim) для детекции на уровне приложения (ensureDetections). Реализует
// processor.RGBPreparer; отсутствие поддержки движком → nil (деградация).
func (p *Processor) PrepareRGB(ctx context.Context, src io.ReadSeeker) (*processor.RGBFrame, error) {
	if ctx == nil {
		return nil, fmt.Errorf("libvips: nil context")
	}
	if src == nil {
		return nil, fmt.Errorf("libvips: nil source")
	}
	sourceLimit := p.limits.SourceBytes
	if sourceLimit <= 0 {
		sourceLimit = DefaultSourceBytes
	}
	data, err := io.ReadAll(io.LimitReader(src, sourceLimit))
	if err != nil {
		return nil, fmt.Errorf("libvips: read source: %w", err)
	}
	if int64(len(data)) >= sourceLimit {
		return nil, &LimitError{Kind: LimitOutput, Limit: sourceLimit, Actual: int64(len(data))}
	}
	return p.backend.prepareRGB(ctx, data)
}

// runWatchdog выполняет тяжёлую (cgo) операцию в отдельной горутине и
// ожидает её завершения либо отмены ctx (К2). Сама cgo-операция не может
// быть прервана libvips-биндингом, но сервис не блокируется: по ctx.Done()
// возвращается ошибка контекста, запрос получает 504, а слот семафора
// освобождается вызывающим (defer p.sem.release()).
//
// Важно: завершившаяся по таймауту горутина остаётся висеть в cgo до
// фактического завершения операции libvips. Это допустимый компромисс:
// сервис не блокирует пул воркеров и не теряет семафор.
func runWatchdog[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	done := make(chan watchdogResult[T], 1)
	go func() {
		out, err := fn()
		done <- watchdogResult[T]{out: out, err: err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// watchdogResult — результат watchdog-вызова.
type watchdogResult[T any] struct {
	out T
	err error
}
