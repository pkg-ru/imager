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
	// SourceFingerprint — отпечаток источника (Size/mtime/SHA-256),
	// вычисленный на уровне приложения; используется при best-effort записи
	// self-detection результатов в sidecar (инвалидация кэша). nil = не
	// вычислялся.
	SourceFingerprint *filemeta.SourceFingerprint
	// MetaKey — ключ sidecar-метаданных РОДИТЕЛЯ (для картинок совпадает с
	// ключом ассета; для видео-ассетов — ключ кадра frameKey). Используется
	// при best-effort записи self-detection результатов в sidecar.
	MetaKey object.ObjectKey
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

// DetectedFace — лицо, обнаруженное детектором (координаты оригинала +
// уверенность модели).
type DetectedFace struct {
	// Box — бокс в пикселях ОРИГИНАЛА (до trim/кропа).
	Box filemeta.PixelBox
	// Confidence — уверенность детектора в интервале [0,1].
	Confidence float64
}

// DetectedObject — объект, обнаруженный детектором (координаты оригинала +
// уверенность + имя класса).
type DetectedObject struct {
	// Box — бокс в пикселях ОРИГИНАЛА (до trim/кропа).
	Box filemeta.PixelBox
	// Confidence — уверенность детектора в интервале [0,1].
	Confidence float64
	// Label — имя класса (например "COCO_person"); может быть пустой.
	Label string
}

// DetectionsDetail — детализированные результаты детекции self-detection,
// разделённые на faces/objects с реальной уверенностью модели. Заполняется
// ТОЛЬКО при self-detection (внутри процессора); при DetectionsReady=true
// оставляется nil (app-слой уже владеет боксами).
type DetectionsDetail struct {
	// Faces — найденные лица (face-crop/face-fix-crop); nil = не искались.
	Faces []DetectedFace
	// Objects — найденные объекты (object-crop/object-fix-crop); nil =
	// не искались.
	Objects []DetectedObject
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
	// Detections — боксы детекции (faces/objects), использованные или
	// найденные процессором в пикселях ОРИГИНАЛА (до trim/кропа). Заполняются
	// для детекторных операций (fc/oc/fct/oct): при self-detection — боксы,
	// найденные моделью внутри процессора; при DetectionsReady=true —
	// переданные app-боксы (дублируют Input.Boxes, для симметрии). Для
	// прочих операций — nil.
	Detections []filemeta.PixelBox
	// Detail — детализированные результаты self-detection (faces/objects
	// с реальной уверенностью и label). nil = детекция не выполнялась или
	// выполнялась с готовыми боксами из sidecar-кэша.
	Detail *DetectionsDetail
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
