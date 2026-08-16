package fileedit_test

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/types"
)

// editWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func editWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := fileedit.New().(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Edit tool must implement ToolWithWireBlocks")
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

// Source: FileEditTool.ts:575-594 — mapToolResultToToolResultBlockParam.
// gbot's Output has no userModified field, so the wire is always the
// userModified=false template.
func TestEditWire_ConfirmationSentences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		out  *fileedit.Output
		want string
	}{
		{
			name: "single occurrence",
			out:  &fileedit.Output{FilePath: "/tmp/test.go", OldString: "old", NewString: "new"},
			want: "The file /tmp/test.go has been updated successfully.",
		},
		{
			name: "replace all",
			out:  &fileedit.Output{FilePath: "/tmp/test.go", OldString: "old", NewString: "new", ReplaceAll: true},
			want: "The file /tmp/test.go has been updated. All occurrences were successfully replaced.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := editWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

// Non-*Output data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestEditWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := editWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestEditDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	raw := tool.WrapSingleBlock(`{"filePath":"/tmp/x.go","oldString":"a","newString":"b","replaceAll":true}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*fileedit.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *fileedit.Output", v)
	}
	if o.FilePath != "/tmp/x.go" || o.OldString != "a" || o.NewString != "b" || !o.ReplaceAll {
		t.Errorf("decoded = %+v, want FilePath=/tmp/x.go Old=a New=b ReplaceAll=true", o)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero Output
// (unknown fields ignored), which replay would render as an empty diff
// instead of falling back to the wire text.
func TestEditDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestEditDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	raw := tool.WrapSingleBlock("The file /tmp/test.go has been updated successfully.")
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'T' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
