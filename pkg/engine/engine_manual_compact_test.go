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

func compactUserMsg() types.Message {
	return types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("/compact")},
	}
}

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

	_, err := eng.ManualCompact(context.Background(), compactUserMsg(), "")
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

	result, err := eng.ManualCompact(context.Background(), compactUserMsg(), "")
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
		result, err := fix.eng.ManualCompact(ctx, compactUserMsg(), "focus on API design")
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
		if _, err := fix.eng.ManualCompact(ctx, compactUserMsg(), ""); err != nil {
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

	compactor := NewAutoCompactor(store, &testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 1000, provider: p})
	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		Logger:    slog.Default(),
	})
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)
	// Exceed findKeepFrom tail budget (2000 tokens) so compact actually runs.
	eng.SetMessages(makeLargeMessages(20, 600))

	if _, err := eng.ManualCompact(context.Background(), compactUserMsg(), custom); err != nil {
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

// TestEngine_ManualCompact_EmitsToolEvents verifies that ManualCompact emits
// the EventToolStart/Run/ParamDelta/End sequence mirroring the pre-turn
// auto-compact path (engine.go:1356). The tool ID must use the compact-manual-
// prefix so the TUI's toolEndMsg handler finalizes the stream.
func TestEngine_ManualCompact_EmitsToolEvents(t *testing.T) {
	t.Parallel()

	summaryText := "<summary>compacted body</summary>"
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
				Summary: summaryText,
			}, nil
		},
	}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   &testProvider{},
		Dispatcher: ec,
		Model:      "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 100000})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old message 1")}},
	})

	result, err := eng.ManualCompact(context.Background(), compactUserMsg(), "")
	if err != nil {
		t.Fatalf("ManualCompact error: %v", err)
	}
	if result == nil {
		t.Fatal("ManualCompact returned nil result")
	}

	events := ec.Events()
	wantCount := 6
	if len(events) != wantCount {
		t.Fatalf("event count = %d, want %d; events: %+v", len(events), wantCount, eventTypes(events))
	}

	if et := events[0].Type; et != types.EventQueryStart {
		t.Fatalf("events[0].Type = %s, want %s", et, types.EventQueryStart)
	}
	if events[0].Message == nil || events[0].Message.Role != types.RoleUser {
		t.Fatal("events[0].Message is nil or not user role")
	}

	if et := events[1].Type; et != types.EventToolStart {
		t.Fatalf("events[1].Type = %s, want %s", et, types.EventToolStart)
	}
	if events[1].ToolUse == nil {
		t.Fatal("events[1].ToolUse is nil")
	}
	startID := events[1].ToolUse.ID
	if !strings.HasPrefix(startID, "compact-manual-") {
		t.Errorf("ToolUse.ID = %q, want compact-manual-* prefix", startID)
	}
	if events[1].ToolUse.Name != "Compact" {
		t.Errorf("ToolUse.Name = %q, want \"Compact\"", events[1].ToolUse.Name)
	}
	if events[1].ToolUse.Summary != "Compacting conversation..." {
		t.Errorf("ToolUse.Summary = %q, want \"Compacting conversation...\"", events[1].ToolUse.Summary)
	}

	if et := events[2].Type; et != types.EventToolRun {
		t.Fatalf("events[2].Type = %s, want %s", et, types.EventToolRun)
	}
	if events[2].ToolUse == nil || events[2].ToolUse.ID != startID {
		t.Errorf("events[2].ToolUse.ID = %q, want %s", toolUseIDOrEmpty(events[2].ToolUse), startID)
	}

	if et := events[3].Type; et != types.EventToolParamDelta {
		t.Fatalf("events[3].Type = %s, want %s", et, types.EventToolParamDelta)
	}
	if events[3].PartialInput == nil {
		t.Fatal("events[3].PartialInput is nil")
	}
	if events[3].PartialInput.ID != startID {
		t.Errorf("PartialInput.ID = %q, want %s", events[3].PartialInput.ID, startID)
	}
	if !strings.HasPrefix(events[3].PartialInput.Summary, "Conversation compacted") {
		t.Errorf("PartialInput.Summary = %q, want prefix \"Conversation compacted\"", events[3].PartialInput.Summary)
	}

	if et := events[4].Type; et != types.EventToolEnd {
		t.Fatalf("events[4].Type = %s, want %s", et, types.EventToolEnd)
	}
	if events[4].ToolResult == nil {
		t.Fatal("events[4].ToolResult is nil")
	}
	if events[4].ToolResult.ToolUseID != startID {
		t.Errorf("ToolResult.ToolUseID = %q, want %s", events[4].ToolResult.ToolUseID, startID)
	}
	if events[4].ToolResult.IsError {
		t.Error("ToolResult.IsError = true, want false")
	}
	if events[4].ToolResult.DisplayOutput != summaryText {
		t.Errorf("ToolResult.DisplayOutput = %q, want %q (FormatCompactOutput returns result.Summary)", events[4].ToolResult.DisplayOutput, summaryText)
	}

	if et := events[5].Type; et != types.EventQueryEnd {
		t.Fatalf("events[5].Type = %s, want %s", et, types.EventQueryEnd)
	}
	if events[5].Error != nil {
		t.Errorf("QueryEnd.Error = %v, want nil", events[5].Error)
	}
}

// TestEngine_ManualCompact_CustomInstructions_EventSummary verifies custom
// instructions surface in the EventToolStart summary.
func TestEngine_ManualCompact_CustomInstructions_EventSummary(t *testing.T) {
	t.Parallel()

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return &short.CompactResult{
				BeforeTokens:   100,
				AfterTokens:    50,
				BeforeMessages: 2,
				Messages: []types.Message{
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary")}},
				},
				Summary: "ok",
			}, nil
		},
	}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   &testProvider{},
		Dispatcher: ec,
		Model:      "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 100000})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old")}},
	})

	if _, err := eng.ManualCompact(context.Background(), compactUserMsg(), "focus on the API"); err != nil {
		t.Fatalf("ManualCompact error: %v", err)
	}

	starts := ec.FindEvents(types.EventToolStart)
	if len(starts) != 1 {
		t.Fatalf("EventToolStart count = %d, want 1", len(starts))
	}
	want := "Compacting conversation (focus on the API)"
	if starts[0].ToolUse == nil || starts[0].ToolUse.Summary != want {
		t.Errorf("EventToolStart.Summary = %q, want %q",
			toolUseSummaryOrEmpty(starts[0].ToolUse), want)
	}
}

// TestEngine_ManualCompact_ErrorEmitsToolEndError verifies that when the
// compactor returns an error, ManualCompact emits EventToolEnd with IsError
// and no EventToolParamDelta.
func TestEngine_ManualCompact_ErrorEmitsToolEndError(t *testing.T) {
	t.Parallel()

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return nil, fmt.Errorf("summarization unavailable")
		},
	}

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   &testProvider{},
		Dispatcher: ec,
		Model:      "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 100000})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old")}},
	})

	_, err := eng.ManualCompact(context.Background(), compactUserMsg(), "")
	if err == nil {
		t.Fatal("expected error from ManualCompact")
	}
	if !strings.Contains(err.Error(), "summarization unavailable") {
		t.Errorf("error = %v, want substring \"summarization unavailable\"", err)
	}

	events := ec.Events()
	// QueryStart + Start + Run + End(error) + QueryEnd(error); no ParamDelta on the error path.
	wantCount := 5
	if len(events) != wantCount {
		t.Fatalf("event count = %d, want %d; events: %+v", len(events), wantCount, eventTypes(events))
	}

	if et := events[0].Type; et != types.EventQueryStart {
		t.Fatalf("events[0].Type = %s, want %s", et, types.EventQueryStart)
	}
	if et := events[1].Type; et != types.EventToolStart {
		t.Fatalf("events[1].Type = %s, want %s", et, types.EventToolStart)
	}
	if et := events[2].Type; et != types.EventToolRun {
		t.Fatalf("events[2].Type = %s, want %s", et, types.EventToolRun)
	}
	if et := events[3].Type; et != types.EventToolEnd {
		t.Fatalf("events[3].Type = %s, want %s", et, types.EventToolEnd)
	}
	if events[3].ToolResult == nil {
		t.Fatal("events[3].ToolResult is nil")
	}
	if !events[3].ToolResult.IsError {
		t.Error("ToolResult.IsError = false, want true")
	}
	if !strings.Contains(events[3].ToolResult.DisplayOutput, "summarization unavailable") {
		t.Errorf("ToolResult.DisplayOutput = %q, want substring \"summarization unavailable\"",
			events[3].ToolResult.DisplayOutput)
	}
	if et := events[4].Type; et != types.EventQueryEnd {
		t.Fatalf("events[4].Type = %s, want %s", et, types.EventQueryEnd)
	}
	if events[4].Error == nil {
		t.Error("QueryEnd.Error = nil, want error")
	}
	if len(ec.FindEvents(types.EventToolParamDelta)) != 0 {
		t.Error("no EventToolParamDelta expected on error path")
	}
}

// TestEngine_ManualCompact_NilCompactor_NoEvents verifies the comp == nil
// guard returns before any event emission.
func TestEngine_ManualCompact_NilCompactor_NoEvents(t *testing.T) {
	t.Parallel()

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   &testProvider{},
		Dispatcher: ec,
		Model:      "test-model",
	})
	t.Cleanup(func() { eng.Close() })

	_, err := eng.ManualCompact(context.Background(), compactUserMsg(), "")
	if err == nil {
		t.Fatal("expected error when compactor is nil")
	}
	if !strings.Contains(err.Error(), "compaction not configured") {
		t.Errorf("error = %v, want substring \"compaction not configured\"", err)
	}
	if n := len(ec.Events()); n != 0 {
		t.Errorf("expected zero events emitted when compactor is nil, got %d: %+v", n, eventTypes(ec.Events()))
	}
}

func toolUseIDOrEmpty(tu *types.ToolUseEvent) string {
	if tu == nil {
		return ""
	}
	return tu.ID
}

func toolUseSummaryOrEmpty(tu *types.ToolUseEvent) string {
	if tu == nil {
		return ""
	}
	return tu.Summary
}
