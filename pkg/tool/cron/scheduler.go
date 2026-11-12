// Per-engine cron scheduler Manager.
// Source: utils/cronScheduler.ts createCronScheduler + hooks/useScheduledTasks.ts
// fire path. Divergences (plan-approved): the missed-task surfacing is
// owner-gated (gbot runs many engines per process — CC's every-session
// surfacing would spam); the chokidar watcher is replaced by per-tick stat
// reloads; inFlight dedup is dropped (Go writes are synchronous inside
// check()); the fire rides EnqueueAttachment(ItemModePrompt, PriorityNext,
// OriginCron) — CC enqueues mode:'prompt', priority:'later', isMeta:true.
package cron

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/types"
)

const (
	checkInterval     = time.Second
	lockProbeInterval = 5 * time.Second
)

// Sink is the engine surface the scheduler fires into. The busy gate mirrors
// CC's isLoading() check — jobs only fire while the engine is idle.
type Sink interface {
	EnqueueAttachment(item types.QueuedItem)
	IsBusy() bool
}

// Manager schedules prompts for one engine: session tasks in memory, durable
// tasks in the per-dir file store. File tasks are processed only while the
// manager owns the directory's scheduler lock.
type Manager struct {
	dir   string
	store *Store
	lock  *Lock

	mu          sync.Mutex
	sink        Sink
	session     []Task
	nextFireAt  map[string]int64
	missedAsked map[string]bool
	sessionOnly bool
	isOwner     bool
	started     bool
	stopped     bool
	stopCh      chan struct{}
	done        chan struct{}
	nowFn       func() time.Time
}

// NewManager builds a Manager for a projectspace dir. dir == "" leaves it
// inert (Create errors, Start/Stop no-ops).
func NewManager(dir string) *Manager {
	m := &Manager{
		dir:         dir,
		nextFireAt:  map[string]int64{},
		missedAsked: map[string]bool{},
		stopCh:      make(chan struct{}),
		done:        make(chan struct{}),
		nowFn:       time.Now,
	}
	if dir != "" {
		m.store = storeFor(dir)
		m.lock = NewLock(dir, uuid.New().String())
	}
	return m
}

// SetSink wires the engine (called from WireEngine before Start).
func (m *Manager) SetSink(s Sink) {
	m.mu.Lock()
	m.sink = s
	m.mu.Unlock()
}

// SetSessionOnly switches the Manager to sub-agent semantics: ticker for
// session tasks only — no lock, no file store, durable creates rejected.
func (m *Manager) SetSessionOnly() {
	m.mu.Lock()
	m.sessionOnly = true
	m.mu.Unlock()
}

// SetNow overrides the clock (test seam).
func (m *Manager) SetNow(fn func() time.Time) {
	m.mu.Lock()
	m.nowFn = fn
	m.mu.Unlock()
}

// Dir returns the projectspace dir the durable store lives in ("" when
// inert).
func (m *Manager) Dir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dir
}

// Running reports whether the tick loop is active.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// StoreFor exposes the process-shared durable store for a dir (bootstrap and
// tests).
func StoreFor(dir string) *Store { return storeFor(dir) }

// liveManagers counts live Manager tick loops. run() decrements BEFORE
// closing done, so once Manager.Stop() returns (done observed closed) the
// decrement is already visible — the engine tests use that to pin sub-manager
// teardown against RunAgent's return.
var liveManagers atomic.Int32

// LiveManagers reports how many scheduler tick loops are running right now.
func LiveManagers() int32 { return liveManagers.Load() }

// LockFileExists reports whether a scheduler lock file is present in dir.
func LockFileExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, lockFileName))
	return err == nil
}

// Enabled reports whether cron tooling is active: GBOT_DISABLE_CRON truthy
// ({1,true,yes,on}, case-insensitive, trimmed — CC isEnvTruthy) disables it.
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GBOT_DISABLE_CRON")))
	switch v {
	case "1", "true", "yes", "on":
		return false
	}
	return true
}

// Start enables the scheduler: acquire-or-probe the lock, surface missed
// one-shots (owner only), run the 1s check ticker. Idempotent. sessionOnly
// Managers start the ticker only — no lock, no load.
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.dir == "" || m.started || m.stopped {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	if !m.sessionOnly {
		// First acquirer owns the file surface; others re-probe every 5s
		// until the owner dies (PID liveness in the lock body).
		m.isOwner = m.lock.TryAcquire()
	}
	sessionOnly := m.sessionOnly
	m.mu.Unlock()

	if !sessionOnly {
		m.loadInitial()
	}
	go m.run(sessionOnly)
	return nil
}

// StartIfTasksExist boots the scheduler when durable tasks exist (CC
// hasCronTasksSync auto-enable at start()).
func (m *Manager) StartIfTasksExist() {
	m.mu.Lock()
	dir := m.dir
	store := m.store
	m.mu.Unlock()
	if dir == "" {
		return
	}
	if store.HasTasks() {
		_ = m.Start()
	}
}

// Stop tears down the ticker goroutine (waiting for it to exit) and releases
// the lock. Idempotent; a stopped Manager never restarts (the closed stopCh
// would immediately end any new run).
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.dir == "" || !m.started {
		m.mu.Unlock()
		return
	}
	m.started = false
	m.stopped = true
	close(m.stopCh)
	lock := m.lock
	m.mu.Unlock()

	<-m.done
	if lock != nil {
		lock.Release()
	}
}

// run is the tick loop: 1s checks plus (for non-owners) a 5s lock re-probe.
func (m *Manager) run(sessionOnly bool) {
	liveManagers.Add(1)
	defer close(m.done)
	defer liveManagers.Add(-1)
	checkTick := time.NewTicker(checkInterval)
	defer checkTick.Stop()
	probeTick := time.NewTicker(lockProbeInterval)
	defer probeTick.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-checkTick.C:
			m.check()
		case <-probeTick.C:
			if sessionOnly {
				continue
			}
			m.mu.Lock()
			owner := m.isOwner
			m.mu.Unlock()
			if owner {
				continue
			}
			if m.lock.TryAcquire() {
				m.mu.Lock()
				m.isOwner = true
				m.mu.Unlock()
			}
		}
	}
}

// loadInitial runs CC's load(initial=true): refresh the store and surface
// missed ONE-SHOT tasks (owner-gated — D3). Recurring tasks are left to
// check(), which anchors from lastFiredAt and fires on the first tick.
func (m *Manager) loadInitial() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isOwner {
		return
	}
	m.store.Refresh()
	tasks := m.store.Read()
	nowMs := m.now().UnixMilli()
	var missed []Task
	var missedIDs []string
	for _, t := range FindMissedTasks(tasks, nowMs) {
		if t.Recurring || m.missedAsked[t.ID] {
			continue
		}
		m.missedAsked[t.ID] = true
		// nextFireAt sentinel: never let check() re-fire the raw prompt while
		// the removal lands (CC sets Infinity for the same window).
		m.nextFireAt[t.ID] = neverFireAt
		missed = append(missed, t)
		missedIDs = append(missedIDs, t.ID)
	}
	if len(missed) == 0 {
		return
	}
	m.fireLocked(BuildMissedTaskNotification(missed))
	if err := m.store.Remove(missedIDs); err != nil {
		// The notification already told the user the tasks were removed; a
		// failed removal means check() will fire them as normal overdue
		// one-shots — degraded, not lost.
		fmt.Fprintf(os.Stderr, "cron: remove missed tasks: %v\n", err)
	}
}

// check is the 1s tick body. Source: cronScheduler.ts check().
func (m *Manager) check() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sink == nil || m.sink.IsBusy() {
		return
	}
	now := m.now().UnixMilli()
	seen := map[string]bool{}
	// File-backed recurring tasks that fired this tick — batched into one
	// MarkFired write after the loop (N fires = one write).
	var firedFileRecurring []string

	// Shared loop body; isSession routes the one-shot cleanup path.
	process := func(t Task, isSession bool) {
		seen[t.ID] = true

		next, known := m.nextFireAt[t.ID]
		if !known {
			// First sight — anchor from lastFiredAt (recurring) or createdAt.
			// Never-fired recurring tasks use createdAt: anchoring from `now`
			// would compute next-year for pinned crons (`30 14 27 2 *`) when
			// isLoading delayed this tick past the fire time. Fired-before
			// tasks use lastFiredAt so a restarted process reconstructs the
			// same nextFireAt the prior one had in memory.
			if t.Recurring {
				from := t.CreatedAt
				if t.LastFiredAt != 0 {
					from = t.LastFiredAt
				}
				next = neverFireAt
				if n, ok := JitteredNextCronRunMs(t.Cron, from, t.ID); ok {
					next = n
				}
			} else {
				next = neverFireAt
				if n, ok := OneShotJitteredNextCronRunMs(t.Cron, t.CreatedAt, t.ID); ok {
					next = n
				}
			}
			m.nextFireAt[t.ID] = next
		}

		if now < next {
			return
		}

		m.fireLocked(t.Prompt)

		// Aged-out recurring tasks fall through to the one-shot delete paths:
		// they fire one last time, then are removed.
		aged := IsRecurringTaskAged(t, now, RecurringMaxAgeMs)
		if t.Recurring && !aged {
			// Reschedule from now (not from next) to avoid rapid catch-up if
			// the engine was blocked.
			var newNext int64 = neverFireAt
			if n, ok := JitteredNextCronRunMs(t.Cron, now, t.ID); ok {
				newNext = n
			}
			m.nextFireAt[t.ID] = newNext
			if !isSession {
				firedFileRecurring = append(firedFileRecurring, t.ID)
			}
		} else if isSession {
			m.session = removeSessionTasks(m.session, []string{t.ID})
			delete(m.nextFireAt, t.ID)
		} else {
			_ = m.store.Remove([]string{t.ID})
			delete(m.nextFireAt, t.ID)
		}
	}

	// File-backed tasks: only when we own the scheduler lock — the lock stops
	// two gbot engines in the same dir from double-firing one on-disk task.
	if m.isOwner {
		m.store.Refresh()
		for _, t := range m.store.Read() {
			process(t, false)
		}
		if len(firedFileRecurring) > 0 {
			_ = m.store.MarkFired(firedFileRecurring, now)
		}
	}
	// Session-only tasks: process-private, the lock does not apply.
	for _, t := range append([]Task(nil), m.session...) {
		process(t, true)
	}

	if len(seen) == 0 {
		// No live tasks this tick — clear the whole schedule.
		m.nextFireAt = map[string]int64{}
		return
	}
	// Evict schedule entries for tasks no longer present.
	for id := range m.nextFireAt {
		if !seen[id] {
			delete(m.nextFireAt, id)
		}
	}
}

func (m *Manager) now() time.Time {
	return m.nowFn()
}

// fireLocked enqueues the fired prompt through the engine's idle-wake path;
// check()/loadInitial call it with m.mu already held so ticker ticks and
// direct test calls cannot double-fire.
func (m *Manager) fireLocked(prompt string) {
	if m.sink == nil {
		return
	}
	m.sink.EnqueueAttachment(types.QueuedItem{
		UUID:      uuid.New().String(),
		Value:     prompt,
		Mode:      types.ItemModePrompt,
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginCron},
		Timestamp: m.now(),
	})
}

// Create mints an 8-hex id, stores the task (session memory or file), and
// enables the scheduler so the task fires this session (CC
// setScheduledTasksEnabled(true)).
func (m *Manager) Create(cron, prompt string, recurring, durable bool) (string, error) {
	id, err := m.create(cron, prompt, recurring, durable)
	if err != nil {
		return "", err
	}
	_ = m.Start()
	return id, nil
}

func (m *Manager) create(cron, prompt string, recurring, durable bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dir == "" {
		return "", errors.New("cron: scheduler not available (no project directory)")
	}
	if durable && m.sessionOnly {
		return "", errors.New("durable crons are not supported in sub-agent sessions (sub-agents do not persist across sessions)")
	}
	id := uuid.New().String()[:8]
	t := Task{ID: id, Cron: cron, Prompt: prompt, CreatedAt: m.now().UnixMilli(), Recurring: recurring}
	if durable {
		if err := m.store.Add(t); err != nil {
			return "", fmt.Errorf("cron: persist task: %w", err)
		}
	} else {
		m.session = append(m.session, t)
	}
	return id, nil
}

// List returns durable + session tasks; session tasks are stamped
// Durable=false so callers can distinguish them.
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dir == "" {
		tasks := append([]Task(nil), m.session...)
		for i := range tasks {
			tasks[i].Durable = false
		}
		return tasks
	}
	m.store.Refresh()
	out := append([]Task(nil), m.store.Read()...)
	for i := range out {
		out[i].Durable = true
	}
	for _, t := range m.session {
		t.Durable = false
		out = append(out, t)
	}
	return out
}

// Delete removes by id: session sweep first, then the file store. Unknown
// ids error (the tool layer checks List first and owns the user-facing
// message; this is the defense in depth).
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dir == "" {
		return fmt.Errorf("cron: unknown task id %q", id)
	}
	remaining, removed := filterSession(m.session, id)
	if removed {
		m.session = remaining
		delete(m.nextFireAt, id)
		return nil
	}
	tasks := m.store.Read()
	found := false
	for _, t := range tasks {
		if t.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("cron: unknown task id %q", id)
	}
	if err := m.store.Remove([]string{id}); err != nil {
		return err
	}
	delete(m.nextFireAt, id)
	return nil
}

func removeSessionTasks(tasks []Task, ids []string) []Task {
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	out := tasks[:0:0]
	for _, t := range tasks {
		if !idSet[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

func filterSession(tasks []Task, id string) ([]Task, bool) {
	for i, t := range tasks {
		if t.ID == id {
			out := append([]Task(nil), tasks[:i]...)
			return append(out, tasks[i+1:]...), true
		}
	}
	return tasks, false
}

// BuildMissedTaskNotification renders the missed one-shot catch-up prompt.
// Guidance precedes the task list and each prompt is wrapped in a code fence
// one longer than any backtick run inside it, so a multi-line prompt cannot
// close the fence early and un-wrap trailing text (CommonMark rule). Source:
// cronScheduler.ts buildMissedTaskNotification — reworded for gbot (no
// AskUserQuestion tool; projectspace wording; fixed date format).
func BuildMissedTaskNotification(missed []Task) string {
	plural := len(missed) > 1
	taskWord := "task was"
	pronoun := "It has"
	executeWord := "this prompt yet"
	eachWord := "it"
	if plural {
		taskWord = "tasks were"
		pronoun = "They have"
		executeWord = "these prompts yet"
		eachWord = "each one"
	}
	header := "The following one-shot scheduled " + taskWord + " missed while gbot was not running. " +
		pronoun + " already been removed from the project's scheduled_tasks.json in the gbot projectspace.\n\n" +
		"Do NOT execute " + executeWord + ". " +
		"First ask the user in your reply whether to run " + eachWord + " now. " +
		"Only execute if the user confirms."

	blocks := make([]string, 0, len(missed))
	for _, t := range missed {
		meta := "[" + CronToHuman(t.Cron) + ", created " +
			time.UnixMilli(t.CreatedAt).Local().Format("2006-01-02 15:04") + "]"
		fence := strings.Repeat("`", max(3, longestBacktickRun(t.Prompt)+1))
		blocks = append(blocks, meta+"\n"+fence+"\n"+t.Prompt+"\n"+fence)
	}
	return header + "\n\n" + strings.Join(blocks, "\n\n")
}

// longestBacktickRun returns the longest consecutive backtick run in s.
func longestBacktickRun(s string) int {
	longest, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			cur++
			if cur > longest {
				longest = cur
			}
			continue
		}
		cur = 0
	}
	return longest
}

// isOwnerNow is the lock-ownership probe for tests.
func (m *Manager) isOwnerNow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isOwner
}
