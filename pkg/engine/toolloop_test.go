package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// testTool implements tool.Tool for toolloop tests.
type testTool struct {
	name    string
	callFn  func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error)
}

func (t *testTool) Name() string                                                { return t.name }
func (t *testTool) Aliases() []string                                           { return nil }
func (t *testTool) Description(json.RawMessage) (string, error)                 { return t.name + " desc", nil }
func (t *testTool) InputSchema() json.RawMessage                                { return json.RawMessage(`{}`) }
func (t *testTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *testTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *testTool) IsReadOnly(json.RawMessage) bool            { return true }
func (t *testTool) IsDestructive(json.RawMessage) bool         { return false }
func (t *testTool) IsConcurrencySafe(json.RawMessage) bool     { return true }
func (t *testTool) IsEnabled() bool                            { return true }
func (t *testTool) InterruptBehavior() tool.InterruptBehavior  { return tool.InterruptCancel }
func (t *testTool) Prompt() string                             { return "" }
func (t *testTool) RenderResult(any) string                      { return "" }

func TestSequentialToolLoop_SingleTool(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"echo": &testTool{name: "echo"},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}

	var events []types.QueryEvent
	results := engine.SequentialToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(results))
	}
	if results[0].ToolUseID != "tu_1" {
		t.Errorf("expected tool_use_id tu_1, got %s", results[0].ToolUseID)
	}
	if results[0].IsError {
		t.Error("expected no error")
	}
	if results[0].Type != types.ContentTypeToolResult {
		t.Errorf("expected ContentTypeToolResult, got %s", results[0].Type)
	}
	// Verify the tool result content is the JSON-encoded tool output.
	var parsed string
	if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse result content: %v", err)
	}
	if parsed != "ok" {
		t.Errorf("expected result content 'ok', got %q", parsed)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != types.EventToolResult {
		t.Errorf("expected EventToolResult, got %s", events[0].Type)
	}
	if events[0].ToolResult == nil {
		t.Fatal("expected non-nil ToolResult in event")
	}
	if events[0].ToolResult.ToolUseID != "tu_1" {
		t.Errorf("expected event ToolUseID 'tu_1', got %q", events[0].ToolResult.ToolUseID)
	}
}

func TestSequentialToolLoop_UnknownTool(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "nonexistent", Input: json.RawMessage(`{}`)},
	}

	var events []types.QueryEvent
	results := engine.SequentialToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected error for unknown tool")
	}

	var parsed map[string]string
	if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed["error"] != "No such tool available: nonexistent" {
		t.Errorf("unexpected error: %q", parsed["error"])
	}
}

func TestSequentialToolLoop_ToolError(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"fail": &testTool{
			name: "fail",
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("tool crashed")
			},
		},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail", Input: json.RawMessage(`{}`)},
	}

	results := engine.SequentialToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected error result")
	}

	var parsed map[string]string
	if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed["error"] != "tool crashed" {
		t.Errorf("unexpected error: %q", parsed["error"])
	}
}

func TestSequentialToolLoop_MultipleTools(t *testing.T) {
	t.Parallel()
	callCount := map[string]int{}
	tools := map[string]tool.Tool{
		"tool_a": &testTool{name: "tool_a", callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			callCount["tool_a"]++
			return &tool.ToolResult{Data: "a_result"}, nil
		}},
		"tool_b": &testTool{name: "tool_b", callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			callCount["tool_b"]++
			return &tool.ToolResult{Data: "b_result"}, nil
		}},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "tool_a", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "tool_b", Input: json.RawMessage(`{}`)},
	}

	results := engine.SequentialToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if callCount["tool_a"] != 1 || callCount["tool_b"] != 1 {
		t.Errorf("expected each tool called once, got %v", callCount)
	}
	// Verify result ordering matches block ordering.
	if results[0].ToolUseID != "tu_1" {
		t.Errorf("expected results[0] ToolUseID 'tu_1', got %q", results[0].ToolUseID)
	}
	if results[1].ToolUseID != "tu_2" {
		t.Errorf("expected results[1] ToolUseID 'tu_2', got %q", results[1].ToolUseID)
	}
	if results[0].IsError || results[1].IsError {
		t.Error("expected no errors in results")
	}
}

func TestSequentialToolLoop_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before execution

	tools := map[string]tool.Tool{
		"echo": &testTool{name: "echo"},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}

	results := engine.SequentialToolLoop(ctx, tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected error for cancelled context")
	}
	if results[0].ToolUseID != "tu_1" {
		t.Errorf("expected ToolUseID 'tu_1', got %q", results[0].ToolUseID)
	}
	// Verify the error message is the synthetic "user_interrupted" message.
	var parsed map[string]string
	if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse error content: %v", err)
	}
	if parsed["error"] != "User rejected tool use" {
		t.Errorf("expected 'User rejected tool use', got %q", parsed["error"])
	}
}

func TestSequentialToolLoop_SkipsNonToolBlocks(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"echo": &testTool{name: "echo"},
	}

	blocks := []types.ContentBlock{
		types.NewTextBlock("this is not a tool call"),
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}

	results := engine.SequentialToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	// Only the tool_use block should produce a result
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ToolUseID != "tu_1" {
		t.Errorf("expected ToolUseID 'tu_1', got %q", results[0].ToolUseID)
	}
	if results[0].IsError {
		t.Error("expected non-error result for 'echo' tool")
	}
}

func TestSequentialToolLoop_Timing(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"slow": &testTool{name: "slow", callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			time.Sleep(10 * time.Millisecond)
			return &tool.ToolResult{Data: "done"}, nil
		}},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "slow", Input: json.RawMessage(`{}`)},
	}

	var events []types.QueryEvent
	engine.SequentialToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ToolResult.Timing < 10*time.Millisecond {
		t.Errorf("expected timing >= 10ms, got %v", events[0].ToolResult.Timing)
	}
	if events[0].ToolResult.ToolUseID != "tu_1" {
		t.Errorf("expected ToolUseID 'tu_1', got %q", events[0].ToolResult.ToolUseID)
	}
}

func TestSequentialToolLoop_ContextModifier(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"modifier": &testTool{
			name: "modifier",
			callFn: func(_ context.Context, _ json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data: "modified",
					ContextModifier: func(tctx *types.ToolUseContext) *types.ToolUseContext {
						tctx.WorkingDir = "/new-dir"
						return tctx
					},
				}, nil
			},
		},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "modifier", Input: json.RawMessage(`{}`)},
	}

	tctx := &types.ToolUseContext{ToolUseID: "tu_1", WorkingDir: "/old-dir"}
	results := engine.SequentialToolLoop(context.Background(), tools, blocks, tctx, func(evt types.QueryEvent) {})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Error("expected non-error result")
	}
	if tctx.WorkingDir != "/new-dir" {
		t.Errorf("expected WorkingDir to be modified to /new-dir, got %q", tctx.WorkingDir)
	}
}

func TestSequentialToolLoop_ContextModifier_NilTctx(t *testing.T) {
	t.Parallel()
	// When tctx is nil, ContextModifier should not be called even if present
	tools := map[string]tool.Tool{
		"modifier": &testTool{
			name: "modifier",
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data: "ok",
					ContextModifier: func(tctx *types.ToolUseContext) *types.ToolUseContext {
						panic("should not be called with nil tctx")
					},
				}, nil
			},
		},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "modifier", Input: json.RawMessage(`{}`)},
	}

	// tctx is nil - the condition `result.ContextModifier != nil && tctx != nil` is false
	results := engine.SequentialToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}
