package engine_test

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
)

func TestCreateUserMessage(t *testing.T) {
	t.Parallel()
	msg := engine.CreateUserMessage("hello")
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
	msg := engine.CreateAssistantMessage("response text")
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
	msg := engine.CreateToolResultMessage(blocks)
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
	block := engine.CreateToolErrorBlock("tu_1", "something broke")
	if block.Type != types.ContentTypeToolResult {
		t.Errorf("expected ContentTypeToolResult, got %s", block.Type)
	}
	if block.ToolUseID != "tu_1" {
		t.Errorf("expected tool_use_id tu_1, got %s", block.ToolUseID)
	}
	if !block.IsError {
		t.Error("expected IsError to be true")
	}

	var parsed map[string]string
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse content: %v", err)
	}
	if parsed["error"] != "something broke" {
		t.Errorf("expected error 'something broke', got %q", parsed["error"])
	}
}

func TestCreateSyntheticErrorBlock_UserInterrupted(t *testing.T) {
	t.Parallel()
	block := engine.CreateSyntheticErrorBlock("tu_1", "user_interrupted")
	var parsed map[string]string
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed["error"] != "User rejected tool use" {
		t.Errorf("unexpected error message: %q", parsed["error"])
	}
}

func TestCreateSyntheticErrorBlock_StreamingFallback(t *testing.T) {
	t.Parallel()
	block := engine.CreateSyntheticErrorBlock("tu_1", "streaming_fallback")
	var parsed map[string]string
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed["error"] != "Error: Streaming fallback - tool execution discarded" {
		t.Errorf("unexpected error message: %q", parsed["error"])
	}
}

func TestCreateSyntheticErrorBlock_SiblingError(t *testing.T) {
	t.Parallel()
	block := engine.CreateSyntheticErrorBlock("tu_1", "sibling_error")
	var parsed map[string]string
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed["error"] != "Cancelled: parallel tool call errored" {
		t.Errorf("unexpected error message: %q", parsed["error"])
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
	texts := engine.ExtractTextBlocks(msg)
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
	texts := engine.ExtractTextBlocks(msg)
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
	if engine.HasToolUseBlocks(msg) {
		t.Error("expected false for text-only message")
	}

	msg2 := types.Message{
		Content: []types.ContentBlock{
			types.NewTextBlock("text"),
			{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "bash"},
		},
	}
	if !engine.HasToolUseBlocks(msg2) {
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
	blocks := engine.ExtractToolUseBlocks(msg)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 tool use blocks, got %d", len(blocks))
	}
	if blocks[0].Name != "bash" || blocks[1].Name != "grep" {
		t.Errorf("unexpected names: %v", blocks)
	}
}
