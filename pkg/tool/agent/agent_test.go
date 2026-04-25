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

	ctxbuild "github.com/liuy/gbot/pkg/context"
)

// mockTool implements tool.Tool for testing.
type mockTool struct {
	name string
}

func (m *mockTool) Name() string                                                    { return m.name }
func (m *mockTool) Aliases() []string                                               { return nil }
func (m *mockTool) Description(json.RawMessage) (string, error)                     { return "", nil }
func (m *mockTool) InputSchema() json.RawMessage                                    { return nil }
func (m *mockTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *mockTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *mockTool) IsReadOnly(json.RawMessage) bool      { return false }
func (m *mockTool) IsDestructive(json.RawMessage) bool   { return false }
func (m *mockTool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (m *mockTool) IsEnabled() bool                      { return true }
func (m *mockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptBlock }
func (m *mockTool) Prompt() string                       { return "" }
func (m *mockTool) RenderResult(any) string              { return "" }

func (m *mockTool) MaxResultSize() int { return 50000 }

func makeTestTools(names ...string) map[string]tool.Tool {
	m := make(map[string]tool.Tool, len(names))
	for _, n := range names {
		m[n] = &mockTool{name: n}
	}
	return m
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

func TestCallWithMockFactory(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{
			AgentType: "General",
			Content:   "found 3 files",
		}, nil
	}

	parentTools := makeTestTools("Bash", "Read", "Grep")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })

	input := json.RawMessage(`{"description":"search","prompt":"find Query method","subagent_type":"General"}`)
	result, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	sqr, ok := result.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("result.Data should be *SubQueryResult, got %T", result.Data)
	}
	if sqr.Content != "found 3 files" {
		t.Errorf("Content = %q, want %q", sqr.Content, "found 3 files")
	}

	// Verify factory received correct params
	if capturedOpts.Prompt != "find Query method" {
		t.Errorf("factory received Prompt = %q, want %q", capturedOpts.Prompt, "find Query method")
	}
	if len(capturedOpts.Tools) != 3 {
		t.Errorf("factory received %d tools, want 3", len(capturedOpts.Tools))
	}
}

func TestCallEmptySubagentTypeDefaults(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{AgentType: "General", Content: "ok"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })

	// Empty subagent_type → defaults to "General"
	input := json.RawMessage(`{"description":"test","prompt":"do it"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if capturedOpts.AgentType != "General" {
		t.Errorf("AgentType = %q, want %q", capturedOpts.AgentType, "General")
	}
}

func TestCallFactoryError(t *testing.T) {
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return nil, fmt.Errorf("engine crashed: out of memory")
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })

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

	startTime := time.Now().Add(-1 * time.Second)
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

	startTime := time.Now().Add(-2 * time.Second)
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

	startTime := time.Now()
	result := FinalizeResult(messages, "Plan", startTime, types.Usage{}, 1)

	if !strings.Contains(result.Content, "no text output") {
		t.Errorf("Content should mention 'no text output', got %q", result.Content)
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

func TestCallWithInvalidAgentType(t *testing.T) {
	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(
		func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{}, nil
		},
		func() map[string]tool.Tool { return parentTools },
	)

	input := json.RawMessage(`{"description":"test","prompt":"do","subagent_type":"nonexistent"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for unknown agent type")
	}
	if !strings.Contains(err.Error(), "unknown agent type") {
		t.Errorf("error should mention 'unknown agent type', got: %v", err)
	}
}

func TestDescriptionFromInput(t *testing.T) {
	at := New()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with description", `{"description":"search code","prompt":"find"}`, "search code"},
		{"no description, short prompt", `{"prompt":"find the bug"}`, "find the bug"},
		{"invalid json", `{broken`, "Execute a sub-agent task"},
		{"empty input", `{}`, "Execute a sub-agent task"},
		{"no description, long prompt truncation", `{"prompt":"Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor xy"}`, "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor x..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := at.Description(json.RawMessage(tt.input))
			if err != nil {
				t.Fatalf("Description returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Description() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderResult(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentType: "General",
		Content:   "found 3 files matching the query",
	}
	rendered := at.RenderResult(result)
	if !strings.Contains(rendered, "found 3 files") {
		t.Errorf("RenderResult should contain content, got %q", rendered)
	}
}

// TestCallPassesToolUseID verifies that AgentTool.Call propagates the
// ToolUseContext.ToolUseID to the AgentOpts.ParentToolUseID.
// This is required for the TUI to display sub-agent tool progress.
// TestCallFork_DetachedContext verifies that fork agents use a detached context
// (context.Background), NOT the parent query's context. When the parent query's
// context is cancelled (e.g., by ReplState.FinishStream on normal completion),
// the fork agent must survive and complete its work.
//
// Regression: callFork previously passed the parent's siblingCtx to Spawn,
// which derived childCtx from it. FinishStream cancelled the query context,
// cascading to siblingCtx → childCtx → fork agent's API call → "context canceled".
func TestCallFork_DetachedContext(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())

	var factoryCtx context.Context
	var factoryMu sync.Mutex
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		factoryMu.Lock()
		factoryCtx = ctx
		factoryMu.Unlock()

		// Simulate work that outlives parent cancellation
		time.Sleep(300 * time.Millisecond)

		// The fork agent's context must NOT be cancelled
		if ctx.Err() != nil {
			return nil, fmt.Errorf("fork agent context cancelled: %w", ctx.Err())
		}
		return &types.SubQueryResult{Content: "survived", AgentType: "fork"}, nil
	}

	parentTools := makeTestTools("Bash", "Read", "Grep")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetNotifyFn(func(string) {}, func() json.RawMessage { return nil })

	// Messages needed for fork agent (trigger assistant + context history)
	assistantMsg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewTextBlock("I'll search in background"),
			types.NewToolUseBlock("call_fork_1", "Agent", json.RawMessage(`{}`)),
		},
	}
	tctx := &types.ToolUseContext{
		ToolUseID: "call_fork_1",
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("search")}},
			assistantMsg,
		},
	}

	input := json.RawMessage(`{"description":"bg search","prompt":"find all test files","subagent_type":"Explore","run_in_background":true}`)
	result, err := at.Call(parentCtx, input, tctx)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	sqr, ok := result.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("result.Data should be *SubQueryResult, got %T", result.Data)
	}
	if !sqr.AsyncLaunched {
		t.Fatal("result should have AsyncLaunched=true for fork agents")
	}

	// Cancel the parent context — simulates FinishStream on normal query completion.
	// The fork agent is running in a goroutine; this MUST NOT kill it.
	parentCancel()

	// Wait for the fork agent to finish by checking the registry
	final, found := at.forkReg.Wait(sqr.AgentID)
	if !found {
		t.Fatal("fork agent not found in registry")
	}

	if final.Status != ForkCompleted {
		t.Errorf("fork agent Status = %q, want %q (parent cancel should NOT kill fork)", final.Status, ForkCompleted)
	}
	if final.Result == nil || final.Result.Content != "survived" {
		t.Errorf("fork agent Result = %v, want Content=%q", final.Result, "survived")
	}

	// Double-check: the factory received a context that survived parent cancellation
	factoryMu.Lock()
	defer factoryMu.Unlock()
	if factoryCtx == nil {
		t.Fatal("factory was never called")
	}
	if factoryCtx.Err() != nil {
		t.Errorf("factory context err = %v, want nil (detached from parent)", factoryCtx.Err())
	}
}

func TestCallPassesToolUseID(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{
			AgentType: "General",
			Content:   "done",
		}, nil
	}

	parentTools := makeTestTools("Bash", "Read")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })

	input := json.RawMessage(`{"description":"test","prompt":"do it"}`)
	tctx := &types.ToolUseContext{
		ToolUseID: "call_abc123",
	}
	result, err := at.Call(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	// The critical assertion: factory must receive ParentToolUseID
	if capturedOpts.ParentToolUseID != "call_abc123" {
		t.Errorf("ParentToolUseID = %q, want %q", capturedOpts.ParentToolUseID, "call_abc123")
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
	if got := at.IsConcurrencySafe(input); got != false {
		t.Errorf("IsConcurrencySafe() = %v, want false", got)
	}
	if got := at.IsEnabled(); got != true {
		t.Errorf("IsEnabled() = %v, want true", got)
	}
	if got := at.InterruptBehavior(); got != tool.InterruptBlock {
		t.Errorf("InterruptBehavior() = %v, want InterruptBlock", got)
	}
}

func TestRenderResultNonSubQueryResult(t *testing.T) {
	at := New()
	// Pass a non-*SubQueryResult type — should fall through to json.Marshal
	result := at.RenderResult(map[string]string{"key": "value"})
	if !strings.Contains(result, "key") {
		t.Errorf("RenderResult for non-SubQueryResult should contain JSON, got %q", result)
	}
}

func TestPrompt(t *testing.T) {
	at := New()
	prompt := at.Prompt()
	if prompt == "" {
		t.Fatal("Prompt() returned empty string")
	}
	// Verify it contains agent type names
	for _, name := range []string{"General", "Explore", "Plan"} {
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

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })

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

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })

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

	parentTools := makeTestTools("Bash", "Read")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetNotifyFn(func(xml string) {}, func() json.RawMessage { return nil })

	input := json.RawMessage(`{"description":"bg task","prompt":"search code","run_in_background":true}`)
	result, err := at.Call(context.Background(), input, &types.ToolUseContext{ToolUseID: "call_fork_1"})
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

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetNotifyFn(func(xml string) {}, func() json.RawMessage { return nil })

	// subagent_type="Explore" should override default "fork"
	input := json.RawMessage(`{"description":"explore","prompt":"search","run_in_background":true,"subagent_type":"Explore"}`)
	result, _ := at.Call(context.Background(), input, &types.ToolUseContext{ToolUseID: "call_exp"})
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

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetNotifyFn(func(xml string) {}, func() json.RawMessage { return nil })

	// name does NOT override subagent_type — name is only for SendMessage addressing
	input := json.RawMessage(`{"description":"audit","prompt":"check","run_in_background":true,"subagent_type":"Explore","name":"ship-audit"}`)
	result, _ := at.Call(context.Background(), input, &types.ToolUseContext{ToolUseID: "call_audit"})
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
	at.SetFactory(
		func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{}, nil
		},
		func() map[string]tool.Tool { return makeTestTools("Bash") },
	)
	at.SetNotifyFn(func(xml string) {}, func() json.RawMessage { return nil })

	input := json.RawMessage(`{"description":"nested","prompt":"do it","run_in_background":true}`)

	// Simulate being inside a fork child (messages contain fork-boilerplate)
	tctx := &types.ToolUseContext{
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

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetNotifyFn(
		func(xml string) {
			mu.Lock()
			defer mu.Unlock()
			notifications = append(notifications, xml)
		},
		func() json.RawMessage { return json.RawMessage(`"system prompt"`) },
	)

	input := json.RawMessage(`{"description":"bg search","prompt":"find TODOs","run_in_background":true}`)
	result, _ := at.Call(context.Background(), input, &types.ToolUseContext{ToolUseID: "call_notif"})

	// Wait for fork to complete via registry
	sqr := result.Data.(*types.SubQueryResult)
	at.forkReg.Wait(sqr.AgentID)

	mu.Lock()
	defer mu.Unlock()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if !strings.Contains(notifications[0], "<task-notification>") {
		t.Errorf("notification should contain <task-notification>, got %q", notifications[0])
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
	at.SetNotifyFn(func(xml string) {}, func() json.RawMessage { return nil })
	if at.forkReg == nil {
		t.Error("forkReg should be non-nil after SetNotifyFn")
	}
}

func TestCallFork_NoForkWithoutSetNotifyFn(t *testing.T) {
	t.Parallel()
	at := New()
	at.SetFactory(
		func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "sync done"}, nil
		},
		func() map[string]tool.Tool { return makeTestTools("Bash") },
	)
	// SetNotifyFn NOT called — fork not enabled

	input := json.RawMessage(`{"description":"bg","prompt":"do it","run_in_background":true}`)
	result, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	sqr := result.Data.(*types.SubQueryResult)
	// Without SetNotifyFn, run_in_background is ignored — runs synchronously
	if sqr.AsyncLaunched {
		t.Error("should not launch async without SetNotifyFn")
	}
	if sqr.Content != "sync done" {
		t.Errorf("Content = %q, want sync result", sqr.Content)
	}
}

// ---------------------------------------------------------------------------
// FormatWireResult tests
// ---------------------------------------------------------------------------

func TestFormatWireResult_OneShotExplore(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentType:         "Explore",
		Content:           "found 3 files",
		TotalDurationMs:   500,
		TotalTokens:       1000,
		TotalToolUseCount: 2,
	}
	got := at.FormatWireResult(result)
	if got != "found 3 files" {
		t.Errorf("one-shot Explore should return only content, got %q", got)
	}
}

func TestFormatWireResult_OneShotPlan(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentType:         "Plan",
		Content:           "implementation plan",
		TotalDurationMs:   800,
		TotalTokens:       2000,
		TotalToolUseCount: 3,
	}
	got := at.FormatWireResult(result)
	if got != "implementation plan" {
		t.Errorf("one-shot Plan should return only content, got %q", got)
	}
}

func TestFormatWireResult_GeneralAgent(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentType:         "General",
		Content:           "task done",
		TotalDurationMs:   1000,
		TotalTokens:       5000,
		TotalToolUseCount: 5,
	}
	got := at.FormatWireResult(result)
	if !strings.Contains(got, "task done") {
		t.Errorf("should contain content, got %q", got)
	}
	if !strings.Contains(got, "<usage>") {
		t.Errorf("General agent should include usage trailer, got %q", got)
	}
	if strings.Contains(got, "agentId:") {
		t.Errorf("General without AgentID should not have agentId hint, got %q", got)
	}
}

func TestFormatWireResult_ForkWithAgentID(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentID:           "fork-1",
		AgentType:         "General",
		Content:           "completed",
		TotalDurationMs:   2000,
		TotalTokens:       3000,
		TotalToolUseCount: 1,
	}
	got := at.FormatWireResult(result)
	if !strings.Contains(got, `agentId: fork-1`) {
		t.Errorf("should contain agentId hint, got %q", got)
	}
	if !strings.Contains(got, "<usage>") {
		t.Errorf("should include usage trailer, got %q", got)
	}
}

func TestFormatWireResult_AsyncLaunched(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentID:       "fork-2",
		AgentType:     "fork",
		Content:       `Fork agent "fork-2" launched in background`,
		AsyncLaunched: true,
	}
	got := at.FormatWireResult(result)
	if got != `Fork agent "fork-2" launched in background` {
		t.Errorf("async-launched should return only content, got %q", got)
	}
}

func TestFormatWireResult_OneShotWithAgentID(t *testing.T) {
	at := New()
	result := &types.SubQueryResult{
		AgentID:           "fork-3",
		AgentType:         "Explore",
		Content:           "search results",
		TotalDurationMs:   300,
		TotalTokens:       500,
		TotalToolUseCount: 1,
	}
	got := at.FormatWireResult(result)
	// One-shot WITH AgentID should NOT skip trailer (has agentId hint + usage)
	if !strings.Contains(got, `agentId: fork-3`) {
		t.Errorf("one-shot with AgentID should have agentId hint, got %q", got)
	}
	if !strings.Contains(got, "<usage>") {
		t.Errorf("one-shot with AgentID should have usage trailer, got %q", got)
	}
}

func TestFormatWireResult_NonSubQueryResult(t *testing.T) {
	at := New()
	got := at.FormatWireResult("plain string")
	// Should fallback to JSON marshaling
	if !strings.Contains(got, "plain string") {
		t.Errorf("non-SubQueryResult should be JSON-marshaled, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Step 4: User context injection + gitStatus system prompt tests
// ---------------------------------------------------------------------------

func TestCall_UserContextMessages_CurrentDate(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")

	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// UserContextMessages must contain currentDate
	if len(capturedOpts.UserContextMessages) < 1 {
		t.Fatalf("expected at least 1 UserContextMessage, got %d", len(capturedOpts.UserContextMessages))
	}
	firstMsg := capturedOpts.UserContextMessages[0]
	if firstMsg.Role != types.RoleUser {
		t.Errorf("first UserContextMessage Role = %q, want %q", firstMsg.Role, types.RoleUser)
	}
	if len(firstMsg.Content) == 0 {
		t.Fatal("first UserContextMessage has no content")
	}
	if !strings.Contains(firstMsg.Content[0].Text, "Today's date is") {
		t.Errorf("first UserContextMessage should contain currentDate, got %q", firstMsg.Content[0].Text)
	}
}

func TestCall_UserContextMessages_ClaudeMd(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	at.SetGBOTMDContent("# My Project\nBuild with make")

	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// General agent should have 2 UserContextMessages: currentDate + claudeMd
	if len(capturedOpts.UserContextMessages) != 2 {
		t.Fatalf("expected 2 UserContextMessages, got %d", len(capturedOpts.UserContextMessages))
	}
	secondMsg := capturedOpts.UserContextMessages[1]
	if !strings.Contains(secondMsg.Content[0].Text, "My Project") {
		t.Errorf("second UserContextMessage should contain claudeMd content, got %q", secondMsg.Content[0].Text)
	}
}

func TestCall_UserContextMessages_ExploreOmitsClaudeMd(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	at.SetGBOTMDContent("# Project rules")

	input := json.RawMessage(`{"description":"test","prompt":"search","subagent_type":"Explore"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// Explore should omit claudeMd — only currentDate
	if len(capturedOpts.UserContextMessages) != 1 {
		t.Fatalf("Explore should have 1 UserContextMessage (currentDate only), got %d", len(capturedOpts.UserContextMessages))
	}
	if strings.Contains(capturedOpts.UserContextMessages[0].Content[0].Text, "Project rules") {
		t.Error("Explore should NOT receive claudeMd content")
	}
}

func TestCall_UserContextMessages_PlanOmitsClaudeMd(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	at.SetGBOTMDContent("# Project rules")

	input := json.RawMessage(`{"description":"test","prompt":"plan","subagent_type":"Plan"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// Plan should omit claudeMd — only currentDate
	if len(capturedOpts.UserContextMessages) != 1 {
		t.Fatalf("Plan should have 1 UserContextMessage (currentDate only), got %d", len(capturedOpts.UserContextMessages))
	}
}

func TestCall_UserContextMessages_EmptyClaudeMd(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	// No SetGBOTMDContent — empty by default

	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// Only currentDate — no claudeMd message for empty content
	if len(capturedOpts.UserContextMessages) != 1 {
		t.Fatalf("expected 1 UserContextMessage (currentDate only), got %d", len(capturedOpts.UserContextMessages))
	}
}

func TestCall_GitStatusAppendedToSystemPrompt(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	at.SetGitStatus(&ctxbuild.GitStatusInfo{IsGit: true, Branch: "feature-branch", DefaultBranch: "main", IsDirty: true})

	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// System prompt should contain gitStatus for General agent
	sp := string(capturedOpts.SystemPrompt)
	if !strings.Contains(sp, "Git branch: feature-branch") {
		t.Errorf("system prompt should contain git branch, got: %s", sp)
	}
	if !strings.Contains(sp, "Default branch: main") {
		t.Errorf("system prompt should contain default branch, got: %s", sp)
	}
	if !strings.Contains(sp, "dirty") {
		t.Errorf("system prompt should contain dirty status, got: %s", sp)
	}
}

func TestCall_GitStatus_OmittedForExplore(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	at.SetGitStatus(&ctxbuild.GitStatusInfo{IsGit: true, Branch: "feature-branch"})

	input := json.RawMessage(`{"description":"test","prompt":"search","subagent_type":"Explore"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// System prompt should NOT contain gitStatus for Explore
	sp := string(capturedOpts.SystemPrompt)
	if strings.Contains(sp, "Git branch: feature-branch") {
		t.Errorf("Explore system prompt should NOT contain git status, got: %s", sp)
	}
}

func TestCall_GitStatus_OmittedForPlan(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	at.SetGitStatus(&ctxbuild.GitStatusInfo{IsGit: true, Branch: "feature-branch"})

	input := json.RawMessage(`{"description":"test","prompt":"plan","subagent_type":"Plan"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	sp := string(capturedOpts.SystemPrompt)
	if strings.Contains(sp, "Git branch: feature-branch") {
		t.Errorf("Plan system prompt should NOT contain git status, got: %s", sp)
	}
}

func TestCall_NilGitStatus_NoAppend(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	// No SetGitStatus — nil by default

	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	sp := string(capturedOpts.SystemPrompt)
	if strings.Contains(sp, "Git branch:") {
		t.Errorf("nil gitStatus should not append git section, got: %s", sp)
	}
	// But env block should say "Is directory a git repo: No"
	if !strings.Contains(sp, "Is directory a git repo: No") {
		t.Errorf("env block should say No for nil gitStatus, got: %s", sp)
	}
}

func TestCall_UserContextMessages_Ordering(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/tmp")
	at.SetGBOTMDContent("# CLAUDE.md content")

	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// Verify ordering: currentDate first, claudeMd second
	if len(capturedOpts.UserContextMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(capturedOpts.UserContextMessages))
	}
	if !strings.Contains(capturedOpts.UserContextMessages[0].Content[0].Text, "Today's date is") {
		t.Error("first message should be currentDate")
	}
	if !strings.Contains(capturedOpts.UserContextMessages[1].Content[0].Text, "CLAUDE.md") {
		t.Error("second message should be claudeMd")
	}
}

func TestCall_EnhancedSystemPrompt_ContainsEnvBlock(t *testing.T) {
	var capturedOpts AgentOpts
	factory := func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
	at.SetWorkingDir("/home/user/project")
	at.SetGitStatus(&ctxbuild.GitStatusInfo{IsGit: true, Branch: "main"})

	input := json.RawMessage(`{"description":"test","prompt":"do it","subagent_type":"General"}`)
	_, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	// Unmarshal system prompt from json.RawMessage to get actual string content.
	// json.Marshal escapes < and > to \u003c/\u003e for HTML safety.
	var sp string
	if err := json.Unmarshal(capturedOpts.SystemPrompt, &sp); err != nil {
		t.Fatalf("failed to unmarshal system prompt: %v", err)
	}
	if !strings.Contains(sp, "<env>") {
		t.Error("system prompt should contain <env> block")
	}
	if !strings.Contains(sp, "Working directory: /home/user/project") {
		t.Error("system prompt should contain working directory")
	}
	if !strings.Contains(sp, "Is directory a git repo: Yes") {
		t.Error("system prompt should say isGit=Yes")
	}
	if !strings.Contains(sp, "Enabled tools:") {
		t.Error("system prompt should contain enabled tools")
	}
	if !strings.Contains(sp, "avoid using emojis") {
		t.Error("system prompt should contain agent notes")
	}
	// Git status appended for General agent
	if !strings.Contains(sp, "Git branch: main") {
		t.Error("system prompt should contain git branch for General agent")
	}
}

func TestFormatGitStatusForSystemPrompt(t *testing.T) {
	tests := []struct {
		name string
		gs   *ctxbuild.GitStatusInfo
		want []string // substrings that must appear
		skip []string // substrings that must NOT appear
	}{
		{
			name: "clean repo",
			gs:   &ctxbuild.GitStatusInfo{IsGit: true, Branch: "main", DefaultBranch: "main", IsDirty: false},
			want: []string{"Git branch: main", "Default branch: main", "clean"},
		},
		{
			name: "dirty repo",
			gs:   &ctxbuild.GitStatusInfo{IsGit: true, Branch: "feat", DefaultBranch: "", IsDirty: true},
			want: []string{"Git branch: feat", "dirty (uncommitted changes)"},
			skip: []string{"Default branch:"},
		},
		{
			name: "non-git",
			gs:   &ctxbuild.GitStatusInfo{IsGit: false},
			want: []string{},
			skip: []string{"Git branch:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatGitStatusForSystemPrompt(tt.gs)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("expected %q in result, got %q", w, got)
				}
			}
			for _, s := range tt.skip {
				if strings.Contains(got, s) {
					t.Errorf("should NOT contain %q, got %q", s, got)
				}
			}
		})
	}
}


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

	parentTools := makeTestTools("Bash")
	at := New()
	at.SetFactory(factory, func() map[string]tool.Tool { return parentTools })
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
	result := enhanceSystemPrompt("", nil, "/tmp", false, "")
	if !strings.Contains(result, defaultAgentPrompt) {
		t.Error("expected defaultAgentPrompt fallback when basePrompt is empty")
	}
}

func TestEnhanceSystemPrompt_UsesCustomPrompt(t *testing.T) {
	result := enhanceSystemPrompt("Custom agent prompt", nil, "/tmp", false, "")
	if !strings.Contains(result, "Custom agent prompt") {
		t.Error("expected custom prompt to be used")
	}
	if strings.Contains(result, defaultAgentPrompt) {
		t.Error("default prompt should NOT appear when custom is provided")
	}
}

func TestEnhanceSystemPrompt_ContainsNotes(t *testing.T) {
	result := enhanceSystemPrompt("test", nil, "/tmp", false, "")
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
	result := enhanceSystemPrompt("test", nil, "/home/user/project", true, "sonnet")
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
	result := enhanceSystemPrompt("test", nil, "/tmp", false, "")
	if !strings.Contains(result, "Is directory a git repo: No") {
		t.Error("expected isGit=No")
	}
}

func TestEnhanceSystemPrompt_NoModel(t *testing.T) {
	result := enhanceSystemPrompt("test", nil, "/tmp", false, "")
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
	result := enhanceSystemPrompt("test", tools, "/tmp", false, "")
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
