package fileread

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
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
		path string
		want bool
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

// --- byte-truthful MIME (replaces former TestGetMimeType) ---
// executeImage must emit an ImageOutput.MimeType that matches the actual
// bytes produced by utils.MaybeResizeAndDownsampleImageBuffer. The legacy
// getMimeType(ext) derived MIME from the file extension; that path is gone.
// This test pins the new contract: PNG/JPEG/GIF small images pass through
// with their original MIME type.
func TestExecute_ImageMimeTypeByteTruthful(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		filename   string
		encode     func(*testing.T, image.Image) []byte
		wantMime   string
		wantWidth  int
		wantHeight int
	}{
		{
			name:     "png",
			filename: "img.png",
			encode: func(t *testing.T, img image.Image) []byte {
				var buf bytes.Buffer
				if err := png.Encode(&buf, img); err != nil {
					t.Fatalf("png.Encode: %v", err)
				}
				return buf.Bytes()
			},
			wantMime:   "image/png",
			wantWidth:  50,
			wantHeight: 50,
		},
		{
			name:     "jpeg",
			filename: "img.jpg",
			encode: func(t *testing.T, img image.Image) []byte {
				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
					t.Fatalf("jpeg.Encode: %v", err)
				}
				return buf.Bytes()
			},
			wantMime:   "image/jpeg",
			wantWidth:  50,
			wantHeight: 50,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			img := image.NewRGBA(image.Rect(0, 0, tc.wantWidth, tc.wantHeight))
			for y := range tc.wantHeight {
				for x := range tc.wantWidth {
					img.SetRGBA(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
				}
			}
			data := tc.encode(t, img)
			dir := t.TempDir()
			fp := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(fp, data, 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			info, err := os.Stat(fp)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			res, err := executeImage(Input{FilePath: fp}, info)
			if err != nil {
				t.Fatalf("executeImage: %v", err)
			}
			out, ok := res.Data.(ImageOutput)
			if !ok {
				t.Fatalf("Data = %T, want ImageOutput", res.Data)
			}
			if out.MimeType != tc.wantMime {
				t.Errorf("MimeType = %q, want %q", out.MimeType, tc.wantMime)
			}
			if out.OriginalWidth != tc.wantWidth || out.OriginalHeight != tc.wantHeight {
				t.Errorf("Original = %dx%d, want %dx%d",
					out.OriginalWidth, out.OriginalHeight, tc.wantWidth, tc.wantHeight)
			}
			if out.DisplayWidth != tc.wantWidth || out.DisplayHeight != tc.wantHeight {
				t.Errorf("Display = %dx%d, want %dx%d (no resize expected)",
					out.DisplayWidth, out.DisplayHeight, tc.wantWidth, tc.wantHeight)
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

// ---------------------------------------------------------------------------
// Coverage: output() interface methods
// ---------------------------------------------------------------------------

func TestOutputMethods(t *testing.T) {
	t.Parallel()
	// Call all output() interface methods to cover the 0% functions
	TextOutput{}.output()
	ImageOutput{}.output()
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
	// Not parallel — modifies global HOME env var, races with TestExpandPath_TildeWithHome
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

func TestRenderResult_JSONRawMessage(t *testing.T) {
	t.Parallel()
	tt := New()
	// TUI passes marshaled output as json.RawMessage. DecodeResult recovers
	// the concrete type, then renderResult extracts the content field.
	raw := json.RawMessage(`{"content":"hello world","file_path":"/tmp/x.go","num_lines":1}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	result := renderResult(v)
	if result != "hello world" {
		t.Errorf("renderResult(decoded) = %q, want %q", result, "hello world")
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
	tctx := &tool.ToolUseContext{}
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

func TestRenderResult_TextOutputPointer(t *testing.T) {
	t.Parallel()
	result := renderResult(&TextOutput{
		Content:  "hello world",
		FilePath: "/tmp/test.txt",
		NumLines: 42,
	})
	if result != "hello world" {
		t.Errorf("renderResult(*TextOutput) = %q, want %q", result, "hello world")
	}
}

func TestRenderResult_TextOutputValue(t *testing.T) {
	t.Parallel()
	result := renderResult(TextOutput{
		Content:  "hello world",
		FilePath: "/tmp/test.txt",
		NumLines: 1,
	})
	if result != "hello world" {
		t.Errorf("renderResult(TextOutput) = %q, want %q", result, "hello world")
	}
}

func TestRenderResult_TextOutputZeroLines(t *testing.T) {
	t.Parallel()
	result := renderResult(&TextOutput{
		Content:  "",
		FilePath: "/tmp/empty.txt",
		NumLines: 0,
	})
	if result != "" {
		t.Errorf("renderResult(*TextOutput zero lines) = %q, want %q", result, "")
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
	for y := range 3000 {
		for x := range 3000 {
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

// TestOutput_InterfaceConformance verifies all output types satisfy the
// Output interface. This exercises the marker methods that are otherwise
// never called directly (0% coverage otherwise).
func TestOutput_InterfaceConformance(t *testing.T) {
	var _ Output = TextOutput{}
	var _ Output = ImageOutput{}
	var _ Output = FileUnchangedOutput{}

	// Also verify the methods are callable through the interface.
	out := []Output{
		TextOutput{Type: "text"},
		ImageOutput{Type: "image"},
		FileUnchangedOutput{},
	}
	for _, o := range out {
		o.output()
	}
}

func TestDecodeResult_TextOutputRoundTrip(t *testing.T) {
	t.Parallel()
	tt := New()
	original := &TextOutput{Type: "text", FilePath: "/tmp/x.go", Content: "hello", NumLines: 1}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*TextOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TextOutput", v)
	}
	if got.Content != "hello" || got.FilePath != "/tmp/x.go" {
		t.Errorf("round-trip lost fields: %+v", got)
	}
	if tt.RenderResult(original) != tt.RenderResult(v) {
		t.Error("stream and history render differ")
	}
}

func TestDecodeResult_ImageOutputRoundTrip(t *testing.T) {
	t.Parallel()
	tt := New()
	// Array form: image-only result, single image block.
	raw := json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*ImageOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *ImageOutput", v)
	}
	if got.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want %q", got.MimeType, "image/png")
	}
	// FilePath/dims are NOT recoverable from the wire format (locked-in fact #2).
	if got.FilePath != "" {
		t.Errorf("FilePath = %q, want empty (not recoverable from array form)", got.FilePath)
	}
}

func TestRenderResult_ImageNoDims(t *testing.T) {
	t.Parallel()
	// ImageOutput with no FilePath/dims (the shape DecodeResult recovers from
	// array form) renders as "Image (<mime>)" rather than the dim path.
	result := renderResult(&ImageOutput{Type: "image", MimeType: "image/png"})
	if result != "Image (image/png)" {
		t.Errorf("renderResult(no-dims ImageOutput) = %q, want %q", result, "Image (image/png)")
	}
}

func TestDecodeResult_FileUnchangedOutputRoundTrip(t *testing.T) {
	t.Parallel()
	tt := New()
	original := &FileUnchangedOutput{Type: "file_unchanged", FilePath: "/tmp/x.go"}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*FileUnchangedOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *FileUnchangedOutput", v)
	}
	if got.FilePath != "/tmp/x.go" {
		t.Errorf("round-trip lost fields: %+v", got)
	}
	if tt.RenderResult(original) != tt.RenderResult(v) {
		t.Error("stream and history render differ")
	}
}
