package dream

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// mockSessionLister implements SessionLister for testing.
type mockSessionLister struct {
	ids []string
	err error
}

func (m *mockSessionLister) SessionsTouchedSince(projectDir string, since time.Time, excludeSID string) ([]string, error) {
	return m.ids, m.err
}

// mockDispatcher captures dispatched events.
type mockDispatcher struct {
	mu     sync.Mutex
	events []types.QueryEvent
	done   chan struct{} // closed when len(events) reaches target
	target int          // event count to close done
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
	m := NewManager(Config{MinHours: 24, MinSessions: 5}, tmpDir, "/project", "sid",
		&mockSessionLister{}, nil, &mockDispatcher{}, slog.Default())
	should, _, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when disabled")
	}
}

func TestShouldDream_TimeGateNotElapsed(t *testing.T) {
	tmpDir := t.TempDir()

	// Create lock file with recent mtime (< 24h ago)
	lockPath := filepath.Join(tmpDir, lockFileName)
	if err := os.WriteFile(lockPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-1 * time.Hour) // REAL-TIME: testing time-based gate behavior
	if err := os.Chtimes(lockPath, recent, recent); err != nil {
		t.Fatal(err)
	}

	m := NewManager(Config{MinHours: 24, MinSessions: 5}, tmpDir, "/project", "sid",
		&mockSessionLister{}, nil, &mockDispatcher{}, slog.Default())
	should, _, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when time gate not elapsed")
	}
}

func TestShouldDream_ScanThrottle(t *testing.T) {
	tmpDir := t.TempDir()

	// No lock file → time gate passes (0 mtime = very old)
	m := NewManager(Config{MinHours: 24, MinSessions: 5}, tmpDir, "/project", "sid",
		&mockSessionLister{}, nil, &mockDispatcher{}, slog.Default())

	// First call sets lastScanAt
	m.lastScanAt = time.Now() // REAL-TIME: testing scan throttle behavior

	should, _, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when scan throttle active")
	}
}

func TestShouldDream_SessionGateTooFew(t *testing.T) {
	tmpDir := t.TempDir()
	// No lock file → time gate passes

	lister := &mockSessionLister{ids: []string{"s1", "s2"}} // only 2, need 5
	m := NewManager(Config{MinHours: 24, MinSessions: 5}, tmpDir, "/project", "sid",
		lister, nil, &mockDispatcher{}, slog.Default())

	// Set lastScanAt far enough back
	m.lastScanAt = time.Now().Add(-15 * time.Minute) // REAL-TIME: testing scan throttle bypass

	should, _, _, _ := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false when too few sessions")
	}
}

func TestShouldDream_AllGatesPass(t *testing.T) {
	tmpDir := t.TempDir()
	// No lock file → time gate passes (lastConsolidatedAt = 0 = epoch)

	lister := &mockSessionLister{ids: []string{"s1", "s2", "s3", "s4", "s5", "s6"}}
	m := NewManager(Config{MinHours: 24, MinSessions: 5}, tmpDir, "/project", "sid",
		lister, nil, &mockDispatcher{}, slog.Default())

	// Set lastScanAt far enough back
	m.lastScanAt = time.Now().Add(-15 * time.Minute) // REAL-TIME: testing scan throttle bypass

	should, ids, priorMtime, err := m.ShouldDream(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !should {
		t.Error("ShouldDream should return true when all gates pass")
	}
	if len(ids) != 6 {
		t.Errorf("expected 6 session IDs, got %d", len(ids))
	}
	if priorMtime != 0 {
		t.Errorf("priorMtime = %d, want 0 for no prior lock", priorMtime)
	}
}

func TestShouldDream_LockHeldByLivePID(t *testing.T) {
	tmpDir := t.TempDir()

	// Manager A acquires the lock first (MinHours=0, MinSessions=0 bypass gates 2-4)
	listerA := &mockSessionLister{ids: []string{"s1"}}
	mgrA := NewManager(Config{MinHours: 0, MinSessions: 0}, tmpDir, "/project", "sid-a",
		listerA, nil, &mockDispatcher{}, slog.Default())
	mgrA.lastScanAt = time.Now().Add(-15 * time.Minute) // REAL-TIME: testing scan throttle bypass

	should, _, priorMtime, err := mgrA.ShouldDream(context.Background())
	if err != nil {
		t.Fatalf("manager A should acquire: %v", err)
	}
	if !should {
		t.Fatal("manager A should succeed — no prior lock")
	}

	// Manager B tries — should fail because A holds the lock with our live PID
	listerB := &mockSessionLister{ids: []string{"s1", "s2", "s3", "s4", "s5"}}
	mgrB := NewManager(Config{MinHours: 0, MinSessions: 0}, tmpDir, "/project", "sid-b",
		listerB, nil, &mockDispatcher{}, slog.Default())
	mgrB.lastScanAt = time.Now().Add(-15 * time.Minute) // REAL-TIME: testing scan throttle bypass

	shouldB, _, _, err := mgrB.ShouldDream(context.Background())
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

	m := NewManager(DefaultConfig(), tmpDir, "/project", "sid",
		&mockSessionLister{}, runFn, dispatcher, slog.Default())

	m.Execute(context.Background(), []string{"s1", "s2"}, 0)

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

	m := NewManager(DefaultConfig(), tmpDir, "/project", "sid",
		&mockSessionLister{}, runFn, dispatcher, slog.Default())

	m.Execute(context.Background(), []string{"s1"}, priorMtime)

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

// --- RunPostTurn tests ---

func TestRunPostTurn_OnlyMainThread(t *testing.T) {
	m := NewManager(DefaultConfig(), t.TempDir(), "/project", "sid",
		&mockSessionLister{}, nil, &mockDispatcher{}, nil)

	// ShouldDream should not be called for non-main thread
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

	m := NewManager(Config{MinHours: 0, MinSessions: 0}, tmpDir, "/project", "sid",
		&mockSessionLister{ids: []string{"s1"}}, nil, dispatcher, slog.Default())

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

	// No lock file → time passes, sessions enough, lock acquires
	m := NewManager(Config{MinHours: 0, MinSessions: 1}, tmpDir, "/project", "sid",
		&mockSessionLister{ids: []string{"s1"}}, runFn, dispatcher, slog.Default())
	m.lastScanAt = time.Now().Add(-15 * time.Minute) // REAL-TIME: testing scan throttle bypass

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

	lister := &mockSessionLister{ids: []string{"s1", "s2", "s3", "s4", "s5"}}
	m := NewManager(Config{MinHours: 0, MinSessions: 5}, tmpDir, "/project", "sid",
		lister, runFn, dispatcher, slog.Default())

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

	// Verify prompt contains consolidation phases
	if !strings.Contains(capturedPrompt, "Phase 1") {
		t.Error("prompt should contain Phase 1")
	}
	if !strings.Contains(capturedPrompt, "s1") {
		t.Error("prompt should contain session IDs")
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

// lockErrorLister changes lock file permissions when called, causing TryAcquire to fail.
type lockErrorLister struct {
	ids     []string
	memDir  string
	changed bool
}

func (l *lockErrorLister) SessionsTouchedSince(projectDir string, since time.Time, excludeSID string) ([]string, error) {
	if !l.changed {
		lockPath := filepath.Join(l.memDir, lockFileName)
		_ = os.Chmod(lockPath, 0o444) // make read-only so WriteFile fails in TryAcquire
		l.changed = true
	}
	return l.ids, nil
}

func TestShouldDream_ReadLastConsolidatedAtError(t *testing.T) {
	tmpDir := t.TempDir()

	// memoryDir is a regular file → stat of lock file under it fails with ENOTDIR
	memDir := filepath.Join(tmpDir, "notadir")
	if err := os.WriteFile(memDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(Config{MinHours: 24, MinSessions: 5}, memDir, "/project", "sid",
		&mockSessionLister{}, nil, &mockDispatcher{}, slog.Default())
	should, _, _, err := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false on stat error")
	}
	if err != nil {
		t.Errorf("ShouldDream should swallow error, got: %v", err)
	}
}

func TestShouldDream_SessionsTouchedSinceError(t *testing.T) {
	tmpDir := t.TempDir()
	// No lock file → time gate passes (lastConsolidatedAt = 0 = epoch)

	lister := &mockSessionLister{err: errors.New("db connection lost")}
	m := NewManager(Config{MinHours: 24, MinSessions: 5}, tmpDir, "/project", "sid",
		lister, nil, &mockDispatcher{}, slog.Default())
	m.lastScanAt = time.Now().Add(-15 * time.Minute) // REAL-TIME: testing scan throttle bypass

	should, _, _, err := m.ShouldDream(context.Background())
	if should {
		t.Error("ShouldDream should return false on SessionsTouchedSince error")
	}
	if err != nil {
		t.Errorf("ShouldDream should swallow error, got: %v", err)
	}
}

func TestShouldDream_LockAcquireError(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockFileName)

	// Create lock file old enough to pass the 24-hour time gate (Gate 2)
	// and stale enough for TryAcquire to reclaim (>1 hour)
	oldTime := time.Now().Add(-25 * time.Hour) // REAL-TIME: must exceed MinHours
	if err := os.WriteFile(lockPath, []byte("99999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Lister chmods lock file to read-only → TryAcquire WriteFile fails
	lister := &lockErrorLister{
		ids:    []string{"s1", "s2", "s3", "s4", "s5", "s6"},
		memDir: tmpDir,
	}
	m := NewManager(Config{MinHours: 24, MinSessions: 5}, tmpDir, "/project", "sid",
		lister, nil, &mockDispatcher{}, slog.Default())
	m.lastScanAt = time.Now().Add(-15 * time.Minute) // REAL-TIME: testing scan throttle bypass

	should, _, _, err := m.ShouldDream(context.Background())
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

	m := NewManager(DefaultConfig(), tmpDir, "/project", "sid",
		&mockSessionLister{}, runFn, dispatcher, slog.Default())

	// Should not crash — deferred recovery catches the panic
	m.Execute(context.Background(), []string{"s1"}, priorTime.UnixMilli())

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
