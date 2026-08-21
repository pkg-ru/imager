package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	objects map[string][]byte
	// preconditionFailed — если true, PutObject с IfNoneMatch возвращает
	// PreconditionFailed при существующем объекте.
	preconditionFailed bool
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
	}
	return next.HandleInitialize(ctx, in)
}

func (f *fakeS3Handler) handleHead(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
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
	params := in.Parameters.(*s3.DeleteObjectInput)
	delete(f.objects, *params.Key)
	return middleware.InitializeOutput{Result: &s3.DeleteObjectOutput{}}, middleware.Metadata{}, nil
}

func (f *fakeS3Handler) handleList(in middleware.InitializeInput) (middleware.InitializeOutput, middleware.Metadata, error) {
	params := in.Parameters.(*s3.ListObjectsV2Input)
	prefix := ""
	if params.Prefix != nil {
		prefix = *params.Prefix
	}
	var contents []types.Object
	for k, v := range f.objects {
		if prefix == "" || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			contents = append(contents, types.Object{Key: aws.String(k), Size: aws.Int64(int64(len(v)))})
		}
	}
	out := &s3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(false)}
	return middleware.InitializeOutput{Result: out}, middleware.Metadata{}, nil
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
