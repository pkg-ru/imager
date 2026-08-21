package observability

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

// RequestIDHeader — заголовок, из которого берётся/в который пишется
// request ID. По умолчанию X-Request-Id.
const RequestIDHeader = "X-Request-Id"

// Middleware — HTTP middleware для observability:
//   - генерирует/пробрасывает request ID (X-Request-Id) в контекст и ответ;
//   - считает request counters и длительность по bounded status class.
//
// URL/query/raw user input не логируются и не попадают в метрики.
type Middleware struct {
	metrics Metrics
	next    http.Handler
}

// NewMiddleware создаёт Middleware. Если metrics == nil, используется
// NopMetrics.
func NewMiddleware(metrics Metrics, next http.Handler) *Middleware {
	if metrics == nil {
		metrics = NopMetrics()
	}
	return &Middleware{metrics: metrics, next: next}
}

// ServeHTTP реализует http.Handler.
func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Request ID: пробрасываем из заголовка или генерируем.
	id := r.Header.Get(RequestIDHeader)
	if id == "" {
		id = newRequestID()
	}
	ctx := WithRequestID(r.Context(), id)
	w.Header().Set(RequestIDHeader, id)

	// Обёртка для захвата статуса.
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	m.next.ServeHTTP(sw, r.WithContext(ctx))

	class := ClassifyStatus(sw.status)
	m.metrics.IncRequests(class)
	m.metrics.ObserveRequestDuration(class, time.Since(start))
}

// statusWriter захватывает код статуса ответа.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// newRequestID генерирует криптографически случайный 16-байтовый hex ID.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Крайне маловероятно; fallback на time-based.
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}
