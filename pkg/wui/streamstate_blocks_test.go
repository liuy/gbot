package wui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

func buildPendingBlocks(ss streamState) []byte {
	type payload struct {
		Type   string        `json:"type"`
		Blocks []streamBlock `json:"blocks"`
	}
	blocks := make([]streamBlock, len(ss.blocks))
	copy(blocks, ss.blocks)
	out, _ := json.Marshal(payload{Type: "streamState", Blocks: blocks})
	return out
}

func TestUpdateStreamState_TextAccumulation(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{Type: types.EventTextDelta, Text: "hello "})
	updateStreamState(&ss, types.QueryEvent{Type: types.EventTextDelta, Text: "world"})

	if len(ss.blocks) != 1 {
		t.Fatalf("expected 1 text block, got %d blocks", len(ss.blocks))
	}
	if ss.blocks[0].Kind != "text" {
		t.Fatalf("expected kind \"text\", got %q", ss.blocks[0].Kind)
	}
	if ss.blocks[0].Text != "hello world" {
		t.Fatalf("expected text \"hello world\", got %q", ss.blocks[0].Text)
	}
}

func TestUpdateStreamState_TextCreatesNewBlockAfterTool(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{Type: types.EventTextDelta, Text: "first"})
	updateStreamState(&ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Grep"}})
	updateStreamState(&ss, types.QueryEvent{Type: types.EventTextDelta, Text: "second"})

	if len(ss.blocks) != 3 {
		t.Fatalf("expected 3 blocks [text, tool, text], got %d", len(ss.blocks))
	}
	if ss.blocks[0].Kind != "text" || ss.blocks[0].Text != "first" {
		t.Fatalf("block 0: expected text \"first\", got kind=%q text=%q", ss.blocks[0].Kind, ss.blocks[0].Text)
	}
	if ss.blocks[1].Kind != "tool" {
		t.Fatalf("block 1: expected kind \"tool\", got %q", ss.blocks[1].Kind)
	}
	if ss.blocks[2].Kind != "text" || ss.blocks[2].Text != "second" {
		t.Fatalf("block 2: expected text \"second\", got kind=%q text=%q", ss.blocks[2].Kind, ss.blocks[2].Text)
	}
}

func TestUpdateStreamState_ToolLifecycle(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash", Summary: "ls -la"},
	})

	if len(ss.blocks) != 1 || ss.blocks[0].Kind != "tool" {
		t.Fatalf("expected 1 tool block, got %d blocks", len(ss.blocks))
	}
	if ss.blocks[0].State != "running" {
		t.Fatalf("expected state \"running\", got %q", ss.blocks[0].State)
	}
	if ss.blocks[0].Name != "Bash" {
		t.Fatalf("expected name \"Bash\", got %q", ss.blocks[0].Name)
	}
	if ss.blocks[0].Summary != "ls -la" {
		t.Fatalf("expected summary \"ls -la\", got %q", ss.blocks[0].Summary)
	}
	if ss.blocks[0].StartedAt == 0 {
		t.Fatal("expected non-zero StartedAt on tool block")
	}

	updateStreamState(&ss, types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "t1", DisplayOutput: "file1\nfile2"},
	})

	if ss.blocks[0].State != "done" {
		t.Fatalf("expected state \"done\", got %q", ss.blocks[0].State)
	}
	if ss.blocks[0].DisplayOutput != "file1\nfile2" {
		t.Fatalf("expected displayOutput, got %q", ss.blocks[0].DisplayOutput)
	}
}

func TestUpdateStreamState_ToolLifecycle_Error(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "t1", IsError: true, DisplayOutput: "command not found"},
	})

	if ss.blocks[0].State != "error" {
		t.Fatalf("expected state \"error\", got %q", ss.blocks[0].State)
	}
}

func TestUpdateStreamState_ToolParamDelta(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Grep"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type: types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{
			ID:       "t1",
			Name:     "Grep",
			Summary:  "pattern",
			IsSearch: true,
		},
	})

	if ss.blocks[0].Summary != "pattern" {
		t.Fatalf("expected summary \"pattern\", got %q", ss.blocks[0].Summary)
	}
	if !ss.blocks[0].IsSearch {
		t.Fatal("expected IsSearch=true after tool_param_delta")
	}
}

func TestUpdateStreamState_ThinkingLifecycle(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{Type: types.EventThinkingStart})
	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingDelta,
		Thinking: &types.ThinkingEvent{Text: "I should "},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingDelta,
		Thinking: &types.ThinkingEvent{Text: "search first"},
	})

	if len(ss.blocks) != 1 || ss.blocks[0].Kind != "thinking" {
		t.Fatalf("expected 1 thinking block, got %d blocks", len(ss.blocks))
	}
	if ss.blocks[0].Text != "I should search first" {
		t.Fatalf("expected accumulated text, got %q", ss.blocks[0].Text)
	}
	if !ss.blocks[0].Active {
		t.Fatal("expected Active=true during thinking")
	}
	if ss.blocks[0].StartedAt == 0 {
		t.Fatal("expected non-zero StartedAt on thinking block")
	}

	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingEnd,
		Thinking: &types.ThinkingEvent{Duration: 5 * time.Millisecond},
	})

	if ss.blocks[0].Active {
		t.Fatal("expected Active=false after thinking_end")
	}
	if ss.blocks[0].DurationNs != int64(5*time.Millisecond) {
		t.Fatalf("expected DurationNs=%d, got %d", int64(5*time.Millisecond), ss.blocks[0].DurationNs)
	}
}

func TestUpdateStreamState_SubAgentNesting(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "parent1", Name: "Agent"},
	})

	agent := &types.AgentMeta{ParentToolUseID: "parent1", AgentType: "Reviewer"}
	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingStart,
		Agent:    agent,
		Thinking: &types.ThinkingEvent{},
	})

	if len(ss.blocks) != 1 {
		t.Fatalf("expected 1 root block (Agent), got %d", len(ss.blocks))
	}
	parent := &ss.blocks[0]
	if len(parent.Children) != 1 {
		t.Fatalf("expected 1 child block, got %d", len(parent.Children))
	}
	if parent.Children[0].Kind != "thinking" {
		t.Fatalf("expected child kind \"thinking\", got %q", parent.Children[0].Kind)
	}
}

func TestUpdateStreamState_DeepNesting(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tool1", Name: "Agent"},
	})

	agent1 := &types.AgentMeta{ParentToolUseID: "tool1", AgentType: "Explorer", Depth: 1}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		Agent:   agent1,
		ToolUse: &types.ToolUseEvent{ID: "tool2", Name: "Agent"},
	})

	agent2 := &types.AgentMeta{ParentToolUseID: "tool2", AgentType: "Reader", Depth: 2}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		Agent:   agent2,
		ToolUse: &types.ToolUseEvent{ID: "tool3", Name: "Read"},
	})

	// tool3 should be nested: tool1.children -> tool2.children -> tool3
	tool1 := findBlock(ss.blocks, "tool1")
	if tool1 == nil {
		t.Fatal("tool1 not found")
	}
	tool2 := findBlock(tool1.Children, "tool2")
	if tool2 == nil {
		t.Fatal("tool2 not found in tool1's children")
	}
	tool3 := findBlock(tool2.Children, "tool3")
	if tool3 == nil {
		t.Fatal("tool3 not found in tool2's children")
	}
	if tool3.Name != "Read" {
		t.Fatalf("expected tool3 name \"Read\", got %q", tool3.Name)
	}
}

func TestUpdateStreamState_SubAgentTextAndThinking(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Agent"},
	})

	agent := &types.AgentMeta{ParentToolUseID: "tu1", AgentType: "Reviewer"}
	updateStreamState(&ss, types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "sub-agent text",
		Agent: agent,
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingStart,
		Agent:    agent,
		Thinking: &types.ThinkingEvent{},
	})

	if len(ss.blocks) != 1 {
		t.Fatalf("expected 1 root block, got %d", len(ss.blocks))
	}
	if len(ss.blocks[0].Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(ss.blocks[0].Children))
	}
	if ss.blocks[0].Children[0].Kind != "text" || ss.blocks[0].Children[0].Text != "sub-agent text" {
		t.Fatalf("child 0: expected text \"sub-agent text\", got kind=%q text=%q",
			ss.blocks[0].Children[0].Kind, ss.blocks[0].Children[0].Text)
	}
	if ss.blocks[0].Children[1].Kind != "thinking" {
		t.Fatalf("child 1: expected kind \"thinking\", got %q", ss.blocks[0].Children[1].Kind)
	}
}

func TestUpdateStreamState_UnknownParentDropped(t *testing.T) {
	var ss streamState
	agent := &types.AgentMeta{ParentToolUseID: "nonexistent", AgentType: "Ghost"}
	updateStreamState(&ss, types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "orphan",
		Agent: agent,
	})

	if len(ss.blocks) != 0 {
		t.Fatalf("expected 0 blocks (parent not found), got %d", len(ss.blocks))
	}
}

func TestUpdateStreamState_ToolParamDeltaNonExistent(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type: types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{
			ID:      "ghost",
			Name:    "Grep",
			Summary: "noop",
		},
	})

	if len(ss.blocks) != 0 {
		t.Fatalf("expected 0 blocks, got %d", len(ss.blocks))
	}
}

func TestUpdateStreamState_ThinkingEndWithoutStart(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingEnd,
		Thinking: &types.ThinkingEvent{Duration: 1 * time.Millisecond},
	})

	if len(ss.blocks) != 0 {
		t.Fatalf("expected 0 blocks (no thinking_start), got %d", len(ss.blocks))
	}
}

func TestUpdateStreamState_ToolOutputDelta(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		ToolResult: &types.ToolResultEvent{ToolUseID: "t1", DisplayOutput: "partial output"},
	})

	if ss.blocks[0].DisplayOutput != "partial output" {
		t.Fatalf("expected displayOutput \"partial output\", got %q", ss.blocks[0].DisplayOutput)
	}
}

func TestUpdateStreamState_IsWeb(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Web"},
	})

	if !ss.blocks[0].IsWeb {
		t.Fatal("expected IsWeb=true for tool named \"Web\"")
	}
}

func TestBuildPendingBlocks_Empty(t *testing.T) {
	out := buildPendingBlocks(streamState{})
	var msg struct {
		Type   string        `json:"type"`
		Blocks []streamBlock `json:"blocks"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "streamState" {
		t.Fatalf("expected type \"streamState\", got %q", msg.Type)
	}
	if len(msg.Blocks) != 0 {
		t.Fatalf("expected nil or empty blocks, got %v", msg.Blocks)
	}
}

func TestBuildPendingBlocks_ValidBlockJSON(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{Type: types.EventTextDelta, Text: "hello"})
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Agent"},
	})
	agent := &types.AgentMeta{ParentToolUseID: "t1", AgentType: "Reviewer"}
	updateStreamState(&ss, types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "nested text",
		Agent: agent,
	})

	out := buildPendingBlocks(ss)
	raw := string(out)

	if !strings.Contains(raw, `"type":"streamState"`) {
		t.Fatalf("missing type field: %s", raw)
	}
	if !strings.Contains(raw, `"kind":"text"`) {
		t.Fatalf("missing text block: %s", raw)
	}
	if !strings.Contains(raw, `"kind":"tool"`) {
		t.Fatalf("missing tool block: %s", raw)
	}
	if !strings.Contains(raw, `"children"`) {
		t.Fatalf("missing children field for nested blocks: %s", raw)
	}
	if !strings.Contains(raw, `"nested text"`) {
		t.Fatalf("missing nested text content: %s", raw)
	}

	var msg struct {
		Type   string        `json:"type"`
		Blocks []streamBlock `json:"blocks"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msg.Blocks) != 2 {
		t.Fatalf("expected 2 root blocks, got %d", len(msg.Blocks))
	}
	if msg.Blocks[1].Kind != "tool" {
		t.Fatalf("expected block[1] kind \"tool\", got %q", msg.Blocks[1].Kind)
	}
	if len(msg.Blocks[1].Children) != 1 {
		t.Fatalf("expected 1 child in tool block, got %d", len(msg.Blocks[1].Children))
	}
}

func TestFindBlock(t *testing.T) {
	blocks := []streamBlock{
		{Kind: "text", Text: "a"},
		{Kind: "tool", ID: "t1", Name: "Grep", Children: []streamBlock{
			{Kind: "tool", ID: "t2", Name: "Read"},
		}},
	}

	found := findBlock(blocks, "t1")
	if found == nil || found.Name != "Grep" {
		t.Fatal("findBlock failed for t1")
	}

	foundNested := findBlock(blocks, "t2")
	if foundNested == nil || foundNested.Name != "Read" {
		t.Fatal("findBlock failed for nested t2")
	}

	notFound := findBlock(blocks, "nonexistent")
	if notFound != nil {
		t.Fatal("expected nil for nonexistent ID")
	}
}

func TestTargetList_TopLevel(t *testing.T) {
	ss := &streamState{blocks: []streamBlock{{Kind: "text", Text: "a"}}}
	list := targetList(ss, hub.Event{Type: types.EventTextDelta})
	if list != &ss.blocks {
		t.Fatal("expected root blocks for top-level event")
	}
}

func TestTargetList_SubAgent(t *testing.T) {
	ss := &streamState{blocks: []streamBlock{
		{Kind: "tool", ID: "tu1", Name: "Agent"},
	}}
	agent := &types.AgentMeta{ParentToolUseID: "tu1", AgentType: "Reviewer"}
	list := targetList(ss, hub.Event{Type: types.EventTextDelta, Agent: agent})
	if list == nil {
		t.Fatal("expected non-nil list for sub-agent event with existing parent")
	}
	if len(*list) != 0 {
		t.Fatalf("expected empty children list, got %d", len(*list))
	}
}

func TestTargetList_SubAgentUnknownParent(t *testing.T) {
	ss := &streamState{}
	agent := &types.AgentMeta{ParentToolUseID: "ghost", AgentType: "Ghost"}
	list := targetList(ss, hub.Event{Type: types.EventTextDelta, Agent: agent})
	if list != nil {
		t.Fatal("expected nil for unknown parent")
	}
}

func TestUpdateStreamState_TaskFlagPlumbing(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Task", IsTask: true},
	})
	if !ss.blocks[0].IsTask {
		t.Fatal("expected IsTask=true on block after tool_start")
	}
	if raw := string(buildPendingBlocks(ss)); !strings.Contains(raw, `"isTask":true`) {
		t.Fatalf("expected \"isTask\":true in JSON, got: %s", raw)
	}

	// omitempty: a Grep block must NOT carry an isTask field.
	ss = streamState{}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t2", Name: "Grep", IsSearch: true},
	})
	if ss.blocks[0].IsTask {
		t.Fatal("expected IsTask=false for Grep block")
	}
	if raw := string(buildPendingBlocks(ss)); strings.Contains(raw, "isTask") {
		t.Fatalf("expected no isTask field in Grep JSON (omitempty), got: %s", raw)
	}

	// param_delta sets the flag on an existing block.
	ss = streamState{}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t3", Name: "Task"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: &types.PartialInputEvent{ID: "t3", Name: "Task", IsTask: true},
	})
	if !ss.blocks[0].IsTask {
		t.Fatal("expected IsTask=true after tool_param_delta")
	}

	// tool_end sets the flag on an existing block.
	ss = streamState{}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t4", Name: "Task"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "t4", IsTask: true},
	})
	if !ss.blocks[0].IsTask {
		t.Fatal("expected IsTask=true after tool_end")
	}
}

func TestUpdateStreamState_AgentBriefFromToolRun(t *testing.T) {
	// Streaming shape: tool_start carries empty input; the brief arrives on
	// tool_run (content_block_stop), before any sub-agent event.
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_a", Name: "Agent"},
	})
	if ss.blocks[0].Brief != "" {
		t.Fatalf("expected empty Brief after tool_start with empty input, got %q", ss.blocks[0].Brief)
	}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: "tu_a", Name: "Agent", Input: json.RawMessage(`{"description":"d","prompt":"全文任务书"}`)},
	})
	if ss.blocks[0].Brief != "全文任务书" {
		t.Fatalf("Brief = %q, want 全文任务书", ss.blocks[0].Brief)
	}
	if raw := string(buildPendingBlocks(ss)); !strings.Contains(raw, `"brief":"全文任务书"`) {
		t.Fatalf("expected \"brief\":\"全文任务书\" in snapshot JSON, got: %s", raw)
	}

	// Non-streaming shape: content_block_start already carries the full input.
	ss = streamState{}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_b", Name: "Agent", Input: json.RawMessage(`{"prompt":"即刻任务书"}`)},
	})
	if ss.blocks[0].Brief != "即刻任务书" {
		t.Fatalf("Brief after tool_start with full input = %q, want 即刻任务书", ss.blocks[0].Brief)
	}

	// Non-Agent tools never carry a brief, snapshot JSON has no brief key.
	ss = streamState{}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_c", Name: "Bash"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: "tu_c", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
	})
	if ss.blocks[0].Brief != "" {
		t.Fatalf("Bash Brief = %q, want empty", ss.blocks[0].Brief)
	}
	if raw := string(buildPendingBlocks(ss)); strings.Contains(raw, "brief") {
		t.Fatalf("expected no brief key in Bash snapshot JSON (omitempty), got: %s", raw)
	}

	// Undecodable input yields empty brief, no panic.
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: "tu_c", Name: "Bash", Input: json.RawMessage(`not json`)},
	})
	if ss.blocks[0].Brief != "" {
		t.Fatalf("bad-JSON Brief = %q, want empty", ss.blocks[0].Brief)
	}

	// Empty Agent input and promptless Agent input yield empty brief.
	ss = streamState{}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_d", Name: "Agent"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: "tu_d", Name: "Agent", Input: json.RawMessage(`{"description":"no prompt field"}`)},
	})
	if ss.blocks[0].Brief != "" {
		t.Fatalf("promptless Agent Brief = %q, want empty", ss.blocks[0].Brief)
	}

	// Over-cap prompt truncates to 16384 runes + "..." (16387 runes total).
	long := strings.Repeat("长", 20000)
	ss = streamState{}
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_e", Name: "Agent"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: "tu_e", Name: "Agent", Input: json.RawMessage(`{"prompt":"` + long + `"}`)},
	})
	if got := []rune(ss.blocks[0].Brief); len(got) != 16387 {
		t.Fatalf("truncated Brief rune count = %d, want 16387", len(got))
	}
	if !strings.HasSuffix(ss.blocks[0].Brief, "...") {
		t.Fatalf("truncated Brief must end with ..., got suffix %q", ss.blocks[0].Brief[len(ss.blocks[0].Brief)-6:])
	}
	if ss.blocks[0].Brief == long {
		t.Fatal("truncated Brief must differ from the full 20000-rune prompt")
	}
}

func TestUpdateStreamState_ToolRunBriefUnknownBlock(t *testing.T) {
	// tool_run for a block the snapshot never saw must not panic or append.
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: "ghost", Name: "Agent", Input: json.RawMessage(`{"prompt":"x"}`)},
	})
	if len(ss.blocks) != 0 {
		t.Fatalf("expected no blocks, got %d", len(ss.blocks))
	}
	updateStreamState(&ss, types.QueryEvent{
		Type: types.EventToolRun,
	})
	if len(ss.blocks) != 0 {
		t.Fatalf("nil-ToolUse tool_run must be a no-op, got %d blocks", len(ss.blocks))
	}
}

// TestAgentBriefFromInput_Boundaries pins the helper's guards directly: the
// stream-level tests only feed well-formed flows, so a mutant dropping the
// name check (a Bash input that happens to carry a prompt key would mint a
// brief) or the Unmarshal error guard (a type-error input that still
// populates the prompt prefix) survived here.
func TestAgentBriefFromInput_Boundaries(t *testing.T) {
	if got := agentBriefFromInput("Bash", json.RawMessage(`{"prompt":"x"}`)); got != "" {
		t.Fatalf("non-Agent input with a prompt key = %q, want empty", got)
	}
	if got := agentBriefFromInput("", json.RawMessage(`{"prompt":"x"}`)); got != "" {
		t.Fatalf("empty tool name = %q, want empty", got)
	}
	// Type errors leave earlier fields populated (prompt="x") while err != nil —
	// the guard must discard the partial parse, not trust it.
	if got := agentBriefFromInput("Agent", json.RawMessage(`{"prompt":"x","description":123}`)); got != "" {
		t.Fatalf("unmarshal-error input = %q, want empty", got)
	}
	if got := agentBriefFromInput("Agent", nil); got != "" {
		t.Fatalf("nil input = %q, want empty", got)
	}
	if got := agentBriefFromInput("Agent", json.RawMessage(`{"prompt":""}`)); got != "" {
		t.Fatalf("empty prompt = %q, want empty", got)
	}
}
