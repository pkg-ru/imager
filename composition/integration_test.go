package composition

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/ports/storage"
)

// buildFSApp собирает полный production pipeline с реальными FS-хранилищами
// (SourceStore + ResultStore) и fake processor. Возвращает App и каталоги.
func buildFSApp(t *testing.T, opts ...func(*AppOptions)) (*App, string, string) {
	t.Helper()
	rc, err := ParseRuntimeConfig([]byte(testConfigYAML))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	srcDir := t.TempDir()
	resDir := t.TempDir()

	ao := AppOptions{
		Config:    rc.Pipeline,
		HTTP:      rc.HTTP,
		SourceDir: srcDir,
		ResultDir: resDir,
		Processor: fakeProcessor{},
	}
	for _, o := range opts {
		o(&ao)
	}
	app, err := Build(context.Background(), ao)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app, srcDir, resDir
}

// seedSource записывает исходный файл в SourceDir.
func seedSource(t *testing.T, srcDir, rel string, data []byte) {
	t.Helper()
	full := filepath.Join(srcDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
}

// TestIntegrationFullPipelineFS проверяет полный конвейер:
// HTTP → application/generatev2 → fake processor → FS ResultStore → HTTP.
// fakeProcessor копирует исходник в результат, поэтому body == source.
func TestIntegrationFullPipelineFS(t *testing.T) {
	app, srcDir, resDir := buildFSApp(t)
	seedSource(t, srcDir, "img.png", []byte("RAWIMAGE"))

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "RAWIMAGE" {
		t.Errorf("body = %q, want RAWIMAGE", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type = %q, want image/webp", ct)
	}
	if etag := rec.Header().Get("ETag"); etag == "" {
		t.Error("ETag missing")
	}

	// Результат должен быть опубликован в ResultStore (кэш).
	stats, err := app.Results.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 1 {
		t.Fatalf("result objects = %d, want 1", stats.Objects)
	}
	_ = resDir
}

// TestIntegrationCacheHitNoRegeneration проверяет, что повторный запрос
// попадает в кэш и не вызывает повторную обработку.
func TestIntegrationCacheHitNoRegeneration(t *testing.T) {
	app, srcDir, _ := buildFSApp(t)
	seedSource(t, srcDir, "img.png", []byte("RAWIMAGE"))

	do := func() string {
		req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	if got := do(); got != "RAWIMAGE" {
		t.Fatalf("first body = %q, want RAWIMAGE", got)
	}
	if got := do(); got != "RAWIMAGE" {
		t.Fatalf("second body = %q, want RAWIMAGE", got)
	}

	// Кэш должен содержать ровно один объект.
	stats, err := app.Results.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 1 {
		t.Fatalf("result objects = %d, want 1 (cache hit)", stats.Objects)
	}
}

// TestIntegrationPresetResultStoredByCanonicalURL проверяет, что preset-запрос
// /test-jpg/thumb@2.webp после раскрытия пресета сохраняет результат в
// ResultStore под каноническим URL (test-jpg/thumb@2.webp), а не под
// SHA-256 хешем. Это regression-тест для перехода с hash-based на
// canonical-URL-based ключа хранения.
func TestIntegrationPresetResultStoredByCanonicalURL(t *testing.T) {
	app, srcDir, resDir := buildFSApp(t)
	seedSource(t, srcDir, "test.jpg", []byte("RAWIMAGE"))

	do := func() string {
		req := httptest.NewRequest(http.MethodGet, "/test-jpg/thumb@2.webp", nil)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	if got := do(); got != "RAWIMAGE" {
		t.Fatalf("first body = %q, want RAWIMAGE", got)
	}
	if got := do(); got != "RAWIMAGE" {
		t.Fatalf("second body = %q, want RAWIMAGE (cache hit)", got)
	}

	// Результат должен быть опубликован ровно под одним каноническим именем.
	stats, err := app.Results.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 1 {
		t.Fatalf("result objects = %d, want 1", stats.Objects)
	}

	// Физический файл должен существовать под каноническим URL-именем.
	// В новой грамматике канонический URL строится из имени сегмента
	// (пресета), а не из transform-кода.
	want := filepath.Join(resDir, "test-jpg/thumb@2.webp")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected result file %q to exist: %v", want, err)
	}
}

// TestIntegrationNotFoundFS проверяет 404 при отсутствующем исходнике.
func TestIntegrationNotFoundFS(t *testing.T) {
	app, _, _ := buildFSApp(t)

	req := httptest.NewRequest(http.MethodGet, "/missing-png/thumb.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "not_found")
}

// TestIntegrationTraversalRejectedFS проверяет, что traversal-ключ не
// приводит к чтению файла вне SourceDir.
func TestIntegrationTraversalRejectedFS(t *testing.T) {
	app, srcDir, _ := buildFSApp(t)
	// Секретный файл вне SourceDir.
	outside := filepath.Join(filepath.Dir(srcDir), "secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	defer os.Remove(outside)

	// Traversal через URL: /../secret.txt... — должен быть отклонён на
	// уровне парсера (400), а не прочитать файл.
	req := httptest.NewRequest(http.MethodGet, "/../secret-txt/thumb.webp", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (traversal rejected)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatal("traversal leaked file content")
	}
}

// TestIntegrationSourceStoreContractFS проверяет, что FS SourceStore
// удовлетворяет read-only SourceStore контракту (пригоден для FTP-адаптера).
func TestIntegrationSourceStoreContractFS(t *testing.T) {
	app, srcDir, _ := buildFSApp(t)
	seedSource(t, srcDir, "bucket/img.jpg", []byte("SRC"))

	ss, ok := app.Sources.(storage.SourceStore)
	if !ok {
		t.Fatalf("Sources is not SourceStore: %T", app.Sources)
	}
	art, err := ss.Open(context.Background(), object.ObjectKey("bucket/img.jpg"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "SRC" {
		t.Fatalf("read = %q, want SRC", got)
	}
}

// TestIntegrationConcurrentHTTPStampede проверяет, что конкурентные HTTP-
// запросы одного ключа (cache stampede) через полный pipeline возвращают
// корректные данные каждому клиенту (regression для общего artifact).
func TestIntegrationConcurrentHTTPStampede(t *testing.T) {
	app, srcDir, _ := buildFSApp(t)
	seedSource(t, srcDir, "img.png", []byte("RAWIMAGE"))

	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.webp", nil)
			rec := httptest.NewRecorder()
			app.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				errs <- &httpError{code: rec.Code, body: rec.Body.String()}
				return
			}
			if body := rec.Body.String(); body != "RAWIMAGE" {
				errs <- &httpError{code: rec.Code, body: body}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent request error: %v", err)
	}
}

// httpError — вспомогательная ошибка для тестов.
type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string {
	return "status=" + itoa(e.code) + " body=" + e.body
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// assertErrorCode проверяет, что тело ответа содержит error.code == want.
func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("error body not JSON: %v (body=%q)", err, rec.Body.String())
	}
	if env.Error.Code != want {
		t.Errorf("error code = %q, want %q", env.Error.Code, want)
	}
}
