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
		req, err := Parse("/v1/photos/photo-1-" + src + "/thumb.webp")
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

// TestPresetResolveDPRFromURL проверяет, что DPR берётся из URL, а не из
// пресета.
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
		{"/v1/photos/photo-1-jpg/thumb.webp", DefaultDPR},
		{"/v1/photos/photo-1-jpg/thumb@2.webp", 2},
		{"/v1/photos/photo-1-jpg/thumb@3.webp", 3},
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
	req, err := Parse("/v1/photos/photo-1-jpg/thumb.png")
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
	req, err := Parse("/v1/photos/photo-1-jpg/c-120x80@2.webp")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := set.Resolve(req); err == nil {
		t.Fatal("expected error for non-preset request")
	}
}

// mustNewPreset — вспомогательный конструктор для тестов.
func mustNewPreset(t *testing.T, name string, tr Transform, size string, outFmt string) *Preset {
	t.Helper()
	sizeT, err := ParseSize(size)
	if err != nil {
		t.Fatalf("ParseSize(%q): %v", size, err)
	}
	f, err := NewFormat(outFmt)
	if err != nil {
		t.Fatalf("NewFormat(%q): %v", outFmt, err)
	}
	p, err := NewPreset(name, tr, sizeT, f)
	if err != nil {
		t.Fatalf("NewPreset: %v", err)
	}
	return p
}
