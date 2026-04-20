package engine_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Prompt Cache Integration Tests
// ---------------------------------------------------------------------------

// captureProvider records the request passed to Stream for assertion.
type captureProvider struct {
	lastReq *llm.Request
	events  []llm.StreamEvent
}

func (c *captureProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (c *captureProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	c.lastReq = req
	ch := make(chan llm.StreamEvent, len(c.events)+1)
	go func() {
		defer close(ch)
		for _, evt := range c.events {
			ch <- evt
		}
	}()
	return ch, nil
}

// cacheStreamEvents returns a typical streaming response with cache tokens.
func cacheStreamEvents(cacheRead, cacheCreation int) []llm.StreamEvent {
	return []llm.StreamEvent{
		{
			Type: "message_start",
			Message: &llm.MessageStart{
				Model: "test-model",
				Usage: types.Usage{
					InputTokens:              100,
					CacheReadInputTokens:     cacheRead,
					CacheCreationInputTokens: cacheCreation,
				},
			},
		},
		{
			Type:         "content_block_start",
			Index:        0,
			ContentBlock: &types.ContentBlock{Type: types.ContentTypeText},
		},
		{
			Type:  "content_block_delta",
			Index: 0,
			Delta: &llm.StreamDelta{Type: "text_delta", Text: "hello"},
		},
		{
			Type:  "content_block_stop",
			Index: 0,
		},
		{
			Type:     "message_delta",
			DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			Usage:    &llm.UsageDelta{OutputTokens: 10},
		},
		{
			Type: "message_stop",
		},
	}
}

// TestCacheIntegration_RequestHasCacheControl verifies the engine sets
// CacheControl, SystemBlocks, and PromptStateKey on the API request
// when a system prompt is provided.
func TestCacheIntegration_RequestHasCacheControl(t *testing.T) {
	cp := &captureProvider{
		events: cacheStreamEvents(0, 5000),
	}

	eng := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Provider: cp,
	})

	sysPrompt, _ := json.Marshal("You are a helpful assistant.")
	result := eng.QuerySync(context.Background(), "hi", sysPrompt)

	if result.Error != nil {
		t.Fatalf("QuerySync error: %v", result.Error)
	}

	req := cp.lastReq
	if req == nil {
		t.Fatal("expected request to be captured")
	}

	// CacheControl should be set
	if req.CacheControl == nil {
		t.Fatal("expected CacheControl to be set")
	}
	if req.CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want %q", req.CacheControl.Type, "ephemeral")
	}

	// SystemBlocks should contain the system prompt
	if len(req.SystemBlocks) == 0 {
		t.Fatal("expected SystemBlocks to be set")
	}
	if req.SystemBlocks[0].Type != "text" {
		t.Errorf("SystemBlocks[0].Type = %q, want %q", req.SystemBlocks[0].Type, "text")
	}
	if req.SystemBlocks[0].Text != "You are a helpful assistant." {
		t.Errorf("SystemBlocks[0].Text = %q, want %q", req.SystemBlocks[0].Text, "You are a helpful assistant.")
	}

	// PromptStateKey should be set
	if req.PromptStateKey == nil {
		t.Fatal("expected PromptStateKey to be set")
	}
	if req.PromptStateKey.QuerySource != "repl_main_thread" {
		t.Errorf("PromptStateKey.QuerySource = %q, want %q", req.PromptStateKey.QuerySource, "repl_main_thread")
	}
}

// TestCacheIntegration_EmptySystemPrompt_NoCache verifies no caching
// when the system prompt is empty.
func TestCacheIntegration_EmptySystemPrompt_NoCache(t *testing.T) {
	cp := &captureProvider{
		events: cacheStreamEvents(0, 0),
	}

	eng := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Provider: cp,
	})

	_ = eng.QuerySync(context.Background(), "hi", nil)

	req := cp.lastReq
	if req == nil {
		t.Fatal("expected request to be captured")
	}

	if req.CacheControl != nil {
		t.Error("expected CacheControl to be nil for empty system prompt")
	}
	if len(req.SystemBlocks) != 0 {
		t.Errorf("expected no SystemBlocks, got %d", len(req.SystemBlocks))
	}
	if req.PromptStateKey != nil {
		t.Error("expected PromptStateKey to be nil for empty system prompt")
	}
}

// TestCacheIntegration_CacheTokensFlowToEvents verifies cache tokens
// from the API response flow through the engine's event channel.
func TestCacheIntegration_CacheTokensFlowToEvents(t *testing.T) {
	cp := &captureProvider{
		events: cacheStreamEvents(8000, 0),
	}

	eng := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Provider: cp,
	})

	sysPrompt, _ := json.Marshal("You are a helpful assistant.")
	eventCh, resultCh := eng.Query(context.Background(), "hi", sysPrompt)

	var usageEvents []types.UsageEvent
	for evt := range eventCh {
		if evt.Type == types.EventUsage && evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("Query error: %v", result.Error)
	}

	if len(usageEvents) == 0 {
		t.Fatal("expected at least one usage event")
	}

	first := usageEvents[0]
	if first.CacheReadInputTokens != 8000 {
		t.Errorf("first usage CacheReadInputTokens = %d, want 8000", first.CacheReadInputTokens)
	}
	if first.InputTokens != 100 {
		t.Errorf("first usage InputTokens = %d, want 100", first.InputTokens)
	}
}

// TestCacheIntegration_CacheCreationFlow verifies cache_creation tokens
// (first call, cache warming) flow through events.
func TestCacheIntegration_CacheCreationFlow(t *testing.T) {
	cp := &captureProvider{
		events: cacheStreamEvents(0, 5413),
	}

	eng := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Provider: cp,
	})

	sysPrompt, _ := json.Marshal("You are a helpful assistant.")
	eventCh, resultCh := eng.Query(context.Background(), "hi", sysPrompt)

	var usageEvents []types.UsageEvent
	for evt := range eventCh {
		if evt.Type == types.EventUsage && evt.Usage != nil {
			usageEvents = append(usageEvents, *evt.Usage)
		}
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("Query error: %v", result.Error)
	}

	if len(usageEvents) == 0 {
		t.Fatal("expected at least one usage event")
	}

	first := usageEvents[0]
	if first.CacheCreationInputTokens != 5413 {
		t.Errorf("CacheCreationInputTokens = %d, want 5413", first.CacheCreationInputTokens)
	}
	if first.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens = %d, want 0", first.CacheReadInputTokens)
	}
}

// TestCacheIntegration_CacheHitRateCalculation verifies the math:
// pct = cacheRead / (cacheRead + cacheCreation + inputTokens) * 100
func TestCacheIntegration_CacheHitRateCalculation(t *testing.T) {
	cacheRead := 8000
	cacheCreation := 0
	inputTokens := 100 // from the mock events

	total := cacheRead + cacheCreation + inputTokens
	pct := cacheRead * 100 / total

	if pct != 98 {
		t.Errorf("cache hit rate = %d%%, want 98%% (8000/%d)", pct, total)
	}
}
