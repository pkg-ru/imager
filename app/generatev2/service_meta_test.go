package generatev2

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/coordination/singleflight"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/object"
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

	ready, boxes := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, nil)
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
			ready, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), plan, nil, nil)
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

	ready, boxes := s.ensureDetections(context.Background(), object.ObjectKey("src"), plan, nil, nil)
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
	ready2, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), plan, nil, nil)
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

	ready, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, nil)
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

	ready, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, nil)
	if ready {
		t.Fatalf("ready = true, want false (IO error → degrade)")
	}
}

// TestEnsureDetectionsDisabled: metadata/детектор выключены → (false, nil).
func TestEnsureDetectionsDisabled(t *testing.T) {
	s := &Service{deps: Deps{Metadata: nil, Detector: nil}}
	ready, boxes := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, nil)
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

	ready, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, nil)
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
			req := mustReq(t, "", "photo", "png", asset.CropFace, false, "100x100", 1, "webp")
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
	req := mustReq(t, "", "photo", "png", asset.Crop(""), false, "100x100", 1, "webp")
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

	req := mustReq(t, "", "photo", "png", asset.Crop(""), false, "100x100", 1, "webp")
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

	req := mustReq(t, "", "photo", "png", asset.Crop(""), false, "100x100", 1, "webp")
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

	ready, boxes := s.ensureDetections(context.Background(), object.ObjectKey("src"), plan, nil, nil)
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

// fpOf — вспомогательный конструктор отпечатка источника.
func fpOf(size int64) *filemeta.SourceFingerprint {
	return &filemeta.SourceFingerprint{Size: size, ModTimeUnix: 1700000000}
}

// TestEnsureDetectionsFingerprintInvalidation — sidecar с fingerprint,
// не совпавшим с текущим источником, инвалидируется: боксы сбрасываются,
// модель вызывается заново, в sidecar записывается новый отпечаток.
func TestEnsureDetectionsFingerprintInvalidation(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.data["src"] = &filemeta.FileMetadata{
		SchemaVersion: filemeta.CurrentSchemaVersion,
		Faces:         []filemeta.FaceInfo{{PixelBox: filemeta.PixelBox{X: 99, Y: 99, Width: 9, Height: 9}, Confidence: 0.5}},
		Source:        fpOf(111), // не совпадает с текущим (222)
	}
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	ready, boxes := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, fpOf(222))
	if !ready {
		t.Fatalf("ready = false, want true (invalidated → re-detect)")
	}
	if len(boxes) != 1 || boxes[0].X != 10 {
		t.Fatalf("boxes = %+v, want fresh detection (x=10)", boxes)
	}
	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls = %d, want 1 (re-detect after invalidation)", got)
	}
	metaS.mu.Lock()
	saved := metaS.data["src"]
	metaS.mu.Unlock()
	if saved.Source == nil || saved.Source.Size != 222 {
		t.Fatalf("sidecar source = %+v, want new fingerprint (size=222)", saved.Source)
	}
	if saved.Detection == nil || saved.Detection.Detector != "fake" {
		t.Fatalf("sidecar detection info = %+v, want DetectorInfo from Describe()", saved.Detection)
	}
}

// TestEnsureDetectionsFingerprintMatchKeepsCache — совпадающий отпечаток
// НЕ инвалидирует кэш: модель не вызывается (надёжный fast path).
func TestEnsureDetectionsFingerprintMatchKeepsCache(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.data["src"] = &filemeta.FileMetadata{
		SchemaVersion: filemeta.CurrentSchemaVersion,
		Faces:         []filemeta.FaceInfo{{PixelBox: filemeta.PixelBox{X: 1, Y: 2, Width: 30, Height: 30}, Confidence: 0.9}},
		Source:        fpOf(222),
		Detection:     &filemeta.DetectionInfo{Detector: "fake"},
	}
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	ready, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, fpOf(222))
	if !ready {
		t.Fatalf("ready = false, want true (fingerprint match → cache hit)")
	}
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0 (fast path)", got)
	}
}

// TestEnsureDetectionsLegacySidecarNoSourceIsValidCache — sidecar без
// fingerprint (записанные до появления инвалидации) считается валидным
// кэшем: модель не вызывается.
func TestEnsureDetectionsLegacySidecarNoSourceIsValidCache(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.data["src"] = &filemeta.FileMetadata{
		SchemaVersion: filemeta.CurrentSchemaVersion,
		Faces:         []filemeta.FaceInfo{{PixelBox: filemeta.PixelBox{X: 1, Y: 2, Width: 30, Height: 30}, Confidence: 0.9}},
	}
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	ready, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, fpOf(222))
	if !ready {
		t.Fatalf("ready = false, want true (legacy sidecar = valid cache)")
	}
	if got := det.facesCalls.Load(); got != 0 {
		t.Fatalf("DetectFaces calls = %d, want 0 (legacy cache honored)", got)
	}
}

// TestEnsureDetectionsSavesDetectionInfo — при детекции в sidecar пишутся
// DetectionInfo (из Describe) и SourceFingerprint.
func TestEnsureDetectionsSavesDetectionInfo(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	s := newMetaService(metaS, det)

	ready, _ := s.ensureDetections(context.Background(), object.ObjectKey("src"), testMetaPlan(t, processing.OpFaceCrop), nil, fpOf(42))
	if !ready {
		t.Fatalf("ready = false, want true")
	}
	metaS.mu.Lock()
	saved := metaS.data["src"]
	metaS.mu.Unlock()
	if saved == nil {
		t.Fatal("sidecar not saved")
	}
	if saved.Detection == nil || saved.Detection.Detector != "fake" ||
		saved.Detection.FaceModel != "fake-face.onnx" ||
		saved.Detection.ConfidenceThreshold != 0.5 {
		t.Fatalf("detection info = %+v, want fake detector description", saved.Detection)
	}
	if saved.Source == nil || saved.Source.Size != 42 || saved.Source.ModTimeUnix != 1700000000 {
		t.Fatalf("source fingerprint = %+v, want size=42 mtime=1700000000", saved.Source)
	}
}

// fakeSelfDetectionProcessor — процессор, который НЕ реализует RGBPreparer
// (деградация ensureDetections) и возвращает Result.Detections (режим
// self-detection).
type fakeSelfDetectionProcessor struct {
	calls atomic.Int64
}

func (f *fakeSelfDetectionProcessor) Process(_ context.Context, _ processor.Input, _ io.Writer) (*processor.Result, error) {
	f.calls.Add(1)
	return &processor.Result{
		Detections: []filemeta.PixelBox{{X: 7, Y: 8, Width: 30, Height: 30}},
	}, nil
}

var _ processor.Processor = (*fakeSelfDetectionProcessor)(nil)

// TestRecordSelfDetectionsPersists — recordSelfDetections сохраняет боксы
// self-detection в sidecar с DetectionInfo и SourceFingerprint; повторный
// вызов не перезаписывает существующие данные.
func TestRecordSelfDetectionsPersists(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	s := newMetaService(metaS, det)
	boxes := []filemeta.PixelBox{{X: 7, Y: 8, Width: 30, Height: 30}}
	fp := fpOf(42)

	s.recordSelfDetections(context.Background(), object.ObjectKey("src"), fp, processing.OpFaceCrop, boxes, nil)

	metaS.mu.Lock()
	saved := metaS.data["src"]
	metaS.mu.Unlock()
	if saved == nil {
		t.Fatal("sidecar not saved")
	}
	if len(saved.Faces) != 1 || saved.Faces[0].X != 7 || saved.Faces[0].Confidence != 1.0 {
		t.Fatalf("faces = %+v, want 1 box (7,8,30,30) confidence 1.0", saved.Faces)
	}
	if saved.Objects != nil {
		t.Fatalf("objects = %+v, want nil (face operation must not write objects)", saved.Objects)
	}
	if saved.Detection == nil || saved.Detection.Detector != "fake" {
		t.Fatalf("detection = %+v, want fake", saved.Detection)
	}
	if saved.Source == nil || saved.Source.Size != 42 {
		t.Fatalf("source = %+v, want size=42", saved.Source)
	}

	// Повторный вызов: данные уже есть — не перезаписываются (модель не
	// вызывается повторно в реальном сценарии, потому что fast path сработает).
	s.recordSelfDetections(context.Background(), object.ObjectKey("src"), fp, processing.OpFaceCrop, boxes, nil)
	metaS.mu.Lock()
	saved2 := metaS.data["src"]
	metaS.mu.Unlock()
	if len(saved2.Faces) != 1 || saved2.Faces[0].X != 7 {
		t.Fatalf("second record must not overwrite faces: %+v", saved2.Faces)
	}
}

// TestRecordSelfDetectionsNoopGuards — nil store/детектор/боксы → no-op,
// не паникует.
func TestRecordSelfDetectionsNoopGuards(t *testing.T) {
	s := &Service{deps: Deps{Metadata: nil}, log: testutil.NopLogger{}}
	s.recordSelfDetections(context.Background(), "k", nil, processing.OpFaceCrop, []filemeta.PixelBox{{X: 1, Y: 1, Width: 1, Height: 1}}, nil)

	metaS := newFakeMetadataStore()
	s2 := newMetaService(metaS, newFakeDetector())
	s2.recordSelfDetections(context.Background(), object.ObjectKey("src"), nil, processing.OpFaceCrop, nil, nil)
	metaS.mu.Lock()
	n := len(metaS.data)
	metaS.mu.Unlock()
	if n != 0 {
		t.Fatalf("empty detections must not create sidecar, got %d entries", n)
	}
}

// TestRecordSelfDetectionsObjectCropWritesObjects — object-crop пишет боксы
// в m.Objects (с label), а не в m.Faces; Faces остаётся nil.
func TestRecordSelfDetectionsObjectCropWritesObjects(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	s := newMetaService(metaS, det)
	boxes := []filemeta.PixelBox{{X: 1, Y: 2, Width: 40, Height: 50}}
	detail := &processor.DetectionsDetail{
		Objects: []processor.DetectedObject{
			{Box: boxes[0], Confidence: 0.87, Label: "COCO_person"},
		},
	}
	fp := fpOf(42)

	s.recordSelfDetections(context.Background(), object.ObjectKey("src"), fp, processing.OpObjectCrop, boxes, detail)

	metaS.mu.Lock()
	saved := metaS.data["src"]
	metaS.mu.Unlock()
	if saved == nil {
		t.Fatal("sidecar not saved")
	}
	if saved.Faces != nil {
		t.Fatalf("faces = %+v, want nil (object operation)", saved.Faces)
	}
	if len(saved.Objects) != 1 {
		t.Fatalf("objects = %+v, want 1 entry", saved.Objects)
	}
	o := saved.Objects[0]
	if o.X != 1 || o.Y != 2 || o.Width != 40 || o.Height != 50 {
		t.Fatalf("object box = %+v, want (1,2,40,50)", o)
	}
	if o.Confidence != 0.87 {
		t.Fatalf("object confidence = %v, want 0.87 (real model confidence)", o.Confidence)
	}
	if o.Label != "COCO_person" {
		t.Fatalf("object label = %q, want COCO_person", o.Label)
	}
}

// TestRecordSelfDetectionsRealConfidence — при Detail с реальной
// уверенностью модели confidence сохраняется (не деградирует к 1.0) и
// клампится в [0,1].
func TestRecordSelfDetectionsRealConfidence(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	s := newMetaService(metaS, det)
	boxes := []filemeta.PixelBox{{X: 1, Y: 1, Width: 10, Height: 10}}
	detail := &processor.DetectionsDetail{
		Faces: []processor.DetectedFace{
			{Box: boxes[0], Confidence: 1.4},  // > 1 — клампится
			{Box: boxes[0], Confidence: -0.2}, // < 0 — клампится
			{Box: boxes[0], Confidence: 0.55},
		},
	}
	fp := fpOf(42)

	s.recordSelfDetections(context.Background(), object.ObjectKey("src"), fp, processing.OpFaceCrop, boxes, detail)

	metaS.mu.Lock()
	saved := metaS.data["src"]
	metaS.mu.Unlock()
	if saved == nil {
		t.Fatal("sidecar not saved")
	}
	if len(saved.Faces) != 3 {
		t.Fatalf("faces = %+v, want 3 entries", saved.Faces)
	}
	if saved.Faces[0].Confidence != 1.0 {
		t.Fatalf("faces[0].Confidence = %v, want 1.0 (clamped)", saved.Faces[0].Confidence)
	}
	if saved.Faces[1].Confidence != 0.0 {
		t.Fatalf("faces[1].Confidence = %v, want 0.0 (clamped)", saved.Faces[1].Confidence)
	}
	if saved.Faces[2].Confidence != 0.55 {
		t.Fatalf("faces[2].Confidence = %v, want 0.55", saved.Faces[2].Confidence)
	}
}
