// Package processing реализует доменный слой описания операций обработки
// изображений.
//
// Пакет определяет закрытые (closed) enum-ы операций и форматов, а также
// валидируемый immutable ProcessingPlan. План описывает ЧТО нужно сделать
// (операции, форматы, размеры), но НЕ КАК (без движок-специфичных
// аргументов). Исполнитель (libvips и т.п.) отвечает за маппинг плана
// в конкретные команды.
//
// Пакет не зависит от HTTP, файловой системы, движка обработки и загрузчика
// конфигурации.
package processing

import (
	"fmt"
	"math"
	"strings"

	"gitverse.ru/pkg-ru/imager/domain/encoding"
)

// Operation — допустимая enum операций обработки кропа/ресайза.
//
// Trim — НЕ операция enum: это независимый булев фильтр обрезки однотонных
// полей (ProcessingPlan.Trim), применяемый первым к любому типу обработки
// (сначала trim, затем основная операция). Отдельная операция OpTrim не
// существует: trim-only выражается как OpResize + Trim=true.
type Operation string

const (
	// OpResize — изменение размера с сохранением пропорций (без кропа).
	OpResize Operation = "resize"
	// OpCrop — центрированный кроп до целевого размера.
	OpCrop Operation = "crop"
	// OpSmartCrop — «умная» обрезка по значимой области (attention libvips).
	OpSmartCrop Operation = "smart-crop"
	// OpFaceCrop — обрезка по обнаруженным лицам (ONNX YuNet).
	OpFaceCrop Operation = "face-crop"
	// OpObjectCrop — обрезка по обнаруженным объектам (ONNX SSD/YOLO).
	OpObjectCrop Operation = "object-crop"
)

// ValidOperation проверяет, что op является допустимой операцией.
func ValidOperation(op Operation) bool {
	switch op {
	case OpResize, OpCrop, OpSmartCrop, OpFaceCrop, OpObjectCrop:
		return true
	default:
		return false
	}
}

// Format — закрытый enum форматов файлов.
type Format string

const (
	// FormatJPEG — JPEG.
	FormatJPEG Format = "jpeg"
	// FormatPNG — PNG.
	FormatPNG Format = "png"
	// FormatWebP — WebP.
	FormatWebP Format = "webp"
	// FormatGIF — GIF.
	FormatGIF Format = "gif"
	// FormatAVIF — AVIF.
	FormatAVIF Format = "avif"
	// FormatHEIF — HEIF.
	FormatHEIF Format = "heif"
	// FormatAPNG — APNG.
	FormatAPNG Format = "apng"
	// FormatJPEGXL — JPEG XL.
	FormatJPEGXL Format = "jxl"
)

// ValidFormat проверяет, что f является допустимым форматом.
func ValidFormat(f Format) bool {
	switch f {
	case FormatJPEG, FormatPNG, FormatWebP, FormatGIF, FormatAVIF, FormatHEIF, FormatAPNG, FormatJPEGXL:
		return true
	default:
		return false
	}
}

// ParseFormat разбирает строку в Format (регистронезависимо).
//
// Расширения-алиасы нормализуются в канонические форматы: "jpg" → "jpeg",
// "heic" → "heif", "jpegxl" → "jxl". Это соответствует URL-грамматике, где
// расширение исходного файла может быть "jpg", и конфигурации пресетов.
func ParseFormat(s string) (Format, error) {
	f := Format(strings.ToLower(s))
	switch f {
	case "jpg":
		f = FormatJPEG
	case "heic":
		f = FormatHEIF
	case "jpegxl":
		f = FormatJPEGXL
	}
	if !ValidFormat(f) {
		return "", fmt.Errorf("unsupported format %q", s)
	}
	return f, nil
}

// Animated сообщает, поддерживает ли формат анимацию.
func (f Format) Animated() bool {
	switch f {
	case FormatGIF, FormatWebP, FormatAPNG, FormatHEIF:
		return true
	default:
		return false
	}
}

// String возвращает строковое представление формата.
func (f Format) String() string { return string(f) }

// Size — целевой размер обработки.
type Size struct {
	// Width — целевая ширина (0 = авто).
	Width int
	// Height — целевая высота (0 = авто).
	Height int
	// Original — true, если нужно сохранить исходный размер изображения
	// (size=x). В этом случае Width и Height игнорируются.
	Original bool
}

// Valid проверяет корректность размера.
func (s Size) Valid() error {
	if s.Width < 0 || s.Height < 0 {
		return fmt.Errorf("size dimensions must be non-negative, got %dx%d", s.Width, s.Height)
	}
	if !s.Original && s.Width == 0 && s.Height == 0 {
		return fmt.Errorf("size must specify width or height")
	}
	return nil
}

// TrimMode — режим определения цвета однотонного поля для обрезки trim.
type TrimMode string

const (
	// TrimModeAuto — автоматическое определение цвета фона (по краевому
	// пикселю). Режим по умолчанию.
	TrimModeAuto TrimMode = "auto"
	// TrimModeColor — фиксированный цвет фона (задаётся TrimSpec.Color).
	TrimModeColor TrimMode = "color"
)

// ValidTrimMode проверяет допустимость режима trim.
func ValidTrimMode(m TrimMode) bool {
	return m == TrimModeAuto || m == TrimModeColor
}

// TrimSpec — настройки независимого фильтра trim (обрезка однотонных
// полей). Применяется к ЛЮБОЙ основной операции (resize/crop/smart-crop/
// face-crop/object-crop) СТРОГО до неё (сначала trim, затем кроп/ресайз).
type TrimSpec struct {
	// Mode — режим определения цвета фона: auto (авто, по краю) или
	// color (фиксированный цвет). Default: auto.
	Mode TrimMode
	// Color — фиксированный цвет фона в hex-форме "#RRGGBB" (только для
	// Mode=color). Примеры: "#ffffff", "#000000".
	Color string
	// Tolerance — допуск сравнения пикселей с фоновым цветом в диапазоне
	// [0,1]: 0 — точное совпадение, 1 — любые пиксели считаются фоном.
	// Default: 0.0.
	Tolerance float64
}

// DefaultTrimSpec возвращает спецификацию trim по умолчанию
// ({Mode: auto, Tolerance: 0}).
func DefaultTrimSpec() *TrimSpec {
	return &TrimSpec{Mode: TrimModeAuto, Tolerance: 0}
}

// Validate проверяет корректность спецификации trim.
func (t *TrimSpec) Validate() error {
	if t == nil {
		return nil
	}
	if !ValidTrimMode(t.Mode) {
		return fmt.Errorf("invalid trim mode %q, must be auto or color", t.Mode)
	}
	if t.Mode == TrimModeColor && t.Color == "" {
		return fmt.Errorf("trim color mode requires a color")
	}
	if t.Color != "" && !isHexColor(t.Color) {
		return fmt.Errorf("trim color %q must be in #RRGGBB form", t.Color)
	}
	// NaN не проходит сравнения < 0 / > 1 — отклоняем явно.
	if math.IsNaN(t.Tolerance) || t.Tolerance < 0 || t.Tolerance > 1 {
		return fmt.Errorf("trim tolerance must be in [0,1], got %v", t.Tolerance)
	}
	return nil
}

// isHexColor проверяет формат "#RRGGBB".
func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ProcessingPlan — immutable валидируемый план обработки.
//
// План не содержит движок-специфичных аргументов: только доменные операции,
// форматы и размеры. Исполнитель маппит план в команды.
type ProcessingPlan struct {
	// Operation — операция обработки кропа/ресайза (без trim).
	Operation Operation
	// Trim — независимый булев фильтр обрезки однотонных полей (false =
	// не применять). Применяется процессорами СТРОГО до Operation
	// (сначала trim, затем кроп/ресайз).
	Trim bool
	// TrimSpec — настройки trim (режим auto/color + tolerance). nil =
	// спецификация по умолчанию ({auto, 0}).
	TrimSpec *TrimSpec
	// SourceFormat — формат исходного файла.
	SourceFormat Format
	// OutputFormats — результирующий формат.
	OutputFormats Format
	// Size — целевой размер.
	Size Size
	// DPR — множитель плотности пикселей (0 = 1).
	DPR int
	// Quality — качество сжатия (0 = по умолчанию).
	Quality int
	// Loop — зацикливание анимации (nil = по умолчанию).
	Loop *bool
	// Frames — максимальное число кадров анимации (0 = без ограничения).
	Frames int
	// Duration — максимальная длительность анимации в миллисекундах
	// (0 = без ограничения).
	Duration int
	// Watermark — спецификация ватермарки (nil = не применяется).
	// Заполняется из конфигурации (пресет → path-policy → default);
	// НЕ является частью URL-грамматики. Спецификация приходит из
	// доверенного конфига и маппится процессорами через allowlists.
	Watermark *WatermarkSpec
	// Orientation — спецификация ориентационных операций (EXIF auto-orient,
	// поворот, отражение). nil = поведение по умолчанию. Заполняется
	// из конфигурации (прет → processing.default-*); НЕ является частью
	// URL-грамматики. Применяется процессорами СТРОГО до кропа.
	Orientation *OrientationSpec
	// EncodingOverrides — native-параметры кодирования по форматам
	// (формат → ключ реестра domain/encoding → значение). nil = не заданы.
	// Заполняется из пресета/custom (плоские native-ключи YAML) и
	// передаётся процессору для Resolve; валидируется в Validate
	// (инвариант: значения уже проверены при компиляции конфигурации).
	EncodingOverrides map[string]map[string]any
}

// NewProcessingPlan создаёт ProcessingPlan с валидацией.
//
// encOverrides (необязательный variadic-параметр) — native-параметры
// кодирования по форматам; единственный экземпляр (или nil) допускается.
func NewProcessingPlan(op Operation, sourceFormat, outputFormat Format, size Size, dpr, quality int, loop *bool, frames, duration int, encOverrides ...map[string]map[string]any) (*ProcessingPlan, error) {
	if !ValidOperation(op) {
		return nil, fmt.Errorf("processing plan: invalid operation %q", op)
	}
	if !ValidFormat(sourceFormat) {
		return nil, fmt.Errorf("processing plan: invalid source format %q", sourceFormat)
	}
	if !ValidFormat(outputFormat) {
		return nil, fmt.Errorf("processing plan: invalid output format %q", outputFormat)
	}
	if err := size.Valid(); err != nil {
		return nil, fmt.Errorf("processing plan: %w", err)
	}
	if dpr < 0 {
		return nil, fmt.Errorf("processing plan: dpr must be non-negative, got %d", dpr)
	}
	if quality < 0 || quality > 100 {
		return nil, fmt.Errorf("processing plan: quality must be in [0,100], got %d", quality)
	}
	if frames < 0 {
		return nil, fmt.Errorf("processing plan: frames must be non-negative, got %d", frames)
	}
	if duration < 0 {
		return nil, fmt.Errorf("processing plan: duration must be non-negative, got %d", duration)
	}
	var enc map[string]map[string]any
	if len(encOverrides) > 0 {
		enc = encOverrides[0]
	}
	return &ProcessingPlan{
		Operation:         op,
		SourceFormat:      sourceFormat,
		OutputFormats:     outputFormat,
		Size:              size,
		DPR:               dpr,
		Quality:           quality,
		Loop:              loop,
		Frames:            frames,
		Duration:          duration,
		EncodingOverrides: enc,
	}, nil
}

// Validate проверяет корректность плана.
func (p *ProcessingPlan) Validate() error {
	if p == nil {
		return fmt.Errorf("processing plan is nil")
	}
	if !ValidOperation(p.Operation) {
		return fmt.Errorf("processing plan: invalid operation %q", p.Operation)
	}
	if !ValidFormat(p.SourceFormat) {
		return fmt.Errorf("processing plan: invalid source format %q", p.SourceFormat)
	}
	if !ValidFormat(p.OutputFormats) {
		return fmt.Errorf("processing plan: invalid output format %q", p.OutputFormats)
	}
	if err := p.Size.Valid(); err != nil {
		return fmt.Errorf("processing plan: %w", err)
	}
	if p.DPR < 0 {
		return fmt.Errorf("processing plan: dpr must be non-negative, got %d", p.DPR)
	}
	if p.Quality < 0 || p.Quality > 100 {
		return fmt.Errorf("processing plan: quality must be in [0,100], got %d", p.Quality)
	}
	if p.Frames < 0 {
		return fmt.Errorf("processing plan: frames must be non-negative, got %d", p.Frames)
	}
	if p.Duration < 0 {
		return fmt.Errorf("processing plan: duration must be non-negative, got %d", p.Duration)
	}
	if p.Watermark != nil {
		wm := p.Watermark
		if wm.Name == "" || wm.Path == "" {
			return fmt.Errorf("processing plan: watermark name and path must not be empty")
		}
		if !ValidWatermarkPosition(wm.Position) {
			return fmt.Errorf("processing plan: invalid watermark position %q", wm.Position)
		}
		if !ValidWatermarkRepeat(wm.Repeat) {
			return fmt.Errorf("processing plan: invalid watermark repeat %q", wm.Repeat)
		}
	}
	if err := p.TrimSpec.Validate(); err != nil {
		return fmt.Errorf("processing plan: trim: %w", err)
	}
	if err := p.Orientation.Validate(); err != nil {
		return fmt.Errorf("processing plan: %w", err)
	}
	// Native-параметры кодирования: валидируются по реестру (известный
	// формат/параметр, диапазон). Для план-построения значение уже прошло
	// компиляцию конфигурации; здесь — defense-in-depth для программных
	// построений плана.
	for format, params := range p.EncodingOverrides {
		if err := encoding.ValidateOverrides(format, params); err != nil {
			return fmt.Errorf("processing plan: encoding overrides for %q: %w", format, err)
		}
	}
	return nil
}
