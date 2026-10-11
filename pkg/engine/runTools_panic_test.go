package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Panic recovery tests — verifies tool panics produce valid tool results
// ---------------------------------------------------------------------------

// panicTool implements tool.Tool whose Call method panics.
type panicTool struct {
	name string
}

func (p *panicTool) Name() string                                { return p.name }
func (p *panicTool) Aliases() []string                           { return nil }
func (p *panicTool) Description(json.RawMessage) (string, error) { return p.name + " desc", nil }
func (p *panicTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (p *panicTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	panic("test panic in tool")
}
func (p *panicTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (p *panicTool) IsReadOnly(json.RawMessage) bool           { return true }
func (p *panicTool) IsDestructive(json.RawMessage) bool        { return false }
func (p *panicTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (p *panicTool) IsEnabled() bool                           { return true }
func (p *panicTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (p *panicTool) Prompt() string                            { return "" }
func (p *panicTool) MaxResultSize() int                        { return 50000 }
func (p *panicTool) RenderResult(any) string                   { return "" }

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
	var content []types.ContentBlock
	if err := json.Unmarshal(rb.Content, &content); err != nil {
		t.Fatalf("content is not a JSON array: %v", err)
	}
	if len(content) != 1 || content[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", content)
	}
	text := content[0].Text
	if !strings.Contains(text, "internal error") {
		t.Errorf("error content should mention 'internal error', got: %q", text)
	}
	if !strings.Contains(text, "test panic in tool") {
		t.Errorf("error content should contain panic message, got: %q", text)
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
// Nil interface trap tests — verifies that a nil *Checker passed as
// PermissionChecker interface denies the tool call rather than crashing
// the executor or running the tool unchecked.
// This is the classic Go nil interface trap:
//   var p *Checker // nil
//   var i PermissionChecker = p // non-nil interface wrapping nil pointer
//   i.Check(...) // panic!
// ---------------------------------------------------------------------------

// TestSetPermissionChecker_NilInterfaceTrap tests that passing a nil *Checker
// as a PermissionChecker interface denies the tool call instead of crashing
// the executor or letting the tool run unchecked.
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

	// The broken checker must fail secure: the call is denied, not executed.
	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(result.ToolResultBlocks))
	}
	rb := result.ToolResultBlocks[0]
	if rb.ToolUseID != "tu_1" {
		t.Errorf("ToolUseID = %q, want tu_1", rb.ToolUseID)
	}
	if !rb.IsError {
		t.Fatalf("nil-interface-trap checker must deny, got non-error: %s", rb.Content)
	}
	assertDenyText(t, rb.Content, "echo")
}

// TestSetPermissionChecker_NilInterfaceTrap_SecondTool verifies that a nil
// interface trap checker keeps denying across turns — the panic must not
// uninstall the checker and leave later turns unpoliced.
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
	if !result1.ToolResultBlocks[0].IsError {
		t.Fatalf("first call must be denied, got non-error: %s", result1.ToolResultBlocks[0].Content)
	}

	// Second tool call (simulates LLM's next turn) — still denied
	blocks2 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	executor2 := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor2.SetPermissionChecker(trap)
	result2 := executor2.ExecuteAll(blocks2)

	if len(result2.ToolResultBlocks) != 1 {
		t.Fatalf("second call: expected 1 result, got %d", len(result2.ToolResultBlocks))
	}
	if !result2.ToolResultBlocks[0].IsError {
		t.Fatalf("second call must be denied, got non-error: %s", result2.ToolResultBlocks[0].Content)
	}
	assertDenyText(t, result2.ToolResultBlocks[0].Content, "echo")
}

// TestConcurrentToolLoop_PanicRecovery_NextToolSucceeds verifies that
// after a tool panics and returns an error result, the LLM can make a
// new tool call that succeeds. This simulates the real flow:
//
//	Turn 1: LLM calls tool A → tool A panics → recovery returns error
//	Turn 2: LLM calls tool B → tool B executes normally
//
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
	var errMsg1 []types.ContentBlock
	if err := json.Unmarshal(rb1.Content, &errMsg1); err != nil {
		t.Fatalf("turn 1: content is not JSON array: %v", err)
	}
	if len(errMsg1) != 1 || errMsg1[0].Type != types.ContentTypeText {
		t.Fatalf("turn 1: expected single text block, got %+v", errMsg1)
	}
	if !strings.Contains(errMsg1[0].Text, "internal error") {
		t.Errorf("turn 1: error should mention 'internal error', got: %q", errMsg1[0].Text)
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
		if err := json.Unmarshal(rb2.Content, &errMsg2); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
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

// assertDenyText checks a tool_result carries the rule-deny message for name.
func assertDenyText(t *testing.T, content json.RawMessage, name string) {
	t.Helper()
	var blocks []types.ContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		t.Fatalf("content is not a JSON array: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", blocks)
	}
	want := fmt.Sprintf("Permission to use %s has been denied", name)
	if !strings.Contains(blocks[0].Text, want) {
		t.Errorf("expected %q, got %q", want, blocks[0].Text)
	}
}

// TestBrokenPermChecker_DeniesTool verifies that a broken permChecker (nil
// interface trap) denies the tool call on every turn rather than letting it
// run unchecked.
func TestBrokenPermChecker_DeniesTool(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}

	// Create nil interface trap: nil *Checker as PermissionChecker interface
	var nilChecker *permission.Checker
	var trap permission.PermissionChecker = nilChecker

	// Turn 1: tool must be DENIED by the broken permChecker
	blocks1 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	executor1 := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor1.SetPermissionChecker(trap)
	result1 := executor1.ExecuteAll(blocks1)

	if len(result1.ToolResultBlocks) != 1 {
		t.Fatalf("turn 1: expected 1 result, got %d", len(result1.ToolResultBlocks))
	}
	if !result1.ToolResultBlocks[0].IsError {
		t.Fatalf("turn 1: broken permChecker must deny, got non-error: %s", result1.ToolResultBlocks[0].Content)
	}
	assertDenyText(t, result1.ToolResultBlocks[0].Content, "echo")

	// Turn 2: same broken permChecker, different executor — also denied
	blocks2 := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	executor2 := NewStreamingToolExecutor(tools, nil, func(evt types.QueryEvent) {}, context.Background())
	executor2.SetPermissionChecker(trap)
	result2 := executor2.ExecuteAll(blocks2)

	if len(result2.ToolResultBlocks) != 1 {
		t.Fatalf("turn 2: expected 1 result, got %d", len(result2.ToolResultBlocks))
	}
	if !result2.ToolResultBlocks[0].IsError {
		t.Fatalf("turn 2: broken permChecker must deny, got non-error: %s", result2.ToolResultBlocks[0].Content)
	}
	assertDenyText(t, result2.ToolResultBlocks[0].Content, "echo")
}

// ---------------------------------------------------------------------------
// Permission-checker panic must fail secure: the tool call is denied and the
// checker stays installed, so enforcement survives a parse-level panic.
// ---------------------------------------------------------------------------

// countingPanickingChecker mimics the nil-interface trap: every Check panics.
// The counter proves whether later calls still consult the checker.
type countingPanickingChecker struct {
	mu    sync.Mutex
	calls int
}

func (c *countingPanickingChecker) Check(_ string, _ json.RawMessage) permission.Decision {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	panic("permission checker exploded")
}

func (c *countingPanickingChecker) HasRules() bool { return true }

func (c *countingPanickingChecker) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestPermCheckerPanic_DeniesAndKeepsChecker(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}

	checker := &countingPanickingChecker{}
	executor := NewStreamingToolExecutor(tools, nil, func(types.QueryEvent) {}, context.Background())
	executor.SetPermissionChecker(checker)

	result1 := executor.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	})

	if len(result1.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(result1.ToolResultBlocks))
	}
	rb := result1.ToolResultBlocks[0]
	if !rb.IsError {
		t.Fatalf("panicking permission check must deny, got non-error result: %s", rb.Content)
	}
	var content []types.ContentBlock
	if err := json.Unmarshal(rb.Content, &content); err != nil {
		t.Fatalf("unmarshal deny content: %v", err)
	}
	if len(content) != 1 || content[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", content)
	}
	if !strings.Contains(content[0].Text, "Permission to use echo has been denied") {
		t.Errorf("expected deny message, got %q", content[0].Text)
	}
	if checker.callCount() != 1 {
		t.Errorf("checker should have been consulted once, got %d calls", checker.callCount())
	}

	// The checker must survive the panic: a later call on the same executor
	// still goes through it, so enforcement is not silently disabled.
	if executor.permChecker == nil {
		t.Fatal("permChecker must not be nilled by a panicking check")
	}
	result2 := executor.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "echo", Input: json.RawMessage(`{}`)},
	})
	if len(result2.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 result blocks, got %d", len(result2.ToolResultBlocks))
	}
	if !result2.ToolResultBlocks[1].IsError {
		t.Errorf("second call must also be denied, got non-error: %s", result2.ToolResultBlocks[1].Content)
	}
	if checker.callCount() != 2 {
		t.Errorf("checker must still be consulted after a panic, got %d calls", checker.callCount())
	}
}
