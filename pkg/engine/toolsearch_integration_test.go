package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// ToolSearch Integration Tests
// ---------------------------------------------------------------------------
//
// These tests verify the full call chain:
//
//	Engine.QuerySync → queryLoop → runTurns → callLLM → Stream → tool execution
//
// They test observable behavior (what tools appear in API requests),
// not internal implementation details.
//
// Scenarios covered:
//  1. Cold start (0 deferred tools) — ToolSearch NOT activated
//  2. Activation threshold (>= 3 deferred) — ToolSearch registered, filtering active
//  3. Discovery flow — ToolSearch call → discovery → next turn includes tool
//  4. Multi-tool discovery — select:A,B discovers multiple tools
//  5. Compact recovery — new engine restores state from compact boundary
//  6. Tool result recovery — new engine restores state from transcript tool_result

// ---------------------------------------------------------------------------
// Test infrastructure for multi-turn ToolSearch tests
// ---------------------------------------------------------------------------

// sequentialProvider captures ALL requests and returns different responses
// on each Stream call. Used for multi-turn integration tests where the LLM
// first calls ToolSearch (tool_use), then responds with text (end_turn).
type sequentialProvider struct {
	mu        sync.Mutex
	responses [][]llm.StreamEvent
	callIdx   int
	requests  []*llm.Request
}

func (p *sequentialProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *sequentialProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	idx := p.callIdx
	p.callIdx++
	if idx >= len(p.responses) {
		p.mu.Unlock()
		return textEventChannel("no more responses"), nil
	}
	events := p.responses[idx]
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent, len(events)+1)
	go func() {
		defer close(ch)
		for _, evt := range events {
			ch <- evt
		}
	}()
	return ch, nil
}

// textEventChannel returns a channel with a single text end_turn response.
func textEventChannel(text string) <-chan llm.StreamEvent {
	events := tsTextEvents(text)
	ch := make(chan llm.StreamEvent, len(events))
	go func() {
		defer close(ch)
		for _, evt := range events {
			ch <- evt
		}
	}()
	return ch
}

// tsToolsProvider creates a ToolsProvider function that returns a fresh map
// on each call. The template map is cloned so refreshTools can safely add
// ToolSearch without modifying the original.
func tsToolsProvider(tools ...tool.Tool) func() map[string]tool.Tool {
	m := make(map[string]tool.Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return func() map[string]tool.Tool {
		return maps.Clone(m)
	}
}

// tsToolUseEvents returns streaming events simulating an LLM tool_use call.
func tsToolUseEvents(toolName, inputJSON string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 100}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "toolu_integ1", Name: toolName}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: inputJSON}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 50}},
		{Type: "message_stop"},
	}
}

// tsTextEvents returns streaming events simulating a text response (end_turn).
func tsTextEvents(text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 100}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 10}},
		{Type: "message_stop"},
	}
}

// toolDefNames extracts tool names from a slice of ToolDefs.
func toolDefNames(defs []llm.ToolDef) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

// ---------------------------------------------------------------------------
// Test 1: Cold Start — No Deferred Tools
//
// Engine with 0 deferred tools: ToolSearch NOT activated, all tools sent.
// ---------------------------------------------------------------------------

func TestToolSearchIntegration_ColdStart_NoDeferred(t *testing.T) {
	cp := &captureProvider{events: tsTextEvents("hello")}

	eng := New(&Params{
		Logger:   slog.Default(),
		Provider: cp,
		ToolsProvider: tsToolsProvider(
			tsStubTool("Read"),
			tsStubTool("Edit"),
		),
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "hi", "")

	req := cp.lastReq
	if req == nil {
		t.Fatal("expected request to be captured")
	}
	names := toolDefNames(req.Tools)

	// ToolSearch should NOT be registered (no deferred tools)
	if tsContainsStr(names, ToolSearchToolName) {
		t.Error("ToolSearch should NOT be registered with no deferred tools")
	}
	// All tools should be present
	if !tsContainsStr(names, "Read") {
		t.Error("Read should be present")
	}
	if !tsContainsStr(names, "Edit") {
		t.Error("Edit should be present")
	}
}

// ---------------------------------------------------------------------------
// Test 2: Activation — At Threshold
//
// Engine with deferred tools: ToolSearch registered, deferred filtered,
// announcement prepended to messages.
// ---------------------------------------------------------------------------

func TestToolSearchIntegration_Activation_AtThreshold(t *testing.T) {
	cp := &captureProvider{events: tsTextEvents("hello")}

	eng := New(&Params{
		Logger:   slog.Default(),
		Provider: cp,
		ToolsProvider: tsToolsProvider(
			tsStubTool("Read"),
			deferredStubTool("DeferredA"),
			deferredStubTool("DeferredB"),
			deferredStubTool("DeferredC"),
		),
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "hi", "")

	req := cp.lastReq
	if req == nil {
		t.Fatal("expected request to be captured")
	}
	names := toolDefNames(req.Tools)

	// ToolSearch should be registered
	if !tsContainsStr(names, ToolSearchToolName) {
		t.Error("ToolSearch should be registered with >= 3 deferred tools")
	}
	// Non-deferred tool should be present
	if !tsContainsStr(names, "Read") {
		t.Error("Read should be present")
	}
	// Deferred tools should NOT be in toolDefs (undiscovered)
	if tsContainsStr(names, "DeferredA") {
		t.Error("DeferredA should NOT be in toolDefs (undiscovered deferred)")
	}
	if tsContainsStr(names, "DeferredB") {
		t.Error("DeferredB should NOT be in toolDefs (undiscovered deferred)")
	}
	if tsContainsStr(names, "DeferredC") {
		t.Error("DeferredC should NOT be in toolDefs (undiscovered deferred)")
	}

	// Deferred tools should be announced in a synthetic user message
	hasAnnouncement := false
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if strings.Contains(block.Text, "<available-deferred-tools>") {
				hasAnnouncement = true
				if !strings.Contains(block.Text, "DeferredA") {
					t.Error("announcement should list DeferredA")
				}
				if !strings.Contains(block.Text, "DeferredB") {
					t.Error("announcement should list DeferredB")
				}
				if !strings.Contains(block.Text, "DeferredC") {
					t.Error("announcement should list DeferredC")
				}
				if !strings.Contains(block.Text, "</available-deferred-tools>") {
					t.Error("announcement should have closing tag")
				}
			}
		}
	}
	if !hasAnnouncement {
		t.Error("expected <available-deferred-tools> announcement in messages")
	}
}

// ---------------------------------------------------------------------------
// Test 3: Discovery Flow — full hot path chain
//
// Call chain:
//
//	QuerySync → queryLoop → runTurns → callLLM → Stream (tool_use ToolSearch)
//	→ StreamingToolExecutor.AddTool → ExecuteAll → ToolSearch.Execute
//	→ runTurns scans result → DiscoverTools
//	→ callLLM → Stream (text, end_turn)
//
// Verifies: deferred tools NOT in turn 1, discovered tools IN turn 2.
// ---------------------------------------------------------------------------

func TestToolSearchIntegration_DiscoveryFlow(t *testing.T) {
	sp := &sequentialProvider{
		responses: [][]llm.StreamEvent{
			tsToolUseEvents(ToolSearchToolName, `{"query":"select:DeferredA"}`),
			tsTextEvents("Done"),
		},
	}

	eng := New(&Params{
		Logger:   slog.Default(),
		Provider: sp,
		ToolsProvider: tsToolsProvider(
			tsStubTool("Read"),
			deferredStubTool("DeferredA"),
			deferredStubTool("DeferredB"),
			deferredStubTool("DeferredC"),
		),
		MaxTurns: 5,
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "find tasks tool", "")

	if len(sp.requests) < 2 {
		t.Fatalf("expected >= 2 API calls (tool_use + text), got %d", len(sp.requests))
	}

	// Turn 1: ToolSearch present, deferred tools NOT present
	names1 := toolDefNames(sp.requests[0].Tools)
	if !tsContainsStr(names1, ToolSearchToolName) {
		t.Error("turn 1: ToolSearch should be present")
	}
	if !tsContainsStr(names1, "Read") {
		t.Error("turn 1: Read should be present")
	}
	if tsContainsStr(names1, "DeferredA") {
		t.Error("turn 1: DeferredA should NOT be present (undiscovered)")
	}
	if tsContainsStr(names1, "DeferredB") {
		t.Error("turn 1: DeferredB should NOT be present (undiscovered)")
	}
	if tsContainsStr(names1, "DeferredC") {
		t.Error("turn 1: DeferredC should NOT be present (undiscovered)")
	}

	// Turn 2: DeferredA discovered and present, others still undiscovered
	names2 := toolDefNames(sp.requests[1].Tools)
	if !tsContainsStr(names2, "DeferredA") {
		t.Error("turn 2: DeferredA should be present (discovered via ToolSearch)")
	}
	if tsContainsStr(names2, "DeferredB") {
		t.Error("turn 2: DeferredB should NOT be present (still undiscovered)")
	}
	if tsContainsStr(names2, "DeferredC") {
		t.Error("turn 2: DeferredC should NOT be present (still undiscovered)")
	}
}

// ---------------------------------------------------------------------------
// Test 4: Multi-tool discovery via select:A,B
// ---------------------------------------------------------------------------

func TestToolSearchIntegration_MultiToolDiscovery(t *testing.T) {
	sp := &sequentialProvider{
		responses: [][]llm.StreamEvent{
			tsToolUseEvents(ToolSearchToolName, `{"query":"select:DeferredA,DeferredB"}`),
			tsTextEvents("Done"),
		},
	}

	eng := New(&Params{
		Logger:   slog.Default(),
		Provider: sp,
		ToolsProvider: tsToolsProvider(
			tsStubTool("Read"),
			deferredStubTool("DeferredA"),
			deferredStubTool("DeferredB"),
			deferredStubTool("DeferredC"),
		),
		MaxTurns: 5,
	})
	t.Cleanup(func() { eng.Close() })

	_ = eng.QuerySync(context.Background(), "find tasks tools", "")

	if len(sp.requests) < 2 {
		t.Fatalf("expected >= 2 API calls, got %d", len(sp.requests))
	}

	names2 := toolDefNames(sp.requests[1].Tools)
	if !tsContainsStr(names2, "DeferredA") {
		t.Error("DeferredA should be discovered")
	}
	if !tsContainsStr(names2, "DeferredB") {
		t.Error("DeferredB should be discovered")
	}
	if tsContainsStr(names2, "DeferredC") {
		t.Error("DeferredC should NOT be discovered (not selected)")
	}
}

// ---------------------------------------------------------------------------
// Test 5: Compact Recovery — new engine instance restores from compact boundary
//
// Simulates: engine discovers tools → compact saves preCompactDiscoveredTools
// → new engine instance (restart) → RestoreToolSearchState → verify filtering.
//
// Tests real process boundary: new instance, not reused object.
// ---------------------------------------------------------------------------

func TestToolSearchIntegration_CompactRecovery(t *testing.T) {
	// Phase 1: Engine discovers tools via ToolSearch
	sp1 := &sequentialProvider{
		responses: [][]llm.StreamEvent{
			tsToolUseEvents(ToolSearchToolName, `{"query":"select:DeferredA"}`),
			tsTextEvents("Done"),
		},
	}
	eng1 := New(&Params{
		Logger:   slog.Default(),
		Provider: sp1,
		ToolsProvider: tsToolsProvider(
			tsStubTool("Read"),
			deferredStubTool("DeferredA"),
			deferredStubTool("DeferredB"),
			deferredStubTool("DeferredC"),
		),
		MaxTurns: 5,
	})
	t.Cleanup(func() { eng1.Close() })
	result := eng1.QuerySync(context.Background(), "find tasks", "")
	if result.Error != nil {
		t.Fatalf("phase 1 error: %v", result.Error)
	}

	// Verify eng1 actually discovered DeferredA
	if !eng1.toolSearch.IsDiscovered("DeferredA") {
		t.Fatal("phase 1: DeferredA should be discovered")
	}

	// Phase 2: Simulate compact boundary from eng1's state
	boundaryContent, _ := json.Marshal(map[string]any{
		"subtype":                   "compact_boundary",
		"preCompactDiscoveredTools": eng1.toolSearch.DiscoveredNames(),
	})
	compactMessages := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock(string(boundaryContent))}},
	}

	// Phase 3: NEW engine instance — simulates restart after compact
	cp2 := &captureProvider{events: tsTextEvents("hello")}
	eng2 := New(&Params{
		Logger:   slog.Default(),
		Provider: cp2,
		ToolsProvider: tsToolsProvider(
			tsStubTool("Read"),
			deferredStubTool("DeferredA"),
			deferredStubTool("DeferredB"),
			deferredStubTool("DeferredC"),
		),
	})

	// Restore state from compact boundary
	RestoreToolSearchState(compactMessages, eng2.toolSearch)
	eng2.setMessages(compactMessages)

	_ = eng2.QuerySync(context.Background(), "use tasks", "")

	req2 := cp2.lastReq
	if req2 == nil {
		t.Fatal("phase 2: expected request to be captured")
	}
	names2 := toolDefNames(req2.Tools)

	// DeferredA should be active (restored from compact boundary)
	if !tsContainsStr(names2, "DeferredA") {
		t.Error("phase 2: DeferredA should be active after compact recovery")
	}
	// Other deferred tools still undiscovered
	if tsContainsStr(names2, "DeferredB") {
		t.Error("phase 2: DeferredB should NOT be active (not in compact boundary)")
	}
	if tsContainsStr(names2, "DeferredC") {
		t.Error("phase 2: DeferredC should NOT be active (not in compact boundary)")
	}
}

// ---------------------------------------------------------------------------
// Test 6: Tool Result Recovery — new engine restores from transcript
//
// Simulates: session restart with existing transcript containing
// ToolSearch tool_result with <function> blocks.
//
// Tests real process boundary: new instance restores from persisted messages.
// ---------------------------------------------------------------------------

func TestToolSearchIntegration_ToolResultRecovery(t *testing.T) {
	// Build a transcript with a ToolSearch tool_result containing <function> blocks
	resultContent := `<function>{"name": "DeferredA", "description": "List tasks"}</function>`
	resultJSON, _ := json.Marshal(resultContent)

	transcript := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("find tasks")}},
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "toolu_1", Name: ToolSearchToolName, Input: json.RawMessage(`{"query":"DeferredA"}`)},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, Content: resultJSON},
			},
		},
	}

	// NEW engine instance — simulates restart
	cp := &captureProvider{events: tsTextEvents("hello")}
	eng := New(&Params{
		Logger:   slog.Default(),
		Provider: cp,
		ToolsProvider: tsToolsProvider(
			tsStubTool("Read"),
			deferredStubTool("DeferredA"),
			deferredStubTool("DeferredB"),
			deferredStubTool("DeferredC"),
		),
	})
	t.Cleanup(func() { eng.Close() })

	RestoreToolSearchState(transcript, eng.toolSearch)
	eng.setMessages(transcript)

	_ = eng.QuerySync(context.Background(), "use tasks", "")

	req := cp.lastReq
	if req == nil {
		t.Fatal("expected request to be captured")
	}
	names := toolDefNames(req.Tools)

	if !tsContainsStr(names, "DeferredA") {
		t.Error("DeferredA should be active after tool_result recovery")
	}
	if tsContainsStr(names, "DeferredB") {
		t.Error("DeferredB should NOT be active (not in tool_result)")
	}
	if tsContainsStr(names, "DeferredC") {
		t.Error("DeferredC should NOT be active (not in tool_result)")
	}
}
