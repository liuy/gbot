package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// FilterIncompleteToolCalls tests
// ---------------------------------------------------------------------------

func TestFilterIncompleteToolCalls_AllComplete(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"content"`), false),
		}},
	}
	filtered := FilterIncompleteToolCalls(messages)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 messages (all complete), got %d", len(filtered))
	}
}

func TestFilterIncompleteToolCalls_RemovesIncomplete(t *testing.T) {
	t.Parallel()
	// Assistant has tool_use "id1" with result and "id2" WITHOUT result → removed
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("found it"),
			types.NewToolUseBlock("id1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"content"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id2", "Grep", json.RawMessage(`{}`)),
		}},
	}
	filtered := FilterIncompleteToolCalls(messages)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 messages (incomplete assistant removed), got %d", len(filtered))
	}
	// First assistant (id1 has result) should remain
	if filtered[1].Role != types.RoleAssistant {
		t.Error("second message should be the first assistant (with complete tool_use)")
	}
	// Second assistant (id2 no result) should be gone
	for _, msg := range filtered {
		if msg.Role == types.RoleAssistant {
			for _, blk := range msg.Content {
				if blk.Type == types.ContentTypeToolUse && blk.ID == "id2" {
					t.Error("incomplete assistant (id2) should have been removed")
				}
			}
		}
	}
}

func TestFilterIncompleteToolCalls_NoToolUse(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	}
	filtered := FilterIncompleteToolCalls(messages)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 messages (no tool_use blocks), got %d", len(filtered))
	}
}

func TestFilterIncompleteToolCalls_Empty(t *testing.T) {
	t.Parallel()
	filtered := FilterIncompleteToolCalls(nil)
	if len(filtered) != 0 {
		t.Errorf("expected 0 for nil, got %d", len(filtered))
	}
}

// ---------------------------------------------------------------------------
// BuildForkMessages tests
// ---------------------------------------------------------------------------

func TestBuildForkMessages_WithAssistantToolUse(t *testing.T) {
	t.Parallel()
	contextHistory := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search code")}},
	}
	triggerAssistant := &types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewTextBlock("I'll search for you"),
			types.NewToolUseBlock("tu_1", "Agent", json.RawMessage(`{"description":"search","prompt":"find"}`)),
		},
	}

	result := BuildForkMessages(triggerAssistant, contextHistory, "find the Query method")

	// Expected: [user, clonedAssistant, userMsg(placeholder + directive)]
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// First message: original context history
	if result[0].Role != types.RoleUser || result[0].Content[0].Text != "search code" {
		t.Errorf("first message should be context history user msg, got role=%s text=%q", result[0].Role, result[0].Content[0].Text)
	}

	// Second message: cloned assistant
	if result[1].Role != types.RoleAssistant {
		t.Errorf("second message should be assistant, got %s", result[1].Role)
	}
	if len(result[1].Content) != 2 {
		t.Fatalf("cloned assistant should have 2 content blocks, got %d", len(result[1].Content))
	}

	// Third message: user with placeholder tool_result + directive text
	if result[2].Role != types.RoleUser {
		t.Errorf("third message should be user, got %s", result[2].Role)
	}
	if len(result[2].Content) != 2 {
		t.Fatalf("user msg should have 2 blocks (tool_result + directive), got %d", len(result[2].Content))
	}
	// First block: placeholder tool_result
	if result[2].Content[0].Type != types.ContentTypeToolResult {
		t.Errorf("first user block should be tool_result, got %s", result[2].Content[0].Type)
	}
	if result[2].Content[0].ToolUseID != "tu_1" {
		t.Errorf("tool_result ToolUseID = %q, want %q", result[2].Content[0].ToolUseID, "tu_1")
	}
	// Second block: directive with fork-boilerplate
	directiveText := result[2].Content[1].Text
	if !strings.Contains(directiveText, "<fork-boilerplate>") {
		t.Error("directive should contain <fork-boilerplate> tag")
	}
	if !strings.Contains(directiveText, "find the Query method") {
		t.Error("directive should contain the user's prompt")
	}
}

func TestBuildForkMessages_MultipleToolUse(t *testing.T) {
	t.Parallel()
	triggerAssistant := &types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewToolUseBlock("tu_1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("tu_2", "Grep", json.RawMessage(`{}`)),
			types.NewToolUseBlock("tu_3", "Agent", json.RawMessage(`{}`)),
		},
	}

	result := BuildForkMessages(triggerAssistant, nil, "do stuff")

	// Expected: [assistant, user(3 placeholders + directive)]
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	// User message should have 3 tool_results + 1 directive = 4 blocks
	if len(result[1].Content) != 4 {
		t.Fatalf("user msg should have 4 blocks, got %d", len(result[1].Content))
	}
	// Verify all tool_result blocks
	ids := map[string]bool{}
	for i := 0; i < 3; i++ {
		blk := result[1].Content[i]
		if blk.Type != types.ContentTypeToolResult {
			t.Errorf("block %d: expected tool_result, got %s", i, blk.Type)
		}
		ids[blk.ToolUseID] = true
	}
	for _, want := range []string{"tu_1", "tu_2", "tu_3"} {
		if !ids[want] {
			t.Errorf("missing tool_result for %q", want)
		}
	}
}

func TestBuildForkMessages_PlaceholderContentFormat(t *testing.T) {
	t.Parallel()
	// TS forkSubagent.ts:142-150 sends tool_result content as structured content block array:
	//   [{type: "text", text: FORK_PLACEHOLDER_RESULT}]
	// NOT as a bare JSON string. This matters for prompt cache byte-identical prefixes.
	triggerAssistant := &types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewToolUseBlock("tu_1", "Agent", json.RawMessage(`{}`)),
		},
	}

	result := BuildForkMessages(triggerAssistant, nil, "test")

	// Find the tool_result block
	var toolResultBlock *types.ContentBlock
	for i := range result[1].Content {
		if result[1].Content[i].Type == types.ContentTypeToolResult {
			toolResultBlock = &result[1].Content[i]
			break
		}
	}
	if toolResultBlock == nil {
		t.Fatal("no tool_result block found")
	}

	// Content should be a JSON array of content blocks, not a bare string
	var parsed []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(toolResultBlock.Content, &parsed); err != nil {
		t.Fatalf("Content should be a JSON array of content blocks, got parse error: %v\nContent was: %s", err, string(toolResultBlock.Content))
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(parsed))
	}
	if parsed[0].Type != "text" {
		t.Errorf("content block type = %q, want %q", parsed[0].Type, "text")
	}
	if !strings.Contains(parsed[0].Text, "Fork started") {
		t.Errorf("content block text = %q, want to contain 'Fork started'", parsed[0].Text)
	}
}

func TestBuildForkMessages_NilAssistant(t *testing.T) {
	t.Parallel()
	result := BuildForkMessages(nil, nil, "just a prompt")

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role != types.RoleUser {
		t.Errorf("expected user message, got %s", result[0].Role)
	}
	if !strings.Contains(result[0].Content[0].Text, "<fork-boilerplate>") {
		t.Error("should contain fork-boilerplate tag")
	}
}

func TestBuildForkMessages_FiltersContextHistory(t *testing.T) {
	t.Parallel()
	// Context history with an incomplete tool_use — should be filtered
	contextHistory := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("incomplete_id", "Grep", json.RawMessage(`{}`)),
		}},
	}
	triggerAssistant := &types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewToolUseBlock("tu_1", "Agent", json.RawMessage(`{}`)),
		},
	}

	result := BuildForkMessages(triggerAssistant, contextHistory, "do it")

	// Context history should be filtered: incomplete assistant removed
	// Result: [user(from history), triggerAssistant, userMsg]
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Content[0].Text != "search" {
		t.Errorf("first message should be context history user, got %q", result[0].Content[0].Text)
	}
	// The incomplete assistant should NOT be in the result
	for _, msg := range result {
		if msg.Role == types.RoleAssistant {
			for _, blk := range msg.Content {
				if blk.Type == types.ContentTypeToolUse && blk.ID == "incomplete_id" {
					t.Error("incomplete tool_use from context history should have been filtered")
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// buildForkDirective tests
// ---------------------------------------------------------------------------

func TestBuildForkDirective_ContainsRequiredElements(t *testing.T) {
	t.Parallel()
	directive := buildForkDirective("search for bugs")

	if !strings.Contains(directive, "<fork-boilerplate>") {
		t.Error("should contain opening fork-boilerplate tag")
	}
	if !strings.Contains(directive, "</fork-boilerplate>") {
		t.Error("should contain closing fork-boilerplate tag")
	}
	if !strings.Contains(directive, "STOP. READ THIS FIRST.") {
		t.Error("should contain STOP directive")
	}
	if !strings.Contains(directive, "Your directive: search for bugs") {
		t.Error("should contain the user's prompt with directive prefix")
	}
	if !strings.Contains(directive, "Do NOT spawn sub-agents") {
		t.Error("should contain no sub-agents rule")
	}
}
