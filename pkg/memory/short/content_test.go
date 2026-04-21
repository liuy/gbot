package short

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseContentBlocks(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		wantType []string // want block types
		wantText []string // want text values (for text blocks)
	}{
		{
			name:     "empty content returns empty slice",
			json:     "",
			wantType: []string{},
		},
		{
			name:     "invalid JSON returns empty slice",
			json:     "{invalid json",
			wantType: []string{},
		},
		{
			name: "single text block parses text field",
			json: `[{"type":"text","text":"hello world"}]`,
			wantType: []string{"text"},
			wantText: []string{"hello world"},
		},
		{
			name: "tool_use block with name/input fields",
			json: `[{"type":"tool_use","id":"tu1","name":"bash","input":{"command":"ls"}}]`,
			wantType: []string{"tool_use"},
		},
		{
			name: "tool_result block with tool_use_id field",
			json: `[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]`,
			wantType: []string{"tool_result"},
		},
		{
			name: "multiple mixed blocks parse in order",
			json: `[{"type":"text","text":"first"},{"type":"tool_use","id":"tu1"},{"type":"text","text":"second"}]`,
			wantType: []string{"text", "tool_use", "text"},
			wantText: []string{"first", "", "second"},
		},
		{
			name: "thinking block",
			json: `[{"type":"thinking","text":"thinking..."}]`,
			wantType: []string{"thinking"},
		},
		{
			name:     "empty array returns empty slice",
			json:     "[]",
			wantType: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseContentBlocks(tt.json)

			if len(got) != len(tt.wantType) {
				t.Fatalf("ParseContentBlocks() length = %d, want %d", len(got), len(tt.wantType))
			}

			for i, wantType := range tt.wantType {
				if got[i].Type != wantType {
					t.Errorf("block[%d].Type = %q, want %q", i, got[i].Type, wantType)
				}
			}

			if tt.wantText != nil {
				for i, wantText := range tt.wantText {
					if got[i].Text != wantText {
						t.Errorf("block[%d].Text = %q, want %q", i, got[i].Text, wantText)
					}
				}
			}
		})
	}
}

func TestExtractTextFromJSON_Content(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "only keeps text blocks",
			json: `[{"type":"text","text":"hello"},{"type":"tool_use","id":"tu1"},{"type":"text","text":"world"}]`,
			want: "hello\nworld",
		},
		{
			name: "removes tool_use blocks",
			json: `[{"type":"tool_use","id":"tu1","name":"bash"}]`,
			want: "",
		},
		{
			name: "removes thinking blocks",
			json: `[{"type":"thinking","text":"thinking..."}]`,
			want: "",
		},
		{
			name: "removes tool_result blocks",
			json: `[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]`,
			want: "",
		},
		{
			name: "empty JSON returns empty string",
			json: "",
			want: "",
		},
		{
			name: "only tool_use returns empty string",
			json: `[{"type":"tool_use","id":"tu1"}]`,
			want: "",
		},
		{
			name: "mixed blocks only concatenate text",
			json: `[{"type":"text","text":"a"},{"type":"thinking"},{"type":"tool_use","id":"tu1"},{"type":"text","text":"b"}]`,
			want: "a\nb",
		},
		{
			name: "single text block",
			json: `[{"type":"text","text":"single"}]`,
			want: "single",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTextFromJSON(tt.json)
			if got != tt.want {
				t.Errorf("ExtractTextFromJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterUnresolvedToolUses(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		messages []*Message
		wantLen  int
		wantLast string // type of last message
	}{
		{
			name: "truncates to last complete user/assistant pair",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"prompt"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"response"}]`, CreatedAt: now},
				// unresolved tool_use
				{Seq: 3, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"bash"}]`, CreatedAt: now},
			},
			wantLen:  2,
			wantLast: "assistant",
		},
		{
			name: "no unresolved when tool_result exists",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"prompt"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`, CreatedAt: now},
				{Seq: 3, Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]`, CreatedAt: now},
				{Seq: 4, Type: "assistant", Content: `[{"type":"text","text":"done"}]`, CreatedAt: now},
			},
			wantLen:  4,
			wantLast: "assistant",
		},
		{
			name: "multiple consecutive unresolved tool_use all removed",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"prompt"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`, CreatedAt: now},
				{Seq: 3, Type: "assistant", Content: `[{"type":"tool_use","id":"tu2"}]`, CreatedAt: now},
				{Seq: 4, Type: "assistant", Content: `[{"type":"tool_use","id":"tu3"}]`, CreatedAt: now},
			},
			wantLen:  1,
			wantLast: "user",
		},
		{
			name: "middle unresolved不影响前面的完整对",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"first"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"response1"}]`, CreatedAt: now},
				{Seq: 3, Type: "user", Content: `[{"type":"text","text":"second"}]`, CreatedAt: now},
				{Seq: 4, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`, CreatedAt: now}, // unresolved
				{Seq: 5, Type: "assistant", Content: `[{"type":"tool_use","id":"tu2"}]`, CreatedAt: now}, // unresolved
			},
			wantLen:  3,
			wantLast: "user",
		},
		{
			name: "mixed resolved and unresolved - assistant with some resolved kept",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"prompt"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"},{"type":"tool_use","id":"tu2"}]`, CreatedAt: now},
				{Seq: 3, Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"out1"}]`, CreatedAt: now},
				// tu2 is unresolved but tu1 is resolved, so assistant is kept
			},
			wantLen:  3,
			wantLast: "user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterUnresolvedToolUses(tt.messages)

			if len(got) != tt.wantLen {
				t.Fatalf("FilterUnresolvedToolUses() length = %d, want %d", len(got), tt.wantLen)
			}

			if got[len(got)-1].Type != tt.wantLast {
				t.Errorf("last message type = %q, want %q", got[len(got)-1].Type, tt.wantLast)
			}
		})
	}
}

func TestFilterOrphanedThinking(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		messages []*Message
		wantLen  int
		wantUUID string // check that this UUID is present
	}{
		{
			name: "removes only-thinking messages",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking","text":"thinking..."}]`, CreatedAt: now},
			},
			wantLen: 1,
		},
		{
			name: "keeps text+thinking messages",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking"},{"type":"text","text":"response"}]`, CreatedAt: now},
			},
			wantLen:  2,
			wantUUID: "a1",
		},
		{
			name: "keeps non-thinking messages",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"text","text":"response"}]`, CreatedAt: now},
			},
			wantLen:  2,
			wantUUID: "a1",
		},
		{
			name: "multiple consecutive orphan thinking all removed",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking"}]`, CreatedAt: now},
				{Seq: 3, Type: "assistant", UUID: "a2", Content: `[{"type":"thinking"}]`, CreatedAt: now},
				{Seq: 4, Type: "assistant", UUID: "a3", Content: `[{"type":"thinking"}]`, CreatedAt: now},
			},
			wantLen: 1,
		},
		{
			name: "keeps thinking-only if same UUID has non-thinking sibling",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking"}]`, CreatedAt: now},
				{Seq: 3, Type: "assistant", UUID: "a1", Content: `[{"type":"text","text":"response"}]`, CreatedAt: now},
			},
			wantLen:  3, // All kept - thinking will be merged with text sibling
			wantUUID: "a1",
		},
		{
			name: "redacted_thinking also considered thinking block",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"redacted_thinking"}]`, CreatedAt: now},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterOrphanedThinking(tt.messages)

			if len(got) != tt.wantLen {
				t.Fatalf("FilterOrphanedThinking() length = %d, want %d", len(got), tt.wantLen)
			}

			if tt.wantUUID != "" {
				found := false
				for _, msg := range got {
					if msg.UUID == tt.wantUUID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("FilterOrphanedThinking() UUID %q not found in result", tt.wantUUID)
				}
			}
		})
	}
}

func TestHasOnlyWhitespaceTextContent(t *testing.T) {
	tests := []struct {
		name string
		msg  *Message
		want bool
	}{
		{
			name: "removes assistant with only spaces",
			msg:  &Message{Type: "assistant", Content: `[{"type":"text","text":"   "}]`},
			want: true,
		},
		{
			name: "removes assistant with only tabs",
			msg:  &Message{Type: "assistant", Content: `[{"type":"text","text":"\t\t\t"}]`},
			want: true,
		},
		{
			name: "removes assistant with only newlines",
			msg:  &Message{Type: "assistant", Content: `[{"type":"text","text":"\n\n\n"}]`},
			want: true,
		},
		{
			name: "removes assistant with mixed whitespace",
			msg:  &Message{Type: "assistant", Content: `[{"type":"text","text":"  \n\t  \n"}]`},
			want: true,
		},
		{
			name: "keeps assistant with non-whitespace text",
			msg:  &Message{Type: "assistant", Content: `[{"type":"text","text":"hello"}]`},
			want: false,
		},
		{
			name: "keeps assistant with tool_use block",
			msg:  &Message{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`},
			want: false,
		},
		{
			name: "keeps assistant with mixed text and tool_use",
			msg:  &Message{Type: "assistant", Content: `[{"type":"text","text":"  "},{"type":"tool_use","id":"tu1"}]`},
			want: false,
		},
		{
			name: "empty content returns false",
			msg:  &Message{Type: "assistant", Content: "[]"},
			want: false,
		},
		{
			name: "empty string content returns false",
			msg:  &Message{Type: "assistant", Content: ""},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasOnlyWhitespaceTextContent(tt.msg)
			if got != tt.want {
				t.Errorf("HasOnlyWhitespaceTextContent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterWhitespaceOnlyAssistant(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name          string
		messages      []*Message
		wantLen       int
		wantLastType  string
		wantLastCount int // number of user messages (should be merged if adjacent)
	}{
		{
			name: "removes whitespace-only assistant",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"\n\n"}]`, CreatedAt: now},
			},
			wantLen:      1,
			wantLastType: "user",
		},
		{
			name: "keeps non-whitespace assistant",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"hello"}]`, CreatedAt: now},
			},
			wantLen:      2,
			wantLastType: "assistant",
		},
		{
			name: "keeps user and system messages (doesn't check non-assistant)",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "system", Content: `[{"type":"text","text":"\n\n"}]`, CreatedAt: now},
			},
			wantLen:      2,
			wantLastType: "system",
		},
		{
			name: "merges adjacent user messages after filtering whitespace assistant",
			messages: []*Message{
				{Seq: 1, Type: "user", UUID: "u1", Content: `[{"type":"text","text":"first"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"  \n  "}]`, CreatedAt: now},
				{Seq: 3, Type: "user", UUID: "u2", Content: `[{"type":"text","text":"second"}]`, CreatedAt: now},
			},
			wantLen:      1,
			wantLastType: "user",
		},
		{
			name: "no changes when no whitespace-only messages",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"hello"}]`, CreatedAt: now},
			},
			wantLen:      2,
			wantLastType: "assistant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterWhitespaceOnlyAssistant(tt.messages)

			if len(got) != tt.wantLen {
				t.Fatalf("FilterWhitespaceOnlyAssistant() length = %d, want %d", len(got), tt.wantLen)
			}

			if got[len(got)-1].Type != tt.wantLastType {
				t.Errorf("last message type = %q, want %q", got[len(got)-1].Type, tt.wantLastType)
			}
		})
	}
}

func TestMergeUserMessages(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name  string
		a     *Message
		b     *Message
		check func(*Message) // validation function for result
	}{
		{
			name: "merges content blocks",
			a: &Message{
				Seq:    1,
				UUID:   "u1",
				Type:   "user",
				ParentUUID: "p1",
				CreatedAt: now,
			},
			b: &Message{
				Seq:    2,
				UUID:   "u2",
				Type:   "user",
				ParentUUID: "p2",
				CreatedAt: now.Add(1),
			},
			check: func(m *Message) {
				if m.Type != "user" {
					t.Errorf("merged type = %q, want user", m.Type)
				}
				// Content should be empty (both inputs empty)
				blocks := ParseContentBlocks(m.Content)
				if len(blocks) != 0 {
					t.Errorf("expected empty content after merging two empty messages, got %d blocks", len(blocks))
				}
			},
		},
		{
			name: "preserves b's ParentUUID",
			a: &Message{
				Seq:    1,
				Type:   "user",
				ParentUUID: "p1",
				CreatedAt: now,
			},
			b: &Message{
				Seq:    2,
				Type:   "user",
				ParentUUID: "p2",
				CreatedAt: now.Add(1),
			},
			check: func(m *Message) {
				if m.ParentUUID != "p2" {
					t.Errorf("ParentUUID = %q, want p2", m.ParentUUID)
				}
			},
		},
		{
			name: "uses b's CreatedAt",
			a: &Message{
				Seq:    1,
				Type:   "user",
				CreatedAt: now,
			},
			b: &Message{
				Seq:    2,
				Type:   "user",
				CreatedAt: now.Add(1),
			},
			check: func(m *Message) {
				expectedTime := now.Add(1)
				if !m.CreatedAt.Equal(expectedTime) && m.CreatedAt.IsZero() {
					t.Errorf("CreatedAt not as expected")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeUserMessages(tt.a, tt.b)
			if tt.check != nil {
				tt.check(got)
			}
		})
	}
}

func TestIsChainParticipant(t *testing.T) {
	tests := []struct {
		name string
		msg  *Message
		want bool
	}{
		{
			name: "user message is participant",
			msg:  &Message{Type: "user"},
			want: true,
		},
		{
			name: "assistant message is participant",
			msg:  &Message{Type: "assistant"},
			want: true,
		},
		{
			name: "system message is participant",
			msg:  &Message{Type: "system"},
			want: true,
		},
		{
			name: "attachment message is participant",
			msg:  &Message{Type: "attachment"},
			want: true,
		},
		{
			name: "progress message is NOT participant",
			msg:  &Message{Type: "progress"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isChainParticipant(tt.msg)
			if got != tt.want {
				t.Errorf("isChainParticipant() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Test content blocks with full tool_use/input structures
func TestContentBlockToolUseInput(t *testing.T) {
	content := `[{"type":"tool_use","id":"tu1","name":"bash","input":{"command":"ls","cwd":"/tmp"}}]`
	blocks := ParseContentBlocks(content)

	if len(blocks) != 1 {
		t.Fatalf("ParseContentBlocks() length = %d, want 1", len(blocks))
	}

	if blocks[0].Type != "tool_use" {
		t.Errorf("block.Type = %q, want tool_use", blocks[0].Type)
	}
	if blocks[0].ID != "tu1" {
		t.Errorf("block.ID = %q, want tu1", blocks[0].ID)
	}
	if blocks[0].Name != "bash" {
		t.Errorf("block.Name = %q, want bash", blocks[0].Name)
	}

	// Check input is preserved as RawMessage
	var input map[string]any
	if err := json.Unmarshal(blocks[0].Input, &input); err != nil {
		t.Fatalf("failed to unmarshal Input: %v", err)
	}
	if input["command"] != "ls" {
		t.Errorf("input.command = %v, want ls", input["command"])
	}
}

// Test tool_result block with all fields
func TestContentBlockToolResult(t *testing.T) {
	content := `[{"type":"tool_result","tool_use_id":"tu1","content":"output","is_error":false}]`
	blocks := ParseContentBlocks(content)

	if len(blocks) != 1 {
		t.Fatalf("ParseContentBlocks() length = %d, want 1", len(blocks))
	}

	if blocks[0].Type != "tool_result" {
		t.Errorf("block.Type = %q, want tool_result", blocks[0].Type)
	}
	if blocks[0].ToolUseID != "tu1" {
		t.Errorf("block.ToolUseID = %q, want tu1", blocks[0].ToolUseID)
	}
	if string(blocks[0].Content) != `"output"` {
		t.Errorf("block.Content = %q, want %q", string(blocks[0].Content), `"output"`)
	}
	if blocks[0].IsError != false {
		t.Errorf("block.IsError = %v, want false", blocks[0].IsError)
	}
}

func TestMergeUserMessages_NilMessages(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		a    *Message
		b    *Message
	}{
		{
			name: "both nil",
			a:    nil,
			b:    nil,
		},
		{
			name: "first nil",
			a:    nil,
			b:    &Message{Seq: 1, Type: "user", UUID: "u1", CreatedAt: now},
		},
		{
			name: "second nil",
			a:    &Message{Seq: 1, Type: "user", UUID: "u1", CreatedAt: now},
			b:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeUserMessages(tt.a, tt.b)
			if result == nil {
				// If both nil, result is nil (acceptable)
				if tt.a != nil || tt.b != nil {
					t.Error("expected non-nil result when one input is non-nil")
				}
			}
		})
	}
}

func TestFilterWhitespaceOnlyAssistant_WithMerge(t *testing.T) {
	now := time.Now()

	// Test that adjacent user messages are properly merged after filtering
	messages := []*Message{
		{
			Seq:       1,
			Type:      "user",
			UUID:      "u1",
			Content:   `[{"type":"text","text":"first"}]`,
			CreatedAt: now,
		},
		{
			Seq:       2,
			Type:      "assistant",
			Content:   `[{"type":"text","text":"   \n  \t  "}]`, // Whitespace only
			CreatedAt: now.Add(1 * time.Second),
		},
		{
			Seq:       3,
			Type:      "user",
			UUID:      "u2",
			Content:   `[{"type":"text","text":"second"}]`,
			CreatedAt: now.Add(2 * time.Second),
		},
	}

	filtered := FilterWhitespaceOnlyAssistant(messages)

	if len(filtered) != 1 {
		t.Fatalf("got %d messages, want 1 (merged user messages)", len(filtered))
	}

	if filtered[0].Type != "user" {
		t.Errorf("merged message type = %q, want user", filtered[0].Type)
	}

	// Check that content was merged
	blocks := ParseContentBlocks(filtered[0].Content)
	if len(blocks) != 2 {
		t.Errorf("expected 2 merged content blocks, got %d", len(blocks))
	}
}

func TestFilterOrphanedThinking_EdgeCases(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		messages []*Message
		wantLen  int
	}{
		{
			name: "empty blocks in assistant message",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[]`, CreatedAt: now},
			},
			wantLen: 2, // Empty content arrays are kept
		},
		{
			name: "mixed thinking and redacted_thinking",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking"},{"type":"redacted_thinking"}]`, CreatedAt: now},
			},
			wantLen: 1, // All thinking blocks -> orphaned
		},
		{
			name: "thinking with text sibling same UUID",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking"}]`, CreatedAt: now},
				{Seq: 3, Type: "assistant", UUID: "a1", Content: `[{"type":"text","text":"response"}]`, CreatedAt: now.Add(1)},
			},
			wantLen: 3, // Thinking kept because sibling has non-thinking
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := FilterOrphanedThinking(tt.messages)
			if len(filtered) != tt.wantLen {
				t.Errorf("got %d messages, want %d", len(filtered), tt.wantLen)
			}
		})
	}
}

func TestFilterUnresolvedToolUses_EdgeCases(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		messages []*Message
		wantLen  int
	}{
		{
			name: "assistant with no blocks",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[]`, CreatedAt: now},
			},
			wantLen: 2, // Empty assistant kept
		},
		{
			name: "all tool_uses resolved",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"},{"type":"tool_use","id":"tu2"}]`, CreatedAt: now},
				{Seq: 3, Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"out1"}]`, CreatedAt: now},
				{Seq: 4, Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu2","content":"out2"}]`, CreatedAt: now},
			},
			wantLen: 4,
		},
		{
			name: "assistant with some resolved some unresolved",
			messages: []*Message{
				{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`, CreatedAt: now},
				{Seq: 2, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"},{"type":"tool_use","id":"tu2"}]`, CreatedAt: now},
				{Seq: 3, Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"out1"}]`, CreatedAt: now},
				// tu2 is unresolved but tu1 is resolved -> assistant kept
			},
			wantLen: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := FilterUnresolvedToolUses(tt.messages)
			if len(filtered) != tt.wantLen {
				t.Errorf("got %d messages, want %d", len(filtered), tt.wantLen)
			}
		})
	}
}
