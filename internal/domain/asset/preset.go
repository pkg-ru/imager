package asset

import (
	"fmt"
	"sort"
)

// Preset — immutable именованный набор параметров обработки.
//
// Preset НЕ содержит source format: исходный формат определяется URL
// ({source_name}-{source_format}/{preset_name}.{output_format}) и передаётся
// при разрешении. Также preset фиксирует output format: URL обязан
// использовать тот же output format, иначе разрешение завершается ошибкой.
// DPR в пресете не хранится: он берётся из URL.
type Preset struct {
	name         string
	transform    Transform
	size         Size
	outputFormat Format
}

// NewPreset создаёт Preset с валидацией.
func NewPreset(name string, transform Transform, size Size, outputFormat Format) (*Preset, error) {
	if name == "" {
		return nil, fmt.Errorf("preset: empty name")
	}
	if !ValidTransform(transform) {
		return nil, fmt.Errorf("preset %q: invalid transform %q", name, transform)
	}
	if size.IsEmpty() {
		return nil, fmt.Errorf("preset %q: empty size", name)
	}
	if outputFormat == "" {
		return nil, fmt.Errorf("preset %q: empty output format", name)
	}
	return &Preset{
		name:         name,
		transform:    transform,
		size:         size,
		outputFormat: outputFormat,
	}, nil
}

// Name возвращает имя пресета.
func (p *Preset) Name() string { return p.name }

// Transform возвращает режим трансформации.
func (p *Preset) Transform() Transform { return p.transform }

// Size возвращает размер.
func (p *Preset) Size() Size { return p.size }

// OutputFormat возвращает выходной формат.
func (p *Preset) OutputFormat() Format { return p.outputFormat }

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
// пресета. DPR берётся из URL (req.dpr), а не из пресета. Output format в
// URL обязан совпадать с output format пресета, иначе возвращается ошибка.
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
	p, ok := s.byName[req.presetName.String()]
	if !ok {
		return nil, &ResolveError{
			PresetName: req.presetName.String(),
			Reason:     "preset not found",
		}
	}
	if req.outputFormat != p.outputFormat {
		return nil, &ResolveError{
			PresetName: req.presetName.String(),
			Reason: fmt.Sprintf(
				"output format %q does not match preset output format %q",
				req.outputFormat, p.outputFormat,
			),
		}
	}
	return NewRequest(
		req.path,
		req.sourceName,
		req.sourceFormat,
		p.transform,
		p.size,
		req.dpr,
		req.outputFormat,
	)
}
