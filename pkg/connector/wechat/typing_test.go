package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
			lastTypingRefresh: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
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

// TestHandle_TypingGuardForJobDeltas pins the pre-switch guard: background-job
// output deltas are not conversation activity and must return BEFORE the
// typing-refresh block (an idle-time bg job would otherwise pulse the typing
// indicator forever), while a sync sub-agent delta (Agent non-nil, JobID
// empty) still refreshes typing — a bare Agent != nil guard would silence it.
// The guard's placement (before the switch, not inside it) is pinned by the
// same fixture: inside the switch the typing block has already run.
func TestHandle_TypingGuardForJobDeltas(t *testing.T) {
	typingAPI := &mockTypingAPI{}
	c := &WeChatConnector{
		hub:               hub.NewHub(),
		inboundCh:         make(chan inboundMessage, 10),
		typingCache:       newTypingTicketCache(600 * time.Second),
		typingAPI:         typingAPI,
		activeUserID:      "user1",
		lastTypingRefresh: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		sendToUserFn:      func(_ context.Context, _, _ string) error { return nil },
	}

	if calls := typingAPI.getCalls(); len(calls) != 0 {
		t.Fatalf("baseline typing calls = %d, want 0", len(calls))
	}

	// Fixture 1 — job-tagged delta, the exact shape bootstrap produces: the
	// guard must return before the typing-refresh block runs.
	c.Handle(types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		Agent:      &types.AgentMeta{JobID: "bg-1"},
		ToolResult: &types.ToolResultEvent{ToolUseID: "bg-1", DisplayOutput: "x"},
	})
	if calls := typingAPI.getCalls(); len(calls) != 0 {
		t.Fatalf("job-tagged delta made %d typing calls, want 0 (guard must precede the refresh block): %+v", len(calls), calls)
	}

	// Fixture 2 — sync sub-agent delta (Agent non-nil, JobID empty): the
	// refresh condition is still live, so exactly one TypingStart fires.
	c.Handle(types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		Agent:      &types.AgentMeta{ParentToolUseID: "toolu_p"},
		ToolResult: &types.ToolResultEvent{ToolUseID: "toolu_p", DisplayOutput: "y"},
	})
	calls := typingAPI.getCalls()
	if len(calls) != 1 || calls[0].userID != "user1" || calls[0].status != TypingStart {
		t.Fatalf("sync sub-agent delta calls = %+v, want exactly one TypingStart for user1", calls)
	}

	// Fixture 3 — a NON-delta event carrying a JobID (a background job's
	// process event): still conversation activity — the guard's Type clause
	// must not swallow it. Backdate the stamp so the refresh condition is
	// live again after fixture 2's startTyping re-stamped it.
	c.lastTypingRefresh = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Handle(types.QueryEvent{
		Type:  types.EventTextDelta,
		Agent: &types.AgentMeta{JobID: "bg-1"},
		Text:  "bg text",
	})
	calls = typingAPI.getCalls()
	if len(calls) != 2 || calls[1].status != TypingStart {
		t.Fatalf("non-delta job-tagged event calls = %+v, want a second TypingStart", calls)
	}

	// Fixture 4 — a foreground output delta (Agent nil): the guard's nil
	// check must short-circuit before the JobID read; a removed nil clause
	// panics inside the hub's recover and the refresh never fires.
	c.lastTypingRefresh = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Handle(types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{ToolUseID: "toolu_fg", DisplayOutput: "fg"},
	})
	calls = typingAPI.getCalls()
	if len(calls) != 3 || calls[2].status != TypingStart {
		t.Fatalf("foreground delta calls = %+v, want a third TypingStart", calls)
	}
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

// ---------------------------------------------------------------------------
// iLinkTypingAPI — getConfig / sendTyping via real HTTP
// ---------------------------------------------------------------------------

func TestILinkTypingAPI_GetConfig(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ret":0,"typing_ticket":"ticket-from-api"}`)
	}))
	defer srv.Close()

	api := &iLinkTypingAPI{client: srv.Client(), baseURL: srv.URL, token: "tok"}
	ticket, err := api.getConfig(context.Background(), "user1", "")
	if err != nil {
		t.Fatalf("getConfig: %v", err)
	}
	if ticket != "ticket-from-api" {
		t.Errorf("ticket = %q, want ticket-from-api", ticket)
	}
}

func TestILinkTypingAPI_GetConfig_Error(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"ret":-1}`)
	}))
	defer srv.Close()

	api := &iLinkTypingAPI{client: srv.Client(), baseURL: srv.URL, token: "tok"}
	_, err := api.getConfig(context.Background(), "user1", "")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want 'HTTP 500'", err.Error())
	}
}

func TestILinkTypingAPI_SendTyping(t *testing.T) {
	t.Parallel()
	var gotUserID string
	var gotStatus int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotUserID, _ = payload["ilink_user_id"].(string)
		if s, ok := payload["status"].(float64); ok {
			gotStatus = int(s)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ret":0}`)
	}))
	defer srv.Close()

	api := &iLinkTypingAPI{client: srv.Client(), baseURL: srv.URL, token: "tok"}
	err := api.sendTyping(context.Background(), "user1", "ticket-abc", TypingStart)
	if err != nil {
		t.Fatalf("sendTyping: %v", err)
	}
	if gotUserID != "user1" {
		t.Errorf("userID = %q, want user1", gotUserID)
	}
	if gotStatus != TypingStart {
		t.Errorf("status = %d, want %d", gotStatus, TypingStart)
	}
}

func TestILinkTypingAPI_SendTyping_Error(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"ret":-1}`)
	}))
	defer srv.Close()

	api := &iLinkTypingAPI{client: srv.Client(), baseURL: srv.URL, token: "tok"}
	err := api.sendTyping(context.Background(), "user1", "ticket-abc", TypingStop)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want 'HTTP 500'", err.Error())
	}
}

// mockTypingAPISendError always returns a valid ticket from getConfig but
// returns an error from sendTyping, exercising the error logging paths in
// startTyping and stopTyping.
type mockTypingAPISendError struct{}

func (m *mockTypingAPISendError) getConfig(_ context.Context, userID, _ string) (string, error) {
	return "ticket-" + userID, nil
}

func (m *mockTypingAPISendError) sendTyping(_ context.Context, _, _ string, _ int) error {
	return context.DeadlineExceeded
}

func TestStartTyping_SendTypingError(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   &mockTypingAPISendError{},
	}
	c.startTyping(context.Background(), "user1")
	// Should not panic; lastTypingRefresh should remain zero.
	if !c.lastTypingRefresh.IsZero() {
		t.Error("lastTypingRefresh should remain zero after sendTyping error")
	}
}

func TestStopTyping_SendTypingError(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(600 * time.Second),
		typingAPI:   &mockTypingAPISendError{},
	}
	c.stopTyping(context.Background(), "user1")
	// Should not panic.
}

// pinTypingMock records the context of every API call, which the shared
// mockTypingAPI discards — several mutants only show up as a dropped ctx.
type pinTypingMock struct {
	mu       sync.Mutex
	getCtx   []context.Context
	sendCtx  []context.Context
	sends    []pinSendCall
	getErr   error
	sendErr  error
	ticket   string
	getCalls int
}

type pinSendCall struct {
	userID string
	ticket string
	status int
}

func (m *pinTypingMock) getConfig(ctx context.Context, userID, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	m.getCtx = append(m.getCtx, ctx)
	if m.getErr != nil {
		return "", m.getErr
	}
	if m.ticket != "" {
		return m.ticket, nil
	}
	return "ticket-" + userID, nil
}

func (m *pinTypingMock) sendTyping(ctx context.Context, userID, ticket string, status int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendCtx = append(m.sendCtx, ctx)
	m.sends = append(m.sends, pinSendCall{userID: userID, ticket: ticket, status: status})
	return m.sendErr
}

func (m *pinTypingMock) snapshot() (getCalls int, sends []pinSendCall, getCtx, sendCtx []context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls, append([]pinSendCall(nil), m.sends...), append([]context.Context(nil), m.getCtx...), append([]context.Context(nil), m.sendCtx...)
}

func pinNewTypingConnector(mock typingAPI) *WeChatConnector {
	return &WeChatConnector{
		typingCache: newTypingTicketCache(typingTTL),
		typingAPI:   mock,
	}
}

// The 600s ticket TTL is the iLink server-side window; the refresh cadence
// everywhere else is derived from it.
func TestPinTypingTTL(t *testing.T) {
	if typingTTL != 600*time.Second {
		t.Errorf("typingTTL = %v, want 600s", typingTTL)
	}
}

// A cached ticket expires at exactly ttl (>=, not >) and the expired entry is
// evicted rather than left in the map.
func TestPinTypingCacheExpiryBoundary(t *testing.T) {
	c := newTypingTicketCache(60 * time.Second)
	c.set("u1", "t1")
	c.mu.Lock()
	e := c.entries["u1"]
	e.settled = e.settled.Add(-60 * time.Second)
	c.entries["u1"] = e
	c.mu.Unlock()

	if got := c.get("u1"); got != "" {
		t.Errorf("get at exact ttl = %q, want %q (entry is expired)", got, "")
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("entries after expired get = %d, want 0 (evicted)", n)
	}

	// One tick inside the window must still return the ticket.
	c.set("u2", "t2")
	c.mu.Lock()
	e2 := c.entries["u2"]
	e2.settled = e2.settled.Add(-59 * time.Second)
	c.entries["u2"] = e2
	c.mu.Unlock()
	if got := c.get("u2"); got != "t2" {
		t.Errorf("get just inside ttl = %q, want %q", got, "t2")
	}
}

// With a virtual clock the elapsed time is exactly zero, so ttl 0 pins the
// expiry comparison at its boundary: elapsed == ttl is already expired.
func TestPinTypingCacheExpiryAtZeroElapsed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTypingTicketCache(0)
		c.set("u1", "t1")
		if got := c.get("u1"); got != "" {
			t.Errorf("get with ttl 0 = %q, want %q (elapsed == ttl is expired)", got, "")
		}
	})
}

// The map must stay guarded for the whole of get, not just until the first
// return path: concurrent set/get on one cache is a data race otherwise.
func TestPinTypingCacheConcurrentAccess(t *testing.T) {
	c := newTypingTicketCache(60 * time.Second)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				c.set("u", "t")
				if got := c.get("u"); got != "t" {
					t.Errorf("concurrent get = %q, want %q", got, "t")
					return
				}
			}
		})
	}
	wg.Wait()
	if got := c.get("u"); got != "t" {
		t.Errorf("get after concurrent access = %q, want %q", got, "t")
	}
}

// get() must release the lock on every return path; a missing user is a
// second call on the same cache, which deadlocks if the unlock is skipped.
func TestPinTypingCacheUnlocksOnMiss(t *testing.T) {
	c := newTypingTicketCache(60 * time.Second)
	if got := c.get("absent"); got != "" {
		t.Fatalf("get(absent) = %q, want %q", got, "")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.get("absent")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second get() blocked: the mutex is not released on the miss path")
	}
}

// A live cached ticket must be reused without a getConfig round-trip.
func TestPinEnsureTicketReusesCache(t *testing.T) {
	mock := &pinTypingMock{}
	c := pinNewTypingConnector(mock)
	first := c.ensureTicket(context.Background(), "u1")
	if first != "ticket-u1" {
		t.Fatalf("ensureTicket = %q, want %q", first, "ticket-u1")
	}
	second := c.ensureTicket(context.Background(), "u1")
	getCalls, _, _, _ := mock.snapshot()
	if second != "ticket-u1" {
		t.Errorf("second ensureTicket = %q, want %q", second, "ticket-u1")
	}
	if getCalls != 1 {
		t.Errorf("getConfig calls = %d, want 1 (cached ticket must not re-fetch)", getCalls)
	}
}

// startTyping/stopTyping must thread the caller's context into both the
// ticket fetch and the send, and must record the refresh on success.
func TestPinTypingThreadsContext(t *testing.T) {
	mock := &pinTypingMock{}
	c := pinNewTypingConnector(mock)
	c.lastTypingRefresh = time.Time{}
	ctx := t.Context()

	c.startTyping(ctx, "u1")
	// A fresh cache forces stopTyping to fetch its own ticket, so the ctx it
	// passes to getConfig is observable.
	c2 := pinNewTypingConnector(mock)
	c2.stopTyping(ctx, "u1")

	_, sends, getCtx, sendCtx := mock.snapshot()
	if len(getCtx) != 2 {
		t.Fatalf("getConfig calls = %d, want 2", len(getCtx))
	}
	for i, gctx := range getCtx {
		if gctx == nil {
			t.Errorf("getConfig[%d] ctx = nil, want the caller's ctx", i)
		}
	}
	wantSends := []pinSendCall{{userID: "u1", ticket: "ticket-u1", status: TypingStart}, {userID: "u1", ticket: "ticket-u1", status: TypingStop}}
	if len(sends) != 2 || sends[0] != wantSends[0] || sends[1] != wantSends[1] {
		t.Fatalf("sends = %+v, want %+v", sends, wantSends)
	}
	if len(sendCtx) != 2 {
		t.Fatalf("sendTyping calls = %d, want 2", len(sendCtx))
	}
	for i, sctx := range sendCtx {
		if sctx == nil {
			t.Errorf("sendTyping[%d] ctx = nil, want the caller's ctx", i)
		}
	}
	if c.lastTypingRefresh.IsZero() {
		t.Error("lastTypingRefresh not updated after a successful startTyping")
	}
}

// A failed ticket fetch must stop before any send and be logged.
func TestPinStartTypingStopsOnGetConfigError(t *testing.T) {
	mock := &pinTypingMock{getErr: errors.New("boom")}
	c := pinNewTypingConnector(mock)
	logs := pinCaptureSlog(t, func() {
		c.startTyping(context.Background(), "u1")
		c.stopTyping(context.Background(), "u1")
	})
	_, sends, _, _ := mock.snapshot()
	if len(sends) != 0 {
		t.Errorf("sends = %+v, want none when the ticket fetch fails", sends)
	}
	if got := strings.Count(logs, "getConfig failed"); got != 2 {
		t.Errorf("logs contain %d %q lines, want 2: %q", got, "getConfig failed", logs)
	}
}

// A failed send must be logged with its direction, and a successful send must
// not log at all.
func TestPinTypingSendFailureLogged(t *testing.T) {
	mock := &pinTypingMock{sendErr: errors.New("send boom")}
	c := pinNewTypingConnector(mock)
	logs := pinCaptureSlog(t, func() {
		c.startTyping(context.Background(), "u1")
		c.stopTyping(context.Background(), "u1")
	})
	if !strings.Contains(logs, "sendTyping start failed") {
		t.Errorf("logs = %q, want a start-failure line", logs)
	}
	if !strings.Contains(logs, "sendTyping stop failed") {
		t.Errorf("logs = %q, want a stop-failure line", logs)
	}

	ok := &pinTypingMock{}
	c2 := pinNewTypingConnector(ok)
	logs2 := pinCaptureSlog(t, func() {
		c2.startTyping(context.Background(), "u1")
		c2.stopTyping(context.Background(), "u1")
	})
	if strings.Contains(logs2, "sendTyping") {
		t.Errorf("logs on success = %q, want no sendTyping failure lines", logs2)
	}
}

// startTyping must return before touching the API when no typingAPI is wired.
func TestPinStartTypingNilAPI(t *testing.T) {
	c := &WeChatConnector{typingCache: newTypingTicketCache(typingTTL)}
	pinCaptureSlog(t, func() {
		c.startTyping(context.Background(), "u1")
		c.stopTyping(context.Background(), "u1")
	})
	if !c.lastTypingRefresh.IsZero() {
		t.Errorf("lastTypingRefresh = %v with a nil typingAPI, want it untouched", c.lastTypingRefresh)
	}
}
