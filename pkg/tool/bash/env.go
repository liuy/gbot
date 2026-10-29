package bash

import (
	"fmt"
	"os"
	"strings"
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
		before, after, ok := strings.Cut(envVar, "=")
		if !ok {
			continue
		}
		key := before
		value := after

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
// Hooks not implemented.
// Override in tests to exercise the buildCommand session env branch.
var sessionEnvScript = func() string {
	return ""
}

// SessionEnvScript returns the session env script from hooks.
//
// Source: sessionEnvironment.ts:60 — getSessionEnvironmentScript().
// When hooks are implemented, this will return shell commands to set
// environment variables captured from session start hooks.
func SessionEnvScript() string {
	return sessionEnvScript()
}
