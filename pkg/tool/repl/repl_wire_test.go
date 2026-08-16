package repl

import (
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func replWireText(t *testing.T, data any) string {
	t.Helper()
	blocks := New().FormatWireBlocks(data)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	return blocks[0].Text
}

// TS REPLTool has no mapToolResult override, so the wire is the raw
// execution output; gbot's Data is already that output string.
func TestREPLTool_FormatWireBlocks_String(t *testing.T) {
	t.Parallel()

	if got := replWireText(t, "2\n"); got != "2\n" {
		t.Errorf("wire text = %q, want %q", got, "2\n")
	}
	if got := replWireText(t, "line1\nline2\n"); got != "line1\nline2\n" {
		t.Errorf("wire text = %q, want %q", got, "line1\nline2\n")
	}
}

func TestREPLTool_FormatWireBlocks_NonStringFallsBackToJSON(t *testing.T) {
	t.Parallel()

	if got := replWireText(t, 42); got != "42" {
		t.Errorf("wire text = %q, want %q", got, "42")
	}
}

// Old sessions stored the JSON-encoded string (default wire); new sessions
// store the raw output. Both must decode to the same string on replay.
func TestREPLTool_DecodeResult_LegacyAndPlainWireAgree(t *testing.T) {
	t.Parallel()

	r := New()
	v, err := r.DecodeResult(tool.WrapSingleBlock(`"2\n"`))
	if err != nil {
		t.Fatalf("DecodeResult(legacy): %v", err)
	}
	if s, ok := v.(string); !ok || s != "2\n" {
		t.Errorf("DecodeResult(legacy) = %#v, want \"2\\n\"", v)
	}

	v, err = r.DecodeResult(tool.WrapSingleBlock("2\n"))
	if err != nil {
		t.Fatalf("DecodeResult(plain): %v", err)
	}
	if s, ok := v.(string); !ok || s != "2\n" {
		t.Errorf("DecodeResult(plain) = %#v, want \"2\\n\"", v)
	}
}
