package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/types"
)

const (
	// SessionMemoryFileName is the filename for session memory notes.
	SessionMemoryFileName = "SESSION_NOTES.md"
)

// ExtractionFunc is the callback that performs the actual extraction via a forked subagent.
// The engine provides this to avoid circular package dependencies.
// ctx: context for cancellation.
// prompt: the extraction prompt to send to the subagent.
// notesPath: absolute path to the session memory file.
// messages: the full parent conversation history (for fork-style context).
// systemPrompt: the parent engine's rendered system prompt (for cache sharing).
type ExtractionFunc func(ctx context.Context, prompt string, notesPath string, messages []types.Message, systemPrompt string) error

// SessionMemory manages background extraction of session notes.
// TS source: services/SessionMemory/sessionMemory.ts.
type SessionMemory struct {
	config     Config
	workingDir string
	engineID   string
	logger     *slog.Logger
	mu         sync.Mutex

	// State tracking — TS source: sessionMemoryUtils.ts module-level vars.
	initialized          bool
	lastTokenCount       int
	extracting           bool
	extractionStart      time.Time
	toolCallsSinceUpdate int

	// extractFn is provided by the engine layer to run subagent extraction.
	extractFn ExtractionFunc

	// systemPromptFn returns the parent engine's rendered system prompt for cache sharing.
	// Set via SetSystemPromptFn after construction.
	systemPromptFn func() string

	// extractDone is closed when extraction completes. Nil when not extracting.
	extractDone chan struct{}
}

// New creates a new SessionMemory. engineID keys the per-engine notes file
// ({project}/.gbot/session_notes/{engineID}.md); empty defaults to "main"
// (matches engine convention at pkg/engine/engine.go:319-321).
func New(config Config, workingDir string, engineID string, extractFn ExtractionFunc, logger *slog.Logger) *SessionMemory {
	return &SessionMemory{
		config:     config,
		workingDir: workingDir,
		engineID:   engineID,
		extractFn:  extractFn,
		logger:     logger,
	}
}

// ShouldExtract checks whether session memory extraction should run.
// TS source: sessionMemory.ts:134-181 — shouldExtractMemory.
//
// Dual-threshold gate:
//  1. Init gate: if not initialized, check currentTokens >= MinTokensToInit
//  2. Update gate: token threshold ALWAYS required, plus either tool call threshold
//     met OR natural conversation break (last turn has no tool calls)
func (sm *SessionMemory) ShouldExtract(currentTokens int, messages []types.Message) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Init gate — TS: hasMetInitializationThreshold
	if !sm.initialized {
		if currentTokens < sm.config.MinTokensToInit {
			return false
		}
		sm.initialized = true
	}

	// Token threshold ALWAYS required — TS: hasMetUpdateThreshold
	tokenGrowth := currentTokens - sm.lastTokenCount
	hasMetTokenThreshold := tokenGrowth >= sm.config.MinTokensBetweenUpdate
	if !hasMetTokenThreshold {
		return false
	}

	// Tool call threshold — TS: countToolCallsSince
	hasMetToolCallThreshold := sm.toolCallsSinceUpdate >= sm.config.ToolCallsBetweenUpdates

	// Natural conversation break — TS: !hasToolCallsInLastTurn
	hasToolCallsInLastTurn := lastAssistantHasToolCalls(messages)

	// TS line 168-170: shouldExtract = hasMetTokenThreshold && (hasMetToolCallThreshold || !hasToolCallsInLastTurn)
	shouldExtract := hasMetToolCallThreshold || !hasToolCallsInLastTurn
	return shouldExtract
}

// Extract runs the session memory extraction.
// TS source: sessionMemory.ts:272-350 — extractSessionMemory.
//
// 1. Checks for stale extraction and recovers
// 2. Ensures session memory file exists (creates from template)
// 3. Reads current content
// 4. Builds update prompt
// 5. Calls extractFn (forked subagent)
// 6. Updates state
func (sm *SessionMemory) Extract(ctx context.Context, messages []types.Message, currentTokens int) error {
	sm.mu.Lock()

	// Stale extraction recovery — TS: waitForSessionMemoryExtraction stale check
	if sm.extracting {
		if time.Since(sm.extractionStart) > time.Duration(sm.config.ExtractionStaleMs)*time.Millisecond {
			sm.logger.Warn("sessionmemory: stale extraction detected, resetting",
				"stale_duration", time.Since(sm.extractionStart))
			sm.extracting = false
		} else {
			sm.mu.Unlock()
			return nil // extraction already in progress
		}
	}

	sm.extracting = true
	sm.extractionStart = time.Now()
	sm.extractDone = make(chan struct{})
	sm.mu.Unlock()

	// Ensure cleanup on any exit — close channel to wake WaitForExtraction
	defer func() {
		sm.mu.Lock()
		sm.extracting = false
		if sm.extractDone != nil {
			close(sm.extractDone)
			sm.extractDone = nil
		}
		sm.mu.Unlock()
	}()

	// Ensure session memory file exists with template
	notesPath := sm.NotesPath()
	if err := sm.ensureFile(); err != nil {
		return fmt.Errorf("setup session memory file: %w", err)
	}

	// Read current content
	data, err := os.ReadFile(notesPath)
	if err != nil {
		return fmt.Errorf("read session memory: %w", err)
	}
	currentNotes := string(data)

	// Build update prompt — TS: buildSessionMemoryUpdatePrompt
	prompt := BuildUpdatePrompt(currentNotes, notesPath, sm.config)

	// TS: runForkedAgent — no timeout; extraction runs to completion.
	// Timeout is only for WaitForExtraction (SM-compact fallback decision).
	var sysPrompt string
	if sm.systemPromptFn != nil {
		sysPrompt = sm.systemPromptFn()
	}
	if err := sm.extractFn(ctx, prompt, notesPath, messages, sysPrompt); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Update state after successful extraction
	sm.mu.Lock()
	sm.lastTokenCount = currentTokens
	sm.toolCallsSinceUpdate = 0
	sm.mu.Unlock()

	sm.logger.Info("sessionmemory: extraction completed",
		"tokens", currentTokens)

	return nil
}

// IsEmpty checks if the session memory file exists and has real content.
// TS source: prompts.ts:220 — isSessionMemoryEmpty.
func (sm *SessionMemory) IsEmpty() bool {
	notesPath := sm.NotesPath()
	data, err := os.ReadFile(notesPath)
	if err != nil {
		return true
	}
	return IsSessionMemoryEmpty(string(data))
}

// WaitForExtraction waits for any in-progress extraction to complete.
// TS source: sessionMemoryUtils.ts:89-105 — waitForSessionMemoryExtraction.
//
// Uses channel-based signaling. Returns immediately if:
// - No extraction is in progress, OR
// - Extraction is stale (> ExtractionStaleMs), OR
// - Total wait exceeds ExtractionTimeoutMs.
func (sm *SessionMemory) WaitForExtraction() error {
	timeout := time.Duration(sm.config.ExtractionTimeoutMs) * time.Millisecond
	staleThreshold := time.Duration(sm.config.ExtractionStaleMs) * time.Millisecond

	sm.mu.Lock()
	if !sm.extracting {
		sm.mu.Unlock()
		return nil
	}
	// Stale recovery
	if time.Since(sm.extractionStart) > staleThreshold {
		sm.extracting = false
		if sm.extractDone != nil {
			close(sm.extractDone)
			sm.extractDone = nil
		}
		sm.mu.Unlock()
		return nil
	}
	done := sm.extractDone
	sm.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("sessionmemory: extraction wait timed out after %v", timeout)
	}
}

// NotesPath returns the absolute path to the session memory file.
// Per-engine layout: ~/.gbot/memory/session_notes/{engineID}.md.
func (sm *SessionMemory) NotesPath() string {
	id := sm.engineID
	if id == "" {
		id = "main"
	}
	configDir, err := config.ConfigDir()
	if err != nil {
		return filepath.Join(sm.workingDir, ".gbot", "memory", "session_notes", id+".md")
	}
	return filepath.Join(configDir, "memory", "session_notes", id+".md")
}

// RecordToolCalls increments the tool call counter.
// Called by the post-turn hook after each turn with tool results.
func (sm *SessionMemory) RecordToolCalls(count int) {
	sm.mu.Lock()
	sm.toolCallsSinceUpdate += count
	sm.mu.Unlock()
}

// Reset resets all session memory state (for testing).
// TS source: sessionMemoryUtils.ts:201 — resetSessionMemoryState.
func (sm *SessionMemory) Reset() {
	sm.mu.Lock()
	sm.initialized = false
	sm.lastTokenCount = 0
	sm.extracting = false
	sm.extractionStart = time.Time{}
	sm.toolCallsSinceUpdate = 0
	sm.mu.Unlock()
}

// SetSystemPromptFn sets the function that returns the parent engine's rendered
// system prompt. Used for prompt cache sharing during extraction.
// TS: runForkedAgent uses cacheSafeParams.systemPrompt from parent context.
func (sm *SessionMemory) SetSystemPromptFn(fn func() string) {
	sm.systemPromptFn = fn
}

// ensureFile creates the session memory file from the template if it doesn't exist.
// TS source: sessionMemory.ts:183-238 — setupSessionMemoryFile.
func (sm *SessionMemory) ensureFile() error {
	notesPath := sm.NotesPath()

	if _, err := os.Stat(notesPath); err == nil {
		return nil // file exists
	}

	// Ensure directory exists
	dir := filepath.Dir(notesPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(notesPath, []byte(DefaultTemplate), 0644)
}

// lastAssistantHasToolCalls checks if the last assistant message contains tool_use blocks.
// TS source: sessionMemory.ts:168 — hasToolCallsInLastTurn.
func lastAssistantHasToolCalls(messages []types.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleAssistant {
			for _, block := range messages[i].Content {
				if block.Type == types.ContentTypeToolUse {
					return true
				}
			}
			return false
		}
	}
	return false
}

// LoadSessionMemoryContent reads the session memory file content.
// Returns empty string if file doesn't exist.
// Used by SM-compact to get content for compaction.
func LoadSessionMemoryContent(workingDir, engineID string) string {
	if engineID == "" {
		engineID = "main"
	}
	notesPath := filepath.Join(workingDir, ".gbot", "session_notes", engineID+".md")
	data, err := os.ReadFile(notesPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
