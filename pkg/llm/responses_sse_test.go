package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// collectResponsesEvents runs parseResponsesSSE and returns both the events
// and the parser's return value — unlike openai's collectEvents the error is
// part of the contract under test (EOF semantics).
func collectResponsesEvents(ctx context.Context, p *ResponsesProvider, body io.Reader) ([]StreamEvent, error) {
	req := &Request{Model: "glm-4.6", MaxTokens: 100}
	ch := make(chan StreamEvent, 128)
	var parseErr error
	go func() {
		parseErr = p.parseResponsesSSE(ctx, req, body, nil, ch)
		close(ch)
	}()
	var events []StreamEvent
	for e := range ch {
		events = append(events, e)
	}
	return events, parseErr
}

// ---------------------------------------------------------------------------
// 1. CreatedAndText
// ---------------------------------------------------------------------------

func TestResponsesSSE_CreatedAndText(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_1","model":"glm-4.6"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hel"}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"lo"}`,
		``,
		`data: {"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"Hello"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":12,"total_tokens":112}}}`,
		``,
		`data: [DONE]`,
		``,
	)

	events, err := collectResponsesEvents(ctx, p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	msg := events[0].Message
	if msg == nil {
		t.Fatal("message_start has nil Message")
	}
	if msg.ID != "resp_1" {
		t.Errorf("message_start.ID = %q, want resp_1 (from created)", msg.ID)
	}
	if msg.Model != "glm-4.6" {
		t.Errorf("message_start.Model = %q, want glm-4.6", msg.Model)
	}

	if cb := events[1].ContentBlock; cb == nil || cb.Type != types.ContentTypeText {
		t.Errorf("content_block_start block = %+v, want text", events[1].ContentBlock)
	}
	if events[2].Delta == nil || events[2].Delta.Text != "Hel" {
		t.Errorf("first delta = %+v, want Hel", events[2].Delta)
	}
	if events[3].Delta == nil || events[3].Delta.Text != "lo" {
		t.Errorf("second delta = %+v, want lo", events[3].Delta)
	}
	if events[4].Index != 0 {
		t.Errorf("content_block_stop index = %d, want 0", events[4].Index)
	}

	delta := events[5]
	if delta.DeltaMsg == nil || delta.DeltaMsg.StopReason != "end_turn" {
		t.Errorf("message_delta stop_reason = %+v, want end_turn", delta.DeltaMsg)
	}
	if delta.Usage == nil {
		t.Fatal("message_delta has nil Usage")
	}
	if delta.Usage.InputTokens != 60 {
		t.Errorf("Usage.InputTokens = %d, want 60 (100 minus 40 cached)", delta.Usage.InputTokens)
	}
	if delta.Usage.CacheReadInputTokens != 40 {
		t.Errorf("Usage.CacheReadInputTokens = %d, want 40", delta.Usage.CacheReadInputTokens)
	}
	if delta.Usage.OutputTokens != 12 {
		t.Errorf("Usage.OutputTokens = %d, want 12", delta.Usage.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// 2. ReasoningThenText
// ---------------------------------------------------------------------------

func TestResponsesSSE_SummaryInsteadOfRawThought(t *testing.T) {
	// OpenAI's o-series streams reasoning summaries, never raw thought. The
	// dispatch must land them on the same thinking path — server policy
	// decides which flavor arrives, not us.
	fixture := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_1","model":"o9"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","summary_index":0}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Comparing decimals carefully."}`,
		``,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","summary_index":0,"text":"Comparing decimals carefully."}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[{"type":"summary_text","text":"Comparing decimals carefully."}]}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"9.8"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"9.8"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":5}}}`,
		``,
		`data: [DONE]`,
		``,
	)
	events, err := collectResponsesEvents(context.Background(),
		NewResponsesProvider(&ResponsesConfig{Model: "o9"}), fixture)
	if err != nil {
		t.Fatal(err)
	}
	assertEventTypes(t, events,
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop")
	var think strings.Builder
	for _, ev := range events {
		if ev.Delta != nil && ev.Delta.Type == "thinking_delta" {
			think.WriteString(ev.Delta.Thinking)
		}
	}
	if think.String() != "Comparing decimals carefully." {
		t.Errorf("thinking = %q, want the summary text", think.String())
	}
}

func TestResponsesSSE_ReasoningThenText(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_2","model":"glm-4.6"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[]}}`,
		``,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"think "}`,
		``,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"hard"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"think hard"}]}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"Answer"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Answer"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_2","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
		``,
	)

	events, err := collectResponsesEvents(ctx, p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // thinking, index 0
		"content_block_delta", // thinking_delta
		"content_block_delta", // thinking_delta
		"content_block_stop",  // thinking closed before text opens
		"content_block_start", // text, index 1
		"content_block_delta", // text_delta
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	if cb := events[1].ContentBlock; cb == nil || cb.Type != types.ContentTypeThinking || events[1].Index != 0 {
		t.Errorf("thinking block start = %+v index %d, want thinking/0", events[1].ContentBlock, events[1].Index)
	}
	if events[2].Delta == nil || events[2].Delta.Thinking != "think " {
		t.Errorf("first thinking delta = %+v, want 'think '", events[2].Delta)
	}
	if events[3].Delta == nil || events[3].Delta.Thinking != "hard" {
		t.Errorf("second thinking delta = %+v, want hard", events[3].Delta)
	}
	if events[4].Index != 0 {
		t.Errorf("thinking content_block_stop index = %d, want 0", events[4].Index)
	}
	if cb := events[5].ContentBlock; cb == nil || cb.Type != types.ContentTypeText || events[5].Index != 1 {
		t.Errorf("text block start = %+v index %d, want text/1", events[5].ContentBlock, events[5].Index)
	}
}

// ---------------------------------------------------------------------------
// 3. FunctionCallSplitArgs
// ---------------------------------------------------------------------------

func TestResponsesSSE_FunctionCallSplitArgs(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_3"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_abc","name":"bash","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"cmd\""}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":":\"ls\"}"}`,
		``,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"cmd\":\"ls\"}"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_abc","name":"bash","arguments":"{\"cmd\":\"ls\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_3","usage":{"input_tokens":10,"output_tokens":8,"total_tokens":18}}}`,
		``,
	)

	events, err := collectResponsesEvents(ctx, p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	cb := events[1].ContentBlock
	if cb == nil || cb.Type != types.ContentTypeToolUse {
		t.Fatalf("content_block_start = %+v, want tool_use", cb)
	}
	if cb.ID != "call_abc" || cb.Name != "bash" {
		t.Errorf("tool_use ID/Name = %q/%q, want call_abc/bash (memorized from added)", cb.ID, cb.Name)
	}

	var joined strings.Builder
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "input_json_delta" {
			joined.WriteString(e.Delta.PartialJSON)
		}
	}
	if joined.String() != `{"cmd":"ls"}` {
		t.Errorf("joined arguments = %q, want {\"cmd\":\"ls\"}", joined.String())
	}

	if events[5].DeltaMsg == nil || events[5].DeltaMsg.StopReason != "tool_use" {
		t.Errorf("stop_reason = %+v, want tool_use", events[5].DeltaMsg)
	}
}

// ---------------------------------------------------------------------------
// 4. ParallelFunctionCalls
// ---------------------------------------------------------------------------

func TestResponsesSSE_ParallelFunctionCalls(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_4"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"bash","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"ls"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"bash","arguments":"ls"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_b","name":"read_file","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":1,"delta":"{\"path\":\"x\"}"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_b","name":"read_file","arguments":"{\"path\":\"x\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_4"}}`,
		``,
	)

	events, err := collectResponsesEvents(ctx, p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // index 0
		"content_block_delta",
		"content_block_stop",
		"content_block_start", // index 1
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	if events[1].Index != 0 || events[1].ContentBlock.ID != "call_a" {
		t.Errorf("first tool block index/ID = %d/%q, want 0/call_a", events[1].Index, events[1].ContentBlock.ID)
	}
	if events[4].Index != 1 || events[4].ContentBlock.ID != "call_b" {
		t.Errorf("second tool block index/ID = %d/%q, want 1/call_b (itemID lookup must not cross-wire)", events[4].Index, events[4].ContentBlock.ID)
	}
	if events[5].Delta == nil || events[5].Delta.PartialJSON != `{"path":"x"}` {
		t.Errorf("second tool delta = %+v, want path x", events[5].Delta)
	}
	if events[7].DeltaMsg == nil || events[7].DeltaMsg.StopReason != "tool_use" {
		t.Errorf("stop_reason = %+v, want tool_use", events[7].DeltaMsg)
	}
}

// ---------------------------------------------------------------------------
// 5. FunctionCallNoDeltas
// ---------------------------------------------------------------------------

func TestResponsesSSE_FunctionCallNoDeltas(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_5"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":""}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_5"}}`,
		``,
	)

	events, err := collectResponsesEvents(ctx, p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	if events[2].Delta == nil || events[2].Delta.Type != "input_json_delta" || events[2].Delta.PartialJSON != `{"cmd":"ls"}` {
		t.Errorf("catch-up delta = %+v, want full arguments {\"cmd\":\"ls\"}", events[2].Delta)
	}
}

// ---------------------------------------------------------------------------
// 6. EmptyOutput
// ---------------------------------------------------------------------------

func TestResponsesSSE_EmptyOutput(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_6"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_6","output":[]}}`,
		``,
	)

	events, err := collectResponsesEvents(ctx, p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "message_delta", "message_stop")
}

// ---------------------------------------------------------------------------
// 7. UsageMissing
// ---------------------------------------------------------------------------

func TestResponsesSSE_UsageMissing(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	ctx := context.Background()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_7"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_7"}}`,
		``,
	)

	events, err := collectResponsesEvents(ctx, p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	delta := events[4]
	if delta.Usage == nil {
		t.Fatal("message_delta.Usage must be non-nil even when usage is absent")
	}
	if *delta.Usage != (types.Usage{}) {
		t.Errorf("Usage = %+v, want zero value", *delta.Usage)
	}
}

// ---------------------------------------------------------------------------
// 8. Failed classification
// ---------------------------------------------------------------------------

func TestResponsesSSE_FailedClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		payload       string
		wantOverflow  bool
		wantRetryable bool
	}{
		{
			name:          "context_length_exceeded is overflow",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"context_length_exceeded","message":"too long"}}}`,
			wantOverflow:  true,
			wantRetryable: false,
		},
		{
			name:          "rate_limit_exceeded retries",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"rate_limit_exceeded","message":"slow down"}}}`,
			wantRetryable: true,
		},
		{
			name:          "unknown code is fatal with message",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"weird_code","message":"something odd"}}}`,
			wantRetryable: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := newResponsesTestProvider()
			events, err := collectResponsesEvents(context.Background(), p, sseBody("data: "+tc.payload, ``))
			if err != nil {
				t.Fatalf("parseResponsesSSE error: %v", err)
			}

			assertEventTypes(t, events, "message_start", "error")

			apiErr := events[1].Error
			if apiErr == nil {
				t.Fatal("error event has nil Error")
			}
			if got := IsContextOverflow(apiErr); got != tc.wantOverflow {
				t.Errorf("IsContextOverflow = %v, want %v", got, tc.wantOverflow)
			}
			if apiErr.Retryable != tc.wantRetryable {
				t.Errorf("Retryable = %v, want %v", apiErr.Retryable, tc.wantRetryable)
			}
			if apiErr.Message == "" {
				t.Error("Message must carry wire message, got empty")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 9. ErrorEventShapes
// ---------------------------------------------------------------------------

func TestResponsesSSE_ErrorEventShapes(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	flat := sseBody(
		`data: {"type":"error","code":"rate_limit_exceeded","message":"slow down"}`,
		``,
	)
	events, err := collectResponsesEvents(context.Background(), p, flat)
	if err != nil {
		t.Fatalf("flat shape: %v", err)
	}
	assertEventTypes(t, events, "message_start", "error")
	flatErr := events[1].Error
	if flatErr == nil || flatErr.ErrorCode != "rate_limit_exceeded" || !flatErr.Retryable {
		t.Errorf("flat error event = %+v, want code rate_limit_exceeded retryable", events[1].Error)
	}
	if flatErr.Message != "slow down" {
		t.Errorf("flat message = %q, want slow down", flatErr.Message)
	}

	nested := sseBody(
		`data: {"type":"error","error":{"code":"context_length_exceeded","message":"overflow"}}`,
		``,
	)
	events, err = collectResponsesEvents(context.Background(), p, nested)
	if err != nil {
		t.Fatalf("nested shape: %v", err)
	}
	assertEventTypes(t, events, "message_start", "error")
	nestedErr := events[1].Error
	if nestedErr == nil || !IsContextOverflow(nestedErr) {
		t.Errorf("nested error event = %+v, want context overflow", events[1].Error)
	}
}

// ---------------------------------------------------------------------------
// 10. DoneSentinelWithoutCompleted
// ---------------------------------------------------------------------------

func TestResponsesSSE_DoneSentinelWithoutCompleted(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	events, err := collectResponsesEvents(context.Background(), p, sseBody(`data: [DONE]`, ``))
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "message_stop")
	for _, e := range events {
		if e.Type == "message_delta" {
			t.Error("[DONE] sentinel path must not emit message_delta (no usage/stop_reason available)")
		}
	}
}

// ---------------------------------------------------------------------------
// 11. EOFBeforeCompleted
// ---------------------------------------------------------------------------

// errAfterReader yields data first, then a non-EOF error — simulating a
// mid-stream transport failure (e.g. HTTP/2 RST_STREAM).
type errAfterReader struct {
	data   []byte
	offset int
	err    error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.offset < len(r.data) {
		n := copy(p, r.data[r.offset:])
		r.offset += n
		return n, nil
	}
	return 0, r.err
}

func TestResponsesSSE_EOFBeforeCompleted_CleanEOF(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// created + text deltas, then the reader just ends — no completed, no [DONE].
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_11"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("clean EOF must return nil error so the engine retries via StreamInterruptedError, got: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
	)
}

func TestResponsesSSE_EOFBeforeCompleted_IOError(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	data := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_11b\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"delta\":\"x\"}\n\n"
	body := &errAfterReader{data: []byte(data), err: errors.New("boom: connection reset")}

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err == nil {
		t.Fatal("mid-stream IO error must surface as a returned error (transport_error path)")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want underlying scanner error", err)
	}
	if len(events) == 0 {
		t.Error("expected events before the IO error")
	}
}

// ---------------------------------------------------------------------------
// 12. IncompleteMaxTokens
// ---------------------------------------------------------------------------

func TestResponsesSSE_IncompleteMaxTokens(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_12"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"trunc"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"trunc"}]}}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"resp_12","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":7,"output_tokens":9,"total_tokens":16}}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	if events[4].DeltaMsg == nil || events[4].DeltaMsg.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %+v, want max_tokens (engine continuation trigger)", events[4].DeltaMsg)
	}
	if events[4].Usage == nil || events[4].Usage.OutputTokens != 9 {
		t.Errorf("incomplete usage = %+v, want output 9", events[4].Usage)
	}
}

// ---------------------------------------------------------------------------
// 13. UnknownEventsSkipped
// ---------------------------------------------------------------------------

func TestResponsesSSE_UnknownEventsSkipped(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_13"}}`,
		``,
		`data: {"type":"response.in_progress","response":{"id":"resp_13"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0}`,
		``,
		`data: {"type":"response.some_future_event","foo":1}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"ok"}`,
		``,
		`data: {"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_13"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	// Identical to a plain text stream — unknown events must not perturb the sequence.
	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)
	if events[2].Delta == nil || events[2].Delta.Text != "ok" {
		t.Errorf("text delta = %+v, want ok", events[2].Delta)
	}
}

// ---------------------------------------------------------------------------
// 14. TimeoutBoundary — td toggle sequence around tool input phase
// ---------------------------------------------------------------------------

func TestResponsesSSE_TimeoutBoundary_ToolCall(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_14"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{}"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":"{}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_14"}}`,
		``,
	)

	spy := &tdSpy{}
	req := &Request{Model: "glm-4.6", MaxTokens: 100}
	ch := make(chan StreamEvent, 128)
	if err := p.parseResponsesSSE(context.Background(), req, body, spy, ch); err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	close(ch)

	want := []bool{true, false}
	if len(spy.calls) != len(want) {
		t.Fatalf("td calls = %v, want %v", spy.calls, want)
	}
	for i, w := range want {
		if spy.calls[i] != w {
			t.Errorf("td call[%d] = %v, want %v (full: %v)", i, spy.calls[i], w, spy.calls)
		}
	}
}

func TestResponsesSSE_TimeoutBoundary_TextOnly(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_14b"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_14b"}}`,
		``,
	)

	spy := &tdSpy{}
	req := &Request{Model: "glm-4.6", MaxTokens: 100}
	ch := make(chan StreamEvent, 128)
	if err := p.parseResponsesSSE(context.Background(), req, body, spy, ch); err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	close(ch)

	if len(spy.calls) != 0 {
		t.Errorf("td calls = %v, want none for text-only stream", spy.calls)
	}
}

// ---------------------------------------------------------------------------
// 15. MalformedChunkSkipped
// ---------------------------------------------------------------------------

func TestResponsesSSE_MalformedChunkSkipped(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_15"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: this is not valid json`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"ok"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_15"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)
	if events[2].Delta == nil || events[2].Delta.Text != "ok" {
		t.Errorf("text delta after malformed chunk = %+v, want ok", events[2].Delta)
	}
}

// ---------------------------------------------------------------------------
// 16. CompletedClosesOpenBlocks — missing output_item.done
// ---------------------------------------------------------------------------

func TestResponsesSSE_CompletedClosesOpenBlocks_FunctionCall(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_16a"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{}"}`,
		``,
		// output_item.done intentionally absent
		`data: {"type":"response.completed","response":{"id":"resp_16a"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop", // completed must close the dangling block
		"message_delta",
		"message_stop",
	)
}

func TestResponsesSSE_CompletedClosesOpenBlocks_Message(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_16b"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`,
		``,
		// output_item.done intentionally absent
		`data: {"type":"response.completed","response":{"id":"resp_16b"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop", // completed must close the dangling block
		"message_delta",
		"message_stop",
	)
}
