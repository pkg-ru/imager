// Кадровый семафор (frame-workers) для параллельной покадровой обработки
// анимаций в withFrames.
//
// Проблема: покадровые операции (watermark/crop/resize+embed/trim/flip/
// detection-crop) выполняют fn на каждом кадре строго последовательно. Для
// многокадровых анимаций (десятки кадров) это длинная CPU-bound цепочка
// vips-операций, занимающая libvips-слот на всё время.
//
// Решение: worker pool внутри withFrames. Кадры создаются ПОСЛЕДОВАТЕЛЬНО
// (Copy/SetPageHeight/ExtractArea — операции над общим стеком img; вызов
// img.Copy() из нескольких горутин небезопасен), затем fn выполняется
// параллельно на готовых кадрах под отдельным bounded-семафором.
//
// ВАЖНО: это ОТДЕЛЬНЫЙ семафор, не основной libvips-gate (acquireVips).
// Если бы кадры захватывали основной слот, 8 параллельных запросов × 8
// кадров дали бы 64 одновременных vips-операции. Кадровый лимит ограничивает
// параллелизм ВНУТРИ одного запроса, независимо от числа активных запросов.
//
// Лимит по умолчанию: min(GOMAXPROCS, 4), максимум MaxFrameWorkers (8),
// настраивается через конфиг (libvips.frame-workers.workers).
//
// Файл без build-tag: логика семафора и worker pool не зависят от govips
// (обобщённая функция runFramesParallel) и тестируются в любой сборке.
// cgo-применение — в process_libvips.go (build tag "libvips").
package libvips

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"gitverse.ru/pkg-ru/imager/adapters/processor/shared"
)

// MaxFrameWorkers — жёсткий верхний лимит кадровых воркеров: каждый воркер
// выполняет vips-операции, и больше 8 параллельных операций на кадр обычно
// уже не даёт прироста (libvips сам параллелит операции внутри себя по
// ConcurrencyLevel), а память растёт линейно (каждый параллельный fn держит
// пиксельный дрейн своего кадра).
const MaxFrameWorkers = 8

// errFrameSlotOverflow — sentinel переполнения очереди ожидания кадрового
// семафора. На практике недостижим: пул запускает ровно min(n, workers)
// горутин, каждая захватывает один слот, поэтому очередь ожидания никогда
// не превышает workers-1 < max. Передаётся в shared.NewSemaphore для
// консистентности контракта (tooManyErr обязателен).
var errFrameSlotOverflow = fmt.Errorf("libvips: frame semaphore overflow")

// FrameSemaphoreOpts — настройки кадрового семафора. Заполняется из
// конфигурации (libvips.frame-workers.*) с fail-fast валидацией; нулевые
// поля заменяются дефолтами через Normalized.
type FrameSemaphoreOpts struct {
	// Workers — максимум одновременных fn на кадрах внутри одного withFrames.
	// 0 = дефолт min(GOMAXPROCS, 4); значение клэмпится до [1, MaxFrameWorkers].
	Workers int
}

// Validate проверяет корректность настроек (fail-fast на старте):
// отрицательное число воркеров запрещено.
func (o FrameSemaphoreOpts) Validate() error {
	if o.Workers < 0 {
		return fmt.Errorf("frame-workers.workers: negative value %d", o.Workers)
	}
	return nil
}

// Normalized возвращает копию с подстановкой дефолтов вместо нулевых полей
// и клэмпингом воркеров в диапазон [1, MaxFrameWorkers].
func (o FrameSemaphoreOpts) Normalized() FrameSemaphoreOpts {
	if o.Workers <= 0 {
		o.Workers = defaultFrameWorkers()
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Workers > MaxFrameWorkers {
		o.Workers = MaxFrameWorkers
	}
	return o
}

// defaultFrameWorkers — дефолтный лимит кадровых воркеров:
// min(GOMAXPROCS, 4). Покадровый fn — CPU-bound vips-операция; 4 воркеров
// обычно достаточно, чтобы насытить типичный контейнер, не умножая память.
func defaultFrameWorkers() int {
	n := runtime.GOMAXPROCS(0)
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

// newFrameSemaphore создаёт кадровый семафор с лимитом из opts (после
// Normalized). Очередь ожидания ограничена числом слотов; maxWait = 0
// (без временного лимита) — переполнение очереди невозможно по построению
// пула (см. errFrameSlotOverflow).
func newFrameSemaphore(opts FrameSemaphoreOpts) *shared.Semaphore {
	w := opts.Normalized().Workers
	return shared.NewSemaphore(w, 0, errFrameSlotOverflow)
}

// runFramesParallel — обобщённый worker pool покадровой обработки.
//
// Кадры создаются последовательно через makeFrame(i) (безопасный вариант:
// img.Copy() из нескольких горутин не используется), затем fn выполняется
// параллельно на готовых кадрах. Параллелизм ограничен семафором sem
// (кадровый семафор, НЕ основной libvips-gate) и числом воркеров
// workers = min(n, лимит семафора).
//
// Гарантии:
//   - порядок результатов соответствует порядку кадров (slice по индексу);
//   - при ошибке fn на каком-либо кадре запуск НОВЫХ кадров прекращается,
//     уже запущенные дожидаются, возвращается ошибка с НАИМЕНЬШИМ индексом
//     кадра (детерминизм при нескольких ошибках);
//   - ctx (может быть nil) прерывает запуск новых кадров; уже запущенные
//     fn не прерываются принудительно (vips-операции не отменяемы) — их
//     результат игнорируется;
//   - n <= 1 или workers <= 1 — последовательный путь без горутин
//     (поведение одиночного кадра не меняется).
//
// Ресурсы кадров НЕ освобождаются здесь: владелец (withFrames) закрывает
// их после сборки стека или при ошибке, как и в последовательной версии.
func runFramesParallel[T any](ctx context.Context, n, workers int, sem *shared.Semaphore, makeFrame func(i int) (T, error), fn func(f T, i int) error) ([]T, error) {
	// nil ctx допустим (withFrames не имеет сигнатуры с ctx): подставляем
	// background, чтобы sem.Acquire не получил nil.
	if ctx == nil {
		ctx = context.Background()
	}
	// Последовательный путь: одиночный кадр или выключенный параллелизм.
	if n <= 1 || workers <= 1 {
		frames := make([]T, 0, max(n, 0))
		for i := 0; i < n; i++ {
			f, err := makeFrame(i)
			if err != nil {
				return nil, err
			}
			if err := fn(f, i); err != nil {
				return nil, err
			}
			frames = append(frames, f)
		}
		return frames, nil
	}

	if workers > n {
		workers = n
	}

	// Фаза 1: последовательное создание всех кадров (Copy/SetPageHeight/
	// ExtractArea на копии стека img — небезопасно из горутин). При ошибке
	// создания кадра уже созданные НЕ закрываются здесь: владелец (withFrames)
	// получает частичный slice и закрывает его своей closeFrames-логикой.
	frames := make([]T, 0, n)
	for i := 0; i < n; i++ {
		f, err := makeFrame(i)
		if err != nil {
			return frames, err
		}
		frames = append(frames, f)
	}

	// Фаза 2: параллельное применение fn. Пул из `workers` горутин разбирает
	// индексы кадров через атомарный счётчик; семафор ограничивает число
	// одновременных fn. Ошибка/отмена ctx останавливает РАЗБОР новых
	// индексов (уже запущенные fn дожидаются).
	errs := make([]error, n) // ошибка fn по индексу кадра (nil = успеха)
	var mu sync.Mutex        // защищает next/stop
	next := 0                // следующий индекс кадра для разбора
	stop := false            // флаг прекращения запуска новых fn

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if stop || next >= n {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()

				// Отмена ctx прекращает запуск новых кадров. Проверка здесь
				// обязательна: быстрый путь Semaphore.Acquire (свободный
				// токен) не проверяет ctx.Done, и без неё воркеры продолжали
				// бы разбирать кадры после отмены.
				if ctx.Err() != nil {
					mu.Lock()
					stop = true
					mu.Unlock()
					return
				}

				// Слот кадрового семафора: ограничивает одновременные
				// vips-операции на кадрах. Отмена ctx — тоже прекращение
				// запуска новых кадров (уже запущенные дожидаются в wg).
				if err := sem.Acquire(ctx); err != nil {
					mu.Lock()
					stop = true
					mu.Unlock()
					return
				}
				err := fn(frames[i], i)
				sem.Release()

				if err != nil {
					errs[i] = err
					mu.Lock()
					stop = true
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	// Первая ошибка по порядку кадров (детерминированный результат).
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			return frames, errs[i]
		}
	}
	// Ошибка семафора/ctx: ни один кадр не помечен — возвращаем её как есть.
	// (Достижимо только при отмене ctx между итерациями пула.)
	return frames, nil
}
