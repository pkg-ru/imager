package observability

import (
	"expvar"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestIncAssetErrorExpvar проверяет, что счётчики ошибок asset URL
// публикуются в expvar-реестре и выводятся в /metrics.
//
// Проверки используют ПРИРАЩЕНИЕ (дельту) от снапшота значений, снятого
// ДО инкремента: NewStdMetrics() идемпотентно переиспользует глобальные
// expvar-переменные, поэтому абсолютные значения накапливаются между
// запусками теста (-count>1) и нестабильны.
func TestIncAssetErrorExpvar(t *testing.T) {
	sm := NewStdMetrics()

	kinds := []AssetErrorKind{
		AssetErrParse,
		AssetErrPresetNotFound,
		AssetErrPolicyDenied,
		AssetErrInvalidPlan,
	}

	// Снапшот начальных (накопленных в expvar-реестре) значений счётчиков.
	before := make(map[AssetErrorKind]int64, len(kinds))
	for _, k := range kinds {
		before[k] = assetErrorValue(sm, k)
	}

	sm.IncAssetError(AssetErrParse)
	sm.IncAssetError(AssetErrParse)
	sm.IncAssetError(AssetErrPresetNotFound)
	sm.IncAssetError(AssetErrPolicyDenied)
	sm.IncAssetError(AssetErrInvalidPlan)

	// expvar-реестр: счётчик должен существовать после инкремента.
	if sm.assetErrors.Get(string(AssetErrParse)) == nil {
		t.Error("imager_asset_errors missing parse counter")
	}

	// Проверяем приращение счётчиков, а не абсолютные значения.
	wantDelta := map[AssetErrorKind]int64{
		AssetErrParse:          2,
		AssetErrPresetNotFound: 1,
		AssetErrPolicyDenied:   1,
		AssetErrInvalidPlan:    1,
	}
	for k, want := range wantDelta {
		if got := assetErrorValue(sm, k) - before[k]; got != want {
			t.Errorf("imager_asset_errors %s increment = %d, want %d", k, got, want)
		}
	}

	// /metrics вывод: значения в body тоже проверяем как приращение
	// от снапшота, чтобы тест был стабилен при -count>1.
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for k, want := range wantDelta {
		name := "imager_asset_errors_" + string(k)
		if !strings.Contains(body, name) {
			t.Errorf("metrics output missing %q", name)
			continue
		}
		if got := metricValue(t, body, name) - before[k]; got != want {
			t.Errorf("metrics %s increment = %d, want %d", name, got, want)
		}
	}
}

// assetErrorValue возвращает текущее значение счётчика ошибок вида kind
// (0, если ключ ещё не зарегистрирован в глобальной expvar-мапе).
func assetErrorValue(m *StdMetrics, kind AssetErrorKind) int64 {
	v := m.assetErrors.Get(string(kind))
	if v == nil {
		return 0
	}
	iv, ok := v.(*expvar.Int)
	if !ok {
		return 0
	}
	return iv.Value()
}

// metricValue извлекает числовое значение метрики name из /metrics body.
func metricValue(t *testing.T, body, name string) int64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("metric line %q: want %q <value>", line, name)
		}
		v, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			t.Fatalf("metric line %q: bad value: %v", line, err)
		}
		return v
	}
	t.Fatalf("metric %q not found in /metrics output", name)
	return 0
}

// TestNopMetricsAssetError проверяет, что NopMetrics не паникует.
func TestNopMetricsAssetError(t *testing.T) {
	m := NopMetrics()
	m.IncAssetError(AssetErrParse)
}
