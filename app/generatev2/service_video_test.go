package generatev2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/ports/metadata"
)

// videoEnv собирает testEnv с включённым VideoExtractor и метаданными.
func videoEnv(t *testing.T, extractor *fakeVideoExtractor, metaS *fakeMetadataStore) *testEnv {
	t.Helper()
	return newTestEnv(t, func(d *Deps) {
		d.VideoExtractor = extractor
		d.Metadata = metaS
	})
}

// waitForFrameKey ждёт, пока в ResultStore появится x.jpg (асинхронная
// публикация кадра) и в метаданных зафиксируется VideoFrameKey.
func waitForFrameKey(t *testing.T, env *testEnv, metaS *fakeMetadataStore, frameKey object.ObjectKey, metaKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		// x.jpg опубликован в ResultStore.
		if env.res.Has(frameKey) {
			metaS.mu.Lock()
			m := metaS.data[metaKey]
			metaS.mu.Unlock()
			if m != nil && m.VideoFrameKey == string(frameKey) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for frame key %q in results/metadata", frameKey)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestVideoOriginalServedAsIs — запрос ОРИГИНАЛА видео (mp4→mp4, size=x, без
// transform) отдаётся как есть через serveOriginal: VideoExtractor НЕ
// вызывается, процессор НЕ вызывается, данные совпадают с исходным видео.
func TestVideoOriginalServedAsIs(t *testing.T) {
	ext := newFakeVideoExtractor()
	metaS := newFakeMetadataStore()
	env := videoEnv(t, ext, metaS)
	env.src.Add("clip.mp4", []byte("VIDEO-BYTES"))

	ctx := context.Background()
	req := mustReqSize(t, "", "clip", "mp4", asset.Transform(""), asset.NewOriginalSize(), 1, "mp4")

	// isOriginalRequest для mp4→mp4, size=x, без transform = true: fast-path
	// оригинала достижим и для видео.
	if !isOriginalRequest(req) {
		t.Fatal("isOriginalRequest(mp4→mp4, size=x) = false, want true (original video fast-path)")
	}

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate original video: %v", err)
	}
	defer res.Close()

	// Результат — исходное видео как есть (не из кэша).
	if res.FromCache {
		t.Fatal("expected original served as-is, got cache hit")
	}
	data, _ := io.ReadAll(res.Opened)
	if string(data) != "VIDEO-BYTES" {
		t.Fatalf("original video data = %q, want %q", data, "VIDEO-BYTES")
	}

	// VideoExtractor НЕ вызывается (кадр не извлекается).
	if ext.callCount() != 0 {
		t.Fatalf("VideoExtractor calls = %d, want 0 (original served as-is)", ext.callCount())
	}
	// Процессор НЕ вызывается.
	if env.proc.callCount() != 0 {
		t.Fatalf("processor calls = %d, want 0 (original served as-is)", env.proc.callCount())
	}
}

// TestVideoGenerateFirstTime — генерация ассета из видео (первый раз, нет
// x.jpg в метаданных): VideoExtractor вызывается, кадр обрабатывается
// процессором, x.jpg асинхронно сохраняется и VideoFrameKey фиксируется.
func TestVideoGenerateFirstTime(t *testing.T) {
	ext := newFakeVideoExtractor()
	metaS := newFakeMetadataStore()
	env := videoEnv(t, ext, metaS)
	env.src.Add("clip.mp4", []byte("VIDEO-BYTES"))

	ctx := context.Background()
	// mp4→webp с resize — не original, идёт генерация из кадра.
	req := mustReq(t, "", "clip", "mp4", asset.Transform(""), "100x100", 1, "webp")

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate video asset: %v", err)
	}
	defer res.Close()

	// Результат — сгенерированный ассет (не из кэша).
	if res.FromCache {
		t.Fatal("expected generated asset, got cache hit")
	}
	data, _ := io.ReadAll(res.Opened)
	if string(data) != "IMG" {
		t.Fatalf("result data = %q, want IMG", data)
	}

	// VideoExtractor вызван ровно один раз.
	if ext.callCount() != 1 {
		t.Fatalf("VideoExtractor calls = %d, want 1", ext.callCount())
	}
	// Процессор вызван с JPEG-кадром (данные кадра, а не видео).
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1", env.proc.callCount())
	}
	if got := env.proc.lastInputData(); !bytes.Equal(got, ext.frameData()) {
		t.Fatalf("processor input = %d bytes, want JPEG frame %d bytes", len(got), len(ext.frameData()))
	}

	// x.jpg асинхронно сохранён в ResultStore по ключу <видео-ключ>/x.jpg.
	frameKey := videoFrameKey(object.ObjectKey("clip.mp4"))
	waitForFrameKey(t, env, metaS, frameKey, "clip.mp4")
	if !env.res.Has(frameKey) {
		t.Fatalf("x.jpg not published at %q", frameKey)
	}
	if got := env.res.Get(frameKey); !bytes.Equal(got, ext.frameData()) {
		t.Fatalf("x.jpg data mismatch: got %d bytes, want %d", len(got), len(ext.frameData()))
	}
	// VideoFrameKey зафиксирован в метаданных видео.
	metaS.mu.Lock()
	m := metaS.data["clip.mp4"]
	metaS.mu.Unlock()
	if m == nil || m.VideoFrameKey != string(frameKey) {
		t.Fatalf("metadata VideoFrameKey = %q, want %q", m.VideoFrameKey, frameKey)
	}
}

// TestVideoGenerateReusesCachedFrame — повторная генерация использует x.jpg
// из метаданных: VideoExtractor НЕ вызывается, оригинал видео НЕ открывается,
// источником служит x.jpg.
func TestVideoGenerateReusesCachedFrame(t *testing.T) {
	ext := newFakeVideoExtractor()
	metaS := newFakeMetadataStore()
	env := videoEnv(t, ext, metaS)
	env.src.Add("clip.mp4", []byte("VIDEO-BYTES"))

	// Заранее: x.jpg сохранён и VideoFrameKey зафиксирован в метаданных.
	frameKey := videoFrameKey(object.ObjectKey("clip.mp4"))
	env.res.Add(frameKey, ext.frameData())
	metaS.data["clip.mp4"] = &filemeta.FileMetadata{VideoFrameKey: string(frameKey)}

	ctx := context.Background()
	req := mustReq(t, "", "clip", "mp4", asset.Transform(""), "100x100", 1, "webp")

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate video asset (cached frame): %v", err)
	}
	defer res.Close()

	// VideoExtractor НЕ вызывается.
	if ext.callCount() != 0 {
		t.Fatalf("VideoExtractor calls = %d, want 0 (frame cached)", ext.callCount())
	}
	// Оригинал видео НЕ открывается (SourceStore не запрашивает видео).
	for _, k := range env.src.OpenedKeys() {
		if k == "clip.mp4" {
			t.Fatalf("video source %q was opened, want not opened (frame cached)", k)
		}
	}
	// Процессор вызван с данными x.jpg.
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1", env.proc.callCount())
	}
	if got := env.proc.lastInputData(); !bytes.Equal(got, ext.frameData()) {
		t.Fatalf("processor input = %d bytes, want x.jpg %d bytes", len(got), len(ext.frameData()))
	}
}

// TestVideoGenerateCacheHit — готовый ассет (cache-hit) отдаётся без
// извлечения кадра и без чтения метаданных.
func TestVideoGenerateCacheHit(t *testing.T) {
	ext := newFakeVideoExtractor()
	metaS := newFakeMetadataStore()
	env := videoEnv(t, ext, metaS)
	env.src.Add("clip.mp4", []byte("VIDEO-BYTES"))

	ctx := context.Background()
	req := mustReq(t, "", "clip", "mp4", asset.Transform(""), "100x100", 1, "webp")

	// Первый запрос — генерация (кэш пуст). При генерации из видео метаданные
	// читаются один раз (videoFrameKeyFromMeta).
	res1, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate (miss): %v", err)
	}
	_ = res1.Close()
	if ext.callCount() != 1 {
		t.Fatalf("VideoExtractor calls after miss = %d, want 1", ext.callCount())
	}
	metaS.mu.Lock()
	loadsAfterMiss := metaS.loadCalls
	metaS.mu.Unlock()
	if loadsAfterMiss != 1 {
		t.Fatalf("metadata Load calls after miss = %d, want 1", loadsAfterMiss)
	}

	// Второй запрос — cache-hit: кадр не извлекается, метаданные не читаются
	// повторно (число Load не растёт).
	res2, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate (hit): %v", err)
	}
	defer res2.Close()
	if !res2.FromCache {
		t.Fatal("expected cache hit")
	}
	if ext.callCount() != 1 {
		t.Fatalf("VideoExtractor calls after hit = %d, want 1 (no re-extract)", ext.callCount())
	}
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1 (cache hit, no reprocess)", env.proc.callCount())
	}
	metaS.mu.Lock()
	loadsAfterHit := metaS.loadCalls
	metaS.mu.Unlock()
	if loadsAfterHit != loadsAfterMiss {
		t.Fatalf("metadata Load calls grew on cache hit: %d → %d, want no growth", loadsAfterMiss, loadsAfterHit)
	}
}

// TestVideoExtractError — ошибка извлечения кадра → понятный OutcomeProcessing,
// без паники.
func TestVideoExtractError(t *testing.T) {
	ext := newFakeVideoExtractor()
	ext.setErr(errors.New("ffmpeg failed"))
	metaS := newFakeMetadataStore()
	env := videoEnv(t, ext, metaS)
	env.src.Add("clip.mp4", []byte("VIDEO-BYTES"))

	ctx := context.Background()
	req := mustReq(t, "", "clip", "mp4", asset.Transform(""), "100x100", 1, "webp")

	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected error from video extraction")
	}
	wantOutcome(t, err, OutcomeProcessing)
	if ext.callCount() != 1 {
		t.Fatalf("VideoExtractor calls = %d, want 1", ext.callCount())
	}
}

// TestVideoExtractorNotConfigured — VideoExtractor не задан → понятная ошибка
// обработки (OutcomeProcessing), без паники.
func TestVideoExtractorNotConfigured(t *testing.T) {
	metaS := newFakeMetadataStore()
	env := newTestEnv(t, func(d *Deps) {
		d.Metadata = metaS
		// VideoExtractor остаётся nil.
	})
	env.src.Add("clip.mp4", []byte("VIDEO-BYTES"))

	ctx := context.Background()
	req := mustReq(t, "", "clip", "mp4", asset.Transform(""), "100x100", 1, "webp")

	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected error when VideoExtractor not configured")
	}
	wantOutcome(t, err, OutcomeProcessing)
}

// TestVideoOptionsDefaults — нулевые настройки в Deps заменяются дефолтами:
// percent=50, step=1, attempts=3; minContrast=0 → проверка контрастности
// пропускается (передаётся как есть).
func TestVideoOptionsDefaults(t *testing.T) {
	s := &Service{deps: Deps{}}
	opts := s.videoOptions()
	if opts.FramePercent != 50 {
		t.Fatalf("FramePercent = %d, want 50", opts.FramePercent)
	}
	if opts.FrameStep != 1 {
		t.Fatalf("FrameStep = %d, want 1", opts.FrameStep)
	}
	if opts.Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", opts.Attempts)
	}
	if opts.MinContrast != 0 {
		t.Fatalf("MinContrast = %v, want 0 (contrast check skipped)", opts.MinContrast)
	}
}

// TestVideoOptionsCustom — заданные настройки в Deps сохраняются как есть.
func TestVideoOptionsCustom(t *testing.T) {
	s := &Service{deps: Deps{
		DefaultVideoFramePercent: 25,
		DefaultVideoMinContrast:  0.4,
		DefaultVideoFrameStep:    5,
		DefaultVideoAttempts:     7,
	}}
	opts := s.videoOptions()
	if opts.FramePercent != 25 {
		t.Fatalf("FramePercent = %d, want 25", opts.FramePercent)
	}
	if opts.MinContrast != 0.4 {
		t.Fatalf("MinContrast = %v, want 0.4", opts.MinContrast)
	}
	if opts.FrameStep != 5 {
		t.Fatalf("FrameStep = %d, want 5", opts.FrameStep)
	}
	if opts.Attempts != 7 {
		t.Fatalf("Attempts = %d, want 7", opts.Attempts)
	}
}

// TestVideoOptionsPassedToExtractor — настройки videoOptions передаются в
// VideoExtractor при извлечении кадра.
func TestVideoOptionsPassedToExtractor(t *testing.T) {
	ext := newFakeVideoExtractor()
	metaS := newFakeMetadataStore()
	env := newTestEnv(t, func(d *Deps) {
		d.VideoExtractor = ext
		d.Metadata = metaS
		d.DefaultVideoFramePercent = 30
		d.DefaultVideoMinContrast = 0.2
		d.DefaultVideoFrameStep = 2
		d.DefaultVideoAttempts = 4
	})
	env.src.Add("clip.mp4", []byte("VIDEO-BYTES"))

	ctx := context.Background()
	req := mustReq(t, "", "clip", "mp4", asset.Transform(""), "100x100", 1, "webp")

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_ = res.Close()

	opts := ext.lastOptions()
	if opts.FramePercent != 30 || opts.MinContrast != 0.2 || opts.FrameStep != 2 || opts.Attempts != 4 {
		t.Fatalf("extractor options = %+v, want {30 0.2 2 4}", opts)
	}
}

// TestIsVideoFormat — распознавание видео-форматов и не-видео.
func TestIsVideoFormat(t *testing.T) {
	video := []string{"mp4", "webm", "mov", "mkv", "avi", "m4v", "MP4", "WebM", "MOV"}
	for _, f := range video {
		if !isVideoFormat(f) {
			t.Errorf("isVideoFormat(%q) = false, want true", f)
		}
	}
	nonVideo := []string{"jpg", "jpeg", "png", "webp", "gif", "svg", "html", "txt"}
	for _, f := range nonVideo {
		if isVideoFormat(f) {
			t.Errorf("isVideoFormat(%q) = true, want false", f)
		}
	}
}

// TestVideoFrameKey — ключ кадра строится как "<видео-ключ>/x.jpg".
func TestVideoFrameKey(t *testing.T) {
	got := videoFrameKey(object.ObjectKey("photos/clip.mp4"))
	if got != "photos/clip.mp4/x.jpg" {
		t.Fatalf("videoFrameKey = %q, want %q", got, "photos/clip.mp4/x.jpg")
	}
}

// TestVideoFrameKeyFromMeta — чтение VideoFrameKey из метаданных.
func TestVideoFrameKeyFromMeta(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.data["clip.mp4"] = &filemeta.FileMetadata{VideoFrameKey: "clip.mp4/x.jpg"}
	s := &Service{deps: Deps{Metadata: metaS}}

	got, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("videoFrameKeyFromMeta: %v", err)
	}
	if got != "clip.mp4/x.jpg" {
		t.Fatalf("frame key = %q, want %q", got, "clip.mp4/x.jpg")
	}
}

// TestVideoFrameKeyFromMetaMissing — отсутствие метаданных → "" без ошибки
// (кадр извлекается заново).
func TestVideoFrameKeyFromMetaMissing(t *testing.T) {
	metaS := newFakeMetadataStore()
	s := &Service{deps: Deps{Metadata: metaS}}

	got, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("videoFrameKeyFromMeta: %v", err)
	}
	if got != "" {
		t.Fatalf("frame key = %q, want empty", got)
	}
}

// TestVideoFrameKeyFromMetaDisabled — метаданные отключены → "" без ошибки.
func TestVideoFrameKeyFromMetaDisabled(t *testing.T) {
	s := &Service{deps: Deps{Metadata: nil}}
	got, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("videoFrameKeyFromMeta: %v", err)
	}
	if got != "" {
		t.Fatalf("frame key = %q, want empty", got)
	}
}

// TestVideoFrameKeyFromMetaLoadError — неизвестная ошибка Load → OutcomeProcessing.
func TestVideoFrameKeyFromMetaLoadError(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.loadErr = errors.New("disk read failed")
	s := &Service{deps: Deps{Metadata: metaS}}

	_, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err == nil {
		t.Fatal("expected error from metadata Load")
	}
	wantOutcome(t, err, OutcomeProcessing)
}

// TestVideoFrameKeyFromMetaCorrupt — ErrCorrupt → "" без ошибки (переизвлечение).
func TestVideoFrameKeyFromMetaCorrupt(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.loadErr = filemeta.ErrCorrupt
	s := &Service{deps: Deps{Metadata: metaS}}

	got, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("videoFrameKeyFromMeta: %v", err)
	}
	if got != "" {
		t.Fatalf("frame key = %q, want empty", got)
	}
}

// TestVideoFrameKeyFromMetaSchemaTooNew — ErrSchemaTooNew → "" без ошибки.
func TestVideoFrameKeyFromMetaSchemaTooNew(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.loadErr = filemeta.ErrSchemaTooNew
	s := &Service{deps: Deps{Metadata: metaS}}

	got, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("videoFrameKeyFromMeta: %v", err)
	}
	if got != "" {
		t.Fatalf("frame key = %q, want empty", got)
	}
}

// TestVideoFrameKeyFromMetaNotFound — ErrNotFound → "" без ошибки.
func TestVideoFrameKeyFromMetaNotFound(t *testing.T) {
	metaS := newFakeMetadataStore()
	metaS.loadErr = filemeta.ErrNotFound
	s := &Service{deps: Deps{Metadata: metaS}}

	got, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("videoFrameKeyFromMeta: %v", err)
	}
	if got != "" {
		t.Fatalf("frame key = %q, want empty", got)
	}
}

// TestVideoFrameKeyFromMetaNil — Load вернул (nil, nil) → "" без ошибки.
func TestVideoFrameKeyFromMetaNil(t *testing.T) {
	s := &Service{deps: Deps{Metadata: &nilMetaStore{}}}
	got, err := s.videoFrameKeyFromMeta(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("videoFrameKeyFromMeta: %v", err)
	}
	if got != "" {
		t.Fatalf("frame key = %q, want empty", got)
	}
}

// nilMetaStore — metadata.Store, чей Load всегда возвращает (nil, nil).
type nilMetaStore struct{}

func (nilMetaStore) Load(context.Context, string) (*filemeta.FileMetadata, error) { return nil, nil }
func (nilMetaStore) Exists(context.Context, string) (bool, error)                 { return false, nil }
func (nilMetaStore) Save(context.Context, string, *filemeta.FileMetadata) error   { return nil }
func (nilMetaStore) Update(context.Context, string, metadata.UpdateFn) error      { return nil }
func (nilMetaStore) Delete(context.Context, string) error                         { return nil }
