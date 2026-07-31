//go:build !windows

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPrintGUIMessage(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	printGUIMessage("8765")

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	output := buf.String()

	for _, want := range []string{
		"Wails GUI is Windows-only",
		"http://localhost:8765/",
		"Ctrl+C to exit",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q\ngot:\n%s", want, output)
		}
	}
}
