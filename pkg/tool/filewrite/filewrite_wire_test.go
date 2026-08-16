package filewrite_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/types"
)

// writeWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func writeWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := filewrite.New().(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Write tool must implement ToolWithWireBlocks")
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

// Source: FileWriteTool.ts:418-433 — mapToolResultToToolResultBlockParam.
func TestWriteWire_ConfirmationSentences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		out  *filewrite.Output
		want string
	}{
		{
			name: "create",
			out:  &filewrite.Output{Type: filewrite.WriteTypeCreate, FilePath: "/tmp/new.go", Content: "package main\n"},
			want: "File created successfully at: /tmp/new.go",
		},
		{
			name: "update",
			out:  &filewrite.Output{Type: filewrite.WriteTypeUpdate, FilePath: "/tmp/old.go", Content: "x\n"},
			want: "The file /tmp/old.go has been updated successfully.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := writeWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

// TS's switch covers only create/update; an unknown Type (none exist today)
// falls back to JSON rather than inventing a sentence.
func TestWriteWire_UnknownTypeFallsBackToJSON(t *testing.T) {
	t.Parallel()
	out := &filewrite.Output{Type: "weird", FilePath: "/tmp/x.go"}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := writeWireText(t, out); got != string(raw) {
		t.Errorf("wire text = %q, want %q", got, string(raw))
	}
}

func TestWriteWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := writeWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestWriteDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	tt := filewrite.New()
	raw := tool.WrapSingleBlock(`{"type":"create","filePath":"/tmp/x.go","content":"hello"}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*filewrite.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *filewrite.Output", v)
	}
	if o.Type != filewrite.WriteTypeCreate || o.FilePath != "/tmp/x.go" || o.Content != "hello" {
		t.Errorf("decoded = %+v, want type=create FilePath=/tmp/x.go Content=hello", o)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero Output
// (unknown fields ignored), which replay would render as a create summary
// with no path instead of falling back to the wire text.
func TestWriteDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	tt := filewrite.New()
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestWriteDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	tt := filewrite.New()
	raw := tool.WrapSingleBlock("File created successfully at: /tmp/x.go")
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'F' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
