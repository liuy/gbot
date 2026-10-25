package wui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
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

// parkActiveWSH dials a raw WS pair over the hermetic transport and stores
// the server side in activeWS. The client side is never read; over the
// synchronous pipe any server write parks immediately, which is the parked
// -writer precondition these tests need. Returns the server (close it via
// defer before the bubble exits) so callers can tear the subtree down.
func parkActiveWSH(t *testing.T, c *WUIConnector) (*chatServer, *websocket.Conn) {
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
	srv := startHermeticServer(mux)
	cc := srv.newClientConn()
	d := websocket.Dialer{NetDial: func(_, _ string) (net.Conn, error) { return cc, nil }}
	clientWS, _, err := d.Dial("ws://wui-hermetic/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	srvWS := <-srvWSCh
	c.activeWS.Store(srvWS)
	return srv, clientWS
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
	inBubble(t, func(t *testing.T) {
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
		srv, _ := parkActiveWSH(t, c)
		defer srv.close()

		// done must be closed exactly once whether the test fatals mid-way or
		// runs to Assert C.
		var doneOnce sync.Once
		closer := func() { doneOnce.Do(func() { close(c.done) }) }
		defer closer()

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
			t.Fatal("takeover handshake stalled: sendMetadata blocked behind full outbound queue")
		}

		// Assert B: the first frame of the new era must be metadata.
		frames := takeOutboundFrames(c)
		if len(frames) == 0 {
			t.Fatal("no outbound frames after takeover handshake")
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(frames[0].data, &head); err != nil {
			t.Fatalf("unmarshal first outbound frame: %v", err)
		}
		if head.Type != "metadata" {
			t.Fatalf("first outbound frame after takeover = %q, want metadata", head.Type)
		}

		// Assert C: parked flood pushers must be released by the era flip (or
		// done), so the flood finishes promptly.
		closer()
		select {
		case <-floodDone:
		case <-time.After(2 * time.Second):
			t.Fatal("flood sender still blocked after takeover era flip")
		}
	})
}

// TestTakeover_MetadataFirstFrameAndNoDeltaLossUnderFlood exercises the real
// takeover path under a continuous delta flood: a second client dials while
// the first client's outbound backlog is building. The new client's first
// frame must be metadata (never a stale backlog frame), the snapshot must be
// an exact prefix of the flood stream, and snapshot + live tail must equal
// the complete stream — no dropped, duplicated, or out-of-order deltas.
func TestTakeover_MetadataFirstFrameAndNoDeltaLossUnderFlood(t *testing.T) {
	hermeticWUI(t, func(t *testing.T, c *WUIConnector, hs *chatServer) {
		// ws1 takes the active slot; its metadata is drained, then nobody reads
		// it again — that unread client is what builds the backlog.
		_ = hs.dialDrained(t)

		// Bounded flood: deltas are produced before, during, and after the
		// ws2 dial — the takeover must be correct at any interleaving. The
		// count bound replaces the historical wall-time bound: inside a
		// synctest bubble the clock only advances when every goroutine is
		// durably blocked, and during the takeover window events are
		// captured state-only (slot inactive — no push, no backpressure), so
		// a wall-style endless flood would spin forever and freeze the
		// virtual clock. A bounded flood always ends durably (parked on a
		// full queue or returned), letting the takeover's WriteControl
		// deadline fire and the test proceed.
		const floodMax = 4096
		stop := make(chan struct{})
		var stopOnce sync.Once
		stopFlooding := func() { stopOnce.Do(func() { close(stop) }) }
		defer stopFlooding()
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
				if counter.Load() >= floodMax {
					return
				}
				i := counter.Add(1) - 1
				c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("s%d", i)})
			}
		}()

		// The flood must be measurably running before the dial: the snapshot
		// coverage assertion below requires at least the first 32 deltas to
		// have been produced pre-dial.
		if !waitFor(2*time.Second, func() bool { return counter.Load() >= 32 }) {
			t.Fatal("flood did not start producing deltas")
		}

		// Act: dial ws2 through the real RegisterChatWS handler (takeover).
		ws2 := hs.dialWS(t)

		// Assert D: the first frame ws2 receives must be metadata. On master
		// the stale backlog queued ahead of the handshake is delivered first.
		first := readWSMessage(t, ws2)
		var head struct {
			Type     string          `json:"type"`
			Snapshot json.RawMessage `json:"snapshot"`
		}
		if err := json.Unmarshal(first, &head); err != nil {
			t.Fatalf("unmarshal ws2 first frame: %v", err)
		}
		if head.Type != "metadata" {
			t.Fatalf("ws2 first frame type = %q, want metadata (stale backlog leaked past takeover)", head.Type)
		}
		if head.Snapshot != nil {
			t.Fatalf("metadata still embeds a snapshot (%.60s), want it stripped", head.Snapshot)
		}

		// Request the tail page; the nonce identifies the response among the
		// concurrently arriving live event frames.
		pageReq, _ := json.Marshal(map[string]any{"type": "history_request", "cursor": "", "limit": 30, "nonce": 4242})
		if err := ws2.WriteMessage(websocket.TextMessage, pageReq); err != nil {
			t.Fatalf("write page request: %v", err)
		}

		// Assert E/F rewritten as seq-set semantics (v2): the page freezes a
		// prefix of the flood, every delta numbered above the page's seq must
		// arrive exactly once as a live frame, every frame numbered at or
		// below it must be a delta the page already absorbed (wire-late but
		// harmless — the client drops by seq), and page ⊕ live = full stream.
		stopFlooding()
		var pageJSON []byte
		var pageSeq uint64
		var pageText string
		type liveDelta struct {
			seq uint64
			idx int
		}
		var live []liveDelta
		deadline := time.Now().Add(10 * time.Second)
		drained := false
		for !drained {
			if time.Now().After(deadline) {
				t.Fatalf("no-gap invariant violated: live tail never went quiet (page=%d chars, live frames=%d)", len(pageText), len(live))
			}
			_ = ws2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
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
				Nonce uint64 `json:"nonce"`
				Seq   uint64 `json:"seq"`
				Event struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"event"`
				Stream []streamBlock `json:"stream"`
			}
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			switch env.Type {
			case "event":
				if env.Event.Type != "text_delta" {
					continue
				}
				var idx int
				if _, err := fmt.Sscanf(env.Event.Text, "s%d", &idx); err != nil {
					t.Fatalf("unexpected non-flood delta %q", env.Event.Text)
				}
				live = append(live, liveDelta{seq: env.Seq, idx: idx})
			case "history":
				if env.Nonce != 4242 {
					continue
				}
				pageJSON = data
				pageSeq = env.Seq
				if len(env.Stream) != 1 || env.Stream[0].Kind != "text" {
					t.Fatalf("page stream = %d blocks, want exactly 1 text block", len(env.Stream))
				}
				pageText = env.Stream[0].Text
			}
		}
		if pageJSON == nil {
			t.Fatal("page response never arrived (nonce 4242)")
		}

		// The page must be an exact prefix s0..sK of the flood.
		var covered strings.Builder
		k := 0
		for covered.Len() < len(pageText) {
			fmt.Fprintf(&covered, "s%d", k)
			k++
		}
		if covered.String() != pageText {
			t.Fatalf("page text is not an exact concatenation of flood deltas s0..s%d", k-1)
		}
		if k < 32 {
			t.Fatalf("page covers only %d deltas, want at least 32 (flood ran before the dial)", k)
		}

		n := int(counter.Load())
		// (i) deltas numbered above the page are exactly {K..N-1} —
		// continuous, no gap, no duplicate.
		above := make(map[int]bool)
		for _, d := range live {
			if d.seq > pageSeq {
				above[d.idx] = true
			}
		}
		if len(above) != n-k {
			t.Fatalf("deltas above page seq = %d unique indices, want %d (duplicates or dropped frames)", len(above), n-k)
		}
		for i := k; i < n; i++ {
			if !above[i] {
				t.Fatalf("delta s%d (index %d) missing above page seq %d — dropped across the takeover boundary", i, i, pageSeq)
			}
		}
		// (ii) frames numbered at/below the page seq carry deltas the page
		// already absorbed (wire-late, client drops by seq).
		for _, d := range live {
			if d.seq <= pageSeq && d.idx > k-1 {
				t.Fatalf("delta s%d (index %d) numbered %d <= page seq %d but not absorbed by the page", d.idx, d.idx, d.seq, pageSeq)
			}
		}
		// (iii) page ⊕ live(above, seq order) == the full flood stream.
		sort.Slice(live, func(a, b int) bool { return live[a].seq < live[b].seq })
		var rebuilt strings.Builder
		rebuilt.WriteString(pageText)
		for _, d := range live {
			if d.seq > pageSeq {
				fmt.Fprintf(&rebuilt, "s%d", d.idx)
			}
		}
		var expected strings.Builder
		for i := range n {
			fmt.Fprintf(&expected, "s%d", i)
		}
		if rebuilt.String() != expected.String() {
			t.Fatalf("no-gap invariant violated: page+live = %d chars, want %d (missing or duplicated deltas across the takeover boundary)", rebuilt.Len(), expected.Len())
		}
	})
}

// manualEventConnector builds a no-writer connector with one active mock
// engine slot and a parked conn, so onEngineEvent captures are observable
// as queued frames in exact order. Returns the parked server for teardown.
func manualEventConnector(t *testing.T, mock *mockEngine) (*WUIConnector, *chatServer) {
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
	srv, _ := parkActiveWSH(t, c)
	return c, srv
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
	inBubble(t, func(t *testing.T) {
		c, srv := manualEventConnector(t, &mockEngine{})
		defer srv.close()
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
	})
}

// TestOnEngineEvent_QueryEndErrorAndTaskListFraming pins the post-lock push
// framing: a plain query_end error sends an error frame after the event
// frame, an aborted or clean query_end sends the event frame only, and a
// Task tool_end pushes task_list after the event frame while a nil
// ToolResult must not.
func TestOnEngineEvent_QueryEndErrorAndTaskListFraming(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		tl := newRealTaskList(t)
		if _, err := tl.CreateTask("Framing", "desc", "", nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		c, srv := manualEventConnector(t, &mockEngine{taskListFn: func() *task.List { return tl }})
		defer srv.close()

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
	})
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
	inBubble(t, func(t *testing.T) {
		c := newTestConnector(t) // real writer running
		defer c.Stop()

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
		srv := startHermeticServer(mux)
		defer srv.close()
		dial := func() *websocket.Conn {
			t.Helper()
			return srv.dialWSAt(t, "/ws")
		}

		// conn1: never-read client. conn2 reads. The synchronous pipe has no
		// kernel buffer at all, so the writer parks mid-frame on the very
		// first byte of the 1 MiB blocker — a strictly stronger version of
		// the TCP 128KB-window setup this test used on the real transport.
		client1 := dial()
		srvWS1 := <-srvWSCh
		client2 := dial()
		srvWS2 := <-srvWSCh

		c.activeWS.Store(srvWS1)

		// A 1 MiB frame cannot be absorbed while client1 never reads, so the
		// writer parks mid-write. waitFor on an empty queue proves the batch
		// was TAKEN; the unfittable size proves the writer is parked INSIDE
		// WriteMessage.
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
		// before it can reach the new conn. The 500ms observation span runs
		// on the bubble's virtual clock.
		_ = client2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if _, data, err := client2.ReadMessage(); err == nil {
			shown := string(data)
			if len(shown) > 60 {
				shown = shown[:60] + "..."
			}
			t.Fatalf("old-era batch tail reached the new conn before its metadata: %q", shown)
		}
	})
}
