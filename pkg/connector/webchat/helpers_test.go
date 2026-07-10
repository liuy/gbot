package webchat

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
// The webchat WS tests use real TCP connections (httptest.Server + gorilla
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
	mu sync.Mutex

	queryFn        func(ctx context.Context, userMessage, systemPrompt string)
	isBusyFn       func() bool
	messagesFn     func() []types.Message
	toolsFn        func() map[string]tool.Tool
	enqueueFn      func(item types.QueuedItem)
	abortFn        func()
	rewindToFn     func(idx int) error
	systemPromptFn func() string
	taskListFn     func() *task.List
	// onQueryDoneFn simulates engine committing an assistant response.
	// Called after queryFn finishes — mirrors real engine's appendMessage + OnStreamDone.
	onQueryDoneFn func()

	switchSessionFn func(sessionID string) error
	listSessionsFn  func(limit int) ([]*short.Session, error)
	newSessionFn    func() (string, error)
	updateTitleFn   func(sessionID, title string) error
	sessionIDFn     func() string
	engineIDFn      func() string
	modelFn         func() string
	projectDirFn    func() string
	setProviderFn   func(llm.Provider)
	setModelFn      func(string)
	providerFn      func() llm.Provider
	setMaxTokensFn  func(int)
	setInputModFn   func([]string)
	updateAutoFn    func(engine.AutoCompactConfig)

	// Recorded calls for assertions.
	queryCalls         []queryCall
	enqueueCalls       []types.QueuedItem
	abortCount         int
	rewindCalls        []int
	removeAttachment   []string
	removeAttachmentFn func(uuid string) bool
	switchSessionCalls []string
	newSessionCalls    int
	updateTitleCalls   []struct{ ID, Title string }
	setProviderCalls   []string
	setModelCalls      []string
	setMaxTokensCalls  []int
	setInputModCalls   [][]string
	updateAutoCalls    int
}

type queryCall struct {
	userMessage  string
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
	// This mirrors real engine: appendMessage(*resp) → e.OnStreamDone().
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

func (m *mockEngine) Messages() []types.Message {
	if m.messagesFn != nil {
		return m.messagesFn()
	}
	return nil
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

// newTestConnectorWithHub builds a WebChatConnector with a mockEngine and the
// given hub (for hub-routed dispatch tests). Tests configure the mock's
// function fields to control behavior.
func newTestConnectorWithHub(t *testing.T, h *hub.Hub) *WebChatConnector {
	t.Helper()
	return newTestConnectorWithConfig(t, h, nil, nil)
}

// newTestConnectorWithConfig builds a WebChatConnector with providers and
// providerConfigs for config/model_switch tests.
func newTestConnectorWithConfig(t *testing.T, h *hub.Hub, providers map[string]llm.Provider, providerConfigs map[string]*config.Provider) *WebChatConnector {
	t.Helper()
	c := &WebChatConnector{
		engine:          &mockEngine{},
		hub:             h,
		pendingAsks:     make(map[string]*types.AskEvent),
		taskToolIDs:     make(map[string]bool),
		providers:       providers,
		providerConfigs: providerConfigs,
	}
	c.OnStreamDone = func() {
		c.writeMu.Lock()
		c.streamBuf = nil
		c.taskToolIDs = make(map[string]bool)
		c.writeMu.Unlock()
	}
	c.mock().onQueryDoneFn = c.OnStreamDone
	if h != nil {
		c.unsubscribe = h.Subscribe(c)
	}
	t.Cleanup(c.Stop)
	return c
}

// newTestConnector returns a connector with a fresh hub.
func newTestConnector(t *testing.T) *WebChatConnector {
	t.Helper()
	return newTestConnectorWithHub(t, hub.NewHub())
}

// mock returns the connector's mockEngine. Panics if the engine is not a
// *mockEngine (i.e., the connector was not created via newTestConnector*).
func (c *WebChatConnector) mock() *mockEngine {
	return c.engine.(*mockEngine)
}

// firstPendingAskIDTest returns the id of the first stored pending ask.
func (c *WebChatConnector) firstPendingAskIDTest(t *testing.T) string {
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
func (c *WebChatConnector) respondToAskTest(t *testing.T, id string, resp types.AskResponse) {
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
// takeover frames (connect_status, optional history, config, optional task_list)
// so the caller can read events/responses immediately. Returns the client conn
// with all initial frames consumed.
// Tests that don't need history must set mock().messagesFn to return nil.
func dialAndStore(t *testing.T, c *WebChatConnector) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	return ws
}

// drainInitialFrames reads takeover frames until the config frame is consumed.
// The takeover sequence is: connect_status, history (optional), config, replay,
// task_list (optional). We drain connect_status + history (if present) + config.
// The caller is responsible for reading replay/task_list frames if relevant.
func drainInitialFrames(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	for {
		data := readWSMessage(t, ws)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == "config" {
			return
		}
	}
}
