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

func (m *mockCompactor) Compact(_ context.Context, msgs []types.Message) (*short.CompactResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.lastInput = msgs
	if m.result != nil {
		return &short.CompactResult{
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
	return &short.CompactResult{
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
	t.Cleanup(func() { eng.Close() })

	eng.SetMessages(makeLargeMessages(10, 100))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")
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
	t.Cleanup(func() { eng.Close() })

	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")
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
	t.Cleanup(func() { eng.Close() })

	eng.SetMessages(makeLargeMessages(10, 50))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")
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
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
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
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
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
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
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
	t.Cleanup(func() { eng.Close() })

	eng.SetMessages(makeLargeMessages(10, 50))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
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
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
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

func TestCompactor_Compact_FewMessages_CompactAll(t *testing.T) {
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
	if err != nil {
		t.Fatalf("Compact should succeed (compact everything), got: %v", err)
	}
	// Compact-all: result should have boundary + summary, no kept messages.
	if len(result.Messages) < 2 {
		t.Errorf("expected at least boundary + summary, got %d messages", len(result.Messages))
	}
	if result.BeforeMessages != 3 {
		t.Errorf("BeforeMessages should be 3, got %d", result.BeforeMessages)
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
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 40000)

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

	session, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &compactMockProvider{}
	sc := NewAutoCompactor(store, session.SessionID, "test-model", mp, 10000)

	// All messages have only thinking blocks - no extractable text.
	msgs := []types.Message{}
	for i := range 5 {
		msgs = append(msgs, types.Message{
			Role:    types.RoleAssistant,
			Content: []types.ContentBlock{{Type: "thinking", Text: fmt.Sprintf("thinking %d", i)}},
		})
		msgs = append(msgs, types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{{Type: "thinking", Text: fmt.Sprintf("response %d", i)}},
		})
	}

	_, err = sc.Compact(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error when all messages have no extractable text")
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
	sc := NewAutoCompactor(store, "test-session", "test-model", mp, 8000)

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
	// Set ContextTokens to a small value so auto-compact doesn't trigger.
	// In production, post-turn compact ensures context stays below threshold;
	// this simulates that state.
	eng.ContextTokens = 1000

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")
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

	converted := short.StoreMessageToEngine(shortMsg)
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

	converted := short.StoreMessageToEngine(shortMsg)
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

	converted := short.StoreMessageToEngine(shortMsg)
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
	got := short.StoreMessageToEngine(nil)
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

func TestExtractTextFromShortContent_Empty(t *testing.T) {
	got := extractTextFromShortContent("")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractTextFromShortContent_ToolUseEmptyName(t *testing.T) {
	content := `[{"type":"tool_use","name":""},{"type":"text","text":"ok"}]`
	got := extractTextFromShortContent(content)
	if strings.Contains(got, "[]") {
		t.Error("empty name should not produce [] brackets")
	}
	if !strings.Contains(got, "ok") {
		t.Errorf("expected 'ok' in output, got %q", got)
	}
}

func TestExtractTextFromShortContent_ToolResultNonStringContent(t *testing.T) {
	content := `[{"type":"tool_result","content":{"nested":true}}]`
	got := extractTextFromShortContent(content)
	if got != "" {
		t.Errorf("non-string content should be skipped, got %q", got)
	}
}

func TestExtractTextFromShortContent_ToolResultEmptyContent(t *testing.T) {
	content := `[{"type":"tool_result","content":null}]`
	got := extractTextFromShortContent(content)
	if got != "" {
		t.Errorf("null content should produce empty, got %q", got)
	}
}

func TestExtractTextFromShortContent_MultipleTextBlocks(t *testing.T) {
	content := `[{"type":"text","text":"hello"},{"type":"text","text":"world"}]`
	got := extractTextFromShortContent(content)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

// TestBuildResultMessages_RemovesOrphanedToolResults verifies that after compact,
// tool_result blocks whose tool_use was removed are stripped.
// This reproduces the production"tool result's tool id not found".
func TestBuildResultMessages_RemovesOrphanedToolResults(t *testing.T) {
	t.Parallel()

	compactor := &AutoCompactor{}

	// Simulate compact result where head had the tool_use but tail has the tool_result.
	// The kept messages have:
	//   - user msg with tool_result (tool_use_id="orphan_1") — no matching tool_use
	//   - assistant msg with tool_use (id="kept_1")
	//   - user msg with tool_result (tool_use_id="kept_1") — has matching tool_use
	kept := []*short.TranscriptMessage{
		{
			UUID:    "u1",
			Type:    "user",
			Content: `[{"type":"tool_result","tool_use_id":"orphan_1","content":"\"old data\""}]`,
		},
		{
			UUID:    "a1",
			Type:    "assistant",
			Content: `[{"type":"tool_use","id":"kept_1","name":"Bash","input":"{\"command\":\"ls\"}"}]`,
		},
		{
			UUID:    "u2",
			Type:    "user",
			Content: `[{"type":"tool_result","tool_use_id":"kept_1","content":"\"kept data\""}]`,
		},
	}

	pcr := &short.CompactResult{
		BoundaryMarker: &short.TranscriptMessage{
			UUID:    "boundary",
			Type:    "user",
			Content: `[{"type":"text","text":"Previous conversation compacted"}]`,
		},
		MessagesToKeep: kept,
	}

	result := compactor.buildResultMessages(pcr, "summary text")

	// Collect all tool_use IDs in result
	toolUseIDs := make(map[string]bool)
	for _, msg := range result {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolUse {
				toolUseIDs[block.ID] = true
			}
		}
	}

	// Check: no orphaned tool_results
	for _, msg := range result {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult {
				if !toolUseIDs[block.ToolUseID] {
					t.Errorf("orphaned tool_result: tool_use_id=%q has no matching tool_use block", block.ToolUseID)
				}
			}
		}
	}

	// Check: valid tool_result is preserved
	foundKept := false
	for _, msg := range result {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult && block.ToolUseID == "kept_1" {
				foundKept = true
			}
		}
	}
	if !foundKept {
		t.Error("tool_result for kept_1 should be preserved")
	}
}

// ---------------------------------------------------------------------------
// findKeepFrom: [0, min(targetKeepTokens, maxKeepMessages)] design
//
// Invariant: tail = messages[keepFrom:] is in range [0, min(8K tokens, 8 msgs)].
// Walk backwards from tail, stop when EITHER constraint is hit.
// If nothing fits, tail = 0 (compact everything into summary).
// ---------------------------------------------------------------------------

// newFindKeepFromHelper creates a compactor and returns keepFrom for the given messages.
func newFindKeepFromHelper(t *testing.T, contextWindow int, msgs []types.Message) int {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	sc := NewAutoCompactor(store, "test-session", "test-model", &compactMockProvider{}, contextWindow)
	shortMsgs, err := short.EngineMessagesToStore(msgs)
	if err != nil {
		return 0
	}
	return sc.findKeepFrom(shortMsgs)
}

func TestFindKeepFrom_SingleHugeMessage_TailZero(t *testing.T) {
	t.Parallel()
	// contextWindow=40K → target=8K. One message of 40K chars (~10K tokens) > 8K.
	// Nothing fits in tail budget → keepFrom = len (compact everything).
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("x", 40000)),
		}},
	}
	keepFrom := newFindKeepFromHelper(t, 40000, msgs)
	if keepFrom != len(msgs) {
		t.Errorf("single huge msg: keepFrom should be len(%d), got %d", len(msgs), keepFrom)
	}
}

func TestFindKeepFrom_LastMessageHuge_CompactEverything(t *testing.T) {
	t.Parallel()
	// 10 messages: 9 small + 1 huge last. Last alone exceeds 8K budget.
	// Pure token-based: nothing fits in tail → keepFrom = len (compact everything).
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg 0")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("msg 1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg 2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("msg 3")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg 4")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("msg 5")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg 6")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("msg 7")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg 8")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("x", 40000)), // ~10K tokens > 8K
		}},
	}
	keepFrom := newFindKeepFromHelper(t, 40000, msgs)
	// Huge last message exceeds 8K → nothing fits in tail → keepFrom = len.
	// This means Compact will summarize everything (tail=0).
	if keepFrom != len(msgs) {
		t.Errorf("should return len(%d) when nothing fits in budget, got %d", len(msgs), keepFrom)
	}
}

func TestFindKeepFrom_AllHuge_CompactEverything(t *testing.T) {
	t.Parallel()
	// 8 messages, each ~5K tokens (20K chars). First from tail exceeds 8K budget.
	// Nothing fits in tail → keepFrom = len (compact everything, tail=0).
	msgs := makeLargeMessages(8, 5000)
	keepFrom := newFindKeepFromHelper(t, 40000, msgs)
	// First message from tail (5K tokens) fits in 8K budget, second doesn't (10K > 8K).
	// So tail = 1 message, keepFrom = len - 1 = 7.
	tail := len(msgs) - keepFrom
	if tail != 1 {
		t.Errorf("tail should be 1 (first fits, second overflows), got tail=%d keepFrom=%d", tail, keepFrom)
	}
}

func TestFindKeepFrom_SmallMessages_AllFit_NothingToCompact(t *testing.T) {
	t.Parallel()
	// 20 small messages, each ~100 tokens. Total = ~2K tokens << 8K budget.
	// All fit → return len (nothing to compact).
	msgs := makeLargeMessages(20, 100)
	keepFrom := newFindKeepFromHelper(t, 40000, msgs)
	if keepFrom != len(msgs) {
		t.Errorf("all fit: should return len(%d), got %d", len(msgs), keepFrom)
	}
}

func TestFindKeepFrom_Mixed_StopsAtTokenBudget(t *testing.T) {
	t.Parallel()
	// Messages with varying sizes. Budget = 8K tokens, maxKeep = 8 messages.
	// Tail should stop when token budget is hit, even if count < 8.
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old 1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("old 2")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old 3")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("old 4")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old 5")}},
		// Last 3 messages: each ~3K tokens (12K chars). Total ~9K > 8K budget.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("a", 12000)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("b", 12000)),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("c", 12000)),
		}},
	}
	keepFrom := newFindKeepFromHelper(t, 40000, msgs)
	tail := len(msgs) - keepFrom
	// Last 2 messages: 6K tokens < 8K. Adding 3rd: 9K > 8K. So tail = 2.
	if tail != 2 {
		t.Errorf("tail should be 2 (budget hit), got %d (keepFrom=%d)", tail, keepFrom)
	}
}

func TestFindKeepFrom_Empty_ReturnsZero(t *testing.T) {
	t.Parallel()
	keepFrom := newFindKeepFromHelper(t, 40000, nil)
	if keepFrom != 0 {
		t.Errorf("empty messages should return 0, got %d", keepFrom)
	}
}

func TestFindKeepFrom_AllFit_NothingToCompact(t *testing.T) {
	t.Parallel()
	// 3 small messages, all fit in 8K budget and under 8 count.
	// Nothing to compact → return len.
	msgs := makeLargeMessages(3, 100)
	keepFrom := newFindKeepFromHelper(t, 40000, msgs)
	if keepFrom != len(msgs) {
		t.Errorf("all fit: should return len(%d), got %d", len(msgs), keepFrom)
	}
}

// ---------------------------------------------------------------------------
// Compact round-trip must preserve metadata (MessageType, Flags)
//
// ---------------------------------------------------------------------------

func TestEngineToShort_PreservesMetadata(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: now},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: now},
		// Queued message with MessageTypeAttachment
		{Role: types.RoleUser, MessageType: types.MessageTypeAttachment, Content: []types.ContentBlock{types.NewTextBlock("123")}, Timestamp: now},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("ok")}, Timestamp: now},
		// Skill message with FlagMeta
		{Role: types.RoleUser, Flags: types.FlagMeta, Content: []types.ContentBlock{types.NewTextBlock("<command-message>test</command-message>")}, Timestamp: now},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: now},
	}

	// Convert engine → short → back to engine (simulates compact round-trip)
	shortMsgs, err := short.EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}

	// Verify short messages have metadata
	for i, sm := range shortMsgs {
		msgType := msgs[i].MessageType
		flags := msgs[i].Flags
		if msgType != "" || flags != 0 {
			if sm.Metadata == "" {
				t.Errorf("shortMsg[%d] has empty metadata, but original had MessageType=%q Flags=%v", i, msgType, flags)
			}
		}
	}

	// Convert back to engine messages
	for i, sm := range shortMsgs {
		restored := short.StoreMessageToEngine(sm)
		orig := msgs[i]

		if orig.MessageType != "" && restored.MessageType != orig.MessageType {
			t.Errorf("msg[%d]: MessageType lost: got %q, want %q", i, restored.MessageType, orig.MessageType)
		}
		if orig.Flags != 0 && restored.Flags != orig.Flags {
			t.Errorf("msg[%d]: Flags lost: got %v, want %v", i, restored.Flags, orig.Flags)
		}
	}
}

func TestEngineToShort_PreservesAttachmentFields(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Parallel()
	// Specific test: MessageTypeAttachment message survives engineToShort round-trip
	msg := types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock("queued text")},
		Timestamp:   now,
	}

	shortMsgs, err := short.EngineMessagesToStore([]types.Message{msg})
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if len(shortMsgs) != 1 {
		t.Fatalf("expected 1 short message, got %d", len(shortMsgs))
	}

	// The short message MUST have metadata containing message_type
	sm := shortMsgs[0]
	if sm.Metadata == "" {
		t.Fatal("metadata lost after EngineMessagesToStore: MessageTypeAttachment not preserved")
	}

	restored := short.StoreMessageToEngine(sm)
	if restored.MessageType != types.MessageTypeAttachment {
		t.Errorf("MessageType lost after round-trip: got %q, want %q", restored.MessageType, types.MessageTypeAttachment)
	}
}

func TestEngineToShort_PreservesFlagMeta(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role:    types.RoleUser,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("<command-message>skill</command-message>")},
	}

	shortMsgs, err := short.EngineMessagesToStore([]types.Message{msg})
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	sm := shortMsgs[0]
	if sm.Metadata == "" {
		t.Fatal("metadata lost after EngineMessagesToStore: FlagMeta not preserved")
	}

	restored := short.StoreMessageToEngine(sm)
	if !restored.HasFlag(types.FlagMeta) {
		t.Errorf("FlagMeta lost after round-trip: Flags=%v, want FlagMeta(%v)", restored.Flags, types.FlagMeta)
	}
}
