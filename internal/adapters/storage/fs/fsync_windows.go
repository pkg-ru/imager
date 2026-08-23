//go:build windows

package fs

// fsyncDir на Windows: FlushFileBuffers для каталога не поддерживается
// (каталоги открываются только с FILE_FLAG_BACKUP_SEMANTICS, а flush
// каталога на многих Windows-ФС возвращает ошибку или не даёт гарантий).
// Поэтому на Windows это best-effort no-op: возвращаем nil, чтобы не
// ломать Publish/Delete на платформах, где durability каталога недостижима.
// На Unix (fsync_unix.go) ошибки возвращаются вызывающему (У10).
func fsyncDir(dir string) error {
	_ = dir
	return nil
}
