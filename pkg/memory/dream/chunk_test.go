package dream

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// testTimeBase is a fixed timestamp for deterministic test data.
var testTimeBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// testMessages builds n TranscriptMessages with deterministic content.
func testMessages(n int) []*short.TranscriptMessage {
	var msgs []*short.TranscriptMessage
	for i := range n {
		msgs = append(msgs, &short.TranscriptMessage{
			Type:      "user",
			Content:   fmt.Sprintf(`[{"type":"text","text":"msg %d"}]`, i),
			CreatedAt: testTimeBase.Add(time.Duration(i) * time.Minute),
		})
	}
	return msgs
}

func TestChunkByTokens_Empty(t *testing.T) {
	result := chunkByTokens(nil, 100000)
	if result != nil {
		t.Errorf("expected nil for empty input, got %d chunks", len(result))
	}
}

func TestChunkByTokens_SingleChunk(t *testing.T) {
	msgs := testMessages(3)
	chunks := chunkByTokens(msgs, 100000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if len(chunks[0]) != 3 {
		t.Errorf("expected 3 messages in chunk, got %d", len(chunks[0]))
	}
}

func TestChunkByTokens_MultipleChunks(t *testing.T) {
	// Each message has ~1000 chars of text = ~250 tokens
	// budget = 8000/2 = 4000 tokens = ~16 messages per chunk
	// With 50 messages, expect ~4 chunks
	var msgs []*short.TranscriptMessage
	for i := range 50 {
		msgs = append(msgs, &short.TranscriptMessage{
			Type:      "user",
			Content:   fmt.Sprintf(`[{"type":"text","text":"%s"}]`, strings.Repeat("x", 1000)),
			CreatedAt: testTimeBase.Add(time.Duration(i) * time.Minute),
		})
	}

	chunks := chunkByTokens(msgs, 8000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for 50 large messages with small budget, got %d", len(chunks))
	}

	// Verify all messages are accounted for
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	if total != 50 {
		t.Errorf("message count mismatch: %d in chunks vs 50 original", total)
	}

	// Verify message ordering preserved across chunks
	firstMsg := chunks[0][0]
	lastChunk := chunks[len(chunks)-1]
	lastMsg := lastChunk[len(lastChunk)-1]
	if firstMsg.CreatedAt.After(lastMsg.CreatedAt) {
		t.Error("messages should be in chronological order across chunks")
	}
}

func TestChunkByTokens_MinimumBudgetFloor(t *testing.T) {
	// contextWindow=1000 → budget=500, but floor at 2000
	// A single small message should fit in one chunk
	msgs := testMessages(1)
	chunks := chunkByTokens(msgs, 1000)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk with minimum budget floor, got %d", len(chunks))
	}
}

func TestFormatMessages_Output(t *testing.T) {
	ts := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	msgs := []*short.TranscriptMessage{
		{
			Type:      "user",
			Content:   `[{"type":"text","text":"Hello world"}]`,
			CreatedAt: ts,
		},
		{
			Type:      "assistant",
			Content:   `[{"type":"text","text":"Hi there"}]`,
			CreatedAt: ts.Add(1 * time.Minute),
		},
	}

	result := formatMessages(msgs, 1, 3)

	if !strings.Contains(result, "chunk 1/3") {
		t.Error("should contain chunk number")
	}
	if !strings.Contains(result, "[user 2026-03-15 14:30] Hello world") {
		t.Errorf("missing user message line, got: %s", result)
	}
	if !strings.Contains(result, "[assistant 2026-03-15 14:31] Hi there") {
		t.Errorf("missing assistant message line, got: %s", result)
	}
}

func TestFormatMessages_EmptyText(t *testing.T) {
	ts := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	msgs := []*short.TranscriptMessage{
		{
			Type:      "user",
			Content:   `[]`, // no text blocks
			CreatedAt: ts,
		},
	}

	result := formatMessages(msgs, 1, 1)
	if !strings.Contains(result, "[user 2026-03-15 14:30]") {
		t.Error("should contain user line even with empty text")
	}
}
