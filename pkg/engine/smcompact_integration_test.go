package engine

// smcompact_integration_test.go contains chain tests for the full
// Session Memory → SM-compact pipeline.
//
// Test design principles applied:
//   - Test call chains, not functions: entry → hook → extract → compact → observable output
//   - Simulate real boundaries: real filesystem, real Store, mock LLM only
//   - Three scenarios: cold start, hot path, recovery (stale extraction)

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func setTempHome(t *testing.T) {
	t.Helper()
	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatalf("create temp home: %v", err)
	}
	t.Setenv("HOME", homeDir)
}

// ---------------------------------------------------------------------------
// Integration Test 1: Full chain — extract → SM-compact → verify
// ---------------------------------------------------------------------------

// TestSMCompact_Integration_ExtractThenCompact verifies the full pipeline:
//
//	Engine turn → extraction writes session memory file
//	→ next turn exceeds threshold → runCompact() → TrySMCompact() reads file → success
//
// Observable output: CompactResult contains session memory content,
// engine messages are reduced, boundary marker is present.
func TestSMCompact_Integration_ExtractThenCompact(t *testing.T) {
	setTempHome(t)

	tmpDir := t.TempDir()
	// Create a mock extraction function that writes real content to the file
	extractFn := func(ctx context.Context, prompt string, targetPath string, _ []types.Message, _ string) error {
		content := `# Session Notes

## Session Title
Testing SM-compact integration

## Current State
Full chain test: extract → compact → verify

## Files and Functions
smcompact_integration_test.go — chain tests
`
		return os.WriteFile(targetPath, []byte(content), 0644)
	}

	sm := session.New(session.Config{
		MinTokensToInit:        50,
		MinTokensBetweenUpdate: 25,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      60000,
	}, tmpDir, "main", extractFn, slog.Default())
	notesPath := sm.NotesPath()

	// Set up engine with real Store + real AutoCompactor
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	// Turn response (after compact)
	p.addStream(textStreamEvents("test-model", "Turn after SM-compact."), nil)

	compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 500, provider: p})

	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 500, // low threshold to trigger compact
		},
		Logger: slog.Default(),
	})

	// Wire session memory — this registers the post-turn hook
	eng.SetSessionMemory(sm)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Step 1: Run extraction directly (simulates what the post-turn hook does)
	// This avoids the async goroutine race condition while testing the same chain.
	msgs := makeLargeMessages(5, 50)
	if err := sm.Extract(ctx, msgs, 250); err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	// Verify session memory file was created with real content
	data, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("session memory file not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Testing SM-compact integration") {
		t.Errorf("session memory content missing expected text, got: %s", content[:min(200, len(content))])
	}

	// Step 2: Set large messages and query → engine should auto-compact using SM-compact
	// 20 messages × 50 tokens = 1000 tokens > 90% of ContextWindow(500)
	eng.SetMessages(makeLargeMessages(25, 600))
	result := eng.QuerySync(ctx, "turn 1", "")
	if result.Error != nil {
		t.Fatalf("query failed: %v", result.Error)
	}

	// Verify SM-compact was used: messages should contain session memory summary.
	// After compact, engine adds user query + assistant response, so total may
	// equal original count. The key observable is the SM-compact summary content.
	finalMsgs := eng.Messages()
	foundSMSummary := false
	for _, msg := range finalMsgs {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeText {
				if strings.Contains(block.Text, "session memory") &&
					strings.Contains(block.Text, "Testing SM-compact integration") {
					foundSMSummary = true
				}
			}
		}
	}
	if !foundSMSummary {
		t.Error("SM-compact summary not found in final messages — LLM compact was used instead")
	}
}

// ---------------------------------------------------------------------------
// Integration Test 2: Cold start — no file → extraction creates it
// ---------------------------------------------------------------------------

// TestSessionMemory_Integration_ColdStart verifies the cold-start scenario:
//
//	No session memory file exists → first extraction creates from template
//	→ second extraction updates with real content
//
// Observable output: file exists after extraction, content is real (not template).
func TestSessionMemory_Integration_ColdStart(t *testing.T) {
	setTempHome(t)

	tmpDir := t.TempDir()
	// Intentionally do NOT create the memory directory or file

	extractionCount := 0
	extractFn := func(ctx context.Context, prompt string, targetPath string, _ []types.Message, _ string) error {
		extractionCount++
		// First extraction: file is created from template by ensureFile()
		// We write real content to simulate what the sub-agent would do
		content := `# Session Notes

## Session Title
Cold start test — first extraction

## Current State
File was created from template and now has real content
`
		return os.WriteFile(targetPath, []byte(content), 0644)
	}

	sm := session.New(session.Config{
		MinTokensToInit:        50,
		MinTokensBetweenUpdate: 25,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      60000,
	}, tmpDir, "main", extractFn, slog.Default())

	ctx := context.Background()

	// Cold start: no file, no state
	if !sm.IsEmpty() {
		t.Error("expected IsEmpty=true before any extraction (cold start)")
	}

	// Trigger extraction
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi there")}},
	}

	// ShouldExtract with enough tokens → true
	if !sm.ShouldExtract(100, msgs) {
		t.Fatal("ShouldExtract should return true for cold start with enough tokens")
	}

	// Run extraction — should create the file
	if err := sm.Extract(ctx, msgs, 100); err != nil {
		t.Fatalf("Extract failed on cold start: %v", err)
	}

	if extractionCount != 1 {
		t.Errorf("expected 1 extraction call, got %d", extractionCount)
	}

	// Verify file was created
	notesPath := sm.NotesPath()
	data, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("session memory file not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Cold start test") {
		t.Errorf("file content should be from extraction, got: %s", content[:min(200, len(content))])
	}

	// IsEmpty should now return false
	if sm.IsEmpty() {
		t.Error("expected IsEmpty=false after extraction with real content")
	}
}

// ---------------------------------------------------------------------------
// Integration Test 3: Recovery — stale extraction → recovery → next works
// ---------------------------------------------------------------------------

// TestSessionMemory_Integration_StaleRecovery verifies the recovery scenario:
//
//	Extraction starts but stalls (simulates crash/hang)
//	→ stale threshold exceeded → recovery resets state
//	→ next extraction succeeds
//
// Observable output: after recovery, extraction runs and writes file.
func TestSessionMemory_Integration_StaleRecovery(t *testing.T) {
	setTempHome(t)

	tmpDir := t.TempDir()

	blockCh := make(chan struct{})
	firstCall := true
	extractFn := func(ctx context.Context, prompt string, targetPath string, _ []types.Message, _ string) error {
		if firstCall {
			firstCall = false
			<-blockCh // block forever (simulates crash)
		}
		return os.WriteFile(targetPath, []byte("## Session Title\nRecovered content\n"), 0644)
	}

	sm := session.New(session.Config{
		MinTokensToInit:        50,
		MinTokensBetweenUpdate: 25,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      100, // very short stale threshold
	}, tmpDir, "main", extractFn, slog.Default())
	notesPath := sm.NotesPath()

	ctx := context.Background()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	// Start first extraction (will hang)
	sm.ShouldExtract(100, msgs) // initialize
	go func() {
		_ = sm.Extract(ctx, msgs, 100)
	}()

	// Wait for extraction goroutine to start, then for stale threshold (100ms) to pass.
	// Real time is needed because we're testing time-based staleness detection.
	<-time.After(200 * time.Millisecond)

	// Now WaitForExtraction should detect stale and recover
	if err := sm.WaitForExtraction(); err != nil {
		t.Fatalf("WaitForExtraction should recover from stale: %v", err)
	}

	// Unblock the hung extraction (it's already been abandoned)
	close(blockCh)

	// Next extraction should succeed after recovery
	sm.Reset() // reset to allow re-extraction
	if !sm.ShouldExtract(200, msgs) {
		t.Fatal("ShouldExtract should return true after recovery")
	}

	if err := sm.Extract(ctx, msgs, 200); err != nil {
		t.Fatalf("post-recovery Extract failed: %v", err)
	}

	// Verify file was written with recovery content
	data, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("file not written after recovery: %v", err)
	}
	if !strings.Contains(string(data), "Recovered content") {
		t.Errorf("expected recovery content, got: %s", string(data))
	}
}

// ---------------------------------------------------------------------------
// Integration Test 4: Hot path — extraction → update → SM-compact
// ---------------------------------------------------------------------------

// TestSessionMemory_Integration_HotPath verifies the normal hot path:
//
//  1. Write initial session memory content
//  2. SM-compact uses it → produces compact result
//  3. Update session memory with new content
//  4. SM-compact uses updated content → produces different result
//
// Observable output: compact results reflect the current session memory content.
func TestSessionMemory_Integration_HotPath(t *testing.T) {
	setTempHome(t)

	tmpDir := t.TempDir()

	// Set up compactor
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 500, provider: p})
	sm := session.New(session.DefaultConfig(), tmpDir, "main", nil, slog.Default())
	notesPath := sm.NotesPath()
	if err := os.MkdirAll(filepath.Dir(notesPath), 0755); err != nil {
		t.Fatal(err)
	}

	// Write initial content
	initialContent := "## Session Title\nHot path test\n## Current State\nInitial state\n"
	if err := os.WriteFile(notesPath, []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}

	msgs := makeLargeMessages(25, 500)

	// SM-compact round 1: uses initial content
	result1, err := compactor.TrySMCompact(msgs, sm)
	if err != nil {
		t.Fatalf("first TrySMCompact failed: %v", err)
	}
	if result1 == nil {
		t.Fatal("first TrySMCompact should succeed with initial content")
	}
	if !strings.Contains(result1.Summary, "Initial state") {
		t.Errorf("first summary should contain initial content, got: %s", result1.Summary[:min(200, len(result1.Summary))])
	}

	// Update session memory (simulates extraction writing new content)
	updatedContent := "## Session Title\nHot path test\n## Current State\nUpdated state after more work\n"
	if err := os.WriteFile(notesPath, []byte(updatedContent), 0644); err != nil {
		t.Fatal(err)
	}

	// SM-compact round 2: uses updated content
	msgs2 := makeLargeMessages(25, 500)
	result2, err := compactor.TrySMCompact(msgs2, sm)
	if err != nil {
		t.Fatalf("second TrySMCompact failed: %v", err)
	}
	if result2 == nil {
		t.Fatal("second TrySMCompact should succeed with updated content")
	}
	if !strings.Contains(result2.Summary, "Updated state after more work") {
		t.Errorf("second summary should contain updated content, got: %s", result2.Summary[:min(200, len(result2.Summary))])
	}

	// Results should differ (content changed)
	if result1.Summary == result2.Summary {
		t.Error("summaries should differ after session memory update")
	}
}

// ---------------------------------------------------------------------------
// Integration Test 5: SM-compact falls back to LLM compact when empty
// ---------------------------------------------------------------------------

// TestSMCompact_Integration_FallbackToLLM verifies the fallback behavior:
//
//	Session memory exists but is empty (template only)
//	→ TrySMCompact returns nil
//	→ runCompact falls back to LLM summarization
//	→ compact succeeds with LLM summary
//
// Observable output: compact produces LLM summary, not session memory summary.
func TestSMCompact_Integration_FallbackToLLM(t *testing.T) {
	setTempHome(t)

	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &integrationProvider{}
	p.addStream(textStreamEvents("test-model", "Response after LLM compact."), nil)

	compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 500, provider: p})
	sm := session.New(session.DefaultConfig(), tmpDir, "main", nil, slog.Default())
	notesPath := sm.NotesPath()
	if err := os.MkdirAll(filepath.Dir(notesPath), 0755); err != nil {
		t.Fatal(err)
	}

	// Write template-only content (empty)
	if err := os.WriteFile(notesPath, []byte(session.DefaultTemplate), 0644); err != nil {
		t.Fatal(err)
	}

	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 500,
		},
		Logger: slog.Default(),
	})
	eng.SetSessionMemory(sm)

	// Set messages that exceed threshold
	eng.SetMessages(makeLargeMessages(25, 600))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error != nil {
		t.Fatalf("query failed: %v", result.Error)
	}

	// Verify compact happened: FormatCompactSummary converts <summary> tags to
	// "Summary:" text, and the wrapper adds "continued from a previous conversation".
	// After compact, engine adds assistant response so message count may equal original.
	finalMsgs := eng.Messages()

	// Verify LLM compact ran (not SM-compact).
	// SM-compact produces "session memory" text; LLM compact produces "Summary:" text
	// (after FormatCompactSummary strips <summary> tags).
	foundLLMSummary := false
	for _, msg := range finalMsgs {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeText {
				if strings.Contains(block.Text, "Summary:") &&
					strings.Contains(block.Text, "Test summary of conversation") {
					foundLLMSummary = true
				}
			}
		}
	}
	if !foundLLMSummary {
		t.Error("expected LLM summary (Summary: text) since session memory was empty")
	}
}

func TestExtraction_ReceivesConversationContext(t *testing.T) {
	setTempHome(t)

	tmpDir := t.TempDir()

	var captured struct {
		mu              sync.Mutex
		gotMessages     []types.Message
		gotSystemPrompt string
	}

	extractFn := func(ctx context.Context, prompt string, targetPath string, messages []types.Message, systemPrompt string) error {
		captured.mu.Lock()
		captured.gotMessages = messages
		captured.gotSystemPrompt = systemPrompt
		captured.mu.Unlock()
		// Write real content to verify end-to-end
		content := "## Session Title\nExtraction with context test\n"
		return os.WriteFile(targetPath, []byte(content), 0644)
	}

	sm := session.New(session.Config{
		MinTokensToInit:        50,
		MinTokensBetweenUpdate: 25,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      60000,
	}, tmpDir, "main", extractFn, slog.Default())
	notesPath := sm.NotesPath()
	if err := os.MkdirAll(filepath.Dir(notesPath), 0755); err != nil {
		t.Fatal(err)
	}

	// Create messages to pass to extraction
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi there")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("fix the bug")}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sm.Extract(ctx, messages, 100); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	captured.mu.Lock()
	defer captured.mu.Unlock()

	// Assert: extractFn must receive the conversation messages
	if len(captured.gotMessages) != len(messages) {
		t.Errorf("extractFn received %d messages, want %d — conversation context not passed",
			len(captured.gotMessages), len(messages))
	}

	// Assert: messages content must match
	firstText, _ := extractFirstTextBlock(captured.gotMessages[0])
	if firstText != "hello" {
		t.Errorf("first message text = %q, want %q", firstText, "hello")
	}
}

// ---------------------------------------------------------------------------
// Integration Test: SM-compact must persist boundary to DB
// ---------------------------------------------------------------------------

// TestSMCompact_WritesBoundaryToDB verifies that runCompact writes a compact_boundary
// to the store when SM-compact succeeds. Without this, resume loads ALL historical
// messages instead of just the post-compact chain.
//
// Call chain: runCompact() → TrySMCompact() → [RecordCompact] → DB
// Observable output: store has a message with type=system, subtype=compact_boundary.
func TestSMCompact_WritesBoundaryToDB(t *testing.T) {
	setTempHome(t)

	tmpDir := t.TempDir()

	// Set up store + session
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Persist messages so PartialCompact has real data to split
	msgs := makeLargeMessages(20, 500)
	storeMsgs, err := short.EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("convert messages: %v", err)
	}
	for i, m := range storeMsgs {
		if i > 0 {
			m.ParentUUID = storeMsgs[i-1].UUID
		}
		if err := store.AppendMessage(sess.SessionID, m); err != nil {
			t.Fatalf("persist message %d: %v", i, err)
		}
	}

	p := &integrationProvider{}
	compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 40000, provider: p})

	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		Logger:    slog.Default(),
	})
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)
	eng.SetMessages(msgs)

	// Set up session memory (already extracted — file exists on disk)
	sm := session.New(session.DefaultConfig(), tmpDir, "main", nil, slog.Default())
	eng.SetSessionMemory(sm)

	// Write real session memory content at the resolved NotesPath so TrySMCompact succeeds
	notesPath := sm.NotesPath()
	if err := os.MkdirAll(filepath.Dir(notesPath), 0755); err != nil {
		t.Fatalf("mkdir notes dir: %v", err)
	}
	notesContent := `# Session Notes

## Session Title
Boundary persistence test

## Current State
Verifying SM-compact writes boundary to DB

## Files and Functions
smcompact_integration_test.go — boundary test
`
	if err := os.WriteFile(notesPath, []byte(notesContent), 0644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := eng.runCompact(ctx)
	if err != nil {
		t.Fatalf("runCompact failed: %v", err)
	}
	if result == nil {
		t.Fatal("runCompact returned nil result")
	}
	if len(result.Messages) == 0 {
		t.Fatal("runCompact returned empty messages")
	}

	// RED LIGHT: Verify DB has compact_boundary record
	dbMsgs, err := store.LoadMessages(sess.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	foundBoundary := false
	for _, m := range dbMsgs {
		if m.Type == "system" && m.Subtype == "compact_boundary" {
			foundBoundary = true
			break
		}
	}
	if !foundBoundary {
		t.Errorf("SM-compact did not write boundary to DB — got %d messages, none with subtype=compact_boundary. "+
			"Resume will reload ALL historical messages instead of post-compact chain.", len(dbMsgs))
	}
}
