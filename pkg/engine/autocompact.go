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

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// Compactor implements Compactor by delegating to the short.Store's compact
// functions and using the LLM provider to generate a summary.
// TS align: compact.ts:compactConversation + partialCompactConversation
type AutoCompactor struct {
	store      *short.Store
	sessionID  string
	model      string
	provider   llm.Provider
	maxTokens  int // maxTokens for summary LLM call
	logger     *slog.Logger
}

// NewAutoCompactor creates a Compactor for compacting the given session.
func NewAutoCompactor(store *short.Store, sessionID, model string, provider llm.Provider) *AutoCompactor {
	return &AutoCompactor{
		store:     store,
		sessionID: sessionID,
		model:     model,
		provider:  provider,
		maxTokens: 16000,
		logger:    slog.Default(),
	}
}

// Compact compacts the conversation history by:
//  1. Keeping the most recent messages (enough for recent context)
//  2. Summarizing the older messages via LLM
//  3. Returning [boundary, summary, kept_messages]
//
// Returns the post-compact message array, matching the TS CompactionResult structure.
func (c *AutoCompactor) Compact(ctx context.Context, messages []types.Message) ([]types.Message, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("nothing to compact: no messages")
	}

	// Convert engine types → short types for store operations
	shortMsgs := engineToShort(messages)

	// Determine how many recent messages to keep.
	// Strategy: count tokens from tail, keep adding until reaching ~80K tokens
	// (well under the compact trigger threshold of ~187K).
	keepFrom := c.findKeepFrom(shortMsgs)
	if keepFrom >= len(shortMsgs) {
		// Nothing to compact — keep all messages
		return messages, nil
	}
	if keepFrom <= 1 {
		// Need at least 1 message to keep
		return messages, nil
	}

	// Call PartialCompact: creates boundary marker + splits head/tail
	result, err := c.store.PartialCompact(c.sessionID, shortMsgs, keepFrom)
	if err != nil {
		c.logger.Error("PartialCompact failed", "error", err)
		return nil, err
	}

	// Generate summary for the compacted head via LLM
	summaryText, err := c.summarizeMessages(ctx, result.MessagesToKeep)
	if err != nil {
		c.logger.Warn("summarizeMessages failed", "error", err)
		// Fall back: return messages without summary (better than failing entirely)
		return c.buildResultMessages(result, ""), nil
	}

	// Record compact in database (write boundary + summary to store)
	if err := c.store.RecordCompact(c.sessionID, result); err != nil {
		c.logger.Warn("RecordCompact failed", "error", err)
	}

	return c.buildResultMessages(result, summaryText), nil
}

// findKeepFrom determines how many recent messages to keep (count from tail).
// Counts rough tokens from the end and stops when reaching ~80K tokens,
// leaving enough budget for the summary output.
// TS align: compact.ts — keeps enough recent messages for context
func (c *AutoCompactor) findKeepFrom(messages []*short.Message) int {
	const targetKeepTokens = 80000

	// Always keep at least 4 messages (2 turns)
	minKeep := 4
	if len(messages) <= minKeep {
		return len(messages)
	}

	totalTokens := 0
	for i := len(messages) - 1; i >= minKeep-1; i-- {
		tokens := EstimateTokens(messages[i].Content)
		if totalTokens+tokens > targetKeepTokens {
			return i + 1
		}
		totalTokens += tokens
	}
	return minKeep
}

// summarizeMessages calls the LLM to generate a summary of the given messages.
// Uses the full compact prompt template from short.GetCompactPrompt.
func (c *AutoCompactor) summarizeMessages(ctx context.Context, messages []*short.Message) (string, error) {
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
		return "", nil
	}

	// Build the summarization request
	// Use a smaller model for summary generation if available, otherwise same model
	model := c.model
	maxTokens := c.maxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}

	systemPrompt := short.GetCompactPrompt("")
	userContent := fmt.Sprintf("Summarize the following conversation:\n\n%s", conversationText)

	req := &llm.Request{
		Model:     model,
		MaxTokens: maxTokens,
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock(systemPrompt)}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(userContent)}},
		},
		Stream: false,
	}

	resp, err := c.provider.Complete(ctx, req)
	if err != nil {
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

	return short.FormatCompactSummary(summaryText), nil
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
		Role:       types.RoleUser,
		Content:    []types.ContentBlock{types.NewTextBlock(boundaryContent)},
		Timestamp: time.Now(),
	})

	// Summary message (if available)
	if summaryText != "" {
		summaryContent := short.GetCompactUserSummaryMessage(summaryText, true, "", "recent messages are preserved")
		msgs = append(msgs, types.Message{
			Role:       types.RoleUser,
			Content:    []types.ContentBlock{types.NewTextBlock(summaryContent)},
			Timestamp: time.Now(),
		})
	}

	// Kept messages (converted back to types.Message)
	for _, m := range result.MessagesToKeep {
		converted := ShortMessageToEngine(m)
		msgs = append(msgs, converted)
	}

	return msgs
}

// extractTextFromShortContent extracts readable text from a short.Message's JSON content.
func extractTextFromShortContent(content string) string {
	if content == "" {
		return ""
	}

	var blocks []short.ContentBlock
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

// engineToShort converts []types.Message → []*short.Message.
func engineToShort(messages []types.Message) []*short.Message {
	result := make([]*short.Message, 0, len(messages))
	for _, m := range messages {
		contentBytes, _ := json.Marshal(m.Content)
		uid := uuid.New().String()
		result = append(result, &short.Message{
			UUID:      uid,
			ParentUUID: "",
			Type:      string(m.Role),
			Content:   string(contentBytes),
			CreatedAt: m.Timestamp,
		})
	}
	return result
}

// ShortMessageToEngine converts a *short.Message → types.Message.
func ShortMessageToEngine(m *short.Message) types.Message {
	if m == nil {
		return types.Message{}
	}

	var blocks []short.ContentBlock
	if err := json.Unmarshal([]byte(m.Content), &blocks); err != nil {
		// Fall back: treat entire content as text
		return types.Message{
			Role:      types.Role(m.Type),
			Content:   []types.ContentBlock{types.NewTextBlock(m.Content)},
			Timestamp: m.CreatedAt,
		}
	}

	engineBlocks := make([]types.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		engineBlocks = append(engineBlocks, types.ContentBlock{
			Type:       types.ContentType(b.Type),
			Text:       b.Text,
			ID:         b.ID,
			Name:       b.Name,
			Input:      b.Input,
			ToolUseID:  b.ToolUseID,
			Content:    b.Content,
			IsError:    b.IsError,
			Data:       b.Data,
		})
	}

	return types.Message{
		Role:      types.Role(m.Type),
		Content:   engineBlocks,
		Timestamp: m.CreatedAt,
	}
}
