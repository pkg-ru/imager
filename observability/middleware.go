package observability

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync/atomic"
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
	// Атомарный Inc/Dec (expvar.Int.Add) вместо неатомарной пары Value()+1/Set()
	// — исключает гонку между параллельными запросами.
	if sm, ok := m.metrics.(*StdMetrics); ok {
		sm.IncHttpInflight()
	}
	defer func() {
		if sm, ok := m.metrics.(*StdMetrics); ok {
			sm.DecHttpInflight()
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

// Unwrap возвращает внутренний writer. Позволяет нижележащему коду
// получить доступ к оригинальному ResponseWriter (например, для
// sendfile-оптимизации), не теряя совместимость с обёртками.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// requestIDCounter — атомарный счётчик для fallback-генерации request ID.
// Гарантирует уникальность ID в пределах процесса даже при сбое crypto/rand
// (в отличие от прежнего time-based xorshift, который мог давать коллизии
// при генерации нескольких ID в одну наносекунду).
var requestIDCounter atomic.Uint64

// requestIDBase — случайная база fallback-генерации, вычисляется ОДИН раз
// при старте процесса через crypto/rand (с fallback на time). Разные
// перезапуски процесса получают разные базы, поэтому ID не коллизируют
// между перезапусками (при живом crypto/rand).
var requestIDBase uint64

// initRequestIDBase инициализирует requestIDBase при первом вызове
// newRequestID (лениво, чтобы не тратить crypto/rand на старте, если
// middleware не используется).
func initRequestIDBase() {
	if requestIDBase != 0 {
		return
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		var v uint64
		for i := 0; i < 8; i++ {
			v = v<<8 | uint64(b[i])
		}
		requestIDBase = v
		return
	}
	// Fallback: время старта процесса (уникально для каждого запуска).
	requestIDBase = uint64(time.Now().UnixNano())
}

// newRequestID генерирует 16-байтовый hex ID.
//
// Основной путь — криптографически случайные 16 байт (crypto/rand).
// Fallback (сбой crypto/rand) — комбинация атомарного счётчика и случайной
// базы процесса: счётчик исключает коллизии в пределах процесса, база —
// между перезапусками. В отличие от прежнего time-based xorshift, ID не
// могут совпасть при генерации нескольких ID в одну наносекунду.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		initRequestIDBase()
		n := requestIDCounter.Add(1)
		seed := requestIDBase ^ (n * 0x9e3779b97f4a7c15) // смешиваем счётчик
		for i := 0; i < 8; i++ {
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			b[i] = byte(seed >> (8 * (i % 8)))
		}
		// Вторая половина — счётчик (уникальность в пределах процесса).
		for i := 0; i < 8; i++ {
			b[8+i] = byte(n >> (8 * i))
		}
		return hex.EncodeToString(b[:])
	}
	return hex.EncodeToString(b[:])
}
