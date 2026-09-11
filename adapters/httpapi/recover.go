package httpapi

import (
	"encoding/json"
	"net/http"
	dbg "runtime/debug"
	"strconv"

	"gitverse.ru/pkg-ru/imager/observability"
)

// Recover — middleware panic-recovery на уровне корневого mux.
//
// Гарантия сервиса: ЛЮБОЙ запрос (включая /admin/*, /metrics, /healthz,
// /readyz, static) всегда получает HTTP-ответ (500), а не обрыв соединения
// при панике нижележащего handler.
//
// Asset-хендлер (Handler.ServeHTTP) уже имеет собственный recover — этот
// middleware является страховкой для остальных веток (admin, health, metrics,
// static, gzip), у которых recover нет.
//
// Поведение:
//   - если ответ ещё не начат (WriteHeader/Write/Flush не вызывались) —
//     пишем 500 с JSON-envelope в том же формате, что и writeError в
//     handler.go (с соблюдением HEAD-parity: без тела для HEAD);
//   - если ответ уже начат — исправить нельзя (заголовки отправлены),
//     только логируем панику и завершаем обработку;
//   - паника логируется через observability.Logger (Errorf) со stack trace
//     (dbg.Stack), как в govips/vips/error.go;
//   - каждая паника инкрементирует глобальный счётчик imager_panics_total
//     (observability.IncPanicsGlobal) — видимость паник в /metrics.
type Recover struct {
	log  observability.Logger
	next http.Handler
}

// NewRecover создаёт Recover middleware. Если log == nil, используется
// NopLogger.
func NewRecover(log observability.Logger, next http.Handler) *Recover {
	if log == nil {
		log = observability.NopLogger()
	}
	return &Recover{log: log, next: next}
}

// ServeHTTP реализует http.Handler.
func (m *Recover) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// recoverWriter фиксирует, начат ли ответ (WriteHeader/Write/Flush).
	// Тот же механизм, что statusWriter в observability/middleware.go.
	rw := &recoverWriter{ResponseWriter: w}

	defer func() {
		if rec := recover(); rec != nil {
			m.log.Errorf("httpapi: panic in mux handler: %v\nStack:\n%s", rec, dbg.Stack())
			// Каждая паника видна в /metrics (imager_panics_total).
			observability.IncPanicsGlobal()
			// Если ответ уже начат (заголовки отправлены), дописывать тело
			// нельзя — только логируем. Иначе пишем 500 JSON.
			if !rw.started {
				func() {
					defer func() { _ = recover() }()
					writeRecoverError(rw, r)
				}()
			}
		}
	}()

	m.next.ServeHTTP(rw, r)
}

// recoverWriter оборачивает ResponseWriter и фиксирует, начат ли ответ.
type recoverWriter struct {
	http.ResponseWriter
	// started — true, если WriteHeader/Write/Flush уже вызывались
	// (заголовки отправлены или тело начато).
	started bool
}

func (w *recoverWriter) WriteHeader(code int) {
	w.started = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *recoverWriter) Write(p []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(p)
}

func (w *recoverWriter) Flush() {
	w.started = true
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap возвращает внутренний writer. Позволяет нижележащему коду
// получить доступ к оригинальному ResponseWriter (например, для
// sendfile-оптимизации), не теряя совместимость с обёртками.
func (w *recoverWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// writeRecoverError пишет 500 с JSON-envelope в формате writeError
// (handler.go): {"error":{"code":"internal","message":"internal server error"}}.
// Для HEAD пишем только заголовки (Content-Length), без тела.
func writeRecoverError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	body, _ := json.Marshal(recoverEnvelope{Error: recoverDetail{Code: "internal", Message: "internal server error"}})
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusInternalServerError)
	if r != nil && r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// recoverEnvelope — стабильный формат ошибки (совпадает с errorEnvelope).
type recoverEnvelope struct {
	Error recoverDetail `json:"error"`
}

type recoverDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
