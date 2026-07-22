package tui

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// buildTestBreakdown creates a realistic ContextBreakdown for render testing.
func buildTestBreakdown() *engine.ContextBreakdown {
	bd := &engine.ContextBreakdown{
		Model:         "claude-test",
		ContextWindow: 200_000,
		TotalTokens:   96_400,
		Percentage:    48.2,
		IsAutoCompact: true,
	}

	bd.Categories = []engine.ContextCategory{
		{Name: "System prompt", Tokens: 8_000, Percentage: 8.3, Color: engine.ColorSystemPrompt},
		{Name: "Platform info", Tokens: 200, Percentage: 0.2, Color: engine.ColorPlatformInfo},
		{Name: "Tool prompts", Tokens: 15_000, Percentage: 15.6, Color: engine.ColorToolPrompts},
		{Name: "Memory files", Tokens: 3_000, Percentage: 3.1, Color: engine.ColorMemoryFiles},
		{Name: "System tools", Tokens: 4_000, Percentage: 4.2, Color: engine.ColorSystemTools},
		{Name: "MCP tools", Tokens: 2_000, Percentage: 2.1, Color: engine.ColorMCPLoaded},
		{Name: "Messages", Tokens: 50_000, Percentage: 51.9, Color: engine.ColorMessages},
		{Name: "Free space", Tokens: 10_800, Percentage: 11.2, Color: engine.ColorFree, IsFree: true},
		{Name: "Autocompact buffer", Tokens: 3_400, Percentage: 3.5, Color: engine.ColorReserved, IsReserved: true},
	}

	bd.GridRows = engine.BuildGrid(bd.Categories, 200_000)

	bd.MCPToolsLoaded = []engine.MCPToolDetail{
		{Name: "search", ServerName: "github", Tokens: 1200, IsLoaded: true},
		{Name: "create_file", ServerName: "filesystem", Tokens: 800, IsLoaded: true},
	}
	bd.MCPToolsDeferred = []engine.MCPToolDetail{
		{Name: "deploy", ServerName: "vercel", Tokens: 600},
	}

	bd.SystemTools = []engine.SystemToolDetail{
		{Name: "Bash", Tokens: 2000},
		{Name: "Read", Tokens: 1500},
		{Name: "Write", Tokens: 500},
	}

	bd.SystemPromptSections = []engine.SystemPromptSectionDetail{
		{Name: "Base prompt", Tokens: 6000},
		{Name: "Tool prompts", Tokens: 15000},
	}

	bd.MemoryFiles = []engine.MemoryFileDetail{
		{Path: "/home/user/project/CLAUDE.md", Tokens: 2000},
		{Path: "/home/user/.gbot/projects/xyz/user_role.md", Tokens: 1000},
	}

	bd.Agents = []engine.AgentDetail{
		{AgentType: "General", Source: "built-in", Tokens: 800},
		{AgentType: "Explore", Source: "built-in", Tokens: 600},
		{AgentType: "Plan", Source: "built-in", Tokens: 500},
	}

	bd.Skills = []engine.SkillDetail{
		{Name: "commit", Source: "plugin", Tokens: 300},
		{Name: "review-pr", Source: "plugin", Tokens: 200},
	}

	bd.MessageBreakdown = &engine.MessageBreakdown{
		ToolCallTokens:      8_000,
		ToolResultTokens:    25_000,
		AssistantTextTokens: 12_000,
		UserTextTokens:      5_000,
		ToolCallsByType: []engine.ToolCallByType{
			{Name: "Bash", CallTokens: 4000, ResultTokens: 15000},
			{Name: "Read", CallTokens: 3000, ResultTokens: 8000},
			{Name: "Write", CallTokens: 1000, ResultTokens: 2000},
		},
	}

	bd.APIUsage = &engine.APIUsageSnapshot{
		InputTokens:              90_000,
		OutputTokens:             6_400,
		CacheCreationInputTokens: 0,
		CacheReadInputTokens:     0,
	}

	return bd
}

// TestRenderContextView_ContainsAllSections verifies the output has all expected sections.
func TestRenderContextView_ContainsAllSections(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	wantSections := []string{
		"Context Usage",
		"System prompt",
		"Messages",
		"MCP tools",
		"Custom agents",
		"Memory files",
		"Skills",
		"Message breakdown",
	}
	for _, want := range wantSections {
		if !strings.Contains(output, want) {
			t.Errorf("output missing section %q", want)
		}
	}
}

// TestRenderContextView_ContainsTokenCounts verifies token count formatting.
func TestRenderContextView_ContainsTokenCounts(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	if !strings.Contains(output, "48.2%") {
		t.Error("output missing percentage 48.2%")
	}
	if !strings.Contains(output, "tokens") {
		t.Error("output missing 'tokens'")
	}
}

// TestRenderContextView_GridSymbols verifies grid uses correct symbols.
func TestRenderContextView_GridSymbols(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	// Grid should contain at least one filled symbol.
	if !strings.Contains(output, engine.SymFilledFull) && !strings.Contains(output, engine.SymFilledPart) {
		t.Error("grid missing filled symbols")
	}
	// Grid should contain free space symbol.
	if !strings.Contains(output, engine.SymFreeSpace) {
		t.Error("grid missing free space symbol")
	}
}

// TestRenderContextView_MCPSection verifies MCP tool rendering.
func TestRenderContextView_MCPSection(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	if !strings.Contains(output, "Loaded") {
		t.Error("MCP section missing 'Loaded' sub-header")
	}
	if !strings.Contains(output, "search") {
		t.Error("MCP section missing tool 'search'")
	}
	if !strings.Contains(output, "Available") {
		t.Error("MCP section missing 'Available' sub-header for deferred tools")
	}
	if !strings.Contains(output, "deploy") {
		t.Error("MCP section missing deferred tool 'deploy'")
	}
}

// TestRenderContextView_MessageBreakdown verifies message breakdown rendering.
func TestRenderContextView_MessageBreakdown(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	// Verify all breakdown lines present with formatted token counts.
	// FormatTokenCount uses 1024-based: 8000→7.8k, 25000→24.4k, 12000→11.7k, 5000→4.9k
	if !strings.Contains(output, "Tool calls: 7.8k") {
		t.Error("missing 'Tool calls: 7.8k'")
	}
	if !strings.Contains(output, "Tool results: 24.4k") {
		t.Error("missing 'Tool results: 24.4k'")
	}
	if !strings.Contains(output, "Assistant text: 11.7k") {
		t.Error("missing 'Assistant text: 11.7k'")
	}
	if !strings.Contains(output, "User text: 4.9k") {
		t.Error("missing 'User text: 4.9k'")
	}
}

// TestRenderContextView_MessageBreakdown_TopTools verifies top-tools rendering
// with correct names, call/result token counts, and descending order.
func TestRenderContextView_MessageBreakdown_TopTools(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	if !strings.Contains(output, "Top tools:") {
		t.Fatal("missing 'Top tools:' header")
	}
	// ToolCallsByType sorted by CallTokens desc: Bash(4000), Read(3000), Write(1000)
	// Verify Bash appears before Read.
	bashIdx := strings.Index(output, "Bash")
	readIdx := strings.Index(output, "Read")
	if bashIdx < 0 {
		t.Fatal("missing Bash in top tools")
	}
	if readIdx < 0 {
		t.Fatal("missing Read in top tools")
	}
	if bashIdx > readIdx {
		t.Error("Bash should appear before Read (sorted by call tokens desc)")
	}
	// Verify Bash shows calls + results: 4000→"3.9k", 15000→"14.6k"
	if !strings.Contains(output, "Bash: calls 3.9k, results 14.6k") {
		t.Error("missing 'Bash: calls 3.9k, results 14.6k'")
	}
	// Verify Read shows calls + results: 3000→"2.9k", 8000→"7.8k"
	if !strings.Contains(output, "Read: calls 2.9k, results 7.8k") {
		t.Error("missing 'Read: calls 2.9k, results 7.8k'")
	}
	// Write: 1000→"1.0k", 2000→"2.0k"
	if !strings.Contains(output, "Write: calls 1.0k, results 2.0k") {
		t.Error("missing 'Write: calls 1.0k, results 2.0k'")
	}
}

// TestRenderContextView_MessageBreakdown_Nil verifies no section when breakdown is nil.
func TestRenderContextView_MessageBreakdown_Nil(t *testing.T) {
	bd := buildTestBreakdown()
	bd.MessageBreakdown = nil
	output := renderContextView(bd, 80)

	if strings.Contains(output, "Message breakdown") {
		t.Error("should not render Message breakdown when nil")
	}
}

// TestRenderContextView_MessageBreakdown_ZeroTokens verifies no section when all tokens are zero.
func TestRenderContextView_MessageBreakdown_ZeroTokens(t *testing.T) {
	bd := buildTestBreakdown()
	bd.MessageBreakdown = &engine.MessageBreakdown{} // all zero
	output := renderContextView(bd, 80)

	if strings.Contains(output, "Message breakdown") {
		t.Error("should not render Message breakdown when all token counts are zero")
	}
}

// TestRenderContextView_AgentsSection verifies agent rendering.
func TestRenderContextView_AgentsSection(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	if !strings.Contains(output, "Custom agents") {
		t.Error("missing 'Custom agents' section")
	}
	if !strings.Contains(output, "General") {
		t.Error("missing agent 'General'")
	}
}

// TestRenderContextView_MemoryFilesSection verifies memory file rendering.
func TestRenderContextView_MemoryFilesSection(t *testing.T) {
	bd := buildTestBreakdown()
	output := renderContextView(bd, 80)

	if !strings.Contains(output, "Memory files") {
		t.Error("missing 'Memory files' section")
	}
	// Verify path shortening — long paths show ".../parent/file".
	if !strings.Contains(output, ".../project/CLAUDE.md") {
		t.Error("missing shortened path '.../project/CLAUDE.md'")
	}
	// Second file also shortened.
	if !strings.Contains(output, ".../xyz/user_role.md") {
		t.Error("missing shortened path '.../xyz/user_role.md'")
	}
	// Verify token counts appear in output. 2000 → "2.0k", 1000 → "1.0k".
	if !strings.Contains(output, "2.0k tokens") {
		t.Error("missing '2.0k tokens' for CLAUDE.md")
	}
	if !strings.Contains(output, "1.0k tokens") {
		t.Error("missing '1.0k tokens' for user_role.md")
	}
}

// TestRenderContextView_MemoryFiles_Empty verifies no detail section when empty.
// Note: the legend may still show "Memory files" as a category; we check the
// detail section header which uses bold formatting.
func TestRenderContextView_MemoryFiles_Empty(t *testing.T) {
	bd := buildTestBreakdown()
	bd.MemoryFiles = nil
	output := renderMemorySection(bd)
	if output != "" {
		t.Errorf("renderMemorySection should be empty when no files, got:\n%s", output)
	}
}

// TestRenderContextView_SuggestionsAt80Percent verifies suggestions appear when usage >= 80%.
func TestRenderContextView_SuggestionsAt80Percent(t *testing.T) {
	bd := buildTestBreakdown()
	bd.TotalTokens = 170_000
	bd.Percentage = 85.0
	output := renderContextView(bd, 80)

	if !strings.Contains(output, "Suggestions") {
		t.Error("missing 'Suggestions' section at 85% usage")
	}
	if !strings.Contains(output, "full") {
		t.Error("missing 'full' keyword in capacity warning")
	}
}

// TestRenderContextView_NoSuggestionsBelow80 verifies no suggestions below threshold.
func TestRenderContextView_NoSuggestionsBelow80(t *testing.T) {
	bd := buildTestBreakdown()
	bd.TotalTokens = 50_000
	bd.Percentage = 25.0
	bd.MessageBreakdown = nil // no breakdown → no tool-result bloat suggestion
	bd.MemoryFiles = nil      // no memory bloat suggestion
	output := renderContextView(bd, 80)

	if strings.Contains(output, "Suggestions") {
		t.Error("should not show suggestions at 25% usage")
	}
}

// TestShortenPath verifies path shortening logic.
func TestShortenPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/project/CLAUDE.md", ".../project/CLAUDE.md"},
		{"/home/user/file.txt", ".../user/file.txt"},
		{"simple.txt", "simple.txt"},
	}
	for _, tt := range tests {
		got := shortenPath(tt.input)
		if got != tt.want {
			t.Errorf("shortenPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestGenerateSuggestions_ToolResultBloat verifies tool result suggestion.
func TestGenerateSuggestions_ToolResultBloat(t *testing.T) {
	bd := buildTestBreakdown()
	bd.TotalTokens = 100_000
	bd.MessageBreakdown.ToolResultTokens = 20_000 // 20% > 15% threshold
	suggs := generateSuggestions(bd)

	found := false
	for _, s := range suggs {
		if s.Title == "Tool results dominate" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Tool results dominate' suggestion at 20%")
	}
}

// TestGenerateSuggestions_MemoryBloat verifies memory file bloat suggestion.
func TestGenerateSuggestions_MemoryBloat(t *testing.T) {
	bd := buildTestBreakdown()
	bd.TotalTokens = 100_000
	bd.MemoryFiles = []engine.MemoryFileDetail{
		{Path: "big.md", Tokens: 10_000}, // 10% > 5% threshold and > 5000 tokens
	}
	suggs := generateSuggestions(bd)

	found := false
	for _, s := range suggs {
		if s.Title == "Memory files are large" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Memory files are large' suggestion at 10% / 10K tokens")
	}
}

// TestGenerateSuggestions_WarningsBeforeInfo verifies severity ordering.
func TestGenerateSuggestions_WarningsBeforeInfo(t *testing.T) {
	bd := buildTestBreakdown()
	bd.TotalTokens = 170_000
	bd.Percentage = 85.0
	bd.MessageBreakdown.ToolResultTokens = 20_000
	suggs := generateSuggestions(bd)

	if len(suggs) < 2 {
		t.Fatalf("expected >= 2 suggestions, got %d", len(suggs))
	}
	for i := 1; i < len(suggs); i++ {
		if suggs[i-1].Severity == "info" && suggs[i].Severity == "warning" {
			t.Error("warning should come before info")
		}
	}
}

// sanity check import

// ---------------------------------------------------------------------------
// Integration tests: full call chain handleContext → ContextBreakdown → render
// ---------------------------------------------------------------------------

// newAppWithEngineState creates an App backed by a real Engine with configured
// state (system prompt, messages, tools, ContextTokens) so ContextBreakdown
// produces meaningful output.
func newAppWithEngineState(t *testing.T) *App {
	t.Helper()
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("create CLAUDE.md: %v", err)
	}
	eng := engine.New(&engine.Params{
		Model:       "claude-test",
		MaxTokens:   16000,
		TokenBudget: 200000,
		AutoCompact: engine.AutoCompactConfig{ContextWindow: 200000},
		WorkingDir:  tmpDir,
		Logger:      slog.Default(),
		MCPRegistry: mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{}),
	})
	eng.SetSystemPrompt("You are a test assistant.")
	eng.SetSkillListing("commit: create a commit")
	eng.SetAgentDefs([]*types.AgentDefinition{
		{AgentType: "General", WhenToUse: "general tasks"},
		{AgentType: "Explore", WhenToUse: "codebase exploration"},
	})

	h := hub.NewHub()
	app := NewApp(eng, "test", h)
	app.history = NewHistory("")
	app.width = 80
	return app
}

// TestHandleContext_FullChain_ContextOutput verifies the complete path:
// handleContext → engine.ContextBreakdown → renderContextView → observable output.
// This catches integration bugs that unit tests miss (e.g. nil pointer from
// missing engine state, wrong field propagation, rendering crash on real data).
func TestHandleContext_FullChain_ContextOutput(t *testing.T) {
	app := newAppWithEngineState(t)

	// Simulate state after at least one API response.
	app.engine.SetContextTokens(48_000)
	app.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Read the main.go file"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "I'll read that file for you."},
			{Type: types.ContentTypeToolUse, ID: "t1", Name: "Read", Input: json.RawMessage(`{"path":"main.go"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"package main\nfunc main() {}"}]`)},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "The file contains a simple main function."},
		}},
	})

	cmd := app.handleContext("", nil)
	if cmd != nil {
		t.Fatal("handleContext should return nil cmd (sets infoOverlay instead)")
	}
	output := app.infoOverlay
	if output == "" {
		t.Fatal("infoOverlay should be set after handleContext")
	}

	// Verify observable output contains key sections a user would see.
	for _, want := range []string{
		"Context Usage",
		"tokens",
		"System prompt",
		"Messages",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, output)
		}
	}
	// Must NOT show the empty-state message.
	if strings.Contains(output, "Send a message first") {
		t.Error("should not show empty-state message when ContextTokens > 0")
	}
}

// TestHandleContext_ColdStart_NoData verifies that on a fresh engine with no
// API responses, /context returns the empty-state hint rather than crashing.
func TestHandleContext_ColdStart_NoData(t *testing.T) {
	app := newAppWithEngineState(t)
	// Don't set ContextTokens or messages — cold start state.

	cmd := app.handleContext("", nil)
	if cmd == nil {
		t.Fatal("handleContext returned nil cmd")
	}
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want infoMsg", msg)
	}
	if !strings.Contains(string(info), "Send a message first") {
		t.Errorf("cold start should show empty-state hint, got: %q", string(info))
	}
}

// TestHandleContext_StreamingBlocked verifies that /context is rejected during
// active streaming.
func TestHandleContext_StreamingBlocked(t *testing.T) {
	app := newAppWithEngineState(t)
	app.engine.SetContextTokens(48_000)

	// Simulate streaming state.
	app.repl.streaming = true

	cmd := app.handleContext("", nil)
	if cmd == nil {
		t.Fatal("handleContext returned nil cmd during streaming")
	}
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want infoMsg", msg)
	}
	if !strings.Contains(string(info), "Cannot show context while streaming") {
		t.Errorf("streaming guard should block, got: %q", string(info))
	}
}

// TestHandleContext_PersistRestoreRoundTrip verifies that after persisting
// ContextTokens to the store and restoring it on a new engine instance
// (simulating restart), /context produces output instead of the empty-state hint.
// Messages are set on eng2 directly because SetMessages doesn't persist to
// the store — this test focuses on the ContextTokens → ContextBreakdown pipeline.
func TestHandleContext_PersistRestoreRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("create CLAUDE.md: %v", err)
	}

	// Phase 1: engine with store, simulate API response, persist.
	store := newTestStoreForTUI(t)
	eng1 := engine.New(&engine.Params{
		Model:       "claude-test",
		MaxTokens:   16000,
		TokenBudget: 200000,
		AutoCompact: engine.AutoCompactConfig{ContextWindow: 200000},
		WorkingDir:  tmpDir,
		Logger:      slog.Default(),
		MCPRegistry: mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{}),
	})
	eng1.SetSystemPrompt("You are a test assistant.")
	eng1.SetStore(store, tmpDir)
	sid, err := eng1.ResumeOrInitSession(tmpDir, "claude-test")
	if err != nil {
		t.Fatalf("eng1 ResumeOrInitSession: %v", err)
	}

	eng1.SetContextTokens(60_000)
	eng1.PersistContextTokens()

	ses, err := store.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.ContextTokens != 60_000 {
		t.Fatalf("persisted ContextTokens = %d, want 60000", ses.ContextTokens)
	}

	// Phase 2: new engine + new session (simulates restart). Set ContextTokens
	// directly to simulate what ResumeOrInitSession restores from the store.
	eng2 := engine.New(&engine.Params{
		Model:       "claude-test",
		MaxTokens:   16000,
		TokenBudget: 200000,
		AutoCompact: engine.AutoCompactConfig{ContextWindow: 200000},
		WorkingDir:  tmpDir,
		Logger:      slog.Default(),
		MCPRegistry: mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{}),
	})
	eng2.SetSystemPrompt("You are a test assistant.")
	eng2.SetContextTokens(60_000)
	eng2.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hello"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hi there"},
		}},
	})

	// Phase 3: /context on restored engine should show data.
	h := hub.NewHub()
	app := NewApp(eng2, "test", h)
	app.history = NewHistory("")
	app.width = 80

	cmd := app.handleContext("", nil)
	if cmd != nil {
		t.Fatal("handleContext should return nil cmd (sets infoOverlay instead)")
	}
	output := app.infoOverlay
	if output == "" {
		t.Fatal("infoOverlay should be set after handleContext")
	}
	if strings.Contains(output, "Send a message first") {
		t.Errorf("restart should restore context data, got empty-state hint")
	}
	if !strings.Contains(output, "Context Usage") {
		t.Errorf("output missing 'Context Usage' header\nfull:\n%s", output)
	}
}

func newTestStoreForTUI(t *testing.T) *short.Store {
	t.Helper()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// -----------------------------------------------------------------------
// /context dump tests
// -----------------------------------------------------------------------

// TestHandleContext_Dump_WritesFile verifies /context dump writes to project dir.
func TestHandleContext_Dump_WritesFile(t *testing.T) {
	app := newAppWithEngineState(t)
	tmpDir := t.TempDir()
	app.projectDir = tmpDir
	app.engine.SetContextTokens(48_000)
	app.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hello"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Hi there"},
		}},
	})

	cmd := app.handleContext("dump", nil)
	if cmd == nil {
		t.Fatal("handleContext dump should return a cmd (showInfo)")
	}
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want infoMsg", msg)
	}
	dumpPath := filepath.Join(tmpDir, "gbot-context.txt")
	if !strings.Contains(string(info), dumpPath) {
		t.Errorf("expected path %q in message, got: %q", dumpPath, string(info))
	}

	// Verify file was written.
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("failed to read dump file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "=== System Prompt ===") {
		t.Error("dump missing System Prompt section")
	}
	if !strings.Contains(content, "=== Messages") {
		t.Error("dump missing Messages section")
	}
	if !strings.Contains(content, "=== Tools") {
		t.Error("dump missing Tools section")
	}
	if !strings.Contains(content, "Hello") {
		t.Error("dump missing user message content")
	}
	if !strings.Contains(content, "Hi there") {
		t.Error("dump missing assistant message content")
	}
}

// TestHandleContext_Dump_ContainsTools verifies tool definitions appear in dump.
func TestHandleContext_Dump_ContainsTools(t *testing.T) {
	app := newAppWithEngineState(t)
	tmpDir := t.TempDir()
	app.projectDir = tmpDir
	app.engine.SetContextTokens(48_000)
	app.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "test"},
		}},
	})

	app.handleContext("dump", nil)
	data, err := os.ReadFile(filepath.Join(tmpDir, "gbot-context.txt"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	content := string(data)
	// Tools section header should always appear (even with 0 tools).
	if !strings.Contains(content, "=== Tools") {
		t.Error("dump missing Tools section header")
	}
}

// TestHandleContext_Dump_ToolUseAndResult verifies tool_use and tool_result rendering.
func TestHandleContext_Dump_ToolUseAndResult(t *testing.T) {
	app := newAppWithEngineState(t)
	tmpDir := t.TempDir()
	app.projectDir = tmpDir
	app.engine.SetContextTokens(48_000)
	app.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Read main.go"},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "t1", Name: "Read", Input: json.RawMessage(`{"file_path":"main.go"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"package main"}]`)},
		}},
	})

	app.handleContext("dump", nil)
	data, err := os.ReadFile(filepath.Join(tmpDir, "gbot-context.txt"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[tool_use: Read]") {
		t.Error("dump missing tool_use block")
	}
	if !strings.Contains(content, "package main") {
		t.Error("dump missing tool_result content")
	}
}

// TestHandleContext_UnknownArgs verifies unknown sub-commands produce an error.
func TestHandleContext_UnknownArgs(t *testing.T) {
	app := newAppWithEngineState(t)
	app.engine.SetContextTokens(48_000)

	cmd := app.handleContext("badarg", nil)
	if cmd == nil {
		t.Fatal("handleContext with bad args should return a cmd")
	}
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want infoMsg", msg)
	}
	if !strings.Contains(string(info), "Unknown") {
		t.Errorf("expected 'Unknown' in message, got: %q", string(info))
	}
}
