package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func TestStoreMessagesToEngine_Empty(t *testing.T) {
	result, err := StoreMessagesToEngine(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for nil input, got %v", result)
	}

	result, err = StoreMessagesToEngine([]short.TranscriptMessage{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}
}

func TestEngineMessagesToStore_Empty(t *testing.T) {
	result, err := EngineMessagesToStore(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for nil input, got %v", result)
	}

	result, err = EngineMessagesToStore([]types.Message{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}
}

func TestStoreMessagesToEngine_InvalidRole(t *testing.T) {
	msgs := []short.TranscriptMessage{
		{Type: "alien", Content: `[{"type":"text","text":"hello"}]`, CreatedAt: time.Now()},
	}
	_, err := StoreMessagesToEngine(msgs)
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
	if !strings.Contains(err.Error(), "unknown message role") {
		t.Errorf("error should mention unknown message role, got: %v", err)
	}
}

func TestMessageConvert_RoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond) // truncate to avoid monotonic clock differences

	tests := []struct {
		name    string
		message types.Message
	}{
		{
			name: "user text message",
			message: types.Message{
				Role:      types.RoleUser,
				Content:   []types.ContentBlock{types.NewTextBlock("Hello, world!")},
				Timestamp: now,
			},
		},
		{
			name: "assistant text message",
			message: types.Message{
				Role:      types.RoleAssistant,
				Content:   []types.ContentBlock{types.NewTextBlock("Hi there!")},
				Timestamp: now,
			},
		},
		{
			name: "system message",
			message: types.Message{
				Role:      types.RoleSystem,
				Content:   []types.ContentBlock{types.NewTextBlock("System prompt")},
				Timestamp: now,
			},
		},
		{
			name: "tool_use block",
			message: types.Message{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					types.NewToolUseBlock("toolu_123", "Read", json.RawMessage(`{"file_path":"/tmp/test.go"}`)),
				},
				Timestamp: now,
			},
		},
		{
			name: "tool_result block",
			message: types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewToolResultBlock("toolu_123", json.RawMessage(`"file contents here"`), false),
				},
				Timestamp: now,
			},
		},
		{
			name: "thinking block",
			message: types.Message{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeThinking, Text: "Let me think about this..."},
				},
				Timestamp: now,
			},
		},
		{
			name: "redacted_thinking block",
			message: types.Message{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeRedacted, Data: "redacted_data_blob_12345"},
				},
				Timestamp: now,
			},
		},
		{
			name: "mixed content blocks",
			message: types.Message{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeThinking, Text: "hmm"},
					types.NewTextBlock("Here is the answer"),
					types.NewToolUseBlock("toolu_456", "Bash", json.RawMessage(`{"command":"ls -la"}`)),
				},
				Timestamp: now,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Engine → Store → Engine round-trip
			storeMsgs, err := EngineMessagesToStore([]types.Message{tc.message})
			if err != nil {
				t.Fatalf("EngineMessagesToStore error: %v", err)
			}
			if len(storeMsgs) != 1 {
				t.Fatalf("expected 1 store message, got %d", len(storeMsgs))
			}

			gotMsgs, err := StoreMessagesToEngine(storeMsgs)
			if err != nil {
				t.Fatalf("StoreMessagesToEngine error: %v", err)
			}
			if len(gotMsgs) != 1 {
				t.Fatalf("expected 1 engine message, got %d", len(gotMsgs))
			}

			got := gotMsgs[0]

			// Verify role
			if got.Role != tc.message.Role {
				t.Errorf("Role = %q, want %q", got.Role, tc.message.Role)
			}

			// Verify content block count
			if len(got.Content) != len(tc.message.Content) {
				t.Fatalf("Content blocks = %d, want %d", len(got.Content), len(tc.message.Content))
			}

			// Verify each content block
			for i, wantBlock := range tc.message.Content {
				gotBlock := got.Content[i]
				if gotBlock.Type != wantBlock.Type {
					t.Errorf("block[%d].Type = %q, want %q", i, gotBlock.Type, wantBlock.Type)
				}
				if gotBlock.Text != wantBlock.Text {
					t.Errorf("block[%d].Text = %q, want %q", i, gotBlock.Text, wantBlock.Text)
				}
				if gotBlock.ID != wantBlock.ID {
					t.Errorf("block[%d].ID = %q, want %q", i, gotBlock.ID, wantBlock.ID)
				}
				if gotBlock.Name != wantBlock.Name {
					t.Errorf("block[%d].Name = %q, want %q", i, gotBlock.Name, wantBlock.Name)
				}
				if gotBlock.ToolUseID != wantBlock.ToolUseID {
					t.Errorf("block[%d].ToolUseID = %q, want %q", i, gotBlock.ToolUseID, wantBlock.ToolUseID)
				}
				if gotBlock.IsError != wantBlock.IsError {
					t.Errorf("block[%d].IsError = %v, want %v", i, gotBlock.IsError, wantBlock.IsError)
				}
				if gotBlock.Data != wantBlock.Data {
					t.Errorf("block[%d].Data = %q, want %q", i, gotBlock.Data, wantBlock.Data)
				}
				// Compare Input as raw JSON
				if string(gotBlock.Input) != string(wantBlock.Input) {
					t.Errorf("block[%d].Input = %q, want %q", i, string(gotBlock.Input), string(wantBlock.Input))
				}
				// Compare Content as raw JSON
				if string(gotBlock.Content) != string(wantBlock.Content) {
					t.Errorf("block[%d].Content = %q, want %q", i, string(gotBlock.Content), string(wantBlock.Content))
				}
			}

			// Verify timestamp
			if !got.Timestamp.Equal(tc.message.Timestamp) {
				t.Errorf("Timestamp = %v, want %v", got.Timestamp, tc.message.Timestamp)
			}
		})
	}
}

func TestMessageConvert_MultipleMessages(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)

	original := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: now},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: now},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("how are you?")}, Timestamp: now},
	}

	storeMsgs, err := EngineMessagesToStore(original)
	if err != nil {
		t.Fatalf("EngineMessagesToStore error: %v", err)
	}
	if len(storeMsgs) != 3 {
		t.Fatalf("expected 3 store messages, got %d", len(storeMsgs))
	}

	got, err := StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 engine messages, got %d", len(got))
	}

	// Verify roles preserved in order
	wantRoles := []types.Role{types.RoleUser, types.RoleAssistant, types.RoleUser}
	for i, want := range wantRoles {
		if got[i].Role != want {
			t.Errorf("msg[%d].Role = %q, want %q", i, got[i].Role, want)
		}
	}
}
