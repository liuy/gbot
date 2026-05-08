package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func TestNewAbortController(t *testing.T) {
	t.Parallel()
	ac := NewAbortController(context.Background())
	if ac == nil {
		t.Fatal("expected non-nil controller")
	}
	if ac.Context() == nil {
		t.Fatal("expected non-nil context")
	}
	if ac.Reason() != "" {
		t.Errorf("expected empty reason, got %q", ac.Reason())
	}
}

func TestAbortController_Abort(t *testing.T) {
	ac := NewAbortController(context.Background())
	ac.Abort("user interrupt")

	if ac.Reason() != "user interrupt" {
		t.Errorf("expected reason 'user interrupt', got %q", ac.Reason())
	}

	ctx := ac.Context()
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(time.Second):
		t.Fatal("expected context to be done")
	}
}

func TestAbortController_ParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ac := NewAbortController(parent)

	cancel()

	ctx := ac.Context()
	select {
	case <-ctx.Done():
		// Expected: child inherits parent cancellation
	case <-time.After(time.Second):
		t.Fatal("expected child context to be cancelled when parent cancels")
	}
	// Verify the context error is "context canceled" (not a deadline exceeded).
	if ctx.Err() != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", ctx.Err())
	}
	// Reason stays empty because we cancelled the parent, not ac.Abort().
	if ac.Reason() != "" {
		t.Errorf("expected empty reason on parent cancel, got %q", ac.Reason())
	}
}

func TestShouldInterruptTool_NoAbort(t *testing.T) {
	ctx := context.Background()
	// Both InterruptCancel (0) and InterruptBlock (1) should return false when ctx is alive.
	if ShouldInterruptTool(0, ctx) {
		t.Error("expected false for InterruptCancel with live context")
	}
}

func TestShouldInterruptTool_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// InterruptCancel (0) should return true when context is cancelled
	if !ShouldInterruptTool(0, ctx) {
		t.Error("expected true for InterruptCancel with cancelled context")
	}
	// Verify the cancelled context actually reports an error.
	if ctx.Err() != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", ctx.Err())
	}
}

func TestShouldAbort_NoAbort(t *testing.T) {
	ctx := context.Background()
	if err := ShouldAbort(ctx, "streaming"); err != nil {
		t.Errorf("expected nil with live context, got: %v", err)
	}
}

func TestShouldAbort_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ShouldAbort(ctx, "streaming")
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}

	var ae *AbortError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AbortError, got %T", err)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}
	if !errors.Is(ae.Err, context.Canceled) {
		t.Errorf("underlying error = %v, want context.Canceled", ae.Err)
	}
}

// ---------------------------------------------------------------------------
// appendInlineInterruptMessage — synthetic tool_result for orphaned tool_use
// TS source: query.ts:1015-1029 — yieldMissingToolResultBlocks generates
// synthetic tool_result blocks for all tool_use blocks when abort fires.
// ---------------------------------------------------------------------------

// TestAppendInlineInterruptMessage_AddsSyntheticToolResults verifies that
// interrupting mid-tool-call adds synthetic tool_results for orphaned
// tool_use blocks, preventing API error "tool call result does not follow
// tool call (2013)".
func TestAppendInlineInterruptMessage_AddsSyntheticToolResults(t *testing.T) {
	eng := &Engine{
		logger: slog.Default(),
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
			{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					types.NewTextBlock("let me run a command"),
					{
						Type:  types.ContentTypeToolUse,
						ID:    "call_function_abc123_1",
						Name:  "Bash",
						Input: json.RawMessage(`{"command":"sleep 100"}`),
					},
				},
			},
		},
	}

	eng.appendInlineInterruptMessage()

	msgs := eng.Messages()

	// Should have 3 messages: user, assistant (with interrupt text), user (synthetic tool_result)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages after interrupt, got %d", len(msgs))
	}

	// Last message should be a user message with synthetic tool_result
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Role != types.RoleUser {
		t.Fatalf("expected last message role=user, got %s", lastMsg.Role)
	}

	// Should contain exactly 1 tool_result block
	toolResultCount := 0
	for _, block := range lastMsg.Content {
		if block.Type == types.ContentTypeToolResult {
			toolResultCount++
			if block.ToolUseID != "call_function_abc123_1" {
				t.Errorf("tool_result tool_use_id = %q, want call_function_abc123_1", block.ToolUseID)
			}
			if !block.IsError {
				t.Error("synthetic tool_result should have IsError=true")
			}
			var content string
			if err := json.Unmarshal(block.Content, &content); err != nil {
				t.Fatalf("failed to parse tool_result content: %v", err)
			}
			if !strings.Contains(content, "User rejected tool use") {
				t.Errorf("tool_result content = %q, want interrupt message", content)
			}
		}
	}
	if toolResultCount != 1 {
		t.Errorf("expected 1 tool_result block, got %d", toolResultCount)
	}
}

// TestAppendInlineInterruptMessage_NoToolUse_NoSyntheticResult verifies that
// interrupting a text-only response does NOT add synthetic tool_result.
func TestAppendInlineInterruptMessage_NoToolUse_NoSyntheticResult(t *testing.T) {
	eng := &Engine{
		logger: slog.Default(),
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
			{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					types.NewTextBlock("I'm thinking about this..."),
				},
			},
		},
	}

	eng.appendInlineInterruptMessage()

	msgs := eng.Messages()

	// Should still have 2 messages — no synthetic tool_result user message.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (no synthetic tool_result), got %d", len(msgs))
	}

	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected last message role=assistant, got %s", lastMsg.Role)
	}

	hasInterrupt := false
	for _, block := range lastMsg.Content {
		if block.Type == types.ContentTypeText && strings.Contains(block.Text, types.InterruptMessage) {
			hasInterrupt = true
		}
	}
	if !hasInterrupt {
		t.Error("assistant message should contain interrupt text")
	}
}

// TestAppendInlineInterruptMessage_MultipleToolUse_AllGetResults verifies
// that when multiple tool_use blocks exist, each gets a synthetic tool_result.
func TestAppendInlineInterruptMessage_MultipleToolUse_AllGetResults(t *testing.T) {
	eng := &Engine{
		logger: slog.Default(),
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("run parallel")}},
			{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Read", Input: json.RawMessage(`{"file_path":"/a.go"}`)},
					{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
					{Type: types.ContentTypeToolUse, ID: "tu_3", Name: "Grep", Input: json.RawMessage(`{"pattern":"todo"}`)},
				},
			},
		},
	}

	eng.appendInlineInterruptMessage()

	msgs := eng.Messages()

	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	lastMsg := msgs[len(msgs)-1]
	toolResultCount := 0
	seenIDs := map[string]bool{}
	for _, block := range lastMsg.Content {
		if block.Type == types.ContentTypeToolResult {
			toolResultCount++
			seenIDs[block.ToolUseID] = true
		}
	}
	if toolResultCount != 3 {
		t.Errorf("expected 3 synthetic tool_results, got %d", toolResultCount)
	}
	for _, id := range []string{"tu_1", "tu_2", "tu_3"} {
		if !seenIDs[id] {
			t.Errorf("missing synthetic tool_result for tool_use_id %q", id)
		}
	}
}

// TestAppendInlineInterruptMessage_SkipsExistingToolResults verifies that
// tool_use blocks that already have matching tool_results are NOT given
// duplicate synthetic results.
func TestAppendInlineInterruptMessage_SkipsExistingToolResults(t *testing.T) {
	eng := &Engine{
		logger: slog.Default(),
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
			{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolUse, ID: "tu_done", Name: "Read", Input: json.RawMessage(`{"file_path":"/a.go"}`)},
					{Type: types.ContentTypeToolUse, ID: "tu_pending", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
				},
			},
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewToolResultBlock("tu_done", json.RawMessage(`"file contents"`), false),
				},
			},
		},
	}

	eng.appendInlineInterruptMessage()

	msgs := eng.Messages()

	// Should have 4: user, assistant, tool_result for tu_done, synthetic for tu_pending
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}

	lastMsg := msgs[len(msgs)-1]
	toolResultCount := 0
	for _, block := range lastMsg.Content {
		if block.Type == types.ContentTypeToolResult {
			toolResultCount++
			if block.ToolUseID != "tu_pending" {
				t.Errorf("expected synthetic result only for tu_pending, got tool_use_id=%q", block.ToolUseID)
			}
		}
	}
	if toolResultCount != 1 {
		t.Errorf("expected 1 synthetic tool_result (for tu_pending only), got %d", toolResultCount)
	}
}

