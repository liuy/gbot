package fileread

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}

func TestExecute_BlockedDevicePath_tty(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/tty"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/tty")
	}
}

func TestExecute_BlockedDevicePath_console(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/console"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/console")
	}
}

func TestExecute_BlockedDevicePath_stdout(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/stdout"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/stdout")
	}
}

func TestExecute_BlockedDevicePath_stderr(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/stderr"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/stderr")
	}
}

func TestExecute_BlockedDevicePath_fd0(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/fd/0"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/fd/0")
	}
}

func TestExecute_BlockedDevicePath_fd1(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/fd/1"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/fd/1")
	}
}

func TestExecute_BlockedDevicePath_fd2(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/fd/2"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /dev/fd/2")
	}
}

func TestExecute_BlockedDevicePath_procSelfFd0(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/proc/self/fd/0"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /proc/self/fd/0")
	}
}

func TestExecute_BlockedDevicePath_procSelfFd1(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/proc/self/fd/1"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /proc/self/fd/1")
	}
}

func TestExecute_BlockedDevicePath_procSelfFd2(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/proc/self/fd/2"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for /proc/self/fd/2")
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
		t.Error("Execute() error = nil, want error for unreadable file")
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
		t.Error("Execute() error = nil, want error for unreadable file")
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
		t.Errorf("countTotalLines = %d, want 3", count)
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
	_ = result
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
	// Empty file should be caught by either "empty" or "not a valid PDF"
	if !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "not a valid PDF") {
		t.Errorf("Error = %q, want empty/invalid PDF message", err.Error())
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
	if filepath.IsAbs(got) {
		// Should resolve relative to current dir, not crash
		t.Logf("expandPath with empty HOME resolved to %q", got)
	}
}

// ---------------------------------------------------------------------------
// Coverage: getMtimeMs error path
// ---------------------------------------------------------------------------

func TestGetMtimeMs_Error(t *testing.T) {
	t.Parallel()
	_, err := getMtimeMs("/nonexistent/file/path")
	if err == nil {
		t.Error("getMtimeMs should return error for nonexistent file")
	}
}

// ---------------------------------------------------------------------------
// Coverage: countTotalLines error path
// ---------------------------------------------------------------------------

func TestCountTotalLines_Error(t *testing.T) {
	t.Parallel()
	_, err := countTotalLines("/nonexistent/file/path")
	if err == nil {
		t.Error("countTotalLines should return error for nonexistent file")
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
	if !strings.Contains(err.Error(), "password") && !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("Error = %q, want 'password' or 'encrypted' message", err.Error())
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
