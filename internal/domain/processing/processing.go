// Package processing реализует доменный слой описания операций обработки
// изображений.
//
// Пакет определяет закрытые (closed) enum-ы операций и форматов, а также
// валидируемый immutable ProcessingPlan. План описывает ЧТО нужно сделать
// (операции, форматы, размеры), но НЕ КАК (без ImageMagick-специфичных
// аргументов). Исполнитель (ImageMagick и т.п.) отвечает за маппинг плана
// в конкретные команды.
//
// Пакет не зависит от HTTP, файловой системы, ImageMagick и загрузчика
// конфигурации.
package processing

import (
	"fmt"
	"strings"
)

// Operation — допустимая enum операций обработки.
type Operation string

const (
	// OpResize — изменение размера с сохранением пропорций.
	OpResize Operation = "resize"
	// OpCrop — обрезка (centre-crop) до целевого размера.
	OpCrop Operation = "crop"
	// OpTrim — обрезка по краям (trim).
	OpTrim Operation = "trim"
	// OpCropTrim — последовательное применение trim и crop (сначала trim).
	OpCropTrim Operation = "crop-trim"
)

// ValidOperation проверяет, что op является допустимой операцией.
func ValidOperation(op Operation) bool {
	switch op {
	case OpResize, OpCrop, OpTrim, OpCropTrim:
		return true
	default:
		return false
	}
}

// Operations возвращает все допустимые операции.
func Operations() []Operation {
	return []Operation{OpResize, OpCrop, OpTrim, OpCropTrim}
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
)

// ValidFormat проверяет, что f является допустимым форматом.
func ValidFormat(f Format) bool {
	switch f {
	case FormatJPEG, FormatPNG, FormatWebP, FormatGIF, FormatAVIF, FormatHEIF, FormatAPNG:
		return true
	default:
		return false
	}
}

// Formats возвращает все допустимые форматы.
func Formats() []Format {
	return []Format{FormatJPEG, FormatPNG, FormatWebP, FormatGIF, FormatAVIF, FormatHEIF, FormatAPNG}
}

// ParseFormat разбирает строку в Format (регистронезависимо).
//
// Расширения-алиасы нормализуются в канонические форматы: "jpg" → "jpeg",
// "heic" → "heif". Это соответствует URL-грамматике, где расширение
// исходного файла может быть "jpg", и конфигурации пресетов.
func ParseFormat(s string) (Format, error) {
	f := Format(strings.ToLower(s))
	switch f {
	case "jpg":
		f = FormatJPEG
	case "heic":
		f = FormatHEIF
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

// ProcessingPlan — immutable валидируемый план обработки.
//
// План не содержит ImageMagick-специфичных аргументов: только доменные
// операции, форматы и размеры. Исполнитель маппит план в команды.
type ProcessingPlan struct {
	// Operation — операция обработки.
	Operation Operation
	// SourceFormat — формат исходного файла.
	SourceFormat Format
	// OutputFormat — результирующий формат.
	OutputFormat Format
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
}

// NewProcessingPlan создаёт ProcessingPlan с валидацией.
func NewProcessingPlan(op Operation, sourceFormat, outputFormat Format, size Size, dpr, quality int, loop *bool, frames, duration int) (*ProcessingPlan, error) {
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
	return &ProcessingPlan{
		Operation:    op,
		SourceFormat: sourceFormat,
		OutputFormat: outputFormat,
		Size:         size,
		DPR:          dpr,
		Quality:      quality,
		Loop:         loop,
		Frames:       frames,
		Duration:     duration,
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
	if !ValidFormat(p.OutputFormat) {
		return fmt.Errorf("processing plan: invalid output format %q", p.OutputFormat)
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
	return nil
}
