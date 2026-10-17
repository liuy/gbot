package wui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/types"
)

// A reminder-triggered auto-turn must keep the turn's assistant block group
// in exactly ONE of the two takeover regions: while the query runs the
// streamed copy lives in the snapshot (history truncates in-flight rows);
// at query_end the snapshot is reset and history owns the rows. These tests
// drive the production path — real engine, real store, reminder via
// EnqueueAttachment (the job-notification entry point), takeover metadata as
// a fresh client receives it — and pin that the merged view a client renders
// (history + snapshot) shows the assistant text exactly once.

// dupAutoTurnProvider replays scripted stream responses in order.
type dupAutoTurnProvider struct {
	mu        sync.Mutex
	responses [][]llm.StreamEvent
	index     int
}

func (p *dupAutoTurnProvider) Name() string { return "dup-autoturn-mock" }

func (p *dupAutoTurnProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: []types.ContentBlock{types.NewTextBlock("Summary of conversation.")},
	}, nil
}

func (p *dupAutoTurnProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.index >= len(p.responses) {
		return nil, errors.New("no more scripted responses")
	}
	events := p.responses[p.index]
	p.index++
	ch := make(chan llm.StreamEvent, len(events)+1)
	go func() {
		defer close(ch)
		for _, evt := range events {
			ch <- evt
		}
	}()
	return ch, nil
}

// dupParkTool parks the turn mid-tool with the assistant message already in
// engine history (the stream completes while the tool runs), so the test can
// take over at the exact window the bug report describes: the turn's first
// assistant row is committed but the query is still running.
type dupParkTool struct {
	startCh   chan struct{}
	releaseCh chan struct{}
}

func (dupParkTool) Name() string                                { return "dup_park" }
func (dupParkTool) Aliases() []string                           { return nil }
func (dupParkTool) Description(json.RawMessage) (string, error) { return "parks", nil }
func (dupParkTool) InputSchema() json.RawMessage                { return nil }
func (t *dupParkTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	close(t.startCh)
	<-t.releaseCh
	return &tool.ToolResult{Data: "ok"}, nil
}
func (dupParkTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (dupParkTool) IsReadOnly(json.RawMessage) bool        { return true }
func (dupParkTool) IsDestructive(json.RawMessage) bool     { return false }
func (dupParkTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (dupParkTool) IsEnabled() bool                        { return true }
func (dupParkTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (dupParkTool) MaxResultSize() int { return 50000 }
func (dupParkTool) Prompt() string     { return "" }
func (dupParkTool) RenderResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return ""
}

// dupThinkTextToolUseEvents streams ONE assistant message carrying the block
// group the bug report names: thinking + text + tool_use.
func dupThinkTextToolUseEvents(marker, toolID string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 20}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: "weighing the job notification"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "text_delta", Text: marker}},
		{Type: "content_block_stop", Index: 1},
		{Type: "content_block_start", Index: 2, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: "dup_park"}},
		{Type: "content_block_delta", Index: 2, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 2},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
		{Type: "message_stop"},
	}
}

// dupTextEvents closes the auto-turn: the post-tool call answering end_turn.
func dupTextEvents(text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 30}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
}

// wireDupAutoTurnEngine builds the production shape under test: a real engine
// with a real store, wired into a connector slot whose hub subscription is
// live, so the auto-turn's events flow through engineHubShim exactly as they
// do in the daemon.
func wireDupAutoTurnEngine(t *testing.T, prov *dupAutoTurnProvider, park *dupParkTool) (*engine.Engine, *WUIConnector) {
	t.Helper()
	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session, err := store.CreateSessionWithEngine("/proj", "test-model", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}

	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider: prov,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"dup_park": park}
		},
		Model:    "test-model",
		EngineID: "main",
	})
	eng.SetDispatcher(h)
	eng.SetStore(store, t.TempDir())
	eng.SetSessionID(session.SessionID)
	// The idle drain refuses to start without a system prompt
	// (startProcessAttachmentsIfIdle returns early on empty prompt).
	eng.SetSystemPrompt("test system prompt")
	t.Cleanup(func() { eng.Close() })

	// Mock-based harness supplies the queue/writer plumbing; the active slot
	// is swapped for the real engine adapter. The harness's hub subscription
	// keeps routing events for "main" — onEngineEvent looks the slot up by ID.
	c := newTestConnectorWithHub(t, h)
	slot := &engineSlot{
		engineID:           "main",
		engine:             &engineAdapter{eng: eng},
		hub:                h,
		taskToolIDs:        make(map[string]bool),
		jobView:            make(map[string]*jobViewEntry),
		jobFeeds:           make(map[string]*jobFeedState),
		innerToolToFeed:    make(map[string]string),
		pendingAgentEvents: make(map[string]*pendingAgentEvents),
	}
	slot.active.Store(true)
	c.slots["main"] = slot
	return eng, c
}

// dupJobReminderItem mirrors the daemon's job-notification enqueue (the
// engine_job_reminder_test.go canonical form).
func dupJobReminderItem(uuid, jobID string) types.QueuedItem {
	return types.QueuedItem{
		Value: bash.JobNotification{
			JobID:   jobID,
			Status:  "completed",
			Summary: `Background command "make check" completed (exit code 0)`,
		}.FormatXML(),
		Mode:      types.ItemModeJob,
		UUID:      uuid,
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginJob},
		Timestamp: time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC),
	}
}

// dupFreshClientMetadata dials a fresh WS client and returns its metadata
// frame's history and snapshot — the pair the frontend merges into the view.
func dupFreshClientMetadata(t *testing.T, c *WUIConnector) (historyJSON, snapshotJSON string) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	meta := readMetadata(t, ws)
	return string(meta.History), string(meta.Snapshot)
}

// dupRenderedCount models the frontend's history render
// (mapHistoryToChatMessages): a message's visible text is its blocks array
// when present, else the flat text field; empty-text user rows are skipped.
// Counts needle across the rendered history plus the raw snapshot blocks.
func dupRenderedCount(t *testing.T, historyJSON, snapshotJSON, needle string) int {
	t.Helper()
	var hist struct {
		Messages []struct {
			Role   string `json:"role"`
			Text   string `json:"text"`
			Blocks []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(historyJSON), &hist); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	count := 0
	for _, m := range hist.Messages {
		if m.Role == "user" && strings.TrimSpace(m.Text) == "" && len(m.Blocks) == 0 {
			continue
		}
		if len(m.Blocks) == 0 {
			count += strings.Count(m.Text, needle)
			continue
		}
		for _, b := range m.Blocks {
			count += strings.Count(b.Text, needle)
		}
	}
	count += strings.Count(snapshotJSON, needle)
	return count
}

// TestTakeover_AutoTurnMidRunAssistantRenderOnce pins the two-region handover
// for the reminder auto-turn: after the turn's first assistant message is in
// engine history but the query is still running (its tool call parked), a
// client connecting fresh must see that assistant text exactly once across
// the merged view it renders. buildHistory's truncation bound is
// msgs[:queryStartMsgIdx+1] — the +1 keeps the query's user-side rows
// visible; processAttachments must therefore snapshot BEFORE draining the
// reminder rows so idx marks the last user-side row, excluding the in-flight
// assistant row that the snapshot streams (snapshotting after the drain
// pointed the +1 at that assistant row and the client rendered it twice).
// The user-verified production incident: refresh during a long
// reminder auto-turn shows the turn's thinking+text+tool group doubled.
func TestTakeover_AutoTurnMidRunAssistantRenderOnce(t *testing.T) {
	marker := "AUTO-TURN-ANSWER-MARKER-7f3a"
	park := &dupParkTool{startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	prov := &dupAutoTurnProvider{responses: [][]llm.StreamEvent{
		dupTextEvents("prior turn acknowledged"),
		dupThinkTextToolUseEvents(marker, "toolu_dup1"),
		dupTextEvents("notification acknowledged"),
	}}
	eng, c := wireDupAutoTurnEngine(t, prov, park)
	// The parked tool must always be released, on the assertion failure path
	// too, or eng.Close's cleanup would block on the tool goroutine.
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(park.releaseCh) })

	// A completed prior query leaves a stale queryStartMsgIdx behind — the
	// realistic idle state an auto-turn wakes up in. Without the drain-loop
	// snapshot, that stale idx truncates history to msgs[:1] and hides both
	// the prior turn and the reminder row from a fresh client.
	res := eng.QuerySync(context.Background(), "prior user turn", "test system prompt")
	if res.Error != nil {
		t.Fatalf("prior query failed: %v", res.Error)
	}
	if res.Reply != "prior turn acknowledged" {
		t.Fatalf("prior query reply = %q, want scripted reply", res.Reply)
	}

	// Reminder fires while the engine is idle — the auto-turn path:
	// EnqueueAttachment → startProcessAttachmentsIfIdle → processAttachments
	// → runTurns (turn_start but no query_start).
	eng.EnqueueAttachment(dupJobReminderItem("job-dup-1", "bg-1"))

	// Park mid-tool with the assistant message already in history: the tool
	// starts during streaming, the stream then completes and appends the
	// assistant row, and the tool result only lands after release.
	select {
	case <-park.startCh:
	case <-time.After(10 * time.Second):
		t.Fatal("auto-turn tool never started")
	}
	if !waitFor(10*time.Second, func() bool {
		for _, m := range eng.Messages() {
			if m.Role != types.RoleAssistant {
				continue
			}
			for _, b := range m.Content {
				if b.Type == types.ContentTypeText && strings.Contains(b.Text, marker) {
					return true
				}
			}
		}
		return false
	}) {
		t.Fatal("assistant row never reached engine history while the tool ran")
	}

	// Fresh client connects mid-run and receives the composite metadata
	// frame — the same history + snapshot pair the frontend merges.
	historyJSON, snapshotJSON := dupFreshClientMetadata(t, c)

	inHistory := strings.Count(historyJSON, marker)
	inSnapshot := strings.Count(snapshotJSON, marker)
	merged := dupRenderedCount(t, historyJSON, snapshotJSON, marker)
	if merged != 1 {
		t.Fatalf("auto-turn assistant text %q appears %d times in a fresh client's merged view while the auto-turn runs (history=%d, snapshot=%d, frontend-rendered=%d), want exactly 1\nhistory=%s\nsnapshot=%s",
			marker, merged, inHistory, inSnapshot, merged, historyJSON, snapshotJSON)
	}
	// The boundary must keep the reminder row itself: it is a user-side row
	// (msgs[:idx+1] admits it) and must render once from history like any
	// queued user input, never vanish behind the truncation.
	if n := dupRenderedCount(t, historyJSON, snapshotJSON, "make check"); n != 1 {
		t.Fatalf("reminder row renders %d times in a fresh client's merged view, want exactly 1\nhistory=%s\nsnapshot=%s",
			n, historyJSON, snapshotJSON)
	}
}
