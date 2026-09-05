package generatev2

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/internal/testutil"
	"github.com/pkg-ru/imager/observability"
)

// fakePubMetrics — observability.Metrics + PublishQueueMetrics со счётчиками
// глубины очереди и publish-ошибок (для проверки S1-метрик).
type fakePubMetrics struct {
	queueDepth    atomic.Int64
	publishErrors atomic.Int64
}

func (f *fakePubMetrics) IncRequests(observability.StatusClass)                               {}
func (f *fakePubMetrics) ObserveRequestDuration(observability.StatusClass, time.Duration)     {}
func (f *fakePubMetrics) IncCacheHit()                                                        {}
func (f *fakePubMetrics) IncCacheMiss()                                                       {}
func (f *fakePubMetrics) IncProcessorSuccess()                                                {}
func (f *fakePubMetrics) IncProcessorError()                                                  {}
func (f *fakePubMetrics) ObserveProcessorDuration(time.Duration)                              {}
func (f *fakePubMetrics) IncStorageOp(observability.StorageOp, bool)                          {}
func (f *fakePubMetrics) ObserveStorageDuration(observability.StorageOp, bool, time.Duration) {}
func (f *fakePubMetrics) IncAssetError(observability.AssetErrorKind)                          {}

func (f *fakePubMetrics) SetPublishQueueDepth(v int64) { f.queueDepth.Store(v) }
func (f *fakePubMetrics) IncPublishError()             { f.publishErrors.Add(1) }

var _ observability.Metrics = (*fakePubMetrics)(nil)
var _ observability.PublishQueueMetrics = (*fakePubMetrics)(nil)

// slowResultStore — ResultStore, у которого ПЕРВЫЙ вызов Publish блокируется
// до тех пор, пока не будет закрыт канал release (эмуляция медленной записи
// в remote). started закрывается при входе в первый Publish — детерминированный
// барьер «воркер взял задачу и начал писать».
type slowResultStore struct {
	*testutil.MemResultStore
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
}

func newSlowResultStore(release chan struct{}) *slowResultStore {
	return &slowResultStore{
		MemResultStore: testutil.NewMemResultStore(),
		started:        make(chan struct{}),
		release:        release,
	}
}

// waitStarted блокирует тест до первого вызова Publish (или таймаут).
//
// Гонка: воркер может вызвать Publish (и закрыть started, обнулив его) ДО
// того, как тест дойдёт до этого метода. Чтение из nil-канала заблокировало
// бы тест навсегда, поэтому вместо select используем детерминированный
// поллинг по признаку «первый Publish уже выполнялся» (started == nil или
// канал закрыт).
func (s *slowResultStore) waitStarted(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		done := false
		s.mu.Lock()
		done = s.started == nil
		s.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for first Publish to start")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (s *slowResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) error {
	s.mu.Lock()
	var release chan struct{}
	if s.started != nil {
		close(s.started)
		s.started = nil
		release = s.release
	}
	s.mu.Unlock()

	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.MemResultStore.Publish(ctx, key, src, opts)
}

// waitStore блокирует тест, пока объект с ключом не появится в хранилище
// (или не истечёт таймаут) — поллинг с малой паузой.
func waitStore(t *testing.T, st *testutil.MemResultStore, key object.ObjectKey) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st.Has(key) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for object %q in store", key)
}

// asyncEnv собирает Service с асинхронной публикацией (S1).
func asyncEnv(t *testing.T, res *slowResultStore, metrics observability.Metrics, q *PublishQueueConfig) *testEnv {
	t.Helper()
	env := newTestEnv(t, func(d *Deps) {
		d.Results = res
		d.PublishQueue = q
		if metrics != nil {
			d.Metrics = metrics
		}
	})
	return env
}

// TestAsyncPublishReturnsBeforePublishCompletes — ответ клиенту возвращается
// ДО завершения фоновой публикации в remote: данные читаются из буфера,
// а в хранилище результат появляется только после разблокировки воркера.
func TestAsyncPublishReturnsBeforePublishCompletes(t *testing.T) {
	release := make(chan struct{})
	res := newSlowResultStore(release)
	env := asyncEnv(t, res, nil, &PublishQueueConfig{Workers: 1, QueueSize: 4, DrainTimeout: 3 * time.Second})
	defer env.svc.Close()

	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	// Generate возвращается сразу; воркер начинает публикацию и блокируется.
	// Барьер ДО чтения данных: данныые должны быть доступны из буфера
	// независимо от того, завершилась публикация или нет.
	result, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	key := result.Key

	// Пока воркер не завершил публикацию (он блокируется в Publish), объект
	// отсутствует в хранилище — это доказывает асинхронность ответа.
	res.waitStarted(t)
	if res.Has(key) {
		t.Fatal("result already in store while async publish is blocked")
	}

	data, err := io.ReadAll(result.Opened)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data) != "IMG" {
		t.Fatalf("data = %q, want IMG", data)
	}
	_ = result.Close()

	// Разблокируем воркера: публикация завершается, объект появляется.
	close(release)
	waitStore(t, res.MemResultStore, key)
}

// TestAsyncPublishGracefulShutdownDrains — Close (graceful drain) дожидается
// завершения уже принятых задач: пока воркер пишет, Close не завершается;
// после завершения публикации объект гарантированно в хранилище.
func TestAsyncPublishGracefulShutdownDrains(t *testing.T) {
	release := make(chan struct{})
	res := newSlowResultStore(release)
	env := asyncEnv(t, res, nil, &PublishQueueConfig{Workers: 1, QueueSize: 4, DrainTimeout: 3 * time.Second})

	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	result, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	key := result.Key
	_ = result.Close()

	// Воркер начал публикацию и блокируется.
	res.waitStarted(t)

	closing := make(chan error, 1)
	go func() { closing <- env.svc.Close() }()

	// Close НЕ должен завершиться, пока воркер пишет (drain ждёт задачу).
	select {
	case err := <-closing:
		t.Fatalf("Close finished while publish blocked: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	// Разблокируем публикацию: Close дожидается задачи, объект в хранилище.
	close(release)
	select {
	case err := <-closing:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not drain queued publish")
	}
	if !res.Has(key) {
		t.Fatalf("object %q missing after graceful drain", key)
	}

	// Close идемпотентен (sync.Once) — повторный вызов безопасен.
	if err := env.svc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestAsyncPublishQueueOverflowSyncFallback — при переполнении bounded-очереди
// публикация выполняется СИНХРОННО (fallback): результат не теряется и
// сразу (без ожидания очереди) попадает в хранилище.
func TestAsyncPublishQueueOverflowSyncFallback(t *testing.T) {
	release := make(chan struct{})
	res := newSlowResultStore(release)
	// Очередь ёмкостью 1 и один воркер: первая задача блокируется в Publish,
	// вторая занимает очередь, третья переполняет её → sync fallback.
	env := asyncEnv(t, res, nil, &PublishQueueConfig{Workers: 1, QueueSize: 1, DrainTimeout: 3 * time.Second})
	var closeOnce sync.Once
	closeRelease := func() { closeOnce.Do(func() { close(release) }) }
	defer func() {
		closeRelease()
		_ = env.svc.Close()
	}()

	for _, src := range []object.ObjectKey{"a.png", "b.png", "c.png"} {
		env.src.Add(src, []byte("SRC"))
	}

	ctx := context.Background()
	gen := func(name string) (*Result, error) {
		req := mustReq(t, "", name, "png", asset.TransformCrop, "100x100", 2, "webp")
		return env.svc.Generate(ctx, req)
	}

	// Первый — в воркер (блокируется в Publish).
	r1, err := gen("a")
	if err != nil {
		t.Fatalf("Generate(a): %v", err)
	}
	keyA := r1.Key
	env.svc.publishQueueDepthGauge()
	_ = r1.Close()

	// Второй — в очередь (ёимость 1).
	r2, err := gen("b")
	if err != nil {
		t.Fatalf("Generate(b): %v", err)
	}
	keyB := r2.Key
	_ = r2.Close()

	// Третий — переполнение очереди → синхронный fallback.
	r3, err := gen("c")
	if err != nil {
		t.Fatalf("Generate(c): %v", err)
	}
	keyC := r3.Key
	if s := env.svc.publishQueueDepth(); s != 1 {
		t.Fatalf("queue depth = %d, want 1 (second task queued)", s)
	}
	_ = r3.Close()

	// Sync fallback записал объект СРАЗУ, не дожидаясь очереди.
	if !res.Has(keyC) {
		t.Fatal("sync fallback did not publish object immediately")
	}

	// Разблокируем воркера: завершаются публикации a и b.
	closeRelease()
	waitStore(t, res.MemResultStore, keyA)
	waitStore(t, res.MemResultStore, keyB)
}

// TestAsyncPublishRepeatedGenerationNoOverwrite — повторный запрос того же
// ассета до завершения фоновой публикации не ломает NoOverwrite/конкурентную
// публикацию: обе публикации завершаются успешно, результат в кэше ОДИН.
func TestAsyncPublishRepeatedGenerationNoOverwrite(t *testing.T) {
	release := make(chan struct{})
	res := newSlowResultStore(release)
	env := asyncEnv(t, res, nil, &PublishQueueConfig{Workers: 1, QueueSize: 4, DrainTimeout: 3 * time.Second})
	var closeOnce sync.Once
	closeRelease := func() { closeOnce.Do(func() { close(release) }) }
	defer func() {
		closeRelease()
		_ = env.svc.Close()
	}()

	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	r1, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate #1: %v", err)
	}
	key := r1.Key
	_ = r1.Close()

	// Публикация #1 ещё в очереди (воркер может даже не начать). Повторный
	// запрос того же ключа: tryCache → miss (publish ещё не завершён) →
	// повторная генерация → повторная публикация того же ключа.
	r2, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate #2: %v", err)
	}
	if r2.Key != key {
		t.Fatalf("key = %q, want %q (same asset)", r2.Key, key)
	}
	data, _ := io.ReadAll(r2.Opened)
	if string(data) != "IMG" {
		t.Fatalf("data = %q, want IMG", data)
	}
	_ = r2.Close()

	// Обе публикации (воркер + повторная) завершаются без ошибок; конфликт
	// NoOverwrite (если бы хранилище его вернуло) трактуется как успех.
	closeRelease()
	waitStore(t, res.MemResultStore, key)
	if _, err := res.Stats(context.Background()); err != nil {
		t.Fatalf("Stats: %v", err)
	}
}

// TestAsyncPublishRetryExhaustionLoggedAndMetric — при исчерпании retry/backoff
// публикация НЕ ломает ответ (клиент получил буфер), ошибка логируется,
// инкрементируется метрика publish-ошибок, а в кэш результат НЕ попадает.
func TestAsyncPublishRetryExhaustionLoggedAndMetric(t *testing.T) {
	res := testutil.NewMemResultStore()
	res.SetPubErr(object.ErrUnavailable)
	metrics := &fakePubMetrics{}
	env := asyncEnv(t, &slowResultStore{MemResultStore: res}, metrics, &PublishQueueConfig{
		Workers:      1,
		QueueSize:    4,
		DrainTimeout: 3 * time.Second,
	})
	defer env.svc.Close()

	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	result, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	key := result.Key
	data, _ := io.ReadAll(result.Opened)
	if string(data) != "IMG" {
		t.Fatalf("data = %q, want IMG", data)
	}
	_ = result.Close()

	// Воркер ретраит ErrUnavailable 3 раза (backoff ~50+100ms), затем ошибка
	// логируется и инкрементирует счётчик publish-ошибок.
	deadline := time.Now().Add(3 * time.Second)
	for metrics.publishErrors.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if metrics.publishErrors.Load() != 1 {
		t.Fatalf("publishErrors = %d, want 1", metrics.publishErrors.Load())
	}
	if res.Has(key) {
		t.Fatal("result must NOT be in cache after retry exhaustion")
	}
}

// TestPublishFromBufferConflictIsSuccess — ErrConflict (NoOverwrite, объект уже
// существует) трактуется как УСПЕХ: повторная публикация того же ассета уже
// считает результат находящимся в кэше, ошибки клиенту нет.
func TestPublishFromBufferConflictIsSuccess(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.PublishQueue = &PublishQueueConfig{} // async: Conflict обрабатывается в воркере
	})
	defer env.svc.Close()
	env.res.SetPubErr(&object.ConflictError{Key: "photo/thumb.webp"})

	buf, err := env.svc.newBuffer()
	if err != nil {
		t.Fatalf("newBuffer: %v", err)
	}
	if _, err := buf.Write([]byte("DATA")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader, err := buf.NewReader()
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer reader.Close()
	defer buf.Close()

	if err := env.svc.publishFromBuffer(context.Background(), "photo/thumb.webp", reader); err != nil {
		t.Fatalf("publishFromBuffer: %v, want nil (Conflict = success)", err)
	}
}

// TestPublishQueueDisabledIsSync — при Disabled=true / nil-конфиге публикация
// остаётся синхронной (прежнее поведение): результат в хранилище СРАЗУ после
// Generate, воркеры не запущены.
func TestPublishQueueDisabledIsSync(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.PublishQueue = &PublishQueueConfig{Disabled: true}
	})
	defer env.svc.Close()
	if s := env.svc.publishQueueDepth(); s != 0 {
		t.Fatalf("disabled queue depth = %d, want 0", s)
	}

	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")
	result, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !env.res.Has(result.Key) {
		t.Fatal("result missing from store immediately after Generate (sync publish expected)")
	}
	_ = result.Close()
}
