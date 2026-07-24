package wui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
				text, err := tool.UnmarshalSingleBlock(raw)
				if err != nil {
					return nil, err
				}
				var r stringResult
				if err := json.Unmarshal([]byte(text), &r); err != nil {
					return nil, err
				}
				return &r, nil
			},
		},
	}
	raw := json.RawMessage(`[{"type":"text","text":"{\"text\":\"hello world\"}"}]`)
	got, ok := renderViaTool("Bash", raw, tools)
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

func TestRenderViaTool_DecodeErrorReturnsFalse(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{
		"Bash": &decodeMockTool{
			renderFn: func(data any) string { return "SHOULD_NOT_BE_CALLED" },
			decodeFn: func(raw json.RawMessage) (any, error) {
				return nil, errors.New("decode error")
			},
		},
	}
	got, ok := renderViaTool("Bash", json.RawMessage(`garbage`), tools)
	if ok {
		t.Error("renderViaTool(Bash) returned ok=true, want false on decode error")
	}
	if got != "" {
		t.Errorf("renderViaTool(Bash, decode error) = %q, want empty", got)
	}
}

func TestRenderToolOutput_PassesRawArrayToDecodeResult(t *testing.T) {
	t.Parallel()
	var capturedRaw json.RawMessage
	tools := map[string]tool.Tool{
		"Bash": &decodeMockTool{
			renderFn: func(data any) string { return "ok" },
			decodeFn: func(raw json.RawMessage) (any, error) {
				capturedRaw = append(capturedRaw[:0], raw...)
				return &stringResult{Text: "ok"}, nil
			},
		},
	}
	input := json.RawMessage(`[{"type":"text","text":"hello"}]`)
	if _, ok := renderViaTool("Bash", input, tools); !ok {
		t.Fatal("renderViaTool returned ok=false")
	}
	// The mock must receive the raw array bytes verbatim — not unwrapped text.
	if string(capturedRaw) != string(input) {
		t.Errorf("DecodeResult received %q, want raw array %q", string(capturedRaw), string(input))
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
				text, err := tool.UnmarshalSingleBlock(raw)
				if err != nil {
					return nil, err
				}
				var r stringResult
				if err := json.Unmarshal([]byte(text), &r); err != nil {
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

// TestRenderToolOutput_ComputerDeviceInfoRenders verifies that a Computer
// DeviceInfo payload (array-form) renders via DecodeResult and produces
// friendly output instead of raw JSON.
func TestRenderToolOutput_ComputerDeviceInfoRenders(t *testing.T) {
	t.Parallel()
	type deviceInfo struct {
		Manufacturer string `json:"Manufacturer"`
		Model        string `json:"Model"`
	}
	computerMock := &decodeMockTool{
		renderFn: func(data any) string {
			d, ok := data.(*deviceInfo)
			if !ok {
				return "FALLBACK"
			}
			return d.Manufacturer + " " + d.Model
		},
		decodeFn: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var d deviceInfo
			if err := json.Unmarshal([]byte(text), &d); err != nil {
				return nil, err
			}
			return &d, nil
		},
	}
	tools := map[string]tool.Tool{"Computer": computerMock}
	inner := `{"action":"device_info","ok":true,"Manufacturer":"HONOR","Model":"BKQ-AN80"}`
	textJSON, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got := renderToolOutput("Computer", raw, tools)
	if got != "HONOR BKQ-AN80" {
		t.Errorf("renderToolOutput(Computer) = %q, want friendly %q", got, "HONOR BKQ-AN80")
	}
	if strings.Contains(got, `"Manufacturer"`) {
		t.Errorf("renderToolOutput(Computer) leaked raw JSON: %s", got)
	}
}

// TestRenderToolOutput_PersistedFileWrapsContent verifies that persisted-file
// content (bare inner text on disk) gets re-wrapped in array form before
// being fed to DecodeResult.
func TestRenderToolOutput_PersistedFileWrapsContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "result.json")
	fileContent := `{"text":"rendered from file"}`
	if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var capturedRaw json.RawMessage
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
				capturedRaw = append(capturedRaw[:0], raw...)
				text, err := tool.UnmarshalSingleBlock(raw)
				if err != nil {
					return nil, err
				}
				var r stringResult
				if err := json.Unmarshal([]byte(text), &r); err != nil {
					return nil, err
				}
				return &r, nil
			},
		},
	}
	input := "<persisted-output>\nFull output saved to: " + filePath + "\nPreview (first 5 lines):\nline1"
	textJSON, _ := json.Marshal(input)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got := renderToolOutput("Bash", raw, tools)
	if got != "rendered from file" {
		t.Fatalf("renderToolOutput(persisted) = %q, want %q", got, "rendered from file")
	}
	// The mock must have received array form (with bare struct JSON inside the
	// text block), not bare struct.
	if len(capturedRaw) == 0 || capturedRaw[0] != '[' {
		t.Errorf("DecodeResult received non-array input %q", string(capturedRaw))
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
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var r types.SubQueryResult
			if err := json.Unmarshal([]byte(text), &r); err != nil {
				return nil, err
			}
			return &r, nil
		},
	}
	tools := map[string]tool.Tool{"Agent": agentTool}

	// Agent markdown wire format: text block wraps a JSON-encoded
	// SubQueryResult whose Content is the markdown.
	plain := "## 统计结果\n\n| package | lines |\n|---|---|\n| pkg/tui | 100 |"
	inner := map[string]any{"content": plain}
	innerBytes, _ := json.Marshal(inner)
	textJSON, _ := json.Marshal(string(innerBytes))
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got := renderToolOutput("Agent", raw, tools)

	// Expected: markdown preserved verbatim.
	want := "## 统计结果\n\n| package | lines |\n|---|---|\n| pkg/tui | 100 |"
	if got != want {
		t.Errorf("renderToolOutput agent markdown:\n got = %q\n want = %q", got, want)
	}
}
