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

// NewMuxWithAdmission собирает корневой http.Handler с опциональной защитой
// /metrics (п.17) и admission control (В11). maxConcurrent — максимальное
// число одновременно обрабатываемых asset-запросов (0 = без ограничения).
// Admission применяется ТОЛЬКО к asset handler ("/"), не к health/metrics,
// чтобы liveness/readiness оставались доступными при перегрузке.
func NewMuxWithAdmission(h *Handler, health *Health, metrics observability.Metrics, auth MetricsAuthConfig, maxConcurrent int) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler())
	mux.Handle("/metrics", protectMetrics(observability.MetricsHandler(), auth))
	// Корневой путь и всё остальное — через handler (asset URL, fallback/404).
	// Admission control ограничивает число одновременно обрабатываемых
	// asset-запросов (В11): при переполнении семафора — HTTP 503 + Retry-After.
	var assetHandler http.Handler = h
	if maxConcurrent > 0 {
		assetHandler = NewAdmissionControl(maxConcurrent).Wrap(h)
	}
	mux.Handle("/", assetHandler)

	// Observability middleware поверх всего mux (request ID + request metrics).
	// gzip (У2) применяется к JSON-ответам (error envelope) при поддержке
	// клиентом Accept-Encoding: gzip.
	return observability.NewMiddleware(metrics, gzipHandler(mux))
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
