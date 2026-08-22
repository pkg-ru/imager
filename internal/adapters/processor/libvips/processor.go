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
// processor). Используется как primary-движок в routing.Processor; APNG
// не поддерживается libvips — такой вызов возвращает ошибку, и роутинг
// переключается на ImageMagick fallback.
package libvips

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pkg-ru/imager/internal/application/ports/processor"
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

// IsLimitError сообщает, является ли err типизированной ошибкой лимита
// libvips (в том числе обёрнутой).
func IsLimitError(err error, kind LimitKind) bool {
	var le *LimitError
	if !errors.As(err, &le) {
		return false
	}
	return le.Kind == kind
}

// Limits — resource limits для libvips-обработчика.
//
// Значение 0 поля означает "без лимита"/"умолчание govips" (см. поля ниже).
// Лимиты применяются двумя слоями:
//  1. глобальная конфигурация libvips (ConcurrencyLevel, MaxCacheMem/
//     MaxCacheFiles/MaxCacheSize) — задаётся ОДИН раз на процесс при Startup;
//  2. application-level ограничения: bounded writer (OutputBytes) на выход
//     и context deadline (Timeout) на каждый запрос Process.
type Limits struct {
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
}

// backend — реализация обработки изображения (build-tag specific):
//   - libvipsBackend (process_libvips.go, tag "libvips") — реальный govips;
//   - stubBackend (process_stub.go, tag "!libvips") — заглушка с ошибкой.
//
// Интерфейс держит "живой" код адаптера свободным от cgo.
type backend interface {
	// process выполняет загрузку, обработку по плану и экспорт. Возвращает
	// байты результата. Ошибки контекста/лимитов — через ctx и LimitError.
	process(ctx context.Context, data []byte, plan *processing.ProcessingPlan) ([]byte, error)
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
	sem       *semaphore
	backend   backend
	closeOnce sync.Once
}

var _ processor.Processor = (*Processor)(nil)

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
		sem:     newSemaphore(conc, conc),
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
	if err := p.sem.acquire(ctx); err != nil {
		return nil, err
	}
	defer p.sem.release()

	// Application-level context deadline (не полагаемся только на libvips).
	runCtx := ctx
	cancel := func() {}
	if p.limits.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.limits.Timeout)
	}
	defer cancel()

	// Чтение исходника в буфер (libvips работает с []byte).
	data, err := io.ReadAll(in.Source)
	if err != nil {
		return nil, fmt.Errorf("libvips: read source: %w", err)
	}

	// Обработка (govips или заглушка).
	output, err := p.backend.process(runCtx, data, in.Plan)
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
	bw := &boundedWriter{w: out, max: p.limits.OutputBytes, cancel: cancel}
	_, err = bw.Write(output)
	if err != nil {
		return nil, err
	}
	exceeded, actual := bw.exceededN()
	if exceeded {
		return nil, &LimitError{Kind: LimitOutput, Limit: p.limits.OutputBytes, Actual: actual}
	}
	return &processor.Result{Size: actual}, nil
}

// semaphore — bounded очередь слотов конкурентности (аналогично
// imagemagick/processor.go): ограничивает и число активных, и число
// ожидающих. При переполнении очереди ожидания — быстрый отказ
// (ErrTooManyConcurrency).
type semaphore struct {
	mu      sync.Mutex
	slots   chan struct{}
	waiting int
	maxWait int
}

func newSemaphore(max, maxWait int) *semaphore {
	if max <= 0 {
		max = 1
	}
	return &semaphore{slots: make(chan struct{}, max), maxWait: maxWait}
}

// acquire занимает слот. Блокируется до освобождения или отмены ctx.
// Возвращает ErrTooManyConcurrency, если очередь ожидания переполнена.
func (s *semaphore) acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.waiting >= s.maxWait {
		s.mu.Unlock()
		return ErrTooManyConcurrency
	}
	s.waiting++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}()
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release освобождает слот.
func (s *semaphore) release() {
	select {
	case <-s.slots:
	default:
	}
}

// boundedWriter ограничивает запись max байт. При превышении лимита помечает
// exceeded, отменяет ctx и возвращает LimitError. Это application-level
// защита, не полагающаяся на внутренние лимиты libvips.
//
// Потокобезопасен: Write/n могут вызываться из разных goroutines.
type boundedWriter struct {
	mu       sync.Mutex
	w        io.Writer
	max      int64
	n        int64
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.n+int64(len(p)) > b.max {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
		return 0, &LimitError{Kind: LimitOutput, Limit: b.max, Actual: b.n + int64(len(p))}
	}
	n, err := b.w.Write(p)
	b.n += int64(n)
	return n, err
}

// exceededN возвращает флаг превышения и фактический размер (потокобезопасно).
func (b *boundedWriter) exceededN() (bool, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded, b.n
}
