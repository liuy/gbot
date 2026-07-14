// Package webchat implements the gbot web chat connector: an HTTP+WS server
// that serves a React SPA, bridges the engine's QueryEvent stream over
// WebSocket to browser/WebView clients, and handles Ask (permission)
// interactions via a request/response WS protocol.
//
// The connector subscribes to the main engine's hub (the same engine the TUI
// drives). It is request-driven: inbound WS messages trigger queries; the
// connector itself owns no polling loop.
package wui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// queryEventWithAbort embeds QueryEvent and adds an "aborted" boolean for the
// query_end wire payload. The engine's terminal Error is `json:"-"` so it
// never crosses the WS boundary; this is the only channel for the frontend to
// learn the query was user-interrupted (mirrors TUI's
// errors.AsType[*engine.AbortError] check at pkg/tui/repl.go:1134). For
// non-abort events Aborted is false and omitempty drops the key, so the wire
// shape is byte-identical to marshalling QueryEvent directly.
type queryEventWithAbort struct {
	types.QueryEvent
	Aborted bool `json:"aborted,omitempty"`
}

// engineClient is the subset of engine.Engine methods the connector uses.
// Defined as an interface so tests can substitute a mock without polluting
// production code with test-only seam fields.
type engineClient interface {
	Query(ctx context.Context, userMessage, systemPrompt string)
	IsBusy() bool
	Messages() []types.Message
	Tools() map[string]tool.Tool
	EnqueueAttachment(item types.QueuedItem)
	Abort()
	RewindTo(idx int) error
	RemoveAttachment(uuid string) bool
	SystemPrompt() string
	TaskList() *task.List
	SwitchSession(sessionID string) error
	ListSessions(limit int) ([]*short.Session, error)
	NewSession() (string, error)
	UpdateSessionTitle(sessionID, title string) error
	SessionID() string
	EngineID() string
	Model() string
	ProjectDir() string
	SetProvider(provider llm.Provider)
	SetModel(model string)
	Provider() llm.Provider
	SetMaxTokens(n int)
	SetInputModalities(modalities []string)
	UpdateAutoCompactConfig(cfg engine.AutoCompactConfig)
}

// engineAdapter wraps *engine.Engine so it satisfies engineClient. The only
// mismatch is RewindTo: the engine returns (*RewindResult, error) but the
// connector only cares whether it succeeded, so the adapter discards the result.
type engineAdapter struct {
	eng *engine.Engine
}

var _ engineClient = (*engineAdapter)(nil)

func (a *engineAdapter) Query(ctx context.Context, userMessage, systemPrompt string) {
	a.eng.Query(ctx, userMessage, systemPrompt)
}
func (a *engineAdapter) IsBusy() bool                { return a.eng.IsBusy() }
func (a *engineAdapter) Messages() []types.Message   { return a.eng.Messages() }
func (a *engineAdapter) Tools() map[string]tool.Tool { return a.eng.Tools() }
func (a *engineAdapter) EnqueueAttachment(item types.QueuedItem) {
	a.eng.EnqueueAttachment(item)
}
func (a *engineAdapter) Abort()                            { a.eng.Abort() }
func (a *engineAdapter) RemoveAttachment(uuid string) bool { return a.eng.RemoveAttachment(uuid) }
func (a *engineAdapter) SystemPrompt() string              { return a.eng.SystemPrompt() }
func (a *engineAdapter) TaskList() *task.List              { return a.eng.TaskList() }
func (a *engineAdapter) RewindTo(idx int) error {
	_, err := a.eng.RewindTo(idx)
	return err
}
func (a *engineAdapter) SwitchSession(sessionID string) error {
	_, err := a.eng.SwitchSession(sessionID)
	return err
}
func (a *engineAdapter) ListSessions(limit int) ([]*short.Session, error) {
	return a.eng.ListSessions(limit)
}
func (a *engineAdapter) NewSession() (string, error) {
	if err := a.eng.NewSession(a.eng.ProjectDir(), ""); err != nil {
		return "", err
	}
	return a.eng.SessionID(), nil
}
func (a *engineAdapter) UpdateSessionTitle(sessionID, title string) error {
	store := a.eng.Store()
	if store == nil {
		return fmt.Errorf("engine: no store")
	}
	return store.UpdateSessionTitle(sessionID, title)
}
func (a *engineAdapter) SessionID() string  { return a.eng.SessionID() }
func (a *engineAdapter) EngineID() string   { return a.eng.EngineID() }
func (a *engineAdapter) Model() string      { return a.eng.Model() }
func (a *engineAdapter) ProjectDir() string { return a.eng.ProjectDir() }

func (a *engineAdapter) SetProvider(p llm.Provider)    { a.eng.SetProvider(p) }
func (a *engineAdapter) SetModel(m string)             { a.eng.SetModel(m) }
func (a *engineAdapter) Provider() llm.Provider        { return a.eng.Provider() }
func (a *engineAdapter) SetMaxTokens(n int)            { a.eng.SetMaxTokens(n) }
func (a *engineAdapter) SetInputModalities(m []string) { a.eng.SetInputModalities(m) }
func (a *engineAdapter) UpdateAutoCompactConfig(cfg engine.AutoCompactConfig) {
	a.eng.UpdateAutoCompactConfig(cfg)
}

// queryStats accumulates per-query stats (usage + start time + tool count +
// thinking duration) for connect_status. All fields are atomic because they
// are written by onEngineEvent (hub goroutine) and read by switchEngine/
// sendMetadata (readLoop goroutine) without a lock. Reset on EventQueryEnd.
type queryStats struct {
	inputTokens              atomic.Int64
	outputTokens             atomic.Int64
	cacheReadInputTokens     atomic.Int64
	cacheCreationInputTokens atomic.Int64
	toolCount                atomic.Int64
	thinkingMs               atomic.Int64
	startMs                  atomic.Int64
}

// toolSnapshot is the JSON-serializable state of a single in-flight tool call.
type toolSnapshot struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output,omitempty"`
	Done   bool   `json:"done"`
	Error  bool   `json:"error,omitempty"`
}

// thinkingState tracks the in-flight thinking block.
type thinkingState struct {
	Text     string `json:"text"`
	Duration int64  `json:"duration_ns"`
	Done     bool   `json:"done"`
}

// streamState is the per-engine real-time streaming state, equivalent to TUI's
// ReplState. Only accessed by the slot's own hub goroutine (onEngineEvent),
// so no lock is needed. On query_end the state is reset to zero.
type streamState struct {
	text     string
	tools    []toolSnapshot
	thinking *thinkingState
}

// engineSlot holds the per-engine state. Each engine gets its own streamState,
// queryStats, and taskToolIDs so background engines accumulate state
// independently of the active one. The active flag and snapshotSent flag are
// atomic because they are set by switchEngine (readLoop goroutine) and read
// by onEngineEvent (hub goroutine).
type engineSlot struct {
	engineID     string
	engine       engineClient
	hub          *hub.Hub
	unsubscribe  func()
	streamState  streamState
	queryStats   queryStats
	taskToolIDs  map[string]bool
	active       atomic.Bool
	snapshotSent atomic.Bool
}

// WUIConnector implements connector.Connector for the web chat. It owns
// an EngineManager, subscribes to every engine's hub, and routes events to
// the single active WS connection. Inbound WS messages operate on the
// active engine; engine_switch swaps the active engine via atomic flag flip
// and pushes metadata — onEngineEvent sends the streamState snapshot on the
// next event.
type WUIConnector struct {
	mgr   *engine.EngineManager
	slots map[string]*engineSlot // keyed by engine ID

	// active is the active engine ID. atomic.Pointer for lock-free reads.
	active atomic.Pointer[string]

	// Ask → ResponseCh plumbing. When the engine dispatches EventAsk, Handle
	// stores evt.Ask under a fresh monotonic id and emits the ask outbound.
	// When the client responds, the WS handler looks up the id and writes to
	// ResponseCh. On disconnect, all pending asks are aborted.
	pendingAsks map[string]*types.AskEvent
	pendingMu   sync.Mutex
	askCounter  atomic.Int64

	// The single active WS connection. The connector supports one client at a
	// time; a new connection replaces the prior one via takeover.
	activeWS atomic.Pointer[websocket.Conn]

	// wsCh is the single-consumer write queue for the WS writer goroutine.
	// All WS writes go through sendWS → wsCh → wsWriter. This ensures
	// gorilla's single-writer constraint without a mutex on the hot path.
	wsCh chan []byte

	// done signals the wsWriter goroutine to exit during shutdown.
	done chan struct{}

	// slotsMu guards only the slots map (registerEngine at runtime).
	slotsMu sync.RWMutex

	// providers + providerConfigs drive the model picker (config message +
	// model_switch handler). providers maps provider name → llm.Provider
	// (only providers with resolved API keys appear here). providerConfigs
	// is the full config map used for model ordering + capability resolution.
	providers       map[string]llm.Provider
	providerConfigs map[string]*config.Provider

	// createEngine builds a new engine, registers it in the manager, and
	// subscribes this connector to its hub. Set by main.go via
	// SetCreateEngineFn. nil when engine_new is not supported.
	createEngine func(name string) (string, error)

	// testMock caches the mockEngine for lock-free test access via mock().
	// nil in production connectors. Set only by newTestConnector* helpers.
	// Typed as any so the production file does not depend on the test-only
	// mockEngine type; mock() (in helpers_test.go) does the type assertion.
	testMock any
}

// New builds a WUIConnector bound to an EngineManager. The connector
// subscribes to every engine's hub immediately. The active engine is set
// from mgr.ActiveID(). main.go must call SetCreateEngineFn to enable
// engine_new.
func New(mgr *engine.EngineManager, providers map[string]llm.Provider, providerConfigs map[string]*config.Provider) *WUIConnector {
	c := &WUIConnector{
		mgr:             mgr,
		slots:           make(map[string]*engineSlot),
		pendingAsks:     make(map[string]*types.AskEvent),
		providers:       providers,
		providerConfigs: providerConfigs,
		wsCh:            make(chan []byte, 1024),
		done:            make(chan struct{}),
	}
	for _, vs := range mgr.List() {
		c.registerEngine(vs)
	}
	activeID := mgr.ActiveID()
	c.active.Store(&activeID)
	go c.wsWriter()
	return c
}

// ActiveID returns the active engine ID in a lock-free manner.
func (c *WUIConnector) ActiveID() string {
	p := c.active.Load()
	if p == nil {
		return ""
	}
	return *p
}

// wsWriter is the single WS writer goroutine. All WS writes go through wsCh.
// Started in New(), stopped by closing done in Stop().
func (c *WUIConnector) wsWriter() {
	for {
		select {
		case payload := <-c.wsCh:
			ws := c.activeWS.Load()
			if ws != nil {
				_ = ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
					c.activeWS.Store(nil)
					slog.Warn("wui:ws write failed", "error", err)
				}
			}
		case <-c.done:
			return
		}
	}
}

// sendWS enqueues a payload for the wsWriter goroutine. Non-blocking: if wsCh
// is full or done is closed, the payload is dropped. Backpressure prevents
// overwhelming a slow WS client.
func (c *WUIConnector) sendWS(payload []byte) {
	select {
	case c.wsCh <- payload:
	case <-c.done:
	}
}

// activeSlot returns the slot for the active engine. Uses ActiveID() for
// lock-free read. Returns nil if active is unset or the slot is missing.
func (c *WUIConnector) activeSlot() *engineSlot {
	c.slotsMu.RLock()
	defer c.slotsMu.RUnlock()
	return c.slots[c.ActiveID()]
}

// activeEngine returns the active engine's engineClient (may be nil during
// init). Convenience wrapper for inbound WS handlers that always operate on
// the active engine.
func (c *WUIConnector) activeEngine() engineClient {
	if s := c.activeSlot(); s != nil {
		return s.engine
	}
	return nil
}

// engineHubShim is a per-engine hub.EventHandler that tags each event with
// the engine ID so onEngineEvent knows which slot to route to. One shim
// is created per registered engine and subscribed to that engine's hub.
type engineHubShim struct {
	engineID string
	c        *WUIConnector
}

func (s *engineHubShim) Handle(event hub.Event) {
	s.c.onEngineEvent(s.engineID, event)
}

// registerEngine subscribes the connector to vs.Engine's hub and creates a
// per-engine slot. Idempotent: a second call for an existing ID logs and
// returns without re-subscribing (re-subscribing would double-deliver every
// event). Exported as RegisterEngine so main.go can add engines created
// outside the connector (engine_new flow).
func (c *WUIConnector) registerEngine(vs *engine.EngineViewState) {
	if vs.Engine == nil {
		panic(fmt.Sprintf("wui: registerEngine called with nil Engine for %q", vs.ID))
	}
	c.slotsMu.Lock()
	if _, exists := c.slots[vs.ID]; exists {
		c.slotsMu.Unlock()
		slog.Warn("wui: registerEngine called twice for same engine, ignoring", "id", vs.ID)
		return
	}
	c.slotsMu.Unlock()

	h, ok := vs.Engine.Dispatcher().(*hub.Hub)
	if !ok {
		slog.Warn("wui: engine dispatcher is not *hub.Hub, skipping", "id", vs.ID)
		return
	}
	slot := &engineSlot{
		engineID:    vs.ID,
		engine:      &engineAdapter{eng: vs.Engine},
		hub:         h,
		taskToolIDs: map[string]bool{},
	}
	slot.unsubscribe = h.Subscribe(&engineHubShim{engineID: vs.ID, c: c})

	c.slotsMu.Lock()
	c.slots[vs.ID] = slot
	c.slotsMu.Unlock()
}

// RegisterEngine is the exported wrapper around registerEngine, used by
// main.go's createEngineForWebchat to register engines created after boot.
func (c *WUIConnector) RegisterEngine(vs *engine.EngineViewState) {
	c.registerEngine(vs)
}

// Start is a no-op: the HTTP/WS server is registered separately via
// RegisterChatWS in main.go, and the connector is request-driven.
func (c *WUIConnector) Start(ctx context.Context) error { return nil }

// Stop unsubscribes from all engine hubs, aborts any pending asks, and clears
// the active WS. Does NOT abort the active query — a brief disconnect (e.g.
// mobile browser backgrounding) should not interrupt the LLM.
func (c *WUIConnector) Stop() {
	close(c.done)
	c.slotsMu.Lock()
	for _, slot := range c.slots {
		if slot.unsubscribe != nil {
			slot.unsubscribe()
		}
	}
	c.slotsMu.Unlock()
	c.cleanupConn()
}

// Send is an interface no-op: webchat has no outbound platform. Nobody calls
// Send on the webchat connector; it exists solely to satisfy the
// connector.Connector contract.
func (c *WUIConnector) Send(userID, text string) error { return nil }

// Handle dispatches an event to the active engine. Kept for test
// compatibility: existing tests call c.Handle(event) to simulate hub dispatch.
// Production code routes events through engineHubShim → onEngineEvent.
func (c *WUIConnector) Handle(event hub.Event) {
	c.onEngineEvent(c.ActiveID(), event)
}

// resetQueryStats zeros all atomic fields in queryStats.
func resetQueryStats(qs *queryStats) {
	qs.inputTokens.Store(0)
	qs.outputTokens.Store(0)
	qs.cacheReadInputTokens.Store(0)
	qs.cacheCreationInputTokens.Store(0)
	qs.toolCount.Store(0)
	qs.thinkingMs.Store(0)
	qs.startMs.Store(0)
}

// accumulateStats adds the event's usage/tool/thinking contributions into
// the slot's atomic queryStats. Called by onEngineEvent for every event
// (sub-agent events included so stats reflect the full query cost).
func accumulateStats(qs *queryStats, event hub.Event) {
	if event.Type == types.EventUsage && event.Usage != nil {
		qs.inputTokens.Add(int64(event.Usage.InputTokens))
		qs.outputTokens.Add(int64(event.Usage.OutputTokens))
		qs.cacheReadInputTokens.Add(int64(event.Usage.CacheReadInputTokens))
		qs.cacheCreationInputTokens.Add(int64(event.Usage.CacheCreationInputTokens))
	}
	if event.Type == types.EventQueryStart && event.Agent == nil {
		qs.startMs.Store(time.Now().UnixMilli())
	}
	if event.Type == types.EventToolStart && event.ToolUse != nil {
		qs.toolCount.Add(1)
	}
	if event.Type == types.EventThinkingEnd && event.Thinking != nil {
		qs.thinkingMs.Add(event.Thinking.Duration.Milliseconds())
	}
}

// updateStreamState mutates streamState from an event — equivalent to TUI's
// AppendChunk/PendingToolStarted. Only called from the slot's own hub
// goroutine (onEngineEvent), so no lock needed.
func updateStreamState(ss *streamState, event hub.Event) {
	switch event.Type {
	case types.EventTextDelta:
		ss.text += event.Text
	case types.EventToolStart:
		if event.ToolUse == nil {
			break
		}
		input := ""
		if len(event.ToolUse.Input) > 0 {
			input = string(event.ToolUse.Input)
		}
		ss.tools = append(ss.tools, toolSnapshot{
			ID:    event.ToolUse.ID,
			Name:  event.ToolUse.Name,
			Input: input,
		})
	case types.EventToolEnd:
		for i := range ss.tools {
			if event.ToolResult != nil && ss.tools[i].ID == event.ToolResult.ToolUseID {
				ss.tools[i].Done = true
				ss.tools[i].Output = string(event.ToolResult.DisplayOutput)
				break
			}
		}
	case types.EventThinkingStart:
		if event.Thinking != nil {
			ss.thinking = &thinkingState{}
		}
	case types.EventThinkingDelta:
		if ss.thinking != nil && event.Thinking != nil {
			ss.thinking.Text += event.Thinking.Text
		}
	case types.EventThinkingEnd:
		if ss.thinking != nil && event.Thinking != nil {
			ss.thinking.Done = true
			ss.thinking.Duration = int64(event.Thinking.Duration)
		}
	}
}

// buildPendingBlocks builds a "streamState" JSON message from the slot's
// current streamState. Sent by onEngineEvent after a switch to give the
// client the in-flight content before live events start arriving.
func buildPendingBlocks(ss streamState) []byte {
	type thinkingJSON struct {
		Text     string `json:"text"`
		Duration int64  `json:"duration_ns"`
		Done     bool   `json:"done"`
	}
	type payload struct {
		Type     string         `json:"type"`
		Text     string         `json:"text"`
		Tools    []toolSnapshot `json:"tools"`
		Thinking *thinkingJSON  `json:"thinking,omitempty"`
	}
	tools := make([]toolSnapshot, len(ss.tools))
	copy(tools, ss.tools)
	var think *thinkingJSON
	if ss.thinking != nil {
		think = &thinkingJSON{
			Text: ss.thinking.Text, Duration: ss.thinking.Duration, Done: ss.thinking.Done,
		}
	}
	out, _ := json.Marshal(payload{
		Type: "streamState", Text: ss.text, Tools: tools, Thinking: think,
	})
	return out
}

// onEngineEvent is the per-engine event handler, called by engineHubShim for
// each event on that engine's hub. All events from all engines are processed
// here. Stats always accumulate (atomic). For the active engine, a
// streamState snapshot is sent on the first event after a switch, then live
// events go to wsCh. For inactive engines, streamState is updated in
// real-time (like TUI's background drain).
//
// Ask events from inactive engines are silently dropped: an Ask demands a UI
// prompt, and only the active engine's UI is visible.
func (c *WUIConnector) onEngineEvent(engineID string, event hub.Event) {
	if event.Type == types.EventAsk {
		if engineID != c.ActiveID() {
			return
		}
		c.handleAsk(event)
		return
	}

	c.slotsMu.RLock()
	slot := c.slots[engineID]
	c.slotsMu.RUnlock()
	if slot == nil {
		return
	}

	if event.Type == types.EventToolStart && event.Agent == nil &&
		event.ToolUse != nil && event.ToolUse.Name == "Task" {
		slot.taskToolIDs[event.ToolUse.ID] = true
	}

	accumulateStats(&slot.queryStats, event)

	isQueryEnd := event.Type == types.EventQueryEnd && event.Agent == nil

	aborted := false
	if isQueryEnd && event.Error != nil {
		if _, ok := errors.AsType[*engine.AbortError](event.Error); ok {
			aborted = true
			rewind := c.shouldAutoRewindFor(slot.engine)
			slog.Info("wui:abort", "engine", engineID, "shouldAutoRewind", rewind, "msgs", len(slot.engine.Messages()))
			if !rewind {
				interruptPayload, _ := json.Marshal(struct {
					Type  string           `json:"type"`
					Event types.QueryEvent `json:"event"`
				}{Type: "event", Event: types.QueryEvent{
					Type: types.EventTextDelta,
					Text: types.InterruptMessage,
				}})
				updateStreamState(&slot.streamState, types.QueryEvent{
					Type: types.EventTextDelta, Text: types.InterruptMessage,
				})
				c.sendWS(interruptPayload)
			}
			c.autoRewindOnAbortFor(slot.engine)
		}
	}

	payload, err := json.Marshal(struct {
		Type  string              `json:"type"`
		Event queryEventWithAbort `json:"event"`
	}{Type: "event", Event: queryEventWithAbort{QueryEvent: event, Aborted: aborted}})
	if err != nil {
		slog.Warn("wui: marshal event failed", "type", event.Type, "error", err)
		return
	}

	slog.Info("wui:event", "type", event.Type, "engine", engineID,
		"agentType", agentTypeLog(event.Agent), "parentID", parentIDLog(event.Agent))

	if !slot.active.Load() || c.activeWS.Load() == nil {
		updateStreamState(&slot.streamState, event)
		if isQueryEnd {
			slot.streamState = streamState{}
			resetQueryStats(&slot.queryStats)
			slot.taskToolIDs = make(map[string]bool)
		}
		return
	}

	if !slot.snapshotSent.Load() {
		slot.snapshotSent.Store(true)
		if slot.streamState.text != "" || len(slot.streamState.tools) > 0 || slot.streamState.thinking != nil {
			c.sendWS(buildPendingBlocks(slot.streamState))
		}
	}

	c.sendWS(payload)

	if isQueryEnd {
		if event.Error != nil && !aborted {
			c.sendWS(buildError(event.Error))
		}
		slot.streamState = streamState{}
		resetQueryStats(&slot.queryStats)
		slot.taskToolIDs = make(map[string]bool)
	}

	if event.Type == types.EventToolEnd && event.Agent == nil && event.ToolResult != nil {
		if slot.taskToolIDs[event.ToolResult.ToolUseID] {
			if taskPayload := c.buildTaskList(slot); taskPayload != nil {
				c.sendWS(taskPayload)
			}
		}
	}
}

// shouldAutoRewindFor checks whether autoRewindOnAbortFor would rewind, using
// the given engine (per-engine, not global).
func (c *WUIConnector) shouldAutoRewindFor(eng engineClient) bool {
	msgs := eng.Messages()
	lastUserIdx := utils.LastSelectableUserMessageIndex(msgs)
	if lastUserIdx < 0 {
		return true
	}
	return utils.MessagesAfterAreOnlySynthetic(msgs, lastUserIdx)
}

// autoRewindOnAbortFor mirrors TUI's tryAutoRewind, operating on the given engine.
func (c *WUIConnector) autoRewindOnAbortFor(eng engineClient) {
	if !c.shouldAutoRewindFor(eng) {
		return
	}
	msgs := eng.Messages()
	lastUserIdx := utils.LastSelectableUserMessageIndex(msgs)
	if lastUserIdx < 0 {
		return
	}
	if err := eng.RewindTo(lastUserIdx); err != nil {
		slog.Warn("wui: autoRewind failed", "idx", lastUserIdx, "error", err)
	}
}

// inputHistoryEntry is the JSONL on-disk format for the shared input history,
// matching pkg/tui/history.go historyEntry.
type inputHistoryEntry struct {
	Display   string `json:"display"`
	Timestamp int64  `json:"timestamp"`
}

// inputHistoryPath returns the shared per-engine input history JSONL path,
// mirroring pkg/tui/app.go historyPathFor. Returns "" when projectDir or
// engineID is empty (connector has no engine bound).
func (c *WUIConnector) inputHistoryPath() string {
	eng := c.activeEngine()
	if eng == nil {
		return ""
	}
	return c.inputHistoryPathFor(eng)
}

// inputHistoryPathFor computes the history path for a specific engine.
func (c *WUIConnector) inputHistoryPathFor(eng engineClient) string {
	dir := eng.ProjectDir()
	id := eng.EngineID()
	if dir == "" || id == "" {
		return ""
	}
	return filepath.Join(dir, "history", id+".jsonl")
}

// loadInputHistory reads the shared per-engine input history JSONL and returns
// the display entries oldest-first, applying the same consecutive-duplicate
// skip, empty-display skip, malformed-line skip, and maxSize cap as
// pkg/tui/history.go load(). Returns nil (not an empty slice) when the file
// doesn't exist or yields no entries so the JSON field can be omitempty.
func (c *WUIConnector) loadInputHistory() []string {
	path := c.inputHistoryPath()
	return loadInputHistoryFromFile(path)
}

// loadInputHistoryFor reads input history for a specific engine (used by
// buildConnectStatusMessageForSlot during engine_switch, when the slot is not
// yet the active engine).
func (c *WUIConnector) loadInputHistoryFor(eng engineClient) []string {
	path := c.inputHistoryPathFor(eng)
	return loadInputHistoryFromFile(path)
}

// loadInputHistoryFromFile reads the shared per-engine input history JSONL
// from path and returns the display entries oldest-first, applying the same
// consecutive-duplicate skip, empty-display skip, malformed-line skip, and
// maxSize cap as pkg/tui/history.go load(). Returns nil (not an empty slice)
// when the file doesn't exist or yields no entries so the JSON field can be
// omitempty.
func loadInputHistoryFromFile(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil // file doesn't exist yet — that's fine
	}
	defer f.Close()
	const maxSize = 1000
	var items []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry inputHistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip malformed lines
		}
		if entry.Display == "" {
			continue
		}
		if len(items) > 0 && items[len(items)-1] == entry.Display {
			continue // skip consecutive duplicates
		}
		items = append(items, entry.Display)
	}
	if len(items) > maxSize {
		items = items[len(items)-maxSize:]
	}
	return items
}

// appendInputHistory appends a single entry to the shared per-engine input
// history JSONL, mirroring pkg/tui/history.go save(). The file is purely
// append-only; consecutive-duplicate skip and cap happen on read. Errors are
// swallowed — history is best-effort and never blocks the query path.
func (c *WUIConnector) appendInputHistory(cmd string) {
	path := c.inputHistoryPath()
	if path == "" {
		return
	}
	entry := inputHistoryEntry{
		Display:   cmd,
		Timestamp: time.Now().UnixMilli(),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		return
	}
}

// handleAsk stores the AskEvent under a fresh id, builds the askOutbound
// struct (NOT marshalling *types.AskEvent directly — its fields have no json
// tags), and writes it to the active WS via sendWS.
func (c *WUIConnector) handleAsk(event hub.Event) {
	if event.Ask == nil {
		return
	}
	id := strconv.FormatInt(c.askCounter.Add(1), 10)
	c.pendingMu.Lock()
	c.pendingAsks[id] = event.Ask
	c.pendingMu.Unlock()

	kind := "permission"
	if event.Ask.Kind == types.AskInput {
		kind = "input"
	}
	out := askOutbound{
		Type:       "ask",
		ID:         id,
		Kind:       kind,
		ToolName:   event.Ask.ToolName,
		Input:      event.Ask.Input,
		Message:    event.Ask.Message,
		RuleDetail: event.Ask.RuleDetail,
		Prompt:     event.Ask.Prompt,
		Masked:     event.Ask.Masked,
		AgentType:  event.Ask.AgentType,
	}
	payload, err := json.Marshal(out)
	if err != nil {
		slog.Warn("wui: marshal ask failed", "id", id, "error", err)
		return
	}
	c.sendWS(payload)
}

// buildHistory returns a JSON "history" message containing a PAGINATED
// slice of the engine's conversation, or nil if there are no messages at all.
//
// cursor is an opaque token encoding how many messages from the END have
// already been delivered in prior pages ("0"/"" → deliver the most recent
// page). limit defaults to 30 when <= 0. The payload carries nextCursor and
// hasMore so the frontend can request further pages via history_request.
//
// The full history is still computed (needed to resolve tool_result
// cross-references); only the serialized payload shrinks. Tool summaries and
// outputs are rendered via the tool's own Description/RenderResult — the same
// path as TUI's engineMessagesToViews — so history looks identical to streaming.
func (c *WUIConnector) buildHistory(cursor string, limit int) []byte {
	slot := c.activeSlot()
	if slot == nil {
		return nil
	}
	return c.engineMessagesToUI(slot, cursor, limit)
}

// engineMessagesToUI is the per-slot core of buildHistoryMessage. It
// is called directly by handleEngineSwitch to build history for the target slot.
func (c *WUIConnector) engineMessagesToUI(slot *engineSlot, cursor string, limit int) []byte {
	msgs := slot.engine.Messages()
	if len(msgs) == 0 {
		return nil
	}
	tools := slot.engine.Tools()

	// First pass: collect all tool_results keyed by tool_use_id (same as TUI).
	toolResults := make(map[string]types.ContentBlock)
	for _, m := range msgs {
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID != "" {
				toolResults[cb.ToolUseID] = cb
			}
		}
	}

	var out []historyChatMsg
	for _, m := range msgs {
		if m.Role == types.RoleSystem {
			continue
		}
		if m.HasFlag(types.FlagMeta) || m.HasFlag(types.FlagCompactSummary) {
			continue
		}
		hm := historyChatMsg{
			ID:        m.ID,
			Role:      string(m.Role),
			StartedAt: m.Timestamp.UnixMilli(),
			Status:    "done",
		}
		for _, cb := range m.Content {
			switch cb.Type {
			case types.ContentTypeText:
				hm.Text += cb.Text
				// Match TUI's engineMessagesToViews: skip whitespace-only text
				// blocks in the ordered array so they don't consume an
				// eventIndex slot in the frontend. Legacy hm.Text concatenates
				// all text including whitespace (unchanged behavior).
				if strings.TrimSpace(cb.Text) != "" {
					hm.Blocks = append(hm.Blocks, historyBlock{Kind: "text", Text: cb.Text})
				}
			case types.ContentTypeThinking:
				if strings.TrimSpace(cb.Thinking) != "" {
					thinkingEntry := historyThinkingEntry{
						Text:       cb.Thinking,
						DurationNs: cb.ThinkingDurationNs,
					}
					hm.Thinking = append(hm.Thinking, thinkingEntry)
					hm.Blocks = append(hm.Blocks, historyBlock{Kind: "thinking", Thinking: &thinkingEntry})
				}
			case types.ContentTypeToolUse:
				entry := historyToolEntry{
					ID:      cb.ID,
					Name:    formatToolDisplayName(cb.Name),
					Summary: computeToolSummary(cb.Name, cb.Input, tools),
				}
				// Compute search/read/list classification from tool definition.
				if t, ok := tools[cb.Name]; ok {
					if ts, ok := t.(tool.ToolWithSearchOrRead); ok {
						srk := ts.IsSearchOrRead(cb.Input)
						entry.IsSearch = srk.IsSearch
						entry.IsRead = srk.IsRead
						entry.IsList = srk.IsList
						entry.IsLsp = srk.IsLsp
					}
				}
				if result, ok := toolResults[cb.ID]; ok {
					entry.IsError = result.IsError
					entry.DisplayOutput, entry.DurationNs = renderToolOutput(cb.Name, result.Content, tools)
				} else {
					entry.IsRunning = true
				}
				hm.Tools = append(hm.Tools, entry)
				hm.Blocks = append(hm.Blocks, historyBlock{Kind: "tool", Tool: &entry})
			}
		}
		if m.Usage != nil {
			hm.Usage = historyUsage{
				InputTokens:   m.Usage.InputTokens,
				OutputTokens:  m.Usage.OutputTokens,
				CacheRead:     m.Usage.CacheReadInputTokens,
				CacheCreation: m.Usage.CacheCreationInputTokens,
			}
		}
		out = append(out, hm)
	}
	if len(out) == 0 {
		return nil
	}

	// Pagination: cursor is the count of messages from the END already
	// delivered in prior pages. Cursor ""/0 → deliver the most recent page.
	// The slice is taken from the END so a growing conversation (new messages
	// appended at the back) never shifts already-delivered page offsets.
	total := len(out)
	if limit <= 0 {
		limit = 30
	}
	offset := 0
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			offset = n
		}
	}
	end := max(
		// exclusive upper bound
		total-offset, 0)
	start := max(end-limit, 0)
	page := out[start:end]
	hasMore := start > 0
	nextCursor := ""
	if hasMore {
		nextCursor = strconv.Itoa(offset + (end - start))
	}

	payload, _ := json.Marshal(struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}{Type: "history", Messages: page, NextCursor: nextCursor, HasMore: hasMore})
	return payload
}

// formatToolDisplayName mirrors TUI's logic for MCP tool names.
func formatToolDisplayName(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	parts := strings.SplitN(name, "__", 3)
	if len(parts) < 3 {
		return name
	}
	return parts[1] + " - " + parts[2] + " (MCP)"
}

// computeToolSummary calls the tool's Description — same as TUI.
func computeToolSummary(name string, input json.RawMessage, tools map[string]tool.Tool) string {
	t, ok := tools[name]
	if !ok {
		return ""
	}
	desc, err := t.Description(input)
	if err != nil {
		return ""
	}
	return desc
}

// renderToolOutput renders persisted tool_result content via the tool's
// RenderResult — same logic as TUI's renderToolOutput in app.go.
func renderToolOutput(toolName string, raw json.RawMessage, tools map[string]tool.Tool) (string, int64) {
	if len(raw) == 0 {
		return "", 0
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n"), 0
			}
		}
		return string(raw), 0
	}

	rest := s
	elapsed := int64(0)
	if strings.HasPrefix(rest, "[Tool spent ") {
		if idx := strings.Index(rest, "]"); idx >= 0 {
			inner := strings.TrimPrefix(rest[:idx+1], "[Tool spent ")
			inner = strings.TrimSuffix(inner, "s]")
			if sec, err := strconv.ParseFloat(inner, 64); err == nil {
				elapsed = int64(sec * float64(time.Second))
			}
			rest = rest[idx+1:]
		}
	}
	if rest == "" {
		return "", elapsed
	}

	if strings.HasPrefix(rest, "<persisted-output>") {
		if data := readPersistedFile(rest); data != nil {
			if r, ok := renderViaTool(toolName, data, tools); ok && r != "" {
				return r, elapsed
			}
		}
		return extractPersistedPreview(rest), elapsed
	}

	if r, ok := renderViaTool(toolName, json.RawMessage(rest), tools); ok {
		return r, elapsed
	}

	var obj struct {
		Output string `json:"output"`
	}
	if json.Unmarshal([]byte(rest), &obj) == nil && obj.Output != "" {
		return obj.Output, elapsed
	}
	return rest, elapsed
}

// renderViaTool finds the tool, decodes the raw JSON to its concrete result
// type via DecodeResult, then calls RenderResult. Returns (rendered, false)
// if the tool is not in the map so callers can apply their own fallback.
func renderViaTool(toolName string, raw json.RawMessage, tools map[string]tool.Tool) (string, bool) {
	t, ok := tools[toolName]
	if !ok {
		return "", false
	}
	if dt, ok := t.(tool.ToolWithDecodeResult); ok {
		if v, err := dt.DecodeResult(raw); err == nil {
			return t.RenderResult(v), true
		}
	}
	return t.RenderResult(string(raw)), true
}

func readPersistedFile(s string) json.RawMessage {
	_, after, ok := strings.Cut(s, "Full output saved to: ")
	if !ok {
		return nil
	}
	pathEnd := strings.IndexByte(after, '\n')
	if pathEnd < 0 {
		pathEnd = len(after)
	}
	filePath := strings.TrimSpace(after[:pathEnd])
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func extractPersistedPreview(s string) string {
	_, after, ok := strings.Cut(s, "Preview (")
	if !ok {
		return "<output saved to file>"
	}
	_, after0, ok0 := strings.Cut(after, "):\n")
	if !ok0 {
		return "<output saved to file>"
	}
	preview := after0
	lines := strings.SplitN(preview, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
		lines = append(lines, "...")
	}
	result := strings.Join(lines, "\n")
	if result == "" {
		return "<output saved to file>"
	}
	return result
}

// historyChatMsg is the wire shape for a single history message.
type historyChatMsg struct {
	ID        string                 `json:"id"`
	Role      string                 `json:"role"`
	Text      string                 `json:"text"`
	Thinking  []historyThinkingEntry `json:"thinking"`
	Tools     []historyToolEntry     `json:"tools"`
	Blocks    []historyBlock         `json:"blocks,omitempty"`
	Usage     historyUsage           `json:"usage"`
	Error     string                 `json:"error"`
	Status    string                 `json:"status"`
	StartedAt int64                  `json:"startedAt"`
}

// historyBlock is one entry in the ordered Blocks array. It mirrors a single
// content block from the engine message's Content[], preserving original event
// order so the frontend can interleave text/thinking/tool correctly. The legacy
// Text/Thinking/Tools fields concatenate same-type blocks and lose ordering;
// Blocks is authoritative when present.
type historyBlock struct {
	Kind     string                `json:"kind"`               // "text" | "thinking" | "tool"
	Text     string                `json:"text,omitempty"`     // kind == "text"
	Thinking *historyThinkingEntry `json:"thinking,omitempty"` // kind == "thinking"
	Tool     *historyToolEntry     `json:"tool,omitempty"`     // kind == "tool"
}

type historyThinkingEntry struct {
	Text       string `json:"text"`
	DurationNs int64  `json:"durationNs,omitempty"`
}

type historyToolEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Summary       string `json:"summary,omitempty"`
	DisplayOutput string `json:"displayOutput,omitempty"`
	IsError       bool   `json:"isError,omitempty"`
	IsRunning     bool   `json:"isRunning,omitempty"`
	DurationNs    int64  `json:"durationNs,omitempty"`
	IsSearch      bool   `json:"is_search,omitempty"`
	IsRead        bool   `json:"is_read,omitempty"`
	IsList        bool   `json:"is_list,omitempty"`
	IsLsp         bool   `json:"is_lsp,omitempty"`
}

type historyUsage struct {
	InputTokens   int `json:"inputTokens"`
	OutputTokens  int `json:"outputTokens"`
	CacheRead     int `json:"cacheRead"`
	CacheCreation int `json:"cacheCreation"`
}

// buildError formats a query_end error payload for the WS client.
func buildError(err error) []byte {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	out, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{Type: "error", Message: msg})
	return out
}

// askOutbound is the wire shape for an Ask event. It exists because
// *types.AskEvent fields have no json tags — marshalling it directly would
// emit PascalCase keys. The React client expects snake_case (Phase 0).
type askOutbound struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	ToolName   string          `json:"tool_name"`
	Input      json.RawMessage `json:"input,omitempty"`
	Message    string          `json:"message,omitempty"`
	RuleDetail string          `json:"rule_detail,omitempty"`
	Prompt     string          `json:"prompt,omitempty"`
	Masked     bool            `json:"masked,omitempty"`
	AgentType  string          `json:"agent_type,omitempty"`
}

func agentTypeLog(m *types.AgentMeta) string {
	if m == nil {
		return ""
	}
	return m.AgentType
}

func parentIDLog(m *types.AgentMeta) string {
	if m == nil {
		return ""
	}
	return m.ParentToolUseID
}

// taskListOutbound is the wire payload for a task list snapshot. The frontend
// renders the panel from this. Idempotent: a newer task_list replaces the prior
// one entirely.
type taskListOutbound struct {
	Type  string             `json:"type"` // "task_list"
	Tasks []taskListWireItem `json:"tasks"`
}

// taskListWireItem is the wire shape for a single task. Status mirrors the
// engine's task.TaskStatus string values (pending|in_progress|completed).
type taskListWireItem struct {
	ID         string   `json:"id"`
	Subject    string   `json:"subject"`
	Status     string   `json:"status"`
	Owner      string   `json:"owner,omitempty"`
	BlockedBy  []string `json:"blockedBy,omitempty"`
	ActiveForm string   `json:"activeForm,omitempty"`
}

// buildTaskList reads the slot's engine task list, filters internal
// tasks, resolves blockedBy from task IDs to subjects, and returns the JSON
// task_list payload. Returns nil when there are no user-visible tasks.
func (c *WUIConnector) buildTaskList(slot *engineSlot) []byte {
	if slot == nil {
		return nil
	}
	tl := slot.engine.TaskList()
	if tl == nil || tl.Dir() == "" {
		return nil
	}
	allTasks, err := tl.ListTasks()
	if err != nil {
		return nil
	}

	completedIDs := make(map[string]bool)
	subjectByID := make(map[string]string)
	for _, t := range allTasks {
		if t.Status == task.StatusCompleted {
			completedIDs[t.ID] = true
		}
		subjectByID[t.ID] = t.Subject
	}

	var items []taskListWireItem
	for _, t := range allTasks {
		if t.Metadata != nil && t.Metadata["_internal"] != nil {
			continue
		}
		var activeBlockedBy []string
		for _, id := range t.BlockedBy {
			if !completedIDs[id] {
				activeBlockedBy = append(activeBlockedBy, subjectByID[id])
			}
		}
		items = append(items, taskListWireItem{
			ID:         t.ID,
			Subject:    t.Subject,
			Status:     string(t.Status),
			Owner:      t.Owner,
			BlockedBy:  activeBlockedBy,
			ActiveForm: t.ActiveForm,
		})
	}
	if len(items) == 0 {
		return nil
	}

	payload, _ := json.Marshal(taskListOutbound{Type: "task_list", Tasks: items})

	allDone := true
	for _, item := range items {
		if item.Status != string(task.StatusCompleted) {
			allDone = false
			break
		}
	}
	if allDone {
		_ = tl.CleanupCompleted()
	}
	return payload
}

// sessionListItem is the wire shape for a session entry in session_list.
type sessionListItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

// configModelItem is one entry in the config message's models array.
type configModelItem struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// configCurrent is the active provider/model in the config message.
type configCurrent struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// buildConfig is the per-slot config builder, used by
// handleEngineSwitch for non-active slots.
func (c *WUIConnector) buildConfig(slot *engineSlot) []byte {
	var currentProvider string
	if p := slot.engine.Provider(); p != nil {
		currentProvider = p.Name()
	}
	currentModel := slot.engine.Model()
	present := func(name string) bool { _, ok := c.providers[name]; return ok }
	items := config.BuildModelItems(c.providerConfigs, present, currentProvider, currentModel)

	out := struct {
		Type    string            `json:"type"`
		Models  []configModelItem `json:"models"`
		Current configCurrent     `json:"current"`
	}{
		Type:    "config",
		Models:  []configModelItem{},
		Current: configCurrent{Provider: currentProvider, Model: currentModel},
	}
	for _, it := range items {
		out.Models = append(out.Models, configModelItem{Provider: it.Provider, Model: it.Model})
	}
	payload, _ := json.Marshal(out)
	return payload
}

// buildConnectStatus builds connect_status for a specific slot.
// The client calls resetAllState on every connect_status, then loads the
// subsequent history frame. engineID and engineName are included so the
// frontend can track which engine is active during a switch.
// Stats (usage/toolCount/thinkingMs/queryStartMs) are NOT included here —
// they are sent as a separate "stats" frame AFTER replay to avoid
// double-counting (connect_status arrives before replay, so including stats
// here would cause the client to restore them and then re-accumulate the
// same deltas from replayed events).
func (c *WUIConnector) buildConnectStatus(slot *engineSlot) []byte {
	eng := slot.engine
	engineName := c.engineNameForSlot(slot)
	inputHistory := c.loadInputHistoryFor(eng)
	payload, _ := json.Marshal(struct {
		Type         string   `json:"type"`
		Connected    bool     `json:"connected"`
		Agent        string   `json:"agent"`
		Model        string   `json:"model"`
		SessionID    string   `json:"sessionID"`
		InputHistory []string `json:"inputHistory,omitempty"`
		EngineID     string   `json:"engineID"`
		EngineName   string   `json:"engineName"`
	}{
		Type:         "connect_status",
		Connected:    true,
		Agent:        engineName,
		Model:        eng.Model(),
		SessionID:    eng.SessionID(),
		InputHistory: inputHistory,
		EngineID:     slot.engineID,
		EngineName:   engineName,
	})
	return payload
}

// buildStats builds the stats payload from the slot's atomic
// queryStats. No lock needed — all fields are atomic.
func (c *WUIConnector) buildStats(slot *engineSlot) []byte {
	qs := &slot.queryStats
	payload, _ := json.Marshal(struct {
		Type         string      `json:"type"`
		Usage        types.Usage `json:"usage"`
		QueryStartMs int64       `json:"queryStartMs"`
		ToolCount    int         `json:"toolCount"`
		ThinkingMs   int64       `json:"thinkingMs"`
	}{
		Type: "stats",
		Usage: types.Usage{
			InputTokens:              int(qs.inputTokens.Load()),
			OutputTokens:             int(qs.outputTokens.Load()),
			CacheReadInputTokens:     int(qs.cacheReadInputTokens.Load()),
			CacheCreationInputTokens: int(qs.cacheCreationInputTokens.Load()),
		},
		QueryStartMs: qs.startMs.Load(),
		ToolCount:    int(qs.toolCount.Load()),
		ThinkingMs:   qs.thinkingMs.Load(),
	})
	return payload
}

// engineNameForSlot resolves the engine's display name from the manager. Falls
// back to the engine ID if the manager lookup fails.
func (c *WUIConnector) engineNameForSlot(slot *engineSlot) string {
	if c.mgr != nil {
		if vs := c.mgr.Get(slot.engineID); vs != nil {
			return vs.Name
		}
	}
	return slot.engineID
}

// buildSessionList returns a session_list payload for the active engine
// with up to 50 sessions, or nil when the store returns none.
func (c *WUIConnector) buildSessionList() []byte {
	eng := c.activeEngine()
	if eng == nil {
		return nil
	}
	sessions, err := eng.ListSessions(50)
	if err != nil || len(sessions) == 0 {
		return nil
	}
	items := make([]sessionListItem, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, sessionListItem{
			ID:        s.SessionID,
			Title:     s.Title,
			UpdatedAt: s.UpdatedAt.UnixMilli(),
		})
	}
	payload, _ := json.Marshal(struct {
		Type     string            `json:"type"`
		Sessions []sessionListItem `json:"sessions"`
	}{Type: "session_list", Sessions: items})
	return payload
}

// buildEngineListMessage returns an engine_list payload with all engines from
// the manager and the active engine ID. Reads mgr.Snapshot() (lock-safe).
type engineListItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

func (c *WUIConnector) buildEngineList() []byte {
	var views []engine.EngineViewSnapshot
	activeID := c.ActiveID()
	if c.mgr != nil {
		views, activeID = c.mgr.Snapshot()
	}
	items := make([]engineListItem, 0, len(views))
	for _, v := range views {
		items = append(items, engineListItem{
			ID:    v.ID,
			Name:  v.Name,
			Model: v.Model,
		})
	}
	payload, _ := json.Marshal(struct {
		Type     string           `json:"type"`
		Engines  []engineListItem `json:"engines"`
		ActiveID string           `json:"activeID"`
	}{Type: "engine_list", Engines: items, ActiveID: activeID})
	return payload
}

// buildSessionBusy is the fixed error frame for busy-guarded handlers.
func buildSessionBusy() []byte {
	out, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{Type: "error", Message: "Session is busy — please wait for the current request to finish, then try again"})
	return out
}

// handleSessionSwitch loads the target session into the active engine, then
// pushes connect_status + history + config + stats. Rejects when the engine
// is streaming so the active turn is never disturbed.
func (c *WUIConnector) handleSessionSwitch(sessionID string) {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	if eng.IsBusy() {
		c.sendWS(buildSessionBusy())
		return
	}
	if err := eng.SwitchSession(sessionID); err != nil {
		c.sendWS(buildError(err))
		return
	}
	slot := c.activeSlot()
	if slot == nil {
		return
	}
	if c.activeWS.Load() != nil {
		c.sendMetadata(slot)
	}
}

// handleSessionNew creates a fresh session on the active engine, then pushes
// connect_status + config + stats. Rejects when the engine is streaming.
func (c *WUIConnector) handleSessionNew() {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	if eng.IsBusy() {
		c.sendWS(buildSessionBusy())
		return
	}
	if _, err := eng.NewSession(); err != nil {
		c.sendWS(buildError(err))
		return
	}
	slot := c.activeSlot()
	if slot == nil {
		return
	}
	if c.activeWS.Load() != nil {
		c.sendMetadata(slot)
	}
	if payload := c.buildSessionList(); payload != nil {
		c.sendWS(payload)
	}
}

// handleModelSwitch switches the active engine's provider + model, syncs
// capabilities, then pushes fresh connect_status + config so the header
// updates immediately.
func (c *WUIConnector) handleModelSwitch(providerName, modelName string) {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	if eng.IsBusy() {
		c.sendWS(buildSessionBusy())
		return
	}
	provider, ok := c.providers[providerName]
	if !ok {
		c.sendWS(buildError(fmt.Errorf("unknown provider: %s", providerName)))
		return
	}
	cfgProv := c.providerConfigs[providerName]
	if cfgProv == nil {
		c.sendWS(buildError(fmt.Errorf("no config for provider %s", providerName)))
		return
	}
	if !cfgProv.HasModel(modelName) {
		c.sendWS(buildError(fmt.Errorf("model %q not found in provider %s", modelName, providerName)))
		return
	}
	eng.SetProvider(provider)
	eng.SetModel(modelName)
	eng.SetMaxTokens(cfgProv.ResolveMaxTokens(modelName))
	eng.SetInputModalities(cfgProv.ResolveInput(modelName))
	eng.UpdateAutoCompactConfig(engine.AutoCompactConfig{
		ContextWindow:          cfgProv.ResolveContext(modelName),
		MaxConsecutiveFailures: 3,
	})

	if c.mgr != nil {
		fullModel := providerName + "/" + modelName
		c.mgr.SetActiveModel(fullModel)
		if err := c.mgr.PersistMeta(eng.ProjectDir()); err != nil {
			slog.Warn("wui: failed to persist model selection", "error", err)
		}
	}

	slog.Info("wui:model switched", "provider", providerName, "model", modelName)
}

// SetCreateEngineFn injects the engine creation closure used by engine_new.
// main.go calls this to wire engineFactory + engineMgr + store into the
// connector.
func (c *WUIConnector) SetCreateEngineFn(fn func(name string) (string, error)) {
	c.createEngine = fn
}

// sendMetadata sends a composite metadata message containing connect_status,
// config, engine_list, task_list, history, and stats. Does NOT include
// streamState — that is sent by onEngineEvent as a separate message.
func (c *WUIConnector) sendMetadata(slot *engineSlot) {
	type metaPayload struct {
		Type    string          `json:"type"`
		Connect json.RawMessage `json:"connect"`
		Config  json.RawMessage `json:"config"`
		Engines json.RawMessage `json:"engines"`
		Tasks   json.RawMessage `json:"tasks,omitempty"`
		History json.RawMessage `json:"history"`
		Stats   json.RawMessage `json:"stats"`
	}

	payload, _ := json.Marshal(metaPayload{
		Type:    "metadata",
		Connect: c.buildConnectStatus(slot),
		Config:  c.buildConfig(slot),
		Engines: c.buildEngineList(),
		Tasks:   c.buildTaskList(slot),
		History: c.engineMessagesToUI(slot, "", 30),
		Stats:   c.buildStats(slot),
	})
	c.sendWS(payload)
}

// switchEngine is the unified engine switch (pointer swap only). Deactivates
// old engine (its events start updating streamState), flips active ID, syncs
// EngineManager, sends metadata (reads engine state + atomic stats, NOT
// streamState), resets snapshotSent flag, and activates new engine.
// NEVER reads streamState — snapshot is deferred to onEngineEvent.
func (c *WUIConnector) switchEngine(newID string) {
	c.slotsMu.RLock()
	oldID := c.ActiveID()
	oldSlot := c.slots[oldID]
	newSlot := c.slots[newID]
	c.slotsMu.RUnlock()
	if newSlot == nil {
		c.sendWS(buildError(fmt.Errorf("unknown engine: %s", newID)))
		return
	}

	if oldSlot != nil {
		oldSlot.active.Store(false)
	}

	newIDCopy := newID
	c.active.Store(&newIDCopy)

	if c.mgr != nil {
		if err := c.mgr.SetActive(newID); err != nil {
			slog.Warn("wui:switchEngine:SetActive failed", "error", err)
		}
	}

	if c.activeWS.Load() != nil {
		c.sendMetadata(newSlot)
	}

	newSlot.snapshotSent.Store(false)
	newSlot.active.Store(true)
}

// handleEngineSwitch is kept for test compatibility and readLoop dispatch.
// Delegates to switchEngine.
func (c *WUIConnector) handleEngineSwitch(engineID string) {
	c.switchEngine(engineID)
}

// handleEngineNew creates a new engine via the injected createEngine closure,
// then switches to it. If createEngine is nil (not configured), sends an error.
func (c *WUIConnector) handleEngineNew(name string) {
	if c.createEngine == nil {
		c.sendWS(buildError(fmt.Errorf("engine creation not configured")))
		return
	}
	engineID, err := c.createEngine(name)
	if err != nil {
		c.sendWS(buildError(err))
		return
	}
	c.sendWS(c.buildEngineList())
	c.switchEngine(engineID)
}
