// Package asset реализует доменный слой канонических asset URL и связанных
// immutable value objects.
//
// Пакет не зависит от HTTP, файловой системы, ImageMagick, кэша и загрузчика
// конфигурации. Все value objects неизменяемы (immutable): их поля приватны,
// а значения создаются только через конструкторы, выполняющие валидацию.
//
// URL-грамматика версионирована (v1):
//
//	/v1/{path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}
//	/v1/{path}/{source_name}-{source_format}/{preset_name}@{dpr}.{output_format}
//
// transform — один из кодов: "c" (crop), "t" (trim), "ct" (crop затем trim),
// либо отсутствует (тогда применяется resize). dpr — множитель плотности
// пикселей: отсутствие суффикса означает 1, явно допустимы только 2 или 3.
// size "x" означает сохранение исходного размера изображения.
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
	// MaxPathLen — максимальная длина пути (без ведущего "/v1/").
	MaxPathLen = 512
	// MaxSourceNameLen — максимальная длина имени исходника.
	MaxSourceNameLen = 128
	// MaxFormatLen — максимальная длина формата (source/output).
	MaxFormatLen = 16
	// MaxPresetNameLen — максимальная длина имени пресета.
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
	// pathChars — символы, допустимые в пути: буквы, цифры, "-", "_", "/".
	pathChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_/"
	// nameChars — символы, допустимые в имени исходника.
	nameChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_."
	// presetNameChars — символы, допустимые в имени пресета. Дефисы
	// запрещены, чтобы имя пресета в URL
	// {source_name}-{source_format}/{preset_name}.{output_format} можно было
	// однозначно отделить от source_format последним дефисом.
	presetNameChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_."
	// formatChars — символы, допустимые в формате.
	formatChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// Version — версия URL-грамматики.
type Version string

const (
	// V1 — текущая версия URL-грамматики.
	V1 Version = "v1"
)

// ValidVersion сообщает, является ли v допустимой версией.
func ValidVersion(v Version) bool { return v == V1 }

// Transform определяет режим обработки изображения.
type Transform string

const (
	// TransformCrop — обрезка (crop).
	TransformCrop Transform = "c"
	// TransformTrim — обрезка по краям (trim).
	TransformTrim Transform = "t"
	// TransformCropTrim — последовательное применение crop и trim.
	TransformCropTrim Transform = "ct"
)

// ValidTransform проверяет, что transform является допустимым.
// Разрешены ровно "c", "t" и "ct"; любые другие комбинации (включая "tc")
// отклоняются. Пустой transform допустим (означает resize).
func ValidTransform(t Transform) bool {
	return t == TransformCrop || t == TransformTrim || t == TransformCropTrim
}

// SourceName — каноническое имя исходного файла.
type SourceName string

// NewSourceName создаёт SourceName с валидацией длины и символов.
func NewSourceName(s string) (SourceName, error) {
	if err := validateComponent("source name", s, nameChars, MaxSourceNameLen); err != nil {
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

// PresetName — имя пресета.
type PresetName string

// NewPresetName создаёт PresetName с валидацией длины и символов.
// Дефисы в имени пресета запрещены (см. presetNameChars).
func NewPresetName(s string) (PresetName, error) {
	if err := validateComponent("preset name", s, presetNameChars, MaxPresetNameLen); err != nil {
		return "", err
	}
	return PresetName(s), nil
}

// String возвращает строковое представление.
func (n PresetName) String() string { return string(n) }

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

// DPR — device pixel ratio (множитель плотности пикселей).
type DPR int

// NewDPR создаёт DPR с проверкой диапазона. Допустимы значения 1 (default,
// отсутствие суффикса), 2 и 3 (явные). Явная передача 0 или 1 отклоняется
// на уровне парсера, а не здесь.
func NewDPR(v int) (DPR, error) {
	if v < DefaultDPR {
		return 0, fmt.Errorf("dpr must be in [%d,%d], got %d", DefaultDPR, MaxDPR, v)
	}
	if v > MaxDPR {
		return 0, fmt.Errorf("dpr must be in [%d,%d], got %d", DefaultDPR, MaxDPR, v)
	}
	return DPR(v), nil
}

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
// Возвращает ok=false, если хотя бы одна сторона не задана или размер
// является original.
func (s Size) Pixels() (int64, bool) {
	if s.original || s.width == nil || s.height == nil {
		return 0, false
	}
	w := int64(s.width.Int())
	h := int64(s.height.Int())
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
