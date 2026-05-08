package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Panic recovery tests — verifies tool panics produce valid tool results
// ---------------------------------------------------------------------------

// panicTool implements tool.Tool whose Call method panics.
type panicTool struct {
	name string
}

func (p *panicTool) Name() string                                                { return p.name }
func (p *panicTool) Aliases() []string                                           { return nil }
func (p *panicTool) Description(json.RawMessage) (string, error)                 { return p.name + " desc", nil }
func (p *panicTool) InputSchema() json.RawMessage                                { return json.RawMessage(`{}`) }
func (p *panicTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	panic("test panic in tool")
}
func (p *panicTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (p *panicTool) IsReadOnly(json.RawMessage) bool            { return true }
func (p *panicTool) IsDestructive(json.RawMessage) bool         { return false }
func (p *panicTool) IsConcurrencySafe(json.RawMessage) bool     { return true }
func (p *panicTool) IsEnabled() bool                            { return true }
func (p *panicTool) InterruptBehavior() tool.InterruptBehavior  { return tool.InterruptCancel }
func (p *panicTool) Prompt() string                             { return "" }
func (p *panicTool) MaxResultSize() int                         { return 50000 }
func (p *panicTool) RenderResult(any) string                    { return "" }

// TestConcurrentToolLoop_PanicRecovery_ToolResult verifies that a tool which
// panics produces a valid error tool_result (not a crash). This tests the
// critical path: if the LLM sends a tool_use, the engine MUST return a
// matching tool_result, even if the tool panics internally.
func TestConcurrentToolLoop_PanicRecovery_ToolResult(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"panicTool": &panicTool{name: "panicTool"},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_panic", Name: "panicTool", Input: json.RawMessage(`{}`)},
	}

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	// Must have exactly 1 result block matching the tool_use
	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(result.ToolResultBlocks))
	}
	rb := result.ToolResultBlocks[0]
	if rb.ToolUseID != "tu_panic" {
		t.Errorf("ToolUseID = %q, want %q", rb.ToolUseID, "tu_panic")
	}
	if rb.Type != types.ContentTypeToolResult {
		t.Errorf("Type = %q, want ContentTypeToolResult", rb.Type)
	}
	if !rb.IsError {
		t.Error("expected IsError=true for panicked tool")
	}
	// Content must contain the panic message so the LLM can understand what happened
	var content string
	if err := json.Unmarshal(rb.Content, &content); err != nil {
		t.Fatalf("content is not a JSON string: %v", err)
	}
	if !strings.Contains(content, "internal error") {
		t.Errorf("error content should mention 'internal error', got: %q", content)
	}
	if !strings.Contains(content, "test panic in tool") {
		t.Errorf("error content should contain panic message, got: %q", content)
	}
}

// TestConcurrentToolLoop_PanicRecovery_EmitsToolEnd verifies that a ToolEnd
// event is emitted when a tool panics, so the TUI can display the error.
func TestConcurrentToolLoop_PanicRecovery_EmitsToolEnd(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"panicTool": &panicTool{name: "panicTool"},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_panic", Name: "panicTool", Input: json.RawMessage(`{}`)},
	}

	var events []types.QueryEvent
	ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	// Must emit a ToolEnd event
	var toolEnd *types.ToolResultEvent
	for i := range events {
		if events[i].Type == types.EventToolEnd && events[i].ToolResult != nil {
			toolEnd = events[i].ToolResult
			break
		}
	}
	if toolEnd == nil {
		t.Fatal("expected ToolEnd event with ToolResult")
	}
	if toolEnd.ToolUseID != "tu_panic" {
		t.Errorf("ToolUseID = %q, want %q", toolEnd.ToolUseID, "tu_panic")
	}
	if !toolEnd.IsError {
		t.Error("expected IsError=true in ToolEnd event")
	}
}

// TestConcurrentToolLoop_PanicRecovery_DoesNotCrashOtherTools verifies that
// a panicking tool doesn't prevent other tools from completing.
func TestConcurrentToolLoop_PanicRecovery_DoesNotCrashOtherTools(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"panicTool": &panicTool{name: "panicTool"},
		"echo":      &concurrentTool{name: "echo", isSafe: true},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "panicTool", Input: json.RawMessage(`{}`)},
	}

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	// Both tools must produce results
	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 result blocks, got %d", len(result.ToolResultBlocks))
	}
	// Results must be in order: echo (ok), panicTool (error)
	if result.ToolResultBlocks[0].ToolUseID != "tu_1" {
		t.Errorf("first result ToolUseID = %q, want tu_1", result.ToolResultBlocks[0].ToolUseID)
	}
	if result.ToolResultBlocks[0].IsError {
		t.Error("first tool (echo) should not be an error")
	}
	if result.ToolResultBlocks[1].ToolUseID != "tu_2" {
		t.Errorf("second result ToolUseID = %q, want tu_2", result.ToolResultBlocks[1].ToolUseID)
	}
	if !result.ToolResultBlocks[1].IsError {
		t.Error("second tool (panicTool) should be an error")
	}
}

// ---------------------------------------------------------------------------
// Nil interface trap test — verifies that a nil *Checker passed as
// PermissionChecker interface doesn't break tool execution.
// This is the classic Go nil interface trap:
//   var p *Checker // nil
//   var i PermissionChecker = p // non-nil interface wrapping nil pointer
//   i.Check(...) // panic!
// ---------------------------------------------------------------------------

// TestSetPermissionChecker_NilInterfaceTrap tests that passing a nil *Checker
// as a PermissionChecker interface doesn't cause tool panics.
// This verifies the fix at the executor level, independent of main.go.
func TestSetPermissionChecker_NilInterfaceTrap(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}

	// Create a nil *permission.Checker wrapped as PermissionChecker interface.
	// This is the Go nil interface trap: interface value is non-nil
	// (has type info) but underlying pointer is nil.
	var nilChecker *permission.Checker
	var trap permission.PermissionChecker = nilChecker

	executor := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor.SetPermissionChecker(trap)

	result := executor.ExecuteAll(blocks)

	// Tool must execute successfully — permChecker nil trap should be handled.
	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(result.ToolResultBlocks))
	}
	rb := result.ToolResultBlocks[0]
	if rb.ToolUseID != "tu_1" {
		t.Errorf("ToolUseID = %q, want tu_1", rb.ToolUseID)
	}
	if rb.IsError {
		var errMsg string
		if err := json.Unmarshal(rb.Content, &errMsg); err != nil { t.Fatalf("unmarshal: %v", err) }
		t.Errorf("tool should succeed (broken permChecker should be ignored), got error: %q", errMsg)
	}
}

// TestSetPermissionChecker_NilInterfaceTrap_SecondTool verifies that after
// a nil interface trap permChecker is set, multiple tool calls all succeed.
func TestSetPermissionChecker_NilInterfaceTrap_SecondTool(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}

	var nilChecker *permission.Checker
	var trap permission.PermissionChecker = nilChecker

	// First tool call
	blocks1 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	executor1 := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor1.SetPermissionChecker(trap)
	result1 := executor1.ExecuteAll(blocks1)

	if len(result1.ToolResultBlocks) != 1 {
		t.Fatalf("first call: expected 1 result, got %d", len(result1.ToolResultBlocks))
	}
	if result1.ToolResultBlocks[0].IsError {
		t.Error("first call should succeed")
	}

	// Second tool call (simulates LLM's next turn)
	blocks2 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	executor2 := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor2.SetPermissionChecker(trap)
	result2 := executor2.ExecuteAll(blocks2)

	if len(result2.ToolResultBlocks) != 1 {
		t.Fatalf("second call: expected 1 result, got %d", len(result2.ToolResultBlocks))
	}
	if result2.ToolResultBlocks[0].IsError {
		t.Error("second call should succeed")
	}
}

// TestConcurrentToolLoop_PanicRecovery_NextToolSucceeds verifies that
// after a tool panics and returns an error result, the LLM can make a
// new tool call that succeeds. This simulates the real flow:
//   Turn 1: LLM calls tool A → tool A panics → recovery returns error
//   Turn 2: LLM calls tool B → tool B executes normally
// The panic in turn 1 must NOT corrupt system state or prevent turn 2.
func TestConcurrentToolLoop_PanicRecovery_NextToolSucceeds(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"panicTool": &panicTool{name: "panicTool"},
		"echo":      &concurrentTool{name: "echo", isSafe: true},
	}

	// ── Turn 1: tool panics ──
	blocks1 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "panicTool", Input: json.RawMessage(`{}`)},
	}
	var events1 []types.QueryEvent
	result1 := ConcurrentToolLoop(context.Background(), tools, blocks1, nil, func(evt types.QueryEvent) {
		events1 = append(events1, evt)
	})

	// Turn 1 must return an error tool_result (not crash)
	if len(result1.ToolResultBlocks) != 1 {
		t.Fatalf("turn 1: expected 1 result block, got %d", len(result1.ToolResultBlocks))
	}
	rb1 := result1.ToolResultBlocks[0]
	if rb1.ToolUseID != "tu_1" {
		t.Errorf("turn 1: ToolUseID = %q, want tu_1", rb1.ToolUseID)
	}
	if !rb1.IsError {
		t.Error("turn 1: expected IsError=true for panicked tool")
	}
	// Verify error message contains panic info
	var errMsg1 string
	if err := json.Unmarshal(rb1.Content, &errMsg1); err != nil {
		t.Fatalf("turn 1: content is not JSON string: %v", err)
	}
	if !strings.Contains(errMsg1, "internal error") {
		t.Errorf("turn 1: error should mention 'internal error', got: %q", errMsg1)
	}
	// Verify ToolEnd event was emitted
	var foundToolEnd bool
	for _, evt := range events1 {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_1" {
			foundToolEnd = true
			break
		}
	}
	if !foundToolEnd {
		t.Error("turn 1: expected ToolEnd event with ToolResult for tu_1")
	}

	// ── Turn 2: LLM tries a different tool — must succeed ──
	blocks2 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	var events2 []types.QueryEvent
	result2 := ConcurrentToolLoop(context.Background(), tools, blocks2, nil, func(evt types.QueryEvent) {
		events2 = append(events2, evt)
	})

	// Turn 2 must succeed — previous panic must not affect this call
	if len(result2.ToolResultBlocks) != 1 {
		t.Fatalf("turn 2: expected 1 result block, got %d", len(result2.ToolResultBlocks))
	}
	rb2 := result2.ToolResultBlocks[0]
	if rb2.ToolUseID != "tu_2" {
		t.Errorf("turn 2: ToolUseID = %q, want tu_2", rb2.ToolUseID)
	}
	if rb2.IsError {
		var errMsg2 string
		if err := json.Unmarshal(rb2.Content, &errMsg2); err != nil { t.Fatalf("unmarshal: %v", err) }
		t.Errorf("turn 2: tool should succeed (previous panic must not affect new call), got error: %q", errMsg2)
	}
	// Verify success ToolEnd event
	var foundToolEnd2 bool
	for _, evt := range events2 {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_2" {
			foundToolEnd2 = true
			if evt.ToolResult.IsError {
				t.Error("turn 2: ToolEnd event should not be error")
			}
			break
		}
	}
	if !foundToolEnd2 {
		t.Error("turn 2: expected ToolEnd event with ToolResult for tu_2")
	}
}

// TestBrokenPermChecker_ToolStillExecutes verifies that a broken
// permChecker (nil interface trap) does NOT prevent tools from executing.
// The permChecker is a system component — if it panics, the tool should
// still run (with the checker silently disabled), not return an error.
func TestBrokenPermChecker_ToolStillExecutes(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}

	// Create nil interface trap: nil *Checker as PermissionChecker interface
	var nilChecker *permission.Checker
	var trap permission.PermissionChecker = nilChecker

	// Turn 1: tool should SUCCEED despite broken permChecker
	blocks1 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	executor1 := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor1.SetPermissionChecker(trap)
	result1 := executor1.ExecuteAll(blocks1)

	if len(result1.ToolResultBlocks) != 1 {
		t.Fatalf("turn 1: expected 1 result, got %d", len(result1.ToolResultBlocks))
	}
	if result1.ToolResultBlocks[0].IsError {
		var msg string
		if err := json.Unmarshal(result1.ToolResultBlocks[0].Content, &msg); err != nil {
			t.Fatalf("turn 1: unmarshal error: %v", err)
		}
		t.Errorf("turn 1: tool should succeed (broken permChecker must be transparent), got error: %q", msg)
	}

	// Turn 2: same broken permChecker, different tool — should also succeed
	blocks2 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	executor2 := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor2.SetPermissionChecker(trap)
	result2 := executor2.ExecuteAll(blocks2)

	if len(result2.ToolResultBlocks) != 1 {
		t.Fatalf("turn 2: expected 1 result, got %d", len(result2.ToolResultBlocks))
	}
	if result2.ToolResultBlocks[0].IsError {
		var msg string
		if err := json.Unmarshal(result2.ToolResultBlocks[0].Content, &msg); err != nil {
			t.Fatalf("turn 2: unmarshal error: %v", err)
		}
		t.Errorf("turn 2: tool should succeed, got error: %q", msg)
	}
}
