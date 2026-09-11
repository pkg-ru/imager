// Кэш файлов ватермарок: in-memory хранение ИСХОДНЫХ БАЙТОВ файла ватермарки,
// keyed по пути файла (один файл — сотни разных конфигураций наложения;
// параметры применения — позиция/масштаб/repeat — НЕ являются частью ключа).
//
// Почему кэшируются байты, а не декодированный *vips.ImageRef:
//   - govips ImageRef не потокобезопасен и МУТИРУЕТСЯ операциями
//     (ThumbnailWithSize/Replicate меняют изображение in-place), поэтому
//     переиспользование между запросами требует Copy перед каждым использованием
//     и аккуратного владения cgo-ресурсами (риск use-after-close при Shutdown);
//   - декодирование небольшого файла ватермарки дёшево относительно композита,
//     а чтение с диска (главный источник накладных расходов) полностью
//     устраняется кэшем байтов;
//   - память кэша ограничена точно (сумма размеров файлов), тогда как память
//     декодированных пикселей зависит от формата и не поддаётся точному учёту.
//
// Отказоустойчивость: LRU-вытеснение по числу записей И суммарному размеру
// в байтах, TTL записей, инвалидация по mtime/размеру файла (stat выполняется
// на каждый запрос — это дёшево), singleflight на загрузку (параллельные
// запросы одного файла читают диск один раз), fallback на прямое чтение при
// любом промахе — ошибка кэша никогда не ломает запрос.
//
// Файл без build-tag: логика не зависит от govips и тестируется в любой
// сборке (см. watermarkcache_test.go). Интеграция с загрузкой — в
// process_libvips.go (build tag "libvips").
package libvips

import (
	"container/list"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Дефолты кэша ватермарок.
const (
	// DefaultWatermarkCacheMaxFiles — максимум файлов в кэше. Ватермарки
	// задаются администратором и исчисляются единицами, поэтому лимит мал.
	DefaultWatermarkCacheMaxFiles = 32
	// DefaultWatermarkCacheMaxBytes — суммарный бюджет памяти кэша (байт).
	DefaultWatermarkCacheMaxBytes int64 = 64 << 20 // 64 MiB
	// DefaultWatermarkCacheTTL — время жизни записи (инвалидация «на всякий
	// случай»; основная инвалидация — по mtime/размеру).
	DefaultWatermarkCacheTTL = 5 * time.Minute
)

// WatermarkCacheOpts — настройки кэша файлов ватермарок. Заполняется из
// конфигурации (libvips.watermark-cache.*) с fail-fast валидацией; нулевые
// поля отдельных лимитов заменяются дефолтами (см. Normalized).
type WatermarkCacheOpts struct {
	// Enabled — включить кэш. false = каждое использование читает диск
	// (поведение до Фазы 3).
	Enabled bool
	// MaxFiles — максимум записей (файлов) в кэше; вытеснение LRU.
	MaxFiles int
	// MaxBytes — суммарный бюджет памяти кэша в байтах; вытеснение LRU до
	// вписывания в бюджет. Файл больше бюджета не кэшируется (обслуживается
	// напрямую с диска).
	MaxBytes int64
	// TTL — максимальное время жизни записи.
	TTL time.Duration
}

// DefaultWatermarkCacheOpts — настройки по умолчанию (кэш включён).
func DefaultWatermarkCacheOpts() WatermarkCacheOpts {
	return WatermarkCacheOpts{
		Enabled:  true,
		MaxFiles: DefaultWatermarkCacheMaxFiles,
		MaxBytes: DefaultWatermarkCacheMaxBytes,
		TTL:      DefaultWatermarkCacheTTL,
	}
}

// Validate проверяет корректность настроек (fail-fast на старте): отрицательные
// значения запрещены; нулевые лимиты/TTL заменяются дефолтами через Normalized.
func (o WatermarkCacheOpts) Validate() error {
	if o.MaxFiles < 0 {
		return fmt.Errorf("watermark-cache.max-files: negative value %d", o.MaxFiles)
	}
	if o.MaxBytes < 0 {
		return fmt.Errorf("watermark-cache.max-bytes: negative value %d", o.MaxBytes)
	}
	if o.TTL < 0 {
		return fmt.Errorf("watermark-cache.ttl: negative duration %s", o.TTL)
	}
	return nil
}

// Normalized возвращает копию с подстановкой дефолтов вместо нулевых полей.
func (o WatermarkCacheOpts) Normalized() WatermarkCacheOpts {
	d := DefaultWatermarkCacheOpts()
	if o.MaxFiles == 0 {
		o.MaxFiles = d.MaxFiles
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = d.MaxBytes
	}
	if o.TTL == 0 {
		o.TTL = d.TTL
	}
	return o
}

// watermarkEntry — запись кэша: байты файла + снимок stat-информации для
// инвалидации при изменении файла.
type watermarkEntry struct {
	path    string // ключ (хранится в записи для вытеснения из LRU)
	data    []byte
	modTime time.Time
	size    int64
	stored  time.Time // момент записи (для TTL)
}

// watermarkCall — один выполняющийся вызов загрузки (singleflight).
type watermarkCall struct {
	done chan struct{}
	data []byte
	err  error
}

// watermarkCache — потокобезопасный LRU-кэш байтов файлов ватермарок с
// TTL, бюджетом по байтам и singleflight-загрузкой.
type watermarkCache struct {
	mu       sync.Mutex
	entries  map[string]*list.Element // path -> элемент списка (значение = entry)
	lru      *list.List               // LRU-порядок (элементы = пути; front = самый старый)
	inflight map[string]*watermarkCall
	bytes    int64 // суммарный размер закэшированных байтов

	// Метрики: накопительные hit/miss для observability.
	hits   int64
	misses int64

	maxFiles int
	maxBytes int64
	ttl      time.Duration
	now      func() time.Time // инъекция времени для тестов
}

// Ошибки кэша (внутренние; наружу не пробрасываются — при любой проблеме
// вызывающий выполняет fallback на прямое чтение).
var errWatermarkCacheDisabled = errors.New("watermark cache disabled")

// newWatermarkCache создаёт кэш с нормализованными настройками. opts.Enabled
// = false → заглушка без хранения (каждый запрос читает диск).
func newWatermarkCache(opts WatermarkCacheOpts) *watermarkCache {
	c := &watermarkCache{
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
		inflight: make(map[string]*watermarkCall),
		now:      time.Now,
	}
	if opts.Enabled {
		n := opts.Normalized()
		c.maxFiles = n.MaxFiles
		c.maxBytes = n.MaxBytes
		c.ttl = n.TTL
	}
	return c
}

// enabled сообщает, включено ли кэширование.
func (c *watermarkCache) enabled() bool { return c.maxFiles > 0 }

// getOrLoad возвращает содержимое файла: из кэша, если запись свежа (TTL не
// истёк, mtime/размер совпадают с текущим stat), иначе — через singleflight
// загрузку функцией loader. Потокобезопасно; параллельные вызовы с тем же
// путём выполняют loader ровно один раз.
func (c *watermarkCache) getOrLoad(path string, modTime time.Time, size int64, loader func() ([]byte, error)) ([]byte, error) {
	if !c.enabled() {
		return loader()
	}
	now := c.now()

	c.mu.Lock()
	if el, ok := c.entries[path]; ok {
		e := el.Value.(*watermarkEntry)
		fresh := now.Sub(e.stored) < c.ttl &&
			e.modTime.Equal(modTime) && e.size == size
		if fresh {
			c.lru.MoveToBack(el)
			c.hits++
			c.mu.Unlock()
			return e.data, nil
		}
		// Устарела (TTL или изменился файл): вытесняем и перезагружаем.
		c.removeLocked(path, el)
	}
	// Singleflight: присоединяемся к выполняющейся загрузке, если есть.
	if call, ok := c.inflight[path]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.data, call.err
		case <-time.After(c.ttl):
			return nil, errWatermarkCacheDisabled
		}
	}
	call := &watermarkCall{done: make(chan struct{})}
	c.inflight[path] = call
	c.misses++
	c.mu.Unlock()

	// Загрузка вне блокировки; паника в loader не блокирует ключ навсегда.
	func() {
		defer func() {
			if r := recover(); r != nil {
				call.err = fmt.Errorf("watermark load panic: %v", r)
			}
			close(call.done)
			c.mu.Lock()
			delete(c.inflight, path)
			c.mu.Unlock()
		}()
		call.data, call.err = loader()
	}()

	if call.err == nil {
		c.store(path, call.data, modTime, size, now)
	}
	return call.data, call.err
}

// store сохраняет данные в кэше с вытеснением LRU по числу записей и бюджету
// байтов. Файл больше бюджета не кэшируется (обслужен напрямую).
func (c *watermarkCache) store(path string, data []byte, modTime time.Time, size int64, stored time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if int64(len(data)) > c.maxBytes {
		return // слишком большой файл — не кэшируем
	}
	// Защита от двойной вставки (перезагрузка после инвалидации уже удалила
	// старую запись, но проверяем на всякий случай).
	if el, ok := c.entries[path]; ok {
		c.removeLocked(path, el)
	}
	c.entries[path] = c.lru.PushBack(&watermarkEntry{
		path:    path,
		data:    data,
		modTime: modTime,
		size:    size,
		stored:  stored,
	})
	c.bytes += int64(len(data))
	c.evictLocked()
}

// evictLocked вытесняет LRU-записи, пока кэш не впишется в лимиты.
func (c *watermarkCache) evictLocked() {
	for c.lru.Len() > c.maxFiles || c.bytes > c.maxBytes {
		front := c.lru.Front()
		if front == nil {
			return
		}
		e := front.Value.(*watermarkEntry)
		c.removeLocked(e.path, front)
	}
}

// removeLocked удаляет запись по пути и элементу списка.
func (c *watermarkCache) removeLocked(path string, el *list.Element) {
	e := el.Value.(*watermarkEntry)
	c.lru.Remove(el)
	delete(c.entries, path)
	c.bytes -= int64(len(e.data))
}

// len возвращает число записей (для тестов).
func (c *watermarkCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// totalBytes возвращает суммарный размер закэшированных байтов (для тестов).
func (c *watermarkCache) totalBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// stats возвращает снимок метрик кэша для observability: число записей,
// суммарный размер закэшированных байтов, накопительные попадания и промахи.
// Потокобезопасно; не изменяет LRU-порядок.
func (c *watermarkCache) stats() (entries int, bytes int64, hits, misses int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len(), c.bytes, c.hits, c.misses
}
