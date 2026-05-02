// Package toolresult — per-message aggregate budget for tool results.
//
// TS source: src/utils/toolResultStorage.ts enforceToolResultBudget (lines 769-909)
//
// For each API-level user message whose tool_result blocks together exceed
// MaxToolResultsPerMessageChars (200K), the largest FRESH results are persisted
// to disk and replaced with previews. State is tracked across turns so prompt
// cache stability is preserved: previously-replaced results get the same
// byte-identical preview re-applied; previously-unreplaced results are frozen.
package toolresult

import (
	"encoding/json"
	"log/slog"
	"maps"
	"sort"
)

// MaxToolResultsPerMessageChars is the per-message aggregate budget.
// TS: MAX_TOOL_RESULTS_PER_MESSAGE_CHARS (toolLimits.ts:49)
const MaxToolResultsPerMessageChars = 200000

// ContentReplacementState tracks budget decisions across turns.
// One instance is bound to a conversation thread, held by Engine.
//
// TS: ContentReplacementState (toolResultStorage.ts:390-393)
type ContentReplacementState struct {
	// SeenIDs tracks all tool_use_ids processed by the budget.
	// Once seen, an ID's fate is frozen: if it has a Replacement it's always
	// re-applied; if not, it's never replaced (would break prompt cache).
	SeenIDs map[string]bool

	// Replacements maps tool_use_id → preview string for byte-identical
	// re-apply across turns. Zero I/O on re-apply — just a map lookup.
	Replacements map[string]string
}

// ContentReplacementRecord is the serializable form of one replacement decision.
// Persisted to transcript for resume reconstruction.
//
// TS: ContentReplacementRecord (toolResultStorage.ts:466-483)
type ContentReplacementRecord struct {
	Kind        string `json:"kind"`        // always "tool-result"
	ToolUseID   string `json:"tool_use_id"`
	Replacement string `json:"replacement"` // exact preview string
}

// toolResultCandidate is an eligible tool_result block for budget consideration.
//
// TS: ToolResultCandidate (toolResultStorage.ts:486-490)
type toolResultCandidate struct {
	toolUseID string
	content   string // decoded string content
	size      int    // character count
}

// candidatePartition groups candidates by prior decision state.
type candidatePartition struct {
	mustReapply []toolResultCandidate // previously replaced → re-apply
	frozen      []toolResultCandidate // previously seen, unreplaced → off-limits
	fresh       []toolResultCandidate // never seen → eligible
}

// BudgetMessage is the message type used by budget operations.
// Callers convert from their own message type (e.g. types.Message) to this.
type BudgetMessage struct {
	ID      string // message ID (used for assistant message grouping)
	Role    string // "user" or "assistant"
	Content []BudgetBlock
}

// BudgetBlock is a content block used by budget operations.
type BudgetBlock struct {
	Type      string          // "tool_use", "tool_result", "text", etc.
	ID        string          // tool_use block ID
	Name      string          // tool name (tool_use blocks)
	ToolUseID string          // tool_use_id (tool_result blocks)
	Content   json.RawMessage // content (tool_result blocks)
}

// NewContentReplacementState creates an empty state.
// TS: createContentReplacementState (toolResultStorage.ts:395-397)
func NewContentReplacementState() *ContentReplacementState {
	return &ContentReplacementState{
		SeenIDs:      make(map[string]bool),
		Replacements: make(map[string]string),
	}
}

// CloneContentReplacementState clones state for a cache-sharing fork.
// TS: cloneContentReplacementState (toolResultStorage.ts:405-412)
func CloneContentReplacementState(src *ContentReplacementState) *ContentReplacementState {
	if src == nil {
		return nil
	}
	clone := &ContentReplacementState{
		SeenIDs:      make(map[string]bool, len(src.SeenIDs)),
		Replacements: make(map[string]string, len(src.Replacements)),
	}
	maps.Copy(clone.SeenIDs, src.SeenIDs)
	maps.Copy(clone.Replacements, src.Replacements)
	return clone
}

// ContentSize calculates the character count of tool result content.
// Handles both string content and content block arrays.
// TS: contentSize (toolResultStorage.ts:497-513)
func ContentSize(content json.RawMessage) int {
	if len(content) == 0 {
		return 0
	}
	// Try block array first (Anthropic API format: [{type:"text",text:"..."},...])
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err == nil && len(blocks) > 0 {
		total := 0
		for _, b := range blocks {
			if b.Type == "text" {
				total += len(b.Text)
			}
		}
		return total
	}
	// Fallback: treat as raw string (double-wrapped JSON)
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return len(s)
	}
	return len(content)
}

// IsContentAlreadyCompacted checks if content starts with <persisted-output>.
// TS: isContentAlreadyCompacted (toolResultStorage.ts:517-519)
func IsContentAlreadyCompacted(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return len(s) >= len(PersistedOutputTag) &&
			s[:len(PersistedOutputTag)] == PersistedOutputTag
	}
	return false
}

// EnforceToolResultBudget enforces the per-message aggregate budget.
//
// For each API-level user message whose tool_result blocks together exceed
// MaxToolResultsPerMessageChars, the largest FRESH results are persisted and
// replaced with previews. State is mutated in place.
//
// Returns (messages, newlyReplaced). When no replacement is needed, messages
// is returned unchanged.
//
// TS: enforceToolResultBudget (toolResultStorage.ts:769-909)
func EnforceToolResultBudget(
	messages []BudgetMessage,
	state *ContentReplacementState,
	sessionID string,
	skipToolNames map[string]bool,
) ([]BudgetMessage, []ContentReplacementRecord) {
	if state == nil {
		return messages, nil
	}

	candidatesByMessage := collectCandidatesByMessage(messages)
	nameByToolUseID := buildToolNameMap(messages, skipToolNames)
	limit := MaxToolResultsPerMessageChars

	shouldSkip := func(id string) bool {
		if len(skipToolNames) == 0 {
			return false
		}
		name, ok := nameByToolUseID[id]
		return ok && skipToolNames[name]
	}

	replacementMap := make(map[string]string)
	var toPersist []toolResultCandidate
	reappliedCount := 0
	messagesOverBudget := 0

	for _, candidates := range candidatesByMessage {
		partition := partitionByPriorDecision(candidates, state)

		// Re-apply: pure map lookups, byte-identical, no I/O.
		for _, c := range partition.mustReapply {
			replacementMap[c.toolUseID] = c.content
		}
		reappliedCount += len(partition.mustReapply)

		// Fresh.length == 0 means previously-processed message — just re-apply.
		if len(partition.fresh) == 0 {
			for _, c := range candidates {
				state.SeenIDs[c.toolUseID] = true
			}
			continue
		}

		// Skip tools with maxResultSizeChars: Infinity (Read) — never persist.
		var eligible []toolResultCandidate
		for _, c := range partition.fresh {
			if shouldSkip(c.toolUseID) {
				state.SeenIDs[c.toolUseID] = true
			} else {
				eligible = append(eligible, c)
			}
		}

		frozenSize := 0
		for _, c := range partition.frozen {
			frozenSize += c.size
		}
		freshSize := 0
		for _, c := range eligible {
			freshSize += c.size
		}

		var selected []toolResultCandidate
		if frozenSize+freshSize > limit {
			selected = selectFreshToReplace(eligible, frozenSize, limit)
		}

		// Mark non-selected candidates as seen NOW (synchronously).
		selectedIDs := make(map[string]bool, len(selected))
		for _, c := range selected {
			selectedIDs[c.toolUseID] = true
		}
		for _, c := range candidates {
			if !selectedIDs[c.toolUseID] {
				state.SeenIDs[c.toolUseID] = true
			}
		}

		if len(selected) == 0 {
			continue
		}
		messagesOverBudget++
		toPersist = append(toPersist, selected...)
	}

	if len(replacementMap) == 0 && len(toPersist) == 0 {
		return messages, nil
	}

	// Persist selected candidates.
	var newlyReplaced []ContentReplacementRecord
	replacedSize := 0
	for _, c := range toPersist {
		state.SeenIDs[c.toolUseID] = true
		replacement := buildBudgetReplacement(c, sessionID)
		if replacement == "" {
			continue // persist failed, treat as frozen
		}
		replacedSize += c.size
		replacementMap[c.toolUseID] = replacement
		state.Replacements[c.toolUseID] = replacement
		newlyReplaced = append(newlyReplaced, ContentReplacementRecord{
			Kind:        "tool-result",
			ToolUseID:   c.toolUseID,
			Replacement: replacement,
		})
	}

	if len(replacementMap) == 0 {
		return messages, nil
	}

	if len(newlyReplaced) > 0 {
		slog.Info("Per-message budget: persisted tool results", "persisted", len(newlyReplaced), "over_budget_messages", messagesOverBudget, "shed", FormatFileSize(replacedSize), "reapplied", reappliedCount)
	}

	return replaceToolResultContents(messages, replacementMap), newlyReplaced
}

// ApplyToolResultBudget is the query-loop integration point.
// Returns messages unchanged when state is nil or no replacement occurred.
// TS: applyToolResultBudget (toolResultStorage.ts:924-938)
func ApplyToolResultBudget(
	messages []BudgetMessage,
	state *ContentReplacementState,
	sessionID string,
	skipToolNames map[string]bool,
) []BudgetMessage {
	msgs, _ := EnforceToolResultBudget(messages, state, sessionID, skipToolNames)
	return msgs
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// collectCandidatesByMessage groups candidates by API-level user message.
// Only NEW assistant message IDs create group boundaries — same-ID fragments don't.
// TS: collectCandidatesByMessage (toolResultStorage.ts:600-639)
func collectCandidatesByMessage(messages []BudgetMessage) [][]toolResultCandidate {
	var groups [][]toolResultCandidate
	var current []toolResultCandidate

	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = nil
		}
	}

	// Track assistant message IDs — same-ID fragments don't create boundaries.
	// TS: seenAsstIds (toolResultStorage.ts:623)
	seenAsstIDs := make(map[string]bool)
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			current = append(current, collectCandidatesFromMessage(msg)...)
		case "assistant":
			if msg.ID != "" && !seenAsstIDs[msg.ID] {
				flush()
				seenAsstIDs[msg.ID] = true
			}
		}
	}
	flush()

	return groups
}

// collectCandidatesFromMessage extracts eligible tool_result candidates from a user message.
// TS: collectCandidatesFromMessage (toolResultStorage.ts:557-573)
func collectCandidatesFromMessage(msg BudgetMessage) []toolResultCandidate {
	if msg.Role != "user" {
		return nil
	}
	var candidates []toolResultCandidate
	for _, block := range msg.Content {
		if block.Type != "tool_result" || len(block.Content) == 0 {
			continue
		}
		if IsContentAlreadyCompacted(block.Content) {
			continue
		}
		if HasImageBlock(block.Content) {
			continue
		}
		// Decode content string.
		var s string
		if json.Unmarshal(block.Content, &s) != nil {
			s = string(block.Content)
		}
		candidates = append(candidates, toolResultCandidate{
			toolUseID: block.ToolUseID,
			content:   s,
			size:      ContentSize(block.Content),
		})
	}
	return candidates
}

// partitionByPriorDecision splits candidates into mustReapply/frozen/fresh.
// TS: partitionByPriorDecision (toolResultStorage.ts:649-667)
func partitionByPriorDecision(
	candidates []toolResultCandidate,
	state *ContentReplacementState,
) candidatePartition {
	var p candidatePartition
	for _, c := range candidates {
		if replacement, ok := state.Replacements[c.toolUseID]; ok {
			p.mustReapply = append(p.mustReapply, toolResultCandidate{
				toolUseID: c.toolUseID,
				content:   replacement,
				size:      c.size,
			})
		} else if state.SeenIDs[c.toolUseID] {
			p.frozen = append(p.frozen, c)
		} else {
			p.fresh = append(p.fresh, c)
		}
	}
	return p
}

// selectFreshToReplace picks the largest fresh results to replace until
// remaining total ≤ limit. If frozen alone exceeds budget, accept the overage.
// TS: selectFreshToReplace (toolResultStorage.ts:675-692)
func selectFreshToReplace(fresh []toolResultCandidate, frozenSize, limit int) []toolResultCandidate {
	sorted := make([]toolResultCandidate, len(fresh))
	copy(sorted, fresh)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].size > sorted[j].size })

	var selected []toolResultCandidate
	remaining := frozenSize
	for _, c := range sorted {
		remaining += c.size
	}
	for _, c := range sorted {
		if remaining <= limit {
			break
		}
		selected = append(selected, c)
		remaining -= c.size
	}
	return selected
}

// buildBudgetReplacement persists content and builds the preview message.
// Returns empty string on failure.
// TS: buildReplacement (toolResultStorage.ts:728-737)
func buildBudgetReplacement(c toolResultCandidate, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	result, err := PersistToolResult(sessionID, c.toolUseID, []byte(c.content))
	if err != nil {
		slog.Warn("budget: persist failed", "tool_use_id", c.toolUseID, "error", err)
		return ""
	}
	return BuildLargeToolResultMessage(result)
}

// replaceToolResultContents returns a new message slice with replacements applied.
// Messages/blocks with no replacements are passed through by reference.
// TS: replaceToolResultContents (toolResultStorage.ts:699-726)
func replaceToolResultContents(messages []BudgetMessage, replacementMap map[string]string) []BudgetMessage {
	needsAny := false
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == "tool_result" {
				if _, ok := replacementMap[block.ToolUseID]; ok {
					needsAny = true
					break
				}
			}
		}
		if needsAny {
			break
		}
	}
	if !needsAny {
		return messages
	}

	result := make([]BudgetMessage, len(messages))
	for i, msg := range messages {
		result[i] = msg
		if msg.Role != "user" {
			continue
		}
		touched := false
		newContent := make([]BudgetBlock, len(msg.Content))
		for j, block := range msg.Content {
			newContent[j] = block
			if block.Type == "tool_result" {
				if replacement, ok := replacementMap[block.ToolUseID]; ok {
					encoded, _ := json.Marshal(replacement)
					newContent[j].Content = json.RawMessage(encoded)
					touched = true
				}
			}
		}
		if touched {
			result[i] = msg
			result[i].Content = newContent
		}
	}
	return result
}

// buildToolNameMap builds a tool_use_id → tool_name mapping from messages.
// Returns nil if skipToolNames is empty (optimization).
func buildToolNameMap(messages []BudgetMessage, skipToolNames map[string]bool) map[string]string {
	if len(skipToolNames) == 0 {
		return nil
	}
	m := make(map[string]string)
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.Type == "tool_use" && block.ID != "" {
				m[block.ID] = block.Name
			}
		}
	}
	return m
}

// ReconstructContentReplacementState rebuilds state from transcript messages and records.
// Used for session resume: guarantees budget makes same decisions as original session.
// inheritedReplacements fills gaps for IDs in messages that records don't cover
// (e.g., fork-inherited mustReapply entries that were never persisted as newlyReplaced).
// TS: reconstructContentReplacementState (toolResultStorage.ts:960-992)
func ReconstructContentReplacementState(
	messages []BudgetMessage,
	records []ContentReplacementRecord,
	inheritedReplacements map[string]string,
) *ContentReplacementState {
	state := NewContentReplacementState()

	// Collect all candidate IDs from messages.
	candidateIDs := make(map[string]bool)
	for _, group := range collectCandidatesByMessage(messages) {
		for _, c := range group {
			candidateIDs[c.toolUseID] = true
		}
	}

	// Add all candidate IDs as seen.
	for id := range candidateIDs {
		state.SeenIDs[id] = true
	}

	// Populate replacements from records.
	for _, r := range records {
		if r.Kind != "tool-result" {
			continue
		}
		if candidateIDs[r.ToolUseID] {
			state.Replacements[r.ToolUseID] = r.Replacement
		}
	}

	// TS line 980-986: gap-fill from inherited replacements (parent state).
	// A fork's original run applies parent-inherited replacements via mustReapply
	// (never persisted — not newlyReplaced). On resume the sidechain has the
	// original content but no record, so records alone would classify it as frozen.
	// The parent's live state still has the mapping; copy it for IDs in messages
	// that records don't cover.
	for id, replacement := range inheritedReplacements {
		if candidateIDs[id] {
			if _, ok := state.Replacements[id]; !ok {
				state.Replacements[id] = replacement
			}
		}
	}

	return state
}

// ReconstructForSubagentResume rebuilds state for a subagent resume, merging
// parent state to fill gaps where records are missing.
// TS: reconstructForSubagentResume (toolResultStorage.ts:1001-1012)
func ReconstructForSubagentResume(
	parentState *ContentReplacementState,
	resumedMessages []BudgetMessage,
	sidechainRecords []ContentReplacementRecord,
) *ContentReplacementState {
	if parentState == nil {
		return nil
	}
	return ReconstructContentReplacementState(resumedMessages, sidechainRecords, parentState.Replacements)
}
