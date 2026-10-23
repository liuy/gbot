package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// TestRunAgent_HookInputCwd_FollowsOverride pins HookInput.Cwd on the four
// events a cwd-overridden sub-agent touches. TS builds every event from one
// shared createBaseHookInput (hooks.ts:323) that reads getCwd() per dispatch, so
// a hook always sees the live cwd of the engine scope that triggered it. gbot
// builds each event as its own literal, which is why all of them came up empty.
//
// The hook system is attached twice on purpose: Params.Hooks feeds the engine's
// own dispatches (SubagentStop plus the executor's Pre/PostToolUse) while
// RunAgent's SubagentStart reads SharedDeps.Hooks. One instance keeps the
// recorder shared.
func TestRunAgent_HookInputCwd_FollowsOverride(t *testing.T) {
	t.Parallel()

	parentDir := t.TempDir()
	cwd := t.TempDir()

	rec := &integrationHookRecorder{}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"SubagentStart": []hooks.HookMatcher{{Matcher: "", Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "h-substart"},
		}}},
		"SubagentStop": []hooks.HookMatcher{{Matcher: "", Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "h-substop"},
		}}},
		"PreToolUse": []hooks.HookMatcher{{Matcher: "Bash", Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "h-pre"},
		}}},
		"PostToolUse": []hooks.HookMatcher{{Matcher: "Bash", Hooks: []hooks.HookConfig{
			{Type: hooks.HookTypeCommand, Command: "h-post"},
		}}},
	}, rec)

	mp := &testProvider{}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_pwd", Name: "Bash"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"pwd"}`}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
			{Type: "message_stop"},
		}},
		{events: subTextEvents("test", "done")},
	}

	deps := SharedDeps{
		WorkingDir: parentDir,
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hookSystem,
	}
	bt := bash.New(nil)
	ec := newEventCollector()
	eng := newRunAgentEngine(&Params{
		Provider:   mp,
		Model:      "test",
		WorkingDir: parentDir,
		Dispatcher: ec,
		Hooks:      hookSystem,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "report where you are",
		AgentType:       "General",
		Cwd:             cwd,
		ParentToolUseID: "call_hook_cwd",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	// Filter by event name rather than sweeping the recorder: calls outside these
	// four groups (SessionEnd from the deferred Close, for one) legitimately carry
	// the parent's live dir, not the override.
	groups := map[string]int{"SubagentStart": 0, "PreToolUse": 0, "PostToolUse": 0, "SubagentStop": 0}
	for _, c := range rec.Calls() {
		if _, ok := groups[c.event]; !ok {
			continue
		}
		groups[c.event]++
		if c.input.Cwd != cwd {
			t.Errorf("%s hook input Cwd = %q, want the override %q verbatim", c.event, c.input.Cwd, cwd)
		}
	}
	for event, n := range groups {
		if n == 0 {
			t.Fatalf("%s never dispatched; the assertion above would pass vacuously. events: %v", event, rec.Events())
		}
	}
}

// TestExecutor_HookCwd_FallbacksAndPermission drives a standalone
// StreamingToolExecutor through an ask-rule permission check so all three
// executor-side hook events dispatch, and pins what Cwd each one carries.
//
// Two things only this test can pin:
//   - The PermissionRequest construction site. The engine path needs an ask rule
//     to reach it, and the one existing test that touches PermissionRequest
//     (js_hook_perengine_test.go) asserts the opposite — that the inner js hook
//     is skipped by the hook-origin marker — so nothing ever dispatches there.
//   - hookCwd()'s tctx and empty-string fallbacks. The engine's executor always
//     gets a working-dir supplier and a non-nil base tctx, so those branches are
//     unreachable through Engine.Query; only a hand-built executor reaches them.
//     Case 2 and case 3 are each other's control: drop the tctx branch and case 2
//     goes red, drop the empty return and case 3 does.
//
// The emit callback answers the ask as a backstop only: the PermissionRequest
// allow decision skips askUser entirely (TS runHooks semantics), so the
// callback is not expected to fire — if a regression reopens the dialog, it
// gets answered instead of hanging the case until the go-test timeout.
func TestExecutor_HookCwd_FallbacksAndPermission(t *testing.T) {
	t.Parallel()

	supDir := "/tmp/hookcwd-supplier-3f"

	cases := []struct {
		name     string
		tctx     *tool.ToolUseContext
		supplier bool
		want     string
	}{
		{"supplier wins", &tool.ToolUseContext{WorkingDir: "/tmp/hookcwd-tctx-1a"}, true, supDir},
		{"tctx fallback", &tool.ToolUseContext{WorkingDir: "/tmp/hookcwd-tctx-2b"}, false, "/tmp/hookcwd-tctx-2b"},
		{"both empty", nil, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &integrationHookRecorder{
				// One result only, consumed by the first dispatch (PermissionRequest):
				// the recorder's index is call-order, and the allow decision is read
				// solely by that loop. Pre/PostToolUse fall back to a plain success.
				results: []hooks.HookResult{{
					Outcome:  hooks.HookOutcomeSuccess,
					HookName: "h-perm",
					Output:   &hooks.HookOutput{Decision: "allow"},
				}},
			}
			h := hooks.NewHooks(hooks.HooksConfig{
				"PermissionRequest": []hooks.HookMatcher{{Matcher: "", Hooks: []hooks.HookConfig{
					{Type: hooks.HookTypeCommand, Command: "h-perm"},
				}}},
				"PreToolUse": []hooks.HookMatcher{{Matcher: "", Hooks: []hooks.HookConfig{
					{Type: hooks.HookTypeCommand, Command: "h-pre"},
				}}},
				"PostToolUse": []hooks.HookMatcher{{Matcher: "", Hooks: []hooks.HookConfig{
					{Type: hooks.HookTypeCommand, Command: "h-post"},
				}}},
			}, rec)

			emit := func(evt types.QueryEvent) {
				if evt.Type == types.EventAsk && evt.Ask != nil && evt.Ask.ResponseCh != nil {
					evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
				}
			}
			exec := NewStreamingToolExecutor(map[string]tool.Tool{"Echo": echoTool("ok")}, tc.tctx, emit, context.Background())
			if tc.supplier {
				exec.SetWorkingDirSupplier(func() string { return supDir })
			}
			exec.SetHooks(h, "sess")
			exec.SetPermissionChecker(permission.NewChecker([]permission.Rule{
				{
					Value:  permission.RuleValue{ToolName: "Echo"},
					Action: permission.ActionAsk,
					Source: "user",
				},
			}))

			exec.ExecuteAll([]types.ContentBlock{{
				Type:  types.ContentTypeToolUse,
				ID:    "tu_1",
				Name:  "Echo",
				Input: json.RawMessage(`{}`),
			}})

			groups := map[string]int{"PermissionRequest": 0, "PreToolUse": 0, "PostToolUse": 0}
			for _, c := range rec.Calls() {
				if _, ok := groups[c.event]; !ok {
					continue
				}
				groups[c.event]++
				if c.input.Cwd != tc.want {
					t.Errorf("%s hook input Cwd = %q, want %q", c.event, c.input.Cwd, tc.want)
				}
			}
			for event, n := range groups {
				if n == 0 {
					t.Fatalf("%s never dispatched; the Cwd assertion would pass vacuously. events: %v", event, rec.Events())
				}
			}
		})
	}
}
