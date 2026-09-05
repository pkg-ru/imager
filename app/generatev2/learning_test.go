package generatev2

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/object"
)

// fakeLearning — управляемый fake LearningController для тестов.
type fakeLearning struct {
	enabled bool
}

func (f *fakeLearning) Enabled() bool { return f.enabled }

// mustSegmentReq строит segment-запрос (имя пресета/custom).
func mustSegmentReq(t *testing.T, path, srcName, srcFmt, segment string, dpr int, outFmt string) *asset.Request {
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
	seg, err := asset.NewSegmentName(segment)
	if err != nil {
		t.Fatalf("segment name: %v", err)
	}
	r, err := asset.NewSegmentRequest(path, sn, sf, seg, asset.DPR(dpr), of)
	if err != nil {
		t.Fatalf("segment request: %v", err)
	}
	return r
}

// TestLearningForbiddenPathGeneratedNotStored проверяет, что при включённом
// learning-mode запрос, не подходящий по правилам (сегмент — размер-грамматика),
// генерируется и отдаётся клиенту, но НЕ сохраняется в storage.
func TestLearningForbiddenPathGeneratedNotStored(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.Learning = &fakeLearning{enabled: true}
	})
	env.src.Add("forbidden/photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustSegmentReq(t, "forbidden", "photo", "png", "120x60", 0, "webp")

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.FromCache {
		t.Fatal("expected fresh generation, got FromCache=true")
	}
	data, _ := io.ReadAll(res.Opened)
	res.Close()
	if string(data) != "IMG" {
		t.Fatalf("result data = %q, want IMG", data)
	}
	// Storage не должен получить Publish.
	stats, err := env.res.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 0 {
		t.Fatalf("result objects = %d, want 0 (learning-mode must not publish)", stats.Objects)
	}
}

// TestLearningCachedAssetServedFromCache проверяет, что при включённом
// learning-mode ранее сохранённый ассет отдаётся из кэша (FromCache=true).
func TestLearningCachedAssetServedFromCache(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.Learning = &fakeLearning{enabled: true}
	})
	env.src.Add("photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustSegmentReq(t, "forbidden", "photo", "png", "120x60", 0, "webp")

	// Ассет уже существует в storage под каноническим URL.
	url, _, err := asset.NewCanonicalizer().CanonicalizeURL(req)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	env.res.Add(object.ObjectKey(url), []byte("CACHED"))

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !res.FromCache {
		t.Fatal("expected FromCache=true for pre-existing asset")
	}
	data, _ := io.ReadAll(res.Opened)
	res.Close()
	if string(data) != "CACHED" {
		t.Fatalf("result data = %q, want CACHED", data)
	}
	if env.proc.callCount() != 0 {
		t.Fatalf("processor calls = %d, want 0 (cache hit)", env.proc.callCount())
	}
}

// TestLearningAllowedAssetNotStored проверяет, что при включённом learning-mode
// даже разрешённый по правилам ассет НЕ сохраняется в storage.
func TestLearningAllowedAssetNotStored(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.Learning = &fakeLearning{enabled: true}
	})
	env.src.Add("photo.png", []byte("SRC"))

	ctx := context.Background()
	// Пресет thumb разрешён политикой "/" (fallback).
	req := mustSegmentReq(t, "", "photo", "png", "thumb", 0, "webp")

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, _ := io.ReadAll(res.Opened)
	res.Close()
	if string(data) != "IMG" {
		t.Fatalf("result data = %q, want IMG", data)
	}
	stats, err := env.res.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 0 {
		t.Fatalf("result objects = %d, want 0 (learning-mode must not publish)", stats.Objects)
	}
}

// TestLearningOffRegression проверяет, что при выключенном learning-mode
// поведение не изменилось: запрещённый запрос → 403.
func TestLearningOffRegression(t *testing.T) {
	env := newTestEnv(t) // Learning = nil
	env.src.Add("photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustSegmentReq(t, "forbidden", "photo", "png", "120x60", 0, "webp")

	_, err := env.svc.Generate(ctx, req)
	wantOutcome(t, err, OutcomeForbidden)
}

// TestLearningUnknownPresetStillForbidden проверяет, что сегмент — имя
// несуществующего пресета (не размер-грамматика) остаётся 403 даже при
// включённом learning-mode.
func TestLearningUnknownPresetStillForbidden(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.Learning = &fakeLearning{enabled: true}
	})
	env.src.Add("photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustSegmentReq(t, "forbidden", "photo", "png", "banner", 0, "webp")

	_, err := env.svc.Generate(ctx, req)
	wantOutcome(t, err, OutcomeForbidden)
}

// TestLearningConcurrentRequests проверяет, что при конкурентных запросах
// одного и того же ключа при включённом learning-mode оба запроса получают
// валидный результат (без гонки за общий буфер singleflight).
func TestLearningConcurrentRequests(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.Learning = &fakeLearning{enabled: true}
	})
	env.src.Add("forbidden/photo.png", []byte("SRC"))

	// Блокируем процессор, чтобы все запросы вошли в singleflight
	// (детерминированный барьер, как в TestGenerateCacheStampedeSingleFlight).
	block := make(chan struct{})
	env.proc.setBlock(block)

	ctx := context.Background()
	req := mustSegmentReq(t, "forbidden", "photo", "png", "120x60", 0, "webp")

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

	// Ждём, пока ВСЕ запросы войдут в singleflight. В learning-mode нет кэша,
	// поэтому опоздавший запрос (не успевший войти в singleflight до close(block))
	// вызвал бы Process повторно. Дожидаемся, пока callCount() == 1 стабильно
	// в течение окна — это значит, что первый запрос в Process, а остальные
	// уже вошли в singleflight и ждут его результат.
	deadline := time.Now().Add(2 * time.Second)
	for env.proc.callCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1 (singleflight dedup)", env.proc.callCount())
	}
	// Даём остальным запросам войти в singleflight (детерминированный барьер).
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
		data, _ := io.ReadAll(results[i].Opened)
		if string(data) != "IMG" {
			t.Fatalf("Generate[%d] data = %q, want IMG", i, data)
		}
		results[i].Close()
	}
	// Процессор вызывается ровно один раз (singleflight dedup).
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1", env.proc.callCount())
	}
	// Storage пуст.
	stats, err := env.res.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 0 {
		t.Fatalf("result objects = %d, want 0", stats.Objects)
	}
}
