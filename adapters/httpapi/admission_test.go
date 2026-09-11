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
	wrapped := ac.Wrap(next, nil)

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
	wrapped := ac.Wrap(next, nil)

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
	wrapped := ac.Wrap(next, nil)

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
	wrapped := ac.Wrap(next, nil)

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

// TestAdmissionControlDynamicRetryAfter проверяет, что Retry-After при
// переполнении семафора динамический (зависит от текущей занятости), а не
// фиксированный "1". При ёмкости 1 значение остаётся "1" (совместимость с
// существующим поведением); при большей ёмкости — масштабируется от числа
// занятых слотов.
func TestAdmissionControlDynamicRetryAfter(t *testing.T) {
	// Семафор ёмкостью 3: заполняем все слоты, затем отклоняем запрос.
	ac := NewAdmissionControl(3)

	entered := make(chan struct{}, 3)
	release := make(chan struct{}, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	wrapped := ac.Wrap(next, nil)

	// Занимаем все 3 слота.
	for i := 0; i < 3; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/a", nil)
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
		}()
	}
	// Дожидаемся, пока все 3 запроса войдут в handler.
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("request did not enter handler")
		}
	}

	// Четвёртый запрос отклоняется с динамическим Retry-After (>= 3).
	req := httptest.NewRequest(http.MethodGet, "/b", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	ra := rec.Header().Get("Retry-After")
	if ra == "" || ra == "1" {
		t.Fatalf("Retry-After = %q, want dynamic (>= 3)", ra)
	}

	// Освобождаем слоты, чтобы горутины завершились.
	close(release)
	time.Sleep(50 * time.Millisecond)
}

// TestAdmissionControlBypassSameAssetPasses проверяет, что при переполнении
// семафора запрос, для которого bypassCheck возвращает true (может
// присоединиться к уже идущей singleflight-генерации того же ассета),
// пропускается в обход семафора (200, а не 503).
func TestAdmissionControlBypassSameAssetPasses(t *testing.T) {
	ac := NewAdmissionControl(1)

	entered := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	// bypassCheck: true для "/img-png/thumb.png" (тот же ассет, что идёт).
	bypass := func(r *http.Request) bool {
		return r.URL.EscapedPath() == "/img-png/thumb.png"
	}
	wrapped := ac.Wrap(next, bypass)

	// Первый запрос занимает единственный слот и блокируется.
	firstDone := make(chan struct{}, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		firstDone <- struct{}{}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not enter handler")
	}

	// Второй запрос — тот же ассет: bypassCheck == true → 200, не 503.
	// Выполняем в горутине: bypass-запрос тоже входит в next (блокируется
	// на release), поэтому не можем вызывать ServeHTTP синхронно.
	bypassDone := make(chan struct{}, 1)
	var bypassCode int
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		bypassCode = rec.Code
		bypassDone <- struct{}{}
	}()

	// Освобождаем слоты: первый запрос и bypass-запрос завершаются.
	release <- struct{}{}
	release <- struct{}{}
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not finish")
	}
	select {
	case <-bypassDone:
	case <-time.After(5 * time.Second):
		t.Fatal("bypass request did not finish")
	}
	if bypassCode != http.StatusOK {
		t.Fatalf("bypass status = %d, want 200", bypassCode)
	}
}

// TestAdmissionControlBypassDifferentAssetRejected проверяет, что при
// переполнении семафора запрос для ДРУГОГО ассета (bypassCheck == false)
// по-прежнему получает 503.
func TestAdmissionControlBypassDifferentAssetRejected(t *testing.T) {
	ac := NewAdmissionControl(1)

	entered := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	// bypassCheck: true только для "/img-png/thumb.png".
	bypass := func(r *http.Request) bool {
		return r.URL.EscapedPath() == "/img-png/thumb.png"
	}
	wrapped := ac.Wrap(next, bypass)

	// Первый запрос занимает единственный слот и блокируется.
	firstDone := make(chan struct{}, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		firstDone <- struct{}{}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not enter handler")
	}

	// Второй запрос — ДРУГОЙ ассет: bypassCheck == false → 503.
	req := httptest.NewRequest(http.MethodGet, "/other-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	// Освобождаем слот, чтобы первый запрос завершился.
	release <- struct{}{}
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not finish")
	}
}
