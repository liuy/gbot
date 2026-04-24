package agent

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestExtractPartialResult_LastAssistantWithText(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("I found the issue")}},
	}
	got := ExtractPartialResult(messages)
	if got != "I found the issue" {
		t.Errorf("expected %q, got %q", "I found the issue", got)
	}
}

func TestExtractPartialResult_MultipleAssistants(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("first")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("second")}},
	}
	got := ExtractPartialResult(messages)
	if got != "second" {
		t.Errorf("expected last assistant text %q, got %q", "second", got)
	}
}

func TestExtractPartialResult_OnlyToolUseSkipsToEarlier(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("earlier text"),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", nil),
		}},
	}
	got := ExtractPartialResult(messages)
	if got != "earlier text" {
		t.Errorf("expected %q (skipped tool_use-only assistant), got %q", "earlier text", got)
	}
}

func TestExtractPartialResult_EmptySlice(t *testing.T) {
	got := ExtractPartialResult(nil)
	if got != "" {
		t.Errorf("expected empty string for nil slice, got %q", got)
	}
	got = ExtractPartialResult([]types.Message{})
	if got != "" {
		t.Errorf("expected empty string for empty slice, got %q", got)
	}
}

func TestExtractPartialResult_AllNonAssistant(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("sys")}},
	}
	got := ExtractPartialResult(messages)
	if got != "" {
		t.Errorf("expected empty string (no assistant messages), got %q", got)
	}
}

func TestExtractPartialResult_EmptyTextBlockSkipped(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: ""},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("actual content"),
		}},
	}
	got := ExtractPartialResult(messages)
	if got != "actual content" {
		t.Errorf("expected %q (skipped empty text), got %q", "actual content", got)
	}
}

func TestExtractPartialResult_MultipleTextBlocksJoined(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("part one"),
			types.NewToolUseBlock("id1", "Read", nil),
			types.NewTextBlock("part two"),
		}},
	}
	got := ExtractPartialResult(messages)
	want := "part one\npart two"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}
