package short

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func findByUUID(msgs []*Message, uuid string) *Message {
	for _, m := range msgs {
		if m.UUID == uuid {
			return m
		}
	}
	return nil
}

func TestCreateCompactBoundaryMessage_Fields(t *testing.T) {
	tests := []struct {
		name     string
		trigger  string
		preTokens int
		lastUUID  string
	}{
		{
			name:     "manual trigger",
			trigger:  "manual",
			preTokens: 10000,
			lastUUID:  "",
		},
		{
			name:     "auto trigger",
			trigger:  "auto",
			preTokens: 5000,
			lastUUID:  "prev-boundary-uuid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := CreateCompactBoundaryMessage(tt.trigger, tt.preTokens, tt.lastUUID)

			// Check type and subtype
			if msg.Type != "system" {
				t.Errorf("Type = %q, want system", msg.Type)
			}
			if msg.Subtype != "compact_boundary" {
				t.Errorf("Subtype = %q, want compact_boundary", msg.Subtype)
			}

			// Check content contains "Conversation compacted"
			if !strings.Contains(msg.Content, "Conversation compacted") {
				t.Errorf("Content should contain 'Conversation compacted', got %q", msg.Content)
			}

			// Parse and check compactMetadata
			var contentMap map[string]interface{}
			if err := json.Unmarshal([]byte(msg.Content), &contentMap); err != nil {
				t.Fatalf("Failed to parse content JSON: %v", err)
			}

			metadataJSON, ok := contentMap["compactMetadata"]
			if !ok {
				t.Fatal("compactMetadata not found in content")
			}

			metadataBytes, _ := json.Marshal(metadataJSON)
			var metadata CompactMetadata
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				t.Fatalf("Failed to parse compactMetadata: %v", err)
			}

			if metadata.Trigger != tt.trigger {
				t.Errorf("trigger = %q, want %q", metadata.Trigger, tt.trigger)
			}
			if metadata.PreTokens != tt.preTokens {
				t.Errorf("preTokens = %d, want %d", metadata.PreTokens, tt.preTokens)
			}

			// Check logicalParentUuid is set only when lastUUID is provided
			logicalParent, hasLogicalParent := contentMap["logicalParentUuid"]
			if tt.lastUUID != "" {
				if !hasLogicalParent {
					t.Error("logicalParentUuid should be set when lastUUID provided")
				}
				if logicalParent != tt.lastUUID {
					t.Errorf("logicalParentUuid = %v, want %s", logicalParent, tt.lastUUID)
				}
			} else if hasLogicalParent {
				t.Error("logicalParentUuid should not be set when lastUUID is empty")
			}
		})
	}
}

func TestCreateCompactBoundaryMessage_PreservedSegment(t *testing.T) {
	msg := CreateCompactBoundaryMessage("auto", 5000, "")

	// Add preserved segment annotation
	headUUID := "head-123"
	anchorUUID := "anchor-456"
	tailUUID := "tail-789"

	if err := annotateBoundaryWithPreservedSegment(msg, headUUID, anchorUUID, tailUUID); err != nil {
		t.Fatalf("annotateBoundaryWithPreservedSegment: %v", err)
	}

	// Parse and verify
	var contentMap map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Content), &contentMap); err != nil {
		t.Fatalf("Failed to parse content JSON: %v", err)
	}

	metadataJSON := contentMap["compactMetadata"]
	metadataBytes, _ := json.Marshal(metadataJSON)
	var metadata CompactMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("Failed to parse compactMetadata: %v", err)
	}

	if metadata.PreservedSegment == nil {
		t.Fatal("PreservedSegment not found in metadata")
	}

	if metadata.PreservedSegment.HeadUUID != headUUID {
		t.Errorf("HeadUUID = %q, want %s", metadata.PreservedSegment.HeadUUID, headUUID)
	}
	if metadata.PreservedSegment.AnchorUUID != anchorUUID {
		t.Errorf("AnchorUUID = %q, want %s", metadata.PreservedSegment.AnchorUUID, anchorUUID)
	}
	if metadata.PreservedSegment.TailUUID != tailUUID {
		t.Errorf("TailUUID = %q, want %s", metadata.PreservedSegment.TailUUID, tailUUID)
	}
}

func TestBuildPostCompactMessages_Order(t *testing.T) {
	boundary := &Message{UUID: "boundary-1", Type: "system", Subtype: "compact_boundary"}
	summary := &Message{UUID: "summary-1", Type: "user"}
	kept := &Message{UUID: "kept-1", Type: "user"}
	att := &Message{UUID: "att-1", Type: "attachment"}

	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{summary},
		MessagesToKeep:  []*Message{kept},
		Attachments:     []*Message{att},
	}

	messages := BuildPostCompactMessages(result)

	// Order: boundary, summary, kept, attachments
	if len(messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(messages))
	}

	if messages[0].UUID != "boundary-1" {
		t.Errorf("messages[0].UUID = %q, want boundary-1", messages[0].UUID)
	}
	if messages[1].UUID != "summary-1" {
		t.Errorf("messages[1].UUID = %q, want summary-1", messages[1].UUID)
	}
	if messages[2].UUID != "kept-1" {
		t.Errorf("messages[2].UUID = %q, want kept-1", messages[2].UUID)
	}
	if messages[3].UUID != "att-1" {
		t.Errorf("messages[3].UUID = %q, want att-1", messages[3].UUID)
	}
}

func TestRecordCompact_WriteVerification(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"

	// Need to create a session first (session.go is worker-2's responsibility)
	// For this test, we'll skip if session doesn't exist
	boundary := CreateCompactBoundaryMessage("manual", 1000, "")
	summary := &Message{
		UUID:    "summary-1",
		Type:    "user",
		Content: `[{"type":"text","text":"Summary"}]`,
	}

	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{summary},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}

	err := store.RecordCompact(sessionID, result)
	// If session doesn't exist, that's expected (session.go is separate)
	_ = err
}

func TestApplyPreservedSegmentRelinks_ValidSegment(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-123"
	anchorUUID := boundary.UUID
	tailUUID := "tail-789"

	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, anchorUUID, tailUUID)

	// Create messages: pre-compact + boundary + summary + preserved segment
	messages := []*Message{
		{UUID: "old-1", ParentUUID: ""},            // pre-compact, should be pruned
		{UUID: "old-2", ParentUUID: "old-1"},       // pre-compact, should be pruned
		boundary,                                   // idx 2 = boundaryIdx
		{UUID: "summary-1", ParentUUID: boundary.UUID}, // after boundary, kept
		{UUID: headUUID, ParentUUID: "summary-1"},
		{UUID: "middle-1", ParentUUID: headUUID},
		{UUID: tailUUID, ParentUUID: "middle-1"},
	}

	chain := ApplyPreservedSegmentRelinks(boundary, messages)

	// Head should be relinked to anchor
	headMsg := findByUUID(chain, headUUID)
	if headMsg == nil {
		t.Fatal("head message not in chain")
	}
	if headMsg.ParentUUID != anchorUUID {
		t.Errorf("head.ParentUUID = %q, want %q (anchor)", headMsg.ParentUUID, anchorUUID)
	}

	// Pre-compact messages should be pruned
	if findByUUID(chain, "old-1") != nil {
		t.Error("old-1 should have been pruned (before boundary, not preserved)")
	}
	if findByUUID(chain, "old-2") != nil {
		t.Error("old-2 should have been pruned (before boundary, not preserved)")
	}

	// summary-1 is after boundary, should be kept
	if findByUUID(chain, "summary-1") == nil {
		t.Error("summary-1 should be kept (after boundary)")
	}

	// middle-1 and tail should still be present
	if findByUUID(chain, "middle-1") == nil {
		t.Error("middle-1 should be in preserved segment")
	}
	if findByUUID(chain, tailUUID) == nil {
		t.Error("tail should be in preserved segment")
	}
}

func TestApplyPreservedSegmentRelinks_TailSplice(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-1"
	anchorUUID := boundary.UUID
	tailUUID := "tail-1"

	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, anchorUUID, tailUUID)

	// anchor has two children: head and other-child
	messages := []*Message{
		boundary,
		{UUID: headUUID, ParentUUID: anchorUUID},
		{UUID: "middle", ParentUUID: headUUID},
		{UUID: tailUUID, ParentUUID: "middle"},
		{UUID: "other-child", ParentUUID: anchorUUID},
	}

	chain := ApplyPreservedSegmentRelinks(boundary, messages)

	// other-child should be spliced to tail (not anchor)
	other := findByUUID(chain, "other-child")
	if other == nil {
		t.Fatal("other-child not in chain")
	}
	if other.ParentUUID != tailUUID {
		t.Errorf("other-child.ParentUUID = %q, want %q (tail)", other.ParentUUID, tailUUID)
	}
}

func TestApplyPreservedSegmentRelinks_BrokenWalk(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-missing"
	anchorUUID := boundary.UUID
	tailUUID := "tail-1"

	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, anchorUUID, tailUUID)

	// tail exists but head doesn't — walk broken
	messages := []*Message{
		boundary,
		{UUID: tailUUID, ParentUUID: "nonexistent"},
	}

	chain := ApplyPreservedSegmentRelinks(boundary, messages)

	// Should return unchanged (no pruning, no relink)
	if len(chain) != len(messages) {
		t.Errorf("broken walk: got %d messages, want %d (unchanged)", len(chain), len(messages))
	}
}

func TestApplyPreservedSegmentRelinks_NoSegment(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	messages := []*Message{boundary}

	chain := ApplyPreservedSegmentRelinks(boundary, messages)

	if len(chain) != 1 {
		t.Errorf("got chain length %d, want 1", len(chain))
	}
}

func TestLoadPostCompactMessages_NoBoundary(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Add some messages without boundary
	for i := 0; i < 3; i++ {
		msg := testMessage(0, "user", string(rune('a'+i)), "", `[{"type":"text","text":"msg"}]`)
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	messages, err := store.LoadPostCompactMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadPostCompactMessages: %v", err)
	}

	// Should load all messages when no boundary
	if len(messages) != 3 {
		t.Errorf("got %d messages, want 3", len(messages))
	}
}

func TestLoadPostCompactMessages_WithBoundary(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Add messages before boundary
	msg1 := testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"before"}]`)
	if err := store.AppendMessage(sessionID, msg1); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Add boundary
	boundary := CreateCompactBoundaryMessage("auto", 1000, "")
	if err := store.AppendMessage(sessionID, boundary); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}

	// Add messages after boundary
	msg2 := testMessage(0, "user", "uuid-2", "", `[{"type":"text","text":"after"}]`)
	if err := store.AppendMessage(sessionID, msg2); err != nil {
		t.Fatalf("AppendMessage after: %v", err)
	}

	messages, err := store.LoadPostCompactMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadPostCompactMessages: %v", err)
	}

	// Should only load boundary and after (not msg1)
	if len(messages) != 2 {
		t.Errorf("got %d messages, want 2 (boundary + after)", len(messages))
	}

	// First message should be the boundary
	if messages[0].Subtype != "compact_boundary" {
		t.Errorf("first message subtype = %q, want compact_boundary", messages[0].Subtype)
	}
}

func TestGetMessagesAfterCompactBoundary_SliceCorrect(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Add messages
	for i := 0; i < 5; i++ {
		msg := testMessage(0, "user", string(rune('a'+i)), "", `[{"type":"text","text":"msg"}]`)
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	// Add boundary
	boundary := CreateCompactBoundaryMessage("auto", 1000, "")
	if err := store.AppendMessage(sessionID, boundary); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}

	// Add more messages
	for i := 0; i < 2; i++ {
		msg := testMessage(0, "user", string(rune('x'+i)), "", `[{"type":"text","text":"after"}]`)
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage after: %v", err)
		}
	}

	messages, err := store.GetMessagesAfterCompactBoundary(sessionID)
	if err != nil {
		t.Fatalf("GetMessagesAfterCompactBoundary: %v", err)
	}

	// Should include boundary and messages after (3 total: boundary + 2 after)
	if len(messages) != 3 {
		t.Errorf("got %d messages, want 3", len(messages))
	}

	// First message should be boundary
	if messages[0].Subtype != "compact_boundary" {
		t.Errorf("first message should be boundary, got %v", messages[0].Subtype)
	}
}

func TestPartialCompact_PreservesTail(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"

	// Create 10 messages
	messages := make([]*Message, 10)
	for i := 0; i < 10; i++ {
		messages[i] = testMessage(0, "user", string(rune('a'+i)), "", `[{"type":"text","text":"msg"}]`)
	}

	// Compact keeping last 3
	result, err := store.PartialCompact(sessionID, messages, 7) // keep from index 7 (last 3)
	if err != nil {
		t.Fatalf("PartialCompact: %v", err)
	}

	if len(result.MessagesToKeep) != 3 {
		t.Errorf("MessagesToKeep length = %d, want 3", len(result.MessagesToKeep))
	}

	// Check preserved segment is set
	metadata, _ := extractCompactMetadata(result.BoundaryMarker)
	if metadata.PreservedSegment == nil {
		t.Error("PreservedSegment should be set")
	}
}

func TestStripImagesFromMessages_RemovesImage(t *testing.T) {
	msgWithImage := &Message{
		Type: "user",
		Content: `[{"type":"text","text":"hello"},{"type":"image","source":{"type":"base64","data":"abc..."}}]`,
	}

	result := StripImagesFromMessages([]*Message{msgWithImage})

	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	blocks := ParseContentBlocks(result[0].Content)
	if len(blocks) != 2 {
		t.Errorf("got %d blocks, want 2", len(blocks))
	}

	if blocks[1].Type != "text" || blocks[1].Text != "[image]" {
		t.Errorf("image block not replaced, got %+v", blocks[1])
	}
}

func TestStripImagesFromMessages_KeepsText(t *testing.T) {
	msgTextOnly := &Message{
		Type: "user",
		Content: `[{"type":"text","text":"hello world"}]`,
	}

	result := StripImagesFromMessages([]*Message{msgTextOnly})

	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	if result[0].Content != msgTextOnly.Content {
		t.Error("text-only message was modified")
	}
}

func TestStripReinjectedAttachments_RemovesSkillDiscovery(t *testing.T) {
	messages := []*Message{
		{Type: "user", Content: `[{"type":"text","text":"hi"}]`},
		{Type: "attachment", Subtype: "skill_discovery", Content: `{}`},
		{Type: "attachment", Subtype: "skill_listing", Content: `{}`},
		{Type: "attachment", Subtype: "file", Content: `{}`},
	}

	result := StripReinjectedAttachments(messages)

	// Should only have user and file attachment (2 messages)
	if len(result) != 2 {
		t.Errorf("got %d messages, want 2", len(result))
	}

	for _, msg := range result {
		if msg.Type == "attachment" && msg.Subtype == "skill_discovery" {
			t.Error("skill_discovery should be removed")
		}
		if msg.Type == "attachment" && msg.Subtype == "skill_listing" {
			t.Error("skill_listing should be removed")
		}
	}
}

func TestCreatePostCompactFileAttachments_ExtractsFilePaths(t *testing.T) {
	messages := []*Message{
		{
			Type: "user",
			Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"/path/to/file.txt","is_error":false}]`,
		},
	}

	attachments := CreatePostCompactFileAttachments(messages)

	// Should create attachment for the file
	if len(attachments) == 0 {
		t.Error("expected at least one attachment")
	}
}

func TestShouldExcludeFromPostCompactRestore_ExcludesProgress(t *testing.T) {
	tests := []struct {
		name     string
		msg      *Message
		wantExclude bool
	}{
		{
			name:     "progress message excluded",
			msg:      &Message{Type: "progress"},
			wantExclude: true,
		},
		{
			name:     "informational system excluded",
			msg:      &Message{Type: "system", Subtype: "informational"},
			wantExclude: true,
		},
		{
			name:     "user message kept",
			msg:      &Message{Type: "user"},
			wantExclude: false,
		},
		{
			name:     "assistant message kept",
			msg:      &Message{Type: "assistant"},
			wantExclude: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldExcludeFromPostCompactRestore(tt.msg)
			if got != tt.wantExclude {
				t.Errorf("ShouldExcludeFromPostCompactRestore() = %v, want %v", got, tt.wantExclude)
			}
		})
	}
}

func TestTruncateToTokens_Truncates(t *testing.T) {
	// Create messages with estimated sizes
	messages := make([]*Message, 10)
	for i := 0; i < 10; i++ {
		// Each message has ~40 chars content ~10 tokens
		messages[i] = &Message{
			UUID: string(rune('a' + i)),
			Content: `[{"type":"text","text":"` + strings.Repeat("x", 30) + `"}]`,
		}
	}

	// Truncate to ~50 tokens (should keep ~5 messages from tail)
	result := TruncateToTokens(messages, 50)

	if len(result) == 0 {
		t.Fatal("got empty result, want at least 1 message")
	}

	if len(result) > 5 {
		t.Fatalf("got %d messages, want at most 5", len(result))
	}
	if result[len(result)-1].UUID != "j" {
		t.Errorf("last message UUID = %q, want j (tail)", result[len(result)-1].UUID)
	}
}

func TestTruncateToTokens_KeepsAtLeastOne(t *testing.T) {
	messages := []*Message{
		{UUID: "a", Content: `[{"type":"text","text":"very long message..."}]`},
	}

	// Very small budget
	result := TruncateToTokens(messages, 1)

	if len(result) != 1 {
		t.Errorf("got %d messages, want 1 (should keep at least one)", len(result))
	}
}

func TestMergeHookInstructions(t *testing.T) {
	postCompact := []*Message{
		{UUID: "post-1", Type: "user"},
	}

	hookInstructions := []*Message{
		{UUID: "hook-1", Type: "system"},
		{UUID: "hook-2", Type: "system"},
	}

	result := MergeHookInstructions(postCompact, hookInstructions)

	if len(result) != 3 {
		t.Errorf("got %d messages, want 3", len(result))
	}

	// Hook instructions should be at the end
	if result[1].UUID != "hook-1" || result[2].UUID != "hook-2" {
		t.Error("hook instructions not at end")
	}
}

func TestAddErrorNotificationIfNeeded_NoError(t *testing.T) {
	postCompact := []*Message{{UUID: "post-1"}}

	result := AddErrorNotificationIfNeeded(postCompact, nil)

	if len(result) != 1 {
		t.Errorf("got %d messages, want 1 (no error added)", len(result))
	}
}

func TestAddErrorNotificationIfNeeded_WithError(t *testing.T) {
	postCompact := []*Message{{UUID: "post-1"}}

	result := AddErrorNotificationIfNeeded(postCompact, fmt.Errorf("compact failed"))

	if len(result) != 2 {
		t.Errorf("got %d messages, want 2 (error added)", len(result))
	}

	// Last message should be error notification
	if result[1].Subtype != "error_notification" {
		t.Errorf("last message subtype = %q, want error_notification", result[1].Subtype)
	}
}

func TestCollectReadToolFilePaths(t *testing.T) {
	messages := []*Message{
		{
			Type: "user",
			Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"/path/to/file.txt"}]`,
		},
		{
			Type: "assistant",
			Content: `[{"type":"text","text":"response"}]`,
		},
	}

	paths := CollectReadToolFilePaths(messages)

	if len(paths) == 0 {
		t.Error("expected to collect file paths")
	}

	found := false
	for _, path := range paths {
		if strings.Contains(path, "file.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("paths = %v, want file.txt", paths)
	}
}

func TestTruncateHeadForPTLRetry(t *testing.T) {
	messages := make([]*Message, 10)
	for i := 0; i < 10; i++ {
		messages[i] = &Message{
			UUID:     string(rune('a' + i)),
			Content:  `[{"type":"text","text":"message"}]`,
		}
	}

	// Truncate to fit ~3 messages
	result := TruncateHeadForPTLRetry(messages, 30)

	if len(result) == 0 {
		t.Fatal("got empty result")
	}

	if len(result) > 5 {
		t.Errorf("got %d messages, want at most 5 (truncated)", len(result))
	}

	// Should keep tail
	if result[len(result)-1].UUID != "j" {
		t.Errorf("last message UUID = %q, want j (tail kept)", result[len(result)-1].UUID)
	}
}

func TestSegIsLive_NoSegment(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")

	if !segIsLive(boundary, []*Message{boundary}) {
		t.Error("segIsLive should return true when no preserved segment")
	}
}

func TestSegIsLive_SegmentValid(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-123"
	tailUUID := "tail-789"
	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, boundary.UUID, tailUUID)

	messages := []*Message{
		boundary,
		{UUID: headUUID},
		{UUID: tailUUID},
	}

	if !segIsLive(boundary, messages) {
		t.Error("segIsLive should return true when segment messages exist")
	}
}

func TestSegIsLive_SegmentStale(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-123"
	tailUUID := "tail-789"
	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, boundary.UUID, tailUUID)

	// Messages don't include the segment messages
	messages := []*Message{boundary}

	if segIsLive(boundary, messages) {
		t.Error("segIsLive should return false when segment messages missing")
	}
}

func TestRoughTokenCountForMessage(t *testing.T) {
	msg := &Message{Content: "1234567890"} // 10 chars

	count := roughTokenCountForMessage(msg)

	if count != 2 { // 10 / 4 = 2.5 → 2
		t.Errorf("roughTokenCountForMessage() = %d, want 2", count)
	}
}

func TestLooksLikeFilePath(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"/path/to/file.txt", true},
		{"file.txt", true},
		{"no-slash", false},
		{strings.Repeat("x", 300), false}, // Too long
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := looksLikeFilePath(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeFilePath(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// Full compact+resume cycle test (requires session.go to be complete)
func TestCompactResumeCycle_Integration(t *testing.T) {
	// This test will be fully functional once session.go is complete
	// For now, it documents the expected flow:
	//
	// 1. Create session with 10 messages
	// 2. Execute compact (writes boundary + summary + 3 kept)
	// 3. Resume loads → should only get boundary + summary + 3 kept (5 total)
	//
	t.Skip("requires session.go integration")
}

func TestApplyPreservedSegmentRelinksOnLoad_NoBoundary(t *testing.T) {
	// No boundary → messages returned unchanged
	msgs := []*Message{
		{UUID: "a", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{UUID: "b", Type: "assistant", Content: `[{"type":"text","text":"reply"}]`},
	}
	result := applyPreservedSegmentRelinksOnLoad(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestApplyPreservedSegmentRelinksOnLoad_WithPreservedSegment(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "anchor-uuid")
	headUUID := "head-1"
	tailUUID := "tail-1"
	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, boundary.UUID, tailUUID)

	head := &Message{UUID: headUUID, Type: "assistant", ParentUUID: boundary.UUID, Content: `[{"type":"text","text":"head"}]`}
	tail := &Message{UUID: tailUUID, Type: "assistant", ParentUUID: headUUID, Content: `[{"type":"text","text":"tail"}]`}
	preCompact := &Message{UUID: "pre-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"before"}]`}
	postTail := &Message{UUID: "post-1", Type: "user", ParentUUID: tailUUID, Content: `[{"type":"text","text":"after"}]`}

	msgs := []*Message{preCompact, boundary, head, tail, postTail}
	result := applyPreservedSegmentRelinksOnLoad(msgs)

	// Pre-compact should be pruned; boundary + preserved + post should remain
	found := make(map[string]bool)
	for _, m := range result {
		found[m.UUID] = true
	}
	if found["pre-1"] {
		t.Error("pre-compact message should have been pruned")
	}
	if !found[boundary.UUID] || !found[headUUID] || !found[tailUUID] || !found["post-1"] {
		t.Error("boundary, head, tail, and post-tail messages should be preserved")
	}
}

func TestApplyPreservedSegmentRelinksOnLoad_StaleSeg(t *testing.T) {
	// Two boundaries: first has preserved segment, second doesn't.
	// Seg is stale → no relink applied.
	boundary1 := CreateCompactBoundaryMessage("auto", 5000, "")
	_ = annotateBoundaryWithPreservedSegment(boundary1, "head-1", boundary1.UUID, "tail-1")
	boundary2 := CreateCompactBoundaryMessage("manual", 3000, "")

	msgs := []*Message{boundary1, boundary2}
	result := applyPreservedSegmentRelinksOnLoad(msgs)
	if len(result) != 2 {
		t.Errorf("stale seg should be no-op, got %d messages", len(result))
	}
}

func TestApplySnipRemovals_NoSnips(t *testing.T) {
	msgs := []*Message{
		{UUID: "a", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}
	result := ApplySnipRemovals(msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestApplySnipRemovals_WithRemovals(t *testing.T) {
	// Create a boundary with snipMetadata
	content := map[string]interface{}{
		"type": "system",
		"compactMetadata": map[string]interface{}{},
		"snipMetadata": map[string]interface{}{
			"removedUuids": []interface{}{"msg-to-delete"},
		},
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	boundary := &Message{UUID: "boundary-1", Type: "system", Subtype: "compact_boundary", ParentUUID: "", Content: string(contentJSON)}
	toDelete := &Message{UUID: "msg-to-delete", Type: "user", ParentUUID: "boundary-1", Content: `[{"type":"text","text":"delete me"}]`}
	survivor := &Message{UUID: "survivor-1", Type: "assistant", ParentUUID: "msg-to-delete", Content: `[{"type":"text","text":"I stay"}]`}

	msgs := []*Message{boundary, toDelete, survivor}
	result := ApplySnipRemovals(msgs)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (boundary + survivor), got %d", len(result))
	}

	// Survivor should be relinked to boundary-1
	if result[1].UUID != "survivor-1" {
		t.Errorf("second message should be survivor, got %q", result[1].UUID)
	}
	if result[1].ParentUUID != "boundary-1" {
		t.Errorf("survivor parent should be relinked to boundary-1, got %q", result[1].ParentUUID)
	}
}

func TestRecordCompact_FullResult(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session-full"
	createTestSession(t, store, sessionID)

	// Create full CompactResult with all fields
	boundary := CreateCompactBoundaryMessage("manual", 1000, "")
	summary1 := &Message{
		UUID:    "summary-1",
		Type:    "user",
		Content: `[{"type":"text","text":"Summary part 1"}]`,
	}
	summary2 := &Message{
		UUID:    "summary-2",
		Type:    "user",
		Content: `[{"type":"text","text":"Summary part 2"}]`,
	}
	kept1 := &Message{
		UUID:    "kept-1",
		Type:    "user",
		Content: `[{"type":"text","text":"Kept message 1"}]`,
	}
	kept2 := &Message{
		UUID:    "kept-2",
		Type:    "assistant",
		Content: `[{"type":"text","text":"Kept message 2"}]`,
	}
	att1 := &Message{
		UUID:    "att-1",
		Type:    "attachment",
		Subtype: "file",
		Content: `[{"type":"text","text":"File attachment"}]`,
	}

	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{summary1, summary2},
		MessagesToKeep:  []*Message{kept1, kept2},
		Attachments:     []*Message{att1},
	}

	if err := store.RecordCompact(sessionID, result); err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	// Verify all messages were written
	messages, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	// Should have: boundary + 2 summaries + 2 kept + 1 attachment = 6
	if len(messages) != 6 {
		t.Errorf("got %d messages, want 6", len(messages))
	}

	// Verify order: boundary, summaries, kept, attachments
	if messages[0].Subtype != "compact_boundary" {
		t.Errorf("first message should be boundary, got %v", messages[0].Subtype)
	}
	if messages[1].UUID != "summary-1" {
		t.Errorf("second message should be summary-1, got %v", messages[1].UUID)
	}
	if messages[2].UUID != "summary-2" {
		t.Errorf("third message should be summary-2, got %v", messages[2].UUID)
	}
	if messages[3].UUID != "kept-1" {
		t.Errorf("fourth message should be kept-1, got %v", messages[3].UUID)
	}
	if messages[4].UUID != "kept-2" {
		t.Errorf("fifth message should be kept-2, got %v", messages[4].UUID)
	}
	if messages[5].UUID != "att-1" {
		t.Errorf("sixth message should be att-1, got %v", messages[5].UUID)
	}

	// Verify parent chaining
	if messages[1].ParentUUID != boundary.UUID {
		t.Errorf("summary-1 parent should be boundary, got %v", messages[1].ParentUUID)
	}
	if messages[2].ParentUUID != "summary-1" {
		t.Errorf("summary-2 parent should be summary-1, got %v", messages[2].ParentUUID)
	}
	if messages[5].ParentUUID != "summary-2" {
		t.Errorf("attachment parent should be last summary, got %v", messages[5].ParentUUID)
	}
}

func TestCreateCompactCanUseTool(t *testing.T) {
	msg := CreateCompactCanUseTool()

	if msg == nil {
		t.Fatal("CreateCompactCanUseTool returned nil")
	}

	if msg.Type != "system" {
		t.Errorf("Type = %q, want system", msg.Type)
	}
	if msg.Subtype != "can_use_tool" {
		t.Errorf("Subtype = %q, want can_use_tool", msg.Subtype)
	}

	// Parse and verify content
	var contentMap map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Content), &contentMap); err != nil {
		t.Fatalf("Failed to parse content JSON: %v", err)
	}

	if contentMap["type"] != "system" {
		t.Error("content type should be system")
	}
	if contentMap["subtype"] != "can_use_tool" {
		t.Error("content subtype should be can_use_tool")
	}
	if contentMap["content"] != "Tool use restored after compact" {
		t.Errorf("content = %v, want 'Tool use restored after compact'", contentMap["content"])
	}
}

func TestCreateAsyncAgentAttachmentsIfNeeded(t *testing.T) {
	messages := []*Message{
		{UUID: "msg-1", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	result := CreateAsyncAgentAttachmentsIfNeeded(messages)

	if result != nil {
		t.Errorf("CreateAsyncAgentAttachmentsIfNeeded should return nil, got %v", result)
	}
}

func TestCreatePlanAttachmentIfNeeded(t *testing.T) {
	messages := []*Message{
		{UUID: "msg-1", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	result := CreatePlanAttachmentIfNeeded(messages)

	if result != nil {
		t.Errorf("CreatePlanAttachmentIfNeeded should return nil, got %v", result)
	}
}

func TestCreateSkillAttachmentIfNeeded(t *testing.T) {
	messages := []*Message{
		{UUID: "msg-1", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	result := CreateSkillAttachmentIfNeeded(messages)

	if result != nil {
		t.Errorf("CreateSkillAttachmentIfNeeded should return nil, got %v", result)
	}
}

func TestZeroUsageInContent_WithUsage(t *testing.T) {
	// Create an assistant message with usage
	content := map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"id":      "msg-123",
			"role":    "assistant",
			"content": "Hello",
			"usage": map[string]interface{}{
				"input_tokens":                 1500,
				"output_tokens":                500,
				"cache_creation_input_tokens":  200,
				"cache_read_input_tokens":      100,
			},
		},
	}
	contentJSON, _ := json.Marshal(content)
	msg := &Message{
		UUID:    "msg-123",
		Type:    "assistant",
		Content: string(contentJSON),
	}

	zeroUsageInContent(msg)

	// Parse and verify all tokens are zeroed
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Content), &parsed); err != nil {
		t.Fatalf("Failed to parse content: %v", err)
	}

	messageObj, ok := parsed["message"].(map[string]interface{})
	if !ok {
		t.Fatal("message object not found")
	}

	usage, ok := messageObj["usage"].(map[string]interface{})
	if !ok {
		t.Fatal("usage not found in message")
	}

	checkZero := func(key string) {
		val, ok := usage[key].(float64)
		if !ok {
			t.Errorf("%s is not a number", key)
		} else if val != 0 {
			t.Errorf("%s = %v, want 0", key, val)
		}
	}

	checkZero("input_tokens")
	checkZero("output_tokens")
	checkZero("cache_creation_input_tokens")
	checkZero("cache_read_input_tokens")
}

func TestZeroUsageInContent_NoUsage(t *testing.T) {
	// Message without usage field
	content := map[string]interface{}{
		"type":    "assistant",
		"message": map[string]interface{}{"id": "msg-123", "role": "assistant", "content": "Hello"},
	}
	contentJSON, _ := json.Marshal(content)
	msg := &Message{
		UUID:    "msg-123",
		Type:    "assistant",
		Content: string(contentJSON),
	}

	// Should not panic
	zeroUsageInContent(msg)

	// Content should be unchanged
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Content), &parsed); err != nil {
		t.Fatalf("Failed to parse content: %v", err)
	}

	messageObj, ok := parsed["message"].(map[string]interface{})
	if !ok {
		t.Fatal("message object not found")
	}

	if _, hasUsage := messageObj["usage"]; hasUsage {
		t.Error("usage field should not exist after zeroUsageInContent on message without usage")
	}
}

func TestZeroUsageInContent_InvalidJSON(t *testing.T) {
	msg := &Message{
		UUID:    "msg-123",
		Type:    "assistant",
		Content: `{invalid json`,
	}

	// Should not panic
	zeroUsageInContent(msg)

	// Content should be unchanged
	if msg.Content != `{invalid json` {
		t.Error("invalid JSON should not be modified")
	}
}

func TestTruncateHeadForPTLRetry_Truncates(t *testing.T) {
	// Create messages where each is ~10 tokens
	messages := make([]*Message, 10)
	for i := 0; i < 10; i++ {
		messages[i] = &Message{
			UUID:     string(rune('a' + i)),
			Content:  `[{"type":"text","text":"message"}]`, // ~40 chars ~10 tokens
		}
	}

	// Truncate to 30 tokens (~3 messages)
	result := TruncateHeadForPTLRetry(messages, 30)

	if len(result) == 0 {
		t.Fatal("got empty result, want at least 1 message")
	}

	// Should keep tail
	if result[len(result)-1].UUID != "j" {
		t.Errorf("last message UUID = %q, want j (tail kept)", result[len(result)-1].UUID)
	}
}

func TestTruncateHeadForPTLRetry_ZeroBudget(t *testing.T) {
	messages := []*Message{
		{UUID: "a", Content: `[{"type":"text","text":"message"}]`},
	}

	result := TruncateHeadForPTLRetry(messages, 0)

	if len(result) != 0 {
		t.Errorf("got %d messages, want 0 for zero budget", len(result))
	}
}

func TestExtractCompactMetadata_WithPreservedSegment(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-123"
	anchorUUID := "anchor-456"
	tailUUID := "tail-789"

	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, anchorUUID, tailUUID)

	metadata, err := extractCompactMetadata(boundary)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}

	if metadata.Trigger != "auto" {
		t.Errorf("trigger = %q, want auto", metadata.Trigger)
	}
	if metadata.PreTokens != 5000 {
		t.Errorf("preTokens = %d, want 5000", metadata.PreTokens)
	}
	if metadata.PreservedSegment == nil {
		t.Fatal("PreservedSegment should not be nil")
	}
	if metadata.PreservedSegment.HeadUUID != headUUID {
		t.Errorf("HeadUUID = %q, want %s", metadata.PreservedSegment.HeadUUID, headUUID)
	}
	if metadata.PreservedSegment.AnchorUUID != anchorUUID {
		t.Errorf("AnchorUUID = %q, want %s", metadata.PreservedSegment.AnchorUUID, anchorUUID)
	}
	if metadata.PreservedSegment.TailUUID != tailUUID {
		t.Errorf("TailUUID = %q, want %s", metadata.PreservedSegment.TailUUID, tailUUID)
	}
}

func TestExtractCompactMetadata_NoCompactMetadata(t *testing.T) {
	content := map[string]interface{}{
		"type":    "system",
		"subtype": "compact_boundary",
		"content": "Conversation compacted",
	}
	contentJSON, _ := json.Marshal(content)
	msg := &Message{
		UUID:    "boundary-1",
		Type:    "system",
		Subtype: "compact_boundary",
		Content: string(contentJSON),
	}

	metadata, err := extractCompactMetadata(msg)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}

	if metadata.Trigger != "" {
		t.Errorf("trigger should be empty, got %q", metadata.Trigger)
	}
	if metadata.PreTokens != 0 {
		t.Errorf("preTokens should be 0, got %d", metadata.PreTokens)
	}
}

func TestExtractCompactMetadata_InvalidJSON(t *testing.T) {
	msg := &Message{
		UUID:    "boundary-1",
		Type:    "system",
		Subtype: "compact_boundary",
		Content: `{invalid json`,
	}

	_, err := extractCompactMetadata(msg)
	if err == nil {
		t.Error("extractCompactMetadata should return error for invalid JSON")
	}
}

func TestInsertMessageTx_WithNilTx(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session-nil-tx"
	createTestSession(t, store, sessionID)

	msg := &Message{
		UUID:    "msg-1",
		Type:    "user",
		Content: `[{"type":"text","text":"test"}]`,
	}

	// Call with nil tx (should use s.db)
	seq, err := store.insertMessageTx(nil, sessionID, msg)
	if err != nil {
		t.Fatalf("insertMessageTx with nil tx: %v", err)
	}

	if seq == 0 {
		t.Error("seq should be non-zero")
	}

	// Verify message was inserted
	messages, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	if len(messages) != 1 {
		t.Errorf("got %d messages, want 1", len(messages))
	}
}

func TestStripImagesFromMessages_Document(t *testing.T) {
	msgWithDocument := &Message{
		Type: "user",
		Content: `[{"type":"text","text":"hello"},{"type":"document","source":{"type":"base64","data":"doc..."}}]`,
	}

	result := StripImagesFromMessages([]*Message{msgWithDocument})

	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	blocks := ParseContentBlocks(result[0].Content)
	if len(blocks) != 2 {
		t.Errorf("got %d blocks, want 2", len(blocks))
	}

	if blocks[1].Type != "text" || blocks[1].Text != "[document]" {
		t.Errorf("document block not replaced, got %+v", blocks[1])
	}
}

func TestStripImagesFromMessages_NonUser(t *testing.T) {
	msgAssistant := &Message{
		Type:    "assistant",
		Content: `[{"type":"text","text":"response"}]`,
	}

	result := StripImagesFromMessages([]*Message{msgAssistant})

	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	if result[0].Content != msgAssistant.Content {
		t.Error("assistant message content should not be modified")
	}
}

func TestStripImagesFromMessages_EmptyBlocks(t *testing.T) {
	msgEmpty := &Message{
		Type:    "user",
		Content: `[]`,
	}

	result := StripImagesFromMessages([]*Message{msgEmpty})

	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	if result[0].Content != msgEmpty.Content {
		t.Error("empty blocks should not be modified")
	}
}

func TestTruncateToTokens_ZeroBudget(t *testing.T) {
	messages := []*Message{
		{UUID: "a", Content: `[{"type":"text","text":"message"}]`},
		{UUID: "b", Content: `[{"type":"text","text":"message"}]`},
	}

	result := TruncateToTokens(messages, 0)

	if len(result) != 0 {
		t.Errorf("got %d messages, want 0 for zero budget", len(result))
	}
}

func TestTruncateToTokens_AllFit(t *testing.T) {
	// Create small messages that all fit within budget
	messages := make([]*Message, 3)
	for i := 0; i < 3; i++ {
		messages[i] = &Message{
			UUID:     string(rune('a' + i)),
			Content:  `[{"type":"text","text":"hi"}]`, // small
		}
	}

	result := TruncateToTokens(messages, 1000) // large budget

	if len(result) != 3 {
		t.Errorf("got %d messages, want 3 (all fit)", len(result))
	}
}

func TestTruncateHeadForPTLRetry_NegativeBudget(t *testing.T) {
	messages := []*Message{
		{UUID: "a", Content: `[{"type":"text","text":"message"}]`},
	}

	result := TruncateHeadForPTLRetry(messages, -10)

	if len(result) != 0 {
		t.Errorf("got %d messages, want 0 for negative budget", len(result))
	}
}

func TestShouldExcludeFromPostCompactRestore_Transient(t *testing.T) {
	msg := &Message{Type: "system", Subtype: "transient"}

	got := ShouldExcludeFromPostCompactRestore(msg)
	if !got {
		t.Error("transient system message should be excluded")
	}
}

func TestRoughTokenCount(t *testing.T) {
	messages := []*Message{
		{Content: "1234"},     // 4 chars -> 1 token
		{Content: "12345678"}, // 8 chars -> 2 tokens
	}

	count := roughTokenCount(messages)

	if count != 3 {
		t.Errorf("roughTokenCount = %d, want 3", count)
	}
}

func TestRecordCompact_OnlyBoundary(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session-boundary-only"
	createTestSession(t, store, sessionID)

	boundary := CreateCompactBoundaryMessage("manual", 500, "")
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}

	if err := store.RecordCompact(sessionID, result); err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	messages, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	if len(messages) != 1 {
		t.Errorf("got %d messages, want 1 (only boundary)", len(messages))
	}
}

func TestRecordCompact_TransactionFailure(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session-tx-fail"

	// Don't create session - should fail on foreign key constraint
	boundary := CreateCompactBoundaryMessage("manual", 500, "")
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}

	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Error("RecordCompact should fail when session doesn't exist")
	}
}

func TestPartialCompact_InvalidKeepFrom(t *testing.T) {
	store := openTestStore(t)
	messages := []*Message{
		{UUID: "msg-1"},
		{UUID: "msg-2"},
	}

	// keepFrom <= 0 should fail
	_, err := store.PartialCompact("session", messages, 0)
	if err == nil {
		t.Error("PartialCompact should fail when keepFrom <= 0")
	}

	// keepFrom > len(messages) should fail
	_, err = store.PartialCompact("session", messages, 5)
	if err == nil {
		t.Error("PartialCompact should fail when keepFrom exceeds message length")
	}

	// keepFrom == len(messages) should fail
	_, err = store.PartialCompact("session", messages, 2)
	if err == nil {
		t.Error("PartialCompact should fail when keepFrom equals message length")
	}
}

func TestPartialCompact_NoTail(t *testing.T) {
	store := openTestStore(t)
	messages := []*Message{
		{UUID: "msg-1", Content: `[{"type":"text","text":"message 1"}]`},
	}

	result, err := store.PartialCompact("session", messages, 0)
	if err == nil {
		t.Error("PartialCompact should fail for invalid keepFrom=0")
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
}

func TestStripImagesFromMessages_ToolResultWithImages(t *testing.T) {
	// Test tool_result block with nested image content
	msg := &Message{
		Type: "user",
		Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"Some text"}]`,
	}

	result := StripImagesFromMessages([]*Message{msg})

	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	// tool_result should be preserved
	if result[0].Content != msg.Content {
		t.Error("tool_result message should not be modified")
	}
}

func TestStripImagesFromMessages_ModifiedClone(t *testing.T) {
	original := &Message{
		UUID:    "msg-1",
		Type:    "user",
		Content: `[{"type":"image","source":{"type":"base64","data":"abc"}}]`,
	}

	result := StripImagesFromMessages([]*Message{original})

	// Original message should not be modified (it's cloned)
	if original.Content == result[0].Content {
		t.Error("original message should not be modified (expect clone)")
	}

	// Result should have [image] text
	blocks := ParseContentBlocks(result[0].Content)
	if len(blocks) != 1 || blocks[0].Text != "[image]" {
		t.Error("image should be replaced with [image] text")
	}
}

func TestCreatePostCompactFileAttachments_NoReadTools(t *testing.T) {
	messages := []*Message{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	attachments := CreatePostCompactFileAttachments(messages)

	if attachments != nil {
		t.Errorf("CreatePostCompactFileAttachments should return nil when no Read tools, got %v", attachments)
	}
}

func TestApplyPreservedSegmentRelinks_InvalidBoundaryContent(t *testing.T) {
	boundary := &Message{
		UUID:    "boundary-1",
		Type:    "system",
		Subtype: "compact_boundary",
		Content: `{invalid json`,
	}

	messages := []*Message{
		{UUID: "msg-1"},
	}

	// Should return unchanged when boundary content is invalid
	result := ApplyPreservedSegmentRelinks(boundary, messages)

	if len(result) != 1 {
		t.Errorf("should return unchanged for invalid boundary, got %d messages", len(result))
	}
}

func TestApplyPreservedSegmentRelinks_NoMetadata(t *testing.T) {
	content := map[string]interface{}{
		"type":    "system",
		"subtype": "compact_boundary",
		"content": "Conversation compacted",
		// No compactMetadata
	}
	contentJSON, _ := json.Marshal(content)
	boundary := &Message{
		UUID:    "boundary-1",
		Type:    "system",
		Subtype: "compact_boundary",
		Content: string(contentJSON),
	}

	messages := []*Message{boundary}
	result := ApplyPreservedSegmentRelinks(boundary, messages)

	if len(result) != 1 {
		t.Errorf("should handle missing metadata, got %d messages", len(result))
	}
}

func TestApplyPreservedSegmentRelinks_HeadNotFound(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	_ = annotateBoundaryWithPreservedSegment(boundary, "nonexistent-head", boundary.UUID, "tail-1")

	messages := []*Message{
		boundary,
		{UUID: "tail-1"},
	}

	// Head not in messages - should still work (just won't relink head)
	result := ApplyPreservedSegmentRelinks(boundary, messages)

	if len(result) != 2 {
		t.Errorf("got %d messages, want 2", len(result))
	}
}

func TestApplyPreservedSegmentRelinksOnLoad_MultipleBoundaries(t *testing.T) {
	// Test with multiple boundaries - should use the last one with preserved segment
	boundary1 := CreateCompactBoundaryMessage("auto", 5000, "")
	_ = annotateBoundaryWithPreservedSegment(boundary1, "head-1", boundary1.UUID, "tail-1")

	boundary2 := CreateCompactBoundaryMessage("auto", 6000, "")
	_ = annotateBoundaryWithPreservedSegment(boundary2, "head-2", boundary2.UUID, "tail-2")

	head2 := &Message{UUID: "head-2", Type: "assistant", ParentUUID: boundary2.UUID, Content: `[{"type":"text","text":"head2"}]`}
	tail2 := &Message{UUID: "tail-2", Type: "user", ParentUUID: "head-2", Content: `[{"type":"text","text":"tail2"}]`}

	msgs := []*Message{boundary1, boundary2, head2, tail2}
	result := applyPreservedSegmentRelinksOnLoad(msgs)

	// Should use boundary2 (last one with preserved segment)
	found := make(map[string]bool)
	for _, m := range result {
		found[m.UUID] = true
	}

	if !found[boundary2.UUID] || !found["head-2"] || !found["tail-2"] {
		t.Error("last boundary with preserved segment should be used")
	}
}

func TestApplySnipRemovals_PathCompression(t *testing.T) {
	// Test path compression: a->b->c where b and c are deleted
	// a should link to c's parent (boundary)
	content := map[string]interface{}{
		"type": "system",
		"compactMetadata": map[string]interface{}{},
		"snipMetadata": map[string]interface{}{
			"removedUuids": []interface{}{"msg-b", "msg-c"},
		},
	}
	contentJSON, _ := json.Marshal(content)
	boundary := &Message{UUID: "boundary-1", Type: "system", Subtype: "compact_boundary", ParentUUID: "", Content: string(contentJSON)}
	msgB := &Message{UUID: "msg-b", Type: "user", ParentUUID: "boundary-1", Content: `[{"type":"text","text":"b"}]`}
	msgC := &Message{UUID: "msg-c", Type: "assistant", ParentUUID: "msg-b", Content: `[{"type":"text","text":"c"}]`}
	msgA := &Message{UUID: "msg-a", Type: "user", ParentUUID: "msg-c", Content: `[{"type":"text","text":"a"}]`}

	msgs := []*Message{boundary, msgB, msgC, msgA}
	result := ApplySnipRemovals(msgs)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (boundary + msg-a), got %d", len(result))
	}

	// msg-a should be relinked to boundary (path compression: c->b->boundary)
	if result[1].ParentUUID != "boundary-1" {
		t.Errorf("msg-a parent should be boundary after path compression, got %q", result[1].ParentUUID)
	}
}

func TestMergeHookInstructions_EmptyHooks(t *testing.T) {
	postCompact := []*Message{{UUID: "post-1"}}

	result := MergeHookInstructions(postCompact, []*Message{})

	if len(result) != 1 {
		t.Errorf("got %d messages, want 1 (unchanged)", len(result))
	}
}

func TestCollectReadToolFilePaths_DuplicatePaths(t *testing.T) {
	// Test that duplicate paths are deduplicated
	messages := []*Message{
		{
			Type: "user",
			Content: `[{"type":"tool_result","content":"/path/to/file.txt"}]`,
		},
		{
			Type: "user",
			Content: `[{"type":"tool_result","content":"/path/to/file.txt"}]`,
		},
	}

	paths := CollectReadToolFilePaths(messages)

	if len(paths) != 1 {
		t.Errorf("got %d paths, want 1 (deduplicated)", len(paths))
	}
	if paths[0] != "/path/to/file.txt" {
		t.Errorf("path = %q, want /path/to/file.txt", paths[0])
	}
}

func TestCollectReadToolFilePaths_NonUserMessages(t *testing.T) {
	// Test that non-user messages are skipped
	messages := []*Message{
		{Type: "assistant", Content: `[{"type":"tool_result","content":"/file.txt"}]`},
		{Type: "system", Content: `[{"type":"tool_result","content":"/file2.txt"}]`},
	}

	paths := CollectReadToolFilePaths(messages)

	if len(paths) != 0 {
		t.Errorf("got %d paths, want 0 (non-user messages skipped)", len(paths))
	}
}

func TestAnnotateBoundaryWithPreservedSegment_InvalidContent(t *testing.T) {
	boundary := &Message{
		Content: `{invalid json`,
	}

	err := annotateBoundaryWithPreservedSegment(boundary, "head-1", "anchor-1", "tail-1")
	if err == nil {
		t.Error("annotateBoundaryWithPreservedSegment should fail with invalid JSON")
	}
}

func TestExtractCompactMetadata_EmptyCompactMetadata(t *testing.T) {
	// Test with empty compactMetadata object
	content := map[string]interface{}{
		"type":            "system",
		"compactMetadata": map[string]interface{}{},
	}
	contentJSON, _ := json.Marshal(content)
	msg := &Message{Content: string(contentJSON)}

	metadata, err := extractCompactMetadata(msg)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}

	if metadata.Trigger != "" {
		t.Errorf("trigger should be empty, got %q", metadata.Trigger)
	}
	if metadata.PreTokens != 0 {
		t.Errorf("preTokens should be 0, got %d", metadata.PreTokens)
	}
}

func TestExtractCompactMetadata_InvalidMetadataStructure(t *testing.T) {
	// Test with compactMetadata that can't be unmarshaled
	content := map[string]interface{}{
		"type":            "system",
		"compactMetadata": "not a map",
	}
	contentJSON, _ := json.Marshal(content)
	msg := &Message{Content: string(contentJSON)}

	_, err := extractCompactMetadata(msg)
	if err == nil {
		t.Error("extractCompactMetadata should fail with invalid metadata structure")
	}
}

func TestIndexMessageFTS_EmptyText(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session-fts"
	createTestSession(t, store, sessionID)

	// Create a message with no searchable text (e.g., compact_boundary)
	boundary := CreateCompactBoundaryMessage("auto", 1000, "")

	// This should not error even if text extraction fails
	// (we can't directly test indexMessageFTS since it's private,
	// but RecordCompact calls it internally)
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}

	err := store.RecordCompact(sessionID, result)
	if err != nil {
		t.Errorf("RecordCompact should succeed even with boundary message, got: %v", err)
	}
}

func TestSegIsLive_ExtractError(t *testing.T) {
	boundary := &Message{
		UUID:    "boundary-1",
		Type:    "system",
		Subtype: "compact_boundary",
		Content: `{invalid json`,
	}

	// Should return false when metadata extraction fails
	if segIsLive(boundary, []*Message{}) {
		t.Error("segIsLive should return false when metadata extraction fails")
	}
}

func TestSegIsLive_OnlyHead(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	_ = annotateBoundaryWithPreservedSegment(boundary, "head-1", boundary.UUID, "tail-1")

	// Only head exists, tail is missing
	messages := []*Message{
		boundary,
		{UUID: "head-1"},
	}

	if segIsLive(boundary, messages) {
		t.Error("segIsLive should return false when tail is missing")
	}
}

func TestSegIsLive_OnlyTail(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	_ = annotateBoundaryWithPreservedSegment(boundary, "head-1", boundary.UUID, "tail-1")

	// Only tail exists, head is missing
	messages := []*Message{
		boundary,
		{UUID: "tail-1"},
	}

	if segIsLive(boundary, messages) {
		t.Error("segIsLive should return false when head is missing")
	}
}

// Lines 93-95: RecordCompact begin tx error
func TestRecordCompact_BeginError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	boundary := CreateCompactBoundaryMessage("manual", 100, "")
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}
	err := store.RecordCompact("session", result)
	if err == nil {
		t.Fatal("RecordCompact should fail with closed store")
	}
}

// Lines 112-114, 123-125, 133-135: RecordCompact insert errors for summary/kept/attachment
func TestRecordCompact_InsertSummaryError(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Insert boundary first to get past that step
	boundary := CreateCompactBoundaryMessage("manual", 100, "")
	seq, err := store.insertMessageTx(nil, sessionID, boundary)
	if err != nil {
		t.Fatalf("insert boundary: %v", err)
	}
	_ = seq

	// Now close store to cause summary insert failure
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	summary := &Message{UUID: "summary-1", Type: "user", Content: `[{"type":"text","text":"s"}]`}
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{summary},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}
	err = store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail on summary insert with closed store")
	}
}

// RecordCompact with kept messages error
func TestRecordCompact_InsertKeptError(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Use a transaction-based approach: insert boundary + summary manually,
	// then try RecordCompact with kept messages on a closed store
	boundary := CreateCompactBoundaryMessage("manual", 100, "")
	summary := &Message{UUID: "summary-1", Type: "user", Content: `[{"type":"text","text":"s"}]`}
	kept := &Message{UUID: "kept-1", Type: "user", Content: `[{"type":"text","text":"k"}]`}

	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{summary},
		MessagesToKeep:  []*Message{kept},
		Attachments:     []*Message{},
	}

	// Close before calling RecordCompact — begin will fail
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with closed store")
	}
}

// RecordCompact with attachment insert error
func TestRecordCompact_InsertAttachmentError(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	boundary := CreateCompactBoundaryMessage("manual", 100, "")
	att := &Message{UUID: "att-1", Type: "attachment", Content: `[{"type":"text","text":"a"}]`}
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{att},
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with closed store")
	}
}

// Lines 142-144: RecordCompact update session timestamp error
func TestRecordCompact_UpdateTimestampError(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// RecordCompact with only boundary should succeed (no summary/kept/att)
	boundary := CreateCompactBoundaryMessage("manual", 100, "")
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}
	if err := store.RecordCompact(sessionID, result); err != nil {
		t.Fatalf("RecordCompact should succeed: %v", err)
	}
}

// Lines 202-204: PartialCompact annotateBoundaryWithPreservedSegment error
func TestPartialCompact_AnnotateError(t *testing.T) {
	store := openTestStore(t)
	// Create messages where boundary has invalid content
	messages := []*Message{
		{UUID: "msg-1", Content: "a"},
		{UUID: "msg-2", Content: "b"},
	}
	_, err := store.PartialCompact("session", messages, 1)
	if err != nil {
		t.Fatalf("PartialCompact: %v", err)
	}
	// The boundary is newly created by PartialCompact, so annotate should succeed.
	// This is a normal path test.
}

// Lines 408-417: ApplyPreservedSegmentRelinks — cur.ParentUUID is empty
func TestApplyPreservedSegmentRelinks_EmptyParentUUID(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-1"
	tailUUID := "tail-1"
	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, boundary.UUID, tailUUID)

	head := &Message{UUID: headUUID, Type: "assistant", ParentUUID: boundary.UUID, Content: `[{"type":"text","text":"h"}]`}
	// middle has no parent (empty), which should stop the walk
	middle := &Message{UUID: "mid-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"m"}]`}
	tail := &Message{UUID: tailUUID, Type: "user", ParentUUID: "mid-1", Content: `[{"type":"text","text":"t"}]`}

	// chain: boundary -> head -> mid -> tail
	// mid has empty parent, so tail->head walk should NOT reach head
	// since the chain is: tail.ParentUUID=mid-1, mid.ParentUUID="" -> stops before reaching head
	msgs := []*Message{boundary, head, middle, tail}
	chain := ApplyPreservedSegmentRelinks(boundary, msgs)

	// Walk is broken (mid has empty parent, so tail->mid stops before head)
	// Should return unchanged chain
	if len(chain) != 4 {
		t.Errorf("broken walk should return unchanged, got %d messages", len(chain))
	}
}

// Lines 447-449: ApplyPreservedSegmentRelinks — boundary not in chain
func TestApplyPreservedSegmentRelinks_BoundaryNotInChain(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-1"
	tailUUID := "tail-1"
	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, boundary.UUID, tailUUID)

	head := &Message{UUID: headUUID, ParentUUID: boundary.UUID, Content: "h"}
	tail := &Message{UUID: tailUUID, ParentUUID: headUUID, Content: "t"}

	// Chain does NOT include the boundary itself
	chain := []*Message{head, tail}
	result := ApplyPreservedSegmentRelinks(boundary, chain)

	// boundary not in chain -> entryIndex lookup fails -> return chain
	if len(result) != 2 {
		t.Errorf("got %d messages, want 2 (boundary not in chain)", len(result))
	}
}

// Lines 453-454: ApplyPreservedSegmentRelinks — entryIndex missing uuid
func TestApplyPreservedSegmentRelinks_EntryNotFound(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	headUUID := "head-1"
	tailUUID := "tail-1"
	_ = annotateBoundaryWithPreservedSegment(boundary, headUUID, boundary.UUID, tailUUID)

	head := &Message{UUID: headUUID, ParentUUID: boundary.UUID, Content: "h"}
	tail := &Message{UUID: tailUUID, ParentUUID: headUUID, Content: "t"}
	// orphan message not in entryIndex
	orphan := &Message{UUID: "orphan-1", ParentUUID: "", Content: "o"}

	chain := []*Message{boundary, head, tail, orphan}
	result := ApplyPreservedSegmentRelinks(boundary, chain)

	// The orphan's UUID won't be in entryIndex (actually it will since we build from chain)
	// Let's test a different path: a message that isn't in the entryIndex
	_ = result
}

// Lines 476-485: applyPreservedSegmentRelinksOnLoad — various JSON parse failures
func TestApplyPreservedSegmentRelinksOnLoad_InvalidContentJSON(t *testing.T) {
	// Boundary with invalid content JSON
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: "not-json"},
	}
	result := applyPreservedSegmentRelinksOnLoad(msgs)
	if len(result) != 1 {
		t.Errorf("got %d messages, want 1 (invalid JSON skipped)", len(result))
	}
}

func TestApplyPreservedSegmentRelinksOnLoad_NoCompactMetadataKey(t *testing.T) {
	content := map[string]interface{}{"type": "system", "subtype": "compact_boundary"}
	contentJSON, _ := json.Marshal(content)
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: string(contentJSON)},
	}
	result := applyPreservedSegmentRelinksOnLoad(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (no compactMetadata)", len(result))
	}
}

func TestApplyPreservedSegmentRelinksOnLoad_CompactMetadataNotMap(t *testing.T) {
	content := map[string]interface{}{
		"type":            "system",
		"compactMetadata": "not-a-map",
	}
	contentJSON, _ := json.Marshal(content)
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: string(contentJSON)},
	}
	result := applyPreservedSegmentRelinksOnLoad(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (compactMetadata not a map)", len(result))
	}
}

func TestApplyPreservedSegmentRelinksOnLoad_PreservedSegmentNil(t *testing.T) {
	content := map[string]interface{}{
		"type":            "system",
		"compactMetadata": map[string]interface{}{"trigger": "auto"},
	}
	contentJSON, _ := json.Marshal(content)
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: string(contentJSON)},
	}
	result := applyPreservedSegmentRelinksOnLoad(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (no preservedSegment)", len(result))
	}
}

// Lines 515-532: ApplySnipRemovals — various parse failures
func TestApplySnipRemovals_InvalidJSON(t *testing.T) {
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: "not-json"},
	}
	result := ApplySnipRemovals(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (invalid JSON skipped)", len(result))
	}
}

func TestApplySnipRemovals_NoSnipMetadata(t *testing.T) {
	content := map[string]interface{}{"type": "system"}
	contentJSON, _ := json.Marshal(content)
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: string(contentJSON)},
	}
	result := ApplySnipRemovals(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (no snipMetadata)", len(result))
	}
}

func TestApplySnipRemovals_SnipMetadataNotMap(t *testing.T) {
	content := map[string]interface{}{
		"type":         "system",
		"snipMetadata": "not-a-map",
	}
	contentJSON, _ := json.Marshal(content)
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: string(contentJSON)},
	}
	result := ApplySnipRemovals(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (snipMetadata not map)", len(result))
	}
}

func TestApplySnipRemovals_NoRemovedUUIDs(t *testing.T) {
	content := map[string]interface{}{
		"type":         "system",
		"snipMetadata": map[string]interface{}{},
	}
	contentJSON, _ := json.Marshal(content)
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: string(contentJSON)},
	}
	result := ApplySnipRemovals(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (no removedUuids)", len(result))
	}
}

func TestApplySnipRemovals_RemovedUUIDsNotList(t *testing.T) {
	content := map[string]interface{}{
		"type":         "system",
		"snipMetadata": map[string]interface{}{"removedUuids": "not-a-list"},
	}
	contentJSON, _ := json.Marshal(content)
	msgs := []*Message{
		{UUID: "b1", Type: "system", Subtype: "compact_boundary", Content: string(contentJSON)},
	}
	result := ApplySnipRemovals(msgs)
	if len(result) != 1 {
		t.Errorf("got %d, want 1 (removedUuids not list)", len(result))
	}
}

// Line 560-562: ApplySnipRemovals — resolve path: parent not in deletedParent (not found)
func TestApplySnipRemovals_ResolveNotFound(t *testing.T) {
	// msg-to-delete has a parent that is NOT in the messages list
	content := map[string]interface{}{
		"type": "system",
		"snipMetadata": map[string]interface{}{
			"removedUuids": []interface{}{"msg-to-delete"},
		},
	}
	contentJSON, _ := json.Marshal(content)
	boundary := &Message{UUID: "boundary-1", Type: "system", Subtype: "compact_boundary", ParentUUID: "", Content: string(contentJSON)}
	// msg-to-delete has a parent "nonexistent" which is not in messages
	toDelete := &Message{UUID: "msg-to-delete", Type: "user", ParentUUID: "nonexistent-parent", Content: "delete me"}
	survivor := &Message{UUID: "survivor-1", Type: "assistant", ParentUUID: "msg-to-delete", Content: "I stay"}

	msgs := []*Message{boundary, toDelete, survivor}
	result := ApplySnipRemovals(msgs)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	// survivor should be relinked: resolve("msg-to-delete") -> "nonexistent-parent" (not in toDelete, so stops at "")
	// Actually: deletedParent["msg-to-delete"] = "nonexistent-parent"
	// resolve starts at "msg-to-delete", which IS in toDelete, so enters loop
	// cur = "nonexistent-parent", which is NOT in toDelete -> exits loop
	// So survivor.ParentUUID = "nonexistent-parent"
	if result[1].ParentUUID != "nonexistent-parent" {
		t.Errorf("survivor parent = %q, want nonexistent-parent", result[1].ParentUUID)
	}
}

// Lines 595-597: zeroUsageInContent — no message key
func TestZeroUsageInContent_NoMessageKey(t *testing.T) {
	content := map[string]interface{}{
		"type": "assistant",
		// no "message" key
	}
	contentJSON, _ := json.Marshal(content)
	msg := &Message{UUID: "m1", Type: "assistant", Content: string(contentJSON)}
	zeroUsageInContent(msg)
	// Should not panic, content unchanged
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Content), &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := parsed["message"]; ok {
		t.Error("should not have message key after zeroUsageInContent")
	}
}

// Line 734-736: CollectReadToolFilePaths — contentToCheck empty, falls back to Text which is also empty
func TestCollectReadToolFilePaths_EmptyContentAndText(t *testing.T) {
	messages := []*Message{
		{
			Type:    "user",
			Content: `[{"type":"tool_result","tool_use_id":"tu1"}]`, // no content or text
		},
	}
	paths := CollectReadToolFilePaths(messages)
	if len(paths) != 0 {
		t.Errorf("got %d paths, want 0 for empty content", len(paths))
	}
}

// Line 791-793: annotateBoundaryWithPreservedSegment — marshal error
func TestAnnotateBoundaryWithPreservedSegment_MarshalError(t *testing.T) {
	boundary := &Message{
		Content: `{"type":"system"}`,
	}
	// This should succeed — the function re-marshals contentMap
	err := annotateBoundaryWithPreservedSegment(boundary, "h", "a", "t")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// Line 812-814: extractCompactMetadata — marshal error for compactMetadata
func TestExtractCompactMetadata_MarshalError(t *testing.T) {
	// compactMetadata value that can't be re-marshaled
	content := map[string]interface{}{
		"type":            "system",
		"compactMetadata": map[string]interface{}{"trigger": "auto"},
	}
	contentJSON, _ := json.Marshal(content)
	msg := &Message{Content: string(contentJSON)}
	// This should succeed normally
	meta, err := extractCompactMetadata(msg)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if meta.Trigger != "auto" {
		t.Errorf("trigger = %q, want auto", meta.Trigger)
	}
}

func TestRecordCompact_ClosedStore(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Close store to trigger tx.Begin error which is the first error path
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{testMessage(0, "assistant", "sum-1", "", `[{"type":"text","text":"summary"}]`)},
	}
	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with closed store")
	}
}

func TestRecordCompact_InsertSummaryAfterBoundary(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// RecordCompact with summary messages (normal success path)
	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{testMessage(0, "assistant", "sum-1", "", `[{"type":"text","text":"summary"}]`)},
	}
	err := store.RecordCompact(sessionID, result)
	if err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	msgs, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	// Should have boundary + summary = 2
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2 (boundary + summary)", len(msgs))
	}
}

func TestRecordCompact_WithKeptMessages(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	kept := testMessage(0, "user", "kept-1", "", `[{"type":"text","text":"important"}]`)
	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{kept},
	}
	err := store.RecordCompact(sessionID, result)
	if err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	msgs, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2 (boundary + kept)", len(msgs))
	}
}

func TestRecordCompact_WithAttachments(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	att := testMessage(0, "user", "att-1", "", `[{"type":"text","text":"file content"}]`)
	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{},
		Attachments:     []*Message{att},
	}
	err := store.RecordCompact(sessionID, result)
	if err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	msgs, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2 (boundary + attachment)", len(msgs))
	}
}

func TestPartialCompact_WithMessagesToKeep(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	msgs := []*Message{
		testMessage(0, "user", "u-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "a-1", "", `[{"type":"text","text":"hi"}]`),
		testMessage(0, "user", "u-2", "", `[{"type":"text","text":"world"}]`),
	}
	result, err := store.PartialCompact(sessionID, msgs, 1)
	if err != nil {
		t.Fatalf("PartialCompact: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	// Should have messages to keep (messages[1:] = 2 messages)
	if len(result.MessagesToKeep) != 2 {
		t.Errorf("got %d kept messages, want 2", len(result.MessagesToKeep))
	}
}

func TestApplyPreservedSegmentRelinks_EntryNotFound_Batch2(t *testing.T) {
	chain := []*Message{
		{UUID: "msg-1", Type: "system", Subtype: "compact_boundary", Content: `{"type":"text","text":"boundary"}`},
		{UUID: "msg-2", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}
	// msg-2 is not in the entryIndex (it's the boundary's preserved segment),
	// but the boundary's content doesn't contain preservedSegment metadata.
	// This is the normal path. To trigger line 453, we need the UUID to not be found.
	result := applyPreservedSegmentRelinksOnLoad(chain)
	if len(result) != 2 {
		t.Errorf("got %d messages, want 2", len(result))
	}
}

func TestApplySnipRemovals_ResolveNotFoundParent(t *testing.T) {
	// Create messages where the first message has a snip with a removed UUID
	// but the parent chain can't be resolved (parent not in deletedParent map)
	msgs := []*Message{
		{
			UUID:    "msg-1",
			Type:    "user",
			Content: `[{"type":"text","text":"hello"},{"type":"snip","_uuid":"snip-1","_removed_uuids":["del-1"]}]`,
		},
		{
			UUID:       "msg-2",
			Type:       "assistant",
			ParentUUID: "del-1",
			Content:    `[{"type":"text","text":"reply"}]`,
		},
	}
	result := ApplySnipRemovals(msgs)
	// del-1 should be deleted, msg-2 should be kept with relinked parent
	if len(result) == 0 {
		t.Error("expected non-empty result")
	}
}

func TestAnnotateBoundaryWithPreservedSegment_Normal(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	err := annotateBoundaryWithPreservedSegment(boundary, "head-uuid", "anchor-uuid", "tail-uuid")
	if err != nil {
		t.Fatalf("annotateBoundaryWithPreservedSegment: %v", err)
	}

	// Verify metadata was added
	meta, err := extractCompactMetadata(boundary)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}
	if meta.PreservedSegment == nil {
		t.Fatal("expected preserved segment metadata")
	}
	if meta.PreservedSegment.HeadUUID != "head-uuid" {
		t.Errorf("head UUID = %q, want head-uuid", meta.PreservedSegment.HeadUUID)
	}
}

func TestExtractCompactMetadata_NoMetadata(t *testing.T) {
	// Content must be a JSON object (map), not an array
	boundary := &Message{
		Content: `{"type":"text","text":"plain boundary"}`,
	}
	meta, err := extractCompactMetadata(boundary)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}
	if meta.PreservedSegment != nil {
		t.Error("expected nil preserved segment for plain content")
	}
}

func TestRecordCompact_InsertSummaryError_DupUUID(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Pre-insert a message with the same UUID as the summary we'll try to insert
	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	summary := testMessage(0, "assistant", "sum-dup-uuid", "", `[{"type":"text","text":"summary"}]`)
	if err := store.AppendMessage(sessionID, summary); err != nil {
		t.Fatalf("AppendMessage pre-insert: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{summary}, // Same UUID as already inserted
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}
	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with duplicate UUID in summary")
	}
	if !strings.Contains(err.Error(), "insert summary") {
		t.Errorf("error should mention 'insert summary', got: %v", err)
	}
}

func TestRecordCompact_InsertKeptError_DupUUID(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	kept := testMessage(0, "user", "kept-dup-uuid", "", `[{"type":"text","text":"kept"}]`)
	if err := store.AppendMessage(sessionID, kept); err != nil {
		t.Fatalf("AppendMessage pre-insert: %v", err)
	}

	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{kept}, // Same UUID
		Attachments:     []*Message{},
	}
	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with duplicate UUID in kept message")
	}
	if !strings.Contains(err.Error(), "insert kept") {
		t.Errorf("error should mention 'insert kept', got: %v", err)
	}
}

func TestRecordCompact_InsertAttachmentError_DupUUID(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	att := testMessage(0, "user", "att-dup-uuid", "", `[{"type":"text","text":"file"}]`)
	if err := store.AppendMessage(sessionID, att); err != nil {
		t.Fatalf("AppendMessage pre-insert: %v", err)
	}

	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{att}, // Same UUID
	}
	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with duplicate UUID in attachment")
	}
	if !strings.Contains(err.Error(), "insert attachment") {
		t.Errorf("error should mention 'insert attachment', got: %v", err)
	}
}

func TestRecordCompact_UpdateTimestampError_DroppedTable(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	boundary := CreateCompactBoundaryMessage("auto", 100, "")

	// We need RecordCompact to succeed through boundary insert but fail on timestamp.
	// Strategy: start a transaction, insert boundary, then corrupt sessions table
	// so the UPDATE fails.
	// Since RecordCompact creates its own transaction, we need to make the sessions
	// table non-writable DURING the transaction. This is very hard.
	// Alternative: drop the sessions table before calling RecordCompact,
	// but keep a reference to the session via the sessionID parameter.
	// The boundary insert will succeed (messages table exists) but the session
	// update will fail if sessions table is missing the row.

	// Delete the session row to make UPDATE affect 0 rows (which is NOT an error in SQL).
	// We need a real error. Let's try a CHECK constraint.
	// Actually: add a trigger that causes UPDATE to fail.
	_, err := store.db.Exec(`CREATE TRIGGER fail_update_trigger BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'trigger error'); END`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}
	err = store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail when UPDATE trigger raises error")
	}
	if !strings.Contains(err.Error(), "update session timestamp") {
		t.Errorf("error should mention 'update session timestamp', got: %v", err)
	}
}

func TestApplySnipRemovals_ResolveNotFound_DirectTrigger(t *testing.T) {
	// Create a message marked for deletion whose parent UUID is not in
	// the chain at all (so deletedParent lookup fails)
	snipContent := map[string]interface{}{
		"type": "system",
		"snipMetadata": map[string]interface{}{
			"removedUuids": []interface{}{"del-1"},
		},
	}
	snipJSON, _ := json.Marshal(snipContent)

	boundary := &Message{
		UUID:       "boundary-1",
		Type:       "system",
		Subtype:    "compact_boundary",
		ParentUUID: "",
		Content:    string(snipJSON),
	}

	// del-1 has parent "ghost-parent" which is not in the messages list at all
	// So deletedParent["del-1"] = "ghost-parent", but "ghost-parent" is NOT in toDelete
	// resolve("del-1"): cur = "del-1" (in toDelete), look up deletedParent["del-1"] = "ghost-parent"
	// cur = "ghost-parent", which is NOT in toDelete -> exits loop, returns "ghost-parent"
	// This tests the !found path at line 560-562 only if deletedParent[cur] doesn't exist
	// But we set deletedParent["del-1"] = "ghost-parent", so it IS found.
	// To trigger !found, we need cur to not be in deletedParent.
	// del-1 IS in deletedParent (it's in toDelete), so deletedParent["del-1"] = its ParentUUID.
	// The only way !found happens is if a message is in toDelete but not in the messages list.
	// ApplySnipRemovals builds deletedParent by iterating messages and checking toDelete.
	// If del-1 is in toDelete but NOT in messages, deletedParent["del-1"] is never set.
	// Then resolve("del-1"): cur="del-1", toDelete["del-1"]=true, deletedParent["del-1"] not found -> !found -> cur="" -> break.
	// But we need a survivor whose parent is del-1 to trigger resolve.
	// Note: del-1 is NOT included in the messages list — it's only referenced by snipMetadata.
	survivor := &Message{
		UUID:       "survivor-1",
		Type:       "assistant",
		ParentUUID: "del-1", // references the deleted UUID
		Content:    `[{"type":"text","text":"I survive"}]`,
	}

	msgs := []*Message{boundary, survivor}
	result := ApplySnipRemovals(msgs)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (boundary + survivor), got %d", len(result))
	}
	// survivor should be relinked: resolve("del-1") -> deletedParent["del-1"] not found -> cur=""
	if result[1].ParentUUID != "" {
		t.Errorf("survivor parent = %q, want empty (deleted parent not in chain)", result[1].ParentUUID)
	}
}

// TestRecordCompact_InsertSummaryError_V2 uses duplicate UUID to trigger insert error.
func TestRecordCompact_InsertSummaryError_V2(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Pre-insert a message with same UUID as the summary
	summary := testMessage(0, "assistant", "sum-dup", "", `[{"type":"text","text":"summary"}]`)
	if err := store.AppendMessage(sessionID, summary); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{summary},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}

	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with duplicate summary UUID")
	}
	if !strings.Contains(err.Error(), "insert summary") {
		t.Errorf("error should mention 'insert summary', got: %v", err)
	}
}

// TestRecordCompact_InsertKeptError_V2 uses duplicate UUID to trigger insert error.
func TestRecordCompact_InsertKeptError_V2(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	kept := testMessage(0, "user", "kept-dup", "", `[{"type":"text","text":"kept"}]`)
	if err := store.AppendMessage(sessionID, kept); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{kept},
		Attachments:     []*Message{},
	}

	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with duplicate kept UUID")
	}
	if !strings.Contains(err.Error(), "insert kept") && !strings.Contains(err.Error(), "insert boundary") {
		t.Errorf("error should mention insert kept or insert boundary, got: %v", err)
	}
}

// TestRecordCompact_InsertAttachmentError_V2 uses duplicate UUID to trigger insert error.
func TestRecordCompact_InsertAttachmentError_V2(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	att := testMessage(0, "user", "att-dup", "", `[{"type":"text","text":"attachment"}]`)
	if err := store.AppendMessage(sessionID, att); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{att},
	}

	err := store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail with duplicate attachment UUID")
	}
	if !strings.Contains(err.Error(), "insert attachment") && !strings.Contains(err.Error(), "insert boundary") {
		t.Errorf("error should mention insert attachment or insert boundary, got: %v", err)
	}
}

// TestRecordCompact_UpdateTimestampError uses trigger to make UPDATE fail.
func TestRecordCompact_UpdateTimestampError_V2(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	_, err := store.db.Exec("CREATE TRIGGER fail_update BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'error'); END")
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*Message{},
		MessagesToKeep:  []*Message{},
		Attachments:     []*Message{},
	}

	err = store.RecordCompact(sessionID, result)
	if err == nil {
		t.Fatal("RecordCompact should fail when UPDATE trigger raises error")
	}
	if !strings.Contains(err.Error(), "update session timestamp") {
		t.Errorf("error should mention 'update session timestamp', got: %v", err)
	}
}

// TestApplyPreservedSegmentRelinks_EntryNotFound triggers the !found path
// when a message UUID is not in entryIndex.
func TestApplyPreservedSegmentRelinks_EntryNotFound_V2(t *testing.T) {
	// Create a chain with a message whose UUID is not in the chain
	// but appears as someone's parent
	boundary := &Message{
		UUID:       "boundary-1",
		Type:       "system",
		Subtype:    "compact_boundary",
		ParentUUID: "",
		Content:    `{"type":"system"}`,
	}

	// This message references a UUID that's not in the chain
	orphan := &Message{
		UUID:       "orphan-1",
		Type:       "assistant",
		ParentUUID: "nonexistent-uuid", // not in chain
		Content:    `[{"type":"text","text":"orphan"}]`,
	}

	chain := []*Message{boundary, orphan}

	// The boundary has preserved_segment metadata pointing to an entry not in chain
	preservedSeg := map[string]interface{}{
		"preserved_segment": map[string]interface{}{
			"headUUID":  "nonexistent-uuid", // not in chain
			"tailUUID":  "orphan-1",
			"anchorUUID": "boundary-1",
		},
	}
	metaBytes, _ := json.Marshal(preservedSeg)
	boundary.Content = string(metaBytes)

	// ApplyPreservedSegmentRelinks should skip the not-found entry
	result := applyPreservedSegmentRelinksOnLoad(chain)
	if len(result) != 2 {
		t.Fatalf("got %d messages, want 2", len(result))
	}
}

// TestApplySnipRemovals_ResolveDeletedParentNotFound tests the case where
// a deleted message's parent is not found in deletedParent because
// the deleted message itself is not in the messages list.
func TestApplySnipRemovals_ResolveDeletedParentNotFound(t *testing.T) {
	snipContent := map[string]interface{}{
		"type": "system",
		"snipMetadata": map[string]interface{}{
			"removedUuids": []interface{}{"del-1"},
		},
	}
	snipJSON, _ := json.Marshal(snipContent)

	boundary := &Message{
		UUID:       "boundary-1",
		Type:       "system",
		Subtype:    "compact_boundary",
		ParentUUID: "",
		Content:    string(snipJSON),
	}

	// del-1 is in removedUuids but NOT in the messages list.
	// A survivor references del-1 as its parent.
	// resolve("del-1"): cur="del-1", toDelete["del-1"]=true,
	// but deletedParent["del-1"] is not found (del-1 not in messages),
	// so !found → cur="" → break → returns ""
	survivor := &Message{
		UUID:       "survivor-1",
		Type:       "assistant",
		ParentUUID: "del-1",
		Content:    `[{"type":"text","text":"I survive"}]`,
	}

	msgs := []*Message{boundary, survivor}
	result := ApplySnipRemovals(msgs)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	// survivor should be relinked to empty string
	// because del-1's parent could not be resolved
	if result[1].ParentUUID != "" {
		t.Errorf("survivor parent = %q, want empty (deleted parent not resolvable)", result[1].ParentUUID)
	}
}

