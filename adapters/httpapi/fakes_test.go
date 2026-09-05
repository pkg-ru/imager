package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/app/generatev2"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/internal/testutil"
	"gitverse.ru/pkg-ru/imager/ports/detector"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// requireLocalhostTCP пропускает тест, если localhost TCP недоступен.
// Тесты, требующие реального TCP-соединения (rt.Serve + http.Client),
// вызывают этот хелпер в начале.
//
// Проверка выполняется один раз на процесс (sync.Once) и bounded: Listen и
// Dial выполняются в goroutine, ожидание идёт через select с таймаутом.
// На машинах с проблемным сетевым адаптером (например, Windows, где dial
// зависает на уровне драйвера ConnectEx) тесты просто пропускаются.
func requireLocalhostTCP(t *testing.T) {
	t.Helper()
	if err := localhostTCPReady(); err != nil {
		t.Skipf("localhost TCP unavailable: %v", err)
	}
}

// Пакетный кэш результата проверки localhost TCP.
var (
	localhostTCPOnce sync.Once
	localhostTCPErr  error
)

// localhostTCPReady выполняет однократную проверку TCP на 127.0.0.1.
func localhostTCPReady() error {
	localhostTCPOnce.Do(func() {
		localhostTCPErr = probeLocalhostTCP()
	})
	return localhostTCPErr
}

// probeLocalhostTCP — одноразовая проверка Listen→Dial на 127.0.0.1.
func probeLocalhostTCP() error {
	type listenResult struct {
		ln  net.Listener
		err error
	}
	listenCh := make(chan listenResult, 1)
	go func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		listenCh <- listenResult{ln, err}
	}()
	var ln net.Listener
	select {
	case r := <-listenCh:
		if r.err != nil {
			return fmt.Errorf("listen: %w", r.err)
		}
		ln = r.ln
	case <-time.After(3 * time.Second):
		return errors.New("listen timeout")
	}
	defer ln.Close()

	dialCh := make(chan error, 1)
	go func() {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
		if err == nil {
			c.Close()
		}
		dialCh <- err
	}()
	select {
	case err := <-dialCh:
		return err
	case <-time.After(3 * time.Second):
		return errors.New("dial timeout")
	}
}

// fakeGenerator — управляемый fake Generator для тестов.
type fakeGenerator struct {
	mu       sync.Mutex
	results  map[string]*generatev2.Result // keyed by canonical URL
	errs     map[string]error              // keyed by canonical URL
	fallback error                         // ошибка по умолчанию
	block    chan struct{}                 // если задан, Generate блокируется
}

func newFakeGenerator() *fakeGenerator {
	return &fakeGenerator{
		results: map[string]*generatev2.Result{},
		errs:    map[string]error{},
	}
}

func (f *fakeGenerator) addResult(url string, data []byte, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[url] = &generatev2.Result{
		URL: url,
		Opened: testutil.NewMemArtifact(
			data,
			object.ObjectMetadata{Key: object.ObjectKey(url), Size: size},
		),
	}
}

func (f *fakeGenerator) setFallback(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = err
}

func (f *fakeGenerator) Generate(ctx context.Context, req *asset.Request) (*generatev2.Result, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	url, err := req.Build()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.errs[url]; ok {
		return nil, e
	}
	if r, ok := f.results[url]; ok {
		return r, nil
	}
	if f.fallback != nil {
		return nil, f.fallback
	}
	return nil, &generatev2.OutcomeError{Kind: generatev2.OutcomeNotFound, Reason: "not found"}
}

// fakePixel — fake PixelGenerator.
type fakePixel struct {
	bytes []byte
	err   error
}

func (p *fakePixel) GeneratePixel(ctx context.Context, format string) ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.bytes != nil {
		return p.bytes, nil
	}
	return []byte("PIXEL:" + format), nil
}

// memSourceStore — in-memory storage.SourceStore (алиас testutil).
type memSourceStore = testutil.MemSourceStore

func newMemSourceStore() *memSourceStore { return testutil.NewMemSourceStore() }

// memResultStore — in-memory storage.ResultStore (алиас testutil).
type memResultStore = testutil.MemResultStore

func newMemResultStore() *memResultStore { return testutil.NewMemResultStore() }

// fakeProcessor — fake processor.Processor: копирует исходник в out.
type fakeProcessor struct{}

func (fakeProcessor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	n, err := io.Copy(out, in.Source)
	if err != nil {
		return nil, err
	}
	return &processor.Result{Size: n}, nil
}

// fakeDetector — fake detector.Detector: всегда доступен, не находит лиц.
type fakeDetector struct{}

func (fakeDetector) DetectFaces(_ context.Context, _ []byte, _, _ int) ([]filemeta.FaceInfo, error) {
	return []filemeta.FaceInfo{}, nil
}

func (fakeDetector) DetectObjects(_ context.Context, _ []byte, _, _ int) ([]filemeta.ObjectInfo, error) {
	return []filemeta.ObjectInfo{}, nil
}

func (fakeDetector) Available() bool { return true }

var _ detector.Detector = (*fakeDetector)(nil)
