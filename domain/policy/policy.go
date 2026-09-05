package policy

import (
	"fmt"
	"sort"
	"strings"

	"gitverse.ru/pkg-ru/imager/domain/asset"
)

// PathPolicy — скомпилированная path-policy для конкретного префикса пути.
//
// Path — нормализованный префикс пути (например "/", "/users",
// "/basket/products"). "/" — fallback, применяется ко всем путям, если нет
// более специфичного совпадения.
type PathPolicy struct {
	// Path — нормализованный префикс пути.
	Path string
	// Presets — набор пресетов, доступных на этом пути (подмножество
	// глобальных по именам из конфигурации).
	Presets *asset.PresetSet
	// Customs — custom-настройки пути: имя (размер-грамматика, опционально
	// с @dpr) → скомпилированный пресет.
	Customs map[string]*asset.Preset
}

// Policy — deny-by-default скомпилированная политика.
//
// Политика неизменяема после компиляции. По умолчанию (нет path-policies)
// все запросы отклоняются (deny-by-default).
type Policy struct {
	paths        map[string]*PathPolicy
	orderedPaths []string
	presets      *asset.PresetSet
}

// Presets возвращает глобальный набор пресетов (для adminsvc/перечисления).
func (p *Policy) Presets() *asset.PresetSet {
	if p == nil {
		return nil
	}
	return p.presets
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

// matchPath возвращает path-policy для заданного пути по правилу longest
// prefix match или nil, если ни одна не совпала.
//
// Путь запроса (req.Path()) — канонический путь без ведущего "/" (например
// "users", "basket/products"), а префиксы path-policy нормализуются с
// ведущим "/" (например "/users"). Совпадение по сегментам: путь совпадает
// с префиксом, если путь == префикс (без ведущего "/") ИЛИ путь начинается
// с префикс + "/". Префикс "/" — fallback, совпадает с любым путём и
// применяется, когда нет более специфичного совпадения.
func (p *Policy) matchPath(path string) *PathPolicy {
	if p == nil {
		return nil
	}
	best := ""
	bestLen := -1
	for _, prefix := range p.orderedPaths {
		if prefix == "" {
			continue
		}
		if prefix == "/" {
			// "/" — fallback: совпадает с любым путём, но проигрывает
			// любому более специфичному префиксу (длина 1).
			if bestLen < 1 {
				best = prefix
				bestLen = 1
			}
			continue
		}
		trimmed := strings.TrimPrefix(prefix, "/")
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			if len(trimmed) > bestLen {
				best = prefix
				bestLen = len(trimmed)
			}
		}
	}
	if best == "" {
		return nil
	}
	return p.paths[best]
}

// MatchPath возвращает path-policy для заданного пути по правилу longest
// prefix match или nil, если ни одна не совпала. Используется use case'ом
// для определения ватермарки канонического запроса (watermark path-policy).
func (p *Policy) MatchPath(path string) *PathPolicy {
	return p.matchPath(path)
}

// DecisionReason — причина решения политики.
type DecisionReason string

const (
	// ReasonAllowed — запрос разрешён.
	ReasonAllowed DecisionReason = "allowed"
	// ReasonDenyByDefault — запрос отклонён по умолчанию (нет правил).
	ReasonDenyByDefault DecisionReason = "deny_by_default"
	// ReasonPathNotAllowed — для пути нет path-policy (нет "/" и нет
	// совпадений).
	ReasonPathNotAllowed DecisionReason = "path_not_allowed"
	// ReasonSegmentNotAllowed — сегмент (пресет/custom) не найден в
	// path-policy.
	ReasonSegmentNotAllowed DecisionReason = "segment_not_allowed"
	// ReasonDPRNotAllowed — @dpr-суффикс URL не соответствует настройкам
	// пресета/custom.
	ReasonDPRNotAllowed DecisionReason = "dpr_not_allowed"
	// ReasonFormatNotAllowed — output format не входит в whitelist
	// пресета/custom.
	ReasonFormatNotAllowed DecisionReason = "format_not_allowed"
	// ReasonNilRequest — запрос nil.
	ReasonNilRequest DecisionReason = "nil_request"
)

// Decision — результат авторизации запроса.
type Decision struct {
	// Allowed — true, если запрос разрешён.
	Allowed bool
	// Reason — причина решения.
	Reason DecisionReason
	// Detail — дополнительная информация.
	Detail string
}

// ResolveError описывает ошибку разрешения segment URL в настройки
// пресета/custom.
type ResolveError struct {
	SegmentName string
	Reason      string
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("cannot resolve segment %q: %s", e.SegmentName, e.Reason)
}

// Resolve превращает segment Request в канонический Request с применёнными
// настройками пресета/custom.
//
// Алгоритм (см. ТЗ «РЕЗОЛВ»):
//  1. Найти path-policy: longest-prefix match. Нет "/" и нет совпадений →
//     deny (ReasonPathNotAllowed).
//  2. Декомпозиция segment (без @dpr URL):
//     a. СНАЧАЛА точное совпадение полного имени segment@dpr в customs
//     (если в URL есть @dpr). Если найден → используем, dpr из его имени
//     либо из настроек (если задан и совпадает; иначе deny).
//     b. ЕСЛИ не найден и URL имеет @dpr: ищем базовое имя в customs.
//     Разрешено ТОЛЬКО если у custom dpr НЕ установлен. Если установлен →
//     deny. Иначе применяем custom, dpr = dprURL.
//     c. Если URL без @dpr: ищем segment в customs. dpr = из настроек
//     (если установлен 2/3), иначе 1. Точный custom с @dpr в имени НЕ
//     матчится URL без @dpr.
//  3. Если custom не найден — тот же алгоритм в presets пути. Приоритет:
//     customs НАД presets.
//  4. Если ничего не найдено → deny (ReasonSegmentNotAllowed).
//  5. Output format должен входить в whitelist пресета/custom, иначе deny
//     (ReasonFormatNotAllowed).
//
// Возвращает (nil, decision) при deny и (resolved, allowed-decision) при
// успехе.
func (p *Policy) Resolve(req *asset.Request) (*asset.Request, Decision) {
	if req == nil {
		return nil, Decision{Allowed: false, Reason: ReasonNilRequest}
	}
	if !req.IsPreset() {
		return nil, Decision{Allowed: false, Reason: ReasonSegmentNotAllowed, Detail: "request is not a segment url"}
	}
	if req.IsResolved() {
		return req, Decision{Allowed: true, Reason: ReasonAllowed}
	}

	pp := p.matchPath(req.Path())
	if pp == nil {
		return nil, Decision{
			Allowed: false,
			Reason:  ReasonPathNotAllowed,
			Detail:  fmt.Sprintf("no path-policy matches path %q", req.Path()),
		}
	}

	segment := req.SegmentName().String()
	dprURL := req.DPR() // 0 = @dpr в URL отсутствует

	// 1) Точное совпадение полного имени (segment@dpr) в customs.
	if dprURL != 0 {
		if c, ok := pp.Customs[segment+"@"+fmt.Sprintf("%d", dprURL.Int())]; ok {
			return p.applyPreset(req, c, segment+"@"+fmt.Sprintf("%d", dprURL.Int()), dprURL)
		}
	}
	// 2) Базовое имя в customs (wildcard-dpr).
	if c, ok := pp.Customs[segment]; ok {
		if dprURL != 0 {
			// URL с @dpr: разрешено ТОЛЬКО если у custom dpr НЕ установлен.
			if c.DPRSet() {
				return nil, Decision{
					Allowed: false,
					Reason:  ReasonDPRNotAllowed,
					Detail:  fmt.Sprintf("dpr suffix @%d is not allowed for custom %q (dpr is set in config)", dprURL.Int(), segment),
				}
			}
			return p.applyPreset(req, c, segment, dprURL)
		}
		// URL без @dpr: dpr из настроек custom (если установлен 2/3), иначе 1.
		dpr := asset.DPR(asset.DefaultDPR)
		if c.DPR() != 0 {
			dpr = c.DPR()
		}
		return p.applyPreset(req, c, segment, dpr)
	}

	// 2b) Custom с @dpr в имени: базовое имя равно segment, но в URL другой
	//     dpr — конфликт.
	if dprURL != 0 {
		for cname, c := range pp.Customs {
			base, nameDPR, err := asset.SplitPresetNameDPR(cname)
			if err != nil || base != segment || nameDPR == 0 {
				continue
			}
			if nameDPR != dprURL {
				return nil, Decision{
					Allowed: false,
					Reason:  ReasonDPRNotAllowed,
					Detail:  fmt.Sprintf("dpr %d conflicts with dpr %d of custom %q", dprURL.Int(), nameDPR.Int(), cname),
				}
			}
			// nameDPR == dprURL: обрабатывается шагом 1 (точное полное имя).
			_ = c
			break
		}
	}

	// 3) Тот же алгоритм в presets пути.
	if dprURL != 0 {
		if pr, ok := pp.Presets.Get(segment + "@" + fmt.Sprintf("%d", dprURL.Int())); ok {
			return p.applyPreset(req, pr, segment+"@"+fmt.Sprintf("%d", dprURL.Int()), dprURL)
		}
	}
	if pr, ok := pp.Presets.Get(segment); ok {
		if dprURL != 0 {
			if pr.DPRSet() {
				return nil, Decision{
					Allowed: false,
					Reason:  ReasonDPRNotAllowed,
					Detail:  fmt.Sprintf("dpr suffix @%d is not allowed for preset %q (dpr is set in config)", dprURL.Int(), segment),
				}
			}
			return p.applyPreset(req, pr, segment, dprURL)
		}
		dpr := asset.DPR(asset.DefaultDPR)
		if pr.DPR() != 0 {
			dpr = pr.DPR()
		}
		return p.applyPreset(req, pr, segment, dpr)
	}

	// 3b) Preset с @dpr в имени: базовое имя равно segment, но в URL другой
	//     dpr — конфликт.
	if dprURL != 0 {
		for _, pr := range pp.Presets.Names() {
			base, nameDPR, err := asset.SplitPresetNameDPR(pr)
			if err != nil || base != segment || nameDPR == 0 {
				continue
			}
			if nameDPR != dprURL {
				return nil, Decision{
					Allowed: false,
					Reason:  ReasonDPRNotAllowed,
					Detail:  fmt.Sprintf("dpr %d conflicts with dpr %d of preset %q", dprURL.Int(), nameDPR.Int(), pr),
				}
			}
			break
		}
	}

	return nil, Decision{
		Allowed: false,
		Reason:  ReasonSegmentNotAllowed,
		Detail:  fmt.Sprintf("segment %q is not allowed for path %q", segment, req.Path()),
	}
}

// applyPreset применяет настройки пресета/custom к запросу.
//
// dpr — итоговый DPR (из имени пресета, настроек или URL). Проверяются:
//   - конфликт dpr: если пресет имеет фиксированный dpr (поле или @dpr в
//     имени), он должен совпадать с dpr;
//   - output format в whitelist.
func (p *Policy) applyPreset(req *asset.Request, pr *asset.Preset, segment string, dpr asset.DPR) (*asset.Request, Decision) {
	// DPR из имени пресета (например "banner@2" → 2) имеет приоритет над
	// dpr из URL/настроек, если поле dpr не задано.
	fixed := pr.DPR()
	if fixed == 0 {
		if _, nameDPR, err := asset.SplitPresetNameDPR(pr.Name()); err == nil && nameDPR != 0 {
			fixed = nameDPR
		}
	}
	if fixed != 0 && dpr != fixed {
		return nil, Decision{
			Allowed: false,
			Reason:  ReasonDPRNotAllowed,
			Detail: fmt.Sprintf(
				"dpr %d conflicts with preset %q dpr %d", dpr.Int(), pr.Name(), fixed.Int(),
			),
		}
	}
	if fixed != 0 {
		dpr = fixed
	}
	if dpr == 0 {
		dpr = asset.DefaultDPR
	}

	if !pr.AllowsOutputFormat(req.OutputFormats()) {
		return nil, Decision{
			Allowed: false,
			Reason:  ReasonFormatNotAllowed,
			Detail: fmt.Sprintf(
				"output format %q is not allowed for %q", req.OutputFormats(), pr.Name(),
			),
		}
	}

	resolved := req.WithResolved(
		pr.Transform(),
		pr.Size(),
		dpr,
		pr.Quality(),
		pr.Frames(),
		pr.Duration(),
		pr.Loop(),
		pr.Watermark(),
		pr.Orientation(),
	)
	return resolved, Decision{Allowed: true, Reason: ReasonAllowed}
}

// Authorize проверяет, разрешён ли запрос согласно политике.
//
// Политика deny-by-default. Для segment-запросов сначала выполняется
// Resolve (применение настроек пресета/custom), затем проверка результата.
// Для уже разрешённых запросов (IsResolved) проверяется только соответствие
// path-policy (сегмент должен быть разрешён на пути).
//
// Порядок проверок:
//  1. nil → ReasonNilRequest.
//  2. segment-запрос → Resolve (путь, сегмент, dpr, формат).
//  3. разрешённый запрос → path-policy: сегмент должен быть в customs или
//     presets пути.
func (p *Policy) Authorize(req *asset.Request) Decision {
	if req == nil {
		return Decision{Allowed: false, Reason: ReasonNilRequest}
	}
	if req.IsPreset() && !req.IsResolved() {
		_, d := p.Resolve(req)
		return d
	}
	// Разрешённый (или канонический) запрос: проверяем, что сегмент
	// разрешён на пути.
	pp := p.matchPath(req.Path())
	if pp == nil {
		return Decision{
			Allowed: false,
			Reason:  ReasonPathNotAllowed,
			Detail:  fmt.Sprintf("no path-policy matches path %q", req.Path()),
		}
	}
	if req.IsPreset() {
		segment := req.SegmentName().String()
		if _, ok := pp.Customs[segment]; ok {
			return Decision{Allowed: true, Reason: ReasonAllowed}
		}
		if _, ok := pp.Presets.Get(segment); ok {
			return Decision{Allowed: true, Reason: ReasonAllowed}
		}
		return Decision{
			Allowed: false,
			Reason:  ReasonSegmentNotAllowed,
			Detail:  fmt.Sprintf("segment %q is not allowed for path %q", segment, req.Path()),
		}
	}
	// Канонический запрос (программный, без segment): разрешён, если
	// path-policy существует.
	return Decision{Allowed: true, Reason: ReasonAllowed}
}

// Validate проверяет корректность скомпилированной политики.
func (p *Policy) Validate() error {
	if p == nil {
		return fmt.Errorf("policy is nil")
	}
	seen := map[string]bool{}
	for _, pp := range p.paths {
		norm := normalizePath(pp.Path)
		if norm == "" {
			return fmt.Errorf("path-policy: empty path")
		}
		if seen[norm] {
			return fmt.Errorf("duplicate path-policy %q", norm)
		}
		seen[norm] = true
	}
	return nil
}

// PathNames возвращает отсортированный список имён path-policy.
func (p *Policy) PathNames() []string {
	if p == nil {
		return nil
	}
	names := make([]string, 0, len(p.paths))
	for k := range p.paths {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
