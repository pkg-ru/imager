package asset

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseError описывает ошибку разбора asset URL с указанием причины.
type ParseError struct {
	URL     string
	Reason  string
	Segment string
}

func (e *ParseError) Error() string {
	if e.Segment != "" {
		return fmt.Sprintf("invalid asset url %q: %s (segment %q)", e.URL, e.Reason, e.Segment)
	}
	return fmt.Sprintf("invalid asset url %q: %s", e.URL, e.Reason)
}

func parseErr(url, reason, segment string) error {
	return &ParseError{URL: url, Reason: reason, Segment: segment}
}

// Parse разбирает asset URL в immutable Request.
//
// Поддерживаются канонический и preset форматы:
//
//	/{path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}
//	/{path}/{source_name}-{source_format}/{preset_name}@{dpr}.{output_format}
//
// Ведущий "/" необязателен: URL принимается как с ним, так и без него.
//
// transform — необязателен. size обязателен; "x" означает сохранение
// исходного размера. @dpr необязателен: отсутствие означает 1, явные 0 и 1
// отклоняются, допустимы только 2 и 3.
//
// Имя пресета может содержать фиксированный @dpr-суффикс (например
// "thumb@2"). Правило отделения @dpr-суффикса имени от @dpr-суффикса URL:
//
//   - канонический URL (transform-size или size): последний "@" — это
//     @dpr URL;
//   - preset URL с ровно одним "@": это @dpr имени пресета (имя
//     распознаётся целиком, dpr URL = 1);
//   - preset URL с двумя "@" (например "thumb@2@3"): последний "@" — это
//     @dpr URL, а имя пресета — всё до него ("thumb@2").
//
// Разбор выполняется строго от конца URL. Перед разбором выполняется
// безопасная canonicalization: запрещены traversal-сегменты, encoded
// разделители ("%2f", "%2F"), control-символы, а также ограничены длина
// и набор символов.
func Parse(raw string) (*Request, error) {
	if raw == "" {
		return nil, parseErr(raw, "empty url", "")
	}
	if len(raw) > MaxURLLen {
		return nil, parseErr(raw, fmt.Sprintf("url length %d exceeds maximum %d", len(raw), MaxURLLen), "")
	}
	if err := rejectUnsafe(raw); err != nil {
		return nil, parseErr(raw, err.Error(), "")
	}

	// Срезаем ведущий "/" (необязателен).
	rest := strings.TrimPrefix(raw, "/")

	// Отделяем output_format (последний сегмент после последней точки).
	lastDot := strings.LastIndex(rest, ".")
	if lastDot < 0 {
		return nil, parseErr(raw, "missing output format", "")
	}
	outputFormatStr := rest[lastDot+1:]
	if outputFormatStr == "" {
		return nil, parseErr(raw, "empty output format", "")
	}
	outputFormat, err := NewFormat(outputFormatStr)
	if err != nil {
		return nil, parseErr(raw, "invalid output format: "+err.Error(), outputFormatStr)
	}
	core := rest[:lastDot]

	// core = {path}/{source_name}-{source_format}/{rest}.
	// Последний "/" отделяет rest (transform-size / size / preset_name) от
	// {path}/{source_name}-{source_format}.
	lastSlash := strings.LastIndex(core, "/")
	if lastSlash < 0 {
		return nil, parseErr(raw, "missing '/' separator after source format", "")
	}
	head := core[:lastSlash]
	restPart := core[lastSlash+1:]
	if restPart == "" {
		return nil, parseErr(raw, "missing size or preset name", "")
	}

	// В head отделяем path от source_name-source_format по последнему "/".
	path := ""
	sourcePart := head
	if s := strings.LastIndex(head, "/"); s >= 0 {
		path = head[:s]
		sourcePart = head[s+1:]
	}

	// sourcePart = source_name-source_format.
	dash := strings.LastIndex(sourcePart, "-")
	if dash < 0 {
		return nil, parseErr(raw, "missing source format", "")
	}
	sourceNameStr := sourcePart[:dash]
	sourceFormatStr := sourcePart[dash+1:]
	if sourceNameStr == "" {
		return nil, parseErr(raw, "empty source name", "")
	}
	if sourceFormatStr == "" {
		return nil, parseErr(raw, "empty source format", "")
	}

	// Канонизируем путь.
	canon, err := NewCanonicalizer().CanonicalPath(path)
	if err != nil {
		return nil, parseErr(raw, err.Error(), path)
	}

	sourceName, err := NewSourceName(sourceNameStr)
	if err != nil {
		return nil, parseErr(raw, "invalid source name: "+err.Error(), sourceNameStr)
	}
	sourceFormat, err := NewFormat(sourceFormatStr)
	if err != nil {
		return nil, parseErr(raw, "invalid source format: "+err.Error(), sourceFormatStr)
	}

	// Отделяем @dpr в restPart.
	dpr := DPR(DefaultDPR)
	base := restPart
	if atIdx := strings.LastIndex(restPart, "@"); atIdx >= 0 {
		dprStr := restPart[atIdx+1:]
		if dprStr == "" {
			return nil, parseErr(raw, "empty dpr", "@")
		}
		prefix := restPart[:atIdx]
		// Канонический URL (transform-size или size): @dpr — dpr URL.
		if _, _, ok := matchTransformPrefix(prefix); ok || strings.Contains(prefix, "x") {
			v, err := parseURLDPR(dprStr)
			if err != nil {
				return nil, parseErr(raw, err.Error(), "@"+dprStr)
			}
			dpr = v
			base = prefix
		} else if strings.Contains(prefix, "@") {
			// Preset с двумя "@": последний — dpr URL, имя — всё до него.
			v, err := parseURLDPR(dprStr)
			if err != nil {
				return nil, parseErr(raw, err.Error(), "@"+dprStr)
			}
			dpr = v
			base = prefix
		} else {
			// Preset с ровно одним "@": это @dpr имени пресета. Имя
			// распознаётся целиком, dpr URL = 1 (default).
			base = restPart
		}
	}

	// Разбираем base: transform-size / size / preset_name.
	if tr, sz, ok := matchTransformPrefix(base); ok {
		// Канонический URL с transform.
		transform := Transform(tr)
		size, err := ParseSize(sz)
		if err != nil {
			return nil, parseErr(raw, "invalid size: "+err.Error(), sz)
		}
		if size.IsEmpty() {
			return nil, parseErr(raw, "size must not be empty", sz)
		}
		return NewRequest(canon, sourceName, sourceFormat, transform, size, dpr, outputFormat)
	}

	if looksLikeSize(base) {
		// Канонический URL без transform: base — size.
		size, err := ParseSize(base)
		if err != nil {
			return nil, parseErr(raw, "invalid size: "+err.Error(), base)
		}
		if size.IsEmpty() {
			return nil, parseErr(raw, "size must not be empty", base)
		}
		return NewRequest(canon, sourceName, sourceFormat, "", size, dpr, outputFormat)
	}

	// Preset: base — preset_name (может содержать @dpr-суффикс имени).
	presetName, err := NewPresetName(base)
	if err != nil {
		return nil, parseErr(raw, "invalid preset name: "+err.Error(), base)
	}
	// Валидируем @dpr-суффикс имени пресета (если есть): допустимы @1/@2/@3.
	if _, _, err := SplitPresetNameDPR(base); err != nil {
		return nil, parseErr(raw, "invalid preset name: "+err.Error(), base)
	}
	return NewPresetRequest(canon, sourceName, sourceFormat, presetName, dpr, outputFormat)
}

// parseURLDPR разбирает явный @dpr-суффикс URL. Явная передача 0 или 1 —
// ошибка; допустимы только 2 и 3.
func parseURLDPR(s string) (DPR, error) {
	if s == "" {
		return 0, fmt.Errorf("empty dpr")
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("dpr must be an integer")
	}
	if v < MinDPR || v > MaxDPR {
		return 0, fmt.Errorf("dpr must be in [%d,%d]", MinDPR, MaxDPR)
	}
	return DPR(v), nil
}

// matchTransformPrefix проверяет, начинается ли s с одного из transform-кодов
// ("sct-", "fct-", "oct-", "ct-", "sc-", "fc-", "oc-", "c-", "t-"), и
// возвращает transform и оставшийся size. Порядок важен: сначала более
// длинные префиксы ("sct"/"fct"/"oct" перед "sc"/"fc"/"oc", "ct" перед "c"),
// чтобы "sct-120x80" не разобрался как "sc" + "t-120x80", а
// "ct-120x80" — как "c" + "t-120x80".
func matchTransformPrefix(s string) (transform, size string, ok bool) {
	for _, code := range []string{"sct", "fct", "oct", "ct", "sc", "fc", "oc", "c", "t"} {
		marker := code + "-"
		if strings.HasPrefix(s, marker) {
			return code, s[len(marker):], true
		}
	}
	return "", "", false
}

// looksLikeSize сообщает, похожа ли строка на канонический размер
// ("120x80", "x50", "180x", "x"). Размер — это строго форма
// [цифры] 'x' [цифры]; всё остальное (например, имя пресета "max")
// размером не считается и разбирается как пресет.
func looksLikeSize(s string) bool {
	w, h, ok := strings.Cut(s, "x")
	if !ok {
		return false
	}
	if w == "" && h == "" {
		return true // "x" — исходный размер
	}
	return allDigits(w) && allDigits(h)
}

func allDigits(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParseSize разбирает строку размера в Size.
//
// Поддерживаемые формы:
//
//	120x80    — точная ширина и высота
//	x50       — только высота
//	180x      — только ширина
//	x         — сохранение исходного размера
func ParseSize(s string) (Size, error) {
	if s == "" {
		return Size{}, nil
	}
	before, after, ok := strings.Cut(s, "x")
	if !ok {
		return Size{}, fmt.Errorf("missing 'x' separator")
	}
	wStr := before
	hStr := after

	// Специальный размер "x": сохранить исходный размер.
	if wStr == "" && hStr == "" {
		return NewOriginalSize(), nil
	}

	var width, height *Dimension
	if wStr != "" {
		d, err := parseDimension(wStr)
		if err != nil {
			return Size{}, fmt.Errorf("invalid width: %w", err)
		}
		width = &d
	}
	if hStr != "" {
		d, err := parseDimension(hStr)
		if err != nil {
			return Size{}, fmt.Errorf("invalid height: %w", err)
		}
		height = &d
	}
	return NewSize(width, height)
}

func parseDimension(s string) (Dimension, error) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	return NewDimension(v)
}

// rejectUnsafe отклоняет URL, содержащие traversal-сегменты, encoded
// разделители, control-символы или недопустимые символы.
func rejectUnsafe(raw string) error {
	if strings.Contains(raw, "..") {
		return fmt.Errorf("url contains traversal segment")
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "%2f") {
		return fmt.Errorf("url contains encoded path separator")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("url contains control character")
		}
	}
	return nil
}
