package observability

import (
	"expvar"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// MetricsHandler возвращает http.Handler, отдающий метрики в текстовом
// Prometheus exposition format (без внешних зависимостей). Данные берутся
// из expvar-реестра StdMetrics.
//
// Endpoint не содержит URL/query/raw user input и секретов: только
// bounded-cardinality счётчики и гистограммы.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		writeMetrics(w)
	})
}

// writeMetrics выводит expvar-переменные в Prometheus-подобном формате.
func writeMetrics(w http.ResponseWriter) {
	var b strings.Builder
	expvar.Do(func(kv expvar.KeyValue) {
		key := kv.Key
		if !strings.HasPrefix(key, "imager_") {
			return
		}
		writeExpvarMetric(&b, key, kv.Value)
	})
	_, _ = w.Write([]byte(b.String()))
}

// writeExpvarMetric преобразует expvar-значение в текстовые метрики.
func writeExpvarMetric(b *strings.Builder, name string, v expvar.Var) {
	switch val := v.(type) {
	case *expvar.Int:
		fmt.Fprintf(b, "# TYPE %s counter\n%s %d\n", name, name, val.Value())
	case *expvar.Map:
		// Сортируем ключи для детерминированного вывода.
		var keys []string
		val.Do(func(kv expvar.KeyValue) { keys = append(keys, kv.Key) })
		sort.Strings(keys)
		for _, k := range keys {
			sub := val.Get(k)
			if iv, ok := sub.(*expvar.Int); ok {
				fmt.Fprintf(b, "# TYPE %s_%s counter\n%s_%s %d\n", name, k, name, k, iv.Value())
			}
		}
	case *histogram:
		writeHistogramMetric(b, name, val)
	}
}

// writeHistogramMetric выводит гистограмму в Prometheus-формате.
func writeHistogramMetric(b *strings.Builder, name string, h *histogram) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(b, "# TYPE %s histogram\n", name)
	for i, c := range h.counts {
		if i < len(h.buckets) {
			fmt.Fprintf(b, "%s_bucket{le=\"%s\"} %d\n", name, formatBucket(h.buckets[i]), c)
		} else {
			fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", name, c)
		}
	}
	fmt.Fprintf(b, "%s_sum %s\n%s_count %d\n", name, formatFloat(h.sum), name, h.count)
}
