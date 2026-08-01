package tui

// compact_command_test.go tests the /compact TUI handler. After the refactor,
// /compact no longer creates a tool block synchronously. Instead it launches
// ManualCompact in a background goroutine; the engine emits EventToolStart/
// Run/ParamDelta/End through the hub, and readEvents drains those into TUI
// messages that drive the tool block. The test app's engine has no compactor,
// so ManualCompact returns at the comp==nil guard before emitting any event —
// readEvents therefore blocks on appCh forever (no toolEndMsg is produced).

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// newCompactTestApp builds a minimal App with a non-nil engine for compact
// handler tests. The engine has no compactor, so ManualCompact returns at the
// comp==nil guard before emitting any event — no toolEndMsg reaches appCh.
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

// TestHandleCompact_NotStreaming_LaunchesGoroutine verifies that when not
// streaming and an engine is present, handleCompact adds the user message,
// starts the query (streaming flag set), launches the ManualCompact goroutine,
// and returns a non-nil batched cmd. The tool block is NOT created
// synchronously — it now appears only when the engine's EventToolStart arrives
// via readEvents, which the nil-compactor test engine never emits.
func TestHandleCompact_NotStreaming_LaunchesGoroutine(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)

	cmd := a.handleCompact("", nil)
	if cmd == nil {
		t.Fatal("handleCompact should return a non-nil tea.Cmd (batch of readEvents + spinner tick)")
	}

	// StartQuery sets streaming=true and resets pendingTool.
	if !a.repl.streaming {
		t.Error("a.repl.streaming should be true after handleCompact — StartQuery was not called")
	}

	// The user message must be appended so it shows in the transcript.
	// StartQuery (called next) appends an empty assistant message after it,
	// so the user message is the last *user* message, not the last overall.
	msgs := a.repl.Messages()
	userText := lastUserMessageText(msgs)
	if !strings.Contains(userText, "/compact") {
		t.Errorf("expected a user message containing \"/compact\", got last user text %q (msgs=%d)", userText, len(msgs))
	}

	// No synchronous tool block: StartQuery reset pendingTool, and the new
	// handler does not call PendingToolStarted (the engine EventToolStart
	// drives it instead, which the nil-compactor engine never emits).
	if len(a.repl.pendingTool) != 0 {
		t.Errorf("pendingTool should be empty (no synchronous tool block), got %d entries: %+v",
			len(a.repl.pendingTool), a.repl.pendingTool)
	}
}

// TestHandleCompact_BatchesInitialSpinnerTick is the regression test for the
// frozen-spinner bug. /compact does not flow through turnStartMsg (which
// normally emits the initial spinnerTickMsg), so handleCompact must batch its
// own initial tick — otherwise the spinner chain never starts and the
// progress bar stays frozen during the async ManualCompact call.
//
// The batch also contains a.readEvents() which blocks forever on appCh
// (nil-compactor engine emits no events), so a synchronous walk would hang
// the test. Use the goroutine+timeout pattern: run each batched sub-cmd in a
// goroutine and select on its result vs a timeout — readEvents times out
// (skip), the spinner tick returns immediately (assert).
func TestHandleCompact_BatchesInitialSpinnerTick(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)

	cmd := a.handleCompact("", nil)
	if cmd == nil {
		t.Fatal("handleCompact should return a non-nil tea.Cmd")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("cmd() returned nil msg")
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("handleCompact returned %T, expected tea.BatchMsg so an initial spinnerTickMsg fires alongside readEvents", msg)
	}

	foundTick := false
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func(c tea.Cmd) { done <- c() }(sub)
		select {
		case m := <-done:
			if _, ok := m.(spinnerTickMsg); ok {
				foundTick = true
			}
		case <-time.After(500 * time.Millisecond):
			// readEvents blocks on appCh (nil-compactor engine emits no
			// events); that's expected — skip it.
		}
	}
	// Unblock the lingering readEvents goroutine: close idleStop so its
	// select returns idleAbortedMsg and the goroutine exits instead of
	// leaking until process exit. We do NOT nil the field — that write
	// races with readEvents' read of a.idleStop in the select.
	if a.idleStop != nil {
		close(a.idleStop)
	}
	if !foundTick {
		t.Error("no spinnerTickMsg in batched cmd — progress line will freeze during /compact")
	}
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

// TestApp_CompactQueryEndMsg_SyncsContextUsed verifies that after /compact's
// queryEndMsg, the status bar reflects the engine's post-compact ContextTokens.
// ManualCompact emits a full EventQueryStart → EventQueryEnd cycle, so the
// queryEndMsg handler (not the toolEndMsg handler) syncs context tokens.
func TestApp_CompactQueryEndMsg_SyncsContextUsed(t *testing.T) {
	t.Parallel()
	a := newCompactTestApp(t)

	// Pre-compact state: status bar shows a large context.
	a.status.SetContext(80000, 200000)
	// Simulate ManualCompact's effect: engine.ContextTokens updated to the
	// post-compact precise value.
	a.engine.ContextTokens = 15000

	// ManualCompact emits EventQueryEnd after the tool events, so queryEndMsg
	// is what triggers the context sync + FinishStream.
	model, _ := a.Update(queryEndMsg{})
	updated := model.(*App)

	if got := updated.status.contextUsed; got != 15000 {
		t.Errorf("after compact queryEndMsg, contextUsed = %d, want 15000 (engine's post-compact ContextTokens) — status bar must reflect the compacted size, not the pre-compact value",
			got)
	}
}

func lastUserMessageText(msgs []MessageView) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && len(msgs[i].Blocks) > 0 {
			return msgs[i].Blocks[0].Text
		}
	}
	return ""
}
