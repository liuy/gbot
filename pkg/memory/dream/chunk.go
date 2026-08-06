package dream

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/memory/short"
)

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
