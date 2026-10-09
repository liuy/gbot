package wui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// parkActiveWS dials a raw WS pair and stores the server side in activeWS.
// The client side is never read; with no writer draining it the conn is
// inert, and with a writer it parks once its socket buffers fill. Returns
// the server-side conn.
func parkActiveWS(t *testing.T, c *WUIConnector) *websocket.Conn {
	t.Helper()
	srvWSCh := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srvWSCh <- ws
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	clientWS, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientWS.Close() })
	srvWS := <-srvWSCh
	t.Cleanup(func() { _ = srvWS.Close() })
	c.activeWS.Store(srvWS)
	return srvWS
}

// takeOutboundFrames pops every frame currently sitting in the outbound
// queue: the whole current era queue in one take.
func takeOutboundFrames(c *WUIConnector) []wsMsg {
	return c.outQ.Load().take()
}

// TestSwitchEngine_CompletesWhenOutboundQueueFullAndWriterStalled is the
// deterministic deadlock repro for the takeover stall: with the outbound
// queue full and no writer draining it, a flood of engine events parks a
// Handle call inside sendWS while holding ssMu, so switchEngine's
// sendMetadata can never acquire ssMu. The era-scoped queue must release
// the parked pushers (era close) and complete the handshake in
// milliseconds with metadata as the first frame of the new era.
func TestSwitchEngine_CompletesWhenOutboundQueueFullAndWriterStalled(t *testing.T) {
	// Manual connector: NO wsWriter goroutine, outbound capacity 4, done
	// open. slot "main" is active with a mockEngine (empty messages,
	// queryStartMsgIdx -1) so sendMetadata runs its full path.
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(4))
	activeID := "main"
	c.active.Store(&activeID)
	slot := &engineSlot{
		engineID:    "main",
		engine:      &mockEngine{},
		taskToolIDs: make(map[string]bool),
	}
	slot.active.Store(true)
	c.slots["main"] = slot

	// A real upgraded conn stored in activeWS so sendMetadata's send path
	// is the production one. Nothing ever reads or writes it: without a
	// wsWriter the conn is inert.
	parkActiveWS(t, c)

	// done must be closed exactly once whether the test fatals mid-way or
	// runs to Assert C.
	var doneOnce sync.Once
	closer := func() { doneOnce.Do(func() { close(c.done) }) }
	t.Cleanup(closer)

	// Act 1: flood the outbound queue. entered counts Handle calls started;
	// with capacity 4 and no writer, calls 1-4 complete and call 5 is parked
	// on the full queue.
	var entered atomic.Int32
	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		for i := range 64 {
			entered.Add(1)
			c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("d%d", i)})
		}
	}()
	if !waitFor(2*time.Second, func() bool { return entered.Load() >= 5 }) {
		closer()
		t.Fatal("flood never reached the parked pusher: outbound queue did not fill")
	}

	// Act 2: takeover handshake. On master sendMetadata blocks forever on
	// ssMu held by the parked flood call.
	switchDone := make(chan struct{})
	go func() {
		defer close(switchDone)
		c.switchEngine(c.ActiveID())
	}()
	if !waitFor(2*time.Second, func() bool {
		select {
		case <-switchDone:
			return true
		default:
			return false
		}
	}) {
		closer()
		t.Fatal("takeover handshake stalled: sendMetadata blocked behind full outbound queue")
	}

	// Assert B: the first frame of the new era must be metadata.
	frames := takeOutboundFrames(c)
	if len(frames) == 0 {
		closer()
		t.Fatal("no outbound frames after takeover handshake")
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(frames[0].data, &head); err != nil {
		closer()
		t.Fatalf("unmarshal first outbound frame: %v", err)
	}
	if head.Type != "metadata" {
		closer()
		t.Fatalf("first outbound frame after takeover = %q, want metadata", head.Type)
	}

	// Assert C: parked flood pushers must be released by the era flip (or
	// done), so the flood finishes promptly.
	closer()
	select {
	case <-floodDone:
	case <-time.After(2 * time.Second): // REAL-TIME
		t.Fatal("flood sender still blocked after takeover era flip")
	}
}

// TestTakeover_MetadataFirstFrameAndNoDeltaLossUnderFlood exercises the real
// takeover path under a continuous delta flood: a second client dials while
// the first client's outbound backlog is building. The new client's first
// frame must be metadata (never a stale backlog frame), the snapshot must be
// an exact prefix of the flood stream, and snapshot + live tail must equal
// the complete stream — no dropped, duplicated, or out-of-order deltas.
func TestTakeover_MetadataFirstFrameAndNoDeltaLossUnderFlood(t *testing.T) {
	c := newTestConnector(t)
	// ws1 takes the active slot; its metadata is drained, then nobody reads
	// it again — that unread client is what builds the backlog.
	_ = dialAndStore(t, c)

	// Unbounded flood: the loop only stops when stop closes, so deltas are
	// produced before, during, and after the ws2 dial — the takeover must
	// be correct at any interleaving.
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopFlooding := func() { stopOnce.Do(func() { close(stop) }) }
	t.Cleanup(stopFlooding)
	var counter atomic.Int64
	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			i := counter.Add(1) - 1
			c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("s%d", i)})
		}
	}()

	// The flood must be measurably running before the dial: the snapshot
	// coverage assertion below requires at least the first 32 deltas to
	// have been produced pre-dial.
	if !waitFor(2*time.Second, func() bool { return counter.Load() >= 32 }) {
		stopFlooding()
		t.Fatal("flood did not start producing deltas")
	}

	// Act: dial ws2 through the real RegisterChatWS handler (takeover).
	mux2 := http.NewServeMux()
	RegisterChatWS(mux2, c)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	ws2 := dialChatWS(t, "ws"+strings.TrimPrefix(srv2.URL, "http")+"/ws/chat")

	// Assert D: the first frame ws2 receives must be metadata. On master
	// the stale backlog queued ahead of the handshake is delivered first.
	first := readWSMessage(t, ws2)
	var head struct {
		Type     string          `json:"type"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(first, &head); err != nil {
		stopFlooding()
		t.Fatalf("unmarshal ws2 first frame: %v", err)
	}
	if head.Type != "metadata" {
		stopFlooding()
		t.Fatalf("ws2 first frame type = %q, want metadata (stale backlog leaked past takeover)", head.Type)
	}

	// Assert E: exactly one text block in the snapshot, and it must be an
	// exact concatenation of the first K flood deltas (K >= 32).
	snapBlocks := extractSnapshotFromMetadata(t, head.Snapshot)
	if len(snapBlocks) != 1 {
		stopFlooding()
		t.Fatalf("snapshot has %d blocks, want exactly 1 text block", len(snapBlocks))
	}
	if snapBlocks[0].Kind != "text" {
		stopFlooding()
		t.Fatalf("snapshot block kind = %q, want text", snapBlocks[0].Kind)
	}
	snapshotText := snapBlocks[0].Text
	if snapshotText == "" {
		stopFlooding()
		t.Fatal("snapshot text is empty, want a non-empty prefix of the flood stream")
	}
	var covered strings.Builder
	deltas := 0
	for covered.Len() < len(snapshotText) {
		fmt.Fprintf(&covered, "s%d", deltas)
		deltas++
	}
	if covered.String() != snapshotText {
		stopFlooding()
		t.Fatalf("snapshot text is not an exact concatenation of flood deltas s0..s%d", deltas-1)
	}
	if deltas < 32 {
		stopFlooding()
		t.Fatalf("snapshot covers only %d deltas, want at least 32 (flood ran before the dial)", deltas)
	}

	// Assert F (no-gap invariant): stop the flood, then drain ws2 while
	// the flood winds down (the writer needs a reader or it parks on a
	// full socket). Reading continues until a quiet window coincides with
	// flood completion; then snapshot + live must equal the full stream.
	stopFlooding()
	var live strings.Builder
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	drained := false
	for !drained {
		if time.Now().After(deadline) { // REAL-TIME
			t.Fatalf("no-gap invariant violated: live tail never went quiet (snapshot=%d chars, live=%d chars)", len(snapshotText), live.Len())
		}
		_ = ws2.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
		_, data, err := ws2.ReadMessage()
		if err != nil {
			select {
			case <-floodDone:
				drained = true
			default:
			}
			continue
		}
		var env struct {
			Type  string `json:"type"`
			Event struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"event"`
		}
		if json.Unmarshal(data, &env) != nil || env.Type != "event" || env.Event.Type != "text_delta" {
			continue
		}
		live.WriteString(env.Event.Text)
	}

	n := counter.Load()
	var expected strings.Builder
	for i := int64(0); i < n; i++ {
		fmt.Fprintf(&expected, "s%d", i)
	}
	got := snapshotText + live.String()
	if got != expected.String() {
		t.Fatalf("no-gap invariant violated: snapshot+live = %d chars, want %d (missing or duplicated deltas across the takeover boundary)", len(got), expected.Len())
	}

	// Exact boundary: after equality no further delta frame may arrive.
	_ = ws2.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
	if _, data, err := ws2.ReadMessage(); err == nil {
		var env struct {
			Type  string `json:"type"`
			Event struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"event"`
		}
		if json.Unmarshal(data, &env) == nil && env.Type == "event" && env.Event.Type == "text_delta" {
			t.Fatalf("received extra delta %q after exact-boundary equality: duplicate or out-of-era frame", env.Event.Text)
		}
	}
}

// manualEventConnector builds a no-writer connector with one active mock
// engine slot and a parked conn, so onEngineEvent captures are observable
// as queued frames in exact order.
func manualEventConnector(t *testing.T, mock *mockEngine) *WUIConnector {
	t.Helper()
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	activeID := "main"
	c.active.Store(&activeID)
	slot := &engineSlot{
		engineID:    "main",
		engine:      mock,
		taskToolIDs: make(map[string]bool),
	}
	slot.active.Store(true)
	c.slots["main"] = slot
	parkActiveWS(t, c)
	return c
}

// frameHeads takes every queued frame and returns its wire "type" field in
// order. Fatal on unmarshalable frames.
func frameHeads(t *testing.T, c *WUIConnector) []string {
	t.Helper()
	frames := takeOutboundFrames(c)
	heads := make([]string, 0, len(frames))
	for _, f := range frames {
		var h struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(f.data, &h); err != nil {
			t.Fatalf("unmarshal frame head: %v (%q)", err, string(f.data))
		}
		heads = append(heads, h.Type)
	}
	return heads
}

// TestOnEngineEvent_CaptureRequiresActiveAndConn pins the capture
// condition: BOTH slot.active and a live activeWS are required. The
// deactivation window (takeover: slot already false, conn already swapped)
// and the disconnected window (slot true, conn nil) must capture nothing —
// yet update streamState in both, or the snapshot misses deltas.
func TestOnEngineEvent_CaptureRequiresActiveAndConn(t *testing.T) {
	c := manualEventConnector(t, &mockEngine{})
	slot := c.slots["main"]

	// Deactivation window: active=false while activeWS is already set.
	slot.active.Store(false)
	c.onEngineEvent("main", types.QueryEvent{Type: types.EventTextDelta, Text: "d0"})
	if heads := frameHeads(t, c); len(heads) != 0 {
		t.Fatalf("captured frames %v with slot inactive (deactivation-window leak)", heads)
	}
	if got := streamStateCount(c, "main"); got != 1 {
		t.Fatalf("streamState blocks = %d, want 1 (inactive path must still update state)", got)
	}

	// Disconnected window: active=true, conn cleared.
	slot.active.Store(true)
	c.activeWS.Store(nil)
	c.onEngineEvent("main", types.QueryEvent{Type: types.EventTextDelta, Text: "d1"})
	if heads := frameHeads(t, c); len(heads) != 0 {
		t.Fatalf("captured frames %v with no active conn", heads)
	}
	if got := streamStateCount(c, "main"); got != 1 {
		t.Fatalf("streamState blocks = %d, want 1 (second delta must merge into the text block)", got)
	}
}

// TestOnEngineEvent_QueryEndErrorAndTaskListFraming pins the post-lock push
// framing: a plain query_end error sends an error frame after the event
// frame, an aborted or clean query_end sends the event frame only, and a
// Task tool_end pushes task_list after the event frame while a nil
// ToolResult must not.
func TestOnEngineEvent_QueryEndErrorAndTaskListFraming(t *testing.T) {
	tl := newRealTaskList(t)
	if _, err := tl.CreateTask("Framing", "desc", "", nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c := manualEventConnector(t, &mockEngine{taskListFn: func() *task.List { return tl }})

	// Track a Task tool call: one event frame.
	c.onEngineEvent("main", types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Task"},
	})
	if heads := frameHeads(t, c); len(heads) != 1 || heads[0] != "event" {
		t.Fatalf("after Task tool_start: heads = %v, want [event]", heads)
	}

	// Task tool_end: event frame, then task_list frame.
	c.onEngineEvent("main", types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu1"},
	})
	if heads := frameHeads(t, c); len(heads) != 2 || heads[0] != "event" || heads[1] != "task_list" {
		t.Fatalf("after Task tool_end: heads = %v, want [event task_list]", heads)
	}

	// tool_end with nil ToolResult: event frame only — no task_list push.
	c.onEngineEvent("main", types.QueryEvent{Type: types.EventToolEnd})
	if heads := frameHeads(t, c); len(heads) != 1 || heads[0] != "event" {
		t.Fatalf("after nil-ToolResult tool_end: heads = %v, want [event] only", heads)
	}

	// A non-tool_end event carrying a ToolResult (tracked ID) must not push
	// task_list either — the push is reserved for tool_end.
	c.onEngineEvent("main", types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: "out"},
	})
	if heads := frameHeads(t, c); len(heads) != 1 || heads[0] != "event" {
		t.Fatalf("after tool_output_delta: heads = %v, want [event] only", heads)
	}

	// A non-query_end event carrying an Error must not push an error frame —
	// the error frame is reserved for failed query_end.
	c.onEngineEvent("main", types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "mid-stream",
		Error: errors.New("mid-stream errors are not query failures"),
	})
	if heads := frameHeads(t, c); len(heads) != 1 || heads[0] != "event" {
		t.Fatalf("after error-carrying text delta: heads = %v, want [event] only", heads)
	}

	// query_end with a plain error: event frame, then error frame.
	c.onEngineEvent("main", types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: errors.New("boom"),
	})
	frames := takeOutboundFrames(c)
	if len(frames) != 2 {
		t.Fatalf("after failed query_end: %d frames, want 2 (event + error)", len(frames))
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(frames[0].data, &head); err != nil || head.Type != "event" {
		t.Fatalf("first frame = %q, want event", string(frames[0].data))
	}
	var errFrame struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frames[1].data, &errFrame); err != nil || errFrame.Type != "error" {
		t.Fatalf("second frame = %q, want error", string(frames[1].data))
	}
	if !strings.Contains(errFrame.Message, "boom") {
		t.Fatalf("error message = %q, want it to contain 'boom'", errFrame.Message)
	}

	// Aborted query_end: event frame only (no error frame for user aborts).
	c.onEngineEvent("main", types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: &engine.AbortError{Phase: "streaming"},
	})
	if heads := frameHeads(t, c); len(heads) != 1 || heads[0] != "event" {
		t.Fatalf("after aborted query_end: heads = %v, want [event] only", heads)
	}

	// Clean query_end: event frame only.
	c.onEngineEvent("main", types.QueryEvent{Type: types.EventQueryEnd})
	if heads := frameHeads(t, c); len(heads) != 1 || heads[0] != "event" {
		t.Fatalf("after clean query_end: heads = %v, want [event] only", heads)
	}
}

// TestSwitchEngine_NoConnStillActivates pins the no-conn branch: without a
// client there is no handshake, but the switched-to engine must activate
// (and nothing may be pushed).
func TestSwitchEngine_NoConnStillActivates(t *testing.T) {
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	mainID := "main"
	c.active.Store(&mainID)
	mainSlot := &engineSlot{engineID: "main", engine: &mockEngine{}, taskToolIDs: map[string]bool{}}
	mainSlot.active.Store(true)
	c.slots["main"] = mainSlot
	c.slots["other"] = &engineSlot{engineID: "other", engine: &mockEngine{}, taskToolIDs: map[string]bool{}}

	c.switchEngine("other")

	if !c.slots["other"].active.Load() {
		t.Fatal("switchEngine without a conn did not activate the new engine — its events would never stream to a later client")
	}
	if mainSlot.active.Load() {
		t.Fatal("old engine still active after switch")
	}
	if frames := takeOutboundFrames(c); frames != nil {
		t.Fatalf("switchEngine without a conn pushed %d frames, want 0 (no handshake without a client)", len(frames))
	}
}

// TestStop_ClosesDoneAndOutQueue pins shutdown: Stop must close done (the
// writer's only other exit) and close the current era so pushers parked on
// a full queue are released by the queue itself.
func TestStop_ClosesDoneAndOutQueue(t *testing.T) {
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	c.Stop()

	select {
	case <-c.done:
	default:
		t.Fatal("Stop did not close done — the writer goroutine would never exit")
	}
	c.sendWS([]byte("late"))
	if frames := takeOutboundFrames(c); frames != nil {
		t.Fatalf("frames queued after Stop = %d, want 0 (era closed, late frames dropped)", len(frames))
	}
}

// TestCloseForUpgrade_ClosesOutQueueInstallsFresh pins the upgrade path: the
// pre-upgrade era is closed (pending frames dropped, parked pushers released)
// AND a fresh, live queue takes its place. Without the fresh queue the writer
// wakes on the closed era's notify, take() returns nil forever, and the loop
// spins at 100% CPU for the seconds of Shutdown before the upgrade's os.Exit.
// Interregnum frames never reach any client: activeWS is nil after the call,
// so the writer drops them at write time.
func TestCloseForUpgrade_ClosesOutQueueInstallsFresh(t *testing.T) {
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	oldQ := newOutQueue(outQueueCapacity)
	c.outQ.Store(oldQ)

	c.CloseForUpgrade()

	// The pre-upgrade era is closed: pushes to it drop. done is still open
	// here — only the closed era can explain the drop.
	oldQ.push(wsMsg{data: []byte("late")}, c.done)
	if frames := oldQ.take(); frames != nil {
		t.Fatalf("frames taken from closed pre-upgrade era = %d, want 0", len(frames))
	}

	// A fresh queue replaces it, or the writer busy-spins on the closed one.
	fresh := c.outQ.Load()
	if fresh == nil {
		t.Fatal("outQ is nil after CloseForUpgrade — wsWriter has no queue to drain")
	}
	if fresh == oldQ {
		t.Fatal("CloseForUpgrade left the closed era installed — wsWriter spins on its closed notify channel")
	}
	// No-spin property, structurally: an empty live queue must have an OPEN
	// notify channel (a closed one receives immediately, which is the spin).
	select {
	case <-fresh.notify:
		t.Fatal("fresh queue's notify already closed — writer's wait would return immediately (busy-spin)")
	default:
	}
	// Push→take roundtrip completes on the fresh queue.
	c.sendWS([]byte("post"))
	frames := fresh.take()
	if len(frames) != 1 || string(frames[0].data) != "post" {
		t.Fatalf("post-upgrade roundtrip frames = %v, want exactly [post]", frames)
	}
}

// TestWSWriter_EraCheckDropsMidBatchTailOnNewConn is the deterministic
// killer for the per-frame era check: a batch the writer already TOOK from
// the old queue can be mid-write when a takeover swaps in a new conn — the
// batch tail must never reach the new conn before its metadata.
func TestWSWriter_EraCheckDropsMidBatchTailOnNewConn(t *testing.T) {
	c := newTestConnector(t) // real writer running

	srvWSCh := make(chan *websocket.Conn, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srvWSCh <- ws
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dial := func() *websocket.Conn {
		t.Helper()
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return ws
	}

	// conn1: never-read client with a constrained receive buffer. conn2 reads.
	// The buffers must stay ABOVE ~2x loopback MSS (~131KB): a smaller
	// window never reaches the window-update threshold and the flow wedges
	// in zero-window. At 128KB (kernel doubles it) a 1 MiB frame still
	// cannot be absorbed, so the writer parks mid-frame until client1
	// reads.
	client1 := dial()
	t.Cleanup(func() { _ = client1.Close() })
	if tcp, ok := client1.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(128 * 1024)
	}
	srvWS1 := <-srvWSCh
	t.Cleanup(func() { _ = srvWS1.Close() })
	if tcp, ok := srvWS1.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(128 * 1024)
	}
	client2 := dial()
	t.Cleanup(func() { _ = client2.Close() })
	srvWS2 := <-srvWSCh
	t.Cleanup(func() { _ = srvWS2.Close() })

	c.activeWS.Store(srvWS1)

	// A 1 MiB frame exceeds both kernels' buffers even at their defaults,
	// so the writer parks mid-write until client1 reads. waitFor on an
	// empty queue proves the batch was TAKEN; the unfittable size proves
	// the writer is parked INSIDE WriteMessage.
	q1 := c.outQ.Load()
	blocker1 := bytes.Repeat([]byte("b"), 1<<20)
	c.sendWS(blocker1)
	if !waitFor(2*time.Second, func() bool { return q1.len() == 0 }) {
		t.Fatal("writer never took the first blocking batch")
	}

	// Queue blocker2 + tail while the writer is provably parked: the next
	// take() hands them out as ONE batch.
	blocker2 := bytes.Repeat([]byte("c"), 1<<20)
	tail := []byte(`{"type":"stale-tail"}`)
	c.sendWS(blocker2)
	c.sendWS(tail)
	if got := q1.len(); got != 2 {
		t.Fatalf("queued length = %d, want 2 while the writer is parked mid-write", got)
	}

	// Un-park the writer: client1 drains blocker1, the writer finishes the
	// first batch, then takes [blocker2, tail] together and parks on
	// blocker2.
	_, _, err := client1.ReadMessage()
	if err != nil {
		t.Fatalf("client1 read blocker1: %v", err)
	}
	if !waitFor(2*time.Second, func() bool { return q1.len() == 0 }) {
		t.Fatal("writer never took the [blocker2, tail] batch")
	}

	// Takeover lands mid-batch: fresh era, new conn, dead old conn.
	oldQ := c.outQ.Swap(newOutQueue(outQueueCapacity))
	oldQ.close()
	c.activeWS.Swap(srvWS2)
	_ = client1.UnderlyingConn().Close()

	// blocker2's write fails; the era check must drop the batch tail
	// before it can reach the new conn.
	_ = client2.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
	if _, data, err := client2.ReadMessage(); err == nil {
		shown := string(data)
		if len(shown) > 60 {
			shown = shown[:60] + "..."
		}
		t.Fatalf("old-era batch tail reached the new conn before its metadata: %q", shown)
	}
}
