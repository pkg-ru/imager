package remote

import (
	"context"
	"time"

	"github.com/pkg-ru/imager/domain/object"
)

// IsConnErr отличает ошибки соединения (требуют переподключения/discard и
// повторной попытки) от бизнес-ошибок (NotFound, Conflict, Quota, UnsafePath,
// Unavailable), при которых соединение можно освободить без ретрая.
//
// Единый классификатор для адаптеров ftp и sftp: оба набора условий
// совпадают — все типизированные ошибки
// domain/object считаются бизнес-ошибками, всё остальное (IO, SSH, timeout)
// — ошибкой соединения.
func IsConnErr(err error) bool {
	if object.IsNotFound(err) || object.IsConflict(err) || object.IsQuota(err) || object.IsUnsafePath(err) || object.IsUnavailable(err) {
		return false
	}
	return err != nil
}

// Attempts нормализует настроенное число попыток операции: значения < 1
// превращаются в 1.
func Attempts(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// WithOpTimeout оборачивает ctx таймаутом операции, если задан положительный
// таймаут d. Возвращает cancel-функцию, которую вызывающий обязан вызвать
// (defer cancel()). При d <= 0 контекст не изменяется.
func WithOpTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
