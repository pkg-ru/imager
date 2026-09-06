package generatev2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// metaFlightPrefix — префикс singleflight-ключей метаданных.
const metaFlightPrefix = "meta:"

// detectionsResult — результат ensureDetections: готовые боксы в координатах
// оригинала + флаг «боксы валидны, модель вызывать запрещено».
type detectionsResult struct {
	ready bool
	boxes []filemeta.PixelBox
}

// metaFlightKey строит singleflight-ключ "meta:"+srcKey.
func metaFlightKey(srcKey object.ObjectKey) object.ObjectKey {
	return object.ObjectKey(metaFlightPrefix + string(srcKey))
}

// planNeedsOperation возвращает true, если операция плана входит в ops.
// Trim — независимый фильтр и не влияет на необходимость детекции.
func planNeedsOperation(plan *processing.ProcessingPlan, ops ...processing.Operation) bool {
	if plan == nil {
		return false
	}
	for _, op := range ops {
		if plan.Operation == op {
			return true
		}
	}
	return false
}

// ensureDetections — best-effort источник боксов детекции для плана:
//
//   - кэш/детектор отключён или план не требует детекции → (false, nil);
//   - sidecar-кэш дал боксы                              → (true, боксы оригинала);
//   - сбой любой стадии                                  → лог + (false, nil):
//     процессор работает в режиме self-detection.
//
// Модель вызывается ровно один раз на ассет под keyed singleflight
// "meta:"+assetKey; sidecar создаётся лениво (только при реальных данных).
// Метаданные привязаны к АССЕТУ-результату (файл .meta.json лежит рядом с
// ассетом), поэтому ключом служит assetKey, а не ключ источника.
func (s *Service) ensureDetections(
	ctx context.Context,
	assetKey object.ObjectKey,
	plan *processing.ProcessingPlan,
	src io.ReadSeeker,
	srcFingerprint *filemeta.SourceFingerprint,
) (bool, []filemeta.PixelBox) {
	if s.deps.Metadata == nil || s.deps.Detector == nil {
		return false, nil
	}
	if !planNeedsOperation(plan, processing.OpFaceCrop, processing.OpObjectCrop,
		processing.OpFaceFixCrop, processing.OpObjectFixCrop) {
		return false, nil
	}
	if !s.deps.Detector.Available() {
		return false, nil
	}
	// Подготовка RGB на уровне приложения: процессор обязан реализовать
	// processor.RGBPreparer; иначе — деградация к self-detection.
	prep, ok := s.deps.Processor.(processor.RGBPreparer)
	if !ok {
		s.log.Warnf("generatev2: processor %T does not implement RGBPreparer; detection degraded to self-detection (asset=%s)", s.deps.Processor, assetKey)
		return false, nil
	}

	v, err := s.deps.Coordinator.Do(ctx, metaFlightKey(assetKey), func() (any, error) {
		return s.ensureDetectionsLocked(ctx, assetKey, plan, prep, src, srcFingerprint)
	})
	if err != nil {
		// Best-effort: сбой координации/детекции не должен ломать генерацию.
		s.log.Warnf("generatev2: detection cache flight failed (asset=%s): %v", assetKey, err)
		return false, nil
	}
	det, ok := v.(detectionsResult)
	if !ok {
		return false, nil
	}
	return det.ready, det.boxes
}

// ensureDetectionsLocked — тело детекции под keyed singleflight "meta:"+assetKey:
//  1. Load sidecar (промах/битый → свежие данные);
//  2. детекция только отсутствующих данных (Faces == nil / Objects == nil),
//     «проверено, пусто» (non-nil, len==0) не вызывает модель повторно;
//  3. Save результатов под уже удерживаемой блокировкой "meta:"+assetKey
//     (одна операция на ассет; повторный Load не происходит);
//  4. итоговые боксы в координатах ОРИГИНАЛА.
func (s *Service) ensureDetectionsLocked(
	ctx context.Context,
	assetKey object.ObjectKey,
	plan *processing.ProcessingPlan,
	prep processor.RGBPreparer,
	src io.ReadSeeker,
	srcFingerprint *filemeta.SourceFingerprint,
) (any, error) {
	m, err := s.deps.Metadata.Load(ctx, assetKey.String())
	if err != nil {
		if errors.Is(err, filemeta.ErrSchemaTooNew) {
			// Чужие данные более новой версии: не читаем и не перезаписываем.
			s.log.Warnf("generatev2: metadata schema too new (asset=%s); detection degraded to self-detection", assetKey)
			return detectionsResult{}, nil
		}
		if errors.Is(err, filemeta.ErrCorrupt) || errors.Is(err, filemeta.ErrNotFound) {
			// Промах кэша → начинаем со свежих данных (перезапись разрешена).
			m = nil
		} else {
			// IO/прозрачная ошибка: best-effort → деградация.
			return nil, fmt.Errorf("generatev2: load metadata (asset=%s): %w", assetKey, err)
		}
	}
	if m == nil {
		m = filemeta.NewFileMetadata()
	}

	// Инвалидация по отпечатку источника: если sidecar содержит fingerprint,
	// не совпавший с текущим источником — боксы устарели (файл заменён),
	// сбрасываем их и пере-детектируем. Sidecar без fingerprint (записанные
	// до появления инвалидации) считаются валидными (backward-compat).
	if m.Source != nil && !m.Source.Matches(srcFingerprint) {
		s.log.Warnf("generatev2: detection cache invalidated by source fingerprint (asset=%s)", assetKey)
		m.Faces = nil
		m.Objects = nil
		m.Detection = nil
		m.Source = nil
	}

	needFaces := planNeedsOperation(plan, processing.OpFaceCrop, processing.OpFaceFixCrop)
	needObjects := planNeedsOperation(plan, processing.OpObjectCrop, processing.OpObjectFixCrop)
	detectFaces := needFaces && m.Faces == nil
	detectObjects := needObjects && m.Objects == nil

	if detectFaces || detectObjects {
		frame, err := prep.PrepareRGB(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("generatev2: prepare RGB (asset=%s): %w", assetKey, err)
		}
		if frame == nil || len(frame.Pixels) == 0 || frame.Width <= 0 || frame.Height <= 0 {
			return nil, fmt.Errorf("generatev2: prepare RGB returned empty frame (asset=%s)", assetKey)
		}

		// DetectFaces и DetectObjects НЕ зависят друг от друга и используют
		// один и тот же RGB-кадр, поэтому выполняются параллельно
		// (fan-out/fan-in через каналы). Это сокращает время детекции до
		// самого медленного из двух вызовов вместо их суммы. Оба детектора
		// потокобезопасны (см. ports/detector и OnnxDetector).
		type detectResult struct {
			faces   []filemeta.FaceInfo
			objects []filemeta.ObjectInfo
			err     error
		}
		resCh := make(chan detectResult, 1)
		var wg sync.WaitGroup
		if detectFaces {
			wg.Add(1)
			go func() {
				defer wg.Done()
				faces, err := s.deps.Detector.DetectFaces(ctx, frame.Pixels, frame.Width, frame.Height)
				if err != nil {
					resCh <- detectResult{err: fmt.Errorf("generatev2: detect faces (asset=%s): %w", assetKey, err)}
					return
				}
				resCh <- detectResult{faces: faces}
			}()
		}
		if detectObjects {
			wg.Add(1)
			go func() {
				defer wg.Done()
				objects, err := s.deps.Detector.DetectObjects(ctx, frame.Pixels, frame.Width, frame.Height)
				if err != nil {
					resCh <- detectResult{err: fmt.Errorf("generatev2: detect objects (asset=%s): %w", assetKey, err)}
					return
				}
				resCh <- detectResult{objects: objects}
			}()
		}
		wg.Wait()
		close(resCh)
		for r := range resCh {
			if r.err != nil {
				return nil, r.err
			}
			if r.faces != nil {
				// non-nil (в т.ч. пустой) срез — «проверено, пусто»: кэшируется.
				// append([]FaceInfo{}, ...) гарантирует non-nil даже при пустом
				// результате (append([]FaceInfo(nil), ...) вернул бы nil).
				m.Faces = append([]filemeta.FaceInfo{}, r.faces...)
			}
			if r.objects != nil {
				m.Objects = append([]filemeta.ObjectInfo{}, r.objects...)
			}
		}

		// Фиксируем конфигурацию детектора и отпечаток источника: по ним
		// выполняется диагностика и инвалидация кэша.
		desc := s.deps.Detector.Describe()
		m.Detection = &filemeta.DetectionInfo{
			Detector:            desc.Kind,
			FaceModel:           desc.FaceModel,
			ObjectModel:         desc.ObjectModel,
			ConfidenceThreshold: desc.ConfidenceThreshold,
		}
		m.Source = srcFingerprint

		// Сохраняем под уже удерживаемой keyed-блокировкой "meta:"+assetKey
		// (Coordinator.Do выше). Save вместо Update (атомарный read-modify-write
		// внутри Update сделал бы ВТОРОЙ Load того же sidecar-файла за один
		// запрос и вызов бы вложенную блокировку на тот же ключ). Атомарность
		// записи обеспечивает temp+rename внутри MetadataStore.Save.
		if err := s.deps.Metadata.Save(ctx, assetKey.String(), m); err != nil {
			return nil, fmt.Errorf("generatev2: save detections (asset=%s): %w", assetKey, err)
		}
	}

	// Исходные боксы в координатах ОРИГИНАЛА.
	boxes := make([]filemeta.PixelBox, 0, len(m.Faces)+len(m.Objects))
	if needFaces {
		for _, f := range m.Faces {
			boxes = append(boxes, f.PixelBox)
		}
	}
	if needObjects {
		for _, o := range m.Objects {
			boxes = append(boxes, o.PixelBox)
		}
	}
	return detectionsResult{ready: true, boxes: boxes}, nil
}

// detectionFingerprint вычисляет отпечаток источника для sidecar-инвалидации:
// Size и mtime из метаданных артефакта; SHA-256 НЕ вычисляется здесь
// (дорого для больших файлов) — для извлечённых кадров отпечаток строится
// из байтов кадра через detectionFingerprintFromBytes.
func detectionFingerprint(meta object.ObjectMetadata) *filemeta.SourceFingerprint {
	fp := &filemeta.SourceFingerprint{Size: meta.Size}
	if !meta.ModTime.IsZero() {
		fp.ModTimeUnix = meta.ModTime.Unix()
	}
	if fp.Size < 0 {
		fp.Size = 0
	}
	return fp
}

// detectionFingerprintFromBytes строит отпечаток из содержимого (SHA-256 +
// размер): используется для извлечённого видео-кадра, у которого нет
// метаданных файла.
func detectionFingerprintFromBytes(data []byte) *filemeta.SourceFingerprint {
	sum := sha256.Sum256(data)
	return &filemeta.SourceFingerprint{
		Size:       int64(len(data)),
		HashSHA256: hex.EncodeToString(sum[:]),
	}
}

// recordSelfDetections — страховочный best-effort путь сохранения детекции:
// если ensureDetections деградировал (кэш недоступен/ошибка IO) и процессор
// работал в режиме self-detection, боксы из processor.Result записываются в
// sidecar вместе с DetectionInfo и SourceFingerprint. Гарантирует «1 вызов
// модели на родителя» даже при деградации app-level пути кэша.
// Выполняется под keyed singleflight "meta:"+metaKey; метаданные привязаны к
// ключу sidecar-родителя (для видео — ключу кадра frameKey).
//
// op определяет целевую секцию sidecar: face-операции (fc/fct) пишут
// Faces, объектные (oc/oct) — Objects. Когда процессор вернул
// Detail (self-detection с реальной уверенностью), используются его
// confidence/label; иначе confidence боксов не известна и деградирует к 1.0.
func (s *Service) recordSelfDetections(
	ctx context.Context,
	metaKey object.ObjectKey,
	srcFingerprint *filemeta.SourceFingerprint,
	op processing.Operation,
	detections []filemeta.PixelBox,
	detail *processor.DetectionsDetail,
) {
	if s.deps.Metadata == nil || s.deps.Detector == nil || len(detections) == 0 {
		return
	}
	desc := s.deps.Detector.Describe()
	info := &filemeta.DetectionInfo{
		Detector:            desc.Kind,
		FaceModel:           desc.FaceModel,
		ObjectModel:         desc.ObjectModel,
		ConfidenceThreshold: desc.ConfidenceThreshold,
	}
	_, err := s.deps.Coordinator.Do(ctx, metaFlightKey(metaKey), func() (any, error) {
		return nil, s.deps.Metadata.Update(ctx, metaKey.String(), func(m *filemeta.FileMetadata) (bool, error) {
			if m == nil {
				m = filemeta.NewFileMetadata()
			}
			// Целевой сектор: для лиц — Faces, для объектов — Objects.
			isObjectOp := op == processing.OpObjectCrop || op == processing.OpObjectFixCrop
			alreadyHas := m.Faces != nil || m.Objects != nil
			if isObjectOp {
				alreadyHas = m.Objects != nil
			}
			if alreadyHas {
				// Данные уже записаны (другим запросом) — не перезаписываем.
				return false, nil
			}
			if isObjectOp {
				m.Objects = objectsFromDetail(detail, detections)
			} else {
				m.Faces = facesFromDetail(detail, detections)
			}
			m.Detection = info
			m.Source = srcFingerprint
			return true, nil
		})
	})
	if err != nil {
		s.log.Warnf("generatev2: record self-detections failed (meta=%s): %v", metaKey, err)
	}
}

// facesFromDetail конвертирует результаты self-detection в FaceInfo. Если
// процессор вернул Detail (реальная уверенность модели) — берём его;
// иначе (старый API/процессор без Detail) — confidence деградирует к 1.0.
func facesFromDetail(detail *processor.DetectionsDetail, detections []filemeta.PixelBox) []filemeta.FaceInfo {
	if detail != nil && len(detail.Faces) > 0 {
		out := make([]filemeta.FaceInfo, 0, len(detail.Faces))
		for _, d := range detail.Faces {
			out = append(out, filemeta.FaceInfo{
				PixelBox:   d.Box,
				Confidence: clamp01(d.Confidence),
			})
		}
		return out
	}
	return facesFromBoxes(detections)
}

// objectsFromDetail конвертирует результаты self-detection в ObjectInfo. При
// отсутствии Detail label остаётся пустым, confidence — 1.0 (деградация).
func objectsFromDetail(detail *processor.DetectionsDetail, detections []filemeta.PixelBox) []filemeta.ObjectInfo {
	if detail != nil && len(detail.Objects) > 0 {
		out := make([]filemeta.ObjectInfo, 0, len(detail.Objects))
		for _, d := range detail.Objects {
			out = append(out, filemeta.ObjectInfo{
				PixelBox:   d.Box,
				Confidence: clamp01(d.Confidence),
				Label:      d.Label,
			})
		}
		return out
	}
	return objectsFromBoxes(detections)
}

// facesFromBoxes конвертирует боксы из processor.Result.Detections в FaceInfo
// (legacy-путь: процессор не вернул Detail; confidence деградирует к 1.0).
func facesFromBoxes(boxes []filemeta.PixelBox) []filemeta.FaceInfo {
	out := make([]filemeta.FaceInfo, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, filemeta.FaceInfo{PixelBox: b, Confidence: 1.0})
	}
	return out
}

// objectsFromBoxes конвертирует боксы в ObjectInfo (без label).
func objectsFromBoxes(boxes []filemeta.PixelBox) []filemeta.ObjectInfo {
	out := make([]filemeta.ObjectInfo, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, filemeta.ObjectInfo{PixelBox: b, Confidence: 1.0})
	}
	return out
}

// clamp01 ограничивает v интервалом [0,1]; NaN/Inf → 0.
func clamp01(v float64) float64 {
	if v != v || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// updateLargestAIAsset — best-effort обновление largest_ai_asset после
// успешной публикации ассета. Ошибки логируются и не влияют на ответ клиенту.
//
// Вызывающий код выполняет проверку критерия кандидата
// (filemeta.ShouldTrackAsAIAsset) ДО вызова, поэтому обычные resize/watermark
// вообще не входят сюда и не входят в Coordinator.Do. Здесь остаётся только
// страховочный guard: nil-store или не-кандидат → возврат без записи.
// При нулевых SourceWidth/SourceHeight шаг пропускается.
//
// Метаданные привязаны к АССЕТУ-результату, поэтому ключом служит assetKey.
func (s *Service) updateLargestAIAsset(
	ctx context.Context,
	assetKey object.ObjectKey,
	outFormat string,
	outW, outH, srcW, srcH int,
) {
	if s.deps.Metadata == nil {
		return
	}
	if !filemeta.ShouldTrackAsAIAsset(srcW, srcH, outW, outH) {
		return
	}
	_, err := s.deps.Coordinator.Do(ctx, metaFlightKey(assetKey), func() (any, error) {
		uerr := s.deps.Metadata.Update(ctx, assetKey.String(), func(m *filemeta.FileMetadata) (bool, error) {
			if m == nil {
				m = filemeta.NewFileMetadata()
			}
			cur := m.LargestAIAsset
			if cur != nil && cur.Width*cur.Height >= outW*outH {
				// Сохранённый не хуже кандидата — не перезаписываем.
				return false, nil
			}
			m.LargestAIAsset = &filemeta.AIAssetInfo{
				Width:  outW,
				Height: outH,
				Format: outFormat,
				Key:    string(assetKey),
			}
			return true, nil
		})
		if uerr != nil {
			return nil, uerr
		}
		return nil, nil
	})
	if err != nil {
		s.log.Warnf("generatev2: update largest_ai_asset failed (asset=%s): %v", assetKey, err)
	}
}

// updateLargestAIAssetAsync — асинхронная (fire-and-forget) версия
// updateLargestAIAsset. Обновление largest_ai_asset НЕ влияет на результат
// генерации (клиент уже получил буфер), поэтому выполняется в фоне и не
// блокирует ответ клиенту.
//
// Используется context.Background, чтобы запись завершилась даже после
// отмены запроса (иначе обновление терялось бы при быстром ответе).
// best-effort: ошибки логируются и не влияют на генерацию.
func (s *Service) updateLargestAIAssetAsync(
	assetKey object.ObjectKey,
	outFormat string,
	outW, outH, srcW, srcH int,
) {
	if s.deps.Metadata == nil {
		return
	}
	if !filemeta.ShouldTrackAsAIAsset(srcW, srcH, outW, outH) {
		return
	}
	go func() {
		s.updateLargestAIAsset(
			context.Background(),
			assetKey,
			outFormat,
			outW, outH,
			srcW, srcH,
		)
	}()
}

// recordAssetCreationTime — ленивая асинхронная запись unix-времени создания
// ассета (created_unix) в sidecar-метаданные ассета.
//
// Требования:
//   - пишется ТОЛЬКО при создании ассета и ТОЛЬКО если файла метаданных ещё
//     не было (проверка НАЛИЧИЯ файла, без чтения содержимого);
//   - выполняется в фоне (не в основном потоке генерации) и не блокирует
//     ответ клиенту;
//   - файл НЕ читается на каждый запрос/генерацию: проверяется только
//     наличие (Exists), и если файл уже есть — запись не выполняется.
//
// best-effort: ошибки логируются и не влияют на генерацию.
func (s *Service) recordAssetCreationTime(ctx context.Context, assetKey object.ObjectKey) {
	if s.deps.Metadata == nil {
		return
	}
	// Асинхронно: не блокируем основной поток генерации. Используем
	// context.Background, чтобы запись завершилась даже после отмены
	// запроса (иначе время создания терялось бы при быстром ответе).
	go func() {
		bctx := context.Background()
		exists, err := s.deps.Metadata.Exists(bctx, assetKey.String())
		if err != nil {
			s.log.Warnf("generatev2: check asset metadata existence failed (asset=%s): %v", assetKey, err)
			return
		}
		if exists {
			// Файл метаданных уже есть — время создания не перезаписываем.
			return
		}
		err = s.deps.Metadata.Update(bctx, assetKey.String(), func(m *filemeta.FileMetadata) (bool, error) {
			if m == nil {
				m = filemeta.NewFileMetadata()
			}
			if m.CreatedUnix != 0 {
				// Время уже записано — не перезаписываем.
				return false, nil
			}
			m.CreatedUnix = time.Now().Unix()
			return true, nil
		})
		if err != nil {
			s.log.Warnf("generatev2: record asset creation time failed (asset=%s): %v", assetKey, err)
		}
	}()
}
