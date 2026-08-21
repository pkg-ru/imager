// Package s3 реализует storage.SourceStore и storage.ResultStore поверх
// Amazon S3 (и S3-совместимых хранилищ) через AWS SDK v2.
//
// Безопасность ключей: ключи нормализуются через remote.CanonicalKey
// (запрет "..", обратных слешей, NUL). Секреты не передаются в URI/логи:
// учётные данные задаются отдельными полями конфигурации.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// Options — параметры S3-адаптера.
type Options struct {
	// Bucket — имя bucket.
	Bucket string
	// Prefix — префикс ключей внутри bucket (может быть пустым).
	Prefix string
	// Client — S3-клиент. Если nil, используется NewClient.
	Client *s3.Client
	// SpoolDir — каталог временных spool (пусто = os.TempDir).
	SpoolDir string
	// SpoolMaxBytes — максимальный размер source spool (0 = без лимита).
	SpoolMaxBytes int64
	// Pool — общий бюджет памяти процесса для spillable-буферов.
	// Если nil, буферы работают только через память без spill.
	Pool *remote.BufferPool
}

// NewClient создаёт S3-клиент из AWS config.
func NewClient(ctx context.Context, cfg aws.Config) *s3.Client {
	return s3.NewFromConfig(cfg)
}

func (o Options) validate() error {
	if o.Bucket == "" {
		return fmt.Errorf("s3: empty bucket")
	}
	return nil
}

// isNotFound сообщает, является ли ошибка S3 признаком отсутствия объекта
// (код "NotFound" / "NoSuchKey").
func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
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

func (o Options) head(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	full, err := o.key(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	out, err := o.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(o.Bucket),
		Key:    aws.String(full),
	})
	if err != nil {
		if isNotFound(err) {
			return object.ObjectMetadata{}, remote.NotFound(key)
		}
		return object.ObjectMetadata{}, remote.MapError("s3 head", err)
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
	return meta, nil
}

func (o Options) get(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, io.ReadCloser, error) {
	full, err := o.key(key)
	if err != nil {
		return object.ObjectMetadata{}, nil, err
	}
	out, err := o.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.Bucket),
		Key:    aws.String(full),
	})
	if err != nil {
		if isNotFound(err) {
			return object.ObjectMetadata{}, nil, remote.NotFound(key)
		}
		return object.ObjectMetadata{}, nil, remote.MapError("s3 get", err)
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
	return meta, out.Body, nil
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
	return &SourceStore{opts: opts}, nil
}

// Lookup возвращает метаданные исходного объекта.
func (s *SourceStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return s.opts.head(ctx, key)
}

// Open открывает поток исходного объекта через spillable буфер.
func (s *SourceStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	meta, body, err := s.opts.get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	buf, err := remote.NewBuffer(remote.BufferOptions{
		Pool:     s.opts.Pool,
		Dir:      s.opts.SpoolDir,
		MaxBytes: s.opts.SpoolMaxBytes,
	})
	if err != nil {
		return nil, remote.MapError("s3 buffer", err)
	}
	if _, err := buf.WriteFrom(body, s.opts.SpoolMaxBytes); err != nil {
		_ = buf.Close()
		if errors.Is(err, remote.ErrBufferLimit) {
			return nil, fmt.Errorf("s3: source %q exceeds spool limit: %w", key, object.ErrQuota)
		}
		return nil, remote.MapError("s3 buffer", err)
	}
	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		_ = buf.Close()
		return nil, remote.MapError("s3 buffer seek", err)
	}
	return remote.NewBufferArtifact(buf, meta), nil
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
	return &ResultStore{opts: opts}, nil
}

// Lookup возвращает метаданные результата.
func (r *ResultStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return r.opts.head(ctx, key)
}

// Open открывает перематываемый поток результата через spillable буфер.
func (r *ResultStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	meta, body, err := r.opts.get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	buf, err := remote.NewBuffer(remote.BufferOptions{
		Pool:     r.opts.Pool,
		Dir:      r.opts.SpoolDir,
		MaxBytes: r.opts.SpoolMaxBytes,
	})
	if err != nil {
		return nil, remote.MapError("s3 buffer", err)
	}
	if _, err := buf.WriteFrom(body, r.opts.SpoolMaxBytes); err != nil {
		_ = buf.Close()
		if errors.Is(err, remote.ErrBufferLimit) {
			return nil, fmt.Errorf("s3: result %q exceeds spool limit: %w", key, object.ErrQuota)
		}
		return nil, remote.MapError("s3 buffer", err)
	}
	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		_ = buf.Close()
		return nil, remote.MapError("s3 buffer seek", err)
	}
	return remote.NewBufferArtifact(buf, meta), nil
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
// через conditional PUT (If-None-Match: "*").
func (r *ResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	in := &s3.PutObjectInput{
		Bucket:      aws.String(r.opts.Bucket),
		Key:         aws.String(full),
		Body:        src,
		ContentType: aws.String(opts.ContentType),
	}
	if opts.CacheControl != "" {
		in.CacheControl = aws.String(opts.CacheControl)
	}
	if opts.NoOverwrite {
		in.IfNoneMatch = aws.String("*")
	}
	_, err = r.opts.Client.PutObject(ctx, in)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
			return remote.Conflict(key)
		}
		return remote.MapError("s3 put", err)
	}
	return nil
}

// Delete удаляет объект. Идемпотентно.
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
		return remote.MapError("s3 delete", err)
	}
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
	for {
		out, err := r.opts.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(r.opts.Bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return object.StoreStats{}, remote.MapError("s3 list", err)
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
		token = out.NextContinuationToken
	}
	return stats, nil
}

var _ storage.ResultStore = (*ResultStore)(nil)

// ModTime — вспомогательная функция для тестов.
func ModTime(t time.Time) *time.Time { return &t }
