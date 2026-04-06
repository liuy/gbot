package engine

import (
	"log/slog"

	"github.com/user/gbot/pkg/types"
)

// BudgetTracker tracks token budget consumption.
// Source: query.ts — token budget enforcement at Stages 8 and 13.
//
// The TS source maintains:
//   - Token budget per query (configurable)
//   - Cumulative usage across turns
//   - Budget exhaustion triggers TerminalPromptTooLong
//
// Phase 1 uses simple token counting. Phase 2 adds:
//   - Context compression when approaching limit
//   - Message trimming (oldest first)
//   - Proactive compaction
type BudgetTracker struct {
	budget int
	used   int
	logger *slog.Logger
}

// NewBudgetTracker creates a new budget tracker.
func NewBudgetTracker(budget int, logger *slog.Logger) *BudgetTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &BudgetTracker{
		budget: budget,
		logger: logger,
	}
}

// Consume records token usage from a single API call.
// Source: query.ts — accumulate usage across turns.
func (bt *BudgetTracker) Consume(usage types.Usage) {
	bt.used += usage.InputTokens + usage.OutputTokens
}

// Remaining returns the remaining token budget.
func (bt *BudgetTracker) Remaining() int {
	return bt.budget - bt.used
}

// Exhausted returns true if the budget has been consumed.
// Source: query.ts — Stage 13 blocking limit check.
func (bt *BudgetTracker) Exhausted() bool {
	return bt.budget > 0 && bt.used >= bt.budget
}

// CheckAndWarn logs a warning if budget is low.
// Source: query.ts — token budget exceeded warning.
func (bt *BudgetTracker) CheckAndWarn() bool {
	if bt.Exhausted() {
		bt.logger.Warn("token budget exhausted",
			"budget", bt.budget,
			"used", bt.used,
		)
		return true
	}
	return false
}

// Usage returns the total usage consumed so far.
func (bt *BudgetTracker) Usage() types.Usage {
	return types.Usage{
		InputTokens:  bt.used,
		OutputTokens: 0,
	}
}

// TrimMessages removes oldest messages to free budget space.
// Source: query.ts — Stage 8 applyToolResultBudget.
// Phase 1 simple strategy: drop oldest messages until under budget.
// Phase 2 will use context compression instead.
func TrimMessages(messages []types.Message, maxMessages int) []types.Message {
	if maxMessages <= 0 || len(messages) <= maxMessages {
		return messages
	}
	// Keep the most recent messages, drop oldest.
	// Never drop the first message if it's a system message.
	start := len(messages) - maxMessages
	if start > 0 && messages[0].Role == types.RoleSystem {
		// Preserve system message at the front.
		result := make([]types.Message, 0, maxMessages+1)
		result = append(result, messages[0])
		result = append(result, messages[start:]...)
		return result
	}
	return messages[start:]
}
