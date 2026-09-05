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
	"strings"
	"sync"
	"time"

	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/domain/policy"
	"github.com/pkg-ru/imager/observability"
	"github.com/pkg-ru/imager/ports/admin"
	"github.com/pkg-ru/imager/ports/generation"
	"github.com/pkg-ru/imager/ports/metadata"
	"github.com/pkg-ru/imager/ports/storage"
)

// Logger — единый интерфейс логирования из observability.
type Logger = observability.Logger

// Generator — узкий порт генерации ассета, совместимый с
// generatev2.Service.Generate.
type Generator = generation.Generator

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
	// Metadata — опциональное sidecar-хранилище метаданных ассетов
	// (.meta.json). Используется при удалении: удаление sidecar при удалении
	// всех ассетов родителя и очистка largest_ai_asset при удалении
	// выбранных ассетов. nil = метаданные не используются.
	Metadata metadata.Store
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

// FailedAsset — описание неудавшегося ассета (порт ports/admin).
type FailedAsset = admin.FailedAsset

// JobResult — результат выполнения задачи (порт ports/admin).
type JobResult = admin.JobResult

// Service — точка входа административных операций.
type Service struct {
	gen     Generator
	sources storage.SourceStore
	results storage.ResultStore
	presets *asset.PresetSet
	policy  *policy.Policy
	meta    metadata.Store
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
		meta:    deps.Metadata,
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

// Sentinel-ошибки определены в порту ports/admin; здесь — алиасы для
// обратной совместимости (→ HTTP-статусы маппит транспорт).
var (
	ErrQueueFull = admin.ErrQueueFull
	ErrStopped   = admin.ErrStopped
)

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
			// Ошибка перечисления — это невалидный запрос → HTTP 400.
			return nil, ErrInvalidRequest
		}
		if len(urls) == 0 {
			// Политика не разрешает ни одного ассета для исходника
			// (deny-by-default без path-policy/пресетов) — нечего
			// генерировать → HTTP 400.
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

	// wait=true: ждём завершения с таймаутом. time.NewTimer + defer Stop,
	// чтобы таймер не утекал после завершения по j.done.
	timer := time.NewTimer(s.cfg.WaitTimeout)
	defer timer.Stop()
	select {
	case <-j.done:
		if j.err != nil {
			return nil, j.err
		}
		return j.result, nil
	case <-timer.C:
		// Отменяем контекст задачи, чтобы воркер мог прервать генерацию.
		j.cancel()
		return nil, ErrWaitTimeout
	}
}

var (
	// ErrInvalidRequest — заданы оба/ни одного из source/assets, либо
	// невалидный asset URL (→ HTTP 400).
	ErrInvalidRequest = admin.ErrInvalidRequest
	// ErrSourceNotFound — исходник не существует (→ HTTP 404).
	ErrSourceNotFound = admin.ErrSourceNotFound
	// ErrWaitTimeout — превышен таймаут режима wait=true.
	ErrWaitTimeout = admin.ErrWaitTimeout
)

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

// runDelete выполняет удаление ассетов задачи. При удалении ассета, который
// является largest_ai_asset, очищает соответствующее поле в sidecar.
func (s *Service) runDelete(ctx context.Context, j *job) (*JobResult, error) {
	res := &JobResult{JobID: j.id, Status: "completed", Queued: len(j.assets)}
	for _, url := range j.assets {
		key := object.ObjectKey(url)
		if err := s.results.Delete(ctx, key); err != nil {
			res.Failed = append(res.Failed, FailedAsset{URL: url, Code: "storage", Message: err.Error()})
			continue
		}
		res.Deleted++
		s.clearLargestAIAsset(ctx, url)
	}
	s.log.Infof("admin_job done id=%s kind=delete deleted=%d failed=%d", j.id, res.Deleted, len(res.Failed))
	return res, nil
}

// DeleteBySource удаляет все ассеты исходника (кроме самого исходника).
//
// Стратегия удаления (в порядке приоритета):
//   - если result-хранилище реализует storage.PrefixDeleter — используется
//     пакетное DeleteByPrefix с префиксом "{path}/{name}-{format}/"
//     (граничный '/' уже учтён в реализациях адаптеров);
//   - иначе если реализует storage.Lister — fallback на List + Delete по
//     одному;
//   - иначе — «слепое» удаление по ключам: ключи всех ассетов формируются
//     из известных политик/правил и пресетов (enumerateAssets), и каждый
//     ключ удаляется напрямую, БЕЗ перечисления содержимого хранилища.
//     Это позволяет удалять ассеты в хранилищах, не поддерживающих ни
//     List, ни DeleteByPrefix, не проходя по всем записям.
//
// После удаления ассетов удаляется sidecar-файл метаданных родителя
// (.meta.json), если metadata.Store задан.
//
// Сам исходник (path/name.format) лежит вне префикса ассетов и не удаляется.
func (s *Service) DeleteBySource(ctx context.Context, source string) (int, error) {
	ref, err := objectRefKey(source)
	if err != nil {
		return 0, ErrInvalidRequest
	}
	prefix := assetPrefix(ref)

	var deleted int
	switch {
	case s.results != nil:
		if pd, ok := s.results.(storage.PrefixDeleter); ok {
			n, err := pd.DeleteByPrefix(ctx, object.ObjectKey(prefix))
			if err != nil {
				return 0, fmt.Errorf("adminsvc: delete-by-prefix: %w", err)
			}
			deleted = int(n)
			break
		}
		if lister, ok := s.results.(storage.Lister); ok {
			keys, err := lister.List(ctx, object.ObjectKey(prefix))
			if err != nil {
				return 0, fmt.Errorf("adminsvc: list: %w", err)
			}
			for _, k := range keys {
				if err := s.results.Delete(ctx, k); err != nil {
					return deleted, fmt.Errorf("adminsvc: delete %q: %w", k, err)
				}
				deleted++
			}
			break
		}
		// «Слепое» удаление по ключам, сформированным из политик/правил.
		urls, err := enumerateAssets(ref, s.policy, s.presets)
		if err != nil {
			return 0, ErrInvalidRequest
		}
		for _, u := range urls {
			if err := s.results.Delete(ctx, object.ObjectKey(u)); err != nil {
				return deleted, fmt.Errorf("adminsvc: delete %q: %w", u, err)
			}
			deleted++
		}
	default:
		return 0, ErrNotImplemented
	}

	// Удаляем sidecar-метаданные родителя (.meta.json), если они управляются.
	// Sidecar привязан к КАТАЛОГУ ассета (<metaRoot>/<каталог ассета>/.meta.json),
	// поэтому ключ должен указывать на файл внутри каталога родителя
	// "{path}/{name}-{format}/" — имя файла не влияет на расположение sidecar.
	if s.meta != nil {
		metaKey := prefix + "x"
		if err := s.meta.Delete(ctx, metaKey); err != nil {
			s.log.Warnf("adminsvc: delete metadata %q: %v", metaKey, err)
		}
	}

	s.log.Infof("admin_job done id=%s kind=delete-by-source deleted=%d", newJobID(), deleted)
	return deleted, nil
}

// ErrNotImplemented — result-хранилище не поддерживает ни PrefixDeleter, ни
// List (→ HTTP 501). Алиас порта ports/admin; в DeleteBySource
// для таких хранилищ теперь используется «слепое» удаление по ключам,
// поэтому эта ошибка фактически не возвращается.
var ErrNotImplemented = admin.ErrNotImplemented

// DeleteAssets удаляет перечисленные ассеты (канонические URL). Идемпотентно.
//
// Если среди удаляемых ассетов есть крупнейший ИИ-ассет (largest_ai_asset),
// информация о нём очищается в sidecar-метаданных родителя (.meta.json):
// сам файл .meta.json НЕ удаляется, а только поле largest_ai_asset.
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
		// Очищаем largest_ai_asset, если удаляется именно этот ассет.
		s.clearLargestAIAsset(ctx, url)
	}
	return deleted, nil
}

// clearLargestAIAsset очищает largest_ai_asset в sidecar-метаданных родителя,
// если удаляемый ассет (url) совпадает с зафиксированным largest_ai_asset.
// Файл .meta.json при этом НЕ удаляется — только очищается поле.
// best-effort: ошибки логируются и не влияют на результат удаления.
func (s *Service) clearLargestAIAsset(ctx context.Context, url string) {
	if s.meta == nil {
		return
	}
	// Sidecar привязан к КАТАЛОГУ ассета: ключ = каталог + имя файла.
	// Извлекаем каталог из URL (всё до последнего '/').
	idx := strings.LastIndex(url, "/")
	metaKey := url
	if idx >= 0 {
		metaKey = url[:idx] + "/x"
	}
	err := s.meta.Update(ctx, metaKey, func(m *filemeta.FileMetadata) (bool, error) {
		if m == nil || m.LargestAIAsset == nil {
			return false, nil
		}
		if m.LargestAIAsset.Key != url {
			return false, nil
		}
		m.LargestAIAsset = nil
		return true, nil
	})
	if err != nil {
		s.log.Warnf("adminsvc: clear largest_ai_asset %q: %v", url, err)
	}
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
