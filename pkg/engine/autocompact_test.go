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
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Mock Compactor (for engine-level auto-compact tests)
// ---------------------------------------------------------------------------

// mockCompactor tracks compact calls for testing.
type mockCompactor struct {
	mu        sync.Mutex
	callCount int
	lastInput []types.Message
	result    []types.Message
	err       error
}

func (m *mockCompactor) Compact(_ context.Context, msgs []types.Message) (*CompactResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.lastInput = msgs
	if m.result != nil {
		return &CompactResult{
			BeforeTokens: len(msgs) * 100,
			AfterTokens:  len(m.result) * 100,
			Messages:     m.result,
		}, m.err
	}
	// Default: return a minimal user+assistant pair (valid API sequence)
	msgs2 := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("[Previous conversation compacted]")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("[Summary acknowledged]")}},
	}
	return &CompactResult{
		BeforeTokens: len(msgs) * 100,
		AfterTokens:  len(msgs2) * 100,
		Messages:     msgs2,
	}, m.err
}

func (m *mockCompactor) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// ---------------------------------------------------------------------------
// Mock provider for AutoCompactor LLM calls
// ---------------------------------------------------------------------------

type compactMockProvider struct {
	mu               sync.Mutex
	compactCallCount int
	compactInput     []string
	compactErr       error
}

func (m *compactMockProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 1)
	ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "ok"}}
	close(ch)
	return ch, nil
}

func (m *compactMockProvider) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compactCallCount++

	if len(req.Messages) >= 2 {
		lastMsg := req.Messages[len(req.Messages)-1]
		m.compactInput = append(m.compactInput, extractTextFromBlocks(lastMsg.Content))
	}

	if m.compactErr != nil {
		return nil, m.compactErr
	}

	summaryIdx := m.compactCallCount - 1
	summary := "Test summary of previous conversation"
	if summaryIdx < len(m.compactInput) {
		summary = m.compactInput[summaryIdx]
	}

	return &llm.Response{
		ID:    "compact-" + string(rune('0'+m.compactCallCount)),
		Type:  "message",
		Role:  "assistant",
		Model: "compact-model",
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "<summary>\n" + summary + "\n</summary>"},
		},
		StopReason: "end_turn",
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func extractTextFromBlocks(blocks []types.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != "" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// makeLargeMessages creates n messages, each with ~tokensPerMsg estimated tokens.
// Uses the 4 chars/token heuristic to match the engine.currentInputTokens().
func makeLargeMessages(n, tokensPerMsg int) []types.Message {
	text := strings.Repeat("x", tokensPerMsg*4)
	msgs := make([]types.Message, n)
	for i := range msgs {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		msgs[i] = types.Message{
			Role:    role,
			Content: []types.ContentBlock{types.NewTextBlock(text)},
		}
	}
	return msgs
}

// makeMessages creates n messages with ~charCount chars each.
// The last message has unique content for round-trip verification.
func makeMessages(n, charCount int) []types.Message {
	msgs := make([]types.Message, n)
	text := strings.Repeat("x", charCount)
	for i := range msgs {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		content := types.ContentBlock{Type: types.ContentTypeText, Text: text}
		if i == n-1 {
			content.Text = "recent-message-" + string(rune('0'+i))
		}
		msgs[i] = types.Message{
			Role:      role,
			Content:   []types.ContentBlock{content},
			Timestamp: time.Now(), // REAL-TIME: needed for message timestamp in test
		}
	}
	return msgs
}

// ---------------------------------------------------------------------------
// Proactive auto-compact tests
// ---------------------------------------------------------------------------

// TestAutoCompact_Proactive_TriggersWhenOverThreshold verifies proactive compact
// fires when estimated tokens exceed the configured threshold percentage.
// TS align: autoCompact.ts:shouldAutoCompact()
func TestAutoCompact_Proactive_TriggersWhenOverThreshold(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "After compact."), nil)

	mc := &mockCompactor{}

	eng := New(&Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 1000,
		},
		Logger: slog.Default(),
	})

	eng.SetMessages(makeLargeMessages(10, 100))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if mc.CallCount() == 0 {
		t.Error("proactive auto-compact should have been triggered (tokens exceed 90% threshold)")
	}
}

func TestAutoCompact_Proactive_DoesNotTriggerWhenUnderThreshold(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Normal response."), nil)

	mc := &mockCompactor{}

	eng := New(&Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 100000,
		},
		Logger: slog.Default(),
	})

	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if mc.CallCount() != 0 {
		t.Errorf("proactive auto-compact should NOT have been triggered, got %d calls", mc.CallCount())
	}
}

func TestAutoCompact_Proactive_CompactedMessagesReplaced(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "After compact."), nil)

	mc := &mockCompactor{
		result: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("[summary of conversation]")}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("[acknowledged]")}},
		},
	}

	eng := New(&Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 100,
		},
		Logger: slog.Default(),
	})

	eng.SetMessages(makeLargeMessages(10, 50))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	foundSummary := false
	for _, msg := range result.Messages {
		for _, block := range msg.Content {
			if block.Text == "[summary of conversation]" {
				foundSummary = true
			}
		}
	}
	if !foundSummary {
		t.Error("expected compact summary in final messages after proactive compact")
	}
}

// ---------------------------------------------------------------------------
// Reactive auto-compact tests
// ---------------------------------------------------------------------------

// TestAutoCompact_Reactive_TriggersOnContextOverflow verifies reactive compact
// fires when the API returns a prompt_too_long error.
// TS align: query.ts:1119-1175 — reactiveCompact.tryReactiveCompact()
func TestAutoCompact_Reactive_TriggersOnContextOverflow(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(nil, &llm.APIError{
		Status:    400,
		ErrorCode: "prompt_too_long",
		Message:   "input length and max_tokens exceed context limit",
	})
	mp.addResponse(textStreamEvents("test-model", "Recovered after compact."), nil)

	mc := &mockCompactor{}

	eng := New(&Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 100000,
		},
		Logger: slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("expected recovery after reactive compact, got error: %v", result.Error)
	}


	if mc.CallCount() == 0 {
		t.Error("reactive auto-compact should have been triggered on prompt_too_long")
	}
}

// TestAutoCompact_Reactive_NoSecondRetry verifies no infinite retry loop.
func TestAutoCompact_Reactive_NoSecondRetry(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(nil, &llm.APIError{
		Status:    400,
		ErrorCode: "prompt_too_long",
		Message:   "still too long",
	})
	mp.addResponse(nil, &llm.APIError{
		Status:    400,
		ErrorCode: "prompt_too_long",
		Message:   "still too long after compact",
	})

	mc := &mockCompactor{}

	eng := New(&Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 100000,
		},
		Logger: slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error == nil {
		t.Fatal("expected error when reactive compact retry also fails")
	}
	if !strings.Contains(result.Error.Error(), "too long") {
		t.Errorf("error should mention too long, got: %v", result.Error)
	}


	if mc.CallCount() != 1 {
		t.Errorf("expected 1 compact call (reactive, no second retry), got %d", mc.CallCount())
	}
}

func TestAutoCompact_Reactive_NoCompactor_ReturnsError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(nil, &llm.APIError{
		Status:    400,
		ErrorCode: "prompt_too_long",
		Message:   "too long",
	})

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error == nil {
		t.Fatal("expected error when no compactor available")
	}
	if !strings.Contains(result.Error.Error(), "too long") {
		t.Errorf("error should mention too long, got: %v", result.Error)
	}

}

// ---------------------------------------------------------------------------
// Circuit breaker
// ---------------------------------------------------------------------------

// TestAutoCompact_CircuitBreaker_StopsAfterFailures verifies that after
// MaxConsecutiveFailures, proactive compact stops being attempted.
// TS align: autoCompact.ts:241-290
func TestAutoCompact_CircuitBreaker_StopsAfterFailures(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	for range 5 {
		mp.addResponse(textStreamEvents("test-model", "ok"), nil)
	}

	mc := &mockCompactor{err: errors.New("compact failed")}

	eng := New(&Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: AutoCompactConfig{
			ContextWindow:          100,
			MaxConsecutiveFailures: 2,
		},
		Logger: slog.Default(),
	})

	eng.SetMessages(makeLargeMessages(10, 50))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	calls := mc.CallCount()
	if calls == 0 {
		t.Error("expected at least one compact attempt before circuit breaker")
	}
	if calls > 3 {
		t.Errorf("circuit breaker should limit compact attempts after %d failures, got %d calls", 2, calls)
	}
}

// ---------------------------------------------------------------------------
// No compactor = graceful degradation
// ---------------------------------------------------------------------------

func TestAutoCompact_NoCompactor_NormalQuery(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello!"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

}

// ---------------------------------------------------------------------------
// AutoCompactor struct tests
// ---------------------------------------------------------------------------

func TestCompactor_Compact_EmptyMessages(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mp := &compactMockProvider{}
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 200000)

	_, err = sc.Compact(context.Background(), []types.Message{})
	if err == nil {
		t.Error("Compact with empty messages should return error")
	}
}

func TestCompactor_Compact_TooFewMessages(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mp := &compactMockProvider{}
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 200000)

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("continue")}},
	}

	result, err := sc.Compact(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error for too few messages (<=minKeep=4)")
	}
	if !strings.Contains(err.Error(), "nothing to compact") {
		t.Errorf("error should mention 'nothing to compact', got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
}

func TestCompactor_Compact_SummarizesOldMessages(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mp := &compactMockProvider{}
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 200000)

	msgs := makeMessages(10, 5000)

	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if mp.compactCallCount == 0 {
		t.Error("expected LLM call for summary generation")
	}

	foundBoundary := false
	for _, msg := range result.Messages {
		for _, block := range msg.Content {
			var content struct {
				Subtype string `json:"subtype"`
			}
			if json.Unmarshal([]byte(block.Text), &content) == nil && content.Subtype == "compact_boundary" {
				foundBoundary = true
			}
		}
	}
	if !foundBoundary {
		t.Error("expected compact_boundary subtype in result")
	}
}

// TestCompactor_Compact_NothingToCompact_ReturnsError verifies that when
// findKeepFrom determines all messages should be kept (nothing to compact),
// Compact returns an error instead of a fake "success" with zero delta.
// This aligns with TS where autocompact returns wasCompacted:false for no-ops.
func TestCompactor_Compact_NothingToCompact_ReturnsError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mp := &compactMockProvider{}
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 200000)

	// 3 messages ≤ minKeep(4) → findKeepFrom returns len(messages) → "nothing to compact"
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("continue")}},
	}

	_, err = sc.Compact(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error when nothing to compact (≤4 messages)")
	}
	if !strings.Contains(err.Error(), "nothing to compact") {
		t.Errorf("error should mention 'nothing to compact', got: %v", err)
	}
	if mp.compactCallCount != 0 {
		t.Errorf("no LLM call expected, got %d", mp.compactCallCount)
	}
}

// TestCompactor_Compact_EmptyHeadText_ReturnsError verifies that when
// head messages have no extractable text, Compact returns an error
// instead of a no-op "success" with empty summary.
func TestCompactor_Compact_EmptyHeadText_ReturnsError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Create a session so RecordCompact can succeed
	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &compactMockProvider{}
	// Small contextWindow → lower keep target
	sc := NewAutoCompactor(store, session.SessionID, "test-model", mp, 10000)

	// Build messages where head (first ~8) have only non-text blocks that
	// extractTextFromShortContent skips. Use thinking blocks (type "thinking")
	// which are ignored by the extractor.
	msgs := []types.Message{}

	// Head: 8 messages with thinking blocks (not extracted by extractTextFromShortContent)
	for i := range 4 {
		msgs = append(msgs, types.Message{
			Role:    types.RoleAssistant,
			Content: []types.ContentBlock{{Type: "thinking", Text: fmt.Sprintf("thinking %d", i)}},
		})
		msgs = append(msgs, types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{{Type: "thinking", Text: fmt.Sprintf("response %d", i)}},
		})
	}
	// Tail: 2 messages with real text (kept by findKeepFrom)
	msgs = append(msgs, types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 5000))},
	})
	msgs = append(msgs, types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("continue")},
	})

	_, err = sc.Compact(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error when head messages have no extractable text")
	}
	if !strings.Contains(err.Error(), "no extractable text") {
		t.Errorf("error should mention 'no extractable text', got: %v", err)
	}
	if mp.compactCallCount != 0 {
		t.Errorf("no LLM call expected for empty head text, got %d", mp.compactCallCount)
	}
}

func TestCompactor_Compact_LLMErrors_ReturnsError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mp := &compactMockProvider{compactErr: errors.New("LLM unavailable")}
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 200000)

	msgs := makeMessages(10, 5000)
	result, err := sc.Compact(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error when LLM call fails")
	}
	if !strings.Contains(err.Error(), "summarize failed") {
		t.Errorf("error should mention 'summarize failed', got: %v", err)
	}
	if !strings.Contains(err.Error(), "LLM unavailable") {
		t.Errorf("error should wrap LLM error, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
	if mp.compactCallCount == 0 {
		t.Error("expected LLM call attempt")
	}
}

func TestCompactor_Compact_PreservesRecentMessages(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mp := &compactMockProvider{}
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 200000)

	msgs := makeMessages(10, 1000)

	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	foundRecentContent := false
	for _, msg := range result.Messages {
		for _, block := range msg.Content {
			if block.Text == "recent-message-9" {
				foundRecentContent = true
			}
		}
	}
	if !foundRecentContent {
		t.Error("expected recent messages to be preserved in compact result")
	}
}

// ---------------------------------------------------------------------------
// Engine integration with AutoCompactor
// ---------------------------------------------------------------------------

func TestCompactor_EngineIntegration_ReactiveCompact(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mp := &mockProvider{}
	mp.addResponse(nil, &llm.APIError{
		Status:    400,
		ErrorCode: "prompt_too_long",
		Message:   "context too long",
	})
	mp.addResponse(textStreamEvents("test-model", "Success after compact"), nil)

	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 200000)
	eng := New(&Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: sc,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 100000,
		},
	})

	largeMsgs := makeLargeMessages(20, 5000)
	eng.SetMessages(largeMsgs)
	// Set ContextTokens to a small value so blocking limit doesn't refuse
	// the API call. In production, post-turn compact ensures context stays
	// below threshold; this simulates that state.
	eng.ContextTokens = 1000

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", nil)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result.Error != nil {
		t.Errorf("expected success, got: %v", result.Error)
	}
}

// ---------------------------------------------------------------------------
// ShortMessageToEngine round-trip tests
// ---------------------------------------------------------------------------

func TestShortMessageToEngine_ContentRoundTrip(t *testing.T) {
	t.Parallel()

	original := types.Message{
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.NewTextBlock("hello world")},
		Timestamp: time.Now(), // REAL-TIME: needed for message timestamp in test
	}

	contentBytes, _ := json.Marshal(original.Content)
	shortMsg := &short.TranscriptMessage{
		UUID:      "test-uuid",
		Type:      string(original.Role),
		Content:   string(contentBytes),
		CreatedAt: original.Timestamp,
	}

	converted := ShortMessageToEngine(shortMsg)
	if converted.Role != original.Role {
		t.Errorf("Role round-trip: got %s, want %s", converted.Role, original.Role)
	}
	if len(converted.Content) != 1 {
		t.Fatalf("Content blocks: got %d, want 1", len(converted.Content))
	}
	if converted.Content[0].Text != "hello world" {
		t.Errorf("Content text: got %q, want %q", converted.Content[0].Text, "hello world")
	}
}

func TestShortMessageToEngine_NonJSONContent(t *testing.T) {
	t.Parallel()

	shortMsg := &short.TranscriptMessage{
		Type:      "user",
		Content:   "plain text content",
		CreatedAt: time.Now(), // REAL-TIME: needed for CreatedAt field in test
	}

	converted := ShortMessageToEngine(shortMsg)
	if converted.Role != types.RoleUser {
		t.Errorf("Role: got %s, want user", converted.Role)
	}
	if len(converted.Content) != 1 {
		t.Fatalf("Content blocks: got %d, want 1", len(converted.Content))
	}
	if converted.Content[0].Text != "plain text content" {
		t.Errorf("Text: got %q, want %q", converted.Content[0].Text, "plain text content")
	}
}

func TestShortMessageToEngine_ToolBlocks(t *testing.T) {
	t.Parallel()

	blocks := []short.ContentBlock{
		{Type: "text", Text: "result"},
		{Type: "tool_use", Name: "Read", ID: "tu_1", Input: json.RawMessage(`{"path":"/a.go"}`)},
	}
	contentBytes, _ := json.Marshal(blocks)
	shortMsg := &short.TranscriptMessage{
		Type:      "assistant",
		Content:   string(contentBytes),
		CreatedAt: time.Now(), // REAL-TIME: needed for CreatedAt field in test
	}

	converted := ShortMessageToEngine(shortMsg)
	if converted.Role != types.RoleAssistant {
		t.Errorf("Role: got %s, want assistant", converted.Role)
	}
	if len(converted.Content) != 2 {
		t.Errorf("Content blocks: got %d, want 2", len(converted.Content))
	}
	if converted.Content[1].Name != "Read" {
		t.Errorf("Tool name: got %s, want Read", converted.Content[1].Name)
	}
}

func TestAutoCompactor_SummarizeMessages_Empty(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	db, _ := short.NewStore(filepath.Join(tmpDir, "test.db"))
	defer db.Close()
	sc := NewAutoCompactor(db, "sess", "model", &compactMockProvider{}, 1000)
	got, err := sc.summarizeMessages(context.Background(), nil)
	if err != nil {
		t.Fatalf("summarizeMessages(nil) error: %v", err)
	}
	if got != "" {
		t.Errorf("summarizeMessages(nil) = %q, want empty", got)
	}
}

func TestAutoCompactor_SummarizeMessages_NoText(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	db, _ := short.NewStore(filepath.Join(tmpDir, "test.db"))
	defer db.Close()
	sc := NewAutoCompactor(db, "sess", "model", &compactMockProvider{}, 1000)
	// Message with no text content (empty JSON array)
	msgs := []*short.TranscriptMessage{{Type: "user", Content: "[]"}}
	got, err := sc.summarizeMessages(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error for no extractable text in head messages")
	}
	if !strings.Contains(err.Error(), "no extractable text") {
		t.Errorf("error should mention 'no extractable text', got: %v", err)
	}
	if got != "" {
		t.Errorf("summarizeMessages(no text) = %q, want empty", got)
	}
}

func TestAutoCompactor_SummarizeMessages_LLMError(t *testing.T) {
	tmpDir := t.TempDir()
	db, _ := short.NewStore(filepath.Join(tmpDir, "test.db"))
	defer db.Close()
	mp := &compactMockProvider{compactErr: fmt.Errorf("LLM unavailable")}
	c := NewAutoCompactor(db, "sess", "model", mp, 1000)

	msgs := []*short.TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}
	_, err := c.summarizeMessages(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
	if !strings.Contains(err.Error(), "summarize LLM call") {
		t.Errorf("expected summarize error, got: %v", err)
	}
}

func TestAutoCompactor_SummarizeMessages_EmptyResponse(t *testing.T) {
	// Provider returns a response with no text content
	tmpDir2 := t.TempDir()
	db, _ := short.NewStore(filepath.Join(tmpDir2, "test.db"))
	defer db.Close()
	mp := &emptyResponseProvider{}
	c := NewAutoCompactor(db, "sess", "model", mp, 1000)

	msgs := []*short.TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}
	_, err := c.summarizeMessages(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error for empty response")
	}
	if !strings.Contains(err.Error(), "no text in LLM response") {
		t.Errorf("expected 'no text' error, got: %v", err)
	}
}

type emptyResponseProvider struct{}

func (e *emptyResponseProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent)
	close(ch)
	return ch, nil
}
func (e *emptyResponseProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{ID: "empty", Content: []types.ContentBlock{}}, nil
}

func TestAutoCompactor_BuildResultMessages_NoBoundary(t *testing.T) {
	tmpDir3 := t.TempDir()
	db, _ := short.NewStore(filepath.Join(tmpDir3, "test.db"))
	defer db.Close()
	mp := &compactMockProvider{}
	c := NewAutoCompactor(db, "sess", "model", mp, 50000)

	pcr := &short.CompactResult{
		BoundaryMarker: nil,
		MessagesToKeep: []*short.TranscriptMessage{
			{Type: "user", Content: `[{"type":"text","text":"kept"}]`},
		},
	}
	msgs := c.buildResultMessages(pcr, "")
	if len(msgs) < 1 {
		t.Fatal("expected at least boundary message")
	}
	if !strings.Contains(msgs[0].Content[0].Text, "compacted") {
		t.Errorf("expected default boundary text, got %q", msgs[0].Content[0].Text)
	}
}

func TestAutoCompactor_BuildResultMessages_WithSummary(t *testing.T) {
	tmpDir4 := t.TempDir()
	db, _ := short.NewStore(filepath.Join(tmpDir4, "test.db"))
	defer db.Close()
	mp := &compactMockProvider{}
	c := NewAutoCompactor(db, "sess", "model", mp, 50000)

	pcr := &short.CompactResult{
		BoundaryMarker: &short.TranscriptMessage{
			Content: `[{"type":"text","text":"boundary text"}]`,
		},
		MessagesToKeep: []*short.TranscriptMessage{
			{Type: "user", Content: `[{"type":"text","text":"kept"}]`},
		},
	}
	msgs := c.buildResultMessages(pcr, "test summary")
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (boundary+summary+kept), got %d", len(msgs))
	}
}

func TestShortMessageToEngine_Nil(t *testing.T) {
	got := ShortMessageToEngine(nil)
	if got.Role != "" || len(got.Content) != 0 {
		t.Errorf("expected empty message for nil, got %+v", got)
	}
}

func TestExtractTextFromShortContent_ToolUse(t *testing.T) {
	content := `[{"type":"tool_use","name":"Bash"},{"type":"text","text":"output"}]`
	got := extractTextFromShortContent(content)
	if !strings.Contains(got, "[Bash]") {
		t.Errorf("expected [Bash] in output, got %q", got)
	}
	if !strings.Contains(got, "output") {
		t.Errorf("expected 'output', got %q", got)
	}
}

func TestExtractTextFromShortContent_ToolResult(t *testing.T) {
	content := `[{"type":"tool_result","content":"\"result text\""}]`
	got := extractTextFromShortContent(content)
	// Content is a JSON string "\"result text\"" which unquotes to "result text" (with quotes)
	if got != `"result text"` {
		t.Errorf("expected '\"result text\"', got %q", got)
	}
}

func TestExtractTextFromShortContent_PlainText(t *testing.T) {
	got := extractTextFromShortContent("just plain text")
	if got != "just plain text" {
		t.Errorf("expected 'just plain text', got %q", got)
	}
}

