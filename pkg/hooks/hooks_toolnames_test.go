package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Stop-class matcher gating on ToolNames (gbot extension; TS stopHooks.ts
// never runs matchers). Any single hit in ToolNames runs the matcher's
// hooks; a full miss skips them; an empty matcher still runs unconditionally;
// tool events keep matching on ToolName alone.

func TestStop_ToolNames_RegexAnyMatch_Runs(t *testing.T) {
	rec := &HookRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []HookConfig{{Type: HookTypeCommand, Command: "close-tabs"}},
		}},
	}, rec)

	result := h.Stop(context.Background(), &HookInput{
		ToolNames: []string{"Bash", "mcp__plugin_browser_playwright__browser_tabs"},
	})
	if result != nil {
		t.Errorf("non-blocking stop result = %+v, want nil", result)
	}
	if rec.CallCount() != 1 {
		t.Fatalf("calls = %d, want 1 (any-match hit should run the hook)", rec.CallCount())
	}
	calls := rec.Calls()
	if calls[0].command != "close-tabs" {
		t.Errorf("command = %q, want close-tabs", calls[0].command)
	}
	if calls[0].input.ToolNames[1] != "mcp__plugin_browser_playwright__browser_tabs" {
		t.Errorf("input.ToolNames[1] = %q, want the mcp tool name delivered to the hook", calls[0].input.ToolNames[1])
	}
}

func TestStop_ToolNames_AllMiss_Skips(t *testing.T) {
	rec := &HookRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []HookConfig{{Type: HookTypeCommand, Command: "close-tabs"}},
		}},
	}, rec)

	h.Stop(context.Background(), &HookInput{ToolNames: []string{"Bash", "Read", "Grep"}})
	if rec.CallCount() != 0 {
		t.Errorf("calls = %d, want 0 (no tool this turn matches the matcher)", rec.CallCount())
	}
}

func TestStop_ToolNames_PipeSetAnyMatch(t *testing.T) {
	rec := &HookRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{{
			Matcher: "Read|Write",
			Hooks:   []HookConfig{{Type: HookTypeCommand, Command: "fs-cleanup"}},
		}},
	}, rec)

	h.Stop(context.Background(), &HookInput{ToolNames: []string{"Grep", "Write", "Bash"}})
	if rec.CallCount() != 1 {
		t.Errorf("calls = %d, want 1 (Write is in the pipe-separated set)", rec.CallCount())
	}
}

func TestStop_ToolNames_EmptyMatcher_AlwaysRuns(t *testing.T) {
	rec := &HookRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{{
			Hooks: []HookConfig{{Type: HookTypeCommand, Command: "always"}},
		}},
	}, rec)

	h.Stop(context.Background(), &HookInput{ToolNames: []string{"Bash"}})
	if rec.CallCount() != 1 {
		t.Errorf("calls = %d, want 1 (empty matcher must keep running regardless of ToolNames)", rec.CallCount())
	}
}

func TestStop_EmptyToolNames_NonEmptyMatcher_Skips(t *testing.T) {
	// Text-only turn: no tools executed. A non-empty matcher that only
	// matches real tool names must not run against the empty collection.
	rec := &HookRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{{
			Matcher: "Bash",
			Hooks:   []HookConfig{{Type: HookTypeCommand, Command: "bash-epilogue"}},
		}},
	}, rec)

	h.Stop(context.Background(), &HookInput{})
	if rec.CallCount() != 0 {
		t.Errorf("calls = %d, want 0 (matcher \"Bash\" vs empty tool name never matches)", rec.CallCount())
	}
}

func TestSubagentStop_ToolNames_AnyMatch(t *testing.T) {
	rec := &HookRecorder{}
	h := NewHooks(HooksConfig{
		"SubagentStop": []HookMatcher{{
			Matcher: "mcp__.*",
			Hooks:   []HookConfig{{Type: HookTypeCommand, Command: "sub-cleanup"}},
		}},
	}, rec)

	h.SubagentStop(context.Background(), &HookInput{ToolNames: []string{"mcp__x__y"}})
	if rec.CallCount() != 1 {
		t.Errorf("calls = %d, want 1 (SubagentStop gates on the sub-engine's own collection)", rec.CallCount())
	}
}

func TestPreToolUse_ToolName_TakesPrecedenceOverToolNames(t *testing.T) {
	// Tool events are TS semantics: the matcher sees ToolName only. A
	// ToolNames entry must not leak into tool-event matching.
	rec := &HookRecorder{}
	h := NewHooks(HooksConfig{
		"PreToolUse": []HookMatcher{{
			Matcher: "Bash",
			Hooks:   []HookConfig{{Type: HookTypeCommand, Command: "guard"}},
		}},
	}, rec)

	decision, _ := h.PreToolUse(context.Background(), &HookInput{
		ToolName:  "Read",
		ToolNames: []string{"Bash"},
	})
	if decision != HookDecisionPassthrough {
		t.Errorf("decision = %v, want Passthrough (matcher matches ToolName=Read only)", decision)
	}
	if rec.CallCount() != 0 {
		t.Errorf("calls = %d, want 0 (ToolNames must not widen tool-event matching)", rec.CallCount())
	}
}

func TestHookInput_ToolNamesJSONOmitEmpty(t *testing.T) {
	b, err := json.Marshal(&HookInput{HookEventName: "Stop"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"tool_names"`) {
		t.Errorf("json = %s, want tool_names omitted when empty", b)
	}
	b, err = json.Marshal(&HookInput{HookEventName: "Stop", ToolNames: []string{"Bash"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// HookInput always serializes session_id/transcript_path/cwd (no
	// omitempty there), so assert the tool_names fragment in isolation.
	if !strings.Contains(string(b), `"tool_names":["Bash"]`) {
		t.Errorf("json = %s, want tool_names serialized in snake_case with the given order", b)
	}
}
