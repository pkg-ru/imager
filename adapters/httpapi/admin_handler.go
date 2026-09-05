package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/pkg-ru/dynamic"
	"gitverse.ru/pkg-ru/imager/observability"
	"gitverse.ru/pkg-ru/imager/ports/admin"
)

// AdminHandler — HTTP-обработчик административных эндпоинтов.
//
// Маршруты (регистрируются в mux под "/admin/" только при admin.enabled):
//
//	POST   /admin/assets/generate   — фоновая генерация ассетов
//	DELETE /admin/assets/delete     — удаление ассетов
//
// Авторизация: Authorization: Bearer <token> через crypto/subtle.
// Неверный/отсутствующий токен → 403 JSON (в стиле writeError).
type AdminHandler struct {
	svc admin.Service
	cfg AdminConfig
	log Logger
}

// NewAdminHandler создаёт AdminHandler.
func NewAdminHandler(svc admin.Service, cfg AdminConfig, log Logger) *AdminHandler {
	if log == nil {
		log = observability.NopLogger()
	}
	return &AdminHandler{svc: svc, cfg: cfg, log: log}
}

// ServeHTTP выполняет роутинг и bearer-авторизацию.
func (a *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Bearer-авторизация для всех admin-запросов.
	if !a.authorized(r) {
		a.writeError(w, r, http.StatusForbidden, "forbidden", "invalid or missing bearer token")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/admin")
	switch {
	case r.Method == http.MethodPost && path == "/assets/generate":
		a.handleGenerate(w, r)
	case r.Method == http.MethodDelete && path == "/assets/delete":
		a.handleDelete(w, r)
	default:
		w.Header().Set("Allow", "POST, DELETE, GET")
		a.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// authorized проверяет Authorization: Bearer <token> constant-time.
//
// Чтобы не раскрывать длину токена по timing, сравниваются SHA-256 хеши
// предоставленного токена и ожидаемого: хеши всегда имеют одинаковую длину,
// поэтому subtle.ConstantTimeCompare выполняется за константное время без
// раннего выхода по длине.
func (a *AdminHandler) authorized(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	token := strings.TrimSpace(auth[len(prefix):])
	if token == "" {
		return false
	}
	got := sha256.Sum256([]byte(token))
	want := sha256.Sum256([]byte(a.cfg.Token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// generateRequest — тело POST /admin/assets/generate.
type generateRequest struct {
	Source dynamic.String      `json:"source"`
	Assets dynamic.StringSlice `json:"assets"`
	Wait   dynamic.Bool        `json:"wait"`
}

// handleGenerate обрабатывает POST /admin/assets/generate.
func (a *AdminHandler) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		a.mapDecodeError(w, r, err)
		return
	}
	// Ровно одно из source/assets.
	if (req.Source == "") == (len(req.Assets) == 0) {
		a.writeError(w, r, http.StatusBadRequest, "invalid", "exactly one of source or assets is required")
		return
	}

	res, err := a.svc.EnqueueGenerate(req.Source.Unwrap(), req.Assets.Unwrap(), req.Wait.Unwrap())
	if err != nil {
		a.mapServiceError(w, r, err)
		return
	}

	if req.Wait {
		a.writeJSON(w, r, http.StatusOK, res)
		return
	}
	a.writeJSON(w, r, http.StatusAccepted, res)
}

// deleteRequest — тело DELETE /admin/assets/delete.
type deleteRequest struct {
	Source dynamic.String      `json:"source"`
	Assets dynamic.StringSlice `json:"assets"`
}

// handleDelete обрабатывает DELETE /admin/assets/delete.
func (a *AdminHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req deleteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		a.mapDecodeError(w, r, err)
		return
	}
	if (req.Source == "") == (len(req.Assets) == 0) {
		a.writeError(w, r, http.StatusBadRequest, "invalid", "exactly one of source or assets is required")
		return
	}

	var deleted int
	var err error
	if req.Source.Unwrap() != "" {
		deleted, err = a.svc.DeleteBySource(r.Context(), req.Source.Unwrap())
	} else {
		deleted, err = a.svc.DeleteAssets(r.Context(), req.Assets.Unwrap())
	}
	if err != nil {
		a.mapServiceError(w, r, err)
		return
	}
	a.writeJSON(w, r, http.StatusOK, map[string]any{
		"status":  "completed",
		"deleted": deleted,
	})
}

// mapServiceError маппит ошибку adminsvc в HTTP-ответ.
func (a *AdminHandler) mapServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, admin.ErrInvalidRequest):
		a.writeError(w, r, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, admin.ErrSourceNotFound):
		a.writeError(w, r, http.StatusNotFound, "not_found", "source not found")
	case errors.Is(err, admin.ErrQueueFull):
		a.writeError(w, r, http.StatusServiceUnavailable, "overloaded", "queue is full")
	case errors.Is(err, admin.ErrNotImplemented):
		a.writeError(w, r, http.StatusNotImplemented, "not_implemented", "result store does not support listing")
	case errors.Is(err, admin.ErrWaitTimeout):
		a.writeError(w, r, http.StatusGatewayTimeout, "timeout", "wait timeout")
	default:
		a.log.Errorf("httpapi: admin: %v", err)
		a.writeError(w, r, http.StatusInternalServerError, "internal", "internal server error")
	}
}

// maxBodyBytes — максимальный размер тела запроса (1 МБ).
const maxBodyBytes = 1 << 20

// errBodyTooLarge — тело запроса превышает maxBodyBytes (→ HTTP 413).
var errBodyTooLarge = errors.New("request body too large")

// mapDecodeError маппит ошибку декодирования тела в HTTP-ответ.
func (a *AdminHandler) mapDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errBodyTooLarge) {
		a.writeError(w, r, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		return
	}
	a.writeError(w, r, http.StatusBadRequest, "invalid", "invalid json body")
}

// decodeJSON читает и декодирует JSON-тело запроса.
//
// Читается maxBodyBytes+1 байт: если тело превышает лимит — возвращается
// errBodyTooLarge (→ HTTP 413), а не невнятная ошибка "invalid json body".
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxBodyBytes {
		return errBodyTooLarge
	}
	if len(body) == 0 {
		return errors.New("empty body")
	}
	return json.Unmarshal(body, v)
}

// writeJSON пишет JSON-ответ. При ошибке маршалинга логирует её и пишет
// пустое тело с корректным статусом, а не молча.
func (a *AdminHandler) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	body, err := json.Marshal(v)
	if err != nil {
		a.log.Errorf("httpapi: admin: json.Marshal: %v", err)
		body = nil
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// writeError пишет стабильный error envelope (в стиле Handler.writeError).
// При ошибке марширования логирует её и пишет пустое тело с корректным
// статусом, а не молча.
func (a *AdminHandler) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	body, err := json.Marshal(errorEnvelope{Error: errorDetail{Code: code, Message: message}})
	if err != nil {
		a.log.Errorf("httpapi: admin: marshal error envelope: %v", err)
		body = nil
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
