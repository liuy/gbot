// Task, per-dir Store, jitter and missed/aged helpers.
// Source: utils/cronTasks.ts. Divergences (plan-approved): atomic writes
// (temp+rename) replace CC's chokidar awaitWriteFinish stability window; the
// process-shared storeFor map replaces CC's file watcher for same-process
// visibility.
package cron

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Task is one scheduled prompt. Source: utils/cronTasks.ts CronTask. The
// on-disk shape is { id, cron, prompt, createdAt, lastFiredAt?, recurring?,
// permanent? } — the runtime-only Durable flag is stripped on write (CC strips
// its durable flag the same way).
type Task struct {
	ID          string `json:"id"`
	Cron        string `json:"cron"`
	Prompt      string `json:"prompt"`
	CreatedAt   int64  `json:"createdAt"`
	LastFiredAt int64  `json:"lastFiredAt,omitempty"`
	Recurring   bool   `json:"recurring,omitempty"`
	Permanent   bool   `json:"permanent,omitempty"`
	Durable     bool   `json:"-"`
}

const tasksFileName = "scheduled_tasks.json"

// Tuning constants. Source: utils/cronTasks.ts DEFAULT_CRON_JITTER_CONFIG —
// fixed values, no runtime config knob (CC sources them from GrowthBook).
const (
	MaxJobs           = 50
	RecurringMaxAgeMs = 7 * 24 * 60 * 60 * 1000
	MaxAgeDays        = 7

	recurringFrac    = 0.1
	recurringCapMs   = 15 * 60 * 1000
	oneShotMaxMs     = 90 * 1000
	oneShotFloorMs   = 0
	oneShotMinuteMod = 30
)

// storeFor returns the process-shared Store for dir. Engines in one process
// see each other's creates/deletes immediately through the shared cache (CC:
// the chokidar file watcher).
var storeForMap sync.Map // abs dir → *Store

func storeFor(dir string) *Store {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if v, ok := storeForMap.Load(abs); ok {
		return v.(*Store)
	}
	s := &Store{dir: abs}
	actual, loaded := storeForMap.LoadOrStore(abs, s)
	if loaded {
		return actual.(*Store)
	}
	return s
}

// fileStat identifies the file state a cache snapshot was built from. A
// same-size rewrite within mtime granularity is not detectable; Linux mtime
// nanosecond resolution makes that a non-issue in practice.
type fileStat struct {
	exists bool
	mod    time.Time
	size   int64
}

func statFile(path string) fileStat {
	var st fileStat
	if fi, err := os.Stat(path); err == nil {
		st.exists = true
		st.mod = fi.ModTime()
		st.size = fi.Size()
	}
	return st
}

// Store is the per-dir durable task file with an in-memory cache. Read/
// HasTasks serve from cache and re-stat the file so external edits (another
// gbot process) reload transparently. All mutations write through.
type Store struct {
	dir string

	mu     sync.Mutex
	cached []Task
	stat   fileStat
	loaded bool
}

func (s *Store) path() string { return filepath.Join(s.dir, tasksFileName) }

// Refresh re-reads the file into the cache if its stat changed. The loaded
// flag makes the first call unconditional; later calls compare (mtime, size,
// exists) so external writes (another gbot process) reload transparently.
func (s *Store) Refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
}

func (s *Store) refreshLocked() {
	st := statFile(s.path())
	if s.loaded && st == s.stat {
		return
	}
	s.cached = readTasksFile(s.path())
	s.stat = st
	s.loaded = true
}

// Read returns the durable tasks; malformed rows and rows with invalid cron
// strings are silently dropped (CC logs at debug and skips). Missing or
// malformed file → empty.
func (s *Store) Read() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.cached
}

// HasTasks reports whether the file holds any valid task.
func (s *Store) HasTasks() bool {
	return len(s.Read()) > 0
}

// Write overwrites the tasks file atomically (temp file + rename — readers
// never see torn JSON). Empty list writes {"tasks": []} rather than deleting
// so a stat-based reload sees the last-delete (CC: watcher change event).
func (s *Store) Write(tasks []Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(tasks)
}

// writeLocked is Write with s.mu held. Add/Remove/MarkFired keep the lock
// across their whole read-modify-write: the Store is process-shared
// (storeFor), so two Managers interleaving Read→Write would lose a task.
func (s *Store) writeLocked(tasks []Task) error {
	if tasks == nil {
		tasks = []Task{}
	}
	body, err := json.MarshalIndent(struct {
		Tasks []Task `json:"tasks"`
	}{tasks}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, tasksFileName+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path()); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	s.cached = append([]Task(nil), tasks...)
	s.stat = statFile(s.path())
	s.loaded = true
	return nil
}

// Add appends a task (read-modify-write under one lock hold).
func (s *Store) Add(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	tasks := append(append([]Task(nil), s.cached...), t)
	return s.writeLocked(tasks)
}

// Remove drops the given ids. No-op if none match (another session raced us).
func (s *Store) Remove(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	var remaining []Task
	for _, t := range s.cached {
		if !idSet[t.ID] {
			remaining = append(remaining, t)
		}
	}
	if len(remaining) == len(s.cached) {
		return nil
	}
	return s.writeLocked(remaining)
}

// MarkFired stamps lastFiredAt on the given ids and writes back. Batched so N
// fires in one tick = one write. No-op if none match.
func (s *Store) MarkFired(ids []string, firedAt int64) error {
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	// Copy before stamping: Read hands callers s.cached's backing array, so
	// an in-place write would race another engine's List()/Read() loop.
	tasks := append([]Task(nil), s.cached...)
	changed := false
	for i := range tasks {
		if idSet[tasks[i].ID] {
			tasks[i].LastFiredAt = firedAt
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.writeLocked(tasks)
}

// readTasksFile parses the tasks file, validating each row the way CC's
// readCronTasks does: per-field types must match, and the cron string must
// re-validate — a single bad entry never blocks the whole file.
func readTasksFile(path string) []Task {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil
	}
	var out []Task
	for _, row := range file.Tasks {
		var f struct {
			ID          any `json:"id"`
			Cron        any `json:"cron"`
			Prompt      any `json:"prompt"`
			CreatedAt   any `json:"createdAt"`
			LastFiredAt any `json:"lastFiredAt"`
			Recurring   any `json:"recurring"`
			Permanent   any `json:"permanent"`
		}
		if err := json.Unmarshal(row, &f); err != nil {
			continue
		}
		id, ok := f.ID.(string)
		if !ok {
			continue
		}
		cronExpr, ok := f.Cron.(string)
		if !ok {
			continue
		}
		prompt, ok := f.Prompt.(string)
		if !ok {
			continue
		}
		createdAtF, ok := f.CreatedAt.(float64)
		if !ok {
			continue
		}
		if ParseCronExpression(cronExpr) == nil {
			continue
		}
		t := Task{ID: id, Cron: cronExpr, Prompt: prompt, CreatedAt: int64(createdAtF)}
		if lf, ok := f.LastFiredAt.(float64); ok {
			t.LastFiredAt = int64(lf)
		}
		if r, ok := f.Recurring.(bool); ok && r {
			t.Recurring = true
		}
		if p, ok := f.Permanent.(bool); ok && p {
			t.Permanent = true
		}
		out = append(out, t)
	}
	return out
}

// jitterFrac maps a task id to a deterministic [0,1) fraction. Source:
// cronTasks.ts jitterFrac — parseInt(id.slice(0,8), 16) parses the LEADING HEX
// RUN as a prefix (JS parseInt stops at the first non-hex char: "12zz" → 0x12,
// "zz12" → NaN → 0). Not replicated: JS's optional 0x radix prefix, only
// reachable from hand-edited files (minted ids are uuid hex) and safe either
// way with the 0 floor.
func jitterFrac(taskID string) float64 {
	end := 0
	max := min(len(taskID), 8)
	for end < max {
		c := taskID[end]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0
	}
	v, err := parseUintHex(taskID[:end])
	if err != nil {
		return 0
	}
	return float64(v) / 0x1_0000_0000
}

func parseUintHex(s string) (uint64, error) {
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			return 0, errBadHex
		}
		v = v<<4 | d
	}
	return v, nil
}

type hexError struct{}

func (hexError) Error() string { return "cron: invalid hex digit" }

var errBadHex error = hexError{}

// JitteredNextCronRunMs is nextCronRunMs plus a deterministic forward delay
// (frac of the fire gap, capped) so fleet-wide same-cron sessions don't hit
// inference at the same instant. No second match to proportion against →
// fire on t1. Source: cronTasks.ts jitteredNextCronRunMs.
func JitteredNextCronRunMs(cron string, fromMs int64, taskID string) (int64, bool) {
	t1, ok := NextCronRunMs(cron, fromMs)
	if !ok {
		return 0, false
	}
	t2, ok := NextCronRunMs(cron, t1)
	if !ok {
		return t1, true
	}
	jitter := jitterFrac(taskID) * recurringFrac * float64(t2-t1)
	if jitter > recurringCapMs {
		jitter = recurringCapMs
	}
	return t1 + int64(jitter), true
}

// OneShotJitteredNextCronRunMs is nextCronRunMs minus a deterministic lead
// when the computed fire time lands on a minute matching oneShotMinuteMod
// (:00/:30 at defaults — the human-rounding hotspots). One-shots are
// user-pinned so only EARLY is acceptable, clamped to fromMs. Source:
// cronTasks.ts oneShotJitteredNextCronRunMs.
func OneShotJitteredNextCronRunMs(cron string, fromMs int64, taskID string) (int64, bool) {
	t1, ok := NextCronRunMs(cron, fromMs)
	if !ok {
		return 0, false
	}
	// Local minutes, not UTC: cron is evaluated in local time and "user
	// picked a round time" means round in their TZ.
	if time.UnixMilli(t1).Minute()%oneShotMinuteMod != 0 {
		return t1, true
	}
	lead := float64(oneShotFloorMs) + jitterFrac(taskID)*(float64(oneShotMaxMs)-float64(oneShotFloorMs))
	result := t1 - int64(lead)
	if result < fromMs {
		return fromMs, true
	}
	return result, true
}

// FindMissedTasks returns tasks whose next run computed from createdAt is in
// the past. Source: cronTasks.ts findMissedTasks.
func FindMissedTasks(tasks []Task, nowMs int64) []Task {
	var out []Task
	for _, t := range tasks {
		if next, ok := NextCronRunMs(t.Cron, t.CreatedAt); ok && next < nowMs {
			out = append(out, t)
		}
	}
	return out
}

// IsRecurringTaskAged reports whether a recurring task older than maxAgeMs
// should be deleted on its next fire. Permanent tasks never age; 0 = never.
// Source: cronScheduler.ts isRecurringTaskAged.
func IsRecurringTaskAged(t Task, nowMs, maxAgeMs int64) bool {
	if maxAgeMs == 0 {
		return false
	}
	return t.Recurring && !t.Permanent && nowMs-t.CreatedAt >= maxAgeMs
}

// neverFireAt is the sentinel for "no computable next fire" (bad cron string
// on a hand-edited row, or an in-flight one-shot). CC uses Infinity; the map
// stores this instead so comparisons stay in int64 space.
const neverFireAt = math.MaxInt64
