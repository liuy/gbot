package utils

import "strings"

// The queued message reminder envelope: exactly what engine.wrapOriginText's
// default branch produces for a user message sent mid-turn, wrapped in the
// system-reminder tags the API-assembly layer adds. These constants must stay
// byte-identical to the engine-side construction — pinned by
// TestQueuedMessage_PinsByteEqualityWithLegacyAssembledForm in pkg/engine.
const (
	queuedReminderPrefix = "<system-reminder>\nThe user sent a new message while you were working:\n"
	queuedReminderSuffix = "\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>"
)

// Injected job/message reminder envelopes: what engine.buildReminderContent
// produces for drained job items (heads from wrapOriginText's job arms) and
// what the Message tool's buildEnvelope produces for a delivery. Kept
// unexported like the 插话 pair above; the cross-package byte pins live in
// pkg/engine tests through the exported predicates below, against literals
// written in those tests.
const (
	jobCommandReminderPrefix = "<system-reminder>\nA background command completed:\n"
	jobAgentReminderPrefix   = "<system-reminder>\nA background agent completed a job:\n"
	reminderSuffix           = "\n</system-reminder>"
	messageDeliveryPrefix    = "<message from=\""
	messageDeliverySuffix    = "</message>"
)

// UnwrapQueuedReminder extracts the original user text from a
// system-reminder envelope produced by the mid-turn queued message persist path.
// ok is false for anything else — including other system-reminder content —
// so only true user queued messages match.
func UnwrapQueuedReminder(text string) (original string, ok bool) {
	inner, hasPrefix := strings.CutPrefix(text, queuedReminderPrefix)
	if !hasPrefix {
		return "", false
	}
	original, hasSuffix := strings.CutSuffix(inner, queuedReminderSuffix)
	if !hasSuffix {
		return "", false
	}
	return original, true
}

// IsQueuedEnvelope reports whether text is exactly the queued message
// reminder envelope.
func IsQueuedEnvelope(text string) bool {
	_, ok := UnwrapQueuedReminder(text)
	return ok
}

// IsInjectedReminderEnvelope implements the job half of the shared sniff spec:
// true iff text starts at index 0 with one of the two job heads and ends with
// the reminder suffix. The index-0 rule keeps the assembled
// "[ts] <system-reminder>…" wire form from matching, same as UnwrapQueuedReminder.
func IsInjectedReminderEnvelope(text string) bool {
	if !strings.HasSuffix(text, reminderSuffix) {
		return false
	}
	return strings.HasPrefix(text, jobCommandReminderPrefix) ||
		strings.HasPrefix(text, jobAgentReminderPrefix)
}

// IsMessageDeliveryEnvelope implements D7 ①-④. Tightened so a user who
// pastes a <message from="…"> block as their whole prompt is not mistaken
// for a delivery: the sender must be an engine/agent id shape (main, e2,
// agent-3, fork-2 — buildEnvelope XML-escapes from, so real senders never
// carry the excluded bytes) and the text must be free of unescaped
// system-reminder tags, which a real delivery never contains (its body is
// escaped).
func IsMessageDeliveryEnvelope(text string) bool {
	inner, ok := strings.CutPrefix(text, messageDeliveryPrefix)
	if !ok {
		return false
	}
	from, rest, ok := strings.Cut(inner, `"`)
	if !ok || !strings.HasSuffix(rest, messageDeliverySuffix) || from == "" {
		return false
	}
	for _, r := range from {
		isIDRune := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-'
		if !isIDRune {
			return false
		}
	}
	return !strings.Contains(text, "<system-reminder>")
}

// MessageDeliverySender extracts the sender id from a message-delivery
// envelope — the `<message from="X">` head — so UI handlers can label a
// delivery row ("got a message from X") without re-parsing the attribute.
// Returns "" when the head shape is absent; callers gate on
// IsMessageDeliveryEnvelope when they need the full-envelope guarantee.
func MessageDeliverySender(text string) string {
	inner, ok := strings.CutPrefix(text, messageDeliveryPrefix)
	if !ok {
		return ""
	}
	from, _, ok := strings.Cut(inner, `"`)
	if !ok {
		return ""
	}
	return from
}
