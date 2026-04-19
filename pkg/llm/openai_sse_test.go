package llm

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sseBody joins lines with newline and returns a reader.
func sseBody(lines ...string) io.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}


// collectEvents calls parseOpenAISSE in a goroutine and collects all events.
func collectEvents(ctx context.Context, provider *OpenAIProvider, body io.Reader) []StreamEvent {
	req := &Request{Model: "gpt-4", MaxTokens: 100}
	ch := make(chan StreamEvent, 128)
	go func() {
		provider.parseOpenAISSE(ctx, req, body, ch)
		close(ch)
	}()
	var events []StreamEvent
	for e := range ch {
		events = append(events, e)
	}
	return events
}

// assertEventTypes is a test helper that checks the exact sequence of event types.
func assertEventTypes(t *testing.T, events []StreamEvent, want ...string) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d;\n  want: %v\n  got:  %v",
			len(want), len(events), want, eventTypes(events))
	}
	for i, w := range want {
		if events[i].Type != w {
			t.Errorf("event[%d]: expected type %q, got %q", i, w, events[i].Type)
		}
	}
}

// eventTypes extracts the Type field from a slice of events.
func eventTypes(events []StreamEvent) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

// ---------------------------------------------------------------------------
// 1. MessageStart
// ---------------------------------------------------------------------------

func TestOpenAISSE_MessageStart(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	// First event must be message_start
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Type != "message_start" {
		t.Fatalf("first event type = %q, want message_start", events[0].Type)
	}
	msg := events[0].Message
	if msg == nil {
		t.Fatal("message_start event has nil Message")
	}
	// ID should have the "msg_" prefix (synthesized)
	if !strings.HasPrefix(msg.ID, "msg_") {
		t.Errorf("Message.ID = %q, want prefix msg_", msg.ID)
	}
	// Model should come from the request
	if msg.Model != "gpt-4" {
		t.Errorf("Message.Model = %q, want gpt-4", msg.Model)
	}
	// Role should be assistant
	if msg.Role != "assistant" {
		t.Errorf("Message.Role = %q, want assistant", msg.Role)
	}
	// Usage should be zero-valued
	if msg.Usage.InputTokens != 0 || msg.Usage.OutputTokens != 0 {
		t.Errorf("Message.Usage = %+v, want zero", msg.Usage)
	}
}

// ---------------------------------------------------------------------------
// 2. TextOnly
// ---------------------------------------------------------------------------

func TestOpenAISSE_TextOnly(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",    // text block closed on finish_reason "stop"
		"message_delta",
		"message_stop",
	)

	// Verify content_block_start has text type
	cbStart := events[1]
	if cbStart.ContentBlock == nil || cbStart.ContentBlock.Type != types.ContentTypeText {
		t.Errorf("content_block_start type = %v, want text", cbStart.ContentBlock)
	}
	if cbStart.Index != 0 {
		t.Errorf("content_block_start index = %d, want 0", cbStart.Index)
	}

	// Verify text deltas
	if events[2].Delta == nil || events[2].Delta.Text != "Hello" {
		t.Errorf("first delta text = %q, want Hello", events[2].Delta)
	}
	if events[3].Delta == nil || events[3].Delta.Text != " world" {
		t.Errorf("second delta text = %q, want ' world'", events[3].Delta)
	}

	// Verify content_block_stop index (emitted before message_delta for "stop" reason)
	if events[4].Type != "content_block_stop" {
		t.Errorf("event[4] type = %q, want content_block_stop", events[4].Type)
	}
	if events[4].Index != 0 {
		t.Errorf("content_block_stop index = %d, want 0", events[4].Index)
	}

	// Verify message_delta stop reason
	if events[5].DeltaMsg == nil || events[5].DeltaMsg.StopReason != "end_turn" {
		t.Errorf("message_delta stop_reason = %q, want end_turn", events[5].DeltaMsg.StopReason)
	}
}

// ---------------------------------------------------------------------------
// 3. ToolCallComplete — single chunk has id + name + arguments
// ---------------------------------------------------------------------------

func TestOpenAISSE_ToolCallComplete(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"message_delta",
		"content_block_stop",
		"message_stop",
	)

	// content_block_start should have tool_use type with correct id/name
	cbStart := events[1]
	if cbStart.ContentBlock == nil {
		t.Fatal("content_block_start has nil ContentBlock")
	}
	if cbStart.ContentBlock.Type != types.ContentTypeToolUse {
		t.Errorf("ContentBlock.Type = %q, want tool_use", cbStart.ContentBlock.Type)
	}
	if cbStart.ContentBlock.ID != "call_abc" {
		t.Errorf("ContentBlock.ID = %q, want call_abc", cbStart.ContentBlock.ID)
	}
	if cbStart.ContentBlock.Name != "bash" {
		t.Errorf("ContentBlock.Name = %q, want bash", cbStart.ContentBlock.Name)
	}
	if cbStart.Index != 0 {
		t.Errorf("content_block_start Index = %d, want 0", cbStart.Index)
	}

	// content_block_delta should have input_json_delta
	delta := events[2]
	if delta.Delta == nil || delta.Delta.Type != "input_json_delta" {
		t.Fatalf("delta type = %q, want input_json_delta", delta.Delta)
	}
	if delta.Delta.PartialJSON != `{"command":"ls"}` {
		t.Errorf("PartialJSON = %q, want '{\"command\":\"ls\"}'", delta.Delta.PartialJSON)
	}

	// message_delta should have tool_use stop reason
	if events[3].DeltaMsg == nil || events[3].DeltaMsg.StopReason != "tool_use" {
		t.Errorf("message_delta stop_reason = %q, want tool_use", events[3].DeltaMsg.StopReason)
	}
}

// ---------------------------------------------------------------------------
// 4. ToolCallID — ID arrives before function.name
// ---------------------------------------------------------------------------

func TestOpenAISSE_ToolCallID(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	// First chunk: id only, no function name → accumulator created, no emit
	// Second chunk: function.name → content_block_start emitted
	// Third chunk: arguments → input_json_delta
	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xyz","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"","arguments":"{\"path\":\"/tmp/x\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"message_delta",
		"content_block_stop",
		"message_stop",
	)

	// content_block_start should have the stored id and the later-arriving name
	cbStart := events[1]
	if cbStart.ContentBlock == nil {
		t.Fatal("content_block_start has nil ContentBlock")
	}
	if cbStart.ContentBlock.ID != "call_xyz" {
		t.Errorf("ContentBlock.ID = %q, want call_xyz", cbStart.ContentBlock.ID)
	}
	if cbStart.ContentBlock.Name != "read_file" {
		t.Errorf("ContentBlock.Name = %q, want read_file", cbStart.ContentBlock.Name)
	}

	// Verify delta has arguments
	if events[2].Delta == nil || events[2].Delta.PartialJSON != `{"path":"/tmp/x"}` {
		t.Errorf("delta PartialJSON = %q, want '{\"path\":\"/tmp/x\"}'", events[2].Delta)
	}
}

// ---------------------------------------------------------------------------
// 5. ToolCallFragmented — id, name, arguments in separate chunks
// ---------------------------------------------------------------------------

func TestOpenAISSE_ToolCallFragmented(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		// Chunk 1: id only
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_frag","type":"function","function":{}}]},"finish_reason":null}]}`,
		"",
		// Chunk 2: name
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"write_file"}}]},"finish_reason":null}]}`,
		"",
		// Chunk 3: first argument fragment
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		"",
		// Chunk 4: second argument fragment
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/etc/hosts\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",   // index 0 — when name arrives
		"content_block_delta",   // first arg fragment
		"content_block_delta",   // second arg fragment
		"message_delta",
		"content_block_stop",
		"message_stop",
	)

	// Verify tool_use start
	cbStart := events[1]
	if cbStart.ContentBlock.ID != "call_frag" {
		t.Errorf("ContentBlock.ID = %q, want call_frag", cbStart.ContentBlock.ID)
	}
	if cbStart.ContentBlock.Name != "write_file" {
		t.Errorf("ContentBlock.Name = %q, want write_file", cbStart.ContentBlock.Name)
	}

	// Verify two argument deltas
	if events[2].Delta.PartialJSON != `{"path":` {
		t.Errorf("first delta = %q, want '{\"path\":'", events[2].Delta.PartialJSON)
	}
	if events[3].Delta.PartialJSON != `"/etc/hosts"}` {
		t.Errorf("second delta = %q, want '\"/etc/hosts\"}'", events[3].Delta.PartialJSON)
	}
}

// ---------------------------------------------------------------------------
// 6. ToolCallFinishBeforeArguments — finish_reason arrives but content_block_stop
//    is NOT emitted until [DONE]
// ---------------------------------------------------------------------------

func TestOpenAISSE_ToolCallFinishBeforeArguments(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_fin","type":"function","function":{"name":"grep","arguments":"\"pattern\""}}]},"finish_reason":null}]}`,
		"",
		// finish_reason arrives, but tool_use stop does NOT close the block yet
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"message_delta",
		"content_block_stop", // emitted on [DONE], not on finish_reason
		"message_stop",
	)

	// Verify content_block_stop comes after message_delta
	for i, e := range events {
		if e.Type == "message_delta" {
			// Find content_block_stop after this
			found := false
			for j := i + 1; j < len(events); j++ {
				if events[j].Type == "content_block_stop" {
					found = true
					break
				}
			}
			if !found {
				t.Error("content_block_stop should come after message_delta for tool_use")
			}
			break
		}
	}
}

// ---------------------------------------------------------------------------
// 7. MultipleToolCalls — two tool calls with different indices
// ---------------------------------------------------------------------------

func TestOpenAISSE_MultipleToolCalls(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		// Tool call 0: bash
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_bash","type":"function","function":{"name":"bash","arguments":"ls"}}]},"finish_reason":null}]}`,
		"",
		// Tool call 1: read
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",   // index 0 — bash
		"content_block_delta",   // bash args
		"content_block_start",   // index 1 — read_file
		"content_block_delta",   // read_file args
		"message_delta",
		"content_block_stop",    // bash close
		"content_block_stop",    // read close
		"message_stop",
	)

	// Verify first tool call
	cb0 := events[1]
	if cb0.ContentBlock.ID != "call_bash" || cb0.ContentBlock.Name != "bash" {
		t.Errorf("first tool call: ID=%q Name=%q", cb0.ContentBlock.ID, cb0.ContentBlock.Name)
	}
	if cb0.Index != 0 {
		t.Errorf("first tool call Index = %d, want 0", cb0.Index)
	}

	// Verify second tool call
	cb1 := events[3]
	if cb1.ContentBlock.ID != "call_read" || cb1.ContentBlock.Name != "read_file" {
		t.Errorf("second tool call: ID=%q Name=%q", cb1.ContentBlock.ID, cb1.ContentBlock.Name)
	}
	if cb1.Index != 1 {
		t.Errorf("second tool call Index = %d, want 1", cb1.Index)
	}

	// Verify content_block_stop indices
	// NOTE: [DONE] iterates map, so stop order is non-deterministic.
	// We check that both indices 0 and 1 appear among the stops.
	stopIndices := map[int]bool{}
	for _, e := range events {
		if e.Type == "content_block_stop" {
			stopIndices[e.Index] = true
		}
	}
	if !stopIndices[0] {
		t.Error("missing content_block_stop for index 0 (bash)")
	}
	if !stopIndices[1] {
		t.Error("missing content_block_stop for index 1 (read_file)")
	}
}

// ---------------------------------------------------------------------------
// 8. MixedTextAndTools — text followed by tool_call
// ---------------------------------------------------------------------------

func TestOpenAISSE_MixedTextAndTools(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		// Text content
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Let me check."},"finish_reason":null}]}`,
		"",
		// Tool call starts → text block should be closed
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_run","type":"function","function":{"name":"bash","arguments":"ls"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",   // text block
		"content_block_delta",   // "Let me check."
		"content_block_stop",    // text block closed when tool starts
		"content_block_start",   // tool_use block
		"content_block_delta",   // tool args
		"message_delta",
		"content_block_stop",    // tool block closed on [DONE]
		"message_stop",
	)

	// Verify text block
	if events[1].ContentBlock.Type != types.ContentTypeText {
		t.Errorf("first block type = %q, want text", events[1].ContentBlock.Type)
	}
	if events[2].Delta.Text != "Let me check." {
		t.Errorf("text delta = %q, want 'Let me check.'", events[2].Delta.Text)
	}

	// Verify text block closed (content_block_stop at index 3)
	if events[3].Type != "content_block_stop" {
		t.Errorf("expected content_block_stop for text at position 3, got %q", events[3].Type)
	}
	if events[3].Index != 0 {
		t.Errorf("text content_block_stop Index = %d, want 0", events[3].Index)
	}

	// Verify tool_use block
	if events[4].ContentBlock.Type != types.ContentTypeToolUse {
		t.Errorf("tool block type = %q, want tool_use", events[4].ContentBlock.Type)
	}
	if events[4].ContentBlock.ID != "call_run" {
		t.Errorf("tool block ID = %q, want call_run", events[4].ContentBlock.ID)
	}
}

// ---------------------------------------------------------------------------
// 9. ContentFilter — finish_reason "content_filter"
// ---------------------------------------------------------------------------

func TestOpenAISSE_ContentFilter(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"some text"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	// Find the message_delta event
	var msgDelta *StreamEvent
	for i := range events {
		if events[i].Type == "message_delta" {
			msgDelta = &events[i]
			break
		}
	}
	if msgDelta == nil {
		t.Fatal("no message_delta event found")
	}
	if msgDelta.DeltaMsg == nil {
		t.Fatal("message_delta has nil DeltaMsg")
	}
	if msgDelta.DeltaMsg.StopReason != "content_filter" {
		t.Errorf("stop_reason = %q, want content_filter", msgDelta.DeltaMsg.StopReason)
	}
}

// ---------------------------------------------------------------------------
// 10. MaxTokens — finish_reason "length"
// ---------------------------------------------------------------------------

func TestOpenAISSE_MaxTokens(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"truncated"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	var msgDelta *StreamEvent
	for i := range events {
		if events[i].Type == "message_delta" {
			msgDelta = &events[i]
			break
		}
	}
	if msgDelta == nil {
		t.Fatal("no message_delta event found")
	}
	if msgDelta.DeltaMsg.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want max_tokens", msgDelta.DeltaMsg.StopReason)
	}
}

// ---------------------------------------------------------------------------
// 11. MessageStop — [DONE] triggers pending stops + message_stop
// ---------------------------------------------------------------------------

func TestOpenAISSE_MessageStop(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	// Last event must be message_stop
	last := events[len(events)-1]
	if last.Type != "message_stop" {
		t.Errorf("last event type = %q, want message_stop", last.Type)
	}

	// Verify content_block_stop was emitted (text block closed on finish_reason "stop")
	hasBlockStop := false
	for _, e := range events {
		if e.Type == "content_block_stop" {
			hasBlockStop = true
		}
	}
	if !hasBlockStop {
		t.Error("expected content_block_stop for text block")
	}
}

// ---------------------------------------------------------------------------
// 12. ErrorContextLength — ParseAPIError maps context_length_exceeded
// ---------------------------------------------------------------------------

func TestOpenAISSE_ErrorContextLength(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	body := []byte(`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens.","type":"invalid_request_error"}}`)

	apiErr := p.ParseAPIError(body, 400)

	if apiErr.ErrorCode != "prompt_too_long" {
		t.Errorf("ErrorCode = %q, want prompt_too_long", apiErr.ErrorCode)
	}
	if apiErr.Type != "prompt_too_long" {
		t.Errorf("Type = %q, want prompt_too_long", apiErr.Type)
	}
	if apiErr.Retryable {
		t.Error("expected non-retryable for context_length_exceeded")
	}
	if apiErr.Status != 400 {
		t.Errorf("Status = %d, want 400", apiErr.Status)
	}
}

// ---------------------------------------------------------------------------
// 13. LineLengthGuard — lines > 100KB are skipped
// ---------------------------------------------------------------------------

func TestOpenAISSE_LineLengthGuard(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	// Build a line that is > 100KB but still valid data prefix
	longData := strings.Repeat("x", 100_001)
	longLine := "data: " + longData

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		longLine,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	// The long line should be skipped; only message_start, text content_block, and message_stop
	// should appear
	var textDeltas []StreamEvent
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "text_delta" {
			textDeltas = append(textDeltas, e)
		}
	}

	if len(textDeltas) != 1 {
		t.Fatalf("expected exactly 1 text delta (long line skipped), got %d", len(textDeltas))
	}
	if textDeltas[0].Delta.Text != "ok" {
		t.Errorf("text delta = %q, want ok", textDeltas[0].Delta.Text)
	}

	// Verify no event has the long data
	for _, e := range events {
		if e.Delta != nil && e.Delta.Text == longData {
			t.Error("long line should have been skipped but was emitted as an event")
		}
	}
}

// ---------------------------------------------------------------------------
// 14. IdleTimeout — lines arriving after idle timeout cause return
// ---------------------------------------------------------------------------

type slowReader struct {
	data   []byte
	delay  time.Duration
	offset int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func TestOpenAISSE_IdleTimeout(t *testing.T) {
	p := newTestProvider()
	// Set a very short idle timeout
	p.idleTimeout = 1 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Build SSE body: first chunk arrives quickly, second chunk arrives slowly
	sseData := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"

	body := &slowReader{data: []byte(sseData), delay: 100 * time.Millisecond}

	req := &Request{Model: "gpt-4", MaxTokens: 100}
	ch := make(chan StreamEvent, 128)
	go func() {
		p.parseOpenAISSE(ctx, req, body, ch)
		close(ch)
	}()

	var events []StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	// The idle timeout should cause early return — we may get message_start
	// but should NOT get message_stop (stream did not complete normally)
	hasMessageStop := false
	for _, e := range events {
		if e.Type == "message_stop" {
			hasMessageStop = true
		}
	}
	if hasMessageStop {
		t.Error("idle timeout should prevent message_stop from being emitted")
	}

	// Should have at least the message_start
	if len(events) == 0 {
		t.Error("expected at least message_start before timeout")
	}
	if events[0].Type != "message_start" {
		t.Errorf("first event = %q, want message_start", events[0].Type)
	}
}

// ---------------------------------------------------------------------------
// 15. SSEComment — lines starting with : are skipped
// ---------------------------------------------------------------------------

func TestOpenAISSE_SSEComment(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		": this is a comment",
		": another comment",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		": yet another comment",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_stop",
	)

	// Verify the text content came through correctly
	if events[2].Delta.Text != "hello" {
		t.Errorf("text delta = %q, want hello", events[2].Delta.Text)
	}
}

// ---------------------------------------------------------------------------
// 16. EmptyLines — empty lines are skipped
// ---------------------------------------------------------------------------

func TestOpenAISSE_EmptyLines(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		"",
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		"",
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		"",
		"",
		`data: [DONE]`,
		"",
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_stop",
	)
}

// ---------------------------------------------------------------------------
// 17. StreamEndedWithoutDONE — stream body ends without [DONE]
// ---------------------------------------------------------------------------

func TestOpenAISSE_StreamEndedWithoutDONE(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		// No [DONE] — stream just ends
	)

	events := collectEvents(ctx, p, body)

	// Should still emit pending stops and message_stop
	last := events[len(events)-1]
	if last.Type != "message_stop" {
		t.Errorf("last event type = %q, want message_stop", last.Type)
	}

	// Should have content_block_stop for the text block
	hasBlockStop := false
	for _, e := range events {
		if e.Type == "content_block_stop" {
			hasBlockStop = true
		}
	}
	if !hasBlockStop {
		t.Error("expected content_block_stop for text block even without [DONE]")
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: InvalidJSON in data line is skipped
// ---------------------------------------------------------------------------

func TestOpenAISSE_InvalidJSONSkipped(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: this is not valid json`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	// Invalid JSON should be silently skipped
	var textDeltas []string
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil {
			textDeltas = append(textDeltas, e.Delta.Text)
		}
	}
	if len(textDeltas) != 1 || textDeltas[0] != "ok" {
		t.Errorf("text deltas = %v, want exactly [ok]", textDeltas)
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: Context cancellation during parsing
// ---------------------------------------------------------------------------

func TestOpenAISSE_ContextCancellation(t *testing.T) {
	p := newTestProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan StreamEvent, 128)

	// Build a body that has content
	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	req := &Request{Model: "gpt-4", MaxTokens: 100}
	go func() {
		p.parseOpenAISSE(ctx, req, body, ch)
		close(ch)
	}()

	// Read a few events, then cancel
	eventCount := 0
	for range ch {
		eventCount++
		if eventCount >= 2 {
			cancel()
			// Drain remaining — parseOpenAISSE should exit on context cancellation
			for range ch {
			}
			break
		}
	}

	if eventCount < 1 {
		t.Error("expected at least 1 event before cancellation")
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: Non-data lines without "data: " prefix are skipped
// ---------------------------------------------------------------------------

func TestOpenAISSE_NonDataLinesSkipped(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`event: some_event`,
		`id: some_id`,
		`retry: 5000`,
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	// Non-data lines (event:, id:, retry:) should be silently skipped
	var textDeltas []string
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil {
			textDeltas = append(textDeltas, e.Delta.Text)
		}
	}
	if len(textDeltas) != 1 || textDeltas[0] != "ok" {
		t.Errorf("text deltas = %v, want exactly [ok]", textDeltas)
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: Usage in streaming response
// ---------------------------------------------------------------------------

func TestOpenAISSE_UsageInStream(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":5}}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	var msgDelta *StreamEvent
	for i := range events {
		if events[i].Type == "message_delta" {
			msgDelta = &events[i]
			break
		}
	}
	if msgDelta == nil {
		t.Fatal("no message_delta event found")
	}
	if msgDelta.Usage == nil {
		t.Fatal("message_delta has nil Usage")
	}
	if msgDelta.Usage.InputTokens != 100 {
		t.Errorf("Usage.InputTokens = %d, want 100", msgDelta.Usage.InputTokens)
	}
	if msgDelta.Usage.OutputTokens != 5 {
		t.Errorf("Usage.OutputTokens = %d, want 5", msgDelta.Usage.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: Role delta does not create text content
// ---------------------------------------------------------------------------

func TestOpenAISSE_RoleDeltaNoContent(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		// Only role, no content → no text block should be opened
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"message_delta",
		"message_stop",
	)

	// No content_block_start/delta/stop should appear
	for _, e := range events {
		if e.Type == "content_block_start" || e.Type == "content_block_delta" || e.Type == "content_block_stop" {
			t.Errorf("unexpected content event %q for role-only stream", e.Type)
		}
	}
}

func TestOpenAISSE_ToolCallEmptyArguments(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"message_delta",
		"content_block_stop",
		"message_stop",
	)

	// Verify tool call was captured with empty arguments
	delta := events[2]
	if delta.Delta == nil || delta.Delta.PartialJSON != "{}" {
		t.Errorf("input_json_delta = %q, want {}", delta.Delta.PartialJSON)
	}
}

func TestOpenAISSE_ToolArgumentsTooLarge(t *testing.T) {
	// NOT parallel — modifies package-level maxToolArgumentsSize

	// Lower the cap for fast testing, restore after
	origCap := maxToolArgumentsSize
	maxToolArgumentsSize = 10 // 10 bytes for testing
	t.Cleanup(func() { maxToolArgumentsSize = origCap })

	p := newTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_big","type":"function","function":{"name":"bash","arguments":"start"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}]},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	)

	events := collectEvents(ctx, p, body)

	// Should NOT have message_stop since parser returned early
	for _, e := range events {
		if e.Type == "message_stop" {
			t.Error("parser should have returned early on oversized arguments, but got message_stop")
		}
	}
	// Should have message_start and content_block_start at minimum
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	if events[0].Type != "message_start" {
		t.Errorf("first event = %q, want message_start", events[0].Type)
	}
}
