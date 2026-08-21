//go:build unix

package fs

import "os"

// fsyncDir синхронизирует каталог после rename/delete, чтобы запись была
// durable (пережила сбой питания). На Unix открывается каталог и вызывается
// fsync. Ошибки игнорируются: fsync каталога не поддерживается на всех ФС
// (например, некоторые сетевые ФС), поэтому это best-effort гарантия.
func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}
