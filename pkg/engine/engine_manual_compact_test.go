package engine

// engine_manual_compact_test.go tests Engine.ManualCompact, the public entry
// point invoked by the TUI /compact command.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// TestEngine_ManualCompact_NilCompactor verifies that calling ManualCompact
// with no compactor configured returns a descriptive error (rather than
// nil-dereferencing the compactor).
func TestEngine_ManualCompact_NilCompactor(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })

	_, err := eng.ManualCompact(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when compactor is nil")
	}
	if !strings.Contains(err.Error(), "compaction not configured") {
		t.Errorf("error should mention 'compaction not configured', got: %v", err)
	}
}

// TestEngine_ManualCompact_Success verifies the success path: messages are
// replaced by the compactor result and ContextTokens is updated to AfterTokens.
func TestEngine_ManualCompact_Success(t *testing.T) {
	t.Parallel()

	compactor := &funcCompactor{
		fn: func(_ context.Context, messages []types.Message) (*short.CompactResult, error) {
			return &short.CompactResult{
				BeforeTokens:   5000,
				AfterTokens:    1500,
				BeforeMessages: len(messages),
				Messages: []types.Message{
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("boundary")}},
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary")}},
				},
				Summary: "<summary>compacted</summary>",
			}, nil
		},
	}

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 100000})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old message 1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("old reply")}},
	})

	result, err := eng.ManualCompact(context.Background(), "")
	if err != nil {
		t.Fatalf("ManualCompact error: %v", err)
	}
	if result == nil {
		t.Fatal("ManualCompact returned nil result")
	}

	gotMsgs := eng.Messages()
	if len(gotMsgs) != 2 {
		t.Errorf("engine messages after compact = %d, want 2 (boundary + summary)", len(gotMsgs))
	}
	first, _ := extractFirstTextBlock(gotMsgs[0])
	if first != "boundary" {
		t.Errorf("first message text = %q, want %q", first, "boundary")
	}

	eng.mu.RLock()
	ctxTokens := eng.ContextTokens
	eng.mu.RUnlock()
	if ctxTokens != 1500 {
		t.Errorf("ContextTokens = %d, want 1500 (AfterTokens)", ctxTokens)
	}
}

// manualCompactCaseFixture holds the state needed to run a ManualCompact case.
type manualCompactCaseFixture struct {
	eng           *Engine
	completeCalls *int32
}

// newManualCompactSMCase builds an engine backed by a real *AutoCompactor and
// a session memory with valid notes (so TrySMCompact succeeds when invoked).
// completeCalls counts how many times the LLM Complete path ran.
func newManualCompactSMCase(t *testing.T) manualCompactCaseFixture {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Build enough large messages that findKeepFrom returns keepFrom in
	// (1, len): TrySMCompact only succeeds when keepFrom > 1 && keepFrom < len.
	// contextWindow=10000 → keep budget = max(min(2000,60000),2000) = 2000 tokens;
	// 10 messages of ~900 tokens each → head compacted, tail kept.
	msgs := make([]types.Message, 0, 10)
	for i := range 10 {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		msgs = append(msgs, types.Message{
			Role:    role,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat(fmt.Sprintf("msg%d ", i), 600))},
		})
	}
	storeMsgs, sErr := short.EngineMessagesToStore(msgs)
	if sErr != nil {
		t.Fatalf("convert messages: %v", sErr)
	}
	for i, m := range storeMsgs {
		if i > 0 {
			m.ParentUUID = storeMsgs[i-1].UUID
		}
		if err := store.AppendMessage(sess.SessionID, m); err != nil {
			t.Fatalf("persist message %d: %v", i, err)
		}
	}

	var calls int32
	p := &integrationProvider{}
	p.completeFn = func(req *llm.Request) (*llm.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &llm.Response{
			ID:    "summary-resp",
			Type:  "message",
			Role:  "assistant",
			Model: "test-model",
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "<summary>llm summary</summary>"},
			},
			StopReason: "end_turn",
		}, nil
	}

	compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 10000, provider: p})
	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		Logger:    slog.Default(),
	})
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)
	eng.SetMessages(msgs)

	// Session memory with real notes content — TrySMCompact succeeds when invoked.
	sm := session.New(session.DefaultConfig(), tmpDir, "main", nil, slog.Default())
	eng.SetSessionMemory(sm)
	notesPath := sm.NotesPath()
	if err := os.MkdirAll(filepath.Dir(notesPath), 0755); err != nil {
		t.Fatalf("mkdir notes dir: %v", err)
	}
	notesContent := "# Session Notes\n\n## Session Title\nTest\n\n## Current State\nRunning\n"
	if err := os.WriteFile(notesPath, []byte(notesContent), 0644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	t.Cleanup(func() { eng.Close() })
	return manualCompactCaseFixture{eng: eng, completeCalls: &calls}
}

// TestEngine_ManualCompact_CustomInstructions_SkipsSMCompact verifies the TS
// compact.ts:55-57 gate: when customInstructions is non-empty, session-memory
// compaction (which doesn't support custom instructions) must be SKIPPED, so
// the LLM summarization path runs instead. With empty instructions and a
// working session memory, SM compact succeeds and the LLM Complete is NOT called.
func TestEngine_ManualCompact_CustomInstructions_SkipsSMCompact(t *testing.T) {
	t.Run("WithInstructions_UsesLLMNotSM", func(t *testing.T) {
		t.Parallel()
		fix := newManualCompactSMCase(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := fix.eng.ManualCompact(ctx, "focus on API design")
		if err != nil {
			t.Fatalf("ManualCompact(instructions) error: %v", err)
		}
		if got := atomic.LoadInt32(fix.completeCalls); got != 1 {
			t.Errorf("expected LLM Complete to be called exactly once when custom instructions present (SM compact skipped), got %d calls", got)
		}
		if result == nil {
			t.Fatal("expected non-nil result")
		}
	})

	t.Run("EmptyInstructions_UsesSMNotLLM", func(t *testing.T) {
		t.Parallel()
		fix := newManualCompactSMCase(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := fix.eng.ManualCompact(ctx, ""); err != nil {
			t.Fatalf("ManualCompact() error: %v", err)
		}
		if got := atomic.LoadInt32(fix.completeCalls); got != 0 {
			t.Errorf("expected LLM Complete NOT to be called when SM compact succeeds, got %d calls", got)
		}
	})
}

// TestEngine_ManualCompact_PassesInstructionsToCompactor verifies that when
// the compactor is an *AutoCompactor, custom instructions reach
// CompactWithInstructions (not the no-arg Compact). We detect this by
// inspecting the captured LLM request for the "Additional Instructions:" marker.
func TestEngine_ManualCompact_PassesInstructionsToCompactor(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess, err := store.CreateSession(tmpDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	custom := "emphasize the bug-fix decisions"
	var capturedReq atomic.Pointer[llm.Request]
	p := &integrationProvider{}
	p.completeFn = func(req *llm.Request) (*llm.Response, error) {
		capturedReq.Store(req)
		return &llm.Response{
			ID:    "summary-resp",
			Type:  "message",
			Role:  "assistant",
			Model: "test-model",
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "<summary>ok</summary>"},
			},
			StopReason: "end_turn",
		}, nil
	}

	compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 40000, provider: p})
	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		Logger:    slog.Default(),
	})
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	})

	if _, err := eng.ManualCompact(context.Background(), custom); err != nil {
		t.Fatalf("ManualCompact error: %v", err)
	}

	req := capturedReq.Load()
	if req == nil {
		t.Fatal("Complete was not called")
	}
	last := req.Messages[len(req.Messages)-1]
	var lastText string
	for _, b := range last.Content {
		if b.Type == types.ContentTypeText {
			lastText = b.Text
			break
		}
	}
	want := "Additional Instructions:\n" + custom
	if !strings.Contains(lastText, want) {
		t.Errorf("compact prompt does not contain custom instructions.\nwant substring: %q\n got prompt tail: %q",
			want, lastText[max(0, len(lastText)-300):])
	}
}
