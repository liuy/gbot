package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestOpenAIServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

func newOpenAIProviderWithServer(server *httptest.Server) *llm.OpenAIProvider {
	return llm.NewOpenAIProvider(&llm.OpenAIConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "gpt-4",
	})
}

// defaultTestRequest returns a minimal valid request for testing.
func defaultTestRequest() *llm.Request {
	return &llm.Request{
		Model:     "gpt-4",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		},
	}
}

// ---------------------------------------------------------------------------
// Complete — success paths
// ---------------------------------------------------------------------------

func TestOpenAIComplete_TextResponse(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Authorization 'Bearer test-key', got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type 'application/json', got %q", r.Header.Get("Content-Type"))
		}

		// Verify request body contains stream=false
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if stream, ok := reqBody["stream"].(bool); ok && stream {
			t.Error("expected stream=false for Complete()")
		}
		if model, _ := reqBody["model"].(string); model != "gpt-4" {
			t.Errorf("expected model 'gpt-4', got %q", model)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "chatcmpl-123",
			"model": "gpt-4",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5}
		}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	// Verify every field of the response
	if resp.ID != "chatcmpl-123" {
		t.Errorf("ID = %q, want %q", resp.ID, "chatcmpl-123")
	}
	if resp.Type != "message" {
		t.Errorf("Type = %q, want %q", resp.Type, "message")
	}
	if resp.Role != "assistant" {
		t.Errorf("Role = %q, want %q", resp.Role, "assistant")
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Type != types.ContentTypeText {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, types.ContentTypeText)
	}
	if resp.Content[0].Text != "Hello!" {
		t.Errorf("Content[0].Text = %q, want %q", resp.Content[0].Text, "Hello!")
	}
	if resp.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", resp.Model, "gpt-4")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end_turn")
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("Usage.InputTokens = %d, want 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("Usage.OutputTokens = %d, want 5", resp.Usage.OutputTokens)
	}
}

func TestOpenAIComplete_ToolCallResponse(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "chatcmpl-456",
			"model": "gpt-4",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [{"id": "call_abc", "type": "function", "function": {"name": "bash", "arguments": "{\"command\":\"ls\"}"}}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 20, "completion_tokens": 10}
		}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	// Verify response metadata
	if resp.ID != "chatcmpl-456" {
		t.Errorf("ID = %q, want %q", resp.ID, "chatcmpl-456")
	}
	if resp.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", resp.Model, "gpt-4")
	}
	// "tool_calls" finish_reason maps to "tool_use"
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "tool_use")
	}
	if resp.Usage.InputTokens != 20 {
		t.Errorf("Usage.InputTokens = %d, want 20", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 10 {
		t.Errorf("Usage.OutputTokens = %d, want 10", resp.Usage.OutputTokens)
	}

	// Verify tool_use content block
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	cb := resp.Content[0]
	if cb.Type != types.ContentTypeToolUse {
		t.Errorf("Content[0].Type = %q, want %q", cb.Type, types.ContentTypeToolUse)
	}
	if cb.ID != "call_abc" {
		t.Errorf("Content[0].ID = %q, want %q", cb.ID, "call_abc")
	}
	if cb.Name != "bash" {
		t.Errorf("Content[0].Name = %q, want %q", cb.Name, "bash")
	}
	if string(cb.Input) != `{"command":"ls"}` {
		t.Errorf("Content[0].Input = %q, want %q", string(cb.Input), `{"command":"ls"}`)
	}
}

// ---------------------------------------------------------------------------
// Complete — error paths
// ---------------------------------------------------------------------------

func TestOpenAIComplete_ErrorResponse(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"Rate limit exceeded","code":"rate_limit_exceeded"}}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err == nil {
		t.Fatal("expected error for 429 response, got nil")
	}
	if !strings.Contains(err.Error(), "Rate limit exceeded") {
		t.Errorf("error should mention 'Rate limit exceeded', got: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on error, got %+v", resp)
	}

	var apiErr *llm.APIError
	if !isAPIError(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != 429 {
		t.Errorf("Status = %d, want 429", apiErr.Status)
	}
	if !apiErr.Retryable {
		t.Error("Retryable = false, want true for 429")
	}
	if apiErr.Message != "Rate limit exceeded" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "Rate limit exceeded")
	}
}

func TestOpenAIComplete_ContextLengthExceeded(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"too long"}}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err == nil {
		t.Fatal("expected error for context_length_exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error should mention 'too long', got: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on error, got %+v", resp)
	}

	var apiErr *llm.APIError
	if !isAPIError(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.ErrorCode != "prompt_too_long" {
		t.Errorf("ErrorCode = %q, want %q", apiErr.ErrorCode, "prompt_too_long")
	}
	if apiErr.Retryable {
		t.Error("Retryable = true, want false for context_length_exceeded")
	}
	if apiErr.Type != "prompt_too_long" {
		t.Errorf("Type = %q, want %q", apiErr.Type, "prompt_too_long")
	}
	if apiErr.Status != 400 {
		t.Errorf("Status = %d, want 400", apiErr.Status)
	}
}

func TestOpenAIComplete_ServerError(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"type":"server_error","message":"Internal server error"}}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("error should mention 'Internal server error', got: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on error, got %+v", resp)
	}

	var apiErr *llm.APIError
	if !isAPIError(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != 500 {
		t.Errorf("Status = %d, want 500", apiErr.Status)
	}
	if !apiErr.Retryable {
		t.Error("Retryable = false, want true for 500")
	}
	if apiErr.Message != "Internal server error" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "Internal server error")
	}
}

func TestOpenAIComplete_NonRetryableError(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"Invalid API key","code":"invalid_api_key"}}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error should mention 'Invalid API key', got: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on error, got %+v", resp)
	}

	var apiErr *llm.APIError
	if !isAPIError(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != 401 {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
	if apiErr.Retryable {
		t.Error("Retryable = true, want false for 401")
	}
	if apiErr.Message != "Invalid API key" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "Invalid API key")
	}
}

func TestOpenAIComplete_InvalidJSON(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `this is not valid json at all`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err == nil {
		t.Fatal("expected error for invalid JSON response, got nil")
	}
	if resp != nil {
		t.Errorf("expected nil response on error, got %+v", resp)
	}
	if !strings.Contains(err.Error(), "unmarshal response") {
		t.Errorf("expected 'unmarshal response' in error, got: %v", err)
	}
}

func TestOpenAIComplete_ConnectionError(t *testing.T) {
	// Create a server and immediately close it so connections fail
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := provider.Complete(ctx, defaultTestRequest())
	if err == nil {
		t.Fatal("expected error from closed server, got nil")
	}
	if resp != nil {
		t.Errorf("expected nil response on error, got %+v", resp)
	}
	if !strings.Contains(err.Error(), "send request") {
		t.Errorf("expected 'send request' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseAPIError — edge cases (OpenAI-specific)
// ---------------------------------------------------------------------------

func TestParseAPIError_InvalidBody(t *testing.T) {
	provider := llm.NewOpenAIProvider(&llm.OpenAIConfig{APIKey: "key", Model: "m"})

	apiErr := provider.ParseAPIError([]byte(`not json at all`), 500)

	if apiErr.Status != 500 {
		t.Errorf("Status = %d, want 500", apiErr.Status)
	}
	if apiErr.Message != "not json at all" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "not json at all")
	}
	if !apiErr.Retryable {
		t.Error("Retryable = false, want true for 500")
	}
}

func TestParseAPIError_EmptyMessage(t *testing.T) {
	provider := llm.NewOpenAIProvider(&llm.OpenAIConfig{APIKey: "key", Model: "m"})

	body := `{"error":{"code":"unknown","message":"","type":"server_error"}}`
	apiErr := provider.ParseAPIError([]byte(body), 500)

	if apiErr.Status != 500 {
		t.Errorf("Status = %d, want 500", apiErr.Status)
	}
	// When message is empty, should fall back to raw body string
	if apiErr.Message != body {
		t.Errorf("Message = %q, want %q (raw body fallback)", apiErr.Message, body)
	}
	if !apiErr.Retryable {
		t.Error("Retryable = false, want true for 500")
	}
}

// ---------------------------------------------------------------------------
// CalculateBackoffWithRetryAfter
// ---------------------------------------------------------------------------

func TestProvider_CalculateBackoffWithRetryAfter(t *testing.T) {
	cfg := llm.DefaultRetryConfig()

	t.Run("returns value within base range when RetryAfter is zero", func(t *testing.T) {
		// With RetryAfter=0, the function should return a normal base backoff.
		// Base at attempt 0 = 500ms * 2^0 * jitter = 250-750ms.
		result := llm.CalculateBackoffWithRetryAfter(0, cfg, 0)
		if result <= 0 {
			t.Errorf("expected positive backoff, got %v", result)
		}
		if result > cfg.MaxBackoff {
			t.Errorf("backoff %v exceeds max %v", result, cfg.MaxBackoff)
		}
	})

	t.Run("uses RetryAfter when larger than base backoff", func(t *testing.T) {
		retryAfter := 30 * time.Second
		result := llm.CalculateBackoffWithRetryAfter(0, cfg, retryAfter)
		// Base backoff at attempt 0 is 250-750ms, 30s is much larger => should return 30s
		if result != retryAfter {
			t.Errorf("expected RetryAfter %v, got %v", retryAfter, result)
		}
	})

	t.Run("returns value smaller than RetryAfter when RetryAfter is small", func(t *testing.T) {
		// With a tiny RetryAfter (1ms) at attempt 5, base backoff (~8-32s) dominates.
		retryAfter := 1 * time.Millisecond
		result := llm.CalculateBackoffWithRetryAfter(5, cfg, retryAfter)
		// The result is the base backoff (1ms is too small to override)
		if result <= retryAfter {
			t.Errorf("expected backoff larger than RetryAfter %v, got %v", retryAfter, result)
		}
	})

	t.Run("uses base backoff when RetryAfter is smaller than base", func(t *testing.T) {
		// Use a very large RetryAfter that definitely exceeds base backoff
		retryAfter := 60 * time.Second
		result := llm.CalculateBackoffWithRetryAfter(0, cfg, retryAfter)
		if result != retryAfter {
			t.Errorf("expected RetryAfter %v to override base, got %v", retryAfter, result)
		}
	})
}

// ---------------------------------------------------------------------------
// Stream tests (httptest.Server-based)
// ---------------------------------------------------------------------------

func TestOpenAIStream_Success(t *testing.T) {
	callCount := 0
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing Authorization header")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
	if len(events) < 3 {
		t.Fatalf("expected >= 3 events, got %d", len(events))
	}
	if events[0].Type != "message_start" {
		t.Errorf("first event = %q, want message_start", events[0].Type)
	}
	last := events[len(events)-1]
	if last.Type != "message_stop" {
		t.Errorf("last event = %q, want message_stop", last.Type)
	}
}

func TestOpenAIStream_RetryOn429(t *testing.T) {
	callCount := 0
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	if callCount != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", callCount)
	}
	if len(events) == 0 {
		t.Fatal("expected events after retry")
	}
}

func TestOpenAIStream_NonRetryableError(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"authentication_error"}}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	_, err := provider.Stream(ctx, req)
	if err == nil {
		t.Fatal("expected error for 401")
	}

	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != 401 {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
	if apiErr.Retryable {
		t.Error("Retryable should be false for 401")
	}
}

func TestOpenAIStream_ContextCancellation(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"message":"internal error","type":"server_error"}}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	_, err := provider.Stream(ctx, req)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error should mention 'context deadline exceeded', got: %v", err)
	}
}

func TestOpenAIComplete_NoChoices(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":"chatcmpl-1","model":"gpt-4","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}`)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	_, err := provider.Complete(ctx, req)
	if err == nil {
		t.Fatal("expected error for no choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error = %q, want 'no choices'", err.Error())
	}
}

func TestOpenAIComplete_ToolCallAndText(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := "Here's the result"
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"id":"chatcmpl-789","model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":%q,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`, content)
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	resp, err := provider.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	// Should have both text and tool_use content blocks
	if len(resp.Content) != 2 {
		t.Fatalf("len(Content) = %d, want 2", len(resp.Content))
	}
	if resp.Content[0].Type != types.ContentTypeText {
		t.Errorf("Content[0].Type = %q, want text", resp.Content[0].Type)
	}
	if resp.Content[0].Text != "Here's the result" {
		t.Errorf("Content[0].Text = %q, want result text", resp.Content[0].Text)
	}
	if resp.Content[1].Type != types.ContentTypeToolUse {
		t.Errorf("Content[1].Type = %q, want tool_use", resp.Content[1].Type)
	}
	if resp.Content[1].Name != "bash" {
		t.Errorf("Content[1].Name = %q, want bash", resp.Content[1].Name)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("Usage.InputTokens = %d, want 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 20 {
		t.Errorf("Usage.OutputTokens = %d, want 20", resp.Usage.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Helper: extract *APIError from error
// ---------------------------------------------------------------------------

// isAPIError attempts to extract *llm.APIError from err via errors.As.
// Returns true if successful and populates target.
func isAPIError(err error, target **llm.APIError) bool {
	return errors.As(err, target)
}

func TestParseAPIError_RateLimitMapping(t *testing.T) {
	t.Parallel()

	p := llm.NewOpenAIProvider(&llm.OpenAIConfig{APIKey: "test"})
	apiErr := p.ParseAPIError([]byte(`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error","code":"rate_limit_exceeded"}}`), 429)

	if apiErr.ErrorCode != "rate_limit_error" {
		t.Errorf("ErrorCode = %q, want %q", apiErr.ErrorCode, "rate_limit_error")
	}
	if apiErr.Type != "rate_limit_error" {
		t.Errorf("Type = %q, want %q", apiErr.Type, "rate_limit_error")
	}
	if !apiErr.Retryable {
		t.Error("429 should be retryable")
	}
}

func TestParseAPIError_AuthMapping(t *testing.T) {
	t.Parallel()

	p := llm.NewOpenAIProvider(&llm.OpenAIConfig{APIKey: "test"})
	apiErr := p.ParseAPIError([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`), 401)

	if apiErr.ErrorCode != "authentication_error" {
		t.Errorf("ErrorCode = %q, want %q", apiErr.ErrorCode, "authentication_error")
	}
	if apiErr.Retryable {
		t.Error("401 should not be retryable")
	}
}

func TestParseAPIError_ForbiddenMapping(t *testing.T) {
	t.Parallel()

	p := llm.NewOpenAIProvider(&llm.OpenAIConfig{APIKey: "test"})
	apiErr := p.ParseAPIError([]byte(`{"error":{"message":"Forbidden","type":"forbidden"}}`), 403)

	if apiErr.ErrorCode != "permission_error" {
		t.Errorf("ErrorCode = %q, want %q", apiErr.ErrorCode, "permission_error")
	}
	if apiErr.Retryable {
		t.Error("403 should not be retryable")
	}
}

func TestParseAPIError_ServerErrorMapping(t *testing.T) {
	t.Parallel()

	p := llm.NewOpenAIProvider(&llm.OpenAIConfig{APIKey: "test"})
	apiErr := p.ParseAPIError([]byte(`{"error":{"message":"Internal server error","type":"server_error"}}`), 500)

	if apiErr.ErrorCode != "api_error" {
		t.Errorf("ErrorCode = %q, want %q", apiErr.ErrorCode, "api_error")
	}
	if !apiErr.Retryable {
		t.Error("500 should be retryable")
	}
}

// TestOpenAIStream_ReasoningContent tests the GLM reasoning_content (thinking) delta handling.
func TestOpenAIStream_ReasoningContent(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Send reasoning_content first (thinking block)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"glm-4\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"Let me think\"},\"finish_reason\":null}]}\n\n")
		// Then content (should close thinking block, open text block)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"glm-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Answer\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "glm-4", MaxTokens: 100}
	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	// Should have: message_start, thinking_start, thinking_delta, thinking_stop,
	// text_start, text_delta, text_end, message_delta, message_stop
	var gotThinkingStart, gotThinkingDelta bool
	var gotTextStart, gotTextDelta bool
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			if e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeThinking {
				gotThinkingStart = true
			}
			if e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeText {
				gotTextStart = true
			}
		case "content_block_delta":
			if e.Delta != nil {
				switch e.Delta.Type {
				case "thinking_delta":
					gotThinkingDelta = true
					if e.Delta.Thinking != "Let me think" {
						t.Errorf("thinking text = %q, want %q", e.Delta.Thinking, "Let me think")
					}
				case "text_delta":
					gotTextDelta = true
				}
			}
		}
	}
	if !gotThinkingStart {
		t.Error("missing thinking_start event")
	}
	if !gotThinkingDelta {
		t.Error("missing thinking_delta event")
	}
	if !gotTextStart {
		t.Error("missing text_start event")
	}
	if !gotTextDelta {
		t.Error("missing text_delta event")
	}
}

// TestOpenAIStream_ToolCallArgsBeforeName tests the edge case where tool call
// arguments arrive before the function name in the SSE stream.
func TestOpenAIStream_ToolCallArgsBeforeName(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Chunk 1: tool call with ID but no name yet, has arguments
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"\",\"arguments\":\"{\\\"cmd\\\"\"}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 2: name arrives later
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"bash\",\"arguments\":\":\\\"ls\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	// When arguments arrive before the name, content_block_start fires with
	// whatever is available (ID but empty name). Arguments still accumulate correctly.
	var gotToolStart bool
	var toolInput strings.Builder
	var toolID string
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			if e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeToolUse {
				gotToolStart = true
				// ID is set from the first chunk, name is empty (arrives later)
				toolID = e.ContentBlock.ID
			}
		case "content_block_delta":
			if e.Delta != nil && e.Delta.Type == "input_json_delta" {
				toolInput.WriteString(e.Delta.PartialJSON)
			}
		}
	}
	if !gotToolStart {
		t.Error("missing tool_start event")
	}
	if toolID != "call_1" {
		t.Errorf("tool ID = %q, want %q", toolID, "call_1")
	}
	if toolInput.String() != `{"cmd":"ls"}` {
		t.Errorf("accumulated tool input = %q, want %q", toolInput.String(), `{"cmd":"ls"}`)
	}
}

// TestOpenAIStream_StreamEndsWithoutDONE tests graceful handling when stream
// ends without a [DONE] marker.
func TestOpenAIStream_StreamEndsWithoutDONE(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Send text but no [DONE] — just close the connection
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Partial\"},\"finish_reason\":null}]}\n\n")
		// Connection closes here — no [DONE]
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	// Should still get message_stop even without [DONE]
	last := events[len(events)-1]
	if last.Type != "message_stop" {
		t.Errorf("last event = %q, want message_stop", last.Type)
	}
}

// TestOpenAIStream_RetryAfterHeader tests that Retry-After header is parsed
// during retry backoff.
func TestOpenAIStream_RetryAfterHeader(t *testing.T) {
	callCount := 0
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	for range ch {
	}

	if callCount != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", callCount)
	}
}

// TestOpenAIStream_InterleavedParallelToolCalls tests real-world interleaved
// parallel tool call streaming: 3 tools with deltas arriving out of order.
func TestOpenAIStream_InterleavedParallelToolCalls(t *testing.T) {
	server := newTestOpenAIServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Chunk 1: tool 0 starts — bash with id + name
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-p1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_bash\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"co\"}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 2: tool 1 starts — read with id + name
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-p1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_read\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"pa\"}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 3: tool 0 continues — more bash args
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-p1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"mmand\\\":\\\"ls\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 4: tool 2 starts — write with id + name
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-p1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":2,\"id\":\"call_write\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\\\"path\\\":\\\"out.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 5: tool 1 continues — more read args
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-p1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"function\":{\"arguments\":\"th\\\":\\\"main.go\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
		// Finish
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-p1\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newOpenAIProviderWithServer(server)
	ctx := context.Background()
	req := &llm.Request{Model: "gpt-4", MaxTokens: 100}
	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []llm.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	// Collect tool starts and their accumulated input
	toolStarts := map[int]struct {
		id   string
		name string
	}{}
	toolInputs := map[int]string{}
	var stopIndices []int

	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			if e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeToolUse {
				toolStarts[e.Index] = struct {
					id   string
					name string
				}{id: e.ContentBlock.ID, name: e.ContentBlock.Name}
			}
		case "content_block_delta":
			if e.Delta != nil && e.Delta.Type == "input_json_delta" {
				toolInputs[e.Index] += e.Delta.PartialJSON
			}
		case "content_block_stop":
			stopIndices = append(stopIndices, e.Index)
		}
	}

	// Verify 3 tool calls started
	if len(toolStarts) != 3 {
		t.Fatalf("expected 3 tool starts, got %d", len(toolStarts))
	}

	// Tool 0: bash
	if toolStarts[0].name != "bash" {
		t.Errorf("tool[0] name = %q, want %q", toolStarts[0].name, "bash")
	}
	if toolStarts[0].id != "call_bash" {
		t.Errorf("tool[0] id = %q, want %q", toolStarts[0].id, "call_bash")
	}
	if toolInputs[0] != `{"command":"ls"}` {
		t.Errorf("tool[0] input = %q, want %q", toolInputs[0], `{"command":"ls"}`)
	}

	// Tool 1: read_file
	if toolStarts[1].name != "read_file" {
		t.Errorf("tool[1] name = %q, want %q", toolStarts[1].name, "read_file")
	}
	if toolStarts[1].id != "call_read" {
		t.Errorf("tool[1] id = %q, want %q", toolStarts[1].id, "call_read")
	}
	if toolInputs[1] != `{"path":"main.go"}` {
		t.Errorf("tool[1] input = %q, want %q", toolInputs[1], `{"path":"main.go"}`)
	}

	// Tool 2: write_file
	if toolStarts[2].name != "write_file" {
		t.Errorf("tool[2] name = %q, want %q", toolStarts[2].name, "write_file")
	}
	if toolStarts[2].id != "call_write" {
		t.Errorf("tool[2] id = %q, want %q", toolStarts[2].id, "call_write")
	}
	if toolInputs[2] != `{"path":"out.txt"}` {
		t.Errorf("tool[2] input = %q, want %q", toolInputs[2], `{"path":"out.txt"}`)
	}

	// All 3 stops must fire
	stopSet := map[int]bool{}
	for _, idx := range stopIndices {
		stopSet[idx] = true
	}
	for i := range 3 {
		if !stopSet[i] {
			t.Errorf("missing content_block_stop for tool index %d", i)
		}
	}
}
