package fs

import (
	"os"
	"path/filepath"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/object"
)

// TestFileArtifactPathFromOSFile — artifact от *os.File отдаёт путь через
// Path() (pathProvider для videoframe/ffmpeg).
func TestFileArtifactPathFromOSFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	a := &fileArtifact{file: f, meta: object.ObjectMetadata{}}
	if got := a.Path(); got != path {
		t.Fatalf("Path() = %q, want %q", got, path)
	}
}

// TestFileArtifactPathFromNonOSFile — artifact НЕ от *os.File (in-RAM/обёртка)
// возвращает "" — ffmpeg fallback на stdin-pipe.
func TestFileArtifactPathFromNonOSFile(t *testing.T) {
	a := &fileArtifact{file: &readSeekCloserStub{}, meta: object.ObjectMetadata{}}
	if got := a.Path(); got != "" {
		t.Fatalf("Path() = %q, want empty", got)
	}
}

// readSeekCloserStub — не *os.File: проверяет ветку fallback в Path().
type readSeekCloserStub struct{}

func (readSeekCloserStub) Read([]byte) (int, error) { return 0, nil }
func (readSeekCloserStub) Seek(int64, int) (int64, error) {
	return 0, nil
}
func (readSeekCloserStub) Close() error { return nil }

// compile: fileArtifact должен удовлетворять интерфейсу с Path().
var _ interface{ Path() string } = (*fileArtifact)(nil)
