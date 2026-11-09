package fileread

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/markitdown"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/utils"
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

// TestExecute_RelativePathJoinsWorkingDir pins Execute's path-resolution entry:
// a relative file_path is joined onto tctx.WorkingDir once, before the
// sqlite/archive selectors and the stat, so every downstream branch (stat, image,
// document, text) sees the same resolved string. The nil-tctx control proves the
// join is the only relative-path resolver here — without it the path falls
// through to the process cwd and misses the file. The tilde case pins the other
// half: the join guard skips "~/", so only the trailing expandPath can resolve it.
// Not parallel: HOME is process-global.
func TestExecute_RelativePathJoinsWorkingDir(t *testing.T) {
	dir := t.TempDir()
	const marker = "WORKDIRMARK-6h"
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile marker.txt: %v", err)
	}

	res, err := Execute(context.Background(), json.RawMessage(`{"file_path":"marker.txt"}`), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Execute with WorkingDir returned error: %v", err)
	}
	text, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data = %T, want TextOutput", res.Data)
	}
	if !strings.Contains(text.Content, marker) {
		t.Errorf("Content = %q, want the marker %q from WorkingDir", text.Content, marker)
	}

	// Control: no tctx means no WorkingDir, so the relative path resolves
	// against the process cwd and misses the file. Match only the error prefix —
	// expandPath makes the reported path absolute.
	if _, err := Execute(context.Background(), json.RawMessage(`{"file_path":"marker.txt"}`), nil); err == nil {
		t.Error("Execute without tctx returned nil error, want 'file does not exist'")
	} else if !strings.Contains(err.Error(), "file does not exist") {
		t.Errorf("error = %q, want 'file does not exist'", err.Error())
	}

	// The IsAbs half of the guard: an absolute path must pass through a set
	// WorkingDir untouched, otherwise the join re-roots it under WorkingDir.
	absDir := t.TempDir()
	absPath := filepath.Join(absDir, "abs.txt")
	if err := os.WriteFile(absPath, []byte("ABSMARK-4t\n"), 0o644); err != nil {
		t.Fatalf("WriteFile abs.txt: %v", err)
	}
	res, err = Execute(context.Background(), json.RawMessage(`{"file_path":"`+absPath+`"}`), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Execute with an absolute path and WorkingDir set returned error: %v", err)
	}
	text, ok = res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data = %T, want TextOutput", res.Data)
	}
	if !strings.Contains(text.Content, "ABSMARK-4t") {
		t.Errorf("Content = %q, want ABSMARK-4t from the absolute path %q", text.Content, absPath)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "tilde.txt"), []byte("TILDEMARK-2b\n"), 0o644); err != nil {
		t.Fatalf("WriteFile tilde.txt: %v", err)
	}
	res, err = Execute(context.Background(), json.RawMessage(`{"file_path":"~/tilde.txt"}`), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Execute with ~/tilde.txt returned error: %v", err)
	}
	text, ok = res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data = %T, want TextOutput", res.Data)
	}
	if !strings.Contains(text.Content, "TILDEMARK-2b") {
		t.Errorf("Content = %q, want TILDEMARK-2b from the HOME dir", text.Content)
	}
}

// TestExecute_BareTildeExpandsToHome pins the TS path.ts branch where a path
// that is exactly "~" means the home directory, not a file named "~". The
// WorkingDir join has to skip it: once joined, the string is absolute and no
// later tilde expansion can recover the intent. Read refuses directories, so
// the observable proof is the stat verdict naming the HOME dir instead of the
// joined path.
// Not parallel: HOME is process-global.
func TestExecute_BareTildeExpandsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()

	_, err := Execute(context.Background(), json.RawMessage(`{"file_path":"~"}`), &tool.ToolUseContext{WorkingDir: dir})
	want := "path is a directory, not a file: " + home
	if err == nil {
		t.Fatalf(`Execute with file_path "~" returned nil error, want %q`, want)
	}
	if err.Error() != want {
		t.Errorf(`Execute with file_path "~" error = %q, want %q`, err.Error(), want)
	}
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
	// Create a 2001x2001 JPEG image (minimally exceeds the 2000x2000 cap;
	// smaller fixtures keep this suite fast under the race detector).
	// NewRGBA's zero value is a uniform image — no pixel fill loop needed.
	img := image.NewRGBA(image.Rect(0, 0, 2001, 2001))
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
	// Original should be 2001x2001
	if imgOut.OriginalWidth != 2001 || imgOut.OriginalHeight != 2001 {
		t.Errorf("Original = %dx%d, want 2001x2001", imgOut.OriginalWidth, imgOut.OriginalHeight)
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

// Pin tests for the fileread.go mutation sweep; each assertion is exact so a
// single changed byte flips the test. Mutant line:mutator pairs are tracked in
// the campaign ledger.

// ---------------------------------------------------------------------------
// extension maps
// ---------------------------------------------------------------------------

func TestPin_ImageExtensionsJpegWebp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A real JPEG under .jpeg must take the image path.
	jpegPath := filepath.Join(dir, "photo.jpeg")
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jpegPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: jpegPath}), nil)
	if err != nil {
		t.Fatalf("jpeg read: %v", err)
	}
	io, ok := res.Data.(ImageOutput)
	if !ok {
		t.Fatalf("jpeg Data type = %T, want ImageOutput", res.Data)
	}
	if io.Type != "image" || io.MimeType != "image/jpeg" {
		t.Errorf("jpeg output = %+v, want image/image/jpeg", io)
	}

	// Undecodable bytes under .webp must fail in the image path ("decode
	// image"), not fall through to the text path ("null bytes").
	webpPath := filepath.Join(dir, "pic.webp")
	if err := os.WriteFile(webpPath, []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: webpPath}), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "decode image:") {
		t.Fatalf("webp err = %v, want 'decode image:' prefix", err)
	}
}

// Binary extensions that are reachable through Execute (not shadowed by the
// convertible map or intercepted by the archive path).
func TestPin_BinaryExtensionRejected(t *testing.T) {
	t.Parallel()
	exts := []string{
		".ico", ".bmp", ".svg", ".mp3", ".mp4", ".wav", ".avi", ".mov", ".mkv",
		".exe", ".dll", ".so", ".dylib", ".a", ".o", ".obj", ".class", ".pyc",
		".par", ".pickle", ".whl",
	}
	dir := t.TempDir()
	for _, ext := range exts {
		p := filepath.Join(dir, "f"+ext)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
		want := fmt.Sprintf("file has binary extension %s and cannot be read as text: %s", ext, p)
		if err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", ext, err, want)
		}
	}
}

func TestPin_ConvertibleExtensionsRouted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// NUL byte content: convertible routing succeeds via markitdown
	// passthrough/convert, while a fall-through to the text path would be
	// rejected as binary. The .pdf case errors inside the converter.
	tt := []struct {
		ext      string
		wantErr  string // "" means success
		content  string
		validate func(t *testing.T, res *tool.ToolResult)
	}{
		{ext: ".pdf", content: "%PDF-garbage", wantErr: "convert .pdf:"},
		{ext: ".csv", content: "h\nv1\nv2", validate: func(t *testing.T, res *tool.ToolResult) {
			to, ok := res.Data.(TextOutput)
			if !ok || to.Content != "| h |\n| --- |\n| v1 |\n| v2 |" {
				t.Errorf("csv output = %#v, want markdown table", res.Data)
			}
		}},
	}
	for _, c := range tt {
		p := filepath.Join(dir, "f"+c.ext)
		if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
		if c.wantErr != "" {
			if err == nil || !strings.HasPrefix(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want prefix %q", c.ext, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.ext, err)
			continue
		}
		c.validate(t, res)
	}
	// Passthrough/office converters reject NUL content inside the converter
	// ("convert .ext:"), proving the convertible branch was taken — a
	// fall-through would hit the binary-extension or null-bytes errors.
	for _, ext := range []string{".docx", ".epub", ".ipynb", ".doc", ".ppt", ".pptx", ".xls", ".xlsx"} {
		p := filepath.Join(dir, "g"+ext)
		if err := os.WriteFile(p, []byte("a\x00b"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
		if err == nil || !strings.HasPrefix(err.Error(), "convert "+ext+":") {
			t.Errorf("%s with NUL content: err = %v, want 'convert %s:' prefix", ext, err, ext)
		}
	}
}

// ---------------------------------------------------------------------------
// device-path guard, expandPath, renderResult
// ---------------------------------------------------------------------------

func TestPin_BlockedDevicePathGuard(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"/srv/fd/0", "/var/tmp/fd/1", "/proc/fd", "/x/fd/3"} {
		if isBlockedDevicePath(p) {
			t.Errorf("isBlockedDevicePath(%q) = true, want false", p)
		}
	}
	for _, p := range []string{"/proc/self/fd/0", "/proc/123/fd/2", "/dev/zero", "/dev/stdout"} {
		if !isBlockedDevicePath(p) {
			t.Errorf("isBlockedDevicePath(%q) = false, want true", p)
		}
	}
}

func TestPin_ExpandPath(t *testing.T) {
	// No t.Parallel: t.Setenv.
	// Non-canonical absolute paths must pass through verbatim (filepath.Abs
	// would Clean them, which would merge distinct dedup keys).
	if got := expandPath("/tmp/x/../y"); got != "/tmp/x/../y" {
		t.Errorf("expandPath(/tmp/x/../y) = %q", got)
	}
	t.Setenv("HOME", "/home/pin")
	if got := expandPath("~/notes/a.txt"); got != "/home/pin/notes/a.txt" {
		t.Errorf("expandPath(~/notes/a.txt) = %q, want /home/pin/notes/a.txt", got)
	}
	if got := expandPath("~"); got != "/home/pin" {
		t.Errorf("expandPath(~) = %q, want /home/pin", got)
	}
}

func TestPin_RenderResultImageWithoutPath(t *testing.T) {
	t.Parallel()
	if got := renderResult(&ImageOutput{MimeType: "image/png"}); got != "Image (image/png)" {
		t.Errorf("renderResult(&ImageOutput{}) = %q, want 'Image (image/png)'", got)
	}
	if got := renderResult(ImageOutput{MimeType: "image/gif"}); got != "Image (image/gif)" {
		t.Errorf("renderResult(ImageOutput{}) = %q, want 'Image (image/gif)'", got)
	}
	if got := renderResult(&ImageOutput{FilePath: "/a.png", MimeType: "image/png", OriginalWidth: 3, OriginalHeight: 4}); got != "Image: /a.png (3x4)" {
		t.Errorf("renderResult image with path = %q", got)
	}
}

// ---------------------------------------------------------------------------
// ToolDef metadata
// ---------------------------------------------------------------------------

func TestPin_ToolDefMetadata(t *testing.T) {
	t.Parallel()
	tl := New()
	if tl.Name() != "Read" {
		t.Errorf("Name() = %q, want Read", tl.Name())
	}
	aliases := tl.Aliases()
	if len(aliases) != 3 || aliases[0] != "fileread" || aliases[1] != "read" || aliases[2] != "cat" {
		t.Errorf("Aliases() = %v, want [fileread read cat]", aliases)
	}
	var schema map[string]any
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["file_path"]; !ok {
		t.Errorf("InputSchema missing file_path property: %v", schema)
	}
	desc, err := tl.Description(json.RawMessage(`{"file_path":"/x.go"}`))
	if err != nil || desc != "/x.go" {
		t.Errorf("Description = (%q, %v), want (/x.go, nil)", desc, err)
	}
	if tl.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", tl.InterruptBehavior())
	}
	if !strings.Contains(tl.Prompt(), "Reads a file from the local filesystem") {
		t.Errorf("Prompt() = %.60q, want the fileread prompt", tl.Prompt())
	}
	// Call routes through Call_: a file read must come back with content.
	dir := t.TempDir()
	p := filepath.Join(dir, "call.txt")
	if err := os.WriteFile(p, []byte("callbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := tl.Call(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if to, ok := res.Data.(TextOutput); !ok || to.Content != "callbody" {
		t.Errorf("Call result = %#v, want callbody text", res.Data)
	}
	if !tl.IsReadOnly(nil) || !tl.IsConcurrencySafe(nil) {
		t.Error("IsReadOnly/IsConcurrencySafe must be true")
	}
	if got := tl.CheckPermissions(json.RawMessage(`{"file_path":"/x"}`), nil); got == nil {
		t.Error("CheckPermissions returned nil decision")
	}
}

// ---------------------------------------------------------------------------
// DecodeResult
// ---------------------------------------------------------------------------

func decodeResult(t *testing.T, raw string) (any, error) {
	t.Helper()
	dr := New().(tool.ToolWithDecodeResult)
	return dr.DecodeResult([]byte(raw))
}

func TestPin_DecodeResultArrayGuard(t *testing.T) {
	t.Parallel()
	// Empty raw must be rejected by the length guard, not panic on raw[0].
	_, err := decodeResult(t, "")
	if err == nil || !strings.Contains(err.Error(), "expects array-form content") {
		t.Fatalf("empty raw err = %v, want array-form message", err)
	}
	// Non-array: message carries an 80-char truncated preview (81 chars of
	// input -> preview holds exactly 80).
	_, err = decodeResult(t, strings.Repeat("x", 81))
	if err == nil || !strings.Contains(err.Error(), strings.Repeat("x", 80)) {
		t.Fatalf("81-char raw err = %v, want 80-char preview", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 81)) {
		t.Errorf("preview not truncated to 80: %v", err)
	}
}

func TestPin_DecodeResultBlockDispatch(t *testing.T) {
	t.Parallel()
	// Array of a non-block value: the blocks unmarshal error must surface
	// (not the trailing "no decodable block" error).
	_, err := decodeResult(t, "[42]")
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("[42] err = %v, want json unmarshal error", err)
	}

	// Image block decodes with its type and media type intact.
	out, err := decodeResult(t, `[{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"QQ=="}}]`)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := out.(*ImageOutput)
	if !ok {
		t.Fatalf("image decode type = %T, want *ImageOutput", out)
	}
	if img.Type != "image" || img.MimeType != "image/webp" {
		t.Errorf("image decode = %+v, want type image / image/webp", *img)
	}

	// Plain text block decodes with Type set.
	out, err = decodeResult(t, `[{"type":"text","text":"hello"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, ok := out.(*TextOutput)
	if !ok {
		t.Fatalf("text decode type = %T, want *TextOutput", out)
	}
	if to.Type != "text" || to.Content != "hello" {
		t.Errorf("text decode = %+v, want type text / hello", *to)
	}

	// Empty text block is skipped: no decodable block error, not an empty
	// TextOutput.
	if _, err = decodeResult(t, `[{"type":"text","text":""}]`); err == nil || !strings.Contains(err.Error(), "no decodable block") {
		t.Errorf("empty text block err = %v, want no-decodable error", err)
	}
	// A block of any other type is skipped even when it carries text.
	if _, err = decodeResult(t, `[{"type":"thinking","text":"nope"}]`); err == nil || !strings.Contains(err.Error(), "no decodable block") {
		t.Errorf("non-text block err = %v, want no-decodable error", err)
	}
}

func TestPin_DecodeResultDualFormat(t *testing.T) {
	t.Parallel()
	// Leading-space JSON is NOT the legacy dual format (the wire never
	// emitted leading whitespace); it must come back verbatim as content.
	out, err := decodeResult(t, `[{"type":"text","text":" {\"type\":\"text\",\"content\":\"z\"}"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ := out.(*TextOutput)
	if to.Content != " {\"type\":\"text\",\"content\":\"z\"}" {
		t.Errorf("leading-space JSON content = %q, want raw text", to.Content)
	}

	// JSON with a decoded "type" but trailing garbage does not enter the
	// legacy branch (unmarshal fails) and comes back verbatim.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"type\":\"file_unchanged\",bogus"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = out.(*TextOutput)
	if to.Content != "{\"type\":\"file_unchanged\",bogus" {
		t.Errorf("partial-write JSON content = %q, want raw text", to.Content)
	}

	// Valid JSON without a "type" key is not dual format either.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"content\":\"q\"}"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = out.(*TextOutput)
	if to.Content != "{\"content\":\"q\"}" {
		t.Errorf("typeless JSON content = %q, want raw text", to.Content)
	}

	// A dual-format block that matches no case must not stop the scan:
	// the following plain text block is still returned.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"type\":\"other\",\"filePath\":\"/f\"}"},{"type":"text","text":"after"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = out.(*TextOutput)
	if to.Content != "after" {
		t.Errorf("post-legacy-block content = %q, want after", to.Content)
	}

	// The legacy file_unchanged dual format still decodes.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"type\":\"file_unchanged\",\"filePath\":\"/f\"}"}]`)
	if err != nil {
		t.Fatal(err)
	}
	fu, ok := out.(*FileUnchangedOutput)
	if !ok || fu.Type != "file_unchanged" || fu.FilePath != "/f" {
		t.Fatalf("file_unchanged decode = %#v", out)
	}
}

// ---------------------------------------------------------------------------
// Execute input/permission/wrap paths
// ---------------------------------------------------------------------------

func mustPinJSON(t *testing.T, in Input) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPin_ExecuteParseInputWrap(t *testing.T) {
	t.Parallel()
	_, err := Execute(context.Background(), json.RawMessage(`{"file_path": 1}`), nil)
	if err == nil {
		t.Fatal("want error for bad input")
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("err = %v, want wrapped *json.UnmarshalTypeError (%%w)", err)
	}
	// Bad input must be a parse error, not "file_path is required": the
	// unmarshal guard fires first.
	if !strings.HasPrefix(err.Error(), "parse input:") {
		t.Errorf("err = %v, want 'parse input:' prefix", err)
	}
}

func TestPin_JoinGuardOperands(t *testing.T) {
	// No t.Parallel: t.Setenv.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	// "~" must NOT be joined onto WorkingDir (it resolves to HOME, a dir).
	_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: "~"}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil || !strings.HasPrefix(err.Error(), "path is a directory") {
		t.Fatalf("~ err = %v, want 'path is a directory'", err)
	}

	// "~/" paths resolve against HOME even with a WorkingDir set.
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: "~/f.txt"}), &tool.ToolUseContext{WorkingDir: "/nonexistent-wd"})
	if err != nil || res == nil {
		t.Fatalf("~/f.txt with wd: res=%v err=%v", res, err)
	}

	// Absolute paths ignore WorkingDir (Join would produce wd/abs).
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: filepath.Join(dir, "f.txt")}), &tool.ToolUseContext{WorkingDir: "/nonexistent-wd"})
	if err != nil || res == nil {
		t.Fatalf("abs path with wd: res=%v err=%v", res, err)
	}
}

func TestPin_JoinGuardNilTctxRelative(t *testing.T) {
	// No t.Parallel: process-wide chdir.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// A relative path with a nil tctx must resolve via cwd, not panic.
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: "f.txt"}), nil)
	if err != nil || res == nil {
		t.Fatalf("relative nil-tctx: res=%v err=%v", res, err)
	}
}

func TestPin_SqliteHandledResultReturned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pin.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO users (id, name) VALUES (1, 'ada')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: dbPath + ":users"}), nil)
	if err != nil {
		t.Fatalf("sqlite read: %v", err)
	}
	if res == nil {
		t.Fatal("sqlite handled path returned nil result")
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("sqlite Data type = %T, want TextOutput", res.Data)
	}
	if !strings.Contains(to.Content, "users") || !strings.Contains(to.Content, "ada") {
		t.Errorf("sqlite content = %q, want users table with ada row", to.Content)
	}
	if to.Type != "text" {
		t.Errorf("sqlite output Type = %q, want text", to.Type)
	}

	// A handled sqlite path with a bad member must surface the error, not
	// swallow it.
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: dbPath + ":missing_table"}), nil)
	if err == nil || !strings.Contains(err.Error(), "missing_table") {
		t.Fatalf("sqlite missing-table err = %v, want error naming missing_table", err)
	}
}

func TestPin_StatAndReadPermissionWraps(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based permission tests are void as root")
	}
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unstatable: stat error must wrap the *fs.PathError (errors.Is works).
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("stat err = %v, want errors.Is fs.ErrPermission", err)
	}
	_ = os.Chmod(dir, 0o755)

	// Unreadable file: stat succeeds, ReadFile fails; full read.
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("read err = %v, want errors.Is fs.ErrPermission", err)
	}
	// Same via the range path.
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 1, Limit: 1}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("range read err = %v, want errors.Is fs.ErrPermission", err)
	}

	// Undecodable image: decode error wraps the image package error.
	imgPath := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(imgPath, []byte("\x89PNG\r\n\x1a\ngarbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: imgPath}), nil)
	if !strings.HasPrefix(err.Error(), "decode image:") {
		t.Fatalf("bad png err = %v, want 'decode image:' prefix", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("decode image err not wrapped (%%w): %v", err)
	}

	// Unreadable image file: ReadFile error wraps fs.ErrPermission.
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	lockedImg := filepath.Join(dir, "locked.jpg")
	if err := os.WriteFile(lockedImg, jpegBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedImg, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedImg, 0o644) })
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: lockedImg}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("unreadable image err = %v, want errors.Is fs.ErrPermission", err)
	}
}

// ---------------------------------------------------------------------------
// text file reads: content, wire fields, slicing, dedup state
// ---------------------------------------------------------------------------

func TestPin_TextFileFullReadFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("l1\nl2\nl3"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", res.Data)
	}
	want := TextOutput{Type: "text", FilePath: p, Content: "l1\nl2\nl3", NumLines: 3, StartLine: 1, TotalLines: 3}
	if to != want {
		t.Errorf("full read = %+v, want %+v", to, want)
	}
}

func TestPin_TextFileRangeReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("a\nb\nc\nd"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(in Input) TextOutput {
		t.Helper()
		res, err := Execute(context.Background(), mustPinJSON(t, in), nil)
		if err != nil {
			t.Fatal(err)
		}
		to, ok := res.Data.(TextOutput)
		if !ok {
			t.Fatalf("Data type = %T, want TextOutput", res.Data)
		}
		return to
	}
	// offset=2, limit=0: rest of file from line 2.
	if got := run(Input{FilePath: p, Offset: 2}); got.Content != "b\nc\nd" || got.StartLine != 2 || got.NumLines != 3 || got.TotalLines != 4 || got.Type != "text" || got.FilePath != p {
		t.Errorf("offset=2 read = %+v", got)
	}
	// offset=1, limit=1: exactly the first line.
	if got := run(Input{FilePath: p, Offset: 1, Limit: 1}); got.Content != "a" || got.NumLines != 1 || got.StartLine != 1 {
		t.Errorf("offset=1 limit=1 read = %+v", got)
	}
	// CRLF normalization applies to range reads too.
	crlf := filepath.Join(dir, "crlf.txt")
	if err := os.WriteFile(crlf, []byte("x\r\ny"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(Input{FilePath: crlf, Offset: 2}); got.Content != "y" || got.TotalLines != 2 {
		t.Errorf("crlf offset=2 read = %+v", got)
	}
}

func TestPin_TextFileDedupState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	tctx := &tool.ToolUseContext{}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 2, Limit: 1}), tctx); err != nil {
		t.Fatal(err)
	}
	state, ok := tctx.ReadFileState[p]
	if !ok {
		t.Fatalf("ReadFileState missing key %q: %v", p, tctx.ReadFileState)
	}
	want := tool.FileState{Content: "b", Timestamp: info.ModTime().UnixMilli(), Offset: 2, Limit: 1, IsPartialView: true}
	if state != want {
		t.Errorf("state = %+v, want %+v", state, want)
	}

	// Second read with a different offset must NOT dedup.
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 3}), tctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Data.(TextOutput); !ok {
		t.Fatalf("offset-differs read = %T, want TextOutput (no dedup)", res.Data)
	}
	// Second read with a different limit must NOT dedup either.
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Limit: 2}), tctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Data.(TextOutput); !ok {
		t.Fatalf("limit-differs read = %T, want TextOutput (no dedup)", res.Data)
	}

	// Same offset with a different limit must NOT dedup (limit is part of
	// the dedup key even when the recorded view was unsliced).
	sameOff := &tool.ToolUseContext{}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 2}), sameOff); err != nil {
		t.Fatal(err)
	}
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 2, Limit: 1}), sameOff)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Data.(TextOutput); !ok {
		t.Fatalf("same-offset-diff-limit read = %T, want TextOutput (no dedup)", res.Data)
	}

	// An identical repeat of the SAME full read dedups to the stub with its
	// type intact.
	dedupCtx := &tool.ToolUseContext{}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), dedupCtx); err != nil {
		t.Fatal(err)
	}
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), dedupCtx)
	if err != nil {
		t.Fatal(err)
	}
	fu, ok := res.Data.(FileUnchangedOutput)
	if !ok {
		t.Fatalf("identical read = %T, want FileUnchangedOutput", res.Data)
	}
	if fu.Type != "file_unchanged" || fu.FilePath != p {
		t.Errorf("file_unchanged = %+v", fu)
	}
}

// pinTokenContent returns content whose EstimateTokens is exactly want, or
// fails the test (the estimator is a pure, monotonic function of the repeat
// count, so a short scan around the binary-searched crossing suffices).
func pinTokenContent(t *testing.T, want int) string {
	t.Helper()
	est := func(n int) int { return utils.EstimateTokens(strings.Repeat("a ", n)) }
	lo, hi := 1, 1<<20
	if est(hi) < want {
		t.Fatalf("no repeat count up to %d yields %d tokens", hi, want)
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if est(mid) < want {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	for n := lo - 2; n <= lo+2; n++ {
		if n > 0 && est(n) == want {
			return strings.Repeat("a ", n)
		}
	}
	t.Fatalf("no repeat count yields exactly %d tokens (crossing at %d)", want, lo)
	return ""
}

func TestPin_TokenBoundExact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "exact.txt")
	if err := os.WriteFile(p, []byte(pinTokenContent(t, MaxFileReadTokens)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Exactly MaxFileReadTokens passes the bound (> comparison).
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil); err != nil {
		t.Errorf("exact-token read err = %v, want nil", err)
	}
}

func TestPin_TokenBoundExceeded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	content := pinTokenContent(t, MaxFileReadTokens+50)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	input := mustPinJSON(t, Input{FilePath: p})

	_, err := Execute(context.Background(), input, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("nil-tctx err = %v, want token-bound error", err)
	}
	// A capped context with state must fail BEFORE recording the read —
	// a rejected over-read leaving a stale ReadFileState entry would trip
	// fileedit's must-read-first gate for a file the model never read.
	capped := &tool.ToolUseContext{ReadFileState: map[string]tool.FileState{}}
	_, err = Execute(context.Background(), input, capped)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("capped-tctx err = %v, want token-bound error", err)
	}
	if len(capped.ReadFileState) != 0 {
		t.Errorf("rejected over-read recorded ReadFileState entries: %v", capped.ReadFileState)
	}
	// Uncapped context lifts the bound.
	if _, err := Execute(context.Background(), input, &tool.ToolUseContext{UncappedOutput: true}); err != nil {
		t.Errorf("uncapped-tctx err = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// executeDocument: conversion outputs and offset/limit on markdown
// ---------------------------------------------------------------------------

func TestPin_DocumentConvertOutputs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: empty}), nil)
	want := fmt.Sprintf("conversion produced no output: %s", empty)
	if err == nil || err.Error() != want {
		t.Fatalf("empty conversion err = %v, want %q", err, want)
	}

	bad := filepath.Join(dir, "bad.pdf")
	if err := os.WriteFile(bad, []byte("%PDF-garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: bad}), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "convert .pdf:") {
		t.Fatalf("bad pdf err = %v, want 'convert .pdf:' prefix", err)
	}
	if errors.Unwrap(errors.Unwrap(err)) == nil && errors.Unwrap(err) == nil {
		t.Errorf("convert err not wrapped (%%w): %v", err)
	}
}

func TestPin_DocumentMarkdownLineAccounting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// markitdown's csv output has no trailing newline; epub garbage is
	// passed through verbatim, so a trailing newline can be planted.
	csv := filepath.Join(dir, "rows.csv")
	if err := os.WriteFile(csv, []byte("h\nv1\nv2"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: csv}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, _ := res.Data.(TextOutput)
	if to.Content != "| h |\n| --- |\n| v1 |\n| v2 |" || to.TotalLines != 4 || to.NumLines != 4 || to.Type != "text" || to.FilePath != csv {
		t.Errorf("csv doc output = %+v", to)
	}

	// A minimal real docx converts to paragraph markdown (no trailing
	// newline): "hello\n\nworld" is 3 lines.
	docx := filepath.Join(dir, "real.docx")
	if err := makeZip(docx, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>hello</w:t></w:r></w:p><w:p><w:r><w:t>world</w:t></w:r></w:p></w:body></w:document>`,
	}); err != nil {
		t.Fatal(err)
	}
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: docx}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = res.Data.(TextOutput)
	if to.Content != "hello\n\nworld" || to.TotalLines != 3 || to.NumLines != 3 {
		t.Errorf("docx doc output = %+v, want hello/world 3-line accounting", to)
	}

	// offset/limit on the markdown: line 2 only.
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: csv, Offset: 2, Limit: 1}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = res.Data.(TextOutput)
	if to.Content != "| --- |" || to.NumLines != 1 || to.StartLine != 2 || to.TotalLines != 4 {
		t.Errorf("csv doc offset=2 limit=1 = %+v", to)
	}
}

// Over-token converted documents have no in-converter token check; the
// boundTextOutput layer is the only guard for the document path.
func TestPin_TokenBoundDocument(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	big := filepath.Join(dir, "big.csv")
	if err := os.WriteFile(big, bytes.Repeat([]byte("a,b\n"), 40000), 0o644); err != nil {
		t.Fatal(err)
	}
	input := mustPinJSON(t, Input{FilePath: big})

	_, err := Execute(context.Background(), input, &tool.ToolUseContext{})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("capped doc err = %v, want token-bound error", err)
	}
	// The bound must consult tctx without panicking on a nil context.
	_, err = Execute(context.Background(), input, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("nil-tctx doc err = %v, want token-bound error", err)
	}
	if _, err := Execute(context.Background(), input, &tool.ToolUseContext{UncappedOutput: true}); err != nil {
		t.Errorf("uncapped doc err = %v, want nil", err)
	}

	// A document whose markdown estimates exactly MaxFileReadTokens passes
	// the bound (strict > comparison). Row count is binary-searched through
	// the converter itself so the expectation tracks the real markdown.
	m := markitdown.New()
	exact := filepath.Join(dir, "exact.csv")
	mdTokens := func(rows int) int {
		content := "h\n" + strings.Repeat("r\n", rows)
		p := filepath.Join(dir, "probe.csv")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := m.ConvertFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return utils.EstimateTokens(res.Markdown)
	}
	lo, hi := 1, 40000
	for lo < hi {
		mid := (lo + hi) / 2
		if mdTokens(mid) < MaxFileReadTokens {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	found := false
	for n := lo - 2; n <= lo+2 && !found; n++ {
		if n > 0 && mdTokens(n) == MaxFileReadTokens {
			if err := os.WriteFile(exact, []byte("h\n"+strings.Repeat("r\n", n)), 0o644); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no row count yields exactly %d markdown tokens (crossing at %d)", MaxFileReadTokens, lo)
	}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: exact}), &tool.ToolUseContext{}); err != nil {
		t.Errorf("exact-token doc err = %v, want nil", err)
	}
	_ = os.Remove(filepath.Join(dir, "probe.csv"))
}

// ---------------------------------------------------------------------------
// wire blocks (FormatWireBlocks pass-through already covered elsewhere); this
// pins the empty-file and offset-beyond-end warnings used by the text path.
// ---------------------------------------------------------------------------

func TestPin_WireTextWarnings(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	blocks := wb.FormatWireBlocks(TextOutput{Type: "text", TotalLines: 0})
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "contents are empty") {
		t.Errorf("empty warning blocks = %+v", blocks)
	}
	blocks = wb.FormatWireBlocks(TextOutput{Type: "text", StartLine: 9, TotalLines: 4})
	if len(blocks) != 1 || blocks[0].Text != "<system-reminder>Warning: the file exists but is shorter than the provided offset (9). The file has 4 lines.</system-reminder>" {
		t.Errorf("offset warning blocks = %+v", blocks)
	}
	// Line-numbered text: numbering starts at StartLine.
	blocks = wb.FormatWireBlocks(TextOutput{Type: "text", Content: "x\ny", StartLine: 7})
	if len(blocks) != 1 || blocks[0].Text != "7\tx\n8\ty" {
		t.Errorf("numbered blocks = %+v", blocks)
	}
	if b := blocks[0]; b.Type != "text" {
		t.Errorf("text block type = %q", b.Type)
	}
}
