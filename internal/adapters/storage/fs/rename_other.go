//go:build !windows && !unix

package fs

import "os"

// renameReplace — best-effort атомарная замена файла full содержимым
// tmpPath на платформах без специальной поддержки. os.Rename не гарантирует
// атомарность перезаписи на всех ФС, но является разумным fallback.
func renameReplace(tmpPath, full string) error {
	return os.Rename(tmpPath, full)
}
