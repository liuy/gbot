package tool_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// BuildTool — defaults
// ---------------------------------------------------------------------------

func TestBuildToolDefaults(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "TestTool",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
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
		Name_:    "Override",
		Aliases_: []string{"ov"},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "done"}, nil
		},
		InputSchema_: func() json.RawMessage {
			return json.RawMessage(`{}`)
		},
		Description_: func(input json.RawMessage) (string, error) {
			return "override tool", nil
		},
		IsReadOnly_:        func(input json.RawMessage) bool { return true },
		IsDestructive_:     func(input json.RawMessage) bool { return true },
		IsConcurrencySafe_: func(input json.RawMessage) bool { return true },
		IsEnabled_:         func() bool { return false },
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_:            "I am override",
		CheckPermissions_: func(input json.RawMessage, tctx *tool.ToolUseContext) types.PermissionResult {
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
		Name_: "CallTest",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
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

	tctx := &tool.ToolUseContext{ToolUseID: "orig"}
	result := &tool.ToolResult{Data: "test"}

	got := tool.ApplyContextModifier(result, tctx, false)
	if got.ToolUseID != "orig" {
		t.Errorf("ToolUseID = %q, want %q", got.ToolUseID, "orig")
	}
}

func TestApplyContextModifier_ConcurrencySafe_IgnoresModifier(t *testing.T) {
	t.Parallel()

	tctx := &tool.ToolUseContext{ToolUseID: "orig", WorkingDir: "/old"}
	result := &tool.ToolResult{
		Data: "test",
		ContextModifier: func(tctx *tool.ToolUseContext) *tool.ToolUseContext {
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

	tctx := &tool.ToolUseContext{ToolUseID: "orig", WorkingDir: "/old"}
	result := &tool.ToolResult{
		Data: "test",
		ContextModifier: func(tctx *tool.ToolUseContext) *tool.ToolUseContext {
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
		Meta:              map[string]any{"key": "val"},
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

	built := tool.BuildTool(tool.ToolDef{
		Name_: "InterfaceCheck",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return nil },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	})
	// Calling interface methods proves BuildTool's return satisfies tool.Tool.
	if built.Name() != "InterfaceCheck" {
		t.Errorf("Name() = %q, want %q", built.Name(), "InterfaceCheck")
	}
}

// ---------------------------------------------------------------------------
// Default RenderResult_ (lines 199-202) and RenderResult method (line 223)
// ---------------------------------------------------------------------------

func TestBuildTool_DefaultRenderResult(t *testing.T) {
	t.Parallel()

	// When RenderResult_ is nil, BuildTool sets a default that json.Marshal's the data.
	def := tool.ToolDef{
		Name_: "RenderTest",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
		// RenderResult_ intentionally nil — exercises default on lines 198-203
	}

	tt := tool.BuildTool(def)

	// Call RenderResult with structured data — exercises line 223
	result := tt.RenderResult(map[string]string{"key": "value"})
	expected := `{"key":"value"}`
	if result != expected {
		t.Errorf("RenderResult() = %q, want %q", result, expected)
	}
}

func TestBuildTool_DefaultRenderResult_NilData(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "NilRender",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	}

	tt := tool.BuildTool(def)
	result := tt.RenderResult(nil)
	if result != "null" {
		t.Errorf("RenderResult(nil) = %q, want %q", result, "null")
	}
}

func TestBuildTool_DefaultRenderResult_StringData(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "StrRender",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	}

	tt := tool.BuildTool(def)
	result := tt.RenderResult("hello world")
	expected := `"hello world"`
	if result != expected {
		t.Errorf("RenderResult(%q) = %q, want %q", "hello world", result, expected)
	}
}

func TestBuildTool_DefaultMaxResultSizeChars(t *testing.T) {
	t.Parallel()

	// MaxResultSizeChars is an internal field not exposed via the Tool
	// interface. It is set to 50000 by default when 0 (verified in tool.go:230-232).
	// This test confirms the tool builds without panic when the field is left zero.
	def := tool.ToolDef{
		Name_: "SizeTest",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	}

	tt := tool.BuildTool(def)
	if tt.Name() != "SizeTest" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "SizeTest")
	}
}

// ---------------------------------------------------------------------------
// ToolUseContext
// ---------------------------------------------------------------------------

func TestIsDeferred_True(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "Deferred",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
		ShouldDefer_: true,
	}

	tt := tool.BuildTool(def)
	if !tool.IsDeferred(tt) {
		t.Error("IsDeferred() = false, want true for ShouldDefer_=true")
	}
}

func TestIsDeferred_False(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "NotDeferred",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	}

	tt := tool.BuildTool(def)
	if tool.IsDeferred(tt) {
		t.Error("IsDeferred() = true, want false for default tool")
	}
}

func TestSearchHint_WithHint(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "Hinted",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
		SearchHint_:  "search files by pattern",
	}

	tt := tool.BuildTool(def)
	got := tool.SearchHint(tt)
	if got != "search files by pattern" {
		t.Errorf("SearchHint() = %q, want %q", got, "search files by pattern")
	}
}

func TestSearchHint_NoHint(t *testing.T) {
	t.Parallel()

	// minimalTool implements Tool but NOT ToolWithSearchHint
	tt := minimalTool{}
	got := tool.SearchHint(tt)
	if got != "" {
		t.Errorf("SearchHint() = %q, want empty string for tool without hint", got)
	}
}

// minimalTool implements only the Tool interface — no optional interfaces.
type minimalTool struct{}

func (minimalTool) Name() string                                { return "minimal" }
func (minimalTool) Aliases() []string                           { return nil }
func (minimalTool) Description(json.RawMessage) (string, error) { return "minimal", nil }
func (minimalTool) InputSchema() json.RawMessage                { return json.RawMessage(`{}`) }
func (minimalTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (minimalTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (minimalTool) IsReadOnly(json.RawMessage) bool           { return false }
func (minimalTool) IsDestructive(json.RawMessage) bool        { return false }
func (minimalTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (minimalTool) IsEnabled() bool                           { return true }
func (minimalTool) InterruptBehavior() tool.InterruptBehavior { return 0 }
func (minimalTool) MaxResultSize() int                        { return 50000 }
func (minimalTool) Prompt() string                            { return "" }
func (minimalTool) RenderResult(any) string                   { return "" }

func TestBuildTool_FormatWireBlocks_Override(t *testing.T) {
	t.Parallel()

	wantBlock := types.NewImageBlock(types.ImageSource{
		Type:      "base64",
		MediaType: "image/png",
		Data:      "abc",
	})
	def := tool.ToolDef{
		Name_: "WireBlocksTool",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			return []types.ContentBlock{wantBlock}
		},
	}

	tt := tool.BuildTool(def)

	wb, ok := tt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatalf("tool should implement ToolWithWireBlocks when FormatWireBlocks_ is set; got %T", tt)
	}

	blocks := wb.FormatWireBlocks("anything")
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeImage {
		t.Errorf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeImage)
	}
	if blocks[0].Source == nil {
		t.Fatalf("blocks[0].Source is nil")
	}
	if blocks[0].Source.Data != "abc" {
		t.Errorf("blocks[0].Source.Data = %q, want %q", blocks[0].Source.Data, "abc")
	}
	if blocks[0].Source.MediaType != "image/png" {
		t.Errorf("blocks[0].Source.MediaType = %q, want %q", blocks[0].Source.MediaType, "image/png")
	}
}

func TestBuildTool_NoFormatWireBlocks_NotImplements(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "NoWireBlocksTool",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	}

	tt := tool.BuildTool(def)

	if _, ok := tt.(tool.ToolWithWireBlocks); ok {
		t.Fatalf("tool should NOT implement ToolWithWireBlocks when FormatWireBlocks_ is not set; got %T", tt)
	}
}

func TestMaxResultSize_Default(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "SizeDefault",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(input json.RawMessage) (string, error) { return "", nil },
	}

	tt := tool.BuildTool(def)
	if tt.MaxResultSize() != 50000 {
		t.Errorf("MaxResultSize() = %d, want 50000", tt.MaxResultSize())
	}
}

func TestMaxResultSize_Custom(t *testing.T) {
	t.Parallel()

	def := tool.ToolDef{
		Name_: "SizeCustom",
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, nil
		},
		InputSchema_:       func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_:       func(input json.RawMessage) (string, error) { return "", nil },
		MaxResultSizeChars: 100000,
	}

	tt := tool.BuildTool(def)
	if tt.MaxResultSize() != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", tt.MaxResultSize())
	}
}

func TestToolUseContext(t *testing.T) {
	t.Parallel()

	tctx := &tool.ToolUseContext{
		ToolUseID:  "tu-ctx-1",
		WorkingDir: "/tmp",
		Options: tool.ToolUseOptions{
			Debug:   true,
			Verbose: true,
		},
	}

	if tctx.ToolUseID != "tu-ctx-1" {
		t.Errorf("ToolUseID = %q, want %q", tctx.ToolUseID, "tu-ctx-1")
	}
	if tctx.WorkingDir != "/tmp" {
		t.Errorf("WorkingDir = %q, want %q", tctx.WorkingDir, "/tmp")
	}
	if !tctx.Options.Debug {
		t.Error("Options.Debug = false, want true")
	}
	if !tctx.Options.Verbose {
		t.Error("Options.Verbose = false, want true")
	}
}
