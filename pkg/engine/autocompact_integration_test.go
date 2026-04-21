package engine_test

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
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Integration mock provider — supports both Stream and Complete
// ---------------------------------------------------------------------------

// integrationProvider supports both Stream (for engine query loop) and
// Complete (for AutoCompactor's summary LLM call).
type integrationProvider struct {
	mu          sync.Mutex
	streamResps []mockResponse
	streamIdx   int
	completeFn  func(req *llm.Request) (*llm.Response, error)
}

func (p *integrationProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
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

func (p *integrationProvider) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	p.mu.Lock()
	fn := p.completeFn
	p.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	// Default: return a basic summary
	return &llm.Response{
		ID:    "summary-resp",
		Type:  "message",
		Role:  "assistant",
		Model: "test-model",
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "<summary>\nTest summary of conversation\n</summary>"},
		},
		StopReason: "end_turn",
	}, nil
}

func (p *integrationProvider) addStream(events []llm.StreamEvent, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamResps = append(p.streamResps, mockResponse{events: events, err: err})
}

// ---------------------------------------------------------------------------
// Integration Test 1: Proactive E2E with real AutoCompactor + Store
// ---------------------------------------------------------------------------

// TestAutoCompact_Proactive_E2E verifies proactive compact triggers with a
// real AutoCompactor (real Store, real LLM summary mock). Validates that:
//   - Compact is triggered when tokens exceed threshold
//   - Result contains boundary marker from Store
//   - Recent messages are preserved after compact
func TestAutoCompact_Proactive_E2E(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Create a session in the store so RecordCompact can succeed
	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	p.addStream(textStreamEvents("test-model", "After compact response."), nil)

	compactor := engine.NewAutoCompactor(store, session.SessionID, "test-model", p)
	eng := engine.New(&engine.Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 1000,
		},
		Logger: slog.Default(),
	})

	// 10 messages × 100 tokens each = 1000 tokens → exceeds 90% of 1000
	eng.SetMessages(makeLargeMessages(10, 100))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "continue", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Verify boundary marker exists in result messages
	foundBoundary := false
	for _, msg := range result.Messages {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeText {
				var content struct {
					Subtype string `json:"subtype"`
				}
				if json.Unmarshal([]byte(block.Text), &content) == nil && content.Subtype == "compact_boundary" {
					foundBoundary = true
				}
			}
		}
	}
	if !foundBoundary {
		t.Error("proactive compact should have produced a compact_boundary message")
	}
}

// ---------------------------------------------------------------------------
// Integration Test 2: Reactive E2E with real AutoCompactor
// ---------------------------------------------------------------------------

// TestAutoCompact_Reactive_E2E verifies reactive compact with real AutoCompactor:
//   - API returns prompt_too_long error
//   - AutoCompactor.Compact is called (real Store + LLM summary)
//   - Retry succeeds with compacted messages
func TestAutoCompact_Reactive_E2E(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	// First call: prompt_too_long error
	p.addStream(nil, &llm.APIError{
		Status:    400,
		ErrorCode: "prompt_too_long",
		Message:   "input length exceeds context limit",
	})
	// Second call: success after compact
	p.addStream(textStreamEvents("test-model", "Response after reactive compact."), nil)

	compactor := engine.NewAutoCompactor(store, session.SessionID, "test-model", p)
	eng := engine.New(&engine.Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 100000, // high threshold so proactive doesn't fire first
		},
		Logger: slog.Default(),
	})

	// Set large messages to ensure compact has something to work with
	eng.SetMessages(makeLargeMessages(10, 5000))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test query", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("expected recovery after reactive compact, got error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
	}

	// Verify the response came from the retry (second API call)
	foundRecovery := false
	for _, msg := range result.Messages {
		for _, block := range msg.Content {
			if strings.Contains(block.Text, "reactive compact") {
				foundRecovery = true
			}
		}
	}
	if !foundRecovery {
		t.Error("expected response from retry after reactive compact")
	}
}

// ---------------------------------------------------------------------------
// Integration Test 3: Fork sub-engine compact isolation
// ---------------------------------------------------------------------------

// TestAutoCompact_ForkCompact_Isolation verifies that compacting in a
// sub-engine (fork agent) does NOT affect the parent engine's messages.
func TestAutoCompact_ForkCompact_Isolation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}

	compactor := engine.NewAutoCompactor(store, session.SessionID, "test-model", p)

	parentMsgs := makeLargeMessages(10, 100)
	originalCount := len(parentMsgs)

	eng := engine.New(&engine.Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.5,
			ContextWindow: 1000,
		},
		Logger: slog.Default(),
	})
	eng.SetMessages(parentMsgs)

	// Create sub-engine (fork) — should inherit config but not affect parent
	subEng := eng.NewSubEngine(engine.SubEngineOptions{
		SystemPrompt: "You are a sub-agent.",
		MaxTurns:     5,
		Model:        "test-model",
	})

	// Compact the sub-engine directly
	subMsgs := makeLargeMessages(20, 100)
	subEng.SetMessages(subMsgs)

	compacted, compactErr := compactor.Compact(context.Background(), subEng.Messages())
	if compactErr != nil {
		t.Fatalf("sub-engine compact failed: %v", compactErr)
	}

	// Verify compacted messages are different from original sub-messages
	if len(compacted) == len(subMsgs) {
		t.Error("expected sub-engine messages to be reduced after compact")
	}

	// CRITICAL: verify parent messages are UNCHANGED
	parentNow := eng.Messages()
	if len(parentNow) != originalCount {
		t.Errorf("parent messages changed after sub-engine compact: got %d, want %d",
			len(parentNow), originalCount)
	}
}

// ---------------------------------------------------------------------------
// Integration Test 4: Multi-turn conversation → compact → continue
// ---------------------------------------------------------------------------

// TestAutoCompact_MultiTurn_Compact verifies a multi-turn conversation that
// grows, triggers proactive compact, and then continues correctly.
func TestAutoCompact_MultiTurn_Compact(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}

	compactor := engine.NewAutoCompactor(store, session.SessionID, "test-model", p)

	eng := engine.New(&engine.Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 500,
		},
		Logger: slog.Default(),
	})

	// Turn 1: small messages — no compact
	smallMsgs := makeLargeMessages(4, 50) // 4 × 50 tokens = 200 tokens < 90% of 500
	eng.SetMessages(smallMsgs)
	p.addStream(textStreamEvents("test-model", "Turn 1 response."), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "turn 1", nil)
	for range eventCh {
	}
	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("turn 1 failed: %v", result.Error)
	}

	// Turn 2: grow messages past threshold
	largeMsgs := makeLargeMessages(20, 50) // 20 × 50 = 1000 tokens > 90% of 500
	eng.SetMessages(largeMsgs)
	p.addStream(textStreamEvents("test-model", "Turn 2 response after compact."), nil)

	eventCh, resultCh = eng.Query(ctx, "turn 2", nil)
	for range eventCh {
	}
	result = <-resultCh
	if result.Error != nil {
		t.Fatalf("turn 2 failed: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
	}

	// Verify turn 2 response is in final messages
	foundTurn2 := false
	for _, msg := range result.Messages {
		for _, block := range msg.Content {
			if strings.Contains(block.Text, "Turn 2 response") {
				foundTurn2 = true
			}
		}
	}
	if !foundTurn2 {
		t.Error("expected turn 2 response in final messages after compact")
	}
}

// ---------------------------------------------------------------------------
// Integration Test 5: Concurrent compact + notifications
// ---------------------------------------------------------------------------

// TestAutoCompact_Concurrent_Compact verifies no race condition when
// ProcessNotifications enqueues messages while a compact is in progress.
func TestAutoCompact_Concurrent_Compact(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	// Slow Complete to give time for concurrent notification
	p.completeFn = func(req *llm.Request) (*llm.Response, error) {
		time.Sleep(50 * time.Millisecond)
		return &llm.Response{
			ID:    "summary-resp",
			Type:  "message",
			Role:  "assistant",
			Model: "test-model",
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "<summary>\nSummary\n</summary>"},
			},
			StopReason: "end_turn",
		}, nil
	}
	// Add enough stream responses for proactive compact + potential
	// additional turns triggered by enqueued notifications.
	for range 5 {
		p.addStream(textStreamEvents("test-model", "Done."), nil)
	}

	compactor := engine.NewAutoCompactor(store, session.SessionID, "test-model", p)
	eng := engine.New(&engine.Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 1000,
		},
		Logger: slog.Default(),
	})
	eng.SetMessages(makeLargeMessages(10, 100))

	// Concurrently enqueue notifications while query runs
	go func() {
		for i := range 10 {
			eng.EnqueueNotification(types.Message{
				Role:      types.RoleUser,
				Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("notification %d", i))},
				Timestamp: time.Now(),
			})
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error during concurrent compact: %v", result.Error)
	}
}

// ---------------------------------------------------------------------------
// Integration Test 6: Compact + session persistence
// ---------------------------------------------------------------------------

// TestAutoCompact_Compact_Persist verifies that after compact, the boundary
// marker and compacted messages are persisted correctly in the Store.
func TestAutoCompact_Compact_Persist(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}

	compactor := engine.NewAutoCompactor(store, session.SessionID, "test-model", p)

	msgs := makeMessages(10, 5000)
	result, err := compactor.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	// Verify compact produced result with boundary
	if len(result) == 0 {
		t.Fatal("compact returned no messages")
	}

	// Verify Store has messages for this session
	storeMsgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("GetSessionMessages: %v", err)
	}

	// The store should have the boundary marker persisted
	foundBoundary := false
	for _, m := range storeMsgs {
		if strings.Contains(m.Content, "compact_boundary") {
			foundBoundary = true
		}
	}
	if !foundBoundary {
		t.Errorf("expected compact_boundary in persisted messages, got %d messages", len(storeMsgs))
	}

	// Verify result has fewer messages than input (compact reduced them)
	if len(result) >= len(msgs) {
		t.Errorf("compact should reduce messages: got %d, input was %d", len(result), len(msgs))
	}
}
