package engine

import (
	"context"
	"encoding/json"
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
	if !strings.Contains(parsed["error"], "unknown_tool") {
		t.Errorf("expected error to contain 'unknown_tool', got %q", parsed["error"])
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
