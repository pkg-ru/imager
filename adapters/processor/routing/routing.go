// Package routing реализует маршрутизацию между процессорами:
// libvips (основной, in-process через govips) и ImageMagick (опциональный
// fallback для сборок без тега "libvips").
//
// Выбор движка происходит на основе плана обработки (ProcessingPlan):
//   - если формат ИЛИ операция покрываются основным процессором — он
//     используется;
//   - если формат не покрывается primary и fallback настроен — используется
//     fallback;
//   - если формат не покрывается ни одним движком — возвращается
//     типизированная ошибка ErrEngineUnavailable.
//
// Пакет изолирован от cgo: он живёт через абстрактный порт
// processor.Processor и не зависит от govips/libvips напрямую.
package routing

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

// ErrEngineUnavailable — сигнал, что для запрошенного формата/операции нет
// доступного движка. Возвращается, когда формат не покрывается ни primary,
// ни fallback (например, сборка без тега "libvips" и без ImageMagick).
var ErrEngineUnavailable = errors.New("routing: engine unavailable for requested format")

// UnsupportedError описывает неподдерживаемый формат/операцию.
type UnsupportedError struct {
	Format    processing.Format
	Operation string
	Reason    string
	Missing   string // имя требуемого движка (например "imagemagick")
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("routing: format %q not supported by primary engine%s; requires %s",
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
	// Name — имя движка ("libvips", "imagemagick").
	Name string
}

// Processor — маршрутизатор между основным и fallback процессорами.
var _ processor.RGBPreparer = (*Processor)(nil)

// Основной (primary) процессор обрабатывает подавляющее большинство
// операций. Fallback-процессор используется ТОЛЬКО для форматов, которые
// основной не покрывает (например, когда primary — ImageMagick, а формат
// требует libvips). Если fallback не настроен (nil), форматы вне покрытия
// primary вызывают ErrEngineUnavailable.
type Processor struct {
	primary      processor.Processor
	primaryCaps  Capability
	fallback     processor.Processor
	fallbackCaps Capability
}

// Options — параметры маршрутизатора.
type Options struct {
	// Primary — основной процессор (обязательный).
	Primary processor.Processor
	// PrimaryCaps — покрытие основного движка (обязательный).
	PrimaryCaps Capability
	// Fallback — опциональный fallback-процессор (может быть nil).
	Fallback processor.Processor
	// FallbackCaps — покрытие fallback-движка (игнорируется, если
	// Fallback nil).
	FallbackCaps Capability
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
		primary:      opts.Primary,
		primaryCaps:  opts.PrimaryCaps,
		fallback:     opts.Fallback,
		fallbackCaps: opts.FallbackCaps,
	}, nil
}

// Process выполняет обработку на выбранном движке.
//
// Выбор движка:
//  1. Если план покрывается primary — primary.
//  2. Если формат (source или output) не покрывается primary и покрывается
//     fallback — fallback.
//  3. Иначе — UnsupportedError/ErrEngineUnavailable.
func (p *Processor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	if in.Plan == nil {
		return nil, errors.New("routing: nil plan")
	}
	engine, err := p.engineFor(in.Plan)
	if err != nil {
		return nil, err
	}
	return engine.Process(ctx, in, out)
}

// PrepareRGB делегирует подготовку RGB-пикселей выбранному движку.
// Реализует processor.RGBPreparer (извлечение RGB для детекции на уровне
// приложения, ensureDetections). Если выбранный движок не поддерживает
// подготовку RGB — возвращается ошибка (деградация к self-detection).
func (p *Processor) PrepareRGB(ctx context.Context, src io.ReadSeeker) (*processor.RGBFrame, error) {
	// RGBPreparer не зависит от плана — используем primary-движок.
	prep, ok := p.primary.(processor.RGBPreparer)
	if !ok {
		return nil, errors.New("routing: primary processor does not implement RGBPreparer")
	}
	return prep.PrepareRGB(ctx, src)
}

// engineFor выбирает процессор для плана.
func (p *Processor) engineFor(plan *processing.ProcessingPlan) (processor.Processor, error) {
	// Покрытие primary: оба формата (source и output) должны быть в списке.
	if p.primaryCovered(plan) {
		return p.primary, nil
	}

	// Пытаемся переключить на fallback, если формат не покрыт primary.
	if p.fallback != nil {
		if p.fallbackCovered(plan) {
			return p.fallback, nil
		}
		return nil, &UnsupportedError{
			Format:    plan.OutputFormat,
			Operation: string(plan.Operation),
			Reason:    " (not covered by fallback either)",
			Missing:   p.fallbackCaps.Name,
		}
	}

	// Fallback отсутствует — ошибка недоступного движка.
	var missing string
	if p.fallback != nil {
		missing = p.fallbackCaps.Name
	} else {
		missing = "imagemagick"
	}
	return nil, &UnsupportedError{
		Format:    plan.OutputFormat,
		Operation: string(plan.Operation),
		Reason:    " (primary engine does not support this format)",
		Missing:   missing,
	}
}

// primaryCovered проверяет, что оба формата плана покрыты primary-движком.
func (p *Processor) primaryCovered(plan *processing.ProcessingPlan) bool {
	return p.coverAll(p.primaryCaps, plan)
}

func (p *Processor) fallbackCovered(plan *processing.ProcessingPlan) bool {
	return p.coverAll(p.fallbackCaps, plan)
}

func (p *Processor) coverAll(caps Capability, plan *processing.ProcessingPlan) bool {
	if _, ok := caps.Formats[plan.SourceFormat]; !ok {
		return false
	}
	if _, ok := caps.Formats[plan.OutputFormat]; !ok {
		return false
	}
	return true
}
