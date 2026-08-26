// Unit-тесты кэша файлов ватермарок (watermarkcache.go). Чистая логика без
// build-tag: LRU-вытеснение, бюджет байтов, TTL, инвалидация по mtime/размеру,
// singleflight, отключённый кэш.
package libvips

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestCache создаёт кэш с инъекцией фиктивного времени.
func newTestCache(t *testing.T, opts WatermarkCacheOpts) (*watermarkCache, *time.Time) {
	t.Helper()
	c := newWatermarkCache(opts)
	base := time.Unix(1700000000, 0)
	c.now = func() time.Time { return base }
	return c, &base
}

func TestWatermarkCacheHitAndMiss(t *testing.T) {
	c, _ := newTestCache(t, DefaultWatermarkCacheOpts())
	mt := time.Unix(1600000000, 0)
	calls := 0
	loader := func() ([]byte, error) {
		calls++
		return []byte("data"), nil
	}
	d1, err := c.getOrLoad("/wm/logo.png", mt, 4, loader)
	if err != nil || string(d1) != "data" {
		t.Fatalf("first load: %q, %v", d1, err)
	}
	d2, err := c.getOrLoad("/wm/logo.png", mt, 4, loader)
	if err != nil || string(d2) != "data" {
		t.Fatalf("second load: %q, %v", d2, err)
	}
	if calls != 1 {
		t.Fatalf("loader called %d times, want 1 (cache hit expected)", calls)
	}
}

func TestWatermarkCacheInvalidationOnFileChange(t *testing.T) {
	c, _ := newTestCache(t, DefaultWatermarkCacheOpts())
	mt := time.Unix(1600000000, 0)
	calls := 0
	loader := func() ([]byte, error) {
		calls++
		return []byte{byte(calls)}, nil
	}
	d1, _ := c.getOrLoad("k", mt, 10, loader)
	// Изменился размер файла → перезагрузка.
	d2, _ := c.getOrLoad("k", mt, 11, loader)
	if d1[0] == d2[0] {
		t.Fatal("size change must invalidate entry")
	}
	// Изменился mtime → перезагрузка.
	d3, _ := c.getOrLoad("k", mt.Add(time.Second), 11, loader)
	if d2[0] == d3[0] {
		t.Fatal("mtime change must invalidate entry")
	}
	if calls != 3 {
		t.Fatalf("loader called %d times, want 3", calls)
	}
}

func TestWatermarkCacheTTL(t *testing.T) {
	c, base := newTestCache(t, DefaultWatermarkCacheOpts())
	mt := time.Unix(1600000000, 0)
	calls := 0
	loader := func() ([]byte, error) {
		calls++
		return []byte("x"), nil
	}
	if _, err := c.getOrLoad("k", mt, 1, loader); err != nil {
		t.Fatalf("load: %v", err)
	}
	// В пределах TTL — попадание.
	*base = base.Add(DefaultWatermarkCacheTTL - time.Second)
	if _, err := c.getOrLoad("k", mt, 1, loader); err != nil {
		t.Fatalf("load within ttl: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 within TTL", calls)
	}
	// После TTL — перезагрузка.
	*base = base.Add(2 * time.Second)
	if _, err := c.getOrLoad("k", mt, 1, loader); err != nil {
		t.Fatalf("load after ttl: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 after TTL", calls)
	}
}

func TestWatermarkCacheLRUEvictionByFiles(t *testing.T) {
	opts := WatermarkCacheOpts{Enabled: true, MaxFiles: 2, MaxBytes: 1 << 20, TTL: time.Minute}
	c, _ := newTestCache(t, opts)
	mt := time.Unix(1600000000, 0)
	loader := func() ([]byte, error) { return []byte("ab"), nil }
	for _, k := range []string{"a", "b"} {
		if _, err := c.getOrLoad(k, mt, 2, loader); err != nil {
			t.Fatalf("load %s: %v", k, err)
		}
	}
	// Touch "a" — становится недавно использованным.
	if _, err := c.getOrLoad("a", mt, 2, loader); err != nil {
		t.Fatalf("touch a: %v", err)
	}
	// Добавление "c" вытесняет "b" (LRU), а не "a".
	if _, err := c.getOrLoad("c", mt, 2, loader); err != nil {
		t.Fatalf("load c: %v", err)
	}
	if got := c.len(); got != 2 {
		t.Fatalf("len = %d, want 2", got)
	}
	if _, err := c.getOrLoad("a", mt, 2, loader); err != nil {
		t.Fatalf("a should survive: %v", err)
	}
	// "b" была вытеснена — проверяем через totalBytes: 2 записи по 2 байта.
	if got := c.totalBytes(); got != 4 {
		t.Fatalf("totalBytes = %d, want 4", got)
	}
}

func TestWatermarkCacheEvictionByBytes(t *testing.T) {
	opts := WatermarkCacheOpts{Enabled: true, MaxFiles: 100, MaxBytes: 10, TTL: time.Minute}
	c, _ := newTestCache(t, opts)
	mt := time.Unix(1600000000, 0)
	big := make([]byte, 6)
	loader := func() ([]byte, error) { return big, nil }
	for _, k := range []string{"a", "b"} {
		if _, err := c.getOrLoad(k, mt, 6, loader); err != nil {
			t.Fatalf("load %s: %v", k, err)
		}
	}
	// Бюджет 10 байт: после вставки второй записи первая вытеснена.
	if got := c.totalBytes(); got > 10 {
		t.Fatalf("totalBytes = %d, exceeds budget 10", got)
	}
	if got := c.len(); got != 1 {
		t.Fatalf("len = %d, want 1 after byte-budget eviction", got)
	}
}

func TestWatermarkCacheOversizedFileNotCached(t *testing.T) {
	opts := WatermarkCacheOpts{Enabled: true, MaxFiles: 8, MaxBytes: 4, TTL: time.Minute}
	c, _ := newTestCache(t, opts)
	mt := time.Unix(1600000000, 0)
	calls := 0
	loader := func() ([]byte, error) {
		calls++
		return make([]byte, 100), nil
	}
	if _, err := c.getOrLoad("big", mt, 100, loader); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := c.getOrLoad("big", mt, 100, loader); err != nil {
		t.Fatalf("load 2: %v", err)
	}
	if calls != 2 {
		t.Fatalf("oversized file must not be cached; calls = %d", calls)
	}
	if c.totalBytes() != 0 {
		t.Fatalf("totalBytes = %d, want 0", c.totalBytes())
	}
}

func TestWatermarkCacheDisabled(t *testing.T) {
	opts := WatermarkCacheOpts{Enabled: false}
	c, _ := newTestCache(t, opts)
	mt := time.Unix(1600000000, 0)
	calls := 0
	loader := func() ([]byte, error) {
		calls++
		return []byte("d"), nil
	}
	for i := 0; i < 3; i++ {
		if _, err := c.getOrLoad("k", mt, 1, loader); err != nil {
			t.Fatalf("load: %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("disabled cache must always load from disk; calls = %d", calls)
	}
}

func TestWatermarkCacheLoaderErrorNotCached(t *testing.T) {
	c, _ := newTestCache(t, DefaultWatermarkCacheOpts())
	mt := time.Unix(1600000000, 0)
	wantErr := errors.New("disk failure")
	calls := 0
	loader := func() ([]byte, error) {
		calls++
		return nil, wantErr
	}
	if _, err := c.getOrLoad("k", mt, 1, loader); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if _, err := c.getOrLoad("k", mt, 1, loader); !errors.Is(err, wantErr) {
		t.Fatalf("err 2 = %v, want %v", err, wantErr)
	}
	if calls != 2 {
		t.Fatalf("failed loads must not be cached; calls = %d", calls)
	}
	if c.len() != 0 {
		t.Fatalf("cache must stay empty after errors; len = %d", c.len())
	}
}

func TestWatermarkCacheSingleflight(t *testing.T) {
	c, _ := newTestCache(t, DefaultWatermarkCacheOpts())
	mt := time.Unix(1600000000, 0)
	var calls int32
	release := make(chan struct{})
	loader := func() ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		<-release // блокируем загрузку, пока не соберутся параллельные вызовы
		return []byte("shared"), nil
	}
	const n = 8
	var wg sync.WaitGroup
	results := make([][]byte, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = c.getOrLoad("k", mt, 6, loader)
		}(i)
	}
	// Даём горутинам стартовать и присоединиться к singleflight.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("loader called %d times concurrently, want 1", got)
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if string(results[i]) != "shared" {
			t.Fatalf("call %d: result %q, want shared", i, results[i])
		}
	}
}

func TestWatermarkCacheConcurrentAccess(t *testing.T) {
	c, _ := newTestCache(t, WatermarkCacheOpts{Enabled: true, MaxFiles: 4, MaxBytes: 64, TTL: time.Minute})
	mt := time.Unix(1600000000, 0)
	loader := func() ([]byte, error) { return make([]byte, 16), nil }
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%8))
			if _, err := c.getOrLoad(key, mt, 16, loader); err != nil {
				t.Errorf("load %q: %v", key, err)
			}
		}(i)
	}
	wg.Wait()
	if got := c.totalBytes(); got > 64 {
		t.Fatalf("totalBytes = %d, exceeds budget", got)
	}
}

func TestWatermarkCacheOptsValidate(t *testing.T) {
	if err := (WatermarkCacheOpts{MaxFiles: -1}).Validate(); err == nil {
		t.Fatal("negative max-files must fail validation")
	}
	if err := (WatermarkCacheOpts{MaxBytes: -1}).Validate(); err == nil {
		t.Fatal("negative max-bytes must fail validation")
	}
	if err := (WatermarkCacheOpts{TTL: -time.Second}).Validate(); err == nil {
		t.Fatal("negative ttl must fail validation")
	}
	if err := (WatermarkCacheOpts{}).Validate(); err != nil {
		t.Fatalf("zero opts are valid defaults: %v", err)
	}
	n := WatermarkCacheOpts{}.Normalized()
	d := DefaultWatermarkCacheOpts()
	if n.MaxFiles != d.MaxFiles || n.MaxBytes != d.MaxBytes || n.TTL != d.TTL {
		t.Fatalf("normalized = %+v, want defaults %+v", n, d)
	}
}
