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

// PathPolicy — скомпилированная path-policy для конкретного префикса пути.
//
// Path — нормализованный префикс пути (см. normalizePath). Политика
// применяется только к каноническим URL (не preset) и является
// дополнительным ограничением поверх глобальной политики: она не расширяет
// права, а только ужесточает (dpr/crop/trim).
type PathPolicy struct {
	// Path — нормализованный префикс пути (например "/", "/users",
	// "/basket/products"). "/" — fallback, применяется ко всем путям, если
	// нет более специфичного совпадения.
	Path string
	// DPR — допустимый диапазон DPR (nil = без ограничения). Например
	// "0-1" разрешает только dpr=1, "2-3" — dpr 2 или 3.
	DPR *Range
	// Crop — требование к crop (nil = не задано/неважно). true = crop
	// обязан присутствовать в transform, false = crop запрещён.
	Crop *bool
	// Trim — требование к trim (nil = не задано/неважно). true = trim
	// обязан присутствовать в transform, false = trim запрещён.
	Trim *bool
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
	Global       GlobalPolicy
	PathPolicies []PathPolicy
}

// normalizePath нормализует префикс пути:
//   - добавляет ведущий "/", если отсутствует;
//   - убирает завершающий "/" (кроме случая, когда путь — ровно "/").
//
// Примеры: "basket/products" → "/basket/products",
// "/basket/users/" → "/basket/users", "/" → "/". Пустая строка остаётся
// пустой (невалидный префикс).
func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

// pathIndex возвращает индекс path-policy для заданного пути по правилу
// longest prefix match или -1, если ни одна не совпала.
//
// Путь запроса (req.Path()) — канонический путь без ведущего "/" (например
// "users", "basket/products"), а префиксы path-policy нормализуются с
// ведущим "/" (например "/users"). Совпадение по сегментам: путь совпадает
// с префиксом, если путь == префикс (без ведущего "/") ИЛИ путь начинается
// с префикс + "/". Префикс "/" — fallback, совпадает с любым путём и
// применяется, когда нет более специфичного совпадения.
func (p *Policy) pathIndex(path string) int {
	best := -1
	bestLen := -1
	for i, pp := range p.PathPolicies {
		if pp.Path == "" {
			continue
		}
		if pp.Path == "/" {
			// "/" — fallback: совпадает с любым путём, но проигрывает
			// любому более специфичному префиксу (длина 1).
			if bestLen < 1 {
				best = i
				bestLen = 1
			}
			continue
		}
		prefix := strings.TrimPrefix(pp.Path, "/")
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			if len(prefix) > bestLen {
				best = i
				bestLen = len(prefix)
			}
		}
	}
	return best
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
	// ReasonDPRNotAllowed — DPR не попадает в диапазон path-policy.
	ReasonDPRNotAllowed DecisionReason = "dpr_not_allowed"
	// ReasonCropNotAllowed — crop не соответствует требованию path-policy.
	ReasonCropNotAllowed DecisionReason = "crop_not_allowed"
	// ReasonTrimNotAllowed — trim не соответствует требованию path-policy.
	ReasonTrimNotAllowed DecisionReason = "trim_not_allowed"
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
//
// Порядок проверок:
//  1. Глобальная логика: preset → allowed-presets; канонический → size-rules
//     (unsafe пропускает эти проверки).
//  2. Для канонических запросов применяется path-policy как дополнительное
//     ограничение (dpr/crop/trim). Path-policy не расширяет права — она
//     применяется только после того, как запрос разрешён глобальной
//     политикой, и может только ужесточить решение.
func (p *Policy) Authorize(req *asset.Request) Decision {
	if req == nil {
		return Decision{Allowed: false, Reason: ReasonNilRequest}
	}
	g := p.Global

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

	// Канонический запрос: глобальная политика (unsafe пропускает size-rules).
	if g.Authorization != AuthUnsafe {
		if len(g.SizeRules) == 0 {
			return Decision{Allowed: false, Reason: ReasonDenyByDefault}
		}
		matched := false
		for _, rule := range g.SizeRules {
			if rule.Matches(req.Size()) {
				matched = true
				break
			}
		}
		if !matched {
			return Decision{
				Allowed: false,
				Reason:  ReasonSizeNotAllowed,
				Detail:  fmt.Sprintf("size %s is not allowed by any rule", req.Size().String()),
			}
		}
	}

	// Глобальная политика разрешила канонический запрос. Применяем
	// path-policy как дополнительное ограничение (не расширяет права).
	if d := p.authorizePath(req); !d.Allowed {
		return d
	}
	return Decision{Allowed: true, Reason: ReasonAllowed}
}

// authorizePath применяет path-policy к каноническому запросу.
//
// Если path-policies не настроены или ни одна не совпала (нет "/" и нет
// совпадений) — запрос разрешается без ограничений: path-policy опциональна,
// а "/" обычно задаётся как fallback.
func (p *Policy) authorizePath(req *asset.Request) Decision {
	idx := p.pathIndex(req.Path())
	if idx < 0 {
		return Decision{Allowed: true, Reason: ReasonAllowed}
	}
	pp := p.PathPolicies[idx]

	if pp.DPR != nil {
		if !pp.DPR.Contains(req.DPR().Int()) {
			return Decision{
				Allowed: false,
				Reason:  ReasonDPRNotAllowed,
				Detail:  fmt.Sprintf("dpr %d is not allowed for path %q (allowed %d-%d)", req.DPR().Int(), pp.Path, pp.DPR.Min, pp.DPR.Max),
			}
		}
	}
	if pp.Crop != nil {
		hasCrop := req.Transform() == asset.TransformCrop || req.Transform() == asset.TransformCropTrim
		if hasCrop != *pp.Crop {
			return Decision{
				Allowed: false,
				Reason:  ReasonCropNotAllowed,
				Detail:  fmt.Sprintf("crop=%v is not allowed for path %q (required %v)", hasCrop, pp.Path, *pp.Crop),
			}
		}
	}
	if pp.Trim != nil {
		hasTrim := req.Transform() == asset.TransformTrim || req.Transform() == asset.TransformCropTrim
		if hasTrim != *pp.Trim {
			return Decision{
				Allowed: false,
				Reason:  ReasonTrimNotAllowed,
				Detail:  fmt.Sprintf("trim=%v is not allowed for path %q (required %v)", hasTrim, pp.Path, *pp.Trim),
			}
		}
	}
	return Decision{Allowed: true, Reason: ReasonAllowed}
}

// CheckLimits проверяет фактические значения против лимитов политики.
// Лимиты задаются только глобально (path-policy не содержит лимитов).
func (p *Policy) CheckLimits(path string, sourceBytes int64, width, height, dpr, frames int, outputBytes, duration int64) CheckResult {
	return p.Global.Limits.Check(sourceBytes, width, height, dpr, frames, outputBytes, duration)
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
	for i, pp := range p.PathPolicies {
		norm := normalizePath(pp.Path)
		if norm == "" {
			return fmt.Errorf("path-policy %d: empty path", i)
		}
		if seen[norm] {
			return fmt.Errorf("duplicate path-policy %q", norm)
		}
		seen[norm] = true
		if pp.DPR != nil {
			if err := validateDPRRange(*pp.DPR); err != nil {
				return fmt.Errorf("path-policy %q dpr: %w", norm, err)
			}
		}
	}
	return nil
}

// PathNames возвращает отсортированный список имён path-policy.
func (p *Policy) PathNames() []string {
	names := make([]string, 0, len(p.PathPolicies))
	for _, pp := range p.PathPolicies {
		names = append(names, pp.Path)
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
