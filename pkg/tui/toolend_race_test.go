package tui

import (
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// TestToolEnd_MatchesByID_NotByPosition verifies that when two sub-engine tools
// (e.g. Bash + Agent) run concurrently inside the same parent agent, their
// toolEndMsg results are matched to the correct tool block by ToolUseID — not
// by finding the last unfinished block.
//
// toolEndMsg handler used "last !Done block" matching, so if toolEnd
// events arrived in different order than toolStart, outputs were swapped.
func TestToolEnd_MatchesByID_NotByPosition(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Set up: main engine starts an Agent tool
	app.repl.StartQuery()
	app.status.SetStreaming(true)

	agentToolID := "call_agent_parent"
	app.repl.PendingToolStarted(agentToolID, "Agent", "Run sub-tasks", "", tool.SearchReadKind{})

	agent := &types.AgentMeta{
		ParentToolUseID: agentToolID,
		AgentType:       "background",
		Depth:           1,
	}

	// Sub-engine starts Bash (tool A) then Agent (tool B)
	bashID := "call_bash_001"
	agentSubID := "call_agent_sub_002"

	_, _ = app.updateRepl(toolStartMsg{
		ID:       bashID,
		Name:     "Bash",
		Summary:  "sleep 10 && echo done",
		Agent:    agent,
	})
	_, _ = app.updateRepl(toolStartMsg{
		ID:       agentSubID,
		Name:     "Agent",
		Summary:  "Count lines",
		Agent:    agent,
	})

	// toolEnd arrives in SAME order as toolStart: Bash finishes first (0.1s),
	// Agent finishes second (0.3s). "Last !Done" matching would assign Bash's
	// output to the Agent block (last unfinished), causing the swap.
	_, _ = app.updateRepl(toolEndMsg{
		ToolUseID: bashID,
		Output:    "Bash result: bg-1 started",
		Timing:    100 * time.Millisecond,
		Agent:     agent,
	})
	_, _ = app.updateRepl(toolEndMsg{
		ToolUseID: agentSubID,
		Output:    "Agent result: 42 lines",
		Timing:    300 * time.Millisecond,
		Agent:     agent,
	})

	// Find the parent tool view and verify each block has the correct output
	parent := app.repl.findToolView(agentToolID)
	if parent == nil {
		t.Fatal("parent tool view not found")
	}

	var bashBlock, agentBlock *ToolCallView
	for i := range parent.Blocks {
		blk := &parent.Blocks[i]
		if blk.Type != BlockTool {
			continue
		}
		switch blk.ToolCall.ID {
		case bashID:
			bashBlock = &blk.ToolCall
		case agentSubID:
			agentBlock = &blk.ToolCall
		}
	}

	if bashBlock == nil {
		t.Fatal("Bash tool block not found in parent")
	}
	if agentBlock == nil {
		t.Fatal("Agent tool block not found in parent")
	}

	// KEY CHECK: outputs must NOT be swapped
	if bashBlock.Output != "Bash result: bg-1 started" {
		t.Errorf("Bash block output = %q, want %q (agent result leaked in!)", bashBlock.Output, "Bash result: bg-1 started")
	}
	if agentBlock.Output != "Agent result: 42 lines" {
		t.Errorf("Agent block output = %q, want %q (bash result leaked in!)", agentBlock.Output, "Agent result: 42 lines")
	}
}
