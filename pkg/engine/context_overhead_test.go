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
	msgs := makeLargeMessages(10, 10000) // 每条 ~1148 est × 10 ≈ 11.5k > 8000 budget → 真实压缩
	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	want := EstimateMessagesTokens(result.Messages) + 20000
	if result.AfterTokens != want {
		t.Errorf("AfterTokens = %d, want %d (est(%d) + overhead 20000) — 压缩后账本缺静态开销",
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
		t.Errorf("fallback estimation = %d, want %d — 回退路径必须复现锚点口径（含静态开销）", got, total)
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
	// 不传 Compactor，只配 AutoCompact → engine.go 自动创建、meta=engine → 探针生效
	eng := New(&Params{Provider: p, Model: "test-model", AutoCompact: AutoCompactConfig{ContextWindow: 4000}})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)

	msgs := makeLargeMessages(10, 3000) // ~3.5k est > 2000 budget → 真实压缩
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
		t.Errorf("ContextTokens = %d, want %d (est(%d) + overhead %d) — AfterTokens 未入账静态开销",
			got, want, want-overheadWant, overheadWant)
	}
	// 恢复场景：持久化的会话行与内存账本一致
	row, err := store.GetSession(sess.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ContextTokens != want {
		t.Errorf("persisted ContextTokens = %d, want %d", row.ContextTokens, want)
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
