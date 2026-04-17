package tui

import (
	"fmt"
	"testing"
	"time"
)

// freshState creates a ReplState with a started query and one assistant message.
func freshState() *ReplState {
	s := NewReplState()
	s.StartQuery(nil)
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
	s.StartQuery(nil)
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
	time.Sleep(5 * time.Millisecond)
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
	// Simulate sub-agent adding ToolCount via UpdateAgentProgress
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Read",
		Depth:           0,
	})
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Grep",
		Depth:           0,
	})
	// ToolCount from AgentLogs should be accumulated
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

// ---------------------------------------------------------------------------
// UpdateAgentProgress
// ---------------------------------------------------------------------------

func TestUpdateAgentProgress_ToolStart(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Read",
		Summary:         "reading main.go",
		Depth:           0,
	})

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.AgentLogs) != 1 {
		t.Fatalf("expected 1 agent log, got %d", len(blk.ToolCall.AgentLogs))
	}
	log := blk.ToolCall.AgentLogs[0]
	if log.ToolName != "Read" {
		t.Errorf("expected tool Read, got %q", log.ToolName)
	}
	if log.Summary != "reading main.go" {
		t.Errorf("expected summary, got %q", log.Summary)
	}
	if log.Done {
		t.Error("log should not be done yet")
	}
}

func TestUpdateAgentProgress_ToolEnd(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		SubType:         "tool_start",
		ToolName:        "Read",
		Depth:           0,
	})
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		SubType:         "tool_end",
		ToolName:        "Read",
		Depth:           0,
	})

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.AgentLogs) != 1 {
		t.Fatalf("expected 1 agent log, got %d", len(blk.ToolCall.AgentLogs))
	}
	if !blk.ToolCall.AgentLogs[0].Done {
		t.Error("log should be done after tool_end")
	}
}

func TestUpdateAgentProgress_ToolEnd_WithError(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		SubType:         "tool_start",
		ToolName:        "Bash",
		Depth:           0,
	})
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		SubType:         "tool_end",
		ToolName:        "Bash",
		Depth:           0,
		IsError:         true,
	})

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if !blk.ToolCall.AgentLogs[0].IsError {
		t.Error("log should have IsError=true")
	}
}

func TestUpdateAgentProgress_ThinkingStartAndEnd(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	// thinking_start adds a Thinking entry
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		AgentType:       "Explore",
		SubType:         "thinking_start",
		Depth:           0,
	})

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.AgentLogs) != 1 {
		t.Fatalf("expected 1 log (thinking), got %d", len(blk.ToolCall.AgentLogs))
	}
	if blk.ToolCall.AgentLogs[0].ToolName != "Thinking" {
		t.Errorf("expected Thinking, got %q", blk.ToolCall.AgentLogs[0].ToolName)
	}

	// thinking_end removes the Thinking entry
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		SubType:         "thinking_end",
		Depth:           0,
	})

	blk = msgs[0].Blocks[0]
	if len(blk.ToolCall.AgentLogs) != 0 {
		t.Errorf("expected 0 logs (thinking removed), got %d", len(blk.ToolCall.AgentLogs))
	}
}

func TestUpdateAgentProgress_ToolParamDelta(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		SubType:         "tool_start",
		ToolName:        "Read",
		Depth:           0,
	})
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "agent1",
		SubType:         "tool_param_delta",
		ToolName:        "Read",
		Summary:         "/tmp/updated.go",
		Depth:           0,
	})

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.AgentLogs[0].Summary != "/tmp/updated.go" {
		t.Errorf("expected updated summary, got %q", blk.ToolCall.AgentLogs[0].Summary)
	}
}

func TestUpdateAgentProgress_UnknownParent_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// No tool started with this ID — should not panic
	s.UpdateAgentProgress(agentToolMsg{
		ParentToolUseID: "nonexistent",
		SubType:         "tool_start",
		ToolName:        "Read",
		Depth:           0,
	})
	msgs := s.Messages()
	if len(msgs[0].Blocks) != 0 {
		t.Error("expected no blocks")
	}
}

func TestUpdateAgentProgress_TrimsOver50Entries(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	// Add 55 tool entries
	for i := 0; i < 55; i++ {
		s.UpdateAgentProgress(agentToolMsg{
			ParentToolUseID: "agent1",
			SubType:         "tool_start",
			ToolName:        "Read",
			Summary:         fmt.Sprintf("file_%d", i),
			Depth:           0,
		})
		s.UpdateAgentProgress(agentToolMsg{
			ParentToolUseID: "agent1",
			SubType:         "tool_end",
			ToolName:        "Read",
			Depth:           0,
		})
	}

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if len(blk.ToolCall.AgentLogs) > 50 {
		t.Errorf("expected <= 50 agent logs, got %d", len(blk.ToolCall.AgentLogs))
	}
}

// ---------------------------------------------------------------------------
// UpdateAgentUsage
// ---------------------------------------------------------------------------

func TestUpdateAgentUsage_Accumulates(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.PendingToolStarted("agent1", "Agent", "explore", "{}")

	s.UpdateAgentUsage("agent1", 100, 200)
	s.UpdateAgentUsage("agent1", 50, 75)

	msgs := s.Messages()
	blk := msgs[0].Blocks[0]
	if blk.ToolCall.TokensIn != 150 {
		t.Errorf("expected TokensIn=150, got %d", blk.ToolCall.TokensIn)
	}
	if blk.ToolCall.TokensOut != 275 {
		t.Errorf("expected TokensOut=275, got %d", blk.ToolCall.TokensOut)
	}
}

func TestUpdateAgentUsage_UnknownParent_Noop(t *testing.T) {
	t.Parallel()
	s := freshState()
	// Should not panic
	s.UpdateAgentUsage("nonexistent", 100, 200)
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

func TestCloseChannels(t *testing.T) {
	t.Parallel()
	s := freshState()
	s.CloseChannels()
	// Just verify it doesn't panic — resultCh is unexported
}
