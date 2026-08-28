package asset

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg-ru/imager/domain/processing"
)

// Preset — immutable именованный набор параметров обработки.
//
// Preset НЕ содержит source format: исходный формат определяется URL
// ({source_name}-{source_format}/{segment}.{output_format}) и передаётся
// при разрешении. OutputFormats — СПИСОК допустимых выходных форматов
// (whitelist): формат в URL обязан входить в список, иначе разрешение
// завершается ошибкой.
//
// Имя пресета может содержать фиксированный суффикс @dpr (например
// "banner@2"): при разрешении такого пресета dpr=2 применяется всегда.
// Поле dpr (если задано) имеет приоритет над @dpr в имени.
type Preset struct {
	name         string
	transform    Transform
	size         Size
	outputFormat []Format
	dpr          DPR
	// dprSet — true, если dpr задан ЯВНО в настройках (даже 0/1). Отличает
	// «ключ dpr отсутствует» от «dpr: 0»: при dprSet=true @dpr-суффикс в URL
	// запрещён (кроме случая, когда имя пресета содержит тот же @dpr).
	dprSet      bool
	quality     int
	frames      int
	duration    int
	loop        *bool
	watermark   *processing.WatermarkSpec
	orientation *processing.OrientationSpec
}

// NewPreset создаёт Preset с валидацией.
//
// dpr — фиксированный DPR пресета (0 = не задан, берётся из имени/URL).
// dprSet — true, если ключ dpr присутствовал в конфигурации (даже со
// значением 0/1): в этом случае @dpr-суффикс в URL запрещён.
// quality — качество сжатия (0 = default-quality из processing).
// frames — максимальное число кадров анимации (0 = без ограничения).
// duration — максимальная длительность анимации в мс (0 = без ограничения).
// loop — зацикливание анимации (nil = default-loop из processing).
func NewPreset(name string, transform Transform, size Size, outputFormat []Format, dpr DPR, dprSet bool, quality, frames, duration int, loop *bool) (*Preset, error) {
	if name == "" {
		return nil, fmt.Errorf("preset: empty name")
	}
	// Пустой transform допустим (означает resize); любые другие значения
	// проверяются.
	if transform != "" && !ValidTransform(transform) {
		return nil, fmt.Errorf("preset %q: invalid transform %q", name, transform)
	}
	if size.IsEmpty() {
		return nil, fmt.Errorf("preset %q: empty size", name)
	}
	if len(outputFormat) == 0 {
		return nil, fmt.Errorf("preset %q: empty output format list", name)
	}
	for _, f := range outputFormat {
		if f == "" {
			return nil, fmt.Errorf("preset %q: empty output format in list", name)
		}
	}
	// Если фиксированный dpr не задан (0), берём его из @dpr-суффикса имени
	// (например "banner@2" → dpr=2). Поле dpr имеет приоритет над именем.
	if dpr == 0 {
		if _, nameDPR, err := SplitPresetNameDPR(name); err != nil {
			return nil, fmt.Errorf("preset %q: %w", name, err)
		} else if nameDPR != 0 {
			dpr = nameDPR
		}
	}
	if dpr != 0 && !dpr.Valid() {
		return nil, fmt.Errorf("preset %q: dpr must be in [%d,%d], got %d", name, DefaultDPR, MaxDPR, dpr.Int())
	}
	if quality < 0 || quality > 100 {
		return nil, fmt.Errorf("preset %q: quality must be in [0,100], got %d", name, quality)
	}
	if frames < 0 {
		return nil, fmt.Errorf("preset %q: frames must be non-negative, got %d", name, frames)
	}
	if duration < 0 {
		return nil, fmt.Errorf("preset %q: duration must be non-negative, got %d", name, duration)
	}
	return &Preset{
		name:         name,
		transform:    transform,
		size:         size,
		outputFormat: append([]Format(nil), outputFormat...),
		dpr:          dpr,
		dprSet:       dprSet,
		quality:      quality,
		frames:       frames,
		duration:     duration,
		loop:         loop,
	}, nil
}

// Name возвращает имя пресета.
func (p *Preset) Name() string { return p.name }

// Watermark возвращает спецификацию ватермарки пресета (nil = не задана).
func (p *Preset) Watermark() *processing.WatermarkSpec { return p.watermark }

// Orientation возвращает спецификацию ориентации пресета (nil = не задана:
// используется глобальный дефолт processing.default-*).
func (p *Preset) Orientation() *processing.OrientationSpec { return p.orientation }

// WithOrientation возвращает копию пресета с привязанной спецификацией
// ориентации. Используется при компиляции конфигурации: значения
// auto-orient/rotate/flip пресета мержатся с глобальным дефолтом и
// подставляются готовой спецификацией.
func (p *Preset) WithOrientation(o *processing.OrientationSpec) *Preset {
	if p == nil {
		return nil
	}
	cp := *p
	cp.orientation = o
	return &cp
}

// WithWatermark возвращает копию пресета с привязанной спецификацией
// ватермарки. Используется при компиляции конфигурации: имя ватермарки из
// YAML резервируется в реестре и подставляется готовой спецификацией.
func (p *Preset) WithWatermark(wm *processing.WatermarkSpec) *Preset {
	if p == nil {
		return nil
	}
	cp := *p
	cp.watermark = wm
	return &cp
}

// Transform возвращает режим трансформации.
func (p *Preset) Transform() Transform { return p.transform }

// Size возвращает размер.
func (p *Preset) Size() Size { return p.size }

// OutputFormats возвращает список допустимых выходных форматов.
func (p *Preset) OutputFormats() []Format { return append([]Format(nil), p.outputFormat...) }

// AllowsOutputFormat сообщает, входит ли формат в список допустимых.
func (p *Preset) AllowsOutputFormat(f Format) bool {
	for _, of := range p.outputFormat {
		if of == f {
			return true
		}
	}
	return false
}

// DPR возвращает фиксированный DPR пресета (0 = не задан).
func (p *Preset) DPR() DPR { return p.dpr }

// DPRSet сообщает, задан ли dpr ЯВНО в настройках пресета (даже 0/1).
func (p *Preset) DPRSet() bool { return p.dprSet }

// Quality возвращает качество сжатия (0 = default-quality).
func (p *Preset) Quality() int { return p.quality }

// Frames возвращает максимальное число кадров (0 = без ограничения).
func (p *Preset) Frames() int { return p.frames }

// Duration возвращает максимальную длительность в мс (0 = без ограничения).
func (p *Preset) Duration() int { return p.duration }

// Loop возвращает зацикливание анимации (nil = default-loop).
func (p *Preset) Loop() *bool { return p.loop }

// PresetSet — неизменяемый набор пресетов.
type PresetSet struct {
	byName map[string]*Preset
}

// NewPresetSet создаёт набор пресетов и валидирует их.
func NewPresetSet(presets []*Preset) (*PresetSet, error) {
	byName := make(map[string]*Preset, len(presets))
	for _, p := range presets {
		if p == nil {
			return nil, fmt.Errorf("preset set: nil preset")
		}
		if _, dup := byName[p.name]; dup {
			return nil, fmt.Errorf("preset set: duplicate preset name %q", p.name)
		}
		byName[p.name] = p
	}
	return &PresetSet{byName: byName}, nil
}

// Get возвращает пресет по имени.
func (s *PresetSet) Get(name string) (*Preset, bool) {
	p, ok := s.byName[name]
	return p, ok
}

// Names возвращает отсортированный список имён пресетов.
func (s *PresetSet) Names() []string {
	names := make([]string, 0, len(s.byName))
	for n := range s.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ResolveError описывает ошибку разрешения segment URL в канонический запрос.
type ResolveError struct {
	SegmentName string
	// PresetName — алиас SegmentName (обратная совместимость с httpapi).
	PresetName string
	Reason     string
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("cannot resolve segment %q: %s", e.SegmentName, e.Reason)
}

// Resolve превращает segment Request в канонический Request.
//
// Source format берётся из URL (req.SourceFormat), а transform/size — из
// пресета. Output format в URL обязан входить в список допустимых форматов
// пресета, иначе возвращается ошибка.
//
// DPR определяется так:
//   - если пресет имеет фиксированный dpr (поле dpr или суффикс @dpr в
//     имени), он применяется; явный @dpr в URL, отличный от фиксированного,
//     — ошибка разрешения;
//   - иначе dpr берётся из URL (default 1).
//
// Параметры обработки пресета (quality/frames/duration/loop) пробрасываются
// в результирующий запрос через WithProcessingOptions.
func (s *PresetSet) Resolve(req *Request) (*Request, error) {
	if req == nil {
		return nil, &ResolveError{Reason: "nil request"}
	}
	if !req.IsPreset() {
		return nil, &ResolveError{Reason: "request is not a segment url"}
	}
	if req.sourceFormat == "" {
		return nil, &ResolveError{
			SegmentName: req.segmentName.String(),
			PresetName:  req.segmentName.String(),
			Reason:      "missing source format in url",
		}
	}
	if req.resolved {
		return nil, &ResolveError{
			SegmentName: req.segmentName.String(),
			PresetName:  req.segmentName.String(),
			Reason:      "request is already resolved",
		}
	}
	// Имя сегмента из URL. Парсер отделяет последний "@" как @dpr URL,
	// поэтому segmentName — базовое имя без @dpr (например "thumb" для
	// "thumb@2.webp"), а req.dpr — @dpr URL (0 = отсутствует).
	//
	// Поиск пресета:
	//  1. Точное имя segmentName ("thumb").
	//  2. Точное полное имя "segmentName@req.dpr" — пресет с фиксированным
	//     @dpr в имени (например "thumb@2" для URL "thumb@2.webp").
	//  3. Пресет с @dpr в имени, базовое имя которого == segmentName, но
	//     @dpr URL отличен — конфликт dpr.
	segmentName := req.segmentName.String()
	var p *Preset
	var nameDPR DPR
	var exactFull bool
	if pr, ok := s.byName[segmentName]; ok {
		p = pr
	} else if req.dpr != 0 {
		if pr, ok := s.byName[segmentName+"@"+strconv.Itoa(req.dpr.Int())]; ok {
			p = pr
			nameDPR = req.dpr
			exactFull = true
		}
	}
	if p == nil {
		for name, pr := range s.byName {
			base, suffix, err := SplitPresetNameDPR(name)
			if err != nil || base != segmentName || suffix == 0 {
				continue
			}
			// Пресет имеет фиксированный @dpr в имени.
			if req.dpr == suffix {
				p = pr
				nameDPR = suffix
			} else if req.dpr != 0 {
				// URL @dpr не совпадает с @dpr пресета — конфликт.
				return nil, &ResolveError{
					SegmentName: segmentName,
					PresetName:  segmentName,
					Reason: fmt.Sprintf(
						"dpr %d in url conflicts with preset dpr %d",
						req.dpr.Int(), suffix.Int(),
					),
				}
			}
			// req.dpr == 0 (URL без @dpr): пресет с @dpr в имени не матчится.
			break
		}
	}
	if p == nil {
		return nil, &ResolveError{
			SegmentName: segmentName,
			PresetName:  segmentName,
			Reason:      "preset not found",
		}
	}
	if !p.AllowsOutputFormat(req.outputFormat) {
		return nil, &ResolveError{
			SegmentName: segmentName,
			PresetName:  segmentName,
			Reason: fmt.Sprintf(
				"output format %q is not allowed (allowed: %s)",
				req.outputFormat, formatListString(p.outputFormat),
			),
		}
	}

	// DPR: фиксированный dpr пресета (поле dpr имеет приоритет над @dpr
	// имени). Для точного полного имени "@dpr URL" совпадает с @dpr имени:
	// конфликт невозможен, применяется фиксированное значение. Для остальных
	// случаев явный @dpr в URL, отличный от фиксированного, — ошибка. Если
	// фиксированного dpr нет, dpr берётся из URL (default 1).
	fixed := p.dpr
	if fixed == 0 {
		fixed = nameDPR
	}
	var dpr DPR
	if exactFull {
		dpr = fixed
		if dpr == 0 {
			dpr = DefaultDPR
		}
	} else {
		dpr = req.dpr
		if dpr == 0 {
			dpr = DefaultDPR
		}
		if fixed != 0 && dpr != fixed {
			return nil, &ResolveError{
				SegmentName: segmentName,
				PresetName:  segmentName,
				Reason: fmt.Sprintf(
					"dpr %d in url conflicts with preset dpr %d",
					dpr.Int(), fixed.Int(),
				),
			}
		}
		if fixed != 0 {
			dpr = fixed
		}
	}

	resolved, err := NewRequest(
		req.path,
		req.sourceName,
		req.sourceFormat,
		p.transform,
		p.size,
		dpr,
		req.outputFormat,
	)
	if err != nil {
		return nil, &ResolveError{
			SegmentName: req.segmentName.String(),
			PresetName:  req.segmentName.String(),
			Reason:      err.Error(),
		}
	}
	resolved = resolved.WithProcessingOptions(p.quality, p.frames, p.duration, p.loop, p.watermark)
	if p.orientation != nil {
		resolved = resolved.WithOrientation(p.orientation)
	}
	return resolved, nil
}

// formatListString форматирует список форматов для сообщений об ошибках.
func formatListString(formats []Format) string {
	parts := make([]string, 0, len(formats))
	for _, f := range formats {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, ", ")
}

// SplitPresetNameDPR отделяет фиксированный @dpr-суффикс от имени пресета.
//
// Возвращает имя без суффикса и DPR (0, если суффикса нет). Примеры:
//
//	"banner"   → ("banner", 0)
//	"banner@1" → ("banner", 1)   // dpr=1 эквивалентен отсутствию
//	"banner@2" → ("banner", 2)
//	"banner@3" → ("banner", 3)
//
// Суффикс вне [1,3] (например @0/@4) или нечисловой (например "banner@x")
// отклоняется.
func SplitPresetNameDPR(name string) (string, DPR, error) {
	at := strings.LastIndex(name, "@")
	if at < 0 {
		return name, 0, nil
	}
	suffix := name[at+1:]
	if suffix == "" {
		return "", 0, fmt.Errorf("preset name %q: empty dpr suffix", name)
	}
	v, err := strconv.Atoi(suffix)
	if err != nil {
		return "", 0, fmt.Errorf("preset name %q: dpr suffix must be an integer", name)
	}
	if v < DefaultDPR || v > MaxDPR {
		return "", 0, fmt.Errorf("preset name %q: dpr must be in [%d,%d]", name, DefaultDPR, MaxDPR)
	}
	return name[:at], DPR(v), nil
}
