package wui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// dialJobsServer dials without draining — the caller wants the takeover
// metadata frame itself (dialAndStore discards it).
func dialJobsServer(t *testing.T, c *WUIConnector) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
	srv := httptest.NewServer(mux)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	t.Cleanup(srv.Close)
	return ws
}

// readMetadataJobs reads until the metadata frame and returns its embedded
// jobs entries, or nil when the metadata carries no jobs field. Non-metadata
// frames along the way are skipped: an event-driven job_list can slip into
// the new era in the window between sendMetadata's queue swap and its
// metadata push (harmless ordering — metadata is the authoritative snapshot).
func readMetadataJobs(t *testing.T, ws *websocket.Conn) []jobWireItemHydrated {
	t.Helper()
	raw := readUntilType(t, ws, "metadata")
	var head struct {
		Jobs json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatalf("unmarshal metadata frame: %v", err)
	}
	if len(head.Jobs) == 0 || string(head.Jobs) == "null" {
		return nil
	}
	var env jobsMetadataOutbound
	if err := json.Unmarshal(head.Jobs, &env); err != nil {
		t.Fatalf("unmarshal metadata jobs: %v", err)
	}
	return env.Jobs
}

// readJobList reads until a job_list frame and decodes it.
func readJobList(t *testing.T, ws *websocket.Conn) jobListOutbound {
	t.Helper()
	raw := readUntilType(t, ws, "job_list")
	var out jobListOutbound
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	return out
}

// readJobOutput reads until a job_output frame and decodes it.
func readJobOutput(t *testing.T, ws *websocket.Conn) jobOutputOutbound {
	t.Helper()
	raw := readUntilType(t, ws, "job_output")
	var out jobOutputOutbound
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal job_output: %v", err)
	}
	return out
}

// assertNoFrameWithin fails if any frame arrives within window. Used with a
// window spanning multiple delta opportunities: unchanged state must stay
// silent.
func assertNoFrameWithin(t *testing.T, ws *websocket.Conn, window time.Duration) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(window)) // REAL-TIME
	_, data, err := ws.ReadMessage()
	if err == nil {
		t.Fatalf("expected no frame within %v, got %q", window, string(data))
	}
	_ = ws.SetReadDeadline(time.Time{})
}

func bgBashJob(id, status, command string, startedMs int64) *job.JobInfo {
	return &job.JobInfo{
		ID:           id,
		Type:         "bash",
		Status:       status,
		Command:      command,
		Backgrounded: true,
		StartedMs:    startedMs,
	}
}

// dispatchBashStarted pushes a bash job row through the real event path so a
// tail-loop test starts from a panel that knows the job (rows are
// event-driven; the registry no longer feeds the live panel).
func dispatchBashStarted(h *hub.Hub, id, command string, startedMs int64) {
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: id, Kind: "bash", Status: "running", Command: command, StartedMs: startedMs,
	}})
}

// dispatchBashTailDelta pushes a job output snapshot through the real event
// path (the shape bootstrap's emitJob produces for background bash output).
func dispatchBashTailDelta(h *hub.Hub, id, tail string) {
	h.Dispatch(types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		Agent:      &types.AgentMeta{JobID: id},
		ToolResult: &types.ToolResultEvent{ToolUseID: id, DisplayOutput: tail},
	})
}

// TestJobOutputDelta_PushesTailAndSkipsEventFrame pins the routing contract:
// a bash tail delta reaches browsers ONLY as a job_output frame — never as a
// generic event frame (wire compat: browsers consume job_list/job_output).
func TestJobOutputDelta_PushesTailAndSkipsEventFrame(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "make check", time.Now().UnixMilli()) // REAL-TIME

	dispatchBashTailDelta(h, "bg-1", "a\nb\n")
	out := readJobOutput(t, ws)
	if out.JobID != "bg-1" || out.Tail != "a\nb\n" {
		t.Fatalf("job_output = {%s %q}, want {bg-1 \"a\\nb\\n\"}", out.JobID, out.Tail)
	}
	// The delta must not leak a generic event frame after the job_output.
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestJobOutputDelta_DiffGate: an identical snapshot re-dispatched is
// diff-silent — exactly one job_output ever reaches the wire.
func TestJobOutputDelta_DiffGate(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "quiet cmd", time.Now().UnixMilli()) // REAL-TIME

	dispatchBashTailDelta(h, "bg-1", "same\n")
	out := readJobOutput(t, ws)
	if out.JobID != "bg-1" || out.Tail != "same\n" {
		t.Fatalf("job_output = {%s %q}, want {bg-1 \"same\\n\"}", out.JobID, out.Tail)
	}
	dispatchBashTailDelta(h, "bg-1", "same\n")
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestJobOutputDelta_ChangedTailPushes: each changed snapshot pushes its own
// job_output frame, and a stable tail never repeats.
func TestJobOutputDelta_ChangedTailPushes(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "make check", time.Now().UnixMilli()) // REAL-TIME

	dispatchBashTailDelta(h, "bg-1", "a\n")
	first := readJobOutput(t, ws)
	if first.JobID != "bg-1" || first.Tail != "a\n" {
		t.Fatalf("first job_output = {%s %q}, want {bg-1 \"a\\n\"}", first.JobID, first.Tail)
	}
	dispatchBashTailDelta(h, "bg-1", "a\nb\n")
	second := readJobOutput(t, ws)
	if second.JobID != "bg-1" || second.Tail != "a\nb\n" {
		t.Fatalf("second job_output = {%s %q}, want {bg-1 \"a\\nb\\n\"}", second.JobID, second.Tail)
	}
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestJobOutputDelta_InactiveSlotCacheWarm pins the push gate: a non-active
// slot's bash row keeps its tail cache warm (the delta lands in the entry)
// but nothing ever reaches the wire.
func TestJobOutputDelta_InactiveSlotCacheWarm(t *testing.T) {
	c := newTestConnector(t)

	// A second, fully wired slot with a bash row, but NOT active: its tail
	// must never reach the wire.
	h := hub.NewHub()
	mockB := &mockEngine{}
	slotB := &engineSlot{
		engineID:    "engineB",
		engine:      mockB,
		hub:         h,
		taskToolIDs: map[string]bool{},
		jobView:     map[string]*jobViewEntry{},
		streams:     map[string]*streamState{"": {}},
	}
	slotB.unsubscribe = h.Subscribe(&engineHubShim{engineID: "engineB", c: c})
	c.slotsMu.Lock()
	c.slots["engineB"] = slotB
	c.slotsMu.Unlock()
	ws := dialAndStore(t, c)
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "bg-1", Kind: "bash", Status: "running", Command: "build", StartedMs: time.Now().UnixMilli(), // REAL-TIME
	}})
	dispatchBashTailDelta(h, "bg-1", "warm tail\n")

	assertNoFrameWithin(t, ws, 150*time.Millisecond)
	slotB.ssMu.Lock()
	var tail string
	if entry := slotB.jobView["bg-1"]; entry != nil {
		tail = entry.tail
	}
	slotB.ssMu.Unlock()
	if tail != "warm tail\n" {
		t.Fatalf("inactive slot cache tail = %q, want %q (cache warm without push)", tail, "warm tail\n")
	}
}

func TestJobStop_KillsAndPushes(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "make check", time.Now().UnixMilli()) // REAL-TIME

	readJobList(t, ws) // warm the view so the forced push has content
	// The mock keeps reporting bg-1 as running after the kill, so the ack
	// frame's payload equals the baseline — only pushJobsSnapshot's "\x00"
	// sentinel reset forces it through. A readJobList timeout below means
	// the sentinel was lost.
	sendJSON(t, ws, map[string]string{"type": "job_stop", "jobID": "bg-1"})
	out := readJobList(t, ws)

	m := c.mock()
	m.mu.RLock()
	calls := append([]string(nil), m.killJobCalls...)
	m.mu.RUnlock()
	if len(calls) != 1 || calls[0] != "bg-1" {
		t.Fatalf("killJobCalls = %v, want [bg-1]", calls)
	}
	if len(out.Jobs) != 1 || out.Jobs[0].ID != "bg-1" {
		t.Errorf("post-stop job_list = %+v, want the bg-1 entry", out.Jobs)
	}
}

func TestJobStop_KillErrorSendsErrorFrame(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetKillJobFn(func(id string) error {
		return errors.New("no such job")
	})

	ws := dialAndStore(t, c)
	sendJSON(t, ws, map[string]string{"type": "job_stop", "jobID": "bg-404"})
	raw := readUntilType(t, ws, "error")
	var errFrame struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &errFrame); err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if !strings.Contains(errFrame.Message, "no such job") {
		t.Errorf("error message = %q, want it to contain %q", errFrame.Message, "no such job")
	}
}

func TestMetadata_EmbedsJobsWithBlocks(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID:              "agent-1",
			Type:            "agent",
			Status:          "running",
			Description:     "explore the repo",
			AgentType:       "Explore",
			ParentToolUseID: "toolu_parent",
			Backgrounded:    true,
			StartedMs:       1234,
		}}
	})
	ws := dialJobsServer(t, c)
	drainInitialFrames(t, ws) // barrier: activation completed before the dispatch
	// job_started creates the stream region (anchor = the shell's parent id)
	// before the job's process events arrive; the started frame is the row's
	// own event push.
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		Description: "explore the repo", AgentType: "Explore",
		ParentToolUseID: "toolu_parent", StartedMs: 1234,
	}})
	readJobList(t, ws)
	parent := &types.AgentMeta{ParentToolUseID: "toolu_parent", JobID: "agent-1"}
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: parent,
		ToolUse: &types.ToolUseEvent{ID: "toolu_inner", Name: "Bash", Summary: "echo hi"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "agent says "})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "hello"})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, Agent: parent,
		ToolResult: &types.ToolResultEvent{ToolUseID: "toolu_inner"}})

	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	it := items[0]
	if it.ID != "agent-1" || it.Kind != "agent" || it.AgentType != "Explore" || it.ParentToolUseID != "toolu_parent" {
		t.Errorf("hydrated item = %+v, want agent-1/agent/Explore/toolu_parent", it.jobWireItem)
	}
	if it.Description != "explore the repo" {
		t.Errorf("hydrated description = %q, want %q", it.Description, "explore the repo")
	}
	var toolBlock *streamBlock
	var textBlock *streamBlock
	for i := range it.Blocks {
		switch it.Blocks[i].Kind {
		case "tool":
			toolBlock = &it.Blocks[i]
		case "text":
			textBlock = &it.Blocks[i]
		}
	}
	if toolBlock == nil {
		t.Fatalf("no tool block in hydrated blocks: %+v", it.Blocks)
	}
	if toolBlock.ID != "toolu_inner" || toolBlock.Name != "Bash" || toolBlock.State != "done" {
		t.Errorf("tool block = {id:%s name:%s state:%s}, want {toolu_inner Bash done}", toolBlock.ID, toolBlock.Name, toolBlock.State)
	}
	if textBlock == nil || textBlock.Text != "agent says hello" {
		t.Errorf("text block = %+v, want text %q", textBlock, "agent says hello")
	}
}

func TestStreams_MaskingAndNesting(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "running",
			Description: "nested test", ParentToolUseID: "toolu_p",
			Backgrounded: true, StartedMs: 100,
		}}
	})
	// Row + region via the lifecycle event first, then process events —
	// production ordering (Spawn emits started before the job's first turn).
	ws := dialJobsServer(t, c)
	drainInitialFrames(t, ws) // barrier: activation completed before the dispatch
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		Description: "nested test", ParentToolUseID: "toolu_p", StartedMs: 100,
	}})
	readJobList(t, ws)

	// Job's own events (parent == toolu_p) → masked to the region root.
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: &types.AgentMeta{ParentToolUseID: "toolu_p", JobID: "agent-1"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_outer", Name: "Bash", Summary: "outer"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_p", JobID: "agent-1"}, Text: "outer text"})
	// Nested agent events (parent == an inner tool id) → children, no masking.
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: &types.AgentMeta{ParentToolUseID: "toolu_outer", JobID: "agent-1"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_deep", Name: "Grep", Summary: "deep"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_deep", JobID: "agent-1"}, Text: "deep text"})
	// No-JobID event (sync sub-agent somewhere else): chat-only, never any job region.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_unrelated"}, Text: "stray"})
	// Different JobID with no region yet: dropped — must not pollute agent-1's.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_other", JobID: "agent-2"}, Text: "other job text"})

	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	blocks := items[0].Blocks
	if len(blocks) != 2 {
		t.Fatalf("root blocks len = %d, want 2 (tool + text): %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != "tool" || blocks[0].ID != "toolu_outer" {
		t.Fatalf("blocks[0] = {%s %s}, want tool toolu_outer", blocks[0].Kind, blocks[0].ID)
	}
	if len(blocks[0].Children) != 1 || blocks[0].Children[0].ID != "toolu_deep" {
		t.Fatalf("outer children = %+v, want [toolu_deep]", blocks[0].Children)
	}
	deep := blocks[0].Children[0]
	if len(deep.Children) != 1 || deep.Children[0].Kind != "text" || deep.Children[0].Text != "deep text" {
		t.Fatalf("deep children = %+v, want [text \"deep text\"]", deep.Children)
	}
	if blocks[1].Kind != "text" || blocks[1].Text != "outer text" {
		t.Fatalf("blocks[1] = {%s %q}, want text \"outer text\"", blocks[1].Kind, blocks[1].Text)
	}
	for _, b := range blocks {
		if strings.Contains(b.Text, "stray") || strings.Contains(b.Summary, "stray") {
			t.Errorf("no-JobID event leaked into the job region: %+v", b)
		}
		if strings.Contains(b.Text, "other job") || strings.Contains(b.Summary, "other job") {
			t.Errorf("foreign-JobID event leaked into the job region: %+v", b)
		}
	}
	// The foreign job gets its region from its own started event and its
	// event lands there (single delivery per job).
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-2", Kind: "agent", Status: "running", ParentToolUseID: "toolu_other", StartedMs: 101,
	}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_other", JobID: "agent-2"}, Text: "other job text"})
	slot := c.slots["main"]
	slot.ssMu.Lock()
	foreign := slot.streams["agent-2"]
	var foreignBlocks []streamBlock
	if foreign != nil {
		foreignBlocks = append([]streamBlock(nil), foreign.blocks...)
	}
	slot.ssMu.Unlock()
	if foreign == nil || len(foreignBlocks) != 1 || foreignBlocks[0].Text != "other job text" {
		t.Fatalf("agent-2 region blocks = %+v, want exactly [text %q]", foreignBlocks, "other job text")
	}
}

// Events can arrive before any registry enumeration sees the job: job_started
// holds the region from the spawn moment, so discovery finds it already
// populated.
func TestCaptureJobEvent_FeedExistsBeforeDiscovery(t *testing.T) {
	c := newTestConnector(t)
	agentJob := &job.JobInfo{
		ID: "agent-1", Type: "agent", Status: "running",
		Description: "late discovery", ParentToolUseID: "toolu_late",
		Backgrounded: true, StartedMs: 100,
	}
	// started precedes the job's process events (production ordering).
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		Description: "late discovery", ParentToolUseID: "toolu_late", StartedMs: 100,
	}})
	parent := &types.AgentMeta{ParentToolUseID: "toolu_late", JobID: "agent-1"}
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: parent,
		ToolUse: &types.ToolUseEvent{ID: "toolu_x", Name: "Read", Summary: "read file"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "flushed text"})
	// The job is enumerated by the registry only after the events were folded
	// (discovery = the dial's takeover reconcile).
	c.mock().SetJobsFn(func() []*job.JobInfo { return []*job.JobInfo{agentJob} })

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	if len(items[0].Blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2 (tool + text folded before discovery): %+v", len(items[0].Blocks), items[0].Blocks)
	}
	if items[0].Blocks[0].Kind != "tool" || items[0].Blocks[0].ID != "toolu_x" {
		t.Errorf("blocks[0] = %+v, want tool toolu_x", items[0].Blocks[0])
	}
	if items[0].Blocks[1].Kind != "text" || items[0].Blocks[1].Text != "flushed text" {
		t.Errorf("blocks[1] = %+v, want text %q", items[0].Blocks[1], "flushed text")
	}
}

// Fork jobs are agent-family: the panel's family rule is "not bash", so a
// fork-kind entry must get a live feed exactly like an agent-kind one, and
// must not be routed down the bash tail path.
func TestCaptureJobEvent_FeedExistsBeforeDiscovery_ForkKind(t *testing.T) {
	c := newTestConnector(t)
	forkJob := &job.JobInfo{
		ID: "agent-1", Type: "fork", Status: "running",
		Description: "fork feed", ParentToolUseID: "toolu_fk",
		Backgrounded: true, StartedMs: 100,
	}
	// started creates the fork's region (fork is agent-family).
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "fork", Status: "running",
		Description: "fork feed", ParentToolUseID: "toolu_fk", StartedMs: 100,
	}})
	parent := &types.AgentMeta{ParentToolUseID: "toolu_fk", JobID: "agent-1"}
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "fork text"})
	c.mock().SetJobsFn(func() []*job.JobInfo { return []*job.JobInfo{forkJob} })

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	if items[0].Kind != "fork" {
		t.Errorf("wire kind = %q, want fork (JobInfo.Type rides the wire verbatim)", items[0].Kind)
	}
	if len(items[0].Blocks) != 1 {
		t.Fatalf("fork feed blocks len = %d, want 1 — fork kind must own a feed: %+v", len(items[0].Blocks), items[0].Blocks)
	}
	if items[0].Blocks[0].Kind != "text" || items[0].Blocks[0].Text != "fork text" {
		t.Errorf("blocks[0] = %+v, want text %q", items[0].Blocks[0], "fork text")
	}
	if items[0].Tail != "" {
		t.Errorf("fork Tail = %q, want empty (tail fetching is bash-only)", items[0].Tail)
	}
}

// TestReconcile_JobViewCapEvictsUntilAtCap pins the cap on the reconcile
// path (takeover): a registry snapshot over the cap evicts the OLDEST
// TERMINAL rows until at cap; each eviction logs the wui:jobs line.
func TestReconcile_JobViewCapEvictsUntilAtCap(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := make([]*job.JobInfo, 0, 201)
		for i := 1; i <= 201; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "completed", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		return jobs
	})
	rec := mutDCaptureSlog(t)

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 200 {
		t.Fatalf("jobs len = %d, want 200 (capped)", len(items))
	}
	ids := map[string]bool{}
	for _, j := range items {
		ids[j.ID] = true
	}
	if ids["bg-001"] {
		t.Error("bg-001 (smallest startedMs) must be evicted, but is present")
	}
	if !ids["bg-201"] {
		t.Error("bg-201 (largest startedMs) must survive the cap, but is absent")
	}
	if rec.count(slog.LevelInfo, "wui:jobs: view over cap, evicting oldest terminal") != 1 {
		t.Fatal("the single over-cap eviction must log the wui:jobs eviction line exactly once")
	}
}

// TestReconcile_CapNeverEvictsRunning pins that the reconcile cap's victim
// scan is restricted to terminal rows: a running row survives even when the
// view is over cap.
func TestReconcile_CapNeverEvictsRunning(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := make([]*job.JobInfo, 0, 201)
		for i := 1; i <= 200; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "completed", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		jobs = append(jobs, bgBashJob("bg-run", "running", "live cmd", 5000))
		return jobs
	})

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 200 {
		t.Fatalf("jobs len = %d, want 200 (199 terminal + 1 running)", len(items))
	}
	ids := map[string]bool{}
	running := 0
	for _, j := range items {
		ids[j.ID] = true
		if j.Status == "running" {
			running++
		}
	}
	if running != 1 || !ids["bg-run"] {
		t.Errorf("running job must never be evicted: present=%v running=%d", ids["bg-run"], running)
	}
	if ids["bg-001"] {
		t.Error("bg-001 (smallest terminal startedMs) must be the evicted one, but is present")
	}
	if !ids["bg-002"] || !ids["bg-200"] {
		t.Error("newer terminals must survive: bg-002 and bg-200 expected present")
	}
}

// TestMetadata_TakeoverRefreshesTailsViaJobTail pins the takeover hydration:
// rows with no event history (reconcile-born) take a one-time JobTail
// snapshot at takeover, while an event-fed tail wins over the registry
// snapshot (fill-empty-only).
func TestMetadata_TakeoverRefreshesTailsViaJobTail(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	startedMs := time.Now().UnixMilli() // REAL-TIME
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", startedMs)}
	})
	c.mock().SetJobTailFn(func(id string) (string, bool) {
		if id != "bg-1" {
			return "", false
		}
		return "registry-tail", true
	})

	// Part 1 — reconcile-born row (no event history): the takeover snapshot
	// hydrates the tail.
	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 || items[0].Tail != "registry-tail" {
		t.Fatalf("metadata jobs = %+v, want one bg-1 entry with the takeover tail %q", items, "registry-tail")
	}

	// Part 2 — event-fed row wins: the delta warmed the entry, so the later
	// registry snapshot must not overwrite it.
	dispatchBashStarted(h, "bg-1", "make check", startedMs)
	dispatchBashTailDelta(h, "bg-1", "event-tail")
	ws2 := dialJobsServer(t, c)
	items = readMetadataJobs(t, ws2)
	if len(items) != 1 || items[0].Tail != "event-tail" {
		t.Fatalf("post-delta metadata jobs = %+v, want one bg-1 entry with the event-fed tail %q", items, "event-tail")
	}
}

// TestStreams_FeedCapDropsHead pins the 400-block region cap. Only
// non-merging blocks can reach it: consecutive text deltas fold into one text
// block, tool_start blocks never merge.
func TestStreams_FeedCapDropsHead(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "running",
			Description: "cap test", ParentToolUseID: "toolu_cap",
			Backgrounded: true, StartedMs: 100,
		}}
	})
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		Description: "cap test", ParentToolUseID: "toolu_cap", StartedMs: 100,
	}})
	for i := 1; i <= 401; i++ {
		c.Handle(types.QueryEvent{Type: types.EventToolStart,
			Agent:   &types.AgentMeta{ParentToolUseID: "toolu_cap", JobID: "agent-1"},
			ToolUse: &types.ToolUseEvent{ID: fmt.Sprintf("toolu_%03d", i), Name: "Bash", Summary: fmt.Sprintf("step %d", i)}})
	}

	ws := dialJobsServer(t, c)
	readMetadataJobs(t, ws) // consume the takeover frame: connection fully live
	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	blocks := items[0].Blocks
	if len(blocks) != jobFeedMaxBlocks {
		t.Fatalf("blocks len = %d, want %d", len(blocks), jobFeedMaxBlocks)
	}
	if blocks[0].Summary != "step 2" {
		t.Errorf("head block summary = %q, want %q (first dropped)", blocks[0].Summary, "step 2")
	}
	if blocks[jobFeedMaxBlocks-1].Summary != "step 401" {
		t.Errorf("tail block summary = %q, want %q", blocks[jobFeedMaxBlocks-1].Summary, "step 401")
	}
}

func TestSessionSwitch_ClearsTerminalJobs(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	var jobsMu sync.Mutex
	terminalVisible := true
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		if terminalVisible {
			return []*job.JobInfo{
				bgBashJob("bg-1", "completed", "old session cmd", 1000),
				bgBashJob("bg-2", "running", "live cmd", 2000),
			}
		}
		return []*job.JobInfo{bgBashJob("bg-2", "running", "live cmd", 2000)}
	})
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "old session cmd", 1000)
	h.Dispatch(types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "bg-1", Kind: "bash", Status: "completed", Command: "old session cmd", StartedMs: 1000,
	}})
	dispatchBashStarted(h, "bg-2", "live cmd", 2000)
	first := readJobList(t, ws)
	if len(first.Jobs) != 2 {
		t.Fatalf("initial jobs len = %d, want 2", len(first.Jobs))
	}

	// The old session's jobs leave the registry with the session itself; the
	// frozen terminal entry would survive without the explicit clear — and
	// with the registry still listing it, the takeover reconcile would simply
	// re-add it.
	jobsMu.Lock()
	terminalVisible = false
	jobsMu.Unlock()
	sendJSON(t, ws, map[string]string{"type": "session_switch", "sessionID": "other-session"})
	items := readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("post-switch metadata jobs = %+v, want only the running entry", items)
	}
	if items[0].ID != "bg-2" || items[0].Status != "running" {
		t.Errorf("post-switch job = {%s %s}, want {bg-2 running}", items[0].ID, items[0].Status)
	}
}

// TestEngineAdapter_KillJobAndJobTail exercises the real engine wiring: the
// adapter routes through ToolRefs into the per-engine registries.
func TestEngineAdapter_KillJobAndJobTail(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	refs := engine.CreateTools(engine.SharedDeps{}, task.NewList(t.TempDir()))
	eng.SetToolRefs(refs)

	out := bash.NewStreamingOutput(nil)
	bt := refs.BashReg.Spawn("echo hi", 0, out)
	if _, err := out.Write([]byte("hello tail\n")); err != nil {
		t.Fatalf("write output: %v", err)
	}

	a := &engineAdapter{eng: eng}

	tail, ok := a.JobTail(bt.ID)
	if !ok {
		t.Fatalf("JobTail(%s) not found", bt.ID)
	}
	if tail != "hello tail" {
		t.Errorf("JobTail = %q, want %q", tail, "hello tail")
	}

	if err := a.KillJob(bt.ID); err != nil {
		t.Fatalf("KillJob(%s): %v", bt.ID, err)
	}
	var status string
	for _, info := range a.JobSummaries() {
		if info.ID == bt.ID {
			status = info.Status
		}
	}
	if status != "killed" {
		t.Errorf("post-kill status = %q, want killed", status)
	}

	if _, ok := a.JobTail("bg-9999"); ok {
		t.Error("JobTail on unknown id must return ok=false")
	}
	err := a.KillJob("bg-9999")
	if err == nil {
		t.Fatal("KillJob on unknown id must return an error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("KillJob unknown-id error = %v, want it to mention 'not found'", err)
	}
}

// blockingSubagentEngine stands in for a real sub-engine on the spawn path:
// RunAgent blocks until the context is cancelled (the kill), so a background
// spawn stays running for the duration of the assertions with no LLM provider.
type blockingSubagentEngine struct{}

func (blockingSubagentEngine) RunAgent(ctx context.Context, _ agent.AgentOpts) (*types.SubQueryResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// wireJobsEngine builds one fully wired engine on the shared deps: real
// CreateTools toolset, WireEngine callbacks, ToolRefs installed for the
// engineAdapter to route through.
func wireJobsEngine(t *testing.T, deps engine.SharedDeps, id string) (*engine.Engine, engine.ToolRefs) {
	t.Helper()
	eng := engine.New(&engine.Params{EngineID: id, Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	refs := engine.CreateTools(deps, task.NewList(t.TempDir()))
	eng.SetToolRefs(refs)
	engine.WireEngine(eng, refs, deps)
	return eng, refs
}

// waitKilledRow polls the owning adapter's job list until id reports killed —
// the cancel→status flip happens on the spawn goroutine, so it is asynchronous
// — then pins that the very next list drops the row (the Jobs() call that saw
// the terminal state already ran the adapter's lazy cleanup).
func waitKilledRow(t *testing.T, a *engineAdapter, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded poll for async kill
	for {
		status := ""
		found := false
		for _, info := range a.Jobs() {
			if info.ID == id {
				status, found = info.Status, true
			}
		}
		if found && status == "killed" {
			break
		}
		if !found {
			t.Fatalf("job %q vanished from the owner's list before ever reporting killed", id)
		}
		if time.Now().After(deadline) { // REAL-TIME: bounded poll for async kill
			t.Fatalf("job %q status = %q after kill (2s deadline), want killed", id, status)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
	}
	for _, info := range a.Jobs() {
		if info.ID == id {
			t.Errorf("job %q still listed after the killed row was observed — terminal cleanup must drop it", id)
		}
	}
}

func jobInfoIDs(jobs []*job.JobInfo) []string {
	ids := make([]string, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	return ids
}

// TestEngineAdapter_BgTaskOwnership pins the bg-jobs panel's ownership
// contract through the real engine wiring (two engines, one shared
// EngineMgr/IDMinter): the panel is per-engine — engine B neither lists
// engine A's background agents nor can its kill path reach them — and a
// sub-spawned task, routed into the session root's table at spawn time,
// surfaces on the root engine's panel only, still stoppable from there.
func TestEngineAdapter_BgTaskOwnership(t *testing.T) {
	deps := engine.SharedDeps{EngineMgr: engine.NewEngineManager(), IDMinter: agent.NewIDMinter()}
	engA, refsA := wireJobsEngine(t, deps, "engine-a")
	engB, _ := wireJobsEngine(t, deps, "engine-b")
	refsA.Agent.SetEngine(blockingSubagentEngine{})

	aA := &engineAdapter{eng: engA}
	aB := &engineAdapter{eng: engB}

	// (a) Isolation: a bg agent spawned by A exists for A alone.
	res, err := refsA.Agent.Call(context.Background(),
		json.RawMessage(`{"description":"d","prompt":"p","run_in_background":true}`),
		&tool.ToolUseContext{ToolUseID: "t1"})
	if err != nil {
		t.Fatalf("Agent.Call: %v", err)
	}
	sub, ok := res.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("Call data type = %T, want *types.SubQueryResult", res.Data)
	}
	id := sub.AgentID
	if id == "" {
		t.Fatal("spawn returned empty AgentID")
	}
	t.Cleanup(func() { _ = refsA.Agent.Registry().Cancel(id) })

	aJobs := aA.Jobs()
	if len(aJobs) != 1 || aJobs[0].ID != id {
		t.Fatalf("A's jobs = %v, want exactly [%s]", jobInfoIDs(aJobs), id)
	}
	if aJobs[0].Status != "running" {
		t.Errorf("A's job status = %q, want running", aJobs[0].Status)
	}
	if bJobs := aB.Jobs(); len(bJobs) != 0 {
		t.Errorf("B's jobs = %v, want none — engine B must not see engine A's bg task", jobInfoIDs(bJobs))
	}
	if err := aB.KillJob(id); !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("B's KillJob(%q) error = %v, want ErrNotFound", id, err)
	}

	if err := aA.KillJob(id); err != nil {
		t.Fatalf("A's KillJob(%q): %v", id, err)
	}
	waitKilledRow(t, aA, id)

	// (b) Root-table ownership: a sub-spawned task lands in the session
	// root's table at spawn time — in production via RunAgent's routing
	// (mechanism pinned by pkg/engine's acceptance test 2); spawned directly
	// on A's registry here, this test pins the panel consequence: the row
	// surfaces on A's panel (and only A's) and A's kill reaches it.
	orphan, err := refsA.Agent.Registry().Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil, "orphan work", "toolu_orphan", "Explore")
	if err != nil {
		t.Fatalf("spawn orphan: %v", err)
	}
	t.Cleanup(func() { _ = refsA.Agent.Registry().Cancel(orphan.ID) })

	aJobs = aA.Jobs()
	if len(aJobs) != 1 || aJobs[0].ID != orphan.ID {
		t.Fatalf("A's jobs after the root-table spawn = %v, want exactly [%s]", jobInfoIDs(aJobs), orphan.ID)
	}
	if aJobs[0].Status != "running" {
		t.Errorf("orphan status on A's panel = %q, want running", aJobs[0].Status)
	}
	if bJobs := aB.Jobs(); len(bJobs) != 0 {
		t.Errorf("B's jobs after the root-table spawn = %v, want none — the root's bg task must surface only on the root engine", jobInfoIDs(bJobs))
	}
	if err := aB.KillJob(orphan.ID); !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("B's KillJob(%q) error = %v, want ErrNotFound", orphan.ID, err)
	}

	if err := aA.KillJob(orphan.ID); err != nil {
		t.Fatalf("A's KillJob(%q): %v", orphan.ID, err)
	}
	waitKilledRow(t, aA, orphan.ID)
	if bJobs := aB.Jobs(); len(bJobs) != 0 {
		t.Errorf("B's jobs after the orphan kill = %v, want none", jobInfoIDs(bJobs))
	}
}

// TestJobWire_UnknownTypePassthrough pins that JobInfo.Type rides the wire
// verbatim: with the unified field there is no mapper left, so a type outside
// the bash/agent/fork vocabulary reaches the client unchanged instead of
// collapsing to "".
func TestJobWire_UnknownTypePassthrough(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "x-1", Kind: "custom_kind", Status: "running", StartedMs: 42,
	}})

	out := readJobList(t, ws)
	if len(out.Jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(out.Jobs))
	}
	if out.Jobs[0].Kind != "custom_kind" {
		t.Errorf("kind = %q, want the raw type %q", out.Jobs[0].Kind, "custom_kind")
	}
}

// TestJobOutputDelta_NilViewSlotNoPanic pins the nil-map guard: slots built
// directly (bypassing registerEngine) have no jobs maps, and a routed delta
// on such a slot must be a silent no-op, not a nil-map write panic.
func TestJobOutputDelta_NilViewSlotNoPanic(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	ghost := &engineSlot{engineID: "ghost", engine: c.mock()}
	c.slotsMu.Lock()
	c.slots["ghost"] = ghost
	c.slotsMu.Unlock()

	c.onEngineEvent("ghost", types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		Agent:      &types.AgentMeta{JobID: "bg-1"},
		ToolResult: &types.ToolResultEvent{ToolUseID: "bg-1", DisplayOutput: "x"},
	})

	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestApplyJobStreamEvent_NilViewNoPanic pairs with
// TestJobTick_NilViewSlotNoPanic for the event path: a JobID-carrying event
// on a map-less slot must create no region and panic nowhere.
func TestApplyJobStreamEvent_NilViewNoPanic(t *testing.T) {
	c := newTestConnector(t)
	ghost := &engineSlot{engineID: "ghost", hub: hub.NewHub()}
	c.applyJobStreamEvent(ghost, types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_x", JobID: "agent-1"}, Text: "stray"})
}

// TestJobOutputDelta_AgentFamilyNotIntercepted pins the bash-row filter:
// agent-family deltas keep the generic stream path (their event frames feed
// the browser job feeds) and never produce a job_output.
func TestJobOutputDelta_AgentFamilyNotIntercepted(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running", ParentToolUseID: "tu_p", StartedMs: 100,
	}})
	readJobList(t, ws)

	dispatchBashTailDelta(h, "agent-1", "agent tail")

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("no frame after the agent-family delta: %v", err)
	}
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &head) != nil || head.Type != "event" {
		t.Fatalf("frame after the agent-family delta = %s, want a generic event frame", string(data))
	}
}

// TestJobOutputDelta_GuardClauses pins the interception branch's operand
// discrimination: only tool_output_delta events with a non-nil Agent tag
// carrying a JobID may route to the tail path. Everything else — foreground
// deltas (Agent nil), sync sub-agent deltas (JobID empty, even when the
// ToolUseID matches a bash row), and payload-less deltas — keeps the generic
// stream path.
func TestJobOutputDelta_GuardClauses(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "make check", time.Now().UnixMilli()) // REAL-TIME
	readJobList(t, ws)

	// Foreground delta (Agent nil): generic path — event frame, never routed.
	h.Dispatch(types.QueryEvent{Type: types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tool_fg", DisplayOutput: "fg"}})
	readUntilType(t, ws, "event")

	// Sync sub-agent delta (Agent non-nil, JobID empty) whose ToolUseID
	// matches a bash row: still generic — the JobID is the routing key.
	h.Dispatch(types.QueryEvent{Type: types.EventToolOutputDelta,
		Agent:      &types.AgentMeta{ParentToolUseID: "toolu_p"},
		ToolResult: &types.ToolResultEvent{ToolUseID: "bg-1", DisplayOutput: "sync"}})
	readUntilType(t, ws, "event")

	// Payload-less delta: no ToolResult to route by — generic path.
	h.Dispatch(types.QueryEvent{Type: types.EventToolOutputDelta,
		Agent: &types.AgentMeta{JobID: "bg-1"}})
	readUntilType(t, ws, "event")

	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestReconcile_CapRunningOldestNeverEvicted pins that the reconcile cap's
// victim scan is restricted to TERMINAL entries: a running job with the
// smallest StartedMs must survive while the oldest terminal is the one
// evicted.
func TestReconcile_CapRunningOldestNeverEvicted(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := []*job.JobInfo{bgBashJob("bg-run", "running", "oldest but live", 500)}
		for i := 1; i <= 200; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "completed", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		return jobs
	})

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 200 {
		t.Fatalf("jobs len = %d, want 200 (capped)", len(items))
	}
	ids := map[string]bool{}
	for _, j := range items {
		ids[j.ID] = true
	}
	if !ids["bg-run"] {
		t.Error("running job with smallest startedMs was evicted — the victim scan must skip running entries")
	}
	if ids["bg-001"] {
		t.Error("bg-001 (smallest terminal startedMs) must be the evicted one, but is present")
	}
}

// TestReconcile_CapAllRunningRetainsAll pins the cap's escape hatch: with no
// terminal entries to evict, the loop gives up and the over-cap view is
// embedded in full — a live job is never dropped and the loop never spins.
func TestReconcile_CapAllRunningRetainsAll(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := make([]*job.JobInfo, 0, 201)
		for i := 1; i <= 201; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "running", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		return jobs
	})

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 201 {
		t.Fatalf("jobs len = %d, want 201 (all running, cap cannot evict)", len(items))
	}
	running := 0
	for _, j := range items {
		if j.Status == "running" {
			running++
		}
	}
	if running != 201 {
		t.Errorf("running count = %d, want 201", running)
	}
}

// TestReconcile_NilViewAndRegionGuards kills the guard mutants around the
// reconcile's region creation: a nil jobView slot must be a no-op (not a nil
// map write), a nil streams map must not panic, bash rows never get a
// region, and an already-present region's blocks survive the reconcile's
// first sighting (never overwrite).
func TestReconcile_NilViewAndRegionGuards(t *testing.T) {
	c := &WUIConnector{}
	summaries := []*job.JobInfo{
		bgBashJob("bg-1", "running", "cmd", 10),
		{ID: "agent-1", Type: "agent", Status: "running", Description: "d",
			ParentToolUseID: "tu_a", Backgrounded: true, StartedMs: 20},
	}
	now := time.Now() // REAL-TIME

	// nil jobView: no-op (the panic on a nil map write is the kill signal).
	c.reconcileJobsFromRegistry(&engineSlot{}, summaries, now)

	// nil streams: rows are built, the region-creation gate must skip — the
	// mutant that drops the nil check writes to a nil map and panics.
	bare := &engineSlot{jobView: map[string]*jobViewEntry{}}
	c.reconcileJobsFromRegistry(bare, summaries, now)
	if _, ok := bare.jobView["bg-1"]; !ok {
		t.Fatal("reconcile skipped the bash row entirely")
	}
	if _, ok := bare.jobView["agent-1"]; !ok {
		t.Fatal("reconcile skipped the agent row entirely")
	}

	// Region rules: bash never gets one; an existing region (events folded
	// under an id whose row was never built — the missed-started case)
	// survives the first sighting untouched.
	slot := &engineSlot{
		jobView: map[string]*jobViewEntry{},
		streams: map[string]*streamState{
			"":        {},
			"agent-1": {feedAnchor: "tu_pre", blocks: []streamBlock{{Text: "kept"}}},
		},
	}
	c.reconcileJobsFromRegistry(slot, summaries, now)
	if slot.streams["bg-1"] != nil {
		t.Error("bash row got a stream region — regions are agent-family only")
	}
	region := slot.streams["agent-1"]
	if region == nil || len(region.blocks) != 1 || region.blocks[0].Text != "kept" || region.feedAnchor != "tu_pre" {
		t.Errorf("existing region overwritten by the reconcile's first sighting: %+v", region)
	}
}

// TestCaptureJobFrames_EmptyViewGate kills the operand-removal mutants on
// the always-empty gate: an always-empty view captures NOTHING (a client
// that never had jobs hears silence), while an empty view over a non-empty
// baseline still captures — the transition to empty must push the clearing
// frame and settle the baseline on it.
func TestCaptureJobFrames_EmptyViewGate(t *testing.T) {
	c := &WUIConnector{}
	c.activeWS.Store(&websocket.Conn{})
	slot := mutDSlot()
	slot.active.Store(true)

	if snap := c.captureJobFramesLocked(slot); snap != nil {
		t.Fatalf("always-empty view captured %+v, want nil (silence)", snap)
	}
	slot.lastJobsPush = `{"type":"job_list","jobs":[{"id":"gone"}]}`
	snap := c.captureJobFramesLocked(slot)
	if snap == nil {
		t.Fatal("empty view over a non-empty baseline must capture — the clearing frame is the transition signal")
	}
	frames := c.renderJobFrames(slot, snap)
	if len(frames) != 1 || !strings.Contains(frames[0], `"jobs":[]`) {
		t.Fatalf("clearing frames = %v, want exactly one empty job_list frame", frames)
	}
	slot.ssMu.Lock()
	settled := slot.lastJobsPush
	slot.ssMu.Unlock()
	if settled != frames[0] {
		t.Errorf("baseline settled on %q, want the clearing frame %q", settled, frames[0])
	}
}

// sortedJobsFixture mints started-event shells whose startedMs order and ID
// order disagree, plus a 2-way and a 4-way startedMs tie so both sort keys
// are exercised (the 4-way tie also makes an always-false tiebreak comparator
// survive only ~2% of runs rather than ~25%). wantOrder is the (startedMs
// asc, ID asc) expectation.
func sortedJobsFixture() ([]jobWireItem, []string) {
	specs := []struct {
		id string
		ms int64
	}{
		{"bg-e", 1000}, {"bg-h", 1000},
		{"bg-a", 2000}, {"bg-b", 2000}, {"bg-c", 2000}, {"bg-d", 2000},
		{"bg-j", 5000}, {"bg-i", 6000},
	}
	items := make([]jobWireItem, 0, len(specs))
	for _, s := range specs {
		items = append(items, jobWireItem{ID: s.id, Kind: "bash", Status: "running", Command: "cmd " + s.id, StartedMs: s.ms})
	}
	return items, []string{"bg-e", "bg-h", "bg-a", "bg-b", "bg-c", "bg-d", "bg-j", "bg-i"}
}

// dispatchJobRows pushes one started event per fixture row.
func dispatchJobRows(h *hub.Hub, items []jobWireItem) {
	for _, it := range items {
		h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
			ID: it.ID, Kind: it.Kind, Status: it.Status, Command: it.Command, StartedMs: it.StartedMs,
		}})
	}
}

// TestJobList_SortedByStartedMsThenID pins the job_list ordering: startedMs
// ascending, ID ascending as the tiebreak. jobView is a map, so without the
// sort the order would be random; the fixture's ID-vs-started disagreement
// also separates the startedMs key from an ID-only sort. Each started event
// pushes its own diff frame, so frames are read until one carries them all.
func TestJobList_SortedByStartedMsThenID(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	items, wantOrder := sortedJobsFixture()
	ws := dialAndStore(t, c)
	dispatchJobRows(h, items)

	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	var out jobListOutbound
	for time.Now().Before(deadline) { // REAL-TIME
		out = readJobList(t, ws)
		if len(out.Jobs) == len(wantOrder) {
			break
		}
	}
	if len(out.Jobs) != len(wantOrder) {
		t.Fatalf("jobs len = %d, want %d", len(out.Jobs), len(wantOrder))
	}
	for i, want := range wantOrder {
		if out.Jobs[i].ID != want {
			t.Errorf("jobs[%d] = %s, want %s (full order: %v)", i, out.Jobs[i].ID, want, jobIDs(out.Jobs))
		}
	}
}

// TestMetadata_JobsSortedByStartedMsThenID pins the same ordering contract
// for the metadata hydration snapshot (a separate sort call site).
func TestMetadata_JobsSortedByStartedMsThenID(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	items, wantOrder := sortedJobsFixture()
	// Registry backing for the running rows: an unlisted running row is a
	// vanish-drop at the dial's reconcile.
	summaries := make([]*job.JobInfo, 0, len(items))
	for _, it := range items {
		summaries = append(summaries, &job.JobInfo{
			ID: it.ID, Type: it.Kind, Status: it.Status,
			Command: it.Command, Backgrounded: true, StartedMs: it.StartedMs,
		})
	}
	c.mock().SetJobsFn(func() []*job.JobInfo { return summaries })
	dispatchJobRows(h, items)

	ws := dialJobsServer(t, c)
	got := readMetadataJobs(t, ws)
	if len(got) != len(wantOrder) {
		t.Fatalf("metadata jobs len = %d, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("metadata jobs[%d] = %s, want %s (full order: %v)", i, got[i].ID, want, hydratedIDs(got))
		}
	}
}

func jobIDs(jobs []jobWireItem) []string {
	ids := make([]string, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	return ids
}

func hydratedIDs(items []jobWireItemHydrated) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

// TestJobOutputDelta_FramesSortedOnActivation pins the job_output send order:
// pending tails accumulated while inactive flush in ascending job id order on
// the first capture after activation.
func TestJobOutputDelta_FramesSortedOnActivation(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ids := []string{"bg-d", "bg-a", "bg-c", "bg-b"}
	summaries := make([]*job.JobInfo, 0, len(ids))
	for i, id := range ids {
		summaries = append(summaries, bgBashJob(id, "running", "cmd "+id, int64(1000+i)))
	}
	// Registry backing keeps the running rows past the dial's reconcile.
	c.mock().SetJobsFn(func() []*job.JobInfo { return summaries })

	// Rows + tails accumulate while no client is connected (capture is nil,
	// caches warm).
	for i, id := range ids {
		dispatchBashStarted(h, id, "cmd "+id, int64(1000+i))
		dispatchBashTailDelta(h, id, "tail "+id)
	}
	ws := dialAndStore(t, c) // activate: metadata hydrates the rows
	dispatchBashTailDelta(h, "bg-b", "tail bg-b v2")

	want := []string{"bg-a", "bg-b", "bg-c", "bg-d"}
	for _, wantID := range want {
		out := readJobOutput(t, ws)
		if out.JobID != wantID {
			t.Fatalf("job_output order broke at %q, want %q", out.JobID, wantID)
		}
		wantTail := "tail " + wantID
		if wantID == "bg-b" {
			wantTail = "tail bg-b v2"
		}
		if out.Tail != wantTail {
			t.Fatalf("job_output tail for %s = %q, want %q", wantID, out.Tail, wantTail)
		}
	}
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestJobOutputDelta_StableTailDoesNotBlockLaterJobs pins the tail-diff scan:
// a bash entry whose tail is unchanged must be skipped (continue), not abort
// the capture scan (break) — later jobs with changed tails still get their
// frame.
func TestJobOutputDelta_StableTailDoesNotBlockLaterJobs(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "quiet cmd", 1000)
	dispatchBashStarted(h, "bg-2", "loud cmd", 2000)
	readJobList(t, ws)

	// Warm both baselines (id order).
	dispatchBashTailDelta(h, "bg-1", "stable")
	dispatchBashTailDelta(h, "bg-2", "first")
	for _, want := range []string{"bg-1", "bg-2"} {
		out := readJobOutput(t, ws)
		if out.JobID != want {
			t.Fatalf("warm-up frame = %s, want %s", out.JobID, want)
		}
	}

	// Identical bg-1 snapshot again + changed bg-2: exactly one frame.
	dispatchBashTailDelta(h, "bg-1", "stable")
	dispatchBashTailDelta(h, "bg-2", "second")
	out := readJobOutput(t, ws)
	if out.JobID != "bg-2" || out.Tail != "second" {
		t.Fatalf("post-flip frame = {%s %q}, want {bg-2 \"second\"}", out.JobID, out.Tail)
	}
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestPushJobsSnapshot_NilViewNoFrame pins the nil-view guard of the forced
// push: a map-less (test-built) slot must push nothing, even though the
// sentinel baseline reset would otherwise force an empty job_list out.
func TestPushJobsSnapshot_NilViewNoFrame(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	ghost := &engineSlot{engineID: "ghost", engine: c.mock()}
	ghost.active.Store(true)
	c.pushJobsSnapshot(ghost)
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

// TestMetadata_EmptyViewOmitsJobsField pins the empty-view guard: a slot with
// no jobs must omit the metadata jobs field entirely (nil raw JSON), not emit
// an empty {"jobs":[]} envelope.
func TestMetadata_EmptyViewOmitsJobsField(t *testing.T) {
	c := newTestConnector(t)

	ws := dialJobsServer(t, c)
	if items := readMetadataJobs(t, ws); items != nil {
		t.Errorf("metadata jobs = %+v, want the field omitted entirely (nil)", items)
	}
}

// TestSessionNew_ClearsTerminalJobs pins the session_new handler's defensive
// clear: the previous session's terminal entries must not leak into the new
// session's metadata snapshot; running entries belong to the engine and stay.
func TestSessionNew_ClearsTerminalJobs(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	var jobsMu sync.Mutex
	terminalVisible := true
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		if terminalVisible {
			return []*job.JobInfo{
				bgBashJob("bg-1", "completed", "old session cmd", 1000),
				bgBashJob("bg-2", "running", "live cmd", 2000),
			}
		}
		return []*job.JobInfo{bgBashJob("bg-2", "running", "live cmd", 2000)}
	})
	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "old session cmd", 1000)
	h.Dispatch(types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "bg-1", Kind: "bash", Status: "completed", Command: "old session cmd", StartedMs: 1000,
	}})
	dispatchBashStarted(h, "bg-2", "live cmd", 2000)
	first := readJobList(t, ws)
	if len(first.Jobs) != 2 {
		t.Fatalf("initial jobs len = %d, want 2", len(first.Jobs))
	}

	// Same registry turn-over as the switch test: the old session's terminal
	// row leaves the registry, or the takeover reconcile legitimately
	// re-lists it.
	jobsMu.Lock()
	terminalVisible = false
	jobsMu.Unlock()
	sendJSON(t, ws, map[string]string{"type": "session_new"})
	items := readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("post-new metadata jobs = %+v, want only the running entry", items)
	}
	if items[0].ID != "bg-2" || items[0].Status != "running" {
		t.Errorf("post-new job = {%s %s}, want {bg-2 running}", items[0].ID, items[0].Status)
	}
}

// TestJobStop_EmptyJobIDIgnored pins the inbound validation: a job_stop with
// an empty jobID must be dropped before the handler — no kill call, no ack
// frame, no error frame.
func TestJobStop_EmptyJobIDIgnored(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	ws := dialAndStore(t, c)
	dispatchBashStarted(h, "bg-1", "make check", time.Now().UnixMilli()) // REAL-TIME
	readJobList(t, ws)                                                   // warm the view

	sendJSON(t, ws, map[string]string{"type": "job_stop", "jobID": ""})
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
	m := c.mock()
	m.mu.RLock()
	calls := append([]string(nil), m.killJobCalls...)
	m.mu.RUnlock()
	if len(calls) != 0 {
		t.Errorf("killJobCalls = %v, want none for an empty jobID", calls)
	}
}

// TestEngineAdapter_NilRegistries pins the adapter's nil-registry guards:
// KillJob reports the specific error, JobTail reports not-found, and
// JobSummaries returns nil — none of them dereference the missing registry.
func TestEngineAdapter_NilRegistries(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "nil-refs", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetToolRefs(engine.ToolRefs{}) // every registry pointer nil
	a := &engineAdapter{eng: eng}

	err := a.KillJob("bg-1")
	if err == nil {
		t.Fatal("KillJob with nil JobReg must return an error")
	}
	if !strings.Contains(err.Error(), "job registry not available") {
		t.Errorf("KillJob error = %v, want it to mention 'job registry not available'", err)
	}
	if tail, ok := a.JobTail("bg-1"); ok || tail != "" {
		t.Errorf("JobTail with nil BashReg = (%q, %v), want (\"\", false)", tail, ok)
	}
	if got := a.JobSummaries(); got != nil {
		t.Errorf("JobSummaries with nil JobReg = %+v, want nil", got)
	}
}

// TestRegisterEngine_SlotFullyWiredForJobs exercises the registerEngine slot
// literal end to end through real engines: the jobs maps exist (lifecycle,
// feed creation and capture — nil maps would panic), taskToolIDs accepts Task
// calls, the bash job's output deltas reach the wire through the event line
// (registry → engine → hub → panel), and the slot name/system drive
// BusyReport (running job named, system engine excluded).
func TestRegisterEngine_SlotFullyWiredForJobs(t *testing.T) {
	mgr := engine.NewEngineManager()
	h1 := hub.NewHub()
	eng1 := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng1.SetDispatcher(h1)
	eng1.SetToolRefs(engine.ToolRefs{})
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng1})
	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	// e2: named engine with a running bg bash job and a running bg agent.
	// The bash registry's OnJobEvent is wired BEFORE the spawn: the job's
	// delta line copies the registry callback at Spawn time. Same conversion
	// branch as bootstrap's WireEngine.
	h2 := hub.NewHub()
	eng2 := engine.New(&engine.Params{EngineID: "e2", Model: "test-model"})
	eng2.SetDispatcher(h2)
	bashReg := bash.NewBashRegistry()
	agentReg := agent.NewAgentRegistry()
	jobReg := job.NewMultiRegistry(bash.NewJobInfoAdapter(bashReg), agent.NewBackgroundAgentJobAdapter(agentReg))
	eng2.SetToolRefs(engine.ToolRefs{BashReg: bashReg, JobReg: jobReg})
	bashReg.OnJobEvent = engine.JobEventEmitter(eng2)
	bgOut := bash.NewStreamingOutput(nil)
	bt := bashReg.Spawn("sleep 30", 0, bgOut)
	t.Cleanup(func() { _ = bashReg.Kill(bt.ID) })
	state, err := agentReg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil, "explore wiring", "toolu_p", "Explore")
	if err != nil {
		t.Fatalf("spawn bg agent: %v", err)
	}
	t.Cleanup(func() { _ = agentReg.Cancel(state.ID) })
	c.RegisterEngine(&engine.EngineViewState{ID: "e2", Name: "agent-2", Engine: eng2})

	// e3: SYSTEM engine with a running job — BusyReport must exclude it.
	e3Bash := bash.NewBashRegistry()
	bt3 := e3Bash.Spawn("sleep 30", 0, bash.NewStreamingOutput(nil))
	t.Cleanup(func() { _ = e3Bash.Kill(bt3.ID) })
	h3 := hub.NewHub()
	eng3 := engine.New(&engine.Params{EngineID: "e3", Model: "test-model"})
	eng3.SetDispatcher(h3)
	eng3.SetToolRefs(engine.ToolRefs{
		BashReg: e3Bash,
		JobReg:  job.NewMultiRegistry(bash.NewJobInfoAdapter(e3Bash)),
	})
	c.RegisterEngine(&engine.EngineViewState{ID: "e3", Name: "sys", Engine: eng3, System: true})

	// Task tool tracking on the registerEngine-built slot (nil taskToolIDs
	// would panic on the write).
	h2.Dispatch(types.QueryEvent{Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "toolu_task", Name: "Task", Summary: "track me"}})

	ws := dialAndStore(t, c)
	sendJSON(t, ws, map[string]string{"type": "engine_switch", "engineID": "e2"})
	// Rows for registry jobs the panel never saw an event for come from the
	// takeover reconcile embedded in the engine_switch metadata frame.
	items := readMetadataJobs(t, ws)
	got := map[string]string{} // id -> kind
	for _, j := range items {
		got[j.ID] = j.Kind
	}
	if got[bt.ID] != "bash" {
		t.Errorf("job %s kind = %q, want bash (full list: %v)", bt.ID, got[bt.ID], got)
	}
	if got[state.ID] != "agent" {
		t.Errorf("job %s kind = %q, want agent (full list: %v)", state.ID, got[state.ID], got)
	}
	if len(got) != 2 {
		t.Errorf("job list = %v, want exactly the bash and agent entries", got)
	}

	// Agent events through the registerEngine-built maps: job_started creates
	// the stream region (nil streams would panic) and the job's own
	// tool_start folds into it; a no-JobID event (sync sub-agent) is dropped
	// without touching any map.
	h2.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: state.ID, Kind: "agent", Status: "running",
		Description: "explore wiring", AgentType: "Explore",
		ParentToolUseID: "toolu_p", StartedMs: 100,
	}})
	h2.Dispatch(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "toolu_p", JobID: state.ID},
		ToolUse: &types.ToolUseEvent{ID: "toolu_inner", Name: "Grep", Summary: "inner"}})
	h2.Dispatch(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_unrelated"}, Text: "stray"})

	ws2 := dialJobsServer(t, c)
	items = readMetadataJobs(t, ws2)
	var agentItem *jobWireItemHydrated
	for i := range items {
		if items[i].ID == state.ID {
			agentItem = &items[i]
		}
	}
	if agentItem == nil {
		t.Fatalf("metadata jobs = %v, want the agent entry %s", hydratedIDs(items), state.ID)
	}
	if len(agentItem.Blocks) != 1 || agentItem.Blocks[0].ID != "toolu_inner" {
		t.Errorf("agent blocks = %+v, want [toolu_inner] (stray parent must not pollute)", agentItem.Blocks)
	}

	report := c.BusyReport()
	var bashItem *BusyItem
	for i := range report.Items {
		if report.Items[i].EngineID == "e2" && strings.Contains(report.Items[i].Detail, "sleep 30") {
			bashItem = &report.Items[i]
		}
		if report.Items[i].EngineID == "e3" {
			t.Errorf("BusyReport listed system engine e3: %+v — system engines are excluded", report.Items[i])
		}
	}
	if bashItem == nil {
		t.Fatalf("BusyReport items = %+v, want a bash job item for e2", report.Items)
	}
	if bashItem.Engine != "agent-2" {
		t.Errorf("e2 item Engine = %q, want the registered name %q", bashItem.Engine, "agent-2")
	}

	// The event-fed tail line, observed AFTER BusyReport (completion turns
	// the row terminal and would drop it from the busy list): real output
	// arriving on the bash job reaches the wire as a job_output frame through
	// Spawn's progress wiring, the engine's coalesce, and the hub — no poller
	// involved. Complete flushes the buffered delta deterministically
	// (terminated's emitEvent drains buffers first).
	_, _ = bgOut.Write([]byte("sleeping\n"))
	bt.Complete(0, false)
	tail := readJobOutput(t, ws2)
	if tail.JobID != bt.ID || tail.Tail != "sleeping" {
		t.Errorf("event-pushed job_output = {%s %q}, want {%s sleeping}", tail.JobID, tail.Tail, bt.ID)
	}
}

// TestHandleJobStop_NilGuards pins handleJobStop's nil guards with hand-built
// slots: no active slot, and an active slot without an engine — both must be
// silent no-ops, not panics.
func TestHandleJobStop_NilGuards(t *testing.T) {
	newBareConnector := func() *WUIConnector {
		c := &WUIConnector{
			slots:       make(map[string]*engineSlot),
			pendingAsks: make(map[string]*types.AskEvent),
			done:        make(chan struct{}),
			thumbs:      newThumbCache(),
		}
		c.outQ.Store(newOutQueue(outQueueCapacity))
		return c
	}

	// No active slot at all.
	c1 := newBareConnector()
	c1.handleJobStop("bg-1")

	// Active slot whose engine is nil.
	c2 := newBareConnector()
	slot := &engineSlot{engineID: "s1"}
	c2.slots["s1"] = slot
	activeID := "s1"
	c2.active.Store(&activeID)
	c2.handleJobStop("bg-1")
}

// TestApplyJobStreamEvent_Guards pins applyJobStreamEvent's contract on a
// region handed to it directly: a tool_start without a ToolUse applies no
// block and panics nowhere, an event whose parent matches the region anchor
// is masked to the root, and a deeper parent nests under that root block
// instead.
func TestApplyJobStreamEvent_Guards(t *testing.T) {
	c := newTestConnector(t)
	slot := mutDSlot()
	slot.streams["agent-1"] = &streamState{feedAnchor: "toolu_p"}

	// tool_start without a ToolUse: no block, no panic.
	c.applyJobStreamEvent(slot, types.QueryEvent{Type: types.EventToolStart,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_p", JobID: "agent-1"}})

	// Parent matches the anchor: masked, so it lands at the region root.
	c.applyJobStreamEvent(slot, types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "toolu_p", JobID: "agent-1"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_outer", Name: "Bash"}})

	// Deeper parent: nests under the root tool block, unmasked.
	c.applyJobStreamEvent(slot, types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_outer", JobID: "agent-1"}, Text: "nested"})

	stream := slot.streams["agent-1"]
	blocks := stream.blocks
	if len(blocks) != 1 || blocks[0].Kind != "tool" || blocks[0].ID != "toolu_outer" {
		t.Fatalf("root blocks = %+v, want exactly [tool toolu_outer]", blocks)
	}
	if len(blocks[0].Children) != 1 || blocks[0].Children[0].Kind != "text" || blocks[0].Children[0].Text != "nested" {
		t.Errorf("outer children = %+v, want [text %q]", blocks[0].Children, "nested")
	}
}

// newBlankJobsSlot builds a slot with every jobs map initialised and nothing
// in it, for the locked-function tests that drive the panel internals
// directly.
func newBlankJobsSlot(id string) *engineSlot {
	return &engineSlot{
		engineID:    id,
		taskToolIDs: map[string]bool{},
		jobView:     map[string]*jobViewEntry{},
		streams:     map[string]*streamState{"": {}},
	}
}

// TestStreams_RowRemovalDropsStream pins the cleanup guard: an agent-family
// row removal reclaims its stream region, while a bash row (which never owns
// a region) must leave the region set untouched — and the "" chat root is
// never a deletion candidate.
func TestStreams_RowRemovalDropsStream(t *testing.T) {
	c := newTestConnector(t)
	slot := newBlankJobsSlot("s1")
	slot.jobView["agent-1"] = &jobViewEntry{item: jobWireItem{ID: "agent-1", Kind: "fork", ParentToolUseID: "toolu_fk"}}
	slot.jobView["agent-0"] = &jobViewEntry{item: jobWireItem{ID: "agent-0", Kind: "agent"}}
	slot.jobView["bg-1"] = &jobViewEntry{item: jobWireItem{ID: "bg-1", Kind: "bash", ParentToolUseID: "toolu_b"}}
	slot.streams["agent-1"] = &streamState{}
	slot.streams["bg-1"] = &streamState{}

	c.deleteJobEntryLocked(slot, "agent-1")
	if _, ok := slot.streams["agent-1"]; ok {
		t.Error("fork row removal must reclaim its stream region")
	}

	c.deleteJobEntryLocked(slot, "bg-1")
	if _, ok := slot.streams["bg-1"]; !ok {
		t.Error("bash row removal must not touch the region set")
	}

	c.deleteJobEntryLocked(slot, "agent-0")
	if len(slot.jobView) != 0 {
		t.Errorf("jobView len = %d, want 0", len(slot.jobView))
	}
	if len(slot.streams) != 2 {
		t.Errorf("streams len = %d, want 2 (chat root + the bash row's leftover entry)", len(slot.streams))
	}
	if _, ok := slot.streams[""]; !ok {
		t.Error("the \"\" chat root must never be dropped by a row removal")
	}
}

// TestDeleteJobEntryLocked_GhostIDNoPanic pins the not-found guard: deleting
// an id the view never held must be a silent no-op, not a nil-entry deref.
func TestDeleteJobEntryLocked_GhostIDNoPanic(t *testing.T) {
	c := newTestConnector(t)
	slot := &engineSlot{
		engineID:    "s1",
		taskToolIDs: map[string]bool{},
		jobView:     map[string]*jobViewEntry{},
		streams:     map[string]*streamState{"": {}},
	}
	c.deleteJobEntryLocked(slot, "never-existed")
	if len(slot.jobView) != 0 {
		t.Errorf("jobView = %v, want untouched", slot.jobView)
	}
}

// TestSessionNew_RelistDoesNotReplayFlushedPending pins the feed lifecycle
// across a session-new clear: events folded into a feed are consumed by the
// clear (the feed dies with its view entry). If the feed survived, a terminal
// agent relisted after the clear would hydrate with its stale blocks —
// replaying dead history into the fresh entry.
func TestSessionNew_RelistDoesNotReplayFlushedPending(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "completed",
			Description: "clear replay", ParentToolUseID: "toolu_234",
			Backgrounded: true, StartedMs: 100,
		}}
	})
	// Fold events into the region before any registry enumeration sees the
	// job (job_started precedes them, as in production).
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "completed",
		Description: "clear replay", ParentToolUseID: "toolu_234", StartedMs: 100,
	}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "toolu_234", JobID: "agent-1"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_once", Name: "Read", Summary: "read file"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_234", JobID: "agent-1"}, Text: "flushed once"})

	// Each dial takes over the previous connection, so only the newest ws is
	// readable — re-dial per phase.
	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 || len(items[0].Blocks) != 2 {
		t.Fatalf("pre-clear blocks = %+v, want the folded tool+text", items)
	}

	// Terminal agent is still listed by the registry; the session-new clear
	// drops the view entry and its feed. The same metadata frame's takeover
	// reconcile re-lists the still-registered terminal entry — with a FRESH
	// empty region: the cleared feed must not replay.
	sendJSON(t, ws, map[string]string{"type": "session_new"})
	if items = readMetadataJobs(t, ws); len(items) != 1 || items[0].ID != "agent-1" {
		t.Fatalf("post-new metadata jobs = %+v, want the re-listed terminal entry and nothing else", items)
	}
	if len(items[0].Blocks) != 0 {
		t.Fatalf("post-new blocks = %+v, want none — the cleared feed must not replay into the reconciled entry", items[0].Blocks)
	}
}

// mutDSlot builds a slot whose jobs maps exist, for direct calls into the
// panel helpers without a running loop.
func mutDSlot() *engineSlot {
	return &engineSlot{
		jobView: make(map[string]*jobViewEntry),
		streams: map[string]*streamState{"": {}},
	}
}

// ---------------------------------------------------------------------------
// jobs_panel.go
// ---------------------------------------------------------------------------

// Kills jobs_panel.go:142 (expression/remove x2, expression/string-literal
// x2): all three terminal statuses must be recognized.
func TestMutD_IsTerminalJobStatus_AllThree(t *testing.T) {
	for _, s := range []string{"completed", "failed", "killed"} {
		if !isTerminalJobStatus(s) {
			t.Errorf("isTerminalJobStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"running", ""} {
		if isTerminalJobStatus(s) {
			t.Errorf("isTerminalJobStatus(%q) = true, want false", s)
		}
	}
}

// Kills jobs_panel.go:297 (statement/return) and :380 (statement/return):
// every job in a round shares one StartedMs, so ONLY the ID tiebreak orders
// the group — a zeroed tiebreak return leaves the group in map-iteration
// order, which Go randomizes per range as a rotation of insertion order.
// Each round uses freshly built maps, so a surviving mutant must draw the
// identity rotation in both paths across every round.
func TestMutD_JobSort_TiebreakOrdersBothOutputs(t *testing.T) {
	c := &WUIConnector{done: make(chan struct{})}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	c.activeWS.Store(&websocket.Conn{})

	ids := []string{
		"k-01", "k-02", "k-03", "k-04", "k-05",
		"k-06", "k-07", "k-08", "k-09", "k-10",
	}
	want := ids

	for round := 0; round < 6; round++ {
		slot := mutDSlot()
		slot.active.Store(true)
		for _, id := range ids {
			slot.jobView[id] = &jobViewEntry{item: jobWireItem{ID: id, Kind: "bash", Status: "running", StartedMs: 7777}}
		}

		frames := func() []string {
			slot.ssMu.Lock()
			snap := c.captureJobFramesLocked(slot)
			slot.ssMu.Unlock()
			return c.renderJobFrames(slot, snap)
		}()
		if len(frames) != 1 {
			t.Fatalf("round %d: frames = %d, want exactly one job_list push", round, len(frames))
		}
		var list jobListOutbound
		if err := json.Unmarshal([]byte(frames[0]), &list); err != nil {
			t.Fatal(err)
		}
		if got := jobIDs(list.Jobs); !mutDStringSlicesEqual(got, want) {
			t.Fatalf("round %d: job_list tie order = %v, want %v", round, got, want)
		}

		slot.ssMu.Lock()
		raw := c.buildJobsMetadata(slot)
		slot.ssMu.Unlock()
		if raw == nil {
			t.Fatalf("round %d: metadata jobs = nil, want snapshot", round)
		}
		var env jobsMetadataOutbound
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if got := hydratedIDs(env.Jobs); !mutDStringSlicesEqual(got, want) {
			t.Fatalf("round %d: metadata tie order = %v, want %v", round, got, want)
		}
	}
}

func mutDStringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Kills the pushJobsSnapshot Unlock mutant: the forced snapshot must release
// ssMu or every later event/beat deadlocks.
func TestMutD_PushJobsSnapshot_ReleasesLock(t *testing.T) {
	c := &WUIConnector{}
	slot := mutDSlot()
	c.pushJobsSnapshot(slot)
	if !waitFor(time.Second, func() bool { return slot.ssMu.TryLock() }) {
		t.Fatal("pushJobsSnapshot must release ssMu before returning")
	}
	slot.ssMu.Unlock()
}

// TestStreams_UnifiedLockDomain_ConcurrentRegionWriteVsRootRead is the
// lock-domain unification anchor: job-region map writes (applyJobLifecycleEvent
// via started events) and root-region reads/writes (onEngineEvent's root
// branch) run on two concurrent dispatch goroutines. Both paths must be
// serialized by the ONE ssMu domain — under -race any path that drops ssMu
// reports a map race; without -race, concurrent map write+read statistically
// aborts the runtime. Ends with the region census: all job regions present,
// root reset by query_end while job regions survive.
func TestStreams_UnifiedLockDomain_ConcurrentRegionWriteVsRootRead(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	slot := c.activeSlotTest(t)

	const n = 24
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
				ID: fmt.Sprintf("agent-%02d", i), Kind: "agent", Status: "running",
				ParentToolUseID: fmt.Sprintf("tu_%02d", i), StartedMs: int64(i + 1),
			}})
		}
	}()
	// Root-region traffic on the main thread: no-JobID deltas fold into the
	// chat root while the map is being written next door.
	for i := range 400 {
		h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("chat %d ", i)})
	}
	wg.Wait()

	slot.ssMu.Lock()
	root := slot.streams[""]
	rootBlocks := 0
	if root != nil {
		rootBlocks = len(root.blocks)
	}
	slot.ssMu.Unlock()
	if rootBlocks == 0 {
		t.Fatal("chat root collected no blocks — the root-branch writes never ran")
	}

	h.Dispatch(types.QueryEvent{Type: types.EventQueryEnd})

	slot.ssMu.Lock()
	root = slot.streams[""]
	rootLen := 0
	if root != nil {
		rootLen = len(root.blocks)
	}
	regions := 0
	for id := range slot.streams {
		if id != "" {
			regions++
		}
	}
	slot.ssMu.Unlock()
	if regions != n {
		t.Fatalf("job regions = %d, want all %d to survive the concurrent phase and the query_end reset", regions, n)
	}
	if rootLen != 0 {
		t.Fatalf("root blocks after query_end = %d, want 0 (only the \"\" region resets)", rootLen)
	}
}

// Kills the onEngineEvent ssMu-entry mutant on the job-region path: two
// goroutines dispatch the same job's process events through the REAL hub;
// onEngineEvent's ssMu section serializes the region appends and the count is
// exact. With the entry lock dropped (or made immediate), the appends race —
// blocks are lost or the runtime aborts on concurrent map writes. Each
// goroutine mints its own tool ids so no dedupe can mask a lost append;
// iters stays under jobFeedMaxBlocks/2 so the cap never drops a block (the
// exact-count assertion must not be vacuous); the start-gate forces the two
// goroutines to overlap, and the outer rounds rebuild the scenario per
// attempt — make mutate runs without -race, so the kill relies on
// statistical loss/concurrent-map-write detection.
func TestMutD_ApplyJobStreamEvent_HoldsLockDuringAppend(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	// Region first, through the real lifecycle path.
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running", ParentToolUseID: "tu_job", StartedMs: 1,
	}})

	const iters = 150
	for round := 0; round < 100; round++ {
		// Reset the region's blocks without dropping the region itself.
		slot := c.activeSlotTest(t)
		slot.ssMu.Lock()
		slot.streams["agent-1"] = &streamState{feedAnchor: "tu_job"}
		slot.ssMu.Unlock()

		start := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				for i := 0; i < iters; i++ {
					h.Dispatch(types.QueryEvent{Type: types.EventToolStart,
						Agent:   &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"},
						ToolUse: &types.ToolUseEvent{ID: fmt.Sprintf("tu_r%d_g%d_%d", round, g, i), Name: "Bash"}})
				}
			}(g)
		}
		close(start)
		wg.Wait()
		slot.ssMu.Lock()
		stream := slot.streams["agent-1"]
		n := 0
		if stream != nil {
			n = len(stream.blocks)
		}
		slot.ssMu.Unlock()
		if n != 2*iters {
			t.Fatalf("round %d: region root blocks = %d, want exactly %d (the append must run inside onEngineEvent's ssMu section)", round, n, 2*iters)
		}
	}
}

// Kills the applyJobStreamEvent guard mutants: nil Agent, empty JobID, and
// non-feed event types must all return before folding any block — and none
// may create a region.
func TestMutD_ApplyJobStreamEvent_Guards(t *testing.T) {
	c := &WUIConnector{}
	// nil Agent: the guard must return before touching event.Agent.
	c.applyJobStreamEvent(mutDSlot(), types.QueryEvent{Type: types.EventTextDelta, Text: "x"})

	// Agent present but no JobID (root/sync event): chat-only, no region.
	slot := mutDSlot()
	slot.streams["agent-1"] = &streamState{feedAnchor: "tu_job"}
	c.applyJobStreamEvent(slot, types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "tu_sync"}, Text: "x"})
	if n := len(slot.streams); n != 2 {
		t.Errorf("streams = %d keys, want 2 (root + the pre-seeded agent-1) for a no-JobID event", n)
	}
	if s := slot.streams["agent-1"]; s == nil || len(s.blocks) != 0 {
		t.Errorf("agent-1 region blocks = %+v, want none for a no-JobID event", slot.streams["agent-1"])
	}

	// Non-feed event type: usage/lifecycle events contribute no blocks.
	slot2 := mutDSlot()
	slot2.streams["agent-1"] = &streamState{feedAnchor: "tu_job"}
	c.applyJobStreamEvent(slot2, types.QueryEvent{Type: types.EventQueryEnd, Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"}})
	if n := len(slot2.streams); n != 2 {
		t.Errorf("streams = %d keys, want 2 for a non-feed event type", n)
	}
	if s := slot2.streams["agent-1"]; s == nil || len(s.blocks) != 0 {
		t.Errorf("agent-1 region blocks = %+v, want none for a non-feed event type", slot2.streams["agent-1"])
	}
}

// Kills the deleteJobEntryLocked family-guard mutant (!jobHasFeed term
// dropped): a bash entry must not reclaim a foreign job's region on this
// call — with job-id keying, that means never touching any foreign job's
// region (a region dies only with its own row's deleteJobEntryLocked, not
// this call).
func TestMutD_DeleteJobEntry_BashKeepsForeignFeed(t *testing.T) {
	slot := mutDSlot()
	slot.jobView["bashjob"] = &jobViewEntry{item: jobWireItem{ID: "bashjob", Kind: "bash", Status: "completed", ParentToolUseID: "toolu_live"}}
	slot.streams["agent-live"] = &streamState{}
	c := &WUIConnector{}
	c.deleteJobEntryLocked(slot, "bashjob")
	if _, ok := slot.jobView["bashjob"]; ok {
		t.Error("the bash entry itself must be deleted")
	}
	if _, ok := slot.streams["agent-live"]; !ok {
		t.Error("region agent-live must survive: it belongs to a live agent job, not the bash entry")
	}
}

// ---------------------------------------------------------------------------
// JobID routing (single delivery)
// ---------------------------------------------------------------------------

// A sync sub-agent event (no JobID) belongs to the chat region only: no job
// region may be created for it, and its text must never surface inside any
// job's region — not even after an unrelated job is discovered.
func TestStreams_SyncSubAgentReachesNoRegion(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_sync"}, Text: "sync text"})

	slot := c.slots["main"]
	slot.ssMu.Lock()
	n := len(slot.streams)
	slot.ssMu.Unlock()
	if n != 1 {
		t.Fatalf("streams holds %d keys, want 1 (only the chat root) — a no-JobID event must not create a region", n)
	}

	// An unrelated job gets its row+region from its started event and runs
	// its own event through the region: the sync text still reaches no feed.
	// The registry lists the job (it is genuinely running) — an unlisted
	// running row would be a vanish-drop at the dial's reconcile.
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "running",
			Description: "unrelated", ParentToolUseID: "tu_other",
			Backgrounded: true, StartedMs: 100,
		}}
	})
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		Description: "unrelated", ParentToolUseID: "tu_other", StartedMs: 100,
	}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_other", JobID: "agent-1"}, Text: "job text"})
	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	// Exactly one text block — the job's own. If the sync event had leaked
	// (extra block or merged text), the count or the exact text breaks.
	if len(items[0].Blocks) != 1 || items[0].Blocks[0].Text != "job text" {
		t.Fatalf("feed blocks = %+v, want exactly the job's own %q text block", items[0].Blocks, "job text")
	}
}

// A region held from the job's started event must survive the takeover
// reconcile's discovery of the job: reconcile must not reset it, or events
// folded before the dial are lost.
func TestStreams_FeedSurvivesDiscovery(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		Description: "survive discovery", ParentToolUseID: "tu_job", StartedMs: 100,
	}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"}, Text: "pre text"})
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "running",
			Description: "survive discovery", ParentToolUseID: "tu_job",
			Backgrounded: true, StartedMs: 100,
		}}
	})

	// Discovery = the dial's reconcile enumerating the registry.
	ws := dialJobsServer(t, c)
	if items := readMetadataJobs(t, ws); len(items) != 1 || len(items[0].Blocks) != 1 ||
		!strings.Contains(items[0].Blocks[0].Text, "pre text") {
		t.Fatalf("post-discovery blocks = %+v, want the pre-discovery text block intact", items)
	}
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"}, Text: "post text"})

	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	blocks := items[0].Blocks
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "pre text") || !strings.Contains(blocks[0].Text, "post text") {
		t.Fatalf("feed blocks = %+v, want one text block carrying both the pre- and post-discovery text", blocks)
	}
}

// A grandchild sync-spawned inside a background job inherits the job id and
// nests inside the job's own tool block in the job's region (three-level
// tree).
func TestStreams_GrandchildNestsByJobID(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-9", Kind: "agent", Status: "running", ParentToolUseID: "tu_job", StartedMs: 1,
	}})
	// Job's own tool_start: masked to the region root.
	c.Handle(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-9"},
		ToolUse: &types.ToolUseEvent{ID: "tu_outer", Name: "Agent"}})
	// Grandchild (sync spawn inside the job): same JobID, parent = the job's inner tool id.
	c.Handle(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "tu_outer", JobID: "agent-9"},
		ToolUse: &types.ToolUseEvent{ID: "tu_deep", Name: "Grep"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_deep", JobID: "agent-9"}, Text: "deep"})

	slot := c.slots["main"]
	slot.ssMu.Lock()
	stream := slot.streams["agent-9"]
	var blocks []streamBlock
	if stream != nil {
		blocks = append([]streamBlock(nil), stream.blocks...)
	}
	slot.ssMu.Unlock()
	if stream == nil {
		t.Fatal("streams[\"agent-9\"] missing — job_started must create the region")
	}
	if len(blocks) != 1 || blocks[0].Kind != "tool" || blocks[0].ID != "tu_outer" {
		t.Fatalf("root blocks = %+v, want exactly [tool tu_outer]", blocks)
	}
	outer := &blocks[0]
	if len(outer.Children) != 1 || outer.Children[0].Kind != "tool" || outer.Children[0].ID != "tu_deep" {
		t.Fatalf("outer children = %+v, want [tool tu_deep]", outer.Children)
	}
	deep := &outer.Children[0]
	if len(deep.Children) != 1 || deep.Children[0].Kind != "text" || deep.Children[0].Text != "deep" {
		t.Fatalf("deep children = %+v, want [text %q]", deep.Children, "deep")
	}
}

// Single delivery: a JobID-tagged event belongs to the jobs feed only — it
// must not nest into the chat stream. Sync sub-agent events (no JobID) keep
// nesting, and turn_start keeps updating the parent Agent block's name.
func TestOnEngineEvent_ChatSkipsJobEvents(t *testing.T) {
	c := newTestConnector(t)
	// Parent Agent tool block in the chat stream (non-agent event).
	c.Handle(types.QueryEvent{Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_job", Name: "Agent"}})
	// Background job event: single delivery — must not nest into chat.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-9"}, Text: "bg"})

	slot := c.slots["main"]
	slot.ssMu.Lock()
	parent := findBlock(slot.streams[""].blocks, "tu_job")
	var n int
	if parent != nil {
		n = len(parent.Children)
	}
	slot.ssMu.Unlock()
	if parent == nil {
		t.Fatal("parent Agent block missing from the chat stream")
	}
	if n != 0 {
		t.Fatalf("parent block children = %d, want 0 — job events must not nest into the chat stream", n)
	}

	// Reverse guard: a sync sub-agent event (no JobID) still nests.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job"}, Text: "sync"})
	slot.ssMu.Lock()
	parent = findBlock(slot.streams[""].blocks, "tu_job")
	n = len(parent.Children)
	slot.ssMu.Unlock()
	if n != 1 || parent.Children[0].Kind != "text" || parent.Children[0].Text != "sync" {
		t.Fatalf("parent children after the sync event = %+v, want [text %q]", parent.Children, "sync")
	}

	// turn_start exemption: the parent block's name keeps the agent type.
	c.Handle(types.QueryEvent{Type: types.EventTurnStart,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-9", AgentType: "Explore"}})
	slot.ssMu.Lock()
	parent = findBlock(slot.streams[""].blocks, "tu_job")
	name := parent.Name
	slot.ssMu.Unlock()
	if !strings.Contains(name, "Explore") {
		t.Fatalf("parent block name = %q, want it to contain %q (turn_start is exempt from the skip)", name, "Explore")
	}
}
