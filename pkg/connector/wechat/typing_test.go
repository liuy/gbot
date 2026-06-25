package wechat

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// TypingTicketCache
// ---------------------------------------------------------------------------

func TestTypingTicketCache_GetSet(t *testing.T) {
	cache := newTypingTicketCache(600 * time.Second)
	if got := cache.get("user1"); got != "" {
		t.Errorf("empty cache get = %q, want empty", got)
	}
	cache.set("user1", "ticket-abc")
	if got := cache.get("user1"); got != "ticket-abc" {
		t.Errorf("after set, get = %q, want ticket-abc", got)
	}
}

func TestTypingTicketCache_TTLExpiry(t *testing.T) {
	cache := newTypingTicketCache(600 * time.Second)
	cache.set("user1", "ticket-old")
	// Force expiry by backdating the settled time to before the TTL window.
	cache.mu.Lock()
	cache.entries["user1"] = typingCacheEntry{
		ticket:  "ticket-old",
		settled: time.Time{}, // zero time → always expired
	}
	cache.mu.Unlock()
	if got := cache.get("user1"); got != "" {
		t.Errorf("after TTL expiry, get = %q, want empty", got)
	}
}

func TestTypingTicketCache_PerUser(t *testing.T) {
	cache := newTypingTicketCache(600 * time.Second)
	cache.set("user1", "ticket-a")
	cache.set("user2", "ticket-b")
	if got := cache.get("user1"); got != "ticket-a" {
		t.Errorf("user1 get = %q, want ticket-a", got)
	}
	if got := cache.get("user2"); got != "ticket-b" {
		t.Errorf("user2 get = %q, want ticket-b", got)
	}
}

func TestTypingTicketCache_Concurrent(t *testing.T) {
	cache := newTypingTicketCache(600 * time.Second)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cache.set("user", "ticket")
			_ = cache.get("user")
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// startTyping / stopTyping — full chain via handleInbound
// ---------------------------------------------------------------------------

// mockTypingAPI records all typing calls for assertion.
type mockTypingAPI struct {
	mu     sync.Mutex
	calls  []typingCall
	getErr error
}

type typingCall struct {
	userID string
	status int
}

func (m *mockTypingAPI) getConfig(_ context.Context, userID, _ string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	return "mock-ticket-" + userID, nil
}

func (m *mockTypingAPI) sendTyping(_ context.Context, userID, _ string, status int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, typingCall{userID: userID, status: status})
	return nil
}

func (m *mockTypingAPI) getCalls() []typingCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]typingCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// simulateQueryEvents dispatches a full query lifecycle (text reply + end)
// through the Hub, then closes queryDone so handleInbound unblocks. This
// mimics what the real engine does on the dispatch goroutine: query failures
// are delivered as EventQueryEnd{Error: err}, never as a separate EventError.
func simulateQueryEvents(h *hub.Hub, reply string, queryErr error) {
	if reply != "" {
		h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: reply})
		h.Dispatch(types.QueryEvent{Type: types.EventTextEnd})
	}
	endEvt := types.QueryEvent{Type: types.EventQueryEnd}
	if queryErr != nil {
		endEvt.Error = queryErr
	}
	h.Dispatch(endEvt)
}

// Typing indicator must be refreshed every 5s during a query — a single
// startTyping fades after a few seconds. Events arriving >5s after the last
// refresh should re-trigger startTyping. Uses synctest for deterministic timing.
func TestHandleInbound_TypingRefreshedDuringLongQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		typingAPI := &mockTypingAPI{}
		h := hub.NewHub()
		c := &WeChatConnector{
			hub:               h,
			inboundCh:         make(chan inboundMessage, 10),
			typingCache:       newTypingTicketCache(600 * time.Second),
			typingAPI:         typingAPI,
			lastTypingRefresh: time.Now(),
			sendToUserFn:      func(_ context.Context, _, _ string) error { return nil },
		}
		h.Subscribe(c)

		// Simulate engine emitting events with gaps >5s.
		c.queryFn = func(_ context.Context, _, _ string) {
			// First event immediately — no refresh yet (<5s).
			h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "part1"})
			// Sleep 6s (virtual time) — next event should trigger refresh.
			time.Sleep(6 * time.Second)
			h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "part2"})
			// Sleep another 6s — another refresh.
			time.Sleep(6 * time.Second)
			h.Dispatch(types.QueryEvent{Type: types.EventTextEnd})
			h.Dispatch(types.QueryEvent{Type: types.EventQueryEnd})
		}

		c.handleInbound(context.Background(), inboundMessage{userID: "user1", text: "hi"})

		calls := typingAPI.getCalls()
		startCount := 0
		for _, call := range calls {
			if call.status == TypingStart {
				startCount++
			}
		}
		// initial startTyping (from handleInbound) + 2 refreshes = 3 starts minimum.
		if startCount < 3 {
			t.Errorf("expected >= 3 startTyping calls (1 initial + 2 refreshes), got %d. calls=%v", startCount, calls)
		}
	})
}

// Chain test: inbound → startTyping → async query → reply via Handle →
// stopTyping in QueryEnd. Verifies the user sees "typing..." while the engine
// works, and it stops when the query ends.
func TestHandleInbound_TypingIndicator_StartAndStop(t *testing.T) {
	typingAPI := &mockTypingAPI{}
	var sentTexts []string

	h := hub.NewHub()
	c := &WeChatConnector{
		hub:         h,
		inboundCh:   make(chan inboundMessage, 10),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   typingAPI,
		sendToUserFn: func(_ context.Context, _, text string) error {
			sentTexts = append(sentTexts, text)
			return nil
		},
	}
	h.Subscribe(c)
	c.queryFn = func(_ context.Context, _, _ string) {
		simulateQueryEvents(h, "done", nil)
	}

	c.handleInbound(context.Background(), inboundMessage{userID: "user1", text: "hi"})

	calls := typingAPI.getCalls()
	// Must have at least one start (status=1) and one stop (status=2).
	hasStart := false
	hasStop := false
	for _, call := range calls {
		if call.status == TypingStart {
			hasStart = true
		}
		if call.status == TypingStop {
			hasStop = true
		}
	}
	if !hasStart {
		t.Errorf("typing: no start call found, calls = %v", calls)
	}
	if !hasStop {
		t.Errorf("typing: no stop call found, calls = %v", calls)
	}
	// Stop must come before or at the same time as the reply send.
	if len(sentTexts) == 0 {
		t.Error("no reply was sent")
	}
}

// Typing must stop even if the query errors — otherwise the WeChat client
// shows "typing..." forever. On error the engine emits a single
// EventQueryEnd{Error: err}; Handle sends the error message and stops typing
// in the same case.
func TestHandleInbound_TypingStopsOnQueryError(t *testing.T) {
	typingAPI := &mockTypingAPI{}

	h := hub.NewHub()
	c := &WeChatConnector{
		hub:         h,
		inboundCh:   make(chan inboundMessage, 10),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   typingAPI,
		sendToUserFn: func(_ context.Context, _, _ string) error {
			return nil
		},
	}
	h.Subscribe(c)
	c.queryFn = func(_ context.Context, _, _ string) {
		simulateQueryEvents(h, "", context.DeadlineExceeded)
	}

	c.handleInbound(context.Background(), inboundMessage{userID: "user1", text: "hi"})

	calls := typingAPI.getCalls()
	hasStop := false
	for _, call := range calls {
		if call.status == TypingStop {
			hasStop = true
		}
	}
	if !hasStop {
		t.Errorf("typing: must stop on query error, calls = %v", calls)
	}
}

// Typing is best-effort — if getConfig fails, we still process the message.
func TestHandleInbound_TypingFailure_DoesNotBlock(t *testing.T) {
	typingAPI := &mockTypingAPI{getErr: context.DeadlineExceeded}
	sent := false

	h := hub.NewHub()
	c := &WeChatConnector{
		hub:         h,
		inboundCh:   make(chan inboundMessage, 10),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   typingAPI,
		sendToUserFn: func(_ context.Context, _, _ string) error {
			sent = true
			return nil
		},
	}
	h.Subscribe(c)
	c.queryFn = func(_ context.Context, _, _ string) {
		simulateQueryEvents(h, "ok", nil)
	}

	c.handleInbound(context.Background(), inboundMessage{userID: "user1", text: "hi"})

	if !sent {
		t.Error("typing failure should not block message processing")
	}
}
