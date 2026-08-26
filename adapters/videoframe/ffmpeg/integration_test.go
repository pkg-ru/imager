package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pkg-ru/imager/ports/videoframe"
)

// requireFFmpeg проверяет доступность ffmpeg/ffprobe и пропускает тест,
// если они не установлены.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found in PATH, skipping integration test", bin)
		}
	}
}

// makeTestVideo генерирует короткое тестовое видео через ffmpeg.
func makeTestVideo(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test.mp4")
	// 2 секунды, 25 fps, 64x64, цветной тестовый паттерн.
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", "testsrc=duration=2:size=64x64:rate=25",
		"-pix_fmt", "yuv420p",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate video: %v: %s", err, out)
	}
	return path
}

func TestExtractIntegration(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	video := makeTestVideo(t, dir)

	// Источник, реализующий pathProvider (файл на диске).
	f, err := os.Open(video)
	if err != nil {
		t.Fatalf("open video: %v", err)
	}
	defer f.Close()
	src := &fileSource{file: f, path: video}

	ex := NewDefault()
	ctx := context.Background()

	res, err := ex.Extract(ctx, src, videoframe.Options{
		FramePercent: 50,
		MinContrast:  0.1,
		FrameStep:    5,
		Attempts:     3,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res == nil {
		t.Fatal("Extract returned nil result")
	}
	if len(res.Frame) == 0 {
		t.Fatal("Extract returned empty frame")
	}
	if res.Width != 64 || res.Height != 64 {
		t.Fatalf("size = %dx%d, want 64x64", res.Width, res.Height)
	}
	if res.Timestamp < 0 {
		t.Fatalf("timestamp = %v, want >= 0", res.Timestamp)
	}
}

// fileSource — источник, реализующий io.ReadSeeker и pathProvider для тестов.
type fileSource struct {
	file *os.File
	path string
}

func (f *fileSource) Path() string { return f.path }
func (f *fileSource) Read(p []byte) (int, error) {
	return f.file.Read(p)
}
func (f *fileSource) Seek(offset int64, whence int) (int64, error) {
	return f.file.Seek(offset, whence)
}
