package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/types"
)

// mockWireTool wraps mockTool with a FormatWireBlocks override so the
// executor treats it like Edit/Write/MCP: wire view diverges from rich data.
type mockWireTool struct {
	*mockTool
}

func (m *mockWireTool) FormatWireBlocks(data any) []types.ContentBlock {
	return []types.ContentBlock{types.NewTextBlock("wire summary")}
}

// TestExecuteAll_ToolResultData_WireToolsOnly pins the collection rule: the
// rich-data slot is populated only for successful, synchronous results of
// tools whose wire view is a lossy summary (Edit/Write/mcp__*). Bash & other
// wire-plaintext tools keep their complete output as wire content already;
// error and async-background results have no rich view to preserve.
func TestExecuteAll_ToolResultData_WireToolsOnly(t *testing.T) {
	t.Parallel()

	okEdit := &mockWireTool{&mockTool{name: "Edit", enabled: true}}
	okBash := &mockWireTool{&mockTool{name: "Bash", enabled: true}}

	tools := map[string]tool.Tool{
		"Edit": okEdit, "Bash": okBash,
	}
	blocks := []types.ContentBlock{
		types.NewToolUseBlock("id_edit", "Edit", json.RawMessage(`{}`)),
		types.NewToolUseBlock("id_bash", "Bash", json.RawMessage(`{}`)),
	}

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(types.QueryEvent) {})

	if got, want := len(result.ToolResultBlocks), 2; got != want {
		t.Fatalf("ToolResultBlocks = %d, want %d", got, want)
	}
	rich := result.ToolResultData
	if len(rich) != 1 {
		t.Fatalf("ToolResultData = %v, want exactly the Edit entry", rich)
	}
	if data, ok := rich["id_edit"]; !ok || data != "ok" {
		t.Errorf("ToolResultData[id_edit] = %v (%T), want mockTool default Data \"ok\"", data, data)
	}
	if _, ok := rich["id_bash"]; ok {
		t.Error("Bash result must not enter ToolResultData (wire content already stores full output)")
	}

	// Write is slotted too (its wire view is a confirmation sentence).
	toolsWrite := map[string]tool.Tool{"Write": &mockWireTool{&mockTool{name: "Write", enabled: true}}}
	blocksWrite := []types.ContentBlock{types.NewToolUseBlock("id_write", "Write", json.RawMessage(`{}`))}
	resultWrite := ConcurrentToolLoop(context.Background(), toolsWrite, blocksWrite, nil, func(types.QueryEvent) {})
	if data, ok := resultWrite.ToolResultData["id_write"]; !ok || data != "ok" {
		t.Errorf("ToolResultData[id_write] = %v, want the Write entry (Write is slotted like Edit)", resultWrite.ToolResultData)
	}

	// Error and async variants excluded even for slotted tools.
	errEdit := &mockWireTool{&mockTool{name: "Edit", enabled: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return nil, errors.New("boom")
	}}}
	bgWrite := &mockWireTool{&mockTool{name: "Write", enabled: true, callFn: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: &types.SubQueryResult{AsyncLaunched: true}}, nil
	}}}
	toolsBad := map[string]tool.Tool{"Edit": errEdit, "Write": bgWrite}
	blocksBad := []types.ContentBlock{
		types.NewToolUseBlock("id_err", "Edit", json.RawMessage(`{}`)),
		types.NewToolUseBlock("id_bg", "Write", json.RawMessage(`{}`)),
	}
	resultBad := ConcurrentToolLoop(context.Background(), toolsBad, blocksBad, nil, func(types.QueryEvent) {})
	for _, id := range []string{"id_err", "id_bg"} {
		if _, ok := resultBad.ToolResultData[id]; ok {
			t.Errorf("ToolResultData[%s] present, want excluded (error/async)", id)
		}
	}
}

// TestQuery_ToolResultDataAttachedAndDroppedFromAPI runs a real Edit through
// the full engine loop and pins both halves of the two-slot contract:
// the persisted user message carries the rich *fileedit.Output keyed by
// tool_use_id, and the API pipeline (marshalMessagesFrom rebuilds messages
// from Role/Content/Flags only) never leaks the slot to the provider.
func TestQuery_ToolResultDataAttachedAndDroppedFromAPI(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(fp, []byte("package main\n\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]string{
		"file_path": fp, "old_string": "func old() {}", "new_string": "func new() {}",
	})
	if err != nil {
		t.Fatal(err)
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "toolu_e1", "Edit", string(input)), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Edit": fileedit.New()}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Query(ctx, "call edit", "")
	ec.WaitForResult()

	msgs := eng.Messages()
	var richMsg *types.Message
	for i := range msgs {
		if msgs[i].Role != types.RoleUser {
			continue
		}
		for _, cb := range msgs[i].Content {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID == "toolu_e1" {
				richMsg = &msgs[i]
			}
		}
	}
	if richMsg == nil {
		t.Fatal("no tool_result user message for toolu_e1")
	}
	out, ok := richMsg.ToolResultData["toolu_e1"].(*fileedit.Output)
	if !ok {
		t.Fatalf("ToolResultData[toolu_e1] = %T, want *fileedit.Output", richMsg.ToolResultData["toolu_e1"])
	}
	if out.FilePath != fp || out.OldString != "func old() {}" || out.NewString != "func new() {}" {
		t.Errorf("rich output = %+v, want the executed edit's old/new strings", out)
	}

	for i, m := range eng.prepareAPIMessages() {
		if m.ToolResultData != nil {
			t.Errorf("apiMessages[%d] leaks ToolResultData to the provider: %v", i, m.ToolResultData)
		}
	}

	// Persisted metadata must carry the slot (SQLite round-trip rides in the
	// metadata JSON column; this pins the serialization key).
	if meta := richMsg.MetadataToJSON(); !strings.Contains(meta, `"toolUseResult"`) {
		t.Errorf("metadata JSON = %q, want toolUseResult key", meta)
	}
}
