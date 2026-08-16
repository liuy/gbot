package toolsearch_test

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/toolsearch"
	"github.com/liuy/gbot/pkg/types"
)

// toolSearchWireText drives the wire path the engine uses (FormatWireBlocks
// → single text block) and returns the block text.
func toolSearchWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := toolsearch.New().(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("ToolSearch tool must implement ToolWithWireBlocks")
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

// Source: ToolSearchTool.ts:448-470 — no-match text matches TS verbatim;
// the non-empty branch replaces TS's tool_reference blocks (gbot has no
// such ContentBlock type) with a plain-text list (plan D6).
func TestToolSearchWire_NoMatchesWithoutPendingServers(t *testing.T) {
	t.Parallel()
	got := toolSearchWireText(t, &toolsearch.Output{Matches: []string{}, Query: "xyz"})
	if want := "No matching deferred tools found"; got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

func TestToolSearchWire_NoMatchesWithPendingServers(t *testing.T) {
	t.Parallel()
	got := toolSearchWireText(t, &toolsearch.Output{
		Matches:           []string{},
		Query:             "xyz",
		PendingMCPServers: []string{"slack", "github"},
	})
	want := "No matching deferred tools found. Some MCP servers are still connecting: slack, github. Their tools will become available shortly — try searching again."
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Pending server info is deliberately absent from the non-empty branch —
// TS shows it only when search finds nothing.
func TestToolSearchWire_MatchesList(t *testing.T) {
	t.Parallel()
	got := toolSearchWireText(t, &toolsearch.Output{
		Matches:           []string{"FileRead", "FileEdit"},
		Query:             "read",
		PendingMCPServers: []string{"slack"},
	})
	want := "Found 2 tools:\n- FileRead\n- FileEdit"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Non-*Output data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestToolSearchWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := toolSearchWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestToolSearchDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	tl := toolsearch.New()
	raw := tool.WrapSingleBlock(`{"matches":["FileRead"],"query":"read","total_deferred_tools":3}`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*toolsearch.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *toolsearch.Output", v)
	}
	if len(o.Matches) != 1 || o.Matches[0] != "FileRead" || o.Query != "read" || o.TotalDeferredTools != 3 {
		t.Errorf("decoded = %+v, want matches=[FileRead] query=read total=3", o)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero Output
// (unknown fields ignored), which replay would render as the no-match text
// instead of falling back to the wire text.
func TestToolSearchDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	tl := toolsearch.New()
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestToolSearchDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	tl := toolsearch.New()
	raw := tool.WrapSingleBlock("Found 2 tools:\n- FileRead")
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'F' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
