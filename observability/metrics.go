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
	"sync/atomic"
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

// AssetErrorKind — bounded-cardinality категория ошибки asset URL.
type AssetErrorKind string

// Категории ошибок asset URL (фиксированный enum — bounded cardinality).
const (
	AssetErrParse          AssetErrorKind = "parse"
	AssetErrPresetNotFound AssetErrorKind = "preset_not_found"
	AssetErrInvalidPlan    AssetErrorKind = "invalid_plan"
	AssetErrPolicyDenied   AssetErrorKind = "policy_denied"
)

// Metrics — узкий порт observability для pipeline/cache/processor/storage
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
	// IncDetectionDegraded инкрементирует счётчик деградаций AI-детекции:
	// запрос с fc/oc продолжил обработку с fallback-фокусировкой (center-crop)
	// из-за перегрузки detection-семафора, вместо ошибки 503.
	IncDetectionDegraded()
	// IncVipsOrphanOp инкрементирует счётчик «сиротских» cgo-операций libvips:
	// операция, запущенная через watchdog, завершилась ПОСЛЕ отмены
	// watch-контекста (клиент уже получил ошибку, а горутина-сирота доживает
	// в libvips и её результат игнорируется).
	IncVipsOrphanOp()
	// SetVipsOrphanInflight обновляет текущее число активных сиротских
	// cgo-операций (gauge): инкремент при старте сироты, декремент при её
	// завершении.
	SetVipsOrphanInflight(v int64)
	// IncPanics инкрементирует счётчик паник в HTTP-обработке
	// (imager_panics_total). Вызывается Recover-middleware при перехвате
	// паники нижележащего handler.
	IncPanics()

	// Storage.
	IncStorageOp(op StorageOp, err bool)
	ObserveStorageDuration(op StorageOp, err bool, d time.Duration)

	// Asset errors (observability ошибок asset URL).
	IncAssetError(kind AssetErrorKind)
}

// PublishQueueMetrics — ОПЦИОНАЛЬНЫЙ порт метрик асинхронной публикации.
// Не входит в базовый интерфейс Metrics, чтобы не ломать сторонние
// реализации; application-слой использует type-assert и no-op при отсутствии.
type PublishQueueMetrics interface {
	// SetPublishQueueDepth обновляет текущую глубину bounded-очереди
	// асинхронной публикации (gauge).
	SetPublishQueueDepth(v int64)
	// IncPublishError инкрементирует счётчик ошибок асинхронной публикации
	// (после исчерпания retry/backoff).
	IncPublishError()
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
func (nopMetrics) IncDetectionDegraded()                                 {}
func (nopMetrics) IncVipsOrphanOp()                                      {}
func (nopMetrics) SetVipsOrphanInflight(int64)                           {}
func (nopMetrics) IncPanics()                                            {}
func (nopMetrics) IncStorageOp(StorageOp, bool)                          {}
func (nopMetrics) ObserveStorageDuration(StorageOp, bool, time.Duration) {}
func (nopMetrics) IncAssetError(AssetErrorKind)                          {}

// nopMetrics реализует опциональный порт PublishQueueMetrics (no-op).
func (nopMetrics) SetPublishQueueDepth(int64) {}
func (nopMetrics) IncPublishError()           {}

// NopMetrics возвращает no-op реализацию Metrics.
func NopMetrics() Metrics { return nopMetrics{} }

// histogram — простая гистограмма с фиксированными границами бакетов.
//
// Реализация lock-free: счётчики бакетов, сумма и число наблюдений хранятся
// в atomic-переменных, без мьютекса в горячем пути (observe).
// Агрегация (String/вывод) читает атомарно.
//
// Доп.: сумма хранится в наносекундах (int64), чтобы не терять точность
// долей секунды (d.Seconds() обрезала бы до целых секунд).
type histogram struct {
	buckets []float64 // верхние границы бакетов (в секундах)
	counts  []atomic.Uint64
	sumNS   atomic.Int64
	count   atomic.Uint64
}

func newHistogram(buckets []float64) *histogram {
	return &histogram{buckets: buckets, counts: make([]atomic.Uint64, len(buckets)+1)}
}

func (h *histogram) observe(d time.Duration) {
	ns := d.Nanoseconds()
	h.sumNS.Add(ns)
	h.count.Add(1)
	sec := float64(ns) / 1e9
	idx := len(h.buckets)
	for i, b := range h.buckets {
		if sec <= b {
			idx = i
			break
		}
	}
	h.counts[idx].Add(1)
	bumpMetricsVersion()
}

// StdMetrics — production реализация Metrics на stdlib expvar.
//
// Все счётчики и гистограммы хранятся в глобальном expvar-реестре и
// агрегируются в текстовом /metrics endpoint (Prometheus exposition format).
// Отдельный /debug/vars endpoint сервисом не регистрируется. Кардинальность
// ограничена фиксированными enum-ами.
type StdMetrics struct {
	requests    *expvar.Map // class -> counter
	requestDur  *histogram
	cacheHit    *expvar.Int
	cacheMiss   *expvar.Int
	procSuccess *expvar.Int
	procError   *expvar.Int
	procDur     *histogram
	// detectionDegraded — накопительный счётчик деградаций AI-детекции
	// (перегрузка detection-семафора → fallback center-crop).
	detectionDegraded *expvar.Int
	// vipsOrphanOps — накопительный счётчик «сиротских» cgo-операций libvips
	// (завершившихся после отмены watch-контекста).
	vipsOrphanOps *expvar.Int
	// vipsOrphanInflight — gauge текущего числа активных сиротских операций.
	vipsOrphanInflight *expvar.Int
	// panics — накопительный счётчик паник в HTTP-обработке
	// (imager_panics_total).
	panics      *expvar.Int
	storageOps  *expvar.Map // op -> success|error counters
	assetErrors *expvar.Map // kind -> counter (ошибки asset URL)
	// storageDur — bounded registry гистограмм длительности storage ops.
	// Ключ — фиксированный набор "op_success"/"op_error". sync.Map
	// даёт lock-free чтение (LoadOrStore) без глобального мьютекса.
	storageDur sync.Map // string -> *histogram

	// Gauges: текущие значения, а не накопительные счётчики.
	httpInflight     *expvar.Int // число обрабатываемых HTTP-запросов
	singleflightKeys *expvar.Int // число ключей в singleflight
	bufferPoolBytes  *expvar.Int // занятый бюджет buffer pool (байты)
	cacheEvictions   *expvar.Int // всего eviction-файлов из кэша
	cacheEntries     *expvar.Int // число записей в кэше
	publishQueue     *expvar.Int // глубина очереди асинхронной публикации
	publishErrors    *expvar.Int // ошибки асинхронной публикации после retry
}

// NewStdMetrics создаёт StdMetrics и регистрирует expvar-переменные.
//
// Регистрация идемпотентна: повторные вызовы NewStdMetrics (например, при
// пересоздании приложения или в нескольких тестах) не паникуют из-за
// дублирующихся имён expvar-переменных, а переиспользуют существующие.
// Это важно, т.к. expvar-реестр глобален на процесс.
func NewStdMetrics() *StdMetrics {
	m := &StdMetrics{
		requests:           getOrNewMap("imager_requests"),
		requestDur:         getOrNewHistogram("imager_request_duration_seconds"),
		cacheHit:           getOrNewInt("imager_cache_hits"),
		cacheMiss:          getOrNewInt("imager_cache_misses"),
		procSuccess:        getOrNewInt("imager_processor_success"),
		procError:          getOrNewInt("imager_processor_errors"),
		procDur:            getOrNewHistogram("imager_processor_duration_seconds"),
		detectionDegraded:  getOrNewInt("imager_detection_degraded_total"),
		vipsOrphanOps:      getOrNewInt("imager_vips_orphan_ops_total"),
		vipsOrphanInflight: getOrNewInt("imager_vips_orphan_inflight"),
		panics:             getOrNewInt("imager_panics_total"),
		storageOps:         getOrNewMap("imager_storage_ops"),
		assetErrors:        getOrNewMap("imager_asset_errors"),

		// Gauges публикуются как expvar-переменные.
		httpInflight:     getOrNewInt("imager_http_inflight"),
		singleflightKeys: getOrNewInt("imager_singleflight_keys"),
		bufferPoolBytes:  getOrNewInt("imager_buffer_pool_bytes"),
		cacheEvictions:   getOrNewInt("imager_cache_evictions_total"),
		cacheEntries:     getOrNewInt("imager_cache_entries"),
		publishQueue:     getOrNewInt("imager_publish_queue_depth"),
		publishErrors:    getOrNewInt("imager_publish_errors_total"),
	}
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

// metricsInitMu защищает создание expvar-переменных (getOrNewInt/getOrNewMap/
// getOrNewHistogram). expvar.NewInt/NewMap публикуют переменную сразу, а
// expvar.Publish паникует при повторной регистрации того же имени: без
// мьютекса параллельные вызовы глобальных функций (например, из горутин
// watchdog libvips) могли бы создать одну переменную дважды → паника
// "Reuse of exported var name". Создание происходит редко (один раз на имя),
// горячий путь (Add/Set/Value) мьютекс не затрагивает.
var metricsInitMu sync.Mutex

// getOrNewInt возвращает существующую expvar.Int-переменную или создаёт новую.
// Потокобезопасно (см. metricsInitMu).
func getOrNewInt(name string) *expvar.Int {
	metricsInitMu.Lock()
	defer metricsInitMu.Unlock()
	if v := expvar.Get(name); v != nil {
		if iv, ok := v.(*expvar.Int); ok {
			return iv
		}
	}
	return expvar.NewInt(name)
}

// getOrNewMap возвращает существующую expvar.Map-переменную или создаёт новую.
// Потокобезопасно (см. metricsInitMu).
func getOrNewMap(name string) *expvar.Map {
	metricsInitMu.Lock()
	defer metricsInitMu.Unlock()
	if v := expvar.Get(name); v != nil {
		if mv, ok := v.(*expvar.Map); ok {
			return mv
		}
	}
	return expvar.NewMap(name)
}

// getOrNewHistogram возвращает существующую expvar-гистограмму или создаёт
// новую и публикует её. Идемпотентно (см. publishOnce). Потокобезопасно
// (см. metricsInitMu).
func getOrNewHistogram(name string) *histogram {
	metricsInitMu.Lock()
	defer metricsInitMu.Unlock()
	if v := expvar.Get(name); v != nil {
		if h, ok := v.(*histogram); ok {
			return h
		}
	}
	h := newHistogram(durationBuckets)
	publishOnce(name, h)
	return h
}

// durationBuckets — фиксированные границы бакетов длительности (секунды).
var durationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

func (m *StdMetrics) IncRequests(class StatusClass) {
	m.requests.Add(string(class), 1)
	bumpMetricsVersion()
}

func (m *StdMetrics) ObserveRequestDuration(class StatusClass, d time.Duration) {
	m.requestDur.observe(d)
}

func (m *StdMetrics) IncCacheHit() {
	m.cacheHit.Add(1)
	bumpMetricsVersion()
}
func (m *StdMetrics) IncCacheMiss() {
	m.cacheMiss.Add(1)
	bumpMetricsVersion()
}

func (m *StdMetrics) IncProcessorSuccess() {
	m.procSuccess.Add(1)
	bumpMetricsVersion()
}
func (m *StdMetrics) IncProcessorError() {
	m.procError.Add(1)
	bumpMetricsVersion()
}
func (m *StdMetrics) ObserveProcessorDuration(d time.Duration) {
	m.procDur.observe(d)
}

// IncDetectionDegraded инкрементирует счётчик деградаций AI-детекции
// (перегрузка detection-семафора → fallback center-crop).
func (m *StdMetrics) IncDetectionDegraded() {
	if m.detectionDegraded != nil {
		m.detectionDegraded.Add(1)
	}
	bumpMetricsVersion()
}

// IncDetectionDegradedGlobal инкрементирует ГЛОБАЛЬНЫЙ счётчик деградаций
// AI-детекции (тот же expvar-счётчик imager_detection_degraded_total, что и
// StdMetrics.IncDetectionDegraded). Используется кодом без доступа к
// экземпляру Metrics (например, libvips-процессором): регистрация
// идемпотентна (getOrNewInt), поэтому вызов безопасен до/после NewStdMetrics.
func IncDetectionDegradedGlobal() {
	getOrNewInt("imager_detection_degraded_total").Add(1)
	bumpMetricsVersion()
}

// IncVipsOrphanOp инкрементирует счётчик «сиротских» cgo-операций libvips
// (завершившихся после отмены watch-контекста).
func (m *StdMetrics) IncVipsOrphanOp() {
	if m.vipsOrphanOps != nil {
		m.vipsOrphanOps.Add(1)
	}
	bumpMetricsVersion()
}

// SetVipsOrphanInflight обновляет текущее число активных сиротских
// cgo-операций (gauge).
func (m *StdMetrics) SetVipsOrphanInflight(v int64) {
	if m.vipsOrphanInflight != nil {
		m.vipsOrphanInflight.Set(v)
	}
}

// IncVipsOrphanOpGlobal инкрементирует ГЛОБАЛЬНЫЙ счётчик сиротских
// cgo-операций libvips (тот же expvar-счётчик imager_vips_orphan_ops_total,
// что и StdMetrics.IncVipsOrphanOp). Используется кодом без доступа к
// экземпляру Metrics (например, libvips-процессором): регистрация
// идемпотентна (getOrNewInt), поэтому вызов безопасен до/после NewStdMetrics.
func IncVipsOrphanOpGlobal() {
	getOrNewInt("imager_vips_orphan_ops_total").Add(1)
	bumpMetricsVersion()
}

// SetVipsOrphanInflightGlobal обновляет ГЛОБАЛЬНЫЙ gauge активных сиротских
// cgo-операций libvips (imager_vips_orphan_inflight). Аналог
// IncVipsOrphanOpGlobal для gauge-значения.
func SetVipsOrphanInflightGlobal(v int64) {
	getOrNewInt("imager_vips_orphan_inflight").Set(v)
	bumpMetricsVersion()
}

// IncPanics инкрементирует счётчик паник в HTTP-обработке
// (imager_panics_total).
func (m *StdMetrics) IncPanics() {
	if m.panics != nil {
		m.panics.Add(1)
	}
	bumpMetricsVersion()
}

// IncPanicsGlobal инкрементирует ГЛОБАЛЬНЫЙ счётчик паник в HTTP-обработке
// (тот же expvar-счётчик imager_panics_total, что и StdMetrics.IncPanics).
// Используется кодом без доступа к экземпляру Metrics (например,
// Recover-middleware): регистрация идемпотентна (getOrNewInt), поэтому вызов
// безопасен до/после NewStdMetrics.
func IncPanicsGlobal() {
	getOrNewInt("imager_panics_total").Add(1)
	bumpMetricsVersion()
}

// VipsOrphanInflightGlobal возвращает текущее значение глобального gauge
// активных сиротских cgo-операций libvips (imager_vips_orphan_inflight).
// Используется watchdog-механизмом для атомарного инкремента/декремента
// (чтение + Set) и для проверки превышения ёмкости семафора.
func VipsOrphanInflightGlobal() int64 {
	return getOrNewInt("imager_vips_orphan_inflight").Value()
}

func (m *StdMetrics) IncStorageOp(op StorageOp, err bool) {
	key := string(op)
	if err {
		key += "_error"
	} else {
		key += "_success"
	}
	m.storageOps.Add(key, 1)
	bumpMetricsVersion()
}

// IncAssetError инкрементирует счётчик ошибок asset URL по категории.
func (m *StdMetrics) IncAssetError(kind AssetErrorKind) {
	if m.assetErrors == nil {
		return
	}
	m.assetErrors.Add(string(kind), 1)
	bumpMetricsVersion()
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
	// sync.Map даёт lock-free чтение (LoadOrStore) без глобального мьютекса.
	// Кардинальность ограничена фиксированным набором op.
	if v, ok := m.storageDur.Load(key); ok {
		return v.(*histogram)
	}
	h := newHistogram(durationBuckets)
	actual, _ := m.storageDur.LoadOrStore(key, h)
	// Публикация expvar-переменной под мьютексом: expvar.Publish паникует
	// при повторной регистрации того же имени (см. metricsInitMu).
	metricsInitMu.Lock()
	publishOnce("imager_storage_duration_seconds_"+key, actual.(*histogram))
	metricsInitMu.Unlock()
	return actual.(*histogram)
}

// Gauges: методы обновления текущих значений. Используются адаптерами
// (middleware, singleflight, buffer pool, cache) для публикации текущего
// состояния, а не накопительных счётчиков.

// SetHttpInflight обновляет число обрабатываемых HTTP-запросов.
func (m *StdMetrics) SetHttpInflight(v int64) {
	if m.httpInflight != nil {
		m.httpInflight.Set(v)
	}
}

// IncHttpInflight атомарно увеличивает число обрабатываемых HTTP-запросов.
// expvar.Int.Add использует atomic.AddInt64 — одна атомарная операция вместо
// неатомарной пары Value()+1/Set() (две операции с гонкой).
func (m *StdMetrics) IncHttpInflight() {
	if m.httpInflight != nil {
		m.httpInflight.Add(1)
	}
}

// DecHttpInflight атомарно уменьшает число обрабатываемых HTTP-запросов.
func (m *StdMetrics) DecHttpInflight() {
	if m.httpInflight != nil {
		m.httpInflight.Add(-1)
	}
}

// SetSingleflightKeys обновляет число ключей в singleflight.
func (m *StdMetrics) SetSingleflightKeys(v int64) {
	if m.singleflightKeys != nil {
		m.singleflightKeys.Set(v)
	}
}

// SetBufferPoolBytes обновляет занятый бюджет buffer pool (байты).
func (m *StdMetrics) SetBufferPoolBytes(v int64) {
	if m.bufferPoolBytes != nil {
		m.bufferPoolBytes.Set(v)
	}
}

// SetCacheEvictions обновляет счётчик eviction-файлов из кэша.
func (m *StdMetrics) SetCacheEvictions(v int64) {
	if m.cacheEvictions != nil {
		m.cacheEvictions.Set(v)
	}
}

// SetCacheEntries обновляет число записей в кэше.
func (m *StdMetrics) SetCacheEntries(v int64) {
	if m.cacheEntries != nil {
		m.cacheEntries.Set(v)
	}
}

// SetPublishQueueDepth обновляет текущую глубину очереди асинхронной
// публикации (gauge). Реализация PublishQueueMetrics.
func (m *StdMetrics) SetPublishQueueDepth(v int64) {
	if m.publishQueue != nil {
		m.publishQueue.Set(v)
	}
}

// IncPublishError инкрементирует счётчик ошибок асинхронной публикации
// (после исчерпания retry/backoff). Реализация PublishQueueMetrics.
func (m *StdMetrics) IncPublishError() {
	if m.publishErrors != nil {
		m.publishErrors.Add(1)
	}
	bumpMetricsVersion()
}

// String реализует expvar.Var для текстового вывода.
func (h *histogram) String() string {
	out := "{"
	for i := range h.counts {
		c := h.counts[i].Load()
		if i < len(h.buckets) {
			out += `"le_` + formatBucket(h.buckets[i]) + `":` + itoa(c) + ","
		} else {
			out += `"+Inf":` + itoa(c) + ","
		}
	}
	out += `"sum":` + formatFloat(float64(h.sumNS.Load())/1e9) + `,"count":` + itoa(h.count.Load()) + "}"
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
