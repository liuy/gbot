package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func TestRenderViaTool_Found(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Glob": &mockRenderTool{},
	}
	got, ok := renderViaTool("Glob", json.RawMessage(`{"filenames":["a.go"]}`), tools)
	if !ok {
		t.Fatal("renderViaTool(Glob) returned ok=false, want true")
	}
	if got != "a.go" {
		t.Errorf("renderViaTool(Glob) = %q, want %q", got, "a.go")
	}
}

func TestRenderViaTool_NotFound(t *testing.T) {
	t.Parallel()
	_, ok := renderViaTool("Missing", nil, nil)
	if ok {
		t.Error("renderViaTool(Missing) returned ok=true, want false")
	}
}

func TestRenderToolOutput_DecodeResultPath(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Glob": &mockRenderTool{},
	}
	inner := `{"filenames":["a.go","b.go"]}`
	textJSON, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got := renderToolOutput("Glob", raw, tools)
	want := "a.go\nb.go"
	if got != want {
		t.Errorf("renderToolOutput = %q, want %q", got, want)
	}
}

func TestRenderToolOutput_DecodeErrorFallback(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Glob": &mockRenderTool{},
	}
	// Array form whose text content is not valid JSON for the tool.
	// The plain text passes through unchanged.
	raw := json.RawMessage(`[{"type":"text","text":"garbage"}]`)
	got := renderToolOutput("Glob", raw, tools)
	if got != "garbage" {
		t.Errorf("renderToolOutput plain-text passthrough = %q, want %q", got, "garbage")
	}
}

func TestRenderToolOutput_ErrorArrayForm(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{}
	raw := json.RawMessage(`[{"type":"text","text":"boom"}]`)
	got := renderToolOutput("Bash", raw, tools)
	if got != "boom" {
		t.Errorf("got %q, want %q", got, "boom")
	}
}

// TestRenderToolOutput_NoStringBranch proves the legacy string-form branch is
// gone. The input is the bare 7-byte JSON string literal "hello" (with the
// surrounding double-quote characters). With no string branch, the array
// parse fails and the function falls through to string(raw), returning the
// same 7 bytes back. If the string branch were re-added, it would strip the
// duration prefix / try renderViaTool and return the 5-byte hello.
func TestRenderToolOutput_NoStringBranch(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{}
	raw := json.RawMessage(`"hello"`)
	got := renderToolOutput("Bash", raw, tools)
	if got != `"hello"` {
		t.Errorf("got %q (%d bytes), want %q (7 bytes — string(raw) passthrough)", got, len(got), `"hello"`)
	}
}

// TestRenderToolOutput_ArrayFormRenderViaTool verifies that the array-form
// path correctly calls renderViaTool when text looks like JSON.
func TestRenderToolOutput_ArrayFormRenderViaTool(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Glob": &mockRenderTool{},
	}
	inner := `{"filenames":["a.go","b.go"]}`
	textJSON, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got := renderToolOutput("Glob", raw, tools)
	want := "a.go\nb.go"
	if got != want {
		t.Errorf("renderToolOutput = %q, want %q", got, want)
	}
}

// TestRenderToolOutput_ArrayFormNoPrefix verifies the array-form path returns
// elapsed=0 when no duration prefix is present.
func TestRenderToolOutput_ArrayFormNoPrefix(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Glob": &mockRenderTool{},
	}
	arrayInput := json.RawMessage(`[{"type":"text","text":"hello world"}]`)
	got := renderToolOutput("Glob", arrayInput, tools)
	if got != "hello world" {
		t.Errorf("renderToolOutput = %q, want %q", got, "hello world")
	}
}

func TestReadPersistedFile_Valid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "output.json")
	content := `{"files":["a.go","b.go"]}`
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	input := "<persisted-output>Full output saved to: " + fp + "\nPreview (5 lines):"
	got := readPersistedFile(input)
	if got == nil {
		t.Fatal("readPersistedFile returned nil")
	}
	if string(got) != content {
		t.Errorf("readPersistedFile = %q, want %q", string(got), content)
	}
}

func TestReadPersistedFile_NoMarker(t *testing.T) {
	t.Parallel()
	got := readPersistedFile("no marker here")
	if got != nil {
		t.Errorf("readPersistedFile should return nil without marker, got %q", string(got))
	}
}

func TestReadPersistedFile_FileNotFound(t *testing.T) {
	t.Parallel()
	input := "<persisted-output>Full output saved to: /nonexistent/file.json\n"
	got := readPersistedFile(input)
	if got != nil {
		t.Errorf("readPersistedFile should return nil for missing file, got %q", string(got))
	}
}

func TestExtractPersistedPreview_WithPreview(t *testing.T) {
	t.Parallel()
	input := "<persisted-output>\nPreview (5 lines):\nline1\nline2\nline3\nline4\nline5\nmore content"
	got := extractPersistedPreview(input)
	if !strings.Contains(got, "line1") {
		t.Errorf("extractPersistedPreview = %q, should contain preview lines", got)
	}
	if strings.Contains(got, "more content") {
		t.Errorf("extractPersistedPreview should truncate after 5 lines, got %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("extractPersistedPreview should add ... after 5 lines, got %q", got)
	}
}

func TestExtractPersistedPreview_NoPreview(t *testing.T) {
	t.Parallel()
	got := extractPersistedPreview("no preview marker")
	if got != "<output saved to file>" {
		t.Errorf("extractPersistedPreview(no marker) = %q, want fallback", got)
	}
}

func TestExtractPersistedPreview_EmptyPreview(t *testing.T) {
	t.Parallel()
	input := "<persisted-output>\nPreview (0 lines):\n"
	got := extractPersistedPreview(input)
	if got != "<output saved to file>" {
		t.Errorf("extractPersistedPreview(empty) = %q, want fallback", got)
	}
}

func TestFormatToolDisplayName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Bash", "Bash"},
		{"Read", "Read"},
		{"mcp__fetch__get_raw_text", "fetch - get_raw_text (MCP)"},
		{"mcp__db__query", "db - query (MCP)"},
		{"mcp__incomplete", "mcp__incomplete"},
	}
	for _, tt := range tests {
		got := formatToolDisplayName(tt.name)
		if got != tt.want {
			t.Errorf("formatToolDisplayName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"你好世界", 2, "你好..."},
		{"abc", 3, "abc"},
	}
	for _, tt := range tests {
		got := truncateRunes(tt.input, tt.n)
		if got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

func TestAgentTypeName(t *testing.T) {
	tests := []struct {
		name string
		tc   *ToolCallView
		want string
	}{
		{"empty", &ToolCallView{}, ""},
		{"fork ignored", &ToolCallView{AgentType: "fork"}, ""},
		{"real agent", &ToolCallView{AgentType: "claude"}, "claude"},
		{"from logs", &ToolCallView{AgentLogs: []AgentLogEntry{{AgentType: "openai"}}}, "openai"},
	}
	for _, tt := range tests {
		got := agentTypeName(tt.tc)
		if got != tt.want {
			t.Errorf("agentTypeName(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestIndexOfOption(t *testing.T) {
	opts := []DialogOption{
		{Label: "allow", Shortcut: "y"},
		{Label: "deny", Shortcut: "n"},
		{Label: "ask", Shortcut: "a"},
	}
	if got := indexOfOption(opts, opts[1]); got != 1 {
		t.Errorf("indexOfOption(deny) = %d, want 1", got)
	}
	if got := indexOfOption(opts, DialogOption{Label: "missing"}); got != 0 {
		t.Errorf("indexOfOption(missing) = %d, want 0", got)
	}
}

func TestSelectedIndex(t *testing.T) {
	opts := []DialogOption{{Label: "a"}, {Label: "b"}}
	tests := []struct {
		name    string
		aborted bool
		done    bool
		cursor  int
		want    int
	}{
		{"aborted returns -1", true, false, 0, -1},
		{"done with valid cursor", false, true, 1, 1},
		{"done with out-of-range cursor", false, true, 5, -1},
		{"not done returns -1", false, false, 0, -1},
	}
	for _, tt := range tests {
		d := &Dialog{aborted: tt.aborted, done: tt.done, cursor: tt.cursor, options: opts}
		got := d.SelectedIndex()
		if got != tt.want {
			t.Errorf("%s: SelectedIndex() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestExtractXMLField(t *testing.T) {
	if got := extractXMLField("<job-id>bg-1</job-id>", "job-id"); got != "bg-1" {
		t.Errorf("extractXMLField(job-id) = %q, want %q", got, "bg-1")
	}
	if got := extractXMLField("<name>test&apos;s &quot;data&quot;</name>", "name"); got != `test's "data"` {
		t.Errorf("extractXMLField(entities) = %q, want %q", got, `test's "data"`)
	}
	if got := extractXMLField("no tag here", "job-id"); got != "" {
		t.Errorf("extractXMLField(missing) = %q, want empty", got)
	}
	if got := extractXMLField("<tag>value", "tag"); got != "" {
		t.Errorf("extractXMLField(no close) = %q, want empty", got)
	}
}

func TestInsertString(t *testing.T) {
	input := &Input{value: []rune("hello"), cursor: 5}
	input.InsertString(" world")
	if got := string(input.value); got != "hello world" {
		t.Errorf("InsertString = %q, want %q", got, "hello world")
	}
	if input.cursor != 11 {
		t.Errorf("cursor = %d, want 11", input.cursor)
	}

	// Insert at beginning
	input2 := &Input{value: []rune("world"), cursor: 0}
	input2.InsertString("hello ")
	if got := string(input2.value); got != "hello world" {
		t.Errorf("InsertString(at 0) = %q, want %q", got, "hello world")
	}

	// Empty string
	input3 := &Input{value: []rune("abc"), cursor: 1}
	input3.InsertString("")
	if got := string(input3.value); got != "abc" {
		t.Errorf("InsertString(empty) = %q, want %q", got, "abc")
	}
}

func TestFormatToolInput(t *testing.T) {
	// Valid JSON pretty-printed
	got := formatToolInput(json.RawMessage(`{"command":"ls","cwd":"/tmp"}`))
	if !strings.Contains(got, `"command"`) || !strings.Contains(got, "  ") {
		t.Errorf("formatToolInput(valid) = %q, should be pretty-printed", got)
	}

	// Empty input
	if got := formatToolInput(nil); got != "" {
		t.Errorf("formatToolInput(nil) = %q, want empty", got)
	}

	// Invalid JSON — returns raw string
	raw := json.RawMessage(`not json`)
	got = formatToolInput(raw)
	if got != "not json" {
		t.Errorf("formatToolInput(invalid) = %q, want %q", got, "not json")
	}
}

func TestCtxStyle(t *testing.T) {
	// Verify all branches execute without panic and return a style
	// that contains the rendered text. ANSI color output depends on
	// terminal capabilities so we only check content preservation.

	for _, tc := range []struct {
		name  string
		used  int
		total int
		text  string
	}{
		{"green", 10, 100000, "test"},
		{"yellow", 80000, 100000, "warn"},
		{"red", 90000, 100000, "danger"},
		{"zero_total", 0, 0, "zero"},
	} {
		got := ctxStyle(tc.used, tc.total).Render(tc.text)
		if !strings.Contains(got, tc.text) {
			t.Errorf("ctxStyle(%s) = %q, should contain %q", tc.name, got, tc.text)
		}
	}
}

// ---------------------------------------------------------------------------
// renderAgentLogs — sub-agent progress display (integration tests)
// ---------------------------------------------------------------------------

func TestRenderAgentLogs_Empty(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{}
	out := renderAgentLogs(tcv, 80)
	if out != "" {
		t.Errorf("renderAgentLogs(empty) = %q, want empty", out)
	}
}

func TestRenderAgentLogs_ThinkingEntry(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{
			{ToolName: "Thinking", Done: true},
			{ToolName: "Read", Summary: "file.go", Done: true},
		},
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "Thinking...") {
		t.Errorf("renderAgentLogs should contain 'Thinking...', got %q", out)
	}
	if !strings.Contains(out, "Read") {
		t.Errorf("renderAgentLogs should contain 'Read', got %q", out)
	}
	if !strings.Contains(out, "file.go") {
		t.Errorf("renderAgentLogs should contain summary 'file.go', got %q", out)
	}
}

func TestRenderAgentLogs_RunningTool_HasTimer(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{
			{ToolName: "Bash", Summary: "npm test", Done: false, Elapsed: 3 * time.Second},
		},
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "Bash") {
		t.Errorf("renderAgentLogs should contain 'Bash', got %q", out)
	}
	if !strings.Contains(out, "(3s)") {
		t.Errorf("running tool should have elapsed timer '(3s)', got %q", out)
	}
	if !strings.Contains(out, "npm test") {
		t.Errorf("running tool should show summary, got %q", out)
	}
}

func TestRenderAgentLogs_CompletedTool_NoEllipsis(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{
			{ToolName: "Grep", Summary: "5 matches", Done: true},
		},
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "Grep") {
		t.Errorf("renderAgentLogs should contain 'Grep', got %q", out)
	}
	if !strings.Contains(out, "5 matches") {
		t.Errorf("completed tool should show summary, got %q", out)
	}
}

func TestRenderAgentLogs_Overflow(t *testing.T) {
	t.Parallel()
	// 7 entries → only last 5 visible + overflow indicator
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{
			{ToolName: "Tool1", Done: true},
			{ToolName: "Tool2", Done: true},
			{ToolName: "Tool3", Done: true},
			{ToolName: "Tool4", Done: true},
			{ToolName: "Tool5", Done: true},
			{ToolName: "Tool6", Done: true},
			{ToolName: "Tool7", Done: true},
		},
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	// Tool1 and Tool2 should be truncated
	if strings.Contains(out, "Tool1") {
		t.Errorf("overflow should hide Tool1, got %q", out)
	}
	if strings.Contains(out, "Tool2") {
		t.Errorf("overflow should hide Tool2, got %q", out)
	}
	// Tool3-7 should be visible
	if !strings.Contains(out, "Tool3") {
		t.Errorf("Tool3 should be visible, got %q", out)
	}
	// Overflow indicator
	if !strings.Contains(out, "+2 more") {
		t.Errorf("should show '+2 more' overflow, got %q", out)
	}
}

func TestRenderAgentLogs_ExactlyMaxVisible_NoOverflow(t *testing.T) {
	t.Parallel()
	logs := make([]AgentLogEntry, 5)
	for i := range logs {
		logs[i] = AgentLogEntry{ToolName: fmt.Sprintf("Tool%d", i+1), Done: true}
	}
	tcv := &ToolCallView{AgentLogs: logs}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if strings.Contains(out, "+") && strings.Contains(out, "more") {
		t.Errorf("exactly 5 entries should not show overflow, got %q", out)
	}
}

func TestRenderAgentLogs_StatsTokensOnly(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{
			{ToolName: "Read", Done: true},
		},
		TokensIn:  5000,
		TokensOut: 1200,
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "↑") || !strings.Contains(out, "↓") {
		t.Errorf("stats should show token arrows, got %q", out)
	}
}

func TestRenderAgentLogs_StatsWithContextSize(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs:     []AgentLogEntry{{ToolName: "Bash", Done: true}},
		TokensIn:      10000,
		TokensOut:     3000,
		ContextSize:   50000,
		ContextWindow: 200000,
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	// formatContextSize uses types.FormatTokenCount (estimation-based)
	if !strings.Contains(out, "/") {
		t.Errorf("stats should show context size as used/total, got %q", out)
	}
	if !strings.Contains(out, "k") {
		t.Errorf("stats should use k suffix for thousands, got %q", out)
	}
}

func TestRenderAgentLogs_StatsToolCount(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{{ToolName: "Glob", Done: true}},
		ToolCount: 3,
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "3 tools") {
		t.Errorf("stats should show '3 tools', got %q", out)
	}
}

func TestRenderAgentLogs_StatsToolCountOne(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{{ToolName: "Read", Done: true}},
		ToolCount: 1,
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "1 tool") {
		t.Errorf("stats should show singular '1 tool', got %q", out)
	}
	// Should NOT contain "1 tools"
	if strings.Contains(out, "1 tools") {
		t.Errorf("stats should use singular form, got %q", out)
	}
}

func TestRenderAgentLogs_StatsAllCombined(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs:     []AgentLogEntry{{ToolName: "Bash", Done: true}},
		TokensIn:      8000,
		TokensOut:     2000,
		ContextSize:   60000,
		ContextWindow: 200000,
		ToolCount:     5,
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "↑") || !strings.Contains(out, "↓") {
		t.Errorf("combined stats should show tokens, got %q", out)
	}
	if !strings.Contains(out, " · ") {
		t.Errorf("combined stats should use ' · ' separator, got %q", out)
	}
	if !strings.Contains(out, "5 tools") {
		t.Errorf("combined stats should show tool count, got %q", out)
	}
}

func TestRenderAgentLogs_NoSummary(t *testing.T) {
	t.Parallel()
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{
			{ToolName: "Write", Summary: "", Done: true},
		},
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if !strings.Contains(out, "Write") {
		t.Errorf("tool without summary should still show name, got %q", out)
	}
	// Should not have empty parentheses
	if strings.Contains(out, "()") {
		t.Errorf("tool without summary should not have empty parens, got %q", out)
	}
}

func TestRenderAgentLogs_LongSummaryTruncated(t *testing.T) {
	t.Parallel()
	longSummary := "this is a very long summary that should definitely be truncated because it exceeds thirty characters"
	tcv := &ToolCallView{
		AgentLogs: []AgentLogEntry{
			{ToolName: "Bash", Summary: longSummary, Done: true},
		},
	}
	out := stripANSI(renderAgentLogs(tcv, 80))
	if strings.Contains(out, longSummary) {
		t.Errorf("long summary should be truncated, got full text in %q", out)
	}
	if !strings.Contains(out, "this is a very long summary...") {
		t.Errorf("truncated summary expected 'this is a very long summary...', got %q", out)
	}
}

// globResult is the concrete type mockRenderTool decodes from JSON.
type globResult struct {
	Files []string `json:"filenames"`
}

// mockRenderTool is a minimal tool.Tool implementation for render tests.
type mockRenderTool struct{}

func (m *mockRenderTool) Name() string                                { return "Glob" }
func (m *mockRenderTool) Aliases() []string                           { return nil }
func (m *mockRenderTool) Description(json.RawMessage) (string, error) { return "", nil }
func (m *mockRenderTool) InputSchema() json.RawMessage                { return nil }
func (m *mockRenderTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *mockRenderTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *mockRenderTool) RenderResult(data any) string {
	out, ok := data.(*globResult)
	if !ok {
		return ""
	}
	if len(out.Files) == 0 {
		return ""
	}
	return strings.Join(out.Files, "\n")
}
func (m *mockRenderTool) DecodeResult(raw json.RawMessage) (any, error) {
	var out globResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (m *mockRenderTool) IsEnabled() bool                           { return true }
func (m *mockRenderTool) IsReadOnly(json.RawMessage) bool           { return true }
func (m *mockRenderTool) IsDestructive(json.RawMessage) bool        { return false }
func (m *mockRenderTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (m *mockRenderTool) InterruptBehavior() tool.InterruptBehavior { return 0 }
func (m *mockRenderTool) MaxResultSize() int                        { return 0 }
func (m *mockRenderTool) Prompt() string                            { return "" }
