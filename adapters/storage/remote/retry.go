package remote

import (
	"context"
)

// NeverRetry — политика «без повторов»: ни одна ошибка не ретраится,
// операция выполняется ровно одной попыткой (внутренние ошибки возвращаются
// сразу). Используется, например, Publish в sftp.
func NeverRetry(error) bool { return false }

// RetrySpec описывает жизненный цикл соединения внутри retry-цикла: как
// получить соединение, как его закрыть и какие сырые ошибки считать
// ошибками соединения (подлежащими повтору). Заполняется адаптером
// (ftp/sftp); сам цикл един для всех.
type RetrySpec[C, V any] struct {
	// Acquire создаёт новое соединение (из пула или напрямую). Вызывается
	// в начале каждой попытки.
	Acquire func(ctx context.Context) (C, error)
	// Discard закрывает соединение и освобождает слот пула. Обязан быть
	// идемпотентным (Session.Discard идемпотентен).
	Discard func(C)
	// Policy классифицирует сырую ошибку операции: true — ошибка соединения,
	// требуется повторная попытка с новым соединением; false — вернуть
	// ошибку вызывающему немедленно.
	Policy func(error) bool
	// MapDialErr маппит ошибку Acquire для вызывающего (обычно MapError с
	// префиксом протокола).
	MapDialErr func(error) error
	// TakesOwnership сообщает (только для успешного результата), что
	// операция передала соединение результату (стриминг: соединение должно
	// жить до закрытия потока). В этом случае каркас не вызывает Discard —
	// владелец результата обязан вызвать его сам. nil трактуется как false.
	TakesOwnership func(V) bool
}

// Retry — общий retry-каркас операций удалённых хранилищ (используется
// адаптерами ftp и sftp вместо прежних построчно дублированных циклов):
//
//	acquire -> op -> классификация ошибки политикой ->
//	повторная попытка с новым соединением (до MaxAttempts) или возврат.
//
// Каждая серия попыток ограничена таймаутом opts.ReadTimeout (OpTimeout).
//
// Контракт op: возвращает результат, сырую ошибку raw (по ней Policy решает,
// ретраить ли) и замапленную ошибку mapped (возвращается вызывающему; при
// успехе обе равны nil). Ошибки, не прошедшие Policy, и исчерпанный ctx
// завершают цикл сразу; после исчерпания попыток возвращается последняя
// mapped-ошибка.
//
// Владение соединением: каркас закрывает соединение (Discard) после каждой
// неудачной попытки и после успешной — если операция не объявила передачу
// владения через spec.TakesOwnership. Это гарантирует освобождение слота
// пула при любом исходе (прежний каркас не освобождал слот при успехе).
// Ошибка Acquire повторов не требует и прерывает цикл немедленно.
func Retry[C, V any](ctx context.Context, opts ConnOptions, spec RetrySpec[C, V], op func(C) (V, error, error)) (V, error) {
	ctx, cancel := opts.OpTimeout(ctx)
	defer cancel()
	attempts := opts.Attempts()
	var lastErr error
	for range attempts {
		c, err := spec.Acquire(ctx)
		if err != nil {
			var zero V
			return zero, spec.MapDialErr(err)
		}
		// Очистка соединения выполняется один раз на итерации явным
		// замыканием (не defer-в-цикле, который накапливался бы до выхода
		// из функции). Повторный Discard безопасен: он идемпотентен.
		discarded := false
		discard := func() {
			if !discarded {
				discarded = true
				spec.Discard(c)
			}
		}
		v, raw, mapped := op(c)
		if raw == nil && mapped == nil {
			if spec.TakesOwnership == nil || !spec.TakesOwnership(v) {
				discard()
			}
			return v, nil
		}
		discard()
		lastErr = mapped
		if !spec.Policy(raw) {
			var zero V
			return zero, lastErr
		}
		if ctx.Err() != nil {
			var zero V
			return zero, lastErr
		}
	}
	var zero V
	return zero, lastErr
}
