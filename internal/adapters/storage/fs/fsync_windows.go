//go:build windows

package fs

import (
	"golang.org/x/sys/windows"
)

// fsyncDir на Windows: FlushFileBuffers для каталога не поддерживается
// напрямую через os.File.Sync (каталоги открываются только с
// FILE_FLAG_BACKUP_SEMANTICS). Здесь каталог открывается через
// windows.CreateFile с FILE_FLAG_BACKUP_SEMANTICS и выполняется
// FlushFileBuffers, что делает rename/delete durable после сбоя питания.
// Реализация best-effort: все ошибки игнорируются (как на Unix).
func fsyncDir(dir string) {
	pathp, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return
	}
	h, err := windows.CreateFile(
		pathp,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.FlushFileBuffers(h)
}
