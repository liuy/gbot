package webchat

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// readOne pops a single message from the channel with a timeout, failing the
// test if no message arrives in time. All outbound messages land in msgCh.
func readOne(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel message")
		panic("unreachable")
	}
}

// TestHandle_QueryStart serializes a query_start event through Handle and
// asserts the wire JSON wraps the QueryEvent under an "event" key.
func TestHandle_QueryStart(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventQueryStart})
	msg := readOne(t, c.msgCh)

	var env struct {
		Type  string `json:"type"`
		Event struct {
			Type string `json:"type"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "event" {
		t.Errorf("envelope type = %q, want \"event\"", env.Type)
	}
	if env.Event.Type != "query_start" {
		t.Errorf("event.type = %q, want \"query_start\"", env.Event.Type)
	}
}

// TestHandle_TextDelta verifies text_delta lands in the streaming channel and
// carries the text verbatim.
func TestHandle_TextDelta(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "hello"})
	msg := readOne(t, c.msgCh)

	var env struct {
		Type  string `json:"type"`
		Event struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Text != "hello" {
		t.Errorf("event.text = %q, want \"hello\"", env.Event.Text)
	}
}

// TestHandle_ThinkingEnd_Nanoseconds asserts the engine's time.Duration
// (nanoseconds) marshals as an int64 — the React client divides by 1e9.
func TestHandle_ThinkingEnd_Nanoseconds(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{
		Type:     types.EventThinkingEnd,
		Thinking: &types.ThinkingEvent{Duration: 350 * time.Millisecond},
	})
	msg := readOne(t, c.msgCh)

	var env struct {
		Event struct {
			Thinking struct {
				Duration int64 `json:"duration"`
			} `json:"thinking"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	const wantNs int64 = 350 * 1000 * 1000
	if env.Event.Thinking.Duration != wantNs {
		t.Errorf("duration = %d ns, want %d ns (350ms)", env.Event.Thinking.Duration, wantNs)
	}
}

// TestHandle_AgentSnakeCase verifies the Agent field serializes under
// "agent" with snake_case inner fields (Phase 0 json tag additions).
func TestHandle_AgentSnakeCase(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "call_1", AgentType: "Explore", Depth: 0},
		ToolUse: &types.ToolUseEvent{ID: "tu_1", Name: "Grep", Input: json.RawMessage(`{}`)},
	})
	msg := readOne(t, c.msgCh)

	var env struct {
		Event struct {
			Agent *struct {
				ParentToolUseID string `json:"parent_tool_use_id"`
				AgentType       string `json:"agent_type"`
				Depth           int    `json:"depth"`
			} `json:"agent"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Agent == nil {
		t.Fatal("event.agent is nil, want non-nil")
	}
	if env.Event.Agent.ParentToolUseID != "call_1" {
		t.Errorf("agent.parent_tool_use_id = %q, want \"call_1\"", env.Event.Agent.ParentToolUseID)
	}
	if env.Event.Agent.AgentType != "Explore" {
		t.Errorf("agent.agent_type = %q, want \"Explore\"", env.Event.Agent.AgentType)
	}
}

// TestHandle_AskBuildsOutbound verifies Ask events are rewritten into the
// custom askOutbound struct (NOT marshalling *types.AskEvent directly, which
// has PascalCase fields). The message must carry type="ask", kind, and
// tool_name in snake_case.
func TestHandle_AskBuildsOutbound(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:     types.AskPermission,
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"ls"}`),
			Message:  "Allow Bash?",
		},
	})
	msg := readOne(t, c.msgCh)

	var got struct {
		Type     string          `json:"type"`
		ID       string          `json:"id"`
		Kind     string          `json:"kind"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
		Message  string          `json:"message"`
	}
	if err := json.Unmarshal(msg, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "ask" {
		t.Errorf("type = %q, want \"ask\"", got.Type)
	}
	if got.Kind != "permission" {
		t.Errorf("kind = %q, want \"permission\"", got.Kind)
	}
	if got.ToolName != "Bash" {
		t.Errorf("tool_name = %q, want \"Bash\"", got.ToolName)
	}
	if got.Message != "Allow Bash?" {
		t.Errorf("message = %q, want \"Allow Bash?\"", got.Message)
	}
	if string(got.Input) != `{"command":"ls"}` {
		t.Errorf("input = %s, want {\"command\":\"ls\"}", string(got.Input))
	}
	if got.ID != "1" {
		t.Errorf("id = %q, want \"1\" (first ask, monotonic counter)", got.ID)
	}
}

// TestHandle_Error verifies EventError lands in msgCh
// (blocking, must deliver) with the message field set.
func TestHandle_Error(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventError, Error: assertErr("boom")})
	msg := readOne(t, c.msgCh)

	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want \"error\"", env.Type)
	}
	if !strings.Contains(env.Message, "boom") {
		t.Errorf("message = %q, want it to contain \"boom\"", env.Message)
	}
}

// TestHandle_QueryEnd verifies QueryEnd lands in msgCh so the
// "query done" signal is never dropped behind streaming deltas.
func TestHandle_QueryEnd(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	msg := readOne(t, c.msgCh)

	var env struct {
		Type  string `json:"type"`
		Event struct {
			Type string `json:"type"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "event" {
		t.Errorf("envelope type = %q, want \"event\"", env.Type)
	}
	if env.Event.Type != "query_end" {
		t.Errorf("event.type = %q, want \"query_end\"", env.Event.Type)
	}
}

// TestMsgCh_NeverDrops verifies that msgCh blocks when full and never silently
// drops a message, even under concurrent senders exceeding buffer capacity.
func TestMsgCh_NeverDrops(t *testing.T) {
	c := newTestConnector(t)

	// Start reader BEFORE filling so sends don't deadlock.
	const fillCount = handlerBufSize // 1024 to fill buffer
	const extraCount = 50            // beyond buffer, requires blocking
	const total = fillCount + extraCount
	var mu sync.Mutex
	received := make(map[string]bool)
	readerDone := make(chan struct{})
	go func() {
		for {
			select {
			case msg := <-c.msgCh:
				mu.Lock()
				received[string(msg)] = true
				mu.Unlock()
			case <-readerDone:
				return
			}
		}
	}()

	// Send 1024+50 messages from a single goroutine. The first 1024 fill the
	// buffer; the next 50 block until the reader drains. If sendCritical had
	// drop logic, these 50 would be silently discarded.
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range total {
			c.msgCh <- fmt.Appendf(nil, "msg-%d", i)
		}
	})

	sentDone := make(chan struct{})
	go func() { wg.Wait(); close(sentDone) }()
	select {
	case <-sentDone:
	case <-time.After(10 * time.Second):
		t.Fatal("msgCh blocked >10s — messages stuck or dropped")
	}

	// Wait for reader to drain all messages.
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= total {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-time.After(5 * time.Second):
			t.Fatalf("reader only got %d/%d messages", n, total)
		}
	}
	close(readerDone)

	mu.Lock()
	defer mu.Unlock()
	// Verify the 50 "extra" messages (beyond buffer capacity) were delivered.
	for i := fillCount; i < total; i++ {
		key := fmt.Sprintf("msg-%d", i)
		if !received[key] {
			t.Errorf("message %q was dropped (beyond buffer)", key)
		}
	}
}

// TestHandle_PendingAskStoredResponseCh verifies the engine's Ask.ResponseCh
// is reachable from the connector so a later ask_response inbound can unblock
// the engine.
func TestHandle_PendingAskStoredResponseCh(t *testing.T) {
	c := newTestConnector(t)
	ch := make(chan types.AskResponse, 1)
	c.Handle(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:       types.AskPermission,
			ToolName:   "Bash",
			ResponseCh: ch,
		},
	})
	_ = readOne(t, c.msgCh)
	if id := c.firstPendingAskIDTest(t); id != "1" {
		t.Fatalf("firstPendingAskIDTest = %q, want \"1\"", id)
	}
}

// TestSendAskResponse_UnblocksEngine verifies that when a client responds to
// a permission ask, the engine's ResponseCh receives the decision.
func TestSendAskResponse_UnblocksEngine(t *testing.T) {
	c := newTestConnector(t)
	ch := make(chan types.AskResponse, 1)
	c.Handle(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:       types.AskPermission,
			ToolName:   "Bash",
			ResponseCh: ch,
		},
	})
	_ = readOne(t, c.msgCh)

	c.respondToAskTest(t, "1", types.AskResponse{Decision: types.DecisionAllow})

	select {
	case resp := <-ch:
		if resp.Decision != types.DecisionAllow {
			t.Errorf("decision = %q, want %q", resp.Decision, types.DecisionAllow)
		}
	case <-time.After(time.Second):
		t.Fatal("ResponseCh never received the allow decision")
	}
}

// TestCleanupConn_AbortsPendingAsks mirrors the TUI's deny-on-disconnect
// behavior: when the WS closes with pending asks, all of them must be
// unblocked with Aborted=true so the engine doesn't deadlock.
func TestCleanupConn_AbortsPendingAsks(t *testing.T) {
	c := newTestConnector(t)
	ch1 := make(chan types.AskResponse, 1)
	ch2 := make(chan types.AskResponse, 1)
	c.Handle(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{Kind: types.AskPermission, ToolName: "Bash", ResponseCh: ch1}})
	c.Handle(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{Kind: types.AskInput, Prompt: "pwd:", ResponseCh: ch2}})
	_ = readOne(t, c.msgCh)
	_ = readOne(t, c.msgCh)

	c.cleanupConn()

	for i, ch := range []chan types.AskResponse{ch1, ch2} {
		select {
		case resp := <-ch:
			if !resp.Aborted {
				t.Errorf("pending ask %d: Aborted = false, want true", i)
			}
		case <-time.After(time.Second):
			t.Errorf("pending ask %d: ResponseCh never unblocked on cleanup", i)
		}
	}
}

// TestNew_SubscribesToHub verifies New wires the connector into the hub so
// engine events dispatched via the hub reach Handle.
func TestNew_SubscribesToHub(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	// Dispatch through the hub directly: must reach our msgCh, proving New
	// registered the handler.
	h.Dispatch(types.QueryEvent{Type: types.EventTextStart, Text: "via-hub"})
	msg := readOne(t, c.msgCh)
	var env struct {
		Event struct {
			Text string `json:"text"`
		} `json:"event"`
	}
	_ = json.Unmarshal(msg, &env)
	if env.Event.Text != "via-hub" {
		t.Errorf("hub-routed event.text = %q, want \"via-hub\"", env.Event.Text)
	}
}

// assertErr is a tiny error type used to verify error message formatting on
// the wire.
type assertErr string

func (e assertErr) Error() string { return string(e) }

// TestBuildHistoryMessage_BlocksOrdering verifies buildHistoryMessage emits an
// ordered Blocks array that preserves the original Content[] interleaving of
// text/thinking/tool — NOT the legacy concatenation that groups same-type
// blocks together. This is the authoritative path the frontend uses to assign
// eventIndex values so interleavedItems() renders blocks in true event order.
func TestBuildHistoryMessage_BlocksOrdering(t *testing.T) {
	c := newTestConnector(t)
	c.messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "user1",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1000, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolResult, ToolUseID: "toolX", Content: json.RawMessage(`"[Tool spent 1.5s]result-output"`)},
				},
			},
			{
				ID:        "asst1",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1001, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeText, Text: "A"},
					{Type: types.ContentTypeThinking, Thinking: "T"},
					{Type: types.ContentTypeToolUse, ID: "toolX", Name: "Bash", Input: json.RawMessage(`{}`)},
					{Type: types.ContentTypeText, Text: "B"},
				},
			},
		}
	}
	c.toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage()
	if payload == nil {
		t.Fatal("buildHistoryMessage returned nil")
	}
	var env struct {
		Type     string           `json:"type"`
		Messages []historyChatMsg `json:"messages"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "history" {
		t.Errorf("type = %q, want \"history\"", env.Type)
	}
	// First message is the user tool_result carrier; buildHistoryMessage keeps
	// it in the list. Find the assistant message.
	var asst *historyChatMsg
	for i := range env.Messages {
		if env.Messages[i].Role == "assistant" {
			asst = &env.Messages[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("no assistant message in history payload")
	}
	if len(asst.Blocks) != 4 {
		t.Fatalf("assistant Blocks len = %d, want 4 (text/thinking/tool/text)", len(asst.Blocks))
	}
	wantKinds := []string{"text", "thinking", "tool", "text"}
	for i, want := range wantKinds {
		if asst.Blocks[i].Kind != want {
			t.Errorf("Blocks[%d].Kind = %q, want %q", i, asst.Blocks[i].Kind, want)
		}
	}
	if asst.Blocks[0].Text != "A" {
		t.Errorf("Blocks[0].Text = %q, want \"A\"", asst.Blocks[0].Text)
	}
	if asst.Blocks[3].Text != "B" {
		t.Errorf("Blocks[3].Text = %q, want \"B\" (must NOT be concatenated into one entry)", asst.Blocks[3].Text)
	}
	if asst.Blocks[1].Thinking == nil || asst.Blocks[1].Thinking.Text != "T" {
		got := "<nil>"
		if asst.Blocks[1].Thinking != nil {
			got = asst.Blocks[1].Thinking.Text
		}
		t.Errorf("Blocks[1].Thinking.Text = %q, want \"T\"", got)
	}
	if asst.Blocks[2].Tool == nil {
		t.Fatal("Blocks[2].Tool is nil, want non-nil")
	}
	if asst.Blocks[2].Tool.ID != "toolX" {
		t.Errorf("Blocks[2].Tool.ID = %q, want \"toolX\"", asst.Blocks[2].Tool.ID)
	}
	if asst.Blocks[2].Tool.DisplayOutput == "" {
		t.Error("Blocks[2].Tool.DisplayOutput is empty, want rendered from tool_result")
	}
	const wantNs int64 = int64(1.5 * float64(time.Second))
	if asst.Blocks[2].Tool.DurationNs != wantNs {
		t.Errorf("Blocks[2].Tool.DurationNs = %d, want %d (1.5s)", asst.Blocks[2].Tool.DurationNs, wantNs)
	}

	// Legacy fields must stay populated exactly as before so older consumers
	// keep working (the bug being fixed is ordering, not the legacy fields).
	if asst.Text != "AB" {
		t.Errorf("legacy Text = %q, want \"AB\" (concatenated)", asst.Text)
	}
	if len(asst.Thinking) != 1 || asst.Thinking[0].Text != "T" {
		t.Errorf("legacy Thinking = %+v, want exactly one entry \"T\"", asst.Thinking)
	}
	if len(asst.Tools) != 1 || asst.Tools[0].ID != "toolX" {
		t.Errorf("legacy Tools = %+v, want exactly one entry id \"toolX\"", asst.Tools)
	}
}

// TestBuildHistoryMessage_BlocksSkipsWhitespaceText verifies the whitespace
// filter matches TUI's engineMessagesToViews: whitespace-only text blocks are
// skipped in the ordered Blocks array (so they don't consume an eventIndex
// slot in the frontend) but are still concatenated into the legacy Text field.
func TestBuildHistoryMessage_BlocksSkipsWhitespaceText(t *testing.T) {
	c := newTestConnector(t)
	c.messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "asst2",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1000, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeText, Text: "   "},
					{Type: types.ContentTypeText, Text: "real"},
				},
			},
		}
	}
	c.toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage()
	if payload == nil {
		t.Fatal("buildHistoryMessage returned nil")
	}
	var env struct {
		Messages []historyChatMsg `json:"messages"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(env.Messages))
	}
	hm := env.Messages[0]
	// Only the non-whitespace text block should be in Blocks.
	if len(hm.Blocks) != 1 {
		t.Fatalf("Blocks len = %d, want 1 (whitespace-only skipped)", len(hm.Blocks))
	}
	if hm.Blocks[0].Kind != "text" || hm.Blocks[0].Text != "real" {
		t.Errorf("Blocks[0] = %+v, want kind=\"text\" text=\"real\"", hm.Blocks[0])
	}
	// Legacy Text concatenates ALL text including whitespace (unchanged).
	if hm.Text != "   real" {
		t.Errorf("legacy Text = %q, want \"   real\" (whitespace preserved in legacy field)", hm.Text)
	}
}

// TestBuildHistoryMessage_ToolBlockJSONWireFormat verifies the JSON wire
// format of a tool block from the frontend's perspective. The frontend
// HistoryBlock type must match: a tool block serializes as
// { "kind":"tool", "tool": { "id":..., "name":..., ... } }
// NOT as { "kind":"tool", "id":..., "name":... }.
// If this shape changes, the frontend's b.tool!.name access breaks.
func TestBuildHistoryMessage_ToolBlockJSONWireFormat(t *testing.T) {
	c := newTestConnector(t)
	c.messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "user1",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1000, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolResult, ToolUseID: "toolA", Content: json.RawMessage(`"done"`)},
				},
			},
			{
				ID:        "asst1",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1001, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolUse, ID: "toolA", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
				},
			},
		}
	}
	c.toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage()
	if payload == nil {
		t.Fatal("buildHistoryMessage returned nil")
	}

	// Parse into generic map to inspect the raw JSON shape (frontend perspective).
	var env struct {
		Type     string `json:"type"`
		Messages []struct {
			Role   string                       `json:"role"`
			Blocks []map[string]json.RawMessage `json:"blocks"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Find the assistant message with the tool block.
	var toolBlock map[string]json.RawMessage
	for _, msg := range env.Messages {
		if msg.Role != "assistant" {
			continue
		}
		for _, b := range msg.Blocks {
			var kind string
			_ = json.Unmarshal(b["kind"], &kind)
			if kind == "tool" {
				toolBlock = b
				break
			}
		}
	}
	if toolBlock == nil {
		t.Fatal("no tool block found in history payload")
	}

	// The tool data MUST be nested under "tool" key.
	toolRaw, hasTool := toolBlock["tool"]
	if !hasTool {
		t.Fatal("tool block has no \"tool\" key — frontend expects b.tool.{name,summary,...}")
	}

	var toolData struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Summary       string `json:"summary"`
		DisplayOutput string `json:"displayOutput"`
		IsError       bool   `json:"isError"`
		DurationNs    int64  `json:"durationNs"`
	}
	if err := json.Unmarshal(toolRaw, &toolData); err != nil {
		t.Fatalf("unmarshal nested tool: %v", err)
	}
	if toolData.ID != "toolA" {
		t.Errorf("nested tool.ID = %q, want \"toolA\"", toolData.ID)
	}
	if toolData.Name != "Bash" {
		t.Errorf("nested tool.Name = %q, want \"Bash\"", toolData.Name)
	}
}

// TestBuildHistoryMessage_ThinkingBlockJSONWireFormat verifies the JSON wire
// format of a thinking block from the frontend's perspective.
// { "kind":"thinking", "thinking": { "text":"..." } }
// NOT as { "kind":"thinking", "text":"..." }.
func TestBuildHistoryMessage_ThinkingBlockJSONWireFormat(t *testing.T) {
	c := newTestConnector(t)
	c.messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "asst1",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1001, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeThinking, Thinking: "deep thought"},
				},
			},
		}
	}
	c.toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage()
	if payload == nil {
		t.Fatal("buildHistoryMessage returned nil")
	}

	var env struct {
		Messages []struct {
			Role   string                       `json:"role"`
			Blocks []map[string]json.RawMessage `json:"blocks"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var thinkingBlock map[string]json.RawMessage
	for _, msg := range env.Messages {
		for _, b := range msg.Blocks {
			var kind string
			_ = json.Unmarshal(b["kind"], &kind)
			if kind == "thinking" {
				thinkingBlock = b
				break
			}
		}
	}
	if thinkingBlock == nil {
		t.Fatal("no thinking block found in history payload")
	}

	// The thinking data MUST be nested under "thinking" key.
	thinkingRaw, hasThinking := thinkingBlock["thinking"]
	if !hasThinking {
		t.Fatal("thinking block has no \"thinking\" key — frontend expects b.thinking.text")
	}

	var thinkingData struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(thinkingRaw, &thinkingData); err != nil {
		t.Fatalf("unmarshal nested thinking: %v", err)
	}
	if thinkingData.Text != "deep thought" {
		t.Errorf("nested thinking.Text = %q, want \"deep thought\"", thinkingData.Text)
	}
}
