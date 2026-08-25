package generatev2

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/pkg-ru/imager/coordination/singleflight"
	"github.com/pkg-ru/imager/ports/processor"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/processing"
)

// metaTestLogger — тестовый логгер, реализущий полный Logger
// интерфейс (в отличие от fakeLogger в fakes_test.go — только Debugf).
type metaTestLogger struct{}

func (metaTestLogger) Debugf(string, ...any) {}
func (metaTestLogger) Infof(string, ...any)  {}
func (metaTestLogger) Warnf(string, ...any)  {}
func (metaTestLogger) Errorf(string, ...any) {}

// newMetaService строит Service для интеграционных тестов метаданных.
// Используется прямое обращение к полям (мимо validate()).
func newMetaService(metaS *fakeMetadataStore, det *fakeDetector) *Service {
	return &Service{
		deps: Deps{
			Coordinator: singleflight.New(singleflight.Options{}),
			Processor:   newFakeMetaProcessor(),
			Metadata:    metaS,
			Detector:    det,
		},
		log: metaTestLogger{},
	}
}

// testMetaPlan строит план fc (face-crop) для детекции.
func testMetaPlan(t *testing.T, op processing.Operation) *processing.ProcessingPlan {
	t.Helper()
	plan, err := processing.NewProcessingPlan(
		op, processing.FormatJPEG, processing.FormatWebP,
		processing.Size{Width: 100, Height: 100}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

// TestEnsureDetectionsCacheHit: второй вызов читает из sidecar, модель не
// вызывается повторно.
func TestEnsureDetectionsCacheHit(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	// Заранее наполняем sidecar (первый вызов уже прошёл).
	metaS.data["src"] = &filemeta.FileMetadata{
		SchemaVersion: filemeta.CurrentSchemaVersion,
		Faces:         []filemeta.FaceInfo{{PixelBox: filemeta.PixelBox{X: 1, Y: 2, Width: 30, Height: 30}, Confidence: 0.9}},
	}
	s := newMetaService(metaS, det)

	ready, boxes := s.ensureDetections(context.Background(), "src", testMetaPlan(t, processing.OpFaceCrop), nil)
	if !ready {
		t.Fatalf("ready = false, want true (sidecar hit)")
	}
	if len(boxes) != 1 || boxes[0].X != 1 || boxes[0].Y != 2 {
		t.Fatalf("boxes = %+v, want [{1 2 30 30}]", boxes)
	}
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0 (cached)", got)
	}
}

// TestEnsureDetectionsSingleCallConcurrent: N конкурентных запросов одного
// родителя — ровно один вызов модели (keyed singleflight по meta:srcKey).
func TestEnsureDetectionsConcurrentOneCall(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	s := newMetaService(metaS, det)
	plan := testMetaPlan(t, processing.OpFaceCrop)

	const n = 16
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready, _ := s.ensureDetections(context.Background(), "src", plan, nil)
			results[i] = ready
		}(i)
	}
	wg.Wait()

	for i, ready := range results {
		if !ready {
			t.Fatalf("call %d: ready = false, want true", i)
		}
	}
	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls = %d, want exactly 1", got)
	}
}

// TestEnsureDetectionsEmptyCached: пустой результат (лиц нет) тоже
// кэшируется — второй вызов не запускает модель.
func TestEnsureDetectionsEmptyCached(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	det.faces = []filemeta.FaceInfo{} // модель вернёт "лиц нет" (non-nil пустой)
	s := newMetaService(metaS, det)
	plan := testMetaPlan(t, processing.OpFaceCrop)

	ready, boxes := s.ensureDetections(context.Background(), "src", plan, nil)
	if !ready {
		t.Fatalf("ready = false, want true (empty result still tracked)")
	}
	if len(boxes) != 0 {
		t.Fatalf("boxes = %+v, want empty", boxes)
	}
	metaS.mu.Lock()
	saved := metaS.data["src"]
	metaS.mu.Unlock()
	if saved == nil || saved.Faces == nil || len(saved.Faces) != 0 {
		t.Fatalf("sidecar faces = %+v, want non-nil empty (проверено, пусто)", saved)
	}

	// Второй запрос — из sidecar, модель не вызывается.
	ready2, _ := s.ensureDetections(context.Background(), "src", plan, nil)
	if !ready2 {
		t.Fatalf("second call ready = false, want true")
	}
	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls = %d, want exactly 1 (empty cached)", got)
	}
}

// TestEnsureDetectionsCorruptRecalc: ErrCorrupt — промах кэша, пересчитываем
// и сохраняем заново (ready = true).
func TestEnsureDetectionsCorruptRecalc(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.loadErr = filemeta.ErrCorrupt
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	ready, _ := s.ensureDetections(context.Background(), "src", testMetaPlan(t, processing.OpFaceCrop), nil)
	if !ready {
		t.Fatalf("ready = false, want true (corrupt → recalc)")
	}
	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls = %d, want 1", got)
	}
	metaS.mu.Lock()
	saved, ok := metaS.data["src"]
	metaS.mu.Unlock()
	if !ok || saved == nil || len(saved.Faces) == 0 {
		t.Fatalf("sidecar after recalc = %+v, want faces persisted", saved)
	}
}

// TestEnsureDetectionsIOError: неизвестная IO-ошибка store не ломает
// генерацию — деградация (false, nil) к self-detection в процессоре.
func TestEnsureDetectionsIOError(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.loadErr = errors.New("disk read failed")
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	ready, _ := s.ensureDetections(context.Background(), "src", testMetaPlan(t, processing.OpFaceCrop), nil)
	if ready {
		t.Fatalf("ready = true, want false (IO error → degrade)")
	}
}

// TestEnsureDetectionsDisabled: metadata/детектор выключены → (false, nil).
func TestEnsureDetectionsDisabled(t *testing.T) {
	s := &Service{deps: Deps{Metadata: nil, Detector: nil}}
	ready, boxes := s.ensureDetections(context.Background(), "src", testMetaPlan(t, processing.OpFaceCrop), nil)
	if ready || boxes != nil {
		t.Fatalf("disabled: ready=%v boxes=%v, want false/nil", ready, boxes)
	}
}

// TestEnsureDetectionsSchemaTooNew — ErrSchemaTooNew → не трогаем чужие данные,
// деградация (false, nil).
func TestEnsureDetectionsSchemaTooNew(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.loadErr = filemeta.ErrSchemaTooNew
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	ready, _ := s.ensureDetections(context.Background(), "src", testMetaPlan(t, processing.OpFaceCrop), nil)
	if ready {
		t.Fatalf("ready = true, want false (schema too new → degrade)")
	}
}

// metaFullEnv — окружение интеграционного теста generatev2 с включёнными
// метаданными: полный Deps (Sources/Results/Policy/Presets) + in-memory
// metadata store, детектор со счётчиками и кастомный processor.
func metaFullEnv(t *testing.T, proc processor.Processor, metaS *fakeMetadataStore, det *fakeDetector) *testEnv {
	t.Helper()
	return newTestEnv(t, func(d *Deps) {
		d.Metadata = metaS
		d.Detector = det
		d.Processor = proc
	})
}

// TestGenerateConcurrentDetectionsOneCallPerParent — N конкурентных Generate
// РАЗНЫХ ассетов ОДНОГО родителя: детекция выполняется ровно ОДИН раз
// суммарно (keyed singleflight по "meta:srcKey", а НЕ по ассет-ключу).
func TestGenerateConcurrentDetectionsOneCallPerParent(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 100, srcH: 100, outW: 100, outH: 100}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.add("photo.png", []byte("SRC"))

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Разные ассеты одного родителя: разные размеры/формат.
			req := mustReq(t, "", "photo", "png", asset.TransformFaceCrop, fmt.Sprintf("%dx%d", 100+i, 100+i), 1, "webp")
			res, err := env.svc.Generate(context.Background(), req)
			if err != nil {
				errs <- err
				return
			}
			_ = res.Close()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Generate: %v", err)
	}

	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls = %d, want exactly 1 (dedup по родителю)", got)
	}
}

// TestGenerateResizeNoMetadataTouch — обычный resize (план без детекции):
// при включённых метаданных НЕ вызываются ни Load, ни Update и модель не
// запускается (ленивость).
func TestGenerateResizeNoMetadataTouch(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 100, srcH: 100, outW: 100, outH: 100}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.add("photo.png", []byte("SRC"))

	// Пустой transform = resize (buildPlan → OpResize).
	req := mustReq(t, "", "photo", "png", asset.Transform(""), "100x100", 1, "webp")
	res, err := env.svc.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_ = res.Close()

	metaS.mu.Lock()
	loads := metaS.loadCalls
	updates := metaS.updateCalls
	metaS.mu.Unlock()
	if loads != 0 {
		t.Fatalf("Metadata.Load calls = %d, want 0 (resize не трогает sidecar)", loads)
	}
	if updates != 0 {
		t.Fatalf("Metadata.Update calls = %d, want 0 (resize не трогает sidecar)", updates)
	}
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0", got)
	}
}

// TestGenerateLargestAIAssetOnlyForAIAsset — largest_ai_asset обновляется
// ТОЛЬКО при реальном ИИ-ассете (выход больше родителя, те же пропорции).
// Запрос c resize-форматом, но ИИ-размерами результата → sidecar пишется с
// largest_ai_asset (Load для детекции не вызывается — план не требует
// детекции).
func TestGenerateLargestAIAssetOnlyForAIAsset(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	// ИИ-кандидат: 2000x2000 из родителя 1000x1000 (площадь >, пропорции =).
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 1000, srcH: 1000, outW: 2000, outH: 2000}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.add("photo.png", []byte("SRC"))

	req := mustReq(t, "", "photo", "png", asset.Transform(""), "100x100", 1, "webp")
	res, err := env.svc.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_ = res.Close()

	metaS.mu.Lock()
	updates := metaS.updateCalls
	saved := metaS.data["photo.png"]
	metaS.mu.Unlock()
	if updates == 0 {
		t.Fatalf("Metadata.Update calls = 0, want ≥1 (largest_ai_asset должен записаться)")
	}
	if saved == nil || saved.LargestAIAsset == nil {
		t.Fatalf("largest_ai_asset не сохранён: %+v", saved)
	}
	if saved.LargestAIAsset.Width != 2000 || saved.LargestAIAsset.Height != 2000 {
		t.Fatalf("largest_ai_asset = %dx%d, want 2000x2000", saved.LargestAIAsset.Width, saved.LargestAIAsset.Height)
	}
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0 (план не требует детекции)", got)
	}
}

// TestGenerateNonAIAssetDoesNotUpdateMetadata — resize с размерами НЕ больше
// родителя (не ИИ-кандидат) → Metadata не вызывается ВООБЩЕ (0 Load, 0 Update):
// largest_ai_asset лениво пропускается, sidecar не создаётся.
func TestGenerateNonAIAssetDoesNotUpdateMetadata(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 1000, srcH: 1000, outW: 800, outH: 800}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.add("photo.png", []byte("SRC"))

	req := mustReq(t, "", "photo", "png", asset.Transform(""), "100x100", 1, "webp")
	res, err := env.svc.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_ = res.Close()

	metaS.mu.Lock()
	loads := metaS.loadCalls
	updates := metaS.updateCalls
	metaS.mu.Unlock()
	if loads != 0 || updates != 0 {
		t.Fatalf("metadata touched for non-AI asset: loads=%d updates=%d, want 0/0", loads, updates)
	}
}
