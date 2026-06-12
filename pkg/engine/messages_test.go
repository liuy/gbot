package engine

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestCreateUserMessage(t *testing.T) {
	t.Parallel()
	msg := CreateUserMessage("hello")
	if msg.Role != types.RoleUser {
		t.Errorf("expected RoleUser, got %s", msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "hello" {
		t.Errorf("expected 'hello', got %q", msg.Content[0].Text)
	}
	if msg.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestCreateAssistantMessage(t *testing.T) {
	t.Parallel()
	msg := CreateAssistantMessage("response text")
	if msg.Role != types.RoleAssistant {
		t.Errorf("expected RoleAssistant, got %s", msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeText {
		t.Errorf("expected ContentTypeText, got %s", msg.Content[0].Type)
	}
	if msg.Content[0].Text != "response text" {
		t.Errorf("expected 'response text', got %q", msg.Content[0].Text)
	}
}

func TestCreateToolResultMessage(t *testing.T) {
	t.Parallel()
	blocks := []types.ContentBlock{
		types.NewToolResultBlock("tu_1", json.RawMessage(`"ok"`), false),
		types.NewToolResultBlock("tu_2", json.RawMessage(`"also ok"`), false),
	}
	msg := CreateToolResultMessage(blocks)
	if msg.Role != types.RoleUser {
		t.Errorf("expected RoleUser, got %s", msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}
	// Verify the blocks were copied correctly — check ToolUseID and IsError.
	if msg.Content[0].ToolUseID != "tu_1" {
		t.Errorf("expected block 0 ToolUseID 'tu_1', got %q", msg.Content[0].ToolUseID)
	}
	if msg.Content[0].IsError {
		t.Error("expected block 0 IsError false")
	}
	if msg.Content[1].ToolUseID != "tu_2" {
		t.Errorf("expected block 1 ToolUseID 'tu_2', got %q", msg.Content[1].ToolUseID)
	}
	if msg.Content[1].IsError {
		t.Error("expected block 1 IsError false")
	}
}

func TestCreateToolErrorBlock(t *testing.T) {
	t.Parallel()
	block := CreateToolErrorBlock("tu_1", "something broke")
	if block.Type != types.ContentTypeToolResult {
		t.Errorf("expected ContentTypeToolResult, got %s", block.Type)
	}
	if block.ToolUseID != "tu_1" {
		t.Errorf("expected tool_use_id tu_1, got %s", block.ToolUseID)
	}
	if !block.IsError {
		t.Error("expected IsError to be true")
	}

}

func TestCreateSyntheticErrorBlock_UserInterrupted(t *testing.T) {
	t.Parallel()
	block := CreateSyntheticErrorBlock("tu_1", AbortReasonUserInterrupted)
	var parsed string
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed != "User rejected tool use" {
		t.Errorf("unexpected error message: %q", parsed)
	}
}

func TestCreateSyntheticErrorBlock_StreamingFallback(t *testing.T) {
	t.Parallel()
	block := CreateSyntheticErrorBlock("tu_1", AbortReasonStreamingFallback)
	var parsed string
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed != "Error: Streaming fallback - tool execution discarded" {
		t.Errorf("unexpected error message: %q", parsed)
	}
}

func TestCreateSyntheticErrorBlock_SiblingError(t *testing.T) {
	t.Parallel()
	block := CreateSyntheticErrorBlock("tu_1", AbortReasonSiblingError)
	var parsed string
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed != "Cancelled: parallel tool call errored" {
		t.Errorf("unexpected error message: %q", parsed)
	}
}

func TestExtractTextBlocks(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewTextBlock("hello"),
			{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "bash"},
			types.NewTextBlock("world"),
		},
	}
	texts := ExtractTextBlocks(msg)
	if len(texts) != 2 {
		t.Fatalf("expected 2 text blocks, got %d", len(texts))
	}
	if texts[0] != "hello" || texts[1] != "world" {
		t.Errorf("unexpected texts: %v", texts)
	}
}

func TestExtractTextBlocks_Empty(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{},
	}
	texts := ExtractTextBlocks(msg)
	if len(texts) != 0 {
		t.Errorf("expected 0 text blocks, got %d", len(texts))
	}
}

func TestHasToolUseBlocks(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewTextBlock("no tools here"),
		},
	}
	if HasToolUseBlocks(msg) {
		t.Error("expected false for text-only message")
	}

	msg2 := types.Message{
		Content: []types.ContentBlock{
			types.NewTextBlock("text"),
			{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "bash"},
		},
	}
	if !HasToolUseBlocks(msg2) {
		t.Error("expected true for message with tool_use")
	}
}

func TestExtractToolUseBlocks(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewTextBlock("text"),
			{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "bash"},
			{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "grep"},
		},
	}
	blocks := ExtractToolUseBlocks(msg)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 tool use blocks, got %d", len(blocks))
	}
	if blocks[0].Name != "bash" || blocks[1].Name != "grep" {
		t.Errorf("unexpected names: %v", blocks)
	}
}

func TestEnsureToolResultPairing(t *testing.T) {
	t.Parallel()

	userText := func(text string) types.Message {
		return types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(text)},
		}
	}
	asst := func(blocks ...types.ContentBlock) types.Message {
		return types.Message{Role: types.RoleAssistant, Content: blocks}
	}
	textBlock := func(text string) types.ContentBlock {
		return types.NewTextBlock(text)
	}
	toolUse := func(id, name string) types.ContentBlock {
		return types.ContentBlock{Type: types.ContentTypeToolUse, ID: id, Name: name, Input: json.RawMessage(`{}`)}
	}
	toolResult := func(toolUseID, content string, isError bool) types.ContentBlock {
		return types.NewToolResultBlock(toolUseID, json.RawMessage(`"`+content+`"`), isError)
	}
	userWithBlocks := func(blocks ...types.ContentBlock) types.Message {
		return types.Message{Role: types.RoleUser, Content: blocks}
	}

	t.Run("balanced pairs pass through", func(t *testing.T) {
		t.Parallel()
		msgs := []types.Message{
			userText("hello"),
			asst(textBlock("hi"), toolUse("tu_1", "Read")),
			userWithBlocks(toolResult("tu_1", "file content", false)),
			asst(textBlock("done")),
		}
		result := EnsureToolResultPairing(msgs)
		if len(result) != 4 {
			t.Fatalf("expected 4 messages, got %d", len(result))
		}
	})

	t.Run("injects synthetic tool_result for orphan tool_use", func(t *testing.T) {
		t.Parallel()
		msgs := []types.Message{
			userText("hello"),
			asst(textBlock("hi"), toolUse("tu_1", "Read")),
			userText("next message without tool result"),
		}
		result := EnsureToolResultPairing(msgs)
		if len(result) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(result))
		}
		userMsg := result[2]
		if userMsg.Role != types.RoleUser {
			t.Fatal("expected user message after assistant")
		}
		hasSynth := false
		for _, b := range userMsg.Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_1" && b.IsError {
				hasSynth = true
			}
		}
		if !hasSynth {
			t.Error("expected synthetic tool_result for tu_1")
		}
	})

	t.Run("strips orphaned tool_result without matching tool_use", func(t *testing.T) {
		t.Parallel()
		msgs := []types.Message{
			userText("hello"),
			asst(textBlock("hi"), toolUse("tu_1", "Read")),
			userWithBlocks(toolResult("tu_1", "ok", false), toolResult("tu_orphan", "orphan result", false)),
		}
		result := EnsureToolResultPairing(msgs)
		if len(result) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(result))
		}
		for _, b := range result[2].Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_orphan" {
				t.Error("orphaned tool_result should have been stripped")
			}
		}
		found := false
		for _, b := range result[2].Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_1" {
				found = true
			}
		}
		if !found {
			t.Error("valid tool_result tu_1 should remain")
		}
	})

	t.Run("inserts synthetic user message when assistant with orphan tool_use has no next message", func(t *testing.T) {
		t.Parallel()
		msgs := []types.Message{
			userText("hello"),
			asst(textBlock("hi"), toolUse("tu_1", "Read")),
		}
		result := EnsureToolResultPairing(msgs)
		if len(result) != 3 {
			t.Fatalf("expected 3 messages (synthetic user inserted), got %d", len(result))
		}
		last := result[2]
		if last.Role != types.RoleUser {
			t.Fatal("expected user message inserted after assistant")
		}
		hasSynth := false
		for _, b := range last.Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_1" && b.IsError {
				hasSynth = true
			}
		}
		if !hasSynth {
			t.Error("expected synthetic tool_result for tu_1 in inserted user message")
		}
	})

	t.Run("duplicate tool_use IDs across assistants are stripped", func(t *testing.T) {
		t.Parallel()
		msgs := []types.Message{
			userText("hello"),
			asst(toolUse("tu_1", "Read")),
			userWithBlocks(toolResult("tu_1", "result 1", false)),
			userText("continue"),
			asst(toolUse("tu_1", "Read")),
			userWithBlocks(toolResult("tu_1", "result 2", false)),
		}
		result := EnsureToolResultPairing(msgs)
		toolUseCount := 0
		for _, m := range result {
			for _, b := range m.Content {
				if b.Type == types.ContentTypeToolUse && b.ID == "tu_1" {
					toolUseCount++
				}
			}
		}
		if toolUseCount != 1 {
			t.Errorf("expected exactly 1 tool_use block for tu_1 after dedup, got %d", toolUseCount)
		}
	})

	t.Run("all tool_results stripped replaces with placeholder when first message", func(t *testing.T) {
		t.Parallel()
		msgs := []types.Message{
			userWithBlocks(toolResult("tu_orphan", "orphan data", false)),
			asst(textBlock("response")),
		}
		result := EnsureToolResultPairing(msgs)
		if len(result) < 2 {
			t.Fatalf("expected at least 2 messages, got %d", len(result))
		}
		first := result[0]
		for _, b := range first.Content {
			if b.Type == types.ContentTypeToolResult {
				t.Error("orphaned tool_result in first message should have been stripped")
			}
		}
	})
}
