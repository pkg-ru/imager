package observability

import (
	"container/list"
	"sort"
	"sync"
	"sync/atomic"
)

// BadPathStat — статистика по одному проблемному пути.
type BadPathStat struct {
	Path  string
	Count uint64
}

// TopPaths — bounded реестр счётчиков проблемных путей с вытеснением LRU.
//
// Потокобезопасен. При превышении max-entries наименее недавно использованный
// путь вытесняется (LRU), поэтому кардинальность ограничена. Используется для
// observability ошибок asset URL (top-paths).
type TopPaths struct {
	mu     sync.Mutex
	counts map[string]*atomic.Uint64
	elems  map[string]*list.Element // key -> элемент списка (для O(1) touch)
	lru    *list.List               // порядок использования (front = LRU)
	max    int
	total  atomic.Uint64
}

// NewTopPaths создаёт bounded реестр с лимитом max записей.
func NewTopPaths(max int) *TopPaths {
	if max <= 0 {
		max = 1024
	}
	return &TopPaths{
		counts: make(map[string]*atomic.Uint64),
		elems:  make(map[string]*list.Element),
		lru:    list.New(),
		max:    max,
	}
}

// Inc инкрементирует счётчик пути. Если путь ещё не отслеживается и реестр
// заполнен, наименее недавно использованный путь вытесняется.
func (t *TopPaths) Inc(path string) {
	if t == nil || path == "" {
		return
	}
	t.total.Add(1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.counts[path]; ok {
		c.Add(1)
		t.lru.MoveToBack(t.elems[path])
		return
	}
	// Новый путь: если реестр заполнен, вытесняем LRU-запись.
	if len(t.counts) >= t.max {
		if front := t.lru.Front(); front != nil {
			old := front.Value.(string)
			t.lru.Remove(front)
			delete(t.elems, old)
			delete(t.counts, old)
		}
	}
	c := &atomic.Uint64{}
	c.Add(1)
	t.counts[path] = c
	t.elems[path] = t.lru.PushBack(path)
}

// Top возвращает до n путей с наибольшими счётчиками, отсортированных по
// убыванию. При равенстве счётчиков порядок не гарантирован.
func (t *TopPaths) Top(n int) []BadPathStat {
	if t == nil || n <= 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	stats := make([]BadPathStat, 0, len(t.counts))
	for p, c := range t.counts {
		stats = append(stats, BadPathStat{Path: p, Count: c.Load()})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Count != stats[j].Count {
			return stats[i].Count > stats[j].Count
		}
		return stats[i].Path < stats[j].Path
	})
	if len(stats) > n {
		stats = stats[:n]
	}
	return stats
}

// Total возвращает суммарное число инкрементов.
func (t *TopPaths) Total() uint64 {
	if t == nil {
		return 0
	}
	return t.total.Load()
}
