package wui

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// Phase C red anchors: terminal-region semantics (truncate + freeze on
// job_terminated), post-terminal drop through the real idle-kick delivery
// path, and the takeover reconcile in sendMetadata. Written before the
// implementation; each behavior anchor must fail against the phase B state.

// dispatchJobBlocks folds n masked tool_start blocks into the job's region
// through the real hub path (parent == anchor → region root).
func dispatchJobBlocks(h *hub.Hub, jobID, anchor string, from, n int) {
	for i := from; i < from+n; i++ {
		h.Dispatch(types.QueryEvent{Type: types.EventToolStart,
			Agent:   &types.AgentMeta{ParentToolUseID: anchor, JobID: jobID},
			ToolUse: &types.ToolUseEvent{ID: fmt.Sprintf("tu_%04d", i), Name: "Bash", Summary: fmt.Sprintf("step %d", i)}})
	}
}

// TestJobTerminated_TruncatesAndFreezes pins the terminal payload policy: an
// agent-family region is truncated to the last jobTerminalLastN blocks when
// job_terminated lands, and process events arriving after termination are
// dropped — the frozen card never grows.
func TestJobTerminated_TruncatesAndFreezes(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	rec := mutDCaptureSlog(t)

	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		ParentToolUseID: "tu_job", StartedMs: 1,
	}})
	dispatchJobBlocks(h, "agent-1", "tu_job", 1, 410)
	h.Dispatch(types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "failed", ExitCode: 1, ParentToolUseID: "tu_job", StartedMs: 1,
	}})
	// Late process events on the dead job: dropped by the frozen region.
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"}, Text: "late"})
	dispatchJobBlocks(h, "agent-1", "tu_job", 500, 5)
	// The drop decision is observable as its Debug line.
	if rec.count(slog.LevelDebug, "process event for terminated job region, dropping") == 0 {
		t.Error("late process events were not logged as frozen-region drops — the freeze path never ran")
	}

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 {
		t.Fatalf("metadata jobs len = %d, want 1: %+v", len(items), items)
	}
	if items[0].Status != "failed" || items[0].ExitCode != 1 {
		t.Errorf("terminal row = {status:%s exit:%d}, want {failed 1}", items[0].Status, items[0].ExitCode)
	}
	blocks := items[0].Blocks
	if len(blocks) != jobTerminalLastN {
		t.Fatalf("terminal region blocks = %d, want exactly %d (truncated, late events dropped)", len(blocks), jobTerminalLastN)
	}
	if blocks[0].Summary != "step 391" {
		t.Errorf("first kept block = %q, want %q (the last %d survive)", blocks[0].Summary, "step 391", jobTerminalLastN)
	}
	if blocks[jobTerminalLastN-1].Summary != "step 410" {
		t.Errorf("last kept block = %q, want %q", blocks[jobTerminalLastN-1].Summary, "step 410")
	}
}

// TestMetadata_TerminalJobsEmbedLastN pins the takeover payload policy: the
// metadata snapshot embeds terminal agent rows with their frozen last-N
// blocks and live agent rows with the full tree.
func TestMetadata_TerminalJobsEmbedLastN(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	// The registry lists both jobs (an unlisted running row is a vanish-drop
	// at the dial's reconcile — agent-2 must survive it).
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{
			{ID: "agent-1", Type: "agent", Status: "completed",
				Description: "terminal payload", ParentToolUseID: "tu_one",
				Backgrounded: true, StartedMs: 1},
			{ID: "agent-2", Type: "agent", Status: "running",
				Description: "live payload", ParentToolUseID: "tu_two",
				Backgrounded: true, StartedMs: 2},
		}
	})

	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running",
		ParentToolUseID: "tu_one", StartedMs: 1,
	}})
	dispatchJobBlocks(h, "agent-1", "tu_one", 1, 30)
	h.Dispatch(types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "completed", ParentToolUseID: "tu_one", StartedMs: 1,
	}})
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-2", Kind: "agent", Status: "running",
		ParentToolUseID: "tu_two", StartedMs: 2,
	}})
	dispatchJobBlocks(h, "agent-2", "tu_two", 1, 3)

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 2 {
		t.Fatalf("metadata jobs len = %d, want 2: %v", len(items), hydratedIDs(items))
	}
	got := map[string]jobWireItemHydrated{}
	for _, it := range items {
		got[it.ID] = it
	}
	term := got["agent-1"]
	if len(term.Blocks) != jobTerminalLastN {
		t.Fatalf("terminal row blocks = %d, want %d (last-N payload policy)", len(term.Blocks), jobTerminalLastN)
	}
	if term.Blocks[0].Summary != "step 11" || term.Blocks[len(term.Blocks)-1].Summary != "step 30" {
		t.Errorf("terminal row kept [%q..%q], want the LAST %d blocks [step 11..step 30]",
			term.Blocks[0].Summary, term.Blocks[len(term.Blocks)-1].Summary, jobTerminalLastN)
	}
	live := got["agent-2"]
	if len(live.Blocks) != 3 {
		t.Errorf("live row blocks = %d, want 3 (full tree while running)", len(live.Blocks))
	}
}

// jobEventRecorder is a side-of-the-hub observer proving reachability: it sees
// every event the panel saw, independent of the panel's drop decisions.
type jobEventRecorder struct {
	mu     sync.Mutex
	events []hub.Event
}

func (r *jobEventRecorder) Handle(ev hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *jobEventRecorder) snapshot() []hub.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hub.Event(nil), r.events...)
}

// waitForRecord polls the recorder until cond matches one recorded event
// (deadline-bounded); returns false on timeout.
func waitForRecord(t *testing.T, rec *jobEventRecorder, timeout time.Duration, cond func(hub.Event) bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout) // REAL-TIME
	for time.Now().Before(deadline) {   // REAL-TIME
		for _, ev := range rec.snapshot() {
			if cond(ev) {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	return false
}

// TestJobTerminated_LateIdleKickEventsDropped walks the REAL late-delivery
// path (finalize-time idle-kick, the reachable window from the plan's timing
// analysis): a message resolved against the still-running registry entry
// captures the sub-engine handle; delivering to it after job_terminated kicks
// a fresh query whose tagged process events surface AFTER the terminal event
// on the root hub — and the frozen region must drop every one of them. The
// sub-engine needs a non-empty SystemPrompt (the idle drain refuses empty
// prompts) and a non-empty ParentToolUseID (the taggedDispatcher install
// condition) or the kick's events would never carry the dead job id.
func TestJobTerminated_LateIdleKickEventsDropped(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	prov := &dupAutoTurnProvider{responses: [][]llm.StreamEvent{dupTextEvents("late kick text")}}
	eng := engine.New(&engine.Params{Provider: prov, Model: "test-model", EngineID: "main"})
	eng.SetDispatcher(h)
	t.Cleanup(func() { eng.Close() })
	// Swap the harness's mock slot for the real engine, wireDupAutoTurnEngine
	// style — the shim routes by engine id, so "main" now feeds the real slot.
	slot := &engineSlot{
		engineID:    "main",
		engine:      &engineAdapter{eng: eng},
		hub:         h,
		taskToolIDs: make(map[string]bool),
		jobView:     make(map[string]*jobViewEntry),
		streams:     map[string]*streamState{"": {}},
	}
	slot.active.Store(true)
	c.slotsMu.Lock()
	c.slots["main"] = slot
	c.slotsMu.Unlock()

	// Reachability observer: sees everything the panel sees, drop decisions
	// notwithstanding.
	rec := &jobEventRecorder{}
	unsub := h.Subscribe(rec)
	t.Cleanup(unsub)

	reg := agent.NewAgentRegistry()
	reg.SetOnJobEvent(func(et types.QueryEventType, ev types.JobEvent) {
		h.Dispatch(types.QueryEvent{Type: et, Job: &ev})
	})

	runDone := make(chan struct{})
	release := make(chan struct{})
	// Dial BEFORE Spawn: job_started is emitted synchronously inside Spawn, so
	// a later dial would miss the frame (inactive slots push nothing).
	ws := dialAndStore(t, c)
	state, err := reg.Spawn(context.Background(),
		func(ctx context.Context, agentID string) (*types.SubQueryResult, error) {
			// RunAgent's shape: the sub-engine is built INSIDE the spawn
			// goroutine (the registry id exists only here) and handed to the
			// registry as the delivery sink.
			sub := eng.NewSubEngine(engine.SubEngineOptions{
				SystemPrompt:    "late-kick sub system prompt",
				Tools:           map[string]tool.Tool{},
				ParentToolUseID: "tu_late",
				AgentType:       "General",
				JobID:           agentID,
			})
			reg.AttachSink(agentID, sub)
			close(runDone)
			<-release
			return &types.SubQueryResult{Content: "done"}, nil
		},
		nil, "late idle-kick job", "tu_late", "General")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// The started frame lands during Spawn (emitted before the goroutine
	// exists); read it so frame ordering stays deterministic.
	started := readJobList(t, ws)
	if len(started.Jobs) != 1 || started.Jobs[0].ID != state.ID || started.Jobs[0].Status != "running" {
		t.Fatalf("started frame = %+v, want the single running row %s", started.Jobs, state.ID)
	}

	<-runDone // sink attached; the sub-engine sits idle, no query ever ran
	dispatchJobBlocks(h, state.ID, "tu_late", 1, 25)

	// close(release) → runFn returns → finalize → job_terminated. Wait for the
	// TERMINAL frame to land before the late delivery: racing EnqueueAttachment
	// against the terminated handler makes the freeze assertion flaky.
	close(release)
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	termSeen := false
	for time.Now().Before(deadline) && !termSeen { // REAL-TIME
		out := readJobList(t, ws)
		for _, j := range out.Jobs {
			if j.ID == state.ID && isTerminalJobStatus(j.Status) {
				termSeen = true
			}
		}
	}
	if !termSeen {
		t.Fatal("no terminal job_list frame for the job within 5s of release")
	}

	// The delivery that kicks the dead id's idle sub-engine.
	st, ok := reg.Get(state.ID)
	if !ok {
		t.Fatal("registry entry vanished before the late delivery")
	}
	if st.Sink == nil {
		t.Fatal("sink missing from the registry entry — the capture window failed")
	}
	st.Sink.EnqueueAttachment(types.QueuedItem{
		Value:     "late ping",
		Mode:      types.ItemModePrompt,
		Timestamp: time.Now(), // REAL-TIME
	})

	// Reachability proof: the late query's tagged process event ARRIVES at the
	// root hub, after job_terminated. Without this the drop assertion could
	// pass vacuously (nothing was ever delivered).
	if !waitForRecord(t, rec, 10*time.Second, func(ev hub.Event) bool {
		return ev.Type == types.EventTextDelta && ev.Agent != nil &&
			ev.Agent.JobID == state.ID && ev.Text == "late kick text"
	}) {
		t.Fatal("late query never surfaced tagged events at the hub — the idle-kick path did not run (assertion vacuous)")
	}
	termIdx := -1
	for i, ev := range rec.snapshot() {
		if ev.Type == types.EventJobTerminated && ev.Job != nil && ev.Job.ID == state.ID {
			termIdx = i
		}
	}
	if termIdx < 0 {
		t.Fatal("recorder never saw job_terminated")
	}

	// Frozen drop: the region keeps exactly the truncated tail; the row stays
	// terminal (never resurrected to running); no new job frames.
	ws2 := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws2)
	var row *jobWireItemHydrated
	for i := range items {
		if items[i].ID == state.ID {
			row = &items[i]
		}
	}
	if row == nil {
		t.Fatalf("metadata jobs = %v, want the terminal row %s", hydratedIDs(items), state.ID)
	}
	if !isTerminalJobStatus(row.Status) {
		t.Errorf("row status = %q after the late kick, want it to stay terminal", row.Status)
	}
	if len(row.Blocks) != jobTerminalLastN {
		t.Fatalf("region blocks = %d after the late kick, want exactly %d — late process events must be dropped by the frozen region", len(row.Blocks), jobTerminalLastN)
	}
	if row.Blocks[0].Summary != "step 6" || row.Blocks[jobTerminalLastN-1].Summary != "step 25" {
		t.Errorf("frozen tail = [%q..%q], want [step 6..step 25] (last %d of 25)", row.Blocks[0].Summary, row.Blocks[jobTerminalLastN-1].Summary, jobTerminalLastN)
	}
	assertNoFrameWithin(t, ws2, 300*time.Millisecond)
}

// TestSendMetadata_ReconcilesMissingRows pins the takeover reconcile: rows the
// panel never learned (missed lifecycle events) are rebuilt from the registry
// enumeration — nil and foreground-registered entries are skipped, agent
// family rows also get their stream region (terminal ones born frozen).
func TestSendMetadata_ReconcilesMissingRows(t *testing.T) {
	c := newTestConnector(t)
	startedMs := time.Now().UnixMilli() // REAL-TIME
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{
			nil,
			{ID: "bg-fg", Type: "bash", Status: "running", Command: "fg cmd",
				Backgrounded: false, StartedMs: startedMs},
			bgBashJob("bg-1", "running", "make check", startedMs),
			{
				ID: "agent-1", Type: "agent", Status: "completed",
				Description: "missed job", AgentType: "Explore", ParentToolUseID: "toolu_m",
				Backgrounded: true, ExitCode: 3, StartedMs: startedMs - 5,
			},
		}
	})

	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 2 {
		t.Fatalf("metadata jobs len = %d, want 2 (nil + foreground skipped): %+v", len(items), items)
	}
	got := map[string]jobWireItemHydrated{}
	for _, it := range items {
		got[it.ID] = it
	}
	bg, ok := got["bg-1"]
	if !ok {
		t.Fatalf("reconcile missed the registry's bash row: %v", hydratedIDs(items))
	}
	if bg.Kind != "bash" || bg.Status != "running" || bg.Command != "make check" || bg.StartedMs != startedMs {
		t.Errorf("reconciled bash row = %+v, want shell fields from JobSummaries", bg.jobWireItem)
	}
	ag, ok := got["agent-1"]
	if !ok {
		t.Fatalf("reconcile missed the registry's agent row: %v", hydratedIDs(items))
	}
	if ag.Kind != "agent" || ag.Status != "completed" || ag.ExitCode != 3 || ag.AgentType != "Explore" ||
		ag.ParentToolUseID != "toolu_m" || ag.Description != "missed job" {
		t.Errorf("reconciled agent row = %+v, want shell fields from JobSummaries", ag.jobWireItem)
	}
	if len(ag.Blocks) != 0 {
		t.Errorf("terminal reconciled row blocks = %d, want 0 (born frozen, events were never seen)", len(ag.Blocks))
	}

	// The agent family row also got its region — a takeover for a job that
	// later turns out to still be running must have somewhere to fold into.
	slot := c.activeSlotTest(t)
	slot.ssMu.Lock()
	region := slot.streams["agent-1"]
	slot.ssMu.Unlock()
	if region == nil {
		t.Fatal("reconcile created no stream region for the agent row")
	}
	if !region.frozen {
		t.Error("reconciled terminal row's region must be born frozen")
	}
}

// TestSendMetadata_DropsRunningVanished pins the reconcile's deletion side and
// its StartedMs predicate: a running row the registry no longer lists is
// dropped ONLY when its StartedMs strictly precedes the enumeration instant —
// a row minted after the snapshot (job_started event landed between snapshot
// and ssMu) must survive for the next reconcile.
func TestSendMetadata_DropsRunningVanished(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mock().SetJobsFn(func() []*job.JobInfo { return nil })

	now := time.Now() // REAL-TIME
	oldMs := now.Add(-time.Hour).UnixMilli()
	newMs := now.Add(5 * time.Second).UnixMilli()
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "bg-old", Kind: "bash", Status: "running", Command: "doomed", StartedMs: oldMs,
	}})
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "bg-new", Kind: "bash", Status: "running", Command: "just minted", StartedMs: newMs,
	}})

	rec := mutDCaptureSlog(t)
	ws := dialJobsServer(t, c)
	items := readMetadataJobs(t, ws)
	if len(items) != 1 || items[0].ID != "bg-new" {
		t.Fatalf("post-reconcile jobs = %v, want only bg-new (bg-old dropped, bg-new is younger than the snapshot)", hydratedIDs(items))
	}
	if rec.count(slog.LevelInfo, "wui:jobs: running job missing from registry, dropping") != 1 {
		t.Fatal("the vanish drop must log the wui:jobs drop line exactly once")
	}
}

// TestSendMetadata_LocksOutsideEngineCalls guards the lock discipline: the
// registry enumeration must run OUTSIDE ssMu. The mock's JobSummaries grabs
// ssMu itself — correct code (enumerate before locking) glides through, while
// a regression that calls JobSummaries under ssMu self-deadlocks against the
// non-reentrant mutex and the select turns the hang into a red test. The full
// outbound queue pins the era-flip ordering (no push onto the old era while
// holding ssMu).
func TestSendMetadata_LocksOutsideEngineCalls(t *testing.T) {
	c := newTestConnector(t)
	slot := c.activeSlotTest(t)
	c.mock().SetJobSummariesFn(func() []*job.JobInfo {
		slot.ssMu.Lock()
		_ = slot.lastJobsPush // read exists only so SA2001 sees a non-empty section
		slot.ssMu.Unlock()
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", time.Now().UnixMilli())} // REAL-TIME
	})

	q := c.outQ.Load()
	for range outQueueCapacity {
		q.push(wsMsg{data: []byte("fill"), isBinary: false}, c.done)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.sendMetadata(slot)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("sendMetadata deadlocked — engineClient call suspected under ssMu")
	}
}

// TestSendMetadata_JobTailOutsideSsMu guards the takeover fetch's lock
// discipline: the one-time JobTail calls must run OUTSIDE ssMu. The mock's
// JobTail grabs ssMu itself — correct code (fetch between the ssMu sections)
// glides through, while a regression that fetches under ssMu self-deadlocks
// against the non-reentrant mutex and the watchdog turns the hang into a red
// test.
func TestSendMetadata_JobTailOutsideSsMu(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	slot := c.activeSlotTest(t)
	startedMs := time.Now().UnixMilli() // REAL-TIME
	c.mock().SetJobsFn(func() []*job.JobInfo {
		return []*job.JobInfo{bgBashJob("bg-1", "running", "make check", startedMs)}
	})
	c.mock().SetJobTailFn(func(id string) (string, bool) {
		slot.ssMu.Lock()
		_ = slot.lastJobsPush // read exists only so SA2001 sees a non-empty section
		slot.ssMu.Unlock()
		if id != "bg-1" {
			return "", false
		}
		return "takeover-tail", true
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.sendMetadata(slot)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("sendMetadata deadlocked — JobTail suspected under ssMu")
	}
}
