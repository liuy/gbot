package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// minimalTool is a minimal tool implementation for covers skip path in executeTools.
type minimalTool struct{}

func (m *minimalTool) Name() string                                                { return "test" }
func (m *minimalTool) Aliases() []string                                           { return nil }
func (m *minimalTool) Description(json.RawMessage) (string, error)                 { return "test", nil }
func (m *minimalTool) InputSchema() json.RawMessage                                { return nil }
func (m *minimalTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *minimalTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *minimalTool) IsReadOnly(json.RawMessage) bool            { return true }
func (m *minimalTool) IsDestructive(json.RawMessage) bool         { return false }
func (m *minimalTool) IsConcurrencySafe(json.RawMessage) bool     { return true }
func (m *minimalTool) IsEnabled() bool                            { return true }
func (m *minimalTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (m *minimalTool) Prompt() string                             { return "" }
func (m *minimalTool) RenderResult(any) string                    { return "" }

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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 1}},
		{Type: "message_stop"},
	}
	for i := 0; i < 50; i++ {
		mp.addResponse(toolEvents, nil)
	}

	mt := &testTool{name: "tool"}
	eng := New(&Params{
		Provider:    mp,
		Tools:       []tool.Tool{mt},
		Model:       "test",
		TokenBudget: 999999,
	})

	eventCh, resultCh := eng.Query(context.Background(), "test", nil)
	for range eventCh {
	}
	result := <-resultCh
	// After 50 turns the for loop exits, hitting line 226-231
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted after max turns, got %s", result.Terminal)
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
	// Create a slow-streaming provider: sends events with a delay between them.
	// This gives us a window where the stream channel has not-yet-consumed events,
	// allowing ctx.Done() to fire during the for-range iteration.
	slowCh := make(chan llm.StreamEvent, 10)
	mp.addChannelResponse(slowCh)

	eng := New(&Params{
		Provider: mp,
		Model:    "test",
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Start sending events slowly in a goroutine
	go func() {
		defer close(slowCh)
		slowCh <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 5}}}
		slowCh <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		// Delay before next event — cancel ctx during this window
		time.Sleep(100 * time.Millisecond)
		// After this point, callLLM should detect ctx.Done() and return
		slowCh <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "x"}}
	}()

	eventCh, resultCh := eng.Query(ctx, "test", nil)

	// Cancel after a short delay — during streaming
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Drain events
	for range eventCh {
	}
	result := <-resultCh
	if result.Error == nil {
		t.Error("expected error from cancelled context during streaming")
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
				"Zulu":   &minimalTool{},
				"Alpha":  &minimalTool{},
				"Bravo":  &minimalTool{},
			}
		},
	})

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
// StreamAccumulator.ProcessEvent — thinking events (line 81-86, 103-105, 121-131)
// ---------------------------------------------------------------------------

func TestProcessEvent_ThinkingEvents(t *testing.T) {
	t.Parallel()

	a := NewStreamAccumulator()

	// content_block_start with thinking type
	evt := llm.StreamEvent{
		Type:          "content_block_start",
		Index:         0,
		ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking, ID: "think1"},
	}
	emit, err := a.ProcessEvent(evt)
	if err != nil {
		t.Fatalf("ProcessEvent error: %v", err)
	}
	if emit == nil {
		t.Fatal("expected EventThinkingStart emit")
	}
	if emit.Type != types.EventThinkingStart {
		t.Errorf("expected EventThinkingStart, got %s", emit.Type)
	}

	// thinking_delta
	evt = llm.StreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: "thinking..."},
	}
	emit, err = a.ProcessEvent(evt)
	if err != nil {
		t.Fatalf("ProcessEvent error: %v", err)
	}
	if emit != nil {
		t.Errorf("thinking_delta should not emit: got %v", emit)
	}

	// content_block_stop with thinking → emits EventThinkingEnd
	evt = llm.StreamEvent{
		Type:  "content_block_stop",
		Index: 0,
	}
	emit, err = a.ProcessEvent(evt)
	if err != nil {
		t.Fatalf("ProcessEvent error: %v", err)
	}
	if emit == nil {
		t.Fatal("expected EventThinkingEnd emit")
	}
	if emit.Type != types.EventThinkingEnd {
		t.Errorf("expected EventThinkingEnd, got %s", emit.Type)
	}
	if emit.Thinking == nil {
		t.Fatal("Thinking event should be non-nil")
	}
	if emit.Thinking.Duration < 0 {
		t.Errorf("Duration should be >= 0, got %v", emit.Thinking.Duration)
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

func (t *nonStreamingSuccessTool) Name() string                                                             { return t.name }
func (t *nonStreamingSuccessTool) Aliases() []string                                                        { return nil }
func (t *nonStreamingSuccessTool) Description(json.RawMessage) (string, error)                             { return t.name, nil }
func (t *nonStreamingSuccessTool) InputSchema() json.RawMessage                                              { return json.RawMessage(`{}`) }
func (t *nonStreamingSuccessTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: t.data}, nil
}
func (t *nonStreamingSuccessTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult { return types.PermissionAllowDecision{} }
func (t *nonStreamingSuccessTool) IsReadOnly(json.RawMessage) bool         { return true }
func (t *nonStreamingSuccessTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *nonStreamingSuccessTool) IsConcurrencySafe(json.RawMessage) bool     { return true }
func (t *nonStreamingSuccessTool) IsEnabled() bool                            { return true }
func (t *nonStreamingSuccessTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *nonStreamingSuccessTool) Prompt() string                             { return "" }
func (t *nonStreamingSuccessTool) RenderResult(any) string                    { return "rendered output" }

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
	results := executor.ExecuteAll(blocks)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Type != types.ContentTypeToolResult {
		t.Errorf("expected ToolResult block, got %s", results[0].Type)
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

	var cancelled bool
	toolMap := map[string]tool.Tool{
		"slow": &slowCancelTool{onCancel: func() { cancelled = true }},
	}

	var emitted []types.QueryEvent
	executor := NewStreamingToolExecutor(toolMap, nil, func(e types.QueryEvent) {
		emitted = append(emitted, e)
	}, context.Background())

	executor.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "slow"})

	// Let the goroutine start and enter tool.Call (blocking on ctx.Done)
	// before Discard sets the flag. Without this, the early abort path wins.
	time.Sleep(50 * time.Millisecond)

	executor.Discard()

	// Wait for the tool to receive the cancellation
	time.Sleep(100 * time.Millisecond)

	if !cancelled {
		t.Error("tool context should be cancelled after Discard()")
	}
}

func TestStreamingToolExecutor_DiscardPreventsQueuedStart(t *testing.T) {
	t.Parallel()

	var started bool
	toolMap := map[string]tool.Tool{
		"never_run": &neverRunTool{onStart: func() { started = true }},
	}

	executor := NewStreamingToolExecutor(toolMap, nil, func(types.QueryEvent) {}, context.Background())
	executor.AddTool(types.ContentBlock{Type: types.ContentTypeToolUse, ID: "t1", Name: "never_run"})
	executor.Discard()

	// Give queued tool time to potentially start
	time.Sleep(100 * time.Millisecond)

	if started {
		t.Error("queued tool should not start after Discard()")
	}
}

// slowCancelTool blocks until context is cancelled, then reports it.
type slowCancelTool struct {
	onCancel func()
}

func (t *slowCancelTool) Name() string                                                             { return "slow" }
func (t *slowCancelTool) Aliases() []string                                                        { return nil }
func (t *slowCancelTool) Description(json.RawMessage) (string, error)                             { return "slow", nil }
func (t *slowCancelTool) InputSchema() json.RawMessage                                              { return json.RawMessage(`{}`) }
func (t *slowCancelTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	<-ctx.Done()
	t.onCancel()
	return nil, ctx.Err()
}
func (t *slowCancelTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult { return types.PermissionAllowDecision{} }
func (t *slowCancelTool) IsReadOnly(json.RawMessage) bool         { return false }
func (t *slowCancelTool) IsDestructive(json.RawMessage) bool       { return false }
func (t *slowCancelTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (t *slowCancelTool) IsEnabled() bool                          { return true }
func (t *slowCancelTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *slowCancelTool) Prompt() string                            { return "" }
func (t *slowCancelTool) RenderResult(any) string                   { return "" }

// neverRunTool never actually starts (context cancelled before execution).
type neverRunTool struct {
	onStart func()
}

func (t *neverRunTool) Name() string                                                             { return "never_run" }
func (t *neverRunTool) Aliases() []string                                                        { return nil }
func (t *neverRunTool) Description(json.RawMessage) (string, error)                             { return "never_run", nil }
func (t *neverRunTool) InputSchema() json.RawMessage                                              { return json.RawMessage(`{}`) }
func (t *neverRunTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	t.onStart()
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *neverRunTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult { return types.PermissionAllowDecision{} }
func (t *neverRunTool) IsReadOnly(json.RawMessage) bool         { return false }
func (t *neverRunTool) IsDestructive(json.RawMessage) bool       { return false }
func (t *neverRunTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (t *neverRunTool) IsEnabled() bool                          { return true }
func (t *neverRunTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *neverRunTool) Prompt() string                            { return "" }
func (t *neverRunTool) RenderResult(any) string                   { return "" }

// ---------------------------------------------------------------------------
// QueryLoop retry discards old executor (TS query.ts:734,913)
// RED TEST: Currently FAILS — queryLoop does not call Discard() on retry.
// ---------------------------------------------------------------------------

// discardSlowTool blocks until its context is cancelled.
type discardSlowTool struct {
	cancelled bool
	started   bool
	mu        sync.Mutex
}

func (t *discardSlowTool) Name() string { return "discard_slow" }
func (t *discardSlowTool) Aliases() []string { return nil }
func (t *discardSlowTool) Description(json.RawMessage) (string, error) { return "discard_slow", nil }
func (t *discardSlowTool) InputSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (t *discardSlowTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	t.mu.Lock()
	t.started = true
	t.mu.Unlock()
	<-ctx.Done()
	t.mu.Lock()
	t.cancelled = true
	t.mu.Unlock()
	return nil, ctx.Err()
}
func (t *discardSlowTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult { return types.PermissionAllowDecision{} }
func (t *discardSlowTool) IsReadOnly(json.RawMessage) bool         { return false }
func (t *discardSlowTool) IsDestructive(json.RawMessage) bool       { return false }
func (t *discardSlowTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (t *discardSlowTool) IsEnabled() bool                          { return true }
func (t *discardSlowTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *discardSlowTool) Prompt() string                            { return "" }
func (t *discardSlowTool) RenderResult(any) string                   { return "" }
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
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 3}},
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
// RED TEST: Currently FAILS — callLLM does not Discard() the executor on error.
func TestCallLLM_DiscardsExecutorOnStreamError(t *testing.T) {
	dt := &discardSlowTool{}
	p := &midStreamErrorProvider{}
	eng := New(&Params{
		Provider: p,
		Tools:    []tool.Tool{dt},
		Model:    "test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "test", nil)
	for range eventCh {
	}
	result := <-resultCh

	// The retryable error from call 1 should be retried, and call 2 should succeed
	if result.Error != nil {
		t.Fatalf("unexpected error after retry: %v", result.Error)
	}

	// Wait for the tool goroutine to notice cancellation
	time.Sleep(100 * time.Millisecond)

	// Verify tool goroutine was properly cleaned up:
	// - If tool.Call started: it must have been cancelled (ctx.Done fired)
	// - If tool.Call never started: executor aborted it via getAbortReason
	// Without the fix, a started tool would block forever (ctx never cancelled).
	if dt.WasStarted() && !dt.WasCancelled() {
		t.Error("tool started but was never cancelled — callLLM must Discard() executor on stream error")
	}
}

// ---------------------------------------------------------------------------
// executeTools skips non-tool-use blocks (line 398-399)
// ---------------------------------------------------------------------------

func TestExecuteTools_SkipsNonToolUseBlocks(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
	})

	// Mix of text and tool-use blocks — only tool-use blocks should be processed
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "thinking..."}, // should be skipped
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "unknown_tool", Input: json.RawMessage(`{}`)},
	}

	results := eng.executeTools(context.Background(), blocks, make(chan types.QueryEvent, 16))
	// Should have 1 result (the unknown tool error), not 2
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected error result for unknown tool")
	}
	if results[0].ToolUseID != "t1" {
		t.Errorf("expected ToolUseID 't1', got %q", results[0].ToolUseID)
	}
	// Verify the error message mentions the unknown tool name.
	var parsed map[string]string
	if err := json.Unmarshal(results[0].Content, &parsed); err != nil {
		t.Fatalf("failed to parse error content: %v", err)
	}
}

// executeTools_withStreamingTool is a tool that implements ToolWithStreaming.
// Used to cover the streaming execution path in executeTools.
type executeToolsStreamingTool struct {
	name     string
	streamFn func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error)
}

func (t *executeToolsStreamingTool) Name() string                        { return t.name }
func (t *executeToolsStreamingTool) Aliases() []string                   { return nil }
func (t *executeToolsStreamingTool) Description(json.RawMessage) (string, error) { return t.name, nil }
func (t *executeToolsStreamingTool) InputSchema() json.RawMessage        { return json.RawMessage(`{}`) }
func (t *executeToolsStreamingTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil // should never be called — streaming path used
}
func (t *executeToolsStreamingTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *executeToolsStreamingTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *executeToolsStreamingTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *executeToolsStreamingTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *executeToolsStreamingTool) IsEnabled() bool                       { return true }
func (t *executeToolsStreamingTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *executeToolsStreamingTool) Prompt() string                         { return "" }
func (t *executeToolsStreamingTool) RenderResult(any) string               { return "streamed result" }
func (t *executeToolsStreamingTool) ExecuteStream(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error) {
	if t.streamFn != nil {
		return t.streamFn(ctx, input, tctx, onProgress)
	}
	return &tool.ToolResult{Data: "streamed"}, nil
}

// executeTools_errorTool is a tool that returns an error from Call.
// Used to cover the non-streaming error path in executeTools.
type executeToolsErrorTool struct{ name string }

func (t *executeToolsErrorTool) Name() string                                                          { return t.name }
func (t *executeToolsErrorTool) Aliases() []string                                                     { return nil }
func (t *executeToolsErrorTool) Description(json.RawMessage) (string, error)                           { return t.name, nil }
func (t *executeToolsErrorTool) InputSchema() json.RawMessage                                          { return json.RawMessage(`{}`) }
func (t *executeToolsErrorTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return nil, errors.New("tool execution failed")
}
func (t *executeToolsErrorTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *executeToolsErrorTool) IsReadOnly(json.RawMessage) bool        { return false }
func (t *executeToolsErrorTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *executeToolsErrorTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *executeToolsErrorTool) IsEnabled() bool                         { return true }
func (t *executeToolsErrorTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *executeToolsErrorTool) Prompt() string                            { return "" }
func (t *executeToolsErrorTool) RenderResult(any) string                     { return "" }

func TestExecuteTools_StreamingTool(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		Tools: []tool.Tool{
			&executeToolsStreamingTool{
				name: "streamer",
				streamFn: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error) {
					onProgress(tool.ProgressUpdate{Lines: []string{"output line 1"}})
					return &tool.ToolResult{Data: "done"}, nil
				},
			},
		},
	})

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "streamer", Input: json.RawMessage(`{}`)},
	}

	eventCh := make(chan types.QueryEvent, 16)
	results := eng.executeTools(context.Background(), blocks, eventCh)
	close(eventCh)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Errorf("expected no error, got error")
	}
	if results[0].ToolUseID != "t1" {
		t.Errorf("ToolUseID = %q, want t1", results[0].ToolUseID)
	}

	// Verify streaming output delta was emitted
	var gotOutputDelta bool
	for evt := range eventCh {
		if evt.Type == types.EventToolOutputDelta && evt.ToolResult != nil && evt.ToolResult.DisplayOutput == "output line 1" {
			gotOutputDelta = true
		}
	}
	if !gotOutputDelta {
		t.Error("expected EventToolOutputDelta with 'output line 1'")
	}
}

func TestExecuteTools_StreamingToolError(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		Tools: []tool.Tool{
			&executeToolsStreamingTool{
				name: "streamer",
				streamFn: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error) {
					return nil, errors.New("stream failed")
				},
			},
		},
	})

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "streamer", Input: json.RawMessage(`{}`)},
	}

	eventCh := make(chan types.QueryEvent, 16)
	results := eng.executeTools(context.Background(), blocks, eventCh)
	close(eventCh)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected error result")
	}
	if results[0].ToolUseID != "t1" {
		t.Errorf("ToolUseID = %q, want t1", results[0].ToolUseID)
	}

	// Verify error was emitted
	var gotToolEnd bool
	for evt := range eventCh {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.IsError {
			gotToolEnd = true
		}
	}
	if !gotToolEnd {
		t.Error("expected EventToolEnd with IsError=true")
	}
}

func TestExecuteTools_CallError(t *testing.T) {
	t.Parallel()

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		Tools: []tool.Tool{&executeToolsErrorTool{name: "fail_tool"}},
	})

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "fail_tool", Input: json.RawMessage(`{}`)},
	}

	eventCh := make(chan types.QueryEvent, 16)
	results := eng.executeTools(context.Background(), blocks, eventCh)
	close(eventCh)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Error("expected error result from Call failure")
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
}

type testResponse struct {
	events  []llm.StreamEvent
	err     error
	channel chan llm.StreamEvent // if non-nil, return this channel directly
}

func (p *testProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *testProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

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

// testTool is a minimal tool for internal tests.
type testTool struct {
	name string
}

func (t *testTool) Name() string                                                          { return t.name }
func (t *testTool) Aliases() []string                                                     { return nil }
func (t *testTool) Description(json.RawMessage) (string, error)                           { return t.name, nil }
func (t *testTool) InputSchema() json.RawMessage                                          { return nil }
func (t *testTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *testTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *testTool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *testTool) IsDestructive(json.RawMessage) bool        { return false }
func (t *testTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *testTool) IsEnabled() bool                           { return true }
func (t *testTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *testTool) Prompt() string                            { return "" }
func (t *testTool) RenderResult(any) string                     { return "" }

// ---------------------------------------------------------------------------
// Sub-engine tests — source: plan steady-dreaming-sunrise.md
// ---------------------------------------------------------------------------

// subTextEvents creates streaming events for a simple text response (internal helper).
func subTextEvents(model, text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 10}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 5}},
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

	// Case 1: MaxTurns=0 → default 50
	sub1 := parent.NewSubEngine(SubEngineOptions{MaxTurns: 0})
	if sub1.maxTurns != 50 {
		t.Errorf("sub1.maxTurns = %d, want 50 (default)", sub1.maxTurns)
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

	ctx := context.Background()
	result := eng.QuerySync(ctx, "test query", nil)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", result.Terminal)
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

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result := eng.QuerySync(ctx, "test query", nil)
	if result.Terminal != types.TerminalAbortedStreaming {
		t.Errorf("expected TerminalAbortedStreaming, got %s", result.Terminal)
	}
	if result.Error == nil {
		t.Fatal("expected non-nil error from cancelled context")
	}
	if result.Error.Error() != "context canceled" {
		t.Errorf("error = %q, want %q", result.Error.Error(), "context canceled")
	}
}

func TestEmitEventNilSafe(t *testing.T) {
	// Sub-engine has dispatcher=nil. With nil eventCh, emitEvent should silently discard.
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test"})

	if eng.dispatcher != nil {
		t.Fatal("expected nil dispatcher for default engine")
	}
	// This should NOT panic — that's the entire assertion
	eng.emitEvent(nil, types.QueryEvent{Type: types.EventTurnStart})
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &llm.UsageDelta{OutputTokens: 99999}},
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

	// Create sub-engine via NewSubEngine (isSubagent=true, tokenBudget=0)
	subTools := map[string]tool.Tool{"test_tool": mt}
	sub := parent.NewSubEngine(SubEngineOptions{Tools: subTools})

	// Verify sub-engine is marked as subagent
	if !sub.isSubagent {
		t.Error("sub-engine should have isSubagent=true")
	}

	ctx := context.Background()
	result := sub.QuerySync(ctx, "test query", nil)

	// Should complete normally despite heavy token usage (subagent bypasses budget check)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Errorf("sub-agent should bypass budget check and complete, got terminal=%s", result.Terminal)
	}
}

func TestSubMaxTurns(t *testing.T) {
	t.Parallel()
	if subMaxTurns(0) != 50 {
		t.Errorf("subMaxTurns(0) = %d, want 50", subMaxTurns(0))
	}
	if subMaxTurns(-1) != 50 {
		t.Errorf("subMaxTurns(-1) = %d, want 50", subMaxTurns(-1))
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

	subEng := eng.NewSubEngine(SubEngineOptions{
		Tools:           map[string]tool.Tool{"test": &testTool{name: "test"}},
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
	})

	if subEng.dispatcher == nil {
		t.Fatal("sub-engine dispatcher should not be nil when parent has dispatcher")
	}

	// Emit an event through the sub-engine and verify it reaches the mock dispatcher
	subEng.emitEvent(nil, types.QueryEvent{
		Type: types.EventToolStart,
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
