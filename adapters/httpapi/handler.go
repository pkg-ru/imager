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
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg-ru/imager/adapters/lru"
	"github.com/pkg-ru/imager/adapters/processor/routing"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/observability"
	"github.com/pkg-ru/imager/ports/generation"
)

// PixelGenerator — генератор прозрачного 1x1 пикселя в заданном формате.
type PixelGenerator interface {
	// GeneratePixel возвращает байты прозрачного 1x1 в формате format.
	GeneratePixel(ctx context.Context, format string) ([]byte, error)
}

// Generator — узкий порт генерации ассета, реализуемый generatev2.Service.
type Generator = generation.Generator

// Handler — HTTP-обработчик versioned asset URL.
type Handler struct {
	gen    Generator
	cfg    Config
	log    Logger
	format map[string]string // output format -> content-type

	// etagCache — bounded LRU-кэш вычисленных ETag по identity
	// (canonical URL + size), чтобы не пересчитывать SHA-256 на каждый запрос.
	// Ограничен по числу ключей, чтобы не расти безгранично.
	etagCache *lru.Cache[string, string]

	// topPaths — bounded реестр проблемных путей (top-paths). Создаётся в
	// New, если asset-errors.top-paths.enabled. nil = учёт выключен.
	topPaths *observability.TopPaths

	// copyPool — sync.Pool буферов копирования (64 KiB), чтобы не аллоцировать
	// новый буфер на каждый запрос (оптимизация горячего пути).
	copyPool sync.Pool
}

// New создаёт Handler. Конфигурация валидируется и нормализуется.
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
		log = observability.NopLogger()
	}
	h := &Handler{
		gen:       gen,
		cfg:       cfg,
		log:       log,
		format:    buildFormatMap(),
		etagCache: newEtagCache(4096),
		copyPool: sync.Pool{
			New: func() any {
				return make([]byte, 64*1024)
			},
		},
	}
	// Top-paths реестр создаётся, если включён.
	if cfg.AssetErrors.Enabled && cfg.AssetErrors.TopPaths.Enabled {
		h.topPaths = observability.NewTopPaths(cfg.AssetErrors.TopPaths.MaxEntries)
	}
	return h, nil
}

// buildFormatMap строит безопасный маппинг output format -> content-type.
// Не доверяем пользовательскому Content-Type: только известные форматы.
func buildFormatMap() map[string]string {
	m := map[string]string{}
	for _, f := range []string{"jpeg", "jpg", "png", "webp", "gif", "avif", "heif", "heic", "apng", "jxl"} {
		ct := mime.TypeByExtension("." + f)
		if ct == "" {
			switch f {
			case "avif", "heif", "heic", "jxl":
				ct = "image/" + f
			default:
				ct = "application/octet-stream"
			}
		}
		// MIME-типы регистронезависимы; mime.TypeByExtension может вернуть
		// "image/JXL" и т.п. Нормализуем к нижнему регистру для консистентности.
		m[f] = strings.ToLower(ct)
	}
	return m
}

// newEtagCache создаёт bounded LRU-кэш ETag по identity с лимитом max записей
// (generic-реализация из пакета adapters/lru).
func newEtagCache(max int) *lru.Cache[string, string] {
	return lru.New[string, string](max)
}

// ServeHTTP обрабатывает запрос.
//
// Тело обёрнуто в recover — паника в генераторе или при копировании ответа
// не должна ронять процесс. Если ответ ещё не начат, пишем 500.
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
	if origin := r.Header.Get("Origin"); origin != "" {
		// Vary: Origin — чтобы прокси-кэши не отдавали preflight-ответ
		// одного origin другому (как и для основных ответов).
		w.Header().Add("Vary", "Origin")
		if h.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if h.cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			// Отражаем запрошенные headers (ограниченно).
			if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
				w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
			}
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
		// Служебные/мусорные пути (favicon.ico, .well-known/*, robots.txt и
		// т.п.) не являются валидными asset URL и не должны засорять лог
		// WARN-сообщениями: обрабатываем их тихо (DEBUG) и не считаем в
		// метриках/top-paths.
		noise := isNoisePath(path)
		if !noise {
			h.recordAssetError(observability.AssetErrParse, path, "", err.Error())
		}
		// Serve original: отдельная фича — отдача исходника по «простому»
		// URL /path/name.ext со статусом 200 (если включена).
		if h.serveOriginal(w, r, path) {
			return
		}
		// Source fallback: если исходник существует, отдаём его вместо
		// ошибки (неканонический URL).
		if h.serveSourceFallback(w, r, path) {
			return
		}
		if noise {
			h.log.Debugf("httpapi: invalid asset url: %v", err)
		} else {
			h.log.Warnf("httpapi: invalid asset url: %v", err)
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid", "invalid asset url")
		return
	}

	// Явный deadline для генерации, связанный с GenerateTimeout.
	// Превышение маппится в 504 (OutcomeCanceled) через mapError.
	genCtx := r.Context()
	if h.cfg.GenerateTimeout > 0 {
		var cancel context.CancelFunc
		genCtx, cancel = context.WithTimeout(r.Context(), h.cfg.GenerateTimeout)
		defer cancel()
	}

	result, err := h.gen.Generate(genCtx, req)
	if err != nil {
		h.mapError(w, r, err, path)
		return
	}
	defer result.Close()

	h.serveResult(w, r, result)
}

// serveResult отдаёт успешный artifact с корректными headers.
func (h *Handler) serveResult(w http.ResponseWriter, r *http.Request, result *generation.Result) {
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
	// Буферизованное копирование (64 KiB) вместо дефолтных 8 KiB.
	// Буфер берём из sync.Pool, чтобы не аллоцировать на каждый запрос.
	buf := h.copyPool.Get().([]byte)
	defer h.copyPool.Put(buf)
	_, _ = io.CopyBuffer(w, result.Opened, buf)
}

// etagFor вычисляет стабильный ETag из metadata/content identity.
// Результат кэшируется по identity (canonical URL + size), чтобы не
// пересчитывать SHA-256 на каждый запрос.
func (h *Handler) etagFor(meta object.ObjectMetadata, result *generation.Result) string {
	// Если metadata предоставляет ETag, используем его. Нормализуем:
	// убираем кавычки, если хранилище уже вернуло quoted-ETag, чтобы не
	// получить двойные кавычки в заголовке.
	if meta.ETag != "" {
		return `"` + strings.Trim(meta.ETag, `"`) + `"`
	}
	// Иначе — стабильная identity из canonical URL + size, кэшируем.
	identity := result.URL + ":" + strconv.FormatInt(meta.Size, 10)
	if v, ok := h.etagCache.Get(identity); ok {
		return v
	}
	sum := sha256.Sum256([]byte(identity))
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h.etagCache.Set(identity, etag)
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
// rawPath — исходный путь запроса (для source fallback и observability).
func (h *Handler) mapError(w http.ResponseWriter, r *http.Request, err error, rawPath string) {
	// Отмена контекста (таймаут генерации) → 504.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		h.log.Warnf("httpapi: canceled: %v", err)
		h.writeError(w, r, http.StatusGatewayTimeout, "canceled", "request canceled")
		return
	}
	// Недоступный движок (формат вне покрытия libvips): routed
	// EngineUnavailable/UnsupportedError → 501 Not Implemented с
	// понятным сообщением. Обрабатывается до маппинга OutcomeError, т.к.
	// generatev2 оборачивает ошибку процессора в OutcomeProcessing.
	if routing.IsEngineUnavailable(err) {
		h.log.Warnf("httpapi: engine unavailable: %v", err)
		h.writeError(w, r, http.StatusNotImplemented, "unsupported_format",
			"requested format is not supported: "+err.Error())
		return
	}
	var oe *generation.OutcomeError
	if !errors.As(err, &oe) {
		h.log.Errorf("httpapi: unexpected error: %v", err)
		h.writeError(w, r, http.StatusInternalServerError, "processing", "internal server error")
		return
	}

	switch oe.Kind {
	case generation.OutcomeInvalid:
		// Определяем категорию: preset_not_found (неразрешимый пресет) или
		// invalid_plan (недопустимый план/канонизация).
		kind := observability.AssetErrInvalidPlan
		if isPresetNotFound(oe) {
			kind = observability.AssetErrPresetNotFound
		}
		h.recordAssetError(kind, rawPath, presetNameOf(oe), oe.Reason)
		// Source fallback: если исходник существует, отдаём его вместо
		// ошибки (несуществующий пресет / недопустимый план).
		if h.serveSourceFallback(w, r, rawPath) {
			return
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid", "invalid request")
	case generation.OutcomeForbidden:
		h.recordAssetError(observability.AssetErrPolicyDenied, rawPath, "", oe.Reason)
		// Source fallback: если исходник существует, отдаём его вместо
		// ошибки (запрещённая политика).
		if h.serveSourceFallback(w, r, rawPath) {
			return
		}
		h.log.Warnf("httpapi: forbidden: %v", oe)
		h.writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case generation.OutcomeNotFound:
		h.notFound(w, r)
	case generation.OutcomeQuota:
		h.log.Errorf("httpapi: quota: %v", oe)
		h.writeError(w, r, http.StatusInsufficientStorage, "quota", "storage quota exceeded")
	case generation.OutcomeUnavailable:
		h.log.Errorf("httpapi: unavailable: %v", oe)
		h.writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
	case generation.OutcomeOverloaded:
		// Перегрузка процессоров: клиенту следует повторить позже.
		h.log.Warnf("httpapi: overloaded: %v", oe)
		w.Header().Set("Retry-After", "1")
		h.writeError(w, r, http.StatusServiceUnavailable, "overloaded", "service overloaded, retry later")
	case generation.OutcomeProcessing:
		h.log.Errorf("httpapi: processing: %v", oe)
		h.writeError(w, r, http.StatusInternalServerError, "processing", "processing error")
	case generation.OutcomeCanceled:
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
	// os.Open вместо http.ServeFile (который сам пишет 200): статус 404
	// пишется явно ниже, до записи body.
	f, err := os.Open(file)
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
	// Буферизованное копирование (64 KiB).
	_, _ = io.CopyBuffer(w, f, make([]byte, 64*1024))
}

// serveOriginal — отдельная фича: отдача исходника по «простому» URL вида
// /path/name.ext (не относящаяся к source-fallback). Работает только при
// ServeOriginal.Enabled == true; исходник отдаётся со статусом http.StatusOK
// и Cache-Control из ServeOriginal.CacheControl.
//
// Возвращает true, если ответ записан; false — если фича выключена, URL не
// является «простым» путём к исходнику или исходник не найден (обычная
// обработка ошибки продолжается).
func (h *Handler) serveOriginal(w http.ResponseWriter, r *http.Request, rawURL string) bool {
	if !h.cfg.ServeOriginal.Enabled || h.cfg.Sources == nil {
		return false
	}
	key, fileName, ok := h.simpleSourceKey(rawURL)
	if !ok {
		return false
	}
	return h.serveSourceObject(w, r, key, fileName, "", http.StatusOK, h.cfg.ServeOriginal.CacheControl)
}

// serveSourceFallback пытается отдать исходный файл вместо ошибки ассета.
//
// Возвращает true, если fallback выполнен (ответ записан). Логика:
//   - ExtractSourceBestEffort не смог безопасно извлечь исходник → false;
//   - исходник не найден в хранилище → false;
//   - иначе отдаём исходный файл с его оригинальными заголовками.
//
// Канонический source-fallback (URL вида name-format.ext): требует
// SourceFallback.Enabled == true (существующее поведение). «Простые» URL
// /path/name.ext обрабатываются отдельной фичей serveOriginal.
func (h *Handler) serveSourceFallback(w http.ResponseWriter, r *http.Request, rawURL string) bool {
	sf := h.cfg.SourceFallback
	if h.cfg.Sources == nil {
		return false
	}
	ref := asset.ExtractSourceBestEffort(rawURL)
	if ref == nil {
		// SourceRef не извлекается: «простые» URL обрабатываются отдельной
		// фичей serveOriginal (см. handleAsset), здесь — не fallback.
		return false
	}
	// Канонический source-fallback: требует явного включения.
	if !sf.Enabled {
		return false
	}
	return h.serveSourceObject(w, r, object.ObjectKey(ref.Key()), ref.SourceFileName(), ref.SourceFormat, sf.Status, sf.CacheControl)
}

// simpleSourceKey строит безопасный ключ хранилища из «простого» URL вида
// "/path/name.ext": rejectUnsafe + CanonicalPath (те же проверки, что в
// Parse/ExtractSourceBestEffort). Возвращает ключ без ведущего "/"
// ("path/name.ext"), имя файла и ok.
func (h *Handler) simpleSourceKey(rawURL string) (object.ObjectKey, string, bool) {
	if rawURL == "" || len(rawURL) > asset.MaxURLLen {
		return "", "", false
	}
	if err := asset.RejectUnsafe(rawURL); err != nil {
		return "", "", false
	}
	rest := strings.TrimPrefix(rawURL, "/")
	// Последняя точка отделяет расширение; без неё ключ не строим.
	lastDot := strings.LastIndex(rest, ".")
	if lastDot < 0 || lastDot == len(rest)-1 {
		return "", "", false
	}
	// Имя файла — последний сегмент; без него ключ не строим.
	lastSlash := strings.LastIndex(rest, "/")
	fileName := rest[lastSlash+1:]
	if fileName == "" {
		return "", "", false
	}
	canon, err := asset.NewCanonicalizer().CanonicalPath(rest)
	if err != nil {
		return "", "", false
	}
	if canon == "" {
		return "", "", false
	}
	return object.ObjectKey(canon), fileName, true
}

// serveSourceObject открывает исходник из хранилища по ключу и отдаёт его
// с оригинальными заголовками (Content-Type, Content-Disposition, ETag) и
// заданными статусом и Cache-Control. sourceFormat используется для
// определения Content-Type по расширению, если его нет в метаданных.
// Возвращает true, если ответ записан; false — если исходник не найден или
// хранилище недоступно (обычная обработка ошибки продолжается).
func (h *Handler) serveSourceObject(w http.ResponseWriter, r *http.Request, key object.ObjectKey, fileName, sourceFormat string, status int, cacheControl string) bool {
	art, err := h.cfg.Sources.Open(r.Context(), key)
	if err != nil {
		if object.IsNotFound(err) {
			return false
		}
		// Другие ошибки хранилища (unavailable/forbidden) — не fallback,
		// оставляем обычную обработку ошибки.
		return false
	}
	defer art.Close()

	meta := art.Metadata()
	// Content-Type: из метаданных, иначе по расширению, иначе octet-stream.
	// Если sourceFormat пуст («простая» ветка serve-original), формат
	// выводится из расширения fileName.
	ct := meta.ContentType
	if ct == "" {
		ext := sourceFormat
		if ext == "" {
			ext = strings.TrimPrefix(strings.ToLower(extOf(fileName)), ".")
		}
		ct = mime.TypeByExtension("." + ext)
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if meta.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	// Content-Disposition: inline; filename="name.ext".
	w.Header().Set("Content-Disposition", `inline; filename="`+fileName+`"`)
	w.Header().Set("Cache-Control", cacheControl)
	if meta.ETag != "" {
		w.Header().Set("ETag", `"`+strings.Trim(meta.ETag, `"`)+`"`)
	}

	if status == 0 {
		status = DefaultSourceFallbackStatus
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return true
	}
	buf := h.copyPool.Get().([]byte)
	defer h.copyPool.Put(buf)
	_, _ = io.CopyBuffer(w, art, buf)
	return true
}

// recordAssetError фиксирует ошибку asset URL: счётчик метрик, top-paths
// (если включено) и структурный лог.
func (h *Handler) recordAssetError(kind observability.AssetErrorKind, rawURL, preset, reason string) {
	ae := h.cfg.AssetErrors
	if !ae.Enabled {
		return
	}
	if h.cfg.Metrics != nil {
		h.cfg.Metrics.IncAssetError(kind)
	}
	// Top-paths: ключ — путь исходника (source) или hash.
	if ae.TopPaths.Enabled && h.topPaths != nil {
		key := h.topPathKey(rawURL)
		h.topPaths.Inc(key)
	}
	// Структурный лог.
	observability.LogAssetError(h.log, ae.LogLevel, observability.AssetErrorEvent{
		Kind:   string(kind),
		URL:    rawURL,
		Preset: preset,
		Reason: reason,
	})
}

// topPathKey вычисляет ключ для top-paths по конфигурируемому режиму.
func (h *Handler) topPathKey(rawURL string) string {
	mode := h.cfg.AssetErrors.TopPaths.KeyMode
	if mode == "hash" {
		sum := sha256.Sum256([]byte(rawURL))
		return hex.EncodeToString(sum[:16])
	}
	// source: путь исходника, если извлекается, иначе raw URL.
	if ref := asset.ExtractSourceBestEffort(rawURL); ref != nil {
		return ref.Key()
	}
	return rawURL
}

// isPresetNotFound сообщает, является ли OutcomeInvalid ошибкой
// неразрешимого пресета (preset not found).
func isPresetNotFound(oe *generation.OutcomeError) bool {
	if oe == nil {
		return false
	}
	var re *asset.ResolveError
	if errors.As(oe.Cause, &re) {
		return re.PresetName != "" && re.Reason == "preset not found"
	}
	return false
}

// presetNameOf извлекает имя пресета из ошибки разрешения, если есть.
func presetNameOf(oe *generation.OutcomeError) string {
	if oe == nil {
		return ""
	}
	var re *asset.ResolveError
	if errors.As(oe.Cause, &re) {
		return re.PresetName
	}
	return ""
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

// isNoisePath сообщает, является ли путь служебным/мусорным запросом
// (favicon.ico, .well-known/*, robots.txt и т.п.), который не является
// валидным asset URL. Такие запросы приходят от браузеров/сканеров и не
// должны засорять лог WARN-сообщениями и метрики ошибок asset URL.
func isNoisePath(path string) bool {
	if path == "" {
		return false
	}
	// Нормализуем: срезаем ведущий "/" и приводим к нижнему регистру
	// (пути регистронезависимы для служебных файлов).
	p := strings.ToLower(strings.TrimPrefix(path, "/"))
	if p == "" {
		return false
	}
	// Точные имена служебных файлов в корне.
	switch p {
	case "favicon.ico", "favicon.png", "robots.txt", "sitemap.xml",
		"humans.txt", "security.txt", "apple-touch-icon.png",
		"apple-touch-icon-precomposed.png", "browserconfig.xml",
		"site.webmanifest", "manifest.json", "crossdomain.xml",
		"ads.txt", "app-ads.txt", "404.html", "index.html":
		return true
	}
	// Служебные каталоги: .well-known/*, .git/*, .svn/*, .hg/*.
	for _, prefix := range []string{".well-known/", ".git/", ".svn/", ".hg/"} {
		if strings.HasPrefix(p, prefix) {
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
