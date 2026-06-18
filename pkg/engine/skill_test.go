package engine

import (
	"context"
	"strings"
	"testing"

	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/skills"
	taskpkg "github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// TestRunSkill_Fork_EmitsQueryEvents verifies that RunSkill with a fork skill
// emits QueryStart and QueryEnd events (so TUI can render). Regression test
// for the "↑0 ↓0 tokens" bug where the fork path ran silently.
func TestRunSkill_Fork_EmitsQueryEvents(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:      "fork-test",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "General",
		Source:    types.SkillSourceBundled,
		Content:   "Review the code.",
	})

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub-agent review result"), nil)
	mp.addResponse(textStreamEvents("test", "main agent summary"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	defer eng.Close()

	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "fork-test", "", "test system prompt")
	result := ec.WaitForResult()

	startEvents := ec.FindEvents(types.EventQueryStart)
	endEvents := ec.FindEvents(types.EventQueryEnd)
	toolStartEvents := ec.FindEvents(types.EventToolStart)
	toolEndEvents := ec.FindEvents(types.EventToolEnd)

	if len(startEvents) == 0 {
		t.Error("RunSkill fork did not emit query_start")
	}
	if len(endEvents) == 0 {
		t.Error("RunSkill fork did not emit query_end")
	}
	if len(toolStartEvents) == 0 {
		t.Error("RunSkill fork did not emit tool_start for virtual tool card")
	}
	if len(toolEndEvents) == 0 {
		t.Error("RunSkill fork did not emit tool_end for virtual tool card")
	}
	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}

	// Critical: sub-engine events must reach dispatcher. The "↑0 ↓0 tokens"
	// bug was caused by sub-engine dispatcher being nil.
	hasTextDelta := false
	for _, evt := range ec.Events() {
		if evt.Type == types.EventTextDelta {
			hasTextDelta = true
			break
		}
	}
	if !hasTextDelta {
		t.Error("sub-engine text events did not reach dispatcher — TUI will show ↑0 ↓0 tokens")
	}

	// Critical: QueryStart must come BEFORE ToolStart so TUI has an assistant
	// message to attach the tool card to.
	queryStartIdx := -1
	toolStartIdx := -1
	for i, evt := range ec.Events() {
		if evt.Type == types.EventQueryStart && queryStartIdx == -1 {
			queryStartIdx = i
		}
		if evt.Type == types.EventToolStart && toolStartIdx == -1 {
			toolStartIdx = i
		}
	}
	if queryStartIdx == -1 || toolStartIdx == -1 {
		t.Fatal("missing QueryStart or ToolStart events")
	}
	if queryStartIdx > toolStartIdx {
		t.Errorf("QueryStart (idx %d) must come before ToolStart (idx %d) — tool card has no assistant message to attach to",
			queryStartIdx, toolStartIdx)
	}
}

// TestRunSkill_New_EmitsSubAgentEvents verifies that a skill with context=new
// is dispatched to a sub-agent (like fork), NOT treated as inline.
// Observable signals:
//   - emits virtual tool_start/tool_end (sub-agent tool card)
//   - main agent makes TWO LLM calls (sub-agent + main summary), inline makes ONE
//   - emits text_delta from sub-agent (not silent like the ↑0 ↓0 bug)
func TestRunSkill_New_EmitsSubAgentEvents(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:      "new-test",
		Type:      "prompt",
		Context:   "new",
		AgentType: "General",
		Source:    types.SkillSourceBundled,
		Content:   "Review the code.",
	})

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	// Two responses: one for sub-agent, one for main agent's summary turn.
	// If new is mis-routed as inline, only ONE response is consumed and the
	// second is never drained — that's our red signal.
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "sub-agent review result"), nil)
	mp.addResponse(textStreamEvents("test", "main agent summary"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	defer eng.Close()

	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "new-test", "", "test system prompt")
	result := ec.WaitForResult()

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Sub-agent path must emit a virtual tool card.
	if len(ec.FindEvents(types.EventToolStart)) == 0 {
		t.Error("context=new did not emit tool_start — routed as inline, not sub-agent")
	}
	if len(ec.FindEvents(types.EventToolEnd)) == 0 {
		t.Error("context=new did not emit tool_end — routed as inline, not sub-agent")
	}

	// Sub-agent must stream text events (regression for silent sub-agent bug).
	hasTextDelta := false
	for _, evt := range ec.Events() {
		if evt.Type == types.EventTextDelta {
			hasTextDelta = true
			break
		}
	}
	if !hasTextDelta {
		t.Error("sub-agent text events did not reach dispatcher — TUI will show ↑0 ↓0 tokens")
	}
}
func TestRunSkill_Inline_EmitsQueryEvents(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:    "inline-test",
		Type:    "prompt",
		Source:  types.SkillSourceBundled,
		Content: "Do the thing.",
	})

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	defer eng.Close()

	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "inline-test", "", "test system prompt")
	result := ec.WaitForResult()

	startEvents := ec.FindEvents(types.EventQueryStart)
	endEvents := ec.FindEvents(types.EventQueryEnd)

	if len(startEvents) == 0 {
		t.Errorf("RunSkill inline did not emit query_start")
	}
	if len(endEvents) == 0 {
		t.Errorf("RunSkill inline did not emit query_end")
	}
	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

// TestRunSkill_UnknownSkill_EmitsError verifies error path emits QueryEnd with error.
func TestRunSkill_UnknownSkill_EmitsError(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())

	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test",
	})
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	defer eng.Close()

	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "nonexistent", "", "test system prompt")
	result := ec.WaitForResult()

	if result.Error == nil {
		t.Fatal("expected error for unknown skill")
	}
	if !strings.Contains(result.Error.Error(), "unknown skill") {
		t.Errorf("error = %q, want 'unknown skill'", result.Error.Error())
	}
}

// TestRunSkill_Fork_PassesGitStatus verifies that a fork skill launches its
// sub-agent WITH GitStatus propagated, so the sub-agent's <env> block says
// "Is directory a git repo: Yes" instead of "No".
//
// Regression: engine.RunSkill's fork/new branch called RunAgent without
// passing GitStatus, so sub-agents launched via /<skill> (e.g. /plan →
// Planner) saw "Not a git repo" in their system prompt even when running
// inside a git repo. The AgentTool.Call path was unaffected because it
// passes t.gitStatus explicitly.
func TestRunSkill_Fork_PassesGitStatus(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:      "fork-git-test",
		Type:      "prompt",
		Context:   "fork",
		AgentType: "General",
		Source:    types.SkillSourceBundled,
		Content:   "Review the code.",
	})

	gs := &ctxbuild.GitStatusInfo{IsGit: true, Branch: "master", IsDirty: false}
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		GitStatus:  gs,
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	cp := &captureProvider{events: textStreamEvents("test", "ok")}
	eng := New(&Params{Provider: cp, Model: "test"})
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(reg)
	defer eng.Close()

	ec := newEventCollector()
	eng.SetDispatcher(ec)

	eng.RunSkill(context.Background(), "fork-git-test", "", "test system prompt")
	_ = ec.WaitForResult()

	if len(cp.allReqs) == 0 {
		t.Fatal("provider Stream not invoked — sub-agent never ran")
	}
	// The first request is the sub-agent's (fork runs sub before main summary).
	subReq := cp.allReqs[0]
	// The <env> block in the sub-agent system prompt must reflect git status.
	// Source: pkg/tool/agent/agent.go buildEnvBlock — writes "Yes" or "No".
	if !strings.Contains(string(subReq.System), "Is directory a git repo: Yes") {
		t.Errorf("sub-agent system prompt missing 'Is directory a git repo: Yes'\n--- got ---\n%s", string(subReq.System))
	}
}
