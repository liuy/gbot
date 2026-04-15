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

