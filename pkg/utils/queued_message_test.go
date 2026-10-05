package utils

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestUnwrapQueuedReminder(t *testing.T) {
	const raw = "wait, also run the tests"
	envelope := "<system-reminder>\nThe user sent a new message while you were working:\n" + raw +
		"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>"

	got, ok := UnwrapQueuedReminder(envelope)
	if !ok {
		t.Fatal("envelope not recognized")
	}
	if got != raw {
		t.Errorf("unwrapped = %q, want %q", got, raw)
	}
	if !IsQueuedEnvelope(envelope) {
		t.Error("IsQueuedEnvelope(envelope) = false, want true")
	}
}

func TestUnwrapQueuedReminder_NonMatches(t *testing.T) {
	nonMatches := []string{
		"",
		"plain user text",
		"<system-reminder>\nThe user sent a new message while you were working:\nno suffix",
		"prefix only\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>",
		// Other origins share the envelope shape but different wrap text.
		"<system-reminder>\nA background agent completed a job:\ndone\n</system-reminder>",
		"<system-reminder>\nThe coordinator sent a message while you were working:\nhi\n\nAddress this before completing your current task.\n</system-reminder>",
		// Wrapped envelope with leading timestamp (injectTimestamp form) must
		// NOT match — only the exact envelope does.
		"[2026-10-05 11:00:00 CST] <system-reminder>\nThe user sent a new message while you were working:\nhi" +
			"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>",
	}
	for _, text := range nonMatches {
		if _, ok := UnwrapQueuedReminder(text); ok {
			t.Errorf("UnwrapQueuedReminder(%q) matched, want no match", text)
		}
		if IsQueuedEnvelope(text) {
			t.Errorf("IsQueuedEnvelope(%q) = true, want false", text)
		}
	}
}

// IsSelectableUserMessage must skip queued message messages: they are persisted
// user input, but treating them as rewind targets would let auto-rewind throw
// the mid-turn message back into the input box on abort.
func TestIsSelectableUserMessage_QueuedMessageNotSelectable(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(
			"<system-reminder>\nThe user sent a new message while you were working:\nhi" +
				"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>")},
	}
	if IsSelectableUserMessage(msg) {
		t.Error("queued message envelope must not be selectable for rewind")
	}
}
