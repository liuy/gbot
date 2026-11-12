package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/project"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	cronpkg "github.com/liuy/gbot/pkg/tool/cron"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// Cron integration tests: full path Manager tick → EnqueueAttachment → engine
// turn → history. Fixture pattern from engine_queued_message_test.go (stub
// provider, blockingTool) plus a real cron Manager on a t.TempDir store with
// a fake clock. Polling loops carry deadlines; never bare sleeps.

// fakeClock is an atomic test clock: the manager's ticker goroutine reads it
// every second while tests advance it.
type fakeClock struct{ nano atomic.Int64 }

func (c *fakeClock) Now() time.Time                { return time.Unix(0, c.nano.Load()) }
func (c *fakeClock) Set(t time.Time)               { c.nano.Store(t.UnixNano()) }
func (c *fakeClock) Add(d time.Duration) time.Time { return time.Unix(0, c.nano.Add(int64(d))) }

// newCronEngine builds a bare engine + wired cron Manager over dir with a
// controllable clock. The system prompt is set by the caller (some tests
// deliberately delay it).
func newCronEngine(t *testing.T, dir string, responses ...[]llm.StreamEvent) (*Engine, *cronpkg.Manager, *fakeClock, *mockProvider) {
	t.Helper()
	orig := time.Local
	time.Local = time.FixedZone("CST-TEST", 8*3600)
	t.Cleanup(func() { time.Local = orig })

	mp := &mockProvider{}
	for _, evs := range responses {
		mp.addResponse(evs, nil)
	}
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	t.Cleanup(func() { eng.Close() })

	m := cronpkg.NewManager(dir)
	clock := &fakeClock{}
	clock.nano.Store(time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local).UnixNano())
	m.SetSink(eng)
	m.SetNow(clock.Now)
	return eng, m, clock, mp
}

// TestCronIntegration_ColdStartEmptyProjectspace is the cold-start scenario:
// empty dir → nothing scheduled; a session one-shot created through the tool
// fires and lands as a user-turn prompt.
func TestCronIntegration_ColdStartEmptyProjectspace(t *testing.T) {
	dir := t.TempDir()
	eng, m, clock, mp := newCronEngine(t, dir, textStreamEvents("test-model", "reminder noted"))
	eng.SetSystemPrompt("sys")
	eng.SetToolRefs(ToolRefs{Cron: m})

	cronTool := cronpkg.New(m)
	if _, err := cronTool.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil); err != nil {
		t.Fatalf("list: %v", err)
	}
	if tasks := m.List(); len(tasks) != 0 {
		t.Fatalf("cold-start List = %d tasks, want 0", len(tasks))
	}

	if _, err := cronTool.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"* * * * *","prompt":"look at the clock","recurring":false}`), nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.Add(61 * time.Second)

	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: 1s ticker
	fired := false
	for time.Now().Before(deadline) { // REAL-TIME poll loop
		if mp.callCount() >= 1 {
			fired = true
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if !fired {
		t.Fatal("cron fire never ran a provider turn within deadline")
	}
	waitIdle(t, eng)
	found := false
	for _, msg := range eng.Messages() {
		for _, b := range msg.Content {
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, "look at the clock") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("fired cron prompt never landed in engine history")
	}
	if tasks := m.List(); len(tasks) != 0 {
		t.Errorf("session store after one-shot fire = %d tasks, want 0", len(tasks))
	}
}

// TestCronIntegration_WakeTheAgent is the hot path: an idle engine with a
// system prompt gets woken by the fire — a full agentic turn runs, and both
// the assembled request and the in-memory history carry the OriginCron
// envelope (persisted final form; assembly never re-wraps it).
func TestCronIntegration_WakeTheAgent(t *testing.T) {
	dir := t.TempDir()
	eng, m, clock, mp := newCronEngine(t, dir, textStreamEvents("test-model", "done: drank water"))
	eng.SetSystemPrompt("sys")
	eng.SetToolRefs(ToolRefs{Cron: m})

	if _, err := m.Create("* * * * *", "drink water", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.Add(61 * time.Second)

	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: 1s ticker
	for time.Now().Before(deadline) {
		if mp.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if mp.callCount() < 1 {
		t.Fatal("the idle wake never ran an LLM turn")
	}
	waitIdle(t, eng)

	// (a) Assembled request: the OriginCron envelope was built at marshal time.
	enveloped := false
	for _, msg := range mp.lastRequestMessages() {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, b := range msg.Content {
			if b.Type == types.ContentTypeText &&
				strings.Contains(b.Text, "<system-reminder>") &&
				strings.Contains(b.Text, "A scheduled cron task fired:\ndrink water") {
				enveloped = true
			}
		}
	}
	if !enveloped {
		t.Error("assembled request lacks the cron fire envelope")
	}

	// (b) History: a plain user row carrying the envelope bytes (the fire is
	// persisted in final form, not as a bare-prompt attachment row).
	enveloped = false
	for _, msg := range eng.Messages() {
		if msg.Role != types.RoleUser || msg.Attachment != nil {
			continue
		}
		if utils.FirstTextBlockContent(msg) == utils.BuildCronReminderEnvelope("drink water") {
			enveloped = true
		}
	}
	if !enveloped {
		t.Error("history lacks the envelope-form row for the fired cron task")
	}

	// (c) Turn ran and the engine is back to idle.
	if eng.IsBusy() {
		t.Error("engine still busy after the wake turn")
	}
	if tasks := m.List(); len(tasks) != 0 {
		t.Errorf("session store after fire = %d tasks, want 0", len(tasks))
	}
}

// TestCronIntegration_MissedPolicyOnStart is the recovery/missed scenario: an
// overdue durable one-shot written while gbot was down surfaces ONE
// notification on Start, is removed from the file, and never double-fires.
func TestCronIntegration_MissedPolicyOnStart(t *testing.T) {
	dir := t.TempDir()
	eng, m, _, mp := newCronEngine(t, dir, textStreamEvents("test-model", "acknowledged the missed task"))
	eng.SetSystemPrompt("sys")
	eng.SetToolRefs(ToolRefs{Cron: m})

	t0 := time.Date(2026, 11, 11, 3, 0, 0, 0, time.Local)
	store := cronpkg.StoreFor(dir)
	if err := store.Write([]cronpkg.Task{{
		ID: "missd001", Cron: "0 6 * * *", Prompt: "missed morning task",
		CreatedAt: t0.Add(-7 * time.Hour).UnixMilli(),
	}}); err != nil {
		t.Fatalf("seed missed task: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: 1s ticker
	for time.Now().Before(deadline) {
		if mp.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	waitIdle(t, eng)

	notifSeen := false
	for _, msg := range eng.Messages() {
		for _, b := range msg.Content {
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, "missed while gbot was not running") &&
				strings.Contains(b.Text, "missed morning task") {
				notifSeen = true
			}
		}
	}
	if !notifSeen {
		t.Fatal("missed-task notification never reached engine history")
	}
	if store.HasTasks() {
		t.Error("missed one-shot still in the file after Start")
	}
	if got := mp.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want exactly 1 (notification only — no double fire)", got)
	}
}

// TestCronIntegration_DurableReloadAcrossManagers is the recovery/reload
// scenario: a stopped Manager's durable recurring task reloads into a fresh
// Manager, the session task dies with the old one, and lastFiredAt anchoring
// prevents an immediate refire.
func TestCronIntegration_DurableReloadAcrossManagers(t *testing.T) {
	dir := t.TempDir()
	eng, m, _, _ := newCronEngine(t, dir)
	eng.SetSystemPrompt("sys")
	eng.SetToolRefs(ToolRefs{Cron: m})

	if _, err := m.Create("0 9 * * *", "daily check", true, true); err != nil {
		t.Fatalf("durable create: %v", err)
	}
	if _, err := m.Create("* * * * *", "session only", false, false); err != nil {
		t.Fatalf("session create: %v", err)
	}
	m.Stop()

	eng2, m2, _, _ := newCronEngine(t, dir)
	eng2.SetSystemPrompt("sys")
	eng2.SetToolRefs(ToolRefs{Cron: m2})
	if err := m2.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	tasks := m2.List()
	if len(tasks) != 1 {
		t.Fatalf("reloaded List = %+v, want only the durable task", tasks)
	}
	if tasks[0].Prompt != "daily check" {
		t.Errorf("reloaded task = %+v", tasks[0])
	}
	// lastFiredAt-anchored no-refire: the reload must not fire the (not yet
	// overdue) recurring task. The created-at anchor is `now`; give the
	// scheduler a couple of idle checks at the SAME fake now.
	deadline := time.Now().Add(2500 * time.Millisecond) // REAL-TIME: ≥2 ticks
	for time.Now().Before(deadline) {
		if len(eng2.Messages()) != 0 {
			t.Fatal("reloaded manager fired without the schedule elapsing")
		}
		time.Sleep(50 * time.Millisecond) // REAL-TIME
	}
	m2.Stop()
}

// TestCronIntegration_CloseReleasesLockAndSubEngineGuard pins both lifecycle
// protections: Close stops the scheduler and releases the durable lock, and a
// sub-engine's Close (whose toolRefs alias the parent's) must NOT stop the
// parent's Manager — the session-notes extractor closes sub-engines
// mid-session.
func TestCronIntegration_CloseReleasesLockAndSubEngineGuard(t *testing.T) {
	dir := t.TempDir()
	eng, m, _, _ := newCronEngine(t, dir)
	eng.SetSystemPrompt("sys")
	eng.SetToolRefs(ToolRefs{Cron: m})

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Sub-engine mirrors start.go:538-547 (session-notes extractor shape).
	subEng := eng.NewSubEngine(SubEngineOptions{
		SystemPrompt: "notes extractor",
		Tools:        map[string]tool.Tool{},
		AgentType:    "session_memory",
	})
	// The lock MUST exist before the sub closes — a loud setup failure if the
	// manager never acquired it.
	if !cronpkg.LockFileExists(dir) {
		t.Fatal("setup: scheduler lock file missing before sub-engine Close")
	}
	subEng.Close()
	if !cronpkg.LockFileExists(dir) {
		t.Fatal("sub-engine Close released the parent's scheduler lock — the !isSubagent guard failed")
	}

	eng.Close()
	if cronpkg.LockFileExists(dir) {
		t.Error("engine Close left the scheduler lock behind")
	}
}

// TestCronIntegration_MidTurnDefersToIdle pins the IsBusy gate: a fire due
// while the engine runs a tool is deferred; once idle, the next tick fires
// and the prompt is answered as its own turn.
func TestCronIntegration_MidTurnDefersToIdle(t *testing.T) {
	dir := t.TempDir()
	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	eng, m, clock, mp := newCronEngine(t, dir,
		toolUseStreamEvents("test-model", "t1", "blocker", `{}`),
		textStreamEvents("test-model", "tool turn done"),
		textStreamEvents("test-model", "clock says ten oh one"),
	)
	eng.tools = map[string]tool.Tool{bt.Name(): bt}
	eng.SetSystemPrompt("sys")
	eng.SetToolRefs(ToolRefs{Cron: m})

	if _, err := m.Create("* * * * *", "look at the clock", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.Add(61 * time.Second)

	eng.Query(context.Background(), "run the tool", "sys")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	t.Cleanup(func() {
		select {
		case <-bt.releaseCh:
		default:
			close(bt.releaseCh)
		}
	})

	// While the tool runs, no fire: the busy gate defers.
	busyDeadline := time.Now().Add(2200 * time.Millisecond) // REAL-TIME: ≥2 ticks
	for time.Now().Before(busyDeadline) {
		if eng.AttachmentsLen() != 0 {
			t.Fatal("cron item enqueued while the engine was busy — the IsBusy gate failed")
		}
		time.Sleep(50 * time.Millisecond) // REAL-TIME
	}

	close(bt.releaseCh)
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: 1s ticker
	for time.Now().Before(deadline) {
		if mp.callCount() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	waitIdle(t, eng)
	if got := mp.callCount(); got != 3 {
		t.Fatalf("provider calls = %d, want 3 (tool turn, final text, cron answer turn)", got)
	}

	msgs := eng.Messages()
	var assistantTexts []string
	cronIdx := -1
	for i, msg := range msgs {
		if msg.Role == types.RoleAssistant {
			for _, b := range msg.Content {
				if b.Type == types.ContentTypeText {
					assistantTexts = append(assistantTexts, b.Text)
				}
			}
		}
		if msg.Role == types.RoleUser {
			if _, ok := utils.UnwrapCronReminderEnvelope(utils.FirstTextBlockContent(msg)); ok {
				cronIdx = i
			}
		}
	}
	if cronIdx == -1 {
		t.Fatal("no cron prompt row in history after the deferred fire")
	}
	if len(assistantTexts) < 2 {
		t.Fatalf("assistant text turns = %d, want ≥2 (turn result, then the cron answer)", len(assistantTexts))
	}
	if !strings.Contains(assistantTexts[len(assistantTexts)-1], "clock says") {
		t.Errorf("last assistant turn = %q, want the cron answer", assistantTexts[len(assistantTexts)-1])
	}
	// The deferred fire lands AFTER the busy turn's final text (the first
	// assistant row) and BEFORE the answer turn's assistant row.
	firstAssistant := -1
	for i, msg := range msgs {
		if msg.Role == types.RoleAssistant {
			firstAssistant = i
			break
		}
	}
	if firstAssistant == -1 || cronIdx < firstAssistant {
		t.Errorf("cron row at %d, first assistant at %d — fire must follow the busy turn", cronIdx, firstAssistant)
	}
}

// TestCronIntegration_StrandedFirePersistsEnvelope is the ONE variant where
// the envelope appears in HISTORY: the fire strands in the queue (no system
// prompt yet — startProcessAttachmentsIfIdle early-returns), and the next
// user query's turn-boundary drain persists it through
// appendQueuedUserMessage with the reminder envelope.
func TestCronIntegration_StrandedFirePersistsEnvelope(t *testing.T) {
	dir := t.TempDir()
	eng, m, clock, mp := newCronEngine(t, dir,
		textStreamEvents("test-model", "first answer"),
		textStreamEvents("test-model", "and now the clock"),
	)
	// SystemPrompt deliberately NOT set: the fire strands in the queue.
	eng.SetToolRefs(ToolRefs{Cron: m})

	if _, err := m.Create("* * * * *", "check the time", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.Add(61 * time.Second)

	strandDeadline := time.Now().Add(5 * time.Second) // REAL-TIME: 1s ticker
	for time.Now().Before(strandDeadline) {
		if eng.AttachmentsLen() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if got := eng.AttachmentsLen(); got != 1 {
		t.Fatalf("attachment queue = %d, want 1 stranded cron item", got)
	}

	eng.SetSystemPrompt("sys")
	eng.Query(context.Background(), "hello", "sys")
	waitForCallCount(t, mp, 2, 5*time.Second)
	waitIdle(t, eng)

	msgs := eng.Messages()
	envelopeIdx := -1
	for i, msg := range msgs {
		if msg.Role != types.RoleUser || len(msg.Content) == 0 || msg.Content[0].Type != types.ContentTypeText {
			continue
		}
		if strings.HasPrefix(msg.Content[0].Text, "<system-reminder>\nA scheduled cron task fired:\ncheck the time\n</system-reminder>") {
			envelopeIdx = i
		}
	}
	if envelopeIdx == -1 {
		t.Fatal("the stranded cron fire did not persist its envelope in history at the turn boundary")
	}
	// An assistant answer follows the envelope row.
	hasAnswerAfter := false
	for i := envelopeIdx + 1; i < len(msgs); i++ {
		if msgs[i].Role == types.RoleAssistant {
			hasAnswerAfter = true
		}
	}
	if !hasAnswerAfter {
		t.Error("no assistant turn after the envelope row — the follow-up turn never ran")
	}
}

// TestCronIntegration_IdleFirePersistsEnvelopeAndReloads is the restart
// scenario: an idle fire must persist in envelope form (the job/message
// family's discipline — a store-reloaded fire replays as the reminder row,
// never as a plain user bubble), and the reloaded envelope must reach the API
// exactly once (no double-wrap through normalizeAttachmentForAPI).
func TestCronIntegration_IdleFirePersistsEnvelopeAndReloads(t *testing.T) {
	dir := t.TempDir()

	orig := time.Local
	time.Local = time.FixedZone("CST-TEST", 8*3600)
	t.Cleanup(func() { time.Local = orig })

	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "done: drank water"), nil)
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetSystemPrompt("sys")

	m := cronpkg.NewManager(dir)
	clock := &fakeClock{}
	clock.nano.Store(time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local).UnixNano())
	m.SetSink(eng)
	m.SetNow(clock.Now)
	eng.SetToolRefs(ToolRefs{Cron: m})

	if _, err := m.Create("* * * * *", "drink water", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.Add(61 * time.Second)
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: 1s ticker
	for time.Now().Before(deadline) {
		if mp.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if mp.callCount() < 1 {
		t.Fatal("the idle wake never ran an LLM turn")
	}
	waitIdle(t, eng)

	wantEnvelope := utils.BuildCronReminderEnvelope("drink water")

	// (a) In-memory history: a plain user row carrying the envelope bytes —
	// not an attachment row with a bare prompt.
	envelopeRows := 0
	for _, msg := range eng.Messages() {
		if msg.Role != types.RoleUser || msg.Attachment != nil {
			continue
		}
		if utils.FirstTextBlockContent(msg) == wantEnvelope {
			envelopeRows++
		}
	}
	if envelopeRows != 1 {
		t.Fatalf("envelope rows in history = %d, want exactly 1 (got messages: %+v)", envelopeRows, eng.Messages())
	}

	// (b) Store reload: the persisted row survives as the same envelope bytes,
	// and no bare-prompt echo of the fire exists anywhere in the chain.
	reloaded, err := eng.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	reloadedEnvelope := false
	for _, msg := range reloaded {
		text := utils.FirstTextBlockContent(msg)
		if text == wantEnvelope {
			reloadedEnvelope = true
		}
		if msg.Role == types.RoleUser && text == "drink water" {
			t.Errorf("reloaded history carries the fire as a bare user prompt: %q", text)
		}
	}
	if !reloadedEnvelope {
		t.Fatal("the reloaded chain lacks the cron fire envelope row")
	}

	// (c) Assembly on the reloaded history sends the envelope exactly once:
	// the persisted row is a plain user message, so marshalMessages must not
	// route it through normalizeAttachmentForAPI (that would double-wrap).
	mp2 := &mockProvider{}
	mp2.addResponse(textStreamEvents("test-model", "after reload"), nil)
	eng2 := New(&Params{
		Provider:   mp2,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetMessages(reloaded)
	eng2.SetSystemPrompt("sys")
	eng2.Query(context.Background(), "next question", "sys")
	waitForCallCount(t, mp2, 1, 5*time.Second)
	waitIdle(t, eng2)
	cronHeadCount := 0
	for _, msg := range mp2.lastRequestMessages() {
		for _, b := range msg.Content {
			if b.Type != types.ContentTypeText {
				continue
			}
			cronHeadCount += strings.Count(b.Text, "A scheduled cron task fired")
			if strings.Count(b.Text, "A scheduled cron task fired") > 1 || strings.Count(b.Text, "<system-reminder>") > 1 {
				t.Errorf("double-wrapped cron envelope in request text: %q", b.Text)
			}
		}
	}
	if cronHeadCount != 1 {
		t.Errorf("cron envelope heads across request texts = %d, want exactly 1", cronHeadCount)
	}
}

// TestAppendDrainedItem_EnvelopePersistIsCronOnly pins the routing boundary:
// OriginCron alone claims the envelope persist path. A prompt item with any
// other origin is user input and must drain as the attachment row; the cron
// item drains as the envelope row.
func TestAppendDrainedItem_EnvelopePersistIsCronOnly(t *testing.T) {
	eng := New(&Params{
		Logger:     slog.Default(),
		Dispatcher: newEventCollector(),
	})
	t.Cleanup(func() { eng.Close() })

	human := types.QueuedItem{
		Value: "user says hi", Mode: types.ItemModePrompt, UUID: "human-1",
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC),
	}
	att := eng.appendDrainedItem(human)
	if att == nil {
		t.Fatal("human-origin prompt drained as a reminder row, want the attachment message")
	}
	if att.MessageType != types.MessageTypeAttachment || att.Attachment == nil ||
		att.Attachment.Mode != types.ItemModePrompt || att.Attachment.Origin.Kind != types.OriginHuman {
		t.Fatalf("human drain = %+v, want the prompt-mode attachment row", att)
	}
	if msgs := eng.Messages(); len(msgs) != 1 || msgs[0].MessageType != types.MessageTypeAttachment {
		t.Errorf("human-origin history = %+v, want one attachment row", msgs)
	}

	fire := types.QueuedItem{
		Value: "drink water", Mode: types.ItemModePrompt, UUID: "cron-1",
		Origin:    &types.MessageOrigin{Kind: types.OriginCron},
		Timestamp: time.Date(2026, 11, 11, 10, 1, 0, 0, time.UTC),
	}
	if m := eng.appendDrainedItem(fire); m != nil {
		t.Fatalf("cron drain returned %+v, want the nil reminder form", m)
	}
	msgs := eng.Messages()
	if len(msgs) != 2 {
		t.Fatalf("history = %d messages, want 2 (attachment + envelope)", len(msgs))
	}
	if msgs[1].Attachment != nil || msgs[1].MessageType != types.MessageTypeUser {
		t.Errorf("cron row = %+v, want a plain user message (no attachment stub)", msgs[1])
	}
	if got := utils.FirstTextBlockContent(msgs[1]); got != utils.BuildCronReminderEnvelope("drink water") {
		t.Errorf("cron row text = %q, want the envelope bytes", got)
	}
}

// waitIdle polls IsBusy until false with a deadline.
func waitIdle(t *testing.T, eng *Engine) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {
		if !eng.IsBusy() {
			return
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	t.Fatal("engine never returned to idle")
}

// cronTestTaskList builds a task list over a temp dir (SharedDeps needs one).
func cronTestTaskList(t *testing.T) *task.List {
	t.Helper()
	return task.NewList(t.TempDir())
}

// TestCronRegistration_GatesAndWiring covers the bootstrap surface: CreateTools
// registers the Cron tool and a Manager against the projectspace of the
// working dir, WithoutCron and GBOT_DISABLE_CRON suppress it, and WireEngine
// gives a sub-engine's manager sessionOnly semantics (durable rejected).
func TestCronRegistration_GatesAndWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()

	tl := cronTestTaskList(t)
	deps := SharedDeps{WorkingDir: wd}
	refs := CreateTools(deps, tl)
	if refs.Cron == nil {
		t.Fatal("CreateTools did not build a Cron manager")
	}
	if _, ok := refs.Reg.Lookup("Cron"); !ok {
		t.Fatal("Cron tool not registered")
	}
	t.Cleanup(refs.Cron.Stop)
	// The manager points at the projectspace of the working dir.
	if refs.Cron.Dir() != project.Dir(wd) {
		t.Errorf("manager dir = %q, want projectspace %q", refs.Cron.Dir(), project.Dir(wd))
	}

	// WithoutCron suppresses tool + manager.
	refs2 := CreateTools(deps, tl, WithoutCron())
	if refs2.Cron != nil {
		t.Error("WithoutCron left a Cron manager behind")
	}
	if _, ok := refs2.Reg.Lookup("Cron"); ok {
		t.Error("WithoutCron still registered the Cron tool")
	}

	// Env killswitch.
	t.Setenv("GBOT_DISABLE_CRON", "1")
	refs3 := CreateTools(deps, tl)
	if refs3.Cron != nil {
		t.Error("GBOT_DISABLE_CRON=1 did not suppress cron registration")
	}
	t.Setenv("GBOT_DISABLE_CRON", "")

	// WireEngine on a sub-engine: the SUB's own manager gets sessionOnly
	// semantics — the durable reject fires, and no auto-start happens.
	parentEng, _, _, _ := newCronEngine(t, t.TempDir())
	subEng := parentEng.NewSubEngine(SubEngineOptions{SystemPrompt: "sub", Tools: map[string]tool.Tool{}})
	if !subEng.isSubagent {
		t.Fatal("setup: NewSubEngine did not mark the engine as subagent")
	}
	subRefs := CreateTools(deps, tl)
	t.Cleanup(subRefs.Cron.Stop)
	WireEngine(subEng, subRefs, nil)
	if _, err := subRefs.Cron.Create("0 9 * * *", "p", false, true); err == nil {
		t.Error("sub-agent durable create succeeded, want the reject")
	}
	if subRefs.Cron.Running() {
		t.Error("sub manager started without a create (sessionOnly managers start on demand)")
	}
	// A session create on the sub manager works and starts its ticker.
	if _, err := subRefs.Cron.Create("7 * * * *", "p", false, false); err != nil {
		t.Errorf("sub session create: %v", err)
	}
	if !subRefs.Cron.Running() {
		t.Error("sub manager not running after a session create")
	}
	if cronpkg.LockFileExists(subRefs.Cron.Dir()) {
		t.Error("sessionOnly manager acquired the scheduler lock")
	}
}

// TestCronRegistration_StartIfTasksExist pins the boot auto-enable: a
// projectspace with durable tasks starts the scheduler at WireEngine time,
// and closing the engine releases the projectspace lock.
func TestCronRegistration_StartIfTasksExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	tl := cronTestTaskList(t)
	deps := SharedDeps{WorkingDir: wd}
	refs := CreateTools(deps, tl)
	if refs.Cron.Running() {
		t.Error("manager started without any durable tasks")
	}
	refs.Cron.Stop()

	// Seed a durable task in the projectspace, rebuild, wire → started.
	if err := cronpkg.StoreFor(refs.Cron.Dir()).Write([]cronpkg.Task{{
		ID: "boot0001", Cron: "0 9 * * *", Prompt: "boot task", CreatedAt: time.Now().UnixMilli(), Recurring: true, // REAL-TIME: real created-at for the reload anchor
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	refs2 := CreateTools(deps, tl)
	t.Cleanup(refs2.Cron.Stop)
	eng, engMgr, _, _ := newCronEngine(t, t.TempDir())
	engMgr.Stop()
	WireEngine(eng, refs2, nil)
	if !refs2.Cron.Running() {
		t.Fatal("WireEngine did not auto-start the scheduler despite durable tasks")
	}
	if !cronpkg.LockFileExists(refs2.Cron.Dir()) {
		t.Fatal("auto-started manager does not own the projectspace lock")
	}
	eng.SetToolRefs(ToolRefs{Cron: refs2.Cron})
	eng.Close()
	if cronpkg.LockFileExists(refs2.Cron.Dir()) {
		t.Error("engine Close left the scheduler lock behind")
	}
}

// TestCronRunAgentSubCronLifecycle drives a REAL RunAgent whose sub-agent
// calls the Cron tool (session create). The sub's sessionOnly manager starts
// on create; RunAgent's deferred subRefs.Cron.Stop() tears it down when the
// run returns. The sub manager is unreferencable from outside RunAgent, so
// teardown is pinned through the package-level live-manager counter: Stop
// joins the tick goroutine, and run() decrements before closing done —
// back-to-baseline after RunAgent means no tick loop leaked.
func TestCronRunAgentSubCronLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()

	mp := &mockProvider{}
	// Sub-agent script: create a session cron, then finish.
	mp.addResponse(toolUseStreamEvents("parent-model", "tsub", "Cron", `{"action":"create","cron":"7 * * * *","prompt":"sub session cron","recurring":false}`), nil)
	mp.addResponse(textStreamEvents("parent-model", "sub created its cron"), nil)

	eng := newRunAgentEngine(&Params{
		Provider: mp,
		Model:    "parent-model",
		Logger:   silentLogger(),
	})
	t.Cleanup(func() { eng.Close() })
	deps := SharedDeps{
		WorkingDir: wd,
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	eng.SetSharedDeps(&deps)

	baseline := cronpkg.LiveManagers()
	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "create a session cron then finish",
		AgentType: "General",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	// The tool result rode the sub's second request: the wire text proves
	// the sub's own Cron tool executed.
	if !mp.anyRequestContains("Scheduled one-shot task", "Session-only") {
		t.Fatal("sub-agent never created a cron through its own Cron tool")
	}
	if got := cronpkg.LiveManagers(); got != baseline {
		t.Errorf("live cron managers after RunAgent = %d, want baseline %d (sub manager tick loop leaked)", got, baseline)
	}
}

// TestCronWireEngineSinksMainEngine pins WireEngine's SetSink for a MAIN
// (non-sub) engine: a session cron fires into the wired engine's history.
func TestCronWireEngineSinksMainEngine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	tl := cronTestTaskList(t)
	deps := SharedDeps{WorkingDir: wd}
	refs := CreateTools(deps, tl)
	t.Cleanup(refs.Cron.Stop)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "sinked"), nil)
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		Logger:     slog.Default(),
		Dispatcher: &mockDispatcher{},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSystemPrompt("sys")
	eng.SetToolRefs(refs)
	WireEngine(eng, refs, nil)

	orig := time.Local
	time.Local = time.FixedZone("CST-TEST", 8*3600)
	t.Cleanup(func() { time.Local = orig })
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	clock := &fakeClock{}
	clock.nano.Store(t0.UnixNano())
	refs.Cron.SetNow(clock.Now)

	if _, err := refs.Cron.Create("* * * * *", "sink proof", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.Add(61 * time.Second)
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: 1s ticker
	fired := false
	for time.Now().Before(deadline) { // REAL-TIME poll loop
		for _, msg := range eng.Messages() {
			if prompt, ok := utils.UnwrapCronReminderEnvelope(utils.FirstTextBlockContent(msg)); ok && prompt == "sink proof" {
				fired = true
			}
		}
		if fired {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if !fired {
		t.Fatal("WireEngine did not sink the main engine into the cron manager — the fire never landed")
	}
	waitIdle(t, eng)
}
