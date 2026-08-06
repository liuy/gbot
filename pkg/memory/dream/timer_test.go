package dream

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// mockTimerEngine implements DreamEngine for timer tests.
type mockTimerEngine struct {
	mu       sync.Mutex
	busy     bool
	chunkErr error // error to return from RunChunk (nil = success)
	calls    []string
}

func (m *mockTimerEngine) IsBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

func (m *mockTimerEngine) RunChunk(_ context.Context, userMessage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, userMessage)
	return m.chunkErr
}

func (m *mockTimerEngine) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
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
		ContextWindow: 100000,
		IdleThreshold: 2 * time.Hour,
		Cooldown:      6 * time.Hour,
		TickInterval:  50 * time.Millisecond,
		Logger:        slog.Default(),
	}
}

// idleReady returns a mockIdle with lastAssistant far enough in the past to
// pass the idle gate.
func idleReady() *mockTimerIdle {
	return &mockTimerIdle{lastAssistant: time.Now().Add(-3 * time.Hour)} // REAL-TIME: testing idle gate
}

func singleChunkMessages() *mockTimerStore {
	return &mockTimerStore{msgs: testMessages(3)}
}

// twoChunkMessages returns messages large enough to force 2 chunks with
// contextWindow=8000.
func twoChunkMessages() []*short.TranscriptMessage {
	return []*short.TranscriptMessage{
		{
			Type:      "user",
			Content:   `[{"type":"text","text":"` + strings.Repeat("abcdefgh", 2000) + `"}]`,
			CreatedAt: testTimeBase,
		},
		{
			Type:      "assistant",
			Content:   `[{"type":"text","text":"` + strings.Repeat("xyzwuvrs", 2000) + `"}]`,
			CreatedAt: testTimeBase.Add(time.Minute),
		},
	}
}

// sequentialErrorEngine wraps a mockTimerEngine and returns an error starting
// from the failFrom-th call (0-indexed). This tracks calls on the inner engine.
type sequentialErrorEngine struct {
	inner    *mockTimerEngine
	failFrom int
	calls    int
	mu       sync.Mutex
}

func (s *sequentialErrorEngine) IsBusy() bool {
	return s.inner.IsBusy()
}

func (s *sequentialErrorEngine) RunChunk(ctx context.Context, msg string) error {
	s.mu.Lock()
	idx := s.calls
	s.calls++
	s.mu.Unlock()
	if idx >= s.failFrom {
		return errors.New("simulated failure")
	}
	return s.inner.RunChunk(ctx, msg)
}

func (s *sequentialErrorEngine) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// --- Gate tests ---

func TestTick_BusyEngine(t *testing.T) {
	eng := &mockTimerEngine{busy: true}
	p := baseTimerParams(t, eng, singleChunkMessages(), idleReady())
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls when busy, got %d", eng.callCount())
	}
}

func TestTick_NotIdle(t *testing.T) {
	eng := &mockTimerEngine{}
	idle := &mockTimerIdle{lastAssistant: time.Now().Add(-30 * time.Minute)} // REAL-TIME: testing idle gate
	p := baseTimerParams(t, eng, singleChunkMessages(), idle)
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls when not idle, got %d", eng.callCount())
	}
}

func TestTick_IdleZero(t *testing.T) {
	eng := &mockTimerEngine{}
	idle := &mockTimerIdle{lastAssistant: time.Time{}} // zero = no assistant yet
	p := baseTimerParams(t, eng, singleChunkMessages(), idle)
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls when no assistant message, got %d", eng.callCount())
	}
}

func TestTick_CooldownNotElapsed(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, singleChunkMessages(), idleReady())
	writeWatermarkAge(t, p.MemoryDir, 1*time.Hour) // cooldown=6h, 1h ago → not elapsed

	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls during cooldown, got %d", eng.callCount())
	}
}

func TestTick_CooldownElapsed(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, singleChunkMessages(), idleReady())
	writeWatermarkAge(t, p.MemoryDir, 7*time.Hour) // cooldown=6h, 7h ago → elapsed

	p.tick(context.Background())
	if eng.callCount() != 1 {
		t.Errorf("expected 1 RunChunk call after cooldown elapsed, got %d", eng.callCount())
	}
}

func TestTick_NoNewMessages(t *testing.T) {
	eng := &mockTimerEngine{}
	store := &mockTimerStore{msgs: nil}
	p := baseTimerParams(t, eng, store, idleReady())
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls with no messages, got %d", eng.callCount())
	}
}

func TestTick_MessagesSinceError(t *testing.T) {
	eng := &mockTimerEngine{}
	store := &mockTimerStore{err: errors.New("db down")}
	p := baseTimerParams(t, eng, store, idleReady())
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls on MessagesSince error, got %d", eng.callCount())
	}
}

func TestTick_LastAssistantTimeError(t *testing.T) {
	eng := &mockTimerEngine{}
	idle := &mockTimerIdle{err: errors.New("db down")}
	p := baseTimerParams(t, eng, singleChunkMessages(), idle)
	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls on LastAssistantTime error, got %d", eng.callCount())
	}
}

func TestTick_ReadWatermarkError(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, singleChunkMessages(), idleReady())
	// Make memoryDir a path under a file → ReadFile fails with ENOTDIR
	p.MemoryDir = "/dev/null/x"

	p.tick(context.Background())
	if eng.callCount() != 0 {
		t.Errorf("expected 0 RunChunk calls on ReadWatermark error, got %d", eng.callCount())
	}
}

// --- Consolidate tests ---

func TestTick_AllGatesPass(t *testing.T) {
	eng := &mockTimerEngine{}
	store := singleChunkMessages()
	p := baseTimerParams(t, eng, store, idleReady())

	p.tick(context.Background())

	if eng.callCount() != 1 {
		t.Fatalf("expected 1 RunChunk call, got %d", eng.callCount())
	}
	eng.mu.Lock()
	msg := eng.calls[0]
	eng.mu.Unlock()
	if len(msg) < 6 || msg[:6] != "[syste" {
		t.Errorf("RunChunk message should start with [system], got: %q", msg)
	}

	wm, err := ReadWatermark(p.MemoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if wm.IsZero() {
		t.Error("watermark should be written after all chunks succeed")
	}
}

func TestTick_PartialFailure(t *testing.T) {
	eng := &mockTimerEngine{}
	store := &mockTimerStore{msgs: twoChunkMessages()}
	p := baseTimerParams(t, eng, store, idleReady())
	p.ContextWindow = 8000
	wrapped := &sequentialErrorEngine{inner: eng, failFrom: 1}
	p.Engine = wrapped

	p.tick(context.Background())

	if wrapped.callCount() != 2 {
		t.Fatalf("expected 2 RunChunk calls, got %d", wrapped.callCount())
	}
	wm, err := ReadWatermark(p.MemoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if !wm.IsZero() {
		t.Error("watermark should NOT be written on partial failure")
	}
}

func TestTick_AllChunksFail(t *testing.T) {
	eng := &mockTimerEngine{chunkErr: errors.New("llm error")}
	store := singleChunkMessages()
	p := baseTimerParams(t, eng, store, idleReady())

	p.tick(context.Background())

	if eng.callCount() != 1 {
		t.Fatalf("expected 1 RunChunk call, got %d", eng.callCount())
	}
	wm, err := ReadWatermark(p.MemoryDir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if !wm.IsZero() {
		t.Error("watermark should NOT be written when all chunks fail")
	}
}

// --- RunDreamTimer tests ---

func TestRunDreamTimer_StopsOnCancel(t *testing.T) {
	eng := &mockTimerEngine{busy: true}
	p := baseTimerParams(t, eng, singleChunkMessages(), idleReady())

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
	p := baseTimerParams(t, eng, singleChunkMessages(), idleReady())
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
	t.Fatalf("expected at least 1 RunChunk call within 2s, got %d", eng.callCount())
}
