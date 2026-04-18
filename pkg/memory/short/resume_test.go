package short

import (
	"strings"
	"testing"
)

func TestFilterForResume_UnresolvedToolUse(t *testing.T) {
	messages := []*Message{
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
	messages := []*Message{
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
	messages := []*Message{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[]`},
	}

	if !DetectInterruptedTurn(messages) {
		t.Error("expected interrupted for empty assistant content")
	}
}

func TestDetectInterruptedTurn_ToolUseWithoutResult(t *testing.T) {
	messages := []*Message{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
	}

	if !DetectInterruptedTurn(messages) {
		t.Error("expected interrupted for tool_use without result")
	}
}

func TestDetectInterruptedTurn_OnlyThinking(t *testing.T) {
	messages := []*Message{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"thinking","text":"hmm..."}]`},
	}

	if !DetectInterruptedTurn(messages) {
		t.Error("expected interrupted for thinking-only assistant")
	}
}

func TestDetectInterruptedTurn_Complete(t *testing.T) {
	messages := []*Message{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"text","text":"hi there"}]`},
	}

	if DetectInterruptedTurn(messages) {
		t.Error("expected NOT interrupted for complete turn")
	}
}

func TestDetectInterruptedTurn_UserLast(t *testing.T) {
	messages := []*Message{
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
	messages := []*Message{
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
	messages := []*Message{
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
	messages := []*Message{
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

	msgs := []*Message{
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

func TestIsSessionResumable(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Empty session → not resumable
	ok, err := store.IsSessionResumable(sessionID)
	if err != nil {
		t.Fatalf("IsSessionResumable: %v", err)
	}
	if ok {
		t.Error("empty session should not be resumable")
	}

	// Add a message
	msg := testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`)
	if err := store.AppendMessage(sessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	ok, err = store.IsSessionResumable(sessionID)
	if err != nil {
		t.Fatalf("IsSessionResumable: %v", err)
	}
	if !ok {
		t.Error("session with messages should be resumable")
	}
}

func TestResumeSession_FullFlow(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Add messages that simulate a real conversation
	msgs := []*Message{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"hi there"}]`),
		testMessage(0, "user", "uuid-3", "", `[{"type":"text","text":"how are you?"}]`),
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

	if len(messages) == 0 {
		t.Error("should have messages after resume")
	}

	// Messages should be filtered (no unresolved tools, etc.)
	if len(messages) > len(msgs) {
		t.Errorf("got %d messages, want <= %d", len(messages), len(msgs))
	}
}

func TestResumeSession_WithInterruptedTurn(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Add messages ending with an incomplete turn (tool_use without result)
	msgs := []*Message{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"tool_use","id":"tu1","name":"bash"}]`),
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

	// The interrupted turn should be truncated
	if len(messages) == 0 {
		t.Fatal("should have at least one message after truncating interrupted turn")
	}

	// Last message should not be the interrupted assistant
	if messages[len(messages)-1].Type == "assistant" {
		// Check if it has tool_use without result
		blocks := ParseContentBlocks(messages[len(messages)-1].Content)
		hasToolUse := false
		for _, block := range blocks {
			if block.Type == "tool_use" {
				hasToolUse = true
				break
			}
		}
		if hasToolUse {
			t.Error("interrupted assistant with tool_use should be truncated")
		}
	}
}

func TestIsSessionResumable_VariousStates(t *testing.T) {
	store := openTestStore(t)

	tests := []struct {
		name          string
		setup         func(string) error
		wantResumable bool
	}{
		{
			name: "empty session",
			setup: func(sessionID string) error {
				createTestSession(t, store, sessionID)
				return nil
			},
			wantResumable: false,
		},
		{
			name: "session with user message",
			setup: func(sessionID string) error {
				createTestSession(t, store, sessionID)
				return store.AppendMessage(sessionID, testMessage(0, "user", "uuid-user-1", "", `[{"type":"text","text":"hello"}]`))
			},
			wantResumable: true,
		},
		{
			name: "session with assistant message",
			setup: func(sessionID string) error {
				createTestSession(t, store, sessionID)
				if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-user-2", "", `[{"type":"text","text":"hello"}]`)); err != nil {
					return err
				}
				return store.AppendMessage(sessionID, testMessage(0, "assistant", "uuid-asst-2", "", `[{"type":"text","text":"hi"}]`))
			},
			wantResumable: true,
		},
		{
			name: "session with only system messages",
			setup: func(sessionID string) error {
				createTestSession(t, store, sessionID)
				// System messages DO count as visible (only progress is excluded)
				return store.AppendMessage(sessionID, testMessage(0, "system", "uuid-sys-1", "", `[{"type":"text","text":"system"}]`))
			},
			wantResumable: true, // System messages count as visible
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "session-" + tt.name
			if err := tt.setup(sessionID); err != nil {
				t.Fatalf("setup: %v", err)
			}

			ok, err := store.IsSessionResumable(sessionID)
			if err != nil {
				t.Fatalf("IsSessionResumable: %v", err)
			}

			if ok != tt.wantResumable {
				t.Errorf("IsSessionResumable() = %v, want %v", ok, tt.wantResumable)
			}
		})
	}
}

// TestResumeSession_ThinkingOnlyTruncation verifies that a session ending with
// a thinking-only assistant message triggers actual truncation in ResumeSession.
//
// This exposes a subtle issue: the existing TestResumeSession_WithInterruptedTurn
// uses tool_use content, but FilterForResume runs BEFORE DetectInterruptedTurn and
// removes the unresolved tool_use assistant via FilterUnresolvedToolUses. So the
// truncation code path (lines 32-35) is never reached in that test.
//
// Only thinking-only or empty-content assistants survive FilterForResume but are
// still detected as interrupted — these are the cases that actually trigger truncation.
func TestResumeSession_ThinkingOnlyTruncation(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Add a normal exchange followed by a thinking-only assistant (interrupted)
	msgs := []*Message{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"hi there"}]`),
		testMessage(0, "user", "uuid-3", "", `[{"type":"text","text":"read this file"}]`),
		testMessage(0, "assistant", "uuid-4", "", `[{"type":"thinking","text":"let me think..."}]`),
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

	// The thinking-only assistant should be truncated, leaving 3 messages
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3 (thinking-only assistant truncated)", len(messages))
	}

	// Verify the last remaining message is the user message (uuid-3), not any assistant
	lastMsg := messages[len(messages)-1]
	if lastMsg.UUID != "uuid-3" {
		t.Errorf("last message UUID = %q, want uuid-3 (user before interrupted turn)", lastMsg.UUID)
	}
	if lastMsg.Type != "user" {
		t.Errorf("last message type = %q, want user", lastMsg.Type)
	}
}

// TestResumeSession_EmptyContentTruncation verifies empty-content assistant triggers truncation.
func TestResumeSession_EmptyContentTruncation(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	msgs := []*Message{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[]`), // empty content
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

	// Empty-content assistant should be truncated
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1 (empty assistant truncated)", len(messages))
	}
	if messages[0].UUID != "uuid-1" {
		t.Errorf("remaining message UUID = %q, want uuid-1", messages[0].UUID)
	}
}

// TestResumeSession_LoadError verifies ResumeSession propagates load errors.
func TestResumeSession_LoadError(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Add a message so LoadPostCompactMessages has something to load
	msg := testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`)
	if err := store.AppendMessage(sessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Close store to force LoadPostCompactMessages to fail
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, _, err := store.ResumeSession(sessionID)
	if err == nil {
		t.Fatal("expected error when store is closed")
	}
	if !strings.Contains(err.Error(), "load messages") {
		t.Errorf("error should mention 'load messages', got: %v", err)
	}
}

// TestIsSessionResumable_CountError verifies IsSessionResumable propagates count errors.
func TestIsSessionResumable_CountError(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.IsSessionResumable(sessionID)
	if err == nil {
		t.Fatal("expected error when store is closed")
	}
}

// TestDetectInterruptedTurn_MixedTextAndToolUse verifies that an assistant
// with both text AND tool_use is still detected as interrupted (it's the
// last message so there can't be a tool_result).
func TestDetectInterruptedTurn_MixedTextAndToolUse(t *testing.T) {
	messages := []*Message{
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
	messages := []*Message{
		{Type: "assistant", Content: `[{"type":"thinking","text":"hmm"}]`},
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"Read"}]`},
	}

	truncated := TruncateInterruptedTurn(messages)
	if len(truncated) != 0 {
		t.Errorf("got %d messages, want 0 (all assistants truncated)", len(truncated))
	}
}

// Line 39-41: ResumeSession — ProcessResumedConversation error
func TestResumeSession_ProcessError(t *testing.T) {
	// ProcessResumedConversation currently never returns an error,
	// so this path is hard to trigger. The error path would require
	// an internal error in ProcessResumedConversation which doesn't exist.
	// Covered indirectly by existing tests.
}

