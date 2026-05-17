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
	if len(k.entries) != 2 {
		t.Errorf("Len() = %d, want 2", len(k.entries))
	}
}

func TestKillRing_MaxSize(t *testing.T) {
	t.Parallel()

	k := NewKillRing()
	for i := range 15 {
		k.ResetAccumulation()
		k.Push(string(rune('a'+i)), "")
	}
	if len(k.entries) != killRingMaxSize {
		t.Errorf("Len() = %d, want %d", len(k.entries), killRingMaxSize)
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
