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

// mockIntegrationEngine captures Query prompts for assertion.
type mockIntegrationEngine struct {
	mu       sync.Mutex
	busy     bool
	queryErr error
	prompts  []string
}

func (m *mockIntegrationEngine) IsBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

func (m *mockIntegrationEngine) Query(_ context.Context, prompt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, prompt)
	return m.queryErr
}

func (m *mockIntegrationEngine) SessionID() string {
	return "mock-integration-session"
}

func (m *mockIntegrationEngine) capturedPrompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// --- Cold start: fresh DB, no watermark, all conditions met ---

func TestIntegration_ColdStart(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	// Insert assistant message 2h ago → idle gate passes
	insertAssistantMessage(t, store, projectDir, time.Now().Add(-2*time.Hour)) // REAL-TIME: idle gate test

	eng := &mockIntegrationEngine{}
	p := TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		IdleThreshold: 1 * time.Hour,
		Cooldown:      12 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	prompts := eng.capturedPrompts()
	if len(prompts) != 1 {
		t.Fatalf("expected 1 Query call, got %d", len(prompts))
	}
	// The prompt is the consolidation prompt — no message data is injected.
	// It should NOT contain the conversation text.
	if strings.Contains(prompts[0], "test user likes blue") {
		t.Errorf("prompt should not contain raw conversation text in self-driven model, got: %s", prompts[0])
	}
	if !strings.Contains(prompts[0], "Memory directory:") {
		t.Errorf("prompt should contain trigger header, got: %s", prompts[0])
	}
	if !strings.Contains(prompts[0], "Last consolidation: never") {
		t.Errorf("cold-start prompt should say 'never', got: %s", prompts[0])
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

// --- Hot path: watermark exists, new messages arrive, tick fires with lastDream time ---

func TestIntegration_HotPath_LastDreamInPrompt(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	// Old assistant message (2h ago)
	insertAssistantMessage(t, store, projectDir, time.Now().Add(-2*time.Hour)) // REAL-TIME: idle gate test

	// Watermark written 13h ago (past cooldown)
	knownTime := time.Now().Add(-13 * time.Hour) // REAL-TIME: cooldown gate test
	writeWatermarkTime(t, memoryDir, knownTime)

	eng := &mockIntegrationEngine{}
	p := TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		IdleThreshold: 1 * time.Hour,
		Cooldown:      12 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	prompts := eng.capturedPrompts()
	if len(prompts) != 1 {
		t.Fatalf("expected 1 Query call, got %d", len(prompts))
	}
	// The prompt renders the lastDream time as local wall clock + zone name
	roundTripped := time.UnixMilli(knownTime.UnixMilli())
	expected := "Last consolidation: " + roundTripped.Local().Format("2006-01-02 15:04 MST")
	if !strings.Contains(prompts[0], expected) {
		t.Errorf("prompt should contain %q, got: %s", expected, prompts[0])
	}
}

// --- Recovery: Query fails → watermark not advanced → retry succeeds ---

func TestIntegration_Recovery_FailureThenSuccess(t *testing.T) {
	store := setupIntegrationStore(t)
	projectDir := t.TempDir()
	memoryDir := t.TempDir()

	insertAssistantMessage(t, store, projectDir, time.Now().Add(-2*time.Hour)) // REAL-TIME: idle gate test

	// First tick: fail
	engFail := &mockIntegrationEngine{queryErr: errors.New("llm unavailable")}
	p := TimerParams{
		Engine:        engFail,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		IdleThreshold: 1 * time.Hour,
		Cooldown:      12 * time.Hour,
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

	if len(engSuccess.capturedPrompts()) != 1 {
		t.Fatalf("expected 1 Query on retry, got %d", len(engSuccess.capturedPrompts()))
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

	insertAssistantMessage(t, store, projectDir, time.Now().Add(-2*time.Hour)) // REAL-TIME: idle gate test

	eng := &mockIntegrationEngine{busy: true}
	p := TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   store,
		MemoryDir:     memoryDir,
		IdleThreshold: 1 * time.Hour,
		Cooldown:      12 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	if len(eng.capturedPrompts()) != 0 {
		t.Errorf("expected 0 Query calls when busy, got %d", len(eng.capturedPrompts()))
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
		IdleThreshold: 1 * time.Hour,
		Cooldown:      12 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}

	p.tick(context.Background())

	if len(eng.capturedPrompts()) != 0 {
		t.Errorf("expected 0 Query calls with no assistant message, got %d", len(eng.capturedPrompts()))
	}
}
