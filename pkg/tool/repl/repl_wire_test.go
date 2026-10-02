package repl

import (
	"encoding/json"
	"strings"
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

// A structured result renders as one text block followed by one block per image.
func TestREPLTool_FormatWireBlocks_ReplResultWithImages(t *testing.T) {
	t.Parallel()

	data := replResult{
		Text: "out\n",
		Images: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "aGk="}),
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "eW8="}),
		},
	}
	blocks := New().FormatWireBlocks(data)
	if len(blocks) != 3 {
		t.Fatalf("len(blocks) = %d, want 3", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "out\n" {
		t.Errorf("blocks[0] = %+v, want text 'out\\n'", blocks[0])
	}
	wantMedia := []string{"image/png", "image/jpeg"}
	for i, want := range wantMedia {
		b := blocks[i+1]
		if b.Type != types.ContentTypeImage {
			t.Errorf("blocks[%d].Type = %q, want image", i+1, b.Type)
		}
		if b.Source == nil {
			t.Fatalf("blocks[%d] missing source", i+1)
		}
		if b.Source.MediaType != want {
			t.Errorf("blocks[%d] media = %q, want %q", i+1, b.Source.MediaType, want)
		}
	}
}

func TestREPLTool_FormatWireBlocks_ReplResultTextOnly(t *testing.T) {
	t.Parallel()

	blocks := New().FormatWireBlocks(replResult{Text: "2\n"})
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "2\n" {
		t.Errorf("blocks[0] = %+v, want text '2\\n'", blocks[0])
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

// New wires with image evidence must round-trip back into the structured form.
func TestREPLTool_DecodeResult_ImageWireRoundTrip(t *testing.T) {
	t.Parallel()

	data := replResult{
		Text: "shot",
		Images: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "aGk="}),
		},
	}
	raw, err := json.Marshal(New().FormatWireBlocks(data))
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	v, err := New().DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(replResult)
	if !ok {
		t.Fatalf("DecodeResult type = %T, want replResult", v)
	}
	if got.Text != "shot" {
		t.Errorf("Text = %q, want 'shot'", got.Text)
	}
	if len(got.Images) != 1 {
		t.Fatalf("len(Images) = %d, want 1", len(got.Images))
	}
	src := got.Images[0].Source
	if src == nil || src.Type != "base64" || src.MediaType != "image/png" || src.Data != "aGk=" {
		t.Errorf("image source = %+v, want base64 png 'aGk='", src)
	}
}

// An image-only-bearing wire without a text block must be rejected.
func TestREPLTool_DecodeResult_NoTextBlockRejected(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]`)
	if _, err := New().DecodeResult(raw); err == nil {
		t.Error("DecodeResult must reject a wire with no text block")
	}
}

// A wire carrying two text blocks must decode to the first one only.
func TestREPLTool_DecodeResult_FirstTextBlockWins(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`)
	v, err := New().DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	s, ok := v.(string)
	if !ok || s != "first" {
		t.Errorf("DecodeResult = %#v, want \"first\"", v)
	}
}

// Array-shaped but unparseable JSON must surface the unmarshal error.
func TestREPLTool_DecodeResult_MalformedArrayRejected(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[{"type":"text",`)
	if _, err := New().DecodeResult(raw); err == nil {
		t.Error("DecodeResult must reject malformed array JSON")
	}
}

// Long non-array input is rejected with an 80-rune preview in the message.
func TestREPLTool_DecodeResult_LongPreviewTruncated(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(strings.Repeat("x", 200))
	_, err := New().DecodeResult(raw)
	if err == nil {
		t.Fatal("expected error for non-array input")
	}
	if !strings.HasPrefix(err.Error(), `repl: DecodeResult expects array-form content, got "xxx`) {
		t.Errorf("error must carry a preview, got %v", err)
	}
	// The 200-char input must be cut to the 80-rune preview: 81 consecutive
	// x's prove no truncation happened.
	if strings.Contains(err.Error(), strings.Repeat("x", 81)) {
		t.Errorf("preview must be truncated to 80 runes, got %v", err)
	}
}
