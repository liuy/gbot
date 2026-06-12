package engine

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// TestEmitEvent_CoalescesTextDeltas verifies that multiple text_delta events
// are buffered and only flushed when a non-delta event arrives.
func TestEmitEvent_CoalescesTextDeltas(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	// Send 3 text deltas — should be buffered, not dispatched
	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "AA"})
	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "BB"})
	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "CC"})

	if len(md.events) != 0 {
		t.Fatalf("text deltas should be buffered, got %d events", len(md.events))
	}

	// Non-delta event triggers flush
	eng.emitEvent(types.QueryEvent{Type: types.EventTurnStart})

	if len(md.events) != 2 {
		t.Fatalf("expected 2 events (flushed text + turn_start), got %d", len(md.events))
	}

	if md.events[0].Type != types.EventTextDelta {
		t.Fatalf("first event should be flushed text_delta, got %s", md.events[0].Type)
	}
	if md.events[0].Text != "AABBCC" {
		t.Errorf("coalesced text = %q, want %q", md.events[0].Text, "AABBCC")
	}
	if md.events[1].Type != types.EventTurnStart {
		t.Fatalf("second event should be turn_start, got %s", md.events[1].Type)
	}
}

// TestEmitEvent_CoalescesThinkingDeltas verifies thinking_delta coalescing.
func TestEmitEvent_CoalescesThinkingDeltas(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "think1 "}})
	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "think2"}})

	if len(md.events) != 0 {
		t.Fatalf("thinking deltas should be buffered, got %d events", len(md.events))
	}

	// Flush with non-delta
	eng.emitEvent(types.QueryEvent{Type: types.EventTextStart})

	if len(md.events) != 2 {
		t.Fatalf("expected 2 events (flushed thinking + text_start), got %d", len(md.events))
	}

	if md.events[0].Type != types.EventThinkingDelta {
		t.Fatalf("first event should be thinking_delta, got %s", md.events[0].Type)
	}
	if md.events[0].Thinking.Text != "think1 think2" {
		t.Errorf("coalesced thinking = %q, want %q", md.events[0].Thinking.Text, "think1 think2")
	}
}

// TestEmitEvent_FlushOnWindowExpiry verifies that the 100ms coalesce window
// triggers a flush even without a non-delta event.
func TestEmitEvent_FlushOnWindowExpiry(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "early"})

	if len(md.events) != 0 {
		t.Fatal("should be buffered initially")
	}

	// Simulate window expiry by winding back lastTextFlush
	eng.textCoalesce.lastFlush = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	// Next delta should trigger flush of the old buffer
	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "late"})

	if len(md.events) != 1 {
		t.Fatalf("expected 1 flushed event, got %d", len(md.events))
	}
	if md.events[0].Text != "early" {
		t.Errorf("flushed text = %q, want %q", md.events[0].Text, "early")
	}
}

// TestEmitEvent_PerEngineIsolation verifies that two different Engine instances
// have completely independent coalescing buffers — no shared state.
func TestEmitEvent_PerEngineIsolation(t *testing.T) {
	mdA := &mockDispatcher{}
	mdB := &mockDispatcher{}

	engA := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: mdA,
	})
	t.Cleanup(func() { engA.Close() })
	engB := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: mdB,
	})

	// Interleave deltas from two engines
	engA.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "A1"})
	engB.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "B1"})
	engA.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "A2"})
	engB.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "B2"})

	// Flush both
	engA.emitEvent(types.QueryEvent{Type: types.EventTurnStart})
	engB.emitEvent(types.QueryEvent{Type: types.EventTurnStart})

	// Engine A should only have A's text
	textA := coalescedText(mdA.Events())
	if textA != "A1A2" {
		t.Errorf("engine A text = %q, want %q", textA, "A1A2")
	}

	// Engine B should only have B's text
	textB := coalescedText(mdB.Events())
	if textB != "B1B2" {
		t.Errorf("engine B text = %q, want %q", textB, "B1B2")
	}

	// Verify no cross-contamination
	for _, ch := range textA {
		if !strings.ContainsRune("A12", ch) {
			t.Errorf("engine A contaminated with char %q", ch)
		}
	}
	for _, ch := range textB {
		if !strings.ContainsRune("B12", ch) {
			t.Errorf("engine B contaminated with char %q", ch)
		}
	}
}

// TestEmitEvent_FlushesOnQueryEnd verifies that EventQueryEnd (non-delta)
// triggers a flush of any remaining buffered text before itself being dispatched.
func TestEmitEvent_FlushesOnQueryEnd(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "final"})
	eng.emitEvent(types.QueryEvent{Type: types.EventQueryEnd})

	if len(md.events) != 2 {
		t.Fatalf("expected 2 events (flushed text + query_end), got %d", len(md.events))
	}
	if md.events[0].Type != types.EventTextDelta || md.events[0].Text != "final" {
		t.Errorf("first event should be flushed text_delta 'final', got %+v", md.events[0])
	}
	if md.events[1].Type != types.EventQueryEnd {
		t.Errorf("second event should be query_end, got %s", md.events[1].Type)
	}
}

// TestEmitEvent_NilDispatcherDiscards verifies that emitEvent with nil dispatcher
// is a no-op (sub-engine without dispatcher).
func TestEmitEvent_NilDispatcherDiscards(t *testing.T) {
	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test",
		Logger:   slog.Default(),
		// No Dispatcher
	})
	t.Cleanup(func() { eng.Close() })

	// Should not panic
	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "discarded"})
	eng.emitEvent(types.QueryEvent{Type: types.EventTurnStart})
}

// TestEmitEvent_ThinkingDeltaEmptySkipped verifies that thinking_delta with
// empty text is not buffered.
func TestEmitEvent_ThinkingDeltaEmptySkipped(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: ""}})
	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: nil})
	eng.emitEvent(types.QueryEvent{Type: types.EventTurnStart})

	for _, evt := range md.events {
		if evt.Type == types.EventThinkingDelta {
			t.Error("empty thinking deltas should not produce events")
		}
	}
}

func coalescedText(events []types.QueryEvent) string {
	var buf strings.Builder
	for _, evt := range events {
		if evt.Type == types.EventTextDelta {
			buf.WriteString(evt.Text)
		}
	}
	return buf.String()
}
