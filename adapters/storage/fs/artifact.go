package fs

import (
	"io"
	"os"

	"gitverse.ru/pkg-ru/imager/domain/object"
)

// fileArtifact — object.Artifact поверх открытого os.File. Инкапсулирует
// файл, чтобы контракт не зависел от os.File напрямую.
type fileArtifact struct {
	file io.ReadSeekCloser
	meta object.ObjectMetadata
}

// Path возвращает путь к файлу на диске, если artifact открыт от *os.File
// (все ветки fs-хранилища, включая secureOpenFile, используют os.NewFile с
// реальным путём). Иначе возвращает "" — источник без файлового пути.
// См. adapters/videoframe/ffmpeg (pathProvider): путь позволяет ffmpeg
// читать файл напрямую вместо stdin-pipe.
func (a *fileArtifact) Path() string {
	if f, ok := a.file.(*os.File); ok {
		if name := f.Name(); name != "" && name != "/dev/stdin" {
			return name
		}
	}
	return ""
}

// Read реализует io.Reader.
func (a *fileArtifact) Read(p []byte) (int, error) { return a.file.Read(p) }

// Seek реализует io.Seeker.
func (a *fileArtifact) Seek(offset int64, whence int) (int64, error) {
	return a.file.Seek(offset, whence)
}

// Close освобождает файл.
func (a *fileArtifact) Close() error { return a.file.Close() }

// Metadata возвращает метаданные открытого объекта.
func (a *fileArtifact) Metadata() object.ObjectMetadata { return a.meta }

var _ object.Artifact = (*fileArtifact)(nil)

// fileStream — object.Stream поверх открытого os.File для последовательной
// отдачи. Не перематываем: читается от начала до конца, затем закрывается.
type fileStream struct {
	file io.ReadCloser
	meta object.ObjectMetadata
}

// Read реализует io.Reader.
func (a *fileStream) Read(p []byte) (int, error) { return a.file.Read(p) }

// Close освобождает файл.
func (a *fileStream) Close() error { return a.file.Close() }

// Metadata возвращает метаданные открытого объекта.
func (a *fileStream) Metadata() object.ObjectMetadata { return a.meta }

var _ object.Stream = (*fileStream)(nil)
