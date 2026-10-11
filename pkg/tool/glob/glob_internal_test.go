package glob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	bashPath := func() string {
		if p, err := exec.LookPath("bash"); err == nil {
			return p
		}
		return "/bin/bash"
	}()
	content := "#!" + bashPath + "\n" + script
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

// The TS-parity constants are the tool's contract: the result cap decides
// when the narrowing hint appears, the buffer and timeout bound the rg run.
func TestParityConstants(t *testing.T) {
	t.Parallel()
	if MaxGlobResults != 100 {
		t.Errorf("MaxGlobResults = %d, want 100", MaxGlobResults)
	}
	if maxBufferSize != 20_000_000 {
		t.Errorf("maxBufferSize = %d, want 20000000", maxBufferSize)
	}
	if defaultGlobTimeout != 20*time.Second {
		t.Errorf("defaultGlobTimeout = %v, want 20s", defaultGlobTimeout)
	}
}

// Exactly MaxGlobResults files is not truncated; one more is.
func TestExecute_TruncationBoundary(t *testing.T) {
	cases := []struct {
		files     int
		wantCount int
		wantTrunc bool
	}{
		{100, 100, false},
		{101, 100, true},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		for i := range tc.files {
			fp := filepath.Join(dir, fmt.Sprintf("file%03d.go", i))
			if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
		}
		input := json.RawMessage(`{"pattern":"*.go","path":"` + dir + `"}`)
		result, err := Execute(context.Background(), input, nil)
		if err != nil {
			t.Fatalf("Execute(%d files) error: %v", tc.files, err)
		}
		out := result.Data.(*Output)
		if out.Count != tc.wantCount || len(out.Files) != tc.wantCount {
			t.Errorf("%d files: Count=%d len(Files)=%d, want %d", tc.files, out.Count, len(out.Files), tc.wantCount)
		}
		if out.Truncated != tc.wantTrunc {
			t.Errorf("%d files: Truncated = %v, want %v", tc.files, out.Truncated, tc.wantTrunc)
		}
	}
}

// A nil ToolUseContext and an empty WorkingDir both fall back to the process
// working directory, which holds this package's own .go files.
func TestExecute_WorkingDirFallbacks(t *testing.T) {
	input := json.RawMessage(`{"pattern":"glob*.go"}`)

	for _, tctx := range []*tool.ToolUseContext{nil, {}} {
		result, err := Execute(context.Background(), input, tctx)
		if err != nil {
			t.Fatalf("Execute with tctx=%#v: %v", tctx, err)
		}
		out := result.Data.(*Output)
		if out.Count < 1 {
			t.Errorf("tctx=%#v: Count = %d, want >= 1 (cwd fallback found no files)", tctx, out.Count)
		}
	}
}

func TestExecute_ParseInputError(t *testing.T) {
	_, err := Execute(context.Background(), json.RawMessage(`{"pattern":`), nil)
	if err == nil {
		t.Fatal("Execute with truncated JSON: got nil error")
	}
	if !strings.HasPrefix(err.Error(), "parse input: ") {
		t.Errorf("error = %q, want the \"parse input: \" prefix", err.Error())
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error = %v, want it to wrap *json.SyntaxError", err)
	}
}

// ---------------------------------------------------------------------------
// rgRaw failure shapes (fake rg)
// ---------------------------------------------------------------------------

// A non-zero exit that is not a timeout must surface as an rgError wrapping
// the exit error — never as partial results.
func TestRgRaw_NonTimeoutExitTwo(t *testing.T) {
	dir := t.TempDir()
	writeFakeRg(t, dir, `echo "file1.go"
echo "file2.go"
echo "file3.go"
echo "boom" >&2
exit 2`)
	prependPath(t, dir)

	_, err := rgRaw(context.Background(), []string{"--files", dir})
	if err == nil {
		t.Fatal("rgRaw with exit 2: got nil error, want an rgError")
	}
	if !strings.HasPrefix(err.Error(), "ripgrep error: ") {
		t.Errorf("error = %q, want the \"ripgrep error: \" prefix", err.Error())
	}
	var rge *rgError
	if !errors.As(err, &rge) {
		t.Fatalf("error = %T, want *rgError", err)
	}
	if rge.stderr != "boom\n" {
		t.Errorf("rgError.stderr = %q, want %q", rge.stderr, "boom\n")
	}
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("error = %v, want it to wrap *exec.ExitError", err)
	}
}

// On timeout the last line is dropped (it may be cut mid-write) and the
// partial batch is reported.
func TestRgRaw_TimeoutDropsLastLine(t *testing.T) {
	dir := t.TempDir()
	writeFakeRg(t, dir, `echo "file1.go"
echo "file2.go"
echo "file3.go"
x=0; while [ $x -lt 100000000 ]; do x=$((x+1)); done`)
	prependPath(t, dir)

	var recs []capturedRecord
	prev := slog.Default()
	slog.SetDefault(slog.New(&captureHandler{recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	lines, err := rgRaw(ctx, []string{"--files", dir})
	if err != nil {
		t.Fatalf("rgRaw() error: %v", err)
	}
	want := []string{"file1.go", "file2.go"}
	if len(lines) != len(want) || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(recs), recs)
	}
	if recs[0].msg != "glob:rg_timeout_partial" || recs[0].level != slog.LevelWarn {
		t.Errorf("record = (%v, %q), want (%v, %q)", recs[0].level, recs[0].msg, slog.LevelWarn, "glob:rg_timeout_partial")
	}
	if got, wantN := recs[0].attrs["results"], int64(2); got != wantN {
		t.Errorf("results attr = %#v, want %d", got, wantN)
	}
}

// A single partial line becomes zero lines after the drop, so the timeout
// must still be reported as an error rather than an empty success.
func TestRgRaw_TimeoutSingleLine(t *testing.T) {
	dir := t.TempDir()
	writeFakeRg(t, dir, `echo "file1.go"
x=0; while [ $x -lt 100000000 ]; do x=$((x+1)); done`)
	prependPath(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := rgRaw(ctx, []string{"--files", dir})
	if err == nil {
		t.Fatal("rgRaw with one partial line: got nil error, want the timeout error")
	}
	if err.Error() != "ripgrep timed out after 20s" {
		t.Errorf("error = %q, want %q", err.Error(), "ripgrep timed out after 20s")
	}
}

// The EAGAIN retry is logged with the search path, and the retry's own
// failure keeps the "single-threaded retry" wrapper.
func TestRgGlob_EagainRetryLogs(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "eagain_marker")
	writeFakeRg(t, dir, fmt.Sprintf(`if [ -f %s ]; then
    rm %s
    echo "file1.go"
    exit 0
else
    touch %s
    echo "os error 11" >&2
    exit 2
fi`, marker, marker, marker))
	prependPath(t, dir)

	var recs []capturedRecord
	prev := slog.Default()
	slog.SetDefault(slog.New(&captureHandler{recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	lines, err := rgGlob(context.Background(), dir, "*.go")
	if err != nil {
		t.Fatalf("rgGlob() error: %v", err)
	}
	if len(lines) != 1 || lines[0] != "file1.go" {
		t.Fatalf("lines = %v, want [file1.go]", lines)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 log record, got %d: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.level != slog.LevelInfo || rec.msg != "glob:rg_eagain_retry" {
		t.Errorf("record = (%v, %q), want (%v, %q)", rec.level, rec.msg, slog.LevelInfo, "glob:rg_eagain_retry")
	}
	if rec.attrs["path"] != dir {
		t.Errorf("path attr = %#v, want %q", rec.attrs["path"], dir)
	}
}

// A missing ripgrep binary fails at Start: the error must be an rgError
// carrying the wrapped start failure.
func TestRgRaw_StartFailure(t *testing.T) {
	t.Setenv("GBOT_RG_PATH", filepath.Join(t.TempDir(), "definitely-not-rg"))

	_, err := rgRaw(context.Background(), []string{"--files", t.TempDir()})
	if err == nil {
		t.Fatal("rgRaw with a missing binary: got nil error")
	}
	if !strings.HasPrefix(err.Error(), "ripgrep: ") {
		t.Errorf("error = %q, want the \"ripgrep: \" prefix", err.Error())
	}
	if _, ok := errors.AsType[*rgError](err); !ok {
		t.Fatalf("error = %T, want *rgError", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want it to wrap os.ErrNotExist", err)
	}
}

// ---------------------------------------------------------------------------
// slog capture helper
// ---------------------------------------------------------------------------

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type captureHandler struct{ recs *[]capturedRecord }

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	*c.recs = append(*c.recs, capturedRecord{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (c *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(string) slog.Handler      { return c }

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
	if !strings.HasPrefix(err.Error(), "ripgrep error (single-threaded retry): ") {
		t.Errorf("error = %q, want the single-threaded-retry wrapper", err.Error())
	}
	// The retry's own cause must stay unwrappable through both wrappers.
	if _, ok := errors.AsType[*rgError](err); !ok {
		t.Errorf("error = %v, want it to wrap *rgError", err)
	}
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("error = %v, want it to wrap *exec.ExitError", err)
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
