package dream

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// DreamRunFunc runs the dream sub-agent with the given prompt.
// Injected from start.go to avoid a circular import.
type DreamRunFunc func(ctx context.Context, prompt string) error

// MessageLister provides time-filtered message queries.
// *short.Store implements this.
type MessageLister interface {
	MessagesSince(since time.Time) ([]*short.TranscriptMessage, error)
}

// Manager controls auto-dream scheduling and execution.
type Manager struct {
	config        Config
	memoryDir     string
	contextWindow int
	store         MessageLister
	runFn         DreamRunFunc
	dispatcher    types.EventDispatcher
	logger        *slog.Logger

	mu              sync.Mutex
	running         bool      // concurrent execution guard
	lastAssistantAt time.Time // timestamp of the last assistant message seen
}

// NewManager creates a new dream Manager.
func NewManager(cfg Config, memoryDir string, contextWindow int,
	store MessageLister, runFn DreamRunFunc,
	dispatcher types.EventDispatcher, logger *slog.Logger) *Manager {
	return &Manager{
		config:        cfg,
		memoryDir:     memoryDir,
		contextWindow: contextWindow,
		store:         store,
		runFn:         runFn,
		dispatcher:    dispatcher,
		logger:        logger,
	}
}

// ShouldDream evaluates idle-based gate. Returns priorMtime when all pass.
// Gate order:
//  1. IsEnabled()
//  2. Idle: time since last assistant message >= IdleThreshold
//  3. New messages: at least 1 message since last consolidation
//  4. Cooldown: time since lastConsolidatedAt >= DreamCooldown
//  5. Lock: TryAcquireConsolidationLock
func (m *Manager) ShouldDream(ctx context.Context) (shouldRun bool, priorMtime int64, err error) {
	// Gate 1: Enabled check
	if !IsEnabled() {
		return false, 0, nil
	}

	// Gate 2: Idle check — user must be idle (no recent assistant message)
	m.mu.Lock()
	idle := m.lastAssistantAt
	m.mu.Unlock()
	if idle.IsZero() {
		return false, 0, nil
	}
	idleDuration := time.Since(idle)
	if idleDuration < m.config.IdleThreshold {
		m.logger.Debug("dream: skip — user not idle",
			"idle", idleDuration.Round(time.Second),
			"need", m.config.IdleThreshold)
		return false, 0, nil
	}

	// Gate 3: New messages — at least 1 message since last consolidation
	lastAt, err := ReadLastConsolidatedAt(m.memoryDir)
	if err != nil {
		m.logger.Warn("dream: readLastConsolidatedAt failed", "error", err)
		return false, 0, nil
	}
	sinceTime := time.UnixMilli(lastAt)
	msgs, err := m.store.MessagesSince(sinceTime)
	if err != nil {
		m.logger.Warn("dream: MessagesSince failed", "error", err)
		return false, 0, nil
	}
	if len(msgs) == 0 {
		m.logger.Debug("dream: skip — no new messages since last consolidation")
		return false, 0, nil
	}

	// Gate 4: Cooldown — must be >= DreamCooldown since last consolidation
	cooldownRemaining := m.config.DreamCooldown - time.Since(sinceTime)
	if cooldownRemaining > 0 {
		m.logger.Debug("dream: skip — cooldown",
			"remaining", cooldownRemaining.Round(time.Second))
		return false, 0, nil
	}

	// Gate 5: Lock acquire
	prior, acquired, err := TryAcquireConsolidationLock(m.memoryDir)
	if err != nil {
		m.logger.Warn("dream: lock acquire failed", "error", err)
		return false, 0, nil
	}
	if !acquired {
		return false, 0, nil
	}

	m.logger.Info("dream: firing",
		"idle", idleDuration.Round(time.Second),
		"messages", len(msgs))

	return true, prior, nil
}

// Execute runs dream consolidation with virtual tool events.
// 1. Emits EventToolStart("Dream") + EventToolRun
// 2. Queries incremental messages since last consolidation
// 3. Chunks by token budget and runs each chunk through DreamRunFn
// 4. Emits EventToolEnd("Dream") with summary
// 5. On failure: RollbackConsolidationLock
func (m *Manager) Execute(ctx context.Context, priorMtime int64) {
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

	// priorMtime is the lock mtime from BEFORE TryAcquireConsolidationLock
	// updated it in ShouldDream. Using it (not re-reading the lock) avoids
	// treating the just-stamped mtime as the "since" cutoff.
	since := time.UnixMilli(priorMtime)
	messages, err := m.store.MessagesSince(since)

	var chunks [][]*short.TranscriptMessage
	if err == nil {
		chunks = chunkByTokens(messages, m.contextWindow)
	} else {
		m.logger.Warn("dream: MessagesSince failed", "error", err)
	}

	m.dispatcher.Dispatch(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:      dreamID,
			Name:    "Dream",
			Summary: fmt.Sprintf("Consolidating memories (%d chunks)", len(chunks)),
		},
	})
	m.dispatcher.Dispatch(types.QueryEvent{
		Type:    types.EventToolRun,
		ToolUse: &types.ToolUseEvent{ID: dreamID, Name: "Dream"},
	})

	successCount := 0
	for i, chunk := range chunks {
		extra := formatMessages(chunk, i+1, len(chunks))
		prompt := BuildConsolidationPrompt(m.memoryDir, extra)
		if chunkErr := m.runFn(ctx, prompt); chunkErr != nil {
			m.logger.Warn("dream: chunk failed", "chunk", i+1, "error", chunkErr)
		} else {
			successCount++
		}
	}

	output := "Dream consolidation complete."
	if len(chunks) > 0 && successCount == 0 {
		output = fmt.Sprintf("Dream consolidation failed: all %d chunks failed", len(chunks))
		RollbackConsolidationLock(m.memoryDir, priorMtime)
	} else if len(chunks) > 0 && successCount < len(chunks) {
		output = fmt.Sprintf("Dream consolidation partial: %d/%d chunks succeeded", successCount, len(chunks))
		// Rollback so the next run reprocesses all chunks — recall + UNIQUE
		// content dedup prevents duplicates even on reprocessing.
		RollbackConsolidationLock(m.memoryDir, priorMtime)
	} else if len(chunks) > 0 {
		RecordConsolidation(m.memoryDir)
	} else {
		// No messages or query error — nothing to consolidate
		output = "Dream consolidation skipped: no new messages"
		RollbackConsolidationLock(m.memoryDir, priorMtime)
	}

	m.logger.Info("dream: consolidation finished",
		"duration", time.Since(start).Round(time.Millisecond),
		"chunks", len(chunks),
		"success", successCount)

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

	// Check idle gate BEFORE updating lastAssistantAt — otherwise the
	// freshly-generated assistant response (timestamp ≈ now) resets idle
	// to 0 and the gate never passes.
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	shouldRun, priorMtime, err := m.ShouldDream(ctx)

	// Now update lastAssistantAt from the most recent assistant message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleAssistant {
			ts := messages[i].Timestamp
			if ts.IsZero() {
				ts = time.Now()
			}
			m.mu.Lock()
			if ts.After(m.lastAssistantAt) {
				m.lastAssistantAt = ts
			}
			m.mu.Unlock()
			break
		}
	}

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

	go m.Execute(ctx, priorMtime)
}

// chunkByTokens splits messages into chunks that fit within half the context
// window (reserving the other half for system prompt + notes + output).
// Each message is kept whole — never split across chunk boundaries.
func chunkByTokens(messages []*short.TranscriptMessage, contextWindow int) [][]*short.TranscriptMessage {
	if len(messages) == 0 {
		return nil
	}
	// Reserve half the window for the prompt template, system prompt, notes
	// reads, and dream agent output. The floor of 2000 avoids degenerate
	// single-message chunks on tiny context windows.
	budget := max(contextWindow/2, 2000)

	var chunks [][]*short.TranscriptMessage
	var current []*short.TranscriptMessage
	currentTokens := 0

	for _, msg := range messages {
		msgTokens := estimateTokens(msg)
		if currentTokens+msgTokens > budget && len(current) > 0 {
			chunks = append(chunks, current)
			current = nil
			currentTokens = 0
		}
		current = append(current, msg)
		currentTokens += msgTokens
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// estimateTokens gives a rough token count for a message (len/4 heuristic).
func estimateTokens(msg *short.TranscriptMessage) int {
	text := short.ExtractTextFromJSON(msg.Content)
	if text == "" {
		return 10
	}
	return len(text) / 4
}

// formatMessages renders a chunk of messages as readable text for the dream prompt.
func formatMessages(messages []*short.TranscriptMessage, chunkNum, totalChunks int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Recent conversations since last dream (chunk %d/%d):\n\n", chunkNum, totalChunks)
	for _, msg := range messages {
		role := "user"
		if msg.Type == "assistant" {
			role = "assistant"
		}
		ts := msg.CreatedAt.Format("2006-01-02 15:04")
		text := short.ExtractTextFromJSON(msg.Content)
		fmt.Fprintf(&b, "[%s %s] %s\n\n", role, ts, text)
	}
	return b.String()
}
