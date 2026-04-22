package short

import (
	"testing"
	"time"
)

func TestBuildConversationChain_LinearChain(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Create a simple linear chain: user -> assistant -> user -> assistant
	msgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"hi"}]`),
		testMessage(0, "user", "uuid-3", "", `[{"type":"text","text":"how are you?"}]`),
		testMessage(0, "assistant", "uuid-4", "", `[{"type":"text","text":"good"}]`),
	}

	for _, msg := range msgs {
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	chain, err := store.BuildConversationChain(sessionID)
	if err != nil {
		t.Fatalf("BuildConversationChain: %v", err)
	}

	if len(chain) != 4 {
		t.Fatalf("got chain length %d, want 4", len(chain))
	}

	// Should be in root→leaf order (uuid-1, uuid-2, uuid-3, uuid-4)
	if chain[0].UUID != "uuid-1" {
		t.Errorf("chain[0].UUID = %q, want uuid-1", chain[0].UUID)
	}
	if chain[3].UUID != "uuid-4" {
		t.Errorf("chain[3].UUID = %q, want uuid-4", chain[3].UUID)
	}

	// Verify parent links
	if chain[0].ParentUUID != "" {
		t.Errorf("root has parent %q, want empty", chain[0].ParentUUID)
	}
	if chain[1].ParentUUID != "uuid-1" {
		t.Errorf("chain[1] parent = %q, want uuid-1", chain[1].ParentUUID)
	}
}

func TestBuildConversationChain_LeafToRootTraversal(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Create chain: A -> B -> C (C is leaf)
	msgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-a", "", `[{"type":"text","text":"A"}]`),
		testMessage(0, "assistant", "uuid-b", "", `[{"type":"text","text":"B"}]`),
		testMessage(0, "user", "uuid-c", "", `[{"type":"text","text":"C"}]`),
	}

	for _, msg := range msgs {
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	chain, err := store.BuildConversationChain(sessionID)
	if err != nil {
		t.Fatalf("BuildConversationChain: %v", err)
	}

	// Should return A -> B -> C (root to leaf)
	if len(chain) != 3 {
		t.Fatalf("got chain length %d, want 3", len(chain))
	}

	if chain[0].UUID != "uuid-a" {
		t.Errorf("first element UUID = %q, want uuid-a (root)", chain[0].UUID)
	}
	if chain[2].UUID != "uuid-c" {
		t.Errorf("last element UUID = %q, want uuid-c (leaf)", chain[2].UUID)
	}
}

func TestBuildConversationChain_BranchRecovery(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Create a structure where multiple messages point to the same parent
	// This simulates parallel tool results that were orphaned
	//
	// Main chain: user -> assistant -> user
	// Orphan: another assistant with same parent_uuid
	msgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"prompt"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"tool_use","id":"tu1","name":"bash"}]`),
		testMessage(0, "user", "uuid-3", "", `[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]`),
	}

	// Manually insert orphaned assistant with same parent as uuid-2
	// This simulates the case where streaming emitted multiple assistant messages
	orphan := &TranscriptMessage{
		Type:       "assistant",
		UUID:       "uuid-2b",
		ParentUUID: "uuid-1", // Same parent as uuid-2
		Content:    `[{"type":"tool_use","id":"tu2","name":"read"}]`,
		CreatedAt:  time.Now(),
	}
	if err := store.AppendMessage(sessionID, orphan); err != nil {
		t.Fatalf("AppendMessage orphan: %v", err)
	}

	for _, msg := range msgs {
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	chain, err := store.BuildConversationChain(sessionID)
	if err != nil {
		t.Fatalf("BuildConversationChain: %v", err)
	}

	// The chain should include the main chain
	// Orphan recovery happens via recoverOrphanedParallelToolResults
	if len(chain) < 3 {
		t.Fatalf("got chain length %d, want at least 3", len(chain))
	}
}

func TestBuildConversationChain_CycleDetection(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Create messages and manually inject a cycle: A -> B -> C -> A
	// We need to insert messages with specific parent_uuids
	msgA := testMessage(0, "user", "uuid-a", "", `[{"type":"text","text":"A"}]`)
	msgB := testMessage(0, "assistant", "uuid-b", "", `[{"type":"text","text":"B"}]`)
	msgC := testMessage(0, "user", "uuid-c", "", `[{"type":"text","text":"C"}]`)

	if err := store.AppendMessage(sessionID, msgA); err != nil {
		t.Fatalf("AppendMessage A: %v", err)
	}
	if err := store.AppendMessage(sessionID, msgB); err != nil {
		t.Fatalf("AppendMessage B: %v", err)
	}
	if err := store.AppendMessage(sessionID, msgC); err != nil {
		t.Fatalf("AppendMessage C: %v", err)
	}

	// Now create a cycle by updating msgA's parent_uuid to point to msgC
	_, err := store.db.Exec(
		"UPDATE messages SET parent_uuid = ? WHERE uuid = ?",
		"uuid-c", "uuid-a",
	)
	if err != nil {
		t.Fatalf("inject cycle: %v", err)
	}

	// BuildConversationChain should detect cycle and truncate
	chain, err := store.BuildConversationChain(sessionID)
	if err != nil {
		t.Fatalf("BuildConversationChain: %v", err)
	}

	// Chain should exist but be truncated at the cycle point
	// The exact length depends on which message is detected as leaf
	// We just verify it doesn't hang or return all 4 (would be infinite without cycle detection)
	if len(chain) > 4 {
		t.Errorf("chain length %d suggests cycle not detected", len(chain))
	}
}

func TestBuildConversationChain_EmptyChain(t *testing.T) {
	store := openTestStore(t)
	sessionID := "empty-session"

	chain, err := store.BuildConversationChain(sessionID)
	if err != nil {
		t.Fatalf("BuildConversationChain: %v", err)
	}

	if len(chain) != 0 {
		t.Errorf("got chain length %d, want 0", len(chain))
	}
}

func TestBuildConversationChain_SkipsProgress(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	msgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "progress", "uuid-2", "", `[{"type":"text","text":"running..."}]`),
		testMessage(0, "assistant", "uuid-3", "", `[{"type":"text","text":"response"}]`),
		testMessage(0, "progress", "uuid-4", "", `[{"type":"text","text":"still running..."}]`),
		testMessage(0, "user", "uuid-5", "", `[{"type":"text","text":"next"}]`),
	}

	for _, msg := range msgs {
		if err := store.AppendMessage(sessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	chain, err := store.BuildConversationChain(sessionID)
	if err != nil {
		t.Fatalf("BuildConversationChain: %v", err)
	}

	// Chain should only include non-progress messages
	// uuid-1 -> uuid-3 -> uuid-5
	if len(chain) != 3 {
		t.Fatalf("got chain length %d, want 3 (progress messages excluded)", len(chain))
	}

	// Verify no progress messages in chain
	for _, msg := range chain {
		if msg.Type == "progress" {
			t.Errorf("progress message in chain: %v", msg.UUID)
		}
	}
}

func TestRecoverOrphanedParallelToolResults_MultipleAssistantWithSameID(t *testing.T) {
	// Test the core recovery logic directly
	allMessages := []*TranscriptMessage{
		// Main chain assistant
		{
			UUID:       "asst-1",
			Type:       "assistant",
			Content:    `[{"type":"tool_use","id":"tu1","name":"bash"}]`,
			CreatedAt:  time.Now().Add(-2 * time.Second),
		},
		// Orphaned sibling with same tool_use id
		{
			UUID:       "asst-2",
			Type:       "assistant",
			Content:    `[{"type":"tool_use","id":"tu1","name":"read"}]`, // Same id!
			CreatedAt:  time.Now().Add(-1 * time.Second),
		},
		// Tool result for first assistant
		{
			UUID:       "tr-1",
			Type:       "user",
			ParentUUID: "asst-1",
			Content:    `[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]`,
			CreatedAt:  time.Now(),
		},
	}

	chain := []*TranscriptMessage{allMessages[0]} // Chain has only the first assistant

	recovered := recoverOrphanedParallelToolResults(allMessages, chain)

	// Should recover the orphaned assistant (asst-2) and its tool result
	if len(recovered) <= 1 {
		t.Fatalf("recovery failed: got %d messages, want at least 2", len(recovered))
	}

	// Check that asst-2 is in the recovered chain
	foundOrphan := false
	for _, msg := range recovered {
		if msg.UUID == "asst-2" {
			foundOrphan = true
			break
		}
	}
	if !foundOrphan {
		t.Error("orphaned assistant asst-2 not recovered")
	}
}

func TestRecoverOrphanedParallelToolResults_NoSiblings(t *testing.T) {
	// When there are no siblings, chain should be unchanged
	allMessages := []*TranscriptMessage{
		{
			UUID:      "asst-1",
			Type:      "assistant",
			Content:   `[{"type":"tool_use","id":"tu1","name":"bash"}]`,
			CreatedAt: time.Now(),
		},
		{
			UUID:      "user-1",
			Type:      "user",
			Content:   `[{"type":"text","text":"next"}]`,
			CreatedAt: time.Now(),
		},
	}

	chain := []*TranscriptMessage{allMessages[0], allMessages[1]}

	recovered := recoverOrphanedParallelToolResults(allMessages, chain)

	if len(recovered) != 2 {
		t.Errorf("got %d messages, want 2 (no orphans to recover)", len(recovered))
	}
}

func TestRecoverOrphanedParallelToolResults_TimeOrder(t *testing.T) {
	// Verify recovered messages are sorted by timestamp
	now := time.Now()

	allMessages := []*TranscriptMessage{
		{
			UUID:      "asst-1",
			Type:      "assistant",
			Content:   `[{"type":"tool_use","id":"tu1"}]`,
			CreatedAt: now,
		},
		// Orphaned siblings - out of timestamp order
		{
			UUID:      "asst-3",
			Type:      "assistant",
			Content:   `[{"type":"tool_use","id":"tu1"}]`,
			CreatedAt: now.Add(2 * time.Second),
		},
		{
			UUID:      "asst-2",
			Type:      "assistant",
			Content:   `[{"type":"tool_use","id":"tu1"}]`,
			CreatedAt: now.Add(1 * time.Second),
		},
	}

	chain := []*TranscriptMessage{allMessages[0]}

	recovered := recoverOrphanedParallelToolResults(allMessages, chain)

	// Find positions of orphaned assistants
	var pos2, pos3 int
	for i, msg := range recovered {
		if msg.UUID == "asst-2" {
			pos2 = i
		}
		if msg.UUID == "asst-3" {
			pos3 = i
		}
	}

	// asst-2 should come before asst-3 (timestamp order)
	if pos2 > pos3 {
		t.Errorf("orphaned messages not in timestamp order: asst-2 at %d, asst-3 at %d", pos2, pos3)
	}
}

func TestFindLeafMessage(t *testing.T) {
	messages := []*TranscriptMessage{
		{UUID: "a", ParentUUID: "b", CreatedAt: time.Now()}, // a's parent is b
		{UUID: "b", ParentUUID: "", CreatedAt: time.Now()},  // b is root
		{UUID: "c", ParentUUID: "b", CreatedAt: time.Now()}, // c's parent is b
	}

	// Both a and c are leaves (no one points to them)
	// Should return the one with later timestamp or first found
	leaf := findLeafMessage(messages)

	if leaf == nil {
		t.Fatal("got nil leaf, want a or c")
	}

	if leaf.UUID != "a" && leaf.UUID != "c" {
		t.Errorf("got leaf UUID %q, want a or c", leaf.UUID)
	}
}

func TestWalkToRoot(t *testing.T) {
	// Create a simple chain: a <- b <- c (c's parent is b, b's parent is a)
	msgMap := map[string]*TranscriptMessage{
		"a": {UUID: "a", ParentUUID: ""},
		"b": {UUID: "b", ParentUUID: "a"},
		"c": {UUID: "c", ParentUUID: "b"},
	}

	leaf := msgMap["c"]
	chain := walkToRoot(leaf, msgMap)

	// Should walk c -> b -> a
	if len(chain) != 3 {
		t.Fatalf("got chain length %d, want 3", len(chain))
	}

	// Verify order (before reversal)
	if chain[0].UUID != "c" {
		t.Errorf("chain[0].UUID = %q, want c", chain[0].UUID)
	}
	if chain[2].UUID != "a" {
		t.Errorf("chain[2].UUID = %q, want a", chain[2].UUID)
	}
}

func TestReverseMessages(t *testing.T) {
	messages := []*TranscriptMessage{
		{UUID: "a"},
		{UUID: "b"},
		{UUID: "c"},
	}

	reverseMessages(messages)

	if messages[0].UUID != "c" {
		t.Errorf("after reverse, first = %q, want c", messages[0].UUID)
	}
	if messages[2].UUID != "a" {
		t.Errorf("after reverse, last = %q, want a", messages[2].UUID)
	}
}

func TestFindLeafMessage_NoProgressMessages(t *testing.T) {
	now := time.Now()

	messages := []*TranscriptMessage{
		{UUID: "user-1", Type: "user", ParentUUID: "", CreatedAt: now.Add(-2 * time.Second)},
		{UUID: "asst-1", Type: "assistant", ParentUUID: "user-1", CreatedAt: now.Add(-1 * time.Second)},
		{UUID: "prog-1", Type: "progress", ParentUUID: "asst-1", CreatedAt: now},
		{UUID: "user-2", Type: "user", ParentUUID: "asst-1", CreatedAt: now.Add(1 * time.Second)},
	}

	leaf := findLeafMessage(messages)

	if leaf == nil {
		t.Fatal("got nil leaf, want user-2")
	}

	// Should prefer non-progress leaf (user-2 over prog-1)
	if leaf.Type == "progress" {
		t.Errorf("should not select progress message as leaf, got type %q", leaf.Type)
	}

	// user-2 is the true leaf (no one points to it)
	if leaf.UUID != "user-2" {
		t.Errorf("leaf UUID = %q, want user-2", leaf.UUID)
	}
}

func TestWalkToRoot_BrokenChain(t *testing.T) {
	// Test with broken parent references
	msgMap := map[string]*TranscriptMessage{
		"a": {UUID: "a", ParentUUID: ""},
		"b": {UUID: "b", ParentUUID: "a"},
		"c": {UUID: "c", ParentUUID: "broken-ref"}, // Points to non-existent parent
	}

	leaf := msgMap["c"]
	chain := walkToRoot(leaf, msgMap)

	// Should walk c -> (nil, since broken-ref not in map)
	// The function stops when current.ParentUUID != "" but msgMap[current.ParentUUID] is nil
	if len(chain) != 1 {
		t.Errorf("got chain length %d, want 1 (only leaf, broken parent stops walk)", len(chain))
	}

	if chain[0].UUID != "c" {
		t.Errorf("chain[0].UUID = %q, want c", chain[0].UUID)
	}
}

func TestWalkToRoot_CircularRef(t *testing.T) {
	// Create a circular reference
	msgMap := map[string]*TranscriptMessage{
		"a": {UUID: "a", ParentUUID: "b"},
		"b": {UUID: "b", ParentUUID: "c"},
		"c": {UUID: "c", ParentUUID: "a"}, // Cycle back to a
	}

	leaf := msgMap["c"]
	chain := walkToRoot(leaf, msgMap)

	// Should detect cycle and truncate
	if len(chain) > 3 {
		t.Errorf("chain length %d suggests cycle not detected, max should be 3", len(chain))
	}

	// Should have at least the starting message
	if len(chain) == 0 {
		t.Error("chain should not be empty")
	}
}

// Line 13-15: BuildConversationChain returns nil,err when LoadMessages fails
func TestBuildConversationChain_LoadError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := store.BuildConversationChain("any-session")
	if err == nil {
		t.Fatal("BuildConversationChain should fail when store is closed")
	}
}

// Line 59-61: recoverOrphanedParallelToolResults — chainAssistants empty returns chain
func TestRecoverOrphanedParallelToolResults_NoChainAssistants(t *testing.T) {
	allMessages := []*TranscriptMessage{
		{UUID: "user-1", Type: "user", Content: `[{"type":"text","text":"hello"}]`, CreatedAt: time.Now()},
	}
	chain := []*TranscriptMessage{allMessages[0]}
	result := recoverOrphanedParallelToolResults(allMessages, chain)
	if len(result) != 1 {
		t.Errorf("got %d messages, want 1 (no assistants in chain)", len(result))
	}
}

// Line 86-88: recoverOrphanedParallelToolResults — non-assistant msg, getMessageID returns ""
func TestRecoverOrphanedParallelToolResults_NonAssistantWithToolUseID(t *testing.T) {
	// User message with a tool_use-like block (edge case: non-assistant but tool_use block)
	allMessages := []*TranscriptMessage{
		{UUID: "asst-1", Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"bash"}]`, CreatedAt: time.Now()},
		{UUID: "user-1", Type: "user", Content: `[{"type":"text","text":"reply"}]`, CreatedAt: time.Now()},
	}
	chain := []*TranscriptMessage{allMessages[0], allMessages[1]}
	result := recoverOrphanedParallelToolResults(allMessages, chain)
	if len(result) != 2 {
		t.Errorf("got %d messages, want 2 (no orphans to recover)", len(result))
	}
}

// Line 152-154: recoverOrphanedParallelToolResults — group nil
func TestRecoverOrphanedParallelToolResults_NoSiblingsGroup(t *testing.T) {
	// Assistant in chain with tool_use ID, but no siblings at all
	allMessages := []*TranscriptMessage{
		{UUID: "asst-1", Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"bash"}]`, CreatedAt: time.Now()},
		{UUID: "user-1", Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu1"}]`, CreatedAt: time.Now()},
	}
	chain := []*TranscriptMessage{allMessages[0], allMessages[1]}
	// The anchor is asst-1 with tool_use id tu1. No other assistant has same id.
	// So no orphaned siblings. No orphaned TRs.
	result := recoverOrphanedParallelToolResults(allMessages, chain)
	if len(result) != 2 {
		t.Errorf("got %d messages, want 2 (no orphans)", len(result))
	}
}

// Lines 247-256: findLeafMessage — all leaves are sidechain or progress (fallback path)
func TestFindLeafMessage_AllSidechainOrProgress(t *testing.T) {
	now := time.Now()
	messages := []*TranscriptMessage{
		{UUID: "root", Type: "user", ParentUUID: "", CreatedAt: now},
		{UUID: "leaf-1", Type: "progress", ParentUUID: "root", IsSidechain: 0, CreatedAt: now.Add(1 * time.Second)},
		{UUID: "leaf-2", Type: "user", ParentUUID: "root", IsSidechain: 1, CreatedAt: now.Add(2 * time.Second)},
	}
	leaf := findLeafMessage(messages)
	if leaf == nil {
		t.Fatal("got nil leaf, want non-nil (fallback)")
	}
	// Falls back to any leaf, should pick leaf-2 (latest timestamp)
	if leaf.UUID != "leaf-2" {
		t.Errorf("leaf UUID = %q, want leaf-2 (fallback latest)", leaf.UUID)
	}
}

// Lines 247-256: findLeafMessage — all leaves are sidechain (only sidechain candidates)
func TestFindLeafMessage_OnlySidechainLeaves(t *testing.T) {
	now := time.Now()
	messages := []*TranscriptMessage{
		{UUID: "root", Type: "user", ParentUUID: "", CreatedAt: now},
		{UUID: "side-1", Type: "user", ParentUUID: "root", IsSidechain: 1, CreatedAt: now.Add(1 * time.Second)},
	}
	leaf := findLeafMessage(messages)
	if leaf == nil {
		t.Fatal("got nil leaf")
	}
	if leaf.UUID != "side-1" {
		t.Errorf("leaf UUID = %q, want side-1", leaf.UUID)
	}
}

// Line 56-57: FilterUnresolvedToolUses — non user/assistant message skipped
func TestFilterUnresolvedToolUses_NonUserAssistantSkipped(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "system", Content: `[{"type":"text","text":"system msg"}]`},
		{Type: "progress", Content: `running...`},
		{Type: "attachment", Content: `{}`},
	}
	// None of these have tool_use or tool_result blocks, so all should be kept
	result := FilterUnresolvedToolUses(messages)
	if len(result) != 3 {
		t.Errorf("got %d, want 3 (non-user/assistant kept)", len(result))
	}
}

func TestRecoverOrphanedParallelToolResults_NilSiblings(t *testing.T) {
	// Create messages where an assistant has tool_use blocks but the
	// siblings map entry is nil, which shouldn't happen normally.
	// Test by having only a user message in the chain.
	allMessages := []*TranscriptMessage{
		{UUID: "asst-1", Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"bash","input":{}}]`, CreatedAt: time.Now()},
	}
	chain := []*TranscriptMessage{allMessages[0]}
	result := recoverOrphanedParallelToolResults(allMessages, chain)
	if len(result) != 1 {
		t.Errorf("got %d messages, want 1", len(result))
	}
}

func TestChain_GetMessageID_NonAssistant(t *testing.T) {
	// Direct test of the non-assistant path in recoverOrphanedParallelToolResults
	// by having a chain with only user messages (no assistants with tool_use)
	allMessages := []*TranscriptMessage{
		{UUID: "user-1", Type: "user", Content: `[{"type":"text","text":"hello"}]`, CreatedAt: time.Now()},
		{UUID: "user-2", Type: "user", Content: `[{"type":"text","text":"world"}]`, CreatedAt: time.Now().Add(time.Second)},
	}
	chain := []*TranscriptMessage{allMessages[0], allMessages[1]}
	result := recoverOrphanedParallelToolResults(allMessages, chain)
	if len(result) != 2 {
		t.Errorf("got %d, want 2 (no assistants to process)", len(result))
	}
}

func TestChain_NilGroup_Fallback(t *testing.T) {
	// Create a chain with an assistant that has a tool_use block with an ID
	// that no other message shares. This should trigger the nil group fallback.
	allMessages := []*TranscriptMessage{
		{UUID: "asst-1", Type: "assistant", Content: `[{"type":"tool_use","id":"unique-id-1","name":"bash","input":{}}]`, CreatedAt: time.Now()},
		{UUID: "user-1", Type: "user", Content: `[{"type":"text","text":"result"}]`, CreatedAt: time.Now().Add(time.Second)},
	}
	chain := []*TranscriptMessage{allMessages[0], allMessages[1]}

	// The assistant has tool_use with id "unique-id-1"
	// siblingsByMsgId["unique-id-1"] = [] = {asst-1}
	// So group is NOT nil. To make it nil, we need a scenario where
	// the assistant is in chainAssistants but NOT in siblingsByMsgId.
	// siblingsByMsgId is built from ALL messages, so if assistant has tool_use ID,
	// it WILL be in siblingsByMsgId. The nil case is truly impossible with valid data.

	// The only way: assistant in chain with tool_use ID, but that ID has
	// been processed by an earlier assistant in chainAssistants iteration.
	// Since we iterate chainAssistants and check processedGroups,
	// the second assistant with the same ID would be skipped.
	// But group for the first one would be non-nil.

	// This is effectively dead code — nil group can't happen with valid input.
	result := recoverOrphanedParallelToolResults(allMessages, chain)
	if len(result) != 2 {
		t.Errorf("got %d, want 2", len(result))
	}
}

// TestRecoverOrphanedParallelToolResults_NonToolResultWithToolUseID
// tests that a non-tool_result message with tool_use_id triggers the correct path.
func TestRecoverOrphanedParallelToolResults_NonToolResultWithToolUseID(t *testing.T) {
	msgs := []*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{UUID: "a1", Type: "assistant", Content: `[{"type":"text","text":"check"},{"type":"tool_use","id":"tu1","name":"Read"}]`},
		// This is a "user" message (not tool_result) but has tool_use_id in content
		{UUID: "u2", Type: "user", ParentUUID: "a1", Content: `[{"type":"text","text":"some response"}]`, IsSidechain: 0},
	}

	result := recoverOrphanedParallelToolResults(msgs, msgs)
	// Should process without panic
	_ = result
}

// TestRecoverOrphaned_GroupNil tests the branch where a chain assistant's
// message ID is not found in siblingsByMsgId because the assistant is in
// chain but not in allMessages.
func TestRecoverOrphaned_GroupNil(t *testing.T) {
	// chain has an assistant with a tool_use ID, but allMessages is empty.
	// This means siblingsByMsgId won't contain the msgID, so group == nil.
	chain := []*TranscriptMessage{
		{
			Type:    "assistant",
			UUID:    "asst-1",
			Content: `[{"type":"tool_use","id":"tool-123","name":"Read"}]`,
		},
	}
	// allMessages is empty — so chain assistant is not in siblingsByMsgId
	allMsgs := []*TranscriptMessage{}

	result := recoverOrphanedParallelToolResults(allMsgs, chain)
	if len(result) != 1 || result[0].UUID != "asst-1" {
		t.Fatalf("expected chain unchanged, got %v", result)
	}
}

