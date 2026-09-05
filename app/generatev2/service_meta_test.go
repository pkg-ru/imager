package generatev2

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/coordination/singleflight"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/internal/testutil"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

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
		log: testutil.NopLogger{},
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

// TestGenerateConcurrentDetectionsOneCallPerAsset — N конкурентных Generate
// ОДНОГО ассета: детекция выполняется ровно ОДИН раз суммарно (keyed
// singleflight по "meta:"+assetKey). Метаданные привязаны к ассету-результату.
func TestGenerateConcurrentDetectionsOneCallPerAsset(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 100, srcH: 100, outW: 100, outH: 100}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.Add("photo.png", []byte("SRC"))

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Один и тот же ассет: одинаковый размер/формат.
			req := mustReq(t, "", "photo", "png", asset.TransformFaceCrop, "100x100", 1, "webp")
			res, err := env.svc.Generate(context.Background(), req)
			if err != nil {
				errs <- err
				return
			}
			_ = res.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Generate: %v", err)
	}

	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls = %d, want exactly 1 (dedup по ассету)", got)
	}
}

// TestGenerateResizeNoDetection — обычный resize (план без детекции): модель
// не запускается, largest_ai_asset не пишется (не ИИ-кандидат). При этом
// лениво/асинхронно записывается created_unix (время создания ассета).
func TestGenerateResizeNoDetection(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 100, srcH: 100, outW: 100, outH: 100}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.Add("photo.png", []byte("SRC"))

	// Пустой transform = resize (buildPlan → OpResize).
	req := mustReq(t, "", "photo", "png", asset.Transform(""), "100x100", 1, "webp")
	res, err := env.svc.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_ = res.Close()

	// Детекция не вызывается (план без детекции).
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0", got)
	}

	// created_unix записывается асинхронно (файл метаданных создаётся).
	metaS.waitForUpdate(t, 1)
	metaS.mu.Lock()
	saved := metaS.data["photo-png/100x100.webp"]
	metaS.mu.Unlock()
	if saved == nil || saved.CreatedUnix == 0 {
		t.Fatalf("created_unix не записан: %+v", saved)
	}
	// largest_ai_asset не должен быть установлен (не ИИ-кандидат).
	if saved.LargestAIAsset != nil {
		t.Fatalf("largest_ai_asset установлен для не-ИИ ассета: %+v", saved.LargestAIAsset)
	}
}

// TestGenerateLargestAIAssetOnlyForAIAsset — largest_ai_asset обновляется
// ТОЛЬКО при реальном ИИ-ассете (выход больше родителя, те же пропорции).
// Запрос c resize-форматом, но ИИ-размерами результата → sidecar пишется с
// largest_ai_asset (Load для детекции не вызывается — план не требует
// детекции). Метаданные привязаны к ассету-результату (ключ = canonical URL).
func TestGenerateLargestAIAssetOnlyForAIAsset(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	// ИИ-кандидат: 2000x2000 из родителя 1000x1000 (площадь >, пропорции =).
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 1000, srcH: 1000, outW: 2000, outH: 2000}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.Add("photo.png", []byte("SRC"))

	req := mustReq(t, "", "photo", "png", asset.Transform(""), "100x100", 1, "webp")
	res, err := env.svc.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_ = res.Close()

	// created_unix и largest_ai_asset записываются асинхронно (fire-and-forget).
	// Ждём, пока largest_ai_asset появится в sidecar (polling с таймаутом).
	deadline := time.Now().Add(2 * time.Second)
	var saved *filemeta.FileMetadata
	for {
		metaS.mu.Lock()
		saved = metaS.data["photo-png/100x100.webp"]
		metaS.mu.Unlock()
		if saved != nil && saved.LargestAIAsset != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("largest_ai_asset не сохранён: %+v", saved)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if saved.LargestAIAsset == nil {
		t.Fatalf("largest_ai_asset не сохранён: %+v", saved)
	}
	if saved.LargestAIAsset.Width != 2000 || saved.LargestAIAsset.Height != 2000 {
		t.Fatalf("largest_ai_asset = %dx%d, want 2000x2000", saved.LargestAIAsset.Width, saved.LargestAIAsset.Height)
	}
	if saved.LargestAIAsset.Key != "photo-png/100x100.webp" {
		t.Fatalf("largest_ai_asset.key = %q, want %q", saved.LargestAIAsset.Key, "photo-png/100x100.webp")
	}
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0 (план не требует детекции)", got)
	}
}

// TestGenerateNonAIAssetNoLargestAIAsset — resize с размерами НЕ больше
// родителя (не ИИ-кандидат) → largest_ai_asset НЕ пишется. При этом
// created_unix (время создания) записывается лениво/асинхронно.
func TestGenerateNonAIAssetNoLargestAIAsset(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	proc := &fakeMetaProcessorSized{fakeMetaProcessor: fakeMetaProcessor{}, srcW: 1000, srcH: 1000, outW: 800, outH: 800}
	env := metaFullEnv(t, proc, metaS, det)
	env.src.Add("photo.png", []byte("SRC"))

	req := mustReq(t, "", "photo", "png", asset.Transform(""), "100x100", 1, "webp")
	res, err := env.svc.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_ = res.Close()

	// created_unix записывается асинхронно.
	metaS.waitForUpdate(t, 1)
	metaS.mu.Lock()
	saved := metaS.data["photo-png/100x100.webp"]
	metaS.mu.Unlock()
	if saved == nil {
		t.Fatalf("metadata не создан для ассета")
	}
	if saved.LargestAIAsset != nil {
		t.Fatalf("largest_ai_asset установлен для не-ИИ ассета: %+v", saved.LargestAIAsset)
	}
	if saved.CreatedUnix == 0 {
		t.Fatalf("created_unix не записан: %+v", saved)
	}
}

// TestRecordAssetCreationTimeWritesOnce — recordAssetCreationTime записывает
// created_unix только если файла метаданных ещё нет (проверка наличия, без
// чтения содержимого). Повторный вызов не перезаписывает время.
func TestRecordAssetCreationTimeWritesOnce(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	// Первый вызов: файла нет → created_unix записывается.
	s.recordAssetCreationTime(context.Background(), "asset.webp")
	metaS.waitForUpdate(t, 1)
	metaS.mu.Lock()
	first := metaS.data["asset.webp"]
	metaS.mu.Unlock()
	if first == nil || first.CreatedUnix == 0 {
		t.Fatalf("created_unix не записан при первом вызове: %+v", first)
	}
	firstUnix := first.CreatedUnix

	// Второй вызов: файл уже есть → Exists=true, запись не выполняется.
	metaS.mu.Lock()
	before := metaS.updateCalls
	metaS.mu.Unlock()
	s.recordAssetCreationTime(context.Background(), "asset.webp")
	// Ждём, пока асинхронная горутина завершится (если она что-то сделает).
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		metaS.mu.Lock()
		after := metaS.updateCalls
		metaS.mu.Unlock()
		if after > before {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	metaS.mu.Lock()
	second := metaS.data["asset.webp"]
	metaS.mu.Unlock()
	if second == nil || second.CreatedUnix != firstUnix {
		t.Fatalf("created_unix перезаписан: got %d, want %d", second.CreatedUnix, firstUnix)
	}
}

// TestRecordAssetCreationTimeDisabled — при выключенных метаданных запись
// времени создания не выполняется (no-op).
func TestRecordAssetCreationTimeDisabled(t *testing.T) {
	s := &Service{deps: Deps{Metadata: nil}}
	// Не должен паниковать и не должен ничего писать.
	s.recordAssetCreationTime(context.Background(), "asset.webp")
}

// TestEnsureDetectionsObjectCropOnlyObjects — план object-crop требует ТОЛЬКО
// объекты (не лица): вызывается только DetectObjects, результат сохраняется
// в sidecar. Проверяет корректность параллельной реализации детекции
// (не теряет результаты и не вызывает лишние модели).
func TestEnsureDetectionsObjectCropOnlyObjects(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	plan := testMetaPlan(t, processing.OpObjectCrop)

	ready, boxes := s.ensureDetections(context.Background(), "src", plan, nil)
	if !ready {
		t.Fatalf("ready = false, want true")
	}
	// Только объекты: DetectObjects вызван 1 раз, DetectFaces — 0.
	if got := det.objectsCalls.Load(); got != 1 {
		t.Fatalf("DetectObjects calls = %d, want 1", got)
	}
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0", got)
	}
	// Боксы содержат только объект.
	if len(boxes) != 1 {
		t.Fatalf("boxes = %+v, want 1 (object)", boxes)
	}
	// Sidecar сохранил объекты, лица не тронуты.
	metaS.mu.Lock()
	saved := metaS.data["src"]
	metaS.mu.Unlock()
	if saved == nil || len(saved.Objects) != 1 {
		t.Fatalf("sidecar = %+v, want 1 object", saved)
	}
	if len(saved.Faces) != 0 {
		t.Fatalf("sidecar faces = %+v, want empty", saved.Faces)
	}
}
