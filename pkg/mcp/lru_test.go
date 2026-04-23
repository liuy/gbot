package mcp

import (
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Basic Get/Put
// ---------------------------------------------------------------------------

func TestLRUCache_GetPut(t *testing.T) {
	c := NewLRUCache[string, int](3)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)

	val, ok := c.Get("a")
	if !ok || val != 1 {
		t.Errorf("Get(a) = (%d, %v), want (1, true)", val, ok)
	}

	val, ok = c.Get("b")
	if !ok || val != 2 {
		t.Errorf("Get(b) = (%d, %v), want (2, true)", val, ok)
	}

	_, ok = c.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) should return false")
	}
}

// ---------------------------------------------------------------------------
// Eviction
// ---------------------------------------------------------------------------

func TestLRUCache_Eviction(t *testing.T) {
	c := NewLRUCache[string, int](2)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3) // evicts "a" (LRU)

	_, ok := c.Get("a")
	if ok {
		t.Error("expected 'a' to be evicted")
	}

	val, ok := c.Get("b")
	if !ok || val != 2 {
		t.Errorf("Get(b) = (%d, %v), want (2, true)", val, ok)
	}

	val, ok = c.Get("c")
	if !ok || val != 3 {
		t.Errorf("Get(c) = (%d, %v), want (3, true)", val, ok)
	}
}

func TestLRUCache_EvictionOrder_AccessRefreshes(t *testing.T) {
	c := NewLRUCache[string, int](2)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a") // access "a" → moves to front, "b" is now LRU
	c.Put("c", 3) // evicts "b"

	_, ok := c.Get("b")
	if ok {
		t.Error("expected 'b' to be evicted (was LRU after Get(a))")
	}

	val, ok := c.Get("a")
	if !ok || val != 1 {
		t.Errorf("Get(a) = (%d, %v), want (1, true)", val, ok)
	}
}

// ---------------------------------------------------------------------------
// Update existing key
// ---------------------------------------------------------------------------

func TestLRUCache_Update(t *testing.T) {
	c := NewLRUCache[string, int](3)

	c.Put("a", 1)
	c.Put("a", 10)

	val, ok := c.Get("a")
	if !ok || val != 10 {
		t.Errorf("Get(a) = (%d, %v), want (10, true)", val, ok)
	}

	if c.Len() != 1 {
		t.Errorf("Len() = %d, want 1", c.Len())
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestLRUCache_Delete(t *testing.T) {
	c := NewLRUCache[string, int](3)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Delete("a")

	_, ok := c.Get("a")
	if ok {
		t.Error("expected 'a' to be deleted")
	}

	if c.Len() != 1 {
		t.Errorf("Len() = %d, want 1", c.Len())
	}

	// Delete nonexistent key — no panic
	c.Delete("nonexistent")
}

// ---------------------------------------------------------------------------
// Len
// ---------------------------------------------------------------------------

func TestLRUCache_Len(t *testing.T) {
	c := NewLRUCache[string, int](5)
	if c.Len() != 0 {
		t.Errorf("Len() = %d, want 0", c.Len())
	}

	c.Put("a", 1)
	if c.Len() != 1 {
		t.Errorf("Len() = %d, want 1", c.Len())
	}

	c.Put("b", 2)
	c.Put("c", 3)
	if c.Len() != 3 {
		t.Errorf("Len() = %d, want 3", c.Len())
	}
}

// ---------------------------------------------------------------------------
// Capacity 0
// ---------------------------------------------------------------------------

func TestLRUCache_CapacityZero(t *testing.T) {
	c := NewLRUCache[string, int](0)

	c.Put("a", 1)
	_, ok := c.Get("a")
	if ok {
		t.Error("capacity 0: items should not be stored")
	}
}

// ---------------------------------------------------------------------------
// Capacity 1
// ---------------------------------------------------------------------------

func TestLRUCache_CapacityOne(t *testing.T) {
	c := NewLRUCache[string, int](1)

	c.Put("a", 1)
	c.Put("b", 2) // evicts "a"

	_, ok := c.Get("a")
	if ok {
		t.Error("expected 'a' to be evicted")
	}

	val, ok := c.Get("b")
	if !ok || val != 2 {
		t.Errorf("Get(b) = (%d, %v), want (2, true)", val, ok)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access with -race
// ---------------------------------------------------------------------------

func TestLRUCache_Concurrent(t *testing.T) {
	c := NewLRUCache[int, int](100)
	var wg sync.WaitGroup

	// Concurrent writers
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.Put(n, n*10)
		}(i)
	}

	// Concurrent readers
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.Get(n)
		}(i)
	}

	wg.Wait()

	// Verify at least some items survived
	if c.Len() == 0 {
		t.Error("expected some items in cache after concurrent access")
	}
}

// ---------------------------------------------------------------------------
// Generic type support (int keys)
// ---------------------------------------------------------------------------

func TestLRUCache_IntKeys(t *testing.T) {
	c := NewLRUCache[int, string](3)
	c.Put(1, "one")
	c.Put(2, "two")

	val, ok := c.Get(1)
	if !ok || val != "one" {
		t.Errorf("Get(1) = (%q, %v), want (one, true)", val, ok)
	}
}
