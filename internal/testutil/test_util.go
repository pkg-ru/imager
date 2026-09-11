// Package testutil содержит общие тестовые хелперы (in-memory fakes портов),
// используемые несколькими пакетами приложения. Пакет internal — доступен
// только внутри модуля.
package testutil

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/ports/storage"
)

// MemArtifact — object.Artifact поверх []byte (потокобезопасный).
type MemArtifact struct {
	mu   sync.Mutex
	buf  []byte
	pos  int64
	meta object.ObjectMetadata
}

// NewMemArtifact создаёт MemArtifact с данными и метаданными.
func NewMemArtifact(data []byte, meta object.ObjectMetadata) *MemArtifact {
	return &MemArtifact{buf: append([]byte(nil), data...), meta: meta}
}

func (a *MemArtifact) Read(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pos >= int64(len(a.buf)) {
		return 0, io.EOF
	}
	n := copy(p, a.buf[a.pos:])
	a.pos += int64(n)
	return n, nil
}

func (a *MemArtifact) Seek(offset int64, whence int) (int64, error) {
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
	default:
		return a.pos, errors.New("invalid whence")
	}
	if np < 0 || np > int64(len(a.buf)) {
		return a.pos, errors.New("invalid seek")
	}
	a.pos = np
	return np, nil
}

func (a *MemArtifact) Close() error { return nil }

func (a *MemArtifact) Metadata() object.ObjectMetadata { return a.meta }

// MemStream — object.Stream поверх []byte (одноразовый, без Seek).
type MemStream struct {
	mu   sync.Mutex
	buf  []byte
	pos  int
	meta object.ObjectMetadata
}

func (s *MemStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos >= len(s.buf) {
		return 0, io.EOF
	}
	n := copy(p, s.buf[s.pos:])
	s.pos += n
	return n, nil
}

func (s *MemStream) Close() error { return nil }

func (s *MemStream) Metadata() object.ObjectMetadata { return s.meta }

// MemSourceStore — in-memory storage.SourceStore со счётчиками вызовов Open.
type MemSourceStore struct {
	mu        sync.Mutex
	files     map[object.ObjectKey][]byte
	openCalls int
	openKeys  []object.ObjectKey
}

// NewMemSourceStore создаёт пустой MemSourceStore.
func NewMemSourceStore() *MemSourceStore {
	return &MemSourceStore{files: map[object.ObjectKey][]byte{}}
}

// Add кладёт данные исходника (подготовка состояния теста).
func (s *MemSourceStore) Add(key object.ObjectKey, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[key] = data
}

func (s *MemSourceStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.files[key]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (s *MemSourceStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openCalls++
	s.openKeys = append(s.openKeys, key)
	d, ok := s.files[key]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return NewMemArtifact(d, object.ObjectMetadata{Key: key, Size: int64(len(d))}), nil
}

// OpenedKeys возвращает копию списка ключей, открытых через Open.
func (s *MemSourceStore) OpenedKeys() []object.ObjectKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]object.ObjectKey(nil), s.openKeys...)
}

var _ storage.SourceStore = (*MemSourceStore)(nil)

// MemResultStore — in-memory storage.ResultStore с атомарным publish,
// поддержкой List (storage.Lister) и опциональной ошибкой Publish.
type MemResultStore struct {
	mu        sync.Mutex
	data      map[object.ObjectKey][]byte
	pubErr    error
	readCalls int
}

// NewMemResultStore создаёт пустой MemResultStore.
func NewMemResultStore() *MemResultStore {
	return &MemResultStore{data: map[object.ObjectKey][]byte{}}
}

// Add напрямую кладёт данные в store (подготовка состояния теста).
func (r *MemResultStore) Add(key object.ObjectKey, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = append([]byte(nil), data...)
}

// SetPubErr задаёт ошибку, возвращаемую всеми последующими Publish.
func (r *MemResultStore) SetPubErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pubErr = err
}

func (r *MemResultStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (r *MemResultStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return NewMemArtifact(d, object.ObjectMetadata{Key: key, Size: int64(len(d))}), nil
}

func (r *MemResultStore) ReadStream(_ context.Context, key object.ObjectKey) (object.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readCalls++
	d, ok := r.data[key]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &MemStream{buf: append([]byte(nil), d...), meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

// ReadCalls возвращает число вызовов ReadStream (для проверки, что результат
// не перечитывается из хранилища лишний раз).
func (r *MemResultStore) ReadCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readCalls
}

func (r *MemResultStore) Publish(_ context.Context, key object.ObjectKey, src io.Reader, _ object.PublishOptions) error {
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

func (r *MemResultStore) Delete(_ context.Context, key object.ObjectKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key)
	return nil
}

func (r *MemResultStore) Stats(_ context.Context) (object.StoreStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var st object.StoreStats
	for _, d := range r.data {
		st.Objects++
		st.TotalBytes += int64(len(d))
	}
	return st, nil
}

// List реализует storage.Lister: возвращает ключи с заданным префиксом.
func (r *MemResultStore) List(_ context.Context, prefix object.ObjectKey) ([]object.ObjectKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []object.ObjectKey
	for k := range r.data {
		if strings.HasPrefix(string(k), prefix.String()) {
			out = append(out, k)
		}
	}
	return out, nil
}

// Has возвращает true, если объект с ключом опубликован.
func (r *MemResultStore) Has(key object.ObjectKey) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.data[key]
	return ok
}

// Get возвращает данные, опубликованные под ключом (nil, если нет).
func (r *MemResultStore) Get(key object.ObjectKey) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.data[key]...)
}

var _ storage.ResultStore = (*MemResultStore)(nil)
var _ storage.Lister = (*MemResultStore)(nil)

// NopLogger — no-op реализация observability.Logger для тестов.
type NopLogger struct{}

func (NopLogger) Debugf(string, ...any) {}
func (NopLogger) Infof(string, ...any)  {}
func (NopLogger) Warnf(string, ...any)  {}
func (NopLogger) Errorf(string, ...any) {}

// EmptyReader — пустой io.Reader для Publish в тестах.
func EmptyReader() io.Reader {
	return strings.NewReader("")
}
