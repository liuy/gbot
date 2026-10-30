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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
	"uuid"
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
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("old reply")}},
	})

	result, err := eng.ManualCompact(context.Background(), compactUserMsg(), "")
	if err != nil {
		t.Fatalf("ManualCompact error: %v", err)
	}
	if result == nil {
		t.Fatal("ManualCompact returned nil result")
	}

	// EventUsage must be emitted so WUI can update header context size.
	usages := ec.FindEvents(types.EventUsage)
	if len(usages) != 1 {
		t.Fatalf("usage events = %d, want 1", len(usages))
	}
	if usages[0].Usage == nil || usages[0].Usage.InputTokens != 1500 {
		t.Errorf("usage InputTokens = %+v, want 1500", usages[0].Usage)
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
	// contextWindow=10000 → keep budget = max(min(2000,30000),2000) = 2000 tokens;
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
	storeMsgs := short.EngineMessagesToStore(msgs)
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

	compactor := NewAutoCompactor(&testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 10000, provider: p})
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

	compactor := NewAutoCompactor(&testEngineMeta{model: "test-model", sessionID: sess.SessionID, contextWindow: 1000, provider: p})
	eng := New(&Params{
		Provider:  p,
		Model:     "test-model",
		Compactor: compactor,
		Logger:    slog.Default(),
	})
	eng.SetStore(store, tmpDir)
	eng.SetSessionID(sess.SessionID)
	// Exceed findKeepFrom tail budget (2000 tokens; 20 × ~120 store-estimated)
	// so compact actually runs.
	eng.SetMessages(makeLargeMessages(20, 900))

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
// auto-compact path. The tool ID must use the compact-manual-
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
	wantCount := 7
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

	if et := events[5].Type; et != types.EventUsage {
		t.Fatalf("events[5].Type = %s, want %s", et, types.EventUsage)
	}
	if events[5].Usage == nil || events[5].Usage.InputTokens != 1500 {
		t.Errorf("events[5].Usage = %+v, want InputTokens=1500", events[5].Usage)
	}

	if et := events[6].Type; et != types.EventQueryEnd {
		t.Fatalf("events[6].Type = %s, want %s", et, types.EventQueryEnd)
	}
	if events[6].Error != nil {
		t.Errorf("QueryEnd.Error = %v, want nil", events[6].Error)
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

// blockingCompactFixture wires a funcCompactor whose fn signals entry and
// then parks on release, holding ManualCompact exactly inside its LLM-call
// window. enteredCount counts fn invocations so tests can assert the
// compactor ran exactly once.
type blockingCompactFixture struct {
	eng          *Engine
	mp           *mockProvider
	ec           *eventCollector
	entered      chan struct{}
	enteredCount *atomic.Int32
	releaseFn    func()
}

func newBlockingCompactFixture(t *testing.T, result func() (*short.CompactResult, error)) *blockingCompactFixture {
	t.Helper()
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "post-compact answer"), nil)
	entered := make(chan struct{})
	var enteredCount atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFn)
	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			if enteredCount.Add(1) == 1 {
				close(entered)
			}
			<-release
			return result()
		},
	}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 100000})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old message 1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("old reply")}},
	})
	return &blockingCompactFixture{eng: eng, mp: mp, ec: ec, entered: entered, enteredCount: &enteredCount, releaseFn: releaseFn}
}

func twoMessageCompactResult() (*short.CompactResult, error) {
	return &short.CompactResult{
		BeforeTokens:   5000,
		AfterTokens:    1500,
		BeforeMessages: 2,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("boundary")}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary-of-history")}},
		},
		Summary: "<summary>compacted</summary>",
	}, nil
}

// TestManualCompact_MarksEngineBusy_AndRejectsSecondCompact pins the busy
// lifecycle: IsBusy is true for the whole compactor call, a second compact is
// rejected with the busy error without re-entering the compactor, and the
// flag clears when the compact finishes.
func TestManualCompact_MarksEngineBusy_AndRejectsSecondCompact(t *testing.T) {
	fix := newBlockingCompactFixture(t, twoMessageCompactResult)

	done := make(chan error, 1)
	go func() {
		_, err := fix.eng.ManualCompact(context.Background(), compactUserMsg(), "")
		done <- err
	}()
	select {
	case <-fix.entered:
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("compactor never entered")
	}

	if !fix.eng.IsBusy() {
		t.Fatal("IsBusy() = false while manual compact is in flight")
	}

	// Async shape: without the guard the second call blocks inside fn on
	// release, so a synchronous call would hang the package past all
	// assertions. The select bound converts that into a clear failure.
	done2 := make(chan error, 1)
	go func() {
		_, err := fix.eng.ManualCompact(context.Background(), compactUserMsg(), "")
		done2 <- err
	}()
	select {
	case err := <-done2:
		if err == nil || !strings.Contains(err.Error(), "engine: busy") {
			t.Fatalf("second ManualCompact err = %v, want \"engine: busy\"", err)
		}
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("second compact not rejected")
	}
	if got := fix.enteredCount.Load(); got != 1 {
		t.Errorf("compactor entered %d times, want 1", got)
	}

	fix.releaseFn()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first ManualCompact error: %v", err)
		}
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("first compact never finished")
	}
	if fix.eng.IsBusy() {
		t.Error("IsBusy() = true after compact finished, want false")
	}
}

// TestManualCompact_MessageDuringCompact_QueuesThenFlushes is the core race
// test: a message enqueued while ManualCompact rewrites history must stay in
// the queue (no concurrent turn against pre-compact history) and flush
// against post-compact history once the compact ends.
func TestManualCompact_MessageDuringCompact_QueuesThenFlushes(t *testing.T) {
	fix := newBlockingCompactFixture(t, twoMessageCompactResult)
	// startProcessAttachmentsIfIdle early-returns on an empty system prompt,
	// so the flush kick needs one — same constraint as the abort-path flush.
	fix.eng.SetSystemPrompt("sys")

	done := make(chan error, 1)
	go func() {
		_, err := fix.eng.ManualCompact(context.Background(), compactUserMsg(), "")
		done <- err
	}()
	<-fix.entered
	if !fix.eng.IsBusy() {
		t.Fatal("IsBusy() = false while manual compact is in flight")
	}
	fix.eng.EnqueueAttachment(types.QueuedItem{
		Value:     "urgent follow-up",
		Mode:      types.ItemModePrompt,
		UUID:      uuid.New().String(),
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})
	// No concurrent turn: any provider call within the hold window means the
	// enqueue-time kick spawned a turn loop during the compact rewrite.
	holdDeadline := time.Now().Add(300 * time.Millisecond) // REAL-TIME
	for time.Now().Before(holdDeadline) {
		if got := fix.mp.callCount(); got != 0 {
			t.Fatalf("LLM call count mid-compact = %d, want 0 (turn loop raced the history rewrite)", got)
		}
		if got := fix.eng.AttachmentsLen(); got != 1 {
			t.Fatalf("attachment queue len mid-compact = %d, want 1", got)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}

	fix.releaseFn()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ManualCompact error: %v", err)
		}
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("compact never finished")
	}
	// The endCompact kick must flush the queued item: one turn against
	// post-compact history. QueryEnd #1 is the compact's, #2 the flush turn's.
	if !fix.ec.WaitForEventCount(types.EventQueryEnd, 2, 5*time.Second) {
		t.Fatal("queued message never flushed after compact (endCompact kick missing)")
	}
	if got := fix.mp.callCount(); got != 1 {
		t.Fatalf("LLM call count after flush = %d, want 1", got)
	}

	// The flush turn must have seen post-compact history plus the new
	// message, not the pre-compact transcript it would have raced.
	reqText := flattenText(fix.mp.lastRequestMessages())
	if !strings.Contains(reqText, "summary-of-history") {
		t.Error("flush request does not contain the compact summary — ran against pre-compact history")
	}
	if !strings.Contains(reqText, "urgent follow-up") {
		t.Error("flush request does not contain the queued message")
	}
	if strings.Contains(reqText, "old message 1") {
		t.Error("flush request contains pre-compact history — rewrite lost")
	}

	msgs := fix.eng.Messages()
	if flat := flattenText(msgs); strings.Contains(flat, "old message 1") {
		t.Error("post-compact history still contains pre-compact messages")
	}
	// The idle drain appends the queued item as a plain attachment user
	// message (createAttachmentMessages), not the mid-turn reminder envelope.
	queuedIdx := findQueuedUserMessages(msgs, "urgent follow-up")
	if len(queuedIdx) != 1 {
		t.Fatalf("history contains %d queued-message rows, want 1", len(queuedIdx))
	}
	hasAssistantAfter := false
	for i := queuedIdx[0] + 1; i < len(msgs); i++ {
		if msgs[i].Role == types.RoleAssistant {
			hasAssistantAfter = true
		}
	}
	if !hasAssistantAfter {
		t.Error("no assistant message after the queued message — flush turn did not run")
	}
	// runTurns emits its terminal QueryEnd before processAttachments' defer
	// clears queryActive, so poll for the idle mark instead of reading once.
	idleDeadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for fix.eng.IsBusy() {
		if time.Now().After(idleDeadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatal("IsBusy() stayed true after compact and flush finished")
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	if n := fix.eng.AttachmentsLen(); n != 0 {
		t.Errorf("attachment queue len after flush = %d, want 0", n)
	}
}

// TestManualCompact_RejectedWhileQueryActive pins the guard's other side: a
// manual compact attempted while a query is streaming is rejected before any
// event emission or history rewrite, and the query finishes unaffected.
func TestManualCompact_RejectedWhileQueryActive(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "streaming answer"), nil)
	gated := &streamGateProvider{inner: mp, gate: make(chan struct{})}
	var gateOnce sync.Once
	closeGate := func() { gateOnce.Do(func() { close(gated.gate) }) }
	t.Cleanup(closeGate)

	var compactCalls atomic.Int32
	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			compactCalls.Add(1)
			return &short.CompactResult{
				BeforeTokens: 100,
				AfterTokens:  50,
				Messages: []types.Message{
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary")}},
				},
				Summary: "ok",
			}, nil
		},
	}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   gated,
		Dispatcher: ec,
		Model:      "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{ContextWindow: 100000})
	eng.Query(context.Background(), "hello", "sys")
	// Wait until the gated stream is in flight (held open by the gate).
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for {
		gated.mu.Lock()
		calls := gated.calls
		gated.mu.Unlock()
		if calls == 1 {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME
			closeGate()
			t.Fatal("first LLM stream never started")
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}

	_, err := eng.ManualCompact(context.Background(), compactUserMsg(), "")
	if err == nil || !strings.Contains(err.Error(), "engine: busy") {
		t.Fatalf("ManualCompact during active query: err = %v, want \"engine: busy\"", err)
	}
	if n := compactCalls.Load(); n != 0 {
		t.Errorf("compactor ran %d times during an active query, want 0", n)
	}
	for _, ev := range ec.FindEvents(types.EventToolStart) {
		if ev.ToolUse != nil && ev.ToolUse.Name == "Compact" {
			t.Error("Compact ToolStart emitted during an active query — guard must fire before any event")
		}
	}

	closeGate()
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error after rejected compact: %v", res.Error)
	}
}

// TestManualCompact_FailureClearsBusyAndFlushesQueue pins the error path:
// the defer clears the compact-busy mark and still kicks the queued-item
// flush, and the failed compact leaves history untouched.
func TestManualCompact_FailureClearsBusyAndFlushesQueue(t *testing.T) {
	fix := newBlockingCompactFixture(t, func() (*short.CompactResult, error) {
		return nil, fmt.Errorf("summarizer down")
	})
	fix.eng.SetSystemPrompt("sys")

	done := make(chan error, 1)
	go func() {
		_, err := fix.eng.ManualCompact(context.Background(), compactUserMsg(), "")
		done <- err
	}()
	<-fix.entered
	if !fix.eng.IsBusy() {
		t.Fatal("IsBusy() = false while manual compact is in flight")
	}
	fix.eng.EnqueueAttachment(types.QueuedItem{
		Value:     "urgent follow-up",
		Mode:      types.ItemModePrompt,
		UUID:      uuid.New().String(),
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})
	fix.releaseFn()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "summarizer down") {
			t.Fatalf("ManualCompact err = %v, want \"summarizer down\"", err)
		}
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("compact never finished")
	}
	// The flush turn itself proves the compact flag was cleared on the error
	// path: beginQueryIfIdle refuses while compacting, so a leaked flag would
	// strand the queue instead of running this turn. QueryEnd #1 is the
	// compact's error QueryEnd, #2 the flush turn's.
	if !fix.ec.WaitForEventCount(types.EventQueryEnd, 2, 5*time.Second) {
		t.Fatal("queued message never flushed after failed compact (defer kick missing on error path)")
	}
	if got := fix.mp.callCount(); got != 1 {
		t.Fatalf("LLM call count after flush = %d, want 1", got)
	}
	// Same late-clear window as T2: poll instead of reading the flag once.
	idleDeadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for fix.eng.IsBusy() {
		if time.Now().After(idleDeadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatal("IsBusy() stayed true after failed compact and completed flush — flag leaked on the error path")
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	if got := fix.eng.AttachmentsLen(); got != 0 {
		t.Errorf("attachment queue len after flush = %d, want 0", got)
	}
	if flat := flattenText(fix.eng.Messages()); !strings.Contains(flat, "old message 1") {
		t.Error("failed compact removed pre-compact history — result must not apply on error")
	}
	if queued := findQueuedUserMessages(fix.eng.Messages(), "urgent follow-up"); len(queued) != 1 {
		t.Fatalf("history contains %d queued-message rows, want 1", len(queued))
	}
}

// findQueuedUserMessages returns the indices of user messages carrying the
// drained queued item's text. The idle-drain path appends a plain attachment
// message row, so the mid-turn reminder-envelope matcher does not apply.
func findQueuedUserMessages(msgs []types.Message, text string) []int {
	var out []int
	for i, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeText && strings.Contains(cb.Text, text) {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

func toolUseSummaryOrEmpty(tu *types.ToolUseEvent) string {
	if tu == nil {
		return ""
	}
	return tu.Summary
}
