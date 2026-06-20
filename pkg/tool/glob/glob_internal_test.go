package glob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// writeFakeRg creates a fake "rg" executable in dir that runs the given bash script.
func writeFakeRg(t *testing.T, dir, script string) {
	t.Helper()
	rgPath := filepath.Join(dir, "rg")
	content := "#!/bin/bash\n" + script
	if err := os.WriteFile(rgPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake rg: %v", err)
	}
}

// prependPath prepends dir to PATH so fake rg is found first.
func prependPath(t *testing.T, dir string) {
	t.Helper()
	orig := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+orig)
}

// ---------------------------------------------------------------------------
// Execute — basic paths
// ---------------------------------------------------------------------------

func TestExecute_GetwdFallback(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"pattern":"*.go"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Logf("Execute() returned error (expected in some cwd): %v", err)
		return
	}

	output := result.Data.(*Output)
	if output == nil {
		t.Fatal("Output is nil")
	}
	if output.Count < 0 {
		t.Errorf("Count = %d, want >= 0", output.Count)
	}
	if output.DurationMs < 0 {
		t.Errorf("DurationMs = %d, want >= 0", output.DurationMs)
	}
}

func TestExecute_InvalidGlobPattern(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tctx := &tool.ToolUseContext{WorkingDir: dir}

	input := json.RawMessage(`{"pattern":"[invalid"}`)
	_, err := Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for invalid glob pattern")
	}
	if !strings.Contains(err.Error(), "ripgrep") {
		t.Errorf("error = %q, want error containing 'ripgrep'", err.Error())
	}
}

func TestExecute_CancelledContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tctx := &tool.ToolUseContext{WorkingDir: dir}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	input := json.RawMessage(`{"pattern":"**/*"}`)
	_, err := Execute(ctx, input, tctx)
	if err == nil {
		t.Fatal("Execute() with cancelled context should return error")
	}
}

// ---------------------------------------------------------------------------
// isEagainError — unit tests
// ---------------------------------------------------------------------------

func TestIsEagainError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"os error 11", &rgError{stderr: "rg: os error 11"}, true},
		{"Resource temporarily unavailable", &rgError{stderr: "rg: Resource temporarily unavailable"}, true},
		{"other error", &rgError{stderr: "permission denied"}, false},
		{"not rgError", context.Canceled, false},
		{"wrapped rgError", &rgError{stderr: "failed: os error 11 on thread"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isEagainError(tc.err)
			if got != tc.want {
				t.Errorf("isEagainError() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// rgError — Unwrap coverage
// ---------------------------------------------------------------------------

func TestRgError_Unwrap(t *testing.T) {
	t.Parallel()

	inner := fmt.Errorf("inner error")
	rge := &rgError{stderr: "some stderr", wrapped: inner}

	if !errors.Is(rge, inner) {
		t.Error("errors.Is should match inner error via Unwrap")
	}

	var target *rgError
	if !errors.As(rge, &target) {
		t.Error("errors.As should match *rgError")
	}
	if target.stderr != "some stderr" {
		t.Errorf("rgError.stderr = %q, want %q", target.stderr, "some stderr")
	}
}

// ---------------------------------------------------------------------------
// Fake rg — EAGAIN retry
// ---------------------------------------------------------------------------

func TestRgGlob_EagainRetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "eagain_marker")

	// First call: EAGAIN error; second call: success
	script := fmt.Sprintf(`if [ -f %s ]; then
    rm %s
    echo "file1.go"
    echo "file2.go"
    exit 0
else
    touch %s
    echo "os error 11" >&2
    exit 2
fi`, marker, marker, marker)

	writeFakeRg(t, dir, script)
	prependPath(t, dir)

	lines, err := rgGlob(context.Background(), dir, "*.go")
	if err != nil {
		t.Fatalf("rgGlob() error: %v", err)
	}
	if len(lines) != 2 {
		t.Errorf("got %d lines, want 2: %v", len(lines), lines)
	}
}

func TestRgGlob_EagainRetryFails(t *testing.T) {
	dir := t.TempDir()

	// Always return EAGAIN — both first call and retry fail
	writeFakeRg(t, dir, `echo "os error 11" >&2
exit 2`)
	prependPath(t, dir)

	_, err := rgGlob(context.Background(), dir, "*.go")
	if err == nil {
		t.Fatal("expected error when EAGAIN retry also fails")
	}
	if !strings.Contains(err.Error(), "single-threaded retry") {
		t.Errorf("error = %q, want 'single-threaded retry'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Fake rg — timeout with partial results
// ---------------------------------------------------------------------------

func TestRgRaw_TimeoutPartialResults(t *testing.T) {
	dir := t.TempDir()

	// Output 3 lines, then burn CPU in bash-only loop (no subprocess = clean SIGKILL)
	writeFakeRg(t, dir, `echo "file1.go"
echo "file2.go"
echo "file3.go"
x=0; while [ $x -lt 100000000 ]; do x=$((x+1)); done`)
	prependPath(t, dir)

	// Short context timeout → kills rg before sleep finishes
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	lines, err := rgRaw(ctx, []string{"--files", dir})
	if err != nil {
		t.Fatalf("rgRaw() error: %v", err)
	}
	// Should get partial results (last line dropped as potentially incomplete)
	if len(lines) < 1 {
		t.Errorf("got %d lines, want >= 1 partial result", len(lines))
	}
	if len(lines) > 3 {
		t.Errorf("got %d lines, want <= 3", len(lines))
	}
}

// ---------------------------------------------------------------------------
// Fake rg — timeout with no results
// ---------------------------------------------------------------------------

func TestRgRaw_TimeoutNoResults(t *testing.T) {
	dir := t.TempDir()

	// Block without output (bash-only loop, no subprocess = clean SIGKILL)
	writeFakeRg(t, dir, `x=0; while [ $x -lt 100000000 ]; do x=$((x+1)); done`)
	prependPath(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := rgRaw(ctx, []string{"--files", dir})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want 'timed out'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// parseOutput
// ---------------------------------------------------------------------------

func TestParseOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"whitespace only", "  \n  ", []string{}},
		{"single line", "file.go\n", []string{"file.go"}},
		{"multiple lines", "a.go\nb.go\nc.go\n", []string{"a.go", "b.go", "c.go"}},
		{"carriage return", "a.go\r\nb.go\r\n", []string{"a.go", "b.go"}},
		{"trailing empty", "a.go\n\n", []string{"a.go"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseOutput(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseOutput() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseOutput()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
