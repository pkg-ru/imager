// Package fs реализует безопасный filesystem-адаптер для SourceStore и
// ResultStore, а также кэширующие примитивы (квота, LRU, janitor).
//
// Гарантии безопасности (root containment):
//   - Ключи нормализуются на доменном уровне (object.ObjectKey) и повторно
//     проверяются здесь: запрещены ".."-сегменты, абсолютные пути, обратные
//     слеши и зарезервированные сегменты.
//   - Для операций чтения/удаления выполняется platform-specific проверка
//     компонентов пути на символьные ссылки/junction/reparse points
//     (см. secure_open_*.go). Эта проверка best-effort: между проверкой и
//     операцией возможен TOCTOU, поэтому на Unix файлы открываются с
//     O_NOFOLLOW, а на Windows используется запрет reparse points.
//
// FTP-адаптер в будущем реализует только SourceStore; S3/external disk —
// те же storage.ResultStore/SourceStore контракты.
package fs

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// reservedSegmentPrefix — сегменты, зарезервированные хранилищем для
// внутренних файлов (временные файлы публикации и файлы метаданных).
// Ключи, содержащие такие сегменты, отклоняются.
var reservedSegmentPrefix = ".tmp-"

// reservedSegment — имя каталога внутренних метаданных.
const reservedSegment = ".meta"

// Лимиты длины пути (защита от ENAMETOOLONG и неожиданного поведения ФС).
const (
	// maxSegmentLen — максимальная длина одного сегмента (обычный лимит
	// компонента имени файла на большинстве ФС).
	maxSegmentLen = 255
	// maxPathLen — максимальная длина итогового относительного пути.
	maxPathLen = 4096
)

// isReservedSegment сообщает, является ли сегмент зарезервированным на
// Windows: device names (CON, PRN, AUX, NUL, COM1–COM9, LPT1–LPT9,
// регистронезависимо) или имя с завершающей точкой/пробелом. Такие имена
// дают неожиданное поведение ФС на Windows, поэтому отклоняются на всех
// платформах для консистентности.
func isReservedSegment(p string) bool {
	// Завершающие точки или пробелы недопустимы на Windows.
	if strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") {
		return true
	}
	upper := strings.ToUpper(p)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	// COM1–COM9, LPT1–LPT9.
	if len(upper) == 4 {
		prefix := upper[:3]
		if (prefix == "COM" || prefix == "LPT") && upper[3] >= '1' && upper[3] <= '9' {
			return true
		}
	}
	return false
}

// cleanRel нормализует ObjectKey в безопасный относительный путь внутри root.
// Гарантирует root containment: ключ не может выйти за пределы root через
// "..", абсолютные пути или разделители Windows.
func cleanRel(root string, key object.ObjectKey) (string, error) {
	if root == "" {
		return "", fmt.Errorf("fs: empty root")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("fs: abs root: %w", err)
	}
	return cleanRelAbs(absRoot, key)
}

// cleanRelAbs — то же, что cleanRel, но принимает уже абсолютный корень и не
// вызывает filepath.Abs повторно (только filepath.Join и проверка within).
func cleanRelAbs(absRoot string, key object.ObjectKey) (string, error) {
	raw := string(key)
	if raw == "" {
		return "", fmt.Errorf("fs: empty key")
	}
	// Запрещаем обратный слеш (Windows-разделитель) в ключе до нормализации,
	// иначе filepath.ToSlash на Windows превратит его в "/" и проверка
	// сегментов не сработает.
	if strings.ContainsRune(raw, '\\') {
		return "", errUnsafeContainment()
	}
	// NUL-байт недопустим в путях на всех платформах; отклоняем его
	// типизированной ошибкой containment.
	if strings.ContainsRune(raw, 0) {
		return "", errUnsafeContainment()
	}

	// Нормализуем: "/" как разделитель, убираем ведущие слеши.
	clean := filepath.ToSlash(raw)
	clean = strings.Trim(clean, "/")
	parts := strings.Split(clean, "/")

	var out []string
	for _, p := range parts {
		switch p {
		case "", ".":
			continue
		case "..":
			// ".." отклоняется безусловно (консервативно). Даже если ".."
			// можно схлопнуть (a/../b), мы не разрешаем его: это упрощает
			// инвариант и исключает неоднозначность.
			return "", errUnsafeContainment()
		default:
			// Запрещаем обратный слеш в сегменте (Windows-разделитель).
			if strings.ContainsAny(p, "\\") {
				return "", errUnsafeContainment()
			}
			// Сегменты могут быть резервными именами (для коллизий) или
			// управляющими файлами. Такие ключи недоступны как пользовательские.
			if p == reservedSegment || strings.HasPrefix(p, reservedSegmentPrefix) {
				return "", errUnsafeContainment()
			}
			// Windows reserved device names и завершающие точки/пробелы.
			if isReservedSegment(p) {
				return "", errUnsafeContainment()
			}
			// Защита от ENAMETOOLONG: лимит длины сегмента.
			if len(p) > maxSegmentLen {
				return "", errUnsafeContainment()
			}
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "", errUnsafeContainment()
	}

	rel := filepath.Join(out...)
	// Защита от ENAMETOOLONG: лимит длины итогового пути.
	if len(rel) > maxPathLen {
		return "", errUnsafeContainment()
	}
	full := filepath.Join(absRoot, rel)
	if !within(absRoot, full) {
		return "", errUnsafeContainment()
	}
	return rel, nil
}

// within проверяет, что child находится внутри parent (или равен ему).
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
