package wui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/quota"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/task"
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

// TestHandle_QueryEndError_PushesErrorMessage verifies that query_end with
// a non-abort Error pushes a type:"error" message so the frontend can
// display it. API errors (429/5xx) arrive via this path.
func TestHandle_QueryEndError_PushesErrorMessage(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: assertErr("rate limit exceeded")})

	// First message is the query_end event frame.
	msg1 := readWSMessage(t, ws)
	var eventEnv struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(msg1, &eventEnv)
	if eventEnv.Type != "event" {
		t.Fatalf("first message type = %q, want \"event\" (query_end)", eventEnv.Type)
	}

	// Second message should be the error message.
	msg2 := readWSMessage(t, ws)
	var errEnv struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg2, &errEnv); err != nil {
		t.Fatalf("unmarshal error msg: %v", err)
	}
	if errEnv.Type != "error" {
		t.Errorf("error type = %q, want \"error\"", errEnv.Type)
	}
	if !strings.Contains(errEnv.Message, "rate limit exceeded") {
		t.Errorf("error message = %q, want it to contain \"rate limit exceeded\"", errEnv.Message)
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
		ws := dialAndStore(t, c)

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
		ws := dialAndStore(t, c)

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
		ws := dialAndStore(t, c)

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
		ws := dialAndStore(t, c)

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
					{Type: types.ContentTypeToolResult, ToolUseID: "toolX", Content: json.RawMessage(`[{"type":"text","text":"result-output"}]`)},
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

	payload := c.buildHistory(c.activeSlot(), "", 10)
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
	if asst.Blocks[2].Tool.DurationNs != 0 {
		t.Errorf("Blocks[2].Tool.DurationNs = %d, want 0 (no duration prefix)", asst.Blocks[2].Tool.DurationNs)
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

// TestBuildHistoryMessage_DurationReplayed verifies the replay path delivers
// the exact ThinkingDurationNs / ToolDurationNs values that streaming would
// have sent via EventThinkingEnd.Duration / EventToolEnd.Duration. Guards the
// connector.go reads at line ~1043 (cb.ThinkingDurationNs) and line ~1067
// (result.ToolDurationNs) so a future refactor that drops the wiring fails
// this test.
func TestBuildHistoryMessage_DurationReplayed(t *testing.T) {
	c := newTestConnector(t)
	c.mock().messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "user1",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1000, 0),
				Content: []types.ContentBlock{
					{
						Type:           types.ContentTypeToolResult,
						ToolUseID:      "toolX",
						Content:        json.RawMessage(`[{"type":"text","text":"result-output"}]`),
						ToolDurationNs: 6_000_000_000,
					},
				},
			},
			{
				ID:        "asst1",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1001, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeText, Text: "A"},
					{Type: types.ContentTypeThinking, Thinking: "T", ThinkingDurationNs: 1_000_000_000},
					{Type: types.ContentTypeToolUse, ID: "toolX", Name: "Bash", Input: json.RawMessage(`{}`)},
					{Type: types.ContentTypeText, Text: "B"},
				},
			},
		}
	}
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }

	payload := c.buildHistory(c.activeSlot(), "", 10)
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
		t.Fatalf("assistant Blocks len = %d, want 4", len(asst.Blocks))
	}
	if asst.Blocks[1].Thinking == nil {
		t.Fatal("Blocks[1].Thinking is nil, want non-nil")
	}
	if asst.Blocks[1].Thinking.DurationNs != 1_000_000_000 {
		t.Errorf("Blocks[1].Thinking.DurationNs = %d, want 1000000000", asst.Blocks[1].Thinking.DurationNs)
	}
	if asst.Blocks[2].Tool == nil {
		t.Fatal("Blocks[2].Tool is nil, want non-nil")
	}
	if asst.Blocks[2].Tool.DurationNs != 6_000_000_000 {
		t.Errorf("Blocks[2].Tool.DurationNs = %d, want 6000000000", asst.Blocks[2].Tool.DurationNs)
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

	payload := c.buildHistory(c.activeSlot(), "", 10)
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

	payload := c.buildHistory(c.activeSlot(), "", 10)
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

	payload := c.buildHistory(c.activeSlot(), "", 10)
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
	payload := c.buildHistory(c.activeSlot(), "", 10)
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
	if !regexp.MustCompile(`^a:15:0:\d+$`).MatchString(p1.NextCursor) {
		t.Errorf("page 1 nextCursor = %q, want a:15:0:<gen>", p1.NextCursor)
	}

	// Page 2: next 10 (msg-14 … msg-5)
	payload = c.buildHistory(c.activeSlot(), "10", 10)
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
	if !regexp.MustCompile(`^a:5:0:\d+$`).MatchString(p2.NextCursor) {
		t.Errorf("page 2 nextCursor = %q, want a:5:0:<gen>", p2.NextCursor)
	}

	// Page 3: last 5 (msg-4 … msg-0)
	payload = c.buildHistory(c.activeSlot(), "20", 10)
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
	payload = c.buildHistory(c.activeSlot(), "30", 10)
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

// TestBuildHistory_IsBusyExclusion verifies that when the engine is busy
// (streaming), buildHistory excludes the current query — everything from the
// last user text message onward. When idle, all messages are included.
func TestBuildHistory_IsBusyExclusion(t *testing.T) {
	c := newTestConnector(t)
	c.mock().messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "prev_user",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1000, 0),
				Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "previous question"}},
			},
			{
				ID:        "prev_asst",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1001, 0),
				Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "previous answer"}},
			},
			{
				ID:        "cur_user",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1002, 0),
				Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "current question"}},
			},
			{
				ID:        "cur_asst",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1003, 0),
				Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "streaming answer"}},
			},
		}
	}
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }

	// Idle: all 4 messages included
	c.mock().isBusyFn = func() bool { return false }
	payload := c.buildHistory(c.activeSlot(), "", 10)
	var idle struct {
		Messages []historyChatMsg `json:"messages"`
	}
	if err := json.Unmarshal(payload, &idle); err != nil {
		t.Fatalf("idle unmarshal: %v", err)
	}
	if len(idle.Messages) != 4 {
		t.Fatalf("idle messages = %d, want 4", len(idle.Messages))
	}

	// Busy: queryStartMsgIdx=2 means msgs[:3] (prev_user + prev_asst +
	// cur_user). The user query (cur_user) MUST be included — snapshot
	// only carries assistant streaming blocks. cur_asst is excluded
	// (covered by snapshot + live events).
	c.mock().isBusyFn = func() bool { return true }
	c.mock().queryStartMsgIdxFn = func() int { return 2 }
	payload = c.buildHistory(c.activeSlot(), "", 10)
	var busy struct {
		Messages []historyChatMsg `json:"messages"`
	}
	if err := json.Unmarshal(payload, &busy); err != nil {
		t.Fatalf("busy unmarshal: %v", err)
	}
	if len(busy.Messages) != 3 {
		t.Fatalf("busy messages = %d, want 3 (prev_user + prev_asst + cur_user; only cur_asst excluded)", len(busy.Messages))
	}
	if busy.Messages[0].ID != "prev_user" {
		t.Errorf("busy msg[0] = %q, want prev_user", busy.Messages[0].ID)
	}
	if busy.Messages[1].ID != "prev_asst" {
		t.Errorf("busy msg[1] = %q, want prev_asst", busy.Messages[1].ID)
	}
	if busy.Messages[2].ID != "cur_user" {
		t.Errorf("busy msg[2] = %q, want cur_user (user query must be included)", busy.Messages[2].ID)
	}

	// Busy with only 1 user message: history should include that user message
	c.mock().messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "only_user",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1000, 0),
				Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "only question"}},
			},
		}
	}
	payload = c.buildHistory(c.activeSlot(), "", 10)
	if payload == nil {
		t.Fatal("busy with only user msg: expected history with that user msg, got nil")
	}
	var single struct {
		Messages []historyChatMsg `json:"messages"`
	}
	if err := json.Unmarshal(payload, &single); err != nil {
		t.Fatalf("single unmarshal: %v", err)
	}
	if len(single.Messages) != 1 || single.Messages[0].ID != "only_user" {
		t.Fatalf("busy with only user msg = %v, want 1 message (only_user)", single.Messages)
	}
}

// TestHasTextContent verifies the helper used by IsBusy exclusion to find
// the query boundary (last user message with text content).
func TestHasTextContent(t *testing.T) {
	tests := []struct {
		name string
		msg  types.Message
		want bool
	}{
		{"user with text", types.Message{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}, true},
		{"user with empty text", types.Message{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: ""}}}, true},
		{"user with only tool_result", types.Message{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolUseID: "x"}}}, false},
		{"user with no content", types.Message{Role: types.RoleUser, Content: nil}, false},
		{"assistant with text", types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasTextContent(tt.msg); got != tt.want {
				t.Errorf("hasTextContent(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestCurrentUsage_AccumulatesEventUsage(t *testing.T) {
	c := newTestConnector(t)

	// Emit two EventUsage events (simulating two turns in a query).
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 100, OutputTokens: 50}})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 200, OutputTokens: 30, CacheReadInputTokens: 80}})

	qs := &c.slots["main"].queryStats
	inTokens := qs.inputTokens.Load()
	outTokens := qs.outputTokens.Load()
	cacheRead := qs.cacheReadInputTokens.Load()

	if inTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", inTokens)
	}
	if outTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80", outTokens)
	}
	if cacheRead != 80 {
		t.Errorf("CacheReadInputTokens = %d, want 80", cacheRead)
	}
}

func TestCurrentUsage_AccumulatesSubAgentUsage(t *testing.T) {
	c := newTestConnector(t)

	agent := &types.AgentMeta{ParentToolUseID: "tu-1", AgentType: "sub", Depth: 1}
	c.Handle(types.QueryEvent{Type: types.EventUsage, Agent: agent, Usage: &types.UsageEvent{InputTokens: 500}})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 100}})

	inTokens := c.slots["main"].queryStats.inputTokens.Load()

	if inTokens != 600 {
		t.Errorf("InputTokens = %d, want 600 (sub-agent usage should be accumulated)", inTokens)
	}
}

func TestCurrentUsage_ResetsOnQueryEnd(t *testing.T) {
	c := newTestConnector(t)

	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 100}})
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	inTokens := c.slots["main"].queryStats.inputTokens.Load()

	if inTokens != 0 {
		t.Errorf("InputTokens = %d, want 0 after query_end", inTokens)
	}
}

func TestCurrentUsage_AccumulatesToolCountAndThinkingMs(t *testing.T) {
	c := newTestConnector(t)

	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t2", Name: "Read"}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 1500 * time.Millisecond}})

	toolCount := c.slots["main"].queryStats.toolCount.Load()
	thinkingMs := c.slots["main"].queryStats.thinkingMs.Load()

	if toolCount != 2 {
		t.Errorf("toolCount = %d, want 2", toolCount)
	}
	if thinkingMs != 1500 {
		t.Errorf("thinkingMs = %d, want 1500", thinkingMs)
	}
}

func TestCurrentUsage_AccumulatesSubAgentToolAndThinking(t *testing.T) {
	c := newTestConnector(t)

	agent := &types.AgentMeta{ParentToolUseID: "tu-1", AgentType: "sub", Depth: 1}
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: agent, ToolUse: &types.ToolUseEvent{ID: "s1", Name: "Bash"}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Grep"}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Agent: agent, Thinking: &types.ThinkingEvent{Duration: 2000 * time.Millisecond}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 500 * time.Millisecond}})

	toolCount := c.slots["main"].queryStats.toolCount.Load()
	thinkingMs := c.slots["main"].queryStats.thinkingMs.Load()

	if toolCount != 2 {
		t.Errorf("toolCount = %d, want 2 (sub-agent tool should be accumulated)", toolCount)
	}
	if thinkingMs != 2500 {
		t.Errorf("thinkingMs = %d, want 2500 (sub-agent thinking should be accumulated)", thinkingMs)
	}
}

// A background job's token/tool/thinking all bill to the job's own account
// (its completion notification <tokens>), never the parent's progress bar;
// the browser live filter skips them too — takeover must not jump. Sync
// sub-agents count as before.
func TestCurrentUsage_JobEventsExcluded_SyncKept(t *testing.T) {
	c := newTestConnector(t)

	jobMeta := &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-9"}
	syncMeta := &types.AgentMeta{ParentToolUseID: "tu_sync"}
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: jobMeta,
		ToolUse: &types.ToolUseEvent{ID: "j1", Name: "Bash"}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Agent: jobMeta,
		Thinking: &types.ThinkingEvent{Duration: 2 * time.Second}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: syncMeta,
		ToolUse: &types.ToolUseEvent{ID: "s1", Name: "Bash"}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Agent: syncMeta,
		Thinking: &types.ThinkingEvent{Duration: 1 * time.Second}})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Agent: jobMeta,
		Usage: &types.UsageEvent{InputTokens: 7}})

	qs := &c.slots["main"].queryStats
	if got := qs.toolCount.Load(); got != 1 {
		t.Errorf("toolCount = %d, want 1 (job tool excluded, sync sub-agent tool counted)", got)
	}
	if got := qs.thinkingMs.Load(); got != 1000 {
		t.Errorf("thinkingMs = %d, want 1000 (job thinking excluded, sync sub-agent thinking counted)", got)
	}
	if got := qs.inputTokens.Load(); got != 0 {
		t.Errorf("inputTokens = %d, want 0 (job usage bills to the job notification <tokens>)", got)
	}
}

func TestCurrentUsage_BackgroundJobUsageExcluded_SyncKept(t *testing.T) {
	c := newTestConnector(t)

	syncMeta := &types.AgentMeta{ParentToolUseID: "tu_sync"}
	jobMeta := &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-9"}
	c.Handle(types.QueryEvent{Type: types.EventUsage, Agent: syncMeta,
		Usage: &types.UsageEvent{InputTokens: 60}})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Agent: jobMeta,
		Usage: &types.UsageEvent{InputTokens: 500}})

	qs := &c.slots["main"].queryStats
	if got := qs.inputTokens.Load(); got != 60 {
		t.Fatalf("inputTokens = %d, want 60 (sync sub-agent counted, background job excluded)", got)
	}

	c.Handle(types.QueryEvent{Type: types.EventUsage,
		Usage: &types.UsageEvent{InputTokens: 40}})
	if got := qs.inputTokens.Load(); got != 100 {
		t.Fatalf("inputTokens = %d, want 100 (mainline event counted: 60+40)", got)
	}
}

func TestBuildStatsMessage_WithUsage(t *testing.T) {
	c := newTestConnector(t)

	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 100, OutputTokens: 50}})
	payload := c.buildStats(c.activeSlotTest(t))

	var msg struct {
		Type  string `json:"type"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "stats" {
		t.Errorf("type = %q, want \"stats\"", msg.Type)
	}
	if msg.Usage == nil {
		t.Fatal("usage is nil, want non-nil")
	}
	if msg.Usage.InputTokens != 100 {
		t.Errorf("usage.input_tokens = %d, want 100", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 50 {
		t.Errorf("usage.output_tokens = %d, want 50", msg.Usage.OutputTokens)
	}
}

func TestBuildStatsMessage_NoUsage(t *testing.T) {
	c := newTestConnector(t)

	payload := c.buildStats(c.activeSlotTest(t))

	var msg struct {
		Type  string `json:"type"`
		Usage *struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "stats" {
		t.Errorf("type = %q, want \"stats\"", msg.Type)
	}
	if msg.Usage == nil {
		t.Fatal("usage is nil, want zero-value object")
	}
	if msg.Usage.InputTokens != 0 {
		t.Errorf("input_tokens = %d, want 0 when no events received", msg.Usage.InputTokens)
	}
}

func TestBuildStatsMessage_ResetAfterQueryEnd(t *testing.T) {
	c := newTestConnector(t)

	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 100}})
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	payload := c.buildStats(c.activeSlotTest(t))

	var msg struct {
		Usage *struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Usage == nil {
		t.Fatal("usage is nil after query_end")
	}
	if msg.Usage.InputTokens != 0 {
		t.Errorf("input_tokens = %d, want 0 after query_end reset", msg.Usage.InputTokens)
	}
}

func TestTakeover_StatsMessageCarriesAccumulatedUsage(t *testing.T) {
	c := newTestConnector(t)

	ws1 := dialAndStore(t, c)
	t.Cleanup(func() { _ = ws1.Close() })

	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 5000, OutputTokens: 300, CacheReadInputTokens: 200,
	}})

	mux2 := http.NewServeMux()
	RegisterChatWS(mux2, c)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	ws2 := dialChatWS(t, "ws"+strings.TrimPrefix(srv2.URL, "http")+"/ws/chat")

	// The single metadata frame contains the stats.
	meta := readMetadata(t, ws2)
	var stats struct {
		Usage *struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(meta.Stats, &stats); err != nil {
		t.Fatalf("unmarshal stats: %v\nraw: %s", err, meta.Stats)
	}
	if stats.Usage == nil {
		t.Fatalf("usage is nil — stats message did not carry accumulated usage.\nraw: %s", meta.Stats)
	}
	if stats.Usage.InputTokens != 5000 {
		t.Errorf("input_tokens = %d, want 5000", stats.Usage.InputTokens)
	}
	if stats.Usage.OutputTokens != 300 {
		t.Errorf("output_tokens = %d, want 300", stats.Usage.OutputTokens)
	}
	if stats.Usage.CacheReadInputTokens != 200 {
		t.Errorf("cache_read_input_tokens = %d, want 200", stats.Usage.CacheReadInputTokens)
	}
}

func TestTakeover_StatsMessageCarriesQueryStartMs(t *testing.T) {
	c := newTestConnector(t)

	ws1 := dialAndStore(t, c)
	t.Cleanup(func() { _ = ws1.Close() })

	beforeMs := time.Now().UnixMilli() // REAL-TIME
	c.Handle(types.QueryEvent{Type: types.EventQueryStart})

	mux2 := http.NewServeMux()
	RegisterChatWS(mux2, c)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	ws2 := dialChatWS(t, "ws"+strings.TrimPrefix(srv2.URL, "http")+"/ws/chat")

	// The single metadata frame contains the stats.
	meta := readMetadata(t, ws2)
	var stats struct {
		QueryStartMs int64 `json:"queryStartMs"`
	}
	if err := json.Unmarshal(meta.Stats, &stats); err != nil {
		t.Fatalf("unmarshal stats: %v\nraw: %s", err, meta.Stats)
	}
	if stats.QueryStartMs == 0 {
		t.Fatalf("queryStartMs = 0, want non-zero (EventQueryStart should have set it)")
	}
	delta := stats.QueryStartMs - beforeMs
	if delta < -1000 || delta > 1000 {
		t.Errorf("queryStartMs = %d, beforeMs = %d, delta = %dms; want within ±1s", stats.QueryStartMs, beforeMs, delta)
	}
}

// TestEngineSwitch_SendsStatsMessage verifies that after engine switch, the
// metadata frame's stats field carries the target engine's stats. The main
// engine's stats are not transferred — each engine has its own queryStats.
func TestEngineSwitch_SendsStatsMessage(t *testing.T) {
	c := newTestConnector(t)
	_, _ = addMockEngine(t, c, "engineB")

	// Dial and drain initial takeover frames.
	ws := dialAndStore(t, c)
	defer ws.Close()

	// Accumulate stats on the main engine.
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 3000, OutputTokens: 200,
	}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"}})

	// Send engine_switch via WS.
	sendJSON(t, ws, map[string]string{"type": "engine_switch", "engineID": "engineB"})

	// Drain the event frames from usage/tool_start (sent before engine_switch).
	for range 2 {
		_ = readWSMessage(t, ws)
	}

	// The switch sends a single metadata frame.
	meta := readMetadata(t, ws)

	var stats struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		ToolCount int `json:"toolCount"`
	}
	if err := json.Unmarshal(meta.Stats, &stats); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	if stats.Usage == nil {
		t.Fatal("stats.usage is nil")
	}
	// engineB has no accumulated stats — values must be zero.
	if stats.Usage.InputTokens != 0 {
		t.Errorf("stats.usage.input_tokens = %d, want 0 (engineB has no stats)", stats.Usage.InputTokens)
	}
	if stats.ToolCount != 0 {
		t.Errorf("stats.toolCount = %d, want 0 (engineB has no tools)", stats.ToolCount)
	}
}

func TestHandle_QueryEndResetsQueryStartMs(t *testing.T) {
	c := newTestConnector(t)

	c.Handle(types.QueryEvent{Type: types.EventQueryStart})
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	payload := c.buildStats(c.activeSlotTest(t))
	var msg struct {
		QueryStartMs int64 `json:"queryStartMs"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.QueryStartMs != 0 {
		t.Errorf("queryStartMs = %d, want 0 after query_end reset", msg.QueryStartMs)
	}
}

func TestBuildErrorMessage_APIError(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("API Error 429: rate limited")
	data := buildError(err)
	var out struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Type != "error" {
		t.Errorf("type = %q, want 'error'", out.Type)
	}
	if out.Message != "API Error 429: rate limited" {
		t.Errorf("message = %q, want %q", out.Message, "API Error 429: rate limited")
	}
}

// TestBuildHistory_DBBoundaryNotSkipped verifies that a compact boundary
// loaded from DB (Role=RoleSystem, FlagCompactSummary set via metadata,
// content contains "compact_boundary") is NOT skipped by the RoleSystem
// filter — it must be emitted as a divider message.
func TestBuildHistory_DBBoundaryNotSkipped(t *testing.T) {
	c := newTestConnector(t)
	c.mock().messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "boundary-1",
				Role:      types.RoleSystem,
				Flags:     types.FlagCompactSummary,
				Timestamp: time.Unix(1000, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeText, Text: `{"type":"system","subtype":"compact_boundary","content":"Conversation compacted"}`},
				},
			},
			{
				ID:        "user-1",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1001, 0),
				Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "post-compact message"}},
			},
		}
	}
	c.mock().toolsFn = func() map[string]tool.Tool { return nil }
	c.mock().isBusyFn = func() bool { return false }

	payload := c.buildHistory(c.activeSlot(), "", 10)
	if payload == nil {
		t.Fatal("buildHistory returned nil")
	}
	var result struct {
		Messages []historyChatMsg `json:"messages"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(result.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (boundary divider + user msg)", len(result.Messages))
	}
	if !result.Messages[0].CompactBoundary {
		t.Errorf("messages[0] should be compact boundary divider, got %+v", result.Messages[0])
	}
	if result.Messages[0].ID != "boundary-1" {
		t.Errorf("messages[0].ID = %q, want boundary-1", result.Messages[0].ID)
	}
	if result.Messages[1].ID != "user-1" {
		t.Errorf("messages[1].ID = %q, want user-1", result.Messages[1].ID)
	}
}

func TestBuildErrorMessage_GenericError(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("invalid config: missing provider")
	data := buildError(err)
	var out struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Message != "invalid config: missing provider" {
		t.Errorf("message = %q, want %q", out.Message, "invalid config: missing provider")
	}
}

// ---- shared helpers ----

// a1LogSink records slog messages. Several mutants under test (partial
// restore warn, autoRewind failure warn, per-event debug trace) have no
// observable effect besides the log line, so the tests must read the log.
type a1LogSink struct {
	mu   sync.Mutex
	msgs []string
}

func (s *a1LogSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *a1LogSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, r.Message)
	s.mu.Unlock()
	return nil
}

func (s *a1LogSink) WithAttrs([]slog.Attr) slog.Handler { return s }

func (s *a1LogSink) WithGroup(string) slog.Handler { return s }

func (s *a1LogSink) count(msg string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

// a1SwapDefaultLogger swaps slog's default logger for the test's duration.
// Safe against the package's t.Parallel tests: sequential tests never
// interleave with parallel ones in Go's test runner.
func a1SwapDefaultLogger(t *testing.T) *a1LogSink {
	t.Helper()
	sink := &a1LogSink{}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(old) })
	return sink
}

// a1BlockProvider parks the engine's query inside provider.Stream until ctx
// is canceled, giving engineAdapter tests a deterministic busy window that a
// plain provider-less engine cannot provide (it errors out instantly).
type a1BlockProvider struct {
	onStream func()
}

func (p *a1BlockProvider) Name() string { return "a1block" }

func (p *a1BlockProvider) Complete(context.Context, *llm.Request) (*llm.Response, error) {
	return nil, errors.New("a1: Complete unsupported")
}

func (p *a1BlockProvider) Stream(ctx context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.onStream()
	ch := make(chan llm.StreamEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func a1UserMsg(text string) types.Message {
	return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(text)}}
}

func a1AssistantMsg(text string) types.Message {
	return types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(text)}}
}

// ---- resetQueryStats ----

func TestMutA1_ResetQueryStatsZeroesAllFields(t *testing.T) {
	qs := &queryStats{}
	qs.inputTokens.Store(7)
	qs.outputTokens.Store(8)
	qs.cacheReadInputTokens.Store(9)
	qs.cacheCreationInputTokens.Store(10)
	qs.toolCount.Store(11)
	qs.thinkingMs.Store(12)
	qs.startMs.Store(13)
	resetQueryStats(qs)
	want := int64(0)
	for name, got := range map[string]int64{
		"inputTokens":              qs.inputTokens.Load(),
		"outputTokens":             qs.outputTokens.Load(),
		"cacheReadInputTokens":     qs.cacheReadInputTokens.Load(),
		"cacheCreationInputTokens": qs.cacheCreationInputTokens.Load(),
		"toolCount":                qs.toolCount.Load(),
		"thinkingMs":               qs.thinkingMs.Load(),
		"startMs":                  qs.startMs.Load(),
	} {
		if got != want {
			t.Errorf("resetQueryStats: %s = %d, want %d", name, got, want)
		}
	}
}

// ---- accumulateStats ----

func TestMutA1_AccumulateStatsUsage(t *testing.T) {
	qs := &queryStats{}
	accumulateStats(qs, types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 40,
	}})
	if got := qs.inputTokens.Load(); got != 10 {
		t.Errorf("inputTokens = %d, want 10", got)
	}
	if got := qs.outputTokens.Load(); got != 20 {
		t.Errorf("outputTokens = %d, want 20", got)
	}
	if got := qs.cacheReadInputTokens.Load(); got != 30 {
		t.Errorf("cacheReadInputTokens = %d, want 30", got)
	}
	if got := qs.cacheCreationInputTokens.Load(); got != 40 {
		t.Errorf("cacheCreationInputTokens = %d, want 40", got)
	}
	if got := qs.toolCount.Load(); got != 0 {
		t.Errorf("toolCount = %d, want 0", got)
	}

	// Usage event with nil Usage must neither panic nor accumulate.
	qsNil := &queryStats{}
	accumulateStats(qsNil, types.QueryEvent{Type: types.EventUsage})
	if got := qsNil.inputTokens.Load(); got != 0 {
		t.Errorf("usage-with-nil: inputTokens = %d, want 0", got)
	}

	// Non-usage event carrying Usage must not accumulate.
	qsOther := &queryStats{}
	accumulateStats(qsOther, types.QueryEvent{Type: types.EventTextDelta, Text: "x", Usage: &types.UsageEvent{InputTokens: 1}})
	if got := qsOther.inputTokens.Load(); got != 0 {
		t.Errorf("text_delta+usage: inputTokens = %d, want 0", got)
	}
}

func TestMutA1_AccumulateStatsQueryStart(t *testing.T) {
	// In a synctest bubble the clock is frozen while the goroutine runs, so
	// the stored startMs is comparable for equality against a captured
	// reading instead of a wall-clock bound.
	inBubble(t, func(t *testing.T) {
		// Sub-agent query_start must not set startMs.
		qsAgent := &queryStats{}
		accumulateStats(qsAgent, types.QueryEvent{Type: types.EventQueryStart, Agent: &types.AgentMeta{ParentToolUseID: "p"}})
		if got, want := qsAgent.startMs.Load(), int64(0); got != want {
			t.Errorf("agent query_start: startMs = %d, want %d", got, want)
		}

		before := time.Now().UnixMilli()
		qsTop := &queryStats{}
		accumulateStats(qsTop, types.QueryEvent{Type: types.EventQueryStart})
		if got := qsTop.startMs.Load(); got != before {
			t.Errorf("top-level query_start: startMs = %d, want the captured reading %d", got, before)
		}

		// Non-querystart top-level event must not set startMs.
		qsOther := &queryStats{}
		accumulateStats(qsOther, types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
		if got, want := qsOther.startMs.Load(), int64(0); got != want {
			t.Errorf("text_delta: startMs = %d, want %d", got, want)
		}
	})
}

func TestMutA1_AccumulateStatsToolStart(t *testing.T) {
	// ToolStart with nil ToolUse must neither panic nor count.
	qsNil := &queryStats{}
	accumulateStats(qsNil, types.QueryEvent{Type: types.EventToolStart})
	if got := qsNil.toolCount.Load(); got != 0 {
		t.Errorf("tool_start nil ToolUse: toolCount = %d, want 0", got)
	}

	qs := &queryStats{}
	accumulateStats(qs, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t"}})
	if got := qs.toolCount.Load(); got != 1 {
		t.Errorf("tool_start: toolCount = %d, want 1", got)
	}

	// Non-toolstart event carrying ToolUse must not count.
	qsOther := &queryStats{}
	accumulateStats(qsOther, types.QueryEvent{Type: types.EventQueryStart, ToolUse: &types.ToolUseEvent{ID: "t"}})
	if got := qsOther.toolCount.Load(); got != 0 {
		t.Errorf("query_start+ToolUse: toolCount = %d, want 0", got)
	}
}

func TestMutA1_AccumulateStatsThinkingEnd(t *testing.T) {
	// ThinkingEnd with nil Thinking must neither panic nor accumulate.
	qsNil := &queryStats{}
	accumulateStats(qsNil, types.QueryEvent{Type: types.EventThinkingEnd})
	if got := qsNil.thinkingMs.Load(); got != 0 {
		t.Errorf("thinking_end nil Thinking: thinkingMs = %d, want 0", got)
	}

	qs := &queryStats{}
	accumulateStats(qs, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 1500 * time.Millisecond}})
	if got := qs.thinkingMs.Load(); got != 1500 {
		t.Errorf("thinking_end: thinkingMs = %d, want 1500", got)
	}

	// Non-thinkingend event carrying Thinking must not accumulate.
	qsOther := &queryStats{}
	accumulateStats(qsOther, types.QueryEvent{Type: types.EventThinkingStart, Thinking: &types.ThinkingEvent{Duration: time.Second}})
	if got := qsOther.thinkingMs.Load(); got != 0 {
		t.Errorf("thinking_start+Thinking: thinkingMs = %d, want 0", got)
	}
}

// ---- findBlock / targetList ----

func TestMutA1_FindBlock(t *testing.T) {
	blocks := []streamBlock{
		{Kind: "text", ID: "txt", Text: "hello"},
		{Kind: "tool", ID: "outer", Name: "Bash", Children: []streamBlock{
			{Kind: "tool", ID: "nested", Name: "Read"},
		}},
	}
	// Only tool blocks are addressable: a matching ID on a text block is nil.
	if b := findBlock(blocks, "txt"); b != nil {
		t.Errorf("findBlock(text block) = %+v, want nil", b)
	}
	if b := findBlock(blocks, "outer"); b == nil || b.ID != "outer" {
		t.Errorf("findBlock(outer) = %+v, want outer tool block", b)
	}
	// Depth-first descent must reach nested children.
	if b := findBlock(blocks, "nested"); b == nil || b.ID != "nested" {
		t.Errorf("findBlock(nested) = %+v, want nested tool block", b)
	}
	if b := findBlock(blocks, "absent"); b != nil {
		t.Errorf("findBlock(absent) = %+v, want nil", b)
	}
	// Empty-children tool block still finds itself.
	solo := []streamBlock{{Kind: "tool", ID: "solo", Children: []streamBlock{}}}
	if b := findBlock(solo, "solo"); b == nil || b.ID != "solo" {
		t.Errorf("findBlock(solo) = %+v, want solo tool block", b)
	}
}

func TestMutA1_TargetList(t *testing.T) {
	ss := &streamState{blocks: []streamBlock{{Kind: "tool", ID: "p1", Name: "Agent"}}}
	if got := targetList(ss, types.QueryEvent{Type: types.EventTextDelta}); got != &ss.blocks {
		t.Errorf("targetList(top-level) = %p, want root blocks %p", got, &ss.blocks)
	}
	// Sub-agent event without a parent tool ID belongs to the root list.
	agentNoParent := types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{AgentType: "Explore"}}
	if got := targetList(ss, agentNoParent); got != &ss.blocks {
		t.Errorf("targetList(agent no parent) = %p, want root blocks %p", got, &ss.blocks)
	}
	parent := findBlock(ss.blocks, "p1")
	agentWithParent := types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "p1"}}
	if got := targetList(ss, agentWithParent); got != &parent.Children {
		t.Errorf("targetList(agent with parent) = %p, want parent children %p", got, &parent.Children)
	}
	agentOrphan := types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "zz"}}
	if got := targetList(ss, agentOrphan); got != nil {
		t.Errorf("targetList(agent orphan parent) = %p, want nil (event is dropped)", got)
	}
}

// ---- updateStreamState ----

func TestMutA1_UpdateStreamStateToolStartFields(t *testing.T) {
	// Bubble-frozen clock: StartedAt must equal the captured reading exactly.
	inBubble(t, func(t *testing.T) {
		before := time.Now().UnixMilli()
		ss := &streamState{}
		updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
			ID: "tu1", Name: "Bash", Summary: "bash summary",
			IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true,
		}})
		if len(ss.blocks) != 1 {
			t.Fatalf("blocks = %d, want 1", len(ss.blocks))
		}
		b := ss.blocks[0]
		if b.Kind != "tool" || b.ID != "tu1" || b.Name != "Bash" {
			t.Errorf("identity fields = %+v", b)
		}
		if b.Summary != "bash summary" {
			t.Errorf("Summary = %q, want %q", b.Summary, "bash summary")
		}
		if !b.IsSearch || !b.IsRead || !b.IsList || !b.IsLsp || !b.IsTask {
			t.Errorf("classification flags = search:%v read:%v list:%v lsp:%v task:%v, want all true",
				b.IsSearch, b.IsRead, b.IsList, b.IsLsp, b.IsTask)
		}
		if b.IsWeb {
			t.Errorf("IsWeb = true for Bash tool, want false")
		}
		if b.State != "running" {
			t.Errorf("State = %q, want running", b.State)
		}
		if b.StartedAt != before {
			t.Errorf("StartedAt = %d, want the captured reading %d", b.StartedAt, before)
		}
		// Children must be an empty non-nil slice: the wire shape renders
		// `children: []`, and field-clearing it to nil emits `null`.
		if b.Children == nil || len(b.Children) != 0 {
			t.Errorf("Children = %#v, want empty non-nil slice", b.Children)
		}
	})
}

func TestMutA1_UpdateStreamStateTurnStart(t *testing.T) {
	// Top-level turn_start must not panic on the nil-Agent rename guard.
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventTurnStart})
	if !ss.queryActive {
		t.Errorf("queryActive = false after top-level turn_start, want true")
	}

	// fork agents keep the plain parent name.
	ssFork := &streamState{blocks: []streamBlock{{Kind: "tool", ID: "p1", Name: "Agent"}}}
	updateStreamState(ssFork, types.QueryEvent{Type: types.EventTurnStart, Agent: &types.AgentMeta{ParentToolUseID: "p1", AgentType: "fork"}})
	if got := ssFork.blocks[0].Name; got != "Agent" {
		t.Errorf("fork turn_start: parent Name = %q, want %q", got, "Agent")
	}

	// Real agent types annotate the parent tool name.
	ssPlan := &streamState{blocks: []streamBlock{{Kind: "tool", ID: "p1", Name: "Agent"}}}
	updateStreamState(ssPlan, types.QueryEvent{Type: types.EventTurnStart, Agent: &types.AgentMeta{ParentToolUseID: "p1", AgentType: "Planner"}})
	if got := ssPlan.blocks[0].Name; got != "Agent Planner" {
		t.Errorf("planner turn_start: parent Name = %q, want %q", got, "Agent Planner")
	}
}

func TestMutA1_UpdateStreamStateToolParamDelta(t *testing.T) {
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Bash"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{
		ID: "tu1", Summary: "s1", IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true,
	}})
	b := findBlock(ss.blocks, "tu1")
	if b == nil {
		t.Fatal("tool block disappeared")
	}
	if b.Summary != "s1" {
		t.Errorf("Summary = %q, want s1", b.Summary)
	}
	if !b.IsSearch || !b.IsRead || !b.IsList || !b.IsLsp || !b.IsTask {
		t.Errorf("flags = search:%v read:%v list:%v lsp:%v task:%v, want all true",
			b.IsSearch, b.IsRead, b.IsList, b.IsLsp, b.IsTask)
	}
	// Param delta for an unknown block is dropped, not panicked on.
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{ID: "absent"}})
	if len(ss.blocks) != 1 {
		t.Errorf("blocks = %d, want 1", len(ss.blocks))
	}
}

func TestMutA1_UpdateStreamStateAttachmentEcho(t *testing.T) {
	mkSS := func() *streamState {
		ss := &streamState{}
		updateStreamState(ss, types.QueryEvent{Type: types.EventTurnStart})
		return ss
	}
	mkMsg := func(att *types.Attachment, texts ...string) *types.Message {
		content := make([]types.ContentBlock, 0, len(texts))
		for _, s := range texts {
			content = append(content, types.NewTextBlock(s))
		}
		return &types.Message{Role: types.RoleUser, Content: content, Attachment: att, Timestamp: time.Unix(5000, 0)}
	}

	// Prompt-mode attachment echoes the prompt text.
	ss := mkSS()
	updateStreamState(ss, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(&types.Attachment{Mode: types.ItemModePrompt, Prompt: "PROMPT"}, "ignored")})
	if len(ss.blocks) != 1 || ss.blocks[0].Kind != "user" || ss.blocks[0].Text != "PROMPT" {
		t.Fatalf("prompt echo block = %+v, want user block with text PROMPT", ss.blocks)
	}
	if got := ss.blocks[0].StartedAt; got != 5000000 {
		t.Errorf("StartedAt = %d, want 5000000", got)
	}

	// Queue-mode attachments echo content, never the prompt.
	ssQ := mkSS()
	updateStreamState(ssQ, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(&types.Attachment{Mode: types.ItemModeMessage, Prompt: "PROMPT"}, "CONTENT")})
	if len(ssQ.blocks) != 1 || ssQ.blocks[0].Text != "CONTENT" {
		t.Fatalf("queue echo block = %+v, want user block with text CONTENT", ssQ.blocks)
	}

	// Prompt-mode with empty prompt falls back to content blocks.
	ssF := mkSS()
	updateStreamState(ssF, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(&types.Attachment{Mode: types.ItemModePrompt, Prompt: ""}, "FALLBACK")})
	if len(ssF.blocks) != 1 || ssF.blocks[0].Text != "FALLBACK" {
		t.Fatalf("fallback echo block = %+v, want user block with text FALLBACK", ssF.blocks)
	}

	// No attachment: multiple text blocks concatenate in order.
	ssC := mkSS()
	updateStreamState(ssC, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(nil, "A", "B")})
	if len(ssC.blocks) != 1 || ssC.blocks[0].Text != "AB" {
		t.Fatalf("concat echo block = %+v, want user block with text AB", ssC.blocks)
	}
}

func TestMutA1_UpdateStreamStateToolEnd(t *testing.T) {
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Bash"}})
	// Sub-agent activity to verify the completion clear.
	ss.blocks[0].Children = []streamBlock{{Kind: "text", Text: "child work"}}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{
		ToolUseID: "tu1", DisplayOutput: "done-out",
		IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true,
		Duration: 5 * time.Millisecond,
	}})
	b := findBlock(ss.blocks, "tu1")
	if b == nil {
		t.Fatal("tool block disappeared")
	}
	if b.State != "done" {
		t.Errorf("State = %q, want done", b.State)
	}
	if b.DisplayOutput != "done-out" {
		t.Errorf("DisplayOutput = %q, want done-out", b.DisplayOutput)
	}
	if !b.IsSearch || !b.IsRead || !b.IsList || !b.IsLsp || !b.IsTask {
		t.Errorf("flags = search:%v read:%v list:%v lsp:%v task:%v, want all true",
			b.IsSearch, b.IsRead, b.IsList, b.IsLsp, b.IsTask)
	}
	if b.TimingNs != 5000000 {
		t.Errorf("TimingNs = %d, want 5000000", b.TimingNs)
	}
	if b.Children == nil || len(b.Children) != 0 {
		t.Errorf("Children = %#v, want cleared empty non-nil slice", b.Children)
	}

	// Error results mark the block errored.
	ssErr := &streamState{}
	updateStreamState(ssErr, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu2", Name: "Bash"}})
	updateStreamState(ssErr, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "tu2", IsError: true}})
	if got := findBlock(ssErr.blocks, "tu2").State; got != "error" {
		t.Errorf("error result State = %q, want error", got)
	}

	// Unknown tool ID and nil ToolResult are dropped without panic.
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "absent"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolEnd})
}

func TestMutA1_UpdateStreamStateToolOutputDelta(t *testing.T) {
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Bash"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: "partial"}})
	if got := findBlock(ss.blocks, "tu1").DisplayOutput; got != "partial" {
		t.Errorf("DisplayOutput = %q, want partial", got)
	}
	// Unknown ID must not panic on the nil block guard.
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "absent", DisplayOutput: "x"}})
}

func TestMutA1_UpdateStreamStateThinkingDelta(t *testing.T) {
	// Sub-agent delta whose parent tool is gone: nil target list is dropped.
	ssOrphan := &streamState{}
	updateStreamState(ssOrphan, types.QueryEvent{
		Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "T"},
		Agent: &types.AgentMeta{ParentToolUseID: "zz"},
	})
	if len(ssOrphan.blocks) != 0 {
		t.Errorf("orphan thinking delta appended %d blocks, want 0", len(ssOrphan.blocks))
	}

	// Empty block list: the last-block probe must not index [-1].
	ssEmpty := &streamState{}
	updateStreamState(ssEmpty, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "T"}})
	if len(ssEmpty.blocks) != 0 {
		t.Errorf("delta on empty state appended %d blocks, want 0", len(ssEmpty.blocks))
	}

	// Deltas accumulate onto the active thinking block.
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingStart})
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "abc"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "def"}})
	if len(ss.blocks) != 1 || ss.blocks[0].Text != "abcdef" {
		t.Fatalf("thinking block = %+v, want single block with text abcdef", ss.blocks)
	}

	// Delta after a non-thinking last block is dropped entirely.
	ssTool := &streamState{}
	updateStreamState(ssTool, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t", Name: "Bash"}})
	updateStreamState(ssTool, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "xyz"}})
	if len(ssTool.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (no new block for delta after tool)", len(ssTool.blocks))
	}
	if got := ssTool.blocks[0].Text; got != "" {
		t.Errorf("tool block Text polluted = %q, want empty", got)
	}
}

func TestMutA1_UpdateStreamStateThinkingEnd(t *testing.T) {
	// Empty list: no panic, no append.
	ssEmpty := &streamState{}
	updateStreamState(ssEmpty, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: time.Second}})
	if len(ssEmpty.blocks) != 0 {
		t.Errorf("thinking end on empty state appended %d blocks, want 0", len(ssEmpty.blocks))
	}

	// Active thinking block is finalized.
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingStart})
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 2500 * time.Millisecond}})
	if len(ss.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(ss.blocks))
	}
	if ss.blocks[0].Active {
		t.Errorf("Active = true after thinking end, want false")
	}
	if got := ss.blocks[0].DurationNs; got != 2500000000 {
		t.Errorf("DurationNs = %d, want 2500000000", got)
	}

	// Thinking end after a text block leaves it untouched.
	ssText := &streamState{}
	updateStreamState(ssText, types.QueryEvent{Type: types.EventTextDelta, Text: "keep"})
	updateStreamState(ssText, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: time.Second}})
	if len(ssText.blocks) != 1 || ssText.blocks[0].Kind != "text" || ssText.blocks[0].Text != "keep" {
		t.Fatalf("text block after thinking end = %+v, want untouched text block", ssText.blocks)
	}
	if got := ssText.blocks[0].DurationNs; got != 0 {
		t.Errorf("text block DurationNs = %d, want 0", got)
	}

	// Nil Thinking is dropped without panic.
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingEnd})
}

// ---- onEngineEvent ----

func TestMutA1_OnEngineEventQueryEndReset(t *testing.T) {
	c := newTestConnector(t)
	slot := c.activeSlotTest(t)

	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 10}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "task1", Name: "Task"}})

	slot.ssMu.Lock()
	blocks := len(slot.streams[""].blocks)
	slot.ssMu.Unlock()
	if blocks != 2 {
		t.Fatalf("setup: blocks = %d, want 2 (text + tool)", blocks)
	}
	if got := slot.queryStats.inputTokens.Load(); got != 10 {
		t.Fatalf("setup: inputTokens = %d, want 10", got)
	}
	if !slot.taskToolIDs["task1"] {
		t.Fatalf("setup: task1 not tracked")
	}

	// Sub-agent query_end must NOT reset per-engine state.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Agent: &types.AgentMeta{ParentToolUseID: "p"}})
	slot.ssMu.Lock()
	blocks = len(slot.streams[""].blocks)
	slot.ssMu.Unlock()
	if blocks != 2 {
		t.Errorf("sub-agent query_end reset blocks to %d, want 2 preserved", blocks)
	}
	if got := slot.queryStats.inputTokens.Load(); got != 10 {
		t.Errorf("sub-agent query_end reset inputTokens to %d, want 10", got)
	}
	if !slot.taskToolIDs["task1"] {
		t.Errorf("sub-agent query_end cleared taskToolIDs, want preserved")
	}

	// Top-level query_end resets stream state, stats, and task tracking.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	slot.ssMu.Lock()
	blocks = len(slot.streams[""].blocks)
	tasks := len(slot.taskToolIDs)
	slot.ssMu.Unlock()
	if blocks != 0 {
		t.Errorf("query_end left %d blocks, want 0", blocks)
	}
	if got := slot.queryStats.inputTokens.Load(); got != 0 {
		t.Errorf("query_end left inputTokens = %d, want 0", got)
	}
	if tasks != 0 {
		t.Errorf("query_end left %d taskToolIDs, want 0", tasks)
	}
}

func TestMutA1_OnEngineEventFrameNoAbortFlag(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	// A rewound-able message so a wrongly-triggered auto-rewind would leave
	// observable traces (rewind call + rewound flag) on the frame.
	mock.SetMessagesFn(func() []types.Message { return []types.Message{a1UserMsg("q")} })
	ws := dialAndStore(t, c)

	// A non-query_end event carrying an AbortError must not mark the frame
	// aborted nor trigger the auto-rewind path.
	c.Handle(types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "x",
		Error: &engine.AbortError{Phase: "streaming", Err: context.Canceled},
	})

	data := readWSMessage(t, ws)
	var frame struct {
		Type  string         `json:"type"`
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if frame.Type != "event" {
		t.Fatalf("frame type = %q, want event", frame.Type)
	}
	if _, ok := frame.Event["aborted"]; ok {
		t.Errorf("frame carries aborted on plain event: %v", frame.Event["aborted"])
	}
	if _, ok := frame.Event["rewound"]; ok {
		t.Errorf("frame carries rewound on plain event: %v", frame.Event["rewound"])
	}
	if got := frame.Event["text"]; got != "x" {
		t.Errorf("frame event text = %v, want x", got)
	}
	mock.mu.RLock()
	rewinds := len(mock.rewindCalls)
	mock.mu.RUnlock()
	if rewinds != 0 {
		t.Errorf("rewindCalls = %d, want 0 — only query_end aborts may rewind", rewinds)
	}
}

func TestMutA1_OnEngineEventTaskTrackingAndDebugTrace(t *testing.T) {
	sink := a1SwapDefaultLogger(t)
	c := newTestConnector(t)
	slot := c.activeSlotTest(t)

	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "task1", Name: "Task"}})
	if !slot.taskToolIDs["task1"] {
		t.Errorf("Task tool_start not tracked")
	}

	// ToolStart with nil ToolUse must not panic in the tracker.
	c.Handle(types.QueryEvent{Type: types.EventToolStart})

	// A non-toolstart event carrying ToolUse must not be recorded as Task.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "y", ToolUse: &types.ToolUseEvent{ID: "task2", Name: "Task"}})
	// A non-Task tool start must not be tracked either.
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "bash1", Name: "Bash"}})
	if len(slot.taskToolIDs) != 1 {
		t.Errorf("taskToolIDs = %v, want only task1", slot.taskToolIDs)
	}

	if sink.count("wui:event") == 0 {
		t.Errorf("per-event debug trace 'wui:event' missing from log")
	}
}

// ---- shouldAutoRewindFor / autoRewindOnAbortFor ----

func TestMutA1_ShouldAutoRewindFor(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()

	cases := []struct {
		name    string
		pending []types.QueuedItem
		msgs    []types.Message
		want    bool
	}{
		{"queued non-meta prompt blocks rewind", []types.QueuedItem{{Mode: types.ItemModePrompt}}, []types.Message{a1UserMsg("q")}, false},
		{"queued message-mode item does not block", []types.QueuedItem{{Mode: types.ItemModeMessage}}, nil, true},
		{"meta prompt does not block", []types.QueuedItem{{Mode: types.ItemModePrompt, IsMeta: true}}, nil, true},
		{"no pending, no messages", nil, nil, true},
		// No selectable user message exists (assistant-only transcript):
		// the lastUserIdx < 0 branch itself must answer true, not fall
		// through to the synthetic scan over the whole transcript.
		{"assistant-only transcript", nil, []types.Message{a1AssistantMsg("hi")}, true},
		{"assistant answer after prompt", nil, []types.Message{a1UserMsg("q"), a1AssistantMsg("a")}, false},
		{"bare prompt", nil, []types.Message{a1UserMsg("q")}, true},
	}
	for _, tc := range cases {
		pending := tc.pending
		mock.pendingAttachmentsFn = func() []types.QueuedItem { return pending }
		msgs := tc.msgs
		mock.SetMessagesFn(func() []types.Message { return msgs })
		if got := c.shouldAutoRewindFor(mock); got != tc.want {
			t.Errorf("%s: shouldAutoRewindFor = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMutA1_AutoRewindOnAbortFor(t *testing.T) {
	sink := a1SwapDefaultLogger(t)
	c := newTestConnector(t)
	mock := c.mock()
	mock.SetMessagesFn(func() []types.Message { return []types.Message{a1UserMsg("q")} })

	// Rewind failure is reported as (false, "") with a warn trace.
	mock.rewindToFn = func(int) error { return errors.New("boom") }
	if rewound, text := c.autoRewindOnAbortFor(mock); rewound || text != "" {
		t.Errorf("failed rewind = (%v, %q), want (false, \"\")", rewound, text)
	}
	if got := sink.count("wui: autoRewind failed"); got != 1 {
		t.Errorf("warn count = %d, want 1", got)
	}

	// Success reports the rewound prompt text.
	mock.rewindToFn = nil
	rewound, text := c.autoRewindOnAbortFor(mock)
	if !rewound || text != "q" {
		t.Errorf("successful rewind = (%v, %q), want (true, \"q\")", rewound, text)
	}
	mock.mu.RLock()
	calls := append([]int(nil), mock.rewindCalls...)
	mock.mu.RUnlock()
	if len(calls) != 2 || calls[0] != 0 || calls[1] != 0 {
		t.Errorf("rewindCalls = %v, want [0 0]", calls)
	}
	if got := sink.count("wui: autoRewind failed"); got != 1 {
		t.Errorf("warn count after success = %d, want still 1", got)
	}
}

// ---- input history ----

func TestMutA1_InputHistoryPath(t *testing.T) {
	// Connector with no bound engine: empty path, no nil-interface panic.
	bare := &WUIConnector{}
	if got := bare.inputHistoryPath(); got != "" {
		t.Errorf("bare connector inputHistoryPath = %q, want empty", got)
	}

	c := newTestConnector(t)
	mock := c.mock()
	mock.projectDirFn = func() string { return "" }
	mock.engineIDFn = func() string { return "e1" }
	if got := c.inputHistoryPathFor(mock); got != "" {
		t.Errorf("empty projectDir: path = %q, want empty", got)
	}
	mock.projectDirFn = func() string { return "/proj" }
	mock.engineIDFn = func() string { return "" }
	if got := c.inputHistoryPathFor(mock); got != "" {
		t.Errorf("empty engineID: path = %q, want empty", got)
	}
	mock.engineIDFn = func() string { return "e1" }
	want := filepath.Join("/proj", "history", "e1.jsonl")
	if got := c.inputHistoryPathFor(mock); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestMutA1_LoadInputHistoryFromFile(t *testing.T) {
	if got := loadInputHistoryFromFile(""); got != nil {
		t.Errorf("empty path = %v, want nil", got)
	}
	if got := loadInputHistoryFromFile(filepath.Join(t.TempDir(), "missing.jsonl")); got != nil {
		t.Errorf("missing file = %v, want nil", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "h.jsonl")
	// The duplicate-key line partially decodes ("kept" lands in Display
	// before the type error) yet must be skipped as malformed.
	lines := []string{
		`{"display":"a","timestamp":1}`,
		`{"display":"kept","display":123}`,
		`{"display":""}`,
		`{"display":"b","timestamp":2}`,
		`{"display":"b","timestamp":3}`,
		`{"display":"c","timestamp":4}`,
		`not-json`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadInputHistoryFromFile(path)
	if want := []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("load = %v, want %v", got, want)
	}

	// Cap keeps the newest 1000 entries.
	var capLines []string
	for i := 1; i <= 1003; i++ {
		capLines = append(capLines, fmt.Sprintf(`{"display":"line%04d"}`, i))
	}
	capPath := filepath.Join(dir, "cap.jsonl")
	if err := os.WriteFile(capPath, []byte(strings.Join(capLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capped := loadInputHistoryFromFile(capPath)
	if len(capped) != 1000 {
		t.Fatalf("capped len = %d, want 1000", len(capped))
	}
	if capped[0] != "line0004" || capped[999] != "line1003" {
		t.Errorf("cap window = [%s, %s], want [line0004, line1003]", capped[0], capped[999])
	}
}

func TestMutA1_AppendInputHistory(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		c := newTestConnector(t)
		defer c.Stop()
		mock := c.mock()
		before := time.Now().UnixMilli()
		c.appendInputHistory("cmd one")

		path := filepath.Join(mock.projectDir, "history", "main.jsonl")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("history file missing: %v", err)
		}
		if n := strings.Count(string(raw), "\n"); n != 1 {
			t.Errorf("file has %d lines, want 1", n)
		}
		var entry inputHistoryEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("unmarshal history line: %v", err)
		}
		if entry.Display != "cmd one" {
			t.Errorf("display = %q, want %q", entry.Display, "cmd one")
		}
		// The bubble clock never advances mid-goroutine, so the stored
		// timestamp is exactly the captured reading.
		if entry.Timestamp != before {
			t.Errorf("timestamp = %d, want the captured reading %d", entry.Timestamp, before)
		}

		// Directory and file permissions are part of the on-disk contract.
		dirInfo, err := os.Stat(filepath.Join(mock.projectDir, "history"))
		if err != nil {
			t.Fatal(err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Errorf("history dir mode = %o, want 700", got)
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fileInfo.Mode().Perm(); got != 0o600 {
			t.Errorf("history file mode = %o, want 600", got)
		}

		// Round trip through the loader.
		c.appendInputHistory("cmd two")
		if got := c.loadInputHistory(); !slices.Equal(got, []string{"cmd one", "cmd two"}) {
			t.Errorf("loadInputHistory = %v, want [cmd one cmd two]", got)
		}
	})
}

// ---- handleAsk ----

func TestMutA1_HandleAskOutboundFrame(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	c.handleAsk(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{
		Kind:       types.AskPermission,
		ToolName:   "Bash",
		Input:      json.RawMessage(`{"command":"ls"}`),
		Message:    "allow?",
		RuleDetail: "Bash(ls)",
		Prompt:     "pr",
		Masked:     true,
		AgentType:  "Critic",
	}})
	var frame struct {
		Type         string          `json:"type"`
		ID           string          `json:"id"`
		Kind         string          `json:"kind"`
		ToolName     string          `json:"tool_name"`
		Input        json.RawMessage `json:"input"`
		Message      string          `json:"message"`
		RuleDetail   string          `json:"rule_detail"`
		Prompt       string          `json:"prompt"`
		Masked       bool            `json:"masked"`
		AgentType    string          `json:"agent_type"`
		DeadlineUnix int64           `json:"deadline_unix"`
	}
	if err := json.Unmarshal(readWSMessage(t, ws), &frame); err != nil {
		t.Fatalf("unmarshal ask frame: %v", err)
	}
	if frame.Type != "ask" || frame.ID != "1" || frame.Kind != "permission" {
		t.Errorf("ask header = %+v", frame)
	}
	if frame.ToolName != "Bash" || string(frame.Input) != `{"command":"ls"}` || frame.Message != "allow?" {
		t.Errorf("ask body = %+v", frame)
	}
	if frame.RuleDetail != "Bash(ls)" || frame.Prompt != "pr" || !frame.Masked || frame.AgentType != "Critic" {
		t.Errorf("ask extras = rule:%q prompt:%q masked:%v agent:%q",
			frame.RuleDetail, frame.Prompt, frame.Masked, frame.AgentType)
	}
	if frame.DeadlineUnix != 0 {
		t.Errorf("permission ask carries deadline_unix = %d, want 0", frame.DeadlineUnix)
	}
	c.pendingMu.Lock()
	stored := c.pendingAsks["1"] != nil
	c.pendingMu.Unlock()
	if !stored {
		t.Errorf("ask id 1 not stored in pendingAsks")
	}

	// Input asks count up from the shared counter and carry the deadline.
	deadline := time.Unix(1760000000, 0)
	c.handleAsk(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{
		Kind:     types.AskInput,
		Prompt:   "pw",
		Deadline: deadline,
	}})
	var frame2 struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		DeadlineUnix int64  `json:"deadline_unix"`
	}
	if err := json.Unmarshal(readWSMessage(t, ws), &frame2); err != nil {
		t.Fatalf("unmarshal ask frame 2: %v", err)
	}
	if frame2.ID != "2" || frame2.Kind != "input" {
		t.Errorf("second ask = %+v, want id 2 kind input", frame2)
	}
	if frame2.DeadlineUnix != 1760000000 {
		t.Errorf("deadline_unix = %d, want 1760000000", frame2.DeadlineUnix)
	}
}

// ---- engineAdapter ----

func TestMutA1_EngineAdapterRewindToErrorPropagates(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "main", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	t.Cleanup(eng.Close)
	a := &engineAdapter{eng: eng}

	err := a.RewindTo(5)
	if err == nil {
		t.Fatal("RewindTo(5) on empty engine: err = nil, want out-of-range error")
	}
	if err.Error() != "rewind index 5 out of range [0, 0]" {
		t.Errorf("err = %q, want rewind index 5 out of range [0, 0]", err.Error())
	}
}

func TestMutA1_EngineAdapterRewindToPartialRestoreWarn(t *testing.T) {
	sink := a1SwapDefaultLogger(t)
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(hub.NewHub())
	t.Cleanup(eng.Close)

	backupDir := t.TempDir()
	tracker := filehistory.NewTracker(backupDir)
	eng.SetFileHistory(tracker)
	tmpFile := filepath.Join(t.TempDir(), "test.go")
	if err := os.WriteFile(tmpFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tracker.TrackEdit(tmpFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("snap-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	if err := os.WriteFile(tmpFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Destroy the backups so the rewind truncates but cannot restore.
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one backup file")
	}
	for _, ent := range entries {
		if err := os.Remove(filepath.Join(backupDir, ent.Name())); err != nil {
			t.Fatal(err)
		}
	}
	eng.SetMessages([]types.Message{
		{ID: "snap-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
	})

	a := &engineAdapter{eng: eng}
	if err := a.RewindTo(0); err != nil {
		t.Fatalf("RewindTo partial restore: %v — partial restore must not fail the caller", err)
	}
	if got := sink.count("wui:rewind:partial_restore"); got != 1 {
		t.Errorf("partial-restore warn count = %d, want 1", got)
	}

	// A clean rewind logs nothing extra.
	eng2 := engine.New(&engine.Params{EngineID: "e2"})
	eng2.SetDispatcher(hub.NewHub())
	t.Cleanup(eng2.Close)
	eng2.SetMessages([]types.Message{a1UserMsg("q"), a1AssistantMsg("a")})
	a2 := &engineAdapter{eng: eng2}
	if err := a2.RewindTo(1); err != nil {
		t.Fatalf("clean RewindTo: %v", err)
	}
	if got := sink.count("wui:rewind:partial_restore"); got != 1 {
		t.Errorf("clean rewind logged partial_restore (count %d), want count still 1", got)
	}
}

func TestMutA1_EngineAdapterDelegation(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	t.Cleanup(eng.Close)

	streamCalls := make(chan struct{}, 8)
	prov := &a1BlockProvider{onStream: func() { streamCalls <- struct{}{} }}
	a := &engineAdapter{eng: eng}
	a.SetProvider(prov)

	ctx := context.Background()

	// Query must reach the engine (busy) and the provider (Stream call).
	a.Query(ctx, "hi", "")
	if !waitFor(5*time.Second, func() bool { return a.IsBusy() }) {
		t.Fatal("engine never busy after Query — delegation dropped")
	}
	select {
	case <-streamCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("provider.Stream never called — Query delegation dropped")
	}

	// Abort must reach the engine and release the query.
	a.Abort()
	if !waitFor(5*time.Second, func() bool { return !a.IsBusy() }) {
		t.Fatal("engine still busy after Abort — delegation dropped")
	}

	// QueryWithContent must pass a live ctx through (a nil ctx panics in
	// context.WithCancel before anything else happens).
	a.QueryWithContent(ctx, []types.ContentBlock{types.NewTextBlock("hi")}, "")
	if !waitFor(5*time.Second, func() bool { return a.IsBusy() }) {
		t.Fatal("engine never busy after QueryWithContent — delegation dropped")
	}
	select {
	case <-streamCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("provider.Stream never called — QueryWithContent delegation dropped")
	}
	a.Abort()
	if !waitFor(5*time.Second, func() bool { return !a.IsBusy() }) {
		t.Fatal("engine still busy after Abort — delegation dropped")
	}

	// Attachment queue round trip.
	a.EnqueueAttachment(types.QueuedItem{Value: "v1", UUID: "u1", Mode: types.ItemModePrompt})
	pend := a.PendingAttachments()
	if len(pend) != 1 || pend[0].UUID != "u1" {
		t.Errorf("PendingAttachments = %+v, want the enqueued u1 item", pend)
	}

	// System prompt round trip.
	eng.SetSystemPrompt("sp-a1")
	if got := a.SystemPrompt(); got != "sp-a1" {
		t.Errorf("SystemPrompt = %q, want sp-a1", got)
	}

	// SwitchSession surfaces the engine error instead of swallowing it.
	err := a.SwitchSession("nope")
	if err == nil {
		t.Fatal("SwitchSession on store-less engine: err = nil, want error")
	}
	if err.Error() != "switch session: no store" {
		t.Errorf("err = %q, want switch session: no store", err.Error())
	}
}

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// mutA2Tool is a configurable tool.Tool. desc/descErr drive Description;
// renderFn drives RenderResult.
type mutA2Tool struct {
	name    string
	desc    string
	descErr bool
	render  func(any) string
}

func (t *mutA2Tool) Name() string { return t.name }

func (t *mutA2Tool) Aliases() []string { return nil }

func (t *mutA2Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (t *mutA2Tool) Description(json.RawMessage) (string, error) {
	if t.descErr {
		return "", errors.New("muta2 desc error")
	}
	return t.desc, nil
}

func (t *mutA2Tool) RenderResult(data any) string {
	if t.render != nil {
		return t.render(data)
	}
	return fmt.Sprintf("RENDER:%v", data)
}

func (t *mutA2Tool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}

func (t *mutA2Tool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}

func (t *mutA2Tool) IsReadOnly(json.RawMessage) bool { return true }

func (t *mutA2Tool) IsDestructive(json.RawMessage) bool { return false }

func (t *mutA2Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *mutA2Tool) IsEnabled() bool { return true }

func (t *mutA2Tool) InterruptBehavior() tool.InterruptBehavior { return 0 }

func (t *mutA2Tool) MaxResultSize() int { return 0 }

func (t *mutA2Tool) Prompt() string { return "" }

// mutA2FullTool additionally implements ToolWithSearchOrRead and
// ToolWithDecodeResult: DecodeResult unwraps the wire array and RenderResult
// echoes the inner text verbatim.
type mutA2FullTool struct {
	mutA2Tool
	srk     tool.SearchReadKind
	decoded func(string) string
}

func (t *mutA2FullTool) IsSearchOrRead(json.RawMessage) tool.SearchReadKind { return t.srk }

func (t *mutA2FullTool) DecodeResult(raw json.RawMessage) (any, error) {
	text, err := tool.UnmarshalSingleBlock(raw)
	if err != nil {
		return nil, err
	}
	return text, nil
}

func (t *mutA2FullTool) RenderResult(data any) string {
	s, ok := data.(string)
	if !ok {
		return "FALLBACK"
	}
	if t.decoded != nil {
		return t.decoded(s)
	}
	return s
}

// mutA2LLMProvider records the ctx of the last Complete/Stream call.
type mutA2LLMProvider struct {
	name   string
	gotCtx context.Context
}

func (p *mutA2LLMProvider) Name() string { return p.name }

func (p *mutA2LLMProvider) Complete(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
	p.gotCtx = ctx
	return nil, errors.New("muta2 provider boom")
}

func (p *mutA2LLMProvider) Stream(ctx context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.gotCtx = ctx
	return nil, errors.New("muta2 provider boom")
}

// mutA2WarnHandler counts slog records; mirrors outqueue_test.go's pattern.
type mutA2WarnHandler struct {
	records []string
}

func (h *mutA2WarnHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *mutA2WarnHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Message)
	return nil
}

func (h *mutA2WarnHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *mutA2WarnHandler) WithGroup(string) slog.Handler { return h }

func swapMutA2WarnHandler(t *testing.T) *mutA2WarnHandler {
	t.Helper()
	h := &mutA2WarnHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return h
}

// mutA2HistPayload mirrors buildHistory/buildPreCompactHistory output JSON.
type mutA2HistPayload struct {
	Type       string           `json:"type"`
	Messages   []historyChatMsg `json:"messages"`
	Stream     []streamBlock    `json:"stream"`
	NextCursor string           `json:"nextCursor"`
	HasMore    bool             `json:"hasMore"`
	Rev        string           `json:"rev"`
	Busy       bool             `json:"busy"`
	Seq        uint64           `json:"seq"`
	Nonce      uint64           `json:"nonce"`
}

func mutA2UserMsg(id, text string) types.Message {
	return types.Message{
		ID:        id,
		Role:      types.RoleUser,
		Timestamp: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Content:   []types.ContentBlock{types.NewTextBlock(text)},
	}
}

func mutA2BareConnector() *WUIConnector {
	c := &WUIConnector{
		pendingAsks: make(map[string]*types.AskEvent),
		providers:   map[string]llm.Provider{},
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(16))
	return c
}

// ---------------------------------------------------------------------------
// engineAdapter delegation (connector.go:173-221)
// ---------------------------------------------------------------------------

func TestMutA2EngineAdapterNewSession(t *testing.T) {
	// No store: the underlying engine errors; the adapter must surface it.
	noStore := engine.New(&engine.Params{EngineID: "ea-nos", Model: "m"})
	noStore.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: noStore}
	if _, err := a.NewSession(); err == nil || !strings.Contains(err.Error(), "no store") {
		t.Fatalf("NewSession() without store: err = %v, want %q", err, "engine: no store")
	}

	// With a store: a fresh non-empty session id is returned.
	store, err := short.NewStore(t.TempDir() + "/ea.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eng := engine.New(&engine.Params{EngineID: "ea-ok", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	b := &engineAdapter{eng: eng}
	sid, err := b.NewSession()
	if err != nil {
		t.Fatalf("NewSession() with store: %v", err)
	}
	if sid == "" {
		t.Fatal("NewSession() returned empty session id")
	}
	if sid != eng.SessionID() {
		t.Errorf("NewSession() = %q, want engine SessionID() %q", sid, eng.SessionID())
	}
}

func TestMutA2EngineAdapterTitleNoStore(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-t", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}
	err := a.UpdateSessionTitle("s1", "title")
	if err == nil || !strings.Contains(err.Error(), "no store") {
		t.Fatalf("UpdateSessionTitle without store: err = %v, want %q", err, "engine: no store")
	}
}

func TestMutA2EngineAdapterTitleError(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/ea.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := engine.New(&engine.Params{EngineID: "ea-te", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	// Closed store: every title update must error out through the adapter.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	a := &engineAdapter{eng: eng}
	if err := a.UpdateSessionTitle("missing-session", "t"); err == nil {
		t.Fatal("UpdateSessionTitle on closed store: err = nil, want non-nil")
	}
}

func TestMutA2EngineAdapterGetters(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-g", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}
	a.UpdateAutoCompactConfig(engine.AutoCompactConfig{ContextWindow: 200000})
	eng.SetContextTokens(4321)
	if got := a.ContextWindow(); got != 200000 {
		t.Errorf("ContextWindow() = %d, want 200000", got)
	}
	if got := a.GetContextTokens(); got != 4321 {
		t.Errorf("GetContextTokens() = %d, want 4321", got)
	}
	bd := a.ContextBreakdown()
	if bd == nil {
		t.Fatal("ContextBreakdown() = nil, want non-nil breakdown")
	}
	if bd.ContextWindow != 200000 {
		t.Errorf("breakdown.ContextWindow = %d, want 200000", bd.ContextWindow)
	}
	if bd.TotalTokens != 4321 {
		t.Errorf("breakdown.TotalTokens = %d, want 4321", bd.TotalTokens)
	}
}

func TestMutA2EngineAdapterSetters(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-s", Model: "m0"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}

	fp := &mutA2LLMProvider{name: "muta2p"}
	a.SetProvider(fp)
	if a.Provider() != fp {
		t.Error("Provider() after SetProvider does not return the same provider")
	}
	a.SetModel("m2")
	if eng.Model() != "m2" {
		t.Errorf("engine Model() = %q, want m2", eng.Model())
	}
	a.SetMaxTokens(555)
	if eng.MaxTokens() != 555 {
		t.Errorf("engine MaxTokens() = %d, want 555", eng.MaxTokens())
	}
	a.SetInputModalities([]string{"text", "image"})
	if !eng.SupportsModality("image") {
		t.Error("SupportsModality(image) = false after SetInputModalities")
	}
	if err := a.SetThinking(llm.EffortHigh); err != nil {
		t.Fatalf("SetThinking(high): %v", err)
	}
	if a.Thinking() != llm.EffortHigh {
		t.Errorf("Thinking() = %q, want high", a.Thinking())
	}
	if err := a.SetThinking(""); err == nil {
		t.Error("SetThinking(\"\") = nil error, want engine's empty-effort error")
	}
	a.UpdateAutoCompactConfig(engine.AutoCompactConfig{ContextWindow: 128000})
	if a.ContextWindow() != 128000 {
		t.Errorf("ContextWindow() = %d after UpdateAutoCompactConfig, want 128000", a.ContextWindow())
	}
}

func TestMutA2EngineAdapterPreCompactNoStore(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-pc", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}
	page, total, hasBoundary := a.PreCompactMessages(0, 30)
	if page != nil || total != 0 || hasBoundary {
		t.Errorf("PreCompactMessages without store = (%v, %d, %v), want (nil, 0, false)", page, total, hasBoundary)
	}
}

func TestMutA2EngineAdapterManualCompactCtx(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/ea.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ses, err := store.CreateSessionWithEngine("/proj", "test-model", "ea-mc")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	// Two large messages so the compactor's tail budget is exceeded and the
	// summarize path reaches the provider; content is irrelevant.
	for i := range 2 {
		em := types.Message{
			ID:        fmt.Sprintf("big-%d", i),
			Role:      types.RoleUser,
			Timestamp: time.Date(2026, 3, 1, 10, 0, i, 0, time.UTC),
			Content:   []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 200*1024))},
		}
		ts := short.EngineMessagesToStore([]types.Message{em})
		if err := store.AppendMessage(ses.SessionID, ts[0]); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	fp := &mutA2LLMProvider{name: "muta2p"}
	eng := engine.New(&engine.Params{
		EngineID:    "ea-mc",
		Model:       "test-model",
		Provider:    fp,
		AutoCompact: engine.AutoCompactConfig{ContextWindow: 100000},
	})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	if _, err := eng.SwitchSession(ses.SessionID); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	a := &engineAdapter{eng: eng}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The summarize call fails at the provider; the failure itself proves the
	// provider was reached — the contract under test is that the caller's ctx
	// is the one forwarded.
	_, mcErr := a.ManualCompact(ctx, mutA2UserMsg("u", "/compact"), "")
	if mcErr == nil || !strings.Contains(mcErr.Error(), "summarize") {
		t.Fatalf("ManualCompact err = %v, want summarize failure from the fake provider", mcErr)
	}
	if fp.gotCtx == nil {
		t.Fatal("ManualCompact forwarded nil ctx to the provider")
	}
}

// ---------------------------------------------------------------------------
// handleAsk marshal failure (connector.go:1700)
// ---------------------------------------------------------------------------

func TestMutA2HandleAskMarshalFailure(t *testing.T) {
	c := mutA2BareConnector()
	h := swapMutA2WarnHandler(t)
	// Invalid RawMessage JSON makes json.Marshal(askOutbound) fail; nothing
	// may be queued on the WS in that case, and the failure is logged.
	evt := hub.Event{Ask: &types.AskEvent{
		Kind:       types.AskPermission,
		ToolName:   "Bash",
		Input:      json.RawMessage(`{invalid`),
		Message:    "run?",
		ResponseCh: make(chan types.AskResponse, 1),
	}}
	c.handleAsk(evt)
	if got := c.outQ.Load().len(); got != 0 {
		t.Fatalf("outQ len after failed marshal = %d, want 0", got)
	}
	if len(h.records) != 1 || h.records[0] != "wui: marshal ask failed" {
		t.Errorf("warn records = %v, want one wui: marshal ask failed", h.records)
	}
	// Control: a well-formed ask queues exactly one frame.
	evt.Ask.Input = json.RawMessage(`{"command":"ls"}`)
	c.handleAsk(evt)
	if got := c.outQ.Load().len(); got != 1 {
		t.Fatalf("outQ len after valid ask = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// preCompactMessages (connector.go:1748-1778)
// ---------------------------------------------------------------------------

func TestMutA2PreCompactLoadError(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 3, false, 0)
	// NULL parent_uuid breaks row scanning only for the non-boundary rows,
	// so GetLastBoundary still succeeds while LoadMessagesBeforeSeq fails.
	if _, err := store.DB().Exec("UPDATE messages SET parent_uuid = NULL WHERE uuid = 'pre-1'"); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	page, total, hasBoundary := preCompactMessages(store, "s1", 0, 10)
	if page != nil || total != 0 || hasBoundary {
		t.Errorf("preCompactMessages on scan error = (%v, %d, %v), want (nil, 0, false)", page, total, hasBoundary)
	}
}

func mutA2RawRow(t *testing.T, store *short.Store, sid, uuid, typ, subtype, content, metadata string) {
	t.Helper()
	row := &short.TranscriptMessage{
		UUID:      uuid,
		Type:      typ,
		Subtype:   subtype,
		Content:   content,
		Metadata:  metadata,
		CreatedAt: time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC),
	}
	if err := store.AppendMessage(sid, row); err != nil {
		t.Fatalf("AppendMessage %s: %v", uuid, err)
	}
}

func mutA2Boundary(t *testing.T, store *short.Store, sid, lastUUID string) {
	t.Helper()
	b := short.CreateCompactBoundaryMessage("auto", 1000, lastUUID)
	if err := store.AppendMessage(sid, b); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}
}

func mutA2StoreSession(t *testing.T) (*short.Store, string) {
	t.Helper()
	store, err := short.NewStore(t.TempDir() + "/muta2.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sid := "muta2-s"
	if _, err := store.DB().Exec("INSERT INTO sessions (session_id, project_dir) VALUES (?, '')", sid); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return store, sid
}

func mutA2UUIDs(page []*short.TranscriptMessage) []string {
	out := make([]string, len(page))
	for i, m := range page {
		out[i] = m.UUID
	}
	return out
}

func TestMutA2PreCompactAttachmentDropped(t *testing.T) {
	store, sid := mutA2StoreSession(t)
	mutA2RawRow(t, store, sid, "u0", "user", "", `[{"type":"text","text":"a"}]`, "")
	mutA2RawRow(t, store, sid, "att", "attachment", "", `[{"type":"text","text":"skip"}]`, "")
	mutA2RawRow(t, store, sid, "u1", "user", "", `[{"type":"text","text":"b"}]`, "")
	mutA2Boundary(t, store, sid, "u1")

	page, total, hasBoundary := preCompactMessages(store, sid, 0, 10)
	if !hasBoundary || total != 2 {
		t.Fatalf("total = %d, hasBoundary = %v; want 2, true (attachment excluded)", total, hasBoundary)
	}
	if got := mutA2UUIDs(page); len(got) != 2 || got[0] != "u0" || got[1] != "u1" {
		t.Errorf("page = %v, want [u0 u1]", got)
	}
}

func TestMutA2PreCompactSystemInfoDropped(t *testing.T) {
	store, sid := mutA2StoreSession(t)
	mutA2RawRow(t, store, sid, "sys0", "system", "informational", `[{"type":"text","text":"sys"}]`, "")
	mutA2RawRow(t, store, sid, "u0", "user", "", `[{"type":"text","text":"keep"}]`, "")
	mutA2Boundary(t, store, sid, "u0")

	page, total, hasBoundary := preCompactMessages(store, sid, 0, 10)
	if !hasBoundary || total != 1 {
		t.Fatalf("total = %d, hasBoundary = %v; want 1, true (system informational excluded)", total, hasBoundary)
	}
	if got := mutA2UUIDs(page); len(got) != 1 || got[0] != "u0" {
		t.Errorf("page = %v, want [u0] (rows after the informational row survive)", got)
	}
}

func TestMutA2PreCompactMetaFlagDropped(t *testing.T) {
	store, sid := mutA2StoreSession(t)
	metaMsg := types.Message{
		ID:      "meta-u",
		Role:    types.RoleUser,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("hidden")},
	}
	ts := short.EngineMessagesToStore([]types.Message{metaMsg})
	if err := store.AppendMessage(sid, ts[0]); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	mutA2RawRow(t, store, sid, "u0", "user", "", `[{"type":"text","text":"keep"}]`, "")
	// Plain metadata without any flag: hasFilteredFlag must keep this row.
	mutA2RawRow(t, store, sid, "u1", "user", "", `[{"type":"text","text":"plain-meta"}]`, `{}`)
	mutA2Boundary(t, store, sid, "u1")

	page, total, hasBoundary := preCompactMessages(store, sid, 0, 10)
	if !hasBoundary || total != 2 {
		t.Fatalf("total = %d, hasBoundary = %v; want 2, true (FlagMeta row excluded, plain-metadata row kept)", total, hasBoundary)
	}
	if got := mutA2UUIDs(page); len(got) != 2 || got[0] != "u0" || got[1] != "u1" {
		t.Errorf("page = %v, want [u0 u1]", got)
	}
}

func TestMutA2PreCompactDeliveredEqualsTotal(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 3, false, 0)
	page, total, hasBoundary := preCompactMessages(store, "s1", 3, 10)
	if !hasBoundary || total != 3 {
		t.Fatalf("total = %d, hasBoundary = %v; want 3, true", total, hasBoundary)
	}
	if page != nil {
		t.Errorf("page = %v, want nil sentinel when delivered == total", mutA2UUIDs(page))
	}
}

// ---------------------------------------------------------------------------
// buildHistoryChatMsg (connector.go:1804-1917)
// ---------------------------------------------------------------------------

func TestMutA2HistoryChatMsgBasics(t *testing.T) {
	c := mutA2BareConnector()
	ts := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)
	m := types.Message{
		ID:        "m1",
		Role:      types.RoleUser,
		Timestamp: ts,
		Content: []types.ContentBlock{
			types.NewTextBlock("a"),
			types.NewTextBlock("b"),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if hm.ID != "m1" || hm.Role != "user" {
		t.Errorf("id/role = %q/%q, want m1/user", hm.ID, hm.Role)
	}
	if hm.StartedAt != ts.UnixMilli() {
		t.Errorf("startedAt = %d, want %d", hm.StartedAt, ts.UnixMilli())
	}
	if hm.Status != "done" {
		t.Errorf("status = %q, want done", hm.Status)
	}
	if hm.Text != "ab" {
		t.Errorf("text = %q, want concatenated ab", hm.Text)
	}
	if len(hm.Blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(hm.Blocks))
	}
	for i, want := range []string{"a", "b"} {
		if hm.Blocks[i].Kind != "text" || hm.Blocks[i].Text != want {
			t.Errorf("blocks[%d] = %s/%q, want text/%q", i, hm.Blocks[i].Kind, hm.Blocks[i].Text, want)
		}
	}
}

func TestMutA2HistoryChatMsgAPIError(t *testing.T) {
	c := mutA2BareConnector()
	doc := types.ContentBlock{Type: types.ContentTypeDocument, Name: "d.md", Mime: "text/markdown", Size: 12}
	m := types.Message{
		ID:      "e1",
		Role:    types.RoleAssistant,
		Flags:   types.FlagAPIError,
		Content: []types.ContentBlock{types.NewTextBlock("boom1"), types.NewTextBlock("boom2"), doc},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if hm.Error != "boom1boom2" {
		t.Errorf("error = %q, want concatenated boom1boom2", hm.Error)
	}
	if hm.Text != "" {
		t.Errorf("text = %q, want empty (flagged text never lands in Text)", hm.Text)
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Kind != "document" {
		t.Errorf("blocks = %+v, want only the document block after the flagged texts", hm.Blocks)
	}
}

func TestMutA2HistoryChatMsgWhitespaceText(t *testing.T) {
	c := mutA2BareConnector()
	m := types.Message{
		ID:   "w1",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("  \n\t"),
			types.NewTextBlock("real"),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if hm.Text != "  \n\treal" {
		t.Errorf("text = %q, want legacy whitespace preserved", hm.Text)
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Text != "real" {
		t.Errorf("blocks = %+v, want exactly one block (whitespace-only text consumes no slot)", hm.Blocks)
	}
}

func TestMutA2HistoryChatMsgImagePlaceholder(t *testing.T) {
	c := mutA2BareConnector()
	m := types.Message{
		ID:   "i1",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Source: nil},
			{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", Path: "/tmp/dir/foo.png"}},
			{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file"}},
			types.NewTextBlock("hi"),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	wantText := "[image][image: foo.png][image]hi"
	if hm.Text != wantText {
		t.Errorf("text = %q, want %q", hm.Text, wantText)
	}
	wantBlocks := []struct{ kind, text string }{
		{"text", "[image]"},
		{"text", "[image: foo.png]"},
		{"text", "[image]"},
		{"text", "hi"},
	}
	if len(hm.Blocks) != len(wantBlocks) {
		t.Fatalf("blocks len = %d, want %d (%+v)", len(hm.Blocks), len(wantBlocks), hm.Blocks)
	}
	for i, w := range wantBlocks {
		if hm.Blocks[i].Kind != w.kind || hm.Blocks[i].Text != w.text {
			t.Errorf("blocks[%d] = %s/%q, want %s/%q", i, hm.Blocks[i].Kind, hm.Blocks[i].Text, w.kind, w.text)
		}
	}

	// Degraded image followed by a document: both blocks survive.
	m2 := types.Message{
		ID:   "i2",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Source: nil},
			{Type: types.ContentTypeDocument, Name: "n", Mime: "text/plain", Size: 1},
		},
	}
	hm2 := c.buildHistoryChatMsg(m2, nil, nil, nil)
	if len(hm2.Blocks) != 2 || hm2.Blocks[0].Kind != "text" || hm2.Blocks[1].Kind != "document" {
		t.Errorf("blocks = %+v, want [text placeholder, document]", hm2.Blocks)
	}
}

func TestMutA2HistoryChatMsgThinking(t *testing.T) {
	c := mutA2BareConnector()
	blank := types.ContentBlock{Type: types.ContentTypeThinking, Thinking: "   "}
	deep := types.ContentBlock{Type: types.ContentTypeThinking, Thinking: "deep thought", ThinkingDurationNs: 77}
	hm := c.buildHistoryChatMsg(types.Message{ID: "t1", Role: types.RoleAssistant, Content: []types.ContentBlock{blank, deep}}, nil, nil, nil)
	if len(hm.Thinking) != 1 {
		t.Fatalf("thinking entries = %d, want 1 (whitespace-only skipped)", len(hm.Thinking))
	}
	if hm.Thinking[0].Text != "deep thought" || hm.Thinking[0].DurationNs != 77 {
		t.Errorf("thinking[0] = %+v, want {deep thought 77}", hm.Thinking[0])
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Kind != "thinking" || hm.Blocks[0].Thinking == nil || hm.Blocks[0].Thinking.DurationNs != 77 {
		t.Errorf("blocks = %+v, want one thinking block with duration 77", hm.Blocks)
	}
}

func TestMutA2HistoryChatMsgToolUse(t *testing.T) {
	c := mutA2BareConnector()
	tools := map[string]tool.Tool{
		"X": &mutA2FullTool{
			mutA2Tool: mutA2Tool{name: "X", desc: "SUM-X"},
			srk:       tool.SearchReadKind{IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true},
		},
	}
	wire := json.RawMessage(`[{"type":"text","text":"WIRE"}]`)
	toolResults := map[string]types.ContentBlock{
		"tu1": {Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: wire, IsError: true, ToolDurationNs: 99},
	}
	doneMsg := types.Message{
		ID:   "tu-done",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu1", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	hm := c.buildHistoryChatMsg(doneMsg, tools, toolResults, nil)
	if len(hm.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(hm.Tools))
	}
	e := hm.Tools[0]
	if e.ID != "tu1" || e.Name != "X" {
		t.Errorf("entry id/name = %q/%q, want tu1/X", e.ID, e.Name)
	}
	if e.Summary != "SUM-X" {
		t.Errorf("summary = %q, want SUM-X", e.Summary)
	}
	if !e.IsSearch || !e.IsRead || !e.IsList || !e.IsLsp || !e.IsTask {
		t.Errorf("srk flags = %+v, want all true", e)
	}
	if !e.IsError {
		t.Error("IsError = false, want true from the tool_result block")
	}
	if e.IsRunning {
		t.Error("IsRunning = true, want false (result present)")
	}
	if e.DisplayOutput != "WIRE" {
		t.Errorf("displayOutput = %q, want WIRE", e.DisplayOutput)
	}
	if e.DurationNs != 99 {
		t.Errorf("durationNs = %d, want 99", e.DurationNs)
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Kind != "tool" || hm.Blocks[0].Tool == nil || hm.Blocks[0].Tool.ID != "tu1" {
		t.Errorf("blocks = %+v, want one tool block for tu1", hm.Blocks)
	}

	// No result yet: the entry must flip to running.
	runningMsg := types.Message{
		ID:   "tu-run",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu2", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	hm2 := c.buildHistoryChatMsg(runningMsg, tools, toolResults, nil)
	if len(hm2.Tools) != 1 || !hm2.Tools[0].IsRunning {
		t.Errorf("running entry = %+v, want IsRunning true", hm2.Tools)
	}
}

func TestMutA2HistoryChatMsgUsage(t *testing.T) {
	c := mutA2BareConnector()
	m := types.Message{
		ID:    "us1",
		Role:  types.RoleAssistant,
		Usage: &types.Usage{InputTokens: 11, OutputTokens: 22, CacheReadInputTokens: 33, CacheCreationInputTokens: 44},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	want := historyUsage{InputTokens: 11, OutputTokens: 22, CacheRead: 33, CacheCreation: 44}
	if hm.Usage != want {
		t.Errorf("usage = %+v, want %+v", hm.Usage, want)
	}
	// Usage absent: stays zero.
	hm2 := c.buildHistoryChatMsg(mutA2UserMsg("us0", "x"), nil, nil, nil)
	if hm2.Usage != (historyUsage{}) {
		t.Errorf("usage without Usage block = %+v, want zero", hm2.Usage)
	}
}

// ---------------------------------------------------------------------------
// historyImageDataURL (connector.go:1925-1972)
// ---------------------------------------------------------------------------

func mutA2PNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf strings.Builder
	if err := png.Encode(&mutBufWriter{&buf}, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return []byte(buf.String())
}

type mutBufWriter struct{ b *strings.Builder }

func (w *mutBufWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func TestMutA2ImageDataURLNilSource(t *testing.T) {
	c := mutA2BareConnector()
	url, ok := c.historyImageDataURL(types.ContentBlock{Type: types.ContentTypeImage})
	if ok || url != "" {
		t.Errorf("nil source = (%q, %v), want (\"\", false) without panic", url, ok)
	}
	// Unknown source type and undecodable base64 both degrade to (\"\", false).
	weird := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "carrier-pigeon"}}
	if url, ok := c.historyImageDataURL(weird); ok || url != "" {
		t.Errorf("unknown source type = (%q, %v), want (\"\", false)", url, ok)
	}
	badB64 := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: "!!!not-base64!!!"}}
	if url, ok := c.historyImageDataURL(badB64); ok || url != "" {
		t.Errorf("bad base64 = (%q, %v), want (\"\", false)", url, ok)
	}
	// Missing file also degrades cleanly.
	missing := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", Path: "/nonexistent/muta2.png"}}
	if url, ok := c.historyImageDataURL(missing); ok || url != "" {
		t.Errorf("missing file = (%q, %v), want (\"\", false)", url, ok)
	}
	// Valid base64 source round-trips and stays stable across two reads
	// (second read may come from the cache — the value must not change).
	b64 := base64.StdEncoding.EncodeToString(mutA2PNG(t, 300, 200, color.RGBA{R: 200, A: 255}))
	cb := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: b64}}
	url1, ok := c.historyImageDataURL(cb)
	if !ok || !strings.HasPrefix(url1, "data:image/png;base64,") {
		t.Fatalf("base64 read = (%.40s, %v), want passthrough data URL", url1, ok)
	}
	url2, ok := c.historyImageDataURL(cb)
	if !ok || url2 != url1 {
		t.Errorf("second base64 read = (%.40s, %v), want identical (%.40s, true)", url2, ok, url1)
	}
}

func TestMutA2ImageDataURLFileCache(t *testing.T) {
	c := mutA2BareConnector()
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, mutA2PNG(t, 800, 600, color.RGBA{R: 255, A: 255}), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mtime := fi.ModTime()

	cb := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", Path: path}}
	url1, ok := c.historyImageDataURL(cb)
	if !ok || !strings.HasPrefix(url1, "data:") {
		t.Fatalf("first read = (%q, %v), want data URL", url1, ok)
	}

	// Same path, same mtime, different bytes: the cache must still hit.
	if err := os.WriteFile(path, mutA2PNG(t, 800, 600, color.RGBA{B: 255, A: 255}), 0o644); err != nil {
		t.Fatalf("WriteFile 2: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	url2, ok := c.historyImageDataURL(cb)
	if !ok {
		t.Fatal("second read ok = false, want true")
	}
	if url2 != url1 {
		t.Errorf("cache key must include mtime: url2 = %.60s..., want identical to url1", url2)
	}

	// A bumped mtime invalidates the entry.
	later := mtime.Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("Chtimes 2: %v", err)
	}
	url3, ok := c.historyImageDataURL(cb)
	if !ok {
		t.Fatal("third read ok = false, want true")
	}
	if url3 == url1 {
		t.Error("url after mtime bump = url1; changed content must produce a new thumbnail")
	}
}

func TestMutA2ImageDataURLThumbSize(t *testing.T) {
	c := mutA2BareConnector()
	path := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(path, mutA2PNG(t, 1026, 1026, color.RGBA{G: 255, A: 255}), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	url, ok := c.historyImageDataURL(types.ContentBlock{
		Type:   types.ContentTypeImage,
		Source: &types.ImageSource{Type: "file", Path: path},
	})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	const prefix = "data:image/jpeg;base64,"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("url prefix = %.40s, want %q", url, prefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	img, err := jpeg.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("jpeg decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 512 || b.Dy() != 512 {
		t.Errorf("thumbnail bounds = %dx%d, want exactly 512x512", b.Dx(), b.Dy())
	}
}

// ---------------------------------------------------------------------------
// isCompactBoundaryMarker (connector.go:1985-1992)
// ---------------------------------------------------------------------------

func TestMutA2IsCompactBoundaryMarker(t *testing.T) {
	marker := types.Message{Role: types.RoleSystem, Content: []types.ContentBlock{
		types.NewTextBlock(`{"subtype":"compact_boundary"}`),
	}}
	if !isCompactBoundaryMarker(marker) {
		t.Error("marker text block not detected")
	}
	if isCompactBoundaryMarker(mutA2UserMsg("p", "plain text")) {
		t.Error("plain text message misclassified as boundary marker")
	}
	thinkingOnly := types.Message{Role: types.RoleSystem, Content: []types.ContentBlock{
		{Type: types.ContentTypeThinking, Thinking: `"compact_boundary"`},
	}}
	if isCompactBoundaryMarker(thinkingOnly) {
		t.Error("marker string inside a thinking block must not count")
	}
	// The type gate is on cb.Type: a non-text block whose Text FIELD carries
	// the marker must not be misread as a boundary marker.
	toolWithMarkerText := types.Message{Role: types.RoleSystem, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t", Name: "X", Text: `"compact_boundary"`},
	}}
	if isCompactBoundaryMarker(toolWithMarkerText) {
		t.Error("marker string in a tool_use block's Text field must not count")
	}
}

// ---------------------------------------------------------------------------
// buildHistory (connector.go:1994-2103)
// ---------------------------------------------------------------------------

func mutA2Slot(mock *mockEngine) *engineSlot {
	return &engineSlot{engineID: "muta2", engine: mock}
}

func mutA2ParseHistory(t *testing.T, payload []byte) mutA2HistPayload {
	t.Helper()
	var out mutA2HistPayload
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal history payload: %v (raw: %.200s)", err, payload)
	}
	return out
}

func TestMutA2BuildHistoryNilSlot(t *testing.T) {
	c := mutA2BareConnector()
	if got := c.buildHistory(nil, "", 30); got != nil {
		t.Errorf("buildHistory(nil) = %.40s, want nil", got)
	}
}

func TestMutA2BuildHistoryQueryStart(t *testing.T) {
	c := mutA2BareConnector()
	msgs := []types.Message{mutA2UserMsg("m0", "q"), mutA2UserMsg("m1", "a"), mutA2UserMsg("m2", "q2")}
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return msgs })

	// Boundary: query starts at index 0 — exactly one message survives.
	mock.SetQueryStartMsgIdxFn(func() int { return 0 })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 1 || p.Messages[0].Text != "q" {
		t.Errorf("messages after truncation = %+v, want only the query at idx 0", p.Messages)
	}

	// Boundary: idx == len(msgs) must not panic and must keep all messages.
	mock.SetQueryStartMsgIdxFn(func() int { return len(msgs) })
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 3 {
		t.Errorf("messages with idx == len = %d, want 3 (no truncation)", len(p.Messages))
	}
}

func TestMutA2BuildHistoryEmptyAfterFilter(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	sys := types.Message{
		ID:      "sys",
		Role:    types.RoleSystem,
		Content: []types.ContentBlock{types.NewTextBlock("informational")},
	}
	mock.SetMessagesFn(func() []types.Message { return []types.Message{sys} })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 0 || p.HasMore {
		t.Errorf("all filtered: messages=%d hasMore=%v, want empty page/hasMore=false", len(p.Messages), p.HasMore)
	}
	if p.Rev != "1:sys" {
		t.Errorf("all filtered rev = %q, want 1:sys (rev counts the raw truncated view)", p.Rev)
	}
	// No messages at all: empty page frame, rev "0:".
	mock.SetMessagesFn(func() []types.Message { return nil })
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 0 || p.HasMore {
		t.Errorf("no messages: messages=%d hasMore=%v, want empty page/hasMore=false", len(p.Messages), p.HasMore)
	}
	if p.Rev != "0:" {
		t.Errorf("no messages rev = %q, want \"0:\"", p.Rev)
	}
}

func TestMutA2BuildHistoryFilters(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	sys := types.Message{ID: "sys", Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("sysinfo")}}
	meta := mutA2UserMsg("meta", "hidden")
	meta.Flags = types.FlagMeta
	ok := mutA2UserMsg("ok", "visible")
	// Visible message last: a break (instead of continue) at either filter
	// would drop it and collapse the payload to nil.
	mock.SetMessagesFn(func() []types.Message { return []types.Message{sys, meta, ok} })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 1 || p.Messages[0].ID != "ok" || p.Messages[0].Text != "visible" {
		t.Errorf("messages = %+v, want only the unflagged user message", p.Messages)
	}
}

func TestMutA2BuildHistoryBoundaryDivider(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	ts := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	divider := types.Message{
		ID:        "bnd",
		Role:      types.RoleSystem,
		Timestamp: ts,
		Flags:     types.FlagCompactSummary,
		Content:   []types.ContentBlock{types.NewTextBlock(`{"subtype":"compact_boundary"}`)},
	}
	summary := types.Message{
		ID:      "sum",
		Role:    types.RoleSystem,
		Flags:   types.FlagCompactSummary,
		Content: []types.ContentBlock{types.NewTextBlock("human readable summary")},
	}
	mock.SetMessagesFn(func() []types.Message {
		return []types.Message{mutA2UserMsg("u0", "before"), divider, summary, mutA2UserMsg("u1", "after")}
	})
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, divider, user; summary skipped)", len(p.Messages))
	}
	d := p.Messages[1]
	if !d.CompactBoundary {
		t.Errorf("messages[1] = %+v, want compactBoundary divider", d)
	}
	if d.ID != "bnd" || d.Role != "system" || d.StartedAt != ts.UnixMilli() {
		t.Errorf("divider = id %q role %q startedAt %d; want bnd/system/%d", d.ID, d.Role, d.StartedAt, ts.UnixMilli())
	}
	if p.Messages[0].ID != "u0" || p.Messages[2].ID != "u1" {
		t.Errorf("message ids = %q,%q; want u0,u1", p.Messages[0].ID, p.Messages[2].ID)
	}
}

func mutA2NMessages(n int) []types.Message {
	msgs := make([]types.Message, n)
	for i := range n {
		msgs[i] = mutA2UserMsg(fmt.Sprintf("m%02d", i), fmt.Sprintf("t%02d", i))
	}
	return msgs
}

func TestMutA2BuildHistoryDefaultLimit(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	msgs := mutA2NMessages(35)
	mock.SetMessagesFn(func() []types.Message { return msgs })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 0))
	if len(p.Messages) != 30 {
		t.Fatalf("page len = %d, want 30 (default limit applies when limit <= 0)", len(p.Messages))
	}
	if p.Messages[0].Text != "t05" || p.Messages[29].Text != "t34" {
		t.Errorf("page bounds = %q..%q, want t05..t34", p.Messages[0].Text, p.Messages[29].Text)
	}
	if !p.HasMore || !regexp.MustCompile(`^a:5:0:\d+$`).MatchString(p.NextCursor) {
		t.Errorf("hasMore/nextCursor = %v/%q, want true/a:5:0:<gen>", p.HasMore, p.NextCursor)
	}
}

func TestMutA2BuildHistoryCursorPaging(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	msgs := mutA2NMessages(35)
	mock.SetMessagesFn(func() []types.Message { return msgs })

	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "5", 10))
	if len(p.Messages) != 10 {
		t.Fatalf("page len = %d, want 10", len(p.Messages))
	}
	if p.Messages[0].Text != "t20" || p.Messages[9].Text != "t29" {
		t.Errorf("page bounds = %q..%q, want t20..t29", p.Messages[0].Text, p.Messages[9].Text)
	}
	if !regexp.MustCompile(`^a:20:0:\d+$`).MatchString(p.NextCursor) {
		t.Errorf("nextCursor = %q, want a:20:0:<gen> (window ends 20 from head)", p.NextCursor)
	}

	// Cursor "1": offset must advance by exactly one.
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "1", 0))
	if !regexp.MustCompile(`^a:4:0:\d+$`).MatchString(p.NextCursor) {
		t.Errorf("nextCursor for cursor 1 = %q, want a:4:0:<gen>", p.NextCursor)
	}

	// Negative cursor: treated as offset 0, never a negative slice bound.
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "-3", 0))
	if len(p.Messages) != 30 || p.Messages[0].Text != "t05" {
		t.Errorf("negative cursor page = %d messages starting %q, want 30 starting t05", len(p.Messages), p.Messages[0].Text)
	}

	// Overflow cursor: Atoi errors, offset stays 0.
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "92233720368547758080", 0))
	if len(p.Messages) != 30 {
		t.Errorf("overflow cursor page len = %d, want 30", len(p.Messages))
	}
}

func TestMutA2BuildHistoryPreCompactProbe(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return mutA2NMessages(3) })
	var probeDelivered, probeLimit int
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		probeDelivered, probeLimit = delivered, limit
		return nil, 0, true
	}
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if probeDelivered != 0 || probeLimit != 0 {
		t.Errorf("probe args = (%d, %d), want (0, 0): the probe must not load messages", probeDelivered, probeLimit)
	}
	if !p.HasMore || p.NextCursor != "precompact:0" {
		t.Errorf("hasMore/nextCursor = %v/%q, want true/precompact:0 when a boundary exists", p.HasMore, p.NextCursor)
	}
}

func TestMutA2BuildHistoryToolResultKey(t *testing.T) {
	c := mutA2BareConnector()
	// A text block carrying a tool_use_id must NOT satisfy a pending tool_use.
	m0 := types.Message{
		ID:   "k0",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hi", ToolUseID: "tuX"},
		},
	}
	m1 := types.Message{
		ID:   "k1",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tuX", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return []types.Message{m0, m1} })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 2 || len(p.Messages[1].Tools) != 1 {
		t.Fatalf("payload shape unexpected: %+v", p.Messages)
	}
	if !p.Messages[1].Tools[0].IsRunning {
		t.Errorf("tool entry = %+v, want IsRunning (text blocks never count as results)", p.Messages[1].Tools[0])
	}

	// A tool_result with an empty id cannot satisfy a tool_use with an empty id.
	m2 := types.Message{
		ID:   "k2",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "", Content: json.RawMessage(`[{"type":"text","text":"r"}]`)},
		},
	}
	m3 := types.Message{
		ID:   "k3",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	mock.SetMessagesFn(func() []types.Message { return []types.Message{m2, m3} })
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 2 || len(p.Messages[1].Tools) != 1 || !p.Messages[1].Tools[0].IsRunning {
		t.Errorf("empty-id pair = %+v, want IsRunning (empty-keyed results are not collected)", p.Messages)
	}
}

func TestMutA2BuildHistoryRichResult(t *testing.T) {
	c := mutA2BareConnector()
	tools := map[string]tool.Tool{
		"X": &mutA2FullTool{mutA2Tool: mutA2Tool{name: "X"}},
	}
	m0 := types.Message{
		ID:             "r0",
		Role:           types.RoleUser,
		ToolResultData: map[string]any{"tu1": "RICH"},
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`[{"type":"text","text":"WIRE"}]`)},
		},
	}
	m1 := types.Message{
		ID:   "r1",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu1", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	mock := &mockEngine{}
	mock.toolsFn = func() map[string]tool.Tool { return tools }
	mock.SetMessagesFn(func() []types.Message { return []types.Message{m0, m1} })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 2 || len(p.Messages[1].Tools) != 1 {
		t.Fatalf("payload shape unexpected: %+v", p.Messages)
	}
	// WrapRichToolResult re-marshals the rich value, so the decode path sees
	// the JSON string "RICH" (quotes included) — not the wire text.
	if got := p.Messages[1].Tools[0].DisplayOutput; got != `"RICH"` {
		t.Errorf("displayOutput = %q, want rich-rendered %q", got, `"RICH"`)
	}
}

// ---------------------------------------------------------------------------
// buildPreCompactHistory (connector.go:2110-2185)
// ---------------------------------------------------------------------------

func TestMutA2PreCompactHistNilSlot(t *testing.T) {
	c := mutA2BareConnector()
	if got := c.buildPreCompactHistory(nil, "precompact:0", 30); got != nil {
		t.Errorf("buildPreCompactHistory(nil) = %.40s, want nil", got)
	}
}

func TestMutA2PreCompactHistLimitArgs(t *testing.T) {
	c := mutA2BareConnector()
	cases := []struct {
		cursor        string
		limit         int
		wantDelivered int
		wantLimit     int
	}{
		{"precompact:0", 0, 0, 30},
		{"precompact:0", 1, 0, 1},
		{"precompact:1", 0, 1, 30},
		{"precompact:3", 0, 3, 30},
		{"precompact:-3", 0, 0, 30},
		{"precompact:92233720368547758080", 0, 0, 30},
	}
	for _, tc := range cases {
		var gotDelivered, gotLimit int
		mock := &mockEngine{}
		mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
			gotDelivered, gotLimit = delivered, limit
			return nil, 0, false
		}
		c.buildPreCompactHistory(mutA2Slot(mock), tc.cursor, tc.limit)
		if gotDelivered != tc.wantDelivered || gotLimit != tc.wantLimit {
			t.Errorf("cursor %q limit %d: args = (%d, %d), want (%d, %d)",
				tc.cursor, tc.limit, gotDelivered, gotLimit, tc.wantDelivered, tc.wantLimit)
		}
	}
}

func TestMutA2PreCompactHistCursorWarn(t *testing.T) {
	c := mutA2BareConnector()
	h := swapMutA2WarnHandler(t)

	run := func(cursor string) mutA2HistPayload {
		mock := &mockEngine{}
		mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
			return nil, 0, false
		}
		return mutA2ParseHistory(t, c.buildPreCompactHistory(mutA2Slot(mock), cursor, 30))
	}

	h.records = nil
	p := run("precompact:abc")
	if len(h.records) != 1 || h.records[0] != "wui: malformed precompact cursor, treating as 0" {
		t.Errorf("warn records for malformed cursor = %v, want one wui: malformed precompact cursor record", h.records)
	}
	if p.Type != "history" {
		t.Errorf("type = %q, want history", p.Type)
	}

	h.records = nil
	run("precompact:0")
	if len(h.records) != 0 {
		t.Errorf("warn records for numeric cursor 0 = %v, want none", h.records)
	}

	h.records = nil
	run("precompact:-3")
	if len(h.records) != 0 {
		t.Errorf("warn records for negative cursor = %v, want none (Atoi succeeds)", h.records)
	}

	h.records = nil
	run("precompact:92233720368547758080")
	if len(h.records) != 1 {
		t.Errorf("warn records for overflow cursor = %v, want one", h.records)
	}
}

func TestMutA2PreCompactHistVanished(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return nil, 0, false
	}
	payload := c.buildPreCompactHistory(mutA2Slot(mock), "precompact:5", 30)
	if payload == nil {
		t.Fatal("vanished-boundary payload = nil, want an empty-page payload so the client stops paging")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["type"]) != `"history"` {
		t.Errorf("type = %s, want \"history\"", raw["type"])
	}
	if string(raw["messages"]) != "[]" {
		t.Errorf("messages = %s, want [] (non-null empty array)", raw["messages"])
	}
	if string(raw["nextCursor"]) != `""` || string(raw["hasMore"]) != "false" {
		t.Errorf("nextCursor/hasMore = %s/%s, want \"\"/false", raw["nextCursor"], raw["hasMore"])
	}
}

func TestMutA2PreCompactHistRendering(t *testing.T) {
	c := mutA2BareConnector()
	tools := map[string]tool.Tool{
		"X": &mutA2FullTool{mutA2Tool: mutA2Tool{name: "X", desc: "SUM-X"}},
	}
	bndTS := time.Date(2026, 3, 6, 7, 0, 0, 0, time.UTC)
	rows := []*short.TranscriptMessage{
		{UUID: "r0", Type: "user", Content: `[{"type":"text","text":"hello"}]`, CreatedAt: bndTS},
		{UUID: "bnd", Type: "system", Subtype: "compact_boundary", Content: `[]`, CreatedAt: bndTS},
		// User row with boundary subtype must render as a chat message.
		{UUID: "r2", Type: "user", Subtype: "compact_boundary",
			Content: `[{"type":"text","text":"ub","tool_use_id":"tuX"}]`, CreatedAt: bndTS},
		// System informational row must render, not become a divider.
		{UUID: "r3", Type: "system", Subtype: "informational",
			Content: `[{"type":"text","text":"sysinfo"}]`, CreatedAt: bndTS},
		{UUID: "r4", Type: "assistant",
			Content: `[{"type":"tool_use","id":"tu1","name":"X","input":{}}]`, CreatedAt: bndTS},
		{UUID: "r5", Type: "user",
			Content:  `[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"WIRE"}],"is_error":true,"tool_duration_ns":99}]`,
			Metadata: `{"toolUseResult":{"tu1":"RICH"}}`, CreatedAt: bndTS},
		{UUID: "r6", Type: "assistant",
			Content: `[{"type":"tool_use","id":"tuX","name":"X","input":{}}]`, CreatedAt: bndTS},
		// Empty-id pair: a tool_result with no tool_use_id can never satisfy
		// the empty-id tool_use below it.
		{UUID: "r7", Type: "user",
			Content: `[{"type":"tool_result","tool_use_id":"","content":[{"type":"text","text":"anon"}]}]`, CreatedAt: bndTS},
		{UUID: "r8", Type: "assistant",
			Content: `[{"type":"tool_use","id":"","name":"X","input":{}}]`, CreatedAt: bndTS},
	}
	mock := &mockEngine{}
	mock.toolsFn = func() map[string]tool.Tool { return tools }
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return rows, 12, true
	}
	p := mutA2ParseHistory(t, c.buildPreCompactHistory(mutA2Slot(mock), "precompact:0", 30))
	if len(p.Messages) != 9 {
		t.Fatalf("messages = %d, want 9 (%+v)", len(p.Messages), p.Messages)
	}
	d := p.Messages[1]
	if !d.CompactBoundary || d.ID != "bnd" || d.Role != "system" || d.StartedAt != bndTS.UnixMilli() {
		t.Errorf("divider = %+v, want {bnd system %d compactBoundary}", d, bndTS.UnixMilli())
	}
	if p.Messages[2].CompactBoundary || p.Messages[2].Text != "ub" {
		t.Errorf("user+boundary-subtype row = %+v, want a normal rendered message", p.Messages[2])
	}
	if p.Messages[3].CompactBoundary || p.Messages[3].Text != "sysinfo" {
		t.Errorf("system informational row = %+v, want a normal rendered message", p.Messages[3])
	}
	done := p.Messages[4].Tools[0]
	if len(p.Messages[4].Tools) != 1 {
		t.Fatalf("tool entries on r4 = %d, want 1", len(p.Messages[4].Tools))
	}
	if done.IsRunning || !done.IsError || done.DurationNs != 99 || done.DisplayOutput != `"RICH"` {
		t.Errorf("resolved tool entry = %+v, want running=false isError=true duration=99 rich output", done)
	}
	if len(p.Messages[6].Tools) != 1 || !p.Messages[6].Tools[0].IsRunning {
		t.Errorf("unresolved tool entry = %+v, want IsRunning (text blocks are not results)", p.Messages[6].Tools)
	}
	if len(p.Messages[8].Tools) != 1 || !p.Messages[8].Tools[0].IsRunning {
		t.Errorf("empty-id tool entry = %+v, want IsRunning (empty-keyed results are not collected)", p.Messages[8].Tools)
	}
	if !p.HasMore || p.NextCursor != "precompact:9" {
		t.Errorf("hasMore/nextCursor = %v/%q, want true/precompact:9", p.HasMore, p.NextCursor)
	}
}

// ---------------------------------------------------------------------------
// formatToolDisplayName / historyAgentToolName (connector.go:2188-2216)
// ---------------------------------------------------------------------------

func TestMutA2FormatToolDisplayName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Grep", "Grep"},
		{"a__b__c", "a__b__c"},
		{"mcp__srv__tool", "srv - tool (MCP)"},
		{"mcp__a__b__c", "a - b__c (MCP)"},
		{"mcp__lonely", "mcp__lonely"},
	}
	for _, tc := range cases {
		if got := formatToolDisplayName(tc.in); got != tc.want {
			t.Errorf("formatToolDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMutA2HistoryAgentToolName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		// Non-Agent tools never grow a subagent suffix.
		{"Grep", `{"subagent_type":"Explore"}`, "Grep"},
		{"Agent", `{}`, "Agent"},
		{"Agent", `{"subagent_type":"fork"}`, "Agent"},
		{"Agent", `{"subagent_type":"Explore"}`, "Agent Explore"},
		// Unmarshal error (bool field fed a string) must win over the
		// partially decoded subagent_type.
		{"Agent", `{"subagent_type":"x","fork":"notabool"}`, "Agent"},
	}
	for _, tc := range cases {
		got := historyAgentToolName(tc.name, json.RawMessage(tc.input))
		if got != tc.want {
			t.Errorf("historyAgentToolName(%q, %s) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// computeToolSummary / renderToolOutput / renderViaTool / persisted helpers
// (connector.go:2219-2347)
// ---------------------------------------------------------------------------

func TestMutA2ComputeToolSummary(t *testing.T) {
	if got := computeToolSummary("NoSuchTool", json.RawMessage(`{}`), nil); got != "" {
		t.Errorf("summary for unknown tool = %q, want empty", got)
	}
	tools := map[string]tool.Tool{"Grep": &mutA2Tool{name: "Grep", desc: "SUM-G"}}
	if got := computeToolSummary("Grep", json.RawMessage(`{}`), tools); got != "SUM-G" {
		t.Errorf("summary = %q, want SUM-G", got)
	}
	errTools := map[string]tool.Tool{"E": &mutA2Tool{name: "E", descErr: true}}
	if got := computeToolSummary("E", json.RawMessage(`{}`), errTools); got != "" {
		t.Errorf("summary on Description error = %q, want empty", got)
	}
}

func TestMutA2RenderToolOutputEmptyRaw(t *testing.T) {
	if got := renderToolOutput("X", nil, nil); got != "" {
		t.Errorf("renderToolOutput(nil) = %q, want empty", got)
	}
	// A tool that renders everything must still not see empty input: the
	// empty-raw guard fires before any render path.
	tools := map[string]tool.Tool{"X": &mutA2Tool{name: "X", render: func(any) string { return "R" }}}
	if got := renderToolOutput("X", nil, tools); got != "" {
		t.Errorf("renderToolOutput(nil, with tool) = %q, want empty", got)
	}
	// A 1-byte raw payload is past the empty guard and falls back to raw text.
	if got := renderToolOutput("X", json.RawMessage("x"), nil); got != "x" {
		t.Errorf("renderToolOutput(1 byte) = %q, want x", got)
	}
}

func TestMutA2RenderToolOutputTextExtraction(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		note string
	}{
		{`[{"type":"text","text":"hello"},{"type":"text","text":"world"}]`, "hello\nworld", "joined text blocks"},
		{`[{"type":"image","text":"leak"}]`, `[{"type":"image","text":"leak"}]`, "non-text block is not extracted"},
		{`[{"type":"text","text":""}]`, `[{"type":"text","text":""}]`, "empty-text block is not extracted"},
		// Unmarshal type error mid-array: element 0 decodes but the error
		// must force the raw fallback.
		{`[{"type":"text","text":"a"},{"type":123}]`, `[{"type":"text","text":"a"},{"type":123}]`, "type error forces raw fallback"},
		{`[{"type":"image"}]`, `[{"type":"image"}]`, "no text at all"},
	}
	for _, tc := range cases {
		if got := renderToolOutput("X", json.RawMessage(tc.raw), nil); got != tc.want {
			t.Errorf("renderToolOutput(%s): got %q, want %q (%s)", tc.note, got, tc.want, tc.note)
		}
	}
}

func TestMutA2RenderViaToolStringForm(t *testing.T) {
	tools := map[string]tool.Tool{
		"X": &mutA2Tool{name: "X", render: func(data any) string { return "R:" + data.(string) }},
	}
	raw := json.RawMessage(`[{"type":"text","text":"payload"}]`)
	got, ok := renderViaTool("X", raw, tools)
	if !ok {
		t.Fatal("renderViaTool(string-form tool) ok = false, want true")
	}
	if got != "R:"+string(raw) {
		t.Errorf("renderViaTool = %q, want R:%s", got, raw)
	}
}

func TestMutA2ReadPersistedFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(filePath, []byte("FILECONTENT"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got := readPersistedFile("no marker here"); got != nil {
		t.Errorf("readPersistedFile(no marker) = %s, want nil", got)
	}
	got := readPersistedFile("Full output saved to: " + filePath + "\nPreview (first 5 lines):\nx")
	if string(got) != "FILECONTENT" {
		t.Errorf("readPersistedFile = %q, want FILECONTENT", got)
	}
	// Path on the line AFTER the marker: an empty first line means no path,
	// even though a path follows on the next line.
	if got := readPersistedFile("Full output saved to: \n" + filePath + "\n"); got != nil {
		t.Errorf("readPersistedFile(newline-first) = %q, want nil (empty path segment)", got)
	}
}

func TestMutA2ExtractPersistedPreview(t *testing.T) {
	// Exactly 5 lines: no ellipsis is appended.
	five := "Preview (first 5 lines):\nl1\nl2\nl3\nl4\nl5"
	if got := extractPersistedPreview(five); got != "l1\nl2\nl3\nl4\nl5" {
		t.Errorf("preview(5 lines) = %q, want l1..l5 with no ... suffix", got)
	}
	// 6 lines: first 5 plus ellipsis.
	six := "Preview (first 5 lines):\nl1\nl2\nl3\nl4\nl5\nl6"
	if got := extractPersistedPreview(six); got != "l1\nl2\nl3\nl4\nl5\n..." {
		t.Errorf("preview(6 lines) = %q, want l1..l5 + ...", got)
	}
}

// ---------------------------------------------------------------------------
// agentTypeLog / parentIDLog (connector.go:2439-2451)
// ---------------------------------------------------------------------------

func TestMutA2AgentMetaLogs(t *testing.T) {
	if got := agentTypeLog(nil); got != "" {
		t.Errorf("agentTypeLog(nil) = %q, want empty", got)
	}
	if got := agentTypeLog(&types.AgentMeta{AgentType: "Explore"}); got != "Explore" {
		t.Errorf("agentTypeLog = %q, want Explore", got)
	}
	if got := parentIDLog(nil); got != "" {
		t.Errorf("parentIDLog(nil) = %q, want empty", got)
	}
	if got := parentIDLog(&types.AgentMeta{ParentToolUseID: "tu9"}); got != "tu9" {
		t.Errorf("parentIDLog = %q, want tu9", got)
	}
}

// ---------------------------------------------------------------------------
// buildTaskList (connector.go:2475-2537)
// ---------------------------------------------------------------------------

func TestMutA2BuildTaskListNilSlot(t *testing.T) {
	c := mutA2BareConnector()
	if got := c.buildTaskList(nil); got != nil {
		t.Errorf("buildTaskList(nil) = %.40s, want nil", got)
	}
}

func TestMutA2BuildTaskListListError(t *testing.T) {
	c := mutA2BareConnector()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A file where the task dir should be: ReadDir fails with ENOTDIR.
	mock := &mockEngine{}
	mock.taskListFn = func() *task.List { return task.NewList(blocker) }
	if got := c.buildTaskList(mutA2Slot(mock)); got != nil {
		t.Errorf("buildTaskList(ListTasks error) = %.40s, want nil", got)
	}
}

func TestMutA2BuildTaskListFilters(t *testing.T) {
	c := mutA2BareConnector()
	tl := task.NewList(t.TempDir())
	if err := tl.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	id1, err := tl.CreateTask("done-task", "d", "", nil)
	if err != nil {
		t.Fatalf("CreateTask 1: %v", err)
	}
	completed := task.StatusCompleted
	if _, _, err := tl.UpdateTask(id1, task.TaskUpdates{Status: &completed}); err != nil {
		t.Fatalf("UpdateTask 1: %v", err)
	}
	id2, err := tl.CreateTask("visible", "d", "working hard", map[string]any{"note": "plain"})
	if err != nil {
		t.Fatalf("CreateTask 2: %v", err)
	}
	owner := "agent-2"
	if _, _, err := tl.UpdateTask(id2, task.TaskUpdates{Owner: &owner}); err != nil {
		t.Fatalf("UpdateTask 2: %v", err)
	}
	id3, err := tl.CreateTask("internal", "d", "", map[string]any{"_internal": true})
	if err != nil {
		t.Fatalf("CreateTask 3: %v", err)
	}
	id4, err := tl.CreateTask("blocker-task", "d", "", nil)
	if err != nil {
		t.Fatalf("CreateTask 4: %v", err)
	}
	if !tl.BlockTask(id4, id2) {
		t.Fatal("BlockTask(4, 2) = false")
	}

	mock := &mockEngine{}
	mock.taskListFn = func() *task.List { return tl }
	payload := c.buildTaskList(mutA2Slot(mock))
	if payload == nil {
		t.Fatal("payload = nil, want task_list frame")
	}
	var frame taskListOutbound
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.Type != "task_list" {
		t.Errorf("type = %q, want task_list", frame.Type)
	}
	if len(frame.Tasks) != 3 {
		t.Fatalf("tasks = %d (%+v), want 3 (internal excluded, metadata-without-key kept)", len(frame.Tasks), frame.Tasks)
	}
	var t2 *taskListWireItem
	for i := range frame.Tasks {
		if frame.Tasks[i].Subject == "visible" {
			t2 = &frame.Tasks[i]
		}
	}
	if t2 == nil {
		t.Fatal("visible task missing from wire payload")
	}
	if t2.Owner != owner {
		t.Errorf("owner = %q, want %q", t2.Owner, owner)
	}
	if t2.ActiveForm != "working hard" {
		t.Errorf("activeForm = %q, want working hard", t2.ActiveForm)
	}
	if len(t2.BlockedBy) != 1 || t2.BlockedBy[0] != "blocker-task" {
		t.Errorf("blockedBy = %v, want [blocker-task] (pending blocker, resolved to subject)", t2.BlockedBy)
	}
	// A pending task must suppress the completed-cleanup: all four tasks
	// remain on disk after the payload is built.
	after, err := tl.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(after) != 4 {
		t.Errorf("tasks on disk after buildTaskList = %d, want 4 (cleanup must not run)", len(after))
	}
	// id3's internal task survives on disk yet must stay absent from the
	// wire payload: resolve its subject through the ID, then scan the frame.
	var internalSubject string
	for _, tk := range after {
		if tk.ID == id3 {
			internalSubject = tk.Subject
		}
	}
	if internalSubject != "internal" {
		t.Fatalf("task %s on disk has subject %q, want internal", id3, internalSubject)
	}
	for i := range frame.Tasks {
		if frame.Tasks[i].Subject == internalSubject {
			t.Errorf("internal task leaked into the wire payload: %+v", frame.Tasks[i])
		}
	}
}

// ---------------------------------------------------------------------------
// buildConfig (connector.go:2560-2585)
// ---------------------------------------------------------------------------

func TestMutA2BuildConfig(t *testing.T) {
	var provCfg config.Provider
	raw := `{"name":"zhipu","url":"https://z.example","keys":["k"],"models":{"glm-4.6":{},"glm-4.5":{}}}`
	if err := json.Unmarshal([]byte(raw), &provCfg); err != nil {
		t.Fatalf("unmarshal provider: %v", err)
	}
	c := mutA2BareConnector()
	c.providerConfigs = map[string]*config.Provider{"zhipu": &provCfg}
	fp := &mutA2LLMProvider{name: "zhipu"}
	c.providers = map[string]llm.Provider{"zhipu": fp}

	mock := &mockEngine{}
	mock.providerFn = func() llm.Provider { return fp }
	mock.modelFn = func() string { return "glm-4.6" }
	mock.thinkingFn = func() llm.Effort { return llm.EffortHigh }

	payload := c.buildConfig(mutA2Slot(mock))
	if payload == nil {
		t.Fatal("payload = nil")
	}
	var frame struct {
		Type   string `json:"type"`
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
		Current struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"current"`
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("unmarshal config: %v (%.200s)", err, payload)
	}
	if frame.Type != "config" {
		t.Errorf("type = %q, want config", frame.Type)
	}
	if len(frame.Models) != 2 {
		t.Fatalf("models = %+v, want 2 entries", frame.Models)
	}
	if frame.Models[0].Provider != "zhipu" || frame.Models[0].Model != "glm-4.6" {
		t.Errorf("models[0] = %+v, want zhipu/glm-4.6 (config order)", frame.Models[0])
	}
	if frame.Models[1].Provider != "zhipu" || frame.Models[1].Model != "glm-4.5" {
		t.Errorf("models[1] = %+v, want zhipu/glm-4.5", frame.Models[1])
	}
	if frame.Current.Provider != "zhipu" || frame.Current.Model != "glm-4.6" {
		t.Errorf("current = %+v, want zhipu/glm-4.6", frame.Current)
	}
	if frame.Thinking != "high" {
		t.Errorf("thinking = %q, want high", frame.Thinking)
	}

	// No provider configs at all: models must still marshal as [], not null,
	// so the client's array handling does not break on a fresh install.
	empty := mutA2BareConnector()
	empty.providerConfigs = map[string]*config.Provider{}
	mock2 := &mockEngine{}
	mock2.modelFn = func() string { return "m" }
	payload2 := empty.buildConfig(mutA2Slot(mock2))
	var rawCfg map[string]json.RawMessage
	if err := json.Unmarshal(payload2, &rawCfg); err != nil {
		t.Fatalf("unmarshal empty config: %v", err)
	}
	if string(rawCfg["models"]) != "[]" {
		t.Errorf("models with no providers = %s, want []", rawCfg["models"])
	}
}

// ---------------------------------------------------------------------------
// buildQuota (connector.go:2590-2620)
// ---------------------------------------------------------------------------

func TestMutA2BuildQuotaNoWS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitor/usage/quota/limit" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":40,"nextResetTime":4102444800000}]},"msg":""}`))
	}))
	t.Cleanup(srv.Close)

	var provCfg config.Provider
	raw := fmt.Sprintf(`{"name":"zhipu","url":%q,"keys":["k"]}`, srv.URL)
	if err := json.Unmarshal([]byte(raw), &provCfg); err != nil {
		t.Fatalf("unmarshal provider: %v", err)
	}
	c := mutA2BareConnector()
	c.providerConfigs = map[string]*config.Provider{"zhipu": &provCfg}

	// No WS connected: even a fetchable quota must not enqueue a frame.
	c.buildQuota()
	if got, want := c.outQ.Load().len(), 0; got != want {
		t.Errorf("outQ len after buildQuota without WS = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// nonHubDispatcherA3 is an EventDispatcher that is not a *hub.Hub, used to
// exercise registerEngine's dispatcher-type guard.
type nonHubDispatcherA3 struct{}

func (nonHubDispatcherA3) Dispatch(types.QueryEvent) {}

func newMockSlotA3(id string, m engineClient) *engineSlot {
	return &engineSlot{
		engineID:    id,
		engine:      m,
		taskToolIDs: map[string]bool{},
		jobView:     map[string]*jobViewEntry{},
		streams:     map[string]*streamState{"": {}},
	}
}

// bareConnectorA3 builds a WUIConnector with NO background goroutines (no
// wsWriter, no tail loop): outbound frames stay in the queue so tests can
// inspect them deterministically via takeAllFramesA3.
func bareConnectorA3(t *testing.T, m *mockEngine) (*WUIConnector, *mockEngine) {
	t.Helper()
	if m == nil {
		m = &mockEngine{projectDir: t.TempDir()}
	}
	c := &WUIConnector{
		slots:       map[string]*engineSlot{},
		pendingAsks: map[string]*types.AskEvent{},
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	activeID := "main"
	c.active.Store(&activeID)
	slot := newMockSlotA3("main", m)
	slot.active.Store(true)
	c.slots["main"] = slot
	return c, m
}

func takeAllFramesA3(c *WUIConnector) []string {
	var out []string
	for _, f := range c.outQ.Load().take() {
		out = append(out, string(f.data))
	}
	return out
}

func frameJSONFieldA3(t *testing.T, frame, field string, dst any) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(frame), &top); err != nil {
		t.Fatalf("unmarshal frame %s: %v", frame, err)
	}
	raw, ok := top[field]
	if !ok || len(raw) == 0 {
		t.Fatalf("frame %s has no %q field", frame, field)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal %s.%s: %v", field, field, err)
	}
}

// msgRecorderA3 records every Info-or-higher slog record's message so tests
// can assert exact log lines (warn-once semantics, info milestones).
type msgRecorderA3 struct {
	mu   sync.Mutex
	msgs []string
}

func (h *msgRecorderA3) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (h *msgRecorderA3) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}

func (h *msgRecorderA3) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *msgRecorderA3) WithGroup(string) slog.Handler { return h }

func (h *msgRecorderA3) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

func swapMsgRecorderA3(t *testing.T) *msgRecorderA3 {
	t.Helper()
	h := &msgRecorderA3{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return h
}

// readWSFrameDeadlineA3 reads one WS message, returning (nil, false) on
// timeout instead of failing — callers decide whether absence is an error.
func readWSFrameDeadlineA3(t *testing.T, ws *websocket.Conn, d time.Duration) ([]byte, bool) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(d)) // REAL-TIME
	_, data, err := ws.ReadMessage()
	if err != nil {
		return nil, false
	}
	return data, true
}

func docBlockA3(path string) types.ContentBlock {
	return types.ContentBlock{Type: types.ContentTypeDocument, Name: "n.pdf", Path: path, Mime: "application/pdf", Size: 10}
}

func fileImageBlockA3(path, mediaType string) types.ContentBlock {
	return types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: mediaType, Path: path}}
}

func b64ImageBlockA3(mediaType, data string) types.ContentBlock {
	return types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", MediaType: mediaType, Data: data}}
}

func savedOfA3(t *testing.T, c *WUIConnector, id string) savedAttachment {
	t.Helper()
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()
	saved, ok := c.restoredUploads[id]
	if !ok {
		t.Fatalf("no restoredUploads entry for %q", id)
	}
	return saved
}

// withTimeoutA3 runs fn in a goroutine and fatals instead of hanging when a
// mutant strands a mutex (hub.Dispatch recovers panics, so a mutant can leave
// a production lock held forever — the kill must still be fast).
func withTimeoutA3(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s blocked (lock stranded by an earlier call?)", name)
	}
}

// buildRestoredMsgA3 is buildRestoredMsg behind withTimeoutA3: mutants that
// strand restoreMu (rememberUpload defer-removal) would otherwise hang the
// test instead of failing it.
func buildRestoredMsgA3(t *testing.T, c *WUIConnector, item types.QueuedItem) queuedMsgJSON {
	t.Helper()
	ch := make(chan queuedMsgJSON, 1)
	withTimeoutA3(t, "buildRestoredMsg", func() { ch <- c.buildRestoredMsg(item) })
	return <-ch
}

// ---------------------------------------------------------------------------
// thumbCache (lines 291-326)
// ---------------------------------------------------------------------------

func TestMutA3_ThumbCacheCapEviction(t *testing.T) {
	tc := newThumbCache()
	for i := 0; i < 101; i++ {
		tc.put(fmt.Sprintf("k%d", i), "v")
	}
	if _, ok := tc.m["k0"]; ok {
		t.Fatal("k0 must be evicted once the cache exceeds its cap of 100")
	}
	if got := len(tc.order); got != 100 {
		t.Fatalf("order length = %d, want 100", got)
	}
	if _, ok := tc.m["k100"]; !ok {
		t.Fatal("k100 must still be cached")
	}
	// Move-to-back: re-putting k1 refreshes it so the next eviction drops k2.
	tc.put("k1", "v2")
	tc.put("kNew", "v")
	if _, ok := tc.m["k1"]; !ok {
		t.Fatal("re-put key was evicted before older keys")
	}
	if _, ok := tc.m["k2"]; ok {
		t.Fatal("k2 (front of FIFO after k1 refresh) should have been evicted")
	}
	if got := tc.m["k1"]; got != "v2" {
		t.Fatalf("re-put value = %q, want v2", got)
	}
}

func TestMutA3_ThumbCacheMutexRelease(t *testing.T) {
	tc := newThumbCache()
	if v, ok := tc.get("a"); ok || v != "" {
		t.Fatalf("get on empty cache = (%q,%v), want (\"\",false)", v, ok)
	}
	if !tc.mu.TryLock() {
		t.Fatal("get() did not release tc.mu")
	}
	tc.mu.Unlock()

	tc.put("a", "1")
	if !tc.mu.TryLock() {
		t.Fatal("put() did not release tc.mu")
	}
	tc.mu.Unlock()
}

// ---------------------------------------------------------------------------
// outQueue (lines 431-527)
// ---------------------------------------------------------------------------

func TestMutA3_OutQueueCloseIdempotent(t *testing.T) {
	q := newOutQueue(2)
	q.push(wsMsg{data: []byte("x")}, nil)
	q.close()
	withTimeoutA3(t, "second close", q.close) // must be a no-op, not a panic or a stall
	var frames []wsMsg
	withTimeoutA3(t, "take after close", func() { frames = q.take() })
	if frames != nil {
		t.Fatalf("take after close returned %d frames, want nil", len(frames))
	}
	if n := q.len(); n != 0 {
		t.Fatalf("len after close = %d, want 0", n)
	}
}

func TestMutA3_OutQueueMutexRelease(t *testing.T) {
	q := newOutQueue(2)
	q.push(wsMsg{data: []byte("x")}, nil)
	if !q.mu.TryLock() {
		t.Fatal("push() did not release q.mu")
	}
	q.mu.Unlock()

	q.take()
	if !q.mu.TryLock() {
		t.Fatal("take() did not release q.mu")
	}
	q.mu.Unlock()

	q.isPristine()
	if !q.mu.TryLock() {
		t.Fatal("isPristine() did not release q.mu")
	}
	q.mu.Unlock()

	q.len()
	if !q.mu.TryLock() {
		t.Fatal("len() did not release q.mu")
	}
	q.mu.Unlock()

	q.close()
	if !q.mu.TryLock() {
		t.Fatal("close() did not release q.mu")
	}
	q.mu.Unlock()
}

func TestMutA3_OutQueueWarnsOncePerBlockingEpisode(t *testing.T) {
	load, restore := swapWarnHandler(t)
	defer restore()

	q := newOutQueue(1)
	q.push(wsMsg{data: []byte("f1")}, nil)
	pushed := make(chan struct{})
	go func() {
		defer close(pushed)
		q.push(wsMsg{data: []byte("f2")}, nil)
	}()
	if !waitFor(2*time.Second, func() bool { return load() == 1 }) {
		t.Fatalf("expected exactly 1 warn while blocked, got %d", load())
	}
	// Spurious room wakeup while the queue is still full: the pusher must
	// re-check, re-block, and NOT warn again in the same episode.
	q.mu.Lock()
	q.signalLocked(q.room)
	q.mu.Unlock()
	if waitFor(500*time.Millisecond, func() bool { return load() >= 2 }) {
		t.Fatalf("warn fired more than once in one blocking episode (count=%d)", load())
	}
	q.close()
	<-pushed
}

// ---------------------------------------------------------------------------
// New() constructor (lines 633-651)
// ---------------------------------------------------------------------------

func TestMutA3_NewConstructorFields(t *testing.T) {
	mgr := engine.NewEngineManager()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(h)
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Display", Engine: eng})

	providers := map[string]llm.Provider{"zhipu": &stubLLMProvider{name: "zhipu"}}
	cfg := &config.Provider{Name: "zhipu"}
	cfg.Models.Set("glm-5.2", config.ModelConfig{})
	configs := map[string]*config.Provider{"zhipu": cfg}

	c := New(mgr, providers, configs)
	t.Cleanup(c.Stop)

	if c.mgr != mgr {
		t.Fatal("New did not bind the EngineManager")
	}
	if c.ActiveID() != "main" {
		t.Fatalf("ActiveID = %q, want main", c.ActiveID())
	}
	if got := c.engineNameForSlot(c.activeSlot()); got != "Display" {
		t.Fatalf("engineNameForSlot = %q, want Display (from manager)", got)
	}
	if len(c.providers) != 1 || c.providers["zhipu"] == nil {
		t.Fatalf("providers = %#v, want the zhipu stub", c.providers)
	}
	if len(c.providerConfigs) != 1 {
		t.Fatalf("providerConfigs = %#v, want the zhipu config", c.providerConfigs)
	}
	if c.thumbs == nil {
		t.Fatal("New did not install a thumbnail cache")
	}
	if _, ok := c.thumbs.get("k"); ok {
		t.Fatal("fresh thumb cache must miss")
	}

	// pendingAsks map must be initialized: dispatching an ask stores it.
	// hub.Dispatch recovers panics, so a nil map strands pendingMu — poll the
	// lock instead of blocking on it.
	h.Dispatch(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{ToolName: "Bash", Message: "allow?"}})
	stored := false
	if !waitFor(2*time.Second, func() bool {
		if !c.pendingMu.TryLock() {
			return false
		}
		defer c.pendingMu.Unlock()
		stored = len(c.pendingAsks) == 1
		return true
	}) {
		t.Fatal("pendingMu never released after ask dispatch (nil pendingAsks map panicked under recover?)")
	}
	if !stored {
		t.Fatalf("pendingAsks after ask = 0, want 1")
	}
}

// ---------------------------------------------------------------------------
// wsWriter (lines 667-713)
// ---------------------------------------------------------------------------

// TestMutA3_WsWriterEraCheckDropsStaleBatch pins the per-frame era check: a
// batch taken from the old queue must stop writing once the era flips, even
// mid-batch. The writer is stalled on a huge frame (client never reads), the
// era is flipped underneath it, then the client drains: only the in-flight
// big frame may arrive — the queued-behind marker must be dropped.
func TestMutA3_WsWriterEraCheckDropsStaleBatch(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)

	connCh := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- ws
		<-r.Context().Done() // hold the conn open without ever reading
	}))
	t.Cleanup(srv.Close)
	wsClient := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/")
	serverConn := <-connCh
	t.Cleanup(func() { _ = serverConn.Close() })

	c.activeWS.Store(serverConn)
	big := bytes.Repeat([]byte{0xAB}, 48<<20)
	q1 := c.outQ.Load()
	q1.push(wsMsg{data: big, isBinary: true}, c.done)
	q1.push(wsMsg{data: []byte(`{"type":"stale-marker"}`)}, c.done)
	go c.wsWriter()

	// Let the writer take the batch and stall on the 48MB write.
	time.Sleep(300 * time.Millisecond) // REAL-TIME

	// Era flip (the queue half of a takeover).
	q2 := newOutQueue(outQueueCapacity)
	c.outQ.Store(q2)
	q1.close()

	_ = wsClient.SetReadDeadline(time.Now().Add(30 * time.Second)) // REAL-TIME
	_, data, err := wsClient.ReadMessage()
	if err != nil {
		t.Fatalf("read big frame: %v", err)
	}
	if len(data) != len(big) {
		t.Fatalf("big frame truncated: %d bytes, want %d", len(data), len(big))
	}
	if _, ok := readWSFrameDeadlineA3(t, wsClient, 500*time.Millisecond); ok {
		t.Fatal("stale-era frame was delivered after the era flip (per-frame era check broken)")
	}

	close(c.done)
}

// TestMutA3_WsWriterWriteFailureWarns pins the warn on WS write failure: the
// writer must log when WriteMessage errors (closed conn installed as the
// active WS).
func TestMutA3_WsWriterWriteFailureWarns(t *testing.T) {
	load, restore := swapWarnHandler(t)
	defer restore()

	c, _ := bareConnectorA3(t, nil)
	connCh := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- ws
		_ = ws.Close() // close immediately: writes must fail
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	_ = dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/")
	serverConn := <-connCh

	c.activeWS.Store(serverConn)
	go c.wsWriter()
	c.sendWS([]byte(`{"a":1}`))
	if !waitFor(3*time.Second, func() bool { return load() >= 1 }) {
		t.Fatal("write failure was not logged")
	}
	close(c.done)
}

// ---------------------------------------------------------------------------
// activeSlot / registerEngine (lines 741-835)
// ---------------------------------------------------------------------------

func TestMutA3_ActiveSlotMutexRelease(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	if c.activeSlot() == nil {
		t.Fatal("activeSlot returned nil for main")
	}
	if !c.slotsMu.TryLock() {
		t.Fatal("activeSlot() did not release slotsMu")
	}
	c.slotsMu.Unlock()
}

func TestMutA3_RegisterEngineNilEnginePanics(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	panicked, msg := false, ""
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				msg = fmt.Sprint(r)
			}
		}()
		c.registerEngine(&engine.EngineViewState{ID: "x"})
	}()
	if !panicked {
		t.Fatal("registerEngine with nil Engine must panic")
	}
	// The explicit guard must fire BEFORE the nil Engine deref: a nil-deref
	// panic would mean the guard was skipped.
	if !strings.Contains(msg, "registerEngine called with nil Engine") {
		t.Fatalf("panic message = %q, want the explicit nil-Engine guard panic", msg)
	}
}

func TestMutA3_RegisterEngineTwiceIgnored(t *testing.T) {
	mgr := engine.NewEngineManager()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "e1"})
	eng.SetDispatcher(h)
	vs := &engine.EngineViewState{ID: "e1", Name: "e1", Engine: eng}
	mgr.Add(vs)

	rec := swapMsgRecorderA3(t)
	c := New(mgr, nil, nil)
	// Stop runs through withTimeoutA3 at the end (not t.Cleanup): a mutant
	// that strands slotsMu must fail the assertions, not hang the cleanup.
	n0 := -1
	withTimeoutA3(t, "streamStateCount", func() { n0 = streamStateCount(c, "e1") })

	c.registerEngine(vs)
	if got := rec.count("wui: registerEngine called twice for same engine, ignoring"); got != 1 {
		t.Fatalf("double-register warn count = %d, want 1", got)
	}
	n1 := -1
	withTimeoutA3(t, "streamStateCount", func() { n1 = streamStateCount(c, "e1") })
	h.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"}})
	n2 := -1
	withTimeoutA3(t, "streamStateCount", func() { n2 = streamStateCount(c, "e1") })
	if n0 < 0 || n2-n1 != 1 {
		t.Fatalf("tool blocks after one dispatch = %d, want 1 (double subscription)", n2-n1)
	}
	withTimeoutA3(t, "Stop", c.Stop)
}

func TestMutA3_RegisterEngineNonHubDispatcherSkipped(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	eng := engine.New(&engine.Params{EngineID: "e2"})
	eng.SetDispatcher(nonHubDispatcherA3{})

	rec := swapMsgRecorderA3(t)
	c.registerEngine(&engine.EngineViewState{ID: "e2", Name: "e2", Engine: eng})
	if got := rec.count("wui: engine dispatcher is not *hub.Hub, skipping"); got != 1 {
		t.Fatalf("skip warn count = %d, want 1", got)
	}
	c.slotsMu.RLock()
	_, exists := c.slots["e2"]
	c.slotsMu.RUnlock()
	if exists {
		t.Fatal("slot must not be created for a non-Hub dispatcher")
	}
}

func TestMutA3_ActiveSessionTitle(t *testing.T) {
	var limits []int
	m := &mockEngine{}
	m.sessionIDFn = func() string { return "" }
	m.listSessionsFn = func(limit int) ([]*short.Session, error) {
		limits = append(limits, limit)
		return []*short.Session{{SessionID: "s0", Title: "T0"}, {SessionID: "s1", Title: "T1"}}, nil
	}
	if got := activeSessionTitle(m); got != "" {
		t.Fatalf("empty session id: title = %q, want empty", got)
	}
	if len(limits) != 0 {
		t.Fatalf("ListSessions called %d times for empty session id, want 0", len(limits))
	}

	m.sessionIDFn = func() string { return "s1" }
	if got := activeSessionTitle(m); got != "T1" {
		t.Fatalf("title = %q, want T1 (exact session match)", got)
	}
	if len(limits) != 1 || limits[0] != 50 {
		t.Fatalf("ListSessions limits = %v, want [50]", limits)
	}

	m.listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{nil, {SessionID: "s1", Title: "T1"}}, nil
	}
	if got := activeSessionTitle(m); got != "T1" {
		t.Fatalf("title with nil session entry = %q, want T1", got)
	}

	m.listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{{SessionID: "s1", Title: "T1"}}, errors.New("store down")
	}
	if got := activeSessionTitle(m); got != "" {
		t.Fatalf("title on ListSessions error = %q, want empty", got)
	}
}

func TestMutA3_InFlightPromptExcerpt(t *testing.T) {
	mk := func(start int, msgs ...types.Message) *mockEngine {
		return &mockEngine{
			messagesFn:         func() []types.Message { return msgs },
			queryStartMsgIdxFn: func() int { return start },
		}
	}
	// Negative start clamps to 0 (not 1): the first message is still scanned.
	m := mk(-1,
		types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}},
		types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a")}},
	)
	if got := inFlightPromptExcerpt(m); got != "first" {
		t.Fatalf("excerpt = %q, want first", got)
	}
	// Start beyond the message count clamps instead of slicing out of range.
	m = mk(5, types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("a")}})
	if got := inFlightPromptExcerpt(m); got != "" {
		t.Fatalf("excerpt with start past end = %q, want empty", got)
	}
	// Assistant messages and text-less user messages are skipped; a later
	// user prompt is still found.
	m = mk(0,
		types.Message{Role: types.RoleUser, Content: nil},
		types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("as")}},
		types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("real")}},
	)
	if got := inFlightPromptExcerpt(m); got != "real" {
		t.Fatalf("excerpt = %q, want real", got)
	}
}

func TestMutA3_TruncateRunesBoundary(t *testing.T) {
	s80 := strings.Repeat("a", 80)
	if got := truncateRunes(s80, 80); got != s80 {
		t.Fatalf("exact-length string was modified: %q", got)
	}
	s81 := strings.Repeat("b", 81)
	if got, want := truncateRunes(s81, 80), strings.Repeat("b", 80)+"…"; got != want {
		t.Fatalf("truncate(81) = %q, want %q", got, want)
	}
	cjk := strings.Repeat("中", 81)
	if got, want := truncateRunes(cjk, 80), strings.Repeat("中", 80)+"…"; got != want {
		t.Fatalf("truncate(cjk 81) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// buildQuota / formatQuota (lines 2590-2643)
// ---------------------------------------------------------------------------

func TestMutA3_FormatQuota(t *testing.T) {
	now := time.Now() // REAL-TIME
	cases := []struct {
		name string
		info *quota.Info
		want string
	}{
		{"nil", nil, ""},
		{"25h", &quota.Info{Used: 40, ResetAt: now.Add(25 * time.Hour)}, "60%/1d"},
		// 24h minus scheduler drift lands at hours==23: still the h/m branch
		// (the day threshold is >= 24).
		{"24h", &quota.Info{Used: 40, ResetAt: now.Add(24 * time.Hour)}, "60%/23h59m"},
		{"37h", &quota.Info{Used: 40, ResetAt: now.Add(37 * time.Hour)}, "60%/1d"},
		{"49h", &quota.Info{Used: 40, ResetAt: now.Add(49 * time.Hour)}, "60%/2d"},
		{"47h", &quota.Info{Used: 40, ResetAt: now.Add(47 * time.Hour)}, "60%/1d"},
		{"2h17m", &quota.Info{Used: 40, ResetAt: now.Add(2*time.Hour + 17*time.Minute + 30*time.Second)}, "60%/2h17m"},
		{"1h30m", &quota.Info{Used: 40, ResetAt: now.Add(1*time.Hour + 30*time.Minute + 30*time.Second)}, "60%/1h30m"},
		{"17m", &quota.Info{Used: 40, ResetAt: now.Add(17*time.Minute + 30*time.Second)}, "60%/17m"},
		{"1m", &quota.Info{Used: 40, ResetAt: now.Add(1*time.Minute + 30*time.Second)}, "60%/1m"},
		{"59s", &quota.Info{Used: 40, ResetAt: now.Add(59 * time.Second)}, "60%/0m"},
		{"past", &quota.Info{Used: 40, ResetAt: now.Add(-time.Hour)}, "60%/0m"},
	}
	for _, tc := range cases {
		if got := formatQuota(tc.info); got != tc.want {
			t.Errorf("%s: formatQuota = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMutA3_BuildQuotaSendsResult(t *testing.T) {
	endAt := time.Now().Add(49 * time.Hour).UnixMilli() // REAL-TIME
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/token_plan/remains", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","end_time":%d,"current_interval_remaining_percent":40}]}`, endAt)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	configs := map[string]*config.Provider{
		"minimax-local": {Name: "minimax-local", URL: srv.URL, Keys: []string{"k"}},
		"plain":         {Name: "plain", URL: srv.URL, Keys: []string{"k"}},
	}
	c := newTestConnectorWithConfig(t, hub.NewHub(), nil, configs)
	ws := dialAndStore(t, c)

	// Map iteration order is random: repeat so order-dependent mutants
	// (continue→break skipping the detectable provider) cannot hide.
	for i := 0; i < 24; i++ {
		c.buildQuota()
		data, ok := readWSFrameDeadlineA3(t, ws, 2*time.Second)
		if !ok {
			t.Fatalf("iter %d: no quota_result frame (entries lost or fetch skipped)", i)
		}
		var msg struct {
			Type    string `json:"type"`
			Entries []struct {
				Provider string `json:"provider"`
				Quota    string `json:"quota"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("iter %d: unmarshal: %v (frame %s)", i, err, data)
		}
		if msg.Type != "quota_result" {
			t.Fatalf("iter %d: frame type = %q, want quota_result", i, msg.Type)
		}
		if len(msg.Entries) != 1 {
			t.Fatalf("iter %d: entries = %d, want 1 (only the minimax provider)", i, len(msg.Entries))
		}
		if msg.Entries[0].Provider != "minimax-local" || msg.Entries[0].Quota != "40%/2d" {
			t.Fatalf("iter %d: entry = %+v, want {minimax-local 40%%/2d}", i, msg.Entries[0])
		}
	}
}

func TestMutA3_BuildQuotaFetchErrorSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	// All providers failing: nothing is sent at all.
	configs := map[string]*config.Provider{
		"minimax-err": {Name: "minimax-err", URL: srv.URL, Keys: []string{"k"}},
	}
	c := newTestConnectorWithConfig(t, hub.NewHub(), nil, configs)
	ws := dialAndStore(t, c)
	c.buildQuota()
	if data, ok := readWSFrameDeadlineA3(t, ws, 500*time.Millisecond); ok {
		t.Fatalf("fetch error must not emit a frame, got %s", data)
	}
}

func TestMutA3_BuildQuotaMixedProviders(t *testing.T) {
	endAt := time.Now().Add(49 * time.Hour).UnixMilli() // REAL-TIME
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","end_time":%d,"current_interval_remaining_percent":40}]}`, endAt)
	}))
	t.Cleanup(good.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)

	configs := map[string]*config.Provider{
		"minimax-ok":  {Name: "minimax-ok", URL: good.URL, Keys: []string{"k"}},
		"minimax-bad": {Name: "minimax-bad", URL: bad.URL, Keys: []string{"k"}},
	}
	c := newTestConnectorWithConfig(t, hub.NewHub(), nil, configs)
	ws := dialAndStore(t, c)
	// Map iteration order is random: a continue→break at the failing
	// provider would skip the good one whenever "minimax-bad" iterates first.
	for i := 0; i < 24; i++ {
		c.buildQuota()
		data, ok := readWSFrameDeadlineA3(t, ws, 2*time.Second)
		if !ok {
			t.Fatalf("iter %d: no quota_result frame (good provider lost)", i)
		}
		var msg struct {
			Type    string `json:"type"`
			Entries []struct {
				Provider string `json:"provider"`
				Quota    string `json:"quota"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("iter %d: unmarshal: %v", i, err)
		}
		if len(msg.Entries) != 1 || msg.Entries[0].Provider != "minimax-ok" || msg.Entries[0].Quota != "40%/2d" {
			t.Fatalf("iter %d: entries = %+v, want exactly [{mini-ok 40%%/2d}]", i, msg.Entries)
		}
	}
}

// ---------------------------------------------------------------------------
// buildConnectStatus / buildStats / engineNameForSlot / buildSessionList
// (lines 2654-2780)
// ---------------------------------------------------------------------------

func TestMutA3_SendMetadataComposite(t *testing.T) {
	dir := t.TempDir()
	m := &mockEngine{projectDir: dir}
	msgs := make([]types.Message, 31)
	for i := range msgs {
		msgs[i] = types.Message{ID: fmt.Sprintf("msg-%d", i), Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("m%d", i))}}
	}
	m.messagesFn = func() []types.Message { return msgs }
	histDir := filepath.Join(dir, "history")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Third line is a consecutive duplicate and must be skipped on read.
	if err := os.WriteFile(filepath.Join(histDir, "main.jsonl"),
		[]byte("{\"display\":\"alpha\"}\n{\"display\":\"beta\"}\n{\"display\":\"beta\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, _ := bareConnectorA3(t, m)
	// A named manager makes engineID ("main") and engineName ("Mgr-Name")
	// independently observable in the connect sub-frame.
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Mgr-Name", Model: "m-x"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c.mgr = mgr
	slot := c.activeSlotTest(t)
	c.sendMetadata(slot)

	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("sendMetadata produced %d frames, want 1", len(frames))
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &head); err != nil || head.Type != "metadata" {
		t.Fatalf("frame head = %s (%v), want metadata", frames[0], err)
	}
	var connect struct {
		InputHistory []string `json:"inputHistory"`
		WuiHash      string   `json:"wuiHash"`
		EngineID     string   `json:"engineID"`
		EngineName   string   `json:"engineName"`
	}
	frameJSONFieldA3(t, frames[0], "connect", &connect)
	if len(connect.InputHistory) != 2 || connect.InputHistory[0] != "alpha" || connect.InputHistory[1] != "beta" {
		t.Fatalf("inputHistory = %v, want [alpha beta]", connect.InputHistory)
	}
	if connect.WuiHash == "" || connect.WuiHash != wuiAssetHash() {
		t.Fatalf("wuiHash = %q, want the live asset hash", connect.WuiHash)
	}
	if connect.EngineID != "main" {
		t.Fatalf("engineID = %q, want main", connect.EngineID)
	}
	if connect.EngineName != "Mgr-Name" {
		t.Fatalf("engineName = %q, want Mgr-Name", connect.EngineName)
	}
	var engines struct {
		Type    string `json:"type"`
		Engines []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"engines"`
		ActiveID string `json:"activeID"`
	}
	frameJSONFieldA3(t, frames[0], "engines", &engines)
	if engines.Type != "engine_list" {
		t.Fatalf("engines sub-frame type = %q, want engine_list", engines.Type)
	}
	if len(engines.Engines) != 1 {
		t.Fatalf("engines = %d entries, want 1", len(engines.Engines))
	}
	if e := engines.Engines[0]; e.ID != "main" || e.Name != "Mgr-Name" || e.Model != "m-x" {
		t.Fatalf("engine entry = %+v, want {main Mgr-Name m-x}", e)
	}
	if engines.ActiveID != "main" {
		t.Fatalf("engines activeID = %q, want main", engines.ActiveID)
	}
	var cfgHead struct {
		Type string `json:"type"`
	}
	frameJSONFieldA3(t, frames[0], "config", &cfgHead)
	if cfgHead.Type != "config" {
		t.Fatalf("config sub-frame type = %q, want config", cfgHead.Type)
	}
	// v2: history/snapshot are stripped; rev/busy replace them.
	var metaTail struct {
		History  json.RawMessage `json:"history"`
		Snapshot json.RawMessage `json:"snapshot"`
		Rev      string          `json:"rev"`
		Busy     bool            `json:"busy"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &metaTail); err != nil {
		t.Fatalf("unmarshal metadata tail fields: %v", err)
	}
	if metaTail.History != nil || metaTail.Snapshot != nil {
		t.Fatalf("metadata embeds history/snapshot (%.40s / %.40s), want both stripped", metaTail.History, metaTail.Snapshot)
	}
	if metaTail.Rev != "31:msg-30" || metaTail.Busy {
		t.Fatalf("metadata rev/busy = %q/%v, want 31:msg-30/false", metaTail.Rev, metaTail.Busy)
	}
	if !slot.active.Load() {
		t.Fatal("sendMetadata must activate the slot")
	}
}

func TestMutA3_BuildStats(t *testing.T) {
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("context prompt tokens")}}}
	m := &mockEngine{projectDir: t.TempDir()}
	m.messagesFn = func() []types.Message { return msgs }
	c, _ := bareConnectorA3(t, m)
	slot := c.activeSlotTest(t)

	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 11, OutputTokens: 22, CacheReadInputTokens: 33, CacheCreationInputTokens: 44}})
	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventUsage, Agent: &types.AgentMeta{JobID: "agent-9"}, Usage: &types.UsageEvent{InputTokens: 77}})
	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t", Name: "Bash"}})
	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 1500 * time.Millisecond}})
	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventQueryStart})

	var s struct {
		Type         string      `json:"type"`
		Usage        types.Usage `json:"usage"`
		QueryStartMs int64       `json:"queryStartMs"`
		ToolCount    int         `json:"toolCount"`
		ThinkingMs   int64       `json:"thinkingMs"`
		ContextUsed  int         `json:"contextUsed"`
		ContextTotal int         `json:"contextTotal"`
	}
	if err := json.Unmarshal(c.buildStats(slot), &s); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	if s.Type != "stats" {
		t.Fatalf("type = %q, want stats", s.Type)
	}
	if s.Usage.InputTokens != 11 || s.Usage.OutputTokens != 22 || s.Usage.CacheReadInputTokens != 33 || s.Usage.CacheCreationInputTokens != 44 {
		t.Fatalf("usage = %+v, want 11/22/33/44", s.Usage)
	}
	if s.ToolCount != 1 {
		t.Fatalf("toolCount = %d, want 1", s.ToolCount)
	}
	if s.ThinkingMs != 1500 {
		t.Fatalf("thinkingMs = %d, want 1500", s.ThinkingMs)
	}
	if s.QueryStartMs <= 0 {
		t.Fatalf("queryStartMs = %d, want > 0", s.QueryStartMs)
	}
	wantCtx := engine.TokenCountWithEstimation(msgs)
	if wantCtx == 0 {
		t.Fatal("test setup: estimation of the fixture messages must be non-zero")
	}
	if s.ContextUsed != wantCtx {
		t.Fatalf("contextUsed = %d, want %d (estimation fallback)", s.ContextUsed, wantCtx)
	}
	if s.ContextTotal != 200000 {
		t.Fatalf("contextTotal = %d, want 200000 (mock engine window)", s.ContextTotal)
	}
}

func TestMutA3_EngineNameForSlot(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Mgr-Name"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, &mockEngine{projectDir: t.TempDir()})
	slot := c.activeSlotTest(t)

	c.mgr = mgr
	if got := c.engineNameForSlot(slot); got != "Mgr-Name" {
		t.Fatalf("engineNameForSlot = %q, want Mgr-Name (from manager)", got)
	}
	empty := engine.NewEngineManager()
	c.mgr = empty
	if got := c.engineNameForSlot(slot); got != "main" {
		t.Fatalf("engineNameForSlot fallback = %q, want main (engine id)", got)
	}
}

func TestMutA3_BuildEngineList(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main-Name", Model: "m-1"})
	mgr.Add(&engine.EngineViewState{ID: "aux", Name: "Aux-Name", Model: "m-2"})
	if err := mgr.SetActive("aux"); err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, &mockEngine{projectDir: t.TempDir()})
	c.mgr = mgr

	payload := c.buildEngineList()
	if payload == nil {
		t.Fatal("buildEngineList returned nil")
	}
	var msg struct {
		Type    string `json:"type"`
		Engines []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"engines"`
		ActiveID string `json:"activeID"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "engine_list" {
		t.Fatalf("type = %q, want engine_list", msg.Type)
	}
	if len(msg.Engines) != 2 {
		t.Fatalf("engines = %d, want 2 (from manager snapshot)", len(msg.Engines))
	}
	byID := map[string]struct{ Name, Model string }{}
	for _, e := range msg.Engines {
		byID[e.ID] = struct{ Name, Model string }{e.Name, e.Model}
	}
	if got := byID["main"]; got.Name != "Main-Name" || got.Model != "m-1" {
		t.Fatalf("main entry = %+v", got)
	}
	if got := byID["aux"]; got.Name != "Aux-Name" || got.Model != "m-2" {
		t.Fatalf("aux entry = %+v", got)
	}
	if msg.ActiveID != "aux" {
		t.Fatalf("activeID = %q, want aux (from manager snapshot)", msg.ActiveID)
	}
}

// ---------------------------------------------------------------------------
// handleSessionSwitch / handleSessionNew (lines 2790-2875)
// ---------------------------------------------------------------------------

// Equivalent mutants, on record: the slot==nil guards at connector.go:2816
// and 2859 here, and 2969 in handleThinkingSwitch below, are unreachable
// under serial dispatch — activeEngine() already returned non-nil earlier in
// the same call, and slots are never deleted, so the second activeSlot()
// read can only miss inside the RegisterEngine concurrency window (active ID
// swapped between the two reads). A slot lost in that window nil-derefs on
// the next line with or without the guard, so no serial test can observe
// the branch.

func TestMutA3_HandleSessionSwitchPersist(t *testing.T) {
	dir := t.TempDir()
	m := &mockEngine{projectDir: dir}
	c, _ := bareConnectorA3(t, m)
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c.mgr = mgr

	rec := swapMsgRecorderA3(t)
	c.handleSessionSwitch("sess-2")
	if got, want := rec.count("wui:session switch:PersistMeta failed"), 0; got != want {
		t.Fatalf("warn on successful persist = %d, want %d", got, want)
	}
	if vs := mgr.Active(); vs == nil || vs.ActiveSessionID != "sess-2" {
		t.Fatalf("Active().ActiveSessionID = %+v, want sess-2", vs)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatalf("meta.json not persisted: %v", err)
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.projectDirFn = func() string { return blocked }
	c.handleSessionSwitch("sess-3")
	if got := rec.count("wui:session switch:PersistMeta failed"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
	if len(m.switchSessionCalls) != 2 {
		t.Fatalf("switchSessionCalls = %v, want two switches", m.switchSessionCalls)
	}
}

func TestMutA3_HandleSessionNew(t *testing.T) {
	// No active engine: quiet return, not a nil-deref panic.
	cNoEng, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	cNoEng.active.Store(&ghost)
	cNoEng.handleSessionNew()
	if frames := takeAllFramesA3(cNoEng); len(frames) != 0 {
		t.Fatalf("frames = %d, want 0 (no engine)", len(frames))
	}

	// Success: session_list goes out even without a WS connection.
	m := &mockEngine{projectDir: t.TempDir()}
	m.newSessionFn = func() (string, error) { return "ns-1", nil }
	m.listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{{SessionID: "ns-1", Title: "New"}}, nil
	}
	c, _ := bareConnectorA3(t, m)
	c.handleSessionNew()
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (session_list)", len(frames))
	}
	var msg struct {
		Type     string                `json:"type"`
		Sessions []struct{ ID string } `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "session_list" || len(msg.Sessions) != 1 || msg.Sessions[0].ID != "ns-1" {
		t.Fatalf("frame = %s, want session_list with ns-1", frames[0])
	}

	// Persist failure warns exactly once; success stays silent.
	dir := t.TempDir()
	mOK := &mockEngine{projectDir: dir}
	mOK.newSessionFn = func() (string, error) { return "ns-2", nil }
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	cOK, _ := bareConnectorA3(t, mOK)
	cOK.mgr = mgr
	rec := swapMsgRecorderA3(t)
	cOK.handleSessionNew()
	if got := rec.count("wui:session new:PersistMeta failed"); got != 0 {
		t.Fatalf("warn on successful persist = %d, want 0", got)
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mOK.projectDirFn = func() string { return blocked }
	cOK.handleSessionNew()
	if got := rec.count("wui:session new:PersistMeta failed"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// handleModelSwitch (lines 2880-2923)
// ---------------------------------------------------------------------------

func modelSwitchFixtureA3() (map[string]llm.Provider, map[string]*config.Provider) {
	cfg := &config.Provider{Name: "zhipu"}
	cfg.Models.Set("glm-5.2", config.ModelConfig{
		Context:   config.IntOrHuman(111111),
		MaxTokens: config.IntOrHuman(2222),
		Input:     []string{"text", "image"},
	})
	return map[string]llm.Provider{"zhipu": &stubLLMProvider{name: "zhipu"}},
		map[string]*config.Provider{"zhipu": cfg}
}

func TestMutA3_HandleModelSwitch(t *testing.T) {
	providers, configs := modelSwitchFixtureA3()

	// Provider missing from configs: error frame, engine untouched.
	m := &mockEngine{projectDir: t.TempDir()}
	c, _ := bareConnectorA3(t, m)
	c.providers = providers
	c.providerConfigs = map[string]*config.Provider{}
	c.handleModelSwitch("zhipu", "glm-5.2")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (error)", len(frames))
	}
	var errMsg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &errMsg); err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errMsg.Type != "error" || !strings.Contains(errMsg.Message, "no config for provider zhipu") {
		t.Fatalf("error frame = %+v, want no-config error", errMsg)
	}
	if len(m.setModelCalls) != 0 {
		t.Fatalf("model applied despite config error: %v", m.setModelCalls)
	}

	// No active engine: quiet return, not a nil-deref panic.
	cNoEng, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	cNoEng.active.Store(&ghost)
	cNoEng.providers = providers
	cNoEng.providerConfigs = configs
	cNoEng.handleModelSwitch("zhipu", "glm-5.2")

	// Success: engine sync + model_switched frame + info log.
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("context prompt")}}}
	m2 := &mockEngine{projectDir: t.TempDir()}
	m2.messagesFn = func() []types.Message { return msgs }
	c2, _ := bareConnectorA3(t, m2)
	c2.providers = providers
	c2.providerConfigs = configs
	var autoCfg engine.AutoCompactConfig
	m2.updateAutoFn = func(cfg engine.AutoCompactConfig) { autoCfg = cfg }
	rec := swapMsgRecorderA3(t)
	c2.handleModelSwitch("zhipu", "glm-5.2")

	if got := m2.setProviderCalls; len(got) != 1 || got[0] != "zhipu" {
		t.Fatalf("setProviderCalls = %v, want [zhipu]", got)
	}
	if got := m2.setModelCalls; len(got) != 1 || got[0] != "glm-5.2" {
		t.Fatalf("setModelCalls = %v, want [glm-5.2]", got)
	}
	if got := m2.setMaxTokensCalls; len(got) != 1 || got[0] != 2222 {
		t.Fatalf("setMaxTokensCalls = %v, want [2222]", got)
	}
	if got := m2.setInputModCalls; len(got) != 1 || len(got[0]) != 2 || got[0][0] != "text" || got[0][1] != "image" {
		t.Fatalf("setInputModCalls = %v, want [[text image]]", got)
	}
	if autoCfg.ContextWindow != 111111 {
		t.Fatalf("AutoCompactConfig.ContextWindow = %d, want 111111", autoCfg.ContextWindow)
	}
	if autoCfg.MaxConsecutiveFailures != 3 {
		t.Fatalf("AutoCompactConfig.MaxConsecutiveFailures = %d, want 3", autoCfg.MaxConsecutiveFailures)
	}
	if got := rec.count("wui:model switched"); got != 1 {
		t.Fatalf("model-switched info count = %d, want 1", got)
	}
	frames2 := takeAllFramesA3(c2)
	if len(frames2) != 1 {
		t.Fatalf("frames = %d, want 1 (model_switched)", len(frames2))
	}
	var sw struct {
		Type         string `json:"type"`
		ContextUsed  int    `json:"contextUsed"`
		ContextTotal int    `json:"contextTotal"`
		Thinking     string `json:"thinking"`
	}
	if err := json.Unmarshal([]byte(frames2[0]), &sw); err != nil {
		t.Fatalf("unmarshal model_switched: %v", err)
	}
	wantCtx := engine.TokenCountWithEstimation(msgs)
	if wantCtx == 0 {
		t.Fatal("test setup: estimation must be non-zero")
	}
	if sw.Type != "model_switched" || sw.ContextUsed != wantCtx || sw.ContextTotal != 200000 || sw.Thinking != "auto" {
		t.Fatalf("model_switched = %+v, want used=%d total=200000 thinking=auto", sw, wantCtx)
	}

	// Persist failure warns exactly once; success stays silent.
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	mOK := &mockEngine{projectDir: dir}
	mOK.messagesFn = func() []types.Message { return msgs }
	cOK, _ := bareConnectorA3(t, mOK)
	cOK.providers, cOK.providerConfigs = providers, configs
	cOK.mgr = mgr
	rec2 := swapMsgRecorderA3(t)
	cOK.handleModelSwitch("zhipu", "glm-5.2")
	if got, want := rec2.count("wui: failed to persist model selection"), 0; got != want {
		t.Fatalf("warn on successful persist = %d, want %d", got, want)
	}
	if vs := mgr.Get("main"); vs == nil || vs.Model != "zhipu/glm-5.2" {
		t.Fatalf("manager model after switch = %+v, want zhipu/glm-5.2", vs)
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mOK.projectDirFn = func() string { return blocked }
	cOK.handleModelSwitch("zhipu", "glm-5.2")
	if got := rec2.count("wui: failed to persist model selection"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// handleThinkingSwitch (lines 2946-2974)
// ---------------------------------------------------------------------------

func TestMutA3_HandleThinkingSwitch(t *testing.T) {
	// No active engine: quiet return, not a nil-deref panic.
	cNoEng, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	cNoEng.active.Store(&ghost)
	cNoEng.handleThinkingSwitch("high")

	// Success: effort recorded + info logged.
	m := &mockEngine{projectDir: t.TempDir()}
	c, _ := bareConnectorA3(t, m)
	rec := swapMsgRecorderA3(t)
	c.handleThinkingSwitch("high")
	if got := m.setThinkingCalls; len(got) != 1 || got[0] != llm.EffortHigh {
		t.Fatalf("setThinkingCalls = %v, want [high]", got)
	}
	if got := rec.count("wui:thinking switched"); got != 1 {
		t.Fatalf("thinking-switched info count = %d, want 1", got)
	}

	// Engine rejects the effort: error frame out, no info log.
	m2 := &mockEngine{projectDir: t.TempDir()}
	m2.setThinkingFn = func(llm.Effort) error { return errors.New("unsupported effort") }
	c2, _ := bareConnectorA3(t, m2)
	rec2 := swapMsgRecorderA3(t)
	c2.handleThinkingSwitch("high")
	if got := rec2.count("wui:thinking switched"); got != 0 {
		t.Fatalf("info logged despite SetThinking error (count=%d)", got)
	}
	frames := takeAllFramesA3(c2)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (error)", len(frames))
	}
	var errMsg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &errMsg); err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errMsg.Type != "error" || !strings.Contains(errMsg.Message, "unsupported effort") {
		t.Fatalf("error frame = %+v, want the SetThinking error", errMsg)
	}

	// Persist failure warns exactly once; success stays silent.
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	mOK := &mockEngine{projectDir: dir}
	cOK, _ := bareConnectorA3(t, mOK)
	cOK.mgr = mgr
	rec3 := swapMsgRecorderA3(t)
	cOK.handleThinkingSwitch("high")
	if got := rec3.count("wui:thinking persist failed"); got != 0 {
		t.Fatalf("warn on successful persist = %d, want 0", got)
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mOK.projectDirFn = func() string { return blocked }
	cOK.handleThinkingSwitch("high")
	if got := rec3.count("wui:thinking persist failed"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// rememberUpload / takeRewindStash (lines 3134-3294)
// ---------------------------------------------------------------------------

func TestMutA3_RememberUploadMutexRelease(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	c.rememberUpload("id1", savedAttachment{path: "p", mime: "m", kind: "image"})
	if !c.restoreMu.TryLock() {
		t.Fatal("rememberUpload() did not release restoreMu")
	}
	c.restoreMu.Unlock()
	saved := savedOfA3(t, c, "id1")
	if saved.path != "p" || saved.mime != "m" || saved.kind != "image" {
		t.Fatalf("saved = %+v, want {p m image}", saved)
	}
}

func TestMutA3_TakeRewindStash(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	if got := c.takeRewindStash("main"); got != nil {
		t.Fatalf("nil stash: got %#v, want nil (and no panic)", got)
	}

	setStash := func(engineID string, blocks []types.ContentBlock) {
		// TryLock: a mutant stranding restoreMu in an earlier take must fail
		// fast here instead of deadlocking the test.
		if !c.restoreMu.TryLock() {
			t.Fatal("restoreMu stranded by an earlier takeRewindStash call")
		}
		c.rewindStash = &rewindStash{engineID: engineID, blocks: blocks}
		c.restoreMu.Unlock()
	}
	setStash("other", []types.ContentBlock{types.NewTextBlock("x")})
	if got := c.takeRewindStash("main"); got != nil {
		t.Fatalf("foreign-engine stash: got %#v, want nil", got)
	}
	if c.rewindStash == nil {
		t.Fatal("foreign-engine stash must stay put")
	}

	// Non-nil empty stash: strictly nil result (an empty slice would make the
	// restore handler re-walk Messages and resurrect an older prompt).
	setStash("main", []types.ContentBlock{})
	if got := c.takeRewindStash("main"); got != nil {
		t.Fatalf("empty stash: got %#v, want nil", got)
	}

	setStash("main", []types.ContentBlock{types.NewTextBlock("y")})
	got := c.takeRewindStash("main")
	if len(got) != 1 || got[0].Text != "y" {
		t.Fatalf("stash take = %#v, want one y block", got)
	}
	if second := c.takeRewindStash("main"); second != nil {
		t.Fatalf("stash must be one-shot, second take = %#v", second)
	}
	if !c.restoreMu.TryLock() {
		t.Fatal("takeRewindStash() did not release restoreMu")
	}
	c.restoreMu.Unlock()
}

// ---------------------------------------------------------------------------
// buildRestoredMsg (lines 3174-3242)
// ---------------------------------------------------------------------------

func TestMutA3_BuildRestoredMsgTextAndDocs(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)

	msg := buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u1", Value: "v", Content: []types.ContentBlock{types.NewTextBlock("c")}})
	if msg.UUID != "u1" {
		t.Fatalf("UUID = %q, want u1", msg.UUID)
	}
	if msg.Text != "c" {
		t.Fatalf("Text = %q, want c (content text wins over Value)", msg.Text)
	}

	// Single-character content text still wins over Value.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u2", Value: "v", Content: []types.ContentBlock{types.NewTextBlock("x")}})
	if msg.Text != "x" {
		t.Fatalf("Text = %q, want x", msg.Text)
	}

	// No text blocks: falls back to Value.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u3", Value: "v"})
	if msg.Text != "v" {
		t.Fatalf("Text = %q, want v (Value fallback)", msg.Text)
	}

	// Document happy path: attachment metadata + connector-level id binding.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u4", Content: []types.ContentBlock{
		{Type: types.ContentTypeDocument, Name: "n.pdf", Path: "/tmp/n.pdf", Mime: "application/pdf", Size: 123},
		types.NewTextBlock("hello"),
	}})
	if msg.Text != "hello" {
		t.Fatalf("Text = %q, want hello", msg.Text)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if att.ID == "" || att.Name != "n.pdf" || att.Mime != "application/pdf" || att.Size != 123 {
		t.Fatalf("doc attachment = %+v", att)
	}
	if saved := savedOfA3(t, c, att.ID); saved.path != "/tmp/n.pdf" || saved.mime != "application/pdf" || saved.kind != "document" {
		t.Fatalf("saved doc = %+v", saved)
	}

	// Empty-path document is skipped; later text still collected.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u5", Value: "vv", Content: []types.ContentBlock{
		{Type: types.ContentTypeDocument, Path: ""},
		types.NewTextBlock("t"),
	}})
	if len(msg.Attachments) != 0 {
		t.Fatalf("empty-path doc produced %d attachments, want 0", len(msg.Attachments))
	}
	if msg.Text != "t" {
		t.Fatalf("Text = %q, want t", msg.Text)
	}

	// Nil-source image is skipped; the document after it is still processed.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		{Type: types.ContentTypeImage, Source: nil},
		docBlockA3("/d1.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (doc only)", len(msg.Attachments))
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/d1.pdf" {
		t.Fatalf("saved = %+v, want the doc path", saved)
	}

	// File-sourced image with empty path is skipped; doc still processed.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		{Type: types.ContentTypeImage, Source: &types.ImageSource{MediaType: "image/png"}},
		docBlockA3("/d2.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (empty-path image skipped)", len(msg.Attachments))
	}

	// File-sourced image happy path + continue keeps later blocks.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		fileImageBlockA3("/img/p.png", "image/png"),
		docBlockA3("/d3.pdf"),
	}})
	if len(msg.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(msg.Attachments))
	}
	if att := msg.Attachments[0]; att.ID == "" || att.Mime != "image/png" {
		t.Fatalf("file image attachment = %+v", att)
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/img/p.png" || saved.mime != "image/png" || saved.kind != "image" {
		t.Fatalf("saved file image = %+v", saved)
	}
	if saved := savedOfA3(t, c, msg.Attachments[1].ID); saved.path != "/d3.pdf" {
		t.Fatalf("saved second = %+v, want the doc path", saved)
	}
}

func TestMutA3_BuildRestoredMsgBase64Images(t *testing.T) {
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, nil)
	c.mediaCache = store

	// Extension derives from the media type's subtype.
	raw := []byte("PNGBYTES")
	msg := buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("video/mp4", base64.StdEncoding.EncodeToString(raw)),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if att.ID == "" || att.Mime != "video/mp4" || att.Size != len(raw) {
		t.Fatalf("b64 attachment = %+v", att)
	}
	saved := savedOfA3(t, c, att.ID)
	if !strings.HasSuffix(saved.path, ".mp4") {
		t.Fatalf("cached path = %q, want .mp4 suffix", saved.path)
	}
	if saved.mime != "video/mp4" || saved.kind != "image" {
		t.Fatalf("saved = %+v", saved)
	}

	// No-slash media type falls back to .png.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("noslash", base64.StdEncoding.EncodeToString(raw)),
	}})
	saved = savedOfA3(t, c, msg.Attachments[0].ID)
	if !strings.HasSuffix(saved.path, ".png") {
		t.Fatalf("cached path = %q, want .png default", saved.path)
	}

	// Slash at index 0 uses the whole rest as the extension.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("/webm", base64.StdEncoding.EncodeToString(raw)),
	}})
	saved = savedOfA3(t, c, msg.Attachments[0].ID)
	if !strings.HasSuffix(saved.path, ".webm") {
		t.Fatalf("cached path = %q, want .webm suffix", saved.path)
	}

	// Invalid base64 is dropped without panic; later blocks still processed.
	rec := swapMsgRecorderA3(t)
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("image/png", "!!!not-base64!!!"),
		docBlockA3("/d4.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (bad b64 dropped, doc kept)", len(msg.Attachments))
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/d4.pdf" {
		t.Fatalf("saved = %+v, want the doc path", saved)
	}
	if got := rec.count("wui:restore image decode failed, dropping attachment"); got != 1 {
		t.Fatalf("decode-failure warn = %d, want 1", got)
	}
}

func TestMutA3_BuildRestoredMsgCacheSaveFailure(t *testing.T) {
	root := t.TempDir()
	store, err := media.NewAt(root)
	if err != nil {
		t.Fatal(err)
	}
	// Deleting the store root makes every Save fail (temp write ENOENT).
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, nil)
	c.mediaCache = store
	rec := swapMsgRecorderA3(t)

	raw := []byte("DATA")
	msg := buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("image/png", base64.StdEncoding.EncodeToString(raw)),
		docBlockA3("/d5.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (image dropped on Save error, doc kept)", len(msg.Attachments))
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/d5.pdf" {
		t.Fatalf("saved = %+v, want the doc path", saved)
	}
	if got := rec.count("wui:restore image re-materialize failed"); got != 1 {
		t.Fatalf("Save-failure warn = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// buildQueuedMsgs (lines 3251-3280)
// ---------------------------------------------------------------------------

func TestMutA3_BuildQueuedMsgs(t *testing.T) {
	items := []types.QueuedItem{
		{Mode: types.ItemModePrompt, UUID: "a", Value: "v", Content: []types.ContentBlock{types.NewTextBlock("x")}},
		{Mode: types.ItemModeJob, UUID: "b", Value: "job"},
		{Mode: types.ItemModePrompt, IsMeta: true, UUID: "c", Value: "meta"},
		{Mode: types.ItemModePrompt, UUID: "d", Value: "vd", Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Source: &types.ImageSource{MediaType: "image/png"}},
			{Type: types.ContentTypeDocument, Name: "n", Mime: "application/pdf"},
		}},
	}
	out := buildQueuedMsgs(items)
	if len(out) != 2 {
		t.Fatalf("queued msgs = %d, want 2 (job and meta filtered): %+v", len(out), out)
	}
	if out[0].UUID != "a" || out[0].Text != "x" {
		t.Fatalf("out[0] = %+v, want {a x} (content text beats Value)", out[0])
	}
	if out[1].UUID != "d" || out[1].Text != "vd" {
		t.Fatalf("out[1] = %+v, want {d vd}", out[1])
	}
	if len(out[1].Attachments) != 2 ||
		out[1].Attachments[0].Mime != "image/png" ||
		out[1].Attachments[1].Name != "n" || out[1].Attachments[1].Mime != "application/pdf" {
		t.Fatalf("out[1].Attachments = %+v", out[1].Attachments)
	}
}

// ---------------------------------------------------------------------------
// lastPromptAttachmentBlocks / handleRestoreAttachments (lines 3301-3350)
// ---------------------------------------------------------------------------

func TestMutA3_LastPromptAttachmentBlocks(t *testing.T) {
	img := func(tag string) types.ContentBlock {
		return types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{MediaType: "image/png", Data: tag}}
	}
	doc := func(tag string) types.ContentBlock {
		return types.ContentBlock{Type: types.ContentTypeDocument, Path: "/p-" + tag + ".pdf", Mime: "application/pdf"}
	}
	msgs := []types.Message{
		// The match: walked backwards, this is the first real prompt with
		// attachment blocks.
		{Role: types.RoleUser, Content: []types.ContentBlock{img("a0"), doc("d0")}},
		// Skipped: no attachment blocks.
		{Role: types.RoleUser, Content: nil},
		// Skipped: tool_result batch carrying an image.
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewToolResultBlock("tr1", nil, false), img("a2")}},
		// Skipped: meta (system-generated).
		{Role: types.RoleUser, Flags: types.FlagMeta, Content: []types.ContentBlock{img("a3")}},
		// Skipped: assistant message.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{img("a4")}},
	}
	blocks := lastPromptAttachmentBlocks(msgs)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (only the real prompt's blocks): %+v", len(blocks), blocks)
	}
	if blocks[0].Source.Data != "a0" || blocks[1].Path != "/p-d0.pdf" {
		t.Fatalf("blocks = %+v, want [a0 d0]", blocks)
	}
	if got := lastPromptAttachmentBlocks(nil); got != nil {
		t.Fatalf("nil msgs = %#v, want nil", got)
	}
}

func TestMutA3_HandleRestoreAttachments(t *testing.T) {
	// No active slot: empty restore_result, no panic.
	c, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	c.active.Store(&ghost)
	withTimeoutA3(t, "handleRestoreAttachments", c.handleRestoreAttachments)
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (restore_result)", len(frames))
	}
	var resp struct {
		Type        string                `json:"type"`
		Attachments []struct{ ID string } `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "restore_result" || len(resp.Attachments) != 0 {
		t.Fatalf("resp = %+v, want empty restore_result", resp)
	}

	// Slot with nil engine: guarded, no panic.
	c2, _ := bareConnectorA3(t, nil)
	c2.slots["main"].engine = nil
	withTimeoutA3(t, "handleRestoreAttachments", c2.handleRestoreAttachments)

	// Stash path: one attachment re-staged under a fresh id.
	c3, _ := bareConnectorA3(t, nil)
	c3.restoreMu.Lock()
	c3.rewindStash = &rewindStash{engineID: "main", blocks: []types.ContentBlock{fileImageBlockA3("/img/r.png", "image/png")}}
	c3.restoreMu.Unlock()
	withTimeoutA3(t, "handleRestoreAttachments", c3.handleRestoreAttachments)
	frames3 := takeAllFramesA3(c3)
	if len(frames3) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames3))
	}
	if err := json.Unmarshal([]byte(frames3[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Attachments) != 1 || resp.Attachments[0].ID == "" {
		t.Fatalf("attachments = %+v, want one re-staged id", resp.Attachments)
	}
}

// ---------------------------------------------------------------------------
// switchEngine (lines 3359-3393)
// ---------------------------------------------------------------------------

func TestMutA3_SwitchEngineUnknown(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	c.switchEngine("nope")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (error)", len(frames))
	}
	var errMsg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &errMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if errMsg.Type != "error" || !strings.Contains(errMsg.Message, "unknown engine: nope") {
		t.Fatalf("error frame = %+v, want unknown-engine error", errMsg)
	}
	if c.ActiveID() != "main" {
		t.Fatalf("ActiveID = %q, want unchanged main", c.ActiveID())
	}
}

func TestMutA3_SwitchEngineManagerSync(t *testing.T) {
	// SetActive fails (engine missing from the manager): warn once, no
	// PersistMeta run.
	c, _ := bareConnectorA3(t, nil)
	m2 := &mockEngine{projectDir: t.TempDir()}
	m2.engineIDFn = func() string { return "e2" }
	c.slots["e2"] = newMockSlotA3("e2", m2)
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c.mgr = mgr

	rec := swapMsgRecorderA3(t)
	c.switchEngine("e2")
	if got := rec.count("wui:switchEngine:SetActive failed"); got != 1 {
		t.Fatalf("SetActive-failure warn = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(m2.projectDir, "meta.json")); !os.IsNotExist(err) {
		t.Fatal("meta.json must not be written when SetActive fails")
	}
	if c.ActiveID() != "e2" {
		t.Fatalf("ActiveID = %q, want e2", c.ActiveID())
	}
	if !c.slots["e2"].active.Load() || c.slots["main"].active.Load() {
		t.Fatal("active flags not flipped")
	}

	// SetActive succeeds but PersistMeta fails: warn once.
	dir := t.TempDir()
	m3 := &mockEngine{projectDir: dir}
	m3.engineIDFn = func() string { return "e3" }
	c2, _ := bareConnectorA3(t, nil)
	c2.slots["e3"] = newMockSlotA3("e3", m3)
	mgr2 := engine.NewEngineManager()
	mgr2.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	mgr2.Add(&engine.EngineViewState{ID: "e3", Name: "E3"})
	if err := mgr2.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c2.mgr = mgr2
	rec2 := swapMsgRecorderA3(t)

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m3.projectDirFn = func() string { return blocked }
	c2.switchEngine("e3")
	if got := rec2.count("wui:switchEngine:PersistMeta failed"); got != 1 {
		t.Fatalf("PersistMeta-failure warn = %d, want 1", got)
	}

	// Full success: no warns, meta.json written under the new engine's dir.
	c3, _ := bareConnectorA3(t, nil)
	dir3 := t.TempDir()
	m4 := &mockEngine{projectDir: dir3}
	m4.engineIDFn = func() string { return "e4" }
	c3.slots["e4"] = newMockSlotA3("e4", m4)
	mgr3 := engine.NewEngineManager()
	mgr3.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	mgr3.Add(&engine.EngineViewState{ID: "e4", Name: "E4"})
	if err := mgr3.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c3.mgr = mgr3
	rec3 := swapMsgRecorderA3(t)
	c3.switchEngine("e4")
	if got := rec3.count("wui:switchEngine:SetActive failed") + rec3.count("wui:switchEngine:PersistMeta failed"); got != 0 {
		t.Fatalf("success warns = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(dir3, "meta.json")); err != nil {
		t.Fatalf("meta.json not persisted: %v", err)
	}
}
