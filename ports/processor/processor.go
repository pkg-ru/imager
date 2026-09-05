// Package processor defines an abstract port of the image processor.
// Implementations (libvips, mock) do not depend on HTTP, the file system, or
// a specific engine.
package processor

import (
	"context"
	"io"

	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// Input — входные данные для обработки.
type Input struct {
	// Source — исходный объект (перематываемый).
	Source io.ReadSeeker
	// Plan — план обработки (операция, размеры, формат).
	Plan *processing.ProcessingPlan
	// SourceKey — ключ родительского файла (для диагностики/будущих
	// расширений).
	SourceKey object.ObjectKey
	// DetectionsReady — true, если боксы валидны и процессор ОБЯЗАН не
	// вызывать ИИ-модель (боксы получены из sidecar-кэша моделей на уровне
	// приложения).
	DetectionsReady bool
	// Boxes — боксы детекции в пикселях ОРИГИНАЛЬНОГО изображения
	// (filemeta.PixelBox). Используются только при DetectionsReady=true.
	// Для trim-вариантов (fct/oct) процессор транслирует их на
	// trim-offset.
	Boxes []filemeta.PixelBox
}

// Result — результат обработки.
type Result struct {
	// Size — размер выходных данных в байтах.
	Size int64
	// Width — ширина выхода (px; 0 = неизвестно).
	Width int
	// Height — высота выхода (px; 0 = неизвестно).
	Height int
	// SourceWidth — ширина входа (px, из заголовка; 0 = неизвестно).
	SourceWidth int
	// SourceHeight — высота входа (px, из заголовка; 0 = неизвестно).
	SourceHeight int
}

// RGBFrame — RGB-пиксели изображения (3 байта на пиксель, порядок R,G,B)
// и его размеры. Подготовка на уровне приложения для вызова ИИ-детектора
// (ensureDetections) избавляет libvips от повторного декодирования.
type RGBFrame struct {
	// Pixels — непрерывный массив RGB (len == Width*Height*3).
	Pixels []byte
	// Width — ширина кадра в пикселях.
	Width int
	// Height — высота кадра в пикселях.
	Height int
}

// RGBPreparer — опциональный интерфейс процессора: извлекает RGB-пиксели
// источника без применения плана (для детекции на уровне приложения).
// Отсутствие поддержки ⇒ деградация: запрос с fc/oc обрабатывается в режиме
// self-detection внутри процессора.
type RGBPreparer interface {
	// PrepareRGB читает перематываемый источник и возвращает RGB-кадр
	// в размерах ОРИГИНАЛА (без trim), в координатах которого хранятся
	// боксы sidecar.
	PrepareRGB(ctx context.Context, src io.ReadSeeker) (*RGBFrame, error)
}

// Processor — абстрактный исполнитель обработки изображений.
type Processor interface {
	// Process обрабатывает изображение согласно плану и записывает
	// результат в out. Возвращает метаданные результата.
	Process(ctx context.Context, in Input, out io.Writer) (*Result, error)
}
