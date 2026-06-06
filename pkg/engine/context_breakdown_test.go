package engine

import (
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
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
	return New(&Params{
		Model:       "claude-test",
		MaxTokens:   16000,
		TokenBudget: 200000,
		AutoCompact: AutoCompactConfig{ContextWindow: 200000},
		WorkingDir:  tmpDir,
		Logger:      slog.Default(),
		MCPRegistry: mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{}),
		Tools:       []tool.Tool{},
	})
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
	e.SetSystemPrompt(json.RawMessage(`"You are a test assistant."`))
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
	e.SetSystemPrompt(json.RawMessage(`"Test system prompt."`))
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
	e.SetSystemPrompt(json.RawMessage(`"x"`))
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
	e.SetSystemPrompt(json.RawMessage(`"x"`))
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
	e.SetSystemPrompt(json.RawMessage(`"x"`))
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
	e.SetSystemPrompt(json.RawMessage(`"x"`))
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
	e.SetSystemPrompt(json.RawMessage(`"x"`))
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
	e.SetSystemPrompt(json.RawMessage(`"x"`))
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
	e.SetSystemPrompt(json.RawMessage(`"x"`))
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
	e.SetSystemPrompt(json.RawMessage(`"You are a test assistant."`))
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
