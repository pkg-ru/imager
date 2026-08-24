package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIncAssetErrorExpvar проверяет, что счётчики ошибок asset URL
// публикуются в expvar-реестре и выводятся в /metrics.
func TestIncAssetErrorExpvar(t *testing.T) {
	sm := NewStdMetrics()
	sm.IncAssetError(AssetErrParse)
	sm.IncAssetError(AssetErrParse)
	sm.IncAssetError(AssetErrPresetNotFound)
	sm.IncAssetError(AssetErrPolicyDenied)
	sm.IncAssetError(AssetErrInvalidPlan)

	// expvar-реестр.
	if v := sm.assetErrors.Get(string(AssetErrParse)); v == nil {
		t.Error("imager_asset_errors missing parse counter")
	}

	// /metrics вывод.
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"imager_asset_errors_parse 2",
		"imager_asset_errors_preset_not_found 1",
		"imager_asset_errors_policy_denied 1",
		"imager_asset_errors_invalid_plan 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

// TestNopMetricsAssetError проверяет, что NopMetrics не паникует.
func TestNopMetricsAssetError(t *testing.T) {
	m := NopMetrics()
	m.IncAssetError(AssetErrParse)
}
