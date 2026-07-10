package fileedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

func TestDecodeUTF16LE(t *testing.T) {
	t.Parallel()
	data := []byte{0x48, 0x00, 0x69, 0x00}
	got := decodeUTF16LE(data)
	if got != "Hi" {
		t.Errorf("decodeUTF16LE = %q, want %q", got, "Hi")
	}
}

func TestDecodeUTF16LE_OddLength(t *testing.T) {
	t.Parallel()
	data := []byte{0x48, 0x00, 0x69}
	got := decodeUTF16LE(data)
	if got != "H" {
		t.Errorf("decodeUTF16LE odd = %q, want %q", got, "H")
	}
}

func TestEncodeUTF16LE(t *testing.T) {
	t.Parallel()
	got := encodeUTF16LE("AB")
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	if got[0] != 0x41 || got[1] != 0x00 || got[2] != 0x42 || got[3] != 0x00 {
		t.Errorf("encodeUTF16LE = %x, want 41004200", got)
	}
}

func TestRoundtripUTF16LE(t *testing.T) {
	t.Parallel()
	original := "Hello, World!"
	encoded := encodeUTF16LE(original)
	decoded := decodeUTF16LE(encoded)
	if decoded != original {
		t.Errorf("roundtrip: got %q, want %q", decoded, original)
	}
}

func TestRoundtripUTF16LE_SurrogatePairs(t *testing.T) {
	t.Parallel()
	// Emoji and other non-BMP characters require surrogate pairs in UTF-16
	original := "Hello 😀 World 🌈 Test 日本語"
	encoded := encodeUTF16LE(original)
	decoded := decodeUTF16LE(encoded)
	if decoded != original {
		t.Errorf("roundtrip surrogate: got %q, want %q", decoded, original)
	}
}

func TestReadFileForEdit_FileNotExist(t *testing.T) {
	t.Parallel()
	fr := readFileForEdit("/nonexistent/file.txt")
	if fr.fileExists {
		t.Error("fileExists = true, want false")
	}
}

func TestReadFileForEdit_NormalFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(fp, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fr := readFileForEdit(fp)
	if !fr.fileExists {
		t.Fatal("fileExists = false, want true")
	}
	if fr.content != "hello\nworld\n" {
		t.Errorf("content = %q, want %q", fr.content, "hello\nworld\n")
	}
	if fr.hasBOM {
		t.Error("hasBOM = true, want false")
	}
	if fr.hasCRLF {
		t.Error("hasCRLF = true, want false")
	}
	if fr.fileMode&0o644 != 0o644 {
		t.Errorf("fileMode = %o, want 0644", fr.fileMode)
	}
}

func TestReadFileForEdit_CRLFFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "crlf.txt")
	if err := os.WriteFile(fp, []byte("line1\r\nline2\r\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fr := readFileForEdit(fp)
	if !fr.hasCRLF {
		t.Error("hasCRLF = false, want true")
	}
	if fr.content != "line1\nline2\n" {
		t.Errorf("content = %q, want %q", fr.content, "line1\nline2\n")
	}
}

func TestReadFileForEdit_UTF16LEWithBOM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "bom.txt")
	bom := []byte{0xFF, 0xFE}
	text := "hello"
	encoded := make([]byte, len(bom)+len(text)*2)
	copy(encoded, bom)
	for i, r := range text {
		v := uint16(r)
		encoded[len(bom)+i*2] = byte(v)
		encoded[len(bom)+i*2+1] = byte(v >> 8)
	}
	if err := os.WriteFile(fp, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fr := readFileForEdit(fp)
	if !fr.hasBOM {
		t.Error("hasBOM = false, want true")
	}
	if fr.content != "hello" {
		t.Errorf("content = %q, want %q", fr.content, "hello")
	}
}

func TestExecute_MaxFileSizeExceeded(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.txt")
	// Write a file larger than the temporarily lowered limit
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Temporarily lower the limit to trigger the size check
	orig := MaxEditFileSize
	MaxEditFileSize = 5 // 5 bytes — our file is 12 bytes
	defer func() { MaxEditFileSize = orig }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for oversized file")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "file size") {
		t.Errorf("Error = %q, want error mentioning file size", err.Error())
	}
}

func TestExecute_WriteErrorOnNewFile(t *testing.T) {
	// Try to create a new file in a read-only directory
	dir := t.TempDir()
	subdir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(subdir, 0o555); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	defer func() { _ = os.Chmod(subdir, 0o755) }()

	fp := filepath.Join(subdir, "newfile.txt")
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"hello"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error for new file in read-only dir")
	}
	if !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "write") {
		t.Errorf("Error = %q, want error mentioning permission/write issue", err.Error())
	}
}

func TestExecute_WriteErrorOnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	// Create empty file, then make it read-only
	if err := os.WriteFile(fp, []byte(""), 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"hello"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error on read-only empty file")
	}
	if !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "write") {
		t.Errorf("Error = %q, want error mentioning permission/write issue", err.Error())
	}
}

func TestExecute_WriteErrorBOM(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "bom.txt")

	// Write UTF-16 LE with BOM
	bom := []byte{0xFF, 0xFE}
	text := "hello"
	encoded := make([]byte, len(bom)+len(text)*2)
	copy(encoded, bom)
	for i, r := range text {
		v := uint16(r)
		encoded[len(bom)+i*2] = byte(v)
		encoded[len(bom)+i*2+1] = byte(v >> 8)
	}
	if err := os.WriteFile(fp, encoded, 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"world"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error on read-only BOM file")
	}
	if !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "write") {
		t.Errorf("Error = %q, want error mentioning permission/write issue", err.Error())
	}
}

func TestGetStructuredPatch_SingleCharInLine(t *testing.T) {
	// When only a few characters change within a line, each hunk line should
	// contain the FULL line content, not just the changed characters.
	old := "func TestHandleStackOverflow_NonNumericID(t *testing.T) {\n\tu := mustParseURL(t, \"abc\")\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n}"
	new_ := "func TestHandleStackOverflow_NonNumericID2(t *testing.T) {\n\tu := mustParseURL(t, \"abc\")\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n}"

	hunks := tool.ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected hunks, got none")
	}
	for _, h := range hunks {
		for _, l := range h.Lines {
			marker := l[0]
			content := l[1:]
			if marker == '+' || marker == '-' {
				// Each changed line must be a complete line, not a fragment.
				// "func TestHandle..." is 49+ chars; a fragment would be just "2" or "ID2".
				if len(content) < 20 {
					t.Errorf("hunk line [%c] too short (%d chars): %q — expected full line content",
						marker, len(content), content)
				}
				if !strings.Contains(content, "func TestHandleStackOverflow") {
					t.Errorf("hunk line [%c] missing function name: %q", marker, content)
				}
			}
		}
	}
}

func TestGetStructuredPatch_SingleCharInLine_LargeFile(t *testing.T) {
	// O(ND) Myers diff handles 4000-line files easily (4000+4000=8000 < 200000).
	// Verifies ComputePatch handles large files without fallback.
	var oldBuf, newBuf strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&oldBuf, "\tline %d content here with enough padding\n", i)
		fmt.Fprintf(&newBuf, "\tline %d content here with enough padding\n", i)
	}
	oldLines := strings.Split(oldBuf.String(), "\n")
	newLines := strings.Split(newBuf.String(), "\n")
	oldLines[50] = "\tfunc TestHandleStackOverflow_NonNumericID(t *testing.T) {"
	newLines[50] = "\tfunc TestHandleStackOverflow_NonNumericID2(t *testing.T) {"
	oldContent := strings.Join(oldLines, "\n")
	newContent := strings.Join(newLines, "\n")

	hunks := tool.ComputePatch(oldContent, newContent)
	if len(hunks) == 0 {
		t.Fatal("expected hunks")
	}
	for _, h := range hunks {
		for _, l := range h.Lines {
			if len(l) == 0 {
				continue
			}
			marker := l[0]
			content := l[1:]
			if marker == '+' || marker == '-' {
				if strings.Contains(content, "TestHandleStackOverflow") {
					if len(content) < 20 {
						t.Errorf("LARGE FILE: hunk line [%c] too short (%d chars): %q — expected full line",
							marker, len(content), content)
					}
					if !strings.Contains(content, "func TestHandleStackOverflow") {
						t.Errorf("LARGE FILE: hunk line [%c] missing func keyword: %q", marker, content)
					}
				}
			}
		}
	}
}

func TestGetStructuredPatch_RemoveWordFromLine(t *testing.T) {
	// Removing "Feb " from a line should show full line removed and full line added.
	old := "\tcontent before\n\tif !strings.Contains(got.Content, \"Feb 28, 2024\") {\n\t\terror here\n\t}"
	new_ := "\tcontent before\n\tif !strings.Contains(got.Content, \"28, 2024\") {\n\t\terror here\n\t}"

	hunks := tool.ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected hunks, got none")
	}
	for _, h := range hunks {
		for _, l := range h.Lines {
			if len(l) == 0 {
				continue
			}
			marker := l[0]
			content := l[1:]
			if marker == '+' || marker == '-' {
				if strings.Contains(content, "strings.Contains") {
					if !strings.Contains(content, "got.Content") {
						t.Errorf("REMOVED WORD: hunk line [%c] incomplete: %q — missing got.Content",
							marker, content)
					}
					if !strings.Contains(content, "28, 2024") {
						t.Errorf("REMOVED WORD: hunk line [%c] incomplete: %q — missing date",
							marker, content)
					}
				}
			}
		}
	}
}

// Tests that verify large-file diff produces complete lines (no fragments).
func TestGetStructuredPatch_LargeFile_NoFragments_SingleChar(t *testing.T) {
	var buf strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&buf, "\tline %04d padding to fill width\n", i)
	}
	base := buf.String()

	old := strings.Replace(base, "\tline 0050", "\tfunc TestHandleStackOverflow_NonNumericID(t *testing.T) {\n", 1)
	new_ := strings.Replace(base, "\tline 0050", "\tfunc TestHandleStackOverflow_NonNumericID2(t *testing.T) {\n", 1)

	hunks := tool.ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected hunks")
	}

	for _, h := range hunks {
		for _, l := range h.Lines {
			if len(l) == 0 {
				continue
			}
			marker := l[0]
			content := l[1:]
			// No changed line should be just "2" or a fragment shorter than the function name
			if marker == '+' || marker == '-' {
				if content == "2" || content == "" {
					t.Errorf("FRAGMENT: hunk line [%c] is just %q — expected full line",
						marker, content)
				}
				// If this line mentions the function name, it must be complete
				if strings.Contains(content, "NonNumericID") {
					if !strings.Contains(content, "func ") {
						t.Errorf("FRAGMENT: [%c] %q — missing 'func'", marker, content)
					}
					if !strings.Contains(content, "testing.T") {
						t.Errorf("FRAGMENT: [%c] %q — missing 'testing.T'", marker, content)
					}
				}
			}
		}
	}
}

func TestGetStructuredPatch_LargeFile_NoFragments_RemoveWord(t *testing.T) {
	var buf strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&buf, "\tline %04d padding to fill width\n", i)
	}
	base := buf.String()

	old := strings.Replace(base, "\tline 0050", "\tif !strings.Contains(got.Content, \"Feb 28, 2024\") {\n", 1)
	new_ := strings.Replace(base, "\tline 0050", "\tif !strings.Contains(got.Content, \"28, 2024\") {\n", 1)

	hunks := tool.ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected hunks")
	}

	for _, h := range hunks {
		for _, l := range h.Lines {
			if len(l) == 0 {
				continue
			}
			marker := l[0]
			content := l[1:]
			if marker == '+' || marker == '-' {
				if content == "Feb" || content == "Feb " {
					t.Errorf("FRAGMENT: hunk line [%c] is just %q — expected full line with strings.Contains",
						marker, content)
				}
				if strings.Contains(content, "strings.Contains") {
					if !strings.Contains(content, "got.Content") {
						t.Errorf("FRAGMENT: [%c] %q — missing got.Content", marker, content)
					}
				}
			}
		}
	}
}

func TestExecute_WriteErrorNormal(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error on read-only normal file")
	}
	if !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "write") {
		t.Errorf("Error = %q, want error mentioning permission/write issue", err.Error())
	}
}

func TestRenderEditResult_SingleCharChange(t *testing.T) {
	old := "func TestHandleStackOverflow_NonNumericID(t *testing.T) {\n\tu := mustParseURL(t, \"abc\")\n}"
	new_ := "func TestHandleStackOverflow_NonNumericID2(t *testing.T) {\n\tu := mustParseURL(t, \"abc\")\n}"

	result := renderEditResult(&Output{
		FilePath:  "test.go",
		OldString: old,
		NewString: new_,
	})

	strip := stripANSI(result)
	if !strings.Contains(strip, "func TestHandleStackOverflow_NonNumericID") {
		t.Errorf("render result missing old function name: %s", strip)
	}
	if !strings.Contains(strip, "NonNumericID2") {
		t.Errorf("render result missing new function name: %s", strip)
	}
	// The "2" should NOT appear on a line by itself
	lines := strings.SplitSeq(strip, "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "2" || trimmed == "+2" || trimmed == "-2" {
			t.Errorf("found isolated '2' on its own line: %q in output:\n%s", line, strip)
		}
	}
}

func TestRenderEditResult_RemoveWordFromLine(t *testing.T) {
	old := "\tcontent before\n\tif !strings.Contains(got.Content, \"Feb 28, 2024\") {\n\t\terror here\n\t}"
	new_ := "\tcontent before\n\tif !strings.Contains(got.Content, \"28, 2024\") {\n\t\terror here\n\t}"

	result := renderEditResult(&Output{
		FilePath:  "test.go",
		OldString: old,
		NewString: new_,
	})

	strip := stripANSI(result)
	// "Feb" should NOT appear as a standalone removed fragment
	lines := strings.SplitSeq(strip, "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		// If "Feb" appears it must be in a longer line
		if trimmed == "Feb" || trimmed == "-Feb" || trimmed == "+Feb" {
			t.Errorf("found isolated 'Feb' on its own line: %q in output:\n%s", line, strip)
		}
	}
	// Both old and new lines must contain "got.Content"
	if !strings.Contains(strip, "got.Content") {
		t.Errorf("render result missing 'got.Content': %s", strip)
	}
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func TestEditStringsToHunks_SingleCharChange(t *testing.T) {
	old := "func TestHandleStackOverflow_NonNumericID(t *testing.T) {"
	new_ := "func TestHandleStackOverflow_NonNumericID2(t *testing.T) {"

	hunks := tool.ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected hunks")
	}
	for _, h := range hunks {
		for _, l := range h.Lines {
			marker := l[0]
			content := l[1:]
			if marker == '-' {
				if !strings.Contains(content, "func TestHandleStackOverflow_NonNumericID(t") {
					t.Errorf("REMOVED line incomplete: %q", content)
				}
			}
			if marker == '+' {
				if !strings.Contains(content, "func TestHandleStackOverflow_NonNumericID2(t") {
					t.Errorf("ADDED line incomplete: %q", content)
				}
			}
		}
	}
}

func TestEditStringsToHunks_MultiLine_PreservesContext(t *testing.T) {
	old := "line1\nline2\nline3"
	new_ := "line1\nMODIFIED\nline3"

	hunks := tool.ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected hunks")
	}

	var removed, added, context int
	for _, h := range hunks {
		for _, l := range h.Lines {
			switch l[0] {
			case '-':
				removed++
			case '+':
				added++
			default:
				context++
			}
		}
	}
	// line2 is the only changed line — should be 1 remove + 1 add.
	// line1 and line3 are context — should NOT be deleted+reinserted.
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only line2)", removed)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (only MODIFIED)", added)
	}
	if context < 2 {
		t.Errorf("context = %d, want >= 2 (line1 + line3)", context)
	}
}
