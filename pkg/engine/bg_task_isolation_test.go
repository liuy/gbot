package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// stubSubagentEngine blocks RunAgent until its release channel closes or the
// context is cancelled, keeping a background spawn AgentRunning for the
// duration of the assertions without a real LLM provider.
type stubSubagentEngine struct {
	release chan struct{}
}

func (s *stubSubagentEngine) RunAgent(ctx context.Context, _ agenttool.AgentOpts) (*types.SubQueryResult, error) {
	select {
	case <-s.release:
		return &types.SubQueryResult{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Acceptance test 1 (plan 2026-10-14-bg-task-per-engine, Bug 1): a background
// task spawned by engine A must be invisible to engine B — neither B's job
// list nor B's kill path may reach it.
func TestBgTask_PerEngineIsolation(t *testing.T) {
	deps, _, _ := msgDeps(t)

	stub := &stubSubagentEngine{release: make(chan struct{})}
	t.Cleanup(func() { close(stub.release) })

	_, refsA := msgWiredParent(t, &mockProvider{}, "engine-a", deps)
	_, refsB := msgWiredParent(t, &mockProvider{}, "engine-b", deps)
	refsA.Agent.SetEngine(stub)

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

	// A sees exactly its own spawn, already running.
	aList := refsA.JobReg.List()
	if len(aList) != 1 {
		t.Fatalf("A's job list length = %d, want 1", len(aList))
	}
	if aList[0].ID != id {
		t.Errorf("A's job id = %q, want %q", aList[0].ID, id)
	}
	if aList[0].Status != "running" {
		t.Errorf("A's job status = %q, want running", aList[0].Status)
	}

	// B must not see it.
	for _, info := range refsB.JobReg.List() {
		if info.ID == id {
			t.Fatalf("engine B's job list leaks engine A's bg task %q", id)
		}
	}

	// B's kill path must not reach it.
	if err := refsB.JobReg.Kill(id); !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("B's Kill(%q) error = %v, want ErrNotFound", id, err)
	}

	// A's kill path stops it.
	if err := refsA.JobReg.Kill(id); err != nil {
		t.Fatalf("A's Kill(%q): %v", id, err)
	}
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded poll for async kill
	for {
		info, ok := refsA.JobReg.Get(id)
		if ok && info.Status == "killed" {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME: bounded poll for async kill
			status := "<gone>"
			if ok {
				status = info.Status
			}
			t.Fatalf("A's job %q status = %q after kill (2s deadline), want killed", id, status)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
	}
}

// addHangingStream queues a response that emits its events and then blocks
// until release closes or the request context is cancelled — a grandchild
// turn stuck mid-stream that only a background kill can end. match selects
// the requests this slot serves (nil = any): the bg grandchild's Stream call
// races its spawning sub-agent's next turn for the next script slot, so the
// slot must be pinned to the grandchild's request rather than taken in order.
func (m *mockProvider) addHangingStream(events []llm.StreamEvent, release chan struct{}, match func(*llm.Request) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, mockResponse{events: events, hang: release, match: match})
}

// requestHasTextBlock reports whether some text block ends with s. The engine
// prefixes user text blocks with a timestamp ("[...] prompt"), so the bg
// grandchild's prompt block is matched by suffix; assistant tool_use input
// JSON and tool_result content live in non-text blocks and never match.
func requestHasTextBlock(req *llm.Request, s string) bool {
	for _, msg := range req.Messages {
		for _, b := range msg.Content {
			if b.Type == types.ContentTypeText && strings.HasSuffix(b.Text, s) {
				return true
			}
		}
	}
	return false
}

// Acceptance test 2 (plan 2026-10-14-bg-task-per-engine, Bug 2): a background
// task spawned by a sub-agent must land in the ROOT engine's table at spawn
// time — visible and stoppable from the root's job view while the sub is
// still mid-turn — and unrelated engines stay blind.
func TestBgTask_SubAgentTaskSurvivesSubEngineEnd(t *testing.T) {
	deps, mgr, _ := msgDeps(t)

	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)

	mp := &mockProvider{}
	// Sub turn 1: the sub emits an Agent tool_use that spawns its own
	// background grandchild and returns immediately.
	mp.addResponse(toolUseStreamEvents("test", "call_bg", "Agent",
		`{"description":"bg","prompt":"bg-grandchild-hang","run_in_background":true}`), nil)
	// Grandchild turn: hangs mid-stream (no message_stop — that event ends
	// the turn) until release closes or the kill cancels the context.
	mp.addHangingStream([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "grandchild working"}},
		{Type: "content_block_stop", Index: 0},
	}, release, func(req *llm.Request) bool {
		return requestHasTextBlock(req, "bg-grandchild-hang")
	})
	// Sub turn 2: also hangs on release — the observation window exists only
	// while the sub is alive; on the pre-routing code a task reached the
	// root's table only via a deferred hand-off at RunAgent return, missing
	// this window entirely.
	mp.addHangingStream(textStreamEvents("test", "sub done"), release, func(req *llm.Request) bool {
		return requestHasTextBlock(req, "spawn bg")
	})

	parent, parentRefs := msgWiredParent(t, mp, "engine-parent", deps)
	mgr.Add(&EngineViewState{Engine: parent, ID: "engine-parent"})
	_, refsC := msgWiredParent(t, &mockProvider{}, "engine-c", deps)

	type runResult struct {
		res *types.SubQueryResult
		err error
	}
	runDone := make(chan runResult, 1)
	go func() {
		res, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
			Prompt:    "spawn bg",
			AgentType: "General",
			MaxTurns:  2,
		})
		runDone <- runResult{res, err}
	}()

	// The grandchild's row must appear in the ROOT engine's registry while
	// the sub-agent is still mid-turn — at spawn time, not on RunAgent
	// return.
	var id string
	spawnDeadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded poll for async spawn landing
	for {
		list := parentRefs.Agent.Registry().List()
		if len(list) == 1 && list[0].Status == agenttool.AgentRunning {
			id = list[0].ID
			break
		}
		if time.Now().After(spawnDeadline) { // REAL-TIME: bounded poll for async spawn landing
			t.Fatalf("root registry holds %d agents after 2s while the sub still runs, want 1 running — a sub-spawned bg task must land in the root table at spawn time", len(list))
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
	}
	if id == "" {
		t.Fatal("root registry entry has an empty id")
	}
	if _, ok := mgr.BgAgent(id); !ok {
		t.Errorf("mgr.BgAgent(%q) missed while the sub still runs, want resolution via the root table", id)
	}

	// End the hang; the sub — and the parent's RunAgent call — then finish.
	closeRelease()
	rr := <-runDone
	if rr.err != nil {
		t.Fatalf("parent RunAgent: %v", rr.err)
	}

	// The sub ended and changed nothing: the row is still exactly where it
	// was — the root's registry, running.
	orphans := parentRefs.Agent.Registry().List()
	if len(orphans) != 1 {
		t.Fatalf("parent bg registry holds %d agents after the sub ended, want 1", len(orphans))
	}
	if orphans[0].ID != id {
		t.Errorf("parent bg registry id = %q, want %q", orphans[0].ID, id)
	}
	if orphans[0].Status != agenttool.AgentRunning {
		t.Errorf("orphan status = %q, want running", orphans[0].Status)
	}

	// The parent's job view surfaces exactly that one agent-family entry.
	pList := parentRefs.JobReg.List()
	if len(pList) != 1 {
		t.Fatalf("parent job list length = %d, want 1", len(pList))
	}
	if pList[0].ID != id {
		t.Errorf("parent job id = %q, want %q", pList[0].ID, id)
	}
	if pList[0].Status != "running" {
		t.Errorf("parent job status = %q, want running", pList[0].Status)
	}

	// Engine C stays blind to the parent's bg task.
	for _, info := range refsC.JobReg.List() {
		if info.ID == id {
			t.Fatalf("engine C's job list leaks the parent's bg task %q", id)
		}
	}
	if err := refsC.JobReg.Kill(id); !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("C's Kill(%q) error = %v, want ErrNotFound", id, err)
	}

	// The parent's kill path stops it.
	if err := parentRefs.JobReg.Kill(id); err != nil {
		t.Fatalf("parent Kill(%q): %v", id, err)
	}
	killDeadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded poll for async kill
	for {
		info, ok := parentRefs.JobReg.Get(id)
		if ok && info.Status == "killed" {
			break
		}
		if time.Now().After(killDeadline) { // REAL-TIME: bounded poll for async kill
			status := "<gone>"
			if ok {
				status = info.Status
			}
			t.Fatalf("bg task %q status = %q after parent kill (2s deadline), want killed", id, status)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
	}

	// Resolution survives the kill — the row lives in the root's table, which
	// never leaves the manager.
	if st, ok := mgr.BgAgent(id); !ok || st.Status != agenttool.AgentCancelled {
		t.Errorf("mgr.BgAgent(%q) = (%v, %v), want the cancelled entry resolvable via the root table", id, st, ok)
	}
}
