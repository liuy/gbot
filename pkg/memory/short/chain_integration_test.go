package short

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Integration Tests: Append-Only Store + Chain-Walk
//
// These tests exercise full call chains from fork/boundary creation through
// chain-walk reload, using real SQLite stores. No mocking.
//
// Test design checklist:
//   [x] Full call chain test exists (entry → middle → side effect → output)
//   [x] Cold start scenario (empty store)
//   [x] Hot path (normal creation → usage)
//   [x] Recovery scenario (close + reopen = process restart)
//   [x] No mocking of system under test
// ---------------------------------------------------------------------------

// TestIntegration_ForkAndReload verifies the core append-only lifecycle:
// create chain → fork → add new branch → close store → reopen → chain-walk
// returns only the active branch.
//
// Call chain: AppendMessages → AppendMessagesWithForkPoint → Close → NewStore → LoadChainMessages
func TestIntegration_ForkAndReload(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"

	// --- Phase 1: Create initial chain ---
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sessionID := "fork-reload-test"
	createTestSession(t, store, sessionID)

	initialMsgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-u1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-a1", "", `[{"type":"text","text":"hi"}]`),
		testMessage(0, "user", "uuid-u2", "", `[{"type":"text","text":"how are you?"}]`),
		testMessage(0, "assistant", "uuid-a2", "", `[{"type":"text","text":"good"}]`),
	}
	if err := store.AppendMessages(sessionID, initialMsgs); err != nil {
		t.Fatalf("AppendMessages initial: %v", err)
	}

	// --- Phase 2: Simulate rewind by forking from uuid-a1 ---
	// (skipping uuid-u2 and uuid-a2, which become dead branches)
	forkMsgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-u3", "", `[{"type":"text","text":"let me try again"}]`),
		testMessage(0, "assistant", "uuid-a3", "", `[{"type":"text","text":"sure!"}]`),
	}
	if err := store.AppendMessagesWithForkPoint(sessionID, forkMsgs, "uuid-a1"); err != nil {
		t.Fatalf("AppendMessagesWithForkPoint: %v", err)
	}

	// Verify all 6 messages are in the store (append-only, dead branches kept)
	allMsgs, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(allMsgs) != 6 {
		t.Fatalf("expected 6 messages in store (append-only), got %d", len(allMsgs))
	}

	// --- Phase 3: Simulate process restart — close and reopen store ---
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	defer store2.Close()

	// --- Phase 4: Chain-walk should return only active branch ---
	chain, err := store2.LoadChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages after reload: %v", err)
	}

	// Active branch: uuid-u1 → uuid-a1 → uuid-u3 → uuid-a3 (4 messages)
	if len(chain) != 4 {
		t.Fatalf("got %d chain messages, want 4 (active branch only)", len(chain))
	}

	wantUUIDs := []string{"uuid-u1", "uuid-a1", "uuid-u3", "uuid-a3"}
	for i, msg := range chain {
		if msg.UUID != wantUUIDs[i] {
			t.Errorf("chain[%d].UUID = %q, want %q", i, msg.UUID, wantUUIDs[i])
		}
	}

	// Verify parent links in active chain
	if chain[0].ParentUUID != "" {
		t.Errorf("root parent = %q, want empty", chain[0].ParentUUID)
	}
	if chain[2].ParentUUID != "uuid-a1" {
		t.Errorf("fork point: chain[2].ParentUUID = %q, want uuid-a1", chain[2].ParentUUID)
	}
}

// TestIntegration_CompactBoundaryWithFork verifies LoadPostCompactChainMessages
// skips dead branches that exist before AND after the boundary.
//
// Call chain: AppendMessages → insert boundary → AppendMessagesWithForkPoint →
//
//	LoadPostCompactChainMessages → verify active post-boundary chain only
func TestIntegration_CompactBoundaryWithFork(t *testing.T) {
	store := openTestStore(t)
	sessionID := "compact-fork-test"
	createTestSession(t, store, sessionID)

	// --- Phase 1: Pre-boundary messages (will be "compacted away") ---
	preBoundary := []*TranscriptMessage{
		testMessage(0, "user", "pre-u1", "", `[{"type":"text","text":"old question"}]`),
		testMessage(0, "assistant", "pre-a1", "", `[{"type":"text","text":"old answer"}]`),
	}
	if err := store.AppendMessages(sessionID, preBoundary); err != nil {
		t.Fatalf("AppendMessages pre-boundary: %v", err)
	}

	// --- Phase 2: Insert compact boundary ---
	boundary := &TranscriptMessage{
		Type:      "system",
		Subtype:   "compact_boundary",
		UUID:      "boundary-1",
		Content:   `[{"type":"text","text":"compact summary"}]`,
		CreatedAt: testTimeBase,
	}
	if err := store.AppendMessage(sessionID, boundary); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}

	// --- Phase 3: Post-boundary messages ---
	postBoundary := []*TranscriptMessage{
		testMessage(0, "user", "post-u1", "", `[{"type":"text","text":"new question"}]`),
		testMessage(0, "assistant", "post-a1", "", `[{"type":"text","text":"new answer"}]`),
		testMessage(0, "user", "post-u2", "", `[{"type":"text","text":"bad followup"}]`),
		testMessage(0, "assistant", "post-a2", "", `[{"type":"text","text":"bad response"}]`),
	}
	if err := store.AppendMessages(sessionID, postBoundary); err != nil {
		t.Fatalf("AppendMessages post-boundary: %v", err)
	}

	// --- Phase 4: Fork from post-a1 (simulating rewind past post-u2/post-a2) ---
	forkMsgs := []*TranscriptMessage{
		testMessage(0, "user", "post-u3", "", `[{"type":"text","text":"good followup"}]`),
		testMessage(0, "assistant", "post-a3", "", `[{"type":"text","text":"good response"}]`),
	}
	if err := store.AppendMessagesWithForkPoint(sessionID, forkMsgs, "post-a1"); err != nil {
		t.Fatalf("AppendMessagesWithForkPoint: %v", err)
	}

	// --- Phase 5: LoadPostCompactChainMessages should return active chain only ---
	chain, err := store.LoadPostCompactChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadPostCompactChainMessages: %v", err)
	}

	// Expected active chain: boundary-1 → post-u1 → post-a1 → post-u3 → post-a3
	wantUUIDs := []string{"boundary-1", "post-u1", "post-a1", "post-u3", "post-a3"}
	if len(chain) != len(wantUUIDs) {
		t.Fatalf("got %d chain messages, want %d", len(chain), len(wantUUIDs))
	}
	for i, msg := range chain {
		if msg.UUID != wantUUIDs[i] {
			t.Errorf("chain[%d].UUID = %q, want %q", i, msg.UUID, wantUUIDs[i])
		}
	}

	// Verify dead branch messages are NOT in the result
	chainUUIDs := make(map[string]bool)
	for _, msg := range chain {
		chainUUIDs[msg.UUID] = true
	}
	for _, deadUUID := range []string{"pre-u1", "pre-a1", "post-u2", "post-a2"} {
		if chainUUIDs[deadUUID] {
			t.Errorf("dead branch message %q should not be in chain", deadUUID)
		}
	}
}

// TestIntegration_MultipleForks verifies chain-walk picks the latest branch
// after multiple rewinds (each creating a new fork).
//
// Scenario:
//   A → B → C          (original chain)
//   A → B → D          (first rewind, fork from B)
//   A → E              (second rewind, fork from A)
//
// Chain-walk should return: A → E (latest branch)
func TestIntegration_MultipleForks(t *testing.T) {
	store := openTestStore(t)
	sessionID := "multi-fork-test"
	createTestSession(t, store, sessionID)

	// --- Original chain: A → B → C ---
	originalMsgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-a", "", `[{"type":"text","text":"A"}]`),
		testMessage(0, "assistant", "uuid-b", "", `[{"type":"text","text":"B"}]`),
		testMessage(0, "user", "uuid-c", "", `[{"type":"text","text":"C"}]`),
	}
	if err := store.AppendMessages(sessionID, originalMsgs); err != nil {
		t.Fatalf("AppendMessages original: %v", err)
	}

	// --- First rewind: fork from B, add D ---
	fork1 := []*TranscriptMessage{
		testMessage(0, "user", "uuid-d", "", `[{"type":"text","text":"D"}]`),
	}
	if err := store.AppendMessagesWithForkPoint(sessionID, fork1, "uuid-b"); err != nil {
		t.Fatalf("Fork 1: %v", err)
	}

	// Verify chain picks D branch: A → B → D
	chain, err := store.LoadChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages after fork 1: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("after fork 1: got %d messages, want 3", len(chain))
	}
	wantAfterFork1 := []string{"uuid-a", "uuid-b", "uuid-d"}
	for i, msg := range chain {
		if msg.UUID != wantAfterFork1[i] {
			t.Errorf("after fork 1 chain[%d] = %q, want %q", i, msg.UUID, wantAfterFork1[i])
		}
	}

	// --- Second rewind: fork from A, add E (later timestamp so leaf wins) ---
	fork2 := []*TranscriptMessage{
		testMessage(0, "assistant", "uuid-e", "", `[{"type":"text","text":"E"}]`),
	}
	// Manually set later timestamp to ensure E is picked as leaf over D
	fork2[0].CreatedAt = testTimeBase.Add(10 * time.Second)

	if err := store.AppendMessagesWithForkPoint(sessionID, fork2, "uuid-a"); err != nil {
		t.Fatalf("Fork 2: %v", err)
	}

	// --- Chain-walk should return A → E (latest branch) ---
	chain, err = store.LoadChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages after fork 2: %v", err)
	}

	if len(chain) != 2 {
		t.Fatalf("after fork 2: got %d messages, want 2 (A → E)", len(chain))
	}
	wantAfterFork2 := []string{"uuid-a", "uuid-e"}
	for i, msg := range chain {
		if msg.UUID != wantAfterFork2[i] {
			t.Errorf("after fork 2 chain[%d] = %q, want %q", i, msg.UUID, wantAfterFork2[i])
		}
	}
}

// TestIntegration_StoreRestart verifies that closing and reopening the store
// preserves the chain correctly (process boundary simulation).
//
// Call chain: AppendMessages → Close → NewStore → LoadChainMessages
func TestIntegration_StoreRestart(t *testing.T) {
	dbPath := t.TempDir() + "/restart.db"

	// --- Write phase ---
	store1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore 1: %v", err)
	}
	sessionID := "restart-test"
	createTestSession(t, store1, sessionID)

	msgs := []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"world"}]`),
	}
	if err := store1.AppendMessages(sessionID, msgs); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	// Simulate rewind: fork from uuid-1
	forkMsgs := []*TranscriptMessage{
		testMessage(0, "assistant", "uuid-3", "", `[{"type":"text","text":"new response"}]`),
	}
	if err := store1.AppendMessagesWithForkPoint(sessionID, forkMsgs, "uuid-1"); err != nil {
		t.Fatalf("AppendMessagesWithForkPoint: %v", err)
	}

	// Total messages in store: 3 (uuid-1, uuid-2, uuid-3)
	allMsgs, err := store1.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages before close: %v", err)
	}
	if len(allMsgs) != 3 {
		t.Fatalf("expected 3 messages in store, got %d", len(allMsgs))
	}

	if err := store1.Close(); err != nil {
		t.Fatalf("Close store1: %v", err)
	}

	// --- Read phase: new store instance on same DB ---
	store2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore 2 (reopen): %v", err)
	}
	defer store2.Close()

	// LoadChainMessages should return only active branch
	chain, err := store2.LoadChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages after restart: %v", err)
	}

	// Active branch: uuid-1 → uuid-3 (uuid-2 is dead branch)
	if len(chain) != 2 {
		t.Fatalf("got %d chain messages after restart, want 2", len(chain))
	}
	if chain[0].UUID != "uuid-1" {
		t.Errorf("chain[0] = %q, want uuid-1", chain[0].UUID)
	}
	if chain[1].UUID != "uuid-3" {
		t.Errorf("chain[1] = %q, want uuid-3", chain[1].UUID)
	}

	// Verify all messages still in store (append-only)
	allMsgs2, err := store2.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages after restart: %v", err)
	}
	if len(allMsgs2) != 3 {
		t.Errorf("expected 3 total messages after restart, got %d", len(allMsgs2))
	}
}
