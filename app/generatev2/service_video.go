package generatev2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/domain/processing"
	"gitverse.ru/pkg-ru/imager/ports/buffer"
	"gitverse.ru/pkg-ru/imager/ports/processor"
	"gitverse.ru/pkg-ru/imager/ports/videoframe"
)

// videoFormats — множество видео-форматов, для которых ассеты генерируются
// из ОДНОГО кадра (извлечённого через VideoExtractor), а не из самого видео
// (процессоры не умеют декодировать видео).
var videoFormats = map[string]struct{}{
	"mp4": {}, "webm": {}, "mov": {}, "mkv": {}, "avi": {}, "m4v": {},
}

// isVideoFormat сообщает, является ли формат (расширение) видео-источником.
func isVideoFormat(f string) bool {
	_, ok := videoFormats[strings.ToLower(f)]
	return ok
}

// videoFrameKey строит ключ ассета кадра (x.jpg) для видео-источника.
// Ключ вида "<видео-ключ>/x.jpg" — ассет без параметров, лежащий рядом с
// видео-источником.
func videoFrameKey(srcKey object.ObjectKey) object.ObjectKey {
	return object.ObjectKey(string(srcKey) + "/x.jpg")
}

// canonicalSourceDir строит канонический каталог ассета исходника из запроса:
// {path}/{source_name}-{source_format}. Каталог сохраняет canonical-форму
// URL (дефис вместо точки: ivan-mp4), а не физическое имя файла (ivan.mp4):
// именно он используется как каталог результата и ключ sidecar-метаданных.
func canonicalSourceDir(req *asset.Request) object.ObjectKey {
	dir := req.SourceName().String() + "-" + req.SourceFormat().String()
	if req.Path() == "" {
		return object.ObjectKey(dir)
	}
	return object.ObjectKey(req.Path() + "/" + dir)
}

// videoOptions собирает настройки извлечения кадра из конфигурации
// (Deps.DefaultVideo*). Нулевые значения заменяются разумными дефолтами:
// percent=50, step=1, attempts=3. minContrast=0 означает «проверка
// контрастности пропускается» (передаётся как есть).
func (s *Service) videoOptions() videoframe.Options {
	percent := s.deps.DefaultVideoFramePercent
	if percent <= 0 {
		percent = 50
	}
	step := s.deps.DefaultVideoFrameStep
	if step <= 0 {
		step = 1
	}
	attempts := s.deps.DefaultVideoAttempts
	if attempts <= 0 {
		attempts = 3
	}
	return videoframe.Options{
		FramePercent: percent,
		MinContrast:  s.deps.DefaultVideoMinContrast,
		FrameStep:    step,
		Attempts:     attempts,
	}
}

// generateVideoLocked генерирует ассет из видео-источника (не-original
// запрос). Ассет строится из ОДНОГО кадра видео:
//
//   - если в метаданных видео уже есть VideoFrameKey (x.jpg сохранён ранее),
//     источником служит именно x.jpg, а НЕ оригинал видео (видео не
//     открывается);
//   - иначе кадр извлекается через VideoExtractor, асинхронно сохраняется
//     как x.jpg и асинхронно фиксируется в метаданных;
//   - кадр (JPEG) подаётся в существующий Processor.Process по обычным
//     правилам и запросу.
func (s *Service) generateVideoLocked(ctx context.Context, key object.ObjectKey, req *asset.Request) (buffer.Buffer, error) {
	srcKey := s.sourceKey(req)
	// Кадр x.jpg и sidecar-метаданные видео привязаны к КАНОНИЧЕСКОМУ
	// каталогу ассета URL ({path}/{source_name}-{source_format}, например
	// "test/ivan-mp4"), а не к физическому ключу исходника ("test/ivan.mp4"):
	// каталог результата сохраняет canonical-форму URL (дефис вместо точки).
	//
	// Ключом метаданных служит КЛЮЧ КАДРА (frameKey, "test/ivan-mp4/x.jpg"),
	// а не каталог: MetadataStore.metaPath вычисляет sidecar как
	// Dir(assetKey)/.meta.json, поэтому только ключ, указывающий на файл
	// ВНУТРИ канонического каталога, даёт sidecar в
	// result/test/ivan-mp4/.meta.json (а не в корне result/ или
	// родительском каталоге).
	canonDir := canonicalSourceDir(req)
	frameKeyBase := videoFrameKey(canonDir)
	metaKey := frameKeyBase.String()

	// 1. Проверяем метаданные видео на уже сохранённый кадр (x.jpg).
	frameKey, err := s.videoFrameKeyFromMeta(ctx, metaKey)
	if err != nil {
		return nil, err
	}

	var src io.ReadSeeker
	var srcSize int64
	var closeSrc func() error
	// Отпечаток источника детекции (кадра). Для уже сохранённого x.jpg —
	// Size+mtime из метаданных; для свежеизвлечённого кадра — SHA-256
	// байтов кадра (позволяет инвалидировать кэш детекции при повторном
	// извлечении другого кадра).
	var srcFingerprint *filemeta.SourceFingerprint
	if frameKey != "" {
		// x.jpg уже сохранён — открываем его как источник. Оригинал видео
		// НЕ открывается.
		art, err := s.deps.Results.Open(ctx, object.ObjectKey(frameKey))
		if err != nil {
			if object.IsNotFound(err) {
				// x.jpg пропал (например, удалён) — переизвлекаем кадр.
				frameKey = ""
			} else {
				return nil, s.mapResultError(ctx, err)
			}
		} else {
			src = art
			srcSize = art.Metadata().Size
			srcFingerprint = detectionFingerprint(art.Metadata())
			closeSrc = art.Close
		}
	}

	if frameKey == "" {
		// 2. Извлекаем кадр из видео-источника.
		frame, err := s.extractVideoFrame(ctx, srcKey)
		if err != nil {
			return nil, err
		}
		// Асинхронно (fire-and-forget) сохраняем кадр как x.jpg и пишем
		// VideoFrameKey в метаданные. Не блокирует ответ. Кадр публикуется
		// под КАНОНИЧЕСКИМ каталогом ассета (см. canonDir выше), чтобы
		// layout результата соответствовал URL (ivan-mp4, а не ivan.mp4).
		s.persistVideoFrameAsync(canonDir, metaKey, frame.Frame)
		src = bytes.NewReader(frame.Frame)
		srcSize = int64(len(frame.Frame))
		srcFingerprint = detectionFingerprintFromBytes(frame.Frame)
		closeSrc = func() error { return nil }
	}
	defer closeSrc()

	// 3. План обработки: исходный формат — JPEG (кадр), а не видео-формат.
	plan, err := s.buildPlanForSource(req, processing.FormatJPEG)
	if err != nil {
		return nil, outcome(OutcomeInvalid, "build processing plan", err)
	}

	// Проверка application-лимитов ДО обработки. Размер источника —
	// размер кадра (x.jpg или извлечённого). Размеры и DPR — из запроса.
	var w, h int
	if !plan.Size.Original {
		w, h = plan.Size.Width, plan.Size.Height
	}
	check := s.deps.Limits.Check(srcSize, int64(w), int64(h), int64(req.DPR().Int()), 0, 0, 0)
	if check.Exceeded() {
		return nil, outcome(OutcomeForbidden, "application limit: "+check.ExceededLimit, errLimitExceeded)
	}

	// 4. Детекция (fc/oc) — как для обычных картинок, из кадра.
	// КЛЮЧ БЛОКИРОВКИ/SIDECAR — ключ кадра (frameKeyBase), а не ключ ассета:
	// sidecar-родителя лежит в каталоге канонического ассета видео
	// (.meta.json рядом с x.jpg). Это выравнивает app-flight "meta:"+frameKey
	// со store-flight "meta:"+metaKey (MetadataStore.Update) и исключает
	// гонку Save/Update.
	in := processor.Input{
		Source:            src,
		Plan:              plan,
		SourceKey:         srcKey,
		MetaKey:           frameKeyBase,
		SourceFingerprint: srcFingerprint,
	}
	in.DetectionsReady, in.Boxes = s.ensureDetections(ctx, frameKeyBase, plan, src, srcFingerprint)

	// 5. Обработка кадра + публикация результата.
	return s.processAndPublish(ctx, key, in)
}

// videoFrameKeyFromMeta читает VideoFrameKey из метаданных видео-источника.
// Возвращает "" (без ошибки), если метаданные отключены, отсутствуют или
// битые — в этом случае кадр извлекается заново.
func (s *Service) videoFrameKeyFromMeta(ctx context.Context, metaKey string) (string, error) {
	if s.deps.Metadata == nil {
		return "", nil
	}
	m, err := s.deps.Metadata.Load(ctx, metaKey)
	if err != nil {
		if errors.Is(err, filemeta.ErrNotFound) ||
			errors.Is(err, filemeta.ErrCorrupt) ||
			errors.Is(err, filemeta.ErrSchemaTooNew) {
			return "", nil
		}
		return "", outcome(OutcomeProcessing, "load video metadata", err)
	}
	if m == nil {
		return "", nil
	}
	return m.VideoFrameKey, nil
}

// extractVideoFrame открывает видео-источник и извлекает из него один кадр
// через VideoExtractor. Возвращает понятную ошибку, если извлечение
// недоступно или не удалось.
func (s *Service) extractVideoFrame(ctx context.Context, srcKey object.ObjectKey) (*videoframe.Result, error) {
	if s.deps.VideoExtractor == nil {
		return nil, outcome(OutcomeProcessing, "video extraction not configured", nil)
	}
	src, err := s.deps.Sources.Open(ctx, srcKey)
	if err != nil {
		if object.IsNotFound(err) {
			return nil, outcome(OutcomeNotFound, "source not found", err)
		}
		return nil, s.mapSourceError(ctx, err)
	}
	defer src.Close()

	res, err := s.deps.VideoExtractor.Extract(ctx, src, s.videoOptions())
	if err != nil {
		return nil, outcome(OutcomeProcessing, "extract video frame", err)
	}
	if res == nil || len(res.Frame) == 0 {
		return nil, outcome(OutcomeProcessing, "extract video frame: empty frame", nil)
	}
	return res, nil
}

// persistVideoFrameAsync асинхронно (fire-and-forget) сохраняет извлечённый
// кадр как ассет x.jpg и записывает VideoFrameKey в метаданные видео.
// canonDir — КАНОНИЧЕСКИЙ каталог ассета исходника (например
// "test/ivan-mp4"): кадр публикуется как "<canonDir>/x.jpg", метаданные
// пишутся под ключом metaKey (= "<canonDir>/x.jpg"), чтобы sidecar
// (.meta.json) оказался внутри каталога результата.
// best-effort: ошибки логируются и не влияют на генерацию. Используется
// context.Background, чтобы запись завершилась даже после отмены запроса.
func (s *Service) persistVideoFrameAsync(canonDir object.ObjectKey, metaKey string, frame []byte) {
	go func() {
		bctx := context.Background()
		frameKey := videoFrameKey(canonDir)

		// Сохраняем кадр как ассет x.jpg.
		if err := s.deps.Results.Publish(bctx, frameKey, bytes.NewReader(frame), object.PublishOptions{}); err != nil {
			s.log.Warnf("generatev2: persist video frame failed (dir=%s): %v", canonDir, err)
			return
		}

		// Фиксируем VideoFrameKey в метаданных видео.
		if s.deps.Metadata == nil {
			return
		}
		if err := s.deps.Metadata.Update(bctx, metaKey, func(m *filemeta.FileMetadata) (bool, error) {
			if m == nil {
				m = filemeta.NewFileMetadata()
			}
			if m.VideoFrameKey != "" {
				// Уже зафиксирован — не перезаписываем.
				return false, nil
			}
			m.VideoFrameKey = string(frameKey)
			return true, nil
		}); err != nil {
			s.log.Warnf("generatev2: record video frame key failed (dir=%s): %v", canonDir, err)
		}
	}()
}
