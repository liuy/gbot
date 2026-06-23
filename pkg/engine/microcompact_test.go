package engine

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
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
	// Default provider (unknown): CJK=0.65, non-CJK=0.20
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 0},          // 1 non-CJK * 0.20 = 0.2 → 0
		{"abcd", 0},       // 4 * 0.20 = 0.8 → 0
		{"abcdefgh", 1},   // 8 * 0.20 = 1.6 → 1
		{"abcdefghij", 2}, // 10 * 0.20 = 2.0 → 2
		// CJK (default 0.65): 2 * 0.65 = 1.3 → 1
		{"你好", 1},
		// 4 * 0.65 = 2.6 → 2
		{"你好世界", 2},
		// Mixed: 6 non-CJK * 0.20 + 2 CJK * 0.65 = 1.2 + 1.3 = 2.5 → 2
		{"Hello 你好", 2},
		// hiragana NOT in isCJKRune → 5 non-CJK * 0.20 = 1.0 → 1
		{"こんにちは", 1},
		// hangul IS CJK: 5 * 0.65 = 3.25 → 3
		{"안녕하세요", 3},
		// 6 * 0.20 + 2 * 0.65 = 1.2 + 1.3 = 2.5 → 2
		{"abc你好def", 2},
	}
	for _, tt := range tests {
		got := types.EstimateTokens(tt.input)
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
	want := types.EstimateTokens(text)
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
	wantText := types.EstimateTokens("Hello world")
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
	raw := types.EstimateTokens(text)
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
	want := types.EstimateTokens(text) + defaultMessageEnvelopeTokens
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
	want := types.EstimateTokens(thinkingText) + defaultMessageEnvelopeTokens
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
	want := types.EstimateTokens(data) + defaultMessageEnvelopeTokens
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
	want := types.EstimateTokens(combined) + defaultMessageEnvelopeTokens
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
	want := types.EstimateTokens(string(raw)) + defaultMessageEnvelopeTokens
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
	// Verify per-message envelope overhead is applied
	text := "short"
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(text),
		}},
	}
	got := EstimateMessagesTokens(messages)
	raw := types.EstimateTokens(text) // "short" = 5 non-CJK chars * 0.20 = 1
	want := raw + defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("envelope: got %d, want %d (raw=%d + envelope=%d)", got, want, raw, defaultMessageEnvelopeTokens)
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
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-61 * time.Minute)}, // REAL-TIME: needed for message timestamp in test
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
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-5 * time.Minute)}, // REAL-TIME: needed for message timestamp in test
	}
	result := EvaluateTimeBasedTrigger(messages, QuerySourceReplMainThread)
	if result != nil {
		t.Error("evaluateTimeBasedTrigger should return nil when gap < threshold")
	}
}

func TestEvaluateTimeBasedTrigger_OverThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

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
	})
}

func TestEvaluateTimeBasedTrigger_WrongSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
	})
}

func TestEvaluateTimeBasedTrigger_PrefixSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

		messages := []types.Message{
			{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute)},
		}
		result := EvaluateTimeBasedTrigger(messages, "repl_main_thread:outputStyle:custom")
		if result == nil {
			t.Error("evaluateTimeBasedTrigger should fire for prefix-matched source")
		}
	})
}

// ---------------------------------------------------------------------------
// maybeTimeBasedMicrocompact
// ---------------------------------------------------------------------------

func TestMaybeTimeBasedMicrocompact_NoTrigger(t *testing.T) {
	// Gap under threshold → no trigger
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-5 * time.Minute)}, // REAL-TIME: needed for message timestamp in test
	}
	result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
	if result != nil {
		t.Error("maybeTimeBasedMicrocompact should return nil when no trigger")
	}
}

func TestMaybeTimeBasedMicrocompact_ClearsOldResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

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
	})
}

func TestMaybeTimeBasedMicrocompact_KeepsMinOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

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
	})
}

func TestMaybeTimeBasedMicrocompact_NothingToClear(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

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
	})
}

func TestMaybeTimeBasedMicrocompact_AlreadyCleared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

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
	})
}

// ---------------------------------------------------------------------------
// MicrocompactMessages
// ---------------------------------------------------------------------------

func TestMicrocompactMessages_TimeBasedFires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

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
	})
}

func TestMicrocompactMessages_TimeBasedSkips(t *testing.T) {
	// Recent message, no trigger
	messages := []types.Message{
		{Role: types.RoleAssistant, Timestamp: time.Now().Add(-5 * time.Minute)}, // REAL-TIME: needed for message timestamp in test
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
// Logging integration
// ---------------------------------------------------------------------------

func TestMaybeTimeBasedMicrocompact_Logs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

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
				types.NewToolResultBlock("t1", json.RawMessage(`"data content one"`), false),
				types.NewToolResultBlock("t2", json.RawMessage(`"data content two"`), false),
			}},
		}

		maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, logger)
		if !strings.Contains(buf.String(), "engine:time_based_mc") {
			t.Errorf("expected engine:time_based_mc in log output, got: %s", buf.String())
		}
	})
}

// ---------------------------------------------------------------------------
// NotifyCacheDeletion integration
// ---------------------------------------------------------------------------

func TestMaybeTimeBasedMicrocompact_CallsNotifyCacheDeletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

		origCfg := defaultMicrocompactConfig
		defer func() { defaultMicrocompactConfig = origCfg }()
		defaultMicrocompactConfig.TimeBased.KeepRecent = 0

		messages := []types.Message{
			{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
				types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
				types.NewToolUseBlock("t2", "Read", json.RawMessage(`{}`)),
			}},
			{Role: types.RoleUser, Content: []types.ContentBlock{
				types.NewToolResultBlock("t1", json.RawMessage(`"data content one"`), false),
				types.NewToolResultBlock("t2", json.RawMessage(`"data content two"`), false),
			}},
		}

		// This should not panic — NotifyCacheDeletion is a real function
		result := maybeTimeBasedMicrocompact(messages, QuerySourceReplMainThread, nil)
		if result == nil {
			t.Fatal("expected non-nil result")
		}
	})
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
	raw := types.EstimateTokens("user text") +
		types.EstimateTokens("tool output") +
		types.EstimateTokens("thinking text") +
		types.EstimateTokens("assistant text") +
		types.EstimateTokens("Read"+`{"path":"/x"}`)
	want := raw + 2*defaultMessageEnvelopeTokens
	if got != want {
		t.Errorf("EstimateMessagesTokens(multiple) = %d, want %d", got, want)
	}
}

func TestMaybeTimeBasedMicrocompact_PreservesMessageOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseTime := time.Now()

		origCfg := defaultMicrocompactConfig
		defer func() { defaultMicrocompactConfig = origCfg }()
		defaultMicrocompactConfig.TimeBased.KeepRecent = 0

		messages := []types.Message{
			{Role: types.RoleAssistant, Timestamp: baseTime.Add(-61 * time.Minute), Content: []types.ContentBlock{
				types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
				types.NewToolUseBlock("t2", "Bash", json.RawMessage(`{}`)),
			}},
			{Role: types.RoleUser, Content: []types.ContentBlock{
				types.NewToolResultBlock("t1", json.RawMessage(`"data content one"`), false),
				types.NewTextBlock("keep this text"),
				types.NewToolResultBlock("t2", json.RawMessage(`"bash output here"`), false),
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
		if string(result.Messages[1].Content[2].Content) != `"bash output here"` {
			t.Error("tool result t2 should be kept")
		}
	})
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
