package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/asset"
)

// fakeRecorder — управляемый fake PolicyRecorder: запоминает последний req.
type fakeRecorder struct {
	mu  sync.Mutex
	req *asset.Request
}

func (f *fakeRecorder) Observe(req *asset.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.req = req
}

func (f *fakeRecorder) last() *asset.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req
}

// TestHandlerObservesPolicyRecorder проверяет, что после успешного asset.Parse
// handler вызывает PolicyRecorder.Observe с распарсенным запросом.
func TestHandlerObservesPolicyRecorder(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("forbidden/photo-png/120x60.webp", []byte("PNGDATA"), 7)

	rec := &fakeRecorder{}
	cfg := baseConfig()
	cfg.PolicyRecorder = rec
	h := newTestHandler(t, gen, cfg)

	req := httptest.NewRequest(http.MethodGet, "/forbidden/photo-png/120x60.webp", nil)
	recw := httptest.NewRecorder()
	h.ServeHTTP(recw, req)

	if recw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recw.Code)
	}
	got := rec.last()
	if got == nil {
		t.Fatal("PolicyRecorder.Observe was not called")
	}
	if got.Path() != "forbidden" {
		t.Errorf("observed path = %q, want forbidden", got.Path())
	}
	if got.SegmentName().String() != "120x60" {
		t.Errorf("observed segment = %q, want 120x60", got.SegmentName().String())
	}
	if got.OutputFormats().String() != "webp" {
		t.Errorf("observed output format = %q, want webp", got.OutputFormats().String())
	}
}

// TestHandlerNilRecorderNoPanic проверяет, что при PolicyRecorder == nil
// handler работает как раньше (nil-safe).
func TestHandlerNilRecorderNoPanic(t *testing.T) {
	gen := newFakeGenerator()
	gen.addResult("img-png/thumb.png", []byte("PNGDATA"), 7)

	h := newTestHandler(t, gen, baseConfig()) // PolicyRecorder = nil

	req := httptest.NewRequest(http.MethodGet, "/img-png/thumb.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "PNGDATA" {
		t.Errorf("body = %q, want PNGDATA", body)
	}
}
