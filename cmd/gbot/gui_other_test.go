//go:build !windows

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPrintDaemonBanner(t *testing.T) {
	// printDaemonBanner writes to os.Stderr; capture it.
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	printDaemonBanner("8765")

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	got := buf.String()
	for _, want := range []string{
		"WUI server running at http://localhost:8765/",
		"Open the URL in a browser, or press Ctrl+C to exit.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q, got:\n%s", want, got)
		}
	}
}
