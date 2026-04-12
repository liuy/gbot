package bash

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Shell environment snapshot
// Source: utils/bash/ShellSnapshot.ts — createAndSaveSnapshot
// ---------------------------------------------------------------------------

// EnvSnapshot represents a saved shell environment snapshot.
// Source: bashProvider.ts:65 — createAndSaveSnapshot
type EnvSnapshot struct {
	Path string
}

// SaveSnapshot creates a temp file with export statements for all current env vars.
// The snapshot is sourced before each command to preserve the user's shell environment.
//
// Source: ShellSnapshot.ts — creates a script that captures functions, aliases,
// and env vars. Simplified for Go: we export env vars only (functions/aliases
// are not portable across process boundaries in Go).
func SaveSnapshot() (*EnvSnapshot, error) {
	// Create temp file for snapshot
	f, err := os.CreateTemp("", "gbot-snapshot-*.sh")
	if err != nil {
		return nil, fmt.Errorf("create snapshot file: %w", err)
	}
	// Restrict permissions — snapshot contains all env vars including potential secrets
	_ = os.Chmod(f.Name(), 0600)

	var buf strings.Builder
	buf.WriteString("# gbot shell environment snapshot\n")
	buf.WriteString("shopt -s expand_aliases 2>/dev/null || true\n")

	// Export all current environment variables
	// Source: ShellSnapshot.ts — exports PATH, aliases, functions
	for _, envVar := range os.Environ() {
		// Split on first '=' only
		idx := strings.Index(envVar, "=")
		if idx < 0 {
			continue
		}
		key := envVar[:idx]
		value := envVar[idx+1:]

		// Quote the value for safe shell sourcing
		fmt.Fprintf(&buf, "export %s=%q\n", key, value)
	}

	if _, err := f.WriteString(buf.String()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("write snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("close snapshot: %w", err)
	}

	return &EnvSnapshot{Path: f.Name()}, nil
}

// Cleanup removes the snapshot file.
func (s *EnvSnapshot) Cleanup() {
	if s.Path != "" {
		_ = os.Remove(s.Path)
	}
}

// ---------------------------------------------------------------------------
// Session environment script
// Source: sessionEnvironment.ts:60 — getSessionEnvironmentScript()
// ---------------------------------------------------------------------------

// sessionEnvScript is the testable hook for SessionEnvScript.
// Returns "" because hooks are not yet implemented in gbot.
// Override in tests to exercise the buildCommand session env branch.
var sessionEnvScript = func() string {
	return "" // TODO: implement when gbot hooks are ready
}

// SessionEnvScript returns the session env script from hooks.
//
// Source: sessionEnvironment.ts:60 — getSessionEnvironmentScript().
// When hooks are implemented, this will return shell commands to set
// environment variables captured from session start hooks.
func SessionEnvScript() string {
	return sessionEnvScript()
}

// ---------------------------------------------------------------------------
// TMUX socket isolation
// Source: utils/tmuxSocket.ts — full isolation architecture
// ---------------------------------------------------------------------------

// TMUX socket isolation prevents Claude's bash commands from affecting
// the user's tmux sessions. Claude creates its own tmux socket (claude-<PID>),
// and all tmux commands operate on this isolated socket.
//
// Source: tmuxSocket.ts:1-27 — full documentation of the isolation architecture.
// Translated 1:1 from TS with lazy initialization and graceful degradation.

var (
	tmuxMu             sync.Mutex
	tmuxSocketName     string // "claude-<PID>", set lazily
	tmuxSocketPath     string // actual socket path, set after init
	tmuxServerPID      int    // tmux server PID
	tmuxIsInitializing bool
	tmuxAvailable      bool // checked once, cached
	tmuxAvailChecked   bool
	tmuxToolUsed       bool // whether Tmux tool has been used
)

// tmuxResult holds output from a tmux command execution.
type tmuxResult struct {
	Stdout string
	Stderr string
	Code   int
}

// execTmuxOverride is a test hook for mocking execTmux behavior.
// If the function returns (result, true), that result is used instead of
// executing the real tmux command. Returns (_, false) to use real behavior.
var execTmuxOverride func(args []string) (tmuxResult, bool)

// execTmux executes a tmux command with the given arguments.
// Source: tmuxSocket.ts:44-70 — execTmux
func execTmux(args []string) tmuxResult {
	// Test hook: allow overriding specific tmux calls
	if execTmuxOverride != nil {
		if r, ok := execTmuxOverride(args); ok {
			return r
		}
	}
	cmd := exec.Command("tmux", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return tmuxResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// getClaudeSocketName returns "claude-<PID>", created lazily.
// Source: tmuxSocket.ts:91-96
func getClaudeSocketName() string {
	if tmuxSocketName == "" {
		tmuxSocketName = fmt.Sprintf("claude-%d", os.Getpid())
	}
	return tmuxSocketName
}

// getClaudeSocketPath returns the socket path if initialized, else "".
// Source: tmuxSocket.ts:102-104
func getClaudeSocketPath() string {
	return tmuxSocketPath
}

// isSocketInitialized returns true if socket path and PID are set.
// Source: tmuxSocket.ts:118-120
func isSocketInitialized() bool {
	return tmuxSocketPath != "" && tmuxServerPID != 0
}

// getClaudeTmuxEnv returns TMUX env value for Claude's socket.
// Format: "socketPath,PID,0" or "" if not initialized.
//
// Source: tmuxSocket.ts:135-140 — getClaudeTmuxEnv()
// CRITICAL: This value overrides the user's TMUX env var in ALL child processes,
// ensuring any tmux command run via the Bash tool operates on Claude's socket.
func getClaudeTmuxEnv() string {
	if tmuxSocketPath == "" || tmuxServerPID == 0 {
		return ""
	}
	return fmt.Sprintf("%s,%d,0", tmuxSocketPath, tmuxServerPID)
}

// markTmuxToolUsed marks that the Tmux tool was used at least once.
// Source: tmuxSocket.ts:187-189
func markTmuxToolUsed() {
	tmuxToolUsed = true
}

// hasTmuxToolBeenUsed returns whether Tmux tool was used.
// Source: tmuxSocket.ts:195-197
func hasTmuxToolBeenUsed() bool {
	return tmuxToolUsed
}

// checkTmuxAvailable checks if tmux is installed. Result is cached.
// Source: tmuxSocket.ts:151-171
func checkTmuxAvailable() bool {
	if tmuxAvailChecked {
		return tmuxAvailable
	}
	cmd := exec.Command("which", "tmux")
	if err := cmd.Run(); err != nil {
		tmuxAvailable = false
	} else {
		tmuxAvailable = true
	}
	tmuxAvailChecked = true
	return tmuxAvailable
}

// ensureSocketInitialized creates Claude's tmux socket and detached session.
// Lazy — only initializes once. Safe to call multiple times.
//
// Source: tmuxSocket.ts:208-246 — ensureSocketInitialized()
// Translation note: TS uses async/await + initPromise for concurrent safety.
// Go uses mutex + isInitializing flag for the same guarantee.
func ensureSocketInitialized() error {
	tmuxMu.Lock()
	defer tmuxMu.Unlock()

	if isSocketInitialized() {
		return nil
	}

	if !checkTmuxAvailable() {
		return nil // graceful degradation
	}

	if tmuxIsInitializing {
		// Another goroutine is initializing — wait via polling
		// Source: tmuxSocket.ts:222-229 — wait for initPromise
		// The mutex is unlocked here, re-locked in the loop.
		// On break, mutex is held — the outer defer handles unlock.
		tmuxMu.Unlock()
		for {
			time.Sleep(10 * time.Millisecond)
			tmuxMu.Lock()
			if !tmuxIsInitializing {
				break
			}
			tmuxMu.Unlock()
		}
		return nil
	}

	tmuxIsInitializing = true
	defer func() { tmuxIsInitializing = false }()

	return doInitialize()
}

// doInitialize performs the actual tmux socket initialization.
// Source: tmuxSocket.ts:268-415 — doInitialize()
func doInitialize() error {
	socket := getClaudeSocketName()

	// Create new session with custom socket
	// Source: tmuxSocket.ts:282-295
	result := execTmux([]string{
		"-L", socket,
		"new-session", "-d", "-s", "base",
		"-e", "CLAUDE_CODE_SKIP_PROMPT_HISTORY=true",
	})
	if result.Code != 0 {
		// Session might already exist — check
		// Source: tmuxSocket.ts:296-311
		check := execTmux([]string{"-L", socket, "has-session", "-t", "base"})
		if check.Code != 0 {
			return fmt.Errorf("tmux new-session: %s", result.Stderr)
		}
	}

	// Set global env var for all sessions on this socket
	// Source: tmuxSocket.ts:322-329
	execTmux([]string{
		"-L", socket,
		"set-environment", "-g",
		"CLAUDE_CODE_SKIP_PROMPT_HISTORY", "true",
	})

	// Get socket path and server PID
	// Source: tmuxSocket.ts:348-374
	info := execTmux([]string{
		"-L", socket, "display-message", "-p", "#{socket_path},#{pid}",
	})
	if info.Code == 0 {
		parts := strings.Split(strings.TrimSpace(info.Stdout), ",")
		if len(parts) == 2 {
			path := parts[0]
			pid, err := strconv.Atoi(parts[1])
			if err == nil && path != "" {
				tmuxSocketPath = path
				tmuxServerPID = pid
				return nil
			}
		}
	}

	// Fallback: construct socket path from standard tmux location
	// Source: tmuxSocket.ts:376-400
	uid := os.Getuid()
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	fallbackPath := filepath.Join(tmpDir, fmt.Sprintf("tmux-%d", uid), socket)

	pidResult := execTmux([]string{
		"-L", socket, "display-message", "-p", "#{pid}",
	})
	if pidResult.Code == 0 {
		pid, err := strconv.Atoi(strings.TrimSpace(pidResult.Stdout))
		if err == nil {
			tmuxSocketPath = fallbackPath
			tmuxServerPID = pid
			return nil
		}
	}

	return fmt.Errorf("failed to get tmux socket info for %s", socket)
}

// killTmuxServer kills the Claude tmux server. Call on session end.
// Source: tmuxSocket.ts:252-266
func killTmuxServer() error {
	socket := getClaudeSocketName()
	result := execTmux([]string{"-L", socket, "kill-server"})
	if result.Code == 0 {
		return nil
	}
	return fmt.Errorf("kill-server exit %d: %s", result.Code, result.Stderr)
}

// getEnvironmentOverrides returns env var overrides for a command.
// It lazy-initializes the tmux socket when the tmux tool is used or
// the command contains "tmux".
//
// Source: bashProvider.ts:208-253 — getEnvironmentOverrides
// Translation note: TS checks USER_TYPE === 'ant' before initializing.
// Go version always initializes (gbot always runs as the tool user).
func getEnvironmentOverrides(cmd string) map[string]string {
	// TMUX SOCKET ISOLATION (DEFERRED):
	// Initialize Claude's tmux socket ONLY after tmux tool is used
	// or the command uses tmux. This defers startup cost.
	// Source: bashProvider.ts:220-227
	usesTmux := strings.Contains(cmd, "tmux")
	if hasTmuxToolBeenUsed() || usesTmux {
		if err := ensureSocketInitialized(); err != nil {
			// Log but don't fail — graceful degradation
			// Source: tmuxSocket.ts:237-244
			fmt.Fprintf(os.Stderr, "ensureSocketInitialized: %v\n", err)
		}
	}

	tmuxEnv := getClaudeTmuxEnv()
	if tmuxEnv == "" {
		return nil // user's TMUX preserved
	}

	return map[string]string{"TMUX": tmuxEnv}
}

// resetSocketState resets all socket state (for testing).
// Source: tmuxSocket.ts:418-427
func resetSocketState() {
	tmuxSocketName = ""
	tmuxSocketPath = ""
	tmuxServerPID = 0
	tmuxIsInitializing = false
	tmuxAvailable = false
	tmuxAvailChecked = false
	tmuxToolUsed = false
}
