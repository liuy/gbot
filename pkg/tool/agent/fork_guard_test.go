package agent

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestIsInForkChild_NoMarker(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	if IsInForkChild(messages) {
		t.Error("should return false when no fork-boilerplate marker present")
	}
}

func TestIsInForkChild_WithMarker(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("<fork-boilerplate>\nSTOP. READ THIS FIRST.\n</fork-boilerplate>\ndo it")}},
	}
	if !IsInForkChild(messages) {
		t.Error("should return true when fork-boilerplate marker is present in user message")
	}
}

func TestIsInForkChild_MarkerInToolResult(t *testing.T) {
	t.Parallel()
	// Marker in tool_result block (not text) — should NOT be detected
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"<fork-boilerplate>some output</fork-boilerplate>"`), false),
		}},
	}
	if IsInForkChild(messages) {
		t.Error("should return false — marker in tool_result block, not text block")
	}
}

func TestIsInForkChild_MarkerInAssistantMessage(t *testing.T) {
	t.Parallel()
	// Marker in assistant message — should NOT be detected (only checks user messages)
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("<fork-boilerplate>ignore this</fork-boilerplate>")}},
	}
	if IsInForkChild(messages) {
		t.Error("should return false — marker in assistant message, not user message")
	}
}

func TestIsInForkChild_EmptyMessages(t *testing.T) {
	t.Parallel()
	if IsInForkChild(nil) {
		t.Error("should return false for nil messages")
	}
	if IsInForkChild([]types.Message{}) {
		t.Error("should return false for empty messages")
	}
}
