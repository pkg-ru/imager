package ffmpeg

import (
	"bufio"
	"bytes"
	"io"
	"strings"
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

// TestBatchFrameArgs — аргументы ffmpeg: input seek, select+scale-фильтр,
// вывод в stdout.
func TestBatchFrameArgs(t *testing.T) {
	got := batchFrameArgs("/tmp/video.mp4", 1.5, 3, 5)
	want := []string{
		"-ss", "1.5",
		"-i", "/tmp/video.mp4",
		"-threads", "2",
		"-vf", "select='eq(n,0)+eq(n,5)+eq(n,10)',scale='min(1920,iw)':-2",
		"-frames:v", "3",
		"-q:v", "2",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	}
	if len(got) != len(want) {
		t.Fatalf("batchFrameArgs len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("batchFrameArgs[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
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
		t.Fatal("batchFrameArgs: -vf flag not followed by value")
	}
	if got[vfIdx+1] != "select='eq(n,0)+eq(n,5)+eq(n,10)',scale='min(1920,iw)':-2" {
		t.Fatalf("filter arg = %q, want %q", got[vfIdx+1], "select='eq(n,0)+eq(n,5)+eq(n,10)',scale='min(1920,iw)':-2")
	}

	// pipe-ветка использует pipe:0 как input.
	pipe := batchFrameArgs("pipe:0", 0, 1, 5)
	if pipe[3] != "pipe:0" {
		t.Fatalf("pipe input = %q, want pipe:0", pipe[3])
	}
	// Одна попытка: select выбирает только первый кадр, -frames:v 1.
	if pipe[7] != "select='eq(n,0)',scale='min(1920,iw)':-2" || pipe[9] != "1" {
		t.Fatalf("single attempt args = %v", pipe)
	}
}

// TestSelectExpr — выражение фильтра select: номера кадров 0, step, 2*step...
func TestSelectExpr(t *testing.T) {
	tests := []struct {
		name     string
		attempts int64
		step     int64
		want     string
	}{
		{name: "single attempt", attempts: 1, step: 5, want: "eq(n,0)"},
		{name: "three attempts step 5", attempts: 3, step: 5, want: "eq(n,0)+eq(n,5)+eq(n,10)"},
		{name: "step 1", attempts: 3, step: 1, want: "eq(n,0)+eq(n,1)+eq(n,2)"},
		{name: "zero step clamped", attempts: 2, step: 0, want: "eq(n,0)+eq(n,0)"},
		{name: "zero attempts clamped", attempts: 0, step: 5, want: "eq(n,0)"},
		{name: "negative step clamped", attempts: 2, step: -3, want: "eq(n,0)+eq(n,0)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectExpr(tt.attempts, tt.step); got != tt.want {
				t.Fatalf("selectExpr(%d, %d) = %q, want %q", tt.attempts, tt.step, got, tt.want)
			}
		})
	}
}

// TestNextJPEG — разбор потока image2pipe на JPEG-кадры по маркерам
// SOI/EOI, включая несколько кадров подряд и мусор между ними.
func TestNextJPEG(t *testing.T) {
	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}
	frame1 := append(append([]byte{}, soi...), 0x01, 0x02)
	frame1 = append(frame1, eoi...)
	frame2 := append(append([]byte{}, soi...), 0x03)
	frame2 = append(frame2, eoi...)

	// Мусор до первого SOI, два кадра подряд, обрыв без EOI.
	stream := bytes.Join([][]byte{
		[]byte{0x00, 0xFF, 0x00}, // мусор (0xFF с byte-stuffing 0x00)
		frame1,
		[]byte{0xAB, 0xCD}, // мусор между кадрами
		frame2,
		soi, // незавершённый кадр
	}, nil)

	br := bufio.NewReader(bytes.NewReader(stream))

	got1, err := nextJPEG(br)
	if err != nil {
		t.Fatalf("nextJPEG #1: %v", err)
	}
	if !bytes.Equal(got1, frame1) {
		t.Fatalf("nextJPEG #1 = %x, want %x", got1, frame1)
	}

	got2, err := nextJPEG(br)
	if err != nil {
		t.Fatalf("nextJPEG #2: %v", err)
	}
	if !bytes.Equal(got2, frame2) {
		t.Fatalf("nextJPEG #2 = %x, want %x", got2, frame2)
	}

	// Незавершённый кадр — ошибка (EOF).
	if _, err := nextJPEG(br); err == nil {
		t.Fatal("nextJPEG on truncated frame: want error, got nil")
	}
}

// TestNextJPEGEmpyStream — пустой поток даёт ошибку, а не панику.
func TestNextJPEGEmpyStream(t *testing.T) {
	br := bufio.NewReader(strings.NewReader(""))
	if _, err := nextJPEG(br); err == nil {
		t.Fatal("nextJPEG on empty stream: want error, got nil")
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
