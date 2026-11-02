package learning

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/policy"
	"github.com/pkg-ru/imager/observability"
)

// Recorder — сборщик наблюдений learning-mode.
//
// Observe неблокирующий: наблюдения кладутся в buffered channel и
// обрабатываются горутиной-потребителем. Запись в generate-local.yaml
// дебаунсится: не чаще 1 раза в 2 секунды при потоке наблюдений +
// финальная запись в Stop(). Ошибка записи логируется (ERROR), состояние
// в памяти сохраняется, ретрай на следующем наблюдении.
type Recorder struct {
	dir         string // каталог конфигов (файл generate-local.yaml внутри)
	state       map[string]policy.PathPolicyConfig
	presetNames map[string]struct{} // имена пресетов из конфигурации
	logger      observability.Logger
	ch          chan *asset.Request
	done        chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup

	mu         sync.Mutex
	dirty      bool        // есть незаписанные изменения
	lastWrite  time.Time   // время последней записи
	writeTimer *time.Timer // отложенная запись (дебаунс)
	// stopping: Stop начался — новые записи больше не планируются (таймер
	// погашен). Наблюдения, уже лежащие в канале, ВСЁ РАВНО обрабатываются
	// потребителем: дренаж — часть контракта durability (Stop обязан их
	// записать). Фильтр "после Stop ничего не принимаем" — finished.
	stopping bool
	// finished: дренаж завершён, Stop сделал финальную запись. Записи,
	// пришедшие после Stop, игнорируются (поздний flush затёр бы финальное
	// состояние — например, learning-mode: false от ResetLearningMode).
	finished bool
	closed   bool // канал ch закрыт: Observe больше не отправляет
	// drainedCount — число наблюдений, дошедших до потребителя (не
	// отброшенных фильтрами record). Только для диагностики/тестов:
	// "наблюдение потеряно" должно быть отличимо от "наблюдение вообще не
	// дошло до потребителя". Инкремент под r.mu — гонок нет.
	drainedCount int

	// writeMu сериализует ВЕСЬ flush (снятие snapshot + запись файла), чтобы
	// два конкурентных flush не теряли наблюдения: без него оба могли снять
	// snapshot с dirty=true, и запись более старого snapshot перезаписала бы
	// более новый.
	writeMu sync.Mutex
}

// writeInterval — минимальный интервал между записями (дебаунс).
const writeInterval = 2 * time.Second

// channelCap — ёмкость буфера наблюдений.
const channelCap = 256

// localFileName — имя локального конфига слоя generate.
const localFileName = "generate-local.yaml"

// Deps — зависимости Recorder.
type Deps struct {
	// ConfigDir — каталог конфигов (файл generate-local.yaml внутри).
	ConfigDir string
	// Initial — эффективный конфиг политики из загруженного конфига;
	// Initial.PathPolicies — начальное состояние.
	Initial policy.Config
	// PresetNames — набор имён пресетов из конфигурации (policy.presets).
	// Сегмент, совпадающий с одним из имён, наблюдается как пресет
	// (пополняет presets path-policy), а не отбрасывается.
	PresetNames map[string]struct{}
	// Logger — логгер (nil = no-op).
	Logger observability.Logger
}

// NewRecorder создаёт Recorder и запускает горутину-потребителя.
// Начальное состояние — Initial.PathPolicies.
func NewRecorder(d Deps) (*Recorder, error) {
	if d.ConfigDir == "" {
		return nil, os.ErrInvalid
	}
	logger := d.Logger
	if logger == nil {
		logger = observability.NopLogger()
	}
	initialState := NormalizeState(d.Initial.PathPolicies)
	r := &Recorder{
		dir:         d.ConfigDir,
		state:       initialState,
		presetNames: d.PresetNames,
		logger:      logger,
		ch:          make(chan *asset.Request, channelCap),
		done:        make(chan struct{}),
		lastWrite:   time.Now(),
		dirty:       len(initialState) > 0,
	}
	r.wg.Add(1)
	go r.consume()
	return r, nil
}

// Observe регистрирует наблюдение (неблокирующе). req == nil игнорируется.
// Из req извлекаются: path-префикс (каталог исходника), сегмент и формат:
//   - сегмент — размер-грамматика (asset.ParseSize) → custom-наблюдение
//     (AddObservation);
//   - сегмент — имя пресета из Deps.PresetNames → пресетное наблюдение
//     (AddPresetObservation);
//   - иначе наблюдение игнорируется.
//
// Отправка выполняется под r.mu — тем же мьютексом, который держит Stop при
// закрытии канала. Это исключает panic "send on closed channel" в окне между
// проверкой Enabled() в Service.Observe и фактической отправкой (гонка на
// graceful shutdown). select с default не блокирует, поэтому удержание
// мьютекса на время отправки безопасно.
func (r *Recorder) Observe(req *asset.Request) {
	if req == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	select {
	case r.ch <- req:
	default:
		// Буфер полон — наблюдение отбрасывается (non-blocking).
	}
}

// ResetLearningMode персистентно сбрасывает learning-mode: записывает
// policy.learning-mode = false в generate-local.yaml (comment-preserving).
// Best-effort: ошибка записи логируется, но не прерывает shutdown.
func (r *Recorder) ResetLearningMode() {
	r.persistLearningMode(false)
}

// persistLearningMode записывает policy.learning-mode = enabled в
// generate-local.yaml.
func (r *Recorder) persistLearningMode(enabled bool) {
	file := filepath.Join(r.dir, localFileName)
	if err := SetLearningMode(file, enabled); err != nil {
		r.logger.Errorf("learning: write learning-mode=%v to %s: %v", enabled, file, err)
	}
}

// Stop останавливает потребителя: drain канала + финальная запись.
//
// Гарантия (durability contract): каждое наблюдение, принятое Observe до
// начала Stop и не отброшенное переполнением буфера, учтено в состоянии
// Recorder и попадает в generate-local.yaml после возврата Stop.
//
//   - сначала под r.mu: stopping=true (новые записи не планируются,
//     scheduleWriteLocked выходит) и гасится таймер дебаунса. Если callback
//     таймера уже стартовал, он ждёт r.mu и либо сработает до финального
//     flush (тогда финальный flush увидит dirty=false и просто выйдет —
//     данные уже на диске), либо после (тогда финальный flush запишет всё
//     сам, а поздний callback найдёт dirty=false). То есть потеря
//     наблюдения при любом межпотоковом раскладе невозможна;
//   - затем close(ch) под тем же r.mu — атомарно относительно Observe
//     ("send on closed channel" невозможен);
//   - затем wg.Wait() — дренаж буфера потребителем (фоновых flush в wg
//     нет: единственный источник асинхронной записи — дебаунс-таймер,
//     синхронизированный через r.mu/writeMu);
//   - затем финальный flush под writeMu: snapshot снимается с полного
//     состояния;
//   - затем finished=true: LAST-запись. Она нужна, чтобы опоздавший
//     callback таймера (или позднее наблюдение) не затёр финальное
//     состояние — например, learning-mode: false от ResetLearningMode.
//
// Порядок блокировок: r.mu -> (release) -> wg.Wait() -> writeMu. Держать
// writeMu одновременно с ожиданием wg нельзя: callback таймера берёт r.mu
// перед writeMu (см. onTimer/flush) — взаимная блокировка.
func (r *Recorder) Stop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopping = true
		r.stopTimerLocked()
		r.closed = true
		close(r.ch)
		r.mu.Unlock()

		// Дренаж буфера потребителем.
		r.wg.Wait()

		// Финальная запись — dirty отражает ВСЕ дренированные наблюдения.
		r.flush()

		// finished выставляется только после финального flush: то, что
		// попало в state до этого момента, уже на диске. Записи, пришедшие
		// после, игнорируются (иначе поздний flush затёр бы финальное
		// состояние — например, learning-mode: false от ResetLearningMode).
		r.mu.Lock()
		r.finished = true
		r.mu.Unlock()
	})
}

// consume — горутина-потребитель наблюдений.
func (r *Recorder) consume() {
	defer r.wg.Done()
	for req := range r.ch {
		r.record(req)
	}
}

// drained возвращает число наблюдений, дошедших до потребителя (только для
// тестов и диагностики) — см. поле drainedCount.
func (r *Recorder) drained() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drainedCount
}

// record обрабатывает одно наблюдение: извлекает path/size/format,
// обновляет state и планирует запись.
func (r *Recorder) record(req *asset.Request) {
	if !req.IsPreset() {
		return
	}
	size := string(req.SegmentName())
	format := string(req.OutputFormats())
	path := pathPrefix(req.Path())

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.drainedCount++
	// Размер-грамматика ("120x60", "x200", "200x", "x") — custom-наблюдение.
	if _, err := asset.ParseSize(size); err == nil {
		if format == "" {
			return
		}
		if AddObservation(r.state, path, size, format) {
			r.dirty = true
			r.scheduleWriteLocked()
		}
		return
	}
	// Не размер: если имя сегмента — известный пресет из конфигурации,
	// наблюдение пополняет presets path-policy (например /test/my-png/
	// face-fix.png → /test: presets: [face-fix]).
	if _, ok := r.presetNames[size]; ok {
		if AddPresetObservation(r.state, path, size) {
			r.dirty = true
			r.scheduleWriteLocked()
		}
	}
}

// pathPrefix нормализует путь запроса к префиксу path-policy:
// ведущий "/", без хвостового "/" (кроме ровно "/"). req.Path() — это
// УЖЕ путь без source-файла и сегмента (домен asset.Request хранит
// именно каталог исходника), поэтому никаких срезов по "/" не требуется.
// Пустой путь (запрос корневого исходника "/name.ext") не даёт префикса —
// наблюдение игнорируется (path-policy "/" — fallback, не правило).
func pathPrefix(canonicalPath string) string {
	if canonicalPath == "" {
		return ""
	}
	return normalizePrefix(canonicalPath)
}

// scheduleWriteLocked планирует запись с учётом дебаунса: если с последней
// записи прошло >= writeInterval — писать сразу; иначе отложить таймером.
//
// Асинхронная запись делается ТОЛЬКО дебаунс-таймером (никаких фоновых
// горутин). Причина: фоновая горутина, не участвующая в r.wg, обгоняет
// wg.Wait() в Stop — снимает snapshot (dirty=false), ещё не записав файл,
// а финальный flush из Stop видит dirty=false и выходит, оставляя на
// диске усечённое состояние (потеря наблюдения — флейк CI). Вариант
// "участвовать в r.wg" тоже не годится: Stop, ожидая wg, не может
// выставить маркер "после Stop" раньше дренажа, а потребитель,
// дренирующий канал, при активном маркере молча выбрасывает наблюдения
// (тот самый флейк: на диске оставался только learning-mode: false, ни
// одно observation не дошло до state). Таймер синхронизирован через r.mu
// и writeMu: callback либо успевает до финального flush, либо после —
// потеря невозможна ни в каком раскладе (см. Stop).
func (r *Recorder) scheduleWriteLocked() {
	if r.stopping || r.writeTimer != nil {
		return // Stop начался (финальная запись сделает flush) или запись уже запланирована
	}
	elapsed := time.Since(r.lastWrite)
	if elapsed >= writeInterval {
		// С последней записи прошло много времени: писать нужно сразу, но
		// не фоновой горутиной (см. выше) — запланируем flush "немедленно".
		r.writeTimer = time.AfterFunc(0, r.onTimer)
		return
	}
	r.writeTimer = time.AfterFunc(writeInterval-elapsed, r.onTimer)
}

// onTimer — callback дебаунс-таймера: сбрасывает маркер таймера и делает
// запись. Обе операции берут r.mu по отдельности — держать r.mu в flush
// нельзя (flush проверяет finished под r.mu, а r.mu удерживается во время
// отправки в Observe; см. комментарий к Stop о порядке блокировок).
func (r *Recorder) onTimer() {
	r.mu.Lock()
	r.writeTimer = nil
	r.mu.Unlock()
	r.flush()
}

// stopTimerLocked останавливает отложенный таймер (если есть), не ожидая
// его завершения. Вызывается под r.mu: если callback уже стартовал и
// ждёт r.mu, Stop гарантированно не пропустит dirty (callback либо
// успеет снять snapshot до финального flush, либо после — при любом
// раскладе данные попадут на диск, см. комментарий к Stop).
func (r *Recorder) stopTimerLocked() {
	if r.writeTimer != nil {
		r.writeTimer.Stop()
		r.writeTimer = nil
	}
}

// flush выполняет запись state в generate-local.yaml (если есть изменения).
//
// Весь flush (снятие snapshot + запись файла) сериализуется через writeMu:
// конкурентные flush (дебаунс-таймер и Stop) не могут снять snapshot
// одновременно и перезаписать более новый snapshot более старым.
//
// Если Stop уже проставил finished — запись невозможна: опоздавший
// callback таймера (или flush после ResetLearningMode) затёр бы финальное
// состояние файла, где learning-mode уже сброшен в false.
func (r *Recorder) flush() {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.mu.Lock()
	if r.finished {
		// Stop завершён: state зафиксирован на диске, поздние записи
		// (callback таймера, гонка с ResetLearningMode) запрещены.
		r.mu.Unlock()
		return
	}
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	snapshot := NormalizeState(r.state)
	r.dirty = false
	r.lastWrite = time.Now()
	r.mu.Unlock()

	file := filepath.Join(r.dir, localFileName)
	if err := UpdatePathPolicies(file, snapshot); err != nil {
		// Ошибка записи: лог ERROR, состояние в памяти сохраняется,
		// ретрай на следующем наблюдении.
		r.mu.Lock()
		r.dirty = true
		r.mu.Unlock()
		r.logger.Errorf("learning: write %s: %v", file, err)
	}
}
