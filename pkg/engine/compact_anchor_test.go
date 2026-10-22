package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// The 212k-after-compact display defect: after ManualCompact shrinks the
// context, the next query's turn_start InputEstimate must fall to the
// compacted transcript's cost. A surviving billed-usage anchor (an assistant
// kept in the compact tail) instead keeps the estimate at the pre-compact
// request size.
func TestManualCompact_NextTurnEstimateReflectsCompactedContext(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ec := newEventCollector()
	mp := &testProvider{}
	mp.responses = append(mp.responses, testResponse{events: textStreamEvents("test-model", "after compact reply")})
	eng := New(&Params{Provider: mp, Dispatcher: ec, Model: "test-model"})
	t.Cleanup(func() { eng.Close() })

	ac := NewAutoCompactor(store, &testEngineMeta{
		model: "test-model", sessionID: "repro", contextWindow: 8000,
		provider: &compactMockProvider{},
	})
	eng.SetCompactor(ac, AutoCompactConfig{ContextWindow: 8000})

	// Realistic scale: the anchor's billed total matches the pre-compact
	// content plus a normal request-shape overhead (~2k), so the seeded
	// contextOverhead stays sane and the test isolates the ANCHOR survival,
	// not the overhead seed.
	head := strings.Repeat("head ", 66000)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(head)}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply 1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("question 2")}},
		{
			Role:    types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("reply 2")},
			Usage:   &types.Usage{InputTokens: 60000, CacheReadInputTokens: 8000},
		},
	})

	if _, err := eng.ManualCompact(context.Background(), compactUserMsg(), "focus"); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}

	if res := eng.QuerySync(context.Background(), "follow up", ""); res.Error != nil {
		t.Fatalf("query after compact: %v", res.Error)
	}

	turns := ec.FindEvents(types.EventTurnStart)
	if len(turns) == 0 {
		t.Fatal("no turn_start events captured")
	}
	est := turns[len(turns)-1].InputEstimate
	// The compacted transcript is boundary + summary + a short tail: its
	// unanchored cost is small. The 60k billed anchor on the tail assistant
	// describes the PRE-compact request and must not survive as the anchor
	// for post-compact estimates.
	if est <= 100 || est > 5000 {
		t.Errorf("turn_start InputEstimate after compact = %d, want the compacted range (100, 5000] — the pre-compact usage anchor survived the compaction", est)
	}
}

// The auto-compact path applies through its own site; both must strip the
// billed anchors, so pin the strip directly on runCompact's transcript too.
func TestRunCompact_StripsBilledUsageFromCompactedTranscript(t *testing.T) {
	t.Parallel()
	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return &short.CompactResult{
				BoundaryMarker: short.CreateCompactBoundaryMessage("auto", 60000, ""),
				BeforeTokens:   60000,
				AfterTokens:    5000,
				BeforeMessages: 4,
				Messages: []types.Message{
					{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("boundary")}, Flags: types.FlagCompactSummary},
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary")}},
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("kept question")}},
					{
						Role:    types.RoleAssistant,
						Content: []types.ContentBlock{types.NewTextBlock("kept reply")},
						Usage:   &types.Usage{InputTokens: 60000, CacheReadInputTokens: 8000},
					},
				},
				Summary: "<summary>compacted</summary>",
			}, nil
		},
	}
	eng := New(&Params{Provider: &testProvider{}, Dispatcher: newEventCollector(), Model: "test-model"})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 8000})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a1")}, Usage: &types.Usage{InputTokens: 60000}},
	})

	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}

	var assistants, kept int
	for _, m := range eng.Messages() {
		if m.Role == types.RoleAssistant {
			if m.Usage != nil {
				t.Errorf("assistant in compacted transcript still carries Usage %+v (billed anchor must not survive auto-compact)", m.Usage)
			}
			assistants++
		}
		if m.Role == types.RoleUser {
			kept++
		}
	}
	if assistants != 1 || kept != 2 {
		t.Errorf("post-compact transcript shape = %d assistant, %d user, want 1 assistant, 2 user (strip must not drop or alter messages)", assistants, kept)
	}
}

// A compact writes the kept tail back to the DB (RecordCompact INSERT OR
// REPLACE). Rows that keep their Usage metadata hand the dead anchor to every
// later process: a restart, a /resume or a fork reloads it and the estimate is
// back at the pre-compact request size.
func TestCompact_PersistedTailCarriesNoUsageAnchor(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(tmpDir, "t.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	p.completeFn = func(*llm.Request) (*llm.Response, error) {
		return &llm.Response{ID: "s", Type: "message", Role: "assistant", Model: "test-model",
			Content:    []types.ContentBlock{{Type: types.ContentTypeText, Text: "<summary>sum</summary>"}},
			StopReason: "end_turn"}, nil
	}
	eng := New(&Params{Provider: p, Model: "test-model", AutoCompact: AutoCompactConfig{ContextWindow: 4000}})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)

	// ~11.5k content estimate over a 2k keep budget → a real compaction that
	// keeps the tail, including the anchored assistant, verbatim.
	anchored := types.Message{Role: types.RoleAssistant,
		Content:    []types.ContentBlock{types.NewTextBlock("done")},
		Usage:      &types.Usage{InputTokens: 30000, CacheReadInputTokens: 8000},
		Model:      "test-model",
		StopReason: "end_turn"}
	eng.SetMessages(append(append([]types.Message{}, makeLargeMessages(10, 10000)...), anchored))

	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}
	_, boundarySeq, err := store.GetLastBoundary(sess.SessionID)
	if err != nil {
		t.Fatalf("GetLastBoundary: %v", err)
	}
	if boundarySeq == 0 {
		t.Errorf("no compact_boundary row after runCompact — the apply must persist the boundary before swapping the transcript")
	}

	// A second engine over the same DB is the restart/switch boundary.
	engB := New(&Params{Provider: &testProvider{}, Model: "test-model"})
	t.Cleanup(func() { engB.Close() })
	engB.SetStore(store, tmpDir)
	reloaded, err := engB.SwitchSession(sess.SessionID)
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if len(reloaded) == 0 {
		t.Fatal("SwitchSession reloaded no messages — setup invalid")
	}

	var tail *types.Message
	for i, m := range reloaded {
		if m.Role == types.RoleAssistant {
			tail = &reloaded[i]
		}
	}
	if tail == nil {
		t.Fatal("no assistant in the reloaded transcript — setup invalid")
	}
	if tail.Usage != nil {
		t.Errorf("reloaded assistant carries Usage %+v — RecordCompact must write the kept tail without the billed anchor", tail.Usage)
	}
	if tail.Model != "test-model" || tail.StopReason != "end_turn" {
		t.Errorf("reloaded assistant metadata = model %q stop_reason %q, want test-model/end_turn — the strip must clear Usage and keep the rest",
			tail.Model, tail.StopReason)
	}
	want := EstimateMessagesTokens(engB.Messages())
	if got := engB.currentInputTokens(); got != want {
		t.Errorf("reloaded currentInputTokens = %d, want the content estimate %d — the pre-compact anchor (billed 38000) revived through the DB", got, want)
	}
}

// A noop compact (everything already fits the tail budget) returns the input
// messages untouched: every one of them still describes the request that
// billed it, so the anchor stays authoritative and must survive.
func TestRunCompact_NoopKeepsLiveUsageAnchor(t *testing.T) {
	t.Parallel()
	compactor := &funcCompactor{
		fn: func(_ context.Context, msgs []types.Message) (*short.CompactResult, error) {
			total := TokenCountWithEstimation(msgs)
			return &short.CompactResult{
				BeforeTokens:   total,
				AfterTokens:    total,
				BeforeMessages: len(msgs),
				Messages:       msgs,
			}, nil
		},
	}
	eng := New(&Params{Provider: &testProvider{}, Dispatcher: newEventCollector(), Model: "test-model"})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 8000})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a1")},
			Usage: &types.Usage{InputTokens: 60000, CacheReadInputTokens: 8000}},
	})

	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}

	var anchor *types.Usage
	for _, m := range eng.Messages() {
		if m.Role == types.RoleAssistant {
			anchor = m.Usage
		}
	}
	if anchor == nil || anchor.TotalInputTokens() != 68000 {
		t.Errorf("noop compact left the tail assistant with Usage %+v, want the billed 68000 — an unchanged transcript keeps its anchor", anchor)
	}
	if got := eng.currentInputTokens(); got != 68000 {
		t.Errorf("currentInputTokens after a noop compact = %d, want the billed 68000 — a compact that changed nothing must not discard the live usage anchor", got)
	}
	if got := eng.GetContextTokens(); got != 68000 {
		t.Errorf("ContextTokens after a noop compact = %d, want 68000 (the anchored measurement the compactor reported)", got)
	}
}

// The session-memory compact path prices AfterTokens through the same
// overhead-aware estimator, so the seed must be in place before it too: a
// resumed engine whose compact resolves from notes (no LLM call) must not
// land a content-only ledger.
func TestRunCompact_SMPathSeedsOverheadBeforePricing(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(tmpDir, "t.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sm := session.New(session.DefaultConfig(), tmpDir, "main", nil, nil)
	notesPath := sm.NotesPath()
	if err := os.MkdirAll(filepath.Dir(notesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notesPath, []byte("# Session Notes\n## Session Title\nseed timing\n## Current State\npricing AfterTokens\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &integrationProvider{}
	p.completeFn = func(*llm.Request) (*llm.Response, error) {
		return &llm.Response{ID: "s", Type: "message", Role: "assistant", Model: "test-model",
			Content:    []types.ContentBlock{{Type: types.ContentTypeText, Text: "<summary>sum</summary>"}},
			StopReason: "end_turn"}, nil
	}
	eng := New(&Params{Provider: p, Model: "test-model", AutoCompact: AutoCompactConfig{ContextWindow: 4000}})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)
	// New(Params{AutoCompact}) builds the engine's own AutoCompactor with the
	// engine itself as meta, so the overhead closure reads real seeded state.
	eng.SetSessionMemory(sm)

	msgs := makeLargeMessages(10, 10000) // ~11.5k est → exceeds the keep budget
	anchored := types.Message{Role: types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("done")},
		Usage:   &types.Usage{InputTokens: 30000}}
	pre := append(append([]types.Message{}, msgs...), anchored)
	eng.SetMessages(pre)
	seedWant := 30000 - EstimateMessagesTokens(pre)
	if seedWant <= 0 {
		t.Fatalf("setup invalid: seed %d must be positive", seedWant)
	}

	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}
	if got := eng.ContextOverheadTokens(); got != seedWant {
		t.Errorf("ContextOverheadTokens = %d, want seed %d — the SM compact path did not seed from the live anchor", got, seedWant)
	}
	want := EstimateMessagesTokens(eng.Messages()) + seedWant
	if got := eng.GetContextTokens(); got != want {
		t.Errorf("ContextTokens = %d, want %d (est + seed %d) — SM-path AfterTokens priced before the seed landed", got, want, seedWant)
	}
}
