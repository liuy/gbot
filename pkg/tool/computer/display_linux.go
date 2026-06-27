//go:build linux

package computer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// x11UnixGlob is the filesystem path pattern probed for X11 sockets.
// Overridable in tests via detectDisplay's glob indirection so the selection
// logic is unit-testable without a real /tmp/.X11-unix.
var x11UnixGlob = "/tmp/.X11-unix/X*"

// detectDisplay resolves the X11 DISPLAY on Linux with zero config. It is a
// PURE function (no xgb connection, no cua-driver shell-out) so it does not
// double-connect with X11Backend.ensureStarted (C9 fix): X11Backend opens the
// single xgb connection; this just tells it which display to open.
//
//  1. If os.Getenv("DISPLAY") != "" return it unchanged (caller knows best).
//  2. Glob /tmp/.X11-unix/X*, parse each to :N (or :N.M), return the first.
//  3. If none, return an error surfaced by ensureStarted as an actionable hint.
func detectDisplay() (string, error) {
	if d := os.Getenv("DISPLAY"); d != "" {
		return d, nil
	}
	candidates, err := listX11Displays()
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no X11 sockets found in /tmp/.X11-unix")
	}
	return candidates[0], nil
}

// listX11Displays globs /tmp/.X11-unix/X* and parses each socket name to a
// DISPLAY value (`:N` or `:N.M`). Returned sorted for deterministic selection.
func listX11Displays() ([]string, error) {
	matches, err := filepath.Glob(x11UnixGlob)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", x11UnixGlob, err)
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		// Strip the leading "X" — the socket filename is X<n> where <n> is
		// the display number, optionally with a screen suffix.
		if !strings.HasPrefix(base, "X") {
			continue
		}
		num := base[1:]
		if num == "" {
			continue
		}
		out = append(out, ":"+num)
	}
	sort.Strings(out)
	return out, nil
}
