// Конвейер покадровой подготовки AVIF-кадров: параллельная материализация
// пикселей + строго упорядоченное потребление (AddFrame).
//
// Проблема: последовательный экспорт анимированного AVIF на каждый кадр
// выполняет цепочку Copy → SetPageHeight → ExtractArea → RawRGBAPixels
// (полный пиксельный дрейн) → enc.AddFrame (AV1-кодирование через libheif).
// RawRGBAPixels — CPU-bound операция, а AddFrame — единственный
// последовательный этап (libheif-контекст/трек один на ассет, AddFrame не
// потокобезопасен и кадры обязаны идти в порядке последовательности).
//
// Решение: конвейер из трёх фаз.
//   - dispatcher (одна горутина): ПОСЛЕДОВАТЕЛЬНОЕ создание кадров
//     makeFrame(i) — операции над общим стеком img; вызов img.Copy() из
//     нескольких горутин небезопасен (см. framesemaphore.go);
//   - workers (пул горутин): параллельный drain пикселей drain(f, i) —
//     тяжёлый RawRGBAPixels;
//   - consumer (одна горутина): упорядоченное потребление результатов
//     consume(r, i) строго в порядке кадров (переупорядочивание по индексам
//     с окном pending).
//
// Ограничение памяти: кадр занимает слот от создания (makeFrame) до
// потребления (consume) — «в полёте» не более workers кадров (vips-изображение
// + пиксельный буфер) независимо от скорости AddFrame. Все кадры сразу не
// буферизуются.
//
// Ошибки/отмена: первая ошибка (makeFrame/drain/consume) или отмена ctx
// прекращают запуск новых кадров; уже запущенные дожидаются, необработанные
// кадры освобождаются через discard; возвращается детерминированная ошибка
// (consume → первая по индексу ошибка подготовки → ошибка ctx).
//
// Файл без build-tag: логика конвейера не зависит от govips (обобщённая
// функция runAvifFramePipeline) и тестируется в любой сборке. cgo-применение —
// в animated_avif_libvips.go (build tag "libvips").
package libvips

import (
	"context"
	"sync"
)

// avifPipelineFrame — кадр, созданный dispatcher'ом и переданный воркеру.
type avifPipelineFrame[F any] struct {
	index int
	frame F
}

// avifPipelineResult — результат drain'а кадра, переданный потребителю.
type avifPipelineResult[R any] struct {
	index  int
	result R
}

// avifFramePixels — материализованные пиксели кадра + метаданные для
// enc.AddFrame (результат drain-фазы конвейера AVIF-экспорта).
type avifFramePixels struct {
	pixels   []byte
	hasAlpha bool
	duration uint32
}

// runAvifFramePipeline — конвейер покадровой обработки для анимированного
// AVIF-экспорта.
//
// Параметры:
//   - makeFrame(i) — последовательное создание кадра i (dispatcher);
//   - drain(f, i) — параллельная материализация пикселей (воркеры); владеет
//     кадром f и обязан освободить его ресурсы (в т.ч. при ошибке);
//   - consume(r, i) — последовательное потребление результата кадра i
//     (вызывается строго в порядке i = 0..n-1, из одной горутины);
//   - discard(f) — освобождение кадра, который не дошёл до drain из-за
//     остановки конвейера (ошибка/отмена).
//
// Гарантии:
//   - consume вызывается строго в порядке кадров, из одной горутины;
//   - «в полёте» не более workers кадров (слот от makeFrame до consume);
//   - при n <= 1 или workers <= 1 — последовательный путь без горутин
//     (поведение идентично прежнему последовательному циклу);
//   - первая ошибка прекращает запуск новых кадров, уже запущенные
//     дожидаются, необработанные кадры освобождаются через discard;
//   - результат детерминирован: ошибка consume → первая по индексу ошибка
//     подготовки (makeFrame/drain) → ошибка внешнего ctx;
//   - ctx (может быть nil) прерывает ожидание слотов и выдачу результатов;
//     уже запущенные drain не прерываются принудительно (vips-операции не
//     отменяемы) — их результаты игнорируются.
func runAvifFramePipeline[F, R any](
	ctx context.Context,
	n, workers int,
	makeFrame func(i int) (F, error),
	drain func(f F, i int) (R, error),
	consume func(r R, i int) error,
	discard func(f F),
) error {
	// nil ctx допустим (exportAnimatedAvif не имеет сигнатуры с ctx).
	if ctx == nil {
		ctx = context.Background()
	}
	// Последовательный путь: одиночный кадр или выключенный параллелизм —
	// без горутин, тот же порядок операций, что в прежнем цикле.
	if n <= 1 || workers <= 1 {
		for i := 0; i < n; i++ {
			f, err := makeFrame(i)
			if err != nil {
				return err
			}
			r, err := drain(f, i)
			if err != nil {
				return err
			}
			if err := consume(r, i); err != nil {
				return err
			}
		}
		return nil
	}
	if workers > n {
		workers = n
	}

	// Внутренний ctx с отменой: любая ошибка конвейера прекращает ожидание
	// слотов и выдачу результатов. parent сохранён для возврата ошибки
	// ВНЕШНЕЙ отмены (внутренний cancel не должен её маскировать).
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Слоты памяти: кадр занимает слот от создания до потребления. Гарантирует
	// ≤ workers кадров «в полёте» (vips-изображение + пиксельный буфер)
	// независимо от скорости consume (AddFrame).
	slots := make(chan struct{}, workers)

	// Каналы: ёмкость = workers. Инвариант: каждый элемент в канале удерживает
	// слот, слотов ≤ workers = cap(канала), поэтому send'ы никогда не
	// блокируются (см. доказательство в комментариях у dispatcher/воркеров).
	framesCh := make(chan avifPipelineFrame[F], workers)
	resultsCh := make(chan avifPipelineResult[R], workers)

	// stop — прекращение запуска новых кадров при первой ошибке; errs —
	// ошибка подготовки по индексу кадра (для детерминизма берётся первая
	// по порядку).
	var mu sync.Mutex
	stop := false
	errs := make([]error, n)
	fail := func(i int, err error) {
		mu.Lock()
		errs[i] = err
		stop = true
		mu.Unlock()
		cancel()
	}
	stopped := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return stop
	}

	// Dispatcher: последовательное создание кадров под слотами памяти.
	// Слот захватывается ДО выбора индекса — индексы раздаются строго по
	// порядку, поэтому «в полёте» всегда ровно min(workers, n) первых
	// незавершённых кадров, и потребитель никогда не ждёт кадр, который ещё
	// не создан (нет deadlock при медленном consume).
	var dwg sync.WaitGroup
	dwg.Add(1)
	go func() {
		defer dwg.Done()
		defer close(framesCh)
		for i := 0; i < n; i++ {
			if stopped() {
				return
			}
			// Ожидание слота прерывается отменой (внутренней — после ошибки,
			// или внешней).
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			// Слот захвачен, но конвейер могли остановить, пока мы ждали.
			if stopped() {
				<-slots
				return
			}
			f, err := makeFrame(i)
			if err != nil {
				fail(i, err)
				<-slots
				return
			}
			// Send не блокируется: кадр удерживает слот, кадров в канале
			// ≤ workers-1 < cap (остальные слоты — у кадров в drain/очереди
			// результатов).
			framesCh <- avifPipelineFrame[F]{index: i, frame: f}
		}
	}()

	// Воркеры: параллельный drain пикселей. После остановки конвейера
	// оставшиеся кадры из framesCh освобождаются через discard (утечки
	// cgo-ресурсов исключены): воркер читает framesCh до его закрытия.
	var wwg sync.WaitGroup
	wwg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wwg.Done()
			for it := range framesCh {
				if stopped() {
					discard(it.frame)
					continue
				}
				r, err := drain(it.frame, it.index)
				if err != nil {
					fail(it.index, err)
					continue
				}
				// Send не блокируется: результат удерживает слот кадра,
				// результатов в канале ≤ workers-1 < cap.
				resultsCh <- avifPipelineResult[R]{index: it.index, result: r}
			}
		}()
	}

	// Потребитель (текущая горутина): упорядоченное потребление результатов.
	// Внеочередные результаты буферизуются в pending (окно переупорядочивания:
	// ≤ workers-1 элементов, т.к. в полёте ≤ workers кадров).
	pending := make(map[int]R, workers)
	next := 0
	var consumeErr error
consumeLoop:
	for next < n {
		select {
		case res := <-resultsCh:
			pending[res.index] = res.result
			for {
				r, ok := pending[next]
				if !ok {
					break
				}
				delete(pending, next)
				if err := consume(r, next); err != nil {
					consumeErr = err
					cancel()
					break consumeLoop
				}
				// Слот освобождается только здесь: память ограничена даже
				// при медленном consume.
				<-slots
				next++
			}
		case <-ctx.Done():
			// Внутренняя отмена (ошибка подготовки) или внешний ctx.
			break consumeLoop
		}
	}

	// Дожидаемся остановки конвейера: dispatcher закрывает framesCh, воркеры
	// дренируют его (discard необработанных кадров). После этого горутины
	// завершены — утечек нет.
	dwg.Wait()
	wwg.Wait()

	// Детерминированный результат: ошибка consume (самая ранняя по порядку
	// обработки) приоритетна; иначе первая по индексу ошибка подготовки;
	// иначе — ошибка внешней отмены (nil при успехе).
	if consumeErr != nil {
		return consumeErr
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			return errs[i]
		}
	}
	return parent.Err()
}
