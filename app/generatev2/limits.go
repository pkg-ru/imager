package generatev2

import "math"

// Limits — application-level лимиты генерации ассетов (application.limits).
//
// Нулевое значение поля = без ограничения. Лимиты проверяются ДО обработки
// (source-bytes/width/height/pixels/dpr/frames) и ПОСЛЕ обработки
// (output-bytes) через Check.
//
// Структура живёт в app/generatev2 (а не в composition), чтобы избежать
// цикла импортов: composition/app.go импортирует generatev2, поэтому
// generatev2 не может импортировать composition.
type Limits struct {
	// SourceBytes — максимальный размер исходного файла в байтах (0 = без
	// ограничения).
	SourceBytes int64
	// OutputBytes — максимальный размер выходного файла в байтах (0 = без
	// ограничения).
	OutputBytes int64
	// Pixels — максимальное число пикселей (width*height) (0 = без
	// ограничения).
	Pixels int64
	// Width — максимальная ширина (0 = без ограничения).
	Width uint32
	// Height — максимальная высота (0 = без ограничения).
	Height uint32
	// DPR — максимальный DPR (0 = без ограничения).
	DPR uint32
	// Frames — максимальное число кадров (0 = без ограничения).
	Frames uint32
	// Duration — максимальная длительность в миллисекундах (0 = без
	// ограничения).
	Duration uint32
	// Concurrency — максимальное число одновременно выполняемых операций
	// (0 = без ограничения). Валидируется на уровне конфигурации, но НЕ
	// подключается к HTTP-слою (admission control остаётся в httpapi).
	Concurrency uint32
}

// CheckResult — результат проверки лимита.
type CheckResult struct {
	// ExceededLimit — имя превышенного лимита ("" = лимиты не превышены).
	ExceededLimit string
	// Limit — значение лимита.
	Limit int64
	// Actual — фактическое значение.
	Actual int64
}

// Exceeded сообщает, превышен ли какой-либо лимит.
func (r CheckResult) Exceeded() bool { return r.ExceededLimit != "" }

// Check проверяет фактические значения против лимитов. 0 в лимите = без
// ограничения. Возвращает первый превышенный лимит.
//
// Порядок проверок: source-bytes → width → height → pixels → dpr → frames →
// output-bytes → duration. pixels вычисляется как width*height с защитой от
// переполнения (mul64): при переполнении проверка pixels пропускается
// (width/height лимиты сработают раньше).
func (l *Limits) Check(sourceBytes, width, height, dpr, frames, outputBytes, duration int64) CheckResult {
	if l == nil {
		return CheckResult{}
	}
	if l.SourceBytes > 0 && sourceBytes > l.SourceBytes {
		return CheckResult{ExceededLimit: "source-bytes", Limit: l.SourceBytes, Actual: sourceBytes}
	}
	if l.Width > 0 && width > int64(l.Width) {
		return CheckResult{ExceededLimit: "width", Limit: int64(l.Width), Actual: width}
	}
	if l.Height > 0 && height > int64(l.Height) {
		return CheckResult{ExceededLimit: "height", Limit: int64(l.Height), Actual: height}
	}
	if l.Pixels > 0 && width > 0 && height > 0 {
		if pixels, ok := mul64(width, height); ok && pixels > l.Pixels {
			return CheckResult{ExceededLimit: "pixels", Limit: l.Pixels, Actual: pixels}
		}
	}
	if l.DPR > 0 && dpr > int64(l.DPR) {
		return CheckResult{ExceededLimit: "dpr", Limit: int64(l.DPR), Actual: dpr}
	}
	if l.Frames > 0 && frames > int64(l.Frames) {
		return CheckResult{ExceededLimit: "frames", Limit: int64(l.Frames), Actual: frames}
	}
	if l.OutputBytes > 0 && outputBytes > l.OutputBytes {
		return CheckResult{ExceededLimit: "output-bytes", Limit: l.OutputBytes, Actual: outputBytes}
	}
	if l.Duration > 0 && duration > int64(l.Duration) {
		return CheckResult{ExceededLimit: "duration", Limit: int64(l.Duration), Actual: duration}
	}
	return CheckResult{}
}

// mul64 перемножает два неотрицательных int64 с защитой от переполнения.
// Возвращает (0, false) при переполнении или неположительных аргументах.
func mul64(a, b int64) (int64, bool) {
	if a <= 0 || b <= 0 {
		return 0, false
	}
	if a > math.MaxInt64/b {
		return 0, false
	}
	return a * b, true
}
