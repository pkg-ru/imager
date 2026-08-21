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
	req := mustReq(t, "/v1/photos/photo-1-jpg/c-120x80@2.webp")
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
	req := mustReq(t, "/v1/photos/photo-1-jpg/c-120x80@2.webp")
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
		"/v1/photos/photo-1-jpg/c-120x80@2.webp",
		"/v1/photos/photo-1-jpg/c-200x85@2.webp",
	}
	for _, u := range allowed {
		d := p.Authorize(mustReq(t, u))
		if !d.Allowed {
			t.Errorf("expected allowed for %q, got %+v", u, d)
		}
	}

	denied := []string{
		"/v1/photos/photo-1-jpg/c-100x80@2.webp", // width below range
		"/v1/photos/photo-1-jpg/c-301x80@2.webp", // width above range
		"/v1/photos/photo-1-jpg/c-200x79@2.webp", // height below range
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
	req := mustReq(t, "/v1/photos/photo-1-jpg/c-120x80@2.webp")
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
	req := mustReq(t, "/v1/photos/photo-1-jpg/thumb.webp")
	d := p.Authorize(req)
	if !d.Allowed {
		t.Errorf("expected allowed preset, got %+v", d)
	}
}

func TestPresetNotAllowed(t *testing.T) {
	p := &Policy{Global: GlobalPolicy{Authorization: AuthSafe, AllowedPresets: []string{"thumb"}}}
	req := mustReq(t, "/v1/photos/photo-1-jpg/other.webp")
	d := p.Authorize(req)
	if d.Allowed {
		t.Error("expected denied preset")
	}
	if d.Reason != ReasonPresetNotAllowed {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonPresetNotAllowed)
	}
}

func TestBucketOverride(t *testing.T) {
	rule, _ := ParseSizeRule("10x10")
	p := &Policy{
		Global: GlobalPolicy{Authorization: AuthSafe, SizeRules: []SizeRule{rule}},
		Buckets: []BucketPolicy{
			{Bucket: "private", Authorization: AuthUnsafe},
		},
	}
	// В bucket "private" — unsafe, разрешено всё.
	d := p.Authorize(mustReq(t, "/v1/private/photo-1-jpg/c-999x999@2.webp"))
	if !d.Allowed {
		t.Errorf("expected allowed in unsafe bucket, got %+v", d)
	}
	// Вне bucket — safe, только 10x10.
	d = p.Authorize(mustReq(t, "/v1/public/photo-1-jpg/c-999x999@2.webp"))
	if d.Allowed {
		t.Errorf("expected denied outside unsafe bucket, got %+v", d)
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
		{Buckets: []BucketPolicy{{Bucket: ""}}},
		{Buckets: []BucketPolicy{{Bucket: "a"}, {Bucket: "a"}}},
		{Global: GlobalPolicy{Limits: Limits{Width: -1}}},
	}
	for _, p := range invalid {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate(%+v) expected error", p)
		}
	}
}
