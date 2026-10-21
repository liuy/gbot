package wui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// TestApplyJobLifecycleEvent_ImmediateFrame pins the event fast path: a job
// lifecycle event dispatched through the hub pushes its job_list frame at
// once — no tail beat involved (the tail loop is disabled for the test's
// lifetime). started builds the row from the event shell; terminated updates
// status/exitCode; an identical repeat is diff-silent.
func TestApplyJobLifecycleEvent_ImmediateFrame(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	startedMs := time.Now().UnixMilli() // REAL-TIME

	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "bg-1", Kind: "bash", Status: "running", Command: "make check", StartedMs: startedMs,
	}})
	out := readJobList(t, ws)
	if len(out.Jobs) != 1 {
		t.Fatalf("job_list after started = %d rows, want 1", len(out.Jobs))
	}
	row := out.Jobs[0]
	if row.ID != "bg-1" || row.Kind != "bash" || row.Status != "running" {
		t.Errorf("row after started = {id:%s kind:%s status:%s}, want {bg-1 bash running}", row.ID, row.Kind, row.Status)
	}
	if row.Command != "make check" {
		t.Errorf("row Command = %q, want make check", row.Command)
	}
	if row.StartedMs != startedMs {
		t.Errorf("row StartedMs = %d, want %d (shell field parity)", row.StartedMs, startedMs)
	}

	h.Dispatch(types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "bg-1", Kind: "bash", Status: "killed", ExitCode: 137, Command: "make check", StartedMs: startedMs,
	}})
	out = readJobList(t, ws)
	if len(out.Jobs) != 1 {
		t.Fatalf("job_list after terminated = %d rows, want 1", len(out.Jobs))
	}
	row = out.Jobs[0]
	if row.Status != "killed" || row.ExitCode != 137 {
		t.Errorf("row after terminated = {status:%s exit:%d}, want {killed 137}", row.Status, row.ExitCode)
	}

	// Terminated for a row the panel never saw creates it from the FULL
	// event shell — every wire field must survive the conversion.
	h.Dispatch(types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "agent-9", Kind: "agent", Status: "failed", ExitCode: 3,
		Description: "migrated job", AgentType: "Explore", ParentToolUseID: "tu_m", StartedMs: 77,
	}})
	out = readJobList(t, ws)
	var migrated *jobWireItem
	for i := range out.Jobs {
		if out.Jobs[i].ID == "agent-9" {
			migrated = &out.Jobs[i]
		}
	}
	if migrated == nil {
		t.Fatalf("job_list after unknown-row terminated has no agent-9 row: %+v", out.Jobs)
	}
	if migrated.Kind != "agent" || migrated.Status != "failed" || migrated.ExitCode != 3 {
		t.Errorf("migrated row = {kind:%s status:%s exit:%d}, want {agent failed 3}", migrated.Kind, migrated.Status, migrated.ExitCode)
	}
	if migrated.Description != "migrated job" || migrated.AgentType != "Explore" || migrated.ParentToolUseID != "tu_m" {
		t.Errorf("migrated row shell fields = {desc:%q agentType:%q parent:%q}, want full shell parity",
			migrated.Description, migrated.AgentType, migrated.ParentToolUseID)
	}
	if migrated.StartedMs != 77 {
		t.Errorf("migrated row StartedMs = %d, want 77", migrated.StartedMs)
	}

	// Identical shell again: the diff baseline swallows it — no new frame.
	h.Dispatch(types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "bg-1", Kind: "bash", Status: "killed", ExitCode: 137, Command: "make check", StartedMs: startedMs,
	}})
	assertNoFrameWithin(t, ws, 300*time.Millisecond)
}

// TestApplyJobLifecycleEvent_NeverEvictsRunning pins the fast path's capacity
// discipline: an event-built row over the cap evicts only the oldest TERMINAL
// rows; running rows (including the event's own) always survive.
func TestApplyJobLifecycleEvent_NeverEvictsRunning(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)

	slot := c.activeSlotTest(t)
	slot.ssMu.Lock()
	for i := 1; i <= 200; i++ {
		id := fmt.Sprintf("bg-%03d", i)
		slot.jobView[id] = &jobViewEntry{item: jobWireItem{
			ID: id, Kind: "bash", Status: "completed", Command: "old", StartedMs: int64(1000 + i),
		}}
	}
	slot.jobView["bg-run"] = &jobViewEntry{item: jobWireItem{
		ID: "bg-run", Kind: "bash", Status: "running", Command: "live", StartedMs: 5000,
	}}
	slot.ssMu.Unlock()

	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "bg-new", Kind: "bash", Status: "running", Command: "fresh", StartedMs: 6000,
	}})
	out := readJobList(t, ws)
	if len(out.Jobs) != 200 {
		t.Fatalf("job_list rows = %d, want 200 (cap enforced)", len(out.Jobs))
	}
	ids := map[string]bool{}
	running := 0
	for _, j := range out.Jobs {
		ids[j.ID] = true
		if j.Status == "running" {
			running++
		}
	}
	if running != 2 || !ids["bg-run"] || !ids["bg-new"] {
		t.Errorf("running rows = %d (bg-run:%v bg-new:%v), want both running rows present", running, ids["bg-run"], ids["bg-new"])
	}
	if ids["bg-001"] || ids["bg-002"] {
		t.Error("the two oldest terminal rows must be the evicted ones (bg-001, bg-002)")
	}
	if !ids["bg-200"] {
		t.Error("newest terminal row must survive the cap eviction")
	}
}

// TestOnEngineEvent_JobEventNilPayloadIgnored: a lifecycle event without its
// payload is dropped silently — no frame, no panic. Called directly on
// onEngineEvent (not through the hub) so a removed guard panics in the test
// instead of being swallowed by Dispatch's per-handler recover.
func TestOnEngineEvent_JobEventNilPayloadIgnored(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	c.onEngineEvent("main", types.QueryEvent{Type: types.EventJobStarted})
	c.onEngineEvent("main", types.QueryEvent{Type: types.EventJobTerminated})

	assertNoFrameWithin(t, ws, 300*time.Millisecond)
}

// TestOnEngineEvent_JobEventNotForwardedAsEventFrame is the wire-compat
// anchor: job lifecycle events are consumed by the panel branch and never
// marshaled into the generic event frame — browsers only ever see job_list.
func TestOnEngineEvent_JobEventNotForwardedAsEventFrame(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)

	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running", AgentType: "Explore", StartedMs: 42,
	}})
	out := readJobList(t, ws)
	if len(out.Jobs) != 1 || out.Jobs[0].ID != "agent-1" {
		t.Fatalf("job_list = %+v, want the agent-1 row", out.Jobs)
	}

	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
	defer func() { _ = ws.SetReadDeadline(time.Time{}) }()
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return // window elapsed with no further frames
		}
		var head struct {
			Type  string `json:"type"`
			Event *struct {
				Type string `json:"type"`
			} `json:"event"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == "event" &&
			head.Event != nil && strings.HasPrefix(head.Event.Type, "job_") {
			t.Fatalf("job lifecycle event forwarded as generic event frame: %s", string(data))
		}
	}
}
