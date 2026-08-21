package fs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// copyBufPool — переиспользуемый буфер для io.CopyBuffer в writeTemp.
// Аллокация 32KB на каждый вызов writeTemp заменяется на взятие буфера из
// пула, что снижает нагрузку на GC при частых публикациях.
var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024)
		return &b
	},
}

// unsafe оборачивает ошибку в типизированную object.ErrUnsafePath с ключом.
func unsafe(key object.ObjectKey, err error) error {
	if err == nil {
		return nil
	}
	if object.IsUnsafePath(err) {
		return err
	}
	return &unsafePathError{msg: fmt.Sprintf("fs: unsafe path for key %q: %v", key, err)}
}

// unsafeErr — то же, что unsafe, но для мест, где возвращается метаданные.
func unsafeErr(key object.ObjectKey, err error) error { return unsafe(key, err) }

// isSymlinkErr сообщает, является ли ошибка признаком symlink-escape.
func isSymlinkErr(err error) bool {
	return errors.Is(err, errSymlinkEscape)
}

// isExist сообщает, является ли ошибка признаком существования файла
// (EEXIST / ERROR_ALREADY_EXISTS / ERROR_FILE_EXISTS).
func isExist(err error) bool {
	return errors.Is(err, os.ErrExist)
}

// writeTemp копирует src в dst и возвращает число записанных байт.
func writeTemp(tmp *os.File, src io.Reader) (int64, error) {
	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)
	n, err := io.CopyBuffer(tmp, src, *bufp)
	return n, err
}
