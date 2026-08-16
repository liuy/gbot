package bash_test

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/types"
)

// bashWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func bashWireText(t *testing.T, data any) string {
	t.Helper()
	tt := bash.New(nil)
	wb, ok := tt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Bash tool must implement ToolWithWireBlocks")
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

func TestBashWire_Stdout(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  *bash.Output
		want string
	}{
		{
			name: "leading blank lines stripped, trailing trimmed",
			out:  &bash.Output{Stdout: "\n\n  \nhello\n\n"},
			want: "hello",
		},
		{
			name: "leading spaces without newline kept",
			out:  &bash.Output{Stdout: "  hello"},
			want: "  hello",
		},
		{
			name: "whitespace-only stdout drops to empty",
			out:  &bash.Output{Stdout: "\n"},
			want: "",
		},
		{
			name: "stdout and stderr join with newline",
			out:  &bash.Output{Stdout: "out", Stderr: "err"},
			want: "out\nerr",
		},
		{
			name: "stderr alone",
			out:  &bash.Output{Stderr: "  err  "},
			want: "err",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bashWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBashWire_TimedOut(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  *bash.Output
		want string
	}{
		{
			name: "stderr plus aborted tag",
			out:  &bash.Output{Stderr: "boom", TimedOut: true},
			want: "boom\n<error>Command was aborted before completion</error>",
		},
		{
			// The EOL gate checks the RAW stderr (BashTool.tsx:603 `if (stderr)`),
			// not the trimmed error message — whitespace-only stderr still
			// contributes the leading newline.
			name: "whitespace-only stderr keeps leading newline",
			out:  &bash.Output{Stderr: " ", TimedOut: true},
			want: "\n<error>Command was aborted before completion</error>",
		},
		{
			name: "no stderr, tag only",
			out:  &bash.Output{TimedOut: true},
			want: "<error>Command was aborted before completion</error>",
		},
		{
			name: "stdout plus aborted tag",
			out:  &bash.Output{Stdout: "out", TimedOut: true},
			want: "out\n<error>Command was aborted before completion</error>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bashWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBashWire_BackgroundAndEmpty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  *bash.Output
		want string
	}{
		{
			name: "background id",
			out:  &bash.Output{BackgroundJobID: "bg-1"},
			want: "Command running in background with ID: bg-1. Poll its output with the Job tool.",
		},
		{
			name: "all segments empty",
			out:  &bash.Output{},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bashWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBashWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()

	if got := bashWireText(t, "x"); got != "\"x\"" {
		t.Errorf("wire text = %q, want %q", got, "\"x\"")
	}
}

func TestBashDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	raw := tool.WrapSingleBlock(`{"output":"hi","exitCode":0}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*bash.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *bash.Output", v)
	}
	if o.Stdout != "hi" {
		t.Errorf("Stdout = %q, want hi", o.Stdout)
	}
}

// A silent successful command's legacy wire carries only a non-empty cwd —
// the identifying-fields check must include CWD or every mkdir/touch/rm
// replay would be rejected.
func TestBashDecodeResult_SilentSuccessLegacyWire(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	raw := tool.WrapSingleBlock(`{"output":"","exitCode":0,"cwd":"/tmp/x"}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*bash.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *bash.Output", v)
	}
	if o.CWD != "/tmp/x" {
		t.Errorf("CWD = %q, want /tmp/x", o.CWD)
	}
}

func TestBashDecodeResult_TimedOutLegacyWire(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	raw := tool.WrapSingleBlock(`{"output":"","timed_out":true}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*bash.Output)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *bash.Output", v)
	}
	if !o.TimedOut {
		t.Error("TimedOut = false, want true")
	}
}

// A wire text that is itself a JSON object (cat package.json, curl api)
// would decode into an all-zero Output and render empty on replay.
func TestBashDecodeResult_RejectsJSONObjectText(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object text lacking identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("error = %q, want it to mention identifying fields", err.Error())
	}
}

func TestBashDecodeResult_RejectsAllZeroObject(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	raw := tool.WrapSingleBlock(`{"output":""}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject all-zero object (indistinguishable from foreign JSON)")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("error = %q, want it to mention identifying fields", err.Error())
	}
}

func TestBashDecodeResult_RejectsPlainText(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	raw := tool.WrapSingleBlock("Found 3 files")
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON plain text")
	}
	if !strings.Contains(err.Error(), "invalid character 'F' looking for beginning of value") {
		t.Errorf("error = %q, want json syntax error for 'F'", err.Error())
	}
}
