package policy

import (
	"testing"

	"github.com/pkg-ru/imager/internal/domain/asset"
)

func mustReq(t *testing.T, url string) *asset.Request {
	t.Helper()
	req, err := asset.Parse(url)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", url, err)
	}
	return req
}

func TestDenyByDefault(t *testing.T) {
	// Пустая политика (без unsafe и без правил) отклоняет всё.
	p := &Policy{}
	req := mustReq(t, "/photos/photo-1-jpg/c-120x80@2.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Errorf("expected deny-by-default, got allowed: %+v", d)
	}
	if d.Reason != ReasonDenyByDefault {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonDenyByDefault)
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

func TestUnsafeAllowsCanonical(t *testing.T) {
	p := &Policy{Global: GlobalPolicy{Authorization: AuthUnsafe}}
	req := mustReq(t, "/photos/photo-1-jpg/c-120x80@2.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed in unsafe mode, got %+v", d)
	}
	if d.Reason != ReasonAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonAllowed)
	}
}

func TestSafeSizeRule(t *testing.T) {
	rule, err := ParseSizeRule("120-300x80-90")
	if err != nil {
		t.Fatalf("ParseSizeRule error: %v", err)
	}
	p := &Policy{Global: GlobalPolicy{Authorization: AuthSafe, SizeRules: []SizeRule{rule}}}

	allowed := []string{
		"/photos/photo-1-jpg/c-120x80@2.webp",
		"/photos/photo-1-jpg/c-200x85@2.webp",
	}
	for _, u := range allowed {
		d := p.Authorize(mustReq(t, u))
		if !d.Allowed {
			t.Errorf("expected allowed for %q, got %+v", u, d)
		}
	}

	denied := []string{
		"/photos/photo-1-jpg/c-100x80@2.webp", // width below range
		"/photos/photo-1-jpg/c-301x80@2.webp", // width above range
		"/photos/photo-1-jpg/c-200x79@2.webp", // height below range
	}
	for _, u := range denied {
		d := p.Authorize(mustReq(t, u))
		if d.Allowed {
			t.Errorf("expected denied for %q, got %+v", u, d)
		}
		if d.Reason != ReasonSizeNotAllowed {
			t.Errorf("Reason = %q, want %q", d.Reason, ReasonSizeNotAllowed)
		}
	}
}

func TestSafeNoRulesDeny(t *testing.T) {
	p := &Policy{Global: GlobalPolicy{Authorization: AuthSafe}}
	req := mustReq(t, "/photos/photo-1-jpg/c-120x80@2.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Error("expected deny when safe mode has no rules")
	}
	if d.Reason != ReasonDenyByDefault {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonDenyByDefault)
	}
}

func TestPresetAllowed(t *testing.T) {
	p := &Policy{Global: GlobalPolicy{Authorization: AuthSafe, AllowedPresets: []string{"thumb"}}}
	req := mustReq(t, "/photos/photo-1-jpg/thumb.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed preset, got %+v", d)
	}
}

func TestPresetAllowedWithDPRSuffix(t *testing.T) {
	// Имя пресета с @dpr-суффиксом распознаётся целиком: allowed-presets
	// должен содержать "thumb@2".
	p := &Policy{Global: GlobalPolicy{Authorization: AuthSafe, AllowedPresets: []string{"thumb@2"}}}
	req := mustReq(t, "/photos/photo-1-jpg/thumb@2.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed preset thumb@2, got %+v", d)
	}
	if req.PresetName().String() != "thumb@2" {
		t.Errorf("PresetName = %q, want thumb@2", req.PresetName())
	}
}

func TestPresetNotAllowed(t *testing.T) {
	p := &Policy{Global: GlobalPolicy{Authorization: AuthSafe, AllowedPresets: []string{"thumb"}}}
	req := mustReq(t, "/photos/photo-1-jpg/other.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Error("expected denied preset")
	}
	if d.Reason != ReasonPresetNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonPresetNotAllowed)
	}
}

// TestPathIndexLongestPrefixMatch проверяет выбор path-policy по правилу
// longest prefix match на всех примерах из ТЗ (пункт 3).
func TestPathIndexLongestPrefixMatch(t *testing.T) {
	p := &Policy{
		PathPolicies: []PathPolicy{
			{Path: "/"},
			{Path: "/users"},
			{Path: "/basket/users"},
			{Path: "/basket/products"},
			{Path: "/users/gift"},
		},
	}
	tests := []struct {
		path string
		want string
	}{
		// Пункт 1 "/": корневой каталог.
		{"products", "/"},
		{"", "/"},
		// Пункт 2 "/users".
		{"users", "/users"},
		// Пункт 3 "/basket" → "/" (НЕ "/basket/users" — сегменты не совпадают).
		{"basket", "/"},
		// Пункт 4 "/basket/products".
		{"basket/products", "/basket/products"},
		// Пункт 5 "/users/gift" (НЕ "/users").
		{"users/gift", "/users/gift"},
		// Вложенные пути под более специфичным префиксом.
		{"users/gift/deep", "/users/gift"},
		{"basket/products/extra", "/basket/products"},
		{"users/other", "/users"},
	}
	for _, tt := range tests {
		idx := p.pathIndex(tt.path)
		if idx < 0 {
			t.Errorf("pathIndex(%q) = -1, want %q", tt.path, tt.want)
			continue
		}
		if got := p.PathPolicies[idx].Path; got != tt.want {
			t.Errorf("pathIndex(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestPathIndexFallbackRoot проверяет, что "/" — fallback, применяется когда
// ни один другой префикс не совпал.
func TestPathIndexFallbackRoot(t *testing.T) {
	p := &Policy{
		PathPolicies: []PathPolicy{
			{Path: "/users"},
			{Path: "/"},
		},
	}
	// "/" в списке, но для "products" совпадает только "/".
	idx := p.pathIndex("products")
	if idx < 0 || p.PathPolicies[idx].Path != "/" {
		t.Errorf("pathIndex(products) = %d (%q), want /", idx, p.PathPolicies[idx].Path)
	}
	// Для "users" — более специфичный "/users".
	idx = p.pathIndex("users")
	if idx < 0 || p.PathPolicies[idx].Path != "/users" {
		t.Errorf("pathIndex(users) = %d (%q), want /users", idx, p.PathPolicies[idx].Path)
	}
}

// TestPathIndexNoMatch проверяет, что при отсутствии "/" и совпадений
// возвращается -1 (path-policy не применяется).
func TestPathIndexNoMatch(t *testing.T) {
	p := &Policy{
		PathPolicies: []PathPolicy{
			{Path: "/users"},
		},
	}
	if idx := p.pathIndex("products"); idx != -1 {
		t.Errorf("pathIndex(products) = %d, want -1", idx)
	}
}

// TestAuthorizePathDPR проверяет ограничение dpr для канонических URL.
func TestAuthorizePathDPR(t *testing.T) {
	dpr01 := Range{Min: 0, Max: 1}
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/", DPR: &dpr01},
		},
	}
	// dpr=1 (без суффикса) — разрешён.
	d := p.Authorize(mustReq(t, "/products/users-png/c-280x280.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed for dpr=1, got %+v", d)
	}
	// dpr=2 — отклонён.
	d = p.Authorize(mustReq(t, "/products/users-png/c-280x280@2.webp"))
	if d.Allowed {
		t.Error("expected denied for dpr=2")
	}
	if d.Reason != ReasonDPRNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonDPRNotAllowed)
	}
	// dpr=3 — отклонён.
	d = p.Authorize(mustReq(t, "/products/users-png/c-280x280@3.webp"))
	if d.Allowed {
		t.Error("expected denied for dpr=3")
	}
	if d.Reason != ReasonDPRNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonDPRNotAllowed)
	}
}

// TestAuthorizePathDPRRange23 проверяет диапазон "2-3".
func TestAuthorizePathDPRRange23(t *testing.T) {
	dpr23 := Range{Min: 2, Max: 3}
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/users", DPR: &dpr23},
		},
	}
	// dpr=2 — разрешён.
	d := p.Authorize(mustReq(t, "/users/users-png/c-280x280@2.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed for dpr=2, got %+v", d)
	}
	// dpr=3 — разрешён.
	d = p.Authorize(mustReq(t, "/users/users-png/c-280x280@3.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed for dpr=3, got %+v", d)
	}
	// dpr=1 — отклонён.
	d = p.Authorize(mustReq(t, "/users/users-png/c-280x280.webp"))
	if d.Allowed {
		t.Error("expected denied for dpr=1")
	}
	if d.Reason != ReasonDPRNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonDPRNotAllowed)
	}
}

// TestAuthorizePathCrop проверяет ограничение crop (crop=true → белый список
// {c, ct}).
func TestAuthorizePathCrop(t *testing.T) {
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/users", Crop: NewCropAllowList(asset.TransformCrop, asset.TransformCropTrim)},
		},
	}
	// transform "c" — crop присутствует, разрешён.
	d := p.Authorize(mustReq(t, "/users/users-png/c-280x280.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed for crop, got %+v", d)
	}
	// transform "ct" — crop присутствует, разрешён.
	d = p.Authorize(mustReq(t, "/users/users-png/ct-280x280.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed for crop+trim, got %+v", d)
	}
	// transform "" (resize) — crop отсутствует, отклонён.
	d = p.Authorize(mustReq(t, "/users/users-png/280x280.webp"))
	if d.Allowed {
		t.Error("expected denied when crop missing")
	}
	if d.Reason != ReasonCropNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonCropNotAllowed)
	}
	// transform "t" — crop отсутствует, отклонён.
	d = p.Authorize(mustReq(t, "/users/users-png/t-280x280.webp"))
	if d.Allowed {
		t.Error("expected denied when crop missing (trim only)")
	}
	if d.Reason != ReasonCropNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonCropNotAllowed)
	}
}

// TestAuthorizePathCropFalse проверяет запрет crop (crop=false → чёрный
// список {c, ct}).
func TestAuthorizePathCropFalse(t *testing.T) {
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/", Crop: NewCropDenyList(asset.TransformCrop, asset.TransformCropTrim)},
		},
	}
	// transform "" — crop отсутствует, разрешён.
	d := p.Authorize(mustReq(t, "/products/users-png/280x280.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed without crop, got %+v", d)
	}
	// transform "c" — crop запрещён, отклонён.
	d = p.Authorize(mustReq(t, "/products/users-png/c-280x280.webp"))
	if d.Allowed {
		t.Error("expected denied when crop present")
	}
	if d.Reason != ReasonCropNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonCropNotAllowed)
	}
}

// TestAuthorizePathCropModes проверяет строковые crop-режимы path-policy:
// разрешён только указанный режим (и его trim-вариант), остальные transform
// отклоняются с ReasonCropNotAllowed.
func TestAuthorizePathCropModes(t *testing.T) {
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/users", Crop: NewCropAllowList(
				asset.TransformSmartCrop, asset.TransformSmartCropTrim,
			)},
		},
	}
	allowed := []string{
		"/users/users-png/sc-280x280.webp",
		"/users/users-png/sct-280x280.webp",
	}
	for _, u := range allowed {
		d := p.Authorize(mustReq(t, u))
		if !d.Allowed {
			t.Errorf("expected allowed for %q, got %+v", u, d)
		}
	}
	denied := []string{
		"/users/users-png/c-280x280.webp",
		"/users/users-png/ct-280x280.webp",
		"/users/users-png/fc-280x280.webp",
		"/users/users-png/oc-280x280.webp",
		"/users/users-png/280x280.webp", // resize — crop отсутствует
		"/users/users-png/t-280x280.webp",
	}
	for _, u := range denied {
		d := p.Authorize(mustReq(t, u))
		if d.Allowed {
			t.Errorf("expected denied for %q", u)
			continue
		}
		if d.Reason != ReasonCropNotAllowed {
			t.Errorf("Reason = %q, want %q for %q", d.Reason, ReasonCropNotAllowed, u)
		}
	}
}

// TestAuthorizePathCropModeList проверяет список разрешённых режимов:
// crop: [smart, face] пропускает sc/sct/fc/fct и отклоняет остальные.
func TestAuthorizePathCropModeList(t *testing.T) {
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/", Crop: NewCropAllowList(
				asset.TransformSmartCrop, asset.TransformSmartCropTrim,
				asset.TransformFaceCrop, asset.TransformFaceCropTrim,
			)},
		},
	}
	allowed := []string{
		"/products/users-png/sc-280x280.webp",
		"/products/users-png/sct-280x280.webp",
		"/products/users-png/fc-280x280.webp",
		"/products/users-png/fct-280x280.webp",
	}
	for _, u := range allowed {
		d := p.Authorize(mustReq(t, u))
		if !d.Allowed {
			t.Errorf("expected allowed for %q, got %+v", u, d)
		}
	}
	denied := []string{
		"/products/users-png/c-280x280.webp",
		"/products/users-png/oc-280x280.webp",
		"/products/users-png/280x280.webp",
	}
	for _, u := range denied {
		d := p.Authorize(mustReq(t, u))
		if d.Allowed {
			t.Errorf("expected denied for %q", u)
		}
	}
}

// TestAuthorizePathCropNoneDeniesAllModes проверяет deny-форму "none":
// запрещены все crop-режимы, но resize и trim-only разрешены.
func TestAuthorizePathCropNoneDeniesAllModes(t *testing.T) {
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/", Crop: NewCropDenyList(
				asset.TransformCrop, asset.TransformCropTrim,
				asset.TransformSmartCrop, asset.TransformSmartCropTrim,
				asset.TransformFaceCrop, asset.TransformFaceCropTrim,
				asset.TransformObjectCrop, asset.TransformObjectCropTrim,
			)},
		},
	}
	allowed := []string{
		"/products/users-png/280x280.webp",
		"/products/users-png/t-280x280.webp",
	}
	for _, u := range allowed {
		d := p.Authorize(mustReq(t, u))
		if !d.Allowed {
			t.Errorf("expected allowed for %q, got %+v", u, d)
		}
	}
	denied := []string{
		"/products/users-png/c-280x280.webp",
		"/products/users-png/ct-280x280.webp",
		"/products/users-png/sc-280x280.webp",
		"/products/users-png/sct-280x280.webp",
		"/products/users-png/fc-280x280.webp",
		"/products/users-png/fct-280x280.webp",
		"/products/users-png/oc-280x280.webp",
		"/products/users-png/oct-280x280.webp",
	}
	for _, u := range denied {
		d := p.Authorize(mustReq(t, u))
		if d.Allowed {
			t.Errorf("expected denied for %q", u)
			continue
		}
		if d.Reason != ReasonCropNotAllowed {
			t.Errorf("Reason = %q, want %q for %q", d.Reason, ReasonCropNotAllowed, u)
		}
	}
}

// TestAuthorizePathTrimWithSmartCropTrim проверяет, что trim-требование
// учитывает все trim-варианты (включая sct/fct/oct), а не только t/ct.
func TestAuthorizePathTrimWithSmartCropTrim(t *testing.T) {
	trimTrue := true
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/users/gift", Trim: &trimTrue},
		},
	}
	// sct/fct/oct содержат trim — разрешены.
	for _, u := range []string{
		"/users/gift/users-png/sct-280x280.webp",
		"/users/gift/users-png/fct-280x280.webp",
		"/users/gift/users-png/oct-280x280.webp",
	} {
		d := p.Authorize(mustReq(t, u))
		if !d.Allowed {
			t.Errorf("expected allowed for %q (has trim), got %+v", u, d)
		}
	}
}

// TestCropRuleNilAllowsEverything проверяет, что nil-правило не ограничивает.
func TestCropRuleNilAllowsEverything(t *testing.T) {
	var r *CropRule
	for _, tr := range []asset.Transform{"", "c", "t", "ct", "sc", "fc", "oc", "sct", "fct", "oct"} {
		if !r.Allows(tr) {
			t.Errorf("nil rule must allow %q", tr)
		}
	}
}

// TestCropRuleString проверяет человекочитаемое описание правил.
func TestCropRuleString(t *testing.T) {
	if got := (*CropRule)(nil).String(); got != "any transform" {
		t.Errorf("nil String = %q", got)
	}
	allow := NewCropAllowList(asset.TransformCrop, asset.TransformCropTrim)
	if s := allow.String(); s == "" {
		t.Error("allow rule String must not be empty")
	}
	deny := NewCropDenyList(asset.TransformCrop)
	if s := deny.String(); s == "" {
		t.Error("deny rule String must not be empty")
	}
}

// TestAuthorizePathTrim проверяет ограничение trim.
func TestAuthorizePathTrim(t *testing.T) {
	trimTrue := true
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/users/gift", Trim: &trimTrue},
		},
	}
	// transform "t" — trim присутствует, разрешён.
	d := p.Authorize(mustReq(t, "/users/gift/users-png/t-280x280.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed for trim, got %+v", d)
	}
	// transform "ct" — trim присутствует, разрешён.
	d = p.Authorize(mustReq(t, "/users/gift/users-png/ct-280x280.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed for crop+trim, got %+v", d)
	}
	// transform "c" — trim отсутствует, отклонён.
	d = p.Authorize(mustReq(t, "/users/gift/users-png/c-280x280.webp"))
	if d.Allowed {
		t.Error("expected denied when trim missing")
	}
	if d.Reason != ReasonTrimNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonTrimNotAllowed)
	}
}

// TestAuthorizePathLongestPrefix проверяет, что path-policy применяется по
// самому длинному совпадающему префиксу (примеры из ТЗ пункт 3).
func TestAuthorizePathLongestPrefix(t *testing.T) {
	dpr01 := Range{Min: 0, Max: 1}
	dpr23 := Range{Min: 2, Max: 3}
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/", DPR: &dpr01},
			{Path: "/users", DPR: &dpr23},
			{Path: "/users/gift", DPR: &dpr01},
			{Path: "/basket/products", DPR: &dpr01},
		},
	}
	tests := []struct {
		url     string
		allowed bool
	}{
		// "/" — dpr 0-1: dpr=1 разрешён, dpr=2 отклонён.
		{"/products/users-png/c-280x280.webp", true},
		{"/products/users-png/c-280x280@2.webp", false},
		// "/users" — dpr 2-3: dpr=2 разрешён, dpr=1 отклонён.
		{"/users/users-png/c-280x280@2.webp", true},
		{"/users/users-png/c-280x280.webp", false},
		// "/users/gift" — dpr 0-1 (НЕ "/users"): dpr=1 разрешён, dpr=2 отклонён.
		{"/users/gift/users-png/c-280x280.webp", true},
		{"/users/gift/users-png/c-280x280@2.webp", false},
		// "/basket" — нет совпадения кроме "/": dpr=1 разрешён, dpr=2 отклонён.
		{"/basket/users-png/c-280x280.webp", true},
		{"/basket/users-png/c-280x280@2.webp", false},
		// "/basket/products" — dpr 0-1: dpr=1 разрешён, dpr=2 отклонён.
		{"/basket/products/users-png/c-280x280.webp", true},
		{"/basket/products/users-png/c-280x280@2.webp", false},
	}
	for _, tt := range tests {
		d := p.Authorize(mustReq(t, tt.url))
		if d.Allowed != tt.allowed {
			t.Errorf("Authorize(%q) allowed = %v, want %v (reason %q)", tt.url, d.Allowed, tt.allowed, d.Reason)
		}
	}
}

// TestAuthorizePathNotAppliedToPreset проверяет, что path-policy не
// применяется к preset-запросам.
func TestAuthorizePathNotAppliedToPreset(t *testing.T) {
	dpr23 := Range{Min: 2, Max: 3}
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/users", DPR: &dpr23},
		},
	}
	// Preset-запрос с dpr=1 в "/users" — path-policy не применяется, разрешён.
	d := p.Authorize(mustReq(t, "/users/bugoga-gif/thumb.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed preset (path-policy not applied), got %+v", d)
	}
}

// TestAuthorizePathNoMatchAllowed проверяет, что при отсутствии совпадений
// (нет "/") запрос разрешается без ограничений.
func TestAuthorizePathNoMatchAllowed(t *testing.T) {
	dpr23 := Range{Min: 2, Max: 3}
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthUnsafe},
		PathPolicies: []PathPolicy{
			{Path: "/users", DPR: &dpr23},
		},
	}
	// Путь "products" не совпадает ни с одним префиксом — разрешён.
	d := p.Authorize(mustReq(t, "/products/users-png/c-280x280.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed when no path-policy matches, got %+v", d)
	}
}

// TestAuthorizePathDoesNotExpandRights проверяет, что path-policy не
// расширяет права: если глобальная политика запретила, path-policy не
// разрешает.
func TestAuthorizePathDoesNotExpandRights(t *testing.T) {
	rule, _ := ParseSizeRule("10x10")
	dpr01 := Range{Min: 0, Max: 1}
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthSafe, SizeRules: []SizeRule{rule}},
		PathPolicies: []PathPolicy{
			{Path: "/", DPR: &dpr01},
		},
	}
	// Размер 999x999 не покрыт глобальным правилом — отклонён даже если
	// path-policy разрешила бы dpr.
	d := p.Authorize(mustReq(t, "/products/users-png/c-999x999.webp"))
	if d.Allowed {
		t.Error("expected denied by global size rule")
	}
	if d.Reason != ReasonSizeNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonSizeNotAllowed)
	}
}

func TestCheckLimits(t *testing.T) {
	p := &Policy{Global: GlobalPolicy{Limits: Limits{Width: 100}}}
	r := p.CheckLimits("photos", 0, 200, 0, 1, 1, 0, 0)
	if !r.Exceeded() || r.ExceededLimit != "width" {
		t.Errorf("expected width exceed, got %+v", r)
	}
}

func TestValidatePolicy(t *testing.T) {
	valid := &Policy{Global: GlobalPolicy{Authorization: AuthSafe}}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid policy, got %v", err)
	}

	invalid := []*Policy{
		{Global: GlobalPolicy{Authorization: "bogus"}},
		{PathPolicies: []PathPolicy{{Path: ""}}},
		{PathPolicies: []PathPolicy{{Path: "a"}, {Path: "a"}}},
		{PathPolicies: []PathPolicy{{Path: "a"}, {Path: "/a/"}}},
		{PathPolicies: []PathPolicy{{Path: "/", DPR: &Range{Min: 0, Max: 4}}}},
		{Global: GlobalPolicy{Limits: Limits{Width: -1}}},
	}
	for _, p := range invalid {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate(%+v) expected error", p)
		}
	}
}

func TestPathNames(t *testing.T) {
	p := &Policy{
		PathPolicies: []PathPolicy{
			{Path: "/users"},
			{Path: "/"},
			{Path: "/basket/products"},
		},
	}
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
