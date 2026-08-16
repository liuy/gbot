package web

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// webWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func webWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := New(nil).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Web tool must implement ToolWithWireBlocks")
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

// Source: WebFetchTool.ts:300-306 — wire content is the processed result
// string verbatim. gbot's Output.Content holds that text for both modes of
// the merged search+fetch tool.
func TestWebWire_ContentPassesThrough(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  *Output
		want string
	}{
		{
			name: "fetch markdown",
			out:  &Output{Mode: "fetch", Content: "# Title\n\nbody text"},
			want: "# Title\n\nbody text",
		},
		{
			name: "search results",
			out:  &Output{Mode: "search", Content: "Results 1-3 of 3\n1. example.com"},
			want: "Results 1-3 of 3\n1. example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := webWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

// Non-*Output data (including the value form) keeps the JSON fallback so
// anything DecodeResult reconstructs still serializes instead of panicking.
func TestWebWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()

	if got := webWireText(t, "x"); got != "\"x\"" {
		t.Errorf("wire text = %q, want %q", got, "\"x\"")
	}
	want := `{"Mode":"fetch","Content":"x","Raw":null}`
	if got := webWireText(t, Output{Mode: "fetch", Content: "x"}); got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

func TestWebDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	raw := tool.WrapSingleBlock(`{"Mode":"fetch","Content":"# hello"}`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *Output", v)
	}
	if o.Mode != "fetch" || o.Content != "# hello" {
		t.Errorf("decoded output = %+v, want Mode=fetch Content=# hello", o)
	}
}

// Wire text that is itself a JSON object (fetching a raw JSON API endpoint
// without markdown conversion) would decode into an all-zero Output and
// render empty on replay; the identifying-fields check must reject it so
// replay falls back to showing the wire text.
func TestWebDecodeResult_RejectsJSONObjectText(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	for _, wire := range []string{
		`{"name":"gbot","version":"1.0"}`,
		`{}`,
	} {
		raw := tool.WrapSingleBlock(wire)
		_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
		if err == nil {
			t.Errorf("DecodeResult(%s) must reject JSON-object text lacking identifying fields", wire)
			continue
		}
		if !strings.Contains(err.Error(), "identifying fields") {
			t.Errorf("error = %q, want it to mention identifying fields", err.Error())
		}
	}
}

func TestWebDecodeResult_RejectsPlainText(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	raw := tool.WrapSingleBlock("# Page content")
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON plain text")
	}
	if !strings.Contains(err.Error(), "invalid character '#' looking for beginning of value") {
		t.Errorf("error = %q, want json syntax error for '#'", err.Error())
	}
}
