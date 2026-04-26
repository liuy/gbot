// Package engine — microcompact: lightweight pre-request prompt shrinking.
//
// Source: src/services/compact/microCompact.ts (531 lines)
// Source: src/services/compact/timeBasedMCConfig.ts (44 lines)
// Source: src/services/compact/compactWarningState.ts (19 lines)
//
// Time-based microcompact clears old tool result content when the gap since the
// last assistant message exceeds the configured threshold (default 60 minutes),
// matching the server-side prompt cache TTL. This shrinks the prompt before the
// API call, reducing rewrite cost when the cache has expired.
//
// cachedMicrocompact.ts is NOT ported — source is behind feature('CACHED_MICROCOMPACT')
// and does not exist in the repository. All cachedMC exports are no-op stubs.
package engine

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/toolresult"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Constants — source: microCompact.ts:36,38
// ---------------------------------------------------------------------------

const (
	// TimeBasedMCClearedMessage replaces tool_result content when cleared.
	// Source: microCompact.ts:36
	TimeBasedMCClearedMessage = "[Old tool result content cleared]"

	// ImageMaxTokenSize is the approximate token count for images/documents.
	// Source: microCompact.ts:38
	ImageMaxTokenSize = 2000
)

// QuerySourceReplMainThread identifies the main REPL thread as query source.
// Source: TS constants/querySource.ts, engine.go uses "repl_main_thread" for PromptStateKey.
const QuerySourceReplMainThread = "repl_main_thread"

// QuerySourceAgentCustom identifies a custom (non-built-in) sub-agent.
const QuerySourceAgentCustom = "agent:custom"

// QuerySourceCompact identifies the compact system's internal forked agent.
// Used as recursion guard — compact agents must not trigger another compact.
// Source: TS services/compact/compact.ts — querySource: 'compact'
const QuerySourceCompact = "compact"

// QuerySourceSessionMemory identifies the session memory forked agent.
// Used as recursion guard — session memory agents must not trigger compact.
// Source: TS services/SessionMemory/sessionMemory.ts — querySource: 'session_memory'
const QuerySourceSessionMemory = "session_memory"

// compactableTools maps gbot tool names to microcompact eligibility.
// Source: microCompact.ts:41-50 — COMPACTABLE_TOOLS set.
// MAINTENANCE: When adding a new compactable tool, update this map and
// add a test case in TestCompactableTools.
var compactableTools = map[string]bool{
	"Read":   true, // pkg/tool/fileread/fileread.go:361
	"Bash":   true, // pkg/tool/bash/bash.go:93
	"Grep": true, // pkg/tool/grep/grep.go:147)
	"Glob": true, // pkg/tool/glob/glob.go:61)
	"Edit":   true, // pkg/tool/fileedit/fileedit.go:114
	"Write":  true, // pkg/tool/filewrite/filewrite.go:400
	// WebSearch/WebFetch: gbot 未实现，不包含
}

// ---------------------------------------------------------------------------
// Types — source: microCompact.ts:207-220, timeBasedMCConfig.ts:18-28
// ---------------------------------------------------------------------------

// PendingCacheEdits carries cache edit metadata for cached microcompact.
// Source: microCompact.ts:207-213
type PendingCacheEdits struct {
	Trigger                    string   `json:"trigger"` // always "auto"
	DeletedToolIDs             []string `json:"deleted_tool_ids"`
	BaselineCacheDeletedTokens int      `json:"baseline_cache_deleted_tokens"`
}

// MicrocompactResult is the return type for MicrocompactMessages.
// Source: microCompact.ts:215-220
type MicrocompactResult struct {
	Messages       []types.Message
	CompactionInfo *CompactionInfo
}

// CompactionInfo carries optional metadata about what was compacted.
type CompactionInfo struct {
	PendingCacheEdits *PendingCacheEdits
}

// TimeBasedTriggerResult is returned by EvaluateTimeBasedTrigger when the trigger fires.
type TimeBasedTriggerResult struct {
	GapMinutes float64
	Config     TimeBasedMCConfig
}

// ---------------------------------------------------------------------------
// Config — source: timeBasedMCConfig.ts:18-43
// ---------------------------------------------------------------------------

// TimeBasedMCConfig controls time-based microcompact behavior.
// Source: timeBasedMCConfig.ts:18-28
type TimeBasedMCConfig struct {
	Enabled             bool // master switch
	GapThresholdMinutes int  // trigger when gap exceeds this (default 60)
	KeepRecent          int  // keep this many most-recent compactable tool results (default 5)
}

// MicrocompactConfig holds all runtime microcompact settings.
type MicrocompactConfig struct {
	TimeBased TimeBasedMCConfig
}

var defaultMicrocompactConfig = MicrocompactConfig{
	TimeBased: TimeBasedMCConfig{
		Enabled:             true, // gbot 默认开启（MiniMax 支持 cache）
		GapThresholdMinutes: 60,
		KeepRecent:          5,
	},
}

func getMicrocompactConfig() MicrocompactConfig {
	return defaultMicrocompactConfig
}

func getTimeBasedMCConfig() TimeBasedMCConfig {
	return getMicrocompactConfig().TimeBased
}

// ---------------------------------------------------------------------------
// compactWarningState — source: compactWarningState.ts:1-19
// ---------------------------------------------------------------------------

// compactWarningSuppressed tracks whether the "context left until autocompact"
// warning should be suppressed. Source: compactWarningState.ts:8
var compactWarningSuppressed atomic.Bool

// Source: compactWarningState.ts:11
func suppressCompactWarning() { compactWarningSuppressed.Store(true) }

// Source: compactWarningState.ts:16
func clearCompactWarningSuppression() { compactWarningSuppressed.Store(false) }

// ---------------------------------------------------------------------------
// EstimateTokens — source: tokenEstimation.ts
// ---------------------------------------------------------------------------

// EstimateTokens estimates token count from text using character-type-aware heuristic.
// CJK characters (Chinese/Japanese/Korean): ~1.5 tokens/char
// Non-CJK (Latin, digits, symbols, etc.): ~0.25 tokens/char (1 token per 4 chars)
//
// This is a gbot improvement over TS: TS uses plain len/4 which severely
// underestimates CJK content (~0.25 tokens/char instead of ~1.5).
// Based on infinigence/tokenestimate linear regression model and Anthropic's
// guidance that CJK is 2-3x more expensive per character.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	cjk := 0
	nonCJK := 0
	for _, r := range text {
		if isCJK(r) {
			cjk++
		} else {
			nonCJK++
		}
	}
	// CJK: 1.5 tokens/char (3/2)
	// Non-CJK: 0.25 tokens/char (1/4, same as previous len/4 for ASCII)
	return cjk*3/2 + nonCJK/4
}

// isCJK reports whether r is a CJK character (Chinese, Japanese, or Korean).
// Unicode ranges sourced from infinigence/tokenestimate character classification.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0x2A700 && r <= 0x2B73F) || // CJK Extension C
		(r >= 0x2B740 && r <= 0x2B81F) || // CJK Extension D
		(r >= 0x2B820 && r <= 0x2CEAF) || // CJK Extension E
		(r >= 0x2CEB0 && r <= 0x2EBEF) || // CJK Extension F
		(r >= 0x30000 && r <= 0x3134F) || // CJK Extension G
		(r >= 0x3040 && r <= 0x309F) || // Hiragana
		(r >= 0x30A0 && r <= 0x30FF) || // Katakana
		(r >= 0xAC00 && r <= 0xD7AF) || // Hangul Syllables
		(r >= 0x1100 && r <= 0x11FF) || // Hangul Jamo
		(r >= 0x3130 && r <= 0x318F) // Hangul Compatibility Jamo
}

// ---------------------------------------------------------------------------
// calculateToolResultTokens — source: microCompact.ts:138-157
// ---------------------------------------------------------------------------

// calculateToolResultTokens estimates tokens in a tool_result content block.
// Content is json.RawMessage which can be a JSON string ("...") or array ([...]).
func calculateToolResultTokens(content json.RawMessage) int {
	if len(content) == 0 {
		return 0
	}

	// Try to parse as string first (TS: typeof content === 'string')
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		return EstimateTokens(str)
	}

	// Try to parse as array of blocks (TS: Array<TextBlock | ImageBlock | DocumentBlock>)
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(content, &blocks); err == nil {
		total := 0
		for _, block := range blocks {
			blockType := string(block["type"])
			// Remove surrounding quotes from JSON string
			blockType = strings.Trim(blockType, `"`)
			switch blockType {
				case "text":
					var text string
					if err := json.Unmarshal(block["text"], &text); err == nil {
						total += EstimateTokens(text)
					}
				case "image", "document":
					// Images/documents ≈ 2000 tokens regardless of format.
				// Source: microCompact.ts:152
					total += ImageMaxTokenSize
			}
		}
		return total
	}

	// Fallback: estimate from raw bytes
	return EstimateTokens(string(content))
}

// ---------------------------------------------------------------------------
// collectCompactableToolIds — source: microCompact.ts:226-241
// ---------------------------------------------------------------------------

// collectCompactableToolIds walks messages and collects tool_use IDs whose
// tool name is in compactableTools, in encounter order.
func collectCompactableToolIds(messages []types.Message) []string {
	var ids []string
	for i := range messages {
		if messages[i].Role != types.RoleAssistant {
			continue
		}
		for _, block := range messages[i].Content {
			if block.Type == types.ContentTypeToolUse && compactableTools[block.Name] {
				ids = append(ids, block.ID)
			}
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// isMainThreadSource — source: microCompact.ts:249-251
// ---------------------------------------------------------------------------

// isMainThreadSource returns true for the main REPL thread query source.
// Prefix-matches because querySource can be 'repl_main_thread:outputStyle:<style>'.
// Source: microCompact.ts:243-250
func isMainThreadSource(querySource string) bool {
	return querySource == "" || strings.HasPrefix(querySource, QuerySourceReplMainThread)
}

// ---------------------------------------------------------------------------
// EstimateMessagesTokens — source: microCompact.ts:164-205
// ---------------------------------------------------------------------------

// EstimateMessagesTokens estimates token count for messages.
// Pads by 4/3 to be conservative. Source: microCompact.ts:164-205
func EstimateMessagesTokens(messages []types.Message) int {
	totalTokens := 0

	for i := range messages {
		if messages[i].Role != types.RoleUser && messages[i].Role != types.RoleAssistant {
			continue
		}

		for _, block := range messages[i].Content {
			switch block.Type {
			case types.ContentTypeText:
				totalTokens += EstimateTokens(block.Text)

			case types.ContentTypeToolResult:
				totalTokens += calculateToolResultTokens(block.Content)

			case types.ContentTypeThinking:
				totalTokens += EstimateTokens(block.Text)

			case types.ContentTypeRedacted:
				totalTokens += EstimateTokens(block.Data)

			case types.ContentTypeToolUse:
				// Source: microCompact.ts:190-195 — count name + input
				totalTokens += EstimateTokens(block.Name + string(block.Input))

			default:
				// server_tool_use, web_search_tool_result, etc.
				// Source: microCompact.ts:197-199
				raw, _ := json.Marshal(block)
				totalTokens += EstimateTokens(string(raw))
			}
		}
	}

	// Pad estimate by 4/3 to be conservative.
	// Source: microCompact.ts:203-204
	return int(math.Ceil(float64(totalTokens) * 4.0 / 3.0))
}

// ---------------------------------------------------------------------------
// nowFunc — injectable clock for testing
// ---------------------------------------------------------------------------

// nowFunc can be overridden in tests to mock time.
var nowFunc = time.Now

// ---------------------------------------------------------------------------
// EvaluateTimeBasedTrigger — source: microCompact.ts:422-444
// ---------------------------------------------------------------------------

// EvaluateTimeBasedTrigger checks if the time-based trigger should fire.
// Returns gap info when triggered, nil when not.
func EvaluateTimeBasedTrigger(messages []types.Message, querySource string) *TimeBasedTriggerResult {
	config := getTimeBasedMCConfig()
	// Source: microCompact.ts:431 — require explicit querySource for time-based
	if !config.Enabled || querySource == "" || !isMainThreadSource(querySource) {
		return nil
	}

	// Find last assistant message. Source: microCompact.ts:434
	var lastAssistant *types.Message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleAssistant {
			lastAssistant = &messages[i]
			break
		}
	}
	if lastAssistant == nil {
		return nil
	}

	// Source: microCompact.ts:438-439
	gapMinutes := nowFunc().Sub(lastAssistant.Timestamp).Minutes()
	if math.IsInf(gapMinutes, 0) || math.IsNaN(gapMinutes) || gapMinutes < float64(config.GapThresholdMinutes) {
		return nil
	}

	return &TimeBasedTriggerResult{
		GapMinutes: gapMinutes,
		Config:     config,
	}
}

// ---------------------------------------------------------------------------
// maybeTimeBasedMicrocompact — source: microCompact.ts:446-530
// ---------------------------------------------------------------------------

// maybeTimeBasedMicrocompact clears old tool result content when the time-based
// trigger fires. Returns nil when no clearing happens.
func maybeTimeBasedMicrocompact(messages []types.Message, querySource string, logger *slog.Logger) *MicrocompactResult {
	trigger := EvaluateTimeBasedTrigger(messages, querySource)
	if trigger == nil {
		return nil
	}

	compactableIds := collectCompactableToolIds(messages)

	// Floor at 1: slice(-0) returns full array, clearing ALL results leaves
	// zero working context. Source: microCompact.ts:461
	keepRecent := max(trigger.Config.KeepRecent, 1)

	// Build keep/clear sets
	keepCount := min(keepRecent, len(compactableIds))
	keepFrom := len(compactableIds) - keepCount
	keepSet := make(map[string]bool, keepCount)
	for _, id := range compactableIds[keepFrom:] {
		keepSet[id] = true
	}
	// Set-difference: matches TS compactableIds.filter(id => !keepSet.has(id))
	clearSet := make(map[string]bool)
	for _, id := range compactableIds {
		if !keepSet[id] {
			clearSet[id] = true
		}
	}

	if len(clearSet) == 0 {
		return nil
	}

	// Walk messages and clear tool_result content.
	// Source: microCompact.ts:470-492
	tokensSaved := 0
	result := make([]types.Message, len(messages))
	for i := range messages {
		result[i] = messages[i]
		if messages[i].Role != types.RoleUser {
			continue
		}

		touched := false
		newContent := make([]types.ContentBlock, len(messages[i].Content))
		for j, block := range messages[i].Content {
			newContent[j] = block
			if block.Type == types.ContentTypeToolResult &&
				bytes.Contains(block.Content, toolresult.PersistedOutputTagBytes) {
				// Skip already-persisted tool results — they contain compact
				// previews that must not be cleared (TS alignment).
				continue
			}
			if block.Type == types.ContentTypeToolResult &&
				clearSet[block.ToolUseID] &&
				string(block.Content) != `"`+TimeBasedMCClearedMessage+`"` {
				tokensSaved += calculateToolResultTokens(block.Content)
				newContent[j].Content = json.RawMessage(`"` + TimeBasedMCClearedMessage + `"`)
				touched = true
			}
		}

		if touched {
			result[i] = messages[i]
			result[i].Content = newContent
		}
	}

	if tokensSaved == 0 {
		return nil
	}

	// Logging. Source: microCompact.ts:507-509
	if logger != nil {
		logger.Info("engine:time_based_mc",
			"gap_min", int(trigger.GapMinutes),
			"threshold_min", trigger.Config.GapThresholdMinutes,
			"cleared", len(clearSet),
			"kept", len(keepSet),
			"tokens_saved", tokensSaved,
		)
	}

	suppressCompactWarning()

	// Reset cached-MC state. Source: microCompact.ts:517
	ResetMicrocompactState()

	// Notify cache break detection. Source: microCompact.ts:525-527
	llm.NotifyCacheDeletion(llm.PromptStateKey{
		QuerySource: querySource,
	})

	return &MicrocompactResult{Messages: result}
}

// ---------------------------------------------------------------------------
// MicrocompactMessages — source: microCompact.ts:253-293
// ---------------------------------------------------------------------------

// MicrocompactMessages is the main entry point for microcompact.
// Source: microCompact.ts:253
func MicrocompactMessages(messages []types.Message, querySource string, logger *slog.Logger) MicrocompactResult {
	clearCompactWarningSuppression()

	// Time-based first. Source: microCompact.ts:267
	if result := maybeTimeBasedMicrocompact(messages, querySource, logger); result != nil {
		return *result
	}

	// Cached MC: skip. Source: microCompact.ts:276-286
	// TS: if feature('CACHED_MICROCOMPACT') { ... }
	// gbot: cachedMC 模块不存在，等价于 feature flag off

	return MicrocompactResult{Messages: messages}
}

// ---------------------------------------------------------------------------
// cachedMC no-op stubs — source: microCompact.ts:88-135
// NOT IMPLEMENTED: cachedMicrocompact.ts source does not exist (feature gate).
// These align with TS behavior when feature('CACHED_MICROCOMPACT') === false.
// ---------------------------------------------------------------------------

// ConsumePendingCacheEdits returns pending cache edits. No-op: source unavailable.
// Source: microCompact.ts:88-94
func ConsumePendingCacheEdits() *PendingCacheEdits { return nil }

// GetPinnedCacheEdits returns pinned cache edits. No-op: source unavailable.
// Source: microCompact.ts:100-105
func GetPinnedCacheEdits() []any { return nil }

// PinCacheEdits pins cache edits. No-op: source unavailable.
// Source: microCompact.ts:111-118
func PinCacheEdits(int, any) {}

// MarkToolsSentToAPIState marks tools as sent. No-op: source unavailable.
// Source: microCompact.ts:124-128
func MarkToolsSentToAPIState() {}

// ResetMicrocompactState resets cached MC state. No-op: source unavailable.
// Source: microCompact.ts:130-135
func ResetMicrocompactState() {}
