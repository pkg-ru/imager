// Package observability предоставляет лёгкую, bounded-cardinality
// observability-инфраструктуру для production deployment: метрики
// (request/cache/processor/storage stages), структурированное логирование
// (slog-адаптер) и request ID.
//
// Пакет использует только stdlib (expvar, log/slog) и НЕ добавляет тяжёлых
// зависимостей. Метрики имеют ограниченную кардинальность: все label-ы —
// фиксированные enum-ы (status class, storage op), а не произвольные
// пользовательские значения. URL/query/raw user input и секреты не
// логируются и не попадают в метрики.
package observability

import (
	"expvar"
	"strconv"
	"sync"
	"time"
)

// StatusClass — bounded-cardinality класс HTTP-статуса для метрик.
type StatusClass string

// Классы статусов (фиксированный enum — bounded cardinality).
const (
	Status2xx StatusClass = "2xx"
	Status3xx StatusClass = "3xx"
	Status4xx StatusClass = "4xx"
	Status5xx StatusClass = "5xx"
)

// ClassifyStatus маппит HTTP-статус в bounded класс.
func ClassifyStatus(code int) StatusClass {
	switch {
	case code >= 200 && code < 300:
		return Status2xx
	case code >= 300 && code < 400:
		return Status3xx
	case code >= 400 && code < 500:
		return Status4xx
	default:
		return Status5xx
	}
}

// StorageOp — bounded-cardinality операция хранилища.
type StorageOp string

// Операции хранилища (фиксированный enum).
const (
	OpSourceLookup  StorageOp = "source_lookup"
	OpSourceOpen    StorageOp = "source_open"
	OpResultLookup  StorageOp = "result_lookup"
	OpResultOpen    StorageOp = "result_open"
	OpResultPublish StorageOp = "result_publish"
)

// Metrics — узкий порт observability для request/cache/processor/storage
// стадий. Реализации обязаны сохранять bounded cardinality: все label-ы
// являются фиксированными enum-ами, а не произвольными значениями.
type Metrics interface {
	// Request.
	IncRequests(class StatusClass)
	ObserveRequestDuration(class StatusClass, d time.Duration)

	// Cache.
	IncCacheHit()
	IncCacheMiss()

	// Processor.
	IncProcessorSuccess()
	IncProcessorError()
	ObserveProcessorDuration(d time.Duration)

	// Storage.
	IncStorageOp(op StorageOp, err bool)
	ObserveStorageDuration(op StorageOp, err bool, d time.Duration)
}

// nopMetrics — заглушка, используемая при отсутствии метрик.
type nopMetrics struct{}

func (nopMetrics) IncRequests(StatusClass)                               {}
func (nopMetrics) ObserveRequestDuration(StatusClass, time.Duration)     {}
func (nopMetrics) IncCacheHit()                                          {}
func (nopMetrics) IncCacheMiss()                                         {}
func (nopMetrics) IncProcessorSuccess()                                  {}
func (nopMetrics) IncProcessorError()                                    {}
func (nopMetrics) ObserveProcessorDuration(time.Duration)                {}
func (nopMetrics) IncStorageOp(StorageOp, bool)                          {}
func (nopMetrics) ObserveStorageDuration(StorageOp, bool, time.Duration) {}

// NopMetrics возвращает no-op реализацию Metrics.
func NopMetrics() Metrics { return nopMetrics{} }

// histogram — простая гистограмма с фиксированными границами бакетов.
// Реализация потокобезопасна и использует только stdlib.
type histogram struct {
	mu      sync.Mutex
	buckets []float64 // верхние границы бакетов (в секундах)
	counts  []uint64
	sum     float64
	count   uint64
}

func newHistogram(buckets []float64) *histogram {
	return &histogram{buckets: buckets, counts: make([]uint64, len(buckets)+1)}
}

func (h *histogram) observe(d time.Duration) {
	sec := d.Seconds()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += sec
	h.count++
	idx := len(h.buckets)
	for i, b := range h.buckets {
		if sec <= b {
			idx = i
			break
		}
	}
	h.counts[idx]++
}

// StdMetrics — production реализация Metrics на stdlib expvar.
//
// Все счётчики и гистограммы экспортируются через /debug/vars (expvar) и
// агрегируются в текстовом /metrics endpoint. Кардинальность ограничена
// фиксированными enum-ами.
type StdMetrics struct {
	requests        *expvar.Map // class -> counter
	requestDur      *histogram
	cacheHit        *expvar.Int
	cacheMiss       *expvar.Int
	procSuccess     *expvar.Int
	procError       *expvar.Int
	procDur         *histogram
	storageOps      *expvar.Map // op -> success/error counters
	storageDur      *histogram
	storageDurByOp  map[StorageOp]*histogram
	storageDurByErr map[bool]*histogram
}

// NewStdMetrics создаёт StdMetrics и регистрирует expvar-переменные.
//
// Регистрация идемпотентна: повторные вызовы NewStdMetrics (например, при
// пересоздании приложения или в нескольких тестах) не паникуют из-за
// дублирующихся имён expvar-переменных, а переиспользуют существующие.
// Это важно, т.к. expvar-реестр глобален на процесс.
func NewStdMetrics() *StdMetrics {
	m := &StdMetrics{
		requests:    getOrNewMap("imager_requests"),
		requestDur:  newHistogram(durationBuckets),
		cacheHit:    getOrNewInt("imager_cache_hits"),
		cacheMiss:   getOrNewInt("imager_cache_misses"),
		procSuccess: getOrNewInt("imager_processor_success"),
		procError:   getOrNewInt("imager_processor_errors"),
		procDur:     newHistogram(durationBuckets),
		storageOps:  getOrNewMap("imager_storage_ops"),
	}
	publishOnce("imager_request_duration_seconds", m.requestDur)
	publishOnce("imager_processor_duration_seconds", m.procDur)
	return m
}

// publishOnce публикует expvar-переменную, если она ещё не зарегистрирована.
// Идемпотентность важна, т.к. expvar.Publish паникует при повторной
// регистрации того же имени.
func publishOnce(name string, v expvar.Var) {
	if expvar.Get(name) == nil {
		expvar.Publish(name, v)
	}
}

// getOrNewInt возвращает существующую expvar.Int-переменную или создаёт новую.
func getOrNewInt(name string) *expvar.Int {
	if v := expvar.Get(name); v != nil {
		if iv, ok := v.(*expvar.Int); ok {
			return iv
		}
	}
	return expvar.NewInt(name)
}

// getOrNewMap возвращает существующую expvar.Map-переменную или создаёт новую.
func getOrNewMap(name string) *expvar.Map {
	if v := expvar.Get(name); v != nil {
		if mv, ok := v.(*expvar.Map); ok {
			return mv
		}
	}
	return expvar.NewMap(name)
}

// durationBuckets — фиксированные границы бакетов длительности (секунды).
var durationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

func (m *StdMetrics) IncRequests(class StatusClass) {
	m.requests.Add(string(class), 1)
}

func (m *StdMetrics) ObserveRequestDuration(class StatusClass, d time.Duration) {
	m.requestDur.observe(d)
}

func (m *StdMetrics) IncCacheHit()  { m.cacheHit.Add(1) }
func (m *StdMetrics) IncCacheMiss() { m.cacheMiss.Add(1) }

func (m *StdMetrics) IncProcessorSuccess() { m.procSuccess.Add(1) }
func (m *StdMetrics) IncProcessorError()   { m.procError.Add(1) }
func (m *StdMetrics) ObserveProcessorDuration(d time.Duration) {
	m.procDur.observe(d)
}

func (m *StdMetrics) IncStorageOp(op StorageOp, err bool) {
	key := string(op)
	if err {
		key += "_error"
	} else {
		key += "_success"
	}
	m.storageOps.Add(key, 1)
}

func (m *StdMetrics) ObserveStorageDuration(op StorageOp, err bool, d time.Duration) {
	// bounded: агрегируем по op и err в отдельные гистограммы.
	key := string(op)
	if err {
		key += "_error"
	} else {
		key += "_success"
	}
	h := m.storageDurFor(key)
	h.observe(d)
}

func (m *StdMetrics) storageDurFor(key string) *histogram {
	// Ленивая инициализация с bounded cardinality (фиксированный набор op).
	// Используем глобальный registry, чтобы не плодить гистограммы.
	return storageDurRegistry.get(key)
}

// storageDurRegistry — bounded registry гистограмм длительности storage ops.
var storageDurRegistry = &histRegistry{m: map[string]*histogram{}}

type histRegistry struct {
	mu sync.Mutex
	m  map[string]*histogram
}

func (r *histRegistry) get(key string) *histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.m[key]; ok {
		return h
	}
	h := newHistogram(durationBuckets)
	r.m[key] = h
	publishOnce("imager_storage_duration_seconds_"+key, h)
	return h
}

// String реализует expvar.Var для текстового вывода.
func (h *histogram) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := "{"
	for i, c := range h.counts {
		if i < len(h.buckets) {
			out += `"le_` + formatBucket(h.buckets[i]) + `":` + itoa(c) + ","
		} else {
			out += `"+Inf":` + itoa(c) + ","
		}
	}
	out += `"sum":` + formatFloat(h.sum) + `,"count":` + itoa(h.count) + "}"
	return out
}

func formatBucket(v float64) string {
	return formatFloat(v)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func itoa(v uint64) string {
	return strconv.FormatUint(v, 10)
}
