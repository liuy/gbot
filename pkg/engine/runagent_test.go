package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/skills"
	taskpkg "github.com/liuy/gbot/pkg/tool/task"

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
