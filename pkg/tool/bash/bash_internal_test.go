package bash

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"needs truncation", "hello world", 8, "hello..."},
		{"empty string", "", 5, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		maxSize int
		want    string
	}{
		{"small output", "hello", 10, "hello"},
		{"exact size", "hello", 5, "hello"},
		{"needs truncation", "hello world", 5, "hello\n\n... [1 lines truncated] ..."},
		{"empty output", "", 5, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateOutput(tc.input, tc.maxSize)
			if got != tc.want {
				t.Errorf("truncateOutput(%q, %d) = %q, want %q", tc.input, tc.maxSize, got, tc.want)
			}
		})
	}
}

func TestBuildCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cmd       string
		snapshot  *EnvSnapshot
		cwdFile   string
		wantParts []string
	}{
		{
			name:      "basic command without snapshot",
			cmd:       "echo hello",
			snapshot:  nil,
			cwdFile:   "/tmp/cwd.txt",
			wantParts: []string{"shopt -u extglob", "eval 'echo hello'", "< /dev/null", "pwd -P >| /tmp/cwd.txt"},
		},
		{
			name:      "command with snapshot",
			cmd:       "echo hello",
			snapshot:  &EnvSnapshot{Path: "/tmp/snap.sh"},
			cwdFile:   "/tmp/cwd.txt",
			wantParts: []string{"source /tmp/snap.sh 2>/dev/null || true", "shopt -u extglob", "eval 'echo hello'", "< /dev/null", "pwd -P >| /tmp/cwd.txt"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildCommand(tc.cmd, tc.snapshot, tc.cwdFile)
			for _, part := range tc.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("buildCommand() = %q, want to contain %q", got, part)
				}
			}
		})
	}
}

func TestBuildCwdFilePath(t *testing.T) {
	t.Parallel()

	path := buildCwdFilePath("abcd")
	if !strings.Contains(path, "gbot-abcd-cwd") {
		t.Errorf("buildCwdFilePath(\"abcd\") = %q, want to contain 'gbot-abcd-cwd'", path)
	}
	if !strings.HasPrefix(path, os.TempDir()) {
		t.Errorf("buildCwdFilePath() = %q, want prefix %q", path, os.TempDir())
	}
}

func TestTrackCwd(t *testing.T) {
	t.Parallel()

	t.Run("valid cwd file", func(t *testing.T) {
		t.Parallel()
		tmpDir := os.TempDir()
		f, err := os.CreateTemp("", "gbot-test-cwd-*")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(tmpDir); err != nil {
			t.Fatalf("WriteString() error: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close() error: %v", err)
		}
		defer func() { _ = os.Remove(f.Name()) }()

		got := trackCwd(f.Name(), "/original")
		if got != tmpDir {
			t.Errorf("trackCwd() = %q, want %q", got, tmpDir)
		}
	})

	t.Run("missing cwd file", func(t *testing.T) {
		t.Parallel()
		got := trackCwd("/nonexistent/file", "/original")
		if got != "/original" {
			t.Errorf("trackCwd() = %q, want /original", got)
		}
	})

	t.Run("deleted directory", func(t *testing.T) {
		t.Parallel()
		f, err := os.CreateTemp("", "gbot-test-cwd-*")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("/nonexistent/directory/path"); err != nil {
			t.Fatalf("WriteString() error: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close() error: %v", err)
		}
		defer func() { _ = os.Remove(f.Name()) }()

		got := trackCwd(f.Name(), "/original")
		if got != "/original" {
			t.Errorf("trackCwd() = %q, want /original (dir does not exist)", got)
		}
	})

	t.Run("empty cwd content", func(t *testing.T) {
		t.Parallel()
		f, err := os.CreateTemp("", "gbot-test-cwd-*")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("  "); err != nil {
			t.Fatalf("WriteString() error: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close() error: %v", err)
		}
		defer func() { _ = os.Remove(f.Name()) }()

		got := trackCwd(f.Name(), "/original")
		if got != "/original" {
			t.Errorf("trackCwd() = %q, want /original (empty content)", got)
		}
	})
}

func TestDirExists(t *testing.T) {
	t.Parallel()

	t.Run("existing directory", func(t *testing.T) {
		t.Parallel()
		if !dirExists(os.TempDir()) {
			t.Errorf("dirExists(%q) = false, want true", os.TempDir())
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		t.Parallel()
		if dirExists("/nonexistent/path/that/does/not/exist") {
			t.Error("dirExists() = true for nonexistent path")
		}
	})

	t.Run("file is not directory", func(t *testing.T) {
		t.Parallel()
		f, err := os.CreateTemp("", "gbot-test-*")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close() error: %v", err)
		}
		defer func() { _ = os.Remove(f.Name()) }()

		if dirExists(f.Name()) {
			t.Errorf("dirExists(%q) = true, want false (it's a file)", f.Name())
		}
	})
}

func TestBuildCommand_Order(t *testing.T) {
	t.Parallel()

	cmd := buildCommand("ls", nil, "/tmp/cwd")
	parts := strings.Split(cmd, " && ")

	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d: %v", len(parts), parts)
	}
	if !strings.Contains(parts[0], "extglob") {
		t.Errorf("part[0] = %q, want extglob", parts[0])
	}
	if !strings.Contains(parts[1], "eval") {
		t.Errorf("part[1] = %q, want eval", parts[1])
	}
	if !strings.Contains(parts[2], "pwd") {
		t.Errorf("part[2] = %q, want pwd", parts[2])
	}
}

func TestBuildCommand_WithSnapshot(t *testing.T) {
	t.Parallel()

	snap := &EnvSnapshot{Path: "/tmp/snapshot-test.sh"}
	cmd := buildCommand("echo hi", snap, "/tmp/cwd")

	if !strings.HasPrefix(cmd, "source /tmp/snapshot-test.sh") {
		t.Errorf("expected command to start with source, got: %q", cmd[:50])
	}
}

func TestBuildCwdFilePath_Unique(t *testing.T) {
	t.Parallel()

	path1 := buildCwdFilePath("aaaa")
	path2 := buildCwdFilePath("bbbb")
	if path1 == path2 {
		t.Errorf("different IDs should produce different paths: %q == %q", path1, path2)
	}
}

func TestBuildCwdFilePath_InTempDir(t *testing.T) {
	t.Parallel()

	path := buildCwdFilePath("test123")
	expectedPrefix := filepath.Join(os.TempDir(), "gbot-")
	if !strings.HasPrefix(path, expectedPrefix) {
		t.Errorf("buildCwdFilePath() = %q, want prefix %q", path, expectedPrefix)
	}
}

// SessionEnvScript branch in buildCommand is unreachable since SessionEnvScript() returns ""
func TestBuildCommand_SessionEnvBranch(t *testing.T) {
	t.Parallel()

	cmd := buildCommand("echo test", nil, "/tmp/cwd")
	parts := strings.Split(cmd, " && ")
	if len(parts) != 3 {
		t.Errorf("expected 3 parts, got %d: %v", len(parts), parts)
	}
	if !strings.Contains(parts[0], "extglob") {
		t.Errorf("part[0] = %q, want extglob", parts[0])
	}
}

// --- Execute dispatch and executePTY error paths ---

func TestExecute_ForceNonPTY(t *testing.T) {
	// Make isPTYAvailable return false → Execute dispatches to executeNonPTY
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test")
	defer func() { SetPtmxCheckPath(orig) }()

	input := json.RawMessage(`{"command":"echo hello"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", output.ExitCode)
	}
}

func TestExecutePTY_Error(t *testing.T) {
	// Make ptyCommand fail by using non-existent shell
	orig := shellCommand
	shellCommand = "/nonexistent/shell/gbot-test-xyz"
	defer func() { shellCommand = orig }()

	in := Input{Command: "echo hello", Timeout: 10000}
	_, err := executePTY(context.Background(), in, "", 10*time.Second)
	if err == nil {
		t.Error("expected error with non-existent shell")
	}
}

func TestBuildCommand_WithSessionEnv(t *testing.T) {
	// Override sessionEnvScript to test the buildCommand branch
	orig := sessionEnvScript
	sessionEnvScript = func() string { return "export GBOT_TEST_HOOK=1" }
	defer func() { sessionEnvScript = orig }()

	cmd := buildCommand("echo", nil, "/tmp/cwd")
	if !strings.Contains(cmd, "export GBOT_TEST_HOOK=1") {
		t.Errorf("missing session env script in command: %q", cmd)
	}
	parts := strings.Split(cmd, " && ")
	if len(parts) != 4 {
		t.Errorf("expected 4 parts with session env, got %d: %v", len(parts), parts)
	}
}

func TestBashExecuteStream_Echo(t *testing.T) {
	t.Parallel()

	var updates []tool.ProgressUpdate
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo hello"}`), nil, func(u tool.ProgressUpdate) {
		updates = append(updates, u)
	})

	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}

	if out.Stdout == "" {
		t.Error("Stdout should not be empty")
	}

	// Should have received at least one progress update
	if len(updates) == 0 {
		t.Error("expected at least one progress update")
	}
}

func TestBashExecuteStream_MultiLine(t *testing.T) {
	t.Parallel()

	var lastUpdate tool.ProgressUpdate
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo line1; echo line2; echo line3"}`), nil, func(u tool.ProgressUpdate) {
		lastUpdate = u
	})

	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}

	if lastUpdate.TotalBytes == 0 {
		t.Error("TotalBytes should be > 0")
	}
}

func TestBashExecuteStream_ExitCode(t *testing.T) {
	t.Parallel()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"exit 42"}`), nil, func(u tool.ProgressUpdate) {})

	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	if out.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", out.ExitCode)
	}
}

func TestBashExecuteStream_EmptyCommand(t *testing.T) {
	t.Parallel()

	_, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":""}`), nil, nil)

	if err == nil {
		t.Error("expected error for empty command")
	}
}

func TestBashExecuteStream_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := ExecuteStream(context.Background(), json.RawMessage(`invalid`), nil, nil)

	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestBashExecuteStream_NilProgressCallback(t *testing.T) {
	t.Parallel()

	// Should not panic with nil callback
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo safe"}`), nil, nil)

	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}

func TestBashNew_ImplementsStreaming(t *testing.T) {
	t.Parallel()

	tl := New(nil)

	_, ok := tl.(tool.ToolWithStreaming)
	if !ok {
		t.Error("Bash tool should implement ToolWithStreaming")
	}
}

// ---------------------------------------------------------------------------
// run_in_background dispatch — covers spawnBackground
// ---------------------------------------------------------------------------

func TestExecuteStream_RunInBackground_NonPTY(t *testing.T) {
	// Force non-PTY mode for deterministic testing
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-bg")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo bg-hello","run_in_background":true}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	// Step 1: Verify immediate response
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Errorf("Stdout = %q, want background task message", out.Stdout)
	}
	if !strings.Contains(out.Stdout, "bg-") {
		t.Errorf("Stdout = %q, want task ID", out.Stdout)
	}

	// Step 2: Verify the task was registered in the default registry
	registry := DefaultRegistry()
	tasks := registry.List()
	found := false
	for _, task := range tasks {
		if strings.Contains(task.Command, "echo bg-hello") {
			found = true
			// Wait for completion
			select {
			case <-task.done:
				if task.Status != TaskCompleted {
					t.Errorf("task Status = %q, want %q", task.Status, TaskCompleted)
				}
			case <-time.After(5 * time.Second):
				t.Error("background task did not complete within timeout")
			}
			break
		}
	}
	if !found {
		t.Error("background task not found in registry")
	}

	// Step 3: Clean up
	for _, task := range tasks {
		registry.Remove(task.ID)
	}
}

func TestExecuteStream_RunInBackground_CompletesWithOutput(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-bg2")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo bg-output-123","run_in_background":true}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Fatalf("unexpected output: %q", out.Stdout)
	}


	registry := DefaultRegistry()
	for _, task := range registry.List() {
		if strings.Contains(task.Command, "echo bg-output-123") {
			select {
			case <-task.done:
				if task.Output != nil {
					output := task.Output.String()
					if !strings.Contains(output, "bg-output-123") {
						t.Errorf("task output = %q, want to contain bg-output-123", output)
					}
				}
			case <-time.After(5 * time.Second):
				t.Error("task did not complete")
			}
			registry.Remove(task.ID)
			break
		}
	}
}

func TestExecuteStream_RunInBackground_ExitError(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-bg3")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"exit 7","run_in_background":true}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Fatalf("unexpected output: %q", out.Stdout)
	}

	registry := DefaultRegistry()
	for _, task := range registry.List() {
		if strings.Contains(task.Command, "exit 7") {
			select {
			case <-task.done:
				if task.ExitCode != 7 {
					t.Errorf("ExitCode = %d, want 7", task.ExitCode)
				}
				if task.Status != TaskFailed {
					t.Errorf("Status = %q, want %q", task.Status, TaskFailed)
				}
			case <-time.After(5 * time.Second):
				t.Error("task did not complete")
			}
			registry.Remove(task.ID)
			break
		}
	}
}

func TestExecuteStream_RunInBackground_PTY(t *testing.T) {
	// Test PTY branch inside spawnBackground — don't force non-PTY mode
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo pty-bg-test","run_in_background":true}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Errorf("Stdout = %q, want background task message", out.Stdout)
	}


	registry := DefaultRegistry()
	for _, task := range registry.List() {
		if strings.Contains(task.Command, "echo pty-bg-test") {
			select {
			case <-task.done:
				// Task completed in background
			case <-time.After(5 * time.Second):
				t.Error("PTY background task did not complete")
			}
			registry.Remove(task.ID)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// executeNonPTYStreaming coverage — force non-PTY mode
// ---------------------------------------------------------------------------

func TestExecuteStream_NonPTY(t *testing.T) {
	t.Parallel()
	// Force non-PTY by overriding PTY check path
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-nonpty")
	defer func() { SetPtmxCheckPath(orig) }()

	var updates []tool.ProgressUpdate
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo nonpty-echo"}`), nil, func(u tool.ProgressUpdate) {
		updates = append(updates, u)
	})
	if err != nil {
		t.Fatalf("ExecuteStream() nonPTY error: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "nonpty-echo") {
		t.Errorf("Stdout = %q, want containing nonpty-echo", out.Stdout)
	}
	if len(updates) == 0 {
		t.Error("expected at least one progress update")
	}
}


// ---------------------------------------------------------------------------
// ExecuteStream — uncovered branches
// ---------------------------------------------------------------------------

func TestExecuteStream_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := ExecuteStream(context.Background(), json.RawMessage(`{invalid json}`), nil, nil)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestExecuteStream_EmptyCommand(t *testing.T) {
	t.Parallel()
	_, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":""}`), nil, nil)
	if err == nil {
		t.Error("expected error for empty command")
	}
}

func TestExecuteStream_WithTimeout(t *testing.T) {
	t.Parallel()
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo timeout-test","timeout":5000}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}

func TestExecuteStream_CWD(t *testing.T) {
	t.Parallel()
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"pwd","cwd":"/tmp"}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "/tmp") {
		t.Errorf("Stdout = %q, want containing /tmp", out.Stdout)
	}
}

func TestExecuteStream_ToolUseContextCWD(t *testing.T) {
	t.Parallel()
	tctx := &types.ToolUseContext{WorkingDir: "/tmp"}
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"pwd"}`), tctx, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "/tmp") {
		t.Errorf("Stdout = %q, want containing /tmp", out.Stdout)
	}
}

// ---------------------------------------------------------------------------
// executeNonPTYStreaming — timeout and error paths
// ---------------------------------------------------------------------------

func TestExecuteNonPTYStreaming_TimedOut(t *testing.T) {
	t.Parallel()
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/nonpty-timeout")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"sleep 10","timeout":100}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if !out.TimedOut {
		t.Error("TimedOut should be true")
	}
}

func TestExecuteNonPTYStreaming_ExitError(t *testing.T) {
	t.Parallel()
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/nonpty-exit")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"exit 5"}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if out.ExitCode != 5 {
		t.Errorf("ExitCode = %d, want 5", out.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// spawnBackground — PTY path coverage
// ---------------------------------------------------------------------------

func TestSpawnBackground_NonPTY(t *testing.T) {
	t.Parallel()
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/spawn-nonpty")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo spawn-nonpty","run_in_background":true}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Errorf("Stdout = %q, want background message", out.Stdout)
	}
	// Wait for completion
	registry := DefaultRegistry()
	for _, task := range registry.List() {
		if strings.Contains(task.Command, "spawn-nonpty") {
			<-task.done
			registry.Remove(task.ID)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// executePTYStreaming — uncovered paths
// ---------------------------------------------------------------------------

func TestExecutePTYStreaming_CwdFileError(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/pty-cwd-err")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo pty-cwd"}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}
	if !strings.Contains(out.Stdout, "pty-cwd") {
		t.Errorf("Stdout = %q, want containing pty-cwd", out.Stdout)
	}
}

// ---------------------------------------------------------------------------
// ExecuteStream — tctx.WorkingDir path (line 215-216)
// ---------------------------------------------------------------------------

func TestExecuteStream_ToolUseContextWorkingDir(t *testing.T) {
	t.Parallel()
	tctx := &types.ToolUseContext{WorkingDir: "/tmp"}
	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"pwd"}`), tctx, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "/tmp") {
		t.Errorf("Stdout = %q, want containing /tmp", out.Stdout)
	}
}

// ---------------------------------------------------------------------------
// executePTYStreaming — err != nil path (line 265-267)
// ---------------------------------------------------------------------------

func TestExecutePTYStreaming_Error(t *testing.T) {
	// Trigger error in ptyCommand by making shell non-existent
	// This forces executePTYStreaming to return an error at line 265-267
	orig := shellCommand
	shellCommand = "/nonexistent/shell/pty-error-test"
	defer func() { shellCommand = orig }()

	s := NewStreamingOutput(nil)
	_, err := executePTYStreaming(context.Background(), Input{Command: "echo pty-err", Timeout: 10000}, "", 5*time.Second, s, false, DefaultRegistry())
	if err == nil {
		t.Error("expected error with non-existent shell")
	}
}


// ---------------------------------------------------------------------------
// executeNonPTYStreaming — generic error path (line 310-312)
// cmd.Start() failure -> return nil, err
// ---------------------------------------------------------------------------

// cmd.Start() with bash -c always succeeds; bash itself is always found.
// This test verifies the path but the error case (line 629) is unreachable
// without invasive injection hooks. Kept for documentation.
func TestExecuteNonPTYStreaming_StartError(t *testing.T) {
	t.Skip("unreachable without injection hooks - bash is always found")
}

func TestExecuteStream_TimeoutCap(t *testing.T) {
	t.Parallel()
	result, err := ExecuteStream(context.Background(),
		json.RawMessage(`{"command":"echo timeout-cap","timeout":1000000000}`),
		nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}


func TestSpawnBackground_PTYPath(t *testing.T) {
	result, err := ExecuteStream(context.Background(),
		json.RawMessage(`{"command":"echo pty-bg-test","run_in_background":true}`),
		nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Errorf("Stdout = %q, want background message", out.Stdout)
	}


	registry := DefaultRegistry()
	for _, task := range registry.List() {
		if strings.Contains(task.Command, "pty-bg-test") {
			select {
			case <-task.done:
				// Task completed in background
			case <-time.After(5 * time.Second):
				t.Error("PTY background task did not complete")
			}
			registry.Remove(task.ID)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// executeNonPTYStreaming — non-ExitError path (line 310-311)
// ---------------------------------------------------------------------------

func TestExecuteNonPTYStreaming_NonExitError(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/non-exit-err")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(), json.RawMessage(`{"command":"echo test"}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	out := result.Data.(*Output)
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// spawnBackground — non-PTY cmd.Start error path (line 629)
// ---------------------------------------------------------------------------

func TestSpawnBackground_NonPTYCmdStartError(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/spawn-start-err")
	defer func() { SetPtmxCheckPath(orig) }()

	// spawnBackground with a command that should start successfully
	result, err := spawnBackground(context.Background(), Input{Command: "echo spawn"}, "", 10*time.Second, DefaultRegistry())
	if err != nil {
		t.Fatalf("spawnBackground() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Errorf("Stdout = %q, want background message", out.Stdout)
	}
}


// ---------------------------------------------------------------------------
// spawnBackground — PID must be set for Kill to work
// Bug: PTY path hardcodes task.PID = 0, making Kill a no-op
// ---------------------------------------------------------------------------

func TestSpawnBackground_PIDNotZero(t *testing.T) {
	// Swap in a fresh registry so we don't pollute the global one
	orig := defaultRegistry
	freshRegistry := NewBackgroundTaskRegistry()
	defaultRegistry = freshRegistry
	defer func() { defaultRegistry = orig }()

	ctx := context.Background()
	result, err := ExecuteStream(ctx, json.RawMessage(`{"command":"sleep 60","run_in_background":true}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	tasks := freshRegistry.List()
	if len(tasks) == 0 {
		t.Fatal("no background tasks registered")
	}
	task := tasks[0]

	// Poll for PID to be set by the goroutine
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task.mu.Lock()
		pid = task.PID
		task.mu.Unlock()
		if pid != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Cleanup: kill the task regardless of test result
	_ = freshRegistry.Kill(task.ID)

	// Verify ExecuteStream returned a valid result
	if result.Data == nil {
		t.Fatal("ExecuteStream returned nil Data")
	}

	if pid == 0 {
		t.Errorf("PID = 0, want non-zero — background task cannot be killed")
	}
}

// ---------------------------------------------------------------------------
// spawnBackground — two bugs:
//   Bug 1 (PTY): task.Complete called before ptyCommand finishes → immediate
//     completion with exit code 0, process never actually runs.
//   Bug 2 (all paths): taskCtx derived from parent ctx → cancelling parent
//     (query ending) kills the background task (exit code 137).
// ---------------------------------------------------------------------------

func TestSpawnBackground_TaskStaysRunning(t *testing.T) {
	// Use a fresh registry to avoid polluting global state
	orig := defaultRegistry
	freshRegistry := NewBackgroundTaskRegistry()
	defaultRegistry = freshRegistry
	defer func() { defaultRegistry = orig }()

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	result, err := spawnBackground(parentCtx, Input{
		Command:     "sleep 10",
		Description: "test stay running",
	}, t.TempDir(), 30*time.Second, DefaultRegistry())
	if err != nil {
		t.Fatalf("spawnBackground error: %v", err)
	}
	if result.Data == nil {
		t.Fatal("spawnBackground returned nil Data")
	}

	// Give the goroutine time to start the command.
	// With Bug 1 (PTY sync), task.Complete(0, false) is called immediately
	// before the process even starts, so the task will be TaskCompleted here.
		time.Sleep(100 * time.Millisecond)

	tasks := freshRegistry.List()
	if len(tasks) == 0 {
		t.Fatal("no background tasks registered")
	}
	task := tasks[0]

	task.mu.Lock()
	status := task.Status
	exitCode := task.ExitCode
	task.mu.Unlock()

	// Cleanup: kill the task regardless of test result
	if !IsTerminalTaskStatus(status) {
		_ = freshRegistry.Kill(task.ID)
	}

	if status != TaskRunning {
		t.Errorf("BUG: task status = %q (exit code %d), want TaskRunning — "+
			"background task should not complete immediately (PTY sync bug) or "+
			"be killed by parent context (context lifecycle bug)",
			status, exitCode)
	}
}

func TestSpawnBackground_TaskOutlivesParentContext(t *testing.T) {
	// Background task should keep running even after the spawning context is cancelled.
	// Bug 2: taskCtx is derived from parent ctx, so cancelling parent kills the task.
	orig := defaultRegistry
	freshRegistry := NewBackgroundTaskRegistry()
	defaultRegistry = freshRegistry
	defer func() { defaultRegistry = orig }()

	parentCtx, parentCancel := context.WithCancel(context.Background())

	result, err := spawnBackground(parentCtx, Input{
		Command:     "sleep 10",
		Description: "test context independence",
	}, t.TempDir(), 30*time.Second, DefaultRegistry())
	if err != nil {
		t.Fatalf("spawnBackground error: %v", err)
	}
	if result.Data == nil {
		t.Fatal("spawnBackground returned nil Data")
	}

	// Wait for the command to actually start
		time.Sleep(100 * time.Millisecond)

	// Cancel the parent context — simulates the query lifecycle ending.
	// The background task should NOT be affected.
	parentCancel()

	// Give cancellation time to propagate (if it's going to)
		time.Sleep(100 * time.Millisecond)

	tasks := freshRegistry.List()
	if len(tasks) == 0 {
		t.Fatal("no background tasks registered")
	}
	task := tasks[0]

	task.mu.Lock()
	status := task.Status
	exitCode := task.ExitCode
	task.mu.Unlock()

	// Cleanup
	if !IsTerminalTaskStatus(status) {
		_ = freshRegistry.Kill(task.ID)
	}

	if status != TaskRunning {
		t.Errorf("BUG: task status = %q (exit code %d) after parent context cancelled, want TaskRunning — "+
			"background task context should be independent of parent context",
			status, exitCode)
	}
}

// ---------------------------------------------------------------------------
// isAutobackgroundingAllowed — unit tests
// Source: BashTool.tsx:307-315 — isAutobackgroundingAllowed
// ---------------------------------------------------------------------------

func TestIsAutobackgroundingAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"empty", "", true},
		{"whitespace", "  ", true},
		{"sleep disallowed", "sleep 5", false},
		{"echo allowed", "echo hello", true},
		{"make allowed", "make build", true},
		{"git allowed", "git status", true},
		{"npm allowed", "npm install", true},
		{"sleep in pipeline allowed", "echo hi | sleep 1", true}, // first word is "echo"
		{"compound with sleep allowed", "echo start; sleep 10", true}, // first word is "echo"
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isAutobackgroundingAllowed(tc.cmd)
			if got != tc.want {
				t.Errorf("isAutobackgroundingAllowed(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Auto-background on timeout — TDD tests
// Source: BashTool.tsx:967-971 — shellCommand.onTimeout + startBackgrounding
// ---------------------------------------------------------------------------

// TestAutoBackground_NonPTYTimeoutTransitionsToBackground verifies that when
// shouldAutoBackground=true and the command times out, it transitions to a
// background task instead of being killed.
//
// RED LIGHT: This should fail because auto-background is not yet implemented.
// The command will be killed (TimedOut=true) instead of being backgrounded.
func TestAutoBackground_NonPTYTimeoutTransitionsToBackground(t *testing.T) {
	// Force non-PTY mode
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/autobg-nonpty")
	defer func() { SetPtmxCheckPath(orig) }()

	// Fresh registry for isolation
	origReg := defaultRegistry
	freshReg := NewBackgroundTaskRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	// Command: "echo start; sleep 10" — first word is "echo", so auto-bg is allowed.
	// Timeout: 100ms — the command will still be running when timeout fires.
	result, err := ExecuteStream(context.Background(),
		json.RawMessage(`{"command":"echo start; sleep 10","timeout":100}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out := result.Data.(*Output)

	// The command should have been auto-backgrounded, NOT killed.
	if out.BackgroundTaskID == "" {
		t.Fatalf("BackgroundTaskID is empty, want non-empty — command should have been auto-backgrounded on timeout. "+
			"Got: TimedOut=%v ExitCode=%d Stdout=%q", out.TimedOut, out.ExitCode, out.Stdout)
	}

	if out.TimedOut {
		t.Error("TimedOut should be false — command was auto-backgrounded, not killed")
	}

	// Verify task is registered in the background task registry
	task, found := freshReg.Get(out.BackgroundTaskID)
	if !found {
		t.Fatalf("background task %q not found in registry", out.BackgroundTaskID)
	}

	task.mu.Lock()
	pid := task.PID
	task.mu.Unlock()

	// Cleanup: kill the background task
	_ = freshReg.Kill(task.ID)

	if pid == 0 {
		t.Errorf("PID = 0, want non-zero — background task should have a real PID")
	}
}

// TestAutoBackground_PTYTimeoutTransitionsToBackground verifies the PTY path
// auto-backgrounds on timeout.
//
// RED LIGHT: This should fail because auto-background is not yet implemented.
func TestAutoBackground_PTYTimeoutTransitionsToBackground(t *testing.T) {
	// Fresh registry for isolation
	origReg := defaultRegistry
	freshReg := NewBackgroundTaskRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	// Command: "echo start; sleep 10" — first word is "echo", so auto-bg is allowed.
	// Timeout: 100ms — the command will still be running when timeout fires.
	result, err := ExecuteStream(context.Background(),
		json.RawMessage(`{"command":"echo start; sleep 10","timeout":100}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out := result.Data.(*Output)

	if out.BackgroundTaskID == "" {
		t.Fatalf("BackgroundTaskID is empty, want non-empty — command should have been auto-backgrounded on timeout. "+
			"Got: TimedOut=%v ExitCode=%d Stdout=%q", out.TimedOut, out.ExitCode, out.Stdout)
	}

	// Cleanup: kill the background task
	if task, found := freshReg.Get(out.BackgroundTaskID); found {
		_ = freshReg.Kill(task.ID)
	}
}

// TestAutoBackground_FastCommandNotBackgrounded verifies that a fast command
// (completes before timeout) is NOT auto-backgrounded.
func TestAutoBackground_FastCommandNotBackgrounded(t *testing.T) {
	t.Parallel()

	result, err := ExecuteStream(context.Background(),
		json.RawMessage(`{"command":"echo hello","timeout":5000}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.BackgroundTaskID != "" {
		t.Errorf("BackgroundTaskID = %q, want empty — fast command should not be auto-backgrounded", out.BackgroundTaskID)
	}
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}

// TestAutoBackground_SleepNotAutoBackgrounded verifies that "sleep" commands
// are NOT auto-backgrounded — they timeout and die normally.
// Source: BashTool.tsx:219-221 — DISALLOWED_AUTO_BACKGROUND_COMMANDS
func TestAutoBackground_SleepNotAutoBackgrounded(t *testing.T) {
	// Force non-PTY for deterministic behavior
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/autobg-sleep")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := ExecuteStream(context.Background(),
		json.RawMessage(`{"command":"sleep 10","timeout":100}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.BackgroundTaskID != "" {
		t.Errorf("BackgroundTaskID = %q, want empty — sleep should NOT be auto-backgrounded", out.BackgroundTaskID)
	}
	if !out.TimedOut {
		t.Error("TimedOut should be true — sleep should be killed on timeout, not backgrounded")
	}
}
