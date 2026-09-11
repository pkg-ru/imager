package ffmpeg

import (
	"io"
	"testing"
)

// pathSource — источник, реализующий pathProvider с непустым путём
// (path-ветка).
type pathSource struct {
	path string
}

func (s *pathSource) Read([]byte) (int, error) { return 0, io.EOF }
func (s *pathSource) Seek(int64, int) (int64, error) {
	return 0, nil
}
func (s *pathSource) Path() string { return s.path }

// emptyPathSource — источник с pathProvider, но пустым путём (in-RAM буфер —
// fallback на pipe).
type emptyPathSource struct{}

func (s *emptyPathSource) Read([]byte) (int, error) { return 0, io.EOF }
func (s *emptyPathSource) Seek(int64, int) (int64, error) {
	return 0, nil
}
func (s *emptyPathSource) Path() string { return "" }

// plainSource — источник без pathProvider (pipe-ветка).
type plainSource struct{}

func (s *plainSource) Read([]byte) (int, error) { return 0, io.EOF }
func (s *plainSource) Seek(int64, int) (int64, error) {
	return 0, nil
}

// TestInputPath проверяет выбор ветки path/pipe по источнику.
func TestInputPath(t *testing.T) {
	tests := []struct {
		name   string
		source io.ReadSeeker
		want   string
	}{
		{
			name:   "path provider with non-empty path",
			source: &pathSource{path: "/tmp/video.mp4"},
			want:   "/tmp/video.mp4",
		},
		{
			name:   "path provider with empty path falls back to pipe",
			source: &emptyPathSource{},
			want:   "",
		},
		{
			name:   "plain source without path provider",
			source: &plainSource{},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inputPath(tt.source); got != tt.want {
				t.Fatalf("inputPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFrameArgs — аргументы ffmpeg: input seek, scale-фильтр, вывод в stdout.
func TestFrameArgs(t *testing.T) {
	got := frameArgs("/tmp/video.mp4", 1.5)
	want := []string{
		"-ss", "1.5",
		"-i", "/tmp/video.mp4",
		"-threads", "2",
		"-vf", "scale='min(1920,iw)':-2",
		"-frames:v", "1",
		"-q:v", "2",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	}
	if len(got) != len(want) {
		t.Fatalf("frameArgs len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frameArgs[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}

	// Фильтр передаётся одним аргументом (запятая внутри значения — часть
	// argv, экранирование не требуется, склейки аргументов нет).
	vfIdx := -1
	for i, a := range got {
		if a == "-vf" {
			vfIdx = i
			break
		}
	}
	if vfIdx < 0 || vfIdx+1 >= len(got) {
		t.Fatal("frameArgs: -vf flag not followed by value")
	}
	if got[vfIdx+1] != "scale='min(1920,iw)':-2" {
		t.Fatalf("scale filter arg = %q, want %q", got[vfIdx+1], "scale='min(1920,iw)':-2")
	}

	// pipe-ветка использует pipe:0 как input.
	pipe := frameArgs("pipe:0", 0)
	if pipe[3] != "pipe:0" {
		t.Fatalf("pipe input = %q, want pipe:0", pipe[3])
	}
}

// TestProbeArgs — аргументы ffprobe: ограничение анализа и путь/pipe.
func TestProbeArgs(t *testing.T) {
	pathArgs := probeArgs("/tmp/video.mp4")
	joined := ""
	for _, a := range pathArgs {
		joined += a + "\x00"
	}

	// Ограничение анализа контейнера применяется в обеих ветках.
	for _, flag := range []string{"5M"} {
		found := false
		for _, a := range pathArgs {
			if a == flag {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("probeArgs: %q not found in %v", flag, pathArgs)
		}
	}
	for i, a := range pathArgs {
		if a == "-probesize" && (i+1 >= len(pathArgs) || pathArgs[i+1] != "5M") {
			t.Fatalf("probeArgs: -probesize value = %v", pathArgs)
		}
		if a == "-analyzeduration" && (i+1 >= len(pathArgs) || pathArgs[i+1] != "5M") {
			t.Fatalf("probeArgs: -analyzeduration value = %v", pathArgs)
		}
	}
	_ = joined

	// path-ветка: вход — путь файла.
	if pathArgs[len(pathArgs)-1] != "/tmp/video.mp4" {
		t.Fatalf("probeArgs last arg = %q, want path", pathArgs[len(pathArgs)-1])
	}

	// pipe-ветка: вход — pipe:0.
	pipeArgs := probeArgs("pipe:0")
	if pipeArgs[len(pipeArgs)-1] != "pipe:0" {
		t.Fatalf("probeArgs last arg = %q, want pipe:0", pipeArgs[len(pipeArgs)-1])
	}
}

// TestFormatSeconds — форматирование секунд без хвостовых нулей.
func TestFormatSeconds(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{in: 0, want: "0"},
		{in: 1.5, want: "1.5"},
		{in: 10.25, want: "10.25"},
		{in: 3.0, want: "3"},
	}
	for _, tt := range tests {
		if got := formatSeconds(tt.in); got != tt.want {
			t.Fatalf("formatSeconds(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
