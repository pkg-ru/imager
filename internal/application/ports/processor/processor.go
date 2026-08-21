// Package processor определяет абстрактный порт исполнителя обработки
// изображений. Реализации (ImageMagick, libvips, mock) не зависят от
// HTTP, файловой системы или конкретного движка.
package processor

import (
	"context"
	"io"

	"github.com/pkg-ru/imager/internal/domain/processing"
)

// Input — входные данные для обработки.
type Input struct {
	// Source — исходный объект (перематываемый).
	Source io.ReadSeeker
	// Plan — план обработки (операция, размеры, формат).
	Plan *processing.ProcessingPlan
}

// Result — результат обработки.
type Result struct {
	// Size — размер выходных данных в байтах.
	Size int64
}

// Processor — абстрактный исполнитель обработки изображений.
type Processor interface {
	// Process обрабатывает изображение согласно плану и записывает
	// результат в out. Возвращает метаданные результата.
	Process(ctx context.Context, in Input, out io.Writer) (*Result, error)
}
