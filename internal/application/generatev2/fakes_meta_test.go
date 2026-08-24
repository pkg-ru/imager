package generatev2

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/pkg-ru/imager/internal/application/ports/detector"
	"github.com/pkg-ru/imager/internal/application/ports/metadata"
	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/domain/filemeta"
)

// fakeMetadataStore — in-memory metadata.Store со счётчиками вызовов.
// Load мимикрирует sidecar-семантику: отсутствие файла → (nil, nil).
type fakeMetadataStore struct {
	mu          sync.Mutex
	data        map[string]*filemeta.FileMetadata
	loadCalls   int
	saveCalls   int
	updateCalls int
	loadErr     error
	updateErr   error
}

func newFakeMetadataStore() *fakeMetadataStore {
	return &fakeMetadataStore{data: map[string]*filemeta.FileMetadata{}}
}

func (s *fakeMetadataStore) Load(_ context.Context, srcKey string) (*filemeta.FileMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadCalls++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	m, ok := s.data[srcKey]
	if !ok {
		return nil, filemeta.ErrNotFound
	}
	return m, nil
}

func (s *fakeMetadataStore) Save(_ context.Context, srcKey string, m *filemeta.FileMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	s.data[srcKey] = m
	return nil
}

func (s *fakeMetadataStore) Update(_ context.Context, srcKey string, fn metadata.UpdateFn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls++
	if s.updateErr != nil {
		return s.updateErr
	}
	m, ok := s.data[srcKey]
	if !ok {
		m = filemeta.NewFileMetadata()
	}
	changed, err := fn(m)
	if err != nil {
		return err
	}
	if changed {
		s.data[srcKey] = m
		s.saveCalls++
	}
	return nil
}

var _ metadata.Store = (*fakeMetadataStore)(nil)

// fakeDetector — in-memory детектор со счётчиками вызовов моделей.
type fakeDetector struct {
	facesCalls   atomic.Int64
	objectsCalls atomic.Int64
	faces        []filemeta.FaceInfo
	objects      []filemeta.ObjectInfo
	available    bool
	facesErr     error
	objectsErr   error
}

func newFakeDetector() *fakeDetector {
	return &fakeDetector{
		faces:     []filemeta.FaceInfo{{PixelBox: filemeta.PixelBox{X: 10, Y: 10, Width: 40, Height: 40}, Confidence: 0.9}},
		objects:   []filemeta.ObjectInfo{{PixelBox: filemeta.PixelBox{X: 5, Y: 5, Width: 50, Height: 50}, Confidence: 0.8, Label: "person"}},
		available: true,
	}
}

func (d *fakeDetector) DetectFaces(_ context.Context, _ []byte, _, _ int) ([]filemeta.FaceInfo, error) {
	d.facesCalls.Add(1)
	if d.facesErr != nil {
		return nil, d.facesErr
	}
	return d.faces, nil
}

func (d *fakeDetector) DetectObjects(_ context.Context, _ []byte, _, _ int) ([]filemeta.ObjectInfo, error) {
	d.objectsCalls.Add(1)
	if d.objectsErr != nil {
		return nil, d.objectsErr
	}
	return d.objects, nil
}

func (d *fakeDetector) Available() bool { return d.available }

var _ detector.Detector = (*fakeDetector)(nil)

// fakeMetaProcessor — processor.Processor, реализующий RGBPreparer, чтобы
// ensureDetections мог подготовить RGB-кадр для детектора.
type fakeMetaProcessor struct {
	prepErr error
}

func newFakeMetaProcessor() *fakeMetaProcessor { return &fakeMetaProcessor{} }

func (f *fakeMetaProcessor) Process(_ context.Context, _ processor.Input, _ io.Writer) (*processor.Result, error) {
	return &processor.Result{Size: 0}, nil
}

func (f *fakeMetaProcessor) PrepareRGB(_ context.Context, _ io.ReadSeeker) (*processor.RGBFrame, error) {
	if f.prepErr != nil {
		return nil, f.prepErr
	}
	// 2x2 RGB-кадр (12 байт) — достаточно для детектора.
	return &processor.RGBFrame{
		Pixels: make([]byte, 2*2*3),
		Width:  2,
		Height: 2,
	}, nil
}

var _ processor.Processor = (*fakeMetaProcessor)(nil)
var _ processor.RGBPreparer = (*fakeMetaProcessor)(nil)

// fakeMetaProcessorSized — fakeMetaProcessor, возвращающий фиксированные
// размеры в Result (для тестов updateLargestAIAsset).
type fakeMetaProcessorSized struct {
	fakeMetaProcessor
	outW, outH, srcW, srcH int
}

func (f *fakeMetaProcessorSized) Process(_ context.Context, _ processor.Input, _ io.Writer) (*processor.Result, error) {
	return &processor.Result{
		Size:         0,
		Width:        f.outW,
		Height:       f.outH,
		SourceWidth:  f.srcW,
		SourceHeight: f.srcH,
	}, nil
}

var _ processor.Processor = (*fakeMetaProcessorSized)(nil)
var _ processor.RGBPreparer = (*fakeMetaProcessorSized)(nil)
