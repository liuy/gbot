// Package task implements file-based task storage with CRUD operations,
// dependency tracking, and high-water-mark ID allocation.
package task

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TaskStatus represents the lifecycle state of a task.
// Source: utils/tasks.ts — TASK_STATUSES
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"
	StatusInProgress TaskStatus = "in_progress"
	StatusCompleted  TaskStatus = "completed"
)

// Task represents a single task in the task list.
// Source: utils/tasks.ts — TaskSchema
type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Status      TaskStatus     `json:"status"`
	Blocks      []string       `json:"blocks"`    // task IDs this task blocks
	BlockedBy   []string       `json:"blockedBy"` // task IDs that block this task
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskUpdates holds partial update fields for UpdateTask.
// Nil pointer fields mean "no change"; &"" for Owner means "clear".
// Source: utils/tasks.ts — Partial<Omit<Task, 'id'>>
type TaskUpdates struct {
	Subject      *string
	Description  *string
	ActiveForm   *string
	Status       *TaskStatus
	Owner        *string        // nil = no change, &"" = clear
	Metadata     map[string]any // merge semantics: nil value = delete key
	AddBlocks    []string       // atomically add block relationships
	AddBlockedBy []string       // atomically add blockedBy relationships
}

// Sentinel errors for the tasks package.
var (
	ErrTaskNotFound = errors.New("task not found")
	ErrInvalidInput = errors.New("invalid input")
)

// High water mark file name — stores the maximum task ID ever assigned.
// Source: utils/tasks.ts — HIGH_WATER_MARK_FILE
const highWaterMarkFile = ".highwatermark"

// List manages file-based task storage for a single session.
// Source: utils/tasks.ts — module-level functions (createTask, getTask, etc.)
type List struct {
	mu           sync.Mutex
	dir          string    // ~/.gbot/tasks/<session-id>/
	allDoneSince time.Time // Set when all tasks first become completed; used for auto-reset.
}

// NewList creates a List for the given pre-resolved directory path.
// Call TasksDir(sessionID) to resolve the path before passing it here.
func NewList(dir string) *List {
	return &List{dir: dir}
}

func (l *List) Dir() string {
	return l.dir
}

// SetDir updates the storage directory and creates it (idempotent).
// Use for deferred initialization when sessionID is not available at construction time.
func (l *List) SetDir(dir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dir = dir
	return os.MkdirAll(dir, 0o755)
}

// Init creates the storage directory (idempotent).
// Source: utils/tasks.ts — ensureTasksDir
func (l *List) Init() error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return fmt.Errorf("create tasks dir: %w", err)
	}
	return nil
}

// TasksDir resolves the storage directory for a session's task list.
// Uses os.UserHomeDir() + ".gbot/tasks/" pattern (matches toolresult/storage.go).
func TasksDir(sessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	sanitized := sanitizePathComponent(sessionID)
	if sanitized == "" {
		sanitized = "default"
	}
	return filepath.Join(home, ".gbot", "tasks", sanitized), nil
}

// CreateTask creates a new task with a unique monotonically-increasing ID.
// Source: utils/tasks.ts — createTask
func (l *List) CreateTask(subject, description, activeForm string, metadata map[string]any) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	highestID, err := l.findHighestTaskID()
	if err != nil {
		return "", fmt.Errorf("find highest task ID: %w", err)
	}
	newID := highestID + 1

	task := Task{
		ID:          strconv.Itoa(newID),
		Subject:     subject,
		Description: description,
		ActiveForm:  activeForm,
		Status:      StatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
		Metadata:    metadata,
	}

	path := l.taskPath(task.ID)
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal task: %w", err)
	}
	if err := atomicWrite(path, data); err != nil {
		return "", fmt.Errorf("write task file: %w", err)
	}

	// Update high water mark to prevent ID reuse even if file is later deleted.
	if err := l.writeHighWaterMark(newID); err != nil {
		return "", fmt.Errorf("write high water mark: %w", err)
	}

	return task.ID, nil
}

// GetTask reads a single task by ID.
// Returns (nil, nil) if the task does not exist.
// Source: utils/tasks.ts — getTask
func (l *List) GetTask(id string) (*Task, error) {
	path := l.taskPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read task %s: %w", id, err)
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("parse task %s: %w", id, err)
	}
	return &task, nil
}

// UpdateTask applies partial updates to a task.
// The entire read-modify-write cycle is protected by List.mu.
// Returns the updated task, or (nil, ErrTaskNotFound) if not found.
// Source: utils/tasks.ts — updateTask
func (l *List) UpdateTask(id string, u TaskUpdates) (*Task, []string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Read current state under lock.
	task, err := l.readTaskLocked(id)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, ErrTaskNotFound
	}

	// Apply updates and track changed fields.
	var updatedFields []string
	if u.Subject != nil && *u.Subject != task.Subject {
		task.Subject = *u.Subject
		updatedFields = append(updatedFields, "subject")
	}
	if u.Description != nil && *u.Description != task.Description {
		task.Description = *u.Description
		updatedFields = append(updatedFields, "description")
	}
	if u.ActiveForm != nil && *u.ActiveForm != task.ActiveForm {
		task.ActiveForm = *u.ActiveForm
		updatedFields = append(updatedFields, "activeForm")
	}
	if u.Status != nil && *u.Status != task.Status {
		task.Status = *u.Status
		updatedFields = append(updatedFields, "status")
	}
	if u.Owner != nil {
		newOwner := *u.Owner
		if newOwner != task.Owner {
			task.Owner = newOwner
			updatedFields = append(updatedFields, "owner")
		}
	}

	// Metadata merge: nil value = delete key, non-nil = set/replace.
	if u.Metadata != nil {
		if task.Metadata == nil {
			task.Metadata = make(map[string]any)
		}
		for k, v := range u.Metadata {
			if v == nil {
				delete(task.Metadata, k)
			} else {
				task.Metadata[k] = v
			}
		}
		updatedFields = append(updatedFields, "metadata")
	}

	// No changes — early return without writing.
	if len(updatedFields) == 0 && len(u.AddBlocks) == 0 && len(u.AddBlockedBy) == 0 {
		return task, nil, nil
	}

	// Write updated task.
	if err := l.writeTaskLocked(task); err != nil {
		return nil, nil, fmt.Errorf("write task %s: %w", id, err)
	}

	// Handle AddBlocks atomically (under same lock).
	for _, blockedID := range u.AddBlocks {
		if ok := l.blockTaskLocked(id, blockedID); ok {
			updatedFields = append(updatedFields, "addBlocks")
		}
	}
	for _, blockerID := range u.AddBlockedBy {
		if ok := l.blockTaskLocked(blockerID, id); ok {
			updatedFields = append(updatedFields, "addBlockedBy")
		}
	}

	// Trigger auto-reset check on any status change.
	// Completing: may set allDoneSince. Uncompleting: clears it.
	if u.Status != nil {
		l.checkAutoReset()
	}

	return task, updatedFields, nil
}

// checkAutoReset tracks when all tasks first become completed.
// Source: hooks/useTasksV2.ts:113-152
func (l *List) checkAutoReset() {
	tasks, err := l.listTasksLocked()
	if err != nil || len(tasks) == 0 {
		l.allDoneSince = time.Time{}
		return
	}
	for _, t := range tasks {
		if t.Status != StatusCompleted {
			l.allDoneSince = time.Time{}
			return
		}
	}
	if l.allDoneSince.IsZero() {
		l.allDoneSince = time.Now()
	}
}

// ShouldCleanupCompleted reports whether all tasks are completed and delay has elapsed.
// On first call, scans disk to initialize allDoneSince (handles session resume).
// Source: hooks/useTasksV2.ts:129-136 — HIDE_DELAY_MS
func (l *List) ShouldCleanupCompleted(delay time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.allDoneSince.IsZero() {
		tasks, err := l.listTasksLocked()
		if err != nil || len(tasks) == 0 {
			return false
		}
		for _, t := range tasks {
			if t.Status != StatusCompleted {
				return false
			}
		}
		// All completed on disk but allDoneSince not set — session resume.
		// Clear immediately instead of starting a new countdown.
		slog.Info("tasks: cleaned up completed tasks from disk state", "count", len(tasks))
		return true
	}
	return time.Since(l.allDoneSince) >= delay
}

// CleanupCompleted deletes all task files after re-validating all are completed.
// Source: utils/tasks.ts:147-188 — resetTaskList
func (l *List) CleanupCompleted() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	tasks, err := l.listTasksLocked()
	if err != nil || len(tasks) == 0 {
		return nil
	}
	for _, t := range tasks {
		if t.Status != StatusCompleted {
			return nil
		}
	}

	highest, _ := l.findHighestTaskID()
	if highest > 0 {
		_ = l.writeHighWaterMark(highest)
	}

	slog.Info("tasks: cleaned up completed tasks", "count", len(tasks), "highestID", highest)

	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			_ = os.Remove(filepath.Join(l.dir, e.Name()))
		}
	}

	l.allDoneSince = time.Time{}
	return nil
}

// DeleteTask deletes a task and cascades cleanup of block references.
// Source: utils/tasks.ts — deleteTask
func (l *List) DeleteTask(id string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Update high water mark BEFORE deleting to prevent ID reuse.
	numericID, err := strconv.Atoi(id)
	if err == nil {
		currentMark, _ := l.readHighWaterMark()
		if numericID > currentMark {
			_ = l.writeHighWaterMark(numericID)
		}
	}

	// Delete the task file.
	path := l.taskPath(id)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete task %s: %w", id, err)
	}

	// Cascade: remove references to this task from all other tasks.
	// Inline file operations — do NOT call UpdateTask (mutex is non-reentrant).
	allTasks, err := l.listTasksLocked()
	if err != nil {
		return true, nil // task deleted, cascade failure is non-critical
	}
	for _, t := range allTasks {
		newBlocks := removeString(t.Blocks, id)
		newBlockedBy := removeString(t.BlockedBy, id)
		if len(newBlocks) != len(t.Blocks) || len(newBlockedBy) != len(t.BlockedBy) {
			t.Blocks = newBlocks
			t.BlockedBy = newBlockedBy
			_ = l.writeTaskLocked(t) // best-effort
		}
	}

	return true, nil
}

// ListTasks returns all tasks sorted by numeric ID ascending.
// Source: utils/tasks.ts — listTasks
func (l *List) ListTasks() ([]*Task, error) {
	return l.listTasksLocked()
}

// BlockTask establishes a bidirectional block relationship between two tasks.
// blockerID blocks blockedID.
// Source: utils/tasks.ts — blockTask
func (l *List) BlockTask(blockerID, blockedID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.blockTaskLocked(blockerID, blockedID)
}

// --- Internal helpers (must be called under lock where appropriate) ---

// blockTaskLocked establishes a block relationship. Caller must hold List.mu.
func (l *List) blockTaskLocked(blockerID, blockedID string) bool {
	// Self-reference guard.
	if blockerID == blockedID {
		return false
	}

	blocker, err := l.readTaskLocked(blockerID)
	if err != nil || blocker == nil {
		return false
	}
	blocked, err := l.readTaskLocked(blockedID)
	if err != nil || blocked == nil {
		return false
	}

	changed := false

	// blocker.Blocks += blockedID (deduplicated)
	if !slices.Contains(blocker.Blocks, blockedID) {
		blocker.Blocks = append(blocker.Blocks, blockedID)
		changed = true
	}
	// blocked.BlockedBy += blockerID (deduplicated)
	if !slices.Contains(blocked.BlockedBy, blockerID) {
		blocked.BlockedBy = append(blocked.BlockedBy, blockerID)
		changed = true
	}

	if changed {
		_ = l.writeTaskLocked(blocker)
		_ = l.writeTaskLocked(blocked)
	}

	return true
}

// readTaskLocked reads a task by ID. Thread-safe (reads single file atomically).
func (l *List) readTaskLocked(id string) (*Task, error) {
	path := l.taskPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (l *List) writeTaskLocked(task *Task) error {
	path := l.taskPath(task.ID)
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, data)
}

// listTasksLocked reads all tasks from disk. Lock-free (single file reads are atomic).
func (l *List) listTasksLocked() ([]*Task, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var tasks []*Task
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		idStr := strings.TrimSuffix(e.Name(), ".json")
		// Only parse numeric IDs (skip .highwatermark etc.)
		if _, err := strconv.Atoi(idStr); err != nil {
			continue
		}
		task, err := l.readTaskLocked(idStr)
		if err != nil || task == nil {
			continue // skip deleted/corrupt files
		}
		tasks = append(tasks, task)
	}

	// Sort by numeric ID ascending.
	sort.Slice(tasks, func(i, j int) bool {
		a, _ := strconv.Atoi(tasks[i].ID)
		b, _ := strconv.Atoi(tasks[j].ID)
		return a < b
	})

	return tasks, nil
}

// findHighestTaskID returns the max ID ever assigned, considering both
// existing files and the high water mark.
// Source: utils/tasks.ts — findHighestTaskId
func (l *List) findHighestTaskID() (int, error) {
	fromFiles, err := l.findHighestFromFiles()
	if err != nil {
		return 0, err
	}
	fromMark, _ := l.readHighWaterMark() // ignore error, defaults to 0
	return maxInt(fromFiles, fromMark), nil
}

// findHighestFromFiles scans task files for the highest numeric ID.
// Source: utils/tasks.ts — findHighestTaskIdFromFiles
func (l *List) findHighestFromFiles() (int, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	highest := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		idStr := strings.TrimSuffix(e.Name(), ".json")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		if id > highest {
			highest = id
		}
	}
	return highest, nil
}

// readHighWaterMark reads the high water mark file. Returns 0 if missing or corrupt.
// Source: utils/tasks.ts — readHighWaterMark
func (l *List) readHighWaterMark() (int, error) {
	path := filepath.Join(l.dir, highWaterMarkFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, nil // missing file = 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, nil // corrupt file = 0
	}
	return value, nil
}

// writeHighWaterMark writes the high water mark file.
// Source: utils/tasks.ts — writeHighWaterMark
func (l *List) writeHighWaterMark(value int) error {
	path := filepath.Join(l.dir, highWaterMarkFile)
	return atomicWrite(path, []byte(strconv.Itoa(value)))
}

func (l *List) taskPath(id string) string {
	return filepath.Join(l.dir, id+".json")
}

// --- Package-level helpers ---

// sanitizePathComponent sanitizes a string for safe use in file paths.
// Source: utils/tasks.ts — sanitizePathComponent
// Only allows [a-zA-Z0-9_-], replaces everything else with '-'.
func sanitizePathComponent(input string) string {
	var b strings.Builder
	for _, r := range input {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Atomic write via temp file + rename. The mutex protects data consistency, not the temp file itself.
func atomicWrite(path string, data []byte) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("generate random suffix: %w", err)
	}
	tmpPath := fmt.Sprintf("%s.tmp.%x", path, suffix)

	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath) // cleanup temp on failure
		return fmt.Errorf("rename temp to target: %w", err)
	}
	return nil
}

func removeString(s []string, v string) []string {
	var result []string
	for _, x := range s {
		if x != v {
			result = append(result, x)
		}
	}
	return result
}
