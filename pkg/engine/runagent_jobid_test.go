package engine

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// findTextDelta returns the first dispatched text_delta carrying exactly text,
// with its Agent meta intact. All three JobID tests assert on dispatched
// events, not internal fields: the stamp is only observable at the dispatcher.
func findTextDelta(ec *eventCollector, text string) (types.QueryEvent, bool) {
	for _, ev := range ec.FindEvents(types.EventTextDelta) {
		if ev.Text == text {
			return ev, true
		}
	}
	return types.QueryEvent{}, false
}

// State 1 — bg semantics: spawnBackground sets opts.AgentID to the registry
// id before RunAgent runs (agent.go runFn), so a background spawn's sub-engine
// must stamp that id as the events' JobID.
func TestRunAgent_BGJobIDStampedOnMeta(t *testing.T) {
	deps, _, _ := msgDeps(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub text"), nil)

	parent := newRunAgentEngine(&Params{Provider: mp, Model: "test", EngineID: "parent"})
	parent.SetSharedDeps(&deps)
	t.Cleanup(func() { parent.Close() })
	collector := newEventCollector()
	parent.SetDispatcher(collector)

	if _, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		AgentID:         "agent-9",
		ParentToolUseID: "tu1",
		AgentType:       "General",
		Prompt:          "bg job",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	ev, ok := findTextDelta(collector, "sub text")
	if !ok {
		t.Fatal("no text_delta with the sub's text reached the root dispatcher")
	}
	if ev.Agent == nil {
		t.Fatal("sub event carries no Agent meta — the taggedDispatcher contract is broken")
	}
	if ev.Agent.ParentToolUseID != "tu1" {
		t.Errorf("meta.ParentToolUseID = %q, want tu1", ev.Agent.ParentToolUseID)
	}
	if ev.Agent.JobID != "agent-9" {
		t.Errorf("meta.JobID = %q, want agent-9 (a bg spawn's registry id is its job id)", ev.Agent.JobID)
	}
}

// State 2 — inheritance: a sync grandchild spawned by the background job's
// own Agent tool inherits the job's id (its events address the same feed) and
// carries the inner tool_use id as its parent. RunAgent returns no sub-engine
// handle, so the grandchild is driven by the job's LLM script: turn 1 emits a
// sync Agent tool_use, the grandchild consumes the next response, and the
// job's closing turn consumes the last.
func TestRunAgent_SyncGrandchildInheritsJobID(t *testing.T) {
	deps, mgr, _ := msgDeps(t)
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test", "tu_inner", "Agent",
		`{"description":"gc","prompt":"gc"}`), nil)
	mp.addResponse(textStreamEvents("test", "gc text"), nil)
	mp.addResponse(textStreamEvents("test", "job done"), nil)

	parent, _ := msgWiredParent(t, mp, "engine-parent", deps)
	mgr.Add(&EngineViewState{Engine: parent, ID: "engine-parent"})
	collector := newEventCollector()
	parent.SetDispatcher(collector)

	if _, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		AgentID:         "agent-9",
		ParentToolUseID: "tu_job",
		AgentType:       "General",
		MaxTurns:        2,
		Prompt:          "run gc",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	gc, ok := findTextDelta(collector, "gc text")
	if !ok {
		t.Fatal("grandchild text never reached the root dispatcher")
	}
	if gc.Agent == nil {
		t.Fatal("grandchild event carries no Agent meta")
	}
	if gc.Agent.JobID != "agent-9" {
		t.Errorf("grandchild meta.JobID = %q, want agent-9 (inherited from the job's ambient id)", gc.Agent.JobID)
	}
	if gc.Agent.ParentToolUseID != "tu_inner" {
		t.Errorf("grandchild meta.ParentToolUseID = %q, want tu_inner (the job's Agent tool_use id)", gc.Agent.ParentToolUseID)
	}

	own, ok := findTextDelta(collector, "job done")
	if !ok {
		t.Fatal("the job's own closing text never reached the root dispatcher")
	}
	if own.Agent == nil || own.Agent.JobID != "agent-9" {
		t.Errorf("job's own closing meta = %+v, want JobID agent-9", own.Agent)
	}
	if own.Agent.ParentToolUseID != "tu_job" {
		t.Errorf("job's own closing meta.ParentToolUseID = %q, want tu_job", own.Agent.ParentToolUseID)
	}
}

// State 3 — root sync: a sync sub-agent spawned outside any background job
// must carry an empty JobID (chat keeps nesting its events).
func TestRunAgent_RootSyncSpawnNoJobID(t *testing.T) {
	deps, _, _ := msgDeps(t)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "root sync text"), nil)

	parent := newRunAgentEngine(&Params{Provider: mp, Model: "test", EngineID: "parent"})
	parent.SetSharedDeps(&deps)
	t.Cleanup(func() { parent.Close() })
	collector := newEventCollector()
	parent.SetDispatcher(collector)

	if _, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		ParentToolUseID: "tu_sync",
		AgentType:       "General",
		Prompt:          "sync",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	ev, ok := findTextDelta(collector, "root sync text")
	if !ok {
		t.Fatal("no text_delta with the sub's text reached the root dispatcher")
	}
	if ev.Agent == nil {
		t.Fatal("sub event carries no Agent meta")
	}
	if ev.Agent.JobID != "" {
		t.Errorf("meta.JobID = %q, want \"\" (a root-level sync sub-agent is outside every background job)", ev.Agent.JobID)
	}
	if ev.Agent.ParentToolUseID != "tu_sync" {
		t.Errorf("meta.ParentToolUseID = %q, want tu_sync", ev.Agent.ParentToolUseID)
	}
}

// taggedLogCapture records slog records so a test can assert on the
// tagged:dispatch line — the e2e jobID verification greps that log, and the
// field has no other observable surface (mutants deleting it escape without
// this).
type taggedLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *taggedLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *taggedLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r.Clone())
	c.mu.Unlock()
	return nil
}
func (c *taggedLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *taggedLogCapture) WithGroup(string) slog.Handler      { return c }

func (c *taggedLogCapture) firstTaggedOfType(typ string) (slog.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message != "tagged:dispatch" {
			continue
		}
		if v, _ := attrValue(r, "type"); v == typ {
			return r, true
		}
	}
	return slog.Record{}, false
}

func attrValue(r slog.Record, key string) (string, bool) {
	out, found := "", false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out, found = a.Value.String(), true
		}
		return true
	})
	return out, found
}

// TestTaggedDispatch_LogCarriesJobID pins the dispatch log line: the record
// exists (statement removal), carries jobID=agent-9 (field pair removal), and
// logs a truncated prefix of the text (full-text logging is the mutant — the
// truncation width itself is a knob, not a contract, so only the prefix and
// the cut are pinned).
func TestTaggedDispatch_LogCarriesJobID(t *testing.T) {
	deps, _, _ := msgDeps(t)
	mp := &mockProvider{}
	const textLen = 80
	mp.addResponse(textStreamEvents("test", strings.Repeat("x", textLen)), nil)

	parent := newRunAgentEngine(&Params{Provider: mp, Model: "test", EngineID: "parent"})
	parent.SetSharedDeps(&deps)
	t.Cleanup(func() { parent.Close() })
	parent.SetDispatcher(newEventCollector())

	logs := &taggedLogCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })

	if _, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		AgentID:         "agent-9",
		ParentToolUseID: "tu1",
		AgentType:       "General",
		Prompt:          "bg job",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	rec, ok := logs.firstTaggedOfType(string(types.EventTextDelta))
	if !ok {
		t.Fatal("no tagged:dispatch record for the sub's text_delta — the dispatch log line is gone")
	}
	if v, found := attrValue(rec, "jobID"); !found || v != "agent-9" {
		t.Errorf("tagged:dispatch jobID attr = %q (found=%v), want agent-9", v, found)
	}
	v, _ := attrValue(rec, "text")
	if !strings.HasPrefix(v, "xxxx") || len(v) >= textLen {
		t.Errorf("tagged:dispatch text attr = %q, want a truncated prefix of the %d-rune text", v, textLen)
	}
}
