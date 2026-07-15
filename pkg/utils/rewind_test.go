package utils

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// --- IsSyntheticMessage ---

func TestIsSyntheticMessage_InterruptText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock(types.InterruptMessage)},
	}
	if !IsSyntheticMessage(msg) {
		t.Errorf("IsSyntheticMessage(InterruptMessage) = false, want true")
	}
}

func TestIsSyntheticMessage_NormalText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("hello world")},
	}
	if IsSyntheticMessage(msg) {
		t.Errorf("IsSyntheticMessage(normal text) = true, want false")
	}
}

func TestIsSyntheticMessage_EmptyContent(t *testing.T) {
	t.Parallel()
	msg := types.Message{Role: types.RoleAssistant}
	if IsSyntheticMessage(msg) {
		t.Errorf("IsSyntheticMessage(empty content) = true, want false")
	}
}

func TestIsSyntheticMessage_FirstBlockNotText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Bash", nil),
			types.NewTextBlock(types.InterruptMessage),
		},
	}
	if IsSyntheticMessage(msg) {
		t.Errorf("IsSyntheticMessage(first block tool_use) = true, want false")
	}
}

func TestIsSyntheticMessage_SecondBlockInterrupt(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewTextBlock("real text"),
			types.NewTextBlock(types.InterruptMessage),
		},
	}
	if IsSyntheticMessage(msg) {
		t.Errorf("IsSyntheticMessage(interrupt in 2nd block) = true, want false")
	}
}

// --- IsToolUseResultMessage ---

func TestIsToolUseResultMessage_ToolResultFirstBlock(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"result"`), false),
		},
	}
	if !IsToolUseResultMessage(msg) {
		t.Errorf("IsToolUseResultMessage(tool_result first) = false, want true")
	}
}

func TestIsToolUseResultMessage_TextFirstBlock(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("user typed this"),
			types.NewToolResultBlock("id1", json.RawMessage(`"result"`), false),
		},
	}
	if IsToolUseResultMessage(msg) {
		t.Errorf("IsToolUseResultMessage(text first) = true, want false")
	}
}

func TestIsToolUseResultMessage_AssistantRole(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"result"`), false),
		},
	}
	if IsToolUseResultMessage(msg) {
		t.Errorf("IsToolUseResultMessage(assistant role) = true, want false")
	}
}

func TestIsToolUseResultMessage_EmptyContent(t *testing.T) {
	t.Parallel()
	msg := types.Message{Role: types.RoleUser}
	if IsToolUseResultMessage(msg) {
		t.Errorf("IsToolUseResultMessage(empty content) = true, want false")
	}
}

func TestIsToolUseResultMessage_ToolUseFirstBlock(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Bash", nil),
		},
	}
	if IsToolUseResultMessage(msg) {
		t.Errorf("IsToolUseResultMessage(tool_use first) = true, want false")
	}
}

// --- HasNonToolResultContent ---

func TestHasNonToolResultContent_AllToolResults(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"r1"`), false),
			types.NewToolResultBlock("id2", json.RawMessage(`"r2"`), false),
		},
	}
	if HasNonToolResultContent(msg) {
		t.Errorf("HasNonToolResultContent(all tool_result) = true, want false")
	}
}

func TestHasNonToolResultContent_HasText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"r1"`), false),
			types.NewTextBlock("actual text"),
		},
	}
	if !HasNonToolResultContent(msg) {
		t.Errorf("HasNonToolResultContent(has text) = false, want true")
	}
}

func TestHasNonToolResultContent_HasToolUse(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Bash", nil),
		},
	}
	if !HasNonToolResultContent(msg) {
		t.Errorf("HasNonToolResultContent(has tool_use) = false, want true")
	}
}

func TestHasNonToolResultContent_EmptyContent(t *testing.T) {
	t.Parallel()
	msg := types.Message{}
	if HasNonToolResultContent(msg) {
		t.Errorf("HasNonToolResultContent(empty content) = true, want false")
	}
}

func TestHasNonToolResultContent_OnlyText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewTextBlock("just text"),
		},
	}
	if !HasNonToolResultContent(msg) {
		t.Errorf("HasNonToolResultContent(only text) = false, want true")
	}
}

// --- IsSelectableUserMessage ---

func TestIsSelectableUserMessage_NormalUserText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("help me code")},
	}
	if !IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(normal user text) = false, want true")
	}
}

func TestIsSelectableUserMessage_AssistantRole(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("hello")},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(assistant) = true, want false")
	}
}

func TestIsSelectableUserMessage_ToolResultFirstBlock(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"r1"`), false),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(tool_result first) = true, want false")
	}
}

func TestIsSelectableUserMessage_SyntheticInterrupt(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(types.InterruptMessage)},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(synthetic interrupt) = true, want false")
	}
}

func TestIsSelectableUserMessage_CompactSummaryFlag(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:  types.RoleUser,
		Flags: types.FlagCompactSummary,
		Content: []types.ContentBlock{
			types.NewTextBlock("summary content"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(compact summary flag) = true, want false")
	}
}

func TestIsSelectableUserMessage_MetaFlag(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:  types.RoleUser,
		Flags: types.FlagMeta,
		Content: []types.ContentBlock{
			types.NewTextBlock("meta content"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(meta flag) = true, want false")
	}
}

func TestIsSelectableUserMessage_AttachmentType(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content: []types.ContentBlock{
			types.NewTextBlock("attachment content"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(attachment) = true, want false")
	}
}

func TestIsSelectableUserMessage_LocalCommandStdout(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<local-command-stdout>output</local-command-stdout>"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(local-command-stdout) = true, want false")
	}
}

func TestIsSelectableUserMessage_LocalCommandStderr(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<local-command-stderr>error</local-command-stderr>"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(local-command-stderr) = true, want false")
	}
}

func TestIsSelectableUserMessage_BashStdout(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<bash-stdout>output</bash-stdout>"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(bash-stdout) = true, want false")
	}
}

func TestIsSelectableUserMessage_BashStderr(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<bash-stderr>error</bash-stderr>"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(bash-stderr) = true, want false")
	}
}

func TestIsSelectableUserMessage_TickTag(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<tick>content</tick>"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(tick tag) = true, want false")
	}
}

func TestIsSelectableUserMessage_TeammateMessage(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<teammate-message>hi</teammate-message>"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(teammate-message) = true, want false")
	}
}

func TestIsSelectableUserMessage_JobNotification(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<job-notification>done</job-notification>"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(job-notification) = true, want false")
	}
}

func TestIsSelectableUserMessage_TagInMiddleOfText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("prefix <local-command-stdout>output</local-command-stdout> suffix"),
		},
	}
	if IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(tag in middle) = true, want false")
	}
}

func TestIsSelectableUserMessage_EmptyContent(t *testing.T) {
	t.Parallel()
	msg := types.Message{Role: types.RoleUser}
	if !IsSelectableUserMessage(msg) {
		t.Errorf("IsSelectableUserMessage(empty content) = false, want true")
	}
}

// --- LastSelectableUserMessageIndex ---

func TestLastSelectableUserMessageIndex_FoundLast(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second")}},
	}
	got := LastSelectableUserMessageIndex(msgs)
	if got != 2 {
		t.Errorf("LastSelectableUserMessageIndex = %d, want 2", got)
	}
}

func TestLastSelectableUserMessageIndex_FoundMiddle(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("selectable")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"r1"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}},
	}
	got := LastSelectableUserMessageIndex(msgs)
	if got != 0 {
		t.Errorf("LastSelectableUserMessageIndex = %d, want 0", got)
	}
}

func TestLastSelectableUserMessageIndex_NoneSelectable(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"r1"`), false),
		}},
	}
	got := LastSelectableUserMessageIndex(msgs)
	if got != -1 {
		t.Errorf("LastSelectableUserMessageIndex = %d, want -1", got)
	}
}

func TestLastSelectableUserMessageIndex_EmptySlice(t *testing.T) {
	t.Parallel()
	got := LastSelectableUserMessageIndex(nil)
	if got != -1 {
		t.Errorf("LastSelectableUserMessageIndex(nil) = %d, want -1", got)
	}
}

func TestLastSelectableUserMessageIndex_SingleSelectable(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("only")}},
	}
	got := LastSelectableUserMessageIndex(msgs)
	if got != 0 {
		t.Errorf("LastSelectableUserMessageIndex = %d, want 0", got)
	}
}

// --- MessagesAfterAreOnlySynthetic ---

func TestMessagesAfterAreOnlySynthetic_EmptyAfter(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(idx=0, no msgs after) = false, want true")
	}
}

func TestMessagesAfterAreOnlySynthetic_AllSynthetic(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(types.InterruptMessage)}},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(all synthetic) = false, want true")
	}
}

func TestMessagesAfterAreOnlySynthetic_AllToolResults(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"r1"`), false),
		}},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(all tool_result) = false, want true")
	}
}

func TestMessagesAfterAreOnlySynthetic_UserWithNonToolContent(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("real input")}},
	}
	if MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(user with text) = true, want false")
	}
}

func TestMessagesAfterAreOnlySynthetic_AssistantWithText(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response")}},
	}
	if MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(assistant with text) = true, want false")
	}
}

func TestMessagesAfterAreOnlySynthetic_AssistantWithToolUse(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Bash", nil),
		}},
	}
	if MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(assistant with tool_use) = true, want false")
	}
}

func TestMessagesAfterAreOnlySynthetic_AssistantWhitespaceText(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("   \n\t  ")}},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(assistant whitespace text) = false, want true")
	}
}

func TestMessagesAfterAreOnlySynthetic_AssistantEmptyContent(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(assistant empty) = false, want true")
	}
}

func TestMessagesAfterAreOnlySynthetic_AssistantThinkingBlockOnly(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Thinking: "internal thought"},
		}},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(assistant thinking only) = false, want true")
	}
}

func TestMessagesAfterAreOnlySynthetic_AssistantTextThenToolUse(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock(""),
			types.NewToolUseBlock("id1", "Bash", nil),
		}},
	}
	if MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(empty text + tool_use) = true, want false")
	}
}

func TestMessagesAfterAreOnlySynthetic_MixedSyntheticAndReal(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(types.InterruptMessage)}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("real response")}},
	}
	if MessagesAfterAreOnlySynthetic(msgs, 0) {
		t.Errorf("MessagesAfterAreOnlySynthetic(synthetic then real) = true, want false")
	}
}

func TestMessagesAfterAreOnlySynthetic_FromIndexAtEnd(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("a")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("b")}},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 1) {
		t.Errorf("MessagesAfterAreOnlySynthetic(fromIndex at last element) = false, want true")
	}
}

func TestMessagesAfterAreOnlySynthetic_FromIndexPastEnd(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("a")}},
	}
	if !MessagesAfterAreOnlySynthetic(msgs, 5) {
		t.Errorf("MessagesAfterAreOnlySynthetic(fromIndex past end) = false, want true")
	}
}

// --- FirstTextBlockContent ---

func TestFirstTextBlockContent_FirstBlockText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewTextBlock("hello"),
			types.NewTextBlock("world"),
		},
	}
	got := FirstTextBlockContent(msg)
	if got != "hello" {
		t.Errorf("FirstTextBlockContent = %q, want %q", got, "hello")
	}
}

func TestFirstTextBlockContent_TextNotFirst(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Bash", nil),
			types.NewTextBlock("found me"),
		},
	}
	got := FirstTextBlockContent(msg)
	if got != "found me" {
		t.Errorf("FirstTextBlockContent = %q, want %q", got, "found me")
	}
}

func TestFirstTextBlockContent_NoTextBlock(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Bash", nil),
			types.NewToolResultBlock("id1", json.RawMessage(`"r1"`), false),
		},
	}
	got := FirstTextBlockContent(msg)
	if got != "" {
		t.Errorf("FirstTextBlockContent = %q, want %q", got, "")
	}
}

func TestFirstTextBlockContent_EmptyContent(t *testing.T) {
	t.Parallel()
	msg := types.Message{}
	got := FirstTextBlockContent(msg)
	if got != "" {
		t.Errorf("FirstTextBlockContent(empty) = %q, want %q", got, "")
	}
}

func TestFirstTextBlockContent_EmptyText(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Content: []types.ContentBlock{types.NewTextBlock("")},
	}
	got := FirstTextBlockContent(msg)
	if got != "" {
		t.Errorf("FirstTextBlockContent(empty text) = %q, want %q", got, "")
	}
}
