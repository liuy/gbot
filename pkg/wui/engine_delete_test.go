package wui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// recordedAborts reads the mock's abort tally under its lock.
func (m *mockEngine) recordedAborts() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abortCount
}

// engineListState unmarshals buildEngineList's payload and returns the
// per-engine fields keyed by engine id.
func engineListState(t *testing.T, c *WUIConnector) map[string]struct {
	Busy   bool `json:"busy"`
	System bool `json:"system"`
} {
	t.Helper()
	payload := c.buildEngineList()
	var msg struct {
		Engines []struct {
			ID     string `json:"id"`
			Busy   bool   `json:"busy"`
			System bool   `json:"system"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal engine_list: %v", err)
	}
	out := make(map[string]struct {
		Busy   bool `json:"busy"`
		System bool `json:"system"`
	}, len(msg.Engines))
	for _, e := range msg.Engines {
		out[e.ID] = struct {
			Busy   bool `json:"busy"`
			System bool `json:"system"`
		}{Busy: e.Busy, System: e.System}
	}
	return out
}

// TestBuildEngineList_BusyComputedFromEngineAtPullTime: busy is a single
// pull-time value read from the engine's own in-query state (IsBusy), never
// from connector event bookkeeping. A picker opened after query_end must see
// busy=false even though turn/tool events flowed moments earlier; an engine
// mid-query must read busy=true with zero event traffic.
func TestBuildEngineList_BusyComputedFromEngineAtPullTime(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2"})
	mgr.Add(&engine.EngineViewState{ID: "lazy", Name: "lazy"}) // restored, never materialized
	c, _ := bareConnectorA3(t, nil)
	c.mgr = mgr
	e2Mock, hubB := addMockEngine(t, c, "e2")

	busy := false
	e2Mock.isBusyFn = func() bool { return busy }

	// Idle baseline: no query ran, "lazy" has no engine behind it.
	if st := engineListState(t, c); st["e2"].Busy || st["main"].Busy || st["lazy"].Busy {
		t.Fatalf("idle baseline = %+v, want all idle", st)
	}

	// Query in flight: the engine reports busy — the list must carry it
	// with no event traffic at all (pull-time freshness, not bookkeeping).
	busy = true
	if st := engineListState(t, c); !st["e2"].Busy {
		t.Fatalf("engine reporting IsBusy must surface busy=true, got %+v", st["e2"])
	}

	// Query ended: stale turn events must NOT keep the row busy. The tool
	// block waits for the hub's async dispatch, so the events are provably
	// drained before the pull.
	hubB.Dispatch(types.QueryEvent{Type: types.EventTurnStart})
	hubB.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"}})
	if !waitFor(time.Second, func() bool { return streamStateCount(c, "e2") >= 1 }) {
		t.Fatal("timeout: turn events not drained")
	}
	busy = false
	if st := engineListState(t, c); st["e2"].Busy {
		t.Fatalf("busy must come from the engine, not turn-event bookkeeping: %+v", st["e2"])
	}
}

// TestBuildEngineList_NilEngineSlotIdle: a torn-down slot (engine already
// closed) must read busy=false, not panic the list build.
func TestBuildEngineList_NilEngineSlotIdle(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2"})
	c, _ := bareConnectorA3(t, nil)
	c.mgr = mgr
	c.slotsMu.Lock()
	c.slots["e2"] = &engineSlot{engineID: "e2", engine: nil}
	c.slotsMu.Unlock()
	if st := engineListState(t, c); st["e2"].Busy {
		t.Fatalf("nil-engine slot = %+v, want idle", st["e2"])
	}
}

// TestEngineListRequest_PullRoundtrip: the picker pulls on open — an
// engine_list_request over the wire must answer with an engine_list frame
// carrying the engines' busy state computed at that moment.
func TestEngineListRequest_PullRoundtrip(t *testing.T) {
	c, e2Mock, _, _ := deleteTestRig(t)
	e2Mock.isBusyFn = func() bool { return true }
	ws := dialAndStore(t, c)
	defer ws.Close()
	req, _ := json.Marshal(map[string]string{"type": "engine_list_request"})
	if err := ws.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write engine_list_request: %v", err)
	}

	raw := nextFrameOfType(t, ws, "engine_list")
	var list struct {
		Engines []struct {
			ID   string `json:"id"`
			Busy bool   `json:"busy"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal engine_list: %v", err)
	}
	busyByID := map[string]bool{}
	for _, e := range list.Engines {
		busyByID[e.ID] = e.Busy
	}
	if !busyByID["e2"] {
		t.Fatalf("e2 busy = false, want true (engine mid-query)")
	}
	if busyByID["main"] {
		t.Fatalf("main busy = true, want false (idle)")
	}
}

// TestBuildEngineList_SystemFlag: system engines (dream) are flagged so the
// picker can withhold the delete affordance.
func TestBuildEngineList_SystemFlag(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "dream", Name: "Dream", System: true})
	c, _ := bareConnectorA3(t, nil)
	c.mgr = mgr

	st := engineListState(t, c)
	if !st["dream"].System {
		t.Fatal("dream engine should carry system=true")
	}
	if st["main"].System {
		t.Fatal("main engine should carry system=false")
	}
}

// Equivalent mutants, on record: the mgr.Remove error branch in
// handleEngineDelete (connector.go) is unreachable under the handler's own
// pre-checks — not-found/system/readOnly/last-engine all return earlier and
// readLoop dispatch is serial, so Remove cannot fail between the count
// check and the call. Deleting the pre-check instead would let a
// last-engine delete CLOSE the engine before Remove rejects it (torn
// state), so the guard stays as defense-in-depth. Same class: the
// rows.Err() guard in short.DeleteSessionsByEngine (mid-iteration driver
// failure has no deterministic harness — the twin guard in
// CleanupOldSessions has no kill either).

// deleteTestRig builds a connector with a two-engine manager (main + e2),
// both slots mock-backed, plus a recording session-delete closure.
func deleteTestRig(t *testing.T) (*WUIConnector, *mockEngine, *engine.EngineManager, *[]string) {
	t.Helper()
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PersistMeta(dir); err != nil {
		t.Fatal(err)
	}
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	e2Mock := &mockEngine{projectDir: dir}
	e2Mock.projectDirFn = func() string { return dir }
	e2Slot := &engineSlot{
		engineID:    "e2",
		engine:      e2Mock,
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	c.slots["e2"] = e2Slot
	deleted := &[]string{}
	c.SetDeleteSessionsFn(func(engineID string) error {
		*deleted = append(*deleted, engineID)
		return nil
	})
	return c, e2Mock, mgr, deleted
}

func sendEngineDelete(t *testing.T, ws *websocket.Conn, engineID string) {
	t.Helper()
	msg, _ := json.Marshal(map[string]string{"type": "engine_delete", "engineID": engineID})
	if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write engine_delete: %v", err)
	}
}

// nextFrameOfType reads frames until one matches wantType (bounded — the
// handler under test pushes at most a couple of frames).
func nextFrameOfType(t *testing.T, ws *websocket.Conn, wantType string) json.RawMessage {
	t.Helper()
	for i := 0; i < 10; i++ {
		data := readWSMessage(t, ws)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil {
			continue
		}
		if head.Type == wantType {
			return json.RawMessage(data)
		}
	}
	t.Fatalf("no %q frame within 10 frames", wantType)
	return nil
}

func decodeScopedError(t *testing.T, raw json.RawMessage) (message string, scope string) {
	t.Helper()
	var frame struct {
		Message string `json:"message"`
		Scope   string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	return frame.Message, frame.Scope
}

func TestEngineDelete_UnknownEngineRejected(t *testing.T) {
	c, _, _, deleted := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "ghost")
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_delete" {
		t.Fatalf("scope = %q, want engine_delete", scope)
	}
	if msg == "" {
		t.Fatal("error message is empty")
	}
	if len(*deleted) != 0 {
		t.Fatalf("session deletion ran for unknown engine: %v", *deleted)
	}
}

func TestEngineDelete_MainEngineRejected(t *testing.T) {
	c, _, mgr, deleted := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "main")
	_, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_delete" {
		t.Fatalf("scope = %q, want engine_delete", scope)
	}
	if mgr.Count() != 2 {
		t.Fatalf("engines = %d, want 2 (main survives)", mgr.Count())
	}
	if len(*deleted) != 0 {
		t.Fatalf("session deletion ran for main: %v", *deleted)
	}
}

func TestEngineDelete_SystemEngineRejected(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "dream", Name: "Dream", System: true})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "dream")
	_, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_delete" {
		t.Fatalf("scope = %q, want engine_delete", scope)
	}
	if mgr.Get("dream") == nil {
		t.Fatal("dream engine must survive a delete request")
	}
}

func TestEngineDelete_LastEngineRejected(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "only", Name: "only"})
	// Drop main from the manager: "only" is the sole remaining engine.
	if err := mgr.Remove("main"); err != nil {
		t.Fatal(err)
	}
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "only")
	_, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_delete" {
		t.Fatalf("scope = %q, want engine_delete", scope)
	}
	if mgr.Count() != 1 {
		t.Fatalf("engines = %d, want 1 (last engine must survive)", mgr.Count())
	}
}

func TestEngineDelete_NormalRemovesEverything(t *testing.T) {
	c, e2Mock, mgr, deleted := deleteTestRig(t)
	e2Mock.isBusyFn = func() bool { return true }
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "e2")

	raw := nextFrameOfType(t, ws, "engine_list")
	var list struct {
		Engines []struct {
			ID string `json:"id"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal engine_list: %v", err)
	}
	if len(list.Engines) != 1 || list.Engines[0].ID != "main" {
		t.Fatalf("engine_list = %+v, want only main", list.Engines)
	}

	if mgr.Get("e2") != nil {
		t.Fatal("e2 still in manager after delete")
	}
	c.slotsMu.RLock()
	_, slotLeft := c.slots["e2"]
	c.slotsMu.RUnlock()
	if slotLeft {
		t.Fatal("e2 slot still registered after delete")
	}
	if !waitFor(time.Second, func() bool { return e2Mock.recordedAborts() == 1 }) {
		t.Fatalf("busy engine aborts = %d, want 1", e2Mock.recordedAborts())
	}
	if len(*deleted) != 1 || (*deleted)[0] != "e2" {
		t.Fatalf("session deletion calls = %v, want [e2]", *deleted)
	}

	// meta.json must drop e2 — a stale entry would try to resume deleted
	// sessions on the next boot (ghost-id precedent).
	metaBytes, err := os.ReadFile(filepath.Join(e2Mock.projectDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if strings.Contains(string(metaBytes), "agent-2") {
		t.Fatal("meta.json still references the deleted engine")
	}
}

func TestEngineDelete_ActiveEngineFallsBackAndPushesMetadata(t *testing.T) {
	c, _, mgr, deleted := deleteTestRig(t)
	if err := mgr.SetActive("e2"); err != nil {
		t.Fatal(err)
	}
	activeID := "e2"
	c.active.Store(&activeID)
	c.slots["main"].active.Store(false)
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "e2")

	raw := nextFrameOfType(t, ws, "metadata")
	var meta struct {
		Engines struct {
			Engines []struct {
				ID string `json:"id"`
			} `json:"engines"`
			ActiveID string `json:"activeID"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if len(meta.Engines.Engines) != 1 || meta.Engines.Engines[0].ID != "main" {
		t.Fatalf("metadata engines = %+v, want only main", meta.Engines.Engines)
	}
	if meta.Engines.ActiveID != "main" {
		t.Fatalf("metadata activeID = %q, want main (fallback)", meta.Engines.ActiveID)
	}
	if mgr.ActiveID() != "main" {
		t.Fatalf("mgr.ActiveID() = %q, want main", mgr.ActiveID())
	}
	if c.ActiveID() != "main" {
		t.Fatalf("connector active = %q, want main", c.ActiveID())
	}
	if len(*deleted) != 1 || (*deleted)[0] != "e2" {
		t.Fatalf("session deletion calls = %v, want [e2]", *deleted)
	}
}

// queryEndSink records query_end error texts for Close assertions.
type queryEndSink struct {
	events chan string
}

func (s queryEndSink) Handle(ev hub.Event) {
	if ev.Type == types.EventQueryEnd && ev.Error != nil {
		select {
		case s.events <- ev.Error.Error():
		default:
		}
	}
}

// TestBuildEngineList_LazyEntriesIdle: engines restored from meta.json that
// were never switched to have no process behind them (Engine nil, no slot) —
// they must read busy=false, never crash the list build.
func TestBuildEngineList_LazyEntriesIdle(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "lone", Name: "lone"})
	c, _ := bareConnectorA3(t, nil)
	c.mgr = mgr
	// "lone" has NO slot and NO Engine: an unmaterialized view state.
	st := engineListState(t, c)
	if st["lone"].Busy {
		t.Fatalf("lone = %+v, want idle (no engine behind the view state)", st["lone"])
	}
}

func TestBuildScopedError(t *testing.T) {
	var frame struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Scope   string `json:"scope"`
	}
	if err := json.Unmarshal(buildScopedError(engineDeleteScope, nil), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "error" || frame.Message != "unknown error" || frame.Scope != "engine_delete" {
		t.Fatalf("nil-error frame = %+v", frame)
	}
	if err := json.Unmarshal(buildScopedError(engineDeleteScope, errors.New("boom")), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Message != "boom" {
		t.Fatalf("message = %q, want boom", frame.Message)
	}
}

func TestEngineDelete_NilManagerRejected(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	c.handleEngineDelete("e2")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 || !strings.Contains(frames[0], "engine manager unavailable") {
		t.Fatalf("frames = %v, want manager-unavailable scoped error", frames)
	}
	if !strings.Contains(frames[0], `"scope":"engine_delete"`) {
		t.Fatalf("frame lacks engine_delete scope: %s", frames[0])
	}
}

func TestEngineDelete_ReadOnlyEngineRejected(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "wx", Name: "wechat-1", ReadOnly: true})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "wx")
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_delete" {
		t.Fatalf("scope = %q, want engine_delete", scope)
	}
	if msg != "engine \"wechat-1\" is driven by an external connector and cannot be deleted" {
		t.Fatalf("message = %q", msg)
	}
	if mgr.Get("wx") == nil {
		t.Fatal("read-only engine must survive a delete request")
	}
}

func TestEngineDelete_LastEngineExactMessage(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "only", Name: "only"})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "only")
	msg, _ := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if msg != "must keep at least one engine" {
		t.Fatalf("message = %q, want the pre-check wording (not mgr.Remove's backstop)", msg)
	}
}

func TestEngineDelete_NoAbortWhenIdle(t *testing.T) {
	c, e2Mock, mgr, _ := deleteTestRig(t)
	c.handleEngineDelete("e2")
	want := 0
	if got := e2Mock.recordedAborts(); got != want {
		t.Fatalf("idle engine aborts = %d, want %d", got, want)
	}
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should still be deleted")
	}
}

func TestEngineDelete_EngineWithoutSlot(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	// No slot for "slotless", but the view state carries a real engine with
	// a project dir — the registerEngine-before-slot shape. PersistMeta must
	// fall back to it: a no-op persist would leave the engine in meta.json
	// and restart would resurrect an engine whose sessions are gone.
	store, err := short.NewStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	slotless := engine.New(&engine.Params{EngineID: "slotless", Model: "m"})
	slotless.SetDispatcher(hub.NewHub())
	slotless.SetStore(store, dir)
	mgr.Add(&engine.EngineViewState{ID: "slotless", Name: "slotless", Engine: slotless})
	if err := mgr.PersistMeta(dir); err != nil {
		t.Fatal(err)
	}
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	c.handleEngineDelete("slotless")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 || !strings.Contains(frames[0], `"id":"main"`) {
		t.Fatalf("frames = %v, want a single engine_list with main only", frames)
	}
	if mgr.Get("slotless") != nil {
		t.Fatal("slotless engine still in manager")
	}
	metaBytes, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if strings.Contains(string(metaBytes), "slotless") {
		t.Fatal("meta.json still references the slotless engine: a restart would resurrect it")
	}
}

func TestEngineDelete_SlotWithNilEngine(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2"})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	c.slots["e2"] = &engineSlot{
		engineID:    "e2",
		engine:      nil,
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	c.handleEngineDelete("e2")
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should be deleted despite a nil-engine slot")
	}
}

func TestEngineDelete_ClosesRealEngine(t *testing.T) {
	dir := t.TempDir()
	realEng := engine.New(&engine.Params{EngineID: "e2"})
	h := hub.NewHub()
	realEng.SetDispatcher(h)
	events := make(chan string, 4)
	unsub := h.Subscribe(queryEndSink{events: events})
	defer unsub()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2", Engine: realEng})
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	c.slots["e2"] = &engineSlot{
		engineID:    "e2",
		engine:      &engineAdapter{eng: realEng},
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	c.handleEngineDelete("e2")
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should be deleted")
	}
	// A closed engine refuses new queries with a query_end error — the
	// observable proof that Close ran.
	realEng.Query(context.Background(), "hello", "")
	select {
	case msg := <-events:
		if msg != "engine: query on closed engine" {
			t.Fatalf("post-Close query error = %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no query_end after querying the deleted engine")
	}
}

func TestEngineDelete_UnsubscribesSlot(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	unsubCalled := false
	c.slotsMu.Lock()
	c.slots["e2"].unsubscribe = func() { unsubCalled = true }
	c.slotsMu.Unlock()
	c.handleEngineDelete("e2")
	if !unsubCalled {
		t.Fatal("slot teardown must unsubscribe from the engine hub")
	}
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should be deleted")
	}
}

func TestEngineDelete_PersistMetaFailureStillDeletes(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	// A FILE where the project dir should be: WriteWorkspaceMeta must fail,
	// the handler warns and continues.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.mock().projectDirFn = func() string { return t.TempDir() }
	c.slotsMu.RLock()
	e2 := c.slots["e2"]
	c.slotsMu.RUnlock()
	e2Mock2, _ := e2.engine.(*mockEngine)
	e2Mock2.projectDirFn = func() string { return blocker }

	sink := a1SwapDefaultLogger(t)
	c.handleEngineDelete("e2")
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should be deleted despite PersistMeta failing")
	}
	if sink.count("wui:engine_delete:PersistMeta failed") == 0 {
		t.Fatal("PersistMeta failure must be warned")
	}
	frames := takeAllFramesA3(c)
	if len(frames) != 1 || !strings.Contains(frames[0], "engine_list") {
		t.Fatalf("frames = %v, want the engine_list push to survive the persist failure", frames)
	}
}

func TestEngineDelete_WithoutSessionFnStillDeletes(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2"})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	e2Mock := &mockEngine{projectDir: dir}
	e2Mock.projectDirFn = func() string { return dir }
	c.slots["e2"] = &engineSlot{engineID: "e2", engine: e2Mock, hub: h, taskToolIDs: make(map[string]bool)}
	// No SetDeleteSessionsFn: nil closure must be tolerated.
	c.handleEngineDelete("e2")
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should be deleted without a session-deletion closure")
	}
}

func TestEngineDelete_SessionCleanupFailureScopesErrorToClient(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	c.SetDeleteSessionsFn(func(string) error { return errors.New("disk on fire") })
	sink := a1SwapDefaultLogger(t)
	// WS-based, not direct-call: direct-call frame assertions race the
	// wsWriter goroutine the rig spawns (a conn-less writer DROPS frames it
	// takes), and two frames back-to-back lose that race under -race.
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "e2")
	// The UI row already vanished optimistically; a silent failure strands
	// orphan sessions until age cleanup. The scoped error rides the same
	// engine_delete channel the rejections use, AFTER the fresh list so the
	// client's picker restore cannot flash the deleted row back.
	rawList := nextFrameOfType(t, ws, "engine_list")
	if strings.Contains(string(rawList), "agent-2") {
		t.Fatalf("engine_list = %s, want the shrunken list without e2", rawList)
	}
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	// The error frame trails the warn in the handler, so its arrival is the
	// sync point for both the dispatch and the warn.
	if sink.count("wui:engine_delete:session cleanup failed") == 0 {
		t.Fatal("session cleanup failure must be warned")
	}
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should be deleted despite the session cleanup failing")
	}
	if scope != "engine_delete" {
		t.Fatalf("scope = %q, want engine_delete", scope)
	}
	if msg != "session cleanup failed: disk on fire" {
		t.Fatalf("message = %q, want the cleanup failure surfaced", msg)
	}
}

func TestEngineDelete_SessionCleanupFailureAfterActiveFallback(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	c.SetDeleteSessionsFn(func(string) error { return errors.New("disk on fire") })
	if err := mgr.SetActive("e2"); err != nil {
		t.Fatal(err)
	}
	activeID := "e2"
	c.active.Store(&activeID)
	c.slots["main"].active.Store(false)
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineDelete(t, ws, "e2")
	// The wasActive path returns early after switchEngine's metadata push —
	// the cleanup receipt must still go out, after the metadata. Frame reads
	// double as the sync point for the async readLoop dispatch.
	nextFrameOfType(t, ws, "metadata")
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if mgr.Get("e2") != nil {
		t.Fatal("e2 should be deleted despite the session cleanup failing")
	}
	if scope != "engine_delete" {
		t.Fatalf("scope = %q, want engine_delete", scope)
	}
	if msg != "session cleanup failed: disk on fire" {
		t.Fatalf("message = %q, want the cleanup failure surfaced", msg)
	}
}

func TestEngineDelete_ActiveFallbackWithoutSlotPushesList(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2"})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	e2Mock := &mockEngine{projectDir: dir}
	e2Mock.projectDirFn = func() string { return dir }
	c.slots["e2"] = &engineSlot{engineID: "e2", engine: e2Mock, hub: h, taskToolIDs: make(map[string]bool)}
	activeID := "e2"
	c.active.Store(&activeID)
	// Remove the fallback ("main") slot: active-engine delete degrades to
	// the bare engine_list push instead of switchEngine's metadata.
	c.slotsMu.Lock()
	delete(c.slots, "main")
	c.slotsMu.Unlock()
	c.handleEngineDelete("e2")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 || !strings.Contains(frames[0], `"type":"engine_list"`) {
		t.Fatalf("frames = %v, want a single engine_list push", frames)
	}
	if mgr.ActiveID() != "main" {
		t.Fatalf("mgr.ActiveID() = %q, want main", mgr.ActiveID())
	}
}

func TestEngineDelete_EmptyIDEIsIgnoredByRouting(t *testing.T) {
	c, _, _, _ := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()
	emptyMsg, _ := json.Marshal(map[string]string{"type": "engine_delete", "engineID": ""})
	if err := ws.WriteMessage(websocket.TextMessage, emptyMsg); err != nil {
		t.Fatal(err)
	}
	// The empty-id frame must be dropped by routing; the next delete's
	// error is the FIRST frame the client sees.
	sendEngineDelete(t, ws, "ghost")
	msg, _ := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if msg != "unknown engine: ghost" {
		t.Fatalf("first error frame = %q, want the ghost one (empty id was routed)", msg)
	}
}
