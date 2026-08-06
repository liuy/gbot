package dream

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
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

// insertAssistantMessage inserts a main-thread assistant message with the given
// timestamp into a new session.
func insertAssistantMessage(t *testing.T, store *short.Store, projectDir string, ts time.Time) {
	t.Helper()
	ses, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	msg := &short.TranscriptMessage{
		UUID:      "uuid-assistant-" + ts.Format("150405.000000000"),
		Type:      "assistant",
		Content:   `[{"type":"text","text":"test user likes blue"}]`,
		CreatedAt: ts,
	}
	if err := store.AppendMessage(ses.SessionID, msg); err != nil {
		t.Fatalf("append message: %v", err)
	}
}

// mockIntegrationEngine captures RunChunk messages for assertion.
type mockIntegrationEngine struct {
	mu       sync.Mutex
	busy     bool
	chunkErr error
	messages []string
}

func (m *mockIntegrationEngine) IsBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

func (m *mockIntegrationEngine) RunChunk(_ context.Context, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return m.chunkErr
}

func (m *mockIntegrationEngine) capturedMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.messages...)
}

// --- Cold start: fresh DB, no watermark, all conditions met ---

func TestIntegration_ColdStart(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	// Insert assistant message 3h ago → idle gate passes
	insertAssistantMessage(t, store, projectDir, time.Now().Add(-3*time.Hour)) // REAL-TIME: idle gate test

	eng := &mockIntegrationEngine{}
	p := TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		ContextWindow: 100000,
		IdleThreshold: 2 * time.Hour,
		Cooldown:      6 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	msgs := eng.capturedMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 RunChunk call, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0], "test user likes blue") {
		t.Errorf("chunk message should contain conversation text, got: %s", msgs[0])
	}

	// Watermark written on success
	wm, err := ReadWatermark(memoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if wm.IsZero() {
		t.Error("watermark should be written after successful cold-start consolidation")
	}
}

// --- Hot path: watermark exists, new messages arrive, tick consolidates incrementally ---

func TestIntegration_HotPath_IncrementalConsolidation(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	// Old assistant message (3h ago)
	oldMsg := time.Now().Add(-3 * time.Hour) // REAL-TIME: idle gate test
	insertAssistantMessage(t, store, projectDir, oldMsg)

	// Watermark written 5 minutes ago (well within cooldown? no — set to 7h ago)
	writeWatermarkAge(t, memoryDir, 7*time.Hour)

	eng := &mockIntegrationEngine{}
	p := TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		ContextWindow: 100000,
		IdleThreshold: 2 * time.Hour,
		Cooldown:      6 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	msgs := eng.capturedMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 RunChunk call, got %d", len(msgs))
	}
	// The message should include the conversation from after the watermark
	if !strings.Contains(msgs[0], "test user likes blue") {
		t.Errorf("chunk should contain conversation text, got: %s", msgs[0])
	}
}

// --- Recovery: consolidation fails → watermark not advanced → retry succeeds ---

func TestIntegration_Recovery_PartialFailureThenSuccess(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	insertAssistantMessage(t, store, projectDir, time.Now().Add(-3*time.Hour)) // REAL-TIME: idle gate test

	// First tick: fail
	engFail := &mockIntegrationEngine{chunkErr: errors.New("llm unavailable")}
	p := TimerParams{
		Engine:        engFail,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		ContextWindow: 100000,
		IdleThreshold: 2 * time.Hour,
		Cooldown:      6 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}
	p.tick(context.Background())

	// Watermark NOT written
	wm, err := ReadWatermark(memoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if !wm.IsZero() {
		t.Fatal("watermark should NOT be written on failure")
	}

	// Second tick: succeed (cooldown gate doesn't apply since watermark is zero)
	engSuccess := &mockIntegrationEngine{}
	p.Engine = engSuccess
	p.tick(context.Background())

	if len(engSuccess.capturedMessages()) != 1 {
		t.Fatalf("expected 1 RunChunk on retry, got %d", len(engSuccess.capturedMessages()))
	}

	// Watermark NOW written
	wm, err = ReadWatermark(memoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if wm.IsZero() {
		t.Error("watermark should be written after successful retry")
	}
}

// --- Busy engine skips consolidation ---

func TestIntegration_BusyEngineSkips(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	insertAssistantMessage(t, store, projectDir, time.Now().Add(-3*time.Hour)) // REAL-TIME: idle gate test

	eng := &mockIntegrationEngine{busy: true}
	p := TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		ContextWindow: 100000,
		IdleThreshold: 2 * time.Hour,
		Cooldown:      6 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	if len(eng.capturedMessages()) != 0 {
		t.Errorf("expected 0 RunChunk calls when busy, got %d", len(eng.capturedMessages()))
	}
}

// --- No assistant message → idle gate fails (cold DB) ---

func TestIntegration_NoAssistantMessage(t *testing.T) {
	store := setupIntegrationStore(t)
	memoryDir := t.TempDir()

	eng := &mockIntegrationEngine{}
	p := TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		ContextWindow: 100000,
		IdleThreshold: 2 * time.Hour,
		Cooldown:      6 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	if len(eng.capturedMessages()) != 0 {
		t.Errorf("expected 0 RunChunk calls with no assistant message, got %d", len(eng.capturedMessages()))
	}
}
