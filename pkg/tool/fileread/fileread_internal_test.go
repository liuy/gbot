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
	"strconv"
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

func TestRenderResult_DecodedWireText(t *testing.T) {
	t.Parallel()
	tt := New()
	// TUI replay path: marshaled wire array → DecodeResult unwraps the text
	// block → renderResult returns the line-numbered content for the
	// collapse view.
	raw := json.RawMessage(`[{"type":"text","text":"1\thello world"}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	result := renderResult(v)
	if result != "1\thello world" {
		t.Errorf("renderResult(decoded) = %q, want %q", result, "1\thello world")
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
	original := &TextOutput{Type: "text", FilePath: "/tmp/x.go", Content: "hello", StartLine: 3, NumLines: 1, TotalLines: 10}
	raw, err := json.Marshal(tt.(tool.ToolWithWireBlocks).FormatWireBlocks(original))
	if err != nil {
		t.Fatalf("Marshal wire: %v", err)
	}
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*TextOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TextOutput", v)
	}
	if got.Content != "3\thello" {
		t.Errorf("Content = %q, want %q (StartLine-prefixed wire text)", got.Content, "3\thello")
	}
}

func TestDecodeResult_LegacyJSONWireStillDecodes(t *testing.T) {
	t.Parallel()
	tt := New()
	// Sessions recorded before the line-numbered wire stored the whole
	// TextOutput struct as single-line JSON in the text block.
	inner := `{"type":"text","content":"hello","filePath":"/tmp/x.go","numLines":1}`
	raw := json.RawMessage(`[{"type":"text","text":` + strconv.Quote(inner) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*TextOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TextOutput", v)
	}
	if got.Content != "hello" || got.FilePath != "/tmp/x.go" {
		t.Errorf("legacy decode lost fields: %+v", got)
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

	// New wire: the plain-text stub decodes as TextOutput carrying the stub
	// itself (type info is display-only on replay).
	wire := tt.(tool.ToolWithWireBlocks).FormatWireBlocks(FileUnchangedOutput{Type: "file_unchanged", FilePath: "/tmp/x.go"})
	if len(wire) != 1 || wire[0].Type != "text" {
		t.Fatalf("wire = %+v, want single text block", wire)
	}
	if wire[0].Text != "File unchanged since last read. The content from the earlier Read tool_result in this conversation is still current — refer to that instead of re-reading." {
		t.Errorf("stub text = %q, want TS FILE_UNCHANGED_STUB verbatim", wire[0].Text)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("Marshal wire: %v", err)
	}
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*TextOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TextOutput", v)
	}
	if got.Content != wire[0].Text {
		t.Errorf("Content = %q, want the stub text verbatim", got.Content)
	}

	// Legacy wire: struct JSON still decodes to *FileUnchangedOutput.
	inner := `{"type":"file_unchanged","filePath":"/tmp/x.go"}`
	legacyRaw := json.RawMessage(`[{"type":"text","text":` + strconv.Quote(inner) + `}]`)
	lv, err := tt.(tool.ToolWithDecodeResult).DecodeResult(legacyRaw)
	if err != nil {
		t.Fatalf("DecodeResult(legacy): %v", err)
	}
	lgot, ok := lv.(*FileUnchangedOutput)
	if !ok {
		t.Fatalf("DecodeResult(legacy) returned %T, want *FileUnchangedOutput", lv)
	}
	if lgot.FilePath != "/tmp/x.go" {
		t.Errorf("legacy decode lost fields: %+v", lgot)
	}
}

func TestFileRead_DecodeResult_RejectsBareStruct(t *testing.T) {
	t.Parallel()

	tt := New()
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{"content":"hello","file_path":"/tmp/x.go","num_lines":1}`))
	if err == nil {
		t.Error("DecodeResult must reject bare struct form")
	}
}

// ---------------------------------------------------------------------------
// FormatWireBlocks — text wire
// TS source: FileReadTool.ts mapToolResultToToolResultBlockParam 'text' and
// 'file_unchanged' cases; line numbers via utils/file.ts addLineNumbers.
// ---------------------------------------------------------------------------

func TestFormatWireBlocks_TextLineNumbers(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)

	blocks := wb.FormatWireBlocks(TextOutput{Type: "text", Content: "alpha\nbeta", StartLine: 1, TotalLines: 2})
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("blocks = %+v, want single text block", blocks)
	}
	if blocks[0].Text != "1\talpha\n2\tbeta" {
		t.Errorf("Text = %q, want %q", blocks[0].Text, "1\talpha\n2\tbeta")
	}

	// Pointer form takes the same branch (engine may hand either shape).
	pblocks := wb.FormatWireBlocks(&TextOutput{Type: "text", Content: "alpha\nbeta", StartLine: 1})
	if len(pblocks) != 1 || pblocks[0].Text != "1\talpha\n2\tbeta" {
		t.Errorf("pointer form Text = %+v, want numbered text", pblocks)
	}
}

func TestFormatWireBlocks_TextLineNumbersFromStartLine(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	// Offset reads: StartLine carries the 1-based offset (executeTextFile),
	// so numbering continues from it rather than restarting at 1.
	blocks := wb.FormatWireBlocks(&TextOutput{Type: "text", Content: "alpha\nbeta", StartLine: 5, TotalLines: 20})
	if blocks[0].Text != "5\talpha\n6\tbeta" {
		t.Errorf("Text = %q, want %q", blocks[0].Text, "5\talpha\n6\tbeta")
	}
}

func TestFormatWireBlocks_TextTrailingNewline(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	// TS split(/\r?\n/) keeps the empty element a final newline produces and
	// numbers it — off-by-one here would corrupt every offset read.
	blocks := wb.FormatWireBlocks(&TextOutput{Type: "text", Content: "a\nb\n", StartLine: 1, TotalLines: 2})
	if blocks[0].Text != "1\ta\n2\tb\n3\t" {
		t.Errorf("Text = %q, want %q", blocks[0].Text, "1\ta\n2\tb\n3\t")
	}
}

func TestFormatWireBlocks_TextCRLFContent(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	// /\r?\n/ treats CRLF as one separator. Only executeTextFile normalizes
	// \r away; markitdown/sqlite/archive content can still carry CRLF.
	blocks := wb.FormatWireBlocks(&TextOutput{Type: "text", Content: "a\r\nb", StartLine: 1})
	if blocks[0].Text != "1\ta\n2\tb" {
		t.Errorf("Text = %q, want %q", blocks[0].Text, "1\ta\n2\tb")
	}
}

func TestFormatWireBlocks_TextEmptyFileWarning(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	// executeTextFile emits Content:"" TotalLines:0 for an existing empty file.
	blocks := wb.FormatWireBlocks(&TextOutput{Type: "text", Content: "", StartLine: 1, TotalLines: 0})
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("blocks = %+v, want single text block", blocks)
	}
	want := "<system-reminder>Warning: the file exists but the contents are empty.</system-reminder>"
	if blocks[0].Text != want {
		t.Errorf("Text = %q, want %q", blocks[0].Text, want)
	}
}

func TestFormatWireBlocks_TextOffsetBeyondEndWarning(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	// Offset past EOF: Content:"" with StartLine = requested offset,
	// TotalLines = actual count (executeTextFile clamps only the slice).
	blocks := wb.FormatWireBlocks(&TextOutput{Type: "text", Content: "", StartLine: 100, TotalLines: 2})
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("blocks = %+v, want single text block", blocks)
	}
	want := "<system-reminder>Warning: the file exists but is shorter than the provided offset (100). The file has 2 lines.</system-reminder>"
	if blocks[0].Text != want {
		t.Errorf("Text = %q, want %q", blocks[0].Text, want)
	}
}

func TestFormatWireBlocks_FileUnchangedStubPointer(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	blocks := wb.FormatWireBlocks(&FileUnchangedOutput{Type: "file_unchanged", FilePath: "/tmp/x.go"})
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("blocks = %+v, want single text block", blocks)
	}
	want := "File unchanged since last read. The content from the earlier Read tool_result in this conversation is still current — refer to that instead of re-reading."
	if blocks[0].Text != want {
		t.Errorf("Text = %q, want %q", blocks[0].Text, want)
	}
}

func TestFormatWireBlocks_ImagePointer(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	// Engine paths may hand the pointer form; it must take the image branch.
	blocks := wb.FormatWireBlocks(&ImageOutput{Type: "image", MimeType: "image/png", Base64: "abc"})
	if len(blocks) != 1 || blocks[0].Type != "image" {
		t.Fatalf("blocks = %+v, want single image block", blocks)
	}
	if blocks[0].Source == nil || blocks[0].Source.MediaType != "image/png" || blocks[0].Source.Data != "abc" {
		t.Errorf("source = %+v, want base64 image/png with data %q", blocks[0].Source, "abc")
	}
}

func TestFormatWireBlocks_UnknownTypeJSONFallback(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	// Outputs outside the union still fall back to JSON text (pre-existing
	// behavior for defensive callers).
	blocks := wb.FormatWireBlocks("plain")
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("blocks = %+v, want single text block", blocks)
	}
	if blocks[0].Text != `"plain"` {
		t.Errorf("Text = %q, want %q", blocks[0].Text, `"plain"`)
	}
}

// ---------------------------------------------------------------------------
// DecodeResult — dual-format (legacy JSON vs line-numbered plain text)
// ---------------------------------------------------------------------------

func TestDecodeResult_PlainTextWire(t *testing.T) {
	t.Parallel()
	tt := New()
	raw := json.RawMessage(`[{"type":"text","text":"1\thello\n2\tworld"}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*TextOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TextOutput", v)
	}
	if got.Content != "1\thello\n2\tworld" {
		t.Errorf("Content = %q, want wire text verbatim", got.Content)
	}
}

func TestDecodeResult_JSONFileContentNotMisjudged(t *testing.T) {
	t.Parallel()
	tt := New()
	// Reading a file whose content is itself JSON: the wire prefixes line
	// numbers, so the text starts with "1\t{" — the legacy probe must not
	// swallow it.
	inner := "1\t" + `{"type":"text","content":"poison","filePath":"/tmp/evil"}`
	raw := json.RawMessage(`[{"type":"text","text":` + strconv.Quote(inner) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*TextOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TextOutput", v)
	}
	if got.Content != inner {
		t.Errorf("Content = %q, want %q (must not enter legacy JSON path)", got.Content, inner)
	}
	if got.FilePath != "" {
		t.Errorf("FilePath = %q, want empty (legacy fields must stay untouched)", got.FilePath)
	}
}

func TestDecodeResult_UnrecognizedLegacyTypeStillRejected(t *testing.T) {
	t.Parallel()
	tt := New()
	// Object with a "type" field that is neither text nor file_unchanged is
	// legacy-shaped garbage — preserved current behavior: no decode.
	raw := json.RawMessage(`[{"type":"text","text":` + strconv.Quote(`{"type":"notebook","cells":[]}`) + `}]`)
	if _, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw); err == nil {
		t.Error("DecodeResult must reject unrecognized legacy type")
	}
}

func TestDecodeResult_LongNonArrayPreviewTruncated(t *testing.T) {
	t.Parallel()
	tt := New()
	long := `"` + strings.Repeat("x", 200) + `"`
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(long))
	if err == nil {
		t.Fatal("DecodeResult must reject non-array content")
	}
	want := "fileread: DecodeResult expects array-form content, got " + strconv.Quote(`"`+strings.Repeat("x", 79))
	if err.Error() != want {
		t.Errorf("Error = %.100s..., want preview truncated to 80 chars", err.Error())
	}
}

func TestDecodeResult_MalformedArray(t *testing.T) {
	t.Parallel()
	tt := New()
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`[{not json}]`))
	if err == nil {
		t.Fatal("DecodeResult must reject malformed array JSON")
	}
}

func TestDecodeResult_SkipsEmptyAndNonTextBlocks(t *testing.T) {
	t.Parallel()
	tt := New()
	// Empty text block and a non-text block are skipped; the numbered text
	// block after them still decodes.
	raw := json.RawMessage(`[{"type":"text","text":""},{"type":"thinking","thinking":"x"},{"type":"text","text":"1\thello"}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*TextOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TextOutput", v)
	}
	if got.Content != "1\thello" {
		t.Errorf("Content = %q, want %q", got.Content, "1\thello")
	}
}

// ---------------------------------------------------------------------------
// addLineNumbers — TS utils/file.ts compact format
// ---------------------------------------------------------------------------

func TestAddLineNumbers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		content   string
		startLine int
		want      string
	}{
		{"empty", "", 1, ""},
		{"single line", "hello", 1, "1\thello"},
		{"multi line", "a\nb", 1, "1\ta\n2\tb"},
		{"trailing newline", "a\n", 1, "1\ta\n2\t"},
		{"start line offset", "a\nb", 10, "10\ta\n11\tb"},
		{"crlf separator", "a\r\nb", 1, "1\ta\n2\tb"},
		{"lone carriage return stays", "a\rb", 1, "1\ta\rb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := addLineNumbers(tc.content, tc.startLine); got != tc.want {
				t.Errorf("addLineNumbers(%q, %d) = %q, want %q", tc.content, tc.startLine, got, tc.want)
			}
		})
	}
}
