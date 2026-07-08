package webchat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/hub"
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

	// Recorded calls for assertions.
	queryCalls         []queryCall
	enqueueCalls       []types.QueuedItem
	abortCount         int
	rewindCalls        []int
	removeAttachment   []string
	removeAttachmentFn func(uuid string) bool
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

// newTestConnectorWithHub builds a WebChatConnector with a mockEngine and the
// given hub (for hub-routed dispatch tests). Tests configure the mock's
// function fields to control behavior.
func newTestConnectorWithHub(t *testing.T, h *hub.Hub) *WebChatConnector {
	t.Helper()
	c := &WebChatConnector{
		engine:      &mockEngine{},
		hub:         h,
		pendingAsks: make(map[string]*types.AskEvent),
		taskToolIDs: make(map[string]bool),
	}
	c.OnStreamDone = func() {
		c.writeMu.Lock()
		c.currentTurnBuf = nil
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

// dialAndStore connects a WS client to c's endpoint and drains the
// connect_status frame that the takeover always sends first. Returns the
// client conn with connect_status consumed, ready for Handle-event reads.
// Tests that don't need history must set mock().messagesFn to return nil.
func dialAndStore(t *testing.T, c *WebChatConnector) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	_ = readWSMessage(t, ws) // drain connect_status
	return ws
}
