package tui

import (
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// P2-1: Dropped event counter
// ---------------------------------------------------------------------------

func TestTUIHandler_DroppedCounter_Zero(t *testing.T) {
	h := NewTUIHandler()
	if h.Dropped() != 0 {
		t.Errorf("new handler should have 0 dropped, got %d", h.Dropped())
	}
}

func TestTUIHandler_DroppedCounter_WhenBufferFull(t *testing.T) {
	h := NewTUIHandler()
	// Fill the 256-buffer
	for range 256 {
		h.appCh <- textDeltaMsg{Text: "fill"}
	}

	// Next event should be dropped
	h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "dropped"})

	if h.Dropped() != 1 {
		t.Errorf("expected 1 dropped, got %d", h.Dropped())
	}

	// And another
	h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "also dropped"})
	if h.Dropped() != 2 {
		t.Errorf("expected 2 dropped, got %d", h.Dropped())
	}
}

// ---------------------------------------------------------------------------
// P2-3: EventTurnStart and EventQueryStart handling in convertEventToMsg
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_StreamStart(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventTurnStart})
	if msg == nil {
		t.Fatal("EventTurnStart should not return nil")
	}
	_, ok := msg.(turnStartMsg)
	if !ok {
		t.Errorf("expected turnStartMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_EventQueryStart_WithMessage(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventQueryStart,
		Message: &types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock("hello"),
			},
		},
	})
	if msg == nil {
		t.Fatal("EventQueryStart with non-nil Message should not return nil")
	}
	sm, ok := msg.(streamMessageMsg)
	if !ok {
		t.Fatalf("expected streamMessageMsg, got %T", msg)
	}
	if sm.Role != string(types.RoleUser) {
		t.Errorf("expected role %q, got %q", types.RoleUser, sm.Role)
	}
}

func TestConvertEventToMsg_EventQueryStart_NilMessage(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:    types.EventQueryStart,
		Message: nil,
	})
	// nil Message should still return nil — nothing to display
	if msg != nil {
		t.Errorf("EventQueryStart with nil Message should return nil, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// Handle — nil msg (unhandled event)
// ---------------------------------------------------------------------------

func TestTUIHandler_Handle_UnhandledEvent(t *testing.T) {
	h := NewTUIHandler()
	// EventToolParamDelta with nil PartialInput returns nil → Handle does nothing
	h.Handle(types.QueryEvent{Type: types.EventToolParamDelta, PartialInput: nil})
	if h.Dropped() != 0 {
		t.Error("nil msg should not be sent to channel")
	}
	// Buffer has room, so valid event should succeed
	h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "ok"})
	if h.Dropped() != 0 {
		t.Error("valid event should not be dropped")
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — nil ToolUse in ToolUseStart
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_ToolUseStart_NilToolUse(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil ToolUse in ToolUseStart, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — EventToolParamDelta with PartialInput
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_ToolUseDelta_WithPartialInput(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{
			ID:      "t1",
			Delta:   `{"file":"a.go"}`,
			Summary: "a.go",
		},
	})
	tdm, ok := msg.(toolParamDeltaMsg)
	if !ok {
		t.Fatalf("expected toolParamDeltaMsg, got %T", msg)
	}
	if tdm.ID != "t1" {
		t.Errorf("ID = %q, want %q", tdm.ID, "t1")
	}
	if tdm.Summary != "a.go" {
		t.Errorf("Summary = %q, want %q", tdm.Summary, "a.go")
	}
}

func TestConvertEventToMsg_ToolUseDelta_NilPartialInput(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil PartialInput, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — EventThinkingStart / EventThinkingEnd
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_ThinkingStart(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventThinkingStart})
	if msg == nil {
		t.Fatal("EventThinkingStart should not return nil")
	}
	_, ok := msg.(thinkingStartMsg)
	if !ok {
		t.Errorf("expected thinkingStartMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_ThinkingEnd_WithThinking(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:     types.EventThinkingEnd,
		Thinking: &types.ThinkingEvent{Duration: 5 * time.Second},
	})
	if msg == nil {
		t.Fatal("EventThinkingEnd with Thinking should not return nil")
	}
	tem, ok := msg.(thinkingEndMsg)
	if !ok {
		t.Fatalf("expected thinkingEndMsg, got %T", msg)
	}
	if tem.Duration != 5*time.Second {
		t.Errorf("Duration = %v, want 5s", tem.Duration)
	}
}

func TestConvertEventToMsg_ThinkingEnd_NilThinking(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:     types.EventThinkingEnd,
		Thinking: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil Thinking, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — EventThinkingDelta
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_ThinkingDelta(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:     types.EventThinkingDelta,
		Thinking: &types.ThinkingEvent{Text: "reasoning..."},
	})
	if msg == nil {
		t.Fatal("EventThinkingDelta with text should not return nil")
	}
	dm, ok := msg.(thinkingDeltaMsg)
	if !ok {
		t.Fatalf("expected thinkingDeltaMsg, got %T", msg)
	}
	if dm.Text != "reasoning..." {
		t.Errorf("Text = %q, want %q", dm.Text, "reasoning...")
	}
}

func TestConvertEventToMsg_ThinkingDelta_EmptyText(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:     types.EventThinkingDelta,
		Thinking: &types.ThinkingEvent{Text: ""},
	})
	if msg != nil {
		t.Errorf("empty text should return nil, got %T", msg)
	}
}

func TestConvertEventToMsg_ThinkingDelta_NilThinking(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventThinkingDelta,
	})
	if msg != nil {
		t.Errorf("nil Thinking should return nil, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — EventUsage with nil Usage
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_Usage_NilUsage(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:  types.EventUsage,
		Usage: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil Usage, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — EventToolOutputDelta with DisplayOutput
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_ToolUseDelta_WithToolResultDisplayOutput(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     "t1",
			DisplayOutput: "line1\nline2",
			Timing:        500 * time.Millisecond,
		},
	})
	m, ok := msg.(toolOutputDeltaMsg)
	if !ok {
		t.Fatalf("expected toolOutputDeltaMsg, got %T", msg)
	}
	if m.ToolUseID != "t1" {
		t.Errorf("ToolUseID = %q, want t1", m.ToolUseID)
	}
	if m.DisplayOutput != "line1\nline2" {
		t.Errorf("DisplayOutput = %q, want %q", m.DisplayOutput, "line1\nline2")
	}
	if m.Timing != 500*time.Millisecond {
		t.Errorf("Timing = %v, want 500ms", m.Timing)
	}
}

func TestConvertEventToMsg_ToolUseDelta_DisplayOutputEmpty(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     "t1",
			DisplayOutput: "",
			Timing:        0,
		},
	})
	if msg != nil {
		t.Errorf("expected nil for empty DisplayOutput, got %T", msg)
	}
}

func TestConvertEventToMsg_ToolUseDelta_ToolResultNil(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:       types.EventToolParamDelta,
		ToolResult: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil ToolResult, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — EventQueryEnd, EventTurnEnd
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_QueryEnd(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventQueryEnd})
	if msg == nil {
		t.Fatal("EventQueryEnd should not return nil")
	}
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Errorf("expected queryEndMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_TurnEnd(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventTurnEnd})
	if msg != nil {
		t.Errorf("EventTurnEnd should return nil, got %T", msg)
	}
}
