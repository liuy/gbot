package grep_test

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/types"
)

// grepWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func grepWireText(t *testing.T, data any) string {
	t.Helper()
	tt := grep.New()
	wb, ok := tt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Grep tool must implement ToolWithWireBlocks")
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

func TestGrepWire_ContentMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  *grep.Output
		want string
	}{
		{
			name: "match",
			out:  &grep.Output{Mode: "content", Content: "a.go:1:hello"},
			want: "a.go:1:hello",
		},
		{
			name: "no match",
			out:  &grep.Output{Mode: "content", Content: ""},
			want: "No matches found",
		},
		{
			name: "limit",
			out:  &grep.Output{Mode: "content", Content: "a.go:1:hello", AppliedLimit: new(250)},
			want: "a.go:1:hello\n\n[Showing results with pagination = limit: 250]",
		},
		{
			name: "limit and offset",
			out:  &grep.Output{Mode: "content", Content: "a.go:1:hello", AppliedLimit: new(250), AppliedOffset: new(10)},
			want: "a.go:1:hello\n\n[Showing results with pagination = limit: 250, offset: 10]",
		},
		{
			name: "offset only",
			out:  &grep.Output{Mode: "content", Content: "a.go:1:hello", AppliedOffset: new(10)},
			want: "a.go:1:hello\n\n[Showing results with pagination = offset: 10]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := grepWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGrepWire_CountMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  *grep.Output
		want string
	}{
		{
			name: "plural",
			out:  &grep.Output{Mode: "count", NumMatches: 5, NumFiles: 2, Content: "a.go:3\nb.go:2"},
			want: "a.go:3\nb.go:2\n\nFound 5 total occurrences across 2 files.",
		},
		{
			name: "singular",
			out:  &grep.Output{Mode: "count", NumMatches: 1, NumFiles: 1, Content: "a.go:1"},
			want: "a.go:1\n\nFound 1 total occurrence across 1 file.",
		},
		{
			name: "pagination suffix",
			out:  &grep.Output{Mode: "count", NumMatches: 5, NumFiles: 2, Content: "a.go:3\nb.go:2", AppliedLimit: new(250)},
			want: "a.go:3\nb.go:2\n\nFound 5 total occurrences across 2 files. with pagination = limit: 250",
		},
		{
			name: "empty content still gets summary",
			out:  &grep.Output{Mode: "count"},
			want: "No matches found\n\nFound 0 total occurrences across 0 files.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := grepWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGrepWire_FilesWithMatchesMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  *grep.Output
		want string
	}{
		{
			name: "no files",
			out:  &grep.Output{Mode: "files_with_matches", NumFiles: 0},
			want: "No files found",
		},
		{
			name: "one file singular",
			out:  &grep.Output{Mode: "files_with_matches", NumFiles: 1, Filenames: []string{"src/a.go"}},
			want: "Found 1 file\nsrc/a.go",
		},
		{
			name: "many files with limit",
			out: &grep.Output{Mode: "files_with_matches", NumFiles: 3,
				Filenames: []string{"a.go", "b.go", "c.go"}, AppliedLimit: new(250)},
			want: "Found 3 files limit: 250\na.go\nb.go\nc.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := grepWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

// goGrep fallback (rg missing) produces Output without Mode — it keeps the
// JSON wire shape instead of pretending to be a TS mode string.
func TestGrepWire_GoGrepFallbackIsJSON(t *testing.T) {
	t.Parallel()

	out := &grep.Output{
		Matches: []grep.Match{{File: "a.go", Line: 1, Content: "x"}},
		Count:   1,
	}
	want := `{"matches":[{"file":"a.go","line":1,"content":"x"}],"count":1}`
	if got := grepWireText(t, out); got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

func TestGrepWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()

	if got := grepWireText(t, "x"); got != "\"x\"" {
		t.Errorf("wire text = %q, want %q", got, "\"x\"")
	}
}

func TestGrepDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()

	tt := grep.New()
	raw := tool.WrapSingleBlock(`{"mode":"count","numMatches":3,"numFiles":1,"content":"a.go:3"}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*grep.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *grep.Output", v)
	}
	if o.Mode != "count" || o.NumMatches != 3 || o.Content != "a.go:3" {
		t.Errorf("decoded output = %+v, want mode=count numMatches=3 content=a.go:3", o)
	}
}

// A wire text that is itself a JSON object would decode into an all-zero
// Output (unknown fields ignored) and render garbage on replay — the
// identifying-fields check must reject it.
func TestGrepDecodeResult_RejectsJSONObjectText(t *testing.T) {
	t.Parallel()

	tt := grep.New()
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object text lacking identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("error = %q, want it to mention identifying fields", err.Error())
	}
}

func TestGrepDecodeResult_RejectsGoGrepZeroMatches(t *testing.T) {
	t.Parallel()

	tt := grep.New()
	raw := tool.WrapSingleBlock(`{"matches":[],"count":0}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject all-zero goGrep output (indistinguishable from foreign JSON)")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("error = %q, want it to mention identifying fields", err.Error())
	}
}

func TestGrepDecodeResult_RejectsPlainText(t *testing.T) {
	t.Parallel()

	tt := grep.New()
	raw := tool.WrapSingleBlock("No matches found")
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON plain text")
	}
	if !strings.Contains(err.Error(), "invalid character 'N' looking for beginning of value") {
		t.Errorf("error = %q, want json syntax error for 'N'", err.Error())
	}
}
