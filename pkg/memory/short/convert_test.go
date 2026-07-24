package short

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func TestEngineMessagesToStore(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns nil", func(t *testing.T) {
		result, err := EngineMessagesToStore(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil, got %d items", len(result))
		}
	})

	t.Run("converts messages with UUID", func(t *testing.T) {
		msgs := []types.Message{
			{
				ID:        "uuid-1",
				Role:      types.RoleUser,
				Content:   []types.ContentBlock{types.NewTextBlock("hello")},
				Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		result, err := EngineMessagesToStore(msgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 message, got %d", len(result))
		}
		if result[0].UUID != "uuid-1" {
			t.Errorf("UUID = %q, want uuid-1", result[0].UUID)
		}
		if result[0].Type != "user" {
			t.Errorf("Type = %q, want user", result[0].Type)
		}
		if !strings.Contains(result[0].Content, "hello") {
			t.Errorf("Content = %q, should contain hello", result[0].Content)
		}
	})

	t.Run("generates UUID when empty", func(t *testing.T) {
		msgs := []types.Message{
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		}
		result, err := EngineMessagesToStore(msgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result[0].UUID == "" {
			t.Error("UUID should be auto-generated when empty")
		}
	})
}

func TestStoreMessageToEngine_ImageSourceRoundTrip(t *testing.T) {
	t.Parallel()
	// Regression: image content block lost its Source field through
	// store round-trip because the persisted block had no Source field.
	// Manifestation: API error "source is required when type=image".
	srcBlock := types.NewImageBlock(types.ImageSource{
		Type:      "base64",
		MediaType: "image/png",
		Data:      "iVBORw0KGgo=",
	})
	engineMsg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{srcBlock},
	}

	storeMsgs, err := EngineMessagesToStore([]types.Message{engineMsg})
	if err != nil {
		t.Fatalf("EngineMessagesToStore error: %v", err)
	}
	if len(storeMsgs) != 1 {
		t.Fatalf("expected 1 store message, got %d", len(storeMsgs))
	}

	back := StoreMessageToEngine(storeMsgs[0])
	if len(back.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(back.Content))
	}
	got := back.Content[0]
	if got.Type != types.ContentTypeImage {
		t.Fatalf("Type = %q, want image", got.Type)
	}
	if got.Source == nil {
		t.Fatal("Source = nil, want non-nil (source is required when type=image)")
	}
	if got.Source.Type != "base64" {
		t.Errorf("Source.Type = %q, want base64", got.Source.Type)
	}
	if got.Source.MediaType != "image/png" {
		t.Errorf("Source.MediaType = %q, want image/png", got.Source.MediaType)
	}
	if got.Source.Data != "iVBORw0KGgo=" {
		t.Errorf("Source.Data = %q, want iVBORw0KGgo=", got.Source.Data)
	}
}

func TestStoreMessageToEngine(t *testing.T) {
	t.Parallel()

	t.Run("nil input returns empty", func(t *testing.T) {
		msg := StoreMessageToEngine(nil)
		if msg.Role != "" {
			t.Errorf("expected empty role, got %q", msg.Role)
		}
	})

	t.Run("valid JSON content", func(t *testing.T) {
		blocks := []types.ContentBlock{
			{Type: "text", Text: "response"},
		}
		content, _ := json.Marshal(blocks)
		sm := &TranscriptMessage{
			UUID:    "uuid-2",
			Type:    "assistant",
			Content: string(content),
		}
		msg := StoreMessageToEngine(sm)
		if msg.ID != "uuid-2" {
			t.Errorf("ID = %q, want uuid-2", msg.ID)
		}
		if msg.Role != types.RoleAssistant {
			t.Errorf("Role = %q, want assistant", msg.Role)
		}
		if len(msg.Content) != 1 || msg.Content[0].Text != "response" {
			t.Errorf("Content[0].Text = %q, want response", msg.Content[0].Text)
		}
	})

	t.Run("invalid JSON falls back to text block", func(t *testing.T) {
		sm := &TranscriptMessage{
			UUID:    "uuid-3",
			Type:    "user",
			Content: "plain text, not JSON",
		}
		msg := StoreMessageToEngine(sm)
		if len(msg.Content) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(msg.Content))
		}
		if msg.Content[0].Type != types.ContentTypeText {
			t.Errorf("Type = %q, want text", msg.Content[0].Type)
		}
		if msg.Content[0].Text != "plain text, not JSON" {
			t.Errorf("Text = %q, want fallback text", msg.Content[0].Text)
		}
	})
}

func TestStoreMessagesToEngine(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns nil", func(t *testing.T) {
		result, err := StoreMessagesToEngine(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil, got %d items", len(result))
		}
	})

	t.Run("valid messages", func(t *testing.T) {
		blocks := []types.ContentBlock{{Type: "text", Text: "hi"}}
		content, _ := json.Marshal(blocks)
		storeMsgs := []*TranscriptMessage{
			{UUID: "u1", Type: "user", Content: string(content)},
			{UUID: "a1", Type: "assistant", Content: string(content)},
		}
		result, err := StoreMessagesToEngine(storeMsgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(result))
		}
		if result[0].Role != types.RoleUser {
			t.Errorf("Role[0] = %q, want user", result[0].Role)
		}
		if result[1].Role != types.RoleAssistant {
			t.Errorf("Role[1] = %q, want assistant", result[1].Role)
		}
	})

	t.Run("unknown role returns error", func(t *testing.T) {
		storeMsgs := []*TranscriptMessage{
			{UUID: "x1", Type: "unknown_role", Content: "{}"},
		}
		_, err := StoreMessagesToEngine(storeMsgs)
		if err == nil {
			t.Fatal("expected error for unknown role")
		}
		if !strings.Contains(err.Error(), "unknown message role") {
			t.Errorf("error = %q, should mention unknown role", err.Error())
		}
	})

	t.Run("system role accepted", func(t *testing.T) {
		blocks := []types.ContentBlock{{Type: "text", Text: "system prompt"}}
		content, _ := json.Marshal(blocks)
		storeMsgs := []*TranscriptMessage{
			{UUID: "s1", Type: "system", Content: string(content)},
		}
		result, err := StoreMessagesToEngine(storeMsgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result[0].Role != types.RoleSystem {
			t.Errorf("Role = %q, want system", result[0].Role)
		}
	})
}

// TestEngineMessagesToStore_PreservesDuration verifies the persistence path
// (EngineMessagesToStore → DB string → StoreMessageToEngine) round-trips both
// ThinkingDurationNs and ToolDurationNs with their exact integer values.
// Guards against a regression where someone reverts Step 4 to plain json.Marshal,
// which would silently drop the duration fields via the custom MarshalJSON.
func TestEngineMessagesToStore_PreservesDuration(t *testing.T) {
	t.Parallel()

	em := types.Message{
		ID:   "asst-dur",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Thinking: "ponder", ThinkingDurationNs: 2_000_000_000},
			{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`"ok"`), ToolDurationNs: 4_000_000_000},
		},
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	storeMsgs, err := EngineMessagesToStore([]types.Message{em})
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if len(storeMsgs) != 1 {
		t.Fatalf("expected 1 store message, got %d", len(storeMsgs))
	}
	if !strings.Contains(storeMsgs[0].Content, `"thinking_duration_ns":2000000000`) {
		t.Errorf("store Content missing thinking_duration_ns=2000000000: %s", storeMsgs[0].Content)
	}
	if !strings.Contains(storeMsgs[0].Content, `"tool_duration_ns":4000000000`) {
		t.Errorf("store Content missing tool_duration_ns=4000000000: %s", storeMsgs[0].Content)
	}

	back := StoreMessageToEngine(storeMsgs[0])
	if len(back.Content) != 2 {
		t.Fatalf("round-trip content len = %d, want 2", len(back.Content))
	}
	if back.Content[0].ThinkingDurationNs != 2_000_000_000 {
		t.Errorf("back.Content[0].ThinkingDurationNs = %d, want 2000000000", back.Content[0].ThinkingDurationNs)
	}
	if back.Content[1].ToolDurationNs != 4_000_000_000 {
		t.Errorf("back.Content[1].ToolDurationNs = %d, want 4000000000", back.Content[1].ToolDurationNs)
	}
}
