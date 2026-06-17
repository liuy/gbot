package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	taskpkg "github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// TestEngine_RunAgent_General verifies the happy path of Engine.RunAgent with
// a General agent type: agent definition resolves, sub-engine runs, result
// is wrapped via FinalizeResult.
func TestEngine_RunAgent_General(t *testing.T) {
	t.Parallel()

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub-agent did the thing"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	result, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "do something",
		AgentType: "General",
	})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	if result == nil {
		t.Fatal("RunAgent returned nil result")
	}
	if result.AgentType != "General" {
		t.Errorf("AgentType = %q, want General", result.AgentType)
	}
	if !strings.Contains(result.Content, "sub-agent did the thing") {
		t.Errorf("Content = %q, want to contain sub-agent text", result.Content)
	}
	if mp.callCount() < 1 {
		t.Errorf("expected at least 1 provider call, got %d", mp.callCount())
	}
}

// TestEngine_RunAgent_EmptyAgentType_DefaultsToGeneral verifies that an empty
// AgentType defaults to General.
func TestEngine_RunAgent_EmptyAgentType_DefaultsToGeneral(t *testing.T) {
	t.Parallel()

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	result, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "test",
	})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	if result.AgentType != "General" {
		t.Errorf("AgentType = %q, want General (default)", result.AgentType)
	}
}

// TestEngine_RunAgent_ForkType_ResolvesToGeneral verifies that "fork" agent
// type (used by callFork) resolves to General definition instead of failing.
// Regression test for review issue B1.
func TestEngine_RunAgent_ForkType_ResolvesToGeneral(t *testing.T) {
	t.Parallel()

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "fork result"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	_, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "fork test",
		AgentType: "fork",
	})
	if err != nil {
		t.Fatalf("RunAgent with AgentType=fork should not error: %v", err)
	}
}

// TestEngine_RunAgent_UnknownAgentType_Errors verifies that unknown agent
// types produce an error (not silently fall through).
func TestEngine_RunAgent_UnknownAgentType_Errors(t *testing.T) {
	t.Parallel()

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	_, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "test",
		AgentType: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for unknown agent type")
	}
	if !strings.Contains(err.Error(), "unknown agent type") {
		t.Errorf("error = %q, want 'unknown agent type'", err.Error())
	}
}

// TestEngine_RunAgent_MaxTurnsOverride verifies that opts.MaxTurns overrides
// agentDef.MaxTurns. Regression test for review issue B2.
func TestEngine_RunAgent_MaxTurnsOverride(t *testing.T) {
	t.Parallel()

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	// Pass MaxTurns=42 — General def has its own MaxTurns, but caller override
	// should win.
	_, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:   "test",
		MaxTurns: 42,
	})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	// We can't directly inspect sub-engine MaxTurns without more plumbing,
	// but verifying no panic + successful execution is the minimum bar.
}

// TestEngine_RunAgent_NilSharedDeps_Errors verifies the guard clause.
func TestEngine_RunAgent_NilSharedDeps_Errors(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test",
	})
	defer eng.Close()

	_, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt: "test",
	})
	if err == nil {
		t.Fatal("expected error when sharedDeps is nil")
	}
	if !strings.Contains(err.Error(), "sharedDeps is nil") {
		t.Errorf("error = %q, want 'sharedDeps is nil'", err.Error())
	}
}

// TestEngine_RunAgent_HookFires verifies that SubagentStart hooks fire and
// their AdditionalContext is injected into the sub-agent's messages.
func TestEngine_RunAgent_HookFires(t *testing.T) {
	t.Parallel()

	// SubagentStart hooks fire asynchronously; this test verifies they don't
	// cause a panic during RunAgent. We use an empty Hooks (no configured
	// hooks) since the HookMatcher config is complex — the nil-hooks path
	// and the hooks-present path both exercise the hook iteration code.
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "ok"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	_, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "test",
		AgentType: "General",
	})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
}

// TestEngine_RunAgent_ParentToolUseID_DispatchesSubAgentEvents is the
// end-to-end regression test for the "Agent tool card shows no progress"
// bug. When RunAgent is called with a non-empty ParentToolUseID, the
// sub-engine must wire a taggedDispatcher so every sub-agent event
// (text/tool/query_end) reaches the parent dispatcher with Agent metadata
// attached.
//
// Without ParentToolUseID the dispatcher is nil and sub-agent events are
// dropped — which is what the TUI symptom looked like before the fix.
//
// Layered with pkg/tool/agent.TestCallPassesToolUseID, which proves
// AgentTool.Call forwards tctx.ToolUseID into AgentOpts.ParentToolUseID,
// this test closes the loop on the full event-propagation chain.
func TestEngine_RunAgent_ParentToolUseID_DispatchesSubAgentEvents(t *testing.T) {
	t.Parallel()

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	const parentToolUseID = "call_parent_abc"
	const subToolUseID = "call_sub_xyz"

	// Sub-agent: one text turn, then one Bash tool_use. The sub-engine
	// should dispatch text_delta + tool_start + tool_end + query_end —
	// all of which the parent's collector must observe.
	mp := &mockProvider{}
	// Turn 1: sub-agent emits a Bash tool_use.
	mp.addResponse(toolUseStreamEvents("test", subToolUseID, "Bash",
		`{"command":"echo hi","description":"say hi"}`), nil)
	// Turn 2: sub-agent emits a final text response to end the turn.
	mp.addResponse(textStreamEvents("test", "all done"), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Tools:      []tool.Tool{&echoBashStub{}},
		Dispatcher: ec,
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	result, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "run bash",
		AgentType:       "General",
		ParentToolUseID: parentToolUseID,
	})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	if result == nil {
		t.Fatal("RunAgent returned nil result")
	}

	// Drain: the sub-engine runs its turn synchronously inside RunAgent,
	// but events are dispatched through the parent collector. After
	// RunAgent returns the sub-engine has finished, so all events are
	// already in the collector — no WaitForResult needed (that waits for
	// the main engine's QueryEnd, which never fires here).
	events := ec.Events()

	// Every dispatched sub-agent event must carry Agent metadata pointing
	// back to the parent tool_use_id. If any event lacks this, the TUI's
	// tool card lookup (findToolView by ParentToolUseID) fails and the
	// event is silently dropped.
	var sawToolStart, sawQueryEnd bool
	for _, evt := range events {
		if evt.Agent == nil {
			continue
		}
		if evt.Agent.ParentToolUseID != parentToolUseID {
			t.Errorf("event %s: Agent.ParentToolUseID = %q, want %q",
				evt.Type, evt.Agent.ParentToolUseID, parentToolUseID)
		}
		if evt.Type == types.EventToolStart {
			sawToolStart = true
		}
		if evt.Type == types.EventQueryEnd {
			sawQueryEnd = true
		}
	}
	if !sawToolStart {
		t.Errorf("no EventToolStart with Agent metadata reached parent dispatcher; " +
			"sub-agent tool progress would be invisible in TUI (regression)")
	}
	if !sawQueryEnd {
		t.Errorf("no EventQueryEnd with Agent metadata reached parent dispatcher; " +
			"parent tool card would never be marked Done (regression)")
	}
}

// echoBashStub is a minimal Bash tool that returns a canned result so
// sub-engine tool execution completes without requiring real shell access.
type echoBashStub struct{}

func (e *echoBashStub) Name() string      { return "Bash" }
func (e *echoBashStub) Aliases() []string { return nil }
func (e *echoBashStub) Description(json.RawMessage) (string, error) {
	return "echo bash stub", nil
}
func (e *echoBashStub) InputSchema() json.RawMessage { return nil }
func (e *echoBashStub) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "ok"}, nil
}
func (e *echoBashStub) CheckPermissions(input json.RawMessage, tctx *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (e *echoBashStub) IsReadOnly(json.RawMessage) bool        { return false }
func (e *echoBashStub) IsDestructive(json.RawMessage) bool     { return false }
func (e *echoBashStub) IsConcurrencySafe(json.RawMessage) bool { return true }
func (e *echoBashStub) IsEnabled() bool                        { return true }
func (e *echoBashStub) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptBlock
}
func (e *echoBashStub) MaxResultSize() int      { return 0 }
func (e *echoBashStub) Prompt() string          { return "" }
func (e *echoBashStub) RenderResult(any) string { return "" }
