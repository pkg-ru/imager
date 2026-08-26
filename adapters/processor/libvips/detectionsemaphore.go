// Двухуровневая схема семафоров для libvips-процессора (Фаза 4).
//
// Проблема: ONNX-инференс детекции (face-crop/object-crop) — CPU-bound
// операция, которая раньше выполнялась ВНУТРИ слота libvips-семафора,
// занимая дорогой cgo-слот на всё время инференса. При потоке fc/oc-запросов
// лёгкие операции (decode/resize/encode) голодали.
//
// Решение — handoff-схема с СТРОГИМ порядком захвата:
//
//	[libvips-слот] → подготовка RGB → [detection-слот] → ОСВОБОДИТЬ
//	libvips-слот → инференс → [libvips-слот] → release detection-слота →
//	кроп/ресайз/экспорт.
//
// Инварианты дедлок-безопасности:
//  1. Detection-слот захватывается ТОЛЬКО пока держится libvips-слот
//     (порядок: сначала vips, потом detection). Обратный порядок никогда
//     не используется, поэтому циклического ожидания нет.
//  2. Во время инференса держится РОВНО ОДИН слот (detection); libvips-слот
//     освобождён. Ни один запрос не держит оба слота одновременно во время
//     долгих операций.
//  3. Ожидание detection-слота ограничено maxWait и отменой ctx; при отказе
//     запрос завершается ошибкой перегрузки БЕЗ освобождения уже занятого
//     libvips-слота (владение остаётся у вызывающего).
//
// Файл без build-tag: логика семафоров не зависит от govips и тестируется в
// любой сборке. cgo-применение схемы — в process_libvips.go (build tag
// "libvips").
package libvips

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/pkg-ru/imager/adapters/processor/shared"
)

// DefaultDetectionConcurrencyFactor — знаменатель дефолтного лимита
// detection-конкурентности: max(1, GOMAXPROCS/2).
const DefaultDetectionConcurrencyFactor = 2

// DefaultDetectionMaxWait — дефолтный бюджет ожидания detection-слота.
// Инференс короткий (десятки мс на CPU), поэтому длительное ожидание сигнализирует
// о перегрузке — быстрее отдать клиенту 503, чем копить хвост задержки.
const DefaultDetectionMaxWait = 5 * time.Second

// ErrTooManyDetectionConcurrency — сигнал переполнения очереди ожидания
// detection-семафора (аналог ErrTooManyConcurrency для основного семафора).
var ErrTooManyDetectionConcurrency = errors.New("libvips: too many concurrent requests waiting for a detection slot")

// DetectionSemaphoreOpts — настройки detection-семафора. Заполняется из
// конфигурации (libvips.detection.*) с fail-fast валидацией; нулевые поля
// заменяются дефолтами через Normalized.
type DetectionSemaphoreOpts struct {
	// Concurrency — максимум одновременных ONNX-инференсов. 0 = дефолт
	// max(1, GOMAXPROCS/2).
	Concurrency int
	// MaxWait — бюджет ожидания detection-слота. 0 = дефолт
	// DefaultDetectionMaxWait; < 0 = ждать без временного лимита (до
	// освобождения слота или отмены ctx).
	MaxWait time.Duration
}

// Validate проверяет корректность настроек (fail-fast на старте):
// отрицательная конкурентность запрещена.
func (o DetectionSemaphoreOpts) Validate() error {
	if o.Concurrency < 0 {
		return fmt.Errorf("detection.concurrency: negative value %d", o.Concurrency)
	}
	if o.MaxWait < 0 && o.MaxWait != -1 {
		return fmt.Errorf("detection.max-wait: negative duration %s", o.MaxWait)
	}
	return nil
}

// Normalized возвращает копию с подстановкой дефолтов вместо нулевых полей.
func (o DetectionSemaphoreOpts) Normalized() DetectionSemaphoreOpts {
	if o.Concurrency <= 0 {
		o.Concurrency = defaultDetectionConcurrency()
	}
	if o.MaxWait == 0 {
		o.MaxWait = DefaultDetectionMaxWait
	}
	return o
}

// defaultDetectionConcurrency — дефолтный лимит detection-конкурентности:
// max(1, GOMAXPROCS/2). Инференс полностью нагружает ядро, поэтому половина
// ядер оставляет пропускную способность лёгким операциям и HTTP-слою.
func defaultDetectionConcurrency() int {
	n := runtime.GOMAXPROCS(0) / DefaultDetectionConcurrencyFactor
	if n < 1 {
		n = 1
	}
	return n
}

// detectionGate — двухуровневый координатор: основной libvips-семофор +
// отдельный detection-семофор с handoff-перекладыванием слотов вокруг
// инференса. Реализует инварианты порядка захвата, описанные в шапке файла.
type detectionGate struct {
	vips *shared.Semaphore // основной слот (decode/resize/encode)
	det  *shared.Semaphore // тяжёлые ONNX-инференсы
}

// newDetectionGate создаёт координатор. detOpts нормализуется здесь же;
// tooManyErr обоих семафоров задаётся вызывающим адаптером (sentinel-ошибки
// передаются как есть, консистентно с shared.NewSemaphore).
func newDetectionGate(vipsSem, detSem *shared.Semaphore) *detectionGate {
	return &detectionGate{vips: vipsSem, det: detSem}
}

// gateSlot — ручка владения ресурсами; закрывается Release. Позволяет
// process_libvips.go перекладывать слоты без прямого доступа к семафорам.
type gateSlot struct {
	g        *detectionGate
	vipsHeld bool // держим ли libvips-слот
	detHeld  bool // держим ли detection-слот
}

// acquireVips занимает основной libvips-слот (лёгкая фаза обработки).
func (g *detectionGate) acquireVips(ctx context.Context) (*gateSlot, error) {
	if err := g.vips.Acquire(ctx); err != nil {
		return nil, err
	}
	return &gateSlot{g: g, vipsHeld: true}, nil
}

// handoffToDetection захватывает detection-слот и освобождает libvips-слот.
//
// Порядок строго детерминирован: сначала Acquire detection (мы по-прежнему
// держим libvips-слот — это разрешённое вложенное ожидание, см. инвариант 1),
// затем Release libvips. Если Acquire не удался (ctx/maxWait/переполнение
// очереди), libvips-слот ОСТАЁТСЯ у вызывающего — обработка может продолжиться
// или корректно завершиться ошибкой без утечки слотов.
//
// Вызов допустим только когда s.vipsHeld == true и s.detHeld == false.
func (s *gateSlot) handoffToDetection(ctx context.Context) error {
	if !s.vipsHeld || s.detHeld {
		return errors.New("libvips: invalid gate state for detection handoff")
	}
	if err := s.g.det.Acquire(ctx); err != nil {
		return err // libvips-слот остаётся у вызывающего
	}
	s.detHeld = true
	s.g.vips.Release()
	s.vipsHeld = false
	return nil
}

// reacquireVips возвращает libvips-слот после инференса (фаза кропа/экспорта).
// Detection-слот удерживается до успешного возврата libvips-слота: это
// гарантирует, что суммарная конкурентность (vips + detection) не превысит
// лимиты ни на мгновение. Если Acquire не удался — detection-слот остаётся
// у вызывающего до Release.
func (s *gateSlot) reacquireVips(ctx context.Context) error {
	if s.vipsHeld || !s.detHeld {
		return errors.New("libvips: invalid gate state for vips reacquire")
	}
	if err := s.g.vips.Acquire(ctx); err != nil {
		return err
	}
	s.vipsHeld = true
	s.g.det.Release()
	s.detHeld = false
	return nil
}

// Release освобождает все удерживаемые слоты (идемпотентно). Используется
// defer'ом в Process и в путях ошибок между фазами.
func (s *gateSlot) Release() {
	if s == nil {
		return
	}
	if s.detHeld {
		s.g.det.Release()
		s.detHeld = false
	}
	if s.vipsHeld {
		s.g.vips.Release()
		s.vipsHeld = false
	}
}

// detectionWaiting возвращает число ожидающих detection-слот (для метрик).
func (g *detectionGate) detectionWaiting() int { return g.det.Waiting() }
