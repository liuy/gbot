package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// TestAnthropicToolInput_TimeoutDisabled verifies that the SSE idle timeout
// is disabled during tool input phase. Uses a mock server that delays 150ms
// between content_block_start and the first delta (exceeding 50ms timeout).
// If disable doesn't work, stream dies and we get fewer events.
func TestAnthropicToolInput_TimeoutDisabled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)

		// message_start
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()

		// content_block_start (tool_use) — should trigger timeout disable
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_test\",\"name\":\"Write\"}}\n\n"))
		flusher.Flush()

		// 150ms delay — exceeds 50ms timeout. If disable works, stream survives.
		// Select on time.After so we abort cleanly if the client disconnects
		// mid-delay instead of blocking a detached handler goroutine.
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			return
		}

		// content_block_delta (tool parameters)
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"file_path\\\":\\\"/tmp/test.md\\\"}\"}}\n\n"))
		flusher.Flush()

		// content_block_stop — re-enables timeout
		_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		flusher.Flush()

		// message end
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		BaseProvider: BaseProvider{
			httpClient:  srv.Client(),
			idleTimeout: 50 * time.Millisecond,
		},
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   "test-model",
	}

	req := &Request{
		Model:     "test-model",
		MaxTokens: 100,
		Messages:  []types.Message{{Role: "user", Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "write a file"}}}},
	}

	eventCh, err := provider.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var received []StreamEvent
	for evt := range eventCh {
		received = append(received, evt)
		if evt.Error != nil {
			t.Fatalf("API error: %s", evt.Error.Message)
		}
	}

	eventTypes := make([]string, len(received))
	for i, evt := range received {
		eventTypes[i] = evt.Type
	}

	hasToolUseStart := false
	hasMessageStop := false
	for _, evt := range received {
		if evt.ContentBlock != nil && evt.ContentBlock.Type == types.ContentTypeToolUse {
			hasToolUseStart = true
		}
		if evt.Type == "message_stop" {
			hasMessageStop = true
		}
	}

	if !hasToolUseStart {
		t.Errorf("missing tool_use content_block_start, got events: %v", eventTypes)
	}
	if !hasMessageStop {
		t.Errorf("stream truncated — timeout fired during tool input phase? got events: %v", eventTypes)
	}
}
