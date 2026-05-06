package bash

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// Integration tests: executeBash → StreamingOutput → ToolResult (full call chain)
//
// These test the real execution path: command runs, output is captured,
// spill may trigger, ReadContent/Cleanup is called, final ToolResult is correct.
// No mocks — real bash commands, real file I/O.
// ---------------------------------------------------------------------------

// TestIntegration_ExecuteSmallOutput_NoSpill tests the hot path:
// small command → all output in memory → no temp file → correct ToolResult.
func TestIntegration_ExecuteSmallOutput_NoSpill(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"command": "echo hello world"}`)
	result, err := Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "hello world") {
		t.Errorf("Stdout = %q, want to contain %q", out.Stdout, "hello world")
	}
	if out.TimedOut {
		t.Error("TimedOut = true, want false")
	}
}

// TestIntegration_ExecuteLargeOutput_SpillsToDisk tests the spill path:
// large command output (>8MB) triggers spill → output still readable →
// temp file cleaned up → ToolResult has capped output.
func TestIntegration_ExecuteLargeOutput_SpillsToDisk(t *testing.T) {
	t.Parallel()

	// awk generates 200K padded lines (~10MB) — exceeds 8MB spillThreshold
	raw := json.RawMessage(`{"command": "awk 'BEGIN{for(i=1;i<=200000;i++) printf \"%050d\\n\",i}'", "timeout": 60000}`)
	result, err := Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}

	// Output should be present (capped at MaxOutputSize but non-empty)
	if len(out.Stdout) == 0 {
		t.Error("Stdout is empty, expected output from large command")
	}

	// Verify the output starts with the first padded numbers
	if !strings.Contains(out.Stdout, "00000000000000000000000000000000000000000000000001") {
		t.Error("Stdout should contain the first padded line")
	}

	// Output should be capped (not the full 15MB)
	if len(out.Stdout) > MaxOutputSize+1000 {
		t.Errorf("Stdout len = %d, should be capped around %d", len(out.Stdout), MaxOutputSize)
	}
}

// TestIntegration_ExecuteSmallCommand_AllLinesPreserved tests boundary:
// output stays below threshold → all lines present → no truncation.
func TestIntegration_ExecuteSmallCommand_AllLinesPreserved(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"command": "seq 1 100"}`)
	result, err := Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}

	// Should contain all 100 lines (not truncated)
	lines := strings.Split(strings.TrimSpace(out.Stdout), "\n")
	if len(lines) != 100 {
		t.Errorf("got %d lines, want 100", len(lines))
	}
	if lines[0] != "1" {
		t.Errorf("first line = %q, want %q", lines[0], "1")
	}
	if lines[99] != "100" {
		t.Errorf("last line = %q, want %q", lines[99], "100")
	}
}

// TestIntegration_ExecuteProgressDuringSpill tests that progress callbacks
// work correctly even when output spills to disk.
// Verifies: lastLines rolling window, TotalBytes, TotalLines all update
// correctly during and after spill.
func TestIntegration_ExecuteProgressDuringSpill(t *testing.T) {
	t.Parallel()

	var lastProgress tool.ProgressUpdate
	tctx := &tool.ToolUseContext{
		OnProgress: func(u tool.ProgressUpdate) {
			lastProgress = u
		},
	}

	raw := json.RawMessage(`{"command": "awk 'BEGIN{for(i=1;i<=200000;i++) printf \"%050d\\n\",i}'", "timeout": 60000}`)
	result, err := Execute(context.Background(), raw, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data = %T, want *Output", result.Data)
	}

	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}

	// Progress should have been updated with large byte counts
	if lastProgress.TotalBytes < spillThreshold {
		t.Errorf("TotalBytes = %d, should be >= %d (spillThreshold)", lastProgress.TotalBytes, spillThreshold)
	}
	if lastProgress.TotalLines < 100000 {
		t.Errorf("TotalLines = %d, should be > 100K", lastProgress.TotalLines)
	}
	// Last lines should contain recent output
	if len(lastProgress.Lines) == 0 {
		t.Error("lastProgress.Lines is empty, expected recent lines")
	}
}

// TestIntegration_NoTempFilesLeaked verifies that temp files are cleaned up
// after both small and large command execution.
func TestIntegration_NoTempFilesLeaked(t *testing.T) {
	// NOT parallel — must run isolated to detect leaked temp files.

	// List temp files before
	before, _ := os.ReadDir(os.TempDir())
	beforeMap := make(map[string]bool)
	for _, f := range before {
		if strings.HasPrefix(f.Name(), "gbot-output-") {
			beforeMap[f.Name()] = true
		}
	}

	// Run a small command (no spill)
	raw := json.RawMessage(`{"command": "echo no-leak-small"}`)
	_, err := Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute() small error: %v", err)
	}

	// Run a large command that spills
	raw = json.RawMessage(`{"command": "awk 'BEGIN{for(i=1;i<=200000;i++) printf \"%050d\\n\",i}'", "timeout": 60000}`)
	_, err = Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute() large error: %v", err)
	}

	// List temp files after
	after, _ := os.ReadDir(os.TempDir())
	for _, f := range after {
		if strings.HasPrefix(f.Name(), "gbot-output-") && !beforeMap[f.Name()] {
			t.Errorf("temp file leaked: %s", f.Name())
		}
	}
}

// TestIntegration_ExitCodePreservedAfterSpill verifies exit code is correct
// even when the command fails and output is large enough to spill.
func TestIntegration_ExitCodePreservedAfterSpill(t *testing.T) {
	t.Parallel()

	// Command that generates large output AND exits non-zero
	raw := json.RawMessage(`{"command": "awk 'BEGIN{for(i=1;i<=200000;i++) printf \"%050d\\n\",i}'; exit 42", "timeout": 60000}`)
	result, err := Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data = %T, want *Output", result.Data)
	}

	if out.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", out.ExitCode)
	}
}
