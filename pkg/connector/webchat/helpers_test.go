package webchat

import (
	"context"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
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

// newTestConnectorWithHub builds a WebChatConnector with engine=nil and the
// given hub (for hub-routed dispatch tests). The test seams queryFn/isBusyFn
// are left as the engine-bound defaults but with engine=nil they are inert;
// tests override them.
func newTestConnectorWithHub(t *testing.T, h *hub.Hub) *WebChatConnector {
	t.Helper()
	c := &WebChatConnector{
		engine:      nil,
		hub:         h,
		pendingAsks: make(map[string]*types.AskEvent),
		msgCh:       make(chan []byte, handlerBufSize),
	}
	c.queryFn = func(_ context.Context, _, _ string) {} // tests override
	c.isBusyFn = func() bool { return false }
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

// queryCancelActiveTest reports whether a query cancel is currently stored.
func (c *WebChatConnector) queryCancelActiveTest() bool {
	c.queryCancelMu.Lock()
	defer c.queryCancelMu.Unlock()
	return c.queryCancel != nil
}
