package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/user/gbot/pkg/llm"
	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// NewAnthropicProvider tests
// ---------------------------------------------------------------------------

func TestNewAnthropicProvider_Defaults(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey: "test-key",
		Model:  "claude-sonnet-4-20250514",
	})

	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewAnthropicProvider_CustomBaseURL(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "https://custom.api.com/",
		Model:   "test-model",
		Timeout: 10 * time.Second,
	})

	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

// ---------------------------------------------------------------------------
// parseEvent tests
// ---------------------------------------------------------------------------

func TestParseEvent_MessageStart(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":25}}}`
	event := p.ParseEvent("message_start", data)

	if event.Type != "message_start" {
		t.Errorf("expected type message_start, got %s", event.Type)
	}
	if event.Message == nil {
		t.Fatal("expected non-nil Message")
	}
	if event.Message.ID != "msg_1" {
		t.Errorf("expected ID msg_1, got %s", event.Message.ID)
	}
	if event.Message.Model != "claude-sonnet-4-20250514" {
		t.Errorf("unexpected model: %s", event.Message.Model)
	}
}

func TestParseEvent_ContentBlockStart(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0,"content_block":{"type":"text","text":""}}`
	event := p.ParseEvent("content_block_start", data)

	if event.Type != "content_block_start" {
		t.Errorf("expected type content_block_start, got %s", event.Type)
	}
	if event.Index != 0 {
		t.Errorf("expected index 0, got %d", event.Index)
	}
	if event.ContentBlock == nil {
		t.Fatal("expected non-nil ContentBlock")
	}
	if event.ContentBlock.Type != types.ContentTypeText {
		t.Errorf("expected text content block, got %s", event.ContentBlock.Type)
	}
}

func TestParseEvent_ContentBlockStart_ToolUse(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"read_file","input":{}}}`
	event := p.ParseEvent("content_block_start", data)

	if event.ContentBlock == nil {
		t.Fatal("expected non-nil ContentBlock")
	}
	if event.ContentBlock.Type != types.ContentTypeToolUse {
		t.Errorf("expected tool_use, got %s", event.ContentBlock.Type)
	}
	if event.ContentBlock.ID != "tu_1" {
		t.Errorf("expected ID tu_1, got %s", event.ContentBlock.ID)
	}
}

func TestParseEvent_ContentBlockDelta_Text(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0,"delta":{"type":"text_delta","text":"Hello"}}`
	event := p.ParseEvent("content_block_delta", data)

	if event.Delta == nil {
		t.Fatal("expected non-nil Delta")
	}
	if event.Delta.Text != "Hello" {
		t.Errorf("expected text 'Hello', got %s", event.Delta.Text)
	}
}

func TestParseEvent_ContentBlockDelta_JSON(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`
	event := p.ParseEvent("content_block_delta", data)

	if event.Delta == nil {
		t.Fatal("expected non-nil Delta")
	}
	if event.Delta.PartialJSON != `{"path":` {
		t.Errorf("unexpected partial json: %s", event.Delta.PartialJSON)
	}
}

func TestParseEvent_ContentBlockStop(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0}`
	event := p.ParseEvent("content_block_stop", data)

	if event.Index != 0 {
		t.Errorf("expected index 0, got %d", event.Index)
	}
}

func TestParseEvent_MessageDelta(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`
	event := p.ParseEvent("message_delta", data)

	if event.DeltaMsg == nil {
		t.Fatal("expected non-nil DeltaMsg")
	}
	if event.DeltaMsg.StopReason != "end_turn" {
		t.Errorf("expected stop_reason end_turn, got %s", event.DeltaMsg.StopReason)
	}
	if event.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if event.Usage.OutputTokens != 42 {
		t.Errorf("expected 42 output tokens, got %d", event.Usage.OutputTokens)
	}
}

func TestParseEvent_MessageStop(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("message_stop", "{}")

	if event.Type != "message_stop" {
		t.Errorf("expected type message_stop, got %s", event.Type)
	}
}

func TestParseEvent_Ping(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("ping", "")

	if event.Type != "ping" {
		t.Errorf("expected type ping, got %s", event.Type)
	}
}

func TestParseEvent_Error(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"error":{"type":"overloaded_error","message":"Overloaded"}}`
	event := p.ParseEvent("error", data)

	if event.Error == nil {
		t.Fatal("expected non-nil Error")
	}
	if event.Error.Type != "overloaded_error" {
		t.Errorf("expected overloaded_error, got %s", event.Error.Type)
	}
	if event.Error.Message != "Overloaded" {
		t.Errorf("expected 'Overloaded', got %s", event.Error.Message)
	}
}

func TestParseEvent_InvalidJSON(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("message_start", "not valid json")

	if event.Message != nil {
		t.Error("expected nil Message for invalid JSON")
	}
}

func TestParseEvent_UnknownEventType(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("custom_event", `{"data":"test"}`)

	if event.Type != "custom_event" {
		t.Errorf("expected custom_event, got %s", event.Type)
	}
}

// ---------------------------------------------------------------------------
// calculateBackoff tests
// ---------------------------------------------------------------------------

func TestCalculateBackoff(t *testing.T) {
	t.Parallel()

	cfg := llm.DefaultRetryConfig()

	tests := []struct {
		name    string
		attempt int
		minTime time.Duration
		maxTime time.Duration
	}{
		{name: "attempt_0", attempt: 0, minTime: 200 * time.Millisecond, maxTime: 1 * time.Second},
		{name: "attempt_1", attempt: 1, minTime: 500 * time.Millisecond, maxTime: 3 * time.Second},
		{name: "attempt_5", attempt: 5, minTime: 8 * time.Second, maxTime: 34 * time.Second},
		{name: "attempt_10_capped", attempt: 10, minTime: 16 * time.Second, maxTime: 33 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backoff := llm.CalculateBackoff(tt.attempt, cfg)
			if backoff < tt.minTime || backoff > tt.maxTime {
				t.Errorf("attempt %d: backoff %v outside expected range [%v, %v]", tt.attempt, backoff, tt.minTime, tt.maxTime)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isRetryableStatus tests
// ---------------------------------------------------------------------------

func TestIsRetryableStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code    int
		retry   bool
	}{
		{429, true},
		{529, true},
		{500, true},
		{502, true},
		{503, true},
		{504, true},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{200, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.code), func(t *testing.T) {
			t.Parallel()
			got := llm.IsRetryableStatus(tt.code)
			if got != tt.retry {
				t.Errorf("isRetryableStatus(%d) = %v, want %v", tt.code, got, tt.retry)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error classification tests
// ---------------------------------------------------------------------------

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		retry   bool
	}{
		{name: "retryable API error", err: &llm.APIError{Retryable: true}, retry: true},
		{name: "non-retryable API error", err: &llm.APIError{Retryable: false}, retry: false},
		{name: "generic error", err: fmt.Errorf("some error"), retry: false},
		{name: "nil", err: nil, retry: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := llm.IsRetryable(tt.err)
			if got != tt.retry {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.retry)
			}
		})
	}
}

func TestIsContextOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		err   error
		overflow bool
	}{
		{name: "prompt too long", err: &llm.APIError{Status: 400, ErrorCode: "prompt_too_long"}, overflow: true},
		{name: "400 other error", err: &llm.APIError{Status: 400, ErrorCode: "other"}, overflow: false},
		{name: "500 error", err: &llm.APIError{Status: 500}, overflow: false},
		{name: "generic error", err: fmt.Errorf("error"), overflow: false},
		{name: "nil", err: nil, overflow: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := llm.IsContextOverflow(tt.err)
			if got != tt.overflow {
				t.Errorf("IsContextOverflow() = %v, want %v", got, tt.overflow)
			}
		})
	}
}

func TestIsMaxOutputTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		result bool
	}{
		{name: "max_output_tokens", err: &llm.APIError{Type: "max_output_tokens"}, result: true},
		{name: "other type", err: &llm.APIError{Type: "other"}, result: false},
		{name: "generic", err: fmt.Errorf("err"), result: false},
		{name: "nil", err: nil, result: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := llm.IsMaxOutputTokens(tt.err)
			if got != tt.result {
				t.Errorf("IsMaxOutputTokens() = %v, want %v", got, tt.result)
			}
		})
	}
}

func TestIsRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		result bool
	}{
		{name: "429", err: &llm.APIError{Status: 429}, result: true},
		{name: "500", err: &llm.APIError{Status: 500}, result: false},
		{name: "generic", err: fmt.Errorf("err"), result: false},
		{name: "nil", err: nil, result: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := llm.IsRateLimit(tt.err)
			if got != tt.result {
				t.Errorf("IsRateLimit() = %v, want %v", got, tt.result)
			}
		})
	}
}

func TestIsOverloaded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		result bool
	}{
		{name: "529", err: &llm.APIError{Status: 529}, result: true},
		{name: "500", err: &llm.APIError{Status: 500}, result: false},
		{name: "generic", err: fmt.Errorf("err"), result: false},
		{name: "nil", err: nil, result: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := llm.IsOverloaded(tt.err)
			if got != tt.result {
				t.Errorf("IsOverloaded() = %v, want %v", got, tt.result)
			}
		})
	}
}

func TestIsServerError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		result bool
	}{
		{name: "500", err: &llm.APIError{Status: 500}, result: true},
		{name: "502", err: &llm.APIError{Status: 502}, result: true},
		{name: "503", err: &llm.APIError{Status: 503}, result: true},
		{name: "599", err: &llm.APIError{Status: 599}, result: true},
		{name: "400", err: &llm.APIError{Status: 400}, result: false},
		{name: "600", err: &llm.APIError{Status: 600}, result: false},
		{name: "generic", err: fmt.Errorf("err"), result: false},
		{name: "nil", err: nil, result: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := llm.IsServerError(tt.err)
			if got != tt.result {
				t.Errorf("IsServerError() = %v, want %v", got, tt.result)
			}
		})
	}
}

func TestAPIError_Error(t *testing.T) {
	t.Parallel()

	err := &llm.APIError{Message: "something broke"}
	if err.Error() != "something broke" {
		t.Errorf("expected 'something broke', got %s", err.Error())
	}
}

func TestDefaultRetryConfig(t *testing.T) {
	t.Parallel()

	cfg := llm.DefaultRetryConfig()
	if cfg.MaxRetries != 10 {
		t.Errorf("expected MaxRetries 10, got %d", cfg.MaxRetries)
	}
	if cfg.BaseBackoff != 500*time.Millisecond {
		t.Errorf("expected BaseBackoff 500ms, got %v", cfg.BaseBackoff)
	}
	if cfg.MaxBackoff != 32*time.Second {
		t.Errorf("expected MaxBackoff 32s, got %v", cfg.MaxBackoff)
	}
}

func TestIsConnectionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		result bool
	}{
		{name: "connection refused", err: fmt.Errorf("connection refused"), result: true},
		{name: "connection reset", err: fmt.Errorf("connection reset by peer"), result: true},
		{name: "EOF", err: fmt.Errorf("unexpected EOF"), result: true},
		{name: "timeout", err: fmt.Errorf("dial timeout"), result: true},
		{name: "temporary", err: fmt.Errorf("temporary failure"), result: true},
		{name: "other", err: fmt.Errorf("some other error"), result: false},
		{name: "nil", err: nil, result: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := llm.IsConnectionError(tt.err)
			if got != tt.result {
				t.Errorf("isConnectionError() = %v, want %v", got, tt.result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SSE streaming integration tests using httptest.Server
// ---------------------------------------------------------------------------

func TestStream_Integration(t *testing.T) {
	sseData := []string{
		"event: message_start\n" + `data: {"message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","usage":{"input_tokens":10}}}` + "\n\n",
		"event: content_block_start\n" + `data: {"index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		"event: content_block_delta\n" + `data: {"index":0,"delta":{"type":"text_delta","text":"Hi there"}}` + "\n\n",
		"event: content_block_stop\n" + `data: {"index":0}` + "\n\n",
		"event: message_delta\n" + `data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n",
		"event: message_stop\n" + `data: {}` + "\n\n",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header 'test-key', got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version header")
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("expected Accept: text/event-stream header")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}

		for _, data := range sseData {
			_, _ = fmt.Fprint(w, data)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	expectedTypes := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if len(events) != len(expectedTypes) {
		t.Fatalf("expected %d events, got %d", len(expectedTypes), len(events))
	}

	for i, expected := range expectedTypes {
		if events[i].Type != expected {
			t.Errorf("event[%d]: expected type %s, got %s", i, expected, events[i].Type)
		}
	}

	// Verify message content
	if events[0].Message == nil || events[0].Message.ID != "msg_1" {
		t.Error("message_start event missing or wrong ID")
	}
	if events[2].Delta == nil || events[2].Delta.Text != "Hi there" {
		t.Error("content_block_delta missing or wrong text")
	}
	if events[4].DeltaMsg == nil || events[4].DeltaMsg.StopReason != "end_turn" {
		t.Error("message_delta missing or wrong stop_reason")
	}
}

func TestStream_NonRetryableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"invalid request"}}`)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	_, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("test")}},
		},
	})

	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestStream_RetryOn5xx(t *testing.T) {
	var mu sync.Mutex
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		count := callCount
		mu.Unlock()

		if count < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"server_error","message":"bad gateway"}}`)
			return
		}

		// Success on third attempt
		w.Header().Set("Content-Type", "text/event-stream")
_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"usage\":{\"input_tokens\":5}}}\n\n")
		_, _ = fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		_, _ = fmt.Fprintf(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		_, _ = fmt.Fprintf(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:      "test-key",
		BaseURL:     server.URL,
		Model:       "test-model",
		Timeout:     5 * time.Second,
		RetryConfig: &llm.RetryConfig{MaxRetries: 5, BaseBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("test")}},
		},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	if len(events) == 0 {
		t.Fatal("expected at least one event after retry")
	}

	mu.Lock()
	count := callCount
	mu.Unlock()
	if count < 3 {
		t.Errorf("expected at least 3 calls, got %d", count)
	}
}

func TestComplete_Integration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's not a streaming request
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if stream, ok := req["stream"].(bool); ok && stream {
			t.Error("expected stream=false for Complete()")
		}

		resp := llm.Response{
			ID:         "msg_complete",
			Type:       "message",
			Role:       "assistant",
			Model:      "test-model",
			StopReason: "end_turn",
			Usage:      types.Usage{InputTokens: 10, OutputTokens: 5},
			Content:    []types.ContentBlock{types.NewTextBlock("Hello from complete!")},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	resp, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if resp.ID != "msg_complete" {
		t.Errorf("expected ID msg_complete, got %s", resp.ID)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "Hello from complete!" {
		t.Errorf("unexpected content: %+v", resp.Content)
	}
}

func TestComplete_Non200Response(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "bad-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	_, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})

	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestParseAPIError_ValidJSON(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Too many requests"}}`)

	apiErr := p.ParseAPIError(body, 429)

	if apiErr.Status != 429 {
		t.Errorf("expected status 429, got %d", apiErr.Status)
	}
	if apiErr.Message != "Too many requests" {
		t.Errorf("expected 'Too many requests', got %s", apiErr.Message)
	}
	if !apiErr.Retryable {
		t.Error("expected retryable for 429")
	}
}

func TestParseAPIError_InvalidJSON(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	body := []byte(`not json at all`)

	apiErr := p.ParseAPIError(body, 500)

	if apiErr.Status != 500 {
		t.Errorf("expected status 500, got %d", apiErr.Status)
	}
	if apiErr.Message != "not json at all" {
		t.Errorf("expected raw body as message, got %s", apiErr.Message)
	}
}

func TestParseAPIError_EmptyErrorMessage(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	body := []byte(`{"type":"error","error":{"type":""," "message":""}}`)

	apiErr := p.ParseAPIError(body, 500)
	// When Message is empty, should fall back to string(body)
	if apiErr.Status != 500 {
		t.Errorf("expected status 500, got %d", apiErr.Status)
	}
}

func TestStream_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow response to allow context cancellation
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("test")}},
		},
	})

	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}

// ---------------------------------------------------------------------------
// SSE parsing edge case tests
// ---------------------------------------------------------------------------

func TestParseSSE_TrailingEventWithoutEmptyLine(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	// SSE data without trailing empty line
	sseInput := "event: message_stop\ndata: {}\n"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan llm.StreamEvent, 16)
	go func() {
		p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
		close(eventCh)
	}()

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "message_stop" {
		t.Errorf("expected message_stop, got %s", events[0].Type)
	}
}

func TestParseSSE_CommentsIgnored(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	sseInput := ": this is a comment\nevent: message_stop\ndata: {}\n\n"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan llm.StreamEvent, 16)
	go func() {
		p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
		close(eventCh)
	}()

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event (comment ignored), got %d", len(events))
	}
}

func TestParseSSE_MultipleDataLines(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	sseInput := "event: message_start\ndata: {\"message\":\ndata: {\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"usage\":{\"input_tokens\":5}}}\n\n"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan llm.StreamEvent, 16)
	go func() {
		p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
		close(eventCh)
	}()

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestStream_ConnectionError(t *testing.T) {
	// Use httptest mock instead of real port — no TCP timeout delays
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"server_error","message":"unavailable"}}`)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:      "test-key",
		BaseURL:     server.URL,
		Model:       "test-model",
		Timeout:     2 * time.Second,
		RetryConfig: &llm.RetryConfig{MaxRetries: 2, BaseBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("test")}},
		},
	})

	if err == nil {
		t.Fatal("expected error from connection failure")
	}
}

func TestParseSSE_ContextCancellation(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	// Use a string reader with complete SSE data, then cancel context during processing.
	// This tests that ParseSSE respects context cancellation between events.
	sseInput := "event: message_start\ndata: {\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"usage\":{\"input_tokens\":5}}}\n\n"

	ctx, cancel := context.WithCancel(context.Background())
	eventCh := make(chan llm.StreamEvent, 16)

	go func() {
		p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
		close(eventCh)
	}()

	// Receive the first event, then cancel
	<-eventCh
	cancel()

	// Drain remaining events (should be empty or just the last event)
	for range eventCh {
	}
}

func TestStream_ToolUseContentBlock(t *testing.T) {
	sseData := "event: message_start\ndata: {\"message\":{\"id\":\"msg_tu\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"usage\":{\"input_tokens\":10}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"bash\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"ls\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":20}}\n\n" +
		"event: message_stop\ndata: {}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, sseData)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	eventCh, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("run ls")}},
		},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	// Verify tool_use content block
	toolStartIdx := -1
	for i, evt := range events {
		if evt.Type == "content_block_start" && evt.ContentBlock != nil {
			toolStartIdx = i
		}
	}
	if toolStartIdx < 0 {
		t.Fatal("expected content_block_start with tool_use")
	}
	cb := events[toolStartIdx].ContentBlock
	if cb.Type != types.ContentTypeToolUse || cb.ID != "tu_1" || cb.Name != "bash" {
		t.Errorf("unexpected tool_use block: %+v", cb)
	}

	// Verify input_json_delta
	deltaIdx := -1
	for i, evt := range events {
		if evt.Type == "content_block_delta" && evt.Delta != nil && evt.Delta.Type == "input_json_delta" {
			deltaIdx = i
		}
	}
	if deltaIdx < 0 {
		t.Fatal("expected input_json_delta")
	}
	if events[deltaIdx].Delta.PartialJSON != `{"command":"ls"}` {
		t.Errorf("unexpected partial JSON: %s", events[deltaIdx].Delta.PartialJSON)
	}
}


// Benchmark for SSE parsing
func BenchmarkParseEvent(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":25}}}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.ParseEvent("message_start", data)
	}
}

func BenchmarkParseSSE(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	var buf strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&buf, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"word\"}}\n\n")
	}
	sseInput := buf.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		eventCh := make(chan llm.StreamEvent, 256)
		go func() {
			p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
			close(eventCh)
		}()
		for range eventCh {
		}
	}
}
