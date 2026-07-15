package engine

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
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

// TestEmitEvent_CoalescesToolParamDeltas verifies that multiple
// tool_param_delta events for the same tool_use_id are buffered and flushed as
// a single coalesced event when a non-delta event arrives.
func TestEmitEvent_CoalescesToolParamDeltas(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{
		Type: types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{
			ID:    "tool_1",
			Name:  "Bash",
			Delta: `{"command":"`,
		},
	})
	eng.emitEvent(types.QueryEvent{
		Type: types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{
			ID:    "tool_1",
			Name:  "Bash",
			Delta: `echo hi"}`,
		},
	})

	if len(md.events) != 0 {
		t.Fatalf("tool_param_delta should be buffered, got %d events", len(md.events))
	}

	eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

	if len(md.events) != 2 {
		t.Fatalf("expected 2 events (flushed param_delta + tool_start), got %d", len(md.events))
	}
	if md.events[0].Type != types.EventToolParamDelta {
		t.Fatalf("first event should be tool_param_delta, got %s", md.events[0].Type)
	}
	if md.events[0].PartialInput == nil {
		t.Fatal("PartialInput should not be nil")
	}
	if md.events[0].PartialInput.ID != "tool_1" {
		t.Errorf("coalesced param id = %q, want %q", md.events[0].PartialInput.ID, "tool_1")
	}
	if md.events[0].PartialInput.Name != "Bash" {
		t.Errorf("coalesced param name = %q, want %q", md.events[0].PartialInput.Name, "Bash")
	}
	wantDelta := `{"command":"echo hi"}`
	if md.events[0].PartialInput.Delta != wantDelta {
		t.Errorf("coalesced param delta = %q, want %q", md.events[0].PartialInput.Delta, wantDelta)
	}
	if md.events[1].Type != types.EventToolStart {
		t.Fatalf("second event should be tool_start, got %s", md.events[1].Type)
	}
}

// TestEmitEvent_CoalescesToolOutputDeltas verifies that multiple
// tool_output_delta events for the same tool_use_id are buffered and flushed
// as a single coalesced event.
func TestEmitEvent_CoalescesToolOutputDeltas(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{
		Type: types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     "tool_1",
			DisplayOutput: "line1\n",
			IsSearch:      true,
		},
	})
	eng.emitEvent(types.QueryEvent{
		Type: types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     "tool_1",
			DisplayOutput: "line2\n",
			IsSearch:      true,
		},
	})

	if len(md.events) != 0 {
		t.Fatalf("tool_output_delta should be buffered, got %d events", len(md.events))
	}

	eng.emitEvent(types.QueryEvent{Type: types.EventToolEnd})

	if len(md.events) != 2 {
		t.Fatalf("expected 2 events (flushed output_delta + tool_end), got %d", len(md.events))
	}
	if md.events[0].Type != types.EventToolOutputDelta {
		t.Fatalf("first event should be tool_output_delta, got %s", md.events[0].Type)
	}
	if md.events[0].ToolResult == nil {
		t.Fatal("ToolResult should not be nil")
	}
	if md.events[0].ToolResult.ToolUseID != "tool_1" {
		t.Errorf("coalesced output id = %q, want %q", md.events[0].ToolResult.ToolUseID, "tool_1")
	}
	wantOutput := "line1\nline2\n"
	if md.events[0].ToolResult.DisplayOutput != wantOutput {
		t.Errorf("coalesced output = %q, want %q", md.events[0].ToolResult.DisplayOutput, wantOutput)
	}
	if !md.events[0].ToolResult.IsSearch {
		t.Error("coalesced output lost IsSearch metadata")
	}
	if md.events[1].Type != types.EventToolEnd {
		t.Fatalf("second event should be tool_end, got %s", md.events[1].Type)
	}
}

// TestEmitEvent_ToolDeltaMultiID verifies that deltas for different tool_use_ids
// accumulate in parallel and both flush together when a non-delta event arrives.
func TestEmitEvent_ToolDeltaMultiID(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_A", Delta: "A1"},
	})
	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_B", Delta: "B1"},
	})
	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_A", Delta: "A2"},
	})
	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_B", Delta: "B2"},
	})

	if len(md.events) != 0 {
		t.Fatalf("param deltas should be buffered, got %d events", len(md.events))
	}

	eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

	// 2 coalesced param deltas + 1 tool_start
	if len(md.events) != 3 {
		t.Fatalf("expected 3 events (2 param_delta + tool_start), got %d", len(md.events))
	}

	deltas := map[string]string{}
	for _, evt := range md.events[:2] {
		if evt.Type != types.EventToolParamDelta {
			t.Errorf("expected tool_param_delta, got %s", evt.Type)
			continue
		}
		if evt.PartialInput == nil {
			t.Error("PartialInput nil")
			continue
		}
		deltas[evt.PartialInput.ID] = evt.PartialInput.Delta
	}
	if deltas["tool_A"] != "A1A2" {
		t.Errorf("tool_A delta = %q, want %q", deltas["tool_A"], "A1A2")
	}
	if deltas["tool_B"] != "B1B2" {
		t.Errorf("tool_B delta = %q, want %q", deltas["tool_B"], "B1B2")
	}
}

// TestEmitEvent_ToolDeltaWindowExpiry verifies that the coalesce window
// triggers a flush even without a non-delta event.
func TestEmitEvent_ToolDeltaWindowExpiry(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_1", Delta: "early"},
	})

	if len(md.events) != 0 {
		t.Fatal("should be buffered initially")
	}

	eng.paramCoalesce.lastFlush = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_1", Delta: "late"},
	})

	if len(md.events) != 1 {
		t.Fatalf("expected 1 flushed event, got %d", len(md.events))
	}
	if md.events[0].PartialInput == nil {
		t.Fatal("PartialInput nil")
	}
	if md.events[0].PartialInput.Delta != "early" {
		t.Errorf("flushed delta = %q, want %q", md.events[0].PartialInput.Delta, "early")
	}
}

// TestEmitEvent_ToolDeltaEmptySkipped verifies that tool delta events with
// empty text are not buffered.
func TestEmitEvent_ToolDeltaEmptySkipped(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_1", Delta: ""},
	})
	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: nil,
	})
	eng.emitEvent(types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tool_1", DisplayOutput: ""},
	})
	eng.emitEvent(types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		ToolResult: nil,
	})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

	for _, evt := range md.events {
		if evt.Type == types.EventToolParamDelta || evt.Type == types.EventToolOutputDelta {
			t.Errorf("empty tool deltas should not produce events, got %s", evt.Type)
		}
	}
}

// TestEmitEvent_ConcurrentToolParamDeltas verifies that multiple goroutines
// emitting param deltas for distinct tool_use_ids produce exactly one coalesced
// event per id on flush. Exercises the coalesceMu race protection.
func TestEmitEvent_ConcurrentToolParamDeltas(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("tool_%d", i)
			eng.emitEvent(types.QueryEvent{
				Type:         types.EventToolParamDelta,
				PartialInput: &types.PartialInputEvent{ID: id, Delta: fmt.Sprintf("delta_%d", i)},
			})
		}(i)
	}
	wg.Wait()

	// Flush via a non-delta event
	eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

	// Count param delta events per id
	seen := make(map[string]string)
	for _, evt := range md.events {
		if evt.Type == types.EventToolParamDelta && evt.PartialInput != nil {
			seen[evt.PartialInput.ID] = evt.PartialInput.Delta
		}
	}
	if len(seen) != n {
		t.Errorf("expected %d coalesced param events, got %d", n, len(seen))
	}
	for i := range n {
		id := fmt.Sprintf("tool_%d", i)
		want := fmt.Sprintf("delta_%d", i)
		if got, ok := seen[id]; !ok || got != want {
			t.Errorf("param delta for %s = %q, want %q (ok=%v)", id, got, want, ok)
		}
	}
}

// TestEmitEvent_FlushBufsFlushesAllFour verifies that a single non-delta event
// flushes text, thinking, param, AND output coalesce buffers simultaneously.
func TestEmitEvent_FlushBufsFlushesAllFour(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "txt"})
	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "thk"}})
	eng.emitEvent(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "tool_1", Delta: "param"},
	})
	eng.emitEvent(types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tool_2", DisplayOutput: "output"},
	})

	if len(md.events) != 0 {
		t.Fatalf("expected 0 events before flush, got %d", len(md.events))
	}

	// Single non-delta event triggers flushBufs → all four flush
	eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

	gotText, gotThink, gotParam, gotOutput := false, false, false, false
	for _, evt := range md.events {
		switch evt.Type {
		case types.EventTextDelta:
			if evt.Text == "txt" {
				gotText = true
			}
		case types.EventThinkingDelta:
			if evt.Thinking != nil && evt.Thinking.Text == "thk" {
				gotThink = true
			}
		case types.EventToolParamDelta:
			if evt.PartialInput != nil && evt.PartialInput.Delta == "param" {
				gotParam = true
			}
		case types.EventToolOutputDelta:
			if evt.ToolResult != nil && evt.ToolResult.DisplayOutput == "output" {
				gotOutput = true
			}
		}
	}
	if !gotText {
		t.Error("flushBufs did not flush text delta")
	}
	if !gotThink {
		t.Error("flushBufs did not flush thinking delta")
	}
	if !gotParam {
		t.Error("flushBufs did not flush param delta")
	}
	if !gotOutput {
		t.Error("flushBufs did not flush output delta")
	}
}

// TestEmitEvent_WindowExpiredMidStream verifies that deltas arriving after
// the coalesce window expires trigger an early flush of the previous batch,
// producing multiple coalesced events for the same id.
func TestEmitEvent_WindowExpiredMidStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		md := &mockDispatcher{}
		eng := New(&Params{
			Provider:   &mockProvider{},
			Model:      "test",
			Logger:     slog.Default(),
			Dispatcher: md,
		})
		t.Cleanup(func() { eng.Close() })

		eng.window = 1 * time.Millisecond

		eng.emitEvent(types.QueryEvent{
			Type:         types.EventToolParamDelta,
			PartialInput: &types.PartialInputEvent{ID: "tool_1", Delta: "first"},
		})
		time.Sleep(5 * time.Millisecond)
		eng.emitEvent(types.QueryEvent{
			Type:         types.EventToolParamDelta,
			PartialInput: &types.PartialInputEvent{ID: "tool_1", Delta: "second"},
		})
		eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

		var deltas []string
		for _, evt := range md.events {
			if evt.Type == types.EventToolParamDelta && evt.PartialInput != nil {
				deltas = append(deltas, evt.PartialInput.Delta)
			}
		}
		if len(deltas) != 2 {
			t.Fatalf("expected 2 coalesced param events (window split), got %d: %v", len(deltas), deltas)
		}
		if deltas[0] != "first" {
			t.Errorf("first delta = %q, want %q", deltas[0], "first")
		}
		if deltas[1] != "second" {
			t.Errorf("second delta = %q, want %q", deltas[1], "second")
		}
	})
}

// TestEmitEvent_E2E_ToolParamDeltaPreservesMetadata verifies that coalesced
// tool_param_delta events preserve Name and Summary from the original stream.
// This is the observable behavior downstream consumers (TUI, wui) rely on.
func TestEmitEvent_E2E_ToolParamDeltaPreservesMetadata(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	// Simulate LLM streaming tool_use input deltas with metadata.
	deltas := []struct {
		delta   string
		name    string
		summary string
	}{
		{`{"`, "Bash", ""},
		{`command`, "Bash", ""},
		{`":"`, "Bash", "Execute a bash command"},
		{`grep`, "Bash", "Execute a bash command"},
		{` test`, "Bash", "Execute a bash command"},
		{`"}`, "Bash", "Execute a bash command"},
	}
	for _, d := range deltas {
		eng.emitEvent(types.QueryEvent{
			Type: types.EventToolParamDelta,
			PartialInput: &types.PartialInputEvent{
				ID:      "tool_1",
				Delta:   d.delta,
				Name:    d.name,
				Summary: d.summary,
			},
		})
	}

	// Non-delta event triggers flush.
	eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

	var paramEvents []types.QueryEvent
	for _, evt := range md.events {
		if evt.Type == types.EventToolParamDelta {
			paramEvents = append(paramEvents, evt)
		}
	}

	if len(paramEvents) != 1 {
		t.Fatalf("expected 1 coalesced param event, got %d", len(paramEvents))
	}

	pi := paramEvents[0].PartialInput
	if pi.ID != "tool_1" {
		t.Errorf("ID = %q, want tool_1", pi.ID)
	}
	if pi.Name != "Bash" {
		t.Errorf("Name = %q, want Bash", pi.Name)
	}
	if pi.Summary != "Execute a bash command" {
		t.Errorf("Summary = %q, want 'Execute a bash command'", pi.Summary)
	}

	var want strings.Builder
	for _, d := range deltas {
		want.WriteString(d.delta)
	}
	if pi.Delta != want.String() {
		t.Errorf("Delta = %q, want %q", pi.Delta, want.String())
	}
}

// TestEmitEvent_E2E_ConcurrentToolOutputDeltas simulates tool workers on
// separate goroutines emitting output deltas. Verifies coalescing is safe
// under -race and each id gets its own coalesced event.
func TestEmitEvent_E2E_ConcurrentToolOutputDeltas(t *testing.T) {
	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("tool_%d", i)
			for j := range 5 {
				eng.emitEvent(types.QueryEvent{
					Type: types.EventToolOutputDelta,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     id,
						DisplayOutput: fmt.Sprintf("line_%d_%d\n", i, j),
					},
				})
			}
		}(i)
	}
	wg.Wait()

	eng.emitEvent(types.QueryEvent{Type: types.EventToolEnd})

	outputByID := make(map[string]string)
	for _, evt := range md.events {
		if evt.Type == types.EventToolOutputDelta && evt.ToolResult != nil {
			outputByID[evt.ToolResult.ToolUseID] += evt.ToolResult.DisplayOutput
		}
	}

	if len(outputByID) != n {
		t.Errorf("expected %d distinct tool output events, got %d", n, len(outputByID))
	}

	for i := range n {
		id := fmt.Sprintf("tool_%d", i)
		got, ok := outputByID[id]
		if !ok {
			t.Errorf("missing output for %s", id)
			continue
		}
		for j := range 5 {
			want := fmt.Sprintf("line_%d_%d\n", i, j)
			if !strings.Contains(got, want) {
				t.Errorf("output for %s missing %q, got: %s", id, want, got)
			}
		}
	}
}

// TestEmitEvent_E2E_DeltaStormReduction verifies that a burst of 100 param
// deltas for a single tool produces at most a few coalesced events (not 100).
// This is the core anti-hang guarantee.
func TestEmitEvent_E2E_DeltaStormReduction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		md := &mockDispatcher{}
		eng := New(&Params{
			Provider:   &mockProvider{},
			Model:      "test",
			Logger:     slog.Default(),
			Dispatcher: md,
		})
		t.Cleanup(func() { eng.Close() })

		eng.window = 100 * time.Millisecond

		// Simulate 100 deltas arriving within one window (no sleep).
		for i := range 100 {
			eng.emitEvent(types.QueryEvent{
				Type: types.EventToolParamDelta,
				PartialInput: &types.PartialInputEvent{
					ID:    "tool_1",
					Delta: fmt.Sprintf("%d,", i),
				},
			})
		}

		// No flush yet — all within window.
		paramCount := 0
		for _, evt := range md.events {
			if evt.Type == types.EventToolParamDelta {
				paramCount++
			}
		}
		if paramCount != 0 {
			t.Errorf("expected 0 dispatched before flush, got %d", paramCount)
		}

		// Flush via non-delta event.
		eng.emitEvent(types.QueryEvent{Type: types.EventToolStart})

		paramCount = 0
		for _, evt := range md.events {
			if evt.Type == types.EventToolParamDelta {
				paramCount++
			}
		}
		if paramCount != 1 {
			t.Errorf("expected 1 coalesced param event from 100 deltas, got %d", paramCount)
		}
	})
}
