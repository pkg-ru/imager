package observability

import (
	"sync"
	"testing"
)

func TestTopPathsBasic(t *testing.T) {
	tp := NewTopPaths(10)
	tp.Inc("a")
	tp.Inc("a")
	tp.Inc("b")

	if got := tp.Total(); got != 3 {
		t.Errorf("Total() = %d, want 3", got)
	}
	top := tp.Top(10)
	if len(top) != 2 {
		t.Fatalf("Top(10) len = %d, want 2", len(top))
	}
	if top[0].Path != "a" || top[0].Count != 2 {
		t.Errorf("top[0] = %+v, want a:2", top[0])
	}
	if top[1].Path != "b" || top[1].Count != 1 {
		t.Errorf("top[1] = %+v, want b:1", top[1])
	}
}

func TestTopPathsBounded(t *testing.T) {
	tp := NewTopPaths(3)
	for i := 0; i < 10; i++ {
		tp.Inc("path" + string(rune('a'+i)))
	}
	// После заполнения новые пути вытесняют LRU, но bounded: не более 3.
	if got := len(tp.Top(100)); got > 3 {
		t.Errorf("Top(100) len = %d, want <= 3", got)
	}
}

func TestTopPathsLimit(t *testing.T) {
	tp := NewTopPaths(10)
	tp.Inc("a")
	tp.Inc("b")
	tp.Inc("c")
	top := tp.Top(2)
	if len(top) != 2 {
		t.Fatalf("Top(2) len = %d, want 2", len(top))
	}
}

func TestTopPathsNil(t *testing.T) {
	var tp *TopPaths
	tp.Inc("a") // не паникует
	if got := tp.Total(); got != 0 {
		t.Errorf("nil Total() = %d, want 0", got)
	}
	if got := tp.Top(5); got != nil {
		t.Errorf("nil Top(5) = %v, want nil", got)
	}
}

func TestTopPathsConcurrent(t *testing.T) {
	tp := NewTopPaths(100)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				tp.Inc("shared")
				tp.Inc("other")
			}
		}()
	}
	wg.Wait()
	if got := tp.Total(); got != 8*2000 {
		t.Errorf("Total() = %d, want %d", got, 8*2000)
	}
	top := tp.Top(10)
	if len(top) == 0 {
		t.Fatal("Top(10) empty")
	}
}

func TestTopPathsSnapshot(t *testing.T) {
	tp := NewTopPaths(10)
	tp.Inc("x")
	snap := tp.Snapshot(5)
	if len(snap) != 1 || snap[0].Path != "x" {
		t.Errorf("Snapshot = %+v, want [x:1]", snap)
	}
}
