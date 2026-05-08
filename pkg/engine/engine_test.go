package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
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
	if event.Type == types.EventQueryEnd {
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
	mu        sync.Mutex
	responses []mockResponse
	index     int
}

type mockResponse struct {
	events []llm.StreamEvent
	err    error
}

func (m *mockProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: []types.ContentBlock{types.NewTextBlock("Summary of conversation.")},
	}, nil
}

func (m *mockProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.index >= len(m.responses) {
		return nil, errors.New("no more mock responses")
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
	}()
	return ch, nil
}

func (m *mockProvider) addResponse(events []llm.StreamEvent, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, mockResponse{events: events, err: err})
}

// ---------------------------------------------------------------------------
// Mock Tool
// ---------------------------------------------------------------------------

type mockTool struct {
	name    string
	callFn  func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error)
	descFn  func(json.RawMessage) (string, error)
	enabled bool
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
func (t *mockTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *mockTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *mockTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *mockTool) IsEnabled() bool                           { return t.enabled }
func (t *mockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *mockTool) Prompt() string                            { return "" }
func (t *mockTool) RenderResult(any) string                   { return "" }

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
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
	})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "Say hello", nil)
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
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "Read the file", nil)
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

// TestQuery_ToolResultContentIsString verifies that tool_result content is
// serialized as a JSON string (not a raw JSON object) in the API message.
// The Anthropic API expects tool_result.content to be a string, so
// {"files":["a.go"]} must become "\"{\\\"files\\\":[\\\"a.go\\\"]}\"".
// If content is a raw object, the LLM cannot parse tool output.
func TestQuery_ToolResultContentIsString(t *testing.T) {
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
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "List Go files", nil)
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
			// the "content" field is a JSON string (starts with "),
			// not a raw JSON object (starts with {).
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

			// The content MUST be a JSON string (starts and ends with "),
			// not a raw JSON object (starts with {).
			if contentField[0] != '"' {
				t.Errorf("tool_result.content should be a JSON string, got raw object: %s", contentField)
			}

			// Additionally: the string value should contain the tool output
			var contentStr string
			if err := json.Unmarshal(raw["content"], &contentStr); err != nil {
				t.Fatalf("content is not a valid JSON string: %v", err)
			}
			if !strings.Contains(contentStr, "files") {
				t.Errorf("content string should contain 'files', got: %s", contentStr)
			}
			if !strings.Contains(contentStr, "cmd/gbot/main.go") {
				t.Errorf("content string should contain file path, got: %s", contentStr)
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
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Dispatcher: ec,
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		<-blockStarted
		cancel()
	}()

	eng.QuerySync(ctx, "run the tool", nil)

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

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel BEFORE calling Query to deterministically trigger context cancellation
	cancel()

	result := eng.QuerySync(ctx, "test query", nil)
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

func TestQuery_BlockingLimit(t *testing.T) {
	t.Parallel()

	// Verify that the blocking limit refuses API calls when context exceeds threshold.
	// Blocking limit = contextWindow - min(maxTokens, 20000) - 3000
	// With ContextWindow=1000, maxTokens=16000: limit = 1000 - 16000 - 3000 = negative (skip)
	// With ContextWindow=50000, maxTokens=16000: limit = 50000 - 16000 - 3000 = 31000
	// We pre-load messages that estimate to > 31000 tokens and verify blocking.

	mp := &mockProvider{}

	mt := &mockTool{
		name:    "my_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "done"}, nil
		},
	}

	eng := New(&Params{
		Provider:  mp,
		Tools:     []tool.Tool{mt},
		Model:     "test-model",
		MaxTokens: 16000,
		Logger:    slog.Default(),
	})
	// Set auto-compact config without compactor so auto-compact won't fire.
	// Only the blocking limit should guard against oversized context.
	eng.UpdateAutoCompactConfig(AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Pre-load enough messages to exceed blocking limit (31000 tokens).
	// Each message has ~4000 tokens (16000 chars / 4).
	bigText := strings.Repeat("x", 16000)
	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(bigText)},
		}))
	}
	// ~32000 estimated tokens > 31000 blocking limit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", nil)
	if result.Error == nil {
		t.Fatal("expected prompt too long error")
	}
	if !strings.Contains(result.Error.Error(), "Prompt is too long") {
		t.Errorf("Error should contain 'Prompt is too long', got: %v", result.Error)
	}

	// Negative blocking limit: when contextWindow < maxTokens + 3000,
	// the formula produces a negative blockingLimit which is skipped.
	// The query should proceed normally without blocking.
	t.Run("NegativeLimit_SkipsBlocking", func(t *testing.T) {
		mp := &mockProvider{}
		events := []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 10}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: "ok"}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
			{Type: "message_stop"},
		}
		mp.addResponse(events, nil)

		eng := New(&Params{
			Provider:  mp,
			Model:     "test-model",
			MaxTokens: 16000,
			Logger:    slog.Default(),
		})
		// ContextWindow=1000, maxTokens=16000 -> blockingLimit = 1000 - 16000 - 3000 = -18000 (skipped)
		eng.UpdateAutoCompactConfig(AutoCompactConfig{
			ContextWindow:          1000,
			MaxConsecutiveFailures: 3,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result := eng.QuerySync(ctx, "hello", nil)
		if result.Error != nil {
			t.Fatalf("negative blockingLimit should be skipped, got: %v", result.Error)
		}
	})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "use unknown tool", nil)
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
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "call failing tool", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Error.Error(), "bad request") {
		t.Errorf("error should contain 'bad request', got: %v", result.Error)
	}

}

func TestQuery_StreamError_RetryableThenSuccess(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First call: retryable error (429) — now correctly detected via errors.As
	mp.addResponse(nil, &llm.APIError{
		Type:      "rate_limit_error",
		Message:   "rate limited",
		Status:    429,
		Retryable: true,
	})
	// Second call: success after retry
	mp.addResponse(textStreamEvents("test-model", "Recovered!"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("expected no error after retry, got: %v", result.Error)
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
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	// Pre-populate toolSearch state (simulating discovered tools from a prior session).
	eng.toolSearch.DiscoverTools([]string{"TaskList", "TaskUpdate"})
	if !eng.toolSearch.IsDiscovered("TaskList") {
		t.Fatal("precondition: TaskList should be discovered")
	}

	eng.Reset()

	// After reset, toolSearch state should be cleared.
	if eng.toolSearch.IsDiscovered("TaskList") {
		t.Error("expected TaskList to NOT be discovered after Reset")
	}
	if eng.toolSearch.IsDiscovered("TaskUpdate") {
		t.Error("expected TaskUpdate to NOT be discovered after Reset")
	}
}

func TestMessages(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

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

func TestSetMessages_RestoresToolSearchState(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	// Simulate a transcript with a compact boundary carrying preCompactDiscoveredTools.
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: `{"subtype":"compact_boundary","preCompactDiscoveredTools":["TaskList","TaskUpdate"]}`},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	}

	eng.SetMessages(msgs)

	// ToolSearch state should be restored from the compact boundary.
	if !eng.toolSearch.IsDiscovered("TaskList") {
		t.Error("expected TaskList to be discovered after SetMessages with compact boundary")
	}
	if !eng.toolSearch.IsDiscovered("TaskUpdate") {
		t.Error("expected TaskUpdate to be discovered after SetMessages with compact boundary")
	}
	if eng.toolSearch.IsDiscovered("TaskCreate") {
		t.Error("expected TaskCreate to NOT be discovered")
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
		Tools:      []tool.Tool{toolA, toolB},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "call both tools", nil)
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
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "greet me", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	textEvents := ec.FindEvents(types.EventTextDelta)
	if len(textEvents) != 2 {
		t.Fatalf("expected 2 text deltas, got %d", len(textEvents))
	}
	if textEvents[0].Text != "Hello " || textEvents[1].Text != "world!" {
		t.Errorf("unexpected deltas: %v %v", textEvents[0].Text, textEvents[1].Text)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "ping", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do 3 rounds", nil)
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
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error == nil {
		t.Fatal("expected error from stream event error")
	}
	if !strings.Contains(result.Error.Error(), "stream interrupted") {
		t.Errorf("error should contain 'stream interrupted', got: %v", result.Error)
	}
}

// TestQuery_RetryableStreamError tests handleStreamError's Continue=true path.
// When a retryable error occurs mid-stream (returned via event.Error, NOT wrapped),
// handleStreamError should return Continue=true and the loop retries.
func TestQuery_RetryableStreamError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First response: retryable error mid-stream (unwrapped via event.Error)
	retryableEvents := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 5}}},
		{Error: &llm.APIError{Message: "overloaded", Status: 529, Retryable: true}},
	}
	mp.addResponse(retryableEvents, nil)
	// Second response: success after retry
	mp.addResponse(textStreamEvents("test-model", "Recovered!"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first turn completes — queryLoop catches it at top of next iteration
	go func() {
		time.Sleep(10 * time.Millisecond) // REAL-TIME: wait for first turn to complete before canceling
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", nil)
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
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: h,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test hub events", nil)
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
// within each round. Previous bug: turn_end was emitted right after callLLM()
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
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: h,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test ordering", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test interface", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test nil hub", nil)
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

	// Turn 1
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	result1 := eng.QuerySync(ctx1, "My name is Xiaoming", nil)
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
	result2 := eng.QuerySync(ctx2, "What is my name?", nil)
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

func TestEngine_EnqueueNotification(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	// Enqueue a notification from another goroutine (simulates background task callback)
	eng.EnqueueNotification(types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<task-notification><task-id>bg-1</task-id></task-notification>"),
		},
		Timestamp: time.Now(), // REAL-TIME: needed for message timestamp in test
	})

	msgs := eng.Messages()
	// Notification should NOT appear in messages yet — it's queued, not appended
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages (notification is queued, not appended), got %d", len(msgs))
	}
}

func TestQuery_NotificationsDrained(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First call returns tool_use → loop continues
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "my_tool", `{}`), nil)
	// Second call returns text → loop ends
	mp.addResponse(textStreamEvents("test-model", "Notification seen!"), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	// Enqueue notification BEFORE starting query — it should be drained
	// at the start of the first queryLoop iteration.
	eng.EnqueueNotification(types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<task-notification><task-id>bg-1</task-id></task-notification>"),
		},
		Timestamp: time.Now(), // REAL-TIME: needed for message timestamp in test
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// The notification should have been injected as a message
	var notificationMsgSeen bool
	for _, evt := range ec.FindEvents(types.EventQueryStart) {
		if evt.Message != nil {
			for _, block := range evt.Message.Content {
				if strings.HasPrefix(block.Text, "<task-notification>") {
					notificationMsgSeen = true
				}
			}
		}
	}
	if !notificationMsgSeen {
		t.Error("expected notification message to be emitted as EventQueryStart")
	}

	// Verify the notification is in the final message history
	msgs := result.Messages
	found := false
	for _, msg := range msgs {
		if msg.Role == types.RoleUser {
			for _, block := range msg.Content {
				if strings.HasPrefix(block.Text, "<task-notification>") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("notification should be in the final message history")
	}
}

func TestEngine_EnqueueNotification_Concurrent(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	var wg sync.WaitGroup
	notificationCount := 100
	for i := range notificationCount {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			eng.EnqueueNotification(types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewTextBlock(fmt.Sprintf("notification-%d", n)),
				},
				Timestamp: time.Now(), // REAL-TIME: needed for message timestamp in test
			})
		}(i)
	}
	wg.Wait()

	// Count enqueued notifications by triggering a query and checking messages
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Count how many notification messages were injected
	notificationMsgCount := 0
	for _, msg := range result.Messages {
		if msg.Role == types.RoleUser {
			for _, block := range msg.Content {
				if strings.HasPrefix(block.Text, "notification-") {
					notificationMsgCount++
				}
			}
		}
	}

	// All 100 notifications should have been enqueued and drained
	if notificationMsgCount != notificationCount {
		t.Errorf("expected %d notifications to be enqueued and drained, got %d", notificationCount, notificationMsgCount)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eng.QuerySync(ctx, "test", nil)

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)

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
// EnqueueNotification + ProcessNotifications tests
// ---------------------------------------------------------------------------

func TestEnqueueNotification_DispatchesHubEvent(t *testing.T) {
	t.Parallel()

	h := hub.NewHub()
	handler := &hubMockHandler{}
	h.Subscribe(handler)

	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: h,
	})

	eng.EnqueueNotification(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("bg task done")},
	})

	events := handler.Events()
	var found bool
	for _, evt := range events {
		if evt.Type == types.EventNotificationPending {
			found = true
		}
	}
	if !found {
		t.Error("EnqueueNotification should dispatch EventNotificationPending via Hub")
	}
}

func TestEnqueueNotification_NoDispatcher_NoPanic(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	// Should not panic when dispatcher is nil
	eng.EnqueueNotification(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("bg task done")},
	})
}

func TestProcessNotifications_EmptyQueue(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := eng.ProcessNotificationsSync(ctx, nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Error != nil {
		t.Errorf("unexpected result: %v", result.Error)
	}
}

func TestProcessNotifications_DrainsAndRunsTurns(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// LLM sees the notification and responds with text (no tool_use)
	mp.addResponse(textStreamEvents("test-model", "Background task completed."), nil)

	// Capture events via eventCollector
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: ec,
	})

	// Enqueue a notification (no Hub event since dispatcher is nil)
	eng.EnqueueNotification(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("<notification>bg-1 completed</notification>")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.ProcessNotificationsSync(ctx, nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Verify notification was injected: should have at least query_start + turn_start + text_delta + turn_end + query_end
	gotQueryStart := len(ec.FindEvents(types.EventQueryStart)) > 0
	gotTurnStart := len(ec.FindEvents(types.EventTurnStart)) > 0
	gotTextDelta := len(ec.FindEvents(types.EventTextDelta)) > 0
	gotQueryEnd := len(ec.FindEvents(types.EventQueryEnd)) > 0
	if !gotQueryStart {
		t.Error("expected EventQueryStart for notification message")
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

	// Verify notification is in message history
	msgs := result.Messages
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (notification + assistant), got %d", len(msgs))
	}
	// First message should be the notification
	firstText := ""
	for _, blk := range msgs[0].Content {
		if blk.Type == types.ContentTypeText {
			firstText = blk.Text
			break
		}
	}
	if !strings.Contains(firstText, "bg-1 completed") {
		t.Errorf("first message should contain notification, got: %q", firstText)
	}
}

func TestProcessNotifications_ContextCancelled(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// LLM never responds — we cancel the context
	mp.addResponse(nil, context.Canceled)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	eng.EnqueueNotification(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("notification")},
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after a short delay to let the goroutine start
	go func() {
		runtime.Gosched()
		cancel()
	}()

	result := eng.ProcessNotificationsSync(ctx, nil)
	if result.Error == nil {
		t.Error("expected error from cancelled context")
	}
	if result.Error != nil && !strings.Contains(result.Error.Error(), "context canceled") {
		t.Errorf("error should mention 'context canceled', got: %v", result.Error)
	}
	// Context cancellation from provider error path classifies as model_error,
	// not aborted_streaming (which only fires from engine's own <-ctx.Done() check)
	if result.Error == nil {
		t.Errorf("unexpected result: %v", result.Error)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		// No content_block_delta — empty text block
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
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
		Tools:      []tool.Tool{toolRead, toolBash},
		Model:      "test-model",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "read and ls", nil)
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
		Tools:    []tool.Tool{toolRead, toolBash, toolGrep},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "parallel ops", nil)
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
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "/roast", nil)
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
// Root cause of "11 tools vs 14 tools" bug: main.go used to register
// Agent/TaskOutput/TaskStop after New(). Fix: register all tools first.
func TestAllTools_AllToolsRegisteredBeforeEngine(t *testing.T) {
	t.Parallel()

	// Correct registration order: ALL tools before New()
	reg := tool.NewRegistry()
	for _, name := range []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep"} {
		reg.MustRegister(&mockTool{name: name, enabled: true})
	}
	reg.MustRegister(&mockTool{name: "Skill", enabled: true})
	reg.MustRegister(&mockTool{name: "Agent", enabled: true})
	reg.MustRegister(&mockTool{name: "JobOutput", enabled: true})
	reg.MustRegister(&mockTool{name: "JobStop", enabled: true})

	// New() — ToolsProvider snapshots all 10 tools
	eng := New(&Params{
		Provider:      &mockProvider{},
		ToolsProvider: reg.ToolMapFn(),
		Model:         "test-model",
	})

	got := len(eng.AllTools())
	if got != 10 {
		t.Errorf("AllTools() = %d tools, want 10. "+
			"All tools registered before New() but count is wrong.", got)
	}
}

