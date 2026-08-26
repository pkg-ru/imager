// ICC color management (Фаза 5a): политика управления цветом и определение
// sRGB-совместимых профилей БЕЗ lcms-конверсии.
//
// Файл без build-tag: логика платформенно-независима и тестируется в любой
// сборке (см. color_test.go). Применение политики к vips.ImageRef — в
// process_libvips.go (applyColorManagement).
package libvips

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// ColorMode — политика управления цветом (ICC color management).
type ColorMode string

const (
	// ColorStrip — удалять ICC-профиль при обработке (текущее поведение,
	// дефолт для обратной совместимости: stripAllMetadata удаляет профиль
	// при экспорте).
	ColorStrip ColorMode = "strip"
	// ColorTransform — конвертировать embedded-профиль в стандартный sRGB
	// (через PCS) ПЕРЕД пиксельной обработкой. После конверсии изображение
	// обрабатывается как обычное sRGB; профиль при экспорте удаляется.
	ColorTransform ColorMode = "transform"
	// ColorKeep — сохранять embedded-профиль в выходном изображении
	// (профиль НЕ удаляется при экспорте).
	ColorKeep ColorMode = "keep"
)

// ParseColorMode разбирает строку политики color management. Пустая строка =
// strip (дефолт). Неизвестное значение — ошибка (fail-fast при декодировании
// конфигурации).
func ParseColorMode(s string) (ColorMode, error) {
	switch ColorMode(s) {
	case "", ColorStrip:
		return ColorStrip, nil
	case ColorTransform:
		return ColorTransform, nil
	case ColorKeep:
		return ColorKeep, nil
	default:
		return "", fmt.Errorf("unknown color mode %q (allowed: strip, transform, keep)", s)
	}
}

// Valid сообщает, является ли режим известной политикой.
func (m ColorMode) Valid() bool {
	switch m {
	case ColorStrip, ColorTransform, ColorKeep:
		return true
	default:
		return false
	}
}

// iccProfileHeaderSize — минимальный размер заголовка ICC-профиля.
const iccProfileHeaderSize = 128

// isSRGBProfile определяет по сигнатуре/имени, является ли embedded-профиль
// sRGB-совместимым (sRGB IEC61966-2.1 и родственные: "sRGB v2", "sRGB v4" и
// т.п.). Проверка выполняется БЕЗ lcms-конверсии: читается заголовок профиля
// (сигнатура 'acsp', цветовое пространство RGB) и тег описания 'desc'.
//
// Отказоустойчивость: при любых сомнениях (битый заголовок, отсутствие тега
// описания, не-RGB пространство) возвращается false — такой профиль будет
// обработан полным конвейером (transform) либо вызовет strip при ошибке.
func isSRGBProfile(data []byte) bool {
	if len(data) < iccProfileHeaderSize {
		return false
	}
	// Сигнатура профиля 'acsp' (offset 36).
	if !bytes.Equal(data[36:40], []byte("acsp")) {
		return false
	}
	// Цветовое пространство профиля должно быть RGB (offset 16).
	if !bytes.Equal(data[16:20], []byte("RGB ")) {
		return false
	}
	name := iccDescription(data)
	if name == "" {
		return false
	}
	return strings.Contains(strings.ToLower(name), "srgb")
}

// iccDescription извлекает имя профиля из тега 'desc' (тип 'desc').
// Структура тега: 4 байта типа, 4 байта reserved, 4 байта длины ASCII-строки,
// ASCII-строка. Возвращает "" при отсутствии/повреждении тега.
func iccDescription(data []byte) string {
	if len(data) < iccProfileHeaderSize+4 {
		return ""
	}
	count := int(binary.BigEndian.Uint32(data[128:132]))
	for i := 0; i < count; i++ {
		base := 132 + i*12
		if base+12 > len(data) {
			return ""
		}
		if string(data[base:base+4]) != "desc" {
			continue
		}
		off := int(binary.BigEndian.Uint32(data[base+4 : base+8]))
		size := int(binary.BigEndian.Uint32(data[base+8 : base+12]))
		if off < 0 || size < 16 || off+size > len(data) {
			return ""
		}
		if !bytes.Equal(data[off:off+4], []byte("desc")) {
			return ""
		}
		asciiLen := int(binary.BigEndian.Uint32(data[off+8 : off+12]))
		if asciiLen <= 0 || off+12+asciiLen > len(data) {
			return ""
		}
		return string(data[off+12 : off+12+asciiLen])
	}
	return ""
}

// colorNeedsTransform сообщает, требуется ли lcms-конверсия изображения в
// sRGB при режиме transform:
//   - без профиля и уже в sRGB colorspace — конверсия НЕ нужна (fast-path);
//   - с sRGB-совместимым профилем — конверсия НЕ нужна (fast-path, профиль
//     и так описывает sRGB);
//   - с не-sRGB профилем ИЛИ без профиля в не-sRGB colorspace (например
//     CMYK) — конверсия требуется.
//
// colorspaceIsSRGB — флаг того, что colorspace загруженного изображения уже
// sRGB (например vips.Interpretation() == InterpretationSRGB). Порядок
// проверки (дешёвые проверки первыми): режим, наличие профиля, colorspace,
// затем разбор байтов профиля.
func colorNeedsTransform(mode ColorMode, hasICC bool, srgbProfile bool, colorspaceIsSRGB bool) bool {
	if mode != ColorTransform {
		return false
	}
	if hasICC {
		// Профиль есть: конверсия нужна только если он не sRGB-совместим.
		return !srgbProfile
	}
	// Профиля нет: конверсия нужна, только если изображение уже не в sRGB.
	return !colorspaceIsSRGB
}
