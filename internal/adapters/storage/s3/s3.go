// Package s3 реализует storage.SourceStore и storage.ResultStore поверх
// Amazon S3 (и S3-совместимых хранилищ) через AWS SDK v2.
//
// Безопасность ключей: ключи нормализуются через remote.CanonicalKey
// (запрет "..", обратных слешей, NUL). Секреты не передаются в URI/логи:
// учётные данные задаются отдельными полями конфигурации.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/pkg-ru/imager/internal/adapters/lru"
	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// multipartPartSize — размер одной части multipart upload (5 МБ минимум S3).
const multipartPartSize int64 = 5 * 1024 * 1024

// multipartMaxParts — максимальное число частей (S3 лимит 10000).
const multipartMaxParts = 10000

// multipartWorkers — число параллельных UploadPart воркеров.
const multipartWorkers = 8

// maxListPages — предохранитель от бесконечной пагинации в Stats.
const maxListPages = 10000

// DefaultMetadataTTL — TTL кэша метаданных по умолчанию (30 секунд).
const DefaultMetadataTTL = 30 * time.Second

// Options — параметры S3-адаптера.
type Options struct {
	// Bucket — имя bucket.
	Bucket string
	// Prefix — префикс ключей внутри bucket (может быть пустым).
	// Нормализуется в validate(): завершающий "/" обрезается.
	Prefix string
	// Client — S3-клиент. Обязателен (см. NewSourceStore/NewResultStore).
	Client *s3.Client
	// SpoolDir — каталог временных spool (пусто = os.TempDir).
	SpoolDir string
	// SpoolMaxBytes — максимальный размер source spool (0 = без лимита).
	SpoolMaxBytes int64
	// Pool — общий бюджет памяти процесса для spillable-буферов.
	// Если nil, буферы работают только через память без spill.
	Pool *remote.BufferPool
	// MetadataTTL — TTL кэша метаданных (0 = кэш отключён).
	MetadataTTL time.Duration
	// cache — in-memory TTL-кэш метаданных (инициализируется в
	// NewSourceStore/NewResultStore; nil = кэш отключён).
	cache *metadataCache
}

func (o *Options) validate() error {
	if o.Bucket == "" {
		return fmt.Errorf("s3: empty bucket")
	}
	// Нормализация префикса: обрезаем завершающий "/", чтобы ключи не
	// получались с "//" (см. key()).
	o.Prefix = trimSlashes(o.Prefix)
	return nil
}

// trimSlashes обрезает ведущие и завершающие "/" у префикса.
func trimSlashes(s string) string {
	start, end := 0, len(s)
	for start < end && s[start] == '/' {
		start++
	}
	for end > start && s[end-1] == '/' {
		end--
	}
	return s[start:end]
}

// isNotFound сообщает, является ли ошибка S3 признаком отсутствия объекта
// (код "NotFound" / "NoSuchKey" или HTTP 404).
func isNotFound(err error) bool {
	if httpStatus(err) == 404 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
}

// isForbidden сообщает, является ли ошибка S3 признаком запрета доступа
// (код "AccessDenied" или HTTP 403).
func isForbidden(err error) bool {
	if httpStatus(err) == 403 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "AccessForbidden", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return true
		}
	}
	return false
}

// isThrottled сообщает, является ли ошибка признаком троттлинга (HTTP 429
// или код SlowDown/Throttling).
func isThrottled(err error) bool {
	if httpStatus(err) == 429 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "SlowDown", "Throttling", "TooManyRequests", "RequestLimitExceeded":
			return true
		}
	}
	return false
}

// isServerError сообщает, является ли ошибка серверной (HTTP 5xx).
func isServerError(err error) bool {
	code := httpStatus(err)
	return code >= 500 && code <= 599
}

// httpStatus извлекает HTTP status code из ошибки S3 (если доступен),
// иначе 0.
func httpStatus(err error) int {
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		return respErr.HTTPStatusCode()
	}
	return 0
}

// MapError маппит ошибку S3 в типизированную ошибку domain/object.
// Различает 403 (ErrForbidden), 429/5xx (ErrUnavailable с пометкой для
// ретраев), 404 (ErrNotFound) и прочие (ErrUnavailable).
func MapError(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case isNotFound(err):
		return remote.MapError(op, remote.NotFound(object.ObjectKey("")))
	case isForbidden(err):
		return fmt.Errorf("%s: %w", op, object.ErrForbidden)
	case isThrottled(err), isServerError(err):
		return remote.MapError(op, err)
	default:
		return remote.MapError(op, err)
	}
}

func (o Options) key(key object.ObjectKey) (string, error) {
	k, err := remote.CanonicalKey(key)
	if err != nil {
		return "", remote.Unsafe(key, err)
	}
	if o.Prefix == "" {
		return k, nil
	}
	return o.Prefix + "/" + k, nil
}

// metadataCache — потокобезопасный in-memory TTL-кэш метаданных объектов.
// Ключ — полный S3-ключ, значение — метаданные + время записи.
// Ограничен по числу ключей (LRU), чтобы не расти безгранично при шквале
// уникальных ключей. LRU-поведение (touch/evict) делегировано generic-пакету
// adapters/lru; TTL-логика реализована тонкой обёрткой поверх него.
type metadataCache struct {
	ttl time.Duration
	lru *lru.Cache[string, cacheEntry]
}

type cacheEntry struct {
	meta object.ObjectMetadata
	at   time.Time
}

func newMetadataCache(ttl time.Duration) *metadataCache {
	if ttl <= 0 {
		return nil
	}
	return &metadataCache{
		ttl: ttl,
		lru: lru.New[string, cacheEntry](10000),
	}
}

func (c *metadataCache) get(full string) (object.ObjectMetadata, bool) {
	if c == nil {
		return object.ObjectMetadata{}, false
	}
	e, ok := c.lru.Get(full)
	if !ok {
		return object.ObjectMetadata{}, false
	}
	if time.Since(e.at) > c.ttl {
		c.lru.Delete(full)
		return object.ObjectMetadata{}, false
	}
	return e.meta, true
}

func (c *metadataCache) put(full string, meta object.ObjectMetadata) {
	if c == nil {
		return
	}
	c.lru.Set(full, cacheEntry{meta: meta, at: time.Now()})
}

func (c *metadataCache) invalidate(full string) {
	if c == nil {
		return
	}
	c.lru.Delete(full)
}

func (o Options) head(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	full, err := o.key(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	// Кэш метаданных: если запись свежая — возвращаем без round-trip.
	if cached, ok := o.cache.get(full); ok {
		return cached, nil
	}
	out, err := o.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(o.Bucket),
		Key:    aws.String(full),
	})
	if err != nil {
		if isNotFound(err) {
			return object.ObjectMetadata{}, remote.NotFound(key)
		}
		return object.ObjectMetadata{}, MapError("s3 head", err)
	}
	meta := object.ObjectMetadata{Key: key}
	if out.ContentLength != nil {
		meta.Size = *out.ContentLength
	}
	if out.LastModified != nil {
		meta.ModTime = *out.LastModified
	}
	if out.ContentType != nil {
		meta.ContentType = *out.ContentType
	}
	if out.ETag != nil {
		meta.ETag = *out.ETag
	}
	o.cache.put(full, meta)
	return meta, nil
}

func (o Options) get(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, io.ReadCloser, error) {
	full, err := o.key(key)
	if err != nil {
		return object.ObjectMetadata{}, nil, err
	}
	// Conditional GET по сохранённому ETag: если объект не менялся,
	// бэкенд вернёт 304 Not Modified без тела.
	in := &s3.GetObjectInput{
		Bucket: aws.String(o.Bucket),
		Key:    aws.String(full),
	}
	if meta, ok := o.cache.get(full); ok && meta.ETag != "" {
		in.IfNoneMatch = aws.String(meta.ETag)
	}
	out, err := o.Client.GetObject(ctx, in)
	if err != nil {
		if isNotFound(err) {
			return object.ObjectMetadata{}, nil, remote.NotFound(key)
		}
		return object.ObjectMetadata{}, nil, MapError("s3 get", err)
	}
	meta := object.ObjectMetadata{Key: key}
	if out.ContentLength != nil {
		meta.Size = *out.ContentLength
	}
	if out.LastModified != nil {
		meta.ModTime = *out.LastModified
	}
	if out.ContentType != nil {
		meta.ContentType = *out.ContentType
	}
	if out.ETag != nil {
		meta.ETag = *out.ETag
	}
	o.cache.put(full, meta)
	return meta, out.Body, nil
}

// openBuffer — общий helper для Open (Source и Result): скачивает объект в
// spillable буфер и возвращает перематываемый Artifact.
func (o Options) openBuffer(ctx context.Context, key object.ObjectKey, role string) (object.Artifact, error) {
	meta, body, err := o.get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	buf, err := remote.NewBuffer(remote.BufferOptions{
		Pool:     o.Pool,
		Dir:      o.SpoolDir,
		MaxBytes: o.SpoolMaxBytes,
	})
	if err != nil {
		return nil, remote.MapError("s3 buffer", err)
	}
	if _, err := buf.WriteFrom(body, o.SpoolMaxBytes); err != nil {
		_ = buf.Close()
		if errors.Is(err, remote.ErrBufferLimit) {
			return nil, fmt.Errorf("s3: %s %q exceeds spool limit: %w", role, key, object.ErrQuota)
		}
		return nil, remote.MapError("s3 buffer", err)
	}
	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		_ = buf.Close()
		return nil, remote.MapError("s3 buffer seek", err)
	}
	return remote.NewBufferArtifact(buf, meta), nil
}

// SourceStore — S3-реализация storage.SourceStore (read-only).
type SourceStore struct {
	opts Options
}

// NewSourceStore создаёт S3 SourceStore.
func NewSourceStore(opts Options) (*SourceStore, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.Client == nil {
		return nil, fmt.Errorf("s3: source store: client is required")
	}
	opts.cache = newMetadataCache(opts.MetadataTTL)
	return &SourceStore{opts: opts}, nil
}

// Lookup возвращает метаданные исходного объекта.
func (s *SourceStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return s.opts.head(ctx, key)
}

// Open открывает поток исходного объекта через spillable буфер.
func (s *SourceStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	return s.opts.openBuffer(ctx, key, "source")
}

var _ storage.SourceStore = (*SourceStore)(nil)

// ResultStore — S3-реализация storage.ResultStore.
type ResultStore struct {
	opts Options
}

// NewResultStore создаёт S3 ResultStore.
func NewResultStore(opts Options) (*ResultStore, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.Client == nil {
		return nil, fmt.Errorf("s3: result store: client is required")
	}
	opts.cache = newMetadataCache(opts.MetadataTTL)
	return &ResultStore{opts: opts}, nil
}

// Lookup возвращает метаданные результата.
func (r *ResultStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return r.opts.head(ctx, key)
}

// Open открывает перематываемый поток результата через spillable буфер.
func (r *ResultStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	return r.opts.openBuffer(ctx, key, "result")
}

// ReadStream открывает одноразовый поток результата напрямую из S3 без
// материализации. Соединение/поток удерживается до закрытия Stream.
func (r *ResultStore) ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error) {
	meta, body, err := r.opts.get(ctx, key)
	if err != nil {
		return nil, err
	}
	return remote.NewStreamArtifact(body, body, meta), nil
}

// Publish полностью завершает upload до возврата. NoOverwrite реализуется
// через conditional PUT (If-None-Match: "*"). Для крупных объектов (>100 МБ)
// используется multipart upload с параллельными частями.
func (r *ResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}

	// Определяем размер источника. Если известен и превышает порог —
	// multipart upload. Иначе — обычный PUT.
	size, known := readerSize(src)
	if known && size > multipartPartSize {
		return r.publishMultipart(ctx, full, src, size, opts)
	}

	in := &s3.PutObjectInput{
		Bucket:            aws.String(r.opts.Bucket),
		Key:               aws.String(full),
		Body:              src,
		ContentType:       aws.String(opts.ContentType),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
	}
	if known {
		in.ContentLength = aws.Int64(size)
	}
	if opts.CacheControl != "" {
		in.CacheControl = aws.String(opts.CacheControl)
	}
	if opts.NoOverwrite {
		in.IfNoneMatch = aws.String("*")
	}
	_, err = r.opts.Client.PutObject(ctx, in)
	if err != nil {
		if isPreconditionFailed(err) {
			return remote.Conflict(key)
		}
		// Fallback для S3-совместимых, не поддерживающих If-None-Match:
		// если conditional PUT упал не по PreconditionFailed, проверяем
		// существование объекта через HeadObject (с учётом гонки).
		if opts.NoOverwrite && !isNotFound(err) && !isForbidden(err) {
			if _, herr := r.opts.Client.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket: aws.String(r.opts.Bucket),
				Key:    aws.String(full),
			}); herr == nil {
				return remote.Conflict(key)
			}
		}
		return MapError("s3 put", err)
	}
	r.opts.cache.invalidate(full)
	return nil
}

// partData — нарезанная часть multipart upload'а, читаемая из src строго в
// одной горутине (продюсер). Данные передаются воркерам по каналу, поэтому
// не-потокобезопасный src никогда не читается конкурентно (К1).
type partData struct {
	num  int
	data []byte
}

// publishMultipart загружает объект через multipart upload с параллельными
// частями. size должен быть известен заранее.
//
// К1 (race fix): src не обязан быть потокобезопасным (например,
// remote.BufferReader при output-limit: 0). Поэтому чтение src сериализуется:
// продюсерская горутина последовательно нарезает парты в память (по partSize),
// а пул воркеров конкурентно загружает готовые байты через bytes.NewReader.
// Память ограничена буфером канала (multipartWorkers партов в полёте).
// Ошибка любой части прерывает продюсера и приводит к abort multipart.
func (r *ResultStore) publishMultipart(ctx context.Context, full string, src io.Reader, size int64, opts object.PublishOptions) error {
	parts := int((size + multipartPartSize - 1) / multipartPartSize)
	if parts > multipartMaxParts {
		// Слишком много частей для фиксированного размера — увеличиваем
		// размер части до минимально допустимого числа (10000).
		parts = multipartMaxParts
	}
	partSize := (size + int64(parts) - 1) / int64(parts)
	if partSize < multipartPartSize {
		partSize = multipartPartSize
	}

	create := &s3.CreateMultipartUploadInput{
		Bucket:            aws.String(r.opts.Bucket),
		Key:               aws.String(full),
		ContentType:       aws.String(opts.ContentType),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
	}
	if opts.CacheControl != "" {
		create.CacheControl = aws.String(opts.CacheControl)
	}
	created, err := r.opts.Client.CreateMultipartUpload(ctx, create)
	if err != nil {
		return MapError("s3 create multipart", err)
	}
	uploadID := *created.UploadId

	completed := make([]types.CompletedPart, parts)

	// Продюсер: последовательное чтение src (строго одна горутина).
	partCh := make(chan partData, multipartWorkers)
	stopProduce := make(chan struct{})
	var firstErr error
	var errOnce sync.Once
	var abort atomic.Bool
	setErr := func(e error) {
		errOnce.Do(func() {
			firstErr = e
			abort.Store(true)
			close(stopProduce)
		})
	}

	var produceWg sync.WaitGroup
	produceWg.Add(1)
	go func() {
		defer produceWg.Done()
		defer close(partCh)
		for idx := 0; idx < parts; idx++ {
			start := int64(idx) * partSize
			length := partSize
			if start+length > size {
				length = size - start
			}
			buf := make([]byte, length)
			if _, err := io.ReadFull(src, buf); err != nil {
				setErr(fmt.Errorf("s3: read part %d: %w", idx+1, err))
				return
			}
			// Проверяем отмену контекста между партами.
			if err := ctx.Err(); err != nil {
				setErr(err)
				return
			}
			select {
			case partCh <- partData{num: idx + 1, data: buf}:
			case <-stopProduce:
				return
			}
		}
	}()

	// Воркеры: конкурентная загрузка готовых партов из памяти.
	var uploadWg sync.WaitGroup
	for i := 0; i < multipartWorkers; i++ {
		uploadWg.Add(1)
		go func() {
			defer uploadWg.Done()
			for p := range partCh {
				// При уже произошедшей ошибке продолжаем потреблять канал,
				// чтобы не заблокировать продюсера, но не выполняем загрузку.
				if abort.Load() {
					continue
				}
				out, perr := r.opts.Client.UploadPart(ctx, &s3.UploadPartInput{
					Bucket:            aws.String(r.opts.Bucket),
					Key:               aws.String(full),
					UploadId:          aws.String(uploadID),
					PartNumber:        aws.Int32(int32(p.num)),
					Body:              bytes.NewReader(p.data),
					ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
				})
				if perr != nil {
					setErr(perr)
					continue
				}
				cp := types.CompletedPart{PartNumber: aws.Int32(int32(p.num))}
				if out.ETag != nil {
					cp.ETag = aws.String(*out.ETag)
				}
				// Разные индексы пишутся разными воркерами — гонки нет.
				completed[p.num-1] = cp
			}
		}()
	}
	uploadWg.Wait()
	produceWg.Wait()

	// При ошибке (чтение или загрузка любой части) — abort multipart upload.
	if firstErr != nil {
		abortCtx, abortCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer abortCancel()
		_, _ = r.opts.Client.AbortMultipartUpload(abortCtx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(r.opts.Bucket),
			Key:      aws.String(full),
			UploadId: aws.String(uploadID),
		})
		if errors.Is(firstErr, context.Canceled) || errors.Is(firstErr, context.DeadlineExceeded) {
			return firstErr
		}
		return MapError("s3 upload part", firstErr)
	}

	// CompleteMultipartUpload собирает объект из частей.
	_, err = r.opts.Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(r.opts.Bucket),
		Key:      aws.String(full),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completed,
		},
	})
	if err != nil {
		_, _ = r.opts.Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(r.opts.Bucket),
			Key:      aws.String(full),
			UploadId: aws.String(uploadID),
		})
		return MapError("s3 complete multipart", err)
	}
	r.opts.cache.invalidate(full)
	return nil
}

// readerSize определяет размер reader, если он известен:
//   - io.Seeker — через Seek(0, End);
//   - *bytes.Reader / *bytes.Buffer — через Len().
//
// Возвращает (size, true) если размер известен, иначе (0, false).
func readerSize(r io.Reader) (int64, bool) {
	switch r := r.(type) {
	case *bytes.Reader:
		return int64(r.Len()), true
	case *bytes.Buffer:
		return int64(r.Len()), true
	}
	if s, ok := r.(io.Seeker); ok {
		cur, err := s.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, false
		}
		end, err := s.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, false
		}
		_, _ = s.Seek(cur, io.SeekStart)
		return end, true
	}
	return 0, false
}

// isPreconditionFailed сообщает, является ли ошибка PreconditionFailed
// (HTTP 412).
func isPreconditionFailed(err error) bool {
	if httpStatus(err) == 412 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "PreconditionFailed"
	}
	return false
}

// Delete удаляет объект. Идемпотентно: 404 считается успехом.
func (r *ResultStore) Delete(ctx context.Context, key object.ObjectKey) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	_, err = r.opts.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.opts.Bucket),
		Key:    aws.String(full),
	})
	if err != nil {
		if isNotFound(err) {
			// Удаление отсутствующего объекта — не ошибка (идемпотентность).
			r.opts.cache.invalidate(full)
			return nil
		}
		return MapError("s3 delete", err)
	}
	r.opts.cache.invalidate(full)
	return nil
}

// Stats возвращает агрегированную статистику по префиксу.
func (r *ResultStore) Stats(ctx context.Context) (object.StoreStats, error) {
	var stats object.StoreStats
	prefix := r.opts.Prefix
	if prefix != "" {
		prefix += "/"
	}
	var token *string
	for page := 0; ; page++ {
		if page >= maxListPages {
			return object.StoreStats{}, fmt.Errorf("s3: list exceeded %d pages (possible infinite pagination)", maxListPages)
		}
		out, err := r.opts.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(r.opts.Bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return object.StoreStats{}, MapError("s3 list", err)
		}
		for _, obj := range out.Contents {
			stats.Objects++
			if obj.Size != nil {
				stats.TotalBytes += *obj.Size
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		// Защита от бесконечного цикла: если бэкенд вернул IsTruncated=true
		// без NextContinuationToken — прерываем с ошибкой.
		if out.NextContinuationToken == nil {
			return object.StoreStats{}, fmt.Errorf("s3: list truncated without continuation token")
		}
		token = out.NextContinuationToken
	}
	return stats, nil
}

// List возвращает ключи результатов, начинающиеся с prefix. Реализует
// опциональный storage.Lister (используется admin DELETE по исходнику).
//
// Перечисление выполняется через ListObjectsV2 с пагинацией (защита от
// бесконечного цикла — maxListPages). Возвращаемые ключи — canonical
// (без префикса bucket-конфигурации).
func (r *ResultStore) List(ctx context.Context, prefix object.ObjectKey) ([]object.ObjectKey, error) {
	pre := string(prefix)
	pre = trimSlashes(pre)
	fullPrefix := pre
	if r.opts.Prefix != "" {
		if fullPrefix != "" {
			fullPrefix = r.opts.Prefix + "/" + fullPrefix
		} else {
			fullPrefix = r.opts.Prefix
		}
	}
	var keys []object.ObjectKey
	var token *string
	for page := 0; ; page++ {
		if page >= maxListPages {
			return nil, fmt.Errorf("s3: list exceeded %d pages (possible infinite pagination)", maxListPages)
		}
		out, err := r.opts.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(r.opts.Bucket),
			Prefix:            aws.String(fullPrefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, MapError("s3 list", err)
		}
		for _, obj := range out.Contents {
			if obj.Key == nil {
				continue
			}
			key := *obj.Key
			// Срезаем префикс bucket-конфигурации, чтобы вернуть canonical ключ.
			if r.opts.Prefix != "" {
				key = strings.TrimPrefix(key, r.opts.Prefix+"/")
			}
			keys = append(keys, object.ObjectKey(key))
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		if out.NextContinuationToken == nil {
			return nil, fmt.Errorf("s3: list truncated without continuation token")
		}
		token = out.NextContinuationToken
	}
	return keys, nil
}

var _ storage.ResultStore = (*ResultStore)(nil)
