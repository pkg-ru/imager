//go:build windows

package fs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// secureOpenFile открывает файл с запретом trailing reparse point.
//
// На Windows символьные ссылки и junction — это reparse points. Обычное
// os.OpenFile прозрачно следует за reparse point и позволяет открыть файл
// за пределами root. Здесь файл открывается через CreateFile с флагом
// FILE_FLAG_OPEN_REPARSE_POINT: в этом режиме CreateFile возвращает handle
// на саму reparse point (а не на цель), что позволяет проверить атрибуты и
// отбросить symlink/junction. Это исключает TOCTOU-окно для последнего
// компонента. Промежуточные каталоги проверяются отдельной функцией
// walkComponentsNotSymlink (best-effort: окно между проверкой и операцией
// остаётся для промежуточных каталогов). Полная атомарность по всему пути
// через один CreateFile на Windows не достигается (промежуточные каталоги
// следуют reparse points); основной выигрыш от атомарного открытия — на
// Linux (см. secure_open_linux.go).
func secureOpenFile(root string, rel string, flag int, _ os.FileMode) (*os.File, error) {
	path := filepath.Join(root, rel)
	var access uint32
	switch {
	case flag&os.O_WRONLY != 0:
		access = windows.GENERIC_WRITE
	case flag&os.O_RDWR != 0:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	default:
		access = windows.GENERIC_READ
	}

	var disposition uint32
	switch {
	case flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		disposition = windows.CREATE_NEW
	case flag&os.O_CREATE != 0 && flag&os.O_TRUNC != 0:
		disposition = windows.CREATE_ALWAYS
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	case flag&os.O_TRUNC != 0:
		disposition = windows.TRUNCATE_EXISTING
	default:
		disposition = windows.OPEN_EXISTING
	}

	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		pathp,
		access,
		share,
		nil,
		disposition,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	// Проверяем, что открытый объект — обычный файл, а не reparse point.
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, &os.PathError{Op: "attribute", Path: path, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return nil, errSymlinkEscape
	}
	return os.NewFile(uintptr(h), path), nil
}
