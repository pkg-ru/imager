//go:build unix

package fs

import "os"

// fsyncDir синхронизирует каталог после rename/delete, чтобы запись была
// durable (пережила сбой питания). На Unix открывается каталог и вызывается
// fsync. Ошибка возвращается вызывающему: fsync каталога не поддерживается
// на всех ФС (например, некоторые сетевые ФС), поэтому вызывающий решает,
// логировать ли её.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
