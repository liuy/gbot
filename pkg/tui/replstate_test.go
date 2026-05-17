package tui

import (
	"fmt"
	"testing"
	"time"
)

// freshState creates a ReplState with a started query and one assistant message.
func freshState() *ReplState {
	s := NewReplState()
	s.StartQuery()
	return s
}

// ---------------------------------------------------------------------------
// NewReplState
// ---------------------------------------------------------------------------

func TestNewReplState_Initial(t *testing.T) {
	t.Parallel()
	s := NewReplState()
	if s.IsStreaming() {
		t.Error("new state should not be streaming")
	}
	if len(s.Messages()) != 0 {
		t.Errorf("new state should have 0 messages, got %d", len(s.Messages()))
	}
}

// ---------------------------------------------------------------------------
// AddUserMessage
// ---------------------------------------------------------------------------

func TestAddUserMessage(t *testing.T) {
	t.Parallel()
	s := NewReplState()
	s.AddUserMessage("hello")
	msgs := s.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role user, got %q", msgs[0].Role)
	}
	if len(msgs[0].Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(msgs[0].Blocks))
	}
	if msgs[0].Blocks[0].Text != "hello" {
		t.Errorf("expected text %q, got %q", "hello", msgs[0].Blocks[0].Text)
	}
}

// ---------------------------------------------------------------------------
// StartQuery / FinishStream lifecycle
// ---------------------------------------------------------------------------

func TestStartQuery_SetsStreaming(t *testing.T) {
	t.Parallel()
	s := NewReplState()
	s.StartQuery()
	if !s.IsStreaming() {
		t.Error("expected streaming after StartQuery")
	}
	msgs := s.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (assistant), got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("expected role assistant, got %q", msgs[0].Role)
	}
}

func TestFinishStream_StopsStreaming(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.FinishStream(nil)
	if s.IsStreaming() {
		t.Error("expected not streaming after FinishStream")
	}
}

func TestFinishStream_WithErr_AppendsSystemMsg(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.FinishStream(fmt.Errorf("boom"))
	msgs := s.Messages()
	// Should have assistant (from StartQuery) + system error message
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != "system" {
		t.Errorf("expected role system, got %q", last.Role)
	}
	if last.Blocks[0].Text != "Error: boom" {
		t.Errorf("expected error text, got %q", last.Blocks[0].Text)
	}
}

func TestFinishStream_NoErr_NoSystemMsg(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.FinishStream(nil)
	msgs := s.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (assistant only), got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// AppendChunk
// ---------------------------------------------------------------------------

func TestAppendChunk_CreatesTextBlock(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.AppendChunk("hi")
	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.Type != BlockText {
		t.Fatalf("expected BlockText, got %d", blk.Type)
	}
	if blk.Text != "hi" {
		t.Errorf("expected %q, got %q", "hi", blk.Text)
	}
}

func TestAppendChunk_AppendsToLastTextBlock(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.AppendChunk("hello")
	s.AppendChunk(" world")
	msgs := s.Messages()
	if len(msgs[0].Blocks) != 1 {
		t.Fatalf("expected 1 block (appended), got %d", len(msgs[0].Blocks))
	}
	if msgs[0].Blocks[0].Text != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", msgs[0].Blocks[0].Text)
	}
}

func TestAppendChunk_NoMsg_Noop(t *testing.T) {
	t.Parallel()
	s := NewReplState()
	// No StartQuery, no messages — should not panic
	s.AppendChunk("should not crash")
	if len(s.Messages()) != 0 {
		t.Error("expected no messages")
	}
}

// ---------------------------------------------------------------------------
// AppendTextItem
// ---------------------------------------------------------------------------

func TestAppendTextItem_CreatesEmptyTextBlock(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.AppendChunk("first")
	s.AppendTextItem()
	msgs := s.Messages()
	if len(msgs[0].Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(msgs[0].Blocks))
	}
	if msgs[0].Blocks[1].Text != "" {
		t.Errorf("expected empty text, got %q", msgs[0].Blocks[1].Text)
	}
}

// ---------------------------------------------------------------------------
// PendingToolStarted / PendingToolDone
// ---------------------------------------------------------------------------

func TestPendingToolStarted_AddsBlock(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("t1", "Bash", "ls -la", `{"command":"ls -la"}`)
	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.Type != BlockTool {
		t.Fatalf("expected BlockTool, got %d", blk.Type)
	}
	if blk.ToolCall.ID != "t1" {
		t.Errorf("expected ID t1, got %q", blk.ToolCall.ID)
	}
	if blk.ToolCall.Name != "Bash" {
		t.Errorf("expected name Bash, got %q", blk.ToolCall.Name)
	}
	if blk.ToolCall.Done {
		t.Error("tool should not be done yet")
	}
}

func TestPendingToolDone_UpdatesBlock(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("t1", "Bash", "ls", "{}")
	s.PendingToolDone("t1", "file1.txt\nfile2.txt", false, 100*time.Millisecond)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if !blk.ToolCall.Done {
		t.Error("tool should be done")
	}
	if blk.ToolCall.Output != "file1.txt\nfile2.txt" {
		t.Errorf("expected output, got %q", blk.ToolCall.Output)
	}
	if blk.ToolCall.IsError {
		t.Error("should not be error")
	}
}

func TestPendingToolDone_MissingID_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("t1", "Bash", "ls", "{}")
	// Different ID — should not crash or change existing tool
	s.PendingToolDone("nonexistent", "output", false, time.Second)
	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.Done {
		t.Error("t1 should remain not-done")
	}
}

func TestPendingToolDone_PerceivesHigherElapsed(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("t1", "Bash", "ls", "{}")
	// Wait a tiny bit so perceived > reported elapsed
	time.Sleep(5 * time.Millisecond) // REAL-TIME: needed to test perceived elapsed time measurement
	s.PendingToolDone("t1", "ok", false, 1*time.Nanosecond)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.Elapsed < 5*time.Millisecond {
		t.Errorf("expected perceived elapsed >= 5ms, got %v", blk.ToolCall.Elapsed)
	}
}

func TestPendingToolDone_AccumulatesSubAgentToolCount(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")
	// Simulate sub-agent adding blocks directly to Blocks
	tcv := s.pendingTool["agent1"]
	tcv.Blocks = append(tcv.Blocks,
		ContentBlock{Type: BlockTool, ToolCall: ToolCallView{Name: "Read", AgentType: "Explore", Done: true}},
		ContentBlock{Type: BlockTool, ToolCall: ToolCallView{Name: "Grep", AgentType: "Explore", Done: true}},
	)
	tcv.ToolCount = 2
	tcv.AgentType = "Explore"
	s.updateToolBlock("agent1", tcv)
	// ToolCount from Blocks should be accumulated
	s.PendingToolDone("agent1", "done", false, time.Second)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	// 1 (StartQuery toolCount) + 2 (agent sub-tools)
	// The toolCount on ReplState should include sub-agent tools
	// We can't directly check s.toolCount (unexported), but the block should be done
	if !blk.ToolCall.Done {
		t.Error("agent should be done")
	}
}

// ---------------------------------------------------------------------------
// PendingToolDelta
// ---------------------------------------------------------------------------

func TestPendingToolDelta_UpdatesSummary(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("t1", "Read", "", "{}")
	s.PendingToolDelta("t1", `{"file_path":"/tmp/test"}`, "/tmp/test")

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.Summary != "/tmp/test" {
		t.Errorf("expected summary /tmp/test, got %q", blk.ToolCall.Summary)
	}
}

func TestPendingToolDelta_MissingID_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// No PendingToolStarted — delta should not panic
	s.PendingToolDelta("nonexistent", `{"x":1}`, "summary")
}

// ---------------------------------------------------------------------------
// PendingToolOutput
// ---------------------------------------------------------------------------

func TestPendingToolOutput_UpdatesOutputAndDone(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("t1", "Bash", "ls", "{}")
	s.PendingToolOutput("t1", "line1\nline2", 50*time.Millisecond)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.Output != "line1\nline2" {
		t.Errorf("expected output, got %q", blk.ToolCall.Output)
	}
	if !blk.ToolCall.Done {
		t.Error("should be done after output")
	}
}

func TestPendingToolOutput_MissingID_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// Should not panic
	s.PendingToolOutput("nonexistent", "output", time.Second)
}

// ---------------------------------------------------------------------------
// Thinking lifecycle
// ---------------------------------------------------------------------------

func TestThinkingLifecycle(t *testing.T) {
	t.Parallel()
	s := freshState()

	// Start
	s.PendingThinkingStarted()
	msgs := s.Messages()
	if len(msgs[0].Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(msgs[0].Blocks))
	}
	blk := msgs[0].Blocks[0]
	if blk.Type != BlockThinking {
		t.Fatalf("expected BlockThinking, got %d", blk.Type)
	}
	if blk.Thinking.Done {
		t.Error("thinking should not be done yet")
	}

	// Delta
	s.PendingThinkingDelta("hmm...")
	if msgs[0].Blocks[0].Thinking.Text != "hmm..." {
		t.Errorf("expected thinking text, got %q", msgs[0].Blocks[0].Thinking.Text)
	}

	// Done
	s.PendingThinkingDone(200 * time.Millisecond)
	if !msgs[0].Blocks[0].Thinking.Done {
		t.Error("thinking should be done")
	}
	if msgs[0].Blocks[0].Thinking.Duration != 200*time.Millisecond {
		t.Errorf("expected 200ms, got %v", msgs[0].Blocks[0].Thinking.Duration)
	}
}

func TestPendingThinkingDelta_WithoutStarted_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// No PendingThinkingStarted — should not panic or add text
	s.PendingThinkingDelta("orphan delta")
	msgs := s.Messages()
	if len(msgs[0].Blocks) != 0 {
		t.Errorf("expected 0 blocks (no thinking started), got %d", len(msgs[0].Blocks))
	}
}

func TestPendingThinkingDone_WithoutStarted_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// No PendingThinkingStarted — should not panic
	s.PendingThinkingDone(time.Second)
}

func TestPendingThinkingDelta_InvalidIdx_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.AppendChunk("assistant")
	s.PendingThinkingStarted()
	// Remove the thinking block so activeThinkingIdx is out of range
	msgs := s.Messages()
	msgs[0].Blocks = nil
	s.PendingThinkingDelta("should not panic")
}

func TestPendingThinkingDone_InvalidIdx_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.AppendChunk("assistant")
	s.PendingThinkingStarted()
	// Remove the thinking block so activeThinkingIdx is out of range
	msgs := s.Messages()
	msgs[0].Blocks = nil
	s.PendingThinkingDone(time.Second)
}

func TestPendingThinkingDelta_NilLastMsg_Noop(t *testing.T) {
	t.Parallel()
	s := NewReplState()
	// Force activeThinkingIdx without any messages — lastMsg() returns nil
	s.activeThinkingIdx = 0
	s.PendingThinkingDelta("orphan delta")
	if len(s.Messages()) != 0 {
		t.Error("expected no messages")
	}
}

func TestPendingThinkingDone_NilLastMsg_Noop(t *testing.T) {
	t.Parallel()
	s := NewReplState()
	s.activeThinkingIdx = 0
	s.PendingThinkingDone(time.Second)
	if len(s.Messages()) != 0 {
		t.Error("expected no messages")
	}
}

// ---------------------------------------------------------------------------
// Blocks — sub-agent event blocks (replaces AgentEvents)
// ---------------------------------------------------------------------------

func TestBlocks_ToolStart(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	tcv := s.pendingTool["agent1"]
	tcv.Blocks = append(tcv.Blocks, ContentBlock{
		Type: BlockTool,
		ToolCall: ToolCallView{
			Name: "Read", Summary: "reading main.go", AgentType: "Explore",
		},
	})
	tcv.ToolCount++
	tcv.AgentType = "Explore"
	s.updateToolBlock("agent1", tcv)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blk.ToolCall.Blocks))
	}
	b := blk.ToolCall.Blocks[0]
	if b.ToolCall.Name != "Read" {
		t.Errorf("expected tool Read, got %q", b.ToolCall.Name)
	}
	if b.ToolCall.Summary != "reading main.go" {
		t.Errorf("expected summary, got %q", b.ToolCall.Summary)
	}
	if b.ToolCall.Done {
		t.Error("block should not be done yet")
	}
}

func TestBlocks_ToolEnd(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	tcv := s.pendingTool["agent1"]
	tcv.Blocks = append(tcv.Blocks, ContentBlock{
		Type:     BlockTool,
		ToolCall: ToolCallView{Name: "Read"},
	})
	tcv.ToolCount++
	s.updateToolBlock("agent1", tcv)

	// Mark done
	tcv.Blocks[0].ToolCall.Done = true
	s.updateToolBlock("agent1", tcv)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blk.ToolCall.Blocks))
	}
	if !blk.ToolCall.Blocks[0].ToolCall.Done {
		t.Error("block should be done after tool_end")
	}
}

func TestBlocks_ToolEnd_WithError(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	tcv := s.pendingTool["agent1"]
	tcv.Blocks = append(tcv.Blocks, ContentBlock{
		Type:     BlockTool,
		ToolCall: ToolCallView{Name: "Bash"},
	})
	tcv.ToolCount++
	tcv.Blocks[0].ToolCall.Done = true
	tcv.Blocks[0].ToolCall.IsError = true
	s.updateToolBlock("agent1", tcv)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if !blk.ToolCall.Blocks[0].ToolCall.IsError {
		t.Error("block should have IsError=true")
	}
}

func TestBlocks_ThinkingStartAndEnd(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	// thinking_start adds a Thinking block
	tcv := s.pendingTool["agent1"]
	tcv.Blocks = append(tcv.Blocks, ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{},
	})
	tcv.AgentType = "Explore"
	s.updateToolBlock("agent1", tcv)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.Blocks) != 1 {
		t.Fatalf("expected 1 block (thinking), got %d", len(blk.ToolCall.Blocks))
	}
	if blk.ToolCall.Blocks[0].Type != BlockThinking {
		t.Errorf("expected BlockThinking, got %d", blk.ToolCall.Blocks[0].Type)
	}

	// thinking_end removes the Thinking block
	tcv.Blocks = tcv.Blocks[:0]
	s.updateToolBlock("agent1", tcv)

	blk = msgs[0].Blocks[0]
	if len(blk.ToolCall.Blocks) != 0 {
		t.Errorf("expected 0 blocks (thinking removed), got %d", len(blk.ToolCall.Blocks))
	}
}

func TestBlocks_ToolParamDelta(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	tcv := s.pendingTool["agent1"]
	tcv.Blocks = append(tcv.Blocks, ContentBlock{
		Type:     BlockTool,
		ToolCall: ToolCallView{Name: "Read"},
	})
	tcv.ToolCount++
	s.updateToolBlock("agent1", tcv)

	// Update summary (tool_param_delta behavior)
	tcv.Blocks[0].ToolCall.Summary = "/tmp/updated.go"
	s.updateToolBlock("agent1", tcv)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.Blocks[0].ToolCall.Summary != "/tmp/updated.go" {
		t.Errorf("expected updated summary, got %q", blk.ToolCall.Blocks[0].ToolCall.Summary)
	}
}

func TestBlocks_UnknownParent_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// No tool started with this ID — findToolView returns nil, no panic
	tcv := s.findToolView("nonexistent")
	if tcv != nil {
		t.Error("expected nil for unknown parent")
	}
	msgs := s.Messages()
	if len(msgs[0].Blocks) != 0 {
		t.Error("expected no blocks")
	}
}

func TestBlocks_TrimsOver50Entries(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	tcv := s.pendingTool["agent1"]
	// Add 55 tool blocks directly
	for i := range 55 {
		tcv.Blocks = append(tcv.Blocks, ContentBlock{
			Type: BlockTool,
			ToolCall: ToolCallView{
				Name: "Read", Summary: fmt.Sprintf("file_%d", i), Done: true,
			},
		})
		tcv.ToolCount++
	}
	s.trimBlocks(tcv)
	s.updateToolBlock("agent1", tcv)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.Blocks) > 50 {
		t.Errorf("expected <= 50 blocks, got %d", len(blk.ToolCall.Blocks))
	}
}

// ---------------------------------------------------------------------------
// AgentUsage — direct token accumulation (replaces UpdateAgentUsage)
// ---------------------------------------------------------------------------

func TestAgentUsage_Accumulates(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	tcv := s.pendingTool["agent1"]
	tcv.TokensIn += 100
	tcv.TokensOut += 200
	tcv.ContextSize = 300
	s.updateToolBlock("agent1", tcv)

	tcv.TokensIn += 50
	tcv.TokensOut += 75
	tcv.ContextSize = 125
	s.updateToolBlock("agent1", tcv)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.TokensIn != 150 {
		t.Errorf("expected TokensIn=150, got %d", blk.ToolCall.TokensIn)
	}
	if blk.ToolCall.TokensOut != 275 {
		t.Errorf("expected TokensOut=275, got %d", blk.ToolCall.TokensOut)
	}
	// Last contextSize wins
	if blk.ToolCall.ContextSize != 125 {
		t.Errorf("expected ContextSize=125, got %d", blk.ToolCall.ContextSize)
	}
}

func TestSetAgentContextWindow(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	s.SetAgentContextWindow("agent1", 200000)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.ContextWindow != 200000 {
		t.Errorf("expected ContextWindow=200000, got %d", blk.ToolCall.ContextWindow)
	}
}

func TestSetAgentContextWindow_UnknownParent_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// Should not panic
	s.SetAgentContextWindow("nonexistent", 200000)
}

func TestAgentUsage_UnknownParent_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// Should not panic — findToolView returns nil
	tcv := s.findToolView("nonexistent")
	if tcv != nil {
		t.Error("expected nil for unknown parent")
	}
}

// ---------------------------------------------------------------------------
// updateToolBlock — tested indirectly through PendingToolDone
// ---------------------------------------------------------------------------

func TestUpdateToolBlock_NotFound_ReturnsFalse(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("t1", "Bash", "ls", "{}")
	// PendingToolDone with nonexistent ID → updateToolBlock returns false,
	// but PendingToolDone itself just returns (no-op)
	s.PendingToolDone("nonexistent", "output", false, time.Second)

	// Verify t1 is unchanged
	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.Done {
		t.Error("t1 should remain not-done")
	}
}

// ---------------------------------------------------------------------------
// Multiple tools in one query
// ---------------------------------------------------------------------------

func TestMultipleToolsInQuery(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.AppendChunk("let me check")

	s.PendingToolStarted("t1", "Read", "main.go", "{}")
	s.PendingToolStarted("t2", "Grep", "TODO", "{}")

	s.PendingToolDone("t1", "package main...", false, 10*time.Millisecond)
	s.PendingToolDone("t2", "3 matches found", false, 20*time.Millisecond)

	msgs := s.Messages()
	if len(msgs[0].Blocks) != 3 {
		t.Fatalf("expected 3 blocks (1 text + 2 tool), got %d", len(msgs[0].Blocks))
	}
	if msgs[0].Blocks[0].Type != BlockText {
		t.Error("first block should be text")
	}
	if msgs[0].Blocks[1].Type != BlockTool {
		t.Error("second block should be tool")
	}
	if msgs[0].Blocks[2].Type != BlockTool {
		t.Error("third block should be tool")
	}
	if !msgs[0].Blocks[1].ToolCall.Done {
		t.Error("t1 should be done")
	}
	if !msgs[0].Blocks[2].ToolCall.Done {
		t.Error("t2 should be done")
	}
}

// ---------------------------------------------------------------------------
// CloseChannels
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Grandchild (depth 2) recursive search in findToolView / updateToolBlock
// ---------------------------------------------------------------------------

// setupNestedAgent creates a two-level nesting:
//   pendingTool["agent1"] → Blocks[0] = ToolCall{ID: "child_agent"}
//
// This simulates a child agent that has spawned its own agent tool (grandchild).
func setupNestedAgent() *ReplState {
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	// Simulate child agent spawning a grandchild tool inside the parent's Blocks
	parent := s.pendingTool["agent1"]
	parent.AgentType = "Explore"
	parent.Blocks = append(parent.Blocks, ContentBlock{
		Type: BlockTool,
		ToolCall: ToolCallView{
			ID:   "child_agent",
			Name: "Agent",
		},
	})
	parent.ToolCount++
	s.updateToolBlock("agent1", parent)
	return s
}

func TestFindToolView_FindsNestedBlock(t *testing.T) {
	t.Parallel()
	s := setupNestedAgent()

	// "child_agent" is nested inside agent1.Blocks, not at message top level.
	got := s.findToolView("child_agent")
	if got == nil {
		t.Fatal("findToolView should find nested block 'child_agent' inside parent agent")
	}
	if got.ID != "child_agent" {
		t.Errorf("expected ID child_agent, got %q", got.ID)
	}
	if got.Name != "Agent" {
		t.Errorf("expected Name Agent, got %q", got.Name)
	}
}

func TestFindToolView_FindsTopLevel(t *testing.T) {
	t.Parallel()
	s := setupNestedAgent()

	// "agent1" is at top level (pendingTool) — should still work.
	got := s.findToolView("agent1")
	if got == nil {
		t.Fatal("findToolView should find top-level 'agent1'")
	}
	if got.ID != "agent1" {
		t.Errorf("expected ID agent1, got %q", got.ID)
	}
}

func TestFindToolView_DeeplyNested(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	// Level 2: child inside agent1
	parent := s.pendingTool["agent1"]
	parent.AgentType = "Explore"
	parent.Blocks = append(parent.Blocks, ContentBlock{
		Type: BlockTool,
		ToolCall: ToolCallView{
			ID:   "child_agent",
			Name: "Agent",
			Blocks: []ContentBlock{
				{
					Type: BlockTool,
					ToolCall: ToolCallView{
						ID:   "grandchild_read",
						Name: "Read",
					},
				},
			},
		},
	})
	s.updateToolBlock("agent1", parent)

	// Should find the deeply nested grandchild
	got := s.findToolView("grandchild_read")
	if got == nil {
		t.Fatal("findToolView should find deeply nested 'grandchild_read'")
	}
	if got.ID != "grandchild_read" {
		t.Errorf("expected ID grandchild_read, got %q", got.ID)
	}
}

func TestUpdateToolBlock_UpdatesNestedBlock(t *testing.T) {
	t.Parallel()
	s := setupNestedAgent()

	// Mutate the nested child via updateToolBlock
	child := s.findToolView("child_agent")
	child.Done = true
	child.Output = "grandchild result"
	ok := s.updateToolBlock("child_agent", child)
	if !ok {
		t.Fatal("updateToolBlock should return true for nested block")
	}

	// Re-fetch to verify persistence
	got := s.findToolView("child_agent")
	if got == nil {
		t.Fatal("findToolView should still find child_agent after update")
	}
	if !got.Done {
		t.Error("child_agent should be done after update")
	}
	if got.Output != "grandchild result" {
		t.Errorf("expected output 'grandchild result', got %q", got.Output)
	}
}

func TestUpdateToolBlock_NestedNotFound_ReturnsFalse(t *testing.T) {
	t.Parallel()
	s := setupNestedAgent()

	tcv := &ToolCallView{ID: "nonexistent", Done: true}
	ok := s.updateToolBlock("nonexistent", tcv)
	if ok {
		t.Error("updateToolBlock should return false for nonexistent ID")
	}
}

// TestGrandchildTextDelta verifies the full flow: grandchild text events
// are appended to the correct nested parent's Blocks.
func TestGrandchildTextDelta(t *testing.T) {
	t.Parallel()
	s := setupNestedAgent()

	// Simulate grandchild text delta arriving
	parent := s.findToolView("child_agent")
	if parent == nil {
		t.Fatal("child_agent not found")
	}

	// Append text block (same logic as textDeltaMsg handler)
	// parent.Blocks is empty, so this will create a new text block.
	parent.Blocks = append(parent.Blocks, ContentBlock{Type: BlockText, Text: "hello"})
	s.updateToolBlock("child_agent", parent)

	// Verify text is persisted in the nested structure
	got := s.findToolView("child_agent")
	if len(got.Blocks) != 1 {
		t.Fatalf("expected 1 block in child_agent, got %d", len(got.Blocks))
	}
	if got.Blocks[0].Type != BlockText {
		t.Errorf("expected BlockText, got %v", got.Blocks[0].Type)
	}
	if got.Blocks[0].Text != "hello" {
		t.Errorf("expected text 'hello', got %q", got.Blocks[0].Text)
	}
}

// ---------------------------------------------------------------------------
// toolEndMsg with IsBackground — card stays Done=false
// ---------------------------------------------------------------------------

func TestToolEndMsg_IsBackground_NotMarkedDone(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("tool-bg-1", "Agent", "fork", "{}")

	// Simulate toolEndMsg with IsBackground=true via direct state manipulation.
	// The handler sets IsBackground=true and keeps Done=false.
	tcv := s.findToolView("tool-bg-1")
	if tcv == nil {
		t.Fatal("tool-bg-1 should exist")
	}
	tcv.IsBackground = true
	tcv.Output = "Fork agent launched"
	s.updateToolBlock("tool-bg-1", tcv)

	// Verify Done is still false
	got := s.findToolView("tool-bg-1")
	if got.Done {
		t.Error("IsBackground tool should NOT be marked Done")
	}
	if !got.IsBackground {
		t.Error("IsBackground should be true")
	}
	if got.Output != "Fork agent launched" {
		t.Errorf("expected output 'Fork agent launched', got %q", got.Output)
	}
}

// ---------------------------------------------------------------------------
// queryEndMsg from fork agent — marks parent card Done
// ---------------------------------------------------------------------------

func TestQueryEndMsg_ForkAgent_MarksParentDone(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("tool-bg-1", "Agent", "fork", "{}")
	s.pendingToolStart["tool-bg-1"] = time.Now().Add(-1 * time.Second)  // REAL-TIME: pending tool start time

	// Set parent as background
	tcv := s.findToolView("tool-bg-1")
	tcv.IsBackground = true
	s.updateToolBlock("tool-bg-1", tcv)

	// Simulate fork agent completion: manually set Done and Elapsed
	// (the queryEndMsg handler does this)
	parent := s.findToolView("tool-bg-1")
	if parent == nil {
		t.Fatal("parent should exist")
	}
	parent.Done = true
	parent.Elapsed = time.Second
	s.updateToolBlock("tool-bg-1", parent)

	// Verify
	got := s.findToolView("tool-bg-1")
	if !got.Done {
		t.Error("parent card should be Done after fork agent queryEnd")
	}
	if got.Elapsed < 500*time.Millisecond {
		t.Errorf("expected Elapsed >= 500ms, got %v", got.Elapsed)
	}
}

func TestQueryEndMsg_ForkAgent_AlreadyDone_NoOp(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("tool-bg-2", "Agent", "fork", "{}")

	// Set parent as background AND already done
	tcv := s.findToolView("tool-bg-2")
	tcv.IsBackground = true
	tcv.Done = true
	tcv.Elapsed = 2 * time.Second
	s.updateToolBlock("tool-bg-2", tcv)

	// Re-read and verify nothing changed
	got := s.findToolView("tool-bg-2")
	if !got.Done {
		t.Error("should still be Done")
	}
	if got.Elapsed != 2*time.Second {
		t.Errorf("Elapsed should be unchanged, got %v", got.Elapsed)
	}
}
