package generatev2

import (
	"context"
	"io"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/asset"
)

// mustReqSize строит канонический Request с произвольным Size (в т.ч.
// original "x").
func mustReqSize(t *testing.T, path, srcName, srcFmt string, tr asset.Transform, size asset.Size, dpr int, outFmt string) *asset.Request {
	t.Helper()
	sn, err := asset.NewSourceName(srcName)
	if err != nil {
		t.Fatalf("source name: %v", err)
	}
	sf, err := asset.NewFormat(srcFmt)
	if err != nil {
		t.Fatalf("source format: %v", err)
	}
	of, err := asset.NewFormat(outFmt)
	if err != nil {
		t.Fatalf("output format: %v", err)
	}
	r, err := asset.NewRequest("", sn, sf, tr, size, asset.DPR(dpr), of)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return r
}

// TestGenerateOriginalFastPath — запрос ОРИГИНАЛА (size=x, без transform,
// выходной формат == исходному) отдаётся как есть: процессор не вызывается,
// метаданные не читаются, данные совпадают с исходником.
func TestGenerateOriginalFastPath(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	env := newTestEnv(t, func(d *Deps) {
		d.Metadata = metaS
		d.Detector = det
	})
	env.src.Add("photo.png", []byte("SRC-ORIGINAL"))

	ctx := context.Background()
	// size=x, без transform, output == source (png).
	req := mustReqSize(t, "", "photo", "png", asset.Transform(""), asset.NewOriginalSize(), 1, "png")

	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate original: %v", err)
	}
	defer res.Close()

	data, _ := io.ReadAll(res.Opened)
	if string(data) != "SRC-ORIGINAL" {
		t.Fatalf("original data = %q, want %q", data, "SRC-ORIGINAL")
	}
	// Процессор не вызывался (fast-path минует обработку).
	if env.proc.callCount() != 0 {
		t.Fatalf("processor calls = %d, want 0 (original fast-path)", env.proc.callCount())
	}
	// Метаданные не читались.
	metaS.mu.Lock()
	loads := metaS.loadCalls
	metaS.mu.Unlock()
	if loads != 0 {
		t.Fatalf("metadata Load calls = %d, want 0 (original fast-path)", loads)
	}
}

// TestGenerateOriginalNotFound — оригинал не найден → OutcomeNotFound.
func TestGenerateOriginalNotFound(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	req := mustReqSize(t, "", "missing", "png", asset.Transform(""), asset.NewOriginalSize(), 1, "png")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	wantOutcome(t, err, OutcomeNotFound)
}

// TestGenerateOriginalWithTransformNotFastPath — запрос с transform (даже
// size=x) НЕ является оригиналом: идёт обычный конвейер (процессор
// вызывается).
func TestGenerateOriginalWithTransformNotFastPath(t *testing.T) {
	env := newTestEnv(t)
	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	// transform=crop, size=x → не оригинал (есть transform).
	req := mustReqSize(t, "", "photo", "png", asset.TransformCrop, asset.NewOriginalSize(), 1, "png")
	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer res.Close()
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1 (transform requires processing)", env.proc.callCount())
	}
}

// TestGenerateOriginalFormatMismatchNotFast — выходной формат отличается от
// исходного → нужна конвертация, оригинал не отдаётся как есть.
func TestGenerateOriginalFormatMismatchNotFast(t *testing.T) {
	env := newTestEnv(t)
	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	// size=x, без transform, но output=webp != source=png → конвертация.
	req := mustReqSize(t, "", "photo", "png", asset.Transform(""), asset.NewOriginalSize(), 1, "webp")
	res, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer res.Close()
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1 (format conversion)", env.proc.callCount())
	}
}

// TestGenerateCachedAssetNoMetadataRead — готовый ассет (cache-hit) отдаётся
// как есть: процессор не вызывается повторно и метаданные не читаются.
func TestGenerateCachedAssetNoMetadataRead(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	env := newTestEnv(t, func(d *Deps) {
		d.Metadata = metaS
		d.Detector = det
	})
	env.src.Add("photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 1, "webp")

	// Первый запрос — генерация (кэш пуст).
	res1, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate (miss): %v", err)
	}
	_ = res1.Close()

	// Второй запрос — cache-hit: готовый ассет отдаётся как есть.
	res2, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate (hit): %v", err)
	}
	defer res2.Close()
	if !res2.FromCache {
		t.Fatal("expected cache hit")
	}
	// Процессор не вызывался повторно.
	if env.proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1 (cache hit, no reprocess)", env.proc.callCount())
	}
	// Метаданные не читались при cache-hit (детекция не нужна для crop).
	metaS.mu.Lock()
	loads := metaS.loadCalls
	metaS.mu.Unlock()
	if loads != 0 {
		t.Fatalf("metadata Load calls = %d, want 0 (cache hit)", loads)
	}
}

// TestGenerateNonMediaRejected — не-медиа формат (html) отклоняется, даже
// если файл существует.
func TestGenerateNonMediaRejected(t *testing.T) {
	env := newTestEnv(t)
	env.src.Add("photo.png", []byte("SRC"))
	ctx := context.Background()
	// output=html — не медиа-формат.
	req := mustReq(t, "", "photo", "png", asset.TransformCrop, "100x100", 1, "html")
	_, err := env.svc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected invalid error for non-media format")
	}
	wantOutcome(t, err, OutcomeInvalid)
	// Процессор не должен вызываться.
	if env.proc.callCount() != 0 {
		t.Fatalf("processor calls = %d, want 0 (non-media rejected)", env.proc.callCount())
	}
}

// TestIsMediaFormat — проверка множества медиа-форматов.
func TestIsMediaFormat(t *testing.T) {
	media := []string{"jpeg", "jpg", "png", "webp", "gif", "avif", "heif", "heic", "apng", "jxl", "svg", "svgz", "mp4", "webm", "mov", "mkv", "avi", "m4v", "PNG", "WebP"}
	for _, f := range media {
		if !isMediaFormat(f) {
			t.Errorf("isMediaFormat(%q) = false, want true", f)
		}
	}
	nonMedia := []string{"html", "htm", "json", "txt", "css", "js", "xml", "meta", "go", "md"}
	for _, f := range nonMedia {
		if isMediaFormat(f) {
			t.Errorf("isMediaFormat(%q) = true, want false", f)
		}
	}
}
