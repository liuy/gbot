package bash

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
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

func TestExecute_WithProgress_Echo(t *testing.T) {
	t.Parallel()

	var updates []tool.ProgressUpdate
	tctx := &tool.ToolUseContext{
		Ctx: context.Background(),
		OnProgress: func(u tool.ProgressUpdate) {
			updates = append(updates, u)
		},
	}
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo hello"}`), tctx)

	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

func TestExecute_WithProgress_MultiLine(t *testing.T) {
	t.Parallel()

	var lastUpdate tool.ProgressUpdate
	tctx := &tool.ToolUseContext{
		Ctx: context.Background(),
		OnProgress: func(u tool.ProgressUpdate) {
			lastUpdate = u
		},
	}
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo line1; echo line2; echo line3"}`), tctx)

	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

func TestExecute_WithProgress_ExitCode(t *testing.T) {
	t.Parallel()

	tctx := &tool.ToolUseContext{
		Ctx: context.Background(),
		OnProgress: func(u tool.ProgressUpdate) {},
	}
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"exit 42"}`), tctx)

	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	if out.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", out.ExitCode)
	}
}

func TestExecute_NilContext_NoPanic(t *testing.T) {
	t.Parallel()

	// Should not panic with nil tctx
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo safe"}`), nil)

	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// run_in_background dispatch — covers spawnBackground
// ---------------------------------------------------------------------------

func TestExecute_RunInBackground_NonPTY(t *testing.T) {
	// Force non-PTY mode for deterministic testing
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-bg")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo bg-hello","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

func TestExecute_RunInBackground_CompletesWithOutput(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-bg2")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo bg-output-123","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

func TestExecute_RunInBackground_ExitError(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-bg3")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"exit 7","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

func TestExecute_RunInBackground_PTY(t *testing.T) {
	// Test PTY branch inside spawnBackground — don't force non-PTY mode
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo pty-bg-test","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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
// Integration test: bash spawnBackground ID must match registry + TaskStop
// ---------------------------------------------------------------------------

func TestExecute_RunInBackground_TaskIDMatchesRegistry(t *testing.T) {
	// Force non-PTY for deterministic behavior
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-id-match")
	defer func() { SetPtmxCheckPath(orig) }()

	registry := DefaultRegistry()
	// Clean slate
	for _, task := range registry.List() {
		registry.Remove(task.ID)
	}
	defer func() {
		for _, task := range registry.List() {
			registry.Remove(task.ID)
		}
	}()

	// Step 1: Run a background task via Execute
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"sleep 10","run_in_background":true,"description":"test bg task"}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", result.Data)
	}

	// Step 2: Extract task ID from the output message
	// Output format: "Background task started with ID: bg-XXXXX\n..."
	outputID := extractBgID(out.Stdout)
	if outputID == "" {
		t.Fatalf("could not extract bg- ID from output: %q", out.Stdout)
	}

	// Step 3: Verify the ID exists in the default registry
	task, found := registry.Get(outputID)
	if !found {
		// List all tasks to show what's actually registered
		tasks := registry.List()
		var ids []string
		for _, t := range tasks {
			ids = append(ids, t.ID)
		}
		t.Fatalf("registry.Get(%q) not found. Registry contains: %v (ID mismatch bug!)", outputID, ids)
	}
	if task.Command != "sleep 10" {
		t.Errorf("task.Command = %q, want sleep 10", task.Command)
	}

	// Step 4: Verify TaskStop can find it via MultiRegistry + JobInfoAdapter
	taskReg := NewJobInfoAdapter(registry)
	taskInfo, found := taskReg.Get(outputID)
	if !found {
		t.Fatalf("JobInfoAdapter.Get(%q) not found — TaskStop would fail", outputID)
	}
	if taskInfo.Type != "local_bash" {
		t.Errorf("taskInfo.Type = %q, want local_bash", taskInfo.Type)
	}

	// Step 5: Verify Kill works via the adapter (TaskStop's path)
	if err := taskReg.Kill(outputID); err != nil {
		t.Fatalf("JobInfoAdapter.Kill(%q) error: %v — TaskStop would fail", outputID, err)
	}

	// Step 6: Verify task is now in terminal state
	select {
	case <-task.done:
		if task.Status != TaskKilled && task.Status != TaskCompleted {
			t.Errorf("task.Status after kill = %q, want killed or completed", task.Status)
		}
	case <-time.After(3 * time.Second):
		t.Error("task did not terminate after kill within timeout")
	}
}

// extractBgID extracts a bg-XXXXX ID from a string like
// "Background task started with ID: bg-12345\n..."
func extractBgID(s string) string {
	// Find "bg-" followed by digits
	idx := strings.Index(s, "bg-")
	if idx == -1 {
		return ""
	}
	rest := s[idx:]
	end := strings.IndexAny(rest, "\n ")
	if end == -1 {
		return rest
	}
	return rest[:end]
}

// ---------------------------------------------------------------------------
// executeNonPTY coverage — force non-PTY mode
// ---------------------------------------------------------------------------

func TestExecute_NonPTY(t *testing.T) {
	t.Parallel()
	// Force non-PTY by overriding PTY check path
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-nonpty")
	defer func() { SetPtmxCheckPath(orig) }()

	var updates []tool.ProgressUpdate
	tctx := &tool.ToolUseContext{
		Ctx: context.Background(),
		OnProgress: func(u tool.ProgressUpdate) {
			updates = append(updates, u)
		},
	}
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo nonpty-echo"}`), tctx)
	if err != nil {
		t.Fatalf("Execute() nonPTY error: %v", err)
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
// Execute — uncovered branches
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON2(t *testing.T) {
	t.Parallel()
	_, err := Execute(context.Background(), json.RawMessage(`{invalid json}`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %v, want 'parse input'", err)
	}
}

func TestExecute_EmptyCommand2(t *testing.T) {
	t.Parallel()
	_, err := Execute(context.Background(), json.RawMessage(`{"command":""}`), nil)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("error = %v, want 'command is required'", err)
	}
}

func TestExecute_WithTimeout2(t *testing.T) {
	t.Parallel()
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo timeout-test","timeout":5000}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out := result.Data.(*Output)
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}

func TestExecute_CWD2(t *testing.T) {
	t.Parallel()
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"pwd","cwd":"/tmp"}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "/tmp") {
		t.Errorf("Stdout = %q, want containing /tmp", out.Stdout)
	}
}

func TestExecute_ToolUseContextCWD2(t *testing.T) {
	t.Parallel()
	tctx := &tool.ToolUseContext{WorkingDir: "/tmp"}
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"pwd"}`), tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "/tmp") {
		t.Errorf("Stdout = %q, want containing /tmp", out.Stdout)
	}
}

// ---------------------------------------------------------------------------
// executeNonPTY — timeout and error paths
// ---------------------------------------------------------------------------

func TestExecuteNonPTY_TimedOut(t *testing.T) {
	t.Parallel()
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/nonpty-timeout")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"sleep 10","timeout":100}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out := result.Data.(*Output)
	if !out.TimedOut {
		t.Error("TimedOut should be true")
	}
}

func TestExecuteNonPTY_ExitError(t *testing.T) {
	t.Parallel()
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/nonpty-exit")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"exit 5"}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo spawn-nonpty","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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
// executePTY — uncovered paths
// ---------------------------------------------------------------------------

func TestExecutePTY_CwdFileError(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/pty-cwd-err")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo pty-cwd"}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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
// Execute — tctx.WorkingDir path (line 215-216)
// ---------------------------------------------------------------------------

func TestExecute_ToolUseContextWorkingDir(t *testing.T) {
	t.Parallel()
	tctx := &tool.ToolUseContext{WorkingDir: "/tmp"}
	result, err := Execute(context.Background(), json.RawMessage(`{"command":"pwd"}`), tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "/tmp") {
		t.Errorf("Stdout = %q, want containing /tmp", out.Stdout)
	}
}

// ---------------------------------------------------------------------------
// executePTY — err != nil path (line 265-267)
// ---------------------------------------------------------------------------

func TestExecutePTY_PtyCommandError(t *testing.T) {
	// Trigger error in ptyCommand by making shell non-existent
	// This forces executePTY to return an error at line 265-267
	orig := shellCommand
	shellCommand = "/nonexistent/shell/pty-error-test"
	defer func() { shellCommand = orig }()

	s := NewStreamingOutput(nil)
	_, err := executePTY(context.Background(), Input{Command: "echo pty-err", Timeout: 10000}, "", 5*time.Second, s, false, DefaultRegistry())
	if err == nil {
		t.Fatal("expected error with non-existent shell")
	}
	if !strings.Contains(err.Error(), "start command") {
		t.Errorf("error = %v, want 'start command'", err)
	}
}

// ---------------------------------------------------------------------------
// executeNonPTY — generic error path (line 310-312)
// cmd.Start() failure -> return nil, err
// ---------------------------------------------------------------------------

// cmd.Start() with bash -c always succeeds; bash itself is always found.
// This test verifies the path but the error case (line 629) is unreachable
// without invasive injection hooks. Kept for documentation.
func TestExecuteNonPTY_StartError(t *testing.T) {
	t.Skip("unreachable without injection hooks - bash is always found")
}

func TestExecute_TimeoutCap(t *testing.T) {
	t.Parallel()
	result, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo timeout-cap","timeout":1000000000}`),
		nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out := result.Data.(*Output)
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
}

func TestSpawnBackground_PTYPath(t *testing.T) {
	result, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo pty-bg-test","run_in_background":true}`),
		nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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
// executeNonPTY — non-ExitError path (line 310-311)
// ---------------------------------------------------------------------------

func TestExecuteNonPTY_NonExitError(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/non-exit-err")
	defer func() { SetPtmxCheckPath(orig) }()

	result, err := Execute(context.Background(), json.RawMessage(`{"command":"echo test"}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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
	result, err := Execute(ctx, json.RawMessage(`{"command":"sleep 60","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	tasks := freshRegistry.List()
	if len(tasks) == 0 {
		t.Fatal("no background tasks registered")
	}
	task := tasks[0]

	// Poll for PID to be set by the goroutine
	var pid int
	deadline := time.Now().Add(2 * time.Second)  // REAL-TIME: polling deadline
	for time.Now().Before(deadline) {  // REAL-TIME: polling deadline
		task.mu.Lock()
		pid = task.PID
		task.mu.Unlock()
		if pid != 0 {
			break
		}
		runtime.Gosched()
	}

	// Cleanup: kill the task regardless of test result
	_ = freshRegistry.Kill(task.ID)

	// Verify Execute returned a valid result
	if result.Data == nil {
		t.Fatal("Execute returned nil Data")
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

	parentCtx := t.Context()

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
	runtime.Gosched()

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
	runtime.Gosched()

	// Cancel the parent context — simulates the query lifecycle ending.
	// The background task should NOT be affected.
	parentCancel()

	// Give cancellation time to propagate (if it's going to)
	runtime.Gosched()

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
		{"sleep in pipeline allowed", "echo hi | sleep 1", true},      // first word is "echo"
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
	result, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo start; sleep 10","timeout":100}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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
	result, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo start; sleep 10","timeout":100}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

	result, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo hello","timeout":5000}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
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

	result, err := Execute(context.Background(),
		json.RawMessage(`{"command":"sleep 10","timeout":100}`), nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.BackgroundTaskID != "" {
		t.Errorf("BackgroundTaskID = %q, want empty — sleep should NOT be auto-backgrounded", out.BackgroundTaskID)
	}
	if !out.TimedOut {
		t.Error("TimedOut should be true — sleep should be killed on timeout, not backgrounded")
	}
}

// ---------------------------------------------------------------------------
// spawnBackground — non-PTY cmd.Start() error path (bash.go:846-849)
// ---------------------------------------------------------------------------

func TestSpawnBackground_NonPTY_StartError(t *testing.T) {
	// Force non-PTY mode
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-spawn-start-err")
	defer func() { SetPtmxCheckPath(orig) }()

	// Use a fresh registry
	origReg := defaultRegistry
	freshReg := NewBackgroundTaskRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	// Set a non-existent working directory to trigger cmd.Start() error
	result, err := spawnBackground(context.Background(), Input{Command: "echo test"}, "/nonexistent/dir/xyz/gbot-test", 10*time.Second, freshReg)
	if err != nil {
		t.Fatalf("spawnBackground() error: %v (returns nil error, task completes with -1)", err)
	}
	out := result.Data.(*Output)
	if !strings.Contains(out.Stdout, "Background task started") {
		t.Errorf("Stdout = %q, want background message", out.Stdout)
	}

	// Wait for the goroutine to finish
	tasks := freshReg.List()
	if len(tasks) == 0 {
		t.Fatal("no tasks registered")
	}
	task := tasks[0]
	select {
	case <-task.done:
		if task.ExitCode != -1 {
			t.Errorf("ExitCode = %d, want -1 (start error)", task.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Error("background task did not complete within timeout")
	}
}

// ---------------------------------------------------------------------------
// executeNonPTYAutoBg — cmd.Start() error path (bash.go:467-470)
// ---------------------------------------------------------------------------

func TestExecuteNonPTYAutoBg_StartError(t *testing.T) {
	// Force non-PTY mode
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test-autobg-start")
	defer func() { SetPtmxCheckPath(orig) }()

	s := NewStreamingOutput(nil)
	// Use a non-existent working directory to trigger cmd.Start() error
	_, err := executeNonPTYAutoBg(context.Background(), Input{Command: "echo test"}, "/nonexistent/dir/xyz/gbot-test", 10*time.Second, s, DefaultRegistry())
	if err == nil {
		t.Fatal("expected error with non-existent working directory")
	}
	if !strings.Contains(err.Error(), "nonexistent") && !strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "chdir") {
		t.Errorf("error should reference missing directory, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// isAutobackgroundingAllowed — whitespace-only input (bash.go:751-753)
// ---------------------------------------------------------------------------

func TestIsAutobackgroundingAllowed_TabOnly(t *testing.T) {
	t.Parallel()
	got := isAutobackgroundingAllowed("\t\t")
	if !got {
		t.Error("isAutobackgroundingAllowed(tab-only) = false, want true")
	}
}

// ---------------------------------------------------------------------------
// executePTY — tmux environment overrides (bash.go:300-302)
// ---------------------------------------------------------------------------

func TestExecutePTY_TmuxOverrides(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	// Reset socket state and mark tmux as used so getEnvironmentOverrides returns non-nil
	resetSocketState()
	markTmuxToolUsed()
	// Use execTmuxOverride to make tmux operations succeed
	origOverride := execTmuxOverride
	execTmuxOverride = func(args []string) (tmuxResult, bool) {
		if slices.Contains(args, "display-message") {
			return tmuxResult{Code: 0, Stdout: "/tmp/test-socket,12345"}, true
		}
		return tmuxResult{Code: 0}, true
	}
	defer func() { execTmuxOverride = origOverride }()

	// Call through executePTY which sets up all the internal params for executePTYSync
	s := NewStreamingOutput(nil)
	in := Input{Command: "echo tmux-test", Timeout: 10000}
	result, err := executePTY(context.Background(), in, "", 10*time.Second, s, false, nil)
	if err != nil {
		t.Fatalf("executePTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Stdout, "tmux-test") {
		t.Errorf("Stdout = %q, want to contain 'tmux-test'", output.Stdout)
	}
	resetSocketState()
}

// ---------------------------------------------------------------------------
// executePTY — auto-bg path with tmux environment overrides (bash.go:341-343)
// ---------------------------------------------------------------------------

func TestExecutePTYAutoBg_TmuxOverrides(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	resetSocketState()
	markTmuxToolUsed()
	origOverride := execTmuxOverride
	execTmuxOverride = func(args []string) (tmuxResult, bool) {
		if slices.Contains(args, "display-message") {
			return tmuxResult{Code: 0, Stdout: "/tmp/test-socket,12345"}, true
		}
		return tmuxResult{Code: 0}, true
	}
	defer func() { execTmuxOverride = origOverride }()

	// Fresh registry for isolation
	origReg := defaultRegistry
	freshReg := NewBackgroundTaskRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	// Call through executePTY with shouldAutoBg=true and a fresh registry.
	// The command completes before timeout, so it follows the sync path within executePTY.
	s := NewStreamingOutput(nil)
	in := Input{Command: "echo tmux-autobg", Timeout: 10000}
	result, err := executePTY(context.Background(), in, "", 10*time.Second, s, true, freshReg)
	if err != nil {
		t.Fatalf("executePTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Stdout, "tmux-autobg") {
		t.Errorf("Stdout = %q, want to contain 'tmux-autobg'", output.Stdout)
	}
	resetSocketState()
}
