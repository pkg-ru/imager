// Package lru реализует обобщённый потокобезопасный bounded LRU-кэш:
// Get помечает запись как недавно использованную, Set при превышении max
// вытесняет наименее недавно использованную запись.
package lru

import (
	"container/list"
	"strings"
	"sync"
)

// Cache — потокобезопасный LRU-кэш с ограничением по числу записей.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	items map[K]*item[V] // key -> значение + элемент списка (одна map)
	lru   *list.List     // порядок использования (элементы = ключи)
	max   int
}

// item — запись кэша: значение и позиция в списке порядка.
type item[V any] struct {
	value V
	elem  *list.Element
}

// New создаёт кэш с лимитом max записей. Значения max <= 0 заменяются
// значением по умолчанию 4096.
func New[K comparable, V any](max int) *Cache[K, V] {
	if max <= 0 {
		max = 4096
	}
	return &Cache[K, V]{
		items: make(map[K]*item[V]),
		lru:   list.New(),
		max:   max,
	}
}

// Get возвращает значение по ключу и помечает его как недавно использованный.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	// Перемещаем ключ в конец списка (недавно использованный).
	c.lru.MoveToBack(it.elem)
	return it.value, true
}

// Set сохраняет значение по ключу. При превышении max вытесняет LRU-запись.
func (c *Cache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[key]; ok {
		it.value = value
		c.lru.MoveToBack(it.elem)
		return
	}
	c.items[key] = &item[V]{value: value, elem: c.lru.PushBack(key)}
	if c.lru.Len() > c.max {
		if front := c.lru.Front(); front != nil {
			old := front.Value.(K)
			c.lru.Remove(front)
			delete(c.items, old)
		}
	}
}

// Delete удаляет запись по ключу, если она существует.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[key]; ok {
		c.lru.Remove(it.elem)
		delete(c.items, key)
	}
}

// Len возвращает текущее число записей.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// DeletePrefix удаляет все записи, ключи которых начинаются с prefix.
// Возвращает число удалённых записей. Идемпотентно при отсутствии совпадений.
func (c *Cache[K, V]) DeletePrefix(prefix K) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var removed int
	// Собираем совпавшие ключи отдельно, чтобы безопасно удалять во время
	// итерации по map (удаление во время range по map допустимо в Go, но
	// сбор явного списка упрощает логику и не завязан на эту особенность).
	for k, it := range c.items {
		if c.matchesPrefix(k, prefix) {
			c.lru.Remove(it.elem)
			delete(c.items, k)
			removed++
		}
	}
	return removed
}

// matchesPrefix сообщает, начинается ли key с prefix (только лексическое
// сравнение без границ: ответственность за границу '/' лежит на вызывающем,
// который знает семантику ключей).
func (c *Cache[K, V]) matchesPrefix(key, prefix K) bool {
	k, ok := any(key).(string)
	if !ok {
		return false
	}
	p, ok := any(prefix).(string)
	if !ok {
		return false
	}
	return strings.HasPrefix(k, p)
}
