package generatev2

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/coordination/singleflight"
	"github.com/pkg-ru/imager/internal/application/ports/coordinator"
	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/object"
	"github.com/pkg-ru/imager/internal/domain/policy"
)

// testEnv — окружение для тестов.
type testEnv struct {
	svc   *Service
	src   *memSourceStore
	res   *memResultStore
	proc  *fakeProcessor
	coord coordinator.Keyed
}

// newTestEnv собирает Service с fake-зависимостями.
func newTestEnv(t *testing.T, opts ...func(*Deps)) *testEnv {
	t.Helper()

	src := newMemSourceStore()
	res := newMemResultStore()
	proc := newFakeProcessor([]byte("IMG"))
	coord := singleflight.New(singleflight.Options{})

	pol := &policy.Policy{
		Global: policy.GlobalPolicy{
			Authorization: policy.AuthUnsafe,
			Limits:        policy.Unlimited(),
		},
	}
	presets, err := asset.NewPresetSet([]*asset.Preset{
		mustPreset(t, "thumb", asset.TransformCrop, "100x100", "webp"),
	})
	if err != nil {
		t.Fatalf("presets: %v", err)
	}

	deps := Deps{
		Sources:     src,
		Results:     res,
		Coordinator: coord,
		Processor:   proc,
		Policy:      pol,
		Presets:     presets,
		OutputLimit: 0,
		Quality:     85,
		Logger:      fakeLogger{},
	}
	for _, o := range opts {
		o(&deps)
	}

	svc, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testEnv{svc: svc, src: src, res: res, proc: proc, coord: coord}
}

func mustPreset(t *testing.T, name string, tr asset.Transform, size string, outFmt string) *asset.Preset {
	t.Helper()
	of, err := asset.NewFormat(outFmt)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	sz, err := parseSize(size)
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	p, err := asset.NewPreset(name, tr, sz, of)
	if err != nil {
		t.Fatalf("preset: %v", err)
	}
	return p
}

func parseSize(s string) (asset.Size, error) {
	var w, h *asset.Dimension
	// формат "WxH"
	var ws, hs string
	sep := -1
	for i := 0; i < len(s); i++ {
		if s[i] == 'x' {
			sep = i
			break
		}
	}
	if sep < 0 {
		return asset.Size{}, errors.New("bad size")
	}
	ws, hs = s[:sep], s[sep+1:]
	if ws != "" {
		v, err := asset.NewDimension(atoi(ws))
		if err != nil {
			return asset.Size{}, err
		}
		w = &v
	}
	if hs != "" {
		v, err := asset.NewDimension(atoi(hs))
		if err != nil {
			return asset.Size{}, err
		}
		h = &v
	}
	return asset.NewSize(w, h)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// mustReq строит канонический Request.
func mustReq(t *testing.T, path, srcName, srcFmt string, tr asset.Transform, size string, dpr int, outFmt string) *asset.Request {
	t.Helper()
	sn, err := asset.NewSourceName(srcName)
	if err != nil {
		t.Fatalf("source name: %v", err)
	}
	sf, err := asset.NewFormat(srcFmt)
	if err != nil {
		t.Fatalf("source format: %v", err)
	}
	of, err := asset.NewFormat(outFmt)
	if err != nil {
		t.Fatalf("output format: %v", err)
	}
	sz, err := parseSize(size)
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	d, err := asset.NewDPR(dpr)
	if err != nil {
		t.Fatalf("dpr: %v", err)
	}
	r, err := asset.NewRequest(path, sn, sf, tr, sz, d, of)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return r
}

func TestGenerateCacheMissThenHit(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	// Miss: генерация.
	res1, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate miss: %v", err)
	}
	if res1.FromCache {
		t.Fatal("expected miss on first call")
	}
	data1, _ := io.ReadAll(res1.Opened)
	res1.Close()
	if string(data1) != "IMG" {
		t.Fatalf("result data = %q, want IMG", data1)
	}
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1", env.proc.callCount())
	}

	// Hit: кэш, генерация не повторяется.
	res2, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate hit: %v", err)
	}
	if !res2.FromCache {
		t.Fatal("expected cache hit")
	}
	data2, _ := io.ReadAll(res2.Opened)
	res2.Close()
	if string(data2) != "IMG" {
		t.Fatalf("hit data = %q, want IMG", data2)
	}
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls after hit = %d, want 1", env.proc.callCount())
	}
}

func TestGeneratePresetResolves(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photos/photo.png", []byte("SRC"))

	ctx := context.Background()
	// Preset-запрос: photos/photo-png/thumb@2.webp
	sn, _ := asset.NewSourceName("photo")
	sf, _ := asset.NewFormat("png")
	pn, _ := asset.NewPresetName("thumb")
	of, _ := asset.NewFormat("webp")
	d, _ := asset.NewDPR(2)
	preq, err := asset.NewPresetRequest("photos", sn, sf, pn, d, of)
	if err != nil {
		t.Fatalf("preset request: %v", err)
	}

	res, err := env.svc.Generate(ctx, preq)
	if err != nil {
		t.Fatalf("Generate preset: %v", err)
	}
	defer res.Close()
	if res.Request.IsPreset() {
		t.Fatal("resolved request must be canonical")
	}
	if res.Request.Size().String() != "100x100" {
		t.Fatalf("resolved size = %s, want 100x100", res.Request.Size().String())
	}
}

// TestGenerateKeyIsCanonicalURL проверяет, что Result.Key (и, следовательно,
// ключ ResultStore/кэша) равен каноническому URL, а не SHA-256 хешу.
func TestGenerateKeyIsCanonicalURL(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photos/photo.png", []byte("SRC"))

	ctx := context.Background()
	// Preset-запрос: photos/photo-png/thumb@2.webp раскрывается в
	// photos/photo-png/c-100x100@2.webp.
	sn, _ := asset.NewSourceName("photo")
	sf, _ := asset.NewFormat("png")
	pn, _ := asset.NewPresetName("thumb")
	of, _ := asset.NewFormat("webp")
	d, _ := asset.NewDPR(2)
	preq, err := asset.NewPresetRequest("photos", sn, sf, pn, d, of)
	if err != nil {
		t.Fatalf("preset request: %v", err)
	}

	res, err := env.svc.Generate(ctx, preq)
	if err != nil {
		t.Fatalf("Generate preset: %v", err)
	}
	defer res.Close()

	want := object.ObjectKey("photos/photo-png/c-100x100@2.webp")
	if res.Key != want {
		t.Fatalf("Result.Key = %q, want canonical URL %q", res.Key, want)
	}
	if res.URL != string(want) {
		t.Fatalf("Result.URL = %q, want %q", res.URL, want)
	}

	// Повторный запрос должен попасть в кэш по тому же каноническому ключу.
	res2, err := env.svc.Generate(ctx, preq)
	if err != nil {
		t.Fatalf("Generate preset (hit): %v", err)
	}
	defer res2.Close()
	if !res2.FromCache {
		t.Fatal("expected cache hit on second preset request")
	}
	if res2.Key != want {
		t.Fatalf("hit Result.Key = %q, want %q", res2.Key, want)
	}
}

func TestGenerateForbidden(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.Policy = &policy.Policy{
			Global: policy.GlobalPolicy{
				Authorization: policy.AuthSafe,
				SizeRules:     []policy.SizeRule{{Width: &policy.Range{Min: 100, Max: 100}, Height: &policy.Range{Min: 100, Max: 100}}},
				Limits:        policy.Unlimited(),
			},
		}
	})

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "1x1", 2, "webp")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	if !IsOutcome(err, OutcomeForbidden) {
		t.Fatalf("kind = %v, want forbidden", err)
	}
}

func TestGenerateNotFound(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	req := mustReq(t, "", "missing", "png", asset.TransformCrop, "100x100", 2, "webp")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !IsOutcome(err, OutcomeNotFound) {
		t.Fatalf("kind = %v, want not-found", err)
	}
}

func TestGenerateConcurrentSameKeyDedup(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))

	// Блокируем процессор, чтобы оба запроса попали в singleflight.
	block := make(chan struct{})
	env.proc.setBlock(block)

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	results := make([]*Result, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = env.svc.Generate(ctx, req)
		}(i)
	}

	// Ждём, пока все запросы войдут в singleflight.
	time.Sleep(50 * time.Millisecond)
	close(block)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Generate[%d]: %v", i, errs[i])
		}
		if results[i] == nil {
			t.Fatalf("Generate[%d]: nil result", i)
		}
		results[i].Close()
	}
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1 (dedup)", env.proc.callCount())
	}
}

func TestGenerateCanceled(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))

	block := make(chan struct{})
	env.proc.setBlock(block)

	ctx, cancel := context.WithCancel(context.Background())
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")

	done := make(chan error, 1)
	go func() {
		_, err := env.svc.Generate(ctx, req)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	close(block)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected canceled error")
		}
		if !IsOutcome(err, OutcomeCanceled) {
			t.Fatalf("kind = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not return after cancel")
	}
}

func TestGenerateProcessorError(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))
	env.proc.setErr(errors.New("boom"))

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected processing error")
	}
	if !IsOutcome(err, OutcomeProcessing) {
		t.Fatalf("kind = %v, want processing", err)
	}
}

func TestGeneratePublishError(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))
	env.res.pubErr = object.ErrUnavailable

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if !IsOutcome(err, OutcomeUnavailable) {
		t.Fatalf("kind = %v, want unavailable", err)
	}
}

func TestGenerateOutputLimit(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.OutputLimit = 2 // payload "IMG" = 3 байта > 2
	})
	env.src.add("photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected quota error")
	}
	if !IsOutcome(err, OutcomeQuota) {
		t.Fatalf("kind = %v, want quota", err)
	}
}

func TestGenerateQuotaOnPublish(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))
	env.res.pubErr = object.ErrQuota

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "webp")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected quota error")
	}
	if !IsOutcome(err, OutcomeQuota) {
		t.Fatalf("kind = %v, want quota", err)
	}
}

func TestGenerateInvalidPlan(t *testing.T) {
	env := newTestEnv(t)
	env.src.add("photo.png", []byte("SRC"))

	ctx := context.Background()
	// Неподдерживаемый выходной формат → invalid.
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 2, "tiff")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected invalid error")
	}
	if !IsOutcome(err, OutcomeInvalid) {
		t.Fatalf("kind = %v, want invalid", err)
	}
}

func TestGenerateNilRequest(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.svc.Generate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected invalid error")
	}
	if !IsOutcome(err, OutcomeInvalid) {
		t.Fatalf("kind = %v, want invalid", err)
	}
}
