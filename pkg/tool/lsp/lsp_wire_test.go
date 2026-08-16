package lsptool

import (
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func lspWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := New(nil).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Lsp tool must implement ToolWithWireBlocks")
	}
	blocks := wb.FormatWireBlocks(data)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	return blocks[0].Text
}

// Source: LSPTool.ts:415-421 — wire content is output.result; gbot's Data
// is exactly that result string, so it passes through verbatim.
func TestLspWire_StringPassesThrough(t *testing.T) {
	t.Parallel()

	if got := lspWireText(t, "pkg/a.go:10:5 func main"); got != "pkg/a.go:10:5 func main" {
		t.Errorf("wire text = %q, want pass-through", got)
	}
	if got := lspWireText(t, "No references found\n"); got != "No references found\n" {
		t.Errorf("wire text = %q, want %q", got, "No references found\n")
	}
}

func TestLspWire_NonStringFallsBackToJSON(t *testing.T) {
	t.Parallel()

	if got := lspWireText(t, 42); got != "42" {
		t.Errorf("wire text = %q, want %q", got, "42")
	}
}

// Old sessions stored the JSON-encoded string (default wire); new sessions
// store the raw result text. The string probe decodes both to the same
// string on replay.
func TestLsp_DecodeResult_LegacyAndPlainWireAgree(t *testing.T) {
	t.Parallel()

	tt := New(nil)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(`"done"`))
	if err != nil {
		t.Fatalf("DecodeResult(legacy): %v", err)
	}
	if s, ok := v.(string); !ok || s != "done" {
		t.Errorf("DecodeResult(legacy) = %#v, want \"done\"", v)
	}

	v, err = tt.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock("No references found"))
	if err != nil {
		t.Fatalf("DecodeResult(plain): %v", err)
	}
	if s, ok := v.(string); !ok || s != "No references found" {
		t.Errorf("DecodeResult(plain) = %#v, want %q", v, "No references found")
	}
}
