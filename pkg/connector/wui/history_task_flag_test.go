package wui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// History replay must recompute Task classification from the tool registry:
// a Task tool_use in a persisted session gets IsTask on both the tools array
// entry and the tool block, and serializes as "is_task":true for the client.
func TestBuildHistoryChatMsg_TaskIsTaskFlag(t *testing.T) {
	c := &WUIConnector{}
	tools := map[string]tool.Tool{"Task": task.New(task.NewList(t.TempDir()), nil)}

	assistant := types.NewAssistantMessage([]types.ContentBlock{
		types.NewToolUseBlock("toolu_1", "Task", json.RawMessage(`{"action":"create","subject":"S","description":"D"}`)),
	})
	resultBlock := types.NewToolResultBlock("toolu_1", json.RawMessage(`[{"type":"text","text":"Created: #1 S"}]`), false)
	toolResults := map[string]types.ContentBlock{"toolu_1": resultBlock}

	hm := c.buildHistoryChatMsg(assistant, tools, toolResults, nil)
	if len(hm.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(hm.Tools))
	}
	if !hm.Tools[0].IsTask {
		t.Error("hm.Tools[0].IsTask = false, want true")
	}
	if hm.Blocks[0].Tool == nil {
		t.Fatal("hm.Blocks[0].Tool is nil")
	}
	if !hm.Blocks[0].Tool.IsTask {
		t.Error("hm.Blocks[0].Tool.IsTask = false, want true")
	}

	b, err := json.Marshal(hm.Blocks[0])
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	if !strings.Contains(string(b), `"is_task":true`) {
		t.Errorf("block JSON missing \"is_task\":true, got: %s", b)
	}
}
