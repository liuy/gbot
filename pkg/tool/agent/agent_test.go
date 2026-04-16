package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
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
	var capturedOpts SubEngineOpts
	factory := func(ctx context.Context, opts SubEngineOpts) (*types.SubQueryResult, error) {
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
		func(ctx context.Context, opts SubEngineOpts) (*types.SubQueryResult, error) {
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
		{"empty input", `{}`, "Execute a sub-agent task"},
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
// ToolUseContext.ToolUseID to the SubEngineOpts.ParentToolUseID.
// This is required for the TUI to display sub-agent tool progress.
func TestCallPassesToolUseID(t *testing.T) {
	var capturedOpts SubEngineOpts
	factory := func(ctx context.Context, opts SubEngineOpts) (*types.SubQueryResult, error) {
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
