package agent

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// CountToolUses
// ---------------------------------------------------------------------------

func TestCountToolUses_EmptySlice(t *testing.T) {
	if got, want := CountToolUses(nil), 0; got != want {
		t.Fatalf("nil: got %d, want %d", got, want)
	}
	if got, want := CountToolUses([]types.Message{}), 0; got != want {
		t.Fatalf("empty: got %d, want %d", got, want)
	}
}

func TestCountToolUses_OnlyUserMessages(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("there")}},
	}
	got := CountToolUses(messages)
	if got, want := got, 0; got != want {
		t.Fatalf("got %d, want %d (no assistant messages)", got, want)
	}
}

func TestCountToolUses_MultipleAssistants(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("1", "Read", nil),
			types.NewToolUseBlock("2", "Grep", nil),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("3", "Bash", nil),
		}},
	}
	if got := CountToolUses(messages); got != 3 {
		t.Errorf("expected 3 tool_use blocks, got %d", got)
	}
}

func TestCountToolUses_MixedTextAndToolUse(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("let me check"),
			types.NewToolUseBlock("1", "Read", nil),
			types.NewTextBlock("now searching"),
			types.NewToolUseBlock("2", "Grep", nil),
		}},
	}
	if got := CountToolUses(messages); got != 2 {
		t.Errorf("expected 2 (text blocks ignored), got %d", got)
	}
}

func TestCountToolUses_AssistantWithNoToolUse(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("just text, no tools"),
		}},
	}
	if got := CountToolUses(messages); got != 0 {
		t.Errorf("expected 0 (no tool_use), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// GetLastToolUseName
// ---------------------------------------------------------------------------

func TestGetLastToolUseName_NonAssistant(t *testing.T) {
	msg := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}
	if got := GetLastToolUseName(msg); got != "" {
		t.Errorf("expected empty string for non-assistant, got %q", got)
	}
}

func TestGetLastToolUseName_AssistantNoToolUse(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewTextBlock("just text"),
	}}
	if got := GetLastToolUseName(msg); got != "" {
		t.Errorf("expected empty string (no tool_use), got %q", got)
	}
}

func TestGetLastToolUseName_MultipleToolUses(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewToolUseBlock("1", "Read", nil),
		types.NewTextBlock("checking"),
		types.NewToolUseBlock("2", "Grep", nil),
	}}
	got := GetLastToolUseName(msg)
	if got != "Grep" {
		t.Errorf("expected last tool_use name %q, got %q", "Grep", got)
	}
}

func TestGetLastToolUseName_TextAndToolUse(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewTextBlock("let me read"),
		types.NewToolUseBlock("1", "Read", nil),
	}}
	got := GetLastToolUseName(msg)
	if got != "Read" {
		t.Errorf("expected %q, got %q", "Read", got)
	}
}

func TestGetLastToolUseName_EmptyContent(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{}}
	if got := GetLastToolUseName(msg); got != "" {
		t.Errorf("expected empty string for empty content, got %q", got)
	}
}

func TestGetLastToolUseName_ToolUseAtStart(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewToolUseBlock("1", "Bash", nil),
		types.NewTextBlock("done"),
	}}
	got := GetLastToolUseName(msg)
	if got != "Bash" {
		t.Errorf("expected %q (only tool_use, found via backward walk), got %q", "Bash", got)
	}
}
