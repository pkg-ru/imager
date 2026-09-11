package generatev2

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// recoverProcProcessor — processor.Processor + RGBPreparer со счётчиком
// вызовов Process (для проверки «обработка перевыполняется, но модель — нет»).
type recoverProcProcessor struct {
	calls atomic.Int64
}

func (p *recoverProcProcessor) Process(_ context.Context, _ processor.Input, _ io.Writer) (*processor.Result, error) {
	p.calls.Add(1)
	return &processor.Result{
		Size:         0,
		Width:        100,
		Height:       100,
		SourceWidth:  100,
		SourceHeight: 100,
	}, nil
}

func (p *recoverProcProcessor) PrepareRGB(_ context.Context, _ io.ReadSeeker) (*processor.RGBFrame, error) {
	return &processor.RGBFrame{Pixels: make([]byte, 2*2*3), Width: 2, Height: 2}, nil
}

var _ processor.Processor = (*recoverProcProcessor)(nil)
var _ processor.RGBPreparer = (*recoverProcProcessor)(nil)

// TestGeneratePublishLossRecoveryReusesMetadata — A6: самовосстановление после
// потери публикации (crash/failure до publish, но после записи метаданных).
//
// Сценарий симулирует SIGKILL/crash: публикация ассета в кэш падает, однако
// sidecar-метаданные (детекция, created_unix, largest_ai_asset) записываются
// ДО публикации. Повторный запрос того же ассета:
//   - tryCache → miss (ассета в кэше нет);
//   - повторная генерация выполняется, НО боксы детекции переиспользуются из
//     sidecar — модель детекции вызывается РОВНО один раз суммарно;
//   - ассет в итоге публикуется (кэш заполняется).
//
// Это «ленивый reconciler»: восстановление происходит за счёт штатного
// cache-miss пути генерации + переиспользования метаданных (инвариант
// «1 вызов модели на родителя»), без WAL-журнала и startup-scan'а.
func TestGeneratePublishLossRecalRecalizesFromMetadata(t *testing.T) {
	metaS := newFakeMetadataStore()
	det := newFakeDetector()
	proc := &recoverProcProcessor{}
	env := newTestEnv(t, func(d *Deps) {
		d.Metadata = metaS
		d.Detector = det
		d.Processor = proc
	})
	env.src.Add("photo.png", []byte("SRC"))

	ctx := context.Background()
	req := mustReq(t, "", "photo", "png", asset.CropFace, false, "100x100", 1, "webp")
	key := object.ObjectKey("photo-png/100x100.webp")

	// Фаза 1 — «crash»: публикация в кэш недоступна. Генерация доходит до
	// детекции (модель вызывается 1 раз, боксы записываются в sidecar), но
	// publish падает → запрос ошибки, ассета в кэше нет.
	env.res.SetPubErr(object.ErrUnavailable)
	if _, err := env.svc.Generate(ctx, req); err == nil {
		t.Fatalf("Generate: want error when publish is unavailable (simulated crash)")
	}
	env.res.SetPubErr(nil)

	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls after failed publish = %d, want 1", got)
	}
	if env.res.Has(key) {
		t.Fatalf("asset %q must not be published after failed publish", key)
	}
	metaS.mu.Lock()
	saved := metaS.data[string(key)]
	metaS.mu.Unlock()
	if saved == nil || len(saved.Faces) == 0 {
		t.Fatalf("sidecar faces = %+v, want persisted detections (metadata written before publish)", saved)
	}

	// Фаза 2 — «перезапуск»: повторный запрос того же ассета.
	res2, err := env.svc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate after recovery: %v", err)
	}
	defer res2.Close()

	// Ассет опубликован (кэш заполнен) — самовосстановление состоялось.
	if !env.res.Has(key) {
		t.Fatalf("asset %q not published after recovery", key)
	}
	// Модель по-прежнему вызвана ровно ОДИН раз: боксация переиспользована
	// из sidecar, несмотря на повторную генерацию.
	if got := det.facesCalls.Load(); got != 1 {
		t.Fatalf("DetectFaces calls after recovery = %d, want 1 (metadata reused)", got)
	}
	// Обработка была перевыполнена (это цена ленивого восстановления).
	if got := proc.calls.Load(); got != 2 {
		t.Fatalf("processor calls after recovery = %d, want 2 (regenerated)", got)
	}
}
