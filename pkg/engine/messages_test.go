package engine

import (
	"encoding/json"
	"testing"
	"time"

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
	if len(block.Content) == 0 || block.Content[0] != '[' {
		t.Fatalf("expected array-form content starting with '[', got %q", string(block.Content))
	}
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to unmarshal as array: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 inner block, got %d", len(parsed))
	}
	if parsed[0].Type != types.ContentTypeText {
		t.Errorf("expected inner block Type text, got %s", parsed[0].Type)
	}
	if parsed[0].Text != "something broke" {
		t.Errorf("expected inner text 'something broke', got %q", parsed[0].Text)
	}
}

func TestCreateSyntheticErrorBlock_UserInterrupted(t *testing.T) {
	t.Parallel()
	block := CreateSyntheticErrorBlock("tu_1", AbortReasonUserInterrupted)
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 inner block, got %d", len(parsed))
	}
	if parsed[0].Type != types.ContentTypeText {
		t.Errorf("expected inner Type text, got %s", parsed[0].Type)
	}
	if parsed[0].Text != userRejectMessage {
		t.Errorf("unexpected error message: %q, want userRejectMessage", parsed[0].Text)
	}
}

func TestCreateSyntheticErrorBlock_StreamingFallback(t *testing.T) {
	t.Parallel()
	block := CreateSyntheticErrorBlock("tu_1", AbortReasonStreamingFallback)
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 inner block, got %d", len(parsed))
	}
	if parsed[0].Type != types.ContentTypeText {
		t.Errorf("expected inner Type text, got %s", parsed[0].Type)
	}
	if parsed[0].Text != "Error: Streaming fallback - tool execution discarded" {
		t.Errorf("unexpected error message: %q", parsed[0].Text)
	}
}

func TestCreateSyntheticErrorBlock_SiblingError(t *testing.T) {
	t.Parallel()
	block := CreateSyntheticErrorBlock("tu_1", AbortReasonSiblingError)
	var parsed []types.ContentBlock
	if err := json.Unmarshal(block.Content, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 inner block, got %d", len(parsed))
	}
	if parsed[0].Type != types.ContentTypeText {
		t.Errorf("expected inner Type text, got %s", parsed[0].Type)
	}
	if parsed[0].Text != "Cancelled: parallel tool call errored" {
		t.Errorf("unexpected error message: %q", parsed[0].Text)
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

	// TS rebuilds with object spread, keeping every field; the Go port once
	// dropped ID/Timestamp on rebuild, which collapsed the budget's
	// per-assistant grouping when it ran after pairing.
	t.Run("rebuild preserves message fields (TS object spread)", func(t *testing.T) {
		t.Parallel()
		msgs := []types.Message{
			userText("hello"),
			{
				ID:        "asst-keep-me",
				Role:      types.RoleAssistant,
				Content:   []types.ContentBlock{textBlock("hi"), toolUse("tu_1", "Read")},
				Model:     "glm-5.2",
				Timestamp: time.Date(2026, 8, 16, 23, 0, 0, 0, time.UTC),
			},
			userWithBlocks(toolResult("tu_1", "file content", false)),
		}
		result := EnsureToolResultPairing(msgs)
		if len(result) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(result))
		}
		got := result[1]
		if got.ID != "asst-keep-me" {
			t.Errorf("assistant ID = %q, want asst-keep-me", got.ID)
		}
		if got.Model != "glm-5.2" {
			t.Errorf("Model = %q, want glm-5.2", got.Model)
		}
		if got.Timestamp.IsZero() {
			t.Error("Timestamp zeroed by rebuild")
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

func TestEnsureToolResultPairing_SyntheticBlockArrayForm(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("hi"),
			{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Read", Input: json.RawMessage(`{}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("no tool result")}},
	}
	result := EnsureToolResultPairing(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	userMsg := result[2]
	var synth *types.ContentBlock
	for i := range userMsg.Content {
		b := &userMsg.Content[i]
		if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_1" && b.IsError {
			synth = b
			break
		}
	}
	if synth == nil {
		t.Fatal("expected synthetic tool_result for tu_1")
	}
	if len(synth.Content) == 0 || synth.Content[0] != '[' {
		t.Fatalf("expected array-form synthetic content, got %q", string(synth.Content))
	}
	var inner []types.ContentBlock
	if err := json.Unmarshal(synth.Content, &inner); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(inner) != 1 || inner[0].Type != types.ContentTypeText {
		t.Fatalf("expected single text inner block, got %+v", inner)
	}
	if inner[0].Text != syntheticToolResultPlaceholder {
		t.Errorf("expected placeholder text, got %q", inner[0].Text)
	}
}

func TestStripMediaFromMessages_TopLevelImage(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("look"),
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "abc"}),
		},
	}}

	result := StripMediaFromMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	blocks := result[0].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "look" {
		t.Errorf("blocks[0] = %+v, want text 'look'", blocks[0])
	}
	if blocks[1].Type != types.ContentTypeText || blocks[1].Text != "[image]" {
		t.Errorf("blocks[1] = %+v, want text '[image]'", blocks[1])
	}
	// Input must not be mutated.
	if msgs[0].Content[1].Type != types.ContentTypeImage {
		t.Error("original message was mutated")
	}
}

func TestStripMediaFromMessages_TopLevelVideo(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("watch"),
			{Type: types.ContentTypeVideo, Source: &types.ImageSource{Type: "base64", MediaType: "video/mp4", Data: "xyz"}},
		},
	}}

	result := StripMediaFromMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	blocks := result[0].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(blocks))
	}
	if blocks[1].Type != types.ContentTypeText || blocks[1].Text != "[video]" {
		t.Errorf("blocks[1] = %+v, want text '[video]'", blocks[1])
	}
}

func TestStripMediaFromMessages_AssistantUntouched(t *testing.T) {
	t.Parallel()

	// Assistant messages are not processed even if they somehow hold an image.
	msgs := []types.Message{{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "abc"}),
		},
	}}
	result := StripMediaFromMessages(msgs)
	if len(result) != 1 || len(result[0].Content) != 1 {
		t.Fatalf("assistant message should be untouched, got %+v", result)
	}
	if result[0].Content[0].Type != types.ContentTypeImage {
		t.Errorf("assistant image block type = %q, want image (untouched)", result[0].Content[0].Type)
	}
}

func TestStripMediaFromMessages_NestedToolResultImage(t *testing.T) {
	t.Parallel()

	// tool_result.content is a JSON array containing a nested image block.
	nested, _ := json.Marshal([]types.ContentBlock{
		types.NewTextBlock("desc"),
		types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "abc"}),
	})
	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", Content: nested},
		},
	}}

	result := StripMediaFromMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	tr := result[0].Content[0]
	if tr.Type != types.ContentTypeToolResult {
		t.Fatalf("block type = %q, want tool_result", tr.Type)
	}
	var inner []types.ContentBlock
	if err := json.Unmarshal(tr.Content, &inner); err != nil {
		t.Fatalf("unmarshal nested: %v", err)
	}
	if len(inner) != 2 {
		t.Fatalf("nested len = %d, want 2", len(inner))
	}
	if inner[0].Type != types.ContentTypeText || inner[0].Text != "desc" {
		t.Errorf("nested[0] = %+v, want text 'desc'", inner[0])
	}
	if inner[1].Type != types.ContentTypeText || inner[1].Text != "[image]" {
		t.Errorf("nested[1] = %+v, want text '[image]'", inner[1])
	}
}

func TestStripMediaFromMessages_NoMutatesWhenNoMedia(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("just text")},
	}}
	result := StripMediaFromMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0].Content[0].Text != "just text" {
		t.Errorf("text = %q, want 'just text'", result[0].Content[0].Text)
	}
}

// --- TestM_*: second-round mutation gaps in EnsureToolResultPairing and
// its helpers.

func trBlock(id string) types.ContentBlock {
	return types.NewToolResultBlock(id, json.RawMessage(`"r"`), false)
}

func tuBlock(id string) types.ContentBlock {
	return types.NewToolUseBlock(id, "Read", json.RawMessage(`{"file_path":"x"}`))
}

func countBlocks(msgs []types.Message, ctype types.ContentType) int {
	n := 0
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == ctype {
				n++
			}
		}
	}
	return n
}

func TestM_ExtractTextBlocksSkipsEmptyAndNonText(t *testing.T) {
	msg := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
		types.NewTextBlock("kept"),
		types.NewTextBlock(""),
		{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", Text: "tool result text"},
	}}
	got := ExtractTextBlocks(msg)
	if len(got) != 1 || got[0] != "kept" {
		t.Fatalf("ExtractTextBlocks = %q, want [\"kept\"]", got)
	}
}

func TestM_OrphanStripOnlyAppliesToUserMessages(t *testing.T) {
	sys := types.Message{ID: "mutf-sys-tr", Role: types.RoleSystem, Content: []types.ContentBlock{trBlock("tu_1")}}
	got := EnsureToolResultPairing([]types.Message{sys})
	if len(got) != 1 {
		t.Fatalf("pairing result = %d messages, want 1: %+v", len(got), got)
	}
	if got[0].Role != types.RoleSystem {
		t.Errorf("role = %q, want system", got[0].Role)
	}
	if len(got[0].Content) != 1 || got[0].Content[0].Type != types.ContentTypeToolResult ||
		got[0].Content[0].ToolUseID != "tu_1" {
		t.Errorf("system message content = %+v, want the tool_result block untouched", got[0].Content)
	}
}

func TestM_OrphanStripWhenPrevRoleIsUser(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{
		CreateUserMessage("question"),
		{Role: types.RoleUser, Content: []types.ContentBlock{trBlock("tu_orphan")}},
	})
	if len(got) != 1 {
		t.Fatalf("pairing result = %d messages, want 1 (tool_result after a user message is orphaned): %+v",
			len(got), got)
	}
	if countBlocks(got, types.ContentTypeToolResult) != 0 {
		t.Errorf("orphaned tool_result survived: %+v", got)
	}
}

func TestM_PairedToolResultAfterAssistantSurvives(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tuBlock("tu_1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{trBlock("tu_1")}},
	})
	if len(got) != 2 {
		t.Fatalf("pairing result = %d messages, want 2: %+v", len(got), got)
	}
	if got[1].Role != types.RoleUser || countBlocks([]types.Message{got[1]}, types.ContentTypeToolResult) != 1 {
		t.Errorf("paired user message = %+v, want its tool_result kept", got[1])
	}
}

func TestM_StripKeepsSingleRemainingBlock(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("keep"), trBlock("tu_1")}},
	})
	if len(got) != 1 {
		t.Fatalf("pairing result = %d messages, want 1: %+v", len(got), got)
	}
	if len(got[0].Content) != 1 || got[0].Content[0].Type != types.ContentTypeText ||
		got[0].Content[0].Text != "keep" {
		t.Errorf("content = %+v, want the single non-tool_result block kept", got[0].Content)
	}
}

func TestM_DuplicateToolUseDropsOnlyTheDuplicate(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			tuBlock("tu_dup"), types.NewTextBlock("mid"), tuBlock("tu_dup"), types.NewTextBlock("tail"),
		},
	}})
	if len(got) != 2 {
		t.Fatalf("pairing result = %d messages, want 2 (assistant + synthetic result): %+v", len(got), got)
	}
	if len(got[0].Content) != 3 {
		t.Fatalf("assistant content = %d blocks, want 3 (dup dropped, later blocks kept): %+v",
			len(got[0].Content), got[0].Content)
	}
	if got[0].Content[2].Type != types.ContentTypeText || got[0].Content[2].Text != "tail" {
		t.Errorf("last block = %+v, want text \"tail\"", got[0].Content[2])
	}
}

func TestM_EmptyAssistantGetsInterruptedPlaceholder(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{{Role: types.RoleAssistant}})
	if len(got) != 1 {
		t.Fatalf("pairing result = %d messages, want 1", len(got))
	}
	if len(got[0].Content) != 1 || got[0].Content[0].Text != "[Tool use interrupted]" {
		t.Fatalf("assistant content = %+v, want the interrupted placeholder", got[0].Content)
	}
}

func TestM_CleanPairingDoesNotRewriteNextMessage(t *testing.T) {
	// Nothing is missing, orphaned, or duplicated: the assistant must not enter
	// the patch path, so the following empty user message survives untouched
	// instead of being replaced by the "[No content]" placeholder.
	got := EnsureToolResultPairing([]types.Message{
		CreateAssistantMessage("all fine"),
		{Role: types.RoleUser},
	})
	if len(got) != 2 {
		t.Fatalf("pairing result = %d messages, want 2: %+v", len(got), got)
	}
	if len(got[1].Content) != 0 {
		t.Errorf("empty user message rewritten to %+v, want its empty content preserved", got[1].Content)
	}
	if got[1].Flags&types.FlagMeta != 0 {
		t.Errorf("empty user message Flags = %v, want no FlagMeta (message was not synthesized)", got[1].Flags)
	}
}

func TestM_DuplicateToolResultsDedupedInPlace(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tuBlock("tu_1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			trBlock("tu_1"), trBlock("tu_1"), types.NewTextBlock("after"),
		}},
	})
	if len(got) != 2 {
		t.Fatalf("pairing result = %d messages, want 2: %+v", len(got), got)
	}
	if n := countBlocks([]types.Message{got[1]}, types.ContentTypeToolResult); n != 1 {
		t.Errorf("tool_result blocks after dedup = %d, want 1: %+v", n, got[1].Content)
	}
	if len(got[1].Content) != 2 || got[1].Content[1].Text != "after" {
		t.Errorf("patched content = %+v, want [tool_result, text \"after\"]", got[1].Content)
	}
}

func TestM_OrphanedToolResultStrippedFromNextMessage(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{
		CreateAssistantMessage("no tool use here"),
		{Role: types.RoleUser, Content: []types.ContentBlock{trBlock("tu_orphan"), types.NewTextBlock("keep")}},
	})
	if len(got) != 2 {
		t.Fatalf("pairing result = %d messages, want 2: %+v", len(got), got)
	}
	if len(got[1].Content) != 1 || got[1].Content[0].Text != "keep" {
		t.Errorf("patched content = %+v, want only the text block (orphan stripped)", got[1].Content)
	}
}

func TestM_PatchPathOnlyConsumesUserMessages(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tuBlock("tu_1")}},
		CreateAssistantMessage("next assistant"),
	})
	if len(got) != 3 {
		t.Fatalf("pairing result = %d messages, want 3 (synthetic user between the assistants): %+v", len(got), got)
	}
	if got[1].Role != types.RoleUser || countBlocks([]types.Message{got[1]}, types.ContentTypeToolResult) != 1 {
		t.Errorf("message 1 = %+v, want synthetic user carrying the missing tool_result", got[1])
	}
	if got[2].Role != types.RoleAssistant || len(got[2].Content) != 1 ||
		got[2].Content[0].Text != "next assistant" {
		t.Errorf("message 2 = %+v, want the following assistant untouched", got[2])
	}
}

func TestM_MissingResultWhenAssistantIsLastMessage(t *testing.T) {
	got := EnsureToolResultPairing([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tuBlock("tu_last")}},
	})
	if len(got) != 2 {
		t.Fatalf("pairing result = %d messages, want 2: %+v", len(got), got)
	}
	if got[1].Role != types.RoleUser || got[1].Flags&types.FlagMeta == 0 {
		t.Errorf("synthetic message = %+v, want user role with FlagMeta", got[1])
	}
	if len(got[1].Content) != 1 || got[1].Content[0].Type != types.ContentTypeToolResult ||
		got[1].Content[0].ToolUseID != "tu_last" {
		t.Errorf("synthetic content = %+v, want one tool_result for tu_last", got[1].Content)
	}
}

func TestM_EmptyPatchInsertsNoContentMetaMessage(t *testing.T) {
	// The only tool_result in the next user message is orphaned, so after
	// stripping and with nothing missing, the patched content is empty.
	got := EnsureToolResultPairing([]types.Message{
		CreateAssistantMessage("no tool use"),
		{Role: types.RoleUser, Content: []types.ContentBlock{trBlock("tu_orphan")}},
	})
	if len(got) != 2 {
		t.Fatalf("pairing result = %d messages, want 2: %+v", len(got), got)
	}
	if got[1].Role != types.RoleUser {
		t.Errorf("placeholder role = %q, want user", got[1].Role)
	}
	if len(got[1].Content) != 1 || got[1].Content[0].Type != types.ContentTypeText ||
		got[1].Content[0].Text != "[No content]" {
		t.Errorf("placeholder content = %+v, want one text block \"[No content]\"", got[1].Content)
	}
	if got[1].Flags&types.FlagMeta == 0 {
		t.Errorf("placeholder Flags = %v, want FlagMeta set", got[1].Flags)
	}
}

func TestStripToolResults(t *testing.T) {
	in := []types.ContentBlock{types.NewTextBlock("a"), trBlock("tu_1"), types.NewTextBlock("b")}
	got := stripToolResults(types.Message{Role: types.RoleUser, Content: in})
	if len(got) != 2 {
		t.Fatalf("stripToolResults returned %d blocks, want 2: %+v", len(got), got)
	}
	if got[0].Text != "a" || got[1].Text != "b" {
		t.Errorf("stripToolResults = %+v, want the two text blocks in order", got)
	}
	if all := stripToolResults(types.Message{Role: types.RoleUser,
		Content: []types.ContentBlock{trBlock("tu_1")}}); len(all) != 0 {
		t.Errorf("stripToolResults(all tool_results) = %+v, want empty", all)
	}
}

func TestLastRoleEmptySlice(t *testing.T) {
	if got := lastRole(nil); got != "" {
		t.Errorf("lastRole(nil) = %q, want empty role", got)
	}
	if got := lastRole([]types.Message{}); got != "" {
		t.Errorf("lastRole(empty) = %q, want empty role", got)
	}
	if got := lastRole([]types.Message{{Role: types.RoleUser}, {Role: types.RoleAssistant}}); got != types.RoleAssistant {
		t.Errorf("lastRole = %q, want assistant", got)
	}
}

func TestM_NextMessageScanRespectsRole(t *testing.T) {
	// The existing-result scan must look only at a following USER message: an
	// assistant carrying a stray tool_result must not satisfy the pairing.
	got := EnsureToolResultPairing([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tuBlock("tu_1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{trBlock("tu_1")}},
	})
	if len(got) != 3 {
		t.Fatalf("pairing result = %d messages, want 3 (assistant, synthetic user, assistant): %+v", len(got), got)
	}
	if got[1].Role != types.RoleUser || got[1].Flags&types.FlagMeta == 0 {
		t.Errorf("message 1 = %+v, want synthetic user with FlagMeta", got[1])
	}
	if countBlocks([]types.Message{got[1]}, types.ContentTypeToolResult) != 1 ||
		got[1].Content[0].ToolUseID != "tu_1" {
		t.Errorf("synthetic content = %+v, want one tool_result for tu_1", got[1].Content)
	}
	if got[2].Role != types.RoleAssistant {
		t.Errorf("message 2 role = %q, want assistant", got[2].Role)
	}
}
