// Package lru реализует обобщённый потокобезопасный bounded LRU-кэш.
//
// Поведение перенесено из etagCache (httpapi/handler.go) и metadataCache
// (storage/s3/s3.go): Get помечает запись как недавно использованную,
// Set при превышении max вытесняет наименее недавно использованную запись.
package lru

import (
	"container/list"
	"sync"
)

// Cache — потокобезопасный LRU-кэш с ограничением по числу записей.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	m     map[K]V
	elems map[K]*list.Element // key -> элемент списка (для O(1) touch)
	lru   *list.List          // для eviction (элементы = ключи)
	max   int
}

// New создаёт кэш с лимитом max записей. Значения max <= 0 заменяются
// значением по умолчанию 4096.
func New[K comparable, V any](max int) *Cache[K, V] {
	if max <= 0 {
		max = 4096
	}
	return &Cache[K, V]{
		m:     make(map[K]V),
		elems: make(map[K]*list.Element),
		lru:   list.New(),
		max:   max,
	}
}

// Get возвращает значение по ключу и помечает его как недавно использованный.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	if ok {
		// Перемещаем ключ в конец списка (недавно использованный).
		if n := c.elems[key]; n != nil {
			c.lru.MoveToBack(n)
		}
	}
	return v, ok
}

// Set сохраняет значение по ключу. При превышении max вытесняет LRU-запись.
func (c *Cache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok {
		c.m[key] = value
		if n := c.elems[key]; n != nil {
			c.lru.MoveToBack(n)
		}
		return
	}
	c.m[key] = value
	c.elems[key] = c.lru.PushBack(key)
	if c.lru.Len() > c.max {
		if front := c.lru.Front(); front != nil {
			old := front.Value.(K)
			c.lru.Remove(front)
			delete(c.elems, old)
			delete(c.m, old)
		}
	}
}

// Delete удаляет запись по ключу, если она существует.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
	if n := c.elems[key]; n != nil {
		c.lru.Remove(n)
		delete(c.elems, key)
	}
}

// Len возвращает текущее число записей.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
