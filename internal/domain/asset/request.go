package asset

import (
	"fmt"
	"strconv"
	"strings"
)

// Request — immutable типизированное представление asset URL.
//
// Поля приватны и неизменяемы; создаётся только через конструкторы
// (NewRequest, Parse, NewPresetRequest). Для канонического запроса
// заполняются SourceName, SourceFormat, Transform, Size, DPR и OutputFormat,
// а PresetName пуст. Для preset-запроса заполняются SourceName, SourceFormat
// (берётся из URL), PresetName, DPR и OutputFormat.
type Request struct {
	path         string
	sourceName   SourceName
	sourceFormat Format
	transform    Transform
	size         Size
	dpr          DPR
	outputFormat Format
	presetName   PresetName
}

// NewRequest создаёт канонический Request.
func NewRequest(path string, sourceName SourceName, sourceFormat Format, transform Transform, size Size, dpr DPR, outputFormat Format) (*Request, error) {
	if sourceName == "" {
		return nil, fmt.Errorf("request: empty source name")
	}
	if sourceFormat == "" {
		return nil, fmt.Errorf("request: empty source format")
	}
	if transform != "" && !ValidTransform(transform) {
		return nil, fmt.Errorf("request: invalid transform %q", transform)
	}
	if size.IsEmpty() {
		return nil, fmt.Errorf("request: empty size")
	}
	if outputFormat == "" {
		return nil, fmt.Errorf("request: empty output format")
	}
	if !dpr.Valid() {
		return nil, fmt.Errorf("request: dpr must be in [%d,%d], got %d", DefaultDPR, MaxDPR, dpr.Int())
	}
	canon, err := NewCanonicalizer().CanonicalPath(path)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	return &Request{
		path:         canon,
		sourceName:   sourceName,
		sourceFormat: sourceFormat,
		transform:    transform,
		size:         size,
		dpr:          dpr,
		outputFormat: outputFormat,
	}, nil
}

// NewPresetRequest создаёт preset Request. SourceFormat берётся из URL и
// сохраняется в запросе: при разрешении пресета он определяет, какой
// исходный файл искать. DPR берётся из URL (default 1).
func NewPresetRequest(path string, sourceName SourceName, sourceFormat Format, presetName PresetName, dpr DPR, outputFormat Format) (*Request, error) {
	if sourceName == "" {
		return nil, fmt.Errorf("request: empty source name")
	}
	if sourceFormat == "" {
		return nil, fmt.Errorf("request: empty source format")
	}
	if presetName == "" {
		return nil, fmt.Errorf("request: empty preset name")
	}
	if outputFormat == "" {
		return nil, fmt.Errorf("request: empty output format")
	}
	if !dpr.Valid() {
		return nil, fmt.Errorf("request: dpr must be in [%d,%d], got %d", DefaultDPR, MaxDPR, dpr.Int())
	}
	canon, err := NewCanonicalizer().CanonicalPath(path)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	return &Request{
		path:         canon,
		sourceName:   sourceName,
		sourceFormat: sourceFormat,
		presetName:   presetName,
		dpr:          dpr,
		outputFormat: outputFormat,
	}, nil
}

// Path возвращает канонический путь.
func (r *Request) Path() string { return r.path }

// SourceName возвращает имя исходника.
func (r *Request) SourceName() SourceName { return r.sourceName }

// SourceFormat возвращает формат исходника. Для preset-запроса это формат,
// указанный в URL.
func (r *Request) SourceFormat() Format { return r.sourceFormat }

// Transform возвращает режим трансформации (пуст для preset).
func (r *Request) Transform() Transform { return r.transform }

// Size возвращает размер (пуст для preset).
func (r *Request) Size() Size { return r.size }

// DPR возвращает DPR (default 1 для preset без суффикса).
func (r *Request) DPR() DPR { return r.dpr }

// OutputFormat возвращает выходной формат.
func (r *Request) OutputFormat() Format { return r.outputFormat }

// PresetName возвращает имя пресета (пуст для канонического).
func (r *Request) PresetName() PresetName { return r.presetName }

// IsPreset возвращает true, если запрос является preset URL.
func (r *Request) IsPreset() bool { return r.presetName != "" }

// Build собирает канонический URL (без ведущего "/v1/").
//
//	канонический: {path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}
//	preset:       {path}/{source_name}-{source_format}/{preset_name}@{dpr}.{output_format}
//
// DPR=1 (default) не выводится в URL; явные 2 и 3 выводятся как @2/@3.
func (r *Request) Build() (string, error) {
	if r == nil {
		return "", fmt.Errorf("build: nil request")
	}
	var core string
	if r.IsPreset() {
		if r.sourceFormat == "" {
			return "", fmt.Errorf("build: empty source format for preset request")
		}
		core = r.sourceName.String() + "-" + r.sourceFormat.String() + "/" + r.presetName.String()
	} else {
		if !r.dpr.Valid() {
			return "", fmt.Errorf("build: dpr must be in [%d,%d], got %d", DefaultDPR, MaxDPR, r.dpr.Int())
		}
		if r.transform != "" && !ValidTransform(r.transform) {
			return "", fmt.Errorf("build: invalid transform %q", r.transform)
		}
		core = r.sourceName.String() + "-" + r.sourceFormat.String() + "/"
		if r.transform != "" {
			core += string(r.transform) + "-"
		}
		core += r.size.String()
	}
	// DPR suffix: default (1) не выводится.
	if !r.dpr.IsDefault() {
		core += "@" + strconv.Itoa(r.dpr.Int())
	}
	file := core + "." + r.outputFormat.String()
	if r.path == "" {
		return file, nil
	}
	return r.path + "/" + file, nil
}

// FullURL возвращает URL с ведущим "/v1/".
func (r *Request) FullURL() (string, error) {
	u, err := r.Build()
	if err != nil {
		return "", err
	}
	return "/" + string(V1) + "/" + u, nil
}

// CanonicalID вычисляет стабильный идентификатор запроса.
func (r *Request) CanonicalID() (CanonicalID, error) {
	u, err := r.Build()
	if err != nil {
		return CanonicalID{}, err
	}
	return NewCanonicalID(u)
}

// String возвращает канонический URL.
func (r *Request) String() string {
	u, err := r.Build()
	if err != nil {
		return ""
	}
	return u
}

// joinPath — вспомогательная функция (сохранена для совместимости с
// внутренними вызовами).
func joinPath(path, file string) string {
	if path == "" {
		return file
	}
	return strings.TrimSuffix(path, "/") + "/" + file
}
