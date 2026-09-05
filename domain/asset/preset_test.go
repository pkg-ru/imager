package asset

import (
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/processing"
)

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
	if req.SegmentName().String() != "thumb" {
		t.Fatalf("SegmentName = %q, want thumb", req.SegmentName())
	}
	resolved, err := set.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolved.DPR().Int(); got != 2 {
		t.Errorf("Resolve DPR = %d, want 2", got)
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
	req, err := Parse("/photos/photo-1-jpg/thumb@3.webp")
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
	p, err := NewPreset("thumb@2", TransformCrop, mustSize(t, "240x160"), []Format{mustFormat(t, "webp")}, DPR(3), true, 0, 0, 0, nil)
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
	p, err := NewPreset("thumb", TransformCrop, mustSize(t, "120x80"), []Format{mustFormat(t, "webp")}, 0, false, 80, 10, 5000, &loop)
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

// TestPresetResolveOutputFormatList проверяет, что output-formats — список
// допустимых форматов: URL формат должен входить в список.
func TestPresetResolveOutputFormatList(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPresetFormats(t, "thumb", TransformCrop, "120x80", "webp", "avif"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	// Форматы из списка — допустимы.
	for _, out := range []string{"webp", "avif"} {
		req, err := Parse("/photos/photo-1-jpg/thumb." + out)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if _, err := set.Resolve(req); err != nil {
			t.Errorf("Resolve(%s): unexpected error: %v", out, err)
		}
	}
	// Формат вне списка — ошибка.
	req, err := Parse("/photos/photo-1-jpg/thumb.png")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := set.Resolve(req); err == nil {
		t.Fatal("expected output format not allowed error")
	}
}

// TestPresetResolveNonPresetRejected проверяет, что Resolve отклоняет
// канонический (не segment) запрос.
func TestPresetResolveNonPresetRejected(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp"),
	})
	if err != nil {
		t.Fatalf("NewPresetSet: %v", err)
	}
	req, err := NewRequest("photos", mustSourceName(t, "photo-1"), mustFormat(t, "jpg"), TransformCrop, mustSize(t, "120x80"), DPR(2), mustFormat(t, "webp"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := set.Resolve(req); err == nil {
		t.Fatal("expected error for non-segment request")
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
		{"200x100@2", "200x100", 2, true},
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
	return mustNewPresetFormats(t, name, tr, size, outFmt)
}

// mustNewPresetFormats — конструктор с несколькими выходными форматами.
func mustNewPresetFormats(t *testing.T, name string, tr Transform, size string, outFmts ...string) *Preset {
	t.Helper()
	formats := make([]Format, 0, len(outFmts))
	for _, f := range outFmts {
		formats = append(formats, mustFormat(t, f))
	}
	p, err := NewPreset(name, tr, mustSize(t, size), formats, 0, false, 0, 0, 0, nil)
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

func mustSourceName(t *testing.T, name string) SourceName {
	t.Helper()
	n, err := NewSourceName(name)
	if err != nil {
		t.Fatalf("NewSourceName(%q): %v", name, err)
	}
	return n
}

// TestPresetWithOrientation проверяет, что WithOrientation сохраняет
// спецификацию ориентации в пресете.
func TestPresetWithOrientation(t *testing.T) {
	p := mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp")
	if p.Orientation() != nil {
		t.Fatalf("Orientation() = %v, want nil", p.Orientation())
	}
	or := &processing.OrientationSpec{AutoOrient: false, Rotate: processing.Rotation90, Flip: processing.FlipVertical}
	p2 := p.WithOrientation(or)
	if p2 == p {
		t.Fatal("WithOrientation must return a new preset")
	}
	if p2.Orientation() != or {
		t.Errorf("Orientation() = %v, want %v", p2.Orientation(), or)
	}
	// Исходный пресет не изменён.
	if p.Orientation() != nil {
		t.Errorf("original preset Orientation() = %v, want nil", p.Orientation())
	}
}

// TestPresetResolveOrientation проверяет, что ориентация пресета переносится
// в канонический запрос через Resolve.
func TestPresetResolveOrientation(t *testing.T) {
	or := &processing.OrientationSpec{AutoOrient: true, Rotate: processing.Rotation180, Flip: processing.FlipHorizontal}
	p := mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp").WithOrientation(or)
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
	if got := resolved.Orientation(); got != or {
		t.Errorf("resolved Orientation() = %v, want %v", got, or)
	}
}

// TestPresetResolveNoOrientation проверяет, что пресет без ориентации не
// проставляет её в запрос (nil).
func TestPresetResolveNoOrientation(t *testing.T) {
	set, err := NewPresetSet([]*Preset{
		mustNewPreset(t, "thumb", TransformCrop, "120x80", "webp"),
	})
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
	if got := resolved.Orientation(); got != nil {
		t.Errorf("resolved Orientation() = %v, want nil", got)
	}
}

// TestPresetDPRSet проверяет различение «dpr не задан» от «dpr: 0/1».
func TestPresetDPRSet(t *testing.T) {
	// dpr не задан: DPRSet=false, DPR=0.
	p, err := NewPreset("thumb", TransformCrop, mustSize(t, "120x80"), []Format{mustFormat(t, "webp")}, 0, false, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("NewPreset: %v", err)
	}
	if p.DPRSet() || p.DPR() != 0 {
		t.Errorf("expected dpr not set, got DPRSet=%v DPR=%d", p.DPRSet(), p.DPR())
	}
	// dpr: 0 задан явно: DPRSet=true, DPR=0.
	p2, err := NewPreset("thumb", TransformCrop, mustSize(t, "120x80"), []Format{mustFormat(t, "webp")}, 0, true, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("NewPreset: %v", err)
	}
	if !p2.DPRSet() || p2.DPR() != 0 {
		t.Errorf("expected dpr set to 0, got DPRSet=%v DPR=%d", p2.DPRSet(), p2.DPR())
	}
	// dpr: 2 задан явно.
	p3, err := NewPreset("thumb", TransformCrop, mustSize(t, "120x80"), []Format{mustFormat(t, "webp")}, 2, true, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("NewPreset: %v", err)
	}
	if !p3.DPRSet() || p3.DPR() != 2 {
		t.Errorf("expected dpr set to 2, got DPRSet=%v DPR=%d", p3.DPRSet(), p3.DPR())
	}
}
