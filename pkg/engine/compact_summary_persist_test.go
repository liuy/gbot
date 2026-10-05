package engine

// compact_summary_persist_test.go pins 存=发 for the compact summary: the
// summary user message that the live engine sends to the API must also be in
// the RecordCompact persistence set, so a restart reloads it byte-identically
// (provider exact-prefix cache hit + retained pre-compact memory).
//
// TS ground truth: services/compact/compact.ts:614-622 — the summary is a
// UserMessage (isCompactSummary, isVisibleInTranscriptOnly). The flag is a
// UI-render concept: the chat UI hides it, the transcript keeps it, and the
// resume chain (loadTranscriptFile → buildConversationChain) has no summary
// filter. TS persists ✓ loads ✓ sends ✓.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// summaryMarker is the stable first-line fragment of GetCompactUserSummaryMessage.
const summaryMarker = "This session is being continued from a previous conversation"

// marshalSummaryMessage finds the single summary message in an API-marshaled
// list and returns its wire bytes. Wire text carries the injectTimestamp
// prefix, so match by substring. Fails the test when the summary is absent or
// duplicated — both break the byte-pin.
func marshalSummaryMessage(t *testing.T, apiMsgs []types.Message, label string) []byte {
	t.Helper()
	var found []byte
	count := 0
	for _, msg := range apiMsgs {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeText && strings.Contains(block.Text, summaryMarker) {
				count++
				raw, err := json.Marshal(msg)
				if err != nil {
					t.Fatalf("marshal %s summary message: %v", label, err)
				}
				found = raw
			}
		}
	}
	if count != 1 {
		t.Fatalf("%s: summary message count = %d, want 1 (api messages: %d)", label, count, len(apiMsgs))
	}
	return found
}

// findSummaryEngineMessage locates the summary message in raw engine history
// (no timestamp injection at this layer, so match the raw prefix).
func findSummaryEngineMessage(t *testing.T, msgs []types.Message, label string) types.Message {
	t.Helper()
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeText && strings.HasPrefix(block.Text, summaryMarker) {
				return msg
			}
		}
	}
	t.Fatalf("%s: no summary message in engine history (%d messages)", label, len(msgs))
	return types.Message{}
}

// TestCompactSummaryPersist_RestartRoundTrip runs a real partial compact on a
// live engine, captures the live request's summary wire bytes, then reloads
// the session from the store into a fresh engine and asserts the reloaded
// request is byte-identical.
func TestCompactSummaryPersist_RestartRoundTrip(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	compactor := NewAutoCompactor(store, &testEngineMeta{
		model: "test-model", sessionID: session.SessionID, contextWindow: 1000, provider: p,
	})
	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		Logger:    slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(session.SessionID)

	// 10 × ~2000-char messages: partial compact (head summarized, tail kept),
	// same arithmetic as TestAutoCompact_PostTurn_E2E.
	eng.SetMessages(makeMessages(10, 2000))

	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}

	// The store must contain the summary row. This COUNT was 0 in the
	// incident diagnosis — the persistence set skipped the summary entirely.
	var rowCount int
	err = store.DB().QueryRow(
		`SELECT COUNT(*) FROM messages WHERE session_id = ? AND content LIKE ?`,
		session.SessionID, "%"+summaryMarker+"%",
	).Scan(&rowCount)
	if err != nil {
		t.Fatalf("count summary rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("summary rows in store = %d, want 1", rowCount)
	}

	// Live request's summary wire bytes (full production marshal chain).
	liveBytes := marshalSummaryMessage(t, eng.prepareAPIMessages(context.Background()), "live")

	// Restart: a fresh engine loads the session from the store.
	eng2 := New(&Params{
		Provider: p,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetStore(store, tmpDir)
	if _, err := eng2.SwitchSession(session.SessionID); err != nil {
		t.Fatalf("SwitchSession after restart: %v", err)
	}

	reloadedBytes := marshalSummaryMessage(t, eng2.prepareAPIMessages(context.Background()), "reloaded")
	if !bytes.Equal(liveBytes, reloadedBytes) {
		t.Errorf("summary wire bytes differ between live and reloaded:\nlive:     %s\nreloaded: %s",
			liveBytes, reloadedBytes)
	}

	// The reloaded summary must be the same engine message, not just the same
	// wire bytes: user role + FlagCompactSummary (drives stale-marker
	// filtering on the next compact) survive the DB round-trip.
	summaryMsg := findSummaryEngineMessage(t, eng2.Messages(), "reloaded")
	if summaryMsg.Role != types.RoleUser {
		t.Errorf("reloaded summary role = %q, want %q", summaryMsg.Role, types.RoleUser)
	}
	if !summaryMsg.HasFlag(types.FlagCompactSummary) {
		t.Error("reloaded summary lost FlagCompactSummary (metadata column not persisted)")
	}

	// Pairing repair must be a no-op on the reloaded history: the summary is
	// text-only user content, so nothing may be added or stripped.
	reloaded := eng2.Messages()
	paired := EnsureToolResultPairing(reloaded)
	if len(paired) != len(reloaded) {
		t.Fatalf("EnsureToolResultPairing changed reloaded history length: %d → %d", len(reloaded), len(paired))
	}
	for i := range reloaded {
		before, bErr := json.Marshal(reloaded[i])
		after, aErr := json.Marshal(paired[i])
		if bErr != nil || aErr != nil {
			t.Fatalf("marshal paired message %d: before=%v after=%v", i, bErr, aErr)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("EnsureToolResultPairing mutated reloaded message %d:\nbefore: %s\nafter:  %s", i, before, after)
		}
	}

	// The reloaded boundary must stay a system row filtered from the API
	// (boundary handling is unchanged by the summary fix).
	for i, msg := range eng2.prepareAPIMessages(context.Background()) {
		if msg.Role == types.RoleSystem {
			t.Errorf("reloaded API message %d is system role (boundary must be filtered)", i)
		}
	}

	// Context shape sanity: boundary + summary + kept tail must all reload.
	if got := len(eng2.Messages()); got < 3 {
		t.Errorf("reloaded history = %d messages, want >= 3 (boundary + summary + kept)", got)
	}
}

// TestCompactSummaryPersist_StoreRowShape verifies the persisted summary row
// form: user role, chained to the boundary, carrying the flag metadata, and
// the preserved-segment anchor pointing at the summary (what RecordCompact
// actually chains before keep[0] — TS compact.ts:1078-1080 anchors 'up_to'
// compacts at summaryMessages.at(-1), not at the boundary).
func TestCompactSummaryPersist_StoreRowShape(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	compactor := NewAutoCompactor(store, &testEngineMeta{
		model: "test-model", sessionID: session.SessionID, contextWindow: 1000, provider: p,
	})
	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		Logger:    slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(session.SessionID)
	eng.SetMessages(makeMessages(10, 2000))

	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}

	msgs, err := store.LoadChainMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages: %v", err)
	}
	if len(msgs) < 3 {
		t.Fatalf("chain = %d messages, want >= 3 (boundary + summary + kept)", len(msgs))
	}
	if msgs[0].Type != "system" || msgs[0].Subtype != "compact_boundary" {
		t.Errorf("chain[0] = %s/%s, want system/compact_boundary", msgs[0].Type, msgs[0].Subtype)
	}
	if msgs[1].Type != "user" {
		t.Errorf("chain[1] type = %q, want user (summary)", msgs[1].Type)
	}
	if msgs[1].ParentUUID != msgs[0].UUID {
		t.Errorf("summary parent = %q, want boundary uuid %q", msgs[1].ParentUUID, msgs[0].UUID)
	}
	if msgs[2].ParentUUID != msgs[1].UUID {
		t.Errorf("kept[0] parent = %q, want summary uuid %q (summary must stay between boundary and kept)", msgs[2].ParentUUID, msgs[1].UUID)
	}
	blocks := short.ParseContentBlocks(msgs[1].Content)
	if len(blocks) != 1 || !strings.HasPrefix(blocks[0].Text, summaryMarker) {
		t.Errorf("summary content = %q, want single text block starting with %q", msgs[1].Content, summaryMarker)
	}

	// Anchor read straight from the persisted boundary content JSON.
	boundaryBlocks := short.ParseContentBlocks(msgs[0].Content)
	if len(boundaryBlocks) != 1 {
		t.Fatalf("boundary content = %q, want one text block", msgs[0].Content)
	}
	var boundaryInner struct {
		CompactMetadata struct {
			PreservedSegment *struct {
				HeadUUID   string `json:"headUuid"`
				AnchorUUID string `json:"anchorUuid"`
				TailUUID   string `json:"tailUuid"`
			} `json:"preservedSegment"`
		} `json:"compactMetadata"`
	}
	if err := json.Unmarshal([]byte(boundaryBlocks[0].Text), &boundaryInner); err != nil {
		t.Fatalf("parse boundary content JSON: %v", err)
	}
	seg := boundaryInner.CompactMetadata.PreservedSegment
	if seg == nil {
		t.Fatal("partial compact boundary has no preservedSegment")
	}
	if seg.AnchorUUID != msgs[1].UUID {
		t.Errorf("preservedSegment.anchor = %q, want summary uuid %q (else the on-load relink splices the summary off the chain)", seg.AnchorUUID, msgs[1].UUID)
	}
	if seg.HeadUUID != msgs[2].UUID {
		t.Errorf("preservedSegment.head = %q, want kept[0] uuid %q", seg.HeadUUID, msgs[2].UUID)
	}
}
