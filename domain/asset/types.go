// Package asset реализует доменный слой канонических asset URL и связанных
// immutable value objects.
//
// Пакет не зависит от HTTP, файловой системы, движка обработки, кэша и
// загрузчика конфигурации. Все value objects неизменяемы (immutable): их
// поля приватны,
// а значения создаются только через конструкторы, выполняющие валидацию.
//
// URL-грамматика:
//
//	/{path}/{source_name}-{source_format}/{segment}@{dpr}.{output_format}
//
// segment — имя пресета ИЛИ custom-имя (размер-грамматика: "x", "x200",
// "200x", "200x200"), опционально с @dpr-суффиксом. Transform-коды в URL
// отсутствуют: операция (resize/crop/smart-crop/face-crop/object-crop)
// определяется ТОЛЬКО полем crop в пресете/custom. dpr — множитель
// плотности пикселей: отсутствие суффикса означает 1, явно допустимы только
// 2 или 3. Имя пресета/custom может содержать фиксированный @dpr-суффикс
// (например "banner@2" или "200x100@2").
//
// Пакет гарантирует безопасную canonicalization: запрещены traversal-сегменты
// ("..", "."), encoded-разделители ("%2f", "%2F"), control-символы, а также
// ограничены длина и допустимый набор символов каждого компонента.
package asset

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Лимиты длины и символов для компонентов URL.
const (
	// MaxPathLen — максимальная длина пути.
	MaxPathLen = 512
	// MaxSourceNameLen — максимальная длина имени исходника.
	MaxSourceNameLen = 128
	// MaxFormatLen — максимальная длина формата (source/output).
	MaxFormatLen = 16
	// MaxPresetNameLen — максимальная длина имени пресета/сегмента.
	MaxPresetNameLen = 64
	// MaxURLLen — максимальная общая длина канонического URL.
	MaxURLLen = 1024

	// MaxDimension — максимальное значение измерения (ширина/высота).
	MaxDimension = 1 << 20 // 1_048_576
	// DefaultDPR — значение DPR по умолчанию (при отсутствии суффикса @dpr).
	DefaultDPR = 1
	// MinDPR — минимальное явно допустимое значение DPR.
	MinDPR = 2
	// MaxDPR — максимальное явно допустимое значение DPR.
	MaxDPR = 3
)

// Допустимые символы компонентов.
const (
	// segmentNameChars — символы, допустимые в имени сегмента (пресета или
	// custom). Дефисы запрещены, чтобы имя сегмента в URL
	// {source_name}-{source_format}/{segment}.{output_format} можно было
	// однозначно отделить от source_format последним дефисом. "@" допустим:
	// имя может содержать фиксированный суффикс @dpr (например "banner@2"
	// или "200x100@2").
	segmentNameChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@."
	// formatChars — символы, допустимые в формате.
	formatChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// Transform определяет режим кропа (и наличие trim) в обработке.
//
// Кроп и trim — НЕЗАВИСИМЫЕ фильтры: кроп выбирает режим обработки
// (center/smart/face/object), trim — обрезка однотонных полей. Transform
// НЕ является частью URL-грамматики: он вычисляется из поля crop пресета/
// custom при компиляции конфигурации.
type Transform string

const (
	// TransformCrop — центрированная обрезка (crop).
	TransformCrop Transform = "c"
	// TransformTrim — только обрезка однотонных полей (trim), без кропа.
	TransformTrim Transform = "t"
	// TransformCropTrim — trim + центрированный кроп (код "ct": trim в коде
	// последний; применяется сначала trim, затем центрированный кроп).
	TransformCropTrim Transform = "ct"
	// TransformSmartCrop — умная обрезка (smart-crop).
	TransformSmartCrop Transform = "sc"
	// TransformFaceCrop — обрезка по обнаруженным лицам.
	TransformFaceCrop Transform = "fc"
	// TransformObjectCrop — обрезка по обнаруженным объектам.
	TransformObjectCrop Transform = "oc"
	// TransformSmartCropTrim — trim + smart-crop (код "sct": применяется
	// сначала trim, затем smart-crop).
	TransformSmartCropTrim Transform = "sct"
	// TransformFaceCropTrim — trim + face-crop (код "fct").
	TransformFaceCropTrim Transform = "fct"
	// TransformObjectCropTrim — trim + object-crop (код "oct").
	TransformObjectCropTrim Transform = "oct"
)

// ValidTransform проверяет, что transform является допустимым.
// Разрешены: "", "c", "t", "ct", "sc", "fc", "oc", "sct", "fct", "oct".
// Trim-код допустим только последним в коде ("tc" и прочие комбинации
// отклоняются).
func ValidTransform(t Transform) bool {
	switch t {
	case TransformCrop, TransformTrim, TransformCropTrim,
		TransformSmartCrop, TransformFaceCrop, TransformObjectCrop,
		TransformSmartCropTrim, TransformFaceCropTrim, TransformObjectCropTrim:
		return true
	}
	return false
}

// SourceName — каноническое имя исходного файла.
type SourceName string

// NewSourceName создаёт SourceName с валидацией безопасности и длины.
//
// Имя исходника может содержать ЛЮБЫЕ Unicode-символы, допустимые в имени
// файла (буквы любых алфавитов, цифры, пробелы, дефис, подчёркивание,
// точка и т.д.). Запрещены только опасные для файловой системы/безопасности
// вещи: разделители пути ("/", "\\"), traversal-последовательность "..",
// нулевой байт и управляющие символы. Валидация выполняется по рунам (utf8).
func NewSourceName(s string) (SourceName, error) {
	if err := validateSourceName("source name", s, MaxSourceNameLen); err != nil {
		return "", err
	}
	return SourceName(s), nil
}

// String возвращает строковое представление.
func (n SourceName) String() string { return string(n) }

// Format — формат файла (source или output).
type Format string

// NewFormat создаёт Format с валидацией длины и символов.
func NewFormat(s string) (Format, error) {
	if err := validateComponent("format", s, formatChars, MaxFormatLen); err != nil {
		return "", err
	}
	return Format(s), nil
}

// String возвращает строковое представление.
func (f Format) String() string { return string(f) }

// SegmentName — имя сегмента URL: имя пресета ИЛИ custom-имя (размер).
//
// Имя может содержать фиксированный @dpr-суффикс (например "banner@2" или
// "200x100@2"). Валидность самого суффикса проверяется SplitPresetNameDPR
// при создании пресета; здесь проверяется только набор символов и длина.
type SegmentName string

// NewSegmentName создаёт SegmentName с валидацией длины и символов.
// Дефисы в имени сегмента запрещены (см. segmentNameChars).
func NewSegmentName(s string) (SegmentName, error) {
	if err := validateComponent("segment name", s, segmentNameChars, MaxPresetNameLen); err != nil {
		return "", err
	}
	return SegmentName(s), nil
}

// String возвращает строковое представление.
func (n SegmentName) String() string { return string(n) }

// PresetName — алиас SegmentName для публичного API.
type PresetName = SegmentName

// Dimension — значение измерения (ширина/высота) в пикселях.
type Dimension int

// NewDimension создаёт Dimension с проверкой на переполнение и верхний предел.
func NewDimension(v int) (Dimension, error) {
	if v < 0 {
		return 0, fmt.Errorf("dimension must be non-negative, got %d", v)
	}
	if v > MaxDimension {
		return 0, fmt.Errorf("dimension %d exceeds maximum %d", v, MaxDimension)
	}
	return Dimension(v), nil
}

// Int возвращает значение как int.
func (d Dimension) Int() int { return int(d) }

// DPR — device pixel ratio (множитель плотности пикселей). Значения
// создаются парсером URL/пресетов литералами после валидации диапазона.
type DPR int

// Int возвращает значение как int.
func (d DPR) Int() int { return int(d) }

// Valid проверяет, что DPR находится в допустимом диапазоне [1,3].
func (d DPR) Valid() bool { return d >= DefaultDPR && d <= MaxDPR }

// IsDefault сообщает, является ли DPR значением по умолчанию (1).
func (d DPR) IsDefault() bool { return d == DefaultDPR }

// Size описывает целевой размер изображения. nil-поле означает "авто".
// Значения неизменяемы: поля приватны, создаются через NewSize.
// Специальный размер "x" (original=true) означает сохранение исходного
// размера изображения.
type Size struct {
	width    *Dimension
	height   *Dimension
	original bool
}

// NewSize создаёт Size из опциональных измерений.
func NewSize(width, height *Dimension) (Size, error) {
	s := Size{width: width, height: height}
	if s.IsEmpty() {
		return Size{}, errors.New("size must specify width or height")
	}
	return s, nil
}

// NewOriginalSize создаёт Size, означающий сохранение исходного размера.
func NewOriginalSize() Size {
	return Size{original: true}
}

// Width возвращает ширину (nil, если не задана).
func (s Size) Width() *Dimension { return s.width }

// Height возвращает высоту (nil, если не задана).
func (s Size) Height() *Dimension { return s.height }

// IsOriginal возвращает true, если размер означает сохранение исходного
// размера изображения ("x").
func (s Size) IsOriginal() bool { return s.original }

// IsEmpty возвращает true, если размер полностью не задан (не original и
// без измерений).
func (s Size) IsEmpty() bool {
	return !s.original && s.width == nil && s.height == nil
}

// String возвращает каноническое строковое представление размера,
// например "120x80", "x50", "180x", "x".
func (s Size) String() string {
	if s.original {
		return "x"
	}
	var w, h string
	if s.width != nil {
		w = strconv.Itoa(s.width.Int())
	}
	if s.height != nil {
		h = strconv.Itoa(s.height.Int())
	}
	return w + "x" + h
}

// Pixels возвращает число пикселей для точного размера (обе стороны заданы).
// Возвращает ok=false, если хотя бы одна сторона не задана, размер является
// original или одна из сторон <= 0 (0x0 → деление на ноль в проверке
// переполнения).
func (s Size) Pixels() (int64, bool) {
	if s.original || s.width == nil || s.height == nil {
		return 0, false
	}
	w := int64(s.width.Int())
	h := int64(s.height.Int())
	if w <= 0 || h <= 0 {
		return 0, false
	}
	if w > math.MaxInt64/h {
		return 0, false
	}
	return w * h, true
}

// validateComponent проверяет длину и допустимые символы компонента.
func validateComponent(what, s, allowed string, maxLen int) error {
	if s == "" {
		return fmt.Errorf("%s is empty", what)
	}
	if len(s) > maxLen {
		return fmt.Errorf("%s length %d exceeds maximum %d", what, len(s), maxLen)
	}
	for _, r := range s {
		if !strings.ContainsRune(allowed, r) {
			return fmt.Errorf("%s contains invalid character %q", what, r)
		}
	}
	return nil
}

// validateSourceName проверяет имя исходного файла: длина, отсутствие
// разделителей пути, traversal-сегментов, нулевого байта и управляющих
// символов. Остальные Unicode-символы разрешены.
func validateSourceName(what, s string, maxLen int) error {
	if s == "" {
		return fmt.Errorf("%s is empty", what)
	}
	if len(s) > maxLen {
		return fmt.Errorf("%s length %d exceeds maximum %d", what, len(s), maxLen)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("%s contains traversal segment \"..\"", what)
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			// Управляющие символы, включая нулевой байт (\x00).
			return fmt.Errorf("%s contains control character %q", what, r)
		case r == '/' || r == '\\':
			return fmt.Errorf("%s contains path separator %q", what, r)
		}
	}
	return nil
}
