//go:build windows

package fs

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// renameReplace атомарно заменяет файл full содержимым tmpPath.
//
// На Windows os.Rename не атомарен при перезаписи существующего файла:
// MoveFile (без флага MOVEFILE_REPLACE_EXISTING) возвращает
// ERROR_ALREADY_EXISTS, а перезапись открытого/занятого файла может
// вернуть ERROR_ACCESS_DENIED. Здесь используется MoveFileEx с флагами
// MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH, что даёт атомарный
// replace и гарантирует, что запись переживёт сбой питания.
//
// ERROR_ACCESS_DENIED типичен, когда целевой файл временно открыт другим
// процессом (например, читателем). Выполняем несколько ретраев с короткой
// паузой, чтобы дать конкуренту закрыть файл, затем возвращаем ошибку.
func renameReplace(tmpPath, full string) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := moveFileExReplace(tmpPath, full)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isAccessDenied(err) {
			break
		}
		if attempt+1 < maxAttempts {
			time.Sleep(10 * time.Millisecond * time.Duration(attempt+1))
		}
	}
	return &os.PathError{Op: "rename", Path: full, Err: lastErr}
}

func moveFileExReplace(tmpPath, full string) error {
	from, err := windows.UTF16PtrFromString(tmpPath)
	if err != nil {
		return &os.PathError{Op: "rename", Path: full, Err: err}
	}
	to, err := windows.UTF16PtrFromString(full)
	if err != nil {
		return &os.PathError{Op: "rename", Path: full, Err: err}
	}
	const flags = windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH
	if err := windows.MoveFileEx(from, to, flags); err != nil {
		return &os.PathError{Op: "rename", Path: full, Err: err}
	}
	return nil
}

func isAccessDenied(err error) bool {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err == windows.ERROR_ACCESS_DENIED
	}
	return false
}
