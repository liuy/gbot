package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// streamAccumulator reassembles a StreamEvent channel the way the engine does
// (engine.go streaming loop): text/thinking deltas append to the running
// strings, input_json_delta appends to a per-index builder.
type streamAccumulator struct {
	thinking strings.Builder
	text     strings.Builder
	toolArgs map[int]*strings.Builder
	toolMeta map[int]types.ContentBlock
	stop     string
	usage    *types.Usage
	model    string
	gotError bool
}

func newStreamAccumulator() *streamAccumulator {
	return &streamAccumulator{
		toolArgs: map[int]*strings.Builder{},
		toolMeta: map[int]types.ContentBlock{},
	}
}

func (a *streamAccumulator) consume(ch <-chan llm.StreamEvent) {
	for e := range ch {
		switch e.Type {
		case "message_start":
			if e.Message != nil {
				a.model = e.Message.Model
			}
		case "content_block_start":
			if e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeToolUse {
				a.toolMeta[e.Index] = *e.ContentBlock
				if a.toolArgs[e.Index] == nil {
					a.toolArgs[e.Index] = &strings.Builder{}
				}
			}
		case "content_block_delta":
			if e.Delta == nil {
				continue
			}
			switch e.Delta.Type {
			case "text_delta":
				a.text.WriteString(e.Delta.Text)
			case "thinking_delta":
				a.thinking.WriteString(e.Delta.Thinking)
			case "input_json_delta":
				if a.toolArgs[e.Index] == nil {
					a.toolArgs[e.Index] = &strings.Builder{}
				}
				a.toolArgs[e.Index].WriteString(e.Delta.PartialJSON)
			}
		case "message_delta":
			if e.DeltaMsg != nil {
				a.stop = e.DeltaMsg.StopReason
			}
			a.usage = e.Usage
		case "error":
			a.gotError = true
		}
	}
}

// writeSSE writes one SSE event and flushes so the client sees it immediately.
func writeSSE(w http.ResponseWriter, payload string) {
	_, _ = fmt.Fprint(w, "data: "+payload+"\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// glmFullStream is a full GLM-shaped turn: reasoning → text → function_call.
func glmFullStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	writeSSE(w, `{"type":"response.created","response":{"id":"resp_e2e","model":"glm-4.6"}}`)
	writeSSE(w, `{"type":"response.in_progress","response":{"id":"resp_e2e"}}`)

	writeSSE(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[]}}`)
	writeSSE(w, `{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"Thinking "}`)
	writeSSE(w, `{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"about it"}`)
	writeSSE(w, `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Thinking about it"}]}}`)

	writeSSE(w, `{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`)
	writeSSE(w, `{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"Running "}`)
	writeSSE(w, `{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"command."}`)
	writeSSE(w, `{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Running command."}]}}`)

	writeSSE(w, `{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":""}}`)
	writeSSE(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"cmd\""}`)
	writeSSE(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":":\"ls\"}"}`)
	writeSSE(w, `{"type":"response.output_item.done","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}}`)

	writeSSE(w, `{"type":"response.completed","response":{"id":"resp_e2e","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":12,"total_tokens":112}}}`)
	writeSSE(w, `[DONE]`)
}

// TestResponsesStream_EndToEnd drives the full chain: Stream entry → request
// translation → HTTP → SSE parsing → timeoutReader wrapping → channel →
// engine-shaped reassembly.
func TestResponsesStream_EndToEnd(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
			return
		}
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		if s, ok := reqBody["stream"].(bool); !ok || !s {
			t.Errorf("stream = %v, want true", reqBody["stream"])
		}
		if s, ok := reqBody["store"].(bool); !ok || s {
			t.Errorf("store = %v, want false", reqBody["store"])
		}
		glmFullStream(w)
	}))
	defer server.Close()

	provider := llm.NewResponsesProvider(&llm.ResponsesConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "glm-4.6",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req := &llm.Request{
		Model:     "glm-4.6",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("list the files")}},
		},
		Tools: []llm.ToolDef{{
			Name:        "bash",
			Description: "run a command",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`),
		}},
	}

	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	acc := newStreamAccumulator()
	acc.consume(ch) // returns when the channel closes

	if acc.gotError {
		t.Fatal("stream produced an error event")
	}
	if acc.model != "glm-4.6" {
		t.Errorf("model = %q, want glm-4.6", acc.model)
	}
	if acc.thinking.String() != "Thinking about it" {
		t.Errorf("thinking = %q, want 'Thinking about it'", acc.thinking.String())
	}
	if acc.text.String() != "Running command." {
		t.Errorf("text = %q, want 'Running command.'", acc.text.String())
	}
	if len(acc.toolArgs) != 1 {
		t.Fatalf("tool blocks = %d, want 1", len(acc.toolArgs))
	}
	meta := acc.toolMeta[2]
	if meta.ID != "call_1" || meta.Name != "bash" {
		t.Errorf("tool meta = %+v, want call_1/bash", meta)
	}
	gotArgs := acc.toolArgs[2].String()
	if gotArgs != `{"cmd":"ls"}` {
		t.Errorf("tool input = %q, want {\"cmd\":\"ls\"}", gotArgs)
	}
	if !json.Valid([]byte(gotArgs)) {
		t.Errorf("tool input %q is not valid JSON", gotArgs)
	}
	if acc.stop != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", acc.stop)
	}
	if acc.usage == nil {
		t.Fatal("usage is nil")
	}
	if acc.usage.InputTokens != 60 || acc.usage.CacheReadInputTokens != 40 || acc.usage.OutputTokens != 12 {
		t.Errorf("usage = %+v, want 60/40/12", *acc.usage)
	}
}

// TestResponsesStream_RetryOn500 verifies the retry loop wiring: a 500
// followed by a healthy SSE stream still yields the full event sequence.
func TestResponsesStream_RetryOn500(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"code":"internal","message":"boom","type":"server_error"}}`)
			return
		}
		glmFullStream(w)
	}))
	defer server.Close()

	provider := llm.NewResponsesProvider(&llm.ResponsesConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "glm-4.6",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := &llm.Request{
		Model:     "glm-4.6",
		MaxTokens: 100,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		},
	}

	ch, err := provider.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	acc := newStreamAccumulator()
	acc.consume(ch)

	if callCount != 2 {
		t.Errorf("server calls = %d, want 2 (one retry)", callCount)
	}
	if acc.gotError {
		t.Fatal("stream produced an error event after retry")
	}
	if acc.text.String() != "Running command." {
		t.Errorf("text = %q, want 'Running command.'", acc.text.String())
	}
	if acc.stop != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", acc.stop)
	}
}
