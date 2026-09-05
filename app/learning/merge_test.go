package learning

import (
	"testing"

	"github.com/pkg-ru/dynamic"
	"github.com/pkg-ru/imager/domain/policy"
)

// fmts — хелпер построения dynamic.StringSlice.
func fmts(fs ...string) dynamic.StringSlice {
	out := make(dynamic.StringSlice, 0, len(fs))
	for _, f := range fs {
		out = append(out, dynamic.String(f))
	}
	return out
}

// customs — хелпер построения map customs.
func customs(pairs ...[2]any) map[string]policy.PresetConfig {
	m := make(map[string]policy.PresetConfig, len(pairs))
	for _, p := range pairs {
		name, _ := p[0].(string)
		cfg, _ := p[1].(policy.PresetConfig)
		m[name] = cfg
	}
	return m
}

// sizeCustom — custom с output-formats.
func sizeCustom(fs ...string) policy.PresetConfig {
	return policy.PresetConfig{OutputFormats: fmts(fs...)}
}

func TestAddObservation(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]policy.PathPolicyConfig
		path    string
		size    string
		format  string
		changed bool
		want    map[string]policy.PathPolicyConfig
	}{
		{
			name:    "новый путь и custom",
			initial: map[string]policy.PathPolicyConfig{},
			path:    "/chto/to/gde/to",
			size:    "120x60",
			format:  "webp",
			changed: true,
			want: map[string]policy.PathPolicyConfig{
				"/chto/to/gde/to": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
			},
		},
		{
			name: "дедуп формата: custom уже содержит формат",
			initial: map[string]policy.PathPolicyConfig{
				"/a/b": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
			},
			path:    "/a/b",
			size:    "120x60",
			format:  "webp",
			changed: false,
			want: map[string]policy.PathPolicyConfig{
				"/a/b": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
			},
		},
		{
			name: "дополнение output-formats",
			initial: map[string]policy.PathPolicyConfig{
				"/a/b": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
			},
			path:    "/a/b",
			size:    "120x60",
			format:  "avif",
			changed: true,
			want: map[string]policy.PathPolicyConfig{
				"/a/b": {Customs: customs([2]any{"120x60", sizeCustom("avif", "webp")})},
			},
		},
		{
			name: "дедуп через предка: у предка тот же custom",
			initial: map[string]policy.PathPolicyConfig{
				"/a": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
			},
			path:    "/a/b/c",
			size:    "120x60",
			format:  "webp",
			changed: false,
			want: map[string]policy.PathPolicyConfig{
				"/a": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
			},
		},
		{
			name: "дедуп через предка: у предка надмножество форматов",
			initial: map[string]policy.PathPolicyConfig{
				"/a": {Customs: customs([2]any{"120x60", sizeCustom("avif", "webp")})},
			},
			path:    "/a/b",
			size:    "120x60",
			format:  "webp",
			changed: false,
			want: map[string]policy.PathPolicyConfig{
				"/a": {Customs: customs([2]any{"120x60", sizeCustom("avif", "webp")})},
			},
		},
		{
			name:    "невалидный размер игнорируется",
			initial: map[string]policy.PathPolicyConfig{},
			path:    "/a/b",
			size:    "banner",
			format:  "webp",
			changed: false,
			want:    map[string]policy.PathPolicyConfig{},
		},
		{
			name:    "пустой формат игнорируется",
			initial: map[string]policy.PathPolicyConfig{},
			path:    "/a/b",
			size:    "120x60",
			format:  "",
			changed: false,
			want:    map[string]policy.PathPolicyConfig{},
		},
		{
			name:    "корень не добавляется",
			initial: map[string]policy.PathPolicyConfig{},
			path:    "/",
			size:    "120x60",
			format:  "webp",
			changed: false,
			want:    map[string]policy.PathPolicyConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := NormalizeState(tt.initial)
			got := AddObservation(state, tt.path, tt.size, tt.format)
			if got != tt.changed {
				t.Errorf("AddObservation changed = %v, want %v", got, tt.changed)
			}
			gotState := NormalizeState(state)
			if len(gotState) != len(tt.want) {
				t.Fatalf("state = %#v, want %#v", gotState, tt.want)
			}
			for path, pp := range tt.want {
				gotPP, ok := gotState[path]
				if !ok {
					t.Fatalf("state missing path %q: %#v", path, gotState)
				}
				if len(gotPP.Customs) != len(pp.Customs) {
					t.Fatalf("path %q customs = %#v, want %#v", path, gotPP.Customs, pp.Customs)
				}
				for name, cfg := range pp.Customs {
					gotCfg, ok := gotPP.Customs[name]
					if !ok {
						t.Fatalf("path %q missing custom %q", path, name)
					}
					if !samePresetConfig(gotCfg, cfg) {
						t.Errorf("path %q custom %q = %#v, want %#v", path, name, gotCfg, cfg)
					}
				}
			}
		})
	}
}

func TestHoistIdenticalCustomsBrothers(t *testing.T) {
	// Пример из ТЗ: /chto/to/gde/to + /chto/to/gde/tut с одинаковыми
	// customs → общий предок /chto/to/gde, промежуточные пути удалены.
	state := map[string]policy.PathPolicyConfig{
		"/chto/to/gde/to":  {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
		"/chto/to/gde/tut": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
	}
	changed := HoistIdenticalCustoms(state)
	if !changed {
		t.Fatal("expected changed = true")
	}
	got := NormalizeState(state)
	want := map[string]policy.PathPolicyConfig{
		"/chto/to/gde": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
	}
	if len(got) != len(want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
	for path, pp := range want {
		gotPP, ok := got[path]
		if !ok {
			t.Fatalf("state missing path %q: %#v", path, got)
		}
		for name, cfg := range pp.Customs {
			if !samePresetConfig(gotPP.Customs[name], cfg) {
				t.Errorf("path %q custom %q mismatch", path, name)
			}
		}
	}
}

func TestHoistThreeLevels(t *testing.T) {
	// Три уровня: /a/b/c1, /a/b/c2 → /a/b; затем /a/b + /a/d → /a.
	state := map[string]policy.PathPolicyConfig{
		"/a/b/c1": {Customs: customs([2]any{"100x100", sizeCustom("webp")})},
		"/a/b/c2": {Customs: customs([2]any{"100x100", sizeCustom("webp")})},
		"/a/d":    {Customs: customs([2]any{"100x100", sizeCustom("webp")})},
	}
	HoistIdenticalCustoms(state)
	got := NormalizeState(state)
	if len(got) != 1 {
		t.Fatalf("state = %#v, want only /a", got)
	}
	pp, ok := got["/a"]
	if !ok {
		t.Fatalf("state missing /a: %#v", got)
	}
	if len(pp.Customs) != 1 {
		t.Fatalf("/a customs = %#v", pp.Customs)
	}
	if !samePresetConfig(pp.Customs["100x100"], sizeCustom("webp")) {
		t.Errorf("/a custom 100x100 = %#v", pp.Customs["100x100"])
	}
}

func TestHoistPartialCustomsNotHoisted(t *testing.T) {
	// Частично совпадающие customs не поднимаются: у братьев общий
	// custom 120x60, но у одного есть ещё 200x200 — 120x60 поднимается,
	// 200x200 остаётся.
	state := map[string]policy.PathPolicyConfig{
		"/a/b": {Customs: customs(
			[2]any{"120x60", sizeCustom("webp")},
			[2]any{"200x200", sizeCustom("avif")},
		)},
		"/a/c": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
	}
	HoistIdenticalCustoms(state)
	got := NormalizeState(state)
	if len(got) != 2 {
		t.Fatalf("state = %#v, want 2 paths", got)
	}
	a, ok := got["/a"]
	if !ok {
		t.Fatalf("state missing /a: %#v", got)
	}
	if len(a.Customs) != 1 || !samePresetConfig(a.Customs["120x60"], sizeCustom("webp")) {
		t.Errorf("/a customs = %#v, want only 120x60", a.Customs)
	}
	b, ok := got["/a/b"]
	if !ok {
		t.Fatalf("state missing /a/b: %#v", got)
	}
	if len(b.Customs) != 1 || !samePresetConfig(b.Customs["200x200"], sizeCustom("avif")) {
		t.Errorf("/a/b customs = %#v, want only 200x200", b.Customs)
	}
}

func TestHoistPathWithPresetsNotRemoved(t *testing.T) {
	// Путь с непустым presets не удаляется, но customs поднимаются.
	state := map[string]policy.PathPolicyConfig{
		"/a/b": {
			Presets: dynamic.StringSlice{dynamic.String("thumb")},
			Customs: customs([2]any{"120x60", sizeCustom("webp")}),
		},
		"/a/c": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
	}
	HoistIdenticalCustoms(state)
	got := NormalizeState(state)
	if len(got) != 2 {
		t.Fatalf("state = %#v, want 2 paths", got)
	}
	b, ok := got["/a/b"]
	if !ok {
		t.Fatalf("state missing /a/b (presets path must stay): %#v", got)
	}
	if len(b.Customs) != 0 {
		t.Errorf("/a/b customs = %#v, want empty", b.Customs)
	}
	if len(b.Presets) != 1 {
		t.Errorf("/a/b presets = %#v, want [thumb]", b.Presets)
	}
	a, ok := got["/a"]
	if !ok {
		t.Fatalf("state missing /a: %#v", got)
	}
	if !samePresetConfig(a.Customs["120x60"], sizeCustom("webp")) {
		t.Errorf("/a customs = %#v", a.Customs)
	}
}

func TestHoistAncestorBlocksAdd(t *testing.T) {
	// Идентичный custom у предка блокирует добавление (через AddObservation).
	state := map[string]policy.PathPolicyConfig{
		"/a": {Customs: customs([2]any{"120x60", sizeCustom("webp")})},
	}
	if AddObservation(state, "/a/b/c", "120x60", "webp") {
		t.Error("expected no change: ancestor already has identical custom")
	}
	if len(state) != 1 {
		t.Fatalf("state = %#v, want only /a", state)
	}
}

func TestHoistFixpoint(t *testing.T) {
	// Фикспойнт: hoist открывает новые возможности слияния.
	// /a/b/x + /a/b/y → /a/b; затем /a/b + /a/c → /a.
	state := map[string]policy.PathPolicyConfig{
		"/a/b/x": {Customs: customs([2]any{"50x50", sizeCustom("webp")})},
		"/a/b/y": {Customs: customs([2]any{"50x50", sizeCustom("webp")})},
		"/a/c":   {Customs: customs([2]any{"50x50", sizeCustom("webp")})},
	}
	HoistIdenticalCustoms(state)
	got := NormalizeState(state)
	if len(got) != 1 {
		t.Fatalf("state = %#v, want only /a", got)
	}
	pp, ok := got["/a"]
	if !ok {
		t.Fatalf("state missing /a: %#v", got)
	}
	if !samePresetConfig(pp.Customs["50x50"], sizeCustom("webp")) {
		t.Errorf("/a customs = %#v", pp.Customs)
	}
}

func TestHoistAncestorDescendantMerge(t *testing.T) {
	// A == P или A == Q: предок/потомок с идентичным custom → сливается
	// в предка, потомок удаляется.
	state := map[string]policy.PathPolicyConfig{
		"/a":   {Customs: customs([2]any{"80x80", sizeCustom("webp")})},
		"/a/b": {Customs: customs([2]any{"80x80", sizeCustom("webp")})},
	}
	HoistIdenticalCustoms(state)
	got := NormalizeState(state)
	if len(got) != 1 {
		t.Fatalf("state = %#v, want only /a", got)
	}
	pp, ok := got["/a"]
	if !ok {
		t.Fatalf("state missing /a: %#v", got)
	}
	if !samePresetConfig(pp.Customs["80x80"], sizeCustom("webp")) {
		t.Errorf("/a customs = %#v", pp.Customs)
	}
}

func TestNormalizeState(t *testing.T) {
	in := map[string]policy.PathPolicyConfig{
		"/a": {
			Customs: customs([2]any{"120x60", policy.PresetConfig{OutputFormats: fmts("webp", "avif")}}),
		},
		"/empty": {},
	}
	got := NormalizeState(in)
	if _, ok := got["/empty"]; ok {
		t.Error("empty path-policy entry must be removed")
	}
	pp, ok := got["/a"]
	if !ok {
		t.Fatal("state missing /a")
	}
	formats := pp.Customs["120x60"].OutputFormats
	if len(formats) != 2 || string(formats[0]) != "avif" || string(formats[1]) != "webp" {
		t.Errorf("output-formats = %#v, want sorted [avif webp]", formats)
	}
}
