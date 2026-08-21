package fs

import (
	"container/list"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// CacheOptions — конфигурация кэша результатов (квота + eviction).
//
// Accounting guarantees (best-effort):
//   - Учёт ведётся в памяти (последний доступ и размер). Таблица
//     восстанавливается при запуске из статистики существующих файлов,
//     где "последний доступ" приближается ModTime файла. При рестарте
//     LRU-порядок теряется.
//   - Внешние изменения файловой системы (ручное удаление/добавление)
//     могут расходиться с таблицей до следующей сверки. Stats() возвращает
//     фактическую статистику из сканирования.
//   - Промежуточные temp-файлы публикации не учитываются в квоте.
type CacheOptions struct {
	// MaxBytes — soft-лимит суммарного размера в байтах; после публикации
	// превышение обрабатывается eviction (0 = без лимита).
	MaxBytes int64
	// MaxObjects — soft-лимит числа объектов; после публикации превышение
	// обрабатывается eviction (0 = без лимита).
	MaxObjects int64
	// QuotaBytes — жёсткий лимит, проверяемый до записи: при превышении
	// Publish возвращает object.ErrQuota. 0 = MaxBytes (если задан).
	QuotaBytes int64
}

func (o CacheOptions) validate() error {
	if o.MaxBytes < 0 || o.MaxObjects < 0 || o.QuotaBytes < 0 {
		return fmt.Errorf("fs: cache options: negative limit")
	}
	return nil
}

// effectiveQuota возвращает жёсткий лимит байт (0 = нет жёсткой квоты).
// Жёсткая квота задаётся только через QuotaBytes и проверяется до записи.
// MaxBytes является soft-лимитом и обрабатывается eviction после публикации,
// поэтому не участвует в предварительной проверке.
func (o CacheOptions) effectiveQuota() int64 {
	return o.QuotaBytes
}

// CacheStats — статистика кэша, доступная из ResultStore.CacheStats.
type CacheStats struct {
	// Objects — число объектов.
	Objects int64
	// TotalBytes — суммарный размер.
	TotalBytes int64
	// Evicted — число удалённых объектов (с момента старта).
	Evicted int64
}

// lruEntry — элемент LRU-списка: ключ и размер записанного объекта.
type lruEntry struct {
	key  string
	size int64
}

// cacheManager — goroutine-безопасный accounting-слой для ResultStore.
//
// onEvict вызывается для физического удаления файла вне блокировки.
//
// Учёт построен на LRU-списке (container/list) с вспомогательной map
// index[key]*list.Element для O(1) доступа. Аккумуляторы totalBytes и
// totalObjects поддерживаются инкрементально, поэтому currentLocked — O(1).
type cacheManager struct {
	mu sync.RWMutex

	opts CacheOptions
	// list — LRU-порядок: front = самый свежий, back = самый старый.
	list *list.List
	// index — O(1) доступ к элементам списка по ключу.
	index map[string]*list.Element

	// totalBytes / totalObjects — аккумуляторы суммарного размера и числа
	// объектов (инкрементально поддерживаются в recordPublish/restore/remove).
	totalBytes   int64
	totalObjects int64

	// inFlightBytes — зарезервированные байты текущих публикаций (для
	// честного учёта конкурентных publish).
	inFlightBytes int64
	// evicted — счётчик удалённых объектов (статистика).
	evicted int64

	onEvict func(key object.ObjectKey) error
}

func newCacheManager(opts CacheOptions, onEvict func(key object.ObjectKey) error) (*cacheManager, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	return &cacheManager{
		opts:    opts,
		list:    list.New(),
		index:   make(map[string]*list.Element),
		onEvict: onEvict,
	}, nil
}

// storedFile — запись существующего файла кэша для warm-заполнения.
type storedFile struct {
	key     object.ObjectKey
	size    int64
	modTime time.Time
}

// restore заполняет таблицу из существующих файлов (warm start).
func (c *cacheManager) restore(files []storedFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range files {
		k := string(f.key)
		if el, ok := c.index[k]; ok {
			// Дубликат: обновляем размер, не меняя число объектов.
			c.totalBytes += f.size - el.Value.(*lruEntry).size
			el.Value.(*lruEntry).size = f.size
			c.list.MoveToFront(el)
			continue
		}
		el := c.list.PushFront(&lruEntry{key: k, size: f.size})
		c.index[k] = el
		c.totalBytes += f.size
		c.totalObjects++
	}
}

// reserveBytes резервирует n байт для текущей публикации. Проверяет жёсткую
// квоту QuotaBytes с учётом уже зарезервированных конкурентами байт.
// После завершения публикации вызывается releaseBytes (обязательно через
// defer) или commit (для успешного publish).
func (c *cacheManager) reserveBytes(n int64) error {
	q := c.opts.effectiveQuota()
	if n < 0 || q == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, _ := c.currentLocked()
	if cur+c.inFlightBytes+n > q {
		return &object.QuotaError{
			Reason:  "hard quota exceeded before write",
			Limit:   q,
			Current: cur + c.inFlightBytes + n,
		}
	}
	c.inFlightBytes += n
	return nil
}

// releaseBytes снимает резервирование (вызывается либо после commit через
// commitPublish, либо при ошибке через defer).
func (c *cacheManager) releaseBytes(n int64) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.inFlightBytes -= n
	if c.inFlightBytes < 0 {
		c.inFlightBytes = 0
	}
	c.mu.Unlock()
}

// recordPublish фиксирует успешную публикацию ключа с размером и снимает
// резервирование (releaseBytes вызвать до record — см. Publish).
func (c *cacheManager) recordPublish(key object.ObjectKey, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := string(key)
	if el, ok := c.index[k]; ok {
		// Перезапись существующего объекта: корректируем аккумулятор размера.
		c.totalBytes += size - el.Value.(*lruEntry).size
		el.Value.(*lruEntry).size = size
		c.list.MoveToFront(el)
		return
	}
	el := c.list.PushFront(&lruEntry{key: k, size: size})
	c.index[k] = el
	c.totalBytes += size
	c.totalObjects++
}

// touch обновляет последний доступ (LRU) при успешном Lookup/Open.
// Обновляет LRU-порядок только если ключ существует.
func (c *cacheManager) touch(key object.ObjectKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[string(key)]; ok {
		c.list.MoveToFront(el)
	}
}

// remove удаляет запись при Delete.
func (c *cacheManager) remove(key object.ObjectKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := string(key)
	el, ok := c.index[k]
	if !ok {
		return
	}
	c.list.Remove(el)
	delete(c.index, k)
	c.totalBytes -= el.Value.(*lruEntry).size
	c.totalObjects--
}

// currentLocked возвращает суммарный размер и число объектов по таблице
// (должен вызываться при взятом мьютексе). O(1): возвращает аккумуляторы.
func (c *cacheManager) currentLocked() (bytes, objects int64) {
	return c.totalBytes, c.totalObjects
}

// evictIfNeeded запускает eviction после публикации, если превышены soft
// лимиты MaxBytes/MaxObjects. Возвращает число удалённых. Вызывает onEvict
// вне блокировки.
func (c *cacheManager) evictIfNeeded() (int64, error) {
	c.mu.Lock()
	bytes, objects := c.currentLocked()
	overBytes := c.opts.MaxBytes > 0 && bytes > c.opts.MaxBytes
	overObjects := c.opts.MaxObjects > 0 && objects > c.opts.MaxObjects
	if !overBytes && !overObjects {
		c.mu.Unlock()
		return 0, nil
	}
	// Собираем кандидатов с хвоста (самые старые), пока превышены лимиты.
	// Пересчитываем лимиты с учётом удаляемых кандидатов, чтобы не удалять
	// лишнего.
	var cands []string
	for e := c.list.Back(); e != nil && (overBytes || overObjects); e = e.Prev() {
		ent := e.Value.(*lruEntry)
		cands = append(cands, ent.key)
		bytes -= ent.size
		objects--
		overBytes = c.opts.MaxBytes > 0 && bytes > c.opts.MaxBytes
		overObjects = c.opts.MaxObjects > 0 && objects > c.opts.MaxObjects
	}
	c.mu.Unlock()

	var evicted int64
	for _, k := range cands {
		if c.onEvict != nil {
			if err := c.onEvict(object.ObjectKey(k)); err != nil && !os.IsNotExist(err) {
				return evicted, err
			}
		}
		c.remove(object.ObjectKey(k))
		evicted++
	}
	c.mu.Lock()
	c.evicted += evicted
	c.mu.Unlock()
	return evicted, nil
}

// evictedCount возвращает число evictions (для статистики).
func (c *cacheManager) evictedCount() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.evicted
}

// snapshot возвращает in-memory снимок учёта кэша: суммарный размер и число
// объектов по таблице. O(1): возвращает аккумуляторы под RLock. Используется
// для Stats/CacheStats вместо дорогого filepath.Walk по root.
func (c *cacheManager) snapshot() (bytes, objects int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalBytes, c.totalObjects
}
