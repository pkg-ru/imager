package remote

import (
	"io"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// bufferArtifact — object.Artifact поверх spillable Buffer. Инкапсулирует
// Buffer, чтобы контракт не зависел от конкретного ресурса.
type bufferArtifact struct {
	buf  *Buffer
	meta object.ObjectMetadata
}

// NewBufferArtifact создаёт object.Artifact поверх Buffer с метаданными.
func NewBufferArtifact(buf *Buffer, meta object.ObjectMetadata) object.Artifact {
	return &bufferArtifact{buf: buf, meta: meta}
}

// Read реализует io.Reader.
func (a *bufferArtifact) Read(p []byte) (int, error) { return a.buf.Read(p) }

// Seek реализует io.Seeker.
func (a *bufferArtifact) Seek(offset int64, whence int) (int64, error) {
	return a.buf.Seek(offset, whence)
}

// Close освобождает буфер.
func (a *bufferArtifact) Close() error { return a.buf.Close() }

// Metadata возвращает метаданные открытого объекта.
func (a *bufferArtifact) Metadata() object.ObjectMetadata { return a.meta }

var _ object.Artifact = (*bufferArtifact)(nil)

// streamArtifact — object.Stream поверх одноразового потока из remote.
// Не перематываем: читается от начала до конца, затем закрывается.
type streamArtifact struct {
	r    io.Reader
	cl   io.Closer
	meta object.ObjectMetadata
}

// NewStreamArtifact создаёт object.Stream поверх raw-потока с метаданными.
// cl может быть nil, если поток не требует явного закрытия.
func NewStreamArtifact(r io.Reader, cl io.Closer, meta object.ObjectMetadata) object.Stream {
	return &streamArtifact{r: r, cl: cl, meta: meta}
}

// Read реализует io.Reader.
func (a *streamArtifact) Read(p []byte) (int, error) { return a.r.Read(p) }

// Close освобождает поток.
func (a *streamArtifact) Close() error {
	if a.cl != nil {
		return a.cl.Close()
	}
	return nil
}

// Metadata возвращает метаданные открытого объекта.
func (a *streamArtifact) Metadata() object.ObjectMetadata { return a.meta }

var _ object.Stream = (*streamArtifact)(nil)

// ReadSeekCloser — минимальный интерфейс, которому удовлетворяет Spool.
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}
