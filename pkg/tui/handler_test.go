package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
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

func TestTUIHandler_Coalescing_AccumulatesText(t *testing.T) {
	h := NewTUIHandler()
	// text_delta events accumulate, don't go to channel immediately
	for range 10 {
		h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
	}

	// Channel should be empty — text is coalesced
	select {
	case <-h.appCh:
		t.Fatal("expected no messages yet, text should be coalesced")
	default:
	}

	// Non-text event triggers flush
	h.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{}})

	// Now channel should have: flushed text + usage = 2 messages
	msgs := 0
drain:
	for {
		select {
		case <-h.appCh:
			msgs++
		default:
			break drain
		}
	}
	if msgs != 2 {
		t.Errorf("expected 2 messages (flushed text + usage), got %d", msgs)
	}
}

func TestTUIHandler_Coalescing_FlushesOnTimeWindow(t *testing.T) {
	h := NewTUIHandler()

	// Send a small text — should NOT flush immediately (window not elapsed)
	h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "hi"})
	select {
	case <-h.appCh:
		t.Fatal("should not flush within 100ms window")
	default:
	}

	// Wait for the 100ms window to pass
	time.Sleep(150 * time.Millisecond) // REAL-TIME: waiting for TUIHandler's internal coalescing timer (100ms window)

	// Send another text_delta — should trigger flush (window elapsed)
	h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "there"})

	// Should get the coalesced message
	select {
	case msg := <-h.appCh:
		td, ok := msg.(textDeltaMsg)
		if !ok {
			t.Fatalf("expected textDeltaMsg, got %T", msg)
		}
		if td.Text != "hithere" {
			t.Errorf("expected coalesced 'hithere', got %q", td.Text)
		}
	default:
		t.Fatal("expected flushed message after window elapsed")
	}

	// Flush remaining (nothing should be left)
	h.Flush()
	select {
	case <-h.appCh:
		t.Fatal("nothing should remain after time-window flush")
	default:
	}
}

func TestTUIHandler_BlockingWrite_WaitsForConsumer(t *testing.T) {
	h := NewTUIHandler()
	// Fill the buffer
	for range cap(h.appCh) {
		h.appCh <- turnStartMsg{}
	}

	// A non-coalesced event (usage) should block until consumer drains
	done := make(chan struct{})
	go func() {
		h.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{}})
		close(done)
	}()

	// Handle should be blocked
	select {
	case <-done:
		t.Fatal("Handle should block when buffer is full")
	case <-time.After(50 * time.Millisecond):
		// Good — still blocked
	}

	// Drain one message — Handle should complete
	<-h.appCh
	select {
	case <-done:
		// Good — Handle completed
	case <-time.After(time.Second):
		t.Fatal("Handle should complete after consumer drains")
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
	// nil Message should still return nil: nothing to display
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
	// EventQueryEnd produces queryEndMsg — the sole completion signal
	// now that resultCh has been removed.
	if _, ok := msg.(queryEndMsg); !ok {
		t.Errorf("EventQueryEnd should return queryEndMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_TurnEnd(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventTurnEnd})
	if msg != nil {
		t.Errorf("EventTurnEnd should return nil, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// convertEventToMsg — agent (sub-agent) event branches
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_AgentToolStart(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventToolStart,
		Agent: &types.AgentMeta{ParentToolUseID: "parent-1", AgentType: "Explore", Depth: 0},
		ToolUse: &types.ToolUseEvent{ID: "child-1", Name: "Grep", Summary: "searching"},
	})
	ts, ok := msg.(toolStartMsg)
	if !ok {
		t.Fatalf("expected toolStartMsg, got %T", msg)
	}
	if ts.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if ts.Agent.ParentToolUseID != "parent-1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", ts.Agent.ParentToolUseID, "parent-1")
	}
	if ts.Agent.AgentType != "Explore" {
		t.Errorf("Agent.AgentType = %q, want %q", ts.Agent.AgentType, "Explore")
	}
	if ts.Name != "Grep" {
		t.Errorf("Name = %q, want %q", ts.Name, "Grep")
	}
}

func TestConvertEventToMsg_AgentToolStart_NilToolUse(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:   types.EventToolStart,
		Agent:  &types.AgentMeta{ParentToolUseID: "p1"},
		ToolUse: nil,
	})
	if msg != nil {
		t.Errorf("nil ToolUse with agent should return nil, got %T", msg)
	}
}

func TestConvertEventToMsg_AgentToolParamDelta(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventToolParamDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "p1", AgentType: "general-purpose", Depth: 1},
		PartialInput: &types.PartialInputEvent{ID: "c1", Name: "Read", Delta: `{"path":"a.go"}`, Summary: "reading"},
	})
	pd, ok := msg.(toolParamDeltaMsg)
	if !ok {
		t.Fatalf("expected toolParamDeltaMsg, got %T", msg)
	}
	if pd.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if pd.Agent.ParentToolUseID != "p1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", pd.Agent.ParentToolUseID, "p1")
	}
	if pd.ID != "c1" {
		t.Errorf("ID = %q, want %q", pd.ID, "c1")
	}
	if pd.Summary != "reading" {
		t.Errorf("Summary = %q, want %q", pd.Summary, "reading")
	}
}

func TestConvertEventToMsg_AgentToolEnd(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventToolEnd,
		Agent: &types.AgentMeta{ParentToolUseID: "p1", AgentType: "Explore"},
		ToolResult: &types.ToolResultEvent{ToolUseID: "c1", IsError: true},
	})
	te, ok := msg.(toolEndMsg)
	if !ok {
		t.Fatalf("expected toolEndMsg, got %T", msg)
	}
	if te.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if te.Agent.ParentToolUseID != "p1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", te.Agent.ParentToolUseID, "p1")
	}
	if !te.IsError {
		t.Error("IsError = false, want true")
	}
}

func TestConvertEventToMsg_AgentToolRun(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:   types.EventToolRun,
		Agent:  &types.AgentMeta{ParentToolUseID: "p1", AgentType: "general-purpose"},
		ToolUse: &types.ToolUseEvent{ID: "c1", Name: "Bash"},
	})
	tr, ok := msg.(toolRunMsg)
	if !ok {
		t.Fatalf("expected toolRunMsg, got %T", msg)
	}
	if tr.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if tr.Agent.ParentToolUseID != "p1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", tr.Agent.ParentToolUseID, "p1")
	}
	if tr.Name != "Bash" {
		t.Errorf("Name = %q, want Bash", tr.Name)
	}
}

func TestConvertEventToMsg_AgentThinkingStart(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:  types.EventThinkingStart,
		Agent: &types.AgentMeta{ParentToolUseID: "p1", AgentType: "Explore"},
	})
	ts, ok := msg.(thinkingStartMsg)
	if !ok {
		t.Fatalf("expected thinkingStartMsg, got %T", msg)
	}
	if ts.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if ts.Agent.ParentToolUseID != "p1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", ts.Agent.ParentToolUseID, "p1")
	}
}

func TestConvertEventToMsg_AgentThinkingEnd(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:  types.EventThinkingEnd,
		Agent: &types.AgentMeta{ParentToolUseID: "p1"},
	})
	// Agent thinking_end with nil Thinking returns nil
	if msg != nil {
		t.Errorf("expected nil for agent ThinkingEnd with nil Thinking, got %T", msg)
	}
}

func TestConvertEventToMsg_AgentThinkingEnd_WithThinking(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:  types.EventThinkingEnd,
		Agent: &types.AgentMeta{ParentToolUseID: "p1"},
		Thinking: &types.ThinkingEvent{Duration: 1 * time.Second},
	})
	te, ok := msg.(thinkingEndMsg)
	if !ok {
		t.Fatalf("expected thinkingEndMsg, got %T", msg)
	}
	if te.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if te.Agent.ParentToolUseID != "p1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", te.Agent.ParentToolUseID, "p1")
	}
}

func TestConvertEventToMsg_AgentUsage(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:  types.EventUsage,
		Agent: &types.AgentMeta{ParentToolUseID: "p1"},
		Usage: &types.UsageEvent{InputTokens: 50, OutputTokens: 25, CacheReadInputTokens: 10},
	})
	u, ok := msg.(usageMsg)
	if !ok {
		t.Fatalf("expected usageMsg, got %T", msg)
	}
	if u.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if u.Agent.ParentToolUseID != "p1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", u.Agent.ParentToolUseID, "p1")
	}
	if u.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", u.InputTokens)
	}
	if u.CacheReadInputTokens != 10 {
		t.Errorf("CacheReadInputTokens = %d, want 10", u.CacheReadInputTokens)
	}
}

func TestConvertEventToMsg_AgentTextDelta_ProducesTextDeltaMsg(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:  types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "p1"},
		Text:  "sub-agent text",
	})
	td, ok := msg.(textDeltaMsg)
	if !ok {
		t.Fatalf("expected textDeltaMsg, got %T", msg)
	}
	if td.Agent == nil {
		t.Fatal("Agent should not be nil")
	}
	if td.Agent.ParentToolUseID != "p1" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", td.Agent.ParentToolUseID, "p1")
	}
	if td.Text != "sub-agent text" {
		t.Errorf("Text = %q, want %q", td.Text, "sub-agent text")
	}
}

func TestConvertEventToMsg_EventTextStart(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventTextStart})
	if msg == nil {
		t.Fatal("EventTextStart should not return nil")
	}
	if _, ok := msg.(textStartMsg); !ok {
		t.Errorf("expected textStartMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_EventTextEnd(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventTextEnd})
	if msg == nil {
		t.Fatal("EventTextEnd should not return nil")
	}
	if _, ok := msg.(textEndMsg); !ok {
		t.Errorf("expected textEndMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_EventNotificationPending(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventNotificationPending})
	if msg == nil {
		t.Fatal("EventNotificationPending should not return nil")
	}
	if _, ok := msg.(notificationPendingMsg); !ok {
		t.Errorf("expected notificationPendingMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_UnknownEventType(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: "something_else"})
	if msg != nil {
		t.Errorf("unknown event type should return nil, got %T", msg)
	}
}

func TestConvertEventToMsg_ToolRun(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"},
	})
	trm, ok := msg.(toolRunMsg)
	if !ok {
		t.Fatalf("expected toolRunMsg, got %T", msg)
	}
	if trm.Name != "Bash" {
		t.Errorf("Name = %q, want Bash", trm.Name)
	}
}

// ---------------------------------------------------------------------------
// Permission ask event conversion and delivery
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_PermissionAsk(t *testing.T) {
	h := NewTUIHandler()
	ch := make(chan types.PermissionUserDecision, 1)
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventPermissionAsk,
		PermissionAsk: &types.PermissionAskEvent{
			ToolName:   "Bash",
			Input:      json.RawMessage(`{"command":"rm -rf /tmp"}`),
			Message:    "permission required",
			RuleDetail: "Bash(rm -rf *)",
			ResponseCh: ch,
		},
	})
	pm, ok := msg.(permissionAskMsg)
	if !ok {
		t.Fatalf("expected permissionAskMsg, got %T", msg)
	}
	if pm.event == nil {
		t.Fatal("event should not be nil")
	}
	if pm.event.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", pm.event.ToolName)
	}
	if pm.event.RuleDetail != "Bash(rm -rf *)" {
		t.Errorf("RuleDetail = %q, want Bash(rm -rf *)", pm.event.RuleDetail)
	}
}

func TestConvertEventToMsg_PermissionAsk_NilPermissionAsk(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:         types.EventPermissionAsk,
		PermissionAsk: nil,
	})
	if msg != nil {
		t.Errorf("nil PermissionAsk should return nil, got %T", msg)
	}
}

func TestTUIHandler_PermissionAsk_DeliveredToChannel(t *testing.T) {
	h := NewTUIHandler()
	ch := make(chan types.PermissionUserDecision, 1)
	h.Handle(types.QueryEvent{
		Type: types.EventPermissionAsk,
		PermissionAsk: &types.PermissionAskEvent{
			ToolName:   "Write",
			Input:      json.RawMessage(`{"file_path":"test.go"}`),
			Message:    "write permission",
			ResponseCh: ch,
		},
	})

	// Should be delivered immediately (buffer has room)
	select {
	case msg := <-h.appCh:
		pm, ok := msg.(permissionAskMsg)
		if !ok {
			t.Fatalf("expected permissionAskMsg, got %T", msg)
		}
		if pm.event.ToolName != "Write" {
			t.Errorf("ToolName = %q, want Write", pm.event.ToolName)
		}
	default:
		t.Fatal("permission ask event should be delivered to appCh")
	}

	// Should NOT be counted as dropped
	if h.Dropped() != 0 {
		t.Errorf("Dropped = %d, want 0", h.Dropped())
	}
}

func TestTUIHandler_PermissionAsk_TimeoutAutoDeny(t *testing.T) {
	h := NewTUIHandler()
	// Fill the buffer completely so the blocking write cannot succeed
	for range cap(h.appCh) {
		h.appCh <- textDeltaMsg{Text: "fill"}
	}

	ch := make(chan types.PermissionUserDecision, 1)
	done := make(chan struct{})
	go func() {
		h.Handle(types.QueryEvent{
			Type: types.EventPermissionAsk,
			PermissionAsk: &types.PermissionAskEvent{
				ToolName:   "Bash",
				Input:      json.RawMessage(`{"command":"ls"}`),
				Message:    "test",
				ResponseCh: ch,
			},
		})
		close(done)
	}()

	// Wait for timeout + auto-deny (5s timeout)
	select {
	case d := <-ch:
		if d != types.UserDecisionDeny {
			t.Errorf("auto-deny decision = %q, want deny", d)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for auto-deny")
	}
	<-done // ensure Handle returns
}

// ---------------------------------------------------------------------------
// Integration: Hub EventQueryEnd must not produce queryEndMsg
// ---------------------------------------------------------------------------

// TestIntegration_HubEventQueryEndNoQueryEndMsg simulates the full cancel flow:
// engine aborts → hub dispatches EventQueryEnd → TUI handler receives it.
// The handler converts EventQueryEnd to queryEndMsg, which is the sole
// completion signal now that resultCh has been removed.
func TestIntegration_HubEventQueryEndNoQueryEndMsg(t *testing.T) {
	h := hub.NewHub()
	handler := NewTUIHandler()
	h.Subscribe(handler)

	// Simulate engine abort: hub dispatches EventQueryEnd
	h.Dispatch(hub.Event{Type: types.EventQueryEnd})

	// Drain appCh with timeout — should find queryEndMsg (sole completion signal)
	// Poll until we get the event or timeout
	found := false
	timeout := time.After(2 * time.Second)
	for !found {
		select {
		case msg := <-handler.appCh:
			if _, ok := msg.(queryEndMsg); ok {
				found = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for queryEndMsg from hub dispatch")
		}
	}
	if !found {
		t.Error("appCh should contain queryEndMsg from hub's EventQueryEnd")
	}

	// Verify nothing else remains in the channel
	select {
	case <-handler.appCh:
		t.Error("appCh should be empty after consuming queryEndMsg")
	default:
	}
}

// ---------------------------------------------------------------------------
// Benchmarks: measure event throughput (engine → TUIHandler → channel)
// ---------------------------------------------------------------------------

// BenchmarkTUIHandler_TextDelta_Throughput measures raw event throughput:
// how many text_delta events/sec can flow through TUIHandler when the
// consumer drains continuously. With coalescing, text accumulates and
// flushes as batched messages (every 512 bytes).
func BenchmarkTUIHandler_TextDelta_Throughput(b *testing.B) {
	handler := NewTUIHandler()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-handler.appCh:
			case <-done:
				return
			}
		}
	}()

	b.ResetTimer()
	for range b.N {
		handler.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
	}
	b.StopTimer()
	handler.Flush()
	close(done)
}

// NOTE: BufferFull benchmark removed — with blocking writes (no timeout),
// a full channel causes Handle to block indefinitely, which is the intended
// backpressure behavior. This cannot be benchmarked without a consumer.

// BenchmarkTUIHandler_MixedEvents simulates realistic streaming:
// 60% text_delta + 10% thinking_delta + 30% other events.
// Non-streaming events trigger flush of accumulated text/thinking.
func BenchmarkTUIHandler_MixedEvents(b *testing.B) {
	handler := NewTUIHandler()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-handler.appCh:
			case <-done:
				return
			}
		}
	}()

	events := []types.QueryEvent{
		{Type: types.EventTextDelta, Text: "hello world"},
		{Type: types.EventTextDelta, Text: "more text"},
		{Type: types.EventTextDelta, Text: "继续"},
		{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 100, OutputTokens: 50}},
		{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Read", Summary: "main.go"}},
		{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t1", DisplayOutput: "package main", Timing: time.Millisecond}},
		{Type: types.EventTextDelta, Text: "result: "},
		{Type: types.EventTextDelta, Text: "done"},
		{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "hmm"}},
		{Type: types.EventTextDelta, Text: "final"},
	}

	b.ResetTimer()
	for i := range b.N {
		handler.Handle(events[i%len(events)])
	}
	b.StopTimer()
	handler.Flush()
	close(done)
}

// BenchmarkTUIHandler_CoalescedBatch measures the effective message rate
// after coalescing: how many batched messages/sec reach the channel.
// This is the metric that matters for bubbletea Update() call pressure.
func BenchmarkTUIHandler_CoalescedBatch(b *testing.B) {
	handler := NewTUIHandler()

	msgCount := int64(0)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-handler.appCh:
				msgCount++
			case <-done:
				return
			}
		}
	}()

	b.ResetTimer()
	for range b.N {
		handler.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
	}
	handler.Flush()
	b.StopTimer()
	close(done)

	// Report the ratio: how many channel messages vs Handle() calls.
	// Without coalescing this would be ~1.0. With coalescing it should be
	// ~1/512 = 0.002 (one channel message per 512 Handle calls).
	if b.N > 0 {
		ratio := float64(msgCount) / float64(b.N)
		b.ReportMetric(ratio, "msg/call")
	}
}
