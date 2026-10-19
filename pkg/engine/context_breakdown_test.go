package engine

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/long"
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
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	eng := New(&Params{
		Model:       "claude-test",
		MaxTokens:   16000,
		TokenBudget: 200000,
		AutoCompact: AutoCompactConfig{ContextWindow: 200000},
		WorkingDir:  tmpDir,
		Logger:      slog.Default(),
		MCPRegistry: reg,
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
	t.Cleanup(func() { _ = reg.Close() })
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

// TestContextBreakdown_CategoryIDs verifies every emitted category carries a
// stable semantic ID for wui-side i18n. Every entry in the fixture's expected
// set must actually emit, otherwise its ID mapping goes untested.
func TestContextBreakdown_CategoryIDs(t *testing.T) {
	memDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(memDir, "notes.md"), []byte("some durable memory notes"), 0o644); err != nil {
		t.Fatalf("create memory file: %v", err)
	}
	e := newTestEngineForBreakdown(t)
	e.SetMemoryDir(memDir)
	e.SetSystemPrompt("You are a test assistant.")
	e.SetSkillListing("test-skill: a test")
	e.SetAgentDefs([]*types.AgentDefinition{
		{AgentType: "TestAgent", WhenToUse: "test use"},
	})
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	reg.SetToolsForTest([]mcp.DiscoveredTool{
		{
			Name: "mcp__srv__always", OriginalName: "always",
			ServerName: "srv", Description: "always load",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			AlwaysLoad:  true,
		},
	})
	e.mcpRegistry = reg
	// stubTool feeds "Tool prompts" (non-empty Prompt); testTool feeds
	// "System tools" (stubTool's empty Description estimates to 0 tokens).
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "Run a shell command."}
	e.tools["Read"] = &testTool{name: "Read"}
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hello world"},
		}},
	}
	e.ContextTokens = 50_000

	want := map[string]string{
		"System prompt":      "system_prompt",
		"Platform info":      "platform_info",
		"Tool prompts":       "tool_prompts",
		"Skill listing":      "skill_listing",
		"Memory files":       "memory_files",
		"System tools":       "system_tools",
		"MCP tools":          "mcp_tools",
		"Custom agents":      "custom_agents",
		"Messages":           "messages",
		"Free space":         "free_space",
		"Autocompact buffer": "autocompact_buffer",
	}

	bd := e.ContextBreakdown()
	emitted := map[string]bool{}
	for _, c := range bd.Categories {
		id, ok := want[c.Name]
		if !ok {
			t.Fatalf("unexpected category name %q", c.Name)
		}
		if c.ID != id {
			t.Errorf("category %q ID = %q, want %q", c.Name, c.ID, id)
		}
		emitted[c.Name] = true
	}
	for name := range want {
		if !emitted[name] {
			t.Errorf("category %q not emitted; its ID mapping is untested", name)
		}
	}
}

// TestContextBreakdown_SystemPromptSectionIDs verifies every emitted system
// prompt section carries a stable semantic ID for wui-side i18n. Sections with
// zero tokens are dropped, so every entry in the fixture's expected set must
// emit for its ID mapping to be covered.
func TestContextBreakdown_SystemPromptSectionIDs(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("You are a test assistant.")
	e.SetSkillListing("test-skill: a test")
	e.tools["Bash"] = &stubTool{name: "Bash", prompt: "Run a shell command."}
	e.ContextTokens = 50_000

	want := map[string]string{
		"Base prompt":   "base_prompt",
		"Platform info": "platform_info",
		"Tool prompts":  "tool_prompts",
		"Skill listing": "skill_listing",
	}

	bd := e.ContextBreakdown()
	if len(bd.SystemPromptSections) == 0 {
		t.Fatal("SystemPromptSections is empty")
	}
	emitted := map[string]bool{}
	for _, s := range bd.SystemPromptSections {
		id, ok := want[s.Name]
		if !ok {
			t.Fatalf("unexpected section name %q", s.Name)
		}
		if s.ID == "" {
			t.Errorf("section %q has empty ID", s.Name)
		}
		if s.ID != id {
			t.Errorf("section %q ID = %q, want %q", s.Name, s.ID, id)
		}
		emitted[s.Name] = true
	}
	for name := range want {
		if !emitted[name] {
			t.Errorf("section %q not emitted; its ID mapping is untested", name)
		}
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

func TestTokenCountForBlock_Document(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// est wins over the size-based fallback and over the tiny JSON branch.
	est := e.tokenCountForBlock(types.NewDocumentBlock("a.pdf", "/x/a.pdf", "application/pdf", 2<<20, 5000))
	if est != 5000 {
		t.Errorf("document est-tokens branch = %d, want 5000", est)
	}
	fallback := e.tokenCountForBlock(types.NewDocumentBlock("a.bin", "/x/a.bin", "application/octet-stream", 400, 0))
	if fallback != 100 {
		t.Errorf("document size/4 fallback = %d, want 100", fallback)
	}
}

func TestTokenCountForBlock_Thinking(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	block := types.ContentBlock{Type: types.ContentTypeThinking, Thinking: "I need to think about this carefully"}
	got := e.tokenCountForBlock(block)
	// Seed-ratio 6-feature estimate for "I need to think about this carefully":
	// 36 runes → Other 36, Words 7 (no CJK/struct/digit/indent);
	// 7×0.48 + 36×0.11 = 7.32 → 7.
	if got != 7 {
		t.Errorf("thinking block token count = %d, want 7", got)
	}
}

func TestTokenCountForBlock_UnknownType(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	block := types.ContentBlock{Type: "image", Text: "an image block"}
	got := e.tokenCountForBlock(block)
	// Marshal yields {"type":"image","text":"an image block"} — 40 runes:
	// Struct 12 (quotes/braces/colons; ',' is not a struct char), Other 28
	// (25 letters + 2 spaces + comma), Words 3.
	// 12×1.38 + 3×0.48 + 28×0.11 = 21.08 → 21.
	if got != 21 {
		t.Errorf("unknown-type token count = %d, want 21", got)
	}
}

// ---------------------------------------------------------------------------
// safePct tests
// ---------------------------------------------------------------------------

func TestSafePct_ZeroWhole(t *testing.T) {
	t.Parallel()
	// safePct guards the divide: a zero or negative whole yields exactly 0.
	want := 0.0
	if got := safePct(50, 0); got != want {
		t.Errorf("safePct(50, 0) = %f, want %f", got, want)
	}
	if got := safePct(50, -1); got != want {
		t.Errorf("safePct(50, -1) = %f, want %f", got, want)
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
	e := &Engine{}
	ts := newToolSearchState()
	ts.DiscoverTools([]string{"ToolSearch"})
	tools := map[string]tool.Tool{
		"ToolSearch": deferredStubTool("ToolSearch"),
		"Bash":       &stubTool{name: "Bash", prompt: "shell"},
	}
	got := e.estimateSystemTools(tools, ts, nil)
	// Both tools: ToolSearch is deferred but discovered → included.
	bashTokens := e.estimateSingleTool(tools["Bash"])
	toolSearchTokens := e.estimateSingleTool(tools["ToolSearch"])
	wantTotal := bashTokens + toolSearchTokens
	if got != wantTotal {
		t.Errorf("expected Bash+ToolSearch tokens (%d), got %d", wantTotal, got)
	}
}

func TestEstimateSystemTools_DeferredNotDiscovered(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	ts := newToolSearchState()
	tools := map[string]tool.Tool{
		"ToolSearch": deferredStubTool("ToolSearch"),
		"Bash":       &stubTool{name: "Bash", prompt: "shell"},
	}
	got := e.estimateSystemTools(tools, ts, nil)
	// ToolSearch is deferred+not discovered → skipped. Bash alone: ~1 token.
	// Verify by name: only Bash should be in the count.
	if got != e.estimateSingleTool(tools["Bash"]) {
		t.Errorf("expected only Bash tokens (%d), got %d (ToolSearch not skipped?)",
			e.estimateSingleTool(tools["Bash"]), got)
	}
}

func TestEstimateSystemTools_NilToolSearch(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	tools := map[string]tool.Tool{
		"ToolSearch": deferredStubTool("ToolSearch"),
		"Bash":       &stubTool{name: "Bash", prompt: "shell"},
	}
	got := e.estimateSystemTools(tools, nil, nil)
	// With nil toolSearch, deferred ToolSearch is included.
	bashTokens := e.estimateSingleTool(tools["Bash"])
	toolSearchTokens := e.estimateSingleTool(tools["ToolSearch"])
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
	sections := (&Engine{}).estimateSystemPromptSections(raw, tmpDir, "", nil, "")
	if sections.base < 400 {
		t.Errorf("base tokens = %d, want >= 400 (raw systemPrompt has ~500 tokens)", sections.base)
	}
}

// TestContextBreakdown_ScaledCategoriesSumToTotalExact verifies that the
// scaled (non-free, non-reserved) categories sum to TotalTokens — the
// API-reported actual usage. Autocompact buffer (reserved) is part of
// free space, not part of used tokens.
func TestContextBreakdown_ScaledCategoriesSumToTotalExact(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.systemPrompt = strings.Repeat("word ", 1000)
	e.ContextTokens = 50_000

	bd := e.ContextBreakdown()
	if bd.TotalTokens != 50_000 {
		t.Fatalf("TotalTokens = %d, want 50000", bd.TotalTokens)
	}

	scaledSum := 0
	freeSum := 0
	for _, c := range bd.Categories {
		if c.IsFree || c.IsReserved {
			freeSum += c.Tokens
		} else {
			scaledSum += c.Tokens
		}
	}
	if scaledSum != bd.TotalTokens {
		t.Errorf("scaled categories sum = %d, want %d (TotalTokens = used). reserved is leaking into used.",
			scaledSum, bd.TotalTokens)
	}
	window := e.autoCompactConfig.ContextWindow
	if freeSum != window-bd.TotalTokens {
		t.Errorf("free+reserved sum = %d, want %d (contextWindow - TotalTokens)", freeSum, window-bd.TotalTokens)
	}
}

// TestMessageBreakdown_CompactSummaryNotUserText verifies that compact
// summary messages (RoleUser + FlagCompactSummary) are NOT classified as
// UserTextTokens — they are a separate category, not real user input.
// Also verifies image blocks go to AttachmentTokens, not UserTextTokens.
func TestMessageBreakdown_CompactSummaryNotUserText(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.messages = []types.Message{
		// Compact summary — should NOT count as user text
		{Role: types.RoleUser, Flags: types.FlagCompactSummary, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("summary content ", 1000)),
		}},
		// Image block — should count as attachment, not user text
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeImage},
		}},
		// Screenshot from Computer tool — RoleUser + FlagMeta, should NOT count as user text
		{Role: types.RoleUser, Flags: types.FlagMeta, Content: []types.ContentBlock{
			types.NewTextBlock(strings.Repeat("Screenshot captured. ", 500)),
			{Type: types.ContentTypeImage},
		}},
		// Real user input
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("hi"),
		}},
	}
	e.ContextTokens = 100_000

	bd := e.ContextBreakdown()
	if bd.MessageBreakdown == nil {
		t.Fatal("MessageBreakdown is nil")
	}
	if bd.MessageBreakdown.UserTextTokens > 10 {
		t.Errorf("UserTextTokens = %d, want <= 10 (compact summary + image should not count as user text)",
			bd.MessageBreakdown.UserTextTokens)
	}
	// FlagMeta messages (tool screenshots) should be classified as tool
	// result tokens, not silently dropped.
	if bd.MessageBreakdown.ToolResultTokens == 0 {
		t.Errorf("ToolResultTokens = 0, want > 0 (FlagMeta image should count as tool result)")
	}
}

// With a MEMORY.md index the system prompt injects the typed-memory prompt,
// not each memory file's contents — the breakdown must estimate what is
// actually injected.
func TestContextBreakdown_MemoryEstimatesActualInjection(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("You are a test assistant.")
	memDir := t.TempDir()
	// Two real memory files behind the index, each with substantial content.
	const filler = " substantial memory body content "
	for _, name := range []string{"mem_alpha.md", "mem_beta.md"} {
		body := strings.Repeat(filler, 100) // ~3.4 KB each
		if err := os.WriteFile(filepath.Join(memDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx := "# Memory\n\n- [Alpha](mem_alpha.md) — hook a\n- [Beta](mem_beta.md) — hook b\n"
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	e.memoryDir = memDir
	e.ContextTokens = 50_000

	bd := e.ContextBreakdown()

	if len(bd.MemoryFiles) != 1 {
		t.Fatalf("memory file details = %d, want 1 (the typed-memory prompt as one entry); paths: %v",
			len(bd.MemoryFiles), memoryDetailPaths(bd.MemoryFiles))
	}
	want := e.EstimateTokensCalibrated(context.FormatMemoryPrompt(e.workingDir, memDir))
	if got := bd.MemoryFiles[0].Tokens; got != want {
		t.Errorf("memory entry tokens = %d, want %d (EstimateTokensCalibrated of the injected prompt)", got, want)
	}
	if p := bd.MemoryFiles[0].Path; p != filepath.Join(memDir, "MEMORY.md") {
		t.Errorf("memory entry path = %q, want %q", p, filepath.Join(memDir, "MEMORY.md"))
	}
	for _, d := range bd.MemoryFiles {
		if strings.Contains(d.Path, "mem_alpha") || strings.Contains(d.Path, "mem_beta") {
			t.Errorf("memory detail lists full file %q — breakdown must not enumerate memory files that are not injected", d.Path)
		}
	}
}

func memoryDetailPaths(ds []MemoryFileDetail) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Path
	}
	return out
}

// ---------------------------------------------------------------------------
// Batch-B mutation sweep helpers.
// ---------------------------------------------------------------------------

// rleGrid flattens a grid into run-length-encoded cells so a whole 100-square
// grid can be asserted with one slice comparison. GridSquare is comparable, so
// a run continues only while every field (color, fullness, filled, name) is
// identical — the encoding therefore pins all four fields of every square.
func rleGrid(rows [][]GridSquare) []string {
	var out []string
	var cur GridSquare
	have := false
	n := 0
	flush := func() {
		if have {
			out = append(out, fmt.Sprintf("%s|%s|f%v|fill%v|%d", cur.CategoryName, cur.Color, cur.SquareFullness, cur.IsFilled, n))
		}
	}
	for _, row := range rows {
		for _, sq := range row {
			if have && sq == cur {
				n++
				continue
			}
			flush()
			cur, n, have = sq, 1, true
		}
	}
	flush()
	return append([]string{fmt.Sprintf("rows=%d", len(rows))}, out...)
}

// namedDisabledTool is an enabled-looking tool that reports IsEnabled false; its
// token estimate is non-zero so skipping it changes any total.
type namedDisabledTool struct{ name string }

func (t namedDisabledTool) Name() string                 { return t.name }
func (t namedDisabledTool) Aliases() []string            { return nil }
func (t namedDisabledTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t namedDisabledTool) Description(json.RawMessage) (string, error) {
	return t.name + " disabled tool description", nil
}
func (t namedDisabledTool) IsEnabled() bool                           { return false }
func (t namedDisabledTool) Prompt() string                            { return t.name + " prompt text here" }
func (t namedDisabledTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t namedDisabledTool) MaxResultSize() int                        { return 0 }
func (t namedDisabledTool) RenderResult(any) string                   { return "" }
func (t namedDisabledTool) Call(stdctx.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (t namedDisabledTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t namedDisabledTool) IsReadOnly(json.RawMessage) bool        { return false }
func (t namedDisabledTool) IsDestructive(json.RawMessage) bool     { return false }
func (t namedDisabledTool) IsConcurrencySafe(json.RawMessage) bool { return false }

func categoryByName(cats []ContextCategory, name string) (ContextCategory, bool) {
	for _, c := range cats {
		if c.Name == name {
			return c, true
		}
	}
	return ContextCategory{}, false
}

// ---------------------------------------------------------------------------
// Grid constants
// ---------------------------------------------------------------------------

func TestGridConstants(t *testing.T) {
	t.Parallel()
	if GridWidth != 20 {
		t.Errorf("GridWidth = %d, want 20", GridWidth)
	}
	if GridHeight != 5 {
		t.Errorf("GridHeight = %d, want 5", GridHeight)
	}
	// TotalSquares ignores its argument: the grid is always 20×5.
	if got := TotalSquares(0); got != 100 {
		t.Errorf("TotalSquares(0) = %d, want 100", got)
	}
	if got := TotalSquares(200_000); got != 100 {
		t.Errorf("TotalSquares(200000) = %d, want 100", got)
	}
}

// ---------------------------------------------------------------------------
// BuildGrid — one fixture per allocation path, asserted cell by cell.
// ---------------------------------------------------------------------------

func TestBuildGrid_Proportional(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 40_000, Color: "1"},
		{Name: "Beta", Tokens: 20_000, Color: "2"},
		{Name: "Free space", Tokens: 126_000, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 14_000, Color: ColorReserved, IsReserved: true},
	}
	// perSquare = 200000/100 = 2000 → reserved 7, Alpha 20, Beta 10, free 63.
	want := []string{
		"rows=5",
		"Alpha|1|f0.05|fillfalse|20",
		"Beta|2|f0.1|fillfalse|10",
		"Free space|" + ColorFree + "|f0|fillfalse|63",
		"Autocompact buffer|" + ColorReserved + "|f1|filltrue|7",
	}
	if got := rleGrid(BuildGrid(cats, 200_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_FreeFirst(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Free space", Tokens: 130_000, Color: ColorFree, IsFree: true},
		{Name: "Alpha", Tokens: 40_000, Color: "1"},
		{Name: "Beta", Tokens: 20_000, Color: "2"},
		{Name: "Autocompact buffer", Tokens: 10_000, Color: ColorReserved, IsReserved: true},
	}
	// The free category sits at index 0: phase 2 must skip it (freeIdx == 0
	// must still be treated as "found"), and phase 3 must give it the
	// remainder (65), not the 65 squares phase 2 would have assigned it.
	want := []string{
		"rows=5",
		"Free space|" + ColorFree + "|f0|fillfalse|65",
		"Alpha|1|f0.05|fillfalse|20",
		"Beta|2|f0.1|fillfalse|10",
		"Autocompact buffer|" + ColorReserved + "|f1|filltrue|5",
	}
	if got := rleGrid(BuildGrid(cats, 200_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_MinimumReservedSquare(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 89_900, Color: "1"},
		{Name: "Free space", Tokens: 10_000, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 100, Color: ColorReserved, IsReserved: true},
	}
	// perSquare = 1000 → reserved rounds to 0 squares but must keep one.
	want := []string{
		"rows=5",
		"Alpha|1|f0.011123470522803113|fillfalse|90",
		"Free space|" + ColorFree + "|f0|fillfalse|9",
		"Autocompact buffer|" + ColorReserved + "|f1|filltrue|1",
	}
	if got := rleGrid(BuildGrid(cats, 100_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_ZeroTokenReservedGetsNone(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 40_000, Color: "1"},
		{Name: "Beta", Tokens: 20_000, Color: "2"},
		{Name: "Free space", Tokens: 126_000, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 14_000, Color: ColorReserved, IsReserved: true},
		{Name: "Autocompact buffer 2", Tokens: 0, Color: "161", IsReserved: true},
	}
	// A reserved category with zero tokens must get zero squares — the
	// "never drop a category" bump is gated on Tokens > 0.
	want := []string{
		"rows=5",
		"Alpha|1|f0.05|fillfalse|20",
		"Beta|2|f0.1|fillfalse|10",
		"Free space|" + ColorFree + "|f0|fillfalse|63",
		"Autocompact buffer|" + ColorReserved + "|f1|filltrue|7",
	}
	if got := rleGrid(BuildGrid(cats, 200_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_TwoReserved(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 40_000, Color: "1"},
		{Name: "Free space", Tokens: 120_000, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 14_000, Color: ColorReserved, IsReserved: true},
		{Name: "Autocompact buffer 2", Tokens: 6_000, Color: "161", IsReserved: true},
	}
	// reservedSquares must accumulate across both reserved categories (7+3=10),
	// otherwise the free remainder is wrong.
	want := []string{
		"rows=5",
		"Alpha|1|f0.045|fillfalse|22",
		"Free space|" + ColorFree + "|f0|fillfalse|67",
		"Autocompact buffer|" + ColorReserved + "|f1|filltrue|8",
		"Autocompact buffer 2|161|f1|filltrue|3",
	}
	if got := rleGrid(BuildGrid(cats, 200_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_AllZeroTokens(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 0, Color: "1"},
		{Name: "Free space", Tokens: 0, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 0, Color: ColorReserved, IsReserved: true},
	}
	// perSquare == 0 → no content or reserved squares; free absorbs all 100.
	want := []string{"rows=5", "Free space|" + ColorFree + "|f0|fillfalse|100"}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_NoSquaresNoRows(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 0, Color: "1"},
		{Name: "Autocompact buffer", Tokens: 0, Color: ColorReserved, IsReserved: true},
	}
	// No squares at all and no free category to absorb the remainder: the row
	// loop breaks on the first row (start 0 >= len 0), so there are no rows.
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, []string{"rows=0"}) {
		t.Errorf("grid = %v, want [rows=0]", got)
	}
}

func TestBuildGrid_SmallWindowReservedOnly(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Autocompact buffer", Tokens: 50, Color: ColorReserved, IsReserved: true},
	}
	// perSquare = 0.5: the reserved phase must still run (perSquare > 0, not
	// perSquare > 1) and hand the whole grid to the buffer.
	want := []string{"rows=5", "Autocompact buffer|" + ColorReserved + "|f1|filltrue|100"}
	if got := rleGrid(BuildGrid(cats, 5_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_OverflowScaled(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 1_000, Color: "1"},
		{Name: "Beta", Tokens: 1, Color: "2"},
		{Name: "Gamma", Tokens: 1, Color: "3"},
		{Name: "Delta", Tokens: 1, Color: "4"},
		{Name: "Free space", Tokens: 0, Color: ColorFree, IsFree: true},
	}
	// perSquare = 10.03 → Alpha 100 + three bumped tiny categories = 103 >
	// contentCap 100 → the rescale pass brings Alpha down to 97.
	want := []string{
		"rows=5",
		"Alpha|1|f0.010029999999999999|fillfalse|97",
		"Beta|2|f1|filltrue|1",
		"Gamma|3|f1|filltrue|1",
		"Delta|4|f1|filltrue|1",
	}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_OverflowWithFree(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 1_000, Color: "1"},
		{Name: "Beta", Tokens: 1, Color: "2"},
		{Name: "Gamma", Tokens: 1, Color: "3"},
		{Name: "Eps", Tokens: 1, Color: "4"},
		{Name: "Free space", Tokens: 500, Color: ColorFree, IsFree: true},
	}
	// allTokens 1503 → perSquare 15.03 → Alpha 67 + three bumped = 70 ≤
	// contentCap 100, so free keeps the 30-square remainder.
	want := []string{
		"rows=5",
		"Alpha|1|f0.01503|fillfalse|67",
		"Beta|2|f1|filltrue|1",
		"Gamma|3|f1|filltrue|1",
		"Eps|4|f1|filltrue|1",
		"Free space|" + ColorFree + "|f0|fillfalse|30",
	}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_ZeroTokenContentInOverflow(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 1_000, Color: "1"},
		{Name: "Beta", Tokens: 1, Color: "2"},
		{Name: "Gamma", Tokens: 1, Color: "3"},
		{Name: "Delta", Tokens: 1, Color: "4"},
		{Name: "Zero", Tokens: 0, Color: "5"},
		{Name: "Free space", Tokens: 0, Color: ColorFree, IsFree: true},
	}
	// The rescale pass must not hand a square to a zero-token category: the
	// floor is 0 and the "never drop" bump is gated on Tokens > 0.
	want := []string{
		"rows=5",
		"Alpha|1|f0.010029999999999999|fillfalse|97",
		"Beta|2|f1|filltrue|1",
		"Gamma|3|f1|filltrue|1",
		"Delta|4|f1|filltrue|1",
	}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_TinyCategoriesKeepSquares(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{{Name: "Alpha", Tokens: 1_000, Color: "1"}}
	for i := range 60 {
		cats = append(cats, ContextCategory{Name: fmt.Sprintf("T%02d", i), Tokens: 1, Color: "9"})
	}
	cats = append(cats,
		ContextCategory{Name: "Autocompact buffer", Tokens: 1_590, Color: ColorReserved, IsReserved: true},
		ContextCategory{Name: "Free space", Tokens: 0, Color: ColorFree, IsFree: true},
	)
	rows := BuildGrid(cats, 1_000)
	// The rescale pass scales every assignment below 0.5 for the tiny
	// categories; each must still keep exactly one square.
	counts := map[string]int{}
	for _, row := range rows {
		for _, sq := range row {
			counts[sq.CategoryName]++
		}
	}
	for i := range 60 {
		name := fmt.Sprintf("T%02d", i)
		if counts[name] != 1 {
			t.Errorf("%s squares = %d, want 1", name, counts[name])
		}
	}
	if counts["Alpha"] != 16 {
		t.Errorf("Alpha squares = %d, want 16", counts["Alpha"])
	}
	if counts["Free space"] != 0 {
		t.Errorf("Free space squares = %d, want 0", counts["Free space"])
	}
	if counts["Autocompact buffer"] != 24 {
		t.Errorf("Autocompact buffer squares = %d, want 24", counts["Autocompact buffer"])
	}
}

func TestBuildGrid_PartialRowPadded(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 1, Color: "1"},
		{Name: "Beta", Tokens: 1, Color: "2"},
		{Name: "Gamma", Tokens: 1, Color: "3"},
	}
	// 99 squares (33 each) with no free category: the last row holds 19 real
	// squares plus one padded free square.
	want := []string{
		"rows=5",
		"Alpha|1|f0.03|fillfalse|33",
		"Beta|2|f0.03|fillfalse|33",
		"Gamma|3|f0.03|fillfalse|33",
		"Free space|" + ColorFree + "|f0|fillfalse|1",
	}
	if got := rleGrid(BuildGrid(cats, 100)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_TwoFreeCategories(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Free one", Tokens: 0, Color: "240", IsFree: true},
		{Name: "Free two", Tokens: 0, Color: "241", IsFree: true},
		{Name: "Alpha", Tokens: 1, Color: "1"},
		{Name: "Beta", Tokens: 1, Color: "2"},
		{Name: "Gamma", Tokens: 1, Color: "3"},
	}
	// freeIdx must be the FIRST free category; the leftover square goes to it.
	want := []string{
		"rows=5",
		"Free one|240|f0|fillfalse|1",
		"Alpha|1|f0.03|fillfalse|33",
		"Beta|2|f0.03|fillfalse|33",
		"Gamma|3|f0.03|fillfalse|33",
	}
	if got := rleGrid(BuildGrid(cats, 100)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_FullnessExactlyThreshold(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 1, Color: "1"},
		{Name: "Beta", Tokens: 69, Color: "2"},
	}
	// perSquare 0.7 → Alpha's ideal is 10/7 squares, so its fullness is
	// exactly 0.7 and the square counts as filled (>=, not >).
	want := []string{
		"rows=5",
		"Alpha|1|f0.7|filltrue|1",
		"Beta|2|f0.010144927536231883|fillfalse|99",
	}
	if got := rleGrid(BuildGrid(cats, 70)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_FullnessCappedToOne(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 994, Color: "1"},
		{Name: "Beta", Tokens: 6, Color: "2"},
	}
	// Beta's ideal is 0.6 squares → raw fullness 1.666… must be capped to 1.0.
	want := []string{
		"rows=5",
		"Alpha|1|f0.01006036217303823|fillfalse|99",
		"Beta|2|f1|filltrue|1",
	}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// buildCategories — full field-by-field snapshot of the emitted pie.
// ---------------------------------------------------------------------------

func categorySig(cats []ContextCategory) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, fmt.Sprintf("%s|%d|p%.4f|%s|free=%v|res=%v|%s",
			c.Name, c.Tokens, c.Percentage, c.Color, c.IsFree, c.IsReserved, c.ID))
	}
	return out
}

func TestBuildCategories_AllCategories(t *testing.T) {
	t.Parallel()
	scaled := scaledComponents{
		"System prompt":  1_000,
		"Platform info":  900,
		"Tool prompts":   800,
		"Skill listing":  700,
		"Memory files":   600,
		"System tools":   500,
		"MCP tools":      400,
		"Custom agents":  300,
		"Messages":       200,
		"Deferred tools": 100, // never emitted: not part of the scaled map in practice
	}
	got := categorySig(buildCategories(scaled, 1_000, 500, 10_000))
	want := []string{
		"System prompt|1000|p10.0000|" + ColorSystemPrompt + "|free=false|res=false|system_prompt",
		"Platform info|900|p9.0000|" + ColorPlatformInfo + "|free=false|res=false|platform_info",
		"Tool prompts|800|p8.0000|" + ColorToolPrompts + "|free=false|res=false|tool_prompts",
		"Skill listing|700|p7.0000|" + ColorSkillListing + "|free=false|res=false|skill_listing",
		"Memory files|600|p6.0000|" + ColorMemoryFiles + "|free=false|res=false|memory_files",
		"System tools|500|p5.0000|" + ColorSystemTools + "|free=false|res=false|system_tools",
		"MCP tools|400|p4.0000|" + ColorMCPLoaded + "|free=false|res=false|mcp_tools",
		"Deferred tools|100|p1.0000|" + ColorMCPDeferred + "|free=false|res=false|",
		"Custom agents|300|p3.0000|" + ColorAgents + "|free=false|res=false|custom_agents",
		"Messages|200|p2.0000|" + ColorMessages + "|free=false|res=false|messages",
		"Free space|1000|p10.0000|" + ColorFree + "|free=true|res=false|free_space",
		"Autocompact buffer|500|p5.0000|" + ColorReserved + "|free=false|res=true|autocompact_buffer",
	}
	if !slices.Equal(got, want) {
		t.Errorf("categories =\n  %v\nwant\n  %v", got, want)
	}
}

func TestBuildCategories_ZeroTokenCategoriesDropped(t *testing.T) {
	t.Parallel()
	scaled := scaledComponents{
		"System prompt": 0,
		"Messages":      0,
		"Custom agents": 7,
	}
	// Free space and the autocompact buffer are emitted even at zero tokens —
	// the drop rule only applies to ordinary (non-free, non-reserved) slices.
	got := categorySig(buildCategories(scaled, 0, 0, 100))
	want := []string{
		"Custom agents|7|p7.0000|" + ColorAgents + "|free=false|res=false|custom_agents",
		"Free space|0|p0.0000|" + ColorFree + "|free=true|res=false|free_space",
		"Autocompact buffer|0|p0.0000|" + ColorReserved + "|free=false|res=true|autocompact_buffer",
	}
	if !slices.Equal(got, want) {
		t.Errorf("categories =\n  %v\nwant\n  %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// scaleProportionally — exact scaled maps for every branch.
// ---------------------------------------------------------------------------

func scaledSig(out scaledComponents) []string {
	names := []string{
		"System prompt", "Platform info", "Tool prompts", "Skill listing",
		"Memory files", "System tools", "MCP tools", "Custom agents", "Messages",
	}
	sig := make([]string, 0, len(out)+1)
	for _, n := range names {
		v, ok := out[n]
		if !ok {
			sig = append(sig, n+"=MISSING")
			continue
		}
		sig = append(sig, fmt.Sprintf("%s=%d", n, v))
	}
	if len(out) != len(names) {
		sig = append(sig, fmt.Sprintf("len=%d", len(out)))
	}
	return sig
}

func TestScaleProportionally_ExactMap(t *testing.T) {
	t.Parallel()
	est := componentEstimates{
		SystemPromptBase: 1_000,
		PlatformInfo:     500,
		ToolPrompts:      250,
		Messages:         250,
	}
	got := scaledSig(scaleProportionally(est, 1_000))
	want := []string{
		"System prompt=500", "Platform info=250", "Tool prompts=125", "Skill listing=0",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=125",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

func TestScaleProportionally_NeverDropsNonZeroCategory(t *testing.T) {
	t.Parallel()
	est := componentEstimates{
		SystemPromptBase: 1_000,
		PlatformInfo:     1,
		Messages:         1_000,
	}
	// Platform info scales to 0 but must be raised to 1; the rounding
	// remainder (-1) is then charged to the largest category.
	got := scaledSig(scaleProportionally(est, 10))
	want := []string{
		"System prompt=4", "Platform info=1", "Tool prompts=0", "Skill listing=0",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=5",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

func TestScaleProportionally_LargestIsNotFirstCategory(t *testing.T) {
	t.Parallel()
	est := componentEstimates{
		SystemPromptBase: 0,
		PlatformInfo:     1,
		ToolPrompts:      1,
		SkillListing:     1,
	}
	// The remainder must land on the first category holding the maximum
	// estimate (Platform info), not on estimates[0].
	got := scaledSig(scaleProportionally(est, 10))
	want := []string{
		"System prompt=0", "Platform info=4", "Tool prompts=3", "Skill listing=3",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

func TestScaleProportionally_TieGoesToFirstMax(t *testing.T) {
	t.Parallel()
	est := componentEstimates{
		SystemPromptBase: 0,
		PlatformInfo:     4,
		ToolPrompts:      4,
	}
	// Ties resolve to the first maximum: >, not >=.
	got := scaledSig(scaleProportionally(est, 10))
	want := []string{
		"System prompt=0", "Platform info=5", "Tool prompts=5", "Skill listing=0",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

func TestScaleProportionally_ZeroTargetKeepsRawEstimates(t *testing.T) {
	t.Parallel()
	est := componentEstimates{SystemPromptBase: 100, Messages: 200}
	got := scaledSig(scaleProportionally(est, 0))
	want := []string{
		"System prompt=100", "Platform info=0", "Tool prompts=0", "Skill listing=0",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=200",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

func TestScaleProportionally_NegativeTargetKeepsRawEstimates(t *testing.T) {
	t.Parallel()
	est := componentEstimates{SystemPromptBase: 100, Messages: 200}
	got := scaledSig(scaleProportionally(est, -5))
	want := []string{
		"System prompt=100", "Platform info=0", "Tool prompts=0", "Skill listing=0",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=200",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

func TestScaleProportionally_TargetOneScales(t *testing.T) {
	t.Parallel()
	est := componentEstimates{SystemPromptBase: 100, Messages: 200}
	// target 1 is a real scaling target, not the raw-estimate fallback.
	got := scaledSig(scaleProportionally(est, 1))
	want := []string{
		"System prompt=1", "Platform info=0", "Tool prompts=0", "Skill listing=0",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

func TestScaleProportionally_ZeroSumEstimateReturnsAllKeys(t *testing.T) {
	t.Parallel()
	out := scaleProportionally(componentEstimates{}, 1_000)
	if out == nil {
		t.Fatal("scaleProportionally returned nil for a zero-sum estimate")
	}
	got := scaledSig(out)
	want := []string{
		"System prompt=0", "Platform info=0", "Tool prompts=0", "Skill listing=0",
		"Memory files=0", "System tools=0", "MCP tools=0", "Custom agents=0", "Messages=0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("scaled = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// estimateComponents — accumulation, role filter, attachment path.
// ---------------------------------------------------------------------------

func TestEstimateComponents_Exact(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	const (
		att1     = "first attached file content"
		att2     = "second attached file content"
		userText = "user text body"
		toolIn   = `{"command":"ls -la"}`
		toolOut  = `"file1 file2 file3"`
		user2    = "second user text"
		sysText  = "system role message must be skipped"
	)
	est := e.estimateComponents(
		sysPromptSections{base: 10, platform: 20, toolPrompts: 30, skill: 40},
		[]context.MemoryFile{
			{Path: "/m/one.md", Content: "memory one content"},
			{Path: "/m/two.md", Content: "memory two content"},
		},
		nil, nil, nil,
		[]types.Message{
			{Role: types.RoleUser,
				Content:    []types.ContentBlock{types.NewTextBlock(userText)},
				Attachment: &types.Attachment{Type: types.AttachmentTypeQueued, Prompt: att1}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, Name: "Bash", Input: json.RawMessage(toolIn)},
			}},
			{Role: types.RoleUser, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, Content: json.RawMessage(toolOut)},
			}},
			// A skipped role placed before the last message: dropping the
			// filter or breaking out of the loop both lose the tail.
			{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock(sysText)}},
			{Role: types.RoleUser,
				Content:    []types.ContentBlock{types.NewTextBlock(user2)},
				Attachment: &types.Attachment{Type: types.AttachmentTypeQueued, Prompt: att2}},
		},
		nil, 0,
	)

	if est.SystemPromptBase != 10 || est.PlatformInfo != 20 || est.ToolPrompts != 30 || est.SkillListing != 40 {
		t.Errorf("sections not copied: %+v", est)
	}
	wantMem := e.EstimateTokensCalibrated("memory one content") + e.EstimateTokensCalibrated("memory two content")
	if est.MemoryFiles != wantMem {
		t.Errorf("MemoryFiles = %d, want %d", est.MemoryFiles, wantMem)
	}

	a1 := e.EstimateTokensCalibrated(att1)
	a2 := e.EstimateTokensCalibrated(att2)
	u1 := e.EstimateTokensCalibrated(userText)
	u2 := e.EstimateTokensCalibrated(user2)
	call := e.EstimateTokensCalibrated("Bash" + toolIn)
	result := e.EstimateTokensCalibrated(toolOut)
	total := a1 + u1 + call + result + a2 + u2
	wantMessages := int(math.Ceil(float64(total) * 4.0 / 3.0))
	if est.Messages != wantMessages {
		t.Errorf("Messages = %d, want %d (raw total %d)", est.Messages, wantMessages, total)
	}
	mb := est.MessagesByCategory
	if mb == nil {
		t.Fatal("MessagesByCategory is nil")
	}
	if mb.AttachmentTokens != a1+a2 {
		t.Errorf("AttachmentTokens = %d, want %d", mb.AttachmentTokens, a1+a2)
	}
	if mb.UserTextTokens != u1+u2 {
		t.Errorf("UserTextTokens = %d, want %d", mb.UserTextTokens, u1+u2)
	}
	if mb.ToolCallTokens != call {
		t.Errorf("ToolCallTokens = %d, want %d", mb.ToolCallTokens, call)
	}
	if mb.ToolResultTokens != result {
		t.Errorf("ToolResultTokens = %d, want %d", mb.ToolResultTokens, result)
	}
	if mb.AssistantTextTokens != 0 {
		t.Errorf("AssistantTextTokens = %d, want 0", mb.AssistantTextTokens)
	}
	if want := []AttachmentByType{{Name: "file", Tokens: a1 + a2}}; !slices.Equal(mb.AttachmentsByType, want) {
		t.Errorf("AttachmentsByType = %v, want %v", mb.AttachmentsByType, want)
	}
	if want := []ToolCallByType{{Name: "Bash", CallTokens: call, ResultTokens: 0}}; !slices.Equal(mb.ToolCallsByType, want) {
		t.Errorf("ToolCallsByType = %v, want %v", mb.ToolCallsByType, want)
	}
}

// ---------------------------------------------------------------------------
// estimateSystemPromptSections
// ---------------------------------------------------------------------------

func TestEstimateSystemPromptSections_Exact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e := &Engine{}
	tools := map[string]tool.Tool{
		"Bash": &stubTool{name: "Bash", prompt: "Run a shell command. Args: command."},
		"Read": &stubTool{name: "Read", prompt: "Read a file from the filesystem."},
		"Grep": &stubTool{name: "Grep", prompt: "Search file contents with a regex."},
	}
	// Several disabled tools: a `break` where a `continue` belongs loses the
	// enabled entries for (almost) every map iteration order.
	for _, n := range []string{"OffA", "OffB", "OffC", "OffD", "OffE", "OffF", "OffG", "OffH"} {
		tools[n] = namedDisabledTool{name: n}
	}
	skillListing := "- alpha: first skill\n- beta: second skill"
	raw := "You are a test assistant with a long system prompt for estimation."
	got := e.estimateSystemPromptSections(raw, dir, skillListing, tools, "")

	wantBase := e.EstimateTokensCalibrated(raw)
	wantSkill := e.EstimateTokensCalibrated(skillListing)
	wantToolPrompts := 0
	for _, n := range []string{"Bash", "Read", "Grep"} {
		wantToolPrompts += e.EstimateTokensCalibrated(tools[n].Prompt())
	}
	if got.base != wantBase {
		t.Errorf("base = %d, want %d", got.base, wantBase)
	}
	if got.skill != wantSkill {
		t.Errorf("skill = %d, want %d", got.skill, wantSkill)
	}
	if got.toolPrompts != wantToolPrompts {
		t.Errorf("toolPrompts = %d, want %d", got.toolPrompts, wantToolPrompts)
	}
	builder := context.NewBuilder(dir)
	wantPlatform := e.EstimateTokensCalibrated(builder.RuntimeInfo())
	if got.platform != wantPlatform {
		t.Errorf("platform = %d, want %d", got.platform, wantPlatform)
	}
}

func TestEstimateSystemPromptSections_EmptyRawReturnsZero(t *testing.T) {
	t.Parallel()
	got := (&Engine{}).estimateSystemPromptSections("", t.TempDir(), "- skill: x", map[string]tool.Tool{
		"Bash": &stubTool{name: "Bash", prompt: "Run a shell command."},
	}, "")
	if got != (sysPromptSections{}) {
		t.Errorf("sections = %+v, want the zero value when the raw prompt is empty", got)
	}
}

func TestEstimateSystemPromptSections_SkillListingEmpty(t *testing.T) {
	t.Parallel()
	got := (&Engine{}).estimateSystemPromptSections("prompt text here", t.TempDir(), "", nil, "")
	if got.skill != 0 {
		t.Errorf("skill = %d, want 0 for an empty listing", got.skill)
	}
	if got.toolPrompts != 0 {
		t.Errorf("toolPrompts = %d, want 0 with no tools", got.toolPrompts)
	}
	if got.base == 0 {
		t.Error("base = 0, want a non-zero estimate for a non-empty prompt")
	}
}

// ---------------------------------------------------------------------------
// estimateSystemTools — skip rules must skip, not stop.
// ---------------------------------------------------------------------------

func TestEstimateSystemTools_SkipsDoNotStopTheLoop(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	tools := contextToolSet()
	ts := newToolSearchState() // nothing discovered → the deferred tools are skipped
	got := e.estimateSystemTools(tools, ts, nil)
	want := 0
	for _, n := range enabledToolNames {
		want += e.estimateSingleTool(tools[n])
	}
	if got != want {
		t.Errorf("estimateSystemTools = %d, want %d (only the enabled built-ins)", got, want)
	}
}

func TestEstimateSystemTools_DiscoveredDeferredCounts(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	tools := map[string]tool.Tool{
		"Bash":       &testTool{name: "Bash"},
		"ToolSearch": deferredStubTool("ToolSearch"),
	}
	ts := newToolSearchState()
	ts.DiscoverTools([]string{"ToolSearch"})
	got := e.estimateSystemTools(tools, ts, nil)
	want := e.estimateSingleTool(tools["Bash"]) + e.estimateSingleTool(tools["ToolSearch"])
	if got != want {
		t.Errorf("estimateSystemTools = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// MCP tool loading
// ---------------------------------------------------------------------------

func TestMCPToolLLMLoaded_Modes(t *testing.T) {
	t.Parallel()
	// Code mode never enters the request, even when the server marks the tool
	// always-load; direct mode always does, even without that flag.
	if !mcpToolLLMLoaded(mcp.DiscoveredTool{Mode: mcp.ModeDirect}) {
		t.Error("direct mode must load")
	}
	if mcpToolLLMLoaded(mcp.DiscoveredTool{Mode: mcp.ModeCode, AlwaysLoad: true}) {
		t.Error("code mode must not load even with AlwaysLoad")
	}
	if !mcpToolLLMLoaded(mcp.DiscoveredTool{AlwaysLoad: true}) {
		t.Error("default mode with AlwaysLoad must load")
	}
	if mcpToolLLMLoaded(mcp.DiscoveredTool{}) {
		t.Error("default mode without AlwaysLoad must not load")
	}
}

func TestEstimateMCPLoadedTools_NilRegistry(t *testing.T) {
	t.Parallel()
	want := 0
	if got := (&Engine{}).estimateMCPLoadedTools(nil); got != want {
		t.Errorf("estimateMCPLoadedTools(nil) = %d, want %d", got, want)
	}
}

func TestEstimateMCPLoadedTools_SumsLoadedTools(t *testing.T) {
	t.Parallel()
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	schemaA := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	schemaB := json.RawMessage(`{"type":"object","properties":{"b":{"type":"number"}}}`)
	reg.SetToolsForTest([]mcp.DiscoveredTool{
		{Name: "mcp__s__one", OriginalName: "one", ServerName: "s", Description: "first tool", InputSchema: schemaA, Mode: mcp.ModeDirect},
		{Name: "mcp__s__two", OriginalName: "two", ServerName: "s", Description: "second tool", InputSchema: schemaB, AlwaysLoad: true},
		{Name: "mcp__s__code", OriginalName: "code", ServerName: "s", Description: "code mode", InputSchema: schemaA, Mode: mcp.ModeCode, AlwaysLoad: true},
		{Name: "mcp__s__lazy", OriginalName: "lazy", ServerName: "s", Description: "not loaded", InputSchema: schemaB},
	})
	e := &Engine{}
	want := e.estimateMCPSchema("first tool", schemaA) + e.estimateMCPSchema("second tool", schemaB)
	if got := e.estimateMCPLoadedTools(reg); got != want {
		t.Errorf("estimateMCPLoadedTools = %d, want %d", got, want)
	}
}

func TestEstimateSingleTool_AddsSchema(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	tl := &testTool{name: "Bash"}
	desc, _ := tl.Description(nil)
	schema := string(tl.InputSchema())
	want := e.EstimateTokensCalibrated("Bash"+desc) + e.EstimateTokensCalibrated(schema)
	if got := e.estimateSingleTool(tl); got != want {
		t.Errorf("estimateSingleTool = %d, want %d", got, want)
	}
}

func TestEstimateMCPSchema_AddsSchema(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	want := e.EstimateTokensCalibrated("a tool description") + e.EstimateTokensCalibrated(string(schema))
	if got := e.estimateMCPSchema("a tool description", schema); got != want {
		t.Errorf("estimateMCPSchema = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// estimateAgents
// ---------------------------------------------------------------------------

func TestEstimateAgents_Empty(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	want := 0
	if got := e.estimateAgents(nil); got != want {
		t.Errorf("estimateAgents(nil) = %d, want %d", got, want)
	}
	if got := e.estimateAgents([]*types.AgentDefinition{}); got != want {
		t.Errorf("estimateAgents(empty) = %d, want %d", got, want)
	}
}

func TestEstimateAgents_IncludesSystemPrompt(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	defs := []*types.AgentDefinition{
		{AgentType: "Alpha", WhenToUse: "when alpha"},
		{AgentType: "Beta", WhenToUse: "when beta", SystemPrompt: func() string { return "beta system prompt body" }},
	}
	want := e.EstimateTokensCalibrated("Alpha when alpha") +
		e.EstimateTokensCalibrated("Beta when beta beta system prompt body")
	if got := e.estimateAgents(defs); got != want {
		t.Errorf("estimateAgents = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// tokenCountForBlock / classifyMessageBlock / upsertToolCall
// ---------------------------------------------------------------------------

func TestTokenCountForBlock_DocumentEstTokensOne(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// EstTokens == 1 must still win over the size/4 fallback.
	doc := types.NewDocumentBlock("a.pdf", "/x/a.pdf", "application/pdf", 4000, 1)
	if got := e.tokenCountForBlock(doc); got != 1 {
		t.Errorf("document with EstTokens=1 = %d, want 1", got)
	}
}

func TestClassifyMessageBlock_Exact(t *testing.T) {
	t.Parallel()
	assistant := types.Message{Role: types.RoleAssistant}
	user := types.Message{Role: types.RoleUser}

	mb := &MessageBreakdown{}
	classifyMessageBlock(assistant, types.ContentBlock{Type: types.ContentTypeToolUse, Name: "Bash"}, 10, mb)
	classifyMessageBlock(assistant, types.ContentBlock{Type: types.ContentTypeToolUse, Name: "Bash"}, 7, mb)
	classifyMessageBlock(assistant, types.ContentBlock{Type: types.ContentTypeToolUse, Name: "Read"}, 5, mb)
	if mb.ToolCallTokens != 22 {
		t.Errorf("ToolCallTokens = %d, want 22", mb.ToolCallTokens)
	}
	wantCalls := []ToolCallByType{{Name: "Bash", CallTokens: 17, ResultTokens: 0}, {Name: "Read", CallTokens: 5, ResultTokens: 0}}
	if !slices.Equal(mb.ToolCallsByType, wantCalls) {
		t.Errorf("ToolCallsByType = %v, want %v", mb.ToolCallsByType, wantCalls)
	}

	mb = &MessageBreakdown{}
	classifyMessageBlock(user, types.ContentBlock{Type: types.ContentTypeToolResult}, 12, mb)
	classifyMessageBlock(user, types.ContentBlock{Type: types.ContentTypeToolResult}, 5, mb)
	if mb.ToolResultTokens != 17 {
		t.Errorf("tool result ToolResultTokens = %d, want 17 (accumulated)", mb.ToolResultTokens)
	}

	mb = &MessageBreakdown{}
	classifyMessageBlock(types.Message{Role: types.RoleUser, Flags: types.FlagMeta}, types.NewTextBlock("x"), 9, mb)
	classifyMessageBlock(types.Message{Role: types.RoleUser, Flags: types.FlagMeta}, types.NewTextBlock("y"), 6, mb)
	if mb.ToolResultTokens != 15 || mb.UserTextTokens != 0 {
		t.Errorf("meta message: ToolResultTokens=%d UserTextTokens=%d, want 15/0", mb.ToolResultTokens, mb.UserTextTokens)
	}

	mb = &MessageBreakdown{}
	classifyMessageBlock(assistant, types.NewTextBlock("x"), 4, mb)
	classifyMessageBlock(assistant, types.NewTextBlock("y"), 6, mb)
	if mb.AssistantTextTokens != 10 {
		t.Errorf("AssistantTextTokens = %d, want 10 (accumulated)", mb.AssistantTextTokens)
	}

	mb = &MessageBreakdown{}
	classifyMessageBlock(user, types.ContentBlock{Type: types.ContentTypeImage}, 6, mb)
	classifyMessageBlock(user, types.ContentBlock{Type: types.ContentTypeVideo}, 3, mb)
	if mb.AttachmentTokens != 9 {
		t.Errorf("AttachmentTokens = %d, want 9 (image+video)", mb.AttachmentTokens)
	}
	if mb.UserTextTokens != 0 {
		t.Errorf("UserTextTokens = %d, want 0 for media blocks", mb.UserTextTokens)
	}

	mb = &MessageBreakdown{}
	classifyMessageBlock(user, types.NewTextBlock("x"), 8, mb)
	if mb.UserTextTokens != 8 {
		t.Errorf("UserTextTokens = %d, want 8", mb.UserTextTokens)
	}

	mb = &MessageBreakdown{}
	classifyMessageBlock(types.Message{Role: types.RoleUser, Flags: types.FlagCompactSummary}, types.NewTextBlock("x"), 11, mb)
	if mb.ToolCallTokens != 0 || mb.ToolResultTokens != 0 || mb.AttachmentTokens != 0 ||
		mb.AssistantTextTokens != 0 || mb.UserTextTokens != 0 ||
		len(mb.ToolCallsByType) != 0 || len(mb.AttachmentsByType) != 0 {
		t.Errorf("compact summary classified as %+v, want nothing counted", mb)
	}
}

func TestUpsertToolCall_AccumulatesBothFields(t *testing.T) {
	t.Parallel()
	mb := &MessageBreakdown{}
	mb.upsertToolCall("Bash", 10, 3)
	mb.upsertToolCall("Bash", 5, 4)
	if len(mb.ToolCallsByType) != 1 {
		t.Fatalf("ToolCallsByType len = %d, want 1", len(mb.ToolCallsByType))
	}
	want := ToolCallByType{Name: "Bash", CallTokens: 15, ResultTokens: 7}
	if mb.ToolCallsByType[0] != want {
		t.Errorf("ToolCallsByType[0] = %+v, want %+v", mb.ToolCallsByType[0], want)
	}
}

func TestUpsertToolCall_NewEntryFields(t *testing.T) {
	t.Parallel()
	mb := &MessageBreakdown{}
	mb.upsertToolCall("Read", 12, 34)
	want := []ToolCallByType{{Name: "Read", CallTokens: 12, ResultTokens: 34}}
	if !slices.Equal(mb.ToolCallsByType, want) {
		t.Errorf("ToolCallsByType = %v, want %v", mb.ToolCallsByType, want)
	}
}

// ---------------------------------------------------------------------------
// buildDetails — per-item detail slices.
// ---------------------------------------------------------------------------

func TestBuildDetails_SystemPromptSections(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	ds := e.buildDetails(
		sysPromptSections{base: 5, platform: 0, toolPrompts: 3, skill: 1},
		nil, nil, nil, "", nil, nil, nil,
	)
	want := []SystemPromptSectionDetail{
		{Name: "Base prompt", ID: "base_prompt", Tokens: 5},
		{Name: "Tool prompts", ID: "tool_prompts", Tokens: 3},
		{Name: "Skill listing", ID: "skill_listing", Tokens: 1},
	}
	if !slices.Equal(ds.systemPromptSections, want) {
		t.Errorf("systemPromptSections = %v, want %v", ds.systemPromptSections, want)
	}
}

func TestBuildDetails_MemoryFiles(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	ds := e.buildDetails(sysPromptSections{}, []context.MemoryFile{
		{Path: "/m/one.md", Content: "memory one content"},
		{Path: "/m/two.md", Content: "memory two content"},
	}, nil, nil, "", nil, nil, nil)
	want := []MemoryFileDetail{
		{Path: "/m/one.md", Tokens: e.EstimateTokensCalibrated("memory one content")},
		{Path: "/m/two.md", Tokens: e.EstimateTokensCalibrated("memory two content")},
	}
	if !slices.Equal(ds.memoryFiles, want) {
		t.Errorf("memoryFiles = %v, want %v", ds.memoryFiles, want)
	}
}

// contextToolSet builds a tool map with several enabled built-ins plus several
// tools that must be skipped for different reasons. Repeating each skip reason
// makes an early `break` observable for (almost) every map iteration order:
// the mutant only survives if every skip-worthy tool happens to come last.
func contextToolSet() map[string]tool.Tool {
	tools := map[string]tool.Tool{}
	for _, n := range []string{"Bash", "Grep", "Read", "Lsp"} {
		tools[n] = &testTool{name: n}
	}
	for _, n := range []string{"OffA", "OffB", "OffC", "OffD"} {
		tools[n] = namedDisabledTool{name: n}
	}
	for _, n := range []string{"mcp__s__a", "mcp__s__b", "mcp__s__c"} {
		tools[n] = &testTool{name: n}
	}
	for _, n := range []string{"ToolSearch", "PlanSearch", "DocSearch"} {
		tools[n] = deferredStubTool(n)
	}
	return tools
}

var enabledToolNames = []string{"Bash", "Grep", "Read", "Lsp"}
var deferredToolNames = []string{"ToolSearch", "PlanSearch", "DocSearch"}

func toolDetailSig(ds []SystemToolDetail) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, fmt.Sprintf("%s=%d", d.Name, d.Tokens))
	}
	slices.Sort(out)
	return out
}

func TestBuildDetails_SystemAndDeferredTools(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	tools := contextToolSet()
	ts := newToolSearchState()
	ds := e.buildDetails(sysPromptSections{}, nil, tools, ts, "", nil, nil, nil)

	wantSystem := make([]string, 0, 4)
	for _, n := range []string{"Bash", "Grep", "Read", "Lsp"} {
		wantSystem = append(wantSystem, fmt.Sprintf("%s=%d", n, e.estimateSingleTool(tools[n])))
	}
	slices.Sort(wantSystem)
	if got := toolDetailSig(ds.systemTools); !slices.Equal(got, wantSystem) {
		t.Errorf("systemTools = %v, want %v", got, wantSystem)
	}
	wantDeferred := make([]string, 0, 3)
	for _, n := range deferredToolNames {
		wantDeferred = append(wantDeferred, fmt.Sprintf("%s=%d", n, e.estimateSingleTool(tools[n])))
	}
	slices.Sort(wantDeferred)
	if got := toolDetailSig(ds.deferredBuiltin); !slices.Equal(got, wantDeferred) {
		t.Errorf("deferredBuiltin = %v, want %v", got, wantDeferred)
	}
}

func TestBuildDetails_DiscoveredDeferredIsSkipped(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	tools := contextToolSet()
	ts := newToolSearchState()
	ts.DiscoverTools([]string{"ToolSearch", "PlanSearch", "DocSearch"})
	ds := e.buildDetails(sysPromptSections{}, nil, tools, ts, "", nil, nil, nil)

	// A discovered deferred tool is neither a loaded system tool nor a
	// deferred entry: the deferred branch always continues past it.
	wantSystem := make([]string, 0, 4)
	for _, n := range []string{"Bash", "Grep", "Read", "Lsp"} {
		wantSystem = append(wantSystem, fmt.Sprintf("%s=%d", n, e.estimateSingleTool(tools[n])))
	}
	slices.Sort(wantSystem)
	if got := toolDetailSig(ds.systemTools); !slices.Equal(got, wantSystem) {
		t.Errorf("systemTools = %v, want %v", got, wantSystem)
	}
	if len(ds.deferredBuiltin) != 0 {
		t.Errorf("deferredBuiltin = %v, want empty", toolDetailSig(ds.deferredBuiltin))
	}
}

func TestBuildDetails_NilToolSearchDropsDeferredEntry(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	tools := contextToolSet()
	ds := e.buildDetails(sysPromptSections{}, nil, tools, nil, "", nil, nil, nil)
	// With no tool-search state the deferred tool is not listed as deferred
	// (the dump mirrors what the request carries), and it is not a loaded
	// system tool either.
	if len(ds.deferredBuiltin) != 0 {
		t.Errorf("deferredBuiltin = %v, want empty", toolDetailSig(ds.deferredBuiltin))
	}
	wantSystem := make([]string, 0, 4)
	for _, n := range []string{"Bash", "Grep", "Read", "Lsp"} {
		wantSystem = append(wantSystem, fmt.Sprintf("%s=%d", n, e.estimateSingleTool(tools[n])))
	}
	slices.Sort(wantSystem)
	if got := toolDetailSig(ds.systemTools); !slices.Equal(got, wantSystem) {
		t.Errorf("systemTools = %v, want %v", got, wantSystem)
	}
}

func TestBuildDetails_MCPLoadedAndDeferred(t *testing.T) {
	t.Parallel()
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	schemaLoaded := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	schemaLazy := json.RawMessage(`{"type":"object"}`)
	reg.SetToolsForTest([]mcp.DiscoveredTool{
		{Name: "mcp__srv__loaded", OriginalName: "loaded", ServerName: "srv", Description: "loaded tool", InputSchema: schemaLoaded, Mode: mcp.ModeDirect},
		{Name: "mcp__srv__lazy", OriginalName: "lazy", ServerName: "other", Description: "lazy tool", InputSchema: schemaLazy},
		{Name: "mcp__srv__code", OriginalName: "code", ServerName: "srv", Description: "code tool", InputSchema: schemaLazy, Mode: mcp.ModeCode},
	})
	e := &Engine{}
	tools := map[string]tool.Tool{"mcp__srv__loaded": NewMCPTool(reg.GetTools()[0], nil)}
	ds := e.buildDetails(sysPromptSections{}, nil, tools, nil, "", nil, nil, reg)

	wantLoaded := []MCPToolDetail{{
		Name: "loaded", ServerName: "srv", IsLoaded: true,
		Tokens: e.estimateMCPSchema("loaded tool", schemaLoaded),
	}}
	if !slices.Equal(ds.mcpLoaded, wantLoaded) {
		t.Errorf("mcpLoaded = %v, want %v", ds.mcpLoaded, wantLoaded)
	}
	wantDeferred := []MCPToolDetail{{
		Name: "lazy", ServerName: "other",
		Tokens: e.estimateMCPSchema("lazy tool", schemaLazy),
	}}
	if !slices.Equal(ds.mcpDeferred, wantDeferred) {
		t.Errorf("mcpDeferred = %v, want %v", ds.mcpDeferred, wantDeferred)
	}
}

func TestBuildDetails_Agents(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	defs := []*types.AgentDefinition{
		nil,
		{AgentType: "Alpha", WhenToUse: "when alpha"},
	}
	ds := e.buildDetails(sysPromptSections{}, nil, nil, nil, "", defs, nil, nil)
	want := []AgentDetail{{
		AgentType: "Alpha",
		Source:    "built-in",
		Tokens:    e.EstimateTokensCalibrated("Alpha when alpha"),
	}}
	if !slices.Equal(ds.agents, want) {
		t.Errorf("agents = %v, want %v", ds.agents, want)
	}
}

func TestBuildDetails_Skills(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	listing := "  alpha: first skill\n\n\tbeta: second skill\t\n"
	ds := e.buildDetails(sysPromptSections{}, nil, nil, nil, listing, nil, nil, nil)
	want := []SkillDetail{
		{Name: "alpha: first skill", Source: "plugin", Tokens: e.EstimateTokensCalibrated("alpha: first skill")},
		{Name: "beta: second skill", Source: "plugin", Tokens: e.EstimateTokensCalibrated("beta: second skill")},
	}
	if !slices.Equal(ds.skills, want) {
		t.Errorf("skills = %v, want %v", ds.skills, want)
	}
}

func TestBuildDetails_SkillsEmptyListing(t *testing.T) {
	t.Parallel()
	ds := (&Engine{}).buildDetails(sysPromptSections{}, nil, nil, nil, "", nil, nil, nil)
	if len(ds.skills) != 0 {
		t.Errorf("skills = %v, want empty for an empty listing", ds.skills)
	}
}

func TestBuildDetails_MessageBreakdownRoles(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	messages := []types.Message{
		{Role: types.RoleSystem,
			Content:    []types.ContentBlock{types.NewTextBlock("system message text")},
			Attachment: &types.Attachment{Type: types.AttachmentTypeQueued, Prompt: "system attachment prompt"}},
		{Role: types.RoleUser,
			Content:    []types.ContentBlock{types.NewTextBlock("user text body")},
			Attachment: &types.Attachment{Type: types.AttachmentTypeQueued, Prompt: "first attachment prompt"}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, Content: json.RawMessage(`"tool output"`)},
		}},
		{Role: types.RoleUser,
			Content:    []types.ContentBlock{types.NewTextBlock("second user text")},
			Attachment: &types.Attachment{Type: types.AttachmentTypeQueued, Prompt: "second attachment prompt"}},
	}
	ds := e.buildDetails(sysPromptSections{}, nil, nil, nil, "", nil, messages, nil)
	mb := ds.messageBreakdown
	if mb == nil {
		t.Fatal("messageBreakdown is nil")
	}
	a1 := e.EstimateTokensCalibrated("first attachment prompt")
	a2 := e.EstimateTokensCalibrated("second attachment prompt")
	if mb.AttachmentTokens != a1+a2 {
		t.Errorf("AttachmentTokens = %d, want %d", mb.AttachmentTokens, a1+a2)
	}
	wantAttach := []AttachmentByType{{Name: "file", Tokens: a1 + a2}}
	if !slices.Equal(mb.AttachmentsByType, wantAttach) {
		t.Errorf("AttachmentsByType = %v, want %v", mb.AttachmentsByType, wantAttach)
	}
	wantUser := e.EstimateTokensCalibrated("user text body") + e.EstimateTokensCalibrated("second user text")
	if mb.UserTextTokens != wantUser {
		t.Errorf("UserTextTokens = %d, want %d", mb.UserTextTokens, wantUser)
	}
	wantCall := e.EstimateTokensCalibrated("Bash" + `{"command":"ls"}`)
	if mb.ToolCallTokens != wantCall {
		t.Errorf("ToolCallTokens = %d, want %d", mb.ToolCallTokens, wantCall)
	}
	wantResult := e.EstimateTokensCalibrated(`"tool output"`)
	if mb.ToolResultTokens != wantResult {
		t.Errorf("ToolResultTokens = %d, want %d", mb.ToolResultTokens, wantResult)
	}
}

func TestBuildDetails_ToolCallsSortedTopFive(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// Seven tool calls with strictly increasing call tokens; the top five by
	// call tokens must survive, in descending order.
	var messages []types.Message
	want := make([]string, 0, 5)
	for i := range 7 {
		input := json.RawMessage(`{"p":"` + strings.Repeat("x", i*40) + `"}`)
		messages = append(messages, types.Message{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, Name: fmt.Sprintf("Tool%d", i), Input: input},
			},
		})
	}
	ds := e.buildDetails(sysPromptSections{}, nil, nil, nil, "", nil, messages, nil)
	got := make([]string, 0, len(ds.messageBreakdown.ToolCallsByType))
	for _, tc := range ds.messageBreakdown.ToolCallsByType {
		got = append(got, tc.Name)
	}
	for i := 6; i >= 2; i-- {
		want = append(want, fmt.Sprintf("Tool%d", i))
	}
	if !slices.Equal(got, want) {
		t.Errorf("ToolCallsByType order = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// ContextBreakdown end-to-end field wiring.
// ---------------------------------------------------------------------------

func TestContextBreakdown_FieldWiring(t *testing.T) {
	t.Parallel()
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("You are a test assistant.")
	e.tools["Bash"] = &testTool{name: "Bash"}
	e.tools["ToolSearch"] = deferredStubTool("ToolSearch")
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello there")}},
		{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("answer")},
			Usage: &types.Usage{
				InputTokens:              1234,
				OutputTokens:             567,
				CacheCreationInputTokens: 89,
				CacheReadInputTokens:     1011,
			}},
	}
	e.ContextTokens = 50_000

	bd := e.ContextBreakdown()
	if bd.Model != "claude-test" {
		t.Errorf("Model = %q, want claude-test", bd.Model)
	}
	if bd.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200000", bd.ContextWindow)
	}
	if bd.TotalTokens != 50_000 {
		t.Errorf("TotalTokens = %d, want 50000", bd.TotalTokens)
	}
	if !bd.IsAutoCompact {
		t.Error("IsAutoCompact = false, want true for a positive context window")
	}
	if bd.APIUsage == nil {
		t.Fatal("APIUsage is nil")
	}
	wantUsage := APIUsageSnapshot{InputTokens: 1234, OutputTokens: 567, CacheCreationInputTokens: 89, CacheReadInputTokens: 1011}
	if *bd.APIUsage != wantUsage {
		t.Errorf("APIUsage = %+v, want %+v", *bd.APIUsage, wantUsage)
	}
	if got := toolDetailSig(bd.SystemTools); !slices.Equal(got,
		[]string{fmt.Sprintf("Bash=%d", e.estimateSingleTool(e.tools["Bash"]))}) {
		t.Errorf("SystemTools = %v, want only Bash", got)
	}
	if got := toolDetailSig(bd.DeferredBuiltinTools); !slices.Equal(got,
		[]string{fmt.Sprintf("ToolSearch=%d", e.estimateSingleTool(e.tools["ToolSearch"]))}) {
		t.Errorf("DeferredBuiltinTools = %v, want only ToolSearch", got)
	}
}

func TestContextBreakdown_OverWindowClampsFreeAndReserved(t *testing.T) {
	t.Parallel()
	e := newTestEngineForBreakdown(t)
	e.autoCompactConfig.ContextWindow = 10_000
	e.SetSystemPrompt("You are a test assistant.")
	e.messages = []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello there")}},
	}
	// One token over the window: freeTokens is exactly -1.
	e.ContextTokens = 10_001

	bd := e.ContextBreakdown()
	free, ok := categoryByName(bd.Categories, "Free space")
	if !ok {
		t.Fatal("Free space category missing")
	}
	if free.Tokens != 0 {
		t.Errorf("Free space tokens = %d, want 0 when the window is over-full", free.Tokens)
	}
	res, ok := categoryByName(bd.Categories, "Autocompact buffer")
	if !ok {
		t.Fatal("Autocompact buffer category missing")
	}
	if res.Tokens != 0 {
		t.Errorf("Autocompact buffer tokens = %d, want 0 when nothing is free", res.Tokens)
	}
	if bd.Percentage != 100.0 {
		t.Errorf("Percentage = %v, want 100 (capped)", bd.Percentage)
	}
}

func TestContextBreakdown_IsAutoCompactWindowBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		window int
		want   bool
	}{
		{200_000, true},
		{1, true},
		{0, false},
		{-1, false},
	} {
		e := newTestEngineForBreakdown(t)
		e.autoCompactConfig.ContextWindow = tc.window
		e.SetSystemPrompt("You are a test assistant.")
		e.ContextTokens = 500
		bd := e.ContextBreakdown()
		if bd.IsAutoCompact != tc.want {
			t.Errorf("IsAutoCompact for window %d = %v, want %v", tc.window, bd.IsAutoCompact, tc.want)
		}
	}
}

func TestContextBreakdown_ReleasesReadLock(t *testing.T) {
	t.Parallel()
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("You are a test assistant.")
	e.ContextTokens = 5_000
	e.ContextBreakdown()
	// ContextBreakdown must hand back the read lock; a leaked RLock makes the
	// writer-side TryLock fail forever.
	if !e.mu.TryLock() {
		t.Fatal("ContextBreakdown leaked the engine read lock")
	}
	e.mu.Unlock()
}

func TestContextBreakdown_MemoryDirFallbackPath(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	e := newTestEngineForBreakdown(t)
	e.memoryDir = ""
	e.SetSystemPrompt("You are a test assistant.")
	e.ContextTokens = 5_000

	bd := e.ContextBreakdown()
	if len(bd.MemoryFiles) != 1 {
		t.Fatalf("memory files = %d, want 1 (the typed-memory prompt entry)", len(bd.MemoryFiles))
	}
	want := filepath.Join(long.GetMemoryPath(e.workingDir), long.EntrypointName)
	if bd.MemoryFiles[0].Path != want {
		t.Errorf("memory path = %q, want %q (GetMemoryPath fallback)", bd.MemoryFiles[0].Path, want)
	}
}

func TestContextBreakdown_LegacyMemoryFilesWhenPromptEmpty(t *testing.T) {
	// With typed memory disabled the memory prompt is empty, so the breakdown
	// falls back to loading memory files from disk.
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("You are a test assistant.")
	e.ContextTokens = 5_000
	bd := e.ContextBreakdown()
	if len(bd.MemoryFiles) != 0 {
		t.Errorf("memory files = %v, want none when typed memory is disabled", bd.MemoryFiles)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestComputeReservedTokens_SmallWindow(t *testing.T) {
	t.Parallel()
	if got := computeReservedTokens(1, 0); got != 3_000 {
		t.Errorf("computeReservedTokens(1) = %d, want 3000 (the 3K floor)", got)
	}
	if got := computeReservedTokens(-5, 0); got != 0 {
		t.Errorf("computeReservedTokens(-5) = %d, want 0", got)
	}
}

func TestLastAPIUsage_SkipsNonAssistantAndZeroUsage(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Usage: &types.Usage{InputTokens: 10, OutputTokens: 1}},
		{Role: types.RoleAssistant, Usage: &types.Usage{CacheReadInputTokens: 7}},
		{Role: types.RoleAssistant, Usage: &types.Usage{}},
	}
	got := lastAPIUsage(msgs)
	want := APIUsageSnapshot{CacheReadInputTokens: 7}
	if got == nil {
		t.Fatalf("lastAPIUsage = nil, want %+v", want)
	}
	if *got != want {
		t.Errorf("lastAPIUsage = %+v, want %+v", *got, want)
	}
}

func TestLastAPIUsage_IgnoresNonAssistantUsage(t *testing.T) {
	t.Parallel()
	// A trailing user message carries usage data; only assistant messages may
	// answer the API-usage query.
	msgs := []types.Message{
		{Role: types.RoleAssistant, Usage: &types.Usage{InputTokens: 42}},
		{Role: types.RoleUser, Usage: &types.Usage{InputTokens: 999, OutputTokens: 999}},
	}
	got := lastAPIUsage(msgs)
	if got == nil {
		t.Fatal("lastAPIUsage = nil, want the assistant usage")
	}
	if *got != (APIUsageSnapshot{InputTokens: 42}) {
		t.Errorf("lastAPIUsage = %+v, want InputTokens 42", *got)
	}
}

func TestLastAPIUsage_AnySingleNonZeroFieldCounts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want APIUsageSnapshot
	}{
		{"input", APIUsageSnapshot{InputTokens: 8}},
		{"output", APIUsageSnapshot{OutputTokens: 5}},
		{"cache creation", APIUsageSnapshot{CacheCreationInputTokens: 6}},
		{"cache read", APIUsageSnapshot{CacheReadInputTokens: 7}},
	} {
		msgs := []types.Message{
			{Role: types.RoleAssistant, Usage: &types.Usage{InputTokens: 42}},
			{Role: types.RoleAssistant, Usage: &types.Usage{
				InputTokens:              tc.want.InputTokens,
				OutputTokens:             tc.want.OutputTokens,
				CacheCreationInputTokens: tc.want.CacheCreationInputTokens,
				CacheReadInputTokens:     tc.want.CacheReadInputTokens,
			}},
		}
		got := lastAPIUsage(msgs)
		if got == nil {
			t.Errorf("%s: lastAPIUsage = nil, want %+v", tc.name, tc.want)
			continue
		}
		if *got != tc.want {
			t.Errorf("%s: lastAPIUsage = %+v, want %+v", tc.name, *got, tc.want)
		}
	}
}

func TestLastAPIUsage_ToleratesNilUsage(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleAssistant, Usage: &types.Usage{InputTokens: 42}},
		{Role: types.RoleAssistant},
	}
	got := lastAPIUsage(msgs)
	if got == nil {
		t.Fatal("lastAPIUsage = nil, want the earlier assistant usage")
	}
	if *got != (APIUsageSnapshot{InputTokens: 42}) {
		t.Errorf("lastAPIUsage = %+v, want InputTokens 42", *got)
	}
}

func TestSafePct_WholeOne(t *testing.T) {
	t.Parallel()
	if got := safePct(1, 1); got != 100.0 {
		t.Errorf("safePct(1, 1) = %v, want 100", got)
	}
}

func TestCloneHelpers_Nil(t *testing.T) {
	t.Parallel()
	if got := slicesCloneMessages(nil); got != nil {
		t.Errorf("slicesCloneMessages(nil) = %v, want nil", got)
	}
	if got := agentDefsClone(nil); got != nil {
		t.Errorf("agentDefsClone(nil) = %v, want nil", got)
	}
	msgs := []types.Message{{Role: types.RoleUser}}
	if got := slicesCloneMessages(msgs); len(got) != 1 || got[0].Role != types.RoleUser {
		t.Errorf("slicesCloneMessages = %v, want a one-message copy", got)
	}
	defs := []*types.AgentDefinition{{AgentType: "Alpha"}}
	if got := agentDefsClone(defs); len(got) != 1 || got[0].AgentType != "Alpha" {
		t.Errorf("agentDefsClone = %v, want a one-entry copy", got)
	}
}

func TestBuildGrid_OverflowFreeFirst(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Free space", Tokens: 500, Color: ColorFree, IsFree: true},
		{Name: "Alpha", Tokens: 1_000, Color: "1"},
		{Name: "Beta", Tokens: 1, Color: "2"},
		{Name: "Gamma", Tokens: 1, Color: "3"},
		{Name: "Delta", Tokens: 1, Color: "4"},
	}
	// The free category leads the slice: any pass that forgets to skip it
	// (or stops at it) changes the whole allocation.
	want := []string{
		"rows=5",
		"Free space|" + ColorFree + "|f0|fillfalse|30",
		"Alpha|1|f0.01503|fillfalse|67",
		"Beta|2|f1|filltrue|1",
		"Gamma|3|f1|filltrue|1",
		"Delta|4|f1|filltrue|1",
	}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_RescaledOverflowKeepsFreeRemainder(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Free space", Tokens: 1, Color: ColorFree, IsFree: true},
		{Name: "Alpha", Tokens: 12_215, Color: "1"},
		{Name: "Beta", Tokens: 19_758, Color: "2"},
		{Name: "Gamma", Tokens: 12_228, Color: "3"},
		{Name: "Delta", Tokens: 3_478, Color: "4"},
		{Name: "Autocompact buffer", Tokens: 14_671, Color: ColorReserved, IsReserved: true},
	}
	// Phase 2 assigns 78 content squares against a cap of 76, the rescale
	// lands on 75, and free space keeps the single leftover square.
	want := []string{
		"rows=5",
		"Free space|" + ColorFree + "|f0|fillfalse|1",
		"Alpha|1|f0.05104461727384363|fillfalse|19",
		"Beta|2|f0.03155734386071465|fillfalse|31",
		"Gamma|3|f0.0509903500163559|fillfalse|19",
		"Delta|4|f0.1792725704427832|fillfalse|6",
		"Autocompact buffer|" + ColorReserved + "|f1|filltrue|24",
	}
	if got := rleGrid(BuildGrid(cats, 200_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_ZeroTokenContentFirstInOverflow(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Zero", Tokens: 0, Color: "5"},
		{Name: "Alpha", Tokens: 1_000, Color: "1"},
		{Name: "Beta", Tokens: 1, Color: "2"},
		{Name: "Gamma", Tokens: 1, Color: "3"},
		{Name: "Delta", Tokens: 1, Color: "4"},
		{Name: "Free space", Tokens: 0, Color: ColorFree, IsFree: true},
	}
	// Same overflow as OverflowScaled, but the zero-token category leads the
	// slice so a square wrongly handed to it is not truncated away.
	want := []string{
		"rows=5",
		"Alpha|1|f0.010029999999999999|fillfalse|97",
		"Beta|2|f1|filltrue|1",
		"Gamma|3|f1|filltrue|1",
		"Delta|4|f1|filltrue|1",
	}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildGrid_SingleTokenReservedKeepsSquare(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Alpha", Tokens: 99_999, Color: "1"},
		{Name: "Free space", Tokens: 0, Color: ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 1, Color: ColorReserved, IsReserved: true},
	}
	// One token is still a non-zero category: the buffer keeps its square.
	want := []string{
		"rows=5",
		"Alpha|1|f0.01000010000100001|fillfalse|99",
		"Autocompact buffer|" + ColorReserved + "|f1|filltrue|1",
	}
	if got := rleGrid(BuildGrid(cats, 100_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildDetails_ToolCallsSixTruncateToFive(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// Six distinct call sizes: the truncation threshold is >5, so exactly one
	// entry must be dropped.
	var messages []types.Message
	for i := range 6 {
		input := json.RawMessage(`{"p":"` + strings.Repeat("x", i*40) + `"}`)
		messages = append(messages, types.Message{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, Name: fmt.Sprintf("Tool%d", i), Input: input},
			},
		})
	}
	ds := e.buildDetails(sysPromptSections{}, nil, nil, nil, "", nil, messages, nil)
	got := make([]string, 0, len(ds.messageBreakdown.ToolCallsByType))
	for _, tc := range ds.messageBreakdown.ToolCallsByType {
		got = append(got, tc.Name)
	}
	want := []string{"Tool5", "Tool4", "Tool3", "Tool2", "Tool1"}
	if !slices.Equal(got, want) {
		t.Errorf("ToolCallsByType = %v, want %v", got, want)
	}
}

func TestBuildGrid_FreeNamedContentSquareNotFilled(t *testing.T) {
	t.Parallel()
	cats := []ContextCategory{
		{Name: "Free space", Tokens: 1, Color: "7"},
		{Name: "Alpha", Tokens: 100, Color: "1"},
	}
	// IsFilled is decided by the category NAME, not the IsFree flag: a
	// fullness-1.0 square named "Free space" still renders unfilled.
	want := []string{
		"rows=5",
		"Free space|7|f1|fillfalse|1",
		"Alpha|1|f0.0101|fillfalse|99",
	}
	if got := rleGrid(BuildGrid(cats, 1_000)); !slices.Equal(got, want) {
		t.Errorf("grid = %v, want %v", got, want)
	}
}

func TestBuildDetails_EqualCallTokensKeepInsertionOrder(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// Two calls with identical token counts: the sort is a strict `>` on call
	// tokens, so the insertion order survives (a `>=` comparator would swap
	// them and break the strict weak ordering sort.Slice relies on).
	input := json.RawMessage(`{"p":"same payload"}`)
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, Name: "ToolA", Input: input},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, Name: "ToolB", Input: input},
		}},
	}
	ds := e.buildDetails(sysPromptSections{}, nil, nil, nil, "", nil, messages, nil)
	if len(ds.messageBreakdown.ToolCallsByType) != 2 {
		t.Fatalf("ToolCallsByType = %v, want 2 entries", ds.messageBreakdown.ToolCallsByType)
	}
	if ds.messageBreakdown.ToolCallsByType[0].CallTokens != ds.messageBreakdown.ToolCallsByType[1].CallTokens {
		t.Fatalf("fixture lost its point: tokens differ (%d vs %d)",
			ds.messageBreakdown.ToolCallsByType[0].CallTokens,
			ds.messageBreakdown.ToolCallsByType[1].CallTokens)
	}
	got := []string{ds.messageBreakdown.ToolCallsByType[0].Name, ds.messageBreakdown.ToolCallsByType[1].Name}
	want := []string{"ToolA", "ToolB"}
	if !slices.Equal(got, want) {
		t.Errorf("ToolCallsByType order = %v, want %v", got, want)
	}
}

func TestBreakdown_CodeModeMCPToolsHidden(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	directSchema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	reg.SetToolsForTest([]mcp.DiscoveredTool{
		// Code-mode tool first: the skip must not abort iteration over the
		// tools that follow it.
		{
			Name: "mcp__srv__secret", OriginalName: "secret", ServerName: "srv",
			Description: "hidden", InputSchema: json.RawMessage(`{"type":"object"}`),
			Mode: mcp.ModeCode,
		},
		{
			Name: "mcp__srv__direct", OriginalName: "direct", ServerName: "srv",
			Description: "direct load", InputSchema: directSchema,
			Mode: mcp.ModeDirect, // AlwaysLoad stays false: direct must still load
		},
		{
			Name: "mcp__srv__ondemand", OriginalName: "ondemand", ServerName: "srv",
			Description: "load on demand", InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	})
	e.mcpRegistry = reg
	// Mirror refreshTools: discovered MCP tools land in the engine tool map.
	e.tools["mcp__srv__direct"] = NewMCPTool(reg.GetTools()[1], nil)
	e.tools["mcp__srv__secret"] = NewMCPTool(reg.GetTools()[0], nil)
	e.SetSystemPrompt("x")
	e.ContextTokens = 50_000

	// Estimator: only the direct-mode tool counts as LLM-loaded.
	want := e.estimateMCPSchema("direct load", directSchema)
	if got := e.estimateMCPLoadedTools(reg); got != want {
		t.Errorf("estimateMCPLoadedTools = %d, want %d (direct only)", got, want)
	}

	bd := e.ContextBreakdown()
	for _, d := range bd.MCPToolsLoaded {
		if d.Name == "secret" {
			t.Error("code-mode tool must not appear in MCPToolsLoaded")
		}
	}
	for _, d := range bd.MCPToolsDeferred {
		if d.Name == "secret" {
			t.Error("code-mode tool must not appear in MCPToolsDeferred")
		}
	}
	var directLoaded bool
	for _, d := range bd.MCPToolsLoaded {
		if d.Name == "direct" {
			directLoaded = true
		}
	}
	if !directLoaded {
		t.Error("direct-mode tool must appear in MCPToolsLoaded even with AlwaysLoad=false")
	}
}

// --- TestCB_*: second-round mutation gaps in the grid builder and the
// system-tool estimate loop.

func flattenGrid(rows [][]GridSquare) []GridSquare {
	var out []GridSquare
	for _, r := range rows {
		out = append(out, r...)
	}
	return out
}

func TestCB_GridSquareCountsAndFullness(t *testing.T) {
	cats := []ContextCategory{
		{Name: "Free space", ID: "free_space", Tokens: 400, Color: ColorFree, IsFree: true},
		{Name: "Messages", ID: "messages", Tokens: 400, Color: ColorMessages},
		{Name: "Autocompact buffer", ID: "autocompact_buffer", Tokens: 200, Color: ColorReserved, IsReserved: true},
	}
	rows := BuildGrid(cats, 1000)
	if len(rows) != GridHeight {
		t.Fatalf("rows = %d, want %d", len(rows), GridHeight)
	}
	squares := flattenGrid(rows)
	if len(squares) != TotalSquares(1000) {
		t.Fatalf("squares = %d, want %d", len(squares), TotalSquares(1000))
	}
	counts := map[string]int{}
	fullness := map[string]float64{}
	filled := map[string]bool{}
	for _, s := range squares {
		counts[s.CategoryName]++
		if n := counts[s.CategoryName]; n == 1 {
			fullness[s.CategoryName] = s.SquareFullness
			filled[s.CategoryName] = s.IsFilled
		} else if fullness[s.CategoryName] != s.SquareFullness || filled[s.CategoryName] != s.IsFilled {
			t.Errorf("%s squares disagree on fullness/filled: %v vs %v", s.CategoryName,
				fullness[s.CategoryName], s.SquareFullness)
		}
	}
	wantCounts := map[string]int{"Free space": 40, "Messages": 40, "Autocompact buffer": 20}
	for name, want := range wantCounts {
		if counts[name] != want {
			t.Errorf("%s squares = %d, want %d (all: %v)", name, counts[name], want, counts)
		}
	}
	if fullness["Free space"] != 0.0 {
		t.Errorf("free square fullness = %v, want 0", fullness["Free space"])
	}
	if fullness["Autocompact buffer"] != 1.0 {
		t.Errorf("reserved square fullness = %v, want 1", fullness["Autocompact buffer"])
	}
	if fullness["Messages"] != 0.025 {
		t.Errorf("content square fullness = %v, want 0.025 (1 / (400 tokens / 10 tokens-per-square))",
			fullness["Messages"])
	}
	if !filled["Autocompact buffer"] {
		t.Error("reserved squares IsFilled = false, want true (fullness 1 >= 0.7)")
	}
	if filled["Free space"] || filled["Messages"] {
		t.Error("free and content squares IsFilled = true, want false (fullness below 0.7)")
	}
}

func TestCB_GridWithoutSquaresHasNoRows(t *testing.T) {
	rows := BuildGrid(nil, 1000)
	if len(rows) != 0 {
		t.Errorf("rows for empty categories = %d, want 0 (row loop stops once squares run out)", len(rows))
	}
}

func TestCB_SystemToolsSkipsMCPWithoutStoppingTheLoop(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	tools := map[string]tool.Tool{}
	want := 0
	for i := range 10 {
		name := fmt.Sprintf("NormalTool%d", i)
		st := &stubTool{name: name, prompt: fmt.Sprintf("prompt %d", i)}
		tools[name] = st
		want += e.estimateSingleTool(st)
	}
	if want == 0 {
		t.Fatal("expected non-zero tool estimates for the baseline")
	}
	for i := range 10 {
		name := fmt.Sprintf("mcp__srv__tool%d", i)
		tools[name] = &stubTool{name: name, prompt: "mcp schema must not be counted here"}
	}
	got := e.estimateSystemTools(tools, nil, nil)
	if got != want {
		t.Errorf("estimateSystemTools = %d, want %d (every non-MCP tool counted, MCP ones skipped)", got, want)
	}
}

func TestCB_AttachmentBucketIsSingleFileEntry(t *testing.T) {
	// Every attachment lands in one "file" bucket, so the AttachmentsByType sort
	// and top-5 truncation in buildDetails can never see more than one entry.
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	var msgs []types.Message
	for i := range 8 {
		msgs = append(msgs, types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock("msg")},
			Attachment: &types.Attachment{Type: types.AttachmentTypeQueued,
				Prompt: fmt.Sprintf("attachment %d with distinct content", i)},
		})
	}
	e.messages = msgs
	e.ContextTokens = 20_000

	bd := e.ContextBreakdown()
	if bd.MessageBreakdown == nil {
		t.Fatal("MessageBreakdown is nil")
	}
	att := bd.MessageBreakdown.AttachmentsByType
	if len(att) != 1 {
		t.Fatalf("AttachmentsByType = %d entries, want 1: %+v", len(att), att)
	}
	if att[0].Name != "file" {
		t.Errorf("attachment bucket name = %q, want %q", att[0].Name, "file")
	}
	if att[0].Tokens <= 0 {
		t.Errorf("attachment bucket tokens = %d, want > 0", att[0].Tokens)
	}
	if bd.MessageBreakdown.AttachmentTokens <= 0 {
		t.Errorf("AttachmentTokens = %d, want > 0", bd.MessageBreakdown.AttachmentTokens)
	}
}
