package message

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeResolver returns canned Resolve results and records queried ids in
// order, so tests can assert the resolver was (or was not) consulted.
type fakeResolver struct {
	target  Target
	status  string
	ok      bool
	queried []string
}

func (f *fakeResolver) Resolve(id string) (Target, string, bool) {
	f.queried = append(f.queried, id)
	return f.target, f.status, f.ok
}

func (f *fakeResolver) queryCount() int { return len(f.queried) }

func (f *fakeResolver) lastQuery() string {
	if len(f.queried) == 0 {
		return ""
	}
	return f.queried[len(f.queried)-1]
}

// spyTarget records every enqueued item; tests assert count and content.
type spyTarget struct {
	mu    sync.Mutex
	items []types.QueuedItem
}

func (s *spyTarget) EnqueueAttachment(item types.QueuedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
}

func (s *spyTarget) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

func (s *spyTarget) last() types.QueuedItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return types.QueuedItem{}
	}
	return s.items[len(s.items)-1]
}

// newTool builds a MessageTool wired to a running spy target, selfID "main"
// (what WireEngine injects for the top-level engine in production).
func newTool(t *testing.T) (*MessageTool, *fakeResolver, *spyTarget) {
	t.Helper()
	spy := &spyTarget{}
	res := &fakeResolver{target: spy, status: "running", ok: true}
	tl := New(res)
	tl.SetSelfID("main")
	return tl, res, spy
}

func callJSON(t *testing.T, tl *MessageTool, input string) (*tool.ToolResult, error) {
	t.Helper()
	return tl.Call(context.Background(), json.RawMessage(input), &tool.ToolUseContext{})
}

// ---------------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------------

func TestCall_DeliversEnvelope(t *testing.T) {
	t.Parallel()
	tl, _, spy := newTool(t)
	res, err := callJSON(t, tl, `{"to":"agent-3","message":"hello main"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res == nil {
		t.Fatal("Call returned nil result")
	}
	want := MessageResult{To: "agent-3", Message: "hello main"}
	if got := res.Data; got != want {
		t.Errorf("result Data = %+v, want %+v", got, want)
	}
	if got := spy.count(); got != 1 {
		t.Fatalf("enqueued %d items, want 1", got)
	}
	item := spy.last()
	if item.Mode != types.ItemModeMessage {
		t.Errorf("Mode = %q, want %q", item.Mode, types.ItemModeMessage)
	}
	if !strings.Contains(item.Value, `<message from="main">`) {
		t.Errorf("Value = %q, want <message from=\"main\"> envelope header", item.Value)
	}
	if !strings.Contains(item.Value, "hello main") {
		t.Errorf("Value = %q, want the body text", item.Value)
	}
	if !strings.Contains(item.Value, "</message>") {
		t.Errorf("Value = %q, want </message> envelope footer", item.Value)
	}
	if item.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want the delivery time")
	}
}

// Engine targets (status "") deliver identically — no status gate applies.
func TestCall_DeliversToEngineTarget(t *testing.T) {
	t.Parallel()
	spy := &spyTarget{}
	res := &fakeResolver{target: spy, status: "", ok: true}
	tl := New(res)
	tl.SetSelfID("main")
	if _, err := callJSON(t, tl, `{"to":"e2","message":"status?"}`); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := spy.count(); got != 1 {
		t.Fatalf("enqueued %d items, want 1", got)
	}
	if !strings.Contains(spy.last().Value, `<message from="main">`) {
		t.Errorf("Value = %q, want envelope with from=main", spy.last().Value)
	}
}

// TestMessageTool_DeliveryUsesMessageChannel pins the delivery channel: the
// item rides its own mode and origin so the engine persists the envelope
// verbatim instead of reminder-wrapping it. Value is pinned to the exact
// buildEnvelope bytes.
func TestMessageTool_DeliveryUsesMessageChannel(t *testing.T) {
	t.Parallel()
	tl, _, spy := newTool(t)
	if _, err := callJSON(t, tl, `{"to":"agent-3","message":"hello main"}`); err != nil {
		t.Fatalf("Call: %v", err)
	}
	item := spy.last()
	if item.Mode != types.ItemModeMessage {
		t.Errorf("Mode = %q, want %q", item.Mode, types.ItemModeMessage)
	}
	if item.Origin == nil || item.Origin.Kind != types.OriginMessage {
		t.Errorf("Origin = %+v, want Kind %q", item.Origin, types.OriginMessage)
	}
	want := "<message from=\"main\">\nhello main\n</message>"
	if item.Value != want {
		t.Errorf("Value = %q, want the exact buildEnvelope bytes %q", item.Value, want)
	}
}

// A whitespace-padded to is trimmed before resolution.
func TestCall_TrimsToBeforeResolve(t *testing.T) {
	t.Parallel()
	tl, res, spy := newTool(t)
	if _, err := callJSON(t, tl, `{"to":"  agent-3 \n","message":"ping"}`); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := res.lastQuery(); got != "agent-3" {
		t.Errorf("resolver queried %q, want the trimmed agent-3", got)
	}
	if got := spy.count(); got != 1 {
		t.Errorf("enqueued %d items, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Rejections
// ---------------------------------------------------------------------------

func TestCall_RejectsSelfEngineID(t *testing.T) {
	t.Parallel()
	spy := &spyTarget{}
	res := &fakeResolver{target: spy, status: "", ok: true}
	tl := New(res)
	tl.SetSelfID("main")
	_, err := callJSON(t, tl, `{"to":"main","message":"note to self"}`)
	if err == nil {
		t.Fatal("self-send must error")
	}
	if !strings.Contains(err.Error(), "cannot send a message to yourself") {
		t.Errorf("err = %q, want the self-send rejection", err.Error())
	}
	want := 0 // nothing enqueued
	if got := spy.count(); got != want {
		t.Errorf("enqueued %d items, want %d", got, want)
	}
}

func TestCall_RejectsSelfAgentID(t *testing.T) {
	t.Parallel()
	spy := &spyTarget{}
	res := &fakeResolver{target: spy, status: "running", ok: true}
	tl := New(res)
	tl.SetSelfID("agent-3")
	_, err := callJSON(t, tl, `{"to":"agent-3","message":"echo"}`)
	if err == nil {
		t.Fatal("self-send must error")
	}
	if !strings.Contains(err.Error(), "cannot send a message to yourself") {
		t.Errorf("err = %q, want the self-send rejection", err.Error())
	}
	want := 0 // nothing enqueued
	if got := spy.count(); got != want {
		t.Errorf("enqueued %d items, want %d", got, want)
	}
}

func TestCall_UnknownTarget(t *testing.T) {
	t.Parallel()
	spy := &spyTarget{}
	res := &fakeResolver{target: spy, ok: false}
	tl := New(res)
	_, err := callJSON(t, tl, `{"to":"nope","message":"anyone there?"}`)
	if err == nil {
		t.Fatal("unknown target must error")
	}
	for _, want := range []string{`unknown target "nope"`, "Job"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err.Error(), want)
		}
	}
	want := 0 // nothing enqueued
	if got := spy.count(); got != want {
		t.Errorf("enqueued %d items, want %d", got, want)
	}
}

func TestCall_RejectsEmptyTo(t *testing.T) {
	t.Parallel()
	tl, res, _ := newTool(t)
	_, err := callJSON(t, tl, `{"to":"","message":"hi"}`)
	if err == nil {
		t.Fatal("empty to must error")
	}
	if got := err.Error(); got != "to is required" {
		t.Errorf("err = %q, want %q", got, "to is required")
	}
	if res.queryCount() != 0 {
		t.Errorf("resolver consulted %d times, want 0", res.queryCount())
	}
}

func TestCall_RejectsBlankMessage(t *testing.T) {
	t.Parallel()
	tl, res, _ := newTool(t)
	_, err := callJSON(t, tl, `{"to":"agent-3","message":"  \n\t"}`)
	if err == nil {
		t.Fatal("blank message must error")
	}
	if got := err.Error(); got != "message must not be empty" {
		t.Errorf("err = %q, want %q", got, "message must not be empty")
	}
	if res.queryCount() != 0 {
		t.Errorf("resolver consulted %d times, want 0", res.queryCount())
	}
}

func TestCall_RejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	tl, _, _ := newTool(t)
	_, err := callJSON(t, tl, `{"to":`)
	if err == nil {
		t.Fatal("invalid JSON must error, not panic")
	}
	if !strings.HasPrefix(err.Error(), "parse input:") {
		t.Errorf("err = %q, want the parse-input wrapper", err.Error())
	}
	// %w wrapping keeps the JSON syntax error reachable for callers.
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("errors.As(*json.SyntaxError) failed on %q — cause not wrapped", err.Error())
	}
}

func TestCall_TargetTerminal(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed", "cancelled"} {
		spy := &spyTarget{}
		res := &fakeResolver{target: nil, status: status, ok: true}
		tl := New(res)
		_, err := callJSON(t, tl, `{"to":"agent-1","message":"still there?"}`)
		if err == nil {
			t.Fatalf("status %q must error", status)
		}
		for _, want := range []string{"not running", status} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("status %q: err = %q, want it to contain %q", status, err.Error(), want)
			}
		}
		want := 0 // nothing enqueued
		if got := spy.count(); got != want {
			t.Errorf("status %q: enqueued %d items, want %d", status, got, want)
		}
	}
}

func TestCall_TargetStarting(t *testing.T) {
	t.Parallel()
	spy := &spyTarget{}
	res := &fakeResolver{target: nil, status: "starting", ok: true}
	tl := New(res)
	_, err := callJSON(t, tl, `{"to":"agent-2","message":"up yet?"}`)
	if err == nil {
		t.Fatal("starting target must error")
	}
	if !strings.Contains(err.Error(), "still starting") {
		t.Errorf("err = %q, want the starting rejection", err.Error())
	}
	want := 0 // nothing enqueued
	if got := spy.count(); got != want {
		t.Errorf("enqueued %d items, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// Envelope escaping
// ---------------------------------------------------------------------------

func TestCall_EscapesXMLBody(t *testing.T) {
	t.Parallel()
	spy := &spyTarget{}
	res := &fakeResolver{target: spy, status: "", ok: true}
	tl := New(res)
	tl.SetSelfID("main")
	body := "found a<b>&c — 配置在 config/app.yaml"
	if _, err := callJSON(t, tl, `{"to":"e2","message":"`+body+`"}`); err != nil {
		t.Fatalf("Call: %v", err)
	}
	value := spy.last().Value
	for _, want := range []string{"a&lt;b&gt;&amp;c", "配置在 config/app.yaml", `<message from="main">`, "</message>"} {
		if !strings.Contains(value, want) {
			t.Errorf("Value = %q, want it to contain %q", value, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Schema / prompt / description
// ---------------------------------------------------------------------------

func TestInputSchema_RequiredFields(t *testing.T) {
	t.Parallel()
	tl, _, _ := newTool(t)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("schema does not parse: %v", err)
	}
	if len(schema.Properties) != 2 {
		t.Fatalf("properties count = %d (%v), want exactly to and message", len(schema.Properties), schema.Properties)
	}
	if _, ok := schema.Properties["to"]; !ok {
		t.Error("schema lacks the to property")
	}
	if _, ok := schema.Properties["message"]; !ok {
		t.Error("schema lacks the message property")
	}
	if len(schema.Required) != 2 || schema.Required[0] != "to" || schema.Required[1] != "message" {
		t.Errorf("required = %v, want [to message]", schema.Required)
	}
}

func TestPrompt_Mechanics(t *testing.T) {
	t.Parallel()
	tl, _, _ := newTool(t)
	prompt := tl.Prompt()
	for _, want := range []string{
		`{"to": "agent-3", "message":`,
		`{"to": "agent-1"`,
		`Job tool with action "list"`,
		"agent-N",
		"an engine's display name is its id",
		"unless the user named it at creation",
		"legacy e-N",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Prompt() = %q, want it to contain %q", prompt, want)
		}
	}
	for _, banned := range []string{"engine:", "SendMessage", `"Message":`, "fork-2", "chat-"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("Prompt() contains banned text %q", banned)
		}
	}
}

func TestInputSchema_ToDescription(t *testing.T) {
	t.Parallel()
	tl, _, _ := newTool(t)
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("schema does not parse: %v", err)
	}
	desc := schema.Properties["to"].Description
	for _, want := range []string{
		"background agent ID (agent-3)",
		"engine ID (main, dream, agent-1",
		"an engine's display name is its ID unless the user named it at creation",
		"legacy e2",
		"Job tool's list output",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("to description = %q, want it to contain %q", desc, want)
		}
	}
	for _, banned := range []string{"chat-", "fork-"} {
		if strings.Contains(desc, banned) {
			t.Errorf("to description contains banned text %q", banned)
		}
	}
}

func TestDescription_SummarizesMechanics(t *testing.T) {
	t.Parallel()
	tl, _, _ := newTool(t)
	desc, err := tl.Description(nil)
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if !strings.Contains(desc, "message") {
		t.Errorf("Description = %q, want it to mention message sending", desc)
	}
	if !strings.Contains(desc, "agent") {
		t.Errorf("Description = %q, want it to mention agents", desc)
	}
}

func TestSetSelfID_AccessorRoundTrip(t *testing.T) {
	t.Parallel()
	tl := New(&fakeResolver{})
	if got := tl.SelfID(); got != "" {
		t.Errorf("initial SelfID = %q, want empty", got)
	}
	tl.SetSelfID("x")
	if got := tl.SelfID(); got != "x" {
		t.Errorf("SelfID after SetSelfID(x) = %q, want x", got)
	}
}

// ---------------------------------------------------------------------------
// Tool contract
// ---------------------------------------------------------------------------

func TestToolContract(t *testing.T) {
	t.Parallel()
	tl, _, _ := newTool(t)
	if got := tl.Name(); got != "Message" {
		t.Errorf("Name() = %q, want Message", got)
	}
	if got := tl.Aliases(); got != nil {
		t.Errorf("Aliases() = %v, want none", got)
	}
	if got := tl.InterruptBehavior(); got != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", got)
	}
	if tl.IsReadOnly(nil) {
		t.Error("IsReadOnly = true, want false (mutates the target queue)")
	}
	if !tl.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe = false, want true")
	}
	if tool.IsDeferred(tl) {
		t.Error("IsDeferred = true, want false")
	}
	if got := tl.MaxResultSize(); got != 10000 {
		t.Errorf("MaxResultSize() = %d, want 10000", got)
	}
	perm := tl.CheckPermissions(nil, &tool.ToolUseContext{})
	if _, ok := perm.(types.PermissionAllowDecision); !ok {
		t.Errorf("CheckPermissions = %T, want allow-always", perm)
	}
}

// Wire format: string Data goes out as a single text block, non-string Data
// falls back to its JSON encoding.
func TestWireBlocks(t *testing.T) {
	t.Parallel()
	tl, _, _ := newTool(t)
	wb, ok := tool.Tool(tl).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("MessageTool must implement ToolWithWireBlocks")
	}
	blocks := wb.FormatWireBlocks("Delivered to agent-3")
	if len(blocks) != 1 {
		t.Fatalf("string data: %d blocks, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "Delivered to agent-3" {
		t.Errorf("string data: block = %+v, want text Delivered to agent-3", blocks[0])
	}
	blocks = wb.FormatWireBlocks(map[string]int{"n": 1})
	if len(blocks) != 1 {
		t.Fatalf("non-string data: %d blocks, want 1", len(blocks))
	}
	if blocks[0].Text != `{"n":1}` {
		t.Errorf("non-string data: text = %q, want JSON encoding", blocks[0].Text)
	}
}

// A resolver that reports a deliverable status but hands back a nil target is
// rejected instead of panicking on the enqueue.
func TestCall_NilTargetWithDeliverableStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"", "running"} {
		res := &fakeResolver{target: nil, status: status, ok: true}
		tl := New(res)
		_, err := callJSON(t, tl, `{"to":"agent-1","message":"ping"}`)
		if err == nil {
			t.Fatalf("status %q with nil target must error", status)
		}
		if !strings.Contains(err.Error(), "not deliverable") {
			t.Errorf("status %q: err = %q, want the not-deliverable rejection", status, err.Error())
		}
	}
}

func TestMessageTool_DescriptionShowsTarget(t *testing.T) {
	tl, _, _ := newTool(t)
	desc, err := tl.Description(json.RawMessage(`{"to":"main","message":"hi"}`))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc != "sent to main" {
		t.Errorf("Description = %q, want %q", desc, "sent to main")
	}
}

func TestMessageTool_RenderResultPlainString(t *testing.T) {
	tl, _, _ := newTool(t)
	if got := tl.RenderResult("Delivered to main"); got != "Delivered to main" {
		t.Errorf("RenderResult = %q, want plain passthrough", got)
	}
}

func TestMessageTool_RenderResultShowsSentContent(t *testing.T) {
	tl, _, _ := newTool(t)
	got := tl.RenderResult(MessageResult{To: "main", Message: "what time is it?"})
	if got != "what time is it?" {
		t.Errorf("RenderResult = %q, want the sent message content", got)
	}
}

func TestMessageTool_DecodeResultUnwrapsWireBlocks(t *testing.T) {
	tl, _, _ := newTool(t)
	dt, ok := tl.Tool.(tool.ToolWithDecodeResult)
	if !ok {
		t.Fatal("MessageTool does not implement ToolWithDecodeResult")
	}
	v, err := dt.DecodeResult([]byte(`[{"type":"text","text":"hello there"}]`))
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	if got := tl.RenderResult(v); got != "hello there" {
		t.Errorf("decode+render = %q, want hello there", got)
	}
}

func TestMessageTool_WireBlocksFromMessageResult(t *testing.T) {
	tl, _, _ := newTool(t)
	blocks := tl.FormatWireBlocks(MessageResult{To: "agent-3", Message: "hello"})
	if len(blocks) != 1 || blocks[0].Text != "hello" {
		t.Errorf("wire blocks = %+v, want single text of the sent message", blocks)
	}
}

func TestMessageTool_DescriptionFallbackOnBadInput(t *testing.T) {
	tl, _, _ := newTool(t)
	for _, in := range []string{`garbage`, `{}`, `{"to":"  "}`} {
		desc, err := tl.Description(json.RawMessage(in))
		if err != nil {
			t.Fatalf("Description(%s): %v", in, err)
		}
		if desc != "Send a text message to a running agent" {
			t.Errorf("Description(%s) = %q, want static fallback", in, desc)
		}
	}
}

func TestMessageTool_RenderResultMarshalFallback(t *testing.T) {
	tl, _, _ := newTool(t)
	if got := tl.RenderResult(42); got != "42" {
		t.Errorf("RenderResult(42) = %q, want %q", got, "42")
	}
}
