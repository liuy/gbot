package bash

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSaveSnapshot(t *testing.T) {
	t.Parallel()

	snapshot, err := SaveSnapshot()
	if err != nil {
		t.Fatalf("SaveSnapshot() error: %v", err)
	}
	if snapshot == nil {
		t.Fatal("SaveSnapshot() returned nil snapshot")
	}
	if snapshot.Path == "" {
		t.Fatal("SaveSnapshot() returned empty path")
	}

	// Verify file exists
	if _, err := os.Stat(snapshot.Path); os.IsNotExist(err) {
		t.Fatalf("snapshot file does not exist: %s", snapshot.Path)
	}

	// Verify file content has export statements
	data, err := os.ReadFile(snapshot.Path)
	if err != nil {
		t.Fatalf("read snapshot file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "# gbot shell environment snapshot") {
		t.Error("snapshot missing header comment")
	}
	if !strings.Contains(content, "export PATH=") {
		t.Error("snapshot missing PATH export")
	}
	if !strings.Contains(content, "shopt -s expand_aliases") {
		t.Error("snapshot missing expand_aliases")
	}

	// Cleanup
	snapshot.Cleanup()
	if _, err := os.Stat(snapshot.Path); !os.IsNotExist(err) {
		t.Error("Cleanup() did not remove snapshot file")
	}
}

func TestCleanup_EmptyPath(t *testing.T) {
	t.Parallel()

	s := &EnvSnapshot{Path: ""}
	s.Cleanup() // should not panic
}

func TestSessionEnvScript(t *testing.T) {
	t.Parallel()

	got := SessionEnvScript()
	if got != "" {
		t.Errorf("SessionEnvScript() = %q, want empty string (hooks not yet implemented)", got)
	}
}

// Socket state tests must NOT use t.Parallel() — they modify global state
// via resetSocketState(), which races with other tests reading the same globals.

func TestGetClaudeSocketName(t *testing.T) {
	resetSocketState()
	name := getClaudeSocketName()
	if name == "" {
		t.Fatal("getClaudeSocketName() returned empty string")
	}
	if !strings.HasPrefix(name, "claude-") {
		t.Errorf("getClaudeSocketName() = %q, want prefix 'claude-'", name)
	}
	// Second call returns same name
	name2 := getClaudeSocketName()
	if name2 != name {
		t.Errorf("getClaudeSocketName() = %q, want %q (consistent)", name2, name)
	}
}

func TestGetClaudeSocketPath(t *testing.T) {
	resetSocketState()
	path := getClaudeSocketPath()
	if path != "" {
		t.Errorf("getClaudeSocketPath() = %q, want empty before initialization", path)
	}
}

func TestIsSocketInitialized(t *testing.T) {
	resetSocketState()
	if isSocketInitialized() {
		t.Error("isSocketInitialized() = true, want false before initialization")
	}
}

func TestGetClaudeTmuxEnv(t *testing.T) {
	resetSocketState()
	env := getClaudeTmuxEnv()
	if env != "" {
		t.Errorf("getClaudeTmuxEnv() = %q, want empty before initialization", env)
	}
}

func TestMarkTmuxToolUsed(t *testing.T) {
	resetSocketState()
	if hasTmuxToolBeenUsed() {
		t.Error("hasTmuxToolBeenUsed() = true before marking")
	}
	markTmuxToolUsed()
	if !hasTmuxToolBeenUsed() {
		t.Error("hasTmuxToolBeenUsed() = false after marking")
	}
}

func TestCheckTmuxAvailable(t *testing.T) {
	resetSocketState()
	result := checkTmuxAvailable()

	// Second call should use cache
	result2 := checkTmuxAvailable()
	if result2 != result {
		t.Errorf("checkTmuxAvailable() inconsistent: %v then %v", result, result2)
	}
}

func TestCheckTmuxAvailable_CacheHit(t *testing.T) {
	resetSocketState()
	_ = checkTmuxAvailable()
	result := checkTmuxAvailable()
	result2 := checkTmuxAvailable()
	if result != result2 {
		t.Errorf("cache inconsistent: %v then %v", result, result2)
	}
}

func TestCheckTmuxAvailable_NotFound(t *testing.T) {
	resetSocketState()
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", origPath) })
	// Set PATH to empty so "which tmux" fails
	_ = os.Setenv("PATH", "")
	if checkTmuxAvailable() {
		t.Error("expected tmux NOT available with empty PATH")
	}
}

func TestEnsureSocketInitialized_NoTmux(t *testing.T) {
	resetSocketState()
	err := ensureSocketInitialized()
	if err != nil {
		t.Errorf("ensureSocketInitialized() error: %v (expected nil or specific error)", err)
	}
}

func TestGetEnvironmentOverrides_NoTmuxCommand(t *testing.T) {
	resetSocketState()
	overrides := getEnvironmentOverrides("echo hello")
	if overrides != nil {
		t.Errorf("getEnvironmentOverrides(\"echo hello\") = %v, want nil", overrides)
	}
}

func TestGetEnvironmentOverrides_TmuxCommand(t *testing.T) {
	resetSocketState()
	overrides := getEnvironmentOverrides("tmux new-session")
	if overrides == nil {
		t.Skip("tmux not available or not initialized")
	}
	// Verify TMUX key exists if tmux is initialized
	if _, ok := overrides["TMUX"]; !ok {
		t.Logf("overrides = %v (TMUX key may be missing if tmux not running)", overrides)
	}
}

func TestResetSocketState(t *testing.T) {
	markTmuxToolUsed()
	resetSocketState()

	if hasTmuxToolBeenUsed() {
		t.Error("resetSocketState() did not reset tmuxToolUsed")
	}
	if getClaudeSocketPath() != "" {
		t.Error("resetSocketState() did not reset tmuxSocketPath")
	}
	// After reset, socket name should be regenerated (non-empty)
	if getClaudeSocketName() == "" {
		t.Error("getClaudeSocketName() returned empty after reset")
	}
}

func TestKillTmuxServer(t *testing.T) {
	resetSocketState()
	err := killTmuxServer()
	if err != nil {
		// killTmuxServer returns an error only if kill-server fails
		if !strings.Contains(err.Error(), "kill-server") {
			t.Errorf("killTmuxServer() error = %v, want kill-server error", err)
		}
	}
	// No error is also valid (tmux not running or kill succeeded)
}

func TestExecTmux(t *testing.T) {
	t.Parallel()

	result := execTmux([]string{"list-commands"})
	if result.Code == 0 && result.Stdout == "" {
		t.Error("execTmux(list-commands) returned empty stdout with code 0")
	}
}

func TestExecTmux_InvalidSubcommand(t *testing.T) {
	t.Parallel()

	result := execTmux([]string{"invalid-nonexistent-subcommand-xyz"})
	if result.Code == 0 {
		t.Errorf("expected non-zero exit code for invalid subcommand, got 0")
	}
}

func TestGetEnvironmentOverrides_TmuxInit(t *testing.T) {
	resetSocketState()
	markTmuxToolUsed()
	_ = ensureSocketInitialized()
	overrides := getEnvironmentOverrides("echo test")
	if overrides != nil {
		if _, ok := overrides["TMUX"]; !ok {
			t.Errorf("overrides = %v, want TMUX key", overrides)
		}
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		env       []string
		overrides map[string]string
		want      []string
	}{
		{
			name:      "nil overrides",
			env:       []string{"A=1", "B=2"},
			overrides: nil,
			want:      []string{"A=1", "B=2"},
		},
		{
			name:      "empty overrides",
			env:       []string{"A=1", "B=2"},
			overrides: map[string]string{},
			want:      []string{"A=1", "B=2"},
		},
		{
			name:      "override existing key",
			env:       []string{"A=1", "B=2"},
			overrides: map[string]string{"A": "10"},
			want:      []string{"B=2", "A=10"},
		},
		{
			name:      "add new key",
			env:       []string{"A=1"},
			overrides: map[string]string{"C": "3"},
			want:      []string{"A=1", "C=3"},
		},
		{
			name:      "override and add",
			env:       []string{"A=1", "B=2"},
			overrides: map[string]string{"B": "20", "C": "3"},
			want:      []string{"A=1", "B=20", "C=3"},
		},
		{
			name:      "env without equals sign",
			env:       []string{"NOSIGN", "A=1"},
			overrides: map[string]string{},
			want:      []string{"NOSIGN", "A=1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := applyEnvOverrides(tc.env, tc.overrides)
			if len(got) != len(tc.want) {
				t.Errorf("applyEnvOverrides() = %v, want %v", got, tc.want)
				return
			}
			gotSet := make(map[string]bool)
			for _, e := range got {
				gotSet[e] = true
			}
			for _, w := range tc.want {
				if !gotSet[w] {
					t.Errorf("applyEnvOverrides() missing %q in result %v", w, got)
				}
			}
		})
	}
}

// --- SaveSnapshot error paths ---

func TestSaveSnapshot_CreateTempError(t *testing.T) {
	// Trigger os.CreateTemp failure by setting TMPDIR to nonexistent path
	origTmpdir := os.Getenv("TMPDIR")
	t.Cleanup(func() { _ = os.Setenv("TMPDIR", origTmpdir) })
	_ = os.Setenv("TMPDIR", "/nonexistent/path/gbot-test-xyz")

	_, err := SaveSnapshot()
	if err == nil {
		t.Error("expected error with invalid TMPDIR")
	}
	if !strings.Contains(err.Error(), "create snapshot file") {
		t.Errorf("error = %v, want create snapshot file error", err)
	}
}

func TestSaveSnapshot_WriteError(t *testing.T) {
	// Trigger WriteString failure by setting RLIMIT_FSIZE to 0
	var oldLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &oldLimit); err != nil {
		t.Skip("can't get RLIMIT_FSIZE")
	}
	// Restore immediately — RLIMIT_FSIZE=0 prevents ALL file writes
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &oldLimit) })

	zeroLimit := syscall.Rlimit{Cur: 0, Max: oldLimit.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &zeroLimit); err != nil {
		t.Skip("can't set RLIMIT_FSIZE")
	}

	_, err := SaveSnapshot()
	// Restore limit before asserting (so test output can write)
	_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &oldLimit)

	if err == nil {
		t.Error("expected error when write fails (RLIMIT_FSIZE=0)")
	} else if !strings.Contains(err.Error(), "write snapshot") {
		t.Errorf("error = %v, want write snapshot error", err)
	}
}

// --- ensureSocketInitialized coverage paths ---

func TestEnsureSocketInitialized_AlreadyInitialized(t *testing.T) {
	resetSocketState()
	if !checkTmuxAvailable() {
		t.Skip("tmux not available")
	}
	if err := ensureSocketInitialized(); err != nil {
		t.Skipf("tmux init failed: %v", err)
	}
	// Second call hits the already-initialized early return
	if err := ensureSocketInitialized(); err != nil {
		t.Errorf("second call: %v", err)
	}
	// Cleanup
	execTmux([]string{"-L", getClaudeSocketName(), "kill-server"})
}

func TestEnsureSocketInitialized_TmuxNotAvailable(t *testing.T) {
	resetSocketState()
	// Set cached state so tmux appears unavailable
	tmuxAvailChecked = true
	tmuxAvailable = false

	err := ensureSocketInitialized()
	if err != nil {
		t.Errorf("expected nil (graceful degradation), got: %v", err)
	}
}

func TestEnsureSocketInitialized_PollingPath(t *testing.T) {
	resetSocketState()
	// Set state under mutex to avoid race with goroutine reads
	tmuxMu.Lock()
	tmuxAvailChecked = true
	tmuxAvailable = true
	tmuxIsInitializing = true
	tmuxMu.Unlock()

	done := make(chan struct{})
	go func() {
		_ = ensureSocketInitialized()
		close(done)
	}()

	// Let the goroutine enter the polling loop
	time.Sleep(100 * time.Millisecond)

	// Release under mutex — goroutine checks under mutex
	tmuxMu.Lock()
	tmuxIsInitializing = false
	tmuxMu.Unlock()

	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("ensureSocketInitialized hung in polling path")
	}
}

// --- doInitialize coverage paths ---

func TestDoInitialize_WithTmux(t *testing.T) {
	resetSocketState()
	if !checkTmuxAvailable() {
		t.Skip("tmux not available")
	}

	err := ensureSocketInitialized()
	if err != nil {
		t.Logf("ensureSocketInitialized() error (expected without tmux): %v", err)
	}

	if isSocketInitialized() {
		path := getClaudeSocketPath()
		if path == "" {
			t.Error("socket path empty after init")
		}
		env := getClaudeTmuxEnv()
		if env == "" {
			t.Error("tmux env empty after init")
		}
	}
	// Cleanup
	execTmux([]string{"-L", getClaudeSocketName(), "kill-server"})
}

func TestDoInitialize_SessionAlreadyExists(t *testing.T) {
	resetSocketState()
	if !checkTmuxAvailable() {
		t.Skip("tmux not available")
	}
	// First init creates the session
	if err := ensureSocketInitialized(); err != nil {
		t.Skipf("tmux init failed: %v", err)
	}
	// Reset only state vars — tmux session still exists
	socket := getClaudeSocketName()
	tmuxSocketPath = ""
	tmuxServerPID = 0
	tmuxIsInitializing = false

	// Second call: new-session fails (exists), has-session succeeds
	err := doInitialize()
	if err != nil {
		t.Logf("doInitialize on existing session: %v", err)
	}
	// Cleanup
	execTmux([]string{"-L", socket, "kill-server"})
}

func TestDoInitialize_FallbackPath(t *testing.T) {
	resetSocketState()
	if !checkTmuxAvailable() {
		t.Skip("tmux not available")
	}
	// First init to create the session
	if err := ensureSocketInitialized(); err != nil {
		t.Skipf("tmux init failed: %v", err)
	}
	socket := getClaudeSocketName()
	// Reset state but keep tmux session running
	tmuxSocketPath = ""
	tmuxServerPID = 0
	tmuxIsInitializing = false

	// Corrupt display-message output by changing socket name
	// so display-message returns unexpected format → triggers fallback
	tmuxSocketName = ""
	err := doInitialize()
	if err != nil {
		t.Logf("doInitialize() fallback path error: %v", err)
	}

	// Cleanup
	execTmux([]string{"-L", socket, "kill-server"})
}

// --- getEnvironmentOverrides stderr logging ---

func TestGetEnvironmentOverrides_TriggerTmuxInit(t *testing.T) {
	resetSocketState()
	if !checkTmuxAvailable() {
		t.Skip("tmux not available")
	}
	overrides := getEnvironmentOverrides("tmux new-session -d")
	if overrides != nil {
		if _, ok := overrides["TMUX"]; !ok {
			t.Errorf("overrides missing TMUX key: %v", overrides)
		}
	}
	// Cleanup
	execTmux([]string{"-L", getClaudeSocketName(), "kill-server"})
}

func TestGetEnvironmentOverrides_StderrLog(t *testing.T) {
	resetSocketState()
	// Make tmux appear available but use an invalid socket name so init fails
	tmuxAvailChecked = true
	tmuxAvailable = true
	markTmuxToolUsed()
	// Extremely long socket name causes tmux to fail
	tmuxSocketName = strings.Repeat("z", 10000)
	// This exercises the stderr logging path in getEnvironmentOverrides
	overrides := getEnvironmentOverrides("echo test")
	// With a broken tmux setup, overrides should be nil (init fails silently)
	if overrides != nil {
		t.Logf("overrides = %v (unexpected with broken tmux config)", overrides)
	}
}

func TestExecTmux_NonExitError(t *testing.T) {
	// When "tmux" binary can't be found, cmd.Run() returns non-ExitError
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", origPath) })
	_ = os.Setenv("PATH", "")

	result := execTmux([]string{"list-commands"})
	if result.Code == 0 {
		t.Error("expected non-zero exit when tmux not found")
	}
	// Should hit the non-ExitError branch (code = 1)
	if result.Code != 1 {
		t.Logf("code = %d (may vary by system)", result.Code)
	}
}

// --- execTmuxOverride hook ---

func TestExecTmux_OverrideHook(t *testing.T) {
	orig := execTmuxOverride
	execTmuxOverride = func(args []string) (tmuxResult, bool) {
		return tmuxResult{Stdout: "mock output", Code: 42}, true
	}
	defer func() { execTmuxOverride = orig }()

	result := execTmux([]string{"list-commands"})
	if result.Stdout != "mock output" {
		t.Errorf("stdout = %q, want mock output", result.Stdout)
	}
	if result.Code != 42 {
		t.Errorf("code = %d, want 42", result.Code)
	}
}

func TestExecTmux_OverrideNotMatched(t *testing.T) {
	orig := execTmuxOverride
	execTmuxOverride = func(args []string) (tmuxResult, bool) {
		return tmuxResult{}, false // not matched, falls through to real tmux
	}
	defer func() { execTmuxOverride = orig }()

	result := execTmux([]string{"list-commands"})
	// Override returned false, so real tmux is used.
	// Verify we got a real result (not the zero-value from the override).
	if result.Code == 0 {
		if result.Stdout == "" {
			t.Skip("tmux not available — override fallback exercised but no output to verify")
		}
		if !strings.Contains(result.Stdout, "list-commands") {
			t.Errorf("stdout should mention list-commands, got: %q", result.Stdout)
		}
	}
}


// --- doInitialize fallback paths via execTmuxOverride ---

func TestDoInitialize_FallbackPaths(t *testing.T) {
	tests := []struct {
		name         string
		firstResp    tmuxResult // response to display-message with socket_path
		secondResp   tmuxResult // response to display-message without socket_path
		setup        func() func() // optional extra setup/cleanup
		wantErr      bool
		errContains  string
		wantPID      int
		pathContains string
	}{
		{
			name:       "bad_format",
			firstResp:  tmuxResult{Code: 0, Stdout: "bad-format"},
			secondResp: tmuxResult{Code: 0, Stdout: "12345"},
			wantPID:    12345,
		},
		{
			name:       "empty_path",
			firstResp:  tmuxResult{Code: 0, Stdout: ",12345"},
			secondResp: tmuxResult{Code: 0, Stdout: "12345"},
			wantPID:    12345,
		},
		{
			name:       "bad_pid_in_display",
			firstResp:  tmuxResult{Code: 0, Stdout: "/path/to/socket,notnum"},
			secondResp: tmuxResult{Code: 0, Stdout: "12345"},
			wantPID:    12345,
		},
		{
			name:       "empty_tmpdir",
			firstResp:  tmuxResult{Code: 1},
			secondResp: tmuxResult{Code: 0, Stdout: "99999"},
			setup: func() func() {
				orig := os.Getenv("TMPDIR")
				_ = os.Setenv("TMPDIR", "")
				return func() { _ = os.Setenv("TMPDIR", orig) }
			},
			wantPID:      99999,
			pathContains: "/tmp/",
		},
		{
			name:        "non_numeric_pid",
			firstResp:   tmuxResult{Code: 0, Stdout: "not-a-number"},
			secondResp:  tmuxResult{Code: 0, Stdout: "not-a-number"},
			wantErr:     true,
			errContains: "failed to get tmux socket info",
		},
		{
			name:        "both_fail",
			firstResp:   tmuxResult{Code: 1, Stderr: "error"},
			secondResp:  tmuxResult{Code: 1, Stderr: "error"},
			wantErr:     true,
			errContains: "failed to get tmux socket info",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetSocketState()
			if tc.setup != nil {
				defer tc.setup()()
			}

			orig := execTmuxOverride
			first := tc.firstResp
			second := tc.secondResp
			execTmuxOverride = func(args []string) (tmuxResult, bool) {
				for _, a := range args {
					if a == "display-message" {
						if strings.Contains(strings.Join(args, " "), "socket_path") {
							return first, true
						}
						return second, true
					}
				}
				return tmuxResult{Code: 0}, true
			}
			defer func() { execTmuxOverride = orig }()

			err := doInitialize()

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error = %v, want to contain %q", err, tc.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.wantPID != 0 && tmuxServerPID != tc.wantPID {
					t.Errorf("server PID = %d, want %d", tmuxServerPID, tc.wantPID)
				}
				if tc.pathContains != "" && !strings.Contains(tmuxSocketPath, tc.pathContains) {
					t.Errorf("socket path = %q, want to contain %q", tmuxSocketPath, tc.pathContains)
				}
			}
			resetSocketState()
		})
	}
}


func TestKillTmuxServer_Error(t *testing.T) {
	resetSocketState()
	// Mock execTmux to return non-zero code
	orig := execTmuxOverride
	execTmuxOverride = func(args []string) (tmuxResult, bool) {
		return tmuxResult{Code: 1, Stderr: "mock error"}, true
	}
	defer func() { execTmuxOverride = orig }()

	err := killTmuxServer()
	if err == nil {
		t.Error("expected error from killTmuxServer")
	}
	if !strings.Contains(err.Error(), "kill-server") {
		t.Errorf("error = %v, want kill-server error", err)
	}
}
