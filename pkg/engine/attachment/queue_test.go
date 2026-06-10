package attachment

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestQueue_EnqueueAndDrainAll(t *testing.T) {
	var q Queue
	q.Enqueue(types.QueuedItem{Value: "a", Priority: types.PriorityLater})
	q.Enqueue(types.QueuedItem{Value: "b", Priority: types.PriorityNext})
	q.Enqueue(types.QueuedItem{Value: "c", Priority: types.PriorityNow})
	items := q.DrainAll()
	if len(items) != 3 {
		t.Fatalf("DrainAll returned %d items, want 3", len(items))
	}
	if items[0].Value != "a" {
		t.Errorf("items[0].Value = %q, want %q", items[0].Value, "a")
	}
}

func TestQueue_DrainByPriority_Order(t *testing.T) {
	var q Queue
	q.Enqueue(types.QueuedItem{Value: "later", Priority: types.PriorityLater})
	q.Enqueue(types.QueuedItem{Value: "next", Priority: types.PriorityNext})
	q.Enqueue(types.QueuedItem{Value: "now", Priority: types.PriorityNow})
	items := q.DrainByPriority(types.PriorityNext)
	if len(items) != 2 {
		t.Fatalf("DrainByPriority(Next) returned %d items, want 2", len(items))
	}
	values := map[string]bool{}
	for _, item := range items {
		values[item.Value] = true
	}
	if !values["now"] {
		t.Error("expected 'now' in drained items")
	}
	if !values["next"] {
		t.Error("expected 'next' in drained items")
	}
	if values["later"] {
		t.Error("'later' should not be drained by DrainByPriority(Next)")
	}
	remaining := q.DrainAll()
	if len(remaining) != 1 || remaining[0].Value != "later" {
		t.Errorf("DrainAll after DrainByPriority = %v, want [later]", remaining)
	}
}

func TestQueue_DrainEmpty(t *testing.T) {
	var q Queue
	if items := q.DrainAll(); len(items) != 0 {
		t.Errorf("DrainAll on empty = %d items, want 0", len(items))
	}
	if items := q.DrainByPriority(types.PriorityNow); len(items) != 0 {
		t.Errorf("DrainByPriority on empty = %d items, want 0", len(items))
	}
}

func TestQueue_UnknownPriority(t *testing.T) {
	q := &Queue{}
	q.Enqueue(types.QueuedItem{Value: "unknown", Priority: types.QueuePriority("unknown")})
	q.Enqueue(types.QueuedItem{Value: "next", Priority: types.PriorityNext})
	q.Enqueue(types.QueuedItem{Value: "later", Priority: types.PriorityLater})
	items := q.DrainByPriority(types.PriorityNext)
	if len(items) != 2 {
		t.Fatalf("expected 2 items (unknown + next), got %d", len(items))
	}
	if items[0].Value != "next" && items[1].Value != "unknown" && items[0].Value != "unknown" && items[1].Value != "next" {
		t.Errorf("unexpected order: %q, %q", items[0].Value, items[1].Value)
	}
	items2 := q.DrainAll()
	if len(items2) != 1 || items2[0].Value != "later" {
		t.Errorf("expected later to remain, got %v", items2)
	}
}
