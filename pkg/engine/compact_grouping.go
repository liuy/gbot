package engine

// compact_grouping.go groups the compact transcript at API-round boundaries.
// TS align: services/compact/grouping.ts — groupMessagesByApiRound — plus the
// whole-group split rule of compact.ts:truncateHeadForPTLRetry, which is the
// TS consumer whose retain/summarize boundary gbot's token-budget split mirrors.

import "github.com/liuy/gbot/pkg/memory/short"

// GroupMessagesByApiRound groups messages at API-round boundaries: one group
// per API round-trip. A boundary fires when a NEW assistant response begins
// (a different message identity from the prior assistant). For well-formed
// conversations this is an API-safe split point — the API contract requires
// every tool_use to be resolved before the next assistant turn, so pairing
// validity falls out of the assistant-identity boundary; for malformed inputs
// (dangling tool_use after resume/truncation) EnsureToolResultPairing repairs
// the split at API time.
//
// Identity: TS gates on msg.message.id — streaming chunks of one response
// share an id, so boundaries only fire at the start of a genuinely new round.
// gbot mints a fresh UUID per assistant message at append time
// (types.NewAssistantMessage) and emits one assistant message per API
// response, so a new UUID coincides with a new round. The same-identity
// rule below preserves TS's interleave semantics (same-id chunks stay one
// group) should a response ever be split across multiple messages. The
// zero-value corner matches TS too: TS compares against an undefined
// lastAssistantId, so id-less assistants never fire a boundary — an
// empty-UUID assistant compares equal to the initial "" here.
func GroupMessagesByApiRound(messages []*short.TranscriptMessage) [][]*short.TranscriptMessage {
	groups := make([][]*short.TranscriptMessage, 0)
	var current []*short.TranscriptMessage
	// UUID of the most recently seen assistant; the sole boundary gate.
	var lastAssistantID string

	for _, msg := range messages {
		if msg.Type == "assistant" && msg.UUID != lastAssistantID && len(current) > 0 {
			groups = append(groups, current)
			current = []*short.TranscriptMessage{msg}
		} else {
			current = append(current, msg)
		}
		if msg.Type == "assistant" {
			lastAssistantID = msg.UUID
		}
	}

	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

// alignKeepFromToRoundBoundary snaps a token-budget split index onto the
// API-round group boundaries, so the retained tail always starts at a group
// start — an assistant message, never a user(tool_result) whose
// assistant(tool_use) stayed in the summarized head. An orphaned tool_result
// persists with the tail and is stripped by EnsureToolResultPairing after a
// restart, taking the compact boundary block and the prompt-cache prefix with
// it. Mirrors TS truncateHeadForPTLRetry's whole-group drop rule:
//   - a mid-group keepFrom moves FORWARD to the next group start (the tail
//     only shrinks, so the budget invariant still holds);
//   - a keepFrom inside the LAST group clamps BACK to that group's start so at
//     least one whole group stays retained (TS: dropCount = min(dropCount,
//     groups.length-1));
//   - a single-group conversation has no API-safe split: return 0 so the
//     caller summarizes the whole history and retains nothing — TS
//     compactConversation's default, which never keeps a tail.
//
// TS truncateHeadForPTLRetry also prepends a synthetic user marker when the
// retained slice starts with an assistant (the API requires a user-first
// payload). gbot needs no marker: buildResultMessages always fronts the
// retained tail with the boundary and summary user messages.
func alignKeepFromToRoundBoundary(messages []*short.TranscriptMessage, keepFrom int) int {
	if keepFrom <= 0 || keepFrom >= len(messages) {
		return keepFrom
	}
	starts := groupStartIndices(messages)
	for _, start := range starts {
		if start < keepFrom {
			continue
		}
		// First group start at or after keepFrom: either keepFrom already sits
		// on a boundary, or it split a group that must move wholly into the head.
		return start
	}
	// keepFrom sits inside the last group: clamp back so that round stays whole.
	lastStart := starts[len(starts)-1]
	return lastStart
}

// groupStartIndices returns the index at which each
// GroupMessagesByApiRound group begins; starts[0] is always 0.
func groupStartIndices(messages []*short.TranscriptMessage) []int {
	groups := GroupMessagesByApiRound(messages)
	starts := make([]int, 0, len(groups))
	idx := 0
	for _, g := range groups {
		starts = append(starts, idx)
		idx += len(g)
	}
	return starts
}
