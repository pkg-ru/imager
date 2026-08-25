package shared

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ErrOutputLimitExceeded — сигнал превышения лимита размера выхода,
// возвращаемый BoundedWriter.Write. Адаптеры обязаны проверять ExceededN()
// ПЕРВЫМ и маппить превышение в собственную типизированную LimitError;
// сам sentinel служит лишь для раннего прерывания записи.
var ErrOutputLimitExceeded = errors.New("output limit exceeded")

// BoundedWriter ограничивает запись max байт. При превышении лимита помечает
// exceeded, вызывает cancel (чтобы убить subprocess/прервать обработку) и
// возвращает ErrOutputLimitExceeded. Это application-level защита, не
// полагающаяся на внутренние лимиты процессора.
//
// Потокобезопасен: Write может вызываться из goroutine копирования stdout,
// а ExceededN читается после завершения subprocess.
type BoundedWriter struct {
	mu       sync.Mutex
	w        io.Writer
	max      int64
	n        int64
	exceeded bool
	cancel   context.CancelFunc
}

// NewBoundedWriter создаёт BoundedWriter поверх w с лимитом max байт.
// При превышении лимита вызывается cancel (если не nil) и Write возвращает
// ErrOutputLimitExceeded. max <= 0 означает "без лимита".
func NewBoundedWriter(w io.Writer, max int64, cancel context.CancelFunc) *BoundedWriter {
	return &BoundedWriter{w: w, max: max, cancel: cancel}
}

// Write реализует io.Writer. При превышении лимита помечает exceeded, зовёт
// cancel и возвращает ErrOutputLimitExceeded; данные при этом НЕ пишутся в w.
func (b *BoundedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.n+int64(len(p)) > b.max {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
		return 0, ErrOutputLimitExceeded
	}
	n, err := b.w.Write(p)
	b.n += int64(n)
	return n, err
}

// ExceededN возвращает флаг превышения и фактический размер записанных
// данных (потокобезопасно).
func (b *BoundedWriter) ExceededN() (bool, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded, b.n
}
