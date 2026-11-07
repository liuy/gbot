package web

import (
	"encoding/json"
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
	want := `{"Mode":"fetch","Content":"x","Raw":null,"Image":null}`
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

// An image-bearing fetch result wires as [text, image] — same block order as
// Computer's screenshot — so the model gets the summary text plus the inline
// image block, and image-skipping consumers (persist, budget) keep working.
func TestWebWire_ImageOutputEmitsTextAndImageBlocks(t *testing.T) {
	t.Parallel()

	out := &Output{
		Mode:    "fetch",
		Content: "[note] Fetched image binary\n\nFetched image content (image/png).",
		Image:   &FetchImage{Base64: "aGk=", MimeType: "image/png"},
	}
	wb, ok := New(nil).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Web tool must implement ToolWithWireBlocks")
	}
	blocks := wb.FormatWireBlocks(out)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2 (text + image)", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != out.Content {
		t.Errorf("blocks[0] = (%q, %q), want text block with the summary", blocks[0].Type, blocks[0].Text)
	}
	if blocks[1].Type != types.ContentTypeImage {
		t.Fatalf("blocks[1].Type = %q, want %q", blocks[1].Type, types.ContentTypeImage)
	}
	src := blocks[1].Source
	if src == nil || src.Type != "base64" || src.MediaType != "image/png" || src.Data != "aGk=" {
		t.Errorf("blocks[1].Source = %+v, want base64 image/png aGk=", src)
	}
}

// Replay: the [text, image] wire form must decode back into an Output that
// still carries the image, so persisted sessions re-render with the block.
func TestWebDecodeResult_ImageWireRoundTrip(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	raw := json.RawMessage(`[{"type":"text","text":"Fetched image content (image/png)."},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *Output", v)
	}
	if o.Content != "Fetched image content (image/png)." {
		t.Errorf("Content = %q, want the summary text", o.Content)
	}
	if o.Mode != "fetch" {
		t.Errorf("Mode = %q, want fetch", o.Mode)
	}
	if o.Image == nil {
		t.Fatal("decoded Output.Image = nil, want the image block")
	}
	if o.Image.MimeType != "image/png" || o.Image.Base64 != "aGk=" {
		t.Errorf("Image = %+v, want base64 image/png aGk=", o.Image)
	}
}

// Empty and non-array wire content must stay on the legacy path — the image
// unwrap's array-shape guard is what keeps them from reaching the block
// parser (an empty RawMessage would panic on its first byte).
func TestWebDecodeResult_EmptyAndNonArrayRaw(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	for name, raw := range map[string]json.RawMessage{
		"empty":     json.RawMessage(""),
		"object":    json.RawMessage(`{"Mode":"fetch"}`),
		"bare text": json.RawMessage(`"x"`),
	} {
		_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
		if err == nil {
			t.Errorf("%s: DecodeResult must reject %q", name, raw)
			continue
		}
		if !strings.Contains(err.Error(), "expects array-form content") {
			t.Errorf("%s: error = %q, want the array-form complaint", name, err.Error())
		}
	}
}

// encoding/json fills the slice up to the failing element, so a wire with a
// valid image block followed by a malformed one must NOT decode as an image
// result — the unmarshal-error guard is what stops the half-parsed image
// from leaking through.
func TestWebDecodeResult_MalformedArrayFallsBackToLegacyError(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	raw := json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}},{"type":123}]`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatalf("DecodeResult must reject the malformed array, got %+v", v)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("error = %q, want the json type error from the legacy path", err.Error())
	}
}

// An image block without a source carries no payload; the unwrap skips it
// and the wire then has no text block either, so the legacy path errors.
func TestWebDecodeResult_SourcelessImageIgnored(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	raw := json.RawMessage(`[{"type":"image"}]`)
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject a sourceless image-only wire")
	}
	if !strings.Contains(err.Error(), "found no text block") {
		t.Errorf("error = %q, want the no-text-block complaint", err.Error())
	}
}

// A nil-source image block must be skipped, not treated as terminal: later
// blocks in the same wire still decode (a continue→break rewrite drops the
// good image that follows).
func TestWebDecodeResult_NilSourceImageSkippedNotTerminal(t *testing.T) {
	t.Parallel()
	tl := New(nil)
	raw := json.RawMessage(`[{"type":"image"},{"type":"image","source":{"type":"base64","media_type":"image/gif","data":"aGk="}}]`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *Output", v)
	}
	if o.Image == nil {
		t.Fatal("decoded Output.Image = nil, want the image block after the nil-source one")
	}
	if o.Image.MimeType != "image/gif" || o.Image.Base64 != "aGk=" {
		t.Errorf("Image = %+v, want base64 image/gif aGk=", o.Image)
	}
	if o.Mode != "fetch" {
		t.Errorf("Mode = %q, want fetch", o.Mode)
	}
}
