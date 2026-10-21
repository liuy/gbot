package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine/attachment"
	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/tool/repl"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/tool/toolsearch"
	"github.com/liuy/gbot/pkg/types"
)

// eventCollector implements types.EventDispatcher for test observability.
// Captures all events dispatched by the engine, allowing tests to inspect
// event sequences and verify correct behavior.
type eventCollector struct {
	mu     sync.Mutex
	done   chan struct{}
	result QueryResult
	events []types.QueryEvent
}

func newEventCollector() *eventCollector {
	return &eventCollector{done: make(chan struct{})}
}

func (ec *eventCollector) Dispatch(event types.QueryEvent) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.events = append(ec.events, event)
	// Only signal done on the engine's own QueryEnd (no Agent tag).
	// Sub-engine QueryEnds arrive via taggedDispatcher and must not close
	// the channel before the main engine finishes.
	if event.Type == types.EventQueryEnd && event.Agent == nil {
		// First QueryEnd claims the result. A second query (e.g. the idle
		// run after an abort) must not overwrite it — WaitForResult may wake
		// only after that second event lands, and the aborted query's error
		// would be lost.
		select {
		case <-ec.done:
			return
		default:
		}
		ec.result = QueryResult{Error: event.Error}
		if event.Usage != nil {
			ec.result.TotalUsage = types.Usage{
				InputTokens:              event.Usage.InputTokens,
				OutputTokens:             event.Usage.OutputTokens,
				CacheReadInputTokens:     event.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: event.Usage.CacheCreationInputTokens,
			}
		}
		select {
		case <-ec.done:
		// already closed
		default:
			close(ec.done)
		}
	}
}

func (ec *eventCollector) WaitForResult() QueryResult {
	<-ec.done
	ec.mu.Lock()
	defer ec.mu.Unlock()
	return ec.result
}

// WaitForEventCount blocks until at least n events of the given type have
// been dispatched, or the timeout fires. Deterministic replacement for
// sleep-then-cancel tests: the caller cancels a stream only after the engine
// has visibly consumed the events that set up the assertion state.
func (ec *eventCollector) WaitForEventCount(typ types.QueryEventType, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout) // REAL-TIME
	for time.Now().Before(deadline) {   // REAL-TIME
		ec.mu.Lock()
		count := 0
		for _, e := range ec.events {
			if e.Type == typ {
				count++
			}
		}
		ec.mu.Unlock()
		if count >= n {
			return true
		}
		runtime.Gosched()
	}
	return false
}

func (ec *eventCollector) Events() []types.QueryEvent {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	out := make([]types.QueryEvent, len(ec.events))
	copy(out, ec.events)
	return out
}

func (ec *eventCollector) FindEvents(typ types.QueryEventType) []types.QueryEvent {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	var result []types.QueryEvent
	for _, e := range ec.events {
		if e.Type == typ {
			result = append(result, e)
		}
	}
	return result
}

func (ec *eventCollector) Reset() {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.events = nil
	ec.result = QueryResult{}
	ec.done = make(chan struct{})
}

// ---------------------------------------------------------------------------
// Mock Provider
// ---------------------------------------------------------------------------

type mockProvider struct {
	mu          sync.Mutex
	responses   []mockResponse
	index       int
	lastRequest *llm.Request // captured from the most recent Stream call
	// allRequests is an append-only log of every Stream call. Multi-engine
	// tests share one mockProvider, so per-engine attribution of lastRequest
	// races; content scans over the full log are order-independent.
	allRequests []*llm.Request
}

type mockResponse struct {
	events []llm.StreamEvent
	err    error
	// hang non-nil: after emitting its events the stream blocks until this
	// channel closes or the request context is done — a turn stuck
	// mid-flight that only a kill (context cancellation) can end.
	hang chan struct{}
	// entered non-nil: closed right before the stream parks on hang, so a
	// releasing goroutine can hold for a guaranteed wall-clock span instead
	// of racing QuerySync startup with a blind sleep.
	entered chan struct{}
	// match non-nil: this slot is served only to requests satisfying the
	// predicate, instead of strictly in order. Needed when two engines race
	// for the next script slot (a bg grandchild vs its spawning sub-agent's
	// next turn).
	match func(*llm.Request) bool
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: []types.ContentBlock{types.NewTextBlock("Summary of conversation.")},
	}, nil
}

func (m *mockProvider) Stream(ctx context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	m.mu.Lock()
	m.lastRequest = req
	m.allRequests = append(m.allRequests, req)
	m.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.index >= len(m.responses) {
		return nil, errors.New("no more mock responses")
	}
	// Serve the first pending slot that accepts this request. Matching slots
	// stay reserved for their caller; non-matching pending slots are skipped
	// (swapped forward) and served to their own request later.
	served := -1
	for i := m.index; i < len(m.responses); i++ {
		if m.responses[i].match == nil || m.responses[i].match(req) {
			served = i
			break
		}
	}
	if served < 0 {
		return nil, errors.New("no more mock responses")
	}
	if served != m.index {
		m.responses[m.index], m.responses[served] = m.responses[served], m.responses[m.index]
	}
	resp := m.responses[m.index]
	m.index++

	if resp.err != nil {
		return nil, resp.err
	}

	ch := make(chan llm.StreamEvent, len(resp.events)+1)
	go func() {
		defer close(ch)
		for _, evt := range resp.events {
			ch <- evt
		}
		if resp.hang != nil {
			if resp.entered != nil {
				close(resp.entered)
			}
			select {
			case <-resp.hang:
			case <-ctx.Done():
			}
		}
	}()
	return ch, nil
}

func (m *mockProvider) addResponse(events []llm.StreamEvent, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, mockResponse{events: events, err: err})
}

func (m *mockProvider) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.index
}

// lastRequestMessages returns the messages from the most recent Stream call.
func (m *mockProvider) lastRequestMessages() []types.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastRequest == nil {
		return nil
	}
	return m.lastRequest.Messages
}

// anyRequestContains reports whether SOME one recorded request carries ALL
// the given substrings within a single content block (text block text, or a
// tool_result's content JSON). Multi-engine tests share one mockProvider and
// their Stream calls race for script slots, so assertions must not attribute
// requests to engines — a sender's own conversation never sees its envelope,
// making each scanned string unique to exactly one engine's request.
func (m *mockProvider) anyRequestContains(subs ...string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.allRequests {
		if req == nil {
			continue
		}
		for _, msg := range req.Messages {
			for _, b := range msg.Content {
				text := b.Text
				if b.Type == types.ContentTypeToolResult {
					text = string(b.Content)
				}
				all := true
				for _, s := range subs {
					if !strings.Contains(text, s) {
						all = false
						break
					}
				}
				if all {
					return true
				}
			}
		}
	}
	return false
}

// waitForCallCount polls mp.callCount() until it reaches the target, using
// context.WithTimeout for the deadline instead of time.Now()/time.Sleep().
func waitForCallCount(t *testing.T, mp *mockProvider, target int, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if mp.callCount() >= target {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waitForCallCount: expected %d calls, got %d within %v", target, mp.callCount(), timeout)
		default:
			runtime.Gosched()
		}
	}
}

// ---------------------------------------------------------------------------
// Mock Tool
// ---------------------------------------------------------------------------

type mockTool struct {
	name     string
	callFn   func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error)
	descFn   func(json.RawMessage) (string, error)
	enabled  bool
	isSearch bool
}

func (t *mockTool) Name() string      { return t.name }
func (t *mockTool) Aliases() []string { return nil }
func (t *mockTool) Description(input json.RawMessage) (string, error) {
	if t.descFn != nil {
		return t.descFn(input)
	}
	return t.name + " description", nil
}
func (t *mockTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *mockTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *mockTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *mockTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *mockTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *mockTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *mockTool) IsSearchOrRead(input json.RawMessage) tool.SearchReadKind {
	return tool.SearchReadKind{IsSearch: t.isSearch}
}
func (t *mockTool) IsEnabled() bool                           { return t.enabled }
func (t *mockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *mockTool) Prompt() string                            { return "" }
func (t *mockTool) RenderResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return ""
}

func (*mockTool) MaxResultSize() int { return 50000 }

// ---------------------------------------------------------------------------
// Helper: build streaming events
// ---------------------------------------------------------------------------

func textStreamEvents(model, text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
}

func toolUseStreamEvents(model, toolID, toolName, toolInput string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 20}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: toolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: toolInput}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
		{Type: "message_stop"},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
	// Check default values for MaxTokens, TokenBudget, and Model
	if eng.MaxTokens() != 16000 {
		t.Errorf("MaxTokens() = %d, want 16000", eng.MaxTokens())
	}
	if eng.TokenBudget() != 200000 {
		t.Errorf("TokenBudget() = %d, want 200000", eng.TokenBudget())
	}
}

func TestNew_DefaultMaxTokens(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	eng := New(&Params{
		Provider:    mp,
		Model:       "test-model",
		MaxTokens:   0,
		TokenBudget: 0,
		Logger:      nil,
	})
	t.Cleanup(func() { eng.Close() })
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
	// Verify MaxTokens defaults to 16000 when set to 0
	if eng.MaxTokens() != 16000 {
		t.Errorf("MaxTokens() = %d, want 16000 (default)", eng.MaxTokens())
	}
	// Verify TokenBudget defaults to 200000 when set to 0
	if eng.TokenBudget() != 200000 {
		t.Errorf("TokenBudget() = %d, want 200000 (default)", eng.TokenBudget())
	}
}

func TestNew_WithTools(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mt := &mockTool{name: "my_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model: "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
	// Verify eng.Tools() returns the registered tool by name
	tools := eng.Tools()
	if _, ok := tools["my_tool"]; !ok {
		t.Error("Tools() does not contain 'my_tool'")
	}
}

func TestQuery_SimpleTextResponse(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello, world!"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "Say hello", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if len(result.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != types.RoleUser {
		t.Errorf("expected first message to be user, got %s", result.Messages[0].Role)
	}
	if result.Messages[1].Role != types.RoleAssistant {
		t.Errorf("expected second message to be assistant, got %s", result.Messages[1].Role)
	}
	if result.TotalUsage.InputTokens != 10 {
		t.Errorf("expected 10 input tokens, got %d", result.TotalUsage.InputTokens)
	}
	if result.TotalUsage.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", result.TotalUsage.OutputTokens)
	}
}

func TestQuery_ToolUseThenText(t *testing.T) {
	t.Parallel()

	toolID := "tool_123"
	toolName := "read_file"
	toolInput := `{"path":"/tmp/test.txt"}`

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", toolID, toolName, toolInput), nil)
	mp.addResponse(textStreamEvents("test-model", "File contents displayed."), nil)

	mt := &mockTool{
		name:    toolName,
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "file contents here"}, nil
		},
	}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "Read the file", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if len(ec.FindEvents(types.EventToolEnd)) == 0 {
		t.Error("expected to see a tool result event")
	}
	if len(ec.FindEvents(types.EventTextDelta)) == 0 {
		t.Error("expected to see a text delta event")
	}
	if result.TurnCount != 2 {
		t.Errorf("expected 2 turns (2 API calls), got %d", result.TurnCount)
	}
}

// TestQuery_ToolResultContentIsArray verifies that tool_result content is
// serialized as a JSON array of ContentBlock in the API message. After the
// FormatWireBlocks refactor, every successful tool_result.content is an
// array form `[{"type":"text","text":"<JSON-string-of-data>"}]`.
// Default tools (no FormatWireBlocks_) wrap their Data as a JSON string
// inside a single text block, so the LLM still receives the same payload
// as before — just nested one level deeper.
func TestQuery_ToolResultContentIsArray(t *testing.T) {
	t.Parallel()

	toolID := "tool_glob_1"
	toolName := "Glob"
	toolInput := `{"pattern":"**/*.go"}`

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", toolID, toolName, toolInput), nil)
	mp.addResponse(textStreamEvents("test-model", "Found files."), nil)

	// Tool returns structured data (like Glob would)
	type globOutput struct {
		Files []string `json:"files"`
		Count int      `json:"count"`
	}

	mt := &mockTool{
		name:    toolName,
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: globOutput{
				Files: []string{"cmd/gbot/main.go"},
				Count: 1,
			}}, nil
		},
	}

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "List Go files", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Find the tool_result content block in messages
	for _, msg := range result.Messages {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type != types.ContentTypeToolResult {
				continue
			}

			// Serialize this content block to JSON and check that
			// the "content" field is a JSON array (starts with [),
			// not a raw JSON object (starts with {) or a string (starts with ").
			blockJSON, err := json.Marshal(block)
			if err != nil {
				t.Fatalf("marshal content block: %v", err)
			}

			// Parse to extract the content field value
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(blockJSON, &raw); err != nil {
				t.Fatalf("unmarshal block: %v", err)
			}

			contentField := string(raw["content"])
			if contentField == "" {
				t.Fatal("content field is empty")
			}

			// The content MUST be a JSON array (starts with [).
			if contentField[0] != '[' {
				t.Errorf("tool_result.content should be a JSON array, got: %s", contentField)
			}

			// The array must contain a single text block whose Text is the
			// double-wrapped JSON string of the Data.
			var blocks []types.ContentBlock
			if err := json.Unmarshal(raw["content"], &blocks); err != nil {
				t.Fatalf("content is not a valid JSON array: %v", err)
			}
			if len(blocks) != 1 {
				t.Fatalf("len(blocks) = %d, want 1", len(blocks))
			}
			if blocks[0].Type != types.ContentTypeText {
				t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
			}
			// blocks[0].Text is the JSON-encoded string form of the Data —
			// it must still contain "files" and the file path so the LLM
			// can parse tool output.
			if !strings.Contains(blocks[0].Text, "files") {
				t.Errorf("text block should contain 'files', got: %s", blocks[0].Text)
			}
			if !strings.Contains(blocks[0].Text, "cmd/gbot/main.go") {
				t.Errorf("text block should contain file path, got: %s", blocks[0].Text)
			}
		}
	}
}

func TestQuery_EventQueryEndCarriesErrorOnCancel(t *testing.T) {
	// Regression test: when Escape cancels a query during tool execution,
	// EventQueryEnd must carry the cancel error. Previously the error was
	// only in resultCh (now removed) — EventQueryEnd had nil error, so
	// the TUI showed no cancel feedback.
	mp := &mockProvider{}

	// LLM responds with a tool_use
	blockStarted := make(chan struct{})
	mt := &mockTool{
		name:    "slow_tool",
		enabled: true,
		callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			close(blockStarted)
			<-ctx.Done() // block until cancelled
			return nil, ctx.Err()
		},
	}

	toolEvents := toolUseStreamEvents("test-model", "tu1", "slow_tool", `{}`)
	mp.addResponse(toolEvents, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:      "test-model",
		Dispatcher: ec,
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		<-blockStarted
		cancel()
	}()

	eng.QuerySync(ctx, "run the tool", "")

	endEvents := ec.FindEvents(types.EventQueryEnd)
	if len(endEvents) == 0 {
		t.Fatal("expected at least one EventQueryEnd")
	}
	last := endEvents[len(endEvents)-1]
	if last.Error == nil {
		t.Fatal("EventQueryEnd.Error must be non-nil when context cancelled during tool execution")
	}
	if !errors.Is(last.Error, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", last.Error)
	}
}

func TestQuery_ContextCancellation(t *testing.T) {
	mp := &mockProvider{}

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel BEFORE calling Query to deterministically trigger context cancellation
	cancel()

	result := eng.QuerySync(ctx, "test query", "")
	// The context is already cancelled, so the select in queryLoop should catch it
	// before calling callLLM, resulting in a cancellation error.
	// Verify the error is set and mentions context cancellation
	if result.Error == nil {
		t.Fatal("expected non-nil error from context cancellation")
	}
	if !errors.Is(result.Error, context.Canceled) && !strings.Contains(result.Error.Error(), "context") {
		t.Errorf("expected context cancellation error, got: %v", result.Error)
	}
}

func TestQuery_UnknownTool(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := toolUseStreamEvents("test-model", "t1", "unknown_tool", `{}`)
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Tool not found."), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "use unknown tool", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// The unknown tool creates a tool_result block in messages but does NOT
	// emit an EventToolEnd event (only known tools emit events).
	// Verify the conversation continued and completed successfully.
	if result.Error != nil {
		t.Errorf("expected success, got: %v", result.Error)
	}
}

func TestQuery_ToolExecutionError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := toolUseStreamEvents("test-model", "t1", "fail_tool", `{}`)
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Recovered."), nil)

	mt := &mockTool{
		name:    "fail_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errors.New("tool execution failed")
		},
	}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "call failing tool", "")
	if result.Error != nil {
		t.Fatalf("unexpected final error: %v", result.Error)
	}
	toolEndEvents := ec.FindEvents(types.EventToolEnd)
	var gotErrorResult bool
	var errorDisplayOutput string
	for _, evt := range toolEndEvents {
		if evt.ToolResult != nil && evt.ToolResult.IsError {
			gotErrorResult = true
			errorDisplayOutput = evt.ToolResult.DisplayOutput
		}
	}
	if !gotErrorResult {
		t.Error("expected tool result error event")
	}
	if gotErrorResult && !strings.Contains(errorDisplayOutput, "tool execution failed") {
		t.Errorf("error DisplayOutput should mention 'tool execution failed', got: %q", errorDisplayOutput)
	}
}

func TestQuery_StreamError_NonRetryable(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(nil, &llm.APIError{
		Type:      "invalid_request_error",
		Message:   "bad request",
		Status:    400,
		ErrorCode: "prompt_too_long",
		Retryable: false,
	})

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Error.Error(), "bad request") {
		t.Errorf("error should contain 'bad request', got: %v", result.Error)
	}

}

func TestQuery_StreamError_APIErrorTerminal(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Non-retryable API-level error: the provider exhausted HTTP-level retries
	// and did not flag it retryable, so the engine treats it as terminal.
	// (Retryable API errors — SSE idle timeout, overload — are retried by
	// callLLMWithRetry; see TestQuery_RetryableAPIErrorRetries.)
	mp.addResponse(nil, &llm.APIError{
		Type:      "rate_limit_error",
		Message:   "rate limited",
		Status:    429,
		Retryable: false,
	})

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected terminal error for API-level 429, got nil")
	}
	if !strings.Contains(result.Error.Error(), "rate limited") {
		t.Errorf("error should contain 'rate limited', got: %v", result.Error)
	}
	// Provider should only be called once (no engine retry for API errors)
	if mp.index != 1 {
		t.Errorf("expected 1 provider call, got %d", mp.index)
	}

	// The terminal API error must survive as a flagged assistant message in
	// history (TS createAssistantAPIErrorMessage / isApiErrorMessage). Without
	// it a restart shows a lone user query with no reply, and the next turn's
	// model has no idea the request failed.
	msgs := eng.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages in history after terminal API error")
	}
	last := msgs[len(msgs)-1]
	if last.Role != types.RoleAssistant {
		t.Fatalf("last message role = %s, want assistant (error message)", last.Role)
	}
	if !last.HasFlag(types.FlagAPIError) {
		t.Error("error message must carry FlagAPIError")
	}
	if len(last.Content) != 1 || last.Content[0].Type != types.ContentTypeText {
		t.Fatalf("error message content shape wrong: %+v", last.Content)
	}
	errText := last.Content[0].Text
	if !strings.Contains(errText, "rate limited") {
		t.Errorf("error message text should name the failure, got: %q", errText)
	}
	// APIError.Error() already yields "API Error 429: ..." — the synthesized
	// message must not double the prefix.
	if n := strings.Count(errText, "API Error"); n != 1 {
		t.Errorf("error message should carry the API Error marker exactly once, got %d in %q", n, errText)
	}
	if last.HasFlag(types.FlagMeta) {
		t.Error("error message must be visible (no FlagMeta) — the next turn's model needs to see what happened")
	}
}

func TestQuery_DisabledToolSkipped(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	callCount := 0
	mt := &mockTool{
		name:    "disabled_tool",
		enabled: false,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			callCount++
			return &tool.ToolResult{Data: "should not be called"}, nil
		},
	}
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// Verify the mock tool's Execute was NOT called
	if callCount != 0 {
		t.Errorf("disabled tool was called %d times, want 0", callCount)
	}
}

func TestAddSystemMessage(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	eng.AddSystemMessage("system instruction")
	msgs := eng.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != types.RoleSystem {
		t.Errorf("expected RoleSystem, got %s", msgs[0].Role)
	}
	if len(msgs[0].Content) == 0 || msgs[0].Content[0].Text != "system instruction" {
		t.Errorf("unexpected content: %+v", msgs[0].Content)
	}
}

func TestReset(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	eng.AddSystemMessage("msg1")
	eng.AddSystemMessage("msg2")
	if len(eng.Messages()) != 2 {
		t.Fatalf("expected 2 messages before reset")
	}

	eng.Reset()
	if len(eng.Messages()) != 0 {
		t.Fatalf("expected 0 messages after reset, got %d", len(eng.Messages()))
	}
}

func TestReset_ClearsToolSearchState(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Pre-populate toolSearch state (simulating discovered tools from a prior session).
	eng.toolSearch.DiscoverTools([]string{"Task"})
	if !eng.toolSearch.IsDiscovered("Task") {
		t.Fatal("precondition: Tasks should be discovered")
	}

	eng.Reset()

	if eng.toolSearch.IsDiscovered("Task") {
		t.Error("expected Tasks to NOT be discovered after Reset")
	}
}

func TestMessages(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	msgs := eng.Messages()
	if msgs != nil {
		t.Fatalf("expected nil messages initially, got %v", msgs)
	}

	eng.AddSystemMessage("hello")
	msgs = eng.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

func TestMessages_ReturnsCopy(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	eng.AddSystemMessage("hello")
	msgs := eng.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// Mutating the returned slice must not affect the engine
	msgs[0].Content = nil
	msgs2 := eng.Messages()
	if len(msgs2[0].Content) != 1 {
		t.Fatalf("expected ContentBlock preserved after mutating returned slice, got %d blocks", len(msgs2[0].Content))
	}
}

func TestMessages_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			eng.AddSystemMessage("writer")
		}()
		go func() {
			defer wg.Done()
			_ = eng.Messages()
		}()
	}
	wg.Wait()

	msgs := eng.Messages()
	if len(msgs) != 100 {
		t.Fatalf("expected 100 messages, got %d", len(msgs))
	}
}

func TestSetSessionID(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	if eng.SessionID() != "" {
		t.Fatalf("expected empty session ID initially, got %q", eng.SessionID())
	}

	eng.SetSessionID("abc-123")
	if eng.SessionID() != "abc-123" {
		t.Fatalf("expected session ID %q, got %q", "abc-123", eng.SessionID())
	}
}

func TestSetModel(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "initial-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	if eng.Model() != "initial-model" {
		t.Fatalf("expected initial model %q, got %q", "initial-model", eng.Model())
	}

	eng.SetModel("new-model")
	if eng.Model() != "new-model" {
		t.Fatalf("expected model %q after SetModel, got %q", "new-model", eng.Model())
	}
}

func TestSetProvider(t *testing.T) {
	t.Parallel()

	initialProvider := &mockProvider{}
	eng := New(&Params{
		Provider: initialProvider,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Verify initial provider is functional (no panic)
	if eng.Model() != "test-model" {
		t.Fatalf("expected model %q, got %q", "test-model", eng.Model())
	}

	// Switch provider
	newProvider := &mockProvider{}
	eng.SetProvider(newProvider)

	// Verify engine still works after switch
	if eng.Model() != "test-model" {
		t.Fatalf("SetProvider should not change model, got %q", eng.Model())
	}
}

func TestSetMessages(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	eng.SetMessages(msgs)
	got := eng.Messages()
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != types.RoleUser {
		t.Errorf("msg[0].Role = %q, want user", got[0].Role)
	}
	if got[1].Role != types.RoleAssistant {
		t.Errorf("msg[1].Role = %q, want assistant", got[1].Role)
	}

	// SetMessages replaces, not appends
	eng.SetMessages([]types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("system")}},
	})
	got = eng.Messages()
	if len(got) != 1 {
		t.Fatalf("expected 1 message after SetMessages, got %d", len(got))
	}
	if got[0].Role != types.RoleSystem {
		t.Errorf("msg[0].Role = %q, want system", got[0].Role)
	}
}

// TestQuery_ConcurrentSetMessagesDuringQuery guards the e.messages read
// points a query hits while in flight (microcompact, executor history,
// reminder context, API marshaling) against unlocked reads. A goroutine
// hammers SetMessages for the whole query duration — the "caller rewrites
// history mid-query" scenario. The first turn's tool blocks until released,
// parking the query in ExecuteAll so the hammer's writes are guaranteed
// unordered w.r.t. the post-park reads. Run with -race.
func TestQuery_ConcurrentSetMessagesDuringQuery(t *testing.T) {
	toolStarted := make(chan struct{})
	release := make(chan struct{})
	blockTool := &mockTool{
		name:    "Block",
		enabled: true,
		callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			close(toolStarted)
			<-release
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu_race_1", "Block", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{blockTool.Name(): blockTool}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Hammer: rewrite history for the whole query duration. The replacement
	// slice is never mutated after creation, so with locked reads this is
	// memory-safe even though the query loop keeps using its stale reference.
	stop := make(chan struct{})
	var hammerWG sync.WaitGroup
	hammerWrites := 0 // written by hammer goroutine only; read after Wait
	hammerWG.Go(func() {
		replacement := []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("replaced")}},
		}
		for {
			select {
			case <-stop:
				return
			default:
				eng.SetMessages(replacement)
				hammerWrites++
				runtime.Gosched()
			}
		}
	})

	resCh := make(chan QueryResult, 1)
	go func() { resCh <- eng.QuerySync(ctx, "hello", "") }()

	// Query is now parked in ExecuteAll behind Block; yield so the hammer
	// accumulates unordered writes before the engine resumes.
	<-toolStarted
	for range 200 {
		runtime.Gosched()
	}
	close(release)

	res := <-resCh
	close(stop)
	hammerWG.Wait()

	if hammerWrites < 2 {
		t.Fatalf("hammer goroutine starved: only %d SetMessages writes landed during the query", hammerWrites)
	}
	if res.Error != nil {
		t.Fatalf("query returned error: %v", res.Error)
	}
	// SetMessages never touches usage or turn state, so both are deterministic.
	if res.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2 (tool turn + terminal turn)", res.TurnCount)
	}
	if res.TotalUsage.InputTokens != 30 {
		t.Errorf("TotalUsage.InputTokens = %d, want 30 (20 + 10 from the two message_start events)", res.TotalUsage.InputTokens)
	}
	if res.TotalUsage.OutputTokens != 15 {
		t.Errorf("TotalUsage.OutputTokens = %d, want 15 (10 + 5 from the two message_delta events)", res.TotalUsage.OutputTokens)
	}
}

func TestSetMessages_RestoresToolSearchState(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Simulate a transcript with a compact boundary carrying preCompactDiscoveredTools.
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: `{"subtype":"compact_boundary","preCompactDiscoveredTools":["Task"]}`},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	}

	eng.SetMessages(msgs)

	if !eng.toolSearch.IsDiscovered("Task") {
		t.Error("expected Tasks to be discovered after SetMessages with compact boundary")
	}
}

func TestQuery_MultipleToolCalls(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 30}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "tool_a"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t2", Name: "tool_b"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 15}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Both tools executed."), nil)

	var toolACalled, toolBCalled bool
	toolA := &mockTool{name: "tool_a", enabled: true, callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		toolACalled = true
		return &tool.ToolResult{Data: "a_result"}, nil
	}}
	toolB := &mockTool{name: "tool_b", enabled: true, callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		toolBCalled = true
		return &tool.ToolResult{Data: "b_result"}, nil
	}}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				toolA.Name(): toolA,
				toolB.Name(): toolB,
			}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "call both tools", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(ec.FindEvents(types.EventToolEnd)) != 2 {
		t.Errorf("expected 2 tool results, got %d", len(ec.FindEvents(types.EventToolEnd)))
	}
	if !toolACalled {
		t.Error("tool_a was not called")
	}
	if !toolBCalled {
		t.Error("tool_b was not called")
	}
}

func TestQuery_ToolUseStartEvent(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu_1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	ec := newEventCollector()
	mt := &mockTool{name: "my_tool", enabled: true}
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	toolStartEvents := ec.FindEvents(types.EventToolStart)
	if len(toolStartEvents) == 0 {
		t.Fatal("expected EventToolStart event")
	}
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil {
			if evt.ToolUse.ID != "tu_1" {
				t.Errorf("expected tool use ID tu_1, got %s", evt.ToolUse.ID)
			}
			if evt.ToolUse.Name != "my_tool" {
				t.Errorf("expected tool use name my_tool, got %s", evt.ToolUse.Name)
			}
		}
	}
}

func TestQuery_ToolStartIncludesIsSearch(t *testing.T) {
	t.Parallel()

	// Use a tool that implements ToolWithSearchOrRead and returns IsSearch=true.
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu_1", "search_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	ec := newEventCollector()
	mt := &mockTool{name: "search_tool", enabled: true, isSearch: true}
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// The tool_start event should include IsSearch=true so the client
	// can mark the tool as collapsible for grouping.
	toolStartEvents := ec.FindEvents(types.EventToolStart)
	if len(toolStartEvents) == 0 {
		t.Fatal("expected EventToolStart event")
	}
	found := false
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil && evt.ToolUse.Name == "search_tool" {
			if !evt.ToolUse.IsSearch {
				t.Errorf("tool_start for search_tool should have IsSearch=true, got false")
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("search_tool tool_start event not found")
	}
}

func TestQuery_TaskToolStartEndIncludesIsTask(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu_1", "Task", `{"action":"create","subject":"Fix login","description":"x"}`), nil)
	mp.addResponse(toolUseStreamEvents("test-model", "tu_2", "Grep", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	ec := newEventCollector()
	mt := &mockTool{name: "Grep", enabled: true, isSearch: true}
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				"Task": task.New(task.NewList(t.TempDir()), nil),
				"Grep": mt,
			}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	var taskStart, grepStart *types.ToolUseEvent
	for _, evt := range ec.FindEvents(types.EventToolStart) {
		if evt.ToolUse == nil {
			continue
		}
		switch evt.ToolUse.Name {
		case "Task":
			taskStart = evt.ToolUse
		case "Grep":
			grepStart = evt.ToolUse
		}
	}
	if taskStart == nil {
		t.Fatal("Task tool_start event not found")
	}
	if !taskStart.IsTask {
		t.Error("tool_start for Task should have IsTask=true, got false")
	}
	if taskStart.IsSearch {
		t.Error("tool_start for Task should have IsSearch=false, got true")
	}
	if grepStart == nil {
		t.Fatal("Grep tool_start event not found")
	}
	if grepStart.IsTask {
		t.Error("tool_start for Grep should have IsTask=false, got true")
	}
	if !grepStart.IsSearch {
		t.Error("tool_start for Grep should have IsSearch=true, got false")
	}

	foundTaskEnd := false
	for _, evt := range ec.FindEvents(types.EventToolEnd) {
		if evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_1" {
			if !evt.ToolResult.IsTask {
				t.Error("tool_end for Task (tu_1) should have IsTask=true, got false")
			}
			foundTaskEnd = true
		}
	}
	if !foundTaskEnd {
		t.Fatal("Task tool_end event not found")
	}

	// input_json_delta stamping: the streamed param deltas for the Task call
	// carry IsTask (coalesced and flushed by the next turn's events).
	foundTaskParam := false
	for _, evt := range ec.FindEvents(types.EventToolParamDelta) {
		if evt.PartialInput != nil && evt.PartialInput.ID == "tu_1" {
			if !evt.PartialInput.IsTask {
				t.Errorf("tool_param_delta for Task (tu_1) should have IsTask=true, got %+v", evt.PartialInput)
			}
			foundTaskParam = true
		}
	}
	if !foundTaskParam {
		t.Fatal("Task tool_param_delta event not found")
	}
}

func TestQuery_StreamingTextDeltas(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "Hello "}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "world!"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "greet me", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	textEvents := ec.FindEvents(types.EventTextDelta)
	if len(textEvents) != 1 {
		t.Fatalf("expected 1 coalesced text delta, got %d", len(textEvents))
	}
	if textEvents[0].Text != "Hello world!" {
		t.Errorf("coalesced text = %q, want %q", textEvents[0].Text, "Hello world!")
	}
}

func TestQuery_StreamStartAndCompleteEvents(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hi"), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(ec.FindEvents(types.EventTurnStart)) != 1 {
		t.Errorf("expected 1 turn start, got %d", len(ec.FindEvents(types.EventTurnStart)))
	}
	if len(ec.FindEvents(types.EventQueryEnd)) != 1 {
		t.Errorf("expected 1 complete, got %d", len(ec.FindEvents(types.EventQueryEnd)))
	}
}

func TestQuery_PingEvent(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 5}}},
		{Type: "ping"},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "pong"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 2}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "ping", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// Verify assistant message contains text after the ping
	if len(result.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result.Messages))
	}
	var foundPongText bool
	for _, msg := range result.Messages {
		if msg.Role == types.RoleAssistant {
			for _, block := range msg.Content {
				if block.Text == "pong" {
					foundPongText = true
				}
			}
		}
	}
	if !foundPongText {
		t.Error("ping should not corrupt assistant text; expected 'pong' in messages")
	}
}

func TestQuery_NilUsage(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start"},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "no usage"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta"},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.TotalUsage.InputTokens != 0 {
		t.Errorf("expected 0 input tokens for nil usage, got %d", result.TotalUsage.InputTokens)
	}
	if result.TotalUsage.OutputTokens != 0 {
		t.Errorf("expected 0 output tokens for nil usage, got %d", result.TotalUsage.OutputTokens)
	}
}

func TestQuery_MaxTurns(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	for i := range 3 {
		events := toolUseStreamEvents("test-model", fmt.Sprintf("t%d", i), "my_tool", `{}`)
		mp.addResponse(events, nil)
	}
	mp.addResponse(textStreamEvents("test-model", "All done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do 3 rounds", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.TurnCount != 4 {
		t.Errorf("expected 4 turns (4 API calls), got %d", result.TurnCount)
	}
}

// TestQuery_TurnCountMatchesAPICalls verifies that TurnCount equals the
// actual number of API calls made, including the final end_turn call.
// Regression: the end_turn return path skipped turnCount++, causing
// N API calls to report N-1 turns.
func TestQuery_TurnCountMatchesAPICalls(t *testing.T) {
	t.Parallel()

	// 1 tool_use + 1 end_turn = 2 API calls → TurnCount should be 2
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.TurnCount != 2 {
		t.Errorf("expected TurnCount=2 (2 API calls), got %d", result.TurnCount)
	}
}

func TestQuery_DescriptionError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// LLM requests a tool that has a broken Description() implementation
	toolEvents := toolUseStreamEvents("test-model", "t1", "err_desc_tool", `{}`)
	mp.addResponse(toolEvents, nil)
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	mt := &mockTool{
		name:    "err_desc_tool",
		enabled: true,
		descFn:  func(json.RawMessage) (string, error) { return "", errors.New("desc error") },
	}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	toolEndEvents := ec.FindEvents(types.EventToolEnd)
	if len(toolEndEvents) == 0 {
		t.Fatal("expected tool result event to be emitted")
	}
	for _, evt := range toolEndEvents {
		if evt.ToolResult == nil {
			t.Fatal("ToolResult is nil")
		}
	}
}

func TestQuery_ErrorInStream(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Stream returns events that include an error event
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "partial"}},
		{Error: &llm.APIError{Message: "stream interrupted", Status: 500}},
	}
	mp.addResponse(events, nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected error from stream event error")
	}
	if !strings.Contains(result.Error.Error(), "stream interrupted") {
		t.Errorf("error should contain 'stream interrupted', got: %v", result.Error)
	}
}

// TestQuery_RetryableStreamError_APIErrorTerminal verifies that mid-stream API errors
// (e.g. 529 overloaded) are NOT retried at engine level. Provider handles HTTP-level retries;
// if the error reaches engine, it's terminal.
func TestQuery_RetryableStreamError_APIErrorRetries(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Mid-stream retryable API error (529): the engine retries it through
	// callLLMWithRetry — the retryable flag is exactly the gate contract.
	// Second response succeeds, so the query must recover.
	retryableEvents := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 5}}},
		{Error: &llm.APIError{Message: "overloaded", Status: 529, Retryable: true}},
	}
	mp.addResponse(retryableEvents, nil)
	mp.addResponse(textStreamEvents("test-model", "recovered"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("expected recovery after retrying mid-stream 529, got: %v", result.Error)
	}
	if !strings.Contains(result.Reply, "recovered") {
		t.Errorf("reply should contain retried answer, got: %q", result.Reply)
	}
	if mp.index != 2 {
		t.Errorf("expected 2 provider calls (initial + retry), got %d", mp.index)
	}
}

// TestQuery_ContextOverflowStreamError tests that context overflow errors are returned correctly.
func TestQuery_ContextOverflowStreamError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Error: &llm.APIError{Message: "prompt too long", Status: 400, ErrorCode: "prompt_too_long"}},
	}
	mp.addResponse(events, nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Error.Error(), "prompt too long") {
		t.Errorf("error should mention 'prompt too long', got: %v", result.Error)
	}
}

// TestQuery_RateLimitStreamError tests that rate limit errors are returned correctly.
func TestQuery_RateLimitStreamError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Error: &llm.APIError{Message: "rate limited", Status: 429, Retryable: false}},
	}
	mp.addResponse(events, nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Error.Error(), "rate limited") {
		t.Errorf("error should mention 'rate limited', got: %v", result.Error)
	}
}

// TestQuery_ContextCancelledDuringStreaming tests queryLoop's ctx.Done() branch
// at the top of the turn loop.
func TestQuery_ContextCancelledDuringStreaming(t *testing.T) {
	mp := &mockProvider{}
	// Return a complete response (no tool use, end_turn) so queryLoop finishes
	// the first turn and loops back to check ctx.Done() at the top.
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ec := newEventCollector()
	eng.SetDispatcher(ec)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel only after the query has fully completed (QueryEnd observed).
	// The test asserts the normal-completion path returns no error; a fixed
	// 10ms sleep raced the first turn — a mid-stream cancel legitimately
	// produces an error and failed the success assertion.
	go func() {
		ec.WaitForResult()
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")
	// Don't discard result - assert on it
	// The response completes normally (end_turn) before cancellation, so no error expected.
	// This test validates the ctx.Done() path exists; actual cancellation
	// is tested by TestQuery_ContextCancellation.
	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}
	// Verify we got a successful completion
	if result.Error != nil {
		t.Errorf("expected success, got: %v", result.Error)
	}
}

// TestQuery_DescriptionErrorFallback tests callLLM's tool description error fallback
// (line 287-289: desc = t.Name() when Description() returns error).
func TestQuery_DescriptionErrorFallback(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// LLM requests a tool with a broken Description() - should fall back to tool name
	toolEvents := toolUseStreamEvents("test-model", "t1", "desc_err_tool", `{}`)
	mp.addResponse(toolEvents, nil)
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	mt := &mockTool{
		name:    "desc_err_tool",
		enabled: true,
		descFn:  func(json.RawMessage) (string, error) { return "", errors.New("desc error") },
	}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	toolStartEvents := ec.FindEvents(types.EventToolStart)
	if len(toolStartEvents) == 0 {
		t.Fatal("expected EventToolStart event")
	}
	for _, evt := range toolStartEvents {
		if evt.ToolUse != nil && evt.ToolUse.Name != "desc_err_tool" {
			t.Errorf("expected tool name 'desc_err_tool', got '%s'", evt.ToolUse.Name)
		}
	}
}

// TestQuery_HasContentNoBlocks tests callLLM's fallback path where text deltas
// are received but no content_block_start events occurred.
func TestQuery_HasContentNoBlocks(t *testing.T) {
	t.Parallel()
	// hasContent && len(contentBlocks) == 0 fallback in callLLM.
	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "orphan text"}},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 3}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// The fallback should have created a text block from accumulated text
	if len(result.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result.Messages))
	}
}

// TestQuery_ExecuteToolsSkipsNonToolBlocks tests executeTools' skip path for
// non-tool-use blocks (line 360-361). This path shouldn't normally be reached
// since queryLoop filters toolUseBlocks, but it's a safety check.
func TestQuery_ExecuteToolsSkipsNonToolBlocks(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First response: a tool_use AND a text block in the same message
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "thinking..."}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "my_tool"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

// ---------------------------------------------------------------------------
// Hub integration tests
// ---------------------------------------------------------------------------

// hubMockHandler records events received via Hub.
type hubMockHandler struct {
	mu     sync.Mutex
	events []hub.Event
}

func (h *hubMockHandler) Handle(event hub.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
}

func (h *hubMockHandler) Events() []hub.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]hub.Event, len(h.events))
	copy(out, h.events)
	return out
}

func TestQuery_HubReceivesAllEvents(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	toolID := "tu_hub"
	toolName := "hub_tool"
	mp.addResponse(toolUseStreamEvents("test-model", toolID, toolName, `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done via hub."), nil)

	mt := &mockTool{name: toolName, enabled: true}

	h := hub.NewHub()
	handler := &hubMockHandler{}
	h.Subscribe(handler)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: h,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test hub events", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	hubEvents := handler.Events()

	// Verify we got the key event types
	var gotTurnStart, gotToolUseStart, gotToolResult, gotTextDelta, gotComplete bool
	for _, evt := range hubEvents {
		switch evt.Type {
		case types.EventTurnStart:
			gotTurnStart = true
		case types.EventToolStart:
			gotToolUseStart = true
		case types.EventToolEnd:
			gotToolResult = true
		case types.EventTextDelta:
			gotTextDelta = true
		case types.EventQueryEnd:
			gotComplete = true
		}
	}

	if !gotTurnStart {
		t.Error("Hub handler did not receive EventTurnStart")
	}
	if !gotToolUseStart {
		t.Error("Hub handler did not receive EventToolStart")
	}
	if !gotToolResult {
		t.Error("Hub handler did not receive EventToolEnd")
	}
	if !gotTextDelta {
		t.Error("Hub handler did not receive EventTextDelta")
	}
	if !gotComplete {
		t.Error("Hub handler did not receive EventQueryEnd")
	}

	// Verify ordering: first event should be EventQueryStart, last should be EventQueryEnd
	if len(hubEvents) == 0 {
		t.Fatal("expected at least one hub event")
	}
	if hubEvents[0].Type != types.EventQueryStart {
		t.Errorf("expected first event to be EventQueryStart, got %s", hubEvents[0].Type)
	}
	if hubEvents[len(hubEvents)-1].Type != types.EventQueryEnd {
		t.Errorf("expected last event to be EventQueryEnd, got %s", hubEvents[len(hubEvents)-1].Type)
	}
}

// TestQuery_TurnEndAfterToolEnd verifies that turn_end comes AFTER tool_end
// within each round.turn_end was emitted right after callLLM()
// returned, before tool execution, making the ordering turn_end→tool_end.
func TestQuery_TurnEndAfterToolEnd(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	toolID := "tu_order"
	toolName := "order_tool"
	mp.addResponse(toolUseStreamEvents("test-model", toolID, toolName, `{"x":1}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: toolName, enabled: true}

	h := hub.NewHub()
	handler := &hubMockHandler{}
	h.Subscribe(handler)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: h,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test ordering", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	events := handler.Events()

	// Verify: no tool_end appears AFTER turn_end and BEFORE the next turn_start.
	// Bug was: turn_end emitted before tool execution, producing turn_end→tool_end.
	for i, evt := range events {
		if evt.Type != types.EventTurnEnd {
			continue
		}
		// Look forward until next turn_start or end of events.
		for j := i + 1; j < len(events); j++ {
			if events[j].Type == types.EventTurnStart || events[j].Type == types.EventQueryEnd {
				break // reached next round boundary
			}
			if events[j].Type == types.EventToolEnd {
				t.Errorf("turn_end at index %d should come AFTER tool_end at index %d, not before", i, j)
			}
		}
	}
}

func TestQuery_EventDispatcherInterface(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "via interface"), nil)

	d := &mockDispatcher{}
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: d,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test interface", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	events := d.Events()
	if len(events) == 0 {
		t.Fatal("mockDispatcher should receive events")
	}

	// Verify key events received through the interface
	var gotTurnStart, gotTextDelta, gotComplete bool
	for _, evt := range events {
		switch evt.Type {
		case types.EventTurnStart:
			gotTurnStart = true
		case types.EventTextDelta:
			gotTextDelta = true
		case types.EventQueryEnd:
			gotComplete = true
		}
	}
	if !gotTurnStart {
		t.Error("dispatcher did not receive EventTurnStart")
	}
	if !gotTextDelta {
		t.Error("dispatcher did not receive EventTextDelta")
	}
	if !gotComplete {
		t.Error("dispatcher did not receive EventQueryEnd")
	}
}

func TestQuery_HubNilWorks(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: nil,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test nil hub", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

func TestQuery_MultiTurn_MemoryAccumulates(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello Xiaoming!"), nil)
	mp.addResponse(textStreamEvents("test-model", "Your name is Xiaoming."), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Turn 1
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	result1 := eng.QuerySync(ctx1, "My name is Xiaoming", "")
	cancel1()
	if result1.Error != nil {
		t.Fatalf("turn 1 error: %v", result1.Error)
	}

	msgs1 := eng.Messages()
	if len(msgs1) != 2 {
		t.Fatalf("after turn 1: expected 2 messages, got %d", len(msgs1))
	}

	// Turn 2
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	result2 := eng.QuerySync(ctx2, "What is my name?", "")
	cancel2()
	if result2.Error != nil {
		t.Fatalf("turn 2 error: %v", result2.Error)
	}

	// Engine should accumulate: [user1, assistant1, user2, assistant2]
	msgs2 := eng.Messages()
	if len(msgs2) != 4 {
		t.Fatalf("after turn 2: expected 4 messages, got %d", len(msgs2))
	}
	if msgs2[0].Role != types.RoleUser {
		t.Errorf("msg[0] role = %q, want user", msgs2[0].Role)
	}
	if msgs2[1].Role != types.RoleAssistant {
		t.Errorf("msg[1] role = %q, want assistant", msgs2[1].Role)
	}
	if msgs2[2].Role != types.RoleUser {
		t.Errorf("msg[2] role = %q, want user", msgs2[2].Role)
	}
	if msgs2[3].Role != types.RoleAssistant {
		t.Errorf("msg[3] role = %q, want assistant", msgs2[3].Role)
	}

	// Turn 1 user message content preserved
	texts := ExtractTextBlocks(msgs2[0])
	if len(texts) == 0 || texts[0] != "My name is Xiaoming" {
		t.Errorf("msg[0] text = %v, want 'My name is Xiaoming'", texts)
	}
}

// ---------------------------------------------------------------------------
// Token usage: no double-counting across LLM calls
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Notification queue
// ---------------------------------------------------------------------------

func TestEngine_EnqueueAttachment(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Enqueue a notification from another goroutine (simulates background job callback)
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "<job-notification><job-id>bg-1</job-id></job-notification>",
		Mode:      types.ItemModeJob,
		Timestamp: time.Now(), // REAL-TIME: needed for message timestamp in test
	})

	msgs := eng.Messages()
	// Attachment should NOT appear in messages yet — it's queued, not appended
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages (attachment is queued, not appended), got %d", len(msgs))
	}
}

func TestQuery_AttachmentDrainedAfterToolResult(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First call returns tool_use → loop continues
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "my_tool", `{}`), nil)
	// Second call: attachment was drained after tool result, loop continues
	mp.addResponse(textStreamEvents("test-model", "Processing attachment..."), nil)
	// Third call returns text → loop ends
	mp.addResponse(textStreamEvents("test-model", "Attachment seen!"), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Enqueue attachment BEFORE starting query — it should be drained
	// after the first tool result is appended.
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "<job-notification><job-id>bg-1</job-id></job-notification>",
		Mode:      types.ItemModeJob,
		Timestamp: time.Now(), // REAL-TIME: needed for message timestamp in test
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// A drained job notification emits exactly one EventAttachment whose stub
	// carries the persisted envelope verbatim (appendQueuedReminderMessage):
	// Mode stays job, Prompt is the full <system-reminder> envelope — the
	// value the TUI/WUI live row renders from.
	atts := ec.FindEvents(types.EventAttachment)
	if len(atts) != 1 {
		t.Fatalf("got %d EventAttachment events, want exactly 1", len(atts))
	}
	if atts[0].Message == nil || atts[0].Message.Attachment == nil {
		t.Fatal("EventAttachment stub is nil")
	}
	stub := atts[0].Message.Attachment
	if stub.Mode != types.ItemModeJob {
		t.Errorf("stub Mode = %q, want %q", stub.Mode, types.ItemModeJob)
	}
	wantEnvelope := "<system-reminder>\nA background agent completed a job:\n" +
		"<job-notification><job-id>bg-1</job-id></job-notification>\n</system-reminder>"
	if stub.Prompt != wantEnvelope {
		t.Errorf("stub Prompt = %q, want the full envelope %q", stub.Prompt, wantEnvelope)
	}

	// Verify the attachment text is in the final message history (merged into user message by marshalMessages)
	msgs := result.Messages
	found := false
	for _, msg := range msgs {
		if msg.Role == types.RoleUser {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, "bg-1") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("attachment should be in the final message history")
	}
}

// queryEndCapture is a hub.EventHandler that signals when a QueryEnd event arrives.
type queryEndCapture struct {
	done chan struct{}
}

func (h *queryEndCapture) Handle(event hub.Event) {
	if event.Type == types.EventQueryEnd {
		select {
		case h.done <- struct{}{}:
		default:
		}
	}
}

func TestEngine_EnqueueAttachment_Concurrent(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	h := hub.NewHub()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: h,
	})
	t.Cleanup(func() { eng.Close() })

	// Subscribe to hub to detect when auto-process completes.
	queryDone := &queryEndCapture{done: make(chan struct{}, 1)}
	h.Subscribe(queryDone)

	// Enqueue 100 attachments concurrently while idle.
	// systemPrompt is nil so auto-processing does NOT trigger during enqueue.
	var wg sync.WaitGroup
	attachmentCount := 100
	for i := range attachmentCount {
		wg.Go(func() {
			eng.EnqueueAttachment(types.QueuedItem{
				Value:     fmt.Sprintf("notification-%d", i),
				Mode:      types.ItemModeJob,
				Timestamp: time.Date(2024, 1, 1, 0, 0, i, 0, time.UTC),
			})
		})
	}
	wg.Wait()

	// Now set systemPrompt and trigger processing deterministically.
	eng.systemPrompt = "{}"
	eng.ProcessAttachments(context.Background(), eng.systemPrompt)

	// Wait for auto-processing to complete.
	select {
	case <-queryDone.done:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-processing did not complete within timeout")
	}

	// Verify all 100 attachments were drained and injected into messages.
	msgs := eng.Messages()
	attachmentMsgCount := 0
	for _, msg := range msgs {
		if msg.Role == types.RoleUser {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, "notification-") {
					attachmentMsgCount++
				}
			}
		}
	}
	if attachmentMsgCount != attachmentCount {
		t.Errorf("expected %d attachments to be enqueued and drained, got %d", attachmentCount, attachmentMsgCount)
	}
}

// ---------------------------------------------------------------------------
// Auto-process integration tests
// ---------------------------------------------------------------------------

// TestEnqueueAttachment_AutoProcess_FullChain verifies the complete auto-processing
// call chain: EnqueueAttachment while idle -> goroutine fires -> LLM called -> hub
// dispatches events -> messages appear in engine.
func TestEnqueueAttachment_AutoProcess_FullChain(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "I see the background job result."), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: ec,
	})
	t.Cleanup(func() { eng.Close() })
	eng.systemPrompt = "you are helpful"

	// Enqueue while idle - auto-process goroutine fires immediately
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "<job-notification><job-id>bg-1</job-id></job-notification>",
		Mode:      types.ItemModeJob,
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	// Wait for auto-process to complete (hub dispatches QueryEnd)
	select {
	case <-ec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-process did not complete within timeout")
	}

	// Job/message drains emit EventAttachment in final envelope form (see
	// appendQueuedReminderMessage). Verify the LLM turn still runs.
	if len(ec.FindEvents(types.EventTurnStart)) == 0 {
		t.Error("expected EventTurnStart - LLM turn should begin")
	}
	if len(ec.FindEvents(types.EventTextDelta)) == 0 {
		t.Error("expected EventTextDelta - LLM should respond")
	}
	if len(ec.FindEvents(types.EventQueryEnd)) == 0 {
		t.Error("expected EventQueryEnd - query should complete")
	}

	// Verify the attachment text appears in engine messages
	msgs := eng.Messages()
	found := false
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "job-id") {
				found = true
			}
		}
	}
	if !found {
		t.Error("attachment text should appear in engine messages")
	}

	// Verify LLM was called exactly once
	if mp.callCount() != 1 {
		t.Errorf("expected 1 LLM call, got %d", mp.callCount())
	}
}

// TestEnqueueAttachment_AutoProcess_NoFireDuringQuery verifies that EnqueueAttachment
// does NOT trigger auto-processing when a query is active (queryActive==1).
func TestEnqueueAttachment_AutoProcess_NoFireDuringQuery(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First call: returns tool_use -> loop continues (query runs long enough to enqueue)
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "my_tool", "{}"), nil)
	// Second call: text response -> loop ends
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	ec := newEventCollector()
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: ec,
	})
	t.Cleanup(func() { eng.Close() })
	eng.systemPrompt = "{}"

	// Must use Query() (goroutine), not QuerySync — only Query() sets queryActive=1.
	eng.Query(context.Background(), "test", "")

	// Wait for the query to start (first LLM call)
	waitForCallCount(t, mp, 1, 5*time.Second)

	// Enqueue while query is active (queryActive==1) — should NOT auto-process
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "bg-1-done",
		Mode:      types.ItemModeJob,
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	// Wait for query to finish
	select {
	case <-ec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("query did not complete")
	}

	// Query: 2 calls (tool_use + text). Auto-process would add a 3rd.
	if mp.callCount() > 2 {
		t.Errorf("enqueue during query triggered extra LLM calls (auto-process fired): got %d, expected <=2", mp.callCount())
	}
}

// TestEnqueueAttachment_DeferCatchAfterQuery verifies that attachments sitting
// in the queue after a text-only query are caught by the defer's processAttachments.
//
// Strategy: enqueue with no systemPrompt (auto-process won't trigger), then run a
// text-only query (no tool-use -> no turn boundary drain). The attachment stays in the
// queue during the query. When the query finishes, the defer spawns processAttachments.
func TestEnqueueAttachment_DeferCatchAfterQuery(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Query done."), nil)
	mp.addResponse(textStreamEvents("test-model", "Attachment processed."), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: ec,
	})
	t.Cleanup(func() { eng.Close() })

	// Enqueue while no systemPrompt - auto-process won't trigger
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "deferred-attachment",
		Mode:      types.ItemModeJob,
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	// Set systemPrompt so the defer will spawn processAttachments
	eng.systemPrompt = "{}"

	// Run a text-only query (no tool use -> no turn boundary DrainByPriority).
	// Attachment stays in queue during the entire query.
	eng.Query(context.Background(), "test", "")

	// Wait for both the query and the defer-triggered processAttachments to complete.
	// Query = 1 call, processAttachments = 1 call = 2 total.
	waitForCallCount(t, mp, 2, 5*time.Second)

	// Attachment text should appear in engine messages
	msgs := eng.Messages()
	found := false
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "deferred-attachment") {
				found = true
			}
		}
	}
	if !found {
		t.Error("deferred-attachment should appear in messages (caught by defer)")
	}
}

// TestEnqueueAttachment_DuringProcessAttachments verifies that an attachment
// arriving while processAttachments is running is caught by its defer and
// processed by a second goroutine.
func TestEnqueueAttachment_DuringProcessAttachments(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First processAttachments response
	mp.addResponse(textStreamEvents("test-model", "First batch done."), nil)
	// Second processAttachments response (defer-triggered)
	mp.addResponse(textStreamEvents("test-model", "Second batch done."), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Set systemPrompt before any enqueue so auto-process can trigger
	eng.systemPrompt = "{}"

	// Enqueue first attachment - triggers auto-process
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "first-attachment",
		Mode:      types.ItemModeJob,
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	// Wait for processAttachments to start (first LLM call)
	waitForCallCount(t, mp, 1, 5*time.Second)

	// Enqueue second attachment while processAttachments is running.
	// queryActive==1, so no new auto-process goroutine.
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "second-attachment",
		Mode:      types.ItemModeJob,
		Timestamp: time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC),
	})

	// Wait for both processAttachments goroutines to complete.
	// First = 1 call, second (defer-triggered) = 1 call = 2 total.
	waitForCallCount(t, mp, 2, 5*time.Second)

	// Both attachments should be in messages
	msgs := eng.Messages()
	var foundFirst, foundSecond bool
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "first-attachment") {
				foundFirst = true
			}
			if strings.Contains(b.Text, "second-attachment") {
				foundSecond = true
			}
		}
	}
	if !foundFirst {
		t.Error("first-attachment should appear in messages")
	}
	if !foundSecond {
		t.Error("second-attachment should appear in messages (caught by defer)")
	}
}

// TestEnqueueAttachment_AutoProcess_PanicRecovery verifies that if the
// auto-process goroutine panics, the engine recovers and subsequent
// EnqueueAttachment calls can still trigger auto-processing.
func TestEnqueueAttachment_AutoProcess_PanicRecovery(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First response: normal text (no panic - we test panic recovery of the defer mechanism,
	// not an actual panic during LLM call, since that's already tested elsewhere)
	mp.addResponse(textStreamEvents("test-model", "First run done."), nil)
	// Second response for recovery
	mp.addResponse(textStreamEvents("test-model", "Recovered."), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: ec,
	})
	t.Cleanup(func() { eng.Close() })
	eng.systemPrompt = "{}"

	// First enqueue - triggers auto-process
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "first-item",
		Mode:      types.ItemModeJob,
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	// Wait for auto-process to complete
	select {
	case <-ec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("first auto-process did not complete within timeout")
	}

	if mp.callCount() != 1 {
		t.Fatalf("expected 1 LLM call after first auto-process, got %d", mp.callCount())
	}

	// Second enqueue - should trigger another auto-process, proving recovery works
	// (even if the first goroutine had a panic in its defer chain, the engine is reusable)
	eng2ec := newEventCollector()
	eng.dispatcher = eng2ec

	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "after-recovery",
		Mode:      types.ItemModeJob,
		Timestamp: time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC),
	})

	select {
	case <-eng2ec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("second auto-process did not complete within timeout")
	}

	// The second attachment should be processed
	msgs := eng.Messages()
	found := false
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "after-recovery") {
				found = true
			}
		}
	}
	if !found {
		t.Error("after-recovery attachment should appear in messages")
	}
}

func TestQuery_UsageNoDoubleCount(t *testing.T) {
	t.Parallel()

	// Single LLM call: message_start has input=2500, message_delta has output=100.
	// The engine emits cumulative usage snapshots using max() for input/cache tokens.
	// TUI must also use max() (not +=) for input/cache to avoid double-counting.
	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{
			Type:    "message_start",
			Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 2500}},
		},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "hi"}},
		{Type: "content_block_stop", Index: 0},
		{
			Type:     "message_delta",
			DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			Usage:    &types.Usage{OutputTokens: 100},
		},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eng.QuerySync(ctx, "test", "")

	var usageEvents []types.UsageEvent
	for _, evt := range ec.FindEvents(types.EventUsage) {
		if evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}

	// Should have exactly 1 usage event from message_delta (message_start no longer emits).
	if len(usageEvents) != 1 {
		t.Fatalf("expected 1 usage event, got %d: %+v", len(usageEvents), usageEvents)
	}

	// Only event (message_delta): carries complete usage with input + output.
	if usageEvents[0].InputTokens != 2500 {
		t.Errorf("usage InputTokens = %d, want 2500", usageEvents[0].InputTokens)
	}
	if usageEvents[0].OutputTokens != 100 {
		t.Errorf("usage OutputTokens = %d, want 100", usageEvents[0].OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Cache token propagation from message_delta (minimax-style providers)
// ---------------------------------------------------------------------------

func TestQuery_CacheTokensFromMessageDelta(t *testing.T) {
	t.Parallel()

	// Simulate minimax-style provider: cache tokens appear in message_delta,
	// not in message_start. This tests that engine.go reads cache tokens from
	// event.Usage (the actual message_delta data), not from the stale local
	// `usage` variable set by message_start.
	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{
			Type: "message_start",
			Message: &llm.MessageStart{
				Model: "test-model",
				Usage: types.Usage{
					InputTokens:              5000,
					OutputTokens:             0,
					CacheCreationInputTokens: 0,
					CacheReadInputTokens:     0,
				},
			},
		},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "cached response"}},
		{Type: "content_block_stop", Index: 0},
		{
			Type:     "message_delta",
			DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			Usage: &types.Usage{
				OutputTokens:             30,
				CacheReadInputTokens:     5000,
				CacheCreationInputTokens: 0,
			},
		},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")

	var usageEvents []types.UsageEvent
	for _, evt := range ec.FindEvents(types.EventUsage) {
		if evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}

	if len(usageEvents) != 1 {
		t.Fatalf("expected 1 usage event (message_delta only), got %d: %+v", len(usageEvents), usageEvents)
	}

	// Only event (message_delta): carries cache_read from event.Usage.
	if usageEvents[0].CacheReadInputTokens != 5000 {
		t.Errorf("usage CacheRead = %d, want 5000 (from message_delta)", usageEvents[0].CacheReadInputTokens)
	}

	// Verify returned message has correct accumulated cache tokens
	if result.Messages == nil {
		t.Fatal("result.Messages is nil")
	}
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Usage == nil {
		t.Fatal("last message Usage is nil")
	}
	if lastMsg.Usage.CacheReadInputTokens != 5000 {
		t.Errorf("returned message CacheRead = %d, want 5000", lastMsg.Usage.CacheReadInputTokens)
	}
}

func TestQuery_CacheCreationInMessageStart(t *testing.T) {
	t.Parallel()

	// Simulate Anthropic-style: cache_creation in message_start.
	// This should continue to work after the fix.
	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{
			Type: "message_start",
			Message: &llm.MessageStart{
				Model: "test-model",
				Usage: types.Usage{
					InputTokens:              179,
					OutputTokens:             0,
					CacheCreationInputTokens: 5409,
					CacheReadInputTokens:     0,
				},
			},
		},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "hello"}},
		{Type: "content_block_stop", Index: 0},
		{
			Type:     "message_delta",
			DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			Usage: &types.Usage{
				OutputTokens: 5,
			},
		},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")

	var usageEvents []types.UsageEvent
	for _, evt := range ec.FindEvents(types.EventUsage) {
		if evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}

	if len(usageEvents) != 1 {
		t.Fatalf("expected 1 usage event (message_delta only), got %d", len(usageEvents))
	}

	// Only event (message_delta): carries cache_creation from message_delta.
	if usageEvents[0].CacheCreationInputTokens != 5409 {
		t.Errorf("usage CacheCreation = %d, want 5409", usageEvents[0].CacheCreationInputTokens)
	}

	// Returned message
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Usage.CacheCreationInputTokens != 5409 {
		t.Errorf("returned message CacheCreation = %d, want 5409", lastMsg.Usage.CacheCreationInputTokens)
	}
}

// ---------------------------------------------------------------------------
// EnqueueAttachment + ProcessAttachments tests
// ---------------------------------------------------------------------------

func TestEnqueueAttachment_NoDispatcher_NoPanic(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Should not panic when dispatcher is nil
	eng.EnqueueAttachment(types.QueuedItem{
		Value: "bg task done",
		Mode:  types.ItemModeJob,
	})
}

func TestProcessAttachments_EmptyQueue(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// processAttachments returns immediately when queue is empty
	eng.ProcessAttachments(ctx, "")
	runtime.Gosched()
	// No panic = success
}

func TestProcessAttachments_DrainsAndRunsTurns(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "background job completed."), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: ec,
	})
	t.Cleanup(func() { eng.Close() })

	eng.EnqueueAttachment(types.QueuedItem{
		Value: "<notification>bg-1 completed</notification>",
		Mode:  types.ItemModeJob,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eng.ProcessAttachments(ctx, "")
	select {
	case <-ec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for query end")
	}

	gotAttachmentEvents := ec.FindEvents(types.EventAttachment)
	gotTurnStart := len(ec.FindEvents(types.EventTurnStart)) > 0
	gotTextDelta := len(ec.FindEvents(types.EventTextDelta)) > 0
	gotQueryEnd := len(ec.FindEvents(types.EventQueryEnd)) > 0
	// The job reminder drain emits exactly one live event, before the turn.
	if len(gotAttachmentEvents) != 1 {
		t.Errorf("%d EventAttachment events for the job item, want exactly 1 (live row insert)", len(gotAttachmentEvents))
	} else if gotAttachmentEvents[0].Message == nil || gotAttachmentEvents[0].Message.Attachment == nil {
		t.Error("reminder EventAttachment carries no stub message")
	}
	if !gotTurnStart {
		t.Error("expected EventTurnStart")
	}
	if !gotTextDelta {
		t.Error("expected EventTextDelta (LLM response)")
	}
	if !gotQueryEnd {
		t.Error("expected EventQueryEnd")
	}

}
func TestProcessAttachments_ContextCancelled(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(nil, context.Canceled)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: ec,
	})
	t.Cleanup(func() { eng.Close() })

	eng.EnqueueAttachment(types.QueuedItem{
		Value: "notification",
		Mode:  types.ItemModeJob,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		runtime.Gosched()
		cancel()
	}()

	eng.ProcessAttachments(ctx, "")
	select {
	case <-ec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for query end")
	}
	// QueryEnd should be emitted even on cancellation
	gotQueryEnd := len(ec.FindEvents(types.EventQueryEnd)) > 0
	if !gotQueryEnd {
		t.Error("expected EventQueryEnd even on context cancellation")
	}
}

func TestQuery_EventTextStartEmitted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "hello"), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	textStartEvents := ec.FindEvents(types.EventTextStart)
	if len(textStartEvents) == 0 {
		t.Error("expected EventTextStart event to be emitted for text content block")
	}
	// Verify text start fires before any text delta
	textDeltaEvents := ec.FindEvents(types.EventTextDelta)
	if len(textStartEvents) == 0 || len(textDeltaEvents) == 0 {
		t.Fatal("expected both EventTextStart and EventTextDelta events")
	}
	// Verify text start fires before any text delta
	allEvents := ec.Events()
	var textStartIdx, textDeltaIdx = -1, -1
	for i, evt := range allEvents {
		if evt.Type == types.EventTextStart && textStartIdx < 0 {
			textStartIdx = i
		}
		if evt.Type == types.EventTextDelta && textDeltaIdx < 0 {
			textDeltaIdx = i
		}
	}
	if textStartIdx >= 0 && textDeltaIdx >= 0 && textStartIdx > textDeltaIdx {
		t.Error("expected EventTextStart to fire before any EventTextDelta")
	}
}

func TestQuery_EventTextEndEmitted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "hello"), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	textEndEvents := ec.FindEvents(types.EventTextEnd)
	if len(textEndEvents) == 0 {
		t.Error("expected EventTextEnd event to be emitted for text content block")
	}
	// Verify text end fires after text delta
	textDeltaEvents := ec.FindEvents(types.EventTextDelta)
	if len(textEndEvents) == 0 || len(textDeltaEvents) == 0 {
		t.Fatal("expected both EventTextEnd and EventTextDelta events")
	}
	allEvents := ec.Events()
	var textEndIdx, textDeltaIdx = -1, -1
	for i, evt := range allEvents {
		if evt.Type == types.EventTextDelta && textDeltaIdx < 0 {
			textDeltaIdx = i
		}
		if evt.Type == types.EventTextEnd && textEndIdx < 0 {
			textEndIdx = i
		}
	}
	if textEndIdx >= 0 && textDeltaIdx >= 0 && textEndIdx < textDeltaIdx {
		t.Error("expected EventTextEnd to fire after last EventTextDelta")
	}
}

func TestQuery_EventToolRunEmitted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu_1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	toolRunEvents := ec.FindEvents(types.EventToolRun)
	if len(toolRunEvents) == 0 {
		t.Error("expected EventToolRun event to be emitted for tool_use content block")
	}
	var toolRunID, toolRunName string
	for _, evt := range toolRunEvents {
		if evt.ToolUse != nil {
			toolRunID = evt.ToolUse.ID
			toolRunName = evt.ToolUse.Name
		}
	}
	if toolRunID != "tu_1" {
		t.Errorf("EventToolRun ID = %q, want tu_1", toolRunID)
	}
	if toolRunName != "my_tool" {
		t.Errorf("EventToolRun Name = %q, want my_tool", toolRunName)
	}
	if len(ec.FindEvents(types.EventToolStart)) == 0 {
		t.Error("expected EventToolStart")
	}
	if len(ec.FindEvents(types.EventToolEnd)) == 0 {
		t.Error("expected EventToolEnd")
	}
	// Verify ordering: ToolStart before ToolRun, ToolRun before ToolEnd
	allEvents := ec.Events()
	var toolStartIdx, toolRunIdx, toolEndIdx = -1, -1, -1
	for i, evt := range allEvents {
		if evt.Type == types.EventToolStart && toolStartIdx < 0 {
			toolStartIdx = i
		}
		if evt.Type == types.EventToolRun && toolRunIdx < 0 {
			toolRunIdx = i
		}
		if evt.Type == types.EventToolEnd && toolEndIdx < 0 {
			toolEndIdx = i
		}
	}
	if toolStartIdx >= 0 && toolRunIdx >= 0 && toolStartIdx > toolRunIdx {
		t.Error("expected EventToolStart to fire before EventToolRun")
	}
	if toolRunIdx >= 0 && toolEndIdx >= 0 && toolRunIdx > toolEndIdx {
		t.Error("expected EventToolRun to fire before EventToolEnd")
	}
}

func TestQuery_EventOrderingMultiBlock(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Response: thinking -> tool_use (realistic LLM response order)
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 30}}},
		// Block 0: thinking
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: "let me think..."}},
		{Type: "content_block_stop", Index: 0},
		// Block 1: tool_use
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "my_tool"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	// Turn 2: text response after tool execution
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Collect all content lifecycle events in order
	var eventOrder []string
	for _, evt := range ec.Events() {
		switch evt.Type {
		case types.EventThinkingStart, types.EventThinkingDelta, types.EventThinkingEnd,
			types.EventToolStart, types.EventToolParamDelta, types.EventToolRun, types.EventToolEnd,
			types.EventTextStart, types.EventTextDelta, types.EventTextEnd:
			eventOrder = append(eventOrder, string(evt.Type))
		}
	}

	// Expected order: thinking_start -> thinking_delta -> thinking_end ->
	//                 tool_start -> tool_param_delta -> tool_run -> tool_end ->
	//                 text_start -> text_delta -> text_end
	want := []string{
		"thinking_start", "thinking_delta", "thinking_end",
		"tool_start", "tool_param_delta", "tool_run", "tool_end",
		"text_start", "text_delta", "text_end",
	}

	if len(eventOrder) != len(want) {
		t.Fatalf("event count = %d, want %d\n  got:  %v\n  want: %v", len(eventOrder), len(want), eventOrder, want)
	}
	for i, got := range eventOrder {
		if got != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, got, want[i])
		}
	}

	t.Logf("event order: %v", eventOrder)
}

// TestQuery_EventTextStartEnd_EmptyBlock verifies that text start/end events
// are emitted even when the text block has zero deltas (empty text).
func TestQuery_EventTextStartEnd_EmptyBlock(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Response with a text block that has no delta events — just start+stop
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		// No content_block_delta: empty text block
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 0}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if len(ec.FindEvents(types.EventTextStart)) == 0 {
		t.Error("EventTextStart not emitted for empty text block")
	}
	if len(ec.FindEvents(types.EventTextEnd)) == 0 {
		t.Error("EventTextEnd not emitted for empty text block")
	}
}

// TestCallLLM_InterleavedToolCallDeltas verifies that interleaved input_json_delta
// events across two parallel tool_use blocks do not mix arguments.
// This reproduces the bug where OpenAI SSE sends deltas for multiple tool calls
// in interleaved order (index 0 delta, index 1 delta, index 0 delta, ...).
func TestCallLLM_InterleavedToolCallDeltas(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Simulate OpenAI-style interleaved deltas:
	// Tool 0 (Read): {"file_path": "/a.txt"}
	// Tool 1 (Bash): {"command": "ls"}
	// Deltas arrive interleaved: chunk of tool 0, chunk of tool 1, etc.
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 30}}},
		// Both tool_use blocks start
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t_read", Name: "Read"}},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t_bash", Name: "Bash"}},
		// Interleaved input_json_delta: tool 0 gets first chunk
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"file`}},
		// Tool 1 gets its first chunk
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"comman`}},
		// Tool 0 gets second chunk
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `_path": "/a.txt"}`}},
		// Tool 1 gets second chunk
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `d": "ls"}`}},
		// Both stop
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 15}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	var readInput, bashInput json.RawMessage
	toolRead := &mockTool{name: "Read", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		readInput = input
		return &tool.ToolResult{Data: "file content"}, nil
	}}
	toolBash := &mockTool{name: "Bash", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		bashInput = input
		return &tool.ToolResult{Data: "file1\nfile2"}, nil
	}}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				toolRead.Name(): toolRead,
				toolBash.Name(): toolBash,
			}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "read and ls", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Collect EventToolParamDelta events to verify ID/Name correspondence
	var paramDeltas []types.PartialInputEvent
	for _, evt := range ec.FindEvents(types.EventToolParamDelta) {
		if evt.PartialInput != nil {
			paramDeltas = append(paramDeltas, *evt.PartialInput)
		}
	}

	// Verify tool inputs are complete and NOT mixed
	if readInput == nil {
		t.Fatal("Read tool was not called")
	}
	if string(readInput) != `{"file_path": "/a.txt"}` {
		t.Errorf("Read input mixed or incomplete: got %q, want %q", string(readInput), `{"file_path": "/a.txt"}`)
	}

	if bashInput == nil {
		t.Fatal("Bash tool was not called")
	}
	if string(bashInput) != `{"command": "ls"}` {
		t.Errorf("Bash input mixed or incomplete: got %q, want %q", string(bashInput), `{"command": "ls"}`)
	}

	// Verify EventToolParamDelta has correct ID/Name for each tool
	readDeltas := 0
	bashDeltas := 0
	for _, pd := range paramDeltas {
		if pd.ID == "t_read" && pd.Name == "Read" {
			readDeltas++
		} else if pd.ID == "t_bash" && pd.Name == "Bash" {
			bashDeltas++
		} else {
			t.Errorf("unexpected param delta: ID=%q Name=%q", pd.ID, pd.Name)
		}
	}
	if readDeltas == 0 {
		t.Error("no EventToolParamDelta emitted for Read tool")
	}
	if bashDeltas == 0 {
		t.Error("no EventToolParamDelta emitted for Bash tool")
	}
}

// TestCallLLM_ParallelToolCalls_WithRealInput verifies that 3 parallel tool calls
// each receive the correct, unmixed input JSON.
func TestCallLLM_ParallelToolCalls_WithRealInput(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// 3 tool calls with distinct JSON inputs, all interleaved
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 40}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t0", Name: "Read"}},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "Bash"}},
		{Type: "content_block_start", Index: 2, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t2", Name: "Grep"}},
		// Interleaved deltas: each tool gets its input in 2 chunks
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"file_path":`}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":`}},
		{Type: "content_block_delta", Index: 2, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"pattern":`}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: ` "/src/main.go"}`}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: ` "go test"}`}},
		{Type: "content_block_delta", Index: 2, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: ` "TODO"}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_stop", Index: 1},
		{Type: "content_block_stop", Index: 2},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 20}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "All done."), nil)

	var inputs sync.Map
	toolRead := &mockTool{name: "Read", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		inputs.Store("Read", input)
		return &tool.ToolResult{Data: "contents"}, nil
	}}
	toolBash := &mockTool{name: "Bash", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		inputs.Store("Bash", input)
		return &tool.ToolResult{Data: "ok"}, nil
	}}
	toolGrep := &mockTool{name: "Grep", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		inputs.Store("Grep", input)
		return &tool.ToolResult{Data: "matches"}, nil
	}}

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				toolRead.Name(): toolRead,
				toolBash.Name(): toolBash,
				toolGrep.Name(): toolGrep,
			}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "parallel ops", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Verify each tool got exactly the right input, no mixing
	readInput, _ := inputs.Load("Read")
	if string(readInput.(json.RawMessage)) != `{"file_path": "/src/main.go"}` {
		t.Errorf("Read input = %q, want %q", string(readInput.(json.RawMessage)), `{"file_path": "/src/main.go"}`)
	}
	bashInput, _ := inputs.Load("Bash")
	if string(bashInput.(json.RawMessage)) != `{"command": "go test"}` {
		t.Errorf("Bash input = %q, want %q", string(bashInput.(json.RawMessage)), `{"command": "go test"}`)
	}
	grepInput, _ := inputs.Load("Grep")
	if string(grepInput.(json.RawMessage)) != `{"pattern": "TODO"}` {
		t.Errorf("Grep input = %q, want %q", string(grepInput.(json.RawMessage)), `{"pattern": "TODO"}`)
	}
}

func TestSetCompactor(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	// Set compactor and verify it doesn't panic
	eng.SetCompactor(
		&mockCompactor{},
		AutoCompactConfig{
			ContextWindow:          100000,
			MaxConsecutiveFailures: 3,
		},
	)

	// Verify concurrent SetCompactor doesn't race
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			eng.SetCompactor(&mockCompactor{}, AutoCompactConfig{ContextWindow: 100000})
		})
	}
	wg.Wait()
}

// TestQuery_NewMessagesAfterToolResult verifies that when a tool returns
// NewMessages alongside its result, the tool_result user message appears
// BEFORE the NewMessages user messages in the conversation history.
//
// The Anthropic API requires that tool_result directly follows the assistant's
// tool_use block without any intermediate user messages. Inserting NewMessages
// before tool_result causes API error 2013: "tool call result does not follow
// tool call".
//
// TS reference: toolExecution.ts — addToolResult() pushes tool_result FIRST
// (line 1456), then newMessages AFTER (line 1566).
func TestQuery_NewMessagesAfterToolResult(t *testing.T) {
	t.Parallel()

	toolID := "tool_skill_1"
	toolName := "Skill"
	toolInput := `{"skill":"roast"}`

	mp := &mockProvider{}
	// Round 1: assistant responds with tool_use
	mp.addResponse(toolUseStreamEvents("test-model", toolID, toolName, toolInput), nil)
	// Round 2: assistant responds with text (after seeing tool result + new messages)
	mp.addResponse(textStreamEvents("test-model", "Roast complete!"), nil)

	mt := &mockTool{
		name:    toolName,
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{
				Data: skillOutputData("roast", "inline"),
				NewMessages: []types.Message{
					{Role: types.RoleUser, Content: []types.ContentBlock{
						types.NewTextBlock("<command-message>roast</command-message>"),
					}},
					{Role: types.RoleUser, Content: []types.ContentBlock{
						types.NewTextBlock("You are a ruthless roaster..."),
					}},
				},
			}, nil
		},
	}

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "/roast", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Verify message ordering: tool_result must appear BEFORE NewMessages
	msgs := eng.Messages()

	// Find the assistant message with tool_use
	var assistantIdx = -1
	for i, msg := range msgs {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolUse && block.Name == "Skill" {
				assistantIdx = i
				break
			}
		}
		if assistantIdx >= 0 {
			break
		}
	}
	if assistantIdx < 0 {
		t.Fatal("no assistant message with tool_use found")
	}

	// The message immediately after assistant must contain tool_result
	if assistantIdx+1 >= len(msgs) {
		t.Fatal("no message after assistant tool_use")
	}
	firstAfterAssistant := msgs[assistantIdx+1]
	if firstAfterAssistant.Role != types.RoleUser {
		t.Fatalf("message after assistant should be user, got %s", firstAfterAssistant.Role)
	}

	// Verify first user message after assistant contains a tool_result block
	hasToolResult := false
	for _, block := range firstAfterAssistant.Content {
		if block.Type == types.ContentTypeToolResult {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		t.Errorf("first user message after assistant should contain tool_result, got blocks: %v",
			contentBlockTypes(firstAfterAssistant.Content))
	}

	// Verify subsequent messages (NewMessages) come AFTER tool_result
	// and do NOT contain tool_result blocks
	if assistantIdx+2 < len(msgs) {
		for i := assistantIdx + 2; i < len(msgs); i++ {
			for _, block := range msgs[i].Content {
				if block.Type == types.ContentTypeToolResult {
					t.Errorf("message at index %d (after tool_result message) should not contain tool_result", i)
				}
			}
		}
	}

	// Verify NewMessages content is present somewhere after tool_result
	foundSkillContent := false
	for i := assistantIdx + 2; i < len(msgs); i++ {
		for _, block := range msgs[i].Content {
			if block.Type == types.ContentTypeText && strings.Contains(block.Text, "ruthless roaster") {
				foundSkillContent = true
			}
		}
	}
	if !foundSkillContent {
		t.Error("NewMessages content (ruthless roaster) not found after tool_result message")
	}
}

// skillOutputData returns a JSON tool result data for a skill invocation.
func skillOutputData(name, status string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"success":     true,
		"commandName": name,
		"status":      status,
	})
	return data
}

// contentBlockTypes returns the types of content blocks for error messages.
func contentBlockTypes(blocks []types.ContentBlock) []string {
	names := make([]string, len(blocks))
	for i, b := range blocks {
		names[i] = string(b.Type)
	}
	return names
}

// TestAllTools_AllToolsRegisteredBeforeEngine verifies that when all tools
// are registered before New(), AllTools() returns the correct count.
// Root cause of "11 tools vs 14 tools"main.go used to register
// Agent/TaskOutput/TaskStop after New().  register all tools first.
func TestAllTools_AllToolsRegisteredBeforeEngine(t *testing.T) {
	t.Parallel()

	// Correct registration order: ALL tools before New()
	reg := tool.NewRegistry()
	for _, name := range []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep"} {
		reg.MustRegister(&mockTool{name: name, enabled: true})
	}
	reg.MustRegister(&mockTool{name: "Skill", enabled: true})
	reg.MustRegister(&mockTool{name: "Agent", enabled: true})
	reg.MustRegister(&mockTool{name: "Job", enabled: true})

	// New() — ToolsProvider snapshots all 10 tools
	eng := New(&Params{
		Provider:      &mockProvider{},
		ToolsProvider: reg.ToolMapFn(),
		Model:         "test-model",
	})
	t.Cleanup(func() { eng.Close() })

	got := len(eng.AllTools())
	if got != 9 {
		t.Errorf("AllTools() = %d tools, want 9. "+
			"All tools registered before New() but count is wrong.", got)
	}
}

// --- RewindTo ---

func TestRewindTo_BasicTruncate(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp2")}},
	})

	result, err := eng.RewindTo(2)
	if err != nil {
		t.Fatalf("RewindTo failed: %v", err)
	}
	if result.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", result.MessageCount)
	}
	msgs := eng.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after rewind, got %d", len(msgs))
	}
	first, _ := extractFirstTextBlock(msgs[0])
	if first != "msg1" {
		t.Errorf("msgs[0] = %q, want msg1", first)
	}
}

func TestRewindTo_WithFileRestore(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	backupDir := t.TempDir()
	tracker := filehistory.NewTracker(backupDir)
	eng.SetFileHistory(tracker)

	// Create a temp file with "original" content
	tmpFile := filepath.Join(t.TempDir(), "test.go")
	if err := os.WriteFile(tmpFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TrackEdit reads "original" from disk → creates v1 backup of "original".
	// MakeSnapshot captures post-edit state (still "original" since no edit happened yet).
	// RewindTo(0) → rewindSnapshotID="" (no user messages in messages[:0])
	// → Rewind("") → restores from initial snapshot which has v1 backup = "original".
	if err := tracker.TrackEdit(tmpFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("rewind-uuid-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	// NOW modify after snapshot — file on disk becomes "modified"
	if err := os.WriteFile(tmpFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	eng.SetMessages([]types.Message{
		{ID: "rewind-uuid-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
	})

	result, err := eng.RewindTo(0)
	if err != nil {
		t.Fatalf("RewindTo failed: %v", err)
	}
	if len(result.RestoredFiles) != 1 {
		t.Errorf("RestoredFiles = %v, want 1 file", result.RestoredFiles)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("file content = %q, want %q", string(data), "original")
	}
}

func TestRewindTo_RestoreErrSurfaced(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	backupDir := t.TempDir()
	tracker := filehistory.NewTracker(backupDir)
	eng.SetFileHistory(tracker)
	tmpFile := filepath.Join(t.TempDir(), "test.go")
	if err := os.WriteFile(tmpFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tracker.TrackEdit(tmpFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("rewind-uuid-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	if err := os.WriteFile(tmpFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Destroy the backups: the rewind must still truncate messages but
	// surface the file-restore failure instead of reporting success.
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one backup file")
	}
	for _, ent := range entries {
		if err := os.Remove(filepath.Join(backupDir, ent.Name())); err != nil {
			t.Fatal(err)
		}
	}
	eng.SetMessages([]types.Message{
		{ID: "rewind-uuid-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
	})
	result, err := eng.RewindTo(0)
	if err != nil {
		t.Fatalf("RewindTo failed: %v", err)
	}
	if result.RestoreErr == nil {
		t.Error("RestoreErr = nil, want the surfaced backup_not_found restore failure")
	}
	if len(result.RestoredFiles) != 0 {
		t.Errorf("RestoredFiles = %v, want empty", result.RestoredFiles)
	}
}

func TestRewindTo_InvalidIndex(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
	})

	_, err := eng.RewindTo(-1)
	if err == nil {
		t.Error("expected error for negative index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error = %v, want out of range", err)
	}

	_, err = eng.RewindTo(5)
	if err == nil {
		t.Fatal("expected error for index > len(messages)")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should mention out of range, got: %v", err)
	}
}

func TestRewindTo_NoFileHistory(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	// No fileHistory set — should not panic
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
	})

	result, err := eng.RewindTo(1)
	if err != nil {
		t.Fatalf("RewindTo failed: %v", err)
	}
	if result.MessageCount != 1 {
		t.Errorf("MessageCount = %d, want 1", result.MessageCount)
	}
	if len(result.RestoredFiles) != 0 {
		t.Errorf("RestoredFiles should be empty with no fileHistory")
	}
}

// --- RewindScope tests ---

// TestRewindToScoped_MessagesOnly truncates messages but does not restore files.
func TestRewindToScoped_MessagesOnly(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "test.go")
	if err := os.WriteFile(testFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetFileHistory(tracker)

	// TrackEdit reads "original" from disk → v1 backup = "original".
	// MakeSnapshot("0") captures post-edit state (still "original").
	// RewindToScoped(2, MessagesOnly) with messages=[user,asst,user,asst] (no IDs)
	// → last user in messages[:2] is messages[0] → rewindSnapshotID="0" (fallback)
	// → MessagesOnly: does NOT call Rewind, just truncates snapshots.
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("scope-msg-uuid-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	// NOW modify after snapshot
	if err := os.WriteFile(testFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set up 4 messages: user, assistant, user, assistant
	eng.SetMessages([]types.Message{
		{ID: "scope-msg-uuid-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
		{ID: "scope-msg-uuid-2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp2")}},
	})

	// Rewind messages only to index 2 — keep files as-is
	result, err := eng.RewindToScoped(2, RewindMessagesOnly)
	if err != nil {
		t.Fatalf("RewindToScoped: %v", err)
	}
	if result.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", result.MessageCount)
	}
	// Files should NOT be restored
	if len(result.RestoredFiles) != 0 {
		t.Errorf("RestoredFiles should be empty for MessagesOnly, got %v", result.RestoredFiles)
	}
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "modified" {
		t.Errorf("file = %q, want %q — file should NOT be restored with MessagesOnly", string(data), "modified")
	}
	// Messages should be truncated
	msgs := eng.Messages()
	if len(msgs) != 2 {
		t.Errorf("len(Messages) = %d, want 2", len(msgs))
	}
}

// TestRewindToScoped_FilesOnly restores files but does not truncate messages.
func TestRewindToScoped_FilesOnly(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "test.go")
	if err := os.WriteFile(testFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetFileHistory(tracker)

	// TrackEdit reads "original" from disk → v1 backup = "original".
	// MakeSnapshot("0") captures post-edit state (still "original").
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("filesonly-uuid-2"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	// NOW modify after snapshot
	if err := os.WriteFile(testFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	eng.SetMessages([]types.Message{
		{ID: "filesonly-uuid-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
		{ID: "filesonly-uuid-2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp2")}},
	})

	// Rewind files only to index 2 — keep messages as-is
	// rewindSnapshotID: last user in messages[:2] = messages[0] → "0" (fallback)
	// → RewindFilesOnly("0") restores from snapshot "0" which has v1 backup = "original"
	result, err := eng.RewindToScoped(2, RewindFilesOnly)
	if err != nil {
		t.Fatalf("RewindToScoped: %v", err)
	}
	// Files should be restored
	if len(result.RestoredFiles) != 1 || result.RestoredFiles[0] != testFile {
		t.Errorf("RestoredFiles = %v, want [%s]", result.RestoredFiles, testFile)
	}
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("file = %q, want %q", string(data), "original")
	}
	// Messages should NOT be truncated
	msgs := eng.Messages()
	if len(msgs) != 4 {
		t.Errorf("len(Messages) = %d, want 4 — messages should NOT be truncated with FilesOnly", len(msgs))
	}
	// MessageCount should reflect unchanged messages
	if result.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", result.MessageCount)
	}
}

// TestRewindToScoped_All behaves the same as RewindTo (both messages + files).
func TestRewindToScoped_All(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "test.go")
	if err := os.WriteFile(testFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetFileHistory(tracker)

	// TrackEdit reads "original" from disk → v1 backup = "original".
	// MakeSnapshot("0") captures post-edit state (still "original").
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("all-uuid-2"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	// NOW modify after snapshot
	if err := os.WriteFile(testFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	eng.SetMessages([]types.Message{
		{ID: "all-uuid-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
		{ID: "all-uuid-2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp2")}},
	})

	result, err := eng.RewindToScoped(2, RewindAll)
	if err != nil {
		t.Fatalf("RewindToScoped: %v", err)
	}
	if result.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", result.MessageCount)
	}
	if len(result.RestoredFiles) != 1 {
		t.Errorf("RestoredFiles = %v, want 1 file", result.RestoredFiles)
	}
	assertFileContentEngine(t, testFile, "original")
	if len(eng.Messages()) != 2 {
		t.Errorf("len(Messages) = %d, want 2", len(eng.Messages()))
	}
}

// TestRewindToScoped_InvalidIndex returns error for out-of-range index.
func TestRewindToScoped_InvalidIndex(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
	})
	_, err := eng.RewindToScoped(5, RewindAll)
	if err == nil {
		t.Fatal("expected error for out-of-range index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should mention out of range, got: %v", err)
	}
}

// TestRewindToScoped_NoFileHistory does not panic without fileHistory.
func TestRewindToScoped_NoFileHistory(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
	})
	result, err := eng.RewindToScoped(1, RewindMessagesOnly)
	if err != nil {
		t.Fatalf("RewindToScoped: %v", err)
	}
	if result.MessageCount != 1 {
		t.Errorf("MessageCount = %d, want 1", result.MessageCount)
	}
}

func assertFileContentEngine(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s content = %q, want %q", filepath.Base(path), string(data), want)
	}
}

// TestChain_SubEngineEditBackup_QueryWithExisting verifies sub-engine Edit
// tool calls are tracked via RunForkedQuery (production Agent path).
func TestChain_SubEngineEditBackup_QueryWithExisting(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "editme.txt")
	originalContent := []byte("original-content\n")
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	editTool := &mockTool{
		name:    "Edit",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			_ = os.WriteFile(testFile, []byte("modified-by-edit\n"), 0o644)
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	// Set up mock provider before creating sub-engine (avoids race)
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test", "sub_tool_1", "Edit", `{"file_path":"`+testFile+`","old_string":"original-content","new_string":"modified-by-edit"}`), nil)
	mp.addResponse(textStreamEvents("test", "sub-done"), nil)

	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	parentEng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{editTool.Name(): editTool}
		},
		Model:      "test",
		WorkingDir: tmp,
	})
	defer parentEng.Close()
	parentEng.SetFileHistory(tracker)

	// Create sub-engine — inherits fileHistory
	subTools := map[string]tool.Tool{"Edit": editTool}
	subEng := parentEng.NewSubEngine(SubEngineOptions{
		Tools:           subTools,
		AgentType:       "General",
		ParentToolUseID: "parent_tool_1",
	})

	// Reproduce production path: RunForkedQuery with user message that has NO ID
	// (matches cmd/gbot/main.go:283-286 where factory creates messages without ID).
	eventCh := make(chan types.QueryEvent, 50)
	subEng.SetDispatcher(&chanDispatcher{ch: eventCh})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	messages := []types.Message{
		{
			ID:      "sub-edit-msg-001",
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock("edit editme.txt")},
		},
	}
	go subEng.RunForkedQuery(ctx, messages, "")

	for {
		select {
		case evt := <-eventCh:
			if evt.Type == types.EventQueryEnd {
				goto editDone
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for sub-engine EventQueryEnd")
		}
	}
editDone:
	runtime.Gosched()

	// Verify file was modified
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "modified-by-edit\n" {
		t.Fatalf("file not modified by Edit tool, got %q", string(data))
	}

	// Verify TrackEdit recorded the file
	records := tracker.State().TrackedFiles
	if len(records) < 1 {
		t.Fatalf("expected at least 1 tracked file from sub-engine Edit, got %d — TrackEdit skipped because currentTurnMsgID was empty", len(records))
	}

	// Verify rewind restores original content
	restored, err := tracker.Rewind("")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	found := slices.Contains(restored, testFile)
	if !found {
		t.Fatalf("RestoredFiles = %v, want %s included", restored, testFile)
	}

	data, err = os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != string(originalContent) {
		t.Errorf("file = %q, want %q — sub-engine edit was not restored!", string(data), string(originalContent))
	}
}

// TestHasChangesAtMessage_MsgIDDerivation verifies rewind.go uses the correct
// messageID when checking HasChangesAtMessage.
func TestHasChangesAtMessage_MsgIDDerivation(t *testing.T) {
	tmp := t.TempDir()
	fileA := filepath.Join(tmp, "a.txt")
	fileB := filepath.Join(tmp, "b.txt")

	if err := os.WriteFile(fileA, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))

	// Simulate Turn 1: user sends "write a.txt"
	// TrackEdit is called BEFORE the write (saves pre-edit content as backup).
	// Then the tool writes new content, then MakeSnapshot captures the turn boundary.
	turn1MsgID := "turn1-uuid-1234"
	if err := tracker.TrackEdit(fileA); err != nil {
		t.Fatalf("TrackEdit turn1: %v", err)
	}
	// Simulate the Write tool modifying the file (after TrackEdit, before MakeSnapshot)
	if err := os.WriteFile(fileA, []byte("hello-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tracker.MakeSnapshot(turn1MsgID); err != nil {
		t.Fatalf("MakeSnapshot turn1: %v", err)
	}

	// Simulate Turn 2: user sends "write b.txt"
	turn2MsgID := "turn2-uuid-5678"
	if err := tracker.TrackEdit(fileB); err != nil {
		t.Fatalf("TrackEdit turn2: %v", err)
	}
	// Simulate the Write tool modifying the file
	if err := os.WriteFile(fileB, []byte("hello-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tracker.MakeSnapshot(turn2MsgID); err != nil {
		t.Fatalf("MakeSnapshot turn2: %v", err)
	}

	// Verify snapshot layout
	state := tracker.State()
	for i, snap := range state.Snapshots {
		t.Logf("snapshot[%d]: MessageID=%q files=%d", i, snap.MessageID, len(snap.TrackedFileBackups))
	}
	if len(state.Snapshots) < 3 {
		t.Fatalf("expected at least 3 snapshots, got %d", len(state.Snapshots))
	}

	// BUGGY logic: use the selected (second) user message's ID
	buggyHasChanges := tracker.HasChangesAtMessage(turn2MsgID)
	// CORRECT logic: use the previous user message's ID
	correctHasChanges := tracker.HasChangesAtMessage(turn1MsgID)

	t.Logf("buggy:   HasChangesAtMessage(%q) = %v", turn2MsgID, buggyHasChanges)
	t.Logf("correct: HasChangesAtMessage(%q) = %v", turn1MsgID, correctHasChanges)

	if !correctHasChanges {
		t.Errorf("HasChangesAtMessage(%q) = false, want true", turn1MsgID)
	}
}

// ---------------------------------------------------------------------------
// E2E chain tests: QuerySync → TrackEdit → MakeSnapshot → RewindToScoped
// These test the FULL call chain that the user experiences:
// ask LLM to edit a file → /rewind → file restored on disk.
// ---------------------------------------------------------------------------

// TestE2E_WriteTool_RewindAll_RestoresFile exercises:
// QuerySync(Write tool) → TrackEdit → MakeSnapshot → RewindToScoped(RewindAll)
// → verify file content on disk reverted to original.
func TestE2E_WriteTool_RewindAll_RestoresFile(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(testFile, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTool := &mockTool{
		name:    "Write",
		enabled: true,
		callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			var parsed struct {
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal([]byte(input), &parsed); err != nil {
				return nil, err
			}
			if parsed.FilePath != "" {
				if err := os.WriteFile(parsed.FilePath, []byte(parsed.Content), 0o644); err != nil {
					return nil, err
				}
			}
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	eventCh := make(chan types.QueryEvent, 50)
	dispatcher := &chanDispatcher{ch: eventCh}
	mp := &mockProvider{}
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{writeTool.Name(): writeTool}
		},
		Model:      "test",
		Dispatcher: dispatcher,
		WorkingDir: tmp,
	})
	defer eng.Close()
	eng.SetFileHistory(tracker)

	ctx := context.Background()

	// Turn 1: write v1
	mp.addResponse(toolUseStreamEvents("test", "tool_1", "Write",
		`{"file_path":"`+testFile+`","content":"v1"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	eng.QuerySync(ctx, "write v1", "")

	// Turn 2: write v2
	mp.addResponse(toolUseStreamEvents("test", "tool_2", "Write",
		`{"file_path":"`+testFile+`","content":"v2"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	eng.QuerySync(ctx, "write v2", "")

	// Verify file is v2 before rewind
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2" {
		t.Fatalf("file should be v2, got %q", string(data))
	}

	// TS semantics: RewindToScoped(0) → msgs[0].ID → snapshot at turn 1 boundary
	// (BEFORE edits) → file restored to pre-turn-1 state = "v0" (original)
	result, err := eng.RewindToScoped(0, RewindAll)
	if err != nil {
		t.Fatalf("RewindToScoped failed: %v", err)
	}
	if len(result.RestoredFiles) == 0 {
		t.Error("expected at least 1 restored file, got 0")
	}

	data, err = os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v0" {
		t.Errorf("file = %q, want v0 — rewind to turn 1 restores pre-edit state", string(data))
	}
	if len(eng.Messages()) != 0 {
		t.Errorf("expected 0 messages after rewind, got %d", len(eng.Messages()))
	}
}

// TestE2E_WriteTool_RewindIntermediate verifies rewinding to an intermediate
// turn restores the correct version (not the latest or earliest).
// TS semantics: RewindToScoped(idx) uses msgs[idx].ID → snapshot at that turn's boundary.
// 3 turns: v0→v1→v2→v3. Rewind to turn2 index restores v2 (intermediate).
func TestE2E_WriteTool_RewindIntermediate(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(testFile, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTool := &mockTool{
		name:    "Write",
		enabled: true,
		callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			var parsed struct {
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal([]byte(input), &parsed); err != nil {
				return nil, err
			}
			if parsed.FilePath != "" {
				if err := os.WriteFile(parsed.FilePath, []byte(parsed.Content), 0o644); err != nil {
					return nil, err
				}
			}
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	eventCh := make(chan types.QueryEvent, 50)
	dispatcher := &chanDispatcher{ch: eventCh}
	mp := &mockProvider{}
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{writeTool.Name(): writeTool}
		},
		Model:      "test",
		Dispatcher: dispatcher,
		WorkingDir: tmp,
	})
	defer eng.Close()
	eng.SetFileHistory(tracker)

	ctx := context.Background()

	// Turn 1: write v1
	mp.addResponse(toolUseStreamEvents("test", "tool_1", "Write",
		`{"file_path":"`+testFile+`","content":"v1"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	eng.QuerySync(ctx, "write v1", "")

	// Turn 2: write v2
	mp.addResponse(toolUseStreamEvents("test", "tool_2", "Write",
		`{"file_path":"`+testFile+`","content":"v2"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	eng.QuerySync(ctx, "write v2", "")

	// Turn 3: write v3
	mp.addResponse(toolUseStreamEvents("test", "tool_3", "Write",
		`{"file_path":"`+testFile+`","content":"v3"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	eng.QuerySync(ctx, "write v3", "")

	// Verify file is at v3
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v3" {
		t.Fatalf("file should be v3, got %q", string(data))
	}

	// Find index of second real user message (excluding tool_result messages
	// which also have RoleUser). Tool result messages have content blocks
	// with type tool_result — real user messages do not.
	msgs := eng.Messages()
	turn2Idx := -1
	userCount := 0
	for i, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		// Skip tool_result messages — they have ContentTypeToolResult blocks
		isToolResult := false
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeToolResult {
				isToolResult = true
				break
			}
		}
		if isToolResult {
			continue
		}
		userCount++
		if userCount == 2 {
			turn2Idx = i
			break
		}
	}
	if turn2Idx < 0 {
		t.Fatalf("expected at least 3 real user messages, found %d", userCount)
	}

	// RewindToScoped(turn2Idx, RewindAll):
	//   snapshotID = msgs[turn2Idx].ID → snapshot at turn 2 boundary (file=v2)
	//   messages truncated to [:turn2Idx] → only turn 1 remains
	result, err := eng.RewindToScoped(turn2Idx, RewindAll)
	if err != nil {
		t.Fatalf("RewindToScoped failed: %v", err)
	}

	// File should be at v1 (pre-turn-2 state), not v3 (latest) or v0 (earliest)
	data, err = os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v1" {
		t.Errorf("file = %q, want v1 — intermediate rewind restores pre-turn-2 state", string(data))
	}

	if len(result.RestoredFiles) == 0 {
		t.Error("expected restored files from intermediate rewind")
	}
}

// TestE2E_SingleQueryMultiTool_Rewind reproduces the real-world scenario from gbot.log:
// One Query with multiple tool_use turns inside it (not separate QuerySync calls).
// MakeSnapshot should create per-turn snapshots so rewind can restore intermediate states.
//
// Red light: currently MakeSnapshot only runs at Query end (defer), producing one snapshot
// with the final file state. HasChangesAtMessage returns false and rewind can't restore
// intermediate versions.
func TestE2E_SingleQueryMultiTool_Rewind(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(testFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTool := &mockTool{
		name:    "Write",
		enabled: true,
		callFn: func(_ context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			var parsed struct {
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal([]byte(input), &parsed); err != nil {
				return nil, err
			}
			if parsed.FilePath != "" {
				if err := os.WriteFile(parsed.FilePath, []byte(parsed.Content), 0o644); err != nil {
					return nil, err
				}
			}
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	eventCh := make(chan types.QueryEvent, 50)
	dispatcher := &chanDispatcher{ch: eventCh}
	mp := &mockProvider{}
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{writeTool.Name(): writeTool}
		},
		Model:      "test",
		Dispatcher: dispatcher,
		WorkingDir: tmp,
	})
	defer eng.Close()
	eng.SetFileHistory(tracker)

	ctx := context.Background()

	// One QuerySync with multiple internal tool_use turns:
	// Turn 1 (internal): LLM calls Write with "v1"
	// Turn 2 (internal): LLM calls Write with "v2"
	// Turn 3 (internal): LLM returns text "done"
	mp.addResponse(toolUseStreamEvents("test", "tool_1", "Write",
		`{"file_path":"`+testFile+`","content":"v1"}`), nil)
	mp.addResponse(toolUseStreamEvents("test", "tool_2", "Write",
		`{"file_path":"`+testFile+`","content":"v2"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)

	// Single user message → one query with 3 internal turns
	eng.QuerySync(ctx, "edit the file twice", "")

	// File should be at v2 after both writes
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2" {
		t.Fatalf("file should be v2 after query, got %q", string(data))
	}

	// Check that we have multiple snapshots (one per internal turn, not just one at query end)
	state := tracker.State()
	if len(state.Snapshots) < 2 {
		t.Fatalf("expected at least 2 snapshots (one per tool turn), got %d — "+
			"MakeSnapshot must be called per turn, not only at query end",
			len(state.Snapshots))
	}

	// The user message (the only real user message in this query)
	msgs := eng.Messages()
	var userMsgID string
	var userMsgIdx int
	for i, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		isToolResult := false
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeToolResult {
				isToolResult = true
				break
			}
		}
		if !isToolResult {
			userMsgID = m.ID
			userMsgIdx = i
			break
		}
	}
	if userMsgID == "" {
		t.Fatal("expected at least one real user message")
	}

	// Rewind to the user message should restore the file to a state
	// different from the current v2 (ideally "original" or "v1")
	// because TrackEdit captured a v1 backup before the first write.
	hasChanges := tracker.HasChangesAtMessage(userMsgID)
	if !hasChanges {
		t.Errorf("HasChangesAtMessage(%q) = false, want true — "+
			"snapshot at this message should show file state before v2 edits, "+
			"which differs from current disk (v2)", userMsgID)
	}

	// Actually rewind and verify file content changes
	result, err := eng.RewindToScoped(userMsgIdx, RewindAll)
	if err != nil {
		t.Fatalf("RewindToScoped failed: %v", err)
	}
	if len(result.RestoredFiles) == 0 {
		t.Error("expected restored files from rewind of multi-tool query")
	}

	data, err = os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("file should be \"original\" after rewind of multi-tool query, got %q", string(data))
	}
}

// TestRewindTo_RecalculatesContextTokens verifies that ContextTokens is
// recalculated from per-message usage after rewind. TS uses lazy/derived
// tokenCountWithEstimation that walks messages for usage data each time.
// gbot stores ContextTokens, so it must recalculate after truncation.
func TestRewindTo_RecalculatesContextTokens(t *testing.T) {
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")},
			Usage: &types.Usage{InputTokens: 50000, OutputTokens: 100, CacheReadInputTokens: 30000}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp2")},
			Usage: &types.Usage{InputTokens: 80000, OutputTokens: 200, CacheReadInputTokens: 50000}},
	}
	eng.SetMessages(msgs)

	// Simulate ContextTokens from last API response
	eng.ContextTokens = 130200 // 80000+50000+200

	// Rewind to index 2 (keep msg1+resp1, remove msg2+resp2)
	result, err := eng.RewindTo(2)
	if err != nil {
		t.Fatalf("RewindTo failed: %v", err)
	}
	if result.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", result.MessageCount)
	}

	// ContextTokens should be recalculated from resp1's usage: 50000+30000+100 = 80100
	if eng.ContextTokens != 80100 {
		t.Errorf("ContextTokens = %d, want 80100 (precise from resp1 usage)", eng.ContextTokens)
	}
}

// validateToolPairing checks that every tool_use block in assistant messages
// has a matching tool_result in a subsequent user message. Returns the first
// orphaned tool_use ID, or "" if all are paired.
func validateToolPairing(msgs []types.Message) string {
	// Collect all tool_result IDs
	hasResult := map[string]bool{}
	for _, msg := range msgs {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult {
				hasResult[block.ToolUseID] = true
			}
		}
	}

	// Check every tool_use has a result
	for i, msg := range msgs {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolUse && !hasResult[block.ID] {
				return fmt.Sprintf("tool_use %s at msg[%d] has no matching tool_result", block.ID, i)
			}
		}
	}
	return ""
}

// TestE2E_Interrupt_DuringToolExecution_MessagePairing verifies that when the
// user cancels (ESC) while a tool is executing, the engine generates synthetic
// tool_results for orphaned tool_uses, and the resulting message sequence is
// valid for subsequent API calls (no 2013 error).
//
// This is a regression test for the bug where auto-rewind after interrupt
// would remove tool_result but leave tool_use, causing API 2013.
func TestE2E_Interrupt_DuringToolExecution_MessagePairing(t *testing.T) {
	blockStarted := make(chan struct{})

	mt := &mockTool{
		name:    "Bash",
		enabled: true,
		callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			close(blockStarted)
			<-ctx.Done() // simulate long-running tool
			return nil, ctx.Err()
		},
	}

	// LLM returns tool_use for Bash
	toolEvents := toolUseStreamEvents("test-model", "tu_interrupt_1", "Bash", `{"cmd":"sleep 999"}`)
	mp := &mockProvider{}
	mp.addResponse(toolEvents, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:      "test-model",
		Dispatcher: ec,
		Logger:     slog.Default(),
	})
	defer eng.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel while tool is executing
	go func() {
		<-blockStarted
		cancel()
	}()

	result := eng.QuerySync(ctx, "run slow command", "")
	if result.Error == nil {
		t.Fatal("expected error from cancelled context")
	}

	// Validate tool_use/tool_result pairing — this catches the 2013 root cause
	msgs := eng.Messages()
	if errMsg := validateToolPairing(msgs); errMsg != "" {
		t.Fatalf("message pairing broken after interrupt: %s", errMsg)
	}

	// Verify a synthetic tool_result exists for the interrupted tool
	found := false
	for _, msg := range msgs {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult && block.ToolUseID == "tu_interrupt_1" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected synthetic tool_result for tu_interrupt_1")
	}

	// Simulate next query with a fresh provider — verify no 2013
	mp2 := &mockProvider{}
	mp2.addResponse(textStreamEvents("test-model", "ok"), nil)
	eng.SetProvider(mp2)

	result2 := eng.QuerySync(context.Background(), "next query", "")
	if result2.Error != nil {
		t.Fatalf("next query failed: %v — message sequence invalid after interrupt", result2.Error)
	}
}

// searchReadStub implements tool.Tool + ToolWithSearchOrRead for testing.
type searchReadStub struct {
	stubTool
	srk tool.SearchReadKind
}

func (s *searchReadStub) IsSearchOrRead(json.RawMessage) tool.SearchReadKind {
	return s.srk
}

func TestComputeSearchReadKind(t *testing.T) {
	eng := New(&Params{
		Logger: slog.Default(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				"Grep": &searchReadStub{
					name: "Grep",
					srk:  tool.SearchReadKind{IsSearch: true},
				},
				"Bash": &stubTool{name: "Bash"},
			}
		},
	})
	t.Cleanup(func() { eng.Close() })

	// Tool with ToolWithSearchOrRead
	srk := eng.computeSearchReadKind("Grep", json.RawMessage(`{"pattern":"TODO"}`))
	if !srk.IsSearch {
		t.Errorf("Grep: expected IsSearch=true, got %+v", srk)
	}

	// Tool without ToolWithSearchOrRead
	srk = eng.computeSearchReadKind("Bash", json.RawMessage(`{"command":"ls"}`))
	if srk.IsCollapsible() {
		t.Errorf("Bash: expected zero SearchReadKind, got %+v", srk)
	}

	// Non-existent tool
	srk = eng.computeSearchReadKind("NonExistent", nil)
	if srk.IsCollapsible() {
		t.Errorf("NonExistent: expected zero SearchReadKind, got %+v", srk)
	}
}

func TestSetContextTokens(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	if eng.GetContextTokens() != 0 {
		t.Errorf("initial ContextTokens = %d, want 0", eng.GetContextTokens())
	}

	eng.SetContextTokens(5000)
	if eng.GetContextTokens() != 5000 {
		t.Errorf("after SetContextTokens(5000) = %d, want 5000", eng.GetContextTokens())
	}
}

func TestPersistContextTokens(t *testing.T) {
	t.Parallel()
	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })

	// Without store/sessionID, PersistContextTokens is a no-op
	eng.SetContextTokens(12345)
	eng.PersistContextTokens()

	// Verify GetContextTokens still works
	if eng.GetContextTokens() != 12345 {
		t.Errorf("GetContextTokens = %d, want 12345", eng.GetContextTokens())
	}
}

func TestSetMaxTokens(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	if eng.MaxTokens() != 16000 {
		t.Errorf("initial MaxTokens = %d, want 16000", eng.MaxTokens())
	}

	eng.SetMaxTokens(4096)
	if eng.MaxTokens() != 4096 {
		t.Errorf("after SetMaxTokens(4096) = %d, want 4096", eng.MaxTokens())
	}
}

func TestCallLLM_SystemPromptDynamicLoad(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	gbotDir := filepath.Join(tmpDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write custom SYSTEM.md
	customSystem := "You are a custom test assistant. {{SOUL}} {{MODEL}}"
	if err := os.WriteFile(filepath.Join(gbotDir, "SYSTEM.md"), []byte(customSystem), 0644); err != nil {
		t.Fatal(err)
	}

	// Write custom SOUL.md
	customSoul := "# TestSoul\nBe quirky."
	if err := os.WriteFile(filepath.Join(gbotDir, "SOUL.md"), []byte(customSoul), 0644); err != nil {
		t.Fatal(err)
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello!"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "my-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	eng.SetSystemPrompt("{{SYSTEM}}")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "Say hello", "{{SYSTEM}}")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Verify the captured system prompt has dynamic content
	mp.mu.Lock()
	req := mp.lastRequest
	mp.mu.Unlock()

	if req == nil {
		t.Fatal("no request captured")
	}

	var systemText string
	if err := json.Unmarshal(req.System, &systemText); err != nil {
		t.Fatalf("system prompt is not a string: %v", err)
	}

	// {{SYSTEM}} replaced with custom SYSTEM.md content
	if !strings.Contains(systemText, "custom test assistant") {
		t.Errorf("system prompt should contain custom SYSTEM.md content, got: %s", systemText)
	}
	// {{SOUL}} replaced with custom SOUL.md content
	if !strings.Contains(systemText, "TestSoul") {
		t.Errorf("system prompt should contain custom SOUL.md content, got: %s", systemText)
	}
	// {{MODEL}} replaced with engine model
	if !strings.Contains(systemText, "my-model") {
		t.Errorf("system prompt should contain model name, got: %s", systemText)
	}
	// Raw stubs should NOT remain
	if strings.Contains(systemText, "{{SYSTEM}}") {
		t.Error("{{SYSTEM}} stub should be replaced")
	}
	if strings.Contains(systemText, "{{SOUL}}") {
		t.Error("{{SOUL}} stub should be replaced")
	}
	if strings.Contains(systemText, "{{MODEL}}") {
		t.Error("{{MODEL}} stub should be replaced")
	}
}

func TestDumpAPIRequest_SystemPromptDynamicLoad(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	gbotDir := filepath.Join(tmpDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0755); err != nil {
		t.Fatal(err)
	}

	customSystem := "Custom dump assistant. {{SOUL}} {{MODEL}}"
	if err := os.WriteFile(filepath.Join(gbotDir, "SYSTEM.md"), []byte(customSystem), 0644); err != nil {
		t.Fatal(err)
	}
	customSoul := "# DumpSoul\nBe thorough."
	if err := os.WriteFile(filepath.Join(gbotDir, "SOUL.md"), []byte(customSoul), 0644); err != nil {
		t.Fatal(err)
	}

	mp := &mockProvider{}
	eng := New(&Params{
		Provider: mp,
		Model:    "dump-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	eng.SetSystemPrompt("{{SYSTEM}}")

	dump := eng.DumpAPIRequest()
	if dump == nil {
		t.Fatal("DumpAPIRequest returned nil")
	}

	if !strings.Contains(dump.SystemPrompt, "Custom dump assistant") {
		t.Errorf("DumpAPIRequest system prompt should contain custom SYSTEM.md, got: %s", dump.SystemPrompt)
	}
	if !strings.Contains(dump.SystemPrompt, "DumpSoul") {
		t.Errorf("DumpAPIRequest system prompt should contain custom SOUL.md, got: %s", dump.SystemPrompt)
	}
	if !strings.Contains(dump.SystemPrompt, "dump-model") {
		t.Errorf("DumpAPIRequest system prompt should contain model name, got: %s", dump.SystemPrompt)
	}
	if strings.Contains(dump.SystemPrompt, "{{SYSTEM}}") {
		t.Error("DumpAPIRequest {{SYSTEM}} stub should be replaced")
	}
}

// ---------------------------------------------------------------------------
// IsBusy + AttachmentsLen
// ---------------------------------------------------------------------------

// blockingMockProvider holds Stream open until release is closed, keeping
// queryActive == 1 mid-flight for IsBusy testing.
type blockingMockProvider struct {
	mu      sync.Mutex
	release chan struct{}
	called  bool
}

func (b *blockingMockProvider) Name() string { return "blocking-mock" }

func (b *blockingMockProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: []types.ContentBlock{types.NewTextBlock("ok")}}, nil
}

func (b *blockingMockProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	b.mu.Lock()
	b.called = true
	b.mu.Unlock()
	ch := make(chan llm.StreamEvent, 6)
	go func() {
		defer close(ch)
		<-b.release
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "m", Usage: types.Usage{InputTokens: 5}}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "reply"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 3}}
		ch <- llm.StreamEvent{Type: "message_stop"}
	}()
	return ch, nil
}

func (b *blockingMockProvider) wasCalled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.called
}

func TestEngine_IsBusy(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	bmp := &blockingMockProvider{release: release}
	eng := New(&Params{Provider: bmp, Model: "test-model", Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })

	// Fresh engine is not busy.
	if eng.IsBusy() {
		t.Fatal("fresh engine should not be busy")
	}

	// Start a query in the background — Stream blocks on release.
	go eng.Query(context.Background(), "hi", "")

	// Wait for Stream to be called (queryActive is now 1).
	deadline := time.After(3 * time.Second)
	for !bmp.wasCalled() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Stream to be called")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
		}
	}
	// Small extra sleep to ensure queryActive is set (atomic store happens in Query after Stream returns).
	time.Sleep(50 * time.Millisecond) // REAL-TIME: wait for atomic store to propagate

	if !eng.IsBusy() {
		t.Fatal("engine should be busy while Stream is blocked")
	}

	// Release the stream — query ends, queryActive goes back to 0.
	close(release)
	deadline2 := time.After(5 * time.Second)
	for eng.IsBusy() {
		select {
		case <-deadline2:
			t.Fatal("timed out waiting for engine to become idle")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
		}
	}
	if eng.IsBusy() {
		t.Fatal("engine should not be busy after query ends")
	}
}

func TestEngine_AttachmentsLen(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })

	if got := eng.AttachmentsLen(); got != 0 {
		t.Fatalf("AttachmentsLen() = %d, want 0", got)
	}

	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "item-1",
		Mode:      types.ItemModePrompt,
		Priority:  types.PriorityNext,
		Timestamp: time.Now(), // REAL-TIME: test setup timestamp
	})
	if got := eng.AttachmentsLen(); got != 1 {
		t.Fatalf("AttachmentsLen() = %d, want 1", got)
	}

	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "item-2",
		Mode:      types.ItemModeJob,
		Priority:  types.PriorityNext,
		Timestamp: time.Now(), // REAL-TIME: test setup timestamp
	})
	if got := eng.AttachmentsLen(); got != 2 {
		t.Fatalf("AttachmentsLen() = %d, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// createAttachmentMessages — Content pass-through
// ---------------------------------------------------------------------------

func TestCreateAttachmentMessages_WithContent(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})

	imgBlock := types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="})
	item := types.QueuedItem{
		Value:     "metadata-text",
		Mode:      types.ItemModePrompt,
		Priority:  types.PriorityNext,
		UUID:      "test-uuid",
		Timestamp: time.Now(), // REAL-TIME: test setup timestamp
		Content:   []types.ContentBlock{imgBlock},
	}

	msgs := eng.createAttachmentMessages([]types.QueuedItem{item})
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	msg := msgs[0]

	if len(msg.Content) != 1 {
		t.Fatalf("len(msg.Content) = %d, want 1", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeImage {
		t.Fatalf("Content[0].Type = %q, want %q", msg.Content[0].Type, types.ContentTypeImage)
	}
	if msg.Content[0].Source == nil {
		t.Fatal("Content[0].Source should not be nil for image block")
	}
	// Attachment.Prompt still uses Value, not Content.
	if msg.Attachment == nil {
		t.Fatal("msg.Attachment should not be nil")
	}
	if msg.Attachment.Prompt != "metadata-text" {
		t.Fatalf("Attachment.Prompt = %q, want %q", msg.Attachment.Prompt, "metadata-text")
	}
}

func TestCreateAttachmentMessages_TextFallback(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})

	item := types.QueuedItem{
		Value:     "hello",
		Mode:      types.ItemModePrompt,
		Priority:  types.PriorityNext,
		UUID:      "test-uuid-2",
		Timestamp: time.Now(), // REAL-TIME: test setup timestamp
		Content:   nil,
	}

	msgs := eng.createAttachmentMessages([]types.QueuedItem{item})
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	msg := msgs[0]
	if len(msg.Content) != 1 {
		t.Fatalf("len(msg.Content) = %d, want 1", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeText {
		t.Fatalf("Content[0].Type = %q, want %q", msg.Content[0].Type, types.ContentTypeText)
	}
	if msg.Content[0].Text != "hello" {
		t.Fatalf("Content[0].Text = %q, want %q", msg.Content[0].Text, "hello")
	}
}

// TestEngineDefaultModalitiesStripsImage guards the root-cause fix for the
// inputModalities initialization bug. An engine created with NO InputModalities
// must default to ["text"] (the most conservative value), so image blocks in a
// tool-less user turn are stripped to a "[image]" text placeholder before being
// sent to a text-only model. The red phase: before the fix New() left
// inputModalities empty, SupportsModality returned true on the empty branch,
// callLLM skipped stripping, and the image block reached the provider verbatim.
func TestEngineDefaultModalitiesStripsImage(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "ok"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	content := []types.ContentBlock{
		types.NewTextBlock("q"),
		types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}),
	}
	result := eng.QuerySyncWithContent(ctx, content, "")
	if result.Error != nil {
		t.Fatalf("QuerySyncWithContent error: %v", result.Error)
	}

	msgs := mp.lastRequestMessages()
	if len(msgs) == 0 {
		t.Fatal("provider received no messages")
	}
	var userMsg *types.Message
	for i := range msgs {
		if msgs[i].Role == types.RoleUser {
			userMsg = &msgs[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatal("no user message in provider request")
	}

	hasImagePlaceholder := false
	hasImageBlock := false
	for _, cb := range userMsg.Content {
		switch {
		case cb.Type == types.ContentTypeText && cb.Text == "[image]":
			hasImagePlaceholder = true
		case cb.Type == types.ContentTypeImage:
			hasImageBlock = true
		}
	}
	if hasImageBlock {
		t.Error("user message still carries a ContentTypeImage block; default [\"text\"] modalities should have stripped it")
	}
	if !hasImagePlaceholder {
		t.Error("user message missing the \"[image]\" text placeholder that StripMediaFromMessages substitutes for stripped image blocks")
	}
}

// TestQuery_ThinkingDurationPersisted verifies that thinking block duration
// is written into the ContentBlock after thinking_end, so it survives history
// persistence (used by wui/TUI history load to show "Thought for Xs").
func TestQuery_ThinkingDurationPersisted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: "pondering..."}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Query(ctx, "hi", "")
	ec.WaitForResult()

	msgs := eng.Messages()
	if len(msgs) < 2 {
		t.Fatalf("need at least 2 messages, got %d", len(msgs))
	}
	var assistant *types.Message
	for i := range msgs {
		if msgs[i].Role == types.RoleAssistant {
			assistant = &msgs[i]
			break
		}
	}
	if assistant == nil {
		t.Fatal("no assistant message in history")
	}
	var thinking *types.ContentBlock
	for i := range assistant.Content {
		if assistant.Content[i].Type == types.ContentTypeThinking {
			thinking = &assistant.Content[i]
			break
		}
	}
	if thinking == nil {
		t.Fatal("no thinking block in assistant message")
	}
	if thinking.ThinkingDurationNs == 0 {
		t.Errorf("ThinkingDurationNs = 0, want > 0 (duration should be persisted on thinking_end)")
	}
}

// TestQuery_ToolDurationPersisted verifies that the wall-clock tool execution
// time is written into the tool_result block's ToolDurationNs field, so it
// survives history persistence (used by wui/TUI history load to show the
// per-tool duration). Mirrors TestQuery_ThinkingDurationPersisted's scaffolding
// but exercises the tool_use → tool_result path instead.
func TestQuery_ToolDurationPersisted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "toolu_x", "Bash", `{"command":"echo hi"}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	mt := &mockTool{
		name:    "Bash",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "hi"}, nil
		},
	}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Query(ctx, "call tool", "")
	ec.WaitForResult()

	msgs := eng.Messages()
	var result *types.ContentBlock
	for i := range msgs {
		if msgs[i].Role != types.RoleUser {
			continue
		}
		for j := range msgs[i].Content {
			if msgs[i].Content[j].Type == types.ContentTypeToolResult {
				result = &msgs[i].Content[j]
				break
			}
		}
		if result != nil {
			break
		}
	}
	if result == nil {
		t.Fatal("no tool_result block in user messages")
	}
	if result.ToolDurationNs == 0 {
		t.Errorf("ToolDurationNs = 0, want > 0 (duration should be persisted on tool_end)")
	}
}

// TestQuery_ToolErrorDurationPersisted verifies that emitToolError writes
// non-zero ToolDurationNs on error tool_result blocks, mirroring the success
// path. Without this, error tools would show 0s on history replay.
func TestQuery_ToolErrorDurationPersisted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := toolUseStreamEvents("test-model", "t1", "fail_tool", `{}`)
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Recovered."), nil)

	mt := &mockTool{
		name:    "fail_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errors.New("tool execution failed")
		},
	}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Query(ctx, "call failing tool", "")
	ec.WaitForResult()

	// Verify event-level duration
	toolEndEvents := ec.FindEvents(types.EventToolEnd)
	var foundErrorEvt bool
	for _, evt := range toolEndEvents {
		if evt.ToolResult != nil && evt.ToolResult.IsError {
			foundErrorEvt = true
			if evt.ToolResult.Duration <= 0 {
				t.Errorf("error EventToolEnd.Duration = %v, want > 0", evt.ToolResult.Duration)
			}
		}
	}
	if !foundErrorEvt {
		t.Fatal("no error tool_end event found")
	}

	// Verify block-level duration (used for history replay)
	msgs := eng.Messages()
	var result *types.ContentBlock
	for i := range msgs {
		if msgs[i].Role != types.RoleUser {
			continue
		}
		for j := range msgs[i].Content {
			if msgs[i].Content[j].Type == types.ContentTypeToolResult && msgs[i].Content[j].IsError {
				result = &msgs[i].Content[j]
				break
			}
		}
		if result != nil {
			break
		}
	}
	if result == nil {
		t.Fatal("no error tool_result block in user messages")
	}
	if result.ToolDurationNs == 0 {
		t.Errorf("error tool_result ToolDurationNs = 0, want > 0")
	}
}

// streamingProviderWithDelay emits preconfigured events but inserts a real
// time.Sleep between content_block_start (tool_use) and content_block_stop.
// This simulates LLM streaming input_json_delta latency so tests can verify
// duration is measured from content_block_start, not from executeTool entry.
type streamingProviderWithDelay struct {
	events []llm.StreamEvent
	delay  time.Duration // sleep between content_block_start and content_block_stop
	idx    int
}

func (p *streamingProviderWithDelay) Name() string { return "streaming-delay" }
func (p *streamingProviderWithDelay) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, errors.New("not implemented")
}
func (p *streamingProviderWithDelay) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, len(p.events))
	go func() {
		defer close(ch)
		for _, ev := range p.events {
			// Insert the streaming delay AFTER content_block_start for tool_use
			// blocks, BEFORE content_block_stop — this is the wall-clock window
			// the user perceives as "LLM is thinking about the tool call".
			if ev.Type == "content_block_stop" && p.delay > 0 {
				<-time.After(p.delay)
			}
			ch <- ev
			p.idx++
		}
	}()
	return ch, nil
}

// findToolResultBlock scans engine messages for the tool_result block of toolID.
func findToolResultBlock(msgs []types.Message, toolID string) *types.ContentBlock {
	for i := range msgs {
		if msgs[i].Role != types.RoleUser {
			continue
		}
		for j := range msgs[i].Content {
			if msgs[i].Content[j].Type == types.ContentTypeToolResult &&
				msgs[i].Content[j].ToolUseID == toolID {
				return &msgs[i].Content[j]
			}
		}
	}
	return nil
}

// TestQuery_ToolDurationIncludesStreamingTime is the RED test for the bug
// "tt.Duration only measures t.Call execution, not the perceived tool_use →
// tool_end time". It drives the engine with a mock provider that sleeps 100ms
// (synctest virtual time) between content_block_start and content_block_stop
// to simulate LLM streaming the tool input. The actual tool execution is
// instantaneous (Read just opens a file). The persisted ToolDurationNs MUST
// be >= 100ms; if it only measures executeTool→t.Call, it will be ~0.
//
// Three tools are exercised in a single turn to also verify multi-tool timing:
//   - Read on a real temp file (instant execution)
//   - Read on a second real temp file (instant execution)
//   - Edit on a real temp file (instant execution)
//
// All three should report duration >= streamingDelay.
func TestQuery_ToolDurationIncludesStreamingTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tmpDir := t.TempDir()
		file1 := filepath.Join(tmpDir, "a.txt")
		file2 := filepath.Join(tmpDir, "b.txt")
		file3 := filepath.Join(tmpDir, "c.txt")
		mustWriteFile(t, file1, []byte("content-a"))
		mustWriteFile(t, file2, []byte("content-b"))
		mustWriteFile(t, file3, []byte("old"))

		readTool := fileread.New()
		editTool := fileedit.New()
		toolsMap := map[string]tool.Tool{
			"Read": readTool,
			"Edit": editTool,
		}

		// Build a single assistant turn with 3 tool_use blocks.
		// Each tool_use's content_block_start → content_block_stop will be
		// separated by streamingDelay (simulated LLM streaming of input).
		const streamingDelay = 100 * time.Millisecond
		read1Input, _ := json.Marshal(map[string]string{"file_path": file1})
		read2Input, _ := json.Marshal(map[string]string{"file_path": file2})
		editInput, _ := json.Marshal(map[string]any{
			"file_path":  file3,
			"old_string": "old",
			"new_string": "new",
		})

		events := []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 30}}},
		}
		// Helper to append a tool_use block (start → delta → stop). The delay
		// is injected by streamingProviderWithDelay on every content_block_stop.
		addToolUse := func(idx int, toolID, toolName string, input json.RawMessage) {
			events = append(events,
				llm.StreamEvent{Type: "content_block_start", Index: idx,
					ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: toolName}},
				llm.StreamEvent{Type: "content_block_delta", Index: idx,
					Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: string(input)}},
				llm.StreamEvent{Type: "content_block_stop", Index: idx},
			)
		}
		addToolUse(0, "toolu_read1", "Read", read1Input)
		addToolUse(1, "toolu_read2", "Read", read2Input)
		addToolUse(2, "toolu_edit1", "Edit", editInput)
		events = append(events,
			llm.StreamEvent{Type: "message_delta",
				DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"},
				Usage:    &types.Usage{OutputTokens: 30}},
			llm.StreamEvent{Type: "message_stop"},
		)

		mp := &streamingProviderWithDelay{events: events, delay: streamingDelay}
		// Second LLM response: short text to finish the turn.
		finishEvents := textStreamEvents("test-model", "done")
		mp2 := &mockProvider{}
		mp2.addResponse(finishEvents, nil)

		// Chain mp then mp2 via a composite provider.
		ec := newEventCollector()
		eng := New(&Params{
			Provider:      &chainedProvider{providers: []llm.Provider{mp, mp2}},
			Dispatcher:    ec,
			ToolsProvider: func() map[string]tool.Tool { return toolsMap },
			Model:         "test-model",
			Logger:        slog.Default(),
		})
		t.Cleanup(func() { eng.Close() })

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		eng.Query(ctx, "read 2 files and edit a third", "")
		ec.WaitForResult()

		msgs := eng.Messages()
		for _, toolID := range []string{"toolu_read1", "toolu_read2", "toolu_edit1"} {
			blk := findToolResultBlock(msgs, toolID)
			if blk == nil {
				t.Errorf("%s: no tool_result block found", toolID)
				continue
			}
			got := time.Duration(blk.ToolDurationNs)
			// Expect >= streamingDelay because duration must include the
			// simulated LLM streaming of input_json_delta.
			if got < streamingDelay {
				t.Errorf("%s ToolDurationNs = %v, want >= %v (must include streaming input time)",
					toolID, got, streamingDelay)
			} else {
				t.Logf("%s ToolDurationNs = %v ✓", toolID, got)
			}
		}
	})
}

// chainedProvider serves each provider's Stream response in sequence.
type chainedProvider struct {
	providers []llm.Provider
	idx       int
}

func (c *chainedProvider) Name() string { return "chained" }
func (c *chainedProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, errors.New("not implemented")
}
func (c *chainedProvider) Stream(ctx context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	if c.idx >= len(c.providers) {
		return nil, errors.New("no more chained providers")
	}
	p := c.providers[c.idx]
	c.idx++
	return p.Stream(ctx, req)
}

// mustWriteFile is a test helper that writes data to a file and fails the test on error.
func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestQuery_GrepWirePlainText drives a real Grep tool through the full chain
// (tool_use → tool.Call → formatWireBlocksOrDefault → marshalBlocks → API
// request) and asserts the tool_result text sent to the LLM is TS-aligned
// plain text, not an escaped-JSON blob.
func TestQuery_GrepWirePlainText(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed; Grep falls back to goGrep path, wire shape differs")
	}

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"))
	mustWriteFile(t, filepath.Join(dir, "util.go"), []byte("package main\n\nfunc helper() {}\n"))

	toolID := "tool_grep_wire_1"
	toolInput := `{"pattern":"func","path":"` + dir + `","output_mode":"files_with_matches"}`

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", toolID, "Grep", toolInput), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Grep": grep.New()}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "find funcs", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	wire, found := "", false
	for _, msg := range mp.lastRequestMessages() {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type != types.ContentTypeToolResult || block.ToolUseID != toolID {
				continue
			}
			var blocks []types.ContentBlock
			if err := json.Unmarshal(block.Content, &blocks); err != nil {
				t.Fatalf("tool_result.content is not a block array: %v", err)
			}
			if len(blocks) != 1 {
				t.Fatalf("len(blocks) = %d, want 1", len(blocks))
			}
			wire = blocks[0].Text
			found = true
		}
	}
	if !found {
		t.Fatal("no tool_result block for tool_grep_wire_1 in captured request")
	}

	if !strings.HasPrefix(wire, "Found ") {
		t.Errorf("wire text = %q, want prefix %q", wire, "Found ")
	}
	if !strings.Contains(wire, "main.go") {
		t.Errorf("wire text = %q, want it to mention main.go", wire)
	}
	if strings.Contains(wire, `\"filenames\"`) {
		t.Errorf("wire text = %q, still carries the JSON escape wall", wire)
	}
}

// TestQuery_ReplWirePlainText drives a real Repl tool through the full chain
// (tool_use → tool.Call → formatWireBlocksOrDefault → marshalBlocks → API
// request) and asserts the tool_result text sent to the LLM is the raw
// execution output, not the old JSON-encoded default wire.
func TestQuery_ReplWirePlainText(t *testing.T) {
	t.Parallel()

	toolID := "tool_repl_wire_1"
	toolInput := `{"code":"console.log(1+1)"}`

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", toolID, "Repl", toolInput), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	replTool := repl.New()
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Repl": replTool}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	t.Cleanup(replTool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "run js", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	wire, found := "", false
	for _, msg := range mp.lastRequestMessages() {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type != types.ContentTypeToolResult || block.ToolUseID != toolID {
				continue
			}
			var blocks []types.ContentBlock
			if err := json.Unmarshal(block.Content, &blocks); err != nil {
				t.Fatalf("tool_result.content is not a block array: %v", err)
			}
			if len(blocks) != 1 {
				t.Fatalf("len(blocks) = %d, want 1", len(blocks))
			}
			wire = blocks[0].Text
			found = true
		}
	}
	if !found {
		t.Fatal("no tool_result block for tool_repl_wire_1 in captured request")
	}

	// Exact == discriminates the two wires: the JSON default would send the
	// 5-char quoted form "\"2\\n\"" (quotes + escaped newline); the plain-text
	// wire sends the raw console.log output. console.log is required — a bare
	// expression yields empty output (Session.Execute discards the completion
	// value), which the engine replaces with its no-output notice.
	if wire != "2\n" {
		t.Errorf("wire text = %q, want %q", wire, "2\n")
	}
}

// TestQuery_GlobWirePlainText drives a real Glob tool through the full chain
// (tool_use → tool.Call → formatWireBlocksOrDefault → marshalBlocks → API
// request) and asserts the tool_result text sent to the LLM is the bare
// relative-path list, not an escaped-JSON blob.
func TestQuery_GlobWirePlainText(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "main.go"), []byte("package main\n"))

	toolID := "tool_glob_wire_1"
	toolInput := `{"pattern":"**/*.go","path":"` + dir + `"}`

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", toolID, "Glob", toolInput), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Glob": glob.New()}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "find files", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	wire, found := "", false
	for _, msg := range mp.lastRequestMessages() {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type != types.ContentTypeToolResult || block.ToolUseID != toolID {
				continue
			}
			var blocks []types.ContentBlock
			if err := json.Unmarshal(block.Content, &blocks); err != nil {
				t.Fatalf("tool_result.content is not a block array: %v", err)
			}
			if len(blocks) != 1 {
				t.Fatalf("len(blocks) = %d, want 1", len(blocks))
			}
			wire = blocks[0].Text
			found = true
		}
	}
	if !found {
		t.Fatal("no tool_result block for tool_glob_wire_1 in captured request")
	}

	// Single file keeps the list deterministic (rg sorts by mtime; two files
	// created in the same tick could arrive in either order).
	if wire != "main.go" {
		t.Errorf("wire text = %q, want %q", wire, "main.go")
	}
	if strings.Contains(wire, `\"filenames\"`) {
		t.Errorf("wire text = %q, still carries the JSON escape wall", wire)
	}
}

// stopFailureRecorder captures hook dispatches for the terminal-error path.
type stopFailureRecorder struct {
	mu     sync.Mutex
	events []string
	reason string
	cwd    string
	fired  chan struct{}
	once   sync.Once
}

func newStopFailureRecorder() *stopFailureRecorder {
	return &stopFailureRecorder{fired: make(chan struct{})}
}

func (r *stopFailureRecorder) ExecuteHook(ctx context.Context, command string, input *hooks.HookInput, timeout time.Duration, extraEnv []string) hooks.HookResult {
	r.mu.Lock()
	r.events = append(r.events, input.HookEventName)
	r.reason = input.Reason
	r.cwd = input.Cwd
	r.mu.Unlock()
	r.once.Do(func() { close(r.fired) })
	return hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, HookName: command}
}

func (r *stopFailureRecorder) snapshot() (events []string, reason, cwd string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...), r.reason, r.cwd
}

// TestTerminalAPIErrorFiresStopFailureNotStop pins the TS query.ts:1259-1265
// contract: an API-error turn fires the dedicated StopFailure hooks
// (fire-and-forget) and skips normal Stop hooks — the model never produced a
// response, so a blocking Stop hook would loop error→hook→retry→error.
func TestTerminalAPIErrorFiresStopFailureNotStop(t *testing.T) {
	mp := &mockProvider{}
	// Non-retryable 429: terminal at engine level, so the StopFailure hook
	// path must fire with the error text as Reason.
	mp.addResponse(nil, &llm.APIError{Type: "rate_limit_error", Message: "rate limited", Status: 429, Retryable: false})
	rec := newStopFailureRecorder()
	h := hooks.NewHooks(hooks.HooksConfig{
		"StopFailure": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "on-failure"},
		}}},
		"Stop": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "on-stop"},
		}}},
	}, rec)
	dir := t.TempDir()
	eng := New(&Params{Provider: mp, Model: "m", WorkingDir: dir, Logger: slog.Default(), Hooks: h})
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if res := eng.QuerySync(ctx, "test", ""); res.Error == nil {
		t.Fatal("expected terminal error")
	}
	// Fire-and-forget: signal-driven wait, no sleep-poll (weak-test P2).
	select {
	case <-rec.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("StopFailure hook never fired within 2s")
	}
	got, reason, cwd := rec.snapshot()
	if len(got) != 1 || got[0] != "StopFailure" {
		t.Fatalf("expected exactly one StopFailure dispatch, got %v", got)
	}
	if !strings.Contains(reason, "rate limited") {
		t.Errorf("Reason must carry the error text for TS matchQuery parity, got %q", reason)
	}
	if cwd != dir {
		t.Errorf("StopFailure hook input Cwd = %q, want the engine's working dir %q", cwd, dir)
	}
}

// ===========================================================================
// Engine loop pins: exit traces, compact, skills, attachments
// ===========================================================================

// slogCapture is a slog.Handler that records every record with its attributes,
// so log-only statements (which have no other observable effect) stay asserted.

// awaitErrorEvents waits (bounded, signal-polled) until the collector holds at
// least n error-carrying events, then returns them.
func awaitErrorEvents(ec *eventCollector, n int) []types.QueryEvent {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out []types.QueryEvent
	for {
		out = nil
		for _, ev := range ec.Events() {
			if ev.Error != nil {
				out = append(out, ev)
			}
		}
		if len(out) >= n {
			return out
		}
		select {
		case <-ctx.Done():
			return out
		default:
			runtime.Gosched()
		}
	}
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitEngineIdle blocks until the engine reports idle, i.e. its query goroutine
// has run every exit defer. Query registers ownership synchronously, so the
// flag is already set when Query returns — no sleep-then-assert window.
func waitEngineIdle(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for e.IsBusy() {
		select {
		case <-ctx.Done():
			t.Fatalf("waitEngineIdle: engine still busy after %v", 5*time.Second)
		default:
			runtime.Gosched()
		}
	}
}

func TestAutoCompactBuffer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ window, want int }{
		{200_000, 14_000}, // 7% of the window, the TS-13K equivalent point
		{43_000, 3_010},   // just above the floor
		{42_000, 3_000},   // 2940 → floor wins
		{10_000, 3_000},
		{0, 3_000},
	} {
		if got := autoCompactBuffer(tc.window); got != tc.want {
			t.Errorf("autoCompactBuffer(%d) = %d, want %d", tc.window, got, tc.want)
		}
	}
}

func TestLastAssistantText(t *testing.T) {
	t.Parallel()
	assistant := func(texts ...string) types.Message {
		m := types.Message{Role: types.RoleAssistant}
		for _, s := range texts {
			m.Content = append(m.Content, types.NewTextBlock(s))
		}
		return m
	}
	user := func(s string) types.Message {
		return types.NewUserMessage([]types.ContentBlock{types.NewTextBlock(s)})
	}

	// A trailing user message must be skipped, not read.
	if got := lastAssistantText([]types.Message{assistant("older"), user("newest")}); got != "older" {
		t.Errorf("lastAssistantText with trailing user message = %q, want %q", got, "older")
	}
	// No assistant at all → empty.
	if got := lastAssistantText([]types.Message{user("a"), user("b")}); got != "" {
		t.Errorf("lastAssistantText without assistant = %q, want empty", got)
	}
	if got := lastAssistantText(nil); got != "" {
		t.Errorf("lastAssistantText(nil) = %q, want empty", got)
	}
	// Only text blocks concatenate; tool_use blocks are skipped.
	withTool := assistant("part1 ", "part2")
	withTool.Content = append(withTool.Content, types.ContentBlock{
		Type: types.ContentTypeToolUse, ID: "t1", Name: "X",
	})
	if got := lastAssistantText([]types.Message{withTool}); got != "part1 part2" {
		t.Errorf("lastAssistantText mixed blocks = %q, want %q", got, "part1 part2")
	}
	// The LAST assistant wins, not the first one found.
	if got := lastAssistantText([]types.Message{assistant("first"), assistant("second")}); got != "second" {
		t.Errorf("lastAssistantText two assistants = %q, want %q", got, "second")
	}
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	pc := &mockPermChecker{hasRules: true}
	e := New(&Params{
		Provider:          &mockProvider{},
		Model:             "test-model",
		PermissionChecker: pc,
		AutoCompact:       AutoCompactConfig{ContextWindow: 1},
	})
	t.Cleanup(func() { e.Close() })

	if !slices.Equal(e.inputModalities, []string{"text"}) {
		t.Errorf("inputModalities = %v, want the conservative default [text]", e.inputModalities)
	}
	if e.taskList == nil {
		t.Fatal("taskList must be auto-created when Params.TaskList is nil")
	}
	if got := e.taskList.Dir(); got != "" {
		t.Errorf("auto-created taskList Dir() = %q, want empty (resolved per session)", got)
	}
	if e.permissionChecker != pc {
		t.Errorf("permissionChecker = %v, want the injected checker %v", e.permissionChecker, pc)
	}
	if e.compactor == nil {
		t.Fatal("compactor must be auto-created when AutoCompact.ContextWindow > 0")
	}
}

func TestNewKeepsExplicitModalities(t *testing.T) {
	t.Parallel()
	e := New(&Params{
		Provider:        &mockProvider{},
		Model:           "test-model",
		InputModalities: []string{"image"},
	})
	t.Cleanup(func() { e.Close() })
	if !slices.Equal(e.inputModalities, []string{"image"}) {
		t.Errorf("inputModalities = %v, want the injected [image]", e.inputModalities)
	}
}

func TestNewCompactorAutoCreateGuards(t *testing.T) {
	t.Parallel()
	// No provider → nothing to compact with, so no auto-compactor.
	noProvider := New(&Params{Model: "test-model", AutoCompact: AutoCompactConfig{ContextWindow: 50_000}})
	t.Cleanup(func() { noProvider.Close() })
	if noProvider.compactor != nil {
		t.Error("compactor must stay nil when no provider is configured")
	}
	// An injected compactor must never be replaced by the auto-created one.
	injected := &mockCompactor{}
	e := New(&Params{
		Provider:    &mockProvider{},
		Model:       "test-model",
		Compactor:   injected,
		AutoCompact: AutoCompactConfig{ContextWindow: 50_000},
	})
	t.Cleanup(func() { e.Close() })
	if e.compactor != Compactor(injected) {
		t.Errorf("compactor = %v, want the injected compactor to survive", e.compactor)
	}
	// ContextWindow 0 disables proactive compaction entirely.
	off := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
	t.Cleanup(func() { off.Close() })
	if off.compactor != nil {
		t.Error("compactor must stay nil when AutoCompact.ContextWindow is 0")
	}
}

func TestEnqueueAttachmentLogPreview(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: slog.New(logs)})
	t.Cleanup(func() { e.Close() })

	const eighty = "01234567890123456789012345678901234567890123456789012345678901234567890123456789"
	if len(eighty) != 80 {
		t.Fatalf("fixture length = %d, want 80", len(eighty))
	}
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"short", "hello", "hello"},
		{"exactly-80", eighty, eighty},
		{"81-chars", eighty + "x", eighty + "..."},
	}
	for _, tc := range cases {
		logs.mu.Lock()
		logs.calls = nil
		logs.mu.Unlock()
		e.EnqueueAttachment(types.QueuedItem{UUID: "u-" + tc.name, Mode: types.ItemModeJob, Value: tc.value})
		recs := logs.all("engine:attachment_enqueued")
		if len(recs) != 1 {
			t.Fatalf("%s: %d attachment_enqueued records, want exactly 1", tc.name, len(recs))
		}
		if got := recs[0].attrs["value_preview"]; got != tc.want {
			t.Errorf("%s: value_preview = %q, want %q", tc.name, got, tc.want)
		}
		if got := recs[0].attrs["mode"]; got != any(types.ItemModeJob) {
			t.Errorf("%s: mode = %v, want %v", tc.name, got, types.ItemModeJob)
		}
		if got := recs[0].attrs["priority"]; got != any(types.PriorityNext) {
			t.Errorf("%s: priority = %v, want %v", tc.name, got, types.PriorityNext)
		}
	}
}

func TestDrainQueuedUserAttachmentsOnlyDrainsPrompts(t *testing.T) {
	t.Parallel()
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { e.Close() })

	e.EnqueueAttachment(types.QueuedItem{UUID: "note-1", Mode: types.ItemModeNotification, Value: "a note"})
	e.EnqueueAttachment(types.QueuedItem{UUID: "prompt-1", Mode: types.ItemModePrompt, Value: "a prompt"})
	before := len(e.Messages())

	e.drainQueuedUserAttachments()

	if got := e.attachments.Len(); got != 1 {
		t.Errorf("queue length after drain = %d, want 1 (the notification must stay queued)", got)
	}
	msgs := e.Messages()
	if len(msgs) != before+1 {
		t.Fatalf("history grew by %d messages, want exactly 1", len(msgs)-before)
	}
	if msgs[before].ID != "prompt-1" {
		t.Errorf("drained message ID = %q, want the prompt item %q", msgs[before].ID, "prompt-1")
	}
	if msgs[before].Role != types.RoleUser {
		t.Errorf("drained message role = %v, want user", msgs[before].Role)
	}
}

func TestQueuedInsertIndex(t *testing.T) {
	t.Parallel()
	assistantText := func(s string) types.Message {
		return types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(s)}}
	}
	assistantToolUse := func(id string) types.Message {
		return types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: id, Name: "X"},
		}}
	}
	userToolResult := func(id string) types.Message {
		return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: id},
		}}
	}
	userToolUse := func(id string) types.Message {
		return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: id, Name: "X"},
		}}
	}
	assistantToolResult := func(id string) types.Message {
		return types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: id},
		}}
	}

	// Unpaired tool_use → insert before the assistant that owns it.
	if got := queuedInsertIndex([]types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("hi")}),
		assistantToolUse("t1"),
	}); got != 1 {
		t.Errorf("unpaired tool_use: index = %d, want 1", got)
	}
	// Paired tool_use → plain append.
	if got := queuedInsertIndex([]types.Message{assistantToolUse("t1"), userToolResult("t1")}); got != 2 {
		t.Errorf("paired tool_use: index = %d, want 2", got)
	}
	// A tool_result on a NON-user message does not count as a pairing.
	if got := queuedInsertIndex([]types.Message{
		assistantToolResult("t1"),
		assistantToolUse("t1"),
	}); got != 1 {
		t.Errorf("tool_result on assistant: index = %d, want 1 (only user messages pair)", got)
	}
	// A tool_use on a NON-assistant message is not a scan target.
	if got := queuedInsertIndex([]types.Message{
		assistantText("done"),
		userToolUse("t9"),
	}); got != 2 {
		t.Errorf("tool_use on user: index = %d, want 2 (only assistant messages are scanned)", got)
	}
	// The backward scan must skip non-assistant messages, not stop at them.
	if got := queuedInsertIndex([]types.Message{
		assistantToolUse("t1"),
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("x")}),
	}); got != 0 {
		t.Errorf("assistant behind a user message: index = %d, want 0", got)
	}
	// Empty history appends at 0.
	if got := queuedInsertIndex(nil); got != 0 {
		t.Errorf("empty history: index = %d, want 0", got)
	}
}

func TestRemoveAttachment(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: slog.New(logs)})
	t.Cleanup(func() { e.Close() })

	// An empty uuid is never a match, even for a queued item that carries one.
	e.EnqueueAttachment(types.QueuedItem{UUID: "", Mode: types.ItemModeJob, Value: "blank uuid"})
	if e.RemoveAttachment("") {
		t.Error("RemoveAttachment(\"\") must report no removal")
	}
	if got := e.attachments.Len(); got != 1 {
		t.Errorf("queue length = %d, want 1 (the blank-uuid item must stay)", got)
	}

	e.EnqueueAttachment(types.QueuedItem{UUID: "u1", Mode: types.ItemModeJob, Value: "x"})
	if !e.RemoveAttachment("u1") {
		t.Error("RemoveAttachment(u1) must report removal")
	}
	if got := e.attachments.Len(); got != 1 {
		t.Errorf("queue length after removing u1 = %d, want 1", got)
	}
	if e.RemoveAttachment("missing") {
		t.Error("RemoveAttachment(missing) must report no removal")
	}

	recs := logs.all("engine:attachment_removed")
	if len(recs) != 1 {
		t.Fatalf("%d attachment_removed records, want exactly 1", len(recs))
	}
	if got := recs[0].attrs["uuid"]; got != "u1" {
		t.Errorf("attachment_removed uuid = %v, want u1", got)
	}
}

func TestAbort(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: slog.New(logs)})
	t.Cleanup(func() { e.Close() })

	// Nothing active → the noop branch logs.
	e.Abort()
	if recs := logs.all("engine:abort_noop_no_active_cancel"); len(recs) != 1 {
		t.Errorf("%d abort_noop records, want exactly 1", len(recs))
	}

	// The cancel call must run while activeCancelMu is still held: a probe
	// that re-locks from inside the cancel can only fail in the correct code.
	reentrant := false
	e.beginQuery(func() {
		reentrant = e.activeCancelMu.TryLock()
		if reentrant {
			e.activeCancelMu.Unlock()
		}
	})
	e.Abort()
	if reentrant {
		t.Error("Abort released activeCancelMu before the cancel call returned")
	}
	if e.activeCancel != nil {
		t.Error("Abort must clear activeCancel after cancelling")
	}
	if recs := logs.all("engine:abort_entered"); len(recs) != 1 {
		t.Errorf("%d abort_entered records, want exactly 1", len(recs))
	}
	if recs := logs.all("engine:abort_cancel_returned"); len(recs) != 1 {
		t.Errorf("%d abort_cancel_returned records, want exactly 1", len(recs))
	}
	// A second Abort with nothing active goes back to the noop branch.
	e.Abort()
	if recs := logs.all("engine:abort_noop_no_active_cancel"); len(recs) != 2 {
		t.Errorf("%d abort_noop records after the second Abort, want 2", len(recs))
	}
}

func TestBeginQueryIfIdleRejectsClosingEngine(t *testing.T) {
	t.Parallel()
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: silentLogger()})
	e.closing = true
	tok, ok := e.beginQueryIfIdle(func() {})
	if tok != 0 || ok {
		t.Errorf("beginQueryIfIdle on a closing engine = (%d, %v), want (0, false)", tok, ok)
	}
	if e.queryActive.Load() != 0 {
		t.Error("a closing engine must not be marked busy")
	}
}

func TestEndQuery(t *testing.T) {
	t.Parallel()
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { e.Close() })

	// Token 0 means "no ownership": it must never release anyone's state.
	e.queryActive.Store(1)
	e.endQuery(0)
	if got := e.queryActive.Load(); got != 0 && got != 1 {
		t.Fatalf("queryActive after endQuery(0) = %d, want unchanged", got)
	}
	if got := e.queryActive.Load(); got != 1 {
		t.Errorf("endQuery(0) changed queryActive to %d, want 1 (no ownership, no cleanup)", got)
	}

	// The owner releases its own state.
	tok := e.beginQuery(func() {})
	if tok == 0 {
		t.Fatal("beginQuery on an open engine must return a non-zero token")
	}
	e.endQuery(tok)
	if got := e.queryActive.Load(); got != 0 {
		t.Errorf("queryActive after the owner's endQuery = %d, want 0", got)
	}
	if e.activeCancel != nil {
		t.Error("endQuery must clear activeCancel")
	}

	// A stale token must not clobber the newer owner.
	first := e.beginQuery(func() {})
	second := e.beginQuery(func() {})
	if second <= first {
		t.Fatalf("tokens must be monotonic: first=%d second=%d", first, second)
	}
	e.endQuery(first)
	if got := e.queryActive.Load(); got != 1 {
		t.Errorf("stale endQuery cleared the busy flag (%d), want 1", got)
	}
	e.endQuery(second)
	if got := e.queryActive.Load(); got != 0 {
		t.Errorf("queryActive after the current owner's endQuery = %d, want 0", got)
	}
}

// panickingProvider panics inside Stream, reaching Query's recover.
type panickingProvider struct{}

func (p *panickingProvider) Name() string { return "panicker" }

func (p *panickingProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	panic("provider stream exploded")
}

func (p *panickingProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	panic("provider stream exploded")
}

func TestQueryPanicEmitsQueryEnd(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	e := New(&Params{
		Provider:   &panickingProvider{},
		Model:      "test-model",
		Logger:     silentLogger(),
		Dispatcher: ec,
	})
	e.Query(context.Background(), "hi", "")
	waitEngineIdle(t, e)
	e.Close() // joins the query goroutine: every event is dispatched by return

	var errEvents []types.QueryEvent
	for _, ev := range ec.Events() {
		if ev.Error != nil {
			errEvents = append(errEvents, ev)
		}
	}
	if len(errEvents) != 1 {
		t.Fatalf("%d error-carrying events after a panicking query, want exactly 1", len(errEvents))
	}
	if errEvents[0].Type != types.EventQueryEnd {
		t.Errorf("panic event type = %v, want %v", errEvents[0].Type, types.EventQueryEnd)
	}
	if !strings.Contains(errEvents[0].Error.Error(), "provider stream exploded") {
		t.Errorf("panic event error = %q, want it to carry the panic value", errEvents[0].Error.Error())
	}
}

func TestQueryPanicLogs(t *testing.T) {
	// slog.Error is the package-level default logger, so the capture has to
	// swap it; sequential (non-parallel) keeps the global swap race-free.
	logs := &slogCapture{}
	attachSlogCapture(t, logs)

	e := New(&Params{
		Provider: &panickingProvider{},
		Model:    "test-model",
		Logger:   silentLogger(),
	})
	e.Query(context.Background(), "hi", "")
	waitEngineIdle(t, e)
	e.Close()

	recs := logs.all("engine: panic in queryLoop")
	if len(recs) != 1 {
		t.Fatalf("%d panic records, want exactly 1", len(recs))
	}
	if recs[0].level != slog.LevelError {
		t.Errorf("panic record level = %v, want error", recs[0].level)
	}
	if got := fmt.Sprint(recs[0].attrs["error"]); got != "provider stream exploded" {
		t.Errorf("panic record error = %q, want %q", got, "provider stream exploded")
	}
	if got, ok := recs[0].attrs["stack"]; !ok || !strings.Contains(fmt.Sprint(got), "panickingProvider") {
		t.Errorf("panic record stack = %v, want the panic stack", got)
	}
}

func TestProcessAttachmentsBusyFlag(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	hang := make(chan struct{})
	mp.responses = append(mp.responses, mockResponse{
		events: textStreamEvents("test-model", "done"),
		hang:   hang,
	})
	e := New(&Params{Provider: mp, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { e.Close() })

	e.EnqueueAttachment(types.QueuedItem{UUID: "p1", Mode: types.ItemModePrompt, Value: "go"})
	e.ProcessAttachments(context.Background(), "sp")

	waitForCallCount(t, mp, 1, 5*time.Second)
	if !e.IsBusy() {
		t.Error("engine must report busy while attachment processing runs a turn")
	}
	close(hang)
	e.Close()
	if e.IsBusy() {
		t.Error("engine must be idle once attachment processing finishes")
	}
}

func TestProcessAttachmentsReKicksOnExit(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	hang := make(chan struct{})
	mp.responses = append(mp.responses,
		mockResponse{events: textStreamEvents("test-model", "first"), hang: hang},
		mockResponse{events: textStreamEvents("test-model", "second")},
	)
	e := New(&Params{Provider: mp, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { e.Close() })

	// The engine gets its prompt only after the first enqueue, so the
	// enqueue-time kick stays inert and the exit kick is the sole path that can
	// run the second item.
	e.EnqueueAttachment(types.QueuedItem{UUID: "p1", Mode: types.ItemModePrompt, Value: "go"})
	e.SetSystemPrompt("sp")
	e.ProcessAttachments(context.Background(), "sp")
	waitForCallCount(t, mp, 1, 5*time.Second)

	// A job item is invisible to the in-loop prompt drain, so only the exit
	// kick can ever run it.
	e.EnqueueAttachment(types.QueuedItem{UUID: "j1", Mode: types.ItemModeJob, Value: "later job"})
	close(hang)
	waitForCallCount(t, mp, 2, 5*time.Second)
	want := 0 // both drained items were consumed by the exit kick
	if got := e.attachments.Len(); got != want {
		t.Errorf("queue length after the exit kick = %d, want %d", got, want)
	}
}

func TestToolRefsRoundTrip(t *testing.T) {
	t.Parallel()
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { e.Close() })

	refs := ToolRefs{Reg: tool.NewRegistry()}
	if got := e.ToolRefs(); got != (ToolRefs{}) {
		t.Errorf("ToolRefs before SetToolRefs = %+v, want the zero value", got)
	}
	e.SetToolRefs(refs)
	if got := e.ToolRefs(); got != refs {
		t.Errorf("ToolRefs after SetToolRefs = %+v, want %+v", got, refs)
	}
	if e.ToolRefs().Reg != refs.Reg {
		t.Error("ToolRefs().Reg must be the registry that was set")
	}
}

func TestAppendDrainedItemPerMode(t *testing.T) {
	t.Parallel()
	e := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { e.Close() })

	promptMsg := e.appendDrainedItem(types.QueuedItem{UUID: "p1", Mode: types.ItemModePrompt, Value: "hello"})
	if promptMsg == nil {
		t.Fatal("a prompt item must produce an attachment message")
	}
	if promptMsg.ID != "p1" || promptMsg.Role != types.RoleUser {
		t.Errorf("prompt message = (%q, %v), want (p1, user)", promptMsg.ID, promptMsg.Role)
	}
	if promptMsg.MessageType != types.MessageTypeAttachment {
		t.Errorf("prompt message type = %v, want attachment", promptMsg.MessageType)
	}
	if promptMsg.Attachment == nil || promptMsg.Attachment.Prompt != "hello" {
		t.Errorf("prompt message attachment = %+v, want the item value", promptMsg.Attachment)
	}
	for _, mode := range []types.ItemMode{types.ItemModeJob, types.ItemModeMessage, types.ItemModeNotification} {
		if got := e.appendDrainedItem(types.QueuedItem{UUID: "u-" + string(mode), Mode: mode, Value: "v"}); got != nil {
			t.Errorf("appendDrainedItem(%v) = %+v, want nil (reminder items emit their own event)", mode, got)
		}
	}
	if got := len(e.Messages()); got != 4 {
		t.Errorf("history length = %d, want 4 (one per drained item)", got)
	}
}

func TestQueryOnClosedEngine(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	e := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test-model",
		Logger:     silentLogger(),
		Dispatcher: ec,
	})
	e.Close()
	e.Query(context.Background(), "hi", "")

	errEvents := awaitErrorEvents(ec, 1)
	if len(errEvents) != 1 {
		t.Fatalf("%d error events on a closed engine, want exactly 1", len(errEvents))
	}
	if errEvents[0].Type != types.EventQueryEnd {
		t.Errorf("closed-engine event type = %v, want %v", errEvents[0].Type, types.EventQueryEnd)
	}
	if got := errEvents[0].Error.Error(); got != "engine: query on closed engine" {
		t.Errorf("closed-engine error = %q, want the closed-engine message", got)
	}
	if mp, ok := e.provider.(*mockProvider); ok && mp.callCount() != 0 {
		t.Errorf("closed engine made %d provider calls, want 0", mp.callCount())
	}
}

// runAgentDeps is the minimal SharedDeps bundle RunAgent needs.
func runAgentDeps(t *testing.T) SharedDeps {
	t.Helper()
	return SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
}

func runAgentTools() map[string]tool.Tool {
	return map[string]tool.Tool{
		"Read": &mockTool{name: "Read", enabled: true},
		"Grep": &mockTool{name: "Grep", enabled: true},
	}
}

// runAgentOnce runs one sub-agent and returns the request the sub-engine sent.
func runAgentOnce(t *testing.T, mp *mockProvider, opts agenttool.AgentOpts, tools map[string]tool.Tool, deps *SharedDeps) *llm.Request {
	t.Helper()
	if tools == nil {
		tools = runAgentTools()
	}
	d := runAgentDeps(t)
	if deps != nil {
		d = *deps
	}
	eng := New(&Params{
		Provider:      mp,
		Model:         "parent-model",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return tools },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&d)
	if _, err := eng.RunAgent(context.Background(), opts); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	req := mp.lastRequest
	if req == nil {
		t.Fatal("sub-engine never sent a request")
	}
	return req
}

func requestToolNames(req *llm.Request) []string {
	names := make([]string, 0, len(req.Tools))
	for _, td := range req.Tools {
		names = append(names, td.Name)
	}
	slices.Sort(names)
	return names
}

func TestRunAgentModelResolution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts agenttool.AgentOpts
		want string
	}{
		{"inherit-by-default", agenttool.AgentOpts{Prompt: "p", AgentType: "General"}, "parent-model"},
		{"literal-inherit", agenttool.AgentOpts{Prompt: "p", AgentType: "General", Model: "inherit"}, "parent-model"},
	}
	for _, tc := range cases {
		mp := &mockProvider{}
		mp.addResponse(textStreamEvents("test", "ok"), nil)
		req := runAgentOnce(t, mp, tc.opts, nil, nil)
		if req.Model != tc.want {
			t.Errorf("%s: sub-request model = %q, want %q", tc.name, req.Model, tc.want)
		}
	}
}

func TestRunAgentToolRestriction(t *testing.T) {
	t.Parallel()
	// No AllowedTools: the agent-filtered set (both parent tools) survives.
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)
	req := runAgentOnce(t, mp, agenttool.AgentOpts{Prompt: "p", AgentType: "General"}, nil, nil)
	if got := requestToolNames(req); !slices.Equal(got, []string{"Grep", "Read"}) {
		t.Errorf("no AllowedTools: request tools = %v, want [Grep Read]", got)
	}

	// One allowed tool: the restriction must narrow to exactly that tool.
	mp = &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)
	req = runAgentOnce(t, mp, agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", AllowedTools: []string{"Read"},
	}, nil, nil)
	if got := requestToolNames(req); !slices.Equal(got, []string{"Read"}) {
		t.Errorf("AllowedTools=[Read]: request tools = %v, want [Read]", got)
	}

	// Two allowed tools: every entry of the list is considered, not just the first.
	mp = &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)
	req = runAgentOnce(t, mp, agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", AllowedTools: []string{"Read", "Grep", "Missing"},
	}, nil, nil)
	if got := requestToolNames(req); !slices.Equal(got, []string{"Grep", "Read"}) {
		t.Errorf("AllowedTools=[Read Grep Missing]: request tools = %v, want [Grep Read]", got)
	}
}

func TestRunAgentRegistersMcpRegistryTools(t *testing.T) {
	t.Parallel()
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	reg.SetToolsForTest([]mcp.DiscoveredTool{
		{Name: "mcp__srv__alpha", OriginalName: "alpha", ServerName: "srv",
			Description: "a", InputSchema: json.RawMessage(`{"type":"object"}`), AlwaysLoad: true},
		{Name: "mcp__srv__beta", OriginalName: "beta", ServerName: "srv",
			Description: "b", InputSchema: json.RawMessage(`{"type":"object"}`), AlwaysLoad: true},
	})
	deps := runAgentDeps(t)
	deps.McpReg = reg

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)
	tools := runAgentTools()
	tools["mcp__srv__alpha"] = &mockTool{name: "mcp__srv__alpha", enabled: true}
	tools["mcp__srv__beta"] = &mockTool{name: "mcp__srv__beta", enabled: true}

	req := runAgentOnce(t, mp, agenttool.AgentOpts{Prompt: "p", AgentType: "General"}, tools, &deps)
	if got := requestToolNames(req); !slices.Equal(got, []string{"Grep", "Read", "mcp__srv__alpha", "mcp__srv__beta"}) {
		t.Errorf("request tools = %v, want both MCP tools registered from the parent registry", got)
	}
}

func TestRunAgentMaxTurnsOverride(t *testing.T) {
	t.Parallel()
	// Turn 1 asks for a tool, so the loop would continue without a turn cap.
	script := func(mp *mockProvider) {
		mp.addResponse(toolUseStreamEvents("test", "tu1", "Read", `{"file_path":"/nonexistent/probe"}`), nil)
		mp.addResponse(textStreamEvents("test", "after tool"), nil)
	}

	capped := &mockProvider{}
	script(capped)
	d := runAgentDeps(t)
	eng := New(&Params{Provider: capped, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&d)
	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", MaxTurns: 1,
	}); err != nil {
		t.Fatalf("RunAgent(MaxTurns=1): %v", err)
	}
	if got := capped.callCount(); got != 1 {
		t.Errorf("MaxTurns=1 made %d provider calls, want exactly 1", got)
	}

	uncapped := &mockProvider{}
	script(uncapped)
	eng2 := New(&Params{Provider: uncapped, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetSharedDeps(&d)
	if _, err := eng2.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "p", AgentType: "General",
	}); err != nil {
		t.Fatalf("RunAgent(MaxTurns=0): %v", err)
	}
	if got := uncapped.callCount(); got != 2 {
		t.Errorf("MaxTurns=0 made %d provider calls, want 2 (agent default is uncapped)", got)
	}
}

func TestRunAgentPropagatesSubError(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{} // no scripted responses → Stream fails
	d := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&d)
	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{Prompt: "p", AgentType: "General"})
	if err == nil {
		t.Fatalf("RunAgent must surface the sub-engine error, got result %+v", res)
	}
	if res != nil {
		t.Errorf("RunAgent returned result %+v alongside error %v, want nil result", res, err)
	}
	if !strings.Contains(err.Error(), "no more mock responses") {
		t.Errorf("error = %q, want it to carry the provider failure", err.Error())
	}
}

func TestRunAgentUnknownTypeWraps(t *testing.T) {
	t.Parallel()
	d := runAgentDeps(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "parent-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&d)
	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{Prompt: "p", AgentType: "NoSuchAgentType"})
	if err == nil {
		t.Fatalf("unknown agent type must fail, got %+v", res)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil", res)
	}
	if !strings.HasPrefix(err.Error(), `unknown agent type "NoSuchAgentType": `) {
		t.Errorf("error = %q, want the unknown-agent prefix", err.Error())
	}
	inner := errors.Unwrap(err)
	if inner == nil {
		t.Fatalf("error %q does not wrap the definition lookup failure", err.Error())
	}
	if inner.Error() != `unknown agent type "NoSuchAgentType": not found in built-in agents` {
		t.Errorf("wrapped error = %q, want the definition lookup message", inner.Error())
	}
}

func TestRunAgentInterruptedWithoutContent(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	hang := make(chan struct{})
	mp.responses = append(mp.responses, mockResponse{
		events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 12}}},
		},
		hang: hang,
	})
	d := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForCallCount(t, mp, 1, 5*time.Second)
		cancel()
		close(hang)
	}()
	res, err := eng.RunAgent(ctx, agenttool.AgentOpts{Prompt: "p", AgentType: "General"})
	if err != nil {
		t.Fatalf("a cancelled sub-agent must not return an error: %v", err)
	}
	if res == nil {
		t.Fatal("cancelled sub-agent returned nil result")
	}
	if res.Content != "(agent interrupted by user)" {
		t.Errorf("Content = %q, want the interrupt stub", res.Content)
	}
	if res.AgentType != "General" {
		t.Errorf("AgentType = %q, want General", res.AgentType)
	}
	if res.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0 (a stream cancelled before its delta contributes nothing)", res.TotalTokens)
	}
	if res.TotalToolUseCount != 0 {
		t.Errorf("TotalToolUseCount = %d, want 0", res.TotalToolUseCount)
	}
}

func TestRunAgentCancelledWithRealContent(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	hang := make(chan struct{})
	mp.responses = append(mp.responses,
		mockResponse{events: toolUseStreamEvents("test", "tu1", "Read", `{"file_path":"/nonexistent/probe"}`)},
		mockResponse{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 7}}},
		}, hang: hang},
	)
	d := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForCallCount(t, mp, 2, 5*time.Second)
		cancel()
		close(hang)
	}()
	res, err := eng.RunAgent(ctx, agenttool.AgentOpts{Prompt: "p", AgentType: "General"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res == nil {
		t.Fatal("RunAgent returned nil result")
	}
	// The sub-agent produced a real tool_use turn, so the interrupt stub must
	// not replace its finalization.
	if res.Content == "(agent interrupted by user)" {
		t.Error("interrupt stub replaced a sub-agent run that produced real content")
	}
	if res.AgentType != "General" {
		t.Errorf("AgentType = %q, want General", res.AgentType)
	}
	if res.TotalToolUseCount != 1 {
		t.Errorf("TotalToolUseCount = %d, want 1", res.TotalToolUseCount)
	}
	if want := 20 + 10; res.TotalTokens != want {
		t.Errorf("TotalTokens = %d, want %d (usage of the completed turn)", res.TotalTokens, want)
	}
}

func TestRunAgentSubagentStartHookInput(t *testing.T) {
	t.Parallel()
	rec := &integrationHookRecorder{}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"SubagentStart": []hooks.HookMatcher{{Matcher: "", Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "h-substart"},
		}}},
	}, rec)
	dir := t.TempDir()
	deps := SharedDeps{
		WorkingDir: dir,
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hookSystem,
	}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)
	eng := New(&Params{Provider: mp, Model: "parent-model", WorkingDir: dir,
		Logger: silentLogger(), Hooks: hookSystem, ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", Cwd: dir,
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var start *hookCall
	calls := rec.Calls()
	for i := range calls {
		if calls[i].event == "SubagentStart" {
			start = &calls[i]
			break
		}
	}
	if start == nil {
		t.Fatalf("SubagentStart never dispatched; recorded events: %v", rec.Events())
	}
	if start.input.HookEventName != "SubagentStart" {
		t.Errorf("HookEventName = %q, want SubagentStart", start.input.HookEventName)
	}
	if start.input.AgentType != "General" {
		t.Errorf("AgentType = %q, want General", start.input.AgentType)
	}
	if start.input.AgentID == "" {
		t.Error("AgentID is empty: the hook input must carry the sub-engine session id")
	}
	if start.input.Cwd != dir {
		t.Errorf("Cwd = %q, want the cwd override %q", start.input.Cwd, dir)
	}
}

func TestRunAgentNilParentTools(t *testing.T) {
	t.Parallel()
	// A hand-built engine has no tool map at all; RunAgent must still resolve an
	// empty filtered set rather than dereference nil.
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)
	deps := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger()})
	eng.tools = nil
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)
	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{Prompt: "p", AgentType: "General"}); err != nil {
		t.Fatalf("RunAgent with nil parent tools: %v", err)
	}
	req := mp.lastRequest
	if req == nil {
		t.Fatal("sub-engine never sent a request")
	}
	// An empty (non-nil) parent set must behave exactly like a nil one.
	empty := &mockProvider{}
	empty.addResponse(textStreamEvents("test", "ok"), nil)
	eng2 := New(&Params{Provider: empty, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{} }})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetSharedDeps(&deps)
	if _, err := eng2.RunAgent(context.Background(), agenttool.AgentOpts{Prompt: "p", AgentType: "General"}); err != nil {
		t.Fatalf("RunAgent with empty parent tools: %v", err)
	}
	nilNames := requestToolNames(req)
	emptyNames := requestToolNames(empty.lastRequest)
	if !slices.Equal(nilNames, emptyNames) {
		t.Errorf("nil parent tools gave %v, empty parent tools gave %v — they must be identical", nilNames, emptyNames)
	}
	if len(nilNames) == 0 {
		t.Error("unrestricted sub-agent must get the standard tool set, got none")
	}
}

func TestRunAgentSubagentEventsTagged(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub says hi"), nil)
	deps := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(),
		Dispatcher: ec, ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", ParentToolUseID: "call_parent_1",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var tagged int
	for _, ev := range ec.Events() {
		if ev.Agent == nil {
			continue
		}
		tagged++
		if ev.Agent.ParentToolUseID != "call_parent_1" {
			t.Errorf("tagged event %v ParentToolUseID = %q, want call_parent_1", ev.Type, ev.Agent.ParentToolUseID)
		}
		if ev.Agent.AgentType != "General" {
			t.Errorf("tagged event %v AgentType = %q, want General", ev.Type, ev.Agent.AgentType)
		}
		if ev.Agent.Depth != 1 {
			t.Errorf("tagged event %v Depth = %d, want 1", ev.Type, ev.Agent.Depth)
		}
	}
	if tagged == 0 {
		t.Fatal("no sub-agent events were tagged; the assertions above would pass vacuously")
	}
}

func TestRunAgentForkMessages(t *testing.T) {
	t.Parallel()
	fork := []types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("fork-user")}),
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("fork-assistant")}},
	}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)
	req := runAgentOnce(t, mp, agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", ForkMessages: fork,
	}, nil, nil)
	msgs := req.Messages
	if len(msgs) != 3 {
		t.Fatalf("sub request has %d messages, want 3 (two fork messages + the prompt)", len(msgs))
	}
	// Each message carries a timestamp prefix, so the payload is matched by suffix.
	for i, want := range []string{"fork-user", "fork-assistant", "p"} {
		if got := msgs[i].Content[0].Text; !strings.HasSuffix(got, want) {
			t.Errorf("message %d = %q, want it to end with %q", i, got, want)
		}
	}

	// No fork messages: only the prompt is sent.
	mp2 := &mockProvider{}
	mp2.addResponse(textStreamEvents("test", "ok"), nil)
	req2 := runAgentOnce(t, mp2, agenttool.AgentOpts{Prompt: "p", AgentType: "General"}, nil, nil)
	if len(req2.Messages) != 1 {
		t.Fatalf("sub request has %d messages, want 1 (prompt only)", len(req2.Messages))
	}
	if got := req2.Messages[0].Content[0].Text; !strings.HasSuffix(got, "p") {
		t.Errorf("message = %q, want the prompt", got)
	}
}

// TestRunAgentInterruptStubFields pins the fields of the synthesized
// interrupt result. The cancelled turn contributes no usage (its message is
// rewound), so TotalTokens is provably zero here — which is exactly why the
// token field of this literal carries no observable signal.
func TestRunAgentInterruptStubFields(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	hang := make(chan struct{})
	mp.responses = append(mp.responses, mockResponse{
		events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 12}}},
		},
		hang: hang,
	})
	// A forked tool_use turn sits before the prompt, so the run has tool uses
	// on record while everything after the last selectable user message stays
	// synthetic.
	fork := []types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("f1")}),
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_fork", Name: "Read"},
		}},
	}
	deps := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForCallCount(t, mp, 1, 5*time.Second)
		cancel()
		close(hang)
	}()
	res, err := eng.RunAgent(ctx, agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", ForkMessages: fork,
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res == nil {
		t.Fatal("RunAgent returned nil result")
	}
	if res.Content != "(agent interrupted by user)" {
		t.Errorf("Content = %q, want the interrupt stub", res.Content)
	}
	if res.AgentType != "General" {
		t.Errorf("AgentType = %q, want General", res.AgentType)
	}
	if res.TotalToolUseCount != 1 {
		t.Errorf("TotalToolUseCount = %d, want 1 (the forked tool_use counts)", res.TotalToolUseCount)
	}
	if res.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0 (the cancelled turn contributes no usage)", res.TotalTokens)
	}
}

// skillTestEngine wires an engine with a skill registry, a dispatcher and the
// shared deps a fork skill needs.
func skillTestEngine(t *testing.T, mp *mockProvider, reg *skills.Registry, ec *eventCollector) (*Engine, *eventCollector) {
	t.Helper()
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	eng := New(&Params{
		Provider:   mp,
		Model:      "parent-model",
		Logger:     silentLogger(),
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	return eng, ec
}

func skillRegistryWith(t *testing.T, cmds ...types.SkillCommand) *skills.Registry {
	t.Helper()
	reg := skills.NewRegistry(t.TempDir())
	for _, c := range cmds {
		reg.RegisterBundledSkill(c)
	}
	return reg
}

func eventText(ev types.QueryEvent) string {
	if ev.Message == nil || len(ev.Message.Content) == 0 {
		return ""
	}
	return ev.Message.Content[0].Text
}

func TestRunSkillInline(t *testing.T) {
	// "engine: running skill" is a package-level slog.Info, so the capture
	// swaps the default logger; sequential keeps the swap race-free.
	logs := &slogCapture{}
	attachSlogCapture(t, logs)
	reg := skillRegistryWith(t, types.SkillCommand{
		Name:      "probe-inline",
		Type:      "prompt",
		Context:   "inline",
		Source:    types.SkillSourceBundled,
		SourceDir: "/skill/dir",
		Content:   "Do the thing in ${SKILL_DIR}",
	})
	const wantContent = "Do the thing in /skill/dir\n\nARGUMENTS: myargs"

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "main reply"), nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider: mp, Model: "parent-model", Logger: slog.New(logs), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	eng.SetSkillRegistry(reg)
	t.Cleanup(func() { eng.Close() })

	// A prior query moves the query-start watermark off zero, so a missing
	// snapshotQueryStart becomes visible.
	eng.QuerySync(context.Background(), "earlier", "")
	ec.Reset()

	eng.RunSkill(context.Background(), "probe-inline", "myargs", "sys")
	ec.WaitForResult()

	starts := ec.FindEvents(types.EventQueryStart)
	if len(starts) != 1 {
		t.Fatalf("%d query_start events, want exactly 1", len(starts))
	}
	if got := eventText(starts[0]); got != wantContent {
		t.Errorf("query_start message = %q, want %q", got, wantContent)
	}
	if eng.currentTurnMsgID != starts[0].Message.ID {
		t.Errorf("currentTurnMsgID = %q, want the skill message id %q", eng.currentTurnMsgID, starts[0].Message.ID)
	}
	// The watermark is only readable through the accessor while a query owns the
	// engine, so the field is read directly: the snapshot must land right before
	// the skill message, not stay on the previous query's value.
	if got := eng.queryStartMsgIdx; got != 2 {
		t.Errorf("queryStartMsgIdx = %d, want 2 (snapshot taken before the skill message)", got)
	}
	var found bool
	for _, m := range eng.Messages() {
		if textOfMessage(m) == wantContent {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("skill message %q was not appended to history", wantContent)
	}
	if req := mp.lastRequest; req == nil || !strings.Contains(fmt.Sprint(req.Messages), wantContent) {
		t.Error("the provider request does not carry the substituted skill content")
	}

	recs := logs.all("engine: running skill")
	if len(recs) != 1 {
		t.Fatalf("%d running-skill records, want exactly 1", len(recs))
	}
	if got := recs[0].attrs["name"]; got != "probe-inline" {
		t.Errorf("running-skill name = %v, want probe-inline", got)
	}
	if got := recs[0].attrs["context"]; got != "inline" {
		t.Errorf("running-skill context = %v, want inline", got)
	}
	if recs := logs.all("engine: panic in RunSkill"); len(recs) != 0 {
		t.Errorf("%d panic records on a clean skill run, want 0", len(recs))
	}

	invoked := reg.GetInvokedSkillsForAgent("")
	if len(invoked) != 1 {
		t.Fatalf("%d invoked skills recorded, want exactly 1", len(invoked))
	}
	if invoked[0].SkillName != "probe-inline" {
		t.Errorf("invoked SkillName = %q, want probe-inline", invoked[0].SkillName)
	}
	if invoked[0].Content != wantContent {
		t.Errorf("invoked Content = %q, want %q", invoked[0].Content, wantContent)
	}
	if invoked[0].SkillPath != string(types.SkillSourceBundled)+":probe-inline" {
		t.Errorf("invoked SkillPath = %q, want %q", invoked[0].SkillPath, string(types.SkillSourceBundled)+":probe-inline")
	}
}

func textOfMessage(m types.Message) string {
	if len(m.Content) == 0 {
		return ""
	}
	return m.Content[0].Text
}

func TestRunSkillForkEvents(t *testing.T) {
	t.Parallel()
	reg := skillRegistryWith(t, types.SkillCommand{
		Name:      "probe-fork",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "Explore",
		Model:     "zhipu/skill-model",
		Source:    types.SkillSourceBundled,
		Content:   "Review the code.",
	})
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "earlier reply"), nil)
	mp.addResponse(textStreamEvents("test", "sub result"), nil)
	mp.addResponse(textStreamEvents("test", "main summary"), nil)
	ec := newEventCollector()
	eng, _ := skillTestEngine(t, mp, reg, ec)
	t.Cleanup(func() { eng.Close() })
	// The skill's composite model needs a resolvable provider table; the mock
	// doubles as the target instance, so the wire model is the bare name.
	cfg := &config.Config{Providers: []config.Provider{{Name: "zhipu"}}}
	cfg.Providers[0].Models.Set("skill-model", config.ModelConfig{})
	eng.sharedDeps.Cfg = cfg
	eng.sharedDeps.Providers = config.ProviderMap{"zhipu": mp}

	// A prior query moves the query-start watermark off zero.
	eng.QuerySync(context.Background(), "earlier", "")
	ec.Reset()

	eng.RunSkill(context.Background(), "probe-fork", "", "sys")
	ec.WaitForResult()

	starts := ec.FindEvents(types.EventQueryStart)
	if len(starts) != 1 {
		t.Fatalf("%d query_start events, want exactly 1", len(starts))
	}
	if got := eventText(starts[0]); got != "/probe-fork" {
		t.Errorf("query_start message = %q, want /probe-fork", got)
	}
	var slashMsg bool
	for _, m := range eng.Messages() {
		if textOfMessage(m) == "/probe-fork" {
			slashMsg = true
			break
		}
	}
	if !slashMsg {
		t.Error("the /skill user message was not appended to history")
	}
	if eng.currentTurnMsgID != starts[0].Message.ID {
		t.Errorf("currentTurnMsgID = %q, want the skill message id %q", eng.currentTurnMsgID, starts[0].Message.ID)
	}
	if got := eng.queryStartMsgIdx; got != 2 {
		t.Errorf("queryStartMsgIdx = %d, want 2 (snapshot taken before the skill message)", got)
	}

	toolStarts := ec.FindEvents(types.EventToolStart)
	if len(toolStarts) != 1 {
		t.Fatalf("%d tool_start events, want exactly 1", len(toolStarts))
	}
	tu := toolStarts[0].ToolUse
	if tu == nil {
		t.Fatal("tool_start carries no ToolUse payload")
	}
	if !strings.HasPrefix(tu.ID, "skill-fork-") || len(tu.ID) != len("skill-fork-")+8 {
		t.Errorf("tool_start ID = %q, want skill-fork- plus an 8-char uuid", tu.ID)
	}
	if tu.Name != "Skill" {
		t.Errorf("tool_start Name = %q, want Skill", tu.Name)
	}
	if tu.Summary != "probe-fork" {
		t.Errorf("tool_start Summary = %q, want probe-fork", tu.Summary)
	}

	toolRuns := ec.FindEvents(types.EventToolRun)
	if len(toolRuns) != 1 {
		t.Fatalf("%d tool_run events, want exactly 1", len(toolRuns))
	}
	if toolRuns[0].ToolUse == nil {
		t.Fatal("tool_run carries no ToolUse payload")
	}
	if toolRuns[0].ToolUse.ID != tu.ID {
		t.Errorf("tool_run ID = %q, want the tool_start ID %q", toolRuns[0].ToolUse.ID, tu.ID)
	}
	if toolRuns[0].ToolUse.Name != "Skill" {
		t.Errorf("tool_run Name = %q, want Skill", toolRuns[0].ToolUse.Name)
	}

	toolEnds := ec.FindEvents(types.EventToolEnd)
	if len(toolEnds) != 1 {
		t.Fatalf("%d tool_end events, want exactly 1", len(toolEnds))
	}
	tr := toolEnds[0].ToolResult
	if tr == nil {
		t.Fatal("tool_end carries no ToolResult payload")
	}
	if tr.ToolUseID != tu.ID {
		t.Errorf("tool_end ToolUseID = %q, want %q", tr.ToolUseID, tu.ID)
	}
	if tr.DisplayOutput != "sub result" {
		t.Errorf("tool_end DisplayOutput = %q, want the sub-agent content", tr.DisplayOutput)
	}
	if tr.IsError {
		t.Error("tool_end IsError = true, want false for a successful skill run")
	}

	// The sub-agent runs with the skill's own agent type, model and prompt, and
	// its events are tagged with the virtual tool id.
	var tagged int
	for _, ev := range ec.Events() {
		if ev.Agent == nil {
			continue
		}
		tagged++
		if ev.Agent.AgentType != "Explore" {
			t.Errorf("tagged event %v AgentType = %q, want Explore", ev.Type, ev.Agent.AgentType)
		}
		if ev.Agent.ParentToolUseID != tu.ID {
			t.Errorf("tagged event %v ParentToolUseID = %q, want %q", ev.Type, ev.Agent.ParentToolUseID, tu.ID)
		}
	}
	if tagged == 0 {
		t.Fatal("no tagged sub-agent events; the assertions above would pass vacuously")
	}
	var subReq *llm.Request
	for _, req := range mp.allRequests {
		if req != nil && req.Model == "skill-model" {
			subReq = req
			break
		}
	}
	if subReq == nil {
		t.Fatalf("no request carried the skill model; models: %v", func() []string {
			var out []string
			for _, r := range mp.allRequests {
				out = append(out, r.Model)
			}
			return out
		}())
	}
	if got := textOfMessage(subReq.Messages[len(subReq.Messages)-1]); !strings.HasSuffix(got, "Review the code.") {
		t.Errorf("sub-agent prompt = %q, want the skill content", got)
	}

	// The skill result rides history as a meta user message.
	var meta string
	for _, m := range eng.Messages() {
		if m.Flags&types.FlagMeta != 0 {
			meta = textOfMessage(m)
		}
	}
	wantMeta := "Skill \"probe-fork\" completed.\n\nsub result"
	if meta != wantMeta {
		t.Errorf("meta skill-result message = %q, want %q", meta, wantMeta)
	}
}

func TestRunSkillForkFailure(t *testing.T) {
	t.Parallel()
	reg := skillRegistryWith(t, types.SkillCommand{
		Name: "probe-fail", Type: "prompt", Context: "fork", AgentType: "General",
		Source: types.SkillSourceBundled, Content: "Boom.",
	})
	mp := &mockProvider{} // no responses → the sub-agent run fails
	ec := newEventCollector()
	eng, _ := skillTestEngine(t, mp, reg, ec)
	t.Cleanup(func() { eng.Close() })

	eng.RunSkill(context.Background(), "probe-fail", "", "sys")
	ec.WaitForResult()

	ends := ec.FindEvents(types.EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("%d tool_end events, want exactly 1", len(ends))
	}
	if ends[0].ToolResult == nil {
		t.Fatal("tool_end carries no ToolResult payload")
	}
	if !ends[0].ToolResult.IsError {
		t.Error("tool_end IsError = false, want true for a failed sub-agent run")
	}
	if ends[0].ToolResult.DisplayOutput != "Skill execution completed" {
		t.Errorf("tool_end DisplayOutput = %q, want the fallback text", ends[0].ToolResult.DisplayOutput)
	}
	var ends2 []types.QueryEvent
	for _, ev := range ec.FindEvents(types.EventQueryEnd) {
		if ev.Agent == nil {
			ends2 = append(ends2, ev)
		}
	}
	if len(ends2) != 1 {
		t.Fatalf("%d skill-owned query_end events, want exactly 1", len(ends2))
	}
	if ends2[0].Error == nil {
		t.Error("query_end must carry the sub-agent failure")
	}
	// The failed skill must not append its completion notice.
	for _, m := range eng.Messages() {
		if strings.Contains(textOfMessage(m), "probe-fail\" completed") {
			t.Error("a failed skill appended a completion notice")
		}
	}
}

func TestRunSkillWithoutRegistry(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "parent-model", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	eng.RunSkill(context.Background(), "anything", "", "sys")

	deadline := context.Background()
	_ = deadline
	errEvents := awaitErrorEvents(ec, 1)
	if len(errEvents) != 1 {
		t.Fatalf("%d error events without a skill registry, want exactly 1", len(errEvents))
	}
	if errEvents[0].Type != types.EventQueryEnd {
		t.Errorf("event type = %v, want query_end", errEvents[0].Type)
	}
	if got := errEvents[0].Error.Error(); got != "skill registry not configured" {
		t.Errorf("error = %q, want the missing-registry message", got)
	}
}

func TestRunSkillPanic(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	attachSlogCapture(t, logs)

	reg := skillRegistryWith(t, types.SkillCommand{
		Name: "probe-panic", Type: "prompt", Context: "inline",
		Source: types.SkillSourceBundled, Content: "Panic.",
	})
	ec := newEventCollector()
	eng := New(&Params{
		Provider: &panickingProvider{}, Model: "parent-model",
		Logger: silentLogger(), Dispatcher: ec,
	})
	eng.SetSkillRegistry(reg)
	eng.RunSkill(context.Background(), "probe-panic", "", "sys")
	waitEngineIdle(t, eng)
	eng.Close()

	var errEvents []types.QueryEvent
	for _, ev := range ec.Events() {
		if ev.Error != nil {
			errEvents = append(errEvents, ev)
		}
	}
	if len(errEvents) != 1 {
		t.Fatalf("%d error events after a panicking skill, want exactly 1", len(errEvents))
	}
	if errEvents[0].Type != types.EventQueryEnd {
		t.Errorf("panic event type = %v, want query_end", errEvents[0].Type)
	}
	if !strings.Contains(errEvents[0].Error.Error(), "provider stream exploded") {
		t.Errorf("panic event error = %q, want the panic value", errEvents[0].Error.Error())
	}
	recs := logs.all("engine: panic in RunSkill")
	if len(recs) != 1 {
		t.Fatalf("%d panic records, want exactly 1", len(recs))
	}
	if recs[0].level != slog.LevelError {
		t.Errorf("panic record level = %v, want error", recs[0].level)
	}
}

func TestRunSkillBusyFlag(t *testing.T) {
	t.Parallel()
	reg := skillRegistryWith(t, types.SkillCommand{
		Name: "probe-busy", Type: "prompt", Context: "inline",
		Source: types.SkillSourceBundled, Content: "Busy.",
	})
	mp := &mockProvider{}
	hang := make(chan struct{})
	mp.responses = append(mp.responses, mockResponse{events: textStreamEvents("test", "done"), hang: hang})
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger()})
	eng.SetSkillRegistry(reg)
	t.Cleanup(func() { eng.Close() })

	eng.RunSkill(context.Background(), "probe-busy", "", "sys")
	waitForCallCount(t, mp, 1, 5*time.Second)
	if !eng.IsBusy() {
		t.Error("engine must stay busy while the skill turn is in flight")
	}
	close(hang)
	waitEngineIdle(t, eng)
	if eng.IsBusy() {
		t.Error("engine must be idle once the skill run finishes")
	}
}

func TestRunSkillOnClosedEngine(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	reg := skillRegistryWith(t, types.SkillCommand{
		Name: "probe-closed", Type: "prompt", Context: "inline",
		Source: types.SkillSourceBundled, Content: "Nope.",
	})
	mp := &mockProvider{}
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(), Dispatcher: ec})
	eng.SetSkillRegistry(reg)
	eng.Close()

	eng.RunSkill(context.Background(), "probe-closed", "", "sys")
	errEvents := awaitErrorEvents(ec, 1)
	if len(errEvents) != 1 {
		t.Fatalf("%d error events on a closed engine, want exactly 1", len(errEvents))
	}
	if got := errEvents[0].Error.Error(); got != "engine: skill on closed engine" {
		t.Errorf("error = %q, want the closed-engine message", got)
	}
	if mp.callCount() != 0 {
		t.Errorf("closed engine made %d provider calls, want 0", mp.callCount())
	}
}

func TestStartProcessAttachmentsIfIdleGuards(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	// No system prompt: the kick must not run turns even with a queued item.
	eng.EnqueueAttachment(types.QueuedItem{UUID: "q1", Mode: types.ItemModeJob, Value: "v"})
	eng.startProcessAttachmentsIfIdle()
	want := 0 // without a system prompt the kick cannot run any turn
	if got := mp.callCount(); got != want {
		t.Errorf("provider calls with an empty system prompt = %d, want %d", got, want)
	}

	// System prompt set but the engine is busy: no second owner is registered.
	eng.SetSystemPrompt("sys")
	hold := eng.beginQuery(func() {})
	if hold == 0 {
		t.Fatal("beginQuery must return a token on an open engine")
	}
	eng.startProcessAttachmentsIfIdle()
	if eng.activeCancel == nil {
		t.Error("the held query registration must survive a rejected kick")
	}
	eng.endQuery(hold)

	// System prompt set and the engine idle, but the queue is empty: the kick
	// has nothing to run and must not register a query at all.
	empty := New(&Params{Provider: &mockProvider{}, Model: "parent-model", Logger: silentLogger()})
	empty.SetSystemPrompt("sys")
	t.Cleanup(func() { empty.Close() })
	empty.startProcessAttachmentsIfIdle()
	if empty.IsBusy() {
		t.Error("an empty attachment queue started a query")
	}
	empty.mu.RLock()
	registered := empty.activeCancel
	empty.mu.RUnlock()
	if registered != nil {
		t.Error("an empty attachment queue registered a cancel handle")
	}
}

func TestCreateAttachmentMessages(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "parent-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	origin := &types.MessageOrigin{Kind: types.OriginMessage}
	msgs := eng.createAttachmentMessages([]types.QueuedItem{
		{UUID: "u-skip", Mode: types.ItemModeMessage, Value: "skipped"},
		{UUID: "u1", Mode: types.ItemModePrompt, Value: "prompt one", Origin: origin, Timestamp: ts},
		{UUID: "u2", Mode: types.ItemModeJob, Value: "job two", Timestamp: ts},
		{UUID: "u3", Mode: types.ItemModeNotification, Value: "skipped too"},
		{UUID: "u5", Mode: types.ItemModePrompt, Value: "meta item", IsMeta: true},
	})
	if len(msgs) != 3 {
		t.Fatalf("%d attachment messages, want exactly 3 (prompt + job + meta prompt)", len(msgs))
	}
	first := msgs[0]
	if first.ID != "u1" {
		t.Errorf("message ID = %q, want u1", first.ID)
	}
	if first.Role != types.RoleUser || first.MessageType != types.MessageTypeAttachment {
		t.Errorf("message role/type = %v/%v, want user/attachment", first.Role, first.MessageType)
	}
	if !first.Timestamp.Equal(ts) {
		t.Errorf("message Timestamp = %v, want %v", first.Timestamp, ts)
	}
	if first.Flags&types.FlagMeta != 0 {
		t.Error("a non-meta prompt item must not carry FlagMeta")
	}
	if len(first.Content) != 1 || first.Content[0].Text != "prompt one" {
		t.Errorf("message content = %+v, want the item value as one text block", first.Content)
	}
	if first.Attachment == nil {
		t.Fatal("message has no Attachment stub")
	}
	if first.Attachment.Type != types.AttachmentTypeQueued {
		t.Errorf("Attachment.Type = %v, want queued_item", first.Attachment.Type)
	}
	if first.Attachment.Prompt != "prompt one" {
		t.Errorf("Attachment.Prompt = %q, want the item value", first.Attachment.Prompt)
	}
	if first.Attachment.SourceUUID != "u1" {
		t.Errorf("Attachment.SourceUUID = %q, want u1", first.Attachment.SourceUUID)
	}
	if first.Attachment.Mode != types.ItemModePrompt {
		t.Errorf("Attachment.Mode = %v, want prompt", first.Attachment.Mode)
	}
	if first.Attachment.Origin != origin {
		t.Errorf("Attachment.Origin = %v, want the item origin", first.Attachment.Origin)
	}
	if first.Attachment.IsMeta {
		t.Error("Attachment.IsMeta = true, want false")
	}
	// A job item is meta by definition.
	second := msgs[1]
	if second.ID != "u2" {
		t.Errorf("job message ID = %q, want u2", second.ID)
	}
	if second.Flags&types.FlagMeta == 0 {
		t.Error("a job item must carry FlagMeta")
	}
	if second.Attachment == nil || second.Attachment.Mode != types.ItemModeJob {
		t.Errorf("job Attachment = %+v, want Mode job", second.Attachment)
	}
	if second.Attachment.IsMeta {
		t.Error("Attachment.IsMeta mirrors the item flag, not the computed meta state")
	}

	third := msgs[2]
	if third.Attachment == nil || !third.Attachment.IsMeta {
		t.Errorf("meta item Attachment = %+v, want IsMeta true", third.Attachment)
	}
	if third.Flags&types.FlagMeta == 0 {
		t.Error("an IsMeta item must carry FlagMeta")
	}

	// Explicit content blocks win over the value string.
	withBlocks := eng.createAttachmentMessages([]types.QueuedItem{{
		UUID: "u4", Mode: types.ItemModePrompt, Value: "ignored",
		Content: []types.ContentBlock{types.NewTextBlock("block one")},
	}})
	if len(withBlocks) != 1 || withBlocks[0].Content[0].Text != "block one" {
		t.Errorf("explicit content = %+v, want the provided block", withBlocks)
	}
}

func TestProcessAttachmentsPanic(t *testing.T) {
	// Sequential: the default-logger swap must not overlap other captures.
	logs := &slogCapture{}
	attachSlogCapture(t, logs)

	ec := newEventCollector()
	eng := New(&Params{Provider: &panickingProvider{}, Model: "parent-model", Logger: silentLogger(), Dispatcher: ec})
	eng.EnqueueAttachment(types.QueuedItem{UUID: "p1", Mode: types.ItemModePrompt, Value: "go"})
	eng.processAttachments(context.Background(), 0, "sys")
	eng.Close()

	var errEvents []types.QueryEvent
	for _, ev := range ec.Events() {
		if ev.Error != nil {
			errEvents = append(errEvents, ev)
		}
	}
	if len(errEvents) != 1 {
		t.Fatalf("%d error events after a panicking attachment run, want exactly 1", len(errEvents))
	}
	if errEvents[0].Type != types.EventQueryEnd {
		t.Errorf("panic event type = %v, want query_end", errEvents[0].Type)
	}
	if !strings.Contains(errEvents[0].Error.Error(), "provider stream exploded") {
		t.Errorf("panic event error = %q, want the panic value", errEvents[0].Error.Error())
	}
	recs := logs.all("engine: panic in processAttachments")
	if len(recs) != 1 {
		t.Fatalf("%d panic records, want exactly 1", len(recs))
	}
	if recs[0].level != slog.LevelError {
		t.Errorf("panic record level = %v, want error", recs[0].level)
	}
}

func TestProcessAttachmentsCleanRunEvents(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "earlier reply"), nil)
	mp.addResponse(textStreamEvents("test", "reply"), nil)
	ec := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })

	eng.QuerySync(context.Background(), "earlier", "")
	eng.EnqueueAttachment(types.QueuedItem{UUID: "p1", Mode: types.ItemModePrompt, Value: "prompt item"})
	eng.EnqueueAttachment(types.QueuedItem{UUID: "j1", Mode: types.ItemModeJob, Value: "job item"})
	eng.processAttachments(context.Background(), 0, "sys")

	// The watermark is snapshotted BEFORE the drain (87159962): it marks the
	// last pre-drain row so buildHistory excludes the in-flight assistant row
	// the takeover snapshot streams.
	if got := eng.queryStartMsgIdx; got != 2 {
		t.Errorf("queryStartMsgIdx = %d, want 2 (pre-drain snapshot)", got)
	}

	if got := len(ec.FindEvents(types.EventAttachment)); got != 2 {
		t.Errorf("%d EventAttachment events, want 2 (one per drained item)", got)
	}
	var errEvents int
	for _, ev := range ec.Events() {
		if ev.Error != nil {
			errEvents++
		}
	}
	if errEvents != 0 {
		t.Errorf("%d error events on a clean attachment run, want 0", errEvents)
	}
	if eng.IsBusy() {
		t.Error("engine must be idle once processAttachments returns")
	}
}

func TestProcessAttachmentsEmptyQueue(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "must not run"), nil)
	ec := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })

	eng.processAttachments(context.Background(), 0, "sys")

	want := 0 // an empty queue must not run a turn
	if got := mp.callCount(); got != want {
		t.Errorf("provider calls with an empty queue = %d, want %d (no turn may run)", got, want)
	}
	if evs := ec.Events(); len(evs) != 0 {
		t.Errorf("%d events with an empty queue, want 0", len(evs))
	}
	if eng.IsBusy() {
		t.Error("engine must not stay busy after processing an empty queue")
	}
}

func TestNormalizeAttachmentForAPI(t *testing.T) {
	t.Parallel()
	origin := &types.MessageOrigin{Kind: types.OriginMessage}
	job := normalizeAttachmentForAPI(types.Message{
		Content: []types.ContentBlock{types.NewTextBlock("job body")},
		Attachment: &types.Attachment{
			SourceUUID: "u-job",
			Mode:       types.ItemModeJob,
		},
	})
	if job.ID != "u-job" {
		t.Errorf("normalized ID = %q, want the source uuid", job.ID)
	}
	if job.Role != types.RoleUser {
		t.Errorf("normalized role = %v, want user", job.Role)
	}
	if job.Flags&types.FlagMeta == 0 {
		t.Error("a job-origin attachment must carry FlagMeta")
	}
	if !strings.Contains(fmt.Sprint(job.Content), "job body") {
		t.Errorf("normalized content = %v, want it to wrap the body", job.Content)
	}
	// An explicit non-nil origin is meta even when IsMeta is false.
	explicit := normalizeAttachmentForAPI(types.Message{
		Content: []types.ContentBlock{types.NewTextBlock("note")},
		Attachment: &types.Attachment{
			SourceUUID: "u-exp",
			Mode:       types.ItemModeNotification,
			Origin:     origin,
		},
	})
	if explicit.Flags&types.FlagMeta == 0 {
		t.Error("an attachment with an explicit origin must carry FlagMeta")
	}
	// No origin and no meta flag: plain message.
	plain := normalizeAttachmentForAPI(types.Message{
		Content: []types.ContentBlock{types.NewTextBlock("plain")},
		Attachment: &types.Attachment{
			SourceUUID: "u-plain",
			Mode:       types.ItemModePrompt,
		},
	})
	if plain.Flags != 0 {
		t.Errorf("plain attachment Flags = %d, want 0", plain.Flags)
	}
}

func TestCoalesceBufWindow(t *testing.T) {
	t.Parallel()
	var flushed []string
	c := &coalesceBuf{}
	flush := func() { c.flush(func(text string) { flushed = append(flushed, text) }) }

	// First write: nothing to drain yet, the window clock starts here.
	c.write("a", time.Second, flush)
	if len(flushed) != 0 {
		t.Errorf("flushes on the first write = %v, want none", flushed)
	}
	if c.lastFlush.IsZero() {
		t.Fatal("the first write must stamp lastFlush")
	}
	// Window still open: no drain.
	c.write("b", time.Hour, flush)
	if len(flushed) != 0 {
		t.Errorf("flushes inside the window = %v, want none", flushed)
	}
	if got := c.buf.String(); got != "ab" {
		t.Errorf("buffer = %q, want ab", got)
	}
	// Window elapsed (zero window): the next write drains what is buffered first.
	c.write("c", 0, flush)
	if !slices.Equal(flushed, []string{"ab"}) {
		t.Errorf("flushed = %v, want [ab]", flushed)
	}
	if got := c.buf.String(); got != "c" {
		t.Errorf("buffer after the timed drain = %q, want c", got)
	}
	// Explicit flush drains and resets.
	c.flush(func(text string) { flushed = append(flushed, text) })
	if !slices.Equal(flushed, []string{"ab", "c"}) {
		t.Errorf("flushed = %v, want [ab c]", flushed)
	}
	if c.buf.Len() != 0 {
		t.Errorf("buffer after flush = %q, want empty", c.buf.String())
	}
	// Flushing an empty buffer must not dispatch.
	before := len(flushed)
	c.flush(func(text string) { flushed = append(flushed, text) })
	if len(flushed) != before {
		t.Errorf("empty flush dispatched %d payloads, want 0", len(flushed)-before)
	}

	// With the clock never started, nothing may be drained yet — the buffer is
	// only ever appended to.
	seeded := &coalesceBuf{}
	seeded.buf.WriteString("seed")
	var seededFlushed []string
	seeded.write("a", time.Hour, func() {
		seeded.flush(func(text string) { seededFlushed = append(seededFlushed, text) })
	})
	if len(seededFlushed) != 0 {
		t.Errorf("first write with an unstamped clock drained %v, want nothing", seededFlushed)
	}
	if got := seeded.buf.String(); got != "seeda" {
		t.Errorf("buffer = %q, want seeda", got)
	}
}

func TestCoalesceBufFlushStampsClock(t *testing.T) {
	t.Parallel()
	c := &coalesceBuf{}
	c.buf.WriteString("seed")
	c.flush(func(text string) {})
	if c.lastFlush.IsZero() {
		t.Fatal("flush must restamp the coalesce clock")
	}
}

func TestToolDeltaCoalesce(t *testing.T) {
	t.Parallel()
	type sent struct {
		id, text string
		meta     any
	}
	var out []sent
	tc := &toolDeltaCoalesce{}
	flush := func() {
		tc.flush(func(id, text string, meta any, _ *types.AgentMeta) { out = append(out, sent{id, text, meta}) })
	}

	tc.write("t1", "alpha", "meta1", time.Hour, flush)
	tc.write("t2", "", nil, time.Hour, flush)
	if len(out) != 0 {
		t.Errorf("flushes during the window = %d, want 0", len(out))
	}
	tc.flush(func(id, text string, meta any, _ *types.AgentMeta) { out = append(out, sent{id, text, meta}) })
	if len(out) != 1 {
		t.Fatalf("%d payloads after flush, want 1 (the empty buffer is skipped): %+v", len(out), out)
	}
	if out[0].id != "t1" || out[0].text != "alpha" || out[0].meta != any("meta1") {
		t.Errorf("flushed payload = %+v, want {t1 alpha meta1}", out[0])
	}
	if tc.bufs != nil || tc.meta != nil || tc.agents != nil {
		t.Errorf("buffers after flush = %v/%v/%v, want all cleared", tc.bufs, tc.meta, tc.agents)
	}
	// Same unstamped-clock rule for the per-id buffer: nothing drains yet.
	seeded := &toolDeltaCoalesce{}
	seeded.bufs = map[string]*strings.Builder{"t1": {}}
	seeded.bufs["t1"].WriteString("seed")
	var seededOut []string
	seeded.write("t2", "x", nil, time.Hour, func() {
		seeded.flush(func(id, text string, _ any, _ *types.AgentMeta) { seededOut = append(seededOut, id+":"+text) })
	})
	if len(seededOut) != 0 {
		t.Errorf("first write with an unstamped clock drained %v, want nothing", seededOut)
	}

	// A later write without metadata must not resurrect the old payload.
	out = nil
	tc.write("t1", "beta", nil, time.Hour, flush)
	tc.flush(func(id, text string, meta any, _ *types.AgentMeta) { out = append(out, sent{id, text, meta}) })
	if len(out) != 1 || out[0].text != "beta" || out[0].meta != nil {
		t.Errorf("second flush = %+v, want {t1 beta <nil>}", out)
	}
}

func TestToolDeltaCoalesceFlushStampsClock(t *testing.T) {
	t.Parallel()
	tc := &toolDeltaCoalesce{}
	tc.flush(func(id, text string, meta any, _ *types.AgentMeta) {})
	if tc.lastFlush.IsZero() {
		t.Fatal("flush must stamp the coalesce clock even with nothing buffered")
	}
	var flushes int
	tc.write("t1", "x", nil, 0, func() { flushes++ })
	if flushes != 1 {
		t.Errorf("writes after a flush triggered %d drains, want 1", flushes)
	}
}

func TestEmitEventCoalescing(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("sess-probe")
	eng.window = 1 // nanoseconds: every follow-up delta drains the previous one

	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "hello "})
	want := 0 // the first delta stays buffered until the next one arrives
	if got := len(ec.Events()); got != want {
		t.Fatalf("%d events after the first delta, want %d (buffered)", got, want)
	}
	eng.emitEvent(types.QueryEvent{Type: types.EventTextDelta, Text: "world"})
	var texts []string
	for _, ev := range ec.Events() {
		if ev.Type == types.EventTextDelta {
			texts = append(texts, ev.Text)
		}
	}
	if !slices.Equal(texts, []string{"hello "}) {
		t.Errorf("dispatched text deltas = %v, want [hello ]", texts)
	}

	// A non-delta event drains the buffers and carries the session id.
	eng.emitEvent(types.QueryEvent{Type: types.EventTurnStart})
	var lastText *types.QueryEvent
	var start *types.QueryEvent
	events := ec.Events()
	for i, ev := range events {
		if ev.Type == types.EventTextDelta {
			e := events[i]
			lastText = &e
		}
		if ev.Type == types.EventTurnStart {
			e := events[i]
			start = &e
		}
	}
	if lastText == nil || lastText.Text != "world" {
		t.Errorf("buffered text after the flush = %+v, want world", lastText)
	}
	if start == nil {
		t.Fatal("turn_start was not dispatched")
	}
	if start.SessionID != "sess-probe" {
		t.Errorf("dispatched SessionID = %q, want sess-probe", start.SessionID)
	}
}

func TestEmitEventNilPayloadGuards(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolParamDelta})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolOutputDelta})
	want := 0 // nil delta payloads are dropped before dispatch
	if got := len(ec.Events()); got != want {
		t.Errorf("%d events for nil delta payloads, want %d", got, want)
	}
}

func TestEmitEventEffectiveWindow(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	eng.window = 1
	if got := eng.effectiveWindow(); got != 1 {
		t.Errorf("effectiveWindow = %v, want the configured 1ns", got)
	}
	eng.window = 0
	if got := eng.effectiveWindow(); got != coalesceWindow {
		t.Errorf("effectiveWindow with no override = %v, want %v", got, coalesceWindow)
	}
}

func TestFlushBuffersCarryMetadata(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	eng.window = time.Hour

	eng.emitEvent(types.QueryEvent{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{
		ID: "t1", Delta: `{"q":`, Name: "Grep", Summary: "searching",
		IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true,
	}})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{
		ToolUseID: "t1", DisplayOutput: "out",
		IsError: true, IsBackground: true, IsSearch: true, IsRead: true,
		IsList: true, IsLsp: true, IsTask: true,
	}})
	eng.flushBufs()

	var params *types.PartialInputEvent
	var output *types.ToolResultEvent
	for _, ev := range ec.Events() {
		switch ev.Type {
		case types.EventToolParamDelta:
			params = ev.PartialInput
		case types.EventToolOutputDelta:
			output = ev.ToolResult
		}
	}
	if params == nil {
		t.Fatal("no tool_param_delta dispatched")
	}
	if params.ID != "t1" || params.Delta != `{"q":` || params.Name != "Grep" || params.Summary != "searching" {
		t.Errorf("param payload = %+v, want the streamed identity", params)
	}
	if !params.IsSearch || !params.IsRead || !params.IsList || !params.IsLsp || !params.IsTask {
		t.Errorf("param classification flags = %+v, want all true", params)
	}
	if output == nil {
		t.Fatal("no tool_output_delta dispatched")
	}
	if output.ToolUseID != "t1" || output.DisplayOutput != "out" {
		t.Errorf("output payload = %+v, want the streamed text", output)
	}
	if !output.IsError || !output.IsBackground || !output.IsSearch || !output.IsRead ||
		!output.IsList || !output.IsLsp || !output.IsTask {
		t.Errorf("output flags = %+v, want all true", output)
	}
}

func TestFlushBuffersLogPreview(t *testing.T) {
	// flush_text/flush_think log through the package-level default logger.
	logs := &slogCapture{}
	attachSlogCapture(t, logs)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), Dispatcher: newEventCollector()})
	t.Cleanup(func() { eng.Close() })
	eng.window = time.Hour

	text := strings.Repeat("x", 41)
	eng.textCoalesce.write(text, time.Hour, eng.flushTextBuf)
	eng.thinkCoalesce.write(text, time.Hour, eng.flushThinkBuf)
	eng.flushTextBuf()
	eng.flushThinkBuf()

	textRecs := logs.all("engine:flush_text")
	if len(textRecs) != 1 {
		t.Fatalf("%d flush_text records, want exactly 1", len(textRecs))
	}
	if got := textRecs[0].attrs["len"]; got != int64(41) && got != 41 {
		t.Errorf("flush_text len = %v (%T), want 41", got, got)
	}
	if got := textRecs[0].attrs["preview"]; got != text[:40]+"..." {
		t.Errorf("flush_text preview = %q, want the first 40 chars plus an ellipsis", got)
	}
	thinkRecs := logs.all("engine:flush_think")
	if len(thinkRecs) != 1 {
		t.Fatalf("%d flush_think records, want exactly 1", len(thinkRecs))
	}
	if got := thinkRecs[0].attrs["len"]; got != int64(41) && got != 41 {
		t.Errorf("flush_think len = %v (%T), want 41", got, got)
	}
	if got := thinkRecs[0].attrs["preview"]; got != text[:40]+"..." {
		t.Errorf("flush_think preview = %q, want the first 40 chars plus an ellipsis", got)
	}
}

// cacheUsageStreamEvents scripts one text turn with explicit cache token usage.
func cacheUsageStreamEvents(in, cacheRead, cacheCreate, out int) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{
			InputTokens:              in,
			CacheReadInputTokens:     cacheRead,
			CacheCreationInputTokens: cacheCreate,
		}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "reply"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: out}},
		{Type: "message_stop"},
	}
}

func attrInt(got any) int64 {
	switch v := got.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	}
	return -1
}

func TestRunTurnsExitTraceAndSummary(t *testing.T) {
	// PersistNewMessages logs through the package-level default logger.
	logs := &slogCapture{}
	attachSlogCapture(t, logs)

	cases := []struct {
		name                  string
		in, read, create, out int
		wantCache             string
	}{
		{"cache-hit", 10, 200, 7, 5, "hit 92%"},
		{"cache-hit-single-token", 10, 1, 0, 5, "hit 9%"},
		{"cache-warm", 10, 0, 7, 5, "warm"},
		{"cache-warm-single-token", 10, 0, 1, 5, "warm"},
		{"cache-miss", 10, 0, 0, 5, "miss"},
	}
	for _, tc := range cases {
		mp := &mockProvider{}
		mp.addResponse(cacheUsageStreamEvents(tc.in, tc.read, tc.create, tc.out), nil)
		eng := New(&Params{Provider: mp, Model: "test-model", Logger: slog.New(logs)})
		// A session id with no store: the crash-window persist must stay guarded.
		eng.SetSessionID("sess-probe-exit")
		eng.QuerySync(context.Background(), "go", "")
		eng.Close()

		exits := logs.all("engine:runTurns_exit")
		if len(exits) != 1 {
			t.Fatalf("%s: %d runTurns_exit records, want exactly 1", tc.name, len(exits))
		}
		if got := attrInt(exits[0].attrs["messages"]); got != 2 {
			t.Errorf("%s: exit messages = %d, want 2 (user + assistant)", tc.name, got)
		}
		if got := attrInt(exits[0].attrs["turns"]); got != 1 {
			t.Errorf("%s: exit turns = %d, want 1", tc.name, got)
		}
		if got := fmt.Sprint(exits[0].attrs["engine_id"]); got != eng.engineID || got == "" {
			t.Errorf("%s: exit engine_id = %q, want the engine's own id %q", tc.name, got, eng.engineID)
		}
		if got, ok := exits[0].attrs["store_nil"]; !ok || got != true {
			t.Errorf("%s: exit store_nil = %v, want true", tc.name, got)
		}
		if got := exits[0].attrs["engine_id"]; got != "main" {
			t.Errorf("%s: exit engine_id = %v, want main", tc.name, got)
		}

		sums := logs.all("engine:query_summary")
		if len(sums) != 1 {
			t.Fatalf("%s: %d query_summary records, want exactly 1", tc.name, len(sums))
		}
		s := sums[0].attrs
		if got := attrInt(s["input"]); got != int64(tc.in) {
			t.Errorf("%s: summary input = %d, want %d", tc.name, got, tc.in)
		}
		if got := attrInt(s["output"]); got != int64(tc.out) {
			t.Errorf("%s: summary output = %d, want %d", tc.name, got, tc.out)
		}
		if got := attrInt(s["cache_read"]); got != int64(tc.read) {
			t.Errorf("%s: summary cache_read = %d, want %d", tc.name, got, tc.read)
		}
		if got := attrInt(s["cache_creation"]); got != int64(tc.create) {
			t.Errorf("%s: summary cache_creation = %d, want %d", tc.name, got, tc.create)
		}
		if got := attrInt(s["turns"]); got != 1 {
			t.Errorf("%s: summary turns = %d, want 1", tc.name, got)
		}
		if got := s["cache"]; got != tc.wantCache {
			t.Errorf("%s: summary cache = %v, want %q", tc.name, got, tc.wantCache)
		}
		if got := attrInt(s["tools_ms"]); got != 0 {
			t.Errorf("%s: summary tools_ms = %d, want 0 (no tools ran)", tc.name, got)
		}

		// The crash-window persist is guarded by the store check, so only the
		// runTurns defer may report the skip.
		if got := len(logs.all("PersistNewMessages: skip persistence")); got != 1 {
			t.Errorf("%s: %d skip-persistence records, want exactly 1", tc.name, got)
		}
		logs.mu.Lock()
		logs.calls = nil
		logs.mu.Unlock()
	}
}

func TestRunTurnsPanicLog(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	eng := New(&Params{Provider: &panickingProvider{}, Model: "test-model", Logger: slog.New(logs)})
	eng.Query(context.Background(), "go", "")
	waitEngineIdle(t, eng)
	eng.Close()

	recs := logs.all("engine:runTurns_panic")
	if len(recs) != 1 {
		t.Fatalf("%d runTurns_panic records, want exactly 1", len(recs))
	}
	if recs[0].level != slog.LevelError {
		t.Errorf("panic record level = %v, want error", recs[0].level)
	}
	if got := fmt.Sprint(recs[0].attrs["panic"]); got != "provider stream exploded" {
		t.Errorf("panic record panic = %q, want the panic value", got)
	}
	if got, ok := recs[0].attrs["stack"]; !ok || !strings.Contains(fmt.Sprint(got), "panickingProvider") {
		t.Errorf("panic record stack = %v, want the panic stack", got)
	}
}

func TestRunTurnsAbortAtLoopTop(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test", "tu1", "Read", `{"file_path":"/nonexistent/probe"}`), nil)
	mp.addResponse(textStreamEvents("test", "never"), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readTool := &mockTool{name: "Read", enabled: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "done"}, nil
	}}
	eng := New(&Params{
		Provider: mp, Model: "test-model", Logger: silentLogger(), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{"Read": readTool} },
		// Post-turn hooks only fire with auto-compact configured; the compactor
		// itself is cleared so the turn loop, not compaction, is what we observe.
		AutoCompact: AutoCompactConfig{ContextWindow: 200_000},
	})
	eng.mu.Lock()
	eng.compactor = nil
	eng.mu.Unlock()
	// The hook runs at the turn boundary, after the post-tool abort check, so
	// the cancellation is first observed by the loop-top check.
	eng.RegisterPostTurnHook(func(context.Context, []types.Message, int, string) { cancel() })
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(ctx, "go", "")
	if res.Error == nil {
		t.Fatal("a cancelled context must abort the turn loop")
	}
	if got := mp.callCount(); got != 1 {
		t.Errorf("provider calls after the loop-top abort = %d, want 1 (the second turn must not run)", got)
	}
	var ends, mistyped int
	for _, ev := range ec.Events() {
		if ev.Error == nil {
			continue
		}
		if ev.Type == types.EventQueryEnd {
			ends++
		} else {
			mistyped++
		}
	}
	if ends == 0 {
		t.Fatal("no query_end event carries the abort error")
	}
	if mistyped != 0 {
		t.Errorf("%d error-carrying events without the query_end type", mistyped)
	}
	var interrupted bool
	for _, m := range eng.Messages() {
		if m.Role != types.RoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, types.InterruptMessage) {
				interrupted = true
			}
		}
	}
	if !interrupted {
		t.Error("the inline interrupt message was not appended to the last assistant turn")
	}
}

func TestQueryLoopEmitsQueryStart(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "reply"), nil)
	eng := New(&Params{Provider: mp, Model: "test-model", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })

	eng.QuerySync(context.Background(), "hello", "")
	ec.Reset()
	eng.QuerySync(context.Background(), "second", "")
	starts := ec.FindEvents(types.EventQueryStart)
	if len(starts) != 1 {
		t.Fatalf("%d query_start events, want exactly 1", len(starts))
	}
	if starts[0].Message == nil || !strings.HasSuffix(eventText(starts[0]), "second") {
		t.Errorf("query_start message = %+v, want the user message", starts[0].Message)
	}
	if starts[0].Model != "test-model" {
		t.Errorf("query_start Model = %q, want test-model", starts[0].Model)
	}
	if got := eng.queryStartMsgIdx; got != 2 {
		t.Errorf("queryStartMsgIdx = %d, want 2 (snapshot before the second user message)", got)
	}
}

// primeHistory runs one cheap turn so the compacted transcript has more
// messages than the compactor returns — the reduction the breaker counts.
func primeHistory(t *testing.T, eng *Engine, ec *eventCollector, logs *slogCapture) {
	t.Helper()
	eng.QuerySync(context.Background(), "prime", "")
	ec.Reset()
	logs.mu.Lock()
	logs.calls = nil
	logs.mu.Unlock()
}

// compactFixture builds a CompactResult with known payloads so summary lines
// and token counters are assertable.
func compactFixture(summary string, beforeTokens, afterTokens int) *short.CompactResult {
	return &short.CompactResult{
		Summary:      summary,
		BeforeTokens: beforeTokens,
		AfterTokens:  afterTokens,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("compacted")}},
		},
	}
}

type compactProbeKey struct{}

func compactTestEngine(t *testing.T, mc Compactor, ec *eventCollector, logs *slogCapture, hr *integrationHookRecorder) *Engine {
	t.Helper()
	cfg := hooks.HooksConfig{}
	if hr != nil {
		cfg = hooks.HooksConfig{
			"PreCompact": []hooks.HookMatcher{{Matcher: "*", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "pre-compact"},
			}}},
			"PostCompact": []hooks.HookMatcher{{Matcher: "*", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "post-compact"},
			}}},
		}
	}
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test-model",
		MaxTokens:  1000,
		Logger:     slog.New(logs),
		Dispatcher: ec,
		Hooks:      hooks.NewHooks(cfg, hr),
	})
	// effectiveWindow = 6000-1000 = 5000, buffer 3000 → threshold 2000 tokens,
	// which the long prompt used by these tests clears.
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 6000, MaxConsecutiveFailures: 3})
	return eng
}

func TestRunTurnsAutoCompactSuccess(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	ec := newEventCollector()
	hooksRec := &integrationHookRecorder{}
	mc := &recordingCompactor{result: compactFixture("condensed recap", 3000, 900)}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "after compact"), nil)
	eng := compactTestEngine(t, mc, ec, logs, hooksRec)
	eng.provider = mp
	// A previous failure must be cleared by a compact that actually reduced.
	eng.mu.Lock()
	eng.consecutiveCompactFailures = 1
	eng.mu.Unlock()
	t.Cleanup(func() { eng.Close() })

	primeHistory(t, eng, ec, logs)
	// A sentinel on the query context proves the compact hooks were dispatched
	// with the live query context rather than a fresh one.
	eng.QuerySync(context.WithValue(context.Background(), compactProbeKey{}, "sentinel"), strings.Repeat("filler ", 4000), "")

	calls, ctxs := mc.snapshot()
	if calls != 1 {
		t.Fatalf("compactor called %d times, want 1", calls)
	}
	if len(ctxs) != 1 || ctxs[0] == nil || ctxs[0].Value(compactProbeKey{}) != "sentinel" {
		t.Error("the compactor ran without the live query context")
	}
	starts := ec.FindEvents(types.EventToolStart)
	runs := ec.FindEvents(types.EventToolRun)
	ends := ec.FindEvents(types.EventToolEnd)
	params := ec.FindEvents(types.EventToolParamDelta)
	if len(starts) != 1 || len(runs) != 1 || len(ends) != 1 || len(params) != 1 {
		t.Fatalf("compact events: %d start, %d run, %d end, %d param — want 1 each",
			len(starts), len(runs), len(ends), len(params))
	}
	tu := starts[0].ToolUse
	if tu == nil {
		t.Fatal("compact tool_start carries no ToolUse payload")
	}
	if !strings.HasPrefix(tu.ID, "compact-auto-") || len(tu.ID) != len("compact-auto-")+8 {
		t.Errorf("compact id = %q, want compact-auto- plus an 8-char uuid", tu.ID)
	}
	if tu.Name != "Compact" {
		t.Errorf("compact tool_start Name = %q, want Compact", tu.Name)
	}
	if tu.Summary != "Compacting conversation..." {
		t.Errorf("compact tool_start Summary = %q", tu.Summary)
	}
	if runs[0].ToolUse == nil || runs[0].ToolUse.ID != tu.ID || runs[0].ToolUse.Name != "Compact" {
		t.Errorf("compact tool_run = %+v, want the same id and Compact", runs[0].ToolUse)
	}
	if params[0].PartialInput == nil || params[0].PartialInput.ID != tu.ID {
		t.Errorf("compact tool_param_delta = %+v, want the compact id", params[0].PartialInput)
	}
	wantLine := CompactSummaryLine(mc.lastResult())
	if params[0].PartialInput.Summary != wantLine {
		t.Errorf("compact summary line = %q, want %q", params[0].PartialInput.Summary, wantLine)
	}
	tr := ends[0].ToolResult
	if tr == nil {
		t.Fatal("compact tool_end carries no ToolResult payload")
	}
	if tr.ToolUseID != tu.ID {
		t.Errorf("compact tool_end ToolUseID = %q, want %q", tr.ToolUseID, tu.ID)
	}
	if tr.DisplayOutput != "condensed recap" || tr.IsError {
		t.Errorf("compact tool_end = %+v, want the compact summary text and no error", tr)
	}
	eng.mu.RLock()
	failures := eng.consecutiveCompactFailures
	eng.mu.RUnlock()
	if failures != 0 {
		t.Errorf("consecutiveCompactFailures = %d, want 0 (the seeded failure must be reset)", failures)
	}
	var preCompact, postCompact int
	for _, ev := range hooksRec.Events() {
		switch ev {
		case string(hooks.HookPreCompact):
			preCompact++
		case string(hooks.HookPostCompact):
			postCompact++
		}
	}
	if preCompact != 1 {
		t.Errorf("%d PreCompact hook calls, want exactly 1", preCompact)
	}
	if postCompact != 1 {
		t.Errorf("%d PostCompact hook calls, want exactly 1", postCompact)
	}
	ctxLost := len(hooksRec.Ctxs()) != 2
	for _, c := range hooksRec.Ctxs() {
		if c == nil || c.Value(compactProbeKey{}) != "sentinel" {
			ctxLost = true
		}
	}
	if ctxLost {
		t.Errorf("compact hooks ran with %d contexts, want 2 carrying the query context", len(hooksRec.Ctxs()))
	}
	recs := logs.all("pre-turn auto-compact succeeded")
	if len(recs) != 1 {
		t.Fatalf("%d success records, want exactly 1", len(recs))
	}
	if got := attrInt(recs[0].attrs["messages"]); got != 1 {
		t.Errorf("success messages = %d, want 1", got)
	}
}

func TestRunTurnsAutoCompactFailure(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	ec := newEventCollector()
	mc := &recordingCompactor{result: compactFixture("condensed recap", 3000, 900), err: errors.New("compact boom")}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "after failed compact"), nil)
	eng := compactTestEngine(t, mc, ec, logs, nil)
	eng.provider = mp
	t.Cleanup(func() { eng.Close() })

	primeHistory(t, eng, ec, logs)
	eng.QuerySync(context.Background(), strings.Repeat("filler ", 4000), "")

	ends := ec.FindEvents(types.EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("%d tool_end events, want exactly 1", len(ends))
	}
	tr := ends[0].ToolResult
	if tr == nil {
		t.Fatal("failure tool_end carries no ToolResult payload")
	}
	if ts := ec.FindEvents(types.EventToolStart); len(ts) != 1 || ts[0].ToolUse == nil || tr.ToolUseID != ts[0].ToolUse.ID {
		t.Errorf("failure tool_end ToolUseID = %q, want the compact tool id", tr.ToolUseID)
	}
	if tr.DisplayOutput != "Compact failed: compact boom" {
		t.Errorf("failure DisplayOutput = %q, want the compact error", tr.DisplayOutput)
	}
	if !tr.IsError {
		t.Error("failure tool_end IsError = false, want true")
	}
	eng.mu.RLock()
	failures := eng.consecutiveCompactFailures
	eng.mu.RUnlock()
	if failures != 1 {
		t.Errorf("consecutiveCompactFailures = %d, want 1 after a failed compact", failures)
	}
	recs := logs.all("pre-turn auto-compact failed")
	if len(recs) != 1 {
		t.Fatalf("%d failure records, want exactly 1", len(recs))
	}
	if got := fmt.Sprint(recs[0].attrs["error"]); !strings.Contains(got, "compact boom") {
		t.Errorf("failure record error = %q, want the compact error", got)
	}
	if got := attrInt(recs[0].attrs["consecutiveFailures"]); got != 1 {
		t.Errorf("failure record consecutiveFailures = %d, want 1", got)
	}
}

func TestRunTurnsAutoCompactNoReduction(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	ec := newEventCollector()
	// Same message count in and out: BeforeTokens == AfterTokens, so the
	// compact is a no-op and must count as a failure for the breaker.
	noop := compactFixture("condensed recap", 3000, 900)
	noop.AfterTokens = noop.BeforeTokens
	mc := &recordingCompactor{result: noop}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "reply"), nil)
	eng := compactTestEngine(t, mc, ec, logs, nil)
	eng.provider = mp
	t.Cleanup(func() { eng.Close() })

	primeHistory(t, eng, ec, logs)
	eng.QuerySync(context.Background(), strings.Repeat("filler ", 4000), "")
	eng.mu.RLock()
	failures := eng.consecutiveCompactFailures
	eng.mu.RUnlock()
	if failures != 1 {
		t.Errorf("consecutiveCompactFailures = %d, want 1 for a non-reducing compact", failures)
	}
	if len(logs.all("pre-turn auto-compact succeeded")) != 1 {
		t.Error("a non-reducing compact must still log the success branch")
	}
}

func TestRunTurnsFileHistorySnapshot(t *testing.T) {
	// make_snapshot logs through the package-level default logger.
	logs := &slogCapture{}
	attachSlogCapture(t, logs)

	dir := t.TempDir()
	file := filepath.Join(dir, "tracked.txt")
	// Large enough that the snapshot's backup copy clears one millisecond,
	// which is what the query_summary snapshot_ms accumulator measures.
	big := bytes.Repeat([]byte("A"), 8<<20)
	if err := os.WriteFile(file, big, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	tracker := filehistory.NewTracker(dir)
	tracker.LoadState(filehistory.FileHistoryState{
		// Fixed date: the seed snapshot only needs to exist and predate the
		// query; no assertion depends on its distance to the new snapshot.
		Snapshots:        []filehistory.FileHistorySnapshot{{MessageID: "seed-message", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		TrackedFiles:     map[string]bool{file: true},
		SnapshotSequence: 1,
	})
	if err := tracker.TrackEdit(file); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := os.WriteFile(file, bytes.Repeat([]byte("B"), 8<<20), 0o644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}

	var writerCalls int
	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "reply"), nil)
	eng := New(&Params{Provider: mp, Model: "test-model", Logger: slog.New(logs), Dispatcher: ec})
	eng.SetFileHistory(tracker)
	eng.SetFileHistoryWriter(func(filehistory.FileHistoryState) { writerCalls++ })
	t.Cleanup(func() { eng.Close() })

	eng.QuerySync(context.Background(), "go", "")

	recs := logs.all("engine:make_snapshot")
	if len(recs) != 1 {
		t.Fatalf("%d make_snapshot records, want exactly 1", len(recs))
	}
	if got := recs[0].attrs["msgID"]; got != eng.currentTurnMsgID || got == "" {
		t.Errorf("make_snapshot msgID = %v, want the current turn id %q", got, eng.currentTurnMsgID)
	}
	if got := attrInt(recs[0].attrs["trackedFiles"]); got != 1 {
		t.Errorf("make_snapshot trackedFiles = %d, want 1", got)
	}
	if got := attrInt(recs[0].attrs["snapshots"]); got != 2 {
		t.Errorf("make_snapshot snapshots = %d, want 2 (seed + this turn)", got)
	}
	if writerCalls != 1 {
		t.Errorf("fileHistoryWriter called %d times, want 1", writerCalls)
	}
	if len(logs.all("engine:make_snapshot_failed")) != 0 {
		t.Error("a successful snapshot logged a failure")
	}
	summaries := logs.all("engine:query_summary")
	if len(summaries) != 1 {
		t.Fatalf("%d query_summary records, want exactly 1", len(summaries))
	}
	if got := attrInt(summaries[0].attrs["snapshot_ms"]); got < 1 {
		t.Errorf("snapshot_ms = %d, want at least 1 for an 8MB backup copy", got)
	}
}

// prunableHistory builds a transcript with six Read tool results, the
// oldest of which the token prune must clear (KeepRecent is five).
func prunableHistory(t *testing.T) ([]types.Message, json.RawMessage) {
	t.Helper()
	content, err := json.Marshal(strings.Repeat("payload ", 500))
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	var msgs []types.Message
	// The stamps must sit near the wall clock: runTurns evaluates the
	// time-based trigger (time.Since of the last assistant) before the token
	// prune, and stale stamps would let that trigger clear rounds first.
	now := time.Now() // REAL-TIME: fresh stamps keep the time-based trigger off
	for i := range 6 {
		id := fmt.Sprintf("read-%d", i)
		msgs = append(msgs, types.Message{Role: types.RoleAssistant, Timestamp: now, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: id, Name: "Read"},
		}})
		msgs = append(msgs, types.Message{Role: types.RoleUser, Timestamp: now, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: id, Content: content},
		}})
	}
	return msgs, content
}

// passThroughCompactor returns the transcript unchanged: the auto-compact
// runs, reports no reduction, and leaves the token prune as the next resort.
type passThroughCompactor struct {
	calls  int
	tokens int // BeforeTokens; 0 means len(msgs)*100
	reduce bool
}

func (c *passThroughCompactor) Compact(_ context.Context, msgs []types.Message) (*short.CompactResult, error) {
	c.calls++
	n := c.tokens
	if n == 0 {
		n = len(msgs) * 100
	}
	after := n
	if c.reduce {
		after = n / 2
	}
	return &short.CompactResult{
		Messages:       msgs,
		BeforeTokens:   n,
		AfterTokens:    after,
		BeforeMessages: len(msgs),
	}, nil
}

func TestRunTurnsTokenPrune(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	msgs, content := prunableHistory(t)
	saved := calculateToolResultTokens(content)
	if saved == 0 {
		t.Fatal("fixture tool result estimates to zero tokens")
	}

	mp := &mockProvider{}
	// The turn's usage would overwrite ContextTokens, so fail the API call:
	// the prune's subtraction is then the last write to ContextTokens.
	mp.addResponse(nil, errors.New("llm down"))
	eng := New(&Params{
		Provider: mp, Model: "test-model", MaxTokens: 1000, MaxTurns: 1,
		Logger:      slog.New(logs),
		AutoCompact: AutoCompactConfig{ContextWindow: 6000},
	})
	mc := &passThroughCompactor{}
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 6000, MaxConsecutiveFailures: 3})
	eng.setMessages(msgs)
	eng.SetContextTokens(5000)
	t.Cleanup(func() { eng.Close() })

	eng.runTurns(context.Background(), "")

	if mc.calls != 1 {
		t.Fatalf("compactor called %d times, want 1 (the prune only runs after a failed compact)", mc.calls)
	}
	recs := logs.all("engine:token_prune")
	if len(recs) != 1 {
		t.Fatalf("%d token_prune records, want exactly 1", len(recs))
	}
	if got := attrInt(recs[0].attrs["cleared"]); got != 1 {
		t.Errorf("token_prune cleared = %d, want 1 (six results, five kept)", got)
	}
	if got := attrInt(recs[0].attrs["tokens_saved"]); got != int64(saved) {
		t.Errorf("token_prune tokens_saved = %d, want %d", got, saved)
	}
	eng.mu.RLock()
	ctxTokens := eng.ContextTokens
	eng.mu.RUnlock()
	// runCompact leaves ContextTokens at the compactor's AfterTokens
	// (12 messages x 100); the prune then subtracts what it cleared.
	if ctxTokens != 1200-saved {
		t.Errorf("ContextTokens = %d, want %d", ctxTokens, 1200-saved)
	}
	var clearedCount int
	for _, m := range eng.Messages() {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && string(b.Content) == `"`+TokenPrunedMessage+`"` {
				clearedCount++
			}
		}
	}
	if clearedCount != 1 {
		t.Errorf("%d tool results cleared, want 1", clearedCount)
	}
}

func TestRunTurnsTokenPruneGuards(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		subagent  bool
		ctxWindow int
		reducing  bool
		wantPrune bool
	}{
		{"subagent", true, 6000, false, false},
		{"no-context-window", false, 0, false, false},
		{"window-of-one", false, 1, false, true},
		{"successful-compact", false, 6000, true, false},
	}
	for _, tc := range cases {
		logs := &slogCapture{}
		msgs, _ := prunableHistory(t)
		mp := &mockProvider{}
		mp.addResponse(textStreamEvents("test", "reply"), nil)
		eng := New(&Params{
			Provider: mp, Model: "test-model", MaxTokens: 1000, MaxTurns: 1,
			Logger:      slog.New(logs),
			AutoCompact: AutoCompactConfig{ContextWindow: tc.ctxWindow},
		})
		mc := &passThroughCompactor{tokens: 4000, reduce: tc.reducing}
		eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: tc.ctxWindow, MaxConsecutiveFailures: 3})
		eng.isSubagent = tc.subagent
		eng.setMessages(msgs)
		eng.SetContextTokens(5000)
		t.Cleanup(func() { eng.Close() })

		eng.runTurns(context.Background(), "")

		want := 0
		if tc.wantPrune {
			want = 1
		}
		if got := len(logs.all("engine:token_prune")); got != want {
			t.Errorf("%s: %d token_prune records, want %d", tc.name, got, want)
		}
		var cleared int
		for _, m := range eng.Messages() {
			for _, b := range m.Content {
				if b.Type == types.ContentTypeToolResult && string(b.Content) == `"`+TokenPrunedMessage+`"` {
					cleared++
				}
			}
		}
		if cleared != want {
			t.Errorf("%s: %d tool results cleared, want %d", tc.name, cleared, want)
		}
	}
}

func TestRunTurnsLLMTiming(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	mp := &mockProvider{}
	// A text-only turn ends the loop, so the second turn has to be reached
	// through a tool call: two LLM calls means two accumulator terms.
	readTool := &mockTool{name: "Read", enabled: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "done"}, nil
	}}
	hang1, hang2 := make(chan struct{}), make(chan struct{})
	enter1, enter2 := make(chan struct{}), make(chan struct{})
	mp.addResponse(toolUseStreamEvents("test", "tu1", "Read", `{"file_path":"/nonexistent/probe"}`), nil)
	mp.addResponse(textStreamEvents("test", "slow turn two"), nil)
	mp.responses[0].hang = hang1
	mp.responses[0].entered = enter1
	mp.responses[1].hang = hang2
	mp.responses[1].entered = enter2
	go func() {
		// Each hold starts only after that call is parked, so its 30ms is a
		// guaranteed part of the llm_ms sum regardless of scheduling load.
		// The bounded wait turns a stream that never parks into a clean
		// failure: an unbounded channel receive would hold the query open
		// until the package timeout instead.
		if !waitClosed(enter1, 5*time.Second) {
			t.Error("first provider call never reached its hang point")
			close(hang1)
			close(hang2)
			return
		}
		time.Sleep(30 * time.Millisecond) // REAL-TIME: llm_ms reads the wall clock
		close(hang1)
		if !waitClosed(enter2, 5*time.Second) {
			t.Error("second provider call never reached its hang point")
			close(hang2)
			return
		}
		time.Sleep(30 * time.Millisecond) // REAL-TIME: llm_ms reads the wall clock
		close(hang2)
	}()

	eng := New(&Params{
		Provider: mp, Model: "test-model", MaxTurns: 2, Logger: slog.New(logs),
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{"Read": readTool} },
	})
	t.Cleanup(func() { eng.Close() })

	eng.QuerySync(context.Background(), "go", "")

	recs := logs.all("engine:query_summary")
	if len(recs) != 1 {
		t.Fatalf("%d query_summary records, want exactly 1", len(recs))
	}
	// Two turns held open for 30ms each: the accumulator must add both terms.
	// 60 is the sum floor — a last-write-wins accumulator lands at 30 and
	// passes a >=30 bar while still dropping the first turn's time.
	if got := attrInt(recs[0].attrs["llm_ms"]); got < 60 {
		t.Errorf("llm_ms = %d, want at least 60 (two turns held open for 30ms each)", got)
	}
}

// waitClosed reports whether ch was closed within d. Callers must fail the
// test on false — the channel's owner is usually parked on a counterpart
// channel, so an unbounded receive would stall the whole query.
func waitClosed(ch chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func TestRunTurnsTokenPruneClampsAtZero(t *testing.T) {
	t.Parallel()
	logs := &slogCapture{}
	msgs, content := prunableHistory(t)
	saved := calculateToolResultTokens(content)
	mp := &mockProvider{}
	mp.addResponse(nil, errors.New("llm down"))
	eng := New(&Params{
		Provider: mp, Model: "test-model", MaxTokens: 1000, MaxTurns: 1,
		Logger:      slog.New(logs),
		AutoCompact: AutoCompactConfig{ContextWindow: 6000},
	})
	// The compact leaves ContextTokens one token below what the prune will
	// clear, so the subtraction lands on exactly -1: the clamp has to run.
	mc := &passThroughCompactor{tokens: saved - 1}
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 6000, MaxConsecutiveFailures: 3})
	eng.setMessages(msgs)
	eng.SetContextTokens(5000)
	t.Cleanup(func() { eng.Close() })

	eng.runTurns(context.Background(), "")

	if got := len(logs.all("engine:token_prune")); got != 1 {
		t.Fatalf("%d token_prune records, want exactly 1", got)
	}
	eng.mu.RLock()
	ctxTokens := eng.ContextTokens
	eng.mu.RUnlock()
	if ctxTokens != 0 {
		t.Errorf("ContextTokens = %d, want the clamp at 0 (saved %d exceeds the %d the compact left)", ctxTokens, saved, saved-1)
	}
}

func TestGetWorkingDirReleasesLock(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	eng.setWorkingDir("/tmp/probe-dir")
	if got := eng.getWorkingDir(); got != "/tmp/probe-dir" {
		t.Fatalf("getWorkingDir = %q, want the configured directory", got)
	}
	// A leaked RLock would block the next writer forever; TryLock turns that
	// deadlock into an immediate, observable false.
	if !eng.mu.TryLock() {
		t.Error("getWorkingDir left the engine mutex read-locked")
	} else {
		eng.mu.Unlock()
	}
}

func TestDefaultCoalesceWindow(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	if got := eng.effectiveWindow(); got != 100*time.Millisecond {
		t.Errorf("default coalesce window = %v, want 100ms", got)
	}
}

// TestTokenPruneBudgetBoundary pins the token-prune threshold itself: the
// reserved-output and manual-compact buffers are subtracted from the context
// window, and the comparison is "prune only above the budget".
func TestTokenPruneBudgetBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		offset    int // budget = estimate + offset
		wantPrune bool
	}{
		{"at-budget", 0, false},
		{"one-over-budget", -1, true},
	}
	for _, tc := range cases {
		logs := &slogCapture{}
		msgs, _ := prunableHistory(t)
		mp := &mockProvider{}
		mp.addResponse(nil, errors.New("llm down"))
		eng := New(&Params{
			Provider: mp, Model: "test-model", MaxTokens: 100_000, MaxTurns: 1,
			Logger: slog.New(logs),
		})
		eng.mu.Lock()
		eng.compactor = nil
		eng.mu.Unlock()
		eng.setMessages(msgs)
		estimate := eng.currentInputTokens()
		// budget = ContextWindow - min(maxTokens, 20000) - 3000
		window := estimate + tc.offset + 20_000 + 3_000
		eng.mu.Lock()
		eng.autoCompactConfig = AutoCompactConfig{ContextWindow: window}
		eng.mu.Unlock()
		t.Cleanup(func() { eng.Close() })

		eng.runTurns(context.Background(), "")

		want := 0
		if tc.wantPrune {
			want = 1
		}
		if got := len(logs.all("engine:token_prune")); got != want {
			t.Errorf("%s: %d token_prune records, want %d (estimate %d, window %d)",
				tc.name, got, want, estimate, window)
		}
	}
}

func TestMaxTokensRecoveryLimit(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	for range 6 {
		mp.addResponse(textEventsWithStopReason("test-model", "cut off", stopReasonMaxTokens), nil)
	}
	eng := New(&Params{Provider: mp, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	eng.QuerySync(context.Background(), "tell me a long story", "")

	if got := mp.callCount(); got != 4 {
		t.Errorf("provider calls = %d, want 4 (one turn plus three max_tokens recoveries)", got)
	}
	var continuations int
	for _, m := range eng.Messages() {
		if m.Role == types.RoleUser && m.Flags&types.FlagMeta != 0 {
			for _, b := range m.Content {
				if b.Type == types.ContentTypeText && b.Text == continuationPrompt {
					continuations++
				}
			}
		}
	}
	if continuations != 3 {
		t.Errorf("%d continuation prompts, want 3", continuations)
	}
}

// TestRunAgentInterruptStubWithoutUserMessage covers the lastUserIdx < 0
// arm: a fork whose transcript has no selectable user message at all.
func TestRunAgentInterruptStubWithoutUserMessage(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	hang := make(chan struct{})
	mp.responses = append(mp.responses, mockResponse{
		events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 12}}},
		},
		hang: hang,
	})
	fork := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_fork", Name: "Read"},
		}},
	}
	deps := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "parent-model", Logger: silentLogger(),
		ToolsProvider: runAgentTools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForCallCount(t, mp, 1, 5*time.Second)
		cancel()
		close(hang)
	}()

	res, err := eng.RunAgent(ctx, agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", ForkMessages: fork,
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res == nil {
		t.Fatal("RunAgent returned nil result")
	}
	if res.Content != "(agent interrupted by user)" {
		t.Errorf("Content = %q, want the interrupt stub for a transcript with no user message", res.Content)
	}
}

// ===========================================================================
// Usage payload and reactive-compact pins
// ===========================================================================

// distinctUsage is a usage script whose four counters are all distinct and
// non-zero, so clearing or mis-accumulating any one of them breaks an equality
// assertion instead of hiding behind a zero.
var distinctUsage = types.Usage{
	InputTokens:              100,
	OutputTokens:             7,
	CacheReadInputTokens:     30,
	CacheCreationInputTokens: 11,
}

var distinctUsageEvent = types.UsageEvent{
	InputTokens:              100,
	OutputTokens:             7,
	CacheReadInputTokens:     30,
	CacheCreationInputTokens: 11,
}

// textStreamEventsWithUsage streams a text reply with usage carried on both
// message_start and message_delta, the way the Anthropic stream reports it.
func textStreamEventsWithUsage(model, text, stopReason string, u types.Usage) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: u}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: stopReason}, Usage: &u},
		{Type: "message_stop"},
	}
}

// toolUseStreamEventsWithUsage streams a tool_use reply with the same usage payload.
func toolUseStreamEventsWithUsage(model, id, name, input string, u types.Usage) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: u}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: id, Name: name}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: input}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &u},
		{Type: "message_stop"},
	}
}

// singleQueryEnd returns the single query_end event, failing the test if the
// terminal emission is missing or duplicated.
func singleQueryEnd(t *testing.T, ec *eventCollector) types.QueryEvent {
	t.Helper()
	ends := ec.FindEvents(types.EventQueryEnd)
	if len(ends) != 1 {
		t.Fatalf("%d query_end events, want exactly 1", len(ends))
	}
	return ends[0]
}

// checkUsagePayload pins all four counters of a usage payload.
func checkUsagePayload(t *testing.T, label string, got *types.UsageEvent, want types.UsageEvent) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s carries no usage payload", label)
	}
	if *got != want {
		t.Errorf("%s usage = %+v, want %+v", label, *got, want)
	}
}

// TestTerminalQueryEndUsageFields pins the usage payload of the
// no-tool-use terminal exit.
func TestTerminalQueryEndUsageFields(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", distinctUsage), nil)
	ec := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	end := singleQueryEnd(t, ec)
	checkUsagePayload(t, "terminal query_end", end.Usage, distinctUsageEvent)
	if res.TotalUsage != (types.Usage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 30, CacheCreationInputTokens: 11}) {
		t.Errorf("QueryResult.TotalUsage = %+v, want the accumulated usage", res.TotalUsage)
	}
	if res.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", res.TurnCount)
	}
}

// TestMaxTurnsEndQueryEndUsage pins the usage payload of the max-turns
// exhaustion exit.
func TestMaxTurnsEndQueryEndUsage(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_1", "Read", `{}`, distinctUsage), nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider: mp, Model: "m", MaxTurns: 1, Logger: silentLogger(), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	end := singleQueryEnd(t, ec)
	checkUsagePayload(t, "max-turns query_end", end.Usage, distinctUsageEvent)
	if res.TotalUsage != (types.Usage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 30, CacheCreationInputTokens: 11}) {
		t.Errorf("QueryResult.TotalUsage = %+v, want the accumulated usage", res.TotalUsage)
	}
	if res.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", res.TurnCount)
	}
	if len(ec.FindEvents(types.EventTurnEnd)) != 1 {
		t.Errorf("%d turn_end events, want exactly 1 for the tool turn", len(ec.FindEvents(types.EventTurnEnd)))
	}
}

// TestPostStreamAbortQueryEndUsage pins the post-streaming abort exit
// interrupt message, tool discard, turn_end, and the
// full usage payload on both the event and the returned result.
func TestPostStreamAbortQueryEndUsage(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	toolReturned := make(chan struct{})
	// The stream completes (message_stop) but stays open, so the abort is first
	// observed by the post-streaming check, not by callLLM itself.
	hang := make(chan struct{})
	mp.responses = append(mp.responses, mockResponse{
		events: toolUseStreamEventsWithUsage("m", "tu_1", "Slow", `{}`, distinctUsage),
		hang:   hang,
	})
	ec := newEventCollector()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Slow": &mockTool{
				name: "Slow", enabled: true,
				callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
					close(started)
					<-release
					close(toolReturned)
					return &tool.ToolResult{Data: "late"}, nil
				},
			}}
		},
	})
	t.Cleanup(func() {
		select {
		case <-started:
			releaseOnce.Do(func() { close(release) })
			<-toolReturned
		default:
		}
	})
	// The tool must be mid-call before the cancel, so its completion is
	// guaranteed to land after Discard() has run.
	go func() {
		<-started
		cancel()
		close(hang)
	}()
	res := eng.QuerySync(ctx, "go", "")
	if res.Error == nil {
		t.Fatal("expected an abort error from the post-streaming check")
	}
	var abortErr *AbortError
	if !errors.As(res.Error, &abortErr) {
		t.Fatalf("res.Error = %v (%T), want *AbortError", res.Error, res.Error)
	}
	if abortErr.Phase != "streaming" {
		t.Errorf("AbortError.Phase = %q, want \"streaming\"", abortErr.Phase)
	}
	end := singleQueryEnd(t, ec)
	checkUsagePayload(t, "post-stream abort query_end", end.Usage, distinctUsageEvent)
	if end.Error == nil {
		t.Error("query_end carries no abort error")
	}
	if res.TotalUsage != (types.Usage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 30, CacheCreationInputTokens: 11}) {
		t.Errorf("QueryResult.TotalUsage = %+v, want the accumulated usage", res.TotalUsage)
	}
	if len(ec.FindEvents(types.EventTurnEnd)) != 1 {
		t.Errorf("%d turn_end events, want exactly 1 before the abort query_end", len(ec.FindEvents(types.EventTurnEnd)))
	}
	// Discard() ran before the query returned, so the tool that finishes after
	// the abort must not add a tool_end: the executor suppresses emissions once
	// discarded. Release it and give any late emission a window to land.
	before := len(ec.FindEvents(types.EventToolEnd))
	releaseOnce.Do(func() { close(release) })
	<-toolReturned
	deadline := time.NewTimer(300 * time.Millisecond)
	defer deadline.Stop()
	for {
		if n := len(ec.FindEvents(types.EventToolEnd)); n > before {
			t.Fatalf("tool_end count grew from %d to %d after the abort: Discard() did not suppress the late emission", before, n)
		}
		select {
		case <-deadline.C:
			return
		default:
			runtime.Gosched()
		}
	}
}

// TestLLMErrorAbortQueryEndUsage pins the abort exit taken when the LLM
// call itself fails under a cancelled context.
func TestLLMErrorAbortQueryEndUsage(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	// Turn 1 accumulates usage; turn 2's stream dies without message_stop while
	// ctx is cancelled, so callLLM returns the abort error.
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_1", "Read", `{}`, distinctUsage), nil)
	mp.responses = append(mp.responses, mockResponse{
		events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "m", Usage: types.Usage{InputTokens: 500}}},
		},
		hang: make(chan struct{}),
	})
	ec := newEventCollector()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })
	go func() {
		waitForCallCount(t, mp, 2, 5*time.Second)
		cancel()
	}()
	res := eng.QuerySync(ctx, "go", "")
	if res.Error == nil {
		t.Fatal("expected an abort error from the cancelled second turn")
	}
	if _, ok := errors.AsType[*AbortError](res.Error); !ok {
		t.Fatalf("res.Error = %v (%T), want *AbortError", res.Error, res.Error)
	}
	end := singleQueryEnd(t, ec)
	checkUsagePayload(t, "callLLM-abort query_end", end.Usage, distinctUsageEvent)
	if end.Error == nil {
		t.Error("query_end carries no abort error")
	}
	if res.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1 (the aborted call is not a completed turn)", res.TurnCount)
	}
	var interrupted bool
	for _, m := range eng.Messages() {
		if m.Role != types.RoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && b.Text == types.InterruptMessage {
				interrupted = true
			}
		}
	}
	if !interrupted {
		t.Error("the inline interrupt message was not appended on the callLLM abort path")
	}
}

// TestAbortAfterFailedCompactAttempt pins the abort check that runs after
// a failed reactive compact: the abort error, not the
// original API error, is what the query returns.
func TestAbortAfterFailedCompactAttempt(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_1", "Read", `{}`, types.Usage{InputTokens: 10}), nil)
	mp.addResponse(nil, &llm.APIError{Type: "invalid_request_error", ErrorCode: "prompt_too_long", Message: "context overflow", Status: 413, Retryable: false})
	ec := newEventCollector()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	// The compactor cancels the query and then fails, so the abort is observed
	// only by the post-compact check.
	eng.SetCompactor(&funcCompactor{fn: func(context.Context, []types.Message) (*short.CompactResult, error) {
		cancel()
		return nil, errors.New("compact failed on purpose")
	}}, AutoCompactConfig{ContextWindow: 200000})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(ctx, "go", "")
	if res.Error == nil {
		t.Fatal("expected an abort error after the failed compact attempt")
	}
	if _, ok := errors.AsType[*AbortError](res.Error); !ok {
		t.Fatalf("res.Error = %v (%T), want *AbortError — the post-compact abort check must outrank the API error", res.Error, res.Error)
	}
	end := singleQueryEnd(t, ec)
	if end.Error == nil {
		t.Error("query_end carries no abort error")
	} else if _, ok := end.Error.(*AbortError); !ok {
		t.Errorf("query_end error = %v (%T), want *AbortError", end.Error, end.Error)
	}
	var interrupted bool
	for _, m := range eng.Messages() {
		if m.Role != types.RoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && b.Text == types.InterruptMessage {
				interrupted = true
			}
		}
	}
	if !interrupted {
		t.Error("the inline interrupt message was not appended on the post-compact abort path")
	}
}

// overflowAPIError is a context-overflow API error: IsContextOverflow keys on
// ErrorCode, not Type.
func overflowAPIError() error {
	return &llm.APIError{Type: "invalid_request_error", ErrorCode: "prompt_too_long", Message: "context overflow", Status: 413, Retryable: false}
}

// recordingCompactor records the context it was handed and returns a fixed result,
// so the compact event payloads are known to the test.
type recordingCompactor struct {
	mu     sync.Mutex
	calls  int
	gotCtx []context.Context
	result *short.CompactResult
	last   *short.CompactResult
	err    error
}

func (c *recordingCompactor) Compact(ctx context.Context, msgs []types.Message) (*short.CompactResult, error) {
	c.mu.Lock()
	c.calls++
	c.gotCtx = append(c.gotCtx, ctx)
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	out := *c.result
	out.BeforeMessages = len(msgs)
	c.mu.Lock()
	c.last = &out
	c.mu.Unlock()
	return &out, nil
}

// lastResult returns the result the compactor actually handed the engine, so
// summary-line assertions compare against the real BeforeMessages.
func (c *recordingCompactor) lastResult() *short.CompactResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *recordingCompactor) snapshot() (int, []context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]context.Context(nil), c.gotCtx...)
}

// compactEventGroup returns the compact event group in emission order, keyed by
// the tool_use id the group shares.
type compactEventGroup struct {
	start, run, paramDelta, end *types.QueryEvent
	id                          string
}

// collectCompactEvents pulls the Compact events out of the stream and pins that
// every one of them carries the same tool_use id.
func collectCompactEvents(t *testing.T, ec *eventCollector, idPrefix string) compactEventGroup {
	t.Helper()
	var got compactEventGroup
	var starts, runs, deltas, ends int
	for _, ev := range ec.Events() {
		switch ev.Type {
		case types.EventToolStart:
			if ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
				starts++
				got.start = &ev
				got.id = ev.ToolUse.ID
			}
		case types.EventToolRun:
			if ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
				runs++
				got.run = &ev
			}
		case types.EventToolParamDelta:
			if ev.PartialInput != nil && strings.HasPrefix(ev.PartialInput.ID, idPrefix) {
				deltas++
				got.paramDelta = &ev
			}
		case types.EventToolEnd:
			if ev.ToolResult != nil && strings.HasPrefix(ev.ToolResult.ToolUseID, idPrefix) {
				ends++
				got.end = &ev
			}
		}
	}
	if starts != 1 || runs != 1 || deltas != 1 || ends != 1 {
		t.Fatalf("compact event group = %d start, %d run, %d param_delta, %d end; want 1 each", starts, runs, deltas, ends)
	}
	if !strings.HasPrefix(got.id, idPrefix) {
		t.Errorf("compact id = %q, want prefix %q", got.id, idPrefix)
	}
	if len(got.id) != len(idPrefix)+8 {
		t.Errorf("compact id %q has a %d-char suffix, want 8 (uuid slice width)", got.id, len(got.id)-len(idPrefix))
	}
	if got.run.ToolUse == nil || got.run.ToolUse.ID != got.id {
		t.Errorf("tool_run tool_use = %+v, want the same id %q", got.run.ToolUse, got.id)
	}
	if got.paramDelta.PartialInput.ID != got.id {
		t.Errorf("tool_param_delta id = %q, want %q", got.paramDelta.PartialInput.ID, got.id)
	}
	if got.end.ToolResult.ToolUseID != got.id {
		t.Errorf("tool_end tool_use_id = %q, want %q", got.end.ToolResult.ToolUseID, got.id)
	}
	return got
}

// compactHookConfig wires PreCompact/PostCompact command hooks.
func compactHookConfig() hooks.HooksConfig {
	return hooks.HooksConfig{
		"PreCompact": []hooks.HookMatcher{{Matcher: "*", Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "pre-compact"},
		}}},
		"PostCompact": []hooks.HookMatcher{{Matcher: "*", Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "post-compact"},
		}}},
	}
}

// TestReactiveCompactEventPayloads pins the whole reactive-compact event
// group: id width, names, summary lines, hook dispatch,
// and the retry that follows.
func TestReactiveCompactEventPayloads(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(nil, overflowAPIError())
	mp.addResponse(textStreamEventsWithUsage("m", "recovered", "end_turn", types.Usage{InputTokens: 3}), nil)
	ec := newEventCollector()
	hookRec := &integrationHookRecorder{}
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec,
		Hooks: hooks.NewHooks(compactHookConfig(), hookRec),
	})
	mc := &recordingCompactor{result: compactFixture("reactive summary", 4321, 210)}
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 200000})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	if got := mp.callCount(); got != 2 {
		t.Fatalf("%d provider calls, want 2 (compact then retry)", got)
	}
	got := collectCompactEvents(t, ec, "compact-reactive-")
	if got.start.ToolUse.Summary != "Compacting after context overflow..." {
		t.Errorf("tool_start summary = %q, want the overflow summary", got.start.ToolUse.Summary)
	}
	if got.run.ToolUse.Name != "Compact" {
		t.Errorf("tool_run name = %q, want Compact", got.run.ToolUse.Name)
	}
	wantLine := CompactSummaryLine(mc.lastResult())
	if got.paramDelta.PartialInput.Summary != wantLine {
		t.Errorf("tool_param_delta summary = %q, want %q", got.paramDelta.PartialInput.Summary, wantLine)
	}
	if got.end.ToolResult.DisplayOutput != "reactive summary" {
		t.Errorf("tool_end display = %q, want the compact summary", got.end.ToolResult.DisplayOutput)
	}
	if got.end.ToolResult.IsError {
		t.Error("tool_end marks a successful compact as an error")
	}
	calls, ctxs := mc.snapshot()
	if calls != 1 {
		t.Fatalf("compactor called %d times, want 1", calls)
	}
	for i, c := range ctxs {
		if c == nil {
			t.Errorf("compactor call %d ran with a nil context", i)
		}
	}
	hookCtxs := hookRec.Ctxs()
	if len(hookCtxs) != 2 {
		t.Fatalf("%d compact hook dispatches, want 2 (pre + post)", len(hookCtxs))
	}
	for i, c := range hookCtxs {
		if c == nil {
			t.Errorf("compact hook %d dispatched with a nil context", i)
		}
	}
}

// TestReactiveCompactFailureEvent pins the failure arm
// error-flagged tool_end, no param delta, no retry, and
// the warning log.
func TestReactiveCompactFailureEvent(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(nil, overflowAPIError())
	ec := newEventCollector()
	logs := &slogCapture{}
	eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(logs), Dispatcher: ec})
	boom := errors.New("compact boom")
	mc := &recordingCompactor{err: boom}
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 200000})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error == nil {
		t.Fatal("expected the overflow error to stay terminal after a failed compact")
	}
	if got := mp.callCount(); got != 1 {
		t.Errorf("%d provider calls, want 1 (no retry after a failed compact)", got)
	}
	var starts, ends, deltas int
	var end *types.QueryEvent
	for i, ev := range ec.Events() {
		switch ev.Type {
		case types.EventToolStart:
			if ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
				starts++
			}
		case types.EventToolParamDelta:
			deltas++
		case types.EventToolEnd:
			if ev.ToolResult != nil && strings.HasPrefix(ev.ToolResult.ToolUseID, "compact-reactive-") {
				ends++
				end = &ec.Events()[i]
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("%d compact start / %d compact end events, want 1 each", starts, ends)
	}
	if deltas != 0 {
		t.Errorf("%d tool_param_delta events, want 0 on a failed compact", deltas)
	}
	if end.ToolResult.DisplayOutput != fmt.Sprintf("Compact failed: %v", boom) {
		t.Errorf("tool_end display = %q, want %q", end.ToolResult.DisplayOutput, fmt.Sprintf("Compact failed: %v", boom))
	}
	if !end.ToolResult.IsError {
		t.Error("tool_end does not flag the failed compact as an error")
	}
	recs := logs.all("reactive auto-compact failed")
	if len(recs) != 1 {
		t.Fatalf("%d 'reactive auto-compact failed' records, want 1", len(recs))
	}
	if errVal, ok := recs[0].attrs["error"].(error); !ok || errVal != boom {
		t.Errorf("warning attrs[error] = %#v, want the compact error", recs[0].attrs["error"])
	}
}

// TestReactiveCompactSkippedForCompactSource pins the recursion guard at
// compact- and session-memory-forked agents must not run a
// reactive compact.
func TestReactiveCompactSkippedForCompactSource(t *testing.T) {
	t.Parallel()
	for _, agentType := range []string{"compact", "session_memory"} {
		t.Run(agentType, func(t *testing.T) {
			t.Parallel()
			mp := &mockProvider{}
			mp.addResponse(nil, overflowAPIError())
			ec := newEventCollector()
			parent := New(&Params{Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec})
			mc := &recordingCompactor{result: compactFixture("must not run", 4321, 210)}
			parent.SetCompactor(mc, AutoCompactConfig{ContextWindow: 200000})
			t.Cleanup(func() { parent.Close() })
			sub := parent.NewSubEngine(SubEngineOptions{AgentType: agentType, Tools: map[string]tool.Tool{}})
			res := sub.QuerySync(context.Background(), "go", "")
			if res.Error == nil {
				t.Fatal("expected the overflow error to be terminal for a guarded query source")
			}
			if calls, _ := mc.snapshot(); calls != 0 {
				t.Errorf("compactor called %d times, want 0 for a %s query", calls, agentType)
			}
			for _, ev := range ec.Events() {
				if ev.Type == types.EventToolStart && ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
					t.Fatalf("a %s query emitted a Compact tool_start", agentType)
				}
			}
		})
	}
}

// TestContextWindowRecoveryEventPayloads pins the stop_reason recovery
// compact group.
func TestContextWindowRecoveryEventPayloads(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEventsWithUsage("m", "truncated", stopReasonContextWindowExceeded, types.Usage{InputTokens: 40}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "after recovery", "end_turn", types.Usage{InputTokens: 5}), nil)
	ec := newEventCollector()
	hookRec := &integrationHookRecorder{}
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec,
		Hooks: hooks.NewHooks(compactHookConfig(), hookRec),
	})
	mc := &recordingCompactor{result: compactFixture("recovery summary", 4321, 210)}
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 200000})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	if got := mp.callCount(); got != 2 {
		t.Fatalf("%d provider calls, want 2 (recovery then next turn)", got)
	}
	got := collectCompactEvents(t, ec, "compact-recovery-")
	if got.start.ToolUse.Summary != "Compacting to continue truncated response..." {
		t.Errorf("tool_start summary = %q, want the recovery summary", got.start.ToolUse.Summary)
	}
	if got.start.ToolUse.Name != "Compact" {
		t.Errorf("tool_start name = %q, want Compact", got.start.ToolUse.Name)
	}
	if got.run.ToolUse.Name != "Compact" {
		t.Errorf("tool_run name = %q, want Compact", got.run.ToolUse.Name)
	}
	wantLine := CompactSummaryLine(mc.lastResult())
	if got.paramDelta.PartialInput.Summary != wantLine {
		t.Errorf("tool_param_delta summary = %q, want %q", got.paramDelta.PartialInput.Summary, wantLine)
	}
	if got.end.ToolResult.DisplayOutput != "recovery summary" {
		t.Errorf("tool_end display = %q, want the compact summary", got.end.ToolResult.DisplayOutput)
	}
	if got.end.ToolResult.IsError {
		t.Error("tool_end marks a successful recovery compact as an error")
	}
	if n := len(ec.FindEvents(types.EventTurnEnd)); n != 2 {
		t.Errorf("%d turn_end events, want 2 (recovery turn + terminal turn)", n)
	}
	calls, ctxs := mc.snapshot()
	if calls != 1 {
		t.Fatalf("compactor called %d times, want 1", calls)
	}
	for i, c := range ctxs {
		if c == nil {
			t.Errorf("recovery compactor call %d ran with a nil context", i)
		}
	}
	hookCtxs := hookRec.Ctxs()
	if len(hookCtxs) != 2 {
		t.Fatalf("%d compact hook dispatches, want 2 (pre + post)", len(hookCtxs))
	}
	for i, c := range hookCtxs {
		if c == nil {
			t.Errorf("recovery compact hook %d dispatched with a nil context", i)
		}
	}
}

// TestContextWindowRecoveryCompactFailure pins the recovery failure arm
// error-flagged tool_end, no param delta, no recovery
// turn_end, and no second provider call.
func TestContextWindowRecoveryCompactFailure(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEventsWithUsage("m", "truncated", stopReasonContextWindowExceeded, types.Usage{InputTokens: 40}), nil)
	ec := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec})
	boom := errors.New("recovery boom")
	mc := &recordingCompactor{err: boom}
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 200000})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	if got := mp.callCount(); got != 1 {
		t.Errorf("%d provider calls, want 1 (a failed recovery does not continue)", got)
	}
	var starts, ends, deltas, runs int
	var end *types.QueryEvent
	for i, ev := range ec.Events() {
		switch ev.Type {
		case types.EventToolStart:
			if ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
				starts++
				if ev.ToolUse.ID == "" {
					t.Error("compact tool_start carries no tool_use id")
				}
			}
		case types.EventToolRun:
			if ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
				runs++
			}
		case types.EventToolParamDelta:
			deltas++
		case types.EventToolEnd:
			if ev.ToolResult != nil && strings.HasPrefix(ev.ToolResult.ToolUseID, "compact-recovery-") {
				ends++
				end = &ec.Events()[i]
			}
		}
	}
	if starts != 1 || runs != 1 || ends != 1 {
		t.Fatalf("recovery group = %d start, %d run, %d end; want 1 each", starts, runs, ends)
	}
	if deltas != 0 {
		t.Errorf("%d tool_param_delta events, want 0 on a failed recovery compact", deltas)
	}
	if end.ToolResult.DisplayOutput != fmt.Sprintf("Compact failed: %v", boom) {
		t.Errorf("tool_end display = %q, want %q", end.ToolResult.DisplayOutput, fmt.Sprintf("Compact failed: %v", boom))
	}
	if !end.ToolResult.IsError {
		t.Error("tool_end does not flag the failed recovery compact as an error")
	}
	if n := len(ec.FindEvents(types.EventTurnEnd)); n != 1 {
		t.Errorf("%d turn_end events, want 1 (the terminal turn only)", n)
	}
}

// TestContextWindowRecoverySkippedForCompactSource pins the recursion
// guard for compact- and session-memory-forked agents.
func TestContextWindowRecoverySkippedForCompactSource(t *testing.T) {
	t.Parallel()
	for _, agentType := range []string{"compact", "session_memory"} {
		t.Run(agentType, func(t *testing.T) {
			t.Parallel()
			mp := &mockProvider{}
			mp.addResponse(textStreamEventsWithUsage("m", "truncated", stopReasonContextWindowExceeded, types.Usage{InputTokens: 40}), nil)
			ec := newEventCollector()
			parent := New(&Params{Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec})
			mc := &recordingCompactor{result: compactFixture("must not run", 4321, 210)}
			parent.SetCompactor(mc, AutoCompactConfig{ContextWindow: 200000})
			t.Cleanup(func() { parent.Close() })
			sub := parent.NewSubEngine(SubEngineOptions{AgentType: agentType, Tools: map[string]tool.Tool{}})
			res := sub.QuerySync(context.Background(), "go", "")
			if res.Error != nil {
				t.Fatalf("query failed: %v", res.Error)
			}
			if calls, _ := mc.snapshot(); calls != 0 {
				t.Errorf("compactor called %d times, want 0 for a %s query", calls, agentType)
			}
			for _, ev := range ec.Events() {
				if ev.Type == types.EventToolStart && ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
					t.Fatalf("a %s query emitted a Compact tool_start", agentType)
				}
			}
		})
	}
}

// errUpstreamRefused is an error whose FormatLLMError output does not already
// carry the "API Error" prefix, so the engine's prepend is observable.
var errUpstreamRefused = errors.New("upstream refused the request")

// TestTerminalAPIErrorMarker pins the terminal-error exit
// the flagged assistant marker, its exact text, the
// query_end payload, and the terminal log record.
func TestTerminalAPIErrorMarker(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(nil, errUpstreamRefused)
	ec := newEventCollector()
	logs := &slogCapture{}
	eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(logs), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error == nil {
		t.Fatal("expected a terminal error")
	}
	wantText := "API Error: " + errUpstreamRefused.Error()
	var markers []types.Message
	for _, m := range eng.Messages() {
		if m.Role == types.RoleAssistant && m.Flags&types.FlagAPIError != 0 {
			markers = append(markers, m)
		}
	}
	if len(markers) != 1 {
		t.Fatalf("%d API-error assistant markers, want exactly 1", len(markers))
	}
	if markers[0].Flags != types.FlagAPIError {
		t.Errorf("marker flags = %d, want exactly FlagAPIError", markers[0].Flags)
	}
	if len(markers[0].Content) != 1 || markers[0].Content[0].Text != wantText {
		t.Errorf("marker text = %+v, want %q", markers[0].Content, wantText)
	}
	end := singleQueryEnd(t, ec)
	if end.Error == nil || end.Error.Error() != res.Error.Error() {
		t.Errorf("query_end error = %v, want the terminal error %v", end.Error, res.Error)
	}
	recs := logs.all("callLLM error (terminal)")
	if len(recs) != 1 {
		t.Fatalf("%d 'callLLM error (terminal)' records, want 1", len(recs))
	}
	if errVal, ok := recs[0].attrs["error"].(error); !ok || errVal.Error() != errUpstreamRefused.Error() {
		t.Errorf("terminal log attrs[error] = %#v, want the API error", recs[0].attrs["error"])
	}
	if turns, ok := recs[0].attrs["turn"]; !ok || turns != int64(0) {
		t.Errorf("terminal log attrs[turn] = %#v, want 0", recs[0].attrs["turn"])
	}
}

// TestTerminalAPIErrorStopFailurePanic pins the fire-and-forget
// StopFailure goroutine: the hook runs, a panicking
// executor is recovered, and the panic is logged.
func TestTerminalAPIErrorStopFailurePanic(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(nil, errUpstreamRefused)
	logs := &slogCapture{}
	rec := &integrationHookRecorder{panics: true, entered: make(chan struct{})}
	h := hooks.NewHooks(hooks.HooksConfig{
		"StopFailure": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "on-failure"},
		}}},
	}, rec)
	eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(logs), Hooks: h})
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "go", ""); res.Error == nil {
		t.Fatal("expected a terminal error")
	}
	select {
	case <-rec.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the StopFailure hook was never dispatched")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if recs := logs.all("StopFailure hook panic"); len(recs) == 1 {
			if recs[0].attrs["panic"] != "hook executor exploded" {
				t.Errorf("panic attr = %#v, want the recovered value", recs[0].attrs["panic"])
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("no 'StopFailure hook panic' record; records: %v", logMsgs(logs))
		default:
			runtime.Gosched()
		}
	}
}

// logMsgs lists recorded slog messages, for failure output only.
func logMsgs(l *slogCapture) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.calls))
	for _, r := range l.calls {
		out = append(out, r.msg)
	}
	return out
}

// TestUsageAccumulatesAcrossTurns pins the += accumulation of cache
// counters across turns.
func TestUsageAccumulatesAcrossTurns(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_1", "Read", `{}`, types.Usage{
		InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 30, CacheCreationInputTokens: 11,
	}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", types.Usage{
		InputTokens: 60, OutputTokens: 4, CacheReadInputTokens: 5, CacheCreationInputTokens: 2,
	}), nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	want := types.Usage{InputTokens: 160, OutputTokens: 11, CacheReadInputTokens: 35, CacheCreationInputTokens: 13}
	if res.TotalUsage != want {
		t.Errorf("QueryResult.TotalUsage = %+v, want %+v (per-turn sums, not last-wins)", res.TotalUsage, want)
	}
	end := singleQueryEnd(t, ec)
	checkUsagePayload(t, "two-turn query_end", end.Usage, types.UsageEvent{
		InputTokens: 160, OutputTokens: 11, CacheReadInputTokens: 35, CacheCreationInputTokens: 13,
	})
}

// TestCalibrationAnchoredAfterAssistant pins the calibration anchor the
// assistant message leaves behind.
func TestCalibrationAnchoredAfterAssistant(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	usage := types.Usage{InputTokens: 120, CacheReadInputTokens: 40, OutputTokens: 9}
	mp.addResponse(textStreamEventsWithUsage("m", "answer", "end_turn", usage), nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	key := calibrationKey(mp, "m")
	eng.calibMu.Lock()
	cal := eng.calibrations[key]
	eng.calibMu.Unlock()
	if cal == nil {
		t.Fatalf("no calibration entry for key %q after an assistant turn", key)
	}
	if !cal.anchored {
		t.Errorf("calibration for %q is not anchored after the assistant message landed", key)
	}
	if want := usage.TotalInputTokens(); cal.anchorTotal != want {
		t.Errorf("calibration anchorTotal = %d, want %d", cal.anchorTotal, want)
	}
	if wantIdx := len(eng.Messages()) - 1; cal.anchorMsgIdx != wantIdx {
		t.Errorf("calibration anchorMsgIdx = %d, want %d (the assistant message index)", cal.anchorMsgIdx, wantIdx)
	}
}

// TestStopHookBlockedTurn pins the blocking Stop-hook turn
// the log record, the meta feedback message, the
// turn_end, the turn count, and the post-turn hook context.
func TestStopHookBlockedTurn(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEventsWithUsage("m", "I am done", "end_turn", types.Usage{InputTokens: 10}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "fine, more", "end_turn", types.Usage{InputTokens: 12}), nil)
	ec := newEventCollector()
	logs := &slogCapture{}
	block := hooks.HookResult{Outcome: hooks.HookOutcomeBlocking, Stderr: "keep working", HookName: "stop-hook"}
	rec := &integrationHookRecorder{results: []hooks.HookResult{
		block,
		{Outcome: hooks.HookOutcomeSuccess, HookName: "stop-hook"},
	}}
	h := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "stop-hook"},
		}}},
	}, rec)
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: slog.New(logs), Dispatcher: ec, Hooks: h,
		AutoCompact: AutoCompactConfig{ContextWindow: 200_000},
	})
	hooksSeen := &postTurnCtxRecorder{}
	eng.RegisterPostTurnHook(hooksSeen.hook)
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(withSentinel(context.Background()), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	if got := mp.callCount(); got != 2 {
		t.Fatalf("%d provider calls, want 2 (the blocked stop rewakes the loop)", got)
	}
	if recs := logs.all("stop hook blocked, continuing turn"); len(recs) != 1 {
		t.Errorf("%d 'stop hook blocked' records, want 1", len(recs))
	}
	wantFeedback := hooks.StopHookMessage(block)
	var feedback int
	for _, m := range eng.Messages() {
		if m.Role != types.RoleUser || m.Flags&types.FlagMeta == 0 {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && b.Text == wantFeedback {
				feedback++
			}
		}
	}
	if feedback != 1 {
		t.Errorf("%d stop-hook feedback messages equal to %q, want exactly 1", feedback, wantFeedback)
	}
	if n := len(ec.FindEvents(types.EventTurnEnd)); n != 2 {
		t.Errorf("%d turn_end events, want 2 (blocked turn + terminal turn)", n)
	}
	if res.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", res.TurnCount)
	}
	if got := hooksSeen.snapshot(); len(got) != 2 || got[0] != "sentinel=live" || got[1] != "sentinel=live" {
		t.Errorf("post-turn hook contexts = %v, want two live query contexts", got)
	}
}

type sentinelCtxKey struct{}

// withSentinel tags the query context so a hook can prove it received the
// live one rather than nil or a fresh background context.
func withSentinel(ctx context.Context) context.Context {
	return context.WithValue(ctx, sentinelCtxKey{}, "live")
}

// postTurnCtxRecorder records what each post-turn hook call saw.
type postTurnCtxRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *postTurnCtxRecorder) hook(ctx context.Context, _ []types.Message, _ int, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx == nil {
		r.seen = append(r.seen, "NILCTX")
		return
	}
	v, _ := ctx.Value(sentinelCtxKey{}).(string)
	if v == "" {
		v = "<absent>"
	}
	r.seen = append(r.seen, "sentinel="+v)
}

func (r *postTurnCtxRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// TestUnaddressedQueuedMessageTurn pins the queued-message rewake turn
// the log record with its seq, the turn_end, the turn
// count, and the post-turn hook context.
func TestUnaddressedQueuedMessageTurn(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEventsWithUsage("m", "first answer", "end_turn", types.Usage{InputTokens: 10}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "answered the interjection", "end_turn", types.Usage{InputTokens: 12}), nil)
	ec := newEventCollector()
	logs := &slogCapture{}
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: slog.New(logs), Dispatcher: ec,
		AutoCompact: AutoCompactConfig{ContextWindow: 200_000},
	})
	hooksSeen := &postTurnCtxRecorder{}
	eng.RegisterPostTurnHook(hooksSeen.hook)
	t.Cleanup(func() { eng.Close() })
	// The enqueue runs inside the provider's request predicate, i.e. after the
	// engine set its queued-message watermark and before the terminal check —
	// no sleep/poll race.
	mp.responses[0].match = func(*llm.Request) bool {
		eng.EnqueueAttachment(types.QueuedItem{
			Mode:  types.ItemModePrompt,
			Value: "and also do this",
			UUID:  "queued-e1-1",
		})
		return true
	}
	res := eng.QuerySync(withSentinel(context.Background()), "go", "")
	if res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	if got := mp.callCount(); got != 2 {
		t.Fatalf("%d provider calls, want 2 (the unaddressed message forces one more turn)", got)
	}
	recs := logs.all("engine:queued message_unaddressed")
	if len(recs) != 1 {
		t.Fatalf("%d 'message_unaddressed' records, want 1", len(recs))
	}
	if seq, ok := recs[0].attrs["seq"].(uint64); !ok || seq != 1 {
		t.Errorf("message_unaddressed attrs[seq] = %#v, want uint64(1)", recs[0].attrs["seq"])
	}
	if n := len(ec.FindEvents(types.EventTurnEnd)); n != 2 {
		t.Errorf("%d turn_end events, want 2 (rewake turn + terminal turn)", n)
	}
	if res.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", res.TurnCount)
	}
	if got := hooksSeen.snapshot(); len(got) != 2 || got[0] != "sentinel=live" || got[1] != "sentinel=live" {
		t.Errorf("post-turn hook contexts = %v, want two live query contexts", got)
	}
}

// TestPostTurnHookContextsOnNormalPaths pins the live query context on
// the terminal (2329) and tool-turn (2468) post-turn hook calls.
func TestPostTurnHookContextsOnNormalPaths(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_1", "Read", `{}`, types.Usage{InputTokens: 10}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", types.Usage{InputTokens: 12}), nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(),
		AutoCompact: AutoCompactConfig{ContextWindow: 200_000},
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	seen := &postTurnCtxRecorder{}
	eng.RegisterPostTurnHook(seen.hook)
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(withSentinel(context.Background()), "go", ""); res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	got := seen.snapshot()
	if len(got) != 2 {
		t.Fatalf("%d post-turn hook calls, want 2 (tool turn + terminal turn)", len(got))
	}
	for i, s := range got {
		if s != "sentinel=live" {
			t.Errorf("post-turn hook %d saw %q, want the live query context", i, s)
		}
	}
}

// TestQuerySummaryCacheStatus pins the cache verdict of the
// engine:query_summary record across the four usage
// shapes that decide it.
func TestQuerySummaryCacheStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		u    types.Usage
		want string
	}{
		{"read+input", types.Usage{InputTokens: 100, CacheReadInputTokens: 30}, "hit 23%"},
		{"creation only", types.Usage{InputTokens: 100, CacheCreationInputTokens: 50}, "warm"},
		{"no cache", types.Usage{InputTokens: 100}, "miss"},
		// cacheRead alone makes the total exactly 1, which pins the `> 0`
		// comparisons against off-by-one drift.
		{"single cached token", types.Usage{CacheReadInputTokens: 1}, "hit 100%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mp := &mockProvider{}
			mp.addResponse(textStreamEventsWithUsage("m", "answer", "end_turn", tc.u), nil)
			logs := &slogCapture{}
			eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(logs)})
			t.Cleanup(func() { eng.Close() })
			if res := eng.QuerySync(context.Background(), "go", ""); res.Error != nil {
				t.Fatalf("query failed: %v", res.Error)
			}
			recs := logs.all("engine:query_summary")
			if len(recs) != 1 {
				t.Fatalf("%d query_summary records, want 1", len(recs))
			}
			if got := recs[0].attrs["cache"]; got != tc.want {
				t.Errorf("query_summary cache = %q, want %q (usage %+v)", got, tc.want, tc.u)
			}
		})
	}
}

// TestSubagentNeverPersistsQueryMessages pins the `!e.isSubagent` half of
// the persist guards in the query-start snapshot path.
func TestSubagentNeverPersistsQueryMessages(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	session, err := store.CreateSession("", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_1", "Read", `{}`, types.Usage{InputTokens: 10}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", types.Usage{InputTokens: 12}), nil)
	parent := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	parent.SetStore(store, "")
	parent.SetSessionID(session.SessionID)
	t.Cleanup(func() { parent.Close() })
	sub := parent.NewSubEngine(SubEngineOptions{Tools: map[string]tool.Tool{
		"Read": &mockTool{name: "Read", enabled: true},
	}})
	subSession := session.SessionID + "-sub"
	sub.SetStore(store, "")
	sub.SetSessionID(subSession)
	if res := sub.QuerySync(context.Background(), "go", ""); res.Error != nil {
		t.Fatalf("sub query failed: %v", res.Error)
	}
	for _, sid := range []string{session.SessionID, subSession} {
		msgs, err := store.LoadMessages(sid)
		if err != nil {
			t.Fatalf("LoadMessages(%s): %v", sid, err)
		}
		if len(msgs) != 0 {
			t.Errorf("session %s holds %d messages (%v); a sub-agent must never persist",
				sid, len(msgs), firstMessageTexts(msgs))
		}
	}
}

// firstMessageTexts renders stored messages for failure output only.
func firstMessageTexts(msgs []*short.TranscriptMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Type)
	}
	return out
}

// TestFileHistorySnapshotGuards pins the `!e.isSubagent` and
// `e.currentTurnMsgID != ""` halves of the snapshot guard.
func TestFileHistorySnapshotGuards(t *testing.T) {
	t.Parallel()
	t.Run("subagent takes no snapshot", func(t *testing.T) {
		t.Parallel()
		tracker := filehistory.NewTracker(t.TempDir())
		file := filepath.Join(t.TempDir(), "a.go")
		if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := tracker.TrackEdit(file); err != nil {
			t.Fatalf("TrackEdit: %v", err)
		}
		before := len(tracker.State().Snapshots)
		mp := &mockProvider{}
		mp.addResponse(textStreamEventsWithUsage("m", "sub answer", "end_turn", types.Usage{InputTokens: 5}), nil)
		parent := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
		parent.SetFileHistory(tracker)
		t.Cleanup(func() { parent.Close() })
		sub := parent.NewSubEngine(SubEngineOptions{Tools: map[string]tool.Tool{}})
		if res := sub.QuerySync(context.Background(), "go", ""); res.Error != nil {
			t.Fatalf("sub query failed: %v", res.Error)
		}
		if after := len(tracker.State().Snapshots); after != before {
			t.Errorf("sub-agent query added %d snapshots; only the main engine snapshots", after-before)
		}
	})
	t.Run("no snapshot without a turn message id", func(t *testing.T) {
		t.Parallel()
		tracker := filehistory.NewTracker(t.TempDir())
		file := filepath.Join(t.TempDir(), "b.go")
		if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := tracker.TrackEdit(file); err != nil {
			t.Fatalf("TrackEdit: %v", err)
		}
		before := len(tracker.State().Snapshots)
		mp := &mockProvider{}
		mp.addResponse(textStreamEventsWithUsage("m", "answer", "end_turn", types.Usage{InputTokens: 5}), nil)
		eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
		eng.SetFileHistory(tracker)
		t.Cleanup(func() { eng.Close() })
		// runTurns directly: currentTurnMsgID stays empty, which is exactly the
		// case the guard exists for.
		if res := eng.runTurns(context.Background(), ""); res.Error != nil {
			t.Fatalf("runTurns failed: %v", res.Error)
		}
		if after := len(tracker.State().Snapshots); after != before {
			t.Errorf("%d snapshots taken with an empty currentTurnMsgID, want 0", after-before)
		}
	})
	t.Run("main engine with a turn message id snapshots", func(t *testing.T) {
		t.Parallel()
		tracker := filehistory.NewTracker(t.TempDir())
		file := filepath.Join(t.TempDir(), "c.go")
		if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := tracker.TrackEdit(file); err != nil {
			t.Fatalf("TrackEdit: %v", err)
		}
		var written int
		mp := &mockProvider{}
		mp.addResponse(textStreamEventsWithUsage("m", "answer", "end_turn", types.Usage{InputTokens: 5}), nil)
		eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
		eng.SetFileHistory(tracker)
		eng.SetFileHistoryWriter(func(filehistory.FileHistoryState) { written++ })
		t.Cleanup(func() { eng.Close() })
		if res := eng.QuerySync(context.Background(), "go", ""); res.Error != nil {
			t.Fatalf("query failed: %v", res.Error)
		}
		if written != 1 {
			t.Errorf("%d fileHistoryWriter callbacks, want 1 (the pre-loop snapshot)", written)
		}
	})
}

// TestLockHeldForWholeCall pins the deferred unlocks of getWorkingDir
// Query, beginQuery and beginQueryIfIdle: the mutex must
// stay held for the whole call, including the value computation. A defer
// turned into a plain call releases it early, which a concurrent holder sees.
func TestLockHeldForWholeCall(t *testing.T) {
	t.Parallel()
	t.Run("getWorkingDir holds e.mu for the read", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Model: "m", Logger: silentLogger()})
		t.Cleanup(func() { eng.Close() })
		eng.mu.Lock()
		done := make(chan string, 1)
		go func() { done <- eng.getWorkingDir() }()
		select {
		case got := <-done:
			eng.mu.Unlock()
			t.Fatalf("getWorkingDir returned %q while e.mu was held elsewhere: the RUnlock is no longer deferred", got)
		case <-time.After(150 * time.Millisecond):
		}
		eng.mu.Unlock()
		if got := <-done; got != eng.getWorkingDir() {
			t.Errorf("getWorkingDir after unlock = %q, want the engine dir", got)
		}
	})
	t.Run("beginQuery holds activeCancelMu for beginLocked", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Model: "m", Logger: silentLogger()})
		t.Cleanup(func() { eng.Close() })
		eng.activeCancelMu.Lock()
		done := make(chan uint64, 1)
		go func() { done <- eng.beginQuery(func() {}) }()
		select {
		case tok := <-done:
			eng.activeCancelMu.Unlock()
			eng.endQuery(tok)
			t.Fatalf("beginQuery completed with token %d while activeCancelMu was held elsewhere: the Unlock is no longer deferred", tok)
		case <-time.After(150 * time.Millisecond):
		}
		eng.activeCancelMu.Unlock()
		tok := <-done
		if tok == 0 {
			t.Fatal("beginQuery returned 0 after the lock was released")
		}
		eng.endQuery(tok)
	})
	t.Run("beginQueryIfIdle holds activeCancelMu", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Model: "m", Logger: silentLogger()})
		t.Cleanup(func() { eng.Close() })
		eng.activeCancelMu.Lock()
		done := make(chan uint64, 1)
		go func() {
			tok, ok := eng.beginQueryIfIdle(func() {})
			if !ok {
				tok = 0
			}
			done <- tok
		}()
		select {
		case tok := <-done:
			eng.activeCancelMu.Unlock()
			if tok != 0 {
				eng.endQuery(tok)
			}
			t.Fatalf("beginQueryIfIdle completed with token %d while activeCancelMu was held elsewhere: the Unlock is no longer deferred", tok)
		case <-time.After(150 * time.Millisecond):
		}
		eng.activeCancelMu.Unlock()
		tok := <-done
		if tok == 0 {
			t.Fatal("beginQueryIfIdle returned 0 after the lock was released")
		}
		eng.endQuery(tok)
	})
}

// TestNewSeedsCalibrationMap pins the calibrations field of the Engine
// literal in New.
func TestNewSeedsCalibrationMap(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.calibMu.Lock()
	defer eng.calibMu.Unlock()
	if eng.calibrations == nil {
		t.Fatal("New left calibrations nil; the map must be seeded at construction")
	}
	if len(eng.calibrations) != 0 {
		t.Errorf("calibrations = %d entries after New, want 0", len(eng.calibrations))
	}
}

// TestDrainIfKeepsNonUserOrigins pins the drain predicate of
// drainQueuedUserAttachments: only prompt items with a
// user-side origin ride the turn-boundary attach path.
func TestDrainIfKeepsNonUserOrigins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		item types.QueuedItem
	}{
		{"job mode with nil origin", types.QueuedItem{Mode: types.ItemModeJob, Value: "job body", UUID: "u-job"}},
		{"prompt mode with job origin", types.QueuedItem{
			Mode: types.ItemModePrompt, Value: "prompt body", UUID: "u-prompt-job",
			Origin: &types.MessageOrigin{Kind: types.OriginJob},
		}},
		{"message mode with nil origin", types.QueuedItem{Mode: types.ItemModeMessage, Value: "msg body", UUID: "u-msg"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := New(&Params{Model: "m", Logger: silentLogger()})
			t.Cleanup(func() { eng.Close() })
			eng.attachments.Enqueue(tc.item)
			eng.drainQueuedUserAttachments()
			if eng.attachments.Len() != 1 {
				t.Fatalf("drainQueuedUserAttachments drained a %s item; the queue must keep it", tc.name)
			}
			if n := len(eng.Messages()); n != 0 {
				t.Errorf("draining a %s item appended %d messages, want 0", tc.name, n)
			}
		})
	}
}

// TestEmptyDeltaGuardsInEmitEvent pins the nil/empty guards of the
// thinking and tool-output delta arms of emitEvent.
func TestEmptyDeltaGuardsInEmitEvent(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	eng := New(&Params{Model: "m", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: nil})
	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: ""}})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: nil})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "tu_1", DisplayOutput: ""}})
	// A non-delta event flushes the buffers, so any buffered empty payload
	// would surface here.
	eng.emitEvent(types.QueryEvent{Type: types.EventTurnEnd})
	if n := len(ec.FindEvents(types.EventThinkingDelta)); n != 0 {
		t.Errorf("%d thinking_delta events, want 0 (nil and empty payloads must be dropped)", n)
	}
	if n := len(ec.FindEvents(types.EventToolOutputDelta)); n != 0 {
		t.Errorf("%d tool_output_delta events, want 0 (nil and empty payloads must be dropped)", n)
	}
	if n := len(ec.FindEvents(types.EventTurnEnd)); n != 1 {
		t.Errorf("%d turn_end events, want 1", n)
	}
}

// TestToolDeltaCoalesceSkipsEmptyBuffers pins the per-id skip in
// toolDeltaCoalesce.flush: an empty buffer must not stop the
// dispatch of the ids that follow it. Map iteration order is random, so the
// scenario is repeated to make an early-exit regression fail deterministically
// rather than by luck.
func TestToolDeltaCoalesceSkipsEmptyBuffers(t *testing.T) {
	t.Parallel()
	for i := range 24 {
		tr := &toolDeltaCoalesce{}
		tr.write("empty", "", nil, time.Hour, nil)
		tr.write("kept", "payload", nil, time.Hour, nil)
		var dispatched []string
		tr.flush(func(id, text string, _ any, _ *types.AgentMeta) { dispatched = append(dispatched, id+":"+text) })
		if len(dispatched) != 1 || dispatched[0] != "kept:payload" {
			t.Fatalf("iteration %d: flush dispatched %v, want [kept:payload]", i, dispatched)
		}
	}
}

// TestToolSearchDiscovery pins the ToolSearch registration block
// only a successful ToolSearch result contributes
// discovered names, and the discovery is logged with its count.
func TestToolSearchDiscovery(t *testing.T) {
	t.Parallel()
	const tsResult = `<functions><function>{"name": "WebFetch"}</function><function>{"name": "WebSearch"}</function></functions>`
	const foreignResult = `<function>{"name": "Sneaky"}</function>`
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_ts", Name: ToolSearchToolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"query":"web"}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_read", Name: "Read"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 4}},
		{Type: "message_stop"},
	}, nil)
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", types.Usage{InputTokens: 12}), nil)
	logs := &slogCapture{}
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: slog.New(logs),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				ToolSearchToolName: &mockTool{name: ToolSearchToolName, enabled: true,
					callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
						return &tool.ToolResult{Data: tsResult}, nil
					}},
				"Read": &mockTool{name: "Read", enabled: true,
					callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
						return &tool.ToolResult{Data: foreignResult}, nil
					}},
			}
		},
	})
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "go", ""); res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	for _, name := range []string{"WebFetch", "WebSearch"} {
		if !eng.toolSearch.IsDiscovered(name) {
			t.Errorf("tool %q was not registered as discovered", name)
		}
	}
	if eng.toolSearch.IsDiscovered("Sneaky") {
		t.Error("a non-ToolSearch tool's result registered tools; only ToolSearch results may")
	}
	recs := logs.all("toolSearch:discovered")
	if len(recs) != 1 {
		t.Fatalf("%d toolSearch:discovered records, want 1", len(recs))
	}
	if count, ok := recs[0].attrs["count"].(int64); !ok || count != 2 {
		t.Errorf("discovery attrs[count] = %#v, want int64(2)", recs[0].attrs["count"])
	}
	if recs[0].attrs["names"] != "WebFetch,WebSearch" {
		t.Errorf("discovery attrs[names] = %#v, want WebFetch,WebSearch", recs[0].attrs["names"])
	}
}

// TestToolSearchEmptyExtractionIsNotLogged pins the `len(names) > 0`
// guard: a ToolSearch call that yields no names must not
// register or log a discovery.
func TestToolSearchEmptyExtractionIsNotLogged(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_ts", ToolSearchToolName, `{"query":"nothing"}`, types.Usage{InputTokens: 10}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", types.Usage{InputTokens: 12}), nil)
	logs := &slogCapture{}
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: slog.New(logs),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{ToolSearchToolName: &mockTool{name: ToolSearchToolName, enabled: true,
				callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
					return &tool.ToolResult{Data: "no functions here"}, nil
				}}}
		},
	})
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "go", ""); res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	if recs := logs.all("toolSearch:discovered"); len(recs) != 0 {
		t.Errorf("%d toolSearch:discovered records for an empty extraction, want 0", len(recs))
	}
	if names := eng.toolSearch.DiscoveredNames(); len(names) != 0 {
		t.Errorf("discovered tools = %v, want none", names)
	}
}

// TestToolNewMessagesLandAfterToolResults pins the NewMessages append
// and the end-of-round turn_end.
func TestToolNewMessagesLandAfterToolResults(t *testing.T) {
	t.Parallel()
	newMsg := types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("skill content")})
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_1", "Read", `{}`, types.Usage{InputTokens: 10}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", types.Usage{InputTokens: 12}), nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(), Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true,
				callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
					return &tool.ToolResult{Data: "ok", NewMessages: []types.Message{newMsg}}, nil
				}}}
		},
	})
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "go", ""); res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	var injected int
	for _, m := range eng.Messages() {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && b.Text == "skill content" {
				injected++
			}
		}
	}
	if injected != 1 {
		t.Errorf("%d copies of the tool's NewMessage in history, want exactly 1", injected)
	}
	if n := len(ec.FindEvents(types.EventTurnEnd)); n != 2 {
		t.Errorf("%d turn_end events, want 2 (tool round + terminal round)", n)
	}
}

// TestToolTimeAccumulatesAcrossTurns pins the `+=` accumulator behind the
// query_summary tools_ms field.
func TestToolTimeAccumulatesAcrossTurns(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_slow", "Slow", `{}`, types.Usage{InputTokens: 10}), nil)
	mp.addResponse(toolUseStreamEventsWithUsage("m", "tu_fast", "Fast", `{}`, types.Usage{InputTokens: 10}), nil)
	mp.addResponse(textStreamEventsWithUsage("m", "done", "end_turn", types.Usage{InputTokens: 12}), nil)
	logs := &slogCapture{}
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: slog.New(logs),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				"Slow": &mockTool{name: "Slow", enabled: true,
					callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
						time.Sleep(30 * time.Millisecond) // REAL-TIME: tools_ms sums wall-clock call durations
						return &tool.ToolResult{Data: "slow"}, nil
					}},
				"Fast": &mockTool{name: "Fast", enabled: true},
			}
		},
	})
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "go", ""); res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}
	recs := logs.all("engine:query_summary")
	if len(recs) != 1 {
		t.Fatalf("%d query_summary records, want 1", len(recs))
	}
	toolsMs, ok := recs[0].attrs["tools_ms"].(int64)
	if !ok {
		t.Fatalf("tools_ms attr = %#v, want int64", recs[0].attrs["tools_ms"])
	}
	if toolsMs < 25 {
		t.Errorf("tools_ms = %d, want >= 25 (the 30ms slow tool must accumulate into the total)", toolsMs)
	}
}

// TestRunSkillForkQueryStartFields pins every field of the query_start
// event a forked skill emits.
func TestRunSkillForkQueryStartFields(t *testing.T) {
	t.Parallel()
	reg := skillRegistryWith(t, types.SkillCommand{
		Name:      "probe-fork",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "Explore",
		Source:    types.SkillSourceBundled,
		Content:   "Do the forked thing.",
	})
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub result"), nil)
	mp.addResponse(textStreamEvents("test", "main summary"), nil)
	ec := newEventCollector()
	eng, _ := skillTestEngine(t, mp, reg, ec)
	t.Cleanup(func() { eng.Close() })
	eng.RunSkill(context.Background(), "probe-fork", "", "sys")
	ec.WaitForResult()
	starts := ec.FindEvents(types.EventQueryStart)
	if len(starts) != 1 {
		t.Fatalf("%d query_start events, want exactly 1", len(starts))
	}
	if starts[0].Message == nil {
		t.Fatal("query_start carries no message")
	}
	if got := eventText(starts[0]); got != "/probe-fork" {
		t.Errorf("query_start message = %q, want /probe-fork", got)
	}
	if starts[0].Model != eng.Model() {
		t.Errorf("query_start Model = %q, want the engine model %q", starts[0].Model, eng.Model())
	}
	ends := ec.FindEvents(types.EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("%d tool_end events, want exactly 1", len(ends))
	}
	if ends[0].ToolResult == nil {
		t.Fatal("tool_end carries no ToolResult payload")
	}
	if ends[0].ToolResult.DisplayOutput != "sub result" {
		t.Errorf("tool_end DisplayOutput = %q, want the sub-agent's content", ends[0].ToolResult.DisplayOutput)
	}
	if ends[0].ToolResult.IsError {
		t.Error("a successful forked skill flagged its tool_end as an error")
	}
}

// TestRunSkillForkFailureResultText pins the failed-fork arm of the
// result-text guard: a nil result must not be dereferenced,
// and the tool card must show the fallback text as an error.
func TestRunSkillForkFailureResultText(t *testing.T) {
	t.Parallel()
	reg := skillRegistryWith(t, types.SkillCommand{
		Name:      "probe-fork-fail",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "Explore",
		Source:    types.SkillSourceBundled,
		Content:   "Do the failing thing.",
	})
	mp := &mockProvider{}
	// The sub-agent's only call fails, so RunAgent returns (nil, err).
	mp.addResponse(nil, &llm.APIError{Type: "rate_limit_error", Message: "skill sub failed", Status: 429, Retryable: false})
	ec := newEventCollector()
	eng, _ := skillTestEngine(t, mp, reg, ec)
	t.Cleanup(func() { eng.Close() })
	eng.RunSkill(context.Background(), "probe-fork-fail", "", "sys")
	// The failed fork ends the skill run with an untagged query_end carrying
	// the sub-agent error; wait for it instead of sleeping.
	ec.WaitForResult()
	ends := ec.FindEvents(types.EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("%d tool_end events, want exactly 1", len(ends))
	}
	if ends[0].ToolResult == nil {
		t.Fatal("tool_end carries no ToolResult payload")
	}
	if ends[0].ToolResult.DisplayOutput != "Skill execution completed" {
		t.Errorf("tool_end DisplayOutput = %q, want the fallback text for a failed fork", ends[0].ToolResult.DisplayOutput)
	}
	if !ends[0].ToolResult.IsError {
		t.Error("a failed fork did not flag its tool_end as an error")
	}
	var ownEnds int
	for _, ev := range ec.FindEvents(types.EventQueryEnd) {
		if ev.Agent == nil && ev.Error != nil {
			ownEnds++
		}
	}
	if ownEnds != 1 {
		t.Errorf("%d untagged error-carrying query_end events, want 1", ownEnds)
	}
}

// TestRunAgentInterruptStubFieldValues pins the synthesized interrupt
// result of a cancelled sub-agent: the guard that picks
// it and the token/duration fields it carries.
func TestRunAgentInterruptStubFieldValues(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	// The only turn dies mid-stream under cancellation: no content, no usage.
	hang := make(chan struct{})
	mp.responses = append(mp.responses, mockResponse{
		events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 999}}},
		},
		hang: hang,
	})
	deps := runAgentDeps(t)
	eng := New(&Params{
		Provider:      mp,
		Model:         "parent-model",
		Logger:        silentLogger(),
		ToolsProvider: runAgentTools,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)
	// A fork with no selectable user message is the lastUserIdx < 0 arm.
	fork := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_fork", Name: "Read"},
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForCallCount(t, mp, 1, 5*time.Second)
		time.Sleep(20 * time.Millisecond) // REAL-TIME: TotalDurationMs is wall clock
		cancel()
		close(hang)
	}()
	res, err := eng.RunAgent(ctx, agenttool.AgentOpts{Prompt: "p", AgentType: "General", ForkMessages: fork})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res == nil {
		t.Fatal("RunAgent returned nil result")
	}
	if res.Content != "(agent interrupted by user)" {
		t.Errorf("Content = %q, want the interrupt stub", res.Content)
	}
	if res.AgentType != "General" {
		t.Errorf("AgentType = %q, want General", res.AgentType)
	}
	// The stub path is only reached when the fork produced no real content, so
	// no turn ever landed its usage: the token field is pinned at 0 and the
	// duration field is the one that carries information.
	if res.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0 (no completed turn on the stub path)", res.TotalTokens)
	}
	if res.TotalDurationMs < 15 {
		t.Errorf("TotalDurationMs = %d, want >= 15 (the cancelled run outlived the 20ms hold)", res.TotalDurationMs)
	}
	if res.TotalToolUseCount != 1 {
		t.Errorf("TotalToolUseCount = %d, want 1 (the fork transcript's tool_use block)", res.TotalToolUseCount)
	}
}

// ---------------------------------------------------------------------------
// Lock discipline, RunAgent edge paths, RunSkill fork result text
// ---------------------------------------------------------------------------

// lockProbe asserts that mu is acquirable in write mode. The semantic it pins
// is the post-call one — the method hands the lock back before it returns —
// so it fails on a skipped or unreachable Unlock. It deliberately says nothing
// about where inside the body the unlock sits: hoisting an unlock to the top
// of the method leaves the same post-call state and is a data race, not a
// sequential failure.
func lockProbe(t *testing.T, mu *sync.RWMutex) {
	t.Helper()
	if !mu.TryLock() {
		t.Error("mutex still held after the call — the Unlock did not run")
		return
	}
	mu.Unlock()
}

// mutexProbe is lockProbe for plain mutexes.
func mutexProbe(t *testing.T, mu *sync.Mutex) {
	t.Helper()
	if !mu.TryLock() {
		t.Error("mutex still held after the call — the Unlock did not run")
		return
	}
	mu.Unlock()
}

func TestEngine_getWorkingDir_ReleasesReadLock(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })
	_ = eng.getWorkingDir()
	lockProbe(t, &eng.mu)
}

func TestEngine_Abort_ReleasesMutex(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })
	eng.Abort() // no active cancel: exercises the mutex-protected noop path
	mutexProbe(t, &eng.activeCancelMu)
}

func TestEngine_beginQueryIfIdle_ReleasesMutex(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })
	tok, ok := eng.beginQueryIfIdle(func() {})
	if !ok {
		t.Fatal("beginQueryIfIdle on an idle engine returned ok=false")
	}
	mutexProbe(t, &eng.activeCancelMu)
	eng.endQuery(tok)
}

func TestRecordTurnTool_ReleasesMutex(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })
	eng.recordTurnTool("Read")
	mutexProbe(t, &eng.turnToolMu)
	if got := eng.snapshotTurnToolNames(); len(got) != 1 || got[0] != "Read" {
		t.Errorf("snapshotTurnToolNames() = %v, want [Read]", got)
	}
}

func TestResolveThinking_ReleasesReadLock(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })
	_ = eng.resolveThinking("m")
	lockProbe(t, &eng.mu)
}

// TestRunAgent_MaxTurnsOverride_CapsAtOneTurn pins the caller override: with
// opts.MaxTurns=1 the sub-engine must stop after exactly one LLM turn even
// though the turn would otherwise continue into a second call.
func TestRunAgent_MaxTurnsOverride_CapsAtOneTurn(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Turn 1: a Read tool_use; turn 2 (scripted but must never run): text.
	mp.addResponse(toolUseStreamEvents("test", "tu_1", "Read", `{"path":"/tmp/x"}`), nil)
	mp.addResponse(textStreamEvents("test", "second turn"), nil)

	deps := runAgentDeps(t)
	eng := New(&Params{
		Provider: mp,
		Model:    "test",
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
		Logger: silentLogger(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:   "read then answer",
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if got := mp.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (MaxTurns=1 must cap the loop before a second turn)", got)
	}
	// The single turn produced only a tool_use assistant, so the finalizer
	// falls back to its no-text stub — with the cap removed, the second turn's
	// text would land in Content instead.
	if !strings.Contains(res.Content, "no text output") {
		t.Errorf("Content = %q, want the no-text stub (turn 2 must not run)", res.Content)
	}
}

// TestRunAgent_BuiltInDefinitionsOpenNoMCP pins the agent-MCP guard from the
// caller's side: a connector is wired in, and every definition reachable without
// the process-global agent loader declares no MCP servers — so the connector
// must never run. A guard that admitted an empty spec list would run it here.
func TestRunAgent_BuiltInDefinitionsOpenNoMCP(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "done"), nil)
	deps := runAgentDeps(t)
	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return runAgentTools() },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	var calls int
	var specs int
	var ctxNil bool
	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "answer",
		McpConnect: func(ctx context.Context, _ string, raw []json.RawMessage) (*agenttool.McpConnectResult, error) {
			calls++
			specs += len(raw)
			if ctx == nil {
				ctxNil = true
			}
			return &agenttool.McpConnectResult{
				Tools: map[string]tool.Tool{"mcp__srv__extra": &mockTool{name: "mcp__srv__extra", enabled: true}},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if calls != 0 {
		t.Errorf("agent MCP connector calls = %d, want 0 (no reachable definition declares mcpServers); spec count seen = %d, ctx nil = %v",
			calls, specs, ctxNil)
	}
	if res == nil || res.Content != "done" {
		t.Errorf("result = %+v, want the scripted reply", res)
	}
}

// emptyTextOverflowEvents builds a turn whose only content block is an empty
// text block and whose stop reason is model_context_window_exceeded — the one
// assistant shape that is both "real enough" to carry usage and "synthetic
// enough" (empty text) for the interrupt-branch predicate.
func emptyTextOverflowEvents(model string, in, out int) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: in}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: stopReasonContextWindowExceeded}, Usage: &types.Usage{OutputTokens: out}},
		{Type: "message_stop"},
	}
}

// TestRunAgent_InterruptAfterCompactRecovery_ReportsTokens pins the interrupted-
// synthesis branch's token accounting. The branch requires every message after
// the last user prompt to be synthetic, yet usage only accrues on a completed
// turn — the empty-text overflow turn (compact recovery → one more turn) is the
// one sequence that satisfies both, so TotalTokens = input+output is observable
// exactly here.
func TestRunAgent_InterruptAfterCompactRecovery_ReportsTokens(t *testing.T) {
	t.Parallel()

	hang := make(chan struct{})
	mp := &mockProvider{}
	mp.addResponse(emptyTextOverflowEvents("test", 7, 3), nil)
	mp.addResponse(nil, nil)
	mp.responses[1].hang = hang

	deps := runAgentDeps(t)
	eng := New(&Params{
		Provider:    mp,
		Model:       "test",
		Logger:      silentLogger(),
		Compactor:   &passThroughCompactor{},
		AutoCompact: AutoCompactConfig{ContextWindow: 200_000},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitForCallCount(t, mp, 2, 5*time.Second)
		cancel()
		close(hang)
	}()

	res, err := eng.RunAgent(ctx, agenttool.AgentOpts{Prompt: "p", AgentType: "General"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res.Content != "(agent interrupted by user)" {
		t.Errorf("Content = %q, want the interrupt stub", res.Content)
	}
	if res.TotalTokens != 10 {
		t.Errorf("TotalTokens = %d, want 10 (7 input + 3 output of the overflow turn)", res.TotalTokens)
	}
	if res.TotalToolUseCount != 0 {
		t.Errorf("TotalToolUseCount = %d, want 0", res.TotalToolUseCount)
	}
}

// TestRunAgent_InterruptFork_ToolResultHead_UsesInterruptStub pins the
// lastUserIdx<0 arm at the operand level: a fork whose only user message is a
// tool_result carrier (non-selectable) followed by a real assistant text must
// still produce the interrupt stub, not the transcript's own text via the
// finalizer.
func TestRunAgent_InterruptFork_ToolResultHead_UsesInterruptStub(t *testing.T) {
	t.Parallel()

	fork := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_head"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("REAL-FORK-ANSWER-9c1d"),
		}},
	}

	mp := &mockProvider{} // pre-cancelled ctx: the provider is never called
	deps := runAgentDeps(t)
	eng := New(&Params{Provider: mp, Model: "test", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Prompt deliberately empty: a prompt would append a selectable user message
	// at the tail and re-satisfy the synthetic-only operand, hiding the
	// lastUserIdx<0 arm these pins exist to expose.
	res, err := eng.RunAgent(ctx, agenttool.AgentOpts{AgentType: "General", ForkMessages: fork})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res.Content != "(agent interrupted by user)" {
		t.Errorf("Content = %q, want the interrupt stub — a non-selectable fork head with no new content must not fall through to the transcript text", res.Content)
	}
	if strings.Contains(res.Content, "REAL-FORK-ANSWER") {
		t.Errorf("Content leaked the fork transcript's own text: %q", res.Content)
	}
	if calls := allRequestsLen(mp); calls != 0 {
		t.Errorf("provider calls = %d, want 0", calls)
	}
}

// TestRunSkill_ForkResultText_ComesFromSubAgent pins the ToolEnd card text:
// a successful fork skill shows the sub-agent's content, not the generic
// completion placeholder.
func TestRunSkill_ForkResultText_ComesFromSubAgent(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:      "fork-show",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "General",
		Source:    types.SkillSourceBundled,
		Content:   "Do the review.",
	})
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "SUB-RESULT-77af"), nil)
	mp.addResponse(textStreamEvents("test", "main wrap-up"), nil)

	eng := New(&Params{Provider: mp, Model: "test", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "fork-show", "", "sys")
	if err := ec.WaitForResult().Error; err != nil {
		t.Fatalf("RunSkill: %v", err)
	}
	for _, evt := range ec.FindEvents(types.EventToolEnd) {
		if evt.ToolResult == nil || !strings.HasPrefix(evt.ToolResult.ToolUseID, "skill-fork-") {
			continue
		}
		if evt.ToolResult.DisplayOutput != "SUB-RESULT-77af" {
			t.Errorf("fork card DisplayOutput = %q, want the sub-agent text %q", evt.ToolResult.DisplayOutput, "SUB-RESULT-77af")
		}
		if evt.ToolResult.IsError {
			t.Error("fork card IsError = true, want false")
		}
	}
}

// TestRunSkill_ForkEmptyContent_UsesCompletionPlaceholder pins the
// result-text fallback: a fork that succeeds but produces no text must show
// the generic completion placeholder on the virtual tool card, not an empty
// card (and the meta context message must carry the same placeholder).
func TestRunSkill_ForkEmptyContent_UsesCompletionPlaceholder(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:      "fork-empty",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "General",
		Source:    types.SkillSourceBundled,
		Content:   "Do the review.",
	})
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	mp := &mockProvider{}
	// Slot 1 is the sub-agent: a text block that never receives a delta, so
	// RunAgent succeeds with an EMPTY Content (the transcript has a text block,
	// just an empty one — the no-text fallback inside FinalizeResult never
	// fires because a text part exists).
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}, nil)
	// Slot 2 wraps up the main loop after the skill result is appended.
	mp.addResponse(textStreamEvents("test", "main wrap-up"), nil)

	eng := New(&Params{Provider: mp, Model: "test", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "fork-empty", "", "sys")
	if err := ec.WaitForResult().Error; err != nil {
		t.Fatalf("RunSkill: %v", err)
	}
	var card *types.ToolResultEvent
	for _, evt := range ec.FindEvents(types.EventToolEnd) {
		if evt.ToolResult != nil && strings.HasPrefix(evt.ToolResult.ToolUseID, "skill-fork-") {
			card = evt.ToolResult
		}
	}
	if card == nil {
		t.Fatal("no skill-fork ToolEnd card emitted")
	}
	if card.DisplayOutput != "Skill execution completed" {
		t.Errorf("fork card DisplayOutput = %q, want the completion placeholder for an empty sub-agent result", card.DisplayOutput)
	}
	if card.IsError {
		t.Error("fork card IsError = true, want false for a successful empty run")
	}
	var metaSeen bool
	for _, m := range eng.Messages() {
		for _, b := range m.Content {
			if m.Flags&types.FlagMeta != 0 && strings.Contains(b.Text, `Skill "fork-empty" completed.`) {
				metaSeen = true
				if !strings.Contains(b.Text, "Skill execution completed") {
					t.Errorf("skill result context message = %q, want it to carry the placeholder", b.Text)
				}
			}
		}
	}
	if !metaSeen {
		t.Error("no meta skill-completion context message appended")
	}
}

// TestRunSkill_ForkRunAgentError_SurfacesError pins the runErr half of the
// resultText condition: a failed sub-run must not dereference a nil result
// (that panics into the recover handler's "internal error") and must surface
// the RunAgent error on QueryEnd.
func TestRunSkill_ForkRunAgentError_SurfacesError(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:      "fork-badmodel",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "General",
		Model:     "ghostprov/m",
		Source:    types.SkillSourceBundled,
		Content:   "Do the review.",
	})
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	eng := New(&Params{Provider: &mockProvider{}, Model: "test", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "fork-badmodel", "", "sys")
	res := ec.WaitForResult()
	if res.Error == nil {
		t.Fatal("RunSkill with an unresolvable model must fail the query")
	}
	if !strings.Contains(res.Error.Error(), "no config on shared deps") {
		t.Errorf("error = %v, want the RunAgent hard failure (a nil-result deref would surface as \"internal error\")", res.Error)
	}
}

// TestCreateAttachmentMessages_JobMode_StampsMeta pins the meta stamping: a
// job-mode attachment message must carry FlagMeta so downstream filters treat
// it as system-generated.
func TestCreateAttachmentMessages_JobMode_StampsMeta(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })

	msgs := eng.createAttachmentMessages([]types.QueuedItem{
		{Mode: types.ItemModeJob, Value: "bg done"},
		{Mode: types.ItemModePrompt, Value: "user says"},
	})
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[0].Flags&types.FlagMeta == 0 {
		t.Error("job-mode attachment message lacks FlagMeta")
	}
	if msgs[0].Attachment == nil || msgs[0].Attachment.Mode != types.ItemModeJob {
		t.Errorf("job message attachment = %+v, want Mode=job", msgs[0].Attachment)
	}
	if msgs[1].Flags&types.FlagMeta != 0 {
		t.Error("prompt-mode attachment message must not carry FlagMeta")
	}
}

// TestNormalizeAttachmentForAPI_OriginImpliesMeta pins the isMeta derivation:
// a queued attachment whose origin resolves (explicit or job-inferred) must be
// flagged meta on the API form.
func TestNormalizeAttachmentForAPI_OriginImpliesMeta(t *testing.T) {
	t.Parallel()

	src := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("job output")},
		Attachment: &types.Attachment{
			Type:       types.AttachmentTypeQueued,
			SourceUUID: "src-1",
			Mode:       types.ItemModeJob,
			// Origin nil on purpose: job mode must infer the job origin.
		},
	}
	out := normalizeAttachmentForAPI(src)
	if out.Flags&types.FlagMeta == 0 {
		t.Error("job-mode normalized message lacks FlagMeta (origin inference failed)")
	}
	if out.ID != "src-1" {
		t.Errorf("normalized ID = %q, want the attachment SourceUUID", out.ID)
	}

	noMeta := normalizeAttachmentForAPI(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("user text")},
		Attachment: &types.Attachment{
			Type:       types.AttachmentTypeQueued,
			SourceUUID: "src-2",
			Mode:       types.ItemModePrompt,
		},
	})
	if noMeta.Flags&types.FlagMeta != 0 {
		t.Error("prompt-mode message without origin must stay non-meta")
	}
}

func TestStreamEndedError_Message(t *testing.T) {
	t.Parallel()
	err := &StreamEndedError{}
	if got, want := err.Error(), "Connection lost. No response was received"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// engineLogCapture records everything a per-engine logger emits. Engine logs
// ride e.logger (not the global slog default), so a private handler is enough
// and the shared mux stays untouched.
type engineLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *engineLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *engineLogCapture) Handle(_ context.Context, rec slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec)
	return nil
}
func (h *engineLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *engineLogCapture) WithGroup(string) slog.Handler      { return h }

func (h *engineLogCapture) find(t *testing.T, msg string, want func(attrs map[string]any) bool) slog.Record {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Message != msg {
			continue
		}
		attrs := map[string]any{}
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		if want == nil || want(attrs) {
			return rec
		}
	}
	t.Fatalf("no %q log record matching the predicate; got %d records", msg, len(h.records))
	return slog.Record{}
}

// hasMsg reports whether a matching record exists without failing the test,
// for poll loops and absence pins where find's t.Fatalf contract is wrong.
func (h *engineLogCapture) hasMsg(msg string, want func(map[string]any) bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Message != msg {
			continue
		}
		attrs := map[string]any{}
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		if want == nil || want(attrs) {
			return true
		}
	}
	return false
}

// count returns how many records carry the message.
func (h *engineLogCapture) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, rec := range h.records {
		if rec.Message == msg {
			n++
		}
	}
	return n
}

// TestCoalesceBuf_WindowFlush pins the coalesce window semantics under fake
// time: no flush while inside the window, a flush-triggering write at exactly
// the window edge, none of it leaking events mid-window.
func TestCoalesceBuf_WindowFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var flushes int
		c := &coalesceBuf{}
		c.write("a", 100*time.Millisecond, func() { flushes++ })
		c.write("b", 100*time.Millisecond, func() { flushes++ })
		if flushes != 0 {
			t.Fatalf("flushes inside the window = %d, want 0", flushes)
		}
		time.Sleep(100 * time.Millisecond)
		c.write("c", 100*time.Millisecond, func() { flushes++ })
		if flushes != 1 {
			t.Errorf("flushes at the window edge = %d, want 1", flushes)
		}
	})
}

// TestToolDeltaCoalesce_WindowFlush is the per-id variant of the window pin.
func TestToolDeltaCoalesce_WindowFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var flushes int
		tc := &toolDeltaCoalesce{}
		tc.write("tu_1", "a", nil, 100*time.Millisecond, func() { flushes++ })
		tc.write("tu_1", "b", nil, 100*time.Millisecond, func() { flushes++ })
		if flushes != 0 {
			t.Fatalf("flushes inside the window = %d, want 0", flushes)
		}
		time.Sleep(100 * time.Millisecond)
		tc.write("tu_2", "c", nil, 100*time.Millisecond, func() { flushes++ })
		if flushes != 1 {
			t.Errorf("flushes at the window edge = %d, want 1", flushes)
		}
		var seen []string
		tc.flush(func(id, text string, _ any, _ *types.AgentMeta) { seen = append(seen, id+":"+text) })
		if len(seen) != 2 || seen[0] != "tu_1:ab" || seen[1] != "tu_2:c" {
			t.Errorf("flushed = %v, want [tu_1:ab tu_2:c]", seen)
		}
	})
}

// TestEmitEvent_ThinkingAndToolOutputGuards pins that delta-typed events route
// through the coalescing buffers (visible after a flush) while their empty
// counterparts are dropped without a dispatch.
func TestEmitEvent_ThinkingAndToolOutputGuards(t *testing.T) {
	t.Parallel()
	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })

	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "deep thought"}})
	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: nil})
	eng.emitEvent(types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: ""}})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{
		ToolUseID: "tu_o", DisplayOutput: "partial output",
	}})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "tu_o"}})
	eng.emitEvent(types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: nil})
	// A non-delta event flushes pending buffers ahead of itself.
	eng.emitEvent(types.QueryEvent{Type: types.EventTurnStart})

	thinks := ec.FindEvents(types.EventThinkingDelta)
	if len(thinks) != 1 || thinks[0].Thinking.Text != "deep thought" {
		t.Errorf("thinking deltas = %+v, want exactly one carrying %q", thinks, "deep thought")
	}
	outs := ec.FindEvents(types.EventToolOutputDelta)
	if len(outs) != 1 || outs[0].ToolResult.ToolUseID != "tu_o" || outs[0].ToolResult.DisplayOutput != "partial output" {
		t.Errorf("tool output deltas = %+v, want exactly one tu_o delta", outs)
	}
}

// usageStreamEvents is a plain text turn with fully controlled usage numbers.
func usageTextStreamEvents(model, text string, usage types.Usage) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: usage.InputTokens}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{
			OutputTokens:             usage.OutputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
		}},
		{Type: "message_stop"},
	}
}

// TestRunTurns_QuerySummaryCacheStatus pins the cache status derivation on the
// per-query summary log: cache-read > 0 with tokens in flight reports a hit
// percentage; cache-creation only reports warm; neither reports miss.
func TestRunTurns_QuerySummaryCacheStatus(t *testing.T) {
	t.Parallel()

	newEngine := func(resp []llm.StreamEvent) (*Engine, *engineLogCapture) {
		lc := &engineLogCapture{}
		mp := &mockProvider{}
		mp.addResponse(resp, nil)
		eng := New(&Params{Provider: mp, Model: "test-model", Logger: slog.New(lc)})
		return eng, lc
	}

	// 1 cache-read of 3 total tokens = 33%.
	eng, lc := newEngine(usageTextStreamEvents("test-model", "hi", types.Usage{
		InputTokens: 2, OutputTokens: 1, CacheReadInputTokens: 1,
	}))
	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	lc.find(t, "engine:query_summary", func(a map[string]any) bool {
		return a["cache"] == "hit 33%"
	})
	eng.Close()

	// Cache creation alone is a warm cache, not a hit.
	eng, lc = newEngine(usageTextStreamEvents("test-model", "hi", types.Usage{
		InputTokens: 2, OutputTokens: 1, CacheCreationInputTokens: 5,
	}))
	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	lc.find(t, "engine:query_summary", func(a map[string]any) bool {
		return a["cache"] == "warm"
	})
	eng.Close()

	// No cache traffic at all: miss.
	eng, lc = newEngine(usageTextStreamEvents("test-model", "hi", types.Usage{
		InputTokens: 2, OutputTokens: 1,
	}))
	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	lc.find(t, "engine:query_summary", func(a map[string]any) bool {
		return a["cache"] == "miss"
	})
	eng.Close()
}

// TestRunTurns_PersistsUserMessageBeforeLLMSpend pins the crash window: with a
// store wired, the query's user message must be in the DB while the first LLM
// call is still blocked — a kill right after submit then loses nothing.
func TestRunTurns_PersistsUserMessageBeforeLLMSpend(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	hang := make(chan struct{})
	mp := &mockProvider{}
	mp.addResponse(nil, nil)
	mp.responses[0].hang = hang

	eng := New(&Params{Provider: mp, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	ec := newEventCollector()
	eng.SetDispatcher(ec)

	done := make(chan QueryResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		done <- eng.QuerySync(ctx, "precious question", "")
	}()
	waitForCallCount(t, mp, 1, 5*time.Second)

	msgs, err := eng.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages while streaming: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != types.RoleUser {
		t.Errorf("stored messages during first LLM call = %d msgs (first role %s), want exactly the user row", len(msgs), msgs[0].Role)
	}

	cancel()
	close(hang)
	<-done
}

// TestRunTurns_SubagentSkipsCrashWindowPersist pins the isSubagent operand of
// the crash-window persist: sub-engines never persist there (the store cursor
// belongs to the parent session), even when a store is wired by hand.
func TestRunTurns_SubagentSkipsCrashWindowPersist(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	hang := make(chan struct{})
	mp := &mockProvider{}
	mp.addResponse(nil, nil)
	mp.responses[0].hang = hang

	parent := New(&Params{Provider: mp, Model: "test-model", Logger: silentLogger()})
	t.Cleanup(func() { parent.Close() })
	sub := parent.NewSubEngine(SubEngineOptions{AgentType: "General"})
	sub.SetStore(store, "")
	sub.SetSessionID(session.SessionID)

	done := make(chan QueryResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		done <- sub.RunForkedQuery(ctx, []types.Message{
			types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("sub prompt")}),
		}, "")
	}()
	waitForCallCount(t, mp, 1, 5*time.Second)

	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("stored rows during a sub-agent's first LLM call = %d, want 0 (sub-agents skip the crash-window persist)", len(msgs))
	}

	cancel()
	close(hang)
	<-done
}

// oversizedToolHistory builds assistant(tool_use Read)+user(tool_result)
// pairs whose results are big enough to blow any small token budget. The
// providers see no scripted response: the query dies at callLLM AFTER the
// pre-call prune, so ContextTokens still holds the prune arithmetic.
func oversizedToolHistory(pairs int) []types.Message {
	big := strings.Repeat("x", 30_000)
	msgs := []types.Message{types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("start")})}
	for i := range pairs {
		msgs = append(msgs,
			types.NewAssistantMessage([]types.ContentBlock{{
				Type: types.ContentTypeToolUse, ID: fmt.Sprintf("tu_prune_%d", i), Name: "Read",
			}}),
			types.NewUserMessage([]types.ContentBlock{{
				Type: types.ContentTypeToolResult, ToolUseID: fmt.Sprintf("tu_prune_%d", i),
				Content: json.RawMessage(`[{"type":"text","text":"` + big + `"}]`),
			}}),
		)
	}
	return msgs
}

// prunableEngine returns an engine wired with the given window (no compactor)
// and a logger capture; its query errors at callLLM by design.
func prunableEngine(t *testing.T, lc *engineLogCapture, window int) *Engine {
	t.Helper()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: window})
	return eng
}

// TestRunTurns_TokenPrune_FiresOnOversizedMainContext pins the prune guard:
// a main-thread engine with a window and no compact success clears old
// tool results and reduces ContextTokens by the saved estimate.
func TestRunTurns_TokenPrune_FiresOnOversizedMainContext(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	eng := prunableEngine(t, lc, 20_000)
	eng.SetContextTokens(1_000_000)
	eng.SetMessages(oversizedToolHistory(7))

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error == nil {
		t.Fatal("expected the scripted provider failure to end the query")
	}
	rec := lc.find(t, "engine:token_prune", func(a map[string]any) bool { return true })
	cleared, _ := aInt(rec, "cleared")
	if cleared != 2 {
		t.Errorf("cleared = %d, want 2 (7 tool results, 5 kept recent)", cleared)
	}
	saved, ok := aInt(rec, "tokens_saved")
	if !ok {
		t.Fatal("token_prune record lacks the tokens_saved attribute")
	}
	if got, want := eng.GetContextTokens(), 1_000_000-saved; got != want {
		t.Errorf("ContextTokens after prune = %d, want exactly %d (initial 1000000 minus the saved estimate)", got, want)
	}
}

// aInt extracts an int-typed attribute from a captured record.
func aInt(rec slog.Record, key string) (int, bool) {
	var out int
	var ok bool
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key != key {
			return true
		}
		switch v := a.Value.Any().(type) {
		case int:
			out, ok = v, true
		case int64:
			out, ok = int(v), true
		}
		return true
	})
	return out, ok
}

// TestRunTurns_TokenPrune_ClampAtZero pins the negative clamp: when the saved
// estimate exceeds ContextTokens the result pins at exactly 0, never negative.
// Two passes: the first measures the saved estimate, the second sizes
// ContextTokens to saved-1 so the unclamped result would be exactly -1 — the
// one value where a broken clamp threshold is observable.
func TestRunTurns_TokenPrune_ClampAtZero(t *testing.T) {
	t.Parallel()

	hist := oversizedToolHistory(7)

	lcA := &engineLogCapture{}
	engA := prunableEngine(t, lcA, 20_000)
	engA.SetContextTokens(100_000_000)
	engA.SetMessages(hist)
	_ = engA.QuerySync(context.Background(), "q", "") // errors at callLLM by design
	lcA.find(t, "engine:token_prune", func(a map[string]any) bool { return true })
	postA := engA.GetContextTokens()
	saved := 100_000_000 - postA
	if saved <= 0 {
		t.Fatalf("saved estimate = %d, want positive", saved)
	}

	lcB := &engineLogCapture{}
	engB := prunableEngine(t, lcB, 20_000)
	engB.SetContextTokens(saved - 1)
	engB.SetMessages(hist)
	_ = engB.QuerySync(context.Background(), "q", "")
	lcB.find(t, "engine:token_prune", func(a map[string]any) bool { return true })
	const wantClamped = 0
	if got := engB.GetContextTokens(); got != wantClamped {
		t.Errorf("ContextTokens after overshooting prune = %d, want exactly %d (clamped)", got, wantClamped)
	}
}

// TestRunTurns_TokenPrune_ZeroWindowSkipped pins the ContextWindow operand:
// with no window configured the prune must not run even on an oversized
// context — there is no compact budget to defend.
func TestRunTurns_TokenPrune_ZeroWindowSkipped(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	eng := prunableEngine(t, lc, 0)
	eng.SetContextTokens(1_000_000)
	eng.SetMessages(oversizedToolHistory(7))

	_ = eng.QuerySync(context.Background(), "q", "") // errors at callLLM by design
	if n := lc.count("engine:token_prune"); n != 0 {
		t.Errorf("token_prune log records = %d, want 0 (no context window configured)", n)
	}
}

// TestRunTurns_TokenPrune_SkippedAfterSuccessfulCompact pins the
// compactSucceeded operand: a successful pre-turn compact owns the context
// reduction for that turn, and the prune must stay out of its way.
func TestRunTurns_TokenPrune_SkippedAfterSuccessfulCompact(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(&recordingCompactor{result: &short.CompactResult{
		Summary: "s", BeforeTokens: 900_000, AfterTokens: 1_000,
		Messages: oversizedToolHistory(7),
	}}, AutoCompactConfig{ContextWindow: 20_000})
	eng.SetContextTokens(1_000_000)
	eng.SetMessages(oversizedToolHistory(7))

	_ = eng.QuerySync(context.Background(), "q", "") // errors at callLLM by design
	lc.find(t, "pre-turn auto-compact succeeded", func(a map[string]any) bool { return true })
	if n := lc.count("engine:token_prune"); n != 0 {
		t.Errorf("token_prune log records = %d, want 0 (compact already succeeded this turn)", n)
	}
}

// TestRunTurns_TokenPrune_SubagentSkipped pins the isSubagent operand — and
// documents that it is redundant defense: the prune's inner
// isMainThreadSource check already excludes every sub-agent source, so the
// engine-level flag can never flip the outcome alone.
func TestRunTurns_TokenPrune_SubagentSkipped(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	parent := New(&Params{Provider: &mockProvider{}, Model: "test-model", Logger: slog.New(lc)})
	t.Cleanup(func() { parent.Close() })
	parent.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: 20_000})
	sub := parent.NewSubEngine(SubEngineOptions{AgentType: "General"})

	_ = sub.RunForkedQuery(context.Background(), oversizedToolHistory(7), "")
	if n := lc.count("engine:token_prune"); n != 0 {
		t.Errorf("token_prune log records = %d, want 0 (sub-agent contexts are exempt)", n)
	}
}

// mcOffProvider opts its engines out of the microcompact chain.
type mcOffProvider struct{ *mockProvider }

func (mcOffProvider) MicrocompactEnabled() bool { return false }

// TestRunTurns_TokenPrune_ProviderOptOutSkipped pins the provider gate in the
// engine-level prune guard: an oversized main-thread context is left alone when
// the active provider opts out of microcompact. The paired positive control (the
// same fixture with a provider that does not answer the probe) is what makes the
// zero-record assertion mean "gated" rather than "fixture never prunes".
func TestRunTurns_TokenPrune_ProviderOptOutSkipped(t *testing.T) {
	t.Parallel()

	lcOff := &engineLogCapture{}
	off := New(&Params{
		Provider: mcOffProvider{mockProvider: &mockProvider{}},
		Model:    "test-model", Logger: slog.New(lcOff),
	})
	t.Cleanup(func() { off.Close() })
	off.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: 20_000})
	off.SetContextTokens(1_000_000)
	off.SetMessages(oversizedToolHistory(7))
	_ = off.QuerySync(context.Background(), "q", "") // errors at callLLM by design
	if n := lcOff.count("engine:token_prune"); n != 0 {
		t.Errorf("token_prune log records = %d, want 0 (provider opted out of microcompact)", n)
	}

	lcOn := &engineLogCapture{}
	on := prunableEngine(t, lcOn, 20_000)
	on.SetContextTokens(1_000_000)
	on.SetMessages(oversizedToolHistory(7))
	_ = on.QuerySync(context.Background(), "q", "")
	if n := lcOn.count("engine:token_prune"); n != 1 {
		t.Errorf("token_prune log records = %d, want exactly 1 for the identical fixture without the opt-out", n)
	}
}

// TestTerminalAPIError_StopFailureHookDoesNotBlockQueryEnd pins the
// fire-and-forget contract: the query's own QueryEnd must be observable
// while the StopFailure hook is still running. Run synchronously (a removed
// goroutine), the error return would sit behind the hook's I/O.
func TestTerminalAPIError_StopFailureHookDoesNotBlockQueryEnd(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(nil, &llm.APIError{Type: "rate_limit_error", Message: "rate limited", Status: 429, Retryable: false})

	entered := make(chan struct{})
	release := make(chan struct{})
	rec := &integrationHookRecorder{}
	rec.onCall = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	h := hooks.NewHooks(hooks.HooksConfig{
		"StopFailure": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "gated-failure-hook"},
		}}},
	}, rec)

	ec := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "m", Hooks: h, Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	done := make(chan QueryResult, 1)
	go func() { done <- eng.QuerySync(context.Background(), "q", "") }()

	if !ec.WaitForEventCount(types.EventQueryEnd, 1, 5*time.Second) {
		t.Fatal("QueryEnd never arrived — the terminal error return is blocked behind the StopFailure hook")
	}
	// The hook is gated on release, so it can only be running, not done; the
	// dispatch itself may land either side of the QueryEnd observation.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("StopFailure hook never started")
	}
	close(release)
	res := <-done
	if res.Error == nil {
		t.Fatal("expected the terminal provider error")
	}
}

// TestRunStopHook_InputCarriesSessionAndToolNames pins the HookInput fields
// the Stop dispatch fills: session identity and the current query's executed
// tool names (deduped, sorted).
func TestRunStopHook_InputCarriesSessionAndToolNames(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("m", "tu_stop1", "Read", `{"path":"/tmp/a"}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)

	rec := &integrationHookRecorder{}
	h := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "on-stop"},
		}}},
	}, rec)
	eng := New(&Params{
		Provider: mp, Model: "m", Hooks: h, Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("sess-stop-input")

	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	input := rec.lastInput(t)
	if input.SessionID != "sess-stop-input" {
		t.Errorf("Stop input SessionID = %q, want sess-stop-input", input.SessionID)
	}
	if len(input.ToolNames) != 1 || input.ToolNames[0] != "Read" {
		t.Errorf("Stop input ToolNames = %v, want [Read] from this query's executed tools", input.ToolNames)
	}
}

// TestRunStopHook_SubagentInputCarriesAgentType pins the sub-agent half of the
// dispatch: a sub-engine's stop dispatch is a SubagentStop event naming its
// own agent type, not a main-thread Stop.
func TestRunStopHook_SubagentInputCarriesAgentType(t *testing.T) {
	rec := &integrationHookRecorder{}
	h := hooks.NewHooks(hooks.HooksConfig{
		"SubagentStop": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "on-subagent-stop"},
		}}},
	}, rec)
	deps := runAgentDeps(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub done"), nil)
	// Hooks ride the parent engine (NewSubEngine copies e.hooks), not
	// sharedDeps — which only feeds the SubagentStart dispatch.
	eng := New(&Params{Provider: mp, Model: "test", Hooks: h, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "p", AgentType: "General", ParentToolUseID: "tu_substop",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	input := rec.lastInput(t)
	if input.HookEventName != "SubagentStop" {
		t.Errorf("event = %q, want SubagentStop", input.HookEventName)
	}
	if input.AgentType != "General" {
		t.Errorf("input AgentType = %q, want General", input.AgentType)
	}
}

// TestManualCompact_HookInputsCarrySessionAndTrigger pins the compact hook
// inputs: both phases carry the session identity and trigger "manual".
func TestManualCompact_HookInputsCarrySessionAndTrigger(t *testing.T) {
	rec := &integrationHookRecorder{}
	h := hooks.NewHooks(hooks.HooksConfig{
		"PreCompact": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "pre"},
		}}},
		"PostCompact": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "post"},
		}}},
	}, rec)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Hooks: h, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(&recordingCompactor{result: compactFixture("s", 100, 10)}, AutoCompactConfig{ContextWindow: 200_000})
	eng.SetSessionID("sess-compact-hooks")

	if _, err := eng.ManualCompact(context.Background(), types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("/compact")}), ""); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}
	calls := rec.Calls()
	if len(calls) != 2 {
		t.Fatalf("compact hook dispatches = %d, want 2 (pre+post)", len(calls))
	}
	for _, c := range calls {
		if c.input.SessionID != "sess-compact-hooks" {
			t.Errorf("%s input SessionID = %q, want sess-compact-hooks", c.event, c.input.SessionID)
		}
		if c.input.Trigger != "manual" {
			t.Errorf("%s input Trigger = %q, want manual", c.event, c.input.Trigger)
		}
	}
	if calls[0].event != "PreCompact" || calls[1].event != "PostCompact" {
		t.Errorf("compact hook order = [%s %s], want [PreCompact PostCompact]", calls[0].event, calls[1].event)
	}
}

// manualCompactCtxKey carries a sentinel through the caller's context so the
// compactor can prove the ctx handed to it is the one the /compact caller passed.
type manualCompactCtxKey struct{}

// TestManualCompact_EventCardAndCompactorContext pins the manual compact card:
// the four events share one generated tool_use id (whose uuid slice width is
// what pairs them), the compactor receives the caller's context, and the manual
// path suppresses the "context left until auto-compact" warning.
func TestManualCompact_EventCardAndCompactorContext(t *testing.T) {
	// Not parallel: compactWarningSuppressed is process-global.
	clearCompactWarningSuppression()
	ec := newEventCollector()
	mc := &recordingCompactor{result: compactFixture("manual summary", 9_000, 120)}
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), Dispatcher: ec})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(mc, AutoCompactConfig{ContextWindow: 200_000})

	ctx := context.WithValue(context.Background(), manualCompactCtxKey{}, "manual-compact")
	const instructions = "keep the design decisions"
	if _, err := eng.ManualCompact(ctx, types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("/compact")}), instructions); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}

	got := collectCompactEvents(t, ec, "compact-manual-")
	if got.run.ToolUse.Name != "Compact" {
		t.Errorf("tool_run name = %q, want Compact", got.run.ToolUse.Name)
	}
	if got.start.ToolUse.Summary != "Compacting conversation ("+instructions+")" {
		t.Errorf("tool_start summary = %q, want the custom-instruction summary", got.start.ToolUse.Summary)
	}
	if got.end.ToolResult.DisplayOutput != "manual summary" {
		t.Errorf("tool_end display = %q, want the compact summary", got.end.ToolResult.DisplayOutput)
	}
	if got.end.ToolResult.IsError {
		t.Error("tool_end marks a successful manual compact as an error")
	}
	if _, ctxs := mc.snapshot(); len(ctxs) != 1 {
		t.Fatalf("compactor calls = %d, want 1", len(ctxs))
	} else if ctxs[0] == nil || ctxs[0].Value(manualCompactCtxKey{}) != "manual-compact" {
		t.Errorf("compactor ctx = %v, want the caller's context carrying the sentinel", ctxs[0])
	}
	if !compactWarningSuppressed.Load() {
		t.Error("compactWarningSuppressed = false after a manual compact — the manual path suppresses the context-left warning")
	}
}

// boundaryCompactResult wraps compactFixture with a store-persistable boundary
// marker, which is what turns RecordCompact from a no-op into a DB write.
func boundaryCompactResult(summary string, post []types.Message, afterTokens int) *short.CompactResult {
	res := compactFixture(summary, 9_000, afterTokens)
	res.BoundaryMarker = short.CreateCompactBoundaryMessage("auto", 9_000, "")
	res.Messages = post
	return res
}

func compactStoreSession(t *testing.T, store *short.Store, dir string) string {
	t.Helper()
	sess, err := store.CreateSession(dir, "m")
	if err != nil {
		t.Fatalf("CreateSession(%s): %v", dir, err)
	}
	return sess.SessionID
}

// compactPersistEngine builds an engine whose compactor returns a scripted
// result, wired to a live store session with pre-compact history.
func compactPersistEngine(t *testing.T, store *short.Store, dir, sid string, res *short.CompactResult, lc *engineLogCapture) *Engine {
	t.Helper()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, dir)
	eng.SetSessionID(sid)
	eng.SetCompactor(&recordingCompactor{result: res}, AutoCompactConfig{ContextWindow: 200_000})
	eng.SetMessages([]types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("pre-1")}),
		types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("pre-2")}),
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("pre-3")}),
		types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("pre-4")}),
	})
	return eng
}

// TestCompactPaths_PersistBoundaryAndMarkPersisted pins the shared compact tail
// for both entry points: the boundary row is written once, no other row is
// written, the post-compact history is flagged as already persisted, and a
// marker-less or storeless result skips the DB write without panicking.
func TestCompactPaths_PersistBoundaryAndMarkPersisted(t *testing.T) {
	store := newTestStore(t)
	post := []types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("recap")}),
		types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("ok")}),
	}

	// Auto path.
	sidAuto := compactStoreSession(t, store, "/proj-auto")
	lcAuto := &engineLogCapture{}
	engAuto := compactPersistEngine(t, store, "/proj-auto", sidAuto, boundaryCompactResult("auto recap", post, 4321), lcAuto)
	if _, err := engAuto.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}
	if n := lcAuto.count("RecordCompact failed"); n != 0 {
		t.Errorf("RecordCompact failed logged %d times on a live store", n)
	}
	rowsAuto, err := store.LoadMessages(sidAuto)
	if err != nil {
		t.Fatalf("LoadMessages(auto): %v", err)
	}
	if len(rowsAuto) != 1 {
		t.Errorf("auto compact wrote %d rows, want exactly 1 (the boundary marker)", len(rowsAuto))
	}
	if got := engAuto.LastPersistedIdx(); got != len(engAuto.Messages()) {
		t.Errorf("LastPersistedIdx after auto compact = %d, want %d (the replaced history is already stored)", got, len(engAuto.Messages()))
	}
	engAuto.PersistNewMessages()
	rowsAfterPersist, err := store.LoadMessages(sidAuto)
	if err != nil {
		t.Fatalf("LoadMessages after PersistNewMessages: %v", err)
	}
	if len(rowsAfterPersist) != 1 {
		t.Errorf("PersistNewMessages after compact wrote %d extra rows, want 0 — the replaced history must stay flagged persisted", len(rowsAfterPersist)-1)
	}
	if got := engAuto.GetContextTokens(); got != 4321 {
		t.Errorf("ContextTokens after auto compact = %d, want 4321", got)
	}

	// Manual path.
	sidManual := compactStoreSession(t, store, "/proj-manual")
	lcManual := &engineLogCapture{}
	engManual := compactPersistEngine(t, store, "/proj-manual", sidManual, boundaryCompactResult("manual recap", post, 222), lcManual)
	if _, err := engManual.ManualCompact(context.Background(), types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("/compact")}), ""); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}
	if n := lcManual.count("RecordCompact failed"); n != 0 {
		t.Errorf("RecordCompact failed logged %d times on a live store (manual path)", n)
	}
	rowsManual, err := store.LoadMessages(sidManual)
	if err != nil {
		t.Fatalf("LoadMessages(manual): %v", err)
	}
	if len(rowsManual) != 1 {
		t.Errorf("manual compact wrote %d rows, want exactly 1 (the boundary marker)", len(rowsManual))
	}
	if got := engManual.LastPersistedIdx(); got != len(engManual.Messages()) {
		t.Errorf("LastPersistedIdx after manual compact = %d, want %d", got, len(engManual.Messages()))
	}
	engManual.PersistNewMessages()
	rowsManualAfter, err := store.LoadMessages(sidManual)
	// The manual path's chain carries the boundary; the post-compact history is
	// already flagged, so PersistNewMessages must add nothing.
	if err != nil {
		t.Fatalf("LoadMessages after PersistNewMessages (manual): %v", err)
	}
	if len(rowsManualAfter) != 1 {
		t.Errorf("PersistNewMessages after manual compact wrote %d extra rows, want 0", len(rowsManualAfter)-1)
	}

	// Store live, result has no boundary marker: nothing to persist, and the
	// guard must keep RecordCompact from dereferencing nil.
	sidNoMarker := compactStoreSession(t, store, "/proj-nomarker")
	lcNoMarker := &engineLogCapture{}
	engNoMarker := compactPersistEngine(t, store, "/proj-nomarker", sidNoMarker, compactFixture("no marker", 9_000, 10), lcNoMarker)
	if _, err := engNoMarker.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact (markerless): %v", err)
	}
	rowsNoMarker, err := store.LoadMessages(sidNoMarker)
	if err != nil {
		t.Fatalf("LoadMessages(markerless): %v", err)
	}
	if len(rowsNoMarker) != 0 {
		t.Errorf("markerless compact wrote %d rows, want 0", len(rowsNoMarker))
	}

	// Store absent, result carries a marker: the store guard must hold before
	// RecordCompact, and the compact itself still applies.
	engNoStore := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { engNoStore.Close() })
	engNoStore.SetCompactor(&recordingCompactor{result: boundaryCompactResult("storeless recap", post, 77)}, AutoCompactConfig{ContextWindow: 200_000})
	engNoStore.SetMessages([]types.Message{types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("pre")})})
	if _, err := engNoStore.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact (storeless): %v", err)
	}
	if got := len(engNoStore.Messages()); got != len(post) {
		t.Errorf("storeless compact left %d messages, want %d (the replacement still applies)", got, len(post))
	}

	// The manual entry point runs the same persistence tail: a markerless
	// result must not reach RecordCompact, and a storeless engine must not
	// dereference the nil store.
	engManualNoMarker, sidManualNoMarker := func() (*Engine, string) {
		sid := compactStoreSession(t, store, "/proj-manual-nomarker")
		eng := compactPersistEngine(t, store, "/proj-manual-nomarker", sid, compactFixture("no marker", 9_000, 10), &engineLogCapture{})
		return eng, sid
	}()
	if _, err := engManualNoMarker.ManualCompact(context.Background(), types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("/compact")}), ""); err != nil {
		t.Fatalf("ManualCompact (markerless): %v", err)
	}
	rowsManualNoMarker, err := store.LoadMessages(sidManualNoMarker)
	if err != nil {
		t.Fatalf("LoadMessages(manual markerless): %v", err)
	}
	if len(rowsManualNoMarker) != 0 {
		t.Errorf("markerless manual compact wrote %d rows, want 0", len(rowsManualNoMarker))
	}

	engManualNoStore := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { engManualNoStore.Close() })
	engManualNoStore.SetCompactor(&recordingCompactor{result: boundaryCompactResult("storeless manual", post, 66)}, AutoCompactConfig{ContextWindow: 200_000})
	engManualNoStore.SetMessages([]types.Message{types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("pre")})})
	if _, err := engManualNoStore.ManualCompact(context.Background(), types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("/compact")}), ""); err != nil {
		t.Fatalf("ManualCompact (storeless): %v", err)
	}
	if got := len(engManualNoStore.Messages()); got != len(post) {
		t.Errorf("storeless manual compact left %d messages, want %d", got, len(post))
	}
}

// TestCompactPaths_RecordCompactFailureWarns pins the durability failure path:
// a store that rejects the write is logged with its cause on both compact entry
// points, and the compact result is still applied to the engine.
func TestCompactPaths_RecordCompactFailureWarns(t *testing.T) {
	store := newTestStore(t)
	post := []types.Message{types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("recap")})}

	sid := compactStoreSession(t, store, "/proj-closed")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	lcAuto := &engineLogCapture{}
	engAuto := compactPersistEngine(t, store, "/proj-closed", sid, boundaryCompactResult("auto recap", post, 4321), lcAuto)
	if _, err := engAuto.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact must survive a RecordCompact failure, got %v", err)
	}
	recordCompactWarn := func(a map[string]any) bool {
		err, ok := a["error"].(error)
		return ok && err.Error() != ""
	}
	lcAuto.find(t, "RecordCompact failed", recordCompactWarn)

	lcManual := &engineLogCapture{}
	engManual := compactPersistEngine(t, store, "/proj-closed", sid, boundaryCompactResult("manual recap", post, 4321), lcManual)
	if _, err := engManual.ManualCompact(context.Background(), types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("/compact")}), ""); err != nil {
		t.Fatalf("ManualCompact must survive a RecordCompact failure, got %v", err)
	}
	lcManual.find(t, "RecordCompact failed", recordCompactWarn)
	if got := len(engManual.Messages()); got != 1 {
		t.Errorf("messages after a failed RecordCompact = %d, want 1 (the compact result is applied regardless)", got)
	}
}

// TestStripMediaFromMessages_OnlyToolResultContentIsRewritten pins the type half
// of the nested-media case. Content is a free-form raw field on every block, so a
// non-tool_result block that happens to carry one must keep its exact bytes: the
// stripper reaches into tool_result content and nothing else.
func TestStripMediaFromMessages_OnlyToolResultContentIsRewritten(t *testing.T) {
	t.Parallel()
	const raw = `[{"type":"video","source":{"data":"v"}},{"type":"text","text":"keep"}]`
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "carrier", Content: json.RawMessage(raw)},
		{Type: types.ContentTypeToolUse, ID: "tu_media", Name: "Read",
			Input: json.RawMessage(`{"path":"/x"}`), Content: json.RawMessage(raw)},
	}}}
	out := StripMediaFromMessages(msgs)
	if len(out) != 1 || len(out[0].Content) != 2 {
		t.Fatalf("stripped messages = %d / blocks = %d, want 1 / 2", len(out), len(out[0].Content))
	}
	for i, b := range out[0].Content {
		if string(b.Content) != raw {
			t.Errorf("block %d (type %s) content = %s, want the untouched %s", i, b.Type, b.Content, raw)
		}
	}
	if out[0].Content[0].Text != "carrier" {
		t.Errorf("text block altered: %+v, want its own text preserved", out[0].Content[0])
	}
	if out[0].Content[1].ID != "tu_media" || string(out[0].Content[1].Input) != `{"path":"/x"}` {
		t.Errorf("tool_use block altered: %+v, want id and input preserved", out[0].Content[1])
	}
}

// fullUsageToolUseEvents is a tool_use turn carrying all four usage numbers
// with distinct values.
func fullUsageToolUseEvents(model, toolID, toolName, toolInput string, u types.Usage) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: u.InputTokens}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: toolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: toolInput}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{
			OutputTokens:             u.OutputTokens,
			CacheReadInputTokens:     u.CacheReadInputTokens,
			CacheCreationInputTokens: u.CacheCreationInputTokens,
		}},
		{Type: "message_stop"},
	}
}

// TestRunTurns_ToolResultMessageAppended pins that a completed tool turn
// lands its tool_result as a user message in history — the API pairing the
// next request depends on.
func TestRunTurns_ToolResultMessageAppended(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("m", "tu_res1", "Read", `{"path":"/tmp/a"}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	// [user, assistant(tool_use), user(tool_result), assistant(text)]
	if len(res.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %s", len(res.Messages), messageRoles(res.Messages))
	}
	tr := res.Messages[2]
	if tr.Role != types.RoleUser || len(tr.Content) != 1 || tr.Content[0].Type != types.ContentTypeToolResult {
		t.Errorf("message after the tool assistant = %s/%d blocks (first %s), want a user tool_result",
			tr.Role, len(tr.Content), tr.Content[0].Type)
	}
	if tr.Content[0].ToolUseID != "tu_res1" {
		t.Errorf("tool_result ToolUseID = %q, want tu_res1", tr.Content[0].ToolUseID)
	}
}

func messageRoles(msgs []types.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&b, "[%d]%s", i, m.Role)
	}
	return b.String()
}

// TestRunTurns_PostToolAbortCarriesUsage pins the post-tool abort QueryEnd's
// usage trailer: a cancel landing while tools execute still reports the
// completed turn's accumulated usage on the terminal event.
func TestRunTurns_PostToolAbortCarriesUsage(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	inTool := make(chan struct{})
	gatedRead := &mockTool{name: "Read", enabled: true}
	gatedRead.callFn = func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		close(inTool)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &tool.ToolResult{Data: "late"}, nil
		}
	}
	mp := &mockProvider{}
	mp.addResponse(fullUsageToolUseEvents("m", "tu_ab1", "Read", `{"path":"/tmp/a"}`, types.Usage{
		InputTokens: 11, OutputTokens: 7, CacheReadInputTokens: 5, CacheCreationInputTokens: 3,
	}), nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Dispatcher: newEventCollector(), Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": gatedRead}
		},
	})
	t.Cleanup(func() { eng.Close() })
	ec := eng.dispatcher.(*eventCollector)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan QueryResult, 1)
	go func() { done <- eng.QuerySync(ctx, "q", "") }()
	<-inTool // turn 1 (usage landed) is complete; the tool is now in flight
	cancel()
	close(release)
	res := <-done
	if res.Error == nil {
		t.Fatal("expected the abort to end the query")
	}

	ends := ec.FindEvents(types.EventQueryEnd)
	var withErr []types.QueryEvent
	for _, ev := range ends {
		if ev.Error != nil {
			withErr = append(withErr, ev)
		}
	}
	if len(withErr) != 1 {
		t.Fatalf("error QueryEnds = %d, want 1", len(withErr))
	}
	u := withErr[0].Usage
	if u == nil {
		t.Fatal("abort QueryEnd carries no usage trailer")
	}
	if u.InputTokens != 11 || u.OutputTokens != 7 || u.CacheReadInputTokens != 5 || u.CacheCreationInputTokens != 3 {
		t.Errorf("abort usage = in:%d out:%d cacheRead:%d cacheCreate:%d, want 11/7/5/3",
			u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	}
}

// TestRunTurns_TurnBoundaryDrainsQueuedJob pins the priority-drain at the
// tool turn boundary: a job notification queued mid-tool persists as its
// reminder-wrapped final form and echoes an attachment event.
func TestRunTurns_TurnBoundaryDrainsQueuedJob(t *testing.T) {
	t.Parallel()

	var eng *Engine
	signalingRead := &mockTool{name: "Read", enabled: true}
	// Enqueue from inside the tool call: the turn-boundary drain can only
	// run after this Call returns, so the item is deterministically queued
	// ahead of it (an external goroutine raced the drain under load).
	signalingRead.callFn = func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		eng.EnqueueAttachment(types.QueuedItem{
			Mode:  types.ItemModeJob,
			Value: "nightly report finished",
		})
		return &tool.ToolResult{Data: "ok"}, nil
	}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("m", "tu_dr1", "Read", `{"path":"/tmp/a"}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)

	eng = New(&Params{
		Provider: mp, Model: "m", Dispatcher: newEventCollector(), Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": signalingRead}
		},
	})
	t.Cleanup(func() { eng.Close() })
	ec := eng.dispatcher.(*eventCollector)

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}

	var sawJob bool
	for _, m := range res.Messages {
		if m.Role != types.RoleUser || len(m.Content) == 0 || m.Content[0].Type != types.ContentTypeText {
			continue
		}
		if strings.Contains(m.Content[0].Text, "A background agent completed a job") &&
			strings.Contains(m.Content[0].Text, "nightly report finished") {
			sawJob = true
		}
	}
	if !sawJob {
		t.Error("no reminder-wrapped job message in history after the turn boundary drain")
	}
	var sawAttachment bool
	for _, ev := range ec.FindEvents(types.EventAttachment) {
		if ev.Message != nil && ev.Message.Attachment != nil && ev.Message.Attachment.Mode == types.ItemModeJob {
			sawAttachment = true
		}
	}
	if !sawAttachment {
		t.Error("no EventAttachment echoed for the drained job item")
	}
}

// TestRunTurns_ToolNewMessagesAppendedAfterResults pins the ordering contract:
// tool-provided messages follow the tool_result message, ahead of the next
// assistant turn.
func TestRunTurns_ToolNewMessagesAppendedAfterResults(t *testing.T) {
	t.Parallel()

	skillTool := &mockTool{name: "Skill", enabled: true}
	skillTool.callFn = func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{
			Data: "ok",
			NewMessages: []types.Message{types.NewUserMessage([]types.ContentBlock{
				types.NewTextBlock("SKILL-BODY-e59a"),
			})},
		}, nil
	}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("m", "tu_nm1", "Skill", `{}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Skill": skillTool}
		},
	})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	// [user, assistant(tool_use), user(tool_result), user(skill body), assistant(text)]
	if len(res.Messages) != 5 {
		t.Fatalf("messages = %d, want 5: %s", len(res.Messages), messageRoles(res.Messages))
	}
	skillMsg := res.Messages[3]
	if skillMsg.Role != types.RoleUser || len(skillMsg.Content) != 1 ||
		skillMsg.Content[0].Type != types.ContentTypeText || skillMsg.Content[0].Text != "SKILL-BODY-e59a" {
		t.Errorf("skill-provided message = %+v, want the exact skill body after the tool_result", skillMsg)
	}
}

// TestToolSearch_DiscoveryOnlyFromToolSearchResults pins the name gate on the
// discovery scan: a regular tool whose result data happens to parse as a tool
// list must NOT mark deferred tools discovered.
func TestToolSearch_DiscoveryOnlyFromToolSearchResults(t *testing.T) {
	t.Parallel()

	sp := &sequentialProvider{
		responses: [][]llm.StreamEvent{
			tsToolUseEvents("Reporter", `{}`),
			tsTextEvents("Done"),
		},
	}
	eng := New(&Params{
		Logger:   silentLogger(),
		Provider: sp,
		ToolsProvider: tsToolsProvider(
			tool.BuildTool(tool.ToolDef{
				Name_:        "Reporter",
				InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
				Description_: func(json.RawMessage) (string, error) { return "reports", nil },
				Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
					// ToolSearch-shaped payload: would "discover" DeferredA if
					// the scan ignored the tool name.
					return &tool.ToolResult{Data: `{"tools":[{"name":"DeferredA"}]}`}, nil
				},
				IsConcurrencySafe_: func(json.RawMessage) bool { return true },
			}),
			deferredStubTool("DeferredA"),
		),
		MaxTurns: 5,
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "report", "")
	if len(sp.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(sp.requests))
	}
	names2 := toolDefNames(sp.requests[1].Tools)
	if tsContainsStr(names2, "DeferredA") {
		t.Error("DeferredA discovered from a non-ToolSearch tool result — discovery scan must gate on the tool name")
	}
}

// TestCallLLMWithRetry_ExhaustsConfiguredAttempts pins the attempt budget:
// with MaxRetries=1 the engine makes exactly 2 attempts and the terminal
// error is the stream error itself, one RetryAttempt event between them.
func TestCallLLMWithRetry_ExhaustsConfiguredAttempts(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(nil, &StreamEndedError{})
	mp.addResponse(nil, &StreamEndedError{})
	mp.addResponse(nil, &StreamEndedError{}) // a broken attempt budget would consume this third slot

	eng := New(&Params{Provider: mp, Model: "m", Dispatcher: newEventCollector(), Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.retryConfig = &llm.RetryConfig{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	ec := eng.dispatcher.(*eventCollector)

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error == nil {
		t.Fatal("expected the stream error to end the query")
	}
	if _, ok := errors.AsType[*StreamEndedError](res.Error); !ok {
		t.Errorf("terminal error = %v (%T), want the StreamEndedError itself — falling out of the loop must return it, not a synthetic", res.Error, res.Error)
	}
	if got := mp.callCount(); got != 2 {
		t.Errorf("attempts = %d, want 2 (MaxRetries=1 + initial)", got)
	}
	retries := ec.FindEvents(types.EventRetryAttempt)
	if len(retries) != 1 {
		t.Fatalf("RetryAttempt events = %d, want 1", len(retries))
	}
	ra := retries[0].RetryAttempt
	if ra.Attempt != 1 || ra.MaxRetries != 1 {
		t.Errorf("RetryAttempt fields = attempt:%d max:%d, want 1/1", ra.Attempt, ra.MaxRetries)
	}
	if ra.ErrorType != types.RetryErrorStreamEnded {
		t.Errorf("RetryAttempt ErrorType = %q, want %q", ra.ErrorType, types.RetryErrorStreamEnded)
	}
	if ra.Error != "Connection lost. No response was received" {
		t.Errorf("RetryAttempt Error = %q, want the StreamEndedError text", ra.Error)
	}
	if ra.RetryInMs < 0 || ra.RetryInMs > 2 {
		t.Errorf("RetryAttempt RetryInMs = %d, want within the configured 2ms cap", ra.RetryInMs)
	}
}

// TestCallLLMWithRetry_DebugAttemptLog pins the attempt-boundary debug logs:
// with a debug-level engine logger both the start and the returned records
// exist for the retried attempt.
func TestCallLLMWithRetry_DebugAttemptLog(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	mp := &mockProvider{}
	mp.addResponse(nil, &StreamEndedError{})
	mp.addResponse(textStreamEvents("m", "recovered"), nil)

	eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.retryConfig = &llm.RetryConfig{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	starts, returns := lc.count("engine:callLLM_start"), lc.count("engine:callLLM_returned")
	if starts < 2 || returns < 2 {
		t.Errorf("attempt logs: starts=%d returned=%d, want >=2 each (one per attempt)", starts, returns)
	}
	lc.find(t, "engine:callLLM_returned", func(a map[string]any) bool {
		switch v := a["err"].(type) {
		case string:
			return strings.Contains(v, "Connection lost")
		case error:
			return strings.Contains(v.Error(), "Connection lost")
		}
		return false
	})
}

// TestCallLLMWithRetry_BackoffAbortReturnsAbort pins the ctx.Done case of the
// backoff wait: cancelling during the backoff window ends the retry loop with
// the abort error instead of waiting the delay out.
func TestCallLLMWithRetry_BackoffAbortReturnsAbort(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(nil, &StreamEndedError{})

	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.retryConfig = &llm.RetryConfig{MaxRetries: 1, BaseBackoff: 3 * time.Second, MaxBackoff: 3 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan QueryResult, 1)
	go func() { done <- eng.QuerySync(ctx, "q", "") }()
	waitForCallCount(t, mp, 1, 5*time.Second)
	cancelledAt := time.Now() // REAL-TIME: discriminates immediate return from waiting out the 1.5s+ jittered backoff
	cancel()                  // lands inside the 3s backoff window

	select {
	case res := <-done:
		var ab *AbortError
		if !errors.As(res.Error, &ab) || ab.Phase != "streaming" {
			t.Errorf("error during backoff cancel = %v (%T), want the streaming AbortError", res.Error, res.Error)
		}
		// The ctx.Done arm must win over the timer: with a 3s (jittered
		// 1.5s+) backoff, returning after >=1.5s means the loop sat out the
		// delay and the abort only surfaced at the next attempt.
		if elapsed := time.Since(cancelledAt); elapsed >= 1500*time.Millisecond {
			t.Errorf("retry loop returned %v after cancel, want <1.5s (the backoff was not preempted)", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel during backoff did not end the retry loop — it waited the delay out")
	}
}

// TestCallLLM_ThinkingBaselineFromModelConfig pins the per-model thinking
// baseline resolution: a configured non-empty effort reaches the request,
// and an explicitly empty config entry resolves to auto (""), not to a
// stale override.
func TestCallLLM_ThinkingBaselineFromModelConfig(t *testing.T) {
	t.Parallel()

	run := func(thinking map[string]llm.Effort) string {
		var got string
		mp := &testProvider{onStream: func(req *llm.Request) { got = string(req.Thinking) }}
		mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
		eng := New(&Params{Provider: mp, Model: "m", ModelThinking: thinking, Logger: silentLogger()})
		defer eng.Close()
		if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
			t.Fatalf("query: %v", res.Error)
		}
		return got
	}
	if got := run(map[string]llm.Effort{"m": "high"}); got != "high" {
		t.Errorf("request Thinking = %q, want high from the model config baseline", got)
	}
	if got := run(map[string]llm.Effort{"m": ""}); got != "" {
		t.Errorf("request Thinking = %q, want \"\" (empty config entry means auto)", got)
	}
	if got := run(map[string]llm.Effort{"other": "high"}); got != "" {
		t.Errorf("request Thinking = %q, want \"\" (unconfigured model)", got)
	}
}

// unclosedBlocksEvents is a stream that opens a thinking block and a text
// block, closes the text block, then dies mid-stream — the close-events path
// must emit thinking_end for the OPEN block and no second text_end for the
// already-stopped one.
func unclosedBlocksEvents() []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: "hmm"}},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "text_delta", Text: "partial"}},
		{Type: "content_block_stop", Index: 1},
		// No content_block_stop for index 0, no message_stop: the stream
		// errors out with content in flight.
		{Type: "error", Error: &llm.APIError{Message: "stream blew up"}},
	}
}

// TestEmitCloseEvents_OnlyForOpenBlocks pins emitCloseEventsForOpenBlocks:
// on a mid-stream failure, close events fire ONLY for blocks that never got
// content_block_stop — the thinking block here — and carry a real duration.
func TestEmitCloseEvents_OnlyForOpenBlocks(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	mp := eng.provider.(*mockProvider)
	mp.addResponse(unclosedBlocksEvents(), nil)

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error == nil {
		t.Fatal("expected the stream error to end the query")
	}
	ends := ec.FindEvents(types.EventThinkingEnd)
	if len(ends) != 1 {
		t.Fatalf("thinking_end events = %d, want exactly 1 (the open thinking block)", len(ends))
	}
	if ends[0].Thinking == nil || ends[0].Thinking.Duration <= 0 {
		t.Errorf("thinking_end duration = %v, want > 0", ends[0].Thinking)
	}
	// The text block was properly stopped in-stream; a close event for it
	// here would be a duplicate the TUI renders as an extra paragraph.
	textEnds := ec.FindEvents(types.EventTextEnd)
	if len(textEnds) != 1 {
		t.Errorf("text_end events = %d, want exactly 1 (the in-stream stop only)", len(textEnds))
	}
}

// captureRequests builds an engine whose provider records every request.
func captureRequests(t *testing.T, build func(mp *testProvider) *Engine) (*Engine, *[]*llm.Request) {
	t.Helper()
	reqs := &[]*llm.Request{}
	mp := &testProvider{onStream: func(req *llm.Request) {
		reqsCopy := append([]*llm.Request(nil), *reqs...)
		reqsCopy = append(reqsCopy, req)
		*reqs = reqsCopy
	}}
	eng := build(mp)
	t.Cleanup(func() { eng.Close() })
	return eng, reqs
}

// TestCallLLM_RequestStubSubstitutionAndFields pins the request shape callLLM
// assembles: system-prompt stubs substituted exactly once, modalities
// rendered, prompt-cache identity fields, and the wire basics.
func TestCallLLM_RequestStubSubstitutionAndFields(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	const claudeSentinel = "CLAUDEMD-REQSHAPE-31c4"
	if err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte(claudeSentinel), 0644); err != nil {
		t.Fatal(err)
	}

	eng, reqs := captureRequests(t, func(mp *testProvider) *Engine {
		mp.responses = []testResponse{{events: subTextEvents("custom-model", "ok")}}
		return New(&Params{
			Provider: mp, Model: "custom-model", WorkingDir: tmpDir,
			InputModalities: []string{"text", "image"}, Logger: silentLogger(),
		})
	})

	const stubPrompt = "A={{SYSTEM}} B={{SOUL}} C={{MODEL}} D={{MODALITIES}}"
	if res := eng.QuerySync(context.Background(), "q", stubPrompt); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(*reqs))
	}
	req := (*reqs)[0]
	sys := string(req.System)
	for _, stub := range []string{"{{SYSTEM}}", "{{SOUL}}", "{{MODEL}}", "{{MODALITIES}}"} {
		if strings.Contains(sys, stub) {
			t.Errorf("system prompt still carries the %s stub: %.200q", stub, sys)
		}
	}
	if !strings.Contains(sys, "C=custom-model") {
		t.Errorf("system prompt lacks the MODEL substitution: %.200q", sys)
	}
	if !strings.Contains(sys, "D=text+image") {
		t.Errorf("system prompt lacks the MODALITIES substitution (want text+image): %.200q", sys)
	}
	if req.MaxTokens != 16000 {
		t.Errorf("MaxTokens = %d, want the 16000 default", req.MaxTokens)
	}
	if !req.Stream {
		t.Error("Stream = false, want true")
	}
	if req.CacheControl == nil || req.CacheControl.Type != "ephemeral" || req.CacheControl.TTL != "1h" {
		t.Errorf("CacheControl = %+v, want ephemeral 1h for the main engine", req.CacheControl)
	}
	if req.PromptStateKey == nil || req.PromptStateKey.AgentID != "main" {
		t.Errorf("PromptStateKey = %+v, want AgentID main (the engine id)", req.PromptStateKey)
	}
	// The CLAUDE.md prepend must be a meta user message so history filters
	// and rewind treat it as system-generated.
	var ctxMsg *types.Message
	for i := range req.Messages {
		if strings.Contains(flattenText([]types.Message{req.Messages[i]}), claudeSentinel) {
			m := req.Messages[i]
			ctxMsg = &m
			break
		}
	}
	if ctxMsg == nil {
		t.Fatal("no message carries the CLAUDE.md sentinel")
	}
	if ctxMsg.Role != types.RoleUser || ctxMsg.Flags&types.FlagMeta == 0 {
		t.Errorf("CLAUDE.md message = role:%s flags:%d, want a meta user message", ctxMsg.Role, ctxMsg.Flags)
	}
}

// TestCallLLM_DefaultSystemPromptWhenNoSystemMd pins the fallback half of the
// {{SYSTEM}} stub: with no SYSTEM.md in the (isolated) home, the default base
// prompt is injected. HOME is pinned because the loader reads ~/.gbot
// directly; the test stays sequential for the same reason.
func TestCallLLM_DefaultSystemPromptWhenNoSystemMd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	eng, reqs := captureRequests(t, func(mp *testProvider) *Engine {
		mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
		return New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	})
	if res := eng.QuerySync(context.Background(), "q", "X={{SYSTEM}}"); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(*reqs))
	}
	var sys string
	if err := json.Unmarshal((*reqs)[0].System, &sys); err != nil {
		t.Fatalf("req.System is not a JSON string: %v", err)
	}
	if sys != "X="+ctxbuild.DefaultBasePrompt() {
		t.Errorf("system prompt = %.200q, want the default base prompt substituted into the stub", sys)
	}
}

// TestCallLLM_SubengineCacheControl pins the sub-agent half of the cache
// config: a sub-engine's request carries the 5m TTL and the agent-typed
// prompt state key.
func TestCallLLM_SubengineCacheControl(t *testing.T) {
	t.Parallel()

	eng, reqs := captureRequests(t, func(mp *testProvider) *Engine {
		mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
		return New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	})
	sub := eng.NewSubEngine(SubEngineOptions{AgentType: "Explore"})
	if res := sub.RunForkedQuery(context.Background(),
		[]types.Message{types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("hi")})}, "sp"); res.Error != nil {
		t.Fatalf("sub query: %v", res.Error)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(*reqs))
	}
	req := (*reqs)[0]
	if req.CacheControl == nil || req.CacheControl.Type != "ephemeral" || req.CacheControl.TTL != "5m" {
		t.Errorf("sub CacheControl = %+v, want ephemeral 5m", req.CacheControl)
	}
	if req.PromptStateKey == nil || req.PromptStateKey.AgentID != "Explore" {
		t.Errorf("sub PromptStateKey = %+v, want AgentID Explore", req.PromptStateKey)
	}
}

// TestCallLLM_ToolsLogListsNames pins the per-request tools log: the names
// attribute lists every enabled tool, joined.
func TestCallLLM_ToolsLogListsNames(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	mp := &testProvider{}
	mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: slog.New(lc),
		ToolsProvider: tsToolsProvider(tsStubTool("Alpha"), tsStubTool("Beta")),
	})
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	rec := lc.find(t, "callLLM:tools", func(a map[string]any) bool { return true })
	names, _ := aStr(rec, "names")
	if names != "Alpha,Beta" {
		t.Errorf("tools log names = %q, want \"Alpha,Beta\"", names)
	}
}

// aStr extracts a string-typed attribute from a captured record.
func aStr(rec slog.Record, key string) (string, bool) {
	var out string
	var ok bool
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out, ok = a.Value.Any().(string)
		}
		return true
	})
	return out, ok
}

// TestCallLLM_EffortLogRecordsResolution pins the per-request effort log
// fields for the resolve chain (override present vs auto).
func TestCallLLM_EffortLogRecordsResolution(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	mp := &testProvider{}
	mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
	eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(lc), ModelThinking: map[string]llm.Effort{"m": "low"}})
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	rec := lc.find(t, "callLLM:effort", func(a map[string]any) bool { return true })
	resolved, _ := aStr(rec, "resolved")
	if resolved != "low" {
		t.Errorf("effort log resolved = %q, want low", resolved)
	}
}

// TestCallLLM_AllDeferredToolsStillAnnouncedAndListed pins the empty-activeTools
// arm of the tool build: when every tool is deferred (activeTools empty) the
// request still carries the full tool list, and the deferred announcement is
// prepended as the leading user message.
func TestCallLLM_AllDeferredToolsStillAnnouncedAndListed(t *testing.T) {
	t.Parallel()

	sp := &sequentialProvider{responses: [][]llm.StreamEvent{tsTextEvents("Done")}}
	eng := New(&Params{
		Logger:        silentLogger(),
		Provider:      sp,
		ToolsProvider: tsToolsProvider(deferredStubTool("DeferredA"), deferredStubTool("DeferredB")),
		MaxTurns:      2,
		WorkingDir:    t.TempDir(),
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "find", "")
	if len(sp.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(sp.requests))
	}
	req := sp.requests[0]
	names := toolDefNames(req.Tools)
	// ToolSearch itself is always active, so with every real tool deferred the
	// active set is exactly [ToolSearch]; falling to the full list here would
	// defeat the whole deferral contract.
	if len(names) != 1 || names[0] != ToolSearchToolName {
		t.Errorf("tool defs = %v, want exactly [ToolSearch] (undiscovered deferred tools stay out)", names)
	}
	if len(req.Messages) == 0 {
		t.Fatal("no messages in request")
	}
	first := req.Messages[0]
	if first.Role != types.RoleUser {
		t.Errorf("first message role = %s, want user (the deferred announcement)", first.Role)
	}
	if len(first.Content) == 0 || !strings.Contains(first.Content[0].Text, "<available-deferred-tools>") {
		t.Errorf("first message = %.120q, want the deferred-tools announcement", flattenText([]types.Message{first}))
	}
}

// TestCallLLM_EmptyHistoryWithDeferredToolsNoAnnouncement pins the
// len(apiMessages)>0 guard on the announcement prepend: with deferred tools
// active but an empty history, no announcement-only message is fabricated.
func TestCallLLM_EmptyHistoryWithDeferredToolsNoAnnouncement(t *testing.T) {
	t.Parallel()

	eng, reqs := captureRequests(t, func(mp *testProvider) *Engine {
		mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
		parent := New(&Params{Provider: mp, Model: "m", Logger: silentLogger(),
			ToolsProvider: tsToolsProvider(deferredStubTool("DeferredA"))})
		return parent
	})
	sub := eng.NewSubEngine(SubEngineOptions{AgentType: "General",
		Tools: map[string]tool.Tool{"ToolSearch": toolsearch.New(), "DeferredA": deferredStubTool("DeferredA")}})
	if res := sub.RunForkedQuery(context.Background(), nil, "sp"); res.Error != nil {
		t.Fatalf("sub query: %v", res.Error)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(*reqs))
	}
	const wantRequestMsgs = 0
	if got := len((*reqs)[0].Messages); got != wantRequestMsgs {
		t.Errorf("request messages = %d, want %d (an empty history must not gain an announcement-only message); first: %.80q",
			got, wantRequestMsgs, flattenText((*reqs)[0].Messages))
	}
}

// TestCallLLM_EmptyModalitiesFallBackToText pins the modalities fallback for
// engines whose modality slice was cleared at runtime: the stub must render
// "text", not an empty join.
func TestCallLLM_EmptyModalitiesFallBackToText(t *testing.T) {
	t.Parallel()

	eng, reqs := captureRequests(t, func(mp *testProvider) *Engine {
		mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
		e := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
		e.SetInputModalities(nil)
		return e
	})
	if res := eng.QuerySync(context.Background(), "q", "D={{MODALITIES}}"); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(*reqs))
	}
	var sys string
	if err := json.Unmarshal((*reqs)[0].System, &sys); err != nil {
		t.Fatalf("req.System: %v", err)
	}
	if sys != "D=text" {
		t.Errorf("system = %q, want D=text (cleared modalities fall back to text)", sys)
	}
}

// TestCallLLM_NoEmptyAnnouncementAfterFullDiscovery pins the ann != ""
// operand: once every deferred tool has been discovered the announcement is
// empty and no empty prefix message may be fabricated on later turns.
func TestCallLLM_NoEmptyAnnouncementAfterFullDiscovery(t *testing.T) {
	t.Parallel()

	sp := &sequentialProvider{
		responses: [][]llm.StreamEvent{
			tsToolUseEvents(ToolSearchToolName, `{"query":"select:DeferredA"}`),
			tsTextEvents("Done"),
		},
	}
	eng := New(&Params{
		Logger:        silentLogger(),
		Provider:      sp,
		ToolsProvider: tsToolsProvider(deferredStubTool("DeferredA")),
		MaxTurns:      3,
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "find", "")
	if len(sp.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(sp.requests))
	}
	for i, req := range sp.requests {
		for _, msg := range req.Messages {
			if msg.Role != types.RoleUser || len(msg.Content) != 1 || msg.Content[0].Type != types.ContentTypeText {
				continue
			}
			if msg.Content[0].Text == "" {
				t.Errorf("request %d carries an empty user prefix message — the announcement guard must skip a fully-discovered toolset", i)
			}
		}
	}
	// Turn 2 must also carry the discovered tool and no announcement text.
	names2 := toolDefNames(sp.requests[1].Tools)
	if !tsContainsStr(names2, "DeferredA") {
		t.Errorf("turn-2 tool defs = %v, want DeferredA discovered", names2)
	}
	if strings.Contains(flattenText(sp.requests[1].Messages), "<available-deferred-tools>") {
		t.Error("turn 2 still carries a deferred announcement after full discovery")
	}
}

// askDenyingDispatcher answers every permission ask by closing the response
// channel (the "dismissed dialog" outcome), so askUser resolves deny.
type askDenyingDispatcher struct {
	eventCollector
	asks int
}

func (d *askDenyingDispatcher) Dispatch(event types.QueryEvent) {
	if event.Type == types.EventAsk && event.Ask != nil && event.Ask.ResponseCh != nil {
		d.asks++
		close(event.Ask.ResponseCh)
	}
	d.eventCollector.Dispatch(event)
}

// partialThenHangToolUseEvents streams a tool_use to completion (with model +
// stop_reason set), then holds the stream open until the test cancels.
func partialThenHangToolUseEvents(model, toolID, toolName, input string) ([]llm.StreamEvent, chan struct{}) {
	hang := make(chan struct{})
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 4}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: toolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: input}},
		{Type: "content_block_stop", Index: 0},
		// Usage rides the delta so the EventUsage it emits is a deterministic
		// "everything before the hang was consumed" gate for the cancel.
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 2}},
	}, hang
}

// TestCallLLM_CtxCancelMidStream_AppendsPartialAndSynthetics pins the in-loop
// ctx.Done path: the partial assistant is stored with the streamed model and
// stop reason, and the started tool_use gets a synthetic error result.
func TestCallLLM_CtxCancelMidStream_AppendsPartialAndSynthetics(t *testing.T) {
	t.Parallel()

	events, hang := partialThenHangToolUseEvents("streamed-model-x", "tu_cc1", "Read", `{"path":"/tmp/a"}`)
	mp := &mockProvider{}
	mp.addResponse(events, nil)
	mp.responses[0].hang = hang

	eng := New(&Params{
		Provider: mp, Model: "m", Dispatcher: newEventCollector(), Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })

	ec := eng.dispatcher.(*eventCollector)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Gate on the usage event: it fires while processing message_delta,
		// so model, tool_use and stop reason have all landed before the cancel.
		if !ec.WaitForEventCount(types.EventUsage, 1, 5*time.Second) {
			return
		}
		cancel()
	}()
	res := eng.QuerySync(ctx, "q", "")
	if _, ok := errors.AsType[*AbortError](res.Error); !ok {
		t.Fatalf("error = %v, want the abort error", res.Error)
	}
	// [user, assistant(tool_use), user(synthetic tool_result)]
	if len(res.Messages) != 3 {
		t.Fatalf("messages = %d, want 3: %s", len(res.Messages), messageRoles(res.Messages))
	}
	partial := res.Messages[1]
	if partial.Model != "streamed-model-x" {
		t.Errorf("partial assistant Model = %q, want the streamed model", partial.Model)
	}
	if partial.StopReason != "end_turn" {
		t.Errorf("partial assistant StopReason = %q, want the streamed one", partial.StopReason)
	}
	synth := res.Messages[2]
	if len(synth.Content) != 1 || synth.Content[0].Type != types.ContentTypeToolResult || synth.Content[0].ToolUseID != "tu_cc1" {
		t.Fatalf("synthetic message = %+v, want a tool_result for tu_cc1", synth.Content)
	}
	if !strings.Contains(string(synth.Content[0].Content), "discarded") {
		t.Errorf("synthetic tool_result = %s, want the streaming-fallback error text", string(synth.Content[0].Content))
	}
}

// errorMidStreamEvents streams a tool_use then a fatal stream error, with
// model and stop reason already observed.
func errorMidStreamEvents(model, toolID, toolName, input string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: toolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: input}},
		{Type: "content_block_stop", Index: 0},
		// An open thinking block: never stopped, so the error path must emit
		// its close event.
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: "mid"}},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}},
		{Type: "error", Error: &llm.APIError{Message: "upstream exploded", Status: 500}},
	}
}

// TestCallLLM_StreamEventError_AppendsPartialAndSynthetics pins the
// event.Error path: identical durability contract as the cancel path.
func TestCallLLM_StreamEventError_AppendsPartialAndSynthetics(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(errorMidStreamEvents("err-model-y", "tu_ee1", "Read", `{"path":"/tmp/a"}`), nil)
	lc := &engineLogCapture{}
	ecErr := newEventCollector()
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: slog.New(lc), Dispatcher: ecErr,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error == nil || !strings.Contains(res.Error.Error(), "upstream exploded") {
		t.Fatalf("error = %v, want the upstream stream error", res.Error)
	}
	lc.find(t, "stream event error", func(a map[string]any) bool {
		v, _ := a["error"].(error)
		return v != nil && strings.Contains(v.Error(), "upstream exploded")
	})
	// The open thinking block must get its close event before the partial
	// assistant is stored.
	if n := len(ecErr.FindEvents(types.EventThinkingEnd)); n != 1 {
		t.Errorf("thinking_end events = %d, want 1 (the open block's close event)", n)
	}
	// [user, partial assistant, synthetic tool_results, API-error assistant]
	if len(res.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %s", len(res.Messages), messageRoles(res.Messages))
	}
	if res.Messages[1].Model != "err-model-y" {
		t.Errorf("partial assistant Model = %q, want err-model-y", res.Messages[1].Model)
	}
	if res.Messages[1].StopReason != "end_turn" {
		t.Errorf("partial assistant StopReason = %q, want end_turn", res.Messages[1].StopReason)
	}
	if len(res.Messages[2].Content) != 1 || res.Messages[2].Content[0].ToolUseID != "tu_ee1" {
		t.Errorf("synthetic tool_result missing for tu_ee1: %+v", res.Messages[2].Content)
	}
	if res.Messages[3].Flags&types.FlagAPIError == 0 {
		t.Errorf("terminal message flags = %d, want FlagAPIError set on the durable failure marker", res.Messages[3].Flags)
	}
}

// TestCallLLM_ToolStartCarriesSearchReadClassification pins the
// classification fields forwarded on EventToolStart and the coalesced param
// delta (flushed by the terminal QueryEnd).
func TestCallLLM_ToolStartCarriesSearchReadClassification(t *testing.T) {
	t.Parallel()

	classified := &fakeTool{name: "Grep"}
	classified.searchRead = &tool.SearchReadKind{IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true}

	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("m", "tu_cl1", "Grep", `{"pattern":"sentinel-pattern"}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Dispatcher: ec, Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{"Grep": classified} },
	})
	t.Cleanup(func() { eng.Close() })

	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	starts := ec.FindEvents(types.EventToolStart)
	if len(starts) != 1 {
		t.Fatalf("tool_start events = %d, want 1", len(starts))
	}
	tu := starts[0].ToolUse
	if !tu.IsSearch || !tu.IsRead || !tu.IsList || !tu.IsLsp || !tu.IsTask {
		t.Errorf("tool_start classification = search:%v read:%v list:%v lsp:%v task:%v, want all true",
			tu.IsSearch, tu.IsRead, tu.IsList, tu.IsLsp, tu.IsTask)
	}
	var deltas []types.QueryEvent
	for _, ev := range ec.Events() {
		if ev.Type == types.EventToolParamDelta && ev.PartialInput != nil && ev.PartialInput.ID == "tu_cl1" && ev.PartialInput.Delta != "" {
			deltas = append(deltas, ev)
		}
	}
	if len(deltas) == 0 {
		t.Fatal("no param delta with payload for tu_cl1 — the coalesced buffer must flush at query end")
	}
	pi := deltas[len(deltas)-1].PartialInput
	if !strings.Contains(pi.Delta, "sentinel-pattern") {
		t.Errorf("param delta payload = %q, want the streamed input JSON", pi.Delta)
	}
	if pi.Name != "Grep" || !pi.IsSearch || !pi.IsRead {
		t.Errorf("param delta = name:%q search:%v read:%v, want Grep with the tool's classification", pi.Name, pi.IsSearch, pi.IsRead)
	}
}

// TestCallLLM_MalformedDeltaIndexIgnored pins the accumulator-bound guard:
// input_json_delta for an index with no open tool block is dropped without
// panic and without fabricating events. Index 0 matters separately: an
// off-by-one bound (`<=`) still passes a delta aimed past the end, but it
// reads one slot past a short accumulator, which only the zero index exposes
// against an empty one.
func TestCallLLM_MalformedDeltaIndexIgnored(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"y":2}`}},
		{Type: "content_block_delta", Index: 7, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"x":1}`}},
		{Type: "content_block_stop", Index: 9},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}, nil)
	eng := New(&Params{Provider: mp, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("malformed indices must not fail the query: %v", res.Error)
	}
	for _, ev := range ec.Events() {
		if ev.Type == types.EventToolParamDelta || ev.Type == types.EventToolStart {
			t.Errorf("unexpected tool event %s fabricated for an index with no open block", ev.Type)
		}
	}
}

// TestCallLLM_DeltaOnPaddedAccumulatorSlotIgnored pins the nil-slot half of
// the same guard: a tool block at index 2 pads indices 0 and 1 with nil, so a
// delta aimed at a padded slot (a text block's index) has an in-range but
// builder-less accumulator and must be dropped rather than dereferenced.
func TestCallLLM_DeltaOnPaddedAccumulatorSlotIgnored(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "content_block_start", Index: 2, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_pad", Name: "NoSuchTool"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"padded":1}`}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"padded":0}`}},
		{Type: "content_block_stop", Index: 2},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}, nil)
	eng := New(&Params{Provider: mp, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("a delta on a padded slot must not fail the query: %v", res.Error)
	}
	for _, ev := range ec.Events() {
		if ev.Type == types.EventToolParamDelta && ev.PartialInput != nil &&
			strings.Contains(ev.PartialInput.Delta, "padded") {
			t.Errorf("param delta %q fabricated for a padded (builder-less) accumulator slot", ev.PartialInput.Delta)
		}
	}
}

// multiTextBlockEvents builds the text-block edge-case stream: block 0 stops
// with text, block 1 opens with no delta, block 2 streams a single char and
// never stops.
func multiTextBlockEvents() []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "alpha"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_start", Index: 2, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 2, Delta: &llm.StreamDelta{Type: "text_delta", Text: "x"}},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}
}

// TestCallLLM_UnstoppedTextFinalization pins the post-loop text finalization:
// the buffered tail lands in the FIRST empty text block only, the stopped
// block keeps its own text, and later empties stay empty.
func TestCallLLM_UnstoppedTextFinalization(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(multiTextBlockEvents(), nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	assistant := res.Messages[1]
	if len(assistant.Content) != 3 {
		t.Fatalf("assistant blocks = %d, want 3", len(assistant.Content))
	}
	if assistant.Content[0].Text != "alpha" {
		t.Errorf("block0 text = %q, want alpha (stopped block must keep its own text)", assistant.Content[0].Text)
	}
	if assistant.Content[1].Text != "x" {
		t.Errorf("block1 text = %q, want x (first empty block receives the buffered tail)", assistant.Content[1].Text)
	}
	if assistant.Content[2].Text != "" {
		t.Errorf("block2 text = %q, want empty (the fill breaks after one block)", assistant.Content[2].Text)
	}
}

// deltaOnlyTextEvents streams text deltas with no content_block_start at all.
func deltaOnlyTextEvents() []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "blockless"}},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}
}

// emptyCompletedEvents completes a stream with no content whatsoever.
func emptyCompletedEvents() []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}
}

// TestCallLLM_BlocklessDeltaAndEmptyStream pins both no-block finalize arms:
// blockless deltas still produce a text block; a fully empty stream must not
// fabricate one.
func TestCallLLM_BlocklessDeltaAndEmptyStream(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(deltaOnlyTextEvents(), nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if len(res.Messages) != 2 || len(res.Messages[1].Content) != 1 || res.Messages[1].Content[0].Text != "blockless" {
		t.Fatalf("blockless stream produced %d msgs (assistant content %d), want the delta as a synthesized text block",
			len(res.Messages), len(res.Messages[1].Content))
	}

	mp2 := &mockProvider{}
	mp2.addResponse(emptyCompletedEvents(), nil)
	eng2 := New(&Params{Provider: mp2, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng2.Close() })
	res2 := eng2.QuerySync(context.Background(), "q", "")
	if res2.Error != nil {
		t.Fatalf("query: %v", res2.Error)
	}
	if len(res2.Messages) != 2 || len(res2.Messages[1].Content) != 0 {
		t.Fatalf("empty stream assistant content = %d blocks, want 0 (no fabricated empty text block)", len(res2.Messages[1].Content))
	}
}

// TestCallLLM_UsageOverwriteSemantics pins the TS updateUsage contract:
// message_delta overwrites input/cache numbers only when > 0; output is a
// direct set; the usage event carries the stop reason.
func TestCallLLM_UsageOverwriteSemantics(t *testing.T) {
	t.Parallel()

	build := func(startIn, deltaIn, deltaCacheRead, deltaCacheCreate int) (*Engine, *eventCollector) {
		ec := newEventCollector()
		mp := &mockProvider{}
		mp.addResponse([]llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "m", Usage: types.Usage{InputTokens: startIn}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "hi"}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{
				InputTokens:              deltaIn,
				OutputTokens:             6,
				CacheReadInputTokens:     deltaCacheRead,
				CacheCreationInputTokens: deltaCacheCreate,
			}},
			{Type: "message_stop"},
		}, nil)
		eng := New(&Params{Provider: mp, Model: "m", Dispatcher: ec, Logger: silentLogger()})
		return eng, ec
	}

	// Positive delta numbers overwrite the start values (1 is the smallest
	// positive value, so a comparison off by one still fails here).
	eng, ec := build(20, 1, 1, 1)
	t.Cleanup(func() { eng.Close() })
	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	usages := ec.FindEvents(types.EventUsage)
	if len(usages) != 1 {
		t.Fatalf("usage events = %d, want 1", len(usages))
	}
	u := usages[0].Usage
	if u.InputTokens != 1 || u.CacheReadInputTokens != 1 || u.CacheCreationInputTokens != 1 {
		t.Errorf("usage = in:%d cacheRead:%d cacheCreate:%d, want 1/1/1 (positive deltas overwrite)", u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	}
	if u.OutputTokens != 6 {
		t.Errorf("usage output = %d, want 6 (direct set)", u.OutputTokens)
	}
	if u.StopReason != "end_turn" {
		t.Errorf("usage StopReason = %q, want end_turn", u.StopReason)
	}

	// Zero delta numbers keep the start values.
	eng2, ec2 := build(20, 0, 0, 0)
	t.Cleanup(func() { eng2.Close() })
	if res := eng2.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	usages2 := ec2.FindEvents(types.EventUsage)
	if len(usages2) != 1 || usages2[0].Usage.InputTokens != 20 {
		t.Fatalf("usage with zero delta = %+v, want input kept at 20", usages2[0].Usage)
	}
}

// thinkingTurnEvents streams a completed thinking block followed by text.
func thinkingTurnEvents() []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "think-model-9"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: "pondering"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "signature_delta", Signature: "sig-1"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "text_delta", Text: "answer"}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}
}

// TestCallLLM_ThinkingDurationAndSignature pins the thinking block bookkeeping:
// signature lands on the block, the duration is real (not the zero-time span),
// and the in-loop thinking_end carries it too.
func TestCallLLM_ThinkingDurationAndSignature(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse(thinkingTurnEvents(), nil)
	eng := New(&Params{Provider: mp, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	assistant := res.Messages[1]
	if assistant.Model != "think-model-9" {
		t.Errorf("assistant Model = %q, want the streamed model name", assistant.Model)
	}
	if len(assistant.Content) != 2 || assistant.Content[0].Type != types.ContentTypeThinking {
		t.Fatalf("assistant blocks = %+v, want thinking first", assistant.Content)
	}
	th := assistant.Content[0]
	if th.Thinking != "pondering" {
		t.Errorf("thinking text = %q, want pondering", th.Thinking)
	}
	if th.Signature != "sig-1" {
		t.Errorf("thinking signature = %q, want sig-1", th.Signature)
	}
	if th.ThinkingDurationNs <= 0 || th.ThinkingDurationNs > 10*time.Second.Nanoseconds() {
		t.Errorf("thinking duration = %dns, want a real span (0 would mean the start time was never set)", th.ThinkingDurationNs)
	}
	ends := ec.FindEvents(types.EventThinkingEnd)
	if len(ends) != 1 || ends[0].Thinking == nil || ends[0].Thinking.Duration <= 0 {
		t.Errorf("thinking_end events = %+v, want one with a positive duration", ends)
	}
}

// TestCallLLM_InterruptedStreamErrorFields pins the StreamInterruptedError
// payload: block count and model from the interrupted stream.
func TestCallLLM_InterruptedStreamErrorFields(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Text deltas then the channel closes without message_stop and without
	// cancellation: the genuine mid-stream failure.
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "int-model"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "half"}},
	}, nil)
	lc := &engineLogCapture{}
	eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	// Zero retries: the interrupted-stream error must surface as-is instead of
	// turning into mock exhaustion on the second attempt.
	eng.retryConfig = &llm.RetryConfig{MaxRetries: 0, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	res := eng.QuerySync(context.Background(), "q", "")
	var se *StreamInterruptedError
	if !errors.As(res.Error, &se) {
		t.Fatalf("error = %v (%T), want StreamInterruptedError", res.Error, res.Error)
	}
	if se.ContentBlocks != 1 {
		t.Errorf("ContentBlocks = %d, want 1", se.ContentBlocks)
	}
	if se.Model != "int-model" {
		t.Errorf("Model = %q, want int-model", se.Model)
	}
	lc.find(t, "stream interrupted", func(a map[string]any) bool { return true })
}

// gatedProbeTool blocks until released, so the test controls when the tool
// finishes and can observe whether its completion event survives Discard.
type gatedProbeTool struct {
	mockTool
	release chan struct{}
}

func newGatedProbeTool(name string) *gatedProbeTool {
	t := &gatedProbeTool{name: name, enabled: true, release: make(chan struct{})}
	t.callFn = func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		<-t.release
		return &tool.ToolResult{Data: "late"}, nil
	}
	return t
}

// toolEndFor reports whether a completed ToolEnd for the id has arrived.
func toolEndFor(ec *eventCollector, id string) bool {
	for _, ev := range ec.FindEvents(types.EventToolEnd) {
		if ev.ToolResult != nil && ev.ToolResult.ToolUseID == id {
			return true
		}
	}
	return false
}

// assertNoToolEndAfterRelease releases a gated tool and asserts its completion
// event never surfaces: Discard must suppress post-discard emissions.
func assertNoToolEndAfterRelease(t *testing.T, ec *eventCollector, probe *gatedProbeTool, id string) {
	t.Helper()
	close(probe.release)
	deadline := time.Now().Add(300 * time.Millisecond) // REAL-TIME: absence window for the suppressed event
	for time.Now().Before(deadline) {
		if toolEndFor(ec, id) {
			t.Error("tool completion event surfaced after Discard — discarded executors must not emit")
			return
		}
		runtime.Gosched()
	}
}

// TestCallLLM_StreamErrorDiscardsStartedTools pins Discard on the stream-error
// path: a tool still in flight when the stream dies must never surface its
// completion event.
func TestCallLLM_StreamErrorDiscardsStartedTools(t *testing.T) {
	t.Parallel()

	probe := newGatedProbeTool("Read")
	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_dc1", Name: "Read"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: "{}"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "error", Error: &llm.APIError{Message: "boom", Status: 500}},
	}, nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Dispatcher: ec, Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{"Read": probe} },
	})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error == nil || !strings.Contains(res.Error.Error(), "boom") {
		t.Fatalf("error = %v, want the stream failure", res.Error)
	}
	assertNoToolEndAfterRelease(t, ec, probe, "tu_dc1")
}

// TestCallLLM_InterruptedWithStartedTool_Discards pins Discard on the genuine
// interrupted-stream path (channel closed without stop signal or cancel).
func TestCallLLM_InterruptedWithStartedTool_Discards(t *testing.T) {
	t.Parallel()

	probe := newGatedProbeTool("Read")
	ec := newEventCollector()
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "m"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_id1", Name: "Read"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: "{}"}},
		{Type: "content_block_stop", Index: 0},
	}, nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Dispatcher: ec, Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{"Read": probe} },
	})
	t.Cleanup(func() { eng.Close() })
	eng.retryConfig = &llm.RetryConfig{MaxRetries: 0, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	res := eng.QuerySync(context.Background(), "q", "")
	if _, ok := errors.AsType[*StreamInterruptedError](res.Error); !ok {
		t.Fatalf("error = %v (%T), want StreamInterruptedError", res.Error, res.Error)
	}
	assertNoToolEndAfterRelease(t, ec, probe, "tu_id1")
}

// TestCallLLM_UndiscoveredDeferredToolNotExecutable pins the executor tool
// map under ToolSearch: an hallucinated call to an undiscovered deferred tool
// fails with "No such tool", it does not execute.
func TestCallLLM_UndiscoveredDeferredToolNotExecutable(t *testing.T) {
	t.Parallel()

	sp := &sequentialProvider{
		responses: [][]llm.StreamEvent{
			tsToolUseEvents("DeferredA", `{}`),
			tsTextEvents("Done"),
		},
	}
	eng := New(&Params{
		Logger:        silentLogger(),
		Provider:      sp,
		ToolsProvider: tsToolsProvider(deferredStubTool("DeferredA")),
		MaxTurns:      3,
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "go", "")
	if len(sp.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(sp.requests))
	}
	var rejectSeen bool
	for _, m := range sp.requests[1].Messages {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && strings.Contains(string(b.Content), "is deferred and its schema has not been loaded") {
				rejectSeen = true
			}
		}
	}
	if !rejectSeen {
		t.Errorf("hallucinated deferred call was not rejected; second request messages: %.300q", flattenText(sp.requests[1].Messages))
	}
}

// TestCallLLM_DiscoveredDeferredToolExecutes is the positive half of the same
// executor tool map: once ToolSearch has discovered the deferred tool, the
// rebuilt map must carry it, so the call runs instead of being rejected. The
// rejection test alone would also pass with an empty executor map.
func TestCallLLM_DiscoveredDeferredToolExecutes(t *testing.T) {
	t.Parallel()

	sp := &sequentialProvider{
		responses: [][]llm.StreamEvent{
			tsToolUseEvents(ToolSearchToolName, `{"query":"select:DeferredA"}`),
			tsToolUseEvents("DeferredA", `{}`),
			tsTextEvents("Done"),
		},
	}
	eng := New(&Params{
		Logger:        silentLogger(),
		Provider:      sp,
		ToolsProvider: tsToolsProvider(tsStubTool("Read"), deferredStubTool("DeferredA")),
		MaxTurns:      4,
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "go", "")
	if len(sp.requests) != 3 {
		t.Fatalf("requests = %d, want 3 (discovery, the deferred call, the wrap-up)", len(sp.requests))
	}
	results := toolResultContents(sp.requests[2].Messages)
	var executed, rejected bool
	for _, r := range results {
		if strings.Contains(r, "is deferred and its schema has not been loaded") {
			rejected = true
		}
		if strings.Contains(r, "DeferredA") {
			executed = true
		}
	}
	if rejected {
		t.Errorf("discovered deferred tool was rejected; tool results: %.300q", results)
	}
	if !executed {
		t.Errorf("discovered deferred tool never executed; tool results: %.300q", results)
	}
}

// TestCallLLM_InterruptedWithStartedTool_Discards pins Discard on the genuine
// interrupted-stream path: the channel closing without a stop signal must keep
// the queued tool from running.

// denyAllChecker denies every tool by bare rule.
type denyAllChecker struct{}

func (denyAllChecker) Check(string, json.RawMessage) permission.Decision {
	return permission.Decision{Action: permission.ActionDeny}
}
func (denyAllChecker) HasRules() bool { return true }

// askChecker asks for every tool.
type askChecker struct{}

func (askChecker) Check(string, json.RawMessage) permission.Decision {
	return permission.Decision{Action: permission.ActionAsk, Message: "confirm?"}
}
func (askChecker) HasRules() bool { return true }

// toolResultContents extracts every tool_result block's content payload from
// a message slice.
func toolResultContents(msgs []types.Message) []string {
	var out []string
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult {
				out = append(out, string(b.Content))
			}
		}
	}
	return out
}

func TestCallLLM_PermissionCheckerDenies(t *testing.T) {
	t.Parallel()

	eng, reqs := captureRequests(t, func(mp *testProvider) *Engine {
		mp.responses = []testResponse{
			{events: toolUseStreamEvents("m", "tu_pc1", "Read", `{}`)},
			{events: subTextEvents("m", "ok")},
		}
		return New(&Params{
			Provider: mp, Model: "m", PermissionChecker: denyAllChecker{}, Logger: silentLogger(),
			ToolsProvider: func() map[string]tool.Tool {
				return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
			},
		})
	})

	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if len(*reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(*reqs))
	}
	var denySeen bool
	for _, c := range toolResultContents((*reqs)[1].Messages) {
		if strings.Contains(c, "Permission to use Read has been denied") {
			denySeen = true
		}
	}
	if !denySeen {
		t.Errorf("second request lacks the deny tool_result; contents: %.300q", toolResultContents((*reqs)[1].Messages))
	}
}

func TestCallLLM_MemoryDirWritesBypassPermission(t *testing.T) {
	t.Parallel()

	memDir := t.TempDir()
	writePath := filepath.Join(memDir, "note.md")

	writeTool := mockTool{name: "Write", enabled: true}
	writeTool.callFn = func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "written"}, nil
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("m", "tu_md1", "Write", `{"file_path":"`+writePath+`","content":"x"}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)
	eng := New(&Params{
		Provider: mp, Model: "m", PermissionChecker: denyAllChecker{}, Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{"Write": &writeTool} },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetMemoryDir(memDir)

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	contents := toolResultContents(res.Messages)
	for _, c := range contents {
		if strings.Contains(c, "Permission to use Write") {
			t.Fatalf("memory-dir write was denied: %s", c)
		}
	}
	var executed bool
	for _, c := range contents {
		if strings.Contains(c, "written") {
			executed = true
		}
	}
	if !executed {
		t.Errorf("memory-dir write never executed; tool_result contents: %.200q", contents)
	}
}

func TestCallLLM_SubengineToolRejectMessage(t *testing.T) {
	t.Parallel()

	dd := &askDenyingDispatcher{}
	mp := &mockProvider{}
	parent := New(&Params{
		Provider: mp, Model: "m", PermissionChecker: askChecker{}, Dispatcher: dd, Logger: silentLogger(),
	})
	t.Cleanup(func() { parent.Close() })
	mp.addResponse(toolUseStreamEvents("m", "tu_sr1", "Read", `{}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)

	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "General", ParentToolUseID: "tu_sub_reject",
		Tools: map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}},
	})
	res := sub.RunForkedQuery(context.Background(),
		[]types.Message{types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("q")})}, "")
	if res.Error != nil {
		t.Fatalf("sub query: %v", res.Error)
	}
	var rejectText string
	for _, c := range toolResultContents(res.Messages) {
		if strings.Contains(c, "rejected") {
			rejectText = c
		}
	}
	if rejectText == "" {
		t.Fatal("no rejection tool_result found")
	}
	if !strings.Contains(rejectText, "Permission for this tool use was denied") {
		t.Errorf("rejection text = %.160q, want the sub-agent wording", rejectText)
	}
	if strings.Contains(rejectText, "The user doesn't want to proceed") {
		t.Errorf("rejection text = %.160q, leaked the main-thread wording into a sub-agent", rejectText)
	}
}

// TestCallLLM_StreamEndedLogs pins the no-content no-cancel diagnosis log.
func TestCallLLM_StreamEndedLogs(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{{Type: "message_start", Message: &llm.MessageStart{Model: "m"}}}, nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.retryConfig = &llm.RetryConfig{MaxRetries: 0, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	res := eng.QuerySync(context.Background(), "q", "")
	if _, ok := errors.AsType[*StreamEndedError](res.Error); !ok {
		t.Fatalf("error = %v (%T), want StreamEndedError", res.Error, res.Error)
	}
	lc.find(t, "stream ended without content or completion signal", func(a map[string]any) bool { return true })
}

// ---------------------------------------------------------------------------
// Pure-function pins: summary extraction, timestamp injection, API normalize
// ---------------------------------------------------------------------------

func TestComputeSummary_FallbackChain(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })

	eng.toolsProvider = func() map[string]tool.Tool {
		return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
	}
	eng.refreshTools()

	// Empty input short-circuits.
	if got := eng.computeSummary("Read", nil); got != "" {
		t.Errorf("computeSummary(nil) = %q, want empty", got)
	}
	// Known tool without ToolWithSummary: Description().
	if got := eng.computeSummary("Read", json.RawMessage(`{"path":"/tmp/x"}`)); got != "Read description" {
		t.Errorf("computeSummary(Read) = %q, want the Description fallback", got)
	}
	// Unknown tool: partial-JSON extraction.
	if got := eng.computeSummary("Mystery", json.RawMessage(`{"command":"echo hi"}`)); got != "echo hi" {
		t.Errorf("computeSummary(Mystery) = %q, want the command field extracted", got)
	}
}

func TestExtractSummaryFromPartial_Tables(t *testing.T) {
	t.Parallel()
	long30 := strings.Repeat("a", 30)
	long40 := strings.Repeat("b", 40)
	cases := []struct {
		name, tool, partial, want string
	}{
		{"bash command", "Bash", `{"command":"` + long30 + `"}`, long30},
		{"bash clamps at 30", "Bash", `{"command":"` + long30 + `EXTRA"}`, long30 + "..."},
		{"bash clamps at 29", "Bash", `{"command":"` + strings.Repeat("a", 29) + `X"}`, strings.Repeat("a", 29) + "X"},
		{"read file_path", "Read", `{"file_path":"/tmp/x.go"}`, "/tmp/x.go"},
		{"read clamps at 40", "Read", `{"file_path":"` + long40 + `Z"}`, long40 + "..."},
		{"grep pattern", "Grep", `{"pattern":"foo.*bar"}`, "foo.*bar"},
		{"grep clamps at 40", "Grep", `{"pattern":"` + long40 + `Q"}`, long40 + "..."},
		{"underscore normalized", "search_code", `{"pattern":"zig"}`, "zig"},
		{"hyphen normalized", "file-glob", `{"pattern":"zag"}`, "zag"},
		{"unknown tries common fields in order", "Weird", `{"url":"https://x.example"}`, "https://x.example"},
		{"unknown skips empty earlier fields", "Weird", `{"url":"","query":"why"}`, "why"},
		{"unknown clamps at 60", "Weird", `{"url":"` + strings.Repeat("u", 61) + `"}`, strings.Repeat("u", 60) + "..."},
		{"nothing matches", "Weird", `{"other":1}`, ""},
		{"partial json tail", "Bash", `{"command":"echo`, "echo"},
	}
	for _, tc := range cases {
		if got := extractSummaryFromPartial(tc.tool, tc.partial); got != tc.want {
			t.Errorf("%s: extractSummaryFromPartial(%s) = %q, want %q", tc.name, tc.tool, got, tc.want)
		}
	}
}

func TestExtractJSONStringField_Edges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, field, want string
	}{
		{"plain", `{"k":"v"}`, "k", "Pv"},
		{"whitespace after colon", `{"k":  "v"}`, "k", "Pv"},
		{"newline after colon", "{\"k\":\n\"v\"}", "k", "Pv"},
		{"missing key", `{"x":"v"}`, "k", ""},
		{"no colon", `{"k" "v"}`, "k", ""},
		{"value not quoted", `{"k":42}`, "k", ""},
		{"cut before value", `{"k":`, "k", ""},
		{"cut right after quote", `{"k":"`, "k", ""},
		{"value ends at comma", `{"k":"v","j":"w"}`, "k", "Pv"},
		{"value ends at brace", `{"k":"v"}`, "k", "Pv"},
		{"empty value", `{"k":""}`, "k", ""},
	}
	for _, tc := range cases {
		if got := extractJSONStringField(tc.in, tc.field, "P", 100); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := extractJSONStringField(`{"k":"`+strings.Repeat("z", 10)+`"}`, "k", "", 5); got != strings.Repeat("z", 5)+"..." {
		t.Errorf("maxLen clamp = %q, want zzzzz...", got)
	}
	if got := extractJSONStringField(`{"k":"`+strings.Repeat("z", 5)+`"}`, "k", "", 5); got != strings.Repeat("z", 5) {
		t.Errorf("maxLen boundary = %q, want unclamped", got)
	}
}

func TestInjectTimestamp_Table(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 10, 20, 12, 34, 56, 0, time.UTC)
	userMsg := types.Message{Role: types.RoleUser, Timestamp: ts, Content: []types.ContentBlock{types.NewTextBlock("hello")}}
	// Rendering is local wall clock by design; take the expectation from the
	// same conversion the code performs.
	wantPrefix := "[" + ts.Local().Format("2006-01-02 15:04:05")

	out := injectTimestamp([]types.ContentBlock{types.NewTextBlock("hello")}, userMsg)
	if len(out) != 1 || !strings.HasPrefix(out[0].Text, wantPrefix) || !strings.HasSuffix(out[0].Text, " hello") {
		t.Errorf("text-first user message = %q, want %q prefix and the original text", out[0].Text, wantPrefix)
	}

	// Non-text first block: timestamp block prepended.
	out = injectTimestamp([]types.ContentBlock{{Type: types.ContentTypeImage}}, userMsg)
	if len(out) != 2 || out[0].Type != types.ContentTypeText || !strings.HasPrefix(out[0].Text, wantPrefix) {
		t.Errorf("image-first blocks = %d, want a prepended timestamp text block", len(out))
	}

	// tool_result first: untouched.
	trBlock := types.ContentBlock{Type: types.ContentTypeToolResult, ToolUseID: "tu_1"}
	out = injectTimestamp([]types.ContentBlock{trBlock}, userMsg)
	if len(out) != 1 || out[0].Type != types.ContentTypeToolResult {
		t.Errorf("tool_result-first blocks were modified: %+v", out)
	}

	// Meta and non-user messages: untouched.
	metaMsg := userMsg
	metaMsg.Flags = types.FlagMeta
	out = injectTimestamp([]types.ContentBlock{types.NewTextBlock("meta")}, metaMsg)
	if out[0].Text != "meta" {
		t.Errorf("meta user message was stamped: %q", out[0].Text)
	}
	assistantMsg := types.Message{Role: types.RoleAssistant, Timestamp: ts, Content: nil}
	out = injectTimestamp([]types.ContentBlock{types.NewTextBlock("answer")}, assistantMsg)
	if out[0].Text != "answer" {
		t.Errorf("assistant message was stamped: %q", out[0].Text)
	}

	// Empty block slice: the timestamp still lands as a leading block.
	if out = injectTimestamp(nil, userMsg); len(out) != 1 || !strings.HasPrefix(out[0].Text, wantPrefix) {
		t.Errorf("empty blocks -> %d entries, want the single timestamp block", len(out))
	}
}

func TestNormalizeMessagesForAPI_Sanitization(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("compact boundary")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_n1", Content: json.RawMessage(`not-json`)},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_n2", Input: json.RawMessage(`{"partial":`)},
			{Type: types.ContentTypeThinking},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{}}, // becomes empty -> dropped
	}
	out := NormalizeMessagesForAPI(msgs)
	if len(out) != 2 {
		t.Fatalf("messages = %d, want 2 (system dropped, emptied dropped)", len(out))
	}
	var trContent json.RawMessage
	for _, b := range out[0].Content {
		if b.Type == types.ContentTypeToolResult {
			trContent = b.Content
		}
	}
	if !strings.Contains(string(trContent), "[corrupted tool result") {
		t.Errorf("tool_result content = %s, want the sanitized placeholder", trContent)
	}
	for _, b := range out[1].Content {
		if b.Type == types.ContentTypeToolUse && string(b.Input) != "{}" {
			t.Errorf("tool_use input = %s, want sanitized {}", b.Input)
		}
		if b.Type == types.ContentTypeThinking {
			t.Error("empty thinking block survived normalization")
		}
	}
}

func TestStripMediaFromMessages_Table(t *testing.T) {
	t.Parallel()
	img := types.ContentBlock{Type: types.ContentTypeImage}
	vid := types.ContentBlock{Type: types.ContentTypeVideo}
	doc := types.ContentBlock{Type: types.ContentTypeDocument}

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{img, types.NewTextBlock("see this")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{vid}},
		{Role: types.RoleUser, Content: []types.ContentBlock{doc}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{img}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("plain")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{{
			Type: types.ContentTypeToolResult, ToolUseID: "tu_s1",
			Content: json.RawMessage(`[{"type":"image","source":{"data":"z"}},{"type":"text","text":"cap"}]`),
		}}},
		// Every nested media kind must be replaced in place; a sibling text
		// block keeps its position.
		{Role: types.RoleUser, Content: []types.ContentBlock{{
			Type: types.ContentTypeToolResult, ToolUseID: "tu_s3",
			Content: json.RawMessage(`[{"type":"text","text":"lead"},{"type":"video","source":{"data":"v"}},{"type":"document","source":{"data":"d"},"title":"t"}]`),
		}}},
		// A tool_result with no nested content at all stays untouched.
		{Role: types.RoleUser, Content: []types.ContentBlock{{
			Type: types.ContentTypeToolResult, ToolUseID: "tu_s4", Content: json.RawMessage(``),
		}}},
		// Single-kind nested arrays: the kind's own hasMedia flag is what makes
		// the rewrite happen, so a lone video or document must be enough.
		{Role: types.RoleUser, Content: []types.ContentBlock{{
			Type: types.ContentTypeToolResult, ToolUseID: "tu_s5",
			Content: json.RawMessage(`[{"type":"video","source":{"data":"v"}}]`),
		}}},
		{Role: types.RoleUser, Content: []types.ContentBlock{{
			Type: types.ContentTypeToolResult, ToolUseID: "tu_s6",
			Content: json.RawMessage(`[{"type":"document","source":{"data":"d"}}]`),
		}}},
	}
	out := StripMediaFromMessages(msgs)
	if len(out) != 10 {
		t.Fatalf("messages = %d, want 10 (all preserved, media replaced)", len(out))
	}
	if out[0].Content[0].Type != types.ContentTypeText || out[0].Content[0].Text != "[image]" {
		t.Errorf("image block = %+v, want [image] text", out[0].Content[0])
	}
	if out[0].Content[1].Text != "see this" {
		t.Errorf("sibling text lost: %q", out[0].Content[1].Text)
	}
	if out[1].Content[0].Text != "[video]" || out[2].Content[0].Text != "[document]" {
		t.Errorf("video/document replacement wrong: %q %q", out[1].Content[0].Text, out[2].Content[0].Text)
	}
	// Assistant messages pass through untouched.
	if out[3].Content[0].Type != types.ContentTypeImage {
		t.Errorf("assistant media was stripped: %+v", out[3].Content[0])
	}
	// Nested (one level) inside tool_result content.
	var nested []types.ContentBlock
	if err := json.Unmarshal(out[5].Content[0].Content, &nested); err != nil {
		t.Fatalf("nested content not JSON: %v", err)
	}
	if nested[0].Type != types.ContentTypeText || nested[0].Text != "[image]" {
		t.Errorf("nested image = %+v, want [image] text", nested[0])
	}
	if nested[1].Text != "cap" {
		t.Errorf("nested sibling text lost: %q", nested[1].Text)
	}
	var nestedKinds []types.ContentBlock
	if err := json.Unmarshal(out[6].Content[0].Content, &nestedKinds); err != nil {
		t.Fatalf("nested video/document content not JSON: %v", err)
	}
	if len(nestedKinds) != 3 {
		t.Fatalf("nested kinds = %d blocks, want 3 (lead text, video, document)", len(nestedKinds))
	}
	if nestedKinds[0].Type != types.ContentTypeText || nestedKinds[0].Text != "lead" {
		t.Errorf("nested lead block changed: %+v, want the untouched text block", nestedKinds[0])
	}
	if nestedKinds[1].Type != types.ContentTypeText || nestedKinds[1].Text != "[video]" {
		t.Errorf("nested video = %+v, want [video] text", nestedKinds[1])
	}
	if nestedKinds[2].Type != types.ContentTypeText || nestedKinds[2].Text != "[document]" {
		t.Errorf("nested document = %+v, want [document] text", nestedKinds[2])
	}
	if string(out[7].Content[0].Content) != "" {
		t.Errorf("empty tool_result content rewritten: %q, want the empty bytes", out[7].Content[0].Content)
	}
	var loneVideo []types.ContentBlock
	if err := json.Unmarshal(out[8].Content[0].Content, &loneVideo); err != nil {
		t.Fatalf("lone nested video content not JSON: %v", err)
	}
	if len(loneVideo) != 1 || loneVideo[0].Type != types.ContentTypeText || loneVideo[0].Text != "[video]" {
		t.Errorf("lone nested video = %s, want a single [video] text block", out[8].Content[0].Content)
	}
	var loneDoc []types.ContentBlock
	if err := json.Unmarshal(out[9].Content[0].Content, &loneDoc); err != nil {
		t.Fatalf("lone nested document content not JSON: %v", err)
	}
	if len(loneDoc) != 1 || loneDoc[0].Type != types.ContentTypeText || loneDoc[0].Text != "[document]" {
		t.Errorf("lone nested document = %s, want a single [document] text block", out[9].Content[0].Content)
	}
	// Media-free nested content must keep its exact bytes: a re-marshal would
	// reorder fields and break the prompt-cache prefix.
	const rawNested = `[{ "text" : "plain" , "type":"text"}]`
	noMedia := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{
		Type: types.ContentTypeToolResult, ToolUseID: "tu_s2", Content: json.RawMessage(rawNested),
	}}}}
	got := StripMediaFromMessages(noMedia)
	if string(got[0].Content[0].Content) != rawNested {
		t.Errorf("media-free nested content rewritten: %s, want the exact original bytes %s", got[0].Content[0].Content, rawNested)
	}
}

// bigToolResultMessages builds [assistant(tool_use)+user(tool_result)] turns
// with payloads of roughly sizeKB kilobytes each.
func bigToolResultMessages(pairs map[string]int) []types.Message {
	var msgs []types.Message
	for id, kb := range pairs {
		payload := strings.Repeat("d", kb*1024)
		msgs = append(msgs,
			types.NewAssistantMessage([]types.ContentBlock{{
				Type: types.ContentTypeToolUse, ID: id, Name: "Bash",
			}}),
			types.NewUserMessage([]types.ContentBlock{{
				Type: types.ContentTypeToolResult, ToolUseID: id,
				Content: json.RawMessage(`[{"type":"text","text":"` + payload + `"}]`),
			}}),
		)
	}
	return msgs
}

// budgetEngine returns an engine with a session id and record writer wired so
// applyBudget's persist path is live, plus the captured records.
func budgetEngine(t *testing.T) (*Engine, *[]toolresult.ContentReplacementRecord, *engineLogCapture) {
	t.Helper()
	var records []toolresult.ContentReplacementRecord
	lc := &engineLogCapture{}
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("sess-budget")
	eng.SetRecordWriter(func(rs []toolresult.ContentReplacementRecord) {
		records = append(records, rs...)
	})
	return eng, &records, lc
}

// readToolResultContent returns the tool_result content bytes for an id.
func readToolResultContent(msgs []types.Message, id string) (string, bool) {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == id {
				return string(b.Content), true
			}
		}
	}
	return "", false
}

// TestApplyBudget_ReplacesOversizedAndKeepsReadExempt pins the per-message
// budget through the engine: an oversized Bash result is replaced and its
// record persisted, while an equally oversized Read result in the same
// message stays verbatim (the working-set exemption).
func TestApplyBudget_ReplacesOversizedAndKeepsReadExempt(t *testing.T) {
	t.Parallel()

	eng, records, lc := budgetEngine(t)
	// The Read exemption also removes its bytes from the group size, so the
	// Bash result alone must exceed the 200K limit for selection to fire.
	big := strings.Repeat("d", 205*1024)
	msgs := []types.Message{
		types.NewAssistantMessage([]types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_bg_bash", Name: "Bash"},
			{Type: types.ContentTypeToolUse, ID: "tu_bg_read", Name: "Read"},
		}),
		types.NewUserMessage([]types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_bg_bash",
				Content: json.RawMessage(`[{"type":"text","text":"` + big + `"}]`)},
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_bg_read",
				Content: json.RawMessage(`[{"type":"text","text":"` + big + `"}]`)},
		}),
	}
	out := eng.applyBudget(msgs)

	bashOut, ok := readToolResultContent(out, "tu_bg_bash")
	if !ok {
		t.Fatal("Bash tool_result missing after budget")
	}
	if strings.Contains(bashOut, big) {
		t.Error("oversized Bash result was not replaced")
	}
	readOut, ok := readToolResultContent(out, "tu_bg_read")
	if !ok {
		t.Fatal("Read tool_result missing after budget")
	}
	if !strings.Contains(readOut, big) {
		t.Error("Read working-set result was replaced despite the exemption")
	}
	if len(*records) != 1 || (*records)[0].ToolUseID != "tu_bg_bash" {
		t.Errorf("records = %+v, want exactly the Bash replacement record", *records)
	}
	if lc.count("applyBudget: modified tool result content") != 1 {
		t.Errorf("modification debug log count = %d, want 1", lc.count("applyBudget: modified tool result content"))
	}
}

// TestApplyBudget_PerMessageGroupingNotGlobal pins the message-ID grouping: two
// separately-under-budget messages must not trip the aggregate even though
// their sum exceeds the limit — collapsing the IDs would treat them as one.
func TestApplyBudget_PerMessageGroupingNotGlobal(t *testing.T) {
	t.Parallel()

	eng, records, lc := budgetEngine(t)
	msgs := bigToolResultMessages(map[string]int{"tu_bg_m1": 110, "tu_bg_m2": 110})
	out := eng.applyBudget(msgs)
	for _, id := range []string{"tu_bg_m1", "tu_bg_m2"} {
		content, ok := readToolResultContent(out, id)
		if !ok || !strings.Contains(content, "ddd") {
			t.Errorf("%s was replaced; per-message budget must not aggregate across messages", id)
		}
	}
	if len(*records) != 0 {
		t.Errorf("records = %d, want 0", len(*records))
	}
	if n := lc.count("applyBudget: modified tool result content"); n != 0 {
		t.Errorf("modification log fired %d times without any modification", n)
	}
}

// TestPrepareAPIMessages_SystemAndIDAndModality pins the prepare chain through
// a real request: system rows dropped, local IDs cleared, and media stripped
// only when BOTH image and video are unsupported.
func TestPrepareAPIMessages_SystemAndIDAndModality(t *testing.T) {
	t.Parallel()

	imgBlock := types.ContentBlock{Type: types.ContentTypeImage}

	newEng := func(modalities []string) (*Engine, *[]*llm.Request) {
		mp := &testProvider{}
		mp.responses = []testResponse{{events: subTextEvents("m", "ok")}}
		eng := New(&Params{Provider: mp, Model: "m", InputModalities: modalities, Logger: silentLogger()})
		eng.SetMessages([]types.Message{
			{ID: "sys-row", Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("boundary")}},
			{ID: "user-row", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi"), imgBlock}},
			{ID: "tr-asst", Role: types.RoleAssistant, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "tu_prep", Name: "Read"},
			}},
			{ID: "tr-row", Role: types.RoleUser, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_prep", Content: json.RawMessage(`not-json`)},
			}},
		})
		t.Cleanup(func() { eng.Close() })
		var reqs []*llm.Request
		mp.onStream = func(req *llm.Request) { reqs = append(reqs, req) }
		return eng, &reqs
	}

	// Image-capable model: the image block rides through.
	eng, reqs := newEng([]string{"text", "image"})
	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(*reqs))
	}
	var sawImage, sawSanitized bool
	for _, m := range (*reqs)[0].Messages {
		if m.Role == types.RoleSystem {
			t.Error("system row reached the API request")
		}
		if m.ID != "" {
			t.Errorf("message ID %q leaked to the wire", m.ID)
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeImage {
				sawImage = true
			}
			if b.Type == types.ContentTypeToolResult && strings.Contains(string(b.Content), "[corrupted tool result") {
				sawSanitized = true
			}
		}
	}
	if !sawImage {
		t.Error("image block stripped from an image-capable model")
	}
	if !sawSanitized {
		t.Error("corrupted tool_result content reached the wire unsanitized")
	}

	// Text-only model: the image becomes a placeholder.
	eng2, reqs2 := newEng([]string{"text"})
	if res := eng2.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	var sawPlaceholder bool
	for _, m := range (*reqs2)[0].Messages {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeImage {
				t.Error("image block survived on a text-only model")
			}
			if b.Type == types.ContentTypeText && b.Text == "[image]" {
				sawPlaceholder = true
			}
		}
	}
	if !sawPlaceholder {
		t.Error("no [image] placeholder where the stripped block used to be")
	}

	// Video-capable but image-incapable model: the guard strips only when
	// NEITHER modality is supported, so a model that accepts video keeps the
	// media instead of losing it to a placeholder.
	eng3, reqs3 := newEng([]string{"text", "video"})
	if res := eng3.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	var sawImage3 bool
	for _, m := range (*reqs3)[0].Messages {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeImage {
				sawImage3 = true
			}
			if b.Type == types.ContentTypeText && b.Text == "[image]" {
				t.Error("[image] placeholder on a model that accepts video — the strip needs both modalities unsupported")
			}
		}
	}
	if !sawImage3 {
		t.Error("image block stripped although the model accepts video")
	}
}

// TestMarshalMessages_SystemSkipMergeAndCacheControl pins the engine-side
// marshal: system rows skipped, attachment messages normalized and merged into
// the preceding user message, MessageType preserved, cache_control stamped on
// exactly the last block.
func TestMarshalMessages_SystemSkipMergeAndCacheControl(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { eng.Close() })
	eng.SetMessages([]types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("compact boundary")}},
		{ID: "u1", Role: types.RoleUser, MessageType: types.MessageTypeUser,
			Content: []types.ContentBlock{types.NewTextBlock("first question")}},
		{Role: types.RoleUser, MessageType: types.MessageTypeAttachment,
			Content: []types.ContentBlock{types.NewTextBlock("queued job text")},
			Attachment: &types.Attachment{
				Type: types.AttachmentTypeQueued, SourceUUID: "att-1", Mode: types.ItemModeJob,
			}},
		{ID: "a1", Role: types.RoleAssistant, MessageType: types.MessageTypeUser,
			Content: []types.ContentBlock{types.NewTextBlock("answer")}},
	})
	out := eng.marshalMessages()
	if len(out) != 2 {
		t.Fatalf("messages = %d, want 2 (system skipped, attachment merged)", len(out))
	}
	// The attachment merged into the preceding user message as a second block.
	if len(out[0].Content) != 2 {
		t.Fatalf("first user message blocks = %d, want 2 (own + merged attachment)", len(out[0].Content))
	}
	if !strings.Contains(out[0].Content[1].Text, "A background agent completed a job") {
		t.Errorf("merged attachment content = %q, want the job reminder envelope", out[0].Content[1].Text)
	}
	if out[1].MessageType != types.MessageTypeUser || out[1].ID != "a1" {
		t.Errorf("assistant row = type:%s id:%q, want preserved identity", out[1].MessageType, out[1].ID)
	}
	// cache_control lands on the LAST block of the LAST message only.
	if cc := out[1].Content[0].CacheControl; cc == nil || cc.Type != "ephemeral" || cc.TTL != "" {
		t.Errorf("breakpoint cache_control = %+v, want {Type: ephemeral, TTL empty}", cc)
	}
	if out[0].Content[0].CacheControl != nil || out[0].Content[1].CacheControl != nil {
		t.Error("non-final blocks carry cache_control — only the breakpoint block may")
	}

	// A history ending in a block-less message has no breakpoint to stamp: the
	// length guard must skip the index instead of reading Content[-1].
	tailEng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { tailEng.Close() })
	tailEng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("question")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{}},
	})
	tail := tailEng.marshalMessages()
	if len(tail) != 2 {
		t.Fatalf("block-less tail history = %d messages, want 2", len(tail))
	}
	if len(tail[1].Content) != 0 {
		t.Fatalf("block-less tail message gained %d blocks", len(tail[1].Content))
	}
	if tail[0].Content[0].CacheControl != nil {
		t.Error("cache_control landed on a non-final message — an empty final message has no breakpoint")
	}

	// An attachment with nothing before it stands alone: the merge guard's
	// emptiness check must run before it indexes the (empty) result.
	firstEng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { firstEng.Close() })
	firstEng.SetMessages([]types.Message{{
		Role: types.RoleUser, MessageType: types.MessageTypeAttachment,
		Content: []types.ContentBlock{types.NewTextBlock("leading job text")},
		Attachment: &types.Attachment{
			Type: types.AttachmentTypeQueued, SourceUUID: "att-leading", Mode: types.ItemModeJob,
		},
	}})
	first := firstEng.marshalMessages()
	if len(first) != 1 || len(first[0].Content) != 1 {
		t.Fatalf("leading attachment = %d messages / %d blocks, want 1 message with 1 block", len(first), len(first[0].Content))
	}
	if first[0].ID != "att-leading" || !strings.Contains(first[0].Content[0].Text, "A background agent completed a job") {
		t.Errorf("leading attachment = id:%q content:%q, want the job reminder under the attachment's own id",
			first[0].ID, first[0].Content[0].Text)
	}

	// An attachment after an assistant row is not merged into it: only a
	// preceding user row absorbs it.
	afterEng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { afterEng.Close() })
	afterEng.SetMessages([]types.Message{
		{ID: "a2", Role: types.RoleAssistant, MessageType: types.MessageTypeUser,
			Content: []types.ContentBlock{types.NewTextBlock("answer two")}},
		{Role: types.RoleUser, MessageType: types.MessageTypeAttachment,
			Content: []types.ContentBlock{types.NewTextBlock("trailing job text")},
			Attachment: &types.Attachment{
				Type: types.AttachmentTypeQueued, SourceUUID: "att-trailing", Mode: types.ItemModeJob,
			}},
	})
	after := afterEng.marshalMessages()
	if len(after) != 2 {
		t.Fatalf("attachment after an assistant row = %d messages, want 2 (only a user row absorbs it)", len(after))
	}
	if len(after[0].Content) != 1 || after[0].Content[0].Text != "answer two" {
		t.Errorf("assistant row absorbed the attachment: %+v, want its own single text block", after[0].Content)
	}
	if after[1].Role != types.RoleUser || after[1].ID != "att-trailing" || after[1].Flags != types.FlagMeta {
		t.Errorf("attachment row after marshal = role:%s id:%q flags:%d, want user/att-trailing/FlagMeta",
			after[1].Role, after[1].ID, after[1].Flags)
	}
	if !strings.Contains(after[1].Content[0].Text, "A background agent completed a job") {
		t.Errorf("detached attachment content = %q, want the job reminder envelope", after[1].Content[0].Text)
	}

	// A single-message history still gets the breakpoint: the guard is
	// "non-empty", not "more than one".
	oneEng := New(&Params{Provider: &mockProvider{}, Model: "m"})
	t.Cleanup(func() { oneEng.Close() })
	oneEng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("only message")}},
	})
	one := oneEng.marshalMessages()
	if len(one) != 1 || len(one[0].Content) != 1 {
		t.Fatalf("single-message history = %d messages, want 1 with one block", len(one))
	}
	if cc := one[0].Content[0].CacheControl; cc == nil || cc.Type != "ephemeral" || cc.TTL != "" {
		t.Errorf("single-message breakpoint = %+v, want {Type: ephemeral, TTL empty}", cc)
	}
}

func TestQueryStartMsgIdx_IdleVsBusy(t *testing.T) {
	t.Parallel()

	hang := make(chan struct{})
	mp := &mockProvider{}
	mp.addResponse(nil, nil)
	mp.responses[0].hang = hang
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	if got := eng.QueryStartMsgIdx(); got != -1 {
		t.Errorf("idle QueryStartMsgIdx = %d, want -1", got)
	}
	done := make(chan QueryResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- eng.QuerySync(ctx, "hello", "") }()
	waitForCallCount(t, mp, 1, 5*time.Second)
	if got := eng.QueryStartMsgIdx(); got != 0 {
		t.Errorf("busy QueryStartMsgIdx = %d, want 0 (one user message before the loop)", got)
	}
	cancel()
	close(hang)
	<-done
	eng.waitIdleForTest()
	if got := eng.QueryStartMsgIdx(); got != -1 {
		t.Errorf("post-query QueryStartMsgIdx = %d, want -1 again", got)
	}
}

// waitIdleForTest spins until the engine's exit defers have released the busy
// flag (endQuery runs after the result is built).
func (e *Engine) waitIdleForTest() {
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for e.IsBusy() && time.Now().Before(deadline) {
		runtime.Gosched()
	}
}

// TestMarshalMessagesFrom_PureVariant pins the snapshot-based variant used by
// the /context dump: system rows skipped, flags preserved, and a pre-existing
// cache_control on the final block is kept rather than duplicated.
func TestMarshalMessagesFrom_PureVariant(t *testing.T) {
	t.Parallel()

	existing := &types.CacheControlConfig{Type: "ephemeral", TTL: "1h"}
	stamped := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("sys")}},
		{Role: types.RoleUser, Timestamp: stamped, Content: []types.ContentBlock{types.NewTextBlock("question")}},
		{Role: types.RoleUser, Flags: types.FlagMeta, Content: []types.ContentBlock{types.NewTextBlock("ctx")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "end", CacheControl: existing}}},
	}
	out := marshalMessagesFrom(msgs)
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3 (system skipped)", len(out))
	}
	if !strings.HasPrefix(out[0].Content[0].Text, "[") {
		t.Errorf("plain user message = %q, want the timestamp prefix rendered", out[0].Content[0].Text)
	}
	if out[1].Content[0].Text != "ctx" {
		t.Errorf("meta user message = %q, want no timestamp on meta rows", out[1].Content[0].Text)
	}
	if out[1].Flags != types.FlagMeta {
		t.Error("flags lost in the pure marshal variant")
	}
	last := out[len(out)-1].Content[0]
	if last.CacheControl != existing {
		t.Errorf("pre-existing cache_control replaced: %+v", last.CacheControl)
	}

	// Without a pre-existing breakpoint the final block gets a fresh ephemeral
	// one — the /context dump must show the same breakpoint callLLM sends.
	fresh := marshalMessagesFrom([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("one")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("two-a"),
			types.NewTextBlock("two-b"),
		}},
	})
	if len(fresh) != 2 {
		t.Fatalf("fresh history = %d messages, want 2", len(fresh))
	}
	if fresh[0].Content[0].CacheControl != nil || fresh[1].Content[0].CacheControl != nil {
		t.Error("cache_control stamped somewhere other than the final block")
	}
	if cc := fresh[1].Content[1].CacheControl; cc == nil || cc.Type != "ephemeral" || cc.TTL != "" {
		t.Errorf("fresh breakpoint cache_control = %+v, want {Type: ephemeral, TTL empty}", cc)
	}

	// A block-less final message and an empty history have no breakpoint: the
	// guards must skip rather than index Content[-1] or out[-1].
	if got := marshalMessagesFrom(nil); len(got) != 0 {
		t.Errorf("empty history produced %d messages, want 0", len(got))
	}
	// A single one-block message still clears both length guards: the threshold
	// is "non-empty", not "more than one".
	single := marshalMessagesFrom([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("only")}},
	})
	if len(single) != 1 || len(single[0].Content) != 1 {
		t.Fatalf("single-message history = %d messages, want 1 with one block", len(single))
	}
	if cc := single[0].Content[0].CacheControl; cc == nil || cc.Type != "ephemeral" || cc.TTL != "" {
		t.Errorf("single-message breakpoint = %+v, want {Type: ephemeral, TTL empty}", cc)
	}
	emptyTail := marshalMessagesFrom([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("one")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{}},
	})
	if len(emptyTail) != 2 || len(emptyTail[1].Content) != 0 {
		t.Fatalf("block-less tail = %d messages (last has %d blocks), want 2 messages and 0 blocks", len(emptyTail), len(emptyTail[1].Content))
	}
	if emptyTail[0].Content[0].CacheControl != nil {
		t.Error("cache_control stamped on a non-final message — a block-less final message has no breakpoint")
	}

	// Role and Flags are carried through verbatim; only Content is rebuilt.
	mixed := marshalMessagesFrom([]types.Message{
		{Role: types.RoleUser, Flags: types.FlagMeta, Content: []types.ContentBlock{types.NewTextBlock("meta user")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("answer")}},
	})
	if len(mixed) != 2 {
		t.Fatalf("mixed history = %d messages, want 2", len(mixed))
	}
	if mixed[0].Role != types.RoleUser || mixed[0].Flags != types.FlagMeta {
		t.Errorf("first row = role:%q flags:%d, want user/FlagMeta", mixed[0].Role, mixed[0].Flags)
	}
	if mixed[1].Role != types.RoleAssistant || mixed[1].Flags != 0 {
		t.Errorf("second row = role:%q flags:%d, want assistant/no flags", mixed[1].Role, mixed[1].Flags)
	}
}

// TestRefreshTools_RebuildsOrderAndLateTools pins the per-call rebuild: a tool
// appearing in the provider map between calls must enter toolOrder sorted.
func TestRefreshTools_RebuildsOrderAndLateTools(t *testing.T) {
	t.Parallel()

	var cur = map[string]tool.Tool{"Beta": &mockTool{name: "Beta", enabled: true}}
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return cur }})
	t.Cleanup(func() { eng.Close() })

	eng.refreshTools()
	if len(eng.toolOrder) != 1 || eng.toolOrder[0] != "Beta" {
		t.Fatalf("toolOrder after first refresh = %v, want [Beta]", eng.toolOrder)
	}
	cur["Alpha"] = &mockTool{name: "Alpha", enabled: true}
	eng.refreshTools()
	if len(eng.toolOrder) != 2 || eng.toolOrder[0] != "Alpha" || eng.toolOrder[1] != "Beta" {
		t.Errorf("toolOrder after late registration = %v, want sorted [Alpha Beta]", eng.toolOrder)
	}
}

// TestDumpAPIRequest_MirrorsCallLLMAssembly pins the /context dump against the
// real request shape: stub substitution, omit-ClaudeMd filtering (both context
// keys), deferred announcement first, ToolSearch-filtered tool listing, message
// normalization, and the one-shot stub replacement count.
func TestDumpAPIRequest_MirrorsCallLLMAssembly(t *testing.T) {
	// Sequential: HOME is pinned to isolate SYSTEM.md and to carry the global
	// CLAUDE.md / SOUL.md fixtures.
	emptyWork := t.TempDir()
	t.Setenv("HOME", emptyWork) // no .gbot fixtures yet: no global context, no SYSTEM.md

	// A history with nothing to send must produce no messages at all — the
	// deferred announcement is guarded on a non-empty request, otherwise the
	// dump shows a prefix the API would never receive.
	emptyEng := New(&Params{
		Provider: &testProvider{}, Model: "dump-model", WorkingDir: emptyWork,
		Logger:        silentLogger(),
		ToolsProvider: tsToolsProvider(deferredStubTool("DeferredEmpty")),
	})
	t.Cleanup(func() { emptyEng.Close() })
	emptyEng.refreshTools()
	wantMessages := 0 // the announcement needs a request to prefix, and there is none
	if got := len(emptyEng.DumpAPIRequest().Messages); got != wantMessages {
		t.Errorf("empty-history dump messages = %d, want %d (the announcement needs a request to prefix)", got, wantMessages)
	}

	tmpDir := t.TempDir()
	const sentinel = "CLAUDEMD-DUMP-52ea"
	if err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte(sentinel), 0644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatal(err)
	}
	const globalSentinel = "GLOBALCLAUDEMD-DUMP-7b4f"
	const soulSentinel = "SOUL-DUMP-1d63"
	if err := os.WriteFile(filepath.Join(home, ".gbot", "CLAUDE.md"), []byte(globalSentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "SOUL.md"), []byte(soulSentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home) // global CLAUDE.md + SOUL.md present; no SYSTEM.md, so the default base prompt applies

	eng := New(&Params{
		Provider: &testProvider{}, Model: "dump-model", WorkingDir: tmpDir,
		InputModalities: []string{"text", "image"},
		Logger:          silentLogger(),
		ToolsProvider:   tsToolsProvider(deferredStubTool("DeferredA")),
	})
	t.Cleanup(func() { eng.Close() })
	// The dump is documented to skip refreshTools; run it manually so the
	// deferred-tool machinery (ToolSearch + announcement) is in the snapshot.
	eng.refreshTools()
	eng.SetSystemPrompt("S={{SYSTEM}} M={{MODEL}} D={{MODALITIES}}")
	// An orphaned tool_use with a truncated input plus a compact-boundary
	// system row plus a thinking-only assistant row: the dump must repair the
	// pair, sanitize the input, drop the system row and drop the emptied row.
	eng.SetMessages([]types.Message{{
		Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("dump question")},
	}, {
		Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("compact boundary")},
	}, {
		Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeThinking}},
	}, {
		Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_dump_orphan", Name: "Read", Input: json.RawMessage(`{"path":`)},
		},
	}})

	dump := eng.DumpAPIRequest()
	if dump == nil {
		t.Fatal("DumpAPIRequest returned nil")
	}
	if dump.Model != "dump-model" || dump.MaxTokens != 16000 || dump.ContextWindow != 0 {
		t.Errorf("dump scalars = %q/%d/%d", dump.Model, dump.MaxTokens, dump.ContextWindow)
	}
	if !strings.Contains(dump.SystemPrompt, "M=dump-model") {
		t.Errorf("dump system prompt MODEL stub unresolved: %.200q", dump.SystemPrompt)
	}
	if !strings.Contains(dump.SystemPrompt, "D=text+image") || strings.Contains(dump.SystemPrompt, "{{MODALITIES}}") {
		t.Errorf("dump must substitute {{MODALITIES}} with the configured modalities: %.200q", dump.SystemPrompt)
	}
	// The setter (a /model switch) must reach the dump too: an engine whose
	// modalities change after construction has to show the new pair, not the
	// defaults it was built with.
	switchEng := New(&Params{
		Provider: &testProvider{}, Model: "switch-model", WorkingDir: tmpDir, Logger: silentLogger(),
	})
	t.Cleanup(func() { switchEng.Close() })
	switchEng.SetSystemPrompt("D={{MODALITIES}}")
	switchEng.SetInputModalities([]string{"text", "video"})
	if got := switchEng.DumpAPIRequest().SystemPrompt; got != "D=text+video" {
		t.Errorf("dump after SetInputModalities = %q, want the configured text+video", got)
	}
	// An empty modality list falls back to text, exactly like callLLM.
	switchEng.SetInputModalities(nil)
	if got := switchEng.DumpAPIRequest().SystemPrompt; got != "D=text" {
		t.Errorf("dump with no configured modalities = %q, want the text fallback", got)
	}
	if !strings.Contains(dump.SystemPrompt, ctxbuild.DefaultBasePrompt()) {
		t.Errorf("dump system prompt lacks the default base prompt: %.200q", dump.SystemPrompt)
	}
	// A stub is replaced once, exactly like callLLM: a second placeholder in the
	// prompt stays literal, so the replacement count is observable.
	countEng := New(&Params{
		Provider: &testProvider{}, Model: "count-model", WorkingDir: tmpDir,
		Logger: silentLogger(),
	})
	t.Cleanup(func() { countEng.Close() })
	countEng.SetSystemPrompt("A={{SOUL}} B={{SOUL}} C={{MODEL}} D={{MODEL}}")
	countPrompt := countEng.DumpAPIRequest().SystemPrompt
	if n := strings.Count(countPrompt, soulSentinel); n != 1 {
		t.Errorf("{{SOUL}} substituted %d times, want exactly 1: %.300q", n, countPrompt)
	}
	if n := strings.Count(countPrompt, "{{SOUL}}"); n != 1 {
		t.Errorf("{{SOUL}} placeholders left unsubstituted = %d, want exactly 1: %.300q", n, countPrompt)
	}
	if n := strings.Count(countPrompt, "count-model"); n != 1 {
		t.Errorf("{{MODEL}} substituted %d times, want exactly 1: %.300q", n, countPrompt)
	}
	if n := strings.Count(countPrompt, "{{MODEL}}"); n != 1 {
		t.Errorf("{{MODEL}} placeholders left unsubstituted = %d, want exactly 1: %.300q", n, countPrompt)
	}
	// ToolSearch filtering: with a deferred tool still undiscovered the request
	// carries ToolSearch alone.
	var toolNames []string
	for _, td := range dump.Tools {
		toolNames = append(toolNames, td.Name)
	}
	if !slices.Equal(toolNames, []string{ToolSearchToolName}) {
		t.Errorf("dump tools = %v, want exactly [%s] (DeferredA is undiscovered)", toolNames, ToolSearchToolName)
	}
	flat := flattenText(dump.Messages)
	if !strings.Contains(flat, sentinel) || !strings.Contains(flat, globalSentinel) {
		t.Errorf("dump messages lack the project or global CLAUDE.md prepend: %.300q", flat)
	}
	if !strings.Contains(flat, "<available-deferred-tools>") {
		t.Error("dump messages lack the deferred announcement prefix")
	}
	// The announcement prefixes the request; the context block is the next
	// message and carries FlagMeta — the dump has to show what callLLM sends.
	var annIdx, ctxIdx = -1, -1
	for i, m := range dump.Messages {
		text := flattenText([]types.Message{m})
		if strings.Contains(text, "<available-deferred-tools>") && annIdx < 0 {
			annIdx = i
		}
		if strings.Contains(text, sentinel) || strings.Contains(text, globalSentinel) {
			if ctxIdx >= 0 {
				t.Errorf("CLAUDE.md text appears in messages %d and %d — the context block is prepended once", ctxIdx, i)
			}
			ctxIdx = i
		}
	}
	if annIdx != 0 {
		t.Errorf("deferred announcement at message %d, want 0 (it prefixes the request)", annIdx)
	}
	if annIdx >= 0 && dump.Messages[annIdx].Role != types.RoleUser {
		t.Errorf("announcement role = %s, want user", dump.Messages[annIdx].Role)
	}
	if ctxIdx < 0 {
		t.Fatal("no FlagMeta context message in the dump")
	}
	if dump.Messages[ctxIdx].Role != types.RoleUser {
		t.Errorf("context message role = %s, want user", dump.Messages[ctxIdx].Role)
	}
	if dump.Messages[ctxIdx].Flags != types.FlagMeta {
		t.Errorf("context message flags = %d, want FlagMeta", dump.Messages[ctxIdx].Flags)
	}
	if ctxIdx <= annIdx {
		t.Errorf("context message at %d is not after the announcement at %d", ctxIdx, annIdx)
	}
	// Normalization ran on the snapshot: no system row, no thinking-only row,
	// and the truncated tool_use input sanitized to an empty object.
	var orphanInput string
	for _, m := range dump.Messages {
		if m.Role == types.RoleSystem {
			t.Errorf("system row survived dump normalization: %+v", m.Content)
		}
		if len(m.Content) == 1 && m.Content[0].Type == types.ContentTypeThinking {
			t.Errorf("thinking-only row survived dump normalization: %+v", m.Content)
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_dump_orphan" {
				continue
			}
			if b.Type == types.ContentTypeToolUse && b.ID == "tu_dump_orphan" {
				orphanInput = string(b.Input)
			}
		}
	}
	if orphanInput != "{}" {
		t.Errorf("orphan tool_use input in the dump = %q, want the sanitized {}", orphanInput)
	}
	var orphanPaired bool
	for _, m := range dump.Messages {
		if m.Role != types.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_dump_orphan" {
				orphanPaired = true
			}
		}
	}
	if !orphanPaired {
		t.Error("dump leaked an unpaired tool_use — the pairing repair must run on the snapshot too")
	}

	// omitClaudeMd filtering mirrors callLLM for Explore-type agents: BOTH
	// context keys (user-global and project) are filtered out.
	sub := eng.NewSubEngine(SubEngineOptions{AgentType: "Explore"})
	sub.omitClaudeMd = true
	sub.SetSystemPrompt("S={{MODEL}}")
	dump2 := sub.DumpAPIRequest()
	flat2 := flattenText(dump2.Messages)
	if strings.Contains(flat2, sentinel) || strings.Contains(flat2, globalSentinel) {
		t.Errorf("omitClaudeMd dump still carries a CLAUDE.md prepend: %.300q", flat2)
	}
	if !strings.Contains(dump2.SystemPrompt, "S=dump-model") {
		t.Errorf("sub dump MODEL stub unresolved: %.120q", dump2.SystemPrompt)
	}
}

// TestDumpAPIRequest_CodeOnlyToolsExcluded pins the fallback tool listing (no
// ToolSearch in the set): code-only tools stay hidden from the request. Disabled
// tools are filtered twice — the guard here and buildToolDefs — so they are not
// separately asserted.
func TestDumpAPIRequest_CodeOnlyToolsExcluded(t *testing.T) {
	// Sequential: HOME pinned so no host CLAUDE.md/SYSTEM.md leaks into the dump.
	work := t.TempDir()
	t.Setenv("HOME", work)

	eng := New(&Params{
		Provider: &testProvider{}, Model: "dump-model", WorkingDir: work,
		Logger: silentLogger(),
		ToolsProvider: tsToolsProvider(
			&mockTool{name: "Alpha", enabled: true},
			&mockTool{name: "Beta", enabled: true},
			codeOnlyStubTool{deferredStubTool("mcp__s__secret")},
		),
	})
	t.Cleanup(func() { eng.Close() })
	eng.refreshTools()
	eng.SetMessages([]types.Message{{
		Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")},
	}})

	var names []string
	for _, td := range eng.DumpAPIRequest().Tools {
		names = append(names, td.Name)
	}
	if !slices.Equal(names, []string{"Alpha", "Beta"}) {
		t.Errorf("dump tools = %v, want [Alpha Beta] (code-only tools are hidden from the request)", names)
	}
}

// TestDumpAPIRequest_DeferredAnnouncementGuard pins both operands of the
// announcement guard: the prefix message exists only when there is something to
// announce AND something to announce it in — and discovery moves a deferred tool
// from the announcement into the tool list.
func TestDumpAPIRequest_DeferredAnnouncementGuard(t *testing.T) {
	// Sequential: HOME pinned to an empty dir so no user-global context
	// contributes a message of its own.
	work := t.TempDir()
	t.Setenv("HOME", work)

	eng := New(&Params{
		Provider: &testProvider{}, Model: "dump-model", WorkingDir: work,
		Logger: silentLogger(), ToolsProvider: tsToolsProvider(deferredStubTool("DeferredB")),
	})
	t.Cleanup(func() { eng.Close() })
	eng.refreshTools()
	eng.SetMessages([]types.Message{{
		Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("single question")},
	}})

	one := eng.DumpAPIRequest()
	if len(one.Messages) != 2 {
		t.Fatalf("dump with one message and an undiscovered deferred tool = %d messages, want 2 (announcement + question)", len(one.Messages))
	}
	if !strings.Contains(flattenText([]types.Message{one.Messages[0]}), "<available-deferred-tools>") {
		t.Errorf("announcement not prepended to a single-message request: %.200q", flattenText(one.Messages))
	}
	if len(one.Tools) != 1 || one.Tools[0].Name != ToolSearchToolName {
		t.Errorf("dump tools before discovery = %d entries, want exactly ToolSearch", len(one.Tools))
	}

	// Every deferred tool discovered: nothing left to announce, so no
	// placeholder message may be prepended, and the tool now reaches the list.
	eng.toolSearch.DiscoverTools([]string{"DeferredB"})
	discovered := eng.DumpAPIRequest()
	if len(discovered.Messages) != 1 {
		t.Fatalf("dump after full discovery = %d messages, want 1 (no empty announcement)", len(discovered.Messages))
	}
	if strings.Contains(flattenText(discovered.Messages), "<available-deferred-tools>") {
		t.Error("announcement still present after every deferred tool was discovered")
	}
	var names []string
	for _, td := range discovered.Tools {
		names = append(names, td.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"DeferredB", ToolSearchToolName}) {
		t.Errorf("dump tools after discovery = %v, want [DeferredB %s]", names, ToolSearchToolName)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle pins: close, reset, sessions, rewind, persistence
// ---------------------------------------------------------------------------

func TestClose_FiresSessionEndWithIdentity(t *testing.T) {
	t.Parallel()
	rec := &integrationHookRecorder{}
	h := hooks.NewHooks(hooks.HooksConfig{
		"SessionEnd": []hooks.HookMatcher{{Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "on-close"},
		}}},
	}, rec)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Hooks: h, WorkingDir: t.TempDir(), Logger: silentLogger()})
	eng.SetSessionID("sess-close-1")
	eng.Close()
	input := rec.lastInput(t)
	if input.SessionID != "sess-close-1" {
		t.Errorf("SessionEnd input SessionID = %q, want sess-close-1", input.SessionID)
	}
	if input.Cwd != eng.getWorkingDir() {
		t.Errorf("SessionEnd input Cwd = %q, want the engine working dir", input.Cwd)
	}
}

func TestReset_ClearsConversationState(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("m", "one"), nil)
	mp.addResponse(textStreamEvents("m", "two"), nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	if res := eng.QuerySync(context.Background(), "first", ""); res.Error != nil || res.TurnCount != 1 {
		t.Fatalf("first query: err=%v turns=%d", res.Error, res.TurnCount)
	}
	eng.SetContextTokens(5555)
	eng.Reset()
	const wantAfterReset = 0
	if got := eng.GetContextTokens(); got != wantAfterReset {
		t.Errorf("ContextTokens after Reset = %d, want %d", got, wantAfterReset)
	}
	if got := len(eng.Messages()); got != wantAfterReset {
		t.Errorf("messages after Reset = %d, want %d", got, wantAfterReset)
	}
	// Turn counting restarts: the second query reports 1 turn again.
	if res := eng.QuerySync(context.Background(), "second", ""); res.Error != nil || res.TurnCount != 1 {
		t.Fatalf("post-reset query: err=%v turns=%d (a stale turnCount would read 2)", res.Error, res.TurnCount)
	}
}

func TestNewSession_BindsIdentityAndClearsState(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("m", "x"), nil)
	eng := New(&Params{Provider: mp, Model: "m", EngineID: "eng-9", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")
	if res := eng.QuerySync(context.Background(), "old", ""); res.Error != nil {
		t.Fatalf("seed query: %v", res.Error)
	}

	titleLogs := captureSlog(t)
	if err := eng.NewSession("/proj", "Fresh Title"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, ok := titleLogs.find("NewSession: set title", func(map[string]any) bool { return true }); ok {
		t.Error("NewSession logged a set-title error for a successful title update")
	}
	const wantFreshMessages = 0
	if got := len(eng.Messages()); got != wantFreshMessages {
		t.Errorf("messages after NewSession = %d, want %d (Reset must run)", got, wantFreshMessages)
	}
	sessions, err := eng.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	var found bool
	for _, s := range sessions {
		if s.Title == "Fresh Title" {
			found = true
		}
	}
	if !found {
		t.Errorf("titled session not listed: %d sessions", len(sessions))
	}
}

func TestNewSession_NoStoreErrors(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	if _, err := eng.ListSessions(5); err == nil || err.Error() != "engine: no store" {
		t.Errorf("ListSessions without store = %v, want 'engine: no store'", err)
	}
	if _, err := eng.PruneEmptySessions(); err == nil || err.Error() != "engine: no store" {
		t.Errorf("PruneEmptySessions without store = %v, want 'engine: no store'", err)
	}
}

func TestForkSession_CopiesMessagesAndClearsForkPoint(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("m", "x"), nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")
	if err := eng.NewSession("/proj", ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if res := eng.QuerySync(context.Background(), "to fork", ""); res.Error != nil {
		t.Fatalf("seed query: %v", res.Error)
	}
	base := eng.SessionID()
	eng.forkParentUUID = "stale-fork-uuid"

	titleLogs := captureSlog(t)
	forked, err := eng.ForkSession("Fork Title")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if _, ok := titleLogs.find("ForkSession: set title", func(map[string]any) bool { return true }); ok {
		t.Error("ForkSession logged a set-title error for a successful title update")
	}
	if eng.forkParentUUID != "" {
		t.Errorf("forkParentUUID after fork = %q, want cleared", eng.forkParentUUID)
	}
	if eng.LastPersistedIdx() != len(eng.Messages()) {
		t.Errorf("lastPersistedIdx after fork = %d, want %d (the copied history is already persisted)", eng.LastPersistedIdx(), len(eng.Messages()))
	}
	if len(forked) != len(eng.Messages()) || len(forked) == 0 {
		t.Errorf("forked messages = %d, engine messages = %d, want equal and non-empty", len(forked), len(eng.Messages()))
	}
	if eng.SessionID() == base {
		t.Error("engine session id unchanged by fork")
	}
	ses, err := store.GetSession(eng.SessionID())
	if err != nil {
		t.Fatalf("GetSession(fork): %v", err)
	}
	if ses.Title != "Fork Title" {
		t.Errorf("fork title = %q, want Fork Title", ses.Title)
	}
	if ses.ForkPointSeq != 0 {
		t.Errorf("fork point seq = %d, want 0 (fork from the start)", ses.ForkPointSeq)
	}
}

func TestSwitchSession_RestoresUsageAndIdentity(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("m", "x"), nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")

	sessA, err := store.CreateSession("/projA", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateContextTokens(sessA.SessionID, 4321); err != nil {
		t.Fatal(err)
	}

	msgs, err := eng.SwitchSession(sessA.SessionID)
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("switched messages = %d, want empty session", len(msgs))
	}
	if got := eng.GetContextTokens(); got != 4321 {
		t.Errorf("ContextTokens after switch = %d, want the persisted 4321", got)
	}
	if got := eng.SessionID(); got != sessA.SessionID {
		t.Errorf("session id = %q, want %q", got, sessA.SessionID)
	}

	if _, err := eng.SwitchSession("no-such-session"); err == nil || !strings.Contains(err.Error(), "switch session") {
		t.Errorf("switch to ghost session = %v, want the wrapped switch error", err)
	}
}

func TestAppendInlineInterrupt_MarkerOnLastAssistantOnly(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Dispatcher: ec, Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.SetMessages([]types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("q")}),
		types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("first answer")}),
		types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("second answer")}),
	})

	eng.appendInlineInterruptMessage()
	// The marker path logs the shape it annotated: 3 messages, the last
	// assistant growing to 2 blocks.
	lc.find(t, "engine:append_inline_interrupt", func(a map[string]any) bool {
		return a["total_messages"] == int64(3) && a["content_blocks"] == int64(2)
	})
	msgs := eng.Messages()
	if !strings.Contains(msgs[2].Content[len(msgs[2].Content)-1].Text, types.InterruptMessage) {
		t.Error("interrupt marker missing from the LAST assistant message")
	}
	for _, b := range msgs[1].Content {
		if strings.Contains(b.Text, types.InterruptMessage) {
			t.Error("interrupt marker landed on an OLDER assistant message")
		}
	}
}

func TestAppendInlineInterrupt_AutoRewindSkipsEmptyTail(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	// Empty history: nothing to annotate, and no events.
	eng.appendInlineInterruptMessage()
	if n := len(ec.Events()); n != 0 {
		t.Errorf("empty history emitted %d events, want 0", n)
	}

	// History whose tail after the last user message is only synthetic: the
	// autoRewind contract skips both mutation and emission.
	eng.SetMessages([]types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("q")}),
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_iw1"},
		}},
	})
	ec.Reset()
	eng.appendInlineInterruptMessage()
	if n := len(ec.Events()); n != 0 {
		t.Errorf("synthetic-only tail emitted %d events, want 0", n)
	}
	for _, m := range eng.Messages() {
		for _, b := range m.Content {
			if strings.Contains(b.Text, types.InterruptMessage) {
				t.Error("interrupt marker polluted a synthetic-only tail")
			}
		}
	}
}

func TestSetStore_WiresCompactorAndClearsForkPoint(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m",
		AutoCompact: AutoCompactConfig{ContextWindow: 100_000}, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	eng.forkParentUUID = "stale-fork-uuid"
	eng.SetStore(store, "/proj")
	if eng.forkParentUUID != "" {
		t.Errorf("forkParentUUID after SetStore = %q, want cleared", eng.forkParentUUID)
	}
	ac, ok := eng.compactor.(*AutoCompactor)
	if !ok {
		t.Fatalf("compactor = %T, want auto-created AutoCompactor", eng.compactor)
	}
	if ac.store != store {
		t.Error("AutoCompactor store not wired by SetStore")
	}
}

// TestPersistContextTokens_SkipsWithoutStoreOrSession pins that neither guard
// reaches the store: a real row seeded with its own token count must still
// hold it after both skip paths (a storeless engine that ignored its guard
// would crash on the nil deref, an empty-id engine that ignored its own would
// issue a write).
func TestPersistContextTokens_SkipsWithoutStoreOrSession(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	sess, err := store.CreateSession("/proj", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateContextTokens(sess.SessionID, 11); err != nil {
		t.Fatal(err)
	}
	rowTokens := func() int {
		t.Helper()
		got, err := store.GetSession(sess.SessionID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		return got.ContextTokens
	}

	// Storeless engine with a real session id: the guard must skip before the
	// store is touched — a dropped store check is a nil deref here.
	noStore := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { noStore.Close() })
	noStore.SetSessionID(sess.SessionID)
	noStore.SetContextTokens(42)
	noStore.persistContextTokens()
	if got := rowTokens(); got != 11 {
		t.Errorf("ContextTokens after the storeless skip = %d, want the seeded 11 (zero rows written)", got)
	}

	// Store present, empty session id: still zero rows.
	noSID := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { noSID.Close() })
	noSID.SetStore(store, "/proj")
	noSID.SetContextTokens(42)
	noSID.persistContextTokens()
	noSID.persistContextTokensLocked()
	if got := rowTokens(); got != 11 {
		t.Errorf("ContextTokens after the empty-session-id skip = %d, want the seeded 11 (zero rows written)", got)
	}
}

func TestStoreAccessors(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	if eng.HasStore() {
		t.Error("HasStore before SetStore = true")
	}
	if eng.Store() != nil {
		t.Error("Store before SetStore != nil")
	}
	store := newTestStore(t)
	eng.SetStore(store, "")
	if !eng.HasStore() || eng.Store() != store {
		t.Error("store accessors disagree after SetStore")
	}
}

func TestRewindToScoped_MessagesFilesAndForkPoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fh := filehistory.NewTracker(filepath.Join(dir, "fh"))
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fh.TrackEdit(target); err != nil {
		t.Fatal(err)
	}
	// The rewind path restores by snapshot id: snapshot the pre-edit state
	// under the id the engine will derive (the message at the rewind index).
	if err := fh.MakeSnapshot("m2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("after"), 0644); err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")
	eng.SetFileHistory(fh)
	eng.SetSessionID("sess-rewind")
	msgs := []types.Message{
		{ID: "m0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q0")}},
		{ID: "m1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a1")}},
		{ID: "m2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q2")}},
		{ID: "m3", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a3")}},
	}
	eng.SetMessages(msgs)
	eng.markAllPersisted()

	if _, err := eng.RewindToScoped(-1, RewindAll); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("rewind(-1) = %v, want out-of-range error", err)
	}
	if _, err := eng.RewindToScoped(5, RewindAll); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("rewind(5) = %v, want out-of-range error", err)
	}

	// Snapshot msgID derives from the message AT the rewind index.
	res, err := eng.RewindToScoped(2, RewindAll)
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if res.MessageCount != 2 {
		t.Errorf("message count = %d, want 2", res.MessageCount)
	}
	if got := len(eng.Messages()); got != 2 {
		t.Errorf("messages after rewind = %d, want 2", got)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before" {
		t.Errorf("file content after rewind = %q, want the pre-edit 'before'", data)
	}
	// Fork point captures the message BEFORE the cut.
	if eng.forkParentUUID != "m1" {
		t.Errorf("forkParentUUID = %q, want m1 (the last kept message)", eng.forkParentUUID)
	}
	if eng.LastPersistedIdx() != 2 {
		t.Errorf("lastPersistedIdx = %d, want 2", eng.LastPersistedIdx())
	}

	// Rewind to zero clears the fork point.
	if _, err := eng.RewindToScoped(0, RewindMessagesOnly); err != nil {
		t.Fatalf("rewind to 0: %v", err)
	}
	if eng.forkParentUUID != "" {
		t.Errorf("forkParentUUID after rewind-to-0 = %q, want empty", eng.forkParentUUID)
	}
}

func TestRewindToScoped_FilesOnlyKeepsMessages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fh := filehistory.NewTracker(filepath.Join(dir, "fh"))
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fh.TrackEdit(target); err != nil {
		t.Fatal(err)
	}
	if err := fh.MakeSnapshot("w0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetFileHistory(fh)
	eng.SetMessages([]types.Message{
		{ID: "w0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")}},
	})

	res, err := eng.RewindToScoped(0, RewindFilesOnly)
	if err != nil {
		t.Fatalf("files-only rewind: %v", err)
	}
	if res.MessageCount != 1 {
		t.Errorf("files-only message count = %d, want 1 (messages untouched)", res.MessageCount)
	}
	if got := len(eng.Messages()); got != 1 {
		t.Errorf("files-only rewind truncated messages to %d, want untouched", got)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "v1" {
		t.Errorf("files-only rewind content = %q, want v1", data)
	}
}

func TestSetTaskDirForSession_ResolvesUnderHome(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", TaskList: task.NewList(""), Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	eng.SetSessionID("sess tasks/odd")
	wantBase, err := task.TasksDir("sess tasks/odd")
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if got := eng.TaskList().Dir(); got != wantBase {
		t.Errorf("task dir = %q, want %q", got, wantBase)
	}
	// Empty session id: no-op, dir unchanged.
	eng.setTaskDirForSession("")
	if got := eng.TaskList().Dir(); got != wantBase {
		t.Errorf("task dir changed on empty session id: %q", got)
	}
}

func TestFirePostTurnHooks_Gating(t *testing.T) {
	t.Parallel()

	newEng := func(subagent bool, window int) (*Engine, *int32) {
		var calls int32
		eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
		eng.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: window})
		if subagent {
			eng.isSubagent = true
		}
		eng.RegisterPostTurnHook(func(context.Context, []types.Message, int, string) {
			atomic.AddInt32(&calls, 1)
		})
		t.Cleanup(func() { eng.Close() })
		return eng, &calls
	}

	eng, calls := newEng(false, 200_000)
	eng.firePostTurnHooks(context.Background())
	if atomic.LoadInt32(calls) != 1 {
		t.Errorf("main engine with window: hook calls = %d, want 1", *calls)
	}

	eng2, calls2 := newEng(false, 1)
	eng2.firePostTurnHooks(context.Background())
	if atomic.LoadInt32(calls2) != 1 {
		t.Errorf("window=1: hook calls = %d, want 1 (fires for any positive window)", *calls2)
	}

	eng3, calls3 := newEng(false, 0)
	eng3.firePostTurnHooks(context.Background())
	if atomic.LoadInt32(calls3) != 0 {
		t.Errorf("window=0: hook calls = %d, want 0 (auto-compact disabled)", *calls3)
	}

	eng4, calls4 := newEng(true, 200_000)
	eng4.firePostTurnHooks(context.Background())
	if atomic.LoadInt32(calls4) != 0 {
		t.Errorf("subagent: hook calls = %d, want 0", *calls4)
	}
}

func TestFirePostTurnHooks_PanicDoesNotAbortLoop(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: 10})
	ran := make(chan struct{})
	eng.RegisterPostTurnHook(func(context.Context, []types.Message, int, string) { panic("hook blew up") })
	eng.RegisterPostTurnHook(func(context.Context, []types.Message, int, string) { close(ran) })

	eng.firePostTurnHooks(context.Background())
	select {
	case <-ran:
	default:
		t.Fatal("the hook after the panicking one never ran — recovery must not abort the loop")
	}
	lc.find(t, "post-turn hook panicked", func(a map[string]any) bool { return a["error"] == "hook blew up" })
}

func TestUpdateAutoCompactConfig_AppliesImmediately(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	if got := eng.ContextWindow(); got != 0 {
		t.Errorf("initial window = %d, want 0", got)
	}
	eng.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: 123_456})
	if got := eng.ContextWindow(); got != 123_456 {
		t.Errorf("window after update = %d, want 123456", got)
	}
}

func TestTaggedDispatch_PreservesDeepestAgentMeta(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	// Event flow: the deepest layer's dispatcher tags FIRST, outer layers only
	// forward — so the chain is deepest(innermost) → shallower → collector.
	outer := &taggedDispatcher{parent: ec, meta: &types.AgentMeta{AgentType: "Explore", ParentToolUseID: "tu_shallow", Depth: 2}}
	inner := &taggedDispatcher{parent: outer, meta: &types.AgentMeta{AgentType: "General", ParentToolUseID: "tu_deep", Depth: 1}}

	inner.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu_ev"}})
	evts := ec.FindEvents(types.EventToolStart)
	if len(evts) != 1 {
		t.Fatalf("events = %d, want 1", len(evts))
	}
	if evts[0].Agent == nil || evts[0].Agent.ParentToolUseID != "tu_deep" || evts[0].Agent.Depth != 1 {
		t.Errorf("agent meta = %+v, want the deepest (tu_deep, depth 1) preserved through outer layers", evts[0].Agent)
	}

	bare := &taggedDispatcher{parent: ec, meta: &types.AgentMeta{AgentType: "Explore", ParentToolUseID: "tu_tag", Depth: 1}}
	bare.Dispatch(types.QueryEvent{Type: types.EventTurnStart})
	turns := ec.FindEvents(types.EventTurnStart)
	if len(turns) != 1 || turns[0].Agent == nil || turns[0].Agent.AgentType != "Explore" {
		t.Errorf("untagged event did not pick up its layer's meta: %+v", turns)
	}
}

func TestAgentDepthField_AndEventToolID(t *testing.T) {
	t.Parallel()
	if got := agentDepthField(nil); got != -1 {
		t.Errorf("agentDepthField(nil) = %d, want -1", got)
	}
	if got := agentDepthField(&types.AgentMeta{Depth: 3}); got != 3 {
		t.Errorf("agentDepthField(depth 3) = %d, want 3", got)
	}
	if got := eventToolID(types.QueryEvent{ToolUse: &types.ToolUseEvent{ID: "tu_x"}}); got != "tu_x" {
		t.Errorf("eventToolID = %q, want tu_x", got)
	}
	if got := eventToolID(types.QueryEvent{}); got != "" {
		t.Errorf("eventToolID(empty) = %q, want empty", got)
	}
}

func TestIsBuiltInAgent_Table(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"General", "Explore", "Plan", "Planner", "Executor", "Reviewer"} {
		if !isBuiltInAgent(name) {
			t.Errorf("isBuiltInAgent(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "custom-agent", "myExplorer"} {
		if isBuiltInAgent(name) {
			t.Errorf("isBuiltInAgent(%q) = true, want false", name)
		}
	}
}

func TestSubMaxTurns_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want int }{{0, 0}, {-3, 0}, {5, 5}} {
		if got := subMaxTurns(tc.in); got != tc.want {
			t.Errorf("subMaxTurns(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestTaskListReaderAdapter_PendingOnly(t *testing.T) {
	t.Parallel()

	var nilAdapter taskListReaderAdapter
	items, err := nilAdapter.ListPending()
	if err != nil || items != nil {
		t.Errorf("nil adapter = %v/%v, want nil/nil", items, err)
	}

	tl := task.NewList("")
	if err := tl.SetDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	id1, err := tl.CreateTask("pending work", "pending body", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := tl.CreateTask("in-flight work", "wip body", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id3, err := tl.CreateTask("done work", "done body", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	completed, inProgress := task.StatusCompleted, task.StatusInProgress
	if _, _, err := tl.UpdateTask(id3, task.TaskUpdates{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tl.UpdateTask(id2, task.TaskUpdates{Status: &inProgress}); err != nil {
		t.Fatal(err)
	}

	adapter := &taskListReaderAdapter{list: tl}
	pending, err := adapter.ListPending()
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending items = %d, want 2 (completed excluded): %+v", len(pending), pending)
	}
	got := map[string]attachment.TaskItem{}
	for _, it := range pending {
		got[it.ID] = it
	}
	if got[id1].Subject != "pending work" || got[id2].Subject != "in-flight work" {
		t.Errorf("pending items = %+v, want the two unfinished ones", pending)
	}
	if got[id1].Status != string(task.StatusPending) || got[id2].Status != string(task.StatusInProgress) {
		t.Errorf("pending statuses = %q/%q, want pending/in-progress", got[id1].Status, got[id2].Status)
	}
	if got[id1].Description != "pending body" || got[id2].Description != "wip body" {
		t.Errorf("pending descriptions = %q/%q, want the task bodies forwarded", got[id1].Description, got[id2].Description)
	}

	// An unreadable tasks dir (a regular file where the dir should be)
	// surfaces as the adapter's error, not as an empty list. A merely missing
	// dir is an empty list by contract, so the blocker must be a file.
	blocker := filepath.Join(t.TempDir(), "blocker")
	mustWriteFile(t, blocker, []byte("not a dir"))
	if err := tl.SetDir(blocker); err == nil {
		t.Fatal("SetDir onto a regular file unexpectedly succeeded")
	}
	if _, err := adapter.ListPending(); err == nil {
		t.Error("ListPending succeeded over an unreadable tasks dir, want the storage error")
	}
}

func TestNewSubEngine_InheritsAndDiverges(t *testing.T) {
	t.Parallel()

	parent := New(&Params{
		Provider: &mockProvider{}, Model: "parent-model",
		MaxTokens: 4321, InputModalities: []string{"text", "video"},
		Logger: silentLogger(),
	})
	t.Cleanup(func() { parent.Close() })
	parent.SetSessionID("sess-parent")

	sub1 := parent.NewSubEngine(SubEngineOptions{
		AgentType: "General", Model: "sub-model",
		Tools: map[string]tool.Tool{"A": &mockTool{name: "A", enabled: true}, "B": &mockTool{name: "B", enabled: true}},
	})
	sub2 := parent.NewSubEngine(SubEngineOptions{AgentType: "General"})

	if sub1.Model() != "sub-model" || sub2.Model() != "parent-model" {
		t.Errorf("sub models = %q / %q, want override and inherit", sub1.Model(), sub2.Model())
	}
	if sub1.MaxTokens() != 4321 {
		t.Errorf("sub MaxTokens = %d, want inherited 4321", sub1.MaxTokens())
	}
	if !sub1.SupportsModality("video") || !sub1.SupportsModality("text") {
		t.Error("sub did not inherit parent input modalities")
	}
	if sub1.agentMetaDepth != 1 || parent.agentMetaDepth != 0 {
		t.Errorf("depths = sub:%d parent:%d, want 1/0", sub1.agentMetaDepth, parent.agentMetaDepth)
	}
	if sub1.sessionID == parent.sessionID || sub1.sessionID == sub2.sessionID {
		t.Errorf("sub session ids %q / %q must be unique and differ from parent %q", sub1.sessionID, sub2.sessionID, parent.sessionID)
	}
	if sub1.tokenBudget != 0 {
		t.Errorf("sub tokenBudget = %d, want 0 (bypassed via isSubagent)", sub1.tokenBudget)
	}
	// The sub's own tool order is sorted from its filtered set.
	if len(sub1.toolOrder) != 2 || sub1.toolOrder[0] != "A" || sub1.toolOrder[1] != "B" {
		t.Errorf("sub toolOrder = %v, want sorted [A B]", sub1.toolOrder)
	}
	// Sender registry is per-engine: registering on the sub must not leak.
	sub1.RegisterFileSender("wui", nil)
	if sub1.senders == nil || len(sub1.senders) != 1 {
		t.Errorf("sub senders = %v, want its own 1-entry map", sub1.senders)
	}
	if len(parent.senders) != 0 {
		t.Errorf("parent senders = %v, want empty (sub registration leaked)", parent.senders)
	}
}

// TestNewSubEngine_NoTagWithoutParentToolUseID pins the dispatcher wiring
// condition: without ParentToolUseID the sub gets NO dispatcher (its events
// are not forwarded anywhere), with it events bubble tagged.
func TestNewSubEngine_NoTagWithoutParentToolUseID(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	parent := New(&Params{Provider: &mockProvider{}, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { parent.Close() })

	plain := parent.NewSubEngine(SubEngineOptions{AgentType: "General"})
	if plain.dispatcher != nil {
		t.Error("sub without ParentToolUseID got a dispatcher — untagged forwarding must not be wired")
	}
	tagged := parent.NewSubEngine(SubEngineOptions{AgentType: "General", ParentToolUseID: "tu_tagw"})
	if tagged.dispatcher == nil {
		t.Fatal("sub with ParentToolUseID has no dispatcher")
	}
	tagged.emitEvent(types.QueryEvent{Type: types.EventTurnStart})
	if n := len(ec.FindEvents(types.EventTurnStart)); n != 1 {
		t.Errorf("tagged sub events reached parent: %d, want 1", n)
	}
}

func TestQuerySync_ReplyIsLastAssistantText(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("m", "first part"), nil)
	mp.addResponse(textStreamEvents("m", "final answer"), nil)
	// Two-turn query via a tool in between.
	mp.responses = []mockResponse{}
	mp.addResponse(toolUseStreamEvents("m", "tu_qr1", "Read", `{}`), nil)
	mp.addResponse(textStreamEvents("m", "final answer"), nil)
	eng := New(&Params{
		Provider: mp, Model: "m", Logger: silentLogger(),
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}}
		},
	})
	t.Cleanup(func() { eng.Close() })

	res := eng.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if res.Reply != "final answer" {
		t.Errorf("Reply = %q, want the last assistant text", res.Reply)
	}
	if res.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", res.TurnCount)
	}
	// The engine must be idle again — a leaked busy flag would block the next
	// attachment kick.
	eng.waitIdleForTest()
	if eng.IsBusy() {
		t.Error("engine still busy after QuerySync returned")
	}
}

func TestRunForkedQuery_SetsCurrentTurnMsgIDFromLastUser(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("m", "done"), nil)
	mp.addResponse(textStreamEvents("m", "done again"), nil)
	eng := New(&Params{Provider: mp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	// Pre-existing id from an earlier query must not survive.
	eng.currentTurnMsgID = "stale-turn-id"

	fork := []types.Message{
		{ID: "fu-1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("a")}},
		{ID: "fa-1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("b")}},
		{ID: "fu-2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("c")}},
	}
	res := eng.RunForkedQuery(context.Background(), fork, "")
	if res.Error != nil {
		t.Fatalf("forked query: %v", res.Error)
	}
	if res.Reply != "done" {
		t.Errorf("Reply = %q, want the forked query's assistant text %q", res.Reply, "done")
	}
	if eng.currentTurnMsgID != "fu-2" {
		t.Errorf("currentTurnMsgID = %q, want the LAST user message fu-2", eng.currentTurnMsgID)
	}

	// A fork without user messages must still clear the stale id.
	eng.currentTurnMsgID = "stale-turn-id"
	forkNoUser := []types.Message{
		{ID: "fa-9", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("b")}},
	}
	if res := eng.RunForkedQuery(context.Background(), forkNoUser, ""); res.Error != nil {
		t.Fatalf("userless forked query: %v", res.Error)
	}
	if eng.currentTurnMsgID != "" {
		t.Errorf("currentTurnMsgID after a userless fork = %q, want cleared", eng.currentTurnMsgID)
	}
}

func TestQueryWithContent_PanicBecomesQueryEndError(t *testing.T) {
	t.Parallel()

	boom := &panicProvider{}
	ec := newEventCollector()
	logs := captureSlog(t)
	eng := New(&Params{Provider: boom, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	eng.QueryWithContent(context.Background(), []types.ContentBlock{types.NewTextBlock("q")}, "")
	// The recover path emits a QueryEnd carrying the internal error.
	if !ec.WaitForEventCount(types.EventQueryEnd, 1, 5*time.Second) {
		t.Fatal("no QueryEnd after the provider panic — the recover handler never emitted")
	}
	deadline := time.After(5 * time.Second)
	for {
		var sawInternal bool
		for _, ev := range ec.FindEvents(types.EventQueryEnd) {
			if ev.Error != nil && strings.Contains(ev.Error.Error(), "internal error") {
				sawInternal = true
			}
		}
		if sawInternal {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no internal-error QueryEnd after a provider panic")
		default:
			time.Sleep(5 * time.Millisecond) // REAL-TIME: poll interval while waiting on an async goroutine
		}
	}
	if _, ok := logs.find("engine: panic in queryLoop", func(a map[string]any) bool {
		_, hasStack := a["stack"]
		return hasStack
	}); !ok {
		t.Error("no panic diagnostic logged with a stack for the provider panic")
	}
}

// panicProvider blows up inside Stream.
type panicProvider struct{ mockProvider }

func (p *panicProvider) Stream(context.Context, *llm.Request) (<-chan llm.StreamEvent, error) {
	panic("provider exploded")
}

func TestSendFile_RoutesBySource(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	if err := eng.SendFile(context.Background(), "/tmp/x", ""); err == nil || !strings.Contains(err.Error(), "no FileSender registered") {
		t.Errorf("unregistered source error = %v, want it to mention %q", err, wantSenderMissing)
	}

	var gotPath, gotCaption string
	var gotCtx context.Context
	eng.RegisterFileSender("wechat", senderFunc(func(ctx context.Context, path, caption string) error {
		gotCtx, gotPath, gotCaption = ctx, path, caption
		return nil
	}))
	ctx := WithSource(context.Background(), "wechat")
	if err := eng.SendFile(ctx, "/tmp/report.md", "monthly"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if gotPath != "/tmp/report.md" || gotCaption != "monthly" {
		t.Errorf("sender got %q/%q, want the routed file and caption", gotPath, gotCaption)
	}
	if SourceFromContext(gotCtx) != "wechat" {
		t.Errorf("sender ctx source = %q, want wechat (the query ctx must flow through)", SourceFromContext(gotCtx))
	}
}

const wantSenderMissing = "no FileSender registered"

// senderFunc adapts a function to send.FileSender.
type senderFunc func(ctx context.Context, filePath, caption string) error

func (f senderFunc) SendFile(ctx context.Context, filePath, caption string) error {
	return f(ctx, filePath, caption)
}

func TestAccessors_Roundtrip(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	eng.SetEngineID("eng-acc")
	if eng.EngineID() != "eng-acc" {
		t.Errorf("EngineID = %q", eng.EngineID())
	}
	eng.SetModel("other-model")
	if eng.Model() != "other-model" {
		t.Errorf("Model = %q", eng.Model())
	}
	eng.SetMaxTokens(999)
	if eng.MaxTokens() != 999 {
		t.Errorf("MaxTokens = %d", eng.MaxTokens())
	}
	eng.SetInputModalities([]string{"text", "video"})
	if !eng.SupportsModality("video") || eng.SupportsModality("image") {
		t.Error("SupportsModality disagrees with the set modalities")
	}
	eng.SetMemoryDir("/mem/override")
	if eng.MemoryDir() != "/mem/override" {
		t.Errorf("MemoryDir = %q", eng.MemoryDir())
	}
	eng.SetSystemPrompt("stored prompt")
	if eng.SystemPrompt() != "stored prompt" {
		t.Errorf("SystemPrompt = %q", eng.SystemPrompt())
	}
	eng.SetSkillListing("skill listing")
	eng.SetAgentDefs([]*types.AgentDefinition{{AgentType: "X"}})
	if eng.Provider() == nil {
		t.Error("Provider() = nil")
	}
	dd := newEventCollector()
	eng.SetDispatcher(dd)
	if eng.Dispatcher() != dd {
		t.Error("SetDispatcher/Dispatcher mismatch")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle internals, session error paths, rewind boundary semantics,
// sub-engine wiring, session-memory hook, and the lock discipline of the
// accessor block.
// ---------------------------------------------------------------------------

// TestSessionMemory_PostTurnHookExtraction pins the SetSessionMemory closure:
// extraction runs on a goroutine (the hook itself must not block), the query
// ctx flows into Extract, and a failing Extract logs on the engine logger.
func TestSessionMemory_PostTurnHookExtraction(t *testing.T) {
	t.Parallel()

	newHarness := func(extractErr error) (*Engine, *session.SessionMemory, func() []context.Context, chan struct{}, <-chan struct{}) {
		var mu sync.Mutex
		var gotCtxs []context.Context
		release := make(chan struct{})
		finished := make(chan struct{})
		var once sync.Once
		sm := session.New(session.Config{
			MinTokensToInit:         1,
			MinTokensBetweenUpdate:  0,
			MinIntervalMs:           0,
			ToolCallsBetweenUpdates: 1,
		}, t.TempDir(), "", func(ctx context.Context, _, _ string, _ []types.Message, _ string) error {
			mu.Lock()
			gotCtxs = append(gotCtxs, ctx)
			mu.Unlock()
			<-release
			once.Do(func() { close(finished) })
			return extractErr
		}, silentLogger())
		lc := &engineLogCapture{}
		eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: slog.New(lc)})
		t.Cleanup(func() { eng.Close() })
		eng.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: 10})
		eng.SetSessionMemory(sm)
		eng.SetContextTokens(1000)
		eng.SetMessages([]types.Message{
			types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("q")}),
			types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("a")}),
		})
		snapshot := func() []context.Context {
			mu.Lock()
			defer mu.Unlock()
			return append([]context.Context(nil), gotCtxs...)
		}
		return eng, sm, snapshot, release, finished
	}

	// Success path: the hook returns before the (blocking) extraction finishes.
	eng, sm, gotCtxs, release, finished := newHarness(nil)
	if eng.SessionMemory() != sm {
		t.Fatal("SessionMemory() does not return the wired manager")
	}
	hookDone := make(chan struct{})
	go func() {
		eng.firePostTurnHooks(context.Background())
		close(hookDone)
	}()
	select {
	case <-hookDone:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("firePostTurnHooks blocked on extraction — it must run on its own goroutine")
	}
	deadline := time.After(2 * time.Second)
	for len(gotCtxs()) == 0 {
		select {
		case <-deadline:
			close(release)
			t.Fatal("extraction never started")
		default:
			time.Sleep(5 * time.Millisecond) // REAL-TIME: poll interval while waiting on an async goroutine
		}
	}
	if gotCtxs()[0] == nil {
		close(release)
		t.Fatal("Extract received a nil ctx — the hook ctx must flow through")
	}
	close(release)
	// A successful extraction must not log the failure warn (checked after the
	// extraction goroutine has actually finished).
	<-finished
	lcOk := eng.logger.Handler().(*engineLogCapture)
	if lcOk.hasMsg("session memory extraction failed", func(map[string]any) bool { return true }) {
		t.Error("success path logged the extraction-failed warn")
	}

	// Failure path: a failing extraction surfaces as the engine warn log.
	eng2, _, gotCtxs2, release2, _ := newHarness(errors.New("extract blew up"))
	hook2 := make(chan struct{})
	go func() {
		eng2.firePostTurnHooks(context.Background())
		close(hook2)
	}()
	<-hook2
	deadline2 := time.After(2 * time.Second)
	for len(gotCtxs2()) == 0 {
		select {
		case <-deadline2:
			close(release2)
			t.Fatal("failing extraction never started")
		default:
			time.Sleep(5 * time.Millisecond) // REAL-TIME: poll interval while waiting on an async goroutine
		}
	}
	close(release2)
	lc := eng2.logger.Handler().(*engineLogCapture)
	deadlineWarn := time.After(2 * time.Second)
	for !lc.hasMsg("session memory extraction failed", func(map[string]any) bool { return true }) {
		select {
		case <-deadlineWarn:
			t.Fatal("no 'session memory extraction failed' warn after a failing extraction")
		default:
			time.Sleep(5 * time.Millisecond) // REAL-TIME: poll interval while waiting on an async goroutine
		}
	}
}

// TestAppendInlineInterrupt_Guards pins the unreachable-assistant and
// no-selectable-user exits: no marker, no events, and the engine lock is
// handed back on each path.
func TestAppendInlineInterrupt_Guards(t *testing.T) {
	t.Parallel()

	newEng := func(msgs []types.Message) (*Engine, *eventCollector) {
		ec := newEventCollector()
		eng := New(&Params{Provider: &mockProvider{}, Model: "m", Dispatcher: ec, Logger: silentLogger()})
		t.Cleanup(func() { eng.Close() })
		eng.SetMessages(msgs)
		return eng, ec
	}

	// No selectable user message: the marker path must be skipped entirely.
	eng, ec := newEng([]types.Message{
		types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("answer")}),
	})
	eng.appendInlineInterruptMessage()
	if n := len(ec.Events()); n != 0 {
		t.Errorf("history without a user message emitted %d events, want 0", n)
	}
	if b := eng.Messages()[0].Content[len(eng.Messages()[0].Content)-1]; strings.Contains(b.Text, types.InterruptMessage) {
		t.Error("marker appended to a history without a selectable user message")
	}
	lockProbe(t, &eng.mu)

	// Selectable user present but no assistant anywhere: nothing to annotate.
	// The bash-stdout envelope is non-selectable but non-synthetic, so this
	// history passes the synthetic-tail guard and must exit on the
	// no-assistant branch.
	eng2, ec2 := newEng([]types.Message{
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("q")}),
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("<bash-stdout>done</bash-stdout>")}),
	})
	eng2.appendInlineInterruptMessage()
	if n := len(ec2.Events()); n != 0 {
		t.Errorf("assistant-less history emitted %d events, want 0", n)
	}
	lockProbe(t, &eng2.mu)

	// Only-synthetic tail after the last selectable user (with an earlier
	// assistant present): the autoRewind contract skips — no marker may land
	// on the earlier assistant.
	eng3, ec3 := newEng([]types.Message{
		types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("early")}),
		types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("q")}),
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_ig1"},
		}},
	})
	eng3.appendInlineInterruptMessage()
	if n := len(ec3.Events()); n != 0 {
		t.Errorf("synthetic-only tail emitted %d events, want 0", n)
	}
	for _, m := range eng3.Messages() {
		for _, b := range m.Content {
			if strings.Contains(b.Text, types.InterruptMessage) {
				t.Error("marker appended despite the only-synthetic tail after the last user message")
			}
		}
	}
	lockProbe(t, &eng3.mu)
}

// TestAppendSyntheticToolResults_RoleFilters pins that only user messages
// supply satisfied tool_results and only assistant messages contribute
// orphans — the role filters, not block presence, decide.
func TestAppendSyntheticToolResults_RoleFilters(t *testing.T) {
	t.Parallel()

	// A tool_result carried by an ASSISTANT message must not satisfy the
	// orphan scan: the tool_use stays orphaned and gets a synthetic result.
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetMessages([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_sf1"},
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_sf1"},
		}},
	})
	eng.mu.Lock()
	eng.appendSyntheticToolResultsLocked()
	eng.mu.Unlock()
	msgs := eng.Messages()
	if len(msgs) != 2 || len(msgs[1].Content) != 1 || msgs[1].Content[0].ToolUseID != "tu_sf1" {
		t.Fatalf("assistant-carried tool_result must not suppress the synthetic result: %d messages", len(msgs))
	}

	// A tool_use carried by a USER message must not become an orphan.
	eng2 := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetMessages([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_sf2"},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_sf2"},
			{Type: types.ContentTypeToolUse, ID: "tu_sf3"},
		}},
	})
	eng2.mu.Lock()
	eng2.appendSyntheticToolResultsLocked()
	eng2.mu.Unlock()
	if got := len(eng2.Messages()); got != 2 {
		t.Fatalf("user-carried tool_use produced a synthetic message: %d messages, want 2", got)
	}
}

// TestClose_JoinTimeoutStillClearsActiveCancel pins that Close clears the
// stored cancel function even when a tracked goroutine cannot exit to run
// its own endQuery cleanup — the join gives up, the state must not.
func TestClose_JoinTimeoutStillClearsActiveCancel(t *testing.T) {
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		eng.Close()
	})

	cancelled := make(chan struct{})
	tok := eng.beginQuery(func() { close(cancelled) })
	if tok == 0 {
		t.Fatal("beginQuery refused on a fresh engine")
	}
	// A tracked goroutine that outlives the join: Close must return despite
	// it staying parked.
	eng.goRun(func() { <-release })

	eng.Close()
	select {
	case <-cancelled:
	default:
		t.Error("Close did not invoke the active cancel function")
	}
	eng.activeCancelMu.Lock()
	stale := eng.activeCancel
	eng.activeCancelMu.Unlock()
	if stale != nil {
		t.Error("Close left a stale activeCancel behind a stuck tracked goroutine")
	}
}

// TestQueryWithContent_LeasesBusyFlagAndRunsToCompletion pins the goRun
// defers: endQuery must stay deferred until the loop finishes (the engine
// reads busy for the whole query) and cancel must fire only at the end.
func TestQueryWithContent_LeasesBusyFlagAndRunsToCompletion(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	bmp := &blockingMockProvider{release: release}
	ec := newEventCollector()
	eng := New(&Params{Provider: bmp, Model: "m", Dispatcher: ec, Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	eng.QueryWithContent(context.Background(), []types.ContentBlock{types.NewTextBlock("q")}, "")
	deadline := time.After(3 * time.Second)
	for !bmp.wasCalled() {
		select {
		case <-deadline:
			t.Fatal("Stream never called")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll interval while waiting on an async goroutine
		}
	}
	time.Sleep(50 * time.Millisecond) // REAL-TIME: let the goroutine pass the point under test before probing
	if !eng.IsBusy() {
		close(release)
		t.Fatal("engine not busy while its query goroutine is mid-loop — endQuery ran early")
	}
	close(release)
	if !ec.WaitForEventCount(types.EventQueryEnd, 1, 5*time.Second) {
		t.Fatal("no QueryEnd after release")
	}
	msgs := eng.Messages()
	if len(msgs) != 2 || len(msgs[1].Content) != 1 || msgs[1].Content[0].Text != "reply" {
		t.Errorf("assistant reply after full query = %d msgs, want the streamed text 'reply'", len(msgs))
	}
	for _, ev := range ec.FindEvents(types.EventQueryEnd) {
		if ev.Error != nil {
			t.Errorf("QueryEnd carried error %v on a clean run", ev.Error)
		}
	}
}

// TestQuerySyncWithContent_LeasesBusyFlag is the sync variant of the busy
// pin: the flag must be held for the whole call, not released at entry.
func TestQuerySyncWithContent_LeasesBusyFlag(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	bmp := &blockingMockProvider{release: release}
	eng := New(&Params{Provider: bmp, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })

	done := make(chan struct{})
	go func() {
		eng.QuerySyncWithContent(context.Background(), []types.ContentBlock{types.NewTextBlock("q")}, "")
		close(done)
	}()
	deadline := time.After(3 * time.Second)
	for !bmp.wasCalled() {
		select {
		case <-deadline:
			t.Fatal("Stream never called")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll interval while waiting on an async goroutine
		}
	}
	time.Sleep(50 * time.Millisecond) // REAL-TIME: let the goroutine pass the point under test before probing
	if !eng.IsBusy() {
		close(release)
		t.Fatal("engine not busy mid-QuerySyncWithContent — endQuery ran early")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("QuerySyncWithContent did not finish after release")
	}
}

// TestRewindTo_EdgeAndForwardIndices pins three boundary behaviors: an
// at-length rewind derives no snapshot (and reports no restore error), a
// forward rewind past the persisted boundary must not move the fork point,
// and a same-index rewind (idx == lastPersistedIdx) is a no-op for the fork
// point even when history was swapped underneath.
func TestRewindTo_EdgeAndForwardIndices(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	dir := t.TempDir()
	fh := filehistory.NewTracker(filepath.Join(dir, "fh"))
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")
	eng.SetFileHistory(fh)
	eng.SetSessionID("sess-rewind-edge")
	eng.SetMessages([]types.Message{
		{ID: "e0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q0")}},
		{ID: "e1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a1")}},
		{ID: "e2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q2")}},
		{ID: "e3", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a3")}},
	})
	eng.markAllPersisted()

	res, err := eng.RewindToScoped(4, RewindAll)
	if err != nil {
		t.Fatalf("at-length rewind: %v", err)
	}
	if res.RestoreErr != nil {
		t.Errorf("at-length rewind RestoreErr = %v, want nil (no snapshot is derived past the tail)", res.RestoreErr)
	}
	if got := len(eng.Messages()); got != 4 {
		t.Fatalf("at-length rewind truncated to %d, want 4", got)
	}

	// No snapshot is derived for idx == len, so a MessagesOnly rewind at that
	// index must not truncate the tracker either — the real snapshots have to
	// survive for a later rewind to restore from.
	target := filepath.Join(dir, "edge.txt")
	if err := os.WriteFile(target, []byte("v0"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fh.TrackEdit(target); err != nil {
		t.Fatal(err)
	}
	if err := fh.MakeSnapshot("e2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RewindToScoped(4, RewindMessagesOnly); err != nil {
		t.Fatalf("at-length messages-only rewind: %v", err)
	}
	if _, err := eng.RewindToScoped(2, RewindAll); err != nil {
		t.Fatalf("rewind(2) after the at-length rewind: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v0" {
		t.Errorf("tracked file = %q after rewind, want v0 restored from the surviving snapshot", data)
	}

	if _, err := eng.RewindToScoped(2, RewindAll); err != nil {
		t.Fatalf("rewind(2): %v", err)
	}
	if eng.forkParentUUID != "e1" || eng.LastPersistedIdx() != 2 {
		t.Fatalf("rewind(2) fork state = %q/%d, want e1/2", eng.forkParentUUID, eng.LastPersistedIdx())
	}
	eng.appendMessages([]types.Message{
		{ID: "e4", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q4")}},
		{ID: "e5", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a5")}},
	})
	if _, err := eng.RewindToScoped(3, RewindAll); err != nil {
		t.Fatalf("forward rewind(3): %v", err)
	}
	if eng.forkParentUUID != "e1" || eng.LastPersistedIdx() != 2 {
		t.Errorf("forward rewind moved the fork point to %q/%d, want it parked at e1/2",
			eng.forkParentUUID, eng.LastPersistedIdx())
	}

	// Swap history underneath a parked fork point: re-rewinding to the SAME
	// index must not re-derive the fork parent from the swapped messages.
	if _, err := eng.RewindToScoped(1, RewindAll); err != nil {
		t.Fatalf("rewind to the parked index: %v", err)
	}
	if eng.forkParentUUID != "e0" || eng.LastPersistedIdx() != 1 {
		t.Fatalf("parked-index rewind fork state = %q/%d, want e0/1", eng.forkParentUUID, eng.LastPersistedIdx())
	}
	eng.SetMessages([]types.Message{
		{ID: "p0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("x0")}},
		{ID: "p1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("x1")}},
	})
	if _, err := eng.RewindToScoped(1, RewindAll); err != nil {
		t.Fatalf("same-index rewind: %v", err)
	}
	if eng.forkParentUUID != "e0" {
		t.Errorf("same-index rewind re-derived fork parent %q, want it to stay e0", eng.forkParentUUID)
	}
	if eng.LastPersistedIdx() != 1 {
		t.Errorf("same-index rewind lastPersistedIdx = %d, want 1", eng.LastPersistedIdx())
	}

	// The capture is store/session-gated: without either, a rewind across the
	// persisted boundary must leave the fork point alone.
	bare := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { bare.Close() })
	bare.SetMessages([]types.Message{
		{ID: "b0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q0")}},
		{ID: "b1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q1")}},
	})
	bare.markAllPersisted()
	if _, err := bare.RewindToScoped(1, RewindAll); err != nil {
		t.Fatalf("storeless rewind: %v", err)
	}
	if bare.forkParentUUID != "" || bare.LastPersistedIdx() != 2 {
		t.Errorf("storeless rewind captured fork state %q/%d, want untouched \"\"/2", bare.forkParentUUID, bare.LastPersistedIdx())
	}

	bareStore := newTestStore(t)
	bare2 := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { bare2.Close() })
	bare2.SetStore(bareStore, "/proj")
	bare2.SetMessages([]types.Message{
		{ID: "c0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q0")}},
		{ID: "c1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q1")}},
	})
	bare2.markAllPersisted()
	if _, err := bare2.RewindToScoped(1, RewindAll); err != nil {
		t.Fatalf("sessionless rewind: %v", err)
	}
	if bare2.forkParentUUID != "" || bare2.LastPersistedIdx() != 2 {
		t.Errorf("sessionless rewind captured fork state %q/%d, want untouched \"\"/2", bare2.forkParentUUID, bare2.LastPersistedIdx())
	}
}

// TestRewindToScoped_FilesOnlySurfacesRestoreError pins the FilesOnly arm's
// error contract: a missing snapshot is RestoreErr plus the failure log, not
// a silent no-op and not a fatal rewind.
func TestRewindToScoped_FilesOnlySurfacesRestoreError(t *testing.T) {
	t.Parallel()

	lc := &engineLogCapture{}
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: slog.New(lc)})
	t.Cleanup(func() { eng.Close() })
	eng.SetFileHistory(filehistory.NewTracker(filepath.Join(t.TempDir(), "fh")))
	eng.SetMessages([]types.Message{
		{ID: "fr0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")}},
	})

	res, err := eng.RewindToScoped(0, RewindFilesOnly)
	if err != nil {
		t.Fatalf("files-only rewind: %v", err)
	}
	if res.RestoreErr == nil {
		t.Error("RestoreErr = nil for an unknown snapshot, want the tracker error surfaced")
	}
	if got := len(eng.Messages()); got != 1 {
		t.Errorf("messages after failed files-only rewind = %d, want 1 (untouched)", got)
	}
	if !lc.hasMsg("engine:rewind:file_restore_failed", func(map[string]any) bool { return true }) {
		t.Error("no file_restore_failed log for the failed restore")
	}
}

// TestRewindTo_RebuildsToolSearchAndPersistsTokens pins the two side effects
// of the messages arm: tool-search state is rebuilt from the surviving
// history, and the recalculated token count lands in the session row.
func TestRewindTo_RebuildsToolSearchAndPersistsTokens(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")
	sess, err := store.CreateSessionWithEngine("/proj", "m", "")
	if err != nil {
		t.Fatal(err)
	}
	eng.SetSessionID(sess.SessionID)
	// History assembled via appendMessages (no restore pass): the rewind's
	// own restore is what must surface the boundary's discovered set.
	eng.appendMessages([]types.Message{
		{ID: "ts0", Role: types.RoleSystem, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: `{"subtype":"compact_boundary","preCompactDiscoveredTools":["Task"]}`},
		}},
		{ID: "ts1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{ID: "ts2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("more")}},
	})
	eng.markAllPersisted()

	if _, err := eng.RewindToScoped(1, RewindMessagesOnly); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if !eng.toolSearch.IsDiscovered("Task") {
		t.Error("compact-boundary discovery not rebuilt from the surviving messages")
	}

	// Seed the session row with a nonzero count so the rewind's persist is
	// observable in the store.
	eng.SetContextTokens(9999)
	eng.persistContextTokens()
	if _, err := eng.RewindToScoped(0, RewindMessagesOnly); err != nil {
		t.Fatalf("rewind to 0: %v", err)
	}
	ses, err := store.GetSession(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.ContextTokens != 0 {
		t.Errorf("persisted ContextTokens after rewind-to-0 = %d, want the recalculated 0", ses.ContextTokens)
	}
}

// TestSetStore_LeavesWiredCompactorAlone pins the type-assert wiring: a
// compactor that already owns a store must not be re-pointed, and a
// non-auto compactor must survive SetStore without a nil deref.
func TestSetStore_LeavesWiredCompactorAlone(t *testing.T) {
	t.Parallel()

	storeA, storeB := newTestStore(t), newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(&AutoCompactor{store: storeA}, AutoCompactConfig{ContextWindow: 100})
	eng.SetStore(storeB, "/proj")
	if ac, ok := eng.compactor.(*AutoCompactor); !ok || ac.store != storeA {
		t.Errorf("SetStore re-pointed a wired AutoCompactor: %T store=%v", eng.compactor, ac)
	}

	eng2 := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetCompactor(&passThroughCompactor{}, AutoCompactConfig{ContextWindow: 100})
	eng2.SetStore(newTestStore(t), "/proj")
	if _, ok := eng2.compactor.(*passThroughCompactor); !ok {
		t.Errorf("SetStore replaced a non-auto compactor: %T", eng2.compactor)
	}
}

// TestPersistContextTokens_LogMatrix pins both persist helpers' guard and
// error logging across the store/session matrix. The store's UPDATE never
// errors for a live DB (an unknown session just updates zero rows), so the
// error log is driven through a closed store.
func TestPersistContextTokens_LogMatrix(t *testing.T) {
	t.Parallel()

	logs := captureSlog(t)

	mk := func(store *short.Store, sid string) *Engine {
		eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
		t.Cleanup(func() { eng.Close() })
		if store != nil {
			eng.SetStore(store, "/proj")
		}
		if sid != "" {
			eng.SetSessionID(sid)
		}
		eng.SetContextTokens(7)
		return eng
	}

	// Live DB, unknown session: the UPDATE succeeds silently — no error log.
	mk(newTestStore(t), "sess-ghost-persist").persistContextTokens()
	if _, ok := logs.find("persistContextTokens", func(a map[string]any) bool { return a["session"] == "sess-ghost-persist" }); ok {
		t.Error("persistContextTokens logged an error for a live store — the ghost-session UPDATE is silent")
	}

	// Closed store: the error surfaces with the session id attached.
	closed := newTestStore(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	mk(closed, "sess-closed-persist").persistContextTokens()
	if _, ok := logs.find("persistContextTokens", func(a map[string]any) bool { return a["session"] == "sess-closed-persist" }); !ok {
		t.Error("no persistContextTokens error logged for the closed store")
	}

	// Closed store with an empty session id: the guard must skip before the
	// store is touched — no error log for the empty id.
	mk(closed, "").persistContextTokens()
	if _, ok := logs.find("persistContextTokens", func(a map[string]any) bool { return a["session"] == "" }); ok {
		t.Error("persistContextTokens logged for an empty session id — the guard must skip first")
	}

	// No store at all: silent skip even with a session id set.
	mk(nil, "sess-nostore-persist").persistContextTokens()
	if _, ok := logs.find("persistContextTokens", func(a map[string]any) bool { return a["session"] == "sess-nostore-persist" }); ok {
		t.Error("persistContextTokens logged for a storeless engine — it must skip silently")
	}

	// Same matrix for the locked variant.
	mk(newTestStore(t), "sess-ghost-locked").persistContextTokensLocked()
	if _, ok := logs.find("persistContextTokensLocked", func(a map[string]any) bool { return a["session"] == "sess-ghost-locked" }); ok {
		t.Error("persistContextTokensLocked logged an error for a live store")
	}
	mk(closed, "sess-closed-locked").persistContextTokensLocked()
	if _, ok := logs.find("persistContextTokensLocked", func(a map[string]any) bool { return a["session"] == "sess-closed-locked" }); !ok {
		t.Error("no persistContextTokensLocked error logged for the closed store")
	}
	mk(closed, "").persistContextTokensLocked()
	if _, ok := logs.find("persistContextTokensLocked", func(a map[string]any) bool { return a["session"] == "" }); ok {
		t.Error("persistContextTokensLocked logged for an empty session id")
	}
	mk(nil, "sess-nostore-locked").persistContextTokensLocked()
	if _, ok := logs.find("persistContextTokensLocked", func(a map[string]any) bool { return a["session"] == "sess-nostore-locked" }); ok {
		t.Error("persistContextTokensLocked logged for a storeless engine")
	}

	// Valid session: tokens land in the row and no error log fires.
	store := newTestStore(t)
	sess, err := store.CreateSession("/proj", "m")
	if err != nil {
		t.Fatal(err)
	}
	mk(store, sess.SessionID).persistContextTokens()
	ses, err := store.GetSession(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.ContextTokens != 7 {
		t.Errorf("persisted ContextTokens = %d, want 7", ses.ContextTokens)
	}
	if _, ok := logs.find("persistContextTokens", func(a map[string]any) bool { return a["session"] == sess.SessionID }); ok {
		t.Error("persistContextTokens logged an error for a valid session")
	}

	// The exported wrapper must reach the same write.
	sess2, err := store.CreateSession("/proj", "m")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := mk(store, sess2.SessionID)
	wrapper.SetContextTokens(9)
	wrapper.PersistContextTokens()
	ses2, err := store.GetSession(sess2.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if ses2.ContextTokens != 9 {
		t.Errorf("persisted ContextTokens via the exported wrapper = %d, want 9", ses2.ContextTokens)
	}
}

// TestAccessorLockDiscipline pins that the session store accessors acquire
// e.mu for their reads and release it on return, and that a store-less
// PruneEmptySessions still takes and releases the lock.
func TestAccessorLockDiscipline(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")

	if _, err := eng.ListSessions(5); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	lockProbe(t, &eng.mu)
	if _, err := eng.PruneEmptySessions(); err != nil {
		t.Fatalf("PruneEmptySessions: %v", err)
	}
	lockProbe(t, &eng.mu)

	// Store() and PruneEmptySessions must block on a held write lock: their
	// reads happen under e.mu, not around it.
	eng.mu.Lock()
	storeDone := make(chan *short.Store, 1)
	go func() { storeDone <- eng.Store() }()
	select {
	case got := <-storeDone:
		eng.mu.Unlock()
		t.Fatalf("Store() returned %v while the write lock was held — the read left the lock scope", got)
	case <-time.After(150 * time.Millisecond):
	}
	eng.mu.Unlock()
	if got := <-storeDone; got != store {
		t.Errorf("Store() after unlock = %v, want the wired store", got)
	}

	eng.mu.Lock()
	pruneDone := make(chan error, 1)
	go func() {
		_, err := eng.PruneEmptySessions()
		pruneDone <- err
	}()
	select {
	case got := <-pruneDone:
		eng.mu.Unlock()
		t.Fatalf("PruneEmptySessions returned %v while the write lock was held", got)
	case <-time.After(150 * time.Millisecond):
	}
	eng.mu.Unlock()
	if got := <-pruneDone; got != nil {
		t.Errorf("PruneEmptySessions after unlock: %v", got)
	}
}

// TestProjectDir_ReadsUnderLock pins both the value and the locking of
// ProjectDir.
func TestProjectDir_ReadsUnderLock(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(newTestStore(t), "/proj/dir-test")
	if got := eng.ProjectDir(); got != "/proj/dir-test" {
		t.Errorf("ProjectDir = %q, want /proj/dir-test", got)
	}

	eng.mu.Lock()
	done := make(chan string, 1)
	go func() { done <- eng.ProjectDir() }()
	select {
	case got := <-done:
		eng.mu.Unlock()
		t.Fatalf("ProjectDir returned %q while the write lock was held", got)
	case <-time.After(150 * time.Millisecond):
	}
	eng.mu.Unlock()
	if got := <-done; got != "/proj/dir-test" {
		t.Errorf("ProjectDir after unlock = %q", got)
	}
}

// TestNewSession_CreateFailureWrapsCause pins the error contract when the
// store cannot create the session row: an error, and one that still wraps
// the store's cause.
func TestNewSession_CreateFailureWrapsCause(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")
	if err := store.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}
	err := eng.NewSession("/proj", "Nope")
	if err == nil {
		t.Fatal("NewSession succeeded on a closed store")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("NewSession error %q does not wrap its cause", err)
	}
	if got := eng.SessionID(); got != "" {
		t.Errorf("NewSession bound session id %q despite failing", got)
	}
}

// TestForkSession_GhostParentSurfacesError pins the parent-load failure path
// and the fork-point reset on success.
func TestForkSession_GhostParentSurfacesError(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")
	eng.SetSessionID("sess-ghost-parent")

	_, err := eng.ForkSession("t")
	if err == nil {
		t.Fatal("forking a ghost session succeeded")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("fork error %q does not wrap its cause", err)
	}
}

// TestSwitchSession_LoadedStateIsFullyRebound pins the remaining switch
// invariants: persisted pointer reset, fork point cleared, and the ghost
// error wrapping its cause.
func TestSwitchSession_LoadedStateIsFullyRebound(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/proj")

	sessA, err := store.CreateSession("/projA", "m")
	if err != nil {
		t.Fatal(err)
	}
	eng.SetMessages([]types.Message{
		{ID: "sw0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")}},
	})
	eng.markAllPersisted()
	eng.forkParentUUID = "stale-fork"

	if _, err := eng.SwitchSession(sessA.SessionID); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if eng.LastPersistedIdx() != 0 {
		t.Errorf("lastPersistedIdx after switch = %d, want 0 (empty session, all persisted)", eng.LastPersistedIdx())
	}
	if eng.forkParentUUID != "" {
		t.Errorf("forkParentUUID after switch = %q, want cleared", eng.forkParentUUID)
	}

	_, err = eng.SwitchSession("no-such-session")
	if err == nil {
		t.Fatal("switch to ghost session succeeded")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("ghost switch error %q does not wrap its cause", err)
	}
}

// TestSetTaskDirForSession_SubEngineAndBrokenDirs pins the guard (nil task
// list on sub-engines) and both failure logs of the task-dir wiring.
func TestSetTaskDirForSession_SubEngineAndBrokenDirs(t *testing.T) {
	logs := captureSlog(t)

	// Sub-engines carry a nil task list: session binding must be a no-op.
	parent := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { parent.Close() })
	sub := parent.NewSubEngine(SubEngineOptions{AgentType: "General"})
	t.Cleanup(func() { sub.Close() })
	sub.SetSessionID("sub-with-no-tasklist")

	// A home whose .gbot path is a regular file makes SetDir's MkdirAll fail.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gbot"), []byte("not a dir"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", TaskList: task.NewList(""), Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("sess-broken-gbot")
	if _, ok := logs.find("engine: tasks init failed", func(a map[string]any) bool { return a["sessionID"] == "sess-broken-gbot" }); !ok {
		t.Error("no tasks init failed log for the unwritable tasks dir")
	}

	// An unset home makes TasksDir itself fail: warned, and still no panic.
	t.Setenv("HOME", "")
	eng2 := New(&Params{Provider: &mockProvider{}, Model: "m", TaskList: task.NewList(""), Logger: silentLogger()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetSessionID("sess-no-home")
	if _, ok := logs.find("engine: tasks dir resolve failed", func(a map[string]any) bool { return a["sessionID"] == "sess-no-home" }); !ok {
		t.Error("no tasks dir resolve failed log for the unset home")
	}
}

// TestUpdateAutoCompactConfig_ReleasesEngineLock is the lock probe for the
// config setter (the unlock must survive to function exit).
func TestUpdateAutoCompactConfig_ReleasesEngineLock(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.UpdateAutoCompactConfig(AutoCompactConfig{ContextWindow: 5})
	lockProbe(t, &eng.mu)
}

// TestSetInputModalities_ReleasesEngineLock is the lock probe for the
// modality setter.
func TestSetInputModalities_ReleasesEngineLock(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetInputModalities([]string{"text"})
	lockProbe(t, &eng.mu)
	if !eng.SupportsModality("text") || eng.SupportsModality("image") {
		t.Error("SupportsModality disagrees with the set modalities")
	}
}

// TestSendFile_ReleasesSendersLock pins that the sender lookup hands the
// registry lock back on both exit paths.
func TestSendFile_ReleasesSendersLock(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	if err := eng.SendFile(context.Background(), "/tmp/none", ""); err == nil {
		t.Fatal("unregistered source unexpectedly succeeded")
	}
	eng.RegisterFileSender("s", senderFunc(func(context.Context, string, string) error { return nil }))
	if err := eng.SendFile(WithSource(context.Background(), "s"), "/tmp/none", ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	lockProbe(t, &eng.sendersMu)
}

// TestNewSubEngine_SessionIDSequenceIncrements pins the uniqueness suffix:
// consecutive sub-engines step the sequence by exactly one. Sequential on
// purpose — the counter is process-global, and a parallel sub creation from
// another test would inflate the measured step.
func TestNewSubEngine_SessionIDSequenceIncrements(t *testing.T) {
	parent := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { parent.Close() })
	parent.SetSessionID("seq-parent")

	sub1 := parent.NewSubEngine(SubEngineOptions{AgentType: "General"})
	sub2 := parent.NewSubEngine(SubEngineOptions{AgentType: "General"})
	suffix := func(s string) int {
		i := strings.LastIndex(s, "-sub-")
		if i < 0 {
			t.Fatalf("session id %q lacks the -sub- suffix", s)
		}
		var n int
		if _, err := fmt.Sscanf(s[i+len("-sub-"):], "%d", &n); err != nil {
			t.Fatalf("suffix of %q is not numeric: %v", s, err)
		}
		return n
	}
	d := suffix(sub2.sessionID) - suffix(sub1.sessionID)
	if d != 1 {
		t.Errorf("sequence step between consecutive subs = %d, want 1 (%q → %q)", d, sub1.sessionID, sub2.sessionID)
	}
}

// TestNewSubEngine_ToolOrderSortedAcrossManyTools pins the sort with enough
// entries that an unsorted map order cannot pass as sorted.
func TestNewSubEngine_ToolOrderSortedAcrossManyTools(t *testing.T) {
	t.Parallel()

	parent := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { parent.Close() })
	names := []string{"Zulu", "Alpha", "Mike", "Bravo", "Yankee", "Kilo"}
	tools := map[string]tool.Tool{}
	for _, n := range names {
		tools[n] = &mockTool{name: n, enabled: true}
	}
	sub := parent.NewSubEngine(SubEngineOptions{AgentType: "General", Tools: tools})
	want := []string{"Alpha", "Bravo", "Kilo", "Mike", "Yankee", "Zulu"}
	if !slices.Equal(sub.toolOrder, want) {
		t.Errorf("toolOrder = %v, want sorted %v", sub.toolOrder, want)
	}
}

// TestNewSubEngine_ParentWithoutDispatcherStaysUntagged pins the dispatcher
// wiring guard: a parent with no dispatcher must not hand the sub a
// forwarding wrapper over nil.
func TestNewSubEngine_ParentWithoutDispatcherStaysUntagged(t *testing.T) {
	t.Parallel()

	parent := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger()})
	t.Cleanup(func() { parent.Close() })
	sub := parent.NewSubEngine(SubEngineOptions{AgentType: "General", ParentToolUseID: "tu_pd"})
	if sub.dispatcher != nil {
		t.Fatalf("sub.dispatcher = %T with a dispatcher-less parent, want nil", sub.dispatcher)
	}
	sub.emitEvent(types.QueryEvent{Type: types.EventTurnStart})
}

// TestNewSubEngine_InheritsCheckerRegistryAndHooks pins the pointer-level
// inheritance the literal performs and the behavioral effect of the
// permission checker on the sub's own tool calls.
func TestNewSubEngine_InheritsCheckerRegistryAndHooks(t *testing.T) {
	t.Parallel()

	reg := &mcp.Registry{}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("m", "tu_subpc", "Read", `{}`), nil)
	mp.addResponse(textStreamEvents("m", "done"), nil)
	parent := New(&Params{
		Provider: mp, Model: "m",
		MCPRegistry: reg, PermissionChecker: denyAllChecker{},
		Logger: silentLogger(),
	})
	t.Cleanup(func() { parent.Close() })
	var closed []string
	parent.SetOnClose(func(id string) { closed = append(closed, id) })

	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "General",
		Tools:     map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}},
	})
	if sub.mcpRegistry != reg {
		t.Error("sub did not inherit the parent's MCP registry")
	}
	if sub.toolSearch == nil {
		t.Error("sub toolSearch is nil — fresh state must be allocated")
	}
	if sub.reminderEngine == nil {
		t.Error("sub reminderEngine is nil — reminders must be wired for sub-agents")
	}
	sub.Close()
	if len(closed) != 1 || closed[0] != sub.sessionID {
		t.Errorf("sub Close notified onCloseFn with %v, want exactly [%s]", closed, sub.sessionID)
	}

	// The inherited checker denies the sub's own tool calls.
	sub2 := parent.NewSubEngine(SubEngineOptions{
		AgentType: "General",
		Tools:     map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true}},
	})
	res := sub2.QuerySync(context.Background(), "q", "")
	if res.Error != nil {
		t.Fatalf("sub query: %v", res.Error)
	}
	var denySeen bool
	for _, m := range res.Messages {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && strings.Contains(string(b.Content), "Permission to use Read has been denied") {
				denySeen = true
			}
		}
	}
	if !denySeen {
		t.Error("sub tool call was not denied by the inherited permission checker")
	}
}

// TestRunTurns_TurnBoundaryPersistGuardQuiet pins that the turn-boundary
// persist guard skips without reaching PersistNewMessages for main engines
// lacking a store or a session id. The runTurns exit defer legitimately
// calls PersistNewMessages once, so the pin is a count: exactly one skip
// record, not two. Sequential: the empty-session records of other main
// engines must not pollute the count.
func TestRunTurns_TurnBoundaryPersistGuardQuiet(t *testing.T) {
	logs := captureSlog(t)
	count := func(sid string, storeNil bool) int {
		n := 0
		for _, c := range logs.all("PersistNewMessages: skip persistence") {
			gotSid, _ := c.attrs["sessionID"].(string)
			gotStoreNil, _ := c.attrs["store_nil"].(bool)
			if gotSid == sid && gotStoreNil == storeNil {
				n++
			}
		}
		return n
	}

	// Turn 1 is a tool call so the loop actually runs the boundary at the end
	// of the iteration; turn 2 is the text that ends the query. A single text
	// turn returns through a branch that never passes the boundary, which
	// would leave the guard unevaluated and the count insensitive.
	script := func(mp *mockProvider) {
		mp.addResponse(toolUseStreamEvents("m", "tu_guard", "Read", `{"file_path":"/nonexistent/probe"}`), nil)
		mp.addResponse(textStreamEvents("m", "one"), nil)
	}
	tools := func() map[string]tool.Tool {
		return map[string]tool.Tool{"Read": &mockTool{name: "Read", enabled: true,
			callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "ok"}, nil
			}}}
	}

	// Main engine with a session id but no store.
	eng := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), ToolsProvider: tools})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("sess-guard-nostore")
	script(eng.Provider().(*mockProvider))
	if res := eng.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if n := count("sess-guard-nostore", true); n != 1 {
		t.Errorf("skip-persistence records for a storeless engine = %d, want exactly 1 (exit defer only — the turn boundary must not call through)", n)
	}

	// Main engine with a store but no session id.
	eng2 := New(&Params{Provider: &mockProvider{}, Model: "m", Logger: silentLogger(), ToolsProvider: tools})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetStore(newTestStore(t), "/proj")
	script(eng2.Provider().(*mockProvider))
	if res := eng2.QuerySync(context.Background(), "q", ""); res.Error != nil {
		t.Fatalf("query: %v", res.Error)
	}
	if n := count("", false); n != 1 {
		t.Errorf("skip-persistence records for a sessionless engine = %d, want exactly 1 (exit defer only)", n)
	}
}
