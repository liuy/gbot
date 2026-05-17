package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// testTime is a fixed timestamp for deterministic tests.
var testTime = time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

func newTestEngine() *engine.Engine {
	return engine.New(&engine.Params{
		Logger: slog.Default(),
	})
}

func TestMessagesAfterAreOnlySynthetic_Empty(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	}
	if !messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected true: no messages after user message")
	}
}

func TestMessagesAfterAreOnlySynthetic_OnlyThinking(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: "thinking..."},
		}},
	}
	if !messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected true: only thinking blocks after user message")
	}
}

func TestMessagesAfterAreOnlySynthetic_InterruptMsg(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("[Request interrupted by user]"),
		}},
	}
	// Interrupt message is synthetic (matches types.InterruptMessage),
	// so it should be skipped → returns true (no meaningful content after).
	// Source: TS isSyntheticMessage checks SYNTHETIC_MESSAGES set.
	if !messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected true: interrupt message is synthetic (non-meaningful)")
	}
}

func TestMessagesAfterAreOnlySynthetic_AssistantText(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("Hello! How can I help?"),
		}},
	}
	if messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected false: assistant has non-empty text")
	}
}

func TestMessagesAfterAreOnlySynthetic_ToolUse(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("read file")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("tu_1", "Read", nil),
		}},
	}
	if messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected false: assistant has tool_use block")
	}
}

func TestMessagesAfterAreOnlySynthetic_AnotherUser(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: "..."},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("another")}},
	}
	if messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected false: another user message with text is meaningful")
	}
}

func TestMessagesAfterAreOnlySynthetic_Mixed(t *testing.T) {
	// thinking + empty assistant → true
	msgs1 := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: "deep thoughts"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock(""), // empty text
		}},
	}
	if !messagesAfterAreOnlySynthetic(msgs1, 0) {
		t.Error("expected true: only thinking + empty text")
	}

	// thinking + text → false
	msgs2 := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: "deep thoughts"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("actual response"),
		}},
	}
	if messagesAfterAreOnlySynthetic(msgs2, 0) {
		t.Error("expected false: has non-empty text after thinking")
	}
}

func TestMessagesAfterAreOnlySynthetic_ToolResultUser(t *testing.T) {
	// User message with only tool_result blocks → synthetic (skip)
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("tu_1", "Read", nil),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("tu_1", nil, false),
		}},
	}
	// The tool_use at index 1 makes it non-synthetic → false
	if messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected false: assistant has tool_use before tool_result user msg")
	}

	// But checking from after the tool_use: only tool_result user → synthetic
	if !messagesAfterAreOnlySynthetic(msgs, 2) {
		t.Error("expected true: only tool_result user message after index 2")
	}
}

func TestLastSelectableUserMessageIndex(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second")}},
	}
	if got := lastSelectableUserMessageIndex(msgs); got != 2 {
		t.Errorf("expected index 2, got %d", got)
	}
	if got := lastSelectableUserMessageIndex(msgs[:1]); got != 0 {
		t.Errorf("expected index 0, got %d", got)
	}
	if got := lastSelectableUserMessageIndex(nil); got != -1 {
		t.Errorf("expected -1 for nil, got %d", got)
	}

	// Tool_result-only user messages should be skipped
	toolResultMsgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("user query")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewToolUseBlock("tu1", "Read", json.RawMessage(`{}`))}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewToolResultBlock("tu1", json.RawMessage(`"data"`), false)}},
	}
	if got := lastSelectableUserMessageIndex(toolResultMsgs); got != 0 {
		t.Errorf("expected index 0 (skip tool_result msg), got %d", got)
	}

	// Synthetic messages (interrupt text) should be skipped
	syntheticMsgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("user query")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(types.InterruptMessage)}},
	}
	if got := lastSelectableUserMessageIndex(syntheticMsgs); got != 0 {
		t.Errorf("expected index 0 (skip synthetic msg), got %d", got)
	}
}

func TestFirstTextBlockContent(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("hello world"),
		},
	}
	if got := firstTextBlockContent(msg); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}

	emptyMsg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewToolResultBlock("tu_1", nil, false)},
	}
	if got := firstTextBlockContent(emptyMsg); got != "" {
		t.Errorf("expected empty string for no text block, got %q", got)
	}
}

func TestIsSelectableUserMessage_FlagMeta(t *testing.T) {
	// Source: TS selectableUserMessagesFilter line 777 — message.isMeta
	// Skill messages are marked FlagMeta and should NOT appear in rewind list.
	metaMsg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<command-message>roast</command-message>"),
		},
		Flags: types.FlagMeta,
	}
	if isSelectableUserMessage(metaMsg) {
		t.Error("expected FlagMeta message to be filtered out of rewind selection")
	}

	// Non-meta user message should still be selectable
	normalMsg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("你好呀"),
		},
	}
	if !isSelectableUserMessage(normalMsg) {
		t.Error("expected normal user message to be selectable")
	}
}

func TestIsSelectableUserMessage_MessageTypeAttachment(t *testing.T) {
	attMsg := types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content: []types.ContentBlock{
			types.NewTextBlock("<system-reminder>\njob done\n</system-reminder>"),
		},
	}
	if isSelectableUserMessage(attMsg) {
		t.Error("expected MessageTypeAttachment message to be filtered out of rewind selection")
	}
}

func TestIsSelectableUserMessage_NonUserTags(t *testing.T) {
	// Source: TS selectableUserMessagesFilter line 787-790
	// Messages containing terminal/command output tags should be filtered.
	cases := []struct {
		name    string
		text    string
		want    bool
	}{
		{"local-command-stdout", "<local-command-stdout>output</local-command-stdout>", false},
		{"local-command-stderr", "<local-command-stderr>error</local-command-stderr>", false},
		{"bash-stdout", "<bash-stdout>result</bash-stdout>", false},
		{"bash-stderr", "<bash-stderr>err</bash-stderr>", false},
		{"tick", "<tick>heartbeat</tick>", false},
		{"teammate-message", "<teammate-message>hello</teammate-message>", false},
		{"job-notification", "<job-notification>done</job-notification>", false},
		{"normal text", "用户正常消息", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := types.Message{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock(tc.text)},
			}
			got := isSelectableUserMessage(msg)
			if got != tc.want {
				t.Errorf("isSelectableUserMessage(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestLastSelectableUserMessageIndex_SkipsMetaMessages(t *testing.T) {
	// Skill messages (FlagMeta) should not appear in rewind list.
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("你好呀")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("<command-message>roast</command-message>")}, Flags: types.FlagMeta},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("/roast 找2个文件吐槽下")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("You are a ruthless senior engineer...")}, Flags: types.FlagMeta},
	}
	// Should select index 2 (the user's actual input), skipping meta messages
	if got := lastSelectableUserMessageIndex(msgs); got != 2 {
		t.Errorf("expected index 2, got %d", got)
	}
}

func TestTryAutoRewind_NoMeaningfulResponse(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: "thinking..."},
		}, Timestamp: testTime},
	})

	a := &App{
		engine:         eng,
		input:          NewInput(),
		history:        NewHistory(""),
		committedCount: 0,
		repl:           NewReplState(),
	}
	// Simulate the user message was added to history
	a.history.Add("hello")
	// Simulate TUI has committed the previous messages (none in this case)
	a.committedCount = 0

	result := a.tryAutoRewind()

	if !result {
		t.Fatal("expected auto-rewind to fire")
	}

	// Engine messages should be truncated to just the user message
	engMsgs := eng.Messages()
	if len(engMsgs) != 0 {
		t.Fatalf("expected 0 engine messages after rewind, got %d", len(engMsgs))
	}

	// Input should be restored
	if a.input.Value() != "hello" {
		t.Errorf("expected input 'hello', got %q", a.input.Value())
	}

	// History entry should be removed
	if len(a.history.items) != 0 {
		t.Errorf("expected empty history, got %d items", len(a.history.items))
	}
}

func TestTryAutoRewind_SyncsStore(t *testing.T) {
	// Auto-rewind must truncate store to prevent orphaned messages on resume.
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eng := newTestEngine()
	session, err := store.CreateSession(dir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)

	// Simulate: user sent "hello", assistant responded with thinking only,
	// and persistTurn already wrote both to store.
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: "thinking..."},
		}, Timestamp: testTime},
	}
	eng.SetStore(store, "")
	eng.SetMessages(msgs)

	a := &App{
		engine:           eng,
		input:            NewInput(),
		history:          NewHistory(""),
		committedCount:   0,
		repl:             NewReplState(),
		sessionID:        session.SessionID,
	}
	a.history.Add("hello")

	// Persist messages to store (simulating persistTurn before ESC)
	a.engine.PersistNewMessages()
	if a.engine.LastPersistedIdx() != 2 {
		t.Fatalf("setup: expected lastPersistedIdx=2, got %d", a.engine.LastPersistedIdx())
	}

	// Verify store has the messages
	storeMsgsBefore, _ := store.LoadMessages(session.SessionID)
	if len(storeMsgsBefore) != 2 {
		t.Fatalf("setup: expected 2 store messages, got %d", len(storeMsgsBefore))
	}

	// Now auto-rewind (simulating ESC cancel with no meaningful response)
	result := a.tryAutoRewind()
	if !result {
		t.Fatal("expected auto-rewind to fire")
	}

	// Store retains all messages (append-only)
	storeMsgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(storeMsgs) == 0 {
		t.Fatal("expected store to retain messages after auto-rewind (append-only)")
	}

	// lastPersistedIdx should be synced from engine (rewound to 0)
	if a.engine.LastPersistedIdx() != 0 {
		t.Errorf("expected engine lastPersistedIdx=0 after auto-rewind, got %d", a.engine.LastPersistedIdx())
	}
	if a.engine.LastPersistedIdx() != 0 {
		t.Errorf("expected TUI lastPersistedIdx=0 after auto-rewind, got %d", a.engine.LastPersistedIdx())
	}
}

func TestTryAutoRewind_HasMeaningfulResponse(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("Hello! How can I help?"),
		}, Timestamp: testTime},
	})

	a := &App{
		engine:         eng,
		input:          NewInput(),
		history:        NewHistory(""),
		committedCount: 0,
		repl:           NewReplState(),
	}
	a.history.Add("hello")

	result := a.tryAutoRewind()

	if result {
		t.Error("expected auto-rewind NOT to fire (has meaningful response)")
	}

	// Messages should be unchanged
	engMsgs := eng.Messages()
	if len(engMsgs) != 2 {
		t.Fatalf("expected 2 engine messages, got %d", len(engMsgs))
	}
}

func TestHandleRewind_Aborted(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("bye")}, Timestamp: testTime},
	})

	a := &App{
		engine:         eng,
		input:          NewInput(),
		repl:           NewReplState(),
		committedCount: 0,
	}
	a.width = 80

	// Call handleRewind — it sets up the dialog
	_ = a.handleRewind(nil)

	if a.activeDialog == nil {
		t.Fatal("expected dialog to be created")
	}

	// Simulate abort (ESC)
	a.activeDialog.aborted = true
	model, _ := a.onDialogDone(a.activeDialog)
	_ = model.(*App)

	// Messages should be unchanged
	engMsgs := eng.Messages()
	if len(engMsgs) != 3 {
		t.Fatalf("expected 3 messages after abort, got %d", len(engMsgs))
	}
}

func TestHandleRewind_Selected(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("bye")}, Timestamp: testTime},
	})

	a := &App{
		engine:         eng,
		input:          NewInput(),
		repl:           NewReplState(),
		committedCount: 0,
	}
	a.width = 80

	_ = a.handleRewind(nil)

	if a.activeDialog == nil {
		t.Fatal("expected dialog to be created")
	}

	// Select the first user message (index 0)
	// Dialog options are: user message 0, user message 2
	// Selecting option 0 → index 0
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Engine messages truncated — selected message excluded (matches TS slice(0, messageIndex))
	engMsgs := eng.Messages()
	if len(engMsgs) != 0 {
		t.Fatalf("expected 0 messages after rewind (selected msg excluded), got %d", len(engMsgs))
	}

	// TUI messages should be reset
	if len(app.repl.messages) != 0 {
		t.Errorf("expected 0 TUI messages, got %d", len(app.repl.messages))
	}
	if app.committedCount != 0 {
		t.Errorf("expected committedCount=0, got %d", app.committedCount)
	}

	// Input should be restored with selected message text
	// Source: TS restoreMessageSync → textForResubmit → setInputValue
	if app.input.Value() != "hello" {
		t.Errorf("expected input 'hello' (selected message text), got %q", app.input.Value())
	}
}

func TestHandleRewind_DuringStreaming(t *testing.T) {
	eng := newTestEngine()
	a := &App{
		engine: eng,
		repl:   NewReplState(),
	}
	a.repl.streaming = true

	_ = a.handleRewind(nil)

	// Should return nil (no dialog created)
	if a.activeDialog != nil {
		t.Error("expected no dialog during streaming")
	}
}

func TestMessagesAfterAreOnlySynthetic_ToolResultUserOnly(t *testing.T) {
	// User message with only tool_result blocks after fromIndex → synthetic
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("tu_1", nil, false),
		}},
	}
	if !messagesAfterAreOnlySynthetic(msgs, 0) {
		t.Error("expected true: tool-result-only user message is synthetic")
	}
}

func TestHasNonToolResultContent_AllToolResults(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewToolResultBlock("tu_1", nil, false),
			types.NewToolResultBlock("tu_2", nil, false),
		},
	}
	if hasNonToolResultContent(msg) {
		t.Error("expected false: all blocks are tool_result")
	}
}

func TestHasNonToolResultContent_HasText(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewToolResultBlock("tu_1", nil, false),
			types.NewTextBlock("hello"),
		},
	}
	if !hasNonToolResultContent(msg) {
		t.Error("expected true: has text block alongside tool_result")
	}
}

func TestTryAutoRewind_CommittedCountExceedsMessages(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
	})

	a := &App{
		engine:         eng,
		input:          NewInput(),
		history:        NewHistory(""),
		repl:           NewReplState(),
		committedCount: 5, // exceeds len(repl.messages)
	}
	a.history.Add("hello")

	if !a.tryAutoRewind() {
		t.Fatal("expected auto-rewind to fire")
	}

	// committedCount should be clamped to 0
	if a.committedCount != 0 {
		t.Errorf("committedCount = %d, want 0", a.committedCount)
	}
}

func TestHandleRewind_NoUserMessages(t *testing.T) {
		eng := newTestEngine()
		eng.SetMessages([]types.Message{
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		})

		a := &App{
			engine: eng,
			input:  NewInput(),
			repl:   NewReplState(),
		}
		a.width = 80

		_ = a.handleRewind(nil)

		if a.activeDialog == nil {
			t.Fatal("expected dialog showing 'Nothing to rewind to yet.'")
		}
		if a.activeDialog.title != "Nothing to rewind to yet." {
			t.Errorf("dialog title = %q, want %q", a.activeDialog.title, "Nothing to rewind to yet.")
		}
	}

func TestHandleRewind_UserWithNoText(t *testing.T) {
		eng := newTestEngine()
		eng.SetMessages([]types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{
				types.NewToolResultBlock("tu_1", nil, false),
			}, Timestamp: testTime},
		})

		a := &App{
			engine: eng,
			input:  NewInput(),
			repl:   NewReplState(),
		}
		a.width = 80

		_ = a.handleRewind(nil)

		if a.activeDialog == nil {
			t.Fatal("expected dialog showing 'Nothing to rewind to yet.'")
		}
		if a.activeDialog.title != "Nothing to rewind to yet." {
			t.Errorf("dialog title = %q, want %q", a.activeDialog.title, "Nothing to rewind to yet.")
		}
	}

func TestHandleRewind_SyntheticInterruptSkipped(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("do something"),
		}, Timestamp: testTime, ID: "uuid-1"},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("working..."),
		}, Timestamp: testTime, ID: "uuid-2"},
		// Synthetic interrupt message — should NOT appear in rewind dialog
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(types.InterruptMessage),
		}, Timestamp: testTime, ID: "uuid-3"},
	})

	a := &App{
		engine: eng,
		input:  NewInput(),
		repl:   NewReplState(),
	}
	a.width = 80

	_ = a.handleRewind(nil)

	if a.activeDialog == nil {
		t.Fatal("expected dialog with real user message")
	}
	// Should have only 1 option (the real user message), not the interrupt
	if len(a.activeDialog.options) != 1 {
		t.Fatalf("expected 1 option (interrupt filtered), got %d", len(a.activeDialog.options))
	}
}

func TestHandleRewind_CompactBoundarySkipped(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(`{"compactMetadata":{"trigger":"auto","preTokens":191059}}`),
		}, Flags: types.FlagCompactSummary, Timestamp: testTime, ID: "uuid-compact"},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("real user message"),
		}, Timestamp: testTime, ID: "uuid-real"},
	})

	a := &App{engine: eng, input: NewInput(), repl: NewReplState()}
	a.width = 80
	_ = a.handleRewind(nil)

	if a.activeDialog == nil {
		t.Fatal("expected dialog")
	}
	if len(a.activeDialog.options) != 1 {
		t.Fatalf("expected 1 option (compact filtered), got %d", len(a.activeDialog.options))
	}
}

func TestHandleRewind_JobNotificationSkipped(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(`<job-notification><job-id>bg-1</job-id><status>completed</status></job-notification>`),
		}, Timestamp: testTime, ID: "uuid-task"},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("real user message"),
		}, Timestamp: testTime, ID: "uuid-real"},
	})

	a := &App{engine: eng, input: NewInput(), repl: NewReplState()}
	a.width = 80
	_ = a.handleRewind(nil)

	if a.activeDialog == nil {
		t.Fatal("expected dialog")
	}
	if len(a.activeDialog.options) != 1 {
		t.Fatalf("expected 1 option (job-notification filtered), got %d", len(a.activeDialog.options))
	}
}

func TestHandleRewind_SessionResumeSkipped(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("This session is being continued from a previous conversation that ran out of context."),
		}, Flags: types.FlagCompactSummary, Timestamp: testTime, ID: "uuid-resume"},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("real user message"),
		}, Timestamp: testTime, ID: "uuid-real"},
	})

	a := &App{engine: eng, input: NewInput(), repl: NewReplState()}
	a.width = 80
	_ = a.handleRewind(nil)

	if a.activeDialog == nil {
		t.Fatal("expected dialog")
	}
	if len(a.activeDialog.options) != 1 {
		t.Fatalf("expected 1 option (session resume filtered), got %d", len(a.activeDialog.options))
	}
}

func TestHandleRewind_WithStoreTruncation(t *testing.T) {
	eng := newTestEngine()

	// Set up file history
	tracker := filehistory.NewTracker(t.TempDir())
	eng.SetFileHistory(tracker)

	// Set up store
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Create session and persist messages
	sessionID := "test-session"
	if _, err := store.DB().Exec(
		"INSERT INTO sessions (session_id, project_dir, model) VALUES (?, '', '')",
		sessionID,
	); err != nil {
		t.Fatalf("create session: %v", err)
	}

	engMsgs := []types.Message{
		{ID: "uuid-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response1")}, Timestamp: testTime},
		{ID: "uuid-2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response2")}, Timestamp: testTime},
	}
	eng.SetMessages(engMsgs)

	// Persist all messages to store
	for i, msg := range engMsgs {
		tm := &short.TranscriptMessage{
			UUID:    fmt.Sprintf("uuid-%d", i),
			Type:    string(msg.Role),
			Content: fmt.Sprintf(`[{"type":"text","text":"%s"}]`, firstTextBlockContent(msg)),
		}
		if err := store.AppendMessage(sessionID, tm); err != nil {
			t.Fatalf("AppendMessage %d: %v", i, err)
		}
	}

	// Create a backup for file restore using snapshot API.
	// File = "original" on disk → TrackEdit captures v1 backup of "original".
	// MakeSnapshot records the state. Then modify AFTER snapshot.
	tmpFile := filepath.Join(t.TempDir(), "test.go")
	if err := os.WriteFile(tmpFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tracker.TrackEdit(tmpFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("uuid-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	if err := os.WriteFile(tmpFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{
		engine:           eng,
		input:            NewInput(),
		repl:             NewReplState(),
		sessionID:        sessionID,
		fileHistory:      tracker,
	}
	a.width = 80

	_ = a.handleRewind(nil)
	if a.activeDialog == nil {
		t.Fatal("expected dialog")
	}

	// Select first user message (index 0)
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

		// Scope dialog appears because fileHistory has backups
		if app.activeDialog != nil {
			app.activeDialog.done = true
			app.activeDialog.cursor = 0 // "Restore code and conversation"
			model, _ = app.onDialogDone(app.activeDialog)
			app = model.(*App)
		}

	// Store retains all messages (append-only)
	remaining, _ := store.LoadMessages(sessionID)
	if len(remaining) == 0 {
		t.Error("expected store to retain messages after rewind (append-only)")
	}
	if app.engine.LastPersistedIdx() != 0 {
		t.Errorf("lastPersistedIdx = %d, want 0", app.engine.LastPersistedIdx())
	}

	// Verify file was restored
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("file = %q, want %q", string(data), "original")
	}
}

func TestHandleRewind_RewindToError(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second")}, Timestamp: testTime},
	})

	a := &App{
		engine:         eng,
		input:          NewInput(),
		repl:           NewReplState(),
		committedCount: 0,
	}
	a.width = 80

	_ = a.handleRewind(nil)
	if a.activeDialog == nil {
		t.Fatal("expected dialog")
	}

	// Select second user message (cursor=1 -> indices[1]=2)
	// Then clear engine messages so RewindTo(2) fails (2 > 0)
	eng.SetMessages(nil)

	a.activeDialog.done = true
	a.activeDialog.cursor = 1
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Should not crash; input empty because RewindTo failed
	if app.input.Value() != "" {
		t.Errorf("expected empty input after RewindTo error, got %q", app.input.Value())
	}
}

// --- Second dialog tests (scope selection) ---

// TestHandleRewind_NoFileChanges_SkipsScopeDialog verifies that when no
// fileHistory is set, rewind behaves as before (no scope dialog).
func TestHandleRewind_NoFileChanges_SkipsScopeDialog(t *testing.T) {
	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("bye")}, Timestamp: testTime},
	})

	a := &App{
		engine:         eng,
		input:          NewInput(),
		repl:           NewReplState(),
		committedCount: 0,
	}
	a.width = 80

	_ = a.handleRewind(nil)

	// Select first user message (index 0)
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// No fileHistory set -> no scope dialog, direct RewindAll
	engMsgs := eng.Messages()
	if len(engMsgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(engMsgs))
	}
	if app.input.Value() != "hello" {
		t.Errorf("input = %q, want %q", app.input.Value(), "hello")
	}
}

// setupRewindWithFileChanges creates a test app with engine, tracker, and file edit
// ready for the first dialog callback.
func setupRewindWithFileChanges(t *testing.T) (*App, string) {
	t.Helper()
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "test.go")
	if err := os.WriteFile(testFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	eng := newTestEngine()
	eng.SetFileHistory(tracker)
	eng.SetMessages([]types.Message{
		{ID: "uid-hello", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{ID: "uid-edit", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit")}, Timestamp: testTime},
	})

	// Use snapshot API:
	// RewindTo(2) → rewindSnapshotID="0" (last user in messages[:2] is index 0).
	// HasChangesAtMessage uses msgs[2].ID → fallback "2".
	//
	// We need:
	//   - Snapshot "0" with "original" backup → Rewind("0") restores to original
	//   - Snapshot "2" with "original" backup → HasChangesAtMessage("2") detects disk differs
	//
	// Setup: TrackEdit captures "original" as v1, MakeSnapshot("0") records it.
	// MakeSnapshot("2") reuses same backup (file unchanged). Then modify AFTER.
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("uid-hello"); err != nil {
		t.Fatalf("MakeSnapshot 0: %v", err)
	}
	if err := tracker.MakeSnapshot("uid-edit"); err != nil {
		t.Fatalf("MakeSnapshot 2: %v", err)
	}
	_ = os.WriteFile(testFile, []byte("modified"), 0o644)

	a := &App{
		engine:         eng,
		input:          NewInput(),
		repl:           NewReplState(),
		committedCount: 0,
		fileHistory:    tracker,
	}
	a.width = 80

	// First dialog: select second user message (cursor=1 -> index 2)
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 1
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	return app, testFile
}

func TestHandleRewind_WithFileChanges_ShowsScopeDialog(t *testing.T) {
	app, _ := setupRewindWithFileChanges(t)

	if app.activeDialog == nil {
		t.Fatal("expected second dialog (scope selector) when file changes exist")
	}
	opts := app.activeDialog.options
	if len(opts) < 3 {
		t.Fatalf("expected at least 3 scope options, got %d: %v", len(opts), formatOpts(opts))
	}
}

func TestHandleRewind_ScopeBoth(t *testing.T) {
	app, testFile := setupRewindWithFileChanges(t)

	scopeDialog := app.activeDialog
	scopeDialog.done = true
	scopeDialog.cursor = 0 // "Restore code and conversation"
	model, _ := app.onDialogDone(scopeDialog)
	app = model.(*App)

	eng := app.engine
	if len(eng.Messages()) != 2 {
		t.Errorf("expected 2 messages (kept [0..1]), got %d", len(eng.Messages()))
	}
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("file = %q, want %q", string(data), "original")
	}
}

func TestHandleRewind_ScopeConversationOnly(t *testing.T) {
	app, testFile := setupRewindWithFileChanges(t)

	scopeDialog := app.activeDialog
	scopeDialog.done = true
	scopeDialog.cursor = 1 // "Restore conversation"
	model, _ := app.onDialogDone(scopeDialog)
	app = model.(*App)

	if len(app.engine.Messages()) != 2 {
		t.Errorf("expected 2 messages (kept [0..1]), got %d", len(app.engine.Messages()))
	}
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "modified" {
		t.Errorf("file = %q, want %q (should NOT be restored)", string(data), "modified")
	}
}

func TestHandleRewind_ScopeCodeOnly(t *testing.T) {
	app, testFile := setupRewindWithFileChanges(t)

	scopeDialog := app.activeDialog
	scopeDialog.done = true
	scopeDialog.cursor = 2 // "Restore code"
	model, _ := app.onDialogDone(scopeDialog)
	app = model.(*App)

	if len(app.engine.Messages()) != 3 {
		t.Errorf("expected 3 messages (not truncated), got %d", len(app.engine.Messages()))
	}
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("file = %q, want %q", string(data), "original")
	}
}

func TestHandleRewind_ScopeCancel(t *testing.T) {
	app, testFile := setupRewindWithFileChanges(t)

	scopeDialog := app.activeDialog
	scopeDialog.aborted = true
	model, _ := app.onDialogDone(scopeDialog)
	app = model.(*App)

	if len(app.engine.Messages()) != 3 {
		t.Errorf("expected 3 messages (unchanged), got %d", len(app.engine.Messages()))
	}
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "modified" {
		t.Errorf("file = %q, want %q (unchanged)", string(data), "modified")
	}
}

// TestHandleRewind_ToolResultMessagesSkipped verifies that when engine messages
// include tool_result user messages (which have no text/ID), the rewind file
// change check uses the selected message's UUID directly, not a fallback index.
// This reproduces the real scenario: user sends 4 queries with tool calls,
// engine has tool_result intermediaries, rewind must find the correct snapshot.
func TestHandleRewind_ToolResultMessagesSkipped(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "test.go")
	if err := os.WriteFile(testFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	eng := newTestEngine()
	eng.SetFileHistory(tracker)

	userUUID := "uuid-user-edit"
	eng.SetMessages([]types.Message{
		{ID: "uuid-user-hello", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{ID: "uuid-asst-1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{ID: "uuid-user-edit", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit file")}, Timestamp: testTime},
		{ID: "uuid-asst-tool", Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu1", Name: "Edit"}}, Timestamp: testTime},
		// tool_result user message — no ID, no text. The bug was finding this instead of the real user message.
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`"ok"`)}}, Timestamp: testTime},
		{ID: "uuid-asst-done", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	})

	// Setup snapshots: TrackEdit captures v1, MakeSnapshot creates snapshot with userUUID
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot(userUUID); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	// Modify file after snapshot
	if err := os.WriteFile(testFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{
		engine:      eng,
		input:       NewInput(),
		repl:        NewReplState(),
		fileHistory: tracker,
	}
	a.width = 80

	_ = a.handleRewind(nil)

	// Select second user message ("edit file") — cursor=1
	a.activeDialog.done = true
	a.activeDialog.cursor = 1
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Must show scope dialog (hasFileChanges=true), not skip to RewindMessagesOnly
	if app.activeDialog == nil {
		t.Fatal("expected scope dialog (file changes exist), but got nil — HasChangesAtMessage returned false")
	}
	opts := formatOpts(app.activeDialog.options)
	if !strings.Contains(opts, "Restore code") {
		t.Errorf("expected code restore options, got: %s", opts)
	}
}

func formatOpts(opts []DialogOption) string {
	var labels []string
	for _, o := range opts {
		labels = append(labels, o.Label)
	}
	return fmt.Sprintf("%v", labels)
}
