package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

func (t *testTool) MaxResultSize() int { return 50000 }

// blockTool is a test tool with InterruptBlock behavior (like Agent).
type blockTool struct {
	name string
}

func (b *blockTool) Name() string                                                { return b.name }
func (b *blockTool) Aliases() []string                                           { return nil }
func (b *blockTool) Description(json.RawMessage) (string, error)                 { return b.name, nil }
func (b *blockTool) InputSchema() json.RawMessage                                { return json.RawMessage(`{}`) }
func (b *blockTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "completed"}, nil
}
func (b *blockTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (b *blockTool) IsReadOnly(json.RawMessage) bool            { return true }
func (b *blockTool) IsDestructive(json.RawMessage) bool         { return false }
func (b *blockTool) IsConcurrencySafe(json.RawMessage) bool     { return true }
func (b *blockTool) IsEnabled() bool                            { return true }
func (b *blockTool) InterruptBehavior() tool.InterruptBehavior  { return tool.InterruptBlock }
func (b *blockTool) Prompt() string                             { return "" }

func (b *blockTool) MaxResultSize() int { return 50000 }
func (b *blockTool) RenderResult(any) string                      { return "" }

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
	if events[0].Type != types.EventToolEnd {
		t.Errorf("expected EventToolEnd, got %s", events[0].Type)
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

// ---------------------------------------------------------------------------
// StreamingToolExecutor tests — source: StreamingToolExecutor.ts
// ---------------------------------------------------------------------------

// concurrentTool implements tool.Tool with configurable concurrency safety.
type concurrentTool struct {
	name   string
	isSafe bool
	callFn func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error)
}

func (t *concurrentTool) Name() string                        { return t.name }
func (t *concurrentTool) Aliases() []string                   { return nil }
func (t *concurrentTool) Description(json.RawMessage) (string, error) {
	return t.name + " desc", nil
}
func (t *concurrentTool) InputSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (t *concurrentTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *concurrentTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *concurrentTool) IsReadOnly(json.RawMessage) bool           { return t.isSafe }
func (t *concurrentTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *concurrentTool) IsConcurrencySafe(json.RawMessage) bool    { return t.isSafe }
func (t *concurrentTool) IsEnabled() bool                           { return true }
func (t *concurrentTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *concurrentTool) Prompt() string                            { return "" }
func (t *concurrentTool) RenderResult(any) string                     { return "" }

func (t *concurrentTool) MaxResultSize() int { return 50000 }

// streamingConcurrentTool implements both Tool and ToolWithStreaming.
type streamingConcurrentTool struct {
	concurrentTool
	streamFn func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error)
}

func (t *streamingConcurrentTool) ExecuteStream(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error) {
	if t.streamFn != nil {
		return t.streamFn(ctx, input, tctx, onProgress)
	}
	return &tool.ToolResult{Data: "streamed"}, nil
}

func TestConcurrentToolLoop_SingleTool(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	var events []types.QueryEvent
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ToolUseID != "tu_1" {
		t.Errorf("expected ToolUseID tu_1, got %s", results[0].ToolUseID)
	}
	if results[0].IsError {
		t.Error("expected no error")
	}
	if results[0].Type != types.ContentTypeToolResult {
		t.Errorf("expected ContentTypeToolResult, got %s", results[0].Type)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != types.EventToolEnd {
		t.Errorf("expected EventToolEnd, got %s", events[0].Type)
	}
	if events[0].ToolResult == nil || events[0].ToolResult.ToolUseID != "tu_1" {
		t.Error("expected event with ToolUseID tu_1")
	}
}

func TestConcurrentToolLoop_UnknownTool(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "nonexistent", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

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
	if !strings.Contains(parsed["error"], "No such tool available") {
		t.Errorf("unexpected error: %q", parsed["error"])
	}
}

func TestConcurrentToolLoop_ToolError(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"fail": &concurrentTool{
			name:   "fail",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("tool crashed")
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatal("expected error result")
	}
	var parsed map[string]string
	if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed["error"] != "tool crashed" {
		t.Errorf("expected 'tool crashed', got %q", parsed["error"])
	}
}

func TestConcurrentToolLoop_SafeToolsRunInParallel(t *testing.T) {
	t.Parallel()
	// Two safe tools that each sleep 50ms should complete in ~50ms (parallel),
	// not ~100ms (serial). Source: StreamingToolExecutor.ts — safe tools execute concurrently.
	var mu sync.Mutex
	var startTimes []time.Time

	tools := map[string]tool.Tool{
		"safe_a": &concurrentTool{
			name: "safe_a", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				startTimes = append(startTimes, time.Now())
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				return &tool.ToolResult{Data: "a"}, nil
			},
		},
		"safe_b": &concurrentTool{
			name: "safe_b", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				startTimes = append(startTimes, time.Now())
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				return &tool.ToolResult{Data: "b"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "safe_a", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_b", Input: json.RawMessage(`{}`)},
	}
	start := time.Now()
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	elapsed := time.Since(start)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Both tools should start within 20ms of each other (parallel execution).
	mu.Lock()
	defer mu.Unlock()
	if len(startTimes) != 2 {
		t.Fatalf("expected 2 start times, got %d", len(startTimes))
	}
	startDiff := startTimes[1].Sub(startTimes[0])
	if startDiff > 20*time.Millisecond {
		t.Errorf("safe tools should start near-simultaneously, started %v apart", startDiff)
	}
	// Total time should be < 100ms (serial would be ~100ms)
	if elapsed > 120*time.Millisecond {
		t.Errorf("parallel execution should complete in ~50ms, took %v", elapsed)
	}
}

func TestConcurrentToolLoop_UnsafeToolsAreSerial(t *testing.T) {
	t.Parallel()
	// Source: StreamingToolExecutor.ts:129-135 — unsafe tools require exclusive access.
	var mu sync.Mutex
	var timestamps []time.Time

	tools := map[string]tool.Tool{
		"unsafe_a": &concurrentTool{
			name: "unsafe_a", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				timestamps = append(timestamps, time.Now())
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				return &tool.ToolResult{Data: "a"}, nil
			},
		},
		"unsafe_b": &concurrentTool{
			name: "unsafe_b", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				timestamps = append(timestamps, time.Now())
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				return &tool.ToolResult{Data: "b"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "unsafe_a", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "unsafe_b", Input: json.RawMessage(`{}`)},
	}
	start := time.Now()
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	elapsed := time.Since(start)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Serial: total should be ~100ms
	if elapsed < 80*time.Millisecond {
		t.Errorf("unsafe tools should execute serially, expected ~100ms, got %v", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(timestamps) != 2 {
		t.Fatalf("expected 2 timestamps, got %d", len(timestamps))
	}
	gap := timestamps[1].Sub(timestamps[0])
	if gap < 30*time.Millisecond {
		t.Errorf("unsafe tools should be ~50ms apart, got %v", gap)
	}
}

func TestConcurrentToolLoop_MixedSafeUnsafe(t *testing.T) {
	t.Parallel()
	// safe_a → unsafe_b → safe_c (serial due to ordering constraint).
	// Source: StreamingToolExecutor.ts:140-151 — processQueue breaks on blocked non-safe.
	var mu sync.Mutex
	var order []string

	makeTool := func(name string, isSafe bool) *concurrentTool {
		return &concurrentTool{
			name: name, isSafe: isSafe,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				time.Sleep(30 * time.Millisecond)
				return &tool.ToolResult{Data: name}, nil
			},
		}
	}

	tools := map[string]tool.Tool{
		"safe_a":   makeTool("safe_a", true),
		"unsafe_b": makeTool("unsafe_b", false),
		"safe_c":   makeTool("safe_c", true),
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "safe_a", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "unsafe_b", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_3", Name: "safe_c", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	mu.Lock()
	defer mu.Unlock()
	if order[0] != "safe_a" || order[1] != "unsafe_b" || order[2] != "safe_c" {
		t.Errorf("expected [safe_a, unsafe_b, safe_c], got %v", order)
	}
	// Results in insertion order.
	for i, expected := range []string{"tu_1", "tu_2", "tu_3"} {
		if results[i].ToolUseID != expected {
			t.Errorf("results[%d]: expected %s, got %s", i, expected, results[i].ToolUseID)
		}
	}
}

func TestConcurrentToolLoop_ResultsInOrder(t *testing.T) {
	t.Parallel()
	// "slow" completes last, "fast" completes first, but results must be in insertion order.
	// Source: StreamingToolExecutor.ts:412-440 — getCompletedResults yields in order.
	tools := map[string]tool.Tool{
		"slow": &concurrentTool{
			name: "slow", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				time.Sleep(50 * time.Millisecond)
				return &tool.ToolResult{Data: "slow"}, nil
			},
		},
		"fast": &concurrentTool{
			name: "fast", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "fast"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "slow", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "fast", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ToolUseID != "tu_1" {
		t.Errorf("results[0]: expected tu_1 (slow), got %s", results[0].ToolUseID)
	}
	if results[1].ToolUseID != "tu_2" {
		t.Errorf("results[1]: expected tu_2 (fast), got %s", results[1].ToolUseID)
	}
}

func TestConcurrentToolLoop_BashErrorKillsRunningSiblings(t *testing.T) {
	t.Parallel()
	// Source: StreamingToolExecutor.ts:359 — Bash errors cancel sibling tools.
	// Bash (safe/read-only) and another safe tool run in parallel.
	// Bash errors → siblingCancel → other tool's context cancelled.
	var safeCtxCancelled bool

	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name: "Bash", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				time.Sleep(10 * time.Millisecond)
				return nil, errors.New("command failed")
			},
		},
		"safe_tool": &concurrentTool{
			name: "safe_tool", isSafe: true,
			callFn: func(ctx context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				select {
				case <-time.After(5 * time.Second):
					return &tool.ToolResult{Data: "ok"}, nil
				case <-ctx.Done():
					safeCtxCancelled = true
					return nil, ctx.Err()
				}
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"bad cmd"}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_tool", Input: json.RawMessage(`{}`)},
	}
	start := time.Now()
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	elapsed := time.Since(start)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected Bash error")
	}
	if !results[1].IsError {
		t.Error("expected safe_tool to be cancelled by sibling error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("sibling cancellation should be fast, took %v", elapsed)
	}
	if !safeCtxCancelled {
		t.Error("safe_tool should have detected context cancellation from sibling abort")
	}
}

func TestConcurrentToolLoop_NonBashErrorNoKill(t *testing.T) {
	t.Parallel()
	// Source: StreamingToolExecutor.ts:354-364 — only Bash errors cancel siblings.
	// Non-Bash tool errors → siblings should still succeed.
	tools := map[string]tool.Tool{
		"fail_tool": &concurrentTool{
			name: "fail_tool", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("non-bash failure")
			},
		},
		"safe_tool": &concurrentTool{
			name: "safe_tool", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "ok"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail_tool", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_tool", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected fail_tool error")
	}
	if results[1].IsError {
		t.Error("safe_tool should NOT be cancelled by non-Bash error")
	}
}

func TestConcurrentToolLoop_ContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(ctx, tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected error for cancelled context")
	}
	var parsed map[string]string
	if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed["error"] != "User rejected tool use" {
		t.Errorf("expected 'User rejected tool use', got %q", parsed["error"])
	}
}

func TestConcurrentToolLoop_InterruptBlockNotCancelled(t *testing.T) {
	t.Parallel()
	// Source: StreamingToolExecutor.ts:222-228 — block-behavior tools are NOT cancelled on user interrupt.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate user interrupt

	tools := map[string]tool.Tool{
		"agent": &blockTool{name: "agent"},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "agent", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(ctx, tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// InterruptBlock tools should NOT be cancelled — they should complete normally
	if results[0].IsError {
		var parsed map[string]string
		if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
			t.Fatalf("tool error: %s", string(results[0].Content))
		}
		t.Errorf("InterruptBlock tool should not be cancelled, got error: %q", parsed["error"])
	}
}

func TestConcurrentToolLoop_ContextModifierOnlyForUnsafe(t *testing.T) {
	t.Parallel()
	// Source: StreamingToolExecutor.ts:388-395 — ContextModifier only for non-concurrent tools.
	safeModified := false

	tools := map[string]tool.Tool{
		"safe_mod": &concurrentTool{
			name: "safe_mod", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data: "safe",
					ContextModifier: func(tctx *types.ToolUseContext) *types.ToolUseContext {
						safeModified = true
						tctx.WorkingDir = "/safe-dir"
						return tctx
					},
				}, nil
			},
		},
		"unsafe_mod": &concurrentTool{
			name: "unsafe_mod", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data: "unsafe",
					ContextModifier: func(tctx *types.ToolUseContext) *types.ToolUseContext {
						tctx.WorkingDir = "/unsafe-dir"
						return tctx
					},
				}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "safe_mod", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "unsafe_mod", Input: json.RawMessage(`{}`)},
	}
	tctx := &types.ToolUseContext{WorkingDir: "/original"}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, tctx, func(evt types.QueryEvent) {})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if safeModified {
		t.Error("safe tool's ContextModifier should NOT be applied")
	}
	if tctx.WorkingDir != "/unsafe-dir" {
		t.Errorf("expected WorkingDir '/unsafe-dir', got %q", tctx.WorkingDir)
	}
}

func TestConcurrentToolLoop_StreamingTool(t *testing.T) {
	t.Parallel()
	// Source: StreamingToolExecutor.ts:320-382 — ToolWithStreaming gets progress callbacks.
	var progressCalls int

	tools := map[string]tool.Tool{
		"streamer": &streamingConcurrentTool{
			concurrentTool: concurrentTool{name: "streamer", isSafe: true},
			streamFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error) {
				onProgress(tool.ProgressUpdate{Lines: []string{"line 1", "line 2"}})
				progressCalls++
				onProgress(tool.ProgressUpdate{Lines: []string{"line 1", "line 2", "line 3"}})
				progressCalls++
				return &tool.ToolResult{Data: "streamed"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "streamer", Input: json.RawMessage(`{}`)},
	}
	var events []types.QueryEvent
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Error("expected no error")
	}
	if progressCalls != 2 {
		t.Errorf("expected 2 progress calls, got %d", progressCalls)
	}
	var outputDeltas, toolEnds int
	for _, evt := range events {
		switch evt.Type {
		case types.EventToolOutputDelta:
			outputDeltas++
		case types.EventToolEnd:
			toolEnds++
		}
	}
	if outputDeltas != 2 {
		t.Errorf("expected 2 EventToolOutputDelta, got %d", outputDeltas)
	}
	if toolEnds != 1 {
		t.Errorf("expected 1 EventToolEnd, got %d", toolEnds)
	}
}

func TestConcurrentToolLoop_EmptyBlocks(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{}
	results := engine.ConcurrentToolLoop(context.Background(), tools, nil, nil, func(evt types.QueryEvent) {})

	if len(results) != 0 {
		t.Errorf("expected 0 results for nil blocks, got %d", len(results))
	}

	// Also test with non-tool blocks only.
	blocks := []types.ContentBlock{types.NewTextBlock("not a tool")}
	results = engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	if len(results) != 0 {
		t.Errorf("expected 0 results for text-only blocks, got %d", len(results))
	}
}

func TestConcurrentToolLoop_SkipsNonToolBlocks(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}
	blocks := []types.ContentBlock{
		types.NewTextBlock("not a tool"),
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ToolUseID != "tu_1" {
		t.Errorf("expected tu_1, got %s", results[0].ToolUseID)
	}
}

func TestConcurrentToolLoop_BashErrorBlocksQueuedSafe(t *testing.T) {
	t.Parallel()
	// Bash (unsafe) runs first. When it errors, queued safe_tool gets sibling_error
	// synthetic block (not context cancellation — tool function never called).
	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name: "Bash", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("command failed")
			},
		},
		"safe_tool": &concurrentTool{
			name: "safe_tool", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "should not run"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"bad"}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_tool", Input: json.RawMessage(`{}`)},
	}
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected Bash error")
	}
	if !results[1].IsError {
		t.Error("expected safe_tool to be cancelled (sibling error)")
	}
	// Verify safe_tool got sibling error message, not its own output.
	var parsed map[string]string
	if err := json.Unmarshal(results[1].Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if !strings.Contains(parsed["error"], "Cancelled") {
		t.Errorf("expected sibling error message, got %q", parsed["error"])
	}
}

// TestConcurrentToolLoop_UnknownToolDisplayOutput verifies that the unknown-tool
// error path sets DisplayOutput on the event (not just Output).
func TestConcurrentToolLoop_UnknownToolDisplayOutput(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "nonexistent", Input: json.RawMessage(`{}`)},
	}

	var events []types.QueryEvent
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	// Result block must have error content
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatal("expected error result")
	}

	// Event must have non-empty DisplayOutput
	var toolEndEvents []types.QueryEvent
	for _, evt := range events {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_1" {
			toolEndEvents = append(toolEndEvents, evt)
		}
	}
	if len(toolEndEvents) == 0 {
		t.Fatal("no tool_end event found for tu_1")
	}
	evt := toolEndEvents[0]
	if evt.ToolResult.DisplayOutput == "" {
		t.Error("DisplayOutput must not be empty for unknown tool error")
	}
	if !strings.Contains(evt.ToolResult.DisplayOutput, "No such tool available") {
		t.Errorf("DisplayOutput should mention 'No such tool available', got %q", evt.ToolResult.DisplayOutput)
	}
	if !evt.ToolResult.IsError {
		t.Error("event IsError must be true")
	}
}

// TestConcurrentToolLoop_ToolErrorDisplayOutput verifies that emitToolError
// sets DisplayOutput when a tool's Call() returns an error.
func TestConcurrentToolLoop_ToolErrorDisplayOutput(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"fail": &concurrentTool{
			name:   "fail",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("specific failure X")
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail", Input: json.RawMessage(`{}`)},
	}

	var events []types.QueryEvent
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatal("expected error result")
	}

	// Verify DisplayOutput in event
	var toolEndEvents []types.QueryEvent
	for _, evt := range events {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_1" {
			toolEndEvents = append(toolEndEvents, evt)
		}
	}
	if len(toolEndEvents) == 0 {
		t.Fatal("no tool_end event found for tu_1")
	}
	evt := toolEndEvents[0]
	if evt.ToolResult.DisplayOutput == "" {
		t.Error("DisplayOutput must not be empty for tool error")
	}
	if !strings.Contains(evt.ToolResult.DisplayOutput, "specific failure X") {
		t.Errorf("DisplayOutput should contain error message, got %q", evt.ToolResult.DisplayOutput)
	}
}

// TestConcurrentToolLoop_AbortDisplayOutput verifies that the abort path
// (sibling Bash error kills sibling tools) sets DisplayOutput on the synthetic error event.
func TestConcurrentToolLoop_AbortDisplayOutput(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name: "Bash", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("bash boom")
			},
		},
		"slow": &concurrentTool{
			name: "slow", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
				close(started)
				time.Sleep(5 * time.Second)
				return &tool.ToolResult{Data: "should not reach"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_bash", Name: "Bash", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_slow", Name: "slow", Input: json.RawMessage(`{}`)},
	}

	var mu sync.Mutex
	var events []types.QueryEvent
	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	})

	// slow tool should have synthetic error block
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	var slowResult *types.ContentBlock
	for _, r := range results {
		if r.IsError {
			var m map[string]string
			if err := json.Unmarshal(r.Content, &m); err != nil {
				t.Fatalf("failed to parse error block: %v", err)
			}
			if strings.Contains(m["error"], "Cancelled") {
				slowResult = &r
			}
		}
	}
	if slowResult == nil {
		t.Fatal("expected a 'Cancelled' synthetic error for the slow tool")
	}

	// Verify the slow tool's event has non-empty DisplayOutput
	mu.Lock()
	defer mu.Unlock()
	for _, evt := range events {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_slow" {
			if evt.ToolResult.DisplayOutput == "" {
				t.Error("abort path must set non-empty DisplayOutput for synthetic error event")
			}
			if !strings.Contains(evt.ToolResult.DisplayOutput, "Cancelled") {
				t.Errorf("DisplayOutput should mention 'Cancelled', got %q", evt.ToolResult.DisplayOutput)
			}
			return
		}
	}
	t.Fatal("no tool_end event found for tu_slow")
}

// TestConcurrentToolLoop_ToolUseIDInContext verifies that each tool receives
// a ToolUseContext with the correct ToolUseID, even when the executor is
// created with nil tctx. This is required for Agent tool to propagate
// ParentToolUseID for sub-agent progress display.
func TestConcurrentToolLoop_ToolUseIDInContext(t *testing.T) {
	t.Parallel()

	var capturedIDs []string
	var mu sync.Mutex
	tools := map[string]tool.Tool{
		"capture": &testTool{name: "capture", callFn: func(_ context.Context, _ json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			mu.Lock()
			defer mu.Unlock()
			id := ""
			if tctx != nil {
				id = tctx.ToolUseID
			}
			capturedIDs = append(capturedIDs, id)
			return &tool.ToolResult{Data: "captured:" + id}, nil
		}},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_agent_42", Name: "capture", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_read_99", Name: "capture", Input: json.RawMessage(`{}`)},
	}

	results := engine.ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Verify each tool received the correct ToolUseID
	mu.Lock()
	defer mu.Unlock()
	if len(capturedIDs) != 2 {
		t.Fatalf("expected 2 captured IDs, got %d: %v", len(capturedIDs), capturedIDs)
	}

	want := map[string]bool{"tu_agent_42": false, "tu_read_99": false}
	for _, id := range capturedIDs {
		if id == "" {
			t.Error("received empty ToolUseID — tools cannot identify their own tool call")
		}
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("ToolUseID %q was never received", id)
		}
	}
}
