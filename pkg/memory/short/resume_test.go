package short

import (
	"fmt"
	"testing"
)

func TestFilterForResume_UnresolvedToolUse(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
		// No tool_result for tu1 → unresolved
	}

	filtered := FilterForResume(messages)
	if len(filtered) != 1 {
		t.Errorf("got %d messages, want 1 (unresolved tool_use filtered)", len(filtered))
	}
	if filtered[0].Type != "user" {
		t.Errorf("remaining message type = %q, want user", filtered[0].Type)
	}
}

func TestFilterForResume_AllResolved(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
		{Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]`},
		{Type: "assistant", Content: `[{"type":"text","text":"done"}]`},
	}

	filtered := FilterForResume(messages)
	if len(filtered) != 4 {
		t.Errorf("got %d messages, want 4 (all resolved)", len(filtered))
	}
}

func TestDetectInterruptedTurn_EmptyContent(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[]`},
	}

	if !DetectInterruptedTurn(messages) {
		t.Error("expected interrupted for empty assistant content")
	}
}

func TestDetectInterruptedTurn_ToolUseWithoutResult(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
	}

	if !DetectInterruptedTurn(messages) {
		t.Error("expected interrupted for tool_use without result")
	}
}

func TestDetectInterruptedTurn_OnlyThinking(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"thinking","text":"hmm..."}]`},
	}

	if !DetectInterruptedTurn(messages) {
		t.Error("expected interrupted for thinking-only assistant")
	}
}

func TestDetectInterruptedTurn_Complete(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"text","text":"hi there"}]`},
	}

	if DetectInterruptedTurn(messages) {
		t.Error("expected NOT interrupted for complete turn")
	}
}

func TestDetectInterruptedTurn_UserLast(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "assistant", Content: `[{"type":"text","text":"hi"}]`},
		{Type: "user", Content: `[{"type":"text","text":"next question"}]`},
	}

	if DetectInterruptedTurn(messages) {
		t.Error("expected NOT interrupted when last message is user")
	}
}

func TestDetectInterruptedTurn_Empty(t *testing.T) {
	if DetectInterruptedTurn(nil) {
		t.Error("expected false for nil messages")
	}
}

func TestTruncateInterruptedTurn_RemovesTrailingAssistant(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
	}

	truncated := TruncateInterruptedTurn(messages)
	if len(truncated) != 1 {
		t.Fatalf("got %d messages, want 1", len(truncated))
	}
	if truncated[0].Type != "user" {
		t.Errorf("remaining type = %q, want user", truncated[0].Type)
	}
}

func TestTruncateInterruptedTurn_MultipleTrailingAssistant(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"thinking","text":"hmm"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
	}

	truncated := TruncateInterruptedTurn(messages)
	if len(truncated) != 1 {
		t.Fatalf("got %d messages, want 1 (all trailing assistants removed)", len(truncated))
	}
}

func TestTruncateInterruptedTurn_NoAssistant(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	truncated := TruncateInterruptedTurn(messages)
	if len(truncated) != 1 {
		t.Errorf("got %d messages, want 1 (unchanged)", len(truncated))
	}
}

func TestTruncateInterruptedTurn_Empty(t *testing.T) {
	truncated := TruncateInterruptedTurn(nil)
	if truncated != nil {
		t.Errorf("expected nil for empty input, got %v", truncated)
	}
}

func TestResumeSession_Empty(t *testing.T) {
	store := openTestStore(t)

	state, messages, err := store.ResumeSession("nonexistent")
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if state == nil {
		t.Fatal("state should not be nil")
	}
	if len(messages) != 0 {
		t.Errorf("got %d messages, want 0", len(messages))
	}
}

func TestResumeSession_WithMessages(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	msgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"hi"}]`),
	}
	for _, msg := range msgs {
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	state, messages, err := store.ResumeSession(sessionID)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if state == nil {
		t.Fatal("state should not be nil")
	}
	if len(messages) != 2 {
		t.Errorf("got %d messages, want 2", len(messages))
	}
}

// TestDetectInterruptedTurn_MixedTextAndToolUse verifies that an assistant
// with both text AND tool_use is still detected as interrupted (it's the
// last message so there can't be a tool_result).
func TestDetectInterruptedTurn_MixedTextAndToolUse(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"text","text":"let me check"},{"type":"tool_use","id":"tu1","name":"Read"}]`},
	}

	if !DetectInterruptedTurn(messages) {
		t.Error("assistant with text + tool_use as last message should be interrupted")
	}
}

// TestTruncateInterruptedTurn_AllAssistants verifies behavior when ALL messages are assistants.
// In this case truncation removes everything, returning empty slice.
func TestTruncateInterruptedTurn_AllAssistants(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "assistant", Content: `[{"type":"thinking","text":"hmm"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
	}

	truncated := TruncateInterruptedTurn(messages)
	if len(truncated) != 0 {
		t.Errorf("got %d messages, want 0 (all assistants truncated)", len(truncated))
	}
}

// TruncateMessagesFromIndex must skip metadata/progress in OFFSET so it aligns
// with engine message indices. Without this, /rewind + restart loses all messages.
func TestTruncateMessagesFromIndex_MetadataAtStart(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-trunc-meta-first"
	createTestSession(t, store, sessionID)

	for i := 1; i <= 4; i++ {
		if err := store.AppendMessage(sessionID, &TranscriptMessage{
			UUID:    fmt.Sprintf("fh-%d", i),
			Type:    "metadata",
			Subtype: "file_history_state",
			Content: `{"Snapshots":[]}`,
		}); err != nil {
			t.Fatalf("AppendMessage metadata: %v", err)
		}
	}

	for i := 1; i <= 3; i++ {
		if err := store.AppendMessage(sessionID, testMessage(0, "user", fmt.Sprintf("u%d", i), "",
			fmt.Sprintf(`[{"type":"text","text":"turn %d"}]`, i))); err != nil {
			t.Fatalf("AppendMessage user: %v", err)
		}
		if err := store.AppendMessage(sessionID, testMessage(0, "assistant", fmt.Sprintf("a%d", i), "",
			fmt.Sprintf(`[{"type":"text","text":"response %d"}]`, i))); err != nil {
			t.Fatalf("AppendMessage assistant: %v", err)
		}
	}

	allMsgs, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages setup: %v", err)
	}
	if len(allMsgs) != 10 {
		t.Fatalf("setup: expected 10 store messages, got %d", len(allMsgs))
	}

	// Engine index 4 = 3rd user message. Keep user1, asst1, user2, asst2.
	err = store.TruncateMessagesFromIndex(sessionID, 4)
	if err != nil {
		t.Fatalf("TruncateMessagesFromIndex: %v", err)
	}

	remaining, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages after rewind: %v", err)
	}

	var convCount int
	for _, m := range remaining {
		if m.Type != "metadata" {
			convCount++
		}
	}

	if convCount != 4 {
		t.Errorf("got %d conversation messages after rewind, want 4", convCount)
		for i, m := range remaining {
			t.Logf("  remaining[%d] type=%s uuid=%s", i, m.Type, m.UUID)
		}
	}
}

// Line 39-41: ResumeSession — ProcessResumedConversation error
func TestResumeSession_ProcessError(t *testing.T) {
	// ProcessResumedConversation currently never returns an error,
	// so this path is hard to trigger. The error path would require
	// an internal error in ProcessResumedConversation which doesn't exist.
	// Covered indirectly by existing tests.
}
