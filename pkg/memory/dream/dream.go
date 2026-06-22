package dream

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/types"
)

// DreamRunFunc executes the dream sub-agent.
// Injected from main.go to avoid circular import.
// Matches session memory's ExtractionFunc pattern.
type DreamRunFunc func(ctx context.Context, prompt string) error

// SessionLister abstracts session querying.
// Implemented by short.Store in main.go.
type SessionLister interface {
	SessionsTouchedSince(projectDir string, since time.Time, excludeSID string) ([]string, error)
}

// DreamEngineMeta is the interface Manager needs from Engine to read live
// sessionID at dream time. sessionID may be empty at construction time
// (session created post-factory) but filled by the time ShouldDream runs.
type DreamEngineMeta interface {
	SessionID() string
}

// Manager manages auto-dream state and gate logic.
// TS source: autoDream.ts — initAutoDream closure.
type Manager struct {
	config     Config
	memoryDir  string
	projectDir string
	engine     DreamEngineMeta // live engine state (sessionID may change)
	store      SessionLister
	runFn      DreamRunFunc
	dispatcher types.EventDispatcher
	logger     *slog.Logger

	mu         sync.Mutex
	running    bool      // concurrent execution guard
	lastScanAt time.Time // scan throttle (TS: lastSessionScanAt)
}

// NewManager creates a new dream Manager.
func NewManager(cfg Config, memoryDir, projectDir string, engine DreamEngineMeta,
	store SessionLister, runFn DreamRunFunc,
	dispatcher types.EventDispatcher, logger *slog.Logger) *Manager {
	return &Manager{
		config:     cfg,
		memoryDir:  memoryDir,
		projectDir: projectDir,
		engine:     engine,
		store:      store,
		runFn:      runFn,
		dispatcher: dispatcher,
		logger:     logger,
	}
}

// scanThrottleInterval matches TS SESSION_SCAN_INTERVAL_MS = 10 * 60 * 1000.
const scanThrottleInterval = 10 * time.Minute

// ShouldDream evaluates 5-layer gate. Returns sessionIDs + priorMtime when all pass.
// Gate order (cheapest first, matching TS autoDream.ts:125-190):
//  1. IsEnabled()
//  2. Time: hours since lastConsolidatedAt >= MinHours (stat lock file mtime)
//  3. Scan throttle: 10min since last scan
//  4. Sessions: count with updated_at > time.UnixMilli(lastConsolidatedAt) >= MinSessions (excludes currentSID)
//  5. Lock: TryAcquireConsolidationLock
func (m *Manager) ShouldDream(ctx context.Context) (shouldRun bool, sessionIDs []string, priorMtime int64, err error) {
	// Gate 1: Enabled check
	if !IsEnabled() {
		return false, nil, 0, nil
	}

	// Gate 2: Time gate — hours since last consolidation
	lastAt, err := ReadLastConsolidatedAt(m.memoryDir)
	if err != nil {
		m.logger.Warn("dream: readLastConsolidatedAt failed", "error", err)
		return false, nil, 0, nil
	}
	hoursSince := time.Since(time.UnixMilli(lastAt)).Hours()
	if hoursSince < float64(m.config.MinHours) {
		return false, nil, 0, nil
	}

	// Gate 3: Scan throttle — 10 min since last scan
	m.mu.Lock()
	sinceScan := time.Since(m.lastScanAt)
	if sinceScan < scanThrottleInterval {
		m.mu.Unlock()
		m.logger.Debug("dream: scan throttle",
			"since_last_scan", sinceScan.Round(time.Second))
		return false, nil, 0, nil
	}
	m.lastScanAt = time.Now()
	m.mu.Unlock()

	// Gate 4: Session gate — enough sessions touched since last consolidation
	sinceTime := time.UnixMilli(lastAt)
	currentSID := m.engine.SessionID()
	ids, err := m.store.SessionsTouchedSince(m.projectDir, sinceTime, currentSID)
	if err != nil {
		m.logger.Warn("dream: SessionsTouchedSince failed", "error", err)
		return false, nil, 0, nil
	}
	if len(ids) < m.config.MinSessions {
		m.logger.Debug("dream: skip — too few sessions",
			"count", len(ids),
			"need", m.config.MinSessions)
		return false, nil, 0, nil
	}

	// Gate 5: Lock acquire
	prior, acquired, err := TryAcquireConsolidationLock(m.memoryDir)
	if err != nil {
		m.logger.Warn("dream: lock acquire failed", "error", err)
		return false, nil, 0, nil
	}
	if !acquired {
		return false, nil, 0, nil
	}

	m.logger.Info("dream: firing",
		"hours_since", fmt.Sprintf("%.1f", hoursSince),
		"sessions", len(ids))

	return true, ids, prior, nil
}

// Execute runs dream consolidation with virtual tool events.
// 1. Emits EventToolStart("Dream") + EventToolRun
// 2. Builds consolidation prompt with session list
// 3. Calls DreamRunFn
// 4. Emits EventToolEnd("Dream") with summary
// 5. On failure: RollbackConsolidationLock
func (m *Manager) Execute(ctx context.Context, sessionIDs []string, priorMtime int64) {
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("dream: panic in Execute", "panic", r)
			RollbackConsolidationLock(m.memoryDir, priorMtime)
		}
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	start := time.Now()
	dreamID := "dream-" + uuid.New().String()[:8]

	m.dispatcher.Dispatch(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:      dreamID,
			Name:    "Dream",
			Summary: fmt.Sprintf("Consolidating memories (%d sessions)", len(sessionIDs)),
		},
	})
	m.dispatcher.Dispatch(types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: dreamID, Name: "Dream"},
	})

	extra := fmt.Sprintf("Sessions since last consolidation (%d):\n%s",
		len(sessionIDs),
		strings.Join(sessionIDs, "\n"))
	prompt := BuildConsolidationPrompt(m.memoryDir, m.projectDir, extra)

	err := m.runFn(ctx, prompt)

	output := "Dream consolidation complete."
	if err != nil {
		output = fmt.Sprintf("Dream consolidation failed: %v", err)
		RollbackConsolidationLock(m.memoryDir, priorMtime)
	} else {
		RecordConsolidation(m.memoryDir)
	}
	m.logger.Info("dream: consolidation finished",
		"duration", time.Since(start).Round(time.Millisecond),
		"error", err)

	m.dispatcher.Dispatch(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     dreamID,
			DisplayOutput: output,
		},
	})
}

// RunPostTurn is the PostTurnHook entry point.
// Only runs for main thread (querySource == "").
// Concurrent execution guard: if running, skip.
func (m *Manager) RunPostTurn(ctx context.Context, messages []types.Message, currentTokens int, querySource string) {
	// Only main thread
	if querySource != "" {
		return
	}
	// Concurrent execution guard
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	shouldRun, sessionIDs, priorMtime, err := m.ShouldDream(ctx)
	if !shouldRun || err != nil {
		return
	}

	m.mu.Lock()
	m.running = true
	m.mu.Unlock()

	// Abort if context already cancelled (e.g., user pressed Escape during gate check)
	if ctx.Err() != nil {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		RollbackConsolidationLock(m.memoryDir, priorMtime)
		return
	}

	go m.Execute(ctx, sessionIDs, priorMtime)
}
