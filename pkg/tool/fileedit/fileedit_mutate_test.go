package fileedit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

func TestMaxEditFileSize(t *testing.T) {
	t.Parallel()
	if MaxEditFileSize != 1024*1024*1024 {
		t.Errorf("MaxEditFileSize = %d, want 1 GiB", MaxEditFileSize)
	}
}

// Meaningful old lines are longer than 5 chars and not generic.
func TestFindNearbyHint_MeaningfulLines(t *testing.T) {
	t.Parallel()
	content := "alpha line one\nbeta line two\ngamma line three"

	got := findNearbyHint(content, "beta line two")
	if got != "1: alpha line one\n2: beta line two\n3: gamma line three\n" {
		t.Errorf("hint = %q, want the whole file with line numbers", got)
	}
	// Exactly 5 chars is not meaningful → no hint.
	if got := findNearbyHint(content, "beta2"); got != "" {
		t.Errorf("5-char old line = %q, want no hint", got)
	}
	// 6 chars is meaningful.
	if got := findNearbyHint("x beta2 y", "beta2 z"); got != "1: x beta2 y\n" {
		t.Errorf("6-char old line = %q, want the matching line", got)
	}
	// Generic lines are skipped even when long enough.
	if got := findNearbyHint(content, "return"); got != "" {
		t.Errorf("generic old line = %q, want no hint", got)
	}
	// No token overlap → no hint.
	if got := findNearbyHint(content, "zzzz yyyy xxxx"); got != "" {
		t.Errorf("no-overlap search = %q, want no hint", got)
	}
}

// The hint window is ±5 lines around the best match.
func TestFindNearbyHint_ContextWindow(t *testing.T) {
	t.Parallel()
	var lines []string
	for i := range 13 {
		lines = append(lines, "filler"+string(rune('a'+i)))
	}
	lines[9] = "target needle line"
	content := ""
	for i, l := range lines {
		if i > 0 {
			content += "\n"
		}
		content += l
	}
	got := findNearbyHint(content, "target needle line")
	want := "5: fillere\n6: fillerf\n7: fillerg\n8: fillerh\n9: filleri\n10: target needle line\n11: fillerk\n12: fillerl\n13: fillerm\n"
	if got != want {
		t.Errorf("context window =\n%q\nwant\n%q", got, want)
	}
}

// Relative paths resolve against the tool context working directory.
func TestExecute_PathResolution(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(), json.RawMessage(`{"file_path":"a.txt","old_string":"hello","new_string":"bye"}`),
		&tool.ToolUseContext{WorkingDir: sub})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(sub, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "bye" {
		t.Errorf("file = %q, want bye", data)
	}
	// An absolute path ignores the working directory.
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(sub, "b.txt")
	if _, err := Execute(context.Background(), json.RawMessage(`{"file_path":`+jsonQuote(abs)+`,"old_string":"hello","new_string":"abs"}`),
		&tool.ToolUseContext{WorkingDir: dir}); err != nil {
		t.Fatalf("absolute Execute: %v", err)
	}
	data, err = os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abs" {
		t.Errorf("absolute-path file = %q, want abs", data)
	}
	// A relative path without a working directory resolves against the CWD.
	if _, err := Execute(context.Background(), json.RawMessage(`{"file_path":"no-such-dir-file.txt","old_string":"x","new_string":"y"}`), nil); err == nil {
		t.Error("relative path without context should not silently succeed")
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A two-byte UTF-16LE BOM with no payload still counts as a BOM.
func TestReadFileForEdit_BOMAndCRLF(t *testing.T) {
	dir := t.TempDir()
	bomPath := filepath.Join(dir, "bom.txt")
	if err := os.WriteFile(bomPath, []byte{0xFF, 0xFE}, 0o644); err != nil {
		t.Fatal(err)
	}
	res := readFileForEdit(bomPath)
	if !res.fileExists || !res.hasBOM || res.content != "" {
		t.Errorf("BOM-only file = %+v, want exists with BOM and empty content", res)
	}
	utf16 := filepath.Join(dir, "u16.txt")
	if err := os.WriteFile(utf16, append([]byte{0xFF, 0xFE}, []byte{'h', 0, 'i', 0}...), 0o644); err != nil {
		t.Fatal(err)
	}
	res = readFileForEdit(utf16)
	if !res.hasBOM || res.content != "hi" {
		t.Errorf("utf16 file = %+v, want content hi", res)
	}
	crlf := filepath.Join(dir, "crlf.txt")
	if err := os.WriteFile(crlf, []byte("a\r\nb\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = readFileForEdit(crlf)
	if !res.hasCRLF || res.content != "a\nb\n" {
		t.Errorf("crlf file = %+v, want normalized a\\nb\\n", res)
	}
	missing := readFileForEdit(filepath.Join(dir, "missing.txt"))
	if missing.fileExists || missing.content != "" {
		t.Errorf("missing file = %+v, want fileExists false", missing)
	}
}

// decodeUTF16LE drops a trailing odd byte; encodeUTF16LE is its inverse.
func TestUTF16RoundTrip(t *testing.T) {
	t.Parallel()
	if got := decodeUTF16LE([]byte{'h', 0, 'i', 0, 'x'}); got != "hi" {
		t.Errorf("odd length = %q, want hi", got)
	}
	if got := decodeUTF16LE(nil); got != "" {
		t.Errorf("empty = %q, want empty string", got)
	}
	encoded := encodeUTF16LE("hi")
	if string(encoded) != string([]byte{'h', 0, 'i', 0}) {
		t.Errorf("encodeUTF16LE(hi) = %v, want [68 0 69 0]", encoded)
	}
}
