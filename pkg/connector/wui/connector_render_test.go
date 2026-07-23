package wui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
	// Simulate persisted tool_result content: JSON string with [Tool spent] prefix.
	inner := `{"text":"hello"}`
	wrapped, _ := json.Marshal("[Tool spent 1.5s]" + inner)
	got, elapsed := renderToolOutput("Bash", wrapped, tools)
	if got != "hello" {
		t.Errorf("renderToolOutput = %q, want %q", got, "hello")
	}
	if elapsed != int64(1500*time.Millisecond) {
		t.Errorf("elapsed = %d, want %d", elapsed, int64(1500*time.Millisecond))
	}
}

func TestRenderToolOutput_EmptyContent(t *testing.T) {
	t.Parallel()
	got, elapsed := renderToolOutput("Bash", nil, nil)
	if got != "" {
		t.Errorf("renderToolOutput(empty) = %q, want empty", got)
	}
	if elapsed != 0 {
		t.Errorf("elapsed = %d, want 0", elapsed)
	}
}

// TestRenderToolOutput_ArrayFormDuration verifies the WUI connector's
// array-form path extracts the duration prefix from the first text block
// and strips it from the displayed text. Mirrors the TUI behavior in
// pkg/tui/app_internal_test.go. Bug-catch: previous code returned
// ("[Tool spent 1.5s]hello", 0).
func TestRenderToolOutput_ArrayFormDuration(t *testing.T) {
	t.Parallel()
	arrayInput := json.RawMessage(`[{"type":"text","text":"[Tool spent 1.5s]hello"}]`)
	got, elapsed := renderToolOutput("Bash", arrayInput, nil)
	if got != "hello" {
		t.Errorf("renderToolOutput array-form = %q, want %q (prefix stripped)", got, "hello")
	}
	if elapsed != int64(1500*time.Millisecond) {
		t.Errorf("elapsed = %d, want %d", elapsed, int64(1500*time.Millisecond))
	}
}

func TestRenderToolOutput_ArrayFormNoPrefix(t *testing.T) {
	t.Parallel()
	arrayInput := json.RawMessage(`[{"type":"text","text":"hello world"}]`)
	got, elapsed := renderToolOutput("Bash", arrayInput, nil)
	if got != "hello world" {
		t.Errorf("renderToolOutput = %q, want %q", got, "hello world")
	}
	if elapsed != 0 {
		t.Errorf("elapsed = %d, want 0", elapsed)
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
	plain := "[Tool spent 38.3s]## 统计结果\n\n| package | lines |\n|---|---|\n| pkg/tui | 100 |"
	wrapped, _ := json.Marshal(plain)
	got, _ := renderToolOutput("Agent", wrapped, tools)

	// Expected: duration prefix stripped, markdown preserved verbatim.
	want := "## 统计结果\n\n| package | lines |\n|---|---|\n| pkg/tui | 100 |"
	if got != want {
		t.Errorf("renderToolOutput agent markdown:\n got = %q\n want = %q", got, want)
	}
}
