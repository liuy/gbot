package wui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// withJobsWatcherInterval swaps the watcher cadence for the test's lifetime.
// Must run BEFORE the connector is constructed: the ticker interval is read
// when the watcher goroutine starts.
func withJobsWatcherInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := jobsWatcherIntervalNS.Swap(int64(d))
	t.Cleanup(func() { jobsWatcherIntervalNS.Store(old) })
}

func withJobsPendingTTL(t *testing.T, d time.Duration) {
	t.Helper()
	old := jobsPendingTTLNS.Swap(int64(d))
	t.Cleanup(func() { jobsPendingTTLNS.Store(old) })
}

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
// frames along the way are skipped: a watcher job_list can slip into the new
// era in the window between sendMetadata's queue swap and its metadata push
// (harmless ordering — metadata is the authoritative snapshot).
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
// window spanning multiple watcher beats: unchanged state must stay silent.
func assertNoFrameWithin(t *testing.T, ws *websocket.Conn, window time.Duration) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(window)) // REAL-TIME
	_, data, err := ws.ReadMessage()
	if err == nil {
		t.Fatalf("expected no frame within %v, got %q", window, string(data))
	}
	_ = ws.SetReadDeadline(time.Time{})
}

// assertOnlyJobListWithin reads frames for window and fails on anything but
// job_list: the bug it guards against is a repeated job_output for an
// unchanged tail, while an incidental job_list (summary churn) is legal.
func assertOnlyJobListWithin(t *testing.T, ws *websocket.Conn, window time.Duration) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(window)) // REAL-TIME
	defer func() { _ = ws.SetReadDeadline(time.Time{}) }()
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return // window elapsed with no (more) frames
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil || head.Type != "job_list" {
			t.Fatalf("unexpected frame within %v (only job_list allowed): %s", window, string(data))
		}
	}
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

func TestJobTick_PushesJobListOnNewJob(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	started := time.Now().UnixMilli() // REAL-TIME
	// Dial BEFORE any job exists: the first sighting must then serialize the
	// new-entry literal itself. Dialing after SetJobsFn races the watcher —
	// the first tick can fire pre-dial, where the activeWS guard silently
	// swallows the frame and the update branch heals fields by the next tick,
	// hiding new-literal field-clear mutants.
	ws := dialAndStore(t, c)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{
			bgBashJob("bg-1", "running", "make check", started),
			{
				ID:           "bg-2",
				Type:         "bash",
				Status:       "failed",
				Command:      "make lint",
				Backgrounded: true,
				ExitCode:     130,
				StartedMs:    started + 1,
			},
		}
	})
	raw := readUntilType(t, ws, "job_list")
	var out jobListOutbound
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	if len(out.Jobs) != 2 {
		t.Fatalf("jobs len = %d, want 2", len(out.Jobs))
	}
	got := out.Jobs[0]
	if got.ID != "bg-1" || got.Kind != "bash" || got.Status != "running" {
		t.Errorf("job = {id:%s kind:%s status:%s}, want {bg-1 bash running}", got.ID, got.Kind, got.Status)
	}
	if got.Command != "make check" {
		t.Errorf("command = %q, want %q", got.Command, "make check")
	}
	if got.StartedMs != started || got.StartedMs <= 0 {
		t.Errorf("startedMs = %d, want %d (>0)", got.StartedMs, started)
	}
	// The terminal entry must carry its exit code straight from the
	// new-entry literal (field-clear at construction would zero it).
	term := out.Jobs[1]
	if term.Status != "failed" || term.ExitCode != 130 {
		t.Errorf("terminal job = {status:%s exitCode:%d}, want {failed 130}", term.Status, term.ExitCode)
	}
	// job_list is scalar-only; blocks only exist on metadata hydration entries.
	if strings.Contains(string(raw), "blocks") {
		t.Errorf("job_list frame must not carry blocks: %s", string(raw))
	}
}

func TestJobTick_SkipsForegroundBash(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID:           "bg-9",
			Type:         "bash",
			Status:       "running",
			Command:      "foreground cmd",
			Backgrounded: false,
			StartedMs:    time.Now().UnixMilli(), // REAL-TIME
		}}
	})

	ws := dialAndStore(t, c)
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

func TestJobTick_DiffSilence(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "sleep 5", time.Now().UnixMilli())} // REAL-TIME
	})

	ws := dialAndStore(t, c)
	readJobList(t, ws)
	// Unchanged summary and absent tail: several beats, zero frames.
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

func TestJobTick_BashTailPushOnChange(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", time.Now().UnixMilli())} // REAL-TIME
	})
	var tailMu sync.Mutex
	cur := "a\n"
	c.mock().SetJobTailFn(func(id string) (string, bool) {
		if id != "bg-1" {
			return "", false
		}
		tailMu.Lock()
		defer tailMu.Unlock()
		return cur, true
	})

	ws := dialAndStore(t, c)
	readJobList(t, ws)
	first := readJobOutput(t, ws)
	if first.JobID != "bg-1" || first.Tail != "a\n" {
		t.Fatalf("first job_output = {%s %q}, want {bg-1 \"a\\n\"}", first.JobID, first.Tail)
	}

	tailMu.Lock()
	cur = "a\nb\n"
	tailMu.Unlock()
	second := readJobOutput(t, ws)
	if second.JobID != "bg-1" || second.Tail != "a\nb\n" {
		t.Fatalf("second job_output = {%s %q}, want {bg-1 \"a\\nb\\n\"}", second.JobID, second.Tail)
	}
	// Tail stops changing: any frame in the window may only be a job_list,
	// never a repeated job_output.
	assertOnlyJobListWithin(t, ws, 150*time.Millisecond)
}

func TestJobTick_InactiveSlotNoPush(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)

	// A second slot with a fully initialized jobs view and watcher, but NOT
	// active: its job must never reach the wire.
	h := hub.NewHub()
	mockB := &mockEngine{}
	slotB := &engineSlot{
		engineID:           "engineB",
		engine:             mockB,
		hub:                h,
		taskToolIDs:        map[string]bool{},
		jobView:            map[string]*jobViewEntry{},
		jobFeeds:           map[string]*jobFeedState{},
		innerToolToFeed:    map[string]string{},
		pendingAgentEvents: map[string]*pendingAgentEvents{},
	}
	c.slotsMu.Lock()
	c.slots["engineB"] = slotB
	c.slotsMu.Unlock()
	c.startJobsWatcher(slotB)
	mockB.SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "build", time.Now().UnixMilli())} // REAL-TIME
	})

	ws := dialAndStore(t, c)
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

func TestJobStop_KillsAndPushes(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", time.Now().UnixMilli())} // REAL-TIME
	})

	ws := dialAndStore(t, c)
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
	withJobsWatcherInterval(t, 10*time.Millisecond)
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

// TestJobTick_EmptyViewPushesEmptyList pins that a view transitioning to
// empty still pushes the clearing frame: with jobs_clear retired, the live
// trigger is the last running job vanishing from the registry. A diff
// baseline reset to "" would suppress exactly that frame (collect's
// never-pushed guard), leaving the client rendering gone jobs until
// reconnect.
func TestJobTick_EmptyViewPushesEmptyList(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	var jobsMu sync.Mutex
	visible := []*job.JobInfo{bgBashJob("bg-1", "running", "doomed cmd", 1000)}
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		return visible
	})

	ws := dialAndStore(t, c)
	first := readJobList(t, ws)
	if len(first.Jobs) != 1 {
		t.Fatalf("initial jobs len = %d, want 1", len(first.Jobs))
	}

	// The registry drops the job (engine restart / registry eviction): the
	// emptied view must reach the wire as an empty job_list.
	jobsMu.Lock()
	visible = nil
	jobsMu.Unlock()
	second := readJobList(t, ws)
	if len(second.Jobs) != 0 {
		t.Fatalf("after the last job vanishes, jobs len = %d, want 0: %+v", len(second.Jobs), second.Jobs)
	}
	// Baseline settled on the real empty-payload JSON: watcher stays silent.
	assertNoFrameWithin(t, ws, 150*time.Millisecond)
}

func TestMetadata_EmbedsJobsWithBlocks(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
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
	parent := &types.AgentMeta{ParentToolUseID: "toolu_parent"}
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: parent,
		ToolUse: &types.ToolUseEvent{ID: "toolu_inner", Name: "Bash", Summary: "echo hi"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "agent says "})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "hello"})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, Agent: parent,
		ToolResult: &types.ToolResultEvent{ToolUseID: "toolu_inner"}})

	ws := dialJobsServer(t, c)
	readJobList(t, ws) // discovery (+ pending flush) proven before re-dial
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

func TestCaptureJobEvent_MaskingAndNesting(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "running",
			Description: "nested test", ParentToolUseID: "toolu_p",
			Backgrounded: true, StartedMs: 100,
		}}
	})
	// Discovery first, then events — production ordering: the bg agent's
	// nested sub-agent can only emit events after its own tool_start was
	// applied to the feed (which itself follows discovery). Events keyed by
	// an inner tool id arriving pre-discovery have no feed to route into.
	ws := dialJobsServer(t, c)
	readJobList(t, ws)

	// Job's own events (parent == toolu_p) → masked to the feed root.
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: &types.AgentMeta{ParentToolUseID: "toolu_p"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_outer", Name: "Bash", Summary: "outer"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_p"}, Text: "outer text"})
	// Nested agent events (parent == an inner tool id) → children, no masking.
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: &types.AgentMeta{ParentToolUseID: "toolu_outer"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_deep", Name: "Grep", Summary: "deep"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_deep"}, Text: "deep text"})
	// Unrelated parent → parked as pending, must pollute no feed.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_unrelated"}, Text: "stray"})

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
			t.Errorf("unrelated-parent event leaked into feed: %+v", b)
		}
	}
}

func TestPendingAgentEvents_FlushedOnDiscovery(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	agentJob := &job.JobInfo{
		ID: "agent-1", Type: "agent", Status: "running",
		Description: "late discovery", ParentToolUseID: "toolu_late",
		Backgrounded: true, StartedMs: 100,
	}
	parent := &types.AgentMeta{ParentToolUseID: "toolu_late"}
	c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: parent,
		ToolUse: &types.ToolUseEvent{ID: "toolu_x", Name: "Read", Summary: "read file"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "flushed text"})
	// Job appears only after the events were parked.
	c.mock().SetJobsFn(func() []*job.JobInfo { return []*job.JobInfo{agentJob} })

	ws := dialJobsServer(t, c)
	readJobList(t, ws) // discovery → pending flush
	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	if len(items[0].Blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2 (tool + text from pending): %+v", len(items[0].Blocks), items[0].Blocks)
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
func TestPendingAgentEvents_FlushedOnDiscovery_ForkKind(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	forkJob := &job.JobInfo{
		ID: "agent-1", Type: "fork", Status: "running",
		Description: "fork feed", ParentToolUseID: "toolu_fk",
		Backgrounded: true, StartedMs: 100,
	}
	parent := &types.AgentMeta{ParentToolUseID: "toolu_fk"}
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Agent: parent, Text: "fork text"})
	c.mock().SetJobsFn(func() []*job.JobInfo { return []*job.JobInfo{forkJob} })

	ws := dialJobsServer(t, c)
	out := readJobList(t, ws)
	if len(out.Jobs) != 1 {
		t.Fatalf("wire jobs len = %d, want 1", len(out.Jobs))
	}
	if out.Jobs[0].Kind != "fork" {
		t.Errorf("wire kind = %q, want fork (JobInfo.Type rides the wire verbatim)", out.Jobs[0].Kind)
	}

	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
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

func TestJobTick_JobViewCapEvictsUntilAtCap(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := make([]*job.JobInfo, 0, 201)
		for i := 1; i <= 201; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "completed", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		return jobs
	})

	ws := dialAndStore(t, c)
	out := readJobList(t, ws)
	if len(out.Jobs) != 200 {
		t.Fatalf("jobs len = %d, want 200 (capped)", len(out.Jobs))
	}
	ids := map[string]bool{}
	for _, j := range out.Jobs {
		ids[j.ID] = true
	}
	if ids["bg-001"] {
		t.Error("bg-001 (smallest startedMs) must be evicted, but is present")
	}
	if !ids["bg-201"] {
		t.Error("bg-201 (largest startedMs) must survive the cap, but is absent")
	}
}

func TestJobTick_CapNeverEvictsRunning(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := make([]*job.JobInfo, 0, 201)
		for i := 1; i <= 200; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "completed", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		jobs = append(jobs, bgBashJob("bg-run", "running", "live cmd", 5000))
		return jobs
	})

	ws := dialAndStore(t, c)
	out := readJobList(t, ws)
	if len(out.Jobs) != 200 {
		t.Fatalf("jobs len = %d, want 200 (199 terminal + 1 running)", len(out.Jobs))
	}
	ids := map[string]bool{}
	running := 0
	for _, j := range out.Jobs {
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

// TestMetadata_TailFromCacheNeverCallsJobTail locks the deadlock-cycle middle
// edge: metadata's tail must come from the watcher-warmed entry cache, never
// from a JobTail call inside the ssMu → jobsMu section. The 1h interval makes
// the test deterministic — no legitimate background tick can bump the count.
func TestMetadata_TailFromCacheNeverCallsJobTail(t *testing.T) {
	withJobsWatcherInterval(t, time.Hour)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", time.Now().UnixMilli())} // REAL-TIME
	})
	var tailCalls atomic.Int32
	c.mock().SetJobTailFn(func(id string) (string, bool) {
		if id != "bg-1" {
			return "", false
		}
		tailCalls.Add(1)
		return "fixed-tail", true
	})

	// Warm the cache with one direct tick (fetches the tail outside any
	// connector lock, writes it back under jobsMu).
	slot := c.activeSlotTest(t)
	c.jobTick(slot)
	n := tailCalls.Load()
	if n != 1 {
		t.Fatalf("jobTick tail calls = %d, want 1", n)
	}

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	if items[0].Tail != "fixed-tail" {
		t.Errorf("metadata tail = %q, want the cached %q", items[0].Tail, "fixed-tail")
	}
	if got := tailCalls.Load(); got != n {
		t.Errorf("JobTail called %d times after metadata (was %d) — metadata must read the cache, not call JobTail", got, n)
	}
}

// TestCaptureJobEvent_FeedCapDropsHead pins the 400-block feed cap. Plan
// drafted this with text_delta events, but consecutive text deltas merge
// into ONE text block (updateStreamState append semantics), so they can
// never reach the cap; tool_start blocks never merge and do.
func TestCaptureJobEvent_FeedCapDropsHead(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "running",
			Description: "cap test", ParentToolUseID: "toolu_cap",
			Backgrounded: true, StartedMs: 100,
		}}
	})
	for i := 1; i <= 401; i++ {
		c.Handle(types.QueryEvent{Type: types.EventToolStart,
			Agent:   &types.AgentMeta{ParentToolUseID: "toolu_cap"},
			ToolUse: &types.ToolUseEvent{ID: fmt.Sprintf("toolu_%03d", i), Name: "Bash", Summary: fmt.Sprintf("step %d", i)}})
	}

	ws := dialJobsServer(t, c)
	readJobList(t, ws)
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

func TestPendingAgentEvents_TTLSwept(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	withJobsPendingTTL(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_ghost"}, Text: "stale text"})
	// Several beats pass with the job absent — the TTL sweep must discard
	// the parked events before the job ever materializes.
	time.Sleep(100 * time.Millisecond) // REAL-TIME
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-ghost", Type: "agent", Status: "running",
			Description: "ghost", ParentToolUseID: "toolu_ghost",
			Backgrounded: true, StartedMs: 100,
		}}
	})

	ws := dialJobsServer(t, c)
	readJobList(t, ws) // discovery — flushed events would land here if sweeping failed
	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	if len(items[0].Blocks) != 0 {
		t.Errorf("blocks = %+v, want none — TTL sweep should have discarded the parked events", items[0].Blocks)
	}
}

func TestSessionSwitch_ClearsTerminalJobs(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
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
	first := readJobList(t, ws)
	if len(first.Jobs) != 2 {
		t.Fatalf("initial jobs len = %d, want 2", len(first.Jobs))
	}

	// The old session's jobs leave the registry with the session itself;
	// the frozen terminal entry would survive without the explicit clear.
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
	t.Cleanup(func() { _ = refsA.Agent.BgRegistry().Cancel(id) })

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
	orphan, err := refsA.Agent.BgRegistry().Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil, "orphan work", "toolu_orphan", "Explore")
	if err != nil {
		t.Fatalf("spawn orphan: %v", err)
	}
	t.Cleanup(func() { _ = refsA.Agent.BgRegistry().Cancel(orphan.ID) })

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

// TestJobsPanel_DefaultCadence pins the production watcher cadence (500ms
// tick, 5s pending TTL — the documented contract; panel update frequency is
// user-visible). Tests override both knobs, so nothing else observes the
// defaults; a direct value pin is the only deterministic observable.
func TestJobsPanel_DefaultCadence(t *testing.T) {
	if got := jobsWatcherInterval(); got != 500*time.Millisecond {
		t.Errorf("default watcher interval = %v, want 500ms", got)
	}
	if got := jobsPendingTTL(); got != 5*time.Second {
		t.Errorf("default pending TTL = %v, want 5s", got)
	}
}

// TestPendingAgentEvents_ShortDelayBeforeDiscoveryStillFlushes locks the TTL
// semantics: events parked for well under the default 5s TTL must survive
// several job-less ticks and still flush when the job is finally discovered.
// A zeroed TTL (or a zero firstSeen) sweeps them on the first tick and the
// feed would hydrate empty.
func TestPendingAgentEvents_ShortDelayBeforeDiscoveryStillFlushes(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "toolu_slow"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_slow_tool", Name: "Read", Summary: "read file"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_slow"}, Text: "slow flushed text"})
	// Several job-less ticks pass; the default 5s TTL must keep them parked.
	time.Sleep(100 * time.Millisecond) // REAL-TIME
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-slow", Type: "agent", Status: "running",
			Description: "late", ParentToolUseID: "toolu_slow",
			Backgrounded: true, StartedMs: 100,
		}}
	})

	ws := dialJobsServer(t, c)
	readJobList(t, ws) // discovery — the parked events must flush here
	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1", len(items))
	}
	if len(items[0].Blocks) != 2 {
		t.Fatalf("blocks = %+v, want 2 (tool + text survived the delay)", items[0].Blocks)
	}
	if items[0].Blocks[0].ID != "toolu_slow_tool" || items[0].Blocks[1].Text != "slow flushed text" {
		t.Errorf("blocks = [%+v %+v], want [toolu_slow_tool, \"slow flushed text\"]",
			items[0].Blocks[0], items[0].Blocks[1])
	}
}

// TestJobsWatcher_ExitsOnStop pins the watcher's lifecycle: Stop closes
// c.done and the watcher's select must take the done case. A watcher that
// ignores done keeps polling the engine forever after the connector died.
func TestJobsWatcher_ExitsOnStop(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	// Hand-built connector: the test itself owns Stop (newTestConnector
	// registers a second Stop in cleanup — closing c.done twice panics).
	var calls atomic.Int32
	m := &mockEngine{}
	m.SetJobsFn(func() []*job.JobInfo {
		calls.Add(1)
		return nil
	})
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
		testMock:    m,
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	activeID := "main"
	c.active.Store(&activeID)
	slot := &engineSlot{
		engineID:           "main",
		engine:             m,
		hub:                hub.NewHub(),
		taskToolIDs:        map[string]bool{},
		jobView:            map[string]*jobViewEntry{},
		jobFeeds:           map[string]*jobFeedState{},
		innerToolToFeed:    map[string]string{},
		pendingAgentEvents: map[string]*pendingAgentEvents{},
	}
	slot.active.Store(true)
	c.slots["main"] = slot
	c.startJobsWatcher(slot)

	if !waitFor(2*time.Second, func() bool { return calls.Load() > 0 }) {
		t.Fatal("watcher never ticked before Stop")
	}
	c.Stop()
	after := calls.Load()
	time.Sleep(150 * time.Millisecond) // REAL-TIME
	if got := calls.Load(); got != after {
		t.Errorf("JobSummaries calls after Stop = %d (was %d) — watcher must exit on done", got, after)
	}
}

// TestJobWire_UnknownTypePassthrough pins that JobInfo.Type rides the wire
// verbatim: with the unified field there is no mapper left, so a type outside
// the bash/agent/fork vocabulary reaches the client unchanged instead of
// collapsing to "".
func TestJobWire_UnknownTypePassthrough(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "x-1", Type: "custom_kind", Status: "running",
			Backgrounded: true, StartedMs: 42,
		}}
	})

	ws := dialAndStore(t, c)
	out := readJobList(t, ws)
	if len(out.Jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(out.Jobs))
	}
	if out.Jobs[0].Kind != "custom_kind" {
		t.Errorf("kind = %q, want the raw type %q", out.Jobs[0].Kind, "custom_kind")
	}
}

// TestJobTick_NilViewSlotNoPanic pins the nil-map guard: slots built directly
// (bypassing registerEngine) have no jobs maps, and a tick on such a slot must
// be a silent no-op, not a nil-map write panic.
func TestJobTick_NilViewSlotNoPanic(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", 1)}
	})
	ghost := &engineSlot{engineID: "ghost", engine: c.mock()}
	c.jobTick(ghost) // must return at the guard, not panic on the nil jobView
}

// TestCaptureJobEvent_NilViewNoPanic pairs with TestJobTick_NilViewSlotNoPanic
// for the event path: an agent-meta event on a map-less slot must park
// nowhere and panic nowhere.
func TestCaptureJobEvent_NilViewNoPanic(t *testing.T) {
	c := newTestConnector(t)
	ghost := &engineSlot{engineID: "ghost", hub: hub.NewHub()}
	c.captureJobEvent(ghost, types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_x"}, Text: "stray"})
}

// TestJobTick_MergeSkipsNilAndForegroundEntries covers the merge filter's
// three-way contract in one fixture: a nil summary is skipped (no panic), a
// foreground job is skipped without abandoning the loop (a later background
// job still merges), and a fresh completed entry carries its exit code onto
// the wire.
func TestJobTick_MergeSkipsNilAndForegroundEntries(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{
			nil,
			{ID: "bg-fg", Type: "bash", Status: "running", Command: "fg cmd",
				Backgrounded: false, StartedMs: 900},
			{ID: "bg-1", Type: "bash", Status: "completed", Command: "done cmd",
				Backgrounded: true, StartedMs: 7777, ExitCode: 3},
		}
	})

	ws := dialAndStore(t, c)
	out := readJobList(t, ws)
	if len(out.Jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1 (nil + foreground skipped): %+v", len(out.Jobs), out.Jobs)
	}
	got := out.Jobs[0]
	if got.ID != "bg-1" || got.Status != "completed" || got.ExitCode != 3 {
		t.Errorf("job = {%s %s exit:%d}, want {bg-1 completed exit:3}", got.ID, got.Status, got.ExitCode)
	}
}

// TestJobTick_TailsOnlyBashJobs pins the tailID filter: only bash jobs are
// tailed (agent feeds come from events, not tails), and a non-bash entry in
// the middle of the summaries must not abort the scan for later bash ids.
func TestJobTick_TailsOnlyBashJobs(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{
			{ID: "agent-1", Type: "agent", Status: "running",
				Description: "no tails", ParentToolUseID: "toolu_p",
				Backgrounded: true, StartedMs: 100},
			bgBashJob("bg-1", "running", "make check", 200),
		}
	})
	var tailMu sync.Mutex
	var tailIDs []string
	c.mock().SetJobTailFn(func(id string) (string, bool) {
		tailMu.Lock()
		tailIDs = append(tailIDs, id)
		tailMu.Unlock()
		return "tail of " + id, true
	})

	ws := dialAndStore(t, c)
	readJobList(t, ws)
	if out := readJobOutput(t, ws); out.JobID != "bg-1" {
		t.Fatalf("first job_output = %s, want bg-1", out.JobID)
	}
	time.Sleep(80 * time.Millisecond) // REAL-TIME: several more beats
	tailMu.Lock()
	defer tailMu.Unlock()
	for _, id := range tailIDs {
		if id != "bg-1" {
			t.Errorf("JobTail called for %q — only bash jobs may be tailed (calls: %v)", id, tailIDs)
		}
	}
}

// TestJobTick_UpdatesExistingEntryStatusAndExitCode pins the update path of
// an already-known job: a status/exit transition must reach the wire as a new
// job_list frame with the new values (the entry is not re-created).
func TestJobTick_UpdatesExistingEntryStatusAndExitCode(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	var jobsMu sync.Mutex
	info := bgBashJob("bg-1", "running", "long cmd", 5555)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		return []*job.JobInfo{info}
	})

	ws := dialAndStore(t, c)
	first := readJobList(t, ws)
	if first.Jobs[0].Status != "running" {
		t.Fatalf("initial status = %s, want running", first.Jobs[0].Status)
	}

	jobsMu.Lock()
	info = bgBashJob("bg-1", "failed", "long cmd", 5555)
	info.ExitCode = 3
	jobsMu.Unlock()
	second := readJobList(t, ws)
	if len(second.Jobs) != 1 {
		t.Fatalf("updated jobs len = %d, want 1", len(second.Jobs))
	}
	if second.Jobs[0].Status != "failed" || second.Jobs[0].ExitCode != 3 {
		t.Errorf("updated job = {%s exit:%d}, want {failed exit:3}", second.Jobs[0].Status, second.Jobs[0].ExitCode)
	}
}

// TestJobTick_RunningJobVanishesFromRegistry pins the drop path: a running
// entry that disappears from the registry is removed from the view (frame
// with it gone), while jobs still listed survive.
func TestJobTick_RunningJobVanishesFromRegistry(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	var jobsMu sync.Mutex
	visible := []*job.JobInfo{
		bgBashJob("bg-1", "running", "doomed cmd", 1000),
		bgBashJob("bg-2", "running", "live cmd", 2000),
	}
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		return visible
	})

	ws := dialAndStore(t, c)
	first := readJobList(t, ws)
	if len(first.Jobs) != 2 {
		t.Fatalf("initial jobs len = %d, want 2", len(first.Jobs))
	}

	jobsMu.Lock()
	visible = visible[1:]
	jobsMu.Unlock()
	second := readJobList(t, ws)
	if len(second.Jobs) != 1 || second.Jobs[0].ID != "bg-2" {
		t.Fatalf("after vanish jobs = %+v, want only bg-2", second.Jobs)
	}
}

// TestJobTick_CapRunningOldestNeverEvicted pins that the cap's victim scan is
// restricted to TERMINAL entries: a running job with the smallest StartedMs
// must survive while the oldest terminal is the one evicted.
func TestJobTick_CapRunningOldestNeverEvicted(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := []*job.JobInfo{bgBashJob("bg-run", "running", "oldest but live", 500)}
		for i := 1; i <= 200; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "completed", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		return jobs
	})

	ws := dialAndStore(t, c)
	out := readJobList(t, ws)
	if len(out.Jobs) != 200 {
		t.Fatalf("jobs len = %d, want 200 (capped)", len(out.Jobs))
	}
	ids := map[string]bool{}
	for _, j := range out.Jobs {
		ids[j.ID] = true
	}
	if !ids["bg-run"] {
		t.Error("running job with smallest startedMs was evicted — the victim scan must skip running entries")
	}
	if ids["bg-001"] {
		t.Error("bg-001 (smallest terminal startedMs) must be the evicted one, but is present")
	}
}

// TestJobTick_CapAllRunningRetainsAll pins the cap's escape hatch: with no
// terminal entries to evict, the loop gives up and the over-cap view is
// pushed in full — a live job is never dropped and the loop never spins.
func TestJobTick_CapAllRunningRetainsAll(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := make([]*job.JobInfo, 0, 201)
		for i := 1; i <= 201; i++ {
			jobs = append(jobs, bgBashJob(fmt.Sprintf("bg-%03d", i), "running", fmt.Sprintf("cmd %d", i), int64(1000+i)))
		}
		return jobs
	})

	ws := dialAndStore(t, c)
	out := readJobList(t, ws)
	if len(out.Jobs) != 201 {
		t.Fatalf("jobs len = %d, want 201 (all running, cap cannot evict)", len(out.Jobs))
	}
	running := 0
	for _, j := range out.Jobs {
		if j.Status == "running" {
			running++
		}
	}
	if running != 201 {
		t.Errorf("running count = %d, want 201", running)
	}
}

// sortedJobsFixture mints jobs whose startedMs order and ID order disagree,
// plus a 2-way and a 4-way startedMs tie so both sort keys are exercised (the
// 4-way tie also makes an always-false tiebreak comparator survive only ~2%
// of runs rather than ~25%). wantOrder is the (startedMs asc, ID asc)
// expectation.
func sortedJobsFixture() ([]*job.JobInfo, []string) {
	specs := []struct {
		id string
		ms int64
	}{
		{"bg-e", 1000}, {"bg-h", 1000},
		{"bg-a", 2000}, {"bg-b", 2000}, {"bg-c", 2000}, {"bg-d", 2000},
		{"bg-j", 5000}, {"bg-i", 6000},
	}
	jobs := make([]*job.JobInfo, 0, len(specs))
	for _, s := range specs {
		jobs = append(jobs, bgBashJob(s.id, "running", "cmd "+s.id, s.ms))
	}
	return jobs, []string{"bg-e", "bg-h", "bg-a", "bg-b", "bg-c", "bg-d", "bg-j", "bg-i"}
}

// TestJobList_SortedByStartedMsThenID pins the job_list ordering: startedMs
// ascending, ID ascending as the tiebreak. jobView is a map, so without the
// sort the order would be random; the fixture's ID-vs-started disagreement
// also separates the startedMs key from an ID-only sort.
func TestJobList_SortedByStartedMsThenID(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	jobs, wantOrder := sortedJobsFixture()
	c.mock().SetJobsFn(func() []*job.JobInfo { return jobs })

	ws := dialAndStore(t, c)
	out := readJobList(t, ws)
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
	// Deterministic pre-warm: a background tick racing the dial can leave the
	// jobView empty when metadata serializes (dial won, tick never ran).
	withJobsWatcherInterval(t, time.Hour)
	c := newTestConnector(t)
	jobs, wantOrder := sortedJobsFixture()
	c.mock().SetJobsFn(func() []*job.JobInfo { return jobs })
	c.jobTick(c.activeSlotTest(t))

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != len(wantOrder) {
		t.Fatalf("metadata jobs len = %d, want %d", len(items), len(wantOrder))
	}
	for i, want := range wantOrder {
		if items[i].ID != want {
			t.Errorf("metadata jobs[%d] = %s, want %s (full order: %v)", i, items[i].ID, want, hydratedIDs(items))
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

// TestJobOutput_FramesSortedByJobID pins the job_output send order: frames
// for simultaneously-changed tails go out in ascending job id order.
func TestJobOutput_FramesSortedByJobID(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	ids := []string{"bg-d", "bg-a", "bg-c", "bg-b"}
	c.mock().SetJobsFn(func() []*job.JobInfo {
		jobs := make([]*job.JobInfo, 0, len(ids))
		for i, id := range ids {
			jobs = append(jobs, bgBashJob(id, "running", "cmd "+id, int64(1000+i)))
		}
		return jobs
	})
	c.mock().SetJobTailFn(func(id string) (string, bool) { return "tail " + id, true })

	ws := dialAndStore(t, c)
	readJobList(t, ws)
	want := []string{"bg-a", "bg-b", "bg-c", "bg-d"}
	for _, wantID := range want {
		out := readJobOutput(t, ws)
		if out.JobID != wantID {
			t.Fatalf("job_output order broke at %q, want %q", out.JobID, wantID)
		}
		if out.Tail != "tail "+wantID {
			t.Fatalf("job_output tail = %q, want %q", out.Tail, "tail "+wantID)
		}
	}
}

// TestJobOutput_StableTailDoesNotBlockLaterJobs pins the tail-diff loop: a
// bash entry whose tail is unchanged must be skipped (continue), not abort
// the scan (break) — later jobs with changed tails still get their frame.
func TestJobOutput_StableTailDoesNotBlockLaterJobs(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{
			bgBashJob("bg-1", "running", "quiet cmd", 1000),
			bgBashJob("bg-2", "running", "loud cmd", 2000),
		}
	})
	var tailMu sync.Mutex
	tails := map[string]string{"bg-1": "stable", "bg-2": "first"}
	c.mock().SetJobTailFn(func(id string) (string, bool) {
		tailMu.Lock()
		defer tailMu.Unlock()
		return tails[id], true
	})

	ws := dialAndStore(t, c)
	readJobList(t, ws)
	for _, want := range []string{"bg-1", "bg-2"} { // warm both baselines (id order)
		if out := readJobOutput(t, ws); out.JobID != want {
			t.Fatalf("warm-up frame = %s, want %s", out.JobID, want)
		}
	}

	tailMu.Lock()
	tails["bg-2"] = "second"
	tailMu.Unlock()
	out := readJobOutput(t, ws)
	if out.JobID != "bg-2" || out.Tail != "second" {
		t.Fatalf("post-flip frame = {%s %q}, want {bg-2 \"second\"}", out.JobID, out.Tail)
	}
}

// TestPushJobsSnapshot_NilViewNoFrame pins the nil-view guard of the forced
// push: a map-less (test-built) slot must push nothing, even though the
// sentinel baseline reset would otherwise force an empty job_list out.
func TestPushJobsSnapshot_NilViewNoFrame(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
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
	withJobsWatcherInterval(t, 10*time.Millisecond)
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
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
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
	first := readJobList(t, ws)
	if len(first.Jobs) != 2 {
		t.Fatalf("initial jobs len = %d, want 2", len(first.Jobs))
	}

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
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", time.Now().UnixMilli())} // REAL-TIME
	})

	ws := dialAndStore(t, c)
	readJobList(t, ws) // warm the view

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
// literal end to end through real engines: the jobs maps exist (merge, feed
// creation, route registration, pending parking — nil maps would panic), the
// watcher is started (frames arrive), taskToolIDs accepts Task calls, and the
// slot name/system drive BusyReport (running job named, system engine
// excluded).
func TestRegisterEngine_SlotFullyWiredForJobs(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	mgr := engine.NewEngineManager()
	h1 := hub.NewHub()
	eng1 := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng1.SetDispatcher(h1)
	eng1.SetToolRefs(engine.ToolRefs{})
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng1})
	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	// e2: named engine with a running bg bash job and a running bg agent.
	bashReg := bash.NewBackgroundJobRegistry()
	bt := bashReg.Spawn("sleep 30", 0, bash.NewStreamingOutput(nil))
	t.Cleanup(func() { _ = bashReg.Kill(bt.ID) })
	agentReg := agent.NewBackgroundAgentRegistry()
	state, err := agentReg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil, "explore wiring", "toolu_p", "Explore")
	if err != nil {
		t.Fatalf("spawn bg agent: %v", err)
	}
	t.Cleanup(func() { _ = agentReg.Cancel(state.ID) })
	jobReg := job.NewMultiRegistry(bash.NewJobInfoAdapter(bashReg), agent.NewBackgroundAgentJobAdapter(agentReg))
	h2 := hub.NewHub()
	eng2 := engine.New(&engine.Params{EngineID: "e2", Model: "test-model"})
	eng2.SetDispatcher(h2)
	eng2.SetToolRefs(engine.ToolRefs{BashReg: bashReg, JobReg: jobReg})
	c.RegisterEngine(&engine.EngineViewState{ID: "e2", Name: "agent-2", Engine: eng2})

	// e3: SYSTEM engine with a running job — BusyReport must exclude it.
	e3Bash := bash.NewBackgroundJobRegistry()
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
	list := readJobList(t, ws)
	got := map[string]string{} // id -> kind
	for _, j := range list.Jobs {
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

	// Agent events through the registerEngine-built maps: an own-parent
	// tool_start registers an inner route (nil innerToolToFeed would panic)
	// and an unroutable parent parks (nil pendingAgentEvents would panic).
	h2.Dispatch(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "toolu_p"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_inner", Name: "Grep", Summary: "inner"}})
	h2.Dispatch(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_unrelated"}, Text: "stray"})

	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
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

// TestApplyAgentEventLocked_Guards pins applyAgentEventLocked's defensive
// branches directly: an unknown feed key is a no-op, an Agent-nil event is
// applied unmasked at the feed root, and a tool_start without a ToolUse
// registers no route — none of them panic.
func TestApplyAgentEventLocked_Guards(t *testing.T) {
	c := newTestConnector(t)
	slot := &engineSlot{
		engineID:           "s1",
		taskToolIDs:        map[string]bool{},
		jobView:            map[string]*jobViewEntry{},
		jobFeeds:           map[string]*jobFeedState{"toolu_p": {}},
		innerToolToFeed:    map[string]string{},
		pendingAgentEvents: map[string]*pendingAgentEvents{},
	}

	// Unknown feed key: ignored.
	c.applyAgentEventLocked(slot, "ghost", types.QueryEvent{Type: types.EventTextDelta, Text: "nowhere"})

	// tool_start without a ToolUse: no block, no route registration, no panic.
	c.applyAgentEventLocked(slot, "toolu_p", types.QueryEvent{Type: types.EventToolStart,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_p"}})

	// Agent-nil event: applied unmasked — lands as a root text block.
	c.applyAgentEventLocked(slot, "toolu_p", types.QueryEvent{Type: types.EventTextDelta, Text: "root text"})

	feed := slot.jobFeeds["toolu_p"]
	blocks := feed.streamState.blocks
	if len(blocks) != 1 || blocks[0].Kind != "text" || blocks[0].Text != "root text" {
		t.Errorf("feed blocks = %+v, want [text \"root text\"]", blocks)
	}
	if len(slot.innerToolToFeed) != 0 {
		t.Errorf("innerToolToFeed = %v, want no routes registered", slot.innerToolToFeed)
	}
}

// newBlankJobsSlot builds a slot with every jobs map initialised and nothing
// in it, for the locked-function tests that drive the watcher's internals
// directly.
func newBlankJobsSlot(id string) *engineSlot {
	return &engineSlot{
		engineID:           id,
		taskToolIDs:        map[string]bool{},
		jobView:            map[string]*jobViewEntry{},
		jobFeeds:           map[string]*jobFeedState{},
		innerToolToFeed:    map[string]string{},
		pendingAgentEvents: map[string]*pendingAgentEvents{},
	}
}

// TestMergeJobSummariesLocked_FeedAndTailGates pins both family gates: a feed
// is created only for agent-family kinds that carry a parent tool-use id, and
// tail fetching stays bash-only.
func TestMergeJobSummariesLocked_FeedAndTailGates(t *testing.T) {
	c := newTestConnector(t)
	slot := newBlankJobsSlot("s1")
	tailIDs := c.mergeJobSummariesLocked(slot, []*job.JobInfo{
		nil,
		{ID: "agent-1", Type: "fork", Status: "running", ParentToolUseID: "toolu_fk", Backgrounded: true, StartedMs: 1},
		{ID: "agent-0", Type: "agent", Status: "running", Backgrounded: true, StartedMs: 2},
		{ID: "bg-1", Type: "bash", Status: "running", ParentToolUseID: "toolu_b", Backgrounded: true, StartedMs: 3},
	})
	if _, ok := slot.jobFeeds["toolu_fk"]; !ok {
		t.Error("fork job with a parent tool-use id must get a feed")
	}
	if len(slot.jobFeeds) != 1 {
		t.Errorf("jobFeeds len = %d, want 1 — agent-0 has no parent id and bash never owns a feed", len(slot.jobFeeds))
	}
	if len(tailIDs) != 1 || tailIDs[0] != "bg-1" {
		t.Errorf("tailIDs = %v, want [bg-1] — tail fetching is bash-only", tailIDs)
	}
}

// TestDeleteJobEntryLocked_FamilyFeedReclaim pins the cleanup guard: an
// agent-family row reclaims its feed and inner tool routes, while a bash row
// (which never owns a feed) must leave the feed set untouched.
func TestDeleteJobEntryLocked_FamilyFeedReclaim(t *testing.T) {
	c := newTestConnector(t)
	slot := newBlankJobsSlot("s1")
	slot.jobView["agent-1"] = &jobViewEntry{item: jobWireItem{ID: "agent-1", Kind: "fork", ParentToolUseID: "toolu_fk"}}
	slot.jobView["agent-0"] = &jobViewEntry{item: jobWireItem{ID: "agent-0", Kind: "agent"}}
	slot.jobView["bg-1"] = &jobViewEntry{item: jobWireItem{ID: "bg-1", Kind: "bash", ParentToolUseID: "toolu_b"}}
	slot.jobFeeds["toolu_fk"] = &jobFeedState{}
	slot.jobFeeds["toolu_b"] = &jobFeedState{}
	slot.innerToolToFeed["inner-1"] = "toolu_fk"

	c.deleteJobEntryLocked(slot, "agent-1")
	if _, ok := slot.jobFeeds["toolu_fk"]; ok {
		t.Error("fork row must reclaim its feed")
	}
	if _, ok := slot.innerToolToFeed["inner-1"]; ok {
		t.Error("fork row must reclaim its inner tool routes")
	}

	c.deleteJobEntryLocked(slot, "bg-1")
	if _, ok := slot.jobFeeds["toolu_b"]; !ok {
		t.Error("bash row must not touch the feed set")
	}

	c.deleteJobEntryLocked(slot, "agent-0")
	if len(slot.jobView) != 0 {
		t.Errorf("jobView len = %d, want 0", len(slot.jobView))
	}
	if len(slot.jobFeeds) != 1 {
		t.Errorf("jobFeeds len = %d, want 1 (only the bash row's leftover entry)", len(slot.jobFeeds))
	}
}

// TestDeleteJobEntryLocked_GhostIDNoPanic pins the not-found guard: deleting
// an id the view never held must be a silent no-op, not a nil-entry deref.
func TestDeleteJobEntryLocked_GhostIDNoPanic(t *testing.T) {
	c := newTestConnector(t)
	slot := &engineSlot{
		engineID:           "s1",
		taskToolIDs:        map[string]bool{},
		jobView:            map[string]*jobViewEntry{},
		jobFeeds:           map[string]*jobFeedState{},
		innerToolToFeed:    map[string]string{},
		pendingAgentEvents: map[string]*pendingAgentEvents{},
	}
	c.deleteJobEntryLocked(slot, "never-existed")
	if len(slot.jobView) != 0 {
		t.Errorf("jobView = %v, want untouched", slot.jobView)
	}
}

// TestSessionNew_RelistDoesNotReplayFlushedPending pins the pending-events
// bookkeeping: events flushed into a feed at discovery are consumed (deleted
// from pendingAgentEvents). If they were left behind, a terminal agent
// relisted after the session-new clear would rebuild the feed's blocks —
// replaying stale history into the fresh entry.
func TestSessionNew_RelistDoesNotReplayFlushedPending(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{{
			ID: "agent-1", Type: "agent", Status: "completed",
			Description: "clear replay", ParentToolUseID: "toolu_234",
			Backgrounded: true, StartedMs: 100,
		}}
	})
	// Park events BEFORE the watcher ever sees the job.
	c.Handle(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "toolu_234"},
		ToolUse: &types.ToolUseEvent{ID: "toolu_once", Name: "Read", Summary: "read file"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "toolu_234"}, Text: "flushed once"})

	// Each dial takes over the previous connection, so only the newest ws is
	// readable — re-dial per phase.
	ws := dialJobsServer(t, c)
	readJobList(t, ws) // discovery: flush + consume
	ws = dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 || len(items[0].Blocks) != 2 {
		t.Fatalf("pre-clear blocks = %+v, want the flushed tool+text", items)
	}

	// Terminal agent is still listed by the registry; the session-new clear
	// drops the view entry (and must have consumed the pending events at
	// discovery). The cleared state is observable only on the metadata frame
	// (jobs field omitted): the watcher's merge re-adds the still-listed
	// entry within the same beat, so the job_list diff never sees the empty
	// view and pushes no clearing frame on this path.
	sendJSON(t, ws, map[string]string{"type": "session_new"})
	if items = readMetadataJobs(t, ws); items != nil {
		t.Fatalf("post-new metadata jobs = %+v, want the field omitted (terminal cleared)", items)
	}

	// The relist lands on the next watcher beat and pushes no frame (the
	// re-added entry marshals back to the pre-clear baseline), so the wire
	// offers nothing to synchronize on — wait out a beat before the final dial.
	time.Sleep(100 * time.Millisecond) // REAL-TIME

	ws = dialJobsServer(t, c)
	items = readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("post-relist metadata jobs = %v, want the agent entry", hydratedIDs(items))
	}
	if len(items[0].Blocks) != 0 {
		t.Errorf("post-relist blocks = %+v, want none — flushed pending events must not replay", items[0].Blocks)
	}
}
