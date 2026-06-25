// Package engine implements the core agentic loop for gbot.
package engine

// autocompact.go implements engine.Compactor via short.Store.
// TS align: compact.ts + autoCompact.ts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// FormatCompactOutput builds the display text for a successful compact result.
// Structure: stats line first (fills collapse preview), then summary content.
func FormatCompactOutput(result *short.CompactResult) string {
	output := fmt.Sprintf("Conversation compacted (msg: %d → %d, token: %s → %s)",
		result.BeforeMessages, len(result.Messages),
		types.FormatTokenCount(result.BeforeTokens), types.FormatTokenCount(result.AfterTokens))
	if result.Summary != "" {
		output += "\n" + result.Summary
	}
	return output
}

// EngineCompactorMeta is the interface AutoCompactor needs from Engine to
// read live state at compact time. Decouples the two types to avoid a
// circular dependency (Engine creates AutoCompactor, AutoCompactor reads Engine).
type EngineCompactorMeta interface {
	Model() string
	Provider() llm.Provider
	SessionID() string
	ContextWindow() int
}

// Compactor implements Compactor by delegating to the short.Store's compact
// functions and using the LLM provider to generate a summary.
// TS align: compact.ts:compactConversation + partialCompactConversation
type AutoCompactor struct {
	store  *short.Store
	engine EngineCompactorMeta // live engine state (model/provider/sessionID may change)
	logger *slog.Logger
}

// NewAutoCompactor creates a Compactor for compacting the given session.
func NewAutoCompactor(store *short.Store, engine EngineCompactorMeta) *AutoCompactor {
	return &AutoCompactor{
		store:  store,
		engine: engine,
		logger: slog.Default(),
	}
}

// Compact compacts the conversation history by:
//  1. Keeping the most recent messages (enough for recent context)
//  2. Summarizing the older messages via LLM
//  3. Returning CompactResult with summary, token stats, and post-compact messages.
//
// Custom instructions are NOT supported here; use CompactWithInstructions for
// the manual /compact entry point. Keeping the Compactor interface (which uses
// this method) unchanged avoids touching every mock implementation.
func (c *AutoCompactor) Compact(ctx context.Context, messages []types.Message) (*short.CompactResult, error) {
	return c.compact(ctx, messages, "")
}

// CompactWithInstructions is the manual /compact entry point. Identical to
// Compact but threads custom summarization instructions into the LLM prompt.
// TS align: compact.ts:compactConversation(messages, ..., customInstructions, false)
func (c *AutoCompactor) CompactWithInstructions(ctx context.Context, messages []types.Message, customInstructions string) (*short.CompactResult, error) {
	return c.compact(ctx, messages, customInstructions)
}

// compact is the shared body of Compact and CompactWithInstructions.
func (c *AutoCompactor) compact(ctx context.Context, messages []types.Message, customInstructions string) (*short.CompactResult, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("nothing to compact: no messages")
	}
	beforeTokens := TokenCountWithEstimation(messages)

	// Convert engine types → short types for store operations
	shortMsgs, err := short.EngineMessagesToStore(messages)
	if err != nil {
		return nil, fmt.Errorf("convert messages: %w", err)
	}

	// Determine how many recent messages to keep.
	// Walk backwards from tail, keep adding until token budget exceeded.
	// keepFrom = len: compact everything (tail=0).
	// keepFrom < len: compact head, keep tail.
	keepFrom := c.findKeepFrom(shortMsgs)

	// Generate summary for the head messages via LLM
	headMsgs := shortMsgs[:keepFrom]
	summaryText, err := c.summarizeMessages(ctx, headMsgs, customInstructions)
	if err != nil {
		return nil, fmt.Errorf("summarize failed: %w", err)
	}

	if keepFrom < len(shortMsgs) {
		// Normal compact: call PartialCompact to split head/tail.
		pcr, err := c.store.PartialCompact(c.engine.SessionID(), shortMsgs, keepFrom)
		if err != nil {
			c.logger.Error("PartialCompact failed", "error", err)
			return nil, err
		}

		built := c.buildResultMessages(pcr, summaryText)
		pcr.Summary = summaryText
		pcr.BeforeTokens = beforeTokens
		pcr.BeforeMessages = len(messages)
		pcr.AfterTokens = EstimateMessagesTokens(built)
		pcr.Messages = built
		return pcr, nil
	}

	// keepFrom == len: compact everything (tail=0).
	// Build [boundary, summary] directly — no PartialCompact needed.
	built := c.buildCompactAllResult(summaryText)
	boundary := short.CreateCompactBoundaryMessage("auto", 0, "")
	return &short.CompactResult{
		BoundaryMarker: boundary,
		Summary:        summaryText,
		BeforeTokens:   beforeTokens,
		BeforeMessages: len(messages),
		AfterTokens:    EstimateMessagesTokens(built),
		Messages:       built,
	}, nil
}

// findKeepFrom determines how many recent messages to keep (count from tail).
// Pure token-based: walk backwards from tail, keep adding messages until the
// token budget (contextWindow/5, clamped to [2K, 60K]) is exceeded.
//
// Tail range: [0, targetKeepTokens].
//   - tail=0: nothing fits in budget, compact everything into summary
//   - tail=K: K tokens of recent messages kept verbatim
//
// Returns the split index keepFrom. head = messages[:keepFrom], tail = messages[keepFrom:].
func (c *AutoCompactor) findKeepFrom(messages []*short.TranscriptMessage) int {
	if len(messages) == 0 {
		return 0
	}

	targetKeepTokens := max(min(c.engine.ContextWindow()/5, 60000), 2000)

	totalTokens := 0
	for i := len(messages) - 1; i >= 0; i-- {
		tokens := types.EstimateTokens(messages[i].Content)
		if totalTokens+tokens > targetKeepTokens {
			return i + 1
		}
		totalTokens += tokens
	}
	// All messages fit in budget — nothing to compact.
	return len(messages)
}

// summarizeMessages calls the LLM to generate a summary of the given messages.
// Uses the full compact prompt template from short.GetCompactPrompt.
// customInstructions, when non-empty, is appended to the compact prompt via
// GetCompactPrompt — TS align: compact.ts:compactConversation(customInstructions).
func (c *AutoCompactor) summarizeMessages(ctx context.Context, messages []*short.TranscriptMessage, customInstructions string) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	// Build the conversation text for the summarizer
	var sb strings.Builder
	for _, msg := range messages {
		role := msg.Type
		if role == "" {
			role = "unknown"
		}
		// Extract text content from blocks
		text := extractTextFromShortContent(msg.Content)
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "[%s] %s\n\n", role, text)
	}

	conversationText := strings.TrimSpace(sb.String())
	if conversationText == "" {
		// No extractable text in head messages — can't summarize.
		// Return error so the engine treats this as a no-op.
		return "", fmt.Errorf("no extractable text in head messages for summarization")
	}

	// Build the summarization request
	// TS align: compact.ts:1292-1304 — system prompt is short, compact prompt is a user message,
	// conversation messages are included as individual messages.
	model := c.engine.Model()
	maxTokens := 16000
	if maxTokens <= 0 {
		maxTokens = 16000
	}

	// Convert head messages to engine messages for the API call.
	apiMsgs := make([]types.Message, 0, len(messages)+1)
	for _, m := range messages {
		apiMsgs = append(apiMsgs, short.StoreMessageToEngine(m))
	}
	// Strip images before summarization so media payloads do not reach the
	// summarizer. Source: compact.ts:1294.
	apiMsgs = StripImagesFromMessages(apiMsgs)
	// Normalize + repair tool_use/tool_result pairing. The main callLLM
	// path does this (engine.go ~line 2665-2670); without it here, an
	// orphan tool_use reaches the provider and triggers errors like
	// minimax 2013 "tool call result does not follow tool call".
	apiMsgs = NormalizeMessagesForAPI(apiMsgs)
	apiMsgs = EnsureToolResultPairing(apiMsgs)

	// Compact prompt as the last user message (TS: compact.ts:441-443)
	compactPromptUserMsg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(short.GetCompactPrompt(customInstructions))},
	}
	apiMsgs = append(apiMsgs, compactPromptUserMsg)

	estimatedTokens := TokenCountWithEstimation(apiMsgs)
	c.logger.Info("compact:summarize_request",
		"headMessages", len(messages),
		"apiMessages", len(apiMsgs),
		"estimatedTokens", estimatedTokens,
		"maxTokens", maxTokens,
		"contextWindow", c.engine.ContextWindow(),
		"model", model,
	)

	sysPrompt, _ := json.Marshal("You are a helpful AI assistant tasked with summarizing conversations.")
	req := &llm.Request{
		Model:     model,
		MaxTokens: maxTokens,
		System:    sysPrompt,
		Messages:  apiMsgs,
		Stream:    false,
	}

	resp, err := c.engine.Provider().Complete(ctx, req)
	if err != nil {
		c.logger.Error("compact:summarize_failed",
			"headMessages", len(messages),
			"apiMessages", len(apiMsgs),
			"estimatedTokens", estimatedTokens,
			"maxTokens", maxTokens,
			"contextWindow", c.engine.ContextWindow(),
			"model", model,
			"error", err,
		)
		return "", fmt.Errorf("summarize LLM call: %w", err)
	}

	// Extract text from response
	var summaryText string
	for _, block := range resp.Content {
		if block.Type == types.ContentTypeText && block.Text != "" {
			summaryText = block.Text
			break
		}
	}

	if summaryText == "" {
		return "", fmt.Errorf("summarize: no text in LLM response")
	}

	formatted := short.FormatCompactSummary(summaryText)
	c.logger.Info("compact:llm_summary",
		"raw_len", len(summaryText),
		"formatted_len", len(formatted),
		"has_summary_tag", strings.Contains(summaryText, "<summary>"),
		"raw_preview", summaryText[:min(200, len(summaryText))],
	)
	return formatted, nil
}

// buildResultMessages assembles the post-compact message array.
// Order: [boundary_user_msg, summary_user_msg, kept_messages...]
func (c *AutoCompactor) buildResultMessages(result *short.CompactResult, summaryText string) []types.Message {
	msgs := make([]types.Message, 0, 2+len(result.MessagesToKeep))

	// Boundary message: "[Previous conversation compacted]"
	boundaryContent := ""
	if result.BoundaryMarker != nil {
		boundaryContent = extractTextFromShortContent(result.BoundaryMarker.Content)
	}
	if boundaryContent == "" {
		boundaryContent = "Previous conversation compacted"
	}
	msgs = append(msgs, types.Message{
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.NewTextBlock(boundaryContent)},
		Timestamp: time.Now(),
		Flags:     types.FlagCompactSummary,
	})

	// Summary message (if available)
	if summaryText != "" {
		summaryContent := short.GetCompactUserSummaryMessage(summaryText, true, "", "recent messages are preserved")
		msgs = append(msgs, types.Message{
			Role:      types.RoleUser,
			Content:   []types.ContentBlock{types.NewTextBlock(summaryContent)},
			Timestamp: time.Now(),
			Flags:     types.FlagCompactSummary,
		})
	}

	// Kept messages (converted back to types.Message)
	for _, m := range result.MessagesToKeep {
		converted := short.StoreMessageToEngine(m)
		msgs = append(msgs, converted)
	}

	// Remove orphaned tool_results: tool_result blocks whose tool_use was in
	// the removed head. The API rejects tool_results without matching tool_use.
	msgs = removeOrphanedToolResults(msgs)

	return msgs
}

// buildCompactAllResult builds the post-compact message array when tail=0
// (compact everything). Returns [boundary_msg, summary_msg].
func (c *AutoCompactor) buildCompactAllResult(summaryText string) []types.Message {
	msgs := make([]types.Message, 0, 2)

	// Boundary message using compact_boundary format (same as PartialCompact).
	contentMap := map[string]any{
		"type":            "system",
		"subtype":         "compact_boundary",
		"content":         "Conversation compacted",
		"isMeta":          false,
		"compactMetadata": map[string]any{"trigger": "auto"},
	}
	boundaryBytes, _ := json.Marshal(contentMap)
	msgs = append(msgs, types.Message{
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.NewTextBlock(string(boundaryBytes))},
		Timestamp: time.Now(),
		Flags:     types.FlagCompactSummary,
	})

	// Summary message
	if summaryText != "" {
		summaryContent := short.GetCompactUserSummaryMessage(summaryText, true, "", "entire conversation was compacted")
		msgs = append(msgs, types.Message{
			Role:      types.RoleUser,
			Content:   []types.ContentBlock{types.NewTextBlock(summaryContent)},
			Timestamp: time.Now(),
			Flags:     types.FlagCompactSummary,
		})
	}

	return msgs
}

// removeOrphanedToolResults strips tool_result blocks whose tool_use_id has no
// matching tool_use block in the message array. After compact, the head (removed)
// may contain tool_use blocks whose tool_result is in the tail (kept). The API
// rejects tool_results without matching tool_use, so we must remove them.
func removeOrphanedToolResults(msgs []types.Message) []types.Message {
	// Collect all tool_use IDs.
	toolUseIDs := make(map[string]bool)
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolUse {
				toolUseIDs[block.ID] = true
			}
		}
	}

	// Walk messages, strip orphaned tool_results.
	changed := false
	result := make([]types.Message, len(msgs))
	for i, msg := range msgs {
		result[i] = msg
		if msg.Role != types.RoleUser {
			continue
		}
		hasOrphan := false
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult && !toolUseIDs[block.ToolUseID] {
				hasOrphan = true
				break
			}
		}
		if !hasOrphan {
			continue
		}
		// Rebuild content without orphaned blocks.
		newContent := make([]types.ContentBlock, 0, len(msg.Content))
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult && !toolUseIDs[block.ToolUseID] {
				continue // skip orphan
			}
			newContent = append(newContent, block)
		}
		result[i] = msg
		result[i].Content = newContent
		changed = true
	}

	if !changed {
		return msgs
	}
	return result
}

// extractTextFromShortContent extracts readable text from a short.TranscriptMessage's JSON content.
func extractTextFromShortContent(content string) string {
	if content == "" {
		return ""
	}

	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		// Not JSON — treat as plain text
		return content
	}

	var sb strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			sb.WriteString(block.Text)
			sb.WriteString(" ")
		case "tool_use":
			if block.Name != "" {
				fmt.Fprintf(&sb, "[%s] ", block.Name)
			}
		case "tool_result":
			// Extract string content from tool result
			if len(block.Content) > 0 {
				var s string
				if json.Unmarshal(block.Content, &s) == nil {
					sb.WriteString(s)
					sb.WriteString(" ")
				}
			}
		}
	}
	return strings.TrimSpace(sb.String())
}
