package httpapi

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"

	"gitverse.ru/pkg-ru/imager/observability"
)

// gzipWriter — обёртка ResponseWriter, сжимающая ответ gzip, если клиент
// поддерживает Accept-Encoding: gzip и Content-Type — application/json.
// Сжатие применяется только к JSON-ответам (error envelope), чтобы не тратить
// CPU на уже сжатые изображения.
type gzipWriter struct {
	http.ResponseWriter
	// acceptEncoding — значение Accept-Encoding из запроса.
	acceptEncoding string
	// enabled — сжатие активно (Accept-Encoding: gzip и JSON content-type).
	enabled bool
	// started — заголовки уже отправлены (WriteHeader вызван).
	started bool
	// gz — gzip-писатель, если enabled.
	gz *gzip.Writer
	// log — логгер для ошибок gzip-потока (nil = NopLogger).
	log observability.Logger
}

// WriteHeader переопределяет отправку заголовков: решает, включать ли
// сжатие, и корректирует Content-Length (после сжатия длина неизвестна).
func (w *gzipWriter) WriteHeader(code int) {
	if !w.started {
		w.started = true
		if w.shouldCompress() {
			w.enabled = true
			// После сжатия длина неизвестна заранее; убираем Content-Length.
			w.Header().Del("Content-Length")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")
			w.gz = gzip.NewWriter(w.ResponseWriter)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// shouldCompress решает, сжимать ли ответ: клиент поддерживает gzip и
// content-type — JSON.
func (w *gzipWriter) shouldCompress() bool {
	if !acceptsGzip(w.acceptEncoding) {
		return false
	}
	ct := w.Header().Get("Content-Type")
	return strings.HasPrefix(ct, "application/json")
}

// Write реализует io.Writer: сжимает данные, если сжатие включено.
func (w *gzipWriter) Write(p []byte) (int, error) {
	if w.enabled {
		if _, err := w.gz.Write(p); err != nil {
			w.logGzipError("write", err)
			return 0, err
		}
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

// Flush сбрасывает gzip-буфер и вызывает Flush нижележащего writer.
func (w *gzipWriter) Flush() {
	if w.enabled {
		if err := w.gz.Flush(); err != nil {
			w.logGzipError("flush", err)
		}
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close завершает gzip-поток. Вызывается после завершения ответа.
func (w *gzipWriter) Close() error {
	if w.enabled {
		if err := w.gz.Close(); err != nil {
			w.logGzipError("close", err)
		}
	}
	return nil
}

// logGzipError логирует ошибку gzip-потока. Поведение не меняется: после
// WriteHeader исправить ответ нельзя, поэтому только логируем (как в
// recover.go). Логгер по умолчанию — NopLogger.
func (w *gzipWriter) logGzipError(op string, err error) {
	if w.log == nil {
		w.log = observability.NopLogger()
	}
	w.log.Errorf("httpapi: gzip %s error: %v", op, err)
}

// acceptsGzip проверяет Accept-Encoding на поддержку gzip.
func acceptsGzip(header string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Отбрасываем q=0 (явный запрет).
		name := part
		if i := strings.IndexByte(part, ';'); i >= 0 {
			name = strings.TrimSpace(part[:i])
			q := strings.TrimSpace(part[i+1:])
			if strings.HasPrefix(q, "q=") {
				if v, err := strconv.ParseFloat(strings.TrimPrefix(q, "q="), 64); err == nil && v == 0 {
					continue
				}
			}
		}
		if name == "gzip" || name == "*" {
			return true
		}
	}
	return false
}

// gzipHandler оборачивает next, подменяя ResponseWriter на gzipWriter.
// log — логгер для ошибок gzip-потока (nil = NopLogger).
func gzipHandler(log observability.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw := &gzipWriter{
			ResponseWriter: w,
			acceptEncoding: r.Header.Get("Accept-Encoding"),
			log:            log,
		}
		// defer гарантирует завершение gzip-потока даже при панике в
		// нижележащем handler (иначе клиент получит обрезанный поток).
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}
