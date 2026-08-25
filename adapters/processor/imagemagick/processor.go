package imagemagick

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/processor/shared"
	"github.com/pkg-ru/imager/internal/application/ports/processor"
)

// runner абстрагирует запуск subprocess для тестируемости (dependency
// injection). В production используется execRunner.
type runner interface {
	// run запускает binary с args, stdin из in, stdout в out, stderr в err.
	// Возвращает ошибку запуска/ожидания. Отмена ctx должна завершать
	// процесс.
	run(ctx context.Context, binary string, args []string, env []string, in io.Reader, out io.Writer, err io.Writer) error
}

// execRunner — production runner поверх os/exec.
type execRunner struct{}

func (execRunner) run(ctx context.Context, binary string, args []string, env []string, in io.Reader, out io.Writer, err io.Writer) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = err
	return cmd.Run()
}

// Options — параметры создания Processor.
type Options struct {
	// Binary — путь к ImageMagick binary (по умолчанию "magick").
	Binary string
	// Capabilities — предварительно полученный снимок capabilities. Если nil,
	// снимок обнаруживается при создании (lazy, не глобально).
	Capabilities *Capabilities
	// Limits — resource limits для subprocess.
	Limits Limits
	// Policy — настройки deny-by-default policy.xml.
	Policy PolicyConfig
	// StderrLimit — максимальный размер накапливаемого stderr в байтах
	// (по умолчанию 64 KiB).
	StderrLimit int
	// DetectCapabilities — true, если нужно обнаружить capabilities при
	// создании (запуск binary). По умолчанию true.
	DetectCapabilities bool
	// runner — внутренний runner (для тестов). Если nil, используется
	// execRunner.
	runner runner
}

// Processor — instance-scoped ImageMagick processor, реализующий
// processor.Processor. Каждый экземпляр владеет собственным immutable
// снимком capabilities и (опционально) policy.xml — без глобального
// sync.Once.
type Processor struct {
	binary     string
	caps       *Capabilities
	limits     Limits
	policy     PolicyConfig
	stderrN    int
	runner     runner
	policyDir  string
	policyOnce sync.Once
	policyErr  error
	// sem — bounded очередь слотов конкурентности. Вместо простого канала
	// используем очередь с лимитом ожидающих; при переполнении —
	// быстрый отказ (ErrTooManyConcurrency).
	sem *shared.Semaphore
	// baseEnv — кэшированное базовое окружение: os.Environ() + модульные пути
	// binary. Инвариантно на экземпляр; policyDir добавляется
	// отдельно при каждом запуске.
	baseEnv []string
	// metrics — опциональные метрики ожидания слота и запуска.
	metrics *procMetrics
	closed  bool
	closeMu sync.Mutex
}

var _ processor.Processor = (*Processor)(nil)

// ErrTooManyConcurrency — сигнал переполнения очереди ожидания слота.
var ErrTooManyConcurrency = errors.New("imagemagick: too many concurrent requests waiting for a slot")

// New создаёт Processor. Если DetectCapabilities включён, обнаруживает
// capabilities (binary identity/version/formats) при создании. Ошибка
// обнаружения возвращается и не кэшируется глобально.
func New(opts Options) (*Processor, error) {
	binary := opts.Binary
	if binary == "" {
		binary = "magick"
	}
	stderrN := opts.StderrLimit
	if stderrN <= 0 {
		stderrN = 64 * 1024
	}
	r := opts.runner
	if r == nil {
		r = execRunner{}
	}
	p := &Processor{
		binary:  binary,
		limits:  opts.Limits,
		policy:  opts.Policy,
		stderrN: stderrN,
		runner:  r,
		metrics: newProcMetrics(),
	}
	// Bounded очередь слотов: лимит ожидающих = concurrency (или 16, если
	// concurrency не задан). При переполнении — быстрый отказ.
	conc := opts.Limits.Concurrency
	if conc <= 0 {
		conc = 16
	}
	p.sem = shared.NewSemaphore(conc, 0, ErrTooManyConcurrency)
	// Кэшируем базовое окружение один раз.
	p.baseEnv = envForBinary(binary, nil)
	if opts.Capabilities != nil {
		p.caps = opts.Capabilities
	} else if opts.DetectCapabilities {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		caps, err := detectCapabilities(ctx, binary)
		if err != nil {
			return nil, err
		}
		p.caps = caps
	}
	return p, nil
}

// Capabilities возвращает immutable снимок capabilities экземпляра.
func (p *Processor) Capabilities() *Capabilities { return p.caps }

// Binary возвращает путь к бинарю.
func (p *Processor) Binary() string { return p.binary }

// Close освобождает ресурсы: удаляет временный каталог policy.xml.
// Идемпотентен.
func (p *Processor) Close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.policyDir != "" {
		// Удаляем только временный каталог, созданный нами (не пользовательский
		// Dir из конфигурации).
		if p.policy.Dir == "" {
			_ = os.RemoveAll(p.policyDir)
		}
		p.policyDir = ""
	}
	return nil
}

// ensurePolicy записывает policy.xml один раз на экземпляр (lazy).
func (p *Processor) ensurePolicy() (string, error) {
	p.policyOnce.Do(func() {
		p.policyDir, p.policyErr = writePolicyXML(p.policy)
	})
	return p.policyDir, p.policyErr
}

// Process читает исходник из in.Source, применяет план in.Plan и записывает
// результат в out.
//
// Гарантии:
//   - argv строится без shell из allowlisted операций/форматов;
//   - stdout стримится в out через bounded writer (application-level лимит);
//   - stderr накапливается с ограничением размера;
//   - context cancellation завершает subprocess (exec.CommandContext) и не
//     оставляет goroutines;
//   - resource limits применяются через -limit и policy.xml.
func (p *Processor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	if ctx == nil {
		return nil, fmt.Errorf("imagemagick: nil context")
	}
	if in.Source == nil {
		return nil, fmt.Errorf("imagemagick: nil source")
	}
	if in.Plan == nil {
		return nil, fmt.Errorf("imagemagick: nil plan")
	}

	// Ожидание слота конкурентности с bounded очередью. При переполнении
	// очереди — быстрый отказ (ErrTooManyConcurrency), а не бесконечное
	// ожидание. Метрика времени ожидания слота.
	slotStart := time.Now()
	if err := p.sem.Acquire(ctx); err != nil {
		return nil, err
	}
	defer p.sem.Release()
	p.metrics.observeSlotWait(time.Since(slotStart))

	args, err := buildArgv(in.Plan, p.caps, p.limits)
	if err != nil {
		return nil, err
	}

	// Application-level context deadline (не полагаемся только на policy).
	runCtx := ctx
	cancel := func() {}
	if p.limits.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.limits.Timeout)
	}
	defer cancel()

	// Окружение subprocess: базовое окружение кэшировано, добавляем только
	// policyDir в MAGICK_CONFIGURE_PATH.
	var env []string
	policyDir := ""
	if dir, err := p.ensurePolicy(); err != nil {
		return nil, err
	} else if dir != "" {
		policyDir = dir
	}
	env = envWithPolicyDir(p.baseEnv, policyDir)

	// Streaming stdout в out через bounded writer.
	bw := shared.NewBoundedWriter(out, p.limits.OutputBytes, cancel)

	// stderr с ограничением размера.
	stderr := &limitedBuffer{max: p.stderrN}

	// Запуск и ожидание. exec.CommandContext убивает процесс при отмене ctx.
	runStart := time.Now()
	err = p.runner.run(runCtx, p.binary, args, env, in.Source, bw, stderr)
	p.metrics.observeRun(time.Since(runStart))

	// C3: проверяем превышение OutputBytes ПЕРВЫМ (до runCtx.Err()), т.к.
	// boundedWriter при превышении отменяет контекст, и runCtx.Err() =
	// context.Canceled маскирует LimitOutput.
	exceeded, actual := bw.ExceededN()
	if exceeded {
		return nil, &LimitError{Kind: LimitOutput, Limit: p.limits.OutputBytes, Actual: actual}
	}
	if err != nil {
		if runCtx.Err() != nil {
			// Отмена контекста или превышение таймаута.
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return nil, &LimitError{Kind: LimitTime, Limit: int64(p.limits.Timeout.Seconds()), Err: runCtx.Err()}
			}
			return nil, runCtx.Err()
		}
		return nil, fmt.Errorf("imagemagick: command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return &processor.Result{Size: actual}, nil
}

// envWithPolicyDir добавляет policyDir в MAGICK_CONFIGURE_PATH базового
// окружения (без повторного os.Environ()).
func envWithPolicyDir(base []string, policyDir string) []string {
	if policyDir == "" {
		return base
	}
	return upsertEnv(base, envConfigurePath, policyDir)
}

// procMetrics — метрики ожидания слота и запуска.
type procMetrics struct {
	mu        sync.Mutex
	slotWaits []time.Duration
	runTimes  []time.Duration
}

func newProcMetrics() *procMetrics {
	return &procMetrics{}
}

func (m *procMetrics) observeSlotWait(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.slotWaits = append(m.slotWaits, d)
}

func (m *procMetrics) observeRun(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runTimes = append(m.runTimes, d)
}
