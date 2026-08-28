package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAdmissionControlAllowsUnderLimit проверяет, что при свободном семафоре
// запрос проходит к нижележащему handler.
func TestAdmissionControlAllowsUnderLimit(t *testing.T) {
	ac := NewAdmissionControl(1)
	var called int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	wrapped := ac.Wrap(next)

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if called != 1 {
		t.Fatalf("next called %d times, want 1", called)
	}
}

// TestAdmissionControlRejectsOverLimit проверяет, что при переполнении
// семафора возвращается HTTP 503 + Retry-After, а следующий handler не
// вызывается.
func TestAdmissionControlRejectsOverLimit(t *testing.T) {
	// Семафор ёмкостью 1: первый запрос занимает слот, второй отклоняется.
	ac := NewAdmissionControl(1)

	entered := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	wrapped := ac.Wrap(next)

	// Первый запрос занимает единственный слот и блокируется.
	firstDone := make(chan struct{}, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/a", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		firstDone <- struct{}{}
	}()

	// Дожидаемся, пока первый запрос войдёт в handler (займёт слот).
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not enter handler")
	}

	// Второй запрос должен быть отклонён с 503 + Retry-After.
	req := httptest.NewRequest(http.MethodGet, "/b", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Fatalf("Retry-After = %q, want 1", ra)
	}

	// Освобождаем слот, чтобы первый запрос завершился.
	release <- struct{}{}
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not finish")
	}
}

// TestAdmissionControlZeroMeansUnlimited проверяет, что maxConcurrent == 0
// означает отсутствие ограничения: запрос проходит без семафора.
func TestAdmissionControlZeroMeansUnlimited(t *testing.T) {
	ac := NewAdmissionControl(0)
	var called int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	wrapped := ac.Wrap(next)

	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if called != 1 {
		t.Fatalf("next called %d times, want 1", called)
	}
}

// TestAdmissionControlReleasesSlotAfterCompletion проверяет, что слот
// освобождается после завершения запроса (следующий запрос проходит).
func TestAdmissionControlReleasesSlotAfterCompletion(t *testing.T) {
	ac := NewAdmissionControl(1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := ac.Wrap(next)

	// Первый запрос — проходит и освобождает слот.
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", rec.Code)
	}

	// Второй запрос — слот свободен, проходит.
	req = httptest.NewRequest(http.MethodGet, "/b", nil)
	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200", rec.Code)
	}
}
