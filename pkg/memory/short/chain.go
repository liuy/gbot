package short

import (
	"log/slog"
)

// BuildConversationChain builds the conversation chain for a session.
// Traverses from leaf to root via parent_uuid, then reverses.
// Includes cycle detection and orphan recovery.
// TS align: buildConversationChain (sessionStorage.ts:2069-2094)
func (s *Store) BuildConversationChain(sessionID string) ([]*TranscriptMessage, error) {
	messages, err := s.LoadMessages(sessionID)
	if err != nil {
		return nil, err
	}

	if len(messages) == 0 {
		return []*TranscriptMessage{}, nil
	}

	// Build message map by UUID
	msgMap := make(map[string]*TranscriptMessage)
	for _, msg := range messages {
		msgMap[msg.UUID] = msg
	}

	// Find leaf: a message that no other message's parent_uuid points to
	leaf := findLeafMessage(messages)
	if leaf == nil {
		// No leaf found (possibly all orphans or circular), return empty
		return []*TranscriptMessage{}, nil
	}

	// Walk from leaf to root with cycle detection
	chain := walkToRoot(leaf, msgMap)

	// Reverse to get root→leaf order
	reverseMessages(chain)

	// Recover orphaned parallel tool results
	chain = recoverOrphanedParallelToolResults(messages, chain)

	return chain, nil
}

// recoverOrphanedParallelToolResults recovers sibling assistant blocks and tool_results
// that were orphaned by the single-parent walk.
//
// Streaming emits one AssistantMessage per content_block_stop — N parallel tool_uses
// → N messages, distinct uuid, same message.id. Each tool_result's sourceToolAssistantUUID
// points to its own one-block assistant, so the walk only keeps one branch.
//
// This function recovers the orphaned siblings by finding groups with the same message.id
// and inserting them after the anchor (last on-chain member of the group).
// TS align: recoverOrphanedParallelToolResults (sessionStorage.ts:2118-2204)
func recoverOrphanedParallelToolResults(allMessages []*TranscriptMessage, chain []*TranscriptMessage) []*TranscriptMessage {
	// Extract message.id from content JSON for assistant messages
	getMessageID := func(msg *TranscriptMessage) string {
		blocks := ParseContentBlocks(msg.Content)
		for _, block := range blocks {
			if block.Type == "tool_use" && block.ID != "" {
				// In TS, message.id is set to the tool_use.id for assistant messages
				return block.ID
			}
		}
		return ""
	}

	// Build map of all messages by UUID
	allMsgsByUUID := make(map[string]*TranscriptMessage)
	for _, msg := range allMessages {
		allMsgsByUUID[msg.UUID] = msg
	}

	// Collect chain assistants
	chainAssistants := make([]*TranscriptMessage, 0)
	for _, msg := range chain {
		if msg.Type == "assistant" {
			chainAssistants = append(chainAssistants, msg)
		}
	}

	if len(chainAssistants) == 0 {
		return chain
	}

	// Build anchorByMsgId: last on-chain member of each message.id group
	anchorByMsgId := make(map[string]*TranscriptMessage)
	for _, asst := range chainAssistants {
		if msgID := getMessageID(asst); msgID != "" {
			anchorByMsgId[msgID] = asst // later iterations overwrite → last wins
		}
	}

	// Build siblingsByMsgId: all assistant messages with same message.id
	siblingsByMsgId := make(map[string][]*TranscriptMessage)
	for _, msg := range allMessages {
		if msg.Type != "assistant" {
			continue
		}
		msgID := getMessageID(msg)
		if msgID == "" {
			continue
		}
		siblingsByMsgId[msgID] = append(siblingsByMsgId[msgID], msg)
	}

	// Build toolResultsByAsst: user messages with tool_result pointing to assistant
	toolResultsByAsst := make(map[string][]*TranscriptMessage)
	for _, msg := range allMessages {
		if msg.Type != "user" {
			continue
		}
		if msg.ParentUUID == "" {
			continue
		}
		// Check if this user message contains tool_result blocks
		blocks := ParseContentBlocks(msg.Content)
		hasToolResult := false
		for _, block := range blocks {
			if block.Type == "tool_result" {
				hasToolResult = true
				break
			}
		}
		if hasToolResult {
			toolResultsByAsst[msg.ParentUUID] = append(toolResultsByAsst[msg.ParentUUID], msg)
		}
	}

	// Build set of chain message UUIDs for quick lookup
	chainUUIDs := make(map[string]bool)
	for _, msg := range chain {
		chainUUIDs[msg.UUID] = true
	}

	// For each message.id group: collect off-chain siblings and their TRs
	processedGroups := make(map[string]bool)
	inserts := make(map[string][]*TranscriptMessage) // anchor UUID → messages to insert after

	for _, asst := range chainAssistants {
		msgID := getMessageID(asst)
		if msgID == "" || processedGroups[msgID] {
			continue
		}
		processedGroups[msgID] = true

		group := siblingsByMsgId[msgID]
		if group == nil {
			group = []*TranscriptMessage{asst}
		}

		// Collect orphaned siblings (not in chain)
		orphanedSiblings := make([]*TranscriptMessage, 0)
		for _, sib := range group {
			if !chainUUIDs[sib.UUID] {
				orphanedSiblings = append(orphanedSiblings, sib)
			}
		}

		// Collect orphaned TRs for ALL members of the group
		orphanedTRs := make([]*TranscriptMessage, 0)
		for _, member := range group {
			trs := toolResultsByAsst[member.UUID]
			for _, tr := range trs {
				if !chainUUIDs[tr.UUID] {
					orphanedTRs = append(orphanedTRs, tr)
				}
			}
		}

		if len(orphanedSiblings) == 0 && len(orphanedTRs) == 0 {
			continue
		}

		// Sort by created_at timestamp
		sortByCreated(orphanedSiblings)
		sortByCreated(orphanedTRs)

		anchor := anchorByMsgId[msgID]
		recovered := append(orphanedSiblings, orphanedTRs...)
		inserts[anchor.UUID] = recovered
	}

	if len(inserts) == 0 {
		return chain
	}

	// Splice recovered messages into chain
	result := make([]*TranscriptMessage, 0, len(chain)+len(inserts))
	for _, msg := range chain {
		result = append(result, msg)
		if toInsert, exists := inserts[msg.UUID]; exists {
			result = append(result, toInsert...)
		}
	}

	slog.Info("recovered orphaned messages in chain", "count", len(inserts))

	return result
}

// findLeafMessage finds the leaf message (no other message's parent_uuid points to it).
// Prefers non-sidechain, non-progress messages regardless of timestamp.
func findLeafMessage(messages []*TranscriptMessage) *TranscriptMessage {
	// Build set of all UUIDs that are someone's parent
	childUUIDs := make(map[string]bool)

	for _, msg := range messages {
		if msg.ParentUUID != "" {
			childUUIDs[msg.ParentUUID] = true
		}
	}

	// Find messages that are not anyone's parent (potential leaves)
	leaves := make([]*TranscriptMessage, 0)
	for _, msg := range messages {
		if !childUUIDs[msg.UUID] {
			leaves = append(leaves, msg)
		}
	}

	if len(leaves) == 0 {
		return nil // All messages are parents (circular?)
	}

	// First pass: prefer non-sidechain, non-progress leaves (latest by timestamp)
	var best *TranscriptMessage
	var bestTime int64 = -1
	for _, candidate := range leaves {
		if candidate.IsSidechain == 1 || candidate.Type == "progress" {
			continue
		}
		if candidate.CreatedAt.Unix() > bestTime {
			bestTime = candidate.CreatedAt.Unix()
			best = candidate
		}
	}
	if best != nil {
		return best
	}

	// Fall back to any leaf (latest by timestamp)
	var leaf *TranscriptMessage
	var maxTime int64 = -1
	for _, candidate := range leaves {
		if candidate.CreatedAt.Unix() > maxTime {
			maxTime = candidate.CreatedAt.Unix()
			leaf = candidate
		}
	}

	return leaf
}

// walkToRoot walks from leaf to root via parent_uuid with cycle detection.
func walkToRoot(leaf *TranscriptMessage, msgMap map[string]*TranscriptMessage) []*TranscriptMessage {
	chain := make([]*TranscriptMessage, 0)
	seen := make(map[string]bool)

	current := leaf
	for current != nil {
		// Cycle detection
		if seen[current.UUID] {
			slog.Warn("cycle detected in chain, truncating", "uuid", current.UUID)
			break
		}
		seen[current.UUID] = true
		chain = append(chain, current)

		// Move to parent
		if current.ParentUUID == "" {
			break
		}
		current = msgMap[current.ParentUUID]
	}

	return chain
}

// reverseMessages reverses a slice of messages in place.
func reverseMessages(messages []*TranscriptMessage) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}

// sortByCreated sorts messages by created_at timestamp (ascending).
func sortByCreated(messages []*TranscriptMessage) {
	// Simple insertion sort for small slices
	for i := 1; i < len(messages); i++ {
		key := messages[i]
		j := i - 1
		for j >= 0 && messages[j].CreatedAt.After(key.CreatedAt) {
			messages[j+1] = messages[j]
			j--
		}
		messages[j+1] = key
	}
}
