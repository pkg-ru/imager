package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pkg-ru/imager/domain/object"
)

// errSymlinkEscape — внутренняя ошибка: компонент пути является символьной
// ссылкой/junction/reparse point и не может быть использован для операций
// чтения/публикации/удаления. Оборачивается в object.ErrUnsafePath.
var errSymlinkEscape = errors.New("fs: symlink/reparse-point component in path")

// unsafePathError — обёртка над object.ErrUnsafePath с человекочитаемым
// описанием причины.
type unsafePathError struct{ msg string }

func (e *unsafePathError) Error() string { return e.msg }

func (e *unsafePathError) Unwrap() error { return object.ErrUnsafePath }

// walkComponentsNotSymlink проверяет все существующие компоненты пути от root
// до самого full включительно: ни один не должен быть символьной ссылкой
// (symlink), junction или reparse point. Сама root может быть symlink
// (конфигурация); проверка начинается с первого каталога внутри root.
// Несуществующие компоненты пропускаются (каталоги появляются при publish,
// при чтении отсутствие вернёт not-found).
//
// Финальный компонент full тоже проверяется через Lstat: даже если операция
// над ним (например os.Remove при Delete) не следует по ссылке, политика
// запрещает любые операции с ключом, чей путь проходит через ссылку.
//
// Эта проверка best-effort: она не даёт атомарной гарантии (TOCTOU-окно
// между проверкой и операцией остаётся). На Unix жёсткую гарантию для
// последнего компонента даёт O_NOFOLLOW (secureOpenFile); на Windows —
// создание с FILE_FLAG_OPEN_REPARSE_POINT + проверка атрибутов.
func walkComponentsNotSymlink(root, full string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("fs: abs root: %w", err)
	}
	rel, err := filepath.Rel(absRoot, full)
	if err != nil {
		return errUnsafeContainment()
	}
	if rel == "." || rel == "" {
		return nil // сам root проверять не нужно.
	}
	// Идём от родительского каталога файла вверх до root, проверяя
	// промежуточные компоненты.
	cur := filepath.Dir(full)
	for {
		if samePath(cur, absRoot) {
			break
		}
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				// Каталог ещё не существует (например, будет создан при
				// publish): проверим его в процессе создания через
				// MkdirAll; для чтения этот каталог не может существовать.
				return nil
			}
			return fmt.Errorf("fs: lstat %q: %w", cur, err)
		}
		if isSymlink(info) {
			return fmt.Errorf("%w: %q", errSymlinkEscape, cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}

	// Финальный компонент: если full — символьная ссылка, блокируем любые
	// операции с этим путём. Несуществующий full — обычный случай для
	// Publish ДО создания файла и для чтения отсутствующего ключа.
	info, err := os.Lstat(full)
	if err == nil && isSymlink(info) {
		return fmt.Errorf("%w: %q", errSymlinkEscape, full)
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fs: lstat %q: %w", full, err)
	}
	return nil
}

// samePath сравнивает два пути лексически после Abs/Clean.
func samePath(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// isSymlink сообщает, является ли FileInfo символьной ссылкой/junction.
// Windows junction и symlink представляются через os.ModeSymlink.
func isSymlink(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}

// errUnsafeContainment создаёт ошибку containment, совместимую с
// object.ErrUnsafePath.
func errUnsafeContainment() error {
	return &unsafePathError{msg: "fs: key escapes root"}
}
