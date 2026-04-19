package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestProvider() *OpenAIProvider {
	return NewOpenAIProvider(&OpenAIConfig{
		APIKey: "test-key",
		Model:  "gpt-4",
	})
}

// ---------------------------------------------------------------------------
// NewOpenAIProvider tests
// ---------------------------------------------------------------------------

func TestOpenAIProvider_Defaults(t *testing.T) {
	t.Parallel()

	p := NewOpenAIProvider(&OpenAIConfig{
		APIKey: "test-key",
		Model:  "gpt-4",
	})

	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.baseURL != "https://api.openai.com/v1" {
		t.Errorf("baseURL = %q, want %q", p.baseURL, "https://api.openai.com/v1")
	}
	if p.apiKey != "test-key" {
		t.Errorf("apiKey = %q, want %q", p.apiKey, "test-key")
	}
	if p.model != "gpt-4" {
		t.Errorf("model = %q, want %q", p.model, "gpt-4")
	}
	if p.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
	// Verify default timeout is 300s
	if p.httpClient.Timeout != 300*time.Second {
		t.Errorf("httpClient.Timeout = %v, want %v", p.httpClient.Timeout, 300*time.Second)
	}
}

func TestOpenAIProvider_CustomConfig(t *testing.T) {
	t.Parallel()

	p := NewOpenAIProvider(&OpenAIConfig{
		APIKey:  "my-custom-key",
		BaseURL: "https://custom.api.example.com/v1",
		Model:   "gpt-4o",
		Timeout: 60 * time.Second,
	})

	if p.baseURL != "https://custom.api.example.com/v1" {
		t.Errorf("baseURL = %q, want %q", p.baseURL, "https://custom.api.example.com/v1")
	}
	if p.apiKey != "my-custom-key" {
		t.Errorf("apiKey = %q, want %q", p.apiKey, "my-custom-key")
	}
	if p.model != "gpt-4o" {
		t.Errorf("model = %q, want %q", p.model, "gpt-4o")
	}
	if p.httpClient.Timeout != 60*time.Second {
		t.Errorf("httpClient.Timeout = %v, want %v", p.httpClient.Timeout, 60*time.Second)
	}
}

func TestOpenAIProvider_BaseURL_TrailingSlashTrimmed(t *testing.T) {
	t.Parallel()

	p := NewOpenAIProvider(&OpenAIConfig{
		APIKey:  "key",
		BaseURL: "https://api.example.com/v1///",
		Model:   "gpt-4",
	})

	if p.baseURL != "https://api.example.com/v1" {
		t.Errorf("baseURL = %q, want trailing slashes trimmed to %q", p.baseURL, "https://api.example.com/v1")
	}
}

// ---------------------------------------------------------------------------
// translateMessages tests
// ---------------------------------------------------------------------------

func TestTranslateMessages_TextOnly(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock("hello")},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("role = %q, want %q", result[0].Role, "user")
	}
	if result[0].Content != "hello" {
		t.Errorf("content = %q, want %q", result[0].Content, "hello")
	}
}

func TestTranslateMessages_AssistantText(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role:    types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("hi")},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Role != "assistant" {
		t.Errorf("role = %q, want %q", result[0].Role, "assistant")
	}
	if result[0].Content != "hi" {
		t.Errorf("content = %q, want %q", result[0].Content, "hi")
	}
	if len(result[0].ToolCalls) != 0 {
		t.Errorf("tool_calls should be empty, got %d entries", len(result[0].ToolCalls))
	}
}

func TestTranslateMessages_ToolUse(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{
					Type:  types.ContentTypeToolUse,
					ID:    "call_abc123",
					Name:  "get_weather",
					Input: json.RawMessage(`{"city":"SF"}`),
				},
			},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Role != "assistant" {
		t.Errorf("role = %q, want %q", result[0].Role, "assistant")
	}
	if len(result[0].ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(result[0].ToolCalls))
	}

	tc := result[0].ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("tool_call ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Type != "function" {
		t.Errorf("tool_call Type = %q, want %q", tc.Type, "function")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("function name = %q, want %q", tc.Function.Name, "get_weather")
	}
	if tc.Function.Arguments != `{"city":"SF"}` {
		t.Errorf("function arguments = %q, want %q", tc.Function.Arguments, `{"city":"SF"}`)
	}
	// Content should be nil for assistant message with only tool_calls
	if result[0].Content != nil {
		t.Errorf("content should be nil for assistant with only tool_calls, got %v", result[0].Content)
	}
}

func TestTranslateMessages_ToolResult(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{
					Type:      types.ContentTypeToolResult,
					ToolUseID: "call_abc123",
					Content:   json.RawMessage(`"72F and sunny"`),
				},
			},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Role != "tool" {
		t.Errorf("role = %q, want %q", result[0].Role, "tool")
	}
	if result[0].ToolCallID != "call_abc123" {
		t.Errorf("tool_call_id = %q, want %q", result[0].ToolCallID, "call_abc123")
	}
	if result[0].Content != "72F and sunny" {
		t.Errorf("content = %q, want %q", result[0].Content, "72F and sunny")
	}
}

func TestTranslateMessages_ThinkingIgnored(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{
					Type: types.ContentTypeThinking,
					Text: "internal reasoning here",
				},
			},
		},
	}

	result := translateMessages(msgs)

	// Thinking-only assistant produces no output because assistantText is ""
	// and there are no tool_calls, so the assistant message is not emitted.
	if len(result) != 0 {
		t.Fatalf("len(result) = %d, want 0 (thinking blocks should be skipped)", len(result))
	}
}

func TestTranslateMessages_RedactedIgnored(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{
					Type: types.ContentTypeRedacted,
					Data: "redacted-data",
				},
			},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 0 {
		t.Fatalf("len(result) = %d, want 0 (redacted blocks should be skipped)", len(result))
	}
}

func TestTranslateMessages_MixedAssistantTextAndToolUse(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewTextBlock("Let me look that up."),
				{
					Type:  types.ContentTypeToolUse,
					ID:    "call_xyz",
					Name:  "search",
					Input: json.RawMessage(`{"q":"weather"}`),
				},
			},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Role != "assistant" {
		t.Errorf("role = %q, want %q", result[0].Role, "assistant")
	}
	if result[0].Content != "Let me look that up." {
		t.Errorf("content = %q, want %q", result[0].Content, "Let me look that up.")
	}
	if len(result[0].ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(result[0].ToolCalls))
	}
	if result[0].ToolCalls[0].ID != "call_xyz" {
		t.Errorf("tool_call ID = %q, want %q", result[0].ToolCalls[0].ID, "call_xyz")
	}
	if result[0].ToolCalls[0].Function.Name != "search" {
		t.Errorf("function name = %q, want %q", result[0].ToolCalls[0].Function.Name, "search")
	}
}

func TestTranslateMessages_MultipleToolResults(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{
					Type:      types.ContentTypeToolResult,
					ToolUseID: "call_1",
					Content:   json.RawMessage(`"result1"`),
				},
				{
					Type:      types.ContentTypeToolResult,
					ToolUseID: "call_2",
					Content:   json.RawMessage(`"result2"`),
				},
			},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
	if result[0].Role != "tool" {
		t.Errorf("result[0] role = %q, want %q", result[0].Role, "tool")
	}
	if result[0].ToolCallID != "call_1" {
		t.Errorf("result[0] tool_call_id = %q, want %q", result[0].ToolCallID, "call_1")
	}
	if result[1].Role != "tool" {
		t.Errorf("result[1] role = %q, want %q", result[1].Role, "tool")
	}
	if result[1].ToolCallID != "call_2" {
		t.Errorf("result[1] tool_call_id = %q, want %q", result[1].ToolCallID, "call_2")
	}
}

func TestTranslateMessages_Empty(t *testing.T) {
	t.Parallel()

	result := translateMessages(nil)
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0 for nil input", len(result))
	}

	result = translateMessages([]types.Message{})
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0 for empty slice", len(result))
	}
}

// ---------------------------------------------------------------------------
// translateTools tests
// ---------------------------------------------------------------------------

func TestTranslateTools(t *testing.T) {
	t.Parallel()

	tools := []ToolDef{
		{
			Name:        "get_weather",
			Description: "Get current weather",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		},
		{
			Name:        "calculator",
			Description: "Do math",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"expr":{"type":"string"}}}`),
		},
	}

	result := translateTools(tools)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}

	if result[0].Type != "function" {
		t.Errorf("result[0] Type = %q, want %q", result[0].Type, "function")
	}
	if result[0].Function.Name != "get_weather" {
		t.Errorf("result[0] Name = %q, want %q", result[0].Function.Name, "get_weather")
	}
	if result[0].Function.Description != "Get current weather" {
		t.Errorf("result[0] Description = %q, want %q", result[0].Function.Description, "Get current weather")
	}
	if string(result[0].Function.Parameters) != `{"type":"object","properties":{"city":{"type":"string"}}}` {
		t.Errorf("result[0] Parameters = %q, want exact schema", string(result[0].Function.Parameters))
	}

	if result[1].Function.Name != "calculator" {
		t.Errorf("result[1] Name = %q, want %q", result[1].Function.Name, "calculator")
	}
}

func TestTranslateTools_Empty(t *testing.T) {
	t.Parallel()

	result := translateTools(nil)
	if result == nil {
		t.Fatal("expected non-nil (empty) slice, got nil")
	}
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0", len(result))
	}
}

// ---------------------------------------------------------------------------
// extractSystemPrompt tests
// ---------------------------------------------------------------------------

func TestExtractSystemPrompt_String(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`"You are a helpful assistant."`)
	got := extractSystemPrompt(raw)

	if got != "You are a helpful assistant." {
		t.Errorf("got %q, want %q", got, "You are a helpful assistant.")
	}
}

func TestExtractSystemPrompt_ContentBlockArray(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[
		{"type":"text","text":"System instruction part 1."},
		{"type":"text","text":"System instruction part 2."}
	]`)
	got := extractSystemPrompt(raw)

	if !strings.Contains(got, "System instruction part 1.") {
		t.Errorf("result should contain first text block, got %q", got)
	}
	if !strings.Contains(got, "System instruction part 2.") {
		t.Errorf("result should contain second text block, got %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("text blocks should be joined with newline, got %q", got)
	}
}

func TestExtractSystemPrompt_Empty(t *testing.T) {
	t.Parallel()

	got := extractSystemPrompt(nil)
	if got != "" {
		t.Errorf("got %q, want empty string for nil", got)
	}

	got = extractSystemPrompt(json.RawMessage(""))
	if got != "" {
		t.Errorf("got %q, want empty string for empty raw", got)
	}
}

func TestExtractSystemPrompt_InvalidJSON(t *testing.T) {
	t.Parallel()

	got := extractSystemPrompt(json.RawMessage(`{not valid json}`))
	if got != "" {
		t.Errorf("got %q, want empty string for invalid JSON", got)
	}
}

// ---------------------------------------------------------------------------
// extractToolResultText tests
// ---------------------------------------------------------------------------

func TestExtractToolResultText_String(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`"file contents here"`)
	got := extractToolResultText(raw)

	if got != "file contents here" {
		t.Errorf("got %q, want %q", got, "file contents here")
	}
}

func TestExtractToolResultText_ContentBlockArray(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[
		{"type":"text","text":"line 1"},
		{"type":"text","text":"line 2"}
	]`)
	got := extractToolResultText(raw)

	if !strings.Contains(got, "line 1") {
		t.Errorf("result should contain 'line 1', got %q", got)
	}
	if !strings.Contains(got, "line 2") {
		t.Errorf("result should contain 'line 2', got %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("text blocks should be joined with newline, got %q", got)
	}
}

func TestExtractToolResultText_RawFallback(t *testing.T) {
	t.Parallel()

	// Non-string, non-ContentBlock JSON → raw string fallback
	raw := json.RawMessage(`{"lines":42,"ok":true}`)
	got := extractToolResultText(raw)

	if got != `{"lines":42,"ok":true}` {
		t.Errorf("got %q, want raw JSON string %q", got, `{"lines":42,"ok":true}`)
	}
}

func TestExtractToolResultText_Empty(t *testing.T) {
	t.Parallel()

	got := extractToolResultText(nil)
	if got != "" {
		t.Errorf("got %q, want empty string for nil", got)
	}

	got = extractToolResultText(json.RawMessage(""))
	if got != "" {
		t.Errorf("got %q, want empty string for empty raw", got)
	}
}

// ---------------------------------------------------------------------------
// mapFinishReason tests
// ---------------------------------------------------------------------------

func TestMapFinishReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"content_filter", "content_filter"},
		{"some_unknown_reason", "some_unknown_reason"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := mapFinishReason(tt.input)
			if got != tt.want {
				t.Errorf("mapFinishReason(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// translateRequest tests
// ---------------------------------------------------------------------------

func TestOpenAITranslateRequest_NoTools(t *testing.T) {
	t.Parallel()

	p := newTestProvider()

	req := &Request{
		Model:     "gpt-4",
		MaxTokens: 1024,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock("hello")},
			},
		},
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}

	// Unmarshal to inspect structure
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	// "tools" key must NOT be present when no tools are given
	if _, exists := parsed["tools"]; exists {
		t.Error("\"tools\" field should be omitted when no tools are provided, but it was present")
	}

	// Verify model
	if string(parsed["model"]) != `"gpt-4"` {
		t.Errorf("model = %s, want %q", string(parsed["model"]), `"gpt-4"`)
	}

	// Verify stream is omitted when false (omitempty)
	if _, exists := parsed["stream"]; exists {
		t.Errorf("\"stream\" should be omitted when false (omitempty), got %q", string(parsed["stream"]))
	}

	// Verify max_tokens
	if string(parsed["max_tokens"]) != "1024" {
		t.Errorf("max_tokens = %s, want %q", string(parsed["max_tokens"]), "1024")
	}

	// Verify messages contain exactly 1 message
	var messages []openaiMessage
	if err := json.Unmarshal(parsed["messages"], &messages); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want %q", messages[0].Role, "user")
	}
	if messages[0].Content != "hello" {
		t.Errorf("messages[0].Content = %q, want %q", messages[0].Content, "hello")
	}
}

func TestOpenAITranslateRequest_WithTools(t *testing.T) {
	t.Parallel()

	p := newTestProvider()

	req := &Request{
		Model:     "gpt-4",
		MaxTokens: 512,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock("what's the weather?")},
			},
		},
		Tools: []ToolDef{
			{
				Name:        "get_weather",
				Description: "Get weather",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			},
		},
	}

	body, err := p.translateRequest(req, true)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}

	var parsed struct {
		Model   string        `json:"model"`
		Stream  bool          `json:"stream"`
		Tools   []openaiTool  `json:"tools"`
		Message []openaiMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	if !parsed.Stream {
		t.Error("stream = false, want true")
	}
	if len(parsed.Tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(parsed.Tools))
	}
	if parsed.Tools[0].Type != "function" {
		t.Errorf("tool Type = %q, want %q", parsed.Tools[0].Type, "function")
	}
	if parsed.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tool Name = %q, want %q", parsed.Tools[0].Function.Name, "get_weather")
	}
	if parsed.Tools[0].Function.Description != "Get weather" {
		t.Errorf("tool Description = %q, want %q", parsed.Tools[0].Function.Description, "Get weather")
	}
}

func TestOpenAITranslateRequest_WithSystemPrompt(t *testing.T) {
	t.Parallel()

	p := newTestProvider()

	req := &Request{
		Model:     "gpt-4",
		MaxTokens: 256,
		System:    json.RawMessage(`"You are a helpful assistant."`),
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock("hello")},
			},
		},
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}

	var parsed struct {
		Messages []openaiMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	// First message should be system prompt
	if len(parsed.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (system + user)", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "system" {
		t.Errorf("messages[0].Role = %q, want %q", parsed.Messages[0].Role, "system")
	}
	if parsed.Messages[0].Content != "You are a helpful assistant." {
		t.Errorf("messages[0].Content = %q, want %q", parsed.Messages[0].Content, "You are a helpful assistant.")
	}
	// Second message is the user message
	if parsed.Messages[1].Role != "user" {
		t.Errorf("messages[1].Role = %q, want %q", parsed.Messages[1].Role, "user")
	}
}

func TestOpenAITranslateRequest_SystemPromptArray(t *testing.T) {
	t.Parallel()

	p := newTestProvider()

	req := &Request{
		Model:     "gpt-4",
		MaxTokens: 256,
		System: json.RawMessage(`[
			{"type":"text","text":"Rule 1: Be concise."},
			{"type":"text","text":"Rule 2: Be accurate."}
		]`),
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock("hello")},
			},
		},
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}

	var parsed struct {
		Messages []openaiMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	if len(parsed.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (system + user)", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "system" {
		t.Errorf("messages[0].Role = %q, want %q", parsed.Messages[0].Role, "system")
	}
	sysContent, ok := parsed.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("messages[0].Content should be string, got %T", parsed.Messages[0].Content)
	}
	if !strings.Contains(sysContent, "Rule 1: Be concise.") {
		t.Errorf("system prompt should contain first rule, got %q", sysContent)
	}
	if !strings.Contains(sysContent, "Rule 2: Be accurate.") {
		t.Errorf("system prompt should contain second rule, got %q", sysContent)
	}
}

func TestOpenAITranslateRequest_NoSystemPrompt(t *testing.T) {
	t.Parallel()

	p := newTestProvider()

	req := &Request{
		Model:     "gpt-4",
		MaxTokens: 256,
		System:    nil,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock("hello")},
			},
		},
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}

	var parsed struct {
		Messages []openaiMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	// No system message should be present
	if len(parsed.Messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1 (no system prompt)", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want %q", parsed.Messages[0].Role, "user")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestTranslateMessages_UserMultipleTextBlocks(t *testing.T) {
	t.Parallel()

	// User message with multiple text blocks should produce separate messages
	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock("first"),
				types.NewTextBlock("second"),
			},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
	if result[0].Content != "first" {
		t.Errorf("result[0].Content = %q, want %q", result[0].Content, "first")
	}
	if result[1].Content != "second" {
		t.Errorf("result[1].Content = %q, want %q", result[1].Content, "second")
	}
}

func TestTranslateMessages_AssistantMultipleTextBlocks(t *testing.T) {
	t.Parallel()

	// Assistant message with multiple text blocks should concatenate
	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewTextBlock("Hello "),
				types.NewTextBlock("World"),
			},
		},
	}

	result := translateMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Content != "Hello World" {
		t.Errorf("content = %q, want %q (concatenated)", result[0].Content, "Hello World")
	}
}

func TestExtractToolResultText_ContentBlockEmpty(t *testing.T) {
	t.Parallel()

	// ContentBlock array with no text → falls through to raw fallback
	raw := json.RawMessage(`[{"type":"image","data":"base64..."}]`)
	got := extractToolResultText(raw)

	// No text blocks → falls through to raw string fallback
	if got != `[{"type":"image","data":"base64..."}]` {
		t.Errorf("got %q, want raw JSON fallback", got)
	}
}

func TestTranslateRequest_TemperatureStopSequencesMetadata(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	temp := 0.7
	req := &Request{
		Model:         "gpt-4",
		MaxTokens:     1024,
		Temperature:   &temp,
		StopSequences: []string{"\n\n", "END"},
		Metadata:      &RequestMetadata{UserID: "user-123"},
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock("hello")},
			},
		},
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	// Temperature must be present and match
	if _, exists := parsed["temperature"]; !exists {
		t.Fatal("\"temperature\" field missing from request")
	}
	if string(parsed["temperature"]) != "0.7" {
		t.Errorf("temperature = %s, want 0.7", string(parsed["temperature"]))
	}

	// Stop sequences must map to "stop"
	if _, exists := parsed["stop"]; !exists {
		t.Fatal("\"stop\" field missing from request")
	}
	var stops []string
	if err := json.Unmarshal(parsed["stop"], &stops); err != nil {
		t.Fatalf("unmarshal stop: %v", err)
	}
	if len(stops) != 2 || stops[0] != "\n\n" || stops[1] != "END" {
		t.Errorf("stop = %v, want [\\n\\n END]", stops)
	}

	// Metadata.UserID must map to "user"
	if _, exists := parsed["user"]; !exists {
		t.Fatal("\"user\" field missing from request")
	}
	if string(parsed["user"]) != `"user-123"` {
		t.Errorf("user = %s, want %q", string(parsed["user"]), "user-123")
	}
}

func TestTranslateRequest_OmittedWhenNil(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	req := &Request{
		Model:     "gpt-4",
		MaxTokens: 1024,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock("hello")},
			},
		},
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	// Temperature, stop, user must be omitted when not set
	for _, field := range []string{"temperature", "stop", "user"} {
		if _, exists := parsed[field]; exists {
			t.Errorf("%q should be omitted when not set, but was present", field)
		}
	}
}
