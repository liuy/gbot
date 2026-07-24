package wui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// decodeMockTool is a minimal tool.Tool + ToolWithDecodeResult for render tests.
type decodeMockTool struct {
	renderFn func(any) string
	decodeFn func(json.RawMessage) (any, error)
}

func (m *decodeMockTool) Name() string                                { return "Bash" }
func (m *decodeMockTool) Aliases() []string                           { return nil }
func (m *decodeMockTool) Description(json.RawMessage) (string, error) { return "", nil }
func (m *decodeMockTool) InputSchema() json.RawMessage                { return nil }
func (m *decodeMockTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *decodeMockTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *decodeMockTool) RenderResult(data any) string { return m.renderFn(data) }
func (m *decodeMockTool) DecodeResult(raw json.RawMessage) (any, error) {
	return m.decodeFn(raw)
}
func (m *decodeMockTool) IsReadOnly(json.RawMessage) bool           { return true }
func (m *decodeMockTool) IsDestructive(json.RawMessage) bool        { return false }
func (m *decodeMockTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (m *decodeMockTool) IsEnabled() bool                           { return true }
func (m *decodeMockTool) InterruptBehavior() tool.InterruptBehavior { return 0 }
func (m *decodeMockTool) MaxResultSize() int                        { return 0 }
func (m *decodeMockTool) Prompt() string                            { return "" }

// stringResult is the concrete type decodeMockTool decodes from JSON.
type stringResult struct {
	Text string `json:"text"`
}

func TestRenderViaTool_DecodeResultPath(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Bash": &decodeMockTool{
			renderFn: func(data any) string {
				out, ok := data.(*stringResult)
				if !ok {
					return "FALLBACK"
				}
				return out.Text
			},
			decodeFn: func(raw json.RawMessage) (any, error) {
				var r stringResult
				if err := json.Unmarshal(raw, &r); err != nil {
					return nil, err
				}
				return &r, nil
			},
		},
	}
	got, ok := renderViaTool("Bash", json.RawMessage(`{"text":"hello world"}`), tools)
	if !ok {
		t.Fatal("renderViaTool(Bash) returned ok=false, want true")
	}
	if got != "hello world" {
		t.Errorf("renderViaTool(Bash) = %q, want %q", got, "hello world")
	}
}

func TestRenderViaTool_ToolNotInMap(t *testing.T) {
	t.Parallel()
	got, ok := renderViaTool("Missing", json.RawMessage(`{}`), nil)
	if ok {
		t.Error("renderViaTool(Missing) returned ok=true, want false")
	}
	if got != "" {
		t.Errorf("renderViaTool(Missing) = %q, want empty", got)
	}
}

func TestRenderViaTool_DecodeError_FallsBackToString(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Bash": &decodeMockTool{
			renderFn: func(data any) string {
				s, ok := data.(string)
				if !ok {
					return "NON_STRING_FALLBACK"
				}
				return s
			},
			decodeFn: func(raw json.RawMessage) (any, error) {
				return nil, errors.New("decode error")
			},
		},
	}
	got, ok := renderViaTool("Bash", json.RawMessage(`garbage`), tools)
	if !ok {
		t.Fatal("renderViaTool(Bash) returned ok=false, want true")
	}
	if got != "garbage" {
		t.Errorf("renderViaTool(Bash, decode error) = %q, want %q", got, "garbage")
	}
}

func TestRenderToolOutput_DecodeResultPath(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Bash": &decodeMockTool{
			renderFn: func(data any) string {
				out, ok := data.(*stringResult)
				if !ok {
					return "FALLBACK"
				}
				return out.Text
			},
			decodeFn: func(raw json.RawMessage) (any, error) {
				var r stringResult
				if err := json.Unmarshal(raw, &r); err != nil {
					return nil, err
				}
				return &r, nil
			},
		},
	}
	// Array-form content: JSON string with tool result in the text.
	inner := `{"text":"hello"}`
	textJSON, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got := renderToolOutput("Bash", raw, tools)
	if got != "hello" {
		t.Errorf("renderToolOutput = %q, want %q", got, "hello")
	}
}

func TestRenderToolOutput_EmptyContent(t *testing.T) {
	t.Parallel()
	got := renderToolOutput("Bash", nil, nil)
	if got != "" {
		t.Errorf("renderToolOutput(empty) = %q, want empty", got)
	}
}

// TestRenderToolOutput_ArrayFormTextBlock verifies the WUI connector's
// array-form path passes through text blocks verbatim.
func TestRenderToolOutput_ArrayFormTextBlock(t *testing.T) {
	t.Parallel()
	arrayInput := json.RawMessage(`[{"type":"text","text":"hello"}]`)
	got := renderToolOutput("Bash", arrayInput, nil)
	if got != "hello" {
		t.Errorf("renderToolOutput array-form = %q, want %q", got, "hello")
	}
}

func TestRenderToolOutput_ArrayFormNoPrefix(t *testing.T) {
	t.Parallel()
	arrayInput := json.RawMessage(`[{"type":"text","text":"hello world"}]`)
	got := renderToolOutput("Bash", arrayInput, nil)
	if got != "hello world" {
		t.Errorf("renderToolOutput = %q, want %q", got, "hello world")
	}
}

// TestRenderToolOutput_AgentMarkdownNotJSONWrapped verifies that plain
// markdown agent tool results pass through renderViaTool without being
// re-encoded as JSON strings.
func TestRenderToolOutput_AgentMarkdownNotJSONWrapped(t *testing.T) {
	t.Parallel()
	agentTool := &decodeMockTool{
		renderFn: func(data any) string {
			// Mimic AgentTool.RenderResult: SubQueryResult returns Content;
			// anything else falls back to json.Marshal which is the bug.
			if r, ok := data.(*types.SubQueryResult); ok {
				return r.Content
			}
			b, _ := json.Marshal(data)
			return string(b)
		},
		decodeFn: func(raw json.RawMessage) (any, error) {
			// Mimic AgentTool.DecodeResult: expects SubQueryResult JSON.
			// Plain markdown text will fail to decode.
			var r types.SubQueryResult
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			return &r, nil
		},
	}
	tools := map[string]tool.Tool{"Agent": agentTool}

	// Simulate the persisted format: JSON string wrapping plain markdown.
	plain := "## 统计结果\n\n| package | lines |\n|---|---|\n| pkg/tui | 100 |"
	textJSON, _ := json.Marshal(plain)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got := renderToolOutput("Agent", raw, tools)

	// Expected: markdown preserved verbatim.
	want := "## 统计结果\n\n| package | lines |\n|---|---|\n| pkg/tui | 100 |"
	if got != want {
		t.Errorf("renderToolOutput agent markdown:\n got = %q\n want = %q", got, want)
	}
}
