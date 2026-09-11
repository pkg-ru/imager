package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMetricsBoundedCardinality проверяет, что метрики не содержат
// произвольных пользовательских значений (URL, query, raw input, секреты).
// Все label-ы — фиксированные enum-ы (status class, storage op).
func TestMetricsBoundedCardinality(t *testing.T) {
	sm := NewStdMetrics()

	// Эмулируем запросы с разными URL/query — они не должны попасть в метрики.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	m := NewMiddleware(sm, next)

	for _, path := range []string{
		"/img-png/thumb.png",
		"/secret-token-png/thumb.png",
		"/a-png/200x200.png?token=abc123",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		m.ServeHTTP(httptest.NewRecorder(), req)
	}

	// Проверяем, что в выводе метрик нет пользовательских значений.
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, forbidden := range []string{
		"img.png",
		"secret-token",
		"token=abc123",
		"abc123",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics output leaks user input %q", forbidden)
		}
	}

	// Bounded status class присутствует (без зависимости от абсолютного
	// значения — глобальный expvar-реестр общий для всех тестов процесса).
	if !strings.Contains(body, "imager_requests_2xx ") {
		t.Errorf("metrics missing bounded 2xx counter: %q", body)
	}
}

// TestNewStdMetricsIdempotent проверяет, что повторный вызов NewStdMetrics
// не паникует из-за дублирующейся регистрации expvar-переменных (regression
// для пересоздания приложения / нескольких тестов в одном процессе).
func TestNewStdMetricsIdempotent(t *testing.T) {
	// Первый вызов.
	m1 := NewStdMetrics()
	m1.IncRequests(Status2xx)

	// Повторный вызов не должен паниковать из-за дублирующейся регистрации
	// expvar-переменных.
	m2 := NewStdMetrics()
	m2.IncRequests(Status2xx)

	// Оба экземпляра пишут в один и тот же глобальный expvar-реестр.
	// Проверяем только наличие метрики (абсолютное значение зависит от
	// порядка тестов в процессе).
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "imager_requests_2xx ") {
		t.Errorf("metrics missing 2xx counter: %q", rec.Body.String())
	}
}

// TestClassifyStatusBounded проверяет, что ClassifyStatus всегда возвращает
// один из фиксированных классов (bounded cardinality).
func TestClassifyStatusBounded(t *testing.T) {
	cases := map[int]StatusClass{
		200: Status2xx,
		204: Status2xx,
		301: Status3xx,
		304: Status3xx,
		400: Status4xx,
		404: Status4xx,
		500: Status5xx,
		503: Status5xx,
	}
	for code, want := range cases {
		if got := ClassifyStatus(code); got != want {
			t.Errorf("ClassifyStatus(%d) = %q, want %q", code, got, want)
		}
	}
	// Любой нестандартный код → 5xx (не создаёт новой кардинальности).
	if got := ClassifyStatus(999); got != Status5xx {
		t.Errorf("ClassifyStatus(999) = %q, want 5xx", got)
	}
}
