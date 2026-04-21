package tui

import (
	"strings"
	"testing"
)

func TestKillRing_PushAndTop(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	k.Push("hello", "")
	if k.Top() != "hello" {
		t.Errorf("Top() = %q, want %q", k.Top(), "hello")
	}
}

func TestKillRing_PushEmpty(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	k.Push("", "")
	if k.Top() != "" {
		t.Errorf("empty push should not add entry, Top() = %q", k.Top())
	}
}

func TestKillRing_Append(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	k.Push("hello", "")
	k.Push(" world", "append")
	if k.Top() != "hello world" {
		t.Errorf("Top() = %q, want %q", k.Top(), "hello world")
	}
}

func TestKillRing_Prepend(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	k.Push("world", "")
	k.Push("hello ", "prepend")
	if k.Top() != "hello world" {
		t.Errorf("Top() = %q, want %q", k.Top(), "hello world")
	}
}

func TestKillRing_NewEntryAfterReset(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	k.Push("first", "")
	k.ResetAccumulation()
	k.Push("second", "")
	// Should have two entries, newest first
	if k.Top() != "second" {
		t.Errorf("Top() = %q, want %q", k.Top(), "second")
	}
	if k.Len() != 2 {
		t.Errorf("Len() = %d, want 2", k.Len())
	}
}

func TestKillRing_Pop(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	k.Push("first", "")
	k.Push("second", "")

	top := k.Pop()
	if top != "second" {
		t.Errorf("Pop() = %q, want %q", top, "second")
	}
	if k.Top() != "first" {
		t.Errorf("after Pop, Top() = %q, want %q", k.Top(), "first")
	}
}

func TestKillRing_PopEmpty(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	if k.Pop() != "" {
		t.Error("Pop() on empty ring should return empty string")
	}
}

func TestKillRing_Clear(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	k.Push("hello", "")
	k.Clear()
	if k.Len() != 0 {
		t.Errorf("after Clear, Len() = %d, want 0", k.Len())
	}
	if k.Top() != "" {
		t.Errorf("after Clear, Top() = %q, want empty", k.Top())
	}
}

func TestKillRing_Len(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	if k.Len() != 0 {
		t.Errorf("Len() = %d, want 0", k.Len())
	}
	k.Push("a", "")
	k.Push("b", "")
	if k.Len() != 2 {
		t.Errorf("Len() = %d, want 2", k.Len())
	}
}

func TestKillRing_MaxSize(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	for i := range 15 {
		k.ResetAccumulation()
		k.Push(string(rune('a'+i)), "")
	}
	if k.Len() != killRingMaxSize {
		t.Errorf("Len() = %d, want %d", k.Len(), killRingMaxSize)
	}
	// Oldest entries should have been evicted
	if strings.Contains(k.Top(), "a") {
		t.Error("oldest entries should have been evicted")
	}
	if k.Top() != "o" {
		t.Errorf("Top() = %q, want %q (newest entry)", k.Top(), "o")
	}
}

func TestKillRing_TopEmpty(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	if k.Top() != "" {
		t.Errorf("Top() on empty ring = %q, want empty", k.Top())
	}
}
