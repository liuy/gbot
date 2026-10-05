package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// Persisted mid-turn queued messages reload from history carrying the reminder
// envelope. Replay must unwrap to the original text and render as a gray echo
// block (BlockUser) — the same visual the live drain produced — so restart
// output is indistinguishable from the live stream.
func TestResume_QueuedMessageUnwrappedToGrayBlock(t *testing.T) {
	envelope := "<system-reminder>\nThe user sent a new message while you were working:\nremember the tests" +
		"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>"
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("run the build")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(envelope)}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "toolu_1", Name: "Bash", Input: json.RawMessage(`{"command":"make"}`)},
			{Type: types.ContentTypeToolResult, ToolUseID: "toolu_1", Content: json.RawMessage(`[{"type":"text","text":"ok"}]`)},
		}},
	}

	views := engineMessagesToViews(msgs, nil)
	var found *ContentBlock
	for i := range views {
		if views[i].Role != "user" {
			continue
		}
		for j := range views[i].Blocks {
			if views[i].Blocks[j].Type == BlockUser {
				found = &views[i].Blocks[j]
			}
		}
	}
	if found == nil {
		t.Fatal("no BlockUser in replayed views — queued message not unwrapped")
	}
	if found.Text != "remember the tests" {
		t.Errorf("BlockUser text = %q, want unwrapped %q", found.Text, "remember the tests")
	}
	if strings.Contains(found.Text, "<system-reminder>") {
		t.Errorf("BlockUser text must not leak the envelope: %q", found.Text)
	}

	// Rendering shows the ❯-prefixed original text (dim gray), not the envelope.
	forceColorProfile(t)
	rendered := renderMessagesFull(views, 80, false, "", false, false, 0)
	if !strings.Contains(rendered, "❯ \x1b[38;5;246mremember the tests") {
		t.Errorf("rendered view missing gray '❯ remember the tests', got:\n%q", rendered)
	}
	if strings.Contains(rendered, "<system-reminder>") {
		t.Errorf("rendered view must not show the raw envelope, got:\n%s", rendered)
	}
}
