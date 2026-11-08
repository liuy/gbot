package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func newOpenAIMutServer(handler http.HandlerFunc) (*OpenAIProvider, *httptest.Server) {
	srv := httptest.NewServer(handler)
	p := &OpenAIProvider{
		name: "test", httpClient: srv.Client(), idleTimeout: DefaultSSETimeout, retryConfig: DefaultRetryConfig(),
		apiKey:  "key",
		baseURL: srv.URL,
		model:   "gpt-test",
	}
	return p, srv
}

func TestNewOpenAIProviderDefaults(t *testing.T) {
	t.Parallel()

	p := NewOpenAIProvider(&OpenAIConfig{Name: "n", APIKey: "k"})
	if p.baseURL != "https://api.openai.com/v1" {
		t.Errorf("baseURL = %q, want default", p.baseURL)
	}
	if p.httpClient.Timeout != DefaultHTTPTimeout {
		t.Errorf("timeout = %v, want %v", p.httpClient.Timeout, DefaultHTTPTimeout)
	}
	if p.idleTimeout != DefaultSSETimeout {
		t.Errorf("idleTimeout = %v, want %v", p.idleTimeout, DefaultSSETimeout)
	}
	if p.name != "n" || p.model != "" {
		t.Errorf("name/model = %q/%q", p.name, p.model)
	}
	p2 := NewOpenAIProvider(&OpenAIConfig{Name: "n", APIKey: "k", BaseURL: "http://h/v1/", Model: "m"})
	if p2.baseURL != "http://h/v1" || p2.model != "m" {
		t.Errorf("explicit config: baseURL=%q model=%q", p2.baseURL, p2.model)
	}
}

func openAIMutMessages(text string) []types.Message {
	return []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: text}}}}
}

// Complete parses reasoning, text, and tool calls in order and computes usage
// with cached tokens excluded from new input.
func TestOpenAIComplete_TranslateResponse(t *testing.T) {
	t.Parallel()

	body := `{"id":"cc1","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hi there","reasoning_content":"let me think","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Write","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens_details":{"reasoning_tokens":7}}}`
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	defer srv.Close()

	resp, err := p.Complete(t.Context(), &Request{Model: "gpt-test", MaxTokens: 10, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.ID != "cc1" || resp.Model != "gpt-test" || resp.StopReason != "tool_use" || resp.Role != "assistant" {
		t.Errorf("resp header fields = %+v", resp)
	}
	if len(resp.Content) != 3 {
		t.Fatalf("content = %+v, want thinking+text+tool_use", resp.Content)
	}
	if resp.Content[0].Type != types.ContentTypeThinking || resp.Content[0].Thinking != "let me think" {
		t.Errorf("thinking block = %+v", resp.Content[0])
	}
	if resp.Content[1].Type != types.ContentTypeText || resp.Content[1].Text != "hi there" {
		t.Errorf("text block = %+v", resp.Content[1])
	}
	if resp.Content[2].Type != types.ContentTypeToolUse || resp.Content[2].ID != "call_1" || resp.Content[2].Name != "Write" || string(resp.Content[2].Input) != `{"a":1}` {
		t.Errorf("tool block = %+v", resp.Content[2])
	}
	if resp.Usage.InputTokens != 60 || resp.Usage.CacheReadInputTokens != 40 || resp.Usage.OutputTokens != 12 || resp.Usage.CacheCreationInputTokens != 0 {
		t.Errorf("usage = %+v, want input 60 (100-40 cached), cacheRead 40, output 12", resp.Usage)
	}
}

// A null content (tool-only response) must not produce a text block; an empty
// string content must not either.
func TestOpenAIComplete_NullAndEmptyContent(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"c","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"F","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	defer srv.Close()
	resp, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != types.ContentTypeToolUse {
		t.Errorf("null-content resp = %+v, want only the tool block", resp.Content)
	}

	p2, srv2 := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"c","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	defer srv2.Close()
	resp2, err := p2.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp2.Content) != 0 {
		t.Errorf("empty-content resp = %+v, want no blocks", resp2.Content)
	}
}

func TestOpenAIComplete_ErrorPaths(t *testing.T) {
	t.Parallel()

	// Image validation error.
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()
	big := strings.Repeat("a", API_IMAGE_MAX_BASE64_SIZE+1)
	_, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: big}}},
	}}})
	if _, ok := err.(*ImageSizeError); !ok {
		t.Errorf("image err = %v, want *ImageSizeError", err)
	}

	// Marshal failure via an unserializable temperature (NaN).
	nan := math.NaN()
	_, err = p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Temperature: &nan, Messages: openAIMutMessages("q")})
	if err == nil || !strings.HasPrefix(err.Error(), "marshal request:") {
		t.Errorf("marshal err = %v", err)
	}

	// Unmarshalable response body.
	p2, srv2 := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`nope`))
	})
	defer srv2.Close()
	_, err = p2.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err == nil || !strings.HasPrefix(err.Error(), "unmarshal response:") {
		t.Errorf("unmarshal err = %v", err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("unmarshal err does not chain json.SyntaxError: %v", err)
	}

	// Transport failure chains *url.Error.
	p3 := &OpenAIProvider{name: "t", httpClient: &http.Client{}, idleTimeout: DefaultSSETimeout, apiKey: "k", baseURL: "http://127.0.0.1:1"}
	_, err = p3.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err == nil || !strings.HasPrefix(err.Error(), "send request:") {
		t.Errorf("send err = %v", err)
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("send err does not chain *url.Error: %v", err)
	}
}

// Response bodies just under the 50MB cap parse; just over they fail.
func TestOpenAIComplete_BodyCapBoundaries(t *testing.T) {
	t.Parallel()

	pad := strings.Repeat("e", 49*1024*1024+512*1024)
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + pad + `","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	defer srv.Close()
	if _, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")}); err != nil {
		t.Fatalf("49.5MB body: %v", err)
	}

	pad2 := strings.Repeat("f", 50*1024*1024+512*1024)
	p2, srv2 := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + pad2 + `","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	defer srv2.Close()
	_, err := p2.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err == nil || !strings.HasPrefix(err.Error(), "unmarshal response:") {
		t.Errorf("50.5MB body err = %v, want unmarshal failure past the cap", err)
	}
}

// A 400 effort rejection triggers exactly one negotiation retry whose body
// drops the effort spelling; the successful retry's response is returned.
func TestOpenAIComplete_NegotiationRetry(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and these reads; the
	// append+len branch must stay one critical section.
	var mu sync.Mutex
	var bodies []string
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		first := len(bodies) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid","message":"Invalid reasoning_effort \"low\", valid levels are: minimal, low"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"ok","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	defer srv.Close()

	resp, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 10, Thinking: EffortLow, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Complete after negotiation: %v", err)
	}
	if resp.ID != "ok" {
		t.Errorf("resp = %+v, want the retried response", resp)
	}
	mu.Lock()
	bodiesSnapshot := append([]string(nil), bodies...)
	mu.Unlock()
	if len(bodiesSnapshot) != 2 {
		t.Fatalf("request count = %d, want 2", len(bodiesSnapshot))
	}
	if !strings.Contains(bodiesSnapshot[0], `"reasoning_effort":"low"`) {
		t.Errorf("first body = %s, want the effort spelling", bodiesSnapshot[0])
	}
	if !strings.Contains(bodiesSnapshot[1], `"reasoning_effort":"minimal"`) {
		t.Errorf("second body = %s, want the effort remapped to the nearest allowed tier minimal", bodiesSnapshot[1])
	}
}

// translateMessages: assistant turns without content emit nothing, tool
// results follow their assistant turn, reasoning replays as
// reasoning_content, redacted thinking is dropped, and user text without
// images stays a plain string.
func TestOpenAITranslateMessages_Shapes(t *testing.T) {

	logBuf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)

	input := types.ContentBlock{Type: types.ContentTypeRedacted, Data: "blob"}
	msgs := []types.Message{
		{Role: types.RoleAssistant}, // empty assistant turn: no message
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hello"},
			input,
		}},
	}
	out := translateMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("messages = %+v, want exactly the user message", out)
	}
	if out[0].Role != "user" || out[0].Content != "hello" {
		t.Errorf("user message = %+v, want plain string content", out[0])
	}
	if !strings.Contains(logBuf.String(), "dropping redacted_thinking") {
		t.Error("redacted-thinking drop not logged")
	}

	// Assistant with tool call + tool result: assistant(tool_calls) then tool message.
	msgs2 := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "acting"},
			{Type: types.ContentTypeToolUse, ID: "t1", Name: "F", Input: json.RawMessage(`{}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"res"}]`)},
			{Type: types.ContentTypeText, Text: "after"},
		}},
	}
	out2 := translateMessages(msgs2)
	if len(out2) != 3 {
		t.Fatalf("messages = %+v, want assistant+tool+user", out2)
	}
	if out2[0].Role != "assistant" || out2[0].Content != "acting" || len(out2[0].ToolCalls) != 1 || out2[0].ToolCalls[0].ID != "t1" {
		t.Errorf("assistant msg = %+v", out2[0])
	}
	if out2[1].Role != "tool" || out2[1].ToolCallID != "t1" || out2[1].Content != "res" {
		t.Errorf("tool msg = %+v", out2[1])
	}
	if out2[2].Role != "user" || out2[2].Content != "after" {
		t.Errorf("user msg = %+v", out2[2])
	}

	// Single-char reasoning still replays (len>0 boundary).
	out3 := translateMessages([]types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeThinking, Thinking: "x"}}}})
	if len(out3) != 1 || out3[0].ReasoningContent == nil || *out3[0].ReasoningContent != "x" {
		t.Errorf("reasoning msg = %+v, want reasoning_content x", out3)
	}
}

// Tool-result images are lifted into a companion user message; a sourceless
// image inside a tool result is skipped without panic.
func TestOpenAITranslateMessages_ToolResultImages(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolResult, ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"out"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}]`)},
	}}}
	out := translateMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("messages = %+v, want tool + companion image message", out)
	}
	if out[0].Role != "tool" || out[0].Content != "out" {
		t.Errorf("tool msg = %+v", out[0])
	}
	arr, ok := out[1].Content.([]openaiImageURL)
	if !ok || len(arr) != 1 || arr[0].ImageURL.URL != "data:image/png;base64,QUJD" {
		t.Errorf("image companion = %+v", out[1].Content)
	}

	// Empty-text block carrying a source is not an image.
	withSource := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolResult, ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"","source":{"type":"base64","media_type":"image/png","data":"QQ"}}]`)},
	}}}
	out3 := translateMessages(withSource)
	if len(out3) != 1 || out3[0].Role != "tool" {
		t.Errorf("empty-text-with-source = %+v, want a single tool message", out3)
	}

	// Sourceless image block: skipped, no panic.
	noSrc := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolResult, ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"ok"},{"type":"image"}]`)},
	}}}
	out2 := translateMessages(noSrc)
	if len(out2) != 1 || out2[0].Content != "ok" {
		t.Errorf("sourceless image msg = %+v, want single tool message", out2)
	}
}

// Unparseable tool-result content falls back to the raw string as text.
func TestOpenAIExtractToolResultContent_RawFallback(t *testing.T) {
	t.Parallel()

	text, imgs := extractToolResultContent(json.RawMessage(`not json at all`))
	if text != "not json at all" {
		t.Errorf("text = %q, want raw passthrough", text)
	}
	if imgs != nil {
		t.Errorf("imgs = %+v, want nil", imgs)
	}
}

// Request assembly: system prompt, metadata user, temperature, stop
// sequences, tools+tool_choice, and extra params merge.
func TestOpenAITranslateRequest_Assembly(t *testing.T) {
	t.Parallel()

	temp := 0.7
	p := &OpenAIProvider{name: "t", model: "gpt-default"}
	body, err := p.translateRequest(&Request{
		Model:         "",
		MaxTokens:     123,
		Temperature:   &temp,
		StopSequences: []string{"END"},
		System:        json.RawMessage(`"be brief"`),
		Metadata:      &RequestMetadata{UserID: "u1"},
		Tools:         []ToolDef{{Name: "F", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages:      openAIMutMessages("q"),
		Thinking:      EffortHigh,
	}, false)
	if err != nil {
		t.Fatalf("translateRequest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body: %v", err)
	}
	if m["model"] != "gpt-default" {
		t.Errorf("model = %v, want provider default", m["model"])
	}
	if m["max_tokens"] != float64(123) || m["temperature"] != 0.7 {
		t.Errorf("max_tokens/temp = %v/%v", m["max_tokens"], m["temperature"])
	}
	if m["user"] != "u1" {
		t.Errorf("user = %v, want u1", m["user"])
	}
	if m["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", m["reasoning_effort"])
	}
	msgs := m["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("first message = %v, want system", msgs[0])
	}
	if _, ok := m["tools"]; !ok || m["tool_choice"] != "auto" {
		t.Errorf("tools/tool_choice = %v/%v", m["tools"], m["tool_choice"])
	}

	// Extra params merge over the translated body.
	p2 := &OpenAIProvider{name: "t", model: "m", extraParams: map[string]any{"tool_stream": true, "model": "override"}}
	body2, err := p2.translateRequest(&Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")}, false)
	if err != nil {
		t.Fatalf("translateRequest: %v", err)
	}
	var m2 map[string]any
	_ = json.Unmarshal(body2, &m2)
	if m2["tool_stream"] != true || m2["model"] != "override" {
		t.Errorf("extra params not merged: %v", m2)
	}
}

// ParseAPIError maps each status family to its code and retryability.
func TestOpenAIParseAPIError_Table(t *testing.T) {

	p := &OpenAIProvider{}
	cases := []struct {
		status int
		body   string
		typ    string
		code   string
		msg    string
		retry  bool
	}{
		{400, `{"error":{"code":"context_length_exceeded","message":"too long"}}`, "prompt_too_long", "prompt_too_long", "too long", false},
		{429, `{"error":{"message":"slow"}}`, "rate_limit_error", "rate_limit_error", "slow", false},
		{401, `{"error":{"message":"bad key"}}`, "authentication_error", "authentication_error", "bad key", false},
		{403, `{"error":{"message":"no"}}`, "permission_error", "permission_error", "no", false},
		{500, `{"error":{"message":"boom"}}`, "api_error", "api_error", "boom", true},
		{503, `{"error":{"message":"down"}}`, "api_error", "api_error", "down", true},
		{400, `{"error":{"type":"custom_type","message":"other"}}`, "custom_type", "", "other", false},
		{418, `{"error":{"type":"teapot","message":"brewing"}}`, "teapot", "", "brewing", false},
	}
	for _, tc := range cases {
		e := p.ParseAPIError([]byte(tc.body), tc.status)
		if e.Type != tc.typ || e.ErrorCode != tc.code || e.Retryable != tc.retry || e.Message != tc.msg {
			t.Errorf("status %d: type=%q code=%q msg=%q retry=%v, want %q/%q/%q/%v",
				tc.status, e.Type, e.ErrorCode, e.Message, e.Retryable, tc.typ, tc.code, tc.msg, tc.retry)
		}
	}
	// Unparseable body warns and keeps the raw text.
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	e3 := p.ParseAPIError([]byte(`plain junk`), 500)
	slog.SetDefault(oldLog)
	if e3.Message != "plain junk" {
		t.Errorf("raw message = %q", e3.Message)
	}
	if !strings.Contains(logBuf.String(), "parse OpenAI error response failed") {
		t.Errorf("no warn for unparseable body: %q", logBuf.String())
	}

	// Empty structured message falls back to the raw body.
	e := p.ParseAPIError([]byte(`{"error":{"type":"t"}}`), 400)
	if e.Message != `{"error":{"type":"t"}}` {
		t.Errorf("fallback message = %q", e.Message)
	}
	// Unparseable body keeps raw text.
	e2 := p.ParseAPIError([]byte(`plain`), 500)
	if e2.Message != "plain" || e2.Retryable != true {
		t.Errorf("raw body err = %+v", e2)
	}
}

// Pure-reasoning stream: thinking opens without any content trigger, empty
// reasoning deltas open nothing, and finish closes the thinking block.
func TestOpenAISSE_ReasoningBoundaries(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{}
	body := sseBody(
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":""},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":null},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"ponder"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"ing"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens_details":{"reasoning_tokens":7}}}`,
		`data: [DONE]`,
	)
	events := collectEvents(context.Background(), p, body)
	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // thinking
		"content_block_delta",
		"content_block_delta",
		"content_block_stop", // closed at finish
		"message_delta",
		"message_stop",
	)
	if cb := events[1].ContentBlock; cb == nil || cb.Type != types.ContentTypeThinking {
		t.Errorf("thinking block start = %+v", events[1].ContentBlock)
	}
	if events[1].Index != 0 {
		t.Errorf("thinking index = %d, want 0", events[1].Index)
	}
	if d := events[2].Delta; d == nil || d.Type != "thinking_delta" || d.Thinking != "ponder" {
		t.Errorf("first thinking delta = %+v", events[2].Delta)
	}
	if d := events[3].Delta; d == nil || d.Thinking != "ing" {
		t.Errorf("second thinking delta = %+v", events[3].Delta)
	}
	md := events[5]
	if md.DeltaMsg == nil || md.DeltaMsg.StopReason != "end_turn" {
		t.Errorf("stop reason = %+v, want end_turn", md.DeltaMsg)
	}
	if md.Usage == nil || md.Usage.InputTokens != 60 || md.Usage.CacheReadInputTokens != 40 || md.Usage.OutputTokens != 12 {
		t.Errorf("usage = %+v, want input 60 (100-40), cacheRead 40, output 12", md.Usage)
	}
}

// Thinking closes when text starts; a tool call closes both thinking and
// text; the tool block carries the first chunk's ID and name.
func TestOpenAISSE_BlockClosings(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{}
	body := sseBody(
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"hmm"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":" more"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"Write","arguments":"{\"x\":"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`,
	)
	events := collectEvents(context.Background(), p, body)
	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // thinking idx0
		"content_block_delta", // hmm
		"content_block_stop",  // thinking closed by text
		"content_block_start", // text idx1
		"content_block_delta", // answer
		"content_block_delta", // more
		"content_block_stop",  // text closed by tool
		"content_block_start", // tool idx2
		"content_block_delta", // args part 1
		"content_block_delta", // args part 2
		"message_delta",       // finish_reason: tool block still open
		"content_block_stop",  // tool closed by the [DONE] sweep
		"message_stop",
	)
	tool := events[8]
	if tool.ContentBlock == nil || tool.ContentBlock.Type != types.ContentTypeToolUse || tool.ContentBlock.ID != "call_9" || tool.ContentBlock.Name != "Write" {
		t.Errorf("tool block start = %+v", tool.ContentBlock)
	}
	if d := events[9].Delta; d == nil || d.Type != "input_json_delta" || d.PartialJSON != `{"x":` {
		t.Errorf("args delta 1 = %+v", events[9].Delta)
	}
	if d := events[10].Delta; d == nil || d.PartialJSON != "1}" {
		t.Errorf("args delta 2 = %+v", events[10].Delta)
	}
	if md := events[11].DeltaMsg; md == nil || md.StopReason != "tool_use" {
		t.Errorf("stop = %+v, want tool_use", events[11].DeltaMsg)
	}
}

// Arguments arriving before the function name start the block with whatever
// identity is known (empty id/name).
func TestOpenAISSE_ArgumentsBeforeName(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{}
	body := sseBody(
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"late_id","function":{"name":"Late"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	events := collectEvents(context.Background(), p, body)
	assertEventTypes(t, events,
		"message_start",
		"content_block_start", // opened by args, empty id/name
		"content_block_delta", // {"a":
		"content_block_start", // re-announced with the late id/name
		"message_delta",
		"content_block_stop", // closeOpenBlocks sweep
		"message_stop",
	)
	if cb := events[1].ContentBlock; cb == nil || cb.Type != types.ContentTypeToolUse || cb.ID != "" || cb.Name != "" {
		t.Errorf("early-opened tool block = %+v, want empty id/name", events[1].ContentBlock)
	}
	if d := events[2].Delta; d == nil || d.PartialJSON != `{"a":` {
		t.Errorf("args delta = %+v", events[2].Delta)
	}
	if cb := events[3].ContentBlock; cb == nil || cb.ID != "late_id" || cb.Name != "Late" {
		t.Errorf("re-announced tool block = %+v, want late id/name", events[3].ContentBlock)
	}
}

// A tool chunk carrying id + arguments (no name) opens the block with the
// stored id.
func TestOpenAISSE_IdWithArgumentsNoName(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{}
	events := collectEvents(context.Background(), p, sseBody(
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"early_id","function":{"arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
	))
	for _, e := range events {
		if e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeToolUse {
			if e.ContentBlock.ID != "early_id" {
				t.Errorf("block ID = %q, want early_id", e.ContentBlock.ID)
			}
			return
		}
	}
	t.Fatal("no tool block start")
}

// Exactly-at-limit arguments are accepted; one byte over aborts the stream
// with open blocks closed and no message_stop.
func TestOpenAISSE_ToolArgsLimitBoundary(t *testing.T) {
	// NOT parallel — modifies package-level maxToolArgumentsSize.
	orig := maxToolArgumentsSize
	maxToolArgumentsSize = 10
	defer func() { maxToolArgumentsSize = orig }()

	p := &OpenAIProvider{}
	atLimit := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"W","arguments":"12345"}}]},"finish_reason":null}]}`
	second := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"67890"}}]},"finish_reason":null}]}`
	over := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"12345678901"}}]},"finish_reason":null}]}`

	events := collectEvents(context.Background(), p, sseBody(atLimit, second, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `data: [DONE]`))
	if got := eventTypes(events); got[len(got)-1] != "message_stop" {
		t.Errorf("at-limit stream truncated: %v", got)
	}
	deltas := 0
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "input_json_delta" {
			deltas++
		}
	}
	if deltas != 2 {
		t.Errorf("arg deltas = %d, want 2 (both chunks within the 10-byte limit)", deltas)
	}

	events2 := collectEvents(context.Background(), p, sseBody(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"W","arguments":"12345"}}]},"finish_reason":null}]}`, over, `data: [DONE]`))
	sawStop := false
	for _, e := range events2 {
		if e.Type == "message_stop" {
			sawStop = true
		}
	}
	if sawStop {
		t.Error("over-limit args must abort the stream without message_stop")
	}
}

// The synthesized message_start ID has the msg_ prefix and a 24-char
// lowercase-alnum body; different calls produce different IDs.
func TestOpenAIRandomID(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for range 100 {
		id := randomID()
		if len(id) != 24 {
			t.Fatalf("randomID len = %d, want 24", len(id))
		}
		for _, c := range id {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				t.Fatalf("randomID charset violation: %q", id)
			}
		}
		seen[id] = true
	}
	if len(seen) < 50 {
		t.Errorf("only %d distinct IDs in 100 draws", len(seen))
	}
}

// fastrand must not collapse to a constant sequence.
func TestOpenAIFastrandVaries(t *testing.T) {
	t.Parallel()

	seen := map[uint32]bool{}
	for range 100 {
		seen[fastrand()] = true
	}
	if len(seen) < 50 {
		t.Errorf("only %d distinct values in 100 draws", len(seen))
	}
}

// Lines that are not data payloads are skipped without a parse warning.
func TestOpenAISSE_NonDataLinesSilent(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	p := &OpenAIProvider{}
	events := collectEvents(context.Background(), p, sseBody(
		"event: message.created",
		": keepalive",
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`,
		`data: [DONE]`,
	))
	if logBuf.String() != "" {
		t.Errorf("warnings logged for non-data lines: %q", logBuf.String())
	}
	deltas := 0
	for _, e := range events {
		if e.Type == "content_block_delta" {
			deltas++
		}
	}
	if deltas != 1 {
		t.Errorf("deltas = %d, want 1", deltas)
	}
}

// Malformed data payloads warn and are skipped; the stream continues.
func TestOpenAISSE_MalformedChunkWarns(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	p := &OpenAIProvider{}
	events := collectEvents(context.Background(), p, sseBody(
		`data: {bad`,
		`data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		`data: [DONE]`,
	))
	if !strings.Contains(logBuf.String(), "failed to parse chunk") {
		t.Errorf("no warn for malformed chunk; log = %q", logBuf.String())
	}
	logBuf.Reset()
	_ = collectEvents(context.Background(), p, sseBody("data: {bad "+strings.Repeat("q", 300)))
	if !strings.Contains(logBuf.String(), strings.Repeat("q", 200-5)+"...") {
		t.Errorf("malformed chunk data not truncated at 200: %.120q", logBuf.String())
	}
	if strings.Contains(logBuf.String(), strings.Repeat("q", 200-5+1)+"...") {
		t.Error("malformed chunk data truncated past 200")
	}
	deltas := 0
	for _, e := range events {
		if e.Type == "content_block_delta" {
			deltas++
		}
	}
	if deltas != 1 {
		t.Errorf("deltas = %d, want 1 after skipping the malformed chunk", deltas)
	}
}

// Clean EOF without [DONE] still closes blocks and emits message_stop.
func TestOpenAISSE_EofWithoutDone(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{}
	events := collectEvents(context.Background(), p, sseBody(
		`data: {"choices":[{"index":0,"delta":{"content":"tail"},"finish_reason":null}]}`,
	))
	assertEventTypes(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_stop",
	)
}

// Scanner buffer: lines just under the 10MB cap parse, just over fail.
func TestOpenAISSE_BufferBoundaries(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{}
	okLine := strings.Repeat("y", 10485000)
	events := collectEvents(context.Background(), p, strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\""+okLine+"\"},\"finish_reason\":null}]}\n\n"))
	if len(events) < 3 {
		t.Fatalf("under-cap line not parsed: %d events", len(events))
	}
	if d := events[2].Delta; d == nil || len(d.Text) != 10485000 {
		t.Errorf("delta text len = %d, want 10485000", len(d.Text))
	}

	overLine := strings.Repeat("z", 10490000)
	ch := make(chan StreamEvent, 128)
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.parseOpenAISSE(context.Background(), &Request{Model: "m"}, strings.NewReader("data: "+overLine+"\n\n"), nil, ch)
	}()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case evt, open := <-ch:
			_ = evt
			if !open {
				ch = nil
			}
		case err := <-errCh:
			if err == nil {
				t.Error("over-cap line must fail the scanner")
			}
			return
		case <-timeout:
			t.Fatal("parse did not finish")
		}
	}
}

// Stream serializes stream:true and rejects oversized images pre-wire.
func TestOpenAIStream_WireDetails(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read.
	var mu sync.Mutex
	var body []byte
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer srv.Close()

	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 5, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	mu.Lock()
	bodySnapshot := body
	mu.Unlock()
	if !strings.Contains(string(bodySnapshot), `"stream":true`) {
		t.Errorf("body = %s, want stream:true", bodySnapshot)
	}
	if !strings.Contains(string(bodySnapshot), `"path":"/v1/chat/completions"`) && !strings.Contains(string(bodySnapshot), `"messages"`) {
		t.Errorf("body shape unexpected: %s", bodySnapshot)
	}

	big := strings.Repeat("a", API_IMAGE_MAX_BASE64_SIZE+1)
	_, err = p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: big}}},
	}}})
	if _, ok := err.(*ImageSizeError); !ok {
		t.Errorf("oversized image Stream err = %v, want *ImageSizeError", err)
	}
}

// Retryable statuses retry; zero budget makes one attempt wrapping the
// APIError; the first attempt runs without backoff.
func TestOpenAIStream_RetryBehavior(t *testing.T) {
	t.Parallel()

	// hits is written on the server goroutine and read by the assertions below
	// with no happens-before edge; the increment+branch must be one critical
	// section.
	var mu sync.Mutex
	hits := 0
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		early := hits < 2
		mu.Unlock()
		if early {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 2, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits != 2 {
		t.Errorf("hits = %d, want 2", gotHits)
	}

	hits2 := 0
	p2, srv2 := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits2++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	})
	defer srv2.Close()
	p2.retryConfig = &RetryConfig{MaxRetries: 0, BaseBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second}
	_, err = p2.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
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
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Errorf("chain lacks 500 APIError: %v", err)
	}

	// First attempt carries no backoff: cancel during the retry window
	// aborts with exactly one request sent.
	hits3 := 0
	p3, srv3 := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits3++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv3.Close()
	p3.retryConfig = &RetryConfig{MaxRetries: 1, BaseBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second}
	// 2s, not 150ms: under make-check's full load the first request can
	// spend >150ms before the handler runs, and the deadline would expire
	// with hits=0 — a load artifact, not a backoff regression. Still far
	// under the 30s backoff, so the no-backoff-first contract keeps its
	// resolution.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = p3.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	mu.Lock()
	gotHits3 := hits3
	mu.Unlock()
	if gotHits3 != 1 {
		t.Errorf("hits = %d, want 1 (no backoff before first attempt)", gotHits3)
	}
}

// A retryable 503 carrying Retry-After must gate the retry on the server's
// advice. Retry-After: 3600 against a 5ms base means an honored header holds
// the second hit past any test deadline; a dropped header retries within ~7ms
// and the stream succeeds (BUG-1 regression).
func TestOpenAIStream_RetryAfterHeaderHonored(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read; the
	// increment+branch must stay one critical section.
	var mu sync.Mutex
	hits := 0
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
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
		_, _ = w.Write([]byte(`{"error":{"message":"service unavailable","type":"server_error"}}`))
	})
	defer srv.Close()
	p.retryConfig = &RetryConfig{MaxRetries: 2, BaseBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}

	// 2s, not 500ms: the deadline only has to beat the advised 3600s, and
	// under full load the first request can spend >500ms before the handler
	// runs, which would expire the deadline with hits=0.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err == nil {
		for range ch {
		}
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

// Retry-After: 1 is the smallest advised delay that must beat a 5ms base —
// pinning the sec > 0 boundary against off-by-one mutations of the guard.
func TestOpenAIStream_RetryAfterOneSecondBeatsBase(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read.
	var mu sync.Mutex
	hits := 0
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
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
	ch, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err == nil {
		for range ch {
		}
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

// The drained error response is the last place the server's Retry-After
// surfaces; postChat must carry it on the APIError for the retry loop.
func TestOpenAIPostChat_ParsesRetryAfterHeader(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"server_error"}}`))
	})
	defer srv.Close()
	_, apiErr, _, err := p.postChat(context.Background(), []byte("{}"))
	if err != nil {
		t.Fatalf("postChat: %v", err)
	}
	if apiErr == nil || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("apiErr = %+v, want RetryAfter 7s", apiErr)
	}

	// Only positive integer seconds are advice; zero, negative, non-numeric
	// and absent headers leave RetryAfter unset so jittered backoff applies.
	for _, ra := range []string{"0", "-3", "soon", ""} {
		p2, srv2 := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
			if ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"x","type":"server_error"}}`))
		})
		_, apiErr2, _, err2 := p2.postChat(context.Background(), []byte("{}"))
		if err2 != nil {
			t.Fatalf("postChat(Retry-After %q): %v", ra, err2)
		}
		if apiErr2.RetryAfter != 0 {
			t.Errorf("Retry-After %q: RetryAfter = %v, want 0", ra, apiErr2.RetryAfter)
		}
		srv2.Close()
	}
}

// Non-retryable status fails immediately with the parsed error.
func TestOpenAIStream_NonRetryableImmediate(t *testing.T) {
	t.Parallel()

	// No happens-before between the handler goroutine and this read.
	var mu sync.Mutex
	hits := 0
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request","message":"nope"}}`))
	})
	defer srv.Close()

	_, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 400 || apiErr.Message != "nope" || apiErr.Type != "invalid_request" {
		t.Errorf("err = %+v", err)
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits != 1 {
		t.Errorf("hits = %d, want 1", gotHits)
	}
}

// Connection-level failures (abrupt close) retry through the budget.
func TestOpenAIStream_ConnectionErrorRetries(t *testing.T) {
	if raceEnabled {
		t.Skip("abrupt-close error classification is not deterministic under the race detector")
	}
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepts := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts <- struct{}{}
			_ = conn.Close()
		}
	}()

	// DisableKeepAlives keeps every attempt on a fresh connection so an
	// abrupt close always surfaces as EOF (retryable) instead of a pooled
	// "server closed idle connection" (terminal).
	p := &OpenAIProvider{name: "t", httpClient: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}, idleTimeout: DefaultSSETimeout, retryConfig: &RetryConfig{MaxRetries: 2, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}, apiKey: "k", baseURL: "http://" + ln.Addr().String(), model: "m"}
	_, err = p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err == nil || !strings.HasPrefix(err.Error(), "max retries exceeded:") {
		t.Fatalf("err = %v, want max retries exceeded", err)
	}
	if len(accepts) != 3 {
		t.Errorf("accepts = %d, want 3", len(accepts))
	}
}

// A canceled context chains context.Canceled.
func TestOpenAIStream_CanceledCtxChains(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err == nil {
		t.Fatal("nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want chained context.Canceled", err)
	}
}

// Zero idle timeout: no reader timeout; slow gaps survive; the parser
// goroutine delivers events while the stream is in flight.
func TestOpenAIStream_ZeroIdleTimeoutAndEarlyDelivery(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	select {
	case evt, ok := <-ch:
		if !ok || evt.Type != "message_start" {
			t.Fatalf("first = %+v ok=%v, want message_start while in flight", evt, ok)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no early delivery — parser not concurrent")
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
		t.Error("idleTimeout=0 stream must survive the gap")
	}
}

// Cancelling mid-stream closes the channel promptly.
func TestOpenAIStream_CancelClosesChannel(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 80 {
			_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}` + "\n\n"))
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("closed before first event")
	}
	cancel()
	closed := make(chan struct{})
	var sawErr atomic.Bool
	go func() {
		for evt := range ch {
			if evt.Error != nil {
				sawErr.Store(true)
			}
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
	if sawErr.Load() {
		t.Error("cancellation surfaced as a stream error event")
	}
	srv.Close()
}

// The default accumulated-arguments limit is 10MB.
func TestOpenAIDefaultToolArgumentsSize(t *testing.T) {
	t.Parallel()

	// Read through a pristine provider construction: the package var must
	// still hold its default (tests that override restore on defer).
	if maxToolArgumentsSize != 10*1024*1024 {
		t.Errorf("maxToolArgumentsSize = %d, want 10MB", maxToolArgumentsSize)
	}
}

// Delta events carry their block index; block starts carry typed payloads.
func TestOpenAISSE_EventFieldIntegrity(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{}
	events := collectEvents(context.Background(), p, sseBody(
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"t"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"W","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	))
	// thinking: start idx0, delta idx0; text: start idx1, delta idx1; tool: start idx2.
	if events[1].Index != 0 || events[1].ContentBlock.Type != types.ContentTypeThinking {
		t.Errorf("thinking start = %+v", events[1])
	}
	if events[2].Index != 0 || events[2].Delta.Type != "thinking_delta" {
		t.Errorf("thinking delta = %+v", events[2])
	}
	if events[4].Index != 1 || events[4].ContentBlock.Type != types.ContentTypeText {
		t.Errorf("text start = %+v", events[4])
	}
	if events[5].Index != 1 || events[5].Delta.Type != "text_delta" || events[5].Delta.Text != "x" {
		t.Errorf("text delta = %+v", events[5])
	}
	// Thinking opening after text must carry index 1.
	p2 := &OpenAIProvider{}
	events2 := collectEvents(context.Background(), p2, sseBody(
		`data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"t"},"finish_reason":null}]}`,
		`data: [DONE]`,
	))
	var thinkStart, thinkDelta StreamEvent
	for _, e := range events2 {
		if e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeThinking {
			thinkStart = e
		}
		if e.Delta != nil && e.Delta.Type == "thinking_delta" {
			thinkDelta = e
		}
	}
	if thinkStart.Index != 1 || thinkDelta.Index != 1 {
		t.Errorf("second-block thinking indices = %d/%d, want 1/1", thinkStart.Index, thinkDelta.Index)
	}

	toolEvents := 0
	for _, e := range events {
		if e.ContentBlock != nil && e.ContentBlock.Type == types.ContentTypeToolUse {
			toolEvents++
			if e.Index != 2 {
				t.Errorf("tool start index = %d, want 2", e.Index)
			}
		}
	}
	if toolEvents != 1 {
		t.Errorf("tool starts = %d, want 1", toolEvents)
	}
}

// The idle timeout stays disabled across tool-argument deltas: a mid-args gap
// must not kill the stream.
func TestOpenAIStream_ToolPhaseKeepsDisabled(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"W","arguments":"{\"a\":"}}]},"finish_reason":null}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	})
	defer srv.Close()
	p.idleTimeout = 50 * time.Millisecond
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 5, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for evt := range ch {
		if evt.Error != nil {
			t.Fatalf("stream error: %v — argument phase must not trip the idle timeout", evt.Error)
		}
	}
}

// A mid-stream gap outside the tool phase trips the idle timeout (the reader
// wiring is active) and surfaces as a retryable transport error event.
func TestOpenAIStream_IdleGapErrors(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		// 2s, not 300ms: the idle timeout only arms on the reader's first
		// Read; under full load the parser goroutine can start after the
		// whole stream is buffered, and the gap must still be observed.
		// The gap-vs-idle band only widens (40x vs 6x).
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	})
	defer srv.Close()
	p.idleTimeout = 50 * time.Millisecond
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 5, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var serr *APIError
	sawErrType := ""
	for evt := range ch {
		if evt.Error != nil {
			serr = evt.Error
			sawErrType = evt.Type
		}
	}
	if serr == nil {
		t.Fatal("no error event after the idle gap")
	}
	if sawErrType != "error" {
		t.Errorf("error event Type = %q, want error", sawErrType)
	}
	if serr.Type != "transport_error" || serr.Message != ErrIdleTimeout.Error() || !serr.Retryable {
		t.Errorf("idle error = %+v, want retryable transport_error", serr)
	}
}

// A one-nanosecond idle timeout still wraps the reader (the wiring threshold
// is > 0): the stream dies immediately.
func TestOpenAIStream_TinyIdleTimeout(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	defer srv.Close()
	p.idleTimeout = 1
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 5, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var serr *APIError
	for evt := range ch {
		if evt.Error != nil {
			serr = evt.Error
		}
	}
	if serr == nil || serr.Message != ErrIdleTimeout.Error() {
		t.Fatalf("error = %+v, want immediate idle timeout for 1ns", serr)
	}
}

// A scanner failure (line beyond the buffer cap) surfaces as an error event
// carrying the scanner message.
func TestOpenAIStream_ScannerErrorEvent(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("z", 11*1024*1024)
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: " + huge + "\n\n"))
		<-r.Context().Done()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 5, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var serr *APIError
	for evt := range ch {
		if evt.Error != nil {
			serr = evt.Error
		}
	}
	if serr == nil || !strings.Contains(serr.Message, "too long") {
		t.Fatalf("error = %+v, want the scanner error surfaced", serr)
	}
}

// The scanner-error path logs at error level.
func TestOpenAIStream_ScannerErrorLogs(t *testing.T) {
	logBuf := &syncedLogBuffer{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(oldLog)

	huge := strings.Repeat("z", 11*1024*1024)
	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: " + huge + "\n\n"))
		<-r.Context().Done()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 5, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if !strings.Contains(logBuf.String(), "openai sse: scanner error") {
		t.Errorf("scanner error not logged: %q", logBuf.String())
	}
}

// Stream surfaces marshal failures (NaN temperature) with the wrap prefix.
func TestOpenAIStream_MarshalError(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()
	nan := math.NaN()
	_, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Temperature: &nan, Messages: openAIMutMessages("q")})
	if err == nil || !strings.HasPrefix(err.Error(), "marshal request:") {
		t.Errorf("err = %v, want marshal request: prefix", err)
	}
}

func TestNewOpenAIProviderFields(t *testing.T) {
	t.Parallel()

	p := NewOpenAIProvider(&OpenAIConfig{Name: "n", APIKey: "k", Model: "m", ExtraParams: map[string]any{"x": 1}})
	if p.model != "m" {
		t.Errorf("model = %q", p.model)
	}
	if p.extraParams == nil || p.extraParams["x"] != 1 {
		t.Errorf("extraParams = %+v", p.extraParams)
	}
	if p.retryConfig == nil || p.reasoningFallbacks == nil {
		t.Error("retryConfig/reasoningFallbacks not initialized")
	}
	if p.apiKey != "k" {
		t.Errorf("apiKey = %q", p.apiKey)
	}
}

// Assistant turns of each composition emit exactly the right message shape:
// text-only, tool-only (nil content, no empty tool slice), and empty turns
// emit nothing. Images are collected only from user turns with sources.
func TestOpenAITranslateMessages_AssistantCompositions(t *testing.T) {
	t.Parallel()

	// Text-only assistant.
	out := translateMessages([]types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "solo"}}}})
	if len(out) != 1 || out[0].Content != "solo" || out[0].ToolCalls != nil {
		t.Errorf("text-only = %+v", out)
	}

	// Tool-only assistant: content nil, one tool call.
	out2 := translateMessages([]types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "t", Name: "F", Input: json.RawMessage(`{}`)}}}})
	if len(out2) != 1 || out2[0].Content != nil {
		t.Fatalf("tool-only = %+v", out2)
	}
	if out2[0].ToolCalls == nil || len(out2[0].ToolCalls) != 1 {
		t.Errorf("tool-only tool calls = %+v (nil vs empty matters for the wire)", out2[0].ToolCalls)
	}

	// Assistant images are not collected (user-only vision path).
	out3 := translateMessages([]types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: "Q"}}}}})
	if len(out3) != 0 {
		t.Errorf("assistant image produced messages: %+v", out3)
	}

	// Sourceless user image is skipped without panic.
	out4 := translateMessages([]types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeImage}}}})
	if len(out4) != 0 {
		t.Errorf("sourceless image produced messages: %+v", out4)
	}
}

// Cancellation stops the parser even when the caller stops draining: the
// channel closes without a reader.
func TestOpenAIStream_CancelUnblocksBlockedSend(t *testing.T) {
	t.Parallel()

	p, srv := newOpenAIMutServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 80 {
			_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}` + "\n\n"))
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	defer srv.Close()
	p.idleTimeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: openAIMutMessages("q")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("closed before first event")
	}
	// Read exactly 65 events (buffer 64 + the one in flight), then stop:
	// further sends block with no reader and must exit via ctx.Done.
	for range 65 {
		if _, ok := <-ch; !ok {
			t.Fatal("channel closed too early")
		}
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
		t.Fatal("blocked sends did not exit after ctx cancel")
	}
	srv.Close()
}
