package asset

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseSegment(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string // ожидаемый канонический URL (без /)
	}{
		{
			name: "preset",
			url:  "/photos/photo-1-jpg/banner.webp",
			want: "photos/photo-1-jpg/banner.webp",
		},
		{
			name: "preset with dpr",
			url:  "/photos/photo-1-jpg/banner@2.webp",
			want: "photos/photo-1-jpg/banner@2.webp",
		},
		{
			name: "preset name with dpr suffix",
			url:  "/photos/photo-1-jpg/banner@2.webp",
			want: "photos/photo-1-jpg/banner@2.webp",
		},
		{
			name: "custom exact size",
			url:  "/banners/photo-jpg/200x200.webp",
			want: "banners/photo-jpg/200x200.webp",
		},
		{
			name: "custom size with dpr",
			url:  "/banners/photo-jpg/200x100@2.webp",
			want: "banners/photo-jpg/200x100@2.webp",
		},
		{
			name: "custom width only",
			url:  "/banners/photo-jpg/200x.webp",
			want: "banners/photo-jpg/200x.webp",
		},
		{
			name: "custom height only",
			url:  "/banners/photo-jpg/x200.webp",
			want: "banners/photo-jpg/x200.webp",
		},
		{
			name: "custom original",
			url:  "/banners/photo-jpg/x.webp",
			want: "banners/photo-jpg/x.webp",
		},
		{
			name: "nested path with dashes in source name",
			url:  "/a/b/c/my-photo-2-png/200x200@3.gif",
			want: "a/b/c/my-photo-2-png/200x200@3.gif",
		},
		{
			name: "dpr 3",
			url:  "/name-gif/banner@3.jpg",
			want: "name-gif/banner@3.jpg",
		},
		{
			name: "deep path fallback",
			url:  "/fig-ego-znaet/chto-za-papka/bugoga-gif/x.webp",
			want: "fig-ego-znaet/chto-za-papka/bugoga-gif/x.webp",
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

func TestParseSegmentFields(t *testing.T) {
	req, err := Parse("/photos/photo-1-jpg/banner.webp")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !req.IsPreset() {
		t.Fatal("expected segment request")
	}
	if req.SegmentName().String() != "banner" {
		t.Errorf("SegmentName = %q, want banner", req.SegmentName())
	}
	if req.SourceFormat().String() != "jpg" {
		t.Errorf("SourceFormat = %q, want jpg (from url)", req.SourceFormat())
	}
	if req.DPR().Int() != 0 {
		t.Errorf("DPR = %d, want 0 (no @dpr in url)", req.DPR().Int())
	}
	got, err := req.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if want := "photos/photo-1-jpg/banner.webp"; got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
}

// TestParseSegmentDPRURL проверяет, что @dpr-суффикс URL отделяется от имени
// сегмента: последний "@" — dpr URL, имя — всё до него.
func TestParseSegmentDPRURL(t *testing.T) {
	req, err := Parse("/photos/photo-1-jpg/banner@2.webp")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if req.SegmentName().String() != "banner" {
		t.Errorf("SegmentName = %q, want banner", req.SegmentName())
	}
	if req.DPR().Int() != 2 {
		t.Errorf("DPR = %d, want 2 (url dpr)", req.DPR().Int())
	}
	got, err := req.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if want := "photos/photo-1-jpg/banner@2.webp"; got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
}

// TestParseSegmentNameWithDPRSuffix проверяет, что @dpr-суффикс в URL
// отделяется от имени сегмента: последний "@" — всегда dpr URL, имя — всё
// до него. Парсер не знает, пресет это или custom: имя "banner@2" в URL
// разбирается как segment "banner" + dprURL 2; точное имя "banner@2"
// ищется на этапе Resolve (policy).
func TestParseSegmentNameWithDPRSuffix(t *testing.T) {
	req, err := Parse("/photos/photo-1-jpg/banner@2.webp")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if req.SegmentName().String() != "banner" {
		t.Errorf("SegmentName = %q, want banner (dpr separated)", req.SegmentName())
	}
	if req.DPR().Int() != 2 {
		t.Errorf("DPR = %d, want 2", req.DPR().Int())
	}
}

func TestParseInvalid(t *testing.T) {
	invalid := []string{
		"",
		"/",                                                    // empty
		"/photos/photo-1-jpg/banner",                           // missing output format
		"/photos/photo-1-jpg/banner.",                          // empty output format
		"/photos/photo-1-jpg/banner@2",                         // missing output format
		"/photos/photo-1-jpg/banner@2.",                        // empty output format
		"/photos/photo-1-jpg/banner@0.webp",                    // explicit @0
		"/photos/photo-1-jpg/banner@1.webp",                    // explicit @1
		"/photos/photo-1-jpg/banner@4.webp",                    // dpr > MaxDPR
		"/photos/photo-1-jpg/banner@-1.webp",                   // negative dpr
		"/photos/photo-1-jpg/banner@x.webp",                    // non-numeric dpr
		"/photos/photo-1-jpg/banner@.webp",                     // empty dpr
		"/photos/photo-1-jpg/banner@2@3.webp",                  // multiple '@' in segment
		"/photos/photo-1-jpg/@2.webp",                          // empty segment
		"/photos/photo-1-jpg/.webp",                            // empty segment
		"/photos/photo-1-jpg/t-x50@3.png",                      // transform code
		"/photos/photo-1-jpg/ct-180x@2.avif",                   // transform code
		"/photos/photo-1-jpg/sc-120x80@2.webp",                 // transform code
		"/photos/photo-1-jpg/fc-120x80@2.webp",                 // transform code
		"/photos/photo-1-jpg/oc-120x80@2.webp",                 // transform code
		"/photos/photo-1-jpg/sct-120x80@2.webp",                // transform code
		"/photos/photo-1-jpg/fct-120x80@2.webp",                // transform code
		"/photos/photo-1-jpg/oct-120x80@2.webp",                // transform code
		"/photos/photo-1-jpg/tc-120x80@2.webp",                 // invalid transform
		"/photos/photo-1-jpg/crop-120x80@2.webp",               // word "crop"
		"/photos/photo-1-jpg/trim-120x80@2.webp",               // word "trim"
		"/photos/photo-1-jpg/foo-120x80@2.webp",                // unknown
		"/photos/photo-1-jpg/c-@2.webp",                        // empty size
		"/photos/photo-1-jpg/120x80@1.webp",                    // explicit @1
		"/photos/photo-1-jpg/120x80@0.webp",                    // explicit @0
		"/photos/photo-1-jpg/120x80@-1.webp",                   // negative dpr
		"/photos/photo-1-jpg/120x80@4.webp",                    // dpr > MaxDPR
		"/photos/photo-1-jpg/c-99999999999999999999x80@2.webp", // dimension overflow
		"/photos/photo-1-jpg/thumb@0.webp",                     // segment name dpr @0 недопустим
		"/photos/photo-1-jpg/thumb@4.webp",                     // segment name dpr @4 недопустим
		"/photos/photo-1-jpg/thumb@x.webp",                     // segment name dpr нечисловой
		"/photos/photo-1-jpg/thumb@.webp",                      // segment name dpr пустой
		"/photos/photo-1-jpg/banner@2.webp/..",                 // traversal
		"/photos/../photo-1-jpg/banner@2.webp",                 // traversal
		"/photos/photo-1-jpg/banner@2%2fwebp",                  // encoded separator
		"/photos/photo-1-jpg/banner@2.webp\x00",                // control char
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
		"/../../etc/passwd-jpg/banner@2.webp",
		"/a/../b/photo-jpg/banner@2.webp",
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected traversal error", u)
		}
	}
}

func TestParseRejectsEncodedSeparator(t *testing.T) {
	urls := []string{
		"/a%2fb/photo-jpg/banner@2.webp",
		"/a%2Fb/photo-jpg/banner@2.webp",
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected encoded separator error", u)
		}
	}
}

func TestParseRejectsControlChars(t *testing.T) {
	urls := []string{
		"/a\x01b/photo-jpg/banner@2.webp",
		"/photo-jpg/banner@2.webp\x7f",
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected control char error", u)
		}
	}
}

func TestParseRejectsInvalidChars(t *testing.T) {
	// Пробел разрешён в имени исходника (см. NewSourceName); проверяется
	// реально недопустимый control-символ.
	urls := []string{
		"/photos/photo\x01-jpg/banner@2.webp", // control char in source name
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected invalid char error", u)
		}
	}
}

// TestParseAcceptsUnicodeSourceNames проверяет, что имя исходника может
// содержать любые Unicode-символы: кириллицу, CJK, пробелы и т.д.
func TestParseAcceptsUnicodeSourceNames(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string // ожидаемый канонический URL (без /)
	}{
		{
			name: "cyrillic source name",
			url:  "/photos/изображение-png/banner@2.webp",
			want: "photos/изображение-png/banner@2.webp",
		},
		{
			name: "chinese source name",
			url:  "/photos/图片-jpg/banner@2.webp",
			want: "photos/图片-jpg/banner@2.webp",
		},
		{
			name: "source name with space",
			url:  "/photos/my photo-png/banner@2.webp",
			want: "photos/my photo-png/banner@2.webp",
		},
	}
	for _, tt := range cases {
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

// TestParseRejectsUnsafeSourceNames проверяет, что опасные имена исходников
// отклоняются: traversal, разделители пути, control-символы.
func TestParseRejectsUnsafeSourceNames(t *testing.T) {
	urls := []string{
		"/../etc/passwd-jpg/banner@2.webp",     // path traversal
		"/photos/a\\b.png-jpg/banner@2.webp",   // '\' в имени исходника
		"/photos/a\x00b-jpg/banner@2.webp",     // нулевой байт
		"/photos/a\x1f-jpg/banner@2.webp",      // управляющий символ
		"/photos/photo..old-jpg/banner@2.webp", // ".." внутри имени
	}
	for _, u := range urls {
		if _, err := Parse(u); err == nil {
			t.Errorf("Parse(%q) expected unsafe source name error", u)
		}
	}
}

// TestNewSourceNameValidation проверяет валидацию имени исходника напрямую:
// разрешены любые Unicode-символы и пробелы, запрещены разделители пути,
// traversal, нулевой байт, управляющие символы, пустое и слишком длинное имя.
func TestNewSourceNameValidation(t *testing.T) {
	valid := []string{
		"изображение.png",
		"图片.jpg",
		"my photo.png",
		"photo-1_v2.tar.gz",
		"emoji😀name",
		"a",
	}
	for _, s := range valid {
		if _, err := NewSourceName(s); err != nil {
			t.Errorf("NewSourceName(%q) unexpected error: %v", s, err)
		}
	}

	invalid := []string{
		"",                                      // пустое имя
		"../etc/passwd",                         // path traversal
		"a/b.png",                               // '/' — разделитель пути
		"a\\b.png",                              // '\' — разделитель пути
		"a\x00b.png",                            // нулевой байт
		"a\x01b.png",                            // управляющий символ
		"a\x7fb.png",                            // DEL
		strings.Repeat("ф", MaxSourceNameLen+1), // слишком длинное имя
	}
	for _, s := range invalid {
		if _, err := NewSourceName(s); err == nil {
			t.Errorf("NewSourceName(%q) expected error, got nil", s)
		}
	}
}

func TestParseRejectsTooLong(t *testing.T) {
	long := "/" + strings.Repeat("a", MaxURLLen) + "-jpg/banner@2.webp"
	if _, err := Parse(long); err == nil {
		t.Error("expected length error")
	}
}

func TestParseSegmentWithNameContainingX(t *testing.T) {
	// Имя сегмента со строчной "x" (например "max") не должно разбираться
	// как размер — это просто имя.
	req, err := Parse("/photos/photo-1-jpg/max.webp")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !req.IsPreset() {
		t.Fatal("expected segment request for name containing 'x'")
	}
	if req.SegmentName().String() != "max" {
		t.Errorf("SegmentName = %q, want max", req.SegmentName().String())
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

func TestParseDPRSuffix(t *testing.T) {
	// Отсутствие суффикса означает DPR=0 (не задан).
	req, err := Parse("/photos/photo-1-jpg/banner.webp")
	if err != nil {
		t.Fatalf("Parse without dpr: %v", err)
	}
	if req.DPR().Int() != 0 {
		t.Errorf("DPR = %d, want 0 (not set)", req.DPR().Int())
	}

	// Явные @2 и @3 допустимы.
	for _, ok := range []int{2, 3} {
		url := "/photos/photo-1-jpg/banner@" + strconv.Itoa(ok) + ".webp"
		req, err := Parse(url)
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", url, err)
			continue
		}
		if req.DPR().Int() != ok {
			t.Errorf("Parse(%q) DPR = %d, want %d", url, req.DPR().Int(), ok)
		}
	}

	// Явные @0, @1, @4, @5, @-1 отклоняются парсером.
	for _, bad := range []string{"0", "1", "4", "5", "-1"} {
		url := "/photos/photo-1-jpg/banner@" + bad + ".webp"
		if _, err := Parse(url); err == nil {
			t.Errorf("Parse(%q) expected error", url)
		}
	}
}
