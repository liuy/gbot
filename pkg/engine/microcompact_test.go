package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestImageMaxTokenSize(t *testing.T) {
	if ImageMaxTokenSize != 2000 {
		t.Errorf("ImageMaxTokenSize = %d, want 2000", ImageMaxTokenSize)
	}
}

func TestEstimateMessagesTokens_DocumentBlock(t *testing.T) {
	base := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}}
	baseTokens := EstimateMessagesTokens(base)
	// estTokens wins: a 2 MiB doc with a send-time estimate of 12345 must
	// count as 12345, not size/4 (524288).
	withEst := append(slices.Clone(base), types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
		types.NewDocumentBlock("a.pdf", "/x/a.pdf", "application/pdf", 2<<20, 12345),
	}})
	got := EstimateMessagesTokens(withEst) - baseTokens
	if want := 12345 + defaultMessageEnvelopeTokens; got != want {
		t.Errorf("document with estTokens estimated at %d, want %d (est+envelope)", got, want)
	}
	// estTokens 0 (parse failed at send) falls back to size/4.
	fallback := append(slices.Clone(base), types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
		types.NewDocumentBlock("a.bin", "/x/a.bin", "application/octet-stream", 400, 0),
	}})
	got = EstimateMessagesTokens(fallback) - baseTokens
	if want := 100 + defaultMessageEnvelopeTokens; got != want {
		t.Errorf("document fallback estimated at %d, want %d (size/4+envelope)", got, want)
	}
}

func TestQuerySourceReplMainThread(t *testing.T) {
	if QuerySourceReplMainThread != "repl_main_thread" {
		t.Errorf("QuerySourceReplMainThread = %q, want %q", QuerySourceReplMainThread, "repl_main_thread")
	}
}

func TestCompactableTools(t *testing.T) {
	expected := map[string]bool{
		"Read":  true,
		"Bash":  true,
		"Grep":  true,
		"Glob":  true,
		"Edit":  true,
		"Write": true,
	}
	for name := range expected {
		if !compactableTools[name] {
			t.Errorf("compactableTools missing %q", name)
		}
	}
	// Must NOT contain tools that gbot does not implement
	notExpected := []string{"WebSearch", "WebFetch"}
	for _, name := range notExpected {
		if compactableTools[name] {
			t.Errorf("compactableTools should not contain %q", name)
		}
	}
	if len(compactableTools) != len(expected) {
		t.Errorf("compactableTools has %d entries, want %d", len(compactableTools), len(expected))
	}
}

// ---------------------------------------------------------------------------
// EstimateTokens
// ---------------------------------------------------------------------------

func TestEstimateTokens(t *testing.T) {
	// Seed model: CJK*0.72 + Struct*1.38 + Digit*1.34 + Indent*(-0.04) +
	// Words*0.48 + Other*0.11, truncated and floored at 0. Expectations
	// hand-computed; old 2-feature values (CJK*0.65/nonCJK*0.20) noted where
	// they differed.
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 0},          // Other 1, Words 1 → 0.11+0.48 = 0.59 → 0
		{"abcd", 0},       // Other 4, Words 1 → 0.44+0.48 = 0.92 → 0
		{"abcdefgh", 1},   // Other 8, Words 1 → 0.88+0.48 = 1.36 → 1
		{"abcdefghij", 1}, // Other 10, Words 1 → 1.10+0.48 = 1.58 → 1 (old: 2)
		// CJK: 2*0.72 + 0.48 = 1.92 → 1
		{"你好", 1},
		// 4*0.72 + 0.48 = 3.36 → 3 (old: 2)
		{"你好世界", 3},
		// Mixed: Other 6 (incl. space) + CJK 2, Words 2 → 0.66+1.44+0.96 = 3.06 → 3 (old: 2)
		{"Hello 你好", 3},
		// hiragana NOT in isCJKRune → Other 5, Words 1 → 0.55+0.48 = 1.03 → 1
		{"こんにちは", 1},
		// hangul IS CJK: 5*0.72 + 0.48 = 4.08 → 4 (old: 3)
		{"안녕하세요", 4},
		// Other 6, CJK 2, Words 1 → 0.66+1.44+0.48 = 2.58 → 2
		{"abc你好def", 2},
	}
	for _, tt := range tests {
		got := utils.EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// calculateToolResultTokens
// ---------------------------------------------------------------------------

func TestCalculateToolResultTokens_StringContent(t *testing.T) {
	text := "Hello, this is a tool result with some content"
	content := json.RawMessage(`"` + text + `"`)
	got := calculateToolResultTokens(content)
	want := utils.EstimateTokens(text)
	if got != want {
		t.Errorf("calculateToolResultTokens(string) = %d, want %d", got, want)
	}
}

func TestCalculateToolResultTokens_ImageContent(t *testing.T) {
	// Array with image block → ImageMaxTokenSize
	content := json.RawMessage(`[{"type":"image","source":{"type":"base64","data":"..."}}]`)
	got := calculateToolResultTokens(content)
	if got != ImageMaxTokenSize {
		t.Errorf("calculateToolResultTokens(image) = %d, want %d", got, ImageMaxTokenSize)
	}
}

func TestCalculateToolResultTokens_MixedArray(t *testing.T) {
	// Array with text + image blocks
	content := json.RawMessage(`[{"type":"text","text":"Hello world"},{"type":"image","source":{"type":"base64","data":"..."}}]`)
	got := calculateToolResultTokens(content)
	wantText := utils.EstimateTokens("Hello world")
	want := wantText + ImageMaxTokenSize
	if got != want {
		t.Errorf("calculateToolResultTokens(mixed) = %d, want %d", got, want)
	}
}

func TestCalculateToolResultTokens_EmptyContent(t *testing.T) {
	// nil and empty raw JSON carry no content blocks to estimate.
	want := 0
	got := calculateToolResultTokens(nil)
	if got != want {
		t.Errorf("calculateToolResultTokens(nil) = %d, want %d", got, want)
	}
	got = calculateToolResultTokens(json.RawMessage{})
	if got != want {
		t.Errorf("calculateToolResultTokens(empty) = %d, want %d", got, want)
	}
}

func TestCalculateToolResultTokens_DocumentBlock(t *testing.T) {
	content := json.RawMessage(`[{"type":"document","source":{"type":"base64","data":"..."}}]`)
	got := calculateToolResultTokens(content)
	if got != ImageMaxTokenSize {
		t.Errorf("calculateToolResultTokens(document) = %d, want %d", got, ImageMaxTokenSize)
	}
}

// ---------------------------------------------------------------------------
// collectCompactableToolIds
// ---------------------------------------------------------------------------

func TestCollectCompactableToolIds_Basic(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("id2", "Grep", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"result1"`), false),
			types.NewToolResultBlock("id2", json.RawMessage(`"result2"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id3", "Edit", json.RawMessage(`{}`)),
		}},
	}
	ids := collectCompactableToolIds(messages)
	if len(ids) != 3 {
		t.Fatalf("collectCompactableToolIds returned %d ids, want 3", len(ids))
	}
	want := []string{"id1", "id2", "id3"}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], id)
		}
	}
}

func TestCollectCompactableToolIds_NonCompactable(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Agent", json.RawMessage(`{}`)),
			types.NewToolUseBlock("id2", "Read", json.RawMessage(`{}`)),
		}},
	}
	ids := collectCompactableToolIds(messages)
	if len(ids) != 1 {
		t.Fatalf("collectCompactableToolIds returned %d ids, want 1", len(ids))
	}
	if ids[0] != "id2" {
		t.Errorf("ids[0] = %q, want %q", ids[0], "id2")
	}
}

func TestCollectCompactableToolIds_Empty(t *testing.T) {
	ids := collectCompactableToolIds(nil)
	if len(ids) != 0 {
		t.Errorf("collectCompactableToolIds(nil) = %d ids, want 0", len(ids))
	}

	ids = collectCompactableToolIds([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	})
	if len(ids) != 0 {
		t.Errorf("collectCompactableToolIds(user-only) = %d ids, want 0", len(ids))
	}
}

// ---------------------------------------------------------------------------
// isMainThreadSource
// ---------------------------------------------------------------------------

func TestIsMainThreadSource(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", true},
		{"repl_main_thread", true},
		{"repl_main_thread:outputStyle:custom", true},
		{"repl_main_thread:x", true},
		{"agent:builtin:Explore", false},
		{"sdk", false},
		{"compact", false},
	}
	for _, tt := range tests {
		got := isMainThreadSource(tt.input)
		if got != tt.want {
			t.Errorf("isMainThreadSource(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// EstimateMessagesTokens
// ---------------------------------------------------------------------------

func TestEstimateMessagesTokens_Basic(t *testing.T) {
	text := "Hello world, this is a test message"
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(text),
		}},
	}
	got := EstimateMessagesTokens(messages)
	raw := utils.EstimateTokens(text)
	want := raw + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(basic) = %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_ToolResult(t *testing.T) {
	text := "tool output here"
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"`+text+`"`), false),
		}},
	}
	got := EstimateMessagesTokens(messages)
	want := utils.EstimateTokens(text) + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(tool_result) = %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_Thinking(t *testing.T) {
	thinkingText := "Let me think about this carefully"
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Thinking: thinkingText},
		}},
	}
	got := EstimateMessagesTokens(messages)
	want := utils.EstimateTokens(thinkingText) + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(thinking) = %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_RedactedThinking(t *testing.T) {
	data := "redacted-data-abc123"
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeRedacted, Data: data},
		}},
	}
	got := EstimateMessagesTokens(messages)
	want := utils.EstimateTokens(data) + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(redacted_thinking) = %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_ToolUse(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/test.go"}`)
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", input),
		}},
	}
	got := EstimateMessagesTokens(messages)
	combined := "Read" + string(input)
	want := utils.EstimateTokens(combined) + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(tool_use) = %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_UnknownBlockType(t *testing.T) {
	// Fallback: JSON marshal unknown block types
	block := types.ContentBlock{Type: "server_tool_use", Text: "some data"}
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{block}},
	}
	got := EstimateMessagesTokens(messages)
	// Compute expected the same way the function does: JSON marshal → EstimateTokens → envelope
	raw, _ := json.Marshal(block)
	want := utils.EstimateTokens(string(raw)) + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(unknown block) = %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_SkipsNonUserAssistant(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{
			types.NewTextBlock("system prompt"),
		}},
	}
	got := EstimateMessagesTokens(messages)
	// System messages are skipped entirely, leaving nothing to estimate.
	want := 0
	if got != want {
		t.Errorf("EstimateMessagesTokens should skip system messages, got %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_Padding(t *testing.T) {
	// Verify per-message envelope overhead is applied
	text := "short"
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(text),
		}},
	}
	got := EstimateMessagesTokens(messages)
	raw := utils.EstimateTokens(text) // "short": Other 5, Words 1 → 0.55+0.48 = 1.03 → 1
	want := raw + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("envelope: got %d, want %d (raw=%d + envelope=%d)", got, want, raw, defaultMessageEnvelopeTokens)
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestEstimateMessagesTokens_MultipleMessageTypes(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("user text"),
			types.NewToolResultBlock("id1", json.RawMessage(`"tool output"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Thinking: "thinking text"},
			types.NewTextBlock("assistant text"),
			types.NewToolUseBlock("id2", "Read", json.RawMessage(`{"path":"/x"}`)),
		}},
	}
	got := EstimateMessagesTokens(messages)
	// Compute expected from all block types:
	// text "user text" + tool_result "tool output" + thinking "thinking text" +
	// text "assistant text" + tool_use name+input "Read" + `{"path":"/x"}`
	raw := utils.EstimateTokens("user text") +
		utils.EstimateTokens("tool output") +
		utils.EstimateTokens("thinking text") +
		utils.EstimateTokens("assistant text") +
		utils.EstimateTokens("Read"+`{"path":"/x"}`)
	want := raw + 2*defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(multiple) = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// Token-based microcompact tests
// ---------------------------------------------------------------------------

func TestTokenBasedMicrocompact_UnderBudget(t *testing.T) {
	t.Parallel()
	// Tokens under budget → return nil
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"small"`), false),
		}},
	}
	result := maybeTokenBasedMicrocompact(msgs, 100, 1000, TokenPruneConfig{Enabled: true, KeepRecent: 5}, QuerySourceReplMainThread, nil)
	if result != nil {
		t.Error("expected nil when under budget")
	}
}

func TestTokenBasedMicrocompact_OverBudget_ClearsOld(t *testing.T) {
	t.Parallel()
	// 7 Read results, KeepRecent=2 → clear 5 oldest, keep 2 newest
	// currentTokens=50000 > tokenBudget=10000 → triggers pruning
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t3", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t4", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t5", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t6", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t7", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"content 1"`), false),
			types.NewToolResultBlock("t2", json.RawMessage(`"content 2"`), false),
			types.NewToolResultBlock("t3", json.RawMessage(`"content 3"`), false),
			types.NewToolResultBlock("t4", json.RawMessage(`"content 4"`), false),
			types.NewToolResultBlock("t5", json.RawMessage(`"content 5"`), false),
			types.NewToolResultBlock("t6", json.RawMessage(`"content 6"`), false),
			types.NewToolResultBlock("t7", json.RawMessage(`"content 7"`), false),
		}},
	}

	result := maybeTokenBasedMicrocompact(messages, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 2}, QuerySourceReplMainThread, nil)
	if result == nil {
		t.Fatal("expected pruning result")
	}
	if result.Cleared != 5 {
		t.Errorf("cleared %d, want 5", result.Cleared)
	}
	if result.TokensSaved <= 0 {
		t.Error("expected positive tokensSaved")
	}

	// Verify: t1-t5 cleared, t6-t7 kept
	userMsg := result.Messages[1]
	clearedIDs := map[string]bool{"t1": true, "t2": true, "t3": true, "t4": true, "t5": true}
	keptIDs := map[string]bool{"t6": true, "t7": true}
	for _, block := range userMsg.Content {
		if block.Type != types.ContentTypeToolResult {
			continue
		}
		content := string(block.Content)
		isCleared := content == `"[Old tool result content cleared]"`
		if clearedIDs[block.ToolUseID] {
			if !isCleared {
				t.Errorf("tool %s should be cleared, got content: %s", block.ToolUseID, content)
			}
		}
		if keptIDs[block.ToolUseID] {
			if isCleared {
				t.Errorf("tool %s should be kept, got cleared", block.ToolUseID)
			}
		}
	}
}

func TestTokenBasedMicrocompact_SkipsPersisted(t *testing.T) {
	t.Parallel()
	// Persisted results should not be cleared.
	// 3 tools: t1 persisted (in clear set), t2 normal (in clear set), t3 kept (KeepRecent=1).
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t3", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"<persisted-output>big content</persisted-output>"`), false),
			types.NewToolResultBlock("t2", json.RawMessage(`"normal content"`), false),
			types.NewToolResultBlock("t3", json.RawMessage(`"content 3"`), false),
		}},
	}
	result := maybeTokenBasedMicrocompact(messages, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 1}, QuerySourceReplMainThread, nil)
	if result == nil {
		t.Fatal("expected pruning result")
	}
	// KeepRecent=1: keep t3, clear t1+t2.
	// t1 is persisted -> skipped (not cleared).
	// t2 is normal -> cleared.
	if result.Cleared != 1 {
		t.Errorf("cleared %d, want 1 (t1 persisted skipped, t2 cleared)", result.Cleared)
	}
	// Verify t1 content unchanged (still has persisted-output)
	for _, block := range result.Messages[1].Content {
		if block.Type == types.ContentTypeToolResult && block.ToolUseID == "t1" {
			if !bytes.Contains(block.Content, []byte("persisted-output")) {
				t.Error("persisted result t1 should not be cleared")
			}
		}
	}
}

func TestTokenBasedMicrocompact_SkipsAlreadyCleared(t *testing.T) {
	t.Parallel()
	// Already-cleared results should not be double-processed
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t3", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"[Old tool result content cleared]"`), false),
			types.NewToolResultBlock("t2", json.RawMessage(`"content 2"`), false),
			types.NewToolResultBlock("t3", json.RawMessage(`"content 3"`), false),
		}},
	}
	result := maybeTokenBasedMicrocompact(messages, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 1}, QuerySourceReplMainThread, nil)
	if result == nil {
		t.Fatal("expected pruning result")
	}
	// KeepRecent=1: keep t3, clear t1+t2. t1 already cleared → skip. t2 cleared.
	if result.Cleared != 1 {
		t.Errorf("cleared %d, want 1 (t1 already cleared, t2 cleared now)", result.Cleared)
	}
}

func TestTokenBasedMicrocompact_NoCompactableTools(t *testing.T) {
	t.Parallel()
	// No compactable tools → nothing to clear
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Agent", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"agent result"`), false),
		}},
	}
	result := maybeTokenBasedMicrocompact(messages, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 5}, QuerySourceReplMainThread, nil)
	if result != nil {
		t.Error("expected nil when no compactable tools")
	}
}

func TestTokenBasedMicrocompact_AllAlreadyCleared(t *testing.T) {
	t.Parallel()
	// All results already cleared → tokensSaved=0 → return nil
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"[Old tool result content cleared]"`), false),
			types.NewToolResultBlock("t2", json.RawMessage(`"[Old tool result content cleared]"`), false),
		}},
	}
	result := maybeTokenBasedMicrocompact(messages, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 0}, QuerySourceReplMainThread, nil)
	if result != nil {
		t.Error("expected nil when all already cleared")
	}
}

func TestTokenBasedMicrocompact_SubAgentExcluded(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"content"`), false),
		}},
	}
	result := maybeTokenBasedMicrocompact(msgs, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 0}, QuerySourceCompact, nil)
	if result != nil {
		t.Error("expected nil for non-main-thread query source")
	}
}

func TestTokenBasedMicrocompact_SingleToolResult(t *testing.T) {
	t.Parallel()
	// Only 1 result, KeepRecent=5 → kept, nothing to clear
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"content"`), false),
		}},
	}
	result := maybeTokenBasedMicrocompact(messages, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 5}, QuerySourceReplMainThread, nil)
	if result != nil {
		t.Error("expected nil when only 1 result (≤ KeepRecent)")
	}
}

func TestCollectCompactableToolIds_Order(t *testing.T) {
	// Verify encounter order across multiple messages
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("a", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("text"),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("b", "Bash", json.RawMessage(`{}`)),
			types.NewToolUseBlock("c", "Grep", json.RawMessage(`{}`)),
		}},
	}
	ids := collectCompactableToolIds(messages)
	want := []string{"a", "b", "c"}
	if len(ids) != len(want) {
		t.Fatalf("got %d ids, want %d", len(ids), len(want))
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], id)
		}
	}
}

// ---------------------------------------------------------------------------
// TokenCountWithEstimation — TS align: tokenCountWithEstimation (tokens.ts:226)
// ---------------------------------------------------------------------------

func TestTokenCountWithEstimation_UsesLastAssistantUsage(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Usage: &types.Usage{
			InputTokens:          50000,
			OutputTokens:         100,
			CacheReadInputTokens: 30000,
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("follow-up question")}},
	}

	got := TokenCountWithEstimation(msgs)
	// getTokenCountFromUsage: 50000 + 30000 + 0 + 100 = 80100
	// Plus rough estimate for the follow-up message after the assistant
	followUpEstimate := EstimateMessagesTokens(msgs[2:])
	want := 80100 + followUpEstimate
	if got != want {
		t.Errorf("TokenCountWithEstimation = %d, want %d (80100 base + %d delta)", got, want, followUpEstimate)
	}
}

func TestTokenCountWithEstimation_FallbackWhenNoUsage(t *testing.T) {
	// Nil and empty inputs should return 0.
	if TokenCountWithEstimation(nil) > 0 {
		t.Fatal("TokenCountWithEstimation(nil) should be 0")
	}
	if TokenCountWithEstimation([]types.Message{}) > 0 {
		t.Fatal("TokenCountWithEstimation(empty) should be 0")
	}

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("follow-up")}},
	}

	got := TokenCountWithEstimation(msgs)
	want := EstimateMessagesTokens(msgs)
	if got != want {
		t.Errorf("TokenCountWithEstimation = %d, want %d (full estimation fallback)", got, want)
	}
}

func TestTokenCountWithEstimation_AfterRewind(t *testing.T) {
	// Simulate a conversation with two API responses, then rewind to the first.
	allMsgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}, Usage: &types.Usage{
			InputTokens:          50000,
			OutputTokens:         100,
			CacheReadInputTokens: 30000,
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp2")}, Usage: &types.Usage{
			InputTokens:          80000,
			OutputTokens:         200,
			CacheReadInputTokens: 50000,
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg3")}},
	}

	// Full conversation: should use last API response (130200) + msg3 estimate
	fullResult := TokenCountWithEstimation(allMsgs)
	msg3Est := EstimateMessagesTokens(allMsgs[4:])
	wantFull := 130200 + msg3Est
	if fullResult != wantFull {
		t.Errorf("full = %d, want %d", fullResult, wantFull)
	}

	// After rewind to index 2 (keep msg1+resp1): should use first API response (80100)
	remaining := allMsgs[:2]
	rewindResult := TokenCountWithEstimation(remaining)
	if rewindResult != 80100 {
		t.Errorf("after rewind = %d, want 80100 (precise first API response)", rewindResult)
	}
}

func TestTokenCountWithEstimation_ZeroUsageSkipped(t *testing.T) {
	// API may return Usage with all zeros (non-nil pointer).
	// TokenCountWithEstimation should skip this and either find a real
	// Usage or fall back to estimation. Must NOT return 0 for messages
	// with real content.
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello world this is a test")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi there")}, Usage: &types.Usage{
			InputTokens:              0,
			OutputTokens:             0,
			CacheReadInputTokens:     0,
			CacheCreationInputTokens: 0,
		}},
	}

	got := TokenCountWithEstimation(msgs)
	if got == 0 {
		t.Errorf("TokenCountWithEstimation returned 0 for messages with real content — should skip zero Usage and fall back to estimation")
	}

	// Must match pure estimation since zero Usage should be ignored
	want := EstimateMessagesTokens(msgs)
	if got != want {
		t.Errorf("TokenCountWithEstimation = %d, want %d (pure estimation after skipping zero Usage)", got, want)
	}
}

func TestTokenCountWithEstimation_ZeroUsageThenRealUsage(t *testing.T) {
	// Last assistant has zero Usage, second-to-last has real Usage.
	// Should skip the zero-Usage one and use the real one.
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}, Usage: &types.Usage{
			InputTokens: 50000,
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("follow-up")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp2")}, Usage: &types.Usage{
			// All zeros — should be skipped
		}},
	}

	got := TokenCountWithEstimation(msgs)
	// Should use base=50000 from resp1 + estimate for messages after it
	base := 50000
	delta := EstimateMessagesTokens(msgs[2:])
	want := base + delta
	if got != want {
		t.Errorf("TokenCountWithEstimation = %d, want %d (50000 base + %d delta)", got, want, delta)
	}
}

func TestEstimateMessagesTokens_ImageBlock(t *testing.T) {
	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("cat"),
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}),
		},
	}}

	got := EstimateMessagesTokens(msgs)
	// "cat" under the seed model: Other 3, Words 1 → 0.33+0.48 = 0.81 → 0.
	// want = 0 + ImageMaxTokenSize + one message envelope.
	catTokens := utils.EstimateTokens("cat")
	if catTokens != 0 {
		t.Fatalf("EstimateTokens(\"cat\") = %d, want 0 (Other 3, Words 1 → 0.81)", catTokens)
	}
	want := catTokens + ImageMaxTokenSize + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens = %d, want %d (cat %d + image %d + envelope %d)",
			got, want, catTokens, ImageMaxTokenSize, defaultMessageEnvelopeTokens)
	}
	// Sanity: image must contribute exactly ImageMaxTokenSize, not raw JSON size.
	rawJSONOnly := EstimateMessagesTokens([]types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}),
		},
	}})
	if rawJSONOnly != ImageMaxTokenSize+defaultMessageEnvelopeTokens {
		t.Errorf("image-only estimate = %d, want %d (ImageMaxTokenSize + envelope), not raw-JSON overcount",
			rawJSONOnly, ImageMaxTokenSize+defaultMessageEnvelopeTokens)
	}
}

// mcGapMessages builds Read tool_use/tool_result pairs timestamped past a
// 60-minute idle gap (historical shape: idle gaps no longer clear anything).
func mcGapMessages(oldTime time.Time, pairs, resultSize int) []types.Message {
	content := strings.Repeat("z", resultSize)
	var messages []types.Message
	for i := range pairs {
		id := fmt.Sprintf("mc-t-%d", i)
		messages = append(messages, types.Message{
			Role:      types.RoleAssistant,
			Timestamp: oldTime,
			Content: []types.ContentBlock{
				types.NewToolUseBlock(id, "Read", json.RawMessage(`{"path":"/f"}`)),
			},
		})
		messages = append(messages, types.Message{
			Role:      types.RoleUser,
			Timestamp: oldTime,
			Content: []types.ContentBlock{
				types.NewToolResultBlock(id, json.RawMessage(`"`+content+`"`), false),
			},
		})
	}
	return messages
}

func mcCountToolResults(msgs []types.Message) (intact, cleared int) {
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type != types.ContentTypeToolResult {
				continue
			}
			if string(block.Content) == `"`+TokenPrunedMessage+`"` {
				cleared++
			} else if bytes.Contains(block.Content, []byte("zzz")) {
				intact++
			}
		}
	}
	return intact, cleared
}

// TestQuery_IdleGapDoesNotClearToolResults pins the removal of the time-based
// pre-query clear: a 61-minute idle gap must leave every old tool result
// intact. Nothing may clear content by wall-clock age, and no
// engine:time_based_mc log may fire.
func TestQuery_IdleGapDoesNotClearToolResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

		mp := &mockProvider{}
		mp.addResponse(textStreamEvents("test-model", "ok."), nil)

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		eng := New(&Params{
			Provider: mp,
			Model:    "test-model",
			Logger:   logger,
			// Large window: auto-compact and the token prune stay out of the
			// way, so the only thing that could touch the sequence is an
			// idle-gap clear.
			AutoCompact: AutoCompactConfig{ContextWindow: 100000},
		})
		t.Cleanup(func() { eng.Close() })

		// 6 pairs timestamped past a 60-minute idle gap (historical shape:
		// idle gaps no longer clear anything).
		eng.SetMessages(mcGapMessages(baseTime.Add(-61*time.Minute), 6, 40))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result := eng.QuerySync(ctx, "next", "")
		if result.Error != nil {
			t.Fatalf("QuerySync error: %v", result.Error)
		}

		intact, cleared := mcCountToolResults(mp.lastRequestMessages())
		if intact != 6 {
			t.Errorf("intact tool_results sent to LLM = %d, want 6 (idle gap must not clear)", intact)
		}
		if cleared != 0 {
			t.Errorf("cleared tool_results sent to LLM = %d, want 0", cleared)
		}
		if strings.Contains(logBuf.String(), "engine:time_based_mc") {
			t.Errorf("engine:time_based_mc fired on an idle gap, log:\n%s", logBuf.String())
		}
		intact, cleared = mcCountToolResults(eng.Messages())
		if intact != 6 || cleared != 0 {
			t.Errorf("engine messages after query: intact = %d, cleared = %d, want 6/0", intact, cleared)
		}
	})
}

// TestMaybeTokenPrune_FiresWithDefaultProvider: token pressure past the
// budget → prune clears all but KeepRecent.
func TestMaybeTokenPrune_FiresWithDefaultProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

		eng := New(&Params{
			Provider:    &mockProvider{},
			Model:       "test-model",
			Logger:      slog.Default(),
			AutoCompact: AutoCompactConfig{ContextWindow: 40000},
		})
		t.Cleanup(func() { eng.Close() })

		eng.SetMessages(mcGapMessages(baseTime, 100, 3000))

		pruned := eng.maybeTokenPrune()
		if pruned == nil {
			t.Fatal("maybeTokenPrune returned nil for default provider, want prune of 95 results (100 - KeepRecent 5)")
		}
		if pruned.Cleared != 95 {
			t.Errorf("pruned.Cleared = %d, want 95 (100 pairs, KeepRecent=5)", pruned.Cleared)
		}
		if pruned.TokensSaved <= 0 {
			t.Errorf("pruned.TokensSaved = %d, want > 0", pruned.TokensSaved)
		}
	})
}

// TestLastAnchoredTokenCount_Contract pins the extracted predicate's return
// contract: anchorless input reports (0, false) — the sentinel value is part
// of the API even though callers ignore v when !ok — and an anchor that is not
// the last message still wins, billing base + est(tail).
func TestLastAnchoredTokenCount_Contract(t *testing.T) {
	t.Parallel()

	v, ok := lastAnchoredTokenCount(nil, defaultMessageEnvelopeTokens, utils.EstimateTokens)
	if ok || v != 0 {
		t.Errorf("nil transcript: got (%d, %v), want (0, false)", v, ok)
	}

	anchorless := makeLargeMessages(4, 2000)
	v, ok = lastAnchoredTokenCount(anchorless, defaultMessageEnvelopeTokens, utils.EstimateTokens)
	if ok || v != 0 {
		t.Errorf("anchorless transcript: got (%d, %v), want (0, false)", v, ok)
	}

	// Anchor at the head with a two-message tail: the backward scan must walk
	// past the tail to find it.
	msgs := append([]types.Message{{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("done")},
		Usage:   &types.Usage{InputTokens: 9000},
	}}, makeLargeMessages(2, 2000)...)
	v, ok = lastAnchoredTokenCount(msgs, defaultMessageEnvelopeTokens, utils.EstimateTokens)
	if !ok {
		t.Fatal("anchored transcript reported no anchor")
	}
	want := 9000 + EstimateMessagesTokens(msgs[1:])
	if v != want {
		t.Errorf("anchored count = %d, want base + est(tail) = %d", v, want)
	}
}

// ---------------------------------------------------------------------------
// Batch-D mutation coverage: exact-value estimation, clear/keep sets.
// ---------------------------------------------------------------------------

// One message carrying every block type: the per-case `+=` must accumulate, so
// a single-block test cannot tell `total += x` from `total = x`.
func TestMC_EstimateAllBlockTypesInOneMessage(t *testing.T) {
	t.Parallel()
	estLen := func(s string) int { return len(s) }
	unknown := types.ContentBlock{Type: "server_tool_use", Name: "srv", Text: "uu"}
	rawUnknown, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("abc"),
			{Type: types.ContentTypeThinking, Thinking: "abcd"},
			{Type: types.ContentTypeRedacted, Data: "abcde"},
			types.NewToolUseBlock("id1", "Re", json.RawMessage(`x`)),
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}),
			types.NewDocumentBlock("a.pdf", "/x/a.pdf", "application/pdf", 999, 7),
			types.NewDocumentBlock("b.bin", "/x/b.bin", "application/octet-stream", 40, 0),
			unknown,
		},
	}}
	want := 3 + 4 + 5 + len("Rex") + ImageMaxTokenSize + 7 + 40/4 + len(string(rawUnknown)) + defaultMessageEnvelopeTokens
	got := estimateMessagesTokensWith(msgs, defaultMessageEnvelopeTokens, estLen)
	if got != want {
		t.Errorf("estimateMessagesTokensWith(all block types) = %d, want %d", got, want)
	}
}

// tool_result arrays accumulate per block; a one-byte raw payload is not the
// empty-content sentinel.
func TestMC_CalculateToolResultTokens_ArrayAccumulates(t *testing.T) {
	t.Parallel()
	estLen := func(s string) int { return len(s) }
	content := json.RawMessage(`[{"type":"text","text":"abc"},{"type":"text","text":"defg"},{"type":"image"},{"type":"document"}]`)
	want := 3 + 4 + ImageMaxTokenSize + ImageMaxTokenSize
	if got := calculateToolResultTokensWith(content, estLen); got != want {
		t.Errorf("array tool_result = %d, want %d", got, want)
	}
	// len(content)==0 is the only empty sentinel: a 1-byte payload falls to the
	// raw-bytes estimate.
	if got := calculateToolResultTokensWith(json.RawMessage(`{`), estLen); got != 1 {
		t.Errorf("1-byte payload = %d, want 1 (raw-bytes fallback)", got)
	}
	if got := calculateToolResultTokensWith(nil, estLen); got != 0 {
		t.Errorf("empty payload = %d, want 0", got)
	}
}

// The usage anchor sums all four token fields.
func TestMC_LastAnchoredTokenCount_SumsEveryUsageField(t *testing.T) {
	t.Parallel()
	estLen := func(s string) int { return len(s) }
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a1")},
			Usage: &types.Usage{InputTokens: 100, CacheCreationInputTokens: 20, CacheReadInputTokens: 30, OutputTokens: 7}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("tail")}},
	}
	got, ok := lastAnchoredTokenCount(msgs, defaultMessageEnvelopeTokens, estLen)
	if !ok {
		t.Fatal("lastAnchoredTokenCount ok = false, want an anchored total")
	}
	want := 100 + 20 + 30 + 7 + (len("tail") + defaultMessageEnvelopeTokens)
	if got != want {
		t.Errorf("anchored total = %d, want %d", got, want)
	}
}

// The anchor scan walks backwards: the newest assistant with usage wins, and an
// assistant without usage does not shadow it.
func TestMC_LastAnchoredTokenCount_PicksNewestUsageBearingAssistant(t *testing.T) {
	t.Parallel()
	estLen := func(s string) int { return len(s) }
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("old")},
			Usage: &types.Usage{InputTokens: 500}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("nilusage")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("u")}},
	}
	got, ok := lastAnchoredTokenCount(msgs, defaultMessageEnvelopeTokens, estLen)
	if !ok {
		t.Fatal("ok = false, want the older usage-bearing assistant to anchor")
	}
	want := 500 + (len("nilusage") + defaultMessageEnvelopeTokens) + (len("u") + defaultMessageEnvelopeTokens)
	if got != want {
		t.Errorf("anchored total = %d, want %d", got, want)
	}
}

// The token prune defaults: on, keeping the five most recent results.
func TestTokenPrune_DefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := getTokenPruneConfig()
	if !cfg.Enabled {
		t.Error("Enabled = false, want the default on")
	}
	if cfg.KeepRecent != 5 {
		t.Errorf("KeepRecent = %d, want 5", cfg.KeepRecent)
	}
}

// collectCompactableToolIds: assistant-only, compactable-tools-only, encounter order.
func TestMC_CollectCompactableToolIds_FiltersRoleAndTool(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolUseBlock("u-read", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("a1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("a2", "WebFetch", json.RawMessage(`{}`)),
			types.NewTextBlock("note"),
			types.NewToolUseBlock("a3", "Bash", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleSystem, Content: []types.ContentBlock{
			types.NewToolUseBlock("s1", "Grep", json.RawMessage(`{}`)),
		}},
	}
	got := collectCompactableToolIds(msgs)
	want := []string{"a1", "a3"}
	if !slices.Equal(got, want) {
		t.Errorf("collectCompactableToolIds = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Token-pressure prune
// ---------------------------------------------------------------------------

func mcPruneScenario(keepRecent int) []types.Message {
	// Fixed date: the token prune orders by transcript position and never
	// compares timestamps against the clock.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var msgs []types.Message
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("p%d", i)
		msgs = append(msgs, types.Message{
			Role:      types.RoleAssistant,
			Timestamp: base,
			Content:   []types.ContentBlock{types.NewToolUseBlock(id, "Read", json.RawMessage(`{}`))},
		})
		msgs = append(msgs, types.Message{
			Role:      types.RoleUser,
			Timestamp: base,
			Content:   []types.ContentBlock{types.NewToolResultBlock(id, json.RawMessage(`"body-`+id+`-body"`), false)},
		})
	}
	return msgs
}

func TestMC_TokenPrune_BudgetBoundary(t *testing.T) {
	t.Parallel()
	cfg := TokenPruneConfig{Enabled: true, KeepRecent: 1}
	msgs := mcPruneScenario(1)
	if got := maybeTokenBasedMicrocompact(msgs, 1000, 1000, cfg, QuerySourceReplMainThread, nil); got != nil {
		t.Errorf("currentTokens == tokenBudget pruned %d results, want nil (budget is inclusive)", got.Cleared)
	}
	got := maybeTokenBasedMicrocompact(msgs, 1001, 1000, cfg, QuerySourceReplMainThread, nil)
	if got == nil {
		t.Fatal("one token over budget must prune")
	}
	if got.Cleared != 3 {
		t.Errorf("Cleared = %d, want 3 (4 compactable, KeepRecent 1)", got.Cleared)
	}
	wantSaved := 0
	for i := 1; i <= 3; i++ {
		wantSaved += calculateToolResultTokens(json.RawMessage(fmt.Sprintf(`"body-p%d-body"`, i)))
	}
	if got.TokensSaved != wantSaved {
		t.Errorf("TokensSaved = %d, want %d (sum of the three cleared results)", got.TokensSaved, wantSaved)
	}
}

func TestMC_TokenPrune_Guards(t *testing.T) {
	t.Parallel()
	msgs := mcPruneScenario(1)
	disabled := TokenPruneConfig{Enabled: false, KeepRecent: 1}
	if got := maybeTokenBasedMicrocompact(msgs, 10_000, 10, disabled, QuerySourceReplMainThread, nil); got != nil {
		t.Errorf("Enabled=false pruned %d results, want nil", got.Cleared)
	}
	if got := maybeTokenBasedMicrocompact(msgs, 10_000, 10, TokenPruneConfig{Enabled: true, KeepRecent: 1}, QuerySourceAgentCustom, nil); got != nil {
		t.Errorf("sub-agent query source pruned %d results, want nil", got.Cleared)
	}
	if got := maybeTokenBasedMicrocompact(msgs, 10_000, 10, TokenPruneConfig{Enabled: true, KeepRecent: 4}, QuerySourceReplMainThread, nil); got != nil {
		t.Errorf("KeepRecent covering every tool use pruned %d results, want nil", got.Cleared)
	}
}

// KeepRecent is floored at 1: a zero config must not clear the newest result.
func TestMC_TokenPrune_KeepRecentFlooredAtOne(t *testing.T) {
	t.Parallel()
	msgs := mcPruneScenario(0)
	got := maybeTokenBasedMicrocompact(msgs, 10_000, 10, TokenPruneConfig{Enabled: true, KeepRecent: 0}, QuerySourceReplMainThread, nil)
	if got == nil {
		t.Fatal("expected a prune")
	}
	if got.Cleared != 3 {
		t.Errorf("Cleared = %d, want 3 — KeepRecent 0 must floor to keeping exactly one result", got.Cleared)
	}
}

// Exactly one clearable id is still a prune, and the log must report it.
func TestMC_TokenPrune_SingleClearLogs(t *testing.T) {
	t.Parallel()
	msgs := mcPruneScenario(3)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	got := maybeTokenBasedMicrocompact(msgs, 10_000, 10, TokenPruneConfig{Enabled: true, KeepRecent: 3}, QuerySourceReplMainThread, logger)
	if got == nil {
		t.Fatal("expected a prune of the single oldest result")
	}
	if got.Cleared != 1 {
		t.Errorf("Cleared = %d, want 1", got.Cleared)
	}
	wantSaved := calculateToolResultTokens(json.RawMessage(`"body-p1-body"`))
	logged := buf.String()
	if !strings.Contains(logged, "engine:token_based_mc") {
		t.Fatalf("missing engine:token_based_mc log; got:\n%s", logged)
	}
	for _, want := range []string{"current_tokens=10000", "budget=10", "cleared=1", "kept=3", fmt.Sprintf("tokens_saved=%d", wantSaved)} {
		if !strings.Contains(logged, want) {
			t.Errorf("log missing %q; got:\n%s", want, logged)
		}
	}
}

// A tool_result living in an assistant message is out of scope for the pruner.
func TestMC_TokenPrune_IgnoresToolResultsOutsideUserMessages(t *testing.T) {
	t.Parallel()
	// Fixed date: the token prune never reads the clock.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msgs := []types.Message{{
		Role:      types.RoleAssistant,
		Timestamp: base,
		Content: []types.ContentBlock{
			types.NewToolUseBlock("p1", "Read", json.RawMessage(`{}`)),
			types.NewToolResultBlock("p1", json.RawMessage(`"in-assistant"`), false),
		},
	}}
	if got := maybeTokenBasedMicrocompact(msgs, 10_000, 10, TokenPruneConfig{Enabled: true, KeepRecent: 0}, QuerySourceReplMainThread, nil); got != nil {
		t.Errorf("result = non-nil, want nil: the only tool_result sits in an assistant message")
	}
}

// ---------------------------------------------------------------------------
// Microcompact message-shape pins
// ---------------------------------------------------------------------------

// toolPairMessages builds an assistant tool_use + user tool_result pair.
func toolPairMessages(id, tool, result string) []types.Message {
	return []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock(id, tool, json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock(id, json.RawMessage(`"`+result+`"`), false),
		}},
	}
}

// gapMessages builds n Read pairs. Timestamps are transcript history shape
// only — the token prune orders by transcript position and never reads the
// clock.
func gapMessages(n int) []types.Message {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var msgs []types.Message
	for i := range n {
		pair := toolPairMessages(fmt.Sprintf("g%d", i), "Read", fmt.Sprintf("content-%d", i))
		pair[0].Timestamp = old
		pair[1].Timestamp = old
		msgs = append(msgs, pair...)
	}
	return msgs
}

// blockContentOf returns the tool_result content for the block of message i
// carrying the given tool_use id.
func blockContentOf(msgs []types.Message, toolUseID string) (string, bool) {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == toolUseID {
				return string(b.Content), true
			}
		}
	}
	return "", false
}

// The collector must gate on both the block type and the tool name: a text
// block that happens to carry a compactable Name, and a tool_use for a
// non-compactable tool, are both out of scope.
func TestCollectCompactableToolIds_RequiresToolUseTypeAndCompactableName(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, ID: "cb_text", Name: "Read", Text: "named like a compactable tool"},
		types.NewToolUseBlock("tu_task", "Task", json.RawMessage(`{}`)),
		types.NewToolUseBlock("tu_read", "Read", json.RawMessage(`{}`)),
	}}}
	ids := collectCompactableToolIds(msgs)
	if len(ids) != 1 || ids[0] != "tu_read" {
		t.Errorf("collectCompactableToolIds = %v, want [tu_read]", ids)
	}
}

// A document block priced at exactly one estimated token must bill that token
// and not fall through to the size/4 estimate.
func TestEstimateMessagesTokens_DocumentEstTokensOneIsUsed(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{
		types.NewDocumentBlock("a.bin", "/x/a.bin", "application/octet-stream", 4000, 1),
	}}}
	got := EstimateMessagesTokens(msgs)
	if want := 1 + defaultMessageEnvelopeTokens; got != want {
		t.Errorf("document with EstTokens=1 estimated %d, want %d (est + envelope)", got, want)
	}
}

// Usage on a non-assistant message is not an anchor: the estimation fallback
// must still price the whole transcript.
func TestTokenCountWithEstimation_IgnoresUsageOnNonAssistant(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("assistant turn")}},
		{Role: types.RoleUser, Usage: &types.Usage{InputTokens: 50000, OutputTokens: 700},
			Content: []types.ContentBlock{types.NewTextBlock("user turn")}},
	}
	got := TokenCountWithEstimation(msgs)
	if want := EstimateMessagesTokens(msgs); got != want {
		t.Errorf("TokenCountWithEstimation = %d, want %d (user usage must not anchor)", got, want)
	}
}

// An assistant message without Usage is skipped while scanning backwards, not
// dereferenced.
func TestTokenCountWithEstimation_SkipsAssistantWithNilUsage(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("no usage yet")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("follow-up")}},
	}
	got := TokenCountWithEstimation(msgs)
	if want := EstimateMessagesTokens(msgs); got != want {
		t.Errorf("TokenCountWithEstimation = %d, want %d (nil Usage must be skipped)", got, want)
	}
}

// cacheDeletionApplied drives pkg/llm's break-detection state machine for
// a tracked query source and reports whether the deletion notice was applied.
// NotifyCacheDeletion only marks a tracked key; the marker surfaces as the
// deletion_applied record on the next response check, which is the only
// cross-package read of it. Only the anthropic provider drives this state in
// tests, so the key is free for the pins below.
func cacheDeletionApplied(t *testing.T, querySource string, prev, curr int, notify func()) bool {
	t.Helper()
	key := llm.PromptStateKey{QuerySource: querySource}
	logs := captureSlog(t)
	sys := []map[string]any{{"type": "text", "text": "system prompt"}}
	llm.RecordPromptState(sys, nil, key, "gtest-model", nil, "", false, false, false, false, "", 0)
	llm.CheckResponseForCacheBreak(key, prev, 0, nil)
	notify()
	llm.CheckResponseForCacheBreak(key, curr, 0, nil)
	_, found := logs.find("prompt_cache:deletion_applied", func(a map[string]any) bool {
		return attrInt(a["cacheReadPrev"]) == int64(prev) && attrInt(a["cacheReadCurr"]) == int64(curr)
	})
	return found
}

// The token-based clear must notify cache-break detection under the query
// source it was asked about, not an empty key.
func TestMaybeTokenBasedMicrocompact_NotifiesCacheDeletionForQuerySource(t *testing.T) {
	const src = QuerySourceReplMainThread + ":outputStyle:gtest"
	prune := TokenPruneConfig{Enabled: true, KeepRecent: 2}
	if !cacheDeletionApplied(t, src, 5000, 4900, func() {
		if r := maybeTokenBasedMicrocompact(gapMessages(7), 50000, 10000, prune, src, nil); r == nil {
			t.Error("token-based prune returned nil")
		}
	}) {
		t.Error("no prompt_cache:deletion_applied record for the pruned query source")
	}
}

// The token-based walk must copy every message it inspects: an untouched
// message keeps its role and content, and one with no blocks keeps a nil
// content slice.
func TestMaybeTokenBasedMicrocompact_UntouchedMessagesKeepTheirShape(t *testing.T) {
	t.Parallel()
	msgs := gapMessages(7)
	msgs = append(msgs,
		types.Message{Role: types.RoleUser},
		types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("trailing reply")}},
	)
	res := maybeTokenBasedMicrocompact(msgs, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 2}, QuerySourceReplMainThread, nil)
	if res == nil {
		t.Fatal("maybeTokenBasedMicrocompact = nil, want clears")
	}
	if len(res.Messages) != len(msgs) {
		t.Fatalf("result has %d messages, want %d", len(res.Messages), len(msgs))
	}
	empty := res.Messages[len(msgs)-2]
	if empty.Role != types.RoleUser {
		t.Errorf("untouched empty user message role = %q, want user", empty.Role)
	}
	if empty.Content != nil {
		t.Errorf("untouched empty user message Content = %#v, want nil", empty.Content)
	}
	tail := res.Messages[len(msgs)-1]
	if tail.Role != types.RoleAssistant || len(tail.Content) != 1 || tail.Content[0].Text != "trailing reply" {
		t.Errorf("trailing assistant message = %+v, want the original message", tail)
	}
}

// Only tool_result blocks in the clear set are rewritten.
func TestMaybeTokenBasedMicrocompact_ClearsOnlyClearSetToolResults(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("k1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("k2", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("k3", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("k4", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("k1", json.RawMessage(`"pruned result content one"`), false),
			{Type: types.ContentTypeText, ToolUseID: "k1", Content: json.RawMessage(`"text block sharing a cleared id"`)},
			types.NewToolResultBlock("k2", json.RawMessage(`"pruned result content two"`), false),
			types.NewToolResultBlock("k3", json.RawMessage(`"pruned result content three"`), false),
			types.NewToolResultBlock("k4", json.RawMessage(`"pruned result content four"`), false),
		}},
	}
	res := maybeTokenBasedMicrocompact(msgs, 50000, 10000, TokenPruneConfig{Enabled: true, KeepRecent: 2}, QuerySourceReplMainThread, nil)
	if res == nil {
		t.Fatal("maybeTokenBasedMicrocompact = nil, want clears")
	}
	if res.Cleared != 2 {
		t.Errorf("Cleared = %d, want 2 (k1, k2)", res.Cleared)
	}
	user := res.Messages[1]
	if string(user.Content[1].Content) != `"text block sharing a cleared id"` {
		t.Errorf("text block was rewritten to %s, want untouched", user.Content[1].Content)
	}
	for _, id := range []string{"k1", "k2"} {
		got, _ := blockContentOf(res.Messages, id)
		if got != `"`+TokenPrunedMessage+`"` {
			t.Errorf("%s = %s, want cleared", id, got)
		}
	}
	for _, id := range []string{"k3", "k4"} {
		got, _ := blockContentOf(res.Messages, id)
		if got == `"`+TokenPrunedMessage+`"` {
			t.Errorf("%s = cleared, want kept (within the recent window)", id)
		}
	}
}
