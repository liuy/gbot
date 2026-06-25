package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/quota"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// REPL State
// ---------------------------------------------------------------------------

// ReplState holds the interactive REPL session state embedded in App.
//
// All streaming UI state lives here — not on App — so it travels with the
// engine it belongs to. App reads via accessors (StreamingStart, IsThinking,
// ResponseCharCount, TokenRate, etc.) on the active engine's ReplState.
// switchEngine just rebinds a.repl; no per-field restoration needed.
type ReplState struct {
	mu        sync.RWMutex
	messages  []MessageView
	streaming bool
	// streamingStart records when StartQuery ran. switchEngine reads this
	// to restore the progress line when switching back to a live-streaming
	// engine — without it, the newly-active engine shows no elapsed time
	// even though it's mid-query.
	streamingStart time.Time
	// thinkingActive tracks whether the query is currently in the model's
	// reasoning phase (before any visible output). thinkingStart anchors
	// the elapsed counter for the indicator. drain fn flips these on
	// thinkingStart/thinkingEnd events; switchEngine restores them from
	// here when binding a streaming engine.
	thinkingActive   bool
	thinkingStart    time.Time
	thinkingDuration time.Duration
	pendingTool      map[string]*ToolCallView

	// Streaming progress metrics. Maintained by the active path on
	// text/usage events; drain fn maintains them on the same events for
	// background engines so switchEngine needs no restoration.
	responseCharCount     int
	tokenRate             *TokenRate
	outputTokenTarget     int
	inputTokenTarget      int
	displayedInputTokens  int
	displayedOutputTokens int
	cacheReadTokens       int
	cacheCreationTokens   int

	// Tracks partial input accumulation per tool ID for summary updates
	pendingInput map[string]string

	// Tracks when each tool call started streaming (for perceived elapsed)
	pendingToolStart map[string]time.Time

	// Total tool calls in the current query (for progress display)
	toolCount int

	// Cancellation
	cancelFunc context.CancelFunc

	// Tracks the index of the current thinking block in lastMsg().Blocks
	// so deltas can append to it. -1 when no thinking block is active.
	activeThinkingIdx int

	// Commit-on-complete: messages[:committedCount] are committed to terminal
	// scrollback via tea.Println and never re-rendered by Bubble Tea.
	// Only messages[committedCount:] are managed by View().
	committedCount int

	// Per-engine usage accumulator. Tracks token counts for this engine
	// only — switching engines rebinds a.repl, so usage naturally
	// follows. StatusBar reads this via a.status.SetUsage on each update.
	usage types.Usage

	// Per-engine pending queue. User messages queued during streaming
	// belong to this engine only — switching engines should not show
	// another engine's queued messages.
	pendingQueue []pendingQueueItem
}

// NewReplState creates a fresh REPL state.
func NewReplState() *ReplState {
	return &ReplState{
		messages:          []MessageView{},
		pendingTool:       make(map[string]*ToolCallView),
		pendingInput:      make(map[string]string),
		pendingToolStart:  make(map[string]time.Time),
		tokenRate:         NewTokenRate(),
		activeThinkingIdx: -1,
	}
}

// updateToolBlockLocked finds the tool block with the given ID across all
// messages (not just lastMsg) and replaces its ToolCallView. Returns false if
// not found. Caller MUST hold s.mu (Lock or RLock — write is required since
// this mutates blocks in place via pointer writes).
func (s *ReplState) updateToolBlockLocked(id string, tcv *ToolCallView) bool {
	for i := len(s.messages) - 1; i >= 0; i-- {
		if updateToolBlockInBlocks(&s.messages[i].Blocks, id, tcv) {
			return true
		}
	}
	return false
}

// updateToolBlockInBlocks recursively searches and updates a ToolCallView by ID.
func updateToolBlockInBlocks(blocks *[]ContentBlock, id string, tcv *ToolCallView) bool {
	for i := len(*blocks) - 1; i >= 0; i-- {
		blk := &(*blocks)[i]
		if blk.Type == BlockTool {
			if blk.ToolCall.ID == id {
				blk.ToolCall = *tcv
				return true
			}
			if len(blk.ToolCall.Blocks) > 0 {
				if updateToolBlockInBlocks(&blk.ToolCall.Blocks, id, tcv) {
					return true
				}
			}
		}
	}
	return false
}

// findToolViewLocked returns the ToolCallView for the given tool ID.
// Searches pendingTool first, then all messages backwards (most recent first).
// Recursively searches nested Blocks within agent tool calls. Caller MUST hold
// s.mu (either RLock or Lock — this is a read-only operation).
func (s *ReplState) findToolViewLocked(id string) *ToolCallView {
	// 1. Top-level pending tools
	if tcv, ok := s.pendingTool[id]; ok {
		return tcv
	}
	// 2. Search pending tools' nested Blocks (child agent tools)
	for _, tcv := range s.pendingTool {
		if found := findToolViewInBlocks(tcv.Blocks, id); found != nil {
			return found
		}
	}
	// 3. Search messages recursively
	for i := len(s.messages) - 1; i >= 0; i-- {
		if found := findToolViewInBlocks(s.messages[i].Blocks, id); found != nil {
			return found
		}
	}
	return nil
}

// findToolViewInBlocks recursively searches for a ToolCallView by ID within ContentBlocks.
func findToolViewInBlocks(blocks []ContentBlock, id string) *ToolCallView {
	for i := len(blocks) - 1; i >= 0; i-- {
		blk := &blocks[i]
		if blk.Type == BlockTool {
			if blk.ToolCall.ID == id {
				return &blk.ToolCall
			}
			if len(blk.ToolCall.Blocks) > 0 {
				if found := findToolViewInBlocks(blk.ToolCall.Blocks, id); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

// trimBlocksLocked keeps the last 50 non-thinking blocks.
// Thinking blocks do not count toward the limit. Caller MUST hold s.mu.
func (s *ReplState) trimBlocksLocked(tcv *ToolCallView) {
	const maxBlocks = 50
	if len(tcv.Blocks) <= maxBlocks {
		return
	}
	tcv.Blocks = tcv.Blocks[len(tcv.Blocks)-maxBlocks:]
}

// AddUserMessage appends a user message to the session history.
func (s *ReplState) AddUserMessage(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: text}},
	})
}

// StartQuery begins a new streaming query, storing the result channel.
// Creates the assistant message immediately so blocks grow during streaming.
func (s *ReplState) StartQuery() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = true
	s.streamingStart = time.Now()
	s.thinkingActive = false
	s.thinkingStart = time.Time{}
	s.thinkingDuration = 0
	s.responseCharCount = 0
	if s.tokenRate != nil {
		s.tokenRate.Reset()
	}
	s.outputTokenTarget = 0
	s.inputTokenTarget = 0
	s.displayedInputTokens = 0
	s.displayedOutputTokens = 0
	s.cacheReadTokens = 0
	s.cacheCreationTokens = 0
	s.pendingTool = make(map[string]*ToolCallView)
	s.pendingInput = make(map[string]string)
	s.toolCount = 0
	s.messages = append(s.messages, MessageView{Role: "assistant", Blocks: nil})
}

// StreamingStart returns the wall-clock time when StartQuery ran, or zero
// if not streaming. Read by switchEngine to restore the active engine's
// progress line.
func (s *ReplState) StreamingStart() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.streamingStart
}

// StartQueryAtForTest is a test-only variant of StartQuery that injects a
// synthetic streamingStart so tests can verify switchEngine restores the
// elapsed time correctly without sleeping.
func (s *ReplState) StartQueryAtForTest(start time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = true
	s.streamingStart = start
	s.pendingTool = make(map[string]*ToolCallView)
	s.pendingInput = make(map[string]string)
	s.toolCount = 0
	s.messages = append(s.messages, MessageView{Role: "assistant", Blocks: nil})
}

// ResetStreamingUIState clears the per-query streaming UI fields without
// touching messages. Called from resetDisplayState (App-level clear path).
func (s *ReplState) ResetStreamingUIState() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = false
	s.streamingStart = time.Time{}
	s.thinkingActive = false
	s.thinkingStart = time.Time{}
	s.responseCharCount = 0
	if s.tokenRate != nil {
		s.tokenRate.Reset()
	}
	s.outputTokenTarget = 0
	s.inputTokenTarget = 0
	s.displayedInputTokens = 0
	s.displayedOutputTokens = 0
	s.cacheReadTokens = 0
	s.cacheCreationTokens = 0
}

// StartStreamingForTest flips streaming=true and sets streamingStart without
// appending an assistant message. Used by tests that need to drive the
// render path's "is streaming" branch without going through StartQuery.
func (s *ReplState) StartStreamingForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = true
	s.streamingStart = time.Now()
}

// lastMsgLocked returns a pointer to the last message, or nil.
// Caller MUST hold s.mu.
func (s *ReplState) lastMsgLocked() *MessageView {
	if len(s.messages) == 0 {
		return nil
	}
	return &s.messages[len(s.messages)-1]
}

// AppendChunk appends a streaming text delta to the last text block.
func (s *ReplState) AppendChunk(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lastMsgLocked()
	if m == nil {
		return
	}
	if len(m.Blocks) > 0 && m.Blocks[len(m.Blocks)-1].Type == BlockText {
		m.Blocks[len(m.Blocks)-1].Text += text
	} else {
		m.Blocks = append(m.Blocks, ContentBlock{Type: BlockText, Text: text})
	}
}

// AppendTextItem starts a new empty text block.
func (s *ReplState) AppendTextItem() {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lastMsgLocked()
	if m == nil {
		return
	}
	m.Blocks = append(m.Blocks, ContentBlock{Type: BlockText, Text: ""})
}

// formatToolDisplayName converts raw tool names to user-facing display names.
// MCP tools: "mcp__fetch__get_raw_text" → "fetch - get_raw_text (MCP)"
// Built-in tools: returned as-is.
func formatToolDisplayName(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	parts := strings.SplitN(name, "__", 3)
	if len(parts) < 3 {
		return name
	}
	return parts[1] + " - " + parts[2] + " (MCP)"
}

// PendingToolStarted records a new in-progress tool call.
func (s *ReplState) PendingToolStarted(id, name, summary, input string, srk tool.SearchReadKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lastMsgLocked()
	if m == nil {
		return
	}
	tcv := &ToolCallView{ID: id, Name: formatToolDisplayName(name), Summary: summary, Input: input, Done: false, SearchRead: srk}
	s.pendingTool[id] = tcv
	s.toolCount++
	s.pendingToolStart[id] = time.Now()
	m.Blocks = append(m.Blocks, ContentBlock{Type: BlockTool, ToolCall: *tcv})
}

// PendingToolDone updates a tool call with its result.
func (s *ReplState) PendingToolDone(id, output string, isError bool, elapsed time.Duration, srk tool.SearchReadKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tcv, ok := s.pendingTool[id]
	if !ok {
		return
	}
	tcv.Output = output
	tcv.IsError = isError
	tcv.Done = true
	tcv.Elapsed = elapsed
	if srk.IsCollapsible() {
		tcv.SearchRead = srk
	}
	if start, ok := s.pendingToolStart[id]; ok {
		if perceived := time.Since(start); perceived > elapsed {
			tcv.Elapsed = perceived
		}
	}

	// Add sub-agent tool count to global stats
	if tcv.ToolCount > 0 {
		s.toolCount += tcv.ToolCount
	}

	s.updateToolBlockLocked(id, tcv)
}

// PendingToolDelta updates a pending tool's input and summary from engine.
func (s *ReplState) PendingToolDelta(id, delta, summary string, srk tool.SearchReadKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingInput[id] += delta

	tcv, ok := s.pendingTool[id]
	if !ok {
		return
	}

	// Use summary pre-computed by engine (via tool.Description + fallback)
	if summary != "" {
		tcv.Summary = summary
	}
	// Keep raw input length for byte-count display. We don't prettyJSON
	// the partial JSON because streaming partial JSON won't parse, and
	// the running-state header only uses len() for the (1.2KB) indicator.
	tcv.Input = s.pendingInput[id]

	// Update SearchRead from engine recomputation (at content_block_start
	// the input is empty, so isSearch is false; input_json_delta carries
	// the correct classification once partial input arrives).
	if srk.IsCollapsible() {
		tcv.SearchRead = srk
	}

	s.updateToolBlockLocked(id, tcv)
}

// PendingToolOutput updates a streaming tool's output lines in real time.
func (s *ReplState) PendingToolOutput(id, output string, timing time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tcv, ok := s.pendingTool[id]
	if !ok {
		return
	}

	// Track elapsed time (use perceived time for responsiveness)
	if start, ok := s.pendingToolStart[id]; ok {
		if perceived := time.Since(start); perceived > timing {
			tcv.Elapsed = perceived
		}
	}

	// Accumulate output lines (each event carries all current lines)
	tcv.Output = output

	s.updateToolBlockLocked(id, tcv)
}

// PendingThinkingStarted appends a new thinking block to the last message.
func (s *ReplState) PendingThinkingStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeThinkingIdx = -1
	s.thinkingActive = true
	s.thinkingStart = time.Now()
	m := s.lastMsgLocked()
	if m == nil {
		return
	}
	m.Blocks = append(m.Blocks, ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Done: false},
	})
	s.activeThinkingIdx = len(m.Blocks) - 1
}

// PendingThinkingDelta appends text to the active thinking block.
func (s *ReplState) PendingThinkingDelta(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeThinkingIdx < 0 {
		return
	}
	m := s.lastMsgLocked()
	if m == nil {
		return
	}
	if s.activeThinkingIdx >= len(m.Blocks) {
		return
	}
	m.Blocks[s.activeThinkingIdx].Thinking.Text += text
}

// PendingThinkingDone marks the active thinking block as done.
func (s *ReplState) PendingThinkingDone(duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thinkingActive = false
	s.thinkingStart = time.Time{}
	s.thinkingDuration += duration
	if s.activeThinkingIdx < 0 {
		return
	}
	m := s.lastMsgLocked()
	if m == nil {
		return
	}
	if s.activeThinkingIdx >= len(m.Blocks) {
		return
	}
	blk := &m.Blocks[s.activeThinkingIdx].Thinking
	blk.Done = true
	blk.Duration = duration
	s.activeThinkingIdx = -1
}

// IsThinking reports whether the engine is currently in the model's
// reasoning phase. Read by switchEngine to restore a.thinkingActive.
func (s *ReplState) IsThinking() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.thinkingActive
}

// ThinkingStart returns the wall-clock time when the current thinking
// phase began, or zero when not thinking.
func (s *ReplState) ThinkingStart() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.thinkingStart
}

// StartThinkingAtForTest is a test-only setter for thinkingActive +
// thinkingStart so tests can inject a synthetic start without driving
// the full event sequence.
func (s *ReplState) StartThinkingAtForTest(start time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thinkingActive = true
	s.thinkingStart = start
}

// ThinkingDuration returns accumulated thinking time across the query.
func (s *ReplState) ThinkingDuration() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.thinkingDuration
}

// ResponseCharCount returns the count of streamed output characters in
// the current query. Drives the "X chars" progress display.
func (s *ReplState) ResponseCharCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.responseCharCount
}

// AddResponseChars adds n to the response character counter and the
// token rate tracker. Called on text/tool-param deltas.
func (s *ReplState) AddResponseChars(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responseCharCount += len(text)
	if s.tokenRate != nil {
		s.tokenRate.Add(text)
	}
}

// SetResponseCharCount replaces the response character counter. Used by
// tests that need to inject a known value for animation logic.
func (s *ReplState) SetResponseCharCount(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responseCharCount = n
}

// TokenRate returns the underlying *TokenRate for direct method calls
// (Rate, StreamDuration, Reset). Caller must not mutate state outside
// the methods — but AddResponseChars is the preferred write path.
func (s *ReplState) TokenRate() *TokenRate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokenRate
}

// OutputTokenTarget returns the target output tokens used for the
// "X / target" progress indicator.
func (s *ReplState) OutputTokenTarget() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.outputTokenTarget
}

// SetOutputTokenTarget sets the target output tokens.
func (s *ReplState) SetOutputTokenTarget(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outputTokenTarget = n
}

// InputTokenTarget returns the target input tokens.
func (s *ReplState) InputTokenTarget() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inputTokenTarget
}

// SetInputTokenTarget sets the target input tokens.
func (s *ReplState) SetInputTokenTarget(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputTokenTarget = n
}

// DisplayedTokens returns the input/output/cache counters used in the
// status bar context display.
func (s *ReplState) DisplayedTokens() (in, out, cacheRead, cacheCreation int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.displayedInputTokens, s.displayedOutputTokens, s.cacheReadTokens, s.cacheCreationTokens
}

// SetDisplayedTokens sets the input/output/cache counters. Called on
// usageMsg events.
func (s *ReplState) SetDisplayedTokens(in, out, cacheRead, cacheCreation int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.displayedInputTokens = in
	s.displayedOutputTokens = out
	s.cacheReadTokens = cacheRead
	s.cacheCreationTokens = cacheCreation
}

// SetDisplayedInputTokens replaces just the animated input counter.
// Called by the spinner tick to incrementally approach the target.
func (s *ReplState) SetDisplayedInputTokens(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.displayedInputTokens = n
}

// SetDisplayedOutputTokens replaces just the animated output counter.
func (s *ReplState) SetDisplayedOutputTokens(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.displayedOutputTokens = n
}

// SetAgentContextWindow sets the context window for a sub-agent tool call.
// Called once at tool start so the TUI can display "8.7k/30.0k" usage ratios.
func (s *ReplState) SetAgentContextWindow(parentID string, window int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tcv, ok := s.pendingTool[parentID]
	if !ok {
		return
	}
	tcv.ContextWindow = window
	s.updateToolBlockLocked(parentID, tcv)
}

// FinishStream finalizes the streaming session.
// Blocks in s.messages are already built incrementally during streaming.
func (s *ReplState) FinishStream(err error) {
	if pc, _, _, ok := runtime.Caller(1); ok {
		slog.Info("tui:finish_stream", "caller", runtime.FuncForPC(pc).Name(), "err", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = false
	s.streamingStart = time.Time{}
	s.thinkingActive = false
	s.thinkingStart = time.Time{}

	if err != nil {
		s.messages = append(s.messages, MessageView{
			Role:   "system",
			Blocks: []ContentBlock{{Type: BlockText, Text: fmt.Sprintf("Error: %v", err)}},
		})
	}
}

// IsStreaming returns whether a query is in progress.
func (s *ReplState) IsStreaming() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.streaming
}

// Messages returns the session message history.
//
// Returns the underlying slice header under the read lock. Callers in the
// bubbletea goroutine (updateRepl, View) only read it and are safe; callers
// that may mutate the returned slice should copy under lock instead. For
// rendering outside the bubbletea goroutine use MessagesSnapshot.
func (s *ReplState) Messages() []MessageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.messages
}

// MessagesSnapshot returns a copy of the message history. Use this when the
// caller cannot guarantee the bubbletea goroutine's serial access (e.g.
// background-drain goroutine reading a non-active engine's state).
func (s *ReplState) MessagesSnapshot() []MessageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MessageView, len(s.messages))
	copy(out, s.messages)
	return out
}

// FindToolView returns the ToolCallView for the given tool ID, or nil if not
// found. Safe to call from any goroutine. Read-only — uses RLock so it does
// not starve writers.
func (s *ReplState) FindToolView(id string) *ToolCallView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findToolViewLocked(id)
}

// LastMsg returns a pointer to the last message, or nil. Safe to call from
// any goroutine. Returned pointer is into the live slice — caller may read
// but must not mutate without holding the lock.
func (s *ReplState) LastMsg() *MessageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastMsgLocked()
}

// UpdateToolBlock replaces the tool block with the given ID across all
// messages with the supplied ToolCallView. Safe for concurrent use.
func (s *ReplState) UpdateToolBlock(id string, tcv *ToolCallView) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateToolBlockLocked(id, tcv)
}

// TrimBlocks caps the given ToolCallView's Blocks slice to the last 50
// non-thinking blocks. Safe for concurrent use.
func (s *ReplState) TrimBlocks(tcv *ToolCallView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trimBlocksLocked(tcv)
}

// lastMsg, findToolView, updateToolBlock, trimBlocks are backward-compatibility
// aliases that lock and delegate to their *Locked variants. They let existing
// white-box tests (pkg tui) keep using the lowercase names. Production code
// should migrate to the exported LastMsg / FindToolView / UpdateToolBlock /
// TrimBlocks for consistency with the multi-engine locking model.
func (s *ReplState) lastMsg() *MessageView { return s.LastMsg() }
func (s *ReplState) findToolView(id string) *ToolCallView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findToolViewLocked(id)
}
func (s *ReplState) updateToolBlock(id string, tcv *ToolCallView) bool {
	return s.UpdateToolBlock(id, tcv)
}

// AppendStatsLine generates the "↑X ↓Y tokens · rate · tools · elapsed" line
// for a finished query and appends it as a BlockStats entry to the last
// assistant message. Shared between the active path (updateRepl's queryEndMsg
// branch) and the background drain (buildBackgroundDrainFn's queryEndMsg) so
// switching to a previously-background engine shows the same stats the user
// would have seen live.
//
// streamStart must be captured BEFORE FinishStream clears it.
func (s *ReplState) AppendStatsLine(streamStart time.Time, queryUsage types.Usage) {
	if streamStart.IsZero() {
		return
	}
	elapsedStr := formatElapsed(streamStart)
	tokensStr := fmt.Sprintf("↑%s ↓%s tokens",
		types.FormatTokenCount(queryUsage.TotalInputTokens()),
		types.FormatTokenCount(queryUsage.OutputTokens))

	var cachePart string
	if queryUsage.CacheReadInputTokens > 0 || queryUsage.CacheCreationInputTokens > 0 {
		total := queryUsage.CacheReadInputTokens + queryUsage.CacheCreationInputTokens + queryUsage.InputTokens
		if total > 0 {
			if queryUsage.CacheReadInputTokens > 0 {
				pct := queryUsage.CacheReadInputTokens * 100 / total
				cachePart = fmt.Sprintf(" · %d%% cached", pct)
			} else {
				cachePart = fmt.Sprintf(" · %s warmed", types.FormatTokenCount(queryUsage.CacheCreationInputTokens))
			}
		}
	} else {
		cachePart = " · cache missed"
	}

	s.mu.RLock()
	tc := s.toolCount
	s.mu.RUnlock()
	var toolsPart string
	if tc > 0 {
		if tc == 1 {
			toolsPart = " · 1 tool"
		} else {
			toolsPart = fmt.Sprintf(" · %d tools", tc)
		}
	}

	var ratePart string
	if streamDur := s.TokenRate().StreamDuration(); streamDur > 0 && queryUsage.OutputTokens > 0 {
		ratePart = fmt.Sprintf(" · %.1f t/s", float64(queryUsage.OutputTokens)/streamDur.Seconds())
	}

	statsLine := styleDim.Render(tokensStr + ratePart + cachePart + toolsPart + " · " + elapsedStr)
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg := s.lastMsgLocked(); msg != nil {
		msg.Blocks = append(msg.Blocks, ContentBlock{Type: BlockStats, Text: statsLine})
	}
}
func (s *ReplState) trimBlocks(tcv *ToolCallView) { s.TrimBlocks(tcv) }

// ToolCount returns the total tool-call counter for the current query.
func (s *ReplState) ToolCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.toolCount
}

// PendingToolStart returns the perceived start time for a tool ID, or zero.
// Used by sub-agent queryEndMsg handlers to compute elapsed time.
func (s *ReplState) PendingToolStart(id string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.pendingToolStart[id]
	return t, ok
}

// UpdateRunningToolElapsed sets Elapsed on running tool calls in the
// last message to time.Since(start). Called once per second on spinner
// tick so the UI can show a live timer for long-running tools.
func (s *ReplState) UpdateRunningToolElapsed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return
	}
	now := time.Now()
	last := &s.messages[len(s.messages)-1]
	for j := range last.Blocks {
		blk := &last.Blocks[j]
		if blk.Type != BlockTool {
			continue
		}
		// Update top-level running tools via pendingToolStart.
		if !blk.ToolCall.Done {
			if start, ok := s.pendingToolStart[blk.ToolCall.ID]; ok {
				elapsed := now.Sub(start)
				blk.ToolCall.Elapsed = elapsed
				// Sync pendingTool map so sub-agent events (toolStartMsg) that
				// read Elapsed from pendingTool and write it back via
				// updateToolBlock don't clobber the correct value with 0.
				if tcv, ok := s.pendingTool[blk.ToolCall.ID]; ok {
					tcv.Elapsed = elapsed
				}
			}
		}
		// Update nested running blocks (sub-agent tools) via startedAt.
		for k := range blk.ToolCall.Blocks {
			sub := &blk.ToolCall.Blocks[k]
			if sub.Type != BlockTool || sub.ToolCall.Done {
				continue
			}
			if !sub.ToolCall.startedAt.IsZero() {
				sub.ToolCall.Elapsed = now.Sub(sub.ToolCall.startedAt)
			}
		}
	}
}

// CurrentToolName returns the name of the most recently started pending tool,
// or "" when idle. Implements engine.ReplSnapshot for status bar rendering.
// Uses RLock so concurrent render + background drain never deadlock.
func (s *ReplState) CurrentToolName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var name string
	var latest time.Time
	for id, start := range s.pendingToolStart {
		if start.After(latest) {
			if tcv, ok := s.pendingTool[id]; ok && tcv != nil && !tcv.Done {
				name = tcv.Name
				latest = start
			}
		}
	}
	return name
}

// Reset clears all state. Used by session switch / picker / rewind / error
// recovery paths to blank the ReplState before re-populating messages from
// the engine. Acquires the write lock so concurrent readers (background-drain
// goroutines for non-active engines) never observe a half-reset state.
func (s *ReplState) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = []MessageView{}
	s.streaming = false
	s.pendingTool = make(map[string]*ToolCallView)
	s.pendingInput = make(map[string]string)
	s.pendingToolStart = make(map[string]time.Time)
	s.toolCount = 0
	s.activeThinkingIdx = -1
	s.cancelFunc = nil
}

// ---------------------------------------------------------------------------
// REPL Update — handles all REPL-specific messages.
// Called from App.Update in app.go.
// ---------------------------------------------------------------------------

// updateRepl handles REPL-related messages on the App.
// Returns whether the message was handled, and any tea.Cmd to execute.
//
// Concurrency invariant for sub-agent messages (m.Agent != nil): the parent
// tool view is read via findToolView (acquires+releases RLock), mutated
// WITHOUT holding the lock (parent.Blocks = append(...)), then written back
// via updateToolBlock (acquires+releases Lock). This is safe because
// sub-agent events are NEVER routed through the background-drain path —
// buildBackgroundDrainFn gates every mutation on `if m.Agent == nil`, so
// sub-agent state is only touched from this single bubbletea-goroutine
// entry point. Background engines can only mutate their own (non-sub-agent)
// streaming state, which never races with these parent-card updates.
//
// If a future change routes sub-agent events through background drains
// (e.g. background sub-engines), the read-mutate-write here MUST be collapsed
// under a single critical section to avoid losing append-write races.
func (a *App) updateRepl(msg tea.Msg) (bool, tea.Cmd) {
	switch m := msg.(type) {

	case textDeltaMsg:
		a.markViewportDirty()
		// LLM is streaming text — retry (if any) succeeded. Clear retry state
		// because callLLMWithRetry doesn't emit EventQueryStart on retry success,
		// so streamMessageMsg never fires for retried requests.
		a.retryActive = false
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				if len(parent.Blocks) > 0 && parent.Blocks[len(parent.Blocks)-1].Type == BlockText {
					parent.Blocks[len(parent.Blocks)-1].Text += m.Text
				} else {
					parent.Blocks = append(parent.Blocks, ContentBlock{Type: BlockText, Text: m.Text})
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.AppendChunk(m.Text)
		}
		a.repl.AddResponseChars(m.Text)
		return true, a.readEvents()

	case textStartMsg:
		a.retryActive = false
		return true, a.readEvents()

	case textEndMsg:
		// No-op: text content block finished.
		return true, a.readEvents()

	case toolRunMsg:
		// No-op: tool execution started after input accumulation.
		// Sub-agent tool_run is informational; tool_start already added the entry.
		return true, a.readEvents()

	case turnStartMsg:
		// Sub-engine turnStart (from processAttachments → runTurns): do NOT
		// start a new query. Sub-engine text goes to the parent tool view via
		// Agent metadata, and sub-engine's queryEndMsg skips FinishStream().
		// Starting a query here would leave the TUI stuck in streaming=true.
		if m.Agent != nil {
			return true, a.readEvents()
		}
		// Set up streaming state if not already active (e.g. engine auto-processed
		// an attachment while idle — TUI wasn't in streaming mode yet).
		restartStreaming := !a.repl.IsStreaming()
		if restartStreaming {
			a.repl.StartQuery()
			a.status.SetStreaming(true)
			a.spinner.Start()
			a.status.SetUsage(types.Usage{})
		}
		a.markViewportDirty()
		a.repl.AppendTextItem()
		if restartStreaming {
			return true, tea.Batch(
				a.readEvents(),
				tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
					return spinnerTickMsg{}
				}),
			)
		}
		return true, a.readEvents()

	case retryAttemptMsg:
		a.retryActive = true
		a.retryAttempt = m.Attempt
		a.retryMax = m.MaxRetries
		a.retryRemaining = time.Duration(m.RetryInMs) * time.Millisecond
		a.retryStart = time.Now()
		a.retryErrorType = m.ErrorType
		a.markViewportDirty()
		return true, a.readEvents()

	case streamMessageMsg:
		a.retryActive = false
		a.markViewportDirty()
		return true, a.readEvents()

	case toolStartMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			slog.Info("tui:tool_start_subagent", "id", m.ID, "name", m.Name, "parentID", m.Agent.ParentToolUseID, "depth", m.Agent.Depth, "agentType", m.Agent.AgentType, "parentFound", parent != nil)
			if parent != nil {
				srk := tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp}
				parent.Blocks = append(parent.Blocks, ContentBlock{
					Type: BlockTool,
					ToolCall: ToolCallView{
						ID: m.ID, Name: m.Name, Summary: m.Summary,
						Input: m.Input, Done: false, SearchRead: srk,
						startedAt: time.Now(),
					},
				})
				parent.ToolCount++
				if parent.AgentType == "" && m.Agent.AgentType != "" {
					parent.AgentType = m.Agent.AgentType
				}
				a.repl.trimBlocks(parent)
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			srk := tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp}
			a.repl.PendingToolStarted(m.ID, m.Name, m.Summary, m.Input, srk)
			if m.Name == "Agent" {
				a.repl.SetAgentContextWindow(m.ID, a.engine.ContextWindow())
			}
		}
		slog.Info("tui:tool_start", "id", m.ID, "name", m.Name, "summary", m.Summary, "isSearch", m.IsSearch, "isRead", m.IsRead, "isList", m.IsList)
		return true, a.readEvents()

	case toolParamDeltaMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil && m.Summary != "" {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockTool && parent.Blocks[i].ToolCall.ID == m.ID {
						parent.Blocks[i].ToolCall.Summary = m.Summary
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			srk := tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp}
			a.repl.PendingToolDelta(m.ID, m.Delta, m.Summary, srk)
		}
		a.repl.AddResponseChars(m.Delta)
		return true, a.readEvents()

	case toolOutputDeltaMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockTool && parent.Blocks[i].ToolCall.ID == m.ToolUseID {
						parent.Blocks[i].ToolCall.Output = m.DisplayOutput
						if m.Timing > parent.Blocks[i].ToolCall.Elapsed {
							parent.Blocks[i].ToolCall.Elapsed = m.Timing
						}
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.PendingToolOutput(m.ToolUseID, m.DisplayOutput, m.Timing)
		}
		return true, a.readEvents()

	case toolEndMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockTool && parent.Blocks[i].ToolCall.ID == m.ToolUseID {
						parent.Blocks[i].ToolCall.Done = true
						parent.Blocks[i].ToolCall.IsError = m.IsError
						parent.Blocks[i].ToolCall.Output = m.Output
						parent.Blocks[i].ToolCall.Elapsed = m.Timing
						srk := tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp}
						if srk.IsCollapsible() {
							parent.Blocks[i].ToolCall.SearchRead = srk
						}
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.PendingToolDone(m.ToolUseID, m.Output, m.IsError, m.Timing, tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp})
			// Virtual tools (bash shortcut, /compact) drive their own stream
			// lifecycle — no engine queryEndMsg follows — so FinishStream to
			// stop streaming/spinner here, detected by ID prefix.
			if strings.HasPrefix(m.ToolUseID, "bash-shortcut-") || strings.HasPrefix(m.ToolUseID, "compact-manual-") {
				a.repl.FinishStream(nil)
				a.status.SetStreaming(false)
				a.spinner.Stop()
			}
			// /compact: ManualCompact updated engine.ContextTokens to the
			// post-compact precise value (result.AfterTokens). Sync it to
			// the status bar so "used context" reflects the compacted size,
			// not the pre-compact value. Passive compact gets this via
			// queryEndMsg; /compact's virtual tool path bypasses queryEnd.
			if strings.HasPrefix(m.ToolUseID, "compact-manual-") && !m.IsError {
				if ct := a.engine.GetContextTokens(); ct > 0 {
					a.repl.displayedInputTokens = ct
					a.repl.inputTokenTarget = ct
					a.status.SetContext(ct, a.engine.ContextWindow())
				}
			}
		}
		a.taskListDirty = true
		if m.IsError {
			slog.Error("tui:tool_error", "id", m.ToolUseID, "output", m.Output)
		}
		slog.Info("tui:tool_end", "id", m.ToolUseID, "isError", m.IsError, "outputLen", len(m.Output), "agent", m.Agent != nil)
		return true, a.readEvents()

	case queryEndMsg:
		// Sub-agent queryEnd: do NOT finish the main query stream.
		// Only the main engine's queryEnd (Agent == nil) should trigger FinishStream.
		// sub-engine's EventQueryEnd was flowing through without agent metadata,
		// causing FinishStream to cancel the main query's context mid-loop.
		if m.Agent != nil {
			// Sub-agent queryEnd: update parent card elapsed time.
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil && !parent.Done {
				parent.Done = true
				if start, ok := a.repl.pendingToolStart[m.Agent.ParentToolUseID]; ok {
					parent.Elapsed = time.Since(start)
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
			return true, a.readEvents()
		}

		// For abort errors, append inline interrupt message and finish cleanly.
		// The engine already added [Request interrupted by user] to the
		// assistant message content; we mirror it in the TUI display.
		displayErr := m.Err
		if displayErr != nil {
			if _, ok := errors.AsType[*engine.AbortError](displayErr); ok {
				a.repl.AppendChunk(types.InterruptMessage)
				displayErr = nil

				// Auto-rewind: if no meaningful response was produced, restore state
				if m.Agent == nil {
					if a.tryAutoRewind() {
						slog.Info("tui:auto_rewind", "reason", "no_meaningful_response")
					}
				}
			}
		}
		slog.Info("tui:queryEnd", "err", displayErr, "agent", m.Agent != nil)
		// Capture stream start before FinishStream clears it — needed for
		// the stats block below.
		streamStart := a.repl.StreamingStart()
		a.repl.FinishStream(displayErr)

		// Sync status bar with engine's final ContextTokens (post-compact).
		// During streaming the bar showed the API-reported value; compact may
		// have reduced the context after that.
		if ct := a.engine.GetContextTokens(); ct > 0 {
			a.repl.displayedInputTokens = ct
			a.repl.inputTokenTarget = ct
			a.status.SetContext(ct, a.engine.ContextWindow())
		}

		if !streamStart.IsZero() {
			// Use engine's accumulated TotalUsage for stats line (correct
			// across multi-turn queries). Fall back to streaming usage if
			// engine didn't provide accumulated data.
			queryUsage := m.TotalUsage
			if queryUsage.InputTokens == 0 && queryUsage.OutputTokens == 0 {
				queryUsage = a.status.usage
			}
			a.repl.AppendStatsLine(streamStart, queryUsage)
			slog.Info("tui:query_end", "total_in", queryUsage.TotalInputTokens(), "total_out", queryUsage.OutputTokens, "cache_read", queryUsage.CacheReadInputTokens, "cache_creation", queryUsage.CacheCreationInputTokens, "committedCount", a.repl.committedCount, "totalMessages", len(a.repl.messages))
		}

		// Don't commit yet — keep current turn in Bubble Tea view so
		// Ctrl+O (expand/collapse tool output) remains interactive.
		// Commit happens when the user submits the next query.
		a.contentCache = ""
		a.contentDirty = false

		// Keep listening for Hub events while idle (Path B: fork agent
		// notifications). readEvents blocks on appCh.
		return true, a.readEvents()

	case usageMsg:
		// Accumulate per-engine usage on ReplState (not StatusBar).
		a.repl.usage.InputTokens += m.InputTokens
		a.repl.usage.OutputTokens += m.OutputTokens
		a.repl.usage.CacheReadInputTokens += m.CacheReadInputTokens
		a.repl.usage.CacheCreationInputTokens += m.CacheCreationInputTokens
		// Sync to StatusBar for rendering.
		a.status.SetUsage(a.repl.usage)
		// Input tokens arrive all at once — snap immediately
		totalIn := a.status.usage.TotalInputTokens()
		a.repl.displayedInputTokens = totalIn
		a.repl.inputTokenTarget = totalIn
		a.repl.outputTokenTarget = a.status.usage.OutputTokens
		contextSize := m.InputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens + m.OutputTokens
		a.status.SetContext(contextSize, a.engine.ContextWindow())
		slog.Info("tui:usage", "delta_in", m.InputTokens, "delta_out", m.OutputTokens, "context_size", contextSize, "total_in", a.status.usage.TotalInputTokens(), "total_out", a.status.usage.OutputTokens, "cache_read", a.status.usage.CacheReadInputTokens, "cache_creation", a.status.usage.CacheCreationInputTokens)
		// Sub-agent: also accumulate into parent tool call stats
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				totalAgentIn := m.InputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens
				parent.TokensIn += totalAgentIn
				parent.TokensOut += m.OutputTokens
				parent.ContextSize = contextSize
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		}
		return true, a.readEvents()

	case quotaUpdatedMsg:
		// Drop stale fetches triggered before the current provider switch.
		// Async fetches run in the background; if zhipu's HTTP reply arrives
		// after we've switched to minimax, ignoring it preserves the fresh
		// (or empty) state set by refreshQuotaFromProvider.
		if m.seq != a.quotaFetchSeq {
			return true, nil
		}
		// Update status bar with fresh quota. Errors are silent —
		// the previous value (if any) stays until a successful fetch.
		if m.err == nil {
			info := m.info
			a.status.SetQuota(&info)
		}
		return true, nil

	case modelQuotaFetchedMsg:
		if m.err != nil || a.activeDialog == nil || len(a.modelPickerItems) == 0 {
			return true, nil
		}
		quotaDisplay := formatQuota(&m.info)
		updated := false
		for i := range a.modelPickerItems {
			if a.modelPickerItems[i].Provider == m.provider {
				a.modelPickerItems[i].Quota = quotaDisplay
				updated = true
			}
		}
		if !updated {
			return true, nil
		}
		slog.Info("quota: model picker updated", "provider", m.provider, "display", quotaDisplay)
		// Rebuild dialog options from the (now updated) items, preserving cursor.
		items := make([]PickerItem, len(a.modelPickerItems))
		for i := range a.modelPickerItems {
			items[i] = &a.modelPickerItems[i]
		}
		cursor := a.activeDialog.Cursor()
		a.activeDialog = NewDialog("Select model", pickerItemsToOptions(items))
		a.activeDialog.SetCursor(cursor)
		a.activeDialog.width = a.width
		a.markViewportDirty()
		return true, nil

	case thinkingStartMsg:
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				parent.Blocks = append(parent.Blocks, ContentBlock{
					Type: BlockThinking,
				})
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.markViewportDirty()
			a.repl.PendingThinkingStarted()
		}
		return true, a.readEvents()

	case thinkingDeltaMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockThinking && !parent.Blocks[i].Thinking.Done {
						parent.Blocks[i].Thinking.Text += m.Text
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.PendingThinkingDelta(m.Text)
		}
		a.repl.TokenRate().Add(m.Text)
		return true, a.readEvents()

	case thinkingEndMsg:
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockThinking && !parent.Blocks[i].Thinking.Done {
						parent.Blocks[i].Thinking.Done = true
						parent.Blocks[i].Thinking.Duration = m.Duration
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.markViewportDirty()
			a.repl.PendingThinkingDone(m.Duration)
		}
		return true, a.readEvents()

	case permissionAskMsg:
		if m.event != nil {
			detail := extractDetail(m.event.ToolName, m.event.Input)
			ch := m.event.ResponseCh
			a.activeDialog = NewPermissionDialog(m.event, detail)
			a.activeDialog.width = a.width
			a.onDialogDone = func(d *Dialog) (tea.Model, tea.Cmd) {
				dialogDonePermission(d, ch)
				return a, a.readEvents()
			}
		}
		return true, a.readEvents()

	case inputAskMsg:
		if m.event != nil {
			if a.activeInput != nil && !a.activeInput.done {
				sendDecision(a.activeInput.result, types.AskResponse{Aborted: true})
			}
			a.activeInput = NewInputDialog(
				m.event.Prompt,
				m.event.Masked,
				m.event.Deadline,
				m.event.ResponseCh,
			)
			return true, a.activeInput.Init()
		}
		return true, a.readEvents()

	case attachmentMsg:
		slog.Info("tui:attachment_msg", "streaming", a.repl.IsStreaming(), "job_id", m.JobID, "user_text", m.UserText != "", "agent", m.Agent != nil)

		// ItemModePrompt drain: queued user message → insert into conversation
		if m.UserText != "" {
			if m.Agent != nil {
				parent := a.repl.findToolView(m.Agent.ParentToolUseID)
				if parent != nil {
					parent.Blocks = append(parent.Blocks, ContentBlock{Type: BlockText, Text: m.UserText})
					a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
					a.markViewportDirty()
				}
			} else {
				for i, item := range a.repl.pendingQueue {
					if item.ID == m.SourceUUID {
						a.repl.pendingQueue = append(a.repl.pendingQueue[:i], a.repl.pendingQueue[i+1:]...)
						break
					}
				}
				if !a.repl.IsStreaming() {
					// processAttachments path: new user message for next query
					a.repl.messages = append(a.repl.messages, MessageView{
						Role:   "user",
						Blocks: []ContentBlock{{Type: BlockText, Text: m.UserText}},
					})
					a.markViewportDirty()
				} else {
					// Mid-turn drain: append as visual block in current assistant message
					if last := a.repl.lastMsg(); last != nil {
						last.Blocks = append(last.Blocks, ContentBlock{Type: BlockUser, Text: m.UserText})
						a.markViewportDirty()
					}
				}
			}
			return true, a.readEvents()
		}

		// ItemModeJob drain: render notification
		if m.JobID != "" {
			var dotStr string
			if m.Failed {
				dotStr = styleDotError.Render("●")
			} else {
				dotStr = styleDotSuccess.Render("●")
			}
			preview := m.Preview
			if !m.Failed {
				preview = strings.TrimSuffix(preview, " (exit code 0)")
			}
			text := dotStr + " " + preview

			if m.Agent != nil {
				parent := a.repl.findToolView(m.Agent.ParentToolUseID)
				if parent != nil {
					parent.Blocks = append(parent.Blocks, ContentBlock{Type: BlockText, Text: text})
					a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
					a.markViewportDirty()
				}
			} else {
				a.repl.messages = append(a.repl.messages, MessageView{
					Role:   "notification",
					Blocks: []ContentBlock{{Type: BlockText, Text: text}},
				})
				a.markViewportDirty()
			}
		}
		return true, a.readEvents()

	case idleAbortedMsg:
		// No-op: user submitted, new query already started
		return true, nil

	case infoMsg:
		a.status.SetInfo(string(m))
		return true, nil

	case errMsg:
		a.status.SetError(m.Err.Error())
		// Commit uncommitted messages before resetting so error context
		// is preserved in terminal scrollback.
		var errCommitCmd tea.Cmd
		uncommitted := a.repl.messages[a.repl.committedCount:]
		if len(uncommitted) > 0 {
			// Suppress ctrl+o hints in scrollback (noHint=true) — preserve
			// user's expand/collapse state.
			rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", false, true, 0)
			errCommitCmd = tea.Println(rendered)
		}
		a.repl.Reset()
		a.repl.committedCount = 0
		a.spinner.Stop()
		a.input.Focus()
		return true, errCommitCmd

	case userMessageMsg:
		a.markViewportDirty()
		a.repl.AddUserMessage(m.Text)
		return true, a.readEvents()

	case submitMsg:
		return true, a.handleSubmitRepl(m.Text)

	// Periodic spinner tick while streaming
	case spinnerTickMsg:
		if a.repl.IsStreaming() {
			a.markViewportDirty()
			a.toolBlinkTick++
			if a.toolBlinkTick%3 == 0 {
				a.spinner.Tick()
			}
			a.toolBlink = (a.toolBlinkTick/5)%2 == 0
			// Update running tool elapsed once per second (every 10 ticks).
			if a.toolBlinkTick%10 == 0 {
				a.repl.UpdateRunningToolElapsed()
			}
			// Animate displayed tokens toward actual values
			target := max(a.status.usage.TotalInputTokens(), a.repl.inputTokenTarget)
			a.repl.displayedInputTokens = animateTokenValue(a.repl.displayedInputTokens, target)
			outputTarget := max(a.repl.ResponseCharCount()/4, a.repl.outputTokenTarget)
			a.repl.displayedOutputTokens = animateTokenValue(a.repl.displayedOutputTokens, outputTarget)

			// Quota fetch piggybacks on the 100ms spinner tick: every 100
			// ticks (~10s) fire one fetch.
			var fetchCmd tea.Cmd
			if a.toolBlinkTick%100 == 0 {
				fetchCmd = a.fetchQuota()
			}

			return true, tea.Batch(
				tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
					return spinnerTickMsg{}
				}),
				fetchCmd,
			)
		}
		return true, nil

	case bgTickMsg:
		if a.bgEngineStreaming() {
			a.bgBlinkTick++
			a.markViewportDirty()
			return true, tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
				return bgTickMsg{}
			})
		}
		return true, nil
	}
	return false, nil
}

// handleSubmitRepl initiates a streaming query and sets up the REPL state.
func (a *App) handleSubmitRepl(text string) tea.Cmd {
	// Read-only engine guard: the engine is driven by an external connector
	// (WeChat) that calls engine.Query directly; a TUI submit would race
	// with it. Slash commands (starting with '/') are still allowed so the
	// user can /engine switch away.
	if a.inputReadOnly && !strings.HasPrefix(strings.TrimSpace(text), "/") {
		return a.showInfo("This engine is read-only (driven by WeChat). Use /engine to switch.")
	}

	slog.Info("tui:query_start", "text", tool.TruncateRunes(text, 100), "text_len", len(text), "committedCount", a.repl.committedCount, "totalMessages", len(a.repl.messages))

	// Dispatch before the streaming check — switching engines mid-stream
	// is safe: the demoted engine's Hub + drain fn keep its ReplState
	// updated in the background.
	if cmd, ok := a.commands.LookupSlashCommand(text); ok && cmd.Name == "agent" {
		a.history.Add(text)
		a.input.Reset()
		a.pasteStore = make(map[int]string)
		a.nextPasteID = 1
		return a.handleSlashCommand(cmd, nil)
	}

	if a.repl.IsStreaming() {
		return a.handleEnqueueMessage(text)
	}

	// Bash shortcut: !command runs directly via pkg/tool/bash.Execute with
	// streaming output rendered as a virtual tool call. Must be AFTER the
	// streaming check.
	if isBashShortcut(text) {
		// Commit previous turn's messages to scrollback first.
		var commitCmd tea.Cmd
		uncommitted := a.repl.messages[a.repl.committedCount:]
		if len(uncommitted) > 0 {
			rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", false, true, 0)
			a.repl.committedCount = len(a.repl.messages)
			commitCmd = tea.Println(rendered)
		}
		a.history.Add(text)
		a.input.Reset()
		a.pasteStore = make(map[int]string)
		a.nextPasteID = 1
		a.restoreStash()
		a.scrollOffset = 0
		a.scrollTotal = 0
		a.userScrolled = false
		return tea.Batch(commitCmd, a.runBashShortcut(stripBangPrefix(text)),
			tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{}
			}),
		)
	}

	// Commit previous turn's messages to scrollback before starting new turn.
	// This defers the commit so Ctrl+O stays interactive during the
	// completed turn, and scrolls up when user submits next query.
	var commitCmd tea.Cmd
	uncommitted := a.repl.messages[a.repl.committedCount:]
	if len(uncommitted) > 0 {
		// Suppress ctrl+o hints in scrollback (noHint=true) — preserve
		// user's expand/collapse state.
		rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", false, true, 0)
		a.repl.committedCount = len(a.repl.messages)
		commitCmd = tea.Println(rendered)
	}
	// Check for slash commands before adding user message to engine.
	if cmd, ok := a.commands.LookupSlashCommand(text); ok {
		a.history.Add(text)
		a.input.Reset()
		a.pasteStore = make(map[int]string)
		a.nextPasteID = 1
		return a.handleSlashCommand(cmd, commitCmd)
	}

	// Skill slash command: /skill-name args
	// Intercept and dispatch to engine.RunSkill, bypassing the LLM's
	// tool-use decision. Mirrors TS processSlashCommand.
	if skillName, skillArgs, ok := a.commands.LookupSkillCommand(text); ok {
		a.history.Add(text)
		a.input.Reset()
		a.pasteStore = make(map[int]string)
		a.nextPasteID = 1
		a.restoreStash()
		a.scrollOffset = 0
		a.scrollTotal = 0
		a.userScrolled = false
		a.markViewportDirty()
		if a.idleStop != nil {
			close(a.idleStop)
		}
		a.idleStop = make(chan struct{})
		a.repl.cancelFunc = a.engine.Abort

		displayText := "/" + skillName
		if skillArgs != "" {
			displayText += " " + skillArgs
		}
		a.repl.AddUserMessage(displayText)
		a.engine.RunSkill(context.Background(), skillName, skillArgs, a.systemPrompt)
		a.repl.StartQuery()
		a.status.SetStreaming(true)
		a.spinner.Start()
		a.status.SetUsage(types.Usage{})
		a.repl.displayedInputTokens = 0
		a.repl.displayedOutputTokens = 0
		a.repl.inputTokenTarget = types.EstimateTokens(a.systemPrompt) + types.EstimateTokens(displayText)
		return tea.Batch(
			tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{}
			}),
			a.readEvents(),
		)
	}

	a.repl.AddUserMessage(text)
	a.history.Add(text)
	a.input.Reset()
	a.pasteStore = make(map[int]string)
	a.nextPasteID = 1
	a.restoreStash()
	a.scrollOffset = 0
	a.scrollTotal = 0
	a.userScrolled = false
	a.markViewportDirty()

	// Cancel any idle readEvents goroutine to prevent goroutine leak.
	if a.idleStop != nil {
		close(a.idleStop)
	}
	a.idleStop = make(chan struct{})

	// Engine wraps ctx with its own cancel, registered as activeCancel.
	// TUI's cancelFunc calls engine.Abort() which cancels any active operation.
	a.repl.cancelFunc = a.engine.Abort

	// events flow through Hub → TUIHandler → appCh
	a.engine.Query(context.Background(), text, a.systemPrompt)
	a.repl.StartQuery()
	a.status.SetStreaming(true)
	a.spinner.Start()
	a.status.SetUsage(types.Usage{})
	a.repl.displayedInputTokens = 0
	a.repl.displayedOutputTokens = 0
	// Estimate input tokens: use engine's precise ContextTokens (from last
	// API usage) as the base, only estimate the new user message text.
	// Falls back to system prompt estimation on the first turn (cold start).
	base := a.engine.GetContextTokens()
	if base == 0 {
		base = types.EstimateTokens(a.systemPrompt)
	}
	a.repl.inputTokenTarget = base + types.EstimateTokens(text)

	return tea.Batch(
		commitCmd,
		tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
			return spinnerTickMsg{}
		}),
		a.readEvents(),
	)
}

// handleEnqueueMessage queues user input during streaming instead of discarding.
func (a *App) handleEnqueueMessage(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, ok := a.commands.LookupSlashCommand(text); ok {
		return nil
	}
	id := uuid.NewString()
	a.engine.EnqueueAttachment(types.QueuedItem{
		Value:     text,
		Mode:      types.ItemModePrompt,
		UUID:      id,
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Now(),
	})
	a.repl.pendingQueue = append(a.repl.pendingQueue, pendingQueueItem{ID: id, Text: text})
	a.input.Reset()
	a.pasteStore = make(map[int]string)
	a.nextPasteID = 1
	a.restoreStash()
	a.history.Add(text)
	return nil
}

// readEvents reads the next event from TUIHandler.appCh.
// This is called as a tea.Cmd.
func (a *App) readEvents() tea.Cmd {
	return func() tea.Msg {
		if a.tuiHandler == nil {
			return queryEndMsg{}
		}
		select {
		case msg, ok := <-a.tuiHandler.appCh:
			if !ok {
				return queryEndMsg{}
			}
			return msg
		case <-a.idleStop:
			return idleAbortedMsg{}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// prettyJSON formats JSON for display.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "  ", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// renderMessagesFull renders the complete message history without height truncation.
// Terminal native scrollback handles scrolling — matching TS behavior.
func renderMessagesFull(messages []MessageView, width int, expandTools bool, toolDot string, streaming bool, noHint bool, maxOutputLines int) string {
	if len(messages) == 0 {
		welcomeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Italic(true)
		return welcomeStyle.Render("Welcome to gbot. Type a message to get started.")
	}

	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(msg.View(width, expandTools, toolDot, streaming, noHint, maxOutputLines))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// markViewportDirty marks the content cache as needing rebuild.
func (a *App) markViewportDirty() {
	a.contentDirty = true
}

// quotaUpdatedMsg carries a fresh quota snapshot from the async fetcher.
// seq ties the response to the provider context at the time of fetch —
// if the user switches provider mid-flight, the stale response is dropped.
type quotaUpdatedMsg struct {
	info quota.Info
	err  error
	seq  int
}

// fetchQuota returns a tea.Cmd that queries the current provider's quota
// endpoint and produces a quotaUpdatedMsg. Returns nil cmd if no fetcher.
func (a *App) fetchQuota() tea.Cmd {
	if a.quotaFetcher == nil {
		return nil
	}
	seq := a.quotaFetchSeq
	fetcher := a.quotaFetcher
	provider := a.currentProvider
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		info, err := fetcher.Fetch(ctx)
		if err != nil {
			slog.Error("quota: fetch failed", "provider", provider, "error", err)
		} else {
			slog.Info("quota: fetched", "provider", provider,
				"used", info.Used, "remaining", info.Remaining(), "reset_at", info.ResetAt)
		}
		return quotaUpdatedMsg{info: info, err: err, seq: seq}
	}
}
