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
	"github.com/liuy/gbot/pkg/tool/toolresult"
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

// QuerySourceAutoDream identifies the dream consolidation sub-agent.
// Used as recursion guard — dream agents must not trigger compact.
// Source: TS services/autoDream/autoDream.ts — querySource: 'auto_dream'
const QuerySourceAutoDream = "auto_dream"

// compactableTools maps gbot tool names to microcompact eligibility.
// Source: microCompact.ts:41-50 — COMPACTABLE_TOOLS set.
// MAINTENANCE: When adding a new compactable tool, update this map and
// add a test case in TestCompactableTools.
var compactableTools = map[string]bool{
	"Read":  true, // pkg/tool/fileread/fileread.go:361
	"Bash":  true, // pkg/tool/bash/bash.go:93
	"Grep":  true, // pkg/tool/grep/grep.go:147)
	"Glob":  true, // pkg/tool/glob/glob.go:61)
	"Edit":  true, // pkg/tool/fileedit/fileedit.go:114
	"Write": true, // pkg/tool/filewrite/filewrite.go:400
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

// TokenPruneConfig controls token-based pruning behavior.
// Token-based pruning clears old tool result content when token count
// exceeds the blocking limit and auto-compact cannot help (all messages
// are "recent"). This is the gbot equivalent of TS Cached Microcompact
// (which uses Anthropic cache_editing, not available for MiniMax).
type TokenPruneConfig struct {
	Enabled    bool
	KeepRecent int // keep this many most-recent compactable tool results (default 5)
}

// TokenPruneResult carries the result of token-based tool result pruning.
type TokenPruneResult struct {
	Messages    []types.Message
	TokensSaved int
	Cleared     int // number of tool results cleared
}

// MicrocompactConfig holds all runtime microcompact settings.
type MicrocompactConfig struct {
	TimeBased  TimeBasedMCConfig
	TokenBased TokenPruneConfig
}

var defaultMicrocompactConfig = MicrocompactConfig{
	TimeBased: TimeBasedMCConfig{
		Enabled:             true, // gbot 默认开启（MiniMax 支持 cache）
		GapThresholdMinutes: 60,
		KeepRecent:          5,
	},
	TokenBased: TokenPruneConfig{
		Enabled:    true,
		KeepRecent: 5,
	},
}

func getMicrocompactConfig() MicrocompactConfig {
	return defaultMicrocompactConfig
}

func getTokenPruneConfig() TokenPruneConfig {
	return getMicrocompactConfig().TokenBased
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
// Token estimation uses types.EstimateTokens (see pkg/types/text.go).
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// calculateToolResultTokens — source: microCompact.ts:138-157
// ---------------------------------------------------------------------------

// calculateToolResultTokens estimates tokens in a tool_result content block.
// Content is json.RawMessage which can be a JSON string ("...") or array ([...]).
func calculateToolResultTokens(content json.RawMessage) int {
	return calculateToolResultTokensForProvider(content, "")
}

func calculateToolResultTokensForProvider(content json.RawMessage, provider string) int {
	if len(content) == 0 {
		return 0
	}

	// Try to parse as string first (TS: typeof content === 'string')
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		return types.EstimateTokensForProvider(str, provider)
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
					total += types.EstimateTokensForProvider(text, provider)
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
	return types.EstimateTokensForProvider(string(content), provider)
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
// Per-message envelope overhead varies by provider (calibrated via real APIs).
const defaultMessageEnvelopeTokens = 5

func messageEnvelopeTokens(provider string) int {
	switch provider {
	case "zhipu":
		return 12
	case "deepseek":
		return 4
	case "xiaomi":
		return 4
	default:
		return defaultMessageEnvelopeTokens
	}
}

func EstimateMessagesTokens(messages []types.Message) int {
	return EstimateMessagesTokensForProvider(messages, "")
}

func EstimateMessagesTokensForProvider(messages []types.Message, provider string) int {
	totalTokens := 0
	msgCount := 0

	for i := range messages {
		if messages[i].Role != types.RoleUser && messages[i].Role != types.RoleAssistant {
			continue
		}
		msgCount++

		for _, block := range messages[i].Content {
			switch block.Type {
			case types.ContentTypeText:
				totalTokens += types.EstimateTokensForProvider(block.Text, provider)

			case types.ContentTypeToolResult:
				totalTokens += calculateToolResultTokensForProvider(block.Content, provider)

			case types.ContentTypeThinking:
				totalTokens += types.EstimateTokensForProvider(block.Thinking, provider)

			case types.ContentTypeRedacted:
				totalTokens += types.EstimateTokensForProvider(block.Data, provider)

			case types.ContentTypeToolUse:
				totalTokens += types.EstimateTokensForProvider(block.Name+string(block.Input), provider)

			case types.ContentTypeImage:
				totalTokens += ImageMaxTokenSize

			default:
				raw, _ := json.Marshal(block)
				totalTokens += types.EstimateTokensForProvider(string(raw), provider)
			}
		}
	}

	totalTokens += msgCount * messageEnvelopeTokens(provider)
	return totalTokens
}

// ---------------------------------------------------------------------------
// TokenCountWithEstimation — source: tokens.ts:226-261
// ---------------------------------------------------------------------------

// TokenCountWithEstimation estimates the total context token count by finding
// the last assistant message with real API usage data and using that as the
// precise base, then estimating tokens for messages after it.
// Source: TS tokens.ts:226-261 — tokenCountWithEstimation.
func TokenCountWithEstimation(messages []types.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != types.RoleAssistant || msg.Usage == nil {
			continue
		}
		// Found an assistant message with usage data.
		// Source: TS tokens.ts:50-53 — getTokenCountFromUsage.
		usage := msg.Usage
		base := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens + usage.OutputTokens
		// Skip zero-base Usage — API may return non-nil struct with all fields zero.
		// TS source: tokens.ts:56 — getTokenCountFromUsage returns undefined for 0.
		if base == 0 {
			continue
		}
		delta := EstimateMessagesTokens(messages[i+1:])
		return base + delta
	}
	// No message has usage data — fall back to full estimation.
	// Source: TS tokens.ts:260.
	return EstimateMessagesTokens(messages)
}

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
	gapMinutes := time.Since(lastAssistant.Timestamp).Minutes()
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
				string(block.Content) != `"`+TimeBasedMCClearedMessage+`"` &&
				string(block.Content) != `"`+TokenPrunedMessage+`"` {
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

	// Notify cache break detection. Source: microCompact.ts:525-527
	llm.NotifyCacheDeletion(llm.PromptStateKey{
		QuerySource: querySource,
	})

	return &MicrocompactResult{Messages: result}
}

// ---------------------------------------------------------------------------
// Token-based microcompact — gbot equivalent of TS Cached Microcompact
// ---------------------------------------------------------------------------

// TokenPrunedMessage replaces tool_result content when cleared by token pressure.
// Distinct from TimeBasedMCClearedMessage so logs can distinguish the trigger.
const TokenPrunedMessage = "[Old tool result content cleared]"

// maybeTokenBasedMicrocompact clears old compactable tool result content when
// the token count exceeds the budget. Unlike time-based microcompact (60 min gap),
// this fires purely on token pressure, making it effective for sessions with many
// recent large tool results where auto-compact cannot help.
//
// Returns nil when no clearing happens (under budget, wrong source, nothing to clear).
func maybeTokenBasedMicrocompact(
	messages []types.Message,
	currentTokens int,
	tokenBudget int,
	config TokenPruneConfig,
	querySource string,
	logger *slog.Logger,
) *TokenPruneResult {
	if !config.Enabled || currentTokens <= tokenBudget {
		return nil
	}
	if !isMainThreadSource(querySource) {
		return nil
	}

	compactableIds := collectCompactableToolIds(messages)
	keepRecent := max(config.KeepRecent, 1) // Floor at 1 to avoid clearing everything

	// Build keep/clear sets — same pattern as maybeTimeBasedMicrocompact
	keepCount := min(keepRecent, len(compactableIds))
	keepFrom := len(compactableIds) - keepCount
	keepSet := make(map[string]bool, keepCount)
	for _, id := range compactableIds[keepFrom:] {
		keepSet[id] = true
	}
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
	tokensSaved := 0
	cleared := 0
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
				// Skip persisted results — they contain compact previews.
				continue
			}
			if block.Type == types.ContentTypeToolResult &&
				clearSet[block.ToolUseID] &&
				string(block.Content) != `"`+TimeBasedMCClearedMessage+`"` &&
				string(block.Content) != `"`+TokenPrunedMessage+`"` {
				tokensSaved += calculateToolResultTokens(block.Content)
				newContent[j].Content = json.RawMessage(`"` + TokenPrunedMessage + `"`)
				cleared++
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

	if logger != nil {
		logger.Info("engine:token_based_mc",
			"current_tokens", currentTokens,
			"budget", tokenBudget,
			"cleared", cleared,
			"kept", len(keepSet),
			"tokens_saved", tokensSaved,
		)
	}

	suppressCompactWarning()

	llm.NotifyCacheDeletion(llm.PromptStateKey{
		QuerySource: querySource,
	})

	return &TokenPruneResult{
		Messages:    result,
		TokensSaved: tokensSaved,
		Cleared:     cleared,
	}
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
