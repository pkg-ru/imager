// Ориентация изображения: EXIF auto-orient, ручной поворот и отражение.
//
// Спецификация OrientationSpec описывает ПАКЕТ ориентационных операций,
// применяемых ДО resize/crop/trim (порядок: auto-orient → rotate → flip).
// Значения приходят из доверенного конфигурации (глобальный дефолт
// processing.default-* и пресеты), а не из URL.
package processing

import (
	"fmt"
	"strings"
)

// Rotation — угол поворота по часовой стрелке. Закрытый enum: только
// ортогональные углы, поддерживаемые движком libvips (Rotate без расширения
// холста).
type Rotation int

const (
	// RotationNone — без поворота.
	RotationNone Rotation = 0
	// Rotation90 — поворот на 90° по часовой стрелке.
	Rotation90 Rotation = 90
	// Rotation180 — поворот на 180°.
	Rotation180 Rotation = 180
	// Rotation270 — поворот на 270° по часовой стрелке (= 90° против).
	Rotation270 Rotation = 270
)

// ValidRotation проверяет допустимость угла поворота.
func ValidRotation(r Rotation) bool {
	switch r {
	case RotationNone, Rotation90, Rotation180, Rotation270:
		return true
	default:
		return false
	}
}

// String возвращает строковое представление угла ("", "90", "180", "270").
func (r Rotation) String() string {
	if r == RotationNone {
		return ""
	}
	return fmt.Sprintf("%d", int(r))
}

// ParseRotation разбирает строку в Rotation. Допустимы "", "none",
// "0" (эквивалент отсутствия), "90", "180", "270". Регистронезависимо.
func ParseRotation(s string) (Rotation, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return RotationNone, nil
	case "0":
		return RotationNone, nil
	case "90":
		return Rotation90, nil
	case "180":
		return Rotation180, nil
	case "270":
		return Rotation270, nil
	default:
		return RotationNone, fmt.Errorf("invalid rotation %q, must be one of: none, 90, 180, 270", s)
	}
}

// FlipMode — режим отражения. Закрытый enum.
type FlipMode string

const (
	// FlipNone — без отражения.
	FlipNone FlipMode = ""
	// FlipHorizontal — зеркало слева-направо.
	FlipHorizontal FlipMode = "horizontal"
	// FlipVertical — отражение сверху-вниз.
	FlipVertical FlipMode = "vertical"
)

// ValidFlip проверяет допустимость режима отражения.
func ValidFlip(f FlipMode) bool {
	switch f {
	case FlipNone, FlipHorizontal, FlipVertical:
		return true
	default:
		return false
	}
}

// ParseFlip разбирает строку в FlipMode. Допустимы "", "none",
// "horizontal", "vertical". Регистронезависимо.
func ParseFlip(s string) (FlipMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return FlipNone, nil
	case "horizontal":
		return FlipHorizontal, nil
	case "vertical":
		return FlipVertical, nil
	default:
		return FlipNone, fmt.Errorf("invalid flip %q, must be one of: none, horizontal, vertical", s)
	}
}

// OrientationSpec — immutable спецификация ориентационных операций.
//
// Применяется процессором СТРОГО до resize/crop/trim в порядке:
//  1. AutoOrient — автоматический поворот по EXIF Orientation;
//  2. Rotate — ручной поворот на 90/180/270°;
//  3. Flip — отражение horizontal/vertical.
//
// Нулевая спецификация (AutoOrient=false, Rotate=none, Flip=none)
// полностью отключает ориентационную обработку.
type OrientationSpec struct {
	// AutoOrient — применять ли автоматический поворот по EXIF Orientation
	// при загрузке.
	AutoOrient bool
	// Rotate — дополнительный фиксированный поворот (0 = нет).
	Rotate Rotation
	// Flip — отражение ("" = нет).
	Flip FlipMode
}

// NewOrientationSpec создаёт OrientationSpec с валидацией.
func NewOrientationSpec(autoOrient bool, rotate Rotation, flip FlipMode) (*OrientationSpec, error) {
	if !ValidRotation(rotate) {
		return nil, fmt.Errorf("orientation spec: invalid rotation %d, must be one of 0, 90, 180, 270", int(rotate))
	}
	if !ValidFlip(flip) {
		return nil, fmt.Errorf("orientation spec: invalid flip %q, must be one of: none, horizontal, vertical", flip)
	}
	return &OrientationSpec{AutoOrient: autoOrient, Rotate: rotate, Flip: flip}, nil
}

// DefaultOrientation — спецификация по умолчанию: только EXIF auto-orient.
func DefaultOrientation() *OrientationSpec {
	return &OrientationSpec{AutoOrient: true}
}

// IsZero сообщает, отключает ли спецификация всю ориентационную обработку.
func (s *OrientationSpec) IsZero() bool {
	return s == nil || (!s.AutoOrient && s.Rotate == RotationNone && s.Flip == FlipNone)
}

// Validate проверяет корректность спецификации.
func (s *OrientationSpec) Validate() error {
	if s == nil {
		return nil
	}
	if !ValidRotation(s.Rotate) {
		return fmt.Errorf("orientation spec: invalid rotation %d", int(s.Rotate))
	}
	if !ValidFlip(s.Flip) {
		return fmt.Errorf("orientation spec: invalid flip %q", s.Flip)
	}
	return nil
}

// String возвращает человекочитаемое описание (для логов/диагностики).
func (s *OrientationSpec) String() string {
	if s == nil {
		return "auto-orient=true rotate= flip="
	}
	parts := []string{fmt.Sprintf("auto-orient=%v", s.AutoOrient)}
	if s.Rotate != RotationNone {
		parts = append(parts, "rotate="+s.Rotate.String())
	}
	if s.Flip != FlipNone {
		parts = append(parts, "flip="+string(s.Flip))
	}
	return strings.Join(parts, " ")
}
