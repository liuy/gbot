package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func newTestEngineForBreakdown(t *testing.T) *Engine {
	t.Helper()
	tmpDir := t.TempDir()
	// Create a minimal CLAUDE.md so estimateMemoryFiles finds something.
	if err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("create CLAUDE.md: %v", err)
	}
	eng := New(&Params{
		Model:       "claude-test",
		MaxTokens:   16000,
		TokenBudget: 200000,
		AutoCompact: AutoCompactConfig{ContextWindow: 200000},
		WorkingDir:  tmpDir,
		Logger:      slog.Default(),
		MCPRegistry: mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{}),
	})
	t.Cleanup(func() { eng.Close() })
	return eng
}

// TestContextBreakdown_EmptyWhenNoAPICall verifies the no-data guard.
func TestContextBreakdown_EmptyWhenNoAPICall(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	bd := e.ContextBreakdown()
	if bd == nil {
		t.Fatal("ContextBreakdown returned nil")
	}
	if bd.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0 (no API call yet)", bd.TotalTokens)
	}
	if len(bd.Categories) != 0 {
		t.Errorf("Categories = %d, want 0", len(bd.Categories))
	}
}

// TestContextBreakdown_ProportionalSumsToTotal verifies that after
// proportional scaling, all categories sum to totalExact.
func TestContextBreakdown_ProportionalSumsToTotal(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("You are a test assistant.")
	e.SetSkillListing("test-skill: a test")
	e.SetAgentDefs([]*types.AgentDefinition{
		{AgentType: "TestAgent", WhenToUse: "test use"},
	})
	e.tools["Bash"] = &stubTool{
		name:   "Bash",
		prompt: "Run a shell command. Args: command.",
	}
	e.tools["Read"] = &stubTool{
		name:   "Read",
		prompt: "Read a file. Args: path.",
	}
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hello world"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hi there, how can I help?"},
		}},
	}
	const totalExact = 50_000
	e.ContextTokens = totalExact

	bd := e.ContextBreakdown()
	if bd.TotalTokens != totalExact {
		t.Errorf("TotalTokens = %d, want %d", bd.TotalTokens, totalExact)
	}

	// Categories sum to context window (content + reserved + free).
	sum := 0
	for _, c := range bd.Categories {
		sum += c.Tokens
	}
	window := e.autoCompactConfig.ContextWindow
	if sum != window {
		t.Errorf("sum of categories = %d, want %d (diff=%d)", sum, window, window-sum)
	}

	for _, c := range bd.Categories {
		if c.Tokens > 0 && c.Percentage <= 0 {
			t.Errorf("category %q has tokens=%d but percentage=%.2f", c.Name, c.Tokens, c.Percentage)
		}
	}
}

// TestContextBreakdown_GridDimensions verifies the grid has the right size.
func TestContextBreakdown_GridDimensions(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("Test system prompt.")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "shell"}
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hi"},
		}},
	}
	e.ContextTokens = 100_000

	bd := e.ContextBreakdown()
	if len(bd.GridRows) != GridHeight {
		t.Errorf("GridRows = %d, want %d", len(bd.GridRows), GridHeight)
	}
	for i, row := range bd.GridRows {
		if len(row) != GridWidth {
			t.Errorf("row %d width = %d, want %d", i, len(row), GridWidth)
		}
	}
}

// TestContextBreakdown_ReservedAtEnd verifies autocompact squares are last.
func TestContextBreakdown_ReservedAtEnd(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.ContextTokens = 200_000
	bd := e.ContextBreakdown()
	if len(bd.GridRows) == 0 {
		t.Fatal("no grid rows")
	}
	lastReservedIdx := -1
	for i, row := range bd.GridRows {
		for j, sq := range row {
			if sq.CategoryName == "Autocompact buffer" {
				idx := i*GridWidth + j
				if idx > lastReservedIdx {
					lastReservedIdx = idx
				}
			}
		}
	}
	for i, row := range bd.GridRows {
		for j, sq := range row {
			if sq.CategoryName != "Autocompact buffer" && sq.CategoryName != "Free space" {
				idx := i*GridWidth + j
				if lastReservedIdx >= 0 && idx > lastReservedIdx {
					t.Errorf("non-reserved square at idx %d appears after reserved at %d", idx, lastReservedIdx)
				}
			}
		}
	}
}

// TestScaleProportionally_BasicMath verifies the scaling math.
func TestScaleProportionally_BasicMath(t *testing.T) {
	est := componentEstimates{
		SystemPromptBase: 1000,
		PlatformInfo:     500,
		Messages:         2000,
	}
	out := scaleProportionally(est, 70_000)
	sum := 0
	for _, v := range out {
		sum += v
	}
	if sum != 70_000 {
		t.Errorf("scaled sum = %d, want 70000", sum)
	}
}

// TestScaleProportionally_ZeroSum verifies the guard when sumEstimate == 0.
func TestScaleProportionally_ZeroSum(t *testing.T) {
	est := componentEstimates{}
	out := scaleProportionally(est, 1000)
	for k, v := range out {
		if v != 0 {
			t.Errorf("expected zero for %s, got %d", k, v)
		}
	}
}

// TestScaleProportionally_RoundingReconciliation verifies the sum is preserved.
func TestScaleProportionally_RoundingReconciliation(t *testing.T) {
	est := componentEstimates{
		SystemPromptBase: 333,
		PlatformInfo:     333,
		Messages:         334,
	}
	target := 100
	out := scaleProportionally(est, target)
	sum := 0
	for _, v := range out {
		sum += v
	}
	if sum != target {
		t.Errorf("rounding reconciliation failed: sum=%d, want %d", sum, target)
	}
}

// TestContextBreakdown_FreeSpaceNotNegative verifies the free space guard.
func TestContextBreakdown_FreeSpaceNotNegative(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.ContextTokens = 50_000
	bd := e.ContextBreakdown()
	for _, c := range bd.Categories {
		if c.IsFree && c.Tokens < 0 {
			t.Errorf("Free space negative: %d", c.Tokens)
		}
	}
}

// TestContextBreakdown_MCPLoadedAndDeferred splits MCP tools correctly.
func TestContextBreakdown_MCPLoadedAndDeferred(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	reg.SetToolsForTest([]mcp.DiscoveredTool{
		{
			Name: "mcp__srv__always", OriginalName: "always",
			ServerName: "srv", Description: "always load",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			AlwaysLoad:  true,
		},
		{
			Name: "mcp__srv__ondemand", OriginalName: "ondemand",
			ServerName: "srv", Description: "load on demand",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			AlwaysLoad:  false,
		},
	})
	e.mcpRegistry = reg
	e.SetSystemPrompt("x")
	e.ContextTokens = 10_000
	bd := e.ContextBreakdown()
	if len(bd.MCPToolsLoaded)+len(bd.MCPToolsDeferred) == 0 {
		t.Error("expected MCP tool details; got none")
	}
}

// TestContextBreakdown_ThreadSafety runs ContextBreakdown from multiple
// goroutines to surface races.
func TestContextBreakdown_ThreadSafety(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hi"},
		}},
	}
	e.ContextTokens = 5_000

	done := make(chan struct{})
	for range 4 {
		go func() {
			for range 50 {
				_ = e.ContextBreakdown()
			}
			done <- struct{}{}
		}()
	}
	for range 4 {
		<-done
	}
}

// TestContextBreakdown_PercentagesSumTo100 verifies category percentages
// sum to 100% (within rounding).
func TestContextBreakdown_PercentagesSumTo100(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hi"},
		}},
	}
	e.ContextTokens = 8_000
	bd := e.ContextBreakdown()
	total := 0.0
	for _, c := range bd.Categories {
		total += c.Percentage
	}
	if math.Abs(total-100.0) > 0.1 {
		t.Errorf("percentages sum = %.4f, want 100.0", total)
	}
}

// TestContextBreakdown_MessageBreakdown verifies message breakdown fields.
func TestContextBreakdown_MessageBreakdown(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "user text"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, Content: json.RawMessage(`"file1\nfile2"`)},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Here you go."},
		}},
	}
	e.ContextTokens = 5_000
	bd := e.ContextBreakdown()
	if bd.MessageBreakdown == nil {
		t.Fatal("MessageBreakdown is nil")
	}
	mb := bd.MessageBreakdown
	if mb.ToolCallTokens == 0 {
		t.Error("ToolCallTokens should be > 0")
	}
	if mb.ToolResultTokens == 0 {
		t.Error("ToolResultTokens should be > 0")
	}
	if mb.UserTextTokens == 0 {
		t.Error("UserTextTokens should be > 0")
	}
	if mb.AssistantTextTokens == 0 {
		t.Error("AssistantTextTokens should be > 0")
	}
	found := false
	for _, tc := range mb.ToolCallsByType {
		if tc.Name == "Bash" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Bash tool should appear in ToolCallsByType")
	}
}

// TestComputeReservedTokens verifies reserved token formula.
func TestComputeReservedTokens(t *testing.T) {
	if got := computeReservedTokens(200_000, 16_000); got != 14_000 {
		t.Errorf("computeReservedTokens(200K) = %d, want 14000 (7%% of 200K)", got)
	}
	if got := computeReservedTokens(20_000, 16_000); got != 3_000 {
		t.Errorf("computeReservedTokens(20K) = %d, want 3000 (min)", got)
	}
	if got := computeReservedTokens(0, 0); got != 0 {
		t.Errorf("computeReservedTokens(0) = %d, want 0", got)
	}
}

// TestBuildGrid_ReservedCategoryHasSquares verifies reserved categories get grid squares.
func TestBuildGrid_ReservedCategoryHasSquares(t *testing.T) {
	cats := []ContextCategory{
		{Name: "Messages", Tokens: 80_000, Color: ColorMessages},
		{Name: "Free space", Tokens: 106_000, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 14_000, Color: ColorReserved, IsReserved: true},
	}
	rows := BuildGrid(cats, 200_000)

	var reservedCount int
	for _, row := range rows {
		for _, sq := range row {
			if sq.CategoryName == "Autocompact buffer" {
				reservedCount++
			}
		}
	}
	if reservedCount == 0 {
		t.Errorf("Autocompact buffer should have at least 1 grid square, got %d", reservedCount)
	}
}

// TestBuildGrid_FreeSpaceHasSquares verifies free space gets grid squares.
func TestBuildGrid_FreeSpaceHasSquares(t *testing.T) {
	cats := []ContextCategory{
		{Name: "Messages", Tokens: 50_000, Color: ColorMessages},
		{Name: "Free space", Tokens: 136_000, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 14_000, Color: ColorReserved, IsReserved: true},
	}
	rows := BuildGrid(cats, 200_000)

	var freeCount int
	for _, row := range rows {
		for _, sq := range row {
			if sq.CategoryName == "Free space" {
				freeCount++
			}
		}
	}
	if freeCount == 0 {
		t.Errorf("Free space should have at least 1 grid square, got %d", freeCount)
	}
}

// TestContextBreakdown_FreeSpaceNotZeroWhenContextHalfFull verifies free space
// has tokens when context usage is well below the window.
func TestContextBreakdown_FreeSpaceNotZeroWhenContextHalfFull(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hi"},
		}},
	}
	// Half of 200K window.
	e.ContextTokens = 100_000

	bd := e.ContextBreakdown()
	var freeCat *ContextCategory
	for i := range bd.Categories {
		if bd.Categories[i].IsFree {
			freeCat = &bd.Categories[i]
			break
		}
	}
	if freeCat == nil {
		t.Fatal("no Free space category in breakdown")
	}
	if freeCat.Tokens == 0 {
		t.Errorf("Free space tokens = 0, want > 0 (context only uses half the window)")
	}
}

// TestIsMCPToolName verifies MCP tool name detection.
func TestIsMCPToolName(t *testing.T) {
	if !isMCPToolName("mcp__srv__tool") {
		t.Error("expected mcp__srv__tool to be MCP")
	}
	if isMCPToolName("Bash") {
		t.Error("Bash should not be MCP")
	}
}

// TestDumpAPIRequest_SnapshotsState verifies DumpAPIRequest returns a non-nil
// dump with the correct engine state and messages.
func TestDumpAPIRequest_SnapshotsState(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("You are a test assistant.")
	e.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hello"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hi there"},
		}},
	})

	dump := e.DumpAPIRequest()
	if dump == nil {
		t.Fatal("DumpAPIRequest returned nil")
	}
	if dump.Model != "claude-test" {
		t.Errorf("Model = %q, want claude-test", dump.Model)
	}
	if dump.MaxTokens != 16000 {
		t.Errorf("MaxTokens = %d, want 16000", dump.MaxTokens)
	}
	if dump.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", dump.ContextWindow)
	}
	if dump.IsSubagent {
		t.Error("IsSubagent should be false")
	}
	// 2 original messages + 1 prepended context message = 3.
	if len(dump.Messages) != 3 {
		t.Errorf("Messages count = %d, want 3 (2 original + 1 context prepend)", len(dump.Messages))
	}
	if len(dump.SystemPrompt) == 0 {
		t.Error("SystemPrompt should not be empty")
	}

	// Verify messages include prepended context (CLAUDE.md).
	// The first message should be a user message with <system-reminder>.
	if dump.Messages[0].Role != types.RoleUser {
		t.Errorf("first message role = %q, want user (context prepend)", dump.Messages[0].Role)
	}
}

// ---------------------------------------------------------------------------
// upsertAttachment / upsertToolCall unit tests
// ---------------------------------------------------------------------------

func TestUpsertAttachment_NewEntry(t *testing.T) {
	t.Parallel()
	mb := &MessageBreakdown{}
	mb.upsertAttachment("file", 100)
	if len(mb.AttachmentsByType) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(mb.AttachmentsByType))
	}
	if mb.AttachmentsByType[0].Name != "file" {
		t.Errorf("name = %q, want %q", mb.AttachmentsByType[0].Name, "file")
	}
	if mb.AttachmentsByType[0].Tokens != 100 {
		t.Errorf("tokens = %d, want 100", mb.AttachmentsByType[0].Tokens)
	}
}

func TestUpsertAttachment_Accumulate(t *testing.T) {
	t.Parallel()
	mb := &MessageBreakdown{}
	mb.upsertAttachment("file", 100)
	mb.upsertAttachment("file", 50)
	if len(mb.AttachmentsByType) != 1 {
		t.Fatalf("expected 1 attachment (accumulated), got %d", len(mb.AttachmentsByType))
	}
	if mb.AttachmentsByType[0].Tokens != 150 {
		t.Errorf("tokens = %d, want 150 (accumulated)", mb.AttachmentsByType[0].Tokens)
	}
}

func TestUpsertToolCall_UpdateExisting(t *testing.T) {
	t.Parallel()
	mb := &MessageBreakdown{}
	mb.upsertToolCall("Bash", 100, 0)
	mb.upsertToolCall("Bash", 50, 0)
	if len(mb.ToolCallsByType) != 1 {
		t.Fatalf("expected 1 tool call entry, got %d", len(mb.ToolCallsByType))
	}
	if mb.ToolCallsByType[0].CallTokens != 150 {
		t.Errorf("CallTokens = %d, want 150", mb.ToolCallsByType[0].CallTokens)
	}
	if mb.ToolCallsByType[0].ResultTokens != 0 {
		t.Errorf("ResultTokens = %d, want 0", mb.ToolCallsByType[0].ResultTokens)
	}
}

// ---------------------------------------------------------------------------
// lastAPIUsage tests
// ---------------------------------------------------------------------------

func TestLastAPIUsage_EmptyMessages(t *testing.T) {
	t.Parallel()
	if got := lastAPIUsage(nil); got != nil {
		t.Error("expected nil for nil messages")
	}
	if got := lastAPIUsage([]types.Message{}); got != nil {
		t.Error("expected nil for empty messages")
	}
}

func TestLastAPIUsage_ZeroUsageSkipped(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Usage: &types.Usage{}},
	}
	if got := lastAPIUsage(msgs); got != nil {
		t.Error("expected nil when all usage fields are zero")
	}
}

func TestLastAPIUsage_ReturnsLast(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Usage: &types.Usage{InputTokens: 100, OutputTokens: 50}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("ok")}},
		{Role: types.RoleAssistant, Usage: &types.Usage{InputTokens: 200, OutputTokens: 80, CacheCreationInputTokens: 10}},
	}
	got := lastAPIUsage(msgs)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.InputTokens != 200 {
		t.Errorf("InputTokens = %d, want 200", got.InputTokens)
	}
	if got.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80", got.OutputTokens)
	}
	if got.CacheCreationInputTokens != 10 {
		t.Errorf("CacheCreationInputTokens = %d, want 10", got.CacheCreationInputTokens)
	}
}

func TestLastAPIUsage_NoAssistantMessages(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	if got := lastAPIUsage(msgs); got != nil {
		t.Error("expected nil when no assistant messages")
	}
}

// ---------------------------------------------------------------------------
// tokenCountForBlock tests
// ---------------------------------------------------------------------------

func TestTokenCountForBlock_Thinking(t *testing.T) {
	t.Parallel()
	block := types.ContentBlock{Type: types.ContentTypeThinking, Thinking: "I need to think about this carefully"}
	got := tokenCountForBlock(block)
	if got <= 0 {
		t.Errorf("expected positive token count for thinking block, got %d", got)
	}
}

func TestTokenCountForBlock_UnknownType(t *testing.T) {
	t.Parallel()
	block := types.ContentBlock{Type: "image", Text: "an image block"}
	got := tokenCountForBlock(block)
	if got <= 0 {
		t.Errorf("expected positive token count for unknown type via JSON marshal, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// safePct tests
// ---------------------------------------------------------------------------

func TestSafePct_ZeroWhole(t *testing.T) {
	t.Parallel()
	if got := safePct(50, 0); got != 0 {
		t.Errorf("safePct(50, 0) = %f, want 0", got)
	}
	if got := safePct(50, -1); got != 0 {
		t.Errorf("safePct(50, -1) = %f, want 0", got)
	}
}

func TestSafePct_Normal(t *testing.T) {
	t.Parallel()
	if got := safePct(50, 100); got != 50.0 {
		t.Errorf("safePct(50, 100) = %f, want 50.0", got)
	}
}

func TestSafePct_Fraction(t *testing.T) {
	t.Parallel()
	got := safePct(1, 3)
	if math.Abs(got-33.333333) > 0.01 {
		t.Errorf("safePct(1, 3) = %f, want ~33.33", got)
	}
}

// ---------------------------------------------------------------------------
// toolsClone tests
// ---------------------------------------------------------------------------

func TestToolsClone_Nil(t *testing.T) {
	t.Parallel()
	if got := toolsClone(nil); got != nil {
		t.Error("expected nil for nil input")
	}
}

func TestToolsClone_Copy(t *testing.T) {
	t.Parallel()
	original := map[string]tool.Tool{
		"Bash": &stubTool{name: "Bash"},
	}
	cloned := toolsClone(original)
	if len(cloned) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(cloned))
	}
	if _, ok := cloned["Bash"]; !ok {
		t.Error("cloned map missing Bash")
	}
	// Mutating clone should not affect original
	cloned["Read"] = &stubTool{name: "Read"}
	if _, ok := original["Read"]; ok {
		t.Error("original should not have Read after clone mutation")
	}
}

// ---------------------------------------------------------------------------
// estimateComponents — attachment path
// ---------------------------------------------------------------------------

func TestContextBreakdown_AttachmentInMessages(t *testing.T) {
	t.Parallel()
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.messages = []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "user text"},
			},
			Attachment: &types.Attachment{
				Type:   types.AttachmentTypeQueued,
				Prompt: "this is an attached file content that has some tokens",
			},
		},
	}
	e.ContextTokens = 10_000

	bd := e.ContextBreakdown()
	if bd.MessageBreakdown == nil {
		t.Fatal("MessageBreakdown is nil")
	}
	if bd.MessageBreakdown.AttachmentTokens == 0 {
		t.Error("AttachmentTokens should be > 0 when message has Attachment")
	}
	if len(bd.MessageBreakdown.AttachmentsByType) == 0 {
		t.Error("AttachmentsByType should have entries when message has Attachment")
	}
}

// ---------------------------------------------------------------------------
// estimateSystemTools — deferred + toolSearch paths
// Note: deferredStubTool is defined in toolsearch_test.go as a function returning tool.Tool.

func TestEstimateSystemTools_DeferredDiscovered(t *testing.T) {
	t.Parallel()
	ts := newToolSearchState()
	ts.DiscoverTools([]string{"ToolSearch"})
	tools := map[string]tool.Tool{
		"ToolSearch": deferredStubTool("ToolSearch"),
		"Bash":       &stubTool{name: "Bash", prompt: "shell"},
	}
	got := estimateSystemTools(tools, ts, nil)
	// Both tools: ToolSearch is deferred but discovered → included.
	bashTokens := estimateSingleTool(tools["Bash"])
	toolSearchTokens := estimateSingleTool(tools["ToolSearch"])
	wantTotal := bashTokens + toolSearchTokens
	if got != wantTotal {
		t.Errorf("expected Bash+ToolSearch tokens (%d), got %d", wantTotal, got)
	}
}

func TestEstimateSystemTools_DeferredNotDiscovered(t *testing.T) {
	t.Parallel()
	ts := newToolSearchState()
	tools := map[string]tool.Tool{
		"ToolSearch": deferredStubTool("ToolSearch"),
		"Bash":       &stubTool{name: "Bash", prompt: "shell"},
	}
	got := estimateSystemTools(tools, ts, nil)
	// ToolSearch is deferred+not discovered → skipped. Bash alone: ~1 token.
	// Verify by name: only Bash should be in the count.
	if got != estimateSingleTool(tools["Bash"]) {
		t.Errorf("expected only Bash tokens (%d), got %d (ToolSearch not skipped?)",
			estimateSingleTool(tools["Bash"]), got)
	}
}

func TestEstimateSystemTools_NilToolSearch(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"ToolSearch": deferredStubTool("ToolSearch"),
		"Bash":       &stubTool{name: "Bash", prompt: "shell"},
	}
	got := estimateSystemTools(tools, nil, nil)
	// With nil toolSearch, deferred ToolSearch is included.
	bashTokens := estimateSingleTool(tools["Bash"])
	toolSearchTokens := estimateSingleTool(tools["ToolSearch"])
	wantTotal := bashTokens + toolSearchTokens
	if got != wantTotal {
		t.Errorf("expected Bash+ToolSearch tokens (%d), got %d", wantTotal, got)
	}
}

// ---------------------------------------------------------------------------
// buildDetails — attachment + top-5 truncation
// ---------------------------------------------------------------------------

func TestContextBreakdown_BuildDetails_AttachmentBreakdown(t *testing.T) {
	t.Parallel()
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	// Create 6 different attachment types to test truncation at top-5
	e.messages = []types.Message{}
	for i := range 6 {
		e.messages = append(e.messages, types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "msg"},
			},
			Attachment: &types.Attachment{
				Type:   types.AttachmentTypeQueued,
				Prompt: fmt.Sprintf("attachment %d with some content", i),
			},
		})
	}
	e.ContextTokens = 20_000

	bd := e.ContextBreakdown()
	if bd.MessageBreakdown == nil {
		t.Fatal("MessageBreakdown is nil")
	}
	if len(bd.MessageBreakdown.AttachmentsByType) > 5 {
		t.Errorf("AttachmentsByType should be truncated to 5, got %d", len(bd.MessageBreakdown.AttachmentsByType))
	}
	if bd.MessageBreakdown.AttachmentTokens == 0 {
		t.Error("AttachmentTokens should be > 0")
	}
}

func TestContextBreakdown_BuildDetails_SkillsAndAgents(t *testing.T) {
	t.Parallel()
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "x"}
	e.SetSkillListing("- skill-a: do something\n- skill-b: do another thing\n")
	e.SetAgentDefs([]*types.AgentDefinition{
		{AgentType: "TestAgent", WhenToUse: "for testing"},
		{AgentType: "CodeReview", WhenToUse: "for reviewing"},
	})
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	e.ContextTokens = 10_000

	bd := e.ContextBreakdown()
	if len(bd.Skills) == 0 {
		t.Error("expected skill details")
	}
	if len(bd.Agents) == 0 {
		t.Error("expected agent details")
	}
}

func TestContextBreakdown_BuildDetails_MultipleToolCallsTruncation(t *testing.T) {
	t.Parallel()
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	// Create messages with 7 different tool call types to test top-5 truncation
	var msgs []types.Message
	for i := range 7 {
		msgs = append(msgs,
			types.Message{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					{Name: fmt.Sprintf("Tool%d", i), Type: types.ContentTypeToolUse, Input: json.RawMessage(`{}`)},
				},
			},
			types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolResult, Content: json.RawMessage(`"ok"`)},
				},
			},
		)
	}
	e.messages = msgs
	e.ContextTokens = 30_000

	bd := e.ContextBreakdown()
	if bd.MessageBreakdown == nil {
		t.Fatal("MessageBreakdown is nil")
	}
	if len(bd.MessageBreakdown.ToolCallsByType) > 5 {
		t.Errorf("ToolCallsByType should be truncated to 5, got %d", len(bd.MessageBreakdown.ToolCallsByType))
	}
}

func TestEstimateSystemPromptSections_UsesRawSystemPrompt(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	raw := strings.Repeat("word ", 500) // ~500 tokens
	sections := estimateSystemPromptSections(raw, tmpDir, "", nil, "")
	if sections.base < 400 {
		t.Errorf("base tokens = %d, want >= 400 (raw systemPrompt has ~500 tokens)", sections.base)
	}
}
