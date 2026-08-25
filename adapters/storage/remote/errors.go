package remote

import (
	"context"
	"errors"
	"fmt"

	"github.com/pkg-ru/imager/domain/object"
)

// MapError маппит низкоуровневую ошибку удалённого хранилища в типизированную
// ошибку domain/object. Правила:
//   - object.ErrNotFound / object.ErrConflict / object.ErrQuota /
//     object.ErrUnsafePath / object.ErrUnavailable пробрасываются как есть;
//   - context.Canceled / context.DeadlineExceeded пробрасываются как есть
//     (application-слой обрабатывает их отдельно);
//   - всё остальное (сетевые сбои, отказы backend) → object.ErrUnavailable.
func MapError(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, object.ErrNotFound),
		errors.Is(err, object.ErrConflict),
		errors.Is(err, object.ErrQuota),
		errors.Is(err, object.ErrUnsafePath),
		errors.Is(err, object.ErrUnavailable),
		errors.Is(err, object.ErrForbidden):
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return fmt.Errorf("%s: %w", op, object.ErrUnavailable)
	}
}

// NotFound оборачивает ошибку в object.ErrNotFound с ключом.
func NotFound(key object.ObjectKey) error {
	return &object.NotFoundError{Key: key}
}

// Conflict оборачивает ошибку в object.ErrConflict с ключом.
func Conflict(key object.ObjectKey) error {
	return &object.ConflictError{Key: key}
}

// Unsafe оборачивает ошибку в object.ErrUnsafePath с ключом.
func Unsafe(key object.ObjectKey, err error) error {
	if err == nil {
		return object.ErrUnsafePath
	}
	return fmt.Errorf("unsafe path for key %q: %w", key, object.ErrUnsafePath)
}
