package generatev2

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg-ru/imager/coordination/singleflight"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/domain/policy"
	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/observability"
	"github.com/pkg-ru/imager/ports/bounded"
	"github.com/pkg-ru/imager/ports/buffer"
	"github.com/pkg-ru/imager/ports/coordinator"
	"github.com/pkg-ru/imager/ports/detector"
	"github.com/pkg-ru/imager/ports/generation"
	"github.com/pkg-ru/imager/ports/metadata"
	"github.com/pkg-ru/imager/ports/processor"
	"github.com/pkg-ru/imager/ports/storage"
	"github.com/pkg-ru/imager/ports/videoframe"
)

// Logger — единый интерфейс логирования из observability.
type Logger = observability.Logger

// errLimitExceeded — сигнал превышения лимита политики (C1).
var errLimitExceeded = errors.New("policy limit exceeded")

// publishRetryBase — начальная задержка экспоненциального backoff при
// retry публикации.
const publishRetryBase = 50 * time.Millisecond

// publishRetryMax — максимальная задержка backoff при retry публикации.
const publishRetryMax = 2 * time.Second

// publishRetryAttempts — максимальное число попыток публикации.
const publishRetryAttempts = 3

// Параметры асинхронной публикации (S1). Публикация результата в кэш
// выполняется в фоновых воркерах из bounded-очереди, чтобы не держать ответ
// клиента на времени записи в remote (fsync/upload с retry до 2s).
const (
	// publishQueueDefault — ёмкость bounded-очереди асинхронной публикации.
	// При переполнении публикация выполняется синхронно (fallback), чтобы
	// не терять результаты.
	publishQueueDefault = 512
	// publishWorkersDefault — число фоновых воркеров публикации.
	publishWorkersDefault = 4
	// publishDrainTimeout — таймаут graceful drain очереди при Close.
	publishDrainTimeout = 5 * time.Second
)

// LearningController — узкий интерфейс runtime-флага learning-mode.
// Реализуется app/learning.Controller (и learning.Service). nil = выключено.
type LearningController interface {
	Enabled() bool
}

// Deps — зависимости use case.
type Deps struct {
	// Sources — хранилище исходных объектов.
	Sources storage.SourceStore
	// Results — хранилище сгенерированных ассетов (кэш) с атомарным publish.
	Results storage.ResultStore
	// Coordinator — keyed singleflight/блокировка для dedup concurrent запросов.
	Coordinator coordinator.Keyed
	// Processor — абстрактный исполнитель обработки изображений.
	Processor processor.Processor
	// Policy — скомпилированная deny-by-default политика.
	Policy *policy.Policy
	// Presets — набор пресетов для разрешения preset URL.
	Presets *asset.PresetSet
	// Buffers — фабрика spillable-буферов для материализации результата
	// обработки. Если nil, результат материализуется в памяти без spill.
	Buffers buffer.Factory
	// Limits — application-level лимиты генерации ассетов (application.limits).
	// Нулевые поля = без ограничения. nil = лимиты не заданы.
	Limits *Limits
	// Quality — качество сжатия (0 = по умолчанию).
	Quality int
	// DefaultWatermark — ватермарка по умолчанию (nil = не применяется).
	// Используется для запросов без ватермарки в пресете и без совпавшей
	// path-policy с watermark. Приоритет: пресет → path-policy → default.
	DefaultWatermark *processing.WatermarkSpec
	// DefaultOrientation — ориентация по умолчанию (EXIF auto-orient +
	// ручной rotate/flip из processing.default-*). Используется для запросов
	// без ориентации в пресете. Приоритет: пресет → default. nil =
	// {AutoOrient: true}.
	DefaultOrientation *processing.OrientationSpec
	// DefaultTrim — настройки независимого фильтра trim по умолчанию
	// (режим auto/color + tolerance из processing.default-trim-*). nil =
	// {Mode: auto, Tolerance: 0}. Используется для планов с Trim=true.
	DefaultTrim *processing.TrimSpec
	// Logger — опциональный логгер.
	Logger Logger
	// Metrics — опциональные метрики (request/cache/processor/storage).
	// Если nil, используется NopMetrics.
	Metrics observability.Metrics
	// Metadata — локальное sidecar-хранилище метаданных родительских
	// файлов (кэш результатов ИИ-моделей + largest_ai_asset).
	// nil = кэш моделей отключён.
	Metadata metadata.Store
	// Detector — ИИ-детекция лиц/объектов на уровне приложения.
	// nil = детекция остаётся в процессоре (self-detection).
	// Используется только вместе с Metadata; оба nil или оба заданы.
	Detector detector.Detector
	// VideoExtractor — извлекатель кадра из видео (ffmpeg). nil = видео
	// не поддерживается (запрос ассета из видео вернёт понятную ошибку).
	VideoExtractor videoframe.Extractor
	// DefaultVideoFramePercent — процент от длительности видео, на котором
	// выбирается кадр (0-100). 0 = дефолт 50.
	DefaultVideoFramePercent int64
	// DefaultVideoMinContrast — минимальная контрастность кадра (0-1), ниже
	// которой кадр считается неудачным. 0 = проверка контрастности
	// пропускается.
	DefaultVideoMinContrast float64
	// DefaultVideoFrameStep — на сколько кадров идти вперёд при неудачной
	// проверке контрастности. 0 = дефолт 1.
	DefaultVideoFrameStep int64
	// DefaultVideoAttempts — сколько всего попыток извлечения кадра. 0 =
	// дефолт 3.
	DefaultVideoAttempts int64
	// Learning — runtime-флаг learning-mode (nil = выключено). При
	// включённом режиме запросы, не подходящие по правилам, генерируются
	// (если сегмент — размер-грамматика), но НЕ сохраняются в storage.
	Learning LearningController

	// PublishQueue — настройки фоновой (асинхронной) публикации результата
	// (S1). Публикация выполняется воркерами из bounded-очереди после ответа
	// клиенту, чтобы не держать ответ на времени записи в remote.
	// nil = асинхронная публикация выключена (публикации синхронные, прежнее
	// поведение) — используется в тестах, где кэш должен быть готов сразу
	// после Generate. Заданный конфиг с Disabled=false включает асинхронную
	// публикацию; нулевые поля заменяются дефолтами (Workers, QueueSize,
	// DrainTimeout). Disabled=true = синхронная публикация (как nil).
	PublishQueue *PublishQueueConfig
}

// PublishQueueConfig — конфигурация фоновой публикации (S1).
type PublishQueueConfig struct {
	// Disabled — если true, публикация выполняется синхронно (прежнее
	// поведение). false (умолчание) = асинхронная публикация.
	Disabled bool
	// Workers — число воркеров (0 → publishWorkersDefault).
	Workers int
	// QueueSize — ёмкость bounded-очереди (0 → publishQueueDefault).
	QueueSize int
	// DrainTimeout — таймаут graceful drain при Close (0 → publishDrainTimeout).
	DrainTimeout time.Duration
}

func (d *Deps) validate() error {
	if d.Sources == nil {
		return outcome(OutcomeProcessing, "nil SourceStore", nil)
	}
	if d.Results == nil {
		return outcome(OutcomeProcessing, "nil ResultStore", nil)
	}
	if d.Coordinator == nil {
		return outcome(OutcomeProcessing, "nil Coordinator", nil)
	}
	if d.Processor == nil {
		return outcome(OutcomeProcessing, "nil Processor", nil)
	}
	if d.Policy == nil {
		return outcome(OutcomeProcessing, "nil Policy", nil)
	}
	if d.Presets == nil {
		return outcome(OutcomeProcessing, "nil PresetSet", nil)
	}
	return nil
}

// Service — точка входа генерации ассета.
type Service struct {
	deps    Deps
	log     Logger
	metrics observability.Metrics

	// Асинхронная публикация (S1): bounded-очередь и воркеры, выполняющие
	// publish результата в кэш в фоне, чтобы ответ клиенту не ждал записи
	// в remote (fsync/upload с retry). nil-очередь = асинхронная публикация
	// выключена (все публикации синхронные, прежнее поведение).
	pubQueue   chan publishTask
	pubWorkers int
	pubClosed  atomic.Bool
	pubWG      sync.WaitGroup
	pubOnce    sync.Once
	// pubMu защищает канал pubQueue от гонки send/close: asyncPublish
	// проверяет pubClosed и отправляет под этим мьютексом, Close закрывает
	// канал под тем же мьютексом. Это исключает send-on-closed-channel.
	pubMu sync.Mutex
}

// publishTask — задача асинхронной публикации. Содержит разделяемый
// refcount-буфер и ОТДЕЛЬНЫЙ reader из этого буфера, открытый ДО помещения
// задачи в очередь: данные остаются живыми (refcount), даже если клиент
// закрыл свой reader/буфер раньше, чем воркер успел прочитать.
type publishTask struct {
	key    object.ObjectKey
	buf    buffer.Buffer
	reader io.ReadSeekCloser
}

// publishQueueEnabled сообщает, включена ли асинхронная публикация (S1):
// заданный конфиг (не nil) и Disabled=false. nil/Disabled=true — синхронная
// публикация (прежнее поведение, публикация на пути ответа).
func (s *Service) publishQueueEnabled() bool {
	return s.deps.PublishQueue != nil && !s.deps.PublishQueue.Disabled
}

// publishQueueCapacity возвращает ёмкость bounded-очереди публикации
// (0 = очередь выключена). Нулевое значение → дефолт publishQueueDefault.
func (s *Service) publishQueueCapacity() int {
	if !s.publishQueueEnabled() {
		return 0
	}
	if s.deps.PublishQueue.QueueSize > 0 {
		return s.deps.PublishQueue.QueueSize
	}
	return publishQueueDefault
}

// publishWorkerCount возвращает число воркеров публикации.
// Нулевое значение → дефолт publishWorkersDefault.
func (s *Service) publishWorkerCount() int {
	if !s.publishQueueEnabled() {
		return 0
	}
	if s.deps.PublishQueue.Workers > 0 {
		return s.deps.PublishQueue.Workers
	}
	return publishWorkersDefault
}

// publishDrainTimeoutValue возвращает таймаут drain при Close.
// Нулевое значение → дефолт publishDrainTimeout.
func (s *Service) publishDrainTimeoutValue() time.Duration {
	if !s.publishQueueEnabled() {
		return publishDrainTimeout
	}
	if s.deps.PublishQueue.DrainTimeout > 0 {
		return s.deps.PublishQueue.DrainTimeout
	}
	return publishDrainTimeout
}

// New собирает Service и валидирует зависимости.
func New(d Deps) (*Service, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	log := d.Logger
	if log == nil {
		log = observability.NopLogger()
	}
	metrics := d.Metrics
	if metrics == nil {
		metrics = observability.NopMetrics()
	}
	s := &Service{deps: d, log: log, metrics: metrics}
	// S1: bounded-очередь + воркеры асинхронной публикации. Публикация
	// идёт в фоне; при переполнении очереди — fallback на синхронный
	// publish, чтобы не терять результаты. При выключенном конфиге
	// (nil/Disabled) воркеры не запускаются — публикация синхронная.
	s.startPublishWorkers()
	return s, nil
}

// startPublishWorkers запускает воркеров асинхронной публикации из
// bounded-очереди. Воркеры живут до Close (drain очереди и завершение).
func (s *Service) startPublishWorkers() {
	q := s.publishQueueCapacity()
	if q <= 0 {
		return
	}
	n := s.publishWorkerCount()
	if n < 1 {
		n = 1
	}
	s.pubQueue = make(chan publishTask, q)
	s.pubWorkers = n
	for i := 0; i < n; i++ {
		s.pubWG.Add(1)
		go func() {
			defer s.pubWG.Done()
			for task := range s.pubQueue {
				s.processPublishTask(task)
			}
		}()
	}
}

// Close — graceful drain очереди асинхронной публикации. Дожидается
// завершения уже принятых задач с таймаутом (publishDrainTimeout или
// заданным в конфиге DrainTimeout). После Close новые публикации выполняются
// синхронно (fallback), чтобы не терять результаты. Реализует io.Closer
// (для rt.AddCloser). Идемпотентен (sync.Once).
func (s *Service) Close() error {
	s.pubOnce.Do(func() {
		timeout := s.publishDrainTimeoutValue()
		// Закрываем канал под pubMu: asyncPublish не может отправить в канал
		// после close (send-on-closed-channel panic исключён).
		s.pubMu.Lock()
		s.pubClosed.Store(true)
		if s.pubQueue != nil {
			close(s.pubQueue)
		}
		s.pubMu.Unlock()
		// Дожидаемся завершения уже принятых задач с таймаутом.
		done := make(chan struct{})
		go func() {
			s.pubWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(timeout):
			s.log.Warnf("generatev2: publish queue drain timeout after %s; %d tasks may be dropped", timeout, len(s.pubQueue))
		}
	})
	return nil
}

// publishQueueDepth возвращает текущую глубину очереди (для гауга).
func (s *Service) publishQueueDepth() int64 {
	if s.pubQueue == nil {
		return 0
	}
	return int64(len(s.pubQueue))
}

// publishMetrics возвращает опциональный порт метрик публикации (no-op,
// если не реализован) и текущую глубину очереди.
func (s *Service) publishMetrics() (observability.PublishQueueMetrics, int64) {
	if pm, ok := s.metrics.(observability.PublishQueueMetrics); ok {
		return pm, s.publishQueueDepth()
	}
	return nil, s.publishQueueDepth()
}

// asyncPublish ставит задачу публикации в bounded-очередь. Данные задачи
// уже материализованы в reader (refcount-буфер), поэтому при переполнении
// очереди публикация выполняется СИНХРОННО (fallback), чтобы не терять
// результаты. Возвращает true, если задача принята в очередь.
func (s *Service) asyncPublish(task publishTask) bool {
	// Мьютекс исключает гонку с Close: Close закрывает канал под pubMu,
	// поэтому пока мы держим блокировку, канал гарантированно открыт и
	// send на закрытый канал невозможен. Отправка неблокирующая (default на
	// полную очередь) — мьютекс удерживается лишь на микросекунды.
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.pubClosed.Load() {
		return false
	}
	select {
	case s.pubQueue <- task:
		s.publishQueueDepthGauge()
		return true
	default:
		return false
	}
}

// publishQueueDepthGauge публикует текущую глубину очереди в метрики.
func (s *Service) publishQueueDepthGauge() {
	if pm, _ := s.metrics.(observability.PublishQueueMetrics); pm != nil {
		pm.SetPublishQueueDepth(s.publishQueueDepth())
	}
}

// processPublishTask — выполнение одной публикации из очереди. Использует
// context.Background: публикация должна завершиться даже если запрос клиента
// уже отменён (результат уже сгенерирован и отдан). Retry/backoff — внутри
// publishFromBuffer; при исчерпании ошибка логируется и инкрементируется
// метрика publish-ошибок, результат в кэш НЕ добавляется.
func (s *Service) processPublishTask(task publishTask) {
	pubStart := time.Now()
	err := s.publishFromBuffer(context.Background(), task.key, task.reader)
	_ = task.reader.Close()
	_ = task.buf.Close()
	s.publishQueueDepthGauge()
	if err != nil {
		s.metrics.IncStorageOp(observability.OpResultPublish, true)
		s.metrics.ObserveStorageDuration(observability.OpResultPublish, true, time.Since(pubStart))
		if pm, _ := s.metrics.(observability.PublishQueueMetrics); pm != nil {
			pm.IncPublishError()
		}
		// Лог без секретов: ключ ассета (canonical URL, не секрет) и текст
		// ошибки хранилища. URL/query/raw user input не логируются.
		s.log.Errorf("generatev2: async publish failed (asset=%s): %v", task.key, err)
		return
	}
	s.metrics.IncStorageOp(observability.OpResultPublish, false)
	s.metrics.ObserveStorageDuration(observability.OpResultPublish, false, time.Since(pubStart))
}

// Result — типизированный результат генерации ассета (порт ports/generation).
type Result = generation.Result

// Generate выполняет полный конвейер генерации ассета из уже parsed/validated
// запроса req.
//
// Конвейер: policy decision → cache lookup (без гонки Exists+Open) → keyed
// singleflight/coordinator recheck → source lookup → processing через
// абстрактный Processor → bounded output через writer/limit → атомарный
// publish через ResultStore.
func (s *Service) Generate(ctx context.Context, req *asset.Request) (*Result, error) {
	if ctx == nil {
		return nil, outcome(OutcomeInvalid, "nil context", nil)
	}
	if req == nil {
		return nil, outcome(OutcomeInvalid, "nil request", nil)
	}

	// Policy decision (deny-by-default). Для segment-запросов Policy.Resolve
	// применяет настройки пресета/custom и возвращает канонический запрос;
	// для канонических (программных) запросов Authorize проверяет
	// path-policy. ResolveError (неизвестный пресет) маппится в
	// OutcomeInvalid, чтобы HTTP-слой мог вернуть понятную ошибку.
	//
	// Learning-mode: при включённом режиме запрос, НЕ подходящий по правилам,
	// всё равно генерируется, если сегмент — размер-грамматика (custom-имя
	// вида "120x60"). Такой запрос разрешается дефолтными настройками
	// (resize, размер из сегмента, dpr из URL или 1) и продолжает генерацию.
	// Имя несуществующего пресета (не размер) остаётся 403 — в path-policies
	// такие записи не попадают.
	learning := s.learningEnabled()
	if req.IsPreset() && !req.IsResolved() {
		resolved, dec := s.deps.Policy.Resolve(req)
		if !dec.Allowed {
			if learning {
				if lr, ok := s.learningResolve(req); ok {
					req = lr
				} else {
					return nil, outcome(OutcomeForbidden, "policy: "+string(dec.Reason), nil)
				}
			} else {
				return nil, outcome(OutcomeForbidden, "policy: "+string(dec.Reason), nil)
			}
		} else {
			req = resolved
		}
	} else {
		dec := s.deps.Policy.Authorize(req)
		if !dec.Allowed {
			if learning && req.IsPreset() {
				if lr, ok := s.learningResolve(req); ok {
					req = lr
				} else {
					return nil, outcome(OutcomeForbidden, "policy: "+string(dec.Reason), nil)
				}
			} else {
				return nil, outcome(OutcomeForbidden, "policy: "+string(dec.Reason), nil)
			}
		}
	}

	// Канонический cache key — сам canonical URL (без хеширования). Preset
	// уже раскрыт выше, поэтому ключ совпадает с конечным запросом, а
	// закэшированный ассет доступен по человекочитаемому имени.
	url, _, err := asset.NewCanonicalizer().CanonicalizeURL(req)
	if err != nil {
		return nil, outcome(OutcomeInvalid, "canonicalize url", err)
	}
	key := object.ObjectKey(url)

	// Ограничение: отдаём ТОЛЬКО медиа-файлы (картинки/анимации/векторы/
	// видео). Не-медиа форматы (HTML, метаданные, исходники и т.п.) не
	// отдаются, даже если файл существует.
	if !isMediaFormat(req.OutputFormats().String()) {
		return nil, outcome(OutcomeInvalid, "unsupported media format", nil)
	}

	// Fast-path: запрос ОРИГИНАЛА (size=x, без transform, выходной формат
	// равен исходному) — отдаём исходный файл как есть, без построения
	// плана, без чтения метаданных, без обработки процессором.
	if isOriginalRequest(req) {
		return s.serveOriginal(ctx, key, url, req)
	}

	// Fast-path cache lookup (без гонки Exists+Open): если готовый ассет
	// уже существует, отдаём его как есть, без построения плана и без
	// получения информации.
	if st, ok, err := s.tryCache(ctx, key); err != nil {
		return nil, err
	} else if ok {
		s.metrics.IncCacheHit()
		return &Result{Key: key, URL: url, Request: req, Opened: st, FromCache: true}, nil
	}
	s.metrics.IncCacheMiss()

	// Keyed singleflight: concurrent запросы с тем же ключом дедуплицируются.
	// generateLocked публикует результат в кэш ДО возврата, поэтому после
	// Coordinator.Do все запросы (владелец и waiters) получают общий буфер
	// singleflight. Владелец и waiters, успевшие создать reader, читают
	// результат из этого буфера (refcount) — без повторного чтения из remote.
	// Данные буфера живут, пока открыт хотя бы один reader (см.
	// memBuffer/remote.Buffer), поэтому закрытие reader'а одним запросом не
	// ломает остальных.
	v, err := s.deps.Coordinator.Do(ctx, key, func() (any, error) {
		return s.generateLocked(ctx, key, req)
	})
	if err != nil {
		return nil, s.mapCoordinatorError(ctx, err)
	}
	buf, ok := v.(buffer.Buffer)
	if !ok {
		return nil, outcome(OutcomeProcessing, "coordinator returned invalid result", nil)
	}
	// Learning-mode: результат НЕ публикуется в кэш (generateLocked вернул
	// общий буфер singleflight без publish). Отдаём его клиенту напрямую —
	// каждый запрос получает собственный reader из общего буфера, без
	// повторного чтения из remote.
	if learning {
		reader, err := buf.NewReader()
		if err != nil {
			_ = buf.Close()
			return nil, outcome(OutcomeProcessing, "buffer reader", err)
		}
		meta := object.ObjectMetadata{Key: key, Size: buf.Size()}
		return &Result{
			Key:       key,
			URL:       url,
			Request:   req,
			Opened:    &bufferStream{buf: buf, r: reader, meta: meta},
			FromCache: false,
		}, nil
	}
	// Non-learning: результат уже опубликован в кэш. Пытаемся отдать reader
	// из общего буфера singleflight (владелец и waiters, успевшие создать
	// reader до освобождения буфера). Если буфер уже освобождён другим
	// запросом (NewReader → ошибка), читаем результат из кэша — это
	// безопасно, т.к. владелец опубликовал результат ДО возврата из
	// generateLocked.
	reader, err := buf.NewReader()
	if err != nil {
		_ = buf.Close()
		cbuf, err := s.readResultBuffer(ctx, key)
		if err != nil {
			return nil, err
		}
		reader, err := cbuf.NewReader()
		if err != nil {
			_ = cbuf.Close()
			return nil, outcome(OutcomeProcessing, "buffer reader", err)
		}
		meta := object.ObjectMetadata{Key: key, Size: cbuf.Size()}
		return &Result{
			Key:       key,
			URL:       url,
			Request:   req,
			Opened:    &bufferStream{buf: cbuf, r: reader, meta: meta},
			FromCache: false,
		}, nil
	}
	meta := object.ObjectMetadata{Key: key, Size: buf.Size()}
	return &Result{
		Key:       key,
		URL:       url,
		Request:   req,
		Opened:    &bufferStream{buf: buf, r: reader, meta: meta},
		FromCache: false,
	}, nil
}

// learningEnabled сообщает, включён ли learning-mode (nil-контроллер = off).
func (s *Service) learningEnabled() bool {
	return s.deps.Learning != nil && s.deps.Learning.Enabled()
}

// learningResolve разрешает segment-запрос, не подходящий по правилам,
// дефолтными настройками learning-mode: resize, размер из сегмента
// (размер-грамматика), dpr из URL или 1, выходной формат из URL.
//
// Возвращает (nil, false), если сегмент НЕ является размер-грамматикой
// (имя несуществующего пресета) — такой запрос остаётся 403, в path-policies
// такие записи не попадают.
func (s *Service) learningResolve(req *asset.Request) (*asset.Request, bool) {
	if req == nil || !req.IsPreset() {
		return nil, false
	}
	size, err := asset.ParseSize(req.SegmentName().String())
	if err != nil {
		return nil, false
	}
	dpr := req.DPR()
	if dpr == 0 {
		dpr = asset.DefaultDPR
	}
	return req.WithResolved(
		"", // transform: resize
		size,
		dpr,
		0, 0, 0, nil, nil, nil,
	), true
}

// generateLocked выполняет генерацию под защитой singleflight: recheck кэша,
// поиск источника, обработку в Buffer и параллельную публикацию в remote.
// Возвращает общий Buffer, из которого каждый запрос получает собственный
// reader.
func (s *Service) generateLocked(ctx context.Context, key object.ObjectKey, req *asset.Request) (buffer.Buffer, error) {
	// Recheck кэша: другой запрос мог уже сгенерировать ассет.
	if st, ok, err := s.tryCache(ctx, key); err != nil {
		return nil, err
	} else if ok {
		s.metrics.IncCacheHit()
		_ = st.Close()
		// Кэш уже заполнен другим запросом — читаем из remote.
		return s.readResultBuffer(ctx, key)
	}

	// Видео-источник и не-original запрос: ассет генерируется из ОДНОГО
	// кадра видео (извлечённого через VideoExtractor или закэшированного
	// x.jpg), а не из самого видео (процессоры не умеют декодировать
	// видео). Оригинал видео отдаётся как есть через serveOriginal выше.
	if isVideoFormat(req.SourceFormat().String()) {
		return s.generateVideoLocked(ctx, key, req)
	}

	// Поиск и открытие источника одним round-trip: Open возвращает
	// ErrNotFound, если источник отсутствует, поэтому предварительный Lookup
	// (дополнительный сетевой запрос для remote-хранилищ) не нужен.
	//
	// Открытие источника и построение плана обработки НЕ зависят друг от
	// друга, поэтому выполняются параллельно (fan-out/fan-in через каналы).
	// Это сокращает критический путь генерации на время самого медленного
	// из двух шагов вместо их суммы.
	srcKey := s.sourceKey(req)

	type openResult struct {
		src object.Artifact
		err error
	}
	type planResult struct {
		plan *processing.ProcessingPlan
		err  error
	}

	openCh := make(chan openResult, 1)
	planCh := make(chan planResult, 1)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		openStart := time.Now()
		src, err := s.deps.Sources.Open(ctx, srcKey)
		if err != nil {
			s.metrics.IncStorageOp(observability.OpSourceOpen, true)
			s.metrics.ObserveStorageDuration(observability.OpSourceOpen, true, time.Since(openStart))
		} else {
			s.metrics.IncStorageOp(observability.OpSourceOpen, false)
			s.metrics.ObserveStorageDuration(observability.OpSourceOpen, false, time.Since(openStart))
		}
		openCh <- openResult{src: src, err: err}
	}()
	go func() {
		defer wg.Done()
		plan, err := s.buildPlan(req)
		planCh <- planResult{plan: plan, err: err}
	}()
	wg.Wait()
	close(openCh)
	close(planCh)

	or := <-openCh
	if or.err != nil {
		if object.IsNotFound(or.err) {
			return nil, outcome(OutcomeNotFound, "source not found", or.err)
		}
		return nil, s.mapSourceError(ctx, or.err)
	}
	src := or.src
	defer src.Close()

	pr := <-planCh
	if pr.err != nil {
		return nil, outcome(OutcomeInvalid, "build processing plan", pr.err)
	}
	plan := pr.plan

	// C1: проверка application-лимитов ДО обработки. Размер источника берём
	// из метаданных открытого объекта (без дополнительного round-trip).
	// Размеры и DPR — из запроса (уже с учётом DPR-умножения в buildPlan).
	// Frames/duration для статичных изображений не определяются на
	// application-уровне (0) — они контролируются лимитами движка.
	meta := src.Metadata()
	var w, h int
	if !plan.Size.Original {
		w, h = plan.Size.Width, plan.Size.Height
	}
	check := s.deps.Limits.Check(meta.Size, int64(w), int64(h), int64(req.DPR().Int()), 0, 0, 0)
	if check.Exceeded() {
		return nil, outcome(OutcomeForbidden, "application limit: "+check.ExceededLimit, errLimitExceeded)
	}

	// Кэш ИИ-моделей: для планов с детекцией (fc/oc/fct/oct) боксы
	// загружаются из sidecar или добываются детектором ОДИН раз на
	// ассет (keyed singleflight по "meta:"+assetKey). best-effort:
	// при любом сбое возвращается (false, nil) — процессор работает
	// в режиме self-detection. Метаданные привязаны к ассету-результату,
	// поэтому ключом служит key (канонический URL ассета).
	in := processor.Input{Source: src, Plan: plan, SourceKey: srcKey}
	in.DetectionsReady, in.Boxes = s.ensureDetections(ctx, key, plan, src)

	// Обработка в Processor + параллельная публикация в remote.
	buf, err := s.processAndPublish(ctx, key, in)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// readResultBuffer читает результат из remote в Buffer (используется, когда
// кэш уже заполнен другим запросом, но нужно вернуть общий Buffer).
func (s *Service) readResultBuffer(ctx context.Context, key object.ObjectKey) (buffer.Buffer, error) {
	st, err := s.deps.Results.ReadStream(ctx, key)
	if err != nil {
		return nil, s.mapResultError(ctx, err)
	}
	defer st.Close()

	buf, err := s.newBuffer()
	if err != nil {
		return nil, outcome(OutcomeProcessing, "create buffer", err)
	}
	if _, err := io.Copy(buf, st); err != nil {
		_ = buf.Close()
		return nil, outcome(OutcomeProcessing, "read result", err)
	}
	return buf, nil
}

// newBuffer создаёт spillable-буфер через фабрику (или in-memory, если
// фабрика не задана).
func (s *Service) newBuffer() (buffer.Buffer, error) {
	if s.deps.Buffers != nil {
		return s.deps.Buffers.NewBuffer()
	}
	return &memBuffer{}, nil
}

// tryCache пытается вернуть закэшированный результат как одноразовый
// поток (ReadStream). Возвращает (nil, false, nil), если кэш пуст и нужно
// генерировать.
//
// ReadStream возвращает ErrNotFound для отсутствующего объекта, поэтому
// предварительный Lookup (дополнительный round-trip для remote-хранилищ)
// не выполняется.
func (s *Service) tryCache(ctx context.Context, key object.ObjectKey) (object.Stream, bool, error) {
	openStart := time.Now()
	st, err := s.deps.Results.ReadStream(ctx, key)
	if err != nil {
		s.metrics.IncStorageOp(observability.OpResultOpen, true)
		s.metrics.ObserveStorageDuration(observability.OpResultOpen, true, time.Since(openStart))
		if object.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, s.mapResultError(ctx, err)
	}
	s.metrics.IncStorageOp(observability.OpResultOpen, false)
	s.metrics.ObserveStorageDuration(observability.OpResultOpen, false, time.Since(openStart))
	return st, true, nil
}

// sourceKey строит ключ исходного объекта из запроса.
func (s *Service) sourceKey(req *asset.Request) object.ObjectKey {
	file := req.SourceName().String() + "." + req.SourceFormat().String()
	if req.Path() == "" {
		return object.ObjectKey(file)
	}
	return object.ObjectKey(req.Path() + "/" + file)
}

// resolveWatermark определяет ватермарку запроса по приоритету:
//  1. ватермарка пресета/custom (заполняется при разрешении segment URL
//     через Policy.Resolve);
//  2. ватермарка по умолчанию из конфигурации (Deps.DefaultWatermark).
//
// Path-policy больше не несёт ватермарку (поле Watermark удалено из
// PathPolicy в новой архитектуре) — ватермарка приходит только из
// пресета/custom или глобального дефолта.
func (s *Service) resolveWatermark(req *asset.Request) *processing.WatermarkSpec {
	if wm := req.Watermark(); wm != nil {
		return wm
	}
	return s.deps.DefaultWatermark
}

// resolveOrientation определяет ориентацию запроса по приоритету:
//  1. ориентация пресета (заполняется при разрешении preset URL, уже
//     смержена с глобальным дефолтом на этапе компиляции);
//  2. глобальный дефолт Deps.DefaultOrientation (processing.default-*).
//
// Результат никогда не nil: при отсутствии настроек возвращается
// {AutoOrient: true}.
func (s *Service) resolveOrientation(req *asset.Request) *processing.OrientationSpec {
	if o := req.Orientation(); o != nil {
		return o
	}
	if o := s.deps.DefaultOrientation; o != nil {
		return o
	}
	return processing.DefaultOrientation()
}

// buildPlan преобразует канонический запрос в валидированный план обработки.
//
// Quality/frames/duration/loop берутся из запроса (заполняются при
// разрешении пресета); если не заданы (0/nil), используются значения по
// умолчанию из Deps (Quality) или остаются без ограничения.
// Ватермарка: пресет → path-policy → default (см. resolveWatermark).
// Ориентация: пресет → default (см. resolveOrientation).
func (s *Service) buildPlan(req *asset.Request) (*processing.ProcessingPlan, error) {
	srcFmt, err := processing.ParseFormat(req.SourceFormat().String())
	if err != nil {
		return nil, err
	}
	return s.buildPlanForSource(req, srcFmt)
}

// buildPlanForSource строит план обработки с заданным исходным форматом.
// Для видео-источников исходным форматом служит JPEG (извлечённый кадр),
// а не сам видео-формат (процессоры не умеют декодировать видео).
func (s *Service) buildPlanForSource(req *asset.Request, srcFmt processing.Format) (*processing.ProcessingPlan, error) {
	// Кроп и trim — НЕЗАВИСИМЫЕ фильтры. Transform URL — код вида
	// "c"/"t"/"ct"/"sc"/"fc"/"oc"/"sct"/"fct"/"oct": trim в коде всегда
	// последний ("t"-суффикс). Операция плана — только режим кропа/ресайза,
	// trim выделяется в отдельное булево поле (применяется первым).
	op, trim := transformFromPlan(req.Transform())
	outFmt, err := processing.ParseFormat(req.OutputFormats().String())
	if err != nil {
		return nil, err
	}
	// DPR multiplication: targetSize = requested * DPR. Отсутствие суффикса
	// @dpr нормализуется парсером в DefaultDPR (1).
	dpr := req.DPR().Int()
	if dpr <= 0 {
		dpr = asset.DefaultDPR
	}
	// Параметры обработки: пресет имеет приоритет, иначе default-quality.
	quality := req.Quality()
	if quality == 0 {
		quality = s.deps.Quality
	}
	loop := req.Loop()
	frames := req.Frames()
	duration := req.Duration()
	wm := s.resolveWatermark(req)
	or := s.resolveOrientation(req)
	var w, h int
	if req.Size().IsOriginal() {
		// size=x: сохранить исходный размер изображения.
		plan, err := processing.NewProcessingPlan(
			op, srcFmt, outFmt,
			processing.Size{Original: true},
			dpr, quality, loop, frames, duration,
		)
		if err != nil {
			return nil, err
		}
		plan.Watermark = wm
		plan.Orientation = or
		plan.Trim = trim
		plan.TrimSpec = s.resolveTrim()
		return plan, nil
	}
	if dw := req.Size().Width(); dw != nil {
		w = dw.Int() * dpr
	}
	if dh := req.Size().Height(); dh != nil {
		h = dh.Int() * dpr
	}
	// Кроп (и детекторные кропы) требует ОБА измерения: для размеров с
	// одним измерением (x200/200x — вторая сторона вычисляется
	// пропорционально) кроп невозможен, поэтому операция понижается до
	// resize. Trim при этом сохраняется (trim-only остаётся trim-only).
	if (w == 0 || h == 0) && isCropOperation(op) {
		op = processing.OpResize
	}
	plan, err := processing.NewProcessingPlan(
		op, srcFmt, outFmt,
		processing.Size{Width: w, Height: h},
		dpr, quality, loop, frames, duration,
	)
	if err != nil {
		return nil, err
	}
	plan.Watermark = wm
	plan.Orientation = or
	plan.Trim = trim
	plan.TrimSpec = s.resolveTrim()
	return plan, nil
}

// resolveTrim определяет настройки независимого фильтра trim. Trim — не
// URL-параметр: настройки (режим auto/color + tolerance) приходят из
// глобального конфига processing.default-trim-*. nil = спецификация по
// умолчанию ({auto, 0}).
func (s *Service) resolveTrim() *processing.TrimSpec {
	if t := s.deps.DefaultTrim; t != nil {
		return t
	}
	return processing.DefaultTrimSpec()
}

// transformFromPlan маппит Transform URL-код в операцию кропа/ресайза и
// независимый булев trim:
//
//	""   → resize,      trim=false
//	"c"  → crop,        trim=false
//	"t"  → resize,      trim=true   (только trim, без кропа)
//	"ct" → crop,        trim=true   (сначала trim, затем центрированный кроп)
//	"sc" → smart-crop,  trim=false
//	"sct"→ smart-crop,  trim=true
//	"fc" → face-crop,   trim=false
//	"fct"→ face-crop,   trim=true
//	"oc" → object-crop, trim=false
//	"oct"→ object-crop, trim=true
//
// isCropOperation сообщает, является ли операция кропом (требует оба
// измерения целевого размера). Resize — не кроп.
func isCropOperation(op processing.Operation) bool {
	switch op {
	case processing.OpCrop, processing.OpSmartCrop, processing.OpFaceCrop, processing.OpObjectCrop:
		return true
	default:
		return false
	}
}

func transformFromPlan(t asset.Transform) (processing.Operation, bool) {
	switch t {
	case asset.TransformCrop:
		return processing.OpCrop, false
	case asset.TransformCropTrim:
		return processing.OpCrop, true
	case asset.TransformSmartCrop:
		return processing.OpSmartCrop, false
	case asset.TransformSmartCropTrim:
		return processing.OpSmartCrop, true
	case asset.TransformFaceCrop:
		return processing.OpFaceCrop, false
	case asset.TransformFaceCropTrim:
		return processing.OpFaceCrop, true
	case asset.TransformObjectCrop:
		return processing.OpObjectCrop, false
	case asset.TransformObjectCropTrim:
		return processing.OpObjectCrop, true
	case asset.TransformTrim:
		return processing.OpResize, true
	default:
		return processing.OpResize, false
	}
}

// processAndPublish запускает процессор, который пишет результат в
// spillable Buffer, затем публикует результат в remote через ResultStore.
// Возвращает Buffer, из которого клиент читает результат.
//
// Отдача клиенту идёт из Buffer (куда процессор записал результат), а не из
// remote. Публикация выполняется асинхронно (S1): результат ставится в
// bounded-очередь фоновых воркеров и возвращается клиенту СРАЗУ, без
// ожидания записи в remote. Waiters singleflight получают reader из общего
// refcount-буфера (фаза 2), поэтому их ответ НЕ зависит от завершения
// publish. Если очередь переполнена или сервис закрывается — публикация
// выполняется синхронно (fallback), чтобы не терять результаты.
func (s *Service) processAndPublish(ctx context.Context, key object.ObjectKey, in processor.Input) (buffer.Buffer, error) {
	buf, err := s.newBuffer()
	if err != nil {
		return nil, outcome(OutcomeProcessing, "create buffer", err)
	}

	procStart := time.Now()
	procRes, procErr := s.deps.Processor.Process(ctx, in, buf)
	if ctx.Err() != nil {
		s.metrics.IncProcessorError()
		s.metrics.ObserveProcessorDuration(time.Since(procStart))
		_ = buf.Close()
		return nil, outcome(OutcomeCanceled, "canceled", ctx.Err())
	}
	if procErr != nil {
		s.metrics.IncProcessorError()
		s.metrics.ObserveProcessorDuration(time.Since(procStart))
		_ = buf.Close()
		// Перегрузка процессора (bounded очередь переполнена) — сигнал
		// клиенту повторить позже (503 + Retry-After), а не 500.
		if isTooManyConcurrency(procErr) {
			return nil, outcome(OutcomeOverloaded, "processor overloaded", procErr)
		}
		return nil, outcome(OutcomeProcessing, "process image", procErr)
	}
	// C1: post-check application-лимитов (output-bytes). Превышение
	// output-bytes — это квота (OutcomeQuota), а не запрет политики:
	// запрос валиден, но результат не помещается в лимит выхода.
	check := s.deps.Limits.Check(0, 0, 0, 0, 0, buf.Size(), 0)
	if check.Exceeded() {
		s.metrics.IncProcessorError()
		s.metrics.ObserveProcessorDuration(time.Since(procStart))
		_ = buf.Close()
		return nil, outcome(OutcomeQuota, "output exceeds limit: "+check.ExceededLimit, bounded.ErrOutputLimitExceeded)
	}
	s.metrics.IncProcessorSuccess()
	s.metrics.ObserveProcessorDuration(time.Since(procStart))

	// Learning-mode: результат НЕ сохраняется в storage — пропускаем
	// публикацию, largest_ai_asset и created_unix. Буфер возвращается
	// клиенту напрямую (см. Generate).
	if s.learningEnabled() {
		return buf, nil
	}

	// Публикация: асинхронно через bounded-очередь (S1). Открываем ОТДЕЛЬНЫЙ
	// reader из refcount-буфера ДО помещения задачи в очередь: клиент может
	// закрыть свой reader/буфер сразу после ответа, и данные должны остаться
	// живыми для воркера (refcount удерживает память/file, пока открыт хотя
	// бы один reader). Retry/backoff остаются внутри publishFromBuffer.
	reader, err := buf.NewReader()
	if err != nil {
		_ = buf.Close()
		return nil, outcome(OutcomeProcessing, "publish reader", err)
	}

	// Порядок важен: создаём задачу после открытия reader'а и пробуем
	// поставить её в очередь. При переполнении (все воркеры заняты, буфер
	// очереди полон) или после закрытия сервиса — синхронный fallback.

	// largest_ai_asset + created_unix: обе операции best-effort, выполняются
	// АСИНХРОННО (fire-and-forget) и НЕ влияют на результат генерации —
	// клиент получил буфер, данные уже материализованы. Поэтому они ставятся
	// ДО ветки async/sync публикации и выполняются в обоих путях. Если бы они
	// остались только после синхронного publish, при асинхронной публикации
	// метаданные (largest_ai_asset, created_unix) терялись бы.
	//
	// largest_ai_asset: best-effort обновление, ТОЛЬКО при реальном ИИ-ассете
	// (выход больше родителя с теми же пропорциями: srcW×srcH → outW×outH).
	// Обычные resize/watermark не кандидаты → в Metadata даже не входим
	// (ленивость: ни Load, ни Update, ни singleflight). Пропуск проверяется
	// здесь, ДО Coordinator.Do, чтобы не создавать на каждый publish. Размеры
	// берём из Result процессора (0 = неизвестно → ShouldTrackAsAIAsset
	// вернёт false). Метаданные привязаны к АССЕТУ-результату, поэтому
	// ключом служит key. Используется context.Background, чтобы запись
	// завершилась даже после отмены запроса.
	if procRes != nil && filemeta.ShouldTrackAsAIAsset(
		procRes.SourceWidth, procRes.SourceHeight,
		procRes.Width, procRes.Height,
	) {
		s.updateLargestAIAssetAsync(
			key,
			in.Plan.OutputFormats.String(),
			procRes.Width, procRes.Height,
			procRes.SourceWidth, procRes.SourceHeight,
		)
	}

	// created_unix: ленивая асинхронная запись unix-времени создания первого
	// ассета. Выполняется в фоне (не блокирует ответ), best-effort.
	s.recordAssetCreationTime(ctx, key)

	task := publishTask{key: key, buf: buf, reader: reader}
	if s.asyncPublish(task) {
		return buf, nil
	}

	// Fallback: синхронная публикация. Не теряем результаты, но блокируем
	// ответ на время записи в remote — допустимо только при переполнении
	// очереди или shutdown.
	pubStart := time.Now()
	pubErr := s.publishFromBuffer(ctx, key, reader)
	// Закрываем только reader воркера, но НЕ буфер: он возвращается клиенту
	// и закрывается через bufferStream.Close() при result.Close() (client
	// закрывает reader и сам буфер). Раньше здесь был buf.Close(), что
	// освобождало память преждевременно — клиентский buf.NewReader() в
	// Generate падал с "buffer closed" и ломался fallback на readResultBuffer.
	_ = reader.Close()
	if pubErr != nil {
		s.metrics.IncStorageOp(observability.OpResultPublish, true)
		s.metrics.ObserveStorageDuration(observability.OpResultPublish, true, time.Since(pubStart))
		if ctx.Err() != nil {
			return nil, outcome(OutcomeCanceled, "canceled", ctx.Err())
		}
		return nil, s.mapPublishError(ctx, pubErr)
	}
	s.metrics.IncStorageOp(observability.OpResultPublish, false)
	s.metrics.ObserveStorageDuration(observability.OpResultPublish, false, time.Since(pubStart))

	return buf, nil
}

// publishFromBuffer публикует содержимое reader в remote. Принимает готовый
// reader (открытый ДО вызова), чтобы удержать данные refcount-буфера живыми,
// даже если клиент закрыл свой reader/буфер. Вызывается из воркеров
// асинхронной очереди и из синхронного fallback.
//
// Transient-ошибки (ErrUnavailable) ретраятся с экспоненциальным backoff.
// boundedReader не читает лишний байт — лимит проверяется ДО чтения, поэтому
// при превышении в remote не попадает битый объект.
//
// ErrConflict (NoOverwrite, объект уже существует) трактуется как УСПЕХ:
// повторная публикация того же ассета (например, при повторной генерации до
// завершения фоновой публикации) означает, что результат уже в кэше. Это не
// ошибка и не должно логироваться как publish-failure или инкрементировать
// счётчик ошибок.
func (s *Service) publishFromBuffer(ctx context.Context, key object.ObjectKey, reader io.ReadSeekCloser) error {
	var r io.Reader = reader
	var br *bounded.BoundedReader
	if s.deps.Limits != nil && s.deps.Limits.OutputBytes > 0 {
		br = bounded.NewBoundedReader(reader, s.deps.Limits.OutputBytes)
		r = br
	}

	var lastErr error
	for attempt := range publishRetryAttempts {
		if attempt > 0 {
			delay := publishRetryBase << (attempt - 1)
			if delay > publishRetryMax {
				delay = publishRetryMax
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		// Каждая попытка читает с начала: перематываем reader и сбрасываем
		// счётчик boundedReader, иначе повторная попытка опубликует
		// усечённые данные.
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if br != nil {
			br.Reset()
		}
		lastErr = s.deps.Results.Publish(ctx, key, r, object.PublishOptions{})
		if lastErr == nil {
			return nil
		}
		// Объект уже существует (NoOverwrite) — результат уже в кэше,
		// считаем публикацию успешной (см. комментарий выше).
		if object.IsConflict(lastErr) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Ретраим только transient-ошибки (unavailable).
		if !object.IsUnavailable(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// mapOutcomeError — общий каркас маппинга ошибок хранилища/координатора в
// типизированный OutcomeError: отмена контекста имеет приоритет над любым
// маппингом, затем применяется специфичный для вызывающего mapper.
func mapOutcomeError(ctx context.Context, err error, mapper func(error) error) error {
	if ctx.Err() != nil {
		return outcome(OutcomeCanceled, "canceled", ctx.Err())
	}
	return mapper(err)
}

// mapResultError маппит ошибку ResultStore в типизированный OutcomeError.
func (s *Service) mapResultError(ctx context.Context, err error) error {
	return mapOutcomeError(ctx, err, func(err error) error {
		switch {
		case object.IsNotFound(err):
			return outcome(OutcomeNotFound, "result not found", err)
		case object.IsQuota(err):
			return outcome(OutcomeQuota, "result store quota exceeded", err)
		case object.IsUnavailable(err):
			return outcome(OutcomeUnavailable, "result store unavailable", err)
		default:
			return outcome(OutcomeProcessing, "result store error", err)
		}
	})
}

// mapSourceError маппит ошибку SourceStore в типизированный OutcomeError.
func (s *Service) mapSourceError(ctx context.Context, err error) error {
	return mapOutcomeError(ctx, err, func(err error) error {
		switch {
		case object.IsNotFound(err):
			return outcome(OutcomeNotFound, "source not found", err)
		case object.IsUnavailable(err):
			return outcome(OutcomeUnavailable, "source store unavailable", err)
		default:
			return outcome(OutcomeProcessing, "source store error", err)
		}
	})
}

// mapPublishError маппит ошибку публикации в типизированный OutcomeError.
func (s *Service) mapPublishError(ctx context.Context, err error) error {
	return mapOutcomeError(ctx, err, func(err error) error {
		switch {
		case object.IsQuota(err):
			return outcome(OutcomeQuota, "publish quota exceeded", err)
		case object.IsUnavailable(err):
			return outcome(OutcomeUnavailable, "publish store unavailable", err)
		case object.IsConflict(err):
			return outcome(OutcomeProcessing, "publish conflict", err)
		default:
			return outcome(OutcomeProcessing, "publish result", err)
		}
	})
}

// mapCoordinatorError маппит ошибку координатора в типизированный OutcomeError.
// Уже типизированные OutcomeError (например, из generateLocked) пробрасываются
// как есть.
//
// ErrTooManyKeys/ErrKeyTooLong — перегрузка координатора (429/503 с
// Retry-After), а не "unavailable". Маппятся в OutcomeUnavailable с явной
// причиной, чтобы HTTP-слой мог вернуть Retry-After.
func (s *Service) mapCoordinatorError(ctx context.Context, err error) error {
	return mapOutcomeError(ctx, err, func(err error) error {
		var oe *OutcomeError
		if errors.As(err, &oe) {
			return oe
		}
		if errors.Is(err, singleflight.ErrTooManyKeys) || errors.Is(err, singleflight.ErrKeyTooLong) {
			return outcome(OutcomeUnavailable, "coordination overloaded", err)
		}
		return outcome(OutcomeUnavailable, "coordination unavailable", err)
	})
}

// bufferStream — object.Stream поверх buffer.Buffer. Отдаёт клиенту данные
// из буфера, куда процессор записал результат (без повторной материализации
// из remote). Close закрывает reader, что через reference counting
// освобождает ресурсы буфера.
type bufferStream struct {
	buf  buffer.Buffer
	r    io.ReadSeekCloser
	meta object.ObjectMetadata
}

func (s *bufferStream) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *bufferStream) Close() error {
	// Закрываем reader и помечаем буфер closed. Ресурсы буфера (память/файл)
	// освобождаются через reference counting, когда все reader'ы закрыты.
	// Новые reader'ы можно создавать, пока ресурсы не освобождены (см.
	// Buffer.NewReader), поэтому другие запросы singleflight не ломаются.
	err := s.r.Close()
	_ = s.buf.Close()
	return err
}

func (s *bufferStream) Metadata() object.ObjectMetadata { return s.meta }

// isTooManyConcurrency распознаёт сигнал перегрузки процессора (bounded
// очередь ожидания слота переполнена). Процессор (libvips) возвращает
// sentinel-ошибку с одинаковым текстом; распознаём по нему, чтобы
// не завязывать application-слой на конкретные адаптеры.
func isTooManyConcurrency(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "too many concurrent")
}

// mediaFormats — множество форматов, которые сервис отдаёт клиенту.
// Только медиа-файлы: картинки/анимации (jpeg/png/webp/gif/avif/heif/apng/
// jxl), векторы (svg) и видео (mp4/webm/mov/mkv/avi/m4v). Любые другие
// форматы (HTML, метаданные, исходники и т.п.) не отдаются, даже если
// файл существует.
var mediaFormats = map[string]struct{}{
	"jpeg": {}, "jpg": {}, "png": {}, "webp": {}, "gif": {},
	"avif": {}, "heif": {}, "heic": {}, "apng": {}, "jxl": {},
	"svg": {}, "svgz": {},
	"mp4": {}, "webm": {}, "mov": {}, "mkv": {}, "avi": {}, "m4v": {},
}

// isMediaFormat сообщает, является ли формат (расширение) медиа-файлом,
// который сервис отдаёт клиенту.
func isMediaFormat(f string) bool {
	_, ok := mediaFormats[strings.ToLower(f)]
	return ok
}

// isOriginalRequest сообщает, является ли запрос запросом ОРИГИНАЛА:
// size=x (сохранить исходный размер), без transform и с выходным форматом,
// совпадающим с исходным. Такой запрос отдаётся как есть, без обработки.
func isOriginalRequest(req *asset.Request) bool {
	if req == nil {
		return false
	}
	if req.Transform() != "" {
		return false
	}
	if !req.Size().IsOriginal() {
		return false
	}
	// Выходной формат должен совпадать с исходным (иначе нужна конвертация).
	// Сравнение выполняется по нормализованным строкам, чтобы работало и для
	// видео-форматов (mp4/webm/mov/mkv/avi/m4v), которые processing.ParseFormat
	// не распознаёт (он знает только картинки). Для картинок нормализация
	// через ParseFormat приводит алиасы к каноническому виду (jpg→jpeg,
	// heic→heif, jpegxl→jxl).
	return normalizeFormatForCompare(req.SourceFormat().String()) ==
		normalizeFormatForCompare(req.OutputFormats().String())
}

// normalizeFormatForCompare приводит формат к канонической строке для
// сравнения srcFmt==outFmt. Для картинок использует processing.ParseFormat
// (нормализует алиасы jpg→jpeg и т.п.); для видео-форматов (которые ParseFormat
// не знает) возвращает нижний регистр как есть.
func normalizeFormatForCompare(f string) string {
	lower := strings.ToLower(f)
	if isVideoFormat(lower) {
		return lower
	}
	if parsed, err := processing.ParseFormat(lower); err == nil {
		return parsed.String()
	}
	return lower
}

// serveOriginal — fast-path отдачи ОРИГИНАЛА: открывает исходный файл и
// отдаёт его как есть, без построения плана, без чтения метаданных и без
// обработки процессором.
func (s *Service) serveOriginal(ctx context.Context, key object.ObjectKey, url string, req *asset.Request) (*Result, error) {
	srcKey := s.sourceKey(req)
	src, err := s.deps.Sources.Open(ctx, srcKey)
	if err != nil {
		if object.IsNotFound(err) {
			return nil, outcome(OutcomeNotFound, "source not found", err)
		}
		return nil, s.mapSourceError(ctx, err)
	}
	return &Result{
		Key:       key,
		URL:       url,
		Request:   req,
		Opened:    &artifactStream{art: src},
		FromCache: false,
	}, nil
}

// artifactStream — object.Stream поверх object.Artifact (исходного файла).
// Отдаёт исходник как есть; Close закрывает артефакт.
type artifactStream struct {
	art object.Artifact
}

func (s *artifactStream) Read(p []byte) (int, error) { return s.art.Read(p) }

func (s *artifactStream) Close() error { return s.art.Close() }

func (s *artifactStream) Metadata() object.ObjectMetadata { return s.art.Metadata() }
