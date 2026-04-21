package engine

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestTimeBasedMCClearedMessage(t *testing.T) {
	if TimeBasedMCClearedMessage != "[Old tool result content cleared]" {
		t.Errorf("TimeBasedMCClearedMessage = %q, want %q", TimeBasedMCClearedMessage, "[Old tool result content cleared]")
	}
}

func TestImageMaxTokenSize(t *testing.T) {
	if ImageMaxTokenSize != 2000 {
		t.Errorf("ImageMaxTokenSize = %d, want 2000", ImageMaxTokenSize)
	}
}

func TestQuerySourceReplMainThread(t *testing.T) {
	if QuerySourceReplMainThread != "repl_main_thread" {
		t.Errorf("QuerySourceReplMainThread = %q, want %q", QuerySourceReplMainThread, "repl_main_thread")
	}
}

func TestCompactableTools(t *testing.T) {
	expected := map[string]bool{
		"Read":   true,
		"Bash":   true,
		"Search": true,
		"Find":   true,
		"Edit":   true,
		"Write":  true,
	}
	for name := range expected {
		if !compactableTools[name] {
			t.Errorf("compactableTools missing %q", name)
		}
	}
	// Must NOT contain TS names that differ in gbot
	notExpected := []string{"Grep", "Glob", "WebSearch", "WebFetch"}
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
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 0},         // 1/4 = 0
		{"abcd", 1},      // 4/4 = 1
		{"abcdefgh", 2},  // 8/4 = 2
		{"abcdefghij", 2}, // 10/4 = 2
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
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
	want := len(text) / 4
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
	wantText := len("Hello world") / 4
	want := wantText + ImageMaxTokenSize
	if got != want {
		t.Errorf("calculateToolResultTokens(mixed) = %d, want %d", got, want)
	}
}

func TestCalculateToolResultTokens_EmptyContent(t *testing.T) {
	got := calculateToolResultTokens(nil)
	if got != 0 {
		t.Errorf("calculateToolResultTokens(nil) = %d, want 0", got)
	}
	got = calculateToolResultTokens(json.RawMessage{})
	if got != 0 {
		t.Errorf("calculateToolResultTokens(empty) = %d, want 0", got)
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
			types.NewToolUseBlock("id2", "Search", json.RawMessage(`{}`)),
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
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("Hello world, this is a test message"),
		}},
	}
	got := EstimateMessagesTokens(messages)
	raw := len("Hello world, this is a test message") / 4
	want := int(math.Ceil(float64(raw) * 4.0 / 3.0))
	if got != want {
		t.Errorf("EstimateMessagesTokens(basic) = %d, want %d", got, want)
	}
	if got <= raw {
		t.Errorf("EstimateMessagesTokens should pad by 4/3, got %d <= raw %d", got, raw)
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
	// text="tool output here" (16 chars) → EstimateTokens=4 → padded=ceil(4*4/3)=6
	want := int(math.Ceil(float64(EstimateTokens(text)) * 4.0 / 3.0))
	if got != want {
		t.Errorf("EstimateMessagesTokens(tool_result) = %d, want %d", got, want)
	}
}

func TestEstimateMessagesTokens_Thinking(t *testing.T) {
	thinkingText := "Let me think about this carefully"
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: thinkingText},
		}},
	}
	got := EstimateMessagesTokens(messages)
	want := int(math.Ceil(float64(len(thinkingText)/4) * 4.0 / 3.0))
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
	want := int(math.Ceil(float64(len(data)/4) * 4.0 / 3.0))
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
	want := int(math.Ceil(float64(len(combined)/4) * 4.0 / 3.0))
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
	// Compute expected the same way the function does: JSON marshal → EstimateTokens → pad
	raw, _ := json.Marshal(block)
	want := int(math.Ceil(float64(EstimateTokens(string(raw))) * 4.0 / 3.0))
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
	if got != 0 {
		t.Errorf("EstimateMessagesTokens should skip system messages, got %d", got)
	}
}

func TestEstimateMessagesTokens_Padding(t *testing.T) {
	// Verify 4/3 padding is applied
	text := "short"
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(text),
		}},
	}
	got := EstimateMessagesTokens(messages)
	raw := len(text) / 4 // = 1
	want := int(math.Ceil(float64(raw) * 4.0 / 3.0))
	if got != want {
		t.Errorf("padding: got %d, want %d (raw=%d)", got, want, raw)
	}
}

// ---------------------------------------------------------------------------
// evaluateTimeBasedTrigger
// ---------------------------------------------------------------------------

func TestEvaluateTimeBasedTrigger_Disabled(t *testing.T) {
	orig := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = orig }()

	defaultMicrocompactConfig.TimeBased.Enabled = false
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-61 * time.Minute)},
	}
	result := EvaluateTimeBasedTrigger(messages, QuerySourceReplMainThread)
	if result != nil {
		t.Error("evaluateTimeBasedTrigger should return nil when disabled")
	}
}

func TestEvaluateTimeBasedTrigger_NoAssistant(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	result := EvaluateTimeBasedTrigger(messages, QuerySourceReplMainThread)
	if result != nil {
		t.Error("evaluateTimeBasedTrigger should return nil with no assistant message")
	}
}

func TestEvaluateTimeBasedTrigger_UnderThreshold(t *testing.T) {
	// Message from 5 minutes ago — under 60 minute threshold
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-5 * time.Minute)},
	}
	result := EvaluateTimeBasedTrigger(messages, QuerySourceReplMainThread)
	if result != nil {
		t.Error("evaluateTimeBasedTrigger should return nil when gap < threshold")
	}
}

func TestEvaluateTimeBasedTrigger_OverThreshold(t *testing.T) {
	// Save/restore nowFunc
	orig := nowFunc
	defer func() { nowFunc = orig }()

	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	// Assistant message from 61 minutes ago
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute)},
	}
	result := EvaluateTimeBasedTrigger(messages, QuerySourceReplMainThread)
	if result == nil {
		t.Fatal("evaluateTimeBasedTrigger should fire when gap >= threshold")
	}
	if result.GapMinutes < 60 {
		t.Errorf("gap should be >= 60, got %f", result.GapMinutes)
	}
}

func TestEvaluateTimeBasedTrigger_WrongSource(t *testing.T) {
	// Empty querySource → nil (time-based requires explicit source)
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-61 * time.Minute)},
	}
	result := EvaluateTimeBasedTrigger(messages, "")
	if result != nil {
		t.Error("evaluateTimeBasedTrigger should return nil for empty querySource")
	}

	result = EvaluateTimeBasedTrigger(messages, "agent:builtin:Explore")
	if result != nil {
		t.Error("evaluateTimeBasedTrigger should return nil for non-main-thread source")
	}
}

func TestEvaluateTimeBasedTrigger_PrefixSource(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()

	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute)},
	}
	result := EvaluateTimeBasedTrigger(messages, "repl_main_thread:outputStyle:custom")
	if result == nil {
		t.Error("evaluateTimeBasedTrigger should fire for prefix-matched source")
	}
}

// ---------------------------------------------------------------------------
// maybeTimeBasedMicrocompact
// ---------------------------------------------------------------------------

func TestMaybeTimeBasedMicrocompact_NoTrigger(t *testing.T) {
	// Gap under threshold → no trigger
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-5 * time.Minute)},
	}
	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	if result != nil {
		t.Error("maybeTimeBasedMicrocompact should return nil when no trigger")
	}
}

func TestMaybeTimeBasedMicrocompact_ClearsOldResults(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()

	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	// Create messages: assistant with 3 tool_uses, user with 3 tool_results
	// keepRecent=5 → all kept, none cleared → should return nil
	// Let's use a custom config with keepRecent=1
	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 1

	messages := []types.Message{
		0: {Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("tool1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("tool2", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("tool3", "Read", json.RawMessage(`{}`)),
		}},
		1: {Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("tool1", json.RawMessage(`"file content 1"`), false),
			types.NewToolResultBlock("tool2", json.RawMessage(`"file content 2"`), false),
			types.NewToolResultBlock("tool3", json.RawMessage(`"file content 3"`), false),
		}},
	}

	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	if result == nil {
		t.Fatal("maybeTimeBasedMicrocompact should clear results")
	}

	// keepRecent=1 → tool3 kept, tool1+tool2 cleared
	resultMsg := result.Messages[1] // user message with tool_results
	cleared := 0
	kept := 0
	for _, block := range resultMsg.Content {
		if block.Type == types.ContentTypeToolResult {
			content := string(block.Content)
			if content == `"[Old tool result content cleared]"` {
				cleared++
			} else {
				kept++
			}
		}
	}
	if cleared != 2 {
		t.Errorf("cleared %d tool_results, want 2", cleared)
	}
	if kept != 1 {
		t.Errorf("kept %d tool_results, want 1", kept)
	}
}

func TestMaybeTimeBasedMicrocompact_KeepsMinOne(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 0 // should floor to 1

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("tool1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("tool1", json.RawMessage(`"content"`), false),
		}},
	}

	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	// keepRecent=0 → floor to 1 → tool1 kept → clearSet empty → nil
	if result != nil {
		t.Error("with 1 tool and keepRecent=0 (floored to 1), nothing to clear → nil")
	}
}

func TestMaybeTimeBasedMicrocompact_NothingToClear(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 100 // keep all

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("tool1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("tool1", json.RawMessage(`"content"`), false),
		}},
	}

	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	if result != nil {
		t.Error("keepRecent=100 with 1 tool → nothing to clear → nil")
	}
}

func TestMaybeTimeBasedMicrocompact_AlreadyCleared(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 0 // floor to 1

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("tool1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("tool2", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			// Already cleared — should be skipped, tokensSaved = 0
			types.NewToolResultBlock("tool1", json.RawMessage(`"[Old tool result content cleared]"`), false),
		}},
	}

	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	// tool1 is already cleared, tool2 has no result → tokensSaved = 0 → nil
	if result != nil {
		t.Error("already-cleared results should be skipped, tokensSaved=0 → nil")
	}
}

// ---------------------------------------------------------------------------
// MicrocompactMessages
// ---------------------------------------------------------------------------

func TestMicrocompactMessages_TimeBasedFires(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 0

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"content1"`), false),
			types.NewToolResultBlock("t2", json.RawMessage(`"content2"`), false),
		}},
	}

	result := MicrocompactMessages(messages, QuerySourceReplMainThread, nil)
	if result.CompactionInfo != nil {
		t.Error("time-based MC should not set CompactionInfo (that's cached-MC)")
	}

	// Verify content was cleared
	cleared := 0
	for _, block := range result.Messages[1].Content {
		if block.Type == types.ContentTypeToolResult {
			if string(block.Content) == `"[Old tool result content cleared]"` {
				cleared++
			}
		}
	}
	if cleared != 1 { // keepRecent=0 → floor 1 → keep t2, clear t1
		t.Errorf("cleared %d, want 1", cleared)
	}
}

func TestMicrocompactMessages_TimeBasedSkips(t *testing.T) {
	// Recent message, no trigger
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-5 * time.Minute)},
	}
	result := MicrocompactMessages(messages, QuerySourceReplMainThread, nil)
	if len(result.Messages) != len(messages) {
		t.Error("MicrocompactMessages should return original messages when no trigger")
	}
}

func TestMicrocompactMessages_ClearsWarningSuppression(t *testing.T) {
	compactWarningSuppressed.Store(true)
	_ = MicrocompactMessages(nil, QuerySourceReplMainThread, nil)
	if compactWarningSuppressed.Load() {
		t.Error("MicrocompactMessages should clear warning suppression at start")
	}
}

// ---------------------------------------------------------------------------
// cachedMC stubs
// ---------------------------------------------------------------------------

func TestCachedMCStubs(t *testing.T) {
	if ConsumePendingCacheEdits() != nil {
		t.Error("ConsumePendingCacheEdits should return nil")
	}
	if GetPinnedCacheEdits() != nil {
		t.Error("GetPinnedCacheEdits should return nil")
	}
	// No-op functions — just verify they don't panic
	PinCacheEdits(0, nil)
	MarkToolsSentToAPIState()
	ResetMicrocompactState()
}

// ---------------------------------------------------------------------------
// Logging integration
// ---------------------------------------------------------------------------

func TestMaybeTimeBasedMicrocompact_Logs(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 0

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"data"`), false),
			types.NewToolResultBlock("t2", json.RawMessage(`"data"`), false),
		}},
	}

	maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, logger)
	if !strings.Contains(buf.String(), "engine:time_based_mc") {
		t.Errorf("expected engine:time_based_mc in log output, got: %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// NotifyCacheDeletion integration
// ---------------------------------------------------------------------------

func TestMaybeTimeBasedMicrocompact_CallsNotifyCacheDeletion(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 0

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"data"`), false),
			types.NewToolResultBlock("t2", json.RawMessage(`"data"`), false),
		}},
	}

	// This should not panic — NotifyCacheDeletion is a real function
	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
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
			{Type: types.ContentTypeThinking, Text: "thinking text"},
			types.NewTextBlock("assistant text"),
			types.NewToolUseBlock("id2", "Read", json.RawMessage(`{"path":"/x"}`)),
		}},
	}
	got := EstimateMessagesTokens(messages)
	// Compute expected from all block types:
	// text "user text" + tool_result "tool output" + thinking "thinking text" +
	// text "assistant text" + tool_use name+input "Read" + `{"path":"/x"}`
	raw := EstimateTokens("user text") +
		EstimateTokens("tool output") +
		EstimateTokens("thinking text") +
		EstimateTokens("assistant text") +
		EstimateTokens("Read"+`{"path":"/x"}`)
	want := int(math.Ceil(float64(raw) * 4.0 / 3.0))
	if got != want {
		t.Errorf("EstimateMessagesTokens(multiple) = %d, want %d", got, want)
	}
}

func TestMaybeTimeBasedMicrocompact_PreservesMessageOrder(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	baseTime := time.Now()
	nowFunc = func() time.Time { return baseTime }

	origCfg := defaultMicrocompactConfig
	defer func() { defaultMicrocompactConfig = origCfg }()
	defaultMicrocompactConfig.TimeBased.KeepRecent = 0

	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Bash", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("t1", json.RawMessage(`"data"`), false),
			types.NewTextBlock("keep this text"),
			types.NewToolResultBlock("t2", json.RawMessage(`"bash output"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("response"),
		}},
	}

	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	// Text block should be preserved
	if result.Messages[1].Content[1].Text != "keep this text" {
		t.Error("non-tool-result blocks should be preserved")
	}
	// Tool result t1 should be cleared (oldest), t2 kept (keepRecent=1)
	if string(result.Messages[1].Content[0].Content) != `"[Old tool result content cleared]"` {
		t.Error("tool result t1 should be cleared")
	}
	if string(result.Messages[1].Content[2].Content) != `"bash output"` {
		t.Error("tool result t2 should be kept")
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
			types.NewToolUseBlock("c", "Search", json.RawMessage(`{}`)),
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
