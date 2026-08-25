package lru

import "testing"

func TestLRUBasic(t *testing.T) {
	c := New[string, string](2)
	c.Set("a", "1")
	c.Set("b", "2")
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("Get(a) = %q, %v", v, ok)
	}
	// Вытесняется b (a только что использован).
	c.Set("c", "3")
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected b evicted")
	}
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("Get(a) = %q, %v", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != "3" {
		t.Fatalf("Get(c) = %q, %v", v, ok)
	}
}

func TestLRUUpdateExisting(t *testing.T) {
	c := New[int, int](2)
	c.Set(1, 10)
	c.Set(1, 11)
	if v, ok := c.Get(1); !ok || v != 11 {
		t.Fatalf("Get(1) = %d, %v", v, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
}

func TestLRUDelete(t *testing.T) {
	c := New[string, string](4)
	c.Set("k", "v")
	c.Delete("k")
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected deleted key missing")
	}
	// Повторный Delete безопасен.
	c.Delete("k")
}

func TestLRUEvictionOrder(t *testing.T) {
	c := New[string, int](3)
	for i, k := range []string{"x", "y", "z"} {
		c.Set(k, i)
	}
	// Touch x, затем добавляем w — вытесниться должен y.
	if _, ok := c.Get("x"); !ok {
		t.Fatal("missing x")
	}
	c.Set("w", 4)
	if _, ok := c.Get("y"); ok {
		t.Fatal("expected y evicted")
	}
	for _, k := range []string{"x", "z", "w"} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("missing %s", k)
		}
	}
}
