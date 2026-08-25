package generatev2

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pkg-ru/imager/ports/processor"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/domain/processing"
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

// planNeedsDetections возвращает true, если план требует ИИ-детекции
// (face-crop/object-crop — операции на лицах/объектах). Trim — независимый
// фильтр и не влияет на необходимость детекции.
func planNeedsDetections(plan *processing.ProcessingPlan) bool {
	if plan == nil {
		return false
	}
	switch plan.Operation {
	case processing.OpFaceCrop, processing.OpObjectCrop:
		return true
	}
	return false
}

// planNeedsFaces возвращает true, если плану нужны боксы лиц.
func planNeedsFaces(plan *processing.ProcessingPlan) bool {
	if plan == nil {
		return false
	}
	return plan.Operation == processing.OpFaceCrop
}

// planNeedsObjects возвращает true, если плану нужны боксы объектов.
func planNeedsObjects(plan *processing.ProcessingPlan) bool {
	if plan == nil {
		return false
	}
	return plan.Operation == processing.OpObjectCrop
}

// ensureDetections — best-effort источник боксов детекции для плана:
//
//   - кэш/детектор отключён или план не требует детекции → (false, nil);
//   - sidecar-кэш дал боксы                              → (true, боксы оригинала);
//   - сбой любой стадии                                  → лог + (false, nil):
//     процессор работает в режиме self-detection (деградация 8.2).
//
// Модель вызывается ровно один раз на родителя под keyed singleflight
// "meta:"+srcKey; sidecar создаётся лениво (только при реальных данных).
func (s *Service) ensureDetections(
	ctx context.Context,
	srcKey object.ObjectKey,
	plan *processing.ProcessingPlan,
	src io.ReadSeeker,
) (bool, []filemeta.PixelBox) {
	if s.deps.Metadata == nil || s.deps.Detector == nil {
		return false, nil
	}
	if !planNeedsDetections(plan) {
		return false, nil
	}
	if !s.deps.Detector.Available() {
		return false, nil
	}
	// Подготовка RGB на уровне приложения: процессор обязан реализовать
	// processor.RGBPreparer; иначе — деградация к self-detection.
	prep, ok := s.deps.Processor.(processor.RGBPreparer)
	if !ok {
		s.log.Warnf("generatev2: processor %T does not implement RGBPreparer; detection degraded to self-detection (src=%s)", s.deps.Processor, srcKey)
		return false, nil
	}

	v, err := s.deps.Coordinator.Do(ctx, metaFlightKey(srcKey), func() (any, error) {
		return s.ensureDetectionsLocked(ctx, srcKey, plan, prep, src)
	})
	if err != nil {
		// Best-effort: сбой координации/детекции не должен ломать генерацию.
		s.log.Warnf("generatev2: detection cache flight failed (src=%s): %v", srcKey, err)
		return false, nil
	}
	det, ok := v.(detectionsResult)
	if !ok {
		return false, nil
	}
	return det.ready, det.boxes
}

// ensureDetectionsLocked — тело детекции под keyed singleflight "meta:"+srcKey:
//  1. Load sidecar (промах/битый → свежие данные);
//  2. детекция только отсутствующих данных (Faces == nil / Objects == nil),
//     «проверено, пусто» (non-nil, len==0) не вызывает модель повторно;
//  3. Save результатов под уже удерживаемой блокировкой "meta:"+srcKey
//     (одна операция на parent; повторного Load не происходит);
//  4. итоговые боксы в координатах ОРИГИНАЛА.
func (s *Service) ensureDetectionsLocked(
	ctx context.Context,
	srcKey object.ObjectKey,
	plan *processing.ProcessingPlan,
	prep processor.RGBPreparer,
	src io.ReadSeeker,
) (any, error) {
	m, err := s.deps.Metadata.Load(ctx, srcKey.String())
	if err != nil {
		if errors.Is(err, filemeta.ErrSchemaTooNew) {
			// Чужие данные более новой версии: не читаем и не перезаписываем.
			s.log.Warnf("generatev2: metadata schema too new (src=%s); detection degraded to self-detection", srcKey)
			return detectionsResult{}, nil
		}
		if errors.Is(err, filemeta.ErrCorrupt) || errors.Is(err, filemeta.ErrNotFound) {
			// Промах кэша → начинаем со свежих данных (перезапись разрешена).
			m = nil
		} else {
			// IO/прозрачная ошибка: best-effort → деградация.
			return nil, fmt.Errorf("generatev2: load metadata (src=%s): %w", srcKey, err)
		}
	}
	if m == nil {
		m = filemeta.NewFileMetadata()
	}

	needFaces := planNeedsFaces(plan)
	needObjects := planNeedsObjects(plan)
	detectFaces := needFaces && m.Faces == nil
	detectObjects := needObjects && m.Objects == nil

	if detectFaces || detectObjects {
		frame, err := prep.PrepareRGB(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("generatev2: prepare RGB (src=%s): %w", srcKey, err)
		}
		if frame == nil || len(frame.Pixels) == 0 || frame.Width <= 0 || frame.Height <= 0 {
			return nil, fmt.Errorf("generatev2: prepare RGB returned empty frame (src=%s)", srcKey)
		}
		if detectFaces {
			faces, err := s.deps.Detector.DetectFaces(ctx, frame.Pixels, frame.Width, frame.Height)
			if err != nil {
				return nil, fmt.Errorf("generatev2: detect faces (src=%s): %w", srcKey, err)
			}
			// non-nil (в т.ч. пустой) срез — «проверено, пусто»: кэшируется.
			// append([]FaceInfo{}, ...) гарантирует non-nil даже при пустом
			// результате (append([]FaceInfo(nil), ...) вернул бы nil).
			m.Faces = append([]filemeta.FaceInfo{}, faces...)
		}
		if detectObjects {
			objects, err := s.deps.Detector.DetectObjects(ctx, frame.Pixels, frame.Width, frame.Height)
			if err != nil {
				return nil, fmt.Errorf("generatev2: detect objects (src=%s): %w", srcKey, err)
			}
			m.Objects = append([]filemeta.ObjectInfo{}, objects...)
		}
		// Сохраняем под уже удерживаемой keyed-блокировкой "meta:"+srcKey
		// (Coordinator.Do выше). Save вместо Update (атомарный read-modify-write
		// внутри Update сделал бы ВТОРОЙ Load того же sidecar-файла за один
		// запрос и занял бы вложенную блокировку на тот же ключ). Атомарность
		// записи обеспечивает temp+rename внутри MetadataStore.Save.
		if err := s.deps.Metadata.Save(ctx, srcKey.String(), m); err != nil {
			return nil, fmt.Errorf("generatev2: save detections (src=%s): %w", srcKey, err)
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
func (s *Service) updateLargestAIAsset(
	ctx context.Context,
	srcKey, assetKey object.ObjectKey,
	outFormat string,
	outW, outH, srcW, srcH int,
) {
	if s.deps.Metadata == nil {
		return
	}
	if !filemeta.ShouldTrackAsAIAsset(srcW, srcH, outW, outH) {
		return
	}
	_, err := s.deps.Coordinator.Do(ctx, metaFlightKey(srcKey), func() (any, error) {
		uerr := s.deps.Metadata.Update(ctx, srcKey.String(), func(m *filemeta.FileMetadata) (bool, error) {
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
		s.log.Warnf("generatev2: update largest_ai_asset failed (src=%s, asset=%s): %v", srcKey, assetKey, err)
	}
}
