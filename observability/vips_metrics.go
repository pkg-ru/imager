// Периодический сборщик vips-метрик (Фаза 4): tracked memory/allocs, open
// files, operation cache hits/misses libvips + метрики кэша ватермарок
// (hit/miss/size) из watermarkcache.go Фазы 3.
//
// Архитектура: observability НЕ зависит от govips (пакет собирается без
// cgo). Значения поставляются через VipsStatsProvider — функцию, возвращающую
// снимок метрик; провайдер регистрируется адаптером libvips при создании
// движка. До регистрации провайдера collector публикует нули.
//
// Отказоустойчивость: паника/ошибка провайдера перехватывается recover'ом и
// НЕ влияет на обработку запросов — на очередном тике значения просто не
// обновляются; goroutine-сборщик останавливается через Stop (shutdown).
package observability

import (
	"sync"
	"time"
)

// DefaultVipsMetricsInterval — дефолтный интервал сбора vips-метрик.
const DefaultVipsMetricsInterval = 15 * time.Second

// MinVipsMetricsInterval — минимальный интервал сбора (защита от
// деградации производительности слишком частыми cgo-вызовами).
const MinVipsMetricsInterval = time.Second

// VipsSnapshot — снимок vips-метрик одного тика сбора.
type VipsSnapshot struct {
	// TrackedMemory — память, учитываемая аллокатором libvips (байты).
	TrackedMemory int64
	// TrackedAllocs — число активных аллокаций libvips.
	TrackedAllocs int64
	// OpenFiles — число открытых libvips файлов.
	OpenFiles int64
	// MemHighwater — пик tracked memory за время жизни процесса (байты).
	MemHighwater int64
	// OperationsTotal — суммарное число операций libvips, выполненных
	// через govips-биндинг (накопительно; требует CollectStats при Startup).
	// Примечание: отдельных счётчиков operation-cache hit/miss в API
	// govips v2.18.0 НЕТ (ReadRuntimeStats возвращает только счётчики
	// операций по именам), поэтому экспортируется агрегат.
	OperationsTotal int64
	// WatermarkCacheHits / WatermarkCacheMisses — накопительные hit/miss
	// кэша файлов ватермарок (Фаза 3).
	WatermarkCacheHits   int64
	WatermarkCacheMisses int64
	// WatermarkCacheEntries — текущее число записей кэша ватермарок.
	WatermarkCacheEntries int64
	// WatermarkCacheBytes — суммарный размер закэшированных байтов.
	WatermarkCacheBytes int64
}

// VipsStatsProvider возвращает снимок vips-метрик. Вызывается collector'ом
// периодически; реализация обязана быть потокобезопасной. Ошибка/паника
// провайдера игнорируются сборщиком (значения не обновляются).
type VipsStatsProvider func() (VipsSnapshot, error)

// vipsCollector — периодический сборщик vips-метрик в expvar-реестр.
type vipsCollector struct {
	mu       sync.Mutex
	provider VipsStatsProvider
	stop     chan struct{}
	running  bool
}

// Глобальный collector: один на процесс (expvar-реестр глобален).
var (
	vipsCollOnce sync.Once
	vipsColl     *vipsCollector
)

// getVipsCollector создаёт (идемпотентно) и возвращает глобальный collector.
func getVipsCollector() *vipsCollector {
	vipsCollOnce.Do(func() {
		vipsColl = &vipsCollector{}
	})
	return vipsColl
}

// SetVipsStatsProvider регистрирует провайдер vips-метрик и запускает
// периодический сбор с заданным интервалом (интервал < минимума заменяется
// минимумом; interval <= 0 — дефолт). Повторный вызов заменяет провайдер;
// если сборщик был остановлен через StopVipsMetrics — перезапускает его.
// Потокобезопасно.
//
// Экспортируемые gauge-и (Prometheus-текст через MetricsHandler):
//
//	imager_vips_tracked_memory_bytes   — tracked memory libvips
//	imager_vips_tracked_allocs         — число активных аллокаций
//	imager_vips_open_files             — открытые файлы libvips
//	imager_vips_mem_highwater_bytes    — пик tracked memory
//	imager_vips_operations_total       — суммарное число операций libvips
//	imager_vips_watermark_cache_hits_total
//	imager_vips_watermark_cache_misses_total
//	imager_vips_watermark_cache_entries
//	imager_vips_watermark_cache_bytes
func SetVipsStatsProvider(p VipsStatsProvider, interval time.Duration) {
	if p == nil {
		return
	}
	c := getVipsCollector()
	c.mu.Lock()
	c.provider = p
	if !c.running {
		c.running = true
		stop := make(chan struct{})
		c.stop = stop
		if interval <= 0 {
			interval = DefaultVipsMetricsInterval
		}
		if interval < MinVipsMetricsInterval {
			interval = MinVipsMetricsInterval
		}
		go c.run(interval, stop)
	}
	c.mu.Unlock()
}

// StopVipsMetrics останавливает периодический сбор (graceful shutdown).
// Идемпотентен; безопасен до первого SetVipsStatsProvider. Повторный вызов
// SetVipsStatsProvider после Stop перезапускает сборщик.
func StopVipsMetrics() {
	c := getVipsCollector()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return
	}
	close(c.stop)
	c.running = false
}

// run — цикл сбора: немедленный первый тик, затем по таймеру до stop.
// Канал stop передаётся параметром (а не читается из поля структуры),
// чтобы не обращаться к c.stop вне мьютекса: StopVipsMetrics закрывает
// канал под мьютексом, а SetVipsStatsProvider может заменить поле новым
// каналом при перезапуске — чтение поля из горутины было бы гонкой.
func (c *vipsCollector) run(interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		c.collectOnce()
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// collectOnce выполняет один тик: вызывает провайдер под recover и
// обновляет expvar-gauge-и. Любая паника/ошибка провайдера глушится.
func (c *vipsCollector) collectOnce() {
	c.mu.Lock()
	p := c.provider
	c.mu.Unlock()
	if p == nil {
		return
	}
	snap, err := func() (snap VipsSnapshot, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = errVipsProviderPanic{r}
			}
		}()
		return p()
	}()
	if err != nil {
		return // отказоустойчивость: значения просто не обновляются
	}
	setIntGauge("imager_vips_tracked_memory_bytes", snap.TrackedMemory)
	setIntGauge("imager_vips_tracked_allocs", snap.TrackedAllocs)
	setIntGauge("imager_vips_open_files", snap.OpenFiles)
	setIntGauge("imager_vips_mem_highwater_bytes", snap.MemHighwater)
	setIntGauge("imager_vips_operations_total", snap.OperationsTotal)
	setIntGauge("imager_vips_watermark_cache_hits_total", snap.WatermarkCacheHits)
	setIntGauge("imager_vips_watermark_cache_misses_total", snap.WatermarkCacheMisses)
	setIntGauge("imager_vips_watermark_cache_entries", snap.WatermarkCacheEntries)
	setIntGauge("imager_vips_watermark_cache_bytes", snap.WatermarkCacheBytes)
}

// errVipsProviderPanic — внутренняя обёртка паники провайдера.
type errVipsProviderPanic struct{ v any }

func (e errVipsProviderPanic) Error() string { return "vips stats provider panic" }

// setIntGauge обновляет expvar.Int-gauge (идемпотентная публикация через
// getOrNewInt из metrics.go).
func setIntGauge(name string, v int64) {
	getOrNewInt(name).Set(v)
	bumpMetricsVersion()
}
