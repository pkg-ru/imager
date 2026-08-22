package generatev2

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/coordination/singleflight"
	"github.com/pkg-ru/imager/internal/application/ports/buffer"
	"github.com/pkg-ru/imager/internal/application/ports/coordinator"
	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/object"
	"github.com/pkg-ru/imager/internal/domain/policy"
	"github.com/pkg-ru/imager/internal/domain/processing"
	"github.com/pkg-ru/imager/internal/observability"
)

// Logger — минимальный интерфейс логирования (опционален).
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// nopLogger — заглушка, используемая при отсутствии логгера.
type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}

// errOutputLimit — сигнал превышения лимита размера выходного файла.
var errOutputLimit = errors.New("output exceeds limit")

// errLimitExceeded — сигнал превышения лимита политики (C1).
var errLimitExceeded = errors.New("policy limit exceeded")

// publishRetryBase — начальная задержка экспоненциального backoff при
// retry публикации (I4).
const publishRetryBase = 50 * time.Millisecond

// publishRetryMax — максимальная задержка backoff при retry публикации (I4).
const publishRetryMax = 2 * time.Second

// publishRetryAttempts — максимальное число попыток публикации (I4).
const publishRetryAttempts = 3

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
	// OutputLimit — максимальный размер выходного файла в байтах (0 = без
	// ограничения).
	OutputLimit int64
	// Quality — качество сжатия (0 = по умолчанию).
	Quality int
	// Logger — опциональный логгер.
	Logger Logger
	// Metrics — опциональные метрики (request/cache/processor/storage).
	// Если nil, используется NopMetrics.
	Metrics observability.Metrics
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
}

// New собирает Service и валидирует зависимости.
func New(d Deps) (*Service, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	log := d.Logger
	if log == nil {
		log = nopLogger{}
	}
	metrics := d.Metrics
	if metrics == nil {
		metrics = observability.NopMetrics()
	}
	return &Service{deps: d, log: log, metrics: metrics}, nil
}

// Result — типизированный результат генерации ассета.
type Result struct {
	// Key — канонический cache key (canonical URL), под которым ассет
	// опубликован.
	Key object.ObjectKey
	// URL — каноническая форма URL (без ведущего "/").
	URL string
	// Request — конечный канонический запрос (уже с разрешённым preset).
	Request *asset.Request
	// Opened — готовый к чтению поток ассета (для отдачи клиенту).
	Opened object.Stream
	// FromCache — true, если ассет уже существовал и генерация не выполнялась.
	FromCache bool
}

// Close закрывает Opened, если он есть.
func (r *Result) Close() error {
	if r != nil && r.Opened != nil {
		return r.Opened.Close()
	}
	return nil
}

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

	// Разрешение preset URL в канонический запрос.
	if req.IsPreset() {
		resolved, err := s.deps.Presets.Resolve(req)
		if err != nil {
			return nil, outcome(OutcomeInvalid, "resolve preset", err)
		}
		req = resolved
	}

	// Policy decision (deny-by-default).
	dec := s.deps.Policy.Authorize(req)
	if !dec.Allowed {
		return nil, outcome(OutcomeForbidden, "policy: "+string(dec.Reason), nil)
	}

	// Канонический cache key — сам canonical URL (без хеширования). Preset
	// уже раскрыт выше, поэтому ключ совпадает с конечным запросом, а
	// закэшированный ассет доступен по человекочитаемому имени.
	url, _, err := asset.NewCanonicalizer().CanonicalizeURL(req)
	if err != nil {
		return nil, outcome(OutcomeInvalid, "canonicalize url", err)
	}
	key := object.ObjectKey(url)

	// Fast-path cache lookup (без гонки Exists+Open).
	if st, ok, err := s.tryCache(ctx, key); err != nil {
		return nil, err
	} else if ok {
		s.metrics.IncCacheHit()
		return &Result{Key: key, URL: url, Request: req, Opened: st, FromCache: true}, nil
	}
	s.metrics.IncCacheMiss()

	// Keyed singleflight: concurrent запросы с тем же ключом дедуплицируются.
	// generateLocked публикует результат в кэш ДО возврата, поэтому после
	// Coordinator.Do каждый запрос (включая владельца) читает собственный
	// поток из кэша. Это исключает гонку за преждевременное освобождение
	// общего буфера singleflight (cache stampede regression).
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
	// Результат уже опубликован в кэш — закрываем общий буфер и читаем
	// из кэша. Каждый запрос получает собственный буфер/reader, без гонки
	// за общий ресурс.
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

	// Поиск и открытие источника одним round-trip: Open возвращает
	// ErrNotFound, если объект отсутствует, поэтому предварительный Lookup
	// (дополнительный сетевой запрос для remote-хранилищ) не нужен.
	srcKey := s.sourceKey(req)
	openStart := time.Now()
	src, err := s.deps.Sources.Open(ctx, srcKey)
	if err != nil {
		s.metrics.IncStorageOp(observability.OpSourceOpen, true)
		s.metrics.ObserveStorageDuration(observability.OpSourceOpen, true, time.Since(openStart))
		if object.IsNotFound(err) {
			return nil, outcome(OutcomeNotFound, "source not found", err)
		}
		return nil, s.mapSourceError(ctx, err)
	}
	s.metrics.IncStorageOp(observability.OpSourceOpen, false)
	s.metrics.ObserveStorageDuration(observability.OpSourceOpen, false, time.Since(openStart))
	defer src.Close()

	// План обработки.
	plan, err := s.buildPlan(req)
	if err != nil {
		return nil, outcome(OutcomeInvalid, "build processing plan", err)
	}

	// C1: проверка лимитов политики ДО обработки. Размер источника берём из
	// метаданных открытого объекта (без дополнительного round-trip). Размеры
	// и DPR — из запроса (уже с учётом DPR-умножения в buildPlan).
	meta := src.Metadata()
	var w, h int
	if !plan.Size.Original {
		w, h = plan.Size.Width, plan.Size.Height
	}
	check := s.deps.Policy.CheckLimits(req.Path(), meta.Size, w, h, req.DPR().Int(), 0, 0, 0)
	if check.Exceeded() {
		return nil, outcome(OutcomeForbidden, "policy limit: "+check.ExceededLimit, errLimitExceeded)
	}

	// Обработка в Buffer + параллельная публикация в remote.
	buf, err := s.processAndPublish(ctx, key, processor.Input{Source: src, Plan: plan})
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

// buildPlan преобразует канонический запрос в валидированный план обработки.
//
// Quality/frames/duration/loop берутся из запроса (заполняются при
// разрешении пресета); если не заданы (0/nil), используются значения по
// умолчанию из Deps (Quality) или остаются без ограничения.
func (s *Service) buildPlan(req *asset.Request) (*processing.ProcessingPlan, error) {
	var op processing.Operation
	switch req.Transform() {
	case asset.TransformCrop:
		op = processing.OpCrop
	case asset.TransformTrim:
		op = processing.OpTrim
	case asset.TransformCropTrim:
		op = processing.OpCropTrim
	default:
		op = processing.OpResize
	}
	srcFmt, err := processing.ParseFormat(req.SourceFormat().String())
	if err != nil {
		return nil, err
	}
	outFmt, err := processing.ParseFormat(req.OutputFormat().String())
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
	var w, h int
	if req.Size().IsOriginal() {
		// size=x: сохранить исходный размер изображения.
		return processing.NewProcessingPlan(
			op, srcFmt, outFmt,
			processing.Size{Original: true},
			dpr, quality, loop, frames, duration,
		)
	}
	if dw := req.Size().Width(); dw != nil {
		w = dw.Int() * dpr
	}
	if dh := req.Size().Height(); dh != nil {
		h = dh.Int() * dpr
	}
	return processing.NewProcessingPlan(
		op, srcFmt, outFmt,
		processing.Size{Width: w, Height: h},
		dpr, quality, loop, frames, duration,
	)
}

// processAndPublish запускает процессор, который пишет результат в
// spillable Buffer, затем публикует результат в remote через ResultStore.
// Возвращает Buffer, из которого клиент читает результат.
//
// Отдача клиенту идёт из Buffer (куда процессор записал результат), а не из
// remote. Публикация выполняется после завершения записи в буфер, чтобы
// reader буфера прочитал полные данные. Повторный запрос (singleflight)
// ждёт и генерацию, и публикацию (полная готовность remote).
func (s *Service) processAndPublish(ctx context.Context, key object.ObjectKey, in processor.Input) (buffer.Buffer, error) {
	buf, err := s.newBuffer()
	if err != nil {
		return nil, outcome(OutcomeProcessing, "create buffer", err)
	}

	procStart := time.Now()
	_, procErr := s.deps.Processor.Process(ctx, in, buf)
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
		return nil, outcome(OutcomeProcessing, "process image", procErr)
	}
	if buf.Size() > s.deps.OutputLimit && s.deps.OutputLimit > 0 {
		s.metrics.IncProcessorError()
		s.metrics.ObserveProcessorDuration(time.Since(procStart))
		_ = buf.Close()
		return nil, outcome(OutcomeQuota, "output exceeds limit", errOutputLimit)
	}
	s.metrics.IncProcessorSuccess()
	s.metrics.ObserveProcessorDuration(time.Since(procStart))

	// C1: post-check лимитов политики (outputBytes). Frames/duration для
	// статичных изображений не определяются на application-уровне — они
	// контролируются ImageMagick resource limits (list-length/time).
	check := s.deps.Policy.CheckLimits(in.Plan.SourceFormat.String(), 0, 0, 0, 0, 0, buf.Size(), 0)
	if check.Exceeded() {
		_ = buf.Close()
		return nil, outcome(OutcomeForbidden, "policy limit: "+check.ExceededLimit, errLimitExceeded)
	}

	// Публикация в remote после завершения записи в буфер. I4: transient
	// ошибки (ErrUnavailable) ретраятся с экспоненциальным backoff.
	pubStart := time.Now()
	pubErr := s.publishFromBuffer(ctx, key, buf)
	if pubErr != nil {
		s.metrics.IncStorageOp(observability.OpResultPublish, true)
		s.metrics.ObserveStorageDuration(observability.OpResultPublish, true, time.Since(pubStart))
		_ = buf.Close()
		if ctx.Err() != nil {
			return nil, outcome(OutcomeCanceled, "canceled", ctx.Err())
		}
		return nil, s.mapPublishError(ctx, pubErr)
	}
	s.metrics.IncStorageOp(observability.OpResultPublish, false)
	s.metrics.ObserveStorageDuration(observability.OpResultPublish, false, time.Since(pubStart))
	return buf, nil
}

// publishFromBuffer публикует содержимое буфера в remote. Читает из буфера
// по мере записи процессором (параллельно), ограничивая размер OutputLimit.
//
// I4: transient-ошибки (ErrUnavailable) ретраятся с экспоненциальным backoff.
// I12: boundedReader не читает лишний байт — лимит проверяется ДО чтения,
// поэтому при превышении в remote не попадает битый объект.
func (s *Service) publishFromBuffer(ctx context.Context, key object.ObjectKey, buf buffer.Buffer) error {
	reader, err := buf.NewReader()
	if err != nil {
		return err
	}
	defer reader.Close()

	var r io.Reader = reader
	if s.deps.OutputLimit > 0 {
		r = &boundedReader{r: reader, max: s.deps.OutputLimit}
	}

	var lastErr error
	for attempt := 0; attempt < publishRetryAttempts; attempt++ {
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
		// Каждая попытка читает буфер с начала (reader перематываем).
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return err
		}
		lastErr = s.deps.Results.Publish(ctx, key, r, object.PublishOptions{})
		if lastErr == nil {
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

// boundedReader ограничивает чтение max байт и сигнализирует о превышении
// через errOutputLimit. Лимит проверяется ДО чтения: лишний байт не
// читается и не передаётся в Publish (I12).
type boundedReader struct {
	r    io.Reader
	max  int64
	read int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.read >= b.max {
		return 0, errOutputLimit
	}
	if int64(len(p)) > b.max-b.read {
		p = p[:b.max-b.read]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	return n, err
}

// mapResultError маппит ошибку ResultStore в типизированный OutcomeError.
func (s *Service) mapResultError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return outcome(OutcomeCanceled, "canceled", ctx.Err())
	}
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
}

// mapSourceError маппит ошибку SourceStore в типизированный OutcomeError.
func (s *Service) mapSourceError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return outcome(OutcomeCanceled, "canceled", ctx.Err())
	}
	switch {
	case object.IsNotFound(err):
		return outcome(OutcomeNotFound, "source not found", err)
	case object.IsUnavailable(err):
		return outcome(OutcomeUnavailable, "source store unavailable", err)
	default:
		return outcome(OutcomeProcessing, "source store error", err)
	}
}

// mapPublishError маппит ошибку публикации в типизированный OutcomeError.
func (s *Service) mapPublishError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return outcome(OutcomeCanceled, "canceled", ctx.Err())
	}
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
}

// mapCoordinatorError маппит ошибку координатора в типизированный OutcomeError.
// Уже типизированные OutcomeError (например, из generateLocked) пробрасываются
// как есть.
//
// N8: ErrTooManyKeys/ErrKeyTooLong — перегрузка координатора (429/503 с
// Retry-After), а не "unavailable". Маппятся в OutcomeUnavailable с явной
// причиной, чтобы HTTP-слой мог вернуть Retry-After.
func (s *Service) mapCoordinatorError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return outcome(OutcomeCanceled, "canceled", ctx.Err())
	}
	var oe *OutcomeError
	if errors.As(err, &oe) {
		return oe
	}
	if errors.Is(err, singleflight.ErrTooManyKeys) || errors.Is(err, singleflight.ErrKeyTooLong) {
		return outcome(OutcomeUnavailable, "coordination overloaded", err)
	}
	return outcome(OutcomeUnavailable, "coordination unavailable", err)
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

// memBuffer — in-memory реализация buffer.Buffer, используемая, когда
// фабрика spillable-буферов не задана. Хранит данные в памяти процесса.
//
// N2: память освобождается через reference counting — только когда закрыты
// все reader'ы (и сам буфер). В cache stampede один буфер разделяется между
// несколькими запросами (каждый получает собственный reader), поэтому
// обнулять data при первом же Close нельзя: это сломало бы остальных
// читателей. refs считает открытые reader'ы; data обнуляется, когда refs
// достигает 0.
// memChunkSize — размер сегмента chunked-буфера (п.10). Сегментированный
// буфер избегает O(n²) при больших данных: append к одному растущему слайсу
// заменяется добавлением фиксированных сегментов.
const memChunkSize = 32 * 1024

// memBuffer — in-memory реализация buffer.Buffer, используемая, когда
// фабрика spillable-буферов не задана. Хранит данные в памяти процесса.
//
// N2: память освобождается через reference counting — только когда закрыты
// все reader'ы (и сам буфер). В cache stampede один буфер разделяется между
// несколькими запросами (каждый получает собственный reader), поэтому
// обнулять данные при первом же Close нельзя: это сломало бы остальных
// читателей. refs считает открытые reader'ы; данные обнуляются, когда refs
// достигает 0.
//
// П.10: данные хранятся сегментами фиксированного размера (chunked list),
// а не одним растущим слайсом. Это устраняет O(n²) при больших буферах.
type memBuffer struct {
	mu     sync.Mutex
	chunks [][]byte
	size   int
	pos    int
	refs   int
	closed bool
}

func (b *memBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	orig := len(p)
	for len(p) > 0 {
		if len(b.chunks) == 0 || len(b.chunks[len(b.chunks)-1]) == memChunkSize {
			b.chunks = append(b.chunks, make([]byte, 0, memChunkSize))
		}
		last := &b.chunks[len(b.chunks)-1]
		n := memChunkSize - len(*last)
		if n > len(p) {
			n = len(p)
		}
		*last = append(*last, p[:n]...)
		b.size += n
		p = p[n:]
	}
	return orig, nil
}

// readAt копирует данные из сегментов, начиная с позиции off, в p.
// Возвращает число прочитанных байт. Вызывается под mu.
func (b *memBuffer) readAt(p []byte, off int) int {
	if off >= b.size {
		return 0
	}
	total := 0
	// Находим сегмент и смещение внутри него.
	seg := off / memChunkSize
	segOff := off % memChunkSize
	for seg < len(b.chunks) && total < len(p) {
		chunk := b.chunks[seg]
		n := len(chunk) - segOff
		if n <= 0 {
			seg++
			segOff = 0
			continue
		}
		if n > len(p)-total {
			n = len(p) - total
		}
		copy(p[total:total+n], chunk[segOff:segOff+n])
		total += n
		seg++
		segOff = 0
	}
	return total
}

func (b *memBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pos >= b.size {
		return 0, io.EOF
	}
	n := b.readAt(p, b.pos)
	b.pos += n
	return n, nil
}

func (b *memBuffer) Seek(offset int64, whence int) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = int64(b.pos) + offset
	case io.SeekEnd:
		np = int64(b.size) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if np < 0 {
		return 0, errors.New("negative seek position")
	}
	b.pos = int(np)
	return np, nil
}

func (b *memBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	// N2: освобождаем память через reference counting — только когда закрыты
	// все reader'ы. Сам буфер помечается closed, но данные живут, пока есть
	// открытые reader'ы (cache stampede: один буфер, много читателей).
	b.releaseLocked()
	return nil
}

// releaseLocked уменьшает счётчик reader'ов и освобождает память, когда
// буфер закрыт и не осталось открытых reader'ов. Вызывается под mu.
func (b *memBuffer) releaseLocked() {
	if b.closed && b.refs <= 0 {
		b.chunks = nil
		b.size = 0
		b.pos = 0
	}
}

func (b *memBuffer) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(b.size)
}

func (b *memBuffer) NewReader() (io.ReadSeekCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// N2: reader'ы учитываются reference counting. После закрытия буфера
	// новые reader'ы не создаются (данные могут быть освобождены).
	if b.closed {
		return nil, errors.New("buffer closed")
	}
	b.refs++
	return &memBufferReader{buf: b}, nil
}

// memBufferReader — независимый reader поверх memBuffer с собственной
// позицией чтения (для параллельного чтения клиентом и publish).
type memBufferReader struct {
	buf *memBuffer
	pos int
}

func (r *memBufferReader) Read(p []byte) (int, error) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	if r.pos >= r.buf.size {
		return 0, io.EOF
	}
	n := r.buf.readAt(p, r.pos)
	r.pos += n
	return n, nil
}

func (r *memBufferReader) Seek(offset int64, whence int) (int64, error) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = int64(r.pos) + offset
	case io.SeekEnd:
		np = int64(r.buf.size) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if np < 0 {
		return 0, errors.New("negative seek position")
	}
	r.pos = int(np)
	return np, nil
}

func (r *memBufferReader) Close() error {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	// N2: закрытие reader'а уменьшает счётчик; когда буфер закрыт и не
	// осталось reader'ов, память освобождается.
	if r.buf.refs > 0 {
		r.buf.refs--
	}
	r.buf.releaseLocked()
	return nil
}
