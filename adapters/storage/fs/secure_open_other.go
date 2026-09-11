//go:build !windows && !unix

// Generic fallback для платформ без O_NOFOLLOW-эквивалента (например, plan9).
// Проверка выполняется через walkComponentsNotSymlink, а безопасное открытие
// ограничено обычным os.OpenFile: гарантия ухудшена до best-effort
// (документировано в secure.go).

package fs

import (
	"os"
	"path/filepath"
)

// secureOpenFile на платформах без native no-follow поддержки открывает файл
// обычным способом; защита от symlink-escape обеспечивается проверкой
// компонентов пути перед операцией (best-effort, TOCTOU-окно остаётся).
func secureOpenFile(root string, rel string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(filepath.Join(root, rel), flag, perm)
}
