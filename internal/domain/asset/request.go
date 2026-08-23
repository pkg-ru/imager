package asset

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/processing"
)

// Request — immutable типизированное представление asset URL.
//
// Поля приватны и неизменяемы; создаётся только через конструкторы
// (NewRequest, Parse, NewPresetRequest). Для канонического запроса
// заполняются SourceName, SourceFormat, Transform, Size, DPR и OutputFormat,
// а PresetName пуст. Для preset-запроса заполняются SourceName, SourceFormat
// (берётся из URL), PresetName, DPR и OutputFormat.
//
// Поля quality/frames/duration/loop — параметры обработки, которые не
// являются частью URL-грамматики. Они заполняются при разрешении пресета
// (PresetSet.Resolve) и не влияют на Build(): канонический URL строится
// только из URL-компонентов. Для канонических запросов (не preset) эти
// поля нулевые.
type Request struct {
	path         string
	sourceName   SourceName
	sourceFormat Format
	transform    Transform
	size         Size
	dpr          DPR
	outputFormat Format
	presetName   PresetName
	quality      int
	frames       int
	duration     int
	loop         *bool
	watermark    *processing.WatermarkSpec
	orientation  *processing.OrientationSpec
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

// Quality возвращает качество сжатия (0 = default-quality из processing).
// Заполняется при разрешении пресета; пусто для канонических запросов.
func (r *Request) Quality() int { return r.quality }

// Frames возвращает максимальное число кадров анимации (0 = без
// ограничения). Заполняется при разрешении пресета.
func (r *Request) Frames() int { return r.frames }

// Duration возвращает максимальную длительность анимации в миллисекундах
// (0 = без ограничения). Заполняется при разрешении пресета.
func (r *Request) Duration() int { return r.duration }

// Loop возвращает зацикливание анимации (nil = по умолчанию из processing).
// Заполняется при разрешении пресета.
func (r *Request) Loop() *bool { return r.loop }

// Watermark возвращает спецификацию ватермарки (nil = не задана).
// Заполняется при разрешении пресета; для канонических запросов
// ватермарка определяется path-policy/дефолтом на уровне use case.
func (r *Request) Watermark() *processing.WatermarkSpec { return r.watermark }

// Orientation возвращает спецификацию ориентации (nil = не задана:
// используется глобальный дефолт processing.default-* на уровне use case).
// Заполняется при разрешении пресета.
func (r *Request) Orientation() *processing.OrientationSpec { return r.orientation }

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

// Build собирает канонический URL.
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

// WithProcessingOptions возвращает копию запроса с параметрами обработки
// (quality/frames/duration/loop/watermark). Используется при разрешении
// пресета: параметры не являются частью URL-грамматики и не влияют на Build().
func (r *Request) WithProcessingOptions(quality, frames, duration int, loop *bool, watermark *processing.WatermarkSpec) *Request {
	if r == nil {
		return nil
	}
	cp := *r
	cp.quality = quality
	cp.frames = frames
	cp.duration = duration
	cp.loop = loop
	cp.watermark = watermark
	return &cp
}

// WithOrientation возвращает копию запроса со спецификацией ориентации.
// Используется при разрешении пресета: ориентация не является частью
// URL-грамматики и не влияет на Build(). nil = не задана (используется
// глобальный дефолт на уровне use case).
func (r *Request) WithOrientation(o *processing.OrientationSpec) *Request {
	if r == nil {
		return nil
	}
	cp := *r
	cp.orientation = o
	return &cp
}

// joinPath — вспомогательная функция (сохранена для совместимости с
// внутренними вызовами).
func joinPath(path, file string) string {
	if path == "" {
		return file
	}
	return strings.TrimSuffix(path, "/") + "/" + file
}
