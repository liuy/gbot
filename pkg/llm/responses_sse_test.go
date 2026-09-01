package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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
		wantType      string
		wantMessage   string
	}{
		{
			name:          "context_length_exceeded is overflow",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"context_length_exceeded","message":"too long"}}}`,
			wantOverflow:  true,
			wantRetryable: false,
			wantType:      "prompt_too_long",
			wantMessage:   "too long",
		},
		{
			name:          "rate_limit_exceeded retries",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"rate_limit_exceeded","message":"slow down"}}}`,
			wantRetryable: true,
			wantType:      "api_error",
			wantMessage:   "slow down",
		},
		{
			// codex responses.rs:410-414: the else branch maps every unmatched
			// code to ApiError::Retryable — unknown codes are retryable.
			name:          "unknown code is retryable",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"weird_code","message":"something odd"}}}`,
			wantRetryable: true,
			wantType:      "api_error",
			wantMessage:   "something odd",
		},
		{
			// Error object without a message degrades to the raw response
			// body (GLM adaptation layer — codex would surface an empty
			// message, which is strictly worse for the user).
			name:          "missing message falls back to raw body",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"weird_code"}}}`,
			wantRetryable: true,
			wantType:      "api_error",
			wantMessage:   `{"id":"resp_f","error":{"code":"weird_code"}}`,
		},
		{
			name:          "cyber_policy is fatal with message",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"cyber_policy","message":"This request was flagged for cyber policy."}}}`,
			wantRetryable: false,
			wantType:      "cyber_policy",
			wantMessage:   "This request was flagged for cyber policy.",
		},
		{
			name:          "cyber_policy blank message uses fallback",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"cyber_policy","message":"   "}}}`,
			wantRetryable: false,
			wantType:      "cyber_policy",
			wantMessage:   "This request has been flagged for possible cybersecurity risk.",
		},
		{
			name:          "invalid_prompt is invalid_request_error",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"invalid_prompt","message":"Invalid prompt: we've limited access to this content for safety reasons."}}}`,
			wantRetryable: false,
			wantType:      "invalid_request_error",
			wantMessage:   "Invalid prompt: we've limited access to this content for safety reasons.",
		},
		{
			name:          "invalid_prompt empty message falls back",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"invalid_prompt"}}}`,
			wantRetryable: false,
			wantType:      "invalid_request_error",
			wantMessage:   "Invalid request.",
		},
		{
			name:          "bio_policy is invalid_request_error",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"bio_policy","message":"This content was flagged for possible biological risk."}}}`,
			wantRetryable: false,
			wantType:      "invalid_request_error",
			wantMessage:   "This content was flagged for possible biological risk.",
		},
		{
			name:          "insufficient_quota is fatal",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"insufficient_quota","message":"You exceeded your current quota."}}}`,
			wantRetryable: false,
			wantType:      "api_error",
			wantMessage:   "You exceeded your current quota.",
		},
		{
			name:          "usage_not_included is fatal",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"usage_not_included","message":"not included"}}}`,
			wantRetryable: false,
			wantType:      "api_error",
			wantMessage:   "not included",
		},
		{
			name:          "server_is_overloaded retries",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"server_is_overloaded","message":"overloaded"}}}`,
			wantRetryable: true,
			wantType:      "server_overloaded",
			wantMessage:   "overloaded",
		},
		{
			name:          "slow_down retries",
			payload:       `{"type":"response.failed","response":{"id":"resp_f","error":{"code":"slow_down","message":"slow"}}}`,
			wantRetryable: true,
			wantType:      "server_overloaded",
			wantMessage:   "slow",
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
			if apiErr.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", apiErr.Type, tc.wantType)
			}
			if apiErr.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.wantMessage)
			}
		})
	}
}

// TestResponsesSSE_RateLimitDelayParsed feeds the real OpenAI rate-limit wire
// fixture through the failed-event path: the retry delay must be extracted
// into APIError.RetryAfter.
func TestResponsesSSE_RateLimitDelayParsed(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	payload := `{"type":"response.failed","sequence_number":3,"response":{"id":"resp_689bcf18d7f08194bf3440ba62fe05d803fee0cdac429894","object":"response","created_at":1755041560,"status":"failed","background":false,"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for gpt-5.1 in organization org-AAA on tokens per min (TPM): Limit 30000, Used 22999, Requested 12528. Please try again in 11.054s. Visit https://platform.openai.com/account/rate-limits to learn more."}, "usage":null,"user":null,"metadata":{}}}`

	events, err := collectResponsesEvents(context.Background(), p, sseBody("data: "+payload, ``))
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "error")
	apiErr := events[1].Error
	if apiErr == nil {
		t.Fatal("error event has nil Error")
	}
	if !apiErr.Retryable {
		t.Error("Retryable = false, want true")
	}
	diff := apiErr.RetryAfter - 11054*time.Millisecond
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Microsecond {
		t.Errorf("RetryAfter = %v, want ~11.054s (±1µs)", apiErr.RetryAfter)
	}
}

func TestTryParseRetryAfter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		code      string
		message   string
		want      time.Duration
		wantOK    bool
		tolerance time.Duration
	}{
		{
			name:    "milliseconds parse exactly",
			code:    "rate_limit_exceeded",
			message: "Rate limit reached for gpt-5.1 in organization org- on tokens per min (TPM): Limit 1, Used 1, Requested 19304. Please try again in 28ms. Visit https://platform.openai.com/account/rate-limits to learn more.",
			want:    28 * time.Millisecond,
			wantOK:  true,
		},
		{
			name:      "fractional seconds",
			code:      "rate_limit_exceeded",
			message:   "Rate limit reached for gpt-5.1 in organization <ORG> on tokens per min (TPM): Limit 30000, Used 6899, Requested 24050. Please try again in 1.898s. Visit https://platform.openai.com/account/rate-limits to learn more.",
			want:      1898 * time.Millisecond,
			wantOK:    true,
			tolerance: time.Microsecond,
		},
		{
			name:    "azure capital Try and seconds word",
			code:    "rate_limit_exceeded",
			message: "Rate limit exceeded. Try again in 35 seconds.",
			want:    35 * time.Second,
			wantOK:  true,
		},
		{
			// Rust Duration::from_secs_f64 panics past Duration::MAX; the Go
			// port treats unrepresentable values as no delay.
			name:    "overflowing seconds yields no delay",
			code:    "rate_limit_exceeded",
			message: "Please try again in 99999999999999999999s.",
			wantOK:  false,
		},
		{
			// 400 nines exceed f64 range — ParseFloat fails.
			name:    "unparseable magnitude yields no delay",
			code:    "rate_limit_exceeded",
			message: "Please try again in " + strings.Repeat("9", 400) + "s.",
			wantOK:  false,
		},
		{
			name:    "other code yields no delay even with matching message",
			code:    "server_is_overloaded",
			message: "Please try again in 28ms.",
			wantOK:  false,
		},
		{
			name:    "message without delay pattern",
			code:    "rate_limit_exceeded",
			message: "Rate limit reached with no delay hint.",
			wantOK:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tryParseRetryAfter(tc.code, tc.message)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			diff := got - tc.want
			if diff < 0 {
				diff = -diff
			}
			if diff > tc.tolerance {
				t.Errorf("delay = %v, want %v (±%v)", got, tc.want, tc.tolerance)
			}
		})
	}
}

func TestRateLimitRegexLazilyCompiled(t *testing.T) {
	t.Parallel()

	first := rateLimitRegex()
	second := rateLimitRegex()
	if first != second {
		t.Error("rateLimitRegex must return the same compiled instance on every call (OnceValue)")
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

// ---------------------------------------------------------------------------
// 17. LongLineProcessed — a single SSE line above 100KB must still be parsed
// ---------------------------------------------------------------------------

func TestResponsesSSE_LongLineProcessed(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// A single output_text.delta carrying 150000 bytes in one SSE line.
	// Responses servers routinely put the whole output_item.done or
	// response.completed payload on one line; a >100KB payload must not be
	// silently dropped.
	big := strings.Repeat("x", 150000)
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_17"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"`+big+`"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"`+big+`"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_17"}}`,
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

	if events[2].Delta == nil || len(events[2].Delta.Text) != 150000 {
		t.Errorf("delta text length = %d, want 150000", len(events[2].Delta.Text))
	}
}

// ---------------------------------------------------------------------------
// 18. SummaryDone backfill — done text is the authoritative full summary
// ---------------------------------------------------------------------------

func TestResponsesSSE_SummaryDoneBackfillsLostDeltas(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// No reasoning_summary_text.delta at all — the done event is the only
	// carrier of the summary text.
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_18"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0}`,
		``,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"Full summary text"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[{"type":"summary_text","text":"Full summary text"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_18"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // thinking
		"content_block_delta", // thinking_delta with the full text
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	if cb := events[1].ContentBlock; cb == nil || cb.Type != types.ContentTypeThinking {
		t.Errorf("content_block_start block = %+v, want thinking", events[1].ContentBlock)
	}
	if events[2].Delta == nil || events[2].Delta.Type != "thinking_delta" || events[2].Delta.Thinking != "Full summary text" {
		t.Errorf("thinking delta = %+v, want full summary text", events[2].Delta)
	}
}

func TestResponsesSSE_SummaryDoneMultiIndexBackfill(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// summary_index 0 arrives complete (delta + done); summary_index 1 loses
	// its deltas and only the done text survives.
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_18b"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"A"}`,
		``,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"A"}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":1}`,
		``,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":1,"text":"B"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[{"type":"summary_text","text":"A"},{"type":"summary_text","text":"B"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_18b"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	var think strings.Builder
	for _, ev := range events {
		if ev.Delta != nil && ev.Delta.Type == "thinking_delta" {
			think.WriteString(ev.Delta.Thinking)
		}
	}
	if think.String() != "AB" {
		t.Errorf("thinking = %q, want AB", think.String())
	}
	if got := strings.Count(think.String(), "A"); got != 1 {
		t.Errorf("thinking contains %d 'A', want exactly 1 (index 0 must not be duplicated)", got)
	}
}

// TestResponsesSSE_RawReasoningDeltaDoesNotPolluteSummaryIndex: raw
// reasoning_text and the summary are separate text streams within one item.
// The raw delta must stay out of summaryEmitted — otherwise the done(0)
// dedup baseline counts "R" as delivered summary bytes and silently drops
// the real summary text.
func TestResponsesSSE_RawReasoningDeltaDoesNotPolluteSummaryIndex(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_18c"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
		``,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"R"}`,
		``,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"S"}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_18c"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // thinking, opened by the raw delta
		"content_block_delta", // thinking_delta "R"
		"content_block_delta", // thinking_delta "S" backfilled from done(0)
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	var think strings.Builder
	for _, ev := range events {
		if ev.Delta != nil && ev.Delta.Type == "thinking_delta" {
			think.WriteString(ev.Delta.Thinking)
		}
	}
	if think.String() != "RS" {
		t.Errorf("thinking = %q, want RS (raw delta plus full summary backfill)", think.String())
	}
	if events[3].Delta == nil || events[3].Delta.Thinking != "S" {
		t.Errorf("backfill delta = %+v, want full S (baseline must be 0, not polluted by R)", events[3].Delta)
	}
}

// ---------------------------------------------------------------------------
// 19. ItemDone backfill — done items are the authoritative text carrier
// ---------------------------------------------------------------------------

func TestResponsesSSE_ItemDoneBackfillsTextNoDeltas(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_19"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_19"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // text
		"content_block_delta", // text_delta with the full done text
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	if cb := events[1].ContentBlock; cb == nil || cb.Type != types.ContentTypeText {
		t.Errorf("content_block_start block = %+v, want text", events[1].ContentBlock)
	}
	if events[2].Delta == nil || events[2].Delta.Type != "text_delta" || events[2].Delta.Text != "Hello world" {
		t.Errorf("text delta = %+v, want Hello world", events[2].Delta)
	}
}

func TestResponsesSSE_ItemDoneBackfillsReasoningNoDeltas(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_19b"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[{"type":"summary_text","text":"Thought through"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_19b"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	var think strings.Builder
	for _, ev := range events {
		if ev.Delta != nil && ev.Delta.Type == "thinking_delta" {
			think.WriteString(ev.Delta.Thinking)
		}
	}
	if think.String() != "Thought through" {
		t.Errorf("thinking = %q, want Thought through (done summary backfill)", think.String())
	}
	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)
}

func TestResponsesSSE_ItemDoneSuffixWhenDeltaPartiallyLost(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// Delta delivered only "Hel"; the done item carries the full "Hello" —
	// exactly the un-delivered suffix must be backfilled.
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_19c"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hel"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_19c"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta", // "Hel" from the delta
		"content_block_delta", // "lo" backfilled from done
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	if events[3].Delta == nil || events[3].Delta.Text != "lo" {
		t.Errorf("backfill delta = %+v, want exactly lo", events[3].Delta)
	}
}

func TestResponsesSSE_ItemDoneEmptyItemNoBlock(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// A done item with no text and no prior deltas must not synthesize an
	// empty content block.
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_19d"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_19d"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "message_delta", "message_stop")
}

// TestResponsesSSE_ItemDoneWithoutAddedBackfills covers the recovery path
// where output_item.added went missing but the done item still carries the
// authoritative text — the block is created from the done item alone.
func TestResponsesSSE_ItemDoneWithoutAddedBackfills(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		doneItem   string
		wantKind   types.ContentType
		wantDelta  string
		wantEvents []string
	}{
		{
			name:       "message done without added",
			doneItem:   `{"id":"item_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Rescued text"}]}`,
			wantKind:   types.ContentTypeText,
			wantDelta:  "Rescued text",
			wantEvents: []string{"content_block_start", "content_block_delta", "content_block_stop"},
		},
		{
			name:       "reasoning summary done without added",
			doneItem:   `{"id":"item_1","type":"reasoning","content":[],"summary":[{"type":"summary_text","text":"Recovered thought"}]}`,
			wantKind:   types.ContentTypeThinking,
			wantDelta:  "Recovered thought",
			wantEvents: []string{"content_block_start", "content_block_delta", "content_block_stop"},
		},
		{
			name:       "empty done item without added",
			doneItem:   `{"id":"item_1","type":"message","role":"assistant","content":[]}`,
			wantEvents: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := newResponsesTestProvider()
			body := sseBody(
				`data: {"type":"response.created","response":{"id":"resp_19e"}}`,
				``,
				`data: {"type":"response.output_item.done","output_index":0,"item":`+tc.doneItem+`}`,
				``,
				`data: {"type":"response.completed","response":{"id":"resp_19e"}}`,
				``,
			)

			events, err := collectResponsesEvents(context.Background(), p, body)
			if err != nil {
				t.Fatalf("parseResponsesSSE error: %v", err)
			}

			want := append([]string{"message_start"}, tc.wantEvents...)
			want = append(want, "message_delta", "message_stop")
			assertEventTypes(t, events, want...)

			if tc.wantDelta == "" {
				return
			}
			if cb := events[1].ContentBlock; cb == nil || cb.Type != tc.wantKind {
				t.Errorf("content_block_start block = %+v, want %s", events[1].ContentBlock, tc.wantKind)
			}
			if events[2].Delta == nil || (events[2].Delta.Text != tc.wantDelta && events[2].Delta.Thinking != tc.wantDelta) {
				t.Errorf("delta = %+v, want %q", events[2].Delta, tc.wantDelta)
			}
		})
	}
}

// TestResponsesSSE_SummaryDoneEmptyTextNoBlock: a done event with empty text
// must not open a block or emit a delta.
func TestResponsesSSE_SummaryDoneEmptyTextNoBlock(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_19f"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0}`,
		``,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":""}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_19f"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "message_delta", "message_stop")
}

func TestResponsesItemKind(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		itemType string
		want     string
	}{
		{"reasoning", "thinking"},
		{"message", "text"},
		{"function_call", ""},
	} {
		if got := responsesItemKind(tc.itemType); got != tc.want {
			t.Errorf("responsesItemKind(%q) = %q, want %q", tc.itemType, got, tc.want)
		}
	}
}

// TestResponsesSSE_EmptyDeltasSkipped: zero-length deltas must be skipped
// without perturbing the event sequence or opening blocks.
func TestResponsesSSE_EmptyDeltasSkipped(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_19g"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[]}}`,
		``,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":""}`,
		``,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"real thought"}`,
		``,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":""}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"real text"}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_19g"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	// No output_item.done events in this stream, so both blocks stay open
	// until response.completed closes them (creation order).
	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // thinking
		"content_block_delta", // thinking_delta
		"content_block_start", // text
		"content_block_delta", // text_delta
		"content_block_stop",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)
	if events[2].Delta == nil || events[2].Delta.Thinking != "real thought" {
		t.Errorf("thinking delta = %+v, want real thought only", events[2].Delta)
	}
	if events[4].Delta == nil || events[4].Delta.Text != "real text" {
		t.Errorf("text delta = %+v, want real text only", events[4].Delta)
	}
}

// ---------------------------------------------------------------------------
// 21. Incomplete non-max_output_tokens is an error
// ---------------------------------------------------------------------------

func TestResponsesSSE_IncompleteOtherReasonIsError(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// codex reports every incomplete reason other than max_output_tokens as
	// a terminal error; only max_output_tokens keeps the continuation path.
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_21"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"resp_21","status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`,
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
		"error",
	)
	apiErr := events[3].Error
	if apiErr == nil {
		t.Fatal("error event has nil Error")
	}
	if apiErr.Message != "Incomplete response returned, reason: content_filter" {
		t.Errorf("Message = %q, want Incomplete response returned, reason: content_filter", apiErr.Message)
	}
	if apiErr.Retryable {
		t.Error("Retryable = true, want false")
	}
}

func TestResponsesSSE_IncompleteMissingReasonIsError(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_21b"}}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"resp_21b","status":"incomplete"}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "error")
	apiErr := events[1].Error
	if apiErr == nil {
		t.Fatal("error event has nil Error")
	}
	if apiErr.Message != "Incomplete response returned, reason: unknown" {
		t.Errorf("Message = %q, want Incomplete response returned, reason: unknown", apiErr.Message)
	}
}

// ---------------------------------------------------------------------------
// 20. Completed payload parse failures
// ---------------------------------------------------------------------------

func TestResponsesSSE_CompletedUnparseableIsError(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// id has the wrong JSON type — codex serde rejects the payload.
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_20"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":123}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "error")
	if e := events[1].Error; e == nil || !strings.Contains(e.Message, "failed to parse ResponseCompleted") {
		t.Errorf("error event = %+v, want failed to parse ResponseCompleted", events[1].Error)
	}
}

func TestResponsesSSE_CompletedMissingIDIsError(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// codex ResponseCompleted.id is a required field — an empty object fails.
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_20b"}}`,
		``,
		`data: {"type":"response.completed","response":{}}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start", "error")
	if e := events[1].Error; e == nil || !strings.Contains(e.Message, "failed to parse ResponseCompleted") {
		t.Errorf("error event = %+v, want failed to parse ResponseCompleted", events[1].Error)
	}
}

func TestResponsesSSE_CompletedWithoutResponsePayloadIgnored(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	// No response field at all — codex maps this to Ok(None): keep scanning,
	// no terminal events. The clean EOF afterwards surfaces to the engine as
	// StreamInterruptedError (equivalent to codex's "stream closed before
	// response.completed").
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"resp_20c"}}`,
		``,
		`data: {"type":"response.completed"}`,
		``,
	)

	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parseResponsesSSE error: %v", err)
	}

	assertEventTypes(t, events, "message_start")
}
