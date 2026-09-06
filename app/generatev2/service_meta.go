package generatev2

import (
	"context"
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
		return s.ensureDetectionsLocked(ctx, assetKey, plan, prep, src)
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
