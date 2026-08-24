package adminsvc

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/imager/internal/application/generatev2"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/object"
	"github.com/pkg-ru/imager/internal/domain/policy"
)

// wantErr проверяет, что err является указанной ошибкой (в том числе
// обёрнутой) — тот же путь, что использует прод (errors.Is).
func wantErr(t *testing.T, err error, want error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %v, got nil", want)
	}
	if !errors.Is(err, want) {
		t.Fatalf("err = %v (%T), want %v", err, err, want)
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

// memResultStore — storage.ResultStore в памяти с поддержкой List (Lister).
type memResultStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemResultStore() *memResultStore {
	return &memResultStore{data: map[string][]byte{}}
}

func (r *memResultStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key.String()]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (r *memResultStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memArtifact{buf: append([]byte(nil), d...), meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

func (r *memResultStore) ReadStream(_ context.Context, key object.ObjectKey) (object.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &memArtifact{buf: append([]byte(nil), d...), meta: object.ObjectMetadata{Key: key, Size: int64(len(d))}}, nil
}

func (r *memResultStore) Publish(_ context.Context, key object.ObjectKey, src io.Reader, _ object.PublishOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	r.data[key.String()] = data
	return nil
}

func (r *memResultStore) Delete(_ context.Context, key object.ObjectKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key.String())
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

// List реализует storage.Lister: возвращает ключи с заданным префиксом.
func (r *memResultStore) List(_ context.Context, prefix object.ObjectKey) ([]object.ObjectKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []object.ObjectKey
	for k := range r.data {
		if strings.HasPrefix(k, prefix.String()) {
			out = append(out, object.ObjectKey(k))
		}
	}
	return out, nil
}

var _ storage.ResultStore = (*memResultStore)(nil)
var _ storage.Lister = (*memResultStore)(nil)

// fakeGenerator — управляемый fake Generator (совместим с adminsvc.Generator).
type fakeGenerator struct {
	mu       sync.Mutex
	results  map[string]*generatev2.Result
	errs     map[string]error
	fallback error
	calls    int
}

func newFakeGenerator() *fakeGenerator {
	return &fakeGenerator{
		results: map[string]*generatev2.Result{},
		errs:    map[string]error{},
	}
}

func (f *fakeGenerator) addResult(url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[url] = &generatev2.Result{URL: url, Key: object.ObjectKey(url)}
}

func (f *fakeGenerator) setErr(url string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[url] = err
}

func (f *fakeGenerator) setFallback(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = err
}

func (f *fakeGenerator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeGenerator) Generate(_ context.Context, req *asset.Request) (*generatev2.Result, error) {
	url, err := req.Build()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if e, ok := f.errs[url]; ok {
		return nil, e
	}
	if r, ok := f.results[url]; ok {
		return r, nil
	}
	if f.fallback != nil {
		return nil, f.fallback
	}
	// По умолчанию генерация успешна для любого URL.
	return &generatev2.Result{URL: url, Key: object.ObjectKey(url)}, nil
}

var _ Generator = (*fakeGenerator)(nil)

// fakeLogger — заглушка.
type fakeLogger struct{}

func (fakeLogger) Debugf(string, ...any) {}
func (fakeLogger) Infof(string, ...any)  {}
func (fakeLogger) Warnf(string, ...any)  {}
func (fakeLogger) Errorf(string, ...any) {}

// testConfig — конфигурация с малыми значениями для быстрых тестов.
func testConfig() Config {
	return Config{Workers: 2, QueueSize: 4, WaitTimeout: 5 * time.Second}
}

// newTestService собирает Service с fakes.
func newTestService(t *testing.T, gen Generator, sources *memSourceStore, results storage.ResultStore, presets *asset.PresetSet, pol *policy.Policy) *Service {
	t.Helper()
	svc, err := New(Deps{
		Gen:     gen,
		Sources: sources,
		Results: results,
		Presets: presets,
		Policy:  pol,
		Logger:  fakeLogger{},
	}, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// mustPreset создаёт пресет для тестов.
func mustPreset(t *testing.T, name string, size string, outFmt string) *asset.Preset {
	t.Helper()
	sz, err := asset.ParseSize(size)
	if err != nil {
		t.Fatalf("ParseSize(%q): %v", size, err)
	}
	f, err := asset.NewFormat(outFmt)
	if err != nil {
		t.Fatalf("NewFormat(%q): %v", outFmt, err)
	}
	p, err := asset.NewPreset(name, asset.TransformCrop, sz, f, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("NewPreset(%q): %v", name, err)
	}
	return p
}

// mustPresetSet создаёт набор пресетов.
func mustPresetSet(t *testing.T, presets ...*asset.Preset) *asset.PresetSet {
	t.Helper()
	set, err := asset.NewPresetSet(presets)
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	return set
}

// safePolicy с точным size-rule (для перечисления канонических ассетов) и
// разрешённым пресетом "thumb".
func safePolicy() *policy.Policy {
	rule := policy.SizeRule{
		Width:  &policy.Range{Min: 120, Max: 120},
		Height: &policy.Range{Min: 80, Max: 80},
	}
	return &policy.Policy{Global: policy.GlobalPolicy{
		Authorization:  policy.AuthSafe,
		SizeRules:      []policy.SizeRule{rule},
		AllowedPresets: []string{"thumb"},
	}}
}

// unsafePolicy — unsafe authorization без size-rules (перечисление → ошибка).
func unsafePolicy() *policy.Policy {
	return &policy.Policy{Global: policy.GlobalPolicy{Authorization: policy.AuthUnsafe}}
}

// TestEnqueueGenerateInvalidRequest — оба/ни одного из source/assets → 400.
func TestEnqueueGenerateInvalidRequest(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	res := newMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	// Ни одного.
	_, err := svc.EnqueueGenerate("", nil, false)
	wantErr(t, err, ErrInvalidRequest)

	// Оба.
	_, err = svc.EnqueueGenerate("thumbs/photo.jpg", []string{"/thumbs/photo-jpg/c-120x80@2.webp"}, false)
	wantErr(t, err, ErrInvalidRequest)
}

// TestEnqueueGenerateSourceNotFound — несуществующий исходник → 404.
func TestEnqueueGenerateSourceNotFound(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	res := newMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t, mustPreset(t, "thumb", "120x80", "webp")), safePolicy())

	_, err := svc.EnqueueGenerate("thumbs/missing.jpg", nil, false)
	wantErr(t, err, ErrSourceNotFound)
}

// TestEnqueueGenerateUnsafeCannotEnumerate — unsafe без size-rules → 400.
func TestEnqueueGenerateUnsafeCannotEnumerate(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	src.add(object.ObjectKey("thumbs/photo.jpg"), []byte("JPEG"))
	res := newMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t), unsafePolicy())

	_, err := svc.EnqueueGenerate("thumbs/photo.jpg", nil, false)
	wantErr(t, err, ErrInvalidRequest)
}

// TestEnqueueGenerateWaitMode — wait=true генерирует все ассеты и возвращает
// полный результат.
func TestEnqueueGenerateWaitMode(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	src.add(object.ObjectKey("thumbs/photo.jpg"), []byte("JPEG"))
	res := newMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t, mustPreset(t, "thumb", "120x80", "webp")), safePolicy())

	svc.Start(context.Background())
	defer svc.Stop()

	res2, err := svc.EnqueueGenerate("thumbs/photo.jpg", nil, true)
	if err != nil {
		t.Fatalf("EnqueueGenerate: %v", err)
	}
	if res2.Status != "completed" {
		t.Errorf("Status = %q, want completed", res2.Status)
	}
	if res2.Generated == 0 {
		t.Error("expected at least one generated asset")
	}
	if res2.Failed != nil && len(res2.Failed) != 0 {
		t.Errorf("unexpected failed: %+v", res2.Failed)
	}
}

// TestEnqueueGenerateSkipExisting — существующие ассеты пропускаются.
func TestEnqueueGenerateSkipExisting(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	src.add(object.ObjectKey("thumbs/photo.jpg"), []byte("JPEG"))
	res := newMemResultStore()
	// Заранее публикуем один канонический ассет, чтобы он был пропущен.
	url := "thumbs/photo-jpg/120x80.jpeg"
	res.Publish(context.Background(), object.ObjectKey(url), emptyReader(), object.PublishOptions{})
	svc := newTestService(t, gen, src, res, mustPresetSet(t, mustPreset(t, "thumb", "120x80", "webp")), safePolicy())

	svc.Start(context.Background())
	defer svc.Stop()

	res2, err := svc.EnqueueGenerate("thumbs/photo.jpg", nil, true)
	if err != nil {
		t.Fatalf("EnqueueGenerate: %v", err)
	}
	if res2.Skipped == 0 {
		t.Error("expected at least one skipped asset")
	}
}

// TestEnqueueGenerateAssetsMode — режим B (assets) генерирует перечисленные.
func TestEnqueueGenerateAssetsMode(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	res := newMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	svc.Start(context.Background())
	defer svc.Stop()

	url := "thumbs/photo-jpg/c-120x80@2.webp"
	res2, err := svc.EnqueueGenerate("", []string{url}, true)
	if err != nil {
		t.Fatalf("EnqueueGenerate: %v", err)
	}
	if res2.Status != "completed" {
		t.Errorf("Status = %q, want completed", res2.Status)
	}
	if res2.Generated != 1 {
		t.Errorf("Generated = %d, want 1", res2.Generated)
	}
}

// TestEnqueueGenerateQueueFull — переполнение очереди → 503.
func TestEnqueueGenerateQueueFull(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	res := newMemResultStore()
	// QueueSize=1, workers не запущены → очередь переполняется.
	svc, err := New(Deps{
		Gen:     gen,
		Sources: src,
		Results: res,
		Presets: mustPresetSet(t),
		Policy:  &policy.Policy{},
		Logger:  fakeLogger{},
	}, Config{Workers: 1, QueueSize: 1, WaitTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Первая задача занимает очередь (wait=false, воркеры не запущены).
	_, err = svc.EnqueueGenerate("", []string{"thumbs/photo-jpg/c-120x80@2.webp"}, false)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// Вторая — переполнение.
	_, err = svc.EnqueueGenerate("", []string{"thumbs/photo-jpg/c-120x80@3.webp"}, false)
	wantErr(t, err, ErrQueueFull)
}

// TestDeleteBySource — удаление всех ассетов исходника.
func TestDeleteBySource(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	res := newMemResultStore()
	// Публикуем ассеты исходника.
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), emptyReader(), object.PublishOptions{})
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/c-120x80@2.webp"), emptyReader(), object.PublishOptions{})
	// Посторонний ассет (другой исходник) — не должен удаляться.
	res.Publish(context.Background(), object.ObjectKey("thumbs/other-jpg/thumb.webp"), emptyReader(), object.PublishOptions{})
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	deleted, err := svc.DeleteBySource(context.Background(), "thumbs/photo.jpg")
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	// Посторонний ассет остался.
	if _, err := res.Lookup(context.Background(), object.ObjectKey("thumbs/other-jpg/thumb.webp")); err != nil {
		t.Errorf("unrelated asset should remain: %v", err)
	}
}

// TestDeleteAssets — удаление перечисленных ассетов.
func TestDeleteAssets(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	res := newMemResultStore()
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), emptyReader(), object.PublishOptions{})
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/c-120x80@2.webp"), emptyReader(), object.PublishOptions{})
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	deleted, err := svc.DeleteAssets(context.Background(), []string{"thumbs/photo-jpg/thumb.webp", "thumbs/photo-jpg/c-120x80@2.webp"})
	if err != nil {
		t.Fatalf("DeleteAssets: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
}

// TestDeleteBySourceNotLister — result-хранилище без List → 501.
func TestDeleteBySourceNotLister(t *testing.T) {
	gen := newFakeGenerator()
	src := newMemSourceStore()
	// Обычный ResultStore без List.
	res := &nonListerResultStore{}
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	_, err := svc.DeleteBySource(context.Background(), "thumbs/photo.jpg")
	wantErr(t, err, ErrNotImplemented)
}

// nonListerResultStore — ResultStore без поддержки List.
type nonListerResultStore struct{}

func (nonListerResultStore) Lookup(_ context.Context, _ object.ObjectKey) (object.ObjectMetadata, error) {
	return object.ObjectMetadata{}, &object.NotFoundError{}
}
func (nonListerResultStore) Open(_ context.Context, _ object.ObjectKey) (object.Artifact, error) {
	return nil, &object.NotFoundError{}
}
func (nonListerResultStore) ReadStream(_ context.Context, _ object.ObjectKey) (object.Stream, error) {
	return nil, &object.NotFoundError{}
}
func (nonListerResultStore) Publish(_ context.Context, _ object.ObjectKey, _ io.Reader, _ object.PublishOptions) error {
	return nil
}
func (nonListerResultStore) Delete(_ context.Context, _ object.ObjectKey) error { return nil }
func (nonListerResultStore) Stats(_ context.Context) (object.StoreStats, error) {
	return object.StoreStats{}, nil
}

var _ storage.ResultStore = (*nonListerResultStore)(nil)

// emptyReader — пустой io.Reader для Publish в тестах.
func emptyReader() io.Reader {
	return strings.NewReader("")
}
