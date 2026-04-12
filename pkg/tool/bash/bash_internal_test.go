package bash

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		{"needs truncation", "hello world", 5, "hello\n... [output truncated]"},
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
			wantParts: []string{"shopt -u extglob", `eval "echo hello"`, "pwd -P >| /tmp/cwd.txt"},
		},
		{
			name:      "command with snapshot",
			cmd:       "echo hello",
			snapshot:  &EnvSnapshot{Path: "/tmp/snap.sh"},
			cwdFile:   "/tmp/cwd.txt",
			wantParts: []string{"source /tmp/snap.sh 2>/dev/null || true", "shopt -u extglob", `eval "echo hello"`, "pwd -P >| /tmp/cwd.txt"},
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
		_, _ = f.WriteString(tmpDir)
		_ = f.Close()
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
		_, _ = f.WriteString("/nonexistent/directory/path")
		_ = f.Close()
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
		_, _ = f.WriteString("  ")
		_ = f.Close()
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
		_ = f.Close()
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
	orig := ptmxCheckPath
	ptmxCheckPath = "/nonexistent/ptmx/gbot-test"
	defer func() { ptmxCheckPath = orig }()

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
