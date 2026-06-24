package wechat

import (
	"sync"
	"time"
)

// dedupSet provides TTL-based message deduplication.
// Cleanup is lazy — expired entries are pruned on subsequent Add calls.
type dedupSet struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]time.Time
}

func newDedupSet(ttlSeconds int) *dedupSet {
	return &dedupSet{
		ttl:   time.Duration(ttlSeconds) * time.Second,
		items: make(map[string]time.Time),
	}
}

// Add returns false if the key is already present (duplicate).
func (d *dedupSet) Add(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	d.pruneLocked(now)

	if _, exists := d.items[key]; exists {
		return false
	}
	d.items[key] = now
	return true
}

// pruneLocked removes expired entries. Must hold d.mu.
func (d *dedupSet) pruneLocked(now time.Time) {
	cutoff := now.Add(-d.ttl)
	for k, v := range d.items {
		if v.Before(cutoff) {
			delete(d.items, k)
		}
	}
}
