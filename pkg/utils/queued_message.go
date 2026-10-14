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

	// Bare <job-notification> envelope: what both producers emit once the
	// outer system-reminder wrap is gone. The head newline and the stall
	// joint mirror background.go FormatXML's format strings byte-for-byte.
	jobNotificationHead   = "<job-notification>\n"
	jobNotificationSuffix = "</job-notification>"
	jobStallJoint         = "</job-notification>\nLast output:"
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

// jobNotificationSegment returns the field segment between the head and the
// terminator — the final </job-notification> suffix, or the stall joint when
// the raw stall tail follows the closing tag (D2 N1/N2).
func jobNotificationSegment(text string) (string, bool) {
	inner, ok := strings.CutPrefix(text, jobNotificationHead)
	if !ok {
		return "", false
	}
	if segment, ok := strings.CutSuffix(inner, jobNotificationSuffix); ok {
		return segment, true
	}
	if before, _, ok := strings.Cut(inner, jobStallJoint); ok {
		return before, true
	}
	return "", false
}

// IsJobNotificationEnvelope implements the bare half of the sniff spec (D2
// N1-N3): starts at index 0 with the head newline, terminates at the closing
// tag or the stall joint, and carries no unescaped <system-reminder> in the
// field segment — real producers escape their fields, so the raw tag can never
// appear there. The unescaped stall tail is deliberately outside the check: it
// is command output and may legitimately contain anything.
func IsJobNotificationEnvelope(text string) bool {
	segment, ok := jobNotificationSegment(text)
	if !ok {
		return false
	}
	return !strings.Contains(segment, "<system-reminder>")
}

// IsLegacyJobReminderEnvelope is the pre-single-layer job predicate: one of
// the two <system-reminder> job heads at index 0 plus the reminder suffix —
// the double-wrapped form persisted by sessions before ItemModeNotification.
// The TUI replay keeps skipping these rows (no single-layer display form
// exists for them).
func IsLegacyJobReminderEnvelope(text string) bool {
	if !strings.HasSuffix(text, reminderSuffix) {
		return false
	}
	return strings.HasPrefix(text, jobCommandReminderPrefix) ||
		strings.HasPrefix(text, jobAgentReminderPrefix)
}

// IsInjectedReminderEnvelope is the total job predicate: the legacy
// double-wrapped form OR the bare single-layer form. Rewind and the engine
// test helpers want both forms; display callers use the specific halves.
func IsInjectedReminderEnvelope(text string) bool {
	return IsLegacyJobReminderEnvelope(text) || IsJobNotificationEnvelope(text)
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
