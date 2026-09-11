// Package buffer определяет порт spillable-буфера, используемый
// application-слоем для материализации результата обработки.
//
// Buffer — это перематываемый буфер, который сначала пишет данные в память
// процесса, а при превышении общего бюджета (BufferPool) сбрасывается во
// временный файл на диске. Application-слой не зависит от конкретной
// реализации (remote.Buffer и т.п.), а работает через этот интерфейс.
package buffer

import "io"

// Buffer — перематываемый буфер результата.
type Buffer interface {
	io.Writer
	io.Reader
	io.Seeker
	io.Closer
	// Size возвращает размер записанных данных.
	Size() int64
	// NewReader создаёт независимый reader с собственной позицией чтения.
	// Позволяет нескольким потребителям читать один и тот же буфер
	// параллельно (например, отдача клиенту и publish в remote).
	NewReader() (io.ReadSeekCloser, error)
}

// Factory создаёт новые Buffer. Реализация владеет общим бюджетом памяти
// процесса (BufferPool).
type Factory interface {
	// NewBuffer создаёт пустой буфер.
	NewBuffer() (Buffer, error)
}
