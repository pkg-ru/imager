package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pkg-ru/imager/adapters/storage/remote"
	"github.com/pkg-ru/imager/domain/object"
)

func TestNewSourceStoreValidation(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "empty", baseURL: "", wantErr: true},
		{name: "invalid url", baseURL: "://bad", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://host/path", wantErr: true},
		{name: "missing host", baseURL: "http:///path", wantErr: true},
		{name: "query rejected", baseURL: "https://host/path?token=secret", wantErr: true},
		{name: "fragment rejected", baseURL: "https://host/path#frag", wantErr: true},
		{name: "http ok", baseURL: "http://host/path/", wantErr: false},
		{name: "https ok", baseURL: "https://host/path/", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSourceStore(Options{BaseURL: tt.baseURL})
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewSourceStore(%q) error = %v, wantErr %v", tt.baseURL, err, tt.wantErr)
			}
		})
	}
}

func TestSourceStoreURLJoining(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/path_to_image/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), object.ObjectKey("foo/bar.jpg")); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if gotPath != "/path_to_image/foo/bar.jpg" {
		t.Fatalf("got path %q, want %q", gotPath, "/path_to_image/foo/bar.jpg")
	}
}

func TestSourceStoreLookupUsesHEAD(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Length", "42")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	meta, err := s.Lookup(context.Background(), object.ObjectKey("img.jpg"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if gotMethod != http.MethodHead {
		t.Fatalf("method = %q, want HEAD", gotMethod)
	}
	if meta.Size != 42 {
		t.Fatalf("Size = %d, want 42", meta.Size)
	}
	if meta.ContentType != "image/jpeg" {
		t.Fatalf("ContentType = %q, want image/jpeg", meta.ContentType)
	}
	if meta.ETag != `"abc"` {
		t.Fatalf("ETag = %q, want \"abc\"", meta.ETag)
	}
	if meta.ModTime.IsZero() {
		t.Fatal("ModTime is zero, want parsed Last-Modified")
	}
}

func TestSourceStoreOpenReadsBody(t *testing.T) {
	body := "fake-image-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	art, err := s.Open(context.Background(), object.ObjectKey("img.png"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()

	data, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != body {
		t.Fatalf("body = %q, want %q", string(data), body)
	}
	if art.Metadata().Size != int64(len(body)) {
		t.Fatalf("Size = %d, want %d", art.Metadata().Size, len(body))
	}
	if art.Metadata().ContentType != "image/png" {
		t.Fatalf("ContentType = %q, want image/png", art.Metadata().ContentType)
	}
}

func TestSourceStoreNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), object.ObjectKey("missing.jpg")); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("Lookup error = %v, want ErrNotFound", err)
	}
	if _, err := s.Open(context.Background(), object.ObjectKey("missing.jpg")); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("Open error = %v, want ErrNotFound", err)
	}
}

func TestSourceStoreGoneIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), object.ObjectKey("gone.jpg")); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("Lookup error = %v, want ErrNotFound", err)
	}
}

func TestSourceStoreRedirectIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), object.ObjectKey("img.jpg")); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("Lookup error = %v, want ErrUnavailable", err)
	}
	if _, err := s.Open(context.Background(), object.ObjectKey("img.jpg")); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("Open error = %v, want ErrUnavailable", err)
	}
}

func TestSourceStoreServerErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), object.ObjectKey("img.jpg")); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("Lookup error = %v, want ErrUnavailable", err)
	}
}

func TestSourceStoreUnauthorizedIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), object.ObjectKey("img.jpg")); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("Lookup error = %v, want ErrUnavailable", err)
	}
}

func TestSourceStoreUnsafeKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	for _, key := range []object.ObjectKey{"../secret", "a/../../b", `a\b`, ""} {
		if _, err := s.Lookup(context.Background(), key); !errors.Is(err, object.ErrUnsafePath) {
			t.Fatalf("Lookup(%q) error = %v, want ErrUnsafePath", key, err)
		}
		if _, err := s.Open(context.Background(), key); !errors.Is(err, object.ErrUnsafePath) {
			t.Fatalf("Open(%q) error = %v, want ErrUnsafePath", key, err)
		}
	}
}

func TestSourceStoreSpoolLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 1024))
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/", SpoolMaxBytes: 512})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Open(context.Background(), object.ObjectKey("big.jpg")); !errors.Is(err, object.ErrQuota) {
		t.Fatalf("Open error = %v, want ErrQuota", err)
	}
}

func TestSourceStoreContentLengthLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2048")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/", SpoolMaxBytes: 1024})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Open(context.Background(), object.ObjectKey("big.jpg")); !errors.Is(err, object.ErrQuota) {
		t.Fatalf("Open error = %v, want ErrQuota", err)
	}
}

func TestSourceStoreContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Lookup(ctx, object.ObjectKey("img.jpg")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lookup error = %v, want context.Canceled", err)
	}
}

func TestSourceStoreTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{
		BaseURL:     srv.URL + "/",
		ConnOptions: remote.ConnOptions{ReadTimeout: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	// Таймаут HTTP-клиента оборачивается в context.DeadlineExceeded, который
	// remote.MapError сохраняет как есть (контракт обработки отмены).
	if _, err := s.Lookup(context.Background(), object.ObjectKey("img.jpg")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lookup error = %v, want context.DeadlineExceeded", err)
	}
}

func TestSourceStoreSeekable(t *testing.T) {
	body := "seekable-body"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	s, err := NewSourceStore(Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	art, err := s.Open(context.Background(), object.ObjectKey("img.jpg"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()

	if _, err := art.Seek(7, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(art, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != "e-bo" {
		t.Fatalf("read after seek = %q, want %q", string(buf), "e-bo")
	}
}

// TestRetryBackoffJitter проверяет, что backoff растёт экспоненциально и
// содержит случайный джиттер: задержка не должна быть детерминированной.
func TestRetryBackoffJitter(t *testing.T) {
	// Первая попытка: base*1 + jitter ∈ [100ms, 200ms).
	d0 := retryBackoff(0)
	if d0 < 100*time.Millisecond || d0 >= 200*time.Millisecond {
		t.Fatalf("retryBackoff(0) = %v, want in [100ms, 200ms)", d0)
	}
	// Вторая попытка: base*2 + jitter ∈ [200ms, 300ms).
	d1 := retryBackoff(1)
	if d1 < 200*time.Millisecond || d1 >= 300*time.Millisecond {
		t.Fatalf("retryBackoff(1) = %v, want in [200ms, 300ms)", d1)
	}
	// Джиттер: несколько вызовов с одним индексом не должны быть одинаковыми.
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[retryBackoff(0)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("retryBackoff(0) deterministic over 50 calls: %d distinct values", len(seen))
	}
}
