package policy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/asset"
)

// Authorization определяет режим авторизации запроса.
type Authorization string

const (
	// AuthSafe — безопасный режим: разрешены только явно разрешённые
	// параметры (preset или параметры, покрытые правилами).
	AuthSafe Authorization = "safe"
	// AuthUnsafe — небезопасный режим: разрешены произвольные параметры.
	AuthUnsafe Authorization = "unsafe"
)

// ValidAuthorization проверяет допустимость режима авторизации.
func ValidAuthorization(a Authorization) bool {
	return a == AuthSafe || a == AuthUnsafe
}

// SizeRule описывает правило допустимого размера. nil-поле означает "любой".
type SizeRule struct {
	Width  *Range
	Height *Range
}

// Range описывает диапазон допустимых значений измерения.
type Range struct {
	Min int
	Max int
}

// Contains проверяет, что значение попадает в диапазон.
func (r *Range) Contains(v int) bool { return v >= r.Min && v <= r.Max }

// Matches проверяет, что размер удовлетворяет правилу.
func (r *SizeRule) Matches(s asset.Size) bool {
	if r == nil {
		return true
	}
	if r.Width != nil {
		w := s.Width()
		if w == nil {
			return false
		}
		if !r.Width.Contains(w.Int()) {
			return false
		}
	}
	if r.Height != nil {
		h := s.Height()
		if h == nil {
			return false
		}
		if !r.Height.Contains(h.Int()) {
			return false
		}
	}
	return true
}

// BucketPolicy — скомпилированная политика для конкретного bucket (префикса пути).
type BucketPolicy struct {
	Bucket         string
	Authorization  Authorization
	SizeRules      []SizeRule
	AllowedPresets []string
	Limits         Limits
}

// GlobalPolicy — скомпилированная глобальная политика по умолчанию.
type GlobalPolicy struct {
	Authorization  Authorization
	SizeRules      []SizeRule
	AllowedPresets []string
	Limits         Limits
}

// Policy — deny-by-default скомпилированная политика.
//
// Политика неизменяема после компиляции. По умолчанию (когда не задан
// AuthUnsafe и нет правил) все запросы отклоняются (deny-by-default).
type Policy struct {
	Global  GlobalPolicy
	Buckets []BucketPolicy
}

// bucketIndex возвращает индекс политики bucket для заданного пути или -1.
func (p *Policy) bucketIndex(path string) int {
	best := -1
	bestLen := -1
	for i, b := range p.Buckets {
		if b.Bucket == "" {
			continue
		}
		if path == b.Bucket || strings.HasPrefix(path, b.Bucket+"/") {
			if len(b.Bucket) > bestLen {
				best = i
				bestLen = len(b.Bucket)
			}
		}
	}
	return best
}

// effective возвращает объединённую политику для заданного пути.
func (p *Policy) effective(path string) (GlobalPolicy, *BucketPolicy) {
	g := p.Global
	idx := p.bucketIndex(path)
	if idx < 0 {
		return g, nil
	}
	b := p.Buckets[idx]
	if b.Authorization != "" {
		g.Authorization = b.Authorization
	}
	if b.SizeRules != nil {
		g.SizeRules = b.SizeRules
	}
	if b.AllowedPresets != nil {
		g.AllowedPresets = b.AllowedPresets
	}
	if b.Limits != (Limits{}) {
		g.Limits = b.Limits
	}
	return g, &b
}

// DecisionReason — причина решения политики.
type DecisionReason string

const (
	// ReasonAllowed — запрос разрешён.
	ReasonAllowed DecisionReason = "allowed"
	// ReasonDenyByDefault — запрос отклонён по умолчанию (нет правил).
	ReasonDenyByDefault DecisionReason = "deny_by_default"
	// ReasonPresetNotAllowed — пресет не разрешён.
	ReasonPresetNotAllowed DecisionReason = "preset_not_allowed"
	// ReasonSizeNotAllowed — размер не покрыт ни одним правилом.
	ReasonSizeNotAllowed DecisionReason = "size_not_allowed"
	// ReasonNilRequest — запрос nil.
	ReasonNilRequest DecisionReason = "nil_request"
	// ReasonLimitExceeded — превышен лимит.
	ReasonLimitExceeded DecisionReason = "limit_exceeded"
)

// Decision — результат авторизации запроса.
type Decision struct {
	// Allowed — true, если запрос разрешён.
	Allowed bool
	// Reason — причина решения.
	Reason DecisionReason
	// Detail — дополнительная информация (например, имя лимита).
	Detail string
}

// Authorize проверяет, разрешён ли запрос согласно политике.
//
// Политика deny-by-default: если режим не задан как unsafe и запрос не
// покрыт правилами, он отклоняется.
func (p *Policy) Authorize(req *asset.Request) Decision {
	if req == nil {
		return Decision{Allowed: false, Reason: ReasonNilRequest}
	}
	g, _ := p.effective(req.Path())

	if req.IsPreset() {
		if g.Authorization == AuthUnsafe {
			return Decision{Allowed: true, Reason: ReasonAllowed}
		}
		if !contains(g.AllowedPresets, req.PresetName().String()) {
			return Decision{
				Allowed: false,
				Reason:  ReasonPresetNotAllowed,
				Detail:  fmt.Sprintf("preset %q is not allowed", req.PresetName()),
			}
		}
		return Decision{Allowed: true, Reason: ReasonAllowed}
	}

	if g.Authorization == AuthUnsafe {
		return Decision{Allowed: true, Reason: ReasonAllowed}
	}

	if len(g.SizeRules) == 0 {
		return Decision{Allowed: false, Reason: ReasonDenyByDefault}
	}
	for _, rule := range g.SizeRules {
		if rule.Matches(req.Size()) {
			return Decision{Allowed: true, Reason: ReasonAllowed}
		}
	}
	return Decision{
		Allowed: false,
		Reason:  ReasonSizeNotAllowed,
		Detail:  fmt.Sprintf("size %s is not allowed by any rule", req.Size().String()),
	}
}

// CheckLimits проверяет фактические значения против лимитов политики для пути.
func (p *Policy) CheckLimits(path string, sourceBytes int64, width, height, dpr, frames int, outputBytes, duration int64) CheckResult {
	g, _ := p.effective(path)
	return g.Limits.Check(sourceBytes, width, height, dpr, frames, outputBytes, duration)
}

// Validate проверяет корректность скомпилированной политики.
func (p *Policy) Validate() error {
	if p == nil {
		return fmt.Errorf("policy is nil")
	}
	if p.Global.Authorization != "" && !ValidAuthorization(p.Global.Authorization) {
		return fmt.Errorf("global authorization %q is invalid", p.Global.Authorization)
	}
	if _, err := NewLimits(p.Global.Limits); err != nil {
		return fmt.Errorf("global limits: %w", err)
	}
	seen := map[string]bool{}
	for i, b := range p.Buckets {
		if b.Bucket == "" {
			return fmt.Errorf("bucket %d: empty bucket name", i)
		}
		if seen[b.Bucket] {
			return fmt.Errorf("duplicate bucket %q", b.Bucket)
		}
		seen[b.Bucket] = true
		if b.Authorization != "" && !ValidAuthorization(b.Authorization) {
			return fmt.Errorf("bucket %q: invalid authorization %q", b.Bucket, b.Authorization)
		}
		if _, err := NewLimits(b.Limits); err != nil {
			return fmt.Errorf("bucket %q limits: %w", b.Bucket, err)
		}
	}
	return nil
}

// BucketNames возвращает отсортированный список имён bucket.
func (p *Policy) BucketNames() []string {
	names := make([]string, 0, len(p.Buckets))
	for _, b := range p.Buckets {
		names = append(names, b.Bucket)
	}
	sort.Strings(names)
	return names
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
