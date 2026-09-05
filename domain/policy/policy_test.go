package policy

import (
	"testing"

	"github.com/pkg-ru/dynamic"
	"gitverse.ru/pkg-ru/imager/domain/asset"
)

func mustReq(t *testing.T, url string) *asset.Request {
	t.Helper()
	req, err := asset.Parse(url)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", url, err)
	}
	return req
}

// mustPolicy компилирует политику из конфигурации.
func mustPolicy(t *testing.T, cfg Config) *Policy {
	t.Helper()
	compiled, err := Compile(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	return compiled.Policy
}

// baseCfg — базовая конфигурация: "/" с пресетом banner и customs.
func baseCfg() Config {
	return Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/": {
				Presets: dynamic.StringSlice{dynamic.String("banner")},
				Customs: map[string]PresetConfig{
					"200x200": presetCfg("", 0, 0, "webp"),
					"x":       presetCfg("", 0, 0, "webp"),
				},
			},
		},
		Presets: map[string]PresetConfig{
			"banner": presetCfg("banner", 200, 0, "webp", "avif"),
		},
	}
}

func TestDenyByDefault(t *testing.T) {
	// Пустая политика (без path-policies) отклоняет всё.
	p := &Policy{}
	req := mustReq(t, "/photos/photo-1-jpg/banner.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Errorf("expected deny-by-default, got allowed: %+v", d)
	}
	if d.Reason != ReasonPathNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonPathNotAllowed)
	}
}

func TestNilRequest(t *testing.T) {
	p := &Policy{}
	d := p.Authorize(nil)
	if d.Allowed {
		t.Error("expected deny for nil request")
	}
	if d.Reason != ReasonNilRequest {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonNilRequest)
	}
}

func TestPathNotAllowed(t *testing.T) {
	// Нет "/" и нет совпадений → path_not_allowed.
	p := mustPolicy(t, Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/users": {
				Presets: dynamic.StringSlice{dynamic.String("banner")},
			},
		},
		Presets: map[string]PresetConfig{
			"banner": presetCfg("banner", 200, 0, "webp"),
		},
	})
	req := mustReq(t, "/products/photo-1-jpg/banner.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Errorf("expected deny, got %+v", d)
	}
	if d.Reason != ReasonPathNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonPathNotAllowed)
	}
}

func TestSegmentNotAllowed(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/other.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Errorf("expected deny, got %+v", d)
	}
	if d.Reason != ReasonSegmentNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonSegmentNotAllowed)
	}
}

func TestPresetAllowed(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/banner.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed preset, got %+v", d)
	}
}

func TestPresetAllowedWithDPR(t *testing.T) {
	// Пресет banner без dpr в настройках: @2 в URL допустим.
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/banner@2.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed preset with dpr, got %+v", d)
	}
}

func TestPresetDPRSetDeniesSuffix(t *testing.T) {
	// Пресет banner с dpr: 1 в настройках (фиксированный множитель 1):
	// @2 в URL запрещён — вариант @N требует отдельного пресета banner@2.
	cfg := baseCfg()
	pc := cfg.Presets["banner"]
	pc.DPR = dynamic.NewNullable(dynamic.Uint32(1))
	cfg.Presets["banner"] = pc
	p := mustPolicy(t, cfg)
	// Без суффикса — допустимо, итоговый dpr=1.
	req := mustReq(t, "/photos/photo-1-jpg/banner.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed without suffix, got %+v", d)
	}
	// С суффиксом @2 — запрещено.
	req2 := mustReq(t, "/photos/photo-1-jpg/banner@2.webp")
	d2 := p.Authorize(req2)
	if d2.Allowed {
		t.Errorf("expected deny for @2 when dpr set, got %+v", d2)
	}
	if d2.Reason != ReasonDPRNotAllowed {
		t.Errorf("Reason = %q, want %q", d2.Reason, ReasonDPRNotAllowed)
	}
}

func TestPresetNameWithDPR(t *testing.T) {
	// Пресет "banner@2": в URL допустим ТОЛЬКО banner@2.
	cfg := baseCfg()
	cfg.Presets = map[string]PresetConfig{"banner@2": presetCfgWithDPR("banner@2", 200, 0, 2, "webp")}
	pp := cfg.PathPolicies["/"]
	pp.Presets = dynamic.StringSlice{dynamic.String("banner@2")}
	cfg.PathPolicies["/"] = pp
	p := mustPolicy(t, cfg)
	// banner@2 — допустимо.
	req := mustReq(t, "/photos/photo-1-jpg/banner@2.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed banner@2, got %+v", d)
	}
	// banner (без суффикса) — не найдено (нет пресета "banner").
	req2 := mustReq(t, "/photos/photo-1-jpg/banner.webp")
	d2 := p.Authorize(req2)
	if d2.Allowed {
		t.Errorf("expected deny for banner without suffix, got %+v", d2)
	}
	if d2.Reason != ReasonSegmentNotAllowed {
		t.Errorf("Reason = %q, want %q", d2.Reason, ReasonSegmentNotAllowed)
	}
}

func TestPresetNameWithDPRConflict(t *testing.T) {
	// Пресет "banner@2" с dpr: 3 в настройках — ошибка конфига.
	cfg := baseCfg()
	cfg.Presets = map[string]PresetConfig{
		"banner@2": {
			Width:         dynamic.Uint32(200),
			OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
			DPR:           dynamic.NewNullable(dynamic.Uint32(3)),
		},
	}
	if _, err := Compile(cfg, nil, nil); err == nil {
		t.Error("expected config error for dpr conflict in name vs settings")
	}
}

func TestCustomAllowed(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/200x200.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed custom, got %+v", d)
	}
}

func TestCustomWithDPRWildcard(t *testing.T) {
	// Custom 200x200 без dpr в настройках: @2 в URL допустим (wildcard-dpr).
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/200x200@2.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed custom with dpr, got %+v", d)
	}
}

func TestCustomDPRSetDeniesSuffix(t *testing.T) {
	// Custom 200x200 с dpr: 1 в настройках (фиксированный множитель 1):
	// @2 в URL запрещён — для варианта @N нужен отдельный custom 200x200@2.
	cfg := baseCfg()
	cfg.PathPolicies["/"].Customs["200x200"] = PresetConfig{
		Width: dynamic.Uint32(200), Height: dynamic.Uint32(200),
		OutputFormats: dynamic.StringSlice{dynamic.String("webp")},
		DPR:           dynamic.NewNullable(dynamic.Uint32(1)),
	}
	p := mustPolicy(t, cfg)
	req := mustReq(t, "/photos/photo-1-jpg/200x200@2.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Errorf("expected deny for @2 when custom dpr set, got %+v", d)
	}
	if d.Reason != ReasonDPRNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonDPRNotAllowed)
	}
	// Без суффикса — допустимо.
	req2 := mustReq(t, "/photos/photo-1-jpg/200x200.webp")
	if d2 := p.Authorize(req2); !d2.Allowed {
		t.Errorf("expected allowed without suffix, got %+v", d2)
	}
}

func TestCustomExactNameWithDPR(t *testing.T) {
	// Точный custom "200x100@2" (имя с @2 требует dpr: 2): URL 200x100@2
	// матчится точным именем.
	cfg := baseCfg()
	cfg.PathPolicies["/"].Customs["200x100@2"] = presetCfgWithDPR("", 0, 0, 2, "webp", "avif")
	p := mustPolicy(t, cfg)
	req := mustReq(t, "/photos/photo-1-jpg/200x100@2.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed exact custom 200x100@2, got %+v", d)
	}
	// URL 200x100 без суффикса НЕ матчится точным custom с @dpr в имени.
	req2 := mustReq(t, "/photos/photo-1-jpg/200x100.webp")
	d2 := p.Authorize(req2)
	if d2.Allowed {
		t.Errorf("expected deny for 200x100 without suffix, got %+v", d2)
	}
}

func TestFormatNotAllowed(t *testing.T) {
	// Пресет banner: output-formats [webp, avif]. png — запрещён.
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/banner.png")
	d := p.Authorize(req)
	if d.Allowed {
		t.Errorf("expected deny for png, got %+v", d)
	}
	if d.Reason != ReasonFormatNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonFormatNotAllowed)
	}
}

func TestCustomPriorityOverPreset(t *testing.T) {
	// Имя есть и в customs, и в presets — побеждает custom.
	cfg := baseCfg()
	cfg.PathPolicies["/"].Customs["200x200"] = presetCfg("", 300, 100, "webp")
	p := mustPolicy(t, cfg)
	req := mustReq(t, "/photos/photo-1-jpg/200x200.webp")
	resolved, d := p.Resolve(req)
	if !d.Allowed {
		t.Fatalf("expected allowed, got %+v", d)
	}
	if got := resolved.Size().String(); got != "300x100" {
		t.Errorf("resolved size = %q, want 300x100 (custom priority)", got)
	}
}

func TestResolveAppliesSettings(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/banner.webp")
	resolved, d := p.Resolve(req)
	if !d.Allowed {
		t.Fatalf("expected allowed, got %+v", d)
	}
	if !resolved.IsResolved() {
		t.Error("expected resolved request")
	}
	if got := resolved.Size().String(); got != "200x" {
		t.Errorf("resolved size = %q, want 200x", got)
	}
	if got := resolved.DPR().Int(); got != asset.DefaultDPR {
		t.Errorf("resolved DPR = %d, want 1", got)
	}
	if got := resolved.Transform(); got != "" {
		t.Errorf("resolved transform = %q, want empty (resize)", got)
	}
}

func TestResolveCustomSize(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/200x200.webp")
	resolved, d := p.Resolve(req)
	if !d.Allowed {
		t.Fatalf("expected allowed, got %+v", d)
	}
	if got := resolved.Size().String(); got != "200x200" {
		t.Errorf("resolved size = %q, want 200x200", got)
	}
}

func TestResolveCustomX(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/x.webp")
	resolved, d := p.Resolve(req)
	if !d.Allowed {
		t.Fatalf("expected allowed, got %+v", d)
	}
	if !resolved.Size().IsOriginal() {
		t.Errorf("resolved size = %q, want original (x)", resolved.Size().String())
	}
}

func TestResolveDPRFromURL(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	req := mustReq(t, "/photos/photo-1-jpg/banner@2.webp")
	resolved, d := p.Resolve(req)
	if !d.Allowed {
		t.Fatalf("expected allowed, got %+v", d)
	}
	if got := resolved.DPR().Int(); got != 2 {
		t.Errorf("resolved DPR = %d, want 2", got)
	}
}

func TestResolveDPRFromSettings(t *testing.T) {
	// Пресет banner с dpr: 1 (фиксированный множитель): URL без суффикса →
	// итоговый dpr=1.
	cfg := baseCfg()
	pc := cfg.Presets["banner"]
	pc.DPR = dynamic.NewNullable(dynamic.Uint32(1))
	cfg.Presets["banner"] = pc
	p := mustPolicy(t, cfg)
	req := mustReq(t, "/photos/photo-1-jpg/banner.webp")
	resolved, d := p.Resolve(req)
	if !d.Allowed {
		t.Fatalf("expected allowed, got %+v", d)
	}
	if got := resolved.DPR().Int(); got != 1 {
		t.Errorf("resolved DPR = %d, want 1", got)
	}
}

func TestResolveDPRFromName(t *testing.T) {
	// Пресет "banner@2": URL banner@2 → dpr=2 из имени.
	cfg := baseCfg()
	cfg.Presets = map[string]PresetConfig{"banner@2": presetCfgWithDPR("banner@2", 200, 0, 2, "webp")}
	pp := cfg.PathPolicies["/"]
	pp.Presets = dynamic.StringSlice{dynamic.String("banner@2")}
	cfg.PathPolicies["/"] = pp
	p := mustPolicy(t, cfg)
	req := mustReq(t, "/photos/photo-1-jpg/banner@2.webp")
	resolved, d := p.Resolve(req)
	if !d.Allowed {
		t.Fatalf("expected allowed, got %+v", d)
	}
	if got := resolved.DPR().Int(); got != 2 {
		t.Errorf("resolved DPR = %d, want 2", got)
	}
}

func TestResolveDPRConflict(t *testing.T) {
	// Пресет "banner@2": URL banner@3 → конфликт dpr.
	cfg := baseCfg()
	cfg.Presets = map[string]PresetConfig{"banner@2": presetCfgWithDPR("banner@2", 200, 0, 2, "webp")}
	pp := cfg.PathPolicies["/"]
	pp.Presets = dynamic.StringSlice{dynamic.String("banner@2")}
	cfg.PathPolicies["/"] = pp
	p := mustPolicy(t, cfg)
	req := mustReq(t, "/photos/photo-1-jpg/banner@3.webp")
	_, d := p.Resolve(req)
	if d.Allowed {
		t.Fatalf("expected deny, got %+v", d)
	}
	if d.Reason != ReasonDPRNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonDPRNotAllowed)
	}
}

// TestPathIndexLongestPrefixMatch проверяет выбор path-policy по правилу
// longest prefix match.
func TestPathIndexLongestPrefixMatch(t *testing.T) {
	p := mustPolicy(t, Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/":                {},
			"/users":           {},
			"/basket/users":    {},
			"/basket/products": {},
			"/users/gift":      {},
		},
	})
	tests := []struct {
		path string
		want string
	}{
		// "/": корневой каталог (fallback).
		{"products", "/"},
		{"", "/"},
		// "/users".
		{"users", "/users"},
		// "/basket" → "/" (НЕ "/basket/users" — сегменты не совпадают).
		{"basket", "/"},
		// "/basket/products".
		{"basket/products", "/basket/products"},
		// "/users/gift" (НЕ "/users").
		{"users/gift", "/users/gift"},
		// Вложенные пути под более специфичным префиксом.
		{"users/gift/deep", "/users/gift"},
		{"basket/products/extra", "/basket/products"},
		{"users/other", "/users"},
	}
	for _, tt := range tests {
		pp := p.matchPath(tt.path)
		if pp == nil {
			t.Errorf("matchPath(%q) = nil, want %q", tt.path, tt.want)
			continue
		}
		if got := pp.Path; got != tt.want {
			t.Errorf("matchPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestPathIndexFallbackRoot проверяет, что "/" — fallback, применяется когда
// ни один другой префикс не совпал.
func TestPathIndexFallbackRoot(t *testing.T) {
	p := mustPolicy(t, Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/users": {},
			"/":      {},
		},
	})
	// "/" в списке, но для "products" совпадает только "/".
	pp := p.matchPath("products")
	if pp == nil || pp.Path != "/" {
		t.Errorf("matchPath(products) = %v, want /", pp)
	}
	// Для "users" — более специфичный "/users".
	pp = p.matchPath("users")
	if pp == nil || pp.Path != "/users" {
		t.Errorf("matchPath(users) = %v, want /users", pp)
	}
}

// TestPathIndexNoMatch проверяет, что при отсутствии "/" и совпадений
// возвращается nil (path-policy не применяется).
func TestPathIndexNoMatch(t *testing.T) {
	p := mustPolicy(t, Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/users": {},
		},
	})
	if pp := p.matchPath("products"); pp != nil {
		t.Errorf("matchPath(products) = %v, want nil", pp)
	}
}

func TestValidatePolicy(t *testing.T) {
	valid := mustPolicy(t, baseCfg())
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid policy, got %v", err)
	}
}

func TestPathNames(t *testing.T) {
	p := mustPolicy(t, Config{
		PathPolicies: map[string]PathPolicyConfig{
			"/users":           {},
			"/":                {},
			"/basket/products": {},
		},
	})
	got := p.PathNames()
	want := []string{"/", "/basket/products", "/users"}
	if len(got) != len(want) {
		t.Fatalf("PathNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PathNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPresetsAccessor(t *testing.T) {
	p := mustPolicy(t, baseCfg())
	if p.Presets() == nil {
		t.Fatal("expected non-nil presets")
	}
	if _, ok := p.Presets().Get("banner"); !ok {
		t.Error("expected preset banner in global set")
	}
}
