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
