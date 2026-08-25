package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg-ru/imager/app/adminsvc"
	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/ports/storage"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/domain/policy"
)

// adminTestCtx — контекст для тестов admin handler.
type adminTestCtx struct {
	svc  *adminsvc.Service
	gen  *adminFakeGenerator
	src  *adminMemSourceStore
	res  *adminMemResultStore
	cfg  AdminConfig
	auth *AdminHandler
}

// newAdminTestCtx собирает Service + AdminHandler с fakes.
func newAdminTestCtx(t *testing.T) *adminTestCtx {
	t.Helper()
	gen := newAdminFakeGenerator()
	src := newAdminMemSourceStore()
	res := newAdminMemResultStore()
	presets, err := asset.NewPresetSet([]*asset.Preset{})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	pol := &policy.Policy{Global: policy.GlobalPolicy{Authorization: policy.AuthSafe}}
	svc, err := adminsvc.New(adminsvc.Deps{
		Gen:     gen,
		Sources: src,
		Results: res,
		Presets: presets,
		Policy:  pol,
		Logger:  adminFakeLogger{},
	}, adminsvc.Config{Workers: 2, QueueSize: 4, WaitTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("adminsvc.New: %v", err)
	}
	cfg := AdminConfig{Enabled: true, Token: "secret-token", Workers: 2, QueueSize: 4, WaitTimeout: 5 * time.Second}
	return &adminTestCtx{svc: svc, gen: gen, src: src, res: res, cfg: cfg, auth: NewAdminHandler(svc, cfg, adminFakeLogger{})}
}

// adminFakeLogger — заглушка.
type adminFakeLogger struct{}

func (adminFakeLogger) Debugf(string, ...any) {}
func (adminFakeLogger) Infof(string, ...any)  {}
func (adminFakeLogger) Warnf(string, ...any)  {}
func (adminFakeLogger) Errorf(string, ...any) {}

// adminMemArtifact — in-memory object.Artifact.
type adminMemArtifact struct {
	data []byte
	meta object.ObjectMetadata
	off  int
}

func (a *adminMemArtifact) Read(p []byte) (int, error) {
	if a.off >= len(a.data) {
		return 0, io.EOF
	}
	n := copy(p, a.data[a.off:])
	a.off += n
	return n, nil
}

func (a *adminMemArtifact) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = int64(a.off)
	case io.SeekEnd:
		base = int64(len(a.data))
	default:
		return 0, errors.New("invalid whence")
	}
	base += offset
	if base < 0 {
		return 0, errors.New("negative seek")
	}
	a.off = int(base)
	return base, nil
}

func (a *adminMemArtifact) Close() error { return nil }

func (a *adminMemArtifact) Metadata() object.ObjectMetadata { return object.ObjectMetadata{} }

// adminMemSourceStore — in-memory storage.SourceStore.
type adminMemSourceStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newAdminMemSourceStore() *adminMemSourceStore {
	return &adminMemSourceStore{data: map[string][]byte{}}
}

func (s *adminMemSourceStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key.String()]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (s *adminMemSourceStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &adminMemArtifact{data: d}, nil
}

var _ storage.SourceStore = (*adminMemSourceStore)(nil)

// adminMemResultStore — in-memory storage.ResultStore с List.
type adminMemResultStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newAdminMemResultStore() *adminMemResultStore {
	return &adminMemResultStore{data: map[string][]byte{}}
}

func (r *adminMemResultStore) Lookup(_ context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key.String()]
	if !ok {
		return object.ObjectMetadata{}, &object.NotFoundError{Key: key}
	}
	return object.ObjectMetadata{Key: key, Size: int64(len(d))}, nil
}

func (r *adminMemResultStore) Open(_ context.Context, key object.ObjectKey) (object.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &adminMemArtifact{data: d}, nil
}

func (r *adminMemResultStore) ReadStream(_ context.Context, key object.ObjectKey) (object.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[key.String()]
	if !ok {
		return nil, &object.NotFoundError{Key: key}
	}
	return &adminMemArtifact{data: d}, nil
}

func (r *adminMemResultStore) Publish(_ context.Context, key object.ObjectKey, src io.Reader, _ object.PublishOptions) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key.String()] = data
	return nil
}

func (r *adminMemResultStore) Delete(_ context.Context, key object.ObjectKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key.String())
	return nil
}

func (r *adminMemResultStore) Stats(_ context.Context) (object.StoreStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var st object.StoreStats
	for _, d := range r.data {
		st.Objects++
		st.TotalBytes += int64(len(d))
	}
	return st, nil
}

func (r *adminMemResultStore) List(_ context.Context, prefix object.ObjectKey) ([]object.ObjectKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []object.ObjectKey
	for k := range r.data {
		if strings.HasPrefix(k, prefix.String()) {
			out = append(out, object.ObjectKey(k))
		}
	}
	return out, nil
}

var _ storage.ResultStore = (*adminMemResultStore)(nil)
var _ storage.Lister = (*adminMemResultStore)(nil)

// adminFakeGenerator — управляемый fake Generator.
type adminFakeGenerator struct {
	mu       sync.Mutex
	results  map[string]*generatev2.Result
	errs     map[string]error
	fallback error
}

func newAdminFakeGenerator() *adminFakeGenerator {
	return &adminFakeGenerator{
		results: map[string]*generatev2.Result{},
		errs:    map[string]error{},
	}
}

func (f *adminFakeGenerator) addResult(url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[url] = &generatev2.Result{URL: url, Key: object.ObjectKey(url)}
}

func (f *adminFakeGenerator) setErr(url string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[url] = err
}

func (f *adminFakeGenerator) Generate(_ context.Context, req *asset.Request) (*generatev2.Result, error) {
	url, err := req.Build()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.errs[url]; ok {
		return nil, e
	}
	if r, ok := f.results[url]; ok {
		return r, nil
	}
	if f.fallback != nil {
		return nil, f.fallback
	}
	return nil, &generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "not found"}
}

var _ adminsvc.Generator = (*adminFakeGenerator)(nil)

// doAdmin выполняет запрос к admin handler с bearer-токеном.
func doAdmin(a *AdminHandler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

// decodeBody декодирует JSON-тело ответа.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return m
}

// TestAdminAuthMissingToken — отсутствующий токен → 403.
func TestAdminAuthMissingToken(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "", `{"source":"thumbs/photo.jpg"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// TestAdminAuthWrongToken — неверный токен → 403.
func TestAdminAuthWrongToken(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "wrong", `{"source":"thumbs/photo.jpg"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// TestAdminAuthValidToken — верный токен проходит.
func TestAdminAuthValidToken(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token", `{"source":"thumbs/photo.jpg"}`)
	// Исходник не существует → 404 (значит авторизация прошла).
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (auth passed, source missing)", rec.Code)
	}
}

// TestAdminGenerateBadJSON — невалидный JSON → 400.
func TestAdminGenerateBadJSON(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token", `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestAdminGenerateBothFields — заданы оба source/assets → 400.
func TestAdminGenerateBothFields(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token",
		`{"source":"thumbs/photo.jpg","assets":["thumbs/photo-jpg/c-120x80@2.webp"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestAdminGenerateNeitherField — ни одного из source/assets → 400.
func TestAdminGenerateNeitherField(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestAdminGenerateAsync202 — wait=false → 202 accepted.
func TestAdminGenerateAsync202(t *testing.T) {
	ctx := newAdminTestCtx(t)
	ctx.svc.Start(context.Background())
	defer ctx.svc.Stop()
	ctx.gen.addResult("thumbs/photo-jpg/c-120x80@2.webp")

	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token",
		`{"assets":["thumbs/photo-jpg/c-120x80@2.webp"],"wait":false}`)
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["status"] != "accepted" {
		t.Errorf("status field = %v, want accepted", m["status"])
	}
	if m["job_id"] == nil || m["job_id"] == "" {
		t.Error("job_id missing")
	}
}

// TestAdminGenerateWait200 — wait=true → 200 completed.
func TestAdminGenerateWait200(t *testing.T) {
	ctx := newAdminTestCtx(t)
	ctx.svc.Start(context.Background())
	defer ctx.svc.Stop()
	ctx.gen.addResult("thumbs/photo-jpg/c-120x80@2.webp")

	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token",
		`{"assets":["thumbs/photo-jpg/c-120x80@2.webp"],"wait":true}`)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["status"] != "completed" {
		t.Errorf("status field = %v, want completed", m["status"])
	}
}

// TestAdminGenerateSourceNotFound404 — несуществующий исходник → 404.
func TestAdminGenerateSourceNotFound404(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token",
		`{"source":"thumbs/missing.jpg"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestAdminGenerateQueueFull503 — переполнение очереди → 503.
func TestAdminGenerateQueueFull503(t *testing.T) {
	// Отдельный Service с QueueSize=1 и без запущенных воркеров.
	gen := newAdminFakeGenerator()
	src := newAdminMemSourceStore()
	res := newAdminMemResultStore()
	presets, err := asset.NewPresetSet([]*asset.Preset{})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	svc, err := adminsvc.New(adminsvc.Deps{
		Gen:     gen,
		Sources: src,
		Results: res,
		Presets: presets,
		Policy:  &policy.Policy{},
		Logger:  adminFakeLogger{},
	}, adminsvc.Config{Workers: 1, QueueSize: 1, WaitTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("adminsvc.New: %v", err)
	}
	auth := NewAdminHandler(svc, AdminConfig{Enabled: true, Token: "secret-token", Workers: 1, QueueSize: 1, WaitTimeout: 5 * time.Second}, adminFakeLogger{})

	// Первая задача занимает очередь.
	rec1 := doAdmin(auth, http.MethodPost, "/admin/assets/generate", "secret-token",
		`{"assets":["thumbs/photo-jpg/c-120x80@2.webp"],"wait":false}`)
	if rec1.Code != http.StatusAccepted {
		t.Errorf("first status = %d, want 202", rec1.Code)
	}
	// Вторая — переполнение → 503.
	rec2 := doAdmin(auth, http.MethodPost, "/admin/assets/generate", "secret-token",
		`{"assets":["thumbs/photo-jpg/c-120x80@3.webp"],"wait":false}`)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("second status = %d, want 503", rec2.Code)
	}
}

// TestAdminDeleteBySource — DELETE по source → 200 deleted.
func TestAdminDeleteBySource(t *testing.T) {
	ctx := newAdminTestCtx(t)
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), strings.NewReader("x"), object.PublishOptions{})
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/c-120x80@2.webp"), strings.NewReader("x"), object.PublishOptions{})

	rec := doAdmin(ctx.auth, http.MethodDelete, "/admin/assets/delete", "secret-token",
		`{"source":"thumbs/photo.jpg"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if got := int(m["deleted"].(float64)); got != 2 {
		t.Errorf("deleted = %v, want 2", got)
	}
}

// TestAdminDeleteAssets — DELETE по assets → 200 deleted.
func TestAdminDeleteAssets(t *testing.T) {
	ctx := newAdminTestCtx(t)
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), strings.NewReader("x"), object.PublishOptions{})

	rec := doAdmin(ctx.auth, http.MethodDelete, "/admin/assets/delete", "secret-token",
		`{"assets":["thumbs/photo-jpg/thumb.webp"]}`)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if got := int(m["deleted"].(float64)); got != 1 {
		t.Errorf("deleted = %v, want 1", got)
	}
}

// TestAdminMethodNotAllowed — неверный метод → 405.
func TestAdminMethodNotAllowed(t *testing.T) {
	ctx := newAdminTestCtx(t)
	rec := doAdmin(ctx.auth, http.MethodGet, "/admin/assets/generate", "secret-token", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestAdminOversizedBody413 — тело >1МБ → 413 Request Entity Too Large.
func TestAdminOversizedBody413(t *testing.T) {
	ctx := newAdminTestCtx(t)
	// Тело больше 1 МБ (maxBodyBytes+1 байт).
	big := `{"source":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "secret-token", big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["error"] == nil {
		t.Fatal("error envelope missing")
	}
	env := m["error"].(map[string]any)
	if env["code"] != "too_large" {
		t.Errorf("error.code = %v, want too_large", env["code"])
	}
}

// TestAdminAuthWrongTokenLength — неверный токен другой длины отклоняется
// (constant-time авторизация не раскрывает длину токена).
func TestAdminAuthWrongTokenLength(t *testing.T) {
	ctx := newAdminTestCtx(t)
	// Токен другой длины, чем "secret-token".
	rec := doAdmin(ctx.auth, http.MethodPost, "/admin/assets/generate", "x", `{"source":"thumbs/photo.jpg"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// TestAdminDeleteBySourceDeletedCount — DELETE по source возвращает число
// удалённых ассетов.
func TestAdminDeleteBySourceDeletedCount(t *testing.T) {
	ctx := newAdminTestCtx(t)
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), strings.NewReader("x"), object.PublishOptions{})
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/c-120x80@2.webp"), strings.NewReader("x"), object.PublishOptions{})
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/640x.webp"), strings.NewReader("x"), object.PublishOptions{})

	rec := doAdmin(ctx.auth, http.MethodDelete, "/admin/assets/delete", "secret-token",
		`{"source":"thumbs/photo.jpg"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["status"] != "completed" {
		t.Errorf("status = %v, want completed", m["status"])
	}
	if got := int(m["deleted"].(float64)); got != 3 {
		t.Errorf("deleted = %v, want 3", got)
	}
}

// TestAdminDeleteAssetsDeletedCount — DELETE по assets возвращает число
// удалённых ассетов.
func TestAdminDeleteAssetsDeletedCount(t *testing.T) {
	ctx := newAdminTestCtx(t)
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/thumb.webp"), strings.NewReader("x"), object.PublishOptions{})
	ctx.res.Publish(context.Background(), object.ObjectKey("thumbs/photo-jpg/640x.webp"), strings.NewReader("x"), object.PublishOptions{})

	rec := doAdmin(ctx.auth, http.MethodDelete, "/admin/assets/delete", "secret-token",
		`{"assets":["thumbs/photo-jpg/thumb.webp","thumbs/photo-jpg/640x.webp"]}`)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["status"] != "completed" {
		t.Errorf("status = %v, want completed", m["status"])
	}
	if got := int(m["deleted"].(float64)); got != 2 {
		t.Errorf("deleted = %v, want 2", got)
	}
}
