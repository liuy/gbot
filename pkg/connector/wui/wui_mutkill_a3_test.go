package wui

// Mutant-killing tests for connector.go batch A3 (mutation escapes listed in
// the batch manifest). Every test pins observable behavior that the listed
// mutations break; mutants that cannot change any observable behavior are
// argued equivalent in the batch report instead.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/quota"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// nonHubDispatcherA3 is an EventDispatcher that is not a *hub.Hub, used to
// exercise registerEngine's dispatcher-type guard.
type nonHubDispatcherA3 struct{}

func (nonHubDispatcherA3) Dispatch(types.QueryEvent) {}

func newMockSlotA3(id string, m engineClient) *engineSlot {
	return &engineSlot{
		engineID:           id,
		engine:             m,
		taskToolIDs:        map[string]bool{},
		jobView:            map[string]*jobViewEntry{},
		jobFeeds:           map[string]*jobFeedState{},
		innerToolToFeed:    map[string]string{},
		pendingAgentEvents: map[string]*pendingAgentEvents{},
	}
}

// bareConnectorA3 builds a WUIConnector with NO background goroutines (no
// wsWriter, no jobs watcher): outbound frames stay in the queue so tests can
// inspect them deterministically via takeAllFramesA3.
func bareConnectorA3(t *testing.T, m *mockEngine) (*WUIConnector, *mockEngine) {
	t.Helper()
	if m == nil {
		m = &mockEngine{projectDir: t.TempDir()}
	}
	c := &WUIConnector{
		slots:       map[string]*engineSlot{},
		pendingAsks: map[string]*types.AskEvent{},
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	activeID := "main"
	c.active.Store(&activeID)
	slot := newMockSlotA3("main", m)
	slot.active.Store(true)
	c.slots["main"] = slot
	return c, m
}

func takeAllFramesA3(c *WUIConnector) []string {
	var out []string
	for _, f := range c.outQ.Load().take() {
		out = append(out, string(f.data))
	}
	return out
}

func frameJSONFieldA3(t *testing.T, frame, field string, dst any) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(frame), &top); err != nil {
		t.Fatalf("unmarshal frame %s: %v", frame, err)
	}
	raw, ok := top[field]
	if !ok || len(raw) == 0 {
		t.Fatalf("frame %s has no %q field", frame, field)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal %s.%s: %v", field, field, err)
	}
}

// msgRecorderA3 records every Info-or-higher slog record's message so tests
// can assert exact log lines (warn-once semantics, info milestones).
type msgRecorderA3 struct {
	mu   sync.Mutex
	msgs []string
}

func (h *msgRecorderA3) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }
func (h *msgRecorderA3) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}
func (h *msgRecorderA3) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *msgRecorderA3) WithGroup(string) slog.Handler      { return h }

func (h *msgRecorderA3) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

func swapMsgRecorderA3(t *testing.T) *msgRecorderA3 {
	t.Helper()
	h := &msgRecorderA3{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return h
}

// readWSFrameDeadlineA3 reads one WS message, returning (nil, false) on
// timeout instead of failing — callers decide whether absence is an error.
func readWSFrameDeadlineA3(t *testing.T, ws *websocket.Conn, d time.Duration) ([]byte, bool) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(d)) // REAL-TIME
	_, data, err := ws.ReadMessage()
	if err != nil {
		return nil, false
	}
	return data, true
}

func docBlockA3(path string) types.ContentBlock {
	return types.ContentBlock{Type: types.ContentTypeDocument, Name: "n.pdf", Path: path, Mime: "application/pdf", Size: 10}
}

func fileImageBlockA3(path, mediaType string) types.ContentBlock {
	return types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: mediaType, Path: path}}
}

func b64ImageBlockA3(mediaType, data string) types.ContentBlock {
	return types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", MediaType: mediaType, Data: data}}
}

func savedOfA3(t *testing.T, c *WUIConnector, id string) savedAttachment {
	t.Helper()
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()
	saved, ok := c.restoredUploads[id]
	if !ok {
		t.Fatalf("no restoredUploads entry for %q", id)
	}
	return saved
}

// withTimeoutA3 runs fn in a goroutine and fatals instead of hanging when a
// mutant strands a mutex (hub.Dispatch recovers panics, so a mutant can leave
// a production lock held forever — the kill must still be fast).
func withTimeoutA3(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s blocked (lock stranded by an earlier call?)", name)
	}
}

// buildRestoredMsgA3 is buildRestoredMsg behind withTimeoutA3: mutants that
// strand restoreMu (rememberUpload defer-removal) would otherwise hang the
// test instead of failing it.
func buildRestoredMsgA3(t *testing.T, c *WUIConnector, item types.QueuedItem) queuedMsgJSON {
	t.Helper()
	ch := make(chan queuedMsgJSON, 1)
	withTimeoutA3(t, "buildRestoredMsg", func() { ch <- c.buildRestoredMsg(item) })
	return <-ch
}

// ---------------------------------------------------------------------------
// thumbCache (lines 291-326)
// ---------------------------------------------------------------------------

func TestMutA3_ThumbCacheCapEviction(t *testing.T) {
	tc := newThumbCache()
	for i := 0; i < 101; i++ {
		tc.put(fmt.Sprintf("k%d", i), "v")
	}
	if _, ok := tc.m["k0"]; ok {
		t.Fatal("k0 must be evicted once the cache exceeds its cap of 100")
	}
	if got := len(tc.order); got != 100 {
		t.Fatalf("order length = %d, want 100", got)
	}
	if _, ok := tc.m["k100"]; !ok {
		t.Fatal("k100 must still be cached")
	}
	// Move-to-back: re-putting k1 refreshes it so the next eviction drops k2.
	tc.put("k1", "v2")
	tc.put("kNew", "v")
	if _, ok := tc.m["k1"]; !ok {
		t.Fatal("re-put key was evicted before older keys")
	}
	if _, ok := tc.m["k2"]; ok {
		t.Fatal("k2 (front of FIFO after k1 refresh) should have been evicted")
	}
	if got := tc.m["k1"]; got != "v2" {
		t.Fatalf("re-put value = %q, want v2", got)
	}
}

func TestMutA3_ThumbCacheMutexRelease(t *testing.T) {
	tc := newThumbCache()
	if v, ok := tc.get("a"); ok || v != "" {
		t.Fatalf("get on empty cache = (%q,%v), want (\"\",false)", v, ok)
	}
	if !tc.mu.TryLock() {
		t.Fatal("get() did not release tc.mu")
	}
	tc.mu.Unlock()

	tc.put("a", "1")
	if !tc.mu.TryLock() {
		t.Fatal("put() did not release tc.mu")
	}
	tc.mu.Unlock()
}

// ---------------------------------------------------------------------------
// outQueue (lines 431-527)
// ---------------------------------------------------------------------------

func TestMutA3_OutQueueCloseIdempotent(t *testing.T) {
	q := newOutQueue(2)
	q.push(wsMsg{data: []byte("x")}, nil)
	q.close()
	withTimeoutA3(t, "second close", q.close) // must be a no-op, not a panic or a stall
	var frames []wsMsg
	withTimeoutA3(t, "take after close", func() { frames = q.take() })
	if frames != nil {
		t.Fatalf("take after close returned %d frames, want nil", len(frames))
	}
	if n := q.len(); n != 0 {
		t.Fatalf("len after close = %d, want 0", n)
	}
}

func TestMutA3_OutQueueMutexRelease(t *testing.T) {
	q := newOutQueue(2)
	q.push(wsMsg{data: []byte("x")}, nil)
	if !q.mu.TryLock() {
		t.Fatal("push() did not release q.mu")
	}
	q.mu.Unlock()

	q.take()
	if !q.mu.TryLock() {
		t.Fatal("take() did not release q.mu")
	}
	q.mu.Unlock()

	q.isPristine()
	if !q.mu.TryLock() {
		t.Fatal("isPristine() did not release q.mu")
	}
	q.mu.Unlock()

	q.len()
	if !q.mu.TryLock() {
		t.Fatal("len() did not release q.mu")
	}
	q.mu.Unlock()

	q.close()
	if !q.mu.TryLock() {
		t.Fatal("close() did not release q.mu")
	}
	q.mu.Unlock()
}

func TestMutA3_OutQueueWarnsOncePerBlockingEpisode(t *testing.T) {
	load, restore := swapWarnHandler(t)
	defer restore()

	q := newOutQueue(1)
	q.push(wsMsg{data: []byte("f1")}, nil)
	pushed := make(chan struct{})
	go func() {
		defer close(pushed)
		q.push(wsMsg{data: []byte("f2")}, nil)
	}()
	if !waitFor(2*time.Second, func() bool { return load() == 1 }) {
		t.Fatalf("expected exactly 1 warn while blocked, got %d", load())
	}
	// Spurious room wakeup while the queue is still full: the pusher must
	// re-check, re-block, and NOT warn again in the same episode.
	q.mu.Lock()
	q.signalLocked(q.room)
	q.mu.Unlock()
	if waitFor(500*time.Millisecond, func() bool { return load() >= 2 }) {
		t.Fatalf("warn fired more than once in one blocking episode (count=%d)", load())
	}
	q.close()
	<-pushed
}

// ---------------------------------------------------------------------------
// New() constructor (lines 633-651)
// ---------------------------------------------------------------------------

func TestMutA3_NewConstructorFields(t *testing.T) {
	mgr := engine.NewEngineManager()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(h)
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Display", Engine: eng})

	providers := map[string]llm.Provider{"zhipu": &stubLLMProvider{name: "zhipu"}}
	cfg := &config.Provider{Name: "zhipu"}
	cfg.Models.Set("glm-5.2", config.ModelConfig{})
	configs := map[string]*config.Provider{"zhipu": cfg}

	c := New(mgr, providers, configs)
	t.Cleanup(c.Stop)

	if c.mgr != mgr {
		t.Fatal("New did not bind the EngineManager")
	}
	if c.ActiveID() != "main" {
		t.Fatalf("ActiveID = %q, want main", c.ActiveID())
	}
	if got := c.engineNameForSlot(c.activeSlot()); got != "Display" {
		t.Fatalf("engineNameForSlot = %q, want Display (from manager)", got)
	}
	if len(c.providers) != 1 || c.providers["zhipu"] == nil {
		t.Fatalf("providers = %#v, want the zhipu stub", c.providers)
	}
	if len(c.providerConfigs) != 1 {
		t.Fatalf("providerConfigs = %#v, want the zhipu config", c.providerConfigs)
	}
	if c.thumbs == nil {
		t.Fatal("New did not install a thumbnail cache")
	}
	if _, ok := c.thumbs.get("k"); ok {
		t.Fatal("fresh thumb cache must miss")
	}

	// pendingAsks map must be initialized: dispatching an ask stores it.
	// hub.Dispatch recovers panics, so a nil map strands pendingMu — poll the
	// lock instead of blocking on it.
	h.Dispatch(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{ToolName: "Bash", Message: "allow?"}})
	stored := false
	if !waitFor(2*time.Second, func() bool {
		if !c.pendingMu.TryLock() {
			return false
		}
		defer c.pendingMu.Unlock()
		stored = len(c.pendingAsks) == 1
		return true
	}) {
		t.Fatal("pendingMu never released after ask dispatch (nil pendingAsks map panicked under recover?)")
	}
	if !stored {
		t.Fatalf("pendingAsks after ask = 0, want 1")
	}
}

// ---------------------------------------------------------------------------
// wsWriter (lines 667-713)
// ---------------------------------------------------------------------------

// TestMutA3_WsWriterEraCheckDropsStaleBatch pins the per-frame era check: a
// batch taken from the old queue must stop writing once the era flips, even
// mid-batch. The writer is stalled on a huge frame (client never reads), the
// era is flipped underneath it, then the client drains: only the in-flight
// big frame may arrive — the queued-behind marker must be dropped.
func TestMutA3_WsWriterEraCheckDropsStaleBatch(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)

	connCh := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- ws
		<-r.Context().Done() // hold the conn open without ever reading
	}))
	t.Cleanup(srv.Close)
	wsClient := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/")
	serverConn := <-connCh
	t.Cleanup(func() { _ = serverConn.Close() })

	c.activeWS.Store(serverConn)
	big := bytes.Repeat([]byte{0xAB}, 48<<20)
	q1 := c.outQ.Load()
	q1.push(wsMsg{data: big, isBinary: true}, c.done)
	q1.push(wsMsg{data: []byte(`{"type":"stale-marker"}`)}, c.done)
	go c.wsWriter()

	// Let the writer take the batch and stall on the 48MB write.
	time.Sleep(300 * time.Millisecond) // REAL-TIME

	// Era flip (the queue half of a takeover).
	q2 := newOutQueue(outQueueCapacity)
	c.outQ.Store(q2)
	q1.close()

	_ = wsClient.SetReadDeadline(time.Now().Add(30 * time.Second)) // REAL-TIME
	_, data, err := wsClient.ReadMessage()
	if err != nil {
		t.Fatalf("read big frame: %v", err)
	}
	if len(data) != len(big) {
		t.Fatalf("big frame truncated: %d bytes, want %d", len(data), len(big))
	}
	if _, ok := readWSFrameDeadlineA3(t, wsClient, 500*time.Millisecond); ok {
		t.Fatal("stale-era frame was delivered after the era flip (per-frame era check broken)")
	}

	close(c.done)
}

// TestMutA3_WsWriterWriteFailureWarns pins the warn on WS write failure: the
// writer must log when WriteMessage errors (closed conn installed as the
// active WS).
func TestMutA3_WsWriterWriteFailureWarns(t *testing.T) {
	load, restore := swapWarnHandler(t)
	defer restore()

	c, _ := bareConnectorA3(t, nil)
	connCh := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- ws
		_ = ws.Close() // close immediately: writes must fail
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	_ = dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/")
	serverConn := <-connCh

	c.activeWS.Store(serverConn)
	go c.wsWriter()
	c.sendWS([]byte(`{"a":1}`))
	if !waitFor(3*time.Second, func() bool { return load() >= 1 }) {
		t.Fatal("write failure was not logged")
	}
	close(c.done)
}

// ---------------------------------------------------------------------------
// activeSlot / registerEngine (lines 741-835)
// ---------------------------------------------------------------------------

func TestMutA3_ActiveSlotMutexRelease(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	if c.activeSlot() == nil {
		t.Fatal("activeSlot returned nil for main")
	}
	if !c.slotsMu.TryLock() {
		t.Fatal("activeSlot() did not release slotsMu")
	}
	c.slotsMu.Unlock()
}

func TestMutA3_RegisterEngineNilEnginePanics(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	panicked, msg := false, ""
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				msg = fmt.Sprint(r)
			}
		}()
		c.registerEngine(&engine.EngineViewState{ID: "x"})
	}()
	if !panicked {
		t.Fatal("registerEngine with nil Engine must panic")
	}
	// The explicit guard must fire BEFORE the nil Engine deref: a nil-deref
	// panic would mean the guard was skipped.
	if !strings.Contains(msg, "registerEngine called with nil Engine") {
		t.Fatalf("panic message = %q, want the explicit nil-Engine guard panic", msg)
	}
}

func TestMutA3_RegisterEngineTwiceIgnored(t *testing.T) {
	mgr := engine.NewEngineManager()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "e1"})
	eng.SetDispatcher(h)
	vs := &engine.EngineViewState{ID: "e1", Name: "e1", Engine: eng}
	mgr.Add(vs)

	rec := swapMsgRecorderA3(t)
	c := New(mgr, nil, nil)
	// Stop runs through withTimeoutA3 at the end (not t.Cleanup): a mutant
	// that strands slotsMu must fail the assertions, not hang the cleanup.
	n0 := -1
	withTimeoutA3(t, "streamStateCount", func() { n0 = streamStateCount(c, "e1") })

	c.registerEngine(vs)
	if got := rec.count("wui: registerEngine called twice for same engine, ignoring"); got != 1 {
		t.Fatalf("double-register warn count = %d, want 1", got)
	}
	n1 := -1
	withTimeoutA3(t, "streamStateCount", func() { n1 = streamStateCount(c, "e1") })
	h.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"}})
	n2 := -1
	withTimeoutA3(t, "streamStateCount", func() { n2 = streamStateCount(c, "e1") })
	if n0 < 0 || n2-n1 != 1 {
		t.Fatalf("tool blocks after one dispatch = %d, want 1 (double subscription)", n2-n1)
	}
	withTimeoutA3(t, "Stop", c.Stop)
}

func TestMutA3_RegisterEngineNonHubDispatcherSkipped(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	eng := engine.New(&engine.Params{EngineID: "e2"})
	eng.SetDispatcher(nonHubDispatcherA3{})

	rec := swapMsgRecorderA3(t)
	c.registerEngine(&engine.EngineViewState{ID: "e2", Name: "e2", Engine: eng})
	if got := rec.count("wui: engine dispatcher is not *hub.Hub, skipping"); got != 1 {
		t.Fatalf("skip warn count = %d, want 1", got)
	}
	c.slotsMu.RLock()
	_, exists := c.slots["e2"]
	c.slotsMu.RUnlock()
	if exists {
		t.Fatal("slot must not be created for a non-Hub dispatcher")
	}
}

// ---------------------------------------------------------------------------
// BusyReport / activeSessionTitle / inFlightPromptExcerpt / truncateRunes
// (lines 893-997)
// ---------------------------------------------------------------------------

func busyMockA3(id string, busy bool, jobs []*job.JobInfo) *mockEngine {
	return &mockEngine{
		engineIDFn: func() string { return id },
		isBusyFn:   func() bool { return busy },
		jobsFn:     func() []*job.JobInfo { return jobs },
		listSessionsFn: func(int) ([]*short.Session, error) {
			return []*short.Session{{SessionID: "test-session", Title: "Sess-" + id}}, nil
		},
		messagesFn: func() []types.Message {
			return []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("prompt-" + id)}}}
		},
		queryStartMsgIdxFn: func() int { return 0 },
	}
}

func TestMutA3_BusyReportQueryItems(t *testing.T) {
	c := &WUIConnector{pendingAsks: map[string]*types.AskEvent{}}
	c.slots = map[string]*engineSlot{
		"sys":  {engineID: "sys", engine: busyMockA3("sys", true, nil), name: "sys-name", system: true, taskToolIDs: map[string]bool{}},
		"work": {engineID: "work", engine: busyMockA3("work", true, nil), name: "work-name", taskToolIDs: map[string]bool{}},
	}
	// Map iteration order is random: assert on EVERY iteration so an early
	// break at the system slot cannot hide behind a lucky ordering.
	for i := 0; i < 50; i++ {
		rep := c.BusyReport()
		if !rep.Busy || len(rep.Items) != 1 {
			t.Fatalf("iter %d: busy=%v items=%d, want busy with exactly 1 item", i, rep.Busy, len(rep.Items))
		}
		it := rep.Items[0]
		if it.Kind != "query" || it.Engine != "work-name" || it.EngineID != "work" ||
			it.Session != "Sess-work" || it.Detail != "prompt-work" {
			t.Fatalf("iter %d: item = %+v", i, it)
		}
	}
}

func TestMutA3_BusyReportJobItems(t *testing.T) {
	c := &WUIConnector{pendingAsks: map[string]*types.AskEvent{}}
	c.slots = map[string]*engineSlot{
		"jobs": {engineID: "jobs", engine: busyMockA3("jobs", false, []*job.JobInfo{
			nil,
			{ID: "j-done", Status: "completed", Command: "old-cmd"},
			{ID: "j-run", Status: "running", Command: "cmd-x"},
			{ID: "j-nocmd", Status: "running", Description: "desc-y"},
		}), name: "jobs-name", taskToolIDs: map[string]bool{}},
	}
	rep := c.BusyReport()
	if len(rep.Items) != 2 {
		t.Fatalf("items = %d, want 2 (nil and completed jobs skipped)", len(rep.Items))
	}
	if it := rep.Items[0]; it.Kind != "job" || it.EngineID != "jobs" || it.Detail != "cmd-x" {
		t.Fatalf("first job item = %+v", it)
	}
	if it := rep.Items[1]; it.Detail != "desc-y" {
		t.Fatalf("empty Command must fall back to Description, got %+v", it)
	}
}

func TestMutA3_ActiveSessionTitle(t *testing.T) {
	var limits []int
	m := &mockEngine{}
	m.sessionIDFn = func() string { return "" }
	m.listSessionsFn = func(limit int) ([]*short.Session, error) {
		limits = append(limits, limit)
		return []*short.Session{{SessionID: "s0", Title: "T0"}, {SessionID: "s1", Title: "T1"}}, nil
	}
	if got := activeSessionTitle(m); got != "" {
		t.Fatalf("empty session id: title = %q, want empty", got)
	}
	if len(limits) != 0 {
		t.Fatalf("ListSessions called %d times for empty session id, want 0", len(limits))
	}

	m.sessionIDFn = func() string { return "s1" }
	if got := activeSessionTitle(m); got != "T1" {
		t.Fatalf("title = %q, want T1 (exact session match)", got)
	}
	if len(limits) != 1 || limits[0] != 50 {
		t.Fatalf("ListSessions limits = %v, want [50]", limits)
	}

	m.listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{nil, {SessionID: "s1", Title: "T1"}}, nil
	}
	if got := activeSessionTitle(m); got != "T1" {
		t.Fatalf("title with nil session entry = %q, want T1", got)
	}

	m.listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{{SessionID: "s1", Title: "T1"}}, errors.New("store down")
	}
	if got := activeSessionTitle(m); got != "" {
		t.Fatalf("title on ListSessions error = %q, want empty", got)
	}
}

func TestMutA3_InFlightPromptExcerpt(t *testing.T) {
	mk := func(start int, msgs ...types.Message) *mockEngine {
		return &mockEngine{
			messagesFn:         func() []types.Message { return msgs },
			queryStartMsgIdxFn: func() int { return start },
		}
	}
	// Negative start clamps to 0 (not 1): the first message is still scanned.
	m := mk(-1,
		types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}},
		types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a")}},
	)
	if got := inFlightPromptExcerpt(m); got != "first" {
		t.Fatalf("excerpt = %q, want first", got)
	}
	// Start beyond the message count clamps instead of slicing out of range.
	m = mk(5, types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("a")}})
	if got := inFlightPromptExcerpt(m); got != "" {
		t.Fatalf("excerpt with start past end = %q, want empty", got)
	}
	// Assistant messages and text-less user messages are skipped; a later
	// user prompt is still found.
	m = mk(0,
		types.Message{Role: types.RoleUser, Content: nil},
		types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("as")}},
		types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("real")}},
	)
	if got := inFlightPromptExcerpt(m); got != "real" {
		t.Fatalf("excerpt = %q, want real", got)
	}
}

func TestMutA3_TruncateRunesBoundary(t *testing.T) {
	s80 := strings.Repeat("a", 80)
	if got := truncateRunes(s80, 80); got != s80 {
		t.Fatalf("exact-length string was modified: %q", got)
	}
	s81 := strings.Repeat("b", 81)
	if got, want := truncateRunes(s81, 80), strings.Repeat("b", 80)+"…"; got != want {
		t.Fatalf("truncate(81) = %q, want %q", got, want)
	}
	cjk := strings.Repeat("中", 81)
	if got, want := truncateRunes(cjk, 80), strings.Repeat("中", 80)+"…"; got != want {
		t.Fatalf("truncate(cjk 81) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// buildQuota / formatQuota (lines 2590-2643)
// ---------------------------------------------------------------------------

func TestMutA3_FormatQuota(t *testing.T) {
	now := time.Now() // REAL-TIME
	cases := []struct {
		name string
		info *quota.Info
		want string
	}{
		{"nil", nil, ""},
		{"25h", &quota.Info{Used: 40, ResetAt: now.Add(25 * time.Hour)}, "60%/1d"},
		// 24h minus scheduler drift lands at hours==23: still the h/m branch
		// (the day threshold is >= 24).
		{"24h", &quota.Info{Used: 40, ResetAt: now.Add(24 * time.Hour)}, "60%/23h59m"},
		{"37h", &quota.Info{Used: 40, ResetAt: now.Add(37 * time.Hour)}, "60%/1d"},
		{"49h", &quota.Info{Used: 40, ResetAt: now.Add(49 * time.Hour)}, "60%/2d"},
		{"47h", &quota.Info{Used: 40, ResetAt: now.Add(47 * time.Hour)}, "60%/1d"},
		{"2h17m", &quota.Info{Used: 40, ResetAt: now.Add(2*time.Hour + 17*time.Minute + 30*time.Second)}, "60%/2h17m"},
		{"1h30m", &quota.Info{Used: 40, ResetAt: now.Add(1*time.Hour + 30*time.Minute + 30*time.Second)}, "60%/1h30m"},
		{"17m", &quota.Info{Used: 40, ResetAt: now.Add(17*time.Minute + 30*time.Second)}, "60%/17m"},
		{"1m", &quota.Info{Used: 40, ResetAt: now.Add(1*time.Minute + 30*time.Second)}, "60%/1m"},
		{"59s", &quota.Info{Used: 40, ResetAt: now.Add(59 * time.Second)}, "60%/0m"},
		{"past", &quota.Info{Used: 40, ResetAt: now.Add(-time.Hour)}, "60%/0m"},
	}
	for _, tc := range cases {
		if got := formatQuota(tc.info); got != tc.want {
			t.Errorf("%s: formatQuota = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMutA3_BuildQuotaSendsResult(t *testing.T) {
	endAt := time.Now().Add(49 * time.Hour).UnixMilli() // REAL-TIME
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/token_plan/remains", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","end_time":%d,"current_interval_remaining_percent":40}]}`, endAt)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	configs := map[string]*config.Provider{
		"minimax-local": {Name: "minimax-local", URL: srv.URL, Keys: []string{"k"}},
		"plain":         {Name: "plain", URL: srv.URL, Keys: []string{"k"}},
	}
	c := newTestConnectorWithConfig(t, hub.NewHub(), nil, configs)
	ws := dialAndStore(t, c)

	// Map iteration order is random: repeat so order-dependent mutants
	// (continue→break skipping the detectable provider) cannot hide.
	for i := 0; i < 24; i++ {
		c.buildQuota()
		data, ok := readWSFrameDeadlineA3(t, ws, 2*time.Second)
		if !ok {
			t.Fatalf("iter %d: no quota_result frame (entries lost or fetch skipped)", i)
		}
		var msg struct {
			Type    string `json:"type"`
			Entries []struct {
				Provider string `json:"provider"`
				Quota    string `json:"quota"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("iter %d: unmarshal: %v (frame %s)", i, err, data)
		}
		if msg.Type != "quota_result" {
			t.Fatalf("iter %d: frame type = %q, want quota_result", i, msg.Type)
		}
		if len(msg.Entries) != 1 {
			t.Fatalf("iter %d: entries = %d, want 1 (only the minimax provider)", i, len(msg.Entries))
		}
		if msg.Entries[0].Provider != "minimax-local" || msg.Entries[0].Quota != "40%/2d" {
			t.Fatalf("iter %d: entry = %+v, want {minimax-local 40%%/2d}", i, msg.Entries[0])
		}
	}
}

func TestMutA3_BuildQuotaFetchErrorSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	// All providers failing: nothing is sent at all.
	configs := map[string]*config.Provider{
		"minimax-err": {Name: "minimax-err", URL: srv.URL, Keys: []string{"k"}},
	}
	c := newTestConnectorWithConfig(t, hub.NewHub(), nil, configs)
	ws := dialAndStore(t, c)
	c.buildQuota()
	if data, ok := readWSFrameDeadlineA3(t, ws, 500*time.Millisecond); ok {
		t.Fatalf("fetch error must not emit a frame, got %s", data)
	}
}

func TestMutA3_BuildQuotaMixedProviders(t *testing.T) {
	endAt := time.Now().Add(49 * time.Hour).UnixMilli() // REAL-TIME
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","end_time":%d,"current_interval_remaining_percent":40}]}`, endAt)
	}))
	t.Cleanup(good.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)

	configs := map[string]*config.Provider{
		"minimax-ok":  {Name: "minimax-ok", URL: good.URL, Keys: []string{"k"}},
		"minimax-bad": {Name: "minimax-bad", URL: bad.URL, Keys: []string{"k"}},
	}
	c := newTestConnectorWithConfig(t, hub.NewHub(), nil, configs)
	ws := dialAndStore(t, c)
	// Map iteration order is random: a continue→break at the failing
	// provider would skip the good one whenever "minimax-bad" iterates first.
	for i := 0; i < 24; i++ {
		c.buildQuota()
		data, ok := readWSFrameDeadlineA3(t, ws, 2*time.Second)
		if !ok {
			t.Fatalf("iter %d: no quota_result frame (good provider lost)", i)
		}
		var msg struct {
			Type    string `json:"type"`
			Entries []struct {
				Provider string `json:"provider"`
				Quota    string `json:"quota"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("iter %d: unmarshal: %v", i, err)
		}
		if len(msg.Entries) != 1 || msg.Entries[0].Provider != "minimax-ok" || msg.Entries[0].Quota != "40%/2d" {
			t.Fatalf("iter %d: entries = %+v, want exactly [{mini-ok 40%%/2d}]", i, msg.Entries)
		}
	}
}

// ---------------------------------------------------------------------------
// buildConnectStatus / buildStats / engineNameForSlot / buildSessionList
// (lines 2654-2780)
// ---------------------------------------------------------------------------

func TestMutA3_SendMetadataComposite(t *testing.T) {
	dir := t.TempDir()
	m := &mockEngine{projectDir: dir}
	msgs := make([]types.Message, 31)
	for i := range msgs {
		msgs[i] = types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("m%d", i))}}
	}
	m.messagesFn = func() []types.Message { return msgs }
	histDir := filepath.Join(dir, "history")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Third line is a consecutive duplicate and must be skipped on read.
	if err := os.WriteFile(filepath.Join(histDir, "main.jsonl"),
		[]byte("{\"display\":\"alpha\"}\n{\"display\":\"beta\"}\n{\"display\":\"beta\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, _ := bareConnectorA3(t, m)
	// A named manager makes engineID ("main") and engineName ("Mgr-Name")
	// independently observable in the connect sub-frame.
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Mgr-Name", Model: "m-x"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c.mgr = mgr
	slot := c.activeSlotTest(t)
	c.sendMetadata(slot)

	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("sendMetadata produced %d frames, want 1", len(frames))
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &head); err != nil || head.Type != "metadata" {
		t.Fatalf("frame head = %s (%v), want metadata", frames[0], err)
	}
	var connect struct {
		InputHistory []string `json:"inputHistory"`
		WuiHash      string   `json:"wuiHash"`
		EngineID     string   `json:"engineID"`
		EngineName   string   `json:"engineName"`
	}
	frameJSONFieldA3(t, frames[0], "connect", &connect)
	if len(connect.InputHistory) != 2 || connect.InputHistory[0] != "alpha" || connect.InputHistory[1] != "beta" {
		t.Fatalf("inputHistory = %v, want [alpha beta]", connect.InputHistory)
	}
	if connect.WuiHash == "" || connect.WuiHash != wuiAssetHash() {
		t.Fatalf("wuiHash = %q, want the live asset hash", connect.WuiHash)
	}
	if connect.EngineID != "main" {
		t.Fatalf("engineID = %q, want main", connect.EngineID)
	}
	if connect.EngineName != "Mgr-Name" {
		t.Fatalf("engineName = %q, want Mgr-Name", connect.EngineName)
	}
	var engines struct {
		Type    string `json:"type"`
		Engines []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"engines"`
		ActiveID string `json:"activeID"`
	}
	frameJSONFieldA3(t, frames[0], "engines", &engines)
	if engines.Type != "engine_list" {
		t.Fatalf("engines sub-frame type = %q, want engine_list", engines.Type)
	}
	if len(engines.Engines) != 1 {
		t.Fatalf("engines = %d entries, want 1", len(engines.Engines))
	}
	if e := engines.Engines[0]; e.ID != "main" || e.Name != "Mgr-Name" || e.Model != "m-x" {
		t.Fatalf("engine entry = %+v, want {main Mgr-Name m-x}", e)
	}
	if engines.ActiveID != "main" {
		t.Fatalf("engines activeID = %q, want main", engines.ActiveID)
	}
	var cfgHead struct {
		Type string `json:"type"`
	}
	frameJSONFieldA3(t, frames[0], "config", &cfgHead)
	if cfgHead.Type != "config" {
		t.Fatalf("config sub-frame type = %q, want config", cfgHead.Type)
	}
	var history struct {
		Messages   []struct{ Text string } `json:"messages"`
		NextCursor string                  `json:"nextCursor"`
		HasMore    bool                    `json:"hasMore"`
	}
	frameJSONFieldA3(t, frames[0], "history", &history)
	if len(history.Messages) != 30 {
		t.Fatalf("history page = %d messages, want 30", len(history.Messages))
	}
	if history.Messages[0].Text != "m1" {
		t.Fatalf("first page message = %q, want m1 (oldest trimmed)", history.Messages[0].Text)
	}
	if history.NextCursor != "30" || !history.HasMore {
		t.Fatalf("nextCursor=%q hasMore=%v, want 30/true", history.NextCursor, history.HasMore)
	}
	if !slot.active.Load() {
		t.Fatal("sendMetadata must activate the slot")
	}
}

func TestMutA3_BuildStats(t *testing.T) {
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("context prompt tokens")}}}
	m := &mockEngine{projectDir: t.TempDir()}
	m.messagesFn = func() []types.Message { return msgs }
	c, _ := bareConnectorA3(t, m)
	slot := c.activeSlotTest(t)

	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 11, OutputTokens: 22, CacheReadInputTokens: 33, CacheCreationInputTokens: 44}})
	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t", Name: "Bash"}})
	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 1500 * time.Millisecond}})
	accumulateStats(&slot.queryStats, hub.Event{Type: types.EventQueryStart})

	var s struct {
		Type         string      `json:"type"`
		Usage        types.Usage `json:"usage"`
		QueryStartMs int64       `json:"queryStartMs"`
		ToolCount    int         `json:"toolCount"`
		ThinkingMs   int64       `json:"thinkingMs"`
		ContextUsed  int         `json:"contextUsed"`
		ContextTotal int         `json:"contextTotal"`
	}
	if err := json.Unmarshal(c.buildStats(slot), &s); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	if s.Type != "stats" {
		t.Fatalf("type = %q, want stats", s.Type)
	}
	if s.Usage.InputTokens != 11 || s.Usage.OutputTokens != 22 || s.Usage.CacheReadInputTokens != 33 || s.Usage.CacheCreationInputTokens != 44 {
		t.Fatalf("usage = %+v, want 11/22/33/44", s.Usage)
	}
	if s.ToolCount != 1 {
		t.Fatalf("toolCount = %d, want 1", s.ToolCount)
	}
	if s.ThinkingMs != 1500 {
		t.Fatalf("thinkingMs = %d, want 1500", s.ThinkingMs)
	}
	if s.QueryStartMs <= 0 {
		t.Fatalf("queryStartMs = %d, want > 0", s.QueryStartMs)
	}
	wantCtx := engine.TokenCountWithEstimation(msgs)
	if wantCtx == 0 {
		t.Fatal("test setup: estimation of the fixture messages must be non-zero")
	}
	if s.ContextUsed != wantCtx {
		t.Fatalf("contextUsed = %d, want %d (estimation fallback)", s.ContextUsed, wantCtx)
	}
	if s.ContextTotal != 200000 {
		t.Fatalf("contextTotal = %d, want 200000 (mock engine window)", s.ContextTotal)
	}
}

func TestMutA3_EngineNameForSlot(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Mgr-Name"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, &mockEngine{projectDir: t.TempDir()})
	slot := c.activeSlotTest(t)

	c.mgr = mgr
	if got := c.engineNameForSlot(slot); got != "Mgr-Name" {
		t.Fatalf("engineNameForSlot = %q, want Mgr-Name (from manager)", got)
	}
	empty := engine.NewEngineManager()
	c.mgr = empty
	if got := c.engineNameForSlot(slot); got != "main" {
		t.Fatalf("engineNameForSlot fallback = %q, want main (engine id)", got)
	}
}

func TestMutA3_BuildEngineList(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main-Name", Model: "m-1"})
	mgr.Add(&engine.EngineViewState{ID: "aux", Name: "Aux-Name", Model: "m-2"})
	if err := mgr.SetActive("aux"); err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, &mockEngine{projectDir: t.TempDir()})
	c.mgr = mgr

	payload := c.buildEngineList()
	if payload == nil {
		t.Fatal("buildEngineList returned nil")
	}
	var msg struct {
		Type    string `json:"type"`
		Engines []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"engines"`
		ActiveID string `json:"activeID"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "engine_list" {
		t.Fatalf("type = %q, want engine_list", msg.Type)
	}
	if len(msg.Engines) != 2 {
		t.Fatalf("engines = %d, want 2 (from manager snapshot)", len(msg.Engines))
	}
	byID := map[string]struct{ Name, Model string }{}
	for _, e := range msg.Engines {
		byID[e.ID] = struct{ Name, Model string }{e.Name, e.Model}
	}
	if got := byID["main"]; got.Name != "Main-Name" || got.Model != "m-1" {
		t.Fatalf("main entry = %+v", got)
	}
	if got := byID["aux"]; got.Name != "Aux-Name" || got.Model != "m-2" {
		t.Fatalf("aux entry = %+v", got)
	}
	if msg.ActiveID != "aux" {
		t.Fatalf("activeID = %q, want aux (from manager snapshot)", msg.ActiveID)
	}
}

// ---------------------------------------------------------------------------
// handleSessionSwitch / handleSessionNew (lines 2790-2875)
// ---------------------------------------------------------------------------

// Equivalent mutants, on record: the slot==nil guards at connector.go:2816
// and 2859 here, and 2969 in handleThinkingSwitch below, are unreachable
// under serial dispatch — activeEngine() already returned non-nil earlier in
// the same call, and slots are never deleted, so the second activeSlot()
// read can only miss inside the RegisterEngine concurrency window (active ID
// swapped between the two reads). A slot lost in that window nil-derefs on
// the next line with or without the guard, so no serial test can observe
// the branch.

func TestMutA3_HandleSessionSwitchPersist(t *testing.T) {
	dir := t.TempDir()
	m := &mockEngine{projectDir: dir}
	c, _ := bareConnectorA3(t, m)
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c.mgr = mgr

	rec := swapMsgRecorderA3(t)
	c.handleSessionSwitch("sess-2")
	if got, want := rec.count("wui:session switch:PersistMeta failed"), 0; got != want {
		t.Fatalf("warn on successful persist = %d, want %d", got, want)
	}
	if vs := mgr.Active(); vs == nil || vs.ActiveSessionID != "sess-2" {
		t.Fatalf("Active().ActiveSessionID = %+v, want sess-2", vs)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatalf("meta.json not persisted: %v", err)
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.projectDirFn = func() string { return blocked }
	c.handleSessionSwitch("sess-3")
	if got := rec.count("wui:session switch:PersistMeta failed"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
	if len(m.switchSessionCalls) != 2 {
		t.Fatalf("switchSessionCalls = %v, want two switches", m.switchSessionCalls)
	}
}

func TestMutA3_HandleSessionNew(t *testing.T) {
	// No active engine: quiet return, not a nil-deref panic.
	cNoEng, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	cNoEng.active.Store(&ghost)
	cNoEng.handleSessionNew()
	if frames := takeAllFramesA3(cNoEng); len(frames) != 0 {
		t.Fatalf("frames = %d, want 0 (no engine)", len(frames))
	}

	// Success: session_list goes out even without a WS connection.
	m := &mockEngine{projectDir: t.TempDir()}
	m.newSessionFn = func() (string, error) { return "ns-1", nil }
	m.listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{{SessionID: "ns-1", Title: "New"}}, nil
	}
	c, _ := bareConnectorA3(t, m)
	c.handleSessionNew()
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (session_list)", len(frames))
	}
	var msg struct {
		Type     string                `json:"type"`
		Sessions []struct{ ID string } `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "session_list" || len(msg.Sessions) != 1 || msg.Sessions[0].ID != "ns-1" {
		t.Fatalf("frame = %s, want session_list with ns-1", frames[0])
	}

	// Persist failure warns exactly once; success stays silent.
	dir := t.TempDir()
	mOK := &mockEngine{projectDir: dir}
	mOK.newSessionFn = func() (string, error) { return "ns-2", nil }
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	cOK, _ := bareConnectorA3(t, mOK)
	cOK.mgr = mgr
	rec := swapMsgRecorderA3(t)
	cOK.handleSessionNew()
	if got := rec.count("wui:session new:PersistMeta failed"); got != 0 {
		t.Fatalf("warn on successful persist = %d, want 0", got)
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mOK.projectDirFn = func() string { return blocked }
	cOK.handleSessionNew()
	if got := rec.count("wui:session new:PersistMeta failed"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// handleModelSwitch (lines 2880-2923)
// ---------------------------------------------------------------------------

func modelSwitchFixtureA3() (map[string]llm.Provider, map[string]*config.Provider) {
	cfg := &config.Provider{Name: "zhipu"}
	cfg.Models.Set("glm-5.2", config.ModelConfig{
		Context:   config.IntOrHuman(111111),
		MaxTokens: config.IntOrHuman(2222),
		Input:     []string{"text", "image"},
	})
	return map[string]llm.Provider{"zhipu": &stubLLMProvider{name: "zhipu"}},
		map[string]*config.Provider{"zhipu": cfg}
}

func TestMutA3_HandleModelSwitch(t *testing.T) {
	providers, configs := modelSwitchFixtureA3()

	// Provider missing from configs: error frame, engine untouched.
	m := &mockEngine{projectDir: t.TempDir()}
	c, _ := bareConnectorA3(t, m)
	c.providers = providers
	c.providerConfigs = map[string]*config.Provider{}
	c.handleModelSwitch("zhipu", "glm-5.2")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (error)", len(frames))
	}
	var errMsg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &errMsg); err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errMsg.Type != "error" || !strings.Contains(errMsg.Message, "no config for provider zhipu") {
		t.Fatalf("error frame = %+v, want no-config error", errMsg)
	}
	if len(m.setModelCalls) != 0 {
		t.Fatalf("model applied despite config error: %v", m.setModelCalls)
	}

	// No active engine: quiet return, not a nil-deref panic.
	cNoEng, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	cNoEng.active.Store(&ghost)
	cNoEng.providers = providers
	cNoEng.providerConfigs = configs
	cNoEng.handleModelSwitch("zhipu", "glm-5.2")

	// Success: engine sync + model_switched frame + info log.
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("context prompt")}}}
	m2 := &mockEngine{projectDir: t.TempDir()}
	m2.messagesFn = func() []types.Message { return msgs }
	c2, _ := bareConnectorA3(t, m2)
	c2.providers = providers
	c2.providerConfigs = configs
	var autoCfg engine.AutoCompactConfig
	m2.updateAutoFn = func(cfg engine.AutoCompactConfig) { autoCfg = cfg }
	rec := swapMsgRecorderA3(t)
	c2.handleModelSwitch("zhipu", "glm-5.2")

	if got := m2.setProviderCalls; len(got) != 1 || got[0] != "zhipu" {
		t.Fatalf("setProviderCalls = %v, want [zhipu]", got)
	}
	if got := m2.setModelCalls; len(got) != 1 || got[0] != "glm-5.2" {
		t.Fatalf("setModelCalls = %v, want [glm-5.2]", got)
	}
	if got := m2.setMaxTokensCalls; len(got) != 1 || got[0] != 2222 {
		t.Fatalf("setMaxTokensCalls = %v, want [2222]", got)
	}
	if got := m2.setInputModCalls; len(got) != 1 || len(got[0]) != 2 || got[0][0] != "text" || got[0][1] != "image" {
		t.Fatalf("setInputModCalls = %v, want [[text image]]", got)
	}
	if autoCfg.ContextWindow != 111111 {
		t.Fatalf("AutoCompactConfig.ContextWindow = %d, want 111111", autoCfg.ContextWindow)
	}
	if autoCfg.MaxConsecutiveFailures != 3 {
		t.Fatalf("AutoCompactConfig.MaxConsecutiveFailures = %d, want 3", autoCfg.MaxConsecutiveFailures)
	}
	if got := rec.count("wui:model switched"); got != 1 {
		t.Fatalf("model-switched info count = %d, want 1", got)
	}
	frames2 := takeAllFramesA3(c2)
	if len(frames2) != 1 {
		t.Fatalf("frames = %d, want 1 (model_switched)", len(frames2))
	}
	var sw struct {
		Type         string `json:"type"`
		ContextUsed  int    `json:"contextUsed"`
		ContextTotal int    `json:"contextTotal"`
		Thinking     string `json:"thinking"`
	}
	if err := json.Unmarshal([]byte(frames2[0]), &sw); err != nil {
		t.Fatalf("unmarshal model_switched: %v", err)
	}
	wantCtx := engine.TokenCountWithEstimation(msgs)
	if wantCtx == 0 {
		t.Fatal("test setup: estimation must be non-zero")
	}
	if sw.Type != "model_switched" || sw.ContextUsed != wantCtx || sw.ContextTotal != 200000 || sw.Thinking != "auto" {
		t.Fatalf("model_switched = %+v, want used=%d total=200000 thinking=auto", sw, wantCtx)
	}

	// Persist failure warns exactly once; success stays silent.
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	mOK := &mockEngine{projectDir: dir}
	mOK.messagesFn = func() []types.Message { return msgs }
	cOK, _ := bareConnectorA3(t, mOK)
	cOK.providers, cOK.providerConfigs = providers, configs
	cOK.mgr = mgr
	rec2 := swapMsgRecorderA3(t)
	cOK.handleModelSwitch("zhipu", "glm-5.2")
	if got, want := rec2.count("wui: failed to persist model selection"), 0; got != want {
		t.Fatalf("warn on successful persist = %d, want %d", got, want)
	}
	if vs := mgr.Get("main"); vs == nil || vs.Model != "zhipu/glm-5.2" {
		t.Fatalf("manager model after switch = %+v, want zhipu/glm-5.2", vs)
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mOK.projectDirFn = func() string { return blocked }
	cOK.handleModelSwitch("zhipu", "glm-5.2")
	if got := rec2.count("wui: failed to persist model selection"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// handleThinkingSwitch (lines 2946-2974)
// ---------------------------------------------------------------------------

func TestMutA3_HandleThinkingSwitch(t *testing.T) {
	// No active engine: quiet return, not a nil-deref panic.
	cNoEng, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	cNoEng.active.Store(&ghost)
	cNoEng.handleThinkingSwitch("high")

	// Success: effort recorded + info logged.
	m := &mockEngine{projectDir: t.TempDir()}
	c, _ := bareConnectorA3(t, m)
	rec := swapMsgRecorderA3(t)
	c.handleThinkingSwitch("high")
	if got := m.setThinkingCalls; len(got) != 1 || got[0] != llm.EffortHigh {
		t.Fatalf("setThinkingCalls = %v, want [high]", got)
	}
	if got := rec.count("wui:thinking switched"); got != 1 {
		t.Fatalf("thinking-switched info count = %d, want 1", got)
	}

	// Engine rejects the effort: error frame out, no info log.
	m2 := &mockEngine{projectDir: t.TempDir()}
	m2.setThinkingFn = func(llm.Effort) error { return errors.New("unsupported effort") }
	c2, _ := bareConnectorA3(t, m2)
	rec2 := swapMsgRecorderA3(t)
	c2.handleThinkingSwitch("high")
	if got := rec2.count("wui:thinking switched"); got != 0 {
		t.Fatalf("info logged despite SetThinking error (count=%d)", got)
	}
	frames := takeAllFramesA3(c2)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (error)", len(frames))
	}
	var errMsg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &errMsg); err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errMsg.Type != "error" || !strings.Contains(errMsg.Message, "unsupported effort") {
		t.Fatalf("error frame = %+v, want the SetThinking error", errMsg)
	}

	// Persist failure warns exactly once; success stays silent.
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	mOK := &mockEngine{projectDir: dir}
	cOK, _ := bareConnectorA3(t, mOK)
	cOK.mgr = mgr
	rec3 := swapMsgRecorderA3(t)
	cOK.handleThinkingSwitch("high")
	if got := rec3.count("wui:thinking persist failed"); got != 0 {
		t.Fatalf("warn on successful persist = %d, want 0", got)
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mOK.projectDirFn = func() string { return blocked }
	cOK.handleThinkingSwitch("high")
	if got := rec3.count("wui:thinking persist failed"); got != 1 {
		t.Fatalf("warn on failed persist = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// rememberUpload / takeRewindStash (lines 3134-3294)
// ---------------------------------------------------------------------------

func TestMutA3_RememberUploadMutexRelease(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	c.rememberUpload("id1", savedAttachment{path: "p", mime: "m", kind: "image"})
	if !c.restoreMu.TryLock() {
		t.Fatal("rememberUpload() did not release restoreMu")
	}
	c.restoreMu.Unlock()
	saved := savedOfA3(t, c, "id1")
	if saved.path != "p" || saved.mime != "m" || saved.kind != "image" {
		t.Fatalf("saved = %+v, want {p m image}", saved)
	}
}

func TestMutA3_TakeRewindStash(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	if got := c.takeRewindStash("main"); got != nil {
		t.Fatalf("nil stash: got %#v, want nil (and no panic)", got)
	}

	setStash := func(engineID string, blocks []types.ContentBlock) {
		// TryLock: a mutant stranding restoreMu in an earlier take must fail
		// fast here instead of deadlocking the test.
		if !c.restoreMu.TryLock() {
			t.Fatal("restoreMu stranded by an earlier takeRewindStash call")
		}
		c.rewindStash = &rewindStash{engineID: engineID, blocks: blocks}
		c.restoreMu.Unlock()
	}
	setStash("other", []types.ContentBlock{types.NewTextBlock("x")})
	if got := c.takeRewindStash("main"); got != nil {
		t.Fatalf("foreign-engine stash: got %#v, want nil", got)
	}
	if c.rewindStash == nil {
		t.Fatal("foreign-engine stash must stay put")
	}

	// Non-nil empty stash: strictly nil result (an empty slice would make the
	// restore handler re-walk Messages and resurrect an older prompt).
	setStash("main", []types.ContentBlock{})
	if got := c.takeRewindStash("main"); got != nil {
		t.Fatalf("empty stash: got %#v, want nil", got)
	}

	setStash("main", []types.ContentBlock{types.NewTextBlock("y")})
	got := c.takeRewindStash("main")
	if len(got) != 1 || got[0].Text != "y" {
		t.Fatalf("stash take = %#v, want one y block", got)
	}
	if second := c.takeRewindStash("main"); second != nil {
		t.Fatalf("stash must be one-shot, second take = %#v", second)
	}
	if !c.restoreMu.TryLock() {
		t.Fatal("takeRewindStash() did not release restoreMu")
	}
	c.restoreMu.Unlock()
}

// ---------------------------------------------------------------------------
// buildRestoredMsg (lines 3174-3242)
// ---------------------------------------------------------------------------

func TestMutA3_BuildRestoredMsgTextAndDocs(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)

	msg := buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u1", Value: "v", Content: []types.ContentBlock{types.NewTextBlock("c")}})
	if msg.UUID != "u1" {
		t.Fatalf("UUID = %q, want u1", msg.UUID)
	}
	if msg.Text != "c" {
		t.Fatalf("Text = %q, want c (content text wins over Value)", msg.Text)
	}

	// Single-character content text still wins over Value.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u2", Value: "v", Content: []types.ContentBlock{types.NewTextBlock("x")}})
	if msg.Text != "x" {
		t.Fatalf("Text = %q, want x", msg.Text)
	}

	// No text blocks: falls back to Value.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u3", Value: "v"})
	if msg.Text != "v" {
		t.Fatalf("Text = %q, want v (Value fallback)", msg.Text)
	}

	// Document happy path: attachment metadata + connector-level id binding.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u4", Content: []types.ContentBlock{
		{Type: types.ContentTypeDocument, Name: "n.pdf", Path: "/tmp/n.pdf", Mime: "application/pdf", Size: 123},
		types.NewTextBlock("hello"),
	}})
	if msg.Text != "hello" {
		t.Fatalf("Text = %q, want hello", msg.Text)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if att.ID == "" || att.Name != "n.pdf" || att.Mime != "application/pdf" || att.Size != 123 {
		t.Fatalf("doc attachment = %+v", att)
	}
	if saved := savedOfA3(t, c, att.ID); saved.path != "/tmp/n.pdf" || saved.mime != "application/pdf" || saved.kind != "document" {
		t.Fatalf("saved doc = %+v", saved)
	}

	// Empty-path document is skipped; later text still collected.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{UUID: "u5", Value: "vv", Content: []types.ContentBlock{
		{Type: types.ContentTypeDocument, Path: ""},
		types.NewTextBlock("t"),
	}})
	if len(msg.Attachments) != 0 {
		t.Fatalf("empty-path doc produced %d attachments, want 0", len(msg.Attachments))
	}
	if msg.Text != "t" {
		t.Fatalf("Text = %q, want t", msg.Text)
	}

	// Nil-source image is skipped; the document after it is still processed.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		{Type: types.ContentTypeImage, Source: nil},
		docBlockA3("/d1.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (doc only)", len(msg.Attachments))
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/d1.pdf" {
		t.Fatalf("saved = %+v, want the doc path", saved)
	}

	// File-sourced image with empty path is skipped; doc still processed.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		{Type: types.ContentTypeImage, Source: &types.ImageSource{MediaType: "image/png"}},
		docBlockA3("/d2.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (empty-path image skipped)", len(msg.Attachments))
	}

	// File-sourced image happy path + continue keeps later blocks.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		fileImageBlockA3("/img/p.png", "image/png"),
		docBlockA3("/d3.pdf"),
	}})
	if len(msg.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(msg.Attachments))
	}
	if att := msg.Attachments[0]; att.ID == "" || att.Mime != "image/png" {
		t.Fatalf("file image attachment = %+v", att)
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/img/p.png" || saved.mime != "image/png" || saved.kind != "image" {
		t.Fatalf("saved file image = %+v", saved)
	}
	if saved := savedOfA3(t, c, msg.Attachments[1].ID); saved.path != "/d3.pdf" {
		t.Fatalf("saved second = %+v, want the doc path", saved)
	}
}

func TestMutA3_BuildRestoredMsgBase64Images(t *testing.T) {
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, nil)
	c.mediaCache = store

	// Extension derives from the media type's subtype.
	raw := []byte("PNGBYTES")
	msg := buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("video/mp4", base64.StdEncoding.EncodeToString(raw)),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if att.ID == "" || att.Mime != "video/mp4" || att.Size != len(raw) {
		t.Fatalf("b64 attachment = %+v", att)
	}
	saved := savedOfA3(t, c, att.ID)
	if !strings.HasSuffix(saved.path, ".mp4") {
		t.Fatalf("cached path = %q, want .mp4 suffix", saved.path)
	}
	if saved.mime != "video/mp4" || saved.kind != "image" {
		t.Fatalf("saved = %+v", saved)
	}

	// No-slash media type falls back to .png.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("noslash", base64.StdEncoding.EncodeToString(raw)),
	}})
	saved = savedOfA3(t, c, msg.Attachments[0].ID)
	if !strings.HasSuffix(saved.path, ".png") {
		t.Fatalf("cached path = %q, want .png default", saved.path)
	}

	// Slash at index 0 uses the whole rest as the extension.
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("/webm", base64.StdEncoding.EncodeToString(raw)),
	}})
	saved = savedOfA3(t, c, msg.Attachments[0].ID)
	if !strings.HasSuffix(saved.path, ".webm") {
		t.Fatalf("cached path = %q, want .webm suffix", saved.path)
	}

	// Invalid base64 is dropped without panic; later blocks still processed.
	rec := swapMsgRecorderA3(t)
	msg = buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("image/png", "!!!not-base64!!!"),
		docBlockA3("/d4.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (bad b64 dropped, doc kept)", len(msg.Attachments))
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/d4.pdf" {
		t.Fatalf("saved = %+v, want the doc path", saved)
	}
	if got := rec.count("wui:restore image decode failed, dropping attachment"); got != 1 {
		t.Fatalf("decode-failure warn = %d, want 1", got)
	}
}

func TestMutA3_BuildRestoredMsgCacheSaveFailure(t *testing.T) {
	root := t.TempDir()
	store, err := media.NewAt(root)
	if err != nil {
		t.Fatal(err)
	}
	// Deleting the store root makes every Save fail (temp write ENOENT).
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	c, _ := bareConnectorA3(t, nil)
	c.mediaCache = store
	rec := swapMsgRecorderA3(t)

	raw := []byte("DATA")
	msg := buildRestoredMsgA3(t, c, types.QueuedItem{Content: []types.ContentBlock{
		b64ImageBlockA3("image/png", base64.StdEncoding.EncodeToString(raw)),
		docBlockA3("/d5.pdf"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (image dropped on Save error, doc kept)", len(msg.Attachments))
	}
	if saved := savedOfA3(t, c, msg.Attachments[0].ID); saved.path != "/d5.pdf" {
		t.Fatalf("saved = %+v, want the doc path", saved)
	}
	if got := rec.count("wui:restore image re-materialize failed"); got != 1 {
		t.Fatalf("Save-failure warn = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// buildQueuedMsgs (lines 3251-3280)
// ---------------------------------------------------------------------------

func TestMutA3_BuildQueuedMsgs(t *testing.T) {
	items := []types.QueuedItem{
		{Mode: types.ItemModePrompt, UUID: "a", Value: "v", Content: []types.ContentBlock{types.NewTextBlock("x")}},
		{Mode: types.ItemModeJob, UUID: "b", Value: "job"},
		{Mode: types.ItemModePrompt, IsMeta: true, UUID: "c", Value: "meta"},
		{Mode: types.ItemModePrompt, UUID: "d", Value: "vd", Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Source: &types.ImageSource{MediaType: "image/png"}},
			{Type: types.ContentTypeDocument, Name: "n", Mime: "application/pdf"},
		}},
	}
	out := buildQueuedMsgs(items)
	if len(out) != 2 {
		t.Fatalf("queued msgs = %d, want 2 (job and meta filtered): %+v", len(out), out)
	}
	if out[0].UUID != "a" || out[0].Text != "x" {
		t.Fatalf("out[0] = %+v, want {a x} (content text beats Value)", out[0])
	}
	if out[1].UUID != "d" || out[1].Text != "vd" {
		t.Fatalf("out[1] = %+v, want {d vd}", out[1])
	}
	if len(out[1].Attachments) != 2 ||
		out[1].Attachments[0].Mime != "image/png" ||
		out[1].Attachments[1].Name != "n" || out[1].Attachments[1].Mime != "application/pdf" {
		t.Fatalf("out[1].Attachments = %+v", out[1].Attachments)
	}
}

// ---------------------------------------------------------------------------
// lastPromptAttachmentBlocks / handleRestoreAttachments (lines 3301-3350)
// ---------------------------------------------------------------------------

func TestMutA3_LastPromptAttachmentBlocks(t *testing.T) {
	img := func(tag string) types.ContentBlock {
		return types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{MediaType: "image/png", Data: tag}}
	}
	doc := func(tag string) types.ContentBlock {
		return types.ContentBlock{Type: types.ContentTypeDocument, Path: "/p-" + tag + ".pdf", Mime: "application/pdf"}
	}
	msgs := []types.Message{
		// The match: walked backwards, this is the first real prompt with
		// attachment blocks.
		{Role: types.RoleUser, Content: []types.ContentBlock{img("a0"), doc("d0")}},
		// Skipped: no attachment blocks.
		{Role: types.RoleUser, Content: nil},
		// Skipped: tool_result batch carrying an image.
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewToolResultBlock("tr1", nil, false), img("a2")}},
		// Skipped: meta (system-generated).
		{Role: types.RoleUser, Flags: types.FlagMeta, Content: []types.ContentBlock{img("a3")}},
		// Skipped: assistant message.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{img("a4")}},
	}
	blocks := lastPromptAttachmentBlocks(msgs)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (only the real prompt's blocks): %+v", len(blocks), blocks)
	}
	if blocks[0].Source.Data != "a0" || blocks[1].Path != "/p-d0.pdf" {
		t.Fatalf("blocks = %+v, want [a0 d0]", blocks)
	}
	if got := lastPromptAttachmentBlocks(nil); got != nil {
		t.Fatalf("nil msgs = %#v, want nil", got)
	}
}

func TestMutA3_HandleRestoreAttachments(t *testing.T) {
	// No active slot: empty restore_result, no panic.
	c, _ := bareConnectorA3(t, nil)
	ghost := "ghost"
	c.active.Store(&ghost)
	withTimeoutA3(t, "handleRestoreAttachments", c.handleRestoreAttachments)
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (restore_result)", len(frames))
	}
	var resp struct {
		Type        string                `json:"type"`
		Attachments []struct{ ID string } `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "restore_result" || len(resp.Attachments) != 0 {
		t.Fatalf("resp = %+v, want empty restore_result", resp)
	}

	// Slot with nil engine: guarded, no panic.
	c2, _ := bareConnectorA3(t, nil)
	c2.slots["main"].engine = nil
	withTimeoutA3(t, "handleRestoreAttachments", c2.handleRestoreAttachments)

	// Stash path: one attachment re-staged under a fresh id.
	c3, _ := bareConnectorA3(t, nil)
	c3.restoreMu.Lock()
	c3.rewindStash = &rewindStash{engineID: "main", blocks: []types.ContentBlock{fileImageBlockA3("/img/r.png", "image/png")}}
	c3.restoreMu.Unlock()
	withTimeoutA3(t, "handleRestoreAttachments", c3.handleRestoreAttachments)
	frames3 := takeAllFramesA3(c3)
	if len(frames3) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames3))
	}
	if err := json.Unmarshal([]byte(frames3[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Attachments) != 1 || resp.Attachments[0].ID == "" {
		t.Fatalf("attachments = %+v, want one re-staged id", resp.Attachments)
	}
}

// ---------------------------------------------------------------------------
// switchEngine (lines 3359-3393)
// ---------------------------------------------------------------------------

func TestMutA3_SwitchEngineUnknown(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	c.switchEngine("nope")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (error)", len(frames))
	}
	var errMsg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &errMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if errMsg.Type != "error" || !strings.Contains(errMsg.Message, "unknown engine: nope") {
		t.Fatalf("error frame = %+v, want unknown-engine error", errMsg)
	}
	if c.ActiveID() != "main" {
		t.Fatalf("ActiveID = %q, want unchanged main", c.ActiveID())
	}
}

func TestMutA3_SwitchEngineManagerSync(t *testing.T) {
	// SetActive fails (engine missing from the manager): warn once, no
	// PersistMeta run.
	c, _ := bareConnectorA3(t, nil)
	m2 := &mockEngine{projectDir: t.TempDir()}
	m2.engineIDFn = func() string { return "e2" }
	c.slots["e2"] = newMockSlotA3("e2", m2)
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c.mgr = mgr

	rec := swapMsgRecorderA3(t)
	c.switchEngine("e2")
	if got := rec.count("wui:switchEngine:SetActive failed"); got != 1 {
		t.Fatalf("SetActive-failure warn = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(m2.projectDir, "meta.json")); !os.IsNotExist(err) {
		t.Fatal("meta.json must not be written when SetActive fails")
	}
	if c.ActiveID() != "e2" {
		t.Fatalf("ActiveID = %q, want e2", c.ActiveID())
	}
	if !c.slots["e2"].active.Load() || c.slots["main"].active.Load() {
		t.Fatal("active flags not flipped")
	}

	// SetActive succeeds but PersistMeta fails: warn once.
	dir := t.TempDir()
	m3 := &mockEngine{projectDir: dir}
	m3.engineIDFn = func() string { return "e3" }
	c2, _ := bareConnectorA3(t, nil)
	c2.slots["e3"] = newMockSlotA3("e3", m3)
	mgr2 := engine.NewEngineManager()
	mgr2.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	mgr2.Add(&engine.EngineViewState{ID: "e3", Name: "E3"})
	if err := mgr2.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c2.mgr = mgr2
	rec2 := swapMsgRecorderA3(t)

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m3.projectDirFn = func() string { return blocked }
	c2.switchEngine("e3")
	if got := rec2.count("wui:switchEngine:PersistMeta failed"); got != 1 {
		t.Fatalf("PersistMeta-failure warn = %d, want 1", got)
	}

	// Full success: no warns, meta.json written under the new engine's dir.
	c3, _ := bareConnectorA3(t, nil)
	dir3 := t.TempDir()
	m4 := &mockEngine{projectDir: dir3}
	m4.engineIDFn = func() string { return "e4" }
	c3.slots["e4"] = newMockSlotA3("e4", m4)
	mgr3 := engine.NewEngineManager()
	mgr3.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	mgr3.Add(&engine.EngineViewState{ID: "e4", Name: "E4"})
	if err := mgr3.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	c3.mgr = mgr3
	rec3 := swapMsgRecorderA3(t)
	c3.switchEngine("e4")
	if got := rec3.count("wui:switchEngine:SetActive failed") + rec3.count("wui:switchEngine:PersistMeta failed"); got != 0 {
		t.Fatalf("success warns = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(dir3, "meta.json")); err != nil {
		t.Fatalf("meta.json not persisted: %v", err)
	}
}
