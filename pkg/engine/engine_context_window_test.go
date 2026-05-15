package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// textEventsWithStopReason creates stream events for a text response with a
// custom stop_reason.
func textEventsWithStopReason(model, text, stopReason string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: stopReason}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
}

// recoveryCompactor succeeds and returns reduced AfterTokens.
type recoveryCompactor struct{}

func (c *recoveryCompactor) Compact(_ context.Context, messages []types.Message) (*CompactResult, error) {
	if len(messages) == 0 {
		return &CompactResult{AfterTokens: 0, Messages: messages}, nil
	}
	return &CompactResult{
		BeforeTokens: len(messages) * 100,
		AfterTokens:  len(messages) * 50,
		Messages:     messages[:1],
	}, nil
}

// failingRecoveryCompactor always returns an error.
type failingRecoveryCompactor struct{ err string }

func (c *failingRecoveryCompactor) Compact(_ context.Context, _ []types.Message) (*CompactResult, error) {
	return nil, &compactTestError{msg: c.err}
}

type compactTestError struct{ msg string }

func (e *compactTestError) Error() string { return e.msg }

// TestContextWindowExceeded_CompactAndContinue verifies that when the API
// returns stop_reason="model_context_window_exceeded", the engine compacts,
// appends a continuation meta message, and continues the next turn.
func TestContextWindowExceeded_CompactAndContinue(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textEventsWithStopReason("test", "This is a long response that got...", stopReasonContextWindowExceeded), nil)
	mp.addResponse(subTextEvents("test", "...continued successfully!"), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	eng.SetCompactor(&recoveryCompactor{}, AutoCompactConfig{
		ContextWindow: 100000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "tell me a story", nil)
	if result.Error != nil {
		t.Fatalf("expected success, got: %v", result.Error)
	}

	// Verify compact events were emitted (start + run + end).
	toolStartEvents := tc.FindEvents(types.EventToolStart)
	compactCount := 0
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil && evt.ToolUse.Name == "Compact" {
			compactCount++
			if !strings.Contains(evt.ToolUse.Summary, "Compacting to continue") {
				t.Errorf("expected compact summary about continuing, got %q", evt.ToolUse.Summary)
			}
		}
	}
	if compactCount != 1 {
		t.Fatalf("expected 1 compact start event, got %d", compactCount)
	}

	// Verify compact end event shows success (not error).
	toolEndEvents := tc.FindEvents(types.EventToolEnd)
	var foundCompactEnd bool
	for _, evt := range toolEndEvents {
		if evt.ToolResult != nil && strings.Contains(evt.ToolResult.ToolUseID, "compact-recovery-") {
			foundCompactEnd = true
			if evt.ToolResult.IsError {
				t.Error("compact end should not be an error")
			}
			if !strings.Contains(evt.ToolResult.DisplayOutput, "compacted") {
				t.Errorf("expected 'compacted' in display output, got %q", evt.ToolResult.DisplayOutput)
			}
		}
	}
	if !foundCompactEnd {
		t.Error("expected compact end event with 'Context compacted' message")
	}

	// Verify continuation meta message was injected.
	var foundContinuation bool
	for _, msg := range result.Messages {
		if msg.Role == types.RoleUser && msg.Flags&types.FlagMeta != 0 {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, "Output token limit hit") {
					foundContinuation = true
				}
			}
		}
	}
	if !foundContinuation {
		t.Error("expected continuation meta message with 'Output token limit hit'")
	}

	// Verify the final response contains the continuation text.
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected last message role assistant, got %s", lastMsg.Role)
	}
	if !strings.Contains(lastMsg.Content[0].Text, "continued successfully") {
		t.Errorf("expected 'continued successfully' in final response, got %q", lastMsg.Content[0].Text)
	}
}

// TestContextWindowExceeded_RecoveryLimit verifies that recovery is limited to
// maxTokensRecoveryLimit (3) attempts. On the 4th overflow, the response is
// returned as-is without recovery.
func TestContextWindowExceeded_RecoveryLimit(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	for i := range maxTokensRecoveryLimit {
		mp.addResponse(textEventsWithStopReason("test", "truncated "+string(rune('A'+i)), stopReasonContextWindowExceeded), nil)
	}
	// 4th response: limit reached, falls through to terminal.
	mp.addResponse(textEventsWithStopReason("test", "final truncated D", stopReasonContextWindowExceeded), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	eng.SetCompactor(&recoveryCompactor{}, AutoCompactConfig{
		ContextWindow: 100000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("expected no error, got: %v", result.Error)
	}

	// Verify exactly maxTokensRecoveryLimit compact events.
	toolStartEvents := tc.FindEvents(types.EventToolStart)
	compactCount := 0
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil && evt.ToolUse.Name == "Compact" {
			compactCount++
		}
	}
	if compactCount != maxTokensRecoveryLimit {
		t.Errorf("expected %d compact events, got %d", maxTokensRecoveryLimit, compactCount)
	}

	// Verify the last assistant message is the 4th truncated response.
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected assistant message, got %s", lastMsg.Role)
	}
	if !strings.Contains(lastMsg.Content[0].Text, "final truncated D") {
		t.Errorf("expected 'final truncated D' in last message, got %q", lastMsg.Content[0].Text)
	}

	// Verify EventQueryEnd was emitted (terminal path reached).
	queryEndEvents := tc.FindEvents(types.EventQueryEnd)
	if len(queryEndEvents) != 1 {
		t.Fatalf("expected 1 EventQueryEnd, got %d", len(queryEndEvents))
	}
}

// TestContextWindowExceeded_SubAgentGuard verifies that sub-agents with
// agentType="compact" do NOT trigger stop-reason recovery, preventing
// infinite recursion.
func TestContextWindowExceeded_SubAgentGuard(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textEventsWithStopReason("test", "truncated response", stopReasonContextWindowExceeded), nil)

	tc := newEventCollector()
	parent := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	parent.SetCompactor(&recoveryCompactor{}, AutoCompactConfig{
		ContextWindow: 100000,
	})

	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "compact",
		Tools:     map[string]tool.Tool{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := sub.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("expected no error, got: %v", result.Error)
	}

	// Verify NO compact events were emitted.
	toolStartEvents := tc.FindEvents(types.EventToolStart)
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil && evt.ToolUse.Name == "Compact" {
			t.Error("sub-agent should not trigger compact recovery")
		}
	}

	// Verify no continuation meta message.
	for _, msg := range result.Messages {
		if msg.Role == types.RoleUser && msg.Flags&types.FlagMeta != 0 {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, "Output token limit hit") {
					t.Error("sub-agent should not produce continuation message")
				}
			}
		}
	}

	// Verify the truncated response was returned as-is.
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected assistant message, got %s", lastMsg.Role)
	}
	if !strings.Contains(lastMsg.Content[0].Text, "truncated response") {
		t.Errorf("expected 'truncated response', got %q", lastMsg.Content[0].Text)
	}
}

// TestContextWindowExceeded_CompactFails verifies that when compact fails
// during recovery, the truncated response is returned as-is without continuation.
func TestContextWindowExceeded_CompactFails(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textEventsWithStopReason("test", "truncated output", stopReasonContextWindowExceeded), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	eng.SetCompactor(&failingRecoveryCompactor{err: "disk full"}, AutoCompactConfig{
		ContextWindow: 100000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("expected no error, got: %v", result.Error)
	}

	// Verify compact error event was emitted.
	toolEndEvents := tc.FindEvents(types.EventToolEnd)
	var foundCompactError bool
	for _, evt := range toolEndEvents {
		if evt.ToolResult != nil && evt.ToolResult.IsError && strings.Contains(evt.ToolResult.DisplayOutput, "Compact failed") {
			foundCompactError = true
			if !strings.Contains(evt.ToolResult.DisplayOutput, "disk full") {
				t.Errorf("expected error to mention 'disk full', got %q", evt.ToolResult.DisplayOutput)
			}
		}
	}
	if !foundCompactError {
		t.Error("expected compact error event with 'Compact failed: disk full'")
	}

	// Verify NO continuation meta message.
	for _, msg := range result.Messages {
		if msg.Role == types.RoleUser && msg.Flags&types.FlagMeta != 0 {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, "Output token limit hit") {
					t.Error("compact failure should not produce continuation message")
				}
			}
		}
	}

	// Verify the truncated assistant response was still returned.
	var foundAssistant bool
	for _, msg := range result.Messages {
		if msg.Role == types.RoleAssistant {
			foundAssistant = true
			if !strings.Contains(msg.Content[0].Text, "truncated output") {
				t.Errorf("expected 'truncated output', got %q", msg.Content[0].Text)
			}
		}
	}
	if !foundAssistant {
		t.Error("expected assistant message with truncated response")
	}
}

// TestMaxTokens_ContinueWithoutCompact verifies that stop_reason="max_tokens"
// triggers continuation WITHOUT compacting (context still has room).
func TestMaxTokens_ContinueWithoutCompact(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textEventsWithStopReason("test", "This response was cut off at max", stopReasonMaxTokens), nil)
	mp.addResponse(subTextEvents("test", "...and here is the rest!"), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "tell me a joke", nil)
	if result.Error != nil {
		t.Fatalf("expected success, got: %v", result.Error)
	}

	// Verify NO compact events (max_tokens doesn't compact).
	toolStartEvents := tc.FindEvents(types.EventToolStart)
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil && evt.ToolUse.Name == "Compact" {
			t.Error("max_tokens recovery should NOT trigger compact")
		}
	}

	// Verify continuation meta message exists.
	var foundContinuation bool
	for _, msg := range result.Messages {
		if msg.Role == types.RoleUser && msg.Flags&types.FlagMeta != 0 {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, "Output token limit hit") {
					foundContinuation = true
				}
			}
		}
	}
	if !foundContinuation {
		t.Error("expected continuation meta message for max_tokens")
	}

	// Verify the final response has the continuation text.
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected assistant message, got %s", lastMsg.Role)
	}
	if !strings.Contains(lastMsg.Content[0].Text, "here is the rest") {
		t.Errorf("expected 'here is the rest' in final response, got %q", lastMsg.Content[0].Text)
	}

	// Verify at least 2 TurnEnd events (recovery turn + final turn).
	turnEndEvents := tc.FindEvents(types.EventTurnEnd)
	if len(turnEndEvents) < 2 {
		t.Errorf("expected at least 2 EventTurnEnd, got %d", len(turnEndEvents))
	}
}

// TestContextWindowExceeded_WithToolUse verifies that recovery does NOT trigger
// when the response contains tool_use blocks (streamingExecutor != nil).
func TestContextWindowExceeded_WithToolUse(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: "Let me check..."}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "Let me check..."}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "test"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: stopReasonContextWindowExceeded}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(subTextEvents("test", "Done after tool."), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Tools:      []tool.Tool{&minimalTool{}},
		Model:      "test",
		Dispatcher: tc,
	})
	eng.SetCompactor(&recoveryCompactor{}, AutoCompactConfig{
		ContextWindow: 100000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("expected success, got: %v", result.Error)
	}

	// Verify NO compact events (tool_use path, not recovery path).
	toolStartEvents := tc.FindEvents(types.EventToolStart)
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil && evt.ToolUse.Name == "Compact" {
			t.Error("tool_use response should not trigger compact recovery")
		}
	}

	// Verify no continuation meta message.
	for _, msg := range result.Messages {
		if msg.Role == types.RoleUser && msg.Flags&types.FlagMeta != 0 {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, "Output token limit hit") {
					t.Error("tool_use response should not produce continuation message")
				}
			}
		}
	}

	// Verify tool execution happened.
	toolRunEvents := tc.FindEvents(types.EventToolRun)
	if len(toolRunEvents) == 0 {
		t.Error("expected tool execution events")
	}

	// Verify final response.
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected assistant message, got %s", lastMsg.Role)
	}
	if !strings.Contains(lastMsg.Content[0].Text, "Done after tool") {
		t.Errorf("expected 'Done after tool', got %q", lastMsg.Content[0].Text)
	}
}
