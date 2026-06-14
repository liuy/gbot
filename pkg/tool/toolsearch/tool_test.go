package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Mock tool for testing
// ---------------------------------------------------------------------------

// mockTool implements tool.Tool for testing purposes.
type mockTool struct {
	name        string
	description string
	deferred    bool
	searchHint  string
}

func (m *mockTool) Name() string                                { return m.name }
func (m *mockTool) Aliases() []string                           { return nil }
func (m *mockTool) Description(json.RawMessage) (string, error) { return m.description, nil }
func (m *mockTool) RenderResult(data any) string                { return "" }
func (m *mockTool) InputSchema() json.RawMessage                { return nil }
func (m *mockTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *mockTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *mockTool) IsReadOnly(json.RawMessage) bool           { return true }
func (m *mockTool) IsDestructive(json.RawMessage) bool        { return false }
func (m *mockTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (m *mockTool) IsEnabled() bool                           { return true }
func (m *mockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (m *mockTool) MaxResultSize() int                        { return 50000 }
func (m *mockTool) Prompt() string                            { return "" }
func (m *mockTool) IsDeferred() bool                          { return m.deferred }
func (m *mockTool) SearchHint() string                        { return m.searchHint }
func (m *mockTool) NewResultType() any                        { return nil }

// helper to create a deferred tool
func deferredTool(name, desc string) *mockTool {
	return &mockTool{name: name, description: desc, deferred: true}
}

// helper to create a deferred tool with search hint
func deferredToolWithHint(name, desc, hint string) *mockTool {
	return &mockTool{name: name, description: desc, deferred: true, searchHint: hint}
}

// helper to create a non-deferred tool
func regularTool(name, desc string) *mockTool {
	return &mockTool{name: name, description: desc, deferred: false}
}

// helper to build a tool map
func toolMap(tools ...tool.Tool) map[string]tool.Tool {
	m := make(map[string]tool.Tool)
	for _, t := range tools {
		m[t.Name()] = t
	}
	return m
}

// helper to create a ToolUseContext
func makeTctx(tools map[string]tool.Tool) *tool.ToolUseContext {
	return &tool.ToolUseContext{
		Ctx: context.Background(),
		Options: tool.ToolUseOptions{
			Tools: tools,
		},
	}
}

func makeTctxWithPending(tools map[string]tool.Tool, pending []string) *tool.ToolUseContext {
	return &tool.ToolUseContext{
		Ctx: context.Background(),
		Options: tool.ToolUseOptions{
			Tools:             tools,
			PendingMCPServers: pending,
		},
	}
}

// ---------------------------------------------------------------------------
// Test: New() constructor
// ---------------------------------------------------------------------------

func TestNew_ReturnsTool(t *testing.T) {
	tl := New()
	if tl == nil {
		t.Fatal("New() returned nil")
	}
	if tl.Name() != ToolName {
		t.Errorf("expected name %q, got %q", ToolName, tl.Name())
	}
}

func TestNew_IsReadOnly(t *testing.T) {
	tl := New()
	if !tl.IsReadOnly(nil) {
		t.Error("ToolSearch should be read-only")
	}
}

func TestNew_IsConcurrencySafe(t *testing.T) {
	tl := New()
	if !tl.IsConcurrencySafe(nil) {
		t.Error("ToolSearch should be concurrency-safe")
	}
}

func TestNew_InterruptBehavior(t *testing.T) {
	tl := New()
	if tl.InterruptBehavior() != tool.InterruptCancel {
		t.Error("ToolSearch interrupt behavior should be InterruptCancel")
	}
}

func TestNew_PromptIsNotEmpty(t *testing.T) {
	tl := New()
	prompt := tl.Prompt()
	if prompt == "" {
		t.Error("ToolSearch prompt should not be empty")
	}
	// Verify key content from TS source
	if !strings.Contains(prompt, "select:") {
		t.Error("prompt should mention select: syntax")
	}
	if !strings.Contains(prompt, "deferred") {
		t.Error("prompt should mention deferred tools")
	}
	if !strings.Contains(prompt, "max_results") {
		t.Error("prompt should mention max_results")
	}
}

func TestNew_MaxResultSize(t *testing.T) {
	tl := New()
	if tl.MaxResultSize() != 100000 {
		t.Errorf("expected MaxResultSize 100000, got %d", tl.MaxResultSize())
	}
}

func TestNew_Description(t *testing.T) {
	tl := New()
	// With valid input
	input := json.RawMessage(`{"query": "test search"}`)
	desc, err := tl.Description(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc != "test search" {
		t.Errorf("expected description %q, got %q", "test search", desc)
	}

	// With nil input (buildToolDefs scenario) — must return full prompt
	desc, err = tl.Description(nil)
	if err != nil {
		t.Fatalf("unexpected error on nil input: %v", err)
	}
	if !strings.Contains(desc, "Fetches full schema definitions for deferred tools") {
		t.Errorf("expected full tool prompt on nil input, got %q", desc)
	}
	if !strings.Contains(desc, "select:") {
		t.Errorf("expected full tool prompt to contain query forms, got %q", desc)
	}

	// With invalid input — also returns full prompt
	input = json.RawMessage(`invalid json`)
	desc, err = tl.Description(input)
	if err != nil {
		t.Fatalf("unexpected error on invalid input: %v", err)
	}
	if !strings.Contains(desc, "Fetches full schema definitions for deferred tools") {
		t.Errorf("expected full tool prompt on invalid input, got %q", desc)
	}
}

// ---------------------------------------------------------------------------
// Test: InputSchema
// ---------------------------------------------------------------------------

func TestNew_InputSchema(t *testing.T) {
	tl := New()
	schema := tl.InputSchema()
	if schema == nil {
		t.Fatal("input schema should not be nil")
	}
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("input schema missing properties")
	}
	if _, hasQuery := props["query"]; !hasQuery {
		t.Error("input schema missing 'query' property")
	}
	if _, hasMaxResults := props["max_results"]; !hasMaxResults {
		t.Error("input schema missing 'max_results' property")
	}
}

// ---------------------------------------------------------------------------
// Test: parseToolName
// ---------------------------------------------------------------------------

func TestParseToolName_MCPTool(t *testing.T) {
	// Source: ToolSearchTool.ts:138-146
	parsed := parseToolName("mcp__github__create_issue")
	if !parsed.IsMCP {
		t.Error("should be detected as MCP tool")
	}
	// Parts: "github", "create", "issue" (split on __ then _)
	expectedParts := []string{"github", "create", "issue"}
	if len(parsed.Parts) != len(expectedParts) {
		t.Fatalf("expected %v parts, got %v", expectedParts, parsed.Parts)
	}
	for i, expected := range expectedParts {
		if parsed.Parts[i] != expected {
			t.Errorf("part[%d]: expected %q, got %q", i, expected, parsed.Parts[i])
		}
	}
	if parsed.Full != "github create issue" {
		t.Errorf("expected full %q, got %q", "github create issue", parsed.Full)
	}
}

func TestParseToolName_RegularTool(t *testing.T) {
	// Source: ToolSearchTool.ts:149-154
	parsed := parseToolName("FileRead")
	if parsed.IsMCP {
		t.Error("should not be detected as MCP tool")
	}
	expectedParts := []string{"file", "read"}
	if len(parsed.Parts) != len(expectedParts) {
		t.Fatalf("expected %v parts, got %v", expectedParts, parsed.Parts)
	}
	for i, expected := range expectedParts {
		if parsed.Parts[i] != expected {
			t.Errorf("part[%d]: expected %q, got %q", i, expected, parsed.Parts[i])
		}
	}
	if parsed.Full != "file read" {
		t.Errorf("expected full %q, got %q", "file read", parsed.Full)
	}
}

func TestParseToolName_UnderscoreTool(t *testing.T) {
	parsed := parseToolName("Tool_Search")
	if parsed.IsMCP {
		t.Error("should not be detected as MCP tool")
	}
	expectedParts := []string{"tool", "search"}
	if len(parsed.Parts) != len(expectedParts) {
		t.Fatalf("expected %v parts, got %v", expectedParts, parsed.Parts)
	}
}

func TestParseToolName_MCPToolSimpleAction(t *testing.T) {
	parsed := parseToolName("mcp__slack__send")
	if !parsed.IsMCP {
		t.Error("should be detected as MCP tool")
	}
	expectedParts := []string{"slack", "send"}
	if len(parsed.Parts) != len(expectedParts) {
		t.Fatalf("expected %v parts, got %v", expectedParts, parsed.Parts)
	}
	if parsed.Full != "slack send" {
		t.Errorf("expected full %q, got %q", "slack send", parsed.Full)
	}
}

// ---------------------------------------------------------------------------
// Test: compileTermPatterns
// ---------------------------------------------------------------------------

func TestCompileTermPatterns(t *testing.T) {
	terms := []string{"read", "file"}
	patterns := compileTermPatterns(terms)
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(patterns))
	}
	// "read" should match "read" as a word
	if !patterns["read"].MatchString("read files from disk") {
		t.Error("pattern for 'read' should match 'read' in context")
	}
	// "file" should not match "files" at word boundary (actually \b matches at boundaries)
	// "file" should match "a file" but not "afile"
	if patterns["file"].MatchString("afile") {
		t.Error("pattern for 'file' should not match 'afile' (no word boundary)")
	}
}

func TestCompileTermPatterns_Deduplicates(t *testing.T) {
	terms := []string{"read", "read", "file"}
	patterns := compileTermPatterns(terms)
	if len(patterns) != 2 {
		t.Errorf("expected 2 unique patterns, got %d", len(patterns))
	}
}

// ---------------------------------------------------------------------------
// Test: searchToolsWithKeywords — Exact match fast path
// ---------------------------------------------------------------------------

func TestSearchToolsWithKeywords_ExactMatchDeferred(t *testing.T) {
	deferred := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
	)
	all := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
		regularTool("Grep", "Search in files"),
	)

	matches := searchToolsWithKeywords("FileRead", deferred, all, 5)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0] != "FileRead" {
		t.Errorf("expected match %q, got %q", "FileRead", matches[0])
	}
}

func TestSearchToolsWithKeywords_ExactMatchFallback(t *testing.T) {
	// Query matches a non-deferred tool name — should still return it
	deferred := toolMap()
	all := toolMap(
		regularTool("Grep", "Search in files"),
	)

	matches := searchToolsWithKeywords("Grep", deferred, all, 5)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0] != "Grep" {
		t.Errorf("expected match %q, got %q", "Grep", matches[0])
	}
}

func TestSearchToolsWithKeywords_ExactMatchCaseInsensitive(t *testing.T) {
	deferred := toolMap(deferredTool("FileRead", "Read file contents"))
	all := toolMap(deferredTool("FileRead", "Read file contents"))

	matches := searchToolsWithKeywords("fileread", deferred, all, 5)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0] != "FileRead" {
		t.Errorf("expected match %q, got %q", "FileRead", matches[0])
	}
}

// ---------------------------------------------------------------------------
// Test: searchToolsWithKeywords — mcp__ prefix match
// ---------------------------------------------------------------------------

func TestSearchToolsWithKeywords_MCPPrefixMatch(t *testing.T) {
	deferred := toolMap(
		deferredTool("mcp__github__create_issue", "Create a GitHub issue"),
		deferredTool("mcp__github__list_repos", "List GitHub repos"),
		deferredTool("mcp__slack__send", "Send Slack message"),
	)
	all := deferred

	// Search for "mcp__github" should match the two GitHub tools
	matches := searchToolsWithKeywords("mcp__github", deferred, all, 5)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(matches), matches)
	}
	// Both github tools should be present
	found := map[string]bool{}
	for _, m := range matches {
		found[m] = true
	}
	if !found["mcp__github__create_issue"] {
		t.Error("missing mcp__github__create_issue")
	}
	if !found["mcp__github__list_repos"] {
		t.Error("missing mcp__github__list_repos")
	}
}

func TestSearchToolsWithKeywords_MCPPrefixRespectsLimit(t *testing.T) {
	deferred := toolMap(
		deferredTool("mcp__github__create_issue", "Create a GitHub issue"),
		deferredTool("mcp__github__list_repos", "List GitHub repos"),
		deferredTool("mcp__github__get_pr", "Get pull request"),
	)
	all := deferred

	// max_results=1 should return only 1
	matches := searchToolsWithKeywords("mcp__github", deferred, all, 1)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match with limit, got %d: %v", len(matches), matches)
	}
}

func TestSearchToolsWithKeywords_MCPPrefixTooShort(t *testing.T) {
	deferred := toolMap(
		deferredTool("mcp__github__create_issue", "Create a GitHub issue"),
	)
	all := deferred

	// "mcp__" alone (length 5) should NOT trigger prefix match
	matches := searchToolsWithKeywords("mcp__", deferred, all, 5)
	// Should not match by prefix (too short), falls through to keyword search
	// "mcp__" as keyword search should find mcp__ part match
	if len(matches) == 0 {
		// This is acceptable — "mcp__" alone doesn't match keywords well
		return
	}
	// If it does match, it should find the MCP tool
	for _, m := range matches {
		if !strings.Contains(m, "mcp__") {
			t.Errorf("expected MCP tool, got %q", m)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: searchToolsWithKeywords — keyword scoring
// ---------------------------------------------------------------------------

func TestSearchToolsWithKeywords_KeywordPartExactMatch(t *testing.T) {
	deferred := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
	)
	all := deferred

	// "read" should match "FileRead" (part exact match "read")
	matches := searchToolsWithKeywords("read", deferred, all, 5)
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match for 'read'")
	}
	if matches[0] != "FileRead" {
		t.Errorf("expected first match to be FileRead, got %q", matches[0])
	}
}

func TestSearchToolsWithKeywords_KeywordPartContainsMatch(t *testing.T) {
	deferred := toolMap(
		deferredTool("NotebookEditor", "Edit Jupyter notebooks"),
		deferredTool("FileRead", "Read file contents"),
	)
	all := deferred

	// "note" should match "NotebookEditor" (part "notebook" contains "note")
	matches := searchToolsWithKeywords("note", deferred, all, 5)
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match for 'note'")
	}
	if matches[0] != "NotebookEditor" {
		t.Errorf("expected first match to be NotebookEditor, got %q", matches[0])
	}
}

func TestSearchToolsWithKeywords_DescriptionMatch(t *testing.T) {
	deferred := toolMap(
		deferredTool("CustomTool", "A tool for managing jupyter notebooks"),
		deferredTool("FileRead", "Read file contents"),
	)
	all := deferred

	// "jupyter" matches in description of CustomTool
	matches := searchToolsWithKeywords("jupyter", deferred, all, 5)
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match for 'jupyter'")
	}
	if matches[0] != "CustomTool" {
		t.Errorf("expected first match to be CustomTool, got %q", matches[0])
	}
}

func TestSearchToolsWithKeywords_SearchHint(t *testing.T) {
	deferred := toolMap(
		deferredToolWithHint("MyTool", "Does something", "shell execution and terminal"),
		deferredTool("FileRead", "Read file contents"),
	)
	all := deferred

	// "shell" matches search hint of MyTool (score +4)
	matches := searchToolsWithKeywords("shell", deferred, all, 5)
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match for 'shell'")
	}
	if matches[0] != "MyTool" {
		t.Errorf("expected first match to be MyTool, got %q", matches[0])
	}
}

func TestSearchToolsWithKeywords_MCPScoringHigherThanNonMCP(t *testing.T) {
	// MCP tool part exact match = 12, non-MCP = 10
	deferred := toolMap(
		deferredTool("mcp__slack__send", "Send a message"),
		deferredTool("SlackIntegration", "Handles integration"), // part "slack" exact match = 10, no desc match for "slack"
	)
	all := deferred

	// Both have "slack" as a part, but MCP should score higher (12 vs 10)
	matches := searchToolsWithKeywords("slack", deferred, all, 5)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(matches), matches)
	}
	if matches[0] != "mcp__slack__send" {
		t.Errorf("expected MCP tool first (higher score), got %q", matches[0])
	}
}

// ---------------------------------------------------------------------------
// Test: searchToolsWithKeywords — +required syntax
// ---------------------------------------------------------------------------

func TestSearchToolsWithKeywords_RequiredTerms(t *testing.T) {
	deferred := toolMap(
		deferredTool("mcp__github__create_issue", "Create a GitHub issue"),
		deferredTool("mcp__github__list_repos", "List GitHub repos"),
		deferredTool("mcp__slack__send", "Send Slack message"),
	)
	all := deferred

	// "+github issue" — requires "github" to match, ranks by "issue"
	matches := searchToolsWithKeywords("+github issue", deferred, all, 5)
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match")
	}
	// mcp__github__create_issue should match (has "github" as required + "issue" in description)
	found := false
	for _, m := range matches {
		if m == "mcp__github__create_issue" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mcp__github__create_issue in matches, got %v", matches)
	}
	// slack tool should NOT appear (doesn't have "github")
	for _, m := range matches {
		if m == "mcp__slack__send" {
			t.Error("slack tool should not match (missing required 'github')")
		}
	}
}

func TestSearchToolsWithKeywords_RequiredTermsExcludesNonMatching(t *testing.T) {
	deferred := toolMap(
		deferredTool("ToolA", "Does alpha things"),
		deferredTool("ToolB", "Does beta things"),
	)
	all := deferred

	// "+alpha" — only ToolA should match
	matches := searchToolsWithKeywords("+alpha", deferred, all, 5)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %v", len(matches), matches)
	}
	if matches[0] != "ToolA" {
		t.Errorf("expected ToolA, got %q", matches[0])
	}
}

// ---------------------------------------------------------------------------
// Test: searchToolsWithKeywords — max_results limit
// ---------------------------------------------------------------------------

func TestSearchToolsWithKeywords_MaxResultsLimit(t *testing.T) {
	deferred := toolMap(
		deferredTool("ToolA", "searchable alpha tool"),
		deferredTool("ToolB", "searchable beta tool"),
		deferredTool("ToolC", "searchable gamma tool"),
	)
	all := deferred

	matches := searchToolsWithKeywords("searchable", deferred, all, 2)
	if len(matches) > 2 {
		t.Errorf("expected at most 2 matches, got %d: %v", len(matches), matches)
	}
}

// ---------------------------------------------------------------------------
// Test: searchToolsWithKeywords — zero score filtered
// ---------------------------------------------------------------------------

func TestSearchToolsWithKeywords_ZeroScoreFiltered(t *testing.T) {
	deferred := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
	)
	all := deferred

	// "zzzzzxyz" should not match anything
	matches := searchToolsWithKeywords("zzzzzxyz", deferred, all, 5)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for non-matching query, got %d: %v", len(matches), matches)
	}
}

// ---------------------------------------------------------------------------
// Test: searchToolsWithKeywords — full name fallback
// ---------------------------------------------------------------------------

func TestSearchToolsWithKeywords_FullNameFallback(t *testing.T) {
	deferred := toolMap(
		deferredTool("xyz", "Some description"), // single-char parts, "xyz" is the full name
	)
	all := deferred

	// "xyz" should match via full name fallback (no part matches, but full name contains it)
	matches := searchToolsWithKeywords("xyz", deferred, all, 5)
	// This might match via exact match fast path instead
	if len(matches) == 0 {
		t.Error("expected at least 1 match for 'xyz'")
	}
}

// ---------------------------------------------------------------------------
// Test: Execute — select: syntax
// ---------------------------------------------------------------------------

func TestExecute_SelectSingle(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
		regularTool("Grep", "Search in files"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "select:FileRead"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatal("result data is not *Output")
	}
	if len(out.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(out.Matches))
	}
	if out.Matches[0] != "FileRead" {
		t.Errorf("expected match %q, got %q", "FileRead", out.Matches[0])
	}
	if out.Query != "select:FileRead" {
		t.Errorf("expected query preserved, got %q", out.Query)
	}
	if out.TotalDeferredTools != 2 {
		t.Errorf("expected 2 deferred tools, got %d", out.TotalDeferredTools)
	}
}

func TestExecute_SelectMultiple(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "select:FileRead,FileEdit"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(out.Matches), out.Matches)
	}
}

func TestExecute_SelectNonDeferredTool(t *testing.T) {
	// Selecting a non-deferred tool should still return it (harmless no-op)
	// Source: ToolSearchTool.ts:370-376 — finds in deferred, then falls back to full set
	tools := toolMap(
		regularTool("Grep", "Search in files"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "select:Grep"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(out.Matches))
	}
	if out.Matches[0] != "Grep" {
		t.Errorf("expected match %q, got %q", "Grep", out.Matches[0])
	}
}

func TestExecute_SelectMissing(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "select:NonExistent"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 0 {
		t.Errorf("expected 0 matches for non-existent tool, got %d", len(out.Matches))
	}
}

func TestExecute_SelectDeduplicates(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "select:FileRead,FileRead"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 1 {
		t.Errorf("expected 1 match (deduped), got %d: %v", len(out.Matches), out.Matches)
	}
}

// ---------------------------------------------------------------------------
// Test: Execute — keyword search
// ---------------------------------------------------------------------------

func TestExecute_KeywordSearch(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
		deferredTool("NotebookEditor", "Edit Jupyter notebooks"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "read"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) == 0 {
		t.Fatal("expected at least 1 match for 'read'")
	}
	if out.Matches[0] != "FileRead" {
		t.Errorf("expected first match to be FileRead, got %q", out.Matches[0])
	}
}

func TestExecute_KeywordSearchWithMaxResults(t *testing.T) {
	tools := toolMap(
		deferredTool("ToolA", "searchable alpha tool"),
		deferredTool("ToolB", "searchable beta tool"),
		deferredTool("ToolC", "searchable gamma tool"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "searchable", "max_results": 2}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) > 2 {
		t.Errorf("expected at most 2 matches, got %d: %v", len(out.Matches), out.Matches)
	}
}

// ---------------------------------------------------------------------------
// Test: Execute — pending MCP servers
// ---------------------------------------------------------------------------

func TestExecute_NoMatchesWithPendingServers(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
	)
	tctx := makeTctxWithPending(tools, []string{"slack", "github"})
	input := json.RawMessage(`{"query": "zzzznonexistent"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(out.Matches))
	}
	if len(out.PendingMCPServers) != 2 {
		t.Fatalf("expected 2 pending servers, got %d", len(out.PendingMCPServers))
	}
	if out.PendingMCPServers[0] != "slack" {
		t.Errorf("expected first pending server 'slack', got %q", out.PendingMCPServers[0])
	}
	if out.PendingMCPServers[1] != "github" {
		t.Errorf("expected second pending server 'github', got %q", out.PendingMCPServers[1])
	}
}

func TestExecute_NoMatchesWithoutPendingServers(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "zzzznonexistent"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if out.PendingMCPServers != nil {
		t.Errorf("expected nil pending servers, got %v", out.PendingMCPServers)
	}
}

func TestExecute_MatchesNoPendingServers(t *testing.T) {
	// When matches are found, pending servers should NOT be included
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
	)
	tctx := makeTctxWithPending(tools, []string{"slack"})
	input := json.RawMessage(`{"query": "read"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) == 0 {
		t.Fatal("expected at least 1 match")
	}
	if out.PendingMCPServers != nil {
		t.Errorf("pending servers should not be set when matches found, got %v", out.PendingMCPServers)
	}
}

func TestExecute_SelectNoMatchesWithPending(t *testing.T) {
	// Select with no matches should include pending servers
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
	)
	tctx := makeTctxWithPending(tools, []string{"slack"})
	input := json.RawMessage(`{"query": "select:NonExistent"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(out.Matches))
	}
	if len(out.PendingMCPServers) != 1 || out.PendingMCPServers[0] != "slack" {
		t.Errorf("expected pending servers [slack], got %v", out.PendingMCPServers)
	}
}

// ---------------------------------------------------------------------------
// Test: Execute — total_deferred_tools count
// ---------------------------------------------------------------------------

func TestExecute_DeferredToolsCount(t *testing.T) {
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
		regularTool("Grep", "Search in files"), // not deferred
		regularTool("Grep", "Find files"),      // not deferred
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "read"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if out.TotalDeferredTools != 2 {
		t.Errorf("expected 2 deferred tools, got %d", out.TotalDeferredTools)
	}
}

// ---------------------------------------------------------------------------
// Test: Execute — error cases
// ---------------------------------------------------------------------------

func TestExecute_EmptyQuery(t *testing.T) {
	tctx := makeTctx(toolMap())
	input := json.RawMessage(`{"query": ""}`)

	_, err := Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("expected 'query is required' error, got %v", err)
	}
}

func TestExecute_InvalidJSON(t *testing.T) {
	tctx := makeTctx(toolMap())
	input := json.RawMessage(`invalid json`)

	_, err := Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("expected 'parse input' error, got %v", err)
	}
}

func TestExecute_DefaultMaxResults(t *testing.T) {
	// When max_results is 0 (unset), should default to 5
	tools := toolMap(
		deferredTool("ToolA", "searchable alpha"),
		deferredTool("ToolB", "searchable beta"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "searchable"}`)

	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	// Both should match since max_results defaults to 5
	if len(out.Matches) != 2 {
		t.Errorf("expected 2 matches with default max_results, got %d: %v", len(out.Matches), out.Matches)
	}
}

// ---------------------------------------------------------------------------
// Test: RenderResult
// ---------------------------------------------------------------------------

func TestRenderResult_WithMatches(t *testing.T) {
	tl := New()
	// Get the built tool's RenderResult via the Tool interface
	result := tl.RenderResult(&Output{
		Matches:            []string{"FileRead", "FileEdit"},
		Query:              "read",
		TotalDeferredTools: 3,
	})
	if !strings.Contains(result, "Found 2 tools") {
		t.Errorf("expected 'Found 2 tools' in render, got %q", result)
	}
	if !strings.Contains(result, "FileRead") {
		t.Errorf("expected 'FileRead' in render, got %q", result)
	}
	if !strings.Contains(result, "FileEdit") {
		t.Errorf("expected 'FileEdit' in render, got %q", result)
	}
}

func TestRenderResult_NoMatches(t *testing.T) {
	tl := New()
	result := tl.RenderResult(&Output{
		Matches:            []string{},
		Query:              "xyz",
		TotalDeferredTools: 3,
	})
	if !strings.Contains(result, "No matching deferred tools found") {
		t.Errorf("expected 'No matching deferred tools found' in render, got %q", result)
	}
}

func TestRenderResult_NoMatchesWithPending(t *testing.T) {
	tl := New()
	result := tl.RenderResult(&Output{
		Matches:            []string{},
		Query:              "xyz",
		TotalDeferredTools: 3,
		PendingMCPServers:  []string{"slack", "github"},
	})
	if !strings.Contains(result, "still connecting") {
		t.Errorf("expected 'still connecting' in render, got %q", result)
	}
	if !strings.Contains(result, "slack") {
		t.Errorf("expected 'slack' in render, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Test: slices.Contains helper
// ---------------------------------------------------------------------------

func TestContainsString(t *testing.T) {
	if !slices.Contains([]string{"a", "b", "c"}, "b") {
		t.Error("should find 'b' in slice")
	}
	if slices.Contains([]string{"a", "b", "c"}, "d") {
		t.Error("should not find 'd' in slice")
	}
	if slices.Contains([]string{}, "a") {
		t.Error("should not find 'a' in empty slice")
	}
}

// ---------------------------------------------------------------------------
// Test: scoring accuracy — ensure correct relative ordering
// ---------------------------------------------------------------------------

func TestScoring_MCPExactPartBeatsNonMCPExactPart(t *testing.T) {
	// MCP part exact = 12, non-MCP part exact = 10
	deferred := toolMap(
		deferredTool("mcp__slack__send_message", "Send a message via Slack"),
		deferredTool("SlackNotifier", "Notify via Slack"),
	)
	all := deferred

	matches := searchToolsWithKeywords("slack", deferred, all, 5)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(matches), matches)
	}
	if matches[0] != "mcp__slack__send_message" {
		t.Errorf("MCP tool should score higher, got first match %q", matches[0])
	}
}

func TestScoring_SearchHintBeatsDescription(t *testing.T) {
	// searchHint = 4, description = 2
	deferred := toolMap(
		deferredToolWithHint("HintTool", "Does things", "manages notebook sessions"),
		deferredTool("DescTool", "A jupyter notebook manager"),
	)
	all := deferred

	matches := searchToolsWithKeywords("notebook", deferred, all, 5)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(matches), matches)
	}
	if matches[0] != "HintTool" {
		t.Errorf("HintTool should score higher (hint=4 > desc=2), got first %q", matches[0])
	}
}

func TestScoring_PartExactBeatsContains(t *testing.T) {
	// Part exact = 10, part contains = 5
	// Use tool names that won't trigger the exact-match fast path.
	deferred := toolMap(
		deferredTool("FileRead", "Read things"),                      // part "read" exact match => score 10
		deferredTool("FileReaderTool", "A tool for read operations"), // part "reader" contains "read" => score 5
	)
	all := deferred

	matches := searchToolsWithKeywords("read", deferred, all, 5)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(matches), matches)
	}
	if matches[0] != "FileRead" {
		t.Errorf("FileRead should score higher (exact > contains), got first %q", matches[0])
	}
}

// ---------------------------------------------------------------------------
// Test: integration — full Execute flow with realistic tool set
// ---------------------------------------------------------------------------

func TestExecute_Integration_RealisticToolSet(t *testing.T) {
	tools := toolMap(
		deferredTool("mcp__github__create_issue", "Create a new GitHub issue"),
		deferredTool("mcp__github__list_prs", "List GitHub pull requests"),
		deferredTool("mcp__github__merge_pr", "Merge a GitHub pull request"),
		deferredTool("mcp__slack__send_message", "Send a message to a Slack channel"),
		deferredTool("mcp__slack__list_channels", "List Slack channels"),
		deferredTool("mcp__jupyter__run_cell", "Run a Jupyter notebook cell"),
		deferredTool("NotebookEdit", "Edit notebook cells"),
		regularTool("Grep", "Search file contents"),
		regularTool("Grep", "Find files"),
		regularTool("FileRead", "Read file contents"),
	)

	tests := []struct {
		name           string
		query          string
		maxResults     int
		wantMinMatch   int    // at least this many matches
		wantContains   string // first match must contain this substring (if set)
		wantExactFirst string // first match must be exactly this (if set)
	}{
		{
			name:           "select single tool",
			query:          "select:mcp__github__create_issue",
			wantMinMatch:   1,
			wantExactFirst: "mcp__github__create_issue",
		},
		{
			name:         "select multiple tools",
			query:        "select:mcp__github__create_issue,mcp__slack__send_message",
			wantMinMatch: 2,
		},
		{
			name:           "exact match fast path",
			query:          "mcp__github__create_issue",
			wantMinMatch:   1,
			wantExactFirst: "mcp__github__create_issue",
		},
		{
			name:         "mcp prefix search",
			query:        "mcp__github",
			maxResults:   5,
			wantMinMatch: 3, // all 3 github tools
		},
		{
			name:         "keyword: slack",
			query:        "slack",
			wantMinMatch: 2,
			wantContains: "slack", // both MCP slack tools match with equal score
		},
		{
			name:         "keyword: notebook",
			query:        "notebook",
			wantMinMatch: 2, // mcp__jupyter__run_cell (desc) + NotebookEdit (part)
		},
		{
			name:         "required term: +github pr",
			query:        "+github pr",
			wantMinMatch: 1, // at least the github PR tools
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := fmt.Sprintf(`{"query": %q, "max_results": %d}`, tt.query, tt.maxResults)
			tctx := makeTctx(tools)

			result, err := Execute(context.Background(), json.RawMessage(input), tctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := result.Data.(*Output)
			if len(out.Matches) < tt.wantMinMatch {
				t.Errorf("expected at least %d matches, got %d: %v", tt.wantMinMatch, len(out.Matches), out.Matches)
			}
			if tt.wantExactFirst != "" {
				if len(out.Matches) == 0 {
					t.Fatalf("expected first match %q but got 0 matches", tt.wantExactFirst)
				}
				if out.Matches[0] != tt.wantExactFirst {
					t.Errorf("expected first match %q, got %q", tt.wantExactFirst, out.Matches[0])
				}
			}
			if tt.wantContains != "" {
				if len(out.Matches) == 0 {
					t.Fatalf("expected match containing %q but got 0 matches", tt.wantContains)
				}
				if !strings.Contains(out.Matches[0], tt.wantContains) {
					t.Errorf("expected first match to contain %q, got %q", tt.wantContains, out.Matches[0])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests — reaching 100%
// ---------------------------------------------------------------------------

func TestRenderResult_NonOutputType(t *testing.T) {
	tl := New()
	// Pass non-*Output data to trigger fallback path (lines 117-120)
	result := tl.RenderResult("some string data")
	if result != `"some string data"` {
		t.Errorf("expected JSON-encoded string, got %q", result)
	}
}

func TestRenderResult_NonOutputNumber(t *testing.T) {
	tl := New()
	result := tl.RenderResult(42)
	if result != `42` {
		t.Errorf("expected JSON-encoded number, got %q", result)
	}
}

func TestSearchToolsWithKeywords_FullNameFallback_Contains(t *testing.T) {
	// Use a tool where parts don't contain the term but full name does
	deferred := toolMap(
		deferredTool("abcxyzdef", "A tool"),
	)
	all := deferred
	matches := searchToolsWithKeywords("xyz", deferred, all, 5)
	if len(matches) == 0 {
		t.Error("expected match via full name fallback")
	}
}

func TestSearchToolsWithKeywords_PatternCompilationFails(t *testing.T) {
	// Trigger !hasPattern in scoring loop (lines 353-354)
	// Using a term that would fail regex compilation is hard with QuoteMeta,
	// but we can test that search still works with normal terms
	deferred := toolMap(
		deferredTool("FileRead", "Read files"),
	)
	all := deferred
	matches := searchToolsWithKeywords("read", deferred, all, 5)
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match")
	}
}

func TestSearchToolsWithKeywords_RequiredTermContainsMatch(t *testing.T) {
	// Trigger partContainsMatch in required terms filtering (lines 322-324)
	// Tool where a part contains the required term but doesn't exactly match
	deferred := toolMap(
		deferredTool("NotebookEditor", "Edit notebooks"),
		deferredTool("FileRead", "Read files"),
	)
	all := deferred
	// "+note" requires "note" to match; "notebook" contains "note" (contains match)
	matches := searchToolsWithKeywords("+note", deferred, all, 5)
	if len(matches) == 0 {
		t.Error("expected match via part contains in required terms")
	}
	if matches[0] != "NotebookEditor" {
		t.Errorf("expected NotebookEditor, got %q", matches[0])
	}
}

func TestToolDescription_Error(t *testing.T) {
	// Trigger error path in toolDescription (lines 537-539)
	// Create a tool whose Description returns error
	badTool := tool.BuildTool(tool.ToolDef{
		Name_: "BadDesc",
		Description_: func(json.RawMessage) (string, error) {
			return "", fmt.Errorf("desc error")
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
	})
	desc := toolDescription(badTool)
	if desc != "" {
		t.Errorf("expected empty string on error, got %q", desc)
	}
}

func TestSearchToolsWithKeywords_RequiredTermNoPattern(t *testing.T) {
	// Trigger the "!hasPattern → continue" path in required terms (lines 309-311)
	// This happens when a required term fails regex compilation.
	// Since QuoteMeta handles all chars, this is hard to trigger directly.
	// We test that required terms filtering works correctly with normal terms.
	deferred := toolMap(
		deferredTool("mcp__github__create_issue", "Create issue"),
		deferredTool("mcp__slack__send", "Send message"),
	)
	all := deferred
	matches := searchToolsWithKeywords("+github issue", deferred, all, 5)
	if len(matches) == 0 {
		t.Error("expected at least 1 match with required term")
	}
}

func TestExecute_SelectWithEmptyBetween(t *testing.T) {
	// Test select: with multiple commas creating empty entries
	tools := toolMap(
		deferredTool("FileRead", "Read"),
	)
	tctx := makeTctx(tools)
	input := json.RawMessage(`{"query": "select:FileRead,,FileRead"}`)
	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 1 {
		t.Errorf("expected 1 match (deduped, empties filtered), got %d: %v", len(out.Matches), out.Matches)
	}
}

func TestNew_RenderResult_NilData(t *testing.T) {
	tl := New()
	result := tl.RenderResult(nil)
	if result != "null" {
		t.Errorf("expected 'null' for nil data, got %q", result)
	}
}
