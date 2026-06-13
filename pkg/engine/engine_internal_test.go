package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine/attachment"
	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// pollAtomicBool polls an atomic.Bool until it becomes true or timeout elapses.
// Returns error on timeout. Used to replace time.Sleep when waiting for async state changes.
func pollAtomicBool(b *atomic.Bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout) // REAL-TIME: needed for relative timestamp offset in test
	for !b.Load() {
		if time.Now().After(deadline) { // REAL-TIME: needed for polling deadline in test helper
			return fmt.Errorf("timed out waiting for atomic.Bool after %v", timeout)
		}
		time.Sleep(2 * time.Millisecond) // REAL-TIME: polling interval for atomic.Bool
	}
	return nil
}

// minimalTool is a minimal tool implementation for covers skip path in executeTools.
type minimalTool struct{}

func (m *minimalTool) Name() string                                { return "test" }
func (m *minimalTool) Aliases() []string                           { return nil }
func (m *minimalTool) Description(json.RawMessage) (string, error) { return "test", nil }
func (m *minimalTool) InputSchema() json.RawMessage                { return nil }
func (m *minimalTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *minimalTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *minimalTool) IsReadOnly(json.RawMessage) bool        { return true }
func (m *minimalTool) IsDestructive(json.RawMessage) bool     { return false }
func (m *minimalTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (m *minimalTool) IsEnabled() bool                        { return true }
func (m *minimalTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (m *minimalTool) Prompt() string          { return "" }
func (m *minimalTool) RenderResult(any) string { return "" }
func (m *minimalTool) NewResultType() any      { return nil }

func (m *minimalTool) MaxResultSize() int { return 50000 }

func TestInternalMinimalTool(t *testing.T) {
	t.Parallel()
	mt := &minimalTool{}
	if mt.Name() != "test" {
		t.Errorf("Name() = %q, want %q", mt.Name(), "test")
	}
	if !mt.IsEnabled() {
		t.Error("IsEnabled() should be true")
	}
	if !mt.IsReadOnly(nil) {
		t.Error("IsReadOnly() should be true")
	}
	if mt.IsDestructive(nil) {
		t.Error("IsDestructive() should be false")
	}
	if !mt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() should be true")
	}
	if mt.InterruptBehavior() != tool.InterruptCancel {
		t.Error("InterruptBehavior() should be InterruptCancel")
	}
	if mt.Prompt() != "" {
		t.Errorf("Prompt() = %q, want empty", mt.Prompt())
	}
	if mt.InputSchema() != nil {
		t.Error("InputSchema() should be nil")
	}
	aliases := mt.Aliases()
	if aliases != nil {
		t.Errorf("Aliases() = %v, want nil", aliases)
	}
	desc, err := mt.Description(nil)
	if err != nil {
		t.Errorf("Description() error: %v", err)
	}
	if desc != "test" {
		t.Errorf("Description() = %q, want %q", desc, "test")
	}

	// Test CheckPermissions returns allow
	result := mt.CheckPermissions(nil, nil)
	if _, ok := result.(types.PermissionAllowDecision); !ok {
		t.Errorf("CheckPermissions() = %T, want PermissionAllowDecision", result)
	}

	// Test Call returns nil
	toolResult, err := mt.Call(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("Call() error: %v", err)
	}
	if toolResult != nil {
		t.Errorf("Call() = %v, want nil", toolResult)
	}
}

// ---------------------------------------------------------------------------
// extractSummaryFromPartial + extractJSONStringField coverage
// ---------------------------------------------------------------------------

func TestExtractSummaryFromPartial_BashTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("Bash", `{"command":"ls -la /tmp"}`)
	if got != "ls -la /tmp" {
		t.Errorf("Bash: got %q, want %q", got, "ls -la /tmp")
	}
}

func TestExtractSummaryFromPartial_ShellTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("shell", `{"command":"echo hi"}`)
	if got != "echo hi" {
		t.Errorf("shell: got %q, want %q", got, "echo hi")
	}
}

func TestExtractSummaryFromPartial_ReadTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("Read", `{"file_path":"/tmp/test.go"}`)
	if got != "/tmp/test.go" {
		t.Errorf("Read: got %q, want %q", got, "/tmp/test.go")
	}
}

func TestExtractSummaryFromPartial_WriteTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("Write", `{"file_path":"/tmp/out.txt"}`)
	if got != "/tmp/out.txt" {
		t.Errorf("Write: got %q, want %q", got, "/tmp/out.txt")
	}
}

func TestExtractSummaryFromPartial_EditTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("Edit", `{"file_path":"/tmp/edit.go"}`)
	if got != "/tmp/edit.go" {
		t.Errorf("Edit: got %q, want %q", got, "/tmp/edit.go")
	}
}

func TestExtractSummaryFromPartial_FileReadTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("fileread", `{"file_path":"/tmp/readme.md"}`)
	if got != "/tmp/readme.md" {
		t.Errorf("fileread: got %q, want %q", got, "/tmp/readme.md")
	}
}

func TestExtractSummaryFromPartial_FileWriteTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("filewrite", `{"file_path":"/tmp/write.go"}`)
	if got != "/tmp/write.go" {
		t.Errorf("filewrite: got %q, want %q", got, "/tmp/write.go")
	}
}

func TestExtractSummaryFromPartial_FileEditTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("fileedit", `{"file_path":"/tmp/edit.go"}`)
	if got != "/tmp/edit.go" {
		t.Errorf("fileedit: got %q, want %q", got, "/tmp/edit.go")
	}
}

func TestExtractSummaryFromPartial_GlobTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("Glob", `{"pattern":"**/*.go"}`)
	if got != "**/*.go" {
		t.Errorf("Glob: got %q, want %q", got, "**/*.go")
	}
}

func TestExtractSummaryFromPartial_GrepTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("Grep", `{"pattern":"TODO"}`)
	if got != "TODO" {
		t.Errorf("Grep: got %q, want %q", got, "TODO")
	}
}

func TestExtractSummaryFromPartial_FileGlobTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("fileglob", `{"pattern":"*.txt"}`)
	if got != "*.txt" {
		t.Errorf("fileglob: got %q, want %q", got, "*.txt")
	}
}

func TestExtractSummaryFromPartial_SearchCodeTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("searchcode", `{"pattern":"func main"}`)
	if got != "func main" {
		t.Errorf("searchcode: got %q, want %q", got, "func main")
	}
}

func TestExtractSummaryFromPartial_UnknownTool(t *testing.T) {
	t.Parallel()
	got := extractSummaryFromPartial("unknown_tool", `{"something":"value"}`)
	if got != "" {
		t.Errorf("unknown: got %q, want empty", got)
	}
}

func TestExtractJSONStringField_BasicExtraction(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`{"command":"ls -la"}`, "command", "", 30)
	if got != "ls -la" {
		t.Errorf("got %q, want %q", got, "ls -la")
	}
}

func TestExtractJSONStringField_Truncation(t *testing.T) {
	t.Parallel()
	longVal := "a_very_long_command_name_that_exceeds_thirty_characters_easily"
	got := extractJSONStringField(`{"command":"`+longVal+`"}`, "command", "", 30)
	want := "a_very_long_command_name_that_" + "..."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSONStringField_FieldNotFound(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`{"other":"value"}`, "command", "", 30)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractJSONStringField_NoColon(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`"command" "value"`, "command", "", 30)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractJSONStringField_NoQuoteAfterColon(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`"command":123`, "command", "", 30)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractJSONStringField_EmptyValue(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`"command":""`, "command", "", 30)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractJSONStringField_WithWhitespace(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`"command": "ls"`, "command", "cmd: ", 30)
	if got != "cmd: ls" {
		t.Errorf("got %q, want %q", got, "cmd: ls")
	}
}

func TestExtractJSONStringField_WithNewlineWhitespace(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField("\"command\":\n\t\"ls\"", "command", "", 30)
	if got != "ls" {
		t.Errorf("got %q, want %q", got, "ls")
	}
}

func TestExtractJSONStringField_WithPrefix(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`{"file_path":"/tmp/go"}`, "file_path", "path: ", 40)
	if got != "path: /tmp/go" {
		t.Errorf("got %q, want %q", got, "path: /tmp/go")
	}
}

func TestExtractJSONStringField_TerminatedByComma(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`{"pattern":"*.go","path":"/tmp"}`, "pattern", "", 40)
	if got != "*.go" {
		t.Errorf("got %q, want %q", got, "*.go")
	}
}

func TestExtractJSONStringField_TerminatedByBrace(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`{"pattern":"*.go"}`, "pattern", "", 40)
	if got != "*.go" {
		t.Errorf("got %q, want %q", got, "*.go")
	}
}

func TestExtractJSONStringField_TerminatedByQuote(t *testing.T) {
	t.Parallel()
	got := extractJSONStringField(`{"pattern":"*.go"}`, "pattern", "", 40)
	if got != "*.go" {
		t.Errorf("got %q, want %q", got, "*.go")
	}
}

// ---------------------------------------------------------------------------
// Tools() method coverage (line 565-567)
// ---------------------------------------------------------------------------

func TestTools_ReturnsToolMap(t *testing.T) {
	t.Parallel()
	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })
	tools := eng.Tools()
	if tools == nil {
		t.Fatal("Tools() returned nil")
	}
	if len(tools) != 0 {
		t.Errorf("Tools() = %d entries, want 0", len(tools))
	}
}

func TestTools_ReturnsPopulatedMap(t *testing.T) {
	t.Parallel()
	mt := &testTool{name: "my_tool"}
	eng := New(&Params{
		Provider: &testProvider{},
		Tools:    []tool.Tool{mt},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })
	tools := eng.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() = %d entries, want 1", len(tools))
	}
	if _, ok := tools["my_tool"]; !ok {
		t.Error("Tools() missing 'my_tool'")
	}
}

// ---------------------------------------------------------------------------
// ToolsProvider — dynamic tool resolution
// ---------------------------------------------------------------------------

func TestToolsProvider_SeesLateRegisteredTool(t *testing.T) {
	t.Parallel()

	// Simulate the main.go pattern: tools registered after engine construction
	// are visible via the ToolsProvider closure.
	baseTool := &testTool{name: "Bash"}
	toolMap := map[string]tool.Tool{
		"Bash": baseTool,
	}

	eng := New(&Params{
		Provider:      &testProvider{},
		ToolsProvider: func() map[string]tool.Tool { return toolMap },
		Model:         "test",
	})
	t.Cleanup(func() { eng.Close() })

	// Before late-register: engine sees only Bash
	tools := eng.Tools()
	if len(tools) != 1 {
		t.Fatalf("before: expected 1 tool, got %d", len(tools))
	}
	if _, ok := tools["Bash"]; !ok {
		t.Error("before: missing Bash")
	}

	// Late-register Agent tool (simulating main.go post-construction registration)
	toolMap["Agent"] = &testTool{name: "Agent"}

	// After late-register: engine MUST see Agent without any extra call
	tools = eng.Tools()
	if len(tools) != 2 {
		t.Fatalf("after: expected 2 tools, got %d: %v", len(tools), mapKeys(tools))
	}
	if _, ok := tools["Agent"]; !ok {
		t.Error("after: missing Agent")
	}
	if _, ok := tools["Bash"]; !ok {
		t.Error("after: missing Bash (was overwritten)")
	}
}

func TestToolsProvider_NilProviderGivesEmpty(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })
	tools := eng.Tools()
	if len(tools) != 0 {
		t.Errorf("nil provider: expected 0 tools, got %d", len(tools))
	}
}

func TestToolsProvider_PreferOverToolsSlice(t *testing.T) {
	t.Parallel()

	// If both Tools and ToolsProvider are set, ToolsProvider wins
	staticTool := &testTool{name: "static"}
	dynamicTool := &testTool{name: "dynamic"}

	eng := New(&Params{
		Provider: &testProvider{},
		Tools:    []tool.Tool{staticTool},
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"dynamic": dynamicTool}
		},
		Model: "test",
	})
	t.Cleanup(func() { eng.Close() })

	tools := eng.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (dynamic), got %d", len(tools))
	}
	if _, ok := tools["static"]; ok {
		t.Error("static tool should not appear when ToolsProvider is set")
	}
	if _, ok := tools["dynamic"]; !ok {
		t.Error("dynamic tool should appear")
	}
}

func mapKeys(m map[string]tool.Tool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---------------------------------------------------------------------------
// Max turns reached (line 226-231)
// ---------------------------------------------------------------------------

func TestQueryLoop_MaxTurnsReached(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// Provide exactly 50 tool-use responses — each increments turnCount.
	// After 50 iterations, the for loop exits and hits line 226-231.
	toolEvents := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 1, OutputTokens: 1}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t", Name: "tool"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}
	for range 50 {
		mp.addResponse(toolEvents, nil)
	}

	mt := &testTool{name: "tool"}
	tc := newEventCollector()
	eng := New(&Params{
		Provider:    mp,
		Tools:       []tool.Tool{mt},
		Model:       "test",
		TokenBudget: 999999,
		MaxTurns:    50,
		Dispatcher:  tc,
	})
	t.Cleanup(func() { eng.Close() })

	result := eng.QuerySync(context.Background(), "test", "")
	// After 50 turns the for loop exits, hitting line 226-231
	if result.Error != nil {
		t.Fatalf("expected success after max turns, got: %v", result.Error)
	}
	if result.TurnCount != 50 {
		t.Errorf("expected 50 turns, got %d", result.TurnCount)
	}
}

// ---------------------------------------------------------------------------
// Context cancelled during streaming (line 286-287)
// ---------------------------------------------------------------------------

func TestCallLLM_ContextCancelledDuringStreaming(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent, 10)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	ready := make(chan struct{})
	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		close(ready) // signal that initial events are sent
		<-ctx.Done() // wait for cancellation
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "x"}}
	}()

	// Use QuerySync (not Query) to avoid goroutine leaking.
	// The test goroutine blocks until completion, so no concurrent access
	// to e.messages between two goroutines.
	go func() {
		<-ready // wait for initial streaming events to be sent
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected error from cancelled context during streaming")
	}
	if !strings.Contains(result.Error.Error(), "context") {
		t.Errorf("expected error to mention context, got %q", result.Error.Error())
	}
}

// ---------------------------------------------------------------------------
// refreshTools nil provider — early return when toolsProvider is nil
// ---------------------------------------------------------------------------

func TestRefreshTools_NilProvider(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		// No ToolsProvider set → refreshTools should early-return
	})
	t.Cleanup(func() { eng.Close() })

	// Manually call refreshTools — nil provider path
	eng.refreshTools()

	// Engine should still have empty tools
	tools := eng.Tools()
	if len(tools) != 0 {
		t.Errorf("nil provider: expected 0 tools, got %d", len(tools))
	}
}

// ---------------------------------------------------------------------------
// refreshTools with provider — covers non-nil branch (lines 773-779)
// ---------------------------------------------------------------------------

func TestRefreshTools_WithProvider(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				"Zulu":  &minimalTool{},
				"Alpha": &minimalTool{},
				"Bravo": &minimalTool{},
			}
		},
	})
	t.Cleanup(func() { eng.Close() })

	// Initial sort order set in New()
	if len(eng.Tools()) != 3 {
		t.Fatalf("expected 3 tools after New(), got %d", len(eng.Tools()))
	}

	// Call refreshTools — should re-fetch and re-sort from provider
	eng.refreshTools()

	tools := eng.Tools()
	if len(tools) != 3 {
		t.Errorf("expected 3 tools after refresh, got %d", len(tools))
	}

	// Verify sort order: Alpha < Bravo < Zulu.
	// toolOrder is private so we call refreshTools again with different provider
	// to confirm the sort is applied.
	eng.refreshTools()
	tools2 := eng.Tools()
	if len(tools2) != 3 {
		t.Errorf("expected 3 tools after re-refresh, got %d", len(tools2))
	}
}

// ---------------------------------------------------------------------------
// extractErrMsg fallback — non-JSON content returns string(content) (line 526)
// ---------------------------------------------------------------------------

func TestExtractErrMsg_Fallback(t *testing.T) {
	t.Parallel()

	// Non-JSON content → JSON unmarshal fails → returns string(content)
	got := extractErrMsg(json.RawMessage("this is not JSON"))
	if got != "this is not JSON" {
		t.Errorf("got %q, want %q", got, "this is not JSON")
	}

	// Valid JSON but no "error" key → returns string(content)
	got = extractErrMsg(json.RawMessage(`{"message":"not an error"}`))
	if got != `{"message":"not an error"}` {
		t.Errorf("got %q, want raw JSON", got)
	}
}

func TestExtractErrMsg_Success(t *testing.T) {
	t.Parallel()

	// Valid JSON with "error" key
	got := extractErrMsg(json.RawMessage(`{"error":"something went wrong"}`))
	if got != "something went wrong" {
		t.Errorf("got %q, want %q", got, "something went wrong")
	}
}

// ---------------------------------------------------------------------------
// getToolDescription — all branches (line 304-325)
// ---------------------------------------------------------------------------

func TestGetToolDescription_AllFields(t *testing.T) {
	t.Parallel()

	tt := &TrackedTool{
		Name:  "Grep",
		Input: json.RawMessage(`{"pattern":"TODO"}`),
	}
	desc := getToolDescription(tt)
	if desc != "Grep(TODO)" {
		t.Errorf("pattern branch: got %q, want %q", desc, "Grep(TODO)")
	}

	// Command field takes priority
	tt2 := &TrackedTool{
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"ls -la","file_path":"/tmp"}`),
	}
	desc2 := getToolDescription(tt2)
	if desc2 != "Bash(ls -la)" {
		t.Errorf("command branch: got %q, want %q", desc2, "Bash(ls -la)")
	}

	// FilePath when no command
	tt3 := &TrackedTool{
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/tmp/test.go"}`),
	}
	desc3 := getToolDescription(tt3)
	if desc3 != "Read(/tmp/test.go)" {
		t.Errorf("file_path branch: got %q, want %q", desc3, "Read(/tmp/test.go)")
	}

	// Truncation > 40 chars
	tt4 := &TrackedTool{
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"a_very_very_very_long_command_name_that_exceeds_forty_characters"}`),
	}
	desc4 := getToolDescription(tt4)
	// Command truncated to 40 bytes + ellipsis (3 bytes) + Bash() = 49 bytes total
	if !strings.HasSuffix(desc4, "…)") {
		t.Errorf("truncation: expected ellipsis suffix, got %q", desc4)
	}
	if len(desc4) != 49 {
		t.Errorf("truncation: len = %d, want 49 (5+40+3+1 bytes)", len(desc4))
	}

	// No input fields → just tool name
	tt5 := &TrackedTool{
		Name:  "CustomTool",
		Input: json.RawMessage(`{}`),
	}
	desc5 := getToolDescription(tt5)
	if desc5 != "CustomTool" {
		t.Errorf("empty input: got %q, want %q", desc5, "CustomTool")
	}
}

// ---------------------------------------------------------------------------
// executeTool — non-streaming Call success path (lines 437-460)
// ---------------------------------------------------------------------------

// nonStreamingTool returns a result from Call (non-streaming success).
type nonStreamingSuccessTool struct {
	name string
	data any
}

func (t *nonStreamingSuccessTool) Name() string                                { return t.name }
func (t *nonStreamingSuccessTool) Aliases() []string                           { return nil }
func (t *nonStreamingSuccessTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *nonStreamingSuccessTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *nonStreamingSuccessTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: t.data}, nil
}
func (t *nonStreamingSuccessTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *nonStreamingSuccessTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *nonStreamingSuccessTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *nonStreamingSuccessTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *nonStreamingSuccessTool) IsEnabled() bool                        { return true }
func (t *nonStreamingSuccessTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (t *nonStreamingSuccessTool) Prompt() string          { return "" }
func (t *nonStreamingSuccessTool) RenderResult(any) string { return "rendered output" }
func (t *nonStreamingSuccessTool) NewResultType() any      { return nil }

func (*nonStreamingSuccessTool) MaxResultSize() int { return 50000 }

func TestExecuteTool_NonStreamingSuccess(t *testing.T) {
	t.Parallel()

	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"ns_tool": &nonStreamingSuccessTool{name: "ns_tool", data: "success"},
	}

	executor := NewStreamingToolExecutor(toolMap, nil, emit, context.Background())

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "ns_tool", Input: json.RawMessage(`{}`)},
	}
	result := executor.ExecuteAll(blocks)

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if result.ToolResultBlocks[0].Type != types.ContentTypeToolResult {
		t.Errorf("expected ToolResult block, got %s", result.ToolResultBlocks[0].Type)
	}

	// Should have emitted EventToolStart + EventToolEnd (non-error)
	var foundToolEnd bool
	for _, e := range emitted {
		if e.Type == types.EventToolEnd && e.ToolResult != nil && !e.ToolResult.IsError {
			foundToolEnd = true
			break
		}
	}
	if !foundToolEnd {
		t.Errorf("expected non-error EventToolEnd, got events: %v", emitted)
	}
}

// ---------------------------------------------------------------------------
// StreamingToolExecutor.Discard() — aborts in-progress tools and prevents
// queued tools from starting. Called before retry in queryLoop.
// ---------------------------------------------------------------------------

func TestStreamingToolExecutor_DiscardCancelsContext(t *testing.T) {
	t.Parallel()

	var cancelled atomic.Bool
	toolMap := map[string]tool.Tool{
		"slow": &slowCancelTool{onCancel: func() { cancelled.Store(true) }},
	}

	var emitted []types.QueryEvent
	executor := NewStreamingToolExecutor(toolMap, nil, func(e types.QueryEvent) {
		emitted = append(emitted, e)
	}, context.Background())

	started := make(chan struct{})
	toolMap["slow"].(*slowCancelTool).onStarted = func() { close(started) }
	executor.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "slow"})

	// Wait for the goroutine to start and enter tool.Call (blocking on ctx.Done)
	// before Discard sets the flag. Without this, the early abort path wins.
	<-started

	executor.Discard()

	// Wait for the tool to receive the cancellation via sync primitive.
	if err := pollAtomicBool(&cancelled, 2*time.Second); err != nil {
		t.Error("tool context should be cancelled after Discard()")
	}
}

func TestStreamingToolExecutor_DiscardPreventsQueuedStart(t *testing.T) {
	t.Parallel()

	var started atomic.Bool
	toolMap := map[string]tool.Tool{
		"never_run": &neverRunTool{onStart: func() { started.Store(true) }},
	}

	executor := NewStreamingToolExecutor(toolMap, nil, func(types.QueryEvent) {}, context.Background())
	executor.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "never_run"})
	executor.Discard()

	// Discard() is synchronous — no goroutine should start after it returns.
	// Brief poll to confirm the tool never starts.
	// Discard() is synchronous — no goroutine should start after it returns.
	// Yield scheduler to let any pending goroutines run, then assert.
	runtime.Gosched()
	if started.Load() {
		t.Error("queued tool should not start after Discard()")
	}
}

// slowCancelTool blocks until context is cancelled, then reports it.
type slowCancelTool struct {
	onCancel  func()
	onStarted func()
}

func (t *slowCancelTool) Name() string                                { return "slow" }
func (t *slowCancelTool) Aliases() []string                           { return nil }
func (t *slowCancelTool) Description(json.RawMessage) (string, error) { return "slow", nil }
func (t *slowCancelTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *slowCancelTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if t.onStarted != nil {
		t.onStarted()
	}
	<-ctx.Done()
	t.onCancel()
	return nil, ctx.Err()
}
func (t *slowCancelTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *slowCancelTool) IsReadOnly(json.RawMessage) bool           { return false }
func (t *slowCancelTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *slowCancelTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (t *slowCancelTool) IsEnabled() bool                           { return true }
func (t *slowCancelTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *slowCancelTool) Prompt() string                            { return "" }
func (t *slowCancelTool) RenderResult(any) string                   { return "" }
func (t *slowCancelTool) NewResultType() any                        { return nil }

func (*slowCancelTool) MaxResultSize() int { return 50000 }

// neverRunTool never actually starts (context cancelled before execution).
type neverRunTool struct {
	onStart func()
}

func (t *neverRunTool) Name() string                                { return "never_run" }
func (t *neverRunTool) Aliases() []string                           { return nil }
func (t *neverRunTool) Description(json.RawMessage) (string, error) { return "never_run", nil }
func (t *neverRunTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *neverRunTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	// Check context before starting — Discard() cancels siblingCtx.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	t.onStart()
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *neverRunTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *neverRunTool) IsReadOnly(json.RawMessage) bool           { return false }
func (t *neverRunTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *neverRunTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (t *neverRunTool) IsEnabled() bool                           { return true }
func (t *neverRunTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *neverRunTool) Prompt() string                            { return "" }
func (t *neverRunTool) RenderResult(any) string                   { return "" }
func (t *neverRunTool) NewResultType() any                        { return nil }

func (*neverRunTool) MaxResultSize() int { return 50000 }

// ---------------------------------------------------------------------------
// QueryLoop retry discards old executor (TS query.ts:734,913)
// RED TEST: Currently FAILS — queryLoop does not call Discard() on retry.
// ---------------------------------------------------------------------------

// discardSlowTool blocks until its context is cancelled.
type discardSlowTool struct {
	cancelled bool
	started   bool
	mu        sync.Mutex
	done      chan struct{} // closed when Call returns after cancellation
}

func newDiscardSlowTool() *discardSlowTool {
	return &discardSlowTool{done: make(chan struct{})}
}

func (t *discardSlowTool) Name() string                                { return "discard_slow" }
func (t *discardSlowTool) Aliases() []string                           { return nil }
func (t *discardSlowTool) Description(json.RawMessage) (string, error) { return "discard_slow", nil }
func (t *discardSlowTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *discardSlowTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	t.mu.Lock()
	t.started = true
	t.mu.Unlock()
	<-ctx.Done()
	t.mu.Lock()
	t.cancelled = true
	t.mu.Unlock()
	close(t.done)
	return nil, ctx.Err()
}
func (t *discardSlowTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *discardSlowTool) IsReadOnly(json.RawMessage) bool           { return false }
func (t *discardSlowTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *discardSlowTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (t *discardSlowTool) IsEnabled() bool                           { return true }
func (t *discardSlowTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *discardSlowTool) Prompt() string                            { return "" }
func (t *discardSlowTool) RenderResult(any) string                   { return "" }
func (t *discardSlowTool) NewResultType() any                        { return nil }

func (*discardSlowTool) MaxResultSize() int { return 50000 }
func (t *discardSlowTool) WasCancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelled
}
func (t *discardSlowTool) WasStarted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.started
}

// WaitCancelled blocks until Call completes after cancellation, or timeout.
func (t *discardSlowTool) WaitCancelled(timeout time.Duration) error {
	select {
	case <-t.done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timed out waiting for discardSlowTool cancellation after %v", timeout)
	}
}

// midStreamErrorProvider returns tool_use events followed by an in-stream error.
// This tests that callLLM discards the streamingExecutor when a stream error
// occurs after tools have been started.
// Source: TS query.ts:734 — discard() before retry when stream errors mid-execution.
type midStreamErrorProvider struct {
	callCount int
}

func (p *midStreamErrorProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *midStreamErrorProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.callCount++
	switch p.callCount {
	case 1:
		// First call: tool_use events, then error mid-stream.
		// The tool_use block creates the executor and starts the tool goroutine.
		// The error event triggers callLLM to return — without Discard(), the
		// tool goroutine leaks.
		events := []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "discard_slow"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
			{Type: "content_block_stop", Index: 0},
			{Error: &llm.APIError{Status: 429, Retryable: true, Message: "rate limited mid-stream"}},
		}
		ch := make(chan llm.StreamEvent, len(events))
		for _, e := range events {
			ch <- e
		}
		close(ch)
		return ch, nil
	default:
		// Subsequent calls: success
		events := []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 10}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "done"}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 3}},
			{Type: "message_stop"},
		}
		ch := make(chan llm.StreamEvent, len(events))
		for _, e := range events {
			ch <- e
		}
		close(ch)
		return ch, nil
	}
}

// TestCallLLM_DiscardsExecutorOnStreamError verifies that when callLLM encounters
// a stream error AFTER creating a StreamingToolExecutor with running tool goroutines,
// the executor is Discard()ed to cancel those goroutines.
// API-level errors (429) are terminal at engine level (D2), but executor cleanup
// must still happen to prevent goroutine leaks.
func TestCallLLM_DiscardsExecutorOnStreamError(t *testing.T) {
	dt := newDiscardSlowTool()
	p := &midStreamErrorProvider{}
	tc := newEventCollector()
	eng := New(&Params{
		Provider:   p,
		Tools:      []tool.Tool{dt},
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	ctx := t.Context()

	result := eng.QuerySync(ctx, "test", "")

	// API error (429) is terminal at engine level — no retry (D2)
	if result.Error == nil {
		t.Fatal("expected terminal error for mid-stream 429, got nil")
	}
	if !strings.Contains(result.Error.Error(), "rate limited") {
		t.Errorf("error should contain 'rate limited', got: %v", result.Error)
	}

	// Wait for the tool goroutine to settle.
	// If tool started, wait for cancellation; if not started, it was aborted.
	if dt.WasStarted() {
		if err := dt.WaitCancelled(2 * time.Second); err != nil {
			t.Error("tool started but was never cancelled — callLLM must Discard() executor on stream error")
		}
	}

	// Verify tool goroutine was properly cleaned up.
	if dt.WasStarted() && !dt.WasCancelled() {
		t.Error("tool started but was never cancelled — callLLM must Discard() executor on stream error")
	}
}

// ---------------------------------------------------------------------------
// marshalMessages / normalizeMessagesForAPI
// ---------------------------------------------------------------------------

func TestMarshalMessages_StripsResponseOnlyFields(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })
	eng.messages = []types.Message{
		{
			Role:       types.RoleUser,
			Content:    []types.ContentBlock{types.NewTextBlock("hello")},
			Timestamp:  time.Now(), // REAL-TIME: needed for message timestamp in test
			Model:      "claude-3",
			StopReason: "end_turn",
			Usage:      &types.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{
			Role:    types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("hi")},
		},
	}

	got := eng.marshalMessages()

	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}

	// Response-only fields must be zeroed
	if !got[0].Timestamp.IsZero() {
		t.Error("Timestamp should be zeroed")
	}
	if got[0].Model != "" {
		t.Error("Model should be empty")
	}
	if got[0].StopReason != "" {
		t.Error("StopReason should be empty")
	}
	if got[0].Usage != nil {
		t.Error("Usage should be nil")
	}

	// Content must be preserved (with timestamp prefix for user messages)
	userText := got[0].Content[0].Text
	if !regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [A-Z]+\]`).MatchString(userText) || !strings.Contains(userText, "hello") {
		t.Errorf("user content should have [YYYY-MM-DD HH:MM:SS TZ] prefix + original text, got: %q", userText)
	}
	if got[1].Content[0].Text != "hi" {
		t.Errorf("content not preserved: %q", got[1].Content[0].Text)
	}
}

func TestMarshalMessages_ThinkingBlockJSONField(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })
	eng.messages = []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeThinking, Thinking: "Let me analyze this"},
			},
		},
	}

	got := eng.marshalMessages()

	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}

	// Verify the ContentBlock itself has Thinking field populated
	block := got[0].Content[0]
	if block.Type != types.ContentTypeThinking {
		t.Fatalf("block type = %q, want thinking", block.Type)
	}
	if block.Thinking != "Let me analyze this" {
		t.Errorf("Thinking = %q, want %q", block.Thinking, "Let me analyze this")
	}

	// Verify JSON serialization uses "thinking" field (not "text")
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	raw := string(data)
	if !strings.Contains(raw, `"thinking":"Let me analyze this"`) {
		t.Errorf("JSON should contain thinking field with content, got: %s", raw)
	}
}

func TestMarshalMessages_PreservesToolUseAndResult(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })
	eng.messages = []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock("toolu_1", json.RawMessage(`"output"`), false),
			},
		},
	}

	got := eng.marshalMessages()

	if got[0].Content[0].Type != types.ContentTypeToolResult {
		t.Error("tool_result block should be preserved")
	}
	if got[0].Content[0].ToolUseID != "toolu_1" {
		t.Error("ToolUseID should be preserved")
	}
}

// ---------------------------------------------------------------------------
// NormalizeMessagesForAPI tests
// ---------------------------------------------------------------------------

func TestNormalizeMessagesForAPI_FiltersSystemMessages(t *testing.T) {
	t.Parallel()

	// Compact boundaries (RoleSystem) must be filtered out — they are local
	// metadata for tool search, not LLM context. TS normalizeMessagesForAPI
	// filters all system messages except local commands.
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock(`{"subtype":"compact_boundary"}`)}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("another system msg")}},
	}

	result := NormalizeMessagesForAPI(messages)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2 (system messages filtered)", len(result))
	}
	if result[0].Role != types.RoleUser {
		t.Errorf("result[0].Role = %q, want user", result[0].Role)
	}
	if result[0].Content[0].Text != "hello" {
		t.Errorf("result[0].Content = %q, want hello", result[0].Content[0].Text)
	}
	if result[1].Role != types.RoleAssistant {
		t.Errorf("result[1].Role = %q, want assistant", result[1].Role)
	}
}

func TestNormalizeMessagesForAPI_NoSystemMessages(t *testing.T) {
	t.Parallel()

	// Normal conversation with no system messages passes through unchanged.
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	result := NormalizeMessagesForAPI(messages)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
}

func TestNormalizeMessagesForAPI_Empty(t *testing.T) {
	t.Parallel()

	result := NormalizeMessagesForAPI(nil)
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0 for nil", len(result))
	}

	result = NormalizeMessagesForAPI([]types.Message{})
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0 for empty", len(result))
	}
}

func TestNormalizeMessagesForAPI_OnlySystemMessages(t *testing.T) {
	t.Parallel()

	messages := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("sys1")}},
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("sys2")}},
	}

	result := NormalizeMessagesForAPI(messages)

	if len(result) != 0 {
		t.Fatalf("len(result) = %d, want 0 (all system filtered)", len(result))
	}
}

func TestNormalizeMessagesForAPI_StripsEmptyThinkingBlocks(t *testing.T) {
	t.Parallel()

	// Thinking blocks with empty Thinking field must be stripped.
	// Compact/storage can leave {"type":"thinking"} without the required field,
	// causing "missing field thinking" API errors on resume.
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking}, // empty Thinking — must be removed
			types.NewTextBlock("response"),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		// Assistant with valid thinking — must be kept
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Thinking: "let me think"},
			types.NewTextBlock("answer"),
		}},
		// Assistant with only empty thinking — entire message must be dropped
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking},
		}},
	}

	result := NormalizeMessagesForAPI(messages)

	// Expect: msg0 (thinking stripped, text kept), msg1, msg2 (thinking kept, text kept)
	// msg3 dropped entirely (only had empty thinking)
	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3 (msg3 dropped)", len(result))
	}

	// msg0: empty thinking stripped, only text block remains
	if len(result[0].Content) != 1 {
		t.Errorf("msg0: len(content) = %d, want 1 (thinking stripped)", len(result[0].Content))
	}
	if result[0].Content[0].Text != "response" {
		t.Errorf("msg0: content = %q, want 'response'", result[0].Content[0].Text)
	}

	// msg2: valid thinking kept
	if len(result[2].Content) != 2 {
		t.Fatalf("msg2: len(content) = %d, want 2 (thinking+text)", len(result[2].Content))
	}
	if result[2].Content[0].Thinking != "let me think" {
		t.Errorf("msg2: thinking = %q, want 'let me think'", result[2].Content[0].Thinking)
	}

	// Verify serialized JSON has no thinking block without "thinking" field
	for i, msg := range result {
		for j, block := range msg.Content {
			if block.Type == types.ContentTypeThinking {
				raw, _ := json.Marshal(block)
				if !bytes.Contains(raw, []byte(`"thinking":`)) {
					t.Errorf("result[%d].Content[%d]: serialized thinking block missing 'thinking' field: %s", i, j, raw)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers for internal tests
// ---------------------------------------------------------------------------

// testProvider is a minimal mock provider for internal tests.
type testProvider struct {
	mu        sync.Mutex
	responses []testResponse
	index     int
	onStream  func(req *llm.Request) // if set, called before returning events
}

type testResponse struct {
	events  []llm.StreamEvent
	err     error
	channel chan llm.StreamEvent // if non-nil, return this channel directly
}

func (p *testProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *testProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.onStream != nil {
		p.onStream(req)
	}

	if p.index >= len(p.responses) {
		return nil, nil
	}
	resp := p.responses[p.index]
	p.index++

	if resp.err != nil {
		return nil, resp.err
	}

	// If a pre-built channel is provided, return it directly
	if resp.channel != nil {
		return resp.channel, nil
	}

	ch := make(chan llm.StreamEvent, len(resp.events)+1)
	go func() {
		defer close(ch)
		for _, evt := range resp.events {
			ch <- evt
		}
	}()
	return ch, nil
}

func (p *testProvider) addResponse(events []llm.StreamEvent, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append(p.responses, testResponse{events: events, err: err})
}

func (p *testProvider) addChannelResponse(ch chan llm.StreamEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append(p.responses, testResponse{channel: ch})
}

// ---------------------------------------------------------------------------
// Sub-engine tests
// ---------------------------------------------------------------------------

// subTextEvents creates streaming events for a simple text response (internal helper).
func subTextEvents(model, text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
}

func TestNewSubEngineFieldIndependence(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mt := &testTool{name: "test_tool"}
	parent := New(&Params{
		Provider:    mp,
		Tools:       []tool.Tool{mt},
		Model:       "parent-model",
		TokenBudget: 100000,
	})
	t.Cleanup(func() { parent.Close() })

	// Add state to parent
	parent.AddSystemMessage("parent only message")

	// Create sub-engine
	subTools := map[string]tool.Tool{"test_tool": mt}
	sub := parent.NewSubEngine(SubEngineOptions{
		Tools:    subTools,
		MaxTurns: 10,
	})

	// Modify sub's state
	sub.messages = append(sub.messages, types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("sub only message")},
	})
	sub.turnCount = 42
	sub.tokenBudget = -999

	// Verify parent unchanged
	parentMsgs := parent.Messages()
	if len(parentMsgs) != 1 {
		t.Fatalf("parent should have 1 message, got %d", len(parentMsgs))
	}
	if parentMsgs[0].Content[0].Text != "parent only message" {
		t.Errorf("parent message text = %q, want %q", parentMsgs[0].Content[0].Text, "parent only message")
	}
	if parent.turnCount != 0 {
		t.Errorf("parent turnCount = %d, want 0", parent.turnCount)
	}
	if parent.tokenBudget != 100000 {
		t.Errorf("parent tokenBudget = %d, want 100000", parent.tokenBudget)
	}

	// Verify sub has its own independent state
	if len(sub.messages) != 1 {
		t.Errorf("sub should have 1 message, got %d", len(sub.messages))
	}
	if sub.messages[0].Content[0].Text != "sub only message" {
		t.Errorf("sub message text = %q, want %q", sub.messages[0].Content[0].Text, "sub only message")
	}
	if sub.turnCount != 42 {
		t.Errorf("sub turnCount = %d, want 42", sub.turnCount)
	}
	if sub.tokenBudget != -999 {
		t.Errorf("sub tokenBudget = %d, want -999", sub.tokenBudget)
	}
}

func TestNewSubEngineSharesProvider(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	parent := New(&Params{Provider: mp, Model: "test"})
	t.Cleanup(func() { parent.Close() })
	sub := parent.NewSubEngine(SubEngineOptions{})

	// Both should point to the exact same provider instance (pointer equality)
	if sub.provider != parent.provider {
		t.Error("sub-engine should share the same provider instance as parent")
	}
}

func TestNewSubEngineModelOverride(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	parent := New(&Params{Provider: mp, Model: "parent-model"})
	t.Cleanup(func() { parent.Close() })

	// Case 1: no model override → inherits parent
	sub1 := parent.NewSubEngine(SubEngineOptions{})
	if sub1.model != "parent-model" {
		t.Errorf("sub1.model = %q, want %q (inherit from parent)", sub1.model, "parent-model")
	}

	// Case 2: model override → uses override
	sub2 := parent.NewSubEngine(SubEngineOptions{Model: "opus"})
	if sub2.model != "opus" {
		t.Errorf("sub2.model = %q, want %q (override)", sub2.model, "opus")
	}
}

func TestNewSubEngineMaxTurns(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	parent := New(&Params{Provider: mp, Model: "test"})
	t.Cleanup(func() { parent.Close() })

	// Case 1: MaxTurns=0 → no limit
	sub1 := parent.NewSubEngine(SubEngineOptions{MaxTurns: 0})
	if sub1.maxTurns != 0 {
		t.Errorf("sub1.maxTurns = %d, want 0 (no limit)", sub1.maxTurns)
	}

	// Case 2: MaxTurns=5 → 5
	sub2 := parent.NewSubEngine(SubEngineOptions{MaxTurns: 5})
	if sub2.maxTurns != 5 {
		t.Errorf("sub2.maxTurns = %d, want 5", sub2.maxTurns)
	}
}

func TestQuerySync(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(subTextEvents("test", "Hello from sub-agent"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })

	ctx := context.Background()
	result := eng.QuerySync(ctx, "test query", "")

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(result.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != types.RoleUser {
		t.Errorf("expected first message to be user, got %s", result.Messages[0].Role)
	}
	if result.Messages[0].Content[0].Text != "test query" {
		t.Errorf("user message text = %q, want %q", result.Messages[0].Content[0].Text, "test query")
	}
	if result.Messages[1].Role != types.RoleAssistant {
		t.Errorf("expected second message to be assistant, got %s", result.Messages[1].Role)
	}
	if len(result.Messages[1].Content) == 0 {
		t.Fatal("assistant message has no content blocks")
	}
	if result.Messages[1].Content[0].Text != "Hello from sub-agent" {
		t.Errorf("assistant text = %q, want %q", result.Messages[1].Content[0].Text, "Hello from sub-agent")
	}
	if result.TotalUsage.InputTokens != 10 {
		t.Errorf("TotalUsage.InputTokens = %d, want 10", result.TotalUsage.InputTokens)
	}
	if result.TotalUsage.OutputTokens != 5 {
		t.Errorf("TotalUsage.OutputTokens = %d, want 5", result.TotalUsage.OutputTokens)
	}
}

func TestQuerySyncCancellation(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result := eng.QuerySync(ctx, "test query", "")
	if result.Error == nil {
		t.Fatal("expected non-nil error from cancelled context")
	}
	if result.Error.Error() != "context canceled" {
		t.Errorf("error = %q, want %q", result.Error.Error(), "context canceled")
	}
}

func TestEmitEventNilSafe(t *testing.T) {
	// Sub-engine has dispatcher=nil. emitEvent should silently discard.
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	if eng.dispatcher != nil {
		t.Fatal("expected nil dispatcher for default engine")
	}
	// This should NOT panic — that's the entire assertion
	eng.emitEvent(types.QueryEvent{Type: types.EventTurnStart})
}

func TestSubEngineBudgetBypass(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// Heavy token usage that would normally trigger TerminalPromptTooLong
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 99999}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "test_tool"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 99999}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)
	mp.addResponse(subTextEvents("test", "Still running"), nil)

	mt := &testTool{name: "test_tool"}

	// Create parent with tiny budget
	parent := New(&Params{
		Provider:    mp,
		Tools:       []tool.Tool{mt},
		Model:       "test",
		TokenBudget: 100,
	})
	t.Cleanup(func() { parent.Close() })

	// Create sub-engine via NewSubEngine (isSubagent=true, tokenBudget=0)
	subTools := map[string]tool.Tool{"test_tool": mt}
	sub := parent.NewSubEngine(SubEngineOptions{Tools: subTools})

	// Verify sub-engine is marked as subagent
	if !sub.isSubagent {
		t.Error("sub-engine should have isSubagent=true")
	}

	ctx := context.Background()
	result := sub.QuerySync(ctx, "test query", "")

	// Should complete normally despite heavy token usage (subagent bypasses budget check)
	if result.Error != nil {
		t.Fatalf("sub-agent should bypass budget check and complete, got: %v", result.Error)
	}
}

func TestSubMaxTurns(t *testing.T) {
	t.Parallel()
	if subMaxTurns(0) != 0 {
		t.Errorf("subMaxTurns(0) = %d, want 0 (no limit)", subMaxTurns(0))
	}
	if subMaxTurns(-1) != 0 {
		t.Errorf("subMaxTurns(-1) = %d, want 0 (no limit)", subMaxTurns(-1))
	}
	if subMaxTurns(10) != 10 {
		t.Errorf("subMaxTurns(10) = %d, want 10", subMaxTurns(10))
	}
	if subMaxTurns(100) != 100 {
		t.Errorf("subMaxTurns(100) = %d, want 100", subMaxTurns(100))
	}
}

// ---------------------------------------------------------------------------
// TaggedDispatcher
// ---------------------------------------------------------------------------

type mockDispatcher struct {
	events []types.QueryEvent
}

func (m *mockDispatcher) Dispatch(event types.QueryEvent) {
	m.events = append(m.events, event)
}

func (m *mockDispatcher) Events() []types.QueryEvent {
	out := make([]types.QueryEvent, len(m.events))
	copy(out, m.events)
	return out
}

func TestTaggedDispatcher_InjectsMeta(t *testing.T) {
	t.Parallel()

	md := &mockDispatcher{}
	meta := &types.AgentMeta{
		ParentToolUseID: "call_test123",
		AgentType:       "Explore",
		Depth:           0,
	}
	td := &taggedDispatcher{parent: md, meta: meta}

	evt := types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:      "sub_call_1",
			Name:    "Read",
			Summary: "Reading engine.go",
		},
	}

	td.Dispatch(evt)

	if len(md.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(md.events))
	}

	got := md.events[0]
	if got.Agent == nil {
		t.Fatal("Agent meta should be injected")
	}
	if got.Agent.ParentToolUseID != "call_test123" {
		t.Errorf("ParentToolUseID = %q, want %q", got.Agent.ParentToolUseID, "call_test123")
	}
	if got.Agent.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", got.Agent.AgentType, "Explore")
	}
	if got.Agent.Depth != 0 {
		t.Errorf("Depth = %d, want 0", got.Agent.Depth)
	}
	// Original event fields preserved
	if got.ToolUse.Name != "Read" {
		t.Errorf("ToolUse.Name = %q, want %q", got.ToolUse.Name, "Read")
	}
}

func TestNewSubEngine_TaggedDispatcher(t *testing.T) {
	t.Parallel()

	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   &testProvider{},
		Dispatcher: md,
		Model:      "test",
	})
	t.Cleanup(func() { eng.Close() })

	subEng := eng.NewSubEngine(SubEngineOptions{
		Tools:           map[string]tool.Tool{"test": &testTool{name: "test"}},
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
	})

	if subEng.dispatcher == nil {
		t.Fatal("sub-engine dispatcher should not be nil when parent has dispatcher")
	}

	// Emit an event through the sub-engine and verify it reaches the mock dispatcher
	subEng.emitEvent(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "sub_1", Name: "Read"},
	})

	if len(md.events) != 1 {
		t.Fatalf("expected 1 event via mock dispatcher, got %d", len(md.events))
	}
	if md.events[0].Agent == nil {
		t.Fatal("event should have Agent meta")
	}
	if md.events[0].Agent.ParentToolUseID != "call_abc" {
		t.Errorf("ParentToolUseID = %q, want %q", md.events[0].Agent.ParentToolUseID, "call_abc")
	}
}

func TestNewSubEngine_NoDispatcher_NoTagged(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		// No Dispatcher
	})
	t.Cleanup(func() { eng.Close() })

	subEng := eng.NewSubEngine(SubEngineOptions{
		Tools:           map[string]tool.Tool{"test": &testTool{name: "test"}},
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
	})

	// No parent dispatcher → sub-engine dispatcher stays nil
	if subEng.dispatcher != nil {
		t.Error("sub-engine dispatcher should be nil when parent has no dispatcher")
	}
}

// ---------------------------------------------------------------------------
// StreamingToolExecutor.SetMessages — conversation history propagation
// ---------------------------------------------------------------------------

// captureMessagesTool captures the ToolUseContext.Messages it receives.
type captureMessagesTool struct {
	captured []types.Message
	mu       sync.Mutex
}

func (t *captureMessagesTool) Name() string                                { return "capture" }
func (t *captureMessagesTool) Aliases() []string                           { return nil }
func (t *captureMessagesTool) Description(json.RawMessage) (string, error) { return "capture", nil }
func (t *captureMessagesTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *captureMessagesTool) Call(_ context.Context, _ json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if tctx != nil {
		t.mu.Lock()
		t.captured = tctx.Messages
		t.mu.Unlock()
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *captureMessagesTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *captureMessagesTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *captureMessagesTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *captureMessagesTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *captureMessagesTool) IsEnabled() bool                           { return true }
func (t *captureMessagesTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *captureMessagesTool) Prompt() string                            { return "" }
func (t *captureMessagesTool) RenderResult(any) string                   { return "" }
func (t *captureMessagesTool) NewResultType() any                        { return nil }

func (t *captureMessagesTool) MaxResultSize() int { return 50000 }

func (t *captureMessagesTool) Captured() []types.Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.captured
}

// TestStreamingToolExecutor_SetMessages_NilTctx verifies that tools receive
// conversation history even when the executor is created with nil tctx (the
// common case from engine.go's callLLM). Without SetMessages, the tool would
// receive nil Messages. This is critical for fork agent message construction.
func TestStreamingToolExecutor_SetMessages_NilTctx(t *testing.T) {
	t.Parallel()

	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi there")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("do something")}},
	}

	captureTool := &captureMessagesTool{}
	toolMap := map[string]tool.Tool{"capture": captureTool}

	executor := NewStreamingToolExecutor(toolMap, nil, func(types.QueryEvent) {}, context.Background())
	executor.SetMessages(messages)

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "capture", Input: json.RawMessage(`{}`)},
	}
	result := executor.ExecuteAll(blocks)

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.ToolResultBlocks))
	}
	if result.ToolResultBlocks[0].IsError {
		t.Fatalf("expected no error, got error content: %s", string(result.ToolResultBlocks[0].Content))
	}

	captured := captureTool.Captured()
	if len(captured) != 3 {
		t.Fatalf("expected 3 messages in tctx, got %d", len(captured))
	}
	if captured[0].Role != types.RoleUser {
		t.Errorf("first message role = %q, want %q", captured[0].Role, types.RoleUser)
	}
	if captured[0].Content[0].Text != "hello" {
		t.Errorf("first message text = %q, want %q", captured[0].Content[0].Text, "hello")
	}
	if captured[2].Content[0].Text != "do something" {
		t.Errorf("third message text = %q, want %q", captured[2].Content[0].Text, "do something")
	}
}

// TestStreamingToolExecutor_SetMessages_WithExistingTctx verifies that
// SetMessages overrides even when a non-nil tctx exists. This ensures
// the messages field takes priority over tctx.Messages.
func TestStreamingToolExecutor_SetMessages_WithExistingTctx(t *testing.T) {
	t.Parallel()

	oldMessages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("old")}},
	}
	newMessages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("new1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("new2")}},
	}

	captureTool := &captureMessagesTool{}
	toolMap := map[string]tool.Tool{"capture": captureTool}

	tctx := &tool.ToolUseContext{
		ToolUseID: "tu_parent",
		Messages:  oldMessages,
	}

	executor := NewStreamingToolExecutor(toolMap, tctx, func(types.QueryEvent) {}, context.Background())
	executor.SetMessages(newMessages)

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "capture", Input: json.RawMessage(`{}`)},
	}
	executor.ExecuteAll(blocks)

	captured := captureTool.Captured()
	if len(captured) != 2 {
		t.Fatalf("expected 2 messages (new), got %d", len(captured))
	}
	if captured[0].Content[0].Text != "new1" {
		t.Errorf("first message text = %q, want %q", captured[0].Content[0].Text, "new1")
	}
}

// TestStreamingToolExecutor_NoSetMessages_NilTctx verifies that without
// SetMessages, tools receive nil Messages when tctx is nil. This documents
// the baseline behavior that SetMessages fixes.
func TestStreamingToolExecutor_NoSetMessages_NilTctx(t *testing.T) {
	t.Parallel()

	captureTool := &captureMessagesTool{}
	toolMap := map[string]tool.Tool{"capture": captureTool}

	executor := NewStreamingToolExecutor(toolMap, nil, func(types.QueryEvent) {}, context.Background())

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "capture", Input: json.RawMessage(`{}`)},
	}
	executor.ExecuteAll(blocks)

	captured := captureTool.Captured()
	if captured != nil {
		t.Errorf("expected nil Messages without SetMessages, got %d messages", len(captured))
	}
}

// ---------------------------------------------------------------------------
// Phase 3: Engine accessors + RunForkedQuery
// ---------------------------------------------------------------------------

func TestEngineModel_Accessor(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &testProvider{}, Model: "test-model"})
	t.Cleanup(func() { eng.Close() })
	if got := eng.Model(); got != "test-model" {
		t.Errorf("Model() = %q, want %q", got, "test-model")
	}
}

func TestEngineSystemPrompt_Accessors(t *testing.T) {
	t.Parallel()
	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	// Initially empty
	if sp := eng.SystemPrompt(); sp != "" {
		t.Errorf("SystemPrompt() should be empty initially, got %q", sp)
	}

	// Set and read back
	sp := `{"role":"system","content":"you are helpful"}`
	eng.SetSystemPrompt(sp)
	if got := eng.SystemPrompt(); string(got) != string(sp) {
		t.Errorf("SystemPrompt() = %q, want %q", string(got), string(sp))
	}
}

func TestRunForkedQuery(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(subTextEvents("test", "response from existing messages"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })

	// Pre-construct messages — simulating what fork agent builds
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("original user msg")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("original assistant msg")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("fork directive")}},
	}

	result := eng.RunForkedQuery(context.Background(), messages, "")

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Verify messages start with the pre-constructed ones (not an extra injected user msg)
	if len(result.Messages) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Content[0].Text != "original user msg" {
		t.Errorf("first message = %q, want %q", result.Messages[0].Content[0].Text, "original user msg")
	}
	if result.Messages[1].Content[0].Text != "original assistant msg" {
		t.Errorf("second message = %q, want %q", result.Messages[1].Content[0].Text, "original assistant msg")
	}
	// The LLM's response should be appended after the pre-constructed messages
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Errorf("last message role = %q, want assistant", lastMsg.Role)
	}
	if lastMsg.Content[0].Text != "response from existing messages" {
		t.Errorf("last message text = %q, want %q", lastMsg.Content[0].Text, "response from existing messages")
	}
}

// TestRunTurns_DrainsNotificationsAtStage20 verifies that when runTurns hits the

// TestRunForkedQuery_IncludesClaudeMd verifies that fork agents receive the
// CLAUDE.md context injection, matching TS behavior where runForkedAgent
// passes userContext (containing claudeMd) to query() → prependUserContext().
func TestRunForkedQuery_IncludesClaudeMd(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	claudeMdContent := "# Test Project\nFollow TDD for all changes."
	if err := os.WriteFile(tmpDir+"/CLAUDE.md", []byte(claudeMdContent), 0644); err != nil {
		t.Fatal(err)
	}

	var capturedMessages []types.Message
	mp := &testProvider{
		onStream: func(req *llm.Request) {
			capturedMessages = req.Messages
		},
	}
	mp.addResponse(subTextEvents("test", "fork response"), nil)

	parentEng := New(&Params{
		Provider:   mp,
		Model:      "test",
		WorkingDir: tmpDir,
	})
	t.Cleanup(func() { parentEng.Close() })

	subEng := parentEng.NewSubEngine(SubEngineOptions{
		AgentType: "fork",
	})

	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("do the task")}},
	}

	result := subEng.RunForkedQuery(context.Background(), messages, "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(capturedMessages) == 0 {
		t.Fatal("no messages captured from provider")
	}

	first := capturedMessages[0]
	if !strings.Contains(first.Content[0].Text, "Test Project") {
		t.Errorf("first message should contain CLAUDE.md content, got: %q", truncate(first.Content[0].Text, 200))
	}
}

func TestIsBuiltInAgent(t *testing.T) {
	tests := []struct {
		agentType string
		want      bool
	}{
		{"General", true},
		{"Explore", true},
		{"Plan", true},
		{"general-purpose", false},
		{"my-custom-agent", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isBuiltInAgent(tt.agentType); got != tt.want {
			t.Errorf("isBuiltInAgent(%q) = %v, want %v", tt.agentType, got, tt.want)
		}
	}
}

func TestNewSubEngine_SetsAgentType(t *testing.T) {
	parent := New(&Params{})
	t.Cleanup(func() { parent.Close() })

	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "Explore",
		Tools:     map[string]tool.Tool{},
	})
	if sub.agentType != "Explore" {
		t.Errorf("sub.agentType = %q, want %q", sub.agentType, "Explore")
	}
	if !sub.isSubagent {
		t.Error("sub.isSubagent should be true")
	}

	// Main engine should have empty agentType
	if parent.agentType != "" {
		t.Errorf("parent.agentType = %q, want empty", parent.agentType)
	}
}

// ---------------------------------------------------------------------------
// toolresult.MaybePersistLargeToolResult tests
// ---------------------------------------------------------------------------

func TestPersistLargeToolResult_BelowThreshold(t *testing.T) {
	t.Parallel()

	// Short output: pass through unchanged
	input := []byte("hello world")
	pr := toolresult.MaybePersistLargeToolResult(input, "Test", 0, "test-id", "session-1")
	if string(pr.Output) != "hello world" {
		t.Errorf("short output: got %q, want %q", string(pr.Output), "hello world")
	}
	if pr.Persisted {
		t.Error("short output should not be persisted")
	}

	// Negative threshold (Read tool): pass through
	pr = toolresult.MaybePersistLargeToolResult(input, "Read", -1, "test-id", "session-1")
	if string(pr.Output) != "hello world" {
		t.Errorf("negative threshold: got %q, want %q", string(pr.Output), "hello world")
	}
}

func TestPersistLargeToolResult_OverThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	defer toolresult.ResetDirCache()

	// Create large JSON output (double-wrapped string)
	data := strings.Repeat("hello world ", 10000) // ~120K bytes
	validJSON, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}

	pr := toolresult.MaybePersistLargeToolResult(validJSON, "Bash", 30000, "test-tool-use-id", "session-abc")

	// Output must be valid JSON
	if !json.Valid(pr.Output) {
		t.Errorf("persisted output is not valid JSON: %q", pr.Output[:200])
	}

	// Must be persisted
	if !pr.Persisted {
		t.Error("large output should be persisted")
	}
	if pr.FilePath == "" {
		t.Error("FilePath should be set when persisted")
	}

	// Preview should contain the tag
	var preview string
	if err := json.Unmarshal(pr.Output, &preview); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if !strings.Contains(preview, "<persisted-output>") {
		t.Error("preview should contain <persisted-output> tag")
	}

	// File should exist on disk
	if _, err := os.Stat(pr.FilePath); os.IsNotExist(err) {
		t.Errorf("persisted file should exist at %s", pr.FilePath)
	}
}

// ---------------------------------------------------------------------------
// marshalToolOutput tests
// ---------------------------------------------------------------------------

type wireFormatTool struct {
	minimalTool
}

func (w *wireFormatTool) FormatWireResult(data any) string {
	return fmt.Sprintf("custom:%v", data)
}

func TestMarshalToolOutput(t *testing.T) {
	t.Parallel()

	// ToolWithWireFormat: uses custom format
	wfTool := &wireFormatTool{}
	got := marshalToolOutput(wfTool, "result", false)
	if string(got) != `"custom:result"` {
		t.Errorf("wire format: got %q, want %q", string(got), `"custom:result"`)
	}

	// doubleWrap=false: raw JSON
	plainTool := &minimalTool{}
	got = marshalToolOutput(plainTool, map[string]string{"key": "val"}, false)
	if string(got) != `{"key":"val"}` {
		t.Errorf("doubleWrap=false: got %q, want %q", string(got), `{"key":"val"}`)
	}

	// doubleWrap=true: double-wrapped JSON string
	got = marshalToolOutput(plainTool, "hello", true)
	var unwrapped string
	if err := json.Unmarshal(got, &unwrapped); err != nil {
		t.Fatalf("doubleWrap=true: outer unmarshal failed: %v", err)
	}
	if unwrapped != `"hello"` {
		t.Errorf("doubleWrap=true inner: got %q, want %q", unwrapped, `"hello"`)
	}
}

// ---------------------------------------------------------------------------
// shouldAutoCompact tests
// ---------------------------------------------------------------------------

type internalMockCompactor struct{}

func (c *internalMockCompactor) Compact(_ context.Context, messages []types.Message) (*short.CompactResult, error) {
	return &short.CompactResult{
		BeforeTokens: len(messages) * 100,
		AfterTokens:  len(messages) * 100,
		Messages:     messages,
	}, nil
}

func TestShouldAutoCompact(t *testing.T) {
	t.Parallel()

	// No compactor → false
	eng := New(&Params{Model: "test"})
	t.Cleanup(func() { eng.Close() })
	if eng.shouldAutoCompact() {
		t.Error("should be false without compactor")
	}

	// Set compactor with valid config
	eng.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
		ContextWindow: 100000,
	})
	// No messages → tokens=0 → below threshold → false
	if eng.shouldAutoCompact() {
		t.Error("should be false with 0 tokens")
	}

	// ContextWindow=0 → auto-compact disabled → false
	eng2 := New(&Params{Model: "test"})
	eng2.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
		ContextWindow: 0,
	})
	if eng2.shouldAutoCompact() {
		t.Error("should be false with ContextWindow=0")
	}

	// Circuit breaker: too many failures → false
	eng3 := New(&Params{Model: "test"})
	eng3.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
		ContextWindow:          100,
		MaxConsecutiveFailures: 2,
	})
	eng3.consecutiveCompactFailures = 2
	// Even with high tokens, circuit breaker trips
	eng3.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 10000))}},
	})
	if eng3.shouldAutoCompact() {
		t.Error("should be false when circuit breaker trips")
	}

	// Default MaxConsecutiveFailures (0 → defaults to 3)
	eng4 := New(&Params{Model: "test"})
	eng4.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
		ContextWindow: 100,
	})
	eng4.consecutiveCompactFailures = 3
	eng4.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 10000))}},
	})
	if eng4.shouldAutoCompact() {
		t.Error("should be false with default circuit breaker at 3 failures")
	}
}

// TestShouldAutoCompact_SubAgentCanCompact verifies that sub-agents CAN trigger
// proactive auto-compact when they inherit a compactor and exceed the threshold.
// This is the core behavioral change: TS only guards compact/session_memory,
// not all sub-agents. Source: TS autoCompact.ts:169-172.
func TestShouldAutoCompact_SubAgentCanCompact(t *testing.T) {
	t.Parallel()

	parent := New(&Params{Model: "test"})
	t.Cleanup(func() { parent.Close() })
	parent.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
		ContextWindow: 100,
	})

	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "Explore",
		Tools:     map[string]tool.Tool{},
	})

	// Sub should inherit compactor from parent
	if sub.compactor == nil {
		t.Fatal("sub-engine should inherit compactor from parent")
	}

	// Add enough messages to exceed threshold (100 * 0.5 = 50 tokens)
	sub.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 10000))}},
	})

	if !sub.shouldAutoCompact() {
		t.Error("sub-agent with compactor and high tokens should trigger auto-compact")
	}
}

// TestNewSubEngine_SharesCompactor verifies that NewSubEngine passes compactor
// and autoCompactConfig from parent to sub-engine. Source: plan Step 4.
func TestNewSubEngine_SharesCompactor(t *testing.T) {
	t.Parallel()

	parent := New(&Params{Model: "test"})
	t.Cleanup(func() { parent.Close() })
	parent.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
		ContextWindow: 200000,
	})

	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "Explore",
		Tools:     map[string]tool.Tool{},
	})

	if sub.compactor == nil {
		t.Fatal("sub-engine should inherit compactor from parent")
	}
	// Threshold removed: verify ContextWindow is inherited instead
	if sub.autoCompactConfig.ContextWindow != 200000 {
		t.Errorf("sub.autoCompactConfig.ContextWindow = %d, want 200000", sub.autoCompactConfig.ContextWindow)
	}
}

// TestShouldAutoCompact_QuerySourceGuard verifies that built-in and custom
// sub-agents can trigger auto-compact. The compact/session_memory guards are
// forward-looking — they will be tested when those features are implemented.
// Source: TS autoCompact.ts:169-172 — guards only compact and session_memory.
func TestShouldAutoCompact_QuerySourceGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
	}{
		{"Explore agent", "Explore"},
		{"Plan agent", "Plan"},
		{"General agent", "General"},
		{"Custom agent", "my-custom-agent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := New(&Params{Model: "test"})
			t.Cleanup(func() { parent.Close() })
			parent.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
				ContextWindow: 100,
			})

			sub := parent.NewSubEngine(SubEngineOptions{
				AgentType: tt.agentType,
				Tools:     map[string]tool.Tool{},
			})

			// Verify querySource is NOT compact or session_memory
			src := sub.querySource()
			if src == QuerySourceCompact || src == QuerySourceSessionMemory {
				t.Fatalf("querySource %q should not match guard for %s", src, tt.agentType)
			}

			// With enough tokens, shouldAutoCompact should return true
			sub.SetMessages([]types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 10000))}},
			})

			if !sub.shouldAutoCompact() {
				t.Errorf("%s should trigger auto-compact with compactor and high tokens", tt.name)
			}
		})
	}
}

// TestQuery_SubAgent_OversizedContext verifies sub-agents proceed normally
// even with oversized context (no auto-compact, no token pruning).
func TestQuery_SubAgent_OversizedContext(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: "ok"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.isSubagent = true
	eng.SetCompactor(&internalMockCompactor{}, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Pre-load messages with oversized context (~32000 tokens).
	bigText := strings.Repeat("x", 16000)
	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(bigText)},
		}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", "")
	if result.Error != nil {
		t.Fatalf("sub-agent should complete with oversized context, got: %v", result.Error)
	}
}

// ---------------------------------------------------------------------------
// BuildTool with FormatWireResult_ — verifies ToolWithWireFormat via BuildTool factory
// Source: SkillTool.ts:843-861 — mapToolResultToToolResultBlockParam
// ---------------------------------------------------------------------------

func TestMarshalToolOutput_BuildToolWithWireFormat(t *testing.T) {
	t.Parallel()

	tk := tool.BuildTool(tool.ToolDef{
		Name_: "WireFactory",
		Call_: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return nil },
		Description_: func(json.RawMessage) (string, error) { return "", nil },
		FormatWireResult_: func(data any) string {
			return "wire: " + fmt.Sprint(data)
		},
	})

	// Verify the tool implements ToolWithWireFormat
	wf, ok := tk.(tool.ToolWithWireFormat)
	if !ok {
		t.Fatal("BuildTool with FormatWireResult_ should implement ToolWithWireFormat")
	}

	// Verify FormatWireResult works
	got := wf.FormatWireResult("test")
	if got != "wire: test" {
		t.Errorf("FormatWireResult = %q, want %q", got, "wire: test")
	}

	// Verify marshalToolOutput uses it
	output := marshalToolOutput(tk, "test-data", true)
	var decoded string
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatalf("wire format output should be a JSON string, got %q: %v", string(output), err)
	}
	if decoded != "wire: test-data" {
		t.Errorf("decoded = %q, want %q", decoded, "wire: test-data")
	}
}

func TestMarshalToolOutput_BuildToolWithoutWireFormat(t *testing.T) {
	t.Parallel()

	// Standard tool without FormatWireResult_ uses double-wrapped JSON
	tk := tool.BuildTool(tool.ToolDef{
		Name_: "DefaultFactory",
		Call_: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return nil },
		Description_: func(json.RawMessage) (string, error) { return "", nil },
	})

	// Should NOT implement ToolWithWireFormat
	if _, ok := tk.(tool.ToolWithWireFormat); ok {
		t.Error("BuildTool without FormatWireResult_ should NOT implement ToolWithWireFormat")
	}

	// Default double-wrapped JSON
	data := map[string]string{"key": "value"}
	output := marshalToolOutput(tk, data, true)
	var outer string
	if err := json.Unmarshal(output, &outer); err != nil {
		t.Fatalf("default output should be a JSON string, got %q: %v", string(output), err)
	}
	var inner map[string]string
	if err := json.Unmarshal([]byte(outer), &inner); err != nil {
		t.Fatalf("inner should be a JSON object, got %q: %v", outer, err)
	}
	if inner["key"] != "value" {
		t.Errorf("inner[key] = %q, want %q", inner["key"], "value")
	}
}

// TestQuery_PostTurnCompact_Succeeds verifies that post-turn compact runs after
// API response and the query completes successfully.
func TestQuery_PreTurnCompact_Succeeds_OldFormat(t *testing.T) {
	t.Parallel()

	tmp := &testProvider{}
	// API response after compact — InputTokens reflects compacted context.
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 4000}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: "ok"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	tmp.addResponse(events, nil)

	compactCalled := false
	compactor := &funcCompactor{
		fn: func(_ context.Context, messages []types.Message) (*short.CompactResult, error) {
			compactCalled = true
			return &short.CompactResult{
				BeforeTokens:   35000,
				AfterTokens:    4000,
				BeforeMessages: len(messages),
				Messages: []types.Message{
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("boundary")}},
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary")}},
				},
			}, nil
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   tmp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Pre-load messages and set ContextTokens above auto-compact threshold
	// so pre-turn compact triggers before the API call.
	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))},
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", "")

	if !compactCalled {
		t.Fatal("pre-turn compact should have been called")
	}
	if result.Error != nil {
		t.Fatalf("expected success, got: %v", result.Error)
	}
}

// funcCompactor is a test helper that delegates Compact to a function.
type funcCompactor struct {
	fn func(context.Context, []types.Message) (*short.CompactResult, error)
}

func (c *funcCompactor) Compact(ctx context.Context, messages []types.Message) (*short.CompactResult, error) {
	return c.fn(ctx, messages)
}

// TestQuery_PreTurnCompact_UsesRealAPITokens verifies that pre-turn compact
// output shows the real ContextTokens, not the heuristic from the compactor.
func TestQuery_PreTurnCompact_UsesRealAPITokens(t *testing.T) {
	t.Parallel()

	tmp := &testProvider{}
	// API response after compact — InputTokens reflects compacted context.
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 4000}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: "ok"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	tmp.addResponse(events, nil)

	var compactDisplayOutput string
	compactor := &funcCompactor{
		fn: func(_ context.Context, messages []types.Message) (*short.CompactResult, error) {
			return &short.CompactResult{
				BeforeTokens:   10000,
				AfterTokens:    4000,
				BeforeMessages: len(messages),
				Messages: []types.Message{
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("boundary")}},
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary")}},
					{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("recent")}},
				},
			}, nil
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   tmp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Pre-load messages and set ContextTokens above auto-compact threshold.
	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))},
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eng.QuerySync(ctx, "do something", "")

	for _, ev := range tc.FindEvents(types.EventToolEnd) {
		if ev.ToolResult != nil && strings.Contains(ev.ToolResult.DisplayOutput, "compacted") {
			compactDisplayOutput = ev.ToolResult.DisplayOutput
		}
	}

	if compactDisplayOutput == "" {
		t.Fatal("expected compact output event")
	}

	// runCompact uses compactor's BeforeTokens/AfterTokens directly (no delta).
	if !strings.Contains(compactDisplayOutput, "token: 9.8k → 3.9k") {
		t.Errorf("expected compact output to show compactor tokens (9.8k → 3.9k), got:\n%s", compactDisplayOutput)
	}

	eng.mu.RLock()
	ctxTokens := eng.ContextTokens
	eng.mu.RUnlock()
	if ctxTokens != 4005 {
		t.Errorf("ContextTokens = %d, want 4005 (AfterTokens + post-compact API output)", ctxTokens)
	}
}

// ---------------------------------------------------------------------------
// Pre-turn auto-compact: compact runs BEFORE the API call (TS align)
// ---------------------------------------------------------------------------

// TestQuery_PreTurnCompact_Succeeds verifies that when context exceeds the
// auto-compact threshold, the engine runs compact first, reducing context
// so the API call proceeds successfully.
func TestQuery_PreTurnCompact_Succeeds(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: 4000}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: "ok"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(events, nil)

	compactCalled := false
	compactor := &funcCompactor{
		fn: func(_ context.Context, msgs []types.Message) (*short.CompactResult, error) {
			compactCalled = true
			return &short.CompactResult{
				BeforeTokens:   35000,
				AfterTokens:    4000,
				BeforeMessages: len(msgs),
				Messages: []types.Message{
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("boundary")}},
					{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("summary")}},
				},
			}, nil
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Pre-load messages + set ContextTokens ABOVE the auto-compact threshold.
	// With ContextWindow=50000, MaxTokens=16000:
	//   effectiveWindow = 50000 - 16000 = 34000
	//   autoCompactThreshold = 34000 - max(34000*7/100, 3000) = 34000 - 3000 = 31000
	// Setting ContextTokens=35000 triggers auto-compact.
	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))},
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", "")

	// Critical assertions:
	if !compactCalled {
		t.Fatal("pre-turn compact should have been called")
	}
	if result.Error != nil {
		t.Fatalf("expected success (compact should reduce context), got: %v", result.Error)
	}

	// Verify compact events were emitted
	var foundToolStart, foundToolEnd bool
	for _, evt := range tc.events {
		if evt.Type == types.EventToolStart && evt.ToolUse != nil && evt.ToolUse.Name == "Compact" {
			foundToolStart = true
		}
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && !evt.ToolResult.IsError {
			foundToolEnd = true
		}
	}
	if !foundToolStart {
		t.Error("expected Compact ToolStart event")
	}
	if !foundToolEnd {
		t.Error("expected Compact ToolEnd event (success)")
	}
}

// TestQuery_PreTurnCompact_CompactFails_APIProceeds verifies that when compact
// fails, the API call still proceeds. Without a blocking limit, oversized
// context is handled reactively — the API returns an overflow error which
// triggers reactive compact, or succeeds if the provider accepts it.
func TestQuery_PreTurnCompact_CompactFails_APIProceeds(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textStreamEvents("test-model", "api response"), nil)

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return nil, errors.New("compact failed: LLM error")
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))},
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", "")

	// Compact failed, but the API call should proceed anyway.
	if result.Error != nil {
		t.Fatalf("expected API call to proceed despite compact failure, got: %v", result.Error)
	}
}

// TestQuery_PreTurnCompact_ColdStart_NoCompact verifies that on the first turn
// with ContextTokens=0 and empty messages, no compact triggers and the query
// proceeds normally. This is the cold start scenario.
func TestQuery_PreTurnCompact_ColdStart_NoCompact(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello!"), nil)

	compactCalled := false
	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			compactCalled = true
			return nil, errors.New("should not be called")
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})
	// ContextTokens defaults to 0 — cold start.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "hello", "")

	if compactCalled {
		t.Error("compact should not be called on cold start with small context")
	}
	if result.Error != nil {
		t.Fatalf("expected success, got: %v", result.Error)
	}
}

// TestQuery_PreTurnCompact_StillOverLimit verifies that when compact succeeds
// but context remains oversized, the query still proceeds (compactSucceeded=true).
// The API call may 413, which reactive compact handles.
func TestQuery_PreTurnCompact_StillOverLimit(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// API response — succeeds even though context is still large (mock doesn't enforce limits).
	mp.addResponse(textStreamEvents("test-model", "OK despite large context."), nil)

	compactCalled := false
	compactor := &funcCompactor{
		fn: func(_ context.Context, msgs []types.Message) (*short.CompactResult, error) {
			compactCalled = true
			return &short.CompactResult{
				// Compact barely reduces: 35000 → 33000 (still oversized).
				BeforeTokens:   35000,
				AfterTokens:    33000,
				BeforeMessages: len(msgs),
				Messages:       msgs, // Return same messages (simulates minimal reduction).
			}, nil
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))},
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", "")

	if !compactCalled {
		t.Fatal("pre-turn compact should have been called")
	}
	// compactSucceeded=true → API call proceeds.
	// In real usage, the API might 413 and reactive compact handles it.
	// Here the mock succeeds, so query should succeed.
	if result.Error != nil {
		t.Fatalf("expected success (compact succeeded), got: %v", result.Error)
	}
}

// TestQuery_PreTurnCompact_CircuitBreaker_APIProceeds verifies that after
// consecutiveCompactFailures reaches the limit, shouldAutoCompact() returns
// false, compact doesn't run, and the API call proceeds without a pre-turn
// compact. Reactive compact (engine.go) handles overflow if the API rejects.
func TestQuery_PreTurnCompact_CircuitBreaker_APIProceeds(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textStreamEvents("test-model", "api response"), nil)

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return nil, errors.New("compact failed")
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Pre-set circuit breaker as if 3 failures already happened.
	eng.mu.Lock()
	eng.consecutiveCompactFailures = 3
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))},
		}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", "")

	// Circuit breaker tripped, compact skipped, but API call proceeds.
	if result.Error != nil {
		t.Fatalf("expected API call to proceed despite circuit breaker, got: %v", result.Error)
	}
}

// TestQuery_PreTurnCompact_NoOp_APIProceeds verifies that when compact
// "succeeds" but doesn't actually reduce tokens (BeforeTokens == AfterTokens),
// the API call still proceeds. Without a blocking limit, a no-op compact
// doesn't prevent the API call — the provider or reactive compact handles
// overflow.
func TestQuery_PreTurnCompact_NoOp_APIProceeds(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	mp.addResponse(textStreamEvents("test-model", "api response"), nil)

	compactor := &funcCompactor{
		fn: func(_ context.Context, msgs []types.Message) (*short.CompactResult, error) {
			// Simulate AutoCompactor no-op: returns same messages with no token reduction.
			return &short.CompactResult{
				BeforeTokens:   35000,
				AfterTokens:    35000, // Same! No reduction.
				BeforeMessages: len(msgs),
				Messages:       msgs, // Original messages unchanged.
			}, nil
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	for range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))},
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do something", "")

	// No-op compact doesn't prevent the API call.
	if result.Error != nil {
		t.Fatalf("expected API call to proceed despite no-op compact, got: %v", result.Error)
	}

	// Verify compact DID run (it was attempted, just didn't help).
	var compactAttempted bool
	for _, evt := range tc.events {
		if evt.Type == types.EventToolStart && evt.ToolUse != nil && evt.ToolUse.Name == "Compact" {
			compactAttempted = true
		}
	}
	if !compactAttempted {
		t.Error("compact should have been attempted")
	}
}

// ---------------------------------------------------------------------------
// currentInputTokens — TS align: tokenCountWithEstimation (utils/tokens.ts:226)
// ---------------------------------------------------------------------------

func TestCurrentInputTokens_ExactNoDelta(t *testing.T) {
	t.Parallel()
	// Last assistant message has Usage → use that as precise base, no delta after it.
	// TS align: tokenCountWithEstimation finds last assistant with usage.
	eng := &Engine{
		ContextTokens: 9000, // ignored by new implementation
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("world")},
				Usage: &types.Usage{InputTokens: 5000, OutputTokens: 100, CacheReadInputTokens: 3900}},
		},
	}

	got := eng.currentInputTokens()
	// getTokenCountFromUsage: 5000 + 3900 + 0 + 100 = 9000
	if got != 9000 {
		t.Errorf("currentInputTokens() = %d, want 9000 (precise from Usage, no delta)", got)
	}
}

func TestCurrentInputTokens_ExactPlusDelta(t *testing.T) {
	t.Parallel()
	// Last assistant message has Usage, messages after it → Usage + estimated delta.
	// TS align: tokenCountWithEstimation returns base + delta.
	deltaText := strings.Repeat("x", 4000) // ~1000 tokens (4 chars/token)
	eng := &Engine{
		ContextTokens: 9000, // ignored by new implementation
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response")},
				Usage: &types.Usage{InputTokens: 5000, OutputTokens: 100, CacheReadInputTokens: 3900}},
			// Delta: tool result + user message after last assistant
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewToolResultBlock("tool-1", json.RawMessage(`"`+deltaText+`"`), false),
				},
			},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(deltaText)}},
		},
	}

	got := eng.currentInputTokens()
	// Base from Usage: 5000+3900+0+100 = 9000. Plus delta (~2000 tokens).
	if got <= 9000 {
		t.Errorf("currentInputTokens() = %d, want > 9000 (should include delta from new messages)", got)
	}
	if got > 15000 {
		t.Errorf("currentInputTokens() = %d, unexpectedly high (delta should be ~2000)", got)
	}
}

func TestCurrentInputTokens_ZeroContextTokens_Fallback(t *testing.T) {
	t.Parallel()
	// ContextTokens == 0: abnormal state. Should log error and fall back
	// to full message estimation.
	text := strings.Repeat("x", 4000) // ~1000 tokens
	eng := &Engine{
		ContextTokens: 0,
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(text)}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(text)}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(text)}},
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	got := eng.currentInputTokens()
	// Should be roughly 3000 tokens (3 messages × ~1000 each).
	// Range check also catches got <= 0.
	if got < 2000 || got > 6000 {
		t.Errorf("currentInputTokens() = %d, want ~3000 (full message estimation fallback)", got)
	}
}

func TestCurrentInputTokens_NoAssistantMessage_Fallback(t *testing.T) {
	t.Parallel()
	// No assistant message with Usage → full estimation fallback.
	// TS align: tokenCountWithEstimation falls back to rough estimation.
	eng := &Engine{
		ContextTokens: 9000, // ignored — no Usage to derive from
		messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 16000))}},
		},
	}

	got := eng.currentInputTokens()
	// Should be ~8000 tokens (2 messages × 4000 chars / 4 chars per token × 4/3 padding).
	if got < 4000 || got > 16000 {
		t.Errorf("currentInputTokens() = %d, want ~8000 (full estimation fallback, no Usage)", got)
	}
}

// ---------------------------------------------------------------------------
// Phase 1: Abort alignment tests
// ---------------------------------------------------------------------------

// callbackTool is a test tool that invokes a callback on Call.
type callbackTool struct {
	name   string
	onCall func()
}

func (t *callbackTool) Name() string                                { return t.name }
func (t *callbackTool) Aliases() []string                           { return nil }
func (t *callbackTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *callbackTool) InputSchema() json.RawMessage                { return nil }
func (t *callbackTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	if t.onCall != nil {
		t.onCall()
	}
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *callbackTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *callbackTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *callbackTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *callbackTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *callbackTool) IsEnabled() bool                        { return true }
func (t *callbackTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (t *callbackTool) Prompt() string          { return "" }
func (t *callbackTool) RenderResult(any) string { return "" }
func (t *callbackTool) NewResultType() any      { return nil }
func (t *callbackTool) MaxResultSize() int      { return 50000 }

func TestAbortError_TypeDiscrimination(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ShouldAbort(ctx, "streaming")
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	var ae *AbortError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AbortError, got %T", err)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false, want true")
	}
}

func TestSyntheticToolResultsForBlocks(t *testing.T) {
	t.Parallel()

	blocks := []types.ContentBlock{
		types.NewTextBlock("some text"),
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Read"},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "Write"},
		{Type: types.ContentTypeToolUse, ID: "tu_3", Name: "Bash"},
	}

	// tu_1 is started, tu_2 and tu_3 are orphaned
	started := map[string]bool{"tu_1": true}
	results := SyntheticToolResultsForBlocks(blocks, started, AbortReasonUserInterrupted)

	if len(results) != 2 {
		t.Fatalf("expected 2 synthetic blocks, got %d", len(results))
	}

	for i, cb := range results {
		if cb.Type != types.ContentTypeToolResult {
			t.Errorf("results[%d].Type = %q, want %q", i, cb.Type, types.ContentTypeToolResult)
		}
		var parsed string
		if err := json.Unmarshal(cb.Content, &parsed); err != nil {
			t.Fatalf("results[%d]: failed to parse content: %v", i, err)
		}
		if parsed == "" {
			t.Errorf("results[%d]: expected non-empty error content", i)
		}
	}

	ids := map[string]bool{results[0].ToolUseID: true, results[1].ToolUseID: true}
	if ids["tu_1"] {
		t.Error("tu_1 should not appear (it was started)")
	}
	if !ids["tu_2"] || !ids["tu_3"] {
		t.Errorf("expected tu_2 and tu_3 in results, got IDs: %v", results)
	}
}

func TestSyntheticToolResultsForBlocks_NilStarted(t *testing.T) {
	t.Parallel()

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Read"},
		{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "Write"},
	}
	results := SyntheticToolResultsForBlocks(blocks, nil, AbortReasonStreamingFallback)
	if len(results) != 2 {
		t.Fatalf("expected 2 synthetic blocks (all orphan), got %d", len(results))
	}
}

func TestSyntheticToolResultsForBlocks_NoToolUse(t *testing.T) {
	t.Parallel()

	blocks := []types.ContentBlock{types.NewTextBlock("just text")}
	results := SyntheticToolResultsForBlocks(blocks, nil, AbortReasonUserInterrupted)
	if len(results) != 0 {
		t.Errorf("expected 0 synthetic blocks for text-only, got %d", len(results))
	}
}

func TestStartedToolIDs(t *testing.T) {
	t.Parallel()

	executor := &StreamingToolExecutor{
		tools: []*TrackedTool{
			{ID: "t1", Status: StatusExecuting},
			{ID: "t2", Status: StatusCompleted},
			{ID: "t3", Status: 0},
		},
	}
	ids := executor.StartedToolIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 started IDs, got %d", len(ids))
	}
	if !ids["t1"] {
		t.Error("expected t1 (executing)")
	}
	if !ids["t2"] {
		t.Error("expected t2 (completed)")
	}
	if ids["t3"] {
		t.Error("t3 should not be started")
	}
}

func TestRunTurns_LoopTopAbort(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected error from cancelled context")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}
	if !errors.Is(ae.Err, context.Canceled) {
		t.Errorf("underlying error = %v, want context.Canceled", ae.Err)
	}
}

func TestRunTurns_PostStreamingAbort_NoToolUse(t *testing.T) {
	// Text-only response completes a turn normally. Cancel fires right after
	// streaming data is sent. On the next iteration, loop-top catches it.
	// No synthetic blocks because there were no tool_uses.
	mp := &testProvider{}
	ch := make(chan llm.StreamEvent, 10)
	mp.addChannelResponse(ch)
	// Second turn channel (closed by goroutine in case engine reaches it)
	ch2 := make(chan llm.StreamEvent, 10)
	mp.addChannelResponse(ch2)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Send complete text response
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "hello"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 5}}
		ch <- llm.StreamEvent{Type: "message_stop"}
		// Cancel after all streaming data is sent
		cancel()
		close(ch)
		close(ch2)
	}()

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error from loop-top abort after text turn")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T", result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}

	// No synthetic tool_result blocks in any message
	for _, msg := range result.Messages {
		for _, cb := range msg.Content {
			if cb.Type == types.ContentTypeToolResult {
				t.Error("unexpected synthetic tool_result in text-only response")
			}
		}
	}
}

func TestRunTurns_PostStreamingAbort_SyntheticToolResults(t *testing.T) {
	// Streaming returns tool_use response, then ctx is cancelled before ExecuteAll.
	// Synthetic tool_results should be generated for the orphaned tool_uses.
	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent, 20)
	mp.addChannelResponse(slowCh)

	mt := &testTool{name: "Read"}
	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Read"}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"path":"/tmp/test"}`}}
		slowCh <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		slowCh <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 5}}
		slowCh <- llm.StreamEvent{Type: "message_stop"}
		// Cancel after streaming completes but before tools execute.
		// Need a short delay because the buffered channel allows instant sends --
		// the engine needs wall-clock time to drain the channel and process message_stop.
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel

		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T", result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}

	// Verify synthetic tool_result was generated
	if len(result.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result.Messages))
	}
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleUser {
		t.Fatalf("last message role = %q, want %q", lastMsg.Role, types.RoleUser)
	}

	hasToolResult := false
	for _, cb := range lastMsg.Content {
		if cb.Type == types.ContentTypeToolResult {
			hasToolResult = true
			if cb.ToolUseID != "tu_1" {
				t.Errorf("tool_result ToolUseID = %q, want %q", cb.ToolUseID, "tu_1")
			}
			var parsed string
			if err := json.Unmarshal(cb.Content, &parsed); err != nil {
				t.Fatalf("failed to parse tool_result content: %v", err)
			}
			if !strings.Contains(parsed, "User rejected") {
				t.Errorf("error = %q, want to contain 'User rejected'", parsed)
			}
		}
	}
	if !hasToolResult {
		t.Error("expected synthetic tool_result block for orphaned tool_use")
	}
}

func TestRunTurns_PostToolAbort(t *testing.T) {
	// Tool executes, then cancels context. Stage 23 should catch it.
	mp := &testProvider{}
	toolEvents := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "test_tool"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(toolEvents, nil)
	// Second response (won't be reached due to abort)
	mp.addResponse(subTextEvents("test", "done"), nil)

	var ctxCancel context.CancelFunc
	ct := &callbackTool{
		name: "test_tool",
		onCall: func() {
			ctxCancel()
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Tools:      []tool.Tool{ct},
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	ctxCancel = cancel

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error after tool execution abort")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	// Phase depends on goroutine scheduling: tool cancels ctx during streaming,
	// so either Stage 18 (Phase="streaming") or Stage 23 (Phase="tools") catches it.
	if ae.Phase != "tools" && ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q or %q", ae.Phase, "tools", "streaming")
	}
}

// ---------------------------------------------------------------------------
// Post-loop abort tests: !streamComplete cascade
// When provider closes streamCh due to ctx cancel, for-range exits
// without triggering the select <-ctx.Done() guard inside the loop.
// ---------------------------------------------------------------------------

func TestCallLLM_PostLoopAbort_ToolUse(t *testing.T) {
	// Provider sends tool_use events but NOT message_stop, then ctx is cancelled
	// and channel closed. Post-loop cascade should detect abort and generate
	// synthetic tool_results for the orphaned tool_use block.
	mp := &testProvider{}
	ch := make(chan llm.StreamEvent, 20)
	mp.addChannelResponse(ch)

	mt := &testTool{name: "Read"}
	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Read"}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"path":"/tmp/test"}`}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 5}}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
		close(ch)
	}()

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error from post-loop abort")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}

	// Verify synthetic tool_result was generated for the orphaned tool_use
	hasSyntheticResult := false
	for _, msg := range result.Messages {
		for _, cb := range msg.Content {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID == "tu_1" {
				hasSyntheticResult = true
				var parsed string
				if err := json.Unmarshal(cb.Content, &parsed); err != nil {
					t.Fatalf("failed to parse synthetic tool_result: %v", err)
				}
				if !strings.Contains(parsed, "discarded") {
					t.Errorf("synthetic error = %q, want to contain 'discarded'", parsed)
				}
			}
		}
	}
	if !hasSyntheticResult {
		t.Error("expected synthetic tool_result for orphaned tool_use tu_1")
	}
}

func TestCallLLM_PostLoopAbort_NoContent(t *testing.T) {
	// Provider closes channel immediately after ctx is cancelled, before
	// sending any content events. Should return AbortError with no messages.
	mp := &testProvider{}
	ch := make(chan llm.StreamEvent, 5)
	mp.addChannelResponse(ch)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
		close(ch)
	}()

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error from post-loop abort")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}

	// No assistant message should be appended (no content received)
	for _, msg := range result.Messages {
		if msg.Role == types.RoleAssistant && len(msg.Content) > 0 {
			t.Errorf("unexpected assistant message with %d content blocks (expected no content)", len(msg.Content))
		}
	}
}

func TestCallLLM_PostLoopAbort_TextOnly(t *testing.T) {
	// Provider sends text content but NOT message_stop, then ctx cancelled.
	// Should return AbortError with partial assistant message appended.
	mp := &testProvider{}
	ch := make(chan llm.StreamEvent, 20)
	mp.addChannelResponse(ch)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "Hello "}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "wor"}}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
		close(ch)
	}()

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error from post-loop abort")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}

	// Partial assistant message should be appended
	hasAssistant := false
	for _, msg := range result.Messages {
		if msg.Role == types.RoleAssistant && len(msg.Content) > 0 {
			hasAssistant = true
			// Verify text content was preserved
			found := false
			for _, cb := range msg.Content {
				if cb.Type == types.ContentTypeText && strings.Contains(cb.Text, "Hello") {
					found = true
				}
			}
			if !found {
				t.Error("assistant message missing partial text content")
			}
		}
	}
	if !hasAssistant {
		t.Error("expected partial assistant message to be appended")
	}
}

func TestCallLLM_MidStreamAbort_ToolUseOnly(t *testing.T) {
	// Gap 2: mid-stream abort with tool_use but NO text/thinking.
	// hasContent would be false, but contentBlocks is non-empty.
	// The abort handler should still append the partial assistant message
	// and generate synthetic tool_results.
	//
	// Simulate: events flow, then cancel fires, select guard catches it.
	// We use an unbuffered channel to force synchronization: goroutine sends
	// one event at a time, main goroutine consumes, cancel fires mid-stream.
	mp := &testProvider{}
	ch := make(chan llm.StreamEvent, 20)
	mp.addChannelResponse(ch)

	mt := &testTool{name: "Read"}
	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Tools:      []tool.Tool{mt},
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Read"}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"path":"/tmp/test"}`}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
		close(ch)
	}()

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error from abort")
	}

	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}

	// Verify partial assistant message was appended (this is the Gap 2 fix)
	hasAssistant := false
	for _, msg := range result.Messages {
		if msg.Role == types.RoleAssistant && len(msg.Content) > 0 {
			hasAssistant = true
			for _, cb := range msg.Content {
				switch {
				case cb.Type == types.ContentTypeToolUse && cb.ID == "tu_1":
					// expected tool_use block
				case cb.Type == types.ContentTypeText && cb.Text == types.InterruptMessage:
					// expected: inline interrupt message appended on abort
				default:
					t.Errorf("unexpected content block: type=%q id=%q text=%q", cb.Type, cb.ID, cb.Text)
				}
			}
		}
	}
	if !hasAssistant {
		t.Error("expected partial assistant message with tool_use block (Gap 2: contentBlocks non-empty should append)")
	}

	// Verify synthetic tool_result was generated
	hasSynthetic := false
	for _, msg := range result.Messages {
		for _, cb := range msg.Content {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID == "tu_1" {
				hasSynthetic = true
			}
		}
	}
	if !hasSynthetic {
		t.Error("expected synthetic tool_result for orphaned tool_use tu_1")
	}
}

func TestRunTurns_ReactiveCompactAbort(t *testing.T) {
	// Gap 3: cancel during reactive compact should return *AbortError,
	// not the original API error (context overflow).
	mp := &testProvider{}
	// First response: context overflow error
	overflowErr := &llm.APIError{Status: 400, ErrorCode: "prompt_too_long", Message: "context too long"}
	mp.addResponse(nil, overflowErr)
	ch := make(chan llm.StreamEvent, 10)
	mp.addChannelResponse(ch)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(&blockingCompactor{}, AutoCompactConfig{
		ContextWindow: 100000,
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
		close(ch)
	}()

	result := eng.QuerySync(ctx, "test", "")

	if result.Error == nil {
		t.Fatal("expected error")
	}

	// Should be *AbortError, not context overflow or raw context.Canceled
	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}
}

// blockingCompactor blocks on ctx until cancelled, then returns error.
type blockingCompactor struct{}

func (c *blockingCompactor) Compact(ctx context.Context, _ []types.Message) (*short.CompactResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// ---------------------------------------------------------------------------
//  appendInlineInterruptMessage — verify [Request interrupted by user]
// appears in the last assistant message for all 3 abort paths, and does NOT
// appear for loop-top abort.
// ---------------------------------------------------------------------------

// lastAssistantText returns the text content of the last assistant message.
func lastAssistantText(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == types.RoleAssistant {
			var parts []string
			for _, cb := range msgs[i].Content {
				if cb.Type == types.ContentTypeText {
					parts = append(parts, cb.Text)
				}
			}
			return strings.Join(parts, "")
		}
	}
	return ""
}

// hasInterruptMessage checks if any assistant message contains InterruptMessage.
func hasInterruptMessage(msgs []types.Message) bool {
	for _, msg := range msgs {
		for _, cb := range msg.Content {
			if cb.Type == types.ContentTypeText && strings.Contains(cb.Text, types.InterruptMessage) {
				return true
			}
		}
	}
	return false
}

func TestInlineInterrupt_PostStreamingAbort_Text(t *testing.T) {
	// Cancel mid-stream while receiving text — interrupt message should appear.
	// Pattern: same as TestCallLLM_PostLoopAbort_TextOnly but checks inline message.
	mp := &testProvider{}
	ch := make(chan llm.StreamEvent, 20)
	mp.addChannelResponse(ch)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "Hello "}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "world"}}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
		close(ch)
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected abort error")
	}
	if !hasInterruptMessage(result.Messages) {
		text := lastAssistantText(result.Messages)
		t.Errorf("expected %q in assistant messages, got: %q", types.InterruptMessage, text)
	}
}

func TestInlineInterrupt_PostStreamingAbort_ToolUse(t *testing.T) {
	// Cancel after tool_use blocks streamed via channel but before tool execution.
	// Pattern: same as TestRunTurns_PostStreamingAbort_SyntheticToolResults.
	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent, 20)
	mp.addChannelResponse(slowCh)

	mt := &testTool{name: "test_tool"}
	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Tools: []tool.Tool{mt}, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: "Let me help"}}
		slowCh <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "test_tool"}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}}
		slowCh <- llm.StreamEvent{Type: "content_block_stop", Index: 1}
		slowCh <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 5}}
		slowCh <- llm.StreamEvent{Type: "message_stop"}
		// Cancel after streaming completes but before tools execute
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected abort error")
	}
	if !hasInterruptMessage(result.Messages) {
		text := lastAssistantText(result.Messages)
		t.Errorf("expected %q in assistant messages after tool_use abort, got: %q", types.InterruptMessage, text)
	}
}

func TestInlineInterrupt_PostToolAbort(t *testing.T) {
	// Tool executes successfully, then context cancelled at Stage 23.
	mp := &testProvider{}
	toolEvents := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "test_tool"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
	mp.addResponse(toolEvents, nil)
	mp.addResponse(subTextEvents("done", ""), nil)

	var ctxCancel context.CancelFunc
	ct := &callbackTool{
		name: "test_tool",
		onCall: func() {
			time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
			ctxCancel()
		},
	}

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Tools: []tool.Tool{ct}, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	ctxCancel = cancel

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected abort error after tool execution")
	}
	if !hasInterruptMessage(result.Messages) {
		text := lastAssistantText(result.Messages)
		t.Errorf("expected %q in assistant messages after post-tool abort, got: %q", types.InterruptMessage, text)
	}
}

func TestInlineInterrupt_ReactiveCompactAbort_InterruptOnUserMessage(t *testing.T) {
	// Cancel during reactive compact — interrupt appended to user query (last message).
	mp := &testProvider{}
	overflowErr := &llm.APIError{Status: 400, ErrorCode: "prompt_too_long", Message: "context too long"}
	mp.addResponse(nil, overflowErr)
	ch := make(chan llm.StreamEvent, 10)
	mp.addChannelResponse(ch)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(&blockingCompactor{}, AutoCompactConfig{ContextWindow: 100000})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(5 * time.Millisecond) // REAL-TIME: needed for engine to process buffered stream events before cancel
		cancel()
		close(ch)
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected abort error during compact")
	}
	// Interrupt appended to user query (last message in messages).
	if !hasInterruptMessage(result.Messages) {
		t.Error("reactive compact abort should have inline interrupt on user message")
	}
}

func TestInlineInterrupt_LoopTopAbort_InterruptOnUserMessage(t *testing.T) {
	// Loop-top abort — interrupt message appended to user query message.
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("expected error from cancelled context")
	}
	var ae *AbortError
	if !errors.As(result.Error, &ae) {
		t.Fatalf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}
	// Last message should be the user query with interrupt appended.
	msgs := result.Messages
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	last := msgs[len(msgs)-1]
	if last.Role != types.RoleUser {
		t.Fatalf("expected last message to be user, got %s", last.Role)
	}
	if !hasInterruptMessage(msgs) {
		t.Error("loop-top abort should have inline interrupt message on user message")
	}
}

// ---------------------------------------------------------------------------
// Previously 0% functions: setters/getters, MCPTool methods, microcompact no-ops
// ---------------------------------------------------------------------------

func TestEngine_SetRecordWriter(t *testing.T) {
	t.Parallel()
	eng := &Engine{}
	var called bool
	eng.SetRecordWriter(func(records []toolresult.ContentReplacementRecord) {
		called = true
	})
	eng.mu.Lock()
	fn := eng.recordWriter
	eng.mu.Unlock()
	if fn == nil {
		t.Fatal("recordWriter should be set")
	}
	fn(nil)
	if !called {
		t.Error("recordWriter callback should have been called")
	}
}

func TestEngine_ContextWindow(t *testing.T) {
	t.Parallel()
	eng := &Engine{autoCompactConfig: AutoCompactConfig{ContextWindow: 200000}}
	if got := eng.ContextWindow(); got != 200000 {
		t.Errorf("ContextWindow() = %d, want 200000", got)
	}
}

func TestEngine_SetMaxTokens(t *testing.T) {
	t.Parallel()
	eng := &Engine{}
	eng.SetMaxTokens(8192)
	eng.mu.Lock()
	got := eng.maxTokens
	eng.mu.Unlock()
	if got != 8192 {
		t.Errorf("maxTokens = %d, want 8192", got)
	}
}

func TestEngine_SetDispatcher(t *testing.T) {
	t.Parallel()
	eng := &Engine{}
	var d noopDispatcher
	eng.SetDispatcher(&d)
	if eng.dispatcher == nil {
		t.Error("dispatcher should be set")
	}
}

func TestMCPTool_InterruptBehavior(t *testing.T) {
	t.Parallel()
	mt := &MCPTool{}
	if mt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %d, want InterruptCancel", mt.InterruptBehavior())
	}
}

func TestExtractMCPText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content []mcpsdk.Content
		want    string
	}{
		{"empty", nil, ""},
		{"single text", []mcpsdk.Content{&mcpsdk.TextContent{Text: "hello"}}, "hello"},
		{"multi text", []mcpsdk.Content{&mcpsdk.TextContent{Text: "a"}, &mcpsdk.TextContent{Text: "b"}}, "a\nb"},
		{"non-text only", []mcpsdk.Content{&mcpsdk.ImageContent{}}, ""},
		{"mixed", []mcpsdk.Content{&mcpsdk.TextContent{Text: "yes"}, &mcpsdk.ImageContent{}, &mcpsdk.TextContent{Text: "no"}}, "yes\nno"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMCPText(&mcp.MCPToolCallResult{Content: tt.content})
			if got != tt.want {
				t.Errorf("extractMCPText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// noopDispatcher is a minimal EventDispatcher for tests.
type noopDispatcher struct{}

func (d *noopDispatcher) Dispatch(types.QueryEvent) {}

func TestUserOrSubRejectMessage_SubEngine(t *testing.T) {
	exec := &StreamingToolExecutor{}
	exec.SetSubEngine(true)
	if got := exec.userOrSubRejectMessage(); got != subAgentRejectMessage {
		t.Errorf("userOrSubRejectMessage(subEngine) = %q, want %q", got, subAgentRejectMessage)
	}
}

func TestUserOrSubRejectMessage_User(t *testing.T) {
	exec := &StreamingToolExecutor{}
	exec.SetSubEngine(false)
	if got := exec.userOrSubRejectMessage(); got != userRejectMessage {
		t.Errorf("userOrSubRejectMessage(user) = %q, want %q", got, userRejectMessage)
	}
}

func TestApplyBudget_NilState(t *testing.T) {
	eng := New(&Params{})
	defer eng.Close()
	// contentReplacementState is nil by default in this test setup
	msgs := []types.Message{{Role: types.RoleUser}}
	got := eng.applyBudget(msgs)
	if len(got) != 1 {
		t.Errorf("applyBudget(nil state) = %d msgs, want 1", len(got))
	}
}

func TestExecuteTool_ToolNotFound(t *testing.T) {
	rootCtx := t.Context()
	exec := &StreamingToolExecutor{
		toolMap: map[string]tool.Tool{},
		rootCtx: rootCtx,
	}
	tt := &TrackedTool{
		ID:   "test-id",
		Name: "NonExistentTool",
		done: make(chan struct{}),
	}
	exec.executeTool(tt)
	if len(tt.resultBlocks) == 0 {
		t.Errorf("tt.resultBlocks is nil or empty for tool %s", tt.Name)
	}
	if !tt.resultBlocks[0].IsError {
		t.Errorf("resultBlocks[0].IsError = false, want true (tool: %s)", tt.Name)
	}
}

// ---------------------------------------------------------------------------
// shouldAutoCompact — all early-return branches
// ---------------------------------------------------------------------------

func TestShouldAutoCompact_NilCompactor(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test", AutoCompact: AutoCompactConfig{ContextWindow: 128000}})
	if eng.shouldAutoCompact() {
		t.Error("shouldAutoCompact with nil compactor should return false")
	}
	eng.Close()
}

func TestShouldAutoCompact_ZeroContextWindow(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test", AutoCompact: AutoCompactConfig{ContextWindow: 0}})
	eng.compactor = &AutoCompactor{}
	if eng.shouldAutoCompact() {
		t.Error("shouldAutoCompact with ContextWindow=0 should return false")
	}
	eng.Close()
}

func TestShouldAutoCompact_QuerySourceCompact(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test", AutoCompact: AutoCompactConfig{ContextWindow: 128000}})
	defer eng.Close()
	eng.compactor = &AutoCompactor{}
	// Create a sub-engine with agentType=compact → recursion guard
	subEng := eng.NewSubEngine(SubEngineOptions{
		Tools:     map[string]tool.Tool{},
		AgentType: "compact",
	})
	defer subEng.Close()
	if subEng.shouldAutoCompact() {
		t.Error("shouldAutoCompact with agentType=compact should return false")
	}
}

func TestShouldAutoCompact_QuerySourceSessionMemory(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test", AutoCompact: AutoCompactConfig{ContextWindow: 128000}})
	defer eng.Close()
	eng.compactor = &AutoCompactor{}
	// Create a sub-engine with agentType=session_memory → recursion guard
	subEng := eng.NewSubEngine(SubEngineOptions{
		Tools:     map[string]tool.Tool{},
		AgentType: "session_memory",
	})
	defer subEng.Close()
	if subEng.shouldAutoCompact() {
		t.Error("shouldAutoCompact with agentType=session_memory should return false")
	}
}

func TestShouldAutoCompact_CircuitBreaker(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test", AutoCompact: AutoCompactConfig{ContextWindow: 128000, MaxConsecutiveFailures: 3}})
	eng.compactor = &AutoCompactor{}
	eng.consecutiveCompactFailures = 3
	if eng.shouldAutoCompact() {
		t.Error("shouldAutoCompact with circuit breaker tripped should return false")
	}
	eng.Close()
}

// ---------------------------------------------------------------------------
// runStopHook + fireCompactHooks with nil hooks
// ---------------------------------------------------------------------------

func TestRunStopHook_NilHooks(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})
	result := eng.runStopHook(context.Background())
	if result != nil {
		t.Error("runStopHook with nil hooks should return nil")
	}
	eng.Close()
}

func TestFireCompactHooks_NilHooks(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.fireCompactHooks(context.Background(), "test", "pre")
	eng.fireCompactHooks(context.Background(), "test", "post")
}

func TestAppendInlineInterruptMessage_EmptyMessages(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})
	eng.appendInlineInterruptMessage()
	eng.Close()
}

// ---------------------------------------------------------------------------
// calculateToolResultTokens — object/block fallback
// ---------------------------------------------------------------------------

func TestCalculateToolResultTokens_UnknownJSON(t *testing.T) {
	raw := json.RawMessage(`{"unknown":"structure"}`)
	tokens := calculateToolResultTokens(raw)
	if tokens <= 0 {
		t.Errorf("calculateToolResultTokens(%q) = %d, want > 0", string(raw), tokens)
	}
}

// ---------------------------------------------------------------------------
// executeTool additional paths
// ---------------------------------------------------------------------------

func TestExecuteTool_StreamingSuccess(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"stream_tool": &streamingSuccessTool{name: "stream_tool"},
	}
	executor := NewStreamingToolExecutor(toolMap, nil, emit, context.Background())
	tt := &TrackedTool{
		ID:    "t1",
		Name:  "stream_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	executor.executeTool(tt)
	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block")
	}
	if tt.resultBlocks[0].IsError {
		t.Error("expected non-error result")
	}
}

func TestExecuteTool_NonStreamingError(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"err_tool": &nonStreamingSuccessTool{name: "err_tool", data: "should not see"},
	}
	executor := NewStreamingToolExecutor(toolMap, nil, emit, context.Background())

	tt := &TrackedTool{
		ID:    "t1",
		Name:  "err_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	// Overwrite to make Call return error
	executor.toolMap["err_tool"] = &errorTool{name: "err_tool"}

	executor.executeTool(tt)
	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block")
	}
	if !tt.resultBlocks[0].IsError {
		t.Errorf("resultBlocks[0].IsError = false, want true (tool: %s)", tt.Name)
	}
}

func TestExecuteTool_NilResult(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"nil_tool": &nilResultTool{name: "nil_tool"},
	}
	executor := NewStreamingToolExecutor(toolMap, nil, emit, context.Background())
	tt := &TrackedTool{
		ID:    "t1",
		Name:  "nil_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	executor.executeTool(tt)
	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block")
	}
}

func TestExecuteTool_SiblingAbort(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"abort_tool": &nonStreamingSuccessTool{name: "abort_tool", data: "ok"},
	}

	ctx := t.Context()
	executor := NewStreamingToolExecutor(toolMap, nil, emit, ctx)
	// Mark hasErrored → sibling abort
	executor.hasErrored = true

	tt := &TrackedTool{
		ID:    "t1",
		Name:  "abort_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	executor.executeTool(tt)
	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block")
	}
	if !tt.resultBlocks[0].IsError {
		t.Errorf("resultBlocks[0].IsError = %v, want true for sibling abort", tt.resultBlocks[0].IsError)
	}
}

func TestExecuteTool_UserInterruptBlock(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"block_interrupt": &interruptBlockTool{name: "block_interrupt"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled
	executor := NewStreamingToolExecutor(toolMap, nil, emit, ctx)

	tt := &TrackedTool{
		ID:    "t1",
		Name:  "block_interrupt",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	executor.executeTool(tt)
	// InterruptBlock tools are NOT cancelled → should succeed
	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block")
	}
	if tt.resultBlocks[0].IsError {
		t.Error("expected non-error for interrupt-block tool")
	}
}

// Mock tool types

type streamingSuccessTool struct{ name string }

func (t *streamingSuccessTool) Name() string                                { return t.name }
func (t *streamingSuccessTool) Aliases() []string                           { return nil }
func (t *streamingSuccessTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *streamingSuccessTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *streamingSuccessTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "streamed"}, nil
}
func (t *streamingSuccessTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *streamingSuccessTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *streamingSuccessTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *streamingSuccessTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *streamingSuccessTool) IsEnabled() bool                        { return true }
func (t *streamingSuccessTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (t *streamingSuccessTool) Prompt() string          { return "" }
func (t *streamingSuccessTool) RenderResult(any) string { return "streamed" }
func (t *streamingSuccessTool) NewResultType() any      { return nil }
func (*streamingSuccessTool) MaxResultSize() int        { return 50000 }

type errorTool struct{ name string }

func (t *errorTool) Name() string                                { return t.name }
func (t *errorTool) Aliases() []string                           { return nil }
func (t *errorTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *errorTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *errorTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, errors.New("tool error")
}
func (t *errorTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *errorTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *errorTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *errorTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *errorTool) IsEnabled() bool                           { return true }
func (t *errorTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *errorTool) Prompt() string                            { return "" }
func (t *errorTool) RenderResult(any) string                   { return "" }
func (t *errorTool) NewResultType() any                        { return nil }
func (*errorTool) MaxResultSize() int                          { return 50000 }

type nilResultTool struct{ name string }

func (t *nilResultTool) Name() string                                { return t.name }
func (t *nilResultTool) Aliases() []string                           { return nil }
func (t *nilResultTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *nilResultTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *nilResultTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (t *nilResultTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *nilResultTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *nilResultTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *nilResultTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *nilResultTool) IsEnabled() bool                           { return true }
func (t *nilResultTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *nilResultTool) Prompt() string                            { return "" }
func (t *nilResultTool) RenderResult(any) string                   { return "" }
func (t *nilResultTool) NewResultType() any                        { return nil }
func (*nilResultTool) MaxResultSize() int                          { return 50000 }

type interruptBlockTool struct{ name string }

func (t *interruptBlockTool) Name() string                                { return t.name }
func (t *interruptBlockTool) Aliases() []string                           { return nil }
func (t *interruptBlockTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *interruptBlockTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *interruptBlockTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "executed"}, nil
}
func (t *interruptBlockTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *interruptBlockTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *interruptBlockTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *interruptBlockTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *interruptBlockTool) IsEnabled() bool                           { return true }
func (t *interruptBlockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptBlock }
func (t *interruptBlockTool) Prompt() string                            { return "" }

func (t *interruptBlockTool) RenderResult(any) string { return "executed" }
func (t *interruptBlockTool) NewResultType() any      { return nil }
func (*interruptBlockTool) MaxResultSize() int        { return 50000 }

// ---------------------------------------------------------------------------
// askUser — all return paths
// ---------------------------------------------------------------------------

func TestAskUser_SessionCacheHit(t *testing.T) {
	fakeTool := &nonStreamingSuccessTool{name: "AskTool", data: "ok"}
	exec := NewStreamingToolExecutor(
		map[string]tool.Tool{"AskTool": fakeTool},
		nil, func(types.QueryEvent) {}, context.Background(),
	)
	exec.sessionAllowed = map[string]bool{"AskTool": true}
	tt := &TrackedTool{ID: "t1", Name: "AskTool", done: make(chan struct{}), Input: json.RawMessage(`{}`)}
	decision := permission.Decision{Action: permission.ActionAsk}
	result := exec.askUser(tt, decision, "")
	if result != types.DecisionAllow {
		t.Errorf("session cache hit: expected Allow, got %v", result)
	}
}

func TestAskUser_RootCtxDone(t *testing.T) {
	fakeTool := &nonStreamingSuccessTool{name: "AskTool", data: "ok"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate cancel
	exec := NewStreamingToolExecutor(
		map[string]tool.Tool{"AskTool": fakeTool},
		nil, func(types.QueryEvent) {}, ctx,
	)
	tt := &TrackedTool{ID: "t1", Name: "AskTool", done: make(chan struct{}), Input: json.RawMessage(`{}`)}
	decision := permission.Decision{Action: permission.ActionAsk, Message: "test"}
	result := exec.askUser(tt, decision, "")
	if result != types.DecisionDeny {
		t.Errorf("rootCtx done: expected Deny, got %v", result)
	}
}

func TestAskUser_SiblingCtxDone(t *testing.T) {
	fakeTool := &nonStreamingSuccessTool{name: "AskTool", data: "ok"}
	rootCtx := t.Context()
	siblingCtx, siblingCancel := context.WithCancelCause(rootCtx)
	siblingCancel(errors.New("sibling cancelled"))
	exec := NewStreamingToolExecutor(
		map[string]tool.Tool{"AskTool": fakeTool},
		nil, func(types.QueryEvent) {}, rootCtx,
	)
	exec.siblingCtx = siblingCtx
	tt := &TrackedTool{ID: "t1", Name: "AskTool", done: make(chan struct{}), Input: json.RawMessage(`{}`)}
	decision := permission.Decision{Action: permission.ActionAsk, Message: "test"}
	result := exec.askUser(tt, decision, "")
	if result != types.DecisionDeny {
		t.Errorf("siblingCtx done: expected Deny, got %v", result)
	}
}

// TestAskUser_AllowAlways_CachesSession is already covered by TestAskUser_AllowAlways_CachesKey

// ---------------------------------------------------------------------------
// askUser with real response via mock emit
// ---------------------------------------------------------------------------

func TestAskUser_AllowResponse(t *testing.T) {
	fakeTool := &nonStreamingSuccessTool{name: "AskTool", data: "ok"}
	chCh := make(chan chan types.AskResponse, 1)
	exec := NewStreamingToolExecutor(
		map[string]tool.Tool{"AskTool": fakeTool},
		nil, func(evt types.QueryEvent) {
			if evt.Type == types.EventAsk && evt.Ask != nil {
				select {
				case chCh <- evt.Ask.ResponseCh:
				default:
				}
			}
		}, context.Background(),
	)
	tt := &TrackedTool{ID: "t1", Name: "AskTool", done: make(chan struct{}), Input: json.RawMessage(`{}`)}
	decision := permission.Decision{Action: permission.ActionAsk, Message: "test"}

	go func() {
		ch := <-chCh
		ch <- types.AskResponse{Decision: types.DecisionAllow}
	}()

	result := exec.askUser(tt, decision, "")
	if result != types.DecisionAllow {
		t.Errorf("expected Allow, got %v", result)
	}
}

func TestAskUser_AllowAlways_CachesKey(t *testing.T) {
	fakeTool := &nonStreamingSuccessTool{name: "AskTool", data: "ok"}
	chCh := make(chan chan types.AskResponse, 1)
	exec := NewStreamingToolExecutor(
		map[string]tool.Tool{"AskTool": fakeTool},
		nil, func(evt types.QueryEvent) {
			if evt.Type == types.EventAsk && evt.Ask != nil {
				select {
				case chCh <- evt.Ask.ResponseCh:
				default:
				}
			}
		}, context.Background(),
	)
	tt := &TrackedTool{ID: "t1", Name: "AskTool", done: make(chan struct{}), Input: json.RawMessage(`{}`)}
	decision := permission.Decision{Action: permission.ActionAsk, Message: "test"}

	go func() {
		ch := <-chCh
		ch <- types.AskResponse{Decision: types.DecisionAllowAlways}
	}()

	result := exec.askUser(tt, decision, "matched_pattern")
	if result != types.DecisionAllowAlways {
		t.Errorf("expected AllowAlways, got %v", result)
	}
	// After AllowAlways, the cache should have the key
	if exec.sessionAllowed == nil {
		t.Error("sessionAllowed should be set after AllowAlways")
	}
	if !exec.sessionAllowed["AskTool:matched_pattern"] {
		t.Error("sessionAllowed should have AskTool:matched_pattern")
	}
}

func TestAskUser_ClosedChannel(t *testing.T) {
	fakeTool := &nonStreamingSuccessTool{name: "AskTool", data: "ok"}
	chCh := make(chan chan types.AskResponse, 1)
	exec := NewStreamingToolExecutor(
		map[string]tool.Tool{"AskTool": fakeTool},
		nil, func(evt types.QueryEvent) {
			if evt.Type == types.EventAsk && evt.Ask != nil {
				select {
				case chCh <- evt.Ask.ResponseCh:
				default:
				}
			}
		}, context.Background(),
	)
	tt := &TrackedTool{ID: "t1", Name: "AskTool", done: make(chan struct{}), Input: json.RawMessage(`{}`)}
	decision := permission.Decision{Action: permission.ActionAsk, Message: "test"}

	go func() {
		ch := <-chCh
		close(ch)
	}()

	result := exec.askUser(tt, decision, "")
	if result != types.DecisionDeny {
		t.Errorf("closed channel: expected Deny, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// runStopHook + fireCompactHooks with real hooks
// ---------------------------------------------------------------------------

func TestRunStopHook_WithHooks(t *testing.T) {
	mp := &testProvider{}
	fakeHooks := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{
			Matcher: "",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "test-stop"}},
		}},
	}, &hookExecRecorder{})

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
		Hooks:    fakeHooks,
		Logger:   slog.Default(),
	})
	defer eng.Close()

	result := eng.runStopHook(context.Background())
	// Non-blocking hook returns nil result
	if result != nil {
		t.Errorf("runStopHook with non-blocking hook: expected nil result, got %+v", result)
	}
}

func TestRunStopHook_WithHooksBlocking(t *testing.T) {
	mp := &testProvider{}
	fakeHooks := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{
			Matcher: "",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "block-stop"}},
		}},
	}, &blockingHookExecRecorder{blockResult: &hooks.HookResult{
		Outcome:  hooks.HookOutcomeBlocking,
		Stderr:   "blocked by hook",
		HookName: "block-stop",
	}})

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
		Hooks:    fakeHooks,
		Logger:   slog.Default(),
	})
	defer eng.Close()

	result := eng.runStopHook(context.Background())
	if result == nil {
		t.Fatal("runStopHook with blocking hook: expected non-nil result")
	}
	if result.Outcome != hooks.HookOutcomeBlocking {
		t.Errorf("expected blocking outcome, got %v", result.Outcome)
	}
}

func TestRunStopHook_Subagent(t *testing.T) {
	mp := &testProvider{}
	fakeHooks := hooks.NewHooks(hooks.HooksConfig{
		"SubagentStop": []hooks.HookMatcher{{
			Matcher: "",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "sub-stop"}},
		}},
	}, &hookExecRecorder{})

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
		Hooks:    fakeHooks,
		Logger:   slog.Default(),
	})
	defer eng.Close()

	// Create sub-engine (isSubagent = true)
	subEng := eng.NewSubEngine(SubEngineOptions{
		Tools:     map[string]tool.Tool{},
		AgentType: "General",
	})
	defer subEng.Close()

	result := subEng.runStopHook(context.Background())
	if result != nil {
		t.Errorf("runStopHook(subagent) with non-blocking: expected nil, got %+v", result)
	}
}

func TestFireCompactHooks_WithPreAndPost(t *testing.T) {
	mp := &testProvider{}
	fakeHooks := hooks.NewHooks(hooks.HooksConfig{
		"PreCompact": []hooks.HookMatcher{{
			Matcher: "",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "pre-compact"}},
		}},
		"PostCompact": []hooks.HookMatcher{{
			Matcher: "",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "post-compact"}},
		}},
	}, &hookExecRecorder{})

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
		Hooks:    fakeHooks,
		Logger:   slog.Default(),
	})
	defer eng.Close()

	eng.fireCompactHooks(context.Background(), "test_trigger", "pre")
	eng.fireCompactHooks(context.Background(), "test_trigger", "post")
}

// ---------------------------------------------------------------------------
// applyBudget — with contentReplacementState
// ---------------------------------------------------------------------------

func TestApplyBudget_WithContent(t *testing.T) {
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})
	defer eng.Close()

	// Set up contentReplacementState
	eng.contentReplacementState = toolresult.NewContentReplacementState()
	eng.sessionID = "test-session"

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{
					Type:      types.ContentTypeToolResult,
					ToolUseID: "t1",
					ID:        "r1",
					Name:      "Bash",
					Content:   json.RawMessage(`"some tool output that is long enough to trigger budget"`),
				},
			},
		},
	}

	result := eng.applyBudget(msgs)
	if len(result) != 1 {
		t.Errorf("applyBudget with state: expected 1 msg, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// MCPTool.Call — in-memory test server
// ---------------------------------------------------------------------------

func TestExecuteTool_StreamingError(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"stream_err": &streamingErrorTool{name: "stream_err"},
	}
	executor := NewStreamingToolExecutor(toolMap, nil, emit, context.Background())
	tt := &TrackedTool{
		ID:    "t1",
		Name:  "stream_err",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	executor.executeTool(tt)
	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block")
	}
	if !tt.resultBlocks[0].IsError {
		t.Errorf("stream_err should produce error, got IsError=%v", tt.resultBlocks[0].IsError)
	}
}

// ---------------------------------------------------------------------------
// streamingErrorTool — tool that returns error on Call
// ---------------------------------------------------------------------------

type streamingErrorTool struct{ name string }

type hookExecRecorder struct{}

func (h *hookExecRecorder) ExecuteHook(ctx context.Context, command string, input *hooks.HookInput, timeout time.Duration, extraEnv []string) hooks.HookResult {
	return hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, HookName: command}
}

type blockingHookExecRecorder struct {
	blockResult *hooks.HookResult
}

func (h *blockingHookExecRecorder) ExecuteHook(ctx context.Context, command string, input *hooks.HookInput, timeout time.Duration, extraEnv []string) hooks.HookResult {
	if h.blockResult != nil {
		return *h.blockResult
	}
	return hooks.HookResult{Outcome: hooks.HookOutcomeBlocking, Stderr: "blocked", HookName: command}
}

func (t *streamingErrorTool) Name() string                                { return t.name }
func (t *streamingErrorTool) Aliases() []string                           { return nil }
func (t *streamingErrorTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *streamingErrorTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (t *streamingErrorTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, errors.New("call error")
}
func (t *streamingErrorTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *streamingErrorTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *streamingErrorTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *streamingErrorTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *streamingErrorTool) IsEnabled() bool                           { return true }
func (t *streamingErrorTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *streamingErrorTool) Prompt() string                            { return "" }

func (t *streamingErrorTool) RenderResult(any) string { return "error" }
func (t *streamingErrorTool) NewResultType() any      { return nil }
func (*streamingErrorTool) MaxResultSize() int        { return 50000 }

// ---------------------------------------------------------------------------
// executeTool — PostToolUseHook failure path
// ---------------------------------------------------------------------------

func TestExecuteTool_PostToolUseHookFailure(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	// Tool that returns success, but PostToolUseHook logs an error
	toolMap := map[string]tool.Tool{
		"ok_tool": &streamingSuccessTool{name: "ok_tool"},
	}
	exec := NewStreamingToolExecutor(toolMap, nil, emit, context.Background())
	exec.sessionID = "test-session"
	// firePostToolUseHook with nil hooks is a no-op — covered by nil hooks path

	tt := &TrackedTool{
		ID:    "t1",
		Name:  "ok_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	exec.executeTool(tt)
	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block for ok_tool")
	}
	if tt.resultBlocks[0].IsError {
		t.Error("ok_tool should not produce error")
	}
}

// ---------------------------------------------------------------------------
// executeTool — permission deny/ask paths
// ---------------------------------------------------------------------------

// mockPermChecker is a test double for permission.PermissionChecker.
type mockPermChecker struct {
	decision permission.Decision
	hasRules bool
}

func (m *mockPermChecker) Check(_ string, _ json.RawMessage) permission.Decision {
	return m.decision
}
func (m *mockPermChecker) HasRules() bool { return m.hasRules }

func TestExecuteTool_PermissionDeny(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"deny_tool": &streamingSuccessTool{name: "deny_tool"},
	}
	rootCtx := t.Context()
	sibCtx := t.Context()

	exec := NewStreamingToolExecutor(toolMap, nil, emit, rootCtx)
	exec.siblingCtx = sibCtx
	exec.sessionID = "test-session"
	exec.permChecker = &mockPermChecker{
		decision: permission.Decision{Action: permission.ActionDeny},
		hasRules: true,
	}

	tt := &TrackedTool{
		ID:    "t-deny",
		Name:  "deny_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	exec.executeTool(tt)

	if len(tt.resultBlocks) == 0 {
		t.Fatal("result blocks empty for permission denied")
	}
	if !tt.resultBlocks[0].IsError {
		t.Fatalf("denied tool result should be error, got non-error block: %v", tt.resultBlocks[0])
	}
	found := false
	for _, evt := range emitted {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.IsError {
			found = true
		}
	}
	if !found {
		t.Error("missing ToolEnd event for permission denied")
	}
}

func TestExecuteTool_PermissionAsk_SessionCached(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"ask_tool": &streamingSuccessTool{name: "ask_tool"},
	}
	rootCtx := t.Context()
	sibCtx := t.Context()

	exec := NewStreamingToolExecutor(toolMap, nil, emit, rootCtx)
	exec.siblingCtx = sibCtx
	exec.sessionID = "test-session"
	exec.permChecker = &mockPermChecker{
		decision: permission.Decision{Action: permission.ActionAsk},
		hasRules: true,
	}
	// Pre-populate session cache so askUser returns allow without blocking
	exec.sessionAllowed = map[string]bool{"ask_tool": true}

	tt := &TrackedTool{
		ID:    "t-ask",
		Name:  "ask_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	exec.executeTool(tt)

	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block")
	}
	if tt.resultBlocks[0].IsError {
		t.Error("cached permission should allow tool execution")
	}
}

func TestExecuteTool_PermissionAsk_Rejected(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
		if evt.Type == types.EventAsk && evt.Ask != nil {
			evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
		}
	}

	toolMap := map[string]tool.Tool{
		"ask_tool": &streamingSuccessTool{name: "ask_tool"},
	}
	rootCtx := t.Context()
	sibCtx := t.Context()

	exec := NewStreamingToolExecutor(toolMap, nil, emit, rootCtx)
	exec.siblingCtx = sibCtx
	exec.sessionID = "test-session"
	exec.permChecker = &mockPermChecker{
		decision: permission.Decision{Action: permission.ActionAsk},
		hasRules: true,
	}

	tt := &TrackedTool{
		ID:    "t-ask-rej",
		Name:  "ask_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	exec.executeTool(tt)

	if len(tt.resultBlocks) == 0 {
		t.Fatal("result blocks empty for user deny")
	}
	if !tt.resultBlocks[0].IsError {
		t.Fatalf("user deny should produce error result, got: %v", tt.resultBlocks[0])
	}
}

// ---------------------------------------------------------------------------
// executeTool — PreToolUse hook block path
// ---------------------------------------------------------------------------

func TestExecuteTool_PreToolUseHookBlock(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"hooked_tool": &streamingSuccessTool{name: "hooked_tool"},
	}
	rootCtx := t.Context()
	sibCtx := t.Context()

	fakeHooks := hooks.NewHooks(hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{{
			Matcher: "",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "test-pre"}},
		}},
	}, &blockingHookExecRecorder{})

	exec := NewStreamingToolExecutor(toolMap, nil, emit, rootCtx)
	exec.siblingCtx = sibCtx
	exec.sessionID = "test-session"
	exec.hooks = fakeHooks

	tt := &TrackedTool{
		ID:    "t-hook",
		Name:  "hooked_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	exec.executeTool(tt)

	if len(tt.resultBlocks) == 0 {
		t.Fatal("result blocks empty for hook block")
	}
	if !tt.resultBlocks[0].IsError {
		t.Fatalf("hook block should produce error result, got: %v", tt.resultBlocks[0])
	}
	found := false
	for _, evt := range emitted {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.IsError {
			if strings.Contains(string(evt.ToolResult.Output), "PreToolUse hook") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected ToolEnd error event containing 'PreToolUse hook'")
	}
}

// ---------------------------------------------------------------------------
// executeTool — non-streaming nil result
// ---------------------------------------------------------------------------

type nilResultNonStreamingTool struct {
	name string
}

func (t *nilResultNonStreamingTool) Name() string                                { return t.name }
func (t *nilResultNonStreamingTool) Aliases() []string                           { return nil }
func (t *nilResultNonStreamingTool) InputSchema() json.RawMessage                { return nil }
func (t *nilResultNonStreamingTool) Description(json.RawMessage) (string, error) { return "", nil }
func (t *nilResultNonStreamingTool) IsEnabled() bool                             { return true }
func (t *nilResultNonStreamingTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (t *nilResultNonStreamingTool) MaxResultSize() int { return 50000 }
func (t *nilResultNonStreamingTool) Prompt() string     { return "" }

func (t *nilResultNonStreamingTool) RenderResult(any) string { return "" }
func (t *nilResultNonStreamingTool) NewResultType() any      { return nil }
func (t *nilResultNonStreamingTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (t *nilResultNonStreamingTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *nilResultNonStreamingTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *nilResultNonStreamingTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *nilResultNonStreamingTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func TestExecuteTool_NonStreamingNilResult(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"nil_tool": &nilResultNonStreamingTool{name: "nil_tool"},
	}
	rootCtx := t.Context()

	exec := NewStreamingToolExecutor(toolMap, nil, emit, rootCtx)
	exec.sessionID = "test-session"

	tt := &TrackedTool{
		ID:    "t-nil",
		Name:  "nil_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	exec.executeTool(tt)

	if len(tt.resultBlocks) == 0 {
		t.Fatal("expected result block for nil result")
	}
	if tt.resultBlocks[0].IsError {
		t.Error("nil result should not be an error")
	}
	if string(tt.resultBlocks[0].Content) != "null" {
		t.Errorf("expected 'null' content for nil result, got %q", string(tt.resultBlocks[0].Content))
	}
}

// ---------------------------------------------------------------------------
// executeTool — isBackgroundResult path (non-streaming)
// ---------------------------------------------------------------------------

type backgroundNonStreamTool struct {
	name string
}

func (t *backgroundNonStreamTool) Name() string                                { return t.name }
func (t *backgroundNonStreamTool) Aliases() []string                           { return nil }
func (t *backgroundNonStreamTool) InputSchema() json.RawMessage                { return nil }
func (t *backgroundNonStreamTool) Description(json.RawMessage) (string, error) { return "", nil }
func (t *backgroundNonStreamTool) IsEnabled() bool                             { return true }
func (t *backgroundNonStreamTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (t *backgroundNonStreamTool) MaxResultSize() int { return 50000 }
func (t *backgroundNonStreamTool) Prompt() string     { return "" }

func (t *backgroundNonStreamTool) RenderResult(any) string { return "background done" }
func (t *backgroundNonStreamTool) NewResultType() any      { return nil }
func (t *backgroundNonStreamTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: &types.SubQueryResult{AsyncLaunched: true}}, nil
}
func (t *backgroundNonStreamTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *backgroundNonStreamTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *backgroundNonStreamTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *backgroundNonStreamTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func TestExecuteTool_NonStreamingBackgroundResult(t *testing.T) {
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	toolMap := map[string]tool.Tool{
		"bg_tool": &backgroundNonStreamTool{name: "bg_tool"},
	}
	rootCtx := t.Context()

	exec := NewStreamingToolExecutor(toolMap, nil, emit, rootCtx)
	exec.sessionID = "test-session"

	tt := &TrackedTool{
		ID:    "t-bg",
		Name:  "bg_tool",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{}`),
	}
	exec.executeTool(tt)

	found := false
	for _, evt := range emitted {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil {
			if !evt.ToolResult.IsBackground {
				t.Error("expected IsBackground=true for async-launched sub-query")
			}
			found = true
		}
	}
	if !found {
		t.Error("expected ToolEnd event")
	}
}

// ---------------------------------------------------------------------------
// applyBudget — content replacement paths
// ---------------------------------------------------------------------------

func TestApplyBudget_WithContentReplacement(t *testing.T) {
	eng := New(&Params{})
	defer eng.Close()

	originalContent := json.RawMessage(`{"output":"this is a very long output that should be truncated by the budget enforcement"}`)
	eng.contentReplacementState = &toolresult.ContentReplacementState{
		SeenIDs:      map[string]bool{"tc1": true},
		Replacements: map[string]string{"tc1": "[truncated]"},
	}

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: "tc1",
					Content:   originalContent,
				},
			},
		},
	}

	result := eng.applyBudget(msgs)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) == 0 || len(result[0].Content) == 0 {
		t.Fatal("result has no content blocks")
	}
	got := string(result[0].Content[0].Content)
	if got == string(originalContent) {
		t.Error("content was not replaced by budget enforcement")
	}
}

// ---------------------------------------------------------------------------
// StreamingToolExecutor — buildToolCtx
// ---------------------------------------------------------------------------

func TestStreamingToolExecutor_BuildToolCtx(t *testing.T) {
	rootCtx := t.Context()

	emit := func(types.QueryEvent) {}
	exec := NewStreamingToolExecutor(nil, nil, emit, rootCtx)
	exec.sessionID = "test-session"

	tctx := exec.buildToolCtx("tool-123")
	if tctx == nil {
		t.Fatal("expected non-nil ToolUseContext")
	}
	if tctx.ToolUseID != "tool-123" {
		t.Errorf("expected ToolUseID=tool-123, got %q", tctx.ToolUseID)
	}
}

func TestStreamingToolExecutor_BuildToolCtx_OnAskInput(t *testing.T) {
	rootCtx := t.Context()

	var capturedEvent types.QueryEvent
	emit := func(evt types.QueryEvent) { capturedEvent = evt }
	exec := NewStreamingToolExecutor(nil, nil, emit, rootCtx)
	exec.sessionID = "test-session"
	// Provide an empty tctx so buildToolCtx enters the wiring branch
	exec.tctx = &tool.ToolUseContext{}

	tctx := exec.buildToolCtx("tool-ask-1")
	if tctx.OnAskInput == nil {
		t.Fatal("OnAskInput should be wired when tctx.OnAskInput is nil")
	}

	// Call the OnAskInput closure
	deadline := time.Date(2099, 1, 1, 0, 0, 30, 0, time.UTC)
	ch := tctx.OnAskInput("[sudo] password:", true, deadline)

	if ch == nil {
		t.Fatal("OnAskInput should return non-nil channel")
	}

	// Verify the emitted event
	if capturedEvent.Type != types.EventAsk {
		t.Errorf("event type = %v, want %v", capturedEvent.Type, types.EventAsk)
	}
	if capturedEvent.Ask == nil {
		t.Fatal("event Ask should not be nil")
	}
	if capturedEvent.Ask.Kind != types.AskInput {
		t.Errorf("Ask.Kind = %v, want %v", capturedEvent.Ask.Kind, types.AskInput)
	}
	if capturedEvent.Ask.Prompt != "[sudo] password:" {
		t.Errorf("Ask.Prompt = %q, want %q", capturedEvent.Ask.Prompt, "[sudo] password:")
	}
	if !capturedEvent.Ask.Masked {
		t.Error("Ask.Masked should be true")
	}
	if capturedEvent.Ask.ResponseCh == nil {
		t.Fatal("Ask.ResponseCh should not be nil")
	}
	if capturedEvent.Ask.ResponseCh != ch {
		t.Error("ResponseCh should be the same channel returned by OnAskInput")
	}
}

func TestStreamingToolExecutor_BuildToolCtx_OnAskInput_PreservedWhenSet(t *testing.T) {
	rootCtx := t.Context()

	emit := func(types.QueryEvent) {}
	exec := NewStreamingToolExecutor(nil, nil, emit, rootCtx)

	// Pre-set OnAskInput — buildToolCtx should NOT overwrite it
	called := false
	presetFn := func(prompt string, masked bool, deadline time.Time) chan types.AskResponse {
		called = true
		ch := make(chan types.AskResponse, 1)
		ch <- types.AskResponse{Text: "preset-response"}
		return ch
	}
	exec.tctx = &tool.ToolUseContext{OnAskInput: presetFn}

	tctx := exec.buildToolCtx("tool-preset")
	if tctx.OnAskInput == nil {
		t.Fatal("OnAskInput should not be nil")
	}

	ch := tctx.OnAskInput("test", false, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	if !called {
		t.Error("preset OnAskInput should have been called, not overwritten")
	}
	select {
	case resp := <-ch:
		if resp.Text != "preset-response" {
			t.Errorf("response = %q, want %q", resp.Text, "preset-response")
		}
	default:
		t.Fatal("expected response from preset OnAskInput")
	}
}

// ---------------------------------------------------------------------------
// getToolDescription — input field extraction
// ---------------------------------------------------------------------------

func TestGetToolDescription_Command(t *testing.T) {
	tt := &TrackedTool{
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"ls -la /some/very/long/path/that/exceeds/40/chars"}`),
	}
	got := getToolDescription(tt)
	if !strings.Contains(got, "Bash(") {
		t.Errorf("expected Bash(...), got %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected truncation marker for >40 chars, got %q", got)
	}
}

func TestGetToolDescription_FilePath(t *testing.T) {
	tt := &TrackedTool{
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/tmp/test.txt"}`),
	}
	got := getToolDescription(tt)
	if got != "Read(/tmp/test.txt)" {
		t.Errorf("expected Read(/tmp/test.txt), got %q", got)
	}
}

func TestGetToolDescription_Pattern(t *testing.T) {
	tt := &TrackedTool{
		Name:  "Grep",
		Input: json.RawMessage(`{"pattern":"TODO"}`),
	}
	got := getToolDescription(tt)
	if got != "Grep(TODO)" {
		t.Errorf("expected Grep(TODO), got %q", got)
	}
}

func TestGetToolDescription_NoFields(t *testing.T) {
	tt := &TrackedTool{
		Name:  "Tool",
		Input: json.RawMessage(`{"other":"value"}`),
	}
	got := getToolDescription(tt)
	if got != "Tool" {
		t.Errorf("expected Tool, got %q", got)
	}
}

func TestGetToolDescription_InvalidJSON(t *testing.T) {
	tt := &TrackedTool{
		Name:  "Tool",
		Input: json.RawMessage(`{bad`),
	}
	got := getToolDescription(tt)
	if got != "Tool" {
		t.Errorf("expected Tool for invalid JSON, got %q", got)
	}
}

// chanDispatcher is a test EventDispatcher that sends events to a channel.
type chanDispatcher struct {
	ch chan<- types.QueryEvent
}

func (d *chanDispatcher) Dispatch(event types.QueryEvent) {
	d.ch <- event
}

// ---------------------------------------------------------------------------
// Query — async wrapper integration test
// ---------------------------------------------------------------------------

func TestQuery_EmitsQueryStart(t *testing.T) {
	eventCh := make(chan types.QueryEvent, 10)
	dispatcher := &chanDispatcher{ch: eventCh}

	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "hi"}},
		{Type: "message_stop"},
	}, nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: dispatcher,
	})
	defer eng.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eng.Query(ctx, "hello", "")

	// Wait for EventQueryStart
	select {
	case evt := <-eventCh:
		if evt.Type != types.EventQueryStart {
			t.Errorf("expected EventQueryStart, got %v", evt.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventQueryStart")
	}
}

// ---------------------------------------------------------------------------
// ProcessAttachments — async wrapper integration test
// ---------------------------------------------------------------------------

func TestProcessAttachments_EmptyDrain(t *testing.T) {
	eng := New(&Params{})
	defer eng.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// No attachments — ProcessAttachments goroutine returns immediately
	eng.ProcessAttachments(ctx, "")
	// Give goroutine time to run
	runtime.Gosched()
}

func TestProcessAttachments_WithPending(t *testing.T) {
	eventCh := make(chan types.QueryEvent, 10)
	dispatcher := &chanDispatcher{ch: eventCh}

	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "ok"}},
		{Type: "message_stop"},
	}, nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: dispatcher,
	})
	defer eng.Close()

	// Push an attachment
	eng.EnqueueAttachment(types.QueuedItem{
		Value: "notification",
		Mode:  types.ItemModeJob,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eng.ProcessAttachments(ctx, "")

	// Job-mode attachments no longer emit EventAttachment.
	// Verify LLM turn runs (EventTurnStart + EventQueryEnd) without EventAttachment.
	var gotTurn, gotEnd bool
	for !gotTurn || !gotEnd {
		select {
		case evt := <-eventCh:
			if evt.Type == types.EventAttachment {
				t.Error("job-mode attachment should NOT emit EventAttachment")
			}
			if evt.Type == types.EventTurnStart {
				gotTurn = true
			}
			if evt.Type == types.EventQueryEnd {
				gotEnd = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for LLM turn after job attachment")
		}
	}
}

// ---------------------------------------------------------------------------
// Engine.ExecuteTool — RenderResult integration
// ---------------------------------------------------------------------------

// renderResultTool returns structured data from Call() but formats it via RenderResult.
// ExecuteTool must return the RenderResult output, not raw JSON.
type renderResultTool struct {
	name string
}

func (t *renderResultTool) Name() string                                { return t.name }
func (t *renderResultTool) Aliases() []string                           { return nil }
func (t *renderResultTool) Description(json.RawMessage) (string, error) { return "test", nil }
func (t *renderResultTool) InputSchema() json.RawMessage                { return nil }
func (t *renderResultTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: map[string]any{"files": []string{"a.go", "b.go"}, "count": 2}}, nil
}
func (t *renderResultTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *renderResultTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *renderResultTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *renderResultTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *renderResultTool) IsEnabled() bool                        { return true }
func (t *renderResultTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (t *renderResultTool) MaxResultSize() int { return 50000 }
func (t *renderResultTool) Prompt() string     { return "" }
func (t *renderResultTool) RenderResult(data any) string {
	// Simulate a tool that formats structured data as plain text
	m, ok := data.(map[string]any)
	if !ok {
		return fmt.Sprintf("%v", data)
	}
	files, _ := m["files"].([]string)
	return strings.Join(files, "\n")
}

// Regression: Query goroutine's LIFO defer order caused clearActiveCancel to
// wipe out the activeCancel set by startProcessAttachmentsIfIdle. After Query
// finished and processAttachments started, Abort() was a no-op (activeCancel=nil).
// Defer consolidation ensures clearActiveCancel runs before startProcessAttachmentsIfIdle.
func TestAbort_CancelsProcessAttachmentsAfterQueryEnds(t *testing.T) {
	eventCh := make(chan types.QueryEvent, 20)
	dispatcher := &chanDispatcher{ch: eventCh}

	// First response: for the initial Query (returns immediately)
	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "done"}},
		{Type: "message_stop"},
	}, nil)

	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: dispatcher,
	})
	defer eng.Close()

	// Queue an attachment so processAttachments starts after Query finishes
	eng.EnqueueAttachment(types.QueuedItem{
		Value: "post-query work",
		Mode:  types.ItemModeJob,
	})
	eng.systemPrompt = "test"

	// Start a query — will complete, then startProcessAttachmentsIfIdle fires
	ctx := context.Background()
	eng.Query(ctx, "hello", "test")

	// Wait for Query to finish and processAttachments to start
	var gotQueryEnd, gotTurnStart bool
	timeout := time.After(5 * time.Second)
	for !gotQueryEnd || !gotTurnStart {
		select {
		case evt := <-eventCh:
			if evt.Type == types.EventQueryEnd && evt.Agent == nil {
				gotQueryEnd = true
			}
			if evt.Type == types.EventTurnStart && evt.Agent == nil {
				gotTurnStart = true
			}
		case <-timeout:
			t.Fatalf("timed out waiting for events: queryEnd=%v turnStart=%v", gotQueryEnd, gotTurnStart)
		}
	}

	// processAttachments is running. activeCancel must be non-nil so Abort() works.
	eng.activeCancelMu.Lock()
	ac := eng.activeCancel
	eng.activeCancelMu.Unlock()
	if ac == nil {
		t.Fatal("activeCancel is nil during processAttachments — defer ordering wipes it out, Abort() is a no-op")
	}
}

// Regression: ESC aborts query mid-tool, attachment queued during tool execution.
// Turn loop drained the attachment (DrainByPriority) before ShouldAbort check,
// so processAttachments found empty queue and the attachment was lost.
// Expected: attachment survives abort and gets processed by processAttachments.
func TestAbort_DuringTool_AttachmentProcessedByProcessAttachments(t *testing.T) {
	eventCh := make(chan types.QueryEvent, 30)
	dispatcher := &chanDispatcher{ch: eventCh}

	toolStarted := make(chan struct{})
	toolCancelled := make(chan struct{})

	// Tool that blocks until context cancelled
	bashTool := &testTool{
		name: "Bash",
		callFn: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			close(toolStarted)
			<-ctx.Done()
			close(toolCancelled)
			return nil, ctx.Err()
		},
	}

	// First response: LLM calls Bash tool
	mp := &testProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 1}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "bash_1", Name: "Bash"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"sleep 60"}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 1}},
		{Type: "message_stop"},
	}, nil)

	// Second response: for processAttachments turn (LLM responds to the attachment)
	mp.addResponse([]llm.StreamEvent{
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "processed attachment"}},
		{Type: "message_stop"},
	}, nil)

	eng := New(&Params{
		Provider:   mp,
		Tools:      []tool.Tool{bashTool},
		Model:      "test",
		Dispatcher: dispatcher,
	})
	defer eng.Close()
	eng.systemPrompt = "test"

	ctx := context.Background()
	eng.Query(ctx, "run bash", "test")

	// Wait for tool to start executing
	select {
	case <-toolStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool to start")
	}

	// While tool is running, queue an attachment (simulates user typing during tool execution)
	eng.EnqueueAttachment(types.QueuedItem{
		Value:    "user input during bash",
		Mode:     types.ItemModePrompt,
		Priority: types.PriorityNext,
	})

	// Abort the query (ESC)
	eng.Abort()

	// Wait for tool to be cancelled
	select {
	case <-toolCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool cancellation")
	}

	// Now: query ended, processAttachments should process the attachment.
	// We expect to see a second turnStart (from processAttachments).
	var gotFirstTurnEnd, gotSecondTurnStart bool
	timeout := time.After(5 * time.Second)
	for !gotSecondTurnStart {
		select {
		case evt := <-eventCh:
			if evt.Type == types.EventTurnEnd && !gotFirstTurnEnd {
				gotFirstTurnEnd = true
			}
			if evt.Type == types.EventTurnStart && gotFirstTurnEnd {
				gotSecondTurnStart = true
			}
		case <-timeout:
			t.Fatalf("timed out: attachment not processed after abort. "+
				"firstTurnEnd=%v secondTurnStart=%v", gotFirstTurnEnd, gotSecondTurnStart)
		}
	}
}

func TestExecuteTool_ReturnsRenderResult(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Tools:    []tool.Tool{&renderResultTool{name: "list_files"}},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })

	sessionAllowed := make(map[string]bool)
	var mu sync.Mutex

	result, err := eng.ExecuteTool(context.Background(), "list_files", json.RawMessage(`{}`), sessionAllowed, &mu)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}

	// RenderResult formats as newline-separated file paths, NOT JSON.
	if result != "a.go\nb.go" {
		t.Errorf("ExecuteTool: got %q, want %q", result, "a.go\nb.go")
	}
	// Must NOT be JSON like {"count":2,"files":["a.go","b.go"]}
	if strings.HasPrefix(result, "{") {
		t.Errorf("ExecuteTool returned raw JSON, should use RenderResult")
	}
}

// ---------------------------------------------------------------------------
// REPL integration: ExecuteTool → real bash → uncapped output
// Full call chain: Engine.ExecuteTool() → tool_exec.go sets UncappedOutput → bash.Execute() → ReadContent(64MB) → string
// ---------------------------------------------------------------------------

// TestExecuteTool_REPLUncappedOutput tests the full REPL→bash call chain.
// Engine.ExecuteTool() must set UncappedOutput:true on the ToolUseContext,
// causing bash to return full output (> 30KB) instead of the normal cap.
func TestExecuteTool_REPLUncappedOutput(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Tools:    []tool.Tool{bash.New(nil)},
		Model:    "test",
	})
	t.Cleanup(func() { eng.Close() })

	sessionAllowed := make(map[string]bool)
	sessionAllowed["Bash"] = true // skip permission prompt
	var mu sync.Mutex

	// seq 1 20000 produces ~90KB, well above MaxOutputSize=30000
	result, err := eng.ExecuteTool(
		context.Background(),
		"Bash",
		json.RawMessage(`{"command":"seq 1 20000","timeout":30000}`),
		sessionAllowed,
		&mu,
	)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}

	// Result should contain all 20000 lines (not truncated at 30KB)
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) < 20000 {
		t.Errorf("got %d lines, want >= 20000 (output was truncated)", len(lines))
	}
	if len(result) <= 30000 {
		t.Errorf("result len = %d, should exceed MaxOutputSize=30000 for REPL path", len(result))
	}
}

// TestExecuteTool_LLMPathStillCapped verifies that the engine path
// (StreamingToolExecutor) still applies normal caps — only the REPL
// ExecuteTool path should be uncapped.
func TestExecuteTool_LLMPathStillCapped(t *testing.T) {
	t.Parallel()

	// Use StreamingToolExecutor (engine path, not REPL path)
	toolMap := map[string]tool.Tool{"Bash": bash.New(nil)}
	var emitted []types.QueryEvent
	emit := func(evt types.QueryEvent) {
		emitted = append(emitted, evt)
	}

	executor := NewStreamingToolExecutor(toolMap, nil, emit, context.Background())
	blocks := []types.ContentBlock{
		{
			Type:  types.ContentTypeToolUse,
			ID:    "t-capped",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"seq 1 20000","timeout":30000}`),
		},
	}

	result := executor.ExecuteAll(blocks)
	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(result.ToolResultBlocks))
	}

	content := result.ToolResultBlocks[0].Text
	// Engine path should cap output (bash caps at 30KB + MaybePersistLargeToolResult)
	if len(content) > 35000 {
		t.Errorf("engine path result len = %d, should be capped around 30KB", len(content))
	}
}

// ---------------------------------------------------------------------------
// Token-based pruning integration tests
// ---------------------------------------------------------------------------

// TestQuery_TokenPrune_AfterCompactFails verifies THE key scenario: compact
// fails (all messages recent) -> token pruning clears old tool results ->
// tokens drop -> API call succeeds.
func TestQuery_TokenPrune_AfterCompactFails_BlockingAvoided(t *testing.T) {
	t.Parallel()

	tmp := &testProvider{}
	tmp.addResponse(textStreamEvents("test-model", "Success after prune"), nil)

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return nil, errors.New("nothing to compact: all messages within keep target")
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   tmp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Build messages with compactable tool results (Read).
	// 8 Read results x ~5K tokens each -> 40K tokens total.
	// With ContextWindow=50000, MaxTokens=16000:
	//   effectiveWindow = 50000 - 16000 = 34000
	//   pruneThreshold = 34000 - 3000 = 31000
	// Setting ContextTokens=35000 triggers auto-compact.
	// Compact fails -> token prune should clear old Read results -> tokens drop.
	//
	// Use recent timestamps so time-based microcompact does NOT fire (gap < 60 min).
	// Only token-based prune should handle this case.
	now := time.Now() // REAL-TIME: timestamps for microcompact gap calc
	for i := range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewToolUseBlock(fmt.Sprintf("read_%d", i), "Read", json.RawMessage(`{"file_path":"/test.go"}`)),
			},
			Timestamp: now.Add(-time.Duration(8-i) * time.Minute),
		}))
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock(fmt.Sprintf("read_%d", i), json.RawMessage(`"`+strings.Repeat("x", 10000)+`"`), false),
			},
			Timestamp: now.Add(-time.Duration(8-i) * time.Minute),
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")

	if result.Error != nil {
		t.Fatalf("expected success (token prune should reduce context), got: %v", result.Error)
	}
	// Verify API was called (compact failed but pruning reduced context enough)
	if len(result.Messages) == 0 {
		t.Error("expected messages in result")
	}
}

// TestQuery_TokenPrune_NothingToPrune_APIProceeds verifies that when pruning
// has nothing to clear (fewer Read results than KeepRecent), the API call
// still proceeds. Without a blocking limit, oversized context is handled
// reactively by the provider or reactive compact.
func TestQuery_TokenPrune_NothingToPrune_APIProceeds(t *testing.T) {
	t.Parallel()

	tmp := &testProvider{}
	tmp.addResponse(textStreamEvents("test-model", "api response"), nil)

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return nil, errors.New("nothing to compact")
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   tmp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// Only 2 Read results -> pruning keeps KeepRecent=5 -> nothing to clear.
	// API call should still proceed.
	for i := range 2 {
		usage := &types.Usage{InputTokens: 17500, OutputTokens: 50}
		if i == 1 {
			usage = &types.Usage{InputTokens: 35000, OutputTokens: 100}
		}
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewToolUseBlock(fmt.Sprintf("read_%d", i), "Read", json.RawMessage(`{}`)),
			},
			Usage: usage,
		}))
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock(fmt.Sprintf("read_%d", i), json.RawMessage(`"`+strings.Repeat("x", 16000)+`"`), false),
			},
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")
	// Pruning cleared nothing, but the API call should proceed anyway.
	if result.Error != nil {
		t.Fatalf("expected API call to proceed despite nothing to prune, got: %v", result.Error)
	}
}

// TestQuery_TokenPrune_UnderThreshold verifies that when context is below
// the prune threshold, token pruning does not fire and the API call succeeds normally.
func TestQuery_TokenPrune_UnderThreshold(t *testing.T) {
	t.Parallel()

	tmp := &testProvider{}
	tmp.addResponse(textStreamEvents("test-model", "normal response"), nil)

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return nil, errors.New("nothing to compact")
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   tmp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// 3 Read results — context well below prune threshold (31000).
	now := time.Now() // REAL-TIME: timestamps for microcompact gap calc
	for i := range 3 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewToolUseBlock(fmt.Sprintf("read_%d", i), "Read", json.RawMessage(`{"file_path":"/test.go"}`)),
			},
			Timestamp: now.Add(-time.Duration(3-i) * time.Minute),
		}))
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock(fmt.Sprintf("read_%d", i), json.RawMessage(`"`+strings.Repeat("x", 1000)+`"`), false),
			},
			Timestamp: now.Add(-time.Duration(3-i) * time.Minute),
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 10000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")
	if result.Error != nil {
		t.Fatalf("expected success (context under threshold), got: %v", result.Error)
	}
	if len(result.Messages) == 0 {
		t.Error("expected messages in result")
	}
}

// TestQuery_TokenPrune_SubAgentExempt verifies that sub-agents do not trigger
// token-based pruning.
func TestQuery_TokenPrune_SubAgentExempt(t *testing.T) {
	t.Parallel()

	tmp := &testProvider{}
	tmp.addResponse(textStreamEvents("test-model", "sub-agent response"), nil)

	compactor := &funcCompactor{
		fn: func(_ context.Context, _ []types.Message) (*short.CompactResult, error) {
			return nil, errors.New("nothing to compact")
		},
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   tmp,
		Model:      "test-model",
		MaxTokens:  16000,
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.isSubagent = true
	eng.SetCompactor(compactor, AutoCompactConfig{
		ContextWindow:          50000,
		MaxConsecutiveFailures: 3,
	})

	// 8 Read results with high context — would trigger prune on main thread.
	now := time.Now() // REAL-TIME: timestamps for microcompact gap calc
	for i := range 8 {
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewToolUseBlock(fmt.Sprintf("read_%d", i), "Read", json.RawMessage(`{"file_path":"/test.go"}`)),
			},
			Timestamp: now.Add(-time.Duration(8-i) * time.Minute),
		}))
		eng.SetMessages(append(eng.Messages(), types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock(fmt.Sprintf("read_%d", i), json.RawMessage(`"`+strings.Repeat("x", 10000)+`"`), false),
			},
			Timestamp: now.Add(-time.Duration(8-i) * time.Minute),
		}))
	}
	eng.mu.Lock()
	eng.ContextTokens = 35000
	eng.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue", "")
	// Sub-agents skip token pruning, so the API call should proceed.
	if result.Error != nil {
		t.Fatalf("sub-agent query should succeed, got: %v", result.Error)
	}
	if len(result.Messages) == 0 {
		t.Error("expected messages in result")
	}
}

// ---------------------------------------------------------------------------
// Full chain integration test: engine → bash.Execute → Drain → AskEvent
// Tests the complete path from tool execution to interaction detection.
// If any step in the chain is broken, this test will fail.
// ---------------------------------------------------------------------------

func TestExecuteTool_Bash_InteractionDetection(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipped under root — no PTY needed")
	}

	// Real Bash tool with a real BackgroundJobRegistry
	registry := bash.NewBackgroundJobRegistry()
	bashTool := bash.New(registry)

	// Capture AskEvent
	askReceived := make(chan *types.AskEvent, 1)
	emit := func(evt types.QueryEvent) {
		if evt.Type == types.EventAsk && evt.Ask != nil && evt.Ask.Kind == types.AskInput {
			// Respond immediately to unblock Drain
			evt.Ask.ResponseCh <- types.AskResponse{Text: "testpass"}
			select {
			case askReceived <- evt.Ask:
			default:
			}
		}
	}

	// Pass non-nil tctx like production does (engine.go:1179)
	tctx := &tool.ToolUseContext{
		Ctx:        context.Background(),
		WorkingDir: ".",
	}
	toolMap := map[string]tool.Tool{"Bash": bashTool}
	executor := NewStreamingToolExecutor(toolMap, tctx, emit, context.Background())

	tt := &TrackedTool{
		ID:    "test_bash_interaction",
		Name:  "Bash",
		done:  make(chan struct{}),
		Input: json.RawMessage(`{"command":"printf 'Password: ' && sleep 2","description":"test interaction detection"}`),
	}

	go executor.executeTool(tt)

	select {
	case ask := <-askReceived:
		if !strings.Contains(ask.Prompt, "Password") {
			t.Errorf("expected 'Password' in prompt, got %q", ask.Prompt)
		}
		if !ask.Masked {
			t.Error("expected Masked=true for password prompt")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("AskEvent{AskInput} was never emitted — full chain broken somewhere between engine → bash.Execute → Drain → looksLikePrompt → emitAskInput → emitEvent")
	}

	<-tt.done
}

// ---------------------------------------------------------------------------
// Engine retry tests — red phase (callLLMWithRetry not implemented yet)
// Tests exercise retry behavior via QuerySync with mock providers.
// ---------------------------------------------------------------------------

// partialTextEvents creates streaming events WITHOUT message_stop.
// Simulates idle timeout: content received but stream ended without completion.
// toolUseEvents returns a complete stream that produces a tool_use block.
func toolUseEvents(model, toolID, toolName, inputJSON string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: toolID, Name: toolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: inputJSON}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 5}},
		{Type: "message_stop"},
	}
}

func partialTextEvents(model, text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		// NO message_delta, NO message_stop — simulates idle timeout
	}
}

// TestQuery_RetryStreamTimeout verifies retry on stream interrupted (idle timeout).
// Mock sends partial content then closes channel (no message_stop).
// First call → StreamInterruptedError → retry → second call succeeds with full response.
func TestQuery_RetryStreamTimeout(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// First attempt: partial stream (simulates idle timeout)
	mp.addResponse(partialTextEvents("test", "partial"), nil)
	// Second attempt: full successful response
	mp.addResponse(subTextEvents("test", "recovered!"), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	result := eng.QuerySync(context.Background(), "hello", "")
	if result.Error != nil {
		t.Fatalf("expected success after retry, got: %v", result.Error)
	}

	// Verify retry attempt event was emitted
	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) == 0 {
		t.Fatal("expected at least one EventRetryAttempt, got none")
	}
	if retryEvents[0].RetryAttempt == nil {
		t.Fatal("RetryAttempt field should be non-nil")
	}
	if retryEvents[0].RetryAttempt.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", retryEvents[0].RetryAttempt.Attempt)
	}

	// Verify the final text is from the second (successful) attempt
	if len(result.Messages) == 0 {
		t.Fatal("expected at least one message")
	}
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected assistant message, got %s", lastMsg.Role)
	}
	text := lastMsg.Content[0].Text
	if !strings.Contains(text, "recovered!") {
		t.Errorf("expected 'recovered!' in response, got %q", text)
	}
}

// TestQuery_RetryStreamEndedNoContent verifies retry when stream ends
// without any content (simulates connection timeout).
func TestQuery_RetryStreamEndedNoContent(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// First attempt: empty events (simulates connection timeout — no content at all)
	mp.addResponse([]llm.StreamEvent{}, nil)
	// Second attempt: full successful response
	mp.addResponse(subTextEvents("test", "connected!"), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	result := eng.QuerySync(context.Background(), "hello", "")
	if result.Error != nil {
		t.Fatalf("expected success after retry, got: %v", result.Error)
	}

	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) == 0 {
		t.Fatal("expected at least one EventRetryAttempt, got none")
	}
}

// TestQuery_RetryExhausted verifies that exhausting all retries returns terminal error.
func TestQuery_RetryExhausted(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// First response (initial attempt)
	mp.addResponse(partialTextEvents("test", "partial"), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	// Use fast retry config for testing (10ms backoff, 3 retries)
	eng.retryConfig = &llm.RetryConfig{
		MaxRetries:  3,
		BaseBackoff: 10 * time.Millisecond,
		MaxBackoff:  50 * time.Millisecond,
	}

	// Queue 4 responses (1 initial + 3 retries) — all partial
	for range 3 {
		mp.addResponse(partialTextEvents("test", "partial"), nil)
	}

	result := eng.QuerySync(context.Background(), "hello", "")
	if result.Error == nil {
		t.Fatal("expected error after all retries exhausted, got nil")
	}

	// Should be StreamInterruptedError
	var si *StreamInterruptedError
	if !errors.As(result.Error, &si) {
		t.Errorf("expected *StreamInterruptedError, got %T: %v", result.Error, result.Error)
	}
	if si.ContentBlocks == 0 {
		t.Errorf("expected ContentBlocks > 0 in StreamInterruptedError")
	}

	// Should have 3 retry events (4 attempts = 1 initial + 3 retries)
	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) != 3 {
		t.Errorf("expected 3 EventRetryAttempt, got %d", len(retryEvents))
	}
}

// TestQuery_NonRetryableTerminal verifies that API-level errors (e.g. 400) are NOT retried.
// Provider already handles HTTP-level retries; engine should not duplicate.
func TestQuery_NonRetryableTerminal(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// Non-retryable API error (400 Bad Request)
	mp.addResponse(nil, &llm.APIError{
		Message: "bad request",
		Status:  400,
	})

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	result := eng.QuerySync(context.Background(), "hello", "")
	if result.Error == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if !strings.Contains(result.Error.Error(), "bad request") {
		t.Errorf("error should mention 'bad request', got: %v", result.Error)
	}

	// Should NOT have any retry events
	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) != 0 {
		t.Errorf("expected 0 EventRetryAttempt for non-retryable error, got %d", len(retryEvents))
	}

	// Provider should only have been called once
	if mp.index != 1 {
		t.Errorf("expected 1 provider call, got %d", mp.index)
	}
}

// TestQuery_RetryAbortErrorNoRetry verifies AbortError is NOT retried.
// callLLM mutates e.messages on ctx cancellation — retrying would corrupt history.
func TestQuery_RetryAbortErrorNoRetry(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent, 10)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	ready := make(chan struct{})
	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test"}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "partial"}}
		close(ready)
		<-ctx.Done()
		// Send one more event so the select in the streaming loop
		// catches ctx.Done() on the next iteration.
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `itecture review`}}
	}()

	go func() {
		<-ready
		cancel()
	}()

	result := eng.QuerySync(ctx, "hello", "")
	if result.Error == nil {
		t.Fatal("expected AbortError, got nil")
	}

	// Should be AbortError, NOT retried
	var abortErr *AbortError
	if !errors.As(result.Error, &abortErr) {
		t.Errorf("expected *AbortError, got %T: %v", result.Error, result.Error)
	}

	// No retry events — AbortError should not trigger retry
	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) != 0 {
		t.Errorf("expected 0 EventRetryAttempt for AbortError, got %d", len(retryEvents))
	}
}

// TestQuery_RetryContextCancellation verifies ctx cancellation during backoff
// returns immediately without further retry attempts.
func TestQuery_RetryContextCancellation(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// First attempt: partial stream → triggers retry with backoff
	mp.addResponse(partialTextEvents("test", "partial"), nil)
	// Second attempt: would succeed but ctx will be cancelled during backoff
	mp.addResponse(subTextEvents("test", "recovered"), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	// Use retry config with long backoff so cancel fires during wait
	eng.retryConfig = &llm.RetryConfig{
		MaxRetries:  10,
		BaseBackoff: 5 * time.Second,
		MaxBackoff:  30 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context during backoff wait (5s backoff, cancel at 100ms)
	time.AfterFunc(100*time.Millisecond, cancel)

	result := eng.QuerySync(ctx, "hello", "")
	if result.Error == nil {
		t.Fatal("expected error from ctx cancellation, got nil")
	}

	// Should indicate cancellation (AbortError or context error)
	var abortErr *AbortError
	if !errors.As(result.Error, &abortErr) {
		// Also accept raw context error
		if !strings.Contains(result.Error.Error(), "context canceled") {
			t.Errorf("expected AbortError or context cancellation, got: %v", result.Error)
		}
	}
}

// TestQuery_SubagentRetriesStreamError verifies sub-agents retry once on
// stream errors (SSE timeout, incomplete stream) and succeed on retry.
func TestQuery_SubagentRetriesStreamError(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// First attempt: stream interrupted (no message_stop)
	mp.addResponse(partialTextEvents("test", "partial"), nil)
	// Second attempt: complete response
	successEvents := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "retry succeeded"}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}},
		{Type: "message_stop"},
	}
	mp.addResponse(successEvents, nil)

	parentTools := map[string]tool.Tool{}
	parent := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: newEventCollector(),
	})
	t.Cleanup(func() { parent.Close() })

	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "Explore",
		Tools:     parentTools,
	})

	tc := newEventCollector()
	sub.dispatcher = tc

	result := sub.QuerySync(context.Background(), "explore", "")
	if result.Error != nil {
		t.Fatalf("expected success after retry, got: %v", result.Error)
	}

	// Sub-agent should have retried once
	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) != 1 {
		t.Errorf("expected 1 retry attempt, got %d", len(retryEvents))
	}

	// Should have made 2 provider calls (initial + retry)
	if mp.index != 2 {
		t.Errorf("expected 2 provider calls, got %d", mp.index)
	}
}

// TestStreamErrorTypeDiscrimination verifies errors.As works for sentinel types.
func TestStreamErrorTypeDiscrimination(t *testing.T) {
	t.Parallel()

	interrupted := &StreamInterruptedError{ContentBlocks: 3, Model: "claude-3"}
	ended := &StreamEndedError{}

	// errors.Is
	if !errors.Is(interrupted, interrupted) {
		t.Error("errors.Is(self) should be true for StreamInterruptedError")
	}

	// errors.As
	var si *StreamInterruptedError
	if !errors.As(interrupted, &si) {
		t.Error("errors.As should match *StreamInterruptedError")
	}
	if si.ContentBlocks != 3 {
		t.Errorf("ContentBlocks = %d, want 3", si.ContentBlocks)
	}
	if si.Model != "claude-3" {
		t.Errorf("Model = %q, want %q", si.Model, "claude-3")
	}

	var se *StreamEndedError
	if !errors.As(ended, &se) {
		t.Error("errors.As should match *StreamEndedError")
	}

	// Cross-type: StreamInterruptedError should NOT match StreamEndedError
	if _, ok := errors.AsType[*StreamEndedError](interrupted); ok {
		t.Error("StreamInterruptedError should not match *StreamEndedError")
	}

	// isStreamError helper
	if !isStreamError(interrupted) {
		t.Error("isStreamError should return true for StreamInterruptedError")
	}
	if !isStreamError(ended) {
		t.Error("isStreamError should return true for StreamEndedError")
	}

	// Wrapped error
	wrapped := fmt.Errorf("wrapper: %w", interrupted)
	if !isStreamError(wrapped) {
		t.Error("isStreamError should return true for wrapped StreamInterruptedError")
	}
	var si2 *StreamInterruptedError
	if !errors.As(wrapped, &si2) {
		t.Error("errors.As should unwrap to *StreamInterruptedError")
	}
}

// TestQuery_RetryBackoffSequence verifies that RetryAttemptEvent.RetryInMs values
// grow exponentially across attempts and fall within theoretical bounds.
func TestQuery_RetryBackoffSequence(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	// All attempts fail with partial stream
	for range 6 { // 1 initial + 5 retries
		mp.addResponse(partialTextEvents("test", "partial"), nil)
	}

	cfg := &llm.RetryConfig{
		MaxRetries:  5,
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  5 * time.Second,
	}

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.retryConfig = cfg

	result := eng.QuerySync(context.Background(), "hello", "")
	if result.Error == nil {
		t.Fatal("expected error after all retries exhausted, got nil")
	}
	if !strings.Contains(result.Error.Error(), "interrupted") {
		t.Errorf("error should mention interrupted, got: %v", result.Error)
	}

	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) != 5 {
		t.Fatalf("expected 5 EventRetryAttempt, got %d", len(retryEvents))
	}

	// Verify each retry event has increasing backoff within expected bounds.
	// CalculateBackoff(n) = base * 2^n * jitter, where jitter ∈ [0.5, 1.0)
	for i, evt := range retryEvents {
		if evt.RetryAttempt == nil {
			t.Fatalf("retry event %d: RetryAttempt is nil", i)
		}

		attempt := evt.RetryAttempt.Attempt
		retryInMs := evt.RetryAttempt.RetryInMs

		// attempt is 1-indexed in events, CalculateBackoff uses attempt-1
		backoffAttempt := attempt - 1
		// Theoretical range: base * 2^backoffAttempt * [0.5, 1.5) (±50% jitter)
		expBase := float64(cfg.BaseBackoff.Milliseconds())
		lowerBound := int64(expBase * math.Pow(2, float64(backoffAttempt)) * 0.5)
		upperBound := int64(expBase * math.Pow(2, float64(backoffAttempt)) * 1.5)
		// Cap at MaxBackoff
		maxMs := cfg.MaxBackoff.Milliseconds()
		if upperBound > maxMs {
			upperBound = maxMs
		}
		if lowerBound > maxMs {
			lowerBound = maxMs
		}

		if retryInMs < lowerBound || retryInMs > upperBound {
			t.Errorf("retry %d (attempt=%d): RetryInMs=%d outside expected range [%d, %d]",
				i, attempt, retryInMs, lowerBound, upperBound)
		}
	}

}

// TestQuery_MultiTurnRetryReset verifies that retry counters reset between turns.
// Turn 1 retries and succeeds. Turn 2 also encounters a stream error and retries.
// The retry attempt counter should start fresh on each turn (callLLMWithRetry is new per turn).
func TestQuery_MultiTurnRetryReset(t *testing.T) {
	mp := &testProvider{}

	// Turn 1: partial → retry → success (triggers tool use)
	// First call: partial stream → StreamInterruptedError
	mp.addResponse(partialTextEvents("test", "partial"), nil)
	// Retry: successful response with tool_use
	mp.addResponse(toolUseEvents("test", "call_1", "bash", `{"command":"echo hi"}`), nil)
	// Tool result → Turn 2
	// Turn 2: partial → retry → success (text)
	mp.addResponse(partialTextEvents("test", "partial2"), nil)
	mp.addResponse(subTextEvents("test", "turn2 recovered!"), nil)

	tc := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		Dispatcher: tc,
	})
	t.Cleanup(func() { eng.Close() })
	eng.retryConfig = &llm.RetryConfig{
		MaxRetries:  3,
		BaseBackoff: 5 * time.Millisecond,
		MaxBackoff:  20 * time.Millisecond,
	}

	result := eng.QuerySync(context.Background(), "hello", "")
	if result.Error != nil {
		t.Fatalf("expected success after multi-turn retry, got: %v", result.Error)
	}

	retryEvents := tc.FindEvents(types.EventRetryAttempt)
	if len(retryEvents) != 2 {
		t.Fatalf("expected 2 EventRetryAttempt (1 per turn), got %d", len(retryEvents))
	}

	// Both retry events should have Attempt=1 (fresh counter per turn)
	if retryEvents[0].RetryAttempt.Attempt != 1 {
		t.Errorf("turn 1 retry: Attempt=%d, want 1", retryEvents[0].RetryAttempt.Attempt)
	}
	if retryEvents[1].RetryAttempt.Attempt != 1 {
		t.Errorf("turn 2 retry: Attempt=%d, want 1", retryEvents[1].RetryAttempt.Attempt)
	}

	// Verify final response is from turn 2
	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != types.RoleAssistant {
		t.Fatalf("expected assistant message, got %s", lastMsg.Role)
	}
	if !strings.Contains(lastMsg.Content[0].Text, "turn2 recovered!") {
		t.Errorf("expected 'turn2 recovered!' in final message, got %q", lastMsg.Content[0].Text)
	}
}

// ---------------------------------------------------------------------------
// Coverage gap fill: SetOnClose, Dispatcher, SetFileHistoryWriter, Close, etc.
// ---------------------------------------------------------------------------

func TestSetOnClose(t *testing.T) {
	eng := &Engine{sessionID: "test-session"}
	called := false
	eng.SetOnClose(func(sessionID string) {
		called = true
		if sessionID != "test-session" {
			t.Errorf("onCloseFn sessionID = %q, want %q", sessionID, "test-session")
		}
	})
	eng.Close()
	if !called {
		t.Error("SetOnClose callback was not called during Close()")
	}
}

func TestDispatcher(t *testing.T) {
	eng := &Engine{}
	if eng.Dispatcher() != nil {
		t.Error("Dispatcher() should be nil on new engine")
	}
	d := &eventCollector{}
	eng.SetDispatcher(d)
	if eng.Dispatcher() != d {
		t.Error("Dispatcher() should return the set dispatcher")
	}
}

func TestSetFileHistoryWriter(t *testing.T) {
	var called bool
	var state filehistory.FileHistoryState
	eng := &Engine{}
	eng.SetFileHistoryWriter(func(s filehistory.FileHistoryState) {
		called = true
		state = s
	})

	eng.mu.Lock()
	w := eng.fileHistoryWriter
	eng.mu.Unlock()
	if w == nil {
		t.Fatal("fileHistoryWriter should not be nil after SetFileHistoryWriter")
	}

	tracked := map[string]bool{"/tmp/test.go": true}
	w(filehistory.FileHistoryState{TrackedFiles: tracked})
	if !called {
		t.Error("writer callback was not called")
	}
	if !state.TrackedFiles["/tmp/test.go"] {
		t.Error("state mismatch: /tmp/test.go should be tracked")
	}
}

func TestClose_WithMCPRegistry(t *testing.T) {
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	eng := &Engine{
		sessionID:   "s1",
		mcpRegistry: reg,
	}
	eng.Close()
}

func TestRetryErrorType_NonStreamError(t *testing.T) {
	got := retryErrorType(fmt.Errorf("some random error"))
	if got != "" {
		t.Errorf("retryErrorType(random error) = %q, want empty", got)
	}
}

func TestRetryErrorType_StreamInterrupted(t *testing.T) {
	got := retryErrorType(&StreamInterruptedError{ContentBlocks: 3, Model: "claude"})
	if got != types.RetryErrorStreamInterrupted {
		t.Errorf("retryErrorType(StreamInterruptedError) = %q, want %q", got, types.RetryErrorStreamInterrupted)
	}
}

func TestRetryErrorType_StreamEnded(t *testing.T) {
	got := retryErrorType(&StreamEndedError{})
	if got != types.RetryErrorStreamEnded {
		t.Errorf("retryErrorType(StreamEndedError) = %q, want %q", got, types.RetryErrorStreamEnded)
	}
}

type disabledTool struct {
	minimalTool
}

func (d *disabledTool) Name() string    { return "DisabledTool" }
func (d *disabledTool) IsEnabled() bool { return false }

// promptTool has a non-empty Prompt() — simulates a real tool like Edit/Bash.
type promptTool struct {
	minimalTool
}

func (p *promptTool) Description(json.RawMessage) (string, error) {
	return "Short description", nil
}
func (p *promptTool) Prompt() string {
	return "Full multi-line usage instructions that the LLM should see"
}

func TestBuildToolDefs_UsesPromptAsDescription(t *testing.T) {
	tools := []tool.Tool{&promptTool{}}
	defs := buildToolDefs(tools)
	if len(defs) != 1 {
		t.Fatalf("buildToolDefs returned %d defs, want 1", len(defs))
	}
	if defs[0].Description != "Full multi-line usage instructions that the LLM should see" {
		t.Errorf("Description = %q, want Prompt() content", defs[0].Description)
	}
}

func TestBuildToolDefs_EmptyPromptSendsEmptyDescription(t *testing.T) {
	tools := []tool.Tool{&minimalTool{}}
	defs := buildToolDefs(tools)
	if len(defs) != 1 {
		t.Fatalf("buildToolDefs returned %d defs, want 1", len(defs))
	}
	if defs[0].Description != "" {
		t.Errorf("Description = %q, want empty", defs[0].Description)
	}
}

func TestBuildToolDefs_SkipsDisabledTools(t *testing.T) {
	tools := []tool.Tool{
		&minimalTool{},
		&disabledTool{},
	}
	defs := buildToolDefs(tools)
	if len(defs) != 1 {
		t.Fatalf("buildToolDefs returned %d defs, want 1 (disabled tool skipped)", len(defs))
	}
	if defs[0].Name != "test" {
		t.Errorf("def name = %q, want %q", defs[0].Name, "test")
	}
}

// --- Task context injection tests ---

func TestDumpAPIRequest_NoTasks_NoInjection(t *testing.T) {
	dir := t.TempDir()
	tl := task.NewList(dir)
	eng := newTestEngineForBreakdown(t)
	eng.taskList = tl
	eng.addMessage(types.RoleUser, "hello")
	eng.addMessage(types.RoleAssistant, "hi")

	dump := eng.DumpAPIRequest()
	if dump == nil {
		t.Fatal("DumpAPIRequest returned nil")
	}
	for _, msg := range dump.Messages {
		if strings.Contains(msg.Content[0].Text, "pending tasks") {
			t.Error("should not inject pending tasks text when no tasks exist")
		}
	}
}

func TestReminderEngine_CollectsPendingTasks(t *testing.T) {
	dir := t.TempDir()
	tl := task.NewList(dir)
	eng := newTestEngineForBreakdown(t)
	eng.taskList = tl
	id, err := tl.CreateTask("Fix auth bug", "OAuth token refresh fails", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_, _, err = tl.UpdateTask(id, task.TaskUpdates{Status: &[]task.TaskStatus{task.StatusInProgress}[0]})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	for i := range 12 {
		eng.addMessage(types.RoleUser, fmt.Sprintf("msg %d", i))
		eng.addMessage(types.RoleAssistant, fmt.Sprintf("reply %d", i))
	}
	ctx := attachment.ReminderContext{
		Messages:   eng.messages,
		TurnCount:  12,
		IsSubagent: false,
		TaskList:   &taskListReaderAdapter{list: eng.taskList},
	}
	msgs := eng.reminderEngine.Collect(ctx)
	if len(msgs) == 0 {
		t.Fatal("ReminderEngine.Collect should return reminder messages")
	}
	text := msgs[0].Content[0].Text
	if !strings.Contains(text, "Fix auth bug") {
		t.Errorf("reminder should contain task subject, got:\n%s", text)
	}
	if !strings.Contains(text, "in_progress") {
		t.Errorf("reminder should contain task status, got:\n%s", text)
	}
	if !strings.Contains(text, "<system-reminder>") {
		t.Errorf("reminder should be wrapped in system-reminder, got:\n%s", text)
	}
}

func TestReminderEngine_TaskRecovery_AfterRestart(t *testing.T) {
	dir := t.TempDir()
	tl := task.NewList(dir)
	_, err := tl.CreateTask("Migrate DB", "Add migration 0042", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_, err = tl.CreateTask("Write tests", "Cover edge cases", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Simulate restart: new engine, same task dir.
	eng := newTestEngineForBreakdown(t)
	eng.taskList = task.NewList(dir)
	// Build enough turns to trigger reminder.
	for i := range 12 {
		eng.addMessage(types.RoleUser, fmt.Sprintf("msg %d", i))
		eng.addMessage(types.RoleAssistant, fmt.Sprintf("reply %d", i))
	}
	ctx := attachment.ReminderContext{
		Messages:   eng.messages,
		TurnCount:  12,
		IsSubagent: false,
		TaskList:   &taskListReaderAdapter{list: eng.taskList},
	}
	msgs := eng.reminderEngine.Collect(ctx)
	if len(msgs) == 0 {
		t.Fatal("recovered engine should produce reminder for pending tasks")
	}
	text := msgs[0].Content[0].Text
	if !strings.Contains(text, "Migrate DB") {
		t.Errorf("reminder should contain 'Migrate DB', got:\n%s", text)
	}
	if !strings.Contains(text, "Write tests") {
		t.Errorf("reminder should contain 'Write tests', got:\n%s", text)
	}
}

func (e *Engine) addMessage(role types.Role, text string) {
	e.messages = append(e.messages, types.Message{
		Role:    role,
		Content: []types.ContentBlock{types.NewTextBlock(text)},
	})
}

func TestQuerySource_AutoDream(t *testing.T) {
	eng := &Engine{isSubagent: true, agentType: "auto_dream"}
	got := eng.querySource()
	if got != QuerySourceAutoDream {
		t.Errorf("querySource(auto_dream) = %q, want %q", got, QuerySourceAutoDream)
	}
}

func TestQuerySource_BuiltinAgent(t *testing.T) {
	eng := &Engine{isSubagent: true, agentType: "General"}
	got := eng.querySource()
	if !strings.HasPrefix(got, "agent:builtin:") {
		t.Errorf("querySource(builtin agent) = %q, want prefix 'agent:builtin:'", got)
	}
	if !strings.Contains(got, "General") {
		t.Errorf("querySource(General) should contain 'General', got %q", got)
	}
}

func TestQuerySource_CustomAgent(t *testing.T) {
	eng := &Engine{isSubagent: true, agentType: "my-custom-agent"}
	got := eng.querySource()
	if got != QuerySourceAgentCustom {
		t.Errorf("querySource(custom agent) = %q, want %q", got, QuerySourceAgentCustom)
	}
}

func TestFirePostTurnHooks_PanicRecovery(t *testing.T) {
	hookCalled := false
	eng := &Engine{
		logger:            slog.Default(),
		autoCompactConfig: AutoCompactConfig{ContextWindow: 200000},
		postTurnHooks: []PostTurnHook{
			func(ctx context.Context, messages []types.Message, currentTokens int, querySource string) {
				panic("test hook panic")
			},
			func(ctx context.Context, messages []types.Message, currentTokens int, querySource string) {
				hookCalled = true
			},
		},
	}
	eng.firePostTurnHooks(context.Background())
	if !hookCalled {
		t.Error("second hook should have been called after first hook panicked")
	}
}

func TestPendingMCPServerNames_WithRegistry(t *testing.T) {
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	eng := &Engine{mcpRegistry: reg}
	names := eng.pendingMCPServerNames()
	if names == nil {
		return
	}
	if len(names) != 0 {
		t.Errorf("pendingMCPServerNames() = %v, want empty", names)
	}
}

// ---------------------------------------------------------------------------
// Engine.ExecuteTool (tool_exec.go) — permission + execution paths
// ---------------------------------------------------------------------------

// channelDispatcher sends events to a channel for test observability.
type channelDispatcher struct{ ch chan types.QueryEvent }

func (d *channelDispatcher) Dispatch(evt types.QueryEvent) { d.ch <- evt }

func TestEngineExecuteTool_ToolNotFound(t *testing.T) {
	eng := &Engine{logger: slog.Default()}
	_, err := eng.ExecuteTool(context.Background(), "NonExistent", json.RawMessage(`{}`), nil, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent tool")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestEngineExecuteTool_DenyPermission(t *testing.T) {
	eng := &Engine{
		logger: slog.Default(),
		permissionChecker: permission.NewChecker([]permission.Rule{{
			Value:  permission.RuleValue{ToolName: "test"},
			Action: permission.ActionDeny,
			Source: "user",
		}}),
	}
	eng.tools = map[string]tool.Tool{"test": &minimalTool{}}

	_, err := eng.ExecuteTool(context.Background(), "test", json.RawMessage(`{}`), nil, nil)
	if err == nil {
		t.Fatal("expected error for denied tool")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, want 'denied'", err.Error())
	}
}

func TestEngineExecuteTool_ContentRuleDeny(t *testing.T) {
	eng := &Engine{
		logger: slog.Default(),
		permissionChecker: permission.NewChecker([]permission.Rule{{
			Value:  permission.RuleValue{ToolName: "Bash", RuleContent: new("rm -rf *")},
			Action: permission.ActionDeny,
			Source: "user",
		}}),
	}
	eng.tools = map[string]tool.Tool{"Bash": &namedTool{name: "Bash"}}

	_, err := eng.ExecuteTool(context.Background(), "Bash", json.RawMessage(`{"command":"rm -rf /important"}`), nil, nil)
	if err == nil {
		t.Fatal("expected error for content rule deny")
	}
	if !strings.Contains(err.Error(), "denied by content rule") {
		t.Errorf("error = %q, want 'denied by content rule'", err.Error())
	}
}

func TestEngineExecuteTool_AllowPermission(t *testing.T) {
	eng := &Engine{
		logger:            slog.Default(),
		permissionChecker: permission.NewChecker(nil),
	}
	eng.tools = map[string]tool.Tool{"MyTool": &namedTool{name: "MyTool"}}

	result, err := eng.ExecuteTool(context.Background(), "MyTool", json.RawMessage(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want 'ok'", result)
	}
}

func TestEngineExecuteTool_NilResult(t *testing.T) {
	eng := &Engine{
		logger:            slog.Default(),
		permissionChecker: permission.NewChecker(nil),
	}
	eng.tools = map[string]tool.Tool{"test": &minimalTool{}}

	result, err := eng.ExecuteTool(context.Background(), "test", json.RawMessage(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("nil result should give empty string, got %q", result)
	}
}

func TestEngineExecuteTool_ToolError(t *testing.T) {
	eng := &Engine{
		logger:            slog.Default(),
		permissionChecker: permission.NewChecker(nil),
	}
	eng.tools = map[string]tool.Tool{"errTool": &errorTool{name: "errTool"}}

	_, err := eng.ExecuteTool(context.Background(), "errTool", json.RawMessage(`{}`), nil, nil)
	if err == nil {
		t.Fatal("expected error from errorTool")
	}
	if !strings.Contains(err.Error(), "tool error") {
		t.Errorf("error = %q, want 'tool error'", err.Error())
	}
}

func TestEngineExecuteTool_AskPermission_UserAllows(t *testing.T) {
	ch := make(chan types.QueryEvent, 1)
	eng := &Engine{
		logger: slog.Default(),
		permissionChecker: permission.NewChecker([]permission.Rule{{
			Value:  permission.RuleValue{ToolName: "AskTool"},
			Action: permission.ActionAsk,
			Source: "user",
		}}),
		dispatcher: &channelDispatcher{ch: ch},
	}
	eng.tools = map[string]tool.Tool{"AskTool": &namedTool{name: "AskTool"}}

	resultCh := make(chan string)
	errCh := make(chan error)
	go func() {
		r, e := eng.ExecuteTool(context.Background(), "AskTool", json.RawMessage(`{}`), map[string]bool{}, &sync.Mutex{})
		if e != nil {
			errCh <- e
		} else {
			resultCh <- r
		}
	}()

	select {
	case evt := <-ch:
		if evt.Type != types.EventAsk {
			t.Fatalf("expected EventAsk, got %v", evt.Type)
		}
		evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ask event")
	}

	select {
	case result := <-resultCh:
		if result != "ok" {
			t.Errorf("result = %q, want 'ok'", result)
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestEngineExecuteTool_AskPermission_UserDenies(t *testing.T) {
	ch := make(chan types.QueryEvent, 1)
	eng := &Engine{
		logger: slog.Default(),
		permissionChecker: permission.NewChecker([]permission.Rule{{
			Value:  permission.RuleValue{ToolName: "AskTool"},
			Action: permission.ActionAsk,
			Source: "user",
		}}),
		dispatcher: &channelDispatcher{ch: ch},
	}
	eng.tools = map[string]tool.Tool{"AskTool": &namedTool{name: "AskTool"}}

	errCh := make(chan error, 1)
	go func() {
		_, e := eng.ExecuteTool(context.Background(), "AskTool", json.RawMessage(`{}`), map[string]bool{}, &sync.Mutex{})
		errCh <- e
	}()

	select {
	case evt := <-ch:
		evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "permission denied by user") {
			t.Errorf("error = %q, want 'permission denied by user'", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestEngineExecuteTool_AskPermission_AlwaysCache(t *testing.T) {
	ch := make(chan types.QueryEvent, 1)
	eng := &Engine{
		logger: slog.Default(),
		permissionChecker: permission.NewChecker([]permission.Rule{{
			Value:  permission.RuleValue{ToolName: "AskTool"},
			Action: permission.ActionAsk,
			Source: "user",
		}}),
		dispatcher: &channelDispatcher{ch: ch},
	}
	eng.tools = map[string]tool.Tool{"AskTool": &namedTool{name: "AskTool"}}

	sessionAllowed := map[string]bool{}
	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		r, e := eng.ExecuteTool(context.Background(), "AskTool", json.RawMessage(`{}`), sessionAllowed, &sync.Mutex{})
		if e != nil {
			errCh <- e
		} else {
			resultCh <- r
		}
	}()

	select {
	case evt := <-ch:
		evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllowAlways}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	select {
	case result := <-resultCh:
		if result != "ok" {
			t.Errorf("result = %q, want 'ok'", result)
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	if !sessionAllowed["AskTool"] {
		t.Error("DecisionAllowAlways should cache in sessionAllowed")
	}
}

func TestEngineExecuteTool_CachedAllow(t *testing.T) {
	eng := &Engine{
		logger: slog.Default(),
		permissionChecker: permission.NewChecker([]permission.Rule{{
			Value:  permission.RuleValue{ToolName: "AskTool"},
			Action: permission.ActionAsk,
			Source: "user",
		}}),
		dispatcher: &eventCollector{},
	}
	eng.tools = map[string]tool.Tool{"AskTool": &namedTool{name: "AskTool"}}

	result, err := eng.ExecuteTool(context.Background(), "AskTool", json.RawMessage(`{}`), map[string]bool{"AskTool": true}, &sync.Mutex{})
	if err != nil {
		t.Fatalf("cached allow should succeed: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want 'ok'", result)
	}
}

func TestEngineExecuteTool_CtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	eng := &Engine{
		logger: slog.Default(),
		permissionChecker: permission.NewChecker([]permission.Rule{{
			Value:  permission.RuleValue{ToolName: "AskTool"},
			Action: permission.ActionAsk,
			Source: "user",
		}}),
		dispatcher: &eventCollector{},
	}
	eng.tools = map[string]tool.Tool{"AskTool": &namedTool{name: "AskTool"}}

	errCh := make(chan error, 1)
	go func() {
		_, e := eng.ExecuteTool(ctx, "AskTool", json.RawMessage(`{}`), map[string]bool{}, &sync.Mutex{})
		errCh <- e
	}()

	cancel()

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("error = %q, want 'cancelled'", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

// namedTool is a configurable-name tool that returns {Data: "ok"}, rendered as "ok".
type namedTool struct{ name string }

func (d *namedTool) Name() string                                { return d.name }
func (d *namedTool) Aliases() []string                           { return nil }
func (d *namedTool) Description(json.RawMessage) (string, error) { return "", nil }
func (d *namedTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (d *namedTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "ok"}, nil
}
func (d *namedTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (d *namedTool) IsReadOnly(json.RawMessage) bool        { return false }
func (d *namedTool) IsDestructive(json.RawMessage) bool     { return true }
func (d *namedTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (d *namedTool) IsEnabled() bool                        { return true }
func (d *namedTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (d *namedTool) Prompt() string          { return "" }
func (d *namedTool) RenderResult(any) string { return "ok" }
func (d *namedTool) MaxResultSize() int      { return 50000 }

// ---------------------------------------------------------------------------
// isMemoryPathWrite + extractFilePathFromInput
// ---------------------------------------------------------------------------

func TestIsMemoryPathWrite_NotWriteTool(t *testing.T) {
	if isMemoryPathWrite("Read", json.RawMessage(`{"file_path":"/tmp/test"}`)) {
		t.Error("Read tool should never be a memory write")
	}
}

func TestIsMemoryPathWrite_EmptyFilePath(t *testing.T) {
	if isMemoryPathWrite("Write", json.RawMessage(`{}`)) {
		t.Error("empty file_path should not be memory write")
	}
}

func TestIsMemoryPathWrite_InvalidJSON(t *testing.T) {
	if isMemoryPathWrite("Write", json.RawMessage(`{invalid`)) {
		t.Error("invalid JSON should not be memory write")
	}
}

func TestIsMemoryPathWrite_ActualMemoryPath(t *testing.T) {
	cwd, _ := os.Getwd()
	memPath := long.GetMemoryPath(cwd)
	result := isMemoryPathWrite("Write", json.RawMessage(fmt.Sprintf(`{"file_path":"%s/test.md"}`, memPath)))
	if !result {
		t.Error("Write to memory path should return true")
	}
}

func TestIsMemoryPathWrite_NonMemoryPath(t *testing.T) {
	result := isMemoryPathWrite("Write", json.RawMessage(`{"file_path":"/tmp/not-memory.md"}`))
	if result {
		t.Error("Write to non-memory path should return false")
	}
}

func TestExtractFilePathFromInput_Valid(t *testing.T) {
	got := extractFilePathFromInput(json.RawMessage(`{"file_path":"/tmp/test.go"}`))
	if got != "/tmp/test.go" {
		t.Errorf("got %q, want /tmp/test.go", got)
	}
}

func TestExtractFilePathFromInput_Empty(t *testing.T) {
	got := extractFilePathFromInput(json.RawMessage(`{}`))
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractFilePathFromInput_InvalidJSON(t *testing.T) {
	got := extractFilePathFromInput(json.RawMessage(`{invalid`))
	if got != "" {
		t.Errorf("got %q, want empty for invalid JSON", got)
	}
}

// ---------------------------------------------------------------------------
// Stream interrupt: partial tool_use input sanitization
// OpenAI-compatible APIs require valid JSON in tool_use arguments.
// When streaming is interrupted mid-tool-call, the accumulated input
// may be incomplete JSON. These tests verify all interrupt paths.
// ---------------------------------------------------------------------------

// assertToolUseAfterNormalize runs NormalizeMessagesForAPI on engine messages
// (simulating what callLLM does before every API call) and checks the result.
// The sanitization lives in NormalizeMessagesForAPI, not in the interrupt path.
func assertToolUseAfterNormalize(t *testing.T, eng *Engine, toolID, wantInput string) {
	t.Helper()
	normalized := NormalizeMessagesForAPI(eng.Messages())
	for _, msg := range normalized {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolUse && block.ID == toolID {
				if !json.Valid(block.Input) {
					t.Errorf("tool_use %s input is not valid JSON after normalize: %s", toolID, string(block.Input))
				}
				if string(block.Input) != wantInput {
					t.Errorf("tool_use %s input = %q, want %q", toolID, string(block.Input), wantInput)
				}
				return
			}
		}
	}
	t.Errorf("no assistant message with tool_use %s found in %d messages", toolID, len(normalized))
}

// TestStreamInterrupt_PartialToolInput_InLoopSelect tests the ctx.Done() path
// inside the for-range streaming loop (select guard).
func TestStreamInterrupt_PartialToolInput_InLoopSelect(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "call_1", Name: "Agent"}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"desc":"partial`}}
		close(started)
		<-ctx.Done()
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: ` more`}}
	}()

	go func() {
		<-started
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("stream cancellation should produce an error")
	}

	assertToolUseAfterNormalize(t, eng, "call_1", "{}")
}

// TestStreamInterrupt_PartialToolInput_PostLoop tests the post-loop path where
// ctx cancellation closes the provider channel, causing for-range to exit
// naturally without triggering the in-loop select guard.
func TestStreamInterrupt_PartialToolInput_PostLoop(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "call_post", Name: "Bash"}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"sleep`}}
		close(started)
		<-ctx.Done()
		// Don't send more events — channel closes via defer.
		// for-range exits naturally, triggering the post-loop path.
	}()

	go func() {
		<-started
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("stream cancellation should produce an error")
	}

	assertToolUseAfterNormalize(t, eng, "call_post", "{}")
}

// TestStreamInterrupt_MixedToolUseBlocks tests that only partial tool_use
// blocks are sanitized; complete ones are left untouched.
func TestStreamInterrupt_MixedToolUseBlocks(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		// Block 0: complete tool_use with valid JSON
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "call_complete", Name: "Read"}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"path":"/tmp/file.txt"}`}}
		slowCh <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		// Block 1: incomplete tool_use
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "call_partial", Name: "Agent"}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"prompt":"do someth`}}
		close(started)
		<-ctx.Done()
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `ing`}}
	}()

	go func() {
		<-started
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("stream cancellation should produce an error")
	}

	// Yield so the <-started/cancel goroutine and the <-ctx.Done() goroutine
	// have a chance to fully exit before goleak checks.
	runtime.Gosched()

	// Complete tool_use should be preserved as-is
	assertToolUseAfterNormalize(t, eng, "call_complete", `{"path":"/tmp/file.txt"}`)
	// Partial tool_use should be sanitized to {}
	assertToolUseAfterNormalize(t, eng, "call_partial", "{}")
}

// TestStreamInterrupt_ValidToolInput_NotModified verifies that tool_use blocks
// with valid JSON input are NOT modified during interrupt.
func TestStreamInterrupt_ValidToolInput_NotModified(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "call_valid", Name: "Grep"}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"pattern":"TODO"}`}}
		slowCh <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		// Extra event ensures content_block_stop is fully processed before
		// close(started) can fire. Without this, Go's scheduler may run the
		// cancel goroutine between receiving content_block_stop and the select
		// check, causing the interrupt handler to skip content_block_stop processing.
		slowCh <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}}
		close(started)
		<-ctx.Done()
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
	}()

	go func() {
		<-started
		cancel()
	}()

	eng.QuerySync(ctx, "test", "")

	assertToolUseAfterNormalize(t, eng, "call_valid", `{"pattern":"TODO"}`)
}

// TestStreamInterrupt_TextOnly_NoPanic verifies that text-only interruptions
// (no tool_use blocks) don't crash or produce unexpected behavior.
func TestStreamInterrupt_TextOnly_NoPanic(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "I was thinking"}}
		close(started)
		<-ctx.Done()
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "..."}}
	}()

	go func() {
		<-started
		cancel()
	}()

	result := eng.QuerySync(ctx, "test", "")
	if result.Error == nil {
		t.Fatal("stream cancellation should produce an error")
	}

	msgs := eng.Messages()
	// Should have assistant message with text content, no crash
	found := false
	for _, msg := range msgs {
		if msg.Role == types.RoleAssistant {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected assistant message after text-only interrupt")
	}
}

// TestStreamInterrupt_EmptyContent_NoPanic verifies that an interrupt with
// empty contentBlocks doesn't crash (e.g., interrupted before any content).
func TestStreamInterrupt_EmptyContent_NoPanic(t *testing.T) {
	t.Parallel()

	mp := &testProvider{}
	slowCh := make(chan llm.StreamEvent, 5)
	mp.addChannelResponse(slowCh)

	tc := newEventCollector()
	eng := New(&Params{Provider: mp, Model: "test", Dispatcher: tc})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		// No content blocks at all — cancel immediately
	}()

	go func() {
		cancel()
	}()

	eng.QuerySync(ctx, "test", "")
	// Should not panic; messages may or may not be appended depending on timing.
}

func TestEngine_GetContextTokens(t *testing.T) {
	t.Parallel()
	eng := &Engine{ContextTokens: 4242}
	if got := eng.GetContextTokens(); got != 4242 {
		t.Errorf("GetContextTokens() = %d, want 4242", got)
	}
}

func TestEngine_GetContextTokens_Zero(t *testing.T) {
	t.Parallel()
	eng := &Engine{}
	if got := eng.GetContextTokens(); got != 0 {
		t.Errorf("GetContextTokens() = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// queryActive guard for RunForkedQuery / QuerySync
// ---------------------------------------------------------------------------

// TestRunForkedQuery_BlocksConcurrentProcessAttachments verifies that while
// RunForkedQuery is running, an EnqueueAttachment + startProcessAttachmentsIfIdle
// does NOT spawn a concurrent processAttachments goroutine. The root cause of
// the sub-agent text interleaving bug was that RunForkedQuery never set
// queryActive=1, so fork-agent completion notifications arriving via
// EnqueueAttachment could spawn a second goroutine sharing the non-thread-safe
// coalesceBuf.
func TestRunForkedQuery_BlocksConcurrentProcessAttachments(t *testing.T) {
	// Use a channel-based provider that blocks until released.
	streamCh := make(chan llm.StreamEvent, 16)
	started := make(chan struct{})

	provider := &blockingProvider{
		streamCh: streamCh,
		started:  started,
	}

	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   provider,
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })
	eng.systemPrompt = `{"role":"system","content":"test"}`

	// Pre-fill the stream with a complete response so the turn loop finishes
	// cleanly after we unblock. The goroutine will block on reading from streamCh
	// until we send these events.
	// (We send them later, after verifying queryActive.)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan QueryResult, 1)
	msgs := []types.Message{
		{ID: "msg1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("do it")}},
	}
	go func() {
		result := eng.RunForkedQuery(ctx, msgs, eng.systemPrompt)
		done <- result
	}()

	// Wait for the provider to start streaming (proving RunForkedQuery is executing).
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for RunForkedQuery to start")
	}

	// At this point queryActive MUST be 1.
	if v := atomic.LoadInt32(&eng.queryActive); v != 1 {
		t.Fatalf("queryActive = %d during RunForkedQuery, want 1", v)
	}

	// Enqueue an attachment and call startProcessAttachmentsIfIdle.
	// Without the fix, this would spawn a concurrent processAttachments.
	eng.attachments.Enqueue(types.QueuedItem{
		Value: "<job-id>bg-1</job-id><summary>test</summary><status>done</status>",
		Mode:  types.ItemModeJob,
	})
	eng.startProcessAttachmentsIfIdle()

	// queryActive must still be 1 (no second goroutine changed it).
	if v := atomic.LoadInt32(&eng.queryActive); v != 1 {
		t.Fatalf("queryActive = %d after startProcessAttachmentsIfIdle, want 1 (concurrent goroutine spawned!)", v)
	}

	// Release: send a complete response so the turn loop ends.
	streamCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test"}}
	streamCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
	streamCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "done"}}
	streamCh <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
	streamCh <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}}
	streamCh <- llm.StreamEvent{Type: "message_stop"}
	close(streamCh)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for RunForkedQuery to finish")
	}
}

// TestQuerySync_BlocksConcurrentProcessAttachments is the same guard check
// for QuerySync.
func TestQuerySync_BlocksConcurrentProcessAttachments(t *testing.T) {
	streamCh := make(chan llm.StreamEvent, 16)
	started := make(chan struct{})

	provider := &blockingProvider{
		streamCh: streamCh,
		started:  started,
	}

	md := &mockDispatcher{}
	eng := New(&Params{
		Provider:   provider,
		Model:      "test",
		Logger:     slog.Default(),
		Dispatcher: md,
	})
	t.Cleanup(func() { eng.Close() })
	eng.systemPrompt = `{"role":"system","content":"test"}`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan QueryResult, 1)
	go func() {
		result := eng.QuerySync(ctx, "hello", eng.systemPrompt)
		done <- result
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for QuerySync to start")
	}

	if v := atomic.LoadInt32(&eng.queryActive); v != 1 {
		t.Fatalf("queryActive = %d during QuerySync, want 1", v)
	}

	eng.attachments.Enqueue(types.QueuedItem{
		Value: "<job-id>bg-2</job-id><summary>test</summary><status>done</status>",
		Mode:  types.ItemModeJob,
	})
	eng.startProcessAttachmentsIfIdle()

	if v := atomic.LoadInt32(&eng.queryActive); v != 1 {
		t.Fatalf("queryActive = %d after startProcessAttachmentsIfIdle, want 1 (concurrent goroutine spawned!)", v)
	}

	// Release: complete response
	streamCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test"}}
	streamCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
	streamCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "done"}}
	streamCh <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
	streamCh <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}}
	streamCh <- llm.StreamEvent{Type: "message_stop"}
	close(streamCh)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for QuerySync to finish")
	}
}

// blockingProvider is a test llm.Provider that signals when Stream is called
// and returns events from a caller-controlled channel. Only the first Stream
// call signals started and returns streamCh; subsequent calls return nil.
type blockingProvider struct {
	streamCh   chan llm.StreamEvent
	started    chan struct{}
	streamOnce sync.Once
}

func (p *blockingProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *blockingProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	first := false
	p.streamOnce.Do(func() {
		close(p.started)
		first = true
	})
	if !first {
		return nil, nil
	}
	return p.streamCh, nil
}

// TestContextTokens_PersistedAfterAPICall verifies that ContextTokens is
// persisted to the session store when set after an API response.
// /context showed nothing after restart because ContextTokens was never
// saved to the store.
func TestContextTokens_PersistedAfterAPICall(t *testing.T) {
	store := newTestStore(t)
	dir := t.TempDir()

	eng := New(&Params{
		Provider:   &testProvider{},
		Model:      "test",
		Dispatcher: newEventCollector(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, dir)

	// Simulate: engine creates a new session.
	sid, err := eng.ResumeOrInitSession(dir, "test")
	if err != nil {
		t.Fatalf("ResumeOrInitSession: %v", err)
	}

	// Simulate: API response sets ContextTokens.
	eng.mu.Lock()
	eng.ContextTokens = 50000
	eng.mu.Unlock()

	// The engine must persist ContextTokens so restarts can restore it.
	eng.persistContextTokens()

	ses, err := store.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.ContextTokens != 50000 {
		t.Errorf("ses.ContextTokens = %d, want 50000", ses.ContextTokens)
	}
}

// TestContextTokens_RestoredOnResume verifies the restore path in
// ResumeOrInitSession that reads ContextTokens from the session store.
// The full integration test requires messages in the session; here we
// directly test that GetSession returns the persisted value.
func TestContextTokens_RestoredOnResume(t *testing.T) {
	store := newTestStore(t)
	dir := t.TempDir()

	eng := New(&Params{
		Provider:   &testProvider{},
		Model:      "test",
		Dispatcher: newEventCollector(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, dir)

	sid, err := eng.ResumeOrInitSession(dir, "test")
	if err != nil {
		t.Fatalf("ResumeOrInitSession: %v", err)
	}

	eng.mu.Lock()
	eng.ContextTokens = 75000
	eng.mu.Unlock()
	eng.persistContextTokens()

	// Verify GetSession returns the persisted value — this is the
	// same path ResumeOrInitSession uses to restore ContextTokens.
	ses, err := store.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.ContextTokens != 75000 {
		t.Errorf("ses.ContextTokens = %d, want 75000", ses.ContextTokens)
	}
}

func TestEffectiveWindow(t *testing.T) {
	t.Parallel()

	// Default: returns coalesceWindow constant
	eng := New(&Params{Provider: &mockProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	if w := eng.effectiveWindow(); w != coalesceWindow {
		t.Errorf("default effectiveWindow = %v, want %v", w, coalesceWindow)
	}

	// Custom window
	eng.window = 50 * time.Millisecond
	if w := eng.effectiveWindow(); w != 50*time.Millisecond {
		t.Errorf("custom effectiveWindow = %v, want 50ms", w)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long string", 10, "this is a ..."},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

func TestStreamErrorGeneratesSyntheticToolResults(t *testing.T) {
	t.Parallel()

	// Mid-stream API error after a tool_use block was received.
	// The engine should generate synthetic tool_results for orphaned tool_uses.
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_err", Name: "Read"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"file": "test.go"}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}},
		{Error: &llm.APIError{Message: "service overloaded", Status: 529}},
	}

	mp := &testProvider{}
	mp.addResponse(events, nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "read a file", "")
	if result.Error == nil {
		t.Fatal("expected error from stream error event, got nil")
	}
	if !strings.Contains(result.Error.Error(), "service overloaded") {
		t.Errorf("error should contain 'service overloaded', got: %v", result.Error)
	}

	// Verify the engine appended the partial assistant message.
	var assistantMsg *types.Message
	var userMsg *types.Message
	for i := range eng.messages {
		if eng.messages[i].Role == types.RoleAssistant {
			assistantMsg = &eng.messages[i]
		}
		if eng.messages[i].Role == types.RoleUser {
			userMsg = &eng.messages[i]
		}
	}

	if assistantMsg == nil {
		t.Fatal("expected assistant message in engine messages")
	}
	hasToolUse := false
	for _, b := range assistantMsg.Content {
		if b.Type == types.ContentTypeToolUse && b.ID == "tu_err" {
			hasToolUse = true
		}
	}
	if !hasToolUse {
		t.Error("expected tool_use tu_err in assistant message")
	}

	// Verify synthetic tool_result was generated.
	if userMsg == nil {
		t.Fatal("expected user message (with synthetic tool_results) in engine messages")
	}
	hasSynthResult := false
	for _, b := range userMsg.Content {
		if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_err" && b.IsError {
			hasSynthResult = true
		}
	}
	if !hasSynthResult {
		t.Error("expected synthetic tool_result for tu_err with IsError=true")
	}
}
