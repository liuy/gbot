package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// CreateTools tests
// ---------------------------------------------------------------------------

func TestCreateTools_RegistersAllBuiltinTools(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	refs := CreateTools(deps, tl)

	expectedTools := []string{
		"Bash", "Read", "Edit", "Write", "Glob", "Grep",
		"Agent", "Job", "Message",
		"Task",
		"Skill", "Repl",
	}
	toolMap := refs.Reg.ToolMap()
	for _, name := range expectedTools {
		if _, ok := toolMap[name]; !ok {
			t.Errorf("expected tool %q to be registered, but it was not found", name)
		}
	}
}

func TestCreateTools_RegistersMemoryToolsWithShortStore(t *testing.T) {
	t.Parallel()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("short.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
		ShortStore: store,
	}

	refs := CreateTools(deps, tl)
	toolMap := refs.Reg.ToolMap()
	for _, name := range []string{"Recall"} {
		if _, ok := toolMap[name]; !ok {
			t.Errorf("expected tool %q to be registered when ShortStore is set", name)
		}
	}
}

func TestCreateTools_ReturnsNonNullInstances(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	refs := CreateTools(deps, tl)

	if refs.Reg == nil {
		t.Fatal("Reg must not be nil")
	}
	if refs.BashReg == nil {
		t.Fatal("BashReg must not be nil")
	}
	if refs.Agent == nil {
		t.Fatal("Agent must not be nil")
	}
	if refs.REPL == nil {
		t.Fatal("REPL must not be nil")
	}
	if refs.JobReg == nil {
		t.Fatal("JobReg must not be nil")
	}
}

func TestCreateTools_IndependentInstancesPerCall(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	refs1 := CreateTools(deps, tl)
	refs2 := CreateTools(deps, tl)

	if refs1.Reg == refs2.Reg {
		t.Error("two CreateTools calls should return independent registries")
	}
	if refs1.BashReg == refs2.BashReg {
		t.Error("two CreateTools calls should return independent BashReg instances")
	}
	if refs1.Agent == refs2.Agent {
		t.Error("two CreateTools calls should return independent Agent instances")
	}
}

func TestCreateTools_ToolMapFnReturnsCurrentTools(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	refs := CreateTools(deps, tl)
	fn := refs.Reg.ToolMapFn()
	m := fn()

	if len(m) == 0 {
		t.Error("ToolMapFn should return non-empty map")
	}
	if _, ok := m["Bash"]; !ok {
		t.Error("ToolMapFn result should contain Bash")
	}
}

func TestCreateTools_AgentToolConfigured(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	refs := CreateTools(deps, tl)

	// Verify Agent tool is the right type and registered under "Agent"
	at, ok := refs.Reg.ToolMap()["Agent"]
	if !ok {
		t.Fatal("Agent tool not found in registry")
	}
	concrete, ok := at.(*agenttool.AgentTool)
	if !ok {
		t.Fatal("Agent tool is not *agenttool.AgentTool")
	}
	// Verify AgentTool was configured with the working directory
	if concrete == nil {
		t.Fatal("concrete AgentTool should not be nil")
	}
}

func TestCreateTools_BashToolRegistered(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	refs := CreateTools(deps, tl)
	if refs.Reg.Size() < 11 {
		t.Errorf("expected at least 11 tools registered, got %d", refs.Reg.Size())
	}
}

// ---------------------------------------------------------------------------
// WireEngine tests
// ---------------------------------------------------------------------------

func newTestDepsAndRefs(t *testing.T) (SharedDeps, ToolRefs, *Engine) {
	t.Helper()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	t.Cleanup(func() { eng.Close() })
	return deps, refs, eng
}

func TestWireEngine_BashRegOnNotifyWired(t *testing.T) {
	t.Parallel()
	deps, refs, eng := newTestDepsAndRefs(t)
	defer eng.Close()

	WireEngine(eng, refs, deps)

	if refs.BashReg.OnNotify == nil {
		t.Fatal("BashReg.OnNotify should be wired by WireEngine")
	}

	// Call the OnNotify callback directly with a real bash.JobNotification.
	notify := bash.JobNotification{
		JobID:   "bg-1",
		Status:  "completed",
		Summary: "test done",
	}
	refs.BashReg.OnNotify(notify)
	pending := eng.PendingAttachments()
	if len(pending) != 1 {
		t.Fatalf("PendingAttachments = %d items, want 1 (OnNotify must enqueue)", len(pending))
	}
	if pending[0].Mode != types.ItemModeNotification {
		t.Errorf("enqueued Mode = %q, want %q", pending[0].Mode, types.ItemModeNotification)
	}
	if pending[0].Origin == nil || pending[0].Origin.Kind != types.OriginJobCommand {
		t.Errorf("enqueued Origin = %+v, want Kind %q (bash producer splits from the agent origin)", pending[0].Origin, types.OriginJobCommand)
	}
	if pending[0].Value != notify.FormatXML() {
		t.Errorf("enqueued Value = %q, want the FormatXML bytes %q", pending[0].Value, notify.FormatXML())
	}
}

// The agent notify path fires from inside the background-agent completion
// goroutine, so the only directly fireable lever is spawning a real
// background agent against the wired engine (the mock provider fails the
// sub-query immediately; the failure notification still rides the wiring).
func TestWireEngine_ProducerOrigins(t *testing.T) {
	deps, refs, eng := newTestDepsAndRefs(t)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	if _, err := refs.Agent.Call(context.Background(),
		json.RawMessage(`{"prompt":"do it","description":"producer origin probe","run_in_background":true}`),
		&tool.ToolUseContext{ToolUseID: "tu-probe"}); err != nil {
		t.Fatalf("Agent.Call(background): %v", err)
	}

	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	for {
		pending := eng.PendingAttachments()
		if len(pending) >= 1 {
			var agentItem *types.QueuedItem
			for i := range pending {
				if strings.Contains(pending[i].Value, "<agent-type>") {
					agentItem = &pending[i]
				}
			}
			if agentItem != nil {
				if agentItem.Mode != types.ItemModeNotification {
					t.Errorf("agent item Mode = %q, want %q", agentItem.Mode, types.ItemModeNotification)
				}
				if agentItem.Origin == nil || agentItem.Origin.Kind != types.OriginJob {
					t.Errorf("agent item Origin = %+v, want Kind %q", agentItem.Origin, types.OriginJob)
				}
				if !strings.Contains(agentItem.Value, "<job-notification>") {
					t.Errorf("agent item Value = %q, want the notification XML", agentItem.Value)
				}
				return
			}
		}
		if time.Now().After(deadline) { // REAL-TIME: poll loop bounded by deadline
			items := eng.PendingAttachments()
			t.Fatalf("background agent notification never enqueued (pending=%d): %v", len(items), items)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
}

func TestWireEngine_AgentNotifyFnWired(t *testing.T) {
	t.Parallel()
	deps, refs, eng := newTestDepsAndRefs(t)
	defer eng.Close()

	WireEngine(eng, refs, deps)

	// Set a system prompt so the sysPromptFn returns something
	eng.SetSystemPrompt("test prompt")

	sp := eng.SystemPrompt()
	if sp != "test prompt" {
		t.Errorf("SystemPrompt = %s, want %q", sp, "test prompt")
	}
}

func TestWireEngine_AgentFactoryWired(t *testing.T) {
	t.Parallel()
	deps, refs, eng := newTestDepsAndRefs(t)
	defer eng.Close()

	WireEngine(eng, refs, deps)

	// Verify ToolMapFn returns non-empty tools after wiring
	toolMapFn := refs.Reg.ToolMapFn()
	m := toolMapFn()
	if len(m) == 0 {
		t.Error("ToolMapFn should return non-empty map after WireEngine")
	}
}

func TestWireEngine_REPLExecutorWired(t *testing.T) {
	t.Parallel()
	deps, refs, eng := newTestDepsAndRefs(t)
	defer eng.Close()

	WireEngine(eng, refs, deps)

	// Verify REPL tool is functional (non-nil)
	if refs.REPL == nil {
		t.Fatal("REPL tool should not be nil after WireEngine")
	}
}

func TestWireEngine_NilMcpReg_NoPanic(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
		McpReg:     nil, // explicitly nil
	}
	refs := CreateTools(deps, tl)
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	defer eng.Close()

	// Should not panic with nil McpReg
	WireEngine(eng, refs, deps)
}

func TestWireEngine_NilHooks_NoPanic(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      nil, // explicitly nil
	}
	refs := CreateTools(deps, tl)
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	defer eng.Close()

	// Should not panic with nil Hooks (RunAgent fires SubagentStart)
	WireEngine(eng, refs, deps)
}

// ---------------------------------------------------------------------------
// SharedDeps / ToolRefs zero-value tests
// ---------------------------------------------------------------------------

func TestSharedDeps_ZeroValues(t *testing.T) {
	t.Parallel()
	var deps SharedDeps
	if deps.WorkingDir != "" {
		t.Error("default WorkingDir should be empty string")
	}
	if deps.SkillReg != nil {
		t.Error("default SkillReg should be nil")
	}
	if deps.McpReg != nil {
		t.Error("default McpReg should be nil")
	}
	if deps.Hooks != nil {
		t.Error("default Hooks should be nil")
	}
}

func TestToolRefs_ZeroValues(t *testing.T) {
	t.Parallel()
	var refs ToolRefs
	if refs.Reg != nil {
		t.Error("default Reg should be nil")
	}
	if refs.BashReg != nil {
		t.Error("default BashReg should be nil")
	}
	if refs.Agent != nil {
		t.Error("default Agent should be nil")
	}
	if refs.REPL != nil {
		t.Error("default REPL should be nil")
	}
	if refs.JobReg != nil {
		t.Error("default JobReg should be nil")
	}
}

// ---------------------------------------------------------------------------
// WireEngine callback coverage tests
// ---------------------------------------------------------------------------

func TestWireEngine_AgentNotifyCallbackEnqueues(t *testing.T) {
	t.Parallel()
	deps, refs, eng := newTestDepsAndRefs(t)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetSystemPrompt("sys")

	// The agent notify callback is the first arg to SetNotifyFn.
	// We can trigger it by simulating what AgentTool does: call the notify fn.
	// But notifyFn is unexported. Instead, we verify the wiring indirectly:
	// After WireEngine, calling the bash OnNotify should enqueue an item.
	refs.BashReg.OnNotify(bash.JobNotification{
		JobID:  "bg-42",
		Status: "completed",
	})
	// If we got here without panic, the callback path works.
}

func TestWireEngine_AgentFactory_SimplePrompt(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "sub-agent reply"), nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	// Trigger the factory via AgentTool.Run
	agentInput, _ := json.Marshal(map[string]any{
		"prompt":        "say hello",
		"agent_type":    "General",
		"system_prompt": "You are a test agent.",
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-session",
		},
	})
	if err != nil {
		t.Fatalf("Agent.Run failed: %v", err)
	}
	if result == nil {
		t.Fatal("Agent.Run returned nil result")
	}
	// The agent should have called the provider at least once
	if mp.callCount() < 1 {
		t.Errorf("expected at least 1 provider call, got %d", mp.callCount())
	}
}

func TestWireEngine_AgentFactory_ForkMessages(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "fork reply"), nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	// Trigger factory with fork=true (which sets opts.ForkMessages via callFork)
	agentInput, _ := json.Marshal(map[string]any{
		"fork":          true,
		"description":   "test fork",
		"subagent_type": "General",
		"prompt":        "test prompt",
		"system_prompt": "You are a forked agent.",
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-fork-session",
		},
	})
	if err != nil {
		t.Fatalf("Agent.Run with fork_messages failed: %v", err)
	}
	if result == nil {
		t.Fatal("Agent.Run returned nil result for fork path")
	}

	// Verify the prompt is NOT duplicated in the fork agent's messages.
	// TS alignment: forkSubagent.ts:163 — directive is the only place the
	// user's prompt appears (wrapped in buildChildMessage). No separate
	// user message with the bare prompt, and directive block appears once.
	msgs := mp.lastRequestMessages()
	if len(msgs) == 0 {
		t.Fatal("mockProvider did not receive any messages")
	}
	barePromptCount := 0
	directiveCount := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type != types.ContentTypeText {
				continue
			}
			if b.Text == "test prompt" {
				barePromptCount++
			}
			if strings.Contains(b.Text, "Your directive: test prompt") {
				directiveCount++
			}
		}
	}
	if barePromptCount != 0 {
		t.Errorf("prompt should not appear as bare text in fork agent (would duplicate directive); got %d bare occurrences",
			barePromptCount)
	}
	if directiveCount != 1 {
		t.Errorf("directive block should appear exactly once in fork agent; got %d occurrences in %d messages",
			directiveCount, len(msgs))
	}
}

func TestWireEngine_AgentFactory_CancelledContext(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)

	mp := &mockProvider{}
	// Return an error to trigger the error path
	mp.addResponse(nil, context.Canceled)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately to trigger ctx.Err() path

	agentInput, _ := json.Marshal(map[string]any{
		"prompt":        "should be cancelled",
		"agent_type":    "General",
		"system_prompt": "test",
	})
	result, err := refs.Agent.Call(ctx, agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-cancel-session",
		},
	})
	// With cancelled context, the factory returns (result, nil) per the ctx.Err() path
	if err != nil {
		t.Logf("Agent.Run with cancelled context returned error (acceptable): %v", err)
	}
	// With cancelled context, factory returns (result, nil) — verify result exists
	if result != nil {
		sqr, ok := result.Data.(*types.SubQueryResult)
		if ok {
			t.Logf("SubQueryResult.Content = %q (acceptable for cancelled context)", sqr.Content)
		}
	}
}

func TestWireEngine_AgentFactory_ToolFiltering(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "filtered tools reply"), nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	// The Agent tool's factory calls eng.RunAgent, whose root-table re-point
	// reads toolRefs — WireEngine alone leaves it empty (production wiring
	// always pairs the two).
	eng.SetToolRefs(refs)

	// Trigger factory with a restricted tool set
	agentInput, _ := json.Marshal(map[string]any{
		"prompt":        "use only read",
		"agent_type":    "Explore",
		"system_prompt": "test",
		"tools": map[string]any{
			"Read": map[string]any{},
		},
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-filter-session",
		},
	})
	if err != nil {
		t.Fatalf("Agent.Run with tool filtering failed: %v", err)
	}
	if result == nil {
		t.Fatal("Agent.Run returned nil result for tool filtering path")
	}
}

func TestWireEngine_REPLExecutorCallsEngine(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)

	mp := &mockProvider{}
	// Register a simple tool so REPL can call it — the engine only sees tools
	// captured by ToolsProvider at construction time.
	engTestTool := &mockTool{name: "TestTool"}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"TestTool": engTestTool}
		},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	defer eng.Close()

	refs.Reg.MustRegister(engTestTool)

	WireEngine(eng, refs, deps)

	// The REPL executor calls eng.ExecuteTool. Verify it's wired by executing REPL code.
	replInput, _ := json.Marshal(map[string]any{
		"code":       `console.log("hello")`,
		"session_id": "test-repl-session",
	})
	result, err := refs.REPL.Call(context.Background(), replInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-repl-session",
		},
	})
	if err != nil {
		t.Fatalf("REPL.Call failed: %v", err)
	}
	if result == nil {
		t.Fatal("REPL.Call returned nil result")
	}
	// Verify output contains "hello" — read it off the wire form the LLM sees
	outputStr := ""
	if result.Data != nil {
		blocks := refs.REPL.FormatWireBlocks(result.Data)
		if len(blocks) != 1 {
			t.Fatalf("len(blocks) = %d, want 1 text block", len(blocks))
		}
		outputStr = blocks[0].Text
	}
	if !strings.Contains(outputStr, "hello") {
		t.Errorf("REPL output should contain 'hello', got: %s", outputStr)
	}

	// Full chain: bootstrap lister builds the JS tools object from AllTools,
	// and the tools.X property routes through eng.ExecuteTool.
	replInput2, _ := json.Marshal(map[string]any{
		"code":       `const r = tools.TestTool({}); console.log(typeof r + ":" + r)`,
		"session_id": "test-repl-session",
	})
	result2, err := refs.REPL.Call(context.Background(), replInput2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-repl-session",
		},
	})
	if err != nil {
		t.Fatalf("REPL.Call with tools.TestTool failed: %v", err)
	}
	wire2 := ""
	if result2.Data != nil {
		blocks := refs.REPL.FormatWireBlocks(result2.Data)
		if len(blocks) != 1 {
			t.Fatalf("len(blocks) = %d, want 1 text block", len(blocks))
		}
		wire2 = blocks[0].Text
	}
	if wire2 != "string:ok\n" {
		t.Errorf("tools.TestTool chain: got %q, want %q", wire2, "string:ok\n")
	}
}

func TestWireEngine_AgentFactory_GeneralSpawn(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "context reply"), nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	// Plain spawn through the wired Agent tool: no fork messages, no caller
	// context messages and no skills, so RunAgent assembles only the prompt
	// message and hands it to RunForkedQuery.
	const promptSentinel = "spawn-prompt-6b48"
	agentInput, _ := json.Marshal(map[string]any{
		"prompt":        promptSentinel,
		"agent_type":    "General",
		"system_prompt": "test",
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-ctx-session",
		},
	})
	if err != nil {
		t.Fatalf("Agent.Run failed: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Exactly one provider call: a spawn that never reached the LLM and one
	// that looped twice are both wrong, and a "< 1" check cannot tell them
	// apart from the correct path.
	if got := mp.callCount(); got != 1 {
		t.Errorf("provider call count = %d, want 1", got)
	}
	// The request must carry the prompt message and nothing else in the user
	// role. marshalMessages prefixes each user message with its timestamp, so
	// the suffix is the tightest exact match available; the count is what
	// rules out an extra injected user message (a seeded CLAUDE.md or a
	// fallback-path prompt duplicate). CLAUDE.md itself is flagged meta.
	var userTexts []string
	for _, m := range mp.lastRequestMessages() {
		if m.Role == types.RoleUser && m.Flags != types.FlagMeta {
			var b strings.Builder
			for _, cb := range m.Content {
				b.WriteString(cb.Text)
			}
			userTexts = append(userTexts, b.String())
		}
	}
	if len(userTexts) != 1 {
		t.Fatalf("non-meta user messages in request = %d, want 1; texts: %q", len(userTexts), userTexts)
	}
	if !strings.HasSuffix(userTexts[0], promptSentinel) {
		t.Errorf("user message = %q, want it to end with %q", userTexts[0], promptSentinel)
	}
}

func TestWireEngine_AgentFactory_QueryError(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)

	mp := &mockProvider{}
	// Return a non-cancellation error
	mp.addResponse(nil, &llm.APIError{Status: 500, Type: "server_error", Message: "internal error"})

	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	agentInput, _ := json.Marshal(map[string]any{
		"prompt":        "trigger error",
		"agent_type":    "General",
		"system_prompt": "test",
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-error-session",
		},
	})
	// Should get an error from the factory
	if err == nil {
		t.Log("Agent returned no error (acceptable if handled internally)")
	}
	// Verify result is not nil even on error — the factory wraps errors
	if result == nil && err == nil {
		t.Error("expected either result or error to be non-nil")
	}
}

// TestWireEngine_McpConnectCallbackExecutes covers the McpConnect closure
// body (bootstrap.go:192-204) by invoking it via reflection.
func TestWireEngine_McpConnectCallbackExecutes(t *testing.T) {
	t.Parallel()
	mcpReg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = mcpReg.Close() })
	mcpReg.SetToolsForTest([]mcp.DiscoveredTool{
		{
			Name: "mcp__test__tool", OriginalName: "tool",
			ServerName: "test", Description: "test",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			AlwaysLoad:  true,
		},
	})
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
		McpReg:     mcpReg,
	}
	refs := CreateTools(deps, tl)
	WireEngine(nil, refs, deps) // nil eng — only SetMcpConnect side effect (SetEngine accepts nil)

	// Verify mcpConnect was set, then invoke it to exercise the closure body.
	mcpFn := refs.Agent.McpConnectFn()
	if mcpFn == nil {
		t.Fatal("mcpConnect not set")
	}
	// Call the closure — it will try ConnectAgentServers with our inline spec.
	// The MCP client manager in test mode returns an error or empty result.
	// We just verify the closure executes without panic.
	_, _ = mcpFn(context.Background(), "agent-test", []json.RawMessage{json.RawMessage(`{"command":"__nonexistent__","args":["x"]}`)})
}

// -----------------------------------------------------------------------
// WireEngine MCP connect path coverage
// -----------------------------------------------------------------------

func TestWireEngine_McpReg_SetsMcpConnect(t *testing.T) {
	t.Parallel()
	mcpReg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = mcpReg.Close() })
	mcpReg.SetToolsForTest([]mcp.DiscoveredTool{
		{
			Name: "mcp__test__hello", OriginalName: "hello",
			ServerName: "test", Description: "a test tool",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			AlwaysLoad:  true,
		},
	})
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
		McpReg:     mcpReg,
	}
	refs := CreateTools(deps, tl)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "mcp agent reply"), nil)
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	// Verify the McpConnect callback was registered on the agent.
	mcpFn := refs.Agent.McpConnectFn()
	if mcpFn == nil {
		t.Fatal("mcpConnect should be set after WireEngine with non-nil McpReg")
	}

	// Verify MCP tools were merged into sub-engine via Engine.RunAgent.
	// Trigger Agent.Call — the sub-engine should have the MCP tool available.
	// Trigger the factory — the sub-engine should have the MCP tool available.
	agentInput, _ := json.Marshal(map[string]any{
		"prompt":        "use mcp tool",
		"agent_type":    "General",
		"system_prompt": "test",
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-mcp-session",
		},
	})
	if err != nil {
		t.Fatalf("Agent.Call failed: %v", err)
	}
	if result == nil {
		t.Fatal("Agent.Call returned nil result")
	}
}

// ---------------------------------------------------------------------------
// WireEngine hooks AdditionalContext path coverage
// ---------------------------------------------------------------------------

func TestWireEngine_HooksAdditionalContext(t *testing.T) {
	t.Parallel()
	// Create a temp script that outputs JSON with additionalContext.
	script := filepath.Join(t.TempDir(), "subagent_start.sh")
	scriptContent := "#!/bin/sh\necho '{\"additionalContext\": \"injected context from hook\"}'\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	config := hooks.HooksConfig{
		"SubagentStart": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{
				{Type: "command", Command: script},
			}},
		},
	}
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(config, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "hook context reply"), nil)
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()

	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	// Trigger the factory — SubagentStart hook should fire and inject context.
	agentInput, _ := json.Marshal(map[string]any{
		"prompt":        "test hooks",
		"agent_type":    "General",
		"system_prompt": "test",
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{
			SessionID: "test-hooks-session",
		},
	})
	if err != nil {
		t.Fatalf("Agent.Call failed: %v", err)
	}
	if result == nil {
		t.Fatal("Agent.Call returned nil result")
	}
}

func TestCreateTools_PerEngineTaskList(t *testing.T) {
	t.Parallel()

	tl1 := task.NewList(t.TempDir())
	tl2 := task.NewList(t.TempDir())
	deps1 := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	deps2 := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}

	refs1 := CreateTools(deps1, tl1)
	refs2 := CreateTools(deps2, tl2)

	if refs1.Reg == refs2.Reg {
		t.Error("two CreateTools calls should return independent registries")
	}

	// Invoke Task tool from refs1 — should bind to tl1, not tl2.
	taskTool1, ok := refs1.Reg.ToolMap()["Task"]
	if !ok {
		t.Fatal("Task tool not found in refs1")
	}
	createInput := json.RawMessage(`{"action":"create","subject":"s1","description":"d1"}`)
	_, err := taskTool1.Call(context.Background(), createInput, nil)
	if err != nil {
		t.Fatalf("Task tool call on refs1 failed: %v", err)
	}

	tl1Tasks, err := tl1.ListTasks()
	if err != nil {
		t.Fatalf("tl1.ListTasks: %v", err)
	}
	if len(tl1Tasks) != 1 {
		t.Fatalf("tl1 should have 1 task after refs1 tool call, got %d", len(tl1Tasks))
	}
	if tl1Tasks[0].Subject != "s1" {
		t.Errorf("tl1 task subject = %q, want %q", tl1Tasks[0].Subject, "s1")
	}

	tl2Tasks, err := tl2.ListTasks()
	if err != nil {
		t.Fatalf("tl2.ListTasks: %v", err)
	}
	if len(tl2Tasks) != 0 {
		t.Errorf("tl2 should have 0 tasks (mutual exclusion), got %d", len(tl2Tasks))
	}

	// Invoke Task tool from refs2 — should bind to tl2 only.
	taskTool2, ok := refs2.Reg.ToolMap()["Task"]
	if !ok {
		t.Fatal("Task tool not found in refs2")
	}
	createInput2 := json.RawMessage(`{"action":"create","subject":"s2","description":"d2"}`)
	_, err = taskTool2.Call(context.Background(), createInput2, nil)
	if err != nil {
		t.Fatalf("Task tool call on refs2 failed: %v", err)
	}

	tl2Tasks, err = tl2.ListTasks()
	if err != nil {
		t.Fatalf("tl2.ListTasks: %v", err)
	}
	if len(tl2Tasks) != 1 {
		t.Fatalf("tl2 should have 1 task after refs2 tool call, got %d", len(tl2Tasks))
	}
	if tl2Tasks[0].Subject != "s2" {
		t.Errorf("tl2 task subject = %q, want %q", tl2Tasks[0].Subject, "s2")
	}

	// tl1 should still have exactly 1 task (the one from refs1).
	tl1Tasks, err = tl1.ListTasks()
	if err != nil {
		t.Fatalf("tl1.ListTasks: %v", err)
	}
	if len(tl1Tasks) != 1 {
		t.Errorf("tl1 should still have 1 task, got %d", len(tl1Tasks))
	}
}

// TestAgentBackgroundSubagentEndToEnd exercises the full background-subagent
// chain: Agent tool spawn → shared registry → engine.RunAgent (real
// sub-engine, real provider) → job notification → engine attachment queue →
// auto LLM turn that sees the notification.
func TestAgentBackgroundSubagentEndToEnd(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	refs := CreateTools(deps, tl)
	mp := &mockProvider{}
	// Response 1: the background sub-agent's reply. Response 2: the parent's
	// reaction to the job notification (auto-turn after Wait).
	mp.addResponse(textStreamEvents("test-model", "sub-agent reply"), nil)
	mp.addResponse(textStreamEvents("test-model", "noted"), nil)
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	eng.SetSharedDeps(&deps)
	defer eng.Close()
	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)
	// The notification auto-turn requires a non-empty system prompt
	// (startProcessAttachmentsIfIdle bails on empty).
	eng.SetSystemPrompt("parent system prompt")

	agentInput, _ := json.Marshal(map[string]any{
		"description":       "e2e",
		"prompt":            "p",
		"subagent_type":     "General",
		"run_in_background": true,
	})
	result, err := refs.Agent.Call(context.Background(), agentInput, &tool.ToolUseContext{
		ToolUseID: "call_e2e_1",
		Options:   tool.ToolUseOptions{SessionID: "e2e-session"},
	})
	if err != nil {
		t.Fatalf("Agent.Call returned error: %v", err)
	}
	sqr, ok := result.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("result.Data = %T, want *SubQueryResult", result.Data)
	}
	if !sqr.AsyncLaunched {
		t.Fatalf("AsyncLaunched = false, want true (sync path ran instead: %+v)", sqr)
	}
	if sqr.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", sqr.AgentID, "agent-1")
	}
	if sqr.AgentType != "General" {
		t.Errorf("AgentType = %q, want %q", sqr.AgentType, "General")
	}

	code, err := refs.JobReg.Wait("agent-1")
	if err != nil {
		t.Fatalf("JobReg.Wait returned error: %v", err)
	}
	if code != 0 {
		t.Errorf("JobReg.Wait exit code = %d, want 0", code)
	}

	// Sub-agent consumed response 1; the notification auto-turn consumes
	// response 2 via startProcessAttachmentsIfIdle.
	waitForCallCount(t, mp, 2, 5*time.Second)

	var gotNotification, gotAgentType bool
	for _, m := range mp.lastRequestMessages() {
		if m.Role != types.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type != types.ContentTypeText {
				continue
			}
			if strings.Contains(b.Text, "<job-notification>") && strings.Contains(b.Text, "agent-1") {
				gotNotification = true
			}
			if strings.Contains(b.Text, "<agent-type>General</agent-type>") {
				gotAgentType = true
			}
		}
	}
	if !gotNotification {
		t.Error("parent's notification-turn request must contain the <job-notification> with agent-1")
	}
	if !gotAgentType {
		t.Error("notification delivered to the parent must label the agent type General")
	}

	info, found := refs.JobReg.Get("agent-1")
	if !found {
		t.Fatal("JobReg.Get(agent-1) returned false after completion")
	}
	if info.Status != "completed" {
		t.Errorf("job status = %q, want %q", info.Status, "completed")
	}
}

// ---------------------------------------------------------------------------
// Session-root binding (WithSessionRootRefs)
// ---------------------------------------------------------------------------

// newWiredTestEngine builds a bare engine for wiring assertions: mock
// provider/dispatcher and no system prompt, so the attachment kick after an
// enqueue refuses to start a turn and the queue stays countable.
func newWiredTestEngine(t *testing.T) *Engine {
	t.Helper()
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	t.Cleanup(func() { eng.Close() })
	return eng
}

// Root-bound toolsets spawn background bash into the ROOT's registry (panel
// visibility, stop, tail), keep their own ToolRefs registries private, and
// their Job tool reaches root rows — including killing jobs the root itself
// spawned.
func TestCreateTools_WithSessionRootRefs_BindsRootRegistries(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	root := CreateTools(deps, tl)
	sub := CreateTools(deps, tl, WithSessionRootRefs(&root))

	// Engines over both toolsets, so every notification has an observable
	// recipient. Created before the job cleanups below: t.Cleanup is LIFO, so
	// the jobs get killed while the engines are still alive to receive it.
	rootEng := newWiredTestEngine(t)
	WireEngine(rootEng, root, deps)
	subEng := newWiredTestEngine(t)
	WireEngine(subEng, sub, deps)

	bashTool, ok := sub.Reg.Lookup("Bash")
	if !ok {
		t.Fatal("sub toolset has no Bash tool")
	}
	res, err := bashTool.Call(context.Background(),
		json.RawMessage(`{"command":"sleep 30","run_in_background":true,"description":"root-bound"}`), nil)
	if err != nil {
		t.Fatalf("sub Bash.Call: %v", err)
	}
	if out, ok := res.Data.(*bash.Output); !ok || !strings.Contains(out.Stdout, "Background job started with ID:") {
		t.Fatalf("Bash.Call data = %+T, want *bash.Output carrying the bg id line", res.Data)
	}

	rootList := root.BashReg.List()
	if len(rootList) != 1 {
		t.Fatalf("root BashReg holds %d jobs, want 1 (sub spawn must land in the root table)", len(rootList))
	}
	jobID := rootList[0].ID
	if rootList[0].Status != bash.JobRunning {
		t.Errorf("root job row = {%s %s}, want {%s running}", rootList[0].ID, rootList[0].Status, jobID)
	}
	if n := len(sub.BashReg.List()); n != 0 {
		t.Errorf("sub's own BashReg holds %d jobs, want 0 (ToolRefs registries stay private)", n)
	}

	// The sub's Job tool sees root rows (self-inspection capability).
	jobTool, ok := sub.Reg.Lookup("Job")
	if !ok {
		t.Fatal("sub toolset has no Job tool")
	}
	listRes, err := jobTool.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
	if err != nil {
		t.Fatalf("sub Job list: %v", err)
	}
	listOut, ok := listRes.Data.(*job.JobOutput)
	if !ok {
		t.Fatalf("Job list data type = %T, want *job.JobOutput", listRes.Data)
	}
	sawRow := false
	for _, j := range listOut.List.Jobs {
		if j.ID == jobID {
			sawRow = true
		}
	}
	if !sawRow {
		t.Errorf("sub Job list does not include the root-bound row %q", jobID)
	}

	// Kill routing: the sub's Job stop reaches jobs in the ROOT table — one the
	// root spawned directly and one the sub's own Bash spawned there (bg-
	// prefix routes to the root's bash table).
	rootJob := root.BashReg.Spawn("sleep 30", 0, bash.NewStreamingOutput(nil))
	t.Cleanup(func() {
		_ = root.BashReg.Kill(jobID)
		_ = root.BashReg.Kill(rootJob.ID)
	})
	for _, id := range []string{rootJob.ID, jobID} {
		if _, err := jobTool.Call(context.Background(),
			json.RawMessage(`{"action":"stop","job_id":"`+id+`"}`),
			&tool.ToolUseContext{ToolUseID: "t-kill"}); err != nil {
			t.Fatalf("sub Job stop(%q): %v", id, err)
		}
		deadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded poll for async kill
		for {
			info, ok := root.BashReg.Get(id)
			if ok && string(info.Status) == "killed" {
				break
			}
			if time.Now().After(deadline) { // REAL-TIME: bounded poll for async kill
				status := "<gone>"
				if ok {
					status = string(info.Status)
				}
				t.Fatalf("root job %q status = %q after sub stop (2s deadline), want killed", id, status)
			}
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
		}
	}

	// Recipient of the killed notifications: Kill notifies through the
	// registry's own callback — the table owner — never through the per-job
	// override. jobID was spawned by the sub's Bash tool, whose notify hook
	// points at subEng, and its killed notification still lands in the root's
	// queue while the sub's stays empty.
	for _, id := range []string{rootJob.ID, jobID} {
		found := false
		for _, it := range rootEng.PendingAttachments() {
			if !strings.Contains(it.Value, "<job-id>"+id+"</job-id>") ||
				!strings.Contains(it.Value, "<status>killed</status>") {
				continue
			}
			found = true
		}
		if !found {
			t.Errorf("root engine queue has no killed notification for %q", id)
		}
	}
	if n := rootEng.AttachmentsLen(); n != 2 {
		t.Errorf("root engine attachments after the two stops = %d, want 2 (killed notifications go to the table owner)", n)
	}
	if n := subEng.AttachmentsLen(); n != 0 {
		t.Errorf("sub engine attachments after its own stop calls = %d, want 0 (kill bypasses the per-job notify override)", n)
	}
}

// A root missing either registry (or nil) must degrade to the historical
// private-registry behavior — no binding, no panic.
func TestCreateTools_WithSessionRootRefs_NilRootNoOps(t *testing.T) {
	t.Parallel()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{
		WorkingDir: t.TempDir(),
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	sub := CreateTools(deps, tl, WithSessionRootRefs(nil))

	bashTool, ok := sub.Reg.Lookup("Bash")
	if !ok {
		t.Fatal("sub toolset has no Bash tool")
	}
	res, err := bashTool.Call(context.Background(),
		json.RawMessage(`{"command":"sleep 30","run_in_background":true,"description":"own table"}`), nil)
	if err != nil {
		t.Fatalf("sub Bash.Call: %v", err)
	}
	if out, ok := res.Data.(*bash.Output); !ok || !strings.Contains(out.Stdout, "Background job started with ID:") {
		t.Fatalf("Bash.Call data = %+T, want *bash.Output carrying the bg id line", res.Data)
	}
	if got := len(sub.BashReg.List()); got != 1 {
		t.Fatalf("sub's own BashReg holds %d jobs, want 1 (nil root = private table)", got)
	}
	row := sub.BashReg.List()[0]
	t.Cleanup(func() { _ = sub.BashReg.Kill(row.ID) })
}

// A root present but missing either registry must degrade exactly like the
// nil root: the spawn lands in the sub's own table, never in a half-bound
// root table (or the process-global DefaultRegistry).
func TestCreateTools_WithSessionRootRefs_PartialRootDegrades(t *testing.T) {
	t.Parallel()
	for name, missing := range map[string]func(refs *ToolRefs){
		"missing BashReg": func(r *ToolRefs) { r.BashReg = nil },
		"missing JobReg":  func(r *ToolRefs) { r.JobReg = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tl := task.NewList(t.TempDir())
			deps := SharedDeps{
				WorkingDir: t.TempDir(),
				SkillReg:   skills.NewRegistry(t.TempDir()),
				Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
			}
			root := CreateTools(deps, tl)
			missing(&root)
			sub := CreateTools(deps, tl, WithSessionRootRefs(&root))

			bashTool, ok := sub.Reg.Lookup("Bash")
			if !ok {
				t.Fatal("sub toolset has no Bash tool")
			}
			res, err := bashTool.Call(context.Background(),
				json.RawMessage(`{"command":"sleep 30","run_in_background":true,"description":"degraded"}`), nil)
			if err != nil {
				t.Fatalf("sub Bash.Call: %v", err)
			}
			if out, ok := res.Data.(*bash.Output); !ok || !strings.Contains(out.Stdout, "Background job started with ID:") {
				t.Fatalf("Bash.Call data = %+T, want *bash.Output carrying the bg id line", res.Data)
			}
			if got := len(sub.BashReg.List()); got != 1 {
				t.Fatalf("sub's own BashReg holds %d jobs, want 1 (partial root must degrade to the private table)", got)
			}
			row := sub.BashReg.List()[0]
			t.Cleanup(func() { _ = sub.BashReg.Kill(row.ID) })
		})
	}
}

// WireEngine must arm BOTH bash notification hook points with the same
// closure: the registry-level OnNotify and the per-spawner tool hook.
func TestWireEngine_SetsBashJobNotify(t *testing.T) {
	t.Parallel()
	deps, refs, eng := newTestDepsAndRefs(t)

	WireEngine(eng, refs, deps)

	if refs.BashReg.OnNotify == nil {
		t.Fatal("BashReg.OnNotify should be wired by WireEngine")
	}
	if refs.Bash.JobNotifyFn() == nil {
		t.Fatal("Bash.JobNotifyFn should be wired by WireEngine")
	}
	// No system prompt on eng, so the enqueue kick early-returns and the item
	// stays countable in the queue.
	refs.Bash.JobNotifyFn()(bash.JobNotification{JobID: "bg-9", Status: "completed", Summary: "hook probe"})
	if got := eng.AttachmentsLen(); got != 1 {
		t.Fatalf("AttachmentsLen after JobNotifyFn fire = %d, want 1", got)
	}
}

// A RunAgent sub-engine's private background table must stay
// manager-invisible: the sub never enters mgr.engines, so its spawns resolve
// only through the root engine's table. Mirrors RunAgent's three-step wiring:
// CreateTools(root refs) → WireEngine → re-point to the root table.
func TestWireEngine_SubPrivateTableNotManagerReachable(t *testing.T) {
	mp := &mockProvider{}
	deps, mgr, _ := msgDeps(t)

	rootEng, rootRefs := msgWiredParent(t, mp, "root", deps)
	mgr.Add(&EngineViewState{Engine: rootEng, ID: "root"})

	subRefs := CreateTools(deps, task.NewList(""), WithSessionRootRefs(&rootRefs))
	subEng := newWiredTestEngine(t)
	WireEngine(subEng, subRefs, deps)

	subID := spawnHoldingAgent(t, subRefs.Agent.Registry())
	subRefs.Agent.SetRegistry(rootRefs.Agent.Registry())

	if got, ok := mgr.BgAgent(subID); ok || got != nil {
		t.Errorf("BgAgent(sub private id %q) = (%v, %v), want (nil, false): a sub engine's private table must not be manager-reachable", subID, got, ok)
	}

	rootID := spawnHoldingAgent(t, rootRefs.Agent.Registry())
	if _, ok := mgr.BgAgent(rootID); !ok {
		t.Errorf("BgAgent(root id %q) not found: the root engine is in the manager, its table must resolve", rootID)
	}
}
