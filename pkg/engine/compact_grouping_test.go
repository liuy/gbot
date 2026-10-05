package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// TestCompact_RetainSplitKeepsToolPairsIntact pins the compact retain-split to
// API-round boundaries (TS services/compact/grouping.ts — groupMessagesByApiRound:
// one group per API round-trip, boundary at each new assistant response).
//
// The token-budget split (rescaledKeepFrom) can land between an
// assistant(tool_use) and its user(tool_result). That orphaned tool_result is
// persisted with the retained tail; after restart the reloaded payload leads
// with [boundary, summary, user(tool_result)], and EnsureToolResultPairing —
// whose predecessor-must-be-assistant rule fires on any user tool_result not
// preceded by an assistant — strips or drops it, destroying the compact
// boundary block and the prompt-cache prefix with it.
//
// Fixture (keep budget 4000 tokens via contextWindow 20000; measured store
// estimates: a-1 ≈ 2.3K, u-2 ≈ 1.9K, rest ≤ 70): the CJK-rescaled budget walk
// keeps [u-2..u-5] (≈ 2.0K ≤ effective budget ≈ 4.2K) and stops when adding
// a-1 tips over — keepFrom = 2, cutting right between a-1(tool_use tu_1) and
// u-2(tool_result tu_1). Group boundaries are [u-0], [a-1,u-2], [a-3,u-4,u-5],
// so the aligned split must move to 3: tail [a-3,u-4,u-5], head [u-0,a-1,u-2].
func TestCompact_RetainSplitKeepsToolPairsIntact(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &compactCaptureProvider{}
	sc := NewAutoCompactor(store, &testEngineMeta{
		model:         "test-model",
		sessionID:     session.SessionID,
		contextWindow: 20000, // keep budget = 20000/5 = 4000
		provider:      p,
	})

	ts := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	resultJSON := json.RawMessage(`"` + strings.Repeat("y", 17000) + `"`)
	msgs := []types.Message{
		{ID: "u-0", Role: types.RoleUser, Timestamp: ts,
			Content: []types.ContentBlock{types.NewTextBlock("please list the files")}},
		{ID: "a-1", Role: types.RoleAssistant, Timestamp: ts, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("x", 20000)),
			types.NewToolUseBlock("tu_1", "Bash", json.RawMessage(`{"command":"ls"}`)),
		}},
		{ID: "u-2", Role: types.RoleUser, Timestamp: ts, Content: []types.ContentBlock{
			types.NewToolResultBlock("tu_1", resultJSON, false),
		}},
		{ID: "a-3", Role: types.RoleAssistant, Timestamp: ts, Content: []types.ContentBlock{
			types.NewTextBlock("reading the listing"),
			types.NewToolUseBlock("tu_2", "Read", json.RawMessage(`{"file_path":"/a.go"}`)),
		}},
		{ID: "u-4", Role: types.RoleUser, Timestamp: ts, Content: []types.ContentBlock{
			types.NewToolResultBlock("tu_2", json.RawMessage(`"file contents"`), false),
		}},
		{ID: "u-5", Role: types.RoleUser, Timestamp: ts,
			Content: []types.ContentBlock{types.NewTextBlock("thanks, summarize")}},
	}

	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.BeforeMessages != len(msgs) {
		t.Errorf("BeforeMessages = %d, want %d", result.BeforeMessages, len(msgs))
	}

	// (a) The retained tail must start at an API-round boundary — an assistant
	// message — never at the user(tool_result) of a pair whose assistant stayed
	// in the summarized head.
	kept := result.MessagesToKeep
	if len(kept) != 3 {
		describeKept := make([]string, 0, len(kept))
		for _, m := range kept {
			describeKept = append(describeKept, m.Type+"["+firstKeptBlockType(m)+"]")
		}
		t.Fatalf("retained tail: got %d messages (%v), want 3 (a-3, u-4, u-5) — "+
			"the split cut between assistant(tool_use) and user(tool_result)",
			len(kept), describeKept)
	}
	if kept[0].Type != "assistant" {
		t.Errorf("retained tail starts at %s message, want assistant (API-round boundary)", kept[0].Type)
	}
	keptToolUse := blockByType(short.ParseContentBlocks(kept[0].Content), types.ContentTypeToolUse)
	if keptToolUse == nil || keptToolUse.ID != "tu_2" {
		t.Errorf("retained assistant should carry tool_use tu_2, got %+v", keptToolUse)
	}
	keptResult := blockByType(short.ParseContentBlocks(kept[1].Content), types.ContentTypeToolResult)
	if kept[1].Type != "user" || keptResult == nil || keptResult.ToolUseID != "tu_2" {
		t.Errorf("second retained message should be user(tool_result tu_2), got type=%s block=%+v",
			kept[1].Type, keptResult)
	}

	// (b) Restart chain, exactly as runCompact persists and a new process
	// reloads: RecordCompact → LoadPostCompactChainMessages. EnsureToolResultPairing
	// must be a no-op on the reloaded payload.
	if err := store.RecordCompact(session.SessionID, result); err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}
	reloaded, err := store.LoadPostCompactChainMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadPostCompactChainMessages: %v", err)
	}
	if len(reloaded) != 4 {
		t.Fatalf("reloaded chain: got %d messages, want 4 (boundary + 3 kept)", len(reloaded))
	}
	engineReloaded := make([]types.Message, 0, len(reloaded))
	for _, m := range reloaded {
		engineReloaded = append(engineReloaded, short.StoreMessageToEngine(m))
	}
	paired := EnsureToolResultPairing(engineReloaded)
	if len(paired) != len(engineReloaded) {
		t.Errorf("EnsureToolResultPairing changed the reloaded payload: %d → %d messages — "+
			"the retained tail led with an orphaned tool_result",
			len(engineReloaded), len(paired))
	}
	wantRoles := []types.Role{types.RoleSystem, types.RoleAssistant, types.RoleUser, types.RoleUser}
	for i, want := range wantRoles {
		if paired[i].Role != want {
			t.Errorf("paired[%d].Role = %s, want %s", i, paired[i].Role, want)
		}
	}
	for _, m := range paired {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "Orphaned tool result removed") ||
				strings.Contains(b.Text, "[Tool result missing due to internal error]") {
				t.Errorf("repair placeholder leaked into reloaded payload: %q", b.Text)
			}
		}
	}

	// (c) Boundary and summary messages survive at the head of the built result.
	built := result.Messages
	if len(built) != 5 {
		t.Fatalf("built result: got %d messages, want 5 (boundary + summary + 3 kept)", len(built))
	}
	for i := range 2 {
		if built[i].Flags&types.FlagCompactSummary == 0 {
			t.Errorf("built[%d] should carry FlagCompactSummary, flags=%v", i, built[i].Flags)
		}
	}
	if built[1].Content[0].Text == "" {
		t.Error("built[1] (summary message) has empty text")
	}

	// The summarized head must have kept the tu_1 pair together: the summarizer
	// request carries the real tool_result payload, not a synthetic placeholder.
	if p.lastReq == nil {
		t.Fatal("summarizer was never called")
	}
	var headResult *types.ContentBlock
	for i := range p.lastReq.Messages {
		for j := range p.lastReq.Messages[i].Content {
			b := &p.lastReq.Messages[i].Content[j]
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_1" {
				headResult = b
			}
		}
	}
	if headResult == nil {
		t.Fatal("tu_1 tool_result missing from summarizer request — pair split across the boundary")
	}
	if !strings.Contains(string(headResult.Content), strings.Repeat("y", 100)) {
		t.Errorf("tu_1 tool_result in summarizer request is not the real payload: %.80s", headResult.Content)
	}
}

// firstKeptBlockType returns the type of the first content block of a store
// transcript message for failure diagnostics, or "empty" when it has no blocks.
func firstKeptBlockType(m *short.TranscriptMessage) string {
	blocks := short.ParseContentBlocks(m.Content)
	if len(blocks) == 0 {
		return "empty"
	}
	return string(blocks[0].Type)
}

// blockByType returns the first block of the given type, or nil.
func blockByType(blocks []types.ContentBlock, blockType types.ContentType) *types.ContentBlock {
	for i := range blocks {
		if blocks[i].Type == blockType {
			return &blocks[i]
		}
	}
	return nil
}

// storeMsg builds a minimal store transcript message for grouping tests.
func storeMsg(id, typ string) *short.TranscriptMessage {
	return &short.TranscriptMessage{UUID: id, Type: typ, Content: `[{"type":"text","text":"x"}]`}
}

// groupIDs renders groups as per-group UUID sequences for exact comparison.
func groupIDs(groups [][]*short.TranscriptMessage) [][]string {
	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		ids := make([]string, 0, len(g))
		for _, m := range g {
			ids = append(ids, m.UUID)
		}
		out = append(out, ids)
	}
	return out
}

// TestGroupMessagesByApiRound verifies the TS grouping.ts boundary semantics:
// a boundary fires only when a NEW assistant response begins (different
// identity from the prior assistant); everything else — user prompts,
// tool_results, same-identity assistants — joins the current group.
func TestGroupMessagesByApiRound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []*short.TranscriptMessage
		want [][]string
	}{
		{
			name: "preamble user prompt forms group 0, each new assistant opens a round",
			in: []*short.TranscriptMessage{
				storeMsg("u0", "user"),
				storeMsg("a1", "assistant"),
				storeMsg("u2", "user"),
				storeMsg("a3", "assistant"),
				storeMsg("u4", "user"),
			},
			want: [][]string{{"u0"}, {"a1", "u2"}, {"a3", "u4"}},
		},
		{
			// grouping.ts:31 — [tu_A(id=X), result_A, tu_B(id=X)] stays in one
			// group: streaming chunks of one response share an identity, and
			// gbot models that as adjacent assistants with the same UUID.
			name: "tool_result interleaved between same-identity assistant chunks stays one group",
			in: []*short.TranscriptMessage{
				storeMsg("u0", "user"),
				storeMsg("X", "assistant"),
				storeMsg("rA", "user"),
				storeMsg("X", "assistant"),
				storeMsg("Y", "assistant"),
			},
			want: [][]string{{"u0"}, {"X", "rA", "X"}, {"Y"}},
		},
		{
			name: "assistant-led conversation has no preamble group",
			in: []*short.TranscriptMessage{
				storeMsg("a0", "assistant"),
				storeMsg("u1", "user"),
				storeMsg("a2", "assistant"),
			},
			want: [][]string{{"a0", "u1"}, {"a2"}},
		},
		{
			// grouping.ts compares against an undefined lastAssistantId, so
			// id-less assistants never fire a boundary; the zero UUID plays
			// undefined here.
			name: "empty-identity assistants merge into the current group",
			in: []*short.TranscriptMessage{
				storeMsg("u0", "user"),
				storeMsg("", "assistant"),
				storeMsg("u2", "user"),
				storeMsg("", "assistant"),
			},
			want: [][]string{{"u0", "", "u2", ""}},
		},
		{
			name: "empty input yields no groups",
			in:   nil,
			want: [][]string{},
		},
		{
			name: "single user message is one group",
			in:   []*short.TranscriptMessage{storeMsg("u0", "user")},
			want: [][]string{{"u0"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := groupIDs(GroupMessagesByApiRound(tt.in))
			if len(got) != len(tt.want) {
				t.Fatalf("group count = %d, want %d (got %v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if len(got[i]) != len(tt.want[i]) {
					t.Fatalf("group[%d] length = %d, want %d (groups: %v)", i, len(got[i]), len(tt.want[i]), got)
				}
				for j := range tt.want[i] {
					if got[i][j] != tt.want[i][j] {
						t.Errorf("group[%d][%d] = %q, want %q (groups: %v)", i, j, got[i][j], tt.want[i][j], got)
					}
				}
			}
		})
	}
}

// TestAlignKeepFromToRoundBoundary verifies the split snapping rule from TS
// truncateHeadForPTLRetry: only whole groups move between head and tail.
func TestAlignKeepFromToRoundBoundary(t *testing.T) {
	t.Parallel()

	// Groups [u0], [a1,u2], [a3,u4]: group starts {0, 1, 3}.
	rounds := []*short.TranscriptMessage{
		storeMsg("u0", "user"),
		storeMsg("a1", "assistant"),
		storeMsg("u2", "user"),
		storeMsg("a3", "assistant"),
		storeMsg("u4", "user"),
	}
	// Empty-identity assistants force a multi-message group 0:
	// groups [u0,"",u2], [a3,u4], starts {0, 3}.
	fusedRounds := []*short.TranscriptMessage{
		storeMsg("u0", "user"),
		storeMsg("", "assistant"),
		storeMsg("u2", "user"),
		storeMsg("a3", "assistant"),
		storeMsg("u4", "user"),
	}
	// Single group [u0,"",u2]: starts {0}, no API-safe split exists.
	singleGroup := []*short.TranscriptMessage{
		storeMsg("u0", "user"),
		storeMsg("", "assistant"),
		storeMsg("u2", "user"),
	}

	tests := []struct {
		name     string
		in       []*short.TranscriptMessage
		keepFrom int
		want     int
	}{
		{name: "already at group start stays", in: rounds, keepFrom: 1, want: 1},
		{name: "already at later group start stays", in: rounds, keepFrom: 3, want: 3},
		{name: "mid-group split between assistant and its tool_result moves forward", in: rounds, keepFrom: 2, want: 3},
		{name: "split inside multi-message group 0 moves forward to next round", in: fusedRounds, keepFrom: 1, want: 3},
		{name: "split inside last group clamps back to its start", in: rounds, keepFrom: 4, want: 3},
		{name: "single group summarizes all via the zero sentinel", in: singleGroup, keepFrom: 1, want: 0},
		{name: "single group zero sentinel also from the last index", in: singleGroup, keepFrom: 2, want: 0},
		{name: "zero passes through", in: rounds, keepFrom: 0, want: 0},
		{name: "len sentinel passes through", in: rounds, keepFrom: 5, want: 5},
		{name: "beyond len passes through", in: rounds, keepFrom: 9, want: 9},
		{name: "empty transcript passes through", in: nil, keepFrom: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := alignKeepFromToRoundBoundary(tt.in, tt.keepFrom)
			if got != tt.want {
				t.Errorf("alignKeepFromToRoundBoundary(keepFrom=%d) = %d, want %d", tt.keepFrom, got, tt.want)
			}
		})
	}
}

// TestCompact_SingleApiRound_SummarizesAll covers the single-group sentinel:
// a history with no assistant turn has no API-round boundary, so the aligned
// split is 0 and compact() summarizes everything, retaining no tail — TS
// compactConversation's shape. Guards the pkg/app restore regression where a
// user-only history stopped compacting entirely.
func TestCompact_SingleApiRound_SummarizesAll(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &compactCaptureProvider{}
	sc := NewAutoCompactor(store, &testEngineMeta{
		model:         "test-model",
		sessionID:     session.SessionID,
		contextWindow: 5000, // keep budget = 2000; 20 messages ≈ 3.3K store tokens
		provider:      p,
	})

	msgs := make([]types.Message, 0, 20)
	for i := range 20 {
		msgs = append(msgs, types.Message{
			ID:        fmt.Sprintf("u-%d", i),
			Role:      types.RoleUser,
			Timestamp: time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC),
			Content:   []types.ContentBlock{types.NewTextBlock(strings.Repeat("task content ", 100))},
		})
	}

	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(result.MessagesToKeep) != 0 {
		t.Errorf("single-round compact must retain no messages, kept %d", len(result.MessagesToKeep))
	}
	if len(result.Messages) != 2 {
		t.Fatalf("result should be [boundary, summary], got %d messages", len(result.Messages))
	}
	for i := range 2 {
		if result.Messages[i].Flags&types.FlagCompactSummary == 0 {
			t.Errorf("result.Messages[%d] should carry FlagCompactSummary, flags=%v", i, result.Messages[i].Flags)
		}
	}
	if result.BeforeMessages != 20 {
		t.Errorf("BeforeMessages = %d, want 20", result.BeforeMessages)
	}
	if result.AfterTokens >= result.BeforeTokens {
		t.Errorf("AfterTokens %d should be below BeforeTokens %d", result.AfterTokens, result.BeforeTokens)
	}
	if p.lastReq == nil {
		t.Fatal("summarizer was never called")
	}
	// Whole history reaches the summarizer: 20 messages + the compact prompt.
	if len(p.lastReq.Messages) != 21 {
		t.Errorf("summarizer request carries %d messages, want 21 (full history + compact prompt)", len(p.lastReq.Messages))
	}
	meta := parseBoundaryMetadata(t, result.Messages[0])
	if meta["trigger"] != "auto" {
		t.Errorf("boundary trigger = %v, want auto", meta["trigger"])
	}
}
