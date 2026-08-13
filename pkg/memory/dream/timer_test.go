package dream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

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

// mockTimerEngine implements DreamEngine for timer tests.
type mockTimerEngine struct {
	mu       sync.Mutex
	busy     bool
	queryErr error
	prompts  []string
}

func (m *mockTimerEngine) IsBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

func (m *mockTimerEngine) Query(_ context.Context, prompt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, prompt)
	return m.queryErr
}

func (m *mockTimerEngine) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prompts)
}

func (m *mockTimerEngine) lastPrompt() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.prompts) == 0 {
		return ""
	}
	return m.prompts[len(m.prompts)-1]
}

// mockTimerIdle implements IdleQuerier.
type mockTimerIdle struct {
	lastAssistant time.Time
	err           error
}

func (m *mockTimerIdle) LastAssistantTime() (time.Time, error) {
	return m.lastAssistant, m.err
}

// mockTimerStore implements MessageLister.
type mockTimerStore struct {
	msgs []*short.TranscriptMessage
	err  error
}

func (m *mockTimerStore) MessagesSince(_ time.Time) ([]*short.TranscriptMessage, error) {
	return m.msgs, m.err
}

func baseTimerParams(t *testing.T, eng DreamEngine, store MessageLister, idle IdleQuerier) TimerParams {
	t.Helper()
	return TimerParams{
		Engine:        eng,
		Store:         store,
		IdleQuerier:   idle,
		MemoryDir:     t.TempDir(),
		IdleThreshold: 1 * time.Hour,
		Cooldown:      12 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}
}

// idleReady returns a mockIdle with lastAssistant far enough in the past to
// pass the idle gate.
func idleReady() *mockTimerIdle {
	return &mockTimerIdle{lastAssistant: time.Now().Add(-2 * time.Hour)} // REAL-TIME: testing idle gate
}

func someMessages() *mockTimerStore {
	return &mockTimerStore{msgs: testMessages(3)}
}

// --- Gate tests ---

func TestTick_BusyEngine(t *testing.T) {
	eng := &mockTimerEngine{busy: true}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls when busy, got %d", eng.callCount())
	}
}

func TestTick_NotIdle(t *testing.T) {
	eng := &mockTimerEngine{}
	idle := &mockTimerIdle{lastAssistant: time.Now().Add(-30 * time.Minute)} // REAL-TIME: testing idle gate
	p := baseTimerParams(t, eng, someMessages(), idle)
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls when not idle, got %d", eng.callCount())
	}
}

func TestTick_IdleZero(t *testing.T) {
	eng := &mockTimerEngine{}
	idle := &mockTimerIdle{lastAssistant: time.Time{}} // zero = no assistant yet
	p := baseTimerParams(t, eng, someMessages(), idle)
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls when no assistant message, got %d", eng.callCount())
	}
}

func TestTick_CooldownNotElapsed(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	writeWatermarkAge(t, p.MemoryDir, 1*time.Hour) // cooldown=12h, 1h ago → not elapsed

	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls during cooldown, got %d", eng.callCount())
	}
}

func TestTick_CooldownElapsed(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	writeWatermarkAge(t, p.MemoryDir, 13*time.Hour) // cooldown=12h, 13h ago → elapsed

	p.tick(context.Background())
	if eng.callCount() != 1 {
		t.Errorf("expected 1 Query call after cooldown elapsed, got %d", eng.callCount())
	}
}

func TestTick_NoNewMessages(t *testing.T) {
	eng := &mockTimerEngine{}
	store := &mockTimerStore{msgs: nil}
	p := baseTimerParams(t, eng, store, idleReady())
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls with no messages, got %d", eng.callCount())
	}
}

func TestTick_MessagesSinceError(t *testing.T) {
	eng := &mockTimerEngine{}
	store := &mockTimerStore{err: errors.New("db down")}
	p := baseTimerParams(t, eng, store, idleReady())
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls on MessagesSince error, got %d", eng.callCount())
	}
}

func TestTick_LastAssistantTimeError(t *testing.T) {
	eng := &mockTimerEngine{}
	idle := &mockTimerIdle{err: errors.New("db down")}
	p := baseTimerParams(t, eng, someMessages(), idle)
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls on LastAssistantTime error, got %d", eng.callCount())
	}
}

func TestTick_ReadWatermarkError(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	// Make memoryDir a path under a file → ReadFile fails with ENOTDIR
	p.MemoryDir = "/dev/null/x"

	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls on ReadWatermark error, got %d", eng.callCount())
	}
}

// --- Fire tests ---

func TestTick_AllGatesPass(t *testing.T) {
	eng := &mockTimerEngine{}
	store := someMessages()
	p := baseTimerParams(t, eng, store, idleReady())

	p.tick(context.Background())

	if eng.callCount() != 1 {
		t.Fatalf("expected 1 Query call, got %d", eng.callCount())
	}
	prompt := eng.lastPrompt()
	if prompt == "" {
		t.Fatal("Query prompt should not be empty")
	}
	if !strings.Contains(prompt, "Memory directory:") {
		t.Errorf("Query prompt should contain trigger header, got: %q", truncate(prompt, 80))
	}

	wm, err := ReadWatermark(p.MemoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if wm.IsZero() {
		t.Error("watermark should be written after successful Query")
	}
}

func TestTick_QueryFailed_NoWatermark(t *testing.T) {
	eng := &mockTimerEngine{queryErr: errors.New("llm error")}
	store := someMessages()
	p := baseTimerParams(t, eng, store, idleReady())

	p.tick(context.Background())

	if eng.callCount() != 1 {
		t.Fatalf("expected 1 Query call, got %d", eng.callCount())
	}
	wm, err := ReadWatermark(p.MemoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if !wm.IsZero() {
		t.Error("watermark should NOT be written when Query fails")
	}
}

func TestTick_PromptContainsLastDream(t *testing.T) {
	eng := &mockTimerEngine{}
	store := someMessages()
	p := baseTimerParams(t, eng, store, idleReady())
	// Write a watermark at a known time
	knownTime := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	writeWatermarkTime(t, p.MemoryDir, knownTime)
	// Set cooldown to 0 so it passes regardless of how old the watermark is
	p.Cooldown = 0

	p.tick(context.Background())

	if eng.callCount() != 1 {
		t.Fatalf("expected 1 Query call, got %d", eng.callCount())
	}
	// ReadWatermark round-trips through UnixMilli which shifts timezone,
	// so compute the expected string the same way the prompt does.
	roundTripped := time.UnixMilli(knownTime.UnixMilli())
	expected := "Last consolidation: " + roundTripped.Format("2006-01-02 15:04")
	prompt := eng.lastPrompt()
	if !strings.Contains(prompt, expected) {
		t.Errorf("prompt should contain %q, got: %q", expected, truncate(prompt, 200))
	}
}

func TestTick_PromptContainsNeverForColdStart(t *testing.T) {
	eng := &mockTimerEngine{}
	store := someMessages()
	p := baseTimerParams(t, eng, store, idleReady())

	p.tick(context.Background())

	if eng.callCount() != 1 {
		t.Fatalf("expected 1 Query call, got %d", eng.callCount())
	}
	prompt := eng.lastPrompt()
	if !strings.Contains(prompt, "Last consolidation: never") {
		t.Errorf("cold-start prompt should say 'never', got: %q", truncate(prompt, 200))
	}
}

// --- RunDreamTimer tests ---

func TestRunDreamTimer_StopsOnCancel(t *testing.T) {
	eng := &mockTimerEngine{busy: true}
	p := baseTimerParams(t, eng, someMessages(), idleReady())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunDreamTimer(ctx, p)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunDreamTimer did not stop within 2s of cancel")
	}
}

func TestRunDreamTimer_FiresOnTick(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	p.TickInterval = 20 * time.Millisecond

	ctx := t.Context()
	go RunDreamTimer(ctx, p)

	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: polling deadline for async goroutine
	for time.Now().Before(deadline) {           // REAL-TIME: poll condition for async goroutine
		if eng.callCount() >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: polling for async timer tick
	}
	t.Fatalf("expected at least 1 Query call within 2s, got %d", eng.callCount())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
