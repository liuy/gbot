package dream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
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

// logRecord is one captured slog entry.
type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// logCapture is safe across goroutines: RunDreamTimer logs from its own.
type logCapture struct {
	mu   sync.Mutex
	recs *[]logRecord
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.recs = append(*c.recs, logRecord{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (c *logCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(*c.recs)
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// captureParams is baseTimerParams with a Logger that records every entry, so
// a gate's skip reason is asserted rather than inferred from the call count.
func captureParams(t *testing.T, eng DreamEngine, store MessageLister, idle IdleQuerier) (TimerParams, *[]logRecord) {
	t.Helper()
	p := baseTimerParams(t, eng, store, idle)
	var recs []logRecord
	p.Logger = slog.New(&logCapture{recs: &recs})
	return p, &recs
}

func wantRecord(t *testing.T, recs []logRecord, i int, level slog.Level, msg string) logRecord {
	t.Helper()
	if i >= len(recs) {
		t.Fatalf("expected record %d (%v %q), got only %d records: %+v", i, level, msg, len(recs), recs)
	}
	if recs[i].level != level || recs[i].msg != msg {
		t.Fatalf("record %d = (%v, %q), want (%v, %q); all records: %+v",
			i, recs[i].level, recs[i].msg, level, msg, recs)
	}
	return recs[i]
}

// mockTimerEngine implements DreamEngine for timer tests.
type mockTimerEngine struct {
	mu        sync.Mutex
	busy      bool
	queryErr  error
	queryHook func() // side effect run while the dream "runs", e.g. to break the watermark write
	prompts   []string
	ctxs      []context.Context
}

func (m *mockTimerEngine) IsBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

func (m *mockTimerEngine) Query(ctx context.Context, prompt string) error {
	if m.queryHook != nil {
		m.queryHook()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, prompt)
	m.ctxs = append(m.ctxs, ctx)
	return m.queryErr
}

func (m *mockTimerEngine) lastCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ctxs) == 0 {
		return nil
	}
	return m.ctxs[len(m.ctxs)-1]
}

func (m *mockTimerEngine) SessionID() string {
	return "mock-dream-session"
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
	// The tick wiring must feed the engine's session ID and the joined DB
	// path into the trigger — both land inside the embedded queries.
	if !strings.Contains(prompt, "mock-dream-session") {
		t.Errorf("prompt must exclude the dream engine's session ID, got: %q", truncate(prompt, 80))
	}
	wantDB := filepath.Join(p.MemoryDir, "memory.db")
	if !strings.Contains(prompt, wantDB) {
		t.Errorf("prompt must reference the transcript DB at %s", wantDB)
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
	// so compute the expected string the same way the prompt does — local
	// wall clock plus zone abbreviation.
	roundTripped := time.UnixMilli(knownTime.UnixMilli())
	expected := "Last consolidation: " + roundTripped.Local().Format("2006-01-02 15:04 MST")
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

// --- Gate skip reasons ---
//
// Every gate is silent-by-design from the caller's perspective, so the debug
// entry IS the observable behaviour: an operator diagnosing "why didn't the
// dream run" reads these. Each test pins the exact level, message and attrs.

func TestTick_BusyEngine_LogsSkip(t *testing.T) {
	eng := &mockTimerEngine{busy: true}
	p, recs := captureParams(t, eng, someMessages(), idleReady())

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	wantRecord(t, *recs, 0, slog.LevelDebug, "dream: skip — engine busy")
}

func TestTick_LastAssistantTimeError_LogsWarn(t *testing.T) {
	eng := &mockTimerEngine{}
	sentinel := errors.New("db down")
	idle := &mockTimerIdle{err: sentinel}
	p, recs := captureParams(t, eng, someMessages(), idle)

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	rec := wantRecord(t, *recs, 0, slog.LevelWarn, "dream: LastAssistantTime failed")
	if rec.attrs["error"] != any(sentinel) {
		t.Errorf("error attr = %#v, want %#v", rec.attrs["error"], sentinel)
	}
}

func TestTick_IdleZero_LogsSkip(t *testing.T) {
	eng := &mockTimerEngine{}
	p, recs := captureParams(t, eng, someMessages(), &mockTimerIdle{lastAssistant: time.Time{}})

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	wantRecord(t, *recs, 0, slog.LevelDebug, "dream: skip — no assistant message yet")
}

func TestTick_NotIdle_LogsIdleAndNeed(t *testing.T) {
	eng := &mockTimerEngine{}
	idle := &mockTimerIdle{lastAssistant: time.Now().Add(-30 * time.Minute)} // REAL-TIME: idle gate
	p, recs := captureParams(t, eng, someMessages(), idle)

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	rec := wantRecord(t, *recs, 0, slog.LevelDebug, "dream: skip — user not idle")
	if got, want := rec.attrs["idle"], 30*time.Minute; got != any(want) {
		t.Errorf("idle attr = %#v, want %v", got, want)
	}
	if got, want := rec.attrs["need"], time.Hour; got != any(want) {
		t.Errorf("need attr = %#v, want %v (must be the configured threshold)", got, want)
	}
}

func TestTick_ReadWatermarkError_LogsWarn(t *testing.T) {
	eng := &mockTimerEngine{}
	p, recs := captureParams(t, eng, someMessages(), idleReady())
	p.MemoryDir = "/dev/null/x"

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	rec := wantRecord(t, *recs, 0, slog.LevelWarn, "dream: ReadWatermark failed")
	if _, ok := rec.attrs["error"].(error); !ok {
		t.Errorf("error attr = %#v, want an error value", rec.attrs["error"])
	}
}

func TestTick_Cooldown_LogsRemaining(t *testing.T) {
	eng := &mockTimerEngine{}
	p, recs := captureParams(t, eng, someMessages(), idleReady())
	writeWatermarkAge(t, p.MemoryDir, 1*time.Hour) // cooldown 12h, stamped 1h ago

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	rec := wantRecord(t, *recs, 0, slog.LevelDebug, "dream: skip — cooldown")
	// remaining = cooldown - age, rounded to the second.
	if got, want := rec.attrs["remaining"], 11*time.Hour; got != any(want) {
		t.Errorf("remaining attr = %#v, want %v", got, want)
	}
}

// A never-consolidated project has a zero watermark, which must not be read as
// "consolidated a long time ago" and blocked by an enormous cooldown.
func TestTick_ZeroWatermarkNeverInCooldown(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	// The largest representable Duration (~292 years): no cooldown length
	// may gate a never-consolidated project. Time.Sub clamps the zero-time
	// difference to maxDuration, so the age comparison can never be true.
	p.Cooldown = math.MaxInt64

	p.tick(context.Background())

	if eng.callCount() != 1 {
		t.Errorf("expected 1 Query call with no watermark, got %d", eng.callCount())
	}
}

func TestTick_NoNewMessages_LogsSkip(t *testing.T) {
	eng := &mockTimerEngine{}
	p, recs := captureParams(t, eng, &mockTimerStore{msgs: nil}, idleReady())

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	wantRecord(t, *recs, 0, slog.LevelDebug, "dream: skip — no new messages")
}

func TestTick_MessagesSinceError_LogsWarn(t *testing.T) {
	eng := &mockTimerEngine{}
	sentinel := errors.New("db down")
	p, recs := captureParams(t, eng, &mockTimerStore{err: sentinel}, idleReady())

	p.tick(context.Background())

	if len(*recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(*recs), *recs)
	}
	rec := wantRecord(t, *recs, 0, slog.LevelWarn, "dream: MessagesSince failed")
	if rec.attrs["error"] != any(sentinel) {
		t.Errorf("error attr = %#v, want %#v", rec.attrs["error"], sentinel)
	}
}

// --- Fire path ---

func TestTick_AllGatesPass_LogsFiringAndCompleted(t *testing.T) {
	eng := &mockTimerEngine{}
	p, recs := captureParams(t, eng, someMessages(), idleReady())

	p.tick(context.Background())

	if len(*recs) != 2 {
		t.Fatalf("expected 2 log records, got %d: %+v", len(*recs), *recs)
	}
	firing := wantRecord(t, *recs, 0, slog.LevelInfo, "dream: firing")
	// slog widens ints to int64 in the captured value.
	if got, want := firing.attrs["messages"], int64(3); got != any(want) {
		t.Errorf("messages attr = %#v, want %d (the store's message count)", got, want)
	}
	if got, want := firing.attrs["idle"], 2*time.Hour; got != any(want) {
		t.Errorf("idle attr = %#v, want %v", got, want)
	}
	wantRecord(t, *recs, 1, slog.LevelInfo, "dream: completed")
}

func TestTick_QueryFailed_LogsWarnAndNoCompletion(t *testing.T) {
	sentinel := errors.New("llm error")
	eng := &mockTimerEngine{queryErr: sentinel}
	p, recs := captureParams(t, eng, someMessages(), idleReady())

	p.tick(context.Background())

	if len(*recs) != 2 {
		t.Fatalf("expected 2 log records, got %d: %+v", len(*recs), *recs)
	}
	wantRecord(t, *recs, 0, slog.LevelInfo, "dream: firing")
	rec := wantRecord(t, *recs, 1, slog.LevelWarn, "dream: query failed")
	if rec.attrs["error"] != any(sentinel) {
		t.Errorf("error attr = %#v, want %#v", rec.attrs["error"], sentinel)
	}
}

// A failed watermark stamp must be reported: the dream ran, but the next
// tick's cooldown gate will not see it, so the silence is a bug magnet.
func TestTick_WatermarkWriteFails_LogsWarn(t *testing.T) {
	eng := &mockTimerEngine{}
	p, recs := captureParams(t, eng, someMessages(), idleReady())
	eng.queryHook = func() {
		// Occupy the watermark path with a directory so the stamp's
		// WriteFile fails without touching permissions or the clock.
		if err := os.Mkdir(filepath.Join(p.MemoryDir, watermarkFileName), 0o755); err != nil {
			t.Errorf("occupy watermark path: %v", err)
		}
	}

	p.tick(context.Background())

	if len(*recs) != 3 {
		t.Fatalf("expected 3 log records, got %d: %+v", len(*recs), *recs)
	}
	wantRecord(t, *recs, 0, slog.LevelInfo, "dream: firing")
	rec := wantRecord(t, *recs, 1, slog.LevelWarn, "dream: WriteWatermark failed")
	if _, ok := rec.attrs["error"].(error); !ok {
		t.Errorf("error attr = %#v, want an error value", rec.attrs["error"])
	}
	wantRecord(t, *recs, 2, slog.LevelInfo, "dream: completed")
}

// Gate 5: a context cancelled while the earlier gates were running must stop
// the Query from being issued at all.
func TestTick_ContextCancelled_NoQuery(t *testing.T) {
	eng := &mockTimerEngine{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, recs := captureParams(t, eng, someMessages(), idleReady())

	p.tick(ctx)

	if eng.callCount() != 0 {
		t.Errorf("expected 0 Query calls on a cancelled context, got %d", eng.callCount())
	}
	if len(*recs) != 0 {
		t.Errorf("expected no log records before gate 5 returns, got %+v", *recs)
	}
}

type dreamCtxKey struct{}

// Query must receive the caller's context, not a fresh one: cancellation and
// any request-scoped values are what let the dream be interrupted.
func TestTick_QueryReceivesCallerContext(t *testing.T) {
	eng := &mockTimerEngine{}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	ctx := context.WithValue(context.Background(), dreamCtxKey{}, "sentinel")

	p.tick(ctx)

	if eng.callCount() != 1 {
		t.Fatalf("expected 1 Query call, got %d", eng.callCount())
	}
	got := eng.lastCtx()
	if got == nil {
		t.Fatal("Query received a nil context")
	}
	if v := got.Value(dreamCtxKey{}); v != "sentinel" {
		t.Errorf("Query ctx value = %#v, want %q (caller's context must be passed through)", v, "sentinel")
	}
}

// A nil Logger must fall back to slog.Default(): leaving it nil panics the
// timer goroutine on the first gate skip.
func TestRunDreamTimer_NilLoggerFallsBackToDefault(t *testing.T) {
	var recs []logRecord
	capture := &logCapture{recs: &recs}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })

	eng := &mockTimerEngine{busy: true}
	p := baseTimerParams(t, eng, someMessages(), idleReady())
	p.Logger = nil
	p.TickInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunDreamTimer(ctx, p)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: polling deadline for async ticker
	for time.Now().Before(deadline) {           // REAL-TIME: poll condition for async ticker
		if capture.count() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: polling for a ticker tick
	}
	cancel()
	<-done // the goroutine is finished: recs is now safe to read directly

	if len(recs) != 1 {
		t.Fatalf("expected exactly 1 entry on the default logger, got %d: %+v", len(recs), recs)
	}
	wantRecord(t, recs, 0, slog.LevelDebug, "dream: skip — engine busy")
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
