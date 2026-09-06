package asset

import (
	"testing"
)

func TestCanonicalIDStable(t *testing.T) {
	// Один и тот же запрос всегда даёт один и тот же CanonicalID.
	url := "/photos/photo-1-jpg/banner@2.webp"
	req1, err := Parse(url)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	req2, err := Parse(url)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	id1, err := req1.CanonicalID()
	if err != nil {
		t.Fatalf("CanonicalID error: %v", err)
	}
	id2, err := req2.CanonicalID()
	if err != nil {
		t.Fatalf("CanonicalID error: %v", err)
	}
	if !id1.Equal(id2) {
		t.Errorf("CanonicalID not stable: %q != %q", id1.Hash(), id2.Hash())
	}
	if id1.Hash() == "" || id1.URL() == "" {
		t.Error("CanonicalID must have non-empty hash and url")
	}
}

func TestCanonicalIDDistinct(t *testing.T) {
	// Разные запросы дают разные CanonicalID.
	urls := []string{
		"/photos/photo-1-jpg/banner@2.webp",
		"/photos/photo-1-jpg/banner@3.webp",
		"/photos/photo-1-jpg/banner.webp",
		"/photos/photo-1-jpg/200x200.webp",
		"/photos/photo-2-jpg/banner@2.webp",
	}
	ids := make(map[string]bool)
	for _, u := range urls {
		req, err := Parse(u)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", u, err)
		}
		id, err := req.CanonicalID()
		if err != nil {
			t.Fatalf("CanonicalID error: %v", err)
		}
		if ids[id.Hash()] {
			t.Errorf("duplicate canonical id for %q", u)
		}
		ids[id.Hash()] = true
	}
}

func TestCanonicalPathNormalization(t *testing.T) {
	c := NewCanonicalizer()
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"photos", "photos"},
		{"/photos/", "photos"},
		{"a//b", "a/b"},
		{"a/./b", "a/b"},
		{"a/b/c", "a/b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := c.CanonicalPath(tt.in)
			if err != nil {
				t.Fatalf("CanonicalPath(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("CanonicalPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCanonicalPathRejectsTraversal(t *testing.T) {
	c := NewCanonicalizer()
	bad := []string{"..", "a/../b", "a/..", "../a"}
	for _, p := range bad {
		if _, err := c.CanonicalPath(p); err == nil {
			t.Errorf("CanonicalPath(%q) expected traversal error", p)
		}
	}
}

func TestCanonicalPathTooLong(t *testing.T) {
	c := NewCanonicalizer()
	long := make([]byte, MaxPathLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := c.CanonicalPath(string(long)); err == nil {
		t.Error("expected length error")
	}
}

// TestBuildSegmentLessURL проверяет, что segment-less (канонический)
// запрос строит пользовательскую форму {size}@{dpr}.{out} (без
// crop-префикса): такая форма разбирается парсером.
func TestBuildSegmentLessURL(t *testing.T) {
	tests := []struct {
		name string
		path string
		size string
		dpr  int
		want string
	}{
		{"default dpr omitted", "photos", "120x80", 1, "photos/photo-1-jpg/120x80.webp"},
		{"dpr 2", "photos", "120x80", 2, "photos/photo-1-jpg/120x80@2.webp"},
		{"dpr 3", "", "x50", 3, "photo-1-jpg/x50@3.webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := NewRequest(tt.path, mustSourceName(t, "photo-1"), mustFormat(t, "jpg"), CropCenter, false, mustSize(t, tt.size), DPR(tt.dpr), mustFormat(t, "webp"))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			got, err := req.Build()
			if err != nil {
				t.Fatalf("Build() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Build() = %q, want %q", got, tt.want)
			}
			// Форма должна быть обратно совместима с парсером.
			if _, err := Parse("/" + got); err != nil {
				t.Errorf("Parse(%q) error: %v (built URL must be parseable)", got, err)
			}
		})
	}
}

// TestBuildSegmentLessEmptySizeError проверяет, что Build() segment-less
// запроса без размера возвращает ошибку.
func TestBuildSegmentLessEmptySizeError(t *testing.T) {
	req, err := NewRequest("", mustSourceName(t, "photo-1"), mustFormat(t, "jpg"), Crop(""), false, mustSize(t, "x"), DefaultDPR, mustFormat(t, "webp"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Имитируем segment-less запрос с пустым size (Build должен отказать).
	broken := *req
	broken.size = Size{}
	if _, err := broken.Build(); err == nil {
		t.Fatal("expected error for empty size in segment-less Build()")
	}
}

func TestCanonicalizeURLIdempotent(t *testing.T) {
	// Parse -> Build -> Parse -> Build должен быть идемпотентным.
	url := "/photos/photo-1-jpg/banner@2.webp"
	req, err := Parse(url)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	built, err := req.Build()
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	req2, err := Parse("/" + built)
	if err != nil {
		t.Fatalf("Parse(rebuild) error: %v", err)
	}
	built2, err := req2.Build()
	if err != nil {
		t.Fatalf("Build2 error: %v", err)
	}
	if built != built2 {
		t.Errorf("canonicalization not idempotent: %q != %q", built, built2)
	}
}
