package cron

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// fakeClock is an atomic test clock: the ticker goroutine reads it every
// second while tests advance it.
type fakeClock struct{ nano atomic.Int64 }

func newFakeClock(t time.Time) *fakeClock {
	var c fakeClock
	c.nano.Store(t.UnixNano())
	return &c
}

func (c *fakeClock) Now() time.Time                { return time.Unix(0, c.nano.Load()) }
func (c *fakeClock) Set(t time.Time)               { c.nano.Store(t.UnixNano()) }
func (c *fakeClock) Add(d time.Duration) time.Time { return time.Unix(0, c.nano.Add(int64(d))) }

// fakeSink records enqueued items and toggles busy.
type fakeSink struct {
	mu    sync.Mutex
	items []types.QueuedItem
	busy  bool
}

func (s *fakeSink) EnqueueAttachment(item types.QueuedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
}

func (s *fakeSink) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *fakeSink) recorded() []types.QueuedItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]types.QueuedItem(nil), s.items...)
}

// newTestManager builds a started Manager (lock owned, ticker running) with
// a fake clock at t0 and a recording sink. check() may also be called
// directly — Manager.mu serializes it against the ticker.
func newTestManager(t *testing.T, dir string) (*Manager, *fakeSink, *fakeClock) {
	t.Helper()
	pinLocalCST(t)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	clock := newFakeClock(t0)
	m := NewManager(dir)
	sink := &fakeSink{}
	m.SetSink(sink)
	m.SetNow(clock.Now)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)
	return m, sink, clock
}

func TestOneShotSessionFire(t *testing.T) {
	m, sink, clock := newTestManager(t, t.TempDir())
	id, err := m.Create("* * * * *", "check the time", false, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(id) != 8 {
		t.Errorf("id = %q, want 8 hex chars", id)
	}

	// Before the fire time: nothing.
	m.check()
	if items := sink.recorded(); len(items) != 0 {
		t.Fatalf("check before fire time enqueued %d items, want 0", len(items))
	}

	// Past the next match: exactly one prompt item with cron origin.
	clock.Add(61 * time.Second)
	m.check()
	items := sink.recorded()
	if len(items) != 1 {
		t.Fatalf("check after fire time enqueued %d items, want 1", len(items))
	}
	it := items[0]
	if it.Value != "check the time" {
		t.Errorf("Value = %q, want the prompt", it.Value)
	}
	if it.Mode != types.ItemModePrompt {
		t.Errorf("Mode = %q, want prompt", it.Mode)
	}
	if it.Origin == nil || it.Origin.Kind != types.OriginCron {
		t.Errorf("Origin = %+v, want cron kind", it.Origin)
	}
	if it.Priority != types.PriorityNext {
		t.Errorf("Priority = %q, want next", it.Priority)
	}
	if it.UUID == "" {
		t.Error("UUID is empty")
	}
	if tasks := m.List(); len(tasks) != 0 {
		t.Errorf("session store after one-shot fire = %d tasks, want 0", len(tasks))
	}
	// Second check at the same now: no double fire.
	m.check()
	if items := sink.recorded(); len(items) != 1 {
		t.Errorf("second check enqueued %d more items, want still 1 total", len(items))
	}
}

func TestOneShotDurableFireRemovesFromFile(t *testing.T) {
	dir := t.TempDir()
	m, sink, clock := newTestManager(t, dir)
	if _, err := m.Create("0 6 * * *", "durable ping", false, true); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !m.store.HasTasks() {
		t.Fatal("durable create did not write the file")
	}
	// 2026-11-11 10:00 → next 06:00 fire is tomorrow; jump past it.
	clock.Add(20 * 3600 * time.Second)
	m.check()
	items := sink.recorded()
	if len(items) != 1 || items[0].Value != "durable ping" {
		t.Fatalf("durable one-shot fire items = %+v", items)
	}
	if m.store.HasTasks() {
		t.Error("one-shot durable task still in file after fire")
	}
}

func TestRecurringFireReschedulesFromNow(t *testing.T) {
	dir := t.TempDir()
	m, sink, clock := newTestManager(t, dir)
	t0 := clock.Now()
	id, err := m.Create("*/5 * * * *", "hydration check", true, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The first fire is at jittered(*/5 from createdAt) — id-dependent, so
	// derive it instead of hardcoding 10:05.
	firstNext, ok := JitteredNextCronRunMs("*/5 * * * *", t0.UnixMilli(), id)
	if !ok {
		t.Fatal("JitteredNextCronRunMs failed")
	}
	fireTime := time.UnixMilli(firstNext).Add(time.Second)
	clock.Set(fireTime)
	m.check()
	if items := sink.recorded(); len(items) != 1 {
		t.Fatalf("first recurring fire enqueued %d items, want 1", len(items))
	}
	// lastFiredAt persisted in the file.
	tasks := m.store.Read()
	if len(tasks) != 1 || tasks[0].LastFiredAt != fireTime.UnixMilli() {
		t.Fatalf("lastFiredAt not persisted: %+v (want %d)", tasks, fireTime.UnixMilli())
	}
	// Immediate re-check at the same instant must not refire (reschedule from
	// now, not from the old next).
	m.check()
	if items := sink.recorded(); len(items) != 1 {
		t.Errorf("immediate re-check fired again: %d items", len(items))
	}
	// The next fire time is anchored from the fire time.
	wantNext, ok := JitteredNextCronRunMs("*/5 * * * *", fireTime.UnixMilli(), id)
	if !ok {
		t.Fatal("JitteredNextCronRunMs failed")
	}
	m.mu.Lock()
	gotNext := m.nextFireAt[id]
	m.mu.Unlock()
	if gotNext != wantNext {
		t.Errorf("next fire = %d, want %d (rescheduled from fire time)", gotNext, wantNext)
	}
	// Task stays in the file (recurring).
	if !m.store.HasTasks() {
		t.Error("recurring durable task removed after fire")
	}
}

func TestFirstSightAnchorsFromLastFiredAt(t *testing.T) {
	dir := t.TempDir()
	m, sink, clock := newTestManager(t, dir)
	prevFire := clock.Now().Add(-3 * time.Minute)
	if err := m.store.Add(Task{
		ID: "aa112233", Cron: "*/5 * * * *", Prompt: "stale anchor",
		CreatedAt: clock.Now().Add(-24 * time.Hour).UnixMilli(), LastFiredAt: prevFire.UnixMilli(), Recurring: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First sight anchors from lastFiredAt: next = jittered(*/5 from -3min)
	// = up to +5min+jitter away — a now-anchored computation would instead
	// fire immediately.
	m.check()
	if items := sink.recorded(); len(items) != 0 {
		t.Fatalf("first sight fired %d items, want 0 (anchored from lastFiredAt)", len(items))
	}
	// ...and it does fire once the lastFiredAt-anchored next passes.
	want, ok := JitteredNextCronRunMs("*/5 * * * *", prevFire.UnixMilli(), "aa112233")
	if !ok {
		t.Fatal("JitteredNextCronRunMs failed")
	}
	clock.Set(time.UnixMilli(want).Add(time.Second))
	m.check()
	if items := sink.recorded(); len(items) != 1 || items[0].Value != "stale anchor" {
		t.Fatalf("anchored fire items = %+v", items)
	}
}

func TestIsBusyGate(t *testing.T) {
	m, sink, clock := newTestManager(t, t.TempDir())
	if _, err := m.Create("* * * * *", "gated", false, false); err != nil {
		t.Fatalf("Create: %v", err)
	}
	clock.Add(61 * time.Second)
	sink.busy = true
	m.check()
	if items := sink.recorded(); len(items) != 0 {
		t.Fatalf("busy check fired %d items, want 0", len(items))
	}
	sink.busy = false
	m.check()
	if items := sink.recorded(); len(items) != 1 {
		t.Fatalf("idle check fired %d items, want 1", len(items))
	}
}

func TestAgedRecurringFinalFireThenDelete(t *testing.T) {
	dir := t.TempDir()
	m, sink, clock := newTestManager(t, dir)
	old := clock.Now().Add(-8 * 24 * time.Hour)
	if err := m.store.Add(Task{ID: "aged0001", Cron: "* * * * *", Prompt: "old timer", CreatedAt: old.UnixMilli(), Recurring: true}); err != nil {
		t.Fatalf("seed aged: %v", err)
	}
	if err := m.store.Add(Task{ID: "perm0001", Cron: "* * * * *", Prompt: "forever", CreatedAt: old.UnixMilli(), Recurring: true, Permanent: true}); err != nil {
		t.Fatalf("seed permanent: %v", err)
	}
	m.check()
	items := sink.recorded()
	if len(items) != 2 {
		t.Fatalf("aged+permanent fires = %d items, want 2", len(items))
	}
	vals := map[string]bool{}
	for _, it := range items {
		vals[it.Value] = true
	}
	if !vals["old timer"] || !vals["forever"] {
		t.Errorf("fired values = %v", vals)
	}
	remaining := map[string]bool{}
	for _, task := range m.store.Read() {
		remaining[task.ID] = true
	}
	if remaining["aged0001"] {
		t.Error("aged recurring task survived its final fire")
	}
	if !remaining["perm0001"] {
		t.Error("permanent task was deleted despite the exemption")
	}
}

func TestMissedOnStartOwnerGated(t *testing.T) {
	dir := t.TempDir()
	pinLocalCST(t)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	s := storeFor(dir)
	if err := s.Add(Task{ID: "miss0001", Cron: "0 6 * * *", Prompt: "missed one-shot", CreatedAt: t0.Add(-7 * time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Add(Task{ID: "miss0002", Cron: "0 6 * * *", Prompt: "missed recurring", CreatedAt: t0.Add(-7 * time.Hour).UnixMilli(), Recurring: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := NewManager(dir)
	sink := &fakeSink{}
	clock := newFakeClock(t0)
	m.SetSink(sink)
	m.SetNow(clock.Now)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)

	items := sink.recorded()
	if len(items) != 1 {
		t.Fatalf("Start enqueued %d items, want exactly 1 missed notification", len(items))
	}
	notif := items[0].Value
	for _, want := range []string{
		"The following one-shot scheduled task was missed while gbot was not running.",
		"already been removed",
		"Do NOT execute this prompt yet.",
		"ask the user in your reply whether to run it now",
		"Every day at 06:00",
		"```\nmissed one-shot\n```",
	} {
		if !strings.Contains(notif, want) {
			t.Errorf("notification missing %q:\n%s", want, notif)
		}
	}
	if strings.Contains(notif, "missed recurring") {
		t.Error("recurring task leaked into the missed notification (check() owns it)")
	}
	// One-shot deleted from the file; recurring stays.
	remaining := map[string]bool{}
	for _, task := range s.Read() {
		remaining[task.ID] = true
	}
	if remaining["miss0001"] {
		t.Error("missed one-shot still in file after notification")
	}
	if !remaining["miss0002"] {
		t.Error("missed recurring removed from file (must fire via check())")
	}
	// The recurring task fires on the first check instead.
	m.check()
	items = sink.recorded()
	if len(items) != 2 || items[1].Value != "missed recurring" {
		t.Fatalf("post-Start check items = %+v, want the recurring prompt second", items)
	}
}

func TestMissedNotificationPluralAndFence(t *testing.T) {
	pinLocalCST(t)
	created := time.Date(2026, 11, 4, 9, 0, 0, 0, time.Local)
	text := BuildMissedTaskNotification([]Task{
		{ID: "aabb0001", Cron: "0 9 * * 3", Prompt: "first ``` prompt", CreatedAt: created.UnixMilli()},
		{ID: "aabb0002", Cron: "30 14 * * *", Prompt: "second", CreatedAt: created.UnixMilli()},
	})
	for _, want := range []string{
		"The following one-shot scheduled tasks were missed while gbot was not running.",
		"They have already been removed",
		"Do NOT execute these prompts yet.",
		"whether to run each one now",
		"Every Wednesday at 09:00",
		"Every day at 14:30",
		"created 2026-11-04 09:00",
		// Longest backtick run in "first ``` prompt" is 3 → 4-backtick fence.
		"````\nfirst ``` prompt\n````",
		"```\nsecond\n```",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plural notification missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "task was missed") {
		t.Error("singular header leaked into plural notification")
	}
}

func TestNonOwnerSkipsFileTasks(t *testing.T) {
	dir := t.TempDir()
	pinLocalCST(t)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	// An external live owner (our own pid, different identity).
	ext := NewLock(dir, "external-owner")
	if !ext.TryAcquire() {
		t.Fatal("external owner could not acquire")
	}
	t.Cleanup(ext.Release)

	if err := storeFor(dir).Add(Task{ID: "file0001", Cron: "* * * * *", Prompt: "file task", CreatedAt: t0.Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := NewManager(dir)
	sink := &fakeSink{}
	clock := newFakeClock(t0)
	m.SetSink(sink)
	m.SetNow(clock.Now)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)
	// Session task still fires on the non-owner.
	if _, err := m.Create("* * * * *", "session task", false, false); err != nil {
		t.Fatalf("Create: %v", err)
	}
	clock.Add(61 * time.Second)
	m.check()
	items := sink.recorded()
	values := []string{}
	for _, it := range items {
		values = append(values, it.Value)
	}
	if len(items) != 1 || items[0].Value != "session task" {
		t.Fatalf("non-owner check items = %v, want only the session task", values)
	}
	if storeFor(dir).HasTasks() && len(storeFor(dir).Read()) != 1 {
		t.Error("non-owner mutated the file")
	}
}

func TestExternalEditSeenByCheck(t *testing.T) {
	dir := t.TempDir()
	m, sink, clock := newTestManager(t, dir)
	// "Another process" writes a task directly.
	if err := storeFor(dir).Write([]Task{{ID: "extl0001", Cron: "* * * * *", Prompt: "from outside", CreatedAt: clock.Now().Add(-time.Minute).UnixMilli()}}); err != nil {
		t.Fatalf("external write: %v", err)
	}
	clock.Add(2 * time.Minute)
	m.check()
	items := sink.recorded()
	if len(items) != 1 || items[0].Value != "from outside" {
		t.Fatalf("external task not picked up: %+v", items)
	}
}

func TestEvictionOfDeletedIds(t *testing.T) {
	m, _, _ := newTestManager(t, t.TempDir())
	id, err := m.Create("* * * * *", "evict me", false, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// First sight seeds the schedule entry.
	m.check()
	m.mu.Lock()
	if _, ok := m.nextFireAt[id]; !ok {
		m.mu.Unlock()
		t.Fatal("nextFireAt missing the created task")
	}
	m.mu.Unlock()
	if err := m.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	m.check()
	m.mu.Lock()
	_, still := m.nextFireAt[id]
	m.mu.Unlock()
	if still {
		t.Error("deleted task's nextFireAt entry survived the check eviction")
	}
}

func TestStartIdempotentStopReleasesLock(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := newTestManager(t, dir)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	m.Stop()
	// Stoppable timer: a leaked time.After would fire sendTime→time.Now
	// during a LATER test's time.Local restore and trip -race.
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	select {
	case <-m.done:
	case <-timeout.C:
		t.Fatal("tick goroutine did not exit")
	}
	// Lock released: another manager in the same dir can acquire now.
	other := NewLock(dir, "other-identity")
	if !other.TryAcquire() {
		t.Fatal("lock still held after Stop")
	}
	other.Release()
	// Stop is idempotent.
	m.Stop()
	select {
	case <-m.done:
	default:
		t.Fatal("done not closed after second Stop")
	}
}

func TestNewManagerEmptyDirInert(t *testing.T) {
	m, _, _ := newTestManager(t, "")
	if _, err := m.Create("* * * * *", "p", false, false); err == nil {
		t.Fatal("Create on inert manager succeeded, want error")
	}
	if err := m.Start(); err != nil {
		t.Errorf("inert Start returned error: %v", err)
	}
	m.Stop()
}

func TestSessionOnlyManager(t *testing.T) {
	dir := t.TempDir()
	pinLocalCST(t)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	if err := storeFor(dir).Add(Task{ID: "file0009", Cron: "* * * * *", Prompt: "file task", CreatedAt: t0.Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := NewManager(dir)
	sink := &fakeSink{}
	clock := newFakeClock(t0)
	m.SetSink(sink)
	m.SetNow(clock.Now)
	m.SetSessionOnly()
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)
	// No lock acquired: another lock in the same dir succeeds while it runs.
	other := NewLock(dir, "other-identity")
	if !other.TryAcquire() {
		t.Fatal("sessionOnly manager acquired the scheduler lock")
	}
	other.Release()

	// durable create rejected with the CC teammate-reject semantics.
	_, err := m.Create("* * * * *", "p", false, true)
	if err == nil || err.Error() != "durable crons are not supported in sub-agent sessions (sub-agents do not persist across sessions)" {
		t.Fatalf("durable create error = %v, want sub-agent reject", err)
	}
	if _, err := m.Create("* * * * *", "sub session task", false, false); err != nil {
		t.Fatalf("session create: %v", err)
	}
	// Session task fires through check(); the file task is NEVER picked up.
	clock.Add(61 * time.Second)
	m.check()
	items := sink.recorded()
	if len(items) != 1 || items[0].Value != "sub session task" {
		vals := []string{}
		for _, it := range items {
			vals = append(vals, it.Value)
		}
		t.Fatalf("sessionOnly check items = %v, want only the session task", vals)
	}
	if tasks := storeFor(dir).Read(); len(tasks) != 1 || tasks[0].ID != "file0009" {
		t.Errorf("file surface touched by sessionOnly manager: %+v", tasks)
	}
}

func TestListStampsSessionTasks(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := newTestManager(t, dir)
	if err := storeFor(dir).Add(Task{ID: "durb0001", Cron: "0 9 * * *", Prompt: "file", CreatedAt: 1000, Recurring: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := m.Create("* * * * *", "mem", false, false); err != nil {
		t.Fatalf("Create: %v", err)
	}
	tasks := m.List()
	if len(tasks) != 2 {
		t.Fatalf("List = %d tasks, want 2", len(tasks))
	}
	byID := map[string]Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	file := byID["durb0001"]
	if !file.Durable || !file.Recurring {
		t.Errorf("file task = %+v, want durable+recurring", file)
	}
	var session Task
	for _, task := range tasks {
		if task.ID != "durb0001" {
			session = task
		}
	}
	if session.Durable {
		t.Errorf("session task = %+v, want Durable=false", session)
	}
	if session.Recurring {
		t.Errorf("session task = %+v, want one-shot (recurring=false passed)", session)
	}
}

func TestDeleteUnknownID(t *testing.T) {
	m, _, _ := newTestManager(t, t.TempDir())
	if err := m.Delete("nosuch0"); err == nil {
		t.Fatal("Delete unknown id succeeded, want error")
	}
	id, err := m.Create("* * * * *", "p", false, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if tasks := m.List(); len(tasks) != 0 {
		t.Errorf("List after delete = %+v, want empty", tasks)
	}
	// Durable delete flows to the file.
	dir := t.TempDir()
	m2, _, _ := newTestManager(t, dir)
	did, err := m2.Create("0 9 * * *", "p", false, true)
	if err != nil {
		t.Fatalf("Create durable: %v", err)
	}
	if err := m2.Delete(did); err != nil {
		t.Fatalf("Delete durable: %v", err)
	}
	if m2.store.HasTasks() {
		t.Error("durable delete left the task in the file")
	}
}

func TestConcurrentCreateTwoManagersSameDir(t *testing.T) {
	dir := t.TempDir()
	a, _, _ := newTestManager(t, dir)
	b, _, _ := newTestManager(t, dir)
	// Both creates genuinely in flight at the same instant (barrier start): a
	// shared-store Read→Write interleave would drop one of each pair.
	const rounds = 5
	var idsMu sync.Mutex
	var ids []string
	for range rounds {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		for _, m := range []*Manager{a, b} {
			go func(m *Manager) {
				defer wg.Done()
				<-start
				id, err := m.Create("0 9 * * *", "from both", true, true)
				if err != nil {
					t.Errorf("concurrent Create: %v", err)
					return
				}
				idsMu.Lock()
				ids = append(ids, id)
				idsMu.Unlock()
			}(m)
		}
		close(start)
		wg.Wait()
	}
	if len(ids) != 2*rounds {
		t.Fatalf("%d creates succeeded, want %d", len(ids), 2*rounds)
	}
	distinct := map[string]bool{}
	for _, id := range ids {
		distinct[id] = true
	}
	if len(distinct) != 2*rounds {
		t.Errorf("only %d distinct ids from %d creates", len(distinct), 2*rounds)
	}
	tasks := storeFor(dir).Read()
	if len(tasks) != 2*rounds {
		t.Fatalf("file holds %d tasks, want %d (a create was lost)", len(tasks), 2*rounds)
	}
	inFile := map[string]bool{}
	for _, task := range tasks {
		inFile[task.ID] = true
	}
	for _, id := range ids {
		if !inFile[id] {
			t.Errorf("task %s missing from the shared file", id)
		}
	}
	if got := len(a.List()); got != 2*rounds {
		t.Errorf("a.List = %d, want %d (shared store)", got, 2*rounds)
	}
}

// TestLiveManagersCounterTracksStartStop pins the package-level live-manager
// counter: +1 while a tick loop runs, back to baseline once Stop returns
// (run() decrements before closing done). Engine tests use the same counter
// to pin RunAgent sub-manager teardown.
func TestLiveManagersCounterTracksStartStop(t *testing.T) {
	dir := t.TempDir()
	pinLocalCST(t)
	m := NewManager(dir)
	baseline := LiveManagers()
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: goroutine start latency
	atPlusOne := false
	for time.Now().Before(deadline) { // REAL-TIME: poll loop
		if LiveManagers() == baseline+1 {
			atPlusOne = true
			break
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: poll loop
	}
	if !atPlusOne {
		t.Fatalf("LiveManagers never reached baseline+1 (%d), got %d", baseline+1, LiveManagers())
	}
	m.Stop()
	if got := LiveManagers(); got != baseline {
		t.Errorf("LiveManagers after Stop = %d, want baseline %d (tick loop leaked)", got, baseline)
	}
}

func TestEnabledEnvGate(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"0", true},
		{"false", true},
		{"1", false},
		{"TRUE", false},
		{" yes ", false},
		{"On", false},
		{"off", true},
		{"junk", true},
	}
	for _, tc := range cases {
		t.Setenv("GBOT_DISABLE_CRON", tc.value)
		if got := Enabled(); got != tc.want {
			t.Errorf("Enabled() with GBOT_DISABLE_CRON=%q = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestMissedAskedDedupSameIDTwice(t *testing.T) {
	dir := t.TempDir()
	pinLocalCST(t)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	s := storeFor(dir)
	for range 2 {
		if err := s.Add(Task{ID: "dup00001", Cron: "0 6 * * *", Prompt: "dup missed", CreatedAt: t0.Add(-7 * time.Hour).UnixMilli()}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	m := NewManager(dir)
	sink := &fakeSink{}
	clock := newFakeClock(t0)
	m.SetSink(sink)
	m.SetNow(clock.Now)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)
	items := sink.recorded()
	if len(items) != 1 {
		t.Fatalf("Start enqueued %d items, want 1 (duplicate id deduped)", len(items))
	}
	if got := strings.Count(items[0].Value, "dup missed"); got != 1 {
		t.Errorf("notification carries the prompt %d times, want 1", got)
	}
}

func TestLoadInitialNonOwnerNoNotification(t *testing.T) {
	dir := t.TempDir()
	pinLocalCST(t)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	if err := storeFor(dir).Add(Task{ID: "mssd0001", Cron: "0 6 * * *", Prompt: "missed", CreatedAt: t0.Add(-7 * time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ext := NewLock(dir, "external-owner")
	if !ext.TryAcquire() {
		t.Fatal("external owner could not acquire")
	}
	t.Cleanup(ext.Release)
	m := NewManager(dir)
	sink := &fakeSink{}
	m.SetSink(sink)
	m.SetNow(newFakeClock(t0).Now)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)
	if items := sink.recorded(); len(items) != 0 {
		t.Errorf("non-owner surfaced %d missed notifications, want 0 (owner-gated)", len(items))
	}
	if !storeFor(dir).HasTasks() {
		t.Error("non-owner removed the missed task from the file")
	}
}

func TestFiredItemCarriesTimestamp(t *testing.T) {
	m, sink, clock := newTestManager(t, t.TempDir())
	if _, err := m.Create("* * * * *", "stamped", false, false); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fireAt := clock.Add(61 * time.Second)
	m.check()
	items := sink.recorded()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Timestamp.UnixMilli() != fireAt.UnixMilli() {
		t.Errorf("item Timestamp = %v, want the fake now %v", items[0].Timestamp, fireAt)
	}
}

func TestCheckClearsScheduleWhenNoTasks(t *testing.T) {
	dir := t.TempDir()
	m, _, clock := newTestManager(t, dir)
	// A file task seeds a nextFireAt entry, then an external process removes
	// it; the next check must clear the whole schedule.
	if err := storeFor(dir).Write([]Task{{ID: "gonr0001", Cron: "0 9 * * *", Prompt: "p", CreatedAt: clock.Now().UnixMilli() - 3600*1000}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m.check()
	m.mu.Lock()
	_, seeded := m.nextFireAt["gonr0001"]
	m.mu.Unlock()
	if !seeded {
		t.Fatal("setup: nextFireAt not seeded")
	}
	if err := storeFor(dir).Write(nil); err != nil {
		t.Fatalf("external remove-all: %v", err)
	}
	m.check()
	m.mu.Lock()
	remaining := len(m.nextFireAt)
	m.mu.Unlock()
	if remaining != 0 {
		t.Errorf("nextFireAt kept %d entries with no live tasks, want 0", remaining)
	}
}

func TestListInertManager(t *testing.T) {
	m, _, _ := newTestManager(t, "")
	if got := m.List(); len(got) != 0 {
		t.Errorf("inert List = %+v, want empty", got)
	}
}

func TestMissedNotificationExactSingular(t *testing.T) {
	pinLocalCST(t)
	created := time.Date(2026, 11, 4, 9, 0, 0, 0, time.Local)
	got := BuildMissedTaskNotification([]Task{
		{ID: "aabb0001", Cron: "0 9 * * 3", Prompt: "single task", CreatedAt: created.UnixMilli()},
	})
	want := "The following one-shot scheduled task was missed while gbot was not running. " +
		"It has already been removed from the project's scheduled_tasks.json in the gbot projectspace.\n\n" +
		"Do NOT execute this prompt yet. First ask the user in your reply whether to run it now. " +
		"Only execute if the user confirms.\n\n" +
		"[Every Wednesday at 09:00, created 2026-11-04 09:00]\n" +
		"```\nsingle task\n```"
	if got != want {
		t.Errorf("singular notification =\n%q\nwant\n%q", got, want)
	}
}

// TestProbeTakesOverFromDeadOwner pins the 5s lock re-probe: when the owner
// releases (crash equivalent), a non-owner manager flips to owner and starts
// firing file tasks. Real-time bound: probe interval is 5s.
func TestProbeTakesOverFromDeadOwner(t *testing.T) {
	dir := t.TempDir()
	pinLocalCST(t)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	// Recurring (missed handling skips recurring; check() owns the fire) and
	// already due at the advanced clock.
	if err := storeFor(dir).Write([]Task{{ID: "take0001", Cron: "0 9 * * *", Prompt: "takeover task", CreatedAt: t0.Add(-25 * time.Hour).UnixMilli(), Recurring: true}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	owner := NewManager(dir)
	ownerSink := &fakeSink{}
	owner.SetSink(ownerSink)
	owner.SetNow(newFakeClock(t0).Now)
	if err := owner.Start(); err != nil {
		t.Fatalf("owner Start: %v", err)
	}
	if !owner.isOwnerNow() {
		t.Fatal("setup: first manager did not own the lock")
	}

	m := NewManager(dir)
	sink := &fakeSink{}
	clock := newFakeClock(t0)
	m.SetSink(sink)
	m.SetNow(clock.Now)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(m.Stop)
	if m.isOwnerNow() {
		t.Fatal("second manager stole a live owner's lock")
	}
	// Owner dies; the task's fire time passes; the probe flips ownership and
	// the task fires into the survivor.
	owner.Stop()
	clock.Add(3600 * time.Second)
	deadline := time.Now().Add(15 * time.Second) // REAL-TIME: 5s probe + 1s tick
	fired := ""
	for time.Now().Before(deadline) { // REAL-TIME poll loop
		for _, it := range sink.recorded() {
			if it.Value == "takeover task" {
				fired = it.Value
			}
		}
		if fired != "" {
			break
		}
		time.Sleep(50 * time.Millisecond) // REAL-TIME
	}
	if fired != "takeover task" {
		t.Fatal("the probe never took over the lock and fired the file task")
	}
}

// TestOneShotAnchorsWithoutRecurringJitter pins the one-shot first-sight
// branch: the fire time is the exact cron boundary (no forward recurring
// jitter), so a clock advanced exactly to the boundary fires. Under a
// recurring-anchored mutant the next would sit ~3.75 min later for this id.
func TestOneShotAnchorsWithoutRecurringJitter(t *testing.T) {
	dir := t.TempDir()
	m, sink, clock := newTestManager(t, dir)
	t0 := clock.Now()
	// Known id: frac(a0000001) ≈ 0.625 → recurring jitter on an hourly cron
	// would be 0.625*0.1*3600000 = 225000 ms past the boundary.
	if err := m.store.Add(Task{ID: "a0000001", Cron: "7 * * * *", Prompt: "sharp fire", CreatedAt: t0.UnixMilli()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fireAt := time.Date(t0.Year(), t0.Month(), t0.Day(), t0.Hour()+1, 7, 0, 0, time.Local)
	clock.Set(fireAt)
	m.check()
	items := sink.recorded()
	if len(items) != 1 || items[0].Value != "sharp fire" {
		t.Fatalf("one-shot did not fire exactly at the boundary: %d items", len(items))
	}
}

func TestStartAfterStopStaysStopped(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := newTestManager(t, dir)
	m.Stop()
	if err := m.Start(); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	if m.Running() {
		t.Error("manager restarted after Stop — stopped must be terminal (closed stopCh would end any new run instantly)")
	}
	m.Stop() // must not panic on the closed channel
}
