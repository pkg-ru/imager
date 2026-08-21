//go:build unix

package fs

import "os"

// renameReplace атомарно заменяет файл full содержимым tmpPath.
// На Unix os.Rename атомарен и перезаписывает существующий файл.
func renameReplace(tmpPath, full string) error {
	return os.Rename(tmpPath, full)
}
