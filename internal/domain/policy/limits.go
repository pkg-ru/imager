// Package policy реализует доменный слой deny-by-default compiled policy
// обработки asset URL с типизированными лимитами.
//
// Политика компилируется из конфигурации в неизменяемую (immutable)
// структуру, которая используется для принятия решений (authorization)
// без знания источника конфигурации. Пакет не зависит от HTTP, файловой
// системы, ImageMagick и загрузчика конфигурации.
package policy

import (
	"fmt"
	"math"
)

// Limits — типизированные лимиты обработки ассета.
//
// Все поля неизменяемы и валидируются при создании через NewLimits.
// Значение 0 для лимита означает "не ограничено" (unlimited), кроме
// случаев, где это явно указано.
type Limits struct {
	// SourceBytes — максимальный размер исходного файла в байтах.
	SourceBytes int64
	// Width — максимальная ширина в пикселях.
	Width int
	// Height — максимальная высота в пикселях.
	Height int
	// Pixels — максимальное число пикселей (width*height).
	Pixels int64
	// DPR — максимальный DPR.
	DPR int
	// Frames — максимальное число кадров (для анимации).
	Frames int
	// OutputBytes — максимальный размер выходного файла в байтах.
	OutputBytes int64
	// Duration — максимальная длительность в миллисекундах (для анимации).
	Duration int64
	// Concurrency — максимальное число одновременных операций.
	Concurrency int
}

// NewLimits создаёт Limits с валидацией неотрицательности.
func NewLimits(l Limits) (Limits, error) {
	if l.SourceBytes < 0 {
		return Limits{}, fmt.Errorf("limits: source bytes must be non-negative, got %d", l.SourceBytes)
	}
	if l.Width < 0 {
		return Limits{}, fmt.Errorf("limits: width must be non-negative, got %d", l.Width)
	}
	if l.Height < 0 {
		return Limits{}, fmt.Errorf("limits: height must be non-negative, got %d", l.Height)
	}
	if l.Pixels < 0 {
		return Limits{}, fmt.Errorf("limits: pixels must be non-negative, got %d", l.Pixels)
	}
	if l.DPR < 0 {
		return Limits{}, fmt.Errorf("limits: dpr must be non-negative, got %d", l.DPR)
	}
	if l.Frames < 0 {
		return Limits{}, fmt.Errorf("limits: frames must be non-negative, got %d", l.Frames)
	}
	if l.OutputBytes < 0 {
		return Limits{}, fmt.Errorf("limits: output bytes must be non-negative, got %d", l.OutputBytes)
	}
	if l.Duration < 0 {
		return Limits{}, fmt.Errorf("limits: duration must be non-negative, got %d", l.Duration)
	}
	if l.Concurrency < 0 {
		return Limits{}, fmt.Errorf("limits: concurrency must be non-negative, got %d", l.Concurrency)
	}
	return l, nil
}

// Unlimited возвращает Limits без ограничений.
func Unlimited() Limits {
	return Limits{}
}

// CheckResult — результат проверки лимита.
type CheckResult struct {
	// ExceededLimit — имя превышенного лимита (пусто, если всё в порядке).
	ExceededLimit string
	// Limit — значение лимита.
	Limit int64
	// Actual — фактическое значение.
	Actual int64
}

// Exceeded сообщает, превышен ли какой-либо лимит.
func (r CheckResult) Exceeded() bool { return r.ExceededLimit != "" }

// Check проверяет фактические значения против лимитов.
// Возвращает первый превышенный лимит.
func (l Limits) Check(sourceBytes int64, width, height, dpr, frames int, outputBytes, duration int64) CheckResult {
	if l.SourceBytes > 0 && sourceBytes > l.SourceBytes {
		return CheckResult{ExceededLimit: "source_bytes", Limit: l.SourceBytes, Actual: sourceBytes}
	}
	if l.Width > 0 && width > l.Width {
		return CheckResult{ExceededLimit: "width", Limit: int64(l.Width), Actual: int64(width)}
	}
	if l.Height > 0 && height > l.Height {
		return CheckResult{ExceededLimit: "height", Limit: int64(l.Height), Actual: int64(height)}
	}
	if l.Pixels > 0 {
		pixels, ok := mul64(int64(width), int64(height))
		if !ok {
			return CheckResult{ExceededLimit: "pixels", Limit: l.Pixels, Actual: math.MaxInt64}
		}
		if pixels > l.Pixels {
			return CheckResult{ExceededLimit: "pixels", Limit: l.Pixels, Actual: pixels}
		}
	}
	if l.DPR > 0 && dpr > l.DPR {
		return CheckResult{ExceededLimit: "dpr", Limit: int64(l.DPR), Actual: int64(dpr)}
	}
	if l.Frames > 0 && frames > l.Frames {
		return CheckResult{ExceededLimit: "frames", Limit: int64(l.Frames), Actual: int64(frames)}
	}
	if l.OutputBytes > 0 && outputBytes > l.OutputBytes {
		return CheckResult{ExceededLimit: "output_bytes", Limit: l.OutputBytes, Actual: outputBytes}
	}
	if l.Duration > 0 && duration > l.Duration {
		return CheckResult{ExceededLimit: "duration", Limit: l.Duration, Actual: duration}
	}
	return CheckResult{}
}

// mul64 перемножает два int64 с проверкой переполнения.
func mul64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > math.MaxInt64/b {
		return 0, false
	}
	return a * b, true
}
