package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/permission"
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
	// Deterministic rendezvous: each safe tool waits for the other to enter
	// callFn (with timeout). Under true parallelism both pass the rendezvous
	// quickly; under serialization the second tool cannot start before the
	// first finishes, so the first times out waiting for it. Wall-clock
	// timing assertions ("started within 20ms of each other") flaked on
	// loaded CI runs — goroutine scheduling latency regularly exceeded the
	// window.
	rendezvous := make(chan struct{}, 2)
	const waitPeer = 5 * time.Second

	tools := map[string]tool.Tool{
		"safe_a": &concurrentTool{
			name: "safe_a", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				rendezvous <- struct{}{}
				select {
				case <-rendezvous:
				case <-time.After(waitPeer): // REAL-TIME: serialized peer would exceed this
					return nil, errors.New("safe_a: peer did not start — tools ran serially")
				}
				return &tool.ToolResult{Data: "a"}, nil
			},
		},
		"safe_b": &concurrentTool{
			name: "safe_b", isSafe: true,
			callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				rendezvous <- struct{}{}
				select {
				case <-rendezvous:
				case <-time.After(waitPeer): // REAL-TIME: serialized peer would exceed this
					return nil, errors.New("safe_b: peer did not start — tools ran serially")
				}
				return &tool.ToolResult{Data: "b"}, nil
			},
		},
	}
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "safe_a", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "safe_b", Input: json.RawMessage(`{}`)},
	}
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.ToolResultBlocks))
	}
	// A serialized (buggy) run fails the rendezvous inside callFn and the
	// tool returns an error result — success payloads encode as JSON strings.
	for _, cb := range result.ToolResultBlocks {
		msg := extractErrMsg(cb.Content)
		switch cb.ToolUseID {
		case "tu_1":
			if msg != `"a"` {
				t.Errorf("safe_a result = %s, want \"a\" (rendezvous failed: %s)", msg, msg)
			}
		case "tu_2":
			if msg != `"b"` {
				t.Errorf("safe_b result = %s, want \"b\" (rendezvous failed: %s)", msg, msg)
			}
		}
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
	if parsed[0].Text != userRejectMessage {
		t.Errorf("expected userRejectMessage, got %q", parsed[0].Text)
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
	var activeShared int
	var maxActiveShared int

	bashStarted := make(chan struct{})
	bashRelease := make(chan struct{})
	editFinish := make(chan struct{})
	// Buffer 3: under correct serialization e3 signals after editFinish is
	// already closed (send must not block).
	editsStarted := make(chan struct{}, 3)

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
				order = append(order, "Edit:"+in.FilePath)
				isShared := in.FilePath == "shared.go"
				if isShared {
					activeShared++
					if activeShared > maxActiveShared {
						maxActiveShared = activeShared
					}
				}
				mu.Unlock()
				editsStarted <- struct{}{}
				<-editFinish
				mu.Lock()
				if isShared {
					activeShared--
				}
				mu.Unlock()
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
		// Unsafe Bash completes first; edits may enter in any legal order.
		close(bashRelease)
		// Under correct same-file serialization only e1(shared) + e2(other)
		// can be running simultaneously — e3(shared) starts after e1
		// completes. So exactly TWO starts can arrive before any release;
		// waiting for a third would deadlock the test (e3 waits for e1's
		// completion which waits for editFinish).
		for range 2 {
			<-editsStarted
		}
		close(editFinish)
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
	sharedCount := 0
	for _, o := range order {
		if o == "Edit:shared.go" {
			sharedCount++
		}
	}
	if sharedCount != 2 {
		t.Errorf("shared.go count = %d, want 2", sharedCount)
	}
	// Core invariant: the two shared.go edits never ran concurrently.
	if maxActiveShared > 1 {
		t.Errorf("same-file edits overlapped: max concurrent shared.go edits = %d, want 1", maxActiveShared)
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
		name: "WireTool", callFn: callFn,
		blocks: blocks,
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
	if got[0].Text != "hello" {
		t.Errorf("got[0].Text = %q, want %q", got[0].Text, "hello")
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
		name: "ErrTool",
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errors.New("boom")
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

// TestEmitToolError_UsesArrayForm drives a tool that returns an error.
// The result block content must be array form with the error message in a single text block.
func TestEmitToolError_UsesArrayForm(t *testing.T) {
	t.Parallel()

	wt := &wireBlocksTestTool{
		name: "BoomTool",
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errors.New("boom")
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

// TestExecuteTool_UserCancelReturnsUnifiedMessage verifies that when a tool
// is cancelled mid-execution by the user (rootCtx cancelled), the tool_result
// uses the unified "Tool execution was cancelled by the user." message instead
// of the raw context.Canceled error string. The IsError flag stays true.
func TestExecuteTool_UserCancelReturnsUnifiedMessage(t *testing.T) {
	t.Parallel()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	toolMap := map[string]tool.Tool{
		"slow": &testTool{
			name: "slow",
			callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				rootCancel() // simulate user ESC during tool execution
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	tctx := &tool.ToolUseContext{}

	var capturedEvt types.QueryEvent
	var emitMu sync.Mutex
	emit := func(evt types.QueryEvent) {
		if evt.Type == types.EventToolEnd {
			emitMu.Lock()
			capturedEvt = evt
			emitMu.Unlock()
		}
	}

	e := NewStreamingToolExecutor(toolMap, tctx, emit, rootCtx)
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_cancel", Name: "slow", Input: json.RawMessage(`{}`)},
	})

	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	block := res.ToolResultBlocks[0]
	if !block.IsError {
		t.Fatal("IsError must be true for cancelled tool")
	}

	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("unmarshal error block: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text block, got %+v", parsed)
	}
	wantMsg := userRejectMessage
	if parsed[0].Text != wantMsg {
		t.Errorf("cancel message = %q, want %q", parsed[0].Text, wantMsg)
	}

	emitMu.Lock()
	defer emitMu.Unlock()
	if capturedEvt.Type != types.EventToolEnd {
		t.Fatalf("captured event type = %v, want EventToolEnd", capturedEvt.Type)
	}
	if capturedEvt.ToolResult == nil {
		t.Fatal("captured event has nil ToolResult")
	}
	if !capturedEvt.ToolResult.IsError {
		t.Error("captured event IsError must be true")
	}
	if capturedEvt.ToolResult.DisplayOutput == "" {
		t.Error("DisplayOutput must be non-empty for cancelled tool")
	}
}

// TestExecuteTool_UserCancel_SignalKilledError verifies that when a tool
// (like Bash) is killed via signal during user cancel, the tool_result still
// gets the unified message. Bash returns "signal: killed" (not
// context.Canceled) because the process group gets SIGKILL — but the engine
// should detect user-cancel via rootCtx.Err() and override regardless.
func TestExecuteTool_UserCancel_SignalKilledError(t *testing.T) {
	t.Parallel()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	toolMap := map[string]tool.Tool{
		"Bash": &testTool{
			name: "Bash",
			callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				rootCancel() // user ESC
				<-ctx.Done()
				// Simulate what os/exec returns after SIGKILL: signal error,
				// NOT context.Canceled.
				return nil, errors.New("signal: killed")
			},
		},
	}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(types.QueryEvent) {}, rootCtx)
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_bash_sig", Name: "Bash", Input: json.RawMessage(`{}`)},
	})

	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	block := res.ToolResultBlocks[0]
	if !block.IsError {
		t.Fatal("IsError must be true")
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantMsg := userRejectMessage
	if parsed[0].Text != wantMsg {
		t.Errorf("cancel message = %q, want %q (signal: killed should be overridden when rootCtx cancelled)", parsed[0].Text, wantMsg)
	}
}

// TestExecuteTool_UserCancel_BashReturnsSuccessWithExit137 verifies the
// post-execution abort check: when a tool (like Bash) returns err=nil with
// a non-zero exit code after being killed mid-execution, the engine detects
// rootCtx cancellation and routes through emitToolError instead of treating
// it as a normal result.
func TestExecuteTool_UserCancel_BashReturnsSuccessWithExit137(t *testing.T) {
	t.Parallel()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	toolMap := map[string]tool.Tool{
		"Bash": &testTool{
			name: "Bash",
			callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				rootCancel() // user ESC
				<-ctx.Done()
				// Bash returns err=nil with exitCode=137 (SIGKILL), NOT context.Canceled
				return &tool.ToolResult{
					Data: &struct {
						Stdout   string `json:"stdout"`
						ExitCode int    `json:"exitCode"`
					}{Stdout: "", ExitCode: 137},
				}, nil
			},
		},
	}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(types.QueryEvent) {}, rootCtx)
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_bash_137", Name: "Bash", Input: json.RawMessage(`{}`)},
	})

	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	block := res.ToolResultBlocks[0]
	if !block.IsError {
		t.Fatal("IsError must be true — post-exec abort should route through emitToolError")
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed[0].Text != userRejectMessage {
		t.Errorf("message = %q, want userRejectMessage (Bash exitCode=137 + rootCtx cancel should be treated as user abort)", parsed[0].Text)
	}
}

// TestExecuteTool_NormalErrorUnchanged verifies that non-cancel errors are
// NOT rewritten — only user-cancel gets the unified message.
func TestExecuteTool_NormalErrorUnchanged(t *testing.T) {
	t.Parallel()

	toolMap := map[string]tool.Tool{
		"boom": &testTool{
			name: "boom",
			callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
				return nil, errors.New("command failed: exit code 1")
			},
		},
	}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(types.QueryEvent) {}, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_boom", Name: "boom", Input: json.RawMessage(`{}`)},
	})

	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("len(ToolResultBlocks) = %d, want 1", len(res.ToolResultBlocks))
	}
	block := res.ToolResultBlocks[0]
	if !block.IsError {
		t.Fatal("IsError must be true for error tool")
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed[0].Text != "command failed: exit code 1" {
		t.Errorf("normal error = %q, want original error message unchanged", parsed[0].Text)
	}
}

// TestExecuteTool_SiblingCancelUsesUnifiedMessage verifies that when a tool
// is cancelled by a Bash sibling error mid-execution (NOT user interrupt),
// the tool_result uses the unified "Cancelled: parallel tool call errored"
// message — aligned with TS StreamingToolExecutor.ts.
func TestExecuteTool_SiblingCancelUsesUnifiedMessage(t *testing.T) {
	t.Parallel()

	slowEntered := make(chan struct{})
	toolMap := map[string]tool.Tool{
		"Bash": &concurrentTool{
			name: "Bash", isSafe: true,
			callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
				<-slowEntered
				return nil, errors.New("bash boom")
			},
		},
		"slow": &concurrentTool{
			name: "slow", isSafe: true,
			callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
				close(slowEntered)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	tctx := &tool.ToolUseContext{}
	e := NewStreamingToolExecutor(toolMap, tctx, func(types.QueryEvent) {}, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_bash", Name: "Bash", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "tu_slow", Name: "slow", Input: json.RawMessage(`{}`)},
	})

	if len(res.ToolResultBlocks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res.ToolResultBlocks))
	}
	slowBlock := &res.ToolResultBlocks[1]
	if !slowBlock.IsError {
		t.Fatal("IsError must be true for sibling-cancelled tool")
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(slowBlock.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantMsg := "Cancelled: parallel tool call Bash errored"
	if parsed[0].Text != wantMsg {
		t.Errorf("sibling-cancel message = %q, want %q", parsed[0].Text, wantMsg)
	}
}

// ---------------------------------------------------------------------------
// Streaming tool executor pins
// ---------------------------------------------------------------------------

// emitRecorder collects executor emissions.
type emitRecorder struct {
	mu  sync.Mutex
	evs []types.QueryEvent
}

func (c *emitRecorder) fn(evt types.QueryEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evs = append(c.evs, evt)
}

func (c *emitRecorder) all() []types.QueryEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]types.QueryEvent(nil), c.evs...)
}

// only returns the single event of the given type, failing the test if the
// count is not exactly one.
func (c *emitRecorder) only(t *testing.T, kind types.QueryEventType) types.QueryEvent {
	t.Helper()
	var hits []types.QueryEvent
	for _, e := range c.all() {
		if e.Type == kind {
			hits = append(hits, e)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("events of type %s = %d, want exactly 1 (total events: %d)", kind, len(hits), len(c.all()))
	}
	return hits[0]
}

// askTap returns an emit function that records like fn and additionally hands
// every Ask event carrying a response channel to the returned channel, so
// tests can block on the dialog instead of polling the collector.
func (c *emitRecorder) askTap() (chan *types.AskEvent, func(types.QueryEvent)) {
	asks := make(chan *types.AskEvent, 4)
	return asks, func(evt types.QueryEvent) {
		c.fn(evt)
		if evt.Ask != nil && evt.Ask.ResponseCh != nil {
			asks <- evt.Ask
		}
	}
}

// fakeTool is a configurable tool.Tool for the pins below.
type fakeTool struct {
	name       string
	safe       bool
	panicSafe  bool
	callFn     func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error)
	renderFn   func(any) string
	maxSize    int
	interrupt  tool.InterruptBehavior
	deferred   bool
	searchRead *tool.SearchReadKind
}

func (t *fakeTool) Name() string                                { return t.name }
func (t *fakeTool) Aliases() []string                           { return nil }
func (t *fakeTool) Description(json.RawMessage) (string, error) { return t.name + " desc", nil }
func (t *fakeTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *fakeTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *fakeTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *fakeTool) IsReadOnly(json.RawMessage) bool    { return true }
func (t *fakeTool) IsDestructive(json.RawMessage) bool { return false }
func (t *fakeTool) IsConcurrencySafe(json.RawMessage) bool {
	if t.panicSafe {
		panic("concurrency probe exploded")
	}
	return t.safe
}
func (t *fakeTool) IsEnabled() bool { return true }
func (t *fakeTool) InterruptBehavior() tool.InterruptBehavior {
	if t.interrupt != 0 {
		return t.interrupt
	}
	return tool.InterruptCancel
}
func (t *fakeTool) Prompt() string     { return "" }
func (t *fakeTool) MaxResultSize() int { return t.maxSize }
func (t *fakeTool) IsDeferred() bool   { return t.deferred }
func (t *fakeTool) RenderResult(data any) string {
	if t.renderFn != nil {
		return t.renderFn(data)
	}
	return ""
}
func (t *fakeTool) IsSearchOrRead(json.RawMessage) tool.SearchReadKind {
	if t.searchRead != nil {
		return *t.searchRead
	}
	return tool.SearchReadKind{}
}

// slogCapture captures the package-level slog output for the pins that assert a
// diagnostic exists. The default logger is swapped once for a fan-out handler,
// so several pins capture concurrently and the original output stays intact.
// Records are global, so every lookup must be discriminated by attributes —
// matching on the message alone would accept another test's identical log call.
type slogCapture struct {
	mu    sync.Mutex
	calls []slogCall
}

type slogCall struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (h *slogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *slogCapture) Handle(_ context.Context, rec slog.Record) error {
	attrs := map[string]any{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, slogCall{level: rec.Level, msg: rec.Message, attrs: attrs})
	return nil
}

func (h *slogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *slogCapture) WithGroup(string) slog.Handler      { return h }

func (h *slogCapture) all(msg string) []slogCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slogCall
	for _, c := range h.calls {
		if c.msg == msg {
			out = append(out, c)
		}
	}
	return out
}

// acceptAny matches any record with the requested message.
func acceptAny(map[string]any) bool { return true }

func (h *slogCapture) find(msg string, want func(map[string]any) bool) (slogCall, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if c.msg == msg && want(c.attrs) {
			return c, true
		}
	}
	return slogCall{}, false
}

// slogMux fans every record out to the capture handlers registered by the
// running tests, then to the handler that was default before the first pin.
// All slog-capturing tests must register here: a direct slog.SetDefault swap
// would starve the mux's captures of records for the swap's duration.
type slogMux struct {
	mu       sync.Mutex
	captures []slog.Handler
	passthru slog.Handler
}

func (m *slogMux) Enabled(ctx context.Context, l slog.Level) bool {
	m.mu.Lock()
	captures := append([]slog.Handler(nil), m.captures...)
	passthru := m.passthru
	m.mu.Unlock()
	for _, c := range captures {
		if c.Enabled(ctx, l) {
			return true
		}
	}
	return passthru.Enabled(ctx, l)
}

func (m *slogMux) Handle(ctx context.Context, rec slog.Record) error {
	m.mu.Lock()
	captures := append([]slog.Handler(nil), m.captures...)
	passthru := m.passthru
	m.mu.Unlock()
	for _, c := range captures {
		_ = c.Handle(ctx, rec)
	}
	return passthru.Handle(ctx, rec)
}

func (m *slogMux) WithAttrs([]slog.Attr) slog.Handler { return m }
func (m *slogMux) WithGroup(string) slog.Handler      { return m }

var (
	slogMuxOnce   sync.Once
	sharedSlogMux *slogMux
)

func captureSlog(t *testing.T) *slogCapture {
	t.Helper()
	slogMuxOnce.Do(func() {
		// Not slog.Default().Handler(): that wrapper resolves the current
		// default at call time and would route straight back into the mux.
		sharedSlogMux = &slogMux{passthru: slog.NewTextHandler(os.Stderr, nil)}
		slog.SetDefault(slog.New(sharedSlogMux))
	})
	h := &slogCapture{}
	sharedSlogMux.mu.Lock()
	sharedSlogMux.captures = append(sharedSlogMux.captures, h)
	sharedSlogMux.mu.Unlock()
	t.Cleanup(func() {
		sharedSlogMux.mu.Lock()
		defer sharedSlogMux.mu.Unlock()
		for i, c := range sharedSlogMux.captures {
			if c == h {
				sharedSlogMux.captures = append(sharedSlogMux.captures[:i], sharedSlogMux.captures[i+1:]...)
				break
			}
		}
	})
	return h
}

// attachSlogCapture registers an existing handler on the shared mux. Tests whose
// capture type carries its own find API use this instead of swapping the
// global default — a swap starves every mux-registered capture of records for
// the swap's duration and makes the whole package's log assertions flaky.
func attachSlogCapture(t *testing.T, h slog.Handler) {
	t.Helper()
	slogMuxOnce.Do(func() {
		sharedSlogMux = &slogMux{passthru: slog.NewTextHandler(os.Stderr, nil)}
		slog.SetDefault(slog.New(sharedSlogMux))
	})
	sharedSlogMux.mu.Lock()
	sharedSlogMux.captures = append(sharedSlogMux.captures, h)
	sharedSlogMux.mu.Unlock()
	t.Cleanup(func() {
		sharedSlogMux.mu.Lock()
		defer sharedSlogMux.mu.Unlock()
		for i, c := range sharedSlogMux.captures {
			if c == h {
				sharedSlogMux.captures = append(sharedSlogMux.captures[:i], sharedSlogMux.captures[i+1:]...)
				break
			}
		}
	})
}

// toolResultText decodes a tool_result block's array-form content into the
// single text payload it carries.
func toolResultText(t *testing.T, block types.ContentBlock) string {
	t.Helper()
	var blocks []types.ContentBlock
	if err := json.Unmarshal(block.Content, &blocks); err != nil {
		t.Fatalf("result content is not array form: %v (%s)", err, string(block.Content))
	}
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeText {
		t.Fatalf("result content = %+v, want one text block", blocks)
	}
	return blocks[0].Text
}

// TestSettersAssignTheirFields pins the plain setters that have no other
// read path in the executor.
func TestSettersAssignTheirFields(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	e.SetMemoryDir("/tmp/mem-dir")
	e.SetHooks(nil, "sess-42")
	if e.memoryDir != "/tmp/mem-dir" {
		t.Errorf("memoryDir = %q, want %q", e.memoryDir, "/tmp/mem-dir")
	}
	if e.sessionID != "sess-42" {
		t.Errorf("sessionID = %q, want %q", e.sessionID, "sess-42")
	}
	e.SetAssistantContent([]types.ContentBlock{types.NewTextBlock("assistant mid-stream")})
	ctx := e.buildToolCtx("tu_1")
	if len(ctx.AssistantContent) != 1 || ctx.AssistantContent[0].Text != "assistant mid-stream" {
		t.Errorf("AssistantContent = %+v, want the block set through SetAssistantContent", ctx.AssistantContent)
	}
	e.SetMessages([]types.Message{calibTextMessage("history")})
	ctx = e.buildToolCtx("tu_1")
	if len(ctx.Messages) != 1 {
		t.Errorf("Messages = %+v, want the history set through SetMessages", ctx.Messages)
	}
}

// TestHookDispatchCtxWrapsOnlyWithARunner pins both halves of the js-runner
// pin. Without a runner the ctx must come back untouched: dispatch's
// global-default fallback reads the same ctx key, so a wrapped nil runner is
// indistinguishable downstream and the untouched ctx is the only thing a
// caller can observe. With a runner it must travel on the ctx handed to the
// tool, whose own hook dispatches run on exactly that ctx.
func TestHookDispatchCtxWrapsOnlyWithARunner(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	base := t.Context()
	if got := e.hookDispatchCtx(base); got != base {
		t.Errorf("hookDispatchCtx without a runner returned %v, want the caller's ctx untouched", got)
	}
	runner := &countingJsRunner{}
	e.SetJsRunner(runner)
	got := e.hookDispatchCtx(base)
	if got == base {
		t.Fatal("hookDispatchCtx with a runner returned the bare ctx, want the runner pinned onto it")
	}
	if hooks.JsRunnerFrom(got) != runner {
		t.Errorf("JsRunnerFrom(dispatch ctx) = %v, want the executor's runner", hooks.JsRunnerFrom(got))
	}
}

// TestDiscardSetsFlagAndReleasesLock pins both halves of Discard: the
// discarded flag must be set and the mutex must not be left held.
func TestDiscardSetsFlagAndReleasesLock(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	e.Discard()
	e.mu.Lock()
	discarded := e.discarded
	e.mu.Unlock()
	if !discarded {
		t.Error("Discard() left discarded=false — emissions and abort reasons keep working")
	}
	if !e.mu.TryLock() {
		t.Error("Discard() returned with e.mu still held — its Unlock is not running")
	}
	e.mu.Unlock()
	if e.siblingCtx.Err() == nil {
		t.Error("Discard() did not cancel the sibling context")
	}
}

// TestAskUserCacheShortCircuits pins the session-allow cache in front of
// the dialog, including the re-check after the ask mutex is taken.
func TestAskUserCacheShortCircuits(t *testing.T) {
	t.Parallel()
	tt := &TrackedTool{ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}

	// Cache hit on the tool name: no dialog may be emitted.
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev.fn, context.Background())
	e.sessionAllowed = map[string]bool{"Bash": true}
	if got := e.askUser(tt, permission.Decision{Action: permission.ActionAsk, Message: "why"}, ""); got != types.DecisionAllow {
		t.Errorf("cached decision = %q, want allow", got)
	}
	if len(ev.all()) != 0 {
		t.Errorf("cached allow emitted %d events, want 0", len(ev.all()))
	}

	// The cache key is name + ":" + matched content, so a content-scoped entry
	// only short-circuits asks carrying that same content.
	ev2 := &emitRecorder{}
	e2 := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev2.fn, context.Background())
	e2.sessionAllowed = map[string]bool{"Bash:ls": true}
	if got := e2.askUser(tt, permission.Decision{Action: permission.ActionAsk, Message: "why"}, "ls"); got != types.DecisionAllow {
		t.Errorf("content-keyed cache decision = %q, want allow", got)
	}
	if len(ev2.all()) != 0 {
		t.Errorf("content-keyed cache emitted %d events, want 0", len(ev2.all()))
	}

	// The ask mutex must be released even though the dialog path returned.
	if !e.askMu.TryLock() {
		t.Error("askUser returned with askMu still held")
	}
	e.askMu.Unlock()
}

// TestAskEventCarriesItsFields pins the permission dialog payload.
func TestAskEventCarriesItsFields(t *testing.T) {
	t.Parallel()
	ruleContent := "rm *"
	decision := permission.Decision{
		Action:  permission.ActionAsk,
		Message: "Bash needs approval",
		Rule: &permission.Rule{
			Source: "project",
			Value:  permission.RuleValue{ToolName: "Bash", RuleContent: &ruleContent},
		},
	}
	tt := &TrackedTool{ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"rm -rf /tmp/x"}`)}

	ev := &emitRecorder{}
	asks, emit := ev.askTap()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, emit, context.Background())
	done := make(chan types.UserDecision, 1)
	go func() {
		done <- e.askUser(tt, decision, "rm *")
	}()
	var ask *types.AskEvent
	select {
	case ask = <-asks:
	case <-time.After(5 * time.Second):
		t.Fatal("no AskEvent emitted")
	}
	if ask.Kind != types.AskPermission {
		t.Errorf("AskEvent.Kind = %v, want %v", ask.Kind, types.AskPermission)
	}
	if ask.ToolName != "Bash" {
		t.Errorf("AskEvent.ToolName = %q, want Bash", ask.ToolName)
	}
	if string(ask.Input) != `{"command":"rm -rf /tmp/x"}` {
		t.Errorf("AskEvent.Input = %s, want the tool input", string(ask.Input))
	}
	if ask.Message != "Bash needs approval" {
		t.Errorf("AskEvent.Message = %q, want the decision message", ask.Message)
	}
	if !strings.Contains(ask.RuleDetail, "Bash(rm *) from project settings") {
		t.Errorf("AskEvent.RuleDetail = %q, want the rule rendering", ask.RuleDetail)
	}
	ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
	if got := <-done; got != types.DecisionDeny {
		t.Errorf("askUser returned %q, want deny", got)
	}
}

// TestRichResultDataGates pins every exclusion of the rich-data slot.
func TestRichResultDataGates(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{"Edit": &fakeTool{name: "Edit"}}
	e := NewStreamingToolExecutor(tools, nil, nil, context.Background())

	data := map[string]any{"path": "/x"}
	ok := func(tt *TrackedTool) bool {
		_, ok := e.richResultData(tt)
		return ok
	}
	if !ok(&TrackedTool{Name: "Edit", Result: &tool.ToolResult{Data: data}}) {
		t.Error("a successful Edit result must carry rich data")
	}
	if ok(&TrackedTool{Name: "Edit", Err: errors.New("boom"), Result: &tool.ToolResult{Data: data}}) {
		t.Error("an errored tool must not carry rich data")
	}
	if ok(&TrackedTool{Name: "Edit", Result: nil}) {
		t.Error("a nil result must not carry rich data")
	}
	if ok(&TrackedTool{Name: "Edit", Result: &tool.ToolResult{Data: nil}}) {
		t.Error("a nil Data must not carry rich data")
	}
	if ok(&TrackedTool{Name: "Ghost", Result: &tool.ToolResult{Data: data}}) {
		t.Error("a tool absent from the map must not carry rich data")
	}
	if ok(&TrackedTool{Name: "Bash", Result: &tool.ToolResult{Data: "plain"}}) {
		t.Error("a wire-plaintext tool must not carry rich data")
	}
	if ok(&TrackedTool{Name: "Edit", Result: &tool.ToolResult{Data: &types.SubQueryResult{AsyncLaunched: true}}}) {
		t.Error("an async background result must not carry rich data")
	}
}

// TestExtractFilePathOnlyForEditWrite pins the tool-name gate and its
// exact name matching.
func TestExtractFilePathOnlyForEditWrite(t *testing.T) {
	t.Parallel()
	in := json.RawMessage(`{"file_path":"/tmp/a.go"}`)
	if got := extractFilePath("Edit", in); got != "/tmp/a.go" {
		t.Errorf("extractFilePath(Edit) = %q, want /tmp/a.go", got)
	}
	if got := extractFilePath("Write", in); got != "/tmp/a.go" {
		t.Errorf("extractFilePath(Write) = %q, want /tmp/a.go", got)
	}
	for _, name := range []string{"Read", "Grep", "Bash", ""} {
		if got := extractFilePath(name, in); got != "" {
			t.Errorf("extractFilePath(%q) = %q, want empty", name, got)
		}
	}
	if got := extractFilePath("Edit", json.RawMessage(`not json`)); got != "" {
		t.Errorf("extractFilePath(Edit, invalid json) = %q, want empty", got)
	}
	// Unmarshal keeps the value decoded before a later type error, so the
	// guard is what stops a partially-decoded path from reaching conflict
	// tracking for a call the tool itself is going to reject.
	if got := extractFilePath("Edit", json.RawMessage(`{"file_path":"/tmp/a.go","file_path":123}`)); got != "" {
		t.Errorf("extractFilePath(Edit, partial decode) = %q, want the partial path dropped", got)
	}
}

// TestCanExecuteToolFileConflict pins the same-file serialization without
// letting an empty path collide with another empty path.
func TestCanExecuteToolFileConflict(t *testing.T) {
	t.Parallel()
	newSafe := func(path string) *TrackedTool {
		return &TrackedTool{FilePath: path, IsConcurrencySafe: true, Status: StatusQueued}
	}
	cases := []struct {
		name string
		busy *TrackedTool
		want bool
	}{
		{"no path on either side", &TrackedTool{Status: StatusExecuting, IsConcurrencySafe: true}, true},
		{"different files", &TrackedTool{Status: StatusExecuting, IsConcurrencySafe: true, FilePath: "/a"}, true},
		{"same file", &TrackedTool{Status: StatusExecuting, IsConcurrencySafe: true, FilePath: "/a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := ""
			if tc.name == "different files" {
				path = "/b"
			}
			if tc.name == "same file" {
				path = "/a"
			}
			e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
			e.tools = []*TrackedTool{tc.busy}
			if got := e.canExecuteTool(newSafe(path)); got != tc.want {
				t.Errorf("canExecuteTool(%s, path %q) = %v, want %v", tc.name, path, got, tc.want)
			}
		})
	}
}

// TestAbortReasonForDiscarded pins the discard branch of getAbortReason.
func TestAbortReasonForDiscarded(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	e.mu.Lock()
	e.discarded = true
	got := e.getAbortReason(&fakeTool{name: "x"})
	e.mu.Unlock()
	if got != AbortReasonStreamingFallback {
		t.Errorf("getAbortReason(discarded) = %q, want %q", got, AbortReasonStreamingFallback)
	}
}

// TestToolDescriptionTruncation pins the 40-char summary cut at its
// boundary and the malformed-input fallback.
func TestToolDescriptionTruncation(t *testing.T) {
	t.Parallel()
	exact := strings.Repeat("a", 40)
	if got := getToolDescription(&TrackedTool{Name: "Bash", Input: json.RawMessage(`{"command":"` + exact + `"}`)}); got != "Bash("+exact+")" {
		t.Errorf("40-char command description = %q, want no truncation", got)
	}
	over := strings.Repeat("a", 41)
	want := "Bash(" + strings.Repeat("a", 40) + "…)"
	if got := getToolDescription(&TrackedTool{Name: "Bash", Input: json.RawMessage(`{"command":"` + over + `"}`)}); got != want {
		t.Errorf("41-char command description = %q, want %q", got, want)
	}
	if got := getToolDescription(&TrackedTool{Name: "Bash", Input: json.RawMessage(`{`)}); got != "Bash" {
		t.Errorf("malformed input description = %q, want the bare tool name", got)
	}
	if got := getToolDescription(&TrackedTool{Name: "Grep", Input: json.RawMessage(`{"pattern":"x"}`)}); got != "Grep(x)" {
		t.Errorf("pattern description = %q, want Grep(x)", got)
	}
}

// TestMarshalBlocksFailureFallsBack pins the "[]" fallback for content
// that cannot be marshalled.
func TestMarshalBlocksFailureFallsBack(t *testing.T) {
	t.Parallel()
	bad := []types.ContentBlock{{Type: types.ContentTypeText, Content: json.RawMessage(`{broken`)}}
	got := marshalBlocks(bad)
	if string(got) != "[]" {
		t.Errorf("marshalBlocks(unmarshalable) = %s, want []", string(got))
	}
	if string(marshalBlocks(nil)) != "[]" {
		t.Error("marshalBlocks(nil) must be []")
	}
	good := marshalBlocks([]types.ContentBlock{types.NewTextBlock("hi")})
	if !strings.HasPrefix(string(good), `[{"type":"text"`) {
		t.Errorf("marshalBlocks(text) = %s, want the encoded array", string(good))
	}
}

// TestExtractErrMsgShapes pins each accepted shape of the error payload.
func TestExtractErrMsgShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{`[{"type":"text","text":"array text"}]`, "array text"},
		{`{"error":"map error"}`, "map error"},
		{`plain bytes`, "plain bytes"},
		{`[]`, `[]`},
		{``, ``},
		{`[{"type":"image"}]`, `[{"type":"image"}]`},
	}
	for _, c := range cases {
		if got := extractErrMsg(json.RawMessage(c.in)); got != c.want {
			t.Errorf("extractErrMsg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBuildToolCtxOverrides pins which base-context fields the executor
// replaces and which it leaves alone.
func TestBuildToolCtxOverrides(t *testing.T) {
	t.Parallel()
	base := tool.ToolUseContext{
		ToolUseID:        "stale",
		Messages:         []types.Message{calibTextMessage("base history")},
		AssistantContent: []types.ContentBlock{types.NewTextBlock("base assistant")},
		WorkingDir:       "/base",
	}
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, &base, nil, context.Background())

	ctx := e.buildToolCtx("tu_9")
	if ctx.ToolUseID != "tu_9" {
		t.Errorf("ToolUseID = %q, want the per-call id", ctx.ToolUseID)
	}
	if len(ctx.Messages) != 1 || ctx.Messages[0].Content[0].Text != "base history" {
		t.Errorf("Messages = %+v, want the base context's history when the executor has none", ctx.Messages)
	}
	if len(ctx.AssistantContent) != 1 || ctx.AssistantContent[0].Text != "base assistant" {
		t.Errorf("AssistantContent = %+v, want the base context's content when the executor has none", ctx.AssistantContent)
	}

	// With live state, both are replaced.
	e.SetMessages([]types.Message{calibTextMessage("live history")})
	e.SetAssistantContent([]types.ContentBlock{types.NewTextBlock("live assistant")})
	ctx = e.buildToolCtx("tu_10")
	if ctx.Messages[0].Content[0].Text != "live history" {
		t.Errorf("Messages = %+v, want the executor's live history", ctx.Messages)
	}
	if ctx.AssistantContent[0].Text != "live assistant" {
		t.Errorf("AssistantContent = %+v, want the executor's live content", ctx.AssistantContent)
	}

	// Nil base: the context is built from the executor alone.
	e2 := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	e2.SetMessages([]types.Message{calibTextMessage("only")})
	ctx2 := e2.buildToolCtx("tu_11")
	if ctx2.ToolUseID != "tu_11" || len(ctx2.Messages) != 1 {
		t.Errorf("nil-base context = %+v, want the id and the executor history", ctx2)
	}

	// The live working-dir supplier wins over the snapshot.
	e3 := NewStreamingToolExecutor(map[string]tool.Tool{}, &base, nil, context.Background())
	e3.SetWorkingDirSupplier(func() string { return "/live" })
	if got := e3.buildToolCtx("tu_12").WorkingDir; got != "/live" {
		t.Errorf("WorkingDir = %q, want /live (supplier wins)", got)
	}
}

// TestOnAskInputEmitsInputEvent pins the OnAskInput wiring on the tool
// context.
func TestOnAskInputEmitsInputEvent(t *testing.T) {
	t.Parallel()
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev.fn, context.Background())
	ctx := e.buildToolCtx("tu_1")
	if ctx.OnAskInput == nil {
		t.Fatal("OnAskInput not wired")
	}
	// Fixed date: the deadline only has to round-trip through the event.
	deadline := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ch := ctx.OnAskInput("secret?", true, deadline)
	evt := ev.only(t, types.EventAsk)
	ask := evt.Ask
	if ask == nil {
		t.Fatal("EventAsk carries no AskEvent")
	}
	if ask.Kind != types.AskInput {
		t.Errorf("Kind = %v, want %v", ask.Kind, types.AskInput)
	}
	if ask.Prompt != "secret?" {
		t.Errorf("Prompt = %q, want the prompt argument", ask.Prompt)
	}
	if !ask.Masked {
		t.Error("Masked = false, want true")
	}
	if !ask.Deadline.Equal(deadline) {
		t.Errorf("Deadline = %v, want %v", ask.Deadline, deadline)
	}
	if ask.ResponseCh == nil {
		t.Fatal("ResponseCh missing")
	}
	ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
	if resp := <-ch; resp.Decision != types.DecisionAllow {
		t.Errorf("channel response = %+v, want the answer posted to the event", resp)
	}
}

// TestApplyContextModifierGates pins when a tool's context modifier is
// allowed to replace the base context.
func TestApplyContextModifierGates(t *testing.T) {
	t.Parallel()
	base := tool.ToolUseContext{WorkingDir: "/base"}
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, &base, nil, context.Background())
	modifier := func(c *tool.ToolUseContext) *tool.ToolUseContext {
		c.WorkingDir = "/modified"
		return c
	}

	e.applyContextModifier(&TrackedTool{IsConcurrencySafe: false}, &tool.ToolResult{ContextModifier: modifier})
	if e.tctx.WorkingDir != "/modified" {
		t.Errorf("WorkingDir = %q, want /modified (unsafe tool's modifier applied)", e.tctx.WorkingDir)
	}

	e.tctx = &tool.ToolUseContext{WorkingDir: "/base"}
	e.applyContextModifier(&TrackedTool{IsConcurrencySafe: true}, &tool.ToolResult{ContextModifier: modifier})
	if e.tctx.WorkingDir != "/base" {
		t.Errorf("WorkingDir = %q, want /base (concurrency-safe tool must not modify context)", e.tctx.WorkingDir)
	}

	e.applyContextModifier(&TrackedTool{IsConcurrencySafe: false}, &tool.ToolResult{})
	if e.tctx.WorkingDir != "/base" {
		t.Errorf("WorkingDir = %q, want /base (no modifier)", e.tctx.WorkingDir)
	}

	e.applyContextModifier(&TrackedTool{IsConcurrencySafe: false}, nil)
	if e.tctx.WorkingDir != "/base" {
		t.Errorf("WorkingDir = %q, want /base (nil result)", e.tctx.WorkingDir)
	}
}

// TestMemoryPathWriteBypass pins the memory-directory permission bypass.
func TestMemoryPathWriteBypass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inside := filepath.Join(dir, "notes", "a.md")
	outside := filepath.Join(dir, "..", "elsewhere.md")

	if !isMemoryPathWrite("Write", json.RawMessage(`{"file_path":"`+inside+`"}`), dir+string(filepath.Separator)) {
		t.Error("a Write inside the memory override must bypass the permission check")
	}
	if isMemoryPathWrite("Write", json.RawMessage(`{"file_path":"`+outside+`"}`), dir+string(filepath.Separator)) {
		t.Error("a Write outside the memory override must not bypass the permission check")
	}
	if isMemoryPathWrite("Edit", json.RawMessage(`{"file_path":"`+inside+`"}`), dir+string(filepath.Separator)) {
		t.Error("only Write bypasses; Edit must stay policed")
	}
	if isMemoryPathWrite("Write", json.RawMessage(`{"other":"x"}`), dir+string(filepath.Separator)) {
		t.Error("a Write with no file_path must not bypass")
	}
	if isMemoryPathWrite("Write", json.RawMessage(`{broken`), dir+string(filepath.Separator)) {
		t.Error("a Write with malformed input must not bypass")
	}
	if isMemoryPathWrite("Write", json.RawMessage(`{"file_path":""}`), dir+string(filepath.Separator)) {
		t.Error("an empty file_path must not bypass")
	}
	// No override: the cwd-derived memory path decides.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if isMemoryPathWrite("Write", json.RawMessage(`{"file_path":"`+filepath.Join(cwd, "plain-file.md")+`"}`), "") != long.IsMemoryPath(cwd, filepath.Join(cwd, "plain-file.md")) {
		t.Error("the cwd-derived memory path decision must match long.IsMemoryPath")
	}
}

// TestExtractFilePathFromInput pins the pre-edit path probe.
func TestExtractFilePathFromInput(t *testing.T) {
	t.Parallel()
	if got := extractFilePathFromInput(json.RawMessage(`{"file_path":"/tmp/x"}`)); got != "/tmp/x" {
		t.Errorf("extractFilePathFromInput = %q, want /tmp/x", got)
	}
	if got := extractFilePathFromInput(json.RawMessage(`{broken`)); got != "" {
		t.Errorf("extractFilePathFromInput(malformed) = %q, want empty", got)
	}
	if got := extractFilePathFromInput(json.RawMessage(`{"file_path":123}`)); got != "" {
		t.Errorf("extractFilePathFromInput(wrong type) = %q, want empty", got)
	}
	// The same input decoded twice (second value the wrong type) errors while
	// leaving the first path in place; TrackEdit must not be keyed on it.
	if got := extractFilePathFromInput(json.RawMessage(`{"file_path":"/tmp/x","file_path":[]}`)); got != "" {
		t.Errorf("extractFilePathFromInput(partial decode) = %q, want the partial path dropped", got)
	}
}

// hooksWith wires one command hook per queued result, so a test can drive
// multi-result hook sequences.
func hooksWith(event, matcher string, exec *integrationHookRecorder, results ...hooks.HookResult) *hooks.Hooks {
	exec.results = results
	cfgs := make([]hooks.HookConfig, len(results))
	for i := range results {
		cfgs[i] = hooks.HookConfig{Type: hooks.HookTypeCommand, Command: "ptest"}
	}
	return hooks.NewHooks(hooks.HooksConfig{
		event: []hooks.HookMatcher{{Matcher: matcher, Hooks: cfgs}},
	}, exec)
}

// scriptablePermChecker is a scriptable permission.PermissionChecker.
type scriptablePermChecker struct {
	decision permission.Decision
	panics   bool
	onCheck  func()
}

func (c *scriptablePermChecker) Check(string, json.RawMessage) permission.Decision {
	if c.onCheck != nil {
		c.onCheck()
	}
	if c.panics {
		panic("permission checker exploded")
	}
	return c.decision
}
func (c *scriptablePermChecker) HasRules() bool { return true }

// dynamicWireTool is a tool whose wire blocks are explicit.
type dynamicWireTool struct {
	fakeTool
	wire func(any) []types.ContentBlock
}

func (t *dynamicWireTool) FormatWireBlocks(data any) []types.ContentBlock {
	if t.wire != nil {
		return t.wire(data)
	}
	return nil
}

// TestUnknownToolEventAndTrackedTool pins the AddTool failure path: the
// emitted event and the completed TrackedTool record.
func TestUnknownToolEventAndTrackedTool(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev.fn, context.Background())
	e.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "nope", Input: json.RawMessage(`{"x":1}`)}, started)

	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	if tr == nil {
		t.Fatal("EventToolEnd carries no ToolResult")
	}
	if tr.ToolUseID != "tu_1" {
		t.Errorf("ToolUseID = %q, want tu_1", tr.ToolUseID)
	}
	if !tr.IsError {
		t.Error("unknown tool must emit IsError")
	}
	wantMsg := "No such tool available: nope"
	if tr.DisplayOutput != wantMsg {
		t.Errorf("DisplayOutput = %q, want %q", tr.DisplayOutput, wantMsg)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != wantMsg {
		t.Errorf("Output payload = %q, want %q", got, wantMsg)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.tools) != 1 {
		t.Fatalf("tracked tools = %d, want 1", len(e.tools))
	}
	tt := e.tools[0]
	if tt.ID != "tu_1" {
		t.Errorf("tracked ID = %q, want tu_1", tt.ID)
	}
	if tt.Name != "nope" {
		t.Errorf("tracked Name = %q, want nope", tt.Name)
	}
	if string(tt.Input) != `{"x":1}` {
		t.Errorf("tracked Input = %s, want the tool input", string(tt.Input))
	}
	if tt.Status != StatusCompleted {
		t.Errorf("tracked Status = %v, want completed", tt.Status)
	}
	if !tt.IsConcurrencySafe {
		t.Error("a failed unknown tool must be recorded as concurrency-safe")
	}
	if !tt.StartedAt.Equal(started) {
		t.Errorf("tracked StartedAt = %v, want %v", tt.StartedAt, started)
	}
	if len(tt.resultBlocks) != 1 {
		t.Errorf("tracked resultBlocks = %d, want 1", len(tt.resultBlocks))
	}
	select {
	case <-tt.done:
	default:
		t.Error("unknown tool's done channel was never closed")
	}
}

// TestDeferredToolHint pins when the ToolSearch discovery hint replaces
// the generic unknown-tool message.
func TestDeferredToolHint(t *testing.T) {
	t.Parallel()
	deferred := &fakeTool{name: "Secret", deferred: true}
	plain := &fakeTool{name: "Plain"}
	tools := map[string]tool.Tool{ToolSearchToolName: &fakeTool{name: ToolSearchToolName}}
	tctx := &tool.ToolUseContext{Options: tool.ToolUseOptions{Tools: map[string]tool.Tool{"Secret": deferred, "Plain": plain}}}

	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(tools, tctx, ev.fn, context.Background())
	e.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_d", Name: "Secret", Input: json.RawMessage(`{}`)}, time.Time{})
	got := ev.only(t, types.EventToolEnd).ToolResult.DisplayOutput
	if !strings.Contains(got, "Tool Secret is deferred") || !strings.Contains(got, "ToolSearch") {
		t.Errorf("deferred tool message = %q, want the ToolSearch discovery hint", got)
	}

	// A tool that exists in the full map but is not deferred keeps the
	// generic message.
	ev2 := &emitRecorder{}
	e2 := NewStreamingToolExecutor(tools, tctx, ev2.fn, context.Background())
	e2.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_p", Name: "Plain", Input: json.RawMessage(`{}`)}, time.Time{})
	got2 := ev2.only(t, types.EventToolEnd).ToolResult.DisplayOutput
	if got2 != "No such tool available: Plain" {
		t.Errorf("non-deferred tool message = %q, want the generic message", got2)
	}

	// A tool absent from the full map keeps the generic message too.
	ev3 := &emitRecorder{}
	e3 := NewStreamingToolExecutor(tools, tctx, ev3.fn, context.Background())
	e3.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_x", Name: "Ghost", Input: json.RawMessage(`{}`)}, time.Time{})
	if got3 := ev3.only(t, types.EventToolEnd).ToolResult.DisplayOutput; got3 != "No such tool available: Ghost" {
		t.Errorf("unknown tool message = %q, want the generic message", got3)
	}
}

// TestConcurrencyProbePanicIsUnsafe pins the TS-aligned catch behaviour of
// a panicking IsConcurrencySafe.
func TestConcurrencyProbePanicIsUnsafe(t *testing.T) {
	t.Parallel()
	boom := &fakeTool{name: "boom", panicSafe: true}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"boom": boom}, nil, (&emitRecorder{}).fn, context.Background())
	e.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "boom", Input: json.RawMessage(`{}`)}, time.Time{})
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.tools) != 1 {
		t.Fatalf("tracked tools = %d, want 1", len(e.tools))
	}
	if e.tools[0].IsConcurrencySafe {
		t.Error("a panicking IsConcurrencySafe probe must be treated as NOT concurrency-safe")
	}
}

// TestPanicInCallEmitsError pins the panic recovery: an error event, an
// error result block and the diagnostic.
func TestPanicInCallEmitsError(t *testing.T) {
	t.Parallel()
	rec := captureSlog(t)
	boom := &fakeTool{name: "boom", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		panic("tool exploded")
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"boom": boom}, nil, ev.fn, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "boom", Input: json.RawMessage(`{}`)}})
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("result blocks = %d, want 1", len(res.ToolResultBlocks))
	}
	wantMsg := "internal error in tool boom: tool exploded"
	block := res.ToolResultBlocks[0]
	if !block.IsError {
		t.Error("panic result must be an error block")
	}
	if got := toolResultText(t, block); got != wantMsg {
		t.Errorf("panic result = %q, want %q", got, wantMsg)
	}
	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	if tr.ToolUseID != "tu_1" {
		t.Errorf("panic event ToolUseID = %q, want tu_1", tr.ToolUseID)
	}
	if !tr.IsError {
		t.Error("panic event must carry IsError")
	}
	if tr.DisplayOutput != wantMsg {
		t.Errorf("panic event DisplayOutput = %q, want %q", tr.DisplayOutput, wantMsg)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != wantMsg {
		t.Errorf("panic event Output = %q, want %q", got, wantMsg)
	}
	log, ok := rec.find("engine: panic in executeTool", func(a map[string]any) bool {
		return a["tool"] == "boom" && a["error"] == "tool exploded"
	})
	if !ok {
		t.Fatal("panic diagnostic missing from the log")
	}
	if log.attrs["tool"] != "boom" {
		t.Errorf("panic diagnostic tool attr = %v, want boom", log.attrs["tool"])
	}
	if s, _ := log.attrs["stack"].(string); !strings.Contains(s, "goroutine") {
		t.Errorf("panic diagnostic stack attr = %q, want a stack trace", s)
	}
}

// TestMissingToolAtExecute pins the defensive lookup failure inside
// executeTool, including the merged-tool hint.
func TestMissingToolAtExecute(t *testing.T) {
	t.Parallel()
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev.fn, context.Background())
	tt := &TrackedTool{ID: "tu_1", Name: "ghost", Input: json.RawMessage(`{}`), done: make(chan struct{})}
	e.executeTool(tt)

	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	want := "No such tool available: ghost"
	if tr.ToolUseID != "tu_1" || !tr.IsError || tr.DisplayOutput != want {
		t.Errorf("missing-tool event = %+v, want an error event with %q", tr, want)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
		t.Errorf("missing-tool Output = %q, want %q", got, want)
	}
	if len(tt.resultBlocks) != 1 || !tt.resultBlocks[0].IsError {
		t.Errorf("missing-tool result blocks = %+v, want one error block", tt.resultBlocks)
	}

	// A merged tool name keeps its hint.
	ev2 := &emitRecorder{}
	e2 := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev2.fn, context.Background())
	tt2 := &TrackedTool{ID: "tu_2", Name: "Glob", Input: json.RawMessage(`{}`), done: make(chan struct{})}
	e2.executeTool(tt2)
	got := ev2.only(t, types.EventToolEnd).ToolResult.DisplayOutput
	if got != toolNotFoundHint("Glob") {
		t.Errorf("merged-tool message = %q, want the Glob→Grep hint", got)
	}
}

// TestDurationCoversTheCall pins the zero-start fallback: the duration is
// the call's wall time, not the distance from the epoch.
func TestDurationCoversTheCall(t *testing.T) {
	t.Parallel()
	slow := &fakeTool{name: "slow", safe: true, callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		time.Sleep(20 * time.Millisecond) // REAL-TIME: Duration measures the wall-clock call span
		return &tool.ToolResult{Data: "done"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"slow": slow}, nil, ev.fn, context.Background())
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "slow", Input: json.RawMessage(`{}`)}})

	evt := ev.only(t, types.EventToolEnd)
	d := evt.ToolResult.Duration
	if d <= 0 || d > time.Second {
		t.Errorf("event Duration = %v, want a few tens of milliseconds", d)
	}
	e.mu.Lock()
	tt := e.tools[0]
	e.mu.Unlock()
	if tt.Duration <= 0 || tt.Duration > time.Second {
		t.Errorf("tracked Duration = %v, want a few tens of milliseconds", tt.Duration)
	}
}

// TestPermCheckerPanicDenies pins fail-secure: a broken checker denies
// and stays installed.
func TestPermCheckerPanicDenies(t *testing.T) {
	t.Parallel()
	rec := captureSlog(t)
	ran := false
	tt := &fakeTool{name: "guarded", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"guarded": tt}, nil, (&emitRecorder{}).fn, context.Background())
	e.SetPermissionChecker(&scriptablePermChecker{panics: true})
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "guarded", Input: json.RawMessage(`{}`)}})
	if ran {
		t.Error("a panicking permission checker let the tool run")
	}
	if len(res.ToolResultBlocks) != 1 || !res.ToolResultBlocks[0].IsError {
		t.Fatalf("result = %+v, want one error block", res.ToolResultBlocks)
	}
	if got := toolResultText(t, res.ToolResultBlocks[0]); got != ruleDenyMessage("guarded") {
		t.Errorf("deny message = %q, want the rule-deny message", got)
	}
	if _, ok := rec.find("engine: permChecker.Check panicked, denying", func(a map[string]any) bool {
		return a["tool"] == "guarded" && a["error"] == "permission checker exploded"
	}); !ok {
		t.Error("the panicking checker left no diagnostic")
	}
}

// TestRuleDenyEventShape pins the bare-tool deny event and result block.
func TestRuleDenyEventShape(t *testing.T) {
	t.Parallel()
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"guarded": &fakeTool{name: "guarded", safe: true}}, nil, ev.fn, context.Background())
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionDeny, Message: "no"}})
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "guarded", Input: json.RawMessage(`{}`)}})

	want := ruleDenyMessage("guarded")
	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	if tr.ToolUseID != "tu_1" || !tr.IsError || tr.DisplayOutput != want {
		t.Errorf("deny event = %+v, want an error event with the rule-deny message", tr)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
		t.Errorf("deny Output = %q, want the rule-deny message", got)
	}
	if len(res.ToolResultBlocks) != 1 || toolResultText(t, res.ToolResultBlocks[0]) != want {
		t.Errorf("deny result = %+v, want the rule-deny message", res.ToolResultBlocks)
	}
}

// TestPermissionRequestHookAsk pins the PermissionRequest hook phase for
// non-decisive results: the dispatch payload, and that results without an
// allow/deny/block decision fall through to the dialog. The decisive
// decisions are pinned by the allow/deny/block/first-wins tests below
// (TS PermissionContext.ts:233-259 — runHooks returns on the first decisive
// result; only a null return opens the dialog).
func TestPermissionRequestHookAsk(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc    string
		outcome hooks.HookOutcome
		output  *hooks.HookOutput
	}{
		{"no output", hooks.HookOutcomeSuccess, nil},
		{"no decision", hooks.HookOutcomeSuccess, &hooks.HookOutput{Reason: "noise"}},
		{"unknown decision", hooks.HookOutcomeSuccess, &hooks.HookOutput{Decision: "ask"}},
	}
	for _, tc := range cases {
		ran := false
		toolx := &fakeTool{name: "ptest_perm", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			ran = true
			return &tool.ToolResult{Data: "ran"}, nil
		}}
		ev := &emitRecorder{}
		asks, emit := ev.askTap()
		e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_perm": toolx}, nil, emit, context.Background())
		hexec := &integrationHookRecorder{}
		e.SetHooks(hooksWith("PermissionRequest", "ptest_perm", hexec,
			hooks.HookResult{Outcome: tc.outcome, Output: tc.output},
		), "sess-1")
		e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk}})
		go func() {
			ask := <-asks
			ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
		}()
		e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_perm", Input: json.RawMessage(`{}`)}})

		in := hexec.lastInput(t)
		if in.HookEventName != "PermissionRequest" {
			t.Errorf("%s: hook event = %q, want PermissionRequest", tc.desc, in.HookEventName)
		}
		if in.ToolName != "ptest_perm" {
			t.Errorf("%s: hook ToolName = %q, want ptest_perm", tc.desc, in.ToolName)
		}
		if in.Cwd != "" {
			t.Errorf("%s: hook Cwd = %q, want the unset engine default", tc.desc, in.Cwd)
		}
		var ask *types.AskEvent
		for _, evt := range ev.all() {
			if evt.Ask != nil {
				ask = evt.Ask
			}
		}
		if ask == nil {
			t.Fatalf("%s: the permission dialog never opened", tc.desc)
		}
		if ask.ToolName != "ptest_perm" {
			t.Errorf("%s: ask ToolName = %q, want ptest_perm", tc.desc, ask.ToolName)
		}
		if ran {
			t.Errorf("%s: a rejected ask let the tool run", tc.desc)
		}
	}
}

// TestPermissionRequestHookBlock pins that the generic "block" decision
// (gbot's shared decision vocabulary, not a TS PermissionRequest behavior)
// denies the tool with the rule-deny message.
func TestPermissionRequestHookBlock(t *testing.T) {
	t.Parallel()
	hexec := &integrationHookRecorder{}
	ran := false
	toolx := &fakeTool{name: "ptest_perm", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_perm": toolx}, nil, ev.fn, context.Background())
	e.SetHooks(hooksWith("PermissionRequest", "ptest_perm", hexec,
		hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "block"}},
	), "sess-hook")
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk, Message: "ask"}})

	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_perm", Input: json.RawMessage(`{}`)}})
	if ran {
		t.Error("a blocked tool ran")
	}
	if n := askEventCount(ev.all()); n != 0 {
		t.Errorf("hook-block opened %d dialogs, want 0", n)
	}
	want := ruleDenyMessage("ptest_perm")
	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	if tr.ToolUseID != "tu_1" || !tr.IsError || tr.DisplayOutput != want {
		t.Errorf("hook-block event = %+v, want an error event with the rule-deny message", tr)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
		t.Errorf("hook-block Output = %q, want the rule-deny message", got)
	}
	if len(res.ToolResultBlocks) != 1 || toolResultText(t, res.ToolResultBlocks[0]) != want {
		t.Errorf("hook-block result = %+v, want the rule-deny message", res.ToolResultBlocks)
	}
}

// askEventCount counts emitted Ask events.
func askEventCount(evs []types.QueryEvent) int {
	n := 0
	for _, evt := range evs {
		if evt.Type == types.EventAsk {
			n++
		}
	}
	return n
}

// executeAllOrAsk runs ExecuteAll in the background and races it against the
// ask channel: if the permission dialog opens, the ask is denied, the run is
// drained, and the caller's askBranch callback reports it — so a regression
// that reopens the dialog fails by assertion instead of hanging on ResponseCh.
func executeAllOrAsk(t *testing.T, e *StreamingToolExecutor, blocks []types.ContentBlock, asks chan *types.AskEvent, askBranch func()) *ExecuteAllResult {
	t.Helper()
	done := make(chan *ExecuteAllResult, 1)
	go func() { done <- e.ExecuteAll(blocks) }()
	select {
	case ask := <-asks:
		ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
		res := <-done
		askBranch()
		return res
	case res := <-done:
		return res
	}
}

// TestPermissionRequestHookAllowSkipsAsk pins TS PermissionContext.ts:233-239:
// a hook "allow" decision resolves the permission request without the dialog,
// and the tool runs.
func TestPermissionRequestHookAllowSkipsAsk(t *testing.T) {
	t.Parallel()
	ran := false
	toolx := &fakeTool{name: "ptest_perm", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev := &emitRecorder{}
	asks, emit := ev.askTap()
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_perm": toolx}, nil, emit, context.Background())
	hexec := &integrationHookRecorder{}
	e.SetHooks(hooksWith("PermissionRequest", "ptest_perm", hexec,
		hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "allow"}},
	), "sess-allow")
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk}})

	res := executeAllOrAsk(t, e,
		[]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_perm", Input: json.RawMessage(`{}`)}},
		asks, func() { t.Fatal("the permission dialog opened despite the hook's allow decision") })
	if !ran {
		t.Fatal("the tool never ran after the hook's allow decision")
	}
	if n := askEventCount(ev.all()); n != 0 {
		t.Errorf("hook-allow opened %d dialogs, want 0", n)
	}
	if len(res.ToolResultBlocks) != 1 || res.ToolResultBlocks[0].IsError {
		t.Errorf("result after hook allow = %+v, want one non-error block", res.ToolResultBlocks)
	}
}

// TestPermissionRequestHookDeny pins TS PermissionContext.ts:240-258: a hook
// "deny" decision rejects the tool without the dialog, with the hook's reason
// or the TS fallback message.
func TestPermissionRequestHookDeny(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc   string
		reason string
		want   string
	}{
		{"hook reason", "blocked by policy", "blocked by policy"},
		{"fallback", "", "Permission denied by hook"},
	}
	for _, tc := range cases {
		ran := false
		toolx := &fakeTool{name: "ptest_perm", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			ran = true
			return &tool.ToolResult{Data: "ran"}, nil
		}}
		ev := &emitRecorder{}
		asks, emit := ev.askTap()
		e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_perm": toolx}, nil, emit, context.Background())
		hexec := &integrationHookRecorder{}
		e.SetHooks(hooksWith("PermissionRequest", "ptest_perm", hexec,
			hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "deny", Reason: tc.reason}},
		), "sess-deny")
		e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk}})

		res := executeAllOrAsk(t, e,
			[]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_perm", Input: json.RawMessage(`{}`)}},
			asks, func() { t.Fatalf("%s: the permission dialog opened despite the hook's deny decision", tc.desc) })
		if ran {
			t.Errorf("%s: a denied tool ran", tc.desc)
		}
		if n := askEventCount(ev.all()); n != 0 {
			t.Errorf("%s: hook-deny opened %d dialogs, want 0", tc.desc, n)
		}
		tr := ev.only(t, types.EventToolEnd).ToolResult
		if tr.ToolUseID != "tu_1" || !tr.IsError || tr.DisplayOutput != tc.want {
			t.Errorf("%s: hook-deny event = %+v, want an error event with %q", tc.desc, tr, tc.want)
		}
		if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != tc.want {
			t.Errorf("%s: hook-deny Output = %q, want %q", tc.desc, got, tc.want)
		}
		if len(res.ToolResultBlocks) != 1 || toolResultText(t, res.ToolResultBlocks[0]) != tc.want {
			t.Errorf("%s: hook-deny result = %+v, want %q", tc.desc, res.ToolResultBlocks, tc.want)
		}
	}
}

// TestPermissionRequestHookFirstDecisiveWins pins the TS runHooks loop shape:
// the first decisive result ends the loop, so an allow before a deny lets the
// tool run, and a deny before an allow rejects it.
func TestPermissionRequestHookFirstDecisiveWins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc    string
		results []hooks.HookResult
		wantRan bool
		wantMsg string
	}{
		{"allow before deny", []hooks.HookResult{
			{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "allow"}},
			{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "deny"}},
		}, true, ""},
		{"deny before allow", []hooks.HookResult{
			{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "deny", Reason: "first wins"}},
			{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "allow"}},
		}, false, "first wins"},
		// A result without output is skipped, not terminal: the loop must keep
		// draining later hooks, whose decision still resolves the request.
		{"no output then allow", []hooks.HookResult{
			{Outcome: hooks.HookOutcomeSuccess},
			{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "allow"}},
		}, true, ""},
		{"no output then deny", []hooks.HookResult{
			{Outcome: hooks.HookOutcomeSuccess},
			{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "deny", Reason: "late deny"}},
		}, false, "late deny"},
	}
	for _, tc := range cases {
		ran := false
		toolx := &fakeTool{name: "ptest_perm", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			ran = true
			return &tool.ToolResult{Data: "ran"}, nil
		}}
		ev := &emitRecorder{}
		asks, emit := ev.askTap()
		e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_perm": toolx}, nil, emit, context.Background())
		hexec := &integrationHookRecorder{}
		e.SetHooks(hooksWith("PermissionRequest", "ptest_perm", hexec, tc.results...), "sess-order")
		e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk}})

		res := executeAllOrAsk(t, e,
			[]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_perm", Input: json.RawMessage(`{}`)}},
			asks, func() { t.Fatalf("%s: the permission dialog opened", tc.desc) })
		if ran != tc.wantRan {
			t.Errorf("%s: tool ran = %v, want %v", tc.desc, ran, tc.wantRan)
		}
		if n := askEventCount(ev.all()); n != 0 {
			t.Errorf("%s: opened %d dialogs, want 0", tc.desc, n)
		}
		tr := ev.only(t, types.EventToolEnd).ToolResult
		if tc.wantRan {
			if tr.IsError {
				t.Errorf("%s: result = %+v, want the tool's non-error result", tc.desc, tr)
			}
			continue
		}
		if !tr.IsError || tr.DisplayOutput != tc.wantMsg {
			t.Errorf("%s: result = %+v, want an error event with %q", tc.desc, tr, tc.wantMsg)
		}
		if len(res.ToolResultBlocks) != 1 || toolResultText(t, res.ToolResultBlocks[0]) != tc.wantMsg {
			t.Errorf("%s: result blocks = %+v, want %q", tc.desc, res.ToolResultBlocks, tc.wantMsg)
		}
	}
}

// TestAskDecisionHandlesEveryAnswer pins the reject gate after a dialog.
func TestAskDecisionHandlesEveryAnswer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		answer  types.UserDecision
		wantRan bool
		wantMsg string
	}{
		{answer: types.DecisionDeny, wantRan: false, wantMsg: userRejectMessage},
		{answer: types.DecisionAllow, wantRan: true},
		{answer: types.DecisionAllowAlways, wantRan: true},
	}
	for _, tc := range cases {
		answer := tc.answer
		ev := &emitRecorder{}
		ran := false
		toolx := &fakeTool{name: "ptest_ask", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			ran = true
			return &tool.ToolResult{Data: "ran"}, nil
		}}
		asks, emit := ev.askTap()
		e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_ask": toolx}, nil, emit, context.Background())
		e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk, Message: "please"}})
		go func() {
			ask := <-asks
			ask.ResponseCh <- types.AskResponse{Decision: answer}
		}()
		res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_ask", Input: json.RawMessage(`{}`)}})
		if ran != tc.wantRan {
			t.Errorf("answer %v: tool ran = %v, want %v", answer, ran, tc.wantRan)
		}
		if len(res.ToolResultBlocks) != 1 {
			t.Fatalf("answer %v: result blocks = %d, want 1", answer, len(res.ToolResultBlocks))
		}
		if tc.wantMsg == "" {
			if res.ToolResultBlocks[0].IsError {
				t.Errorf("answer %v: result must succeed, got %s", answer, toolResultText(t, res.ToolResultBlocks[0]))
			}
			continue
		}
		if got := toolResultText(t, res.ToolResultBlocks[0]); got != tc.wantMsg {
			t.Errorf("answer %v: reject message = %q, want %q", answer, got, tc.wantMsg)
		}
		evt := ev.only(t, types.EventToolEnd)
		if evt.ToolResult.ToolUseID != "tu_1" || !evt.ToolResult.IsError || evt.ToolResult.DisplayOutput != tc.wantMsg {
			t.Errorf("answer %v: reject event = %+v, want an error event with the reject message", answer, evt.ToolResult)
		}
		if got := toolResultText(t, types.ContentBlock{Content: evt.ToolResult.Output}); got != tc.wantMsg {
			t.Errorf("answer %v: reject Output = %q, want the reject message", answer, got)
		}
	}
}

// TestSubEngineRejectMessage pins the sub-engine variant of the reject text.
func TestSubEngineRejectMessage(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	if got := e.userOrSubRejectMessage(); got != userRejectMessage {
		t.Errorf("main-engine reject = %q, want the user reject message", got)
	}
	e.SetSubEngine(true)
	if got := e.userOrSubRejectMessage(); got != subAgentRejectMessage {
		t.Errorf("sub-engine reject = %q, want the sub-agent reject message", got)
	}
}

// TestContentRuleDeny pins the content-level deny phase, including the
// gate that only runs it when the decision carries content rules.
func TestContentRuleDeny(t *testing.T) {
	t.Parallel()
	permission.RegisterContentChecker("ptest_content", func(_ json.RawMessage, rules []permission.Rule) permission.RuleAction {
		if len(rules) == 0 {
			return permission.ActionAllow
		}
		return permission.ActionDeny
	})
	ran := false
	toolx := &fakeTool{name: "ptest_content", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_content": toolx}, nil, ev.fn, context.Background())
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{
		Action:       permission.ActionAllow,
		ContentRules: []permission.Rule{{Action: permission.ActionDeny, Value: permission.RuleValue{ToolName: "ptest_content"}}},
	}})
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_content", Input: json.RawMessage(`{}`)}})
	if ran {
		t.Error("a content-rule deny let the tool run")
	}
	want := ruleDenyMessage("ptest_content")
	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	if tr.ToolUseID != "tu_1" || !tr.IsError || tr.DisplayOutput != want {
		t.Errorf("content-deny event = %+v, want an error event with the rule-deny message", tr)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
		t.Errorf("content-deny Output = %q, want the rule-deny message", got)
	}
	if len(res.ToolResultBlocks) != 1 || !res.ToolResultBlocks[0].IsError {
		t.Errorf("content-deny result = %+v, want one error block", res.ToolResultBlocks)
	}

	// No content rules → the content phase must not run at all.
	ran2 := false
	tool2 := &fakeTool{name: "ptest_content", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran2 = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev2 := &emitRecorder{}
	e2 := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_content": tool2}, nil, ev2.fn, context.Background())
	e2.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAllow}})
	res2 := e2.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "ptest_content", Input: json.RawMessage(`{}`)}})
	if !ran2 {
		t.Errorf("an allow decision without content rules was blocked: %s", toolResultText(t, res2.ToolResultBlocks[0]))
	}
}

// TestContentRuleAsk pins the content-level ask phase: the dialog carries
// the generated message and the matched pattern, and a denial rejects.
func TestContentRuleAsk(t *testing.T) {
	t.Parallel()
	permission.RegisterContentChecker("ptest_cask", func(_ json.RawMessage, rules []permission.Rule) permission.RuleAction {
		if len(rules) == 0 {
			return permission.ActionAllow
		}
		return permission.ActionAsk
	})
	permission.RegisterContentChecker("ptest_callow", func(_ json.RawMessage, rules []permission.Rule) permission.RuleAction {
		if len(rules) == 0 {
			return permission.ActionAllow
		}
		return permission.ActionAsk
	})

	ev := &emitRecorder{}
	ran := false
	toolx := &fakeTool{name: "ptest_cask", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	asks, emit := ev.askTap()
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_cask": toolx}, nil, emit, context.Background())
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{
		Action: permission.ActionAllow,
		ContentRules: []permission.Rule{{
			Action: permission.ActionAsk,
			Value:  permission.RuleValue{ToolName: "ptest_cask", RuleContent: new("danger")},
		}},
	}})
	go func() {
		ask := <-asks
		ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
	}()
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_cask", Input: json.RawMessage(`{}`)}})
	if ran {
		t.Error("a denied content-rule ask let the tool run")
	}
	var ask *types.AskEvent
	for _, evt := range ev.all() {
		if evt.Ask != nil {
			ask = evt.Ask
		}
	}
	if ask == nil {
		t.Fatal("content-rule ask opened no dialog")
	}
	if ask.Message != "tool ptest_cask requires permission by content rule" {
		t.Errorf("ask Message = %q, want the generated content-rule message", ask.Message)
	}
	if ask.Kind != types.AskPermission {
		t.Errorf("ask Kind = %v, want %v", ask.Kind, types.AskPermission)
	}
	if len(res.ToolResultBlocks) != 1 || toolResultText(t, res.ToolResultBlocks[0]) != userRejectMessage {
		t.Errorf("content-ask reject result = %+v, want the reject message", res.ToolResultBlocks)
	}
}

//go:fix inline

// TestPreToolUseHookInputAndBlock pins the hook payload and the blocking
// decision's event.
func TestPreToolUseHookInputAndBlock(t *testing.T) {
	t.Parallel()
	hexec := &integrationHookRecorder{}
	ran := false
	toolx := &fakeTool{name: "ptest_hook", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_hook": toolx}, nil, ev.fn, context.Background())
	e.SetHooks(hooksWith("PreToolUse", "ptest_hook", hexec,
		hooks.HookResult{Outcome: hooks.HookOutcomeBlocking, Stderr: "nope"},
	), "sess-pre")
	e.SetWorkingDirSupplier(func() string { return "/work/dir" })
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_hook", Input: json.RawMessage(`{"a":1}`)}})
	if ran {
		t.Error("a blocking PreToolUse hook did not stop the tool")
	}
	in := hexec.lastInput(t)
	if in.SessionID != "sess-pre" {
		t.Errorf("hook SessionID = %q, want sess-pre", in.SessionID)
	}
	if in.ToolUseID != "tu_1" {
		t.Errorf("hook ToolUseID = %q, want tu_1", in.ToolUseID)
	}
	if string(in.ToolInput) != `{"a":1}` {
		t.Errorf("hook ToolInput = %s, want the tool input", string(in.ToolInput))
	}
	if in.Cwd != "/work/dir" {
		t.Errorf("hook Cwd = %q, want the live working dir", in.Cwd)
	}
	want := "Execution stopped by PreToolUse hook for tool ptest_hook"
	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	if tr.ToolUseID != "tu_1" || !tr.IsError || tr.DisplayOutput != want {
		t.Errorf("hook-block event = %+v, want an error event with %q", tr, want)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
		t.Errorf("hook-block Output = %q, want %q", got, want)
	}
}

// TestDiscardAfterAbortCheck pins the second discard probe inside
// executeTool: a tool discarded after the first check must not reach Call.
func TestDiscardAfterAbortCheck(t *testing.T) {
	t.Parallel()
	ran := false
	toolx := &fakeTool{name: "ptest_disc", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_disc": toolx}, nil, ev.fn, context.Background())
	e.SetPermissionChecker(&scriptablePermChecker{
		decision: permission.Decision{Action: permission.ActionAllow},
		onCheck:  func() { e.mu.Lock(); e.discarded = true; e.mu.Unlock() },
	})
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_disc", Input: json.RawMessage(`{}`)}})
	if ran {
		t.Error("a tool discarded mid-check reached Call")
	}
	// Emissions are suppressed once the executor is discarded, so the pin is
	// on the recorded result: the synthetic fallback block must be there.
	want := "Error: Streaming fallback - tool execution discarded"
	e.mu.Lock()
	blocks := e.tools[0].resultBlocks
	e.mu.Unlock()
	if len(blocks) != 1 || !blocks[0].IsError {
		t.Fatalf("discard result blocks = %+v, want one error block", blocks)
	}
	if got := toolResultText(t, blocks[0]); got != want {
		t.Errorf("discard result = %q, want %q", got, want)
	}
	if n := len(ev.all()); n != 0 {
		t.Errorf("discarded executor emitted %d events, want 0", n)
	}
}

// TestProgressEmitsDeltaEvents pins the streaming progress gate and the
// delta payload.
func TestProgressEmitsDeltaEvents(t *testing.T) {
	t.Parallel()
	toolx := &fakeTool{name: "ptest_stream", safe: true, callFn: func(_ context.Context, _ json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
		tctx.OnProgress(tool.ProgressUpdate{Lines: nil})
		tctx.OnProgress(tool.ProgressUpdate{Lines: []string{}})
		tctx.OnProgress(tool.ProgressUpdate{Lines: []string{"one"}})
		tctx.OnProgress(tool.ProgressUpdate{Lines: []string{"two", "three"}})
		return &tool.ToolResult{Data: "done"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_stream": toolx}, nil, ev.fn, context.Background())
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_stream", Input: json.RawMessage(`{}`)}})

	deltas := []types.QueryEvent{}
	for _, evt := range ev.all() {
		if evt.Type == types.EventToolOutputDelta {
			deltas = append(deltas, evt)
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("progress deltas = %d, want 2 (empty progress must not emit)", len(deltas))
	}
	if deltas[0].ToolResult.DisplayOutput != "one" || deltas[1].ToolResult.DisplayOutput != "two\nthree" {
		t.Errorf("delta payloads = %q, %q, want %q and %q",
			deltas[0].ToolResult.DisplayOutput, deltas[1].ToolResult.DisplayOutput, "one", "two\nthree")
	}
	for _, d := range deltas {
		if d.ToolResult.ToolUseID != "tu_1" {
			t.Errorf("delta ToolUseID = %q, want tu_1", d.ToolResult.ToolUseID)
		}
		if d.ToolResult.IsError {
			t.Error("a progress delta must not be marked as an error")
		}
	}
}

// TestDisplayOutputFallsBackToProgress pins the last-progress fallback when
// the tool renders nothing.
func TestDisplayOutputFallsBackToProgress(t *testing.T) {
	t.Parallel()
	toolx := &fakeTool{name: "ptest_fb", safe: true, callFn: func(_ context.Context, _ json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
		tctx.OnProgress(tool.ProgressUpdate{Lines: []string{"first chunk"}})
		tctx.OnProgress(tool.ProgressUpdate{Lines: []string{"last chunk"}})
		return &tool.ToolResult{Data: "done"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_fb": toolx}, nil, ev.fn, context.Background())
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_fb", Input: json.RawMessage(`{}`)}})
	evt := ev.only(t, types.EventToolEnd)
	if got := evt.ToolResult.DisplayOutput; got != "last chunk" {
		t.Errorf("DisplayOutput = %q, want the last progress line (the tool renders nothing)", got)
	}
}

// TestEmptyWireResultGetsMarker pins that the post-processing of the wire
// content (empty-result marker) reaches both the event and the result block.
func TestEmptyWireResultGetsMarker(t *testing.T) {
	t.Parallel()
	toolx := &dynamicWireTool{name: "ptest_empty", safe: true}
	toolx.wire = func(any) []types.ContentBlock { return nil }
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_empty": toolx}, nil, ev.fn, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_empty", Input: json.RawMessage(`{}`)}})
	want := "(ptest_empty completed with no output)"
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("result blocks = %d, want 1", len(res.ToolResultBlocks))
	}
	if got := toolResultText(t, res.ToolResultBlocks[0]); got != want {
		t.Errorf("empty-result block = %q, want %q", got, want)
	}
	evt := ev.only(t, types.EventToolEnd)
	if got := toolResultText(t, types.ContentBlock{Content: evt.ToolResult.Output}); got != want {
		t.Errorf("empty-result event Output = %q, want %q", got, want)
	}
}

// TestSuccessEventShape pins every field the success ToolEnd carries.
func TestSuccessEventShape(t *testing.T) {
	t.Parallel()
	toolx := &fakeTool{
		name: "ptest_ok", safe: true,
		renderFn:   func(any) string { return "rendered result" },
		searchRead: &tool.SearchReadKind{IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true},
		callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "payload", NewMessages: []types.Message{calibTextMessage("injected")}}, nil
		},
	}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_ok": toolx}, nil, ev.fn, context.Background())
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_ok", Input: json.RawMessage(`{}`)}})

	evt := ev.only(t, types.EventToolEnd)
	tr := evt.ToolResult
	if tr.ToolUseID != "tu_1" {
		t.Errorf("ToolUseID = %q, want tu_1", tr.ToolUseID)
	}
	if tr.DisplayOutput != "rendered result" {
		t.Errorf("DisplayOutput = %q, want the tool's rendering", tr.DisplayOutput)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != `"payload"` {
		t.Errorf("Output = %q, want the JSON-encoded data", got)
	}
	if tr.IsError || tr.IsBackground {
		t.Errorf("flags = error %v background %v, want both false", tr.IsError, tr.IsBackground)
	}
	if !tr.IsSearch || !tr.IsRead || !tr.IsList || !tr.IsLsp || !tr.IsTask {
		t.Errorf("kind flags = %+v, want search/read/list/lsp/task all true", tr)
	}
	if tr.Duration <= 0 || tr.Duration > time.Second {
		t.Errorf("Duration = %v, want the call's wall time", tr.Duration)
	}
	if len(res.NewMessages) != 1 {
		t.Errorf("NewMessages = %d, want the injected message", len(res.NewMessages))
	}
	if len(res.ToolResultBlocks) != 1 || res.ToolResultBlocks[0].ToolDurationNs <= 0 {
		t.Errorf("result block = %+v, want a positive ToolDurationNs", res.ToolResultBlocks)
	}
}

// TestBackgroundResultFlag pins the async-launch flag on the end event.
func TestBackgroundResultFlag(t *testing.T) {
	t.Parallel()
	toolx := &fakeTool{name: "ptest_bg", safe: true, renderFn: func(any) string { return "launched" }, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: &types.SubQueryResult{AsyncLaunched: true}}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_bg": toolx}, nil, ev.fn, context.Background())
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_bg", Input: json.RawMessage(`{}`)}})
	tr := ev.only(t, types.EventToolEnd).ToolResult
	if !tr.IsBackground {
		t.Errorf("IsBackground = %v, want true for an async-launched sub-query", tr.IsBackground)
	}
}

// TestPostExecAbort pins the abort check that runs after a successful
// Call (a tool killed mid-flight reports the unified abort message).
func TestPostExecAbort(t *testing.T) {
	t.Parallel()
	t.Run("user interrupt", func(t *testing.T) {
		root, cancel := context.WithCancel(context.Background())
		toolx := &fakeTool{name: "ptest_abort", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			cancel()
			return &tool.ToolResult{Data: "killed mid-flight"}, nil
		}}
		ev := &emitRecorder{}
		e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_abort": toolx}, nil, ev.fn, root)
		res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_abort", Input: json.RawMessage(`{}`)}})
		if len(res.ToolResultBlocks) != 1 || !res.ToolResultBlocks[0].IsError {
			t.Fatalf("result = %+v, want an error block", res.ToolResultBlocks)
		}
		if got := toolResultText(t, res.ToolResultBlocks[0]); got != userRejectMessage {
			t.Errorf("post-cancel result = %q, want the user-reject message", got)
		}
		tr := ev.only(t, types.EventToolEnd).ToolResult
		if !tr.IsError || tr.DisplayOutput != userRejectMessage {
			t.Errorf("post-cancel event = %+v, want an error event with the user-reject message", tr)
		}
	})

	t.Run("sibling error", func(t *testing.T) {
		var nestedErrored bool
		toolx := &fakeTool{name: "ptest_sib", safe: true, callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			// A fresh executor must not inherit the sibling chain's error
			// state: cancellation travels through the shared context, never
			// through package globals.
			nested := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
			nested.mu.Lock()
			nestedErrored = nested.hasErrored
			nested.mu.Unlock()
			return &tool.ToolResult{Data: "ok"}, nil
		}}
		root := t.Context()
		ev := &emitRecorder{}
		ex := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_sib": toolx}, nil, ev.fn, root)
		// Cancel only the sibling chain (a sibling errored, the user did not interrupt).
		ex.siblingCancel(errors.New("sibling_error"))
		ex.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_sib", Input: json.RawMessage(`{}`)}})
		if nestedErrored {
			t.Error("a fresh executor inherited the sibling chain's error state")
		}
		want := "Cancelled: parallel tool call errored"
		tr := ev.only(t, types.EventToolEnd).ToolResult
		if !tr.IsError || tr.DisplayOutput != want {
			t.Errorf("sibling-cancelled event = %+v, want an error event with %q", tr, want)
		}
		if tr.ToolUseID != "tu_1" {
			t.Errorf("sibling-cancelled ToolUseID = %q, want tu_1", tr.ToolUseID)
		}
		if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
			t.Errorf("sibling-cancelled Output = %q, want the abort block carrying %q", got, want)
		}
	})
}

// TestSiblingErrorMessage pins both renderings of the sibling-cancel
// message in emitToolError.
func TestSiblingErrorMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		want string
	}{
		{"Bash(rm -rf)", "Cancelled: parallel tool call Bash(rm -rf) errored"},
		{"", "Cancelled: parallel tool call errored"},
	}
	for _, tc := range cases {
		root := t.Context()
		ev := &emitRecorder{}
		e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev.fn, root)
		e.siblingCancel(errors.New("sibling_error"))
		e.mu.Lock()
		e.errToolDesc = tc.desc
		e.mu.Unlock()
		tt := &TrackedTool{ID: "tu_1", Name: "slow", Input: json.RawMessage(`{}`), done: make(chan struct{})}
		e.emitToolError(&fakeTool{name: "slow"}, tt, errors.New("boom"))

		evt := ev.only(t, types.EventToolEnd)
		tr := evt.ToolResult
		if tr.ToolUseID != "tu_1" || !tr.IsError {
			t.Errorf("desc %q: event = %+v, want an error event for tu_1", tc.desc, tr)
		}
		if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != tc.want {
			t.Errorf("desc %q: Output = %q, want %q", tc.desc, got, tc.want)
		}
		if tr.DisplayOutput != tc.want {
			t.Errorf("desc %q: DisplayOutput = %q, want %q", tc.desc, tr.DisplayOutput, tc.want)
		}
		if tt.Err == nil || tt.Err.Error() != "boom" {
			t.Errorf("desc %q: tracked Err = %v, want the original error", tc.desc, tt.Err)
		}
		if len(tt.resultBlocks) != 1 || !tt.resultBlocks[0].IsError {
			t.Errorf("desc %q: result blocks = %+v, want one error block", tc.desc, tt.resultBlocks)
		}
	}
}

// TestBashErrorCancelsSiblings pins the Bash-only sibling abort and the
// error bookkeeping it leaves behind.
func TestBashErrorCancelsSiblings(t *testing.T) {
	t.Parallel()
	bash := &fakeTool{name: "Bash", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return nil, errors.New("signal: killed")
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Bash": bash}, nil, ev.fn, context.Background())
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_b", Name: "Bash", Input: json.RawMessage(`{"command":"sleep 5"}`)}})

	e.mu.Lock()
	errored, desc := e.hasErrored, e.errToolDesc
	tt := e.tools[0]
	e.mu.Unlock()
	if !errored {
		t.Error("a Bash error did not mark the executor as errored")
	}
	if desc != "Bash(sleep 5)" {
		t.Errorf("errToolDesc = %q, want Bash(sleep 5)", desc)
	}
	if tt.Err == nil || tt.Err.Error() != "signal: killed" {
		t.Errorf("tracked Err = %v, want the Bash error", tt.Err)
	}
	if e.siblingCtx.Err() == nil {
		t.Error("a Bash error did not cancel the sibling context")
	}
}

// TestPostToolUseHookInputs pins the success and failure hook payloads.
func TestPostToolUseHookInputs(t *testing.T) {
	t.Parallel()
	hexec := &integrationHookRecorder{}
	toolx := &fakeTool{name: "ptest_post", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return nil, errors.New("post failure")
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_post": toolx}, nil, (&emitRecorder{}).fn, context.Background())
	e.SetHooks(hooksWith("PostToolUseFailure", "ptest_post", hexec, hooks.HookResult{Outcome: hooks.HookOutcomeSuccess}), "sess-post")
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_post", Input: json.RawMessage(`{"in":7}`)}})

	in := hexec.lastInput(t)
	if in.HookEventName != "PostToolUseFailure" {
		t.Errorf("hook event = %q, want PostToolUseFailure", in.HookEventName)
	}
	if in.SessionID != "sess-post" {
		t.Errorf("hook SessionID = %q, want sess-post", in.SessionID)
	}
	if in.ToolUseID != "tu_1" {
		t.Errorf("hook ToolUseID = %q, want tu_1", in.ToolUseID)
	}
	if string(in.ToolInput) != `{"in":7}` {
		t.Errorf("hook ToolInput = %s, want the tool input", string(in.ToolInput))
	}
	if in.ToolName != "ptest_post" {
		t.Errorf("hook ToolName = %q, want ptest_post", in.ToolName)
	}
}

// TestFileHistoryTracksBeforeEdit pins the pre-edit backup and the
// skip diagnostic.
func TestFileHistoryTracksBeforeEdit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	histDir := filepath.Join(dir, "history")
	ran := false
	toolx := &fakeTool{name: "Edit", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "edited"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Edit": toolx}, nil, (&emitRecorder{}).fn, context.Background())
	e.SetFileHistory(filehistory.NewTracker(histDir))
	e.currentTurnMsgID = "msg-1"
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Edit", Input: json.RawMessage(`{"file_path":"` + src + `"}`)}})
	if !ran {
		t.Fatal("the tracked Edit did not run")
	}
	entries, err := os.ReadDir(histDir)
	if err != nil || len(entries) == 0 {
		t.Errorf("no backup written for a tracked Edit (dir err=%v entries=%d)", err, len(entries))
	}
}

// TestFileHistorySkipWarned pins the diagnostic when an edit runs without
// a history tracker, and its attributes.
func TestFileHistorySkipWarned(t *testing.T) {
	t.Parallel()
	rec := captureSlog(t)
	toolx := &fakeTool{name: "Write", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "written"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Write": toolx}, nil, (&emitRecorder{}).fn, context.Background())
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Write", Input: json.RawMessage(`{"file_path":"/tmp/x"}`)}})

	log, ok := rec.find("engine:file_history_skip", func(a map[string]any) bool {
		return a["tool"] == "Write" && a["currentTurnMsgID"] == ""
	})
	if !ok {
		t.Fatal("an untracked Write left no file-history skip diagnostic")
	}
	if log.attrs["tool"] != "Write" {
		t.Errorf("skip tool attr = %v, want Write", log.attrs["tool"])
	}
	hasFH, _ := log.attrs["hasFileHistory"].(bool)
	if hasFH {
		t.Error("hasFileHistory attr must be false on the skip branch")
	}
	if id, _ := log.attrs["currentTurnMsgID"].(string); id != "" {
		t.Errorf("currentTurnMsgID attr = %q, want empty", id)
	}

	// A non-edit tool is not warned about.
	rec2 := captureSlog(t)
	reader := &fakeTool{name: "Read", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "read"}, nil
	}}
	e2 := NewStreamingToolExecutor(map[string]tool.Tool{"Read": reader}, nil, (&emitRecorder{}).fn, context.Background())
	e2.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/x"}`)}})
	if _, ok := rec2.find("engine:file_history_skip", func(a map[string]any) bool {
		return a["tool"] == "Read"
	}); ok {
		t.Error("a Read was warned about as an untracked edit")
	}
}

// TestTrackEditFailureWarnedAndContinues pins that a failed backup warns
// but does not stop the edit.
func TestTrackEditFailureWarnedAndContinues(t *testing.T) {
	t.Parallel()
	rec := captureSlog(t)
	dir := t.TempDir()
	notAFile := dir // a directory as file_path makes the backup copy fail
	ran := false
	toolx := &fakeTool{name: "Edit", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "edited"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Edit": toolx}, nil, (&emitRecorder{}).fn, context.Background())
	e.SetFileHistory(filehistory.NewTracker(filepath.Join(dir, "history")))
	e.currentTurnMsgID = "msg-1"
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Edit", Input: json.RawMessage(`{"file_path":"` + notAFile + `"}`)}})
	if !ran {
		t.Error("a failed backup stopped the edit")
	}
	if _, ok := rec.find("filehistory:track_edit_failed", func(a map[string]any) bool {
		return a["file"] == notAFile
	}); !ok {
		t.Error("a failed TrackEdit left no warning")
	}
}

// TestAskCacheFastPathSkipsMutex pins that a cached allow is answered
// before the ask mutex is taken: the fast path must not serialize behind a
// pending dialog.
func TestAskCacheReadsUnderTheAskMutex(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, (&emitRecorder{}).fn, context.Background())
	e.sessionAllowed = map[string]bool{"Bash": true}
	if !e.askMu.TryLock() {
		t.Fatal("askMu was already held before the probe")
	}
	tt := &TrackedTool{ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}
	done := make(chan types.UserDecision, 1)
	go func() { done <- e.askUser(tt, permission.Decision{Action: permission.ActionAsk, Message: "why"}, "") }()
	// The cache read lives under askMu (a lock-free read races the
	// allow-always write), so a held mutex must block even a cached allow…
	select {
	case got := <-done:
		t.Fatalf("askUser returned %q while askMu was held", got)
	case <-time.After(1500 * time.Millisecond):
	}
	// …and the cached decision must come back once the mutex is free.
	e.askMu.Unlock()
	select {
	case got := <-done:
		if got != types.DecisionAllow {
			t.Errorf("cached decision = %q, want allow", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("askUser never returned after askMu was released")
	}
}

// TestAskCacheMissOpensDialog pins that a populated cache without the
// matching key still asks, and that the dialog path releases the ask mutex.
func TestAskCacheMissOpensDialog(t *testing.T) {
	t.Parallel()
	ev := &emitRecorder{}
	asks, emit := ev.askTap()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, emit, context.Background())
	e.sessionAllowed = map[string]bool{"Read": true}
	tt := &TrackedTool{ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}
	go func() {
		ask := <-asks
		ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
	}()
	if got := e.askUser(tt, permission.Decision{Action: permission.ActionAsk, Message: "why"}, ""); got != types.DecisionDeny {
		t.Errorf("uncached decision = %q, want deny", got)
	}
	if len(ev.all()) != 1 || ev.all()[0].Ask == nil {
		t.Errorf("events = %d, want one permission dialog", len(ev.all()))
	}
	if !e.askMu.TryLock() {
		t.Error("askUser returned with askMu still held")
	}
	e.askMu.Unlock()
}

// TestExecuteAllReleasesExecutorLock pins that both the queue pump and the
// result-collection phase hand the executor mutex back.
func TestExecuteAllReleasesExecutorLock(t *testing.T) {
	t.Parallel()
	toolx := &fakeTool{name: "ptest_lock", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "ok"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_lock": toolx}, nil, (&emitRecorder{}).fn, context.Background())
	e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_lock", Input: json.RawMessage(`{}`)}})
	if !e.mu.TryLock() {
		t.Error("ExecuteAll returned holding the executor mutex")
	}
	e.mu.Unlock()
}

// TestRichReplayToolsNames pins which tools keep a rich replay payload.
func TestRichReplayToolsNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"mcp__srv__tool", "mcp__x", "Edit", "Write"} {
		if !richReplayTools(name) {
			t.Errorf("richReplayTools(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Bash", "Read", "Grep", "", "mcp_"} {
		if richReplayTools(name) {
			t.Errorf("richReplayTools(%q) = true, want false", name)
		}
	}
}

// TestRichResultDataUnknownTool pins the tool-map gate on its own: a
// rich-replay name that is not in the map must still be rejected.
func TestRichResultDataUnknownTool(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	got, ok := e.richResultData(&TrackedTool{Name: "Edit", Result: &tool.ToolResult{Data: map[string]any{"path": "/x"}}})
	if ok || got != nil {
		t.Errorf("richResultData for a tool absent from the map = (%v, %v), want (nil, false)", got, ok)
	}
}

// TestFileHistoryGates pins the two preconditions of the pre-edit backup
// and the warning that fires when they are missing.
func TestFileHistoryGates(t *testing.T) {
	t.Parallel()
	t.Run("no turn id means no backup", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "target.txt")
		if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		histDir := filepath.Join(dir, "history")
		ran := false
		toolx := &fakeTool{name: "Edit", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			ran = true
			return &tool.ToolResult{Data: "edited"}, nil
		}}
		rec := captureSlog(t)
		e := NewStreamingToolExecutor(map[string]tool.Tool{"Edit": toolx}, nil, (&emitRecorder{}).fn, context.Background())
		e.SetFileHistory(filehistory.NewTracker(histDir))
		e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Edit", Input: json.RawMessage(`{"file_path":"` + src + `"}`)}})
		if !ran {
			t.Fatal("the Edit did not run")
		}
		entries, err := os.ReadDir(histDir)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read history dir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("a turn-less Edit wrote %d backups, want none", len(entries))
		}
		if _, ok := rec.find("engine:file_history_skip", func(a map[string]any) bool {
			has, _ := a["hasFileHistory"].(bool)
			return a["tool"] == "Edit" && has
		}); !ok {
			t.Error("an edit without a turn id left no skip diagnostic")
		}
	})

	t.Run("turn id without a tracker is warned, not dereferenced", func(t *testing.T) {
		ran := false
		toolx := &fakeTool{name: "Edit", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			ran = true
			return &tool.ToolResult{Data: "edited"}, nil
		}}
		ev := &emitRecorder{}
		e := NewStreamingToolExecutor(map[string]tool.Tool{"Edit": toolx}, nil, ev.fn, context.Background())
		e.currentTurnMsgID = "msg-1"
		res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Edit", Input: json.RawMessage(`{"file_path":"/tmp/x"}`)}})
		if !ran {
			t.Error("an Edit with no tracker was aborted")
		}
		if len(res.ToolResultBlocks) != 1 || res.ToolResultBlocks[0].IsError {
			t.Errorf("result = %+v, want a successful edit block", res.ToolResultBlocks)
		}
	})

	t.Run("Edit is warned about, not just Write", func(t *testing.T) {
		rec := captureSlog(t)
		toolx := &fakeTool{name: "Edit", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "edited"}, nil
		}}
		e := NewStreamingToolExecutor(map[string]tool.Tool{"Edit": toolx}, nil, (&emitRecorder{}).fn, context.Background())
		e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Edit", Input: json.RawMessage(`{"file_path":"/tmp/x"}`)}})
		log, ok := rec.find("engine:file_history_skip", func(a map[string]any) bool {
			has, _ := a["hasFileHistory"].(bool)
			return a["tool"] == "Edit" && !has
		})
		if !ok {
			t.Fatal("an untracked Edit left no skip diagnostic")
		}
		if log.attrs["tool"] != "Edit" {
			t.Errorf("skip tool attr = %v, want Edit", log.attrs["tool"])
		}
	})
}

// TestContentPhaseNeedsRules pins that the content-level phase only runs
// when the decision carries content rules.
func TestContentPhaseNeedsRules(t *testing.T) {
	t.Parallel()
	permission.RegisterContentChecker("ptest_norules", func(_ json.RawMessage, rules []permission.Rule) permission.RuleAction {
		if len(rules) == 0 {
			return permission.ActionDeny
		}
		return permission.ActionAllow
	})
	ran := false
	toolx := &fakeTool{name: "ptest_norules", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_norules": toolx}, nil, ev.fn, context.Background())
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAllow, ContentRules: []permission.Rule{}}})
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_norules", Input: json.RawMessage(`{}`)}})
	if !ran {
		t.Errorf("an empty rule list entered the content phase: %s", toolResultText(t, res.ToolResultBlocks[0]))
	}
	if len(ev.all()) != 1 || ev.only(t, types.EventToolEnd).ToolResult.IsError {
		t.Errorf("events = %+v, want one successful tool_end", ev.all())
	}
}

// TestMemoryPathWriteNeedsAutoMemory pins the auto-memory gate: with the
// feature disabled no Write bypasses the permission check.
func TestMemoryPathWriteNeedsAutoMemory(t *testing.T) {
	// Not parallel: the environment gate is process-global.
	dir := t.TempDir()
	inside := filepath.Join(dir, "notes", "a.md")
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "false")
	if isMemoryPathWrite("Write", json.RawMessage(`{"file_path":"`+inside+`"}`), dir+string(filepath.Separator)) {
		t.Error("a Write bypassed permissions while auto-memory is disabled")
	}
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "true")
	if !isMemoryPathWrite("Write", json.RawMessage(`{"file_path":"`+inside+`"}`), dir+string(filepath.Separator)) {
		t.Error("a Write inside the override was policed while auto-memory is enabled")
	}
}

// TestContentRuleAskAnswers pins every answer the content-rule dialog
// accepts and the rejection event it produces.
func TestContentRuleAskAnswers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		answer  types.UserDecision
		wantRan bool
	}{
		{"allow", types.DecisionAllow, true},
		{"allow_always", types.DecisionAllowAlways, true},
		{"deny", types.DecisionDeny, false},
	}
	for i, tc := range cases {
		toolName := "ptest_cask_" + tc.name
		permission.RegisterContentChecker(toolName, func(_ json.RawMessage, rules []permission.Rule) permission.RuleAction {
			if len(rules) == 0 {
				return permission.ActionAllow
			}
			return permission.ActionAsk
		})
		ran := false
		toolx := &fakeTool{name: toolName, safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			ran = true
			return &tool.ToolResult{Data: "ran"}, nil
		}}
		ev := &emitRecorder{}
		asks, emit := ev.askTap()
		e := NewStreamingToolExecutor(map[string]tool.Tool{toolName: toolx}, nil, emit, context.Background())
		e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{
			Action: permission.ActionAllow,
			ContentRules: []permission.Rule{{
				Action: permission.ActionAsk,
				Value:  permission.RuleValue{ToolName: toolName, RuleContent: new("danger")},
			}},
		}})
		go func() {
			ask := <-asks
			ask.ResponseCh <- types.AskResponse{Decision: tc.answer}
		}()
		res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_" + string(rune('0'+i)), Name: toolName, Input: json.RawMessage(`{}`)}})

		if ran != tc.wantRan {
			t.Errorf("%s: tool ran = %v, want %v", tc.name, ran, tc.wantRan)
		}
		if tc.wantRan {
			continue
		}
		want := userRejectMessage
		if len(res.ToolResultBlocks) != 1 || !res.ToolResultBlocks[0].IsError {
			t.Fatalf("%s: result = %+v, want one error block", tc.name, res.ToolResultBlocks)
		}
		if got := toolResultText(t, res.ToolResultBlocks[0]); got != want {
			t.Errorf("%s: result text = %q, want the reject message", tc.name, got)
		}
		evt := ev.only(t, types.EventToolEnd)
		tr := evt.ToolResult
		if tr.ToolUseID != "tu_"+string(rune('0'+i)) {
			t.Errorf("%s: reject event ToolUseID = %q, want the tool call id", tc.name, tr.ToolUseID)
		}
		if !tr.IsError {
			t.Errorf("%s: reject event IsError = false, want true", tc.name)
		}
		if tr.DisplayOutput != want {
			t.Errorf("%s: reject event DisplayOutput = %q, want the reject message", tc.name, tr.DisplayOutput)
		}
		if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
			t.Errorf("%s: reject event Output = %q, want the block carrying the reject message", tc.name, got)
		}
	}
}

// TestMemoryPathWriteEmptyPath pins that an empty file_path never matches
// the memory override, even when the override is the working directory itself.
func TestMemoryPathWriteEmptyPath(t *testing.T) {
	t.Parallel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if isMemoryPathWrite("Write", json.RawMessage(`{"file_path":""}`), cwd) {
		t.Error("an empty file_path resolved to the memory override")
	}
}

// TestPreExecAbortEventShape pins the abort event emitted before a tool
// ever runs, when a sibling already errored.
func TestPreExecAbortEventShape(t *testing.T) {
	t.Parallel()
	ran := false
	toolx := &fakeTool{name: "ptest_pre", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran = true
		return &tool.ToolResult{Data: "ran"}, nil
	}}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"ptest_pre": toolx}, nil, ev.fn, context.Background())
	e.mu.Lock()
	e.hasErrored = true
	e.mu.Unlock()
	res := e.ExecuteAll([]types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "ptest_pre", Input: json.RawMessage(`{}`)}})
	if ran {
		t.Error("a tool ran after a sibling error was recorded")
	}
	want := "Cancelled: parallel tool call errored"
	if len(res.ToolResultBlocks) != 1 || !res.ToolResultBlocks[0].IsError {
		t.Fatalf("result = %+v, want one error block", res.ToolResultBlocks)
	}
	if got := toolResultText(t, res.ToolResultBlocks[0]); got != want {
		t.Errorf("pre-abort result = %q, want %q", got, want)
	}
	tr := ev.only(t, types.EventToolEnd).ToolResult
	if tr.ToolUseID != "tu_1" {
		t.Errorf("pre-abort ToolUseID = %q, want tu_1", tr.ToolUseID)
	}
	if !tr.IsError {
		t.Error("pre-abort IsError = false, want true")
	}
	if tr.DisplayOutput != want {
		t.Errorf("pre-abort DisplayOutput = %q, want %q", tr.DisplayOutput, want)
	}
	if got := toolResultText(t, types.ContentBlock{Content: tr.Output}); got != want {
		t.Errorf("pre-abort Output = %q, want the block carrying %q", got, want)
	}
	if tr.Duration != 0 {
		t.Errorf("pre-abort Duration = %v, want zero (the tool never ran)", tr.Duration)
	}
}

// ---------------------------------------------------------------------------
// askUser dialog pins
// ---------------------------------------------------------------------------

// askTapRecorder forwards ask events to a channel so a test can wait for the
// dialog without polling.
type askTapRecorder struct {
	mu     sync.Mutex
	asks   []types.QueryEvent
	ch     chan types.QueryEvent
	others int
}

func (c *askTapRecorder) fn(evt types.QueryEvent) {
	switch evt.Type {
	case types.EventAsk:
		c.mu.Lock()
		c.asks = append(c.asks, evt)
		c.mu.Unlock()
		c.ch <- evt
	case types.EventToolEnd:
		c.mu.Lock()
		c.others++
		c.mu.Unlock()
	}
}

func (c *askTapRecorder) askCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.asks)
}

// askUserWithDecision runs askUser in the background and returns its decision
// after answering the dialog it opens with d.
func askUserWithDecision(t *testing.T, e *StreamingToolExecutor, ev *askTapRecorder, tt *TrackedTool, d types.UserDecision) types.UserDecision {
	t.Helper()
	done := make(chan types.UserDecision, 1)
	go func() {
		done <- e.askUser(tt, permission.Decision{Action: permission.ActionAsk, Message: "allow?"}, "")
	}()
	select {
	case ask := <-ev.ch:
		ask.Ask.ResponseCh <- types.AskResponse{Decision: d}
	case <-time.After(5 * time.Second):
		t.Fatal("askUser never opened a dialog")
	}
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("askUser did not return after the dialog was answered")
	}
	return ""
}

// A session-allow cache only short-circuits the dialog when it holds this
// exact cache key; a non-nil cache that misses must still ask.
func TestAskUserCacheHitRequiresTheMatchingKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cache map[string]bool
	}{
		{"empty cache", map[string]bool{}},
		{"cache without this tool", map[string]bool{"Read": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := &askTapRecorder{ch: make(chan types.QueryEvent, 4)}
			e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev.fn, context.Background())
			e.sessionAllowed = tc.cache
			tt := &TrackedTool{ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}
			if got := askUserWithDecision(t, e, ev, tt, types.DecisionDeny); got != types.DecisionDeny {
				t.Errorf("decision = %q, want deny (a cache miss must reach the user)", got)
			}
			if n := ev.askCount(); n != 1 {
				t.Errorf("ask events = %d, want 1", n)
			}
		})
	}
}

// The ask mutex must still be held while the dialog is in flight: that is what
// keeps two concurrent asks from prompting the user at the same time.
func TestAskUserHoldsAskMutexWhileEmittingTheDialog(t *testing.T) {
	t.Parallel()
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, nil, context.Background())
	held := make(chan bool, 1)
	e.emitEvent = func(evt types.QueryEvent) {
		if evt.Type != types.EventAsk {
			return
		}
		free := e.askMu.TryLock()
		if free {
			e.askMu.Unlock()
		}
		held <- !free
		evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
	}
	tt := &TrackedTool{ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}
	done := make(chan types.UserDecision, 1)
	go func() {
		done <- e.askUser(tt, permission.Decision{Action: permission.ActionAsk, Message: "allow?"}, "")
	}()
	select {
	case got := <-held:
		if !got {
			t.Error("askMu was already released while the dialog was being emitted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("askUser never emitted the dialog")
	}
	select {
	case got := <-done:
		if got != types.DecisionAllow {
			t.Errorf("decision = %q, want allow", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("askUser did not return")
	}
}

// A second ask for the same tool must be settled by the cache re-check taken
// after the ask mutex, not by opening another dialog.
func TestAskUserRechecksCacheAfterTakingTheAskMutex(t *testing.T) {
	t.Parallel()
	ev := &askTapRecorder{ch: make(chan types.QueryEvent, 4)}
	e := NewStreamingToolExecutor(map[string]tool.Tool{}, nil, ev.fn, context.Background())
	ask := permission.Decision{Action: permission.ActionAsk, Message: "allow?"}
	ttA := &TrackedTool{ID: "tu_a", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}
	ttB := &TrackedTool{ID: "tu_b", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}
	resA := make(chan types.UserDecision, 1)
	resB := make(chan types.UserDecision, 1)
	go func() { resA <- e.askUser(ttA, ask, "") }()
	var firstAsk types.QueryEvent
	select {
	case firstAsk = <-ev.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the first ask never opened a dialog")
	}
	// B passes the pre-lock cache check (still empty) and then waits for the
	// ask mutex A holds.
	go func() { resB <- e.askUser(ttB, ask, "") }()
	firstAsk.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllowAlways}
	select {
	case d := <-resA:
		if d != types.DecisionAllowAlways {
			t.Fatalf("A decision = %q, want allow-always", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("A never returned after being answered")
	}
	select {
	case d := <-resB:
		if d != types.DecisionAllow {
			t.Errorf("B decision = %q, want allow from the post-lock cache re-check", d)
		}
	case a := <-ev.ch:
		a.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
		t.Error("B opened a second dialog — the post-lock cache re-check did not short-circuit it")
	case <-time.After(5 * time.Second):
		t.Fatal("B never returned: the post-lock cache re-check did not settle it")
	}
	if n := ev.askCount(); n != 1 {
		t.Errorf("ask events = %d, want exactly 1", n)
	}
}

// toolUseBlock builds a tool_use block for the executor tests.
func toolUseBlock(id, name string, input string) types.ContentBlock {
	return types.ContentBlock{Type: types.ContentTypeToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

// Two Edit calls on different files are independent: the file-conflict rule
// must not serialize them.
func TestCanExecuteTool_SerializesOnlyTheSameFile(t *testing.T) {
	t.Parallel()
	edits := make(chan string, 4)
	release := make(chan struct{})
	ed := &fakeTool{name: "Edit", safe: true, callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		edits <- extractFilePathFromInput(input)
		<-release
		return &tool.ToolResult{Data: "ok"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Edit": ed}, nil, nil, context.Background())
	e.AddTool(toolUseBlock("tu_a", "Edit", `{"file_path":"/tmp/a.go"}`), time.Time{})
	e.AddTool(toolUseBlock("tu_b", "Edit", `{"file_path":"/tmp/b.go"}`), time.Time{})
	seen := map[string]bool{}
	for range 2 {
		select {
		case p := <-edits:
			seen[p] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only these Edit calls started: %v (different files must run together)", seen)
		}
	}
	if !seen["/tmp/a.go"] || !seen["/tmp/b.go"] {
		t.Errorf("Edit calls = %v, want both files", seen)
	}
	close(release)
}

// A PermissionRequest hook result without parsed output is neither an approval
// nor a block: the permission phase must fall through to the dialog intact.
func TestPermissionRequestHookWithoutOutputFallsThroughToAsk(t *testing.T) {
	t.Parallel()
	ex := &integrationHookRecorder{}
	h := hooksWith("PermissionRequest", "*", ex, hooks.HookResult{Outcome: hooks.HookOutcomeSuccess})
	ev := &askTapRecorder{ch: make(chan types.QueryEvent, 4)}
	ran := make(chan bool, 1)
	bash := &fakeTool{name: "Bash", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran <- true
		return &tool.ToolResult{Data: "ok"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Bash": bash}, nil, ev.fn, context.Background())
	e.SetHooks(h, "gtest-session")
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk, Message: "allow?"}})
	e.AddTool(toolUseBlock("tu_1", "Bash", `{"command":"ls"}`), time.Time{})
	select {
	case ask := <-ev.ch:
		ask.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
	case <-time.After(5 * time.Second):
		t.Fatal("no permission dialog after a hook result without output")
	}
	_ = e.ExecuteAll(nil)
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool never ran after a hook result without output")
	}
}

// An "allow" PermissionRequest hook result must not be read as a block — and
// under TS runHooks semantics it resolves the request itself: no dialog, the
// tool still runs.
func TestPermissionRequestAllowHookDoesNotBlockTheTool(t *testing.T) {
	t.Parallel()
	ex := &integrationHookRecorder{}
	h := hooksWith("PermissionRequest", "*", ex,
		hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "allow"}})
	ev := &askTapRecorder{ch: make(chan types.QueryEvent, 4)}
	ran := make(chan bool, 1)
	bash := &fakeTool{name: "Bash", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		ran <- true
		return &tool.ToolResult{Data: "ok"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Bash": bash}, nil, ev.fn, context.Background())
	e.SetHooks(h, "gtest-session")
	e.SetPermissionChecker(&scriptablePermChecker{decision: permission.Decision{Action: permission.ActionAsk, Message: "allow?"}})
	e.AddTool(toolUseBlock("tu_1", "Bash", `{"command":"ls"}`), time.Time{})
	select {
	case ask := <-ev.ch:
		ask.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
		t.Fatal("the dialog opened despite the allow hook result")
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool never ran after an allow hook result")
	}
	_ = e.ExecuteAll(nil)
	if got := ev.askCount(); got != 0 {
		t.Errorf("the allow hook result opened %d dialogs, want 0", got)
	}
	if got := ex.CallCount(); got != 1 {
		t.Errorf("PermissionRequest dispatches = %d, want exactly 1", got)
	}
}

// A tool discarded between the abort check and the execution gate is settled
// by the re-check: its result block is the synthetic streaming-fallback block.
// The EventToolEnd emitted there is dropped by doEmit's discarded guard, which
// is why the emission carries no observable payload — pinned below so a change
// to either half is visible.
func TestDiscardRecheckSettlesTheToolWithTheFallbackBlock(t *testing.T) {
	t.Parallel()
	ex := &integrationHookRecorder{}
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Bash": &fakeTool{name: "Bash", safe: true}}, nil, ev.fn, context.Background())
	// The PreToolUse hook runs after the pre-execution abort check, so
	// discarding there lands exactly on the re-check below it.
	ex.onCall = e.Discard
	h := hooksWith(string(hooks.HookPreToolUse), "*", ex, hooks.HookResult{Outcome: hooks.HookOutcomeSuccess})
	e.SetHooks(h, "gtest-session")
	res := e.ExecuteAll([]types.ContentBlock{toolUseBlock("tu_1", "Bash", `{"command":"ls"}`)})
	wantOut := string(CreateSyntheticErrorBlock("tu_1", AbortReasonStreamingFallback).Content)
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("result blocks = %d, want 1", len(res.ToolResultBlocks))
	}
	got := res.ToolResultBlocks[0]
	if got.Type != types.ContentTypeToolResult || got.ToolUseID != "tu_1" {
		t.Errorf("result block = %+v, want a tool_result for tu_1", got)
	}
	if string(got.Content) != wantOut {
		t.Errorf("result content = %s, want the streaming-fallback block %s", got.Content, wantOut)
	}
	if n := len(ev.all()); n != 0 {
		t.Errorf("discarded executor emitted %d events, want 0 (doEmit suppresses them)", n)
	}
}

// A tool with no renderable result and no progress output must not deref the
// unset last-progress pointer.
func TestEmptyRenderResultWithoutProgressLeavesDisplayOutputEmpty(t *testing.T) {
	t.Parallel()
	ev := &emitRecorder{}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Plain": &fakeTool{name: "Plain", safe: true}}, nil, ev.fn, context.Background())
	_ = e.ExecuteAll([]types.ContentBlock{toolUseBlock("tu_1", "Plain", `{}`)})
	evt := ev.only(t, types.EventToolEnd)
	if evt.ToolResult == nil {
		t.Fatal("EventToolEnd carries no ToolResult")
	}
	if evt.ToolResult.IsError {
		t.Errorf("tool end reported an error: %s", evt.ToolResult.DisplayOutput)
	}
	if evt.ToolResult.DisplayOutput != "" {
		t.Errorf("DisplayOutput = %q, want empty", evt.ToolResult.DisplayOutput)
	}
}

// extractErrMsg's array branch is gated on the payload actually starting with
// a bracket: an empty payload must not be indexed, and a payload that only
// looks like an array later must fall through to the other shapes.
func TestExtractErrMsgShapeGates(t *testing.T) {
	t.Parallel()
	if got := extractErrMsg(nil); got != "" {
		t.Errorf("extractErrMsg(nil) = %q, want empty", got)
	}
	if got := extractErrMsg(json.RawMessage(`[`)); got != "[" {
		t.Errorf(`extractErrMsg("[") = %q, want the raw payload`, got)
	}
	padded := json.RawMessage(` [{"type":"text","text":"hidden"}]`)
	if got := extractErrMsg(padded); got != string(padded) {
		t.Errorf("extractErrMsg(leading-space array) = %q, want the raw payload (the gate reads the first byte)", got)
	}
	arr := json.RawMessage(`[{"type":"text","text":"shown"}]`)
	if got := extractErrMsg(arr); got != "shown" {
		t.Errorf("extractErrMsg(array) = %q, want the first text block", got)
	}
}

// The cancelled-path yield window ends as soon as the tool goroutine reaches
// close(tt.done); it must not burn the whole window.
func TestExecuteAllYieldWindowEndsWhenTheToolCompletes(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entered := make(chan struct{})
	finish := make(chan struct{})
	// InterruptBlock keeps the tool running past the cancelled root context,
	// which is what puts ExecuteAll in the bounded-yield branch.
	slow := &fakeTool{name: "Blocker", safe: true, interrupt: tool.InterruptBlock,
		callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			close(entered)
			<-finish
			return &tool.ToolResult{Data: "ok"}, nil
		}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Blocker": slow}, nil, nil, ctx)
	go func() {
		<-entered
		close(finish)
	}()
	start := time.Now() // REAL-TIME: the 100ms yield window is a wall-clock bound
	res := e.ExecuteAll([]types.ContentBlock{toolUseBlock("tu_1", "Blocker", `{}`)})
	elapsed := time.Since(start)
	if len(res.ToolResultBlocks) != 1 {
		t.Fatalf("result blocks = %d, want 1", len(res.ToolResultBlocks))
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("ExecuteAll waited %v after the tool completed, want well under the 100ms yield window", elapsed)
	}
}

// processQueue holds e.mu for its whole scan, so concurrent calls cannot start
// the same queued tool twice.
func TestProcessQueueStartsEachQueuedToolExactlyOnce(t *testing.T) {
	t.Parallel()
	const queued = 200
	var starts atomic.Int64
	release := make(chan struct{})
	allStarted := make(chan struct{})
	slow := &fakeTool{name: "Slow", safe: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		if starts.Add(1) == int64(queued) {
			close(allStarted)
		}
		<-release
		return &tool.ToolResult{Data: "ok"}, nil
	}}
	e := NewStreamingToolExecutor(map[string]tool.Tool{"Slow": slow}, nil, nil, context.Background())
	e.mu.Lock()
	for i := range queued {
		e.tools = append(e.tools, &TrackedTool{
			ID:                fmt.Sprintf("tu_%d", i),
			Name:              "Slow",
			IsConcurrencySafe: true,
			Status:            StatusQueued,
			done:              make(chan struct{}),
		})
	}
	e.mu.Unlock()
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			<-gate
			e.processQueue()
		})
	}
	close(gate)
	wg.Wait()
	select {
	case <-allStarted:
	case <-time.After(5 * time.Second):
	}
	n := starts.Load()
	close(release)
	if n != queued {
		t.Errorf("tool starts = %d, want %d (a queued tool must be handed to exactly one goroutine)", n, queued)
	}
}
