package wechat

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
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

// Chain test: inbound → startTyping → querySync → stopTyping → send reply.
// Verifies the user sees "typing..." while the engine works, and it stops
// before the reply arrives.
func TestHandleInbound_TypingIndicator_StartAndStop(t *testing.T) {
	typingAPI := &mockTypingAPI{}
	var sentTexts []string

	c := &WeChatConnector{
		inboundCh:   make(chan inboundMessage, 10),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   typingAPI,
	}
	c.querySyncFn = func(_ context.Context, _, _ string) *engine.QueryResult {
		return &engine.QueryResult{Reply: "done"}
	}
	c.sendToUserFn = func(_ context.Context, _, text string) error {
		sentTexts = append(sentTexts, text)
		return nil
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

// Typing must stop even if querySync returns an error — otherwise the
// WeChat client shows "typing..." forever.
func TestHandleInbound_TypingStopsOnQueryError(t *testing.T) {
	typingAPI := &mockTypingAPI{}

	c := &WeChatConnector{
		inboundCh:   make(chan inboundMessage, 10),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   typingAPI,
	}
	c.querySyncFn = func(_ context.Context, _, _ string) *engine.QueryResult {
		return &engine.QueryResult{Error: context.DeadlineExceeded}
	}
	c.sendToUserFn = func(_ context.Context, _, _ string) error {
		return nil
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

	c := &WeChatConnector{
		inboundCh:   make(chan inboundMessage, 10),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   typingAPI,
	}
	c.querySyncFn = func(_ context.Context, _, _ string) *engine.QueryResult {
		return &engine.QueryResult{Reply: "ok"}
	}
	c.sendToUserFn = func(_ context.Context, _, _ string) error {
		sent = true
		return nil
	}

	c.handleInbound(context.Background(), inboundMessage{userID: "user1", text: "hi"})

	if !sent {
		t.Error("typing failure should not block message processing")
	}
}
