package short

import (
	"strings"
	"testing"
	"time"
)

// --- ParseContentBlocks ---

// Empty content is a normal state, not a parse failure: it must return an
// empty slice without the invalid-JSON warning that the failure path logs.
func TestParseContentBlocks_EmptyInputDoesNotWarn(t *testing.T) {
	logBuf := captureSlog(t)

	got := ParseContentBlocks("")
	if len(got) != 0 {
		t.Errorf("len(blocks) = %d, want 0", len(got))
	}
	if logBuf.String() != "" {
		t.Errorf("ParseContentBlocks(\"\") logged %q, want no record", logBuf.String())
	}
}

// The invalid-JSON path returns an allocated empty slice; falling through the
// guard would hand back the nil destination instead.
func TestParseContentBlocks_InvalidJSONReturnsEmptyNotNil(t *testing.T) {
	got := ParseContentBlocks("{invalid json")
	if got == nil {
		t.Fatal("ParseContentBlocks returned nil, want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("len(blocks) = %d, want 0", len(got))
	}
}

func TestParseContentBlocks_InvalidJSONWarns(t *testing.T) {
	logBuf := captureSlog(t)

	_ = ParseContentBlocks("{invalid json")
	if !strings.Contains(logBuf.String(), `msg="ParseContentBlocks: invalid JSON"`) {
		t.Errorf("log = %q, want an invalid-JSON record", logBuf.String())
	}
}

// --- ExtractTextFromJSON block filter ---

// An empty text block contributes nothing, not an extra newline.
func TestExtractTextFromJSON_EmptyTextBlockContributesNothing(t *testing.T) {
	got := ExtractTextFromJSON(`[{"type":"text","text":"a"},{"type":"text","text":""}]`)
	if got != "a" {
		t.Errorf("ExtractTextFromJSON = %q, want %q", got, "a")
	}
}

// Only text blocks are indexed; a non-text block carrying a text field must
// not leak its text.
func TestExtractTextFromJSON_NonTextBlockTextIgnored(t *testing.T) {
	got := ExtractTextFromJSON(`[{"type":"tool_use","id":"tu1","text":"leaked"}]`)
	if got != "" {
		t.Errorf("ExtractTextFromJSON = %q, want empty", got)
	}
}

// --- FilterUnresolvedToolUses ---

// The id-collection pass only reads user and assistant messages: a system
// message carrying a tool_use block must not create an unresolved id, which
// would push the filter through the rebuild path instead of the fast path.
func TestFilterUnresolvedToolUses_Pass1IgnoresOtherTypes(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "system", Content: `[{"type":"tool_use","id":"tu1"}]`},
		{Seq: 2, Type: "user", Content: `[{"type":"text","text":"hi"}]`},
	}

	assertSameFilteredSlice(t, FilterUnresolvedToolUses(messages), messages)
}

// assertSameFilteredSlice pins that the filter returned the caller's slice
// untouched: same length and the same backing array, which is what the
// nothing-unresolved fast path guarantees.
func assertSameFilteredSlice(t *testing.T, got, in []*TranscriptMessage) {
	t.Helper()

	if len(got) != len(in) {
		t.Fatalf("len(filtered) = %d, want %d", len(got), len(in))
	}
	if &got[0] != &in[0] {
		t.Error("filtered is a freshly built slice; the no-op fast path must return the input slice")
	}
}

// The id-collection pass skips other types and keeps scanning; stopping at the
// first non-user/assistant message loses later tool_use ids.
func TestFilterUnresolvedToolUses_Pass1KeepsScanningPastOtherTypes(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`},
		{Seq: 2, Type: "system", Content: `[{"type":"text","text":"noise"}]`},
		{Seq: 3, Type: "assistant", Content: `[{"type":"tool_use","id":"tu2"}]`},
	}

	got := FilterUnresolvedToolUses(messages)
	if len(got) != 1 {
		t.Errorf("len(filtered) = %d, want 1 (both unresolved assistants dropped)", len(got))
	}
	if got[0].Type != "system" {
		t.Errorf("survivor type = %q, want system", got[0].Type)
	}
}

// tool_result ids are matched by tool_use_id only: a block of another type
// carrying tool_use_id must not resolve a pending tool_use.
func TestFilterUnresolvedToolUses_OtherBlockToolUseIDDoesNotResolve(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`},
		{Seq: 2, Type: "user", Content: `[{"type":"text","text":"noise","tool_use_id":"tu1"}]`},
	}

	got := FilterUnresolvedToolUses(messages)
	if len(got) != 1 {
		t.Errorf("len(filtered) = %d, want 1 (tu1 stays unresolved, assistant dropped)", len(got))
	}
	if got[0].Type != "user" {
		t.Errorf("survivor type = %q, want user", got[0].Type)
	}
}

// With nothing unresolved the input is returned as-is, not rebuilt: the fast
// path aliases the caller's slice.
func TestFilterUnresolvedToolUses_NothingUnresolvedReturnsInputSlice(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`},
		{Seq: 2, Type: "user", Content: `[{"type":"tool_result","tool_use_id":"tu1","content":"out"}]`},
	}

	assertSameFilteredSlice(t, FilterUnresolvedToolUses(messages), messages)
}

// Only assistant messages are candidates for removal; a user message holding
// an unresolved tool_use block survives.
func TestFilterUnresolvedToolUses_KeepsNonAssistantWithToolUse(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "user", Content: `[{"type":"tool_use","id":"tu1"}]`},
	}

	got := FilterUnresolvedToolUses(messages)
	if len(got) != 1 {
		t.Errorf("len(filtered) = %d, want 1 (user messages are never filtered)", len(got))
	}
}

// Per-message tool_use ids come from tool_use blocks only: an unrelated block
// carrying an id must not make the message look unresolved.
func TestFilterUnresolvedToolUses_MsgIDsIgnoreOtherBlockTypes(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"}]`},
		{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"noise","id":"tu1"}]`},
	}

	got := FilterUnresolvedToolUses(messages)
	if len(got) != 1 {
		t.Errorf("len(filtered) = %d, want 1 (the text-block id must not count as a tool_use)", len(got))
	}
	if got[0].Seq != 2 {
		t.Errorf("survivor seq = %d, want 2", got[0].Seq)
	}
}

// A tool_use block without an id is not a tracked id, so it cannot rescue a
// message whose real tool_uses are all unresolved.
func TestFilterUnresolvedToolUses_ToolUseWithoutIDIsNotAnID(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[{"type":"tool_use","id":"tu1"},{"type":"tool_use","name":"bash"}]`},
	}

	got := FilterUnresolvedToolUses(messages)
	if len(got) != 0 {
		t.Errorf("len(filtered) = %d, want 0 (tu1 unresolved, the id-less block is not a rescue)", len(got))
	}
}

// --- FilterOrphanedThinking ---

// The sibling scan only looks at assistant messages: a user message sharing a
// UUID must not vouch for an orphaned thinking-only assistant.
func TestFilterOrphanedThinking_Pass1IgnoresNonAssistantSiblings(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "user", UUID: "a1", Content: `[{"type":"text","text":"hi"}]`},
		{Seq: 2, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking"}]`},
	}

	got := FilterOrphanedThinking(messages)
	if len(got) != 1 {
		t.Errorf("len(filtered) = %d, want 1 (the thinking-only assistant is orphaned)", len(got))
	}
	if got[0].Type != "user" {
		t.Errorf("survivor type = %q, want user", got[0].Type)
	}
}

// A thinking-only assistant is kept only when a same-UUID sibling actually has
// non-thinking content.
func TestFilterOrphanedThinking_ThinkingOnlyWithoutSiblingDropped(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", UUID: "a1", Content: `[{"type":"thinking"}]`},
	}

	got := FilterOrphanedThinking(messages)
	if len(got) != 0 {
		t.Errorf("len(filtered) = %d, want 0", len(got))
	}
}

// An empty content array is kept and the loop continues with the rest.
func TestFilterOrphanedThinking_EmptyBlocksKeptAndLoopContinues(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", UUID: "a1", Content: `[]`},
		{Seq: 2, Type: "user", UUID: "u1", Content: `[{"type":"text","text":"hi"}]`},
	}

	got := FilterOrphanedThinking(messages)
	if len(got) != 2 {
		t.Errorf("len(filtered) = %d, want 2", len(got))
	}
}

// Only assistant messages are candidates: a thinking-only user message stays.
func TestFilterOrphanedThinking_KeepsNonAssistantThinkingOnly(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "user", Content: `[{"type":"thinking"}]`},
	}

	got := FilterOrphanedThinking(messages)
	if len(got) != 1 {
		t.Errorf("len(filtered) = %d, want 1 (user messages are never filtered)", len(got))
	}
}

// --- FilterWhitespaceOnlyAssistant ---

// An assistant with an empty content array is kept and the loop continues.
func TestFilterWhitespaceOnlyAssistant_EmptyBlocksKeptAndLoopContinues(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[]`},
		{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"   "}]`},
		{Seq: 3, Type: "user", Content: `[{"type":"text","text":"hi"}]`},
	}

	got := FilterWhitespaceOnlyAssistant(messages)
	if len(got) != 2 {
		t.Errorf("len(filtered) = %d, want 2 (empty-content assistant and the user message kept)", len(got))
	}
	if got[0].Seq != 1 || got[1].Seq != 3 {
		t.Errorf("survivors = seq %d, %d, want seq 1, 3", got[0].Seq, got[1].Seq)
	}
}

// The empty-content keep is real: dropping it loses the message once any other
// message changed.
func TestFilterWhitespaceOnlyAssistant_KeepsEmptyContentAssistant(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[]`},
		{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"\t"}]`},
	}

	got := FilterWhitespaceOnlyAssistant(messages)
	if len(got) != 1 {
		t.Errorf("len(filtered) = %d, want 1 (the empty-content assistant is kept)", len(got))
	}
	if got[0].Seq != 1 {
		t.Errorf("survivor seq = %d, want 1", got[0].Seq)
	}
}

// A non-whitespace assistant is kept even when another message was filtered.
func TestFilterWhitespaceOnlyAssistant_KeepsValidAssistant(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "user", Content: `[{"type":"text","text":"hi"}]`},
		{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"real reply"}]`},
		{Seq: 3, Type: "assistant", Content: `[{"type":"text","text":"  \n "}]`},
	}

	got := FilterWhitespaceOnlyAssistant(messages)
	if len(got) != 2 {
		t.Errorf("len(filtered) = %d, want 2", len(got))
	}
	if got[1].Seq != 2 {
		t.Errorf("second survivor seq = %d, want 2", got[1].Seq)
	}
}

// Without any filtered message the input is returned untouched — the merge
// pass must not run, so adjacent user messages stay separate.
func TestFilterWhitespaceOnlyAssistant_NoChangesKeepsAdjacentUsers(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "user", Content: `[{"type":"text","text":"one"}]`},
		{Seq: 2, Type: "user", Content: `[{"type":"text","text":"two"}]`},
	}

	got := FilterWhitespaceOnlyAssistant(messages)
	if len(got) != 2 {
		t.Errorf("len(filtered) = %d, want 2 (no changes means no merge pass)", len(got))
	}
}

// Only a user message following a user message merges: an assistant after a
// user must not be folded into it.
func TestFilterWhitespaceOnlyAssistant_MergeRequiresCurrentUser(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "user", Content: `[{"type":"text","text":"one"}]`},
		{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"   "}]`},
		{Seq: 3, Type: "assistant", Content: `[{"type":"text","text":"reply"}]`},
	}

	got := FilterWhitespaceOnlyAssistant(messages)
	if len(got) != 2 {
		t.Errorf("len(filtered) = %d, want 2 (the assistant must not merge into the user)", len(got))
	}
	if got[1].Type != "assistant" {
		t.Errorf("second survivor type = %q, want assistant", got[1].Type)
	}
}

// The merge needs the *previous* message to be a user message too: a user
// following an assistant stays separate.
func TestFilterWhitespaceOnlyAssistant_MergeRequiresPreviousUser(t *testing.T) {
	messages := []*TranscriptMessage{
		{Seq: 1, Type: "assistant", Content: `[{"type":"text","text":"reply"}]`},
		{Seq: 2, Type: "assistant", Content: `[{"type":"text","text":"   "}]`},
		{Seq: 3, Type: "user", Content: `[{"type":"text","text":"one"}]`},
	}

	got := FilterWhitespaceOnlyAssistant(messages)
	if len(got) != 2 {
		t.Errorf("len(filtered) = %d, want 2 (the user must not merge into the assistant)", len(got))
	}
	if got[0].Type != "assistant" || got[1].Type != "user" {
		t.Errorf("survivors = %q, %q, want assistant, user", got[0].Type, got[1].Type)
	}
}

// --- mergeUserMessages field provenance ---

// Every field of the merged message has a documented source message; a dropped
// initialiser silently zeroes it and breaks the resumed chain.
func TestMergeUserMessages_FieldProvenance(t *testing.T) {
	t1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

	a := &TranscriptMessage{
		Seq:               11,
		SessionID:         "sess-a",
		UUID:              "u1",
		ParentUUID:        "p-a",
		LogicalParentUUID: "lpa",
		IsSidechain:       1,
		Type:              "user",
		Subtype:           "sub-a",
		Content:           `[{"type":"text","text":"first"}]`,
		CreatedAt:         t1,
	}
	b := &TranscriptMessage{
		Seq:               22,
		SessionID:         "sess-b",
		UUID:              "u2",
		ParentUUID:        "p-b",
		LogicalParentUUID: "lpb",
		IsSidechain:       0,
		Type:              "user",
		Subtype:           "sub-b",
		Content:           `[{"type":"text","text":"second"}]`,
		CreatedAt:         t2,
	}

	got := mergeUserMessages(a, b)

	if got.Seq != 11 {
		t.Errorf("Seq = %d, want 11 (from a)", got.Seq)
	}
	if got.SessionID != "sess-a" {
		t.Errorf("SessionID = %q, want sess-a (from a)", got.SessionID)
	}
	if got.UUID != "u1" {
		t.Errorf("UUID = %q, want u1 (from a)", got.UUID)
	}
	if got.ParentUUID != "p-b" {
		t.Errorf("ParentUUID = %q, want p-b (from b)", got.ParentUUID)
	}
	if got.LogicalParentUUID != "lpb" {
		t.Errorf("LogicalParentUUID = %q, want lpb (from b)", got.LogicalParentUUID)
	}
	if got.IsSidechain != 1 {
		t.Errorf("IsSidechain = %d, want 1 (from a)", got.IsSidechain)
	}
	if got.Type != "user" {
		t.Errorf("Type = %q, want user", got.Type)
	}
	if got.Subtype != "sub-a" {
		t.Errorf("Subtype = %q, want sub-a (from a)", got.Subtype)
	}
	if !got.CreatedAt.Equal(t2) {
		t.Errorf("CreatedAt = %v, want %v (the later timestamp)", got.CreatedAt, t2)
	}

	blocks := ParseContentBlocks(got.Content)
	if len(blocks) != 2 {
		t.Fatalf("merged content has %d blocks, want 2", len(blocks))
	}
	if blocks[0].Text != "first" || blocks[1].Text != "second" {
		t.Errorf("merged block texts = %q, %q, want first, second", blocks[0].Text, blocks[1].Text)
	}
}
