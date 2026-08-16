package send

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// sendWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func sendWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := New(&fakeSender{}).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Send tool must implement ToolWithWireBlocks")
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

// One-line confirmation (plan D7) — the upload details are not the LLM's
// concern, only the delivered path.
func TestSendWire_Confirmation(t *testing.T) {
	t.Parallel()
	got := sendWireText(t, &SendResult{FilePath: "/tmp/report.png", Status: "sent"})
	if want := "File sent: /tmp/report.png"; got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Non-*SendResult data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestSendWire_NonResultFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := sendWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestSendDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	tt := New(&fakeSender{})
	raw := tool.WrapSingleBlock(`{"file_path":"/tmp/x.png","status":"sent"}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	s, ok := v.(*SendResult)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *SendResult", v)
	}
	if s.FilePath != "/tmp/x.png" || s.Status != "sent" {
		t.Errorf("decoded = %+v, want FilePath=/tmp/x.png Status=sent", s)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero SendResult
// (unknown fields ignored), which replay would render as "Sent" instead of
// falling back to the wire text.
func TestSendDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	tt := New(&fakeSender{})
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestSendDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	tt := New(&fakeSender{})
	raw := tool.WrapSingleBlock("File sent: /tmp/x.png")
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'F' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
