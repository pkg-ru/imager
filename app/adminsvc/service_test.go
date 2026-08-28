package adminsvc

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/dynamic"
	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/domain/policy"
	"github.com/pkg-ru/imager/internal/testutil"
	"github.com/pkg-ru/imager/ports/metadata"
	"github.com/pkg-ru/imager/ports/storage"
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

// testConfig — конфигурация с малыми значениями для быстрых тестов.
func testConfig() Config {
	return Config{Workers: 2, QueueSize: 4, WaitTimeout: 5 * time.Second}
}

// newTestService создаёт Service с fakes.
func newTestService(t *testing.T, gen Generator, sources *testutil.MemSourceStore, results storage.ResultStore, presets *asset.PresetSet, pol *policy.Policy) *Service {
	t.Helper()
	return newTestServiceMeta(t, gen, sources, results, presets, pol, nil)
}

// newTestServiceMeta создаёт Service с fakes и опциональным metadata.Store.
func newTestServiceMeta(t *testing.T, gen Generator, sources *testutil.MemSourceStore, results storage.ResultStore, presets *asset.PresetSet, pol *policy.Policy, meta metadata.Store) *Service {
	t.Helper()
	svc, err := New(Deps{
		Gen:      gen,
		Sources:  sources,
		Results:  results,
		Presets:  presets,
		Policy:   pol,
		Metadata: meta,
		Logger:   testutil.NopLogger{},
	}, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// memMetadataStore — in-memory metadata.Store для тестов удаления.
type memMetadataStore struct {
	mu   sync.Mutex
	data map[string]*filemeta.FileMetadata
}

func newMemMetadataStore() *memMetadataStore {
	return &memMetadataStore{data: map[string]*filemeta.FileMetadata{}}
}

func (s *memMetadataStore) Load(_ context.Context, key string) (*filemeta.FileMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.data[key]
	if !ok {
		return nil, filemeta.ErrNotFound
	}
	return m.Clone(), nil
}

func (s *memMetadataStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok, nil
}

func (s *memMetadataStore) Save(_ context.Context, key string, m *filemeta.FileMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = m.Clone()
	return nil
}

func (s *memMetadataStore) Update(_ context.Context, key string, fn metadata.UpdateFn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.data[key]
	if !ok {
		m = filemeta.NewFileMetadata()
	}
	changed, err := fn(m)
	if err != nil {
		return err
	}
	if changed {
		s.data[key] = m
	}
	return nil
}

func (s *memMetadataStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *memMetadataStore) get(key string) *filemeta.FileMetadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.data[key]
	if !ok {
		return nil
	}
	return m.Clone()
}

var _ metadata.Store = (*memMetadataStore)(nil)

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
	p, err := asset.NewPreset(name, asset.TransformCrop, sz, []asset.Format{f}, 0, false, 0, 0, 0, nil)
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

// safePolicy — политика с "/" (fallback) и пресетом "thumb" (для
// перечисления ассетов исходника).
func safePolicy() *policy.Policy {
	compiled, err := policy.Compile(policy.Config{
		PathPolicies: map[string]policy.PathPolicyConfig{
			"/": {
				Presets: dynamic.StringSlice{dynamic.String("thumb")},
			},
		},
		Presets: []policy.PresetConfig{
			{
				Name:          dynamic.String("thumb"),
				Crop:          dynamic.String("center"),
				Width:         dynamic.Uint32(120),
				Height:        dynamic.Uint32(80),
				OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
			},
		},
	}, nil, nil)
	if err != nil {
		panic(err)
	}
	return compiled.Policy
}

// unsafePolicy — deny-by-default политика без path-policies (перечисление →
// пусто → ErrInvalidRequest).
func unsafePolicy() *policy.Policy {
	compiled, err := policy.Compile(policy.Config{}, nil, nil)
	if err != nil {
		panic(err)
	}
	return compiled.Policy
}

// TestEnqueueGenerateInvalidRequest — оба/ни одного из source/assets → 400.
func TestEnqueueGenerateInvalidRequest(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
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
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t, mustPreset(t, "thumb", "120x80", "webp")), safePolicy())

	_, err := svc.EnqueueGenerate("thumbs/missing.jpg", nil, false)
	wantErr(t, err, ErrSourceNotFound)
}

// TestEnqueueGenerateUnsafeCannotEnumerate — unsafe без size-rules → 400.
func TestEnqueueGenerateUnsafeCannotEnumerate(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	src.Add(object.ObjectKey("thumbs/photo.jpg"), []byte("JPEG"))
	res := testutil.NewMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t), unsafePolicy())

	_, err := svc.EnqueueGenerate("thumbs/photo.jpg", nil, false)
	wantErr(t, err, ErrInvalidRequest)
}

// TestEnqueueGenerateWaitMode — wait=true генерирует все ассеты и возвращает
// полный результат.
func TestEnqueueGenerateWaitMode(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	src.Add(object.ObjectKey("thumbs/photo.jpg"), []byte("JPEG"))
	res := testutil.NewMemResultStore()
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
	src := testutil.NewMemSourceStore()
	src.Add(object.ObjectKey("thumbs/photo.jpg"), []byte("JPEG"))
	res := testutil.NewMemResultStore()
	// Заранее публикуем один канонический ассет, чтобы он был пропущен.
	url := "thumbs/photo-jpg/thumb.webp"
	res.Publish(context.Background(), object.ObjectKey(url), testutil.EmptyReader(), object.PublishOptions{})
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
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	svc.Start(context.Background())
	defer svc.Stop()

	url := "thumbs/photo-jpg/thumb@2.webp"
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
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	// QueueSize=1, workers не запущены → очередь переполняется.
	svc, err := New(Deps{
		Gen:     gen,
		Sources: src,
		Results: res,
		Presets: mustPresetSet(t),
		Policy:  &policy.Policy{},
		Logger:  testutil.NopLogger{},
	}, Config{Workers: 1, QueueSize: 1, WaitTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Первая задача занимает очередь (wait=false, воркеры не запущены).
	_, err = svc.EnqueueGenerate("", []string{"thumbs/photo-jpg/thumb@2.webp"}, false)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// Вторая — переполнение.
	_, err = svc.EnqueueGenerate("", []string{"thumbs/photo-jpg/thumb@3.webp"}, false)
	wantErr(t, err, ErrQueueFull)
}

// TestDeleteBySource — удаление всех ассетов исходника.
func TestDeleteBySource(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	// Публикуем ассеты исходника.
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), testutil.EmptyReader(), object.PublishOptions{})
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb@2.webp"), testutil.EmptyReader(), object.PublishOptions{})
	// Посторонний ассет (другой исходник) — не должен удаляться.
	res.Publish(context.Background(), object.ObjectKey("thumbs/other-jpg/thumb.webp"), testutil.EmptyReader(), object.PublishOptions{})
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
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), testutil.EmptyReader(), object.PublishOptions{})
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb@2.webp"), testutil.EmptyReader(), object.PublishOptions{})
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	deleted, err := svc.DeleteAssets(context.Background(), []string{"thumbs/photo-jpg/thumb.webp", "thumbs/photo-jpg/thumb@2.webp"})
	if err != nil {
		t.Fatalf("DeleteAssets: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
}

// TestDeleteBySourceNotLister — result-хранилище без List и без PrefixDeleter
// больше НЕ возвращает 501: используется «слепое» удаление по ключам,
// сформированным из политик/правил. С пустой политикой перечисление даёт
// пустой список — удаление завершается без ошибки.
func TestDeleteBySourceNotLister(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	// Обычный ResultStore без List.
	res := &nonListerResultStore{}
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	deleted, err := svc.DeleteBySource(context.Background(), "thumbs/photo.jpg")
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 (empty policy enumerates no assets)", deleted)
	}
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

// prefixDeleterResultStore — ResultStore, реализующий storage.PrefixDeleter
// (но НЕ storage.Lister), для проверки пути DeleteByPrefix в DeleteBySource.
type prefixDeleterResultStore struct {
	mu      sync.Mutex
	data    map[string][]byte
	lastPre string
}

func newPrefixDeleterResultStore() *prefixDeleterResultStore {
	return &prefixDeleterResultStore{data: map[string][]byte{}}
}

func (r *prefixDeleterResultStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[key.String()]; !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key}, nil
}
func (r *prefixDeleterResultStore) Open(_ context.Context, _ object.ObjectKey) (object.Artifact, error) {
	return nil, &object.NotFoundError{}
}
func (r *prefixDeleterResultStore) ReadStream(_ context.Context, _ object.ObjectKey) (object.Stream, error) {
	return nil, &object.NotFoundError{}
}
func (r *prefixDeleterResultStore) Publish(_ context.Context, key object.ObjectKey, _ io.Reader, _ object.PublishOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key.String()] = []byte("")
	return nil
}
func (r *prefixDeleterResultStore) Delete(_ context.Context, key object.ObjectKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key.String())
	return nil
}
func (r *prefixDeleterResultStore) Stats(_ context.Context) (object.StoreStats, error) {
	return object.StoreStats{}, nil
}

// DeleteByPrefix удаляет все ключи с префиксом (с граничным '/') и
// запоминает переданный префикс для проверки.
func (r *prefixDeleterResultStore) DeleteByPrefix(_ context.Context, prefix object.ObjectKey) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastPre = prefix.String()
	var n int64
	for k := range r.data {
		if strings.HasPrefix(k, prefix.String()) {
			delete(r.data, k)
			n++
		}
	}
	return n, nil
}

func (r *prefixDeleterResultStore) lastPrefix() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPre
}

var _ storage.ResultStore = (*prefixDeleterResultStore)(nil)
var _ storage.PrefixDeleter = (*prefixDeleterResultStore)(nil)

// TestDeleteBySourcePrefixDeleter — DeleteBySource использует PrefixDeleter,
// когда хранилище его реализует (даже без Lister).
func TestDeleteBySourcePrefixDeleter(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := newPrefixDeleterResultStore()
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), testutil.EmptyReader(), object.PublishOptions{})
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/c-120x80@2.webp"), testutil.EmptyReader(), object.PublishOptions{})
	// Посторонний ассет — не должен удаляться.
	res.Publish(context.Background(), object.ObjectKey("thumbs/other-jpg/thumb.webp"), testutil.EmptyReader(), object.PublishOptions{})
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})

	deleted, err := svc.DeleteBySource(context.Background(), "thumbs/photo.jpg")
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if got := res.lastPrefix(); got != "thumbs/photo-jpg/" {
		t.Errorf("DeleteByPrefix prefix = %q, want %q", got, "thumbs/photo-jpg/")
	}
	// Посторонний ассет остался.
	if _, err := res.Lookup(context.Background(), "thumbs/other-jpg/thumb.webp"); err != nil {
		t.Errorf("unrelated asset should remain: %v", err)
	}
}

// blockingGenerator — Generator, который блокируется до отмены контекста.
// Используется для проверки отмены задачи при wait-timeout.
type blockingGenerator struct {
	mu       sync.Mutex
	started  chan struct{}
	released chan struct{}
}

func newBlockingGenerator() *blockingGenerator {
	return &blockingGenerator{started: make(chan struct{}), released: make(chan struct{})}
}

func (b *blockingGenerator) Generate(ctx context.Context, _ *asset.Request) (*generatev2.Result, error) {
	close(b.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.released:
		return &generatev2.Result{URL: "x", Key: object.ObjectKey("x")}, nil
	}
}

// TestEnqueueGenerateWaitTimeoutCancels — при ErrWaitTimeout контекст задачи
// отменяется, и воркер может прервать генерацию (нет утечки).
func TestEnqueueGenerateWaitTimeoutCancels(t *testing.T) {
	gen := newBlockingGenerator()
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	svc, err := New(Deps{
		Gen:     gen,
		Sources: src,
		Results: res,
		Presets: mustPresetSet(t),
		Policy:  &policy.Policy{},
		Logger:  testutil.NopLogger{},
	}, Config{Workers: 1, QueueSize: 4, WaitTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Start(context.Background())
	defer svc.Stop()

	_, err = svc.EnqueueGenerate("", []string{"thumbs/photo-jpg/thumb.webp"}, true)
	wantErr(t, err, ErrWaitTimeout)

	// Воркер должен получить отмену контекста и завершиться.
	select {
	case <-gen.started:
	case <-time.After(2 * time.Second):
		t.Fatal("generator never started")
	}
	select {
	case <-gen.released:
		// Не должен быть освобождён вручную — отмена должна сработать.
		t.Fatal("generator was released manually, expected context cancellation")
	case <-time.After(2 * time.Second):
		// Ожидаем, что воркер завершился по отмене контекста.
	}
}

// TestConcurrentEnqueueStop — конкурентные enqueue и Stop не вызывают panic
// (гонка «send on closed channel»).
func TestConcurrentEnqueueStop(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	svc := newTestService(t, gen, src, res, mustPresetSet(t), &policy.Policy{})
	svc.Start(context.Background())

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = svc.EnqueueGenerate("", []string{"thumbs/photo-jpg/thumb.webp"}, false)
				}
			}
		}()
	}
	// Даём горутинам поработать, затем останавливаем сервис.
	time.Sleep(20 * time.Millisecond)
	svc.Stop()
	close(stop)
	wg.Wait()
}

// blindResultStore — ResultStore БЕЗ List и БЕЗ PrefixDeleter, но с
// реальным хранением данных. Используется для проверки «слепого» удаления
// по ключам, сформированным из политик/правил.
type blindResultStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newBlindResultStore() *blindResultStore {
	return &blindResultStore{data: map[string][]byte{}}
}

func (r *blindResultStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key.String()]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}
func (r *blindResultStore) Open(_ context.Context, _ object.ObjectKey) (object.Artifact, error) {
	return nil, &object.NotFoundError{}
}
func (r *blindResultStore) ReadStream(_ context.Context, _ object.ObjectKey) (object.Stream, error) {
	return nil, &object.NotFoundError{}
}
func (r *blindResultStore) Publish(_ context.Context, key object.ObjectKey, src io.Reader, _ object.PublishOptions) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key.String()] = data
	return nil
}
func (r *blindResultStore) Delete(_ context.Context, key object.ObjectKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key.String())
	return nil
}
func (r *blindResultStore) Stats(_ context.Context) (object.StoreStats, error) {
	return object.StoreStats{}, nil
}

var _ storage.ResultStore = (*blindResultStore)(nil)

// TestDeleteBySourceBlindKeys — «слепое» удаление по ключам, сформированным
// из политик/правил, для хранилища без List и без PrefixDeleter.
func TestDeleteBySourceBlindKeys(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := newBlindResultStore()
	// Публикуем ассеты, которые должны быть сформированы политикой/пресетами.
	// safePolicy разрешает пресет "thumb" (dpr=1, output webp) → ключ
	// "thumbs/photo-jpg/thumb.webp".
	res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), testutil.EmptyReader(), object.PublishOptions{})
	// Посторонний ассет — не должен удаляться (вне сформированных ключей).
	res.Publish(context.Background(), object.ObjectKey("thumbs/other-jpg/thumb.webp"), testutil.EmptyReader(), object.PublishOptions{})
	svc := newTestService(t, gen, src, res, mustPresetSet(t, mustPreset(t, "thumb", "120x80", "webp")), safePolicy())

	deleted, err := svc.DeleteBySource(context.Background(), "thumbs/photo.jpg")
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (preset thumb.webp)", deleted)
	}
	// Посторонний ассет остался.
	if _, err := res.Lookup(context.Background(), object.ObjectKey("thumbs/other-jpg/thumb.webp")); err != nil {
		t.Errorf("unrelated asset should remain: %v", err)
	}
}

// TestDeleteBySourceRemovesMeta — DeleteBySource удаляет sidecar-метаданные
// родителя (.meta.json).
func TestDeleteBySourceRemovesMeta(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	meta := newMemMetadataStore()
	// Sidecar родителя: ключ = каталог ассета + имя файла.
	meta.Save(context.Background(), "thumbs/photo-jpg/x", filemeta.NewFileMetadata())
	svc := newTestServiceMeta(t, gen, src, res, mustPresetSet(t), &policy.Policy{}, meta)

	if _, err := svc.DeleteBySource(context.Background(), "thumbs/photo.jpg"); err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if m := meta.get("thumbs/photo-jpg/x"); m != nil {
		t.Error("metadata sidecar should be removed after DeleteBySource")
	}
}

// TestDeleteAssetsClearsLargestAIAsset — DeleteAssets очищает largest_ai_asset,
// если удаляется именно этот ассет, но НЕ удаляет сам .meta.json.
func TestDeleteAssetsClearsLargestAIAsset(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	meta := newMemMetadataStore()
	// Sidecar с зафиксированным largest_ai_asset.
	url := "thumbs/photo-jpg/thumb.webp"
	m := filemeta.NewFileMetadata()
	m.LargestAIAsset = &filemeta.AIAssetInfo{Width: 2000, Height: 2000, Format: "webp", Key: url}
	meta.Save(context.Background(), "thumbs/photo-jpg/x", m)
	svc := newTestServiceMeta(t, gen, src, res, mustPresetSet(t), &policy.Policy{}, meta)

	if _, err := svc.DeleteAssets(context.Background(), []string{url}); err != nil {
		t.Fatalf("DeleteAssets: %v", err)
	}
	// .meta.json остался, но largest_ai_asset очищен.
	got := meta.get("thumbs/photo-jpg/x")
	if got == nil {
		t.Fatal("metadata sidecar should remain after DeleteAssets")
	}
	if got.LargestAIAsset != nil {
		t.Errorf("largest_ai_asset should be cleared, got %+v", got.LargestAIAsset)
	}
}

// TestDeleteAssetsKeepsLargestAIAssetOther — DeleteAssets НЕ трогает
// largest_ai_asset, если удаляется другой ассет.
func TestDeleteAssetsKeepsLargestAIAssetOther(t *testing.T) {
	gen := newFakeGenerator()
	src := testutil.NewMemSourceStore()
	res := testutil.NewMemResultStore()
	meta := newMemMetadataStore()
	url := "thumbs/photo-jpg/thumb.webp"
	m := filemeta.NewFileMetadata()
	m.LargestAIAsset = &filemeta.AIAssetInfo{Width: 2000, Height: 2000, Format: "webp", Key: url}
	meta.Save(context.Background(), "thumbs/photo-jpg/x", m)
	svc := newTestServiceMeta(t, gen, src, res, mustPresetSet(t), &policy.Policy{}, meta)

	// Удаляем другой ассет.
	if _, err := svc.DeleteAssets(context.Background(), []string{"thumbs/photo-jpg/other.webp"}); err != nil {
		t.Fatalf("DeleteAssets: %v", err)
	}
	got := meta.get("thumbs/photo-jpg/x")
	if got == nil || got.LargestAIAsset == nil {
		t.Fatal("largest_ai_asset should remain when deleting a different asset")
	}
}
