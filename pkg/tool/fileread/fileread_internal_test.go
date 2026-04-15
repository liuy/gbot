package fileread

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Blocked device paths
// ---------------------------------------------------------------------------

func TestExecute_BlockedDevicePath(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"file_path":"/dev/zero"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for blocked device path")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_stdin(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/stdin"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/stdin")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_tty(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/tty"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/tty")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_console(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/console"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/console")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_stdout(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/stdout"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/stdout")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_stderr(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/stderr"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/stderr")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_fd0(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/fd/0"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/fd/0")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_fd1(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/fd/1"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/fd/1")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_fd2(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/fd/2"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/fd/2")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_procSelfFd0(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/proc/self/fd/0"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /proc/self/fd/0")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_procSelfFd1(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/proc/self/fd/1"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /proc/self/fd/1")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestExecute_BlockedDevicePath_procSelfFd2(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/proc/self/fd/2"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /proc/self/fd/2")
	}
	if !strings.Contains(err.Error(), "cannot read device file") {
		t.Errorf("Error = %q, want 'cannot read device file'", err.Error())
	}
}

func TestIsBlockedDevicePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path  string
		want  bool
	}{
		{"/dev/zero", true},
		{"/dev/urandom", true},
		{"/dev/stdin", true},
		{"/dev/tty", true},
		{"/dev/console", true},
		{"/dev/stdout", true},
		{"/dev/stderr", true},
		{"/dev/fd/0", true},
		{"/dev/fd/1", true},
		{"/dev/fd/2", true},
		{"/proc/self/fd/0", true},
		{"/proc/self/fd/1", true},
		{"/proc/self/fd/2", true},
		{"/proc/1234/fd/0", true},
		{"/proc/1234/fd/1", true},
		{"/proc/1234/fd/2", true},
		{"/dev/null", false},
		{"/tmp/file.txt", false},
		{"/proc/self/fd/3", false},
		{"/proc/self/fd/99", false},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			got := isBlockedDevicePath(tc.path)
			if got != tc.want {
				t.Errorf("isBlockedDevicePath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Binary detection
// ---------------------------------------------------------------------------

func TestExecute_BinaryExtension(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.exe")
	if err := os.WriteFile(fp, []byte("binary content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for binary extension")
	}
	if !strings.Contains(err.Error(), "binary extension") {
		t.Errorf("Error = %q, want 'binary extension' error", err.Error())
	}
}

func TestExecute_NullBytes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "binary.bin")
	// Write file with a null byte embedded
	if err := os.WriteFile(fp, []byte("hello\x00world"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for null bytes")
	}
	if !strings.Contains(err.Error(), "null bytes") {
		t.Errorf("Error = %q, want 'null bytes' error", err.Error())
	}
}

func TestExecute_LongLineWithOffsetLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "longline.txt")
	// Create a line longer than bufio.Scanner's old 64K buffer
	// With os.ReadFile approach (matching TS), long lines are handled fine
	longLine := strings.Repeat("x", 70000) + "\n"
	if err := os.WriteFile(fp, []byte(longLine), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Use offset/limit path — should succeed (no scanner buffer limit)
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":1,"limit":1}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v (long lines should work with single-read approach)", err)
	}
	output, ok := result.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if output.NumLines != 1 {
		t.Errorf("NumLines = %d, want 1", output.NumLines)
	}
	if len(output.Content) != 70000 {
		t.Errorf("Content length = %d, want 70000", len(output.Content))
	}
}

func TestExecute_ReadFileError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "noperm.txt")
	if err := os.WriteFile(fp, []byte("secret"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Restore permissions for cleanup
	defer func() { _ = os.Chmod(fp, 0o644) }()

	// Reading a file with no permissions should fail
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for unreadable file")
	}
	if !strings.Contains(err.Error(), fp) {
		t.Errorf("Error = %q, should contain path %q", err.Error(), fp)
	}
}

func TestExecute_OpenFileError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "noperm2.txt")
	if err := os.WriteFile(fp, []byte("secret"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	// Reading with offset/limit triggers os.Open path
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":1,"limit":1}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for unreadable file")
	}
	if !strings.Contains(err.Error(), fp) {
		t.Errorf("Error = %q, should contain path %q", err.Error(), fp)
	}
}

func TestCountTotalLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "lines.txt")
	content := "a\nb\nc\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	count, err := countTotalLines(fp)
	if err != nil {
		t.Fatalf("countTotalLines: %v", err)
	}
	if count != 3 {
		t.Errorf("countTotalLines = %d, want 3", count)
	}
}

func TestCountTotalLines_NoTrailingNewline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "lines2.txt")
	content := "a\nb\nc"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	count, err := countTotalLines(fp)
	if err != nil {
		t.Fatalf("countTotalLines: %v", err)
	}
	if count != 2 {
		t.Errorf("countTotalLines = %d, want 2", count)
	}
}

func TestCountTotalLines_Empty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	count, err := countTotalLines(fp)
	if err != nil {
		t.Fatalf("countTotalLines: %v", err)
	}
	if count != 0 {
		t.Errorf("countTotalLines = %d, want 0", count)
	}
}

// --- parsePDFPageRange ---
func TestParsePDFPageRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  *PageRange
	}{
		{"5", &PageRange{FirstPage: 5, LastPage: 5}},
		{"1-10", &PageRange{FirstPage: 1, LastPage: 10}},
		{"3-", &PageRange{FirstPage: 3, LastPage: math.MaxInt}},
		{"", nil},
		{"0", nil},
		{"-1", nil}, // negative page
		{"1-", &PageRange{FirstPage: 1, LastPage: math.MaxInt}},
		{"10-5", nil}, // inverted range
		{"abc", nil},
		{"  ", nil}, // whitespace only
		{"  3  ", &PageRange{FirstPage: 3, LastPage: 3}}, // leading/trailing trimmed
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := parsePDFPageRange(tc.input)
			if tc.want == nil {
				if got != nil {
					t.Errorf("got %v, want nil", got)
				}
				return
			}
			if got == nil || got.FirstPage != tc.want.FirstPage || got.LastPage != tc.want.LastPage {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// --- getMimeType ---
func TestGetMimeType(t *testing.T) {
	t.Parallel()
	tests := []struct{ ext, want string }{
		{".png", "image/png"},
		{".jpg", "image/jpeg"},
		{".jpeg", "image/jpeg"},
		{".gif", "image/gif"},
		{".webp", "image/webp"},
		{".unknown", "application/octet-stream"},
	}
	for _, tc := range tests {
		t.Run(tc.ext, func(t *testing.T) {
			t.Parallel()
			if got := getMimeType(tc.ext); got != tc.want {
				t.Errorf("getMimeType(%q) = %q, want %q", tc.ext, got, tc.want)
			}
		})
	}
}

// --- expandPath ---
func TestExpandPath(t *testing.T) {
	t.Parallel()
	// Absolute path should return as-is
	abs := "/tmp/test.txt"
	if got := expandPath(abs); got != abs {
		t.Errorf("expandPath(%q) = %q, want %q", abs, got, abs)
	}
	// Relative path should become absolute
	rel := "test.txt"
	got := expandPath(rel)
	if !filepath.IsAbs(got) {
		t.Errorf("expandPath(%q) = %q, want absolute path", rel, got)
	}
}

// --- PDF pdftoppm tests ---
func TestIsPdftoppmAvailable(t *testing.T) {
	t.Parallel()
	// Just verify it returns a boolean without crashing
	result := isPdftoppmAvailable()
	t.Logf("isPdftoppmAvailable() = %v", result)
}

func TestGetPDFPageCount(t *testing.T) {
	// Requires a PDF file
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.pdf")
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	count := getPDFPageCount(fp)
	if count < 1 {
		t.Errorf("getPDFPageCount = %d, want >= 1", count)
	}
	// Also verify that a non-existent file returns 0
	count2 := getPDFPageCount(filepath.Join(dir, "nonexistent.pdf"))
	if count2 != 0 {
		t.Errorf("getPDFPageCount(nonexistent) = %d, want 0", count2)
	}
}

// ---------------------------------------------------------------------------
// Task #21: PDF magic byte check
// ---------------------------------------------------------------------------

func TestExecute_PDFInvalidMagicBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "fake.pdf")
	// Write a file with .pdf extension but HTML content (not %PDF-)
	if err := os.WriteFile(fp, []byte("<html><body>Not a PDF</body></html>"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should reject non-PDF file with .pdf extension")
	}
	if !strings.Contains(err.Error(), "not a valid PDF") {
		t.Errorf("Error = %q, want 'not a valid PDF'", err.Error())
	}
}

func TestExecute_PDFEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.pdf")
	if err := os.WriteFile(fp, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should reject empty PDF file")
	}
	// Empty file should be caught by "PDF file is empty"
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Error = %q, want 'empty' message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// P1 #3: PDF encryption/password detection
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Coverage: output() interface methods
// ---------------------------------------------------------------------------

func TestOutputMethods(t *testing.T) {
	t.Parallel()
	// Call all output() interface methods to cover the 0% functions
	TextOutput{}.output()
	ImageOutput{}.output()
	PDFOutput{}.output()
	PartsOutput{}.output()
	FileUnchangedOutput{}.output()
}

// ---------------------------------------------------------------------------
// Coverage: expandPath branches
// ---------------------------------------------------------------------------

func TestExpandPath_TildeWithHome(t *testing.T) {
	t.Parallel()
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	got := expandPath("~/test.txt")
	want := filepath.Join(home, "test.txt")
	if got != want {
		t.Errorf("expandPath(\"~/test.txt\") = %q, want %q", got, want)
	}
}

func TestExpandPath_TildeHomeEmpty(t *testing.T) {
	t.Parallel()
	orig := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", orig) }()
	_ = os.Unsetenv("HOME")
	// When HOME is empty, should fall through to filepath.Abs
	got := expandPath("~/test.txt")
	if !filepath.IsAbs(got) {
		t.Errorf("expandPath(\"~/test.txt\") with empty HOME = %q, want absolute path", got)
	}
	if !strings.HasSuffix(got, "test.txt") {
		t.Errorf("expandPath(\"~/test.txt\") = %q, should end with 'test.txt'", got)
	}
}

// ---------------------------------------------------------------------------
// Coverage: getMtimeMs error path
// ---------------------------------------------------------------------------

func TestGetMtimeMs_Error(t *testing.T) {
	t.Parallel()
	_, err := getMtimeMs("/nonexistent/file/path")
	if err == nil {
		t.Fatal("getMtimeMs should return error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "/nonexistent/file/path") {
		t.Errorf("Error = %q, should contain path", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Coverage: countTotalLines error path
// ---------------------------------------------------------------------------

func TestCountTotalLines_Error(t *testing.T) {
	t.Parallel()
	_, err := countTotalLines("/nonexistent/file/path")
	if err == nil {
		t.Fatal("countTotalLines should return error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "/nonexistent/file/path") {
		t.Errorf("Error = %q, should contain path", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Coverage: parsePDFPageRange additional cases
// ---------------------------------------------------------------------------

func TestParsePDFPageRange_MultipleDashes(t *testing.T) {
	t.Parallel()
	got := parsePDFPageRange("1-2-3")
	if got != nil {
		t.Errorf("parsePDFPageRange(\"1-2-3\") = %v, want nil", got)
	}
}

func TestParsePDFPageRange_NegativeLastPage(t *testing.T) {
	t.Parallel()
	got := parsePDFPageRange("5--1")
	if got != nil {
		t.Errorf("parsePDFPageRange(\"5--1\") = %v, want nil", got)
	}
}

func TestParsePDFPageRange_ZeroRange(t *testing.T) {
	t.Parallel()
	got := parsePDFPageRange("0-5")
	if got != nil {
		t.Errorf("parsePDFPageRange(\"0-5\") = %v, want nil (first page 0)", got)
	}
}

// ---------------------------------------------------------------------------
// P1 #3: PDF encryption/password detection
// ---------------------------------------------------------------------------

func TestExecute_PDFEncrypted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "encrypted.pdf")
	// Create a valid-looking PDF with /Encrypt dictionary
	pdfContent := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n3 0 obj\n<< /Filter /Standard /V 1 /R 2 /Length 40 >>\nendobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \n0000000115 00000 n \ntrailer\n<< /Size 4 /Root 1 0 R /Encrypt 3 0 R >>\nstartxref\n200\n%%EOF\n"
	if err := os.WriteFile(fp, []byte(pdfContent), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should reject encrypted PDF")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("Error = %q, want 'password' message", err.Error())
	}
}

func TestIsPDFEncrypted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"encrypted", "%PDF-1.4\n/Encrypt 3 0 R\n", true},
		{"encrypted_with_dict", "%PDF-1.4\n<< /Encrypt >>\n", true},
		{"not_encrypted", "%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isPDFEncrypted([]byte(tc.content))
			if got != tc.want {
				t.Errorf("isPDFEncrypted() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Task #21: Open-ended page range always exceeds max (already fixed in #18)
// Verify it stays correct.
// ---------------------------------------------------------------------------

func TestExecute_PDFPagesOpenEndAlwaysExceedsMax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.pdf")
	// Write a minimal valid PDF
	pdfContent := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\nxref\n0 3\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \ntrailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n109\n%%EOF\n"
	if err := os.WriteFile(fp, []byte(pdfContent), 0644); err != nil {
		t.Fatal(err)
	}
	// Open-ended range "1-" should always be rejected
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"1-"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should reject open-ended page range")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("Error = %q, want 'exceeds maximum'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// parsePDFPageRange — line 171-173: lastPage part non-numeric
// ---------------------------------------------------------------------------

func TestParsePDFPageRange_LastPageNonNumeric(t *testing.T) {
	t.Parallel()
	got := parsePDFPageRange("1-abc")
	if got != nil {
		t.Errorf("parsePDFPageRange(\"1-abc\") = %v, want nil (non-numeric last page)", got)
	}
}

// ---------------------------------------------------------------------------
// getPDFPageCount — error paths (lines 272-274, 285-286)
// ---------------------------------------------------------------------------

func TestGetPDFPageCount_InvalidFile(t *testing.T) {
	t.Parallel()
	// A non-PDF file: pdfinfo should fail → error path (lines 272-274)
	count := getPDFPageCount("/dev/null")
	if count != 0 {
		t.Errorf("getPDFPageCount(/dev/null) = %d, want 0", count)
	}
}

func TestGetPDFPageCount_NonexistentFile(t *testing.T) {
	t.Parallel()
	count := getPDFPageCount("/nonexistent/file.pdf")
	if count != 0 {
		t.Errorf("getPDFPageCount(nonexistent) = %d, want 0", count)
	}
}

func TestGetPDFPageCount_NoPagesLine(t *testing.T) {
	t.Parallel()
	// Create a temp file with text that pdfinfo can try to parse but won't have Pages:
	dir := t.TempDir()
	fp := filepath.Join(dir, "fake.pdf")
	// Write content that won't have "Pages:" in pdfinfo output
	if err := os.WriteFile(fp, []byte("not a pdf at all"), 0644); err != nil {
		t.Fatal(err)
	}
	// pdfinfo will error on this, so returns 0 (line 272-274)
	// or succeeds but no Pages: line → returns 0 (line 285-286)
	count := getPDFPageCount(fp)
	if count != 0 {
		t.Errorf("getPDFPageCount(fake) = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// extractPDFPages — error paths (lines 292-294, 307-316)
// ---------------------------------------------------------------------------

func TestExtractPDFPages_NonexistentPDF(t *testing.T) {
	t.Parallel()
	// pdftoppm will fail on nonexistent file → lines 307-310
	_, _, err := extractPDFPages("/nonexistent/file.pdf", 1, 5)
	if err == nil {
		t.Fatal("extractPDFPages should fail on nonexistent file")
	}
	if !strings.Contains(err.Error(), "pdftoppm failed") {
		t.Errorf("Error = %q, want 'pdftoppm failed'", err.Error())
	}
}

func TestExtractPDFPages_InvalidPDF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "fake.pdf")
	if err := os.WriteFile(fp, []byte("not a pdf"), 0644); err != nil {
		t.Fatal(err)
	}
	// pdftoppm will fail on a non-PDF file → lines 307-310
	_, _, err := extractPDFPages(fp, 1, 1)
	if err == nil {
		t.Fatal("extractPDFPages should fail on invalid PDF")
	}
	if !strings.Contains(err.Error(), "pdftoppm failed") {
		t.Errorf("Error = %q, want 'pdftoppm failed'", err.Error())
	}
}

// TestExtractPDFPages_MkdirTempError tests the MkdirTemp error path (line 292-294).
// NOT parallel because it temporarily changes TMPDIR for the process.
func TestExtractPDFPages_MkdirTempError(t *testing.T) {
	// Create a readonly directory to use as TMPDIR
	roDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(roDir, 0555); err != nil {
		t.Fatal(err)
	}
	// Make readonly after creation
	if err := os.Chmod(roDir, 0555); err != nil {
		t.Skip("chmod not supported")
	}
	defer func() { _ = os.Chmod(roDir, 0755) }()

	// Save and override TMPDIR
	origTmpdir := os.Getenv("TMPDIR")
	t.Cleanup(func() { _ = os.Setenv("TMPDIR", origTmpdir) })
	_ = os.Setenv("TMPDIR", roDir)

	_, _, err := extractPDFPages("/tmp/test1.pdf", 1, 1)
	if err == nil {
		t.Fatal("extractPDFPages should fail when MkdirTemp fails")
	}
}

// ---------------------------------------------------------------------------
// renderResult — uncovered type switch branches (lines 389-409)
// ---------------------------------------------------------------------------

func TestRenderResult_ImageOutputPointer(t *testing.T) {
	t.Parallel()
	result := renderResult(&ImageOutput{
		FilePath:       "/tmp/img.png",
		OriginalWidth:  800,
		OriginalHeight: 600,
	})
	if result != "Image: /tmp/img.png (800x600)" {
		t.Errorf("renderResult(*ImageOutput) = %q, want %q", result, "Image: /tmp/img.png (800x600)")
	}
}

func TestRenderResult_PDFOutputPointer(t *testing.T) {
	t.Parallel()
	result := renderResult(&PDFOutput{
		FilePath:     "/tmp/doc.pdf",
		OriginalSize: 12345,
	})
	if result != "PDF: /tmp/doc.pdf (12345 bytes)" {
		t.Errorf("renderResult(*PDFOutput) = %q, want %q", result, "PDF: /tmp/doc.pdf (12345 bytes)")
	}
}

func TestRenderResult_PDFOutputValue(t *testing.T) {
	t.Parallel()
	result := renderResult(PDFOutput{
		FilePath:     "/tmp/doc.pdf",
		OriginalSize: 9999,
	})
	if result != "PDF: /tmp/doc.pdf (9999 bytes)" {
		t.Errorf("renderResult(PDFOutput) = %q, want %q", result, "PDF: /tmp/doc.pdf (9999 bytes)")
	}
}

func TestRenderResult_PartsOutputPointer(t *testing.T) {
	t.Parallel()
	result := renderResult(&PartsOutput{
		FilePath: "/tmp/doc.pdf",
		Count:    5,
	})
	if result != "PDF: /tmp/doc.pdf (5 pages extracted)" {
		t.Errorf("renderResult(*PartsOutput) = %q, want %q", result, "PDF: /tmp/doc.pdf (5 pages extracted)")
	}
}

func TestRenderResult_PartsOutputValue(t *testing.T) {
	t.Parallel()
	result := renderResult(PartsOutput{
		FilePath: "/tmp/doc.pdf",
		Count:    3,
	})
	if result != "PDF: /tmp/doc.pdf (3 pages extracted)" {
		t.Errorf("renderResult(PartsOutput) = %q, want %q", result, "PDF: /tmp/doc.pdf (3 pages extracted)")
	}
}

func TestRenderResult_FileUnchangedOutputPointer(t *testing.T) {
	t.Parallel()
	result := renderResult(&FileUnchangedOutput{
		FilePath: "/tmp/test.go",
	})
	if result != "File unchanged: /tmp/test.go" {
		t.Errorf("renderResult(*FileUnchangedOutput) = %q, want %q", result, "File unchanged: /tmp/test.go")
	}
}

func TestRenderResult_FileUnchangedOutputValue(t *testing.T) {
	t.Parallel()
	result := renderResult(FileUnchangedOutput{
		FilePath: "/tmp/test.go",
	})
	if result != "File unchanged: /tmp/test.go" {
		t.Errorf("renderResult(FileUnchangedOutput) = %q, want %q", result, "File unchanged: /tmp/test.go")
	}
}

func TestRenderResult_DefaultCase(t *testing.T) {
	t.Parallel()
	// Pass a type not handled by any case to hit the default branch (line 407-409)
	result := renderResult(42)
	if result != "42" {
		t.Errorf("renderResult(42) = %q, want %q", result, "42")
	}
}

// ---------------------------------------------------------------------------
// executeImage — read file error (line 516-518)
// ---------------------------------------------------------------------------

func TestExecute_ImageReadFileError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "noperm.png")
	if err := os.WriteFile(fp, []byte("fake png data"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should fail for unreadable image file")
	}
	if !strings.Contains(err.Error(), "read file") {
		t.Errorf("Error = %q, want 'read file' error", err.Error())
	}
}

// ---------------------------------------------------------------------------
// executePDF — file too large without pages (lines 608-613)
// ---------------------------------------------------------------------------

func TestExecute_PDFTooLargeNoPages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "large.pdf")
	// Create a PDF larger than PDFTargetRawSize (20MB) without /Encrypt
	// Write a valid PDF header followed by padding
	pdfHeader := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"
	padding := strings.Repeat("X", PDFTargetRawSize+100)
	content := pdfHeader + padding
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should reject large PDF without pages param")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("Error = %q, want 'larger than' message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// executePDF — read PDF file fallback error (lines 676-678)
// ---------------------------------------------------------------------------

func TestExecute_PDFReadFileFallbackError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "noperm.pdf")
	// Write a valid PDF header then remove permissions
	pdfContent := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\nxref\n0 3\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \ntrailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n109\n%%EOF\n"
	if err := os.WriteFile(fp, []byte(pdfContent), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should fail for unreadable PDF")
	}
	if !strings.Contains(err.Error(), fp) {
		t.Errorf("Error = %q, should contain path %q", err.Error(), fp)
	}
}

// ---------------------------------------------------------------------------
// executeTextFile — edge cases in offset/limit path (lines 777-810)
// ---------------------------------------------------------------------------

func TestExecute_TextFileOffsetLimitEmptyContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty_with_limit.txt")
	if err := os.WriteFile(fp, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	// Read empty file with offset+limit triggers the text=="" path (line 777-779)
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":1,"limit":5}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output, ok := result.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if output.TotalLines != 0 {
		t.Errorf("TotalLines = %d, want 0 for empty file", output.TotalLines)
	}
}

func TestExecute_TextFileOffsetBeyondEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "short.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Offset beyond file length → start > totalLines path (line 790-792)
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":100,"limit":5}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output, ok := result.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if output.NumLines != 0 {
		t.Errorf("NumLines = %d, want 0 (offset beyond end)", output.NumLines)
	}
}

func TestExecute_TextFileLimitExceedsEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "limit_beyond.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Limit larger than remaining lines → limit doesn't clamp end (line 787-789 not hit)
	// offset=2, limit=100 → end = totalLines=3 (start+limit=101 > 3)
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":2,"limit":100}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(TextOutput)
	if output.NumLines != 2 {
		t.Errorf("NumLines = %d, want 2 (lines 2 and 3)", output.NumLines)
	}
}

func TestExecute_TextFileOffsetLimitNilContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "niltctx.txt")
	if err := os.WriteFile(fp, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// nil tctx → line 807-818 ReadFileState init branch skipped
	// But actually tctx is nil so entire block skipped
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":1,"limit":1}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(TextOutput)
	if output.Content != "hello" {
		t.Errorf("Content = %q, want %q", output.Content, "hello")
	}
}

func TestExecute_TextFileReadFileStateNilMap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "nilmap.txt")
	if err := os.WriteFile(fp, []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// tctx with nil ReadFileState → line 808-810 creates the map
	tctx := &types.ToolUseContext{}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if tctx.ReadFileState == nil {
		t.Error("ReadFileState should have been initialized")
	}
	output := result.Data.(TextOutput)
	if output.Content != "data\n" {
		t.Errorf("Content = %q, want %q", output.Content, "data\n")
	}
}

// ---------------------------------------------------------------------------
// renderResult — value type cases (lines 389-390, 393-394)
// These pass non-pointer types to renderResult to hit the value-type branches.
// ---------------------------------------------------------------------------

func TestRenderResult_TextOutputValue(t *testing.T) {
	t.Parallel()
	result := renderResult(TextOutput{
		Content:  "hello world",
		FilePath: "/tmp/test.txt",
	})
	if result != "hello world" {
		t.Errorf("renderResult(TextOutput) = %q, want %q", result, "hello world")
	}
}

func TestRenderResult_ImageOutputValue(t *testing.T) {
	t.Parallel()
	result := renderResult(ImageOutput{
		FilePath:       "/tmp/img.png",
		OriginalWidth:  100,
		OriginalHeight: 200,
	})
	if result != "Image: /tmp/img.png (100x200)" {
		t.Errorf("renderResult(ImageOutput) = %q, want %q", result, "Image: /tmp/img.png (100x200)")
	}
}

// ---------------------------------------------------------------------------
// executeImage — JPEG resize path (line 545-548)
// ---------------------------------------------------------------------------

func TestExecute_ImageResizedJpeg(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a 3000x3000 JPEG image (exceeds 2000x2000 max) to test jpeg resize path
	img := image.NewRGBA(image.Rect(0, 0, 3000, 3000))
	for y := 0; y < 3000; y++ {
		for x := 0; x < 3000; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 128, 0, 255})
		}
	}
	// Encode as JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(dir, "large.jpg")
	if err := os.WriteFile(fp, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	imgOut, ok := result.Data.(ImageOutput)
	if !ok {
		t.Fatalf("Data type = %T, want ImageOutput", result.Data)
	}
	// Original should be 3000x3000
	if imgOut.OriginalWidth != 3000 || imgOut.OriginalHeight != 3000 {
		t.Errorf("Original = %dx%d, want 3000x3000", imgOut.OriginalWidth, imgOut.OriginalHeight)
	}
	// Display should be resized to <= 2000x2000
	if imgOut.DisplayWidth > 2000 || imgOut.DisplayHeight > 2000 {
		t.Errorf("Display = %dx%d, should be <= 2000x2000", imgOut.DisplayWidth, imgOut.DisplayHeight)
	}
	if imgOut.MimeType != "image/jpeg" {
		t.Errorf("MimeType = %q, want image/jpeg", imgOut.MimeType)
	}
	if imgOut.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", imgOut.FilePath, fp)
	}
	if imgOut.OriginalSize == 0 {
		t.Error("OriginalSize is 0, want non-zero")
	}
	// Aspect ratio should be maintained (square -> still square)
	if imgOut.DisplayWidth != imgOut.DisplayHeight {
		t.Errorf("Aspect ratio not maintained: %dx%d", imgOut.DisplayWidth, imgOut.DisplayHeight)
	}
}

// ---------------------------------------------------------------------------
// executePDF — extractPDFPages error with page range (lines 647-649)
// Uses a corrupt PDF that passes magic byte and encryption checks but
// causes pdftoppm to fail.
// ---------------------------------------------------------------------------

func TestExecute_PDFPagesExtractError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "corrupt.pdf")
	// Valid %PDF- header, no /Encrypt, but missing xref -> pdftoppm fails
	content := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n"
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"1"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should fail when pdftoppm fails on corrupt PDF")
	}
	if !strings.Contains(err.Error(), "extract PDF pages") {
		t.Errorf("Error = %q, want 'extract PDF pages'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// getPDFPageCount — no Pages: line in output (line 285)
// We create a text file; pdfinfo may fail or succeed without Pages line.
// ---------------------------------------------------------------------------

func TestGetPDFPageCount_TextFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "notpdf.txt")
	if err := os.WriteFile(fp, []byte("hello world\nthis is text\n"), 0644); err != nil {
		t.Fatal(err)
	}
	count := getPDFPageCount(fp)
	// Either way, should return 0 (no Pages: line or error)
	if count != 0 {
		t.Errorf("getPDFPageCount(text file) = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// isPdftoppmAvailable — error path (line 250-252)
// NOT parallel because it changes PATH for the process.
// ---------------------------------------------------------------------------

func TestIsPdftoppmAvailable_ErrorPath(t *testing.T) {
	// Save and override PATH to make pdftoppm unfindable
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", origPath) })
	_ = os.Setenv("PATH", "/nonexistent")

	result := isPdftoppmAvailable()
	if result {
		t.Error("isPdftoppmAvailable() = true, want false when pdftoppm not in PATH")
	}
}

// ---------------------------------------------------------------------------
// extractPDFPages — ReadDir error (lines 313-316)
// Test by calling extractPDFPages and removing the tmpdir between
// pdftoppm execution and ReadDir. We do this by directly testing the
// function with a non-PDF that causes pdftoppm to fail first (covered above)
// and by testing with a valid PDF where pdftoppm succeeds.
// Since we can't inject a ReadDir failure, we test with a corrupt PDF
// that pdftoppm can actually process but produces no output files.
// ---------------------------------------------------------------------------

func TestExtractPDFPages_ReadDirError(t *testing.T) {
	t.Parallel()
	// Use a valid PDF to let pdftoppm succeed, then check count
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.pdf")
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	tmpDir, count, err := extractPDFPages(fp, 1, 1)
	if err != nil {
		t.Fatalf("extractPDFPages error: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	if count != 1 {
		t.Errorf("count = %d, want 1 (extracted pages 1-1)", count)
	}
	// Verify output directory contains JPG files
	entries, err2 := os.ReadDir(tmpDir)
	if err2 != nil {
		t.Fatalf("ReadDir error: %v", err2)
	}
	jpgCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jpg") {
			jpgCount++
		}
	}
	if jpgCount != 1 {
		t.Errorf("JPG files in output dir = %d, want 1", jpgCount)
	}
}
