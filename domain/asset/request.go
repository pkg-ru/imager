package asset

import (
	"fmt"
	"strconv"
	"strings"

	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// Request — immutable типизированное представление asset URL.
//
// Поля приватны и неизменяемы; создаётся только через конструкторы
// (NewRequest, NewSegmentRequest, NewPresetRequest, Parse).
//
// Новая грамматика: /{path}/{source_name}-{source_format}/{segment}@{dpr}.{out},
// где segment — имя пресета ИЛИ custom-имя (размер). Transform-коды в URL
// отсутствуют: операция определяется полем crop пресета/custom.
//
// Для segment-запроса заполняются SourceName, SourceFormat, SegmentName, DPR
// (0 = @dpr в URL отсутствует) и OutputFormats. Поля transform/size/quality/
// frames/duration/loop/watermark/orientation заполняются при разрешении
// (PresetSet.Resolve / Policy.Resolve) и не влияют на Build(): канонический
// URL строится из segmentName + dpr + outputFormat.
//
// Для канонического запроса (NewRequest, программное создание) заполняются
// Transform, Size, DPR и OutputFormats, а SegmentName пуст. Build() в этом
// случае использует старую форму {transform}-{size}@{dpr}.{out} (совместимость
// с программными вызовами; парсер такую форму не разбирает).
type Request struct {
	path         string
	sourceName   SourceName
	sourceFormat Format
	segmentName  SegmentName
	transform    Transform
	size         Size
	dpr          DPR
	outputFormat Format
	quality      int
	frames       int
	duration     int
	loop         *bool
	watermark    *processing.WatermarkSpec
	orientation  *processing.OrientationSpec
	// encOverrides — native-параметры форматов (формат → нативные параметры
	// kebab-case), заполняется при разрешении пресета/custom. nil = не заданы.
	// Передаётся в buildPlanForSource и далее в ProcessingPlan.EncodingOverrides.
	encOverrides map[string]map[string]any
	resolved     bool
}

// NewRequest создаёт канонический Request (transform/size, без segment).
// Используется программно (тесты, перечисление); парсер такую форму не
// разбирает.
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

// NewSegmentRequest создаёт segment Request (имя пресета/custom) из URL.
// dprURL — @dpr-суффикс URL: 0 = отсутствует, 2/3 = явный. SourceFormat
// берётся из URL и сохраняется в запросе: при разрешении он определяет,
// какой исходный файл искать.
func NewSegmentRequest(path string, sourceName SourceName, sourceFormat Format, segmentName SegmentName, dprURL DPR, outputFormat Format) (*Request, error) {
	if sourceName == "" {
		return nil, fmt.Errorf("request: empty source name")
	}
	if sourceFormat == "" {
		return nil, fmt.Errorf("request: empty source format")
	}
	if segmentName == "" {
		return nil, fmt.Errorf("request: empty segment name")
	}
	if outputFormat == "" {
		return nil, fmt.Errorf("request: empty output format")
	}
	if dprURL != 0 && !dprURL.Valid() {
		return nil, fmt.Errorf("request: dpr must be in [%d,%d], got %d", MinDPR, MaxDPR, dprURL.Int())
	}
	canon, err := NewCanonicalizer().CanonicalPath(path)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	return &Request{
		path:         canon,
		sourceName:   sourceName,
		sourceFormat: sourceFormat,
		segmentName:  segmentName,
		dpr:          dprURL,
		outputFormat: outputFormat,
	}, nil
}

// NewPresetRequest создаёт preset Request (обратно-совместимая обёртка
// NewSegmentRequest). dpr — фиксированный DPR (1-3), как раньше.
func NewPresetRequest(path string, sourceName SourceName, sourceFormat Format, presetName PresetName, dpr DPR, outputFormat Format) (*Request, error) {
	return NewSegmentRequest(path, sourceName, sourceFormat, SegmentName(presetName), dpr, outputFormat)
}

// Path возвращает канонический путь.
func (r *Request) Path() string { return r.path }

// Quality возвращает качество сжатия (0 = default-quality из секции encoders).
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
// Заполняется при разрешении пресета.
func (r *Request) Watermark() *processing.WatermarkSpec { return r.watermark }

// Orientation возвращает спецификацию ориентации (nil = не задана:
// используется глобальный дефолт processing.default-* на уровне use case).
// Заполняется при разрешении пресета.
func (r *Request) Orientation() *processing.OrientationSpec { return r.orientation }

// EncodingOverrides возвращает native-параметры форматов (формат → нативные
// параметры kebab-case). Возвращаемая копия защищает внутреннее состояние.
// nil = не заданы. Заполняется при разрешении пресета/custom.
func (r *Request) EncodingOverrides() map[string]map[string]any {
	return cloneEncOverrides(r.encOverrides)
}

// SourceName возвращает имя исходника.
func (r *Request) SourceName() SourceName { return r.sourceName }

// SourceFormat возвращает формат исходника. Для segment-запроса это формат,
// указанный в URL.
func (r *Request) SourceFormat() Format { return r.sourceFormat }

// Transform возвращает режим трансформации (пуст для неразрешённого segment).
func (r *Request) Transform() Transform { return r.transform }

// Size возвращает размер (пуст для неразрешённого segment).
func (r *Request) Size() Size { return r.size }

// DPR возвращает DPR. Для неразрешённого segment-запроса это @dpr-суффикс
// URL (0 = отсутствует); после разрешения — итоговый DPR (1-3).
func (r *Request) DPR() DPR { return r.dpr }

// OutputFormats возвращает выходной формат.
func (r *Request) OutputFormats() Format { return r.outputFormat }

// SegmentName возвращает имя сегмента (пресета/custom; пуст для
// канонического запроса).
func (r *Request) SegmentName() SegmentName { return r.segmentName }

// PresetName возвращает имя пресета (алиас SegmentName; пуст для
// канонического запроса).
func (r *Request) PresetName() PresetName { return PresetName(r.segmentName) }

// IsPreset возвращает true, если запрос является segment URL (имя
// пресета/custom).
func (r *Request) IsPreset() bool { return r.segmentName != "" }

// IsResolved возвращает true, если запрос уже разрешён (настройки
// пресета/custom применены).
func (r *Request) IsResolved() bool { return r.resolved }

// Build собирает канонический URL.
//
//	segment:  {path}/{source_name}-{source_format}/{segment}@{dpr}.{output_format}
//	канонич.: {path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}
//
// Для segment-запросов DPR=1 (default) и DPR=0 (не задан) не выводятся;
// явные 2 и 3 выводятся как @2/@3, если имя сегмента не содержит @dpr
// (например "banner@2" — суффикс уже в имени).
func (r *Request) Build() (string, error) {
	if r == nil {
		return "", fmt.Errorf("build: nil request")
	}
	var core string
	if r.segmentName != "" {
		if r.sourceFormat == "" {
			return "", fmt.Errorf("build: empty source format for segment request")
		}
		core = r.sourceName.String() + "-" + r.sourceFormat.String() + "/" + r.segmentName.String()
		if r.dpr != 0 && !r.dpr.IsDefault() && !strings.Contains(r.segmentName.String(), "@") {
			core += "@" + strconv.Itoa(r.dpr.Int())
		}
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
		if !r.dpr.IsDefault() {
			core += "@" + strconv.Itoa(r.dpr.Int())
		}
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
// (quality/frames/duration/loop/watermark/encOverrides). Используется при
// разрешении пресета: параметры не являются частью URL-грамматики и не
// влияют на Build().
func (r *Request) WithProcessingOptions(quality, frames, duration int, loop *bool, watermark *processing.WatermarkSpec, encOverrides map[string]map[string]any) *Request {
	if r == nil {
		return nil
	}
	cp := *r
	cp.quality = quality
	cp.frames = frames
	cp.duration = duration
	cp.loop = loop
	cp.watermark = watermark
	cp.encOverrides = cloneEncOverrides(encOverrides)
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

// WithResolved возвращает копию запроса с применёнными настройками
// пресета/custom. segmentName сохраняется: канонический URL строится из
// него. resolved=true помечает запрос разрешённым (Authorize не резолвит
// повторно).
func (r *Request) WithResolved(transform Transform, size Size, dpr DPR, quality, frames, duration int, loop *bool, watermark *processing.WatermarkSpec, orientation *processing.OrientationSpec, encOverrides map[string]map[string]any) *Request {
	if r == nil {
		return nil
	}
	cp := *r
	cp.transform = transform
	cp.size = size
	cp.dpr = dpr
	cp.quality = quality
	cp.frames = frames
	cp.duration = duration
	cp.loop = loop
	cp.watermark = watermark
	cp.orientation = orientation
	cp.encOverrides = cloneEncOverrides(encOverrides)
	cp.resolved = true
	return &cp
}
