package wui

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/types"
)

// executeRealEdit runs a production Edit call so the result shape is real.
func executeRealEdit(t *testing.T) (ed tool.Tool, res *tool.ToolResult, fp string) {
	t.Helper()
	dir := t.TempDir()
	fp = filepath.Join(dir, "sample.go")
	if err := os.WriteFile(fp, []byte("package main\n\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed = fileedit.New()
	input, err := json.Marshal(map[string]string{
		"file_path": fp, "old_string": "func old() {}", "new_string": "func new() {}",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err = fileedit.Execute(context.Background(), input, &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return ed, res, fp
}

// persistedToolTurn builds the message pair exactly as the fixed engine
// persists it after an Edit: the user message carries the wire blocks in
// content (LLM view) AND the rich result in ToolResultData (replay view).
func persistedToolTurn(t *testing.T, ed tool.Tool, res *tool.ToolResult, fp string) (types.Message, types.Message) {
	t.Helper()
	wb := ed.(tool.ToolWithWireBlocks)
	wireJSON, err := json.Marshal(wb.FormatWireBlocks(res.Data))
	if err != nil {
		t.Fatal(err)
	}
	toolUseID := "toolu_edit_1"
	assistant := types.NewAssistantMessage([]types.ContentBlock{
		types.NewToolUseBlock(toolUseID, "Edit", json.RawMessage(`{"file_path":`+mustJSON(t, fp)+`}`)),
	})
	user := types.NewUserMessage([]types.ContentBlock{
		types.NewToolResultBlock(toolUseID, wireJSON, false),
	})
	user.ToolResultData = map[string]any{toolUseID: res.Data}
	return assistant, user
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// replayToolOutput mirrors buildHistory's two-pass shape: resolve results by
// tool_use_id across the message list, then render the assistant turn.
func replayToolOutput(t *testing.T, c *WUIConnector, msgs []types.Message, tools map[string]tool.Tool) string {
	t.Helper()
	toolResults := make(map[string]types.ContentBlock)
	richResults := make(map[string]any)
	for _, m := range msgs {
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID != "" {
				toolResults[cb.ToolUseID] = cb
			}
		}
		maps.Copy(richResults, m.ToolResultData)
	}
	for _, m := range msgs {
		if m.Role != types.RoleAssistant {
			continue
		}
		hm := c.buildHistoryChatMsg(m, tools, toolResults, richResults)
		if len(hm.Tools) == 0 {
			t.Fatal("replayed assistant turn has no tool entry")
		}
		return hm.Tools[0].DisplayOutput
	}
	t.Fatal("no assistant message in replay input")
	return ""
}

// assertVisibleDiff holds the three replay assertions shared by both tests.
func assertVisibleDiff(t *testing.T, got string) {
	t.Helper()
	if strings.Contains(got, "has been updated successfully") {
		t.Errorf("replay rendered the LLM wire sentence %q — diff lost", got)
	}
	if !strings.Contains(got, "func new() {}") || !strings.Contains(got, "func old() {}") {
		t.Errorf("replay output lacks old/new content, got: %q", got)
	}
	// Diff form: line-numbered +/- lines (matches RenderDiff/RenderToolOutput shape).
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
		t.Errorf("replay output has no +/- diff lines (+:%v -:%v), got: %q", hasAdd, hasDel, got)
	}
}

// TestEditReplay_PreservesDiff pins the history-replay contract for Edit:
// whatever tool_result shape persistence stores must render back into a
// visible diff after refresh — not the LLM-facing confirmation sentence.
//
// Regression: e08363c (wire-plaintext) applied FormatWireBlocks at execution
// time, so the persisted tool_result became "The file X has been updated
// successfully." Replay then showed that sentence — the diff silently
// disappeared. Fix (TS parity, toolExecution.ts:1456-1466): the user message
// stores BOTH slots — wire blocks in content (for the API) and the rich
// result in ToolResultData (for replay). This test exercises the fixed
// two-slot message through the real replay renderer.
func TestEditReplay_PreservesDiff(t *testing.T) {
	ed, res, fp := executeRealEdit(t)
	assistant, user := persistedToolTurn(t, ed, res, fp)

	tools := map[string]tool.Tool{"Edit": ed}
	c := &WUIConnector{}
	got := replayToolOutput(t, c, []types.Message{assistant, user}, tools)

	assertVisibleDiff(t, got)
}

// TestEditReplay_UnmarshalableRichFallsBack pins the degradation path: if
// the rich slot cannot be re-wrapped for DecodeResult, replay must fall back
// to the wire content rather than rendering nothing.
func TestEditReplay_UnmarshalableRichFallsBack(t *testing.T) {
	ed, res, fp := executeRealEdit(t)
	assistant, user := persistedToolTurn(t, ed, res, fp)
	user.ToolResultData["toolu_edit_1"] = make(chan int) // json.Marshal fails

	tools := map[string]tool.Tool{"Edit": ed}
	got := replayToolOutput(t, &WUIConnector{}, []types.Message{assistant, user}, tools)

	if got != "The file "+fp+" has been updated successfully." {
		t.Errorf("fallback replay = %q, want the stored wire sentence", got)
	}
}

// TestEditReplay_FullRoundTrip runs the complete chain the bug lived in:
// execution → engine-shaped user message → EngineMessagesToStore →
// StoreMessagesToEngine (SQLite metadata round-trip) → replay rendering.
// The rich data must survive the storage round-trip as JSON and still render
// the diff (the tool recovers its concrete type via DecodeResult).
func TestEditReplay_FullRoundTrip(t *testing.T) {
	ed, res, fp := executeRealEdit(t)
	assistant, user := persistedToolTurn(t, ed, res, fp)

	storeMsgs, err := short.EngineMessagesToStore([]types.Message{assistant, user})
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	loaded, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("round-trip changed message count: %d", len(loaded))
	}
	// The rich slot must be re-read as a JSON-decoded map, not dropped.
	if len(loaded[1].ToolResultData) != 1 {
		t.Fatalf("ToolResultData lost in storage round-trip: %+v", loaded[1].ToolResultData)
	}

	tools := map[string]tool.Tool{"Edit": ed}
	c := &WUIConnector{}
	got := replayToolOutput(t, c, loaded, tools)

	assertVisibleDiff(t, got)
}

// TestEditReplay_MissingToolFallsBackToWire pins the degradation guard: a
// rich slot exists but the tool is not resolvable (e.g. MCP server
// disconnected, tool unregistered) — replay must show the readable wire
// content, not a raw JSON blob from the undecodable rich slot.
func TestEditReplay_MissingToolFallsBackToWire(t *testing.T) {
	ed, res, fp := executeRealEdit(t)
	assistant, user := persistedToolTurn(t, ed, res, fp)

	// Empty tools map: tool unresolvable, rich path must not run.
	tools := map[string]tool.Tool{}
	c := &WUIConnector{}
	got := replayToolOutput(t, c, []types.Message{assistant, user}, tools)

	if strings.Contains(got, `"filePath"`) || strings.Contains(got, `"oldString"`) {
		t.Errorf("replay rendered raw rich JSON, want readable wire content, got: %q", got)
	}
	if !strings.Contains(got, "has been updated successfully") {
		t.Errorf("replay without the tool should show the wire sentence, got: %q", got)
	}
}
