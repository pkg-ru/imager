// Package bounded defines the shared output-limit mechanism: a single
// sentinel error for "output limit exceeded" plus bounded Writer/Reader
// helpers used by both adapters (processors) and app (use cases).
//
// The package lives in ports so that both app and adapters may import it
// without violating layering (app -> ports/domain, adapters -> ports/domain).
package bounded

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ErrOutputLimitExceeded — единый sentinel превышения лимита размера выхода.
// Возвращается BoundedWriter.Write и BoundedReader.Read. Вызывающий код
// проверяет его через errors.Is; адаптеры дополнительно маппят превышение
// в собственные типизированные LimitError (sentinel служит для раннего
// прерывания записи/чтения).
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

// BoundedReader ограничивает чтение max байт и сигнализирует о превышении
// через ErrOutputLimitExceeded. Лимит проверяется ДО передачи данных дальше:
// лишний байт не читается в p и не передаётся потребителю (например,
// Publish), поэтому при превышении в remote не попадает битый объект.
//
// Граничный случай: вывод ровно max байт допустим. Когда прочитано ровно
// max, выполняется пробное чтение одного байта: если данных больше нет —
// возвращается io.EOF (публикация успешна), если есть —
// ErrOutputLimitExceeded.
type BoundedReader struct {
	r     io.Reader
	max   int64
	read  int64
	probe [1]byte
}

// NewBoundedReader создаёт BoundedReader поверх r с лимитом max байт.
// max <= 0 означает "без лимита" (Read пробрасывается напрямую).
func NewBoundedReader(r io.Reader, max int64) *BoundedReader {
	return &BoundedReader{r: r, max: max}
}

// Read реализует io.Reader.
func (b *BoundedReader) Read(p []byte) (int, error) {
	if b.max <= 0 {
		return b.r.Read(p)
	}
	if b.read >= b.max {
		// Достигнут лимит. Пробуем прочитать один байт вне p, чтобы
		// отличить «ровно max» (EOF) от «больше max» (ошибка).
		n, err := b.r.Read(b.probe[:])
		if n > 0 {
			return 0, ErrOutputLimitExceeded
		}
		if err != nil {
			return 0, err // io.EOF, если данных больше нет
		}
		return 0, ErrOutputLimitExceeded
	}
	if int64(len(p)) > b.max-b.read {
		p = p[:b.max-b.read]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	return n, err
}

// Reset сбрасывает счётчик прочитанных байт перед повторной попыткой
// публикации (после Seek(0) базового reader'а).
func (b *BoundedReader) Reset() {
	b.read = 0
}
