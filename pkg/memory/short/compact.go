package short

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// PreservedSegment describes a tail portion of conversation that was preserved
// during partial compact. Used by resume to relink the segment.
type PreservedSegment struct {
	HeadUUID    string `json:"headUuid"`    // First kept message
	AnchorUUID  string `json:"anchorUuid"`  // Boundary or last summary
	TailUUID    string `json:"tailUuid"`    // Last kept message
}

// CompactMetadata holds the metadata from a compact boundary's content JSON.
type CompactMetadata struct {
	Trigger           string           `json:"trigger"`
	PreTokens         int              `json:"preTokens"`
	MessagesSummarized int             `json:"messagesSummarized"`
	UserContext       string           `json:"userContext"`
	PreservedSegment  *PreservedSegment `json:"preservedSegment,omitempty"`
}

// CreateCompactBoundaryMessage creates a compact boundary marker message.
// TS align: messages.ts:4530-4555
func CreateCompactBoundaryMessage(trigger string, preTokens int, lastPreCompactUUID string) *Message {
	now := time.Now().UTC()
	msgUUID := uuid.New().String()

	compactMetadata := CompactMetadata{
		Trigger:           trigger,
		PreTokens:         preTokens,
		MessagesSummarized: 0, // Filled later by RecordCompact
		UserContext:       "", // Filled later by RecordCompact
	}

	contentMap := map[string]interface{}{
		"type":      "system",
		"subtype":   "compact_boundary",
		"content":   "Conversation compacted",
		"isMeta":    false,
		"timestamp": now.Format(time.RFC3339),
		"uuid":      msgUUID,
		"level":     "info",
		"compactMetadata": compactMetadata,
	}

	// Set logicalParentUuid only if lastPreCompactUUID is provided
	if lastPreCompactUUID != "" {
		contentMap["logicalParentUuid"] = lastPreCompactUUID
	}

	contentBytes, _ := json.Marshal(contentMap)

	return &Message{
		UUID:       msgUUID,
		ParentUUID: "", // Boundary is always chain root
		Type:       "system",
		Subtype:    "compact_boundary",
		Content:    string(contentBytes),
		CreatedAt:  now,
	}
}

// BuildPostCompactMessages constructs the post-compact message array.
// Order: [boundaryMarker, summaryMessages..., messagesToKeep..., attachments...]
// TS align: compact.ts:330-338
func BuildPostCompactMessages(result *CompactResult) []*Message {
	messages := make([]*Message, 0)
	messages = append(messages, result.BoundaryMarker)
	messages = append(messages, result.SummaryMessages...)
	messages = append(messages, result.MessagesToKeep...)
	messages = append(messages, result.Attachments...)
	return messages
}

// RecordCompact writes a compact operation to the database.
// 1. Writes boundary marker (parent_uuid="")
// 2. Writes summary messages (parent_uuid=boundary.uuid)
// 3. Writes kept messages (preserves original parent_uuid chain)
// 4. Writes attachments (parent_uuid=last summary/kept uuid)
// TS align: compactConversation write portion
func (s *Store) RecordCompact(sessionID string, result *CompactResult) error {

	// Begin transaction
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Track last UUID for chaining
	lastUUID := result.BoundaryMarker.UUID

	// 1. Insert boundary marker (parent_uuid="")
	seq, err := s.insertMessageTx(tx, sessionID, result.BoundaryMarker)
	if err != nil {
		return fmt.Errorf("insert boundary: %w", err)
	}
	s.indexMessageFTS(tx, seq, result.BoundaryMarker.Content)

	// 2. Insert summary messages
	for _, summary := range result.SummaryMessages {
		summary.ParentUUID = lastUUID
		seq, err := s.insertMessageTx(tx, sessionID, summary)
		if err != nil {
			return fmt.Errorf("insert summary: %w", err)
		}
		s.indexMessageFTS(tx, seq, summary.Content)
		lastUUID = summary.UUID
	}

	// 3. Insert kept messages (preserve original chain)
	for _, kept := range result.MessagesToKeep {
		// Keep original ParentUUID for chain integrity
		seq, err := s.insertMessageTx(tx, sessionID, kept)
		if err != nil {
			return fmt.Errorf("insert kept message: %w", err)
		}
		s.indexMessageFTS(tx, seq, kept.Content)
	}

	// 4. Insert attachments
	for _, att := range result.Attachments {
		att.ParentUUID = lastUUID
		seq, err := s.insertMessageTx(tx, sessionID, att)
		if err != nil {
			return fmt.Errorf("insert attachment: %w", err)
		}
		s.indexMessageFTS(tx, seq, att.Content)
	}

	// 5. Update sessions.updated_at
	_, err = tx.Exec("UPDATE sessions SET updated_at = ? WHERE session_id = ?",
		time.Now().Format(time.RFC3339), sessionID)
	if err != nil {
		return fmt.Errorf("update session timestamp: %w", err)
	}

	return tx.Commit()
}

// LoadPostCompactMessages loads messages from the last compact boundary onward.
// If no boundary exists, loads all messages.
// TS align: loadTranscriptFile compact loading logic
func (s *Store) LoadPostCompactMessages(sessionID string) ([]*Message, error) {

	// Find last boundary
	_, boundarySeq, err := s.GetLastBoundary(sessionID)
	if err != nil {
		return nil, fmt.Errorf("find last boundary: %w", err)
	}

	if boundarySeq == 0 {
		// No boundary, load all messages
		return s.LoadMessages(sessionID)
	}

	// Load from boundary onward
	return s.LoadMessagesAfterSeq(sessionID, boundarySeq-1)
}

// GetMessagesAfterCompactBoundary returns messages after the last compact boundary.
// Includes the boundary itself in the result.
// TS align: messages.ts:4643-4656
func (s *Store) GetMessagesAfterCompactBoundary(sessionID string) ([]*Message, error) {
	return s.LoadPostCompactMessages(sessionID)
}

// PartialCompact compacts only the head portion of messages, keeping the tail.
// keepFrom specifies the index (0-based) from which to start keeping messages.
// TS align: compact.ts:1500-1600 partialCompactConversation
func (s *Store) PartialCompact(sessionID string, messages []*Message, keepFrom int) (*CompactResult, error) {
	if keepFrom <= 0 {
		return nil, fmt.Errorf("keepFrom must be positive, got %d", keepFrom)
	}
	if keepFrom >= len(messages) {
		return nil, fmt.Errorf("keepFrom=%d exceeds messages length=%d", keepFrom, len(messages))
	}

	// Split into head (to compact) and tail (to keep)
	headToCompact := messages[:keepFrom]
	messagesToKeep := messages[keepFrom:]

	// Estimate pre-compact tokens
	preTokens := roughTokenCount(headToCompact)

	// Create boundary with preserved segment annotation
	boundary := CreateCompactBoundaryMessage("auto", preTokens, "")
	if len(messagesToKeep) > 0 {
		// Annotate with preserved segment
		headUUID := messagesToKeep[0].UUID
		tailUUID := messagesToKeep[len(messagesToKeep)-1].UUID
		anchorUUID := boundary.UUID // Boundary is the anchor

			_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, anchorUUID, tailUUID)
	}

	result := &CompactResult{
		BoundaryMarker:    boundary,
		SummaryMessages:   []*Message{}, // Filled by engine layer
		MessagesToKeep:    messagesToKeep,
		Attachments:       []*Message{},
		PreCompactTokens:  preTokens,
		PostCompactTokens: 0, // Filled after summary generation
	}

	return result, nil
}

// StripImagesFromMessages removes image and document blocks from messages.
// Replaces them with [image] or [document] text markers.
// TS align: compact.ts:145-200
func StripImagesFromMessages(messages []*Message) []*Message {
	result := make([]*Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Type != "user" {
			result = append(result, msg)
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		if len(blocks) == 0 {
			result = append(result, msg)
			continue
		}

		modified := false
		newBlocks := make([]ContentBlock, 0, len(blocks))

		for _, block := range blocks {
			if block.Type == "image" {
				modified = true
				newBlocks = append(newBlocks, ContentBlock{Type: "text", Text: "[image]"})
			} else if block.Type == "document" {
				modified = true
				newBlocks = append(newBlocks, ContentBlock{Type: "text", Text: "[document]"})
			} else if block.Type == "tool_result" && len(block.Content) > 0 {
				// Check for nested images in tool_result content
				// (Simplified - full implementation would parse nested content)
				newBlocks = append(newBlocks, block)
			} else {
				newBlocks = append(newBlocks, block)
			}
		}

		if !modified {
			result = append(result, msg)
			continue
		}

		// Create new message with stripped content
		contentBytes, _ := json.Marshal(newBlocks)
		newMsg := *msg
		newMsg.Content = string(contentBytes)
		result = append(result, &newMsg)
	}

	return result
}

// StripReinjectedAttachments removes attachment types that will be re-injected.
// Removes skill_discovery and skill_listing attachments.
// TS align: compact.ts:211-223
func StripReinjectedAttachments(messages []*Message) []*Message {
	result := make([]*Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Type == "attachment" && msg.Subtype == "skill_discovery" {
			continue
		}
		if msg.Type == "attachment" && msg.Subtype == "skill_listing" {
			continue
		}
		result = append(result, msg)
	}

	return result
}

// CreatePostCompactFileAttachments extracts file attachments from tool_results.
// Collects file paths from Read tool results and creates attachment messages.
// TS align: compact.ts:500-530
func CreatePostCompactFileAttachments(preCompactMessages []*Message) []*Message {
	// Collect file paths from Read tool results
	filePaths := CollectReadToolFilePaths(preCompactMessages)
	if len(filePaths) == 0 {
		return nil
	}

	// Create attachment message(s)
	// (Simplified - full implementation would group files and create proper attachments)
	attachments := make([]*Message, 0)
	for _, path := range filePaths {
		content := map[string]interface{}{
			"type":       "attachment",
			"subtype":    "file_reference",
			"content":    fmt.Sprintf("File: %s", path),
			"filepath":   path,
		}
		contentBytes, _ := json.Marshal(content)
		attachments = append(attachments, &Message{
			Type:    "attachment",
			Subtype: "file_reference",
			Content: string(contentBytes),
		})
	}

	return attachments
}

// ShouldExcludeFromPostCompactRestore returns true if a message should be
// excluded from post-compact restoration (progress, temporary system messages).
// TS align: compact.ts:540-560
func ShouldExcludeFromPostCompactRestore(msg *Message) bool {
	if msg.Type == "progress" {
		return true
	}
	if msg.Type == "system" && msg.Subtype == "informational" {
		return true
	}
	if msg.Type == "system" && msg.Subtype == "transient" {
		return true
	}
	return false
}

// TruncateToTokens truncates messages to fit within maxTokens.
// Keeps the most recent messages (tail) and drops the oldest.
// TS align: compact.ts:570-590
func TruncateToTokens(messages []*Message, maxTokens int) []*Message {
	if maxTokens <= 0 {
		return []*Message{}
	}

	totalTokens := 0
	// Count from tail backwards
	for i := len(messages) - 1; i >= 0; i-- {
		msgTokens := roughTokenCountForMessage(messages[i])
		if totalTokens+msgTokens > maxTokens {
			// Include this message if we'd otherwise have nothing
			if i == len(messages)-1 {
				return []*Message{messages[i]}
			}
			return messages[i+1:]
		}
		totalTokens += msgTokens
	}

	return messages
}

// ApplyPreservedSegmentRelinks relinks preserved segment messages into the chain.
// After partial compact, preserved segment messages still point to now-deleted
// parents. This function:
//  1. Validates tail→head walk (abort if chain broken)
//  2. Relinks head.parent_uuid = anchorUuid
//  3. Splices anchor's other children to tail
//  4. Zeros stale usage tokens on preserved assistant messages
//  5. Prunes messages before boundary that aren't in the preserved segment
//
// TS align: sessionStorage.ts:1839-1956 (applyPreservedSegmentRelinks)
func ApplyPreservedSegmentRelinks(boundary *Message, chain []*Message) []*Message {
	metadata, err := extractCompactMetadata(boundary)
	if err != nil {
		return chain
	}

	if metadata.PreservedSegment == nil {
		return chain // No preserved segment
	}

	seg := metadata.PreservedSegment

	// Build index: uuid → position in chain
	entryIndex := make(map[string]int, len(chain))
	msgMap := make(map[string]*Message, len(chain))
	for i, msg := range chain {
		entryIndex[msg.UUID] = i
		msgMap[msg.UUID] = msg
	}

	// Validate tail→head walk BEFORE mutating.
	// tail→head walk: start at tailUuid, follow parent_uuid to headUuid.
	preservedUUIDs := make(map[string]bool)
	if segIsLive(boundary, chain) {
		walkSeen := make(map[string]bool)
		cur := msgMap[seg.TailUUID]
		reachedHead := false
		for cur != nil && !walkSeen[cur.UUID] {
			walkSeen[cur.UUID] = true
			preservedUUIDs[cur.UUID] = true
			if cur.UUID == seg.HeadUUID {
				reachedHead = true
				break
			}
			if cur.ParentUUID != "" {
				cur = msgMap[cur.ParentUUID]
			} else {
				cur = nil
			}
		}
		if !reachedHead {
			// tail→head walk broke — return unchanged so resume loads
			// the full pre-compact history.
			slog.Warn("ApplyPreservedSegmentRelinks: tail→head walk broken, skipping relink")
			return chain
		}
	}

	// Relink head.parent_uuid = anchorUuid
	if head, ok := msgMap[seg.HeadUUID]; ok {
		head.ParentUUID = seg.AnchorUUID
	}

	// Tail-splice: anchor's other children → tail.
	// Any message whose parent_uuid is anchorUuid (and isn't head) gets
	// reparented to tailUuid.
	for _, msg := range chain {
		if msg.ParentUUID == seg.AnchorUUID && msg.UUID != seg.HeadUUID {
			msg.ParentUUID = seg.TailUUID
		}
	}

	// Zero stale usage on preserved assistant messages.
	// On-disk input_tokens reflect pre-compact context (~190K) — without
	// zeroing, resume → immediate autocompact spiral.
	for uuid := range preservedUUIDs {
		msg, ok := msgMap[uuid]
		if !ok || msg.Type != "assistant" {
			continue
		}
		zeroUsageInContent(msg)
	}

	// Prune everything before the boundary that isn't preserved.
	boundaryIdx, ok := entryIndex[boundary.UUID]
	if !ok {
		return chain
	}
	var result []*Message
	for _, msg := range chain {
		idx := entryIndex[msg.UUID]
		// Keep if at or after boundary, or in preserved segment
		if idx >= boundaryIdx || preservedUUIDs[msg.UUID] {
			result = append(result, msg)
		}
	}

	return result
}

// applyPreservedSegmentRelinksOnLoad scans messages for the last compact boundary
// with a preserved segment and applies relinks. Mirrors TS loadTranscriptFile:3704.
func applyPreservedSegmentRelinksOnLoad(messages []*Message) []*Message {
	var lastSegBoundary *Message
	absoluteLastBoundaryIdx := -1
	lastSegBoundaryIdx := -1

	for i, msg := range messages {
		if msg.Type == "system" && msg.Subtype == "compact_boundary" {
			absoluteLastBoundaryIdx = i
			var contentMap map[string]interface{}
			if err := json.Unmarshal([]byte(msg.Content), &contentMap); err != nil {
				continue
			}
			meta, ok := contentMap["compactMetadata"]
			if !ok {
				continue
			}
			metaMap, ok := meta.(map[string]interface{})
			if !ok {
				continue
			}
			if seg, ok := metaMap["preservedSegment"]; ok && seg != nil {
				lastSegBoundary = msg
				lastSegBoundaryIdx = i
			}
		}
	}

	if lastSegBoundary == nil {
		return messages
	}

	// Seg is stale if a no-seg boundary came after it — skip relink.
	if lastSegBoundaryIdx != absoluteLastBoundaryIdx {
		return messages
	}

	return ApplyPreservedSegmentRelinks(lastSegBoundary, messages)
}

// ApplySnipRemovals removes messages marked as deleted by snip operations.
// Mirrors TS applySnipRemovals (sessionStorage.ts:1982-2040).
func ApplySnipRemovals(messages []*Message) []*Message {
	toDelete := make(map[string]bool)
	for _, msg := range messages {
		if msg.Type != "system" || msg.Subtype != "compact_boundary" {
			continue
		}
		var contentMap map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &contentMap); err != nil {
			continue
		}
		snipMeta, ok := contentMap["snipMetadata"]
		if !ok {
			continue
		}
		snipMap, ok := snipMeta.(map[string]interface{})
		if !ok {
			continue
		}
		removedUUIDs, ok := snipMap["removedUuids"]
		if !ok {
			continue
		}
		uuidList, ok := removedUUIDs.([]interface{})
		if !ok {
			continue
		}
		for _, uid := range uuidList {
			if s, ok := uid.(string); ok {
				toDelete[s] = true
			}
		}
	}

	if len(toDelete) == 0 {
		return messages
	}

	// Build parent map for deleted entries
	deletedParent := make(map[string]string)
	for _, msg := range messages {
		if toDelete[msg.UUID] {
			deletedParent[msg.UUID] = msg.ParentUUID
		}
	}

	// Path-compressed resolution: find the first non-deleted ancestor
	resolve := func(startUUID string) string {
		path := []string{}
		cur := startUUID
		for cur != "" && toDelete[cur] {
			path = append(path, cur)
			parent, found := deletedParent[cur]
			if !found {
				cur = ""
				break
			}
			cur = parent
		}
		for _, p := range path {
			deletedParent[p] = cur
		}
		return cur
	}

	// Filter out deleted messages and relink survivors
	var result []*Message
	for _, msg := range messages {
		if toDelete[msg.UUID] {
			continue
		}
		if msg.ParentUUID != "" && toDelete[msg.ParentUUID] {
			msg.ParentUUID = resolve(msg.ParentUUID)
		}
		result = append(result, msg)
	}

	return result
}

// zeroUsageInContent zeros usage tokens in an assistant message's content JSON.
// Prevents resume→autocompact spiral from stale pre-compact token counts.
func zeroUsageInContent(msg *Message) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(msg.Content), &parsed); err != nil {
		return
	}
	messageObj, ok := parsed["message"].(map[string]any)
	if !ok {
		return
	}
	usage, ok := messageObj["usage"].(map[string]any)
	if !ok {
		return
	}
	usage["input_tokens"] = float64(0)
	usage["output_tokens"] = float64(0)
	usage["cache_creation_input_tokens"] = float64(0)
	usage["cache_read_input_tokens"] = float64(0)
	if updated, err := json.Marshal(parsed); err == nil {
		msg.Content = string(updated)
	}
}

// segIsLive checks if a preserved segment is still valid (not superseded).
// TS align: sessionStorage.ts:1846-1870
func segIsLive(boundary *Message, allMessages []*Message) bool {
	metadata, err := extractCompactMetadata(boundary)
	if err != nil {
		return false
	}

	if metadata.PreservedSegment == nil {
		return true // No segment to check
	}

	// Check if head and tail are still in the messages
	headFound := false
	tailFound := false
	for _, msg := range allMessages {
		if msg.UUID == metadata.PreservedSegment.HeadUUID {
			headFound = true
		}
		if msg.UUID == metadata.PreservedSegment.TailUUID {
			tailFound = true
		}
	}

	return headFound && tailFound
}

// MergeHookInstructions merges hook instructions into post-compact messages.
// TS align: compact.ts:600-620
func MergeHookInstructions(postCompact []*Message, hookInstructions []*Message) []*Message {
	if len(hookInstructions) == 0 {
		return postCompact
	}

	// Append hook instructions to the end
	result := make([]*Message, 0, len(postCompact)+len(hookInstructions))
	result = append(result, postCompact...)
	result = append(result, hookInstructions...)
	return result
}

// CreateCompactCanUseTool creates a can-use-tool message for post-compact.
// TS align: compact.ts:625-640
func CreateCompactCanUseTool() *Message {
	content := map[string]interface{}{
		"type":    "system",
		"subtype": "can_use_tool",
		"content": "Tool use restored after compact",
	}
	contentBytes, _ := json.Marshal(content)
	return &Message{
		Type:    "system",
		Subtype: "can_use_tool",
		Content: string(contentBytes),
	}
}

// CreateAsyncAgentAttachmentsIfNeeded creates attachments for async agents.
// TS align: compact.ts:645-670
func CreateAsyncAgentAttachmentsIfNeeded(preCompact []*Message) []*Message {
	// Check for running async agents
	// (Simplified - full implementation would check agent state)
	return nil
}

// CreatePlanAttachmentIfNeeded creates a plan attachment if plan mode was active.
// TS align: compact.ts:675-695
func CreatePlanAttachmentIfNeeded(preCompact []*Message) []*Message {
	// Check for plan mode in pre-compact messages
	// (Simplified - full implementation would detect plan state)
	return nil
}

// CreateSkillAttachmentIfNeeded creates skill attachment for active skills.
// TS align: compact.ts:725-745
func CreateSkillAttachmentIfNeeded(preCompact []*Message) []*Message {
	// Check for invoked skills
	// (Simplified - full implementation would check skill state)
	return nil
}

// AddErrorNotificationIfNeeded adds an error notification on compact failure.
// TS align: compact.ts:750-770
func AddErrorNotificationIfNeeded(postCompact []*Message, compactErr error) []*Message {
	if compactErr == nil {
		return postCompact
	}

	content := map[string]interface{}{
		"type":    "system",
		"subtype": "error_notification",
		"content": fmt.Sprintf("Compact error: %v", compactErr),
		"level":   "error",
	}
	contentBytes, _ := json.Marshal(content)
	errorMsg := &Message{
		Type:    "system",
		Subtype: "error_notification",
		Content: string(contentBytes),
	}

	result := make([]*Message, 0, len(postCompact)+1)
	result = append(result, postCompact...)
	result = append(result, errorMsg)
	return result
}

// CollectReadToolFilePaths collects file paths from Read tool results.
// TS align: compact.ts:775-795
func CollectReadToolFilePaths(messages []*Message) []string {
	paths := make(map[string]bool)

	for _, msg := range messages {
		if msg.Type != "user" {
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		for _, block := range blocks {
			if block.Type == "tool_result" {
				// Check if this is a Read tool result
				// (Simplified - full implementation would check tool_use_id name)
				contentToCheck := block.Content
				if contentToCheck == "" {
					contentToCheck = block.Text
				}
				if contentToCheck != "" {
					// Extract file path from content if it looks like a file
					if len(contentToCheck) < 256 && looksLikeFilePath(contentToCheck) {
						paths[contentToCheck] = true
					}
				}
			}
		}
	}

	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	return result
}

// TruncateHeadForPTLRetry truncates head for prompt-too-long retry.
// TS align: compact.ts:243-291
func TruncateHeadForPTLRetry(messages []*Message, maxTokens int) []*Message {
	if maxTokens <= 0 {
		return []*Message{}
	}
	return TruncateToTokens(messages, maxTokens)
}

// annotateBoundaryWithPreservedSegment adds preserved segment metadata to boundary.
func annotateBoundaryWithPreservedSegment(boundary *Message, headUUID, anchorUUID, tailUUID string) error {
	// Parse existing content
	var contentMap map[string]interface{}
	if err := json.Unmarshal([]byte(boundary.Content), &contentMap); err != nil {
		return err
	}

	// Get or create compactMetadata
	compactMetadataJSON, ok := contentMap["compactMetadata"]
	var compactMetadata CompactMetadata
	if ok {
		compactMetadataBytes, _ := json.Marshal(compactMetadataJSON)
		_ = json.Unmarshal(compactMetadataBytes, &compactMetadata)
	}

	// Add preserved segment
	compactMetadata.PreservedSegment = &PreservedSegment{
		HeadUUID:   headUUID,
		AnchorUUID: anchorUUID,
		TailUUID:   tailUUID,
	}

	// Update contentMap
	contentMap["compactMetadata"] = compactMetadata

	// Marshal back
	contentBytes, _ := json.Marshal(contentMap)


	boundary.Content = string(contentBytes)
	return nil
}
// extractCompactMetadata extracts compact metadata from boundary content JSON.
func extractCompactMetadata(boundary *Message) (*CompactMetadata, error) {
	var contentMap map[string]interface{}
	if err := json.Unmarshal([]byte(boundary.Content), &contentMap); err != nil {
		return nil, err
	}

	compactMetadataJSON, ok := contentMap["compactMetadata"]
	if !ok {
		return &CompactMetadata{}, nil
	}

	compactMetadataBytes, _ := json.Marshal(compactMetadataJSON)

	var metadata CompactMetadata
	if err := json.Unmarshal(compactMetadataBytes, &metadata); err != nil {
		return nil, err
	}

	return &metadata, nil
}

// insertMessageTx inserts a message within a transaction (ignores parent tracking).
// Returns the auto-increment seq for FTS indexing.
func (s *Store) insertMessageTx(tx *sql.Tx, sessionID string, msg *Message) (int64, error) {
	createdAt := msg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	query := `
		INSERT INTO messages (session_id, uuid, parent_uuid, logical_parent_uuid,
		                     is_sidechain, type, subtype, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	var result sql.Result
	var err error
	if tx != nil {
		result, err = tx.Exec(query, sessionID, msg.UUID, msg.ParentUUID,
			msg.LogicalParentUUID, msg.IsSidechain, msg.Type, msg.Subtype, msg.Content,
			createdAt)
	} else {
		result, err = s.db.Exec(query, sessionID, msg.UUID, msg.ParentUUID,
			msg.LogicalParentUUID, msg.IsSidechain, msg.Type, msg.Subtype, msg.Content,
			createdAt)
	}

	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// indexMessageFTS indexes a message's content in FTS5 if it has searchable text.
func (s *Store) indexMessageFTS(db dbExec, seq int64, content string) {
	text := ExtractTextFromJSON(content)
	if text == "" {
		return
	}
	if err := s.insertFTS(db, seq, content); err != nil {
		slog.Warn("FTS index failed in RecordCompact", "seq", seq, "error", err)
	}
}

// roughTokenCount estimates token count for messages.
func roughTokenCount(messages []*Message) int {
	count := 0
	for _, msg := range messages {
		count += roughTokenCountForMessage(msg)
	}
	return count
}

// roughTokenCountForMessage estimates token count for a single message.
// Rough estimate: 1 token per 4 characters.
func roughTokenCountForMessage(msg *Message) int {
	return len(msg.Content) / 4
}

// looksLikeFilePath checks if a string looks like a file path.
func looksLikeFilePath(s string) bool {
	// Simple heuristic: contains / or . and not too long
	if len(s) > 200 {
		return false
	}
	for _, c := range s {
		if c == '/' || c == '.' {
			return true
		}
	}
	return false
}
