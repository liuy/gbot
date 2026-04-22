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

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
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

	// Verify defaults through behavior: Stream with a server that checks
	// headers and URL proves apiKey/model/baseURL were wired correctly.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key 'test-key', got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Authorization 'Bearer test-key', got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":"x","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0},"content":[]}`)
	}))
	defer server.Close()

	// Override to local server to verify fields are used
	p2 := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-20250514",
		BaseURL: server.URL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := p2.Complete(ctx, &llm.Request{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 10,
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", resp.Model, "claude-sonnet-4-20250514")
	}
}

func TestNewAnthropicProvider_CustomBaseURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the custom API key is sent
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key 'test-key', got %q", r.Header.Get("x-api-key"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":"x","type":"message","role":"assistant","model":"test-model","stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0},"content":[]}`)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 10 * time.Second,
	})

	if p == nil {
		t.Fatal("expected non-nil provider")
	}

	// Verify the custom BaseURL and Model are wired through by completing a request
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 10,
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Model != "test-model" {
		t.Errorf("Model = %q, want %q", resp.Model, "test-model")
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

func TestParseEvent_MessageDelta_InputTokens(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":500,"output_tokens":42}}`
	event := p.ParseEvent("message_delta", data)

	if event.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if event.Usage.InputTokens != 500 {
		t.Errorf("expected 500 input tokens, got %d", event.Usage.InputTokens)
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
		code  int
		retry bool
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
		name  string
		err   error
		retry bool
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
		name     string
		err      error
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

// ---------------------------------------------------------------------------
// Complete — error path tests
// ---------------------------------------------------------------------------

func TestComplete_SendRequestError(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:0", // port 0 = connection refused
		Model:   "test-model",
		Timeout: 1 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})

	if err == nil {
		t.Fatal("expected error from connection refused")
	}
	if !strings.Contains(err.Error(), "send request") {
		t.Errorf("expected 'send request' in error, got: %v", err)
	}
}

func TestComplete_ContextCanceled(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:0",
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})

	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestComplete_ReadBodyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write header then immediately close connection to cause read error
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		// Hijack the connection and close it mid-read
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Log("cannot hijack, skipping")
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
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
		t.Fatal("expected error from read body failure")
	}
	if !strings.Contains(err.Error(), "read response") {
		t.Errorf("expected 'read response' in error, got: %v", err)
	}
}

func TestComplete_UnmarshalResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "this is not valid json")
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
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
		t.Fatal("expected error from unmarshal failure")
	}
	if !strings.Contains(err.Error(), "unmarshal response") {
		t.Errorf("expected 'unmarshal response' in error, got: %v", err)
	}
}

func TestComplete_InvalidURL(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "http://\x00invalid", // invalid URL character
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
		t.Fatal("expected error from invalid URL")
	}
}

func TestComplete_MarshalError(t *testing.T) {
	t.Parallel()
	// Trigger json.Marshal error by passing invalid json.RawMessage in System field.
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:0",
		Model:   "test-model",
		Timeout: 1 * time.Second,
	})

	ctx := context.Background()
	_, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		System:    json.RawMessage{0xff}, // invalid JSON triggers marshal error
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})

	if err == nil {
		t.Fatal("expected error from marshal failure")
	}
	if !strings.Contains(err.Error(), "marshal request") {
		t.Errorf("expected 'marshal request' in error, got: %v", err)
	}
}

func TestStream_MarshalError(t *testing.T) {
	t.Parallel()
	// Trigger json.Marshal error by passing invalid json.RawMessage in System field.
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:0",
		Model:   "test-model",
		Timeout: 1 * time.Second,
	})

	ctx := context.Background()
	_, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		System:    json.RawMessage{0xff}, // invalid JSON triggers marshal error
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("test")}},
		},
	})

	if err == nil {
		t.Fatal("expected error from marshal failure")
	}
	if !strings.Contains(err.Error(), "marshal request") {
		t.Errorf("expected 'marshal request' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stream — additional error path tests
// ---------------------------------------------------------------------------

func TestStream_SendRequestNonConnectionError(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "http://\x00invalid",
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
		t.Fatal("expected error from invalid URL")
	}
	// Should NOT be a connection error — it's a URL parse error
	if strings.Contains(err.Error(), "connection") {
		t.Errorf("unexpected 'connection' in error for URL parse failure: %v", err)
	}
}

func TestStream_ConnectionRefusedRetry(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:0", // connection refused
		Model:   "test-model",
		Timeout: 1 * time.Second,
		RetryConfig: &llm.RetryConfig{
			MaxRetries:  2,
			BaseBackoff: 10 * time.Millisecond,
			MaxBackoff:  20 * time.Millisecond,
		},
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
		t.Fatal("expected error from max retries exceeded")
	}
	if !strings.Contains(err.Error(), "max retries exceeded") {
		t.Errorf("expected 'max retries exceeded' in error, got: %v", err)
	}
}

func TestStream_ContextCanceledDuringBackoff(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"server_error","message":"unavailable"}}`)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
		RetryConfig: &llm.RetryConfig{
			MaxRetries:  10,
			BaseBackoff: 500 * time.Millisecond,
			MaxBackoff:  2 * time.Second,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("test")}},
		},
	})

	if err == nil {
		t.Fatal("expected error from context cancellation during backoff")
	}
}

func TestStream_MaxRetriesExceeded(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"server_error","message":"bad gateway"}}`)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
		RetryConfig: &llm.RetryConfig{
			MaxRetries:  2,
			BaseBackoff: 10 * time.Millisecond,
			MaxBackoff:  20 * time.Millisecond,
		},
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
		t.Fatal("expected error from max retries exceeded")
	}
	if !strings.Contains(err.Error(), "max retries exceeded") {
		t.Errorf("expected 'max retries exceeded' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseSSE — additional edge case tests
// ---------------------------------------------------------------------------

func TestParseSSE_EmptyInput(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan llm.StreamEvent, 16)
	go func() {
		p.ParseSSE(ctx, strings.NewReader(""), eventCh)
		close(eventCh)
	}()

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	if len(events) != 0 {
		t.Errorf("expected 0 events for empty input, got %d", len(events))
	}
}

func TestParseSSE_OnlyEmptyLines(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan llm.StreamEvent, 16)
	go func() {
		p.ParseSSE(ctx, strings.NewReader("\n\n\n"), eventCh)
		close(eventCh)
	}()

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	if len(events) != 0 {
		t.Errorf("expected 0 events for only empty lines, got %d", len(events))
	}
}

func TestParseSSE_EventWithoutData(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	// Event type but no data — should not emit
	sseInput := "event: message_stop\n\n"

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

	// event type with no data = not emitted (eventData.Len() == 0)
	if len(events) != 0 {
		t.Errorf("expected 0 events for event without data, got %d", len(events))
	}
}

func TestParseSSE_DataWithoutEventType(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	// Data but no event type — should not emit
	sseInput := "data: {}\n\n"

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

	if len(events) != 0 {
		t.Errorf("expected 0 events for data without event type, got %d", len(events))
	}
}

func TestParseSSE_FullChannelCancellation(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	// Create many SSE events
	var buf strings.Builder
	for range 200 {
		buf.WriteString("event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
	}
	sseInput := buf.String()

	ctx, cancel := context.WithCancel(context.Background())

	// Unbuffered channel: every send blocks until we receive.
	// We read a few, then stop reading and cancel — ParseSSE will be
	// blocked on the send select and will exit via ctx.Done().
	eventCh := make(chan llm.StreamEvent)

	done := make(chan struct{})
	go func() {
		p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
		close(done)
	}()

	// Read a handful of events to let ParseSSE proceed
	for range 5 {
		<-eventCh
	}
	// Now cancel while ParseSSE is blocked trying to send the next event
	cancel()

	select {
	case <-done:
		// good — ParseSSE exited
	case <-time.After(5 * time.Second):
		t.Fatal("ParseSSE did not exit after context cancellation")
	}
}

func TestParseSSE_TrailingEventCancellation(t *testing.T) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	// SSE with no trailing empty line — tests the "last event" path
	sseInput := "event: message_stop\ndata: {}\n"

	ctx, cancel := context.WithCancel(context.Background())
	// Unbuffered channel: ParseSSE will block on the final-event send
	eventCh := make(chan llm.StreamEvent)

	unblock := make(chan struct{})
	go func() {
		p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
		close(unblock)
	}()

	// Don't read from eventCh — ParseSSE is blocked on the trailing event send.
	// Cancel the context so it exits via the ctx.Done() branch.
	// Wait a moment for the goroutine to reach the select.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-unblock:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("ParseSSE did not exit after context cancellation on trailing event")
	}
}

// ---------------------------------------------------------------------------
// ParseAPIError — additional edge case tests
// ---------------------------------------------------------------------------

func TestParseAPIError_EmptyBody(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	apiErr := p.ParseAPIError([]byte(""), 500)

	if apiErr.Status != 500 {
		t.Errorf("expected status 500, got %d", apiErr.Status)
	}
	if apiErr.Message != "" {
		t.Errorf("expected empty message for empty body, got %s", apiErr.Message)
	}
	if !apiErr.Retryable {
		t.Error("expected retryable for 500")
	}
}

func TestParseAPIError_ValidJSONEmptyMessage(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	// JSON parses but error.message is empty — should fall back to string(body)
	body := `{"type":"error","error":{"type":"bad_request","message":""}}`
	apiErr := p.ParseAPIError([]byte(body), 400)

	if apiErr.Status != 400 {
		t.Errorf("expected status 400, got %d", apiErr.Status)
	}
	if apiErr.Message != body {
		t.Errorf("expected fallback to raw body, got %s", apiErr.Message)
	}
}

func TestParseAPIError_NonRetryableStatus(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	body := `{"type":"error","error":{"type":"auth_error","message":"Unauthorized"}}`
	apiErr := p.ParseAPIError([]byte(body), 401)

	if apiErr.Retryable {
		t.Error("expected non-retryable for 401")
	}
	if apiErr.Type != "error" {
		t.Errorf("expected type 'error', got %s", apiErr.Type)
	}
}

func TestParseAPIError_NilBody(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	apiErr := p.ParseAPIError(nil, 502)

	if apiErr.Status != 502 {
		t.Errorf("expected status 502, got %d", apiErr.Status)
	}
	if !apiErr.Retryable {
		t.Error("expected retryable for 502")
	}
}

// ---------------------------------------------------------------------------
// NewAnthropicProvider — edge cases
// ---------------------------------------------------------------------------

func TestNewAnthropicProvider_WithRetryConfig(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey: "test-key",
		Model:  "test-model",
		RetryConfig: &llm.RetryConfig{
			MaxRetries:  3,
			BaseBackoff: 100 * time.Millisecond,
			MaxBackoff:  5 * time.Second,
		},
	})

	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewAnthropicProvider_TrailingSlashTrimmed(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "https://api.example.com///",
		Model:   "test-model",
	})

	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

// ---------------------------------------------------------------------------
// ParseEvent — invalid JSON for remaining event types
// ---------------------------------------------------------------------------

func TestParseEvent_InvalidJSON_ContentBlockStart(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("content_block_start", "not valid json")

	if event.ContentBlock != nil {
		t.Error("expected nil ContentBlock for invalid JSON")
	}
}

func TestParseEvent_InvalidJSON_ContentBlockDelta(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("content_block_delta", "not valid json")

	if event.Delta != nil {
		t.Error("expected nil Delta for invalid JSON")
	}
}

func TestParseEvent_InvalidJSON_ContentBlockStop(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("content_block_stop", "not valid json")

	// Index should be 0 (default) when JSON fails
	if event.Index != 0 {
		t.Errorf("expected index 0 for invalid JSON, got %d", event.Index)
	}
}

func TestParseEvent_InvalidJSON_MessageDelta(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("message_delta", "not valid json")

	if event.DeltaMsg != nil {
		t.Error("expected nil DeltaMsg for invalid JSON")
	}
	if event.Usage != nil {
		t.Error("expected nil Usage for invalid JSON")
	}
}

func TestParseEvent_InvalidJSON_Error(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	event := p.ParseEvent("error", "not valid json")

	if event.Error != nil {
		t.Error("expected nil Error for invalid JSON")
	}
}

func TestParseEvent_ThinkingDelta(t *testing.T) {
	t.Parallel()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}`
	event := p.ParseEvent("content_block_delta", data)

	if event.Delta == nil {
		t.Fatal("expected non-nil Delta")
	}
	if event.Delta.Thinking != "Let me think..." {
		t.Errorf("expected thinking 'Let me think...', got %s", event.Delta.Thinking)
	}
	if event.Delta.Type != "thinking_delta" {
		t.Errorf("expected type 'thinking_delta', got %s", event.Delta.Type)
	}
}

// ---------------------------------------------------------------------------
// setHeaders verification
// ---------------------------------------------------------------------------

func TestSetHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check all expected headers
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("x-api-key") != "my-api-key" {
			t.Errorf("expected x-api-key 'my-api-key', got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("Authorization") != "Bearer my-api-key" {
			t.Errorf("expected Authorization 'Bearer my-api-key', got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version '2023-06-01', got %s", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("anthropic-dangerous-direct-browser-access") != "true" {
			t.Errorf("expected anthropic-dangerous-direct-browser-access 'true', got %s", r.Header.Get("anthropic-dangerous-direct-browser-access"))
		}

		resp := llm.Response{
			ID:         "msg_headers",
			Type:       "message",
			Role:       "assistant",
			Model:      "test-model",
			StopReason: "end_turn",
			Content:    []types.ContentBlock{types.NewTextBlock("ok")},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "my-api-key",
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
	if resp.ID != "msg_headers" {
		t.Errorf("expected ID msg_headers, got %s", resp.ID)
	}
}

// ---------------------------------------------------------------------------
// Stream — response body close verification
// ---------------------------------------------------------------------------

func TestStream_BodyClosedOnError(t *testing.T) {
	// Verifies that Stream returns an error for non-2xx responses.
	// Body closure is handled by the HTTP client and cannot be directly
	// observed from outside the package without wrapping http.Response.Body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
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

// TestStream_RedirectTriggersGetBody verifies that when the server redirects,
// the HTTP client calls GetBody to rewind the request body.
func TestStream_RedirectTriggersGetBody(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"usage\":{\"input_tokens\":5}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer target.Close()

	// Redirect server: 307 redirect to the target
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: redirector.URL,
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eventCh, err := p.Stream(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("test")}},
		},
	})
	if err != nil {
		t.Fatalf("Stream() error after redirect: %v", err)
	}

	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	if len(events) == 0 {
		t.Fatal("expected events after redirect")
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
	for range 100 {
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

// ---------------------------------------------------------------------------
// Cache control and prompt cache break detection tests (US-003)
// ---------------------------------------------------------------------------

func TestAnthropicRequest_CacheControlHeader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the prompt caching beta header is present
		if got := r.Header.Get("anthropic-beta"); got != "prompt-caching-2024-10-22" {
			t.Errorf("expected anthropic-beta 'prompt-caching-2024-10-22', got %q", got)
		}
		resp := llm.Response{
			ID:         "msg_cache_hdr",
			Type:       "message",
			Role:       "assistant",
			Model:      "test-model",
			StopReason: "end_turn",
			Content:    []types.ContentBlock{types.NewTextBlock("ok")},
		}
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
		CacheControl: &types.CacheControlConfig{
			Type: "ephemeral",
		},
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "msg_cache_hdr" {
		t.Errorf("expected ID msg_cache_hdr, got %s", resp.ID)
	}
}

func TestAnthropicRequest_NoBetaHeaderWithoutCacheControl(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the prompt caching beta header is NOT present when CacheControl is nil
		if got := r.Header.Get("anthropic-beta"); got != "" {
			t.Errorf("expected no anthropic-beta header, got %q", got)
		}
		resp := llm.Response{
			ID:         "msg_no_beta",
			Type:       "message",
			Role:       "assistant",
			Model:      "test-model",
			StopReason: "end_turn",
			Content:    []types.ContentBlock{types.NewTextBlock("ok")},
		}
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
		// No CacheControl — beta header should NOT be sent
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "msg_no_beta" {
		t.Errorf("expected ID msg_no_beta, got %s", resp.ID)
	}
}

func TestAnthropicRequest_SystemWithCacheControl(t *testing.T) {
	t.Parallel()

	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)

		resp := llm.Response{
			ID:         "msg_cc",
			Type:       "message",
			Role:       "assistant",
			Model:      "test-model",
			StopReason: "end_turn",
			Content:    []types.ContentBlock{types.NewTextBlock("ok")},
		}
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
	_, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		CacheControl: &types.CacheControlConfig{
			Type: "ephemeral",
			TTL:  "1h",
		},
		SystemBlocks: []llm.SystemBlockParam{
			{Type: "text", Text: "You are a helpful assistant."},
			{Type: "text", Text: "Additional context."},
		},
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	// Verify the system field was serialized as an array with cache_control on the last block
	var req map[string]any
	if err := json.Unmarshal(receivedBody, &req); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	systemArr, ok := req["system"].([]any)
	if !ok {
		t.Fatal("expected system to be an array")
	}
	if len(systemArr) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(systemArr))
	}

	// First block: no cache_control
	first := systemArr[0].(map[string]any)
	if _, hasCC := first["cache_control"]; hasCC {
		t.Error("first block should NOT have cache_control")
	}
	if first["text"] != "You are a helpful assistant." {
		t.Errorf("first block text = %q, want 'You are a helpful assistant.'", first["text"])
	}

	// Second block: has cache_control
	second := systemArr[1].(map[string]any)
	cc, hasCC := second["cache_control"]
	if !hasCC {
		t.Fatal("second block should have cache_control")
	}
	ccMap := cc.(map[string]any)
	if ccMap["type"] != "ephemeral" {
		t.Errorf("cache_control type = %q, want 'ephemeral'", ccMap["type"])
	}
}

func TestAnthropicComplete_CacheTokensParsed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := llm.Response{
			ID:         "msg_tokens",
			Type:       "message",
			Role:       "assistant",
			Model:      "test-model",
			StopReason: "end_turn",
			Usage: types.Usage{
				InputTokens:              100,
				OutputTokens:             50,
				CacheCreationInputTokens: 500,
				CacheReadInputTokens:     10000,
			},
			Content: []types.ContentBlock{types.NewTextBlock("ok")},
		}
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

	if resp.Usage.CacheReadInputTokens != 10000 {
		t.Errorf("CacheReadInputTokens = %d, want 10000", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.CacheCreationInputTokens != 500 {
		t.Errorf("CacheCreationInputTokens = %d, want 500", resp.Usage.CacheCreationInputTokens)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", resp.Usage.OutputTokens)
	}
}

func TestAnthropicComplete_CacheBreakDetection(t *testing.T) {
	t.Parallel()

	// Reset break detection state
	llm.ResetPromptCacheBreakDetection()

	key := llm.PromptStateKey{
		QuerySource: "repl_main_thread",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := llm.Response{
			ID:         "msg_break",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-sonnet-4-20250514",
			StopReason: "end_turn",
			Usage: types.Usage{
				InputTokens:          100,
				OutputTokens:         50,
				CacheReadInputTokens: 10000,
			},
			Content: []types.ContentBlock{types.NewTextBlock("ok")},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "claude-sonnet-4-20250514",
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()

	// First call — sets baseline
	_, err := p.Complete(ctx, &llm.Request{
		Model:          "claude-sonnet-4-20250514",
		MaxTokens:      1024,
		PromptStateKey: &key,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() first call error: %v", err)
	}

	// Second call — should not crash even with same tokens
	_, err = p.Complete(ctx, &llm.Request{
		Model:          "claude-sonnet-4-20250514",
		MaxTokens:      1024,
		PromptStateKey: &key,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() second call error: %v", err)
	}

	llm.ResetPromptCacheBreakDetection()
}

func TestAnthropicStream_CacheTokensAccumulated(t *testing.T) {
	t.Parallel()

	llm.ResetPromptCacheBreakDetection()

	key := llm.PromptStateKey{
		QuerySource: "repl_main_thread",
	}

	sseData := []string{
		"event: message_start\n" + `data: {"message":{"id":"msg_s","type":"message","role":"assistant","model":"test","usage":{"input_tokens":10,"cache_read_input_tokens":8000,"cache_creation_input_tokens":200}}}` + "\n\n",
		"event: content_block_start\n" + `data: {"index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		"event: content_block_delta\n" + `data: {"index":0,"delta":{"type":"text_delta","text":"Hi"}}` + "\n\n",
		"event: content_block_stop\n" + `data: {"index":0}` + "\n\n",
		"event: message_delta\n" + `data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"cache_read_input_tokens":2000}}` + "\n\n",
		"event: message_stop\n" + `data: {}` + "\n\n",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
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

	ctx := context.Background()
	eventCh, err := p.Stream(ctx, &llm.Request{
		Model:          "test-model",
		MaxTokens:      1024,
		PromptStateKey: &key,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	// Drain all events
	var events []llm.StreamEvent
	for evt := range eventCh {
		events = append(events, evt)
	}

	// Verify message_start has cache tokens
	msgStart := events[0]
	if msgStart.Message == nil {
		t.Fatal("expected message_start event")
	}
	if msgStart.Message.Usage.CacheReadInputTokens != 8000 {
		t.Errorf("message_start CacheReadInputTokens = %d, want 8000", msgStart.Message.Usage.CacheReadInputTokens)
	}
	if msgStart.Message.Usage.CacheCreationInputTokens != 200 {
		t.Errorf("message_start CacheCreationInputTokens = %d, want 200", msgStart.Message.Usage.CacheCreationInputTokens)
	}

	// Verify message_delta has cache tokens
	msgDelta := events[4]
	if msgDelta.Usage == nil {
		t.Fatal("expected usage in message_delta")
	}
	if msgDelta.Usage.CacheReadInputTokens != 2000 {
		t.Errorf("message_delta CacheReadInputTokens = %d, want 2000", msgDelta.Usage.CacheReadInputTokens)
	}

	llm.ResetPromptCacheBreakDetection()
}

func TestAnthropicComplete_NoCacheControl_SystemUnchanged(t *testing.T) {
	t.Parallel()

	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		resp := llm.Response{
			ID:         "msg_no_cc",
			Type:       "message",
			Role:       "assistant",
			Model:      "test-model",
			StopReason: "end_turn",
			Content:    []types.ContentBlock{types.NewTextBlock("ok")},
		}
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
	_, err := p.Complete(ctx, &llm.Request{
		Model:     "test-model",
		MaxTokens: 1024,
		SystemBlocks: []llm.SystemBlockParam{
			{Type: "text", Text: "You are a helpful assistant."},
		},
		// CacheControl is nil — system blocks should NOT be serialized as system field
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	// When CacheControl is nil, SystemBlocks should not override System
	var req map[string]any
	if err := json.Unmarshal(receivedBody, &req); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if _, hasSystem := req["system"]; hasSystem {
		t.Error("expected no system field when CacheControl is nil and System is empty")
	}
}

func TestAnthropicComplete_CacheBreakWithSystemBlocks(t *testing.T) {
	t.Parallel()

	llm.ResetPromptCacheBreakDetection()

	key := llm.PromptStateKey{
		QuerySource: "repl_main_thread",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := llm.Response{
			ID:         "msg_sb",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-sonnet-4-20250514",
			StopReason: "end_turn",
			Usage: types.Usage{
				InputTokens:          100,
				OutputTokens:         50,
				CacheReadInputTokens: 10000,
			},
			Content: []types.ContentBlock{types.NewTextBlock("ok")},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "claude-sonnet-4-20250514",
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()

	// Call with SystemBlocks and CacheControl
	_, err := p.Complete(ctx, &llm.Request{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		CacheControl: &types.CacheControlConfig{
			Type: "ephemeral",
			TTL:  "1h",
		},
		SystemBlocks: []llm.SystemBlockParam{
			{Type: "text", Text: "System prompt v1"},
		},
		PromptStateKey: &key,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	// Second call with changed system — break detection should work
	_, err = p.Complete(ctx, &llm.Request{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		CacheControl: &types.CacheControlConfig{
			Type: "ephemeral",
			TTL:  "1h",
		},
		SystemBlocks: []llm.SystemBlockParam{
			{Type: "text", Text: "System prompt v2 - changed"},
		},
		PromptStateKey: &key,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	})
	if err != nil {
		t.Fatalf("Complete() second call error: %v", err)
	}

	llm.ResetPromptCacheBreakDetection()
}

func TestRequestToSystemMaps_SystemBlocks(t *testing.T) {
	t.Parallel()

	req := &llm.Request{
		SystemBlocks: []llm.SystemBlockParam{
			{Type: "text", Text: "Block 1"},
			{Type: "text", Text: "Block 2", CacheControl: &types.CacheControlConfig{Type: "ephemeral"}},
		},
	}

	result := llm.RequestToSystemMaps(req)
	if len(result) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(result))
	}
	if result[0]["text"] != "Block 1" {
		t.Errorf("block 0 text = %q, want 'Block 1'", result[0]["text"])
	}
	if _, hasCC := result[0]["cache_control"]; hasCC {
		t.Error("block 0 should not have cache_control")
	}
	if result[1]["text"] != "Block 2" {
		t.Errorf("block 1 text = %q, want 'Block 2'", result[1]["text"])
	}
	cc := result[1]["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control type = %q, want 'ephemeral'", cc["type"])
	}
}

func TestRequestToSystemMaps_RawSystemString(t *testing.T) {
	t.Parallel()

	req := &llm.Request{
		System: json.RawMessage(`"raw system string"`),
	}

	result := llm.RequestToSystemMaps(req)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	if result[0]["type"] != "text" {
		t.Errorf("block type = %q, want 'text'", result[0]["type"])
	}
	if result[0]["text"] != "raw system string" {
		t.Errorf("block text = %q, want 'raw system string'", result[0]["text"])
	}
}

func TestRequestToSystemMaps_RawSystemArray(t *testing.T) {
	t.Parallel()

	req := &llm.Request{
		System: json.RawMessage(`[{"type":"text","text":"hello"}]`),
	}

	result := llm.RequestToSystemMaps(req)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	if result[0]["text"] != "hello" {
		t.Errorf("block text = %q, want 'hello'", result[0]["text"])
	}
}

func TestRequestToSystemMaps_Empty(t *testing.T) {
	t.Parallel()

	req := &llm.Request{}
	result := llm.RequestToSystemMaps(req)
	if result != nil {
		t.Errorf("expected nil for empty request, got %v", result)
	}
}

func TestRequestToToolMaps(t *testing.T) {
	t.Parallel()

	req := &llm.Request{
		Tools: []llm.ToolDef{
			{Name: "read_file", Description: "Read a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "bash", Description: "Run command", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}

	result := llm.RequestToToolMaps(req)
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}
	if result[0]["name"] != "read_file" {
		t.Errorf("tool 0 name = %q, want 'read_file'", result[0]["name"])
	}
	if result[1]["name"] != "bash" {
		t.Errorf("tool 1 name = %q, want 'bash'", result[1]["name"])
	}
}

func TestRequestToToolMaps_Empty(t *testing.T) {
	t.Parallel()

	req := &llm.Request{}
	result := llm.RequestToToolMaps(req)
	if result != nil {
		t.Errorf("expected nil for empty tools, got %v", result)
	}
}
