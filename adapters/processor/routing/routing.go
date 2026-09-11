// Package routing реализует маршрутизацию к процессору libvips.
//
// Выбор движка происходит на основе плана обработки (ProcessingPlan):
//   - если формат ИЛИ операция покрываются процессором — он используется;
//   - если формат не покрывается — возвращается типизированная ошибка
//     ErrEngineUnavailable.
//
// Пакет изолирован от cgo: он живёт через абстрактный порт
// processor.Processor и не зависит от govips/libvips напрямую.
package routing

import (
	"context"
	"errors"
	"fmt"
	"io"

	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// ErrEngineUnavailable — сигнал, что для запрошенного формата/операции нет
// доступного движка. Возвращается, когда формат не покрывается процессором
// (например, сборка без тега "libvips").
var ErrEngineUnavailable = errors.New("routing: engine unavailable for requested format")

// UnsupportedError описывает неподдерживаемый формат/операцию.
type UnsupportedError struct {
	Format    processing.Format
	Operation string
	Reason    string
	Missing   string // имя требуемого движка (например "libvips")
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("routing: format %q not supported by engine%s; requires %s",
		e.Format, e.Reason, e.Missing)
}

// IsEngineUnavailable проверяет, является ли ошибка сигналом недоступного
// движка (включая ошибки типа *UnsupportedError).
func IsEngineUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrEngineUnavailable) {
		return true
	}
	var ue *UnsupportedError
	return errors.As(err, &ue)
}

// Capability — описание покрытия движка.
type Capability struct {
	// Formats — набор поддерживаемых форматов (нижний регистр).
	Formats map[processing.Format]bool
	// Name — имя движка ("libvips").
	Name string
}

// Processor — маршрутизатор к процессору.
var _ processor.RGBPreparer = (*Processor)(nil)

// Processor обрабатывает операции на единственном движке. Форматы вне
// покрытия primary вызывают ErrEngineUnavailable.
type Processor struct {
	primary     processor.Processor
	primaryCaps Capability
}

// Options — параметры маршрутизатора.
type Options struct {
	// Primary — основной процессор (обязательный).
	Primary processor.Processor
	// PrimaryCaps — покрытие основного движка (обязательный).
	PrimaryCaps Capability
}

// New создает маршрутизатор. Валидирует обязательные поля.
func New(opts Options) (*Processor, error) {
	if opts.Primary == nil {
		return nil, errors.New("routing: primary processor is required")
	}
	if opts.PrimaryCaps.Formats == nil {
		return nil, errors.New("routing: primary capabilities are required")
	}
	return &Processor{
		primary:     opts.Primary,
		primaryCaps: opts.PrimaryCaps,
	}, nil
}

// Process выполняет обработку на движке.
//
// Если формат (source или output) не покрывается primary — возвращается
// UnsupportedError/ErrEngineUnavailable.
func (p *Processor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	if in.Plan == nil {
		return nil, errors.New("routing: nil plan")
	}
	if !p.primaryCovered(in.Plan) {
		return nil, &UnsupportedError{
			Format:    in.Plan.OutputFormats,
			Operation: string(in.Plan.Operation),
			Reason:    " (engine does not support this format)",
			Missing:   p.primaryCaps.Name,
		}
	}
	return p.primary.Process(ctx, in, out)
}

// PrepareRGB делегирует подготовку RGB-пикселей движку.
// Реализует processor.RGBPreparer (извлечение RGB для детекции на уровне
// приложения, ensureDetections). Если движок не поддерживает подготовку
// RGB — возвращается ошибка (деградация к self-detection).
func (p *Processor) PrepareRGB(ctx context.Context, src io.ReadSeeker) (*processor.RGBFrame, error) {
	prep, ok := p.primary.(processor.RGBPreparer)
	if !ok {
		return nil, errors.New("routing: primary processor does not implement RGBPreparer")
	}
	return prep.PrepareRGB(ctx, src)
}

// primaryCovered проверяет, что оба формата плана покрыты primary-движком.
func (p *Processor) primaryCovered(plan *processing.ProcessingPlan) bool {
	if _, ok := p.primaryCaps.Formats[plan.SourceFormat]; !ok {
		return false
	}
	if _, ok := p.primaryCaps.Formats[plan.OutputFormats]; !ok {
		return false
	}
	return true
}
