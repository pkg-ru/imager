// Тесты периодического сборщика vips-метрик (Фаза 4): экспорт gauge-ов,
// отказоустойчивость провайдера (паника/ошибка не ломают сбор), stop.
package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// vipsGauges — все экспортируемые vips-gauge-и.
var vipsGauges = []string{
	"imager_vips_tracked_memory_bytes",
	"imager_vips_tracked_allocs",
	"imager_vips_open_files",
	"imager_vips_mem_highwater_bytes",
	"imager_vips_operations_total",
	"imager_vips_watermark_cache_hits_total",
	"imager_vips_watermark_cache_misses_total",
	"imager_vips_watermark_cache_entries",
	"imager_vips_watermark_cache_bytes",
}

func metricsBody(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// TestVipsMetricsExported проверяет, что после регистрации провайдера все
// vips-gauge-и присутствуют в выводе /metrics со значениями снимка.
func TestVipsMetricsExported(t *testing.T) {
	SetVipsStatsProvider(func() (VipsSnapshot, error) {
		return VipsSnapshot{
			TrackedMemory:         123456,
			TrackedAllocs:         42,
			OpenFiles:             3,
			MemHighwater:          999999,
			OperationsTotal:       777,
			WatermarkCacheHits:    10,
			WatermarkCacheMisses:  2,
			WatermarkCacheEntries: 4,
			WatermarkCacheBytes:   8192,
		}, nil
	}, MinVipsMetricsInterval)

	// Первый тик выполняется немедленно; даём горутине собраться.
	waitFor := func(cond func() bool, msg string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("condition not met: %s", msg)
	}
	// Ждём ПОСЛЕДНИЙ по порядку установки в collectOnce gauge
	// (watermark_cache_bytes): его появление гарантирует, что все остальные
	// gauge-и этого тика уже выставлены — иначе срез /metrics мог бы попасть
	// между установками отдельных gauge-ов одного тика (флак).
	waitFor(func() bool { return strings.Contains(metricsBody(t), "imager_vips_watermark_cache_bytes 8192") },
		"watermark cache bytes value not exported")

	body := metricsBody(t)
	for _, g := range vipsGauges {
		if !strings.Contains(body, g+" ") {
			t.Errorf("metrics missing %s", g)
		}
	}
	if !strings.Contains(body, "imager_vips_watermark_cache_hits_total 10") {
		t.Errorf("watermark cache hits not exported: %q", body)
	}
	if !strings.Contains(body, "imager_vips_watermark_cache_bytes 8192") {
		t.Errorf("watermark cache bytes not exported")
	}
	StopVipsMetrics()
}

// TestVipsMetricsProviderPanicTolerated проверяет отказоустойчивость:
// паника провайдера не должна влиять на collector (значения остаются
// предыдущими, сбор продолжается).
func TestVipsMetricsProviderPanicTolerated(t *testing.T) {
	calls := 0
	SetVipsStatsProvider(func() (VipsSnapshot, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		return VipsSnapshot{TrackedMemory: 555}, nil
	}, MinVipsMetricsInterval)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(metricsBody(t), "imager_vips_tracked_memory_bytes 555") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(metricsBody(t), "imager_vips_tracked_memory_bytes 555") {
		t.Fatal("collector did not recover after provider panic")
	}
	StopVipsMetrics()
}

// TestVipsMetricsProviderErrorIgnored — ошибка провайдера игнорируется так же,
// как паника.
func TestVipsMetricsProviderErrorIgnored(t *testing.T) {
	calls := 0
	SetVipsStatsProvider(func() (VipsSnapshot, error) {
		calls++
		if calls == 1 {
			return VipsSnapshot{}, errors.New("transient failure")
		}
		return VipsSnapshot{TrackedAllocs: 7}, nil
	}, MinVipsMetricsInterval)

	deadline := time.Now().Add(2 * time.Second)
	ok := false
	for time.Now().Before(deadline) {
		if strings.Contains(metricsBody(t), "imager_vips_tracked_allocs 7") {
			ok = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ok {
		t.Fatal("collector did not continue after provider error")
	}
	StopVipsMetrics()
}

// TestSetVipsStatsProviderNilSafe — nil-провайдер игнорируется; Stop до
// запуска безопасен.
func TestSetVipsStatsProviderNilSafe(t *testing.T) {
	SetVipsStatsProvider(nil, 0)
	StopVipsMetrics()
	StopVipsMetrics() // идемпотентно
}
