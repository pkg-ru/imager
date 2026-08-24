package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRuntimeReadinessLiveness(t *testing.T) {
	requireLocalhostTCP(t)
	rt, err := NewRuntime(RuntimeOptions{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		Addr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	defer func() { _ = rt.Shutdown(context.Background()) }()

	health := NewHealth(rt)
	mux := NewMuxWithAdmission(newTestHandler(t, newFakeGenerator(), baseConfig()), health, nil,
		MetricsAuthConfig{}, 0, nil)

	// Liveness до shutdown.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness status = %d, want 200", rec.Code)
	}

	// Readiness до shutdown.
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness status = %d, want 200", rec.Code)
	}

	// Shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Readiness после shutdown → 503.
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness after shutdown = %d, want 503", rec.Code)
	}

	// Liveness после shutdown → 503 (процесс завершается).
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("liveness after shutdown = %d, want 503", rec.Code)
	}
}

func TestRuntimeServeAndShutdown(t *testing.T) {
	requireLocalhostTCP(t)
	rt, err := NewRuntime(RuntimeOptions{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		Addr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- rt.Serve()
	}()

	// Даём серверу стартовать.
	time.Sleep(50 * time.Millisecond)

	// Реальный HTTP-запрос.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + rt.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Graceful shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Serve должен завершиться без ошибки.
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
}

// TestRuntimeShutdownTimeoutForceClose проверяет, что при превышении
// ShutdownTimeout активные соединения принудительно закрываются и Shutdown
// возвращается (bounded graceful shutdown, без зависания).
func TestRuntimeShutdownTimeoutForceClose(t *testing.T) {
	requireLocalhostTCP(t)
	release := make(chan struct{})
	rt, err := NewRuntime(RuntimeOptions{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Запрос никогда не завершается сам по себе.
			<-release
			w.WriteHeader(http.StatusOK)
		}),
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- rt.Serve()
	}()
	time.Sleep(50 * time.Millisecond)

	// Запускаем активный запрос, который не завершается.
	done := make(chan struct{})
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + rt.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
		close(done)
	}()

	// Shutdown должен вернуться после таймаута (force close).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := rt.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("Shutdown took %v, want bounded by timeout", elapsed)
	}

	// Освобождаем запрос, чтобы не оставить goroutine.
	close(release)
	<-done
}

func TestRuntimeShutdownWaitsForActiveRequest(t *testing.T) {
	requireLocalhostTCP(t)
	release := make(chan struct{})
	started := make(chan struct{})
	rt, err := NewRuntime(RuntimeOptions{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			w.WriteHeader(http.StatusOK)
		}),
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- rt.Serve()
	}()
	time.Sleep(50 * time.Millisecond)

	// Запускаем активный запрос.
	done := make(chan struct{})
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + rt.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	<-started

	// Shutdown должен дождаться активного запроса.
	shutdownDone := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.Shutdown(ctx)
		close(shutdownDone)
	}()

	// Shutdown не должен завершиться, пока запрос активен.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while request still active")
	case <-time.After(200 * time.Millisecond):
	}

	// Освобождаем запрос.
	close(release)
	<-done

	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not complete after request finished")
	}
}
