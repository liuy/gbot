package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// --- formatSMSummary ---

func TestFormatSMSummary(t *testing.T) {
	t.Parallel()
	content := "## Session Title\nTesting session memory\n"
	result := formatSMSummary(content)

	if !strings.Contains(result, "summary of the conversation from session memory") {
		t.Error("should contain the summary preamble")
	}
	if !strings.Contains(result, "Testing session memory") {
		t.Error("should contain the original content")
	}
	// Content should come after preamble
	preambleIdx := strings.Index(result, "session memory:")
	contentIdx := strings.Index(result, "Testing session memory")
	if contentIdx <= preambleIdx {
		t.Error("content should appear after the preamble")
	}
}

// --- TrySMCompact ---

func TestTrySMCompact_NilSessionMemory(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)
	msgs := makeLargeMessages(10, 100)

	result, err := ac.TrySMCompact(msgs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("should return nil with nil session memory")
	}
}

func TestTrySMCompact_EmptySessionMemory(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, session.SessionMemoryFileName)
	// Write template-only content (empty)
	if err := os.WriteFile(notesPath, []byte(session.DefaultTemplate), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sm := session.New(session.DefaultConfig(), tmpDir, nil, nil)
	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	msgs := makeLargeMessages(20, 500)
	result, err := ac.TrySMCompact(msgs, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("should return nil with empty session memory")
	}
}

func TestTrySMCompact_WithRealContent(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, session.SessionMemoryFileName)

	// Write real session memory content
	content := `# Session Notes

## Session Title
Implementing session memory tests

## Current State
Writing SM-compact test coverage

## Files and Functions
smcompact.go — TrySMCompact function
smcompact_test.go — test file
`
	if err := os.WriteFile(notesPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := short.NewStore(t.TempDir() + "/test2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sm := session.New(session.DefaultConfig(), tmpDir, nil, nil)
	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	// Create enough messages for PartialCompact to work
	// Need at least keepFrom > 1 and keepFrom < len(messages)
	msgs := makeLargeMessages(20, 500)

	result, err := ac.TrySMCompact(msgs, sm)
	if err != nil {
		t.Fatalf("TrySMCompact failed: %v", err)
	}
	if result == nil {
		t.Fatal("should return non-nil result with real content")
	}

	// Verify the result structure
	if result.Summary == "" {
		t.Error("summary should not be empty")
	}
	if !strings.Contains(result.Summary, "session memory") {
		t.Error("summary should reference session memory")
	}
	if !strings.Contains(result.Summary, "Implementing session memory tests") {
		t.Error("summary should contain the real session memory content")
	}
	if result.BeforeMessages != len(msgs) {
		t.Errorf("BeforeMessages = %d, want %d", result.BeforeMessages, len(msgs))
	}
	if result.BeforeTokens <= 0 {
		t.Error("BeforeTokens should be positive")
	}
	if len(result.Messages) == 0 {
		t.Error("result should contain compacted messages")
	}
	// Compacted messages should be fewer than original
	if len(result.Messages) >= len(msgs) {
		t.Errorf("result has %d messages, should be fewer than original %d",
			len(result.Messages), len(msgs))
	}
	if result.AfterTokens <= 0 {
		t.Error("AfterTokens should be positive")
	}
}

func TestTrySMCompact_TooFewMessages(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, session.SessionMemoryFileName)
	content := "## Session Title\nReal work\n"
	if err := os.WriteFile(notesPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sm := session.New(session.DefaultConfig(), tmpDir, nil, nil)
	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	// Only 2 messages — too few for PartialCompact to find a valid boundary
	msgs := makeLargeMessages(2, 100)

	result, err := ac.TrySMCompact(msgs, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return nil because keepFrom will be >= len or <= 1
	if result != nil {
		t.Error("should return nil with too few messages")
	}
}

func TestTrySMCompact_NoFile(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	// Don't create the memory file

	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sm := session.New(session.DefaultConfig(), tmpDir, nil, nil)
	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	msgs := makeLargeMessages(20, 500)
	result, err := ac.TrySMCompact(msgs, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("should return nil when session memory file doesn't exist")
	}
}

// --- buildResultMessages ---

func TestBuildSMResultMessages_BasicAssembly(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	// Create a minimal CompactResult with messages to keep
	shortMsgs := makeLargeMessages(10, 100)
	shortList, err := short.EngineMessagesToStore(shortMsgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}

	// Use PartialCompact to get a real CompactResult
	keepFrom := 3
	pcr, err := store.PartialCompact("test-session", shortList, keepFrom)
	if err != nil {
		t.Fatalf("PartialCompact failed: %v", err)
	}

	summaryText := "This is a test summary from session memory."
	msgs := ac.buildResultMessages(pcr, summaryText)

	// Should have: boundary + summary + kept messages
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages (boundary + summary + kept), got %d", len(msgs))
	}

	// First message should be boundary (user role)
	if msgs[0].Role != types.RoleUser {
		t.Errorf("first message role = %q, want %q", msgs[0].Role, types.RoleUser)
	}

	// Second message should contain summary
	bb, _ := extractFirstTextBlock(msgs[1])
	if !strings.Contains(bb, "test summary from session memory") {
		t.Errorf("second message should contain summary, got: %q", bb)
	}

	// Remaining messages should be the kept ones
	keptCount := len(pcr.MessagesToKeep)
	if len(msgs) != 2+keptCount {
		t.Errorf("expected %d messages (2 + %d kept), got %d",
			2+keptCount, keptCount, len(msgs))
	}
}

func TestBuildSMResultMessages_EmptyBoundary(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	// Create a CompactResult with nil boundary marker
	shortMsgs := makeLargeMessages(10, 100)
	shortList, err := short.EngineMessagesToStore(shortMsgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	pcr, err := store.PartialCompact("test-session", shortList, 3)
	if err != nil {
		t.Fatalf("PartialCompact failed: %v", err)
	}

	msgs := ac.buildResultMessages(pcr, "summary text")
	if len(msgs) == 0 {
		t.Fatal("should produce at least one message")
	}

	// Boundary message should exist even if content is empty
	if msgs[0].Role != types.RoleUser {
		t.Errorf("boundary should be user role, got %q", msgs[0].Role)
	}
}

// --- Integration: SetSessionMemory + post-turn hook ---

func TestSetSessionMemory_RegistersHook(t *testing.T) {
	setTempHome(t)
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})

	hookCount := 0
	// Verify hooks are registered by checking postTurnHooks length
	eng.RegisterPostTurnHook(func(ctx context.Context, messages []types.Message, currentTokens int, querySource string) {
		hookCount++
	})

	sm := session.New(session.DefaultConfig(), t.TempDir(), nil, nil)
	eng.SetSessionMemory(sm)

	// SetSessionMemory should register another hook
	eng.mu.Lock()
	hookLen := len(eng.postTurnHooks)
	eng.mu.Unlock()

	if hookLen != 2 {
		t.Errorf("expected 2 post-turn hooks, got %d", hookLen)
	}
}

func TestSetSessionMemory_NilDoesNotRegisterHook(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})

	eng.SetSessionMemory(nil)

	eng.mu.Lock()
	hookLen := len(eng.postTurnHooks)
	eng.mu.Unlock()

	if hookLen != 0 {
		t.Errorf("nil SessionMemory should not register hooks, got %d", hookLen)
	}
}

// --- Integration: querySource recursion guard ---

func TestQuerySource_SessionMemoryAgent(t *testing.T) {
	eng := New(&Params{Model: "test"})
	subEng := eng.NewSubEngine(SubEngineOptions{
		AgentType: "session_memory",
		Tools:     map[string]tool.Tool{},
	})

	src := subEng.querySource()
	if src != QuerySourceSessionMemory {
		t.Errorf("querySource = %q, want %q", src, QuerySourceSessionMemory)
	}
}

func TestQuerySource_CompactAgent(t *testing.T) {
	eng := New(&Params{Model: "test"})
	subEng := eng.NewSubEngine(SubEngineOptions{
		AgentType: "compact",
		Tools:     map[string]tool.Tool{},
	})

	src := subEng.querySource()
	if src != QuerySourceCompact {
		t.Errorf("querySource = %q, want %q", src, QuerySourceCompact)
	}
}

func TestShouldAutoCompact_SessionMemoryAgent(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		AutoCompact: AutoCompactConfig{ContextWindow: 128000},
	})
	defer eng.Close()
	eng.compactor = &AutoCompactor{}

	subEng := eng.NewSubEngine(SubEngineOptions{
		AgentType: "session_memory",
		Tools:     map[string]tool.Tool{},
	})
	defer subEng.Close()

	// Add messages to exceed threshold
	subEng.SetMessages(makeLargeMessages(20, 1000))

	if subEng.shouldAutoCompact() {
		t.Error("session_memory agent should not auto-compact (recursion guard)")
	}
}

// --- helper ---

// extractFirstTextBlock returns the first text content block's text.
func extractFirstTextBlock(msg types.Message) (string, bool) {
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeText {
			return b.Text, true
		}
	}
	return "", false
}

// --- SM-compact coverage gap tests ---

func TestTrySMCompact_WaitForExtractionError(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, session.SessionMemoryFileName)
	smContent := "## Session Title\nReal work\n"
	if err := os.WriteFile(notesPath, []byte(smContent), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	blockCh := make(chan struct{})
	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		<-blockCh
		return nil
	}

	sm := session.New(session.Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    100,
		ExtractionStaleMs:      60000,
	}, tmpDir, extractFn, slog.Default())

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	go func() { _ = sm.Extract(context.Background(), msgs, 100) }()
	time.Sleep(50 * time.Millisecond) // REAL-TIME: wait for extraction to start

	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	largeMsgs := makeLargeMessages(20, 500)
	result, err := ac.TrySMCompact(largeMsgs, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("should return nil when WaitForExtraction times out")
	}
	close(blockCh)
}

func TestTrySMCompact_EmptyMessages(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, session.SessionMemoryFileName)
	content := "## Session Title\nReal work\n"
	if err := os.WriteFile(notesPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sm := session.New(session.DefaultConfig(), tmpDir, nil, nil)
	ac := NewAutoCompactor(store, "test-session", "test-model", nil, 40000)

	result, err := ac.TrySMCompact([]types.Message{}, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("should return nil with empty messages")
	}
}

