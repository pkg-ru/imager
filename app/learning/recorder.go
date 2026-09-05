package learning

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/policy"
	"gitverse.ru/pkg-ru/imager/observability"
)

// Recorder — сборщик наблюдений learning-mode.
//
// Observe неблокирующий: наблюдения кладутся в buffered channel и
// обрабатываются горутиной-потребителем. Запись в generate-local.yaml
// дебаунсится: не чаще 1 раза в 2 секунды при потоке наблюдений +
// финальная запись в Stop(). Ошибка записи логируется (ERROR), состояние
// в памяти сохраняется, ретрай на следующем наблюдении.
type Recorder struct {
	dir      string // каталог конфигов (файл generate-local.yaml внутри)
	state    map[string]policy.PathPolicyConfig
	logger   observability.Logger
	ch       chan *asset.Request
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	mu         sync.Mutex
	dirty      bool        // есть незаписанные изменения
	lastWrite  time.Time   // время последней записи
	writeTimer *time.Timer // отложенная запись (дебаунс)
	stopped    bool

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
		dir:       d.ConfigDir,
		state:     initialState,
		logger:    logger,
		ch:        make(chan *asset.Request, channelCap),
		done:      make(chan struct{}),
		lastWrite: time.Now(),
		dirty:     len(initialState) > 0,
	}
	r.wg.Add(1)
	go r.consume()
	return r, nil
}

// Observe регистрирует наблюдение (неблокирующе). req == nil игнорируется.
// Из req извлекаются: path-префикс (без сегмента-файла), size
// (req.SegmentName() — только если валиден как размер-грамматика через
// asset.ParseSize; иначе наблюдение игнорируется), format (первый из
// req.OutputFormats()).
func (r *Recorder) Observe(req *asset.Request) {
	if req == nil {
		return
	}
	select {
	case r.ch <- req:
	default:
		// Буфер полон — наблюдение отбрасывается (non-blocking).
	}
}

// Stop останавливает потребителя: drain канала + финальная запись.
func (r *Recorder) Stop() {
	r.stopOnce.Do(func() {
		close(r.ch)
		r.wg.Wait()
		r.mu.Lock()
		r.stopped = true
		r.stopTimerLocked()
		r.mu.Unlock()
		// Финальная запись.
		r.flush()
	})
}

// consume — горутина-потребитель наблюдений.
func (r *Recorder) consume() {
	defer r.wg.Done()
	for req := range r.ch {
		r.record(req)
	}
}

// record обрабатывает одно наблюдение: извлекает path/size/format,
// обновляет state и планирует запись.
func (r *Recorder) record(req *asset.Request) {
	if !req.IsPreset() {
		return
	}
	size := string(req.SegmentName())
	// Только размер-грамматика ("120x60", "x200", "200x", "x"); имена
	// пресетов и @dpr-суффиксы игнорируются.
	if _, err := asset.ParseSize(size); err != nil {
		return
	}
	format := string(req.OutputFormats())
	if format == "" {
		return
	}
	path := pathPrefix(req.Path())

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	if AddObservation(r.state, path, size, format) {
		r.dirty = true
		r.scheduleWriteLocked()
	}
}

// pathPrefix извлекает путь-префикс из канонического пути запроса:
// путь без сегмента-файла. Канонический путь запроса —
// "{path}/{source_name}-{source_format}/{segment}.{out}" (без ведущего
// "/"); префикс — часть до последнего "/" с добавленным ведущим "/"
// (нормализация path-policy). Путь без "/" (только source-файл) даёт
// пустой префикс — наблюдение игнорируется.
func pathPrefix(canonicalPath string) string {
	if canonicalPath == "" {
		return ""
	}
	i := lastIndexByte(canonicalPath, '/')
	if i < 0 {
		return ""
	}
	return "/" + canonicalPath[:i]
}

// lastIndexByte — последний индекс байта c в s или -1.
func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// scheduleWriteLocked планирует запись с учётом дебаунса: если с последней
// записи прошло >= writeInterval — писать сразу; иначе отложить таймером.
func (r *Recorder) scheduleWriteLocked() {
	if r.writeTimer != nil {
		return // запись уже запланирована
	}
	elapsed := time.Since(r.lastWrite)
	if elapsed >= writeInterval {
		go r.flush()
		return
	}
	r.writeTimer = time.AfterFunc(writeInterval-elapsed, func() {
		r.mu.Lock()
		r.writeTimer = nil
		r.mu.Unlock()
		r.flush()
	})
}

// stopTimerLocked останавливает отложенный таймер (если есть).
func (r *Recorder) stopTimerLocked() {
	if r.writeTimer != nil {
		r.writeTimer.Stop()
		r.writeTimer = nil
	}
}

// flush выполняет запись state в generate-local.yaml (если есть изменения).
// Весь flush (снятие snapshot + запись файла) сериализуется через writeMu:
// конкурентные flush (дебаунс-таймер и Stop) не могут снять snapshot
// одновременно и перезаписать более новый snapshot более старым.
func (r *Recorder) flush() {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.mu.Lock()
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
