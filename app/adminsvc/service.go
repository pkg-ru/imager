// Package adminsvc реализует application use case административных операций
// над ассетами: фоновая генерация всех/выбранных ассетов исходника и
// удаление ассетов (по исходнику или по списку).
//
// Сервис работает через очередь задач (jobs chan) с пулом воркеров:
//   - EnqueueGenerate ставит задачу генерации в очередь; при переполнении
//     очереди возвращает ошибку ErrQueueFull (→ HTTP 503);
//   - режим wait=true блокирует до завершения всех ассетов задачи (с
//     таймаутом cfg.WaitTimeout) и возвращает полный результат;
//   - режим wait=false возвращает 202 Accepted сразу после постановки в
//     очередь.
//
// Сервис переиспользует generatev2-конвейер через узкий интерфейс Generator
// (совместим с generatev2.Service.Generate) и storage.SourceStore/ResultStore.
package adminsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/ports/storage"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/domain/policy"
	"github.com/pkg-ru/imager/observability"
)

// Logger — единый интерфейс логирования из observability.
type Logger = observability.Logger

// Generator — узкий порт генерации ассета, совместимый с
// generatev2.Service.Generate.
type Generator interface {
	Generate(ctx context.Context, req *asset.Request) (*generatev2.Result, error)
}

// Config — конфигурация admin-сервиса (workers/queue/wait-timeout).
type Config struct {
	// Workers — число параллельных фоновых генераций. Должно быть ≥ 1.
	Workers int
	// QueueSize — ёмкость очереди задач. Должно быть ≥ 1.
	QueueSize int
	// WaitTimeout — таймаут режима wait=true. Должен быть > 0.
	WaitTimeout time.Duration
}

// Deps — зависимости сервиса.
type Deps struct {
	// Gen — генератор ассета (generatev2.Service).
	Gen Generator
	// Sources — хранилище исходников (для проверки существования).
	Sources storage.SourceStore
	// Results — хранилище результатов (skip-existing, delete, list).
	Results storage.ResultStore
	// Presets — набор пресетов (для перечисления ассетов по правилам).
	Presets *asset.PresetSet
	// Policy — скомпилированная политика (для перечисления и авторизации).
	Policy *policy.Policy
	// Logger — опциональный логгер.
	Logger Logger
}

// jobKind — тип задачи.
type jobKind string

const (
	jobGenerate jobKind = "generate"
	jobDelete   jobKind = "delete"
)

// job — единица работы в очереди.
type job struct {
	id     string
	kind   jobKind
	source string
	assets []string
	wait   bool

	// ctx/cancel — контекст задачи. Создаётся для wait=true и отменяется
	// при таймауте ожидания клиента (ErrWaitTimeout), чтобы воркер мог
	// прервать генерацию. После завершения задачи cancel вызывается
	// процессом (идемпотентно, безопасно при двойном вызове).
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	result *JobResult
	err    error
}

// FailedAsset — описание неудавшегося ассета.
type FailedAsset struct {
	URL     string `json:"url"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JobResult — результат выполнения задачи.
type JobResult struct {
	JobID     string        `json:"job_id"`
	Status    string        `json:"status"` // "accepted" | "completed"
	Queued    int           `json:"queued"`
	Generated int           `json:"generated,omitempty"`
	Skipped   int           `json:"skipped,omitempty"`
	Failed    []FailedAsset `json:"failed,omitempty"`
	Deleted   int           `json:"deleted,omitempty"`
}

// Service — точка входа административных операций.
type Service struct {
	gen     Generator
	sources storage.SourceStore
	results storage.ResultStore
	presets *asset.PresetSet
	policy  *policy.Policy
	log     Logger
	cfg     Config

	jobs chan *job
	wg   sync.WaitGroup

	mu       sync.Mutex
	started  bool
	stopping bool
}

// New собирает Service и валидирует зависимости.
func New(deps Deps, cfg Config) (*Service, error) {
	if deps.Gen == nil {
		return nil, fmt.Errorf("adminsvc: nil generator")
	}
	if deps.Sources == nil {
		return nil, fmt.Errorf("adminsvc: nil source store")
	}
	if deps.Results == nil {
		return nil, fmt.Errorf("adminsvc: nil result store")
	}
	if deps.Presets == nil {
		return nil, fmt.Errorf("adminsvc: nil presets")
	}
	if deps.Policy == nil {
		return nil, fmt.Errorf("adminsvc: nil policy")
	}
	if cfg.Workers < 1 {
		return nil, fmt.Errorf("adminsvc: workers must be >= 1, got %d", cfg.Workers)
	}
	if cfg.QueueSize < 1 {
		return nil, fmt.Errorf("adminsvc: queue-size must be >= 1, got %d", cfg.QueueSize)
	}
	if cfg.WaitTimeout <= 0 {
		return nil, fmt.Errorf("adminsvc: wait-timeout must be > 0, got %v", cfg.WaitTimeout)
	}
	log := deps.Logger
	if log == nil {
		log = observability.NopLogger()
	}
	return &Service{
		gen:     deps.Gen,
		sources: deps.Sources,
		results: deps.Results,
		presets: deps.Presets,
		policy:  deps.Policy,
		log:     log,
		cfg:     cfg,
		jobs:    make(chan *job, cfg.QueueSize),
	}, nil
}

// Start запускает пул worker-горутин. Идемпотентно.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	for i := 0; i < s.cfg.Workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
}

// Stop выполняет graceful drain очереди: помечает сервис останавливающимся
// (под мьютексом), закрывает канал задач и ждёт завершения всех воркеров.
// Идемпотентно.
//
// Флаг stopping и close(s.jobs) выполняются под s.mu — тем же мьютексом,
// который держит enqueue при попытке отправки. Это исключает отправку в
// закрытый канал (panic "send on closed channel").
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	close(s.jobs)
	s.mu.Unlock()
	s.wg.Wait()
}

// Close реализует io.Closer (для rt.AddCloser) — вызывает Stop.
func (s *Service) Close() error {
	s.Stop()
	return nil
}

// worker обрабатывает задачи из очереди до её закрытия.
func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()
	for j := range s.jobs {
		s.process(ctx, j)
	}
}

// process выполняет задачу и сигнализирует о завершении через j.done.
// Запись j.result/j.err выполняется только здесь (воркером) до close(j.done),
// поэтому чтение в EnqueueGenerate после <-j.done безопасно.
//
// Для wait-задач используется cancellable job ctx (j.ctx): при таймауте
// ожидания клиента EnqueueGenerate вызывает j.cancel(), и воркер может
// прервать генерацию. Для wait=false задач используется ctx воркера.
func (s *Service) process(ctx context.Context, j *job) {
	jobCtx := ctx
	if j.ctx != nil {
		jobCtx = j.ctx
	}
	switch j.kind {
	case jobGenerate:
		j.result, j.err = s.runGenerate(jobCtx, j)
	case jobDelete:
		j.result, j.err = s.runDelete(jobCtx, j)
	}
	if j.done != nil {
		close(j.done)
	}
	// Освобождаем ресурсы контекста задачи (идемпотентно).
	if j.cancel != nil {
		j.cancel()
	}
}

// newJobID генерирует случайный hex-идентификатор (8 байт).
func newJobID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand практически не возвращает ошибку; fallback на время.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// enqueue ставит задачу в очередь. Возвращает ErrQueueFull при переполнении.
//
// Отправка защищена s.mu — тем же мьютексом, который держит Stop() при
// установке stopping и close(s.jobs). Это исключает гонку «send on closed
// channel»: если Stop() уже закрыл канал, stopping==true и мы не отправляем.
func (s *Service) enqueue(j *job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return ErrStopped
	}
	select {
	case s.jobs <- j:
		return nil
	default:
		return ErrQueueFull
	}
}

// ErrQueueFull — очередь задач переполнена (→ HTTP 503).
var ErrQueueFull = errors.New("adminsvc: queue is full")

// ErrStopped — сервис остановлен (Stop вызван), новые задачи не принимаются.
var ErrStopped = errors.New("adminsvc: service is stopped")

// EnqueueGenerate ставит задачу генерации ассетов.
//
// source — путь исходника (режим A: генерируются ВСЕ ассеты по правилам);
// assets — список канонических URL (режим B). Ровно одно из source/assets
// должно быть задано (иначе ErrInvalidRequest).
//
// wait=true: блокирует до завершения всех ассетов (с таймаутом cfg.WaitTimeout)
// и возвращает полный результат. wait=false: возвращает 202-результат сразу.
func (s *Service) EnqueueGenerate(source string, assets []string, wait bool) (*JobResult, error) {
	if (source == "") == (len(assets) == 0) {
		return nil, ErrInvalidRequest
	}
	var urls []string
	if source != "" {
		// Режим A: проверяем существование исходника ДО постановки в очередь.
		key := object.ObjectKey(source)
		if _, err := s.sources.Lookup(context.Background(), key); err != nil {
			if object.IsNotFound(err) {
				return nil, ErrSourceNotFound
			}
			return nil, fmt.Errorf("adminsvc: lookup source: %w", err)
		}
		ref, err := parseSourceKey(source)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		urls, err = enumerateAssets(ref, s.policy, s.presets)
		if err != nil {
			// Ошибка перечисления (например, unsafe authorization без
			// size-rules) — это невалидный запрос → HTTP 400.
			return nil, ErrInvalidRequest
		}
	} else {
		// Режим B: валидируем каждый asset URL.
		canon := asset.NewCanonicalizer()
		for _, a := range assets {
			req, err := asset.Parse(a)
			if err != nil {
				return nil, ErrInvalidRequest
			}
			url, _, err := canon.CanonicalizeURL(req)
			if err != nil {
				return nil, ErrInvalidRequest
			}
			urls = append(urls, url)
		}
	}

	j := job{
		id:     newJobID(),
		kind:   jobGenerate,
		source: source,
		assets: urls,
		wait:   wait,
	}
	if wait {
		j.done = make(chan struct{})
		// Контекст задачи: отменяется при таймауте ожидания клиента, чтобы
		// воркер мог прервать генерацию (утечка при ErrWaitTimeout).
		j.ctx, j.cancel = context.WithCancel(context.Background())
	}
	if err := s.enqueue(&j); err != nil {
		if j.cancel != nil {
			j.cancel()
		}
		return nil, err
	}

	s.log.Infof("admin_job enqueued id=%s kind=generate queued=%d wait=%v", j.id, len(urls), wait)

	if !wait {
		return &JobResult{JobID: j.id, Status: "accepted", Queued: len(urls)}, nil
	}

	// wait=true: ждём завершения с таймаутом.
	select {
	case <-j.done:
		if j.err != nil {
			return nil, j.err
		}
		return j.result, nil
	case <-time.After(s.cfg.WaitTimeout):
		// Отменяем контекст задачи, чтобы воркер мог прервать генерацию.
		j.cancel()
		return nil, ErrWaitTimeout
	}
}

// ErrInvalidRequest — заданы оба/ни одного из source/assets, либо невалидный
// asset URL (→ HTTP 400).
var ErrInvalidRequest = errors.New("adminsvc: invalid request: exactly one of source or assets is required")

// ErrSourceNotFound — исходник не существует (→ HTTP 404).
var ErrSourceNotFound = errors.New("adminsvc: source not found")

// ErrWaitTimeout — превышен таймаут режима wait=true.
var ErrWaitTimeout = errors.New("adminsvc: wait timeout")

// runGenerate выполняет генерацию всех ассетов задачи.
func (s *Service) runGenerate(ctx context.Context, j *job) (*JobResult, error) {
	res := &JobResult{JobID: j.id, Status: "completed", Queued: len(j.assets)}
	for _, url := range j.assets {
		// Пропуск существующих: перед генерацией проверяем кэш.
		key := object.ObjectKey(url)
		if _, err := s.results.Lookup(ctx, key); err == nil {
			res.Skipped++
			continue
		} else if !object.IsNotFound(err) {
			res.Failed = append(res.Failed, FailedAsset{URL: url, Code: "storage", Message: err.Error()})
			continue
		}

		req, err := asset.Parse(url)
		if err != nil {
			res.Failed = append(res.Failed, FailedAsset{URL: url, Code: "invalid", Message: err.Error()})
			continue
		}
		r, err := s.gen.Generate(ctx, req)
		if err != nil {
			res.Failed = append(res.Failed, FailedAsset{URL: url, Code: codeForErr(err), Message: err.Error()})
			continue
		}
		_ = r.Close()
		res.Generated++
	}
	s.log.Infof("admin_job done id=%s kind=generate generated=%d skipped=%d failed=%d",
		j.id, res.Generated, res.Skipped, len(res.Failed))
	return res, nil
}

// runDelete выполняет удаление ассетов задачи.
func (s *Service) runDelete(ctx context.Context, j *job) (*JobResult, error) {
	res := &JobResult{JobID: j.id, Status: "completed", Queued: len(j.assets)}
	for _, url := range j.assets {
		key := object.ObjectKey(url)
		if err := s.results.Delete(ctx, key); err != nil {
			res.Failed = append(res.Failed, FailedAsset{URL: url, Code: "storage", Message: err.Error()})
			continue
		}
		res.Deleted++
	}
	s.log.Infof("admin_job done id=%s kind=delete deleted=%d failed=%d", j.id, res.Deleted, len(res.Failed))
	return res, nil
}

// DeleteBySource удаляет все ассеты исходника (кроме самого исходника).
//
// Стратегия удаления:
//   - если result-хранилище реализует storage.PrefixDeleter — используется
//     пакетное DeleteByPrefix с префиксом "{path}/{name}-{format}/"
//     (граничный '/' уже учтён в реализациях адаптеров);
//   - иначе если реализует storage.Lister — fallback на List + Delete по
//     одному;
//   - иначе возвращается ErrNotImplemented (→ HTTP 501).
//
// Сам исходник (path/name.format) лежит вне префикса ассетов и не удаляется.
func (s *Service) DeleteBySource(ctx context.Context, source string) (int, error) {
	ref, err := objectRefKey(source)
	if err != nil {
		return 0, ErrInvalidRequest
	}
	prefix := assetPrefix(ref)

	if pd, ok := s.results.(storage.PrefixDeleter); ok {
		n, err := pd.DeleteByPrefix(ctx, object.ObjectKey(prefix))
		if err != nil {
			return 0, fmt.Errorf("adminsvc: delete-by-prefix: %w", err)
		}
		s.log.Infof("admin_job done id=%s kind=delete-by-source deleted=%d", newJobID(), n)
		return int(n), nil
	}

	lister, ok := s.results.(storage.Lister)
	if !ok {
		return 0, ErrNotImplemented
	}
	keys, err := lister.List(ctx, object.ObjectKey(prefix))
	if err != nil {
		return 0, fmt.Errorf("adminsvc: list: %w", err)
	}
	deleted := 0
	for _, k := range keys {
		if err := s.results.Delete(ctx, k); err != nil {
			return deleted, fmt.Errorf("adminsvc: delete %q: %w", k, err)
		}
		deleted++
	}
	s.log.Infof("admin_job done id=%s kind=delete-by-source deleted=%d", newJobID(), deleted)
	return deleted, nil
}

// ErrNotImplemented — result-хранилище не поддерживает ни PrefixDeleter, ни
// List (→ HTTP 501).
var ErrNotImplemented = errors.New("adminsvc: result store does not support listing or prefix deletion")

// DeleteAssets удаляет перечисленные ассеты (канонические URL). Идемпотентно.
func (s *Service) DeleteAssets(ctx context.Context, assets []string) (int, error) {
	deleted := 0
	canon := asset.NewCanonicalizer()
	for _, a := range assets {
		req, err := asset.Parse(a)
		if err != nil {
			return deleted, ErrInvalidRequest
		}
		url, _, err := canon.CanonicalizeURL(req)
		if err != nil {
			return deleted, ErrInvalidRequest
		}
		if err := s.results.Delete(ctx, object.ObjectKey(url)); err != nil {
			return deleted, fmt.Errorf("adminsvc: delete %q: %w", url, err)
		}
		deleted++
	}
	return deleted, nil
}

// codeForErr маппит ошибку генерации в короткий код для FailedAsset.
func codeForErr(err error) string {
	var oe *generatev2.OutcomeError
	if errors.As(err, &oe) {
		return string(oe.Kind)
	}
	if object.IsNotFound(err) {
		return "not_found"
	}
	return "error"
}
