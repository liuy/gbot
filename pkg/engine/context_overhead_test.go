package engine

// context_overhead_test.go verifies the request-shape overhead accounting:
// real usage bills system prompt + tool schemas + skills + memory on top of
// message content, so post-compact token counts (AfterTokens, ContextTokens,
// fallback estimation) must include the learned overhead or the context meter
// jumps the moment the first post-compact response lands.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// overheadMeta is a testEngineMeta exposing a learned context overhead, the
// same probe NewAutoCompactor discovers on a real Engine.
type overheadMeta struct {
	testEngineMeta
	ov int
}

func (m *overheadMeta) ContextOverheadTokens() int { return m.ov }

func TestAutoCompactor_AfterTokens_IncludesEngineContextOverhead(t *testing.T) {
	t.Parallel()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sc := NewAutoCompactor(store, &overheadMeta{
		model: "m", sessionID: "s-overhead", contextWindow: 40000, provider: &compactMockProvider{},
		ov: 20000,
	})
	msgs := makeLargeMessages(10, 10000) // ~1148 est each × 10 ≈ 11.5k > 8000 budget → real compaction
	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	want := EstimateMessagesTokens(result.Messages) + 20000
	if result.AfterTokens != want {
		t.Errorf("AfterTokens = %d, want %d (est(%d) + overhead 20000) — post-compact ledger missing static overhead",
			result.AfterTokens, want, want-20000)
	}
}

// TestAutoCompactor_AfterTokens_NoProbeMeta_NoAddition: metas without the
// overhead probe keep the historical content-only AfterTokens; no value is
// invented for engines that never learn an overhead.
func TestAutoCompactor_AfterTokens_NoProbeMeta_NoAddition(t *testing.T) {
	t.Parallel()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sc := NewAutoCompactor(store, &testEngineMeta{model: "m", sessionID: "s-noprobe", contextWindow: 40000, provider: &compactMockProvider{}})
	msgs := makeLargeMessages(10, 10000)
	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	want := EstimateMessagesTokens(result.Messages)
	if result.AfterTokens != want {
		t.Errorf("AfterTokens = %d, want %d (content estimate only, no probe → no overhead)", result.AfterTokens, want)
	}
}

func TestEngine_EstimationFallback_MatchesAnchoredAfterResponse(t *testing.T) {
	t.Parallel()
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 2500}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "ok"}},
		{Type: "content_block_stop"},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			Usage: &types.Usage{InputTokens: 2500, CacheReadInputTokens: 5000, OutputTokens: 100}},
		{Type: "message_stop"},
	}, nil)
	eng := New(&Params{Provider: mp, Model: "test-model"})
	t.Cleanup(func() { eng.Close() })

	if result := eng.QuerySync(context.Background(), "test", ""); result.Error != nil {
		t.Fatalf("QuerySync: %v", result.Error)
	}
	const total = 2500 + 5000 + 100 // 7600 = TotalInput + Output
	if got := eng.GetContextTokens(); got != total {
		t.Fatalf("ContextTokens = %d, want %d", got, total)
	}
	msgs := eng.Messages()
	if got := eng.tokenCountWithEstimation(msgs); got != total {
		t.Fatalf("anchored estimation = %d, want %d", got, total)
	}
	stripped := make([]types.Message, len(msgs))
	copy(stripped, msgs)
	for i := range stripped {
		stripped[i].Usage = nil
	}
	if got := eng.tokenCountWithEstimation(stripped); got != total {
		t.Errorf("fallback estimation = %d, want %d — fallback must reproduce the anchored total (overhead included)", got, total)
	}
}

func TestEngine_RunCompact_ContextTokens_IncludesOverhead(t *testing.T) {
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
	// No Compactor passed, only AutoCompact → engine.go auto-creates it with meta=engine so the probe is live.
	eng := New(&Params{Provider: p, Model: "test-model", AutoCompact: AutoCompactConfig{ContextWindow: 4000}})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)

	msgs := makeLargeMessages(10, 3000) // ~3.5k est > 2000 budget → real compaction
	anchored := types.Message{Role: types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("done")},
		Usage:   &types.Usage{InputTokens: 9600}}
	eng.SetMessages(append(append([]types.Message{}, msgs...), anchored))
	eng.recordContextOverhead(&anchored)

	overheadWant := 9600 - EstimateMessagesTokens(eng.Messages())
	if got := eng.ContextOverheadTokens(); got != overheadWant {
		t.Fatalf("ContextOverheadTokens = %d, want %d", got, overheadWant)
	}
	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}
	want := EstimateMessagesTokens(eng.Messages()) + overheadWant
	if got := eng.GetContextTokens(); got != want {
		t.Errorf("ContextTokens = %d, want %d (est(%d) + overhead %d) — AfterTokens missing static overhead",
			got, want, want-overheadWant, overheadWant)
	}
	// Restore scenario: the persisted session row matches the in-memory ledger.
	row, err := store.GetSession(sess.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ContextTokens != want {
		t.Errorf("persisted ContextTokens = %d, want %d", row.ContextTokens, want)
	}
}

// TestEngine_RunCompact_SeedsOverhead_WhenUnlearned pins the restart
// incident: after a daemon restart the learned overhead is gone, but the
// reloaded transcript still carries its usage anchor — a manual /compact
// before any response must seed the overhead from that anchor instead of
// billing AfterTokens content-only (~20k gap).
func TestEngine_RunCompact_SeedsOverhead_WhenUnlearned(t *testing.T) {
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

	msgs := makeLargeMessages(10, 10000) // ~11.5k est → real compaction
	anchored := types.Message{Role: types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("done")},
		Usage:   &types.Usage{InputTokens: 30000}}
	pre := append(append([]types.Message{}, msgs...), anchored)
	eng.SetMessages(pre)
	// Restart scenario: the anchor is present, the learned value is not — recordContextOverhead is never called.

	seedWant := 30000 - EstimateMessagesTokens(pre)
	if seedWant <= 0 {
		t.Fatalf("setup invalid: seed %d must be positive (anchor 30000 vs est %d)",
			seedWant, EstimateMessagesTokens(pre))
	}
	if _, err := eng.runCompact(context.Background()); err != nil {
		t.Fatalf("runCompact: %v", err)
	}
	if got := eng.ContextOverheadTokens(); got != seedWant {
		t.Errorf("ContextOverheadTokens = %d, want seed %d (anchor 30000 − est %d) — first post-restart compact did not seed from the anchor",
			got, seedWant, 30000-seedWant)
	}
	want := EstimateMessagesTokens(eng.Messages()) + seedWant
	if got := eng.GetContextTokens(); got != want {
		t.Errorf("ContextTokens = %d, want %d (est(%d) + seed %d) — AfterTokens missing the anchor-seeded static overhead",
			got, want, want-seedWant, seedWant)
	}
	row, err := store.GetSession(sess.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ContextTokens != want {
		t.Errorf("persisted ContextTokens = %d, want %d", row.ContextTokens, want)
	}
}

// TestEngine_SeedContextOverhead_Guards pins the seed's contract: only the
// unset state is fillable, non-positive residuals (estimator overshoot) leave
// it unset, and a transcript without a usable anchor seeds nothing.
func TestEngine_SeedContextOverhead_Guards(t *testing.T) {
	t.Parallel()
	newEngine := func() *Engine {
		eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
		t.Cleanup(func() { eng.Close() })
		return eng
	}
	anchoredSet := func(input int) []types.Message {
		msgs := makeLargeMessages(2, 1000)
		return append(msgs, types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("done")},
			Usage:   &types.Usage{InputTokens: input}})
	}
	goodSeed := func() int { return 30000 - EstimateMessagesTokens(anchoredSet(30000)) }

	t.Run("learned_value_wins", func(t *testing.T) {
		t.Parallel()
		eng := newEngine()
		eng.SetMessages(anchoredSet(9000))
		eng.recordContextOverhead(&types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("done")},
			Usage:   &types.Usage{InputTokens: 9000}})
		want := 9000 - EstimateMessagesTokens(eng.Messages())
		eng.seedContextOverhead(anchoredSet(30000)) // would seed 30000−est ≠ want
		if got := eng.ContextOverheadTokens(); got != want {
			t.Errorf("overhead = %d after seed attempt, want learned %d — the learned value must win over the seed", got, want)
		}
	})

	t.Run("learned_zero_wins", func(t *testing.T) {
		t.Parallel()
		eng := newEngine()
		// A legitimate learned 0: the content estimate already covers the bill (clamp path).
		eng.SetMessages(makeLargeMessages(10, 12000))
		eng.recordContextOverhead(&types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("done")},
			Usage:   &types.Usage{InputTokens: 100}})
		eng.seedContextOverhead(anchoredSet(30000))
		if got := eng.ContextOverheadTokens(); got != 0 {
			t.Errorf("overhead = %d after seed attempt, want learned 0 — 0 must not be treated as the unset sentinel", got)
		}
	})

	t.Run("negative_residual_stays_unset_then_good_anchor_seeds", func(t *testing.T) {
		t.Parallel()
		eng := newEngine()
		eng.seedContextOverhead(anchoredSet(100)) // est ≈ 250 > 100 → negative residual
		if got := eng.ContextOverheadTokens(); got != 0 {
			t.Errorf("overhead = %d after negative residual, want 0 (unset)", got)
		}
		// Unset means a later good anchor can still seed.
		eng.seedContextOverhead(anchoredSet(30000))
		if got := eng.ContextOverheadTokens(); got != goodSeed() {
			t.Errorf("overhead = %d, want %d — a good anchor must still seed after a negative residual", got, goodSeed())
		}
	})

	t.Run("zero_residual_treated_as_unset", func(t *testing.T) {
		t.Parallel()
		eng := newEngine()
		pre := anchoredSet(0)
		// Anchor base exactly equals the estimate → residual 0: no seed, and the unset state is preserved.
		exact := EstimateMessagesTokens(pre)
		pre[2].Usage = &types.Usage{InputTokens: exact}
		eng.seedContextOverhead(pre)
		if got := eng.ContextOverheadTokens(); got != 0 {
			t.Errorf("overhead = %d after zero residual, want 0 (unset)", got)
		}
		eng.seedContextOverhead(anchoredSet(30000))
		if got := eng.ContextOverheadTokens(); got != goodSeed() {
			t.Errorf("overhead = %d, want %d — a zero residual must count as unset", got, goodSeed())
		}
	})

	t.Run("one_residual_seeds", func(t *testing.T) {
		t.Parallel()
		eng := newEngine()
		pre := anchoredSet(0)
		// Anchor base = est + 1 → residual is exactly 1: anything above 0 must seed.
		exact := EstimateMessagesTokens(pre)
		pre[2].Usage = &types.Usage{InputTokens: exact + 1}
		eng.seedContextOverhead(pre)
		if got := eng.ContextOverheadTokens(); got != 1 {
			t.Errorf("overhead = %d, want 1 — a residual of 1 must seed, not fall into the non-positive guard", got)
		}
	})

	t.Run("seed_is_one_shot", func(t *testing.T) {
		t.Parallel()
		eng := newEngine()
		eng.seedContextOverhead(anchoredSet(30000))
		want := goodSeed()
		// A second seed (post-compact message set with a stale anchor) must not overwrite the first.
		eng.seedContextOverhead(anchoredSet(50000))
		if got := eng.ContextOverheadTokens(); got != want {
			t.Errorf("overhead = %d after second seed, want first seed %d", got, want)
		}
	})

	t.Run("no_anchor_no_seed", func(t *testing.T) {
		t.Parallel()
		eng := newEngine()
		eng.seedContextOverhead(makeLargeMessages(4, 1000))
		if got := eng.ContextOverheadTokens(); got != 0 {
			t.Errorf("overhead = %d without usage anchor, want 0", got)
		}
	})
}

// TestEngine_ManualCompact_SeedsOverhead pins the manual /compact seam — the
// user-visible trigger of the restart incident. mockCompactor keeps the
// result shape irrelevant; the assertion is on the seeded engine state.
func TestEngine_ManualCompact_SeedsOverhead(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &mockProvider{}, Model: "test-model", Compactor: &mockCompactor{}})
	t.Cleanup(func() { eng.Close() })

	msgs := makeLargeMessages(2, 1000)
	anchored := types.Message{Role: types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("done")},
		Usage:   &types.Usage{InputTokens: 20000}}
	pre := append(append([]types.Message{}, msgs...), anchored)
	eng.SetMessages(pre)
	want := 20000 - EstimateMessagesTokens(pre)

	if _, err := eng.ManualCompact(context.Background(), types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("/compact")},
	}, ""); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}
	if got := eng.ContextOverheadTokens(); got != want {
		t.Errorf("ContextOverheadTokens = %d, want seed %d — the manual /compact path did not seed", got, want)
	}
}

func TestEngine_ContextOverhead_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("cold_start_zero", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
		t.Cleanup(func() { eng.Close() })
		want := 0
		if got := eng.ContextOverheadTokens(); got != want {
			t.Errorf("fresh engine overhead = %d, want %d", got, want)
		}
	})

	t.Run("nil_and_zero_usage_do_not_learn_or_reset", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
		t.Cleanup(func() { eng.Close() })
		eng.recordContextOverhead(&types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("done")},
			Usage:   &types.Usage{InputTokens: 5000}})
		first := eng.ContextOverheadTokens()
		if first == 0 {
			t.Fatal("first learn produced 0 overhead — setup invalid (est ≥ 5000)")
		}

		eng.recordContextOverhead(nil)
		nilUsage := types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("n")}}
		eng.recordContextOverhead(&nilUsage) // non-nil msg, nil Usage pointer
		zeroUsage := types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("z")},
			Usage:   &types.Usage{}} // non-nil but all-zero → total 0
		eng.recordContextOverhead(&zeroUsage)
		if got := eng.ContextOverheadTokens(); got != first {
			t.Errorf("overhead = %d after nil/zero-usage observations, want unchanged %d", got, first)
		}
	})

	t.Run("negative_residual_clamped_to_zero", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
		t.Cleanup(func() { eng.Close() })
		// Huge content estimate (10×12000 chars ≈ 13k est) vs tiny real bill.
		eng.SetMessages(makeLargeMessages(10, 12000))
		eng.recordContextOverhead(&types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("done")},
			Usage:   &types.Usage{InputTokens: 100}})
		want := 0
		if got := eng.ContextOverheadTokens(); got != want {
			t.Errorf("overhead = %d, want %d (estimator overshoot must clamp)", got, want)
		}
	})

	t.Run("refresh_takes_latest", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
		t.Cleanup(func() { eng.Close() })
		for _, input := range []int{5000, 9000} {
			eng.recordContextOverhead(&types.Message{Role: types.RoleAssistant,
				Content: []types.ContentBlock{types.NewTextBlock("done")},
				Usage:   &types.Usage{InputTokens: input}})
		}
		want := 9000 - EstimateMessagesTokens(eng.Messages())
		if got := eng.ContextOverheadTokens(); got != want {
			t.Errorf("overhead = %d, want latest-residual %d", got, want)
		}
	})

	t.Run("total_one_still_learns", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
		t.Cleanup(func() { eng.Close() })
		// Empty history: est = 0, so the residual equals the 1-token bill.
		eng.recordContextOverhead(&types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("done")},
			Usage:   &types.Usage{OutputTokens: 1}})
		want := 1
		if got := eng.ContextOverheadTokens(); got != want {
			t.Errorf("overhead = %d, want %d (total=1 must learn, not fall in the ≤0 guard)", got, want)
		}
	})

	t.Run("locked_fallback_matches_unlocked", func(t *testing.T) {
		t.Parallel()
		eng := New(&Params{Provider: &mockProvider{}, Model: "test-model"})
		t.Cleanup(func() { eng.Close() })
		msgs := makeLargeMessages(4, 2000)
		eng.SetMessages(msgs)
		eng.recordContextOverhead(&types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("done")},
			Usage:   &types.Usage{InputTokens: 7000}})

		want := eng.tokenCountWithEstimation(msgs)
		if want == EstimateMessagesTokens(msgs) {
			t.Fatal("setup invalid: overhead not in fallback estimate")
		}
		// RewindToScoped holds e.mu in write mode while estimating.
		eng.mu.Lock()
		got := eng.tokenCountWithEstimationLocked(msgs)
		eng.mu.Unlock()
		if got != want {
			t.Errorf("locked fallback = %d, want %d (unlocked result)", got, want)
		}
	})
}
