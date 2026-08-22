package httpapi

import (
	"net"
	"net/http"
	"strings"

	"github.com/pkg-ru/imager/internal/observability"
)

// MetricsAuthConfig — конфигурация защиты /metrics endpoint (п.17).
// По умолчанию выключена (для совместимости). При включении /metrics
// доступен только при наличии валидного токена (X-Metrics-Token) или с
// разрешённого IP.
type MetricsAuthConfig struct {
	// Token — bearer-токен для доступа к /metrics (пусто = не требуется).
	Token string
	// AllowedIPs — список разрешённых IP (CIDR или точные адреса).
	AllowedIPs []string
}

// NewMux собирает корневой http.Handler: health endpoints, metrics и asset
// handler, обёрнутый в observability middleware (request ID + metrics).
//
// Маршруты:
//   - /healthz  — liveness
//   - /readyz   — readiness
//   - /metrics  — bounded-cardinality метрики (Prometheus exposition)
//   - asset URL — делегируется Handler (fallback semantics)
func NewMux(h *Handler, health *Health, metrics observability.Metrics) http.Handler {
	return NewMuxWithAuth(h, health, metrics, MetricsAuthConfig{})
}

// NewMuxWithAuth собирает корневой http.Handler с опциональной защитой
// /metrics (п.17). Если MetricsAuthConfig пуст — /metrics доступен без
// аутентификации (совместимость).
func NewMuxWithAuth(h *Handler, health *Health, metrics observability.Metrics, auth MetricsAuthConfig) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler())
	mux.Handle("/metrics", protectMetrics(observability.MetricsHandler(), auth))
	// Корневой путь и всё остальное — через handler (asset URL, fallback/404).
	mux.Handle("/", h)

	// Observability middleware поверх всего mux (request ID + request metrics).
	return observability.NewMiddleware(metrics, mux)
}

// protectMetrics оборачивает metrics handler защитой (токен/IP-фильтр).
func protectMetrics(next http.Handler, auth MetricsAuthConfig) http.Handler {
	if auth.Token == "" && len(auth.AllowedIPs) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.Token != "" {
			if r.Header.Get("X-Metrics-Token") != auth.Token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if len(auth.AllowedIPs) > 0 && !ipAllowed(r.RemoteAddr, auth.AllowedIPs) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ipAllowed проверяет, разрешён ли адрес remoteAddr (host:port) списком
// allowed (точные IP или CIDR).
func ipAllowed(remoteAddr string, allowed []string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(a); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if aip := net.ParseIP(a); aip != nil && aip.Equal(ip) {
			return true
		}
	}
	return false
}
