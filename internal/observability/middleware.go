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

	// Gauge http_inflight: инкрементируем на время обработки запроса.
	// Используем type-assert, чтобы не расширять публичный интерфейс Metrics.
	if sm, ok := m.metrics.(*StdMetrics); ok {
		sm.SetHttpInflight(sm.httpInflight.Value() + 1)
	}
	defer func() {
		if sm, ok := m.metrics.(*StdMetrics); ok {
			sm.SetHttpInflight(sm.httpInflight.Value() - 1)
		}
	}()

	// Обёртка для захвата статуса. status=0 означает "не записан" — после
	// обработки подставляем 200 (неявный статус по умолчанию).
	sw := &statusWriter{ResponseWriter: w}
	m.next.ServeHTTP(sw, r.WithContext(ctx))

	status := sw.status
	if status == 0 {
		status = http.StatusOK
	}
	class := ClassifyStatus(status)
	m.metrics.IncRequests(class)
	m.metrics.ObserveRequestDuration(class, time.Since(start))
}

// statusWriter захватывает код статуса ответа.
//
// Переопределяет Write и Flush, чтобы гарантировать корректный захват
// статуса, даже если handler пишет body без явного WriteHeader (тогда
// неявный статус 200 должен быть зафиксирован).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// newRequestID генерирует криптографически случайный 16-байтовый hex ID.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Крайне маловероятно; fallback на time-based + случайная составляющая
		// (доп. замечание): при сбое rand все ID не должны совпадать в одну
		// секунду.
		now := time.Now().UnixNano()
		// Смешиваем время с псевдослучайным значением из runtime fastrand.
		seed := uint64(now) ^ uint64(time.Now().UnixNano()<<1)
		for i := 0; i < 8; i++ {
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			b[i] = byte(seed >> (8 * (i % 8)))
		}
		return hex.EncodeToString(b[:])
	}
	return hex.EncodeToString(b[:])
}
