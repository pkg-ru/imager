package generatev2

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"sync"

	"github.com/pkg-ru/imager/ports/videoframe"
)

// testJPEG генерирует маленькое JPEG-изображение (4x4) в памяти. Используется
// как детерминированный кадр для fakeVideoExtractor.
func testJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

// fakeVideoExtractor — videoframe.Extractor с детерминированным JPEG-кадром и
// записью вызовов (сколько раз вызван, с какими Options). Умеет возвращать
// ошибку по настройке.
type fakeVideoExtractor struct {
	mu       sync.Mutex
	frame    []byte
	err      error
	calls    int
	lastOpts videoframe.Options
}

func newFakeVideoExtractor() *fakeVideoExtractor {
	return &fakeVideoExtractor{frame: testJPEG()}
}

func (f *fakeVideoExtractor) Extract(_ context.Context, _ io.ReadSeeker, opts videoframe.Options) (*videoframe.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return &videoframe.Result{Frame: f.frame, Width: 4, Height: 4, Timestamp: 0}, nil
}

func (f *fakeVideoExtractor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeVideoExtractor) lastOptions() videoframe.Options {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOpts
}

func (f *fakeVideoExtractor) frameData() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frame
}

func (f *fakeVideoExtractor) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

var _ videoframe.Extractor = (*fakeVideoExtractor)(nil)
