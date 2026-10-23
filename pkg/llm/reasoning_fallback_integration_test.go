package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Scriptable chat-completions server: request N runs step N (the last step
// repeats); every JSON body is recorded so tests can assert exact wire
// dialects and request counts. reset() swaps the script and zeroes the
// counter, simulating a server-side cold start for restart scenarios.
// ---------------------------------------------------------------------------

type chatScriptStep func(w http.ResponseWriter, body map[string]any)

type chatScriptServer struct {
	mu     sync.Mutex
	count  int
	bodies []map[string]any
	steps  []chatScriptStep
	srv    *httptest.Server
}

func newChatScriptServer(steps ...chatScriptStep) *chatScriptServer {
	s := &chatScriptServer{steps: steps}
	s.srv = httptest.NewServer(s)
	return s
}

func (s *chatScriptServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	idx := s.count
	s.count++
	steps := s.steps
	s.mu.Unlock()
	if idx >= len(steps) {
		idx = len(steps) - 1
	}
	steps[idx](w, body)
}

func (s *chatScriptServer) url() string { return s.srv.URL }

func (s *chatScriptServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *chatScriptServer) body(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

func (s *chatScriptServer) reset(steps ...chatScriptStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = steps
	s.count = 0
	s.bodies = nil
}

func chatSSEStep(w http.ResponseWriter, _ map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`+"\n\n")
	_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func chatCompletionStep(w http.ResponseWriter, _ map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"prompt_tokens_details":{},"completion_tokens_details":{}}}`)
}

func chatErrorStep(status int, body []byte) chatScriptStep {
	return func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func newChatProvider(url string, extra map[string]any) *OpenAIProvider {
	return NewOpenAIProvider(&OpenAIConfig{
		Name:        "scripted",
		APIKey:      "test-key",
		BaseURL:     url,
		Model:       "m",
		ExtraParams: extra,
	})
}

func reasoningUserRequest(e Effort) *Request {
	return &Request{
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		},
		Thinking: e,
	}
}

func drainStream(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func assertSuccessfulStream(t *testing.T, events []StreamEvent) {
	t.Helper()
	assertEventTypes(t, events, "message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop")
	if len(events) < 3 || events[2].Delta == nil || events[2].Delta.Text != "ok" {
		t.Errorf("text delta = %+v, want ok", events[2].Delta)
	}
	if events[4].DeltaMsg == nil || events[4].DeltaMsg.StopReason != "end_turn" {
		t.Errorf("stop reason = %+v, want end_turn", events[4].DeltaMsg)
	}
}

func assertBodyString(t *testing.T, body map[string]any, key, want string) {
	t.Helper()
	if got := body[key]; got != want {
		t.Errorf("body[%q] = %v, want %q", key, got, want)
	}
}

func assertBodyAbsent(t *testing.T, body map[string]any, key string) {
	t.Helper()
	if _, present := body[key]; present {
		t.Errorf("body[%q] = %v, want key absent", key, body[key])
	}
}

// ---------------------------------------------------------------------------
// G1: cold start, freeze reuse, and auto-request isolation on one instance.
// ---------------------------------------------------------------------------

func TestStream_ReasoningFallbackLifecycle(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, invalidReasoningBody("reasoning_effort", "max")),
		chatSSEStep,
		chatSSEStep,
		chatSSEStep,
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	ctx := context.Background()

	// Cold start: the 400 quotes a must-be list without max, so the retry
	// carries the nearest tier high — exactly two requests on the wire.
	ch, err := p.Stream(ctx, reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("cold start stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if got := srv.requestCount(); got != 2 {
		t.Fatalf("cold start requests = %d, want 2 (400 + negotiated retry)", got)
	}
	assertBodyString(t, srv.body(0), "reasoning_effort", "max")
	assertBodyString(t, srv.body(1), "reasoning_effort", "high")
	assertBodyAbsent(t, srv.body(0), "thinking")
	assertBodyAbsent(t, srv.body(1), "thinking")

	// Frozen conclusion: the same selection connects on the first try.
	ch, err = p.Stream(ctx, reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("frozen stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if got := srv.requestCount(); got != 3 {
		t.Fatalf("frozen requests total = %d, want 3 (single request, no 400 round-trip)", got)
	}
	assertBodyString(t, srv.body(2), "reasoning_effort", "high")

	// An auto request carries no effort spelling and must not be injected
	// with the frozen tier — rewriting only touches existing spellings.
	ch, err = p.Stream(ctx, reasoningUserRequest(""))
	if err != nil {
		t.Fatalf("auto stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if got := srv.requestCount(); got != 4 {
		t.Fatalf("auto requests total = %d, want 4", got)
	}
	assertBodyAbsent(t, srv.body(3), "reasoning_effort")
}

// G2: a fresh provider instance renegotiates — freezing is per-instance
// memory, never persisted.
func TestStream_ReasoningFallbackRestartRenegotiates(t *testing.T) {
	coldScript := func(steps *[]chatScriptStep) {
		*steps = []chatScriptStep{
			chatErrorStep(400, invalidReasoningBody("reasoning_effort", "max")),
			chatSSEStep,
		}
	}
	var steps []chatScriptStep
	coldScript(&steps)
	srv := newChatScriptServer(steps...)
	defer srv.srv.Close()
	ctx := context.Background()

	first := newChatProvider(srv.url(), nil)
	ch, err := first.Stream(ctx, reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("first instance stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("first instance requests = %d, want 2", srv.requestCount())
	}

	// Simulated restart: same endpoint, cold script, brand-new instance.
	coldScript(&steps)
	srv.reset(steps...)
	second := newChatProvider(srv.url(), nil)
	ch, err = second.Stream(ctx, reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("second instance stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("second instance requests = %d, want 2 (renegotiated from scratch)", srv.requestCount())
	}
	assertBodyString(t, srv.body(0), "reasoning_effort", "max")
	assertBodyString(t, srv.body(1), "reasoning_effort", "high")
}

// G4: a rejection with no allowed vocabulary freezes "drop the field".
func TestStream_ReasoningFallbackNullFreeze(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, noCueReasoningBody()),
		chatSSEStep,
		chatSSEStep,
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	ctx := context.Background()

	ch, err := p.Stream(ctx, reasoningUserRequest(EffortHigh))
	if err != nil {
		t.Fatalf("null-freeze stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("null-freeze requests = %d, want 2", srv.requestCount())
	}
	assertBodyString(t, srv.body(0), "reasoning_effort", "high")
	assertBodyAbsent(t, srv.body(1), "reasoning_effort")

	// The frozen null applies to later requests — still no field.
	ch, err = p.Stream(ctx, reasoningUserRequest(EffortHigh))
	if err != nil {
		t.Fatalf("frozen null stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 3 {
		t.Fatalf("frozen null requests = %d, want 3", srv.requestCount())
	}
	assertBodyAbsent(t, srv.body(2), "reasoning_effort")
}

// G3: explicit-disable fallbacks stay per-request — a games-style none
// request must not poison later enabled turns.
func TestStream_ReasoningFallbackExplicitDisableNotFrozen(t *testing.T) {
	srv := newChatScriptServer(func(w http.ResponseWriter, body map[string]any) {
		if body["reasoning_effort"] == "none" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(copilotUnsupportedValueBody("none", "gpt-6-astra"))
			return
		}
		chatSSEStep(w, body)
	})
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	ctx := context.Background()

	// The none request is rejected and retried clamped to the lowest tier.
	ch, err := p.Stream(ctx, reasoningUserRequest(EffortNone))
	if err != nil {
		t.Fatalf("explicit-disable stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("explicit-disable requests = %d, want 2", srv.requestCount())
	}
	assertBodyString(t, srv.body(0), "reasoning_effort", "none")
	assertBodyString(t, srv.body(1), "reasoning_effort", "low")

	// No freeze: a normal high request still sends high and connects at once.
	ch, err = p.Stream(ctx, reasoningUserRequest(EffortHigh))
	if err != nil {
		t.Fatalf("post-disable stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 3 {
		t.Fatalf("post-disable requests = %d, want 3 (frozen low would have leaked)", srv.requestCount())
	}
	assertBodyString(t, srv.body(2), "reasoning_effort", "high")
}

// G5: a non-negotiable 400 surfaces immediately with exactly one request.
func TestStream_ReasoningFallbackUnrelated400NoRetry(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, []byte(`{"error":{"message":"invalid model","type":"invalid_request_error"}}`)),
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)

	_, err := p.Stream(context.Background(), reasoningUserRequest(EffortHigh))
	if err == nil {
		t.Fatal("stream err = nil, want the 400 to surface")
	}
	if err.Error() != "API Error 400: invalid model" {
		t.Errorf("err = %q, want %q", err.Error(), "API Error 400: invalid model")
	}
	if srv.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1 (no retry for unrelated rejections)", srv.requestCount())
	}
}

// G6: the non-streaming Complete path negotiates the same way and freezes
// for later calls.
func TestComplete_ReasoningFallbackColdStart(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, invalidReasoningBody("reasoning_effort", "max")),
		chatCompletionStep,
		chatCompletionStep,
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	ctx := context.Background()

	resp, err := p.Complete(ctx, reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "ok" {
		t.Errorf("content = %+v, want single ok text block", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop reason = %q, want end_turn", resp.StopReason)
	}
	if srv.requestCount() != 2 {
		t.Fatalf("complete requests = %d, want 2", srv.requestCount())
	}
	assertBodyString(t, srv.body(0), "reasoning_effort", "max")
	assertBodyString(t, srv.body(1), "reasoning_effort", "high")

	// Second call: frozen tier, one request.
	resp, err = p.Complete(ctx, reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "ok" {
		t.Errorf("second content = %+v, want single ok text block", resp.Content)
	}
	if srv.requestCount() != 3 {
		t.Fatalf("second complete requests = %d, want 3 (single request)", srv.requestCount())
	}
	assertBodyString(t, srv.body(2), "reasoning_effort", "high")
}

// G7: an extra_params GLM toggle survives negotiation untouched — the null
// conclusion drops only the effort field, never user-provided params.
func TestStream_ReasoningFallbackExtraParamsCoexist(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, noCueReasoningBody()),
		chatSSEStep,
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), map[string]any{"thinking": map[string]any{"type": "enabled"}})

	ch, err := p.Stream(context.Background(), reasoningUserRequest(EffortHigh))
	if err != nil {
		t.Fatalf("extra-params stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("extra-params requests = %d, want 2", srv.requestCount())
	}
	assertBodyString(t, srv.body(0), "reasoning_effort", "high")
	if got := fmt.Sprint(srv.body(0)["thinking"]); !strings.Contains(got, "enabled") {
		t.Errorf("body0 thinking = %v, want enabled toggle present", srv.body(0)["thinking"])
	}
	assertBodyAbsent(t, srv.body(1), "reasoning_effort")
	if got := fmt.Sprint(srv.body(1)["thinking"]); !strings.Contains(got, "enabled") {
		t.Errorf("body1 thinking = %v, want the exemption to survive negotiation", srv.body(1)["thinking"])
	}
}

// ---------------------------------------------------------------------------
// The freeze key scopes by wire model, and an empty request model resolves to
// the provider default — both sides of that resolution are pinned by one
// freeze/lookup sequence.
func TestStream_ReasoningFallbackKeyScopedByModel(t *testing.T) {
	srv := newChatScriptServer(func(w http.ResponseWriter, body map[string]any) {
		switch body["reasoning_effort"] {
		case "none":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(copilotUnsupportedValueBody("none", "gpt-6-astra"))
		case "max":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(invalidReasoningBody("reasoning_effort", "max"))
		default:
			chatSSEStep(w, body)
		}
	})
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	ctx := context.Background()

	// An explicit-disable request under an explicit wire model negotiates to
	// low but must not freeze anything.
	req := reasoningUserRequest(EffortNone)
	req.Model = "explicit-model"
	ch, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatalf("explicit-disable stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("explicit-disable requests = %d, want 2", srv.requestCount())
	}

	// A provider-default-model request (empty request model) negotiates max →
	// high and freezes under the default model's key.
	ch, err = p.Stream(ctx, reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("default-model stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if got := srv.requestCount(); got != 4 {
		t.Fatalf("default-model requests = %d, want 4 (fresh negotiation)", got)
	}
	assertBodyString(t, srv.body(2), "reasoning_effort", "max")
	assertBodyString(t, srv.body(3), "reasoning_effort", "high")

	// A request whose model string equals the provider default must hit that
	// frozen conclusion: single request, already downgraded.
	req = reasoningUserRequest(EffortMax)
	req.Model = "m"
	ch, err = p.Stream(ctx, req)
	if err != nil {
		t.Fatalf("default-model-string stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if got := srv.requestCount(); got != 5 {
		t.Fatalf("default-model-string requests = %d, want 5 (frozen key hit)", got)
	}
	assertBodyString(t, srv.body(4), "reasoning_effort", "high")
}

// Transport-path pins: what each failure layer surfaces and how the
// negotiation layer reacts to non-negotiable errors.
// ---------------------------------------------------------------------------

// hijackCloseStep cuts the TCP connection mid-request, so the client sees a
// transport-level EOF instead of an HTTP response.
func hijackCloseStep(w http.ResponseWriter, _ map[string]any) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

func fastRetries(p *OpenAIProvider) {
	p.retryConfig = &RetryConfig{MaxRetries: 0, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

// A negotiable 400 followed by a transport failure on the retry: the
// negotiation already succeeded once, so the transport error surfaces and
// nothing is frozen.
func TestStream_ReasoningFallbackRetryTransportFailure(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, invalidReasoningBody("reasoning_effort", "max")),
		hijackCloseStep,
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	fastRetries(p)

	_, err := p.Stream(context.Background(), reasoningUserRequest(EffortMax))
	if err == nil {
		t.Fatal("stream err = nil, want transport failure to surface")
	}
	if !strings.Contains(err.Error(), "EOF") {
		t.Errorf("err = %q, want an EOF transport error", err)
	}
	if srv.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", srv.requestCount())
	}
	// Nothing froze: the next stream renegotiates from the original tier.
	srv.reset(
		chatErrorStep(400, invalidReasoningBody("reasoning_effort", "max")),
		chatSSEStep,
	)
	ch, err := p.Stream(context.Background(), reasoningUserRequest(EffortMax))
	if err != nil {
		t.Fatalf("post-failure stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("post-failure requests = %d, want 2 (no freeze after failed retry)", srv.requestCount())
	}
}

// Complete surfaces transport errors from both its first and second POST.
func TestComplete_TransportFailures(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, invalidReasoningBody("reasoning_effort", "max")),
		hijackCloseStep,
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)

	_, err := p.Complete(context.Background(), reasoningUserRequest(EffortMax))
	if err == nil {
		t.Fatal("complete err = nil, want the retry's EOF to surface")
	}
	if !strings.Contains(err.Error(), "send request: ") || !strings.Contains(err.Error(), "EOF") {
		t.Errorf("err = %q, want send-request-wrapped EOF", err)
	}
	// The %w chain must keep the underlying url.Error unwrappable.
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("err = %v, want an unwrappable *url.Error", err)
	}
	if srv.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", srv.requestCount())
	}
}

// Complete surfaces a non-negotiable rejection on its retry unchanged.
func TestComplete_ReasoningFallbackRetryNonNegotiable(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(400, invalidReasoningBody("reasoning_effort", "max")),
		chatErrorStep(400, []byte(`{"error":{"message":"invalid model","type":"invalid_request_error"}}`)),
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)

	_, err := p.Complete(context.Background(), reasoningUserRequest(EffortMax))
	if err == nil {
		t.Fatal("complete err = nil, want the second 400 to surface")
	}
	if err.Error() != "API Error 400: invalid model" {
		t.Errorf("err = %q, want %q", err.Error(), "API Error 400: invalid model")
	}
	if srv.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", srv.requestCount())
	}
}

// Retryable statuses flow through the transport retry loop, and exhausting
// it surfaces "max retries exceeded" — the negotiation layer ignores the
// wrapped APIError because no body was captured for it.
func TestStream_RetryableStatusAndExhaustion(t *testing.T) {
	srv := newChatScriptServer(
		chatErrorStep(500, []byte(`{"error":{"message":"boom","type":"server_error"}}`)),
		chatSSEStep,
	)
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	p.retryConfig = &RetryConfig{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	// 500 then success: one transport retry, no negotiation involved.
	ch, err := p.Stream(context.Background(), reasoningUserRequest(EffortHigh))
	if err != nil {
		t.Fatalf("5xx-then-success stream: %v", err)
	}
	assertSuccessfulStream(t, drainStream(t, ch))
	if srv.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", srv.requestCount())
	}

	// Exhausted retries: the last error wraps up with no captured body, so
	// negotiation declines and the wrapped error surfaces.
	srv.reset(chatErrorStep(500, []byte(`{"error":{"message":"boom","type":"server_error"}}`)))
	fastRetries(p)
	_, err = p.Stream(context.Background(), reasoningUserRequest(EffortHigh))
	if err == nil {
		t.Fatal("stream err = nil, want retry exhaustion")
	}
	if err.Error() != "max retries exceeded: API Error 500: boom" {
		t.Errorf("err = %q, want %q", err.Error(), "max retries exceeded: API Error 500: boom")
	}
	// The wrap must keep the APIError unwrappable for the engine's retry
	// classifier (errors.AsType walks the %w chain).
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != 500 {
		t.Errorf("wrapped error does not unwrap to the 500 APIError: %v", err)
	}
	if srv.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1", srv.requestCount())
	}
}

// Unbuildable requests fail at request construction; the bare error keeps the
// %w chain, and the callers wrap it with the single send-request prefix.
func TestChatTransport_InvalidURL(t *testing.T) {
	p := &OpenAIProvider{apiKey: "k", baseURL: "http://exa mple.com"}
	var urlErr *url.Error

	_, _, _, err := p.postChat(context.Background(), []byte("{}"))
	if err == nil || strings.Contains(err.Error(), "create request: ") {
		t.Errorf("postChat err = %v, want bare create failure", err)
	}
	if !errors.As(err, &urlErr) {
		t.Errorf("postChat err = %v, want unwrappable *url.Error", err)
	}

	_, _, err = p.connectChatStream(context.Background(), []byte("{}"))
	if err == nil || !strings.Contains(err.Error(), "send request: ") || strings.Contains(err.Error(), "create request: ") {
		t.Errorf("connectChatStream err = %v, want single send-request wrap", err)
	}
	if !errors.As(err, &urlErr) {
		t.Errorf("connectChatStream err = %v, want unwrappable *url.Error", err)
	}

	_, err = p.Complete(context.Background(), reasoningUserRequest(EffortHigh))
	if err == nil || !strings.Contains(err.Error(), "send request: ") || strings.Contains(err.Error(), "create request: ") {
		t.Errorf("complete err = %v, want single send-request wrap", err)
	}
	if !errors.As(err, &urlErr) {
		t.Errorf("complete err = %v, want unwrappable *url.Error", err)
	}
}

// postChat drains non-200 bodies at exactly the 1MB error cap (aligned with
// the stream path; Complete's old 50MB whole-body read is gone by design).
func TestPostChat_ErrorBodyCap(t *testing.T) {
	var handler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2<<20)) // 2MB
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	p := newChatProvider(srv.URL, nil)

	_, apiErr, captured, err := p.postChat(context.Background(), []byte("{}"))
	if err != nil || apiErr == nil || captured == nil {
		t.Fatalf("postChat = (%v, %v, %v), want parsed 400", err, apiErr, captured)
	}
	if len(apiErr.Message) != 1<<20 {
		t.Errorf("apiErr.Message length = %d, want exactly %d (1MB error-body cap)", len(apiErr.Message), 1<<20)
	}
	if len(captured.bodyText) != 1<<20 {
		t.Errorf("captured.bodyText length = %d, want exactly %d", len(captured.bodyText), 1<<20)
	}
}

// A frozen explicit-model conclusion must apply only to that model: a
// disable request under a different model neither inherits nor disturbs it.
// The sequence also pins that a request model equal to the provider default
// resolves into the default's freeze slot.
func TestStream_ReasoningFallbackExplicitModelFreeze(t *testing.T) {
	srv := newChatScriptServer(func(w http.ResponseWriter, body map[string]any) {
		switch body["reasoning_effort"] {
		case "none":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(copilotUnsupportedValueBody("none", "gpt-6-astra"))
		case "max":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(invalidReasoningBody("reasoning_effort", "max"))
		default:
			chatSSEStep(w, body)
		}
	})
	defer srv.srv.Close()
	p := newChatProvider(srv.url(), nil)
	ctx := context.Background()
	stream := func(model string, e Effort) {
		t.Helper()
		req := reasoningUserRequest(e)
		req.Model = model
		ch, err := p.Stream(ctx, req)
		if err != nil {
			t.Fatalf("stream(model=%q, effort=%q): %v", model, e, err)
		}
		assertSuccessfulStream(t, drainStream(t, ch))
	}

	// Freeze "explicit" → high under the explicit model's key.
	stream("explicit", EffortMax)
	if srv.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", srv.requestCount())
	}

	// A disable request under another model starts clean: none is sent as
	// none (a leaked frozen tier would rewrite it), then clamps to low.
	stream("other", EffortNone)
	if srv.requestCount() != 4 {
		t.Fatalf("requests = %d, want 4 (no inherited freeze)", srv.requestCount())
	}
	assertBodyString(t, srv.body(2), "reasoning_effort", "none")
	assertBodyString(t, srv.body(3), "reasoning_effort", "low")

	// The explicit model's frozen tier still applies to it alone.
	stream("explicit", EffortMax)
	if srv.requestCount() != 5 {
		t.Fatalf("requests = %d, want 5 (explicit model frozen)", srv.requestCount())
	}
	assertBodyString(t, srv.body(4), "reasoning_effort", "high")
}
