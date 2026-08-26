package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/types"
)

// editTurnMessages builds the persisted shape after an Edit: wire blocks in
// the tool_result content, rich output in the user message's ToolResultData.
// legacy=true omits the rich slot (pre-fix sessions).
func editTurnMessages(legacy bool) []types.Message {
	wire, err := json.Marshal([]types.ContentBlock{
		types.NewTextBlock("The file /tmp/sample.go has been updated successfully."),
	})
	if err != nil {
		panic(err)
	}
	assistant := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewToolUseBlock("toolu_e1", "Edit", json.RawMessage(`{"file_path":"/tmp/sample.go"}`)),
	}}
	user := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
		types.NewToolResultBlock("toolu_e1", wire, false),
	}}
	if !legacy {
		user.ToolResultData = map[string]any{"toolu_e1": &fileedit.Output{
			FilePath:  "/tmp/sample.go",
			OldString: "func old() {}",
			NewString: "func new() {}",
		}}
	}
	return []types.Message{assistant, user}
}

func findEditToolOutput(t *testing.T, msgs []types.Message) string {
	t.Helper()
	views := engineMessagesToViews(msgs, map[string]tool.Tool{"Edit": fileedit.New()})
	for i := len(views) - 1; i >= 0; i-- {
		for _, b := range views[i].Blocks {
			if b.Type == BlockTool {
				return b.ToolCall.Output
			}
		}
	}
	t.Fatal("no tool block in resumed views")
	return ""
}

// TestResume_EditRichToolResultRendersDiff pins the TUI replay contract: a
// session resumed with the two-slot tool_result message must render the diff,
// not the LLM-facing confirmation sentence stored in the wire content.
func TestResume_EditRichToolResultRendersDiff(t *testing.T) {
	got := findEditToolOutput(t, editTurnMessages(false))

	if strings.Contains(got, "has been updated successfully") {
		t.Errorf("resume rendered the LLM wire sentence %q — diff lost", got)
	}
	hasAdd, hasDel := false, false
	for line := range strings.SplitSeq(got, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, "+func new() {}") {
			hasAdd = true
		}
		if strings.HasSuffix(trimmed, "-func old() {}") {
			hasDel = true
		}
	}
	if !hasAdd || !hasDel {
		t.Errorf("resume output has no +/- diff lines (+:%v -:%v), got: %q", hasAdd, hasDel, got)
	}
}

// TestResume_EditLegacyWireFallsBack pins old-session compatibility: without
// the rich slot the wire content is shown unchanged (no migration, no
// re-derivation) — the documented degradation for pre-fix transcripts.
func TestResume_EditLegacyWireFallsBack(t *testing.T) {
	got := findEditToolOutput(t, editTurnMessages(true))
	if got != "The file /tmp/sample.go has been updated successfully." {
		t.Errorf("legacy replay = %q, want the stored wire sentence verbatim", got)
	}
}

// TestResume_EditUnmarshalableRichFallsBack pins the degradation path for a
// rich slot that cannot be re-wrapped: replay falls back to the wire content.
func TestResume_EditUnmarshalableRichFallsBack(t *testing.T) {
	msgs := editTurnMessages(false)
	msgs[1].ToolResultData["toolu_e1"] = make(chan int) // json.Marshal fails
	got := findEditToolOutput(t, msgs)
	if got != "The file /tmp/sample.go has been updated successfully." {
		t.Errorf("fallback resume = %q, want the stored wire sentence", got)
	}
}

// TestResume_EditMissingToolFallsBackToWire mirrors the wui guard test: a
// rich slot exists but the tool is unresolvable (e.g. MCP server
// disconnected) — replay shows the readable wire sentence, not a raw JSON
// blob from the undecodable rich slot.
func TestResume_EditMissingToolFallsBackToWire(t *testing.T) {
	msgs := editTurnMessages(false)
	views := engineMessagesToViews(msgs, map[string]tool.Tool{})
	for i := len(views) - 1; i >= 0; i-- {
		for _, b := range views[i].Blocks {
			if b.Type == BlockTool {
				got := b.ToolCall.Output
				if strings.Contains(got, `"filePath"`) || strings.Contains(got, `"oldString"`) {
					t.Errorf("missing-tool resume = raw JSON, want wire sentence: %q", got)
				}
				if got != "The file /tmp/sample.go has been updated successfully." {
					t.Errorf("missing-tool resume = %q, want the stored wire sentence", got)
				}
				return
			}
		}
	}
	t.Fatal("no tool block in resumed views")
}
