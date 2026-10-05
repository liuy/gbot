package short

// compact_summary_test.go covers the compact summary persistence pieces:
// CreateCompactSummaryMessage (persisted form), reanchorPreservedSegment
// (chain-anchor maintenance), and RecordCompact's summary row + metadata
// column round-trip.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestCreateCompactSummaryMessage_Fields(t *testing.T) {
	msg := CreateCompactSummaryMessage("summary body")

	if msg.Type != "user" {
		t.Errorf("Type = %q, want user", msg.Type)
	}
	if msg.UUID == "" {
		t.Error("UUID is empty — RecordCompact needs it to chain and re-anchor")
	}
	if msg.ParentUUID != "" {
		t.Errorf("ParentUUID = %q, want empty (RecordCompact chains it after the boundary)", msg.ParentUUID)
	}
	if msg.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero — the timestamp round-trips into the reloaded wire bytes")
	}

	blocks := ParseContentBlocks(msg.Content)
	if len(blocks) != 1 {
		t.Fatalf("content blocks = %d, want 1: %s", len(blocks), msg.Content)
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "summary body" {
		t.Errorf("block = %+v, want single text block with the summary body", blocks[0])
	}

	// The metadata column must carry FlagCompactSummary so the reload restores
	// it (drives the next compact's stale-marker filter).
	engineMsg := StoreMessageToEngine(msg)
	if !engineMsg.HasFlag(types.FlagCompactSummary) {
		t.Errorf("flag lost on engine round-trip, metadata = %q", msg.Metadata)
	}
	if engineMsg.Role != types.RoleUser {
		t.Errorf("engine role = %q, want user", engineMsg.Role)
	}
	if engineMsg.ID != msg.UUID {
		t.Errorf("engine ID = %q, want store uuid %q", engineMsg.ID, msg.UUID)
	}
}

func TestReanchorPreservedSegment_RewritesAnchor(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "head-1", boundary.UUID, "tail-1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	reanchorPreservedSegment(boundary, "summary-1")

	meta, err := extractCompactMetadata(boundary)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}
	seg := meta.PreservedSegment
	if seg == nil {
		t.Fatal("preservedSegment lost by re-anchor")
	}
	if seg.AnchorUUID != "summary-1" {
		t.Errorf("AnchorUUID = %q, want summary-1", seg.AnchorUUID)
	}
	if seg.HeadUUID != "head-1" || seg.TailUUID != "tail-1" {
		t.Errorf("re-anchor must keep head/tail, got head=%q tail=%q", seg.HeadUUID, seg.TailUUID)
	}
}

func TestReanchorPreservedSegment_NoSegment(t *testing.T) {
	// Whole-history compact: boundary carries no preserved segment — the
	// re-anchor must be a no-op, not add one.
	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	before := boundary.Content

	reanchorPreservedSegment(boundary, "summary-1")

	if boundary.Content != before {
		t.Errorf("content changed without a preserved segment:\nbefore: %s\nafter:  %s", before, boundary.Content)
	}
}

func TestReanchorPreservedSegment_UnparseableBoundary(t *testing.T) {
	boundary := &TranscriptMessage{UUID: "b-1", Type: "system", Subtype: "compact_boundary", Content: ""}

	reanchorPreservedSegment(boundary, "summary-1")

	if boundary.Content != "" {
		t.Errorf("content rewritten from unparseable input: %q", boundary.Content)
	}
}

// TestRecordCompact_SummaryRowAndAnchor runs the store-level compact write
// with a summary in the persistence set and verifies the durable effects:
// summary row chained between boundary and kept tail, preserved-segment
// anchor pointing at the summary, and the flag metadata surviving the write.
func TestRecordCompact_SummaryRowAndAnchor(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	msgs := []*TranscriptMessage{
		testMessage(0, "user", "u-1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "a-1", "u-1", `[{"type":"text","text":"hi"}]`),
		testMessage(0, "user", "u-2", "a-1", `[{"type":"text","text":"world"}]`),
	}
	result, err := store.PartialCompact(sessionID, msgs, 1, "auto")
	if err != nil {
		t.Fatalf("PartialCompact: %v", err)
	}
	result.SummaryMessages = []*TranscriptMessage{CreateCompactSummaryMessage("summarized head")}

	if err := store.RecordCompact(sessionID, result); err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	chain, err := store.LoadChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages: %v", err)
	}
	if len(chain) != 4 {
		t.Fatalf("chain length = %d, want 4 (boundary + summary + 2 kept): %+v", len(chain), chainUUIDs(chain))
	}
	if chain[0].Type != "system" || chain[0].Subtype != "compact_boundary" {
		t.Errorf("chain[0] = %s/%s, want system/compact_boundary", chain[0].Type, chain[0].Subtype)
	}
	if chain[1].Type != "user" || !strings.Contains(chain[1].Content, "summarized head") {
		t.Errorf("chain[1] = %s %q, want user summary row", chain[1].Type, chain[1].Content)
	}
	if chain[1].ParentUUID != chain[0].UUID {
		t.Errorf("summary.ParentUUID = %q, want boundary %q", chain[1].ParentUUID, chain[0].UUID)
	}
	if chain[2].UUID != "a-1" || chain[2].ParentUUID != chain[1].UUID {
		t.Errorf("kept[0] = %s parent %q, want a-1 parented at summary %q", chain[2].UUID, chain[2].ParentUUID, chain[1].UUID)
	}
	if chain[3].UUID != "u-2" || chain[3].ParentUUID != "a-1" {
		t.Errorf("kept[1] = %s parent %q, want u-2 parented at a-1", chain[3].UUID, chain[3].ParentUUID)
	}

	meta, err := extractCompactMetadata(chain[0])
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}
	if meta.PreservedSegment == nil {
		t.Fatal("partial compact boundary has no preservedSegment")
	}
	if meta.PreservedSegment.AnchorUUID != chain[1].UUID {
		t.Errorf("anchor = %q, want summary uuid %q (head-summarizing compacts anchor at summaryMessages.at(-1), TS compact.ts:1078-1080)", meta.PreservedSegment.AnchorUUID, chain[1].UUID)
	}

	// metadata column round-trip: the reloaded engine message keeps the flag.
	summaryEngine := StoreMessageToEngine(chain[1])
	if !summaryEngine.HasFlag(types.FlagCompactSummary) {
		var metadata string
		if err := store.db.QueryRow("SELECT metadata FROM messages WHERE uuid = ?", chain[1].UUID).Scan(&metadata); err != nil {
			t.Fatalf("read summary metadata column: %v", err)
		}
		t.Errorf("flag lost on reload, metadata column = %q", metadata)
	}
}

// TestRecordCompact_SummaryWithoutKeptTail covers the whole-history compact
// shape: a summary but no kept tail (no preserved segment). The re-anchor
// guard must no-op and the summary must still persist and reload.
func TestRecordCompact_SummaryWithoutKeptTail(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*TranscriptMessage{CreateCompactSummaryMessage("everything summarized")},
	}
	if err := store.RecordCompact(sessionID, result); err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	chain, err := store.LoadChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want 2 (boundary + summary)", len(chain))
	}
	if chain[1].ParentUUID != chain[0].UUID {
		t.Errorf("summary.ParentUUID = %q, want boundary %q", chain[1].ParentUUID, chain[0].UUID)
	}

	meta, err := extractCompactMetadata(chain[0])
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}
	if meta.PreservedSegment != nil {
		t.Errorf("whole-history compact grew a preservedSegment: %+v", meta.PreservedSegment)
	}
}

// TestRecordCompact_MetadataColumnWritten pins insertMessageTx's metadata
// write directly: a NULL metadata column silently strips flags from every
// boundary/summary row RecordCompact persists.
func TestRecordCompact_MetadataColumnWritten(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// CreateCompactBoundaryMessage sets flag metadata; RecordCompact must not
	// drop it on insert.
	result := &CompactResult{BoundaryMarker: CreateCompactBoundaryMessage("auto", 100, "")}
	if err := store.RecordCompact(sessionID, result); err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	chain, err := store.LoadChainMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("chain length = %d, want 1 (boundary)", len(chain))
	}
	if !strings.Contains(chain[0].Metadata, `"flags"`) {
		t.Errorf("boundary metadata = %q, want flags field persisted", chain[0].Metadata)
	}
	var metadata struct {
		Flags types.MessageFlag `json:"flags"`
	}
	if err := json.Unmarshal([]byte(chain[0].Metadata), &metadata); err != nil {
		t.Fatalf("parse boundary metadata: %v", err)
	}
	if metadata.Flags&types.FlagCompactSummary == 0 {
		t.Errorf("boundary flags = %d, want FlagCompactSummary bit set", metadata.Flags)
	}
}

func chainUUIDs(msgs []*TranscriptMessage) []string {
	uuids := make([]string, len(msgs))
	for i, m := range msgs {
		uuids[i] = m.UUID
	}
	return uuids
}
