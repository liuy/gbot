//go:build windows

package bash

import (
	"testing"
)

// Windows kill path tests are deferred — they require taskkill.exe and a
// real Windows process tree to be meaningful. The cross-compile gate
// (make build-windows) ensures the path compiles. Runtime coverage is
// listed in the plan under "What's testable only on Windows (deferred)".

func TestKillProcessTree_InvalidPID(t *testing.T) {
	// PID 999999 is essentially guaranteed to not exist.
	// killProcessTree should swallow the taskkill error and return nil.
	if err := killProcessTree(999999); err != nil {
		t.Errorf("killProcessTree(999999) error = %v, want nil", err)
	}
}
