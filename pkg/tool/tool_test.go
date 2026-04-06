package tool_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// BuildTool — defaults
// ---------------------------------------------------------------------------

func TestBuildToolDefaults(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_:  "TestTool",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "ok"}, nil
		},
		InputSchema_: func() json.RawMessage {
			return json.RawMessage(`{"type":"object"}`)
		},
		Description_: func(input json.RawMessage) (string, error) {
			return "a test tool", nil
		},
	}

	tt := tool.BuildTool(def)

	// Required fields
	if tt.Name() != "TestTool" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "TestTool")
	}
	if tt.Aliases() != nil {
		t.Errorf("Aliases() = %v, want nil", tt.Aliases())
	}

	desc, err := tt.Description(nil)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "a test tool" {
		t.Errorf("Description() = %q, want %q", desc, "a test tool")
	}

	schema := tt.InputSchema()
	if string(schema) != `{"type":"object"}` {
		t.Errorf("InputSchema() = %s, want %s", schema, `{"type":"object"}`)
	}

	// Defaults: IsReadOnly = false
	if tt.IsReadOnly(nil) {
		t.Error("IsReadOnly() default = true, want false")
	}
	// Defaults: IsDestructive = false
	if tt.IsDestructive(nil) {
		t.Error("IsDestructive() default = true, want false")
	}
	// Defaults: IsConcurrencySafe = false
	if tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() default = true, want false")
	}
	// Defaults: IsEnabled = true
	if !tt.IsEnabled() {
		t.Error("IsEnabled() default = false, want true")
	}
	// Defaults: InterruptBehavior = InterruptCancel (0) — zero value of iota
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %d, want %d", tt.InterruptBehavior(), tool.InterruptCancel)
	}
	// Defaults: Prompt = ""
	if tt.Prompt() != "" {
		t.Errorf("Prompt() = %q, want empty", tt.Prompt())
	}
	// Defaults: CheckPermissions = allow
	perm := tt.CheckPermissions(nil, nil)
	if perm.Behavior() != types.BehaviorAllow {
		t.Errorf("CheckPermissions() behavior = %q, want %q", perm.Behavior(), types.BehaviorAllow)
	}
}

func TestBuildToolWithOverrides(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_:  "Override",
		Aliases_: []string{"ov"},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "done"}, nil
		},
		InputSchema_: func() json.RawMessage {
			return json.RawMessage(`{}`)
		},
		Description_: func(input json.RawMessage) (string, error) {
			return "override tool", nil
		},
		IsReadOnly_: func(input json.RawMessage) bool { return true },
		IsDestructive_: func(input json.RawMessage) bool { return true },
		IsConcurrencySafe_: func(input json.RawMessage) bool { return true },
		IsEnabled_: func() bool { return false },
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: "I am override",
		CheckPermissions_: func(input json.RawMessage, tctx *types.ToolUseContext) types.PermissionResult {
			return types.PermissionDenyDecision{Message: "nope"}
		},
	}

	tt := tool.BuildTool(def)

	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly() = false, want true")
	}
	if !tt.IsDestructive(nil) {
		t.Error("IsDestructive() = false, want true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = false, want true")
	}
	if tt.IsEnabled() {
		t.Error("IsEnabled() = true, want false")
	}
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %d, want %d", tt.InterruptBehavior(), tool.InterruptCancel)
	}
	if tt.Prompt() != "I am override" {
		t.Errorf("Prompt() = %q, want %q", tt.Prompt(), "I am override")
	}

	aliases := tt.Aliases()
	if len(aliases) != 1 || aliases[0] != "ov" {
		t.Errorf("Aliases() = %v, want [ov]", aliases)
	}

	perm := tt.CheckPermissions(nil, nil)
	if perm.Behavior() != types.BehaviorDeny {
		t.Errorf("CheckPermissions() = %q, want %q", perm.Behavior(), types.BehaviorDeny)
	}
}

// ---------------------------------------------------------------------------
// BuildTool — Call execution
// ---------------------------------------------------------------------------

func TestBuildToolCall(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_:  "CallTest",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: map[string]any{"echo": string(input)}}, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	}

	tt := tool.BuildTool(def)

	result, err := tt.Call(context.Background(), json.RawMessage(`"hello"`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map[string]any", result.Data)
	}
	if data["echo"] != `"hello"` {
		t.Errorf("Data[echo] = %q, want %q", data["echo"], `"hello"`)
	}
}

// ---------------------------------------------------------------------------
// ApplyContextModifier
// ---------------------------------------------------------------------------

func TestApplyContextModifier_NilModifier(t *testing.T) {
	t.Parallel()

	tctx := &types.ToolUseContext{ToolUseID: "orig"}
	result := &tool.ToolResult{Data: "test"}

	got := tool.ApplyContextModifier(result, tctx, false)
	if got.ToolUseID != "orig" {
		t.Errorf("ToolUseID = %q, want %q", got.ToolUseID, "orig")
	}
}

func TestApplyContextModifier_ConcurrencySafe_IgnoresModifier(t *testing.T) {
	t.Parallel()

	tctx := &types.ToolUseContext{ToolUseID: "orig", WorkingDir: "/old"}
	result := &tool.ToolResult{
		Data: "test",
		ContextModifier: func(tctx *types.ToolUseContext) *types.ToolUseContext {
			tctx.WorkingDir = "/modified"
			return tctx
		},
	}

	got := tool.ApplyContextModifier(result, tctx, true)
	if got.WorkingDir != "/old" {
		t.Errorf("WorkingDir = %q, want %q (modifier should be ignored)", got.WorkingDir, "/old")
	}
}

func TestApplyContextModifier_SerialTool_AppliesModifier(t *testing.T) {
	t.Parallel()

	tctx := &types.ToolUseContext{ToolUseID: "orig", WorkingDir: "/old"}
	result := &tool.ToolResult{
		Data: "test",
		ContextModifier: func(tctx *types.ToolUseContext) *types.ToolUseContext {
			tctx.WorkingDir = "/modified"
			return tctx
		},
	}

	got := tool.ApplyContextModifier(result, tctx, false)
	if got.WorkingDir != "/modified" {
		t.Errorf("WorkingDir = %q, want %q", got.WorkingDir, "/modified")
	}
}

// ---------------------------------------------------------------------------
// InterruptBehavior constants
// ---------------------------------------------------------------------------

func TestInterruptBehaviorConstants(t *testing.T) {
	t.Parallel()

	if tool.InterruptCancel != 0 {
		t.Errorf("InterruptCancel = %d, want 0", tool.InterruptCancel)
	}
	if tool.InterruptBlock != 1 {
		t.Errorf("InterruptBlock = %d, want 1", tool.InterruptBlock)
	}
}

// ---------------------------------------------------------------------------
// SearchReadKind
// ---------------------------------------------------------------------------

func TestSearchReadKind(t *testing.T) {
	t.Parallel()

	srk := tool.SearchReadKind{IsSearch: true, IsRead: true, IsList: false}
	if !srk.IsSearch {
		t.Error("IsSearch = false, want true")
	}
	if !srk.IsRead {
		t.Error("IsRead = false, want true")
	}
	if srk.IsList {
		t.Error("IsList = true, want false")
	}
}

// ---------------------------------------------------------------------------
// MCPMeta
// ---------------------------------------------------------------------------

func TestMCPMetaJSON(t *testing.T) {
	t.Parallel()

	meta := tool.MCPMeta{
		Meta: map[string]any{"key": "val"},
		StructuredContent: map[string]any{"result": true},
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got tool.MCPMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Meta["key"] != "val" {
		t.Errorf("Meta[key] = %v, want val", got.Meta["key"])
	}
	if got.StructuredContent["result"] != true {
		t.Errorf("StructuredContent[result] = %v, want true", got.StructuredContent["result"])
	}
}

// ---------------------------------------------------------------------------
// ToolResult JSON
// ---------------------------------------------------------------------------

func TestToolResultJSON(t *testing.T) {
	t.Parallel()

	result := tool.ToolResult{
		Data: map[string]any{"output": "hello"},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got tool.ToolResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	m, ok := got.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map[string]any", got.Data)
	}
	if m["output"] != "hello" {
		t.Errorf("Data[output] = %v, want hello", m["output"])
	}
}

// ---------------------------------------------------------------------------
// Tool interface compliance
// ---------------------------------------------------------------------------

func TestBuildToolImplementsToolInterface(t *testing.T) {
	t.Parallel()

	// This test verifies the interface is satisfied at compile time
	// by assigning to a Tool variable.
	var _ = tool.BuildTool(tool.ToolDef{
		Name_:        "InterfaceCheck",
		Call_:        func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) { return nil, nil },
		InputSchema_: func() json.RawMessage { return nil },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	})
}
