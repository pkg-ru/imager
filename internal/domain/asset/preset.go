package asset

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/processing"
)

// Preset — immutable именованный набор параметров обработки.
//
// Preset НЕ содержит source format: исходный формат определяется URL
// ({source_name}-{source_format}/{preset_name}.{output_format}) и передаётся
// при разрешении. Также preset фиксирует output format: URL обязан
// использовать тот же output format, иначе разрешение завершается ошибкой.
//
// Имя пресета может содержать фиксированный суффикс @dpr (например
// "thumb@2"): при разрешении такого пресета dpr=2 применяется всегда.
// Поле dpr (если задано) имеет приоритет над @dpr в имени.
type Preset struct {
	name         string
	transform    Transform
	size         Size
	outputFormat Format
	dpr          DPR
	quality      int
	frames       int
	duration     int
	loop         *bool
	watermark    *processing.WatermarkSpec
	orientation  *processing.OrientationSpec
}

// NewPreset создаёт Preset с валидацией.
//
// dpr — фиксированный DPR пресета (0 = не задан, берётся из имени/URL).
// quality — качество сжатия (0 = default-quality из processing).
// frames — максимальное число кадров анимации (0 = без ограничения).
// duration — максимальная длительность анимации в мс (0 = без ограничения).
// loop — зацикливание анимации (nil = default-loop из processing).
func NewPreset(name string, transform Transform, size Size, outputFormat Format, dpr DPR, quality, frames, duration int, loop *bool) (*Preset, error) {
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
	if outputFormat == "" {
		return nil, fmt.Errorf("preset %q: empty output format", name)
	}
	// Если фиксированный dpr не задан (0), берём его из @dpr-суффикса имени
	// (например "thumb@2" → dpr=2). Поле dpr имеет приоритет над именем.
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
		outputFormat: outputFormat,
		dpr:          dpr,
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

// OutputFormat возвращает выходной формат.
func (p *Preset) OutputFormat() Format { return p.outputFormat }

// DPR возвращает фиксированный DPR пресета (0 = не задан).
func (p *Preset) DPR() DPR { return p.dpr }

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

// ResolveError описывает ошибку разрешения preset URL в канонический запрос.
type ResolveError struct {
	PresetName string
	Reason     string
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("cannot resolve preset %q: %s", e.PresetName, e.Reason)
}

// Resolve превращает preset Request в канонический Request.
//
// Source format берётся из URL (req.SourceFormat), а transform/size — из
// пресета. Output format в URL обязан совпадать с output format пресета,
// иначе возвращается ошибка.
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
		return nil, &ResolveError{Reason: "request is not a preset url"}
	}
	if req.sourceFormat == "" {
		return nil, &ResolveError{
			PresetName: req.presetName.String(),
			Reason:     "missing source format in url",
		}
	}
	if req.transform != "" || !req.size.IsEmpty() {
		return nil, &ResolveError{
			PresetName: req.presetName.String(),
			Reason:     "preset cannot be partially overridden",
		}
	}
	// Имя пресета из URL: сначала ищем полное имя (в т.ч. с @dpr-суффиксом,
	// например "thumb@2"). Если полное имя не найдено, но оно разбивается на
	// base + @dpr (например "thumb@2" → "thumb" + 2), ищем base — обратная
	// совместимость: пресет "thumb" с явным @dpr-суффиксом URL.
	presetName := req.presetName.String()
	p, ok := s.byName[presetName]
	var nameDPR DPR
	if !ok {
		if base, dprSuffix, err := SplitPresetNameDPR(presetName); err == nil && base != presetName {
			if bp, exists := s.byName[base]; exists {
				p = bp
				ok = true
				nameDPR = dprSuffix
			}
		}
	}
	if !ok {
		return nil, &ResolveError{
			PresetName: presetName,
			Reason:     "preset not found",
		}
	}
	if req.outputFormat != p.outputFormat {
		return nil, &ResolveError{
			PresetName: presetName,
			Reason: fmt.Sprintf(
				"output format %q does not match preset output format %q",
				req.outputFormat, p.outputFormat,
			),
		}
	}

	// DPR: фиксированный dpr пресета (поле dpr имеет приоритет над @dpr
	// имени) либо @dpr из суффикса имени (при fallback на base-пресет).
	// Явный @dpr в URL, отличный от фиксированного, — ошибка. Если
	// фиксированного dpr нет, dpr берётся из URL (default 1).
	fixed := p.dpr
	if fixed == 0 {
		fixed = nameDPR
	}
	dpr := req.dpr
	if fixed != 0 {
		if req.dpr != DefaultDPR && req.dpr != fixed {
			return nil, &ResolveError{
				PresetName: presetName,
				Reason: fmt.Sprintf(
					"dpr %d in url conflicts with preset dpr %d",
					req.dpr.Int(), fixed.Int(),
				),
			}
		}
		dpr = fixed
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
			PresetName: req.presetName.String(),
			Reason:     err.Error(),
		}
	}
	resolved = resolved.WithProcessingOptions(p.quality, p.frames, p.duration, p.loop, p.watermark)
	if p.orientation != nil {
		resolved = resolved.WithOrientation(p.orientation)
	}
	return resolved, nil
}

// SplitPresetNameDPR отделяет фиксированный @dpr-суффикс от имени пресета.
//
// Возвращает имя без суффикса и DPR (0, если суффикса нет). Примеры:
//
//	"thumb"   → ("thumb", 0)
//	"thumb@1" → ("thumb", 1)   // dpr=1 эквивалентен отсутствию
//	"thumb@2" → ("thumb", 2)
//	"thumb@3" → ("thumb", 3)
//
// Суффикс вне [1,3] (например @0/@4) или нечисловой (например "thumb@x")
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
