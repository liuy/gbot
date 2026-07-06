package webchat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// TestHandle_QueryStart serializes a query_start event through Handle and
// asserts the wire JSON wraps the QueryEvent under an "event" key.
func TestHandle_QueryStart(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventQueryStart})
	msg := readWSMessage(t, ws)

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
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "hello"})
	msg := readWSMessage(t, ws)

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
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{
		Type:     types.EventThinkingEnd,
		Thinking: &types.ThinkingEvent{Duration: 350 * time.Millisecond},
	})
	msg := readWSMessage(t, ws)

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
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "call_1", AgentType: "Explore", Depth: 0},
		ToolUse: &types.ToolUseEvent{ID: "tu_1", Name: "Grep", Input: json.RawMessage(`{}`)},
	})
	msg := readWSMessage(t, ws)

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
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:     types.AskPermission,
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"ls"}`),
			Message:  "Allow Bash?",
		},
	})
	msg := readWSMessage(t, ws)

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

// TestHandle_Error verifies EventError is written to the active WS
// (blocking, must deliver) with the message field set.
func TestHandle_Error(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventError, Error: assertErr("boom")})
	msg := readWSMessage(t, ws)

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

// TestHandle_QueryEnd verifies QueryEnd is written to the active WS so the
// "query done" signal is never dropped behind streaming deltas.
func TestHandle_QueryEnd(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	msg := readWSMessage(t, ws)

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

// TestHandle_QueryEnd_AbortedFlag verifies the server serializes the abort
// signal into the WS query_end payload: when the engine's terminal Error is
// *engine.AbortError, the event JSON must include "aborted":true (the Error
// field itself is `json:"-"` so this is the only channel). For nil error or
// a non-abort error, the key must be absent so the frontend treats it as a
// normal completion.
func TestHandle_QueryEnd_AbortedFlag(t *testing.T) {
	// Build a real context.Canceled so AbortError.Unwrap resolves like in prod.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	abortErr := &engine.AbortError{Phase: "streaming", Err: ctx.Err()}

	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr})
	msg := readWSMessage(t, ws)

	var env struct {
		Event struct {
			Type    string `json:"type"`
			Aborted bool   `json:"aborted"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Type != "query_end" {
		t.Errorf("event.type = %q, want \"query_end\"", env.Event.Type)
	}
	if !env.Event.Aborted {
		t.Error("event.aborted = false, want true (engine emitted AbortError)")
	}

	// Raw JSON must contain the literal "aborted":true — a struct field that
	// silently fails to marshal would still leave env.Event.Aborted==true via
	// a stale default; asserting on the bytes catches that.
	if !strings.Contains(string(msg), `"aborted":true`) {
		t.Errorf("wire payload missing \"aborted\":true; got: %s", string(msg))
	}
}

// TestHandle_QueryEnd_NoAbortedOnNilError verifies that a normal query_end
// (no AbortError) does NOT set the aborted flag, so the frontend proceeds with
// the non-abort completion path.
func TestHandle_QueryEnd_NoAbortedOnNilError(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	msg := readWSMessage(t, ws)

	var env struct {
		Event struct {
			Aborted bool `json:"aborted"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Aborted {
		t.Error("event.aborted = true, want false (nil error → normal completion)")
	}
	// omitempty must drop the key entirely so a normal query_end is
	// byte-identical to the pre-abort-detection wire shape.
	if strings.Contains(string(msg), `"aborted"`) {
		t.Errorf("wire payload should omit aborted key on nil error; got: %s", string(msg))
	}
}

// TestHandle_QueryEnd_NoAbortedOnNonAbortError verifies a non-Abort terminal
// error (e.g. an API failure) does not set aborted — only *engine.AbortError
// counts as a user interrupt.
func TestHandle_QueryEnd_NoAbortedOnNonAbortError(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: assertErr("api 500")})
	msg := readWSMessage(t, ws)

	var env struct {
		Event struct {
			Aborted bool `json:"aborted"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Aborted {
		t.Error("event.aborted = true, want false (non-abort error must not look like an interrupt)")
	}
}

// TestHandle_QueryEnd_AutoRewind verifies that when an abort produces no
// meaningful assistant content (only synthetic interrupt text or nothing),
// the connector rewinds the engine to the last user message. When there IS
// partial content (real text or a tool_use), the rewind must NOT happen.
func TestHandle_QueryEnd_AutoRewind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	abortErr := &engine.AbortError{Phase: "streaming", Err: ctx.Err()}

	t.Run("rewinds_when_no_content", func(t *testing.T) {
		c := newTestConnector(t)
		ws := dialAndStore(t, c)
		mock := c.mock()
		mock.messagesFn = func() []types.Message {
			return []types.Message{
				{
					ID:        "user1",
					Role:      types.RoleUser,
					Timestamp: time.Unix(1000, 0),
					Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}},
				},
				{
					ID:        "asst1",
					Role:      types.RoleAssistant,
					Timestamp: time.Unix(1001, 0),
					Content: []types.ContentBlock{
						{Type: types.ContentTypeText, Text: types.InterruptMessage},
					},
				},
			}
		}

		c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr})
		_ = readWSMessage(t, ws) // drain query_end

		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.rewindCalls) != 1 {
			t.Fatalf("rewindCalls len = %d, want 1", len(mock.rewindCalls))
		}
		if mock.rewindCalls[0] != 0 {
			t.Errorf("rewindCalls[0] = %d, want 0 (last user message index)", mock.rewindCalls[0])
		}
	})

	t.Run("does_not_rewind_when_partial_text", func(t *testing.T) {
		c := newTestConnector(t)
		ws := dialAndStore(t, c)
		mock := c.mock()
		mock.messagesFn = func() []types.Message {
			return []types.Message{
				{
					ID:        "user1",
					Role:      types.RoleUser,
					Timestamp: time.Unix(1000, 0),
					Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}},
				},
				{
					ID:        "asst1",
					Role:      types.RoleAssistant,
					Timestamp: time.Unix(1001, 0),
					Content: []types.ContentBlock{
						{Type: types.ContentTypeText, Text: "partial response"},
						{Type: types.ContentTypeText, Text: types.InterruptMessage},
					},
				},
			}
		}

		c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr})
		_ = readWSMessage(t, ws) // drain query_end

		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.rewindCalls) != 0 {
			t.Errorf("rewindCalls len = %d, want 0 (partial content present)", len(mock.rewindCalls))
		}
	})

	t.Run("does_not_rewind_when_tool_use", func(t *testing.T) {
		c := newTestConnector(t)
		ws := dialAndStore(t, c)
		mock := c.mock()
		mock.messagesFn = func() []types.Message {
			return []types.Message{
				{
					ID:        "user1",
					Role:      types.RoleUser,
					Timestamp: time.Unix(1000, 0),
					Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}},
				},
				{
					ID:        "asst1",
					Role:      types.RoleAssistant,
					Timestamp: time.Unix(1001, 0),
					Content: []types.ContentBlock{
						{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Bash", Input: json.RawMessage(`{}`)},
					},
				},
			}
		}

		c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr})
		_ = readWSMessage(t, ws) // drain query_end

		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.rewindCalls) != 0 {
			t.Errorf("rewindCalls len = %d, want 0 (tool_use present)", len(mock.rewindCalls))
		}
	})

	t.Run("no_rewind_when_no_user_message", func(t *testing.T) {
		c := newTestConnector(t)
		ws := dialAndStore(t, c)
		mock := c.mock()
		mock.messagesFn = func() []types.Message {
			return []types.Message{
				{
					ID:        "asst1",
					Role:      types.RoleAssistant,
					Timestamp: time.Unix(1001, 0),
					Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: types.InterruptMessage}},
				},
			}
		}

		c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr})
		_ = readWSMessage(t, ws) // drain query_end

		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.rewindCalls) != 0 {
			t.Errorf("rewindCalls len = %d, want 0 (no user message to rewind to)", len(mock.rewindCalls))
		}
	})
}

// TestHandle_PendingAskStoredResponseCh verifies the engine's Ask.ResponseCh
// is reachable from the connector so a later ask_response inbound can unblock
// the engine.
func TestHandle_PendingAskStoredResponseCh(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	ch := make(chan types.AskResponse, 1)
	c.Handle(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:       types.AskPermission,
			ToolName:   "Bash",
			ResponseCh: ch,
		},
	})
	_ = readWSMessage(t, ws)
	if id := c.firstPendingAskIDTest(t); id != "1" {
		t.Fatalf("firstPendingAskIDTest = %q, want \"1\"", id)
	}
}

// TestSendAskResponse_UnblocksEngine verifies that when a client responds to
// a permission ask, the engine's ResponseCh receives the decision.
func TestSendAskResponse_UnblocksEngine(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	ch := make(chan types.AskResponse, 1)
	c.Handle(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:       types.AskPermission,
			ToolName:   "Bash",
			ResponseCh: ch,
		},
	})
	_ = readWSMessage(t, ws)

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
	ws := dialAndStore(t, c)
	ch1 := make(chan types.AskResponse, 1)
	ch2 := make(chan types.AskResponse, 1)
	c.Handle(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{Kind: types.AskPermission, ToolName: "Bash", ResponseCh: ch1}})
	c.Handle(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{Kind: types.AskInput, Prompt: "pwd:", ResponseCh: ch2}})
	_ = readWSMessage(t, ws)
	_ = readWSMessage(t, ws)

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
	ws := dialAndStore(t, c)
	// Dispatch through the hub directly: must reach our WS, proving New
	// registered the handler.
	h.Dispatch(types.QueryEvent{Type: types.EventTextStart, Text: "via-hub"})
	msg := readWSMessage(t, ws)
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
	c.mock().messagesFn = func() []types.Message {
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
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage("", 10)
	if payload == nil {
		t.Fatal("buildHistoryMessage returned nil")
	}
	var env struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "history" {
		t.Errorf("type = %q, want \"history\"", env.Type)
	}
	if env.NextCursor != "" {
		t.Errorf("nextCursor = %q, want \"\" (only 1 message)", env.NextCursor)
	}
	if env.HasMore {
		t.Error("hasMore = true, want false (only 1 message)")
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
	c.mock().messagesFn = func() []types.Message {
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
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage("", 10)
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
	c.mock().messagesFn = func() []types.Message {
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
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage("", 10)
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
	c.mock().messagesFn = func() []types.Message {
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
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistoryMessage("", 10)
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

// TestBuildHistoryMessage_Pagination verifies cursor-based pagination:
// first page returns the latest `limit` messages with nextCursor/hasMore;
// subsequent pages return older messages until all are delivered.
func TestBuildHistoryMessage_Pagination(t *testing.T) {
	c := newTestConnector(t)
	// 25 assistant messages
	c.mock().messagesFn = func() []types.Message {
		msgs := make([]types.Message, 25)
		for i := range 25 {
			msgs[i] = types.Message{
				ID:        fmt.Sprintf("msg-%d", i),
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(int64(1000+i), 0),
				Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("content-%d", i))},
			}
		}
		return msgs
	}
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }

	// Page 1: latest 10 (msg-24 … msg-15)
	payload := c.buildHistoryMessage("", 10)
	if payload == nil {
		t.Fatal("page 1 returned nil")
	}
	var p1 struct {
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(payload, &p1); err != nil {
		t.Fatalf("page 1 unmarshal: %v", err)
	}
	if len(p1.Messages) != 10 {
		t.Fatalf("page 1 messages = %d, want 10", len(p1.Messages))
	}
	if p1.Messages[0].Text != "content-15" {
		t.Errorf("page 1 first text = %q, want \"content-15\" (oldest in page)", p1.Messages[0].Text)
	}
	if p1.Messages[9].Text != "content-24" {
		t.Errorf("page 1 last text = %q, want \"content-24\" (newest in page)", p1.Messages[9].Text)
	}
	if !p1.HasMore {
		t.Error("page 1 hasMore = false, want true")
	}
	if p1.NextCursor != "10" {
		t.Errorf("page 1 nextCursor = %q, want \"10\"", p1.NextCursor)
	}

	// Page 2: next 10 (msg-14 … msg-5)
	payload = c.buildHistoryMessage("10", 10)
	if payload == nil {
		t.Fatal("page 2 returned nil")
	}
	var p2 struct {
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(payload, &p2); err != nil {
		t.Fatalf("page 2 unmarshal: %v", err)
	}
	if len(p2.Messages) != 10 {
		t.Fatalf("page 2 messages = %d, want 10", len(p2.Messages))
	}
	if p2.Messages[0].Text != "content-5" {
		t.Errorf("page 2 first text = %q, want \"content-5\"", p2.Messages[0].Text)
	}
	if p2.Messages[9].Text != "content-14" {
		t.Errorf("page 2 last text = %q, want \"content-14\"", p2.Messages[9].Text)
	}
	if !p2.HasMore {
		t.Error("page 2 hasMore = false, want true")
	}
	if p2.NextCursor != "20" {
		t.Errorf("page 2 nextCursor = %q, want \"20\"", p2.NextCursor)
	}

	// Page 3: last 5 (msg-4 … msg-0)
	payload = c.buildHistoryMessage("20", 10)
	if payload == nil {
		t.Fatal("page 3 returned nil")
	}
	var p3 struct {
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(payload, &p3); err != nil {
		t.Fatalf("page 3 unmarshal: %v", err)
	}
	if len(p3.Messages) != 5 {
		t.Fatalf("page 3 messages = %d, want 5", len(p3.Messages))
	}
	if p3.Messages[0].Text != "content-0" {
		t.Errorf("page 3 first text = %q, want \"content-0\"", p3.Messages[0].Text)
	}
	if p3.Messages[4].Text != "content-4" {
		t.Errorf("page 3 last text = %q, want \"content-4\"", p3.Messages[4].Text)
	}
	if p3.HasMore {
		t.Error("page 3 hasMore = true, want false")
	}
	if p3.NextCursor != "" {
		t.Errorf("page 3 nextCursor = %q, want \"\"", p3.NextCursor)
	}

	// Cursor beyond total: empty page
	payload = c.buildHistoryMessage("30", 10)
	if payload == nil {
		t.Fatal("page beyond total returned nil")
	}
	var p4 struct {
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(payload, &p4); err != nil {
		t.Fatalf("page 4 unmarshal: %v", err)
	}
	if len(p4.Messages) != 0 {
		t.Fatalf("page 4 messages = %d, want 0 (empty)", len(p4.Messages))
	}
	if p4.HasMore {
		t.Error("page 4 hasMore = true, want false")
	}
}
