package asset

import (
	"strings"
	"testing"
)

func TestParseCanonical(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string // ожидаемый канонический URL (без /v1/)
	}{
		{
			name: "crop",
			url:  "/v1/photos/photo-1-jpg/c-120x80@2.webp",
			want: "photos/photo-1-jpg/c-120x80@2.webp",
		},
		{
			name: "trim",
			url:  "/v1/logo-png/t-x50@3.png",
			want: "logo-png/t-x50@3.png",
		},
		{
			name: "crop trim",
			url:  "/v1/img-jpg/ct-180x@2.avif",
			want: "img-jpg/ct-180x@2.avif",
		},
		{
			name: "nested path with dashes in source name",
			url:  "/v1/a/b/c/my-photo-2-png/c-10x10@3.gif",
			want: "a/b/c/my-photo-2-png/c-10x10@3.gif",
		},
		{
			name: "dpr 3",
			url:  "/v1/name-gif/t-220x30@3.jpg",
			want: "name-gif/t-220x30@3.jpg",
		},
		{
			name: "no transform",
			url:  "/v1/photos/photo-1-jpg/120x80.webp",
			want: "photos/photo-1-jpg/120x80.webp",
		},
		{
			name: "original size",
			url:  "/v1/photos/photo-1-jpg/x.webp",
			want: "photos/photo-1-jpg/x.webp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := Parse(tt.url)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.url, err)
			}
			got, err := req.Build()
			if err != nil {
				t.Fatalf("Build() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Build() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePreset(t *testing.T) {
	req, err := Parse("/v1/photos/photo-1-jpg/thumb.webp")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !req.IsPreset() {
		t.Fatal("expected preset request")
	}
	if req.PresetName().String() != "thumb" {
		t.Errorf("PresetName = %q, want thumb", req.PresetName())
	}
	if req.SourceFormat().String() != "jpg" {
		t.Errorf("SourceFormat = %q, want jpg (from url)", req.SourceFormat())
	}
	if req.DPR().Int() != DefaultDPR {
		t.Errorf("DPR = %d, want %d (default)", req.DPR().Int(), DefaultDPR)
	}
	got, err := req.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if want := "photos/photo-1-jpg/thumb.webp"; got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
}

func TestParsePresetWithDPR(t *testing.T) {
	req, err := Parse("/v1/photos/photo-1-jpg/thumb@2.webp")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !req.IsPreset() {
		t.Fatal("expected preset request")
	}
	if req.DPR().Int() != 2 {
		t.Errorf("DPR = %d, want 2", req.DPR().Int())
	}
	got, err := req.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if want := "photos/photo-1-jpg/thumb@2.webp"; got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
}

func TestParseInvalid(t *testing.T) {
	invalid := []string{
		"",
		"photos/photo-1-jpg/c-120x80@2.webp", // missing /v1/
		"/v1/",                               // empty
		"/v1/photos/photo-1-jpg/c-120x80@2",  // missing output format
		"/v1/photos/photo-1-jpg/c-120x80@2.", // empty output format
		// Старая дефисная грамматика канонического URL не поддерживается.
		"/v1/photos/photo-1-jpg-c-120x80@2.webp",
		// Старый дефисный preset не поддерживается.
		"/v1/photos/photo-1-jpg-thumb.webp",
		// invalid transform
		"/v1/photos/photo-1-jpg/tc-120x80@2.webp",                 // tc недопустим
		"/v1/photos/photo-1-jpg/crop-120x80@2.webp",               // слово "crop"
		"/v1/photos/photo-1-jpg/trim-120x80@2.webp",               // слово "trim"
		"/v1/photos/photo-1-jpg/foo-120x80@2.webp",                // неизвестный
		"/v1/photos/photo-1-jpg/c-@2.webp",                        // empty size
		"/v1/photos/photo-1-jpg/c-120x80@1.webp",                  // explicit @1
		"/v1/photos/photo-1-jpg/c-120x80@0.webp",                  // explicit @0
		"/v1/photos/photo-1-jpg/c-120x80@-1.webp",                 // negative dpr
		"/v1/photos/photo-1-jpg/c-120x80@4.webp",                  // dpr > MaxDPR
		"/v1/photos/photo-1-jpg/c-99999999999999999999x80@2.webp", // dimension overflow
		"/v1/photos/photo-1-jpg/thumb@1.webp",                     // preset explicit @1
		"/v1/photos/photo-1-jpg/thumb@0.webp",                     // preset explicit @0
		"/v1/photos/photo-1-jpg/c-120x80@2.webp/..",               // traversal
		"/v1/photos/../photo-1-jpg/c-120x80@2.webp",               // traversal
		"/v1/photos/photo-1-jpg/c-120x80@2%2fwebp",                // encoded separator
		"/v1/photos/photo-1-jpg/c-120x80@2.webp\x00",              // control char
	}
	for _, u := range invalid {
		t.Run(u, func(t *testing.T) {
			if _, err := Parse(u); err == nil {
				t.Errorf("Parse(%q) expected error, got nil", u)
			}
		})
	}
}

func TestParseRejectsTraversal(t *testing.T) {
	urls := []string{
		"/v1/../../etc/passwd-jpg/c-120x80@2.webp",
		"/v1/a/../b/photo-jpg/c-120x80@2.webp",
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected traversal error", u)
		}
	}
}

func TestParseRejectsEncodedSeparator(t *testing.T) {
	urls := []string{
		"/v1/a%2fb/photo-jpg/c-120x80@2.webp",
		"/v1/a%2Fb/photo-jpg/c-120x80@2.webp",
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected encoded separator error", u)
		}
	}
}

func TestParseRejectsControlChars(t *testing.T) {
	urls := []string{
		"/v1/a\x01b/photo-jpg/c-120x80@2.webp",
		"/v1/photo-jpg/c-120x80@2.webp\x7f",
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected control char error", u)
		}
	}
}

func TestParseRejectsInvalidChars(t *testing.T) {
	urls := []string{
		"/v1/photos/photo name-jpg/c-120x80@2.webp", // space in source name
		"/v1/photos/photo!-jpg/c-120x80@2.webp",     // invalid char
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected invalid char error", u)
		}
	}
}

func TestParseRejectsTooLong(t *testing.T) {
	long := "/v1/" + strings.Repeat("a", MaxURLLen) + "-jpg/c-120x80@2.webp"
	if _, err := Parse(long); err == nil {
		t.Error("expected length error")
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"120x80", "120x80"},
		{"x50", "x50"},
		{"180x", "180x"},
		{"x", "x"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			s, err := ParseSize(tt.in)
			if err != nil {
				t.Fatalf("ParseSize(%q) error: %v", tt.in, err)
			}
			if s.String() != tt.want {
				t.Errorf("ParseSize(%q) = %q, want %q", tt.in, s.String(), tt.want)
			}
		})
	}
}

func TestParseSizeInvalid(t *testing.T) {
	invalid := []string{
		"120",
		"abcx80",
		"120xabc",
		"-5x10",
		"120x-5",
		"99999999999999999999x80",
	}
	for _, s := range invalid {
		t.Run(s, func(t *testing.T) {
			if _, err := ParseSize(s); err == nil {
				t.Errorf("ParseSize(%q) expected error", s)
			}
		})
	}
}

func TestParseDimensionOverflow(t *testing.T) {
	if _, err := NewDimension(1 << 30); err == nil {
		t.Error("expected overflow error for dimension > MaxDimension")
	}
	if _, err := NewDimension(-1); err == nil {
		t.Error("expected error for negative dimension")
	}
}

func TestParseDPRRange(t *testing.T) {
	// Допустимы 1 (default), 2 и 3.
	for _, ok := range []int{1, 2, 3} {
		if _, err := NewDPR(ok); err != nil {
			t.Errorf("NewDPR(%d) unexpected error: %v", ok, err)
		}
	}
	for _, bad := range []int{0, 4, 5, -1} {
		if _, err := NewDPR(bad); err == nil {
			t.Errorf("NewDPR(%d) expected error", bad)
		}
	}
}
