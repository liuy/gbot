package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/message"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// msgDeps builds SharedDeps carrying the resolver's manager plus a shared id
// minter, plus a standalone registry for direct resolver-literal tests (to be
// reachable it must be attached to an owner engine via addTableOwner; wired
// engines mint their own per-engine table inside CreateTools — deps carries
// no shared registry).
func msgDeps(t *testing.T) (SharedDeps, *EngineManager, *agenttool.BackgroundAgentRegistry) {
	t.Helper()
	mgr := NewEngineManager()
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
		EngineMgr:  mgr,
		IDMinter:   agenttool.NewIDMinter(),
	}
	return deps, mgr, agenttool.NewBackgroundAgentRegistry()
}

// spawnRunningAgent pre-seeds the registry with a running agent whose runFn
// blocks until the test cleans up, so its status stays AgentRunning.
func spawnRunningAgent(t *testing.T, reg *agenttool.BackgroundAgentRegistry) string {
	t.Helper()
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	state, err := reg.Spawn(
		context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-blocked
			return &types.SubQueryResult{}, nil
		},
		nil, "test agent", "call_1", "agent",
	)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return state.ID
}

// msgWiredParent builds a parent engine wired through the production path:
// CreateTools + WireEngine + ToolsProvider from the same refs, so its tool
// set contains the wired Agent and message tools.
func msgWiredParent(t *testing.T, mp *mockProvider, engineID string, deps SharedDeps) (*Engine, ToolRefs) {
	t.Helper()
	tl := task.NewList(t.TempDir())
	refs := CreateTools(deps, tl)
	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		EngineID:      engineID,
		ToolsProvider: refs.Reg.ToolMap,
	})
	t.Cleanup(func() { eng.Close() })
	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)
	eng.SetSharedDeps(&deps)
	return eng, refs
}

// ---------------------------------------------------------------------------
// Resolver
// ---------------------------------------------------------------------------

// The background-agent registry is consulted first: an id present in both
// the registry and the manager resolves to the registry's sink.
func TestEngineMessageResolver_AgentRegistryFirst(t *testing.T) {
	t.Parallel()
	_, mgr, reg := msgDeps(t)
	addTableOwner(t, mgr, "bg-owner", reg)
	id := spawnRunningAgent(t, reg)
	sink := &msgSpyTarget{}
	reg.AttachSink(id, sink)

	engineView := New(&Params{Provider: &mockProvider{}, EngineID: id})
	defer engineView.Close()
	mgr.Add(&EngineViewState{Engine: engineView, ID: id})

	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve(id)
	if !ok {
		t.Fatal("Resolve returned ok=false for a registered running agent")
	}
	if status != "running" {
		t.Errorf("status = %q, want running", status)
	}
	if target != message.Target(sink) {
		t.Errorf("target = %v, want the registry's sink", target)
	}
}

func TestEngineMessageResolver_EngineByID(t *testing.T) {
	t.Parallel()
	_, mgr, _ := msgDeps(t)
	eng := New(&Params{Provider: &mockProvider{}, EngineID: "e2"})
	defer eng.Close()
	mgr.Add(&EngineViewState{Engine: eng, ID: "e2"})

	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve("e2")
	if !ok {
		t.Fatal("Resolve returned ok=false for a registered engine id")
	}
	if status != "" {
		t.Errorf("status = %q, want empty for an engine target", status)
	}
	if target != message.Target(eng) {
		t.Errorf("target = %v, want the exact registered engine", target)
	}
}

func TestEngineMessageResolver_TerminalAgent(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"completed", "failed", "cancelled"} {
		_, mgr, reg := msgDeps(t)
		addTableOwner(t, mgr, "bg-owner", reg)
		var cancel context.CancelFunc
		ctx := context.Background()
		if want == "cancelled" {
			// Spawn's goroutine maps a cancelled child ctx + error to
			// AgentCancelled; any other error maps to AgentFailed.
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		runFn := func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			switch want {
			case "failed":
				return nil, errors.New("boom")
			case "cancelled":
				// childCtx is cancelled — the registry maps ctx errors to
				// AgentCancelled, any other error to AgentFailed.
				return nil, ctx.Err()
			default:
				return &types.SubQueryResult{}, nil
			}
		}
		st, err := reg.Spawn(ctx, runFn, nil, "s", "call_1", "agent")
		if cancel != nil {
			cancel()
		}
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		final, ok := reg.Wait(st.ID)
		if !ok {
			t.Fatalf("Wait(%s) returned false", st.ID)
		}
		if string(final.Status) != want {
			t.Fatalf("setup: status = %q, want %q", final.Status, want)
		}
		r := &EngineMessageResolver{mgr: mgr}
		target, status, ok := r.Resolve(st.ID)
		if !ok {
			t.Errorf("status %s: ok=false, want a known terminal agent", want)
		}
		if status != want {
			t.Errorf("status = %q, want %q", status, want)
		}
		if target != nil {
			t.Errorf("target = %v, want nil for a terminal agent", target)
		}
	}
}

func TestEngineMessageResolver_StartingAgent(t *testing.T) {
	t.Parallel()
	_, mgr, reg := msgDeps(t)
	addTableOwner(t, mgr, "bg-owner", reg)
	id := spawnRunningAgent(t, reg) // no AttachSink — still starting

	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve(id)
	if !ok {
		t.Fatal("ok=false for a running agent without a sink")
	}
	if status != "starting" {
		t.Errorf("status = %q, want starting", status)
	}
	if target != nil {
		t.Errorf("target = %v, want nil while starting", target)
	}
}

func TestEngineMessageResolver_UnknownID(t *testing.T) {
	t.Parallel()
	_, mgr, _ := msgDeps(t)
	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve("nobody")
	if ok {
		t.Error("ok=true for an unknown id")
	}
	if target != nil || status != "" {
		t.Errorf("target = %v, status = %q, want zero values", target, status)
	}
}

func TestEngineMessageResolver_NameFallback(t *testing.T) {
	t.Parallel()
	_, mgr, _ := msgDeps(t)
	eng := New(&Params{Provider: &mockProvider{}, EngineID: "main"})
	defer eng.Close()
	mgr.Add(&EngineViewState{Engine: eng, ID: "main", Name: "research"})

	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve("research")
	if !ok {
		t.Fatal("Resolve by engine name returned ok=false")
	}
	if status != "" {
		t.Errorf("status = %q, want empty for an engine target", status)
	}
	if target != message.Target(eng) {
		t.Errorf("target = %v, want the engine named research", target)
	}
}

func TestEngineMessageResolver_IDBeatsName(t *testing.T) {
	t.Parallel()
	_, mgr, _ := msgDeps(t)
	a := New(&Params{Provider: &mockProvider{}, EngineID: "x1"})
	defer a.Close()
	b := New(&Params{Provider: &mockProvider{}, EngineID: "dup"})
	defer b.Close()
	mgr.Add(&EngineViewState{Engine: a, ID: "x1", Name: "dup"})
	mgr.Add(&EngineViewState{Engine: b, ID: "dup"})

	r := &EngineMessageResolver{mgr: mgr}
	target, _, ok := r.Resolve("dup")
	if !ok {
		t.Fatal("Resolve(dup) returned ok=false")
	}
	if target != message.Target(b) {
		t.Errorf("target = %v, want engine B (id match wins over name match)", target)
	}
}

// A registry id beats an engine NAME that shadows it (legacy pre-validation
// state where an engine could be named agent-1).
func TestEngineMessageResolver_RegistryIDBeatsEngineName(t *testing.T) {
	t.Parallel()
	_, mgr, reg := msgDeps(t)
	addTableOwner(t, mgr, "bg-owner", reg)
	id := spawnRunningAgent(t, reg)
	sink := &msgSpyTarget{}
	reg.AttachSink(id, sink)

	eng := New(&Params{Provider: &mockProvider{}, EngineID: "legacy"})
	defer eng.Close()
	mgr.Add(&EngineViewState{Engine: eng, ID: "legacy", Name: id})

	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve(id)
	if !ok {
		t.Fatal("Resolve returned ok=false")
	}
	if status != "running" {
		t.Errorf("status = %q, want running (registry wins)", status)
	}
	if target != message.Target(sink) {
		t.Errorf("target = %v, want the registry's sink", target)
	}
}

// Lazy view states are undeliverable by name too — the same nil-Engine
// check the id path applies.
func TestEngineMessageResolver_NameOnLazyEngineNotDeliverable(t *testing.T) {
	t.Parallel()
	_, mgr, _ := msgDeps(t)
	mgr.Add(&EngineViewState{ID: "x", Name: "lazy-name"}) // Engine nil

	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve("lazy-name")
	if ok {
		t.Error("ok=true for a lazy view state resolved by name")
	}
	if target != nil || status != "" {
		t.Errorf("target = %v, status = %q, want zero values", target, status)
	}
}

func TestEngineMessageResolver_UnknownName(t *testing.T) {
	t.Parallel()
	_, mgr, _ := msgDeps(t)
	r := &EngineMessageResolver{mgr: mgr}
	target, status, ok := r.Resolve("no-such-name")
	if ok {
		t.Error("ok=true for an unknown name")
	}
	if target != nil || status != "" {
		t.Errorf("target = %v, status = %q, want zero values", target, status)
	}
}

func TestEngineMessageResolver_NilViewEngine(t *testing.T) {
	t.Parallel()
	_, mgr, _ := msgDeps(t)
	mgr.Add(&EngineViewState{ID: "lazy"}) // Engine nil, like meta.json views

	r := &EngineMessageResolver{mgr: mgr}
	_, _, ok := r.Resolve("lazy")
	if ok {
		t.Error("ok=true for a view state with a nil Engine")
	}
}

// Both sources nil (reload's harvest toolset) must degrade to not-found
// without touching the nil manager — EngineManager.Get takes m.mu.RLock,
// which panics on a nil receiver.
func TestEngineMessageResolver_NilDeps(t *testing.T) {
	t.Parallel()
	r := &EngineMessageResolver{}
	target, status, ok := r.Resolve("main")
	if ok {
		t.Error("ok=true with nil resolver deps")
	}
	if target != nil || status != "" {
		t.Errorf("target = %v, status = %q, want zero values", target, status)
	}
}

// ---------------------------------------------------------------------------
// CreateTools / WireEngine wiring
// ---------------------------------------------------------------------------

func TestCreateTools_RegistersMessageTool(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps, _, _ := msgDeps(t)

	refs := CreateTools(deps, tl)
	if _, ok := refs.Reg.ToolMap()["Message"]; !ok {
		t.Error("message tool missing from the tool map with resolver deps present")
	}
	if refs.Message == nil {
		t.Fatal("ToolRefs.Message is nil with resolver deps present")
	}

	// Harvest path (reload): nil EngineMgr and nil IDMinter still register
	// the tool so ToolPrompts collection sees it.
	plain := SharedDeps{WorkingDir: t.TempDir()}
	refs2 := CreateTools(plain, tl)
	if _, ok := refs2.Reg.ToolMap()["Message"]; !ok {
		t.Error("message tool missing from the tool map on the harvest path")
	}
	if refs2.Message == nil {
		t.Fatal("ToolRefs.Message is nil on the harvest path")
	}
}

func TestWireEngine_SetsSelfIDAndEngineID(t *testing.T) {
	t.Parallel()
	deps, _, _ := msgDeps(t)
	_, refs := msgWiredParent(t, &mockProvider{}, "e2", deps)

	if got := refs.Message.SelfID(); got != "e2" {
		t.Errorf("refs.Message.SelfID() = %q, want e2", got)
	}
	if got := refs.Agent.EngineID(); got != "e2" {
		t.Errorf("refs.Agent.EngineID() = %q, want e2", got)
	}
}

// RunAgent must attach the freshly built sub-engine as the background
// agent's sink, so the registry entry becomes deliverable.
func TestRunAgent_BackgroundWiring(t *testing.T) {
	t.Parallel()
	deps, mgr, reg := msgDeps(t)
	id := spawnRunningAgent(t, reg)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub done"), nil)
	eng := newRunAgentEngine(&Params{Provider: mp, Model: "test", EngineID: "main"})
	defer eng.Close()
	eng.SetSharedDeps(&deps)
	mgr.Add(&EngineViewState{Engine: eng, ID: "main"})

	result, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:     "run",
		AgentID:    id,
		MaxTurns:   1,
		BgRegistry: reg,
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if result == nil || result.Content != "sub done" {
		t.Errorf("result = %+v, want sub done", result)
	}
	got, ok := reg.Get(id)
	if !ok {
		t.Fatal("registry lost the running agent")
	}
	if got.Sink == nil {
		t.Fatal("Sink is nil after RunAgent — sub-engine was never attached")
	}
	if got.Status != agenttool.AgentRunning {
		t.Errorf("status = %q, want running (blocked runFn)", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Integration: RunAgent wiring → resolver → message tool → queue → LLM request
// ---------------------------------------------------------------------------

// msgSpyTarget records enqueued items for direct delivery assertions.
type msgSpyTarget struct {
	mu    sync.Mutex
	items []types.QueuedItem
}

func (s *msgSpyTarget) EnqueueAttachment(item types.QueuedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
}

// A background agent delivering to its parent engine goes through the real
// queue and the parent's idle auto-turn: the parent's next LLM request must
// carry the envelope, and the child's next request must carry the tool
// result — if the call errored, "Delivered to main" never appears.
func TestEngine_BackgroundAgentMessagesParent(t *testing.T) {
	t.Parallel()
	deps, mgr, _ := msgDeps(t)

	mp := &mockProvider{}
	parent, refs := msgWiredParent(t, mp, "main", deps)
	reg := refs.Agent.BgRegistry()
	id := spawnRunningAgent(t, reg) // agent exists before RunAgent attaches its sink

	mp.addResponse(toolUseStreamEvents("test", "t1", "Message", `{"to":"main","message":"hello main"}`), nil)
	mp.addResponse(textStreamEvents("test", "child final"), nil)
	mp.addResponse(textStreamEvents("test", "parent auto-turn ack"), nil)

	parent.SetSystemPrompt("parent system prompt")
	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})

	if _, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:     "report to base",
		AgentID:    id,
		MaxTurns:   2,
		BgRegistry: reg,
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	waitForCallCount(t, mp, 3, 10*time.Second)
	if !mp.anyRequestContains(fmt.Sprintf(`<message from=%q>`, id), "hello main") {
		t.Error("no recorded request carries the agent envelope — delivery through the queue + auto-turn failed")
	}
	if !mp.anyRequestContains("hello main") {
		t.Error("no recorded request carries the sent-message tool result — the message call errored")
	}
}

// The sub-agent's own id must be wired as its selfID: a call addressed to
// itself is rejected with the self-send error in the sub's next request.
func TestEngine_BackgroundAgentSelfSendRejected(t *testing.T) {
	t.Parallel()
	deps, mgr, _ := msgDeps(t)

	mp := &mockProvider{}
	parent, refs := msgWiredParent(t, mp, "main", deps)
	reg := refs.Agent.BgRegistry()
	id := spawnRunningAgent(t, reg) // pre-seed so the sink attach fires

	mp.addResponse(toolUseStreamEvents("test", "t1", "Message", fmt.Sprintf(`{"to":%q,"message":"note to self"}`, id)), nil)
	mp.addResponse(textStreamEvents("test", "child final"), nil)

	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})

	if _, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:     "talk to yourself",
		AgentID:    id,
		MaxTurns:   2,
		BgRegistry: reg,
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	waitForCallCount(t, mp, 2, 10*time.Second)
	if !mp.anyRequestContains(fmt.Sprintf("cannot send a message to yourself (%s)", id)) {
		t.Error("no recorded request carries the self-send rejection — RunAgent did not set the sub's selfID")
	}
}

// Parent → background agent delivery: the parent's message tool resolves the
// pre-seeded agent and enqueues into its live sub-engine, whose auto-turn
// request carries the envelope with the parent engine id as sender.
func TestEngine_ParentMessagesBackgroundAgent(t *testing.T) {
	t.Parallel()
	deps, mgr, _ := msgDeps(t)

	mp := &mockProvider{}
	parent, refs := msgWiredParent(t, mp, "main", deps)
	reg := refs.Agent.BgRegistry()
	id := spawnRunningAgent(t, reg) // agent exists before RunAgent attaches its sink

	mp.addResponse(textStreamEvents("test", "first sub run"), nil)
	mp.addResponse(textStreamEvents("test", "sub auto-turn ack"), nil)

	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})

	// Live sub-engine: RunAgent completes and leaves the sub deliverable.
	// MaxTurns stays unset — a capped sub would hit its turn limit before
	// the auto-turn triggered by the incoming message can run.
	if _, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:     "one shot",
		AgentID:    id,
		BgRegistry: reg,
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	got, ok := reg.Get(id)
	if !ok || got.Sink == nil {
		t.Fatalf("agent %s has no sink after RunAgent", id)
	}

	res, err := refs.Message.Call(context.Background(), json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"status update"}`, id)), nil)
	if err != nil {
		t.Fatalf("message call: %v", err)
	}
	if res == nil {
		t.Fatal("message call returned nil")
	}

	waitForCallCount(t, mp, 2, 10*time.Second)
	if !mp.anyRequestContains(`<message from="main">`, "status update") {
		t.Error("no recorded request carries the parent envelope — parent→child delivery failed")
	}
}

// A sync sub-agent (no AgentID) must fall back to its parent engine id as
// its messaging identity: the envelope reads from="main", not from="".
// The parent is driven through a real Query so its turn actually requests
// the Agent tool; the foreground sub blocks the parent's tool exec, so the
// scripted slot order parent→sub→sub→parent→e2 is race-free.
func TestEngine_SyncSubIdentityFallsBackToParent(t *testing.T) {
	t.Parallel()
	deps, mgr, _ := msgDeps(t)

	mp := &mockProvider{}
	// Parent turn 1: foreground Agent call — the sub runs inside its tool exec.
	mp.addResponse(toolUseStreamEvents("test", "t1", "Agent", `{"description":"probe","prompt":"report to e2"}`), nil)
	// Sub turn 1: message e2.
	mp.addResponse(toolUseStreamEvents("test", "t2", "Message", `{"to":"e2","message":"hello e2"}`), nil)
	// Sub turn 2: final text — returns to the parent's tool exec.
	mp.addResponse(textStreamEvents("test", "sub final"), nil)
	// Parent turn 2: final text.
	mp.addResponse(textStreamEvents("test", "parent final"), nil)
	// e2's auto-turn after the envelope lands.
	mp.addResponse(textStreamEvents("test", "e2 auto-turn ack"), nil)

	parent, _ := msgWiredParent(t, mp, "main", deps)
	parent.SetSystemPrompt("parent system prompt")
	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})

	e2 := New(&Params{Provider: mp, Model: "test", EngineID: "e2"})
	defer e2.Close()
	e2.SetSystemPrompt("e2 system prompt")
	mgr.Add(&EngineViewState{Engine: e2, ID: "e2"})

	res := parent.QuerySync(context.Background(), "spawn a sync sub", "")
	if res.Error != nil {
		t.Fatalf("parent query: %v", res.Error)
	}

	waitForCallCount(t, mp, 5, 15*time.Second)
	if !mp.anyRequestContains(`<message from="main">`, "hello e2") {
		t.Error("no recorded request carries the from=main envelope — sync sub identity fallback failed (empty selfID or self-reject)")
	}
}

// A background agent's own background spawn (grandchild) must name the
// agent as the spawning engine: 由 engine:agent-1, not engine:.
// The parent query is capped at MaxTurns 1 so the parent stops right after
// the background spawn — agent-1's first turn is then the only contender
// for the next scripted slot, and the grandchild line is deterministic.
func TestEngine_GrandchildSpawnIdentityLine(t *testing.T) {
	t.Parallel()
	deps, mgr, _ := msgDeps(t)

	mp := &mockProvider{}
	// Parent turn 1: spawn background agent (MaxTurns 1 ends the parent here).
	mp.addResponse(toolUseStreamEvents("test", "p1", "Agent", `{"description":"worker","prompt":"work","run_in_background":true}`), nil)
	// agent-1 turn 1: spawn its own background grandchild.
	mp.addResponse(toolUseStreamEvents("test", "a1", "Agent", `{"description":"gc","prompt":"help","run_in_background":true,"name":"gc"}`), nil)
	// Grandchild and agent-1 follow-ups: text either way — the two slots may
	// race, but text responses are side-effect-free for the scan.
	mp.addResponse(textStreamEvents("test", "gc done"), nil)
	mp.addResponse(textStreamEvents("test", "agent-1 done"), nil)

	tl := task.NewList(t.TempDir())
	refs := CreateTools(deps, tl)
	parent := New(&Params{
		Provider:      mp,
		Model:         "test",
		EngineID:      "main",
		MaxTurns:      1,
		ToolsProvider: refs.Reg.ToolMap,
	})
	defer parent.Close()
	WireEngine(parent, refs, deps)
	parent.SetToolRefs(refs)
	parent.SetSharedDeps(&deps)
	parent.SetSystemPrompt("parent system prompt")
	mgr.Add(&EngineViewState{Engine: parent, ID: "main"})

	res := parent.QuerySync(context.Background(), "spawn the worker", "")
	if res.Error != nil {
		t.Fatalf("parent query: %v", res.Error)
	}

	waitForCallCount(t, mp, 4, 15*time.Second)
	if !mp.anyRequestContains("你是后台 agent-2（gc），由 engine:agent-1 派生") {
		t.Error("no recorded request carries the grandchild identity line naming engine:agent-1 — SetEngineID propagation failed")
	}
}
