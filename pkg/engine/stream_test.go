package engine_test

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

func TestStreamAccumulator_TextOnly(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()

	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "Hello "}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "world!"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 5}},
		{Type: "message_stop"},
	}

	var textDeltas int
	for _, evt := range events {
		emit, err := acc.ProcessEvent(evt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if emit != nil && emit.Type == types.EventTextDelta {
			textDeltas++
		}
	}

	msg := acc.BuildMessage()
	if msg.Model != "test-model" {
		t.Errorf("expected model test-model, got %s", msg.Model)
	}
	if msg.StopReason != "end_turn" {
		t.Errorf("expected stop_reason end_turn, got %s", msg.StopReason)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "Hello world!" {
		t.Errorf("expected 'Hello world!', got %q", msg.Content[0].Text)
	}
	if textDeltas != 2 {
		t.Errorf("expected 2 text deltas, got %d", textDeltas)
	}
	if acc.HasToolUse() {
		t.Error("expected no tool use")
	}
	if msg.Usage.InputTokens != 10 {
		t.Errorf("expected 10 input tokens, got %d", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", msg.Usage.OutputTokens)
	}
}

func TestStreamAccumulator_ToolUse(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()

	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 20}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "bash"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"ls"}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 10}},
		{Type: "message_stop"},
	}

	var toolStartSeen bool
	for _, evt := range events {
		emit, err := acc.ProcessEvent(evt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if emit != nil && emit.Type == types.EventToolStart {
			toolStartSeen = true
			if emit.ToolUse.ID != "tu_1" {
				t.Errorf("expected tool use ID tu_1, got %s", emit.ToolUse.ID)
			}
			if emit.ToolUse.Name != "bash" {
				t.Errorf("expected tool name bash, got %s", emit.ToolUse.Name)
			}
		}
	}

	if !toolStartSeen {
		t.Error("expected tool use start event")
	}
	if !acc.HasToolUse() {
		t.Error("expected HasToolUse to be true")
	}

	blocks := acc.ToolUseBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 tool use block, got %d", len(blocks))
	}
	if blocks[0].Name != "bash" {
		t.Errorf("expected tool name bash, got %s", blocks[0].Name)
	}
	var input map[string]string
	if err := json.Unmarshal(blocks[0].Input, &input); err != nil {
		t.Fatalf("failed to parse tool input: %v", err)
	}
	if input["command"] != "ls" {
		t.Errorf("expected command ls, got %s", input["command"])
	}
}

func TestStreamAccumulator_MixedTextAndToolUse(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()

	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "Let me check."}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "grep"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"pattern":"todo"}`}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}},
		{Type: "message_stop"},
	}

	for _, evt := range events {
		_, err := acc.ProcessEvent(evt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	msg := acc.BuildMessage()
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeText {
		t.Errorf("expected first block to be text")
	}
	if msg.Content[1].Type != types.ContentTypeToolUse {
		t.Errorf("expected second block to be tool_use")
	}
}

func TestStreamAccumulator_Ping(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()

	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test"}},
		{Type: "ping"},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "pong"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}},
		{Type: "message_stop"},
	}

	for _, evt := range events {
		emit, err := acc.ProcessEvent(evt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// ping should not emit any event
		if evt.Type == "ping" && emit != nil {
			t.Error("ping should not emit event")
		}
	}

	msg := acc.BuildMessage()
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "pong" {
		t.Errorf("expected 'pong', got %q", msg.Content[0].Text)
	}
}

func TestStreamAccumulator_StreamError(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()

	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "partial"}},
		{Error: &llm.APIError{Message: "stream interrupted", Status: 500}},
	}

	for i, evt := range events {
		_, err := acc.ProcessEvent(evt)
		if i == 3 {
			if err == nil {
				t.Fatal("expected error from stream event error")
			}
			if err.Error() != "stream interrupted" {
				t.Errorf("unexpected error message: %v", err)
			}
		} else if err != nil {
			t.Fatalf("unexpected error at event %d: %v", i, err)
		}
	}
}

func TestStreamAccumulator_NilFields(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()

	events := []llm.StreamEvent{
		{Type: "message_start"},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "no usage"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta"},
		{Type: "message_stop"},
	}

	for _, evt := range events {
		_, err := acc.ProcessEvent(evt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	msg := acc.BuildMessage()
	if msg.Model != "" {
		t.Errorf("expected empty model for nil MessageStart, got %s", msg.Model)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
}

func TestStreamAccumulator_BuildMessage_HasContentNoBlocks(t *testing.T) {
	t.Parallel()
	// Edge case: text deltas received but no content_block_start
	// This shouldn't happen in practice but BuildMessage has a fallback.
	acc := engine.NewStreamAccumulator()

	// Simulate receiving text_delta without content_block_start
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "orphan text"}},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}},
		{Type: "message_stop"},
	}

	for _, evt := range events {
		_, err := acc.ProcessEvent(evt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	msg := acc.BuildMessage()
	// hasContent=true but contentBlocks is empty → fallback creates a text block
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 fallback content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "orphan text" {
		t.Errorf("expected 'orphan text', got %q", msg.Content[0].Text)
	}
}

func TestStreamAccumulator_ToolUseBlocksEmpty(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()
	blocks := acc.ToolUseBlocks()
	if blocks != nil {
		t.Errorf("expected nil, got %v", blocks)
	}
}

func TestStreamAccumulator_ToolUseBlocksNotEmpty(t *testing.T) {
	t.Parallel()
	acc := engine.NewStreamAccumulator()

	// Simulate processing tool events
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tool_123", Name: "test_tool"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"arg":"value"}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_stop"},
	}

	for _, evt := range events {
		_, err := acc.ProcessEvent(evt)
		if err != nil {
			t.Fatalf("ProcessEvent error: %v", err)
		}
	}

	blocks := acc.ToolUseBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 tool use block, got %d", len(blocks))
	}
	if blocks[0].Name != "test_tool" {
		t.Errorf("expected tool name 'test_tool', got %s", blocks[0].Name)
	}
}
