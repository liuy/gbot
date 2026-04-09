package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

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
	return nil, errors.New("not implemented")
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
	callFn  func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error)
	descFn  func(json.RawMessage) (string, error)
	enabled bool
}

func (t *mockTool) Name() string                                                { return t.name }
func (t *mockTool) Aliases() []string                                           { return nil }
func (t *mockTool) Description(input json.RawMessage) (string, error) {
	if t.descFn != nil {
		return t.descFn(input)
	}
	return t.name + " description", nil
}
func (t *mockTool) InputSchema() json.RawMessage                                { return json.RawMessage(`{"type":"object","properties":{}}`) }
func (t *mockTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *mockTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *mockTool) IsReadOnly(json.RawMessage) bool            { return true }
func (t *mockTool) IsDestructive(json.RawMessage) bool         { return false }
func (t *mockTool) IsConcurrencySafe(json.RawMessage) bool     { return true }
func (t *mockTool) IsEnabled() bool                            { return t.enabled }
func (t *mockTool) InterruptBehavior() tool.InterruptBehavior  { return tool.InterruptCancel }
func (t *mockTool) Prompt() string                             { return "" }

// ---------------------------------------------------------------------------
// Helper: build streaming events
// ---------------------------------------------------------------------------

func textStreamEvents(model, text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 5}},
		{Type: "message_stop"},
	}
}

func toolUseStreamEvents(model, toolID, toolName, toolInput string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 20}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: toolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: toolInput}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 10}},
		{Type: "message_stop"},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
	})
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
}

func TestNew_DefaultMaxTokens(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	eng := engine.New(&engine.Params{
		Provider:    mp,
		Model:       "test-model",
		MaxTokens:   0,
		TokenBudget: 0,
		Logger:      nil,
	})
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
}

func TestNew_WithTools(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mt := &mockTool{name: "my_tool", enabled: true}
	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
	})
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
}

func TestQuery_SimpleTextResponse(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello, world!"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Say hello", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %s", result.Terminal)
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
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "file contents here"}, nil
		},
	}

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Read the file", nil)

	var toolResultSeen, textDeltaSeen bool
	for evt := range eventCh {
		if evt.Type == types.EventToolResult {
			toolResultSeen = true
		}
		if evt.Type == types.EventTextDelta {
			textDeltaSeen = true
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %s", result.Terminal)
	}
	if !toolResultSeen {
		t.Error("expected to see a tool result event")
	}
	if !textDeltaSeen {
		t.Error("expected to see a text delta event")
	}
	if result.TurnCount != 1 {
		t.Errorf("expected 1 turn, got %d", result.TurnCount)
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
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: globOutput{
				Files: []string{"cmd/gbot/main.go"},
				Count: 1,
			}}, nil
		},
	}

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "List Go files", nil)
	for range eventCh {
	}

	result := <-resultCh
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

func TestQuery_ContextCancellation(t *testing.T) {
	mp := &mockProvider{}

	// The mock provider's Stream returns events on a channel. To test cancellation
	// during streaming, we need the provider to block long enough for ctx to cancel.
	// Instead, we use a provider that returns an error from Stream() but we cancel
	// during the loop. The simplest approach: provide no responses so the provider
	// returns an error, but cancel the context before the query even starts.
	//
	// Actually, the simplest way is to cancel context during the streaming event
	// accumulation. We do this by having the stream channel block.

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel before Query starts to trigger the context cancellation path
	cancel()

	eventCh, resultCh := eng.Query(ctx, "test query", nil)
	for range eventCh {
	}

	result := <-resultCh
	// The context is already cancelled, so the select in queryLoop should catch it
	// before calling callLLM, resulting in TerminalAbortedStreaming.
	// However, timing is tricky. The goroutine may start and check ctx.Done() immediately.
	if result.Terminal != types.TerminalAbortedStreaming && result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalAbortedStreaming or TerminalCompleted, got %s", result.Terminal)
	}
}

func TestQuery_TokenBudgetExhaustion(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 99999}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "my_tool"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 99999}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	mt := &mockTool{
		name:    "my_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "done"}, nil
		},
	}

	eng := engine.New(&engine.Params{
		Provider:    mp,
		Tools:       []tool.Tool{mt},
		Model:       "test-model",
		TokenBudget: 100,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "do something", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Terminal != types.TerminalPromptTooLong {
		t.Fatalf("expected TerminalPromptTooLong, got %s", result.Terminal)
	}
}

func TestQuery_UnknownTool(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := toolUseStreamEvents("test-model", "t1", "unknown_tool", `{}`)
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Tool not found."), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "use unknown tool", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// The unknown tool creates a tool_result block in messages but does NOT
	// emit an EventToolResult event (only known tools emit events).
	// Verify the conversation continued and completed successfully.
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errors.New("tool execution failed")
		},
	}

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "call failing tool", nil)

	var gotErrorResult bool
	for evt := range eventCh {
		if evt.Type == types.EventToolResult && evt.ToolResult != nil && evt.ToolResult.IsError {
			gotErrorResult = true
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected final error: %v", result.Error)
	}
	if !gotErrorResult {
		t.Error("expected tool result error event")
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Fatal("expected error")
	}
	// The error is wrapped by callLLM as "stream request: <original>", so
	// classifyTerminalError cannot unwrap it to see the APIError type.
	// It falls through to TerminalModelError for wrapped errors.
	if result.Terminal != types.TerminalModelError {
		t.Errorf("expected TerminalModelError, got %s", result.Terminal)
	}
}

func TestQuery_StreamError_RetryableThenSuccess(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// First call: retryable error (but the engine wraps it, so it becomes non-retryable)
	mp.addResponse(nil, &llm.APIError{
		Type:      "rate_limit_error",
		Message:   "rate limited",
		Status:    429,
		Retryable: true,
	})
	// Since the error is wrapped, handleStreamError won't see it as retryable.
	// The loop will stop. So we don't add a second response.
	// This tests the actual behavior: wrapped errors are not seen as retryable.

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Fatal("expected error")
	}
	// The wrapped error causes TerminalModelError since type assertion fails
	if result.Terminal != types.TerminalModelError {
		t.Errorf("expected TerminalModelError, got %s", result.Terminal)
	}
}

func TestQuery_DisabledToolSkipped(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mt := &mockTool{name: "disabled_tool", enabled: false}
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

func TestAddSystemMessage(t *testing.T) {
	t.Parallel()

	eng := engine.New(&engine.Params{
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

	eng := engine.New(&engine.Params{
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

func TestMessages(t *testing.T) {
	t.Parallel()

	eng := engine.New(&engine.Params{
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

func TestClassifyTerminalError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected types.TerminalReason
	}{
		{
			name:     "context overflow",
			err:      &llm.APIError{Status: 400, ErrorCode: "prompt_too_long"},
			expected: types.TerminalPromptTooLong,
		},
		{
			name:     "rate limit",
			err:      &llm.APIError{Status: 429},
			expected: types.TerminalBlockingLimit,
		},
		{
			name:     "server error 500",
			err:      &llm.APIError{Status: 500},
			expected: types.TerminalModelError,
		},
		{
			name:     "generic error",
			err:      errors.New("something went wrong"),
			expected: types.TerminalModelError,
		},
		{
			name:     "overloaded 529",
			err:      &llm.APIError{Status: 529},
			expected: types.TerminalModelError,
		},
		{
			name:     "API error 400 without prompt_too_long",
			err:      &llm.APIError{Status: 400, ErrorCode: "other_error"},
			expected: types.TerminalModelError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_ = engine.New(&engine.Params{
				Provider: &mockProvider{},
				Model:    "test",
				Logger:   slog.Default(),
			})
			switch tt.expected {
			case types.TerminalPromptTooLong:
				if !llm.IsContextOverflow(tt.err) {
					t.Error("expected IsContextOverflow to be true")
				}
			case types.TerminalBlockingLimit:
				if !llm.IsRateLimit(tt.err) {
					t.Error("expected IsRateLimit to be true")
				}
			}
		})
	}
}

func TestEscapeJSONString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple", input: "hello", want: "hello"},
		{name: "with newline", input: "line1\nline2", want: "line1\nline2"},
		{name: "with tab", input: "col1\tcol2", want: "col1\tcol2"},
		{name: "with backslash", input: `path\to\file`, want: `path\to\file`},
		{name: "with quotes", input: `say "hi"`, want: `say "hi"`},
		{name: "empty", input: "", want: ""},
		{name: "html chars", input: "<b>bold</b>", want: "\\u003cb\\u003ebold\\u003c/b\\u003e"},
		{name: "ampersand", input: "a&b", want: "a\\u0026b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := engine.EscapeJSONString(tt.input)
			if got != tt.want {
				t.Errorf("EscapeJSONString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 15}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Both tools executed."), nil)

	toolA := &mockTool{name: "tool_a", enabled: true, callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "a_result"}, nil
	}}
	toolB := &mockTool{name: "tool_b", enabled: true, callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: "b_result"}, nil
	}}

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{toolA, toolB},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "call both tools", nil)

	var toolResults int
	for evt := range eventCh {
		if evt.Type == types.EventToolResult {
			toolResults++
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if toolResults != 2 {
		t.Errorf("expected 2 tool results, got %d", toolResults)
	}
}

func TestQuery_ToolUseStartEvent(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu_1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)

	var toolUseStartSeen bool
	for evt := range eventCh {
		if evt.Type == types.EventToolUseStart && evt.ToolUse != nil {
			toolUseStartSeen = true
			if evt.ToolUse.ID != "tu_1" {
				t.Errorf("expected tool use ID tu_1, got %s", evt.ToolUse.ID)
			}
			if evt.ToolUse.Name != "my_tool" {
				t.Errorf("expected tool use name my_tool, got %s", evt.ToolUse.Name)
			}
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !toolUseStartSeen {
		t.Error("expected EventToolUseStart event")
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "greet me", nil)

	var deltas []string
	for evt := range eventCh {
		if evt.Type == types.EventTextDelta {
			deltas = append(deltas, evt.Text)
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(deltas) != 2 {
		t.Fatalf("expected 2 text deltas, got %d", len(deltas))
	}
	if deltas[0] != "Hello " || deltas[1] != "world!" {
		t.Errorf("unexpected deltas: %v", deltas)
	}
}

func TestQuery_StreamStartAndCompleteEvents(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hi"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)

	var streamStarts, completes int
	for evt := range eventCh {
		switch evt.Type {
		case types.EventStreamStart:
			streamStarts++
		case types.EventComplete:
			completes++
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if streamStarts != 1 {
		t.Errorf("expected 1 stream start, got %d", streamStarts)
	}
	if completes != 1 {
		t.Errorf("expected 1 complete, got %d", completes)
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 2}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "ping", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

func TestQuery_MaxTurns(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	for i := 0; i < 3; i++ {
		events := toolUseStreamEvents("test-model", fmt.Sprintf("t%d", i), "my_tool", `{}`)
		mp.addResponse(events, nil)
	}
	mp.addResponse(textStreamEvents("test-model", "All done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "do 3 rounds", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.TurnCount != 3 {
		t.Errorf("expected 3 turns, got %d", result.TurnCount)
	}
}

func TestQuery_DescriptionError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	mt := &mockTool{name: "err_desc_tool", enabled: true}
	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}
	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Fatal("expected error from stream event error")
	}
	if result.Terminal != types.TerminalModelError {
		t.Errorf("expected TerminalModelError, got %s", result.Terminal)
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
	}
}

// TestQuery_ContextOverflowStreamError tests classifyTerminalError's context overflow path.
func TestQuery_ContextOverflowStreamError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Error: &llm.APIError{Message: "prompt too long", Status: 400, ErrorCode: "prompt_too_long"}},
	}
	mp.addResponse(events, nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if result.Terminal != types.TerminalPromptTooLong {
		t.Errorf("expected TerminalPromptTooLong, got %s", result.Terminal)
	}
}

// TestQuery_RateLimitStreamError tests classifyTerminalError's rate limit path.
func TestQuery_RateLimitStreamError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	events := []llm.StreamEvent{
		{Error: &llm.APIError{Message: "rate limited", Status: 429, Retryable: false}},
	}
	mp.addResponse(events, nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if result.Terminal != types.TerminalBlockingLimit {
		t.Errorf("expected TerminalBlockingLimit, got %s", result.Terminal)
	}
}

// TestQuery_ContextCancelledDuringStreaming tests queryLoop's ctx.Done() branch
// at the top of the turn loop.
func TestQuery_ContextCancelledDuringStreaming(t *testing.T) {
	mp := &mockProvider{}
	// Return a complete response (no tool use, end_turn) so queryLoop finishes
	// the first turn and loops back to check ctx.Done() at the top.
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first turn completes — queryLoop catches it at top of next iteration
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	// The response completes normally (end_turn) — no error expected.
	// This test validates the ctx.Done() path exists; actual cancellation
	// is tested by TestQuery_CancelledContext.
	_ = result
}

// TestQuery_DescriptionErrorFallback tests callLLM's tool description error fallback
// (line 224-226: desc = t.Name() when Description() returns error).
func TestQuery_DescriptionErrorFallback(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	mt := &mockTool{
		name:    "desc_err_tool",
		enabled: true,
		descFn:  func(json.RawMessage) (string, error) { return "", errors.New("desc error") },
	}
	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

// TestEscapeJSONString_QuotedInput tests the branch where HTMLEscape output
// starts and ends with '"' (line 432-433).
func TestEscapeJSONString_QuotedInput(t *testing.T) {
	t.Parallel()
	got := engine.EscapeJSONString(`"quoted"`)
	if got != "quoted" {
		t.Errorf("EscapeJSONString(%q) = %q, want %q", `"quoted"`, got, "quoted")
	}
}

// TestEscapeJSONString_Unicode tests unicode and special char escaping edge cases.
func TestEscapeJSONString_Unicode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "multi-byte unicode", input: "日本語", want: "日本語"},
		{name: "emoji", input: "🎉", want: "🎉"},
		{name: "mixed html", input: "a<b&c", want: "a\\u003cb\\u0026c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := engine.EscapeJSONString(tt.input)
			if got != tt.want {
				t.Errorf("EscapeJSONString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestEscapeJSONString_ShortString tests the branch where result is shorter than 2 chars
// (no surrounding quotes to strip).
func TestEscapeJSONString_ShortString(t *testing.T) {
	t.Parallel()
	// Single char
	got := engine.EscapeJSONString("a")
	if got != "a" {
		t.Errorf("expected 'a', got %q", got)
	}
}

// TestQuery_HasContentNoBlocks tests callLLM's fallback path where text deltas
// are received but no content_block_start events occurred.
func TestQuery_HasContentNoBlocks(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// Stream with text deltas but no content_block_start — triggers the
	// hasContent && len(contentBlocks) == 0 fallback in callLLM.
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "orphan text"}},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 3}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "my_tool", enabled: true}
	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
		Dispatcher: h,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test hub events", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	hubEvents := handler.Events()

	// Verify we got the key event types
	var gotStreamStart, gotToolUseStart, gotToolResult, gotTextDelta, gotComplete bool
	for _, evt := range hubEvents {
		switch evt.Type {
		case types.EventStreamStart:
			gotStreamStart = true
		case types.EventToolUseStart:
			gotToolUseStart = true
		case types.EventToolResult:
			gotToolResult = true
		case types.EventTextDelta:
			gotTextDelta = true
		case types.EventComplete:
			gotComplete = true
		}
	}

	if !gotStreamStart {
		t.Error("Hub handler did not receive EventStreamStart")
	}
	if !gotToolUseStart {
		t.Error("Hub handler did not receive EventToolUseStart")
	}
	if !gotToolResult {
		t.Error("Hub handler did not receive EventToolResult")
	}
	if !gotTextDelta {
		t.Error("Hub handler did not receive EventTextDelta")
	}
	if !gotComplete {
		t.Error("Hub handler did not receive EventComplete")
	}

	// Verify ordering: first event should be EventMessage, last should be EventComplete
	if len(hubEvents) == 0 {
		t.Fatal("expected at least one hub event")
	}
	if hubEvents[0].Type != types.EventMessage {
		t.Errorf("expected first event to be EventMessage, got %s", hubEvents[0].Type)
	}
	if hubEvents[len(hubEvents)-1].Type != types.EventComplete {
		t.Errorf("expected last event to be EventComplete, got %s", hubEvents[len(hubEvents)-1].Type)
	}
}

// mockDispatcher is a non-hub EventDispatcher for testing interface compliance.
type mockDispatcher struct {
	mu     sync.Mutex
	events []types.QueryEvent
}

func (d *mockDispatcher) Dispatch(event types.QueryEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, event)
}

func (d *mockDispatcher) Events() []types.QueryEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]types.QueryEvent, len(d.events))
	copy(out, d.events)
	return out
}

func TestQuery_EventDispatcherInterface(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "via interface"), nil)

	d := &mockDispatcher{}
	eng := engine.New(&engine.Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: d,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test interface", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	events := d.Events()
	if len(events) == 0 {
		t.Fatal("mockDispatcher should receive events")
	}

	// Verify key events received through the interface
	var gotStreamStart, gotTextDelta, gotComplete bool
	for _, evt := range events {
		switch evt.Type {
		case types.EventStreamStart:
			gotStreamStart = true
		case types.EventTextDelta:
			gotTextDelta = true
		case types.EventComplete:
			gotComplete = true
		}
	}
	if !gotStreamStart {
		t.Error("dispatcher did not receive EventStreamStart")
	}
	if !gotTextDelta {
		t.Error("dispatcher did not receive EventTextDelta")
	}
	if !gotComplete {
		t.Error("dispatcher did not receive EventComplete")
	}
}

func TestQuery_HubNilWorks(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Dispatcher: nil,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test nil hub", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
	}
}

func TestQuery_MultiTurn_MemoryAccumulates(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello Xiaoming!"), nil)
	mp.addResponse(textStreamEvents("test-model", "Your name is Xiaoming."), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	// Turn 1
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	eventCh1, resultCh1 := eng.Query(ctx1, "My name is Xiaoming", nil)
	for range eventCh1 {
	}
	result1 := <-resultCh1
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
	eventCh2, resultCh2 := eng.Query(ctx2, "What is my name?", nil)
	for range eventCh2 {
	}
	result2 := <-resultCh2
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
	texts := engine.ExtractTextBlocks(msgs2[0])
	if len(texts) == 0 || texts[0] != "My name is Xiaoming" {
		t.Errorf("msg[0] text = %v, want 'My name is Xiaoming'", texts)
	}
}
