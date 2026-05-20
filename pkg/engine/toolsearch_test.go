package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Test helpers — tool construction for ToolSearch tests
// ---------------------------------------------------------------------------

// tsStubTool creates a simple tool with the given name (not deferred).
// Named tsStubTool to avoid collision with stubTool in mcp_tool_test.go.
func tsStubTool(name string) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_: name,
		InputSchema_: func() json.RawMessage {
			return json.RawMessage(`{"type":"object"}`)
		},
		Description_: func(json.RawMessage) (string, error) {
			return name + " tool", nil
		},
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: name + " result"}, nil
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
	})
}

// deferredStubTool creates a deferred tool (ShouldDefer_=true).
func deferredStubTool(name string) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        name,
		ShouldDefer_: true,
		SearchHint_:  "search hint for " + name,
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
		Description_: func(json.RawMessage) (string, error) { return name + " deferred tool", nil },
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: name}, nil
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
	})
}

// deferredMCPTool creates a deferred MCP tool (AlwaysLoad=false -> IsDeferred=true).
func deferredMCPTool(name string) tool.Tool {
	info := mcp.DiscoveredTool{
		Name:         name,
		OriginalName: strings.TrimPrefix(name, "mcp__server__"),
		ServerName:   "server",
		Description:  name + " MCP tool",
		// AlwaysLoad defaults to false -> IsDeferred returns true
	}
	return NewMCPTool(info, nil)
}

// alwaysLoadMCPTool creates a non-deferred MCP tool (AlwaysLoad=true -> IsDeferred=false).
func alwaysLoadMCPTool(name string) tool.Tool {
	info := mcp.DiscoveredTool{
		Name:         name,
		OriginalName: strings.TrimPrefix(name, "mcp__server__"),
		ServerName:   "server",
		Description:  name + " MCP tool",
		AlwaysLoad:   true,
	}
	return NewMCPTool(info, nil)
}

// tsBuildToolMap creates a map[string]tool.Tool from a slice of tools.
func tsBuildToolMap(tools ...tool.Tool) map[string]tool.Tool {
	m := make(map[string]tool.Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return m
}

// tsToolOrder returns sorted keys of a tool map.
func tsToolOrder(m map[string]tool.Tool) []string {
	order := make([]string, 0, len(m))
	for name := range m {
		order = append(order, name)
	}
	slices.Sort(order)
	return order
}

// ---------------------------------------------------------------------------
// Tests — toolSearchState
// ---------------------------------------------------------------------------

func TestToolSearchState_DiscoverTools(t *testing.T) {
	s := newToolSearchState()

	// Initially nothing discovered
	if s.IsDiscovered("Read") {
		t.Fatal("expected Read to not be discovered initially")
	}

	// Discover some tools
	s.DiscoverTools([]string{"Read", "Edit"})

	if !s.IsDiscovered("Read") {
		t.Fatal("expected Read to be discovered after DiscoverTools")
	}
	if !s.IsDiscovered("Edit") {
		t.Fatal("expected Edit to be discovered after DiscoverTools")
	}
	if s.IsDiscovered("Grep") {
		t.Fatal("expected Grep to NOT be discovered")
	}

	// Discover more
	s.DiscoverTools([]string{"Grep"})
	if !s.IsDiscovered("Grep") {
		t.Fatal("expected Grep to be discovered after second call")
	}

	// DiscoveredNames returns sorted list
	names := s.DiscoveredNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 discovered names, got %d", len(names))
	}
	// Verify sorted order
	if names[0] != "Edit" || names[1] != "Grep" || names[2] != "Read" {
		t.Errorf("expected sorted [Edit, Grep, Read], got %v", names)
	}
}

func TestToolSearchState_Empty(t *testing.T) {
	s := newToolSearchState()
	if len(s.DiscoveredNames()) != 0 {
		t.Fatal("expected empty discovered names for new state")
	}
}

// ---------------------------------------------------------------------------
// Tests — FilterToolsForRequest
// ---------------------------------------------------------------------------

func TestFilterToolsForRequest_NoDeferred(t *testing.T) {
	// 0 deferred tools — no filtering needed
	tools := tsBuildToolMap(
		tsStubTool("Read"),
		tsStubTool("Edit"),
	)
	state := newToolSearchState()
	order := tsToolOrder(tools)

	active, deferredNames, activated := FilterToolsForRequest(tools, state, order)

	if activated {
		t.Fatal("expected filtering NOT activated when no deferred tools")
	}
	if len(deferredNames) != 0 {
		t.Fatalf("expected no deferred names when no deferred tools, got %v", deferredNames)
	}
	// All tools should be active
	if len(active) != 2 {
		t.Fatalf("expected 2 active tools (all), got %d", len(active))
	}
}

func TestFilterToolsForRequest_WithDeferred(t *testing.T) {
	// Any deferred tools activate filtering
	tools := tsBuildToolMap(
		tsStubTool("Read"),
		tsStubTool("Edit"),
		deferredStubTool("DeferredA"),
		deferredStubTool("DeferredB"),
		deferredStubTool("DeferredC"),
	)
	state := newToolSearchState()
	order := tsToolOrder(tools)

	active, deferredNames, activated := FilterToolsForRequest(tools, state, order)

	if !activated {
		t.Fatal("expected filtering activated when deferred tools exist")
	}
	// Non-deferred tools should be active
	activeToolNames := tsExtractToolNames(active)
	if !tsContainsStr(activeToolNames, "Read") {
		t.Error("expected Read in active tools")
	}
	if !tsContainsStr(activeToolNames, "Edit") {
		t.Error("expected Edit in active tools")
	}
	// Deferred tools should NOT be active (none discovered yet)
	if tsContainsStr(activeToolNames, "DeferredA") {
		t.Error("expected DeferredA to NOT be active (undiscovered)")
	}
	// Deferred names should list all 3
	if len(deferredNames) != 3 {
		t.Fatalf("expected 3 deferred names, got %d: %v", len(deferredNames), deferredNames)
	}
}

func TestFilterToolsForRequest_WithDiscovered(t *testing.T) {
	tools := tsBuildToolMap(
		tsStubTool("Read"),
		deferredStubTool("DeferredA"),
		deferredStubTool("DeferredB"),
		deferredStubTool("DeferredC"),
	)
	state := newToolSearchState()
	state.DiscoverTools([]string{"DeferredA"}) // discover one

	order := tsToolOrder(tools)
	active, deferredNames, activated := FilterToolsForRequest(tools, state, order)

	if !activated {
		t.Fatal("expected filtering activated")
	}

	activeToolNames := tsExtractToolNames(active)
	if !tsContainsStr(activeToolNames, "DeferredA") {
		t.Error("expected discovered DeferredA to be active")
	}
	if tsContainsStr(activeToolNames, "DeferredB") {
		t.Error("expected undiscovered DeferredB to NOT be active")
	}
	if tsContainsStr(activeToolNames, "DeferredC") {
		t.Error("expected undiscovered DeferredC to NOT be active")
	}

	// Only undiscovered deferred tools in deferred list
	if len(deferredNames) != 2 {
		t.Fatalf("expected 2 deferred names (DeferredB, DeferredC), got %d: %v", len(deferredNames), deferredNames)
	}
}

func TestFilterToolsForRequest_MCPDeferredTools(t *testing.T) {
	tools := tsBuildToolMap(
		tsStubTool("Read"),
		deferredMCPTool("mcp__server__tool1"),
		deferredMCPTool("mcp__server__tool2"),
		deferredMCPTool("mcp__server__tool3"),
	)
	state := newToolSearchState()
	state.DiscoverTools([]string{"mcp__server__tool1"})

	order := tsToolOrder(tools)
	active, _, activated := FilterToolsForRequest(tools, state, order)

	if !activated {
		t.Fatal("expected filtering activated with MCP tools")
	}

	activeToolNames := tsExtractToolNames(active)
	if !tsContainsStr(activeToolNames, "mcp__server__tool1") {
		t.Error("expected discovered mcp__server__tool1 to be active")
	}
	if tsContainsStr(activeToolNames, "mcp__server__tool2") {
		t.Error("expected undiscovered mcp__server__tool2 to NOT be active")
	}
}

func TestFilterToolsForRequest_AlwaysLoadMCPNotDeferred(t *testing.T) {
	// AlwaysLoad=true MCP tool should NOT be deferred
	tools := tsBuildToolMap(
		tsStubTool("Read"),
		alwaysLoadMCPTool("mcp__server__important"),
		deferredStubTool("DeferredA"),
		deferredStubTool("DeferredB"),
		deferredStubTool("DeferredC"),
	)
	state := newToolSearchState()
	order := tsToolOrder(tools)

	active, _, activated := FilterToolsForRequest(tools, state, order)

	if !activated {
		t.Fatal("expected filtering activated")
	}

	activeToolNames := tsExtractToolNames(active)
	if !tsContainsStr(activeToolNames, "mcp__server__important") {
		t.Error("expected AlwaysLoad MCP tool to always be active")
	}
	if !tsContainsStr(activeToolNames, "Read") {
		t.Error("expected Read to be active")
	}
}

// ---------------------------------------------------------------------------
// Tests — DeferredToolsAnnouncement
// ---------------------------------------------------------------------------

func TestDeferredToolsAnnouncement(t *testing.T) {
	tools := []tool.Tool{deferredStubTool("DeferredA"), deferredStubTool("DeferredB"), deferredStubTool("mcp__server__tool1")}
	result := DeferredToolsAnnouncement(tools)

	if !strings.Contains(result, "<available-deferred-tools>") {
		t.Error("expected opening tag")
	}
	if !strings.Contains(result, "</available-deferred-tools>") {
		t.Error("expected closing tag")
	}
	if !strings.Contains(result, "DeferredA") {
		t.Error("expected DeferredA in announcement")
	}
	if !strings.Contains(result, "mcp__server__tool1") {
		t.Error("expected mcp__server__tool1 in announcement")
	}
	// Verify hints are included
	if !strings.Contains(result, "search hint for DeferredA") {
		t.Error("expected search hint for DeferredA")
	}
}

func TestDeferredToolsAnnouncement_MCPToolFallbackToDescription(t *testing.T) {
	// MCP tool without SearchHint but with Description should show Description.
	mcpNoHint := deferredMCPTool("mcp__server__fetch") // has Description but no SearchHint
	result := DeferredToolsAnnouncement([]tool.Tool{mcpNoHint})

	if !strings.Contains(result, "mcp__server__fetch:") {
		t.Error("expected tool name with colon separator for description fallback")
	}
	if !strings.Contains(result, "mcp__server__fetch MCP tool") {
		t.Errorf("expected Description as fallback, got:\n%s", result)
	}
}

func TestDeferredToolsAnnouncement_SearchHintPreferred(t *testing.T) {
	// When both SearchHint and Description exist, SearchHint should win.
	builtWithHint := deferredStubTool("DeferredA")
	mcpNoHint := deferredMCPTool("mcp__server__tool1")
	result := DeferredToolsAnnouncement([]tool.Tool{builtWithHint, mcpNoHint})

	if !strings.Contains(result, "search hint for DeferredA") {
		t.Error("expected SearchHint for built-in tool")
	}
	if !strings.Contains(result, "mcp__server__tool1 MCP tool") {
		t.Errorf("expected Description fallback for MCP tool, got:\n%s", result)
	}
}

func TestDeferredToolsAnnouncement_Empty(t *testing.T) {
	result := DeferredToolsAnnouncement(nil)
	if result != "" {
		t.Errorf("expected empty announcement for nil, got %q", result)
	}

	result = DeferredToolsAnnouncement([]tool.Tool{})
	if result != "" {
		t.Errorf("expected empty announcement for empty slice, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Tests — ExtractDiscoveredToolNamesFromResult
// ---------------------------------------------------------------------------

func TestExtractDiscoveredToolNamesFromResult_Nil(t *testing.T) {
	names := ExtractDiscoveredToolNamesFromResult(nil)
	if names != nil {
		t.Errorf("expected nil for nil input, got %v", names)
	}
}

func TestExtractDiscoveredToolNamesFromResult_FunctionBlocks(t *testing.T) {
	// Simulate ToolSearch result with <function> blocks
	data := `<functions>
<function>{"description": "List tasks", "name": "DeferredA", "parameters": {"type": "object"}}</function>
<function>{"description": "Update task", "name": "DeferredB", "parameters": {"type": "object"}}</function>
</functions>`

	names := ExtractDiscoveredToolNamesFromResult(data)
	if len(names) != 2 {
		t.Fatalf("expected 2 tool names, got %d: %v", len(names), names)
	}
	if names[0] != "DeferredA" {
		t.Errorf("expected first name to be DeferredA, got %s", names[0])
	}
	if names[1] != "DeferredB" {
		t.Errorf("expected second name to be DeferredB, got %s", names[1])
	}
}

func TestExtractDiscoveredToolNamesFromResult_JSONStringWrapper(t *testing.T) {
	// JSON string wrapper around function blocks
	inner := `<function>{"name": "Grep", "description": "Search"}</function>`
	wrapped, _ := json.Marshal(inner) // becomes "\"<function>...\""

	names := ExtractDiscoveredToolNamesFromResult(string(wrapped))
	if len(names) != 1 {
		t.Fatalf("expected 1 tool name, got %d: %v", len(names), names)
	}
	if names[0] != "Grep" {
		t.Errorf("expected Grep, got %s", names[0])
	}
}

func TestExtractDiscoveredToolNamesFromResult_MapWithTools(t *testing.T) {
	data := map[string]any{
		"tools": []any{
			map[string]any{"name": "DeferredA"},
			map[string]any{"name": "DeferredB"},
		},
	}
	names := ExtractDiscoveredToolNamesFromResult(data)
	if len(names) != 2 {
		t.Fatalf("expected 2 tool names, got %d: %v", len(names), names)
	}
}

func TestExtractDiscoveredToolNamesFromResult_MapWithNames(t *testing.T) {
	data := map[string]any{
		"names": []any{"Tool1", "Tool2"},
	}
	names := ExtractDiscoveredToolNamesFromResult(data)
	if len(names) != 2 {
		t.Fatalf("expected 2 tool names, got %d: %v", len(names), names)
	}
}

// ---------------------------------------------------------------------------
// Tests — RestoreToolSearchState
// ---------------------------------------------------------------------------

func TestRestoreToolSearchState_CompactBoundary(t *testing.T) {
	state := newToolSearchState()

	// Simulate a compact boundary message with preCompactDiscoveredTools
	boundaryContent := `{"subtype":"compact_boundary","preCompactDiscoveredTools":["DeferredA","DeferredB"]}`
	messages := []types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.ContentBlock{
				types.NewTextBlock(boundaryContent),
			},
		},
	}

	RestoreToolSearchState(messages, state)

	if !state.IsDiscovered("DeferredA") {
		t.Error("expected DeferredA to be discovered from compact boundary")
	}
	if !state.IsDiscovered("DeferredB") {
		t.Error("expected DeferredB to be discovered from compact boundary")
	}
	if state.IsDiscovered("DeferredC") {
		t.Error("expected DeferredC to NOT be discovered (not in boundary)")
	}
}

func TestRestoreToolSearchState_ToolResult(t *testing.T) {
	state := newToolSearchState()

	// Simulate a tool_result block from ToolSearch
	resultContent := `<function>{"name": "Grep", "description": "Search code"}</function>`
	resultJSON, _ := json.Marshal(resultContent)

	messages := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{
					Type:    types.ContentTypeToolResult,
					Content: resultJSON,
				},
			},
		},
	}

	RestoreToolSearchState(messages, state)

	if !state.IsDiscovered("Grep") {
		t.Error("expected Grep to be discovered from tool_result")
	}
}

func TestRestoreToolSearchState_MixedMessages(t *testing.T) {
	state := newToolSearchState()

	// Compact boundary + tool_result
	boundaryContent := `{"subtype":"compact_boundary","preCompactDiscoveredTools":["DeferredA"]}`
	resultContent := `<function>{"name": "Grep", "description": "Search"}</function>`
	resultJSON, _ := json.Marshal(resultContent)

	messages := []types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.ContentBlock{
				types.NewTextBlock(boundaryContent),
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, Content: resultJSON},
			},
		},
	}

	RestoreToolSearchState(messages, state)

	if !state.IsDiscovered("DeferredA") {
		t.Error("expected DeferredA from compact boundary")
	}
	if !state.IsDiscovered("Grep") {
		t.Error("expected Grep from tool_result")
	}
}

// ---------------------------------------------------------------------------
// Tests — tool.IsDeferred integration
// ---------------------------------------------------------------------------

func TestIsDeferred_BuiltTool(t *testing.T) {
	regular := tsStubTool("Read")
	if tool.IsDeferred(regular) {
		t.Error("expected regular tool to NOT be deferred")
	}

	deferred := deferredStubTool("DeferredA")
	if !tool.IsDeferred(deferred) {
		t.Error("expected deferred tool to be deferred")
	}
}

func TestIsDeferred_MCPTool(t *testing.T) {
	// MCP tool without AlwaysLoad should be deferred
	mcpDeferred := deferredMCPTool("mcp__server__tool1")
	if !tool.IsDeferred(mcpDeferred) {
		t.Error("expected MCP tool without AlwaysLoad to be deferred")
	}

	// MCP tool with AlwaysLoad should NOT be deferred
	mcpAlwaysLoad := alwaysLoadMCPTool("mcp__server__important")
	if tool.IsDeferred(mcpAlwaysLoad) {
		t.Error("expected MCP tool with AlwaysLoad to NOT be deferred")
	}
}

// ---------------------------------------------------------------------------
// Tests — ToolSearchActivationError
// ---------------------------------------------------------------------------

func TestToolSearchActivationError(t *testing.T) {
	err := &ToolSearchActivationError{ToolName: "DeferredA"}
	msg := err.Error()
	if !strings.Contains(msg, "DeferredA") {
		t.Errorf("error should mention tool name, got: %s", msg)
	}
	if !strings.Contains(msg, ToolSearchToolName) {
		t.Errorf("error should mention ToolSearch, got: %s", msg)
	}
	if !strings.Contains(msg, "deferred") {
		t.Errorf("error should mention deferred, got: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// Tests — FilterToolsForRequest with ToolSearch tool in map
// ---------------------------------------------------------------------------

func TestFilterToolsForRequest_ToolSearchAlwaysIncluded(t *testing.T) {
	// Build tools including a stub "ToolSearch" tool
	tools := tsBuildToolMap(
		tsStubTool("Read"),
		tsStubTool(ToolSearchToolName), // The ToolSearch tool itself
		deferredStubTool("DeferredA"),
		deferredStubTool("DeferredB"),
		deferredStubTool("DeferredC"),
	)
	state := newToolSearchState()
	order := tsToolOrder(tools)

	active, _, activated := FilterToolsForRequest(tools, state, order)

	if !activated {
		t.Fatal("expected filtering activated")
	}

	activeToolNames := tsExtractToolNames(active)
	if !tsContainsStr(activeToolNames, ToolSearchToolName) {
		t.Error("expected ToolSearch tool to always be included in active tools")
	}
	if !tsContainsStr(activeToolNames, "Read") {
		t.Error("expected Read to be active")
	}
}

// ---------------------------------------------------------------------------
// Test helpers — utility functions
// ---------------------------------------------------------------------------

func tsContainsStr(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

func tsExtractToolNames(tools []tool.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	return names
}

// ---------------------------------------------------------------------------
// Tests — Concurrency safety
// ---------------------------------------------------------------------------

func TestToolSearchState_ConcurrentAccess(t *testing.T) {
	s := newToolSearchState()
	const goroutines = 100
	var wg sync.WaitGroup

	// Concurrent writers: each goroutine discovers a unique tool
	wg.Add(goroutines)
	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("Tool_%d", n)
			s.DiscoverTools([]string{name})
		}(i)
	}

	// Concurrent readers: check tools while writes are happening
	stopReaders := make(chan struct{})
	var readerWg sync.WaitGroup
	readerWg.Add(10)
	for range 10 {
		go func() {
			defer readerWg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					s.IsDiscovered("Tool_0")
					s.DiscoveredNames()
				}
			}
		}()
	}

	wg.Wait()
	close(stopReaders)
	readerWg.Wait()

	// Verify all tools were discovered
	names := s.DiscoveredNames()
	if len(names) != goroutines {
		t.Fatalf("expected %d discovered tools, got %d", goroutines, len(names))
	}
	// Verify each tool is discoverable
	for i := range goroutines {
		name := fmt.Sprintf("Tool_%d", i)
		if !s.IsDiscovered(name) {
			t.Errorf("expected Tool_%d to be discovered", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests — engine toolsearch.go reaching 100%
// ---------------------------------------------------------------------------

func TestFilterToolsForRequest_DisabledTool(t *testing.T) {
	// Test the !t.IsEnabled() path in below-threshold (line 105-106)
	disabledTool := tool.BuildTool(tool.ToolDef{
		Name_:        "Disabled",
		IsEnabled_:   func() bool { return false },
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(json.RawMessage) (string, error) { return "disabled", nil },
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
	})
	tools := map[string]tool.Tool{
		"Read":     tsStubTool("Read"),
		"Disabled": disabledTool,
	}
	state := newToolSearchState()
	order := []string{"Read", "Disabled"}

	active, deferredNames, activated := FilterToolsForRequest(tools, state, order)
	if activated {
		t.Error("should not be activated with 0 deferred")
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active tool (disabled excluded), got %d", len(active))
	}
	if active[0].Name() != "Read" {
		t.Errorf("expected Read, got %s", active[0].Name())
	}
	if len(deferredNames) != 0 {
		t.Errorf("expected no deferred names, got %v", deferredNames)
	}
}

func TestFilterToolsForRequest_DisabledInActivePartition(t *testing.T) {
	// Test disabled tool in active partition path (lines 116-117, 126-129)
	disabled := tool.BuildTool(tool.ToolDef{
		Name_:        "DisabledDeferred",
		ShouldDefer_: true,
		IsEnabled_:   func() bool { return false },
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(json.RawMessage) (string, error) { return "dd", nil },
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
	})
	tools := map[string]tool.Tool{
		"Read":             tsStubTool("Read"),
		"DisabledDeferred": disabled,
		"DeferredA":         deferredStubTool("DeferredA"),
		"DeferredB":       deferredStubTool("DeferredB"),
		"DeferredC":       deferredStubTool("DeferredC"),
	}
	state := newToolSearchState()
	order := []string{"Read", "DisabledDeferred", "DeferredA", "DeferredB", "DeferredC"}

	active, _, activated := FilterToolsForRequest(tools, state, order)
	if !activated {
		t.Fatal("expected activated")
	}
	for _, at := range active {
		if at.Name() == "DisabledDeferred" {
			t.Error("disabled tool should not be in active tools")
		}
	}
}

func TestFilterToolsForRequest_ToolSearchNameMatch(t *testing.T) {
	// Test the "name == ToolSearchToolName" branch in active partition (line 119-120)
	// ToolSearch tool that IS deferred but should still be active
	tools := tsBuildToolMap(
		tsStubTool("Read"),
		tsStubTool(ToolSearchToolName),
		deferredStubTool("DeferredA"),
		deferredStubTool("DeferredB"),
		deferredStubTool("DeferredC"),
	)
	state := newToolSearchState()
	// Don't discover ToolSearch — it should still be active via name check
	order := tsToolOrder(tools)
	active, _, activated := FilterToolsForRequest(tools, state, order)
	if !activated {
		t.Fatal("expected activated")
	}
	activeNames := tsExtractToolNames(active)
	if !tsContainsStr(activeNames, ToolSearchToolName) {
		t.Error("ToolSearch should always be in active tools even when undiscovered")
	}
}

func TestExtractDiscoveredToolNamesFromResult_ByteSlice(t *testing.T) {
	// Test []byte case (line 181-182)
	data := []byte(`<function>{"name": "Grep"}</function>`)
	names := ExtractDiscoveredToolNamesFromResult(data)
	if len(names) != 1 {
		t.Fatalf("expected 1 name, got %d: %v", len(names), names)
	}
	if names[0] != "Grep" {
		t.Errorf("expected Grep, got %s", names[0])
	}
}

func TestExtractDiscoveredToolNamesFromResult_MarshalFallback(t *testing.T) {
	// Test JSON marshal + re-parse fallback (lines 187-196)
	// Use a struct that isn't string, []byte, or map[string]any
	type customResult struct {
		Tools []string `json:"tools"`
	}
	data := customResult{Tools: []string{"Tool1", "Tool2"}}
	names := ExtractDiscoveredToolNamesFromResult(data)
	if len(names) != 2 {
		t.Fatalf("expected 2 names via marshal fallback, got %d: %v", len(names), names)
	}
	if names[0] != "Tool1" {
		t.Errorf("expected Tool1, got %s", names[0])
	}
}

func TestExtractDiscoveredToolNamesFromResult_MarshalFallbackString(t *testing.T) {
	// Test marshal fallback that produces a string (not map) → extractToolNamesFromString
	data := 42 // int → marshal to "42" → not a map → falls through to string extraction
	names := ExtractDiscoveredToolNamesFromResult(data)
	// "42" has no <function> blocks, so should return nil
	if names != nil {
		t.Errorf("expected nil for non-tool data, got %v", names)
	}
}

func TestExtractDiscoveredToolNamesFromResult_MarshalError(t *testing.T) {
	// Test marshal error path (lines 188-190)
	// Channel can't be marshaled
	names := ExtractDiscoveredToolNamesFromResult(make(chan int))
	if names != nil {
		t.Errorf("expected nil for unmarshalable data, got %v", names)
	}
}

func TestExtractToolNamesFromString_StructuredJSON(t *testing.T) {
	// Test structured JSON path in extractToolNamesFromString (lines 213-216)
	jsonStr := `{"tools": ["Tool1", "Tool2"]}`
	names := extractToolNamesFromString(jsonStr)
	if len(names) != 2 {
		t.Fatalf("expected 2 names from structured JSON, got %d: %v", len(names), names)
	}
}

func TestExtractToolNamesFromString_NoFunctionBlocks(t *testing.T) {
	// Test early return when no <function> blocks (line 228)
	names := extractToolNamesFromString("no function blocks here")
	if names != nil {
		t.Errorf("expected nil for no function blocks, got %v", names)
	}
}

func TestExtractToolNamesFromMap_NoMatchingKeys(t *testing.T) {
	// Test empty result when no matching keys (line 271)
	data := map[string]any{
		"other": []any{"something"},
	}
	names := extractToolNamesFromMap(data)
	if names != nil {
		t.Errorf("expected nil for no matching keys, got %v", names)
	}
}

func TestRestoreFromCompactBoundary_NonTextBlock(t *testing.T) {
	// Test non-text content block in system message (lines 312-313)
	state := newToolSearchState()
	messages := []types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse}, // non-text block
			},
		},
	}
	RestoreToolSearchState(messages, state)
	// Should not panic, should not discover anything
	if len(state.DiscoveredNames()) != 0 {
		t.Error("expected no discoveries from non-text block")
	}
}

func TestRestoreFromToolResult_NonToolResult(t *testing.T) {
	// Test non-tool-result block (lines 331-332)
	state := newToolSearchState()
	block := types.ContentBlock{Type: types.ContentTypeText, Text: "hello"}
	restoreFromToolResult(block, state)
	if len(state.DiscoveredNames()) != 0 {
		t.Error("expected no discoveries from text block")
	}
}

func TestRestoreFromToolResult_EmptyContent(t *testing.T) {
	// Test empty content (lines 336-338)
	state := newToolSearchState()
	block := types.ContentBlock{Type: types.ContentTypeToolResult, Content: nil}
	restoreFromToolResult(block, state)
	if len(state.DiscoveredNames()) != 0 {
		t.Error("expected no discoveries from empty content")
	}
}

func TestRestoreFromToolResult_InvalidJSON(t *testing.T) {
	// Test JSON unmarshal failure path (lines 342-344)
	state := newToolSearchState()
	// Content that is valid JSON string but doesn't contain <function> blocks
	content, _ := json.Marshal("plain text no functions")
	block := types.ContentBlock{Type: types.ContentTypeToolResult, Content: content}
	restoreFromToolResult(block, state)
	if len(state.DiscoveredNames()) != 0 {
		t.Error("expected no discoveries from invalid content")
	}
}

func TestToolSearchActivationError_ErrorMethod(t *testing.T) {
	// Test Error() method (lines 359-364)
	err := &ToolSearchActivationError{ToolName: "DeferredA"}
	msg := err.Error()
	if !strings.Contains(msg, "DeferredA") {
		t.Errorf("error should contain tool name, got: %s", msg)
	}
	if !strings.Contains(msg, "deferred") {
		t.Errorf("error should mention deferred, got: %s", msg)
	}
	if !strings.Contains(msg, ToolSearchToolName) {
		t.Errorf("error should mention ToolSearch, got: %s", msg)
	}
}

func TestFilterToolsForRequest_DeferredToolSearchAlwaysActive(t *testing.T) {
	// Test "name == ToolSearchToolName" branch (lines 126-129)
	// ToolSearch tool is marked as deferred but should still be active
	deferredTS := tool.BuildTool(tool.ToolDef{
		Name_:        ToolSearchToolName,
		ShouldDefer_: true,
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(json.RawMessage) (string, error) { return "search", nil },
		Call_: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
	})
	tools := map[string]tool.Tool{
		"Read":             tsStubTool("Read"),
		ToolSearchToolName: deferredTS,
		"DeferredA":         deferredStubTool("DeferredA"),
		"DeferredB":       deferredStubTool("DeferredB"),
		"DeferredC":       deferredStubTool("DeferredC"),
	}
	state := newToolSearchState()
	order := []string{"Read", ToolSearchToolName, "DeferredA", "DeferredB", "DeferredC"}

	active, _, activated := FilterToolsForRequest(tools, state, order)
	if !activated {
		t.Fatal("expected activated")
	}
	activeNames := tsExtractToolNames(active)
	if !tsContainsStr(activeNames, ToolSearchToolName) {
		t.Error("deferred ToolSearch should still be active via name check")
	}
}

func TestExtractToolNamesFromString_UnclosedFunction(t *testing.T) {
	// Test unclosed <function> tag (lines 228-229)
	s := `<function>{"name": "Tool1"}</function><function>{"name": "Tool2"}`
	names := extractToolNamesFromString(s)
	if len(names) != 1 {
		t.Fatalf("expected 1 name (second unclosed), got %d: %v", len(names), names)
	}
	if names[0] != "Tool1" {
		t.Errorf("expected Tool1, got %s", names[0])
	}
}

func TestRestoreFromToolResult_InvalidJSONContent(t *testing.T) {
	// Test JSON unmarshal failure → raw string fallback (lines 342-344)
	state := newToolSearchState()
	// Content that is NOT a valid JSON string (unmarshal to string fails)
	content := json.RawMessage(`not a json string`)
	block := types.ContentBlock{Type: types.ContentTypeToolResult, Content: content}
	restoreFromToolResult(block, state)
	// Should fall through to string(block.Content) which has no <function> blocks
	if len(state.DiscoveredNames()) != 0 {
		t.Error("expected no discoveries from non-JSON content without function blocks")
	}
}
