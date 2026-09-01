// Package wui implements the gbot web chat connector: an HTTP+WS server
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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/quota"
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
	QueryWithContent(ctx context.Context, content []types.ContentBlock, systemPrompt string)
	IsBusy() bool
	Messages() []types.Message
	QueryStartMsgIdx() int
	Tools() map[string]tool.Tool
	EnqueueAttachment(item types.QueuedItem)
	Abort()
	RewindTo(idx int) error
	RemoveAttachment(uuid string) bool
	PendingAttachments() []types.QueuedItem
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
	ContextWindow() int
	GetContextTokens() int
	ContextBreakdown() *engine.ContextBreakdown
	SetProvider(provider llm.Provider)
	SetModel(model string)
	Provider() llm.Provider
	SetThinking(effort llm.Effort) error
	Thinking() llm.Effort
	SetMaxTokens(n int)
	SetInputModalities(modalities []string)
	UpdateAutoCompactConfig(cfg engine.AutoCompactConfig)
	// PreCompactMessages pages SQLite messages that live before the last
	// compact boundary. See engineAdapter.PreCompactMessages for contract.
	PreCompactMessages(delivered, limit int) (msgs []*short.TranscriptMessage, total int, hasBoundary bool)
	ManualCompact(ctx context.Context, userMsg types.Message, customInstructions string) (*short.CompactResult, error)
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
func (a *engineAdapter) QueryWithContent(ctx context.Context, content []types.ContentBlock, systemPrompt string) {
	a.eng.QueryWithContent(ctx, content, systemPrompt)
}
func (a *engineAdapter) IsBusy() bool                { return a.eng.IsBusy() }
func (a *engineAdapter) Messages() []types.Message   { return a.eng.Messages() }
func (a *engineAdapter) QueryStartMsgIdx() int       { return a.eng.QueryStartMsgIdx() }
func (a *engineAdapter) Tools() map[string]tool.Tool { return a.eng.Tools() }
func (a *engineAdapter) EnqueueAttachment(item types.QueuedItem) {
	a.eng.EnqueueAttachment(item)
}
func (a *engineAdapter) Abort()                            { a.eng.Abort() }
func (a *engineAdapter) RemoveAttachment(uuid string) bool { return a.eng.RemoveAttachment(uuid) }
func (a *engineAdapter) PendingAttachments() []types.QueuedItem {
	return a.eng.PendingAttachments()
}
func (a *engineAdapter) SystemPrompt() string { return a.eng.SystemPrompt() }
func (a *engineAdapter) TaskList() *task.List { return a.eng.TaskList() }
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
func (a *engineAdapter) SessionID() string     { return a.eng.SessionID() }
func (a *engineAdapter) EngineID() string      { return a.eng.EngineID() }
func (a *engineAdapter) Model() string         { return a.eng.Model() }
func (a *engineAdapter) ProjectDir() string    { return a.eng.ProjectDir() }
func (a *engineAdapter) ContextWindow() int    { return a.eng.ContextWindow() }
func (a *engineAdapter) GetContextTokens() int { return a.eng.GetContextTokens() }
func (a *engineAdapter) ContextBreakdown() *engine.ContextBreakdown {
	return a.eng.ContextBreakdown()
}

func (a *engineAdapter) SetProvider(p llm.Provider)     { a.eng.SetProvider(p) }
func (a *engineAdapter) SetModel(m string)              { a.eng.SetModel(m) }
func (a *engineAdapter) Provider() llm.Provider         { return a.eng.Provider() }
func (a *engineAdapter) SetThinking(e llm.Effort) error { return a.eng.SetThinking(e) }
func (a *engineAdapter) Thinking() llm.Effort           { return a.eng.Thinking() }
func (a *engineAdapter) SetMaxTokens(n int)             { a.eng.SetMaxTokens(n) }
func (a *engineAdapter) SetInputModalities(m []string)  { a.eng.SetInputModalities(m) }
func (a *engineAdapter) UpdateAutoCompactConfig(cfg engine.AutoCompactConfig) {
	a.eng.UpdateAutoCompactConfig(cfg)
}

// PreCompactMessages pages SQLite messages that live before the engine's last
// compact boundary. Delegates to the standalone preCompactMessages helper so
// the core logic can be tested without constructing a full *engine.Engine.
// Returns nil/0/false when the engine has no store or no boundary exists.
func (a *engineAdapter) PreCompactMessages(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
	store := a.eng.Store()
	if store == nil {
		return nil, 0, false
	}
	return preCompactMessages(store, a.eng.SessionID(), delivered, limit)
}

func (a *engineAdapter) ManualCompact(ctx context.Context, userMsg types.Message, customInstructions string) (*short.CompactResult, error) {
	return a.eng.ManualCompact(ctx, userMsg, customInstructions)
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

// thumbCache memoizes resized history thumbnails so buildHistoryChatMsg
// does not re-decode + re-scale + re-base64 the same image on every
// history_request. Key is "<path>|<mtime>" so a replaced file invalidates.
// Bounded to ~100 entries via FIFO eviction (LRU is aspirational — a simple
// map + insertion-order slice is fine for this workload, since history is
// rebuilt in temporal order each time).
type thumbCache struct {
	mu    sync.Mutex
	order []string
	m     map[string]string
	cap   int
}

func newThumbCache() *thumbCache {
	return &thumbCache{m: map[string]string{}, cap: 100}
}

func (tc *thumbCache) get(key string) (string, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	v, ok := tc.m[key]
	return v, ok
}

func (tc *thumbCache) put(key, dataURL string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if _, exists := tc.m[key]; exists {
		tc.m[key] = dataURL
		// Move-to-back: a re-put on an existing key refreshes its position
		// in the FIFO order so it is not evicted before newer entries. This
		// gives the cache LRU-like semantics for the workload where the
		// same path is requested multiple times across history refreshes.
		for i, k := range tc.order {
			if k == key {
				tc.order = append(tc.order[:i], tc.order[i+1:]...)
				break
			}
		}
		tc.order = append(tc.order, key)
		return
	}
	tc.m[key] = dataURL
	tc.order = append(tc.order, key)
	if len(tc.order) > tc.cap {
		evict := tc.order[0]
		tc.order = tc.order[1:]
		delete(tc.m, evict)
	}
}

// streamBlock is one node in the streaming Block[] tree, matching client
// model.ts Block type. JSON tags produce the exact field names the client expects.
type streamBlock struct {
	Kind          string        `json:"kind"`                    // "text" | "thinking" | "tool"
	ID            string        `json:"id"`                      // all kinds
	Text          string        `json:"text,omitempty"`          // kind == "text" | "thinking"
	Name          string        `json:"name,omitempty"`          // kind == "tool"
	Summary       string        `json:"summary,omitempty"`       // kind == "tool"
	IsSearch      bool          `json:"isSearch,omitempty"`      // kind == "tool"
	IsRead        bool          `json:"isRead,omitempty"`        // kind == "tool"
	IsList        bool          `json:"isList,omitempty"`        // kind == "tool"
	IsLsp         bool          `json:"isLsp,omitempty"`         // kind == "tool"
	IsWeb         bool          `json:"isWeb,omitempty"`         // kind == "tool"
	State         string        `json:"state,omitempty"`         // kind == "tool": "running" | "done" | "error"
	DisplayOutput string        `json:"displayOutput,omitempty"` // kind == "tool"
	TimingNs      int64         `json:"timingNs,omitempty"`      // kind == "tool"
	DurationNs    int64         `json:"durationNs,omitempty"`    // kind == "thinking"
	Active        bool          `json:"active,omitempty"`        // kind == "thinking"
	StartedAt     int64         `json:"startedAt,omitempty"`     // kind == "thinking" | "tool"
	Children      []streamBlock `json:"children"`                // kind == "tool"
}

// streamState is the per-engine real-time streaming state, equivalent to TUI's
// ReplState. Protected by slot.ssMu (hub goroutine in onEngineEvent, readLoop
// goroutine in sendMetadata). Reset on query_end.
type streamState struct {
	blocks []streamBlock
}

// engineSlot holds the per-engine state. Each engine gets its own streamState,
// queryStats, and taskToolIDs so background engines accumulate state
// independently of the active one. The active flag + ssMu mutex are
// atomic because they are set by switchEngine (readLoop goroutine) and read
// by onEngineEvent (hub goroutine).
type engineSlot struct {
	engineID    string
	engine      engineClient
	hub         *hub.Hub
	unsubscribe func()
	ssMu        sync.Mutex
	streamState streamState
	queryStats  queryStats
	taskToolIDs map[string]bool
	active      atomic.Bool
}

// wsMsg carries one outbound WS frame through wsCh. isBinary selects the
// gorilla opcode (TextMessage vs BinaryMessage) in wsWriter, so a single
// channel + single writer goroutine owns both frame types and FIFO ordering
// is the only synchronization needed for file_start → binary → file_end.
type wsMsg struct {
	data     []byte
	isBinary bool
}

// WUIConnector implements connector.Connector for the web chat. It owns
// an EngineManager, subscribes to every engine's hub, and routes events to
// the single active WS connection. Inbound WS messages operate on the
// active engine; engine_switch swaps the active engine via atomic flag flip
// and pushes metadata (with embedded streamState snapshot).
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
	// Takeover (serveChatWS) is the one exception: it uses WriteControl
	// (gorilla's only concurrency-safe write method) to send a close frame
	// directly, bypassing wsCh.
	wsCh chan wsMsg

	// done signals the wsWriter goroutine to exit during shutdown.
	done chan struct{}

	// sendFileMu serializes concurrent SendFile calls so each file's
	// file_start → chunk → file_end frame sequence is contiguous on wsCh
	// (the executor runs concurrency-safe tools in parallel; see runTools.go).
	sendFileMu sync.Mutex

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

	// mediaCache saves files received via the WS chunked-upload path. nil
	// until SetMediaCache is called — handlers must check for nil and
	// degrade to text-only when unset.
	mediaCache *media.Store

	// thumbs memoizes resized history thumbnails (data URL form) so
	// buildHistoryChatMsg does not re-decode on every history_request.
	thumbs *thumbCache

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
		wsCh:            make(chan wsMsg, 1024),
		done:            make(chan struct{}),
		thumbs:          newThumbCache(),
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

// wsWriter is the single WS writer goroutine. All outbound frames — text and
// binary — go through wsCh. Started in New(), stopped by closing done in Stop().
func (c *WUIConnector) wsWriter() {
	for {
		select {
		case msg := <-c.wsCh:
			ws := c.activeWS.Load()
			if ws != nil {
				opcode := websocket.TextMessage
				if msg.isBinary {
					opcode = websocket.BinaryMessage
				}
				_ = ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
				err := ws.WriteMessage(opcode, msg.data)
				if err != nil {
					c.activeWS.Store(nil)
					slog.Warn("wui:ws write failed", "error", err)
				}
			}
		case <-c.done:
			return
		}
	}
}

// sendBinaryChunk enqueues a binary frame for wsWriter, mirroring sendWS's
// blocking select (no default — blocks until wsWriter drains a slot or done
// closes). Binary chunks cannot be dropped (dropping corrupts the file), and
// wsWriter always drains wsCh, so this never deadlocks on shutdown: the only
// escape is <-c.done.
//
// The data is copied because callers (SendFile) reuse the read buffer across
// loop iterations — without a copy, a later ReadFull would overwrite the
// bytes of a chunk still buffered in wsCh. android.go avoids this by writing
// each chunk synchronously; the buffered channel here requires ownership
// transfer.
func (c *WUIConnector) sendBinaryChunk(data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case c.wsCh <- wsMsg{data: cp, isBinary: true}:
	case <-c.done:
	}
}

// sendWS enqueues a payload for the wsWriter goroutine.
// Logs a warning if wsCh is full (slow WS client).
func (c *WUIConnector) sendWS(payload []byte) {
	select {
	case c.wsCh <- wsMsg{data: payload, isBinary: false}:
		return
	case <-c.done:
		return
	default:
		// wsCh full — warn but still block (don't drop events).
		slog.Warn("wui:sendWS blocked (wsCh full)", "len", len(c.wsCh), "cap", cap(c.wsCh))
		select {
		case c.wsCh <- wsMsg{data: payload, isBinary: false}:
		case <-c.done:
		}
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

// ObserveLLM resolves the active engine's provider/model for the game observe
// handler. Any missing piece (no active engine, nil provider, empty model)
// reports not-ok — the handler answers 503 instead of attempting a call it
// cannot make.
func (c *WUIConnector) ObserveLLM() (llm.Provider, string, bool) {
	eng := c.activeEngine()
	if eng == nil {
		return nil, "", false
	}
	p, m := eng.Provider(), eng.Model()
	if p == nil || m == "" {
		return nil, "", false
	}
	return p, m, true
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
// main.go's createEngineForWUI to register engines created after boot.
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

// Send is an interface no-op: wui has no outbound platform. Nobody calls
// Send on the wui connector; it exists solely to satisfy the
// connector.Connector contract.
func (c *WUIConnector) Send(userID, text string) error { return nil }

// fileStartMsg is the wire shape of the file_start text frame (server → browser).
type fileStartMsg struct {
	Type string `json:"type"` // "file_start"
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// fileEndMsg is the wire shape of the file_end text frame (server → browser).
type fileEndMsg struct {
	Type string `json:"type"` // "file_end"
	Name string `json:"name"`
}

// fileChunkSize is the max payload per binary frame, mirroring
// pkg/tool/computer/android.go's sendFileChunkSize (256 KiB).
const fileChunkSize = 256 * 1024

// SendFile streams a local file to the active WS client as a chunked binary
// sequence: a file_start text frame, 256 KiB binary frames, then a file_end
// text frame. Satisfies send.FileSender so the Send tool can route file
// deliveries to the browser. Caption is dropped — the file frames carry only
// the file, matching WeChat's media-only behavior.
//
// sendFileMu is held across the entire sequence so concurrent SendFile calls
// (the Send tool is concurrency-safe) cannot interleave their frames on wsCh.
// The file is streamed from disk via io.ReadFull so peak memory is one chunk
// regardless of size — there is no cap.
func (c *WUIConnector) SendFile(_ context.Context, filePath, _ string) error {
	c.sendFileMu.Lock()
	defer c.sendFileMu.Unlock()

	fi, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("wui: send file: %w", err)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("wui: read file: %w", err)
	}
	defer f.Close()

	startPayload, err := json.Marshal(fileStartMsg{
		Type: "file_start",
		Name: filepath.Base(filePath),
		Mime: media.MimeFromExt(filepath.Ext(filePath)),
		Size: fi.Size(),
	})
	if err != nil {
		return fmt.Errorf("wui: marshal file_start: %w", err)
	}
	c.sendWS(startPayload)

	buf := make([]byte, fileChunkSize)
	for {
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			c.sendBinaryChunk(buf[:n])
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("wui: read file: %w", rerr)
		}
	}

	endPayload, err := json.Marshal(fileEndMsg{
		Type: "file_end",
		Name: filepath.Base(filePath),
	})
	if err != nil {
		return fmt.Errorf("wui: marshal file_end: %w", err)
	}
	c.sendWS(endPayload)
	return nil
}

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

// findBlock searches the block tree depth-first for a tool block with the
// given ID. Returns nil if not found.
func findBlock(blocks []streamBlock, id string) *streamBlock {
	for i := range blocks {
		if blocks[i].Kind == "tool" && blocks[i].ID == id {
			return &blocks[i]
		}
		if len(blocks[i].Children) > 0 {
			if found := findBlock(blocks[i].Children, id); found != nil {
				return found
			}
		}
	}
	return nil
}

// targetList returns the block slice that an event should append to.
// For sub-agent events (Agent != nil), it finds the parent tool's children.
// For top-level events, it returns the root blocks slice.
func targetList(ss *streamState, event hub.Event) *[]streamBlock {
	if event.Agent != nil && event.Agent.ParentToolUseID != "" {
		parent := findBlock(ss.blocks, event.Agent.ParentToolUseID)
		if parent != nil {
			return &parent.Children
		}
		return nil
	}
	return &ss.blocks
}

// updateStreamState mutates streamState from an event. Tree-aware: sub-agent
// events nest into the parent tool's children. Only called from the slot's
// own hub goroutine (onEngineEvent), so no lock needed.
func updateStreamState(ss *streamState, event hub.Event) {
	switch event.Type {
	case types.EventTextDelta:
		list := targetList(ss, event)
		if list == nil {
			return
		}
		if n := len(*list); n > 0 && (*list)[n-1].Kind == "text" {
			(*list)[n-1].Text += event.Text
		} else {
			*list = append(*list, streamBlock{Kind: "text", Text: event.Text})
		}

	case types.EventToolStart:
		if event.ToolUse == nil {
			return
		}
		list := targetList(ss, event)
		if list == nil {
			return
		}
		*list = append(*list, streamBlock{
			Kind:      "tool",
			ID:        event.ToolUse.ID,
			Name:      event.ToolUse.Name,
			Summary:   event.ToolUse.Summary,
			IsSearch:  event.ToolUse.IsSearch,
			IsRead:    event.ToolUse.IsRead,
			IsList:    event.ToolUse.IsList,
			IsLsp:     event.ToolUse.IsLsp,
			IsWeb:     event.ToolUse.Name == "Web",
			State:     "running",
			StartedAt: time.Now().UnixMilli(),
			Children:  []streamBlock{},
		})

	case types.EventTurnStart:
		// Sub-agent turn_start: update parent Agent block's name with the
		// agent type so snapshot/historical replay shows "Agent Planner"
		// instead of just "Agent". The live streaming path does this in
		// the frontend via updateAgentToolName; updateStreamState needs to
		// mirror it for snapshot consistency.
		if event.Agent != nil && event.Agent.AgentType != "" && event.Agent.AgentType != "fork" {
			if event.Agent.ParentToolUseID != "" {
				parent := findBlock(ss.blocks, event.Agent.ParentToolUseID)
				if parent != nil && !strings.Contains(parent.Name, event.Agent.AgentType) {
					parent.Name = parent.Name + " " + event.Agent.AgentType
				}
			}
		}

	case types.EventToolParamDelta:
		if event.PartialInput == nil {
			return
		}
		b := findBlock(ss.blocks, event.PartialInput.ID)
		if b == nil {
			return
		}
		if event.PartialInput.Summary != "" {
			b.Summary = event.PartialInput.Summary
		}
		if event.PartialInput.IsSearch {
			b.IsSearch = true
		}
		if event.PartialInput.IsRead {
			b.IsRead = true
		}
		if event.PartialInput.IsList {
			b.IsList = true
		}
		if event.PartialInput.IsLsp {
			b.IsLsp = true
		}

	case types.EventAttachment:
		// Queued message drained mid-query at a turn boundary. Append a user
		// echo block so takeover snapshot includes it.
		if event.Message == nil {
			return
		}
		var text strings.Builder
		for _, cb := range event.Message.Content {
			if cb.Type == types.ContentTypeText {
				text.WriteString(cb.Text)
			}
		}
		if text.String() == "" {
			return
		}
		ss.blocks = append(ss.blocks, streamBlock{
			Kind: "user",
			Text: text.String(),
		})

	case types.EventToolEnd:
		if event.ToolResult == nil {
			return
		}
		b := findBlock(ss.blocks, event.ToolResult.ToolUseID)
		if b == nil {
			return
		}
		if event.ToolResult.IsError {
			b.State = "error"
		} else {
			b.State = "done"
		}
		b.DisplayOutput = event.ToolResult.DisplayOutput
		if event.ToolResult.IsSearch {
			b.IsSearch = true
		}
		if event.ToolResult.IsRead {
			b.IsRead = true
		}
		if event.ToolResult.IsList {
			b.IsList = true
		}
		if event.ToolResult.IsLsp {
			b.IsLsp = true
		}
		b.TimingNs = int64(event.ToolResult.Duration)
		// Agent tool: children were populated by sub-agent streaming events
		// during execution. Clear them on completion so snapshot/takeover
		// doesn't render stale sub-agent activity.
		b.Children = []streamBlock{}

	case types.EventToolOutputDelta:
		if event.ToolResult == nil {
			return
		}
		b := findBlock(ss.blocks, event.ToolResult.ToolUseID)
		if b == nil {
			return
		}
		b.DisplayOutput = event.ToolResult.DisplayOutput

	case types.EventThinkingStart:
		list := targetList(ss, event)
		if list == nil {
			return
		}
		*list = append(*list, streamBlock{
			Kind:      "thinking",
			Active:    true,
			StartedAt: time.Now().UnixMilli(),
		})

	case types.EventThinkingDelta:
		if event.Thinking == nil {
			return
		}
		list := targetList(ss, event)
		if list == nil {
			return
		}
		if n := len(*list); n > 0 && (*list)[n-1].Kind == "thinking" {
			(*list)[n-1].Text += event.Thinking.Text
		}

	case types.EventThinkingEnd:
		if event.Thinking == nil {
			return
		}
		list := targetList(ss, event)
		if list == nil {
			return
		}
		if n := len(*list); n > 0 && (*list)[n-1].Kind == "thinking" {
			(*list)[n-1].Active = false
			(*list)[n-1].DurationNs = int64(event.Thinking.Duration)
		}
	}
}

// onEngineEvent is the per-engine event handler, called by engineHubShim for
// each event on that engine's hub. All events from all engines are processed
// here. Stats always accumulate (atomic). For the active engine, live
// events go to wsCh. For inactive engines, streamState is updated in
// real-time (like TUI's background drain). The streamState snapshot is
// embedded in the metadata frame by sendMetadata (under ssMu).
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

	// Track Task tool calls so tool_end can push task_list. Sub-agents share
	// the parent engine's task list (RunAgent passes e.taskList to CreateTools),
	// so we MUST track sub-agent Task calls too — otherwise panel updates only
	// arrive on reconnect. The Agent filter stays for query_end/stat resets
	// below, which are per-engine; task tracking is shared.
	if event.Type == types.EventToolStart &&
		event.ToolUse != nil &&
		event.ToolUse.Name == "Task" {
		slot.taskToolIDs[event.ToolUse.ID] = true
	}

	accumulateStats(&slot.queryStats, event)

	isQueryEnd := event.Type == types.EventQueryEnd && event.Agent == nil

	aborted := false
	if isQueryEnd && event.Error != nil {
		if _, ok := errors.AsType[*engine.AbortError](event.Error); ok {
			aborted = true
			rewind := c.shouldAutoRewindFor(slot.engine)
			slog.Debug("wui:abort", "engine", engineID, "shouldAutoRewind", rewind, "msgs", len(slot.engine.Messages()))
			// Engine emits the interrupt text via text_start/delta/end on
			// its own; the connector only needs to drive rewind here.
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

	slog.Debug("wui:event", "type", event.Type, "engine", engineID,
		"agentType", agentTypeLog(event.Agent), "parentID", parentIDLog(event.Agent))

	if !slot.active.Load() || c.activeWS.Load() == nil {
		slot.ssMu.Lock()
		updateStreamState(&slot.streamState, event)
		if isQueryEnd {
			slot.streamState = streamState{}
			resetQueryStats(&slot.queryStats)
			slot.taskToolIDs = make(map[string]bool)
		}
		slot.ssMu.Unlock()
		return
	}

	slot.ssMu.Lock()
	updateStreamState(&slot.streamState, event)
	c.sendWS(payload)
	if isQueryEnd {
		if event.Error != nil && !aborted {
			c.sendWS(buildError(event.Error))
		}
		slot.streamState = streamState{}
		resetQueryStats(&slot.queryStats)
		slot.taskToolIDs = make(map[string]bool)
	}
	slot.ssMu.Unlock()

	// Sub-agent Task tool_end must also push task_list (see ToolStart comment).
	if event.Type == types.EventToolEnd && event.ToolResult != nil {
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
	if kind == "input" && !event.Ask.Deadline.IsZero() {
		out.DeadlineUnix = event.Ask.Deadline.Unix()
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
func hasTextContent(m types.Message) bool {
	for _, b := range m.Content {
		if b.Type == types.ContentTypeText {
			return true
		}
	}
	return false
}

// preCompactMessages pages SQLite messages that live before the last compact
// boundary of sessionID. delivered is TAIL-RELATIVE: 0 returns the limit
// messages immediately preceding the LAST boundary (boundary-adjacent); each
// subsequent request advances delivered by the previous page length. The
// returned slice is ASC (chronological) so the client's prepend logic is
// unchanged.
//
// Cross-epoch: rows strictly before the LAST boundary include intermediate
// compact_boundary markers (one per prior compact). Those markers are kept
// (Subtype=="compact_boundary") and returned in-line; other system subtypes
// (informational, transient, error_notification, etc.) and attachments are
// dropped.
//
// Returns (page, totalFiltered, hasBoundary). When limit==0 the function does
// NOT load messages — it only probes whether a boundary exists
// ((nil, 0, true) when one does). When no boundary exists the result is
// (nil, 0, false). When delivered >= total the result is (nil, total, true)
// so the client sees a final empty page with hasMore=false.
//
// The total counts ONLY filtered messages so tail offsets stay stable across
// conversation growth (pre-compact slice is immutable post-compact).
func preCompactMessages(store *short.Store, sessionID string, delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
	_, boundarySeq, err := store.GetLastBoundary(sessionID)
	if err != nil || boundarySeq == 0 {
		return nil, 0, false
	}
	if limit == 0 {
		return nil, 0, true
	}
	pre, err := store.LoadMessagesBeforeSeq(sessionID, boundarySeq)
	if err != nil {
		return nil, 0, false
	}
	filtered := make([]*short.TranscriptMessage, 0, len(pre))
	for _, m := range pre {
		if m.Type == string(types.RoleSystem) && m.Subtype != "compact_boundary" {
			continue
		}
		if m.Type == "attachment" {
			continue
		}
		if m.Subtype != "compact_boundary" && hasFilteredFlag(m) {
			continue
		}
		filtered = append(filtered, m)
	}
	if delivered >= len(filtered) {
		return nil, len(filtered), true
	}
	end := len(filtered) - delivered
	start := max(end-limit, 0)
	return filtered[start:end], len(filtered), true
}

// hasFilteredFlag decodes the message's metadata JSON and reports whether
// FlagMeta or FlagCompactSummary is set. Used to drop compact summaries and
// meta-tagged rows. Boundary markers are excluded by the caller (subtype check).
func hasFilteredFlag(m *short.TranscriptMessage) bool {
	if m.Metadata == "" {
		return false
	}
	var meta struct {
		Flags types.MessageFlag `json:"flags,omitempty"`
	}
	if err := json.Unmarshal([]byte(m.Metadata), &meta); err != nil {
		return false
	}
	return meta.Flags&(types.FlagMeta|types.FlagCompactSummary) != 0
}

// buildHistoryChatMsg converts a single types.Message into the wire-shape
// historyChatMsg. It runs the same per-block loop as TUI's
// engineMessagesToViews so pre-compact and post-compact messages render
// identically. toolResults maps tool_use_id → result block for cross-reference.
//
// Defined as a method on *WUIConnector so it can access c.thumbs for
// memoizing resized image thumbnails.
func (c *WUIConnector) buildHistoryChatMsg(m types.Message, tools map[string]tool.Tool, toolResults map[string]types.ContentBlock, richResults map[string]any) historyChatMsg {
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
		case types.ContentTypeImage:
			dataURL, ok := c.historyImageDataURL(cb)
			if !ok {
				// Degrade: never silently drop the attachment. Emit a
				// placeholder text block so the user sees the image existed
				// but the bytes are gone (file deleted, cache miss, etc.).
				placeholder := "[image]"
				if cb.Source != nil && cb.Source.Path != "" {
					placeholder = "[image: " + filepath.Base(cb.Source.Path) + "]"
				}
				hm.Text += placeholder
				hm.Blocks = append(hm.Blocks, historyBlock{Kind: "text", Text: placeholder})
				continue
			}
			hm.Blocks = append(hm.Blocks, historyBlock{Kind: "image", Src: dataURL})
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
				Name:    historyAgentToolName(cb.Name, cb.Input),
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
				// Rich-data slot first: wire-plaintext tools (Edit/Write/MCP)
				// persist an LLM-facing summary as content; the rich view for
				// replay comes from ToolResultData. The tool must still be
				// resolvable — an MCP server that has disconnected would leave
				// the rich JSON undecodable, and raw-JSON beats the readable
				// wire text. Legacy sessions without the slot fall back to
				// decoding the wire content.
				_, hasTool := tools[cb.Name]
				if data, hasRich := richResults[cb.ID]; hasRich && hasTool {
					if raw := tool.WrapRichToolResult(data); raw != nil {
						entry.DisplayOutput = renderToolOutput(cb.Name, raw, tools)
					} else {
						entry.DisplayOutput = renderToolOutput(cb.Name, result.Content, tools)
					}
				} else {
					entry.DisplayOutput = renderToolOutput(cb.Name, result.Content, tools)
				}
				entry.DurationNs = result.ToolDurationNs
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
	return hm
}

// historyImageDataURL converts an image ContentBlock into a data: URL for
// history replay. Both source types are decoded to raw bytes and resized to
// 512px long edge (JPEG q80); the result is cached on c.thumbs. File sources
// key by path+mtime; base64 sources key by a sha256 prefix of the source
// string (avoiding 100KB+ map keys). Returns (dataURL, false) on any
// decode/resize failure so the caller can emit a placeholder block.
func (c *WUIConnector) historyImageDataURL(cb types.ContentBlock) (string, bool) {
	if cb.Source == nil {
		return "", false
	}
	var (
		raw      []byte
		cacheKey string
	)
	switch cb.Source.Type {
	case "file":
		key := cb.Source.Path
		if fi, err := os.Stat(cb.Source.Path); err == nil {
			key = cb.Source.Path + "|" + fi.ModTime().String()
			if v, ok := c.thumbs.get(key); ok {
				return v, true
			}
		}
		data, err := os.ReadFile(cb.Source.Path)
		if err != nil {
			return "", false
		}
		raw = data
		cacheKey = key
	case "base64":
		// Hash the encoded string (not the decoded bytes) so identical
		// source strings hit the cache without paying for a second decode.
		sum := sha256.Sum256([]byte(cb.Source.Data))
		key := "b64:" + hex.EncodeToString(sum[:8])
		if v, ok := c.thumbs.get(key); ok {
			return v, true
		}
		data, err := base64.StdEncoding.DecodeString(cb.Source.Data)
		if err != nil {
			return "", false
		}
		raw = data
		cacheKey = key
	default:
		return "", false
	}
	thumb, mt, err := utils.ResizeForThumbnail(raw, 512)
	if err != nil {
		return "", false
	}
	dataURL := "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(thumb)
	c.thumbs.put(cacheKey, dataURL)
	return dataURL, true
}

// IsBusy exclusion when the engine is streaming. When busy, the assistant's
// streaming response (after the last user text message) is omitted so the
// snapshot/streamState provides that data instead (zero overlap). The user's
// query message itself is included in history.
//
// cursor has two namespaces: "" or "N" for in-memory paging (current
// behavior); "precompact:D" hands off to buildPreCompactHistory which reads
// SQLite rows strictly before the last compact boundary.
// isCompactBoundaryMarker checks whether a FlagCompactSummary message is a
// compact_boundary marker (as opposed to the human-readable summary). The
// boundary marker's content is a JSON string containing "subtype":"compact_boundary".
func isCompactBoundaryMarker(m types.Message) bool {
	for _, cb := range m.Content {
		if cb.Type == types.ContentTypeText && strings.Contains(cb.Text, `"compact_boundary"`) {
			return true
		}
	}
	return false
}

func (c *WUIConnector) buildHistory(slot *engineSlot, cursor string, limit int) []byte {
	if slot == nil {
		return nil
	}
	if strings.HasPrefix(cursor, "precompact:") {
		return c.buildPreCompactHistory(slot, cursor, limit)
	}
	msgs := slot.engine.Messages()

	// Query in progress: exclude messages[idx+1:] (in-flight assistant
	// streaming covered by snapshot + live events). The user query at
	// messages[idx] MUST be included — snapshot only carries assistant
	// streaming blocks, not the user's input. Without this, the current
	// query vanishes from history (and on reconnect).
	if idx := slot.engine.QueryStartMsgIdx(); idx >= 0 && idx < len(msgs) {
		msgs = msgs[:idx+1]
	}

	if len(msgs) == 0 {
		return nil
	}

	tools := slot.engine.Tools()

	// First pass: collect all tool_results keyed by tool_use_id (same as TUI).
	toolResults := make(map[string]types.ContentBlock)
	richResults := make(map[string]any)
	for _, m := range msgs {
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID != "" {
				toolResults[cb.ToolUseID] = cb
			}
		}
		maps.Copy(richResults, m.ToolResultData)
	}

	var out []historyChatMsg
	for _, m := range msgs {
		if m.Role == types.RoleSystem && !isCompactBoundaryMarker(m) {
			continue
		}
		if m.HasFlag(types.FlagMeta) {
			continue
		}
		if m.HasFlag(types.FlagCompactSummary) {
			// FlagCompactSummary covers both compact_boundary markers and
			// the human-readable compact summary message. Only the boundary
			// marker becomes a divider; the summary is skipped (its content
			// is a system instruction for the LLM, not user-visible).
			if isCompactBoundaryMarker(m) {
				out = append(out, historyChatMsg{
					ID:              m.ID,
					Role:            "system",
					CompactBoundary: true,
					StartedAt:       m.Timestamp.UnixMilli(),
				})
			}
			continue
		}
		out = append(out, c.buildHistoryChatMsg(m, tools, toolResults, richResults))
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

	// When the in-memory top is reached AND the engine has a compact boundary,
	// extend pagination into the SQLite pre-compact region. The limit=0 probe
	// avoids loading any messages — preCompactMessages returns hasBoundary
	// without touching the DB rows.
	if !hasMore {
		_, _, hasBoundary := slot.engine.PreCompactMessages(0, 0)
		if hasBoundary {
			hasMore = true
			nextCursor = "precompact:0"
		}
	}

	payload, _ := json.Marshal(struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}{Type: "history", Messages: page, NextCursor: nextCursor, HasMore: hasMore})
	return payload
}

// buildPreCompactHistory serves the SQLite-side pre-compact page. Parses the
// delivered offset from cursor "precompact:D" (malformed → 0), pages through
// preCompactMessages, runs each non-marker message through buildHistoryChatMsg,
// and emits in-page compact_boundary markers as synthetic
// historyChatMsg{role:"system", compactBoundary:true}.
func (c *WUIConnector) buildPreCompactHistory(slot *engineSlot, cursor string, limit int) []byte {
	if slot == nil {
		return nil
	}
	if limit <= 0 {
		limit = 30
	}
	delivered := 0
	if rest, ok := strings.CutPrefix(cursor, "precompact:"); ok {
		if n, err := strconv.Atoi(rest); err == nil && n > 0 {
			delivered = n
		} else if err != nil {
			slog.Warn("wui: malformed precompact cursor, treating as 0", "cursor", cursor)
		}
	}

	page, total, hasBoundary := slot.engine.PreCompactMessages(delivered, limit)
	if !hasBoundary {
		// Defensive: cursor pointed at pre-compact but the boundary vanished
		// between requests (e.g. session switch). Return an empty page with
		// hasMore=false so the client stops paging.
		payload, _ := json.Marshal(struct {
			Type       string           `json:"type"`
			Messages   []historyChatMsg `json:"messages"`
			NextCursor string           `json:"nextCursor"`
			HasMore    bool             `json:"hasMore"`
		}{Type: "history", Messages: []historyChatMsg{}, NextCursor: "", HasMore: false})
		return payload
	}

	tools := slot.engine.Tools()
	// Pre-compact messages never see in-flight tool_results (they are
	// immutable, persisted with their results). Re-scan blocks to populate the
	// toolResults map so renderToolOutput can resolve them by tool_use_id.
	toolResults := make(map[string]types.ContentBlock)
	richResults := make(map[string]any)
	for _, m := range page {
		for _, cb := range short.ParseContentBlocks(m.Content) {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID != "" {
				toolResults[cb.ToolUseID] = cb
			}
		}
		maps.Copy(richResults, short.StoreMessageToEngine(m).ToolResultData)
	}

	out := make([]historyChatMsg, 0, len(page))
	for _, tm := range page {
		if tm.Type == string(types.RoleSystem) && tm.Subtype == "compact_boundary" {
			out = append(out, historyChatMsg{
				ID:              tm.UUID,
				Role:            string(types.RoleSystem),
				StartedAt:       tm.CreatedAt.UnixMilli(),
				Status:          "done",
				CompactBoundary: true,
			})
			continue
		}
		em := short.StoreMessageToEngine(tm)
		out = append(out, c.buildHistoryChatMsg(em, tools, toolResults, richResults))
	}

	end := delivered + len(out)
	hasMore := end < total
	nextCursor := ""
	if hasMore {
		nextCursor = "precompact:" + strconv.Itoa(end)
	}

	payload, _ := json.Marshal(struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}{Type: "history", Messages: out, NextCursor: nextCursor, HasMore: hasMore})
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

// historyAgentToolName returns the display name for a history tool_use block,
// appending the sub-agent type for Agent tools. The live path appends agent
// type on the client (updateAgentToolName); history replay must recover it
// from the persisted input since no tool-start event is replayed.
func historyAgentToolName(name string, input json.RawMessage) string {
	display := formatToolDisplayName(name)
	if name != "Agent" {
		return display
	}
	var parsed types.AgentInput
	if err := json.Unmarshal(input, &parsed); err != nil {
		return display
	}
	if parsed.SubagentType == "" || parsed.SubagentType == "fork" {
		return display
	}
	return display + " " + parsed.SubagentType
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
//
// After the array-form unification every DecodeResult strictly accepts the
// wire array shape. We pass the raw array straight through; legacy
// string-form sessions replayed from disk fail DecodeResult and fall through
// to text extraction.
func renderToolOutput(toolName string, raw json.RawMessage, tools map[string]tool.Tool) string {
	if len(raw) == 0 {
		return ""
	}

	// Pass the raw array straight through. After the array-form unification
	// every DecodeResult accepts this shape; legacy string-form rows fail
	// DecodeResult and fall through.
	if r, ok := renderViaTool(toolName, raw, tools); ok {
		return r
	}

	// DecodeResult unavailable or errored — extract text from blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 {
		return string(raw)
	}
	rest := strings.Join(parts, "\n")

	// Persisted-output: file content is bare inner text. Re-wrap into the
	// array form DecodeResult expects.
	if strings.HasPrefix(rest, "<persisted-output>") {
		if data := readPersistedFile(rest); data != nil {
			wrapped := tool.WrapSingleBlock(string(data))
			if r, ok := renderViaTool(toolName, wrapped, tools); ok {
				return r
			}
		}
		return extractPersistedPreview(rest)
	}

	// DecodeResult rejected the payload (e.g., Edit error stored as a text
	// block — fileedit.DecodeResult expects an Output struct). Tools with a
	// string-form RenderResult (like renderEditError) get one more chance to
	// shorten / format the text. Without this, streaming shows the short
	// form (emitToolError calls RenderResult) but history replay shows raw.
	if t, ok := tools[toolName]; ok {
		return t.RenderResult(rest)
	}
	return rest
}

// renderViaTool finds the tool, decodes the raw JSON to its concrete result
// type via DecodeResult, then calls RenderResult. Returns (rendered, false)
// if the tool is not in the map or DecodeResult errors — caller must apply
// its own fallback.
func renderViaTool(toolName string, raw json.RawMessage, tools map[string]tool.Tool) (string, bool) {
	t, ok := tools[toolName]
	if !ok {
		return "", false
	}
	if dt, ok := t.(tool.ToolWithDecodeResult); ok {
		if v, err := dt.DecodeResult(raw); err == nil {
			return t.RenderResult(v), true
		}
		return "", false
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
	ID              string                 `json:"id"`
	Role            string                 `json:"role"`
	CompactBoundary bool                   `json:"compactBoundary,omitempty"`
	Text            string                 `json:"text"`
	Thinking        []historyThinkingEntry `json:"thinking"`
	Tools           []historyToolEntry     `json:"tools"`
	Blocks          []historyBlock         `json:"blocks,omitempty"`
	Usage           historyUsage           `json:"usage"`
	Error           string                 `json:"error"`
	Status          string                 `json:"status"`
	StartedAt       int64                  `json:"startedAt"`
}

// historyBlock is one entry in the ordered Blocks array. It mirrors a single
// content block from the engine message's Content[], preserving original event
// order so the frontend can interleave text/thinking/tool correctly. The legacy
// Text/Thinking/Tools fields concatenate same-type blocks and lose ordering;
// Blocks is authoritative when present.
type historyBlock struct {
	Kind     string                `json:"kind"`               // "text" | "thinking" | "tool" | "image"
	Text     string                `json:"text,omitempty"`     // kind == "text"
	Thinking *historyThinkingEntry `json:"thinking,omitempty"` // kind == "thinking"
	Tool     *historyToolEntry     `json:"tool,omitempty"`     // kind == "tool"
	Src      string                `json:"src,omitempty"`      // kind == "image": data URL
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
		msg = llm.FormatLLMError(err)
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
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	ToolName     string          `json:"tool_name"`
	Input        json.RawMessage `json:"input,omitempty"`
	Message      string          `json:"message,omitempty"`
	RuleDetail   string          `json:"rule_detail,omitempty"`
	Prompt       string          `json:"prompt,omitempty"`
	Masked       bool            `json:"masked,omitempty"`
	AgentType    string          `json:"agent_type,omitempty"`
	DeadlineUnix int64           `json:"deadline_unix,omitempty"`
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
		// Empty list still needs a push so the client clears its panel.
		// Returning nil here would leave stale tasks visible until reconnect.
		payload, _ := json.Marshal(taskListOutbound{Type: "task_list", Tasks: []taskListWireItem{}})
		return payload
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
		Type     string            `json:"type"`
		Models   []configModelItem `json:"models"`
		Current  configCurrent     `json:"current"`
		Thinking string            `json:"thinking"`
	}{
		Type:     "config",
		Models:   []configModelItem{},
		Current:  configCurrent{Provider: currentProvider, Model: currentModel},
		Thinking: string(slot.engine.Thinking()),
	}
	for _, it := range items {
		out.Models = append(out.Models, configModelItem{Provider: it.Provider, Model: it.Model})
	}
	payload, _ := json.Marshal(out)
	return payload
}

// buildQuota fetches quota for all providers and sends the result as a
// quota_result WS message. Called on demand (not during metadata) so the
// quota API is only hit when the model picker is open.
func (c *WUIConnector) buildQuota() {
	ws := c.activeWS.Load()
	if ws == nil {
		return
	}
	type quotaEntry struct {
		Provider string `json:"provider"`
		Quota    string `json:"quota"`
	}
	var entries []quotaEntry
	for name := range c.providerConfigs {
		p := c.providerConfigs[name]
		f := quota.Detect(p)
		if f == nil {
			continue
		}
		info, err := f.Fetch(context.Background())
		if err != nil {
			continue
		}
		entries = append(entries, quotaEntry{Provider: name, Quota: formatQuota(&info)})
	}
	if len(entries) == 0 {
		return
	}
	payload, _ := json.Marshal(struct {
		Type    string       `json:"type"`
		Entries []quotaEntry `json:"entries"`
	}{Type: "quota_result", Entries: entries})
	c.sendWS(payload)
}

// formatQuota formats quota.Info into a compact display string like "85%/2h30m".
func formatQuota(info *quota.Info) string {
	if info == nil {
		return ""
	}
	rem := info.Remaining()
	left := max(time.Until(info.ResetAt), 0)
	hours := int(left.Hours())
	if hours >= 24 {
		days := hours / 24
		return fmt.Sprintf("%d%%/%dd", rem, days)
	}
	if hours > 0 {
		mins := int(left.Minutes()) % 60
		return fmt.Sprintf("%d%%/%dh%dm", rem, hours, mins)
	}
	mins := int(left.Minutes())
	if mins > 0 {
		return fmt.Sprintf("%d%%/%dm", rem, mins)
	}
	return fmt.Sprintf("%d%%/0m", rem)
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
	ctxUsed := slot.engine.GetContextTokens()
	if ctxUsed == 0 {
		ctxUsed = engine.TokenCountWithEstimation(slot.engine.Messages())
	}
	payload, _ := json.Marshal(struct {
		Type         string      `json:"type"`
		Usage        types.Usage `json:"usage"`
		QueryStartMs int64       `json:"queryStartMs"`
		ToolCount    int         `json:"toolCount"`
		ThinkingMs   int64       `json:"thinkingMs"`
		ContextUsed  int         `json:"contextUsed"`
		ContextTotal int         `json:"contextTotal"`
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
		ContextUsed:  ctxUsed,
		ContextTotal: slot.engine.ContextWindow(),
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

// handleSessionSwitch loads the target session into the active engine, then
// pushes connect_status + history + config + stats. Rejected while the
// engine is busy.
// errBusySessionOp rejects session operations that would swap the context
// out from under a running query — remaining responses would land in the
// wrong session.
var errBusySessionOp = errors.New("engine is busy — wait for the current query to finish")

func (c *WUIConnector) handleSessionSwitch(sessionID string) {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	if eng.IsBusy() {
		c.sendWS(buildError(errBusySessionOp))
		return
	}
	if err := eng.SwitchSession(sessionID); err != nil {
		c.sendWS(buildError(err))
		return
	}
	// Persist the selection like the TUI picker does (vs.ActiveSessionID +
	// persistWorkspaceMeta) so a restart resumes the switched-to session
	// instead of reverting to the last persisted one.
	if c.mgr != nil {
		if vs := c.mgr.Active(); vs != nil {
			vs.ActiveSessionID = sessionID
		}
		if err := c.mgr.PersistMeta(eng.ProjectDir()); err != nil {
			slog.Warn("wui:session switch:PersistMeta failed", "error", err)
		}
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
// connect_status + config + stats. Rejected while the engine is busy
// (the sidebar grays its controls as an affordance).
func (c *WUIConnector) handleSessionNew() {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	if eng.IsBusy() {
		c.sendWS(buildError(errBusySessionOp))
		return
	}
	if _, err := eng.NewSession(); err != nil {
		c.sendWS(buildError(err))
		return
	}
	// Persist the new session like the TUI /session -n path does, so a
	// restart resumes into it instead of the previous one.
	if c.mgr != nil {
		if vs := c.mgr.Active(); vs != nil {
			vs.ActiveSessionID = eng.SessionID()
		}
		if err := c.mgr.PersistMeta(eng.ProjectDir()); err != nil {
			slog.Warn("wui:session new:PersistMeta failed", "error", err)
		}
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

	ctxUsed := eng.GetContextTokens()
	if ctxUsed == 0 {
		ctxUsed = engine.TokenCountWithEstimation(eng.Messages())
	}
	c.sendWS(buildModelSwitched(ctxUsed, eng.ContextWindow(), eng.Thinking()))
}

// buildModelSwitched carries the post-switch context numbers plus the
// re-resolved effort — without an override the new model's baseline applies,
// so the client pill must be re-synced on every model switch.
func buildModelSwitched(contextUsed, contextTotal int, effort llm.Effort) []byte {
	payload, _ := json.Marshal(struct {
		Type         string `json:"type"`
		ContextUsed  int    `json:"contextUsed"`
		ContextTotal int    `json:"contextTotal"`
		Thinking     string `json:"thinking"`
	}{
		Type:         "model_switched",
		ContextUsed:  contextUsed,
		ContextTotal: contextTotal,
		Thinking:     string(effort),
	})
	return payload
}

// handleThinkingSwitch sets the active engine's effort and pushes a fresh
// config frame so the pill renders only server-resolved state (the engine is
// the single source of truth).
func (c *WUIConnector) handleThinkingSwitch(effort string) {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	e, err := llm.ParseEffort(effort)
	if err != nil {
		c.sendWS(buildError(err))
		return
	}
	if err := eng.SetThinking(e); err != nil {
		c.sendWS(buildError(err))
		return
	}
	if c.mgr != nil {
		c.mgr.SetActiveThinking(e)
		if err := c.mgr.PersistMeta(eng.ProjectDir()); err != nil {
			slog.Warn("wui:thinking persist failed", "error", err)
		}
	}
	slog.Info("wui:thinking switched", "effort", e)
	slot := c.activeSlot()
	if slot == nil {
		return
	}
	if c.activeWS.Load() != nil {
		c.sendWS(c.buildConfig(slot))
	}
}

// SetCreateEngineFn injects the engine creation closure used by engine_new.
// main.go calls this to wire engineFactory + engineMgr + store into the
// connector.
func (c *WUIConnector) SetCreateEngineFn(fn func(name string) (string, error)) {
	c.createEngine = fn
}

// SetMediaCache injects the media store used by the WS chunked-upload path
// to save uploaded files. Without this call, attachment uploads degrade to
// text-only (the readLoop's accumulator rejects handleEnd with "no media
// cache").
func (c *WUIConnector) SetMediaCache(store *media.Store) {
	c.mediaCache = store
}

// sendMetadata sends a composite metadata message containing connect_status,
// config, engine_list, task_list, history, snapshot, queuedMsgs, and stats.
// The snapshot embeds the current streamState (under ssMu) so the client
// receives streaming state and history in a single message. queuedMsgs
// restores user-typed messages still waiting in the attachment queue so a
// reconnecting client can re-render its pending queue.
func (c *WUIConnector) sendMetadata(slot *engineSlot) {
	type metaPayload struct {
		Type       string          `json:"type"`
		Connect    json.RawMessage `json:"connect"`
		Config     json.RawMessage `json:"config"`
		Engines    json.RawMessage `json:"engines"`
		Tasks      json.RawMessage `json:"tasks,omitempty"`
		History    json.RawMessage `json:"history"`
		Snapshot   json.RawMessage `json:"snapshot,omitempty"`
		QueuedMsgs []queuedMsgJSON `json:"queuedMsgs,omitempty"`
		Stats      json.RawMessage `json:"stats"`
	}

	slot.ssMu.Lock()
	history := c.buildHistory(slot, "", 30)
	var snapshot json.RawMessage
	if len(slot.streamState.blocks) > 0 {
		snapshot, _ = json.Marshal(struct {
			Blocks []streamBlock `json:"blocks"`
		}{Blocks: slot.streamState.blocks})
	}

	payload, _ := json.Marshal(metaPayload{
		Type:       "metadata",
		Connect:    c.buildConnectStatus(slot),
		Config:     c.buildConfig(slot),
		Engines:    c.buildEngineList(),
		Tasks:      c.buildTaskList(slot),
		History:    history,
		Snapshot:   snapshot,
		QueuedMsgs: buildQueuedMsgs(slot.engine.PendingAttachments()),
		Stats:      c.buildStats(slot),
	})
	c.sendWS(payload)
	slot.ssMu.Unlock()
}

// queuedMsgJSON is the wire shape for a queued user message restored on
// takeover. Matches the frontend's queuedMsgs entry type {uuid, text}.
type queuedMsgJSON struct {
	UUID string `json:"uuid"`
	Text string `json:"text"`
}

// buildQueuedMsgs filters pending attachment items down to user-typed prompt
// messages and maps them to the wire shape. Job-mode items (system-generated
// notifications) and meta-tagged items are excluded so only real user input
// is restored. Text is pulled from Content blocks when present (image/structured
// attachments), falling back to the plain Value field.
func buildQueuedMsgs(items []types.QueuedItem) []queuedMsgJSON {
	var out []queuedMsgJSON
	for _, item := range items {
		if item.Mode != types.ItemModePrompt || item.IsMeta {
			continue
		}
		text := item.Value
		if len(item.Content) > 0 {
			var sb strings.Builder
			for _, cb := range item.Content {
				if cb.Type == types.ContentTypeText {
					sb.WriteString(cb.Text)
				}
			}
			if sb.Len() > 0 {
				text = sb.String()
			}
		}
		out = append(out, queuedMsgJSON{UUID: item.UUID, Text: text})
	}
	return out
}

// switchEngine is the unified engine switch (pointer swap only). Deactivates
// old engine (its events start updating streamState), flips active ID, syncs
// EngineManager, sends metadata (with embedded streamState snapshot under
// ssMu), and activates new engine.
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
		} else {
			dir := newSlot.engine.ProjectDir()
			if err := c.mgr.PersistMeta(dir); err != nil {
				slog.Warn("wui:switchEngine:PersistMeta failed", "error", err)
			}
		}
	}

	if c.activeWS.Load() != nil {
		c.sendMetadata(newSlot)
	}

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
