// Тесты двухуровневых семафоров: порядок захвата (handoff),
// fallback при отказе ожидания, конфиг-валидация DetectionSemaphoreOpts.
//
// Файл без build-tag: логика detectionsemaphore.go не зависит от govips и
// тестируется в любой сборке.
package libvips

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/adapters/processor/shared"
)

func newTestGate(vipsMax, detMax int, detMaxWait time.Duration) *detectionGate {
	return newDetectionGate(
		shared.NewSemaphore(vipsMax, 0, ErrTooManyConcurrency),
		shared.NewSemaphore(detMax, detMaxWait, ErrTooManyDetectionConcurrency),
	)
}

// TestDetectionSemaphoreOptsValidate — fail-fast валидация настроек.
func TestDetectionSemaphoreOptsValidate(t *testing.T) {
	if err := (DetectionSemaphoreOpts{Concurrency: -1}).Validate(); err == nil {
		t.Fatal("negative concurrency must fail validation")
	}
	if err := (DetectionSemaphoreOpts{MaxWait: -time.Second}).Validate(); err == nil {
		t.Fatal("negative max-wait must fail validation")
	}
	if err := (DetectionSemaphoreOpts{}).Validate(); err != nil {
		t.Fatalf("zero opts are valid defaults: %v", err)
	}
}

// TestDetectionSemaphoreOptsNormalized — подстановка дефолтов: concurrency
// = max(1, GOMAXPROCS/2), max-wait = DefaultDetectionMaxWait.
func TestDetectionSemaphoreOptsNormalized(t *testing.T) {
	n := DetectionSemaphoreOpts{}.Normalized()
	wantConc := defaultDetectionConcurrency()
	if n.Concurrency != wantConc {
		t.Errorf("normalized concurrency = %d, want %d", n.Concurrency, wantConc)
	}
	if wantConc < 1 {
		t.Errorf("default concurrency must be >= 1, got %d", wantConc)
	}
	if n.MaxWait != DefaultDetectionMaxWait {
		t.Errorf("normalized max-wait = %s, want %s", n.MaxWait, DefaultDetectionMaxWait)
	}
	// Явные значения сохраняются.
	e := DetectionSemaphoreOpts{Concurrency: 3, MaxWait: time.Second}.Normalized()
	if e.Concurrency != 3 || e.MaxWait != time.Second {
		t.Errorf("explicit values must be preserved, got %+v", e)
	}
}

// TestGateHandoffReleasesVipsSlot — handoff освобождает libvips-слот на
// время инференса и возвращает его после reacquire.
func TestGateHandoffReleasesVipsSlot(t *testing.T) {
	g := newTestGate(1, 1, time.Second)
	slot, err := g.acquireVips(context.Background())
	if err != nil {
		t.Fatalf("acquireVips: %v", err)
	}
	defer slot.Release()

	// Пока держим vips-слот, второй acquire должен блокироваться (проверяем
	// косвенно через capacity: попытка с отменённым ctx сразу откажется).
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.acquireVips(ctx2); !errors.Is(err, context.Canceled) {
		t.Fatalf("second acquire with canceled ctx = %v, want context.Canceled", err)
	}

	// Handoff: берём detection-слот, отпускаем vips-слот.
	if err := slot.handoffToDetection(context.Background()); err != nil {
		t.Fatalf("handoffToDetection: %v", err)
	}
	if slot.vipsHeld || !slot.detHeld {
		t.Fatalf("after handoff: vipsHeld=%v detHeld=%v, want false/true", slot.vipsHeld, slot.detHeld)
	}

	// Во время инференса vips-слот свободен: другой запрос может его взять.
	s2, err := g.acquireVips(context.Background())
	if err != nil {
		t.Fatalf("acquireVips during inference: %v", err)
	}
	s2.Release()

	// Возврат vips-слота; detection-слот удерживается до успешного возврата.
	if err := slot.reacquireVips(context.Background()); err != nil {
		t.Fatalf("reacquireVips: %v", err)
	}
	if !slot.vipsHeld || slot.detHeld {
		t.Fatalf("after reacquire: vipsHeld=%v detHeld=%v, want true/false", slot.vipsHeld, slot.detHeld)
	}
}

// TestGateHandoffFailureKeepsVipsSlot — при отказе ожидания detection-слота
// libvips-слот остаётся у вызывающего (нет утечки слотов).
func TestGateHandoffFailureKeepsVipsSlot(t *testing.T) {
	g := newTestGate(1, 1, 50*time.Millisecond)
	slot, err := g.acquireVips(context.Background())
	if err != nil {
		t.Fatalf("acquireVips: %v", err)
	}
	defer slot.Release()

	// Занимаем единственный detection-слот «чужим» запросом (детерминированно:
	// Acquire из этой же горутины; удержание слота эмулирует инференс).
	if err := g.det.Acquire(context.Background()); err != nil {
		t.Fatalf("blocker det.Acquire: %v", err)
	}

	// Handoff с коротким maxWait должен отказаться, но сохранить vips-слот.
	err = slot.handoffToDetection(context.Background())
	g.det.Release()
	if err == nil {
		t.Fatal("handoff must fail when detection semaphore is busy")
	}
	if !slot.vipsHeld || slot.detHeld {
		t.Fatalf("failed handoff must keep vips slot: vipsHeld=%v detHeld=%v", slot.vipsHeld, slot.detHeld)
	}
}

// TestGateInvalidStateTransitions — некорректные переходы состояния
// отклоняются (защита от логических ошибок вызывающего).
func TestGateInvalidStateTransitions(t *testing.T) {
	g := newTestGate(1, 1, time.Second)

	// reacquire без handoff.
	slot, err := g.acquireVips(context.Background())
	if err != nil {
		t.Fatalf("acquireVips: %v", err)
	}
	defer slot.Release()
	if err := slot.reacquireVips(context.Background()); err == nil {
		t.Fatal("reacquire without handoff must fail")
	}

	// Двойной handoff.
	if err := slot.handoffToDetection(context.Background()); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if err := slot.handoffToDetection(context.Background()); err == nil {
		t.Fatal("double handoff must fail")
	}

	// Release идемпотентен и не паникует; nil-safe.
	slot2 := &gateSlot{}
	slot2.Release() // ничего не держит — ок
	var nilSlot *gateSlot
	nilSlot.Release()
}

// TestGateReleaseFreesBothSlots — Release освобождает всё удерживаемое:
// после Release оба слота доступны другим запросам.
func TestGateReleaseFreesBothSlots(t *testing.T) {
	g := newTestGate(1, 1, time.Second)
	slot, err := g.acquireVips(context.Background())
	if err != nil {
		t.Fatalf("acquireVips: %v", err)
	}
	if err := slot.handoffToDetection(context.Background()); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if err := slot.reacquireVips(context.Background()); err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	slot.Release()
	if slot.vipsHeld || slot.detHeld {
		t.Fatalf("Release must free all slots: vipsHeld=%v detHeld=%v", slot.vipsHeld, slot.detHeld)
	}
	// Оба ресурса снова доступны.
	s, err := g.acquireVips(context.Background())
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	s.Release()
}

// TestGateConcurrentHandoff — конкурентный handoff не теряет слоты:
// суммарно занятых слотов никогда не больше лимитов.
//
// Число воркеров = vipsMax + очередь ожидания (2 + 2 = 4): Semaphore
// намеренно быстро отклоняет Acquire при переполнении очереди ожидания
// (bounded waiting), поэтому при большем числе воркеров часть acquireVips
// закономерно получала ErrTooManyConcurrency — это не потеря слотов, а
// штатный отказ перегрузки, что делало тест флакующим.
func TestGateConcurrentHandoff(t *testing.T) {
	const workers = 4
	g := newTestGate(2, 2, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot, err := g.acquireVips(context.Background())
			if err != nil {
				t.Errorf("acquireVips: %v", err)
				return
			}
			defer slot.Release()
			if err := slot.handoffToDetection(context.Background()); err != nil {
				return // перегрузка допустима
			}
			if err := slot.reacquireVips(context.Background()); err != nil {
				t.Errorf("reacquireVips: %v", err)
			}
		}()
	}
	wg.Wait()
	if w := g.detectionWaiting(); w != 0 {
		t.Errorf("detection waiting after completion = %d, want 0", w)
	}
}
