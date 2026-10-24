// Package engine — token-based tool result pruning plus token estimation
// helpers.
//
// Source: src/services/compact/microCompact.ts (token prune + estimators)
// Source: src/services/compact/tokens.ts (token estimators)
//
// Token pruning is the gbot equivalent of TS Cached Microcompact (which uses
// Anthropic cache_editing, not available for MiniMax): it clears old tool
// result content only when the token count exceeds the budget and
// auto-compact cannot help (all messages are "recent").
//
// cachedMicrocompact.ts is NOT ported — source is behind feature('CACHED_MICROCOMPACT')
// and does not exist in the repository.
package engine

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// ---------------------------------------------------------------------------
// Constants — source: microCompact.ts:38
// ---------------------------------------------------------------------------

const (
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
	// WebSearch/WebFetch: not implemented in gbot, excluded
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

var defaultTokenPruneConfig = TokenPruneConfig{
	Enabled:    true,
	KeepRecent: 5,
}

func getTokenPruneConfig() TokenPruneConfig {
	return defaultTokenPruneConfig
}

// ---------------------------------------------------------------------------
// Token estimation uses utils.EstimateTokens (see pkg/utils/token.go).
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// calculateToolResultTokens — source: microCompact.ts:138-157
// ---------------------------------------------------------------------------

// calculateToolResultTokens estimates tokens in a tool_result content block.
// Content is json.RawMessage which can be a JSON string ("...") or array ([...]).
func calculateToolResultTokens(content json.RawMessage) int {
	return calculateToolResultTokensWith(content, utils.EstimateTokens)
}

// calculateToolResultTokensWith is the estimator-parameterized core shared by
// the free function and the engine's calibrated path.
func calculateToolResultTokensWith(content json.RawMessage, est func(string) int) int {
	if len(content) == 0 {
		return 0
	}

	// Try to parse as string first (TS: typeof content === 'string')
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		return est(str)
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
					total += est(text)
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
	return est(string(content))
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

// Per-message envelope overhead (role framing around each message's
// content). Measured per-provider variance is <5% of estimate totals, so a
// single constant serves every provider and is not worth calibrating.
const defaultMessageEnvelopeTokens = 5

func EstimateMessagesTokens(messages []types.Message) int {
	return estimateMessagesTokensWith(messages, defaultMessageEnvelopeTokens, utils.EstimateTokens)
}

// estimateMessagesTokensWith is the estimator-parameterized core shared by
// the free function and the engine's calibrated path.
func estimateMessagesTokensWith(messages []types.Message, envelope int, est func(string) int) int {
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
				totalTokens += est(block.Text)

			case types.ContentTypeToolResult:
				totalTokens += calculateToolResultTokensWith(block.Content, est)

			case types.ContentTypeThinking:
				totalTokens += est(block.Thinking)

			case types.ContentTypeRedacted:
				totalTokens += est(block.Data)

			case types.ContentTypeToolUse:
				totalTokens += est(block.Name + string(block.Input))

			case types.ContentTypeImage:
				totalTokens += ImageMaxTokenSize
			case types.ContentTypeDocument:
				// Send-time estimate over the parsed markdown; the
				// Size/4 fallback covers a failed parse (estTokens 0).
				if block.EstTokens > 0 {
					totalTokens += block.EstTokens
				} else {
					totalTokens += int(block.Size) / 4
				}

			default:
				raw, _ := json.Marshal(block)
				totalTokens += est(string(raw))
			}
		}
	}

	totalTokens += msgCount * envelope
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
	return tokenCountWithEstimationCore(messages, defaultMessageEnvelopeTokens, utils.EstimateTokens)
}

// tokenCountWithEstimationCore is the estimator-parameterized core shared by
// the free function and the engine's calibrated path.
func tokenCountWithEstimationCore(messages []types.Message, envelope int, est func(string) int) int {
	if v, ok := lastAnchoredTokenCount(messages, envelope, est); ok {
		return v
	}
	// No message has usage data — fall back to full estimation.
	// Source: TS tokens.ts:260.
	return estimateMessagesTokensWith(messages, envelope, est)
}

// lastAnchoredTokenCount returns the usage-anchored total when the transcript
// carries a usable anchor (last assistant message with non-zero usage), with
// the post-anchor tail estimated; ok=false when the fallback must run.
func lastAnchoredTokenCount(messages []types.Message, envelope int, est func(string) int) (int, bool) {
	for i, msg := range slices.Backward(messages) {

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
		return base + estimateMessagesTokensWith(messages[i+1:], envelope, est), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Calibrated engine wrappers (see token_calibration.go)
// ---------------------------------------------------------------------------

// tokenCountWithEstimation is TokenCountWithEstimation with the calibrated
// estimator improving the delta/fallback estimates. The usage-anchored fast
// path is unchanged: a real base + calibrated delta still wins.
// Must not be called while holding e.mu.
func (e *Engine) tokenCountWithEstimation(messages []types.Message) int {
	est := e.calibratedEstimator()
	if v, ok := lastAnchoredTokenCount(messages, defaultMessageEnvelopeTokens, est); ok {
		return v
	}
	// The anchored path's real base already bills the request-shape overhead
	// (system prompt, tool schemas, skills, memory); the fallback must bill it
	// too or the two paths disagree by exactly that amount. The equality
	// learned at response time (fallback = est + (total − est) = total)
	// persists for later appends because estimateMessagesTokensWith is
	// per-message additive — both sides grow by the same est(tail).
	return estimateMessagesTokensWith(messages, defaultMessageEnvelopeTokens, est) + e.ContextOverheadTokens()
}

// tokenCountWithEstimationLocked is tokenCountWithEstimation for callers
// already holding e.mu in write mode (RewindToScoped): provider/model are
// read directly instead of re-acquiring the lock.
func (e *Engine) tokenCountWithEstimationLocked(messages []types.Message) int {
	R := e.ratiosFor(calibrationKey(e.provider, e.model))
	est := func(s string) int {
		return estimateWithFeatures(utils.CountTokenFeatures(s), R)
	}
	if v, ok := lastAnchoredTokenCount(messages, defaultMessageEnvelopeTokens, est); ok {
		return v
	}
	return estimateMessagesTokensWith(messages, defaultMessageEnvelopeTokens, est) + e.ContextOverheadTokens()
}

// ---------------------------------------------------------------------------
// Token-based pruning — gbot equivalent of TS Cached Microcompact
// ---------------------------------------------------------------------------

// TokenPrunedMessage replaces tool_result content when cleared by token pressure.
const TokenPrunedMessage = "[Old tool result content cleared]"

// maybeTokenBasedMicrocompact clears old compactable tool result content when
// the token count exceeds the budget. It fires purely on token pressure,
// making it effective for sessions with many recent large tool results where
// auto-compact cannot help.
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

	// Build keep/clear sets
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
				string(block.Content) != `"`+TokenPrunedMessage+`"` {
				tokensSaved += calculateToolResultTokens(block.Content)
				newContent[j].Content = json.RawMessage(`"` + TokenPrunedMessage + `"`)
				cleared++
				touched = true
			}
		}

		if touched {
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

	llm.NotifyCacheDeletion(llm.PromptStateKey{
		QuerySource: querySource,
	})

	return &TokenPruneResult{
		Messages:    result,
		TokensSaved: tokensSaved,
		Cleared:     cleared,
	}
}
