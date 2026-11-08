package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fmt"
	"github.com/liuy/gbot/pkg/types"
	"math"
	"net"
	"net/url"
)

func newResponsesMutServer(handler http.HandlerFunc) (*ResponsesProvider, *httptest.Server) {
	srv := httptest.NewServer(handler)
	p := &ResponsesProvider{
		name: "test", httpClient: srv.Client(), idleTimeout: DefaultSSETimeout, retryConfig: DefaultRetryConfig(),
		apiKey:  "key",
		baseURL: srv.URL,
		model:   "glm-test",
	}
	return p, srv
}

// waitUntil polls cond until it holds or timeout elapses: the substitute for
// widening a fixed ctx window when that window is itself the mutation
// detection band (e.g. it must stay under a Retry-After the test honors).
func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout) // REAL-TIME
	for time.Now().Before(deadline) {   // REAL-TIME
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	return cond()
}

func TestNewResponsesProviderDefaults(t *testing.T) {
	t.Parallel()

	p := NewResponsesProvider(&ResponsesConfig{Name: "n", APIKey: "k"})
	if p.baseURL != "https://api.openai.com/v1" {
		t.Errorf("baseURL = %q", p.baseURL)
	}
	if p.httpClient.Timeout != DefaultHTTPTimeout {
		t.Errorf("timeout = %v", p.httpClient.Timeout)
	}
	if p.idleTimeout != DefaultSSETimeout {
		t.Errorf("idleTimeout = %v", p.idleTimeout)
	}
	p2 := NewResponsesProvider(&ResponsesConfig{Name: "n", APIKey: "k", BaseURL: "http://h/v1/", Model: "m"})
	if p2.baseURL != "http://h/v1" || p2.model != "m" {
		t.Errorf("explicit: %q %q", p2.baseURL, p2.model)
	}
	if p2.name != "n" || p2.retryConfig == nil {
		t.Errorf("explicit fields: name=%q retryConfig=%v", p2.name, p2.retryConfig)
	}
}

func TestResponsesUsageToUsage(t *testing.T) {
	t.Parallel()

	empty := responsesUsageToUsage(nil)
	if empty != (types.Usage{}) {
		t.Errorf("nil usage = %+v, want zero", empty)
	}

	u := &responsesUsage{InputTokens: 100, OutputTokens: 12, TotalTokens: 112}
	got := responsesUsageToUsage(u)
	if got.InputTokens != 100 || got.CacheReadInputTokens != 0 || got.OutputTokens != 12 || got.CacheCreationInputTokens != 0 {
		t.Errorf("no-details usage = %+v", got)
	}

	u.InputTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 40}
	got = responsesUsageToUsage(u)
	if got.InputTokens != 60 || got.CacheReadInputTokens != 40 {
		t.Errorf("cached usage = %+v, want input 60/cacheRead 40", got)
	}
}

// translateResponsesItems preserves block order and shapes: thinking→reasoning,
// text→message, tool_use→function_call, tool_result→function_call_output,
// images only from user turns.
func TestTranslateResponsesItems_Shapes(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Thinking: "why"},
			{Type: types.ContentTypeText, Text: "answer"},
			{Type: types.ContentTypeToolUse, ID: "c1", Name: "W", Input: json.RawMessage(`{"a":1}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "c1", Content: json.RawMessage(`[{"type":"text","text":"res"}]`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hello"},
			{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "QUJD"}},
		}},
	}
	items := translateResponsesItems(msgs)
	if len(items) != 5 {
		t.Fatalf("items = %+v, want 5", items)
	}
	if items[0].Type != "reasoning" || len(items[0].Content) != 1 || items[0].Content[0].Type != "reasoning_text" || items[0].Content[0].Text != "why" {
		t.Errorf("reasoning item = %+v", items[0])
	}
	if items[1].Type != "message" || items[1].Role != "assistant" || items[1].Content[0].Type != "output_text" {
		t.Errorf("assistant message = %+v", items[1])
	}
	if items[2].Type != "function_call" || items[2].CallID != "c1" || items[2].Name != "W" || items[2].Arguments != `{"a":1}` {
		t.Errorf("function_call = %+v", items[2])
	}
	if items[3].Type != "function_call_output" || items[3].CallID != "c1" || items[3].Output == nil || *items[3].Output != "res" {
		t.Errorf("function_call_output = %+v", items[3])
	}
	if items[4].Type != "message" || items[4].Role != "user" {
		t.Errorf("user message = %+v", items[4])
	}
	if len(items[4].Content) != 2 || items[4].Content[0].Type != "input_text" || items[4].Content[1].Type != "input_image" ||
		items[4].Content[1].ImageURL != "data:image/png;base64,QUJD" {
		t.Errorf("user content = %+v", items[4].Content)
	}

	// Empty assistant text contributes nothing; a redacted block is dropped.
	items2 := translateResponsesItems([]types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, Text: ""},
		{Type: types.ContentTypeRedacted, Data: "blob"},
	}}})
	if len(items2) != 0 {
		t.Errorf("empty assistant turn = %+v, want no items", items2)
	}
}

// Request assembly: max_output_tokens defaults to 32768, tools carry
// tool_choice auto, prompt_cache_key rides the state key, stop sequences are
// dropped, and reasoning maps from the effort axis.
func TestResponsesTranslateRequest(t *testing.T) {
	t.Parallel()

	p := &ResponsesProvider{name: "t", model: "glm-default"}
	body, err := p.translateRequest(&Request{
		Messages:       []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}},
		StopSequences:  []string{"END"},
		PromptStateKey: &PromptStateKey{QuerySource: "repl_main_thread"},
		Tools:          []ToolDef{{Name: "F", Description: "d", InputSchema: json.RawMessage(`{}`)}},
	}, true)
	if err != nil {
		t.Fatalf("translateRequest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body: %v", err)
	}
	if m["max_output_tokens"] != float64(32768) {
		t.Errorf("default max_output_tokens = %v, want 32768", m["max_output_tokens"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v", m["stream"], m["store"])
	}
	if m["prompt_cache_key"] != "repl_main_thread" {
		t.Errorf("prompt_cache_key = %v", m["prompt_cache_key"])
	}
	if _, hasStop := m["stop"]; hasStop {
		t.Error("stop_sequences leaked into the Responses body")
	}
	if m["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v", m["tool_choice"])
	}
	if _, hasReasoning := m["reasoning"]; hasReasoning {
		t.Error("auto effort must omit the reasoning field")
	}

	body2, _ := p.translateRequest(&Request{
		Model:     "glm-4.6",
		MaxTokens: 77,
		Thinking:  EffortHigh,
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}},
	}, false)
	var m2 map[string]any
	_ = json.Unmarshal(body2, &m2)
	if m2["max_output_tokens"] != float64(77) {
		t.Errorf("explicit max_output_tokens = %v", m2["max_output_tokens"])
	}
	if r, ok := m2["reasoning"].(map[string]any); !ok || r["effort"] != "high" {
		t.Errorf("reasoning = %v, want effort high", m2["reasoning"])
	}
}

// Stop-reason derivation: incomplete+max_output_tokens keeps the recovery
// trigger; a function call beats end_turn; otherwise end_turn.
func TestMapResponsesStopReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status, reason string
		sawCall        bool
		want           string
	}{
		{"incomplete", "max_output_tokens", false, "max_tokens"},
		{"incomplete", "content_filter", false, "end_turn"},
		{"completed", "", true, "tool_use"},
		{"completed", "", false, "end_turn"},
		{"incomplete", "max_output_tokens", true, "max_tokens"},
	}
	for _, tc := range cases {
		if got := mapResponsesStopReason(tc.status, tc.reason, tc.sawCall); got != tc.want {
			t.Errorf("mapResponsesStopReason(%q,%q,%v) = %q, want %q", tc.status, tc.reason, tc.sawCall, got, tc.want)
		}
	}
}

// Non-streaming translation: reasoning summaries and content concatenate
// without separator; messages emit text blocks; function calls map to tool
// blocks; failed and non-max incomplete bodies error.
func TestResponsesTranslateResponse(t *testing.T) {
	t.Parallel()

	p := &ResponsesProvider{}
	body := `{"id":"r1","model":"glm","status":"completed","output":[
		{"type":"reasoning","summary":[{"type":"summary_text","text":"s1"},{"type":"summary_text","text":"s2"}],"content":[{"type":"reasoning_text","text":"r1"},{"type":"text","text":"r2"}]},
		{"type":"message","content":[{"type":"output_text","text":"hello "},{"type":"output_text","text":"world"},{"type":"other","text":"x"}]},
		{"type":"function_call","call_id":"call_1","name":"W","arguments":"{\"a\":1}"}
	],"usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cached_tokens":30},"total_tokens":105}}`
	resp, err := p.translateResponse([]byte(body))
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(resp.Content) != 4 {
		t.Fatalf("content = %+v", resp.Content)
	}
	if resp.Content[0].Type != types.ContentTypeThinking || resp.Content[0].Thinking != "s1s2r1r2" {
		t.Errorf("thinking = %+v, want s1s2r1r2", resp.Content[0])
	}
	if resp.Content[1].Text != "hello " || resp.Content[2].Text != "world" {
		t.Errorf("text parts = %+v %+v", resp.Content[1], resp.Content[2])
	}
	if resp.Content[3].Type != types.ContentTypeToolUse || resp.Content[3].ID != "call_1" || string(resp.Content[3].Input) != `{"a":1}` {
		t.Errorf("tool = %+v", resp.Content[3])
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop = %q, want tool_use", resp.StopReason)
	}
	if resp.Usage.InputTokens != 70 || resp.Usage.CacheReadInputTokens != 30 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	// failed status with error object.
	_, err = p.translateResponse([]byte(`{"id":"r2","status":"failed","error":{"code":"x","message":"died","type":"server_error"}}`))
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Type != "server_error" || apiErr.Message != "died" {
		t.Errorf("failed err = %+v", err)
	}

	// failed status without error object falls back to the body.
	_, err = p.translateResponse([]byte(`{"id":"r3","status":"failed"}`))
	apiErr2, ok := err.(*APIError)
	if !ok || apiErr2.Message == "" {
		t.Errorf("fallback err = %+v", err)
	}

	// incomplete with a non-max reason is terminal.
	_, err = p.translateResponse([]byte(`{"id":"r4","status":"incomplete","incomplete_details":{"reason":"content_filter"}}`))
	if err == nil || !strings.Contains(err.Error(), "content_filter") {
		t.Errorf("incomplete err = %v", err)
	}

	// incomplete with max_output_tokens keeps the recovery stop reason.
	resp2, err := p.translateResponse([]byte(`{"id":"r5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`))
	if err != nil {
		t.Fatalf("max-output incomplete: %v", err)
	}
	if resp2.StopReason != "max_tokens" {
		t.Errorf("stop = %q, want max_tokens", resp2.StopReason)
	}
}

// Error classification mirrors codex's priority chain.
func TestResponsesStreamAPIError_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code, msg  string
		typ        string
		retry      bool
		retryAfter time.Duration
	}{
		{"context_length_exceeded", "too long", "prompt_too_long", false, 0},
		{"insufficient_quota", "no quota", "upstream", false, 0},
		{"cyber_policy", "flagged", "cyber_policy", false, 0},
		{"cyber_policy", "", "cyber_policy", false, 0},
		{"invalid_prompt", "", "invalid_request_error", false, 0},
		{"bio_policy", "", "invalid_request_error", false, 0},
		{"server_is_overloaded", "", "server_overloaded", true, 0},
		{"slow_down", "", "server_overloaded", true, 0},
		{"rate_limit_exceeded", "try again in 2.5s", "upstream", true, 2500 * time.Millisecond},
		{"unknown_code", "", "upstream", true, 0},
	}
	for _, tc := range cases {
		we := &responsesWireError{Code: tc.code, Message: tc.msg, Type: "upstream"}
		e := responsesStreamAPIError(we, []byte(`raw`))
		if e.Type != tc.typ || e.Retryable != tc.retry || e.RetryAfter != tc.retryAfter {
			t.Errorf("%s: type=%q retry=%v after=%v, want %q/%v/%v", tc.code, e.Type, e.Retryable, e.RetryAfter, tc.typ, tc.retry, tc.retryAfter)
		}
	}
	// Empty type defaults; empty message falls back to the raw payload.
	e := responsesStreamAPIError(&responsesWireError{Code: "c"}, []byte(`body text`))
	if e.Type != "api_error" || e.Message != "body text" {
		t.Errorf("defaults: %+v", e)
	}
	// Whitespace-only cyber_policy message gets the fixed wording.
	e2 := responsesStreamAPIError(&responsesWireError{Code: "cyber_policy", Message: "   "}, nil)
	if e2.Message != "This request has been flagged for possible cybersecurity risk." {
		t.Errorf("cyber fallback = %q", e2.Message)
	}
}

// Retry-after parsing: seconds (fractional), milliseconds (truncated), no
// match, non-rate-limit code, and overflow.
func TestTryParseRetryAfterVariants(t *testing.T) {
	t.Parallel()

	if d, ok := tryParseRetryAfter("rate_limit_exceeded", "Please try again in 11.054s"); !ok || d != 11054*time.Millisecond {
		t.Errorf("seconds: %v %v", d, ok)
	}
	if d, ok := tryParseRetryAfter("rate_limit_exceeded", "try again in 2 seconds"); !ok || d != 2*time.Second {
		t.Errorf("seconds word: %v %v", d, ok)
	}
	if d, ok := tryParseRetryAfter("rate_limit_exceeded", "try again in 250ms"); !ok || d != 250*time.Millisecond {
		t.Errorf("millis: %v %v", d, ok)
	}
	if d, ok := tryParseRetryAfter("rate_limit_exceeded", "try again in 2.7ms"); !ok || d != 2*time.Millisecond {
		t.Errorf("millis truncate: %v %v", d, ok)
	}
	if _, ok := tryParseRetryAfter("rate_limit_exceeded", "no delay mentioned"); ok {
		t.Error("no match must report false")
	}
	if _, ok := tryParseRetryAfter("other_code", "try again in 1s"); ok {
		t.Error("non-rate-limit code must report false")
	}
}

func TestIncompleteResponseError(t *testing.T) {
	t.Parallel()

	e := incompleteResponseError("content_filter")
	if e.Retryable || !strings.Contains(e.Message, "content_filter") {
		t.Errorf("err = %+v", e)
	}
	e2 := incompleteResponseError("")
	if !strings.Contains(e2.Message, "unknown") {
		t.Errorf("empty reason = %q, want unknown", e2.Message)
	}
}

// output_item.done text: reasoning = summary then content; message =
// output_text parts only.
func TestResponsesItemDoneText(t *testing.T) {
	t.Parallel()

	r := &responsesItem{Type: "reasoning",
		Summary: []responsesSummaryPart{{Type: "summary_text", Text: "s"}, {Type: "other", Text: "x"}},
		Content: []responsesContentPart{{Type: "reasoning_text", Text: "a"}, {Type: "text", Text: "b"}, {Type: "output_text", Text: "skip"}}}
	if got := responsesItemDoneText(r); got != "sab" {
		t.Errorf("reasoning done text = %q, want sab", got)
	}
	m := &responsesItem{Type: "message",
		Content: []responsesContentPart{{Type: "output_text", Text: "x"}, {Type: "input_text", Text: "skip"}}}
	if got := responsesItemDoneText(m); got != "x" {
		t.Errorf("message done text = %q, want x", got)
	}
	if got := responsesItemDoneText(&responsesItem{Type: "function_call"}); got != "" {
		t.Errorf("function_call done text = %q", got)
	}
}

func TestResponsesItemKindMapping(t *testing.T) {
	t.Parallel()

	if responsesItemKind("reasoning") != "thinking" || responsesItemKind("message") != "text" || responsesItemKind("function_call") != "" {
		t.Error("responsesItemKind mapping broken")
	}
}

// Summary deltas stream as thinking; the done text backfills only the
// undelivered suffix; a second summary index tracks separately.
func TestResponsesSSE_SummaryBackfill(t *testing.T) {
	p := newResponsesTestProvider()
	body := sseBody(
		`data: {"type":"response.created","response":{"id":"r1","model":"glm"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"sum"}`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","summary_index":0,"text":"summary tail"}`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":1,"delta":"two"}`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","summary_index":1,"text":"twost"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":10,"output_tokens":2}}}`,
	)
	events, err := collectResponsesEvents(context.Background(), p, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var thinking []string
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "thinking_delta" {
			thinking = append(thinking, e.Delta.Thinking)
		}
	}
	want := []string{"sum", "mary tail", "two", "st"}
	if len(thinking) != len(want) {
		t.Fatalf("thinking deltas = %v, want %v", thinking, want)
	}
	for i := range want {
		if thinking[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, thinking[i], want[i])
		}
	}
	// Block must be closed before the terminal message_delta.
	stopIdx, deltaIdx := -1, -1
	for i, e := range events {
		if e.Type == "content_block_stop" {
			stopIdx = i
		}
		if e.Type == "message_delta" {
			deltaIdx = i
		}
	}
	if stopIdx == -1 || deltaIdx == -1 || stopIdx > deltaIdx {
		t.Errorf("close (idx %d) must precede message_delta (idx %d)", stopIdx, deltaIdx)
	}
}

// Raw reasoning_text deltas accumulate; the item done text dedups against
// delivered bytes.
func TestResponsesSSE_ReasoningTextDeltas(t *testing.T) {
	p := newResponsesTestProvider()
	body := sseBody(
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_9","output_index":0,"delta":"abc"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_9","type":"reasoning","content":[{"type":"reasoning_text","text":"abcdef"}]}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	)
	events, _ := collectResponsesEvents(context.Background(), p, body)
	var deltas []string
	sawStop := false
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil {
			deltas = append(deltas, e.Delta.Thinking)
		}
		if e.Type == "content_block_stop" {
			sawStop = true
		}
	}
	if len(deltas) != 2 || deltas[0] != "abc" || deltas[1] != "def" {
		t.Errorf("deltas = %v, want [abc def] (done backfills only the suffix)", deltas)
	}
	if !sawStop {
		t.Error("reasoning block not closed at item done")
	}
}

// Message done without any prior state opens the block and emits the full
// text; a done item with no text creates nothing.
func TestResponsesSSE_MessageDoneWithoutDeltas(t *testing.T) {
	p := newResponsesTestProvider()
	body := sseBody(
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"m_1","type":"message","content":[{"type":"output_text","text":"whole"}]}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"m_2","type":"message","content":[]}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	)
	events, _ := collectResponsesEvents(context.Background(), p, body)
	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)
	if d := events[2].Delta; d == nil || d.Text != "whole" {
		t.Errorf("delta = %+v, want whole", events[2].Delta)
	}
}

// Function-call argument deltas open the block defensively when the added
// event went missing; the done arguments ride whole when no deltas streamed.
func TestResponsesSSE_FunctionCallPaths(t *testing.T) {
	p := newResponsesTestProvider()
	// Missing output_item.added: delta opens the block with the event's call_id.
	body := sseBody(
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"call_id":"call_x","delta":"{\"a\":"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"1}"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	)
	events, _ := collectResponsesEvents(context.Background(), p, body)
	if len(events) < 2 || events[1].Type != "content_block_start" {
		t.Fatalf("defensive open missing: %v", eventTypes(events))
	}
	if cb := events[1].ContentBlock; cb == nil || cb.Type != types.ContentTypeToolUse || cb.ID != "call_x" {
		t.Errorf("defensive block = %+v, want call_x", events[1].ContentBlock)
	}
	deltas := 0
	for _, e := range events {
		if e.Delta != nil && e.Delta.Type == "input_json_delta" {
			deltas++
		}
	}
	if deltas != 2 {
		t.Errorf("arg deltas = %d, want 2", deltas)
	}

	// Done without deltas emits the full arguments once.
	body2 := sseBody(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_2","type":"function_call","call_id":"call_y","name":"W"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_2","type":"function_call","call_id":"call_y","name":"W","arguments":"{\"b\":2}"}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	)
	events2, _ := collectResponsesEvents(context.Background(), p, body2)
	args := ""
	for _, e := range events2 {
		if e.Delta != nil && e.Delta.Type == "input_json_delta" {
			args += e.Delta.PartialJSON
		}
	}
	if args != `{"b":2}` {
		t.Errorf("done arguments = %q, want {\"b\":2}", args)
	}
	var start StreamEvent
	for _, e := range events2 {
		if e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeToolUse {
			start = e
		}
	}
	if start.ContentBlock == nil || start.ContentBlock.ID != "call_y" || start.ContentBlock.Name != "W" {
		t.Errorf("tool start = %+v", start.ContentBlock)
	}
}

// Argument overflow aborts without message_stop; exactly-at-limit passes.
func TestResponsesSSE_FunctionArgsLimit(t *testing.T) {
	// NOT parallel — modifies package-level maxToolArgumentsSize.
	orig := maxToolArgumentsSize
	maxToolArgumentsSize = 10
	defer func() { maxToolArgumentsSize = orig }()

	p := newResponsesTestProvider()
	over := sseBody(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"c","name":"W"}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc","output_index":0,"delta":"12345"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc","output_index":0,"delta":"678901"}`,
	)
	events, _ := collectResponsesEvents(context.Background(), p, over)
	for _, e := range events {
		if e.Type == "message_stop" {
			t.Error("over-limit args must abort without message_stop")
		}
	}

	atLimit := sseBody(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"c","name":"W"}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc","output_index":0,"delta":"1234567890"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	)
	events2, _ := collectResponsesEvents(context.Background(), p, atLimit)
	sawStop := false
	for _, e := range events2 {
		if e.Type == "message_stop" {
			sawStop = true
		}
	}
	if !sawStop {
		t.Error("exactly-at-limit args must complete the stream")
	}
}

// Terminal events: completed without a response field keeps scanning;
// unparseable/missing-id completed errors; incomplete with non-max reason
// errors; failed and top-level error events classify.
func TestResponsesSSE_TerminalEvents(t *testing.T) {
	p := newResponsesTestProvider()

	// completed with empty response: continue scanning, clean EOF ends quietly.
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(`data: {"type":"response.completed"}`))
	if len(events) != 0 {
		t.Errorf("empty completed emitted events: %v", eventTypes(events))
	}

	// unparseable completed payload → error event, no message_stop.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(`data: {"type":"response.completed","response":"not an object"}`))
	if len(events) == 0 || events[len(events)-1].Type == "message_stop" {
		t.Fatalf("unparseable completed: %v", eventTypes(events))
	}
	last := events[len(events)-1]
	if last.Type != "error" || last.Error == nil || !strings.Contains(last.Error.Message, "failed to parse ResponseCompleted") {
		t.Errorf("error event = %+v", last)
	}

	// completed without id is a hard error too.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(`data: {"type":"response.completed","response":{"status":"completed"}}`))
	last2 := events[len(events)-1]
	if last2.Type != "error" || !strings.Contains(last2.Error.Message, "missing id") {
		t.Errorf("missing-id error = %+v", last2)
	}

	// incomplete non-max reason → terminal error.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(`data: {"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"content_filter"}}}`))
	last3 := events[len(events)-1]
	if last3.Type != "error" || !strings.Contains(last3.Error.Message, "content_filter") {
		t.Errorf("incomplete error = %+v", last3)
	}

	// incomplete max_output_tokens → terminal state with recovery stop reason.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(`data: {"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`))
	found := false
	for _, e := range events {
		if e.Type == "message_delta" && e.DeltaMsg != nil && e.DeltaMsg.StopReason == "max_tokens" {
			found = true
		}
	}
	if !found {
		t.Errorf("max-output incomplete missing recovery stop: %v", eventTypes(events))
	}

	// response.failed with structured error.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(`data: {"type":"response.failed","response":{"id":"r","error":{"code":"cyber_policy","message":"flagged"}}}`))
	last4 := events[len(events)-1]
	if last4.Type != "error" || last4.Error.Type != "cyber_policy" {
		t.Errorf("failed error = %+v", last4.Error)
	}

	// top-level error event, flat shape.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(`data: {"type":"error","code":"rate_limit_exceeded","message":"try again in 3s"}`))
	last5 := events[len(events)-1]
	if last5.Type != "error" || last5.Error.RetryAfter != 3*time.Second {
		t.Errorf("flat error = %+v", last5.Error)
	}

	// [DONE] before any terminal event closes blocks and stops.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","item_id":"m","output_index":0,"delta":"x"}`,
		`data: [DONE]`,
	))
	types := eventTypes(events)
	if types[len(types)-1] != "message_stop" {
		t.Errorf("[DONE] tail = %v", types)
	}
}

// A stream that never sends response.created still begins with message_start
// synthesized on the first emitted event.
func TestResponsesSSE_SyntheticStart(t *testing.T) {
	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","item_id":"m","output_index":0,"delta":"hi"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	if len(events) == 0 || events[0].Type != "message_start" {
		t.Fatalf("no synthetic message_start: %v", eventTypes(events))
	}
	if events[0].Message == nil || !strings.HasPrefix(events[0].Message.ID, "msg_") {
		t.Errorf("synthetic start = %+v", events[0].Message)
	}
}

// No-op event kinds are ignored entirely.
func TestResponsesSSE_NoOps(t *testing.T) {
	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.in_progress"}`,
		`data: {"type":"response.content_part.added"}`,
		`data: {"type":"response.content_part.done"}`,
		`data: {"type":"response.reasoning_text.done"}`,
		`data: {"type":"response.reasoning_summary_part.added"}`,
		`data: {"type":"response.output_text.done"}`,
		`data: {"type":"response.function_call_arguments.done"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	assertEventTypes(t, events, "message_start", "message_delta", "message_stop")
}

// Scanner buffer: under-cap lines parse, over-cap fail.
func TestResponsesSSE_BufferBoundaries(t *testing.T) {
	p := newResponsesTestProvider()
	ok := strings.Repeat("y", 10485000)
	events, _ := collectResponsesEvents(context.Background(), p, strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"output_index\":0,\"delta\":\""+ok+"\"}\n\n"))
	if len(events) < 3 {
		t.Fatalf("under-cap line not parsed: %d events", len(events))
	}
	over := strings.Repeat("z", 10490000)
	_, err := collectResponsesEvents(context.Background(), p, strings.NewReader("data: "+over+"\n\n"))
	if err == nil {
		t.Error("over-cap line must fail the scanner")
	}
}

// Complete error paths: image guard, transport wrap, HTTP error parse,
// unmarshalable body.
func TestResponsesComplete_ErrorPaths(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded","message":"too long"}}`))
	})
	defer srv.Close()

	_, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 400 || apiErr.ErrorCode != "prompt_too_long" {
		t.Errorf("http error = %+v", err)
	}

	p2, srv2 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	defer srv2.Close()
	_, err = p2.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil || !strings.HasPrefix(err.Error(), "unmarshal response:") {
		t.Errorf("unmarshal err = %v", err)
	}

	p3 := &ResponsesProvider{name: "t", httpClient: &http.Client{}, idleTimeout: DefaultSSETimeout, apiKey: "k", baseURL: "http://127.0.0.1:1"}
	_, err = p3.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil || !strings.HasPrefix(err.Error(), "send request:") {
		t.Errorf("send err = %v", err)
	}
}

// Complete happy path pins the wire body: endpoint, headers, store=false.
func TestResponsesComplete_Wire(t *testing.T) {
	t.Parallel()

	// The handler goroutine and these assertions share the captures with no
	// happens-before edge through the HTTP chain; mu supplies it.
	var mu sync.Mutex
	var gotReq *http.Request
	var gotBody []byte
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotReq = r
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"r","model":"glm","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`))
	})
	defer srv.Close()

	resp, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 5, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "done" || resp.StopReason != "end_turn" {
		t.Errorf("resp = %+v", resp)
	}
	mu.Lock()
	reqSnapshot := gotReq
	bodySnapshot := gotBody
	mu.Unlock()
	if reqSnapshot == nil || reqSnapshot.Header.Get("Authorization") != "Bearer key" {
		t.Error("auth header missing")
	}
	if !strings.HasSuffix(reqSnapshot.URL.Path, "/responses") {
		t.Errorf("path = %q, want /responses", reqSnapshot.URL.Path)
	}
	var m map[string]any
	_ = json.Unmarshal(bodySnapshot, &m)
	if m["store"] != false || m["stream"] != false {
		t.Errorf("wire body store/stream = %v/%v", m["store"], m["stream"])
	}
}

// Retryable statuses retry until success; a zero budget makes exactly one
// attempt whose failure wraps the APIError.
func TestResponsesStream_RetryBehavior(t *testing.T) {
	t.Parallel()

	// hits is written on the server goroutine and read by the assertions below
	// with no happens-before edge; the increment+branch must be one critical
	// section.
	var mu sync.Mutex
	hits := 0
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		early := hits < 2
		mu.Unlock()
		if early {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n"))
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 2, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits != 2 {
		t.Errorf("hits = %d, want 2 (one 502 then success)", gotHits)
	}

	hits2 := 0
	p2, srv2 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits2++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	})
	defer srv2.Close()
	p2.retryConfig = &RetryConfig{MaxRetries: 0, BaseBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second}
	_, err = p2.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil || !strings.HasPrefix(err.Error(), "max retries exceeded:") {
		t.Fatalf("err = %v, want max retries exceeded", err)
	}
	mu.Lock()
	gotHits2 := hits2
	mu.Unlock()
	if gotHits2 != 1 {
		t.Errorf("zero-budget hits = %d, want 1", gotHits2)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Errorf("chain lacks 503 APIError: %v", err)
	}
}

// Non-retryable status fails immediately with the parsed error.
func TestResponsesStream_NonRetryableImmediate(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read.
	var mu sync.Mutex
	hits := 0
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"x","message":"bad key","type":"auth"}}`))
	})
	defer srv.Close()

	_, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 401 || apiErr.Message != "bad key" || apiErr.Retryable {
		t.Errorf("err = %+v, want non-retryable 401", err)
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits != 1 {
		t.Errorf("hits = %d, want 1", gotHits)
	}
}

// Backoff happens only before retries; cancellation during it aborts before
// the next request.
func TestResponsesStream_BackoffOnlyOnRetry(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read.
	var mu sync.Mutex
	hits := 0
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 1, BaseBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second}
	// 2s, not 150ms: under make-check's full load the first request can
	// spend >150ms before the handler runs, and then the deadline expires
	// with hits=0 — a load artifact, not a backoff regression. 2s stays
	// far under the 30s backoff, so "backoff before first attempt" still
	// yields hits=0 and any backoff shrink below 2s still yields hits>=2.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil {
		t.Fatal("nil error")
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits != 1 {
		t.Errorf("hits = %d, want 1 — first attempt runs without backoff", gotHits)
	}
}

// A retryable 503 carrying Retry-After must gate the retry on the server's
// advice (BUG-1b regression): Retry-After: 3600 against a 5ms base holds the
// second hit past any test deadline; a dropped header retries within ~7ms.
func TestResponsesStream_RetryAfterHeaderHonored(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read; the
	// increment+branch must stay one critical section.
	var mu sync.Mutex
	hits := 0
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		later := hits > 1
		mu.Unlock()
		if later {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"overloaded","type":"server_error"}}`))
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 2, BaseBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}

	// 2s, not 500ms: the deadline only has to beat the advised 3600s, and
	// under full load the first request can spend >500ms before the handler
	// runs, which would expire the deadline with hits=0.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil {
		t.Fatal("stream succeeded: Retry-After 3600 was not honored (second hit inside deadline)")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits != 1 {
		t.Errorf("hits = %d, want 1 — retry must wait the advised 3600s", gotHits)
	}
}

// The drained error body's headers are the last place Retry-After surfaces;
// a zero-retry Stream must still surface it on the wrapped APIError.
func TestResponsesStream_ParsesRetryAfterHeader(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"server_error"}}`))
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 0, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	_, err := p.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil {
		t.Fatal("nil error")
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("err = %v, want wrapped APIError with RetryAfter 7s", err)
	}

	// Negative integer advice is parsed but rejected by the sec > 0 guard;
	// non-numeric advice is not advice at all. Both leave RetryAfter at 0
	// so jittered backoff applies.
	for _, ra := range []string{"-3", "soon"} {
		p2, srv2 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", ra)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"x","type":"server_error"}}`))
		})
		p2.retryConfig = &RetryConfig{MaxRetries: 0, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
		_, err = p2.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
		if err == nil {
			t.Fatal("nil error")
		}
		apiErr2, ok := errors.AsType[*APIError](err)
		if !ok || apiErr2.RetryAfter != 0 {
			t.Fatalf("Retry-After %q: err = %v, want wrapped APIError with RetryAfter 0", ra, err)
		}
		srv2.Close()
	}
}

// Retry-After: 1 is the smallest advised delay that must beat a 5ms base —
// pinning the sec > 0 boundary against off-by-one mutations of the guard.
func TestResponsesStream_RetryAfterOneSecondBeatsBase(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read.
	var mu sync.Mutex
	hits := 0
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"x","type":"server_error"}}`))
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 2, BaseBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil {
		t.Fatal("stream succeeded: Retry-After 1s was not honored")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	// The ctx window is a detection band (it must stay under the advised 1s
	// so an honored Retry-After holds the second hit past it), so it cannot
	// be widened for load; instead poll: under full load the deadline can
	// expire mid-request and the handler's increment lands after Stream
	// returned.
	if !waitUntil(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits == 1
	}) {
		mu.Lock()
		gotHits := hits
		mu.Unlock()
		t.Errorf("hits = %d, want 1 — retry must wait the advised 1s", gotHits)
	}
}

// The Stream retry drain caps error bodies at exactly 1MB, matching the
// openai postChat cap: oversized garbage surfaces as a Message truncated at
// the cap rather than the full body.
func TestResponsesStream_ErrorBodyCap(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2<<20)) // 2MB of non-JSON garbage
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 0, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	_, err := p.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil {
		t.Fatal("nil error")
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || len(apiErr.Message) != 1<<20 {
		t.Fatalf("message length = %d, want exactly 1MB cap", len(apiErr.Message))
	}
}

// A canceled context surfaces as a chained context error.
func TestResponsesStream_CanceledCtxChains(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err == nil {
		t.Fatal("nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want chained context.Canceled", err)
	}
}

// Zero idle timeout: no reader timeout, slow gaps survive.
func TestResponsesStream_ZeroIdleTimeout(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n"))
		w.(http.Flusher).Flush()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	sawStop := false
	for evt := range ch {
		if evt.Error != nil {
			t.Fatalf("unexpected error: %v", evt.Error)
		}
		if evt.Type == "message_stop" {
			sawStop = true
		}
	}
	if !sawStop {
		t.Error("stream with idleTimeout=0 must survive the gap")
	}
}

// The parser goroutine delivers events while the stream is in flight.
func TestResponsesStream_ParserDeliversEarly(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"))
		w.(http.Flusher).Flush()
		// Hold scales with the client window (2:1): a parser that only
		// forwards after the stream ends still cannot deliver inside the
		// window, so the concurrency contract keeps its resolution.
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n"))
		w.(http.Flusher).Flush()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	select {
	case evt, ok := <-ch:
		if !ok || evt.Type != "message_start" {
			t.Fatalf("first = %+v ok=%v, want message_start", evt, ok)
		}
		// 1s, not 200ms: under make-check's full load (race detector +
		// parallel packages) the parser goroutine can lose 200ms to
		// scheduling. Still half the server's 2s hold, so a batch-mode
		// parser cannot sneak inside the window.
	case <-time.After(1 * time.Second):
		t.Fatal("no early delivery — parser not concurrent")
	}
	for range ch {
	}
}

// Cancelling mid-stream closes the channel promptly.
func TestResponsesStream_CancelClosesChannel(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 80 {
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"output_index\":0,\"delta\":\"x\"}\n\n"))
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("closed before first event")
	}
	cancel()
	closed := make(chan struct{})
	go func() {
		for range ch {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
	srv.Close()
}

// Malformed chunks warn (truncated at 200) and are skipped; non-data lines
// are silent; an added event without its item payload is ignored.
func TestResponsesSSE_ParseHygiene(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLog)

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		"event: response.created",
		": keepalive",
		"",
		`data: {bad `+strings.Repeat("q", 300),
		`data: {"type":"response.output_item.added","output_index":0}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	logged := logBuf.String()
	if !strings.Contains(logged, "failed to parse chunk") {
		t.Errorf("no warn for malformed chunk: %q", logged)
	}
	if !strings.Contains(logged, strings.Repeat("q", 195)+"...") {
		t.Errorf("chunk warn data not truncated at 200: %.120q", logged)
	}
	if strings.Contains(logged, strings.Repeat("q", 196)+"...") {
		t.Error("chunk warn data truncated past 200")
	}
	// The item-less added event produced no block; the stream still completes.
	for _, e := range events {
		if e.Type == "content_block_start" {
			t.Error("item-less added event must not open a block")
		}
	}
}

// response.created enriches the synthesized message_start with the response
// id and model; the fallback start (no created) carries the request model.
func TestResponsesSSE_CreatedFields(t *testing.T) {
	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.created","response":{"id":"resp_9","model":"glm-4.7"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_9"}}`,
	))
	if len(events) == 0 || events[0].Message == nil {
		t.Fatal("no message_start")
	}
	if events[0].Message.ID != "resp_9" || events[0].Message.Model != "glm-4.7" || events[0].Message.Role != "assistant" {
		t.Errorf("created start = %+v", events[0].Message)
	}

	events2, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","item_id":"m","output_index":0,"delta":"x"}`,
		`data: {"type":"response.completed","response":{"id":"r"}}`,
	))
	if events2[0].Message == nil || !strings.HasPrefix(events2[0].Message.ID, "msg_") || events2[0].Message.Role != "assistant" {
		t.Errorf("fallback start = %+v", events2[0].Message)
	}
}

// Interleaved items keep their own block indices; a function_call item marks
// the stop reason as tool_use at completion.
func TestResponsesSSE_ItemIndicesAndStopReason(t *testing.T) {
	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"a","type":"reasoning"}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"b","type":"message"}}`,
		`data: {"type":"response.reasoning_text.delta","item_id":"a","delta":"th"}`,
		`data: {"type":"response.output_text.delta","item_id":"b","delta":"tx"}`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"id":"c","type":"function_call","call_id":"k","name":"W"}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	indices := map[string]int{}
	for _, e := range events {
		if e.Delta != nil && e.Delta.Type == "thinking_delta" {
			indices["thinking"] = e.Index
		}
		if e.Delta != nil && e.Delta.Type == "text_delta" {
			indices["text"] = e.Index
		}
	}
	if indices["thinking"] != 0 || indices["text"] != 1 {
		t.Errorf("delta indices = %v, want thinking 0 / text 1", indices)
	}
	stop := ""
	for _, e := range events {
		if e.Type == "message_delta" && e.DeltaMsg != nil {
			stop = e.DeltaMsg.StopReason
		}
	}
	if stop != "tool_use" {
		t.Errorf("stop = %q, want tool_use after a function_call item", stop)
	}
}

// Residual-cleanup round 2. Each test targets SURVIVED keys from
// /home/yliu/mutate-llm/mutver-responses-readj.txt; kills verified by
// targeted mutver re-runs (see ledger). Tests that swap the default slog
// handler stay serial, matching TestResponsesSSE_ParseHygiene.

var nanVal = math.NaN()

// Non-data lines and empty-payload events are SKIPPED, never stream-ending:
// every guard-continue must keep the scan alive, and clean framing lines
// must not warn.
func TestResponsesSSE_SkipGuardsScanEverything(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLog)

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		"event: response.created",
		`data: {"type":"response.output_text.delta","item_id":"t","delta":""}`,
		"event: ping",
		`data: {"type":"response.output_text.delta","item_id":"t","delta":"a"}`,
		`data: {"type":"response.output_item.added","output_index":0}`,
		`data: {"type":"response.output_text.delta","item_id":"t","delta":"b"}`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"t","text":""}`,
		`data: {"type":"response.output_item.done","output_index":0}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	types := eventTypes(events)
	if len(types) == 0 || types[len(types)-1] != "message_stop" {
		t.Fatalf("terminal lost after skip lines: %v", types)
	}
	var text string
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Text != "" {
			text += e.Delta.Text
		}
	}
	if text != "ab" {
		t.Errorf("deltas after skip lines = %q, want ab", text)
	}
	if strings.Contains(logBuf.String(), "failed to parse chunk") {
		t.Errorf("clean framing lines warned: %s", logBuf.String())
	}
}

// message_start fields on both paths: the synthetic start carries msg_+random
// ID, the request model, and assistant role.
func TestResponsesSSE_MessageStartFields(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","item_id":"t","delta":"x"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	if len(events) == 0 || events[0].Type != "message_start" || events[0].Message == nil {
		t.Fatalf("no synthetic message_start: %v", eventTypes(events))
	}
	msg := events[0].Message
	if !strings.HasPrefix(msg.ID, "msg_") || len(msg.ID) <= len("msg_") {
		t.Errorf("synthetic ID = %q, want msg_+random", msg.ID)
	}
	if msg.Model != "glm-4.6" {
		t.Errorf("synthetic Model = %q, want glm-4.6", msg.Model)
	}
	if msg.Role != "assistant" {
		t.Errorf("synthetic Role = %q, want assistant", msg.Role)
	}

	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.created","response":{"id":"resp_9","model":"glm-4.7"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_9"}}`,
	))
	msg = events[0].Message
	if msg.ID != "resp_9" || msg.Model != "glm-4.7" || msg.Role != "assistant" {
		t.Errorf("created start = %+v", msg)
	}

	// created with an empty payload keeps the generated msg_ ID.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.created"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	msg = events[0].Message
	if !strings.HasPrefix(msg.ID, "msg_") || len(msg.ID) <= len("msg_") {
		t.Errorf("created-empty ID = %q, want msg_+random", msg.ID)
	}
	if msg.Model != "glm-4.6" {
		t.Errorf("created-empty Model = %q, want glm-4.6", msg.Model)
	}
}

// Item keys: lazy-open states key by item_id in ADD order (reverse-arriving
// deltas keep add-order indices); id-less deltas group by output_index; a
// done event carrying both item_id and item.id resolves to item_id.
// The overflow guard: a delay landing exactly on float64(MaxInt64) seconds
// is rejected, not converted (the > vs >= boundary of the guard).
func TestResponsesSSE_RetryAfterOverflowGuard(t *testing.T) {
	t.Parallel()

	if d, ok := tryParseRetryAfter("rate_limit_exceeded", "try again in 9223372036.854775807s"); ok {
		t.Errorf("overflow guard let %v through (ok=%v), want rejection", d, ok)
	}
	if d, ok := tryParseRetryAfter("rate_limit_exceeded", "try again in 9223372036.854776s"); ok {
		t.Errorf("rounded overflow let %v through, want rejection", d)
	}
}

func TestResponsesSSE_ItemKeyResolution(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_item.added","item":{"id":"a","type":"message"}}`,
		`data: {"type":"response.output_item.added","item":{"id":"b","type":"message"}}`,
		`data: {"type":"response.output_text.delta","item_id":"b","delta":"B"}`,
		`data: {"type":"response.output_text.delta","item_id":"a","delta":"A"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	var startIdx, deltaIdx []int
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			startIdx = append(startIdx, e.Index)
		case "content_block_delta":
			deltaIdx = append(deltaIdx, e.Index)
		}
	}
	if len(startIdx) != 2 || startIdx[0] != 1 || startIdx[1] != 0 {
		t.Errorf("start indices = %v, want [1 0] (add order a=0 b=1)", startIdx)
	}
	if len(deltaIdx) != 2 || deltaIdx[0] != 1 || deltaIdx[1] != 0 {
		t.Errorf("delta indices = %v, want [1 0]", deltaIdx)
	}

	// No item_id: output_index groups into distinct blocks.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"x"}`,
		`data: {"type":"response.output_text.delta","output_index":1,"delta":"y"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	startIdx = nil
	for _, e := range events {
		if e.Type == "content_block_start" {
			startIdx = append(startIdx, e.Index)
		}
	}
	if len(startIdx) != 2 || startIdx[0] != 0 || startIdx[1] != 1 {
		t.Errorf("index-keyed starts = %v, want [0 1]", startIdx)
	}

	// item_id beats item.id; a spurious state must not consume an index.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","item_id":"a","delta":"A"}`,
		`data: {"type":"response.output_item.done","item_id":"a","item":{"id":"WRONG","type":"message","content":[{"type":"output_text","text":"A"}]}}`,
		`data: {"type":"response.output_text.delta","item_id":"c","delta":"C"}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	startIdx = nil
	sawC := false
	for _, e := range events {
		if e.Type == "content_block_start" {
			startIdx = append(startIdx, e.Index)
		}
		if e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Text == "C" {
			sawC = true
		}
	}
	if !sawC {
		t.Fatalf("delta C lost: %v", eventTypes(events))
	}
	if len(startIdx) != 2 || startIdx[1] != 1 {
		t.Errorf("starts after item_id-keyed close = %v, want [0 1] with no index skip", startIdx)
	}
}

// Strict index sequence for a mixed thinking/text stream, including the
// summary backfill when deltas were lost and the terminal sweep for blocks
// whose done events never arrived.
func TestResponsesSSE_IndicesStrictMixed(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_item.added","item":{"id":"th","type":"reasoning"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"th","summary_index":0,"delta":"ab"}`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"th","summary_index":0,"delta":"cd"}`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"th","summary_index":0,"text":"abcd"}`,
		`data: {"type":"response.output_item.added","item":{"id":"th2","type":"reasoning"}}`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"th2","summary_index":0,"text":"backfill"}`,
		`data: {"type":"response.output_item.added","item":{"id":"tx","type":"message"}}`,
		`data: {"type":"response.output_text.delta","item_id":"tx","delta":"hello"}`,
		`data: {"type":"response.output_item.done","item":{"id":"tx","type":"message","text":"hello"}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	type ev struct {
		typ   string
		index int
		text  string
	}
	var got []ev
	for _, e := range events {
		switch e.Type {
		case "content_block_start", "content_block_stop":
			got = append(got, ev{e.Type, e.Index, ""})
		case "content_block_delta":
			txt := ""
			if e.Delta != nil {
				txt = e.Delta.Thinking + e.Delta.Text + e.Delta.PartialJSON
			}
			got = append(got, ev{e.Type, e.Index, txt})
		}
	}
	want := []ev{
		{"content_block_start", 0, ""},
		{"content_block_delta", 0, "ab"},
		{"content_block_delta", 0, "cd"},
		{"content_block_start", 1, ""},
		{"content_block_delta", 1, "backfill"},
		{"content_block_start", 2, ""},
		{"content_block_delta", 2, "hello"},
		{"content_block_stop", 2, ""},
		{"content_block_stop", 0, ""},
		{"content_block_stop", 1, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("event shape = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Tool-argument paths: the defensive open without output_item.added carries
// the call id exactly once; done items never re-emit deltas that already
// streamed (or skip args that did); the size limit warns and ends the stream;
// argsLen is a running sum; item-less done events are ignored.
func TestResponsesSSE_ToolArgsPaths(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLog)

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc","call_id":"call_9","delta":"{\"x\":"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc","delta":"1}"}`,
		`data: {"type":"response.output_item.done","item_id":"fc","item":{"id":"fc","type":"function_call","call_id":"call_9","name":"bash","arguments":"{\"x\":1}"}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	var starts, argsDeltas []StreamEvent
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			starts = append(starts, e)
		case "content_block_delta":
			argsDeltas = append(argsDeltas, e)
		}
	}
	if len(starts) != 1 || starts[0].Index != 0 || starts[0].ContentBlock == nil || starts[0].ContentBlock.ID != "call_9" {
		t.Errorf("defensive starts = %+v, want one at index 0 with id call_9", starts)
	}
	if len(argsDeltas) != 2 {
		t.Errorf("args deltas = %d, want exactly the two streamed (no re-emit, no dup open)", len(argsDeltas))
	}

	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_item.done","item":{"id":"fc2","type":"function_call","call_id":"call_2","name":"bash","arguments":"{\"a\":1}"}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	starts = nil
	argsDeltas = nil
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			starts = append(starts, e)
		case "content_block_delta":
			argsDeltas = append(argsDeltas, e)
		}
	}
	if len(starts) != 1 || starts[0].ContentBlock == nil || starts[0].ContentBlock.ID != "call_2" || starts[0].ContentBlock.Name != "bash" || starts[0].ContentBlock.Type != types.ContentTypeToolUse || starts[0].Index != 0 {
		t.Errorf("done-defensive start = %+v, want tool_use at index 0 with id+name", starts)
	}
	if len(argsDeltas) != 1 || argsDeltas[0].Delta == nil || argsDeltas[0].Delta.PartialJSON != `{"a":1}` || argsDeltas[0].Index != 0 {
		t.Errorf("done args = %+v, want exactly one full-args delta at index 0", argsDeltas)
	}
	stopIdx := -1
	for _, e := range events {
		if e.Type == "content_block_stop" {
			stopIdx = e.Index
		}
	}
	if stopIdx != 0 {
		t.Errorf("done-function-call stop index = %d, want 0", stopIdx)
	}
	var deltaMsg *MessageDelta
	for _, e := range events {
		if e.Type == "message_delta" {
			deltaMsg = e.DeltaMsg
		}
	}
	if deltaMsg == nil || deltaMsg.StopReason != "tool_use" {
		t.Errorf("done-only function_call stop reason = %+v, want tool_use", deltaMsg)
	}

	// argsLen is a running sum: three 4MB deltas trip the 10MB cap on the
	// third, not earlier.
	chunk := strings.Repeat("x", 4<<20)
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.function_call_arguments.delta","item_id":"big","call_id":"c","delta":"`+chunk+`"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"big","call_id":"c","delta":"`+chunk+`"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"big","call_id":"c","delta":"`+chunk+`"}`,
	))
	deltas := 0
	for _, e := range events {
		if e.Type == "content_block_delta" {
			deltas++
		}
	}
	if deltas != 2 {
		t.Errorf("deltas before cap = %d, want 2 (third trips the running sum)", deltas)
	}
	if !strings.Contains(logBuf.String(), "tool arguments exceed size limit") {
		t.Errorf("cap warn missing: %s", logBuf.String())
	}
	logBuf.Reset()

	// 6MB+6MB deltas trip the cap on the second, before its emission.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.function_call_arguments.delta","item_id":"big","call_id":"c","delta":"`+strings.Repeat("y", 6<<20)+`"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"big","call_id":"c","delta":"`+strings.Repeat("y", 6<<20)+`"}`,
	))
	deltas = 0
	for _, e := range events {
		if e.Type == "content_block_delta" {
			deltas++
		}
	}
	if deltas != 1 {
		t.Errorf("over-cap deltas = %d, want 1 (second trips the cap)", deltas)
	}
	if !strings.Contains(logBuf.String(), "tool arguments exceed size limit") {
		t.Errorf("single-shot cap warn missing: %s", logBuf.String())
	}

	// item-less done event: ignored without panic, stream still completes.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_item.done","output_index":3}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	if types := eventTypes(events); types[len(types)-1] != "message_stop" {
		t.Errorf("item-less done broke the stream: %v", types)
	}

	// a repeated function_call done never re-emits its args, and a text block
	// before the call pins the call's indices at 1.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","item_id":"pre","delta":"P"}`,
		`data: {"type":"response.output_item.done","item":{"id":"fc3","type":"function_call","call_id":"call_3","name":"bash","arguments":"{\"z\":9}"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"fc3","type":"function_call","call_id":"call_3","name":"bash","arguments":"{\"z\":9}"}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	args3 := 0
	fcStartIdx := -1
	for _, e := range events {
		switch e.Type {
		case "content_block_delta":
			if e.Delta != nil && e.Delta.PartialJSON != "" {
				args3++
				if e.Index != 1 {
					t.Errorf("fc args delta index = %d, want 1 (after the text block)", e.Index)
				}
			}
		case "content_block_start":
			if e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeToolUse {
				fcStartIdx = e.Index
			}
		}
	}
	if args3 != 1 {
		t.Errorf("repeated function_call done emitted %d args deltas, want 1", args3)
	}
	if fcStartIdx != 1 {
		t.Errorf("fc start index = %d, want 1", fcStartIdx)
	}
}

// Terminal error details: the completed-parse failure surfaces the real
// unmarshal error; failed-without-error falls back to a 200-truncated
// api_error; the error-event warn truncates its payload.
func TestResponsesSSE_TerminalErrorDetails(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLog)

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.completed","response":"not an object"}`,
	))
	last := events[len(events)-1]
	if last.Type != "error" || last.Error == nil || !strings.Contains(last.Error.Message, "cannot unmarshal") {
		t.Errorf("completed-parse error = %+v, want the real unmarshal text", last.Error)
	}

	raw := strings.Repeat("z", 250)
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.failed","response":"`+raw+`"}`,
	))
	last = events[len(events)-1]
	if last.Type != "error" || last.Error == nil {
		t.Fatalf("failed fallback event = %+v", last)
	}
	if last.Error.Type != "api_error" {
		t.Errorf("fallback type = %q, want api_error", last.Error.Type)
	}
	if len(last.Error.Message) != 203 || !strings.HasSuffix(last.Error.Message, "...") {
		t.Errorf("fallback message len = %d, want 203 (200+ellipsis)", len(last.Error.Message))
	}

	errLine := `{"type":"error","error":"` + strings.Repeat("y", 300) + `"}`
	_, _ = collectResponsesEvents(context.Background(), p, sseBody("data: "+errLine))
	logged := logBuf.String()
	if !strings.Contains(logged, "failed to parse error event") {
		t.Errorf("error-event warn missing: %s", logged)
	}
	yFit := 200 - len(`{"type":"error","error":"`)
	if !strings.Contains(logged, strings.Repeat("y", yFit)+"...") || strings.Contains(logged, strings.Repeat("y", yFit+1)+"...") {
		t.Errorf("error-event warn truncation wrong: %.120q", logged)
	}

	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"content_filter"}}}`,
	))
	last = events[len(events)-1]
	if last.Type != "error" || last.Error == nil || last.Error.Type != "api_error" || !strings.Contains(last.Error.Message, "content_filter") {
		t.Errorf("incomplete error = %+v, want api_error with reason", last.Error)
	}

	// failed with a parseable response but no error object: generic fallback.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.failed","response":{"id":"r1","model":"m"}}`,
	))
	last = events[len(events)-1]
	if last.Type != "error" || last.Error == nil || last.Error.Type != "api_error" || last.Error.Message == "" {
		t.Errorf("failed-no-error fallback = %+v, want api_error with raw message", last.Error)
	}

	// the completed-parse error event keeps its api_error type.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.completed","response":"not an object"}`,
	))
	last = events[len(events)-1]
	if last.Error == nil || last.Error.Type != "api_error" {
		t.Errorf("completed-parse error type = %+v, want api_error", last.Error)
	}

	// a parseable payload with NO id is also a hard error (not a terminal).
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.completed","response":{"model":"m"}}`,
	))
	last = events[len(events)-1]
	if last.Type != "error" || last.Error == nil || !strings.Contains(last.Error.Message, "missing id") {
		t.Errorf("missing-id completed = %+v, want missing-id error", last)
	}

	// dual-shape error event: the nested error object wins over the flat pair.
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"error","code":"FLAT","message":"flat","error":{"code":"NESTED","message":"nested"}}`,
	))
	last = events[len(events)-1]
	if last.Error == nil || !strings.Contains(last.Error.Message, "nested") {
		t.Errorf("dual-shape error = %+v, want nested values", last.Error)
	}

	// incomplete with an unparseable payload warns and degrades to unknown reason.
	logBuf.Reset()
	events, _ = collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.incomplete","response":"not an object"}`,
	))
	last = events[len(events)-1]
	if last.Type != "error" || last.Error == nil || !strings.Contains(last.Error.Message, "unknown") {
		t.Errorf("incomplete-garbage error = %+v, want unknown reason", last.Error)
	}
	if !strings.Contains(logBuf.String(), "failed to parse terminal payload") {
		t.Errorf("terminal-parse warn missing: %s", logBuf.String())
	}

	// stream error event with empty code/message: message falls back to the
	// 200-truncated raw payload.
	errEvt := `{"type":"error","code":"rate_limit_exceeded","message":"","pad":"` + strings.Repeat("p", 300) + `"}`
	events, _ = collectResponsesEvents(context.Background(), p, sseBody("data: "+errEvt))
	last = events[len(events)-1]
	if last.Type != "error" || last.Error == nil {
		t.Fatalf("raw-fallback error event = %+v", last)
	}
	pFit := 200 - len(`{"type":"error","code":"rate_limit_exceeded","message":"","pad":"`)
	if !strings.Contains(last.Error.Message, strings.Repeat("p", pFit)+"...") || len(last.Error.Message) != 203 {
		t.Errorf("raw fallback message len = %d, want 203 (200+ellipsis)", len(last.Error.Message))
	}
}

// translateResponsesItems guards: empty thinking emits no reasoning item,
// user thinking never does, nil-source images are skipped without panic, and
// content-less user messages produce no item.
func TestResponsesTranslateItems_Guards(t *testing.T) {
	t.Parallel()

	items := translateResponsesItems([]types.Message{{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hi"},
			{Type: types.ContentTypeThinking, Thinking: ""},
		},
	}})
	for _, it := range items {
		if it.Type == "reasoning" {
			t.Errorf("empty thinking produced a reasoning item: %+v", it)
		}
	}

	items = translateResponsesItems([]types.Message{{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeThinking, Thinking: "secret"}},
	}})
	if len(items) != 0 {
		t.Errorf("user thinking produced items: %+v", items)
	}

	items = translateResponsesItems([]types.Message{{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeImage}},
	}})
	if len(items) != 0 {
		t.Errorf("nil-source image produced items: %+v", items)
	}

	items = translateResponsesItems([]types.Message{{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "one"}},
	}})
	if len(items) != 1 || items[0].Role != "assistant" {
		t.Errorf("assistant text items = %+v, want one assistant item", items)
	}
}

// translateRequest debug logs: stop_sequences and tool_result image drops are
// logged with their payloads; clean requests stay silent.
func TestResponsesTranslateRequest_DebugLogs(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(oldLog)

	p := newResponsesTestProvider()
	base := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}

	body, err := p.translateRequest(&Request{Model: "m", MaxTokens: 1, Messages: base, StopSequences: []string{"END"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "dropping stop_sequences") || !strings.Contains(logged, "count=1") {
		t.Errorf("stop drop debug missing: %s", logged)
	}
	if bytes.Contains(body, []byte(`"stop"`)) {
		t.Errorf("stop field reached the wire: %s", body)
	}

	logBuf.Reset()
	if _, err := p.translateRequest(&Request{Model: "m", MaxTokens: 1, Messages: base}, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logBuf.String(), "dropping") {
		t.Errorf("clean request logged drops: %s", logBuf.String())
	}

	logBuf.Reset()
	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type:      types.ContentTypeToolResult,
			ToolUseID: "tu_1",
			Content:   json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]`),
		}},
	}}
	if _, err := p.translateRequest(&Request{Model: "m", MaxTokens: 1, Messages: msgs}, true); err != nil {
		t.Fatal(err)
	}
	logged = logBuf.String()
	if !strings.Contains(logged, "dropping tool_result image content") || !strings.Contains(logged, "tool_use_id=tu_1") || !strings.Contains(logged, "images=1") {
		t.Errorf("image drop debug missing: %s", logged)
	}

	logBuf.Reset()
	msgs[0].Content[0].Content = json.RawMessage(`[{"type":"text","text":"ok"}]`)
	if _, err := p.translateRequest(&Request{Model: "m", MaxTokens: 1, Messages: msgs}, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logBuf.String(), "dropping tool_result image content") {
		t.Errorf("text-only tool_result logged image drop: %s", logBuf.String())
	}

	logBuf.Reset()
	redacted := []types.Message{{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{{Type: types.ContentTypeRedacted, Data: "blob"}},
	}}
	if _, err := p.translateRequest(&Request{Model: "m", MaxTokens: 1, Messages: redacted}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logBuf.String(), "dropping redacted_thinking") {
		t.Errorf("redacted drop debug missing: %s", logBuf.String())
	}
}

// translateResponse guard rails: empty summaries/parts never contribute,
// failed-status bodies carry Status 200 with the truncated raw message, and
// the stop-reason table ignores max_output_tokens unless status is
// incomplete.
func TestResponsesTranslateResponse_Guards(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	body := []byte(`{"id":"r","output":[` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":""},{"type":"summary_text","text":"real summary"}],"content":[{"type":"reasoning_text","text":""},{"type":"text","text":"tail"}]},` +
		`{"type":"message","content":[{"type":"output_text","text":""},{"type":"output_text","text":"hello"}]}` +
		`],"usage":{"input_tokens":1,"output_tokens":2}}`)
	resp, err := p.translateResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("content = %+v, want thinking+text only", resp.Content)
	}
	if resp.Content[0].Type != types.ContentTypeThinking || resp.Content[0].Thinking != "real summarytail" {
		t.Errorf("thinking = %+v", resp.Content[0])
	}
	if resp.Content[1].Type != types.ContentTypeText || resp.Content[1].Text != "hello" {
		t.Errorf("text = %+v", resp.Content[1])
	}

	body = []byte(`{"id":"r","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":""}],"content":[]}]}`)
	resp, err = p.translateResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 0 {
		t.Errorf("empty reasoning emitted blocks: %+v", resp.Content)
	}

	long := strings.Repeat("w", 250)
	_, err = p.translateResponse([]byte(`{"id":"r","status":"failed","model":"m","error":null,"x":"` + long + `"}`))
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("failed status err = %v, want APIError", err)
	}
	if apiErr.Status != 200 {
		t.Errorf("failed Status = %d, want 200", apiErr.Status)
	}
	if len(apiErr.Message) != 203 || !strings.HasSuffix(apiErr.Message, "...") {
		t.Errorf("failed Message len = %d, want 203", len(apiErr.Message))
	}

	if got := mapResponsesStopReason("completed", "max_output_tokens", false); got != "end_turn" {
		t.Errorf("completed+max = %q, want end_turn", got)
	}
	if got := mapResponsesStopReason("incomplete", "", false); got != "end_turn" {
		t.Errorf("incomplete without reason = %q, want end_turn", got)
	}
	if got := mapResponsesStopReason("incomplete", "max_output_tokens", false); got != "max_tokens" {
		t.Errorf("incomplete+max = %q, want max_tokens", got)
	}
	if got := mapResponsesStopReason("completed", "", true); got != "tool_use" {
		t.Errorf("completed+call = %q, want tool_use", got)
	}
}

// parseHTTPError table: type/message extraction, empty-message fallback, the
// unparseable warn, and the status classifications.
func TestResponsesParseHTTPError_Table(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLog)

	p := newResponsesTestProvider()

	e := p.parseHTTPError([]byte(`{"error":{"type":"server_error","message":"slow down"}}`), 503)
	if e.Type != "api_error" || e.Message != "slow down" || !e.Retryable || e.ErrorCode != "api_error" {
		t.Errorf("503 = %+v (body message kept, 5xx type wins)", e)
	}

	e = p.parseHTTPError([]byte(`{"error":{"type":"x","message":""}}`), 503)
	if e.Message != `{"error":{"type":"x","message":""}}` {
		t.Errorf("empty message fallback = %q", e.Message)
	}

	e = p.parseHTTPError([]byte("garbage"), 502)
	if e.Message != "garbage" || !strings.Contains(logBuf.String(), "parse Responses error response failed") {
		t.Errorf("garbage = %+v / log %s", e, logBuf.String())
	}

	e = p.parseHTTPError([]byte(`{"error":{"code":"context_length_exceeded","message":"too long"}}`), 400)
	if e.Retryable || e.ErrorCode != "prompt_too_long" || e.Type != "prompt_too_long" {
		t.Errorf("400+context = %+v", e)
	}

	e = p.parseHTTPError([]byte(`{"error":{"code":"invalid_request","type":"invalid_request_error","message":"bad"}}`), 400)
	if e.Retryable || e.ErrorCode != "" || e.Type != "invalid_request_error" || e.Message != "bad" {
		t.Errorf("400+other = %+v, want generic 400 with body type kept", e)
	}

	// The prompt_too_long mapping requires BOTH 400 and the code: a 5xx body
	// carrying the same code stays a generic api_error.
	e = p.parseHTTPError([]byte(`{"error":{"code":"context_length_exceeded","message":"too long"}}`), 500)
	if e.ErrorCode != "api_error" || e.Type != "api_error" || !e.Retryable {
		t.Errorf("500+context-code = %+v, want generic api_error", e)
	}

	e = p.parseHTTPError([]byte(`{"error":{"message":"no auth"}}`), 401)
	if e.Retryable || e.ErrorCode != "authentication_error" {
		t.Errorf("401 = %+v", e)
	}

	e = p.parseHTTPError([]byte(`{"error":{"message":"denied"}}`), 403)
	if e.Retryable || e.ErrorCode != "permission_error" {
		t.Errorf("403 = %+v", e)
	}

	e = p.parseHTTPError([]byte(`{"error":{"message":"teapot"}}`), 499)
	if e.ErrorCode != "" {
		t.Errorf("499 ErrorCode = %q, want empty (only >=500 maps)", e.ErrorCode)
	}
	e = p.parseHTTPError([]byte(`{"error":{"message":"boom"}}`), 500)
	if e.ErrorCode != "api_error" || !e.Retryable {
		t.Errorf("500 = %+v", e)
	}
}

// Complete/Stream error surfaces: marshal failure (NaN temperature), merge
// failure (NaN extra param), oversized images, bad URL, transport wrap, and
// a read truncated mid-body.
func TestResponsesHTTP_ErrorSurfaces(t *testing.T) {
	t.Parallel()

	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"r"}`))
	})
	defer srv.Close()

	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}

	_, err := p.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs, Temperature: &nanVal})
	if err == nil || !strings.Contains(err.Error(), "marshal request") {
		t.Fatalf("NaN temperature err = %v", err)
	}
	if _, ok := errors.AsType[*json.UnsupportedValueError](err); !ok {
		t.Errorf("marshal error not unwrappable: %v", err)
	}

	p2, srv2 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	defer srv2.Close()
	p2.extraParams = map[string]any{"x": math.NaN()}
	_, err = p2.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err == nil || !strings.Contains(err.Error(), "merge extra params") {
		t.Fatalf("NaN extra err = %v", err)
	}
	if _, ok := errors.AsType[*json.UnsupportedValueError](err); !ok {
		t.Errorf("merge error not unwrappable: %v", err)
	}

	// No happens-before between the handler goroutines and these reads.
	var mu sync.Mutex
	var body []byte
	p3, srv3 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"r"}`))
	})
	defer srv3.Close()
	p3.extraParams = map[string]any{"custom": true}
	if _, err := p3.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	bodySnapshot := body
	mu.Unlock()
	if !bytes.Contains(bodySnapshot, []byte(`"custom":true`)) {
		t.Errorf("1-key extraParams missing from wire: %s", bodySnapshot)
	}

	hits := 0
	p4, srv4 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	})
	defer srv4.Close()
	bigMsgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type:   types.ContentTypeImage,
			Source: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: strings.Repeat("a", API_IMAGE_MAX_BASE64_SIZE+1)},
		}},
	}}
	_, err = p4.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: bigMsgs})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("oversized image err = %v", err)
	}
	if _, err := p4.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: bigMsgs}); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("oversized image stream err = %v", err)
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits != 0 {
		t.Errorf("oversized image still sent %d requests", gotHits)
	}

	p5 := &ResponsesProvider{name: "t", httpClient: srv.Client(), retryConfig: DefaultRetryConfig(), apiKey: "k", baseURL: "http://[::bad", model: "m"}
	var urlErr *url.Error
	_, err = p5.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err == nil || !strings.Contains(err.Error(), "create request") {
		t.Errorf("complete bad-url err = %v", err)
	}
	if !errors.As(err, &urlErr) {
		t.Errorf("create-request error not unwrappable: %v", err)
	}
	_, err = p5.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err == nil || !strings.Contains(err.Error(), "create request") {
		t.Errorf("stream bad-url err = %v", err)
	}
	if !errors.As(err, &urlErr) {
		t.Errorf("stream create-request error not unwrappable: %v", err)
	}

	p6, srv6 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	base6 := srv6.URL
	srv6.Close()
	p6.baseURL = base6
	_, err = p6.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err == nil || !strings.Contains(err.Error(), "send request") {
		t.Fatalf("transport err = %v", err)
	}
	if !errors.As(err, &urlErr) {
		t.Errorf("transport error not unwrappable: %v", err)
	}

	ln7, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	go func() {
		conn, aerr := ln7.Accept()
		if aerr != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\nshort"))
		time.Sleep(100 * time.Millisecond) // REAL-TIME: client must finish reading headers before the close
		_ = conn.Close()
	}()
	// Zero retries and no keep-alives: this pins the truncated-body error
	// from the FIRST attempt on a connection nobody reuses. With pooled
	// connections, the transport's readLoop races the listener close and
	// its peek error (readLoopPeekFailLocked) masquerades as the truncation
	// error under load — the once-in-six flake.
	noKa := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	p7 := &ResponsesProvider{name: "t", httpClient: noKa, retryConfig: &RetryConfig{MaxRetries: 0}, apiKey: "k", baseURL: "http://" + ln7.Addr().String(), model: "m"}
	_, err = p7.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	_ = ln7.Close()
	// Which transport error surfaces is a Go-internal timing race the test
	// cannot control (readLoopPeekFailLocked when the close beats the body
	// read, unexpected EOF when it doesn't). The pinned contract is weaker
	// on shape and stronger on meaning: the failure must SURFACE, wrapped
	// in the request context, never swallowed into a nil error.
	if err == nil || (!strings.Contains(err.Error(), "read response") && !strings.Contains(err.Error(), "send request")) {
		t.Fatalf("truncated body err = %v", err)
	}
}

// Stream-side plumbing: default retry config when nil, wire headers, NaN
// temperature propagation, the unmarshal wrap, and connection-error retries
// that respect backoff timing.
func TestResponsesStream_Plumbing(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and these reads.
	var mu sync.Mutex
	var gotCT, gotAuth string
	hits := 0
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer srv.Close()
	p.retryConfig = nil // Stream must fall back to DefaultRetryConfig
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}
	ch, err := p.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err != nil {
		t.Fatalf("nil retryConfig Stream: %v", err)
	}
	for range ch {
	}
	mu.Lock()
	gotHits, ct, auth := hits, gotCT, gotAuth
	mu.Unlock()
	if gotHits != 1 || ct != "application/json" || auth != "Bearer key" {
		t.Errorf("hits=%d ct=%q auth=%q", gotHits, ct, auth)
	}

	// NaN temperature errors identically on the stream path.
	p2, srv2 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {})
	defer srv2.Close()
	if _, err := p2.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs, Temperature: &nanVal}); err == nil || !strings.Contains(err.Error(), "marshal request") {
		t.Errorf("stream NaN err = %v", err)
	}

	// 200 with invalid JSON wraps the syntax error.
	p3, srv3 := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	})
	defer srv3.Close()
	_, err = p3.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err == nil || !strings.Contains(err.Error(), "unmarshal response") {
		t.Fatalf("bad 200 err = %v", err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("unmarshal error not unwrappable: %v", err)
	}
}

// Connection-error retries consume the backoff budget: with a 300ms base the
// full 2-retry cycle waits past 500ms. REAL-TIME: asserts elapsed wall clock;
// the mutant returns after the first failure (~0ms).
func TestResponsesStream_ConnectionRetryWaitsBackoff(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()

	p := &ResponsesProvider{
		name: "t", httpClient: &http.Client{}, retryConfig: &RetryConfig{MaxRetries: 2, BaseBackoff: 300 * time.Millisecond, MaxBackoff: 600 * time.Millisecond},
		apiKey:  "k",
		baseURL: "http://" + dead,
		model:   "m",
	}
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}
	start := time.Now() // REAL-TIME: bounds the backoff window against the wall clock
	_, err = p.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "max retries exceeded") {
		t.Fatalf("err = %v, want max retries exceeded", err)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("elapsed = %v, want >=500ms (two backoffs before giving up)", elapsed)
	}
}

// A 1ns idle timeout wraps the stream in the timeout reader: a slow line
// gaps into an idle error instead of parsing cleanly.
func TestResponsesStream_IdleTimeoutWrapsReader(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLog)
	p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond) // REAL-TIME: gap must exceed the 1ns idle window
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n"))
	})
	defer srv.Close()
	p.idleTimeout = 1 // 1ns: any gap trips the reader
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}
	ch, err := p.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var last StreamEvent
	for e := range ch {
		last = e
	}
	if last.Type != "error" || last.Error == nil || !last.Error.Retryable {
		t.Errorf("idle-timeout stream tail = %+v, want retryable transport error", last)
	}
	if !strings.Contains(logBuf.String(), "responses sse: scanner error") {
		t.Errorf("scanner-error log missing: %s", logBuf.String())
	}
}

// Done-guard edges: a done event for a delta'd block with no text closes at
// the done position; a done-only item with no text consumes no index; a
// repeated done never re-emits its text.
func TestResponsesSSE_DoneGuardEdges(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	events, _ := collectResponsesEvents(context.Background(), p, sseBody(
		`data: {"type":"response.output_text.delta","item_id":"a","delta":"A"}`,
		`data: {"type":"response.output_item.done","item":{"id":"a","type":"message","text":""}}`,
		`data: {"type":"response.output_item.done","item":{"id":"empty","type":"message","text":""}}`,
		`data: {"type":"response.output_item.done","item":{"id":"b","type":"message","content":[{"type":"output_text","text":"B"}]}}`,
		`data: {"type":"response.output_item.done","item":{"id":"b","type":"message","content":[{"type":"output_text","text":"B"}]}}`,
		`data: {"type":"response.completed","response":{"id":"r1"}}`,
	))
	var shape []string
	textCount := map[string]int{}
	for _, e := range events {
		switch e.Type {
		case "content_block_start", "content_block_delta", "content_block_stop":
			shape = append(shape, fmt.Sprintf("%s@%d", e.Type, e.Index))
			if e.Type == "content_block_delta" && e.Delta != nil {
				textCount[e.Delta.Text]++
			}
		}
	}
	want := []string{
		"content_block_start@0", "content_block_delta@0", "content_block_stop@0",
		"content_block_start@1", "content_block_delta@1", "content_block_stop@1",
	}
	if len(shape) != len(want) {
		t.Fatalf("shape = %v, want %v (empty done consumes no index)", shape, want)
	}
	for i := range want {
		if shape[i] != want[i] {
			t.Errorf("shape[%d] = %s, want %s", i, shape[i], want[i])
		}
	}
	if textCount["A"] != 1 || textCount["B"] != 1 {
		t.Errorf("text emissions = %v, want each text exactly once", textCount)
	}
}

// truncated JSON fails; operand mutations shift the boundary observably.
func TestResponsesComplete_BodyCapBoundaries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		total  int
		wantOK bool
	}{
		{30 << 20, true},           // below every candidate cap
		{(49 << 20) + 1000, true},  // above 49MB, below 50MB
		{(50 << 20) + 1000, false}, // above the cap: truncated JSON fails
		{60 << 20, false},          // only a 100MB cap would parse this
	} {
		prefix, suffix := `{"id":"r","status":"completed","x":"`, `"}`
		var b []byte
		b = append(b, prefix...)
		for len(b) < tc.total-len(suffix) {
			b = append(b, 'z')
		}
		b = append(b, suffix...)

		p, srv := newResponsesMutServer(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
		})
		msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}}}
		_, err := p.Complete(context.Background(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
		srv.Close()
		if tc.wantOK && err != nil {
			t.Errorf("body %d: err = %v, want parse OK", tc.total, err)
		}
		if !tc.wantOK && err == nil {
			t.Errorf("body %d: parsed OK, want truncation failure", tc.total)
		}
	}
}

// The SSE scanner buffer grows to 10MB: lines within the cap parse on both
// sides of the operand-decremented boundaries.
func TestResponsesSSE_ScannerBufferCap(t *testing.T) {
	t.Parallel()

	for _, size := range []int{(9 << 20) + 512*1024, (10 << 20) - 4*1024, (10 << 20) - 512} {
		big := strings.Repeat("x", size)
		p := newResponsesTestProvider()
		events, _ := collectResponsesEvents(context.Background(), p, sseBody(
			`data: {"type":"response.output_text.delta","item_id":"t","delta":"`+big+`"}`,
			`data: {"type":"response.completed","response":{"id":"r1"}}`,
		))
		ok := false
		for _, e := range events {
			if e.Type == "content_block_delta" && e.Delta != nil && len(e.Delta.Text) == size {
				ok = true
			}
		}
		if !ok {
			t.Errorf("line of %d bytes did not parse inside the 10MB cap", size)
		}
	}
}
