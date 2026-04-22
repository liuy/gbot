package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func TestLogEngineEvent_AllBranches(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		// Text lifecycle
		{"text_start", Event{Type: types.EventTextStart}},
		{"text_delta", Event{Type: types.EventTextDelta, Text: "hello"}},
		{"text_end", Event{Type: types.EventTextEnd}},

		// Tool lifecycle — with and without data
		{"tool_start_nil", Event{Type: types.EventToolStart}},
		{"tool_start_with_use", Event{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "1", Name: "Bash", Summary: "test"}}},
		{"tool_param_delta_nil", Event{Type: types.EventToolParamDelta}},
		{"tool_param_delta_short", Event{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{ID: "1", Delta: "short", Summary: "s"}}},
		{"tool_param_delta_long", Event{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{ID: "1", Delta: strings.Repeat("x", 100), Summary: "s"}}},
		{"tool_run_nil", Event{Type: types.EventToolRun}},
		{"tool_run_with_use", Event{Type: types.EventToolRun, ToolUse: &types.ToolUseEvent{ID: "1", Name: "Read"}}},
		{"tool_output_delta_nil", Event{Type: types.EventToolOutputDelta}},
		{"tool_output_delta_with_result", Event{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "1", DisplayOutput: "line1\nline2\nline3"}}},
		{"tool_end_nil", Event{Type: types.EventToolEnd}},
		{"tool_end_with_result", Event{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "1", DisplayOutput: "done", IsError: false, Timing: 100 * time.Millisecond}}},
		{"tool_end_error", Event{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "1", DisplayOutput: "err", IsError: true}}},

		// Usage
		{"usage_nil", Event{Type: types.EventUsage}},
		{"usage_with_data", Event{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 10, CacheCreationInputTokens: 20}}},

		// Turn lifecycle
		{"turn_start", Event{Type: types.EventTurnStart}},
		{"turn_end", Event{Type: types.EventTurnEnd}},

		// Query lifecycle
		{"query_end", Event{Type: types.EventQueryEnd}},
		{"query_start_nil", Event{Type: types.EventQueryStart}},
		{"query_start_with_msg", Event{Type: types.EventQueryStart, Message: &types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}}},

		// Thinking
		{"thinking_start", Event{Type: types.EventThinkingStart}},
		{"thinking_delta_nil", Event{Type: types.EventThinkingDelta}},
		{"thinking_delta_with_text", Event{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "hmm..."}}},
		{"thinking_end_nil", Event{Type: types.EventThinkingEnd}},
		{"thinking_end_with_duration", Event{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 2 * time.Second}}},

		// Error
		{"error", Event{Type: types.EventError, Error: errTest}},

		// Unknown / default
		{"unknown_type", Event{Type: "custom_event_type"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logEngineEvent(tc.event)
		})
	}
}
