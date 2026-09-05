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
// Новая грамматика (transform-коды отсутствуют):
//
//	/{path}/{source_name}-{source_format}/{segment}@{dpr}.{output_format}
//
// segment — имя пресета ИЛИ custom-имя (размер-грамматика "x", "x200",
// "200x", "200x200"), опционально с @dpr-суффиксом. Ведущий "/" необязателен:
// URL принимается как с ним, так и без него.
//
// @dpr в URL необязателен: отсутствие означает dprURL=0 (не задан), явные
// 0 и 1 отклоняются, допустимы только 2 и 3. Имя сегмента может содержать
// фиксированный @dpr-суффикс (например "banner@2" или "200x100@2"): правило
// отделения — последний "@" в сегментной части URL всегда является @dpr URL,
// а имя сегмента — всё до него. Двойной "@" в сегментной части (например
// "banner@2@3") отклоняется.
//
// Разбор выполняется строго от конца URL. Перед разбором выполняется
// безопасная canonicalization: запрещены traversal-сегменты, encoded
// разделители ("%2f", "%2F"), control-символы, а также ограничены длина
// и набор символов.
// SourceRef — ссылка на исходник, извлечённая из произвольного URL.
//
// Используется для source fallback: когда полный Parse не удался (неверный
// preset, неканонический URL, недопустимый план), но исходник можно надёжно
// выделить из URL, сервис может отдать исходный файл вместо пикселя/ошибки.
type SourceRef struct {
	Path         string
	SourceName   string
	SourceFormat string // расширение без точки, lowercase
}

// SourceFileName возвращает имя файла исходника, включая расширение.
func (s *SourceRef) SourceFileName() string {
	if s == nil {
		return ""
	}
	if s.SourceFormat != "" {
		return s.SourceName + "." + s.SourceFormat
	}
	return s.SourceName
}

// Key возвращает канонический ключ исходника в хранилище: "path/name.ext"
// либо просто "name.ext", если путь пуст.
func (s *SourceRef) Key() string {
	if s == nil {
		return ""
	}
	file := s.SourceFileName()
	if s.Path == "" {
		return file
	}
	return s.Path + "/" + file
}

// ExtractSourceBestEffort пытается извлечь path/sourceName/sourceFormat из
// произвольного URL даже если полный Parse не удался. Возвращает nil, если
// извлечь безопасно нельзя.
//
// Используется тот же разбор «от конца», что в Parse (последняя точка →
// output format; последний "/" → rest; последний "-" → name-format), но БЕЗ
// строгих проверок формата/размера/пресета. Обязательны только непустые
// name/format и прохождение существующих проверок безопасности пути
// (rejectUnsafe + CanonicalPath).
func ExtractSourceBestEffort(raw string) *SourceRef {
	if raw == "" {
		return nil
	}
	if len(raw) > MaxURLLen {
		return nil
	}
	if err := rejectUnsafe(raw); err != nil {
		return nil
	}

	rest := strings.TrimPrefix(raw, "/")

	// Последняя точка → output format (отбрасываем, нам нужен core).
	lastDot := strings.LastIndex(rest, ".")
	if lastDot < 0 {
		return nil
	}
	if rest[lastDot+1:] == "" {
		return nil
	}
	core := rest[:lastDot]

	// core = {path}/{source_name}-{source_format}/{rest}.
	// Последний "/" отделяет rest (segment) от
	// {path}/{source_name}-{source_format}.
	lastSlash := strings.LastIndex(core, "/")
	if lastSlash < 0 {
		return nil
	}
	head := core[:lastSlash]
	restPart := core[lastSlash+1:]
	if restPart == "" {
		return nil
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
		return nil
	}
	sourceName := sourcePart[:dash]
	sourceFormat := sourcePart[dash+1:]
	if sourceName == "" || sourceFormat == "" {
		return nil
	}

	// Валидируем sourceName через NewSourceName (те же проверки безопасности,
	// что в Parse): длина, управляющие символы, разделители пути, "..".
	// Дополнительно запрещаем '%' в имени исходника — encoded-обходы.
	if _, err := NewSourceName(sourceName); err != nil {
		return nil
	}
	if strings.Contains(sourceName, "%") {
		return nil
	}

	// Канонизируем путь (те же проверки безопасности, что в Parse).
	canon, err := NewCanonicalizer().CanonicalPath(path)
	if err != nil {
		return nil
	}

	return &SourceRef{
		Path:         canon,
		SourceName:   sourceName,
		SourceFormat: strings.ToLower(sourceFormat),
	}
}

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

	// core = {path}/{source_name}-{source_format}/{segment}.
	// Последний "/" отделяет segment от {path}/{source_name}-{source_format}.
	lastSlash := strings.LastIndex(core, "/")
	if lastSlash < 0 {
		return nil, parseErr(raw, "missing '/' separator after source format", "")
	}
	head := core[:lastSlash]
	restPart := core[lastSlash+1:]
	if restPart == "" {
		return nil, parseErr(raw, "missing segment name", "")
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

	// Отделяем @dpr URL в restPart: последний "@" — всегда @dpr URL, имя
	// сегмента — всё до него. Двойной "@" в сегментной части отклоняется.
	dprURL := DPR(0)
	segment := restPart
	if atIdx := strings.LastIndex(restPart, "@"); atIdx >= 0 {
		dprStr := restPart[atIdx+1:]
		if dprStr == "" {
			return nil, parseErr(raw, "empty dpr", "@")
		}
		v, err := parseURLDPR(dprStr)
		if err != nil {
			return nil, parseErr(raw, err.Error(), "@"+dprStr)
		}
		dprURL = v
		segment = restPart[:atIdx]
		if strings.Contains(segment, "@") {
			return nil, parseErr(raw, "segment contains multiple '@'", segment)
		}
	}
	if segment == "" {
		return nil, parseErr(raw, "missing segment name", "")
	}

	seg, err := NewSegmentName(segment)
	if err != nil {
		return nil, parseErr(raw, "invalid segment name: "+err.Error(), segment)
	}
	return NewSegmentRequest(canon, sourceName, sourceFormat, seg, dprURL, outputFormat)
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

// ParseSize разбирает строку размера в Size.
//
// Поддерживаемые формы:
//
//	120x80    — точная ширина и высота
//	x50       — только высота
//	180x      — только ширина
//	x         — сохранение исходного размера
//
// Используется для custom-имён (размер-грамматика) и в тестах.
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

// RejectUnsafe — экспортированная обёртка rejectUnsafe. Используется
// адаптерами (например, httpapi source fallback) для тех же проверок
// безопасности URL, что и Parse.
func RejectUnsafe(raw string) error {
	return rejectUnsafe(raw)
}

// rejectUnsafe отклоняет URL, содержащие traversal-сегменты, encoded
// разделители, control-символы, обратные слеши, NUL-последовательности
// ("%00") и любые %XX-последовательности, декодирующиеся в control-символ.
func rejectUnsafe(raw string) error {
	if strings.Contains(raw, "..") {
		return fmt.Errorf("url contains traversal segment")
	}
	if strings.Contains(raw, "\\") {
		return fmt.Errorf("url contains backslash")
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "%2f") {
		return fmt.Errorf("url contains encoded path separator")
	}
	// "%00" и любые %XX, где XX — hex-код control-символа (< 0x20 или 0x7F):
	// encoded-обходы, которые после декодирования дают управляющие байты.
	for i := 0; i+2 < len(lower); i++ {
		if lower[i] != '%' {
			continue
		}
		hi, ok1 := hexVal(lower[i+1])
		lo, ok2 := hexVal(lower[i+2])
		if !ok1 || !ok2 {
			continue
		}
		v := hi<<4 | lo
		if v < 0x20 || v == 0x7f {
			return fmt.Errorf("url contains encoded control character")
		}
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("url contains control character")
		}
	}
	return nil
}

// hexVal возвращает значение hex-цифры (0-15) и признак валидности.
func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	default:
		return 0, false
	}
}
