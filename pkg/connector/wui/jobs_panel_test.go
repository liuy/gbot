package wui

import (
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
		Type:         "local_bash",
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
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", started)}
	})

	ws := dialAndStore(t, c)
	raw := readUntilType(t, ws, "job_list")
	var out jobListOutbound
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(out.Jobs))
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
			Type:         "local_bash",
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

func TestJobsClear_RemovesTerminalKeepsRunning(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{
			bgBashJob("bg-1", "completed", "done cmd", 1000),
			bgBashJob("bg-2", "running", "live cmd", 2000),
		}
	})

	ws := dialAndStore(t, c)
	first := readJobList(t, ws)
	if len(first.Jobs) != 2 {
		t.Fatalf("initial jobs len = %d, want 2", len(first.Jobs))
	}

	sendJSON(t, ws, map[string]string{"type": "jobs_clear"})
	second := readJobList(t, ws)
	if len(second.Jobs) != 1 {
		t.Fatalf("after clear jobs len = %d, want 1 (running only): %+v", len(second.Jobs), second.Jobs)
	}
	if second.Jobs[0].ID != "bg-2" || second.Jobs[0].Status != "running" {
		t.Errorf("surviving job = {%s %s}, want {bg-2 running}", second.Jobs[0].ID, second.Jobs[0].Status)
	}
}

// TestJobsClear_EmptyViewPushesEmptyList pins the acceptance scenario "after
// clearing all done jobs the float button disappears": clearing an
// all-terminal view must push an empty job_list. A diff-baseline reset to ""
// would suppress exactly that frame (collect's never-pushed guard), leaving
// the client rendering cleared jobs until reconnect.
func TestJobsClear_EmptyViewPushesEmptyList(t *testing.T) {
	withJobsWatcherInterval(t, 10*time.Millisecond)
	c := newTestConnector(t)
	var jobsMu sync.Mutex
	visible := []*job.JobInfo{
		bgBashJob("bg-1", "completed", "finished cmd", 1000),
		bgBashJob("bg-2", "failed", "dead cmd", 2000),
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

	// The registry drops terminal jobs alongside the clear (bash evicts ~3s
	// after completion), so the emptied view stays empty afterwards.
	jobsMu.Lock()
	visible = nil
	jobsMu.Unlock()
	sendJSON(t, ws, map[string]string{"type": "jobs_clear"})
	second := readJobList(t, ws)
	if len(second.Jobs) != 0 {
		t.Fatalf("after clearing an all-terminal view, jobs len = %d, want 0: %+v", len(second.Jobs), second.Jobs)
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
			Type:            "local_agent",
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
			ID: "agent-1", Type: "local_agent", Status: "running",
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
		ID: "agent-1", Type: "local_agent", Status: "running",
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
			ID: "agent-1", Type: "local_agent", Status: "running",
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
			ID: "agent-ghost", Type: "local_agent", Status: "running",
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
