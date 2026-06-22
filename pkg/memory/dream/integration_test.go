package dream

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// setupIntegrationStore creates a real SQLite store in a temp directory.
func setupIntegrationStore(t *testing.T) *short.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// createSessions inserts n sessions into the store for the given projectDir.
func createSessions(t *testing.T, store *short.Store, projectDir string, n int) []string {
	t.Helper()
	var ids []string
	for range n {
		s, err := store.CreateSession(projectDir, "test-model")
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		ids = append(ids, s.SessionID)
	}
	return ids
}

// setLockAge creates a lock file with mtime set to duration ago.
func setLockAge(t *testing.T, memoryDir string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(memoryDir, lockFileName)
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-age) // REAL-TIME: set lock file age
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
}

// waitForEvents polls until dispatcher has at least n events or deadline.
func waitForEvents(t *testing.T, d *mockDispatcher, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)              // REAL-TIME: polling deadline
	for time.Now().Before(deadline) && len(d.Events()) < n { // REAL-TIME: poll condition
		time.Sleep(10 * time.Millisecond) // REAL-TIME: polling for async event dispatch
	}
}

// ---------------------------------------------------------------------------
// Cold start: no lock file, empty DB
// ---------------------------------------------------------------------------

func TestIntegration_ColdStart_EmptyDB(t *testing.T) {
	store := setupIntegrationStore(t)
	memoryDir := t.TempDir()
	dispatcher := &mockDispatcher{}

	var runCalled bool
	runFn := func(ctx context.Context, prompt string) error {
		runCalled = true
		return nil
	}

	mgr := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, "/project", &fakeEngine{sid: "current-sid"},
		store, runFn, dispatcher, slog.Default())

	mgr.RunPostTurn(context.Background(), nil, 0, "")

	if runCalled {
		t.Error("DreamRunFn should not be called with empty DB")
	}
	if len(dispatcher.Events()) != 0 {
		t.Errorf("expected 0 events, got %d", len(dispatcher.Events()))
	}
	// No lock file should exist
	if _, err := os.Stat(filepath.Join(memoryDir, lockFileName)); !os.IsNotExist(err) {
		t.Error("lock file should not exist after cold start skip")
	}
}

// ---------------------------------------------------------------------------
// Hot path: full cycle — fire → consolidate → record → skip second run
// ---------------------------------------------------------------------------

func TestIntegration_HotPath_FullCycle(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	// Insert 6 sessions (real SQLite)
	sessions := createSessions(t, store, projectDir, 6)
	currentSID := sessions[0] // excluded from session gate

	// Lock file mtime = 25h ago → past MinHours=24
	setLockAge(t, memoryDir, 25*time.Hour)

	var capturedPrompt string
	dispatcher := &mockDispatcher{}
	runFn := func(ctx context.Context, prompt string) error {
		capturedPrompt = prompt
		return nil
	}

	mgr := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, projectDir, &fakeEngine{sid: currentSID},
		store, runFn, dispatcher, slog.Default())

	// Fire
	mgr.RunPostTurn(context.Background(), nil, 0, "")
	waitForEvents(t, dispatcher, 3)

	// 1. DreamRunFn was called with session IDs in prompt
	if capturedPrompt == "" {
		t.Fatal("DreamRunFn was not called")
	}
	for _, id := range sessions[1:] {
		if !strings.Contains(capturedPrompt, id) {
			t.Errorf("prompt should contain session ID %s", id)
		}
	}

	// 2. Virtual tool events: Start → Run → End
	events := dispatcher.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Type != "tool_start" || events[0].ToolUse.Name != "Dream" {
		t.Errorf("event[0] should be tool_start/Dream, got %v/%v", events[0].Type, events[0].ToolUse)
	}
	if events[1].Type != "tool_run" {
		t.Errorf("event[1] should be tool_run, got %v", events[1].Type)
	}
	if events[2].Type != "tool_end" {
		t.Errorf("event[2] should be tool_end, got %v", events[2].Type)
	}
	if !strings.Contains(events[2].ToolResult.DisplayOutput, "complete") {
		t.Errorf("ToolEnd should show completion, got: %s", events[2].ToolResult.DisplayOutput)
	}

	// 3. RecordConsolidation updated lock mtime to recent
	lockPath := filepath.Join(memoryDir, lockFileName)
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file should exist: %v", err)
	}
	if time.Since(info.ModTime()) > 5*time.Second {
		t.Errorf("lock mtime should be recent after RecordConsolidation, got %v", info.ModTime())
	}

	// 4. Second run should skip — 24h not elapsed since consolidation
	dispatcher2 := &mockDispatcher{}
	mgr2 := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, projectDir, &fakeEngine{sid: currentSID},
		store, runFn, dispatcher2, slog.Default())
	mgr2.RunPostTurn(context.Background(), nil, 0, "")

	if len(dispatcher2.Events()) != 0 {
		t.Error("second run should not fire — 24h not elapsed since consolidation")
	}
}

// ---------------------------------------------------------------------------
// Recovery: stale lock with dead PID → reclaim and run
// ---------------------------------------------------------------------------

func TestIntegration_Recovery_StaleLock(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	createSessions(t, store, projectDir, 6)

	// Create lock with dead PID and mtime 25h ago
	lockPath := filepath.Join(memoryDir, lockFileName)
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	deadPID := 999999
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-25 * time.Hour) // REAL-TIME: stale lock timestamp
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	var runCalled bool
	dispatcher := &mockDispatcher{}
	runFn := func(ctx context.Context, prompt string) error {
		runCalled = true
		return nil
	}

	mgr := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, projectDir, &fakeEngine{sid: "current-sid"},
		store, runFn, dispatcher, slog.Default())

	mgr.RunPostTurn(context.Background(), nil, 0, "")
	waitForEvents(t, dispatcher, 3)

	if !runCalled {
		t.Error("should reclaim stale lock and run dream")
	}
}

// ---------------------------------------------------------------------------
// Recovery: failed consolidation → rollback → retry succeeds
// ---------------------------------------------------------------------------

func TestIntegration_Recovery_FailedConsolidation(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	createSessions(t, store, projectDir, 6)
	setLockAge(t, memoryDir, 25*time.Hour)

	priorTime := time.Now().Add(-25 * time.Hour) // REAL-TIME: prior lock timestamp

	dispatcher := &mockDispatcher{}
	runFn := func(ctx context.Context, prompt string) error {
		return fmt.Errorf("consolidation crashed")
	}

	mgr := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, projectDir, &fakeEngine{sid: "current-sid"},
		store, runFn, dispatcher, slog.Default())

	mgr.RunPostTurn(context.Background(), nil, 0, "")
	waitForEvents(t, dispatcher, 3)

	// 1. Lock should be rolled back (mtime rewound to ~priorTime)
	lockPath := filepath.Join(memoryDir, lockFileName)
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file should exist after rollback: %v", err)
	}
	gotMtime := info.ModTime()
	if gotMtime.Sub(priorTime).Abs() > 2*time.Second {
		t.Errorf("rollback should rewind mtime to ~%v, got %v", priorTime, gotMtime)
	}

	// 2. ToolEnd shows failure
	events := dispatcher.Events()
	if len(events) < 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if !strings.Contains(events[2].ToolResult.DisplayOutput, "failed") {
		t.Errorf("ToolEnd should show failure, got: %s", events[2].ToolResult.DisplayOutput)
	}
}

// ---------------------------------------------------------------------------
// Recovery: process restart — new Manager reads lock from previous run
// ---------------------------------------------------------------------------

func TestIntegration_Recovery_ProcessRestart(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	createSessions(t, store, projectDir, 6)
	setLockAge(t, memoryDir, 25*time.Hour)

	// Process 1: run dream successfully
	dispatcher1 := &mockDispatcher{}
	mgr1 := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, projectDir, &fakeEngine{sid: "current-sid"},
		store, func(ctx context.Context, prompt string) error { return nil },
		dispatcher1, slog.Default())
	mgr1.RunPostTurn(context.Background(), nil, 0, "")
	waitForEvents(t, dispatcher1, 3)

	// Process 2: new Manager instance = simulated process restart
	dispatcher2 := &mockDispatcher{}
	var runCalled bool
	mgr2 := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, projectDir, &fakeEngine{sid: "current-sid"},
		store, func(ctx context.Context, prompt string) error {
			runCalled = true
			return nil
		},
		dispatcher2, slog.Default())
	mgr2.RunPostTurn(context.Background(), nil, 0, "")

	if runCalled {
		t.Error("new instance should skip — 24h not elapsed since RecordConsolidation")
	}
}

// ---------------------------------------------------------------------------
// Recovery: ctx already cancelled → abort before goroutine launch
// ---------------------------------------------------------------------------

func TestIntegration_CtxCancellation(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	createSessions(t, store, projectDir, 6)
	setLockAge(t, memoryDir, 25*time.Hour)

	var runCalled bool
	dispatcher := &mockDispatcher{}
	runFn := func(ctx context.Context, prompt string) error {
		runCalled = true
		return nil
	}

	mgr := NewManager(Config{MinHours: 24, MinSessions: 5},
		memoryDir, projectDir, &fakeEngine{sid: "current-sid"},
		store, runFn, dispatcher, slog.Default())

	// Cancel context before calling RunPostTurn
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mgr.RunPostTurn(ctx, nil, 0, "")

	// Give a brief window for any goroutine to launch
	time.Sleep(100 * time.Millisecond) // REAL-TIME: brief window for goroutine launch

	if runCalled {
		t.Error("DreamRunFn should not be called when context is already cancelled")
	}
	if len(dispatcher.Events()) != 0 {
		t.Errorf("expected 0 events on cancelled ctx, got %d", len(dispatcher.Events()))
	}

	// Lock should be rolled back (ctx cancel triggers rollback in RunPostTurn)
	lockPath := filepath.Join(memoryDir, lockFileName)
	// The lock file should still exist but be rolled back to prior mtime
	// Since there was no prior lock (setLockAge created it), rollback unlinks it
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		// Prior lock existed (setLockAge), so rollback rewinds mtime — file still exists
		// Verify mtime is old (not recent) — proving rollback happened
		info, statErr := os.Stat(lockPath)
		if statErr == nil && time.Since(info.ModTime()) < 1*time.Hour {
			t.Error("lock mtime should be rewound (old), not recent — rollback didn't happen")
		}
	}
}
