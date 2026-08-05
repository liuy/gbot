package dream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// mockMessageLister implements MessageLister for testing.
type mockMessageLister struct {
	msgs    []*short.TranscriptMessage
	err     error
	onCall  func() // optional callback invoked on each MessagesSince call
	callCnt int
}

func (m *mockMessageLister) MessagesSince(since time.Time) ([]*short.TranscriptMessage, error) {
	m.callCnt++
	if m.onCall != nil {
		m.onCall()
	}
	return m.msgs, m.err
}

// testTimeBase is a fixed timestamp for deterministic test data.
var testTimeBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// testMessages builds n TranscriptMessages with deterministic content.
func testMessages(n int) []*short.TranscriptMessage {
	var msgs []*short.TranscriptMessage
	for i := range n {
		msgs = append(msgs, &short.TranscriptMessage{
			Type:      "user",
			Content:   fmt.Sprintf(`[{"type":"text","text":"msg %d"}]`, i),
			CreatedAt: testTimeBase.Add(time.Duration(i) * time.Minute),
		})
	}
	return msgs
}

// mockDispatcher captures dispatched events.
type mockDispatcher struct {
	mu     sync.Mutex
	events []types.QueryEvent
	done   chan struct{} // closed when len(events) reaches target
	target int           // event count to close done
}

func (m *mockDispatcher) Dispatch(event types.QueryEvent) {
	m.mu.Lock()
	m.events = append(m.events, event)
	count := len(m.events)
	d := m.done
	target := m.target
	m.mu.Unlock()
	if d != nil && count >= target {
		select {
		case <-d:
		default:
			close(d)
		}
	}
}

func (m *mockDispatcher) Events() []types.QueryEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]types.QueryEvent(nil), m.events...)
}

// --- ShouldDream gate tests ---

func TestShouldDream_Disabled(t *testing.T) {
	t.Setenv("GBOT_AUTO_DREAM", "false")

	tmpDir := t.TempDir()
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		&mockMessageLister{}, nil, &mockDispatcher{}, slog.Default())
	should, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when disabled")
	}
}

func TestShouldDream_NotIdle(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		&mockMessageLister{}, nil, &mockDispatcher{}, slog.Default())

	// lastAssistantAt is zero → Gate 2 (idle check) fails
	should, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when lastAssistantAt is zero")
	}
}

func TestShouldDream_IdleButTooRecent(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		&mockMessageLister{}, nil, &mockDispatcher{}, slog.Default())

	// lastAssistantAt 1h ago, IdleThreshold=2h → not idle long enough
	m.lastAssistantAt = time.Now().Add(-1 * time.Hour) // REAL-TIME: testing idle gate
	should, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when idle duration < IdleThreshold")
	}
}

func TestShouldDream_NoNewMessages(t *testing.T) {
	tmpDir := t.TempDir()

	lister := &mockMessageLister{msgs: nil}
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		lister, nil, &mockDispatcher{}, slog.Default())

	m.lastAssistantAt = time.Now().Add(-3 * time.Hour) // REAL-TIME: past idle threshold

	should, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when no new messages since last consolidation")
	}
}

func TestShouldDream_CooldownNotElapsed(t *testing.T) {
	tmpDir := t.TempDir()

	// Lock file with recent mtime (< DreamCooldown=6h) → cooldown gate fails
	lockPath := filepath.Join(tmpDir, lockFileName)
	if err := os.WriteFile(lockPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-1 * time.Hour) // REAL-TIME: testing cooldown gate
	if err := os.Chtimes(lockPath, recent, recent); err != nil {
		t.Fatal(err)
	}

	lister := &mockMessageLister{msgs: testMessages(3)}
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		lister, nil, &mockDispatcher{}, slog.Default())

	m.lastAssistantAt = time.Now().Add(-3 * time.Hour) // REAL-TIME: past idle threshold

	should, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when cooldown not elapsed")
	}
}

func TestShouldDream_AllGatesPass(t *testing.T) {
	tmpDir := t.TempDir()

	lister := &mockMessageLister{msgs: testMessages(6)}
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		lister, nil, &mockDispatcher{}, slog.Default())

	m.lastAssistantAt = time.Now().Add(-3 * time.Hour) // REAL-TIME: past idle threshold

	should, priorMtime, err := m.ShouldDream(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !should {
		t.Error("ShouldDream should return true when all gates pass")
	}
	if priorMtime != 0 {
		t.Errorf("priorMtime = %d, want 0 for no prior lock", priorMtime)
	}
	if lister.callCnt != 1 {
		t.Errorf("MessagesSince should be called once, got %d", lister.callCnt)
	}
}

func TestShouldDream_LockHeldByLivePID(t *testing.T) {
	tmpDir := t.TempDir()

	// Manager A acquires the lock first (IdleThreshold=0, DreamCooldown=0 bypass gates 2+4)
	listerA := &mockMessageLister{msgs: testMessages(1)}
	mgrA := NewManager(Config{IdleThreshold: 0, DreamCooldown: 0}, tmpDir, 100000,
		listerA, nil, &mockDispatcher{}, slog.Default())
	mgrA.lastAssistantAt = time.Now() // REAL-TIME: pass idle gate (IdleThreshold=0)

	should, priorMtime, err := mgrA.ShouldDream(context.Background())
	if err != nil {
		t.Fatalf("manager A should acquire: %v", err)
	}
	if !should {
		t.Fatal("manager A should succeed — no prior lock")
	}

	// Manager B tries — should fail because A holds the lock with our live PID
	listerB := &mockMessageLister{msgs: testMessages(1)}
	mgrB := NewManager(Config{IdleThreshold: 0, DreamCooldown: 0}, tmpDir, 100000,
		listerB, nil, &mockDispatcher{}, slog.Default())
	mgrB.lastAssistantAt = time.Now() // REAL-TIME: pass idle gate

	shouldB, _, err := mgrB.ShouldDream(context.Background())
	if err != nil {
		t.Fatalf("manager B ShouldDream: %v", err)
	}
	if shouldB {
		t.Error("manager B should NOT acquire — lock held by manager A's live PID")
	}

	RollbackConsolidationLock(tmpDir, priorMtime)
}

// --- Execute tests ---

func TestExecute_EmitsVirtualToolEvents(t *testing.T) {
	tmpDir := t.TempDir()

	var runCalled atomic.Bool
	runFn := func(ctx context.Context, prompt string) error {
		runCalled.Store(true)
		return nil
	}
	dispatcher := &mockDispatcher{}

	m := NewManager(DefaultConfig(), tmpDir, 100000,
		&mockMessageLister{msgs: testMessages(2)}, runFn, dispatcher, slog.Default())

	m.Execute(context.Background(), 0)

	if !runCalled.Load() {
		t.Error("DreamRunFn should have been called")
	}

	// Should have 3 events: ToolStart, ToolRun, ToolEnd
	if len(dispatcher.Events()) != 3 {
		t.Fatalf("expected 3 events, got %d", len(dispatcher.Events()))
	}
	if dispatcher.Events()[0].Type != types.EventToolStart {
		t.Errorf("event[0] type = %v, want EventToolStart", dispatcher.Events()[0].Type)
	}
	if dispatcher.Events()[1].Type != types.EventToolRun {
		t.Errorf("event[1] type = %v, want EventToolRun", dispatcher.Events()[1].Type)
	}
	if dispatcher.Events()[2].Type != types.EventToolEnd {
		t.Errorf("event[2] type = %v, want EventToolEnd", dispatcher.Events()[2].Type)
	}

	// ToolStart should have name "Dream"
	if dispatcher.Events()[0].ToolUse == nil || dispatcher.Events()[0].ToolUse.Name != "Dream" {
		t.Error("EventToolStart missing Dream tool name")
	}
	// ToolEnd should have success output
	if dispatcher.Events()[2].ToolResult == nil || !strings.Contains(dispatcher.Events()[2].ToolResult.DisplayOutput, "complete") {
		t.Error("EventToolEnd missing completion output")
	}
}

func TestExecute_RollbackOnFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// Create lock file with known mtime for rollback
	lockPath := filepath.Join(tmpDir, lockFileName)
	priorTime := time.Now().Add(-1 * time.Hour) // REAL-TIME: testing rollback timing
	if err := os.WriteFile(lockPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, priorTime, priorTime); err != nil {
		t.Fatal(err)
	}
	priorMtime := priorTime.UnixMilli()

	runFn := func(ctx context.Context, prompt string) error {
		return context.DeadlineExceeded
	}
	dispatcher := &mockDispatcher{}

	m := NewManager(DefaultConfig(), tmpDir, 100000,
		&mockMessageLister{msgs: testMessages(1)}, runFn, dispatcher, slog.Default())

	m.Execute(context.Background(), priorMtime)

	// Verify rollback: lock file should have rewound mtime
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file should exist after rollback: %v", err)
	}
	rewoundMtime := info.ModTime()
	diff := rewoundMtime.Sub(priorTime)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("rollback should rewind mtime to ~%v, got %v", priorTime, rewoundMtime)
	}

	// ToolEnd should show failure
	if len(dispatcher.Events()) != 3 {
		t.Fatalf("expected 3 events, got %d", len(dispatcher.Events()))
	}
	if !strings.Contains(dispatcher.Events()[2].ToolResult.DisplayOutput, "failed") {
		t.Errorf("ToolEnd should show failure, got: %s", dispatcher.Events()[2].ToolResult.DisplayOutput)
	}
}

func TestExecute_PartialFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// 2 messages with enough text to create 2 chunks (small contextWindow)
	largeMsg1 := &short.TranscriptMessage{
		Type:      "user",
		Content:   `[{"type":"text","text":"` + strings.Repeat("abcdefgh", 2000) + `"}]`,
		CreatedAt: testTimeBase,
	}
	largeMsg2 := &short.TranscriptMessage{
		Type:      "assistant",
		Content:   `[{"type":"text","text":"` + strings.Repeat("xyzwuvrs", 2000) + `"}]`,
		CreatedAt: testTimeBase.Add(1 * time.Minute),
	}

	callCnt := 0
	runFn := func(ctx context.Context, prompt string) error {
		callCnt++
		if callCnt == 1 {
			return nil // first chunk succeeds
		}
		return errors.New("chunk 2 failed")
	}
	dispatcher := &mockDispatcher{}

	m := NewManager(DefaultConfig(), tmpDir, 8000, // small budget forces 2 chunks
		&mockMessageLister{msgs: []*short.TranscriptMessage{largeMsg1, largeMsg2}},
		runFn, dispatcher, slog.Default())

	m.Execute(context.Background(), 0)

	if callCnt != 2 {
		t.Fatalf("expected 2 chunk calls, got %d", callCnt)
	}
	// Partial success → Rollback (so failed chunk messages are reprocessed
	// next run). Output says "partial".
	output := dispatcher.Events()[2].ToolResult.DisplayOutput
	if !strings.Contains(output, "partial") {
		t.Errorf("expected partial output, got: %s", output)
	}
	// Verify lock was rolled back: priorMtime=0 means a fresh lock; rollback
	// unlinks it. If RecordConsolidation had been called, the file would exist.
	if _, err := os.Stat(filepath.Join(tmpDir, lockFileName)); !os.IsNotExist(err) {
		t.Error("lock file should not exist after partial rollback")
	}
}

// --- RunPostTurn tests ---

func TestRunPostTurn_OnlyMainThread(t *testing.T) {
	m := NewManager(DefaultConfig(), t.TempDir(), 100000,
		&mockMessageLister{}, nil, &mockDispatcher{}, nil)

	// RunPostTurn returns early if querySource != ""
	m.RunPostTurn(context.Background(), nil, 0, "auto_dream")
	// No crash = pass (running stays false)
	m.mu.Lock()
	stillRunning := m.running
	m.mu.Unlock()
	if stillRunning {
		t.Error("running should be false for non-main thread")
	}
}

func TestRunPostTurn_ConcurrentGuard(t *testing.T) {
	tmpDir := t.TempDir()
	dispatcher := &mockDispatcher{}

	m := NewManager(Config{IdleThreshold: 0, DreamCooldown: 0}, tmpDir, 100000,
		&mockMessageLister{msgs: testMessages(1)}, nil, dispatcher, slog.Default())

	// Simulate already running
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()

	m.RunPostTurn(context.Background(), nil, 0, "")

	// Should not dispatch any events
	if len(dispatcher.Events()) != 0 {
		t.Errorf("expected 0 events when running=true, got %d", len(dispatcher.Events()))
	}
}

func TestRunPostTurn_SetsRunningOnExecute(t *testing.T) {
	tmpDir := t.TempDir()
	dispatcher := &mockDispatcher{done: make(chan struct{}), target: 3}

	executed := make(chan struct{})
	runFn := func(ctx context.Context, prompt string) error {
		close(executed)
		return nil
	}

	m := NewManager(Config{IdleThreshold: 0, DreamCooldown: 0}, tmpDir, 100000,
		&mockMessageLister{msgs: testMessages(1)}, runFn, dispatcher, slog.Default())
	m.lastAssistantAt = time.Now() // REAL-TIME: pass idle gate (IdleThreshold=0)

	m.RunPostTurn(context.Background(), nil, 0, "")

	// Wait for all 3 virtual tool events (ToolStart, ToolRun, ToolEnd)
	select {
	case <-dispatcher.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Execute should have dispatched 3 events within 5 seconds")
	}

	// Yield to let deferred cleanup (running = false) execute
	runtime.Gosched()
	m.mu.Lock()
	stillRunning := m.running
	m.mu.Unlock()
	if stillRunning {
		t.Error("running should be false after Execute completes")
	}
}

// --- Chain test: full pipeline ---

func TestChain_FullPipeline(t *testing.T) {
	tmpDir := t.TempDir()
	dispatcher := &mockDispatcher{done: make(chan struct{}), target: 3}

	var runCalled atomic.Bool
	var capturedPrompt string
	runFn := func(ctx context.Context, prompt string) error {
		runCalled.Store(true)
		capturedPrompt = prompt
		return nil
	}

	msgs := testMessages(5)
	lister := &mockMessageLister{msgs: msgs}
	m := NewManager(Config{IdleThreshold: 0, DreamCooldown: 0}, tmpDir, 100000,
		lister, runFn, dispatcher, slog.Default())
	m.lastAssistantAt = time.Now() // REAL-TIME: pass idle gate

	// Run the full pipeline via RunPostTurn (main thread)
	m.RunPostTurn(context.Background(), nil, 0, "")

	// Wait for all 3 virtual tool events
	select {
	case <-dispatcher.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Execute should have dispatched 3 events within 5 seconds")
	}
	runtime.Gosched() // yield for deferred cleanup

	if !runCalled.Load() {
		t.Fatal("DreamRunFn should have been called")
	}

	// Verify prompt contains Step headers (not Phase)
	if !strings.Contains(capturedPrompt, "Step 1") {
		t.Error("prompt should contain Step 1")
	}
	if strings.Contains(capturedPrompt, "Phase 1") {
		t.Error("prompt should NOT contain Phase 1")
	}
	// Verify prompt contains message text from the chunk
	if !strings.Contains(capturedPrompt, "msg 0") {
		t.Error("prompt should contain message text from chunk")
	}

	// Verify lock file was created
	lockPath := filepath.Join(tmpDir, lockFileName)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("lock file should exist after consolidation: %v", err)
	}
	if len(data) == 0 {
		t.Error("lock file should contain PID")
	}

	// Verify virtual tool events were emitted
	if len(dispatcher.Events()) != 3 {
		t.Fatalf("expected 3 virtual tool events, got %d", len(dispatcher.Events()))
	}
	if dispatcher.Events()[0].ToolUse.Name != "Dream" {
		t.Errorf("tool name = %q, want Dream", dispatcher.Events()[0].ToolUse.Name)
	}
}

func TestShouldDream_ReadLastConsolidatedAtError(t *testing.T) {
	tmpDir := t.TempDir()

	// memoryDir is a regular file → stat of lock file under it fails with ENOTDIR
	memDir := filepath.Join(tmpDir, "notadir")
	if err := os.WriteFile(memDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(DefaultConfig(), memDir, 100000,
		&mockMessageLister{}, nil, &mockDispatcher{}, slog.Default())
	m.lastAssistantAt = time.Now().Add(-3 * time.Hour) // REAL-TIME: past idle threshold

	should, _, err := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false on stat error")
	}
	if err != nil {
		t.Errorf("ShouldDream should swallow error, got: %v", err)
	}
}

func TestShouldDream_MessagesSinceError(t *testing.T) {
	tmpDir := t.TempDir()

	lister := &mockMessageLister{err: errors.New("db connection lost")}
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		lister, nil, &mockDispatcher{}, slog.Default())
	m.lastAssistantAt = time.Now().Add(-3 * time.Hour) // REAL-TIME: past idle threshold

	should, _, err := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false on MessagesSince error")
	}
	if err != nil {
		t.Errorf("ShouldDream should swallow error, got: %v", err)
	}
}

func TestShouldDream_LockAcquireError(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockFileName)

	// Lock old enough for cooldown (DefaultConfig: DreamCooldown=6h) and stale
	// enough for TryAcquire to reclaim (>1 hour)
	oldTime := time.Now().Add(-25 * time.Hour) // REAL-TIME: must exceed DreamCooldown
	if err := os.WriteFile(lockPath, []byte("99999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Lister chmods lock file to read-only → TryAcquire WriteFile fails
	lister := &mockMessageLister{
		msgs: testMessages(3),
		onCall: func() {
			_ = os.Chmod(lockPath, 0o444)
		},
	}
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		lister, nil, &mockDispatcher{}, slog.Default())
	m.lastAssistantAt = time.Now().Add(-3 * time.Hour) // REAL-TIME: past idle threshold

	should, _, err := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false on lock acquire error")
	}
	if err != nil {
		t.Errorf("ShouldDream should swallow error, got: %v", err)
	}

	// Restore for cleanup
	_ = os.Chmod(lockPath, 0o644)
}

func TestExecute_PanicRecovery(t *testing.T) {
	tmpDir := t.TempDir()

	// Create lock file with known mtime for rollback
	lockPath := filepath.Join(tmpDir, lockFileName)
	priorTime := time.Now().Add(-1 * time.Hour) // REAL-TIME: testing rollback timing
	if err := os.WriteFile(lockPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, priorTime, priorTime); err != nil {
		t.Fatal(err)
	}

	runFn := func(ctx context.Context, prompt string) error {
		panic("test panic")
	}
	dispatcher := &mockDispatcher{}

	m := NewManager(DefaultConfig(), tmpDir, 100000,
		&mockMessageLister{msgs: testMessages(1)}, runFn, dispatcher, slog.Default())

	// Should not crash — deferred recovery catches the panic
	m.Execute(context.Background(), priorTime.UnixMilli())

	// Verify running was reset
	m.mu.Lock()
	stillRunning := m.running
	m.mu.Unlock()
	if stillRunning {
		t.Error("running should be false after panic recovery")
	}

	// Verify rollback occurred (mtime rewound)
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file should exist after rollback: %v", err)
	}
	diff := info.ModTime().Sub(priorTime)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("rollback should rewind mtime to ~%v, got %v", priorTime, info.ModTime())
	}
}

// --- RunPostTurn idle tracking tests ---

func TestRunPostTurn_TracksLastAssistantAt(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(Config{IdleThreshold: 2 * time.Hour, DreamCooldown: 6 * time.Hour},
		tmpDir, 100000,
		&mockMessageLister{}, nil, &mockDispatcher{}, slog.Default())

	// Pass an assistant message with a timestamp 3h ago
	oldTs := time.Now().Add(-3 * time.Hour) // REAL-TIME: testing idle tracking
	msgs := []types.Message{
		{Role: types.RoleAssistant, Timestamp: oldTs},
	}

	m.RunPostTurn(context.Background(), msgs, 0, "")

	m.mu.Lock()
	got := m.lastAssistantAt
	m.mu.Unlock()

	if !got.Equal(oldTs) {
		t.Errorf("lastAssistantAt = %v, want %v", got, oldTs)
	}
}

func TestRunPostTurn_DoesNotOverwriteNewerLastAssistantAt(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(DefaultConfig(), tmpDir, 100000,
		&mockMessageLister{}, nil, &mockDispatcher{}, slog.Default())

	// Set a recent lastAssistantAt
	recent := time.Now().Add(-30 * time.Minute) // REAL-TIME: testing idle tracking
	m.lastAssistantAt = recent

	// Pass an older assistant message
	older := time.Now().Add(-2 * time.Hour) // REAL-TIME: testing idle tracking
	msgs := []types.Message{
		{Role: types.RoleAssistant, Timestamp: older},
	}

	m.RunPostTurn(context.Background(), msgs, 0, "")

	m.mu.Lock()
	got := m.lastAssistantAt
	m.mu.Unlock()

	if !got.Equal(recent) {
		t.Errorf("lastAssistantAt = %v, want %v (should not overwrite with older)", got, recent)
	}
}

// --- chunkByTokens tests ---

func TestChunkByTokens_Empty(t *testing.T) {
	result := chunkByTokens(nil, 100000)
	if result != nil {
		t.Errorf("expected nil for empty input, got %d chunks", len(result))
	}
}

func TestChunkByTokens_SingleChunk(t *testing.T) {
	msgs := testMessages(3)
	chunks := chunkByTokens(msgs, 100000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if len(chunks[0]) != 3 {
		t.Errorf("expected 3 messages in chunk, got %d", len(chunks[0]))
	}
}

func TestChunkByTokens_MultipleChunks(t *testing.T) {
	// Each message has ~1000 chars of text = ~250 tokens
	// budget = 8000/2 = 4000 tokens = ~16 messages per chunk
	// With 50 messages, expect ~4 chunks
	var msgs []*short.TranscriptMessage
	for i := range 50 {
		msgs = append(msgs, &short.TranscriptMessage{
			Type:      "user",
			Content:   fmt.Sprintf(`[{"type":"text","text":"%s"}]`, strings.Repeat("x", 1000)),
			CreatedAt: testTimeBase.Add(time.Duration(i) * time.Minute),
		})
	}

	chunks := chunkByTokens(msgs, 8000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for 50 large messages with small budget, got %d", len(chunks))
	}

	// Verify all messages are accounted for
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	if total != 50 {
		t.Errorf("message count mismatch: %d in chunks vs 50 original", total)
	}

	// Verify message ordering preserved across chunks
	firstMsg := chunks[0][0]
	lastChunk := chunks[len(chunks)-1]
	lastMsg := lastChunk[len(lastChunk)-1]
	if firstMsg.CreatedAt.After(lastMsg.CreatedAt) {
		t.Error("messages should be in chronological order across chunks")
	}
}

func TestChunkByTokens_MinimumBudgetFloor(t *testing.T) {
	// contextWindow=1000 → budget=500, but floor at 2000
	// A single small message should fit in one chunk
	msgs := testMessages(1)
	chunks := chunkByTokens(msgs, 1000)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk with minimum budget floor, got %d", len(chunks))
	}
}

// --- formatMessages tests ---

func TestFormatMessages_Output(t *testing.T) {
	ts := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	msgs := []*short.TranscriptMessage{
		{
			Type:      "user",
			Content:   `[{"type":"text","text":"Hello world"}]`,
			CreatedAt: ts,
		},
		{
			Type:      "assistant",
			Content:   `[{"type":"text","text":"Hi there"}]`,
			CreatedAt: ts.Add(1 * time.Minute),
		},
	}

	result := formatMessages(msgs, 1, 3)

	if !strings.Contains(result, "chunk 1/3") {
		t.Error("should contain chunk number")
	}
	if !strings.Contains(result, "[user 2026-03-15 14:30] Hello world") {
		t.Errorf("missing user message line, got: %s", result)
	}
	if !strings.Contains(result, "[assistant 2026-03-15 14:31] Hi there") {
		t.Errorf("missing assistant message line, got: %s", result)
	}
}

func TestFormatMessages_EmptyText(t *testing.T) {
	ts := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	msgs := []*short.TranscriptMessage{
		{
			Type:      "user",
			Content:   `[]`, // no text blocks
			CreatedAt: ts,
		},
	}

	result := formatMessages(msgs, 1, 1)
	if !strings.Contains(result, "[user 2026-03-15 14:30]") {
		t.Error("should contain user line even with empty text")
	}
}
