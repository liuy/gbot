package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Pipeline mock provider — supports Stream and Complete
// ---------------------------------------------------------------------------

// pipelineProvider supports both Stream (engine query loop) and
// Complete (AutoCompactor summary LLM call).
type pipelineProvider struct {
	mu          sync.Mutex
	streamResps []pipelineMockResp
	streamIdx   int
	completeFn  func(req *llm.Request) (*llm.Response, error)
}

type pipelineMockResp struct {
	events []llm.StreamEvent
	err    error
}

func (p *pipelineProvider) Name() string { return "pipeline" }

func (p *pipelineProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streamIdx >= len(p.streamResps) {
		return nil, errors.New("no more mock stream responses")
	}
	resp := p.streamResps[p.streamIdx]
	p.streamIdx++
	if resp.err != nil {
		return nil, resp.err
	}
	ch := make(chan llm.StreamEvent, len(resp.events)+1)
	go func() {
		defer close(ch)
		for _, evt := range resp.events {
			ch <- evt
		}
	}()
	return ch, nil
}

func (p *pipelineProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	p.mu.Lock()
	fn := p.completeFn
	p.mu.Unlock()
	if fn != nil {
		return fn(nil)
	}
	return &llm.Response{
		ID:    "summary-resp",
		Type:  "message",
		Role:  "assistant",
		Model: "test-model",
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "<summary>\nPipeline test summary\n</summary>"},
		},
		StopReason: "end_turn",
	}, nil
}

func (p *pipelineProvider) addStream(events []llm.StreamEvent, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamResps = append(p.streamResps, pipelineMockResp{events: events, err: err})
}

// pipelineStreamEvents creates a mock LLM text streaming response.
func pipelineStreamEvents(model, text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
}

// ---------------------------------------------------------------------------
// Integration Test: Auto-compact pipeline
// ---------------------------------------------------------------------------

// TestCompactPipeline_OldToolResultsStillAutoCompact verifies the compact
// pipeline:
//  1. Messages accumulate with old tool_results (timestamps > 60 min old —
//     historical shape only, nothing clears them)
//  2. Token count exceeds threshold → auto-compact triggers
//  3. Mock LLM generates summary → compact boundary appears
func TestCompactPipeline_OldToolResultsStillAutoCompact(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

		// Set up Store + AutoCompactor
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		store, err := short.NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer store.Close()

		session, err := store.CreateSession(tmpDir, "test-model")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// Mock provider: Complete returns summary for auto-compact
		p := &pipelineProvider{}
		p.addStream(pipelineStreamEvents("test-model", "Response after both compacts."), nil)

		compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: session.SessionID, contextWindow: 500, provider: p})
		tc := newEventCollector()
		eng := New(&Params{
			Provider:  p,
			Model:     "test-model",
			Compactor: compactor,
			AutoCompact: AutoCompactConfig{
				ContextWindow: 500,
			},
			Logger:     slog.Default(),
			Dispatcher: tc,
		})

		// Build messages: old tool_use + tool_result pairs, timestamps > 60 min
		// old (historical shape only; nothing clears them anymore).
		// Each pair contributes ~400 chars of tool_result content (~100 tokens).
		// Plus large text messages to push past auto-compact threshold.
		oldTime := baseTime.Add(-61 * time.Minute)
		var messages []types.Message

		// 10 tool_use/result pairs with old timestamps (old tool results
		// accumulate; auto-compact must still trigger on the token pressure).
		bigResult := strings.Repeat("x", 400) // ~100 tokens each
		for i := range 10 {
			id := fmt.Sprintf("tool-%d", i)
			messages = append(messages, types.Message{
				Role:      types.RoleAssistant,
				Timestamp: oldTime,
				Content: []types.ContentBlock{
					types.NewToolUseBlock(id, "Read", json.RawMessage(`{"path":"/file"}`)),
				},
			})
			messages = append(messages, types.Message{
				Role:      types.RoleUser,
				Timestamp: oldTime,
				Content: []types.ContentBlock{
					types.NewToolResultBlock(id, json.RawMessage(`"`+bigResult+`"`), false),
				},
			})
		}

		// Add large text messages to ensure auto-compact still triggers on
		// token pressure.
		// 6 messages × 200 chars = ~1200 chars = ~300 tokens > 50% of 500
		largeText := strings.Repeat("y", 2000)
		for i := range 6 {
			role := types.RoleUser
			if i%2 == 1 {
				role = types.RoleAssistant
			}
			messages = append(messages, types.Message{
				Role:      role,
				Timestamp: oldTime,
				Content:   []types.ContentBlock{types.NewTextBlock(largeText)},
			})
		}

		eng.SetMessages(messages)

		// Run Query
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result := eng.QuerySync(ctx, "continue", "")
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		// Verify auto-compact fired: compact_boundary should exist in result messages
		// When compact-all fires (keepFrom=len), all messages are replaced with
		// [boundary, summary] and cleared tool_results are not separately observable.
		foundBoundary := false
		for _, msg := range result.Messages {
			for _, block := range msg.Content {
				if block.Type == types.ContentTypeText {
					var content struct {
						Subtype string `json:"subtype"`
					}
					if json.Unmarshal([]byte(block.Text), &content) == nil &&
						content.Subtype == "compact_boundary" {
						foundBoundary = true
					}
				}
			}
		}
		if !foundBoundary {
			t.Error("expected auto-compact to produce a compact_boundary message")
		}
	})
}

// TestCompactPipeline_IdleGapAloneClearsNothing pins that a 61-minute idle
// gap clears nothing on its own: every old tool result stays intact and
// auto-compact does not fire under a large window.
func TestCompactPipeline_IdleGapAloneClearsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		store, err := short.NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer store.Close()

		session, err := store.CreateSession(tmpDir, "test-model")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		p := &pipelineProvider{}
		p.addStream(pipelineStreamEvents("test-model", "Response after idle gap."), nil)

		compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: session.SessionID, contextWindow: 40000, provider: p})
		eng := New(&Params{
			Provider:  p,
			Model:     "test-model",
			Compactor: compactor,
			AutoCompact: AutoCompactConfig{
				ContextWindow: 100000, // high threshold → auto-compact won't trigger
			},
			Logger: slog.Default(),
		})
		tc := newEventCollector()
		eng.dispatcher = tc

		// Small tool results timestamped past a 60-minute idle gap
		// (historical shape: idle gaps no longer clear anything).
		oldTime := baseTime.Add(-61 * time.Minute)
		smallResult := strings.Repeat("z", 40) // ~10 tokens
		var messages []types.Message
		for i := range 6 {
			id := fmt.Sprintf("t-%d", i)
			messages = append(messages, types.Message{
				Role:      types.RoleAssistant,
				Timestamp: oldTime,
				Content: []types.ContentBlock{
					types.NewToolUseBlock(id, "Bash", json.RawMessage(`{"cmd":"ls"}`)),
				},
			})
			messages = append(messages, types.Message{
				Role:      types.RoleUser,
				Timestamp: oldTime,
				Content: []types.ContentBlock{
					types.NewToolResultBlock(id, json.RawMessage(`"`+smallResult+`"`), false),
				},
			})
		}

		eng.SetMessages(messages)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result := eng.QuerySync(ctx, "test", "")
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		// Every tool result must still carry its original content.
		intact, cleared := 0, 0
		for _, msg := range result.Messages {
			for _, block := range msg.Content {
				if block.Type != types.ContentTypeToolResult {
					continue
				}
				if string(block.Content) == `"[Old tool result content cleared]"` {
					cleared++
				} else if strings.Contains(string(block.Content), "zzz") {
					intact++
				}
			}
		}
		if intact != 6 || cleared != 0 {
			t.Errorf("tool results after an idle gap: intact = %d, cleared = %d, want 6/0", intact, cleared)
		}

		// Auto-compact must not fire either.
		for _, msg := range result.Messages {
			for _, block := range msg.Content {
				if block.Type == types.ContentTypeText {
					var content struct {
						Subtype string `json:"subtype"`
					}
					if json.Unmarshal([]byte(block.Text), &content) == nil &&
						content.Subtype == "compact_boundary" {
						t.Error("auto-compact should NOT have triggered with high ContextWindow")
					}
				}
			}
		}
	})
}
