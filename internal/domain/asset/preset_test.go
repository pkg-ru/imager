package asset

import "testing"

// TestPresetResolveDifferentSourceFormats проверяет, что один пресет работает
// с разными source formats из URL (source format не хранится в пресете).
func TestPresetResolveDifferentSourceFormats(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	for _, src := range []string{"jpg", "png", "webp"} {
		req, err := Parse("/photos/photo-1-" + src + "/thumb.webp")
		if err != nil {
			t.Fatalf("Parse(%s): %v", src, err)
		}
		resolved, err := set.Resolve(req)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", src, err)
		}
		if resolved.IsPreset() {
			t.Fatalf("Resolve(%s): resolved request must be canonical", src)
		}
		if got := resolved.SourceFormat(); got.String() != src {
			t.Errorf("Resolve(%s) source format = %q, want %q", src, got, src)
		}
		if got := resolved.SourceName().String(); got != "photo-1" {
			t.Errorf("Resolve(%s) source name = %q, want photo-1", src, got)
		}
	}
}

// TestPresetResolveDPRFromURL проверяет, что DPR берётся из URL, когда пресет
// не имеет фиксированного dpr. URL "thumb@2.webp" разбивается на base "thumb"
// + @dpr 2 (fallback на base-пресет).
func TestPresetResolveDPRFromURL(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"/photos/photo-1-jpg/thumb.webp", DefaultDPR},
		{"/photos/photo-1-jpg/thumb@2.webp", 2},
		{"/photos/photo-1-jpg/thumb@3.webp", 3},
	} {
		req, err := Parse(tc.url)
		if err != nil {
			t.Fatalf("Parse(%s): %v", tc.url, err)
		}
		resolved, err := set.Resolve(req)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", tc.url, err)
		}
		if got := resolved.DPR().Int(); got != tc.want {
			t.Errorf("Resolve(%s) DPR = %d, want %d", tc.url, got, tc.want)
		}
	}
}

// TestPresetResolveDPRInName проверяет, что пресет с @dpr в имени применяет
// фиксированный dpr.
func TestPresetResolveDPRInName(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb@2", TransformCrop, "240x160", "webp"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	// URL без @dpr: dpr берётся из имени пресета.
	req, err := Parse("/photos/photo-1-jpg/thumb@2.webp")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if req.PresetName().String() != "thumb@2" {
		t.Fatalf("PresetName = %q, want thumb@2", req.PresetName())
	}
	resolved, err := set.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolved.DPR().Int(); got != 2 {
		t.Errorf("Resolve DPR = %d, want 2", got)
	}
	// URL с тем же @dpr — допустимо.
	req2, err := Parse("/photos/photo-1-jpg/thumb@2@2.webp")
	if err != nil {
		t.Fatalf("Parse(thumb@2@2): %v", err)
	}
	if req2.PresetName().String() != "thumb@2" {
		t.Fatalf("PresetName = %q, want thumb@2", req2.PresetName())
	}
	if got := req2.DPR().Int(); got != 2 {
		t.Fatalf("URL DPR = %d, want 2", got)
	}
	resolved2, err := set.Resolve(req2)
	if err != nil {
		t.Fatalf("Resolve(thumb@2@2): %v", err)
	}
	if got := resolved2.DPR().Int(); got != 2 {
		t.Errorf("Resolve(thumb@2@2) DPR = %d, want 2", got)
	}
}

// TestPresetResolveDPRConflict проверяет, что явный @dpr в URL, отличный от
// фиксированного dpr пресета, — ошибка разрешения.
func TestPresetResolveDPRConflict(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb@2", TransformCrop, "240x160", "webp"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	req, err := Parse("/photos/photo-1-jpg/thumb@2@3.webp")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := set.Resolve(req); err == nil {
		t.Fatal("expected dpr conflict error")
	}
}

// TestPresetResolveDPRFieldPriority проверяет, что поле dpr в настройках
// пресета имеет приоритет над @dpr в имени.
func TestPresetResolveDPRFieldPriority(t *testing.T) {
	// Пресет "thumb@2" с полем dpr=3: применяется dpr=3.
	p, err := NewPreset("thumb@2", TransformCrop, mustSize(t, "240x160"), mustFormat(t, "webp"), DPR(3), 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("NewPreset: %v", err)
	}
	set, err := NewPresetSet([]*Preset{p})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	req, err := Parse("/photos/photo-1-jpg/thumb@2.webp")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved, err := set.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolved.DPR().Int(); got != 3 {
		t.Errorf("Resolve DPR = %d, want 3 (field priority)", got)
	}
}

// TestPresetResolveProcessingOptions проверяет, что параметры обработки
// пресета пробрасываются в результирующий запрос.
func TestPresetResolveProcessingOptions(t *testing.T) {
	loop := true
	p, err := NewPreset("thumb", TransformCrop, mustSize(t, "120x80"), mustFormat(t, "webp"), 0, 80, 10, 5000, &loop)
	if err != nil {
		t.Fatalf("NewPreset: %v", err)
	}
	set, err := NewPresetSet([]*Preset{p})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	req, err := Parse("/photos/photo-1-jpg/thumb.webp")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved, err := set.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolved.Quality(); got != 80 {
		t.Errorf("Quality = %d, want 80", got)
	}
	if got := resolved.Frames(); got != 10 {
		t.Errorf("Frames = %d, want 10", got)
	}
	if got := resolved.Duration(); got != 5000 {
		t.Errorf("Duration = %d, want 5000", got)
	}
	if resolved.Loop() == nil || !*resolved.Loop() {
		t.Errorf("Loop = %v, want true", resolved.Loop())
	}
}

// TestPresetResolveOutputFormatMismatch проверяет, что URL output format,
// отличающийся от preset output format, отклоняется при разрешении.
func TestPresetResolveOutputFormatMismatch(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	// preset output format = webp, URL использует png → ошибка.
	req, err := Parse("/photos/photo-1-jpg/thumb.png")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := set.Resolve(req); err == nil {
		t.Fatal("expected output format mismatch error")
	}
}

// TestPresetResolveNonPresetRejected проверяет, что Resolve отклоняет
// канонический (не preset) запрос.
func TestPresetResolveNonPresetRejected(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	req, err := Parse("/photos/photo-1-jpg/c-120x80@2.webp")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := set.Resolve(req); err == nil {
		t.Fatal("expected error for non-preset request")
	}
}

// TestSplitPresetNameDPR проверяет отделение @dpr-суффикса от имени пресета.
func TestSplitPresetNameDPR(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantDPR int
		ok      bool
	}{
		{"thumb", "thumb", 0, true},
		{"thumb@1", "thumb", 1, true}, // @1 эквивалентен отсутствию
		{"thumb@2", "thumb", 2, true},
		{"thumb@3", "thumb", 3, true},
		{"thumb@0", "", 0, false}, // @0 недопустим
		{"thumb@4", "", 0, false}, // @4 вне диапазона
		{"thumb@x", "", 0, false}, // нечисловой
		{"thumb@", "", 0, false},  // пустой суффикс
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, dpr, err := SplitPresetNameDPR(tt.in)
			if tt.ok {
				if err != nil {
					t.Fatalf("SplitPresetNameDPR(%q) error: %v", tt.in, err)
				}
				if got != tt.want {
					t.Errorf("SplitPresetNameDPR(%q) name = %q, want %q", tt.in, got, tt.want)
				}
				if dpr.Int() != tt.wantDPR {
					t.Errorf("SplitPresetNameDPR(%q) dpr = %d, want %d", tt.in, dpr.Int(), tt.wantDPR)
				}
			} else if err == nil {
				t.Errorf("SplitPresetNameDPR(%q) expected error", tt.in)
			}
		})
	}
}

// mustNewPreset — вспомогательный конструктор для тестов.
func mustNewPreset(t *testing.T, name string, tr Transform, size string, outFmt string) *Preset {
	t.Helper()
	p, err := NewPreset(name, tr, mustSize(t, size), mustFormat(t, outFmt), 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("NewPreset: %v", err)
	}
	return p
}

func mustSize(t *testing.T, size string) Size {
	t.Helper()
	sizeT, err := ParseSize(size)
	if err != nil {
		t.Fatalf("ParseSize(%q): %v", size, err)
	}
	return sizeT
}

func mustFormat(t *testing.T, outFmt string) Format {
	t.Helper()
	f, err := NewFormat(outFmt)
	if err != nil {
		t.Fatalf("NewFormat(%q): %v", outFmt, err)
	}
	return f
}
