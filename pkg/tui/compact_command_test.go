package tui

// compact_command_test.go tests the /compact TUI handler. /compact uses the
// virtual tool pattern (mirrors bash_shortcut.go): a tool block is created
// synchronously so the card appears immediately, then ManualCompact runs
// async and returns a toolEndMsg that fills the result into the card.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newCompactTestApp builds a minimal App with a non-nil engine for compact
// handler tests. The engine has no compactor, so ManualCompact returns an
// error — useful for testing the error→toolEndMsg path. Guards (streaming /
// nil engine) are checked before the async cmd runs.
func newCompactTestApp(t *testing.T) *App {
	t.Helper()
	return newTestApp(&tuiMockProvider{})
}

// assertCmdProducesInfoMsg walks a tea.Cmd's result and reports whether any
// leaf produces an infoMsg, and returns its text. Used to detect showInfo.
func assertCmdProducesInfoMsg(t *testing.T, cmd tea.Cmd) (bool, string) {
	t.Helper()
	if cmd == nil {
		return false, ""
	}
	return msgProducesInfoMsg(t, cmd())
}

func msgProducesInfoMsg(t *testing.T, msg tea.Msg) (bool, string) {
	t.Helper()
	if msg == nil {
		return false, ""
	}
	if info, ok := msg.(infoMsg); ok {
		return true, string(info)
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if ok, text := assertCmdProducesInfoMsg(t, sub); ok {
				return true, text
			}
		}
	}
	return false, ""
}

// msgProducesToolEnd walks a tea.Msg (including nested BatchMsg) and reports
// the first toolEndMsg found, if any. The async cmd may be batched with a
// spinnerTickMsg, so we must walk the batch.
func msgProducesToolEnd(t *testing.T, msg tea.Msg) (toolEndMsg, bool) {
	t.Helper()
	if msg == nil {
		return toolEndMsg{}, false
	}
	if tem, ok := msg.(toolEndMsg); ok {
		return tem, true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if tem, ok := msgProducesToolEnd(t, sub()); ok {
				return tem, true
			}
		}
	}
	return toolEndMsg{}, false
}

// msgBatchContainsSpinnerTick walks a tea.Msg (including nested BatchMsg) and
// reports whether any leaf produces a spinnerTickMsg. Without an initial tick,
// the spinner chain never starts and the progress bar stays frozen during
// the async ManualCompact call.
func msgBatchContainsSpinnerTick(t *testing.T, msg tea.Msg) bool {
	t.Helper()
	if msg == nil {
		return false
	}
	if _, ok := msg.(spinnerTickMsg); ok {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if _, ok := sub().(spinnerTickMsg); ok {
				return true
			}
		}
	}
	return false
}

// TestHandleCompact_NotStreaming_CreatesToolBlock verifies that when not
// streaming and an engine is present, handleCompact synchronously creates a
// Compact tool block (PendingToolStarted) so the card appears immediately,
// and returns a non-nil async cmd whose execution yields a toolEndMsg.
func TestHandleCompact_NotStreaming_CreatesToolBlock(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)

	cmd := a.handleCompact("", nil)
	if cmd == nil {
		t.Fatal("handleCompact should return a non-nil tea.Cmd to run compact async")
	}

	// The tool block must be created synchronously so the card shows before
	// the (possibly multi-second) LLM call completes.
	if len(a.repl.pendingTool) == 0 {
		t.Error("PendingToolStarted should add a Compact tool block synchronously — without it the card appears only after ManualCompact returns")
	}

	tem, ok := msgProducesToolEnd(t, cmd())
	if !ok {
		t.Fatalf("batched cmd should produce toolEndMsg, got %T", cmd())
	}
	if !strings.HasPrefix(tem.ToolUseID, "compact-manual-") {
		t.Errorf("toolEndMsg.ToolUseID = %q, want compact-manual-* prefix", tem.ToolUseID)
	}
	// Engine has no compactor → ManualCompact returns "compaction not
	// configured" → the error path must surface it in the card.
	if !tem.IsError {
		t.Error("toolEndMsg.IsError should be true when ManualCompact fails (no compactor)")
	}
	if !strings.Contains(tem.Output, "compaction not configured") {
		t.Errorf("toolEndMsg.Output = %q, want substring 'compaction not configured'", tem.Output)
	}
}

// TestHandleCompact_BatchesInitialSpinnerTick is the regression test for the
// frozen-spinner bug. /compact does not flow through turnStartMsg (which
// normally emits the initial spinnerTickMsg), so handleCompact must batch its
// own initial tick — otherwise the spinner chain never starts and the
// progress bar stays frozen during the async ManualCompact call.
func TestHandleCompact_BatchesInitialSpinnerTick(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)

	cmd := a.handleCompact("", nil)
	if cmd == nil {
		t.Fatal("handleCompact should return a non-nil tea.Cmd")
	}
	if !msgBatchContainsSpinnerTick(t, cmd()) {
		t.Error("handleCompact must batch an initial spinnerTickMsg — without it the spinner never animates during the async ManualCompact call")
	}
}

// TestHandleCompact_CustomInstructionsShown verifies custom instructions are
// reflected in the tool card summary.
func TestHandleCompact_CustomInstructionsShown(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)

	a.handleCompact("focus on the bug fix", nil)

	if len(a.repl.pendingTool) == 0 {
		t.Fatal("expected a pending tool block")
	}
	// Find the Compact tool block and check its summary carries the instructions.
	const wantSummary = "Compacting conversation (focus on the bug fix)"
	for _, tc := range a.repl.pendingTool {
		if tc.Name == "Compact" && tc.Summary == wantSummary {
			return
		}
	}
	t.Errorf("Compact tool block summary = want %q, pendingTool=%+v", wantSummary, a.repl.pendingTool)
}

// TestHandleCompact_Streaming_Rejects verifies that /compact while streaming
// is rejected with an info message and does NOT create a tool block.
func TestHandleCompact_Streaming_Rejects(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)
	a.repl.streaming = true

	cmd := a.handleCompact("", nil)
	if cmd == nil {
		t.Fatal("expected non-nil cmd (showInfo) when streaming")
	}
	ok, text := assertCmdProducesInfoMsg(t, cmd)
	if !ok {
		t.Fatalf("expected infoMsg rejecting compact while streaming, got %T", cmd())
	}
	if !strings.Contains(text, "Cannot compact while streaming") {
		t.Errorf("info text = %q, want substring %q", text, "Cannot compact while streaming")
	}
	if len(a.repl.pendingTool) != 0 {
		t.Error("reject path must NOT create a tool block — streaming rejection happens before dispatch")
	}
}

// TestHandleCompact_NilEngine_Rejects verifies that /compact with no engine
// is rejected with an info message and does NOT create a tool block.
func TestHandleCompact_NilEngine_Rejects(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)
	a.engine = nil

	cmd := a.handleCompact("", nil)
	if cmd == nil {
		t.Fatal("expected non-nil cmd (showInfo) when engine is nil")
	}
	ok, text := assertCmdProducesInfoMsg(t, cmd)
	if !ok {
		t.Fatalf("expected infoMsg rejecting compact with nil engine, got %T", cmd())
	}
	if !strings.Contains(text, "No engine available") {
		t.Errorf("info text = %q, want substring %q", text, "No engine available")
	}
	if len(a.repl.pendingTool) != 0 {
		t.Error("reject path must NOT create a tool block")
	}
}
