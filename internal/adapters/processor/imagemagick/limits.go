// Package imagemagick реализует production ImageMagick adapter для
// processor.Processor порта.
//
// Адаптер изолирован от application/domain: он принимает только доменные
// типы (processing.ProcessingPlan, object.Artifact) и не протекает
// ImageMagick-специфичные типы наружу. Экземпляр Processor владеет
// собственным immutable снимком capabilities и (опционально) deny-by-default
// policy.xml — без глобального sync.Once.
package imagemagick

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// LimitKind — тип ресурсного лимита.
type LimitKind string

const (
	// LimitMemory — лимит памяти (bytes).
	LimitMemory LimitKind = "memory"
	// LimitMap — лимит виртуальной памяти (bytes).
	LimitMap LimitKind = "map"
	// LimitDisk — лимит дискового кэша (bytes).
	LimitDisk LimitKind = "disk"
	// LimitThreads — лимит потоков.
	LimitThreads LimitKind = "threads"
	// LimitTime — лимит времени CPU (seconds).
	LimitTime LimitKind = "time"
	// LimitPixels — лимит числа пикселей.
	LimitPixels LimitKind = "pixels"
	// LimitFrames — лимит числа кадров.
	LimitFrames LimitKind = "frames"
	// LimitOutput — лимит размера выходного файла (bytes).
	LimitOutput LimitKind = "output"
)

// Limits — типизированные resource limits для ImageMagick subprocess.
//
// Значение 0 означает "не ограничено". Лимиты применяются тремя слоями:
//  1. image -limit аргументы (ImageMagick resource limits);
//  2. policy.xml resource limits (если включено);
//  3. application-level bounded writer (OutputBytes) и context deadline
//     (Timeout) — обязательны и не полагаются только на ImageMagick policy.
type Limits struct {
	// MemoryBytes — лимит памяти в байтах.
	MemoryBytes int64
	// MapBytes — лимит виртуальной памяти в байтах.
	MapBytes int64
	// DiskBytes — лимит дискового кэша в байтах.
	DiskBytes int64
	// Threads — лимит потоков.
	Threads int
	// TimeSeconds — лимит времени CPU в секундах.
	TimeSeconds int
	// Width — лимит ширины изображения в пикселях (0 = не ограничено).
	// Защищает от decompression bomb по одной стороне.
	Width int64
	// Height — лимит высоты изображения в пикселях (0 = не ограничено).
	Height int64
	// Pixels — лимит площади изображения (width*height) в пикселях.
	// Применяется через `-limit area` и policy `area` (защита от bomb).
	Pixels int64
	// Frames — лимит числа кадров.
	Frames int
	// OutputBytes — application-level лимит размера выходного файла в байтах.
	OutputBytes int64
	// Timeout — application-level context deadline для subprocess.
	Timeout time.Duration
	// Concurrency — максимальное число одновременно работающих ImageMagick
	// subprocess (0 = без ограничения). Ограничивает суммарное потребление
	// CPU/RAM при параллельных запросах с разными ключами.
	Concurrency int
	// WebPMethod — метод сжатия WebP (0-6; по умолчанию 4 — баланс
	// скорость/размер). 0 = использовать умолчание ImageMagick.
	WebPMethod int
	// PNGCompressionLevel — уровень сжатия PNG (0-9; по умолчанию 6).
	// 0 = использовать умолчание ImageMagick.
	PNGCompressionLevel int
}

// LimitError — типизированная ошибка превышения лимита.
type LimitError struct {
	// Kind — тип превышенного лимита.
	Kind LimitKind
	// Limit — значение лимита.
	Limit int64
	// Actual — фактическое значение.
	Actual int64
	// Err — исходная ошибка (может быть nil).
	Err error
}

// Error реализует error.
func (e *LimitError) Error() string {
	msg := fmt.Sprintf("imagemagick: %s limit exceeded (limit=%d actual=%d)", e.Kind, e.Limit, e.Actual)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap возвращает исходную ошибку.
func (e *LimitError) Unwrap() error { return e.Err }

// boundedWriter ограничивает запись max байт. При превышении лимита
// помечает exceeded, отменяет ctx (чтобы убить subprocess) и возвращает
// LimitError. Это application-level защита, не полагающаяся на policy.
//
// Потокобезопасен: Write может вызываться из goroutine копирования stdout,
// а n/exceeded читаются после завершения subprocess.
type boundedWriter struct {
	mu       sync.Mutex
	w        io.Writer
	max      int64
	n        int64
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.n+int64(len(p)) > b.max {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
		return 0, &LimitError{Kind: LimitOutput, Limit: b.max, Actual: b.n + int64(len(p))}
	}
	n, err := b.w.Write(p)
	b.n += int64(n)
	return n, err
}

// exceededN возвращает флаг превышения и фактический размер (потокобезопасно).
func (b *boundedWriter) exceededN() (bool, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded, b.n
}

// limitedBuffer накапливает stderr с ограничением размера, не теряя
// информацию о том, что вывод был обрезан. Потокобезопасен (Write может
// вызываться из goroutine копирования stderr).
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		return b.buf.Write(p)
	}
	if b.buf.Len() >= b.max {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

// String возвращает накопленный stderr с маркером обрезки.
func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if b.truncated {
		s += "... (stderr truncated)"
	}
	return s
}
