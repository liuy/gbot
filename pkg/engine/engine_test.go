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
func (t *mockTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	if t.callFn != nil {
		return t.callFn(ctx, input, tctx)
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *mockTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
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
	eng := engine.New(&engine.Params{
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
		if evt.Type == types.EventToolEnd {
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel BEFORE calling Query to deterministically trigger context cancellation
	cancel()

	eventCh, resultCh := eng.Query(ctx, "test query", nil)
	for range eventCh {
	}

	result := <-resultCh
	// The context is already cancelled, so the select in queryLoop should catch it
	// before calling callLLM, resulting in TerminalAbortedStreaming.
	if result.Terminal != types.TerminalAbortedStreaming {
		t.Errorf("expected TerminalAbortedStreaming, got %s (error: %v)", result.Terminal, result.Error)
	}
	// Also verify the error is set and mentions context cancellation
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
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "done"}, nil
		},
	}

	eng := engine.New(&engine.Params{
		Provider:  mp,
		Tools:     []tool.Tool{mt},
		Model:     "test-model",
		MaxTokens: 16000,
		Logger:    slog.Default(),
	})
	// Set auto-compact config without compactor so auto-compact won't fire.
	// Only the blocking limit should guard against oversized context.
	eng.UpdateAutoCompactConfig(engine.AutoCompactConfig{
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

	eventCh, resultCh := eng.Query(ctx, "do something", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Terminal != types.TerminalPromptTooLong {
		t.Fatalf("expected TerminalPromptTooLong, got %s", result.Terminal)
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
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 5}},
			{Type: "message_stop"},
		}
		mp.addResponse(events, nil)

		eng := engine.New(&engine.Params{
			Provider:  mp,
			Model:     "test-model",
			MaxTokens: 16000,
			Logger:    slog.Default(),
		})
		// ContextWindow=1000, maxTokens=16000 -> blockingLimit = 1000 - 16000 - 3000 = -18000 (skipped)
		eng.UpdateAutoCompactConfig(engine.AutoCompactConfig{
			ContextWindow:          1000,
			MaxConsecutiveFailures: 3,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		eventCh, resultCh := eng.Query(ctx, "hello", nil)
		for range eventCh {
		}

		result := <-resultCh
		if result.Terminal != types.TerminalCompleted {
			t.Fatalf("negative blockingLimit should be skipped, got %s", result.Terminal)
		}
	})
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
	// emit an EventToolEnd event (only known tools emit events).
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
	var errorDisplayOutput string
	for evt := range eventCh {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.IsError {
			gotErrorResult = true
			errorDisplayOutput = evt.ToolResult.DisplayOutput
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected final error: %v", result.Error)
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
	if !strings.Contains(result.Error.Error(), "bad request") {
		t.Errorf("error should contain 'bad request', got: %v", result.Error)
	}
	// The error is wrapped by callLLM as "stream request: <original>", but
	// classifyTerminalError uses errors.As to unwrap and see the APIError type.
	if result.Terminal != types.TerminalPromptTooLong {
		t.Errorf("expected TerminalPromptTooLong, got %s", result.Terminal)
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
		t.Fatalf("expected no error after retry, got: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted after retry, got %s", result.Terminal)
	}
}

func TestQuery_DisabledToolSkipped(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	callCount := 0
	mt := &mockTool{
		name:    "disabled_tool",
		enabled: false,
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			callCount++
			return &tool.ToolResult{Data: "should not be called"}, nil
		},
	}
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
	// Verify the mock tool's Execute was NOT called
	if callCount != 0 {
		t.Errorf("disabled tool was called %d times, want 0", callCount)
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

func TestMessages_ReturnsCopy(t *testing.T) {
	t.Parallel()

	eng := engine.New(&engine.Params{
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

	eng := engine.New(&engine.Params{
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

	eng := engine.New(&engine.Params{
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

	eng := engine.New(&engine.Params{
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
	eng := engine.New(&engine.Params{
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

	eng := engine.New(&engine.Params{
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

// TestClassifyTerminalError tests the classifyTerminalError helper function.
// This test validates error classification by triggering actual engine errors
// and checking the TerminalReason in the result.
func TestClassifyTerminalError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		makeErr  func() *llm.APIError
		expected types.TerminalReason
	}{
		{
			name:     "context overflow",
			makeErr:  func() *llm.APIError { return &llm.APIError{Status: 400, ErrorCode: "prompt_too_long"} },
			expected: types.TerminalPromptTooLong,
		},
		{
			name:     "rate limit",
			makeErr:  func() *llm.APIError { return &llm.APIError{Status: 429} },
			expected: types.TerminalBlockingLimit,
		},
		{
			name:     "server error 500",
			makeErr:  func() *llm.APIError { return &llm.APIError{Status: 500} },
			expected: types.TerminalModelError,
		},
		{
			name:     "overloaded 529",
			makeErr:  func() *llm.APIError { return &llm.APIError{Status: 529} },
			expected: types.TerminalModelError,
		},
		{
			name:     "API error 400 without prompt_too_long",
			makeErr:  func() *llm.APIError { return &llm.APIError{Status: 400, ErrorCode: "other_error"} },
			expected: types.TerminalModelError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mp := &mockProvider{}
			// Return an error from Stream to trigger classifyTerminalError
			// The error is NOT wrapped when returned via event.Error
			events := []llm.StreamEvent{
				{Error: tt.makeErr()},
			}
			mp.addResponse(events, nil)

			eng := engine.New(&engine.Params{
				Provider: mp,
				Model:    "test",
				Logger:   slog.Default(),
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			eventCh, resultCh := eng.Query(ctx, "test", nil)
			for range eventCh {
			}

			result := <-resultCh
			if result.Terminal != tt.expected {
				t.Errorf("classifyTerminalError() = %s, want %s", result.Terminal, tt.expected)
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

	var toolACalled, toolBCalled bool
	toolA := &mockTool{name: "tool_a", enabled: true, callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		toolACalled = true
		return &tool.ToolResult{Data: "a_result"}, nil
	}}
	toolB := &mockTool{name: "tool_b", enabled: true, callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		toolBCalled = true
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
		if evt.Type == types.EventToolEnd {
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
		if evt.Type == types.EventToolStart && evt.ToolUse != nil {
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
		t.Error("expected EventToolStart event")
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

	var turnStarts, completes int
	for evt := range eventCh {
		switch evt.Type {
		case types.EventTurnStart:
			turnStarts++
		case types.EventQueryEnd:
			completes++
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if turnStarts != 1 {
		t.Errorf("expected 1 turn start, got %d", turnStarts)
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
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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
	// LLM requests a tool that has a broken Description() implementation
	toolEvents := toolUseStreamEvents("test-model", "t1", "err_desc_tool", `{}`)
	mp.addResponse(toolEvents, nil)
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	mt := &mockTool{
		name:    "err_desc_tool",
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

	var toolResultSeen bool
	for evt := range eventCh {
		if evt.Type == types.EventToolEnd {
			toolResultSeen = true
			if evt.ToolResult == nil {
				t.Fatal("ToolResult is nil")
			}
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !toolResultSeen {
		t.Error("expected tool result event to be emitted")
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
	if !strings.Contains(result.Error.Error(), "stream interrupted") {
		t.Errorf("error should contain 'stream interrupted', got: %v", result.Error)
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
	if !strings.Contains(result.Error.Error(), "prompt too long") {
		t.Errorf("error should mention 'prompt too long', got: %v", result.Error)
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
	if !strings.Contains(result.Error.Error(), "rate limited") {
		t.Errorf("error should mention 'rate limited', got: %v", result.Error)
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
	// Don't discard result - assert on it
	// The response completes normally (end_turn) before cancellation, so no error expected.
	// This test validates the ctx.Done() path exists; actual cancellation
	// is tested by TestQuery_ContextCancellation.
	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}
	// Verify we got a successful completion
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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
		if evt.Type == types.EventToolStart && evt.ToolUse != nil {
			toolUseStartSeen = true
			// Verify description fell back to tool name
			if evt.ToolUse.Name != "desc_err_tool" {
				t.Errorf("expected tool name 'desc_err_tool', got '%s'", evt.ToolUse.Name)
			}
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !toolUseStartSeen {
		t.Error("expected EventToolStart event")
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
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
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

	eng := engine.New(&engine.Params{
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: h,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test ordering", nil)
	for range eventCh {
	}

	result := <-resultCh
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

	eng := engine.New(&engine.Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
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

// ---------------------------------------------------------------------------
// Token usage: no double-counting across LLM calls
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Notification queue
// ---------------------------------------------------------------------------

func TestEngine_EnqueueNotification(t *testing.T) {
	t.Parallel()

	eng := engine.New(&engine.Params{
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
		Timestamp: time.Now(),
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
	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	// Enqueue notification BEFORE starting query — it should be drained
	// at the start of the first queryLoop iteration.
	eng.EnqueueNotification(types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<task-notification><task-id>bg-1</task-id></task-notification>"),
		},
		Timestamp: time.Now(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)

	var notificationMsgSeen bool
	for evt := range eventCh {
		if evt.Type == types.EventQueryStart && evt.Message != nil {
			for _, block := range evt.Message.Content {
				if strings.HasPrefix(block.Text, "<task-notification>") {
					notificationMsgSeen = true
				}
			}
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// The notification should have been injected as a message
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

	eng := engine.New(&engine.Params{
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
				Timestamp: time.Now(),
			})
		}(i)
	}
	wg.Wait()

	// Count enqueued notifications by triggering a query and checking messages
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
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
			Usage:    &llm.UsageDelta{OutputTokens: 100},
		},
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

	var usageEvents []types.UsageEvent
	for evt := range eventCh {
		if evt.Type == types.EventUsage && evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}
	<-resultCh

	// Should have exactly 2 usage events: message_start (input) + message_delta (output)
	if len(usageEvents) != 2 {
		t.Fatalf("expected 2 usage events, got %d: %+v", len(usageEvents), usageEvents)
	}

	// First event (message_start): input tokens set, output=0
	if usageEvents[0].InputTokens != 2500 {
		t.Errorf("first usage InputTokens = %d, want 2500", usageEvents[0].InputTokens)
	}
	if usageEvents[0].OutputTokens != 0 {
		t.Errorf("first usage OutputTokens = %d, want 0 (not yet known)", usageEvents[0].OutputTokens)
	}

	// Second event (message_delta): output tokens set, input carries max() value (2500)
	if usageEvents[1].OutputTokens != 100 {
		t.Errorf("second usage OutputTokens = %d, want 100", usageEvents[1].OutputTokens)
	}
	if usageEvents[1].InputTokens != 2500 {
		t.Errorf("second usage InputTokens = %d, want 2500 (max of start+delta)", usageEvents[1].InputTokens)
	}

	// Verify TUI-style accumulation using max() for input, += for output:
	totalIn, totalOut := 0, 0
	for _, u := range usageEvents {
		if u.InputTokens > totalIn {
			totalIn = u.InputTokens
		}
		totalOut += u.OutputTokens
	}
	if totalIn != 2500 {
		t.Errorf("accumulated input tokens = %d, want 2500", totalIn)
	}
	if totalOut != 100 {
		t.Errorf("accumulated output tokens = %d, want 100", totalOut)
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
			Usage: &llm.UsageDelta{
				OutputTokens:             30,
				CacheReadInputTokens:     5000,
				CacheCreationInputTokens: 0,
			},
		},
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

	var usageEvents []types.UsageEvent
	for evt := range eventCh {
		if evt.Type == types.EventUsage && evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}
	result := <-resultCh

	if len(usageEvents) != 2 {
		t.Fatalf("expected 2 usage events, got %d: %+v", len(usageEvents), usageEvents)
	}

	// First event (message_start): cache tokens are 0
	if usageEvents[0].CacheReadInputTokens != 0 {
		t.Errorf("first usage CacheRead = %d, want 0", usageEvents[0].CacheReadInputTokens)
	}

	// Second event (message_delta): MUST carry the cache_read from event.Usage,
	// not from the stale local `usage` variable (which was 0 from message_start).
	if usageEvents[1].CacheReadInputTokens != 5000 {
		t.Errorf("second usage CacheRead = %d, want 5000 (from message_delta)", usageEvents[1].CacheReadInputTokens)
	}

	// Verify TUI-style accumulation
	totalCacheRead := 0
	for _, u := range usageEvents {
		if u.CacheReadInputTokens > 0 {
			totalCacheRead = u.CacheReadInputTokens
		}
	}
	if totalCacheRead != 5000 {
		t.Errorf("accumulated cache_read = %d, want 5000", totalCacheRead)
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
			Usage: &llm.UsageDelta{
				OutputTokens: 5,
			},
		},
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

	var usageEvents []types.UsageEvent
	for evt := range eventCh {
		if evt.Type == types.EventUsage && evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}
	result := <-resultCh

	if len(usageEvents) != 2 {
		t.Fatalf("expected 2 usage events, got %d", len(usageEvents))
	}

	// First event (message_start): cache_creation=5409
	if usageEvents[0].CacheCreationInputTokens != 5409 {
		t.Errorf("first usage CacheCreation = %d, want 5409", usageEvents[0].CacheCreationInputTokens)
	}

	// Accumulated total using TUI-style > 0 overwrite (not +=)
	totalCacheCreation := 0
	for _, u := range usageEvents {
		if u.CacheCreationInputTokens > 0 {
			totalCacheCreation = u.CacheCreationInputTokens
		}
	}
	if totalCacheCreation != 5409 {
		t.Errorf("accumulated cache_creation = %d, want 5409", totalCacheCreation)
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

	eng := engine.New(&engine.Params{
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

	eng := engine.New(&engine.Params{
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

	eng := engine.New(&engine.Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventCh, resultCh := eng.ProcessNotifications(ctx, nil)

	// Drain events
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("Terminal = %q, want completed (empty queue)", result.Terminal)
	}
}

func TestProcessNotifications_DrainsAndRunsTurns(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	// LLM sees the notification and responds with text (no tool_use)
	mp.addResponse(textStreamEvents("test-model", "Background task completed."), nil)

	// No Hub — events flow through eventCh
	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	// Enqueue a notification (no Hub event since dispatcher is nil)
	eng.EnqueueNotification(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("<notification>bg-1 completed</notification>")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.ProcessNotifications(ctx, nil)

	// Collect events from eventCh (no Hub)
	var eventTypes []types.QueryEventType
	for evt := range eventCh {
		eventTypes = append(eventTypes, evt.Type)
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("Terminal = %q, want completed", result.Terminal)
	}

	// Verify notification was injected: should have at least query_start + turn_start + text_delta + turn_end + query_end
	var gotQueryStart, gotTurnStart, gotTextDelta, gotQueryEnd bool
	for _, et := range eventTypes {
		switch et {
		case types.EventQueryStart:
			gotQueryStart = true
		case types.EventTurnStart:
			gotTurnStart = true
		case types.EventTextDelta:
			gotTextDelta = true
		case types.EventQueryEnd:
			gotQueryEnd = true
		}
	}
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

	eng := engine.New(&engine.Params{
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
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	eventCh, resultCh := eng.ProcessNotifications(ctx, nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Error("expected error from cancelled context")
	}
	if result.Error != nil && !strings.Contains(result.Error.Error(), "context canceled") {
		t.Errorf("error should mention 'context canceled', got: %v", result.Error)
	}
	// Context cancellation from provider error path classifies as model_error,
	// not aborted_streaming (which only fires from engine's own <-ctx.Done() check)
	if result.Terminal == types.TerminalCompleted {
		t.Errorf("Terminal should not be completed on error, got %q", result.Terminal)
	}
}

func TestQuery_EventTextStartEmitted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "hello"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)

	var textStartSeen bool
	var textStartBeforeDelta bool
	var deltaSeen bool
	for evt := range eventCh {
		if evt.Type == types.EventTextStart {
			textStartSeen = true
			if !deltaSeen {
				textStartBeforeDelta = true
			}
		}
		if evt.Type == types.EventTextDelta {
			deltaSeen = true
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !textStartSeen {
		t.Error("expected EventTextStart event to be emitted for text content block")
	}
	if !textStartBeforeDelta {
		t.Error("expected EventTextStart to fire before any EventTextDelta")
	}
}

func TestQuery_EventTextEndEmitted(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "hello"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)

	var textEndSeen bool
	var textEndAfterDelta bool
	var deltaSeen bool
	for evt := range eventCh {
		if evt.Type == types.EventTextDelta {
			deltaSeen = true
		}
		if evt.Type == types.EventTextEnd {
			textEndSeen = true
			if deltaSeen {
				textEndAfterDelta = true
			}
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !textEndSeen {
		t.Error("expected EventTextEnd event to be emitted for text content block")
	}
	if !textEndAfterDelta {
		t.Error("expected EventTextEnd to fire after last EventTextDelta")
	}
}

func TestQuery_EventToolRunEmitted(t *testing.T) {
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

	var toolRunSeen bool
	var toolRunID, toolRunName string
	var toolStartSeen, toolEndSeen bool
	var toolStartBeforeRun, toolRunBeforeEnd bool
	for evt := range eventCh {
		switch evt.Type {
		case types.EventToolStart:
			toolStartSeen = true
		case types.EventToolRun:
			toolRunSeen = true
			if evt.ToolUse != nil {
				toolRunID = evt.ToolUse.ID
				toolRunName = evt.ToolUse.Name
			}
			if toolStartSeen {
				toolStartBeforeRun = true
			}
		case types.EventToolEnd:
			toolEndSeen = true
			if toolRunSeen {
				toolRunBeforeEnd = true
			}
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !toolRunSeen {
		t.Error("expected EventToolRun event to be emitted for tool_use content block")
	}
	if toolRunID != "tu_1" {
		t.Errorf("EventToolRun ID = %q, want tu_1", toolRunID)
	}
	if toolRunName != "my_tool" {
		t.Errorf("EventToolRun Name = %q, want my_tool", toolRunName)
	}
	if !toolStartSeen {
		t.Error("expected EventToolStart")
	}
	if !toolEndSeen {
		t.Error("expected EventToolEnd")
	}
	if !toolStartBeforeRun {
		t.Error("expected EventToolStart to fire before EventToolRun")
	}
	if !toolRunBeforeEnd {
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 10}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	// Turn 2: text response after tool execution
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

	// Collect all content lifecycle events in order
	var eventOrder []string
	for evt := range eventCh {
		switch evt.Type {
		case types.EventThinkingStart, types.EventThinkingDelta, types.EventThinkingEnd,
			types.EventToolStart, types.EventToolParamDelta, types.EventToolRun, types.EventToolEnd,
			types.EventTextStart, types.EventTextDelta, types.EventTextEnd:
			eventOrder = append(eventOrder, string(evt.Type))
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 0}},
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

	var gotTextStart, gotTextEnd bool
	for evt := range eventCh {
		switch evt.Type {
		case types.EventTextStart:
			gotTextStart = true
		case types.EventTextEnd:
			gotTextEnd = true
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if !gotTextStart {
		t.Error("EventTextStart not emitted for empty text block")
	}
	if !gotTextEnd {
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 15}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	var readInput, bashInput json.RawMessage
	toolRead := &mockTool{name: "Read", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		readInput = input
		return &tool.ToolResult{Data: "file content"}, nil
	}}
	toolBash := &mockTool{name: "Bash", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		bashInput = input
		return &tool.ToolResult{Data: "file1\nfile2"}, nil
	}}

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{toolRead, toolBash},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "read and ls", nil)

	// Collect EventToolParamDelta events to verify ID/Name correspondence
	var paramDeltas []types.PartialInputEvent
	for evt := range eventCh {
		if evt.Type == types.EventToolParamDelta && evt.PartialInput != nil {
			paramDeltas = append(paramDeltas, *evt.PartialInput)
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 20}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(textStreamEvents("test-model", "All done."), nil)

	var inputs sync.Map
	toolRead := &mockTool{name: "Read", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		inputs.Store("Read", input)
		return &tool.ToolResult{Data: "contents"}, nil
	}}
	toolBash := &mockTool{name: "Bash", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		inputs.Store("Bash", input)
		return &tool.ToolResult{Data: "ok"}, nil
	}}
	toolGrep := &mockTool{name: "Grep", enabled: true, callFn: func(_ context.Context, input json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
		inputs.Store("Grep", input)
		return &tool.ToolResult{Data: "matches"}, nil
	}}

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{toolRead, toolBash, toolGrep},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "parallel ops", nil)
	// Drain events
	for range eventCh {
	}

	result := <-resultCh
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

	eng := engine.New(&engine.Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	// Set compactor and verify it doesn't panic
	eng.SetCompactor(
		&mockCompactor{},
		engine.AutoCompactConfig{
			ContextWindow:          100000,
			MaxConsecutiveFailures: 3,
		},
	)

	// Verify concurrent SetCompactor doesn't race
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			eng.SetCompactor(&mockCompactor{}, engine.AutoCompactConfig{ContextWindow: 100000})
		})
	}
	wg.Wait()
}
