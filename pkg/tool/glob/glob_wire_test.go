package glob_test

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/types"
)

// globWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func globWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := glob.New().(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Glob tool must implement ToolWithWireBlocks")
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

// Source: GlobTool.ts:177-197 — mapToolResultToToolResultBlockParam.
func TestGlobWire_EmptyIsNoFilesFound(t *testing.T) {
	t.Parallel()
	if got := globWireText(t, &glob.Output{Files: []string{}, Count: 0}); got != "No files found" {
		t.Errorf("wire text = %q, want %q", got, "No files found")
	}
}

func TestGlobWire_FilesOnePerLine(t *testing.T) {
	t.Parallel()
	want := "src/a.go\nsrc/b.go\nsrc/c.go"
	if got := globWireText(t, &glob.Output{
		Files: []string{"src/a.go", "src/b.go", "src/c.go"},
		Count: 3,
	}); got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

func TestGlobWire_TruncatedAppendsHintLine(t *testing.T) {
	t.Parallel()
	want := "a.go\n(Results are truncated. Consider using a more specific path or pattern.)"
	if got := globWireText(t, &glob.Output{
		Files:     []string{"a.go"},
		Count:     1,
		Truncated: true,
	}); got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Non-*Output data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestGlobWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := globWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestGlobDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	tl := glob.New()
	raw := tool.WrapSingleBlock(`{"filenames":["a.go","b.go"],"numFiles":2,"durationMs":5,"truncated":false}`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*glob.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *glob.Output", v)
	}
	if len(o.Files) != 2 || o.Files[0] != "a.go" || o.Files[1] != "b.go" {
		t.Errorf("Files = %v, want [a.go b.go]", o.Files)
	}
	if o.Count != 2 {
		t.Errorf("Count = %d, want 2", o.Count)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero Output
// (unknown fields ignored), which replay would render as empty instead of
// falling back to the wire text — same false-positive class the other
// wire-plaintext tools guard against.
func TestGlobDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	tl := glob.New()
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

// All-zero legacy wire (empty glob with sub-millisecond duration) is
// indistinguishable from any other JSON object — rejecting it is the
// accepted replay degradation for this shape.
func TestGlobDecodeResult_RejectsAllZeroLegacyWire(t *testing.T) {
	t.Parallel()
	tl := glob.New()
	raw := tool.WrapSingleBlock(`{"filenames":[],"numFiles":0,"durationMs":0,"truncated":false}`)
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject all-zero output (no identifying fields)")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestGlobDecodeResult_RejectsNonJSONWire(t *testing.T) {
	t.Parallel()
	tl := glob.New()
	raw := tool.WrapSingleBlock("No files found")
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'N' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
