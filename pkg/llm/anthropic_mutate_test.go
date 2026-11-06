package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func newAnthropicMutProvider(base string) *AnthropicProvider {
	return &AnthropicProvider{
		name: "test", httpClient: &http.Client{}, idleTimeout: DefaultSSETimeout,
		apiKey:  "key",
		baseURL: base,
	}
}

func TestNewAnthropicProviderDefaults(t *testing.T) {
	t.Parallel()

	p := NewAnthropicProvider(&AnthropicConfig{Name: "x", APIKey: "k"})
	if p.baseURL != "https://api.anthropic.com" {
		t.Errorf("default baseURL = %q, want https://api.anthropic.com", p.baseURL)
	}
	if p.httpClient.Timeout != DefaultHTTPTimeout {
		t.Errorf("default timeout = %v, want %v", p.httpClient.Timeout, DefaultHTTPTimeout)
	}
	if p.idleTimeout != DefaultSSETimeout {
		t.Errorf("idleTimeout = %v, want %v", p.idleTimeout, DefaultSSETimeout)
	}

	p2 := NewAnthropicProvider(&AnthropicConfig{Name: "x", APIKey: "k", BaseURL: "http://host.test/", Timeout: time.Second})
	if p2.baseURL != "http://host.test" {
		t.Errorf("baseURL trailing slash not trimmed: %q", p2.baseURL)
	}
	if p2.httpClient.Timeout != time.Second {
		t.Errorf("explicit timeout = %v, want 1s", p2.httpClient.Timeout)
	}
}

// Complete must send stream:false even when the caller pre-set Stream, attach
// the caching beta header only when cache control is configured, record
// prompt state, and parse the response body.
func TestAnthropicComplete_WireAndResponse(t *testing.T) {
	// Serial: asserts the global prompt-state store, which parallel
	// promptcache tests reset.

	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()

	var gotReq *http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}`))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	req := &Request{
		Model:          "claude",
		MaxTokens:      100,
		Stream:         true, // Complete must force this back to false
		Messages:       []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}},
		PromptStateKey: &PromptStateKey{QuerySource: "repl_main_thread"},
	}
	resp, err := p.Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.ID != "msg_1" || resp.Model != "claude" || resp.StopReason != "end_turn" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.CacheReadInputTokens != 7 || resp.Usage.CacheCreationInputTokens != 3 {
		t.Errorf("usage = %+v, want {10 5 7 3}", resp.Usage)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "hi" {
		t.Errorf("content = %+v", resp.Content)
	}

	if gotReq == nil {
		t.Fatal("no request captured")
	}
	if gotReq.Header.Get("x-api-key") != "key" || gotReq.Header.Get("Authorization") != "Bearer key" {
		t.Errorf("auth headers missing: %v", gotReq.Header)
	}
	if gotReq.Header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("anthropic-version = %q", gotReq.Header.Get("anthropic-version"))
	}
	if gotReq.Header.Get("anthropic-beta") != "" {
		t.Errorf("beta header without cache control = %q, want empty", gotReq.Header.Get("anthropic-beta"))
	}
	var body struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if body.Stream {
		t.Error("request body has stream:true — Complete must serialize stream:false")
	}
	muState.Lock()
	_, tracked := stateStore["repl_main_thread"]
	muState.Unlock()
	if !tracked {
		t.Error("Complete with PromptStateKey did not record prompt state")
	}
}

// The caching beta header rides along only when cache control is configured.
func TestAnthropicComplete_BetaHeaderWithCacheControl(t *testing.T) {
	t.Parallel()

	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(`{"id":"m","content":[],"usage":{}}`))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	req := &Request{
		Model:        "claude",
		MaxTokens:    10,
		CacheControl: &types.CacheControlConfig{Type: "ephemeral"},
		SystemBlocks: []SystemBlockParam{{Type: "text", Text: "sys"}},
	}
	if _, err := p.Complete(t.Context(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if beta != "prompt-caching-2024-10-22" {
		t.Errorf("anthropic-beta = %q, want prompt-caching-2024-10-22", beta)
	}
	// The last system block carries the cache_control marker and the blocks
	// are serialized into the system field.
	if req.SystemBlocks[len(req.SystemBlocks)-1].CacheControl == nil {
		t.Error("cache_control not injected into last system block")
	}
	var sys []map[string]any
	if err := json.Unmarshal(req.System, &sys); err != nil || len(sys) != 1 {
		t.Errorf("req.System = %s (%v), want one serialized block", req.System, err)
	}
}

func TestAnthropicComplete_ErrorWraps(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}

	// Unmarshalable response body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	p := newAnthropicMutProvider(srv.URL)
	_, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err == nil || !strings.HasPrefix(err.Error(), "unmarshal response:") {
		t.Errorf("err = %v, want unmarshal response: prefix", err)
	}
	srv.Close()

	// Transport failure.
	p2 := newAnthropicMutProvider("http://127.0.0.1:1")
	_, err = p2.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if err == nil || !strings.HasPrefix(err.Error(), "send request:") {
		t.Errorf("err = %v, want send request: prefix", err)
	}

	// Unserializable system payload.
	p3 := newAnthropicMutProvider("http://127.0.0.1:1")
	_, err = p3.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: msgs, System: json.RawMessage(`{bad`)})
	if err == nil || !strings.HasPrefix(err.Error(), "marshal request:") {
		t.Errorf("err = %v, want marshal request: prefix", err)
	}

	// Oversized image short-circuits before the wire.
	big := strings.Repeat("a", API_IMAGE_MAX_BASE64_SIZE+1)
	_, err = p3.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: big}}},
	}}})
	if _, ok := err.(*ImageSizeError); !ok {
		t.Errorf("oversized image err = %v, want *ImageSizeError", err)
	}
}

// A short-read body (Content-Length overstates the payload) surfaces as a
// read response error, not a truncated success.
func TestAnthropicComplete_ShortReadBody(t *testing.T) {
	if raceEnabled {
		t.Skip("raw-TCP short-read timing is not deterministic under the race detector")
	}
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\nshort"))
		_ = conn.Close()
	}()

	p := newAnthropicMutProvider("http://" + ln.Addr().String())
	_, err = p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err == nil || !strings.HasPrefix(err.Error(), "read response:") {
		t.Errorf("err = %v, want read response: prefix", err)
	}
}

// Retryable statuses are retried up to the budget; a non-retryable status
// fails immediately with the parsed APIError.
func TestAnthropicStream_RetryBehavior(t *testing.T) {
	t.Parallel()

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.retryConfig = &RetryConfig{MaxRetries: 3, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	sawStop := false
	for evt := range ch {
		if evt.Error != nil {
			t.Fatalf("stream error: %v", evt.Error)
		}
		if evt.Type == "message_stop" {
			sawStop = true
		}
	}
	if !sawStop {
		t.Error("no message_stop after retries")
	}
	if hits != 3 {
		t.Errorf("server hits = %d, want 3 (two 503s then success)", hits)
	}

	// Non-retryable status: immediate failure, single hit.
	hits2 := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits2++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	defer srv2.Close()
	p2 := newAnthropicMutProvider(srv2.URL)
	_, err = p2.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err == nil {
		t.Fatal("Stream on 400 = nil error")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 400 || apiErr.Message != "bad" {
		t.Errorf("err = %+v, want 400 APIError with message bad", err)
	}
	if hits2 != 1 {
		t.Errorf("non-retryable hits = %d, want 1", hits2)
	}
}

// Cancellation during the retry backoff must abort the stream instead of
// sleeping out the full window.
func TestAnthropicStream_CtxCancelDuringBackoff(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.retryConfig = &RetryConfig{MaxRetries: 5, BaseBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now() // REAL-TIME: bounds the backoff window against the wall clock
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Stream = nil error, want ctx error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Stream took %v — backoff did not observe ctx cancellation", elapsed)
	}
}

// Cache tokens from message_start AND message_delta accumulate before the
// post-call cache-break check; the break log reports the summed values.
func TestAnthropicStream_CacheTokenAccumulation(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":3000,\"cache_creation_input_tokens\":100}}}\n\n"))
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m2\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":1500,\"cache_creation_input_tokens\":20}}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5,\"cache_read_input_tokens\":2000,\"cache_creation_input_tokens\":50}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer srv.Close()

	logBuf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	p := newAnthropicMutProvider(srv.URL)
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	// Prime a previous read high enough that the summed 5000 counts as a break.
	RecordPromptState(nil, nil, key, "model", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 10000, 0, nil) // sets PrevCacheRead=10000

	ch, err := p.Stream(context.Background(), &Request{Model: "m", MaxTokens: 1, PromptStateKey: &key, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "prompt_cache:break") {
		t.Fatalf("no cache break logged; log = %q", logged)
	}
	if !strings.Contains(logged, "cacheReadCurr=6500") {
		t.Errorf("cacheReadCurr not the accumulated 6500 (3000+1500+2000); log = %q", logged)
	}
	if !strings.Contains(logged, "cacheCreation=170") {
		t.Errorf("cacheCreation not the accumulated 170 (100+20+50); log = %q", logged)
	}
}

// ParseSSE framing: events flush on blank lines, the trailing event flushes
// without one, data without an event type is dropped, the event type resets
// after each flush, and comments never become events.
func TestAnthropicParseSSE_Framing(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")
	events := runParseSSE(t, p, strings.Join([]string{
		"event: ping",
		"data: {}",
		"",
		"data: {}",
		"",
		"event: message_stop",
		"data: {}",
		": comment line",
		"",
	}, "\n"))

	if len(events) != 2 {
		t.Fatalf("events = %d (%v), want 2 (ping + message_stop; typeless data dropped)", len(events), eventTypes(events))
	}
	if events[0].Type != "ping" || events[1].Type != "message_stop" {
		t.Errorf("types = %v, want [ping message_stop]", eventTypes(events))
	}
}

func runParseSSE(t *testing.T, p *AnthropicProvider, input string) []StreamEvent {
	t.Helper()
	ch := make(chan StreamEvent, 128)
	var events []StreamEvent
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := p.ParseSSE(context.Background(), strings.NewReader(input), nil, ch); err != nil {
			t.Errorf("ParseSSE: %v", err)
		}
		close(ch)
	})
	for evt := range ch {
		events = append(events, evt)
	}
	wg.Wait()
	return events
}

// Lines larger than the initial 1MB buffer still parse; lines beyond the
// 10MB cap fail the scanner.
func TestAnthropicParseSSE_BufferLimits(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")
	big := strings.Repeat("a", 2*1024*1024)
	events := runParseSSE(t, p, "event: ping\ndata: {\"pad\":\""+big[:1024*1024+10]+"\"}\n\n")
	if len(events) != 1 || events[0].Type != "ping" {
		t.Fatalf("1MB+ data line not parsed: %d events", len(events))
	}

	huge := strings.Repeat("x", 11*1024*1024)
	ch := make(chan StreamEvent, 4)
	err := p.ParseSSE(context.Background(), strings.NewReader("data: "+huge+"\n\n"), nil, ch)
	if err == nil {
		t.Error("11MB line beyond the 10MB cap must fail the scanner")
	}
}

// Each event type parses its payload; malformed JSON still yields the typed
// event with no payload and logs a warning.
func TestAnthropicParseEvent_Payloads(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")

	msg := p.ParseEvent("message_start", `{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":3}}}`, nil)
	if msg.Message == nil || msg.Message.ID != "m1" || msg.Message.Usage.InputTokens != 3 {
		t.Errorf("message_start = %+v", msg)
	}

	blk := p.ParseEvent("content_block_start", `{"index":2,"content_block":{"type":"tool_use","id":"t1","name":"Write"}}`, nil)
	if blk.Index != 2 || blk.ContentBlock == nil || blk.ContentBlock.Type != types.ContentTypeToolUse || blk.ContentBlock.ID != "t1" {
		t.Errorf("content_block_start = %+v", blk)
	}

	delta := p.ParseEvent("content_block_delta", `{"index":1,"delta":{"type":"text_delta","text":"hi"}}`, nil)
	if delta.Index != 1 || delta.Delta == nil || delta.Delta.Text != "hi" {
		t.Errorf("content_block_delta = %+v", delta)
	}

	stop := p.ParseEvent("content_block_stop", `{"index":4}`, nil)
	if stop.Index != 4 {
		t.Errorf("content_block_stop index = %d, want 4", stop.Index)
	}

	md := p.ParseEvent("message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9,"cache_read_input_tokens":4}}`, nil)
	if md.DeltaMsg == nil || md.DeltaMsg.StopReason != "end_turn" || md.Usage == nil || md.Usage.OutputTokens != 9 || md.Usage.CacheReadInputTokens != 4 {
		t.Errorf("message_delta = %+v", md)
	}

	errEvt := p.ParseEvent("error", `{"error":{"type":"overloaded","message":"slow down"}}`, nil)
	if errEvt.Error == nil || errEvt.Error.Type != "overloaded" || errEvt.Error.Message != "slow down" {
		t.Errorf("error event = %+v", errEvt)
	}
}

func TestAnthropicParseEvent_MalformedWarns(t *testing.T) {

	p := newAnthropicMutProvider("")
	logBuf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	for _, typ := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "error"} {
		evt := p.ParseEvent(typ, `{bad json`, nil)
		if evt.Type != typ {
			t.Errorf("malformed %s event type = %q", typ, evt.Type)
		}
	}
	out := logBuf.String()
	for _, typ := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "error"} {
		if !strings.Contains(out, "parse "+typ) {
			t.Errorf("no warning for malformed %s; log = %q", typ, out)
		}
	}
}

// Error body parsing: structured messages win, raw bodies fall back, and the
// overflow / image-too-large phrasings map to their error codes.
func TestAnthropicParseAPIError(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")

	structured := p.ParseAPIError([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"m"}}`), 400)
	if structured.Message != "m" || structured.Type != "error" || structured.Status != 400 || structured.Retryable {
		t.Errorf("structured = %+v", structured)
	}

	raw := p.ParseAPIError([]byte(`plain failure`), 500)
	if raw.Message != "plain failure" || !raw.Retryable {
		t.Errorf("raw = %+v", raw)
	}

	empty := p.ParseAPIError([]byte(`{"error":{"type":"e"}}`), 400)
	if empty.Message != `{"error":{"type":"e"}}` {
		t.Errorf("empty message fallback = %q", empty.Message)
	}

	overflow := p.ParseAPIError([]byte(`{"error":{"message":"prompt is too long: 213462 tokens > 200000 maximum"}}`), 400)
	if overflow.ErrorCode != "prompt_too_long" {
		t.Errorf("overflow ErrorCode = %q, want prompt_too_long", overflow.ErrorCode)
	}
	imgSize := p.ParseAPIError([]byte(`{"error":{"message":"image exceeds the maximum allowed size"}}`), 400)
	if imgSize.ErrorCode != "image_too_large" {
		t.Errorf("image size ErrorCode = %q", imgSize.ErrorCode)
	}
	imgDim := p.ParseAPIError([]byte(`{"error":{"message":"image dimensions exceed the many-image limit"}}`), 400)
	if imgDim.ErrorCode != "image_too_large" {
		t.Errorf("image dims ErrorCode = %q", imgDim.ErrorCode)
	}
	other := p.ParseAPIError([]byte(`{"error":{"message":"some other problem"}}`), 400)
	if other.ErrorCode != "" {
		t.Errorf("unrelated ErrorCode = %q, want empty", other.ErrorCode)
	}
}

// State recorded by Complete/Stream must carry the zero-value negotiation
// inputs (autoMode, overage, extraBodyHash) — they seed change detection.
func TestAnthropicComplete_RecordsZeroNegotiationState(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"m","content":[],"usage":{}}`))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	_, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, PromptStateKey: &key, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	muState.Lock()
	st, ok := stateStore["repl_main_thread"]
	muState.Unlock()
	if !ok {
		t.Fatal("no recorded prompt state")
	}
	if st.AutoModeActive || st.IsUsingOverage || st.FastMode || st.CachedMCEnabled || st.ExtraBodyHash != 0 || st.CallCount != 1 {
		t.Errorf("recorded state = %+v, want zero negotiation inputs and CallCount 1", st)
	}
}

// Complete's post-call cache-break check reports the response usage tokens.
func TestAnthropicComplete_PostCallCacheBreak(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"m","content":[],"usage":{"cache_read_input_tokens":5000}}`))
	}))
	defer srv.Close()

	logBuf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	p := newAnthropicMutProvider(srv.URL)
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(nil, nil, key, "model", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 10000, 0, nil)

	if _, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, PromptStateKey: &key, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "prompt_cache:break") || !strings.Contains(logged, "cacheReadCurr=5000") {
		t.Errorf("post-call break log missing or wrong tokens; log = %q", logged)
	}
}

// Stream records prompt state (pre-call) under its own key namespace.
func TestAnthropicStream_RecordsPromptState(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	key := PromptStateKey{QuerySource: "sdk"}
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, PromptStateKey: &key, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	muState.Lock()
	_, tracked := stateStore["sdk"]
	muState.Unlock()
	if !tracked {
		t.Error("Stream with PromptStateKey did not record prompt state")
	}
}

// Stream serializes stream:true, honors cache-control headers, and rejects
// oversized images before the wire.
func TestAnthropicStream_WireDetails(t *testing.T) {
	var body []byte
	var beta, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		beta = r.Header.Get("anthropic-beta")
		accept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	req := &Request{
		Model:        "m",
		MaxTokens:    1,
		CacheControl: &types.CacheControlConfig{Type: "ephemeral"},
		SystemBlocks: []SystemBlockParam{{Type: "text", Text: "sys"}},
		Messages:     []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}},
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if !strings.Contains(string(body), `"stream":true`) {
		t.Errorf("request body = %s, want stream:true", body)
	}
	if beta != "prompt-caching-2024-10-22" {
		t.Errorf("anthropic-beta = %q, want prompt-caching-2024-10-22", beta)
	}
	if accept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", accept)
	}

	// Oversized image short-circuits Stream.
	big := strings.Repeat("a", API_IMAGE_MAX_BASE64_SIZE+1)
	_, err = p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: big}}},
	}}})
	if _, ok := err.(*ImageSizeError); !ok {
		t.Errorf("oversized image Stream err = %v, want *ImageSizeError", err)
	}
}

// Wrapped errors keep their chain: the %w wrap is load-bearing for callers
// that classify the cause.
func TestAnthropicComplete_ErrorChains(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	p := newAnthropicMutProvider(srv.URL)
	_, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		t.Errorf("unmarshal response error does not chain *json.SyntaxError: %v", err)
	}

	p2 := newAnthropicMutProvider("http://127.0.0.1:1")
	_, err = p2.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: msgs})
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("send request error does not chain *url.Error: %v", err)
	}

	_, err = p2.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: msgs, System: json.RawMessage(`{bad`)})
	var msErr *json.MarshalerError
	if !errors.As(err, &msErr) && !errors.As(err, &syn) {
		t.Errorf("marshal request error does not chain a json error: %v", err)
	}
}

// Retry-budget boundaries: with MaxRetries=0 exactly one attempt is made, and
// its failure surfaces as a wrapped APIError.
func TestAnthropicStream_ZeroRetryBudget(t *testing.T) {
	t.Parallel()

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.retryConfig = &RetryConfig{MaxRetries: 0, BaseBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second}
	_, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err == nil || !strings.HasPrefix(err.Error(), "max retries exceeded:") {
		t.Fatalf("err = %v, want max retries exceeded", err)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want exactly 1 (the single permitted attempt)", hits)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Errorf("err chain lacks the 503 APIError: %v", err)
	}
}

// The first attempt carries no backoff; a retryable failure then backs off,
// and cancellation during that backoff aborts before a second request.
func TestAnthropicStream_BackoffOnlyOnRetry(t *testing.T) {
	t.Parallel()

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.retryConfig = &RetryConfig{MaxRetries: 1, BaseBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err == nil {
		t.Fatal("Stream = nil error, want ctx cancellation")
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1 — the first attempt must run without backoff", hits)
	}
}

// Connection-level failures (EOF from an abruptly-closed socket) retry until
// the budget is spent.
func TestAnthropicStream_ConnectionErrorRetries(t *testing.T) {
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
			_ = conn.Close() // immediate close → client sees EOF
		}
	}()

	p := newAnthropicMutProvider("http://" + ln.Addr().String())
	p.httpClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}} // fresh conn per attempt: abrupt close ⇒ EOF (retryable)
	p.retryConfig = &RetryConfig{MaxRetries: 2, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	_, err = p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err == nil {
		t.Fatal("nil error")
	}
	if strings.Contains(err.Error(), "server closed idle connection") {
		t.Skip("transport classified the abrupt close as terminal; retry count not observable")
	}
	if !strings.HasPrefix(err.Error(), "max retries exceeded:") {
		t.Fatalf("err = %v, want max retries exceeded", err)
	}
	if len(accepts) != 3 {
		t.Errorf("server accepted %d connections, want 3 (initial + 2 retries)", len(accepts))
	}
}

// Error bodies are capped at 1MB: a longer body is truncated and falls back
// to the raw-bytes message.
func TestAnthropicStream_ErrorBodyCap(t *testing.T) {
	t.Parallel()

	pad := strings.Repeat("x", 1500*1024) // 1.5MB message, parses when read whole
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"` + pad + `"}}`))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	_, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if !strings.HasPrefix(apiErr.Message, `{"error":{"message":"xxx`) {
		t.Errorf("message = %.80q, want the truncated raw body (1MB cap) as fallback", apiErr.Message)
	}
}

// A zero idle timeout means no timeoutReader at all: streams flow even with
// slow gaps between events.
func TestAnthropicStream_ZeroIdleTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.idleTimeout = 0
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	sawStop := false
	for evt := range ch {
		if evt.Error != nil {
			t.Fatalf("unexpected stream error: %v", evt.Error)
		}
		if evt.Type == "message_stop" {
			sawStop = true
		}
	}
	if !sawStop {
		t.Error("stream with idleTimeout=0 must survive the 200ms gap")
	}
}

// The SSE parser goroutine runs concurrently with event forwarding: events
// reach the caller while the stream is still in flight.
func TestAnthropicStream_ParserGoroutineDeliversEarly(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(400 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.idleTimeout = 0
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	select {
	case evt, ok := <-ch:
		if !ok || evt.Type != "message_start" {
			t.Fatalf("first event = %+v ok=%v, want message_start while stream in flight", evt, ok)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no event within 200ms — parser not running concurrently")
	}
	for range ch {
	}
}

// Cancelling the context while the forward loop holds unsent events must
// still close the channel promptly.
func TestAnthropicStream_CancelClosesChannel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 80 {
			_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.idleTimeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("stream closed before first event")
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
		t.Fatal("channel not closed after ctx cancel — forward loop stuck")
	}
	srv.Close()
}

// During plain text streaming the idle timeout stays armed: a pause after a
// text content_block_start must kill the stream.
func TestAnthropicStream_TextPhaseKeepsIdleTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.idleTimeout = 50 * time.Millisecond
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var streamErr *APIError
	sawStop := false
	for evt := range ch {
		if evt.Error != nil {
			streamErr = evt.Error
		}
		if evt.Type == "message_stop" {
			sawStop = true
		}
	}
	if streamErr == nil || streamErr.Message != ErrIdleTimeout.Error() {
		t.Fatalf("stream error = %v, want idle timeout (text phase must not disable it)", streamErr)
	}
	if sawStop {
		t.Error("message_stop after idle timeout")
	}
}

// During tool-input streaming the idle timeout stays disabled across argument
// deltas — only a content_block_stop-shaped delta re-arms it.
func TestAnthropicStream_ToolPhaseStaysDisabled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"Write\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.idleTimeout = 50 * time.Millisecond
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for evt := range ch {
		if evt.Error != nil {
			t.Fatalf("stream error: %v — argument deltas must not re-arm the idle timeout", evt.Error)
		}
	}
}

// A content_block_stop-shaped delta re-arms the idle timeout: the subsequent
// pause then kills the stream.
func TestAnthropicStream_StopDeltaReArmsIdleTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"Write\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"content_block_stop\"}}\n\n"))
		// Padding: fill the parser channel so the forward loop processes the
		// re-arm delta before the scanner blocks on the pause below.
		for range 200 {
			_, _ = w.Write([]byte("event: ping\ndata: {}\n\n"))
		}
		w.(http.Flusher).Flush()
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	p.idleTimeout = 50 * time.Millisecond
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var streamErr *APIError
	for evt := range ch {
		if evt.Error != nil {
			streamErr = evt.Error
		}
	}
	if streamErr == nil || streamErr.Message != ErrIdleTimeout.Error() {
		t.Fatalf("stream error = %v, want idle timeout re-armed by the content_block_stop delta", streamErr)
	}
}

// Trailing unflushed data is dropped without an event type; a trailing event
// type without data is dropped too.
func TestAnthropicParseSSE_TrailingFragments(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")
	events := runParseSSE(t, p, "event: ping\ndata: {}\n\ndata: {}")
	if len(events) != 1 || events[0].Type != "ping" {
		t.Errorf("events = %v, want only ping (trailing typeless data dropped)", eventTypes(events))
	}

	events = runParseSSE(t, p, "event: ping\ndata: {}\n\nevent: message_stop")
	if len(events) != 1 || events[0].Type != "ping" {
		t.Errorf("events = %v, want only ping (trailing dataless event dropped)", eventTypes(events))
	}
}

// The scanner buffer accepts lines just under the 10MB cap and rejects lines
// just over it.
func TestAnthropicParseSSE_BufferBoundaries(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")

	// 10485000 bytes: below the 10485760 cap even if a max-side 1024 loses 1.
	ok := strings.Repeat("y", 10485000)
	events := runParseSSE(t, p, "event: ping\ndata: "+ok+"\n\n")
	if len(events) != 1 {
		t.Fatalf("just-under-cap line not parsed: %d events", len(events))
	}

	// 10490000 bytes: over the real cap (mutants that raise it would parse).
	over := strings.Repeat("z", 10490000)
	ch := make(chan StreamEvent, 4)
	if err := p.ParseSSE(context.Background(), strings.NewReader("data: "+over+"\n\n"), nil, ch); err == nil {
		t.Error("just-over-cap line must fail the scanner")
	}
}

// Raw-payload logging truncates at the documented lengths: 500 chars for the
// message_start info line, 200 for malformed-payload warnings.
func TestAnthropicParseEvent_TruncateLogLengths(t *testing.T) {
	p := newAnthropicMutProvider("")
	logBuf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)

	// message_start info line: raw= is the first 500 chars of the data plus
	// "..." — the JSON prefix consumes part of the window.
	prefix := `{"message":{"id":"`
	_ = p.ParseEvent("message_start", prefix+strings.Repeat("a", 600)+`"}}`, nil)
	trunc := strings.Repeat("a", 500-len(prefix)) + "..."
	if !strings.Contains(logBuf.String(), trunc) {
		t.Errorf("message_start raw not truncated at 500; log tail = %.120q", logBuf.String())
	}
	if strings.Contains(logBuf.String(), strings.Repeat("a", 500-len(prefix)+1)+"...") {
		t.Error("message_start raw truncated past 500")
	}
	logBuf.Reset()
	_ = p.ParseEvent("message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`, nil)
	if !strings.Contains(logBuf.String(), "llm:message_delta_raw") || !strings.Contains(logBuf.String(), "output_tokens=7") {
		t.Errorf("message_delta raw info line missing; log = %.200q", logBuf.String())
	}

	// Malformed-payload warnings truncate at 200 chars: "{bad" + 196 b's.
	for _, typ := range []string{"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "error"} {
		logBuf.Reset()
		_ = p.ParseEvent(typ, `{bad`+strings.Repeat("b", 300), nil)
		if !strings.Contains(logBuf.String(), `{bad`+strings.Repeat("b", 200-4)+"...") {
			t.Errorf("malformed %s warning not truncated at 200; log tail = %.120q", typ, logBuf.String())
		}
	}
}

// Image error classification requires both phrasings; each arm alone must not
// trigger.
func TestAnthropicParseAPIError_ImageNegativeCases(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")
	for _, msg := range []string{
		"image exceeds 5mb",
		"maximum size exceeded",
		"image dimensions exceed the limit",
		"many-image cap reached",
	} {
		if got := p.ParseAPIError([]byte(`{"error":{"message":"`+msg+`"}}`), 400).ErrorCode; got != "" {
			t.Errorf("message %q classified as %q, want no code", msg, got)
		}
	}
}

// Response bodies up to just under the 50MB read cap parse whole.
func TestAnthropicComplete_LargeBodyUnderCap(t *testing.T) {
	t.Parallel()

	pad := strings.Repeat("c", 49*1024*1024+512*1024) // ~49.5MB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + pad + `","content":[],"usage":{}}`))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	resp, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Complete on ~49.5MB body: %v", err)
	}
	if resp.ID != pad {
		t.Error("large response ID mismatch")
	}
}

// Bodies past the 50MB read cap truncate and fail to parse.
func TestAnthropicComplete_LargeBodyOverCap(t *testing.T) {
	t.Parallel()

	pad := strings.Repeat("d", 50*1024*1024+512*1024) // ~50.5MB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + pad + `","content":[],"usage":{}}`))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	_, err := p.Complete(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err == nil || !strings.HasPrefix(err.Error(), "unmarshal response:") {
		t.Errorf("err = %v, want unmarshal response: failure past the 50MB cap", err)
	}
}

// Stream's prompt-state record carries zero negotiation inputs like
// Complete's.
func TestAnthropicStream_RecordsZeroNegotiationState(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	key := PromptStateKey{QuerySource: "sdk"}
	ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, PromptStateKey: &key, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	muState.Lock()
	st, ok := stateStore["sdk"]
	muState.Unlock()
	if !ok {
		t.Fatal("no recorded prompt state")
	}
	if st.AutoModeActive || st.IsUsingOverage || st.FastMode || st.CachedMCEnabled || st.ExtraBodyHash != 0 {
		t.Errorf("recorded state = %+v, want zero negotiation inputs", st)
	}
}

// A canceled context surfaces as a chained context error from Stream's send.
func TestAnthropicStream_CanceledCtxChains(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	p := newAnthropicMutProvider(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Stream(ctx, &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
	if err == nil {
		t.Fatal("Stream with canceled ctx = nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want a chained context.Canceled", err)
	}
}

// One-character data payloads still flush as events, including trailing ones.
func TestAnthropicParseSSE_SingleCharData(t *testing.T) {
	t.Parallel()

	p := newAnthropicMutProvider("")
	events := runParseSSE(t, p, "event: ping\ndata: x\n\n")
	if len(events) != 1 || events[0].Type != "ping" {
		t.Errorf("events = %v, want one ping for single-char data", eventTypes(events))
	}

	events = runParseSSE(t, p, "event: ping\ndata: x")
	if len(events) != 1 || events[0].Type != "ping" {
		t.Errorf("events = %v, want one ping for trailing single-char data", eventTypes(events))
	}
}
func TestAnthropicStream_RetryAfterHeaderHonored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A fake Transport keeps every hop on the virtual clock: no real
		// sockets (synctest bubbles deadlock on real IO), and the 2s
		// Retry-After wait advances instantly.
		var hits []time.Time
		tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			hits = append(hits, time.Now())
			if len(hits) == 1 {
				// 503 is the retryable family (429 is deliberately fail-fast
				// per dd472a36); Anthropic's 529 overloaded also lands here.
				h := http.Header{}
				h.Set("Retry-After", "2")
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: h, Body: io.NopCloser(strings.NewReader(`{"type":"error"}`))}, nil
			}
			sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: message_stop\ndata: {}\n\n"
			h := http.Header{}
			h.Set("Content-Type", "text/event-stream")
			return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(sse))}, nil
		})
		p := &AnthropicProvider{name: "t", httpClient: &http.Client{Transport: tr}, idleTimeout: DefaultSSETimeout, apiKey: "k", baseURL: "http://virtual"}
		// The tiny base backoff is what runs if the header is ignored.
		p.retryConfig = &RetryConfig{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
		ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		for evt := range ch {
			if evt.Error != nil {
				t.Fatalf("stream error: %v", evt.Error)
			}
		}
		if len(hits) != 2 {
			t.Fatalf("hits = %d, want 2", len(hits))
		}
		gap := hits[1].Sub(hits[0])
		if gap < 1800*time.Millisecond {
			t.Fatalf("retry gap = %v, want >= 1.8s (Retry-After: 2 honored)", gap)
		}
		if gap > 2*time.Millisecond*1000 {
			t.Fatalf("retry gap = %v, want <= 2s (no sleep beyond the advice)", gap)
		}
	})
}

// Non-positive, non-numeric and absent Retry-After leave the jittered
// backoff in charge; a fail-fast 429 still carries parsed advice.
func TestAnthropicStream_RetryAfterHeaderParsing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, ra := range []string{"0", "-3", "soon", ""} {
			var hits []time.Time
			tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hits = append(hits, time.Now())
				h := http.Header{}
				if ra != "" {
					h.Set("Retry-After", ra)
				}
				if len(hits) == 1 {
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: h, Body: io.NopCloser(strings.NewReader(`{"type":"error"}`))}, nil
				}
				h.Set("Content-Type", "text/event-stream")
				sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: message_stop\ndata: {}\n\n"
				return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(sse))}, nil
			})
			p := &AnthropicProvider{name: "t", httpClient: &http.Client{Transport: tr}, idleTimeout: DefaultSSETimeout, apiKey: "k", baseURL: "http://virtual"}
			p.retryConfig = &RetryConfig{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
			ch, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
			if err != nil {
				t.Fatalf("Stream(Retry-After %q): %v", ra, err)
			}
			for evt := range ch {
				if evt.Error != nil {
					t.Fatalf("stream error (Retry-After %q): %v", ra, evt.Error)
				}
			}
			if len(hits) != 2 {
				t.Fatalf("Retry-After %q: hits = %d, want 2", ra, len(hits))
			}
			if gap := hits[1].Sub(hits[0]); gap > 100*time.Millisecond {
				t.Errorf("Retry-After %q: gap = %v, want jittered backoff (<100ms)", ra, gap)
			}
		}
		// 429 is fail-fast by design (dd472a36) but the parsed advice must
		// still ride the APIError for callers.
		tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			h := http.Header{}
			h.Set("Retry-After", "7")
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: h, Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error"}}`))}, nil
		})
		p := &AnthropicProvider{name: "t", httpClient: &http.Client{Transport: tr}, idleTimeout: DefaultSSETimeout, apiKey: "k", baseURL: "http://virtual"}
		_, err := p.Stream(t.Context(), &Request{Model: "m", MaxTokens: 1, Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}}})
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("429 error = %v, want *APIError", err)
		}
		if apiErr.RetryAfter != 7*time.Second {
			t.Errorf("fail-fast 429 RetryAfter = %v, want 7s", apiErr.RetryAfter)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
