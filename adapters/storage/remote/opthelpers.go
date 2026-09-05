package remote

import (
	"gitverse.ru/pkg-ru/imager/domain/object"
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
