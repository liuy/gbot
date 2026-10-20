package engine

import (
	"context"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// indexOfType returns the collector position of the first event of the given
// type whose Job payload (when present) matches jobID, or -1.
func indexOfType(ec *eventCollector, typ types.QueryEventType, jobID string) int {
	for i, ev := range ec.Events() {
		if ev.Type != typ {
			continue
		}
		if ev.Job != nil && ev.Job.ID != jobID {
			continue
		}
		return i
	}
	return -1
}

// waitForEventType polls the collector until an event of the given type for
// the job appears (deadline-bounded); returns its index or -1 on timeout.
func waitForEventType(ec *eventCollector, typ types.QueryEventType, jobID string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout) // REAL-TIME
	for time.Now().Before(deadline) {   // REAL-TIME
		if i := indexOfType(ec, typ, jobID); i >= 0 {
			return i
		}
	}
	return -1
}

// TestRunAgent_BGJobLifecycleEventsOrdered pins the total order between the
// registry's lifecycle events and the job's process events on the root
// dispatcher: job_started strictly before the job's first tagged event,
// job_terminated strictly after its last one. The spawn goroutine emits both
// on the same goroutine that runs RunAgent, so any violation is a lost
// ordering guarantee, not a race.
func TestRunAgent_BGJobLifecycleEventsOrdered(t *testing.T) {
	deps, mgr, _ := msgDeps(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "job body text"), nil)

	parent, refs := msgWiredParent(t, mp, "main", deps)
	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})
	collector := newEventCollector()
	parent.SetDispatcher(collector)
	reg := refs.Agent.BgRegistry()

	runDone := make(chan struct{})
	state, err := reg.Spawn(context.Background(),
		func(ctx context.Context, agentID string) (*types.SubQueryResult, error) {
			defer close(runDone)
			res, err := parent.RunAgent(ctx, agenttool.AgentOpts{
				AgentID:         agentID,
				ParentToolUseID: "tu_ordered",
				AgentType:       "General",
				Prompt:          "run",
				BgRegistry:      reg,
				MaxTurns:        1,
			})
			return res, err
		},
		nil, "ordered job", "tu_ordered", "General")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-runDone
	if _, ok := reg.Wait(state.ID); !ok {
		t.Fatal("registry lost the spawned agent")
	}

	startedIdx := waitForEventType(collector, types.EventJobStarted, state.ID, 5*time.Second)
	if startedIdx < 0 {
		t.Fatal("no job_started event for the spawned agent reached the root dispatcher")
	}
	started := collector.Events()[startedIdx]
	if started.Job == nil {
		t.Fatal("job_started carries no Job payload")
	}
	if started.Job.Kind != "agent" {
		t.Errorf("job_started Kind = %q, want agent", started.Job.Kind)
	}
	if started.Job.ParentToolUseID != "tu_ordered" {
		t.Errorf("job_started ParentToolUseID = %q, want tu_ordered", started.Job.ParentToolUseID)
	}
	if started.Job.StartedMs <= 0 {
		t.Errorf("job_started StartedMs = %d, want > 0", started.Job.StartedMs)
	}
	if started.Job.Status != "running" {
		t.Errorf("job_started Status = %q, want running", started.Job.Status)
	}

	events := collector.Events()
	firstProc, lastProc := -1, -1
	for i, ev := range events {
		if ev.Agent != nil && ev.Agent.JobID == state.ID {
			if firstProc < 0 {
				firstProc = i
			}
			lastProc = i
		}
	}
	if firstProc < 0 {
		t.Fatal("no process events tagged with the job id reached the root dispatcher")
	}
	if startedIdx > firstProc {
		t.Errorf("job_started (index %d) must precede the job's first process event (index %d)", startedIdx, firstProc)
	}

	termIdx := waitForEventType(collector, types.EventJobTerminated, state.ID, 5*time.Second)
	if termIdx < 0 {
		t.Fatal("no job_terminated event for the spawned agent reached the root dispatcher")
	}
	term := collector.Events()[termIdx]
	if term.Job == nil {
		t.Fatal("job_terminated carries no Job payload")
	}
	if term.Job.Status != "completed" || term.Job.ExitCode != 0 {
		t.Errorf("job_terminated = {status:%s exit:%d}, want {completed 0}", term.Job.Status, term.Job.ExitCode)
	}
	if term.Job.ID != state.ID || term.Job.Kind != "agent" {
		t.Errorf("job_terminated = {id:%s kind:%s}, want {%s agent}", term.Job.ID, term.Job.Kind, state.ID)
	}
	if termIdx < lastProc {
		t.Errorf("job_terminated (index %d) must follow the job's last process event (index %d)", termIdx, lastProc)
	}
}

// TestWireEngine_AttachesJobEventEmitters pins the bootstrap wiring: a
// registry Spawn after CreateTools+WireEngine surfaces started+terminated on
// the engine's own dispatcher — the emitters must be attached without any
// engine query running.
func TestWireEngine_AttachesJobEventEmitters(t *testing.T) {
	deps, mgr, _ := msgDeps(t)
	parent, refs := msgWiredParent(t, &mockProvider{}, "main", deps)
	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})
	collector := newEventCollector()
	parent.SetDispatcher(collector)
	reg := refs.Agent.BgRegistry()

	state, err := reg.Spawn(context.Background(),
		func(context.Context, string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "done"}, nil
		},
		nil, "wiring probe", "tu_wire", "Explore")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, ok := reg.Wait(state.ID); !ok {
		t.Fatal("registry lost the spawned agent")
	}

	if i := waitForEventType(collector, types.EventJobStarted, state.ID, 5*time.Second); i < 0 {
		t.Fatal("WireEngine did not attach the agent registry's job-event emitter — no job_started on the dispatcher")
	}
	if i := waitForEventType(collector, types.EventJobTerminated, state.ID, 5*time.Second); i < 0 {
		t.Fatal("no job_terminated on the dispatcher after a terminal spawn")
	}

	// The bash registry's emitter is wired by the same call: a spawn there
	// must surface job_started on the dispatcher too.
	bgJob := refs.BashReg.Spawn("wiring bash probe", 0, nil)
	if i := waitForEventType(collector, types.EventJobStarted, bgJob.ID, 5*time.Second); i < 0 {
		t.Fatalf("no job_started for bash job %s — WireEngine did not attach refs.BashReg.OnJobEvent", bgJob.ID)
	}
}

// TestLateJobEvents_IdleKickPath proves the reachability of post-terminal
// process events: a message resolved against the still-running registry entry
// (the check-then-use window before finalize) captures the sub-engine handle;
// delivering to it after job_terminated kicks a fresh query whose tagged
// events surface AFTER the terminal event on the dispatcher. The terminated
// event must strictly precede every late process event.
func TestLateJobEvents_IdleKickPath(t *testing.T) {
	deps, mgr, _ := msgDeps(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "job body"), nil)
	mp.addResponse(textStreamEvents("test", "late ack text"), nil)

	parent, refs := msgWiredParent(t, mp, "main", deps)
	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})
	collector := newEventCollector()
	parent.SetDispatcher(collector)
	reg := refs.Agent.BgRegistry()
	resolver := &EngineMessageResolver{mgr: mgr}

	runDone := make(chan struct{})
	release := make(chan struct{})
	state, err := reg.Spawn(context.Background(),
		func(ctx context.Context, agentID string) (*types.SubQueryResult, error) {
			res, err := parent.RunAgent(ctx, agenttool.AgentOpts{
				AgentID:         agentID,
				ParentToolUseID: "tu_late",
				AgentType:       "General",
				Prompt:          "run",
				BgRegistry:      reg,
				// No MaxTurns: a capped sub exhausts its turn budget before
				// the idle-kick query can run (see BackgroundAgentMessagesParent).
			})
			close(runDone)
			<-release
			return res, err
		},
		nil, "late-window job", "tu_late", "General")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	<-runDone // sub-engine idle; registry entry still running (runFn parked)
	target, status, ok := resolver.Resolve(state.ID)
	if !ok {
		t.Fatal("resolver missed the running agent")
	}
	if status != "running" {
		t.Fatalf("resolver status = %q, want running (finalize has not run yet)", status)
	}
	if target == nil {
		t.Fatal("resolver returned no target for the running agent — sink not attached")
	}

	close(release) // finalize writes the terminal state and emits job_terminated
	if i := waitForEventType(collector, types.EventJobTerminated, state.ID, 5*time.Second); i < 0 {
		t.Fatal("no job_terminated after release — finalize did not emit")
	}

	// The delivery that kicks the dead id's idle engine: a queued prompt
	// starts a new query whose events still carry the terminal job's id.
	target.EnqueueAttachment(types.QueuedItem{
		Value:     "late ping",
		Mode:      types.ItemModePrompt,
		Timestamp: time.Now(), // REAL-TIME
	})

	// Wait for the late query to surface at least one tagged event.
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	lateSeen := false
	for time.Now().Before(deadline) && !lateSeen { // REAL-TIME
		for _, ev := range collector.Events() {
			if ev.Agent != nil && ev.Agent.JobID == state.ID && ev.Text == "late ack text" {
				lateSeen = true
				break
			}
		}
	}
	if !lateSeen {
		t.Fatal("late query never surfaced tagged events — the idle-kick path did not run")
	}

	events := collector.Events()
	termIdx := indexOfType(collector, types.EventJobTerminated, state.ID)
	after := 0
	for i, ev := range events {
		if i <= termIdx || ev.Agent == nil || ev.Agent.JobID != state.ID {
			continue
		}
		after++
		if i < termIdx {
			t.Errorf("process event for the terminal job (index %d, type %s) precedes job_terminated (index %d)", i, ev.Type, termIdx)
		}
	}
	if after == 0 {
		t.Fatal("no process events after job_terminated — the reachability assertion is vacuous")
	}
}
