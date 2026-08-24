package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// fakeS3Handler — минимальный in-memory S3 backend для тестов.
type fakeS3Handler struct {
	mu      sync.Mutex
	objects map[string][]byte
	// multipart — незавершённые multipart uploads: uploadID -> (part -> data).
	multipart map[string]map[int][]byte
	// abortCount — число вызовов AbortMultipartUpload (для тестов).
	abortCount int
	// preconditionFailed — если true, PutObject с IfNoneMatch возвращает
	// PreconditionFailed при существующем объекте.
	preconditionFailed bool
	// failStatus — если >0, все операции возвращают ошибку с этим HTTP
	// status code (для тестов маппинга ошибок).
	failStatus int
	// failUploadPart — если true, только UploadPart возвращает ошибку 500
	// (для теста abort multipart).
	failUploadPart bool
	// truncateWithoutToken — если true, ListObjectsV2 возвращает
	// IsTruncated=true без NextContinuationToken (для теста пагинации).
	truncateWithoutToken bool
	// listPages — число страниц, возвращаемых ListObjectsV2 до завершения.
	listPages int
}

func newFakeS3Handler() *fakeS3Handler {
	return &fakeS3Handler{objects: map[string][]byte{}}
}

// client создаёт s3.Client с middleware, перехватывающим запросы.
func (f *fakeS3Handler) client() *s3.Client {
	cfg := aws.Config{Region: "us-east-1"}
	cfg.APIOptions = append(cfg.APIOptions, func(stack *middleware.Stack) error {
		return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("fakeS3", f.initialize), middleware.After)
	})
	return s3.NewFromConfig(cfg)
}

func (f *fakeS3Handler) initialize(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
	op := middleware.GetOperationName(ctx)
	switch op {
	case "HeadObject":
		return f.handleHead(in)
	case "GetObject":
		return f.handleGet(in)
	case "PutObject":
		return f.handlePut(in)
	case "DeleteObject":
		return f.handleDelete(in)
	case "ListObjectsV2":
		return f.handleList(in)
	case "CreateMultipartUpload":
		return f.handleCreateMultipart(in)
	case "UploadPart":
		return f.handleUploadPart(in)
	case "CompleteMultipartUpload":
		return f.handleCompleteMultipart(in)
	case "AbortMultipartUpload":
		return f.handleAbortMultipart(in)
	}
	return next.HandleInitialize(ctx, in)
}

func (f *fakeS3Handler) handleCreateMultipart(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.CreateMultipartUploadInput)
	uploadID := "upload-" + *params.Key
	f.mu.Lock()
	if f.multipart == nil {
		f.multipart = map[string]map[int][]byte{}
	}
	f.multipart[uploadID] = map[int][]byte{}
	f.mu.Unlock()
	return middleware.InitializeOutput{Result: &s3.CreateMultipartUploadOutput{UploadId: aws.String(uploadID)}}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleUploadPart(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failUploadPart {
		return middleware.InitializeOutput{}, middleware.Metadata{}, &smithy.GenericAPIError{Code: "InternalError", Message: "upload part forced failure"}
	}
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.UploadPartInput)
	data, err := io.ReadAll(params.Body)
	if err != nil {
		return middleware.InitializeOutput{}, middleware.Metadata{}, err
	}
	f.mu.Lock()
	if f.multipart[*params.UploadId] == nil {
		f.mu.Unlock()
		return middleware.InitializeOutput{}, middleware.Metadata{}, &smithy.GenericAPIError{Code: "NoSuchUpload", Message: "no such upload"}
	}
	f.multipart[*params.UploadId][int(*params.PartNumber)] = data
	f.mu.Unlock()
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))
	return middleware.InitializeOutput{Result: &s3.UploadPartOutput{ETag: aws.String(etag)}}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleCompleteMultipart(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.CompleteMultipartUploadInput)
	key := *params.Key
	f.mu.Lock()
	parts := f.multipart[*params.UploadId]
	if parts == nil {
		f.mu.Unlock()
		return middleware.InitializeOutput{}, middleware.Metadata{}, &smithy.GenericAPIError{Code: "NoSuchUpload", Message: "no such upload"}
	}
	// Собираем объект из частей по порядку номеров (проверка целостности).
	var out bytes.Buffer
	for i := 1; i <= len(parts); i++ {
		out.Write(parts[i])
	}
	delete(f.multipart, *params.UploadId)
	f.objects[key] = out.Bytes()
	f.mu.Unlock()
	return middleware.InitializeOutput{Result: &s3.CompleteMultipartUploadOutput{}}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleAbortMultipart(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	params := in.Parameters.(*s3.AbortMultipartUploadInput)
	f.mu.Lock()
	delete(f.multipart, *params.UploadId)
	f.abortCount++
	f.mu.Unlock()
	return middleware.InitializeOutput{Result: &s3.AbortMultipartUploadOutput{}}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleHead(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.HeadObjectInput)
	key := *params.Key
	data, ok := f.objects[key]
	if !ok {
		return middleware.InitializeOutput{}, middleware.Metadata{}, &smithy.GenericAPIError{Code: "NotFound", Message: "not found"}
	}
	out := &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(data)))}
	return middleware.InitializeOutput{Result: out}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleGet(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.GetObjectInput)
	key := *params.Key
	data, ok := f.objects[key]
	if !ok {
		return middleware.InitializeOutput{}, middleware.Metadata{}, &smithy.GenericAPIError{Code: "NotFound", Message: "not found"}
	}
	out := &s3.GetObjectOutput{
		ContentLength: aws.Int64(int64(len(data))),
		Body:          io.NopCloser(bytes.NewReader(data)),
	}
	return middleware.InitializeOutput{Result: out}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handlePut(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.PutObjectInput)
	key := *params.Key
	if f.preconditionFailed {
		if _, exists := f.objects[key]; exists {
			return middleware.InitializeOutput{}, middleware.Metadata{}, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "precondition failed"}
		}
	}
	data, err := io.ReadAll(params.Body)
	if err != nil {
		return middleware.InitializeOutput{}, middleware.Metadata{}, err
	}
	f.objects[key] = data
	return middleware.InitializeOutput{Result: &s3.PutObjectOutput{}}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleDelete(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.DeleteObjectInput)
	delete(f.objects, *params.Key)
	return middleware.InitializeOutput{Result: &s3.DeleteObjectOutput{}}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleList(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	if f.failStatus > 0 {
		return middleware.InitializeOutput{}, middleware.Metadata{}, f.failError()
	}
	params := in.Parameters.(*s3.ListObjectsV2Input)
	prefix := ""
	if params.Prefix != nil {
		prefix = *params.Prefix
	}
	// Собираем ключи, удовлетворяющие префиксу, в отсортированном порядке.
	var keys []string
	for k := range f.objects {
		if prefix == "" || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	// Пагинация: токен — последний выданный ключ; страница = 1 объект.
	start := 0
	if params.ContinuationToken != nil {
		tok := *params.ContinuationToken
		for i, k := range keys {
			if k == tok {
				start = i + 1
				break
			}
		}
	}
	var contents []types.Object
	if start < len(keys) {
		k := keys[start]
		contents = append(contents, types.Object{Key: aws.String(k), Size: aws.Int64(int64(len(f.objects[k])))})
	}
	out := &s3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(false)}
	if f.truncateWithoutToken {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = nil
		return middleware.InitializeOutput{Result: out}, middleware.Metadata{}, nil
	}
	if start+1 < len(keys) {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(keys[start])
	}
	return middleware.InitializeOutput{Result: out}, middleware.Metadata{}, nil
}

// failError создаёт ошибку с заданным HTTP status code.
func (f *fakeS3Handler) failError() error {
	code := "InternalError"
	switch f.failStatus {
	case 403:
		code = "AccessDenied"
	case 404:
		code = "NotFound"
	case 429:
		code = "SlowDown"
	}
	return &smithy.GenericAPIError{Code: code, Message: "forced failure"}
}

func TestS3SourceLookupOpen(t *testing.T) {
	f := newFakeS3Handler()
	f.objects["dir/file.bin"] = []byte("source payload")

	s, err := NewSourceStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}

	key := object.ObjectKey("dir/file.bin")
	meta, err := s.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len("source payload")) {
		t.Fatalf("size = %d", meta.Size)
	}

	art, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "source payload" {
		t.Fatalf("got %q", got)
	}
}

func TestS3SourceNotFound(t *testing.T) {
	f := newFakeS3Handler()
	s, err := NewSourceStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "missing.jpg"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := s.Open(context.Background(), "missing.jpg"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestS3ResultPublishReadDelete(t *testing.T) {
	f := newFakeS3Handler()
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	key := object.ObjectKey("out/result.bin")
	payload := []byte("result payload")

	if err := r.Publish(ctx, key, bytes.NewReader(payload), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	meta, err := r.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", meta.Size, len(payload))
	}
	art, err := r.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read mismatch")
	}

	if err := r.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.Lookup(ctx, key); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestS3ResultNoOverwriteConflict(t *testing.T) {
	f := newFakeS3Handler()
	f.preconditionFailed = true
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	key := object.ObjectKey("dup.bin")
	if err := r.Publish(ctx, key, bytes.NewReader([]byte("v1")), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish v1: %v", err)
	}
	err = r.Publish(ctx, key, bytes.NewReader([]byte("v2")), object.PublishOptions{NoOverwrite: true})
	if !errors.Is(err, object.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestS3UnsafeKey(t *testing.T) {
	f := newFakeS3Handler()
	s, err := NewSourceStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "../escape"); !errors.Is(err, object.ErrUnsafePath) {
		t.Fatalf("expected ErrUnsafePath, got %v", err)
	}
}

func TestS3Stats(t *testing.T) {
	f := newFakeS3Handler()
	f.objects["a/1.bin"] = []byte("12345")
	f.objects["a/2.bin"] = []byte("123")
	f.objects["b/3.bin"] = []byte("12")
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	stats, err := r.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 3 {
		t.Fatalf("objects = %d, want 3", stats.Objects)
	}
	if stats.TotalBytes != 10 {
		t.Fatalf("total = %d, want 10", stats.TotalBytes)
	}
}

// TestS3StatsTruncatedWithoutToken проверяет защиту от бесконечного цикла:
// если бэкенд вернул IsTruncated=true без NextContinuationToken, Stats
// должен завершиться ошибкой, а не зависнуть.
func TestS3StatsTruncatedWithoutToken(t *testing.T) {
	f := newFakeS3Handler()
	f.objects["a/1.bin"] = []byte("12345")
	f.truncateWithoutToken = true
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r.Stats(context.Background()); err == nil {
		t.Fatal("expected error for truncated list without continuation token")
	}
}

// TestS3StatsPagination verifies that Stats follows pagination tokens.
func TestS3StatsPagination(t *testing.T) {
	f := newFakeS3Handler()
	f.objects["a/1.bin"] = []byte("12345")
	f.objects["a/2.bin"] = []byte("123")
	f.listPages = 2
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	stats, err := r.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 2 {
		t.Fatalf("objects = %d, want 2", stats.Objects)
	}
	if stats.TotalBytes != 8 {
		t.Fatalf("total = %d, want 8", stats.TotalBytes)
	}
}

// TestS3Forbidden verifies that 403 maps to object.ErrForbidden.
func TestS3Forbidden(t *testing.T) {
	f := newFakeS3Handler()
	f.failStatus = 403
	s, err := NewSourceStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "x.bin"); !errors.Is(err, object.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// TestS3Throttled verifies that 429 maps to ErrUnavailable (retryable).
func TestS3Throttled(t *testing.T) {
	f := newFakeS3Handler()
	f.failStatus = 429
	s, err := NewSourceStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "x.bin"); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// TestS3ServerError verifies that 5xx maps to ErrUnavailable (retryable).
func TestS3ServerError(t *testing.T) {
	f := newFakeS3Handler()
	f.failStatus = 500
	s, err := NewSourceStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "x.bin"); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// TestS3DeleteIdempotent verifies that deleting a missing object is not an
// error (idempotent delete).
func TestS3DeleteIdempotent(t *testing.T) {
	f := newFakeS3Handler()
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	// Объекта нет — Delete должен вернуть nil (идемпотентно).
	if err := r.Delete(context.Background(), "missing.bin"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

// TestS3PrefixNormalization verifies that a trailing slash in Prefix is
// trimmed so keys do not get "//".
func TestS3PrefixNormalization(t *testing.T) {
	f := newFakeS3Handler()
	f.objects["dir/file.bin"] = []byte("payload")
	s, err := NewSourceStore(Options{Bucket: "bucket", Prefix: "dir/", Client: f.client()})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	meta, err := s.Lookup(context.Background(), "file.bin")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len("payload")) {
		t.Fatalf("size = %d, want %d", meta.Size, len("payload"))
	}
}

// nonThreadSafeReader — намеренно НЕ потокобезопасный reader, имитирующий
// remote.BufferReader при output-limit: 0 (К1). Общее состояние (offset, r)
// без блокировок: конкурентные Read вызовы привели бы к повреждению данных.
// Используется в тесте multipart, чтобы доказать, что чтение источника
// сериализуется продюсером.
type nonThreadSafeReader struct {
	data   []byte
	offset int
}

func (r *nonThreadSafeReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

// TestS3MultipartNoRace verifies that publishMultipart reads the shared
// (non-thread-safe) source serially: parts are sliced by a single producer
// goroutine, uploaded concurrently, and the assembled object matches the
// original payload byte-for-byte.
func TestS3MultipartPublish(t *testing.T) {
	f := newFakeS3Handler()
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	key := object.ObjectKey("big/object.webp")

	// 3 полных парта (по multipartPartSize) + хвост: ~15.5 МБ.
	payload := make([]byte, multipartPartSize*3+12345)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	src := &nonThreadSafeReader{data: payload}

	if err := r.Publish(ctx, key, src, object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Объект должен быть собран из всех частей в правильном порядке.
	got, ok := f.objects["big/object.webp"]
	if !ok {
		t.Fatalf("object %q not published", "big/object.webp")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("multipart payload corrupted: got %d bytes, want %d (race in parallel reader?)", len(got), len(payload))
	}

	// Проверяем через Open, что объект читается корректно.
	art, err := r.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	read, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(read, payload) {
		t.Fatalf("read back mismatch")
	}
}

// TestS3MultipartAbortOnError verifies, что при ошибке загрузки любой части
// выполняется AbortMultipartUpload и возвращается типизированная ошибка.
func TestS3MultipartAbortOnError(t *testing.T) {
	f := newFakeS3Handler()
	r, err := NewResultStore(Options{Bucket: "bucket", Client: f.client()})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	payload := make([]byte, multipartPartSize*2)
	for i := range payload {
		payload[i] = byte(i)
	}
	// UploadPart должен завершиться ошибкой 500 (IsUnavailable).
	f.failUploadPart = true
	err = r.Publish(context.Background(), object.ObjectKey("fail.bin"), bytes.NewReader(payload), object.PublishOptions{})
	if !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	f.mu.Lock()
	aborted := f.abortCount
	f.mu.Unlock()
	if aborted == 0 {
		t.Fatal("expected AbortMultipartUpload after upload part failure")
	}
}
