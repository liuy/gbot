package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// mockTool implements tool.Tool for testing.
type mockTool struct {
	name string
}

func (m *mockTool) Name() string                                { return m.name }
func (m *mockTool) Aliases() []string                           { return nil }
func (m *mockTool) Description(json.RawMessage) (string, error) { return "", nil }
func (m *mockTool) InputSchema() json.RawMessage                { return nil }
func (m *mockTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *mockTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *mockTool) IsReadOnly(json.RawMessage) bool           { return false }
func (m *mockTool) IsDestructive(json.RawMessage) bool        { return false }
func (m *mockTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (m *mockTool) IsEnabled() bool                           { return true }
func (m *mockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptBlock }
func (m *mockTool) Prompt() string                            { return "" }
func (m *mockTool) RenderResult(any) string                   { return "" }

func (m *mockTool) MaxResultSize() int { return 50000 }

func makeTestTools(names ...string) map[string]tool.Tool {
	m := make(map[string]tool.Tool, len(names))
	for _, n := range names {
		m[n] = &mockTool{name: n}
	}
	return m
}

// mockSubEngine implements SubagentEngine for testing.
type mockSubEngine struct {
	runFn    func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error)
	captured []AgentOpts
}

func (m *mockSubEngine) RunAgent(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
	m.captured = append(m.captured, opts)
	if m.runFn != nil {
		return m.runFn(ctx, opts)
	}
	return &types.SubQueryResult{AgentType: opts.AgentType, Content: "ok"}, nil
}

func TestAgentInputParsing(t *testing.T) {
	// Normal JSON
	input := `{"description":"search code","prompt":"find the Query method"}`
	var parsed types.AgentInput
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		t.Fatalf("failed to parse valid input: %v", err)
	}
	if parsed.Description != "search code" {
		t.Errorf("Description = %q, want %q", parsed.Description, "search code")
	}
	if parsed.Prompt != "find the Query method" {
		t.Errorf("Prompt = %q, want %q", parsed.Prompt, "find the Query method")
	}
}

func TestAgentInputMissingFields(t *testing.T) {
	// Missing prompt — JSON is valid but Prompt is empty
	badInput := `{"description":"no prompt"}`
	var parsed types.AgentInput
	if err := json.Unmarshal([]byte(badInput), &parsed); err != nil {
		t.Fatalf("unmarshal should not fail on missing optional fields: %v", err)
	}
	if parsed.Prompt != "" {
		t.Errorf("Prompt should be empty, got %q", parsed.Prompt)
	}
}
func TestCallEmptySubagentTypeDefaults(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{AgentType: "General", Content: "ok"}, nil
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})

	// Empty subagent_type → defaults to "General"
	input := json.RawMessage(`{"description":"test","prompt":"do it"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	_ = capturedOpts.AgentType // AgentType resolution moved to Engine.RunAgent
}
func TestCallFactoryError(t *testing.T) {
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return nil, fmt.Errorf("engine crashed: out of memory")
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})

	input := json.RawMessage(`{"description":"test","prompt":"do it"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error when factory returns error")
	}
	if !strings.Contains(err.Error(), "sub-agent execution failed") {
		t.Errorf("error should mention 'sub-agent execution failed', got: %v", err)
	}
}

func TestResultExtractionNormal(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("found it")}},
	}

	startTime := time.Now().Add(-1 * time.Second) // REAL-TIME: startTime for FinalizeResult
	result := FinalizeResult(messages, "General", startTime, types.Usage{InputTokens: 100, OutputTokens: 50}, 0)

	if result.Content != "found it" {
		t.Errorf("Content = %q, want %q", result.Content, "found it")
	}
	if result.AgentType != "General" {
		t.Errorf("AgentType = %q, want %q", result.AgentType, "General")
	}
	if result.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", result.TotalTokens)
	}
}

func TestResultExtractionFallback(t *testing.T) {
	// Last assistant has only tool_use (no text), previous has text
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("found something"),
			types.NewToolUseBlock("id1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"file content"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id2", "Grep", json.RawMessage(`{}`)),
		}},
	}

	startTime := time.Now().Add(-2 * time.Second) // REAL-TIME: startTime for FinalizeResult
	result := FinalizeResult(messages, "Explore", startTime, types.Usage{InputTokens: 200, OutputTokens: 100}, 2)

	// Should walk backward and find "found something" from the first assistant
	if result.Content != "found something" {
		t.Errorf("Content = %q, want %q (backward walk fallback)", result.Content, "found something")
	}
	if result.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", result.AgentType, "Explore")
	}
	if result.TotalToolUseCount != 2 {
		t.Errorf("TotalToolUseCount = %d, want 2", result.TotalToolUseCount)
	}
}

func TestResultExtractionNoText(t *testing.T) {
	// All messages have no text — pure tool_use scenario
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Grep", json.RawMessage(`{}`)),
		}},
	}

	startTime := time.Now() // REAL-TIME: startTime for FinalizeResult
	result := FinalizeResult(messages, "Plan", startTime, types.Usage{}, 1)

	if !strings.Contains(result.Content, "no text output") {
		t.Errorf("Content should mention 'no text output', got %q", result.Content)
	}
}

// Legacy: engine no longer places the interrupt marker on user messages;
// FinalizeResult's scan-all behavior is preserved for backward compatibility
// with persisted sessions created before the unified-interrupt refactor.
func TestResultExtractionInterruptOnUserMessage(t *testing.T) {
	// Sub-agent was interrupted — interrupt marker is on user message (tool_result).
	// FinalizeResult should detect it and return interrupted message, not "completed".
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"file contents"`), false),
			types.NewTextBlock(types.InterruptMessage),
		}},
	}

	startTime := time.Now() // REAL-TIME: startTime for FinalizeResult
	result := FinalizeResult(messages, "Plan", startTime, types.Usage{}, 1)

	if strings.Contains(result.Content, "completed") {
		t.Errorf("should not say 'completed' when interrupted, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "interrupted") {
		t.Errorf("Content should mention 'interrupted', got: %q", result.Content)
	}
}

func TestResultExtractionInterruptOnAssistantMessage(t *testing.T) {
	// Sub-agent interrupted mid-stream — interrupt on assistant message.
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("Let me check"),
			types.NewTextBlock(types.InterruptMessage),
		}},
	}

	startTime := time.Now() // REAL-TIME: startTime for FinalizeResult
	result := FinalizeResult(messages, "Explore", startTime, types.Usage{}, 0)

	// Should return the text content found ("Let me check[Request interrupted by user]")
	if !strings.Contains(result.Content, "Let me check") {
		t.Errorf("should contain text content, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, types.InterruptMessage) {
		t.Errorf("should contain interrupt marker, got: %q", result.Content)
	}
}

func TestCallNilFactory(t *testing.T) {
	at := New() // No SetFactory called

	input := json.RawMessage(`{"description":"test","prompt":"do something"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error when factory is nil")
	}
	if !strings.Contains(err.Error(), "agent tool not initialized") {
		t.Errorf("error should mention 'not initialized', got: %v", err)
	}
}

func TestCall_InvalidAgentType_FallsBackToEmpty(t *testing.T) {
	t.Parallel()
	mockEng := &mockSubEngine{}
	at := New()
	at.SetEngine(mockEng)

	input := json.RawMessage(`{"description":"test","prompt":"do","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned unexpected error: %v", err)
	}
	// Engine handles agent def; Call just passes the value through
}

// TestCallPassesToolUseID is a regression test: when the LLM invokes the
// Agent tool, the parent tool_use_id (from ToolUseContext.ToolUseID) must
// be forwarded to engine.RunAgent via AgentOpts.ParentToolUseID.
//
// Without it, NewSubEngine skips wiring the taggedDispatcher, and every
// sub-agent event (text_delta, tool_start, tool_end, ...) dispatched inside
// the sub-engine is dropped on the floor. Symptom: the Agent tool card in
// the TUI shows no progress while the sub-agent runs; only the final result
// appears. See taggedDispatcher guard in engine.NewSubEngine:
//
//	if e.dispatcher != nil && opts.ParentToolUseID != "" { ... }
//
// Fork path (callFork) already forwards this; the non-fork path used to
// forget it.
func TestCallPassesToolUseID(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{
			AgentType: "General",
			Content:   "done",
		}, nil
	}

	_ = makeTestTools("Bash", "Read")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})

	input := json.RawMessage(`{"description":"test","prompt":"do it"}`)
	tctx := &tool.ToolUseContext{
		ToolUseID: "call_abc123",
	}
	result, err := at.Call(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	// The critical assertion: factory must receive ParentToolUseID.
	// Without this, NewSubEngine's taggedDispatcher is never wired and
	// sub-agent events never reach the parent TUI tool card.
	if capturedOpts.ParentToolUseID != "call_abc123" {
		t.Errorf("AgentOpts.ParentToolUseID = %q, want %q (from ToolUseContext.ToolUseID)",
			capturedOpts.ParentToolUseID, "call_abc123")
	}
}
func TestInterfaceCompliance(t *testing.T) {
	// Verify AgentTool satisfies tool.Tool interface
	var _ tool.Tool = New()
}

func TestName(t *testing.T) {
	at := New()
	if got := at.Name(); got != "Agent" {
		t.Errorf("Name() = %q, want %q", got, "Agent")
	}
}

func TestAliases(t *testing.T) {
	at := New()
	if got := at.Aliases(); got != nil {
		t.Errorf("Aliases() = %v, want nil", got)
	}
}

func TestInputSchema(t *testing.T) {
	at := New()
	schema := at.InputSchema()
	if len(schema) == 0 {
		t.Fatal("InputSchema() returned empty")
	}
	// Verify it's valid JSON containing expected fields
	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema() missing properties")
	}
	if _, ok := props["description"]; !ok {
		t.Error("InputSchema() missing description property")
	}
	if _, ok := props["prompt"]; !ok {
		t.Error("InputSchema() missing prompt property")
	}
}

func TestPermissionMethods(t *testing.T) {
	at := New()
	input := json.RawMessage(`{}`)

	if got := at.CheckPermissions(input, nil); got.Behavior() != types.BehaviorAllow {
		t.Errorf("CheckPermissions() = %v, want allow", got)
	}
	if got := at.IsReadOnly(input); got != false {
		t.Errorf("IsReadOnly() = %v, want false", got)
	}
	if got := at.IsDestructive(input); got != false {
		t.Errorf("IsDestructive() = %v, want false", got)
	}
	if got := at.IsConcurrencySafe(input); got != true {
		t.Errorf("IsConcurrencySafe() = %v, want true", got)
	}
	if got := at.IsEnabled(); got != true {
		t.Errorf("IsEnabled() = %v, want true", got)
	}
	if got := at.InterruptBehavior(); got != tool.InterruptBlock {
		t.Errorf("InterruptBehavior() = %v, want InterruptBlock", got)
	}
	if got := at.MaxResultSize(); got != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", got)
	}
}

func TestSetSkillRegistry(t *testing.T) {
	at := New()
	reg := &testSkillRegistry{}
	at.SetSkillRegistry(reg)
	// SkillReg now flows through Engine.RunAgent via sharedDeps;
	// the runner no longer holds it. This just verifies no panic.
}

func TestSetMcpConnect(t *testing.T) {
	at := New()
	var fn McpConnectFunc = func(ctx context.Context, name string, specs []json.RawMessage) (*McpConnectResult, error) {
		return nil, nil
	}
	at.SetMcpConnect(fn)
	// McpConnect flows via Runner() → AgentOpts → Engine.RunAgent.
}

func TestJobAdapter_NilForkReg(t *testing.T) {
	at := New()
	if got := at.JobAdapter(); got != nil {
		t.Error("JobAdapter() should return nil when forkReg is nil")
	}
}

func TestJobAdapter_NonNilForkReg(t *testing.T) {
	at := New()
	at.SetNotifyFn(func(string) {}, func() string { return "" })
	if got := at.JobAdapter(); got == nil {
		t.Error("JobAdapter() should return non-nil when forkReg is set")
	}
}

func TestForkAgentJobAdapter_Prefix(t *testing.T) {
	reg := NewForkAgentRegistry()
	a := NewForkAgentJobAdapter(reg)
	if got := a.Prefix(); got != "fork-" {
		t.Errorf("Prefix() = %q, want %q", got, "fork-")
	}
}

type testSkillRegistry struct{}

func (t *testSkillRegistry) GetAllSkills() []types.SkillCommand { return nil }

func TestRenderResultNonSubQueryResult(t *testing.T) {
	at := New()
	// Pass a non-*SubQueryResult type — should fall through to json.Marshal
	result := at.RenderResult(map[string]string{"key": "value"})
	if !strings.Contains(result, "key") {
		t.Errorf("RenderResult for non-SubQueryResult should contain JSON, got %q", result)
	}
}

func TestRenderResult_JSONRawMessage(t *testing.T) {
	at := New()
	// Resume path: array-form wire wrapping a SubQueryResult.
	inner := `{"content":"Found 3 files matching the pattern","usage":{"input_tokens":100,"output_tokens":50}}`
	textBytes, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textBytes) + `}]`)
	v, err := at.DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	got := at.RenderResult(v)
	if !strings.Contains(got, "Found 3 files") {
		t.Errorf("RenderResult(decoded) should contain result content, got: %q", got)
	}
	// Must NOT show raw JSON keys like "usage"
	if strings.Contains(got, `"usage"`) {
		t.Errorf("RenderResult should not show raw JSON, got: %q", got)
	}
}

func TestAgent_DecodeResult_RejectsBareStruct(t *testing.T) {
	t.Parallel()

	at := New()
	_, err := at.DecodeResult(json.RawMessage(`{"content":"x"}`))
	if err == nil {
		t.Error("DecodeResult must reject bare struct form")
	}
}

func TestPrompt(t *testing.T) {
	at := New()
	prompt := at.Prompt()
	if prompt == "" {
		t.Fatal("Prompt() returned empty string")
	}
	// Without a Loader initialized, ListAgentDefinitions returns only the
	// hardcoded General/Explore agents. Bundled Planner/Executor/Reviewer
	// appear once the Loader is initialized (see loader_test.go).
	for _, name := range []string{"General", "Explore"} {
		if !strings.Contains(prompt, name) {
			t.Errorf("Prompt() should contain %q", name)
		}
	}
}

func TestFormatAgentLine(t *testing.T) {
	def := &types.AgentDefinition{
		AgentType:       "Test",
		WhenToUse:       "Test agent",
		Tools:           []string{"Read", "Bash"},
		DisallowedTools: nil,
	}
	line := formatAgentLine(def)
	if !strings.Contains(line, "Test") {
		t.Errorf("formatAgentLine should contain agent type, got %q", line)
	}
	if !strings.Contains(line, "Read, Bash") {
		t.Errorf("formatAgentLine should contain tools, got %q", line)
	}
}

func TestGetToolsDescription_AllowlistAndDenylist(t *testing.T) {
	// Both allowlist and denylist — effective tools = allowlist minus denylist
	def := &types.AgentDefinition{
		Tools:           []string{"Read", "Bash", "Grep"},
		DisallowedTools: []string{"Bash"},
	}
	got := getToolsDescription(def)
	if got != "Read, Grep" {
		t.Errorf("getToolsDescription(allow+deny) = %q, want %q", got, "Read, Grep")
	}
}

func TestGetToolsDescription_AllowlistOnly(t *testing.T) {
	def := &types.AgentDefinition{
		Tools:           []string{"Read", "Bash"},
		DisallowedTools: nil,
	}
	got := getToolsDescription(def)
	if got != "Read, Bash" {
		t.Errorf("getToolsDescription(allowlist only) = %q, want %q", got, "Read, Bash")
	}
}

func TestGetToolsDescription_DenylistOnly(t *testing.T) {
	def := &types.AgentDefinition{
		Tools:           nil,
		DisallowedTools: []string{"Edit", "Write"},
	}
	got := getToolsDescription(def)
	if got != "All tools except Edit, Write" {
		t.Errorf("getToolsDescription(denylist only) = %q, want %q", got, "All tools except Edit, Write")
	}
}

func TestGetToolsDescription_Neither(t *testing.T) {
	def := &types.AgentDefinition{
		Tools:           nil,
		DisallowedTools: nil,
	}
	got := getToolsDescription(def)
	if got != "All tools" {
		t.Errorf("getToolsDescription(neither) = %q, want %q", got, "All tools")
	}
}

func TestGetToolsDescription_AllowlistEmptyAfterDenylist(t *testing.T) {
	// All allowed tools are also disallowed → returns "None"
	def := &types.AgentDefinition{
		Tools:           []string{"Edit"},
		DisallowedTools: []string{"Edit"},
	}
	got := getToolsDescription(def)
	if got != "None" {
		t.Errorf("getToolsDescription(empty after deny) = %q, want %q", got, "None")
	}
}

func TestModelInheritResolvedToEmpty(t *testing.T) {
	// All built-in agents have Model="inherit", which must be resolved to ""
	// before passing to the factory. Otherwise NewSubEngine treats "inherit"
	// as a literal model name and passes it to the API.
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})

	// No model specified → agentDef.Model="inherit" → should resolve to ""
	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if capturedOpts.Model != "" {
		t.Errorf("Model = %q, want %q (inherit should resolve to empty for parent inheritance)", capturedOpts.Model, "")
	}
}

func TestModelExplicitOverride(t *testing.T) {
	// When user specifies an explicit model, it should pass through
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})

	input := json.RawMessage(`{"description":"test","prompt":"do it","model":"custom-model-v1"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if capturedOpts.Model != "custom-model-v1" {
		t.Errorf("Model = %q, want %q", capturedOpts.Model, "custom-model-v1")
	}
}

func TestFilterToolsForAgent_GlobalDisallowed(t *testing.T) {
	// Temporarily add a global disallowed tool
	orig := AllAgentDisallowedTools
	AllAgentDisallowedTools = map[string]bool{"Bash": true}
	defer func() { AllAgentDisallowedTools = orig }()

	allTools := makeTestTools("Read", "Bash", "Grep")
	def := &types.AgentDefinition{}
	filtered := FilterToolsForAgent(allTools, def)

	if _, ok := filtered["Bash"]; ok {
		t.Error("filtered should not contain globally disallowed tool Bash")
	}
	if _, ok := filtered["Read"]; !ok {
		t.Error("filtered should still contain Read")
	}
}

// ---------------------------------------------------------------------------
// Fork agent tests
// ---------------------------------------------------------------------------

func TestCallFork_LaunchesInBackground(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		mu.Lock()
		capturedOpts = opts
		mu.Unlock()
		return &types.SubQueryResult{Content: "fork done", AgentType: "fork"}, nil
	}

	_ = makeTestTools("Bash", "Read")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	input := json.RawMessage(`{"description":"bg task","prompt":"search code","fork":true,"run_in_background":true}`)
	result, err := at.Call(context.Background(), input, &tool.ToolUseContext{ToolUseID: "call_fork_1"})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	sqr, ok := result.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("result.Data = %T, want *SubQueryResult", result.Data)
	}
	if !sqr.AsyncLaunched {
		t.Error("AsyncLaunched should be true for fork agent")
	}
	if sqr.AgentType != "fork" {
		t.Errorf("AgentType = %q, want %q", sqr.AgentType, "fork")
	}
	if sqr.AgentID == "" {
		t.Error("AgentID should not be empty")
	}

	// Wait for fork agent to complete via registry
	at.forkReg.Wait(sqr.AgentID)

	// Verify factory received fork messages
	mu.Lock()
	opts := capturedOpts
	mu.Unlock()

	if len(opts.ForkMessages) == 0 {
		t.Error("factory should receive non-empty ForkMessages")
	}
	if opts.AgentType != "fork" {
		t.Errorf("AgentType = %q, want %q", opts.AgentType, "fork")
	}
	if opts.MaxTurns != 200 {
		t.Errorf("MaxTurns = %d, want 200", opts.MaxTurns)
	}
}

func TestCallFork_AgentTypeSubagentType(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		mu.Lock()
		capturedOpts = opts
		mu.Unlock()
		return &types.SubQueryResult{Content: "ok"}, nil
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	// subagent_type="Explore" should override default "fork"
	input := json.RawMessage(`{"description":"explore","prompt":"search","fork":true,"run_in_background":true,"subagent_type":"Explore"}`)
	result, _ := at.Call(context.Background(), input, &tool.ToolUseContext{ToolUseID: "call_exp"})
	sqr := result.Data.(*types.SubQueryResult)
	at.forkReg.Wait(sqr.AgentID)

	mu.Lock()
	opts := capturedOpts
	mu.Unlock()
	if opts.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", opts.AgentType, "Explore")
	}
}

func TestCallFork_AgentTypeName(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		mu.Lock()
		capturedOpts = opts
		mu.Unlock()
		return &types.SubQueryResult{Content: "ok"}, nil
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	// name does NOT override subagent_type — name is only for SendMessage addressing
	input := json.RawMessage(`{"description":"audit","prompt":"check","fork":true,"run_in_background":true,"subagent_type":"Explore","name":"ship-audit"}`)
	result, _ := at.Call(context.Background(), input, &tool.ToolUseContext{ToolUseID: "call_audit"})
	sqr := result.Data.(*types.SubQueryResult)
	at.forkReg.Wait(sqr.AgentID)

	mu.Lock()
	opts := capturedOpts
	mu.Unlock()
	if opts.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", opts.AgentType, "Explore")
	}
}

func TestCallFork_RecursiveGuard(t *testing.T) {
	t.Parallel()
	at := New()
	mockEng := &mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return &types.SubQueryResult{}, nil
	}}
	at.SetEngine(mockEng)
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	input := json.RawMessage(`{"description":"nested","prompt":"do it","fork":true,"run_in_background":true}`)

	// Simulate being inside a fork child (messages contain fork-boilerplate)
	tctx := &tool.ToolUseContext{
		ToolUseID: "call_nested",
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("<fork-boilerplate>STOP</fork-boilerplate>")}},
		},
	}

	_, err := at.Call(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("expected error for recursive fork")
	}
	if !strings.Contains(err.Error(), "cannot spawn agents from within a fork agent") {
		t.Errorf("error = %q, want mention of recursive fork", err.Error())
	}
}

func TestCallFork_NotificationDelivered(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var notifications []string

	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return &types.SubQueryResult{
			Content:         "search complete",
			TotalDurationMs: 500,
			TotalTokens:     1000,
		}, nil
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	at.SetNotifyFn(
		func(xml string) {
			mu.Lock()
			defer mu.Unlock()
			notifications = append(notifications, xml)
		},
		func() string { return "system prompt" },
	)

	input := json.RawMessage(`{"description":"bg search","prompt":"find TODOs","fork":true,"run_in_background":true}`)
	result, _ := at.Call(context.Background(), input, &tool.ToolUseContext{ToolUseID: "call_notif"})

	// Wait for fork to complete via registry
	sqr := result.Data.(*types.SubQueryResult)
	at.forkReg.Wait(sqr.AgentID)

	mu.Lock()
	defer mu.Unlock()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if !strings.Contains(notifications[0], "<job-notification>") {
		t.Errorf("notification should contain <job-notification>, got %q", notifications[0])
	}
	if !strings.Contains(notifications[0], "search complete") {
		t.Errorf("notification should contain result content, got %q", notifications[0])
	}
}

func TestSetNotifyFn_EnablesFork(t *testing.T) {
	t.Parallel()
	at := New()
	if at.forkReg != nil {
		t.Error("forkReg should be nil before SetNotifyFn")
	}
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })
	if at.forkReg == nil {
		t.Error("forkReg should be non-nil after SetNotifyFn")
	}
}

func TestCallFork_NoForkWithoutSetNotifyFn(t *testing.T) {
	t.Parallel()
	at := New()
	mockEng := &mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return &types.SubQueryResult{}, nil
	}}
	at.SetEngine(mockEng)
	// SetNotifyFn NOT called — fork not enabled

	input := json.RawMessage(`{"description":"bg","prompt":"do it","fork":true,"run_in_background":true}`)
	_, err := at.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error when fork=true but SetNotifyFn not called")
	}
	if !strings.Contains(err.Error(), "fork mode is not available") {
		t.Errorf("error = %q, want mention of fork mode not available", err.Error())
	}
}

// ---------------------------------------------------------------------------
// FormatWireBlocks tests
// ---------------------------------------------------------------------------

func TestFormatWireBlocks_OneShotExplore(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentType:         "Explore",
		Content:           "found 3 files",
		TotalDurationMs:   500,
		TotalTokens:       1000,
		TotalToolUseCount: 2,
	}
	blocks := at.FormatWireBlocks(result)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	if blocks[0].Text != "found 3 files" {
		t.Errorf("one-shot Explore should return only content, got %q", blocks[0].Text)
	}
}

func TestFormatWireBlocks_OneShotPlan(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentType:         "Plan",
		Content:           "implementation plan",
		TotalDurationMs:   800,
		TotalTokens:       2000,
		TotalToolUseCount: 3,
	}
	blocks := at.FormatWireBlocks(result)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	if blocks[0].Text != "implementation plan" {
		t.Errorf("one-shot Plan should return only content, got %q", blocks[0].Text)
	}
}

func TestFormatWireBlocks_GeneralAgent(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentType:         "General",
		Content:           "task done",
		TotalDurationMs:   1000,
		TotalTokens:       5000,
		TotalToolUseCount: 5,
	}
	blocks := at.FormatWireBlocks(result)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	if !strings.Contains(blocks[0].Text, "task done") {
		t.Errorf("should contain content, got %q", blocks[0].Text)
	}
	if !strings.Contains(blocks[0].Text, "<usage>") {
		t.Errorf("General agent should include usage trailer, got %q", blocks[0].Text)
	}
	if strings.Contains(blocks[0].Text, "agentId:") {
		t.Errorf("General without AgentID should not have agentId hint, got %q", blocks[0].Text)
	}
}

func TestFormatWireBlocks_ForkWithAgentID(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentID:           "fork-1",
		AgentType:         "General",
		Content:           "completed",
		TotalDurationMs:   2000,
		TotalTokens:       3000,
		TotalToolUseCount: 1,
	}
	blocks := at.FormatWireBlocks(result)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, `agentId: fork-1`) {
		t.Errorf("should contain agentId hint, got %q", blocks[0].Text)
	}
	if !strings.Contains(blocks[0].Text, "<usage>") {
		t.Errorf("should include usage trailer, got %q", blocks[0].Text)
	}
}

func TestFormatWireBlocks_AsyncLaunched(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentID:       "fork-2",
		AgentType:     "fork",
		Content:       `Fork agent "fork-2" launched in background`,
		AsyncLaunched: true,
	}
	blocks := at.FormatWireBlocks(result)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Text != `Fork agent "fork-2" launched in background` {
		t.Errorf("async-launched should return only content, got %q", blocks[0].Text)
	}
}

func TestFormatWireBlocks_OneShotWithAgentID(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentID:           "fork-3",
		AgentType:         "Explore",
		Content:           "search results",
		TotalDurationMs:   300,
		TotalTokens:       500,
		TotalToolUseCount: 1,
	}
	blocks := at.FormatWireBlocks(result)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	// One-shot WITH AgentID should NOT skip trailer (has agentId hint + usage)
	if !strings.Contains(blocks[0].Text, `agentId: fork-3`) {
		t.Errorf("one-shot with AgentID should have agentId hint, got %q", blocks[0].Text)
	}
	if !strings.Contains(blocks[0].Text, "<usage>") {
		t.Errorf("one-shot with AgentID should have usage trailer, got %q", blocks[0].Text)
	}
}

func TestFormatWireBlocks_NonSubQueryResult(t *testing.T) {
	at := New()
	blocks := at.FormatWireBlocks("plain string")
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	// Should fallback to JSON marshaling
	if !strings.Contains(blocks[0].Text, "plain string") {
		t.Errorf("non-SubQueryResult should be JSON-marshaled, got %q", blocks[0].Text)
	}
}

// ---------------------------------------------------------------------------
// Step 4: User context injection + gitStatus system prompt tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Step 6: Skill preloading integration tests
// ---------------------------------------------------------------------------

func TestCall_SkillPreloading_Integration(t *testing.T) {
	// Test the full skill loading pipeline with types.SkillCommand directly.
	// File loading is handled by skills.Registry, tested in pkg/skills/.
	// (Call() integration tested separately via TestCall_SkillPreloading_EmptySkills)
	allSkills := []types.SkillCommand{
		{Name: "commit", Content: "# Commit Skill\nCreate atomic commits"},
		{Name: "review", Content: "# Review Skill\nReview code quality"},
	}

	resolved := ResolveSkillNames([]string{"commit", "review"}, allSkills, "General")
	if len(resolved) != 2 {
		t.Fatalf("ResolveSkillNames should resolve 2, got %d", len(resolved))
	}

	msgs := BuildSkillMessages(resolved)
	if len(msgs) != 2 {
		t.Fatalf("BuildSkillMessages should produce 2 messages, got %d", len(msgs))
	}

	// Verify message structure
	for i, msg := range msgs {
		if msg.Role != types.RoleUser {
			t.Errorf("msg[%d].Role = %q, want %q", i, msg.Role, types.RoleUser)
		}
		text := msg.Content[0].Text
		if !strings.Contains(text, "<command-message>") {
			t.Errorf("msg[%d] should contain command-message tag", i)
		}
		if !strings.Contains(text, "<skill-format>true</skill-format>") {
			t.Errorf("msg[%d] should contain skill-format tag", i)
		}
	}
}

func TestCall_SkillPreloading_EmptySkills(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	at.SetWorkingDir(t.TempDir())

	// General agent has no Skills defined — no skill messages
	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// UserContextMessages should be exactly 1 (currentDate only, no claudeMd set)
	for _, msg := range capturedOpts.UserContextMessages {
		if strings.Contains(msg.Content[0].Text, "<command-message>") {
			t.Error("no skill messages expected when agent has no Skills defined")
		}
	}
}

func TestEnhanceSystemPrompt_FallbackOnEmpty(t *testing.T) {
	result := EnhanceSystemPrompt("", nil, "/tmp", false, "")
	if !strings.Contains(result, defaultAgentPrompt) {
		t.Error("expected defaultAgentPrompt fallback when basePrompt is empty")
	}
}

func TestEnhanceSystemPrompt_UsesCustomPrompt(t *testing.T) {
	result := EnhanceSystemPrompt("Custom agent prompt", nil, "/tmp", false, "")
	if !strings.Contains(result, "Custom agent prompt") {
		t.Error("expected custom prompt to be used")
	}
	if strings.Contains(result, defaultAgentPrompt) {
		t.Error("default prompt should NOT appear when custom is provided")
	}
}

func TestEnhanceSystemPrompt_ContainsNotes(t *testing.T) {
	result := EnhanceSystemPrompt("test", nil, "/tmp", false, "")
	if !strings.Contains(result, "absolute file paths") {
		t.Error("expected notes about absolute paths")
	}
	if !strings.Contains(result, "avoid using emojis") {
		t.Error("expected notes about emojis")
	}
	if !strings.Contains(result, "Do not use a colon before tool calls") {
		t.Error("expected notes about colons")
	}
}

func TestEnhanceSystemPrompt_ContainsEnvBlock(t *testing.T) {
	result := EnhanceSystemPrompt("test", nil, "/home/user/project", true, "sonnet")
	if !strings.Contains(result, "<env>") {
		t.Error("expected <env> block")
	}
	if !strings.Contains(result, "Working directory: /home/user/project") {
		t.Error("expected working directory in env block")
	}
	if !strings.Contains(result, "Is directory a git repo: Yes") {
		t.Error("expected isGit=Yes")
	}
	if !strings.Contains(result, "You are powered by the model sonnet") {
		t.Error("expected model name")
	}
}

func TestEnhanceSystemPrompt_NotGitRepo(t *testing.T) {
	result := EnhanceSystemPrompt("test", nil, "/tmp", false, "")
	if !strings.Contains(result, "Is directory a git repo: No") {
		t.Error("expected isGit=No")
	}
}

func TestEnhanceSystemPrompt_NoModel(t *testing.T) {
	result := EnhanceSystemPrompt("test", nil, "/tmp", false, "")
	if strings.Contains(result, "You are powered by the model") {
		t.Error("model line should not appear when model is empty")
	}
}

func TestEnhanceSystemPrompt_ToolNames(t *testing.T) {
	tools := map[string]tool.Tool{
		"Grep": &mockTool{name: "Grep"},
		"Read": &mockTool{name: "Read"},
		"Bash": &mockTool{name: "Bash"},
	}
	result := EnhanceSystemPrompt("test", tools, "/tmp", false, "")
	if !strings.Contains(result, "Enabled tools:") {
		t.Error("expected Enabled tools section")
	}
	if !strings.Contains(result, "- Bash") {
		t.Error("expected Bash in tool list")
	}
	if !strings.Contains(result, "- Grep") {
		t.Error("expected Grep in tool list")
	}
	if !strings.Contains(result, "- Read") {
		t.Error("expected Read in tool list")
	}
}

func TestFormatToolNamesList_Empty(t *testing.T) {
	if got := formatToolNamesList(nil); got != "" {
		t.Errorf("expected empty string for nil tools, got %q", got)
	}
}

func TestFormatToolNamesList_Sorted(t *testing.T) {
	tools := map[string]tool.Tool{
		"Zebra":  &mockTool{name: "Zebra"},
		"Alpha":  &mockTool{name: "Alpha"},
		"Middle": &mockTool{name: "Middle"},
	}
	got := formatToolNamesList(tools)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "- Alpha" {
		t.Errorf("expected first tool Alpha, got %q", lines[0])
	}
	if lines[1] != "- Middle" {
		t.Errorf("expected second tool Middle, got %q", lines[1])
	}
	if lines[2] != "- Zebra" {
		t.Errorf("expected third tool Zebra, got %q", lines[2])
	}
}

func TestExtractPartialResult_LastAssistantWithText(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("I found the issue")}},
	}
	got := ExtractPartialResult(messages)
	if got != "I found the issue" {
		t.Errorf("expected %q, got %q", "I found the issue", got)
	}
}

func TestExtractPartialResult_MultipleAssistants(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("first")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("second")}},
	}
	got := ExtractPartialResult(messages)
	if got != "second" {
		t.Errorf("expected last assistant text %q, got %q", "second", got)
	}
}

func TestExtractPartialResult_OnlyToolUseSkipsToEarlier(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("earlier text"),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", nil),
		}},
	}
	got := ExtractPartialResult(messages)
	if got != "earlier text" {
		t.Errorf("expected %q (skipped tool_use-only assistant), got %q", "earlier text", got)
	}
}

func TestExtractPartialResult_EmptySlice(t *testing.T) {
	got := ExtractPartialResult(nil)
	if got != "" {
		t.Errorf("expected empty string for nil slice, got %q", got)
	}
	got = ExtractPartialResult([]types.Message{})
	if got != "" {
		t.Errorf("expected empty string for empty slice, got %q", got)
	}
}

func TestExtractPartialResult_AllNonAssistant(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("sys")}},
	}
	got := ExtractPartialResult(messages)
	if got != "" {
		t.Errorf("expected empty string (no assistant messages), got %q", got)
	}
}

func TestExtractPartialResult_EmptyTextBlockSkipped(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: ""},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("actual content"),
		}},
	}
	got := ExtractPartialResult(messages)
	if got != "actual content" {
		t.Errorf("expected %q (skipped empty text), got %q", "actual content", got)
	}
}

func TestExtractPartialResult_MultipleTextBlocksJoined(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("part one"),
			types.NewToolUseBlock("id1", "Read", nil),
			types.NewTextBlock("part two"),
		}},
	}
	got := ExtractPartialResult(messages)
	want := "part one\npart two"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// ---------------------------------------------------------------------------
// CountToolUses
// ---------------------------------------------------------------------------

func TestCountToolUses_EmptySlice(t *testing.T) {
	if got, want := CountToolUses(nil), 0; got != want {
		t.Fatalf("nil: got %d, want %d", got, want)
	}
	if got, want := CountToolUses([]types.Message{}), 0; got != want {
		t.Fatalf("empty: got %d, want %d", got, want)
	}
}

func TestCountToolUses_OnlyUserMessages(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("there")}},
	}
	got := CountToolUses(messages)
	if got, want := got, 0; got != want {
		t.Fatalf("got %d, want %d (no assistant messages)", got, want)
	}
}

func TestCountToolUses_MultipleAssistants(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("1", "Read", nil),
			types.NewToolUseBlock("2", "Grep", nil),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("3", "Bash", nil),
		}},
	}
	if got := CountToolUses(messages); got != 3 {
		t.Errorf("expected 3 tool_use blocks, got %d", got)
	}
}

func TestCountToolUses_MixedTextAndToolUse(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("let me check"),
			types.NewToolUseBlock("1", "Read", nil),
			types.NewTextBlock("now searching"),
			types.NewToolUseBlock("2", "Grep", nil),
		}},
	}
	if got := CountToolUses(messages); got != 2 {
		t.Errorf("expected 2 (text blocks ignored), got %d", got)
	}
}

func TestCountToolUses_AssistantWithNoToolUse(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("just text, no tools"),
		}},
	}
	if got := CountToolUses(messages); got != 0 {
		t.Errorf("expected 0 (no tool_use), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// GetLastToolUseName
// ---------------------------------------------------------------------------

func TestGetLastToolUseName_NonAssistant(t *testing.T) {
	msg := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}
	if got := GetLastToolUseName(msg); got != "" {
		t.Errorf("expected empty string for non-assistant, got %q", got)
	}
}

func TestGetLastToolUseName_AssistantNoToolUse(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewTextBlock("just text"),
	}}
	if got := GetLastToolUseName(msg); got != "" {
		t.Errorf("expected empty string (no tool_use), got %q", got)
	}
}

func TestGetLastToolUseName_MultipleToolUses(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewToolUseBlock("1", "Read", nil),
		types.NewTextBlock("checking"),
		types.NewToolUseBlock("2", "Grep", nil),
	}}
	got := GetLastToolUseName(msg)
	if got != "Grep" {
		t.Errorf("expected last tool_use name %q, got %q", "Grep", got)
	}
}

func TestGetLastToolUseName_TextAndToolUse(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewTextBlock("let me read"),
		types.NewToolUseBlock("1", "Read", nil),
	}}
	got := GetLastToolUseName(msg)
	if got != "Read" {
		t.Errorf("expected %q, got %q", "Read", got)
	}
}

func TestGetLastToolUseName_EmptyContent(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{}}
	if got := GetLastToolUseName(msg); got != "" {
		t.Errorf("expected empty string for empty content, got %q", got)
	}
}

func TestGetLastToolUseName_ToolUseAtStart(t *testing.T) {
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		types.NewToolUseBlock("1", "Bash", nil),
		types.NewTextBlock("done"),
	}}
	got := GetLastToolUseName(msg)
	if got != "Bash" {
		t.Errorf("expected %q (only tool_use, found via backward walk), got %q", "Bash", got)
	}
}

func TestBuildEnvBlock_ContainsShellAndModel(t *testing.T) {
	env := buildEnvBlock("/tmp", false, "test-model")
	if !strings.Contains(env, "/tmp") {
		t.Errorf("env block should contain working dir, got: %q", env)
	}
	if !strings.Contains(env, "test-model") {
		t.Errorf("env block should contain model, got: %q", env)
	}
	if !strings.Contains(env, "Shell:") {
		t.Errorf("env block should contain Shell line, got: %q", env)
	}
}

func TestForkSync_InheritsContext(t *testing.T) {
	// fork=true + run_in_background=false → sync fork, inherits parent context
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{
			AgentType: "fork",
			Content:   "found 3 files",
		}, nil
	}

	_ = makeTestTools("Bash", "Read", "Grep")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	at.SetNotifyFn(func(string) {}, func() string { return "parent system prompt" })

	assistantMsg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewTextBlock("I'll search"),
			types.NewToolUseBlock("call_1", "Agent", json.RawMessage(`{}`)),
		},
	}
	tctx := &tool.ToolUseContext{
		ToolUseID: "call_1",
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
			assistantMsg,
		},
	}

	input := json.RawMessage(`{"description":"fork sync search","prompt":"find all test files","fork":true}`)
	result, err := at.Call(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	sqr, ok := result.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("result.Data should be *SubQueryResult, got %T", result.Data)
	}
	if sqr.AsyncLaunched {
		t.Error("result should NOT have AsyncLaunched for sync fork")
	}
	if sqr.Content != "found 3 files" {
		t.Errorf("Content = %q, want %q", sqr.Content, "found 3 files")
	}

	// Verify fork messages were built (not the fresh agent path)
	if len(capturedOpts.ForkMessages) == 0 {
		t.Error("factory should receive ForkMessages for fork path")
	}
	// Verify parent system prompt was inherited
	if capturedOpts.SystemPrompt != "parent system prompt" {
		t.Errorf("SystemPrompt = %q, want parent system prompt", capturedOpts.SystemPrompt)
	}
	// Tools are resolved by Engine.RunAgent, not passed through AgentOpts in fork path.
	_ = capturedOpts.Tools
}

func TestForkFalse_BackgroundTrue_NoForkReg(t *testing.T) {
	// fork=false + run_in_background=true without forkReg → falls through to normal sync path
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return &types.SubQueryResult{
			AgentType: "General",
			Content:   "done",
		}, nil
	}

	_ = makeTestTools("Bash", "Read")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	// No SetNotifyFn → forkReg is nil

	input := json.RawMessage(`{"description":"bg without fork","prompt":"do something","run_in_background":true}`)
	result, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	sqr, ok := result.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("result.Data should be *SubQueryResult, got %T", result.Data)
	}
	// Falls through to sync path (no fork, no registry for background)
	if sqr.AsyncLaunched {
		t.Error("should not be async — no forkReg to manage background agents")
	}
	if sqr.Content != "done" {
		t.Errorf("Content = %q, want %q", sqr.Content, "done")
	}
}

func TestForkTrue_NoForkReg_ReturnsError(t *testing.T) {
	// fork=true but forkReg is nil → error
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return nil, fmt.Errorf("should not be called")
	}

	_ = makeTestTools("Bash")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: factory})
	// No SetNotifyFn → forkReg is nil

	input := json.RawMessage(`{"description":"fork no reg","prompt":"test","fork":true}`)
	_, err := at.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error when fork=true but forkReg is nil")
	}
	if !strings.Contains(err.Error(), "fork mode is not available") {
		t.Errorf("error = %q, want mention of fork mode not available", err.Error())
	}
}
