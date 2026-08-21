package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg-ru/imager/internal/application/generatev2"
	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// PixelGenerator — генератор прозрачного 1x1 пикселя в заданном формате.
type PixelGenerator interface {
	// GeneratePixel возвращает байты прозрачного 1x1 в формате format.
	GeneratePixel(ctx context.Context, format string) ([]byte, error)
}

// Generator — узкий порт генерации ассета, реализуемый generatev2.Service.
type Generator interface {
	Generate(ctx context.Context, req *asset.Request) (*generatev2.Result, error)
}

// Handler — HTTP-обработчик versioned asset URL.
type Handler struct {
	gen    Generator
	cfg    Config
	log    Logger
	format map[string]string // output format -> content-type

	// etagCache — кэш вычисленных ETag по identity (canonical URL + size),
	// чтобы не пересчитывать SHA-256 на каждый запрос (п.15).
	etagCache sync.Map // string -> string
}

// New создаёт Handler. Конфигурация валидируется и нормализуется.
func New(gen Generator, cfg Config) (*Handler, error) {
	if gen == nil {
		return nil, fmt.Errorf("httpapi: nil generator")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.normalize()
	log := cfg.Logger
	if log == nil {
		log = nopLogger{}
	}
	return &Handler{
		gen:    gen,
		cfg:    cfg,
		log:    log,
		format: buildFormatMap(),
	}, nil
}

// buildFormatMap строит безопасный маппинг output format -> content-type.
// Не доверяем пользовательскому Content-Type: только известные форматы.
func buildFormatMap() map[string]string {
	m := map[string]string{}
	for _, f := range []string{"jpeg", "jpg", "png", "webp", "gif", "avif", "heif", "heic", "apng"} {
		ct := mime.TypeByExtension("." + f)
		if ct == "" {
			switch f {
			case "avif", "heif", "heic":
				ct = "image/" + f
			default:
				ct = "application/octet-stream"
			}
		}
		m[f] = ct
	}
	return m
}

// ServeHTTP обрабатывает запрос.
//
// П.2: тело обёрнуто в try/catch — паника в генераторе или при копировании
// ответа не должна ронять процесс. Если ответ ещё не начат, пишем 500.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Security headers применяются ко всем ответам.
	h.applySecurityHeaders(w)

	// CORS (deny-by-default).
	h.applyCORS(w, r)

	defer func() {
		if rec := recover(); rec != nil {
			h.log.Errorf("httpapi: panic in handler: %v", rec)
			// Пытаемся записать 500. Если ответ уже начат (заголовки
			// отправлены), запись не сработает — это безопасно, просто
			// логируем. Ошибки записи игнорируем.
			func() {
				defer func() { _ = recover() }()
				h.writeError(w, r, http.StatusInternalServerError, "processing", "internal server error")
			}()
		}
	}()

	switch r.Method {
	case http.MethodOptions:
		h.handleOptions(w, r)
		return
	case http.MethodGet, http.MethodHead:
		h.handleAsset(w, r)
		return
	default:
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		h.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
}

// handleOptions обрабатывает OPTIONS: явный Allow и CORS policy.
func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	if origin := r.Header.Get("Origin"); origin != "" && h.originAllowed(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if h.cfg.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		// Отражаем запрошенные headers (ограниченно).
		if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
			w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAsset обрабатывает GET/HEAD для asset URL.
func (h *Handler) handleAsset(w http.ResponseWriter, r *http.Request) {
	path := pathPart(r.URL.EscapedPath())
	if path == "" || path == "/" {
		h.notFound(w, r)
		return
	}

	// Ограничение длины URL до парсинга.
	if len(path) > h.cfg.MaxURLLen {
		h.writeError(w, r, http.StatusRequestURITooLong, "invalid", "url too long")
		return
	}

	req, err := asset.Parse(path)
	if err != nil {
		h.log.Warnf("httpapi: invalid asset url: %v", err)
		h.writeError(w, r, http.StatusBadRequest, "invalid", "invalid asset url")
		return
	}

	// П.18: явный deadline для генерации, связанный с GenerateTimeout.
	// Превышение маппится в 504 (OutcomeCanceled) через mapError.
	genCtx := r.Context()
	if h.cfg.GenerateTimeout > 0 {
		var cancel context.CancelFunc
		genCtx, cancel = context.WithTimeout(r.Context(), h.cfg.GenerateTimeout)
		defer cancel()
	}

	result, err := h.gen.Generate(genCtx, req)
	if err != nil {
		h.mapError(w, r, err)
		return
	}
	defer result.Close()

	h.serveResult(w, r, result)
}

// serveResult отдаёт успешный artifact с корректными headers.
func (h *Handler) serveResult(w http.ResponseWriter, r *http.Request, result *generatev2.Result) {
	meta := result.Opened.Metadata()

	// Content-Type из безопасного format mapping (не доверяем пользователю).
	format := strings.ToLower(strings.TrimPrefix(extOf(result.URL), "."))
	ct := h.format[format]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)

	// Content-Length, если доступен.
	if meta.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}

	// ETag на стабильной metadata/content identity.
	etag := h.etagFor(meta, result)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}

	// Cache-Control immutable для canonical generated assets.
	w.Header().Set("Cache-Control", h.cfg.CacheControl)

	// Conditional request: If-None-Match → 304 без body.
	// Поддерживаем список ETag и wildcard "*" (RFC 7232).
	if etag != "" && ifNoneMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	// П.7: буферизованное копирование (64 KiB) вместо дефолтных 8 KiB.
	_, _ = io.CopyBuffer(w, result.Opened, make([]byte, 64*1024))
}

// etagFor вычисляет стабильный ETag из metadata/content identity.
// П.15: результат кэшируется по identity (canonical URL + size), чтобы не
// пересчитывать SHA-256 на каждый запрос.
func (h *Handler) etagFor(meta object.ObjectMetadata, result *generatev2.Result) string {
	// Если metadata предоставляет ETag, используем его.
	if meta.ETag != "" {
		return `"` + meta.ETag + `"`
	}
	// Иначе — стабильная identity из canonical URL + size, кэшируем.
	identity := result.URL + ":" + strconv.FormatInt(meta.Size, 10)
	if v, ok := h.etagCache.Load(identity); ok {
		return v.(string)
	}
	sum := sha256.Sum256([]byte(identity))
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h.etagCache.Store(identity, etag)
	return etag
}

// ifNoneMatch проверяет If-None-Match против текущего ETag.
// Поддерживает список ETag через запятую и wildcard "*".
func ifNoneMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}

// mapError маппит типизированную ошибку use case в HTTP-статус.
func (h *Handler) mapError(w http.ResponseWriter, r *http.Request, err error) {
	// П.18: отмена контекста (таймаут генерации) → 504.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		h.log.Warnf("httpapi: canceled: %v", err)
		h.writeError(w, r, http.StatusGatewayTimeout, "canceled", "request canceled")
		return
	}
	var oe *generatev2.OutcomeError
	if !errors.As(err, &oe) {
		h.log.Errorf("httpapi: unexpected error: %v", err)
		h.writeError(w, r, http.StatusInternalServerError, "processing", "internal server error")
		return
	}

	switch oe.Kind {
	case generatev2.OutcomeInvalid:
		h.writeError(w, r, http.StatusBadRequest, "invalid", "invalid request")
	case generatev2.OutcomeForbidden:
		h.log.Warnf("httpapi: forbidden: %v", oe)
		h.writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case generatev2.OutcomeNotFound:
		h.notFound(w, r)
	case generatev2.OutcomeQuota:
		h.log.Errorf("httpapi: quota: %v", oe)
		h.writeError(w, r, http.StatusInsufficientStorage, "quota", "storage quota exceeded")
	case generatev2.OutcomeUnavailable:
		h.log.Errorf("httpapi: unavailable: %v", oe)
		h.writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
	case generatev2.OutcomeProcessing:
		h.log.Errorf("httpapi: processing: %v", oe)
		h.writeError(w, r, http.StatusInternalServerError, "processing", "processing error")
	case generatev2.OutcomeCanceled:
		h.log.Warnf("httpapi: canceled: %v", oe)
		h.writeError(w, r, http.StatusGatewayTimeout, "canceled", "request canceled")
	default:
		h.log.Errorf("httpapi: unknown outcome: %v", oe)
		h.writeError(w, r, http.StatusInternalServerError, "processing", "internal server error")
	}
}

// notFound применяет not-found fallback semantics.
// Сначала явно WriteHeader(404), затем копируем fallback body.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	nf := h.cfg.NotFound
	format := outputFormat(r.URL.EscapedPath())

	// Pixel fallback.
	if nf.Pixel && format != "" && h.cfg.Pixel != nil {
		if bytes, err := h.cfg.Pixel.GeneratePixel(r.Context(), format); err == nil {
			ct := h.format[format]
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", h.cfg.NotFoundCacheControl)
			w.Header().Set("Content-Length", strconv.Itoa(len(bytes)))
			w.WriteHeader(http.StatusNotFound)
			if r.Method != http.MethodHead {
				_, _ = w.Write(bytes)
			}
			return
		}
	}

	// Image/Page file fallback.
	if nf.Image != "" {
		h.serveFallbackFile(w, r, nf.Image)
		return
	}
	if nf.Page != "" {
		h.serveFallbackFile(w, r, nf.Page)
		return
	}

	// Redirect fallback. Для HEAD не пишем body (GET/HEAD parity).
	if nf.Redirect != "" {
		w.Header().Set("Location", nf.Redirect)
		w.WriteHeader(http.StatusMovedPermanently)
		if r.Method != http.MethodHead {
			body := []byte("<a href=\"" + html.EscapeString(nf.Redirect) + "\">Moved Permanently</a>.\n")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		}
		return
	}

	// Нет fallback — корректный 404 с error envelope.
	h.writeError(w, r, http.StatusNotFound, "not_found", "not found")
}

// serveFallbackFile отдаёт статический файл с явным статусом 404.
// Не использует http.ServeFile (который сам пишет 200): сначала
// WriteHeader(404), затем копируем содержимое.
func (h *Handler) serveFallbackFile(w http.ResponseWriter, r *http.Request, file string) {
	f, err := openFallback(file)
	if err != nil {
		h.log.Errorf("httpapi: fallback file %q: %v", file, err)
		h.writeError(w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		h.log.Errorf("httpapi: fallback stat %q: %v", file, err)
		h.writeError(w, r, http.StatusNotFound, "not_found", "not found")
		return
	}

	ct := mime.TypeByExtension(strings.ToLower(extOf(file)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", h.cfg.NotFoundCacheControl)
	if info.Size() > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	// Явный статус ДО записи body.
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodHead {
		return
	}
	// П.7: буферизованное копирование (64 KiB).
	_, _ = io.CopyBuffer(w, f, make([]byte, 64*1024))
}

// writeError пишет стабильный error envelope.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", h.cfg.NotFoundCacheControl)
	body, _ := json.Marshal(errorEnvelope{Error: errorDetail{Code: code, Message: message}})
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// errorEnvelope — стабильный формат ошибки.
type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// applySecurityHeaders добавляет security headers ко всем ответам.
func (h *Handler) applySecurityHeaders(w http.ResponseWriter) {
	hdr := w.Header()
	hdr.Set("X-Content-Type-Options", "nosniff")
	if h.cfg.ReferrerPolicy != "" {
		hdr.Set("Referrer-Policy", h.cfg.ReferrerPolicy)
	}
	if h.cfg.CSP != "" {
		hdr.Set("Content-Security-Policy", h.cfg.CSP)
	}
}

// applyCORS применяет deny-by-default CORS policy.
func (h *Handler) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	// Vary: Origin ставится всегда при наличии Origin, чтобы прокси-кэши
	// не смешивали ответы для разных origins (в т.ч. denied).
	hdr := w.Header()
	hdr.Add("Vary", "Origin")
	if !h.originAllowed(origin) {
		return
	}
	hdr.Set("Access-Control-Allow-Origin", origin)
	if h.cfg.AllowCredentials {
		hdr.Set("Access-Control-Allow-Credentials", "true")
	}
}

// originAllowed проверяет origin против allowlist (deny-by-default).
func (h *Handler) originAllowed(origin string) bool {
	for _, o := range h.cfg.AllowedOrigins {
		if o == "*" {
			return true
		}
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// pathPart извлекает путь из escaped path (до query).
func pathPart(escapedPath string) string {
	if i := strings.IndexByte(escapedPath, '?'); i >= 0 {
		return escapedPath[:i]
	}
	return escapedPath
}

// extOf возвращает расширение (с точкой) из пути.
func extOf(p string) string {
	i := strings.LastIndexByte(p, '.')
	if i < 0 {
		return ""
	}
	return p[i:]
}

// outputFormat извлекает расширение из URI, если оно похоже на формат.
func outputFormat(escapedPath string) string {
	p := pathPart(escapedPath)
	ext := strings.ToLower(strings.TrimPrefix(extOf(p), "."))
	if ext == "" || len(ext) > 8 {
		return ""
	}
	for _, r := range ext {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}
