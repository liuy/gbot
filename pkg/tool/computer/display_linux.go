//go:build linux

package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// detectDisplay resolves the X11 DISPLAY on Linux with zero config.
// Translate of the plan's Step 4 (no direct Hermes equivalent — Hermes
// assumes DISPLAY is already exported). Logic:
//  1. If os.Getenv("DISPLAY") != "" return it unchanged (caller knows best).
//  2. Glob /tmp/.X11-unix/X*, parse each to :N (or :N.M).
//  3. For each candidate, ask cua-driver `list_windows` (on_screen_only=true)
//     via listWindowsFn and pick the first that returns ≥1 window.
//
// If none have windows, returns an error. listWindowsFn is a package-level
// indirection so the selection logic is unit-testable without a live
// cua-driver binary.
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
	for _, display := range candidates {
		count, err := listWindowsFn(display)
		if err != nil {
			continue
		}
		if count >= 1 {
			return display, nil
		}
	}
	return "", fmt.Errorf("no X11 display with visible windows found in /tmp/.X11-unix")
}

// listWindowsFn returns the number of on-screen windows on the given DISPLAY,
// by spawning `cua-driver call list_windows '{"on_screen_only":true}'` with a
// short timeout. Overridable in tests.
var listWindowsFn = defaultListWindows

// defaultListWindows shells out to cua-driver to count on-screen windows on
// the given DISPLAY. It returns the count of windows in the
// structuredContent.windows array (the contract cua_backend.py:387-402 relies
// on), or 0 if the call failed or no structured payload came back.
func defaultListWindows(display string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd, err := resolveDriverCmd()
	if err != nil {
		return 0, err
	}

	c := exec.CommandContext(ctx, cmd, "call", "list_windows", `{"on_screen_only":true}`)
	envMap := childEnv(map[string]string{"DISPLAY": display})
	c.Env = mapToEnvSlice(envMap)
	out, err := c.Output()
	if err != nil {
		return 0, fmt.Errorf("cua-driver list_windows on %s: %w", display, err)
	}

	// cua-driver call prints JSON to stdout. Parse windows out of either the
	// top-level `windows` array (CLI shape) or structuredContent.windows
	// (MCP shape) — be lenient since the CLI surface differs slightly from
	// the MCP one.
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return 0, nil // unparseable output counts as "no windows"
	}
	if w, ok := parsed["windows"].([]any); ok {
		return len(w), nil
	}
	if sc, ok := parsed["structuredContent"].(map[string]any); ok {
		if w, ok := sc["windows"].([]any); ok {
			return len(w), nil
		}
	}
	return 0, nil
}

// listX11Displays globs /tmp/.X11-unix/X* and parses each socket name to a
// DISPLAY value (`:N` or `:N.M`). Returned sorted for deterministic probing.
// Translate of the plan's Step 4 parse spec.
func listX11Displays() ([]string, error) {
	matches, err := filepath.Glob("/tmp/.X11-unix/X*")
	if err != nil {
		return nil, fmt.Errorf("glob /tmp/.X11-unix/X*: %w", err)
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
