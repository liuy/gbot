// Package mcp — lru: Generic LRU cache for MCP tool list caching.
//
// Source: TS uses memoizeWithLRU from utils/memoize.js.
// Custom implementation using container/list + map (no external dependency).
package mcp

import (
	"container/list"
	"sync"
)

// entry stores a key-value pair in the list element.
type entry[K comparable, V any] struct {
	key   K
	value V
}

// LRUCache is a thread-safe LRU cache with generic key/value types.
// No onEvict callback — removed in plan v4 as over-engineering (no callers).
type LRUCache[K comparable, V any] struct {
	capacity int
	mu       sync.Mutex
	items    map[K]*list.Element
	order    *list.List // front = most recent, back = least recent
}

// NewLRUCache creates a new LRU cache with the given capacity.
// Capacity 0 means no items are stored (every Put is immediately evicted).
func NewLRUCache[K comparable, V any](capacity int) *LRUCache[K, V] {
	return &LRUCache[K, V]{
		capacity: capacity,
		items:    make(map[K]*list.Element),
		order:    list.New(),
	}
}

// Get retrieves a value by key. Returns the value and true if found,
// or the zero value and false if not found.
// Moves the accessed item to the front (most recently used).
func (c *LRUCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*entry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// Put adds or updates a key-value pair.
// If the cache is at capacity, the least recently used item is evicted.
func (c *LRUCache[K, V]) Put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value.(*entry[K, V]).value = value
		return
	}

	// Capacity 0 means no items stored
	if c.capacity <= 0 {
		return
	}

	// Evict LRU if at capacity
	if c.order.Len() >= c.capacity {
		back := c.order.Back()
		if back != nil {
			ent := c.order.Remove(back).(*entry[K, V])
			delete(c.items, ent.key)
		}
	}

	// Add new entry
	ent := &entry[K, V]{key: key, value: value}
	elem := c.order.PushFront(ent)
	c.items[key] = elem
}

// Delete removes a key from the cache.
func (c *LRUCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.Remove(elem)
		delete(c.items, key)
	}
}

// Len returns the number of items in the cache.
func (c *LRUCache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
