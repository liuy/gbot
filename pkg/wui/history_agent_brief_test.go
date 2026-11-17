package wui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// TestBuildHistoryChatMsg_AgentToolCarriesBrief verifies the history replay of
// an Agent tool_use block extracts the orchestrator prompt into Brief, so a
// page reload of an old session renders the same brief box as the live path.
func TestBuildHistoryChatMsg_AgentToolCarriesBrief(t *testing.T) {
	c := newTestConnector(t)
	m := types.Message{
		ID:        "m-brief",
		Role:      types.RoleAssistant,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "toolu_1", Name: "Agent", Input: json.RawMessage(`{"description":"Plan it","prompt":"全文任务书","subagent_type":"Planner"}`)},
			{Type: types.ContentTypeToolResult, ToolUseID: "toolu_1", Content: json.RawMessage(`[{"type":"text","text":"done"}]`)},
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if len(hm.Tools) != 1 {
		t.Fatalf("Tools = %d, want 1", len(hm.Tools))
	}
	if hm.Tools[0].Brief != "全文任务书" {
		t.Fatalf("Brief = %q, want 全文任务书", hm.Tools[0].Brief)
	}
	raw, err := json.Marshal(hm)
	if err != nil {
		t.Fatalf("marshal history msg: %v", err)
	}
	if !strings.Contains(string(raw), `"brief":"全文任务书"`) {
		t.Fatalf("expected brief key in history JSON, got: %s", raw)
	}

	// Non-Agent tool keeps Brief empty; JSON carries no brief key.
	m2 := types.Message{
		ID:        "m-bash-brief",
		Role:      types.RoleAssistant,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "toolu_2", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
			{Type: types.ContentTypeToolResult, ToolUseID: "toolu_2", Content: json.RawMessage(`[{"type":"text","text":"file"}]`)},
		},
	}
	hm2 := c.buildHistoryChatMsg(m2, nil, nil, nil)
	if hm2.Tools[0].Brief != "" {
		t.Fatalf("Bash Brief = %q, want empty", hm2.Tools[0].Brief)
	}
	raw2, err := json.Marshal(hm2)
	if err != nil {
		t.Fatalf("marshal bash msg: %v", err)
	}
	if strings.Contains(string(raw2), `"brief":`) {
		t.Fatalf("expected no brief key in Bash history JSON (omitempty), got: %s", raw2)
	}

	// Old-session semantics: a pre-brief Agent input without a prompt field
	// must degrade to an empty brief, not a crash.
	m3 := types.Message{
		ID:        "m-legacy",
		Role:      types.RoleAssistant,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "toolu_3", Name: "Agent", Input: json.RawMessage(`{"description":"legacy","subagent_type":"Planner"}`)},
			{Type: types.ContentTypeToolResult, ToolUseID: "toolu_3", Content: json.RawMessage(`[{"type":"text","text":"done"}]`)},
		},
	}
	hm3 := c.buildHistoryChatMsg(m3, nil, nil, nil)
	if hm3.Tools[0].Brief != "" {
		t.Fatalf("legacy promptless Agent Brief = %q, want empty", hm3.Tools[0].Brief)
	}

	// Malformed input yields empty brief.
	m4 := types.Message{
		ID:        "m-bad-brief",
		Role:      types.RoleAssistant,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "toolu_4", Name: "Agent", Input: json.RawMessage(`not json`)},
			{Type: types.ContentTypeToolResult, ToolUseID: "toolu_4", Content: json.RawMessage(`[{"type":"text","text":"done"}]`)},
		},
	}
	hm4 := c.buildHistoryChatMsg(m4, nil, nil, nil)
	if hm4.Tools[0].Brief != "" {
		t.Fatalf("bad-JSON Brief = %q, want empty", hm4.Tools[0].Brief)
	}

	// Over-cap prompt truncates: 16384 runes + "..." suffix.
	long := strings.Repeat("字", 17000)
	m5 := types.Message{
		ID:        "m-long",
		Role:      types.RoleAssistant,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "toolu_5", Name: "Agent", Input: json.RawMessage(`{"prompt":"` + long + `"}`)},
			{Type: types.ContentTypeToolResult, ToolUseID: "toolu_5", Content: json.RawMessage(`[{"type":"text","text":"done"}]`)},
		},
	}
	hm5 := c.buildHistoryChatMsg(m5, nil, nil, nil)
	if got := []rune(hm5.Tools[0].Brief); len(got) != 16387 {
		t.Fatalf("history Brief rune count = %d, want 16387", len(got))
	}
	if !strings.HasSuffix(hm5.Tools[0].Brief, "...") {
		t.Fatalf("history Brief must end with ..., got %q", hm5.Tools[0].Brief[len(hm5.Tools[0].Brief)-6:])
	}
	if hm5.Tools[0].Brief == long {
		t.Fatal("history Brief must be truncated, not verbatim")
	}
}
