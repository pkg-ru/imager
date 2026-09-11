package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"gitverse.ru/pkg-ru/imager/domain/object"
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

// ctxReader — обёртка reader, проверяющая ctx на каждом чтении.
// Позволяет прервать копирование в temp-файл при отмене контекста
// (GenerateTimeout/shutdown), не дожидаясь завершения медленного источника.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

// Read реализует io.Reader: проверяет отмену контекста до чтения.
func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// writeTemp копирует src в dst и возвращает число записанных байт.
// Если ctx != nil, копирование прерывается при отмене контекста.
func writeTemp(tmp *os.File, src io.Reader, ctx context.Context) (int64, error) {
	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)
	if ctx != nil {
		src = ctxReader{ctx: ctx, r: src}
	}
	n, err := io.CopyBuffer(tmp, src, *bufp)
	return n, err
}
