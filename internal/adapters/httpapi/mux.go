package httpapi

import (
	"net/http"

	"github.com/pkg-ru/imager/internal/observability"
)

// NewMux собирает корневой http.Handler: health endpoints, metrics и asset
// handler, обёрнутый в observability middleware (request ID + metrics).
//
// Маршруты:
//   - /healthz  — liveness
//   - /readyz   — readiness
//   - /metrics  — bounded-cardinality метрики (Prometheus exposition)
//   - /v1/...   — asset URL (делегируется Handler)
//   - всё прочее — 404 через Handler (fallback semantics)
func NewMux(h *Handler, health *Health, metrics observability.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler())
	mux.Handle("/metrics", observability.MetricsHandler())
	mux.Handle("/v1/", h)
	// Корневой путь и всё остальное — через handler (для fallback/404).
	mux.Handle("/", h)

	// Observability middleware поверх всего mux (request ID + request metrics).
	return observability.NewMiddleware(metrics, mux)
}
