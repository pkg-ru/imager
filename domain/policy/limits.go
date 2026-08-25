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

	"github.com/pkg-ru/dynamic"
)

// Limits — типизированные лимиты обработки ассета.
//
// Все поля неизменяемы и валидируются при создании через NewLimits.
// Значение 0 для лимита означает "не ограничено" (unlimited), кроме
// случаев, где это явно указано.
type Limits struct {
	// SourceBytes — максимальный размер исходного файла в байтах.
	SourceBytes dynamic.Int64 `yaml:"source-bytes"`
	// Width — максимальная ширина в пикселях.
	Width dynamic.Int64 `yaml:"width"`
	// Height — максимальная высота в пикселях.
	Height dynamic.Int64 `yaml:"height"`
	// Pixels — максимальное число пикселей (width*height).
	Pixels dynamic.Int64 `yaml:"pixels"`
	// DPR — максимальный DPR.
	DPR dynamic.Int64 `yaml:"dpr"`
	// Frames — максимальное число кадров (для анимации).
	Frames dynamic.Int64 `yaml:"frames"`
	// OutputBytes — максимальный размер выходного файла в байтах.
	OutputBytes dynamic.Int64 `yaml:"output-bytes"`
	// Duration — максимальная длительность в миллисекундах (для анимации).
	Duration dynamic.Int64 `yaml:"duration"`
	// Concurrency — максимальное число одновременных операций.
	Concurrency dynamic.Int64 `yaml:"concurrency"`
}

// NewLimits создаёт Limits с валидацией неотрицательности.
func NewLimits(l Limits) (Limits, error) {
	if l.SourceBytes.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: source bytes must be non-negative, got %d", l.SourceBytes.Unwrap())
	}
	if l.Width.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: width must be non-negative, got %d", l.Width.Unwrap())
	}
	if l.Height.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: height must be non-negative, got %d", l.Height.Unwrap())
	}
	if l.Pixels.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: pixels must be non-negative, got %d", l.Pixels.Unwrap())
	}
	if l.DPR.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: dpr must be non-negative, got %d", l.DPR.Unwrap())
	}
	if l.Frames.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: frames must be non-negative, got %d", l.Frames.Unwrap())
	}
	if l.OutputBytes.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: output bytes must be non-negative, got %d", l.OutputBytes.Unwrap())
	}
	if l.Duration.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: duration must be non-negative, got %d", l.Duration.Unwrap())
	}
	if l.Concurrency.Unwrap() < 0 {
		return Limits{}, fmt.Errorf("limits: concurrency must be non-negative, got %d", l.Concurrency.Unwrap())
	}
	return l, nil
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
	if l.SourceBytes.Unwrap() > 0 && sourceBytes > l.SourceBytes.Unwrap() {
		return CheckResult{ExceededLimit: "source_bytes", Limit: l.SourceBytes.Unwrap(), Actual: sourceBytes}
	}
	if l.Width.Unwrap() > 0 && int64(width) > l.Width.Unwrap() {
		return CheckResult{ExceededLimit: "width", Limit: l.Width.Unwrap(), Actual: int64(width)}
	}
	if l.Height.Unwrap() > 0 && int64(height) > l.Height.Unwrap() {
		return CheckResult{ExceededLimit: "height", Limit: l.Height.Unwrap(), Actual: int64(height)}
	}
	if l.Pixels.Unwrap() > 0 {
		pixels, ok := mul64(int64(width), int64(height))
		if !ok {
			return CheckResult{ExceededLimit: "pixels", Limit: l.Pixels.Unwrap(), Actual: math.MaxInt64}
		}
		if pixels > l.Pixels.Unwrap() {
			return CheckResult{ExceededLimit: "pixels", Limit: l.Pixels.Unwrap(), Actual: pixels}
		}
	}
	if l.DPR.Unwrap() > 0 && int64(dpr) > l.DPR.Unwrap() {
		return CheckResult{ExceededLimit: "dpr", Limit: l.DPR.Unwrap(), Actual: int64(dpr)}
	}
	if l.Frames.Unwrap() > 0 && int64(frames) > l.Frames.Unwrap() {
		return CheckResult{ExceededLimit: "frames", Limit: l.Frames.Unwrap(), Actual: int64(frames)}
	}
	if l.OutputBytes.Unwrap() > 0 && outputBytes > l.OutputBytes.Unwrap() {
		return CheckResult{ExceededLimit: "output_bytes", Limit: l.OutputBytes.Unwrap(), Actual: outputBytes}
	}
	if l.Duration.Unwrap() > 0 && duration > l.Duration.Unwrap() {
		return CheckResult{ExceededLimit: "duration", Limit: l.Duration.Unwrap(), Actual: duration}
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
