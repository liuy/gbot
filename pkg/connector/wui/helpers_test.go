package wui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
)

// waitFor polls cond until it returns true or the timeout elapses.
// The wui WS tests use real TCP connections (httptest.Server + gorilla
// dialer), so there is no channel to select on — polling is the only option.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout) // REAL-TIME
	for time.Now().Before(deadline) {   // REAL-TIME
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	return cond()
}

// mockEngine implements engineClient for tests. Each method delegates to a
// configurable function field; tests set only the fields they need. The mu
// guards the recorded slices so concurrent goroutines (Query runs in its own
// goroutine) can safely append.
type mockEngine struct {
	mu sync.RWMutex

	queryFn            func(ctx context.Context, userMessage, systemPrompt string)
	queryWithContentFn func(ctx context.Context, content []types.ContentBlock, systemPrompt string)
	isBusyFn           func() bool
	queryStartMsgIdxFn func() int
	messagesFn         func() []types.Message
	toolsFn            func() map[string]tool.Tool
	enqueueFn          func(item types.QueuedItem)
	abortFn            func()
	rewindToFn         func(idx int) error
	systemPromptFn     func() string
	taskListFn         func() *task.List
	// onQueryDoneFn simulates engine committing an assistant response.
	// Called after queryFn finishes — mirrors real engine's appendMessage.
	onQueryDoneFn func()

	switchSessionFn    func(sessionID string) error
	listSessionsFn     func(limit int) ([]*short.Session, error)
	newSessionFn       func() (string, error)
	updateTitleFn      func(sessionID, title string) error
	sessionIDFn        func() string
	engineIDFn         func() string
	modelFn            func() string
	projectDirFn       func() string
	contextBreakdownFn func() *engine.ContextBreakdown
	setProviderFn      func(llm.Provider)
	setModelFn         func(string)
	providerFn         func() llm.Provider
	setMaxTokensFn     func(int)
	setInputModFn      func([]string)
	updateAutoFn       func(engine.AutoCompactConfig)
	preCompactFn       func(delivered, limit int) ([]*short.TranscriptMessage, int, bool)

	// Recorded calls for assertions.
	queryCalls            []queryCall
	queryWithContentCalls []contentCall
	enqueueCalls          []types.QueuedItem
	abortCount            int
	rewindCalls           []int
	removeAttachment      []string
	removeAttachmentFn    func(uuid string) bool
	switchSessionCalls    []string
	newSessionCalls       int
	updateTitleCalls      []struct{ ID, Title string }
	setProviderCalls      []string
	setModelCalls         []string
	setMaxTokensCalls     []int
	setInputModCalls      [][]string
	updateAutoCalls       int
}

type queryCall struct {
	userMessage  string
	systemPrompt string
}

// contentCall captures one QueryWithContent invocation. The content slice is
// deep-copied at record time so later caller-side mutation cannot retroactively
// alter the captured assertion data.
type contentCall struct {
	content      []types.ContentBlock
	systemPrompt string
}

func (m *mockEngine) Query(ctx context.Context, userMessage, systemPrompt string) {
	m.mu.Lock()
	m.queryCalls = append(m.queryCalls, queryCall{userMessage, systemPrompt})
	m.mu.Unlock()
	if m.queryFn != nil {
		m.queryFn(ctx, userMessage, systemPrompt)
	}
	// Engine commits assistant response after streaming finishes.
	// This mirrors real engine: appendMessage(*resp).
	if m.onQueryDoneFn != nil {
		m.onQueryDoneFn()
	}
}

func (m *mockEngine) QueryWithContent(ctx context.Context, content []types.ContentBlock, systemPrompt string) {
	// Defensive copy so caller-side mutation cannot alter the recorded call.
	copied := make([]types.ContentBlock, len(content))
	copy(copied, content)
	m.mu.Lock()
	m.queryWithContentCalls = append(m.queryWithContentCalls, contentCall{content: copied, systemPrompt: systemPrompt})
	m.mu.Unlock()
	if m.queryWithContentFn != nil {
		m.queryWithContentFn(ctx, content, systemPrompt)
	}
	if m.onQueryDoneFn != nil {
		m.onQueryDoneFn()
	}
}

func (m *mockEngine) IsBusy() bool {
	if m.isBusyFn != nil {
		return m.isBusyFn()
	}
	return false
}

func (m *mockEngine) QueryStartMsgIdx() int {
	if m.queryStartMsgIdxFn != nil {
		return m.queryStartMsgIdxFn()
	}
	return -1
}

func (m *mockEngine) Messages() []types.Message {
	m.mu.RLock()
	fn := m.messagesFn
	m.mu.RUnlock()
	if fn != nil {
		return fn()
	}
	return nil
}

// SetMessagesFn sets the messages function under the mock's lock so tests
// can update it while a server goroutine may be reading.
func (m *mockEngine) SetMessagesFn(fn func() []types.Message) {
	m.mu.Lock()
	m.messagesFn = fn
	m.mu.Unlock()
}

func (m *mockEngine) Tools() map[string]tool.Tool {
	if m.toolsFn != nil {
		return m.toolsFn()
	}
	return nil
}

func (m *mockEngine) EnqueueAttachment(item types.QueuedItem) {
	m.mu.Lock()
	m.enqueueCalls = append(m.enqueueCalls, item)
	m.mu.Unlock()
	if m.enqueueFn != nil {
		m.enqueueFn(item)
	}
}

func (m *mockEngine) Abort() {
	m.mu.Lock()
	m.abortCount++
	m.mu.Unlock()
	if m.abortFn != nil {
		m.abortFn()
	}
}

func (m *mockEngine) RemoveAttachment(uuid string) bool {
	m.mu.Lock()
	m.removeAttachment = append(m.removeAttachment, uuid)
	m.mu.Unlock()
	if m.removeAttachmentFn != nil {
		return m.removeAttachmentFn(uuid)
	}
	return true
}

func (m *mockEngine) RewindTo(idx int) error {
	m.mu.Lock()
	m.rewindCalls = append(m.rewindCalls, idx)
	m.mu.Unlock()
	if m.rewindToFn != nil {
		return m.rewindToFn(idx)
	}
	return nil
}

func (m *mockEngine) SystemPrompt() string {
	if m.systemPromptFn != nil {
		return m.systemPromptFn()
	}
	return ""
}

func (m *mockEngine) TaskList() *task.List {
	if m.taskListFn != nil {
		return m.taskListFn()
	}
	return nil
}

func (m *mockEngine) SwitchSession(sessionID string) error {
	m.mu.Lock()
	m.switchSessionCalls = append(m.switchSessionCalls, sessionID)
	m.mu.Unlock()
	if m.switchSessionFn != nil {
		return m.switchSessionFn(sessionID)
	}
	return nil
}

func (m *mockEngine) ListSessions(limit int) ([]*short.Session, error) {
	if m.listSessionsFn != nil {
		return m.listSessionsFn(limit)
	}
	return nil, nil
}

func (m *mockEngine) NewSession() (string, error) {
	m.mu.Lock()
	m.newSessionCalls++
	m.mu.Unlock()
	if m.newSessionFn != nil {
		return m.newSessionFn()
	}
	return "new-session-id", nil
}

func (m *mockEngine) UpdateSessionTitle(sessionID, title string) error {
	m.mu.Lock()
	m.updateTitleCalls = append(m.updateTitleCalls, struct{ ID, Title string }{sessionID, title})
	m.mu.Unlock()
	if m.updateTitleFn != nil {
		return m.updateTitleFn(sessionID, title)
	}
	return nil
}

func (m *mockEngine) SessionID() string {
	if m.sessionIDFn != nil {
		return m.sessionIDFn()
	}
	return "test-session"
}

func (m *mockEngine) EngineID() string {
	if m.engineIDFn != nil {
		return m.engineIDFn()
	}
	return "main"
}

func (m *mockEngine) Model() string {
	if m.modelFn != nil {
		return m.modelFn()
	}
	return "glm-5.2"
}

func (m *mockEngine) ProjectDir() string {
	if m.projectDirFn != nil {
		return m.projectDirFn()
	}
	return "/tmp/test"
}

func (m *mockEngine) ContextWindow() int    { return 200000 }
func (m *mockEngine) GetContextTokens() int { return 0 }
func (m *mockEngine) ContextBreakdown() *engine.ContextBreakdown {
	if m.contextBreakdownFn != nil {
		return m.contextBreakdownFn()
	}
	return nil
}

func (m *mockEngine) SetProvider(p llm.Provider) {
	m.mu.Lock()
	m.setProviderCalls = append(m.setProviderCalls, p.Name())
	m.mu.Unlock()
	if m.setProviderFn != nil {
		m.setProviderFn(p)
	}
}

func (m *mockEngine) SetModel(model string) {
	m.mu.Lock()
	m.setModelCalls = append(m.setModelCalls, model)
	m.mu.Unlock()
	if m.setModelFn != nil {
		m.setModelFn(model)
	}
}

func (m *mockEngine) Provider() llm.Provider {
	if m.providerFn != nil {
		return m.providerFn()
	}
	return nil
}

func (m *mockEngine) SetMaxTokens(n int) {
	m.mu.Lock()
	m.setMaxTokensCalls = append(m.setMaxTokensCalls, n)
	m.mu.Unlock()
	if m.setMaxTokensFn != nil {
		m.setMaxTokensFn(n)
	}
}

func (m *mockEngine) SetInputModalities(modalities []string) {
	m.mu.Lock()
	m.setInputModCalls = append(m.setInputModCalls, modalities)
	m.mu.Unlock()
	if m.setInputModFn != nil {
		m.setInputModFn(modalities)
	}
}

func (m *mockEngine) UpdateAutoCompactConfig(cfg engine.AutoCompactConfig) {
	m.mu.Lock()
	m.updateAutoCalls++
	m.mu.Unlock()
	if m.updateAutoFn != nil {
		m.updateAutoFn(cfg)
	}
}

// PreCompactMessages delegates to preCompactFn when set; otherwise returns the
// no-boundary shape so buildHistory treats the mock as having no pre-compact
// history (preserving existing in-memory-only test behavior).
func (m *mockEngine) PreCompactMessages(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
	if m.preCompactFn != nil {
		return m.preCompactFn(delivered, limit)
	}
	return nil, 0, false
}

// newTestConnectorWithHub builds a WUIConnector with a mockEngine and the
// given hub (for hub-routed dispatch tests). Tests configure the mock's
// function fields to control behavior.
func newTestConnectorWithHub(t *testing.T, h *hub.Hub) *WUIConnector {
	t.Helper()
	return newTestConnectorWithConfig(t, h, nil, nil)
}

// newTestConnectorWithConfig builds a WUIConnector with providers and
// providerConfigs for config/model_switch tests. The connector is constructed
// directly (not via New) because mockEngine is not a *engine.Engine; the slot
// map and active field are set up manually. The mock's onQueryDoneFn is wired
// to reset streamState so tests that trigger Query get realistic buffer cleanup.
func newTestConnectorWithConfig(t *testing.T, h *hub.Hub, providers map[string]llm.Provider, providerConfigs map[string]*config.Provider) *WUIConnector {
	t.Helper()
	mock := &mockEngine{}
	const engineID = "main"
	c := &WUIConnector{
		mgr:             nil,
		slots:           make(map[string]*engineSlot),
		pendingAsks:     make(map[string]*types.AskEvent),
		providers:       providers,
		providerConfigs: providerConfigs,
		wsCh:            make(chan []byte, 1024),
		done:            make(chan struct{}),
		testMock:        mock,
		thumbs:          newThumbCache(),
	}
	activeID := engineID
	c.active.Store(&activeID)
	slot := &engineSlot{
		engineID:    engineID,
		engine:      mock,
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	slot.active.Store(true)
	if h != nil {
		slot.unsubscribe = h.Subscribe(&engineHubShim{engineID: engineID, c: c})
	}
	c.slots[engineID] = slot
	mock.onQueryDoneFn = func() {
		slot.taskToolIDs = make(map[string]bool)
	}
	go c.wsWriter()
	t.Cleanup(c.Stop)
	return c
}

// newTestConnector returns a connector with a fresh hub.
func newTestConnector(t *testing.T) *WUIConnector {
	t.Helper()
	return newTestConnectorWithHub(t, hub.NewHub())
}

// mock returns the connector's mockEngine. Uses the cached testMock pointer
// (set by newTestConnector*) for lock-free access. Falls back to activeSlot's
// engine for connectors built via the real New(). Panics if neither holds a
// *mockEngine.
func (c *WUIConnector) mock() *mockEngine {
	if c.testMock != nil {
		return c.testMock.(*mockEngine)
	}
	if slot := c.activeSlot(); slot != nil {
		if m, ok := slot.engine.(*mockEngine); ok {
			return m
		}
	}
	panic("mock() called on connector without a mockEngine")
}

// firstPendingAskIDTest returns the id of the first stored pending ask.
func (c *WUIConnector) firstPendingAskIDTest(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(time.Second) // REAL-TIME
	for time.Now().Before(deadline) {       // REAL-TIME
		c.pendingMu.Lock()
		for id := range c.pendingAsks {
			c.pendingMu.Unlock()
			return id
		}
		c.pendingMu.Unlock()
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	return ""
}

// respondToAskTest writes a response to the pending ask with the given id.
func (c *WUIConnector) respondToAskTest(t *testing.T, id string, resp types.AskResponse) {
	t.Helper()
	c.pendingMu.Lock()
	ask := c.pendingAsks[id]
	delete(c.pendingAsks, id)
	c.pendingMu.Unlock()
	if ask == nil || ask.ResponseCh == nil {
		t.Fatalf("respondToAskTest: no pending ask with id %q", id)
	}
	select {
	case ask.ResponseCh <- resp:
	default:
		t.Fatalf("respondToAskTest: ResponseCh blocked")
	}
}

// dialAndStore connects a WS client to c's endpoint and drains the initial
// takeover frames (connect_status, config, engine_list, task_list, history,
// replay, stats) so the caller can read events/responses immediately. Returns
// the client conn with all initial frames consumed.
// Tests that don't need history must set mock().messagesFn to return nil.
func dialAndStore(t *testing.T, c *WUIConnector) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	var handlerWG sync.WaitGroup
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		handlerWG.Add(1)
		defer handlerWG.Done()
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
	srv := httptest.NewServer(mux)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	t.Cleanup(func() {
		_ = ws.Close()
		handlerWG.Wait()
		srv.Close()
	})
	return ws
}

// activeStreamBufLen returns the number of blocks in the active engine's
// streamState. Test accessor for verifying in-flight state.
func (c *WUIConnector) activeStreamBufLen() int {
	c.slotsMu.RLock()
	defer c.slotsMu.RUnlock()
	if s := c.slots[c.ActiveID()]; s != nil {
		return len(s.streamState.blocks)
	}
	return 0
}

// streamStateCount returns the number of blocks in the named engine's
// streamState. Test accessor for multi-engine tests.
func streamStateCount(c *WUIConnector, engineID string) int {
	c.slotsMu.RLock()
	defer c.slotsMu.RUnlock()
	s := c.slots[engineID]
	if s == nil {
		return 0
	}
	return len(s.streamState.blocks)
}

// activeEngineTest returns the active engine's engineClient for test use.
func (c *WUIConnector) activeEngineTest() engineClient {
	return c.activeEngine()
}

// activeSlotTest returns the active slot, fataling if nil.
func (c *WUIConnector) activeSlotTest(t *testing.T) *engineSlot {
	t.Helper()
	slot := c.activeSlot()
	if slot == nil {
		t.Fatal("activeSlot() returned nil")
	}
	return slot
}

// drainInitialFrames reads takeover frames until the metadata frame is consumed.
// The new takeover sequence sends a single "metadata" composite frame.
func drainInitialFrames(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	for {
		data := readWSMessage(t, ws)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == "metadata" {
			return
		}
	}
}

// readMetadata reads one WS message, asserts it is a metadata frame, and
// returns the raw JSON of each sub-field. Tests use this to extract connect,
// config, stats, snapshot, etc. from the composite metadata frame.
func readMetadata(t *testing.T, ws *websocket.Conn) struct {
	Connect  json.RawMessage
	Config   json.RawMessage
	Engines  json.RawMessage
	Tasks    json.RawMessage
	History  json.RawMessage
	Snapshot json.RawMessage
	Stats    json.RawMessage
} {
	t.Helper()
	data := readWSMessage(t, ws)
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		t.Fatalf("unmarshal metadata head: %v", err)
	}
	if head.Type != "metadata" {
		t.Fatalf("expected metadata frame, got type %q", head.Type)
	}
	var raw struct {
		Connect  json.RawMessage `json:"connect"`
		Config   json.RawMessage `json:"config"`
		Engines  json.RawMessage `json:"engines"`
		Tasks    json.RawMessage `json:"tasks"`
		History  json.RawMessage `json:"history"`
		Snapshot json.RawMessage `json:"snapshot"`
		Stats    json.RawMessage `json:"stats"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal metadata body: %v", err)
	}
	return struct {
		Connect  json.RawMessage
		Config   json.RawMessage
		Engines  json.RawMessage
		Tasks    json.RawMessage
		History  json.RawMessage
		Snapshot json.RawMessage
		Stats    json.RawMessage
	}{
		Connect:  raw.Connect,
		Config:   raw.Config,
		Engines:  raw.Engines,
		Tasks:    raw.Tasks,
		History:  raw.History,
		Snapshot: raw.Snapshot,
		Stats:    raw.Stats,
	}
}

// extractSnapshotFromMetadata parses the snapshot field (a json.RawMessage
// already extracted by readMetadata) and returns the stream blocks.
// Returns nil if the snapshot is empty or absent.
func extractSnapshotFromMetadata(t *testing.T, snapshotRaw json.RawMessage) []streamBlock {
	t.Helper()
	if len(snapshotRaw) == 0 {
		return nil
	}
	var snap struct {
		Blocks []streamBlock `json:"blocks"`
	}
	if err := json.Unmarshal(snapshotRaw, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	return snap.Blocks
}
