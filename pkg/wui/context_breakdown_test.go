package wui

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
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
			{Name: "System prompt", ID: "system_prompt", Tokens: 5000, Percentage: 2.5, Color: "12", IsFree: false, IsReserved: false},
			{Name: "Messages", ID: "messages", Tokens: 30000, Percentage: 15.0, Color: "255", IsFree: false, IsReserved: false},
			{Name: "Free space", ID: "free_space", Tokens: 150000, Percentage: 75.0, Color: "240", IsFree: true, IsReserved: false},
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
	if cat0["id"] != "system_prompt" {
		t.Errorf("cat0 id = %v, want system_prompt", cat0["id"])
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
	if cat2["id"] != "free_space" {
		t.Errorf("cat2 id = %v, want free_space", cat2["id"])
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
			{Name: "Base prompt", ID: "base_prompt", Tokens: 2000},
			{Name: "Tool prompts", ID: "tool_prompts", Tokens: 3000},
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
	if !ok || len(sps) != 2 {
		t.Fatalf("systemPromptSections: ok=%v len=%d", ok, len(sps))
	}
	sps0 := sps[0].(map[string]any)
	if sps0["name"] != "Base prompt" {
		t.Errorf("sps0 name = %v", sps0["name"])
	}
	if sps0["id"] != "base_prompt" {
		t.Errorf("sps0 id = %v, want base_prompt", sps0["id"])
	}
	sps1 := sps[1].(map[string]any)
	if sps1["id"] != "tool_prompts" {
		t.Errorf("sps1 id = %v, want tool_prompts", sps1["id"])
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

// ---------------------------------------------------------------------------
// context_breakdown.go
// ---------------------------------------------------------------------------

// Kills the composite/field-clear mutants on every converter line (110, 112,
// 121, 129, 137, 145 x2, 153 x2, 165, 168, 181, 183, 188, 193, 194, 195,
// 207, 208, 209): each converter must copy every field.
func TestMutD_ConvertersCopyEveryField(t *testing.T) {
	mcp := convertMCPTools([]engine.MCPToolDetail{{Name: "search", ServerName: "brave", Tokens: 800, IsLoaded: true}})
	if len(mcp) != 1 {
		t.Fatalf("mcp len = %d, want 1", len(mcp))
	}
	if mcp[0].ServerName != "brave" {
		t.Errorf("mcp ServerName = %q, want brave", mcp[0].ServerName)
	}
	if mcp[0].IsLoaded != true {
		t.Errorf("mcp IsLoaded = %v, want true", mcp[0].IsLoaded)
	}
	if mcp[0].Tokens != 800 || mcp[0].Name != "search" {
		t.Errorf("mcp = %+v, want the input detail verbatim", mcp[0])
	}

	sys := convertSystemTools([]engine.SystemToolDetail{{Name: "Bash", Tokens: 1000}})
	if len(sys) != 1 || sys[0].Name != "Bash" || sys[0].Tokens != 1000 {
		t.Errorf("systemTools = %+v, want {Bash 1000}", sys)
	}

	sec := convertPromptSections([]engine.SystemPromptSectionDetail{{Name: "Base prompt", ID: "base_prompt", Tokens: 2000}})
	if len(sec) != 1 || sec[0].Name != "Base prompt" || sec[0].ID != "base_prompt" || sec[0].Tokens != 2000 {
		t.Errorf("promptSections = %+v, want all three fields", sec)
	}

	mem := convertMemoryFiles([]engine.MemoryFileDetail{{Path: "CLAUDE.md", Tokens: 1500}})
	if len(mem) != 1 || mem[0].Path != "CLAUDE.md" || mem[0].Tokens != 1500 {
		t.Errorf("memoryFiles = %+v, want both fields", mem)
	}

	ag := convertAgents([]engine.AgentDetail{{AgentType: "coder", Source: "built-in", Tokens: 400}})
	if len(ag) != 1 || ag[0].AgentType != "coder" || ag[0].Source != "built-in" || ag[0].Tokens != 400 {
		t.Errorf("agents = %+v, want all three fields", ag)
	}

	sk := convertSkills([]engine.SkillDetail{{Name: "commit", Source: "plugin", Tokens: 300}})
	if len(sk) != 1 || sk[0].Name != "commit" || sk[0].Source != "plugin" || sk[0].Tokens != 300 {
		t.Errorf("skills = %+v, want all three fields", sk)
	}

	cat := convertCategories([]engine.ContextCategory{{Name: "Messages", ID: "messages", Tokens: 30000, Percentage: 15.5, Color: "255", IsFree: false, IsReserved: true}})
	if len(cat) != 1 {
		t.Fatalf("categories len = %d, want 1", len(cat))
	}
	if cat[0].Percentage != 15.5 {
		t.Errorf("category Percentage = %v, want 15.5", cat[0].Percentage)
	}
	if cat[0].IsReserved != true {
		t.Errorf("category IsReserved = %v, want true", cat[0].IsReserved)
	}
	if cat[0].Name != "Messages" || cat[0].ID != "messages" || cat[0].Tokens != 30000 || cat[0].Color != "255" || cat[0].IsFree != false {
		t.Errorf("category = %+v, want every field copied", cat[0])
	}

	mb := convertMessageBreakdown(&engine.MessageBreakdown{
		ToolCallTokens:      3000,
		ToolResultTokens:    6000,
		AttachmentTokens:    500,
		AssistantTextTokens: 2000,
		UserTextTokens:      1000,
		ToolCallsByType:     []engine.ToolCallByType{{Name: "Bash", CallTokens: 2000, ResultTokens: 4000}},
		AttachmentsByType:   []engine.AttachmentByType{{Name: "file", Tokens: 500}},
	})
	if mb == nil {
		t.Fatal("messageBreakdown = nil, want populated")
	}
	if mb.AttachmentTokens != 500 || mb.AssistantTextTokens != 2000 || mb.UserTextTokens != 1000 {
		t.Errorf("mb token fields = %d/%d/%d, want 500/2000/1000", mb.AttachmentTokens, mb.AssistantTextTokens, mb.UserTextTokens)
	}
	if len(mb.ToolCallsByType) != 1 || mb.ToolCallsByType[0].Name != "Bash" || mb.ToolCallsByType[0].ResultTokens != 4000 {
		t.Errorf("toolCallsByType = %+v, want name and result tokens", mb.ToolCallsByType)
	}
	if len(mb.AttachmentsByType) != 1 || mb.AttachmentsByType[0].Name != "file" || mb.AttachmentsByType[0].Tokens != 500 {
		t.Errorf("attachmentsByType = %+v, want name and tokens", mb.AttachmentsByType)
	}

	api := convertAPIUsage(&engine.APIUsageSnapshot{
		InputTokens: 70000, OutputTokens: 2000,
		CacheCreationInputTokens: 5000, CacheReadInputTokens: 60000,
	})
	if api == nil {
		t.Fatal("apiUsage = nil, want populated")
	}
	if api.OutputTokens != 2000 || api.CacheCreationInputTokens != 5000 || api.CacheReadInputTokens != 60000 {
		t.Errorf("apiUsage = %+v, want output/cache fields copied", api)
	}
}

// Kills context_breakdown.go:176 (branch/if): nil must map to nil without a
// panic (a non-nil breakdown with nil MessageBreakdown is a legal state).
func TestMutD_ConvertMessageBreakdown_NilIsNil(t *testing.T) {
	if got := convertMessageBreakdown(nil); got != nil {
		t.Fatalf("convertMessageBreakdown(nil) = %+v, want nil", got)
	}
}

// Kills context_breakdown.go:214 (expression/remove x2, numbers x2): nil and
// zero-token breakdowns must serialize as the minimal early-return shape, so
// the wire never carries an empty full frame.
func TestMutD_BuildContextBreakdown_ZeroShapes(t *testing.T) {
	want := `{"type":"context_breakdown","totalTokens":0}`
	if got := string(buildContextBreakdown(nil)); got != want {
		t.Errorf("nil breakdown = %s, want %s", got, want)
	}
	bd := &engine.ContextBreakdown{Model: "m", TotalTokens: 0, Percentage: 12.5}
	if got := string(buildContextBreakdown(bd)); got != want {
		t.Errorf("zero-token breakdown = %s, want %s (zero tokens take the minimal path)", got, want)
	}
}

// Kills context_breakdown.go:225 (composite/field-clear: ContextWindow) and
// :228 (composite/field-clear: IsAutoCompact).
func TestMutD_BuildContextBreakdown_TopLevelFields(t *testing.T) {
	bd := &engine.ContextBreakdown{
		Model:         "glm-5.2",
		ContextWindow: 131072,
		TotalTokens:   7,
		Percentage:    2.5,
		IsAutoCompact: true,
	}
	payload := buildContextBreakdown(bd)
	if payload == nil {
		t.Fatal("payload = nil, want JSON")
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	if m["contextWindow"] != float64(131072) {
		t.Errorf("contextWindow = %v, want 131072", m["contextWindow"])
	}
	if m["isAutoCompact"] != true {
		t.Errorf("isAutoCompact = %v, want true", m["isAutoCompact"])
	}
	if m["model"] != "glm-5.2" || m["percentage"] != 2.5 {
		t.Errorf("model/percentage = %v/%v, want glm-5.2/2.5", m["model"], m["percentage"])
	}
}

// Kills context_breakdown.go:243 (expression/error-guard) and :244
// (statement/remove): a NaN percentage is the one input json.Marshal rejects;
// the failure must be logged.
func TestMutD_BuildContextBreakdown_MarshalFailureWarns(t *testing.T) {
	rec := mutDCaptureSlog(t)
	bd := &engine.ContextBreakdown{TotalTokens: 10, Percentage: math.NaN()}
	if payload := buildContextBreakdown(bd); payload != nil {
		t.Fatalf("payload = %s, want nil when marshal fails", payload)
	}
	if n := rec.count(slog.LevelWarn, "wui: marshal context_breakdown failed"); n != 1 {
		t.Fatalf("marshal-failure warn count = %d, want exactly 1", n)
	}
}

// Kills context_breakdown.go:253 (branch/if) and :265 (branch/if): a missing
// active engine must be a silent no-op, not a nil-interface panic.
func TestMutD_ContextHandlers_NilEngineNoPanic(t *testing.T) {
	c := newTestConnector(t)
	unknown := "no-such-engine"
	c.active.Store(&unknown)
	c.handleContextRequest()
	c.handleCompactRequest()
}

// Kills context_breakdown.go:268 (expression/error-guard), :268
// (conditional/negated, with the success test below) and :269
// (statement/remove): compact failures must warn exactly once.
func TestMutD_HandleCompactRequest_FailureWarns(t *testing.T) {
	c := newTestConnector(t)
	c.mock().manualCompactFn = func(context.Context, types.Message, string) (*short.CompactResult, error) {
		return nil, errors.New("compact: engine busy")
	}
	rec := mutDCaptureSlog(t)
	c.handleCompactRequest()
	if n := rec.count(slog.LevelWarn, "wui: manual compact failed"); n != 1 {
		t.Fatalf("compact-failure warn count = %d, want exactly 1", n)
	}
}

// Kills the conditional/negated flavor of context_breakdown.go:268.
func TestMutD_HandleCompactRequest_SuccessSilent(t *testing.T) {
	c := newTestConnector(t)
	c.mock().manualCompactFn = func(context.Context, types.Message, string) (*short.CompactResult, error) {
		return nil, nil
	}
	rec := mutDCaptureSlog(t)
	c.handleCompactRequest()
	rec.assertNoneWithin(t, slog.LevelWarn, "wui: manual compact failed", 100*time.Millisecond)
}
