//go:build linux

package computer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListX11DisplaysParse verifies the /tmp/.X11-unix/X10 → :10 parse against
// a temp directory (no host dependency).
func TestListX11DisplaysParse(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"X0", "X1", "X10"} {
		if err := writeFakeSocket(filepath.Join(dir, name)); err != nil {
			t.Fatalf("write socket %s: %v", name, err)
		}
	}
	orig := x11UnixGlob
	x11UnixGlob = filepath.Join(dir, "X*")
	t.Cleanup(func() { x11UnixGlob = orig })

	got, err := listX11Displays()
	if err != nil {
		t.Fatalf("listX11Displays: %v", err)
	}
	want := []string{":0", ":1", ":10"}
	if len(got) != len(want) {
		t.Fatalf("listX11Displays = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("listX11Displays[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDetectDisplayHonorsEnv verifies DISPLAY env var short-circuits the glob.
func TestDetectDisplayHonorsEnv(t *testing.T) {
	t.Setenv("DISPLAY", ":99")
	got, err := detectDisplay()
	if err != nil {
		t.Fatalf("detectDisplay: %v", err)
	}
	if got != ":99" {
		t.Errorf("detectDisplay = %q, want :99", got)
	}
}

// TestDetectDisplayGlobSelectsFirst verifies that with no DISPLAY env, the
// first glob match (sorted) is returned. Covers the C9 simplification (pure
// glob, no window-count probe, no xgb connection).
func TestDetectDisplayGlobSelectsFirst(t *testing.T) {
	t.Setenv("DISPLAY", "")
	dir := t.TempDir()
	if err := writeFakeSocket(filepath.Join(dir, "X7")); err != nil {
		t.Fatalf("write socket: %v", err)
	}
	if err := writeFakeSocket(filepath.Join(dir, "X3")); err != nil {
		t.Fatalf("write socket: %v", err)
	}
	orig := x11UnixGlob
	x11UnixGlob = filepath.Join(dir, "X*")
	t.Cleanup(func() { x11UnixGlob = orig })

	got, err := detectDisplay()
	if err != nil {
		t.Fatalf("detectDisplay: %v", err)
	}
	if got != ":3" {
		t.Errorf("detectDisplay = %q, want :3 (sorted first)", got)
	}
}

// TestDetectDisplayNoSocketsError verifies the error path when no X11 sockets
// exist and DISPLAY is unset. ensureStarted wraps this into its actionable hint.
func TestDetectDisplayNoSocketsError(t *testing.T) {
	t.Setenv("DISPLAY", "")
	dir := t.TempDir()
	orig := x11UnixGlob
	x11UnixGlob = filepath.Join(dir, "X*")
	t.Cleanup(func() { x11UnixGlob = orig })

	_, err := detectDisplay()
	if err == nil {
		t.Fatal("detectDisplay: expected error when no sockets, got nil")
	}
	if msg := err.Error(); msg == "" {
		t.Errorf("error message empty")
	}
}

// writeFakeSocket creates a zero-byte file standing in for an X11 socket.
func writeFakeSocket(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}
