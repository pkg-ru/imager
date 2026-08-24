package httpapi

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/pkg-ru/imager/internal/application/generatev2"
	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// fakeGenerator — управляемый fake Generator для тестов.
type fakeGenerator struct {
	mu       sync.Mutex
	results  map[string]*generatev2.Result // keyed by canonical URL
	errs     map[string]error              // keyed by canonical URL
	fallback error                         // ошибка по умолчанию
	block    chan struct{}                 // если задан, Generate блокируется
}

func newFakeGenerator() *fakeGenerator {
	return &fakeGenerator{
		results: map[string]*generatev2.Result{},
		errs:    map[string]error{},
	}
}

func (f *fakeGenerator) addResult(url string, data []byte, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[url] = &generatev2.Result{
		URL: url,
		Opened: &memArtifact{
			data: data,
			meta: object.ObjectMetadata{Key: object.ObjectKey(url), Size: size},
		},
	}
}

func (f *fakeGenerator) setFallback(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = err
}

func (f *fakeGenerator) Generate(ctx context.Context, req *asset.Request) (*generatev2.Result, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	url, err := req.Build()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.errs[url]; ok {
		return nil, e
	}
	if r, ok := f.results[url]; ok {
		return r, nil
	}
	if f.fallback != nil {
		return nil, f.fallback
	}
	return nil, &generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "not found"}
}

// memArtifact — in-memory object.Artifact.
type memArtifact struct {
	data []byte
	meta object.ObjectMetadata
	off  int
}

func (a *memArtifact) Read(p []byte) (int, error) {
	if a.off >= len(a.data) {
		return 0, io.EOF
	}
	n := copy(p, a.data[a.off:])
	a.off += n
	return n, nil
}

func (a *memArtifact) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = int64(a.off)
	case io.SeekEnd:
		base = int64(len(a.data))
	default:
		return 0, errors.New("invalid whence")
	}
	base += offset
	if base < 0 {
		return 0, errors.New("negative seek")
	}
	a.off = int(base)
	return base, nil
}

func (a *memArtifact) Close() error { return nil }

func (a *memArtifact) Metadata() object.ObjectMetadata { return a.meta }

// fakePixel — fake PixelGenerator.
type fakePixel struct {
	bytes []byte
	err   error
}

func (p *fakePixel) GeneratePixel(ctx context.Context, format string) ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.bytes != nil {
		return p.bytes, nil
	}
	return []byte("PIXEL:" + format), nil
}

// memSourceStore — in-memory storage.SourceStore.
type memSourceStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemSourceStore() *memSourceStore {
	return &memSourceStore{data: map[string][]byte{}}
}

func (s *memSourceStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key.String()]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (s *memSourceStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memArtifact{data: d, meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

// memResultStore — in-memory storage.ResultStore.
type memResultStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemResultStore() *memResultStore {
	return &memResultStore{data: map[string][]byte{}}
}

func (s *memResultStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key.String()]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (s *memResultStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memArtifact{data: d, meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

func (s *memResultStore) ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memArtifact{data: d, meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

func (s *memResultStore) Publish(ctx context.Context, key object.ObjectKey, r io.Reader, opts object.PublishOptions) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if opts.NoOverwrite {
		if _, ok := s.data[key.String()]; ok {
			return &object.ConflictError{Key: key}
		}
	}
	s.data[key.String()] = data
	return nil
}

func (s *memResultStore) Delete(ctx context.Context, key object.ObjectKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key.String())
	return nil
}

func (s *memResultStore) Stats(ctx context.Context) (object.StoreStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st object.StoreStats
	for _, d := range s.data {
		st.Objects++
		st.TotalBytes += int64(len(d))
	}
	return st, nil
}

// fakeProcessor — fake processor.Processor: копирует исходник в out.
type fakeProcessor struct{}

func (fakeProcessor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	n, err := io.Copy(out, in.Source)
	if err != nil {
		return nil, err
	}
	return &processor.Result{Size: n}, nil
}
