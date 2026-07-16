package wui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
)

func TestContextRequest_FullBreakdown(t *testing.T) {
	c := newTestConnector(t)
	bd := &engine.ContextBreakdown{
		Model:         "test-model",
		ContextWindow: 200000,
		TotalTokens:   50000,
		Percentage:    25.0,
		IsAutoCompact: true,
		Categories: []engine.ContextCategory{
			{Name: "System prompt", Tokens: 5000, Percentage: 2.5, Color: "12", IsFree: false, IsReserved: false},
			{Name: "Messages", Tokens: 30000, Percentage: 15.0, Color: "255", IsFree: false, IsReserved: false},
			{Name: "Free space", Tokens: 150000, Percentage: 75.0, Color: "240", IsFree: true, IsReserved: false},
		},
		MCPToolsLoaded: []engine.MCPToolDetail{
			{Name: "search", ServerName: "brave", Tokens: 800, IsLoaded: true},
		},
		MessageBreakdown: &engine.MessageBreakdown{
			ToolCallTokens:   5000,
			ToolResultTokens: 10000,
		},
	}
	c.mock().contextBreakdownFn = func() *engine.ContextBreakdown { return bd }

	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]string{"type": "context_request"})

	data := readWSMessage(t, ws)
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["type"] != "context_breakdown" {
		t.Fatalf("type = %v, want context_breakdown", msg["type"])
	}
	if msg["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", msg["model"])
	}
	if msg["totalTokens"] != float64(50000) {
		t.Errorf("totalTokens = %v, want 50000", msg["totalTokens"])
	}
	if msg["percentage"] != 25.0 {
		t.Errorf("percentage = %v, want 25.0", msg["percentage"])
	}
	cats, ok := msg["categories"].([]any)
	if !ok {
		t.Fatalf("categories is not array: %T", msg["categories"])
	}
	if len(cats) != 3 {
		t.Fatalf("categories len = %d, want 3", len(cats))
	}
	cat0 := cats[0].(map[string]any)
	if cat0["name"] != "System prompt" {
		t.Errorf("cat0 name = %v", cat0["name"])
	}
	if cat0["tokens"] != float64(5000) {
		t.Errorf("cat0 tokens = %v, want 5000", cat0["tokens"])
	}
	if cat0["color"] != "12" {
		t.Errorf("cat0 color = %v, want \"12\"", cat0["color"])
	}
	if cat0["isFree"] != false {
		t.Errorf("cat0 isFree = %v, want false", cat0["isFree"])
	}
	cat2 := cats[2].(map[string]any)
	if cat2["isFree"] != true {
		t.Errorf("cat2 isFree = %v, want true", cat2["isFree"])
	}

	mcpLoaded, ok := msg["mcpToolsLoaded"].([]any)
	if !ok {
		t.Fatalf("mcpToolsLoaded is not array: %T", msg["mcpToolsLoaded"])
	}
	if len(mcpLoaded) != 1 {
		t.Fatalf("mcpToolsLoaded len = %d, want 1", len(mcpLoaded))
	}
	mcp0 := mcpLoaded[0].(map[string]any)
	if mcp0["name"] != "search" {
		t.Errorf("mcp0 name = %v, want search", mcp0["name"])
	}
	if mcp0["tokens"] != float64(800) {
		t.Errorf("mcp0 tokens = %v, want 800", mcp0["tokens"])
	}

	mb, ok := msg["messageBreakdown"].(map[string]any)
	if !ok {
		t.Fatalf("messageBreakdown is not object: %T", msg["messageBreakdown"])
	}
	if mb["toolCallTokens"] != float64(5000) {
		t.Errorf("mb toolCallTokens = %v, want 5000", mb["toolCallTokens"])
	}
	if mb["toolResultTokens"] != float64(10000) {
		t.Errorf("mb toolResultTokens = %v, want 10000", mb["toolResultTokens"])
	}
}

func TestContextRequest_EmptyBreakdown(t *testing.T) {
	c := newTestConnector(t)
	c.mock().contextBreakdownFn = func() *engine.ContextBreakdown {
		return &engine.ContextBreakdown{TotalTokens: 0}
	}

	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]string{"type": "context_request"})

	data := readWSMessage(t, ws)
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["type"] != "context_breakdown" {
		t.Fatalf("type = %v, want context_breakdown", msg["type"])
	}
	if msg["totalTokens"] != float64(0) {
		t.Errorf("totalTokens = %v, want 0", msg["totalTokens"])
	}
}

func TestContextRequest_NilBreakdown(t *testing.T) {
	c := newTestConnector(t)
	// contextBreakdownFn is nil, so ContextBreakdown() returns nil

	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]string{"type": "context_request"})

	data := readWSMessage(t, ws)
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["type"] != "context_breakdown" {
		t.Fatalf("type = %v, want context_breakdown", msg["type"])
	}
	if msg["totalTokens"] != float64(0) {
		t.Errorf("totalTokens = %v, want 0", msg["totalTokens"])
	}
}

func TestContextBreakdownWire_AllFields(t *testing.T) {
	c := newTestConnector(t)
	bd := &engine.ContextBreakdown{
		Model:         "full-model",
		ContextWindow: 200000,
		TotalTokens:   80000,
		Percentage:    40.0,
		IsAutoCompact: true,
		Categories: []engine.ContextCategory{
			{Name: "Messages", Tokens: 40000, Percentage: 20.0, Color: "255"},
		},
		MCPToolsLoaded: []engine.MCPToolDetail{
			{Name: "loaded_tool", ServerName: "srv1", Tokens: 500, IsLoaded: true},
		},
		MCPToolsDeferred: []engine.MCPToolDetail{
			{Name: "deferred_tool", ServerName: "srv2", Tokens: 300, IsLoaded: false},
		},
		DeferredBuiltinTools: []engine.SystemToolDetail{
			{Name: "Web", Tokens: 200},
		},
		SystemTools: []engine.SystemToolDetail{
			{Name: "Bash", Tokens: 1000},
		},
		SystemPromptSections: []engine.SystemPromptSectionDetail{
			{Name: "Base prompt", Tokens: 2000},
		},
		MemoryFiles: []engine.MemoryFileDetail{
			{Path: "CLAUDE.md", Tokens: 1500},
		},
		Agents: []engine.AgentDetail{
			{AgentType: "coder", Source: "built-in", Tokens: 400},
		},
		Skills: []engine.SkillDetail{
			{Name: "commit", Source: "plugin", Tokens: 300},
		},
		MessageBreakdown: &engine.MessageBreakdown{
			ToolCallTokens:      3000,
			ToolResultTokens:    6000,
			AttachmentTokens:    500,
			AssistantTextTokens: 2000,
			UserTextTokens:      1000,
			ToolCallsByType: []engine.ToolCallByType{
				{Name: "Bash", CallTokens: 2000, ResultTokens: 4000},
			},
			AttachmentsByType: []engine.AttachmentByType{
				{Name: "file", Tokens: 500},
			},
		},
		APIUsage: &engine.APIUsageSnapshot{
			InputTokens:              70000,
			OutputTokens:             2000,
			CacheCreationInputTokens: 5000,
			CacheReadInputTokens:     60000,
		},
	}
	c.mock().contextBreakdownFn = func() *engine.ContextBreakdown { return bd }

	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]string{"type": "context_request"})

	data := readWSMessage(t, ws)
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify every section is present
	sps, ok := msg["systemPromptSections"].([]any)
	if !ok || len(sps) != 1 {
		t.Fatalf("systemPromptSections: ok=%v len=%d", ok, len(sps))
	}
	sps0 := sps[0].(map[string]any)
	if sps0["name"] != "Base prompt" {
		t.Errorf("sps0 name = %v", sps0["name"])
	}

	mfs, ok := msg["memoryFiles"].([]any)
	if !ok || len(mfs) != 1 {
		t.Fatalf("memoryFiles: ok=%v len=%d", ok, len(mfs))
	}
	mfs0 := mfs[0].(map[string]any)
	if mfs0["path"] != "CLAUDE.md" {
		t.Errorf("mfs0 path = %v", mfs0["path"])
	}

	sts, ok := msg["systemTools"].([]any)
	if !ok || len(sts) != 1 {
		t.Fatalf("systemTools: ok=%v len=%d", ok, len(sts))
	}
	sts0 := sts[0].(map[string]any)
	if sts0["name"] != "Bash" {
		t.Errorf("sts0 name = %v", sts0["name"])
	}

	agents, ok := msg["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("agents: ok=%v len=%d", ok, len(agents))
	}
	ag0 := agents[0].(map[string]any)
	if ag0["agentType"] != "coder" {
		t.Errorf("ag0 agentType = %v", ag0["agentType"])
	}

	skills, ok := msg["skills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("skills: ok=%v len=%d", ok, len(skills))
	}
	sk0 := skills[0].(map[string]any)
	if sk0["name"] != "commit" {
		t.Errorf("sk0 name = %v", sk0["name"])
	}

	api, ok := msg["apiUsage"].(map[string]any)
	if !ok {
		t.Fatalf("apiUsage missing")
	}
	if api["inputTokens"] != float64(70000) {
		t.Errorf("apiUsage inputTokens = %v, want 70000", api["inputTokens"])
	}

	mb, ok := msg["messageBreakdown"].(map[string]any)
	if !ok {
		t.Fatalf("messageBreakdown missing")
	}
	tcbt, ok := mb["toolCallsByType"].([]any)
	if !ok || len(tcbt) != 1 {
		t.Fatalf("toolCallsByType: ok=%v len=%d", ok, len(tcbt))
	}
	tcbt0 := tcbt[0].(map[string]any)
	if tcbt0["callTokens"] != float64(2000) {
		t.Errorf("tcbt0 callTokens = %v, want 2000", tcbt0["callTokens"])
	}

	abt, ok := mb["attachmentsByType"].([]any)
	if !ok || len(abt) != 1 {
		t.Fatalf("attachmentsByType: ok=%v len=%d", ok, len(abt))
	}
	abt0 := abt[0].(map[string]any)
	if abt0["tokens"] != float64(500) {
		t.Errorf("abt0 tokens = %v, want 500", abt0["tokens"])
	}

	dbt, ok := msg["deferredBuiltinTools"].([]any)
	if !ok || len(dbt) != 1 {
		t.Fatalf("deferredBuiltinTools: ok=%v len=%d", ok, len(dbt))
	}
	dbt0 := dbt[0].(map[string]any)
	if dbt0["name"] != "Web" {
		t.Errorf("dbt0 name = %v, want Web", dbt0["name"])
	}

	mcpDef, ok := msg["mcpToolsDeferred"].([]any)
	if !ok || len(mcpDef) != 1 {
		t.Fatalf("mcpToolsDeferred: ok=%v len=%d", ok, len(mcpDef))
	}
	mcpDef0 := mcpDef[0].(map[string]any)
	if mcpDef0["name"] != "deferred_tool" {
		t.Errorf("mcpDef0 name = %v, want deferred_tool", mcpDef0["name"])
	}
}

// TestContextRequest_NoEngine verifies no WS response when activeEngine is nil.
func TestContextRequest_NoEngine(t *testing.T) {
	c := newTestConnector(t)
	c.mock().contextBreakdownFn = func() *engine.ContextBreakdown {
		return &engine.ContextBreakdown{TotalTokens: 50000}
	}

	ws := dialAndStore(t, c)
	defer ws.Close()

	// Manually clear the active engine by setting active to an unknown ID.
	badID := "nonexistent"
	c.active.Store(&badID)

	sendJSON(t, ws, map[string]string{"type": "context_request"})

	done := make(chan []byte, 1)
	go func() {
		_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
		_, data, err := ws.ReadMessage()
		if err != nil {
			done <- nil
			return
		}
		done <- data
	}()
	select {
	case data := <-done:
		if data != nil {
			t.Fatal("expected no response when active engine is nil")
		}
	case <-time.After(400 * time.Millisecond):
	}
}
