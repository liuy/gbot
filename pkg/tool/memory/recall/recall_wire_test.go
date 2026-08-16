package recall

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// recallWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func recallWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := New(nil).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Recall tool must implement ToolWithWireBlocks")
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

// The empty-search hint rides in messages[] for the LLM — on the wire it
// goes out bare, without the numbered-block framing.
func TestRecallWire_EmptyHintVerbatim(t *testing.T) {
	t.Parallel()
	got := recallWireText(t, &Output{Messages: []msgHit{{Content: emptyHint}}})
	if got != emptyHint {
		t.Errorf("wire text = %q, want %q", got, emptyHint)
	}
}

func TestRecallWire_NoMessages(t *testing.T) {
	t.Parallel()
	if got := recallWireText(t, &Output{Messages: []msgHit{}}); got != "No matches found." {
		t.Errorf("wire text = %q, want %q", got, "No matches found.")
	}
}

// Score and UUID ride on the header line: the LLM needs the UUID for
// uuid-mode follow-up reads (the schema documents that flow) and the score
// to judge match confidence — the old JSON wire carried both.
func TestRecallWire_MultiEntryWithScoreAndUUID(t *testing.T) {
	t.Parallel()
	got := recallWireText(t, &Output{Messages: []msgHit{
		{UUID: "u-1", Content: "first hit", Date: "2026-01-02 15:04", Score: 1.25},
		{UUID: "u-2", Content: "second\nhit lines", Date: "2026-01-03 16:05"},
	}})
	want := "score 1.25  1. 2026-01-02 15:04  uuid u-1\n" +
		"   first hit\n" +
		"\n" +
		"2. 2026-01-03 16:05  uuid u-2\n" +
		"   second\n" +
		"   hit lines"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// uuid mode has Score==0 (no relevance concept) — header carries no score
// prefix (avoids "score 0.00" noise, mirroring the render rule).
func TestRecallWire_UUIDModeHasNoScorePrefix(t *testing.T) {
	t.Parallel()
	got := recallWireText(t, &Output{Messages: []msgHit{
		{UUID: "u-9", Content: "full message text", Date: "2026-05-06 07:08"},
	}})
	want := "1. 2026-05-06 07:08  uuid u-9\n   full message text"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Trailing-newline content must not leave a stray 3-space line at the end
// of an entry (same reason the render trims per block).
func TestRecallWire_TrailingNewlineContentTrimmed(t *testing.T) {
	t.Parallel()
	got := recallWireText(t, &Output{Messages: []msgHit{
		{UUID: "u-1", Content: "line\n", Date: "2026-01-02 15:04", Score: 1},
	}})
	want := "score 1.00  1. 2026-01-02 15:04  uuid u-1\n   line"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Non-*Output data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestRecallWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := recallWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestRecallDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	r := New(nil)
	raw := tool.WrapSingleBlock(`{"messages":[{"uuid":"u-1","content":"hello","date":"2026-01-01","score":1.5}]}`)
	v, err := r.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *Output", v)
	}
	if len(o.Messages) != 1 || o.Messages[0].UUID != "u-1" || o.Messages[0].Content != "hello" || o.Messages[0].Score != 1.5 {
		t.Errorf("decoded = %+v, want one hit uuid=u-1 content=hello score=1.5", o)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero Output
// (unknown fields ignored), which replay would render as
// "No matches found." instead of falling back to the wire text.
func TestRecallDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	r := New(nil)
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := r.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestRecallDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	r := New(nil)
	raw := tool.WrapSingleBlock("No matches found.")
	_, err := r.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'N' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
