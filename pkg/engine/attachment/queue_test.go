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

// -----------------------------------------------------------------------
// RemoveByUUID
// -----------------------------------------------------------------------

func TestQueue_RemoveByUUID_FoundMiddlePreservesOrder(t *testing.T) {
	var q Queue
	q.Enqueue(types.QueuedItem{UUID: "u-1", Value: "a"})
	q.Enqueue(types.QueuedItem{UUID: "u-2", Value: "b"})
	q.Enqueue(types.QueuedItem{UUID: "u-3", Value: "c"})

	if ok := q.RemoveByUUID("u-2"); !ok {
		t.Fatal("RemoveByUUID(u-2) = false, want true (item exists)")
	}
	if got := q.Len(); got != 2 {
		t.Fatalf("Len after remove = %d, want 2", got)
	}
	remaining := q.DrainAll()
	if len(remaining) != 2 {
		t.Fatalf("remaining = %d items, want 2", len(remaining))
	}
	if remaining[0].UUID != "u-1" || remaining[0].Value != "a" {
		t.Errorf("remaining[0] = {%s, %q}, want {u-1, a}", remaining[0].UUID, remaining[0].Value)
	}
	if remaining[1].UUID != "u-3" || remaining[1].Value != "c" {
		t.Errorf("remaining[1] = {%s, %q}, want {u-3, c}", remaining[1].UUID, remaining[1].Value)
	}
}

func TestQueue_RemoveByUUID_NotFoundReturnsFalse(t *testing.T) {
	var q Queue
	q.Enqueue(types.QueuedItem{UUID: "u-1", Value: "a"})

	if ok := q.RemoveByUUID("missing"); ok {
		t.Error("RemoveByUUID(missing) = true, want false")
	}
	if got := q.Len(); got != 1 {
		t.Errorf("Len after not-found remove = %d, want 1 (unchanged)", got)
	}
}

func TestQueue_RemoveByUUID_EmptyUUIDReturnsFalse(t *testing.T) {
	var q Queue
	q.Enqueue(types.QueuedItem{UUID: "u-1", Value: "a"})

	if ok := q.RemoveByUUID(""); ok {
		t.Error("RemoveByUUID(\"\") = true, want false")
	}
	if got := q.Len(); got != 1 {
		t.Errorf("Len after empty-uuid remove = %d, want 1 (unchanged)", got)
	}
}

func TestQueue_RemoveByUUID_EmptyQueueReturnsFalse(t *testing.T) {
	var q Queue
	if ok := q.RemoveByUUID("u-1"); ok {
		t.Error("RemoveByUUID on empty queue = true, want false")
	}
	if got := q.Len(); got != 0 {
		t.Errorf("Len after remove on empty = %d, want 0", got)
	}
}

func TestSnapshot_ReturnsCopyWithoutConsuming(t *testing.T) {
	var q Queue
	q.Enqueue(types.QueuedItem{UUID: "u-1", Value: "a", Mode: types.ItemModePrompt})
	q.Enqueue(types.QueuedItem{UUID: "u-2", Value: "b", Mode: types.ItemModePrompt})
	q.Enqueue(types.QueuedItem{UUID: "u-3", Value: "c", Mode: types.ItemModeJob})

	snap := q.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("Snapshot returned %d items, want 3", len(snap))
	}
	if snap[0].UUID != "u-1" || snap[0].Value != "a" {
		t.Errorf("snap[0] = {%s, %q}, want {u-1, a}", snap[0].UUID, snap[0].Value)
	}
	if snap[1].UUID != "u-2" || snap[1].Value != "b" {
		t.Errorf("snap[1] = {%s, %q}, want {u-2, b}", snap[1].UUID, snap[1].Value)
	}
	if snap[2].UUID != "u-3" || snap[2].Value != "c" {
		t.Errorf("snap[2] = {%s, %q}, want {u-3, c}", snap[2].UUID, snap[2].Value)
	}
	if got := q.Len(); got != 3 {
		t.Errorf("Len after Snapshot = %d, want 3 (Snapshot must not consume)", got)
	}
	drained := q.DrainAll()
	if len(drained) != 3 {
		t.Fatalf("DrainAll after Snapshot = %d items, want 3", len(drained))
	}
	if drained[0].UUID != "u-1" || drained[1].UUID != "u-2" || drained[2].UUID != "u-3" {
		t.Errorf("DrainAll after Snapshot = %s, %s, %s; want u-1, u-2, u-3", drained[0].UUID, drained[1].UUID, drained[2].UUID)
	}
}

func TestSnapshot_EmptyQueueReturnsNil(t *testing.T) {
	var q Queue
	snap := q.Snapshot()
	if snap != nil {
		t.Errorf("Snapshot on empty queue = %v, want nil", snap)
	}
	if got := q.Len(); got != 0 {
		t.Errorf("Len after empty Snapshot = %d, want 0", got)
	}
}

func TestSnapshot_MutationDoesNotAffectQueue(t *testing.T) {
	var q Queue
	q.Enqueue(types.QueuedItem{UUID: "u-1", Value: "a"})

	snap := q.Snapshot()
	snap[0].Value = "mutated"
	snap[0].UUID = "changed"

	drained := q.DrainAll()
	if len(drained) != 1 {
		t.Fatalf("DrainAll = %d items, want 1", len(drained))
	}
	if drained[0].Value != "a" || drained[0].UUID != "u-1" {
		t.Errorf("DrainAll[0] = {%s, %q}, want {u-1, a} (Snapshot copy must be independent)", drained[0].UUID, drained[0].Value)
	}
}
