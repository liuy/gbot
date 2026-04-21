package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// TestCacheWarmThenHit verifies the expected cache token lifecycle:
//
//	Turn 1 (cache warm): CacheCreationInputTokens > 0, CacheReadInputTokens = 0
//	Turn 2 (cache hit):  CacheReadInputTokens > 0, CacheCreationInputTokens = 0, InputTokens ≈ 0
//
// This tests that the engine correctly passes CacheControl to the API request
// and correctly accumulates cache tokens from streaming responses.
func TestCacheWarmThenHit(t *testing.T) {
	callCount := 0

	// multiCallProvider returns different responses for each call:
	//   Call 1: cache warm (CacheCreation > 0, CacheRead = 0)
	//   Call 2: cache hit  (CacheRead > 0, CacheCreation = 0, InputTokens = 0)
	cp := &multiCallProvider{
		handler: func() []llm.StreamEvent {
			callCount++
			if callCount == 1 {
				// Turn 1: cache warm — system prompt is being cached
				return cacheStreamEventsDetailed(
					"msg_warm", 0, 4800, // InputTokens=0, CacheCreation=4800
					"hello!", 25, // output
				)
			}
			// Turn 2: cache hit — system prompt read from cache
			return cacheStreamEventsDetailed(
				"msg_hit", 0, 0, // InputTokens=0, CacheCreation=0
				"hi there!", 30, // output
			)
			// CacheRead comes in message_delta (simulates MiniMax behavior)
		},
		cacheReadOnDelta: 5600, // cache read reported in message_delta on turn 2
	}

	eng := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Provider: cp,
	})

	sysPrompt, _ := json.Marshal("You are a helpful coding assistant with a long system prompt that spans many tokens.")

	// Turn 1: cache warm
	result1 := eng.QuerySync(context.Background(), "你好", sysPrompt)
	if result1.Error != nil {
		t.Fatalf("turn 1 error: %v", result1.Error)
	}

	// Verify turn 1: CacheCreation > 0, CacheRead = 0
	if result1.TotalUsage.CacheCreationInputTokens == 0 {
		t.Errorf("turn 1 CacheCreationInputTokens = 0, expected > 0 (system prompt should be cached)")
	}
	if result1.TotalUsage.CacheReadInputTokens != 0 {
		t.Errorf("turn 1 CacheReadInputTokens = %d, expected 0 (no cache to read on first call)",
			result1.TotalUsage.CacheReadInputTokens)
	}
	// InputTokens should be non-cache portion (user message only)
	t.Logf("turn 1: input=%d creation=%d read=%d output=%d",
		result1.TotalUsage.InputTokens,
		result1.TotalUsage.CacheCreationInputTokens,
		result1.TotalUsage.CacheReadInputTokens,
		result1.TotalUsage.OutputTokens)

	// Turn 2: cache hit
	result2 := eng.QuerySync(context.Background(), "测试消息", sysPrompt)
	if result2.Error != nil {
		t.Fatalf("turn 2 error: %v", result2.Error)
	}

	// Verify turn 2: CacheRead > 0 (system prompt hit cache)
	if result2.TotalUsage.CacheReadInputTokens == 0 {
		t.Errorf("turn 2 CacheReadInputTokens = 0, expected > 0 (system prompt should be in cache)")
	}
	if result2.TotalUsage.CacheCreationInputTokens != 0 {
		t.Errorf("turn 2 CacheCreationInputTokens = %d, expected 0 (cache already warm)",
			result2.TotalUsage.CacheCreationInputTokens)
	}

	totalInput := result2.TotalUsage.TotalInputTokens()
	t.Logf("turn 2: input=%d creation=%d read=%d total=%d output=%d",
		result2.TotalUsage.InputTokens,
		result2.TotalUsage.CacheCreationInputTokens,
		result2.TotalUsage.CacheReadInputTokens,
		totalInput,
		result2.TotalUsage.OutputTokens)

	// Verify the request had CacheControl set
	if cp.lastReq == nil {
		t.Fatal("no request captured")
	}
	if cp.lastReq.CacheControl == nil {
		t.Error("expected CacheControl to be set on request")
	}
	if cp.lastReq.CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want 'ephemeral'", cp.lastReq.CacheControl.Type)
	}

	// Verify total calls
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

// TestCacheTokenNoDoubleCount verifies that cache tokens reported in both
// message_start and message_delta are NOT double-counted.
// This guards against providers that echo cache_creation/cache_read in
// message_delta after already reporting them in message_start.
func TestCacheTokenNoDoubleCount(t *testing.T) {
	// Provider that reports CacheCreation=4800 in BOTH message_start AND message_delta.
	cp := &doubleCountProvider{
		cacheCreation: 4800,
		cacheRead:     0,
	}

	eng := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Provider: cp,
	})

	sysPrompt, _ := json.Marshal("You are a helpful assistant with a long system prompt.")
	result := eng.QuerySync(context.Background(), "hello", sysPrompt)
	if result.Error != nil {
		t.Fatalf("error: %v", result.Error)
	}

	// Should be exactly 4800, NOT 9600 (which would happen with += accumulation)
	if result.TotalUsage.CacheCreationInputTokens != 4800 {
		t.Errorf("CacheCreationInputTokens = %d, want 4800 (no double-counting)",
			result.TotalUsage.CacheCreationInputTokens)
	}
	t.Logf("cache_creation=%d (expected exactly 4800, not 9600)",
		result.TotalUsage.CacheCreationInputTokens)
}

// TestCacheTokenNoDoubleCount_Read verifies the same for cache_read tokens
// when they appear in both message_start and message_delta.
func TestCacheTokenNoDoubleCount_Read(t *testing.T) {
	// Provider that reports CacheRead=5600 in BOTH message_start AND message_delta.
	cp := &doubleCountProvider{
		cacheCreation: 0,
		cacheRead:     5600,
	}

	eng := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Provider: cp,
	})

	sysPrompt, _ := json.Marshal("You are a helpful assistant.")
	result := eng.QuerySync(context.Background(), "hello", sysPrompt)
	if result.Error != nil {
		t.Fatalf("error: %v", result.Error)
	}

	if result.TotalUsage.CacheReadInputTokens != 5600 {
		t.Errorf("CacheReadInputTokens = %d, want 5600 (no double-counting)",
			result.TotalUsage.CacheReadInputTokens)
	}
	t.Logf("cache_read=%d (expected exactly 5600, not 11200)",
		result.TotalUsage.CacheReadInputTokens)
}

// doubleCountProvider reports cache tokens in BOTH message_start and message_delta,
// simulating providers like MiniMax that echo cache totals in both events.
type doubleCountProvider struct {
	cacheCreation int
	cacheRead     int
	lastReq       *llm.Request
}

func (d *doubleCountProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (d *doubleCountProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	d.lastReq = req
	events := []llm.StreamEvent{
		{
			Type: "message_start",
			Message: &llm.MessageStart{
				ID:    "msg_double",
				Model: "test-model",
				Usage: types.Usage{
					InputTokens:              10,
					CacheCreationInputTokens: d.cacheCreation,
					CacheReadInputTokens:     d.cacheRead,
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
			Delta: &llm.StreamDelta{Type: "text_delta", Text: "hi!"},
		},
		{
			Type:  "content_block_stop",
			Index: 0,
		},
		{
			Type:     "message_delta",
			DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			// Echo the same cache tokens in message_delta (triggers double-counting with +=)
			Usage: &llm.UsageDelta{
				OutputTokens:             5,
				InputTokens:              10,
				CacheCreationInputTokens: d.cacheCreation,
				CacheReadInputTokens:     d.cacheRead,
			},
		},
		{
			Type: "message_stop",
		},
	}

	ch := make(chan llm.StreamEvent, len(events)+1)
	go func() {
		defer close(ch)
		for _, evt := range events {
			ch <- evt
		}
	}()
	return ch, nil
}

// cacheStreamEventsDetailed creates streaming events with explicit cache token values.
func cacheStreamEventsDetailed(msgID string, inputTokens, cacheCreation int, text string, outputTokens int) []llm.StreamEvent {
	return []llm.StreamEvent{
		{
			Type: "message_start",
			Message: &llm.MessageStart{
				ID:    msgID,
				Model: "test-model",
				Usage: types.Usage{
					InputTokens:              inputTokens,
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
			Delta: &llm.StreamDelta{Type: "text_delta", Text: text},
		},
		{
			Type:  "content_block_stop",
			Index: 0,
		},
		{
			Type:     "message_delta",
			DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			Usage:    &llm.UsageDelta{OutputTokens: outputTokens},
		},
		{
			Type: "message_stop",
		},
	}
}

// multiCallProvider calls handler() for each Stream call to get events.
type multiCallProvider struct {
	handler         func() []llm.StreamEvent
	lastReq         *llm.Request
	cacheReadOnDelta int // if > 0, inject CacheRead in message_delta
	callIndex       int
}

func (m *multiCallProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *multiCallProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	m.lastReq = req
	events := m.handler()

	// Inject cache_read in message_delta for call index > 0 (cache hit scenario)
	m.callIndex++
	if m.cacheReadOnDelta > 0 && m.callIndex > 1 {
		for i := range events {
			if events[i].Type == "message_delta" && events[i].Usage != nil {
				events[i].Usage.CacheReadInputTokens = m.cacheReadOnDelta
			}
		}
	}

	ch := make(chan llm.StreamEvent, len(events)+1)
	go func() {
		defer close(ch)
		for _, evt := range events {
			ch <- evt
		}
	}()
	return ch, nil
}
