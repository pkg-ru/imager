package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg-ru/imager/observability"
	"github.com/pkg-ru/imager/web/static"
)

// MetricsAuthConfig — конфигурация защиты /metrics endpoint.
// По умолчанию выключена. При включении /metrics доступен только при наличии
// валидного токена (X-Metrics-Token) или с разрешённого IP.
type MetricsAuthConfig struct {
	// Token — bearer-токен для доступа к /metrics (пусто = не требуется).
	Token string
	// AllowedIPs — список разрешённых IP (CIDR или точные адреса).
	AllowedIPs []string
}

// NewMuxWithAdmission собирает корневой http.Handler с опциональной защитой
// /metrics и admission control. maxConcurrent — максимальное число
// одновременно обрабатываемых asset-запросов (0 = без ограничения).
// Admission применяется ТОЛЬКО к asset handler ("/"), не к health/metrics,
// чтобы liveness/readiness оставались доступными при перегрузке.
func NewMuxWithAdmission(h http.Handler, health *Health, metrics observability.Metrics, auth MetricsAuthConfig, maxConcurrent int, admin http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler())
	mux.Handle("/metrics", protectMetrics(observability.MetricsHandler(), auth))
	// Административные эндпоинты регистрируются ДО корневого "/*", только
	// если admin включён (admin != nil). Иначе запросы /admin/* уходят в
	// asset handler → 404.
	if admin != nil {
		mux.Handle("/admin/", admin)
	}
	// Статические файлы (favicon) регистрируются ДО корневого "/*", чтобы
	// /favicon.ico и /favicon-*.png отдавались иконкой (200) вместо ошибки
	// парсинга asset URL. Не конфликтует с asset-роутингом: эти пути не
	// являются валидными asset URL.
	mux.Handle("/favicon.ico", staticHandler("favicon.ico", "image/x-icon"))
	// Корневой путь и всё остальное — через handler (asset URL, fallback/404).
	// Admission control ограничивает число одновременно обрабатываемых
	// asset-запросов: при переполнении семафора — HTTP 503 + Retry-After.
	var assetHandler http.Handler = h
	if maxConcurrent > 0 {
		assetHandler = NewAdmissionControl(maxConcurrent).Wrap(h)
	}
	mux.Handle("/", assetHandler)

	// Observability middleware поверх всего mux (request ID + request metrics).
	// gzip применяется к JSON-ответам (error envelope) при поддержке
	// клиентом Accept-Encoding: gzip.
	return observability.NewMiddleware(metrics, gzipHandler(mux))
}

// staticHandler возвращает http.Handler, отдающий встроенный статический
// файл (из web/static) с заданным Content-Type. Файл читается из embed.FS
// один раз при первом запросе и кэшируется — ноль чтения с диска в
// рантайме. Поддерживает GET и HEAD (без body).
func staticHandler(name, contentType string) http.Handler {
	var (
		once sync.Once
		data []byte
		err  error
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			data, err = static.FS.ReadFile(name)
		})
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		// Favicon кэшируется браузером; короткий max-age без immutable
		// (файл может обновиться при деплое).
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(data)
	})
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
