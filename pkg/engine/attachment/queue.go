// Package attachment manages the attachment queue — a thread-safe FIFO of
// queued items injected as user messages at turn boundaries.
package attachment

import (
	"sync"

	"github.com/liuy/gbot/pkg/types"
)

// Queue is a thread-safe FIFO of queued items to be injected
// as attachments at turn boundaries.
// Source: TS commandQueue with enqueuePendingNotification priority system.
type Queue struct {
	mu    sync.Mutex
	items []types.QueuedItem
}

// Enqueue adds an item to the queue.
func (q *Queue) Enqueue(item types.QueuedItem) {
	q.mu.Lock()
	q.items = append(q.items, item)
	q.mu.Unlock()
}

// RemoveByUUID removes the first item with the given UUID from the queue.
// Returns true if found and removed; false if absent or queue is empty.
// Source: TS messageQueueManager popAllEditable clears the editable subset —
// gbot keys by UUID and (queue is all-editable) so this removes any matched item.
func (q *Queue) RemoveByUUID(uuid string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, item := range q.items {
		if item.UUID == uuid {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true
		}
	}
	return false
}

// Len returns the number of pending items.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// DrainByPriority drains items with priority <= maxPriority.
// TS source: messageQueueManager.ts:525 — getCommandsByMaxPriority
func (q *Queue) DrainByPriority(maxPriority types.QueuePriority) []types.QueuedItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	threshold := priorityOrder(maxPriority)
	var matched, remaining []types.QueuedItem
	for _, item := range q.items {
		itemPri := priorityOrder(item.Priority)
		if item.Priority == "" {
			itemPri = priorityOrder(types.PriorityNext)
		}
		if itemPri <= threshold {
			matched = append(matched, item)
		} else {
			remaining = append(remaining, item)
		}
	}
	q.items = remaining
	return matched
}

// Snapshot returns the current items for the WUI connector to restore queued
// messages on takeover. Does not drain.
func (q *Queue) Snapshot() []types.QueuedItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]types.QueuedItem(nil), q.items...)
}

// DrainAll drains all items. Used for no-tool-use terminal path.
func (q *Queue) DrainAll() []types.QueuedItem {
	q.mu.Lock()
	pending := q.items
	q.items = nil
	q.mu.Unlock()
	return pending
}

func priorityOrder(p types.QueuePriority) int {
	switch p {
	case types.PriorityNow:
		return 0
	case types.PriorityNext:
		return 1
	case types.PriorityLater:
		return 2
	default:
		return 1
	}
}
