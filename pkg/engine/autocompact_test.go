package engine_test

import (
	"context"
	"encoding/json"
	"errors"
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

func (m *mockCompactor) Compact(_ context.Context, msgs []types.Message) ([]types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.lastInput = msgs
	if m.result != nil {
		return m.result, m.err
	}
	// Default: return a minimal user+assistant pair (valid API sequence)
	return []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("[Previous conversation compacted]")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("[Summary acknowledged]")}},
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
	compactInput    []string
	compactErr      error
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
			Timestamp: time.Now(),
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

	eng := engine.New(&engine.Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 1000,
		},
		Logger: slog.Default(),
	})

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

	if mc.CallCount() == 0 {
		t.Error("proactive auto-compact should have been triggered (tokens exceed 90% threshold)")
	}
}

func TestAutoCompact_Proactive_DoesNotTriggerWhenUnderThreshold(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Normal response."), nil)

	mc := &mockCompactor{}

	eng := engine.New(&engine.Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
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

	eventCh, resultCh := eng.Query(ctx, "continue", nil)
	for range eventCh {
	}

	result := <-resultCh
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

	eng := engine.New(&engine.Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 100,
		},
		Logger: slog.Default(),
	})

	eng.SetMessages(makeLargeMessages(10, 50))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "continue", nil)
	for range eventCh {
	}

	result := <-resultCh
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

	eng := engine.New(&engine.Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 100000,
		},
		Logger: slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("expected recovery after reactive compact, got error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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

	eng := engine.New(&engine.Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 100000,
		},
		Logger: slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Fatal("expected error when reactive compact retry also fails")
	}
	if result.Terminal != types.TerminalPromptTooLong {
		t.Errorf("expected TerminalPromptTooLong, got %s", result.Terminal)
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error == nil {
		t.Fatal("expected error when no compactor available")
	}
	if result.Terminal != types.TerminalPromptTooLong {
		t.Errorf("expected TerminalPromptTooLong, got %s", result.Terminal)
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
	for i := 0; i < 5; i++ {
		mp.addResponse(textStreamEvents("test-model", "ok"), nil)
	}

	mc := &mockCompactor{err: errors.New("compact failed")}

	eng := engine.New(&engine.Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: mc,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:              0.9,
			ContextWindow:          100,
			MaxConsecutiveFailures: 2,
		},
		Logger: slog.Default(),
	})

	eng.SetMessages(makeLargeMessages(10, 50))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
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

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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
	defer func() { _ = store.Close() }()

	mp := &compactMockProvider{}
	sc := engine.NewAutoCompactor(store, "test-session", "test-model", mp)

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
	defer func() { _ = store.Close() }()

	mp := &compactMockProvider{}
	sc := engine.NewAutoCompactor(store, "test-session", "test-model", mp)

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("continue")}},
	}

	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 messages (no compact needed), got %d", len(result))
	}
	if mp.compactCallCount != 0 {
		t.Errorf("no LLM call expected for <4 messages, got %d", mp.compactCallCount)
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
	defer func() { _ = store.Close() }()

	mp := &compactMockProvider{}
	sc := engine.NewAutoCompactor(store, "test-session", "test-model", mp)

	msgs := makeMessages(10, 5000)

	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if mp.compactCallCount == 0 {
		t.Error("expected LLM call for summary generation")
	}

	foundBoundary := false
	for _, msg := range result {
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

func TestCompactor_Compact_LLMErrors_FallsBack(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	mp := &compactMockProvider{compactErr: errors.New("LLM unavailable")}
	sc := engine.NewAutoCompactor(store, "test-session", "test-model", mp)

	msgs := makeMessages(10, 5000)
	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact should not return error on LLM failure (graceful fallback): %v", err)
	}

	if len(result) == 0 {
		t.Error("expected at least boundary message after fallback")
	}
	if mp.compactCallCount == 0 {
		t.Error("expected LLM call attempt even on failure")
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
	defer func() { _ = store.Close() }()

	mp := &compactMockProvider{}
	sc := engine.NewAutoCompactor(store, "test-session", "test-model", mp)

	msgs := makeMessages(10, 1000)

	result, err := sc.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	foundRecentContent := false
	for _, msg := range result {
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

func TestCompactor_EngineIntegration_ProactiveCompact(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	mp := &mockProvider{}
	mp.addResponse(nil, &llm.APIError{
		Status:    400,
		ErrorCode: "prompt_too_long",
		Message:   "context too long",
	})
	mp.addResponse(textStreamEvents("test-model", "Success after compact"), nil)

	sc := engine.NewAutoCompactor(store, "test-session", "test-model", mp)
	eng := engine.New(&engine.Params{
		Provider:  mp,
		Model:     "test-model",
		Compactor: sc,
		AutoCompact: engine.AutoCompactConfig{
			Threshold:     0.9,
			ContextWindow: 100000,
		},
	})

	largeMsgs := makeLargeMessages(20, 5000)
	eng.SetMessages(largeMsgs)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "continue", nil)
	for range eventCh {
	}

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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
		Timestamp: time.Now(),
	}

	contentBytes, _ := json.Marshal(original.Content)
	shortMsg := &short.Message{
		UUID:      "test-uuid",
		Type:      string(original.Role),
		Content:   string(contentBytes),
		CreatedAt: original.Timestamp,
	}

	converted := engine.ShortMessageToEngine(shortMsg)
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

	shortMsg := &short.Message{
		Type:      "user",
		Content:   "plain text content",
		CreatedAt: time.Now(),
	}

	converted := engine.ShortMessageToEngine(shortMsg)
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
	shortMsg := &short.Message{
		Type:      "assistant",
		Content:   string(contentBytes),
		CreatedAt: time.Now(),
	}

	converted := engine.ShortMessageToEngine(shortMsg)
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
