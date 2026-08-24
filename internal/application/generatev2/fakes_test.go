package generatev2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// wantOutcome проверяет, что err является *OutcomeError с указанной
// категорией (в том числе обёрнутой) — тот же путь, что использует прод
// (errors.As + switch по Kind).
func wantOutcome(t *testing.T, err error, kind OutcomeKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected OutcomeError %v, got nil", kind)
	}
	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v (%T), want *OutcomeError", err, err)
	}
	if oe.Kind != kind {
		t.Fatalf("kind = %v, want %v", oe.Kind, kind)
	}
}

// memArtifact — object.Artifact поверх []byte.
type memArtifact struct {
	mu   sync.Mutex
	buf  []byte
	pos  int64
	meta object.ObjectMetadata
}

func (a *memArtifact) Read(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pos >= int64(len(a.buf)) {
		return 0, io.EOF
	}
	n := copy(p, a.buf[a.pos:])
	a.pos += int64(n)
	return n, nil
}

func (a *memArtifact) Seek(offset int64, whence int) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = a.pos + offset
	case io.SeekEnd:
		np = int64(len(a.buf)) + offset
	}
	if np < 0 || np > int64(len(a.buf)) {
		return a.pos, errors.New("invalid seek")
	}
	a.pos = np
	return np, nil
}

func (a *memArtifact) Close() error { return nil }

func (a *memArtifact) Metadata() object.ObjectMetadata { return a.meta }

// memStream — object.Stream поверх []byte (одноразовый, без Seek).
type memStream struct {
	mu   sync.Mutex
	buf  []byte
	pos  int
	meta object.ObjectMetadata
}

func (s *memStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos >= len(s.buf) {
		return 0, io.EOF
	}
	n := copy(p, s.buf[s.pos:])
	s.pos += n
	return n, nil
}

func (s *memStream) Close() error { return nil }

func (s *memStream) Metadata() object.ObjectMetadata { return s.meta }

// memSourceStore — storage.SourceStore в памяти.
type memSourceStore struct {
	mu    sync.Mutex
	files map[object.ObjectKey][]byte
}

func newMemSourceStore() *memSourceStore {
	return &memSourceStore{files: map[object.ObjectKey][]byte{}}
}

func (s *memSourceStore) add(key object.ObjectKey, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[key] = data
}

func (s *memSourceStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.files[key]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (s *memSourceStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.files[key]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memArtifact{buf: append([]byte(nil), d...), meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

var _ storage.SourceStore = (*memSourceStore)(nil)

// memResultStore — storage.ResultStore в памяти с атомарным publish.
type memResultStore struct {
	mu       sync.Mutex
	data     map[object.ObjectKey][]byte
	pubErr   error
	lookupFn func(object.ObjectKey) error
}

func newMemResultStore() *memResultStore {
	return &memResultStore{data: map[object.ObjectKey][]byte{}}
}

func (r *memResultStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lookupFn != nil {
		if err := r.lookupFn(key); err != nil {
			return object.ObjectMetadata{}, err
		}
	}
	d, ok := r.data[key]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (r *memResultStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memArtifact{buf: append([]byte(nil), d...), meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

func (r *memResultStore) ReadStream(_ context.Context, key object.ObjectKey) (object.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memStream{buf: append([]byte(nil), d...), meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

func (r *memResultStore) Publish(_ context.Context, key object.ObjectKey, src io.Reader, _ object.PublishOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pubErr != nil {
		return r.pubErr
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	r.data[key] = data
	return nil
}

func (r *memResultStore) Delete(_ context.Context, key object.ObjectKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key)
	return nil
}

func (r *memResultStore) Stats(_ context.Context) (object.StoreStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var st object.StoreStats
	for _, d := range r.data {
		st.Objects++
		st.TotalBytes += int64(len(d))
	}
	return st, nil
}

var _ storage.ResultStore = (*memResultStore)(nil)

// fakeProcessor — processor.Processor с детерминированным выводом и
// возможностью эмулировать ошибки/блокировку.
type fakeProcessor struct {
	mu      sync.Mutex
	payload []byte
	procErr error
	block   chan struct{}
	calls   int
	lastCtx context.Context
}

func newFakeProcessor(payload []byte) *fakeProcessor {
	return &fakeProcessor{payload: payload}
}

func (f *fakeProcessor) Process(ctx context.Context, _ processor.Input, out io.Writer) (*processor.Result, error) {
	f.mu.Lock()
	f.calls++
	f.lastCtx = ctx
	block := f.block
	procErr := f.procErr
	payload := f.payload
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if procErr != nil {
		return nil, procErr
	}
	if _, err := out.Write(payload); err != nil {
		return nil, err
	}
	return &processor.Result{Size: int64(len(payload))}, nil
}

func (f *fakeProcessor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProcessor) setBlock(ch chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block = ch
}

func (f *fakeProcessor) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procErr = err
}

// fakeLogger — заглушка.
type fakeLogger struct{}

func (fakeLogger) Debugf(string, ...any) {}
func (fakeLogger) Infof(string, ...any)  {}
func (fakeLogger) Warnf(string, ...any)  {}
func (fakeLogger) Errorf(string, ...any) {}

var _ = bytes.NewReader
