package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// testTool implements tool.Tool for toolloop tests.
type testTool struct {
	name   string
	callFn func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error)
}

func (t *testTool) Name() string                                { return t.name }
func (t *testTool) Aliases() []string                           { return nil }
func (t *testTool) Description(json.RawMessage) (string, error) { return t.name + " desc", nil }
func (t *testTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *testTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *testTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *testTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *testTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *testTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *testTool) IsEnabled() bool                           { return true }
func (t *testTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *testTool) Prompt() string                            { return "" }
func (t *testTool) RenderResult(any) string                   { return "" }

func (t *testTool) MaxResultSize() int { return 50000 }

// blockTool is a test tool with InterruptBlock behavior (like Agent).
type blockTool struct {
	name string
}

func (b *blockTool) Name() string                                { return b.name }
func (b *blockTool) Aliases() []string                           { return nil }
func (b *blockTool) Description(json.RawMessage) (string, error) { return b.name, nil }
func (b *blockTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (b *blockTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "completed"}, nil
}
func (b *blockTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (b *blockTool) IsReadOnly(json.RawMessage) bool           { return true }
func (b *blockTool) IsDestructive(json.RawMessage) bool        { return false }
func (b *blockTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (b *blockTool) IsEnabled() bool                           { return true }
func (b *blockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptBlock }
func (b *blockTool) Prompt() string                            { return "" }

func (b *blockTool) MaxResultSize() int      { return 50000 }
func (b *blockTool) RenderResult(any) string { return "" }

// ---------------------------------------------------------------------------
// StreamingToolExecutor tests — source: StreamingToolExecutor.ts
// ---------------------------------------------------------------------------

// concurrentTool implements tool.Tool with configurable concurrency safety.
type concurrentTool struct {
	name   string
	isSafe bool
	callFn func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error)
}

func (t *concurrentTool) Name() string      { return t.name }
func (t *concurrentTool) Aliases() []string { return nil }
func (t *concurrentTool) Description(json.RawMessage) (string, error) {
	return t.name + " desc", nil
}
func (t *concurrentTool) InputSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (t *concurrentTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *concurrentTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *concurrentTool) IsReadOnly(json.RawMessage) bool           { return t.isSafe }
func (t *concurrentTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *concurrentTool) IsConcurrencySafe(json.RawMessage) bool    { return t.isSafe }
func (t *concurrentTool) IsEnabled() bool                           { return true }
func (t *concurrentTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *concurrentTool) Prompt() string                            { return "" }
func (t *concurrentTool) RenderResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return ""
}

func (t *concurrentTool) MaxResultSize() int { return 50000 }

func TestConcurrentToolLoop_SingleTool(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"echo": &concurrentTool{name: "echo", isSafe: true},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "echo", Input: json.RawMessage(`{}`)},
	}
	var events []types.QueryEvent
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if result.ToolResultBlocks[0].ToolUseID != "tu_1" {
		t.Errorf("expected ToolUseID tu_1, got %s", result.ToolResultBlocks[0].ToolUseID)
	}
	if result.ToolResultBlocks[0].IsError {
		t.Fatalf("expected no error, got IsError=true")
	}
	if result.ToolResultBlocks[0].Type != types.ContentTypeToolResult {
		t.Errorf("expected ContentTypeToolResult, got %s", result.ToolResultBlocks[0].Type)
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
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error for unknown tool")
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse error content: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	if !strings.Contains(parsed[0].Text, "No such tool available") {
		t.Errorf("error should mention 'No such tool available', got: %q", parsed[0].Text)
	}
}

func TestConcurrentToolLoop_ToolError(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"fail": &concurrentTool{
			name:   "fail",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("tool crashed")
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error result")
	}
	// Error content is array form: single text block with the error message.
	var errMsg []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &errMsg); err != nil {
		t.Fatalf("failed to parse error content as array: %v", err)
	}
	if len(errMsg) != 1 || errMsg[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", errMsg)
	}
	if !strings.Contains(errMsg[0].Text, "tool crashed") {
		t.Errorf("expected 'tool crashed', got %q", errMsg[0].Text)
	}
}

func TestConcurrentToolLoop_ToolErrorContentIsArray(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"fail": &concurrentTool{
			name:   "fail",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("tool crashed")
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error result")
	}

	content := result.ToolResultBlocks[0].Content
	// Error content must be array form (single text block).
	if len(content) == 0 || content[0] != '[' {
		t.Errorf("error content should be a JSON array, got: %s", content)
	}

	var parsed []types.ContentBlock
	if err := json.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("failed to unmarshal error content as array: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	if !strings.Contains(parsed[0].Text, "tool crashed") {
		t.Errorf("error message should contain 'tool crashed', got %q", parsed[0].Text)
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				startTimes = append(startTimes, time.Now()) // REAL-TIME: needed to verify concurrent/serial execution timing
				mu.Unlock()
				time.Sleep(50 * time.Millisecond) // REAL-TIME: needed to verify parallel execution timing
				return &tool.ToolResult{Data: "a"}, nil
			},
		},
		"safe_b": &concurrentTool{
			name: "safe_b", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				startTimes = append(startTimes, time.Now()) // REAL-TIME: needed to verify concurrent/serial execution timing
				mu.Unlock()
				time.Sleep(50 * time.Millisecond) // REAL-TIME: needed to verify parallel execution timing
				return &tool.ToolResult{Data: "b"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "safe_a", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_b", Input: json.RawMessage(`{}`)},
	}
	start := time.Now() // REAL-TIME: needed to measure elapsed time for timing assertions
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	elapsed := time.Since(start)

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				timestamps = append(timestamps, time.Now()) // REAL-TIME: needed to verify concurrent/serial execution timing
				mu.Unlock()
				time.Sleep(50 * time.Millisecond) // REAL-TIME: needed to verify serial execution timing
				return &tool.ToolResult{Data: "a"}, nil
			},
		},
		"unsafe_b": &concurrentTool{
			name: "unsafe_b", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				timestamps = append(timestamps, time.Now()) // REAL-TIME: needed to verify concurrent/serial execution timing
				mu.Unlock()
				time.Sleep(50 * time.Millisecond) // REAL-TIME: needed to verify serial execution timing
				return &tool.ToolResult{Data: "b"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "unsafe_a", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "unsafe_b", Input: json.RawMessage(`{}`)},
	}
	start := time.Now() // REAL-TIME: needed to measure elapsed time for timing assertions
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	elapsed := time.Since(start)

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				time.Sleep(30 * time.Millisecond) // REAL-TIME: needed to verify mixed execution ordering
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
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.ToolResultBlocks))
	}
	mu.Lock()
	defer mu.Unlock()
	if order[0] != "safe_a" || order[1] != "unsafe_b" || order[2] != "safe_c" {
		t.Errorf("expected [safe_a, unsafe_b, safe_c], got %v", order)
	}
	// Results in insertion order.
	for i, expected := range []string{"tu_1", "tu_2", "tu_3"} {
		if result.ToolResultBlocks[i].ToolUseID != expected {
			t.Errorf("result.ToolResultBlocks[%d]: expected %s, got %s", i, expected, result.ToolResultBlocks[i].ToolUseID)
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				time.Sleep(50 * time.Millisecond) // REAL-TIME: needed to verify result ordering despite different completion times
				return &tool.ToolResult{Data: "slow"}, nil
			},
		},
		"fast": &concurrentTool{
			name: "fast", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "fast"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "slow", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "fast", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	if result.ToolResultBlocks[0].ToolUseID != "tu_1" {
		t.Errorf("result.ToolResultBlocks[0]: expected tu_1 (slow), got %s", result.ToolResultBlocks[0].ToolUseID)
	}
	if result.ToolResultBlocks[1].ToolUseID != "tu_2" {
		t.Errorf("result.ToolResultBlocks[1]: expected tu_2 (fast), got %s", result.ToolResultBlocks[1].ToolUseID)
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				time.Sleep(10 * time.Millisecond) // REAL-TIME: needed to test Bash error kills sibling cancellation timing
				return nil, errors.New("command failed")
			},
		},
		"safe_tool": &concurrentTool{
			name: "safe_tool", isSafe: true,
			callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
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
	start := time.Now() // REAL-TIME: needed to measure elapsed time for timing assertions
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	elapsed := time.Since(start)

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected Bash error")
	}
	var bashParsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &bashParsed); err != nil {
		t.Fatalf("failed to parse Bash error content: %v", err)
	}
	if len(bashParsed) != 1 || bashParsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", bashParsed)
	}
	if !strings.Contains(bashParsed[0].Text, "command failed") {
		t.Errorf("error should mention 'command failed', got: %q", bashParsed[0].Text)
	}
	if !result.ToolResultBlocks[1].IsError {
		t.Fatal("expected safe_tool to be cancelled by sibling error")
	}
	var safeParsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[1].Content, &safeParsed); err != nil {
		t.Fatalf("failed to parse safe_tool error content: %v", err)
	}
	if len(safeParsed) != 1 || safeParsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", safeParsed)
	}
	if !strings.Contains(safeParsed[0].Text, "context canceled") && !strings.Contains(safeParsed[0].Text, "Cancelled") {
		t.Errorf("error should mention cancellation, got: %q", safeParsed[0].Text)
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("non-bash failure")
			},
		},
		"safe_tool": &concurrentTool{
			name: "safe_tool", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "ok"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail_tool", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_tool", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected fail_tool error")
	}
	var failParsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &failParsed); err != nil {
		t.Fatalf("failed to parse fail_tool error content: %v", err)
	}
	if len(failParsed) != 1 || failParsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", failParsed)
	}
	if !strings.Contains(failParsed[0].Text, "non-bash failure") {
		t.Errorf("error should mention 'non-bash failure', got: %q", failParsed[0].Text)
	}
	if result.ToolResultBlocks[1].IsError {
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
	result := ConcurrentToolLoop(ctx, tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error for cancelled context")
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	if parsed[0].Text != "User rejected tool use" {
		t.Errorf("expected 'User rejected tool use', got %q", parsed[0].Text)
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
	result := ConcurrentToolLoop(ctx, tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	// InterruptBlock tools should NOT be cancelled — they should complete normally
	if result.ToolResultBlocks[0].IsError {
		var parsed string
		if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &parsed); err != nil {
			t.Fatalf("tool error: %s", string(result.ToolResultBlocks[0].Content))
		}
		t.Errorf("InterruptBlock tool should not be cancelled, got error: %q", parsed)
	}
}

func TestConcurrentToolLoop_ContextModifierOnlyForUnsafe(t *testing.T) {
	t.Parallel()
	// Source: StreamingToolExecutor.ts:388-395 — ContextModifier only for non-concurrent tools.
	safeModified := false

	tools := map[string]tool.Tool{
		"safe_mod": &concurrentTool{
			name: "safe_mod", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data: "safe",
					ContextModifier: func(tctx *tool.ToolUseContext) *tool.ToolUseContext {
						safeModified = true
						tctx.WorkingDir = "/safe-dir"
						return tctx
					},
				}, nil
			},
		},
		"unsafe_mod": &concurrentTool{
			name: "unsafe_mod", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data: "unsafe",
					ContextModifier: func(tctx *tool.ToolUseContext) *tool.ToolUseContext {
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
	tctx := &tool.ToolUseContext{WorkingDir: "/original"}
	result := ConcurrentToolLoop(context.Background(), tools, blocks, tctx, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
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
	// Source: StreamingToolExecutor.ts:320-382 — tools use OnProgress in ToolUseContext.
	var progressCalls int

	tools := map[string]tool.Tool{
		"streamer": &concurrentTool{
			name: "streamer", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
				if tctx != nil && tctx.OnProgress != nil {
					tctx.OnProgress(tool.ProgressUpdate{Lines: []string{"line 1", "line 2"}})
					progressCalls++
					tctx.OnProgress(tool.ProgressUpdate{Lines: []string{"line 1", "line 2", "line 3"}})
					progressCalls++
				}
				return &tool.ToolResult{Data: "streamed"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "streamer", Input: json.RawMessage(`{}`)},
	}
	var events []types.QueryEvent
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if result.ToolResultBlocks[0].IsError {
		t.Fatalf("expected no error, got IsError=true")
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
	result := ConcurrentToolLoop(context.Background(), tools, nil, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 0 {
		t.Errorf("expected 0 results for nil blocks, got %d", len(result.ToolResultBlocks))
	}

	// Also test with non-tool blocks only.
	blocks := []types.ContentBlock{types.NewTextBlock("not a tool")}
	result = ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 0 {
		t.Errorf("expected 0 results for text-only blocks, got %d", len(result.ToolResultBlocks))
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
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if result.ToolResultBlocks[0].ToolUseID != "tu_1" {
		t.Errorf("expected tu_1, got %s", result.ToolResultBlocks[0].ToolUseID)
	}
}

func TestConcurrentToolLoop_BashErrorBlocksQueuedSafe(t *testing.T) {
	t.Parallel()
	// Bash (unsafe) runs first. When it errors, queued safe_tool gets sibling_error
	// synthetic block (not context cancellation — tool function never called).
	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name: "Bash", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("command failed")
			},
		},
		"safe_tool": &concurrentTool{
			name: "safe_tool", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "should not run"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"bad"}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_tool", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected Bash error")
	}
	var bashParsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &bashParsed); err != nil {
		t.Fatalf("failed to parse Bash error content: %v", err)
	}
	if len(bashParsed) != 1 || bashParsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", bashParsed)
	}
	if !strings.Contains(bashParsed[0].Text, "command failed") {
		t.Errorf("error should mention 'command failed', got: %q", bashParsed[0].Text)
	}
	if !result.ToolResultBlocks[1].IsError {
		t.Fatal("expected safe_tool to be cancelled (sibling error)")
	}
	// Verify safe_tool got sibling error message, not its own output.
	var parsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[1].Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	if !strings.Contains(parsed[0].Text, "Cancelled") {
		t.Errorf("expected sibling error message, got %q", parsed[0].Text)
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
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	// Result block must have error content
	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error result")
	}
	var unkParsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &unkParsed); err != nil {
		t.Fatalf("failed to parse error content: %v", err)
	}
	if len(unkParsed) != 1 || unkParsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", unkParsed)
	}
	if !strings.Contains(unkParsed[0].Text, "No such tool available") {
		t.Errorf("error should mention 'No such tool available', got: %q", unkParsed[0].Text)
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("specific failure X")
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "fail", Input: json.RawMessage(`{}`)},
	}

	var events []types.QueryEvent
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		events = append(events, evt)
	})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error result")
	}
	// Error content is array form: single text block.
	var errMsg []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &errMsg); err != nil {
		t.Fatalf("failed to parse error content as array: %v", err)
	}
	if len(errMsg) != 1 || errMsg[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", errMsg)
	}
	if !strings.Contains(errMsg[0].Text, "specific failure X") {
		t.Errorf("error should mention 'specific failure X', got: %q", errMsg[0].Text)
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
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("bash boom")
			},
		},
		"slow": &concurrentTool{
			name: "slow", isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(started)
				time.Sleep(5 * time.Second) // REAL-TIME: long sleep to test abort path (context cancelled before completion)
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
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	})

	// slow tool should have synthetic error block
	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	var slowResult *types.ContentBlock
	for _, r := range result.ToolResultBlocks {
		if r.IsError {
			var text string
			var blocks []types.ContentBlock
			if err := json.Unmarshal(r.Content, &blocks); err != nil {
				t.Fatalf("failed to parse error block: %v", err)
			}
			for _, b := range blocks {
				if b.Type == types.ContentTypeText {
					text = b.Text
					break
				}
			}
			if strings.Contains(text, "Cancelled") {
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
		"capture": &testTool{name: "capture", callFn: func(_ context.Context, _ json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
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

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
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

// ---------------------------------------------------------------------------
// Deferred tool hint tests

func TestConcurrentToolLoop_DeferredToolHint(t *testing.T) {
	// When ToolSearch is active, calling a deferred tool that hasn't been
	// discovered should produce an error hinting to use ToolSearch.
	deferredTool := tool.BuildTool(tool.ToolDef{
		Name_:        "mcp__fetch__get_markdown",
		ShouldDefer_: true,
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
		Description_: func(json.RawMessage) (string, error) { return "fetch markdown", nil },
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
	})
	toolSearchTool := tool.BuildTool(tool.ToolDef{
		Name_:        "ToolSearch",
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
		Description_: func(json.RawMessage) (string, error) { return "search tools", nil },
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
	})

	// Full tool map (has deferred tool + ToolSearch)
	fullTools := map[string]tool.Tool{
		"ToolSearch":               toolSearchTool,
		"mcp__fetch__get_markdown": deferredTool,
	}
	// Filtered tool map (ToolSearch active, deferred not discovered)
	filteredTools := map[string]tool.Tool{
		"ToolSearch": toolSearchTool,
	}
	tctx := &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			Tools: fullTools,
		},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "mcp__fetch__get_markdown", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), filteredTools, blocks, tctx, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error for deferred tool")
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse error content: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	// Should hint to use ToolSearch
	if !strings.Contains(parsed[0].Text, "ToolSearch") {
		t.Errorf("error should mention ToolSearch, got: %q", parsed[0].Text)
	}
	if !strings.Contains(parsed[0].Text, "select:mcp__fetch__get_markdown") {
		t.Errorf("error should suggest select:tool_name, got: %q", parsed[0].Text)
	}
}

// TestToolNotFound_GlobHint verifies that calling the long-gone "Glob" tool
// (merged into Grep) returns an actionable hint instead of a bare "no such
// tool" error — without it, models trained on Claude Code keep retrying Glob.
func TestToolNotFound_GlobHint(t *testing.T) {
	filteredTools := map[string]tool.Tool{} // empty — Glob is unknown
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_glob", Name: "Glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
	}
	result := ConcurrentToolLoop(context.Background(), filteredTools, blocks, nil, func(types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Fatal("expected error for unknown tool")
	}
	var msg []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &msg); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(msg) != 1 || msg[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", msg)
	}
	text := msg[0].Text
	// Must mention Grep (the replacement tool) and concrete JSON syntax
	// so the LLM sees `glob` as a field name in the input JSON, not
	// a CLI flag or shorthand.
	if !strings.Contains(text, "Grep") {
		t.Errorf("hint should mention Grep, got: %q", text)
	}
	if !strings.Contains(text, `"glob"`) {
		t.Errorf("hint should show `glob` as a JSON field name, got: %q", text)
	}
	if !strings.Contains(text, "*.go") {
		t.Errorf("hint should show a concrete glob example, got: %q", text)
	}
	if strings.Contains(text, "No such tool available") {
		t.Errorf("hint should replace the generic 'no such tool' message, got: %q", text)
	}
}

// TestToolNotFound_UnknownTool_NoHint verifies that for tools the hint
// registry doesn't know about, the error stays generic (no false hint).
func TestToolNotFound_UnknownTool_NoHint(t *testing.T) {
	filteredTools := map[string]tool.Tool{}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_x", Name: "SomeRandomName", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), filteredTools, blocks, nil, func(types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 || !result.ToolResultBlocks[0].IsError {
		t.Fatalf("expected 1 error result, got %+v", result.ToolResultBlocks)
	}
	var msg []types.ContentBlock
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &msg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(msg) != 1 || msg[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", msg)
	}
	if !strings.Contains(msg[0].Text, "No such tool available: SomeRandomName") {
		t.Errorf("expected generic message, got: %q", msg[0].Text)
	}
}

// TestChain_BashFileBackup_EmptyWorkingDir_NoBackupRecorded verifies the full
// executeTool chain for Bash tools when WorkingDir is empty. In this case
// executeTool skips TakeSnapshot (requires non-empty WorkingDir), so no files
// are tracked in the tracker.
func TestChain_BashFileBackup_EmptyWorkingDir_NoBackupRecorded(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "data.txt")
	if err := os.WriteFile(testFile, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Bash-like tool that modifies a file
	bashTool := &testTool{
		name: "Bash",
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			_ = os.WriteFile(testFile, []byte("modified by bash\n"), 0o644)
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	// Create executor WITHOUT WorkingDir — executeTool skips TakeSnapshot
	toolMap := map[string]tool.Tool{"Bash": bashTool}
	tctx := &tool.ToolUseContext{} // WorkingDir is empty
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))

	e := NewStreamingToolExecutor(toolMap, tctx, func(_ types.QueryEvent) {}, context.Background())
	e.SetMessages([]types.Message{
		{ID: "msg-1", Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "run bash"}}},
	})
	e.SetFileHistory(tracker)
	e.currentTurnMsgID = "msg-0"

	result := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tool_1", Name: "Bash", Input: json.RawMessage(`{}`)},
	})
	if len(result.ToolResultBlocks) == 0 {
		t.Fatal("expected tool result blocks")
	}

	// Verify: NO tracked files because WorkingDir was empty — TakeSnapshot was skipped
	state := tracker.State()
	if len(state.TrackedFiles) != 0 {
		t.Fatalf("expected 0 tracked files with empty WorkingDir, got %d: %v",
			len(state.TrackedFiles), state.TrackedFiles)
	}

	// Verify: only the initial empty snapshot exists (no Bash snapshot created)
	if len(state.Snapshots) != 1 {
		t.Fatalf("expected 1 snapshot (initial only), got %d", len(state.Snapshots))
	}

	// Verify: file was NOT restored to original
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read test file: %v", err)
	}
	if string(data) != "modified by bash\n" {
		t.Fatalf("file should still be modified, got %q", string(data))
	}
}

// TestChain_BashFileBackup_WithWorkingDir_BackupRecorded verifies that when
// WorkingDir IS set on tctx, the full chain works:
// TakeSnapshot → tool call → DetectChanges → TrackEdit for each change.
// The modified file should appear in TrackedFiles and the latest snapshot's
// TrackedFileBackups with a non-empty backup file preserving the original content.
func TestChain_BashFileBackup_WithWorkingDir_BackupRecorded(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "data.txt")
	originalContent := []byte("original\n")
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	bashTool := &testTool{
		name: "Bash",
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			_ = os.WriteFile(testFile, []byte("modified by bash\n"), 0o644)
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	// Create executor WITH WorkingDir set
	toolMap := map[string]tool.Tool{"Bash": bashTool}
	tctx := &tool.ToolUseContext{WorkingDir: tmp}
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))

	e := NewStreamingToolExecutor(toolMap, tctx, func(_ types.QueryEvent) {}, context.Background())
	e.SetMessages([]types.Message{
		{ID: "msg-1", Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "run bash"}}},
	})
	e.SetFileHistory(tracker)
	e.currentTurnMsgID = "msg-1"

	result := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tool_2", Name: "Bash", Input: json.RawMessage(`{}`)},
	})
	if len(result.ToolResultBlocks) == 0 {
		t.Fatal("expected tool result blocks")
	}

	// Verify: file IS tracked because WorkingDir was set
	state := tracker.State()
	if !state.TrackedFiles[testFile] {
		t.Errorf("expected %s to be in TrackedFiles, got: %v", testFile, state.TrackedFiles)
	}

	// Verify: the initial snapshot has a backup entry for the file
	// (TrackEditFromContent adds to the most recent snapshot, doesn't create a new one)
	lastSnap := state.Snapshots[len(state.Snapshots)-1]
	backup, ok := lastSnap.TrackedFileBackups[testFile]
	if !ok {
		t.Fatalf("expected %s in snapshot TrackedFileBackups, got: %v",
			testFile, lastSnap.TrackedFileBackups)
	}

	// Verify: BackupFileName is non-empty (file existed before Bash modified it)
	if backup.BackupFileName == "" {
		t.Error("expected non-empty BackupFileName for modified file (file existed before)")
	}

	// Verify: backup file on disk contains original content
	backupPath := filepath.Join(tmp, ".backups", backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup file: %v", err)
	}
	if string(data) != string(originalContent) {
		t.Errorf("backup content = %q, want %q", string(data), string(originalContent))
	}
}

// ---------------------------------------------------------------------------
// File-level conflict detection tests
// ---------------------------------------------------------------------------

// editTool is a concurrentTool registered under name "Edit" so that
// extractFilePath parses file_path from the JSON input.
type editTool struct {
	concurrentTool
}

func (t *editTool) Name() string      { return "Edit" }
func (t *editTool) Aliases() []string { return nil }

// writeTool is a concurrentTool registered under name "Write".
type writeTool struct {
	concurrentTool
}

func (t *writeTool) Name() string      { return "Write" }
func (t *writeTool) Aliases() []string { return nil }

// editInput builds a JSON input with file_path for Edit tools.
func editInput(file string) json.RawMessage {
	return json.RawMessage(`{"file_path":"` + file + `","old_string":"x","new_string":"y"}`)
}

// writeInput builds a JSON input with file_path for Write tools.
func writeInput(file string) json.RawMessage {
	return json.RawMessage(`{"file_path":"` + file + `","content":"data"}`)
}

// TestFileConflict_SameFile_EditEdit_Serializes verifies two Edit tools on the
// same file execute sequentially (not in parallel).
func TestFileConflict_SameFile_EditEdit_Serializes(t *testing.T) {
	t.Parallel()

	// If conflict detection is removed, both Edits start simultaneously.
	// We detect this by having each Edit send to a channel; if 2 values
	// arrive before context cancel, they ran in parallel.
	started := make(chan struct{}, 2)

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				started <- struct{}{}
				// Block until context cancels — exposes parallelism
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "e2", Name: "Edit", Input: editInput("a.go")},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ConcurrentToolLoop(ctx, tools, blocks, nil, func(types.QueryEvent) {})
		close(done)
	}()

	// Wait for first Edit to start, then cancel.
	select {
	case <-started:
	case <-done:
		t.Fatal("ConcurrentToolLoop returned before any Edit started")
	}
	cancel()
	<-done // tools return ctx.Err() on cancel, ConcurrentToolLoop finishes

	// If conflict detection works, only 1 Edit entered callFn.
	select {
	case <-started:
		t.Error("second Edit started on same file — conflict detection broken")
	default:
	}
}

// TestFileConflict_DifferentFile_EditEdit_Parallel verifies two Edit tools on
// different files run concurrently.
func TestFileConflict_DifferentFile_EditEdit_Parallel(t *testing.T) {
	t.Parallel()

	started1, started2 := make(chan struct{}), make(chan struct{})
	release1, release2 := make(chan struct{}), make(chan struct{})

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(input, &in)
				if in.FilePath == "a.go" {
					close(started1)
					<-release1
				} else {
					close(started2)
					<-release2
				}
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "e2", Name: "Edit", Input: editInput("b.go")},
	}

	go func() {
		// Wait for both to start, then release both
		<-started1
		<-started2
		close(release1)
		close(release2)
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
}

// TestFileConflict_SameFile_EditWrite_Serializes verifies Edit and Write on the
// same file serialize across tool types.
func TestFileConflict_SameFile_EditWrite_Serializes(t *testing.T) {
	t.Parallel()

	editStarted, editRelease := make(chan struct{}), make(chan struct{})
	writeStarted, writeRelease := make(chan struct{}), make(chan struct{})

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(editStarted)
				<-editRelease
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
		"Write": &writeTool{concurrentTool{
			name:   "Write",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(writeStarted)
				<-writeRelease
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "w1", Name: "Write", Input: writeInput("a.go")},
	}

	// Release Write only after Edit has started, proving serialization.
	go func() {
		<-editStarted
		close(editRelease)
		<-writeStarted
		close(writeRelease)
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
}

// TestFileConflict_UnsafeTool_BlocksAll verifies an unsafe tool blocks all
// queued tools regardless of file path.
func TestFileConflict_UnsafeTool_BlocksAll(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var order []string

	bashStarted := make(chan struct{})
	bashRelease := make(chan struct{})

	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name:   "Bash",
			isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(bashStarted)
				<-bashRelease
				mu.Lock()
				order = append(order, "Bash")
				mu.Unlock()
				return &tool.ToolResult{Data: "ok"}, nil
			},
		},
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				order = append(order, "Edit")
				mu.Unlock()
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "b1", Name: "Bash", Input: json.RawMessage(`{"command":"echo hi"}`)},
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
	}

	go func() {
		<-bashStarted
		close(bashRelease)
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "Bash" || order[1] != "Edit" {
		t.Errorf("execution order = %v, want [Bash Edit]", order)
	}
}

// TestFileConflict_UnsafeThenSameFile serializes: unsafe runs first, then
// same-file Edit and Write serialize after it.
func TestFileConflict_UnsafeThenSameFile(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var order []string

	editStarted := make(chan struct{})
	editRelease := make(chan struct{})
	writeStarted := make(chan struct{})
	writeRelease := make(chan struct{})
	bashStarted := make(chan struct{})
	bashRelease := make(chan struct{})

	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name:   "Bash",
			isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(bashStarted)
				<-bashRelease
				mu.Lock()
				order = append(order, "Bash")
				mu.Unlock()
				return &tool.ToolResult{Data: "ok"}, nil
			},
		},
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(editStarted)
				<-editRelease
				mu.Lock()
				order = append(order, "Edit")
				mu.Unlock()
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
		"Write": &writeTool{concurrentTool{
			name:   "Write",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(writeStarted)
				<-writeRelease
				mu.Lock()
				order = append(order, "Write")
				mu.Unlock()
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "b1", Name: "Bash", Input: json.RawMessage(`{"command":"echo"}`)},
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "w1", Name: "Write", Input: writeInput("a.go")},
	}

	go func() {
		<-bashStarted
		close(bashRelease)
		<-editStarted
		close(editRelease)
		<-writeStarted
		close(writeRelease)
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.ToolResultBlocks))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "Bash" || order[1] != "Edit" || order[2] != "Write" {
		t.Errorf("execution order = %v, want [Bash Edit Write]", order)
	}
}

// TestFileConflict_ThreeTool_FanOut verifies mixed parallel+serial: two Edits
// on different files run parallel, a third Edit on same file as first serializes.
func TestFileConflict_ThreeTool_FanOut(t *testing.T) {
	t.Parallel()

	startedA, startedB, startedA2 := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release1, release2 := make(chan struct{}), make(chan struct{})

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(input, &in)
				switch in.FilePath {
				case "a.go":
					select {
					case <-startedA:
						close(startedA2) // second a.go
						<-release2
					default:
						close(startedA) // first a.go
						<-release1
					}
				case "b.go":
					close(startedB)
					<-release1
				}
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "e2", Name: "Edit", Input: editInput("b.go")},
		{Type: types.ContentTypeToolUse, ID: "e3", Name: "Edit", Input: editInput("a.go")},
	}

	go func() {
		// a.go and b.go should start in parallel; a.go#2 must wait
		<-startedA
		<-startedB
		close(release1) // release first a.go + b.go
		<-startedA2
		close(release2) // release second a.go
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.ToolResultBlocks))
	}
}

// TestFileConflict_QueuedTool_ResumesAfterConflictClears verifies that when a
// file-conflict tool finishes, the waiting tool starts.
func TestFileConflict_QueuedTool_ResumesAfterConflictClears(t *testing.T) {
	t.Parallel()

	editRelease := make(chan struct{})
	writeStarted := make(chan struct{})
	done := make(chan struct{})

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				<-editRelease
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
		"Write": &writeTool{concurrentTool{
			name:   "Write",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(writeStarted)
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "w1", Name: "Write", Input: writeInput("a.go")},
	}

	go func() {
		ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
		close(done)
	}()

	// Let Edit start, then release it. Write should follow.
	close(editRelease)

	select {
	case <-writeStarted:
		<-done
	case <-done:
		t.Error("ConcurrentToolLoop finished before Write started — file conflict not resolved")
	}
}

// TestFileConflict_NonEditWrite_NoFilePath verifies tools that aren't Edit/Write
// have empty FilePath and never trigger file conflict.
func TestFileConflict_NonEditWrite_NoFilePath(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 3)
	release := make(chan struct{})

	tools := map[string]tool.Tool{
		"Grep": &concurrentTool{
			name:   "Grep",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				started <- struct{}{}
				<-release
				return &tool.ToolResult{Data: "ok"}, nil
			},
		},
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				started <- struct{}{}
				<-release
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "g1", Name: "Grep", Input: json.RawMessage(`{"pattern":"foo"}`)},
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "g2", Name: "Grep", Input: json.RawMessage(`{"pattern":"bar"}`)},
	}

	// All 3 tools are concurrency-safe with no file conflict
	// (Grep has no file_path, only 1 Edit), so all should start.
	go func() {
		for range 3 {
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				return
			}
		}
		close(release)
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.ToolResultBlocks))
	}
}

// TestFileConflict_InvalidJSON_NoConflict verifies Edit with invalid JSON input
// has empty FilePath and doesn't conflict with other Edits.
func TestFileConflict_InvalidJSON_NoConflict(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 2)
	release := make(chan struct{})

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				started <- struct{}{}
				<-release
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: json.RawMessage(`{broken json`)},
		{Type: types.ContentTypeToolUse, ID: "e2", Name: "Edit", Input: editInput("a.go")},
	}

	// e1 has invalid JSON → empty FilePath → no file conflict with e2.
	// Both Edits should start in parallel.
	go func() {
		for range 2 {
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				return
			}
		}
		close(release)
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
}

// TestFileConflict_AllSameFile_FullSerialization verifies four Edits on the same
// file execute strictly sequentially.
func TestFileConflict_AllSameFile_FullSerialization(t *testing.T) {
	t.Parallel()

	const n = 4
	var mu sync.Mutex
	var order []string
	started := make([]chan struct{}, n)
	for i := range started {
		started[i] = make(chan struct{})
	}
	releases := make(chan struct{}, n)

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(input, &in)
				mu.Lock()
				idx := len(order)
				order = append(order, in.FilePath)
				mu.Unlock()
				close(started[idx])
				<-releases
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	var blocks []types.ContentBlock
	for i := range n {
		blocks = append(blocks, types.ContentBlock{
			Type:  types.ContentTypeToolUse,
			ID:    "e" + string(rune('0'+i)),
			Name:  "Edit",
			Input: editInput("a.go"),
		})
	}

	go func() {
		for i := range n {
			<-started[i]
			releases <- struct{}{}
		}
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != n {
		t.Fatalf("expected %d results, got %d", n, len(result.ToolResultBlocks))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != n {
		t.Fatalf("execution count = %d, want %d", len(order), n)
	}
	for _, f := range order {
		if f != "a.go" {
			t.Errorf("execution on wrong file: %q", f)
		}
	}
}

// TestFileConflict_ErrorClearsConflict verifies that when an Edit errors on a
// file, a queued Edit on the SAME file can start afterward (no deadlock).
func TestFileConflict_ErrorClearsConflict(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var callCount int
	var order []string

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(input, &in)
				mu.Lock()
				n := callCount
				callCount++
				order = append(order, in.FilePath)
				mu.Unlock()
				if n == 0 {
					return nil, errors.New("injected error")
				}
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "e2", Name: "Edit", Input: editInput("a.go")},
	}

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	if !result.ToolResultBlocks[0].IsError {
		t.Error("first Edit should have errored")
	}
	if result.ToolResultBlocks[1].IsError {
		t.Error("second Edit should have succeeded (error cleared conflict)")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("execution count = %d, want 2", len(order))
	}
}

// TestFileConflict_ColdStart_EmptyBlocks verifies the executor handles empty
// input without panic or events.
func TestFileConflict_ColdStart_EmptyBlocks(t *testing.T) {
	t.Parallel()

	var eventCount int
	result := ConcurrentToolLoop(context.Background(), map[string]tool.Tool{}, nil, nil, func(types.QueryEvent) {
		eventCount++
	})
	if len(result.ToolResultBlocks) != 0 {
		t.Errorf("expected 0 results, got %d", len(result.ToolResultBlocks))
	}
	if eventCount != 0 {
		t.Errorf("expected 0 events, got %d", eventCount)
	}
}

// TestFileConflict_MixedQueue_SafeAndUnsafe_ResumeAfterUnsafe verifies that after
// an unsafe tool completes, queued safe tools are properly re-evaluated.
func TestFileConflict_MixedQueue_SafeAndUnsafe_ResumeAfterUnsafe(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var order []string
	var editCounter int

	editStarted := make([]chan struct{}, 2)
	for i := range editStarted {
		editStarted[i] = make(chan struct{})
	}
	editReleases := make(chan struct{}, 2)
	bashStarted := make(chan struct{})
	bashRelease := make(chan struct{})

	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name:   "Bash",
			isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(bashStarted)
				<-bashRelease
				mu.Lock()
				order = append(order, "Bash")
				mu.Unlock()
				return &tool.ToolResult{Data: "ok"}, nil
			},
		},
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(input, &in)
				mu.Lock()
				editIdx := editCounter
				editCounter++
				order = append(order, "Edit:"+in.FilePath)
				mu.Unlock()
				close(editStarted[editIdx])
				<-editReleases
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "b1", Name: "Bash", Input: json.RawMessage(`{"command":"echo"}`)},
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "e2", Name: "Edit", Input: editInput("a.go")},
	}

	go func() {
		<-bashStarted
		close(bashRelease)
		<-editStarted[0]
		editReleases <- struct{}{}
		<-editStarted[1]
		editReleases <- struct{}{}
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.ToolResultBlocks))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("execution count = %d, want 3", len(order))
	}
	if order[0] != "Bash" {
		t.Errorf("first = %q, want Bash", order[0])
	}
	if order[1] != "Edit:a.go" || order[2] != "Edit:a.go" {
		t.Errorf("order = %v, want [Bash Edit:a.go Edit:a.go]", order)
	}
}

// TestFileConflict_DifferentFile_WriteWrite_Parallel verifies two Write tools on
// different files run in parallel.
func TestFileConflict_DifferentFile_WriteWrite_Parallel(t *testing.T) {
	t.Parallel()

	started1, started2 := make(chan struct{}), make(chan struct{})
	releases := make(chan struct{}, 2)

	tools := map[string]tool.Tool{
		"Write": &writeTool{concurrentTool{
			name:   "Write",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(input, &in)
				if in.FilePath == "a.go" {
					close(started1)
				} else {
					close(started2)
				}
				<-releases
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "w1", Name: "Write", Input: writeInput("a.go")},
		{Type: types.ContentTypeToolUse, ID: "w2", Name: "Write", Input: writeInput("b.go")},
	}

	go func() {
		<-started1
		<-started2
		releases <- struct{}{}
		releases <- struct{}{}
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
}

// TestFileConflict_SameFile_RaceDetector stress-tests the file conflict path
// under high concurrency — adds many tools simultaneously to expose races.
func TestFileConflict_SameFile_RaceDetector(t *testing.T) {
	t.Parallel()

	const n = 8
	var mu sync.Mutex
	var execCount int

	started := make([]chan struct{}, n)
	for i := range started {
		started[i] = make(chan struct{})
	}
	releases := make(chan struct{}, n)

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				mu.Lock()
				idx := execCount
				execCount++
				mu.Unlock()
				close(started[idx])
				<-releases
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}

	var blocks []types.ContentBlock
	for i := range n {
		blocks = append(blocks, types.ContentBlock{
			Type:  types.ContentTypeToolUse,
			ID:    "e" + string(rune('0'+i)),
			Name:  "Edit",
			Input: editInput("same.go"),
		})
	}

	go func() {
		for i := range n {
			<-started[i]
			releases <- struct{}{}
		}
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != n {
		t.Fatalf("expected %d results, got %d", n, len(result.ToolResultBlocks))
	}
	mu.Lock()
	defer mu.Unlock()
	if execCount != n {
		t.Fatalf("only %d/%d tools executed — deadlock", execCount, n)
	}
}

// TestFileConflict_FilePathRace_TwoFiles interleaved verifies no race between
// file-conflict detection and concurrent AddTool calls for different files.
func TestFileConflict_FilePathRace_TwoFiles(t *testing.T) {
	t.Parallel()

	const n = 6
	started := make([]chan struct{}, n)
	for i := range started {
		started[i] = make(chan struct{})
	}
	release := make(chan struct{})

	tools := map[string]tool.Tool{
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath  string `json:"file_path"`
					OldString string `json:"old_string"`
				}
				_ = json.Unmarshal(input, &in)
				idx := int(in.OldString[1] - '0')
				close(started[idx])
				<-release
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}

	var blocks []types.ContentBlock
	for i := range n {
		file := "a.go"
		if i%2 == 1 {
			file = "b.go"
		}
		blocks = append(blocks, types.ContentBlock{
			Type:  types.ContentTypeToolUse,
			ID:    "e" + string(rune('0'+i)),
			Name:  "Edit",
			Input: json.RawMessage(`{"file_path":"` + file + `","old_string":"x` + string(rune('0'+i)) + `","new_string":"y"}`),
		})
	}

	go func() {
		for i := range n {
			select {
			case <-started[i]:
			case <-time.After(5 * time.Second):
				close(release)
				return
			}
		}
		close(release)
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != n {
		t.Fatalf("expected %d results, got %d", len(result.ToolResultBlocks), n)
	}
}

// TestFileConflict_UnsafeBlocksQueuedSafe_ThenFileConflictSerializes verifies
// the full lifecycle: unsafe blocks → completes → safe tools start → same-file
// tools serialize.
func TestFileConflict_UnsafeBlocksQueuedSafe_ThenFileConflictSerializes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var order []string
	var editCounter int

	editStarted := make([]chan struct{}, 3)
	for i := range editStarted {
		editStarted[i] = make(chan struct{})
	}
	editReleases := make(chan struct{}, 3)
	bashStarted := make(chan struct{})
	bashRelease := make(chan struct{})

	tools := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name:   "Bash",
			isSafe: false,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(bashStarted)
				<-bashRelease
				mu.Lock()
				order = append(order, "Bash")
				mu.Unlock()
				return &tool.ToolResult{Data: "ok"}, nil
			},
		},
		"Edit": &editTool{concurrentTool{
			name:   "Edit",
			isSafe: true,
			callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				var in struct {
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(input, &in)
				mu.Lock()
				editIdx := editCounter
				editCounter++
				order = append(order, "Edit:"+in.FilePath)
				mu.Unlock()
				close(editStarted[editIdx])
				<-editReleases
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}},
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "b1", Name: "Bash", Input: json.RawMessage(`{"command":"echo"}`)},
		{Type: types.ContentTypeToolUse, ID: "e1", Name: "Edit", Input: editInput("shared.go")},
		{Type: types.ContentTypeToolUse, ID: "e2", Name: "Edit", Input: editInput("other.go")},
		{Type: types.ContentTypeToolUse, ID: "e3", Name: "Edit", Input: editInput("shared.go")},
	}

	go func() {
		<-bashStarted
		close(bashRelease)
		<-editStarted[0] // e1 (shared)
		editReleases <- struct{}{}
		<-editStarted[1] // e2 (other)
		editReleases <- struct{}{}
		<-editStarted[2] // e3 (shared, after e1)
		editReleases <- struct{}{}
	}()

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})
	if len(result.ToolResultBlocks) != 4 {
		t.Fatalf("expected 4 results, got %d", len(result.ToolResultBlocks))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 4 {
		t.Fatalf("execution count = %d, want 4", len(order))
	}
	if order[0] != "Bash" {
		t.Errorf("first = %q, want Bash", order[0])
	}
	if order[3] != "Edit:shared.go" {
		t.Errorf("last = %q, want Edit:shared.go", order[3])
	}
	sharedCount := 0
	for _, o := range order {
		if o == "Edit:shared.go" {
			sharedCount++
		}
	}
	if sharedCount != 2 {
		t.Errorf("shared.go count = %d, want 2", sharedCount)
	}
}

// stuckTool blocks forever on an empty channel, ignoring ctx cancel.
// Used to test that ExecuteAll respects rootCtx cancellation.
type stuckTool struct {
	name string
	done chan struct{} // blocks forever — never closed
}

func (t *stuckTool) Name() string                                { return t.name }
func (t *stuckTool) Aliases() []string                           { return nil }
func (t *stuckTool) Description(json.RawMessage) (string, error) { return "stuck", nil }
func (t *stuckTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *stuckTool) Call(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	<-t.done // blocks forever — never returns
	return nil, nil
}
func (t *stuckTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *stuckTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *stuckTool) IsDestructive(json.RawMessage) bool        { return true }
func (t *stuckTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *stuckTool) IsEnabled() bool                           { return true }
func (t *stuckTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *stuckTool) Prompt() string                            { return "" }
func (t *stuckTool) RenderResult(any) string                   { return "" }
func (t *stuckTool) MaxResultSize() int                        { return 50000 }

// TestExecuteAll_BlocksOnStuckTool_AfterCtxCancel verifies that ExecuteAll
// does NOT block waiting for a stuck tool after rootCtx is cancelled.
//
// The old code did `<-ch` even after `<-rootCtx.Done()`, so a stuck
// tool blocked the engine indefinitely.
func TestExecuteAll_BlocksOnStuckTool_AfterCtxCancel(t *testing.T) {
	t.Parallel()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	toolMap := map[string]tool.Tool{"stuck": &stuckTool{name: "stuck"}}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(_ types.QueryEvent) {}, rootCtx)

	signal := make(chan struct{})
	go func() {
		defer close(signal)
		_ = e.ExecuteAll([]types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "t1", Name: "stuck", Input: json.RawMessage(`{}`)},
		})
	}()

	// Wait for tool goroutine to enter Call.
	time.Sleep(10 * time.Millisecond) // REAL-TIME: let tool enter blocked state

	// Cancel rootCtx — ExecuteAll should return promptly, not block on stuck tool.
	rootCancel()

	select {
	case <-signal:
		// ExecuteAll returned — good, context cancel worked.
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteAll did not return after rootCtx cancel — stuck tool blocked engine")
	}
}

// ---------------------------------------------------------------------------
// executeTool → FormatWireBlocks integration tests (Step 8)
// ---------------------------------------------------------------------------

// wireBlocksTestTool implements tool.Tool AND tool.ToolWithWireBlocks.
type wireBlocksTestTool struct {
	testTool
	blocks    []types.ContentBlock
	callCount int
}

func (t *wireBlocksTestTool) FormatWireBlocks(_ any) []types.ContentBlock {
	t.callCount++
	return t.blocks
}

// runWireBlocksTool wires a wireBlocksTestTool through ExecuteAll and returns
// the single tool_result ContentBlock + emitted EventToolEnd (if any).
func runWireBlocksTool(t *testing.T, blocks []types.ContentBlock, callFn func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error)) (types.ContentBlock, types.QueryEvent) {
	t.Helper()

	wt := &wireBlocksTestTool{
		testTool: testTool{name: "WireTool", callFn: callFn},
		blocks:   blocks,
	}
	toolMap := map[string]tool.Tool{"WireTool": wt}
	tctx := &tool.ToolUseContext{}

	var capturedEvt types.QueryEvent
	emit := func(evt types.QueryEvent) {
		if evt.Type == types.EventToolEnd {
			capturedEvt = evt
		}
	}
	e := NewStreamingToolExecutor(toolMap, tctx, emit, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tool_1", Name: "WireTool", Input: json.RawMessage(`{}`)},
	})
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	return res.ToolResultBlocks[0], capturedEvt
}

func TestExecuteTool_ResultContentIsArray_FormattedViaFormatWireBlocks(t *testing.T) {
	t.Parallel()

	block, _ := runWireBlocksTool(t,
		[]types.ContentBlock{types.NewTextBlock("hello")},
		func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "ignored"}, nil
		},
	)
	if len(block.Content) == 0 {
		t.Fatal("block.Content is empty")
	}
	if block.Content[0] != '[' {
		t.Fatalf("block.Content[0] = %q, want '[' (array form). Full: %s", string(block.Content[0]), string(block.Content))
	}
	var got []types.ContentBlock
	if err := json.Unmarshal(block.Content, &got); err != nil {
		t.Fatalf("array unmarshal failed: %v (content=%s)", err, string(block.Content))
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Type != types.ContentTypeText {
		t.Fatalf("got[0].Type = %q, want %q", got[0].Type, types.ContentTypeText)
	}
	if got[0].Text != "[Tool spent 0.0s]hello" {
		t.Errorf("got[0].Text = %q, want %q", got[0].Text, "[Tool spent 0.0s]hello")
	}
}

func TestExecuteTool_ResultContentImageViaFormatWireBlocks(t *testing.T) {
	t.Parallel()

	imgBlock := types.NewImageBlock(types.ImageSource{
		Type:      "base64",
		MediaType: "image/png",
		Data:      "abc",
	})
	block, _ := runWireBlocksTool(t,
		[]types.ContentBlock{imgBlock},
		func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "ignored"}, nil
		},
	)
	if len(block.Content) == 0 {
		t.Fatal("block.Content is empty")
	}
	if block.Content[0] != '[' {
		t.Fatalf("block.Content[0] = %q, want '[' (array form)", string(block.Content[0]))
	}
	var got []types.ContentBlock
	if err := json.Unmarshal(block.Content, &got); err != nil {
		t.Fatalf("array unmarshal failed: %v (content=%s)", err, string(block.Content))
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Type != types.ContentTypeImage {
		t.Fatalf("got[0].Type = %q, want %q", got[0].Type, types.ContentTypeImage)
	}
}

func TestExecuteTool_EventToolEndOutputIsArray(t *testing.T) {
	t.Parallel()

	_, evt := runWireBlocksTool(t,
		[]types.ContentBlock{types.NewTextBlock("hello")},
		func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "ignored"}, nil
		},
	)
	if evt.Type != types.EventToolEnd {
		t.Fatalf("evt.Type = %q, want %q", evt.Type, types.EventToolEnd)
	}
	if evt.ToolResult == nil {
		t.Fatal("evt.ToolResult is nil")
	}
	if len(evt.ToolResult.Output) == 0 {
		t.Fatal("evt.ToolResult.Output is empty")
	}
	if evt.ToolResult.Output[0] != '[' {
		t.Fatalf("Output[0] = %q, want '[' (array form). Full: %s", string(evt.ToolResult.Output[0]), string(evt.ToolResult.Output))
	}
}

func TestExecuteTool_ErrorPathUsesArrayForm(t *testing.T) {
	t.Parallel()

	wt := &wireBlocksTestTool{
		testTool: testTool{
			name: "ErrTool",
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("boom")
			},
		},
		blocks: []types.ContentBlock{types.NewTextBlock("should-not-be-used")},
	}
	toolMap := map[string]tool.Tool{"ErrTool": wt}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(_ types.QueryEvent) {}, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tool_1", Name: "ErrTool", Input: json.RawMessage(`{}`)},
	})
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	block := res.ToolResultBlocks[0]
	if len(block.Content) == 0 {
		t.Fatal("block.Content is empty")
	}
	if block.Content[0] != '[' {
		t.Fatalf("error-path block.Content[0] = %q, want '[' (array form). Full: %s", string(block.Content[0]), string(block.Content))
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	if !strings.Contains(parsed[0].Text, "boom") {
		t.Errorf("error text should contain 'boom', got %q", parsed[0].Text)
	}
	if wt.callCount != 0 {
		t.Errorf("FormatWireBlocks call count = %d, want 0 (error path must bypass)", wt.callCount)
	}
}

// TestEmitToolError_UsesArrayForm drives a tool that returns an error with a
// non-zero elapsed time. The result block content must be array form, with the
// single text block prefixed by "[Tool spent Xs]" and ending with the error
// message.
func TestEmitToolError_UsesArrayForm(t *testing.T) {
	t.Parallel()

	wt := &wireBlocksTestTool{
		testTool: testTool{
			name: "BoomTool",
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("boom")
			},
		},
		blocks: []types.ContentBlock{types.NewTextBlock("should-not-be-used")},
	}
	toolMap := map[string]tool.Tool{"BoomTool": wt}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(_ types.QueryEvent) {}, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tool_1", Name: "BoomTool", Input: json.RawMessage(`{}`)},
	})
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	block := res.ToolResultBlocks[0]
	if len(block.Content) == 0 || block.Content[0] != '[' {
		t.Fatalf("error block.Content should start with '[', got %q", string(block.Content))
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	text := parsed[0].Text
	if !strings.HasPrefix(text, "[Tool spent ") {
		t.Errorf("error text should start with '[Tool spent ', got %q", text)
	}
	if !strings.HasSuffix(text, "boom") {
		t.Errorf("error text should end with 'boom', got %q", text)
	}
	if wt.callCount != 0 {
		t.Errorf("FormatWireBlocks call count = %d, want 0 (error path must bypass)", wt.callCount)
	}
}

func TestExecuteTool_NilResultUsesArrayForm(t *testing.T) {
	t.Parallel()
	nt := &testTool{
		name: "NilTool",
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
	}
	toolMap := map[string]tool.Tool{"NilTool": nt}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(_ types.QueryEvent) {}, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tool_nil", Name: "NilTool", Input: json.RawMessage(`{}`)},
	})
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	block := res.ToolResultBlocks[0]
	if block.IsError {
		t.Error("nil result should not be IsError")
	}
	if len(block.Content) == 0 || block.Content[0] != '[' {
		t.Fatalf("nil-result block.Content should start with '[', got %q", string(block.Content))
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	if parsed[0].Text != "null" {
		t.Errorf("expected inner text 'null', got %q", parsed[0].Text)
	}
}

func TestExtractErrMsg_ArrayForm(t *testing.T) {
	t.Parallel()
	// Array form: should return the inner text block's Text.
	arrayIn := json.RawMessage(`[{"type":"text","text":"User rejected tool use"}]`)
	if got := extractErrMsg(arrayIn); got != "User rejected tool use" {
		t.Errorf("array form: got %q, want %q", got, "User rejected tool use")
	}
	// Map form: existing behavior preserved.
	mapIn := json.RawMessage(`{"error":"something"}`)
	if got := extractErrMsg(mapIn); got != "something" {
		t.Errorf("map form: got %q, want %q", got, "something")
	}
	// Plain JSON string form: existing fallback returns raw bytes.
	strIn := json.RawMessage(`"plain string"`)
	if got := extractErrMsg(strIn); got != `"plain string"` {
		t.Errorf("string form: got %q, want %q", got, `"plain string"`)
	}
}
